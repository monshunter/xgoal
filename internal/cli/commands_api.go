package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/doctor"
	"github.com/spf13/cobra"
)

type doctorOptions struct {
	active  bool
	profile string
	timeout time.Duration
}

func newDoctorCommand(runtime runtime) *cobra.Command {
	var options doctorOptions
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Inspect local xgoal capabilities and policy boundaries",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			request, err := doctorRequest(options)
			if err != nil {
				return err
			}
			if !options.active {
				paths, err := commandPaths(cmd)
				if err != nil {
					return fail(2, err)
				}
				value := doctor.Inspect(cmd.Context(), paths)
				raw, err := json.Marshal(value)
				if err != nil {
					return fail(5, err)
				}
				prettyJSON(cmd.OutOrStdout(), raw)
				return nil
			}
			return runtime.executeAPI(cmd, request)
		},
	}
	cmd.Flags().BoolVar(&options.active, "active", false, "run an explicit provider transport probe")
	cmd.Flags().StringVar(&options.profile, "profile", "", "agent profile ID for an active probe")
	cmd.Flags().DurationVar(&options.timeout, "timeout", 0, "positive active probe timeout")
	return cmd
}

func doctorRequest(options doctorOptions) (requestSpec, error) {
	if !options.active {
		if options.profile != "" || options.timeout != 0 {
			return requestSpec{}, errors.New("--profile and --timeout require --active")
		}
		return requestSpec{method: http.MethodGet, path: "/v1/doctor"}, nil
	}
	if strings.TrimSpace(options.profile) == "" {
		return requestSpec{}, errors.New("--profile is required with --active")
	}
	if options.timeout.Milliseconds() <= 0 {
		return requestSpec{}, errors.New("--timeout must be a positive duration")
	}
	return requestSpec{
		method: http.MethodPost,
		path:   "/v1/doctor/active-probes",
		body: map[string]any{
			"profile_id":                     options.profile,
			"acknowledge_provider_transport": true,
			"timeout_milliseconds":           options.timeout.Milliseconds(),
		},
	}, nil
}

type runOptions struct {
	acceptanceFiles []string
	goalFile        string
	goal            string
	proposalFile    string
	mode            string
	goalID          string
	wait            bool
	format          string
}

