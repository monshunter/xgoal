package app_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/app"
	"github.com/monshunter/xgoal/internal/project"
)

func TestStatusIsReadOnlyAndDoesNotReportHeldOwnerStopped(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "xgoal-status-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	appGit(t, root, "init", "-b", "main")
	paths, err := app.ResolvePaths(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	status, err := app.Status(context.Background(), paths)
	if err != nil || status.State != "STOPPED" || status.OwnershipHeld {
		t.Fatalf("initial status=%#v err=%v", status, err)
	}
	if _, err := os.Stat(paths.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status created state: %v", err)
	}
	ownership, err := project.Acquire(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	defer ownership.Close()
	status, err = app.Status(context.Background(), paths)
	if err != nil || status.State != "UNREACHABLE" || !status.OwnershipHeld {
		t.Fatalf("held status=%#v err=%v", status, err)
	}
	if _, err := os.Stat(filepath.Join(paths.StateDir, "state.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("status opened database: %v", err)
	}
}

func TestStartRejectsExistingForeignListenerWithoutCreatingState(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "xgoal-start-reject-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	appGit(t, root, "init", "-b", "main")
	socket := filepath.Join(root, "foreign.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.NotFoundHandler()}
	go server.Serve(listener)
	defer server.Close()
	paths, err := app.ResolvePaths(root, "", socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := app.Start(ctx, paths, "/must-not-launch"); !errors.Is(err, api.ErrIdentityMismatch) {
		t.Fatalf("foreign listener not diagnosed: %v", err)
	}
	if _, err := os.Stat(paths.StateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected start created state: %v", err)
	}
	connection, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		t.Fatalf("foreign listener removed: %v", err)
	}
	connection.Close()
}
