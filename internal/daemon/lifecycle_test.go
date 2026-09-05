package daemon_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/daemon"
)

type noRecovery struct{}

func (noRecovery) Recover(context.Context) error { return nil }

func TestDaemonDoesNotReplaceLiveSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "xgoal-live-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "live.sock")
	existing, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer existing.Close()
	server, err := daemon.New(daemon.Config{RunDir: dir, SocketPath: socket, Identity: testIdentity()}, http.NotFoundHandler(), noRecovery{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = server.Serve(ctx)
	if !errors.Is(err, daemon.ErrAlreadyRunning) {
		t.Fatalf("live socket was not protected: %v", err)
	}
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		t.Fatalf("original listener inaccessible: %v", err)
	}
	conn.Close()
}

func TestDaemonShutdownCancelsAndWaitsForRequests(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "xgoal-stop-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "x.sock")
	started, finished := make(chan struct{}), make(chan struct{})
	server, err := daemon.New(daemon.Config{RunDir: dir, SocketPath: socket, ShutdownTimeout: time.Second, Identity: testIdentity()}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		time.Sleep(50 * time.Millisecond)
		close(finished)
	}), noRecovery{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForSocket(t, socket)
	client, err := api.NewProjectClient(socket, time.Second, testExpected())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	go func() { _, _, _ = client.Do(context.Background(), http.MethodGet, "/test", "", nil) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown stuck")
	}
	select {
	case <-finished:
	default:
		t.Fatal("server returned before request cleanup")
	}
}

func TestDaemonRejectsWrongProjectProtocolAndInstanceBeforeHandler(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "xgoal-fence-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "rpc.sock")
	var calls atomic.Int64
	server, err := daemon.New(daemon.Config{RunDir: dir, SocketPath: socket, Identity: testIdentity()}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }), noRecovery{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	waitForSocket(t, socket)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	expected := testIdentity()
	headers := map[string]string{api.HeaderProject: expected.ProjectID, api.HeaderRepository: expected.RepositoryIdentity, api.HeaderProtocol: expected.ProtocolVersion, api.HeaderInstance: expected.InstanceID}
	for _, invalid := range []string{"all", api.HeaderProject, api.HeaderRepository, api.HeaderProtocol, api.HeaderInstance} {
		request, _ := http.NewRequest(http.MethodPost, "http://xgoal.local/v1/goals", nil)
		if invalid != "all" {
			for key, value := range headers {
				request.Header.Set(key, value)
			}
			request.Header.Set(invalid, "wrong")
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusConflict {
			t.Fatalf("invalid %s accepted: %d", invalid, response.StatusCode)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid request reached handler %d times", calls.Load())
	}
	wrong := testExpected()
	wrong.ProjectID = "different-project"
	wrongClient, err := api.NewProjectClient(socket, time.Second, wrong)
	if err != nil {
		t.Fatal(err)
	}
	defer wrongClient.Close()
	if _, err := wrongClient.Handshake(context.Background()); !errors.Is(err, api.ErrIdentityMismatch) {
		t.Fatalf("wrong project handshake: %v", err)
	}
	valid, err := api.NewProjectClient(socket, time.Second, testExpected())
	if err != nil {
		t.Fatal(err)
	}
	defer valid.Close()
	code, _, err := valid.Do(context.Background(), http.MethodPost, "/v1/goals", "", nil)
	if err != nil || code != http.StatusNoContent || calls.Load() != 1 {
		t.Fatalf("valid request failed: %d %v calls=%d", code, err, calls.Load())
	}
}

func TestDaemonCleanupDoesNotUnlinkReplacementSocket(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "xgoal-inode-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "rpc.sock")
	server, err := daemon.New(daemon.Config{RunDir: dir, SocketPath: socket, Identity: testIdentity()}, http.NotFoundHandler(), noRecovery{})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	replacement, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		t.Fatalf("replacement listener was removed: %v", err)
	}
	connection.Close()
}

func TestDaemonShutdownTimeoutKeepsWaitingForRequestCleanup(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "xgoal-drain-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "rpc.sock")
	started, finished := make(chan struct{}), make(chan struct{})
	server, err := daemon.New(daemon.Config{RunDir: dir, SocketPath: socket, Identity: testIdentity(), ShutdownTimeout: 20 * time.Millisecond}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		time.Sleep(150 * time.Millisecond)
		close(finished)
	}), noRecovery{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(ctx) }()
	waitForSocket(t, socket)
	client, err := api.NewProjectClient(socket, time.Second, testExpected())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	go func() { _, _, _ = client.Do(context.Background(), http.MethodGet, "/work", "", nil) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown timeout not reported: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish")
	}
	select {
	case <-finished:
	default:
		t.Fatal("daemon returned before request cleanup after forced HTTP close")
	}
}

func TestListenerFailureCancelsRuntimeBeforeWaitingForHandlers(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "xgoal-close-order-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "rpc.sock")
	runtimeContext, cancelRuntime := context.WithCancel(context.Background())
	defer cancelRuntime()
	started, finished := make(chan struct{}), make(chan struct{})
	server, err := daemon.New(daemon.Config{RunDir: dir, SocketPath: socket, Identity: testIdentity(), ShutdownTimeout: time.Second, RequestStop: cancelRuntime}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-runtimeContext.Done(); close(finished) }), noRecovery{})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(runtimeContext) }()
	waitForSocket(t, socket)
	client, err := api.NewProjectClient(socket, time.Second, testExpected())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	go func() { _, _, _ = client.Do(context.Background(), http.MethodGet, "/join-engine", "", nil) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("request did not start")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runtimeContext.Done():
	case <-time.After(200 * time.Millisecond):
		cancelRuntime()
		<-done
		t.Fatal("listener failure did not cancel runtime before waiting for handler")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not join")
	}
	select {
	case <-finished:
	default:
		t.Fatal("server returned before dependent request cleanup")
	}
}
