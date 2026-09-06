package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

// Real binaries, sockets and SQLite; no Provider invocation is needed to list
// existing Goals. Seeded cancelled Goals cannot start execution on daemon boot.
func TestRealCLIGoalListPaginationIsolationAndReadOnly(t *testing.T) {
	base, err := os.MkdirTemp("/tmp", "xgoal-list-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	base, err = filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	binary := compileCurrentDirectoryCLI(t, base)
	env := lifecycleEnvironment(filepath.Join(base, "runtime"))
	var roots []string
	for _, name := range []string{"populated", "empty"} {
		root := filepath.Join(base, name)
		cliRepository(t, root)
		roots = append(roots, root)
		if out, err := invokeCurrentDirectoryCLI(binary, env, "--project", root, "init"); err != nil {
			t.Fatalf("init: %v %s", err, out)
		}
	}
	root := roots[0]
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(root, ".xgoal"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	expected := []string{"a_" + strings.Repeat("长", 12000) + "\t"}
	for i := 0; i < 104; i++ {
		expected = append(expected, fmt.Sprintf("goal_%03d", i))
	}
	for _, id := range expected {
		event := sqlite.EventInput{Type: "GoalCreated", ActorType: "kernel", Payload: map[string]any{}}
		if err := s.CreateGoal(ctx, domain.Goal{ID: id, State: domain.GoalDraft, Version: 1}, event); err != nil {
			s.Close()
			t.Fatal(err)
		}
		if err := s.UpdateGoalState(ctx, id, 1, domain.GoalCancelled, event); err != nil {
			s.Close()
			t.Fatal(err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, projectRoot := range roots {
		if out, err := invokeCurrentDirectoryCLI(binary, env, "--project", projectRoot, "daemon", "start", "--timeout", "20s"); err != nil {
			t.Fatalf("start: %v %s", err, out)
		}
		t.Cleanup(func() {
			if out, err := invokeCurrentDirectoryCLI(binary, env, "--project", projectRoot, "daemon", "stop", "--timeout", "10s"); err != nil {
				t.Errorf("stop: %v %s", err, out)
			}
		})
	}
	invoke := func(projectRoot string, args ...string) string {
		t.Helper()
		out, err := invokeCurrentDirectoryCLI(binary, env, append([]string{"--project", projectRoot}, args...)...)
		if err != nil {
			t.Fatalf("CLI %v: %v %s", args, err, out)
		}
		return out
	}
	// Check immutable state before/after the queries while both daemons are live.
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(root, ".xgoal", "state.db")}).String() + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	counts := func() map[string]int64 {
		t.Helper()
		result := map[string]int64{}
		for _, table := range []string{"goals", "events", "effects", "invocations", "idempotency_records"} {
			var n int64
			if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
				t.Fatal(err)
			}
			result[table] = n
		}
		return result
	}
	before := counts()
	indexBefore, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	configBefore, err := os.ReadFile(filepath.Join(root, "xgoal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	headBefore := currentDirectoryGit(t, root, "rev-parse", "HEAD")
	var got []string
	cursor := ""
	// One-record pages force even the long/tab ID through the real HTTP cursor path.
	firstCursor := ""
	for {
		var page sqlite.GoalPage
		if err := json.Unmarshal([]byte(invoke(root, "goal", "list", "--state", "CANCELLED", "--limit", "1", "--after", cursor)), &page); err != nil {
			t.Fatal(err)
		}
		for _, g := range page.Items {
			if g.State != domain.GoalCancelled || g.Version != 2 {
				t.Fatalf("goal=%+v", g)
			}
			got = append(got, g.GoalID)
		}
		if page.NextCursor == "" {
			break
		}
		if firstCursor == "" {
			firstCursor = page.NextCursor
		}
		if page.NextCursor == cursor {
			t.Fatal("stalled cursor")
		}
		cursor = page.NextCursor
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("got %d goals want %d", len(got), len(expected))
	}
	var first sqlite.GoalPage
	if err := json.Unmarshal([]byte(invoke(root, "goal", "list")), &first); err != nil || len(first.Items) != 100 || first.NextCursor == "" {
		t.Fatalf("default page=%d %v", len(first.Items), err)
	}
	human := invoke(root, "goal", "list", "--limit", "1", "--state", "CANCELLED", "--format", "human")
	if !strings.Contains(human, "GOAL_ID") || !strings.Contains(human, "Next: xgoal --project "+shellArg(root)+" goal list --limit 1 --state CANCELLED --after "+shellArg(firstCursor)+" --format human") {
		t.Fatal("human omitted working next-page command")
	}
	if out := invoke(roots[1], "goal", "list", "--format", "human"); strings.TrimSpace(out) != "No matching Goals." {
		t.Fatalf("project leaked: %s", out)
	}
	if out := invoke(root, "goal", "list", "--state", "COMPLETED", "--format", "human"); strings.TrimSpace(out) != "No matching Goals." {
		t.Fatalf("state leaked: %s", out)
	}
	status, statusErr := invokeCurrentDirectoryCLI(binary, env, "--project", root, "status", "goal_000", "--format", "human")
	var exitErr *exec.ExitError
	if !errors.As(statusErr, &exitErr) || exitErr.ExitCode() != 4 || !strings.Contains(status, "CANCELLED") {
		t.Fatalf("status=%s", status)
	}
	ids := invoke(root, "ids")
	if !strings.Contains(ids, `"more": true`) {
		t.Fatal("ids compatibility changed")
	}
	after := counts()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("queries wrote state: %v -> %v", before, after)
	}
	indexAfter, _ := os.ReadFile(filepath.Join(root, ".git", "index"))
	configAfter, _ := os.ReadFile(filepath.Join(root, "xgoal.yaml"))
	if string(indexBefore) != string(indexAfter) || string(configBefore) != string(configAfter) || headBefore != currentDirectoryGit(t, root, "rev-parse", "HEAD") {
		t.Fatal("queries changed user files")
	}
	if before["invocations"] != 0 || after["invocations"] != 0 {
		t.Fatal("list invoked Provider")
	}
}