func newRunCommand(runtime runtime) *cobra.Command {
	var options runOptions
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Create and start a Goal",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := checkFormat(options.format); err != nil {
				return err
			}
			if options.format == "human" && !options.wait {
				return errors.New("--format human on run requires --wait")
			}
			request, err := runRequest(cmd.InOrStdin(), options, runtime.newID)
			if err != nil {
				return err
			}
			request.human = options.format == "human"
			if options.wait && !cmd.Flags().Changed("format") && runtime.terminal != nil && runtime.terminal(cmd.ErrOrStderr()) {
				request.human = true
			}
			return runtime.executeAPI(cmd, request)
		},
	}
	cmd.Flags().StringVar(&options.goalFile, "goal-file", "", "read the Goal from a file, or - for stdin")
	cmd.Flags().StringArrayVar(&options.acceptanceFiles, "acceptance-file", nil, "optional repository acceptance document or script (repeatable)")
	cmd.Flags().StringVar(&options.goal, "goal", "", "Goal text")
	cmd.Flags().StringVar(&options.proposalFile, "proposal-file", "", "optional planner proposal JSON")
	cmd.Flags().StringVar(&options.mode, "mode", "", "execution mode: fast or standard")
	cmd.Flags().StringVar(&options.goalID, "id", "", "explicit Goal ID")
	cmd.Flags().BoolVar(&options.wait, "wait", false, "wait until the Goal completes, waits, or is cancelled")
	cmd.Flags().StringVar(&options.format, "format", "json", "wait feedback: json or human (terminal stderr shows progress by default; stdout stays JSON)")
	_ = cmd.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]cobra.Completion{"json", "human"}, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("mode", cobra.FixedCompletions([]cobra.Completion{"fast", "standard"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func runRequest(stdin io.Reader, options runOptions, newID func(string) (string, error)) (requestSpec, error) {
	if (options.goalFile == "") == (options.goal == "") {
		return requestSpec{}, errors.New("exactly one of --goal or --goal-file is required")
	}
	if options.mode != "" && options.mode != "fast" && options.mode != "standard" {
		return requestSpec{}, errors.New("--mode must be fast or standard")
	}
	rawGoal := []byte(options.goal)
	var err error
	if options.goalFile != "" {
		if options.goalFile == "-" {
			rawGoal, err = io.ReadAll(io.LimitReader(stdin, 1<<20))
		} else {
			rawGoal, err = os.ReadFile(options.goalFile)
		}
		if err != nil {
			return requestSpec{}, err
		}
	}
	goalID := options.goalID
	if goalID == "" {
		goalID, err = newID("goal")
		if err != nil {
			return requestSpec{}, fail(5, err)
		}
	}
	body := map[string]any{"goal_id": goalID, "raw_goal": string(rawGoal), "created_by": currentActor()}
	if len(options.acceptanceFiles) > 0 {
		body["acceptance_files"] = options.acceptanceFiles
	}
	if options.mode != "" {
		body["mode"] = options.mode
	}
	if options.proposalFile != "" {
		proposalBytes, readErr := os.ReadFile(options.proposalFile)
		if readErr != nil {
			return requestSpec{}, readErr
		}
		var proposal any
		if err := json.Unmarshal(proposalBytes, &proposal); err != nil {
			return requestSpec{}, errors.New("proposal file is invalid JSON")
		}
		body["proposal"] = proposal
	}
	return requestSpec{method: http.MethodPost, path: "/v1/goals", body: body, wait: options.wait, timeout: 30 * time.Minute}, nil
}

func newStatusCommand(runtime runtime) *cobra.Command {
	var watch bool
	var after, format string
	cmd := &cobra.Command{
		Use:   "status <goal-id>",
		Short: "Show Goal status or watch Goal events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkFormat(format); err != nil {
				return err
			}
			if format == "human" && after != "" {
				return errors.New("--after-event-id requires --format json")
			}
			if after != "" && !watch {
				return errors.New("--after-event-id requires --watch")
			}
			path := "/v1/goals/" + url.PathEscape(args[0])
			if watch && format != "human" {
				path += "/events?watch=1"
				if after != "" {
					path += "&after_event_id=" + url.QueryEscape(after)
				}
			}
			return runtime.executeAPI(cmd, requestSpec{method: http.MethodGet, path: path, watch: watch, human: format == "human"})
		},
	}
	cmd.Flags().BoolVar(&watch, "watch", false, "stream Goal events")
	cmd.Flags().StringVar(&after, "after-event-id", "", "resume the event stream after this event ID")
	cmd.Flags().StringVar(&format, "format", "json", "status presentation: json or human")
	_ = cmd.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]cobra.Completion{"json", "human"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func newLogsCommand(runtime runtime) *cobra.Command { return newInvocationLogsCommand(runtime) }

func newGatesCommand(runtime runtime) *cobra.Command {
	return getCommand("gates <goal-id>", "List open Human Gates for a Goal", func(id string) string {
		return "/v1/goals/" + url.PathEscape(id) + "/gates?state=open"
	}, runtime)
}

func newReportCommand(runtime runtime) *cobra.Command {
	return getCommand("report <goal-id>", "Show the final Goal report", func(id string) string {
		return "/v1/goals/" + url.PathEscape(id) + "/report"
	}, runtime)
}

func getCommand(use, short string, path func(string) string, runtime runtime) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runtime.executeAPI(cmd, requestSpec{method: http.MethodGet, path: path(args[0])})
		},
	}
}

type mutationOptions struct {
	version int64
	reason  string
}

