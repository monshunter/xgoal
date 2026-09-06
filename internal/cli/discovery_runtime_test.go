package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/project"
	"github.com/monshunter/xgoal/internal/projectinit"
)

func TestRealCLIInitializationExplainsKnownAndUnknownTests(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-init-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	binary := compileCurrentDirectoryCLI(t, base)
	bin := filepath.Join(base, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	writeCurrentDirectoryFixture(t, filepath.Join(bin, "codex"), "#!/bin/sh\necho fixture\n", 0700)
	var env []string
	for _, entry := range project.GitEnvironment() {
		if !strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "XGOAL_") {
			env = append(env, entry)
		}
	}
	env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "XGOAL_RUNTIME_DIR="+filepath.Join(base, "runtime"))
	for _, known := range []bool{false, true} {
		name := "unknown"
		if known {
			name = "node"
		}
		root := filepath.Join(base, name)
		cliRepository(t, root)
		writeCurrentDirectoryFixture(t, filepath.Join(root, "README.md"), "fixture\n", 0600)
		if known {
			writeCurrentDirectoryFixture(t, filepath.Join(root, "package.json"), `{"scripts":{"test":"touch accidentally-ran"}}`, 0600)
		}
		currentDirectoryGit(t, root, "add", ".")
		currentDirectoryGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@invalid", "commit", "-q", "-m", "baseline")
		out, err := invokeCurrentDirectoryCLI(binary, env, "--project", root, "init")
		if err != nil {
			t.Fatalf("init: %v %s", err, out)
		}
		var result projectinit.Result
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatal(err)
		}
		v := result.ValidationPreparation
		if (v.Status == "entrypoints_detected") != known || v.Coverage != "not_verified" || len(v.Preparation) == 0 {
			t.Fatalf("init coverage=%+v", v)
		}
		if known && (len(v.Entries) != 1 || v.Entries[0].Configured) {
			t.Fatalf("discovery silently changed trust: %+v", v)
		}
		if _, err := os.Stat(filepath.Join(root, "accidentally-ran")); !os.IsNotExist(err) {
			t.Fatal("init executed package test")
		}
		out, err = invokeCurrentDirectoryCLI(binary, env, "--project", root, "doctor")
		if err != nil || !strings.Contains(out, `"validation_preparation"`) || !strings.Contains(out, `"not_verified"`) {
			t.Fatalf("doctor: %v %s", err, out)
		}
	}
}
