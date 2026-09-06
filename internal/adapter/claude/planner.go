package claude

import (
	"context"
	"errors"
	"fmt"
	callindex "github.com/monshunter/xgoal/internal/invocation"
	"os"
	"path/filepath"
	"strings"

	baseadapter "github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/planner"
	"github.com/monshunter/xgoal/internal/supervisor"
)

func (runtime *Adapter) Plan(ctx context.Context, invocation planner.Invocation, sink baseadapter.EventSink) (planner.Execution, error) {
	invocation.ExecutionConfig = invocation.ExecutionConfig.Clone()
	if err := baseadapter.ValidateExecution(invocation.ExecutionConfig, invocation.ProfileID, "claude-cli", "planner"); err != nil {
		return planner.Execution{}, err
	}
	if _, err := planner.ValidateInvocation(invocation); err != nil {
		return planner.Execution{}, err
	}
	model, err := decodeJSON(invocation.OutputSchema)
	if err != nil {
		return planner.Execution{}, err
	}
	schema, err := canonical.Marshal(claudeSchema(model))
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
	metadata, err := canonical.Marshal(invocation.Record())
	if err != nil {
		return planner.Execution{}, err
	}
	if err := writeImmutable(filepath.Join(directory, "invocation.json"), metadata, 0o600); err != nil {
		return planner.Execution{}, err
	}
	eventsDir := filepath.Join(directory, "events")
	if err := os.Mkdir(eventsDir, 0o700); err != nil {
		return planner.Execution{}, err
	}
	runContext, cancel := context.WithTimeout(ctx, invocation.Timeout)
	defer cancel()
	limiter := &outputLimiter{remaining: invocation.MaxOutputBytes}
	stream := newStream(eventsDir, filepath.ToSlash(filepath.Join("plans", invocation.InvocationID)), sink, runtime.clock.Now, limiter, cancel)
	stream.runtimeRoot = filepath.Dir(filepath.Dir(runtime.root))
	stderr := &boundedStderr{limiter: limiter, cancel: cancel, live: callindex.NewStderrLog(filepath.Dir(filepath.Dir(runtime.root)), directory)}
	tools := "Read,Glob,Grep"
	allowedTools := tools
	if invocation.ExecutionConfig != nil {
		tools = strings.Join(invocation.ExecutionConfig.Tools, ",")
		allowedTools = strings.Join(invocation.ExecutionConfig.AllowedTools, ",")
	}
	arguments := []string{runtime.binary, "-p", "--input-format", "text", "--output-format", "stream-json", "--verbose", "--json-schema", string(schema), "--permission-mode", "dontAsk", "--tools", tools, "--allowedTools", allowedTools}
	arguments = append(arguments, executionArguments(invocation.ExecutionConfig)...)
	process, processErr := supervisor.Run(runContext, supervisor.Command{Argv: arguments, Dir: invocation.WorkDir, Env: environmentList(environment), Stdin: strings.NewReader(invocation.Prompt), Stdout: stream, Stderr: stderr, GracePeriod: gracePeriod})
	stderrErr := stderr.persist(filepath.Join(directory, "stderr.log"))
	proposal, sessionID, resultErr := stream.finalizePlanner(min64(invocation.MaxOutputBytes, maxResultBytes))
	var finalErr error
	switch {
	case errors.Is(processErr, supervisor.ErrProcessUnconfirmed):
		finalErr = processErr
	case stream.failure() != nil:
		finalErr = stream.failure()
	case stderrErr != nil:
		finalErr = stderrErr
	case runContext.Err() != nil:
		finalErr = runContext.Err()
	case resultErr != nil:
		finalErr = resultErr
	case processErr != nil:
		finalErr = fmt.Errorf("Claude CLI Planner exited %d: %w", process.ExitCode, processErr)
	case process.ExitCode != 0:
		finalErr = fmt.Errorf("Claude CLI Planner exited %d", process.ExitCode)
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
