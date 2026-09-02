package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/completion"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/evidence"
	"github.com/monshunter/xgoal/internal/protocol"
	finalreport "github.com/monshunter/xgoal/internal/report"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/monshunter/xgoal/internal/validator"
	"github.com/monshunter/xgoal/internal/workspace"
)

const humanAcceptanceReason = "human_acceptance"

func (engine *Engine) finalizeGoal(ctx context.Context, goal domain.Goal) error {
	revision, err := engine.store.GoalRevision(ctx, goal.ActiveRevisionID)
	if err != nil {
		return err
	}
	frozen, err := decodeFrozenContract(revision.ContractJSON)
	if err != nil {
		return err
	}
	status, err := engine.store.GoalStatus(ctx, goal.ID)
	if err != nil {
		return err
	}
	humanRequired := contractNeedsHumanAcceptance(frozen)
	humanGate, humanSatisfied := acceptedHumanGate(status.Gates)
	if humanRequired && !humanSatisfied {
		if !hasOpenHumanGate(status.Gates) {
			if err := engine.openHumanAcceptanceGate(ctx, goal, frozen); err != nil {
				return err
			}
		}
		if goal.State == domain.GoalRunning {
			return engine.store.UpdateGoalState(ctx, goal.ID, goal.Version, domain.GoalWaiting, event("GoalWaiting", "kernel", map[string]any{"reason": humanAcceptanceReason}))
		}
		return nil
	}
	if goal.State == domain.GoalRunning {
		if err := engine.store.UpdateGoalState(ctx, goal.ID, goal.Version, domain.GoalVerifying, event("GoalVerifying", "kernel", map[string]any{"reason": "required work completed"})); err != nil {
			return err
		}
		goal, err = engine.store.Goal(ctx, goal.ID)
		if err != nil {
			return err
		}
		status, err = engine.store.GoalStatus(ctx, goal.ID)
		if err != nil {
			return err
		}
	}
	if goal.State != domain.GoalVerifying {
		return nil
	}

	_, integration, err := engine.integration(ctx, goal.ID)
	if err != nil {
		return err
	}
	registry, err := validator.LoadRegistry(ctx, engine.repository, integration.Commit, "xgoal.yaml")
	if err != nil {
		return err
	}
	if registry.ConfigHash() != engine.configHash || frozen.ConfigHash != engine.configHash {
		return errors.New("final validation config differs from the frozen Goal Revision")
	}
	if _, err := engine.store.RecordValidatorRegistry(ctx, registry); err != nil {
		return err
	}
	attempt, err := lastSuccessfulAttempt(status.Attempts)
	if err != nil {
		return err
	}
	workspaceID, err := randomID("workspace_final")
	if err != nil {
		return err
	}
	validationWorkspace, err := engine.workspaces.Create(ctx, workspace.Spec{
		ID: workspaceID, AttemptID: attempt.ID, Kind: workspace.Validation,
		BaseCommit: integration.Commit, BaseTree: integration.Tree, ConfigHash: engine.configHash,
	})
	if err != nil {
		return err
	}
	if _, _, err := engine.store.RecordWorkspace(ctx, validationWorkspace); err != nil {
		return err
	}
	validationHandle, _, err := engine.prepareValidationEnvironment(ctx, validationWorkspace, revision)
	if err != nil {
		if _, evidenceErr := engine.recordEnvironmentFailureEvidence(ctx, goal.ID, revision, integration.Tree, "final-validation", err); evidenceErr != nil {
			return errors.Join(err, evidenceErr)
		}
		return err
	}
	defer engine.environment.Cleanup(context.Background(), validationHandle)
	validatorIDs, err := finalValidatorIDs(frozen, registry)
	if err != nil {
		return err
	}
	runs, err := engine.runValidators(ctx, registry, validationHandle, validationWorkspace, revision, attempt.ID, goal.ID, integration.Tree, validatorIDs, evidence.SetFinal)
	if err != nil {
		return err
	}
	evidenceIDs := make([]string, 0, len(runs)+1)
	for _, run := range runs {
		evidenceIDs = append(evidenceIDs, run.ID)
	}
	humanEvidenceID := ""
	if humanRequired {
		humanEvidenceID, err = engine.recordHumanAcceptanceEvidence(ctx, goal.ID, revision, integration.Tree, humanGate)
		if err != nil {
			return err
		}
		evidenceIDs = append(evidenceIDs, humanEvidenceID)
	}
	setID, err := randomID("evidence_set_final")
	if err != nil {
		return err
	}
	finalSet, err := evidence.NewSet(setID, evidence.SetFinal, revision.Hash, engine.configHash, integration.Tree, evidenceIDs, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := engine.store.CreateEvidenceSet(ctx, finalSet); err != nil {
		return err
	}
	findings, err := engine.store.GoalFindings(ctx, goal.ID)
	if err != nil {
		return err
	}
	report, facts, err := engine.buildFinalReport(frozen, revision, status, integration.Commit, integration.Tree, finalSet.ID, runs, humanEvidenceID, findings)
	if err != nil {
		return err
	}
	result, _, err := engine.finalizer.Finalize(ctx, goal.ID, goal.Version, facts, report, event("GoalCompleted", "kernel", map[string]any{"tree": integration.Tree, "evidence_set_id": finalSet.ID}))
	if err != nil {
		return err
	}
	if !result.Complete {
		return fmt.Errorf("completion predicate rejected finalization: %s", strings.Join(result.Reasons, "; "))
	}
	return nil
}

func finalValidatorIDs(frozen frozenContract, registry *validator.Registry) ([]string, error) {
	requested := make(map[string]bool)
	for _, criterion := range frozen.Contract.AcceptanceCriteria {
		for _, id := range criterion.Validators {
			requested[id] = true
		}
	}
	for _, definition := range registry.Definitions() {
		if definition.Required && contains(definition.Phases, "final") {
			requested[definition.ID] = true
		}
	}
	ids := make([]string, 0, len(requested))
	for id := range requested {
		definition, exists := registry.Definition(id)
		if !exists || !contains(definition.Phases, "final") {
			return nil, fmt.Errorf("acceptance validator %q is not registered for final validation", id)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return nil, errors.New("final validation set is empty")
	}
	return ids, nil
}

func (engine *Engine) buildFinalReport(frozen frozenContract, revision domain.GoalRevision, status sqlite.GoalStatus, commit, tree, setID string, runs []validationEvidence, humanEvidenceID string, findings []sqlite.ReviewFinding) (finalreport.Report, sqlite.CompletionFacts, error) {
	workByID := make(map[string]domain.WorkItem, len(status.WorkItems))
	works := make([]finalreport.WorkTrace, 0, len(status.WorkItems))
	var scopeValues []string
	for _, item := range status.WorkItems {
		workByID[item.ID] = item
		works = append(works, finalreport.WorkTrace{ID: item.ID, State: string(item.State), Title: item.Title, Required: item.Required, Authority: domain.AuthorityFact})
		scopeValues = append(scopeValues, item.WriteScope...)
	}
	attempts := make([]finalreport.AttemptTrace, 0, len(status.Attempts))
	for _, attempt := range status.Attempts {
		profile, exists := engine.profiles[attempt.AgentProfileID]
		work, workExists := workByID[attempt.WorkItemID]
		if !exists || !workExists {
			continue
		}
		attempts = append(attempts, finalreport.AttemptTrace{
			ID: attempt.ID, WorkID: attempt.WorkItemID, Role: string(work.RecommendedRole), Provider: profile.Adapter,
			State: string(attempt.State), PacketHash: attempt.PacketHash, ResultTree: attempt.ResultTree, Authority: domain.AuthorityFact,
		})
	}
	evidenceByValidator := make(map[string]string, len(runs))
	validators := make([]finalreport.ValidatorTrace, 0, len(runs))
	for _, run := range runs {
		evidenceByValidator[run.Validator] = run.ID
		validators = append(validators, finalreport.ValidatorTrace{
			ID: run.Validator, Command: run.Receipt.Argv, ReceiptHash: run.Hash, Result: string(run.Receipt.Result),
			Reproduction: append([]string(nil), run.Receipt.Argv...), Flaky: run.Flaky, Authority: domain.AuthorityDeterministic,
		})
	}
	criteria := make([]finalreport.CriterionTrace, 0, len(frozen.Contract.AcceptanceCriteria))
	completionCriteria := make([]completion.CriterionStatus, 0, len(frozen.Contract.AcceptanceCriteria))
	for _, criterion := range frozen.Contract.AcceptanceCriteria {
		ids := make([]string, 0, len(criterion.Validators)+1)
		for _, validatorID := range criterion.Validators {
			if id := evidenceByValidator[validatorID]; id != "" {
				ids = append(ids, id)
			}
		}
		if criterion.HumanAcceptance && humanEvidenceID != "" {
			ids = append(ids, humanEvidenceID)
		}
		if len(ids) == 0 {
			return finalreport.Report{}, sqlite.CompletionFacts{}, fmt.Errorf("criterion %q has no current final evidence", criterion.ID)
		}
		criteria = append(criteria, finalreport.CriterionTrace{ID: criterion.ID, Description: criterion.Statement, Status: "PASS", EvidenceIDs: ids, ValidatorIDs: criterion.Validators, Authority: domain.AuthorityDeterministic})
		completionCriteria = append(completionCriteria, completion.CriterionStatus{ID: criterion.ID, Satisfied: true, Current: true, TreeHash: tree})
	}
	gates := make([]finalreport.GateTrace, 0, len(status.Gates))
	for _, gate := range status.Gates {
		gates = append(gates, finalreport.GateTrace{ID: gate.ID, State: string(gate.State), Decision: string(gate.Decision), Reason: gate.ReasonCode, Authority: domain.AuthorityDecision})
	}
	findingTraces := make([]finalreport.FindingTrace, 0, len(findings))
	openBlocking := 0
	for _, finding := range findings {
		findingTraces = append(findingTraces, finalreport.FindingTrace{ID: finding.Finding.ID, Severity: string(finding.Finding.Severity), State: string(finding.State), Claim: finding.Finding.Claim, Basis: finding.Finding.Basis, Authority: domain.AuthorityInference})
		if finding.State == domain.FindingOpen && blockingSeverity(string(finding.Finding.Severity), engine.config.Review.BlockSeverities) {
			openBlocking++
		}
	}
	completedAt := time.Now().UTC()
	report := finalreport.Report{
		ProtocolVersion: finalreport.ProtocolVersion,
		Goal:            finalreport.GoalTrace{ID: status.Goal.ID, Raw: revision.RawGoal, Revision: revision.Revision, RevisionHash: revision.Hash, ConfigHash: frozen.ConfigHash, CreatedBy: frozen.CreatedBy, Authority: domain.AuthorityDecision},
		Work:            works, Attempts: attempts,
		Final:    finalreport.FinalTrace{Commit: commit, Tree: tree, EvidenceSetID: setID, Scope: uniqueSorted(scopeValues), Decisions: []finalreport.Statement{{Text: "Only independently validated Git state was promoted to the integration branch.", Authority: domain.AuthorityDeterministic}}, Authority: domain.AuthorityFact},
		Criteria: criteria, Validators: validators, Gates: gates, Findings: findingTraces,
		Execution: []finalreport.ExecutionMetric{
			{Name: "wall_time", Unit: "millisecond", Known: true, Value: completedAt.Sub(revision.FrozenAt).Milliseconds(), Authority: domain.AuthorityFact},
			{Name: "attempts", Unit: "attempt", Known: true, Value: int64(len(status.Attempts)), Authority: domain.AuthorityFact},
			{Name: "human_gates", Unit: "gate", Known: true, Value: int64(len(status.Gates)), Authority: domain.AuthorityFact},
		},
		Limitations: []finalreport.Statement{
			{Text: "v0.1 local runtime uses process and filesystem isolation (L0), not a hostile multi-tenant sandbox.", Authority: domain.AuthorityFact},
			{Text: "Provider Transport is available only to trusted Agent Profiles; it is distinct from project/tool network policy " + engine.config.Runtime.ProjectNetwork + ".", Authority: domain.AuthorityFact},
			{Text: "Project secret policy is " + engine.config.Runtime.ProjectSecrets + "; L0 cannot prove host credential isolation from a locally executed provider CLI.", Authority: domain.AuthorityFact},
		},
		Timestamps: finalreport.TimestampTrace{StartedAt: revision.FrozenAt.UTC().Format(time.RFC3339Nano), CompletedAt: completedAt.Format(time.RFC3339Nano), Authority: domain.AuthorityFact},
	}
	facts := sqlite.CompletionFacts{
		IntegrationTree: tree, ExpectedTree: tree, Criteria: completionCriteria,
		OpenBlockingFindings: openBlocking, ScopePolicyPassed: true, FinalValidationSetCurrent: true,
		FinalEvidenceSetID: setID, HumanAcceptanceRequired: contractNeedsHumanAcceptance(frozen), HumanAcceptanceSatisfied: !contractNeedsHumanAcceptance(frozen) || humanEvidenceID != "",
	}
	return report, facts, nil
}

func contractNeedsHumanAcceptance(frozen frozenContract) bool {
	for _, criterion := range frozen.Contract.AcceptanceCriteria {
		if criterion.HumanAcceptance {
			return true
		}
	}
	return false
}

func acceptedHumanGate(gates []domain.Gate) (domain.Gate, bool) {
	for _, gate := range gates {
		if gate.ReasonCode == humanAcceptanceReason && gate.State == domain.GateApproved && gate.Decision == domain.GateAllow {
			return gate, true
		}
	}
	return domain.Gate{}, false
}

func hasOpenHumanGate(gates []domain.Gate) bool {
	for _, gate := range gates {
		if gate.ReasonCode == humanAcceptanceReason && gate.State == domain.GateOpen {
			return true
		}
	}
	return false
}

func (engine *Engine) openHumanAcceptanceGate(ctx context.Context, goal domain.Goal, frozen frozenContract) error {
	id, err := randomID("gate_acceptance")
	if err != nil {
		return err
	}
	_, err = engine.store.CreateGate(ctx, sqlite.GateDraft{
		ID: id, GoalID: goal.ID, ReasonCode: humanAcceptanceReason,
		Facts: map[string]any{"criteria": frozen.Contract.AcceptanceCriteria}, Unknowns: []string{"human acceptance decision"},
		Options: []string{"allow", "deny"}, Recommendation: "review the final integration state and allow only if the human acceptance criteria are satisfied",
		Action: domain.ActionPublishArtifact, Scope: []string{"final-report/" + goal.ID}, ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
		MaxUses: 1, Revocable: true, Required: true,
	}, event("GateOpened", "kernel", map[string]any{"reason": humanAcceptanceReason}))
	return err
}

func (engine *Engine) recordHumanAcceptanceEvidence(ctx context.Context, goalID string, revision domain.GoalRevision, tree string, gate domain.Gate) (string, error) {
	id, err := randomID("evidence_human")
	if err != nil {
		return "", err
	}
	definitionHash, err := canonical.Hash("human-acceptance-definition", "v1", map[string]any{"reason": humanAcceptanceReason})
	if err != nil {
		return "", err
	}
	payloadHash, err := canonical.Hash("human-acceptance", "v1", map[string]any{"gate_id": gate.ID, "decision": gate.Decision, "decided_by": gate.DecidedBy, "decision_reason": gate.DecisionReason, "tree": tree})
	if err != nil {
		return "", err
	}
	record := evidence.Record{Evidence: protocol.Evidence{
		ProtocolVersion: protocol.EvidenceVersion, ID: id, Kind: "HUMAN_ACCEPTANCE", SubjectID: goalID,
		Producer: "human-gate/" + gate.ID, Authority: domain.AuthorityDecision,
		GoalRevisionHash: revision.Hash, ConfigHash: engine.configHash, TreeHash: tree,
		PayloadHash: payloadHash, State: domain.EvidenceCurrent, CreatedAt: gate.DecidedAt.UTC(),
	}, DefinitionHash: definitionHash, EnvironmentHash: engine.configHash}
	if err := engine.store.AppendEvidence(ctx, record); err != nil {
		return "", err
	}
	return id, nil
}

func lastSuccessfulAttempt(attempts []domain.Attempt) (domain.Attempt, error) {
	for index := len(attempts) - 1; index >= 0; index-- {
		if attempts[index].State == domain.AttemptSucceeded && attempts[index].ResultTree != "" {
			return attempts[index], nil
		}
	}
	return domain.Attempt{}, errors.New("final validation requires a successful promoted Attempt")
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func blockingSeverity(value string, configured []string) bool {
	for _, candidate := range configured {
		if strings.EqualFold(value, candidate) {
			return true
		}
	}
	return false
}
