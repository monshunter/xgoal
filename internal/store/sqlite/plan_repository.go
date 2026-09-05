package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
	basestore "github.com/monshunter/xgoal/internal/store"
)

const (
	goalRevisionSchema = "xgoal.goal-revision/v1"
	planGraphSchema    = "xgoal.plan-graph/v1"
)

// GoalRevisionDraft is the untrusted semantic input frozen into an immutable revision.
type GoalRevisionDraft struct {
	ID       string
	GoalID   string
	Revision int64
	RawGoal  string
	Contract any
}

// PlanRevisionDraft is a candidate graph persisted only after deterministic validation.
type PlanRevisionDraft struct {
	ID             string
	GoalRevisionID string
	Revision       int64
	WorkItems      []domain.WorkItem
	Dependencies   []domain.WorkDependency
}

// FreezeGoalRevision stores one immutable contract and activates it on a draft Goal.
func (s *Store) FreezeGoalRevision(
	ctx context.Context,
	draft GoalRevisionDraft,
	expectedGoalVersion int64,
	event EventInput,
) (domain.GoalRevision, error) {
	if !validIdempotencyLabel(draft.ID) || !validIdempotencyLabel(draft.GoalID) || draft.Revision <= 0 || strings.TrimSpace(draft.RawGoal) == "" {
		return domain.GoalRevision{}, errors.New("invalid goal revision draft")
	}
	contractJSON, contractHash, err := canonicalValue("goal-revision", goalRevisionSchema, draft.Contract)
	if err != nil {
		return domain.GoalRevision{}, fmt.Errorf("canonicalize goal revision: %w", err)
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return domain.GoalRevision{}, err
	}
	var result domain.GoalRevision
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		return s.freezeGoalRevisionTx(ctx, tx, draft, expectedGoalVersion, contractJSON, contractHash, prepared, &result)
	})
	if err != nil {
		return domain.GoalRevision{}, err
	}
	return result, nil
}

// GoalRevision returns an immutable persisted Goal contract.
func (s *Store) GoalRevision(ctx context.Context, id string) (domain.GoalRevision, error) {
	if id == "" {
		return domain.GoalRevision{}, errors.New("goal revision id is empty")
	}
	return readGoalRevision(ctx, s.db, id)
}

func readGoalRevision(ctx context.Context, queryer rowQueryer, id string) (domain.GoalRevision, error) {
	var revision domain.GoalRevision
	var frozenAt string
	err := queryer.QueryRowContext(ctx, `
SELECT id, goal_id, revision, raw_goal, contract_json, contract_hash, frozen_at
FROM goal_revisions
WHERE id = ?`, id).Scan(
		&revision.ID,
		&revision.GoalID,
		&revision.Revision,
		&revision.RawGoal,
		&revision.ContractJSON,
		&revision.Hash,
		&frozenAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GoalRevision{}, fmt.Errorf("goal revision %q: %w", id, basestore.ErrNotFound)
	}
	if err != nil {
		return domain.GoalRevision{}, fmt.Errorf("read goal revision %q: %w", id, err)
	}
	revision.FrozenAt, err = time.Parse(time.RFC3339Nano, frozenAt)
	if err != nil {
		return domain.GoalRevision{}, fmt.Errorf("parse goal revision %q frozen_at: %w", id, err)
	}
	return revision, nil
}

