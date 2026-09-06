package claude

import (
	"context"
	"errors"
	"github.com/monshunter/xgoal/internal/adapter"
	callindex "github.com/monshunter/xgoal/internal/invocation"
	"github.com/monshunter/xgoal/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLiveStderrAndSlowNotificationCannotBlockCancellation(t *testing.T) {
	f := newFixture(t, "blocking")
	data, err := os.ReadFile(f.binary)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "  blocking) sleep 60 ;;", "  blocking) printf 'api_key=live-sentinel\\nvisible diagnostic\\n' >&2; sleep 60 ;;", 1))
	if err := os.WriteFile(f.binary, data, 0700); err != nil {
		t.Fatal(err)
	}
	runtime := f.adapter(t)
	entered := make(chan struct{}, 1)
	notifier := callindex.NewNotifier(func(ctx context.Context) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
	})
	defer notifier.Close()
	handle, err := runtime.Start(context.Background(), f.invocation(t, "invocation_live", adapter.SessionFresh), func(protocol.AgentEvent) error { notifier.Notify(); return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Cancel(context.Background(), handle) })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	for i := 0; i < 100000; i++ {
		notifier.Notify()
	}
	live := filepath.Join(f.runtimeRoot, "adapters", "claude", "invocations", "invocation_live", "stderr", "events", "000002.json")
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err = os.ReadFile(live)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stderr unavailable before exit: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(string(data), "visible diagnostic") {
		t.Fatalf("live stderr: %s", data)
	}
	first, err := os.ReadFile(filepath.Join(filepath.Dir(live), "000001.json"))
	if err != nil || strings.Contains(string(first), "live-sentinel") {
		t.Fatalf("live redaction: %s %v", first, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runtime.Cancel(ctx, handle); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Wait(ctx, handle); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}
