package orchestrator

import (
	"context"
	"path/filepath"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
	callindex "github.com/monshunter/xgoal/internal/invocation"
	"github.com/monshunter/xgoal/internal/protocol"
)

type invocationTracker struct {
	engine   *Engine
	id       string
	notifier *callindex.Notifier
}

func (engine *Engine) beginInvocation(ctx context.Context, in callindex.Input, packetPath string, schema []byte) (*invocationTracker, error) {
	root, err := filepath.EvalSymlinks(engine.runtimeRoot)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(root, packetPath)
	if err == nil && !callindex.Relative(relative) {
		relative, err = filepath.Rel(engine.runtimeRoot, packetPath)
	}
	if err != nil {
		return nil, err
	}
	data, _, err := callindex.ReadFile(engine.runtimeRoot, relative, 16<<20)
	if err != nil {
		return nil, err
	}
	in.ConfigHash = engine.configHash
	in.PacketPath = relative
	in.PacketSHA256 = callindex.SHA256(data)
	in.SchemaSHA256 = callindex.SHA256(schema)
	in.DelegationHash = protocol.DelegationHash()
	in.ProviderDir, err = callindex.Directory(in.Provider, in.Role, in.ID)
	if err != nil {
		return nil, err
	}
	if _, err := engine.store.RegisterInvocation(ctx, in); err != nil {
		return nil, err
	}
	tracker := &invocationTracker{engine: engine, id: in.ID}
	tracker.notifier = callindex.NewNotifier(func(ctx context.Context) { _, _ = engine.store.RefreshInvocation(ctx, in.ID) })
	return tracker, nil
}
func (t *invocationTracker) sink() adapter.EventSink {
	return func(protocol.AgentEvent) error { t.notifier.Notify(); return nil }
}
func (t *invocationTracker) finish(ctx context.Context, session, status string, cause error) error {
	t.notifier.Close()
	bounded, stop := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer stop()
	// Index maintenance cannot turn a returned Provider result into a new execution.
	_ = t.engine.store.FinishInvocation(bounded, t.id, session, status, cause)
	return cause
}

func (engine *Engine) recoverInvocationIndex(ctx context.Context) error {
	records, err := engine.store.Invocations(ctx, "", "")
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.Observation.Status != "running" {
			continue
		}
		current, err := engine.store.RefreshInvocation(ctx, record.Input.ID)
		if err != nil {
			return err
		}
		current.Observation.Status = "interrupted"
		current.Observation.Failure = "daemon recovered without a returned invocation observation; process ownership and execution recovery are tracked separately"
		if err := engine.store.UpdateInvocation(ctx, current.Input.ID, current.Version, current.Observation); err != nil {
			return err
		}
	}
	return nil
}
