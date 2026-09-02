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
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/monshunter/xgoal/internal/api"
	"github.com/monshunter/xgoal/internal/app"
	"github.com/monshunter/xgoal/internal/config"
)

const version = "dev"

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printUsage(stdout)
		return 0
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "xgoal %s\n", version)
		return 0
	case "config":
		return runConfig(args[1:], stdout, stderr)
	case "daemon":
		return runDaemon(args[1:], stderr)
	case "init", "doctor", "run", "status", "logs", "gates", "approve", "pause", "resume", "cancel", "report", "clean", "goal", "work":
		return runAPICommand(args, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) != 3 || args[0] != "validate" || args[1] != "--file" || args[2] == "" {
		fmt.Fprintln(stderr, "usage: xgoal config validate --file <path>")
		return 2
	}
	cfg, err := config.LoadFile(args[2])
	if err != nil {
		fmt.Fprintf(stderr, "invalid config: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "valid %s %s %q\n", cfg.APIVersion, cfg.Kind, cfg.Metadata.Name)
	return 0
}

func runDaemon(args []string, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "serve" {
		fmt.Fprintln(stderr, "usage: xgoal daemon serve [--project <path>] [--state-dir <path>] [--socket <path>]")
		return 2
	}
	project, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 5
	}
	if value, ok := option(args[1:], "--project"); ok {
		project = value
	}
	stateDir, _ := option(args[1:], "--state-dir")
	socket, _ := option(args[1:], "--socket")
	paths, err := app.ResolvePaths(project, stateDir, socket)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Serve(ctx, paths); err != nil {
		fmt.Fprintf(stderr, "daemon failed: %v\n", err)
		return 5
	}
	return 0
}

func runAPICommand(args []string, stdout, stderr io.Writer) int {
	client, err := currentClient()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	method, path, body, watch, err := commandRequest(args)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx := context.Background()
	if watch {
		ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		status, err := client.Stream(ctx, path, stdout)
		if err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(stderr, "API stream failed: %v\n", err)
			return 6
		}
		return exitCode(status, nil)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	key := ""
	if method != http.MethodGet {
		key, err = generatedID("request")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 5
		}
	}
	status, response, err := client.Do(ctx, method, path, key, body)
	if err != nil {
		fmt.Fprintf(stderr, "API request failed: %v\n", err)
		return 6
	}
	if len(response) > 0 {
		writer := stdout
		if status >= 400 {
			writer = stderr
		}
		prettyJSON(writer, response)
	}
	return exitCode(status, response)
}

