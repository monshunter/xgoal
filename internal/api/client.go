package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"time"
)

type Client struct {
	http             *http.Client
	expected         ExpectedIdentity
	mu               sync.Mutex
	identity         DaemonIdentity
	handshakeTimeout time.Duration
}

const ProtocolVersion = "xgoal.control/v1alpha1"
const SoftwareVersion = "v0.1.0"
const HeaderProtocol = "X-Xgoal-Protocol"
const HeaderProject = "X-Xgoal-Project"
const HeaderRepository = "X-Xgoal-Repository"
const HeaderInstance = "X-Xgoal-Instance"

var ErrIdentityMismatch = errors.New("daemon identity does not match the requested project or protocol")
var ErrDaemonUnavailable = errors.New("daemon is temporarily unavailable")
var ErrDaemonAccessDenied = errors.New("daemon access denied")

type ExpectedIdentity struct {
	ProjectID          string
	RepositoryIdentity string
	ProjectRoot        string
	StateDir           string
}

type DaemonIdentity struct {
	ProtocolVersion    string `json:"protocol_version"`
	SoftwareVersion    string `json:"software_version"`
	ProjectID          string `json:"project_id"`
	RepositoryIdentity string `json:"repository_identity"`
	ProjectRoot        string `json:"project_root"`
	StateDir           string `json:"state_dir"`
	InstanceID         string `json:"instance_id"`
	PID                int    `json:"pid"`
	StartedAt          string `json:"started_at"`
	State              string `json:"state"`
}

func (identity DaemonIdentity) Validate() error {
	if identity.ProtocolVersion != ProtocolVersion || identity.SoftwareVersion == "" || identity.ProjectID == "" || identity.RepositoryIdentity == "" || identity.InstanceID == "" || identity.PID <= 0 || !filepath.IsAbs(identity.ProjectRoot) || filepath.Clean(identity.ProjectRoot) != identity.ProjectRoot || !filepath.IsAbs(identity.StateDir) || filepath.Clean(identity.StateDir) != identity.StateDir {
		return ErrIdentityMismatch
	}
	if _, err := time.Parse(time.RFC3339Nano, identity.StartedAt); err != nil {
		return ErrIdentityMismatch
	}
	switch identity.State {
	case "STARTING", "READY", "STOPPING":
	default:
		return ErrIdentityMismatch
	}
	return nil
}

func NewProjectClient(socketPath string, timeout time.Duration, expected ExpectedIdentity) (*Client, error) {
	if expected.ProjectID == "" || expected.RepositoryIdentity == "" || !filepath.IsAbs(expected.ProjectRoot) || !filepath.IsAbs(expected.StateDir) {
		return nil, ErrIdentityMismatch
	}
	client, err := NewUnixClient(socketPath, timeout)
	if err != nil {
		return nil, err
	}
	client.expected = expected
	return client, nil
}

// Handshake is read-only. Every later business request is fenced to this instance.
func (client *Client) Handshake(ctx context.Context) (DaemonIdentity, error) {
	ctx, cancel := context.WithTimeout(ctx, client.handshakeTimeout)
	defer cancel()
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.expected.ProjectID == "" {
		return DaemonIdentity{}, ErrIdentityMismatch
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://xgoal.local/v1/daemon", nil)
	if err != nil {
		return DaemonIdentity{}, err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return DaemonIdentity{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return DaemonIdentity{}, fmt.Errorf("daemon handshake status %d: %w", response.StatusCode, ErrDaemonAccessDenied)
		}
		if response.StatusCode >= http.StatusInternalServerError || response.StatusCode == http.StatusTooManyRequests {
			return DaemonIdentity{}, fmt.Errorf("daemon handshake status %d: %w", response.StatusCode, ErrDaemonUnavailable)
		}
		return DaemonIdentity{}, fmt.Errorf("daemon handshake status %d: %w", response.StatusCode, ErrIdentityMismatch)
	}
	var identity DaemonIdentity
	decoder := json.NewDecoder(io.LimitReader(response.Body, 32<<10))
	if err := decoder.Decode(&identity); err != nil {
		if ctx.Err() != nil {
			return DaemonIdentity{}, ctx.Err()
		}
		return DaemonIdentity{}, fmt.Errorf("invalid daemon handshake: %w", ErrIdentityMismatch)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if ctx.Err() != nil {
			return DaemonIdentity{}, ctx.Err()
		}
		return DaemonIdentity{}, fmt.Errorf("trailing daemon handshake data: %w", ErrIdentityMismatch)
	}
	if err := identity.Validate(); err != nil {
		return DaemonIdentity{}, err
	}
	if identity.ProjectID != client.expected.ProjectID || identity.RepositoryIdentity != client.expected.RepositoryIdentity || identity.ProjectRoot != client.expected.ProjectRoot || identity.StateDir != client.expected.StateDir {
		return DaemonIdentity{}, ErrIdentityMismatch
	}
	if client.identity.InstanceID != "" && client.identity.InstanceID != identity.InstanceID {
		return DaemonIdentity{}, fmt.Errorf("daemon instance changed: %w", ErrIdentityMismatch)
	}
	client.identity = identity
	return identity, nil
}

func (client *Client) identify(ctx context.Context, request *http.Request) error {
	if client.expected.ProjectID == "" {
		return nil
	} // Transport-only clients cannot bypass the server fence.
	client.mu.Lock()
	identity := client.identity
	client.mu.Unlock()
	if identity.InstanceID == "" {
		var err error
		identity, err = client.Handshake(ctx)
		if err != nil {
			return err
		}
	}
	request.Header.Set(HeaderProtocol, ProtocolVersion)
	request.Header.Set(HeaderProject, identity.ProjectID)
	request.Header.Set(HeaderRepository, identity.RepositoryIdentity)
	request.Header.Set(HeaderInstance, identity.InstanceID)
	return nil
}

func (client *Client) Close() { client.http.CloseIdleConnections() }

func NewUnixClient(socketPath string, timeout time.Duration) (*Client, error) {
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath || timeout <= 0 {
		return nil, fmt.Errorf("client requires a clean absolute socket path and positive timeout")
	}
	dialer := net.Dialer{Timeout: timeout}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socketPath)
	}}
	return &Client{http: &http.Client{Transport: transport}, handshakeTimeout: timeout}, nil
}

func (client *Client) Stream(ctx context.Context, path string, writer io.Writer) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://xgoal.local"+path, nil)
	if err != nil {
		return 0, err
	}
	if err := client.identify(ctx, request); err != nil {
		return 0, err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	_, err = io.Copy(writer, response.Body)
	return response.StatusCode, err
}

func (client *Client) Do(ctx context.Context, method, path, idempotencyKey string, requestBody any) (int, []byte, error) {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://xgoal.local"+path, body)
	if err != nil {
		return 0, nil, err
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	if err := client.identify(ctx, request); err != nil {
		return 0, nil, err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	encoded, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return 0, nil, err
	}
	return response.StatusCode, encoded, nil
}
