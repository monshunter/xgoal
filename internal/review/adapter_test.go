package review_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/adapter/claude"
	"github.com/monshunter/xgoal/internal/adapter/codex"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/review"
)

func TestClaudeAndCodexReviewAdaptersShareStrictContract(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"claude", "codex"} {
		provider := provider
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			workspace := filepath.Join(root, "workspace")
			if err := os.Mkdir(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			workspace, _ = filepath.EvalSymlinks(workspace)
			runtimeRoot := filepath.Join(root, "runtime")
			store, err := review.NewStore(runtimeRoot)
			if err != nil {
				t.Fatal(err)
			}
			packet := protocol.ReviewPacket{ProtocolVersion: protocol.ReviewPacketVersion, ID: "review_" + provider, GoalRevisionHash: strings.Repeat("a", 64), PlanRevisionHash: strings.Repeat("b", 64), WorkItemID: "work_1", ImplementationAttemptID: "attempt_1", ImplementationProfileID: "implementation-profile", ImplementationSessionID: "implementation-session", BaseTree: strings.Repeat("c", 40), CandidateTree: strings.Repeat("d", 40), ConfigHash: strings.Repeat("e", 64), Workspace: workspace, PatchBundlePath: filepath.Join(root, "patch"), PatchBundleHash: strings.Repeat("f", 64), ValidatorReceipts: []protocol.ReviewReceiptRef{{ID: "receipt_1", Path: filepath.Join(root, "receipt.json"), Hash: strings.Repeat("1", 64)}}, RequiredChecks: []string{"correctness"}, ReviewerProfileID: provider + "-reviewer"}
			artifact, err := store.SavePacket(packet)
			if err != nil {
				t.Fatal(err)
			}
			schema, err := protocol.Schema(protocol.SchemaReviewResult)
			if err != nil {
				t.Fatal(err)
			}
			argumentsPath := filepath.Join(root, "arguments")
			binary := filepath.Join(root, provider)
			resultJSON := `{"protocol_version":"xgoal.review-result/v1alpha1","review_status":"changes_requested","findings":[{"id":"finding_1","severity":"high","category":"correctness","path":"internal/state.go","line":42,"claim":"state can drift","basis":"transition is unchecked","recommended_fix":"validate transition"}],"suggested_validators":["state-race"]}`
			var script string
			if provider == "claude" {
				script = `#!/bin/sh
set -eu
printf '%s\n' "$*" > "$ARGS_PATH"
cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"claude-review-session"}'
printf '%s\n' '{"type":"result","is_error":false,"session_id":"claude-review-session","usage":{"input_tokens":5,"output_tokens":2},"total_cost_usd":0.000007,"structured_output":` + resultJSON + `}'
`
			} else {
				script = `#!/bin/sh
set -eu
printf '%s\n' "$*" > "$ARGS_PATH"
cat >/dev/null
printf '%s\n' '{"type":"thread.started","thread_id":"codex-review-session"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"{\"protocol_version\":\"xgoal.review-result/v1alpha1\",\"review_status\":\"changes_requested\",\"findings\":[{\"id\":\"finding_1\",\"severity\":\"high\",\"category\":\"correctness\",\"path\":\"internal/state.go\",\"line\":42,\"claim\":\"state can drift\",\"basis\":\"transition is unchecked\",\"recommended_fix\":\"validate transition\"}],\"suggested_validators\":[\"state-race\"]}"}}'
`
			}
			if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			invocation := review.Invocation{InvocationID: "invoke_" + provider, ReviewID: packet.ID, ReviewerProfileID: packet.ReviewerProfileID, ImplementationProfileID: packet.ImplementationProfileID, ImplementationSessionID: packet.ImplementationSessionID, GoalRevisionHash: packet.GoalRevisionHash, PlanRevisionHash: packet.PlanRevisionHash, BaseTree: packet.BaseTree, CandidateTree: packet.CandidateTree, PacketHash: artifact.Hash, WorkDir: workspace, PacketPath: artifact.Path, Prompt: "Review the immutable packet and return only the required result.", OutputSchema: schema, Environment: map[string]string{"ARGS_PATH": argumentsPath}, PermissionMode: "dontAsk", Tools: []string{"Read", "Glob", "Grep"}, Timeout: 30 * time.Second, MaxOutputBytes: 4 << 20}
			var reviewer review.Adapter
			if provider == "claude" {
				reviewer, err = claude.New(claude.Config{Binary: binary, RuntimeRoot: runtimeRoot, ProjectRoot: workspace, Environment: invocation.Environment})
			} else {
				reviewer, err = codex.New(codex.Config{Binary: binary, RuntimeRoot: runtimeRoot, ProjectRoot: workspace, Environment: invocation.Environment})
			}
			if err != nil {
				t.Fatal(err)
			}
			execution, err := reviewer.Review(context.Background(), invocation, nil)
			if err != nil {
				t.Fatal(err)
			}
			if execution.Result.ReviewStatus != protocol.ReviewChangesRequested || len(execution.Result.Findings) != 1 || execution.Result.Authority() != domain.AuthorityInference || execution.SessionID == packet.ImplementationSessionID {
				t.Fatalf("review execution = %+v", execution)
			}
			arguments, err := os.ReadFile(argumentsPath)
			if err != nil {
				t.Fatal(err)
			}
			if provider == "claude" && !strings.Contains(string(arguments), "--permission-mode dontAsk --tools Read,Glob,Grep --allowedTools Read,Glob,Grep") {
				t.Fatalf("Claude review arguments = %s", arguments)
			}
			if provider == "codex" && !strings.Contains(string(arguments), "--sandbox read-only") {
				t.Fatalf("Codex review arguments = %s", arguments)
			}
		})
	}
}
