package control

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/exporter"
	"github.com/monshunter/xgoal/internal/redact"
)

func (s *Service) lockArtifacts(ctx context.Context) (func(), error) {
	s.artifactOnce.Do(func() { s.artifactToken = make(chan struct{}, 1) })
	select {
	case s.artifactToken <- struct{}{}:
		return func() { <-s.artifactToken }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (s *Service) exportGoal(ctx context.Context, op api.Operation) (int, any, error) {
	var request struct {
		Output string `json:"output"`
	}
	if err := api.DecodeStrict(op.Body, &request); err != nil || !filepath.IsAbs(request.Output) || filepath.Clean(request.Output) != request.Output {
		return 0, nil, invalid("output must be a clean absolute directory path", err)
	}
	release, err := s.lockArtifacts(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer release()
	result, err := exporter.Export(ctx, s.store, s.projectRoot, op.ResourceID, request.Output)
	if err != nil {
		var incomplete *exporter.IncompleteError
		if errors.As(err, &incomplete) {
			return 0, nil, &api.APIError{Status: http.StatusConflict, Code: "EXPORT_INCOMPLETE", Message: redact.String(err.Error())}
		}
		return 0, nil, invalid("export could not start", errors.New(redact.String(err.Error())))
	}
	return http.StatusCreated, result, nil
}
