package reconcile

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/monshunter/xgoal/internal/canonical"
)

const fingerprintSchema = "xgoal.failure-fingerprint/v1"

// FailureClass is the stable top-level reason used by deterministic reconciliation.
type FailureClass string

const (
	AgentUnavailable           FailureClass = "AGENT_UNAVAILABLE"
	AgentBlocked               FailureClass = "AGENT_BLOCKED"
	AgentFailed                FailureClass = "AGENT_FAILED"
	AgentProtocolInvalid       FailureClass = "AGENT_PROTOCOL_INVALID"
	AgentTimeout               FailureClass = "AGENT_TIMEOUT"
	AgentInterrupted           FailureClass = "AGENT_INTERRUPTED"
	EnvironmentPrepFailed      FailureClass = "ENVIRONMENT_PREP_FAILED"
	ScopeViolation             FailureClass = "SCOPE_VIOLATION"
	PatchEmpty                 FailureClass = "PATCH_EMPTY"
	PatchConflict              FailureClass = "PATCH_CONFLICT"
	ValidatorFailed            FailureClass = "VALIDATOR_FAILED"
	ValidatorUnavailable       FailureClass = "VALIDATOR_UNAVAILABLE"
	ReviewBlocked              FailureClass = "REVIEW_BLOCKED"
	GoalAmbiguous              FailureClass = "GOAL_AMBIGUOUS"
	PolicyBlocked              FailureClass = "POLICY_BLOCKED"
	NoMaterialProgress         FailureClass = "NO_MATERIAL_PROGRESS"
	InternalInvariantViolation FailureClass = "INTERNAL_INVARIANT_VIOLATION"
)

var failureClasses = map[FailureClass]struct{}{
	AgentUnavailable: {}, AgentBlocked: {}, AgentFailed: {}, AgentProtocolInvalid: {}, AgentTimeout: {}, AgentInterrupted: {},
	EnvironmentPrepFailed: {}, ScopeViolation: {}, PatchEmpty: {}, PatchConflict: {},
	ValidatorFailed: {}, ValidatorUnavailable: {}, ReviewBlocked: {}, GoalAmbiguous: {},
	PolicyBlocked: {}, NoMaterialProgress: {}, InternalInvariantViolation: {},
}

func (class FailureClass) Valid() bool {
	_, ok := failureClasses[class]
	return ok
}

// Failure is the complete semantic input of a failure fingerprint.
type Failure struct {
	Class                   FailureClass `json:"failure_class"`
	PrimaryError            string       `json:"normalized_primary_error"`
	ValidatorDefinitionHash string       `json:"validator_definition_hash"`
	BaseTree                string       `json:"base_tree"`
	ResultTree              string       `json:"result_tree"`
	GoalRevisionHash        string       `json:"goal_revision_hash"`
	RelevantConfigHash      string       `json:"relevant_config_hash"`
}

