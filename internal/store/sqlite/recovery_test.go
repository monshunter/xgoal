package sqlite

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
)

const recoveryHelperEnvironment = "XGOAL_RECOVERY_HELPER"

func TestEffectRecoveryAcrossProcessBoundaryUsesReadBack(t *testing.T) {
	if os.Getenv(recoveryHelperEnvironment) == "1" {
		runRecoveryWriterProcess(t)
		return
	}

	ctx := context.Background()
	projectDir := filepath.Join(t.TempDir(), "project")
	markerPath := filepath.Join(t.TempDir(), "effect-marker.json")
	command := exec.Command(os.Args[0], "-test.run=^TestEffectRecoveryAcrossProcessBoundaryUsesReadBack$")
	command.Env = append(os.Environ(),
		recoveryHelperEnvironment+"=1",
		"XGOAL_RECOVERY_PROJECT="+projectDir,
		"XGOAL_RECOVERY_MARKER="+markerPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("recovery writer process error = %v\n%s", err, output)
	}

	store, err := Open(ctx, projectDir, clock.Real{})
	if err != nil {
		t.Fatalf("Open() after process exit error = %v", err)
	}
	defer store.Close()
	recoverable, err := store.RecoverableEffects(ctx)
	if err != nil {
		t.Fatalf("RecoverableEffects() error = %v", err)
	}
	if len(recoverable) != 1 || recoverable[0].ID != "effect_recovery" || recoverable[0].State != domain.EffectExecuting {
		t.Fatalf("recoverable effects = %+v", recoverable)
	}

	effect, err := store.UpdateEffect(ctx, recoverable[0].ID, recoverable[0].Version, domain.EffectRecovering, nil, EventInput{
		Type: "EffectRecoveryStarted", ActorType: "kernel", Payload: map[string]any{"read_back": true},
	})
	if err != nil {
		t.Fatalf("move effect to recovering: %v", err)
	}
	marker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("read external effect marker: %v", err)
	}
	var observation map[string]any
	if err := json.Unmarshal(marker, &observation); err != nil {
		t.Fatalf("decode external effect marker: %v", err)
	}
	effect, err = store.UpdateEffect(ctx, effect.ID, effect.Version, domain.EffectObserving, observation, EventInput{
		Type: "EffectReadBackObserved", ActorType: "kernel", Payload: map[string]any{"marker": markerPath},
	})
	if err != nil {
		t.Fatalf("move effect to observing: %v", err)
	}
	effect, err = store.UpdateEffect(ctx, effect.ID, effect.Version, domain.EffectSucceeded, observation, EventInput{
		Type: "EffectRecovered", ActorType: "kernel", Payload: map[string]any{"outcome": "succeeded"},
	})
	if err != nil {
		t.Fatalf("complete recovered effect: %v", err)
	}
	if effect.State != domain.EffectSucceeded || effect.ObservationHash == "" {
		t.Fatalf("recovered effect = %+v", effect)
	}
	recoverable, err = store.RecoverableEffects(ctx)
	if err != nil {
		t.Fatalf("RecoverableEffects() after recovery error = %v", err)
	}
	if len(recoverable) != 0 {
		t.Fatalf("terminal recoverable effects = %+v, want none", recoverable)
	}

	replayed, created, err := store.RequestEffect(ctx, EffectRequest{
		ID: "different_id", Key: "project/goal/work/attempt/workspace-create/1", Type: "WORKSPACE_CREATE",
		Request: map[string]any{"path": "workspace/work_1"},
	}, EventInput{Type: "EffectRequested", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatalf("RequestEffect() replay error = %v", err)
	}
	if created || replayed.ID != effect.ID || replayed.State != domain.EffectSucceeded {
		t.Fatalf("replayed effect = %+v, created = %v", replayed, created)
	}
}

func runRecoveryWriterProcess(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	projectDir := os.Getenv("XGOAL_RECOVERY_PROJECT")
	markerPath := os.Getenv("XGOAL_RECOVERY_MARKER")
	if projectDir == "" || markerPath == "" {
		t.Fatal("recovery helper environment is incomplete")
	}
	store, err := Open(ctx, projectDir, clock.Real{})
	if err != nil {
		t.Fatalf("Open() in writer process error = %v", err)
	}
	effect, created, err := store.RequestEffect(ctx, EffectRequest{
		ID: "effect_recovery", Key: "project/goal/work/attempt/workspace-create/1", Type: "WORKSPACE_CREATE",
		Request: map[string]any{"path": "workspace/work_1"},
	}, EventInput{Type: "EffectRequested", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil || !created {
		t.Fatalf("RequestEffect() = %+v, %v, %v", effect, created, err)
	}
	if _, err := store.UpdateEffect(ctx, effect.ID, effect.Version, domain.EffectExecuting, nil, EventInput{
		Type: "EffectExecuting", ActorType: "kernel", Payload: map[string]any{},
	}); err != nil {
		t.Fatalf("move effect to executing: %v", err)
	}
	marker, err := json.Marshal(map[string]any{"created": true, "path": "workspace/work_1", "tree": "tree-1"})
	if err != nil {
		t.Fatalf("encode external marker: %v", err)
	}
	if err := os.WriteFile(markerPath, marker, 0o600); err != nil {
		t.Fatalf("write external effect marker: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() in writer process error = %v", err)
	}
}
