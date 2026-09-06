package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	callindex "github.com/monshunter/xgoal/internal/invocation"
	"github.com/spf13/cobra"
)

func validInvocationRole(role string) error {
	if role == "" {
		return nil
	}
	_, err := callindex.Directory("codex-cli", role, "id")
	return err
}
func newInvocationsCommand(runtime runtime) *cobra.Command {
	var role string
	cmd := &cobra.Command{Use: "invocations <goal-id>", Short: "List Provider invocations for a Goal", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validInvocationRole(role); err != nil {
			return err
		}
		path := "/v1/goals/" + url.PathEscape(args[0]) + "/invocations"
		if role != "" {
			path += "?role=" + url.QueryEscape(role)
		}
		return runtime.executeAPI(cmd, requestSpec{method: http.MethodGet, path: path})
	}}
	cmd.Flags().StringVar(&role, "role", "", "filter planner, implementer, reviewer or acceptance")
	return cmd
}
func newContextCommand(runtime runtime) *cobra.Command {
	return getCommand("context <invocation-id>", "Inspect invocation inputs, effective settings and public results", func(id string) string { return "/v1/invocations/" + url.PathEscape(id) + "/context" }, runtime)
}
func newInvocationLogsCommand(runtime runtime) *cobra.Command {
	var invocationID, goalID, role, stream string
	var after int64
	var limit int
	var follow bool
	cmd := &cobra.Command{Use: "logs [attempt-id]", Short: "Show Attempt logs or durable Provider invocation output", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if err := validInvocationRole(role); err != nil {
			return err
		}
		if stream != "stdout" && stream != "stderr" {
			return errors.New("--stream must be stdout or stderr")
		}
		if after < 0 || limit < 1 || limit > 256 {
			return errors.New("--after must be nonnegative and --limit must be 1..256")
		}
		if len(args) == 1 {
			if invocationID != "" || goalID != "" || role != "" || follow || cmd.Flags().Changed("after") || cmd.Flags().Changed("stream") || cmd.Flags().Changed("limit") {
				return errors.New("Attempt logs do not accept invocation selection or stream flags")
			}
			return runtime.executeAPI(cmd, requestSpec{method: http.MethodGet, path: "/v1/attempts/" + url.PathEscape(args[0]) + "/logs"})
		}
		if (invocationID == "") == (goalID == "") {
			return errors.New("choose exactly one of --invocation or --goal")
		}
		if role != "" && goalID == "" {
			return errors.New("--role requires --goal")
		}
		if goalID != "" {
			client, err := runtime.newClient()
			if err != nil {
				return err
			}
			if closer, ok := client.(interface{ Close() }); ok {
				defer closer.Close()
			}
			path := "/v1/goals/" + url.PathEscape(goalID) + "/invocations"
			if role != "" {
				path += "?role=" + url.QueryEscape(role)
			}
			status, body, err := client.Do(cmd.Context(), http.MethodGet, path, "", nil)
			if err != nil {
				return fail(6, err)
			}
			if status >= 400 {
				prettyJSON(cmd.ErrOrStderr(), body)
				return silentStatus(exitCode(status, body))
			}
			var view struct {
				Invocations []callindex.Summary `json:"invocations"`
			}
			if err := json.Unmarshal(body, &view); err != nil {
				return fail(5, err)
			}
			if len(view.Invocations) == 0 {
				return errors.New("no matching invocation; use invocations to inspect available calls")
			}
			invocationID = view.Invocations[len(view.Invocations)-1].ID
			fmt.Fprintf(cmd.ErrOrStderr(), "Following invocation %s (selection fixed for this command)\n", invocationID)
			// Retain the same fenced project client for selection and reading.
			runtime.newClient = func() (apiClient, error) { return client, nil }
		}
		query := url.Values{"after": {strconv.FormatInt(after, 10)}, "limit": {strconv.Itoa(limit)}, "stream": {stream}}
		if follow {
			query.Set("watch", "1")
		}
		return runtime.executeAPI(cmd, requestSpec{method: http.MethodGet, path: "/v1/invocations/" + url.PathEscape(invocationID) + "/logs?" + query.Encode(), watch: follow})
	}}
	cmd.Flags().StringVar(&invocationID, "invocation", "", "read this invocation")
	cmd.Flags().StringVar(&goalID, "goal", "", "select the latest matching invocation in a Goal")
	cmd.Flags().StringVar(&role, "role", "", "filter Goal invocation selection by role")
	cmd.Flags().StringVar(&stream, "stream", "stdout", "stdout or stderr")
	cmd.Flags().Int64Var(&after, "after", 0, "resume after this invocation stream sequence")
	cmd.Flags().IntVar(&limit, "limit", 100, "maximum events per page (1..256)")
	cmd.Flags().BoolVar(&follow, "follow", false, "follow this invocation until its persisted output is drained")
	return cmd
}
