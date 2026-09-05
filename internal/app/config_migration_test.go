package app_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/app"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/project"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestLegacyConfigDaemonReadsStateWithoutConstructingExecution(t *testing.T) {
	root := t.TempDir()
	appGit(t, root, "init", "-b", "main")
	configuration, err := os.ReadFile("../../xgoal.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.NewReplacer("provider: current-directory", "provider: git-worktree", "command: codex", "command: /missing/xgoal-test-codex", "command: claude", "command: /missing/xgoal-test-claude").Replace(string(configuration))
	if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	paths, err := app.ResolvePaths(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(context.Background(), paths.StateDir, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateGoal(context.Background(), domain.Goal{ID: "retained", State: domain.GoalDraft, Version: 1}, sqlite.EventInput{Type: "GoalCreated", ActorType: "human", Payload: map[string]any{}}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	cancel, result := startMigrationTestDaemon(t, paths)
	waitForServeSocket(t, paths.SocketPath, result)
	client, err := api.NewProjectClient(paths.SocketPath, time.Second, app.ExpectedIdentity(paths))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	code, body, err := client.Do(context.Background(), http.MethodGet, "/v1/goals/retained", "", nil)
	if err != nil || code != http.StatusOK || !strings.Contains(string(body), `"execution_available":false`) || !strings.Contains(string(body), "CONFIG_MIGRATION_REQUIRED") {
		t.Fatalf("legacy status unavailable or ambiguous: %d %s %v", code, body, err)
	}
	for _, directory := range []string{"workspaces", "packets", "patches", "promotions", "adapters"} {
		if _, err := os.Stat(filepath.Join(paths.StateDir, directory)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy startup constructed execution directory %s: %v", directory, err)
		}
	}
	code, body, err = client.Do(context.Background(), http.MethodPost, "/v1/goals", "legacy-reject", map[string]any{"goal_id": "must_not_exist", "raw_goal": "no", "mode": "standard"})
	if err != nil || code != http.StatusConflict || !strings.Contains(string(body), "CONFIG_MIGRATION_REQUIRED") {
		t.Fatalf("legacy execution request: %d %s %v", code, body, err)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), []byte(legacy+"\nunknownField: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := app.Serve(context.Background(), paths); err == nil || !strings.Contains(err.Error(), "unknownField") {
		t.Fatalf("unknown configuration error was silently downgraded: %v", err)
	}
	if held, err := project.OwnershipHeld(paths); err != nil || held {
		t.Fatalf("configuration failure leaked ownership: %v %v", held, err)
	}
}

func startMigrationTestDaemon(t *testing.T, paths app.Paths) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	result, finished := make(chan error, 1), make(chan struct{})
	go func() {
		result <- app.Serve(ctx, paths)
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(15 * time.Second):
			t.Error("test daemon did not finish cleanup")
		}
	})
	return cancel, result
}
