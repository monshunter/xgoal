package harness_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/harness"
)

func TestRequiredHarnessUsesProviderManifestAndPreservesProjectFiles(t *testing.T) {
	root := t.TempDir()
	required := config.Harness{Type: "autogo", Required: true}
	if _, err := harness.Discover(root, "codex-cli", required); err == nil {
		t.Fatal("missing required Harness accepted")
	}
	files := map[string]string{"AGENTS.md": "# Project rules\nKeep the business invariant.\n", ".agents/skills/autogo-change-implement/SKILL.md": "---\nname: implement\ndescription: implement current work\n---\n", "docs/AGENTS.md": "# Documentation rules\n"}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest, _ := json.Marshal(map[string]any{"owner": "autogo", "schema_version": 3, "agent": "codex", "harness_pack": "core", "autogo_version": "0.3.0", "managed_files": []string{"AGENTS.md", ".agents/skills/autogo-change-implement/SKILL.md"}, "managed_blocks": map[string]string{"docs/AGENTS.md": "DOCS-CONTRACT"}})
	path := filepath.Join(root, ".autogo", "manifests", "codex.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	input, err := harness.Discover(root, "codex-cli", required)
	if err != nil || !input.Found || !input.Compatible || len(input.Files) != 4 || input.LoadObservation != "unknown" {
		t.Fatalf("discovery %+v %v", input, err)
	}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.Discover(root, "claude-cli", required); err == nil {
		t.Fatal("Codex installation advertised Claude compatibility")
	}
	for name, content := range files {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || string(got) != content {
			t.Fatalf("discovery modified %s", name)
		}
	}
	if err := os.Remove(filepath.Join(root, ".agents", "skills", "autogo-change-implement", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.Discover(root, "codex-cli", required); err == nil {
		t.Fatal("missing declared skill accepted")
	}
}

func TestOptionalHarnessDoesNotRequireInstallationOrReadEscapes(t *testing.T) {
	root := t.TempDir()
	input, err := harness.Discover(root, "codex-cli", config.Harness{})
	if err != nil || input.Found || !input.Compatible || input.LoadObservation != "unknown" {
		t.Fatalf("optional absent %+v %v", input, err)
	}
	if !strings.Contains(strings.Join(input.Diagnostics, " "), "install the project-local core") {
		t.Fatal("optional preparation guidance missing")
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := harness.Discover(root, "codex-cli", config.Harness{}); err == nil {
		t.Fatal("escaped rule symlink accepted")
	}
	input, err = harness.Discover(root, "codex-cli", config.Harness{Type: "autogo", Required: true})
	if !errors.Is(err, harness.ErrRequired) || input.Compatible || len(input.Diagnostics) == 0 {
		t.Fatalf("unsafe required Harness lost typed preparation diagnostic: %+v %v", input, err)
	}
}
