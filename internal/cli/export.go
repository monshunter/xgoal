package cli

import (
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

func newExportCommand(runtime runtime) *cobra.Command {
	var output string
	cmd := &cobra.Command{Use: "export <goal-id>", Short: "Export a consistent project audit snapshot and a focused Goal view", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if output == "" {
			return errors.New("--output is required and must name a new directory outside the project")
		}
		absolute, err := filepath.Abs(output)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		cmd.SetContext(ctx)
		return runtime.executeAPI(cmd, requestSpec{method: http.MethodPost, path: "/v1/goals/" + url.PathEscape(args[0]) + "/exports", body: map[string]any{"output": absolute}, timeout: 5 * time.Minute})
	}}
	cmd.Flags().StringVar(&output, "output", "", "new local export directory (whole project database and sealed artifacts)")
	return cmd
}
