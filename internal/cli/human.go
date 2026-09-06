package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/mattn/go-isatty"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/redact"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/spf13/cobra"
)

func terminalWriter(writer io.Writer) bool {
	file, ok := writer.(interface{ Fd() uintptr })
	return ok && isatty.IsTerminal(file.Fd())
}

func checkFormat(format string) error {
	if format != "json" && format != "human" {
		return errors.New("--format must be json or human")
	}
	return nil
}

type humanGoal struct {
	Acceptance *struct {
		State      string   `json:"state"`
		Policy     string   `json:"policy"`
		InputFiles []string `json:"input_files"`
		Criteria   int      `json:"criteria"`
		Generated  int      `json:"generated_validators"`
	} `json:"acceptance_preparation"`
	GoalID           string              `json:"goal_id"`
	State            string              `json:"state"`
	Version          int64               `json:"version"`
	PlanningState    string              `json:"planning_state"`
	PlanningBlocker  string              `json:"planning_blocker"`
	ExecutionBlocker string              `json:"execution_blocker"`
	WorkItems        []domain.WorkItem   `json:"work_items"`
	Gates            []domain.Gate       `json:"gates"`
	Activity         sqlite.GoalActivity `json:"activity"`
}

// Terminal text is a presentation of public facts. Strip control characters so
// Agent text cannot forge lines or execute terminal escape sequences.
func terminalText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, redact.String(value))
}

func renderHumanGoal(response []byte, command string) (string, error) {
	if command == "" {
		command = "xgoal"
	}
	var goal humanGoal
	if err := json.Unmarshal(response, &goal); err != nil || goal.GoalID == "" {
		return "", errors.New("invalid Goal status response")
	}
	var out strings.Builder
	fmt.Fprintf(&out, "Goal %s · %s · version %d\n", terminalText(goal.GoalID), terminalText(goal.State), goal.Version)
	if goal.PlanningState != "" {
		fmt.Fprintf(&out, "Planning: %s\n", terminalText(goal.PlanningState))
	}
	if a := goal.Acceptance; a != nil {
		fmt.Fprintf(&out, "Acceptance: %s · policy %s · %d input files · %d criteria · %d generated checks\n", terminalText(a.State), terminalText(a.Policy), len(a.InputFiles), a.Criteria, a.Generated)
	}
	for _, blocker := range []string{goal.PlanningBlocker, goal.ExecutionBlocker} {
		if blocker != "" {
			fmt.Fprintf(&out, "Blocked: %s\n", terminalText(blocker))
		}
	}
	for _, work := range goal.WorkItems {
		fmt.Fprintf(&out, "Work %s · %s · version %d · %s\n", terminalText(work.ID), terminalText(string(work.State)), work.Version, terminalText(work.Title))
	}
	activity := goal.Activity
	if activity.ObservationError != "" {
		fmt.Fprintf(&out, "Observation unavailable: %s\n", terminalText(activity.ObservationError))
	}
	fmt.Fprintf(&out, "Heartbeat: %s (lease signal; process liveness is not inferred)\n", humanTime(activity.HeartbeatAt))
	fmt.Fprintf(&out, "Last output: %s\n", humanTime(activity.LastOutputAt))
	fmt.Fprintf(&out, "Material progress: %s", humanTime(activity.LastMaterialProgressAt))
	if activity.MaterialProgressKind != "" {
		fmt.Fprintf(&out, " (%s)", terminalText(activity.MaterialProgressKind))
	}
	out.WriteByte('\n')
	if call := activity.LatestInvocation; call != nil {
		fmt.Fprintf(&out, "Invocation %s · %s · %s · %s\n", terminalText(call.ID), terminalText(call.Role), terminalText(call.ProfileID), terminalText(call.Observation.Status))
		for _, problem := range []string{call.Observation.Unavailable, call.Observation.LogError} {
			if problem != "" {
				fmt.Fprintf(&out, "Log warning: %s\n", terminalText(problem))
			}
		}
	}
	if event := activity.LatestEvent; event != nil {
		fmt.Fprintf(&out, "Latest event: %s · %s\n", terminalText(event.Type), humanTime(&event.At))
	}
	gatePending := false
	approvedGateID := ""
	for _, gate := range goal.Gates {
		if gate.State == domain.GateOpen || gate.Required && (gate.State == domain.GateDenied || gate.State == domain.GateRevoked) {
			fmt.Fprintf(&out, "Gate %s · %s · version %d · %s\n", terminalText(gate.ID), terminalText(string(gate.State)), gate.Version, terminalText(gate.ReasonCode))
			gatePending = true
		}
		if gate.Required && gate.State == domain.GateApproved && gate.Used == 0 {
			fmt.Fprintf(&out, "Gate %s · APPROVED · version %d · answer retained, continuation pending\n", terminalText(gate.ID), gate.Version)
			if approvedGateID == "" {
				approvedGateID = gate.ID
			}
		}
	}
	// A read action never presumes a Gate decision or a retry is safe.
	switch {
	case goal.State == "COMPLETED":
		fmt.Fprintf(&out, "Next: %s report %s\n", command, shellArg(goal.GoalID))
	case !gatePending && approvedGateID != "":
		fmt.Fprintf(&out, "Next: %s gate get %s\n", command, shellArg(approvedGateID))
	case gatePending || goal.PlanningState == "WAITING":
		fmt.Fprintf(&out, "Next: %s gates %s\n", command, shellArg(goal.GoalID))
	case activity.LatestInvocation != nil:
		call := activity.LatestInvocation
		if call.Observation.Status == "running" {
			fmt.Fprintf(&out, "Next: %s logs --invocation %s --follow\n", command, shellArg(call.ID))
		} else {
			fmt.Fprintf(&out, "Next: %s context %s\n", command, shellArg(call.ID))
		}
	default:
		fmt.Fprintf(&out, "Next: %s status %s\n", command, shellArg(goal.GoalID))
	}
	return out.String(), nil
}

