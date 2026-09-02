package reconcile_test

import (
	"testing"

	"github.com/monshunter/xgoal/internal/reconcile"
)

func validFailure() reconcile.Failure {
	return reconcile.Failure{
		Class: reconcile.ValidatorFailed, PrimaryError: "exit code 1 at 2026-09-02T12:01:02Z on 127.0.0.1:43122 in /tmp/xgoal-123/work",
		ValidatorDefinitionHash: "validator", BaseTree: "base", ResultTree: "result",
		GoalRevisionHash: "goal", RelevantConfigHash: "config",
	}
}

func TestFingerprintNormalizesOnlyRuntimeNoise(t *testing.T) {
	first := validFailure()
	second := first
	second.PrimaryError = "exit   code 1 at 2026-09-02T13:14:15.999Z on 127.0.0.1:59999 in /tmp/other/work\nexit code 1 at 2026-09-02T13:14:15.999Z on 127.0.0.1:59999 in /tmp/other/work"
	firstHash, err := reconcile.Fingerprint(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := reconcile.Fingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash {
		t.Fatalf("runtime noise changed fingerprint: %s != %s", firstHash, secondHash)
	}
	second.PrimaryError = "exit code 2 at 2026-09-02T13:14:15Z on 127.0.0.1:59999 in /tmp/other/work"
	semanticHash, err := reconcile.Fingerprint(second)
	if err != nil {
		t.Fatal(err)
	}
	if semanticHash == firstHash {
		t.Fatal("semantic exit-code change did not change fingerprint")
	}
}

func TestMaterialProgressUsesOnlySpecifiedFields(t *testing.T) {
	base := reconcile.Snapshot{AcceptedPatchHash: "a", ValidatorOutcomeSetHash: "v", ResolvedGateSetHash: "g", OpenBlockingFindingSetHash: "f", PlanRevision: 1}
	if reconcile.MaterialProgress(base, base) {
		t.Fatal("identical snapshots must not count as progress")
	}
	changed := base
	changed.NewAuthoritativeEvidence = true
	if !reconcile.MaterialProgress(base, changed) {
		t.Fatal("new authoritative evidence must count as progress")
	}
}

func TestSameFingerprintWithoutProgressNeverRetriesSameStrategy(t *testing.T) {
	failure := validFailure()
	decision, err := reconcile.Decide(reconcile.Input{
		Failure: failure, Previous: reconcile.Snapshot{PlanRevision: 1}, Current: reconcile.Snapshot{PlanRevision: 1},
		SameFingerprintStrategy: 1, BudgetAvailable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action == reconcile.RetryNewAttempt {
		t.Fatalf("decision = %+v, mechanical retry must be forbidden", decision)
	}
}

func TestAgentFailureMayRetryOnlyWithoutSideEffectsAndWithBudget(t *testing.T) {
	failure := validFailure()
	failure.Class = reconcile.AgentProtocolInvalid
	decision, err := reconcile.Decide(reconcile.Input{Failure: failure, Current: reconcile.Snapshot{PlanRevision: 2}, BudgetAvailable: true})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != reconcile.RetryNewAttempt {
		t.Fatalf("action = %s, want retry", decision.Action)
	}
	decision, err = reconcile.Decide(reconcile.Input{Failure: failure, Current: reconcile.Snapshot{PlanRevision: 2}, BudgetAvailable: false})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action != reconcile.WaitBudget {
		t.Fatalf("action = %s, want wait budget", decision.Action)
	}
}