var (
	rfc3339Pattern      = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})\b`)
	unixTimePattern     = regexp.MustCompile(`\b1[5-9]\d{8}(?:\d{3}|\d{6}|\d{9})?\b`)
	loopbackPortPattern = regexp.MustCompile(`\b(localhost|127\.0\.0\.1|\[::1\]):\d{2,5}\b`)
	tempPathPattern     = regexp.MustCompile(`(?:/private)?/(?:var/)?folders/[A-Za-z0-9_./-]+|/tmp/[A-Za-z0-9_./-]+`)
	spacePattern        = regexp.MustCompile(`[ \t]+`)
)

// NormalizeError removes only known non-semantic runtime noise and duplicate lines.
func NormalizeError(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = rfc3339Pattern.ReplaceAllString(value, "<time>")
	value = unixTimePattern.ReplaceAllString(value, "<time>")
	value = loopbackPortPattern.ReplaceAllString(value, "$1:<port>")
	value = tempPathPattern.ReplaceAllString(value, "<tmp>")
	seen := make(map[string]struct{})
	lines := make([]string, 0)
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(spacePattern.ReplaceAllString(line, " "))
		if line == "" {
			continue
		}
		if _, exists := seen[line]; exists {
			continue
		}
		seen[line] = struct{}{}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func (failure Failure) Validate() error {
	if !failure.Class.Valid() {
		return errors.New("invalid failure class")
	}
	if strings.TrimSpace(failure.PrimaryError) == "" {
		return errors.New("primary error is empty")
	}
	for label, value := range map[string]string{
		"validator definition hash": failure.ValidatorDefinitionHash,
		"base tree":                 failure.BaseTree,
		"goal revision hash":        failure.GoalRevisionHash,
		"relevant config hash":      failure.RelevantConfigHash,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is empty", label)
		}
	}
	return nil
}

// Fingerprint returns a canonical hash after normalizing the primary error.
func Fingerprint(failure Failure) (string, error) {
	failure.PrimaryError = NormalizeError(failure.PrimaryError)
	if err := failure.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash("failure-fingerprint", fingerprintSchema, failure)
}

// Snapshot is the complete material-progress projection from the technical specification.
type Snapshot struct {
	AcceptedPatchHash          string `json:"accepted_patch_hash"`
	ValidatorOutcomeSetHash    string `json:"validator_outcome_set_hash"`
	ResolvedGateSetHash        string `json:"resolved_gate_set_hash"`
	OpenBlockingFindingSetHash string `json:"open_blocking_finding_set_hash"`
	PlanRevision               int64  `json:"plan_revision"`
	NewAuthoritativeEvidence   bool   `json:"new_authoritative_evidence"`
}

func MaterialProgress(previous, current Snapshot) bool {
	return current.AcceptedPatchHash != previous.AcceptedPatchHash ||
		current.ValidatorOutcomeSetHash != previous.ValidatorOutcomeSetHash ||
		current.ResolvedGateSetHash != previous.ResolvedGateSetHash ||
		current.OpenBlockingFindingSetHash != previous.OpenBlockingFindingSetHash ||
		current.PlanRevision != previous.PlanRevision ||
		current.NewAuthoritativeEvidence
}

func SnapshotHash(snapshot Snapshot) (string, error) {
	return canonical.Hash("material-progress", "xgoal.material-progress/v1", snapshot)
}

type Action string

const (
	RetryNewAttempt Action = "RETRY_NEW_ATTEMPT"
	Diagnose        Action = "DIAGNOSE"
	FixWorkItem     Action = "FIX_WORK_ITEM"
	SwitchStrategy  Action = "SWITCH_STRATEGY"
	Replan          Action = "REPLAN"
	WaitGate        Action = "WAIT_GATE"
	Quarantine      Action = "QUARANTINE"
	StopInvariant   Action = "STOP_INVARIANT"
)

type Input struct {
	Failure                 Failure
	Previous                Snapshot
	Current                 Snapshot
	SameFingerprintStrategy int64
	SideEffectsObserved     bool
}

type Decision struct {
	Action Action `json:"action"`
	Reason string `json:"reason"`
}

// Decide applies the v0.1 deterministic decision table.
func Decide(input Input) (Decision, error) {
	if err := input.Failure.Validate(); err != nil {
		return Decision{}, err
	}
	progress := MaterialProgress(input.Previous, input.Current)
	if input.SameFingerprintStrategy > 0 && !progress {
		return Decision{Action: Diagnose, Reason: "same fingerprint and strategy produced no material progress"}, nil
	}
	switch input.Failure.Class {
	case AgentUnavailable, AgentFailed, AgentProtocolInvalid, AgentTimeout, AgentInterrupted:
		if input.SideEffectsObserved {
			return Decision{Action: Diagnose, Reason: "agent failure may have produced side effects"}, nil
		}
		return Decision{Action: RetryNewAttempt, Reason: "agent failure is isolated and a new strategy attempt is allowed"}, nil
	case EnvironmentPrepFailed:
		return Decision{Action: WaitGate, Reason: "trusted environment preparation did not recover dependency"}, nil
	case ScopeViolation:
		return Decision{Action: Quarantine, Reason: "out-of-scope changes cannot enter integration"}, nil
	case PatchEmpty:
		return Decision{Action: Diagnose, Reason: "attempt produced no patch"}, nil
	case PatchConflict:
		return Decision{Action: FixWorkItem, Reason: "candidate must be rebased or repaired against current integration"}, nil
	case ValidatorFailed:
		return Decision{Action: FixWorkItem, Reason: "validator failure has current evidence for a fix"}, nil
	case AgentBlocked, ValidatorUnavailable, GoalAmbiguous, PolicyBlocked:
		return Decision{Action: WaitGate, Reason: "human decision or restored authority is required"}, nil
	case ReviewBlocked:
		return Decision{Action: FixWorkItem, Reason: "blocking review finding requires a fix or waiver gate"}, nil
	case NoMaterialProgress:
		return Decision{Action: SwitchStrategy, Reason: "strategy must change after explicit no-progress classification"}, nil
	case InternalInvariantViolation:
		return Decision{Action: StopInvariant, Reason: "project write loop must stop after invariant violation"}, nil
	default:
		return Decision{}, fmt.Errorf("unhandled failure class %q", input.Failure.Class)
	}
}

// SortedClasses exposes the protocol set for contract tests and status output.
func SortedClasses() []FailureClass {
	result := make([]FailureClass, 0, len(failureClasses))
	for class := range failureClasses {
		result = append(result, class)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
