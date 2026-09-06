package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/spf13/cobra"
)

func newGoalListCommand(runtime runtime) *cobra.Command {
	var state, format string
	q := sqlite.GoalListQuery{}
	cmd := &cobra.Command{Use: "list", Short: "List project Goals and current states", Args: cobra.NoArgs,
		Long:              "List project Goals in ID order. Follow next_cursor with --after to read all pages. Requires the project daemon; does not start it.",
		ValidArgsFunction: cobra.NoFileCompletions,
		RunE: func(cmd *cobra.Command, _ []string) error {
			q.State = domain.GoalState(state)
			if err := q.Validate(); err != nil {
				return err
			}
			if err := checkFormat(format); err != nil {
				return err
			}
			values := url.Values{"limit": {strconv.Itoa(q.Limit)}}
			if state != "" {
				values.Set("state", state)
			}
			if q.After != "" {
				values.Set("after", q.After)
			}
			request := requestSpec{method: http.MethodGet, path: "/v1/goals?" + values.Encode()}
			if format == "human" {
				request.render = func(response []byte, command string) (string, error) { return renderGoalList(response, command, q) }
			}
			return runtime.executeAPI(cmd, request)
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "filter by exact Goal state (default: all states)")
	cmd.Flags().IntVar(&q.Limit, "limit", 100, "maximum Goals per page (1..100)")
	cmd.Flags().StringVar(&q.After, "after", "", "opaque next_cursor returned by the previous page")
	cmd.Flags().StringVar(&format, "format", "json", "list presentation: json or human")
	_ = cmd.RegisterFlagCompletionFunc("state", cobra.FixedCompletions([]cobra.Completion{string(domain.GoalDraft), string(domain.GoalReady), string(domain.GoalRunning), string(domain.GoalWaiting), string(domain.GoalVerifying), string(domain.GoalCompleted), string(domain.GoalCancelled)}, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]cobra.Completion{"json", "human"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func renderGoalList(response []byte, command string, q sqlite.GoalListQuery) (string, error) {
	var page sqlite.GoalPage
	if err := json.Unmarshal(response, &page); err != nil || page.Items == nil {
		return "", errors.New("invalid Goal list response")
	}
	if len(page.Items) == 0 {
		return "No matching Goals.\n", nil
	}
	var out strings.Builder
	table := tabwriter.NewWriter(&out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "GOAL_ID\tSTATE\tVERSION\tPLANNING\tUPDATED_AT\tSUMMARY")
	for _, g := range page.Items {
		if g.GoalID == "" || !g.State.Valid() || g.Version <= 0 {
			return "", errors.New("invalid Goal list item")
		}
		planning := g.PlanningState
		if planning == "" {
			planning = "-"
		}
		fmt.Fprintf(table, "%s\t%s\t%d\t%s\t%s\t%s\n", terminalText(g.GoalID), terminalText(string(g.State)), g.Version, terminalText(planning), g.UpdatedAt.UTC().Format(time.RFC3339), terminalText(g.Summary))
	}
	if err := table.Flush(); err != nil {
		return "", err
	}
	if page.NextCursor != "" {
		fmt.Fprintf(&out, "Next: %s goal list --limit %d", command, q.Limit)
		if q.State != "" {
			fmt.Fprintf(&out, " --state %s", shellArg(string(q.State)))
		}
		fmt.Fprintf(&out, " --after %s --format human\n", shellArg(page.NextCursor))
	}
	return out.String(), nil
}
