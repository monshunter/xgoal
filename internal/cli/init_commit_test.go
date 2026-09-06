package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/project"
	"github.com/monshunter/xgoal/internal/projectinit"
)

// Exercise the actual CLI, daemon, Git and SQLite without a user commit.
// Provider executables are fixtures; this is not a real model quality test.
func TestRealCLIUnbornInitRunsGoalWithoutManualCommit(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-unborn-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	binary := compileCurrentDirectoryCLI(t, base)
	bin := filepath.Join(base, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	roles := filepath.Join(base, "roles.tsv")
	currentDirectoryProviders(t, bin, roles)
	root := filepath.Join(base, "project")
	cliRepository(t, root)
	currentDirectoryGit(t, root, "config", "user.name", "Fixture")
	currentDirectoryGit(t, root, "config", "user.email", "fixture@invalid")
	// An existing reviewed xgoal config is allowed by init. It selects bounded
	// fixtures and a business validator, and init itself commits this baseline.
	writeCurrentDirectoryFixture(t, filepath.Join(root, "xgoal.yaml"), currentDirectoryConfiguration(bin, roles), 0600)
	var env []string
	for _, entry := range project.GitEnvironment() {
		if !strings.HasPrefix(entry, "PATH=") && !strings.HasPrefix(entry, "XGOAL_") {
			env = append(env, entry)
		}
	}
	env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "XGOAL_RUNTIME_DIR="+filepath.Join(base, "runtime"))
	invoke := func(args ...string) string {
		t.Helper()
		output, err := invokeCurrentDirectoryCLI(binary, env, append([]string{"--project", root}, args...)...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, output)
		}
		return output
	}
	var initialized projectinit.Result
	if err := json.Unmarshal([]byte(invoke("init")), &initialized); err != nil {
		t.Fatal(err)
	}
	head := currentDirectoryGit(t, root, "rev-parse", "HEAD")
	if initialized.InitialCommit != head || currentDirectoryGit(t, root, "status", "--porcelain=v1") != "" {
		t.Fatalf("init did not establish a clean baseline: %+v", initialized)
	}
	if paths := currentDirectoryGit(t, root, "ls-tree", "-r", "--name-only", "HEAD"); paths != ".gitignore\n.xgoalignore\nxgoal.yaml" {
		t.Fatalf("unexpected initial commit paths: %s", paths)
	}
	index, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	invoke("daemon", "start", "--timeout", "20s")
	t.Cleanup(func() { invoke("daemon", "stop", "--timeout", "10s") })
	const goalID = "goal_unborn_init"
	invoke("run", "--id", goalID, "--goal", "append one accepted line to output.txt", "--wait")
	var status struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(invoke("status", goalID)), &status); err != nil || status.State != "COMPLETED" {
		t.Fatalf("Goal did not complete after init: %+v %v", status, err)
	}
	content, err := os.ReadFile(filepath.Join(root, "output.txt"))
	if err != nil || string(content) != "accepted\n" {
		t.Fatalf("missing accepted result: %q %v", content, err)
	}
	after, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil || !bytes.Equal(index, after) || currentDirectoryGit(t, root, "rev-parse", "HEAD") != head {
		t.Fatal("Goal changed the initialization commit or user index")
	}
	calls, err := os.ReadFile(roles)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"planner", "implementer", "reviewer", "validator"} {
		if !strings.Contains(string(calls), role+"\t"+root+"\n") {
			t.Fatalf("missing %s execution: %s", role, calls)
		}
	}
	t.Logf("initial_commit=%s; Goal completed with planner, implementer, reviewer and trusted validation", head)
}
