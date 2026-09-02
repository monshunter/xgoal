package daemon_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/daemon"
)

type recovery struct {
	socket string
	called chan struct{}
}

func (recovery *recovery) Recover(context.Context) error {
	if _, err := os.Lstat(recovery.socket); !errors.Is(err, os.ErrNotExist) {
		return errors.New("socket existed before recovery")
	}
	close(recovery.called)
	return nil
}

func TestDaemonUsesPrivateSocketSingleWriterAndRecoveryBeforeListen(t *testing.T) {
	temporary, err := os.MkdirTemp("/tmp", "xgoal-daemon-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporary) })
	runDir := filepath.Join(temporary, "run")
	socket := filepath.Join(runDir, "xgoal.sock")
	recovered := &recovery{socket: socket, called: make(chan struct{})}
	server, err := daemon.New(daemon.Config{RunDir: runDir, SocketPath: socket, ShutdownTimeout: time.Second}, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}), recovered)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- server.Serve(ctx) }()
	waitForSocket(t, socket)
	select {
	case <-recovered.called:
	default:
		t.Fatal("recovery was not called before socket became ready")
	}
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %o, want 600", info.Mode().Perm())
	}
	client, err := api.NewUnixClient(socket, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	status, _, err := client.Do(context.Background(), http.MethodGet, "/health", "", nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("socket request status=%d err=%v", status, err)
	}
	secondRecovery := &recovery{socket: filepath.Join(runDir, "never-created.sock"), called: make(chan struct{})}
	second, _ := daemon.New(daemon.Config{RunDir: runDir, SocketPath: socket}, http.NotFoundHandler(), secondRecovery)
	if err := second.Serve(context.Background()); !errors.Is(err, daemon.ErrAlreadyRunning) {
		t.Fatalf("second daemon error = %v, want ErrAlreadyRunning", err)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("serve shutdown error = %v", err)
	}
	if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains after shutdown: %v", err)
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
	t.Fatalf("socket %s did not become ready", path)
}
