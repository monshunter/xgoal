package api_test

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
)

func TestHandshakeDistinguishesUnavailabilityFromIdentityMismatch(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "xgoal-handshake-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "rpc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })}
	go server.Serve(listener)
	defer server.Close()
	client, err := api.NewProjectClient(socket, time.Second, api.ExpectedIdentity{ProjectID: "project", RepositoryIdentity: "repo", ProjectRoot: "/project", StateDir: "/project/.xgoal"})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Handshake(context.Background())
	if !errors.Is(err, api.ErrDaemonUnavailable) || errors.Is(err, api.ErrIdentityMismatch) {
		t.Fatalf("temporary HTTP 503 was classified as identity conflict: %v", err)
	}
}

func TestHandshakePreservesAccessDeniedClassification(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "xgoal-handshake-deny-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "rpc.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) })}
			go server.Serve(listener)
			defer server.Close()
			client, err := api.NewProjectClient(socket, time.Second, api.ExpectedIdentity{ProjectID: "project", RepositoryIdentity: "repo", ProjectRoot: "/project", StateDir: "/project/.xgoal"})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = client.Handshake(context.Background())
			if !errors.Is(err, api.ErrDaemonAccessDenied) || errors.Is(err, api.ErrIdentityMismatch) {
				t.Fatalf("access denial lost: %v", err)
			}
		})
	}
}
