package claude

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	baseadapter "github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/protocol"
	reviewcontract "github.com/monshunter/xgoal/internal/review"
	"github.com/monshunter/xgoal/internal/supervisor"
)

func (runtime *Adapter) Review(ctx context.Context, invocation reviewcontract.Invocation, sink baseadapter.EventSink) (reviewcontract.Execution, error) {
	packet, err := reviewcontract.ValidateInvocation(invocation)
	if err != nil {
		return reviewcontract.Execution{}, err
	}
	schema, schemaHash, err := validateSchema(invocation.OutputSchema, protocol.SchemaReviewResult, protocol.ReviewResultVersion)
	if err != nil {
		return reviewcontract.Execution{}, err
	}
	environment, err := buildEnvironment(invocation.Environment)
	if err != nil {
		return reviewcontract.Execution{}, err
	}
	directory := filepath.Join(runtime.root, "reviews", invocation.InvocationID)
	if err := os.Mkdir(directory, 0o700); err != nil {
		return reviewcontract.Execution{}, err
	}
	eventsDir := filepath.Join(directory, "events")
	if err := os.Mkdir(eventsDir, 0o700); err != nil {
		return reviewcontract.Execution{}, err
	}
	metadata := struct {
		ProtocolVersion string   `json:"protocol_version"`
		InvocationID    string   `json:"invocation_id"`
		ReviewID        string   `json:"review_id"`
		PacketHash      string   `json:"packet_hash"`
		PacketPath      string   `json:"packet_path"`
		ReviewerProfile string   `json:"reviewer_profile_id"`
		Implementer     string   `json:"implementation_profile_id"`
		CandidateTree   string   `json:"candidate_tree"`
		PermissionMode  string   `json:"permission_mode"`
		Tools           []string `json:"tools"`
		SchemaHash      string   `json:"schema_hash"`
	}{"xgoal.claude-review-invocation/v1alpha1", invocation.InvocationID, packet.ID, invocation.PacketHash, invocation.PacketPath, invocation.ReviewerProfileID, invocation.ImplementationProfileID, invocation.CandidateTree, invocation.PermissionMode, append([]string(nil), invocation.Tools...), schemaHash}
	content, err := canonical.Marshal(metadata)
	if err != nil {
		return reviewcontract.Execution{}, err
	}
	if err := writeImmutable(filepath.Join(directory, "invocation.json"), content, 0o600); err != nil {
		return reviewcontract.Execution{}, err
	}
	runContext, cancel := context.WithTimeout(ctx, invocation.Timeout)
	defer cancel()
	limiter := &outputLimiter{remaining: invocation.MaxOutputBytes}
	stream := newStream(eventsDir, filepath.ToSlash(filepath.Join("reviews", invocation.InvocationID)), sink, runtime.clock.Now, limiter, cancel)
	stderr := &boundedStderr{limiter: limiter, cancel: cancel}
	tools := strings.Join(invocation.Tools, ",")
	arguments := []string{runtime.binary, "-p", "--input-format", "text", "--output-format", "stream-json", "--verbose", "--json-schema", string(schema), "--permission-mode", invocation.PermissionMode, "--tools", tools, "--allowedTools", tools}
	process, processErr := supervisor.Run(runContext, supervisor.Command{Argv: arguments, Dir: invocation.WorkDir, Env: environmentList(environment), Stdin: strings.NewReader(invocation.Prompt), Stdout: stream, Stderr: stderr, GracePeriod: gracePeriod})
	stderrErr := stderr.persist(filepath.Join(directory, "stderr.log"))
	result, sessionID, resultErr := stream.finalizeReview(min64(invocation.MaxOutputBytes, maxResultBytes))
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
		finalErr = fmt.Errorf("Claude CLI review exited %d: %w", process.ExitCode, processErr)
	case process.ExitCode != 0:
		finalErr = fmt.Errorf("Claude CLI review exited %d", process.ExitCode)
	}
	if finalErr != nil {
		return reviewcontract.Execution{}, finalErr
	}
	resultContent, err := canonical.Marshal(result)
	if err != nil {
		return reviewcontract.Execution{}, err
	}
	if err := writeImmutable(filepath.Join(directory, "result.json"), resultContent, 0o600); err != nil {
		return reviewcontract.Execution{}, err
	}
	return reviewcontract.Execution{Result: result, SessionID: sessionID}, nil
}

func validateReviewFixture(content []byte) error {
	if len(bytes.TrimSpace(content)) == 0 {
		return errors.New("empty review fixture")
	}
	return nil
}

var _ reviewcontract.Adapter = (*Adapter)(nil)
