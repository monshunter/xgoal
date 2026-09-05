package orchestrator

import "context"

// TryProjectExecution runs an explicitly requested probe in the same slot as
// Goal execution. The callback includes its durable observation and cleanup.
// A busy project does not enqueue an additional provider invocation.
func (engine *Engine) TryProjectExecution(ctx context.Context, run func(context.Context) error) (bool, error) {
	engine.mu.Lock()
	stopping := engine.stopping
	engine.mu.Unlock()
	if stopping || (engine.runtimeContext != nil && engine.runtimeContext.Err() != nil) {
		return false, context.Canceled
	}
	if !engine.serial.TryLock() {
		return false, nil
	}
	defer engine.serial.Unlock()
	engine.mu.Lock()
	stopping = engine.stopping
	engine.mu.Unlock()
	if stopping {
		return false, context.Canceled
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if engine.runtimeContext != nil {
		stop := context.AfterFunc(engine.runtimeContext, cancel)
		defer stop()
		if engine.runtimeContext.Err() != nil {
			return false, context.Canceled
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := engine.store.CheckExecutionAvailable(ctx); err != nil {
		return false, err
	}
	return true, run(ctx)
}

func (engine *Engine) drainExecutions() {
	engine.mu.Lock()
	engine.stopping = true
	engine.mu.Unlock()
	engine.cancelAll()
	// Probe execution belongs to an HTTP handler, so waiting only for the
	// Engine loop would otherwise close SQLite while that handler cleans up.
	engine.serial.Lock()
	engine.serial.Unlock()
}