func humanTime(at *time.Time) string {
	if at == nil || at.IsZero() {
		return "unknown"
	}
	return at.UTC().Format(time.RFC3339Nano)
}

func shellArg(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.')
	}) == -1 {
		return value
	}
	return "'" + strings.ReplaceAll(terminalText(value), "'", "'\"'\"'") + "'"
}

type humanFeedback struct {
	command  string
	previous string
	lastAt   time.Time
}

func (feedback *humanFeedback) write(writer io.Writer, response []byte, now time.Time, force bool) error {
	text, err := renderHumanGoal(response, feedback.command)
	if err != nil {
		return err
	}
	elapsed := now.Sub(feedback.lastAt)
	if !force && feedback.previous != "" && (elapsed < time.Second || text == feedback.previous && elapsed < 15*time.Second) {
		return nil
	}
	if _, err := fmt.Fprint(writer, text); err != nil {
		return err
	}
	feedback.previous, feedback.lastAt = text, now
	return nil
}

func (runtime runtime) watchHumanGoal(ctx context.Context, cmd *cobra.Command, client apiClient, path string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	feedback := humanFeedback{command: humanCommand(cmd)}
	lastStatus := 0
	selectedGoalID := ""
	for {
		status, response, err := readGoalObservation(ctx, client, path)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return silentStatus(exitCode(lastStatus, nil))
			}
			return fail(6, fmt.Errorf("Goal observation failed: %w", err))
		}
		lastStatus = status
		if status < 200 || status >= 300 {
			prettyJSON(cmd.ErrOrStderr(), response)
			return silentStatus(exitCode(status, response))
		}
		var selected humanGoal
		if err := json.Unmarshal(response, &selected); err != nil || selected.GoalID == "" {
			return fail(5, errors.New("invalid Goal observation identity"))
		}
		if selectedGoalID == "" {
			selectedGoalID = selected.GoalID
			path = "/v1/goals/" + url.PathEscape(selectedGoalID)
		} else if selected.GoalID != selectedGoalID {
			return fail(5, errors.New("Goal observation identity changed"))
		}
		if err := feedback.write(cmd.OutOrStdout(), response, runtime.now(), false); err != nil {
			return fail(5, err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func readGoalObservation(ctx context.Context, client apiClient, path string) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return client.Do(ctx, http.MethodGet, path, "", nil)
}

func humanCommand(cmd *cobra.Command) string {
	command := "xgoal"
	for _, name := range []string{"project", "state-dir", "socket"} {
		value, _ := cmd.Flags().GetString(name)
		if value != "" {
			command += " --" + name + " " + shellArg(value)
		}
	}
	return command
}
