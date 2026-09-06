package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/app"
	"github.com/spf13/cobra"
)

const version = api.SoftwareVersion

type apiClient interface {
	Do(context.Context, string, string, string, any) (int, []byte, error)
	Stream(context.Context, string, io.Writer) (int, error)
}

type runtime struct {
	terminal  func(io.Writer) bool
	newClient func() (apiClient, error)
	newID     func(string) (string, error)
	now       func() time.Time
}

type commandError struct {
	code   int
	err    error
	silent bool
}

func (err *commandError) Error() string {
	if err.err == nil {
		return "command failed"
	}
	return err.err.Error()
}

func (err *commandError) Unwrap() error { return err.err }

func fail(code int, err error) error {
	return &commandError{code: code, err: err}
}

func silentStatus(code int) error {
	if code == 0 {
		return nil
	}
	return &commandError{code: code, silent: true}
}

// Run executes xgoal with explicit output streams and returns the stable process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	dependencies := runtime{
		terminal: terminalWriter,
		newID:    generatedID,
		now:      time.Now,
	}
	return execute(args, os.Stdin, stdout, stderr, dependencies)
}

func execute(args []string, stdin io.Reader, stdout, stderr io.Writer, runtime runtime) int {
	root := newRootCommand(runtime)
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetContext(context.Background())
	if err := root.Execute(); err != nil {
		code := 2
		var commandErr *commandError
		if errors.As(err, &commandErr) {
			code = commandErr.code
			if commandErr.silent {
				return code
			}
		}
		fmt.Fprintln(stderr, err)
		return code
	}
	return 0
}

func newRootCommand(runtime runtime) *cobra.Command {
	root := &cobra.Command{
		Use:           "xgoal",
		Short:         "Evidence-closed coding orchestrator",
		SilenceErrors: true,
		SilenceUsage:  true,
		Version:       version,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	var projectPath, stateDir, socketPath string
	root.PersistentFlags().StringVar(&projectPath, "project", "", "project directory (or XGOAL_PROJECT)")
	root.PersistentFlags().StringVar(&stateDir, "state-dir", "", "project state directory (or XGOAL_STATE_DIR)")
	root.PersistentFlags().StringVar(&socketPath, "socket", "", "Unix socket path (or XGOAL_SOCKET)")
	if runtime.newClient == nil {
		runtime.newClient = func() (apiClient, error) {
			paths, err := app.ResolvePaths(projectPath, stateDir, socketPath)
			if err != nil {
				return nil, err
			}
			return api.NewProjectClient(paths.SocketPath, 5*time.Second, app.ExpectedIdentity(paths))
		}
	}
	root.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	root.AddCommand(
		newInitCommand(),
		newDoctorCommand(runtime),
		newRunCommand(runtime),
		newStatusCommand(runtime),
		newLogsCommand(runtime),
		newInvocationsCommand(runtime),
		newContextCommand(runtime),
		newIDsCommand(runtime),
		newGateCommand(runtime),
		newGatesCommand(runtime),
		newApproveCommand(runtime),
		newGoalControlCommand(runtime, "pause"),
		newGoalControlCommand(runtime, "resume"),
		newGoalControlCommand(runtime, "cancel"),
		newReportCommand(runtime),
		newExportCommand(runtime),
		newCleanCommand(runtime),
		newConfigCommand(),
		newBenchmarkCommand(runtime),
		newDaemonCommand(),
		newGoalCommand(runtime),
		newWorkCommand(runtime),
		newVersionCommand(),
	)
	configureIdentifierCompletion(root, runtime)
	return root
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the xgoal version",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintf(cmd.OutOrStdout(), "xgoal %s\n", version)
		},
	}
}

func groupCommand(use, short string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
}

type requestSpec struct {
	method  string
	path    string
	body    any
	watch   bool
	wait    bool
	human   bool
	timeout time.Duration
}

