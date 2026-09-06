package cli

import (
	"bytes"
	"encoding/json"
	"github.com/monshunter/xgoal/internal/project"
	"github.com/monshunter/xgoal/internal/report"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealCLINewProjectGeneratesAcceptanceAndRequiresReviewInFast(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-generated-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !t.Failed() {
			_ = os.RemoveAll(base)
		} else {
			t.Log("retained", base)
		}
	})
	base, _ = filepath.EvalSymlinks(base)
	binary := compileCurrentDirectoryCLI(t, base)
	bin := filepath.Join(base, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	roles := filepath.Join(base, "roles.tsv")
	currentDirectoryProviderProposal(t, bin, roles, func(p map[string]any) {
		c := p["contract"].(map[string]any)
		c["generated_validators"] = []any{map[string]any{"id": "output-check", "description": "every output line has required bytes", "runtime": "sh", "script": "test -s output.txt && ! grep -v '^accepted$' output.txt", "timeout_seconds": 5}}
	})
	root := filepath.Join(base, "project")
	cliRepository(t, root)
	currentDirectoryGit(t, root, "config", "user.name", "Fixture")
	currentDirectoryGit(t, root, "config", "user.email", "fixture@invalid")
	var env []string
	for _, e := range project.GitEnvironment() {
		if !strings.HasPrefix(e, "PATH=") && !strings.HasPrefix(e, "XGOAL_") {
			env = append(env, e)
		}
	}
	env = append(env, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "XGOAL_RUNTIME_DIR="+filepath.Join(base, "runtime"))
	invoke := func(args ...string) string {
		t.Helper()
		out, err := invokeCurrentDirectoryCLI(binary, env, append([]string{"--project", root}, args...)...)
		if err != nil {
			t.Fatalf("CLI %v: %v\n%s", args, err, out)
		}
		return out
	}
	invoke("init")
	configBefore, err := os.ReadFile(filepath.Join(root, "xgoal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configBefore), "git-diff-check") || strings.Contains(string(configBefore), "output-check") {
		t.Fatal("fixture preinstalled business acceptance")
	}
	head := currentDirectoryGit(t, root, "rev-parse", "HEAD")
	indexBefore, _ := os.ReadFile(filepath.Join(root, ".git/index"))
	invoke("daemon", "start", "--timeout", "20s")
	t.Cleanup(func() {
		_, _ = invokeCurrentDirectoryCLI(binary, env, "--project", root, "daemon", "stop", "--timeout", "15s")
	})
	var reports []report.Report
	for _, id := range []string{"goal_generated_a", "goal_generated_b"} {
		invoke("run", "--id", id, "--goal", "append one accepted line to output.txt", "--mode", "fast", "--wait")
		var response struct {
			Report report.Report `json:"json"`
		}
		if err := json.Unmarshal([]byte(invoke("report", id)), &response); err != nil {
			t.Fatal(err)
		}
		r := response.Report
		if len(r.Criteria) != 1 || !strings.HasPrefix(r.Criteria[0].ValidatorIDs[0], id+"__") {
			t.Fatalf("wrong frozen namespace: %+v", r.Criteria)
		}
		found := false
		for _, v := range r.Validators {
			if v.Source == "agent_generated" && v.Result == "PASSED" {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing actual generated evidence: %+v", r.Validators)
		}
		reports = append(reports, r)
	}
	calls, err := os.ReadFile(roles)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "reviewer\t") != 2 {
		t.Fatalf("fast bypassed generated acceptance Review: %s", calls)
	}
	configAfter, _ := os.ReadFile(filepath.Join(root, "xgoal.yaml"))
	indexAfter, _ := os.ReadFile(filepath.Join(root, ".git/index"))
	if !bytes.Equal(configBefore, configAfter) || !bytes.Equal(indexBefore, indexAfter) || head != currentDirectoryGit(t, root, "rev-parse", "HEAD") {
		t.Fatal("Goal modified config or user Git metadata")
	}
	invoke("daemon", "stop", "--timeout", "15s")
	assertCurrentDirectoryEvidence(t, filepath.Join(root, ".xgoal"), reports)
}
