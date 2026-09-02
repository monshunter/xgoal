package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"time"
)

type Client struct {
	http *http.Client
}

func NewUnixClient(socketPath string, timeout time.Duration) (*Client, error) {
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath || timeout <= 0 {
		return nil, fmt.Errorf("client requires a clean absolute socket path and positive timeout")
	}
	dialer := net.Dialer{Timeout: timeout}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dialer.DialContext(ctx, "unix", socketPath)
	}}
	return &Client{http: &http.Client{Transport: transport}}, nil
}

func (client *Client) Stream(ctx context.Context, path string, writer io.Writer) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://xgoal.local"+path, nil)
	if err != nil {
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