func (runtime runtime) executeAPI(cmd *cobra.Command, request requestSpec) error {
	client, err := runtime.newClient()
	if err != nil {
		return fail(2, err)
	}
	if closer, ok := client.(interface{ Close() }); ok {
		defer closer.Close()
	}
	ctx := cmd.Context()
	if request.watch {
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		if request.human {
			return runtime.watchHumanGoal(ctx, cmd, client, request.path)
		}
		status, streamErr := client.Stream(ctx, request.path, cmd.OutOrStdout())
		if streamErr != nil && !errors.Is(streamErr, context.Canceled) {
			return fail(6, fmt.Errorf("API stream failed: %w", streamErr))
		}
		return silentStatus(exitCode(status, nil))
	}
	if request.wait {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
	} else {
		timeout := request.timeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	key := ""
	if request.method != http.MethodGet {
		key, err = runtime.newID("request")
		if err != nil {
			return fail(5, err)
		}
	}
	status, response, err := client.Do(ctx, request.method, request.path, key, request.body)
	if err != nil {
		return fail(6, fmt.Errorf("API request failed: %w", err))
	}
	if len(response) > 0 {
		writer := cmd.OutOrStdout()
		if status >= 400 {
			writer = cmd.ErrOrStderr()
		}
		if request.human && !request.wait && status >= 200 && status < 300 {
			text, err := renderHumanGoal(response, humanCommand(cmd))
			if err != nil {
				return fail(5, err)
			}
			if _, err := fmt.Fprint(writer, text); err != nil {
				return fail(5, err)
			}
		} else {
			prettyJSON(writer, response)
		}
	}
	if request.wait && status >= 200 && status < 300 {
		var feedback *humanFeedback
		if request.human {
			feedback = &humanFeedback{command: humanCommand(cmd)}
			if err := feedback.write(cmd.ErrOrStderr(), response, runtime.now(), true); err != nil {
				return fail(5, err)
			}
		}
		goalID, state, waiting, decodeErr := createdGoalState(response)
		if decodeErr != nil {
			return fail(5, fmt.Errorf("invalid Goal creation response: %w", decodeErr))
		}
		switch {
		case waiting || state == "WAITING":
			return silentStatus(3)
		case state == "CANCELLED":
			return silentStatus(4)
		case state == "COMPLETED":
			return nil
		default:
			return runtime.waitForGoal(ctx, cmd, client, goalID, feedback)
		}
	}
	return silentStatus(exitCode(status, response))
}

func (runtime runtime) waitForGoal(ctx context.Context, cmd *cobra.Command, client apiClient, goalID string, feedback *humanFeedback) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		status, response, err := readGoalObservation(ctx, client, "/v1/goals/"+url.PathEscape(goalID))
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return silentStatus(5)
			}
			return fail(6, fmt.Errorf("Goal wait failed: %w", err))
		}
		if status < 200 || status >= 300 {
			prettyJSON(cmd.ErrOrStderr(), response)
			return silentStatus(exitCode(status, response))
		}
		_, state, waiting, decodeErr := createdGoalState(response)
		if decodeErr != nil {
			return fail(5, fmt.Errorf("invalid Goal status response: %w", decodeErr))
		}
		terminal := waiting || state == "WAITING" || state == "CANCELLED" || state == "COMPLETED"
		if feedback != nil {
			if err := feedback.write(cmd.ErrOrStderr(), response, runtime.now(), terminal); err != nil {
				return fail(5, err)
			}
		}
		if terminal {
			prettyJSON(cmd.OutOrStdout(), response)
			return silentStatus(exitCode(status, response))
		}
		select {
		case <-ctx.Done():
			return silentStatus(5)
		case <-ticker.C:
		}
	}
}

type goalProjection struct {
	GoalID              string `json:"goal_id"`
	State               string `json:"state"`
	PlanningState       string `json:"planning_state"`
	PlannerGateRequired bool   `json:"planner_gate_required"`
	ExecutionBlocker    string `json:"execution_blocker"`
	Gates               []struct {
		State    string `json:"state"`
		Required bool   `json:"required"`
	} `json:"gates"`
}

func (goal goalProjection) waiting() bool {
	if goal.State == "COMPLETED" || goal.State == "CANCELLED" || goal.State == "" {
		return false
	}
	if goal.State == "WAITING" || goal.PlanningState == "WAITING" || goal.PlanningState == "PAUSED" || goal.PlannerGateRequired || goal.ExecutionBlocker != "" {
		return true
	}
	for _, gate := range goal.Gates {
		if gate.Required && gate.State == "OPEN" {
			return true
		}
	}
	return false
}

func createdGoalState(response []byte) (goalID, state string, waiting bool, err error) {
	var value goalProjection
	if err := json.Unmarshal(response, &value); err != nil || value.GoalID == "" {
		return "", "", false, errors.New("goal_id is missing")
	}
	return value.GoalID, value.State, value.waiting(), nil
}

func generatedID(prefix string) (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(bytes), nil
}

func prettyJSON(writer io.Writer, raw []byte) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		_, _ = writer.Write(raw)
		_, _ = fmt.Fprintln(writer)
		return
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func exitCode(status int, response []byte) int {
	if status >= 200 && status < 300 {
		var goal goalProjection
		if json.Unmarshal(response, &goal) == nil {
			if goal.State == "CANCELLED" {
				return 4
			}
			if goal.waiting() {
				return 3
			}
		}
		return 0
	}
	switch status {
	case http.StatusBadRequest:
		return 2
	case http.StatusForbidden:
		return 7
	case http.StatusTooManyRequests:
		return 3
	case http.StatusServiceUnavailable:
		return 6
	default:
		return 5
	}
}

func currentActor() string {
	if value := os.Getenv("USER"); value != "" {
		return value
	}
	return "local-user"
}
