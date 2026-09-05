package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/project"
)

var ErrAlreadyRunning = project.ErrAlreadyRunning

type Recovery interface{ Recover(context.Context) error }

type Config struct {
	RunDir          string
	SocketPath      string
	ShutdownTimeout time.Duration
	Identity        api.DaemonIdentity
	RequestStop     context.CancelFunc
}

type Server struct {
	config     Config
	handler    http.Handler
	recovery   Recovery
	mu         sync.Mutex
	listener   net.Listener
	socketInfo os.FileInfo
	serving    bool
	stopping   bool
	requests   sync.WaitGroup
}

func New(config Config, handler http.Handler, recovery Recovery) (*Server, error) {
	if handler == nil || recovery == nil || !cleanAbsolute(config.RunDir) || !cleanAbsolute(config.SocketPath) {
		return nil, errors.New("daemon requires handler, recovery, and clean absolute runtime and socket paths")
	}
	if err := config.Identity.Validate(); err != nil {
		return nil, fmt.Errorf("daemon identity: %w", err)
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 5 * time.Second
	}
	config.Identity.State = "STARTING"
	return &Server{config: config, handler: handler, recovery: recovery}, nil
}

// Prepare creates the listener after recovery, without starting any request or execution loop.
// The caller holds repository ownership until all requests and other workers have joined.
func (server *Server) Prepare(ctx context.Context) error {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.listener != nil || server.serving || server.stopping {
		return errors.New("daemon already prepared or stopped")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ensurePrivateDirectory(server.config.RunDir); err != nil {
		return err
	}
	if err := ensurePrivateDirectory(filepath.Dir(server.config.SocketPath)); err != nil {
		return err
	}
	if err := server.recovery.Recover(ctx); err != nil {
		return fmt.Errorf("recover daemon state: %w", err)
	}
	if err := removeStaleSocket(server.config.SocketPath); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: server.config.SocketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen on unix socket: %w", err)
	}
	listener.SetUnlinkOnClose(false)
	info, err := os.Lstat(server.config.SocketPath)
	if err == nil {
		err = os.Chmod(server.config.SocketPath, 0600)
	}
	if err != nil {
		_ = listener.Close()
		if info != nil {
			_ = removeOwnedSocket(server.config.SocketPath, info)
		}
		return fmt.Errorf("secure daemon socket: %w", err)
	}
	server.socketInfo = info
	server.listener = &uidListener{Listener: listener, uid: os.Getuid()}
	return nil
}

func (server *Server) Close() error {
	server.mu.Lock()
	listener, info := server.listener, server.socketInfo
	server.listener = nil
	server.stopping = true
	server.mu.Unlock()
	var err error
	if listener != nil {
		if closeErr := listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			err = closeErr
		}
	}
	if info != nil {
		err = errors.Join(err, removeOwnedSocket(server.config.SocketPath, info))
	}
	return err
}

func (server *Server) Serve(ctx context.Context) (returnErr error) {
	server.mu.Lock()
	prepared := server.listener != nil
	server.mu.Unlock()
	if !prepared {
		if err := server.Prepare(ctx); err != nil {
			return err
		}
	}
	server.mu.Lock()
	if server.serving || server.stopping {
		server.mu.Unlock()
		return errors.New("daemon already serving or stopped")
	}
	server.serving = true
	server.config.Identity.State = "READY"
	listener := server.listener
	server.mu.Unlock()
	defer func() { returnErr = errors.Join(returnErr, server.Close()) }()
	requestContext, cancelRequests := context.WithCancel(ctx)
	defer cancelRequests()
	httpServer := &http.Server{Handler: http.HandlerFunc(server.dispatch), BaseContext: func(net.Listener) context.Context { return requestContext }, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 32 << 10}
	result := make(chan error, 1)
	go func() { result <- httpServer.Serve(listener) }()
	var serveErr error
	finished := false
	select {
	case serveErr = <-result:
		finished = true
	case <-ctx.Done():
	}
	server.mu.Lock()
	server.stopping = true
	server.config.Identity.State = "STOPPING"
	server.mu.Unlock()
	// Listener failures must stop the owning execution loop before we join
	// handlers that may themselves be waiting for that loop to finish.
	if server.config.RequestStop != nil {
		server.config.RequestStop()
	}
	cancelRequests()
	shutdownContext, cancel := context.WithTimeout(context.Background(), server.config.ShutdownTimeout)
	defer cancel()
	shutdownErr := httpServer.Shutdown(shutdownContext)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, httpServer.Close())
	}
	if !finished {
		serveErr = <-result
	}
	// No handler can Add after stopping was set under the same mutex.
	// A non-cooperative handler keeps ownership held rather than allowing concurrent state writers.
	server.requests.Wait()
	if errors.Is(serveErr, http.ErrServerClosed) || errors.Is(serveErr, net.ErrClosed) {
		serveErr = nil
	}
	return errors.Join(shutdownErr, serveErr)
}

