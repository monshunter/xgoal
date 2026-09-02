package app_test

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/app"
)

func TestRealUnixAPIWritesAndReplaysGoal(t *testing.T) {
	temporary, err := os.MkdirTemp("/tmp", "xgoal-app-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	paths, err := app.ResolvePaths(temporary, filepath.Join(temporary, "state"), "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- app.Serve(ctx, paths) }()
	waitForSocket(t, paths.SocketPath)
	client, err := api.NewUnixClient(paths.SocketPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"goal_id": "goal_runtime", "raw_goal": "bounded goal", "mode": "standard"}
	for range 2 {
		status, response, err := client.Do(context.Background(), http.MethodPost, "/v1/goals", "same-request", body)
		if err != nil || status != http.StatusCreated || !strings.Contains(string(response), `"goal_id":"goal_runtime"`) {
			t.Fatalf("create status=%d body=%s err=%v", status, response, err)
		}
	}
	status, response, err := client.Do(context.Background(), http.MethodPost, "/v1/goals", "same-request", map[string]any{"goal_id": "different", "raw_goal": "different", "mode": "standard"})
	if err != nil || status != http.StatusConflict || !strings.Contains(string(response), `"code":"CONFLICT"`) {
		t.Fatalf("idempotency mismatch status=%d body=%s err=%v", status, response, err)
	}
	status, response, err = client.Do(context.Background(), http.MethodGet, "/v1/doctor", "", nil)
	if err != nil || status != http.StatusOK || !strings.Contains(string(response), `"project_network_policy":"deny"`) {
		t.Fatalf("doctor status=%d body=%s err=%v", status, response, err)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket not ready; exists error=%v", func() error { _, err := os.Stat(path); return err }())
}