// CreatePlanRevision validates and atomically persists a draft DAG and all Work Items.
func (s *Store) CreatePlanRevision(ctx context.Context, draft PlanRevisionDraft, event EventInput) (domain.PlanRevision, error) {
	workItems, dependencies, graphHash, err := validateAndHashPlan(draft)
	if err != nil {
		return domain.PlanRevision{}, err
	}
	preparedPlanEvent, err := prepareEvent(event)
	if err != nil {
		return domain.PlanRevision{}, err
	}
	preparedWorkEvents := make(map[string]preparedEvent, len(workItems))
	for _, work := range workItems {
		prepared, err := prepareEvent(EventInput{
			Type:          "WorkItemCreated",
			ActorType:     event.ActorType,
			ActorID:       event.ActorID,
			CorrelationID: event.CorrelationID,
			Payload:       map[string]any{"plan_revision_id": draft.ID},
		})
		if err != nil {
			return domain.PlanRevision{}, err
		}
		preparedWorkEvents[work.ID] = prepared
	}

	var result domain.PlanRevision
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		return s.createPlanRevisionTx(ctx, tx, draft, workItems, dependencies, graphHash, preparedPlanEvent, preparedWorkEvents, &result)
	})
	if err != nil {
		return domain.PlanRevision{}, err
	}
	return result, nil
}

// ActivatePlanRevision atomically activates a valid draft and starts a ready Goal.
func (s *Store) ActivatePlanRevision(
	ctx context.Context,
	id string,
	expectedPlanVersion, expectedGoalVersion int64,
	event EventInput,
) (domain.PlanRevision, error) {
	if id == "" || expectedPlanVersion <= 0 || expectedGoalVersion <= 0 {
		return domain.PlanRevision{}, errors.New("invalid plan activation")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return domain.PlanRevision{}, err
	}
	var result domain.PlanRevision
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		return s.activatePlanRevisionTx(ctx, tx, id, expectedPlanVersion, expectedGoalVersion, prepared, &result)
	})
	if err != nil {
		return domain.PlanRevision{}, err
	}
	return result, nil
}

// PlanRevision returns one persisted Plan aggregate.
func (s *Store) PlanRevision(ctx context.Context, id string) (domain.PlanRevision, error) {
	if id == "" {
		return domain.PlanRevision{}, errors.New("plan revision id is empty")
	}
	return readPlanRevision(ctx, s.db, id)
}

func readPlanRevision(ctx context.Context, queryer rowQueryer, id string) (domain.PlanRevision, error) {
	var plan domain.PlanRevision
	err := queryer.QueryRowContext(ctx, `
SELECT id, goal_revision_id, revision, graph_hash, status, version
FROM plan_revisions
WHERE id = ?`, id).Scan(
		&plan.ID,
		&plan.GoalRevisionID,
		&plan.Revision,
		&plan.GraphHash,
		&plan.Status,
		&plan.Version,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PlanRevision{}, fmt.Errorf("plan revision %q: %w", id, basestore.ErrNotFound)
	}
	if err != nil {
		return domain.PlanRevision{}, fmt.Errorf("read plan revision %q: %w", id, err)
	}
	if !plan.Status.Valid() || plan.Version <= 0 {
		return domain.PlanRevision{}, fmt.Errorf("plan revision %q contains invalid persisted state", id)
	}
	return plan, nil
}

// WorkItem returns one current Work aggregate.
func (s *Store) WorkItem(ctx context.Context, id string) (domain.WorkItem, error) {
	if id == "" {
		return domain.WorkItem{}, errors.New("work item id is empty")
	}
	return readWorkItem(ctx, s.db, id)
}

// WorkItems returns the Plan's Work Items in stable ID order.
func (s *Store) WorkItems(ctx context.Context, planRevisionID string) ([]domain.WorkItem, error) {
	if planRevisionID == "" {
		return nil, errors.New("plan revision id is empty")
	}
	rows, err := s.db.QueryContext(ctx, workItemSelect+` WHERE plan_revision_id = ? ORDER BY id`, planRevisionID)
	if err != nil {
		return nil, fmt.Errorf("read work items for plan %q: %w", planRevisionID, err)
	}
	defer rows.Close()
	var items []domain.WorkItem
	for rows.Next() {
		item, err := scanWorkItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate work items for plan %q: %w", planRevisionID, err)
	}
	return items, nil
}

