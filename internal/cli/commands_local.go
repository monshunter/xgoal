package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/monshunter/xgoal/internal/app"
	"github.com/monshunter/xgoal/internal/benchmark"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/projectinit"
	"github.com/spf13/cobra"
)

func newInitCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize xgoal in the current Git repository",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := commandPaths(cmd)
			if err != nil {
				return fail(2, err)
			}
			result, err := projectinit.Initialize(cmd.Context(), projectinit.Options{ProjectRoot: paths.ProjectRoot, StateDir: paths.StateDir, SocketPath: paths.SocketPath})
			if err != nil {
				return fail(5, fmt.Errorf("init failed: %w", err))
			}
			raw, err := json.Marshal(result)
			if err != nil {
				return fail(5, err)
			}
			prettyJSON(cmd.OutOrStdout(), raw)
			return nil
		},
	}
}

func newConfigCommand() *cobra.Command {
	parent := groupCommand("config", "Inspect and validate xgoal configuration")
	var file string
	validate := &cobra.Command{
		Use:   "validate",
		Short: "Validate an xgoal project configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.LoadFile(file)
			if err != nil {
				return fail(2, fmt.Errorf("invalid config: %w", err))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "valid %s %s %q\n", cfg.APIVersion, cfg.Kind, cfg.Metadata.Name)
			return nil
		},
	}
	validate.Flags().StringVar(&file, "file", "", "path to the configuration file")
	_ = validate.MarkFlagRequired("file")
	parent.AddCommand(validate)
	return parent
}

func newBenchmarkCommand(runtime runtime) *cobra.Command {
	parent := groupCommand("benchmark", "Validate and run reproducible benchmark suites")
	parent.AddCommand(newBenchmarkValidateCommand(), newBenchmarkRunCommand(runtime))
	return parent
}

func newBenchmarkValidateCommand() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate release coverage for a benchmark suite",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			suite, err := benchmark.Load(file)
			if err != nil {
				return fail(2, fmt.Errorf("invalid benchmark suite: %w", err))
			}
			if err := suite.ValidateReleaseCoverage(); err != nil {
				return fail(2, fmt.Errorf("invalid release benchmark suite: %w", err))
			}
			hash, _ := suite.Hash()
			identities, _ := suite.ComparisonIdentities()
			pretty, err := json.Marshal(map[string]any{"valid": true, "suite_hash": hash, "comparison_identities": identities, "execution": "NOT_RUN", "upload": false})
			if err != nil {
				return fail(5, err)
			}
			prettyJSON(cmd.OutOrStdout(), pretty)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to the benchmark suite")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

func newBenchmarkRunCommand(runtime runtime) *cobra.Command {
	var file, taskID, groupID string
	var runNumber int64
	cmd := &cobra.Command{
		Use:   "run --file <suite.json> --task <id> --group <id> [--run <n>] -- <argv...>",
		Short: "Run one benchmark task and group",
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() != 0 || len(args) == 0 {
				return errors.New("runner argv is required after --")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if runNumber <= 0 {
				return errors.New("--run must be a positive integer")
			}
			suite, err := benchmark.Load(file)
			if err != nil {
				return fail(2, fmt.Errorf("invalid benchmark suite: %w", err))
			}
			projected, err := benchmark.ProjectTask(suite, taskID)
			if err != nil {
				return fail(2, err)
			}
			result, runErr := benchmark.Run(cmd.Context(), projected, groupID, runNumber, benchmark.CommandRunner{Argv: args}, runtime.now)
			if runErr != nil && result.TaskID == "" {
				return fail(5, fmt.Errorf("benchmark execution failed: %w", runErr))
			}
			suiteHash, _ := suite.Hash()
			report, err := benchmark.NewResultReport(suiteHash, []benchmark.RunResult{result}, runtime.now().UTC().Format(time.RFC3339Nano))
			if err != nil {
				return fail(5, err)
			}
			jsonBytes, _, err := report.Render()
			if err != nil {
				return fail(5, err)
			}
			prettyJSON(cmd.OutOrStdout(), jsonBytes)
			if !result.FinalAcceptancePass {
				return silentStatus(5)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to the benchmark suite")
	cmd.Flags().StringVar(&taskID, "task", "", "benchmark task ID")
	cmd.Flags().StringVar(&groupID, "group", "", "comparison group ID")
	cmd.Flags().Int64Var(&runNumber, "run", 1, "run number")
	_ = cmd.MarkFlagRequired("file")
	_ = cmd.MarkFlagRequired("task")
	_ = cmd.MarkFlagRequired("group")
	return cmd
}

func commandPaths(cmd *cobra.Command) (app.Paths, error) {
	project, _ := cmd.Flags().GetString("project")
	state, _ := cmd.Flags().GetString("state-dir")
	socket, _ := cmd.Flags().GetString("socket")
	return app.ResolvePaths(project, state, socket)
}

func newDaemonCommand() *cobra.Command {
	parent := groupCommand("daemon", "Manage the current project's local daemon")
	serve := &cobra.Command{Use: "serve", Short: "Run the project's Unix Socket API in the foreground", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		paths, err := commandPaths(cmd)
		if err != nil {
			return fail(2, err)
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := app.Serve(ctx, paths); err != nil {
			return fail(5, fmt.Errorf("daemon failed: %w", err))
		}
		return nil
	}}
	status := &cobra.Command{Use: "status", Short: "Inspect daemon identity and readiness without opening its database", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		paths, err := commandPaths(cmd)
		if err != nil {
			return fail(2, err)
		}
		value, err := app.Status(cmd.Context(), paths)
		raw, _ := json.Marshal(value)
		prettyJSON(cmd.OutOrStdout(), raw)
		if err != nil {
			return fail(6, err)
		}
		if value.State != "READY" {
			return silentStatus(6)
		}
		return nil
	}}
	parent.AddCommand(serve, status)
	for _, action := range []string{"start", "stop"} {
		var timeout time.Duration
		child := &cobra.Command{Use: action, Short: action + " the project's background daemon", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if timeout <= 0 {
				return errors.New("--timeout must be a positive duration")
			}
			paths, err := commandPaths(cmd)
			if err != nil {
				return fail(2, err)
			}
			signalContext, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			ctx, cancel := context.WithTimeout(signalContext, timeout)
			defer cancel()
			var value any
			if action == "start" {
				value, err = app.Start(ctx, paths, "")
			} else {
				value, err = app.Stop(ctx, paths)
			}
			raw, _ := json.Marshal(value)
			prettyJSON(cmd.OutOrStdout(), raw)
			if err != nil {
				return fail(6, err)
			}
			return nil
		}}
		child.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "maximum time to wait for daemon lifecycle completion")
		parent.AddCommand(child)
	}
	return parent
}