func (server *Server) dispatch(writer http.ResponseWriter, request *http.Request) {
	server.mu.Lock()
	identity := server.config.Identity
	if server.stopping {
		server.mu.Unlock()
		daemonError(writer, http.StatusServiceUnavailable, "DAEMON_STOPPING")
		return
	}
	server.requests.Add(1)
	server.mu.Unlock()
	defer server.requests.Done()
	if request.Method == http.MethodGet && request.URL.Path == "/v1/daemon" {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(identity)
		return
	}
	if request.Header.Get(api.HeaderProtocol) != identity.ProtocolVersion || request.Header.Get(api.HeaderProject) != identity.ProjectID || request.Header.Get(api.HeaderRepository) != identity.RepositoryIdentity || request.Header.Get(api.HeaderInstance) != identity.InstanceID {
		daemonError(writer, http.StatusConflict, "DAEMON_IDENTITY_MISMATCH")
		return
	}
	if request.URL.Path == "/v1/daemon/stop" {
		if request.Method != http.MethodPost {
			daemonError(writer, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED")
			return
		}
		if server.config.RequestStop == nil {
			daemonError(writer, http.StatusServiceUnavailable, "STOP_UNAVAILABLE")
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(writer).Encode(map[string]any{"instance_id": identity.InstanceID, "state": "STOPPING"})
		server.config.RequestStop()
		return
	}
	server.handler.ServeHTTP(writer, request)
}

func daemonError(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]string{"code": code, "message": code}})
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect daemon socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("daemon socket path exists and is not a real unix socket")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		return errors.New("daemon socket is not owned by this user")
	}
	connection, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return ErrAlreadyRunning
	}
	if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, os.ErrNotExist) {
		return fmt.Errorf("cannot prove daemon socket is stale: %w", dialErr)
	}
	return removeOwnedSocket(path, info)
}

func removeOwnedSocket(path string, owned os.FileInfo) error {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(current, owned) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove daemon socket: %w", err)
	}
	return nil
}

// EnsureRuntimeDirectory creates only private directories used for runtime transport and logs.
func EnsureRuntimeDirectory(path string) error {
	return ensurePrivateDirectory(path)
}

func ensurePrivateDirectory(path string) error {
	// Verify existing ancestors first; never follow a symlink while creating sensitive paths.
	var missing []string
	for current := path; current != filepath.Dir(current); current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			missing = append(missing, current)
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe daemon directory: %s", current)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || (int(stat.Uid) != os.Getuid() && stat.Uid != 0) || (current != path && info.Mode().Perm()&0022 != 0 && info.Mode()&os.ModeSticky == 0) {
			return fmt.Errorf("unsafe daemon directory ownership or permissions: %s", current)
		}
	}
	for i := len(missing) - 1; i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("daemon directory must be a real directory owned by this user")
	}
	return os.Chmod(path, 0700)
}

func cleanAbsolute(path string) bool { return filepath.IsAbs(path) && filepath.Clean(path) == path }

type uidListener struct {
	net.Listener
	uid int
}

func (listener *uidListener) Accept() (net.Conn, error) {
	for {
		connection, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}
		uid, err := peerUID(connection)
		if err == nil && uid == listener.uid {
			return connection, nil
		}
		_ = connection.Close()
	}
}
