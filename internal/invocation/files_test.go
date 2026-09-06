package invocation

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDurableCursorResumesAndRejectsGapAndSymlink(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	dir := "adapters/codex/invocations/invoke_1/events"
	if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
		t.Fatal(err)
	}
	put := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put("000001.json", `{"type":"thread.started","thread_id":"session_1"}`)
	observation, err := Scan(context.Background(), root, filepath.Dir(dir), Observation{})
	if err != nil || observation.Cursor != 1 || observation.SessionID != "session_1" || observation.ObservedModel != "unknown" {
		t.Fatalf("scan: %+v %v", observation, err)
	}
	put("000002.json", `{"type":"item.completed","item":{"type":"agent_message","text":"token=abcdefghijk"}}`)
	next, err := Scan(context.Background(), root, filepath.Dir(dir), observation)
	if err != nil || next.Cursor != 2 {
		t.Fatalf("resume: %+v %v", next, err)
	}
	page, err := ReadPage(context.Background(), root, filepath.Dir(dir), 1, 2, 10)
	if err != nil || len(page) != 1 || page[0].Sequence != 2 || strings.Contains(string(page[0].Data), "abcdefghijk") {
		t.Fatalf("page: %+v %v", page, err)
	}
	put("000004.json", `{"type":"turn.completed"}`)
	if _, err := Scan(context.Background(), root, filepath.Dir(dir), next); err == nil {
		t.Fatal("gap accepted")
	}
	os.Remove(filepath.Join(root, dir, "000004.json"))
	outside := filepath.Join(t.TempDir(), "secret")
	os.WriteFile(outside, []byte(`{"secret":"private"}`), 0600)
	os.Symlink(outside, filepath.Join(root, dir, "000003.json"))
	if _, err := Scan(context.Background(), root, filepath.Dir(dir), next); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, _, err := ReadFile(root, "../secret", 1024); err == nil {
		t.Fatal("escape accepted")
	}
}

func TestPublicEventsDoNotExposeThinkingAndPreserveToolResults(t *testing.T) {
	raw := []byte(`{"type":"assistant","unknown_private":"hidden sentinel","message":{"content":[{"type":"thinking","thinking":"private chain"},{"type":"text","text":"public observation"},{"type":"tool_use","name":"Read","input":{"path":"output.txt"}}]}}`)
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(Public(value))
	if strings.Contains(string(got), "private chain") || strings.Contains(string(got), "hidden sentinel") || !strings.Contains(string(got), "public observation") || !strings.Contains(string(got), "tool_use") {
		t.Fatal(string(got))
	}
}

func TestNotificationsNeverWaitForSlowRefreshAndReconcileOnClose(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	observer := NewNotifier(func(ctx context.Context) {
		select {
		case entered <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	observer.Notify()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	go func() {
		for i := 0; i < 100000; i++ {
			observer.Notify()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("slow refresh blocked provider")
	}
	close(release)
	observer.Close()
	observer.Notify()
}

func TestConcurrentNextEventPublicationIsNotAGap(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	dir := "adapters/codex/invocations/invoke_1"
	os.MkdirAll(filepath.Join(root, dir, "events"), 0700)
	if _, _, err := readEvent(root, dir, 1); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// Provider publishes while the reader is between ENOENT and the directory
	// snapshot. A valid next event is not proof that an earlier event vanished.
	os.WriteFile(filepath.Join(root, dir, "events", "000001.json"), []byte(`{"type":"thread.started","thread_id":"s"}`), 0600)
	if err := checkGap(root, dir, 0); err != nil {
		t.Fatalf("normal append misclassified: %v", err)
	}
	got, err := Scan(context.Background(), root, dir, Observation{})
	if err != nil || got.Cursor != 1 {
		t.Fatalf("next read lost event: %+v %v", got, err)
	}
}