// UpdateWorkState applies a Work transition with CAS and its Event atomically.
func (s *Store) UpdateWorkState(
	ctx context.Context,
	id string,
	expectedVersion int64,
	state domain.WorkState,
	event EventInput,
) error {
	if id == "" || expectedVersion <= 0 || !state.Valid() {
		return errors.New("invalid work state update")
	}
	prepared, err := prepareEvent(event)
	if err != nil {
		return err
	}
	return s.withTransaction(ctx, func(tx *sql.Tx) error {
		work, err := readWorkItem(ctx, tx, id)
		if err != nil {
			return err
		}
		if work.Version != expectedVersion {
			return fmt.Errorf("work item %q: %w", id, basestore.ErrConflict)
		}
		if err := domain.ValidateWorkTransition(work.State, state); err != nil {
			return err
		}
		if state == domain.WorkReady {
			if err := ensureWorkCanBecomeReady(ctx, tx, work, s.source.Now()); err != nil {
				return err
			}
		}
		updated, err := tx.ExecContext(ctx, `
UPDATE work_items
SET state = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`, state, s.source.Now().UTC().Format(time.RFC3339Nano), id, expectedVersion)
		if err != nil {
			return fmt.Errorf("update work item %q: %w", id, err)
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return fmt.Errorf("read work update result: %w", err)
		}
		if affected != 1 {
			return fmt.Errorf("work item %q: %w", id, basestore.ErrConflict)
		}
		return s.appendEvent(ctx, tx, "work", id, prepared)
	})
}

func ensureWorkCanBecomeReady(ctx context.Context, tx *sql.Tx, work domain.WorkItem, now time.Time) error {
	var planStatus domain.PlanRevisionState
	var goalState domain.GoalState
	var activeRevisionID, planGoalRevisionID string
	if err := tx.QueryRowContext(ctx, `
SELECT plan.status, goal.state, goal.active_revision_id, plan.goal_revision_id
FROM plan_revisions plan
JOIN goal_revisions goal_revision ON goal_revision.id = plan.goal_revision_id
JOIN goals goal ON goal.id = goal_revision.goal_id
WHERE plan.id = ?`, work.PlanRevisionID).Scan(&planStatus, &goalState, &activeRevisionID, &planGoalRevisionID); err != nil {
		return fmt.Errorf("read plan state for work %q: %w", work.ID, err)
	}
	if planStatus != domain.PlanActive || goalState != domain.GoalRunning || activeRevisionID != planGoalRevisionID {
		return fmt.Errorf("work item %q goal or plan is not active for scheduling: %w", work.ID, basestore.ErrConflict)
	}
	var incompleteDependencies int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM work_dependencies dependency
JOIN work_items prerequisite ON prerequisite.id = dependency.from_id
WHERE dependency.to_id = ?
  AND dependency.dependency_type = ?
  AND prerequisite.state <> ?`, work.ID, domain.DependencyHard, domain.WorkCompleted).Scan(&incompleteDependencies); err != nil {
		return fmt.Errorf("read dependencies for work %q: %w", work.ID, err)
	}
	if incompleteDependencies != 0 {
		return fmt.Errorf("work item %q has %d incomplete dependencies: %w", work.ID, incompleteDependencies, basestore.ErrConflict)
	}
	var openRequiredGates int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM gates gate_record
JOIN plan_revisions plan ON plan.id = ?
JOIN goal_revisions goal_revision ON goal_revision.id = plan.goal_revision_id
WHERE gate_record.goal_id = goal_revision.goal_id
  AND `+gateBlocksExecution+`
  AND gate_record.required = 1
  AND (gate_record.work_item_id IS NULL OR gate_record.work_item_id = ?)`,
		work.PlanRevisionID,
		now.UTC().Format(time.RFC3339Nano),
		work.ID,
	).Scan(&openRequiredGates); err != nil {
		return fmt.Errorf("read required gates for work %q: %w", work.ID, err)
	}
	if openRequiredGates != 0 {
		return fmt.Errorf("work item %q has %d open required gates: %w", work.ID, openRequiredGates, basestore.ErrConflict)
	}
	return nil
}

