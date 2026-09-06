package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	callindex "github.com/monshunter/xgoal/internal/invocation"
)

func copyInvocationStream(ctx context.Context, path string, reader io.Reader, writer io.Writer) error {
	parsed, err := url.Parse(path)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 4 {
		return errors.New("invalid invocation stream path")
	}
	id := parts[2]
	stream := parsed.Query().Get("stream")
	if stream == "" {
		stream = "stdout"
	}
	cursor := int64(0)
	if after := parsed.Query().Get("after"); after != "" {
		cursor, err = strconv.ParseInt(after, 10, 64)
		if err != nil {
			return err
		}
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	for scanner.Scan() {
		data := scanner.Bytes()
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(data, &envelope); err != nil {
			return fmt.Errorf("invalid invocation stream frame: %w", err)
		}
		if raw, ok := envelope["error"]; ok {
			var detail struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			}
			if err := json.Unmarshal(raw, &detail); err != nil {
				return errors.New("invalid invocation error envelope")
			}
			if _, err := writer.Write(append(append([]byte(nil), data...), '\n')); err != nil {
				return err
			}
			return fmt.Errorf("%s: %s", detail.Code, detail.Message)
		}
		var page callindex.LogPage
		if err := json.Unmarshal(data, &page); err != nil {
			return err
		}
		if page.Invocation.ID != id || page.Stream != stream || page.After != cursor || page.DurableCursor < page.Next {
			return errors.New("invocation stream identity or cursor changed")
		}
		for _, event := range page.Events {
			if event.Sequence != cursor+1 {
				return errors.New("invocation stream skipped or repeated an event")
			}
			cursor = event.Sequence
		}
		if page.Next != cursor {
			return errors.New("invocation stream cursor lacks matching events")
		}
		if _, err := writer.Write(append(append([]byte(nil), data...), '\n')); err != nil {
			return err
		}
		if page.Complete {
			status := page.Invocation.Observation.Status
			if page.Next != page.DurableCursor || (status != "returned" && status != "failed" && status != "interrupted") || page.Invocation.Observation.Unavailable != "" {
				return errors.New("invalid invocation completion marker")
			}
			return nil
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("invocation stream ended before a complete marker; reconnect with the last durable cursor")
}