func commandRequest(args []string) (method, path string, body any, watch bool, err error) {
	switch args[0] {
	case "init":
		return http.MethodPost, "/v1/projects/init", map[string]any{}, false, nil
	case "doctor":
		return http.MethodGet, "/v1/doctor", nil, false, nil
	case "run":
		goalFile, ok := option(args[1:], "--goal-file")
		if !ok {
			return "", "", nil, false, errors.New("usage: xgoal run --goal-file <path> [--mode fast|standard] [--id <goal-id>]")
		}
		rawGoal, readErr := os.ReadFile(goalFile)
		if readErr != nil {
			return "", "", nil, false, readErr
		}
		goalID, ok := option(args[1:], "--id")
		if !ok {
			goalID, readErr = generatedID("goal")
			if readErr != nil {
				return "", "", nil, false, readErr
			}
		}
		mode, ok := option(args[1:], "--mode")
		if !ok {
			mode = "standard"
		}
		return http.MethodPost, "/v1/goals", map[string]any{"goal_id": goalID, "raw_goal": string(rawGoal), "mode": mode}, false, nil
	case "status":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return "", "", nil, false, errors.New("usage: xgoal status <goal-id> [--watch] [--after-event-id <id>]")
		}
		if hasFlag(args[2:], "--watch") {
			path := "/v1/goals/" + args[1] + "/events?watch=1"
			if after, ok := option(args[2:], "--after-event-id"); ok {
				path += "&after_event_id=" + after
			}
			return http.MethodGet, path, nil, true, nil
		}
		return http.MethodGet, "/v1/goals/" + args[1], nil, false, nil
	case "logs":
		if len(args) != 2 {
			return "", "", nil, false, errors.New("usage: xgoal logs <attempt-id>")
		}
		return http.MethodGet, "/v1/attempts/" + args[1] + "/logs", nil, false, nil
	case "gates":
		if len(args) != 2 {
			return "", "", nil, false, errors.New("usage: xgoal gates <goal-id>")
		}
		return http.MethodGet, "/v1/goals/" + args[1] + "/gates?state=open", nil, false, nil
	case "approve":
		if len(args) < 2 {
			return "", "", nil, false, errors.New("usage: xgoal approve <gate-id> --version <n> --reason <text> [--decision ALLOW|DENY] [--by <actor>]")
		}
		version, parseErr := requiredVersion(args[2:])
		if parseErr != nil {
			return "", "", nil, false, parseErr
		}
		reason, ok := option(args[2:], "--reason")
		if !ok || strings.TrimSpace(reason) == "" {
			return "", "", nil, false, errors.New("--reason is required")
		}
		decision, ok := option(args[2:], "--decision")
		if !ok {
			decision = "ALLOW"
		}
		actor, ok := option(args[2:], "--by")
		if !ok || actor == "" {
			actor = os.Getenv("USER")
			if actor == "" {
				actor = "local-user"
			}
		}
		return http.MethodPost, "/v1/gates/" + args[1] + "/decisions", map[string]any{"expected_version": version, "decision": strings.ToUpper(decision), "decided_by": actor, "reason": reason}, false, nil
	case "pause", "resume", "cancel":
		if len(args) < 2 {
			return "", "", nil, false, fmt.Errorf("usage: xgoal %s <goal-id> --version <n> [--reason <text>]", args[0])
		}
		version, parseErr := requiredVersion(args[2:])
		if parseErr != nil {
			return "", "", nil, false, parseErr
		}
		reason, _ := option(args[2:], "--reason")
		return http.MethodPost, "/v1/goals/" + args[1] + "/" + args[0], map[string]any{"expected_version": version, "reason": reason}, false, nil
	case "report":
		if len(args) != 2 {
			return "", "", nil, false, errors.New("usage: xgoal report <goal-id>")
		}
		return http.MethodGet, "/v1/goals/" + args[1] + "/report", nil, false, nil
	case "clean":
		projectID := filepath.Base(mustWorkingDirectory())
		if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
			projectID = args[1]
		}
		return http.MethodPost, "/v1/projects/" + projectID + "/clean", map[string]any{"dry_run": hasFlag(args[1:], "--dry-run")}, false, nil
	case "goal":
		if len(args) >= 3 && args[1] == "get" {
			return http.MethodGet, "/v1/goals/" + args[2], nil, false, nil
		}
		if len(args) >= 3 && args[1] == "replan" {
			file, ok := option(args[3:], "--file")
			if !ok {
				return "", "", nil, false, errors.New("usage: xgoal goal replan <goal-id> --file <request.json>")
			}
			raw, readErr := os.ReadFile(file)
			if readErr != nil {
				return "", "", nil, false, readErr
			}
			var request any
			if json.Unmarshal(raw, &request) != nil {
				return "", "", nil, false, errors.New("replan request is invalid JSON")
			}
			return http.MethodPost, "/v1/goals/" + args[2] + "/replan", request, false, nil
		}
	case "work":
		if len(args) == 3 && args[1] == "list" {
			return http.MethodGet, "/v1/goals/" + args[2] + "/work-items", nil, false, nil
		}
		if len(args) >= 3 && args[1] == "retry" {
			version, parseErr := requiredVersion(args[3:])
			if parseErr != nil {
				return "", "", nil, false, parseErr
			}
			reason, _ := option(args[3:], "--reason")
			return http.MethodPost, "/v1/work-items/" + args[2] + "/retry", map[string]any{"expected_version": version, "reason": reason}, false, nil
		}
	}
	return "", "", nil, false, errors.New("invalid command arguments; run xgoal help")
}

func currentClient() (*api.Client, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	paths, err := app.ResolvePaths(workingDirectory, os.Getenv("XGOAL_STATE_DIR"), os.Getenv("XGOAL_SOCKET"))
	if err != nil {
		return nil, err
	}
	return api.NewUnixClient(paths.SocketPath, 5*time.Second)
}

func option(args []string, name string) (string, bool) {
	for index := 0; index < len(args); index++ {
		if args[index] == name && index+1 < len(args) {
			return args[index+1], true
		}
	}
	return "", false
}

func hasFlag(args []string, name string) bool {
	for _, argument := range args {
		if argument == name {
			return true
		}
	}
	return false
}

func requiredVersion(args []string) (int64, error) {
	value, ok := option(args, "--version")
	if !ok {
		return 0, errors.New("--version is required")
	}
	version, err := strconv.ParseInt(value, 10, 64)
	if err != nil || version <= 0 {
		return 0, errors.New("--version must be a positive integer")
	}
	return version, nil
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
		var goal struct {
			State string `json:"state"`
		}
		if json.Unmarshal(response, &goal) == nil {
			if goal.State == "WAITING" {
				return 3
			}
			if goal.State == "CANCELLED" {
				return 4
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

func mustWorkingDirectory() string {
	value, err := os.Getwd()
	if err != nil {
		return "."
	}
	return value
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "xgoal - evidence-closed coding orchestrator")
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Core commands:")
	fmt.Fprintln(writer, "  xgoal daemon serve")
	fmt.Fprintln(writer, "  xgoal init | doctor")
	fmt.Fprintln(writer, "  xgoal run --goal-file <path> [--mode fast|standard]")
	fmt.Fprintln(writer, "  xgoal status <goal-id> [--watch]")
	fmt.Fprintln(writer, "  xgoal logs <attempt-id> | gates <goal-id> | approve <gate-id>")
	fmt.Fprintln(writer, "  xgoal pause|resume|cancel <goal-id> --version <n>")
	fmt.Fprintln(writer, "  xgoal report <goal-id> | clean [project-id] [--dry-run]")
	fmt.Fprintln(writer, "  xgoal goal get|replan ... | work list|retry ...")
	fmt.Fprintln(writer, "  xgoal config validate --file <path> | version")
}
