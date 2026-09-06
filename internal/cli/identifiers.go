package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/monshunter/xgoal/internal/store/sqlite"
	"github.com/spf13/cobra"
)

func validIdentifierKind(kind string) bool {
	return kind == "goal" || kind == "work" || kind == "gate" || kind == "invocation"
}

func newIDsCommand(runtime runtime) *cobra.Command {
	var kind string
	cmd := &cobra.Command{Use: "ids [prefix]", Short: "Find project IDs and current versions", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !validIdentifierKind(kind) {
			return errors.New("--kind must be goal, work, gate or invocation")
		}
		prefix := ""
		if len(args) > 0 {
			prefix = args[0]
		}
		return runtime.executeAPI(cmd, requestSpec{method: http.MethodGet, path: identifierPath(kind, prefix)})
	}}
	cmd.Flags().StringVar(&kind, "kind", "goal", "identifier kind: goal, work, gate or invocation")
	_ = cmd.RegisterFlagCompletionFunc("kind", cobra.FixedCompletions([]cobra.Completion{"goal", "work", "gate", "invocation"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func identifierPath(kind, prefix string) string {
	return "/v1/identifiers?" + url.Values{"kind": {kind}, "prefix": {prefix}}.Encode()
}

func (runtime runtime) completionIDs(cmd *cobra.Command, kind, prefix string) []sqlite.Identifier {
	ctx, cancel := context.WithTimeout(cmd.Context(), 500*time.Millisecond)
	defer cancel()
	client, err := runtime.newClient()
	if err != nil {
		return nil
	}
	if closer, ok := client.(interface{ Close() }); ok {
		defer closer.Close()
	}
	status, data, err := client.Do(ctx, http.MethodGet, identifierPath(kind, prefix), "", nil)
	if err != nil || status != http.StatusOK {
		return nil
	}
	var page sqlite.IdentifierPage
	if err := json.Unmarshal(data, &page); err != nil {
		return nil
	}
	return page.Items
}

func (runtime runtime) completeIDs(kind string) func(*cobra.Command, []string, string) ([]cobra.Completion, cobra.ShellCompDirective) {
	return func(cmd *cobra.Command, args []string, prefix string) ([]cobra.Completion, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		items := runtime.completionIDs(cmd, kind, prefix)
		result := make([]cobra.Completion, 0, len(items))
		for _, item := range items {
			result = append(result, fmt.Sprintf("%s\t%s, version %d", terminalText(item.ID), terminalText(item.State), item.Version))
		}
		return result, cobra.ShellCompDirectiveNoFileComp
	}
}

func configureIdentifierCompletion(root *cobra.Command, runtime runtime) {
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		kind := ""
		parent := cmd.Parent()
		if parent == root {
			switch cmd.Name() {
			case "status", "gates", "report", "pause", "resume", "cancel", "invocations", "export":
				kind = "goal"
			case "approve":
				kind = "gate"
			case "context":
				kind = "invocation"
			}
		} else if parent != nil {
			switch parent.Name() {
			case "goal":
				if cmd.Name() != "list" {
					kind = "goal"
				}
			case "gate":
				kind = "gate"
			case "work":
				kind = "work"
				if cmd.Name() == "list" {
					kind = "goal"
				}
			}
		}
		if kind != "" {
			cmd.ValidArgsFunction = runtime.completeIDs(kind)
			for _, flag := range []string{"version", "expected-version"} {
				if cmd.Flags().Lookup(flag) == nil {
					continue
				}
				_ = cmd.RegisterFlagCompletionFunc(flag, func(cmd *cobra.Command, args []string, prefix string) ([]cobra.Completion, cobra.ShellCompDirective) {
					if len(args) != 1 {
						return nil, cobra.ShellCompDirectiveNoFileComp
					}
					items := runtime.completionIDs(cmd, kind, args[0])
					if len(items) == 0 || len(items) > 1 && items[0].ID != args[0] {
						return nil, cobra.ShellCompDirectiveNoFileComp
					}
					return []cobra.Completion{strconv.FormatInt(items[0].Version, 10)}, cobra.ShellCompDirectiveNoFileComp
				})
			}
		}
		if cmd.Name() == "logs" && parent == root {
			for _, flag := range []string{"goal", "invocation"} {
				_ = cmd.RegisterFlagCompletionFunc(flag, func(cmd *cobra.Command, _ []string, prefix string) ([]cobra.Completion, cobra.ShellCompDirective) {
					return runtime.completeIDs(flag)(cmd, nil, prefix)
				})
			}
		}
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(root)
}
