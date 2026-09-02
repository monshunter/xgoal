// Package goalcompile validates untrusted Planner proposals before persistence.
package goalcompile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
)

const (
	ContractVersion = "xgoal.goal-contract/v1"
	PlanVersion     = "xgoal.plan-proposal/v1"
)

type Contract struct {
	Summary            string                `json:"summary"`
	Rationale          string                `json:"rationale"`
	InScope            []string              `json:"in_scope"`
	OutOfScope         []string              `json:"out_of_scope"`
	Constraints        []string              `json:"constraints"`
	AcceptanceCriteria []AcceptanceCriterion `json:"acceptance_criteria"`
	QualityAttributes  []string              `json:"quality_attributes"`
	HumanGates         []string              `json:"human_gates"`
	CompletionPolicy   CompletionPolicy      `json:"completion_policy"`
}

type AcceptanceCriterion struct {
	ID              string   `json:"id"`
	Statement       string   `json:"statement"`
	Validators      []string `json:"validators"`
	HumanAcceptance bool     `json:"human_acceptance"`
}

type CompletionPolicy struct {
	RequireAllRequiredItems   bool `json:"require_all_required_items"`
	RequireNoBlockingFindings bool `json:"require_no_blocking_findings"`
	RequireFinalValidation    bool `json:"require_final_validation"`
}

type Plan struct {
	Summary   string     `json:"summary"`
	WorkItems []PlanWork `json:"work_items"`
}

type PlanWork struct {
	ClientKey          string      `json:"client_key"`
	Title              string      `json:"title"`
	Objective          string      `json:"objective"`
	DependsOn          []string    `json:"depends_on"`
	ReadScope          []string    `json:"read_scope"`
	WriteScope         []string    `json:"write_scope"`
	AcceptanceCriteria []string    `json:"acceptance_criteria"`
	Validators         []string    `json:"validators"`
	RecommendedRole    domain.Role `json:"recommended_role"`
	Required           bool        `json:"required"`
}

type Compiled struct {
	Contract     Contract
	ContractHash string
	PlanHash     string
	WorkItems    []domain.WorkItem
	Dependencies []domain.WorkDependency
}

func Compile(goalID, revisionID, planID string, contract Contract, plan Plan, trustedValidators map[string]bool) (Compiled, error) {
	if !safeID(goalID) || !safeID(revisionID) || !safeID(planID) {
		return Compiled{}, errors.New("invalid compile identities")
	}
	contract = normalizeContract(contract)
	if err := contract.Validate(); err != nil {
		return Compiled{}, err
	}
	contractHash, err := canonical.Hash("goal-contract", ContractVersion, contract)
	if err != nil {
		return Compiled{}, err
	}
	works, dependencies, err := compilePlan(revisionID, planID, contract, plan, trustedValidators)
	if err != nil {
		return Compiled{}, err
	}
	planHash, err := canonical.Hash("plan-proposal", PlanVersion, normalizePlan(plan))
	if err != nil {
		return Compiled{}, err
	}
	return Compiled{Contract: contract, ContractHash: contractHash, PlanHash: planHash, WorkItems: works, Dependencies: dependencies}, nil
}

func (contract Contract) Validate() error {
	if blank(contract.Summary) || blank(contract.Rationale) || len(contract.InScope) == 0 || len(contract.OutOfScope) == 0 || len(contract.Constraints) == 0 || len(contract.AcceptanceCriteria) == 0 || len(contract.QualityAttributes) == 0 || len(contract.HumanGates) == 0 {
		return errors.New("Goal Contract has required semantic gaps")
	}
	if !contract.CompletionPolicy.RequireAllRequiredItems || !contract.CompletionPolicy.RequireNoBlockingFindings || !contract.CompletionPolicy.RequireFinalValidation {
		return errors.New("Goal Contract cannot weaken completion policy")
	}
	if !uniqueNonBlank(contract.InScope) || !uniqueNonBlank(contract.OutOfScope) || !uniqueNonBlank(contract.Constraints) || !uniqueNonBlank(contract.QualityAttributes) || !uniqueNonBlank(contract.HumanGates) {
		return errors.New("Goal Contract lists must be non-empty and unique")
	}
	out := make(map[string]struct{}, len(contract.OutOfScope))
	for _, value := range contract.OutOfScope {
		out[strings.ToLower(strings.TrimSpace(value))] = struct{}{}
	}
	for _, value := range contract.InScope {
		if _, exists := out[strings.ToLower(strings.TrimSpace(value))]; exists {
			return fmt.Errorf("Goal Contract scope contradiction %q", value)
		}
	}
	seen := make(map[string]struct{}, len(contract.AcceptanceCriteria))
	for _, criterion := range contract.AcceptanceCriteria {
		if !safeID(criterion.ID) || blank(criterion.Statement) || (len(criterion.Validators) == 0 && !criterion.HumanAcceptance) || !uniqueNonBlank(criterion.Validators) {
			return fmt.Errorf("acceptance criterion %q is invalid or unverifiable", criterion.ID)
		}
		if _, exists := seen[criterion.ID]; exists {
			return fmt.Errorf("duplicate acceptance criterion %q", criterion.ID)
		}
		seen[criterion.ID] = struct{}{}
	}
	return nil
}

