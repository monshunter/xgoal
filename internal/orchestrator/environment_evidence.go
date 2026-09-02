package orchestrator

import (
	"context"
	"runtime"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/evidence"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/reconcile"
)

func (engine *Engine) recordEnvironmentFailureEvidence(ctx context.Context, subjectID string, revision domain.GoalRevision, tree, stage string, cause error) (string, error) {
	id, err := randomID("evidence_environment")
	if err != nil {
		return "", err
	}
	definitionHash, err := canonical.Hash("environment-preparation-definition", "xgoal.environment-failure/v1", map[string]any{
		"runtime": engine.config.Runtime, "bootstrap": engine.config.Bootstrap, "stage": stage,
	})
	if err != nil {
		return "", err
	}
	environmentHash, err := canonical.Hash("environment-failure-host", "xgoal.environment-failure/v1", map[string]any{
		"os": runtime.GOOS, "arch": runtime.GOARCH, "isolation_level": "L0",
	})
	if err != nil {
		return "", err
	}
	payloadHash, err := canonical.Hash("environment-failure", "xgoal.environment-failure/v1", map[string]any{
		"stage": stage, "error": reconcile.NormalizeError(cause.Error()),
	})
	if err != nil {
		return "", err
	}
	record := evidence.Record{Evidence: protocol.Evidence{
		ProtocolVersion: protocol.EvidenceVersion, ID: id, Kind: "ENVIRONMENT_FAILURE", SubjectID: subjectID,
		Producer: "environment/local", Authority: domain.AuthorityDeterministic,
		GoalRevisionHash: revision.Hash, ConfigHash: engine.configHash, TreeHash: tree,
		PayloadHash: payloadHash, State: domain.EvidenceCurrent, CreatedAt: time.Now().UTC(),
	}, DefinitionHash: definitionHash, EnvironmentHash: environmentHash}
	if err := engine.store.AppendEvidence(ctx, record); err != nil {
		return "", err
	}
	return id, nil
}
