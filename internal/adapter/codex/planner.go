package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	baseadapter "github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/planner"
	"github.com/monshunter/xgoal/internal/supervisor"
)

func (runtime *Adapter) Plan(ctx context.Context, invocation planner.Invocation, sink baseadapter.EventSink) (planner.Execution, error) {
	if _, err := planner.ValidateInvocation(invocation); err != nil {
		return planner.Execution{}, err
	}
	model, err := decodeJSONModel(invocation.OutputSchema)
	if err != nil {
		return planner.Execution{}, err
	}
	providerModel, err := codexOutputSchema(model)
	if err != nil {
		return planner.Execution{}, err
	}
	schema, err := canonical.Marshal(providerModel)
	if err != nil {
		return planner.Execution{}, err
	}
	environment, err := buildEnvironment(invocation.Environment)
	if err != nil {
		return planner.Execution{}, err
	}
	directory := filepath.Join(runtime.root, "plans", invocation.InvocationID)
	if err := os.Mkdir(directory, 0o700); err != nil {
		return planner.Execution{}, err
	}
	eventsDir := filepath.Join(directory, "events")
	if err := os.Mkdir(eventsDir, 0o700); err != nil {
		return planner.Execution{}, err
	}
	schemaPath := filepath.Join(directory, "output-schema.json")
	if err := writeImmutable(schemaPath, schema, 0o400); err != nil {
		return planner.Execution{}, err
	}
	runContext, cancel := context.WithTimeout(ctx, invocation.Timeout)
	defer cancel()
	limiter := &outputLimiter{remaining: invocation.MaxOutputBytes}
	stream := newJSONLStream(eventsDir, filepath.ToSlash(filepath.Join("plans", invocation.InvocationID)), sink, runtime.clock, limiter, cancel)
	stderr := &boundedStderr{limiter: limiter, cancel: cancel}
	arguments := []string{runtime.binary, "--ask-for-approval", "never", "--sandbox", "read-only", "--cd", invocation.WorkDir, "exec", "--json", "--output-schema", schemaPath, "--color", "never", "-"}
	process, processErr := supervisor.Run(runContext, supervisor.Command{Argv: arguments, Dir: invocation.WorkDir, Env: environmentList(environment), Stdin: strings.NewReader(invocation.Prompt), Stdout: stream, Stderr: stderr, GracePeriod: defaultGracePeriod})
	stderrErr := stderr.persist(filepath.Join(directory, "stderr.log"))
	proposal, sessionID, resultErr := stream.FinalizePlanner(min64(invocation.MaxOutputBytes, maxResultBytes))
	var finalErr error
	switch {
	case stream.failure() != nil:
		finalErr = stream.failure()
	case stderrErr != nil:
		finalErr = stderrErr
	case runContext.Err() != nil:
		finalErr = runContext.Err()
	case resultErr != nil:
		finalErr = resultErr
	case processErr != nil:
		finalErr = fmt.Errorf("Codex CLI Planner exited %d: %w", process.ExitCode, processErr)
	case process.ExitCode != 0:
		finalErr = fmt.Errorf("Codex CLI Planner exited %d", process.ExitCode)
	}
	if finalErr != nil {
		return planner.Execution{}, finalErr
	}
	content, err := canonical.Marshal(proposal)
	if err != nil {
		return planner.Execution{}, err
	}
	if err := writeImmutable(filepath.Join(directory, "result.json"), content, 0o600); err != nil {
		return planner.Execution{}, err
	}
	return planner.Execution{Proposal: proposal, SessionID: sessionID}, nil
}

var _ planner.Adapter = (*Adapter)(nil)
