package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/monshunter/xgoal/internal/acceptance"
	baseadapter "github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/supervisor"
)

func (runtime *Adapter) Accept(ctx context.Context, invocation acceptance.Invocation, sink baseadapter.EventSink) (acceptance.Execution, error) {
	invocation.ExecutionConfig = invocation.ExecutionConfig.Clone()
	if err := baseadapter.ValidateExecution(invocation.ExecutionConfig, invocation.ProfileID, "claude-cli", "acceptance"); err != nil {
		return acceptance.Execution{}, err
	}
	if _, err := acceptance.ValidateInvocation(invocation); err != nil {
		return acceptance.Execution{}, err
	}
	model, err := decodeJSON(invocation.OutputSchema)
	if err != nil {
		return acceptance.Execution{}, err
	}
	schema, err := canonical.Marshal(claudeSchema(model))
	if err != nil {
		return acceptance.Execution{}, err
	}
	environment, err := buildEnvironment(invocation.Environment)
	if err != nil {
		return acceptance.Execution{}, err
	}
	directory := filepath.Join(runtime.root, "acceptances", invocation.InvocationID)
	if err := os.Mkdir(directory, 0o700); err != nil {
		return acceptance.Execution{}, err
	}
	metadata, err := canonical.Marshal(invocation.Record())
	if err != nil {
		return acceptance.Execution{}, err
	}
	if err := writeImmutable(filepath.Join(directory, "invocation.json"), metadata, 0o600); err != nil {
		return acceptance.Execution{}, err
	}
	eventsDir := filepath.Join(directory, "events")
	if err := os.Mkdir(eventsDir, 0o700); err != nil {
		return acceptance.Execution{}, err
	}
	runContext, cancel := context.WithTimeout(ctx, invocation.Timeout)
	defer cancel()
	limiter := &outputLimiter{remaining: invocation.MaxOutputBytes}
	stream := newStream(eventsDir, filepath.ToSlash(filepath.Join("acceptances", invocation.InvocationID)), sink, runtime.clock.Now, limiter, cancel)
	stderr := &boundedStderr{limiter: limiter, cancel: cancel}
	tools := "Read,Glob,Grep"
	allowedTools := tools
	if invocation.ExecutionConfig != nil {
		tools = strings.Join(invocation.ExecutionConfig.Tools, ",")
		allowedTools = strings.Join(invocation.ExecutionConfig.AllowedTools, ",")
	}
	arguments := []string{runtime.binary, "-p", "--input-format", "text", "--output-format", "stream-json", "--verbose", "--json-schema", string(schema), "--permission-mode", "dontAsk", "--tools", tools, "--allowedTools", allowedTools}
	arguments = append(arguments, executionArguments(invocation.ExecutionConfig)...)
	process, processErr := supervisor.Run(runContext, supervisor.Command{InvocationID: acceptance.ProcessID(invocation.InvocationID), Argv: arguments, Dir: invocation.WorkDir, Env: environmentList(environment), Stdin: strings.NewReader(invocation.Prompt), Stdout: stream, Stderr: stderr, GracePeriod: gracePeriod})
	stderrErr := stderr.persist(filepath.Join(directory, "stderr.log"))
	result, sessionID, resultErr := stream.finalizeAgent(min64(invocation.MaxOutputBytes, maxResultBytes))
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
		finalErr = fmt.Errorf("Claude CLI Acceptance exited %d: %w", process.ExitCode, processErr)
	case process.ExitCode != 0:
		finalErr = fmt.Errorf("Claude CLI Acceptance exited %d", process.ExitCode)
	}
	if finalErr != nil {
		return acceptance.Execution{}, finalErr
	}
	return acceptance.Execution{Result: result, SessionID: sessionID}, nil
}

var _ acceptance.Adapter = (*Adapter)(nil)
