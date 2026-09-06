package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/spf13/cobra"
)

func gateResumeRequest(id string, gateVersion, ownerVersion int64) requestSpec {
	return requestSpec{method: http.MethodPost, path: "/v1/gates/" + url.PathEscape(id) + "/resume", body: map[string]any{"expected_gate_version": gateVersion, "expected_owner_version": ownerVersion}}
}

func newGateResumeCommand(runtime runtime) *cobra.Command {
	var version, ownerVersion int64
	cmd := &cobra.Command{Use: "resume <gate-id>", Short: "Resume the owner of an already approved Gate without recording another decision", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if version <= 0 || ownerVersion <= 0 {
			return errors.New("positive --version and --owner-version are required")
		}
		return runtime.executeAPI(cmd, gateResumeRequest(args[0], version, ownerVersion))
	}}
	cmd.Flags().Int64Var(&version, "version", 0, "expected approved Gate version")
	cmd.Flags().Int64Var(&ownerVersion, "owner-version", 0, "expected Goal version for planning/final acceptance, or Work Item version")
	return cmd
}

func (runtime runtime) decideAndResume(cmd *cobra.Command, request requestSpec, ownerVersion int64) error {
	client, err := runtime.newClient()
	if err != nil {
		return fail(2, err)
	}
	if closer, ok := client.(interface{ Close() }); ok {
		defer closer.Close()
	}
	post := func(request requestSpec) ([]byte, error) {
		key, err := runtime.newID("request")
		if err != nil {
			return nil, fail(5, err)
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
		defer cancel()
		status, response, err := client.Do(ctx, request.method, request.path, key, request.body)
		if err != nil {
			return nil, fail(6, fmt.Errorf("API request failed: %w", err))
		}
		writer := cmd.OutOrStdout()
		if status >= 400 {
			writer = cmd.ErrOrStderr()
		}
		prettyJSON(writer, response)
		if status < 200 || status >= 300 {
			return nil, silentStatus(exitCode(status, response))
		}
		return response, nil
	}
	response, err := post(request)
	if err != nil {
		return err
	}
	var gate domain.Gate
	prefix, decodeErr := url.PathUnescape(strings.TrimSuffix(strings.TrimPrefix(request.path, "/v1/gates/"), "/decisions"))
	expected, _ := request.body.(map[string]any)["expected_version"].(int64)
	if json.Unmarshal(response, &gate) != nil || decodeErr != nil || prefix == "" || !strings.HasPrefix(gate.ID, prefix) || gate.Version != expected+1 || gate.State != domain.GateApproved || gate.Decision != domain.GateAllow {
		return fail(5, errors.New("decision response was received but its Gate identity is invalid; inspect the decision before continuing"))
	}
	if _, err := post(gateResumeRequest(gate.ID, gate.Version, ownerVersion)); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Decision retained for %s. Inspect the owner and retry only: %s gate resume %s --version %d --owner-version %d\n", terminalText(gate.ID), humanCommand(cmd), shellArg(gate.ID), gate.Version, ownerVersion)
		return err
	}
	return nil
}
