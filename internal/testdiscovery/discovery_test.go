package testdiscovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/config"
)

func TestDiscoveryReportsKnownEntriesWithoutExecutingScripts(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{"go.mod": "module fixture\n", "package.json": `{"packageManager":"pnpm@9.0","scripts":{"test":"touch never-created && echo secret-sentinel"}}`, "pytest.ini": "[pytest]\n", "Cargo.toml": "[package]\n", "Makefile": "test: setup\n\ttouch never-created\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	r := Inspect(root, &config.Config{Validators: []config.Validator{{ID: "go-test-all", Argv: []string{"go", "test", "./..."}}}})
	if r.Status != "entrypoints_detected" || len(r.Entries) != 5 || !r.Entries[0].Configured || r.Coverage != "not_verified" || r.Entries[1].Argv[0] != "pnpm" {
		t.Fatalf("discovery=%+v", r)
	}
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), "secret-sentinel") {
		t.Fatal("script body exposed")
	}
	if _, err := os.Stat(filepath.Join(root, "never-created")); !os.IsNotExist(err) {
		t.Fatal("discovery executed project code")
	}
}

func TestDiscoveryUnknownAndUnsafeInputsGivePreparation(t *testing.T) {
	root := t.TempDir()
	r := Inspect(root, nil)
	if r.Status != "unknown" || len(r.Preparation) == 0 || !strings.Contains(r.Preparation[0], "only checks whitespace") {
		t.Fatalf("unknown=%+v", r)
	}
	secret := filepath.Join(t.TempDir(), "private.json")
	if err := os.WriteFile(secret, []byte(`{"scripts":{"test":"private"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "package.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Makefile"), make([]byte, (1<<20)+1), 0600); err != nil {
		t.Fatal(err)
	}
	r = Inspect(root, nil)
	if r.Status != "unknown" || len(r.Entries) != 0 || len(r.Diagnostics) != 2 {
		t.Fatalf("unsafe=%+v", r)
	}
}
