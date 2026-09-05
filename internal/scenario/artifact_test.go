package scenario_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/scenario"
)

func fixture(t *testing.T) (string, string, scenario.Manifest) {
	t.Helper()
	runtime, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(runtime, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "response.json"), []byte(`{"value":"accepted"}`), 0600); err != nil {
		t.Fatal(err)
	}
	return runtime, source, scenario.Manifest{EvidenceID: "evidence_scenario", Scenario: config.Scenario{ID: "business", Description: "valid response", Steps: []string{"request and assert"}, Validators: []string{"business-check"}, ArtifactPaths: []string{"response.json"}}, GoalRevisionHash: strings.Repeat("a", 64), ConfigHash: strings.Repeat("b", 64), TreeHash: strings.Repeat("c", 40), EnvironmentID: "environment_final", EnvironmentHash: strings.Repeat("d", 64), ReceiptHashes: map[string]string{"business-check": strings.Repeat("e", 64)}}
}

func TestSealedScenarioSurvivesSourceChangesAndRejectsArtifactCorruption(t *testing.T) {
	runtime, source, input := fixture(t)
	m, err := scenario.Seal(context.Background(), runtime, source, input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "response.json"), []byte("different"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := scenario.Load(context.Background(), runtime, m.EvidenceID)
	if err != nil || loaded.Hash != m.Hash {
		t.Fatalf("sealed artifact changed: %+v %v", loaded, err)
	}
	sealed := filepath.Join(runtime, "scenarios", m.EvidenceID, "files", "response.json")
	if err := os.Chmod(sealed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sealed, []byte("corrupt"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sealed, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := scenario.Load(context.Background(), runtime, m.EvidenceID); err == nil {
		t.Fatal("corrupt artifact accepted")
	}
}

func TestScenarioSealRejectsMissingLinkedAndOversizedOutput(t *testing.T) {
	for _, kind := range []string{"missing", "linked", "parent-link", "oversized", "traversal"} {
		t.Run(kind, func(t *testing.T) {
			runtime, source, input := fixture(t)
			path := filepath.Join(source, "response.json")
			switch kind {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "linked":
				if err := os.Rename(path, filepath.Join(source, "original")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("original", path); err != nil {
					t.Fatal(err)
				}
			case "parent-link":
				if err := os.Symlink(source, filepath.Join(source, "linked")); err != nil {
					t.Fatal(err)
				}
				input.Scenario.ArtifactPaths = []string{"linked/response.json"}
			case "oversized":
				if err := os.Truncate(path, 33<<20); err != nil {
					t.Fatal(err)
				}
			case "traversal":
				input.Scenario.ArtifactPaths = []string{"../source/response.json"}
			}
			if _, err := scenario.Seal(context.Background(), runtime, source, input); err == nil {
				t.Fatal("unsafe artifact sealed")
			}
			if _, err := scenario.Load(context.Background(), runtime, input.EvidenceID); err == nil {
				t.Fatal("incomplete seal published a manifest")
			}
		})
	}
}
