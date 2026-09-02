package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

var ErrAlreadyRunning = errors.New("xgoal daemon is already running")

type Recovery interface {
	Recover(context.Context) error
}

type Config struct {
	RunDir          string
	SocketPath      string
	ShutdownTimeout time.Duration
}

type Server struct {
	config   Config
	handler  http.Handler
	recovery Recovery
}

func New(config Config, handler http.Handler, recovery Recovery) (*Server, error) {
	if handler == nil || recovery == nil || !cleanAbsolute(config.RunDir) || !cleanAbsolute(config.SocketPath) || filepath.Dir(config.SocketPath) != config.RunDir {
		return nil, errors.New("daemon requires handler, recovery, and a socket directly under a clean absolute run directory")
	}
	if config.ShutdownTimeout <= 0 {
		config.ShutdownTimeout = 5 * time.Second
	}
	return &Server{config: config, handler: handler, recovery: recovery}, nil
}

// Serve holds the single-writer lock, recovers persisted workers, then exposes the API.
func (server *Server) Serve(ctx context.Context) (returnErr error) {
	if err := ensurePrivateDirectory(server.config.RunDir); err != nil {
		return err
	}
	lock, err := acquireLock(filepath.Join(server.config.RunDir, "daemon.lock"))
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.release()) }()
	if err := server.recovery.Recover(ctx); err != nil {
		return fmt.Errorf("recover daemon state: %w", err)
	}
	if err := removeStaleSocket(server.config.SocketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", server.config.SocketPath)
	if err != nil {
		return fmt.Errorf("listen on unix socket: %w", err)
	}
	defer func() {
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			returnErr = errors.Join(returnErr, err)
		}
		if err := os.Remove(server.config.SocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, fmt.Errorf("remove daemon socket: %w", err))
		}
	}()
	if err := os.Chmod(server.config.SocketPath, 0o600); err != nil {
		return fmt.Errorf("secure daemon socket: %w", err)
	}
	listener = &uidListener{Listener: listener, uid: os.Getuid()}
	httpServer := &http.Server{
		Handler:           server.handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	serveResult := make(chan error, 1)
	go func() { serveResult <- httpServer.Serve(listener) }()
	select {
	case err := <-serveResult:
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), server.config.ShutdownTimeout)
		defer cancel()
		shutdownErr := httpServer.Shutdown(shutdownContext)
		serveErr := <-serveResult
		if errors.Is(serveErr, http.ErrServerClosed) || errors.Is(serveErr, net.ErrClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}

type fileLock struct{ file *os.File }

func acquireLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("lock daemon state: %w", err)
	}
	return &fileLock{file: file}, nil
}

func (lock *fileLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	return errors.Join(err, lock.file.Close())
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
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale daemon socket: %w", err)
	}
	return nil
}

func ensurePrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return fmt.Errorf("create daemon run directory: %w", err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("daemon run path is not a real directory")
	}
	return os.Chmod(path, 0o700)
}

func cleanAbsolute(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

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