func compilePlan(revisionID, planID string, contract Contract, plan Plan, trusted map[string]bool) ([]domain.WorkItem, []domain.WorkDependency, error) {
	if blank(plan.Summary) || len(plan.WorkItems) == 0 || len(plan.WorkItems) > 64 {
		return nil, nil, errors.New("Planner proposal has no bounded Work Graph")
	}
	criteria := make(map[string]bool, len(contract.AcceptanceCriteria))
	for _, criterion := range contract.AcceptanceCriteria {
		criteria[criterion.ID] = false
	}
	byKey := make(map[string]PlanWork, len(plan.WorkItems))
	ids := make(map[string]string, len(plan.WorkItems))
	for _, proposed := range plan.WorkItems {
		if !safeID(proposed.ClientKey) || blank(proposed.Title) || blank(proposed.Objective) || len(proposed.Title) > 200 || len(proposed.Objective) > 4000 ||
			len(proposed.ReadScope) == 0 || len(proposed.WriteScope) == 0 || len(proposed.AcceptanceCriteria) == 0 || len(proposed.Validators) == 0 || !proposed.RecommendedRole.Valid() {
			return nil, nil, fmt.Errorf("unbounded or incomplete Work Item %q", proposed.ClientKey)
		}
		if _, exists := byKey[proposed.ClientKey]; exists {
			return nil, nil, fmt.Errorf("duplicate client_key %q", proposed.ClientKey)
		}
		for _, scope := range append(append([]string(nil), proposed.ReadScope...), proposed.WriteScope...) {
			if !validScope(scope) {
				return nil, nil, fmt.Errorf("work %q has invalid scope %q", proposed.ClientKey, scope)
			}
		}
		for _, validator := range proposed.Validators {
			if !trusted[validator] {
				return nil, nil, fmt.Errorf("work %q references untrusted validator %q", proposed.ClientKey, validator)
			}
		}
		for _, criterion := range proposed.AcceptanceCriteria {
			if _, exists := criteria[criterion]; !exists {
				return nil, nil, fmt.Errorf("work %q references unknown criterion %q", proposed.ClientKey, criterion)
			}
			if proposed.Required {
				criteria[criterion] = true
			}
		}
		byKey[proposed.ClientKey] = proposed
		ids[proposed.ClientKey] = opaqueID("work", revisionID+"\x00"+proposed.ClientKey)
	}
	for criterion, covered := range criteria {
		if !covered {
			return nil, nil, fmt.Errorf("required criterion %q has no required Work Item", criterion)
		}
	}
	adjacency := make(map[string]map[string]bool, len(plan.WorkItems))
	var dependencies []domain.WorkDependency
	for _, proposed := range plan.WorkItems {
		for _, prerequisite := range proposed.DependsOn {
			if _, exists := byKey[prerequisite]; !exists || prerequisite == proposed.ClientKey {
				return nil, nil, fmt.Errorf("work %q has invalid dependency %q", proposed.ClientKey, prerequisite)
			}
			if adjacency[prerequisite] == nil {
				adjacency[prerequisite] = make(map[string]bool)
			}
			adjacency[prerequisite][proposed.ClientKey] = true
			dependencies = append(dependencies, domain.WorkDependency{FromID: ids[prerequisite], ToID: ids[proposed.ClientKey], Type: domain.DependencyHard})
		}
	}
	if cyclic(byKey, adjacency) {
		return nil, nil, errors.New("Planner Work Graph contains a cycle")
	}
	for leftIndex, left := range plan.WorkItems {
		for rightIndex := leftIndex + 1; rightIndex < len(plan.WorkItems); rightIndex++ {
			right := plan.WorkItems[rightIndex]
			if reachable(left.ClientKey, right.ClientKey, adjacency, map[string]bool{}) || reachable(right.ClientKey, left.ClientKey, adjacency, map[string]bool{}) {
				continue
			}
			if scopesMayOverlap(left.WriteScope, right.WriteScope) {
				return nil, nil, fmt.Errorf("unordered Work Items %q and %q have conflicting write scope", left.ClientKey, right.ClientKey)
			}
		}
	}
	works := make([]domain.WorkItem, 0, len(plan.WorkItems))
	for _, proposed := range plan.WorkItems {
		works = append(works, domain.WorkItem{
			ID: ids[proposed.ClientKey], PlanRevisionID: planID, State: domain.WorkPending,
			Title: proposed.Title, Objective: proposed.Objective,
			ReadScope: sorted(proposed.ReadScope), WriteScope: sorted(proposed.WriteScope),
			AcceptanceCriteria: sorted(proposed.AcceptanceCriteria), ValidatorIDs: sorted(proposed.Validators),
			RecommendedRole: proposed.RecommendedRole, Required: proposed.Required, Version: 1,
		})
	}
	sort.Slice(works, func(i, j int) bool { return works[i].ID < works[j].ID })
	sort.Slice(dependencies, func(i, j int) bool {
		return dependencies[i].FromID+dependencies[i].ToID < dependencies[j].FromID+dependencies[j].ToID
	})
	return works, dependencies, nil
}