func newApproveCommand(runtime runtime) *cobra.Command {
	var options mutationOptions
	var decision, actor string
	var resume bool
	var ownerVersion int64
	cmd := &cobra.Command{
		Use:   "approve <gate-id>",
		Short: "Record a finite decision for a Human Gate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if options.version <= 0 {
				return errors.New("--version must be a positive integer")
			}
			if strings.TrimSpace(options.reason) == "" {
				return errors.New("--reason is required")
			}
			decision = strings.ToUpper(decision)
			if decision != "ALLOW" && decision != "DENY" {
				return errors.New("--decision must be ALLOW or DENY")
			}
			if resume && (ownerVersion <= 0 || decision != "ALLOW") || !resume && cmd.Flags().Changed("owner-version") {
				return errors.New("--resume requires --decision ALLOW and a positive --owner-version")
			}
			if actor == "" {
				actor = currentActor()
			}
			request := requestSpec{
				method: http.MethodPost,
				path:   "/v1/gates/" + url.PathEscape(args[0]) + "/decisions",
				body: map[string]any{
					"expected_version": options.version,
					"decision":         decision,
					"decided_by":       actor,
					"reason":           options.reason,
				},
			}
			if resume {
				return runtime.decideAndResume(cmd, request, ownerVersion)
			}
			return runtime.executeAPI(cmd, request)
		},
	}
	cmd.Flags().Int64Var(&options.version, "version", 0, "expected Gate version")
	cmd.Flags().StringVar(&options.reason, "reason", "", "decision reason")
	cmd.Flags().StringVar(&decision, "decision", "ALLOW", "decision: ALLOW or DENY")
	cmd.Flags().StringVar(&actor, "by", "", "decision actor (defaults to the current user)")
	cmd.Flags().BoolVar(&resume, "resume", false, "resume this Gate's owner after recording ALLOW")
	cmd.Flags().Int64Var(&ownerVersion, "owner-version", 0, "expected Goal version for planning/final acceptance, or Work Item version")
	_ = cmd.MarkFlagRequired("version")
	_ = cmd.MarkFlagRequired("reason")
	_ = cmd.RegisterFlagCompletionFunc("decision", cobra.FixedCompletions([]cobra.Completion{"ALLOW", "DENY"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func newGoalControlCommand(runtime runtime, action string) *cobra.Command {
	var options mutationOptions
	cmd := &cobra.Command{
		Use:   action + " <goal-id>",
		Short: strings.ToUpper(action[:1]) + action[1:] + " a Goal",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if options.version <= 0 {
				return errors.New("--version must be a positive integer")
			}
			request := requestSpec{
				method: http.MethodPost,
				path:   "/v1/goals/" + url.PathEscape(args[0]) + "/" + action,
				body:   map[string]any{"expected_version": options.version, "reason": options.reason},
			}
			return runtime.executeAPI(cmd, request)
		},
	}
	cmd.Flags().Int64Var(&options.version, "version", 0, "expected Goal version")
	cmd.Flags().StringVar(&options.reason, "reason", "", "operation reason")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

func newCleanCommand(runtime runtime) *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "clean [project-id]",
		Short: "Clean safe, unreferenced runtime artifacts",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectID := filepath.Base(mustWorkingDirectory())
			if len(args) == 1 {
				projectID = args[0]
			}
			return runtime.executeAPI(cmd, requestSpec{
				method: http.MethodPost,
				path:   "/v1/projects/" + url.PathEscape(projectID) + "/clean",
				body:   map[string]any{"dry_run": dryRun},
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "report deletions without applying them")
	return cmd
}

func newGoalCommand(runtime runtime) *cobra.Command {
	parent := groupCommand("goal", "Inspect and revise Goals")
	parent.AddCommand(
		newGoalListCommand(runtime),
		getCommand("get <goal-id>", "Get one Goal", func(id string) string { return "/v1/goals/" + url.PathEscape(id) }, runtime),
		newGoalPlanCommand(runtime),
		newGoalFileCommand(runtime, "replan"),
		newGoalFileCommand(runtime, "finalize"),
	)
	return parent
}

func newGoalPlanCommand(runtime runtime) *cobra.Command {
	var options mutationOptions
	var proposalFile string
	cmd := &cobra.Command{
		Use:   "plan <goal-id>",
		Short: "Retry planning a draft Goal with a new planning generation",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if options.version <= 0 {
				return errors.New("--expected-version must be a positive integer")
			}
			if strings.TrimSpace(options.reason) == "" {
				return errors.New("--reason is required")
			}
			body := map[string]any{"expected_version": options.version, "reason": options.reason}
			if proposalFile != "" {
				raw, err := os.ReadFile(proposalFile)
				if err != nil {
					return err
				}
				var proposal any
				if err := json.Unmarshal(raw, &proposal); err != nil {
					return errors.New("proposal file is invalid JSON")
				}
				body["proposal"] = proposal
			}
			return runtime.executeAPI(cmd, requestSpec{method: http.MethodPost, path: "/v1/goals/" + url.PathEscape(args[0]) + "/plan", body: body})
		},
	}
	cmd.Flags().Int64Var(&options.version, "expected-version", 0, "expected Goal version")
	cmd.Flags().Int64Var(&options.version, "version", 0, "alias for --expected-version")
	cmd.MarkFlagsMutuallyExclusive("expected-version", "version")
	cmd.Flags().StringVar(&options.reason, "reason", "", "planning retry reason")
	cmd.Flags().StringVar(&proposalFile, "proposal-file", "", "optional reviewed planner proposal JSON")
	return cmd
}

func newGoalFileCommand(runtime runtime, action string) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   action + " <goal-id>",
		Short: strings.ToUpper(action[:1]) + action[1:] + " a Goal from a JSON request",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			var body any
			if err := json.Unmarshal(raw, &body); err != nil {
				return fmt.Errorf("%s request is invalid JSON", action)
			}
			return runtime.executeAPI(cmd, requestSpec{method: http.MethodPost, path: "/v1/goals/" + url.PathEscape(args[0]) + "/" + action, body: body})
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to the JSON request")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func newWorkCommand(runtime runtime) *cobra.Command {
	parent := groupCommand("work", "Inspect and control Work Items")
	parent.AddCommand(
		getCommand("get <work-id>", "Read Work Item state and current version", func(id string) string { return "/v1/work-items/" + url.PathEscape(id) }, runtime),
		getCommand("list <goal-id>", "List Work Items for a Goal", func(id string) string { return "/v1/goals/" + url.PathEscape(id) + "/work-items" }, runtime),
		newWorkControlCommand(runtime, "retry"),
		newWorkControlCommand(runtime, "cancel"),
	)
	return parent
}

func newGateCommand(runtime runtime) *cobra.Command {
	parent := groupCommand("gate", "Inspect a Human Gate")
	parent.AddCommand(getCommand("get <gate-id>", "Read Gate facts and current version", func(id string) string { return "/v1/gates/" + url.PathEscape(id) }, runtime))
	parent.AddCommand(newGateResumeCommand(runtime))
	return parent
}

func newWorkControlCommand(runtime runtime, action string) *cobra.Command {
	var options mutationOptions
	cmd := &cobra.Command{
		Use:   action + " <work-id>",
		Short: strings.ToUpper(action[:1]) + action[1:] + " a Work Item",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if options.version <= 0 {
				return errors.New("--version must be a positive integer")
			}
			return runtime.executeAPI(cmd, requestSpec{
				method: http.MethodPost,
				path:   "/v1/work-items/" + url.PathEscape(args[0]) + "/" + action,
				body:   map[string]any{"expected_version": options.version, "reason": options.reason},
			})
		},
	}
	cmd.Flags().Int64Var(&options.version, "version", 0, "expected Work Item version")
	cmd.Flags().StringVar(&options.reason, "reason", "", "operation reason")
	_ = cmd.MarkFlagRequired("version")
	return cmd
}

func mustWorkingDirectory() string {
	value, err := os.Getwd()
	if err != nil {
		return "."
	}
	return value
}
