package benchmark

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/monshunter/xgoal/internal/redact"
)

const runnerResultVersion = "xgoal.benchmark-runner-result/v1"

// CommandRunner runs an explicitly supplied argv without a shell. It treats exit status as
// process fact only; completion is claimed solely through a strict sidecar result.
type CommandRunner struct {
	Argv      []string
	Env       []string
	MaxOutput int64
}

type commandResult struct {
	ProtocolVersion    string `json:"protocol_version"`
	ClaimedCompleted   bool   `json:"claimed_completed"`
	Attempts           int64  `json:"attempts"`
	HumanInterventions int64  `json:"human_interventions"`
	RegressionFailures int64  `json:"regression_failures"`
	RecoverySuccess    bool   `json:"recovery_success"`
	NoProgressAttempts int64  `json:"no_progress_attempts"`
}

func (runner CommandRunner) Run(ctx context.Context, request RunRequest) (RunnerResult, error) {
	if len(runner.Argv) == 0 || !filepath.IsAbs(request.Workspace) {
		return RunnerResult{}, errors.New("command runner requires argv and absolute workspace")
	}
	maxOutput := runner.MaxOutput
	if maxOutput <= 0 {
		maxOutput = 1 << 20
	}
	argv := make([]string, len(runner.Argv))
	for index, value := range runner.Argv {
		argv[index] = strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(value, "{workspace}", request.Workspace), "{goal}", request.Goal), "{result}", filepath.Join(request.Workspace, ".xgoal-benchmark-run.json"))
	}
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir = request.Workspace
	command.Env = append(os.Environ(), runner.Env...)
	var output limitedBuffer
	output.limit = maxOutput
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	result := RunnerResult{ExitCode: exitCode(err), Failure: redact.String(output.String())}
	sidecar, sidecarErr := readCommandResult(filepath.Join(request.Workspace, ".xgoal-benchmark-run.json"))
	if sidecarErr == nil {
		result.ClaimedCompleted = sidecar.ClaimedCompleted
		result.Attempts = sidecar.Attempts
		result.HumanInterventions = sidecar.HumanInterventions
		result.RegressionFailures = sidecar.RegressionFailures
		result.RecoverySuccess = sidecar.RecoverySuccess
		result.NoProgressAttempts = sidecar.NoProgressAttempts
	} else if err == nil {
		result.Failure = strings.TrimSpace(strings.Join([]string{result.Failure, "runner result unavailable: " + sidecarErr.Error()}, "; "))
	}
	return result, err
}

func readCommandResult(path string) (commandResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return commandResult{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 64<<10))
	decoder.DisallowUnknownFields()
	var result commandResult
	if err := decoder.Decode(&result); err != nil {
		return commandResult{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return commandResult{}, errors.New("runner result contains trailing JSON")
	}
	if result.ProtocolVersion != runnerResultVersion || result.Attempts < 0 || result.HumanInterventions < 0 || result.RegressionFailures < 0 || result.NoProgressAttempts < 0 || result.NoProgressAttempts > result.Attempts {
		return commandResult{}, errors.New("invalid benchmark runner result")
	}
	return result, nil
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int64
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := buffer.limit - int64(buffer.buffer.Len())
	if remaining > 0 {
		keep := int64(len(value))
		if keep > remaining {
			keep = remaining
		}
		_, _ = buffer.buffer.Write(value[:keep])
	}
	return written, nil
}

func (buffer *limitedBuffer) String() string {
	value := buffer.buffer.String()
	if int64(buffer.buffer.Len()) >= buffer.limit {
		value += " [truncated]"
	}
	return value
}

func RunnerSidecarSchema() string {
	return fmt.Sprintf(`{"protocol_version":%q,"claimed_completed":true,"attempts":1,"human_interventions":0,"regression_failures":0,"recovery_success":false,"no_progress_attempts":0}`, runnerResultVersion)
}