func normalizeContract(value Contract) Contract {
	value.InScope, value.OutOfScope, value.Constraints = sorted(value.InScope), sorted(value.OutOfScope), sorted(value.Constraints)
	value.QualityAttributes, value.HumanGates = sorted(value.QualityAttributes), sorted(value.HumanGates)
	value.AcceptanceCriteria = append([]AcceptanceCriterion(nil), value.AcceptanceCriteria...)
	for index := range value.AcceptanceCriteria {
		value.AcceptanceCriteria[index].Validators = sorted(value.AcceptanceCriteria[index].Validators)
	}
	sort.Slice(value.AcceptanceCriteria, func(i, j int) bool { return value.AcceptanceCriteria[i].ID < value.AcceptanceCriteria[j].ID })
	return value
}

func normalizePlan(value Plan) Plan {
	value.WorkItems = append([]PlanWork(nil), value.WorkItems...)
	for index := range value.WorkItems {
		item := &value.WorkItems[index]
		item.DependsOn, item.ReadScope, item.WriteScope = sorted(item.DependsOn), sorted(item.ReadScope), sorted(item.WriteScope)
		item.AcceptanceCriteria, item.Validators = sorted(item.AcceptanceCriteria), sorted(item.Validators)
	}
	sort.Slice(value.WorkItems, func(i, j int) bool { return value.WorkItems[i].ClientKey < value.WorkItems[j].ClientKey })
	return value
}

func cyclic(nodes map[string]PlanWork, adjacency map[string]map[string]bool) bool {
	visiting, visited := make(map[string]bool), make(map[string]bool)
	var visit func(string) bool
	visit = func(node string) bool {
		if visiting[node] {
			return true
		}
		if visited[node] {
			return false
		}
		visiting[node] = true
		for next := range adjacency[node] {
			if visit(next) {
				return true
			}
		}
		delete(visiting, node)
		visited[node] = true
		return false
	}
	for node := range nodes {
		if visit(node) {
			return true
		}
	}
	return false
}

func reachable(from, to string, adjacency map[string]map[string]bool, seen map[string]bool) bool {
	if from == to {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	for next := range adjacency[from] {
		if reachable(next, to, adjacency, seen) {
			return true
		}
	}
	return false
}

func scopesMayOverlap(left, right []string) bool {
	for _, a := range left {
		for _, b := range right {
			ap, bp := literalPrefix(a), literalPrefix(b)
			if ap == "" || bp == "" || ap == bp || strings.HasPrefix(ap, bp+"/") || strings.HasPrefix(bp, ap+"/") {
				return true
			}
		}
	}
	return false
}

func literalPrefix(scope string) string {
	var result []string
	for _, segment := range strings.Split(strings.TrimPrefix(scope, "/"), "/") {
		if strings.Contains(segment, "*") {
			break
		}
		result = append(result, segment)
	}
	return strings.Join(result, "/")
}

func validScope(value string) bool {
	if !utf8.ValidString(value) || !strings.HasPrefix(value, "/") || value == "/" || strings.Contains(value, "\\") || strings.ContainsRune(value, '\x00') || path.Clean(value) != value {
		return false
	}
	for _, segment := range strings.Split(value[1:], "/") {
		if segment == "" || segment == "." || segment == ".." || segment == ".git" || (strings.Contains(segment, "**") && segment != "**") {
			return false
		}
	}
	return true
}

func opaqueID(prefix, value string) string {
	hash := sha256.Sum256([]byte(value))
	return prefix + "_" + hex.EncodeToString(hash[:12])
}

func sorted(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func uniqueNonBlank(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if blank(value) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func blank(value string) bool  { return strings.TrimSpace(value) == "" }
func safeID(value string) bool { return !blank(value) && !strings.ContainsAny(value, "/\\\r\n\x00") }
