package benchmark

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadValidatesFixtureAndFairComparisonIdentity(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "fixture")
	if err := os.Mkdir(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "input.txt"), []byte("fixed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash, err := HashFixture(fixture)
	if err != nil {
		t.Fatal(err)
	}
	suitePath := filepath.Join(root, "suite.json")
	writeSuite(t, suitePath, hash)
	suite, err := Load(suitePath)
	if err != nil {
		t.Fatal(err)
	}
	identities, err := suite.ComparisonIdentities()
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 3 || identities[0].TaskHash != identities[1].TaskHash || identities[1].TaskHash != identities[2].TaskHash {
		t.Fatalf("comparison identities are not fair: %+v", identities)
	}

	if err := os.WriteFile(filepath.Join(fixture, "input.txt"), []byte("mutated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(suitePath); err == nil {
		t.Fatal("mutated fixture unexpectedly validated")
	}
}

func TestResultPreservesFailuresAndFalseCompletion(t *testing.T) {
	runs := []RunResult{
		{
			TaskID: "bug", GroupID: "native", Run: 1, RunnerClaimedCompleted: true, FinalAcceptancePass: false,
			ExitCode: 0, StartedAt: "2026-09-02T00:00:00Z", CompletedAt: "2026-09-02T00:01:00Z",
			Attempts: 2, RegressionFailures: 1, RecoverySuccess: true, NoProgressAttempts: 1,
			WallTime: Metric{Known: true, Value: 60000, Unit: "millisecond"},
		},
	}
	report, err := NewResultReport(strings.Repeat("a", 64), runs, "2026-09-02T00:02:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Runs[0].FalseCompleted {
		t.Fatal("false completion was not derived from claim and final acceptance")
	}
	jsonBytes, markdown, err := report.Render()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(jsonBytes), "token_usage") || strings.Contains(string(jsonBytes), "estimated_cost") || !strings.Contains(string(markdown), "FAIL") || !strings.Contains(string(markdown), "1/2") {
		t.Fatalf("result did not preserve failure or retained provider accounting:\n%s\n%s", jsonBytes, markdown)
	}
}

func TestHarnessRunsHiddenAcceptanceAfterRunnerAndRetainsFailure(t *testing.T) {
	root := t.TempDir()
	fixture := filepath.Join(root, "fixture")
	hidden := filepath.Join(root, "hidden")
	if err := os.Mkdir(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(hidden, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture, "answer.txt"), []byte("wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hidden, "accept.sh"), []byte("#!/bin/sh\ntest \"$(cat answer.txt)\" = right\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	hash, _ := HashFixture(fixture)
	suite := validSuite(root, hash)
	suite.Tasks[0].HiddenOverlay = "hidden"
	suite.Tasks[0].AcceptanceArgv = []string{"sh", "accept.sh"}
	runner := RunnerFunc(func(_ context.Context, request RunRequest) (RunnerResult, error) {
		if _, err := os.Stat(filepath.Join(request.Workspace, "accept.sh")); !os.IsNotExist(err) {
			t.Fatal("runner could see hidden acceptance")
		}
		return RunnerResult{ClaimedCompleted: true, ExitCode: 0, Attempts: 2, HumanInterventions: 1, RegressionFailures: 1, RecoverySuccess: true, NoProgressAttempts: 1}, nil
	})
	result, err := Run(context.Background(), suite, "native", 1, runner, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalAcceptancePass || !result.FalseCompleted || result.AcceptanceExitCode == 0 || result.RegressionFailures != 1 || !result.RecoverySuccess || result.NoProgressAttempts != 1 || result.FirstAttemptPass {
		t.Fatalf("hidden acceptance result = %+v", result)
	}
}

func writeSuite(t *testing.T, path, fixtureHash string) {
	t.Helper()
	suite := validSuite(filepath.Dir(path), fixtureHash)
	raw, err := suite.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func validSuite(root, fixtureHash string) Suite {
	return Suite{
		ProtocolVersion: SuiteVersion,
		Name:            "test-suite",
		Repetitions:     1,
		Groups: []Group{
			{ID: "native", Kind: GroupNative},
			{ID: "autogo-single", Kind: GroupAutoGoSingle},
			{ID: "xgoal-standard", Kind: GroupXGoalStandard},
		},
		Tasks: []Task{{
			ID: "bug", Category: CategoryBug, Fixture: "fixture", FixtureHash: fixtureHash,
			Goal: "make answer right", AcceptanceArgv: []string{"true"}, TimeoutSeconds: 60,
		}},
		root: root,
	}
}
