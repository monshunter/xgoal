package review_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	baseadapter "github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/adapter/claude"
	"github.com/monshunter/xgoal/internal/adapter/codex"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/review"
	"github.com/monshunter/xgoal/internal/workpacket"
)

// TestM4RealCrossProviderReview is opt-in because it spends both installed CLI
// provider budgets. It proves two real implementation/review directions while
// deterministic file/tree checks remain the acceptance authority.
func TestM4RealCrossProviderReview(t *testing.T) {
	if os.Getenv("XGOAL_RUN_CROSS_REVIEW_SMOKE") != "1" {
		t.Skip("set XGOAL_RUN_CROSS_REVIEW_SMOKE=1 to run real cross-provider acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	for _, path := range []struct{ name, implementer, reviewer string }{{"codex_to_claude", "codex", "claude"}, {"claude_to_codex", "claude", "codex"}} {
		path := path
		t.Run(path.name, func(t *testing.T) { runRealCrossPath(t, ctx, path.name, path.implementer, path.reviewer) })
	}
}

func runRealCrossPath(t *testing.T, ctx context.Context, name, implementerName, reviewerName string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("# xgoal M4 real cross review\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runReviewGit(t, workspace, "init", "-b", "main")
	runReviewGit(t, workspace, "add", "README.md")
	runReviewGit(t, workspace, "-c", "user.name=XGoal Smoke", "-c", "user.email=xgoal-smoke@example.invalid", "commit", "--no-verify", "-m", "fixture")
	baseTree := strings.TrimSpace(runReviewGit(t, workspace, "rev-parse", "HEAD^{tree}"))
	runtimeRoot := filepath.Join(root, "runtime")
	packetStore, err := workpacket.NewStore(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	goalHash := strings.Repeat("a", 64)
	planHash := strings.Repeat("b", 64)
	attemptID := "attempt_" + name
	workID := "work_" + name
	filename := name + ".txt"
	content := "xgoal " + name + " accepted\n"
	packet := protocol.WorkPacket{ProtocolVersion: protocol.WorkPacketVersion, Project: protocol.PacketProject{Name: "m4-real-cross", BaseTree: baseTree, Workspace: workspace}, Goal: protocol.PacketGoal{ID: "goal_" + name, Revision: 1, Summary: "real cross-provider acceptance", ContractHash: goalHash}, WorkItem: protocol.PacketWorkItem{ID: workID, Title: "create exact acceptance file", Objective: "create one exact file", ReadScope: []string{"/**"}, WriteScope: []string{"/" + filename}, AcceptanceCriteria: []string{"file has exact requested content"}, ValidatorIDs: []string{"exact-file"}}, Role: domain.RoleImplementer, Constraints: protocol.PacketConstraints{ProjectNetwork: "deny", ProjectSecrets: "deny"}, RequiredOutputSchema: protocol.AgentResultVersion}
	packetArtifact, created, err := packetStore.Save(attemptID, packet)
	if err != nil || !created {
		t.Fatalf("save packet = %v, %v", created, err)
	}
	agentSchema, err := protocol.Schema(protocol.SchemaAgentResult)
	if err != nil {
		t.Fatal(err)
	}
	providerEnvironment := realProviderEnvironment()
	implementerEnvironment := environmentForProvider(providerEnvironment, implementerName)
	implementer := newRealBaseAdapter(t, implementerName, runtimeRoot, workspace, implementerEnvironment)
	invocation := baseadapter.Invocation{InvocationID: "invoke_" + name, AttemptID: attemptID, WorkItemID: workID, ProfileID: implementerName + "-implementer", GoalRevisionHash: goalHash, PlanRevisionHash: planHash, BaseTree: baseTree, PacketHash: packetArtifact.Hash, Role: domain.RoleImplementer, WorkDir: workspace, PacketPath: packetArtifact.Path, Prompt: fmt.Sprintf("Execute the immutable xgoal Work Packet at %s. Create only %s with exact UTF-8 content %q. Do not commit, do not use network, and return the required structured AgentResult.", packetArtifact.Path, filename, content), OutputSchema: agentSchema, Environment: implementerEnvironment, Timeout: 5 * time.Minute, MaxOutputBytes: 16 << 20, SessionPolicy: baseadapter.SessionFresh}
	if implementerName == "codex" {
		invocation.SandboxPolicy = "workspace-write"
	} else {
		invocation.PermissionMode = "dontAsk"
		invocation.ToolPolicy = []string{"Read", "Glob", "Grep", "Edit", "Write"}
	}
	handle, err := implementer.Start(ctx, invocation, nil)
	if err != nil {
		t.Fatalf("start %s implementation: %v", implementerName, err)
	}
	result, err := implementer.Wait(ctx, handle)
	if err != nil {
		logRealAdapterArtifacts(t, runtimeRoot)
		t.Fatalf("wait %s implementation: %v", implementerName, err)
	}
	if result.Status != protocol.ResultCompleted {
		t.Fatalf("implementation result = %+v", result)
	}
	sessionID := adapterSessionID(t, implementer, handle)
	actual, err := os.ReadFile(filepath.Join(workspace, filename))
	if err != nil || string(actual) != content {
		t.Fatalf("deterministic exact-file validation = %q, %v", actual, err)
	}
	runReviewGit(t, workspace, "add", filename)
	candidateTree := strings.TrimSpace(runReviewGit(t, workspace, "write-tree"))
	digest := sha256.Sum256(actual)
	reviewStore, err := review.NewStore(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	reviewPacket := protocol.ReviewPacket{ProtocolVersion: protocol.ReviewPacketVersion, ID: "review_" + name, GoalRevisionHash: goalHash, PlanRevisionHash: planHash, WorkItemID: workID, ImplementationAttemptID: attemptID, ImplementationProfileID: invocation.ProfileID, ImplementationSessionID: sessionID, BaseTree: baseTree, CandidateTree: candidateTree, ConfigHash: strings.Repeat("c", 64), Workspace: workspace, PatchBundlePath: filepath.Join(runtimeRoot, "patches", attemptID), PatchBundleHash: hex.EncodeToString(digest[:]), ValidatorReceipts: []protocol.ReviewReceiptRef{{ID: "exact_file_" + name, Path: filepath.Join(runtimeRoot, "validator", "receipts", "exact_file_"+name+".json"), Hash: strings.Repeat("d", 64)}}, RequiredChecks: []string{"correctness", "scope", "test_gap"}, ReviewerProfileID: reviewerName + "-reviewer"}
	reviewArtifact, err := reviewStore.SavePacket(reviewPacket)
	if err != nil {
		t.Fatal(err)
	}
	reviewSchema, err := protocol.Schema(protocol.SchemaReviewResult)
	if err != nil {
		t.Fatal(err)
	}
	reviewerEnvironment := environmentForProvider(providerEnvironment, reviewerName)
	reviewer := newRealReviewAdapter(t, reviewerName, runtimeRoot, workspace, reviewerEnvironment)
	reviewInvocation := review.Invocation{InvocationID: "review_invoke_" + name, ReviewID: reviewPacket.ID, ReviewerProfileID: reviewPacket.ReviewerProfileID, ImplementationProfileID: reviewPacket.ImplementationProfileID, ImplementationSessionID: sessionID, GoalRevisionHash: goalHash, PlanRevisionHash: planHash, BaseTree: baseTree, CandidateTree: candidateTree, PacketHash: reviewArtifact.Hash, WorkDir: workspace, PacketPath: reviewArtifact.Path, Prompt: fmt.Sprintf("Independently review the immutable Review Packet at %s and the actual workspace. Make exactly two Read calls: first that packet, then %s. The deterministic exact-file validator observed exact content %q and candidate tree %s. Immediately decide correctness, scope, regression, test gap, and security; do not perform extended analysis. Return approved with no findings if this single-file bounded change satisfies the packet, otherwise report concrete findings. Return only the required structured ReviewResult.", reviewArtifact.Path, filepath.Join(workspace, filename), content, candidateTree), OutputSchema: reviewSchema, Environment: reviewerEnvironment, PermissionMode: "dontAsk", Tools: []string{"Read"}, Timeout: 5 * time.Minute, MaxOutputBytes: 16 << 20}
	reviewExecution, err := reviewer.Review(ctx, reviewInvocation, nil)
	if err != nil {
		logRealAdapterArtifacts(t, runtimeRoot)
		t.Fatalf("wait %s review: %v", reviewerName, err)
	}
	if reviewExecution.SessionID == "" || reviewExecution.SessionID == sessionID || reviewExecution.Result.ReviewStatus != protocol.ReviewApproved || len(reviewExecution.Result.Findings) != 0 {
		t.Fatalf("cross review execution = %+v, implementation session %q", reviewExecution, sessionID)
	}
	saved, err := reviewStore.SaveResult(reviewPacket.ID, reviewExecution.Result)
	if err != nil {
		t.Fatal(err)
	}
	if _, hash, err := review.ReadResult(saved.Path); err != nil || hash != saved.Hash {
		t.Fatalf("review result readback = %q, %v", hash, err)
	}
	t.Logf("%s passed: implementation session %s, review session %s, tree %s", name, sessionID, reviewExecution.SessionID, candidateTree)
}

func newRealBaseAdapter(t *testing.T, name, runtimeRoot, workspace string, environment map[string]string) baseadapter.Adapter {
	t.Helper()
	binary, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	if name == "codex" {
		runtime, err := codex.New(codex.Config{Binary: binary, RuntimeRoot: runtimeRoot, ProjectRoot: workspace, Environment: environment})
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}
	runtime, err := claude.New(claude.Config{Binary: binary, RuntimeRoot: runtimeRoot, ProjectRoot: workspace, Environment: environment})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}
func newRealReviewAdapter(t *testing.T, name, runtimeRoot, workspace string, environment map[string]string) review.Adapter {
	t.Helper()
	base := newRealBaseAdapter(t, name, runtimeRoot, workspace, environment)
	reviewer, ok := base.(review.Adapter)
	if !ok {
		t.Fatalf("%s adapter lacks review", name)
	}
	return reviewer
}
func adapterSessionID(t *testing.T, runtime baseadapter.Adapter, handle baseadapter.Handle) string {
	t.Helper()
	provider, ok := runtime.(interface {
		SessionID(baseadapter.Handle) (string, error)
	})
	if !ok {
		t.Fatal("adapter lacks session observation")
	}
	id, err := provider.SessionID(handle)
	if err != nil || id == "" {
		t.Fatalf("session = %q, %v", id, err)
	}
	return id
}
func realProviderEnvironment() map[string]string {
	result := map[string]string{}
	for _, name := range []string{
		"PATH", "HOME", "TMPDIR", "CODEX_HOME", "CLAUDE_CONFIG_DIR",
		"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL",
		"CLAUDE_CODE_EFFORT_LEVEL", "CLAUDE_CODE_SUBAGENT_MODEL",
	} {
		if value, ok := os.LookupEnv(name); ok {
			result[name] = value
		}
	}
	return result
}

func environmentForProvider(all map[string]string, provider string) map[string]string {
	result := make(map[string]string)
	for name, value := range all {
		if provider == "claude" || (!strings.HasPrefix(name, "ANTHROPIC_") && !strings.HasPrefix(name, "CLAUDE_")) {
			result[name] = value
		}
	}
	return result
}
func runReviewGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func logRealAdapterArtifacts(t *testing.T, root string) {
	t.Helper()
	var total int
	_ = filepath.WalkDir(filepath.Join(root, "adapters"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		if filepath.Base(path) != "stderr.log" && !strings.Contains(path, string(filepath.Separator)+"events"+string(filepath.Separator)) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err == nil && !strings.Contains(string(content), `"subtype":"thinking_tokens"`) && total+len(content) <= 64<<10 {
			relative, _ := filepath.Rel(root, path)
			t.Logf("sanitized adapter artifact %s: %s", filepath.ToSlash(relative), content)
			total += len(content)
		}
		return nil
	})
}
