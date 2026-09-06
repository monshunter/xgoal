package validator

import (
	"github.com/monshunter/xgoal/internal/validationplan"
	"os"
	"path/filepath"
	"testing"
)

func TestAcceptanceOverlayProtectsInputsWithoutChangingProjectDefinitions(t *testing.T) {
	root := t.TempDir()
	i, err := validationplan.NewInput("acceptance.md", "100644", []byte("exact behavior"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, i.Path), []byte(i.Content), 0600); err != nil {
		t.Fatal(err)
	}
	base := &Registry{definitions: map[string]Definition{"project": {ID: "project", Hash: "original"}}}
	for _, generated := range [][]validationplan.Generated{nil, {{ID: "goal_a__behavior", Description: "exact check", Runtime: "sh", Script: "test -s output.txt", TimeoutSeconds: 5}}} {
		current, err := base.WithAcceptance(generated, []validationplan.Input{i})
		if err != nil {
			t.Fatal(err)
		}
		if base.definitions["project"].Hash != current.definitions["project"].Hash || len(base.definitions) != 1 {
			t.Fatal("mutated project registry")
		}
		if err := current.VerifyAcceptanceInputs(root); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, i.Path), []byte("weakened"), 0600); err != nil {
			t.Fatal(err)
		}
		if current.VerifyAcceptanceInputs(root) == nil {
			t.Fatal("accepted modified user material")
		}
		if err := os.WriteFile(filepath.Join(root, i.Path), []byte(i.Content), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