const workItemSelect = `
SELECT id, plan_revision_id, state, title, objective_json, scope_json,
       acceptance_json, validator_ids_json, recommended_role, required, version
FROM work_items`

type sqlRows interface {
	Scan(...any) error
}

func readWorkItem(ctx context.Context, queryer rowQueryer, id string) (domain.WorkItem, error) {
	item, err := scanWorkItem(queryer.QueryRowContext(ctx, workItemSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkItem{}, fmt.Errorf("work item %q: %w", id, basestore.ErrNotFound)
	}
	return item, err
}

func scanWorkItem(row sqlRows) (domain.WorkItem, error) {
	var item domain.WorkItem
	var objectiveJSON, scopeJSON, acceptanceJSON, validatorsJSON []byte
	var required int
	err := row.Scan(
		&item.ID,
		&item.PlanRevisionID,
		&item.State,
		&item.Title,
		&objectiveJSON,
		&scopeJSON,
		&acceptanceJSON,
		&validatorsJSON,
		&item.RecommendedRole,
		&required,
		&item.Version,
	)
	if err != nil {
		return domain.WorkItem{}, err
	}
	var scopes planScopes
	if err := json.Unmarshal(objectiveJSON, &item.Objective); err != nil {
		return domain.WorkItem{}, fmt.Errorf("decode work item %q objective: %w", item.ID, err)
	}
	if err := json.Unmarshal(scopeJSON, &scopes); err != nil {
		return domain.WorkItem{}, fmt.Errorf("decode work item %q scopes: %w", item.ID, err)
	}
	if err := json.Unmarshal(acceptanceJSON, &item.AcceptanceCriteria); err != nil {
		return domain.WorkItem{}, fmt.Errorf("decode work item %q acceptance: %w", item.ID, err)
	}
	if err := json.Unmarshal(validatorsJSON, &item.ValidatorIDs); err != nil {
		return domain.WorkItem{}, fmt.Errorf("decode work item %q validators: %w", item.ID, err)
	}
	item.ReadScope = scopes.Read
	item.WriteScope = scopes.Write
	item.Required = required == 1
	if !item.State.Valid() || !item.RecommendedRole.Valid() || item.Version <= 0 || (required != 0 && required != 1) {
		return domain.WorkItem{}, fmt.Errorf("work item %q contains invalid persisted state", item.ID)
	}
	return item, nil
}

func insertWorkItem(ctx context.Context, tx *sql.Tx, work domain.WorkItem, now string) error {
	objectiveJSON, err := canonical.Marshal(work.Objective)
	if err != nil {
		return fmt.Errorf("canonicalize work item %q objective: %w", work.ID, err)
	}
	scopeJSON, err := canonical.Marshal(planScopes{Read: work.ReadScope, Write: work.WriteScope})
	if err != nil {
		return fmt.Errorf("canonicalize work item %q scopes: %w", work.ID, err)
	}
	acceptanceJSON, err := canonical.Marshal(work.AcceptanceCriteria)
	if err != nil {
		return fmt.Errorf("canonicalize work item %q acceptance: %w", work.ID, err)
	}
	validatorsJSON, err := canonical.Marshal(work.ValidatorIDs)
	if err != nil {
		return fmt.Errorf("canonicalize work item %q validators: %w", work.ID, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO work_items(
    id, plan_revision_id, state, title, objective_json, scope_json,
    acceptance_json, validator_ids_json, recommended_role, required,
    version, created_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		work.ID,
		work.PlanRevisionID,
		work.State,
		work.Title,
		objectiveJSON,
		scopeJSON,
		acceptanceJSON,
		validatorsJSON,
		work.RecommendedRole,
		boolInteger(work.Required),
		work.Version,
		now,
		now,
	); err != nil {
		return fmt.Errorf("insert work item %q: %w", work.ID, err)
	}
	return nil
}

type planScopes struct {
	Read  []string `json:"read"`
	Write []string `json:"write"`
}

type hashedPlanGraph struct {
	WorkItems    []hashedPlanWork        `json:"work_items"`
	Dependencies []domain.WorkDependency `json:"dependencies"`
}

type hashedPlanWork struct {
	ID                 string      `json:"id"`
	Title              string      `json:"title"`
	Objective          string      `json:"objective"`
	ReadScope          []string    `json:"read_scope"`
	WriteScope         []string    `json:"write_scope"`
	AcceptanceCriteria []string    `json:"acceptance_criteria"`
	ValidatorIDs       []string    `json:"validator_ids"`
	RecommendedRole    domain.Role `json:"recommended_role"`
	Required           bool        `json:"required"`
}

func validateAndHashPlan(draft PlanRevisionDraft) ([]domain.WorkItem, []domain.WorkDependency, string, error) {
	if !validIdempotencyLabel(draft.ID) || !validIdempotencyLabel(draft.GoalRevisionID) || draft.Revision <= 0 || len(draft.WorkItems) == 0 {
		return nil, nil, "", errors.New("invalid plan revision draft")
	}
	workItems := append([]domain.WorkItem(nil), draft.WorkItems...)
	sort.Slice(workItems, func(i, j int) bool { return workItems[i].ID < workItems[j].ID })
	workByID := make(map[string]domain.WorkItem, len(workItems))
	for index := range workItems {
		work := &workItems[index]
		if err := validateWorkDraft(*work, draft.ID); err != nil {
			return nil, nil, "", err
		}
		if _, exists := workByID[work.ID]; exists {
			return nil, nil, "", fmt.Errorf("duplicate work item id %q", work.ID)
		}
		work.ReadScope = sortedCopy(work.ReadScope)
		work.WriteScope = sortedCopy(work.WriteScope)
		work.AcceptanceCriteria = sortedCopy(work.AcceptanceCriteria)
		work.ValidatorIDs = sortedCopy(work.ValidatorIDs)
		workByID[work.ID] = *work
	}
	dependencies := append([]domain.WorkDependency(nil), draft.Dependencies...)
	sort.Slice(dependencies, func(i, j int) bool {
		if dependencies[i].FromID != dependencies[j].FromID {
			return dependencies[i].FromID < dependencies[j].FromID
		}
		return dependencies[i].ToID < dependencies[j].ToID
	})
	seenDependencies := make(map[string]struct{}, len(dependencies))
	adjacency := make(map[string][]string, len(workItems))
	indegree := make(map[string]int, len(workItems))
	for id := range workByID {
		indegree[id] = 0
	}
	for _, dependency := range dependencies {
		if !dependency.Type.Valid() || dependency.FromID == dependency.ToID {
			return nil, nil, "", fmt.Errorf("invalid dependency %s -> %s", dependency.FromID, dependency.ToID)
		}
		if _, exists := workByID[dependency.FromID]; !exists {
			return nil, nil, "", fmt.Errorf("dependency source %q does not exist", dependency.FromID)
		}
		if _, exists := workByID[dependency.ToID]; !exists {
			return nil, nil, "", fmt.Errorf("dependency target %q does not exist", dependency.ToID)
		}
		key := dependency.FromID + "\x00" + dependency.ToID
		if _, exists := seenDependencies[key]; exists {
			return nil, nil, "", fmt.Errorf("duplicate dependency %s -> %s", dependency.FromID, dependency.ToID)
		}
		seenDependencies[key] = struct{}{}
		adjacency[dependency.FromID] = append(adjacency[dependency.FromID], dependency.ToID)
		indegree[dependency.ToID]++
	}
	ready := make([]string, 0, len(workItems))
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	visited := 0
	for len(ready) > 0 {
		id := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		visited++
		for _, dependent := range adjacency[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
	}
	if visited != len(workItems) {
		return nil, nil, "", errors.New("plan revision dependency graph contains a cycle")
	}

	hashedWork := make([]hashedPlanWork, 0, len(workItems))
	for _, work := range workItems {
		hashedWork = append(hashedWork, hashedPlanWork{
			ID:                 work.ID,
			Title:              work.Title,
			Objective:          work.Objective,
			ReadScope:          work.ReadScope,
			WriteScope:         work.WriteScope,
			AcceptanceCriteria: work.AcceptanceCriteria,
			ValidatorIDs:       work.ValidatorIDs,
			RecommendedRole:    work.RecommendedRole,
			Required:           work.Required,
		})
	}
	graphHash, err := canonical.Hash("plan-graph", planGraphSchema, hashedPlanGraph{WorkItems: hashedWork, Dependencies: dependencies})
	if err != nil {
		return nil, nil, "", fmt.Errorf("hash plan graph: %w", err)
	}
	return workItems, dependencies, graphHash, nil
}

func validateWorkDraft(work domain.WorkItem, planID string) error {
	if !validIdempotencyLabel(work.ID) || work.PlanRevisionID != planID || work.State != domain.WorkPending || work.Version != 1 {
		return fmt.Errorf("invalid work item %q identity or initial state", work.ID)
	}
	if strings.TrimSpace(work.Title) == "" || strings.TrimSpace(work.Objective) == "" || !work.RecommendedRole.Valid() {
		return fmt.Errorf("work item %q requires title, objective, and role", work.ID)
	}
	if err := validateUniqueStrings(work.AcceptanceCriteria, "acceptance criteria"); err != nil {
		return fmt.Errorf("work item %q: %w", work.ID, err)
	}
	if err := validateUniqueStrings(work.ValidatorIDs, "validator ids"); err != nil {
		return fmt.Errorf("work item %q: %w", work.ID, err)
	}
	if len(work.ReadScope) == 0 || len(work.WriteScope) == 0 {
		return fmt.Errorf("work item %q requires read and write scopes", work.ID)
	}
	for _, pattern := range append(append([]string(nil), work.ReadScope...), work.WriteScope...) {
		if !validWorkScope(pattern) {
			return fmt.Errorf("work item %q has invalid scope %q", work.ID, pattern)
		}
	}
	return nil
}

func validateUniqueStrings(values []string, label string) error {
	if len(values) == 0 {
		return fmt.Errorf("%s are required", label)
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validIdempotencyLabel(value) {
			return fmt.Errorf("%s contain an invalid value", label)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contain duplicate %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validWorkScope(pattern string) bool {
	if !utf8.ValidString(pattern) || !strings.HasPrefix(pattern, "/") || pattern == "/" || strings.Contains(pattern, "\\") || strings.ContainsRune(pattern, '\x00') || strings.Contains(pattern[1:], "//") {
		return false
	}
	for _, segment := range strings.Split(pattern[1:], "/") {
		if segment == "" || segment == "." || segment == ".." || segment == ".git" || (strings.Contains(segment, "**") && segment != "**") {
			return false
		}
	}
	return true
}

func sortedCopy(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

// FreezeGoalRevision transaction body is shared by standalone operations and atomic planning publication.
func (s *Store) freezeGoalRevisionTx(ctx context.Context, tx *sql.Tx, draft GoalRevisionDraft, expectedGoalVersion int64, contractJSON []byte, contractHash string, prepared preparedEvent, result *domain.GoalRevision) error {
	goal, err := readGoal(ctx, tx, draft.GoalID)
	if err != nil {
		return err
	}
	if goal.Version != expectedGoalVersion {
		return fmt.Errorf("goal %q: %w", goal.ID, basestore.ErrConflict)
	}
	if err := domain.ValidateGoalTransition(goal.State, domain.GoalReady); err != nil {
		return err
	}
	if _, err := readGoalRevision(ctx, tx, draft.ID); err == nil {
		return fmt.Errorf("goal revision %q: %w", draft.ID, basestore.ErrAlreadyExists)
	} else if !errors.Is(err, basestore.ErrNotFound) {
		return err
	}
	var nextRevision int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(revision), 0) + 1
FROM goal_revisions
WHERE goal_id = ?`, draft.GoalID).Scan(&nextRevision); err != nil {
		return fmt.Errorf("allocate goal revision: %w", err)
	}
	if draft.Revision != nextRevision {
		return fmt.Errorf("goal revision %q number %d, want %d: %w", draft.ID, draft.Revision, nextRevision, basestore.ErrConflict)
	}
	frozenAt := s.source.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO goal_revisions(id, goal_id, revision, raw_goal, contract_json, contract_hash, frozen_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		draft.ID,
		draft.GoalID,
		draft.Revision,
		draft.RawGoal,
		contractJSON,
		contractHash,
		frozenAt.Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("insert goal revision %q: %w", draft.ID, err)
	}
	updated, err := tx.ExecContext(ctx, `
UPDATE goals
SET state = ?, active_revision_id = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`,
		domain.GoalReady,
		draft.ID,
		frozenAt.Format(time.RFC3339Nano),
		draft.GoalID,
		expectedGoalVersion,
	)
	if err != nil {
		return fmt.Errorf("activate goal revision %q: %w", draft.ID, err)
	}
	affected, err := updated.RowsAffected()
	if err != nil {
		return fmt.Errorf("read goal revision activation result: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("goal %q: %w", goal.ID, basestore.ErrConflict)
	}
	if err := s.appendEvent(ctx, tx, "goal", goal.ID, prepared); err != nil {
		return err
	}
	*result = domain.GoalRevision{
		ID:           draft.ID,
		GoalID:       draft.GoalID,
		Revision:     draft.Revision,
		RawGoal:      draft.RawGoal,
		ContractJSON: append([]byte(nil), contractJSON...),
		Hash:         contractHash,
		FrozenAt:     frozenAt,
	}
	return nil
}

// CreatePlanRevision transaction body is shared by standalone operations and atomic planning publication.
func (s *Store) createPlanRevisionTx(ctx context.Context, tx *sql.Tx, draft PlanRevisionDraft, workItems []domain.WorkItem, dependencies []domain.WorkDependency, graphHash string, preparedPlanEvent preparedEvent, preparedWorkEvents map[string]preparedEvent, result *domain.PlanRevision) error {
	goalRevision, err := readGoalRevision(ctx, tx, draft.GoalRevisionID)
	if err != nil {
		return err
	}
	goal, err := readGoal(ctx, tx, goalRevision.GoalID)
	if err != nil {
		return err
	}
	if goal.ActiveRevisionID != goalRevision.ID || goal.State == domain.GoalCompleted || goal.State == domain.GoalCancelled {
		return fmt.Errorf("goal revision %q is not active: %w", goalRevision.ID, basestore.ErrConflict)
	}
	if _, err := readPlanRevision(ctx, tx, draft.ID); err == nil {
		return fmt.Errorf("plan revision %q: %w", draft.ID, basestore.ErrAlreadyExists)
	} else if !errors.Is(err, basestore.ErrNotFound) {
		return err
	}
	var nextRevision int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(revision), 0) + 1
FROM plan_revisions
WHERE goal_revision_id = ?`, draft.GoalRevisionID).Scan(&nextRevision); err != nil {
		return fmt.Errorf("allocate plan revision: %w", err)
	}
	if draft.Revision != nextRevision {
		return fmt.Errorf("plan revision %q number %d, want %d: %w", draft.ID, draft.Revision, nextRevision, basestore.ErrConflict)
	}
	now := s.source.Now().UTC().Format(time.RFC3339Nano)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO plan_revisions(id, goal_revision_id, revision, graph_hash, status, version, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, 1, ?, ?)`,
		draft.ID,
		draft.GoalRevisionID,
		draft.Revision,
		graphHash,
		domain.PlanDraft,
		now,
		now,
	); err != nil {
		return fmt.Errorf("insert plan revision %q: %w", draft.ID, err)
	}
	if err := s.appendEvent(ctx, tx, "plan", draft.ID, preparedPlanEvent); err != nil {
		return err
	}
	for _, work := range workItems {
		if err := insertWorkItem(ctx, tx, work, now); err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "work", work.ID, preparedWorkEvents[work.ID]); err != nil {
			return err
		}
	}
	for _, dependency := range dependencies {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO work_dependencies(from_id, to_id, dependency_type)
VALUES (?, ?, ?)`, dependency.FromID, dependency.ToID, dependency.Type); err != nil {
			return fmt.Errorf("insert dependency %s -> %s: %w", dependency.FromID, dependency.ToID, err)
		}
	}
	*result = domain.PlanRevision{
		ID:             draft.ID,
		GoalRevisionID: draft.GoalRevisionID,
		Revision:       draft.Revision,
		GraphHash:      graphHash,
		Status:         domain.PlanDraft,
		Version:        1,
	}
	return nil
}

// ActivatePlanRevision transaction body is shared by standalone operations and atomic planning publication.
func (s *Store) activatePlanRevisionTx(ctx context.Context, tx *sql.Tx, id string, expectedPlanVersion, expectedGoalVersion int64, prepared preparedEvent, result *domain.PlanRevision) error {
	plan, err := readPlanRevision(ctx, tx, id)
	if err != nil {
		return err
	}
	if plan.Version != expectedPlanVersion {
		return fmt.Errorf("plan revision %q: %w", id, basestore.ErrConflict)
	}
	if err := domain.ValidatePlanRevisionTransition(plan.Status, domain.PlanActive); err != nil {
		return err
	}
	goalRevision, err := readGoalRevision(ctx, tx, plan.GoalRevisionID)
	if err != nil {
		return err
	}
	goal, err := readGoal(ctx, tx, goalRevision.GoalID)
	if err != nil {
		return err
	}
	if goal.Version != expectedGoalVersion || goal.ActiveRevisionID != plan.GoalRevisionID {
		return fmt.Errorf("goal %q: %w", goal.ID, basestore.ErrConflict)
	}
	if err := domain.ValidateGoalTransition(goal.State, domain.GoalRunning); err != nil {
		return err
	}
	now := s.source.Now().UTC().Format(time.RFC3339Nano)
	updated, err := tx.ExecContext(ctx, `
UPDATE plan_revisions
SET status = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ? AND status = ?`, domain.PlanActive, now, id, expectedPlanVersion, domain.PlanDraft)
	if err != nil {
		return fmt.Errorf("activate plan revision %q: %w", id, err)
	}
	affected, err := updated.RowsAffected()
	if err != nil {
		return fmt.Errorf("read plan activation result: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("plan revision %q: %w", id, basestore.ErrConflict)
	}
	updated, err = tx.ExecContext(ctx, `
UPDATE goals
SET state = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ?`, domain.GoalRunning, now, goal.ID, expectedGoalVersion)
	if err != nil {
		return fmt.Errorf("start goal %q: %w", goal.ID, err)
	}
	affected, err = updated.RowsAffected()
	if err != nil {
		return fmt.Errorf("read goal start result: %w", err)
	}
	if affected != 1 {
		return fmt.Errorf("goal %q: %w", goal.ID, basestore.ErrConflict)
	}
	if err := s.appendEvent(ctx, tx, "plan", id, prepared); err != nil {
		return err
	}
	if err := s.appendEvent(ctx, tx, "goal", goal.ID, prepared); err != nil {
		return err
	}
	plan.Status = domain.PlanActive
	plan.Version++
	*result = plan
	return nil
}
