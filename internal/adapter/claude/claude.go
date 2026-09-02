package claude

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/redact"
	"github.com/monshunter/xgoal/internal/supervisor"
)

const (
	adapterID      = "claude-cli"
	maxProbeOutput = 4 << 20
	maxPacketBytes = 8 << 20
	maxResultBytes = 1 << 20
	gracePeriod    = 2 * time.Second
	activePrompt   = "Return a completed xgoal AgentResult JSON object with summary 'claude active contract probe'. Do not inspect or modify files."
)

type Config struct {
	Binary      string
	RuntimeRoot string
	ProjectRoot string
	Environment map[string]string
	Clock       clock.Clock
}

type Adapter struct {
	binary, root, projectRoot string
	environment               map[string]string
	clock                     clock.Clock
	mu                        sync.Mutex
	executions                map[string]*execution
}

type execution struct {
	done      chan struct{}
	cancel    context.CancelFunc
	running   *supervisor.Running
	metadata  invocationArtifact
	mu        sync.RWMutex
	result    protocol.AgentResult
	err       error
	sessionID string
	canceled  bool
	once      sync.Once
}

type boundedStderr struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	limiter *outputLimiter
	cancel  func()
	err     error
}

func New(configuration Config) (*Adapter, error) {
	if configuration.Binary == "" || !cleanAbsolute(configuration.RuntimeRoot) || !cleanAbsolute(configuration.ProjectRoot) {
		return nil, errors.New("Claude adapter requires binary, runtime root, and project root")
	}
	binary, err := exec.LookPath(configuration.Binary)
	if err != nil {
		return nil, fmt.Errorf("%w: locate Claude CLI: %v", adapter.ErrUnavailable, err)
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("%w: Claude binary is not executable", adapter.ErrUnavailable)
	}
	if err := ensurePrivateDirectory(configuration.RuntimeRoot); err != nil {
		return nil, err
	}
	runtimeRoot, err := filepath.EvalSymlinks(configuration.RuntimeRoot)
	if err != nil {
		return nil, err
	}
	projectRoot, err := canonicalDirectory(configuration.ProjectRoot)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(runtimeRoot, "adapters", "claude")
	for _, dir := range []string{root, filepath.Join(root, "invocations"), filepath.Join(root, "plans"), filepath.Join(root, "reviews"), filepath.Join(root, "sessions"), filepath.Join(root, "probes")} {
		if err := ensurePrivateDirectory(dir); err != nil {
			return nil, err
		}
	}
	environment, err := buildEnvironment(configuration.Environment)
	if err != nil {
		return nil, err
	}
	if configuration.Clock == nil {
		configuration.Clock = clock.Real{}
	}
	return &Adapter{binary: binary, root: root, projectRoot: projectRoot, environment: environment, clock: configuration.Clock, executions: make(map[string]*execution)}, nil
}

func (runtime *Adapter) ID() string { return adapterID }

func (runtime *Adapter) Start(ctx context.Context, invocation adapter.Invocation, sink adapter.EventSink) (adapter.Handle, error) {
	if invocation.SessionPolicy != adapter.SessionFresh {
		return adapter.Handle{}, errors.New("new Claude invocation requires fresh session policy")
	}
	return runtime.start(ctx, invocation, "", sink)
}

func (runtime *Adapter) Resume(ctx context.Context, invocation adapter.Invocation, sessionID string, sink adapter.EventSink) (adapter.Handle, error) {
	if invocation.SessionPolicy != adapter.SessionResumeCompatible || !validSessionID(sessionID) {
		return adapter.Handle{}, fmt.Errorf("%w: Claude resume policy or session id is invalid", adapter.ErrSessionMismatch)
	}
	return runtime.start(ctx, invocation, sessionID, sink)
}

func (runtime *Adapter) start(ctx context.Context, invocation adapter.Invocation, resumeID string, sink adapter.EventSink) (adapter.Handle, error) {
	validated, err := runtime.validateInvocation(invocation)
	if err != nil {
		return adapter.Handle{}, err
	}
	if resumeID != "" {
		stored, err := readSession(runtime.root, resumeID)
		if err != nil || !sameBinding(stored, validated.metadata) {
			return adapter.Handle{}, fmt.Errorf("%w: persisted Claude session does not match invocation", adapter.ErrSessionMismatch)
		}
	}
	runtime.mu.Lock()
	if _, exists := runtime.executions[invocation.InvocationID]; exists {
		runtime.mu.Unlock()
		return adapter.Handle{}, fmt.Errorf("Claude invocation %q already exists", invocation.InvocationID)
	}
	directory := filepath.Join(runtime.root, "invocations", invocation.InvocationID)
	if err := os.Mkdir(directory, 0o700); err != nil {
		runtime.mu.Unlock()
		return adapter.Handle{}, err
	}
	eventsDir := filepath.Join(directory, "events")
	if err := os.Mkdir(eventsDir, 0o700); err != nil {
		runtime.mu.Unlock()
		return adapter.Handle{}, err
	}
	metadataContent, err := canonical.Marshal(validated.metadata)
	if err != nil {
		runtime.mu.Unlock()
		return adapter.Handle{}, err
	}
	if err := writeImmutable(filepath.Join(directory, "invocation.json"), metadataContent, 0o600); err != nil {
		runtime.mu.Unlock()
		return adapter.Handle{}, err
	}
	runContext, cancel := context.WithTimeout(ctx, invocation.Timeout)
	limiter := &outputLimiter{remaining: invocation.MaxOutputBytes}
	stream := newStream(eventsDir, filepath.ToSlash(filepath.Join("invocations", invocation.InvocationID)), sink, runtime.clock.Now, limiter, cancel)
	stderr := &boundedStderr{limiter: limiter, cancel: cancel}
	running, err := supervisor.Start(supervisor.Command{Argv: runtime.arguments(invocation, validated.schema, resumeID), Dir: validated.workDir, Env: validated.environment, Stdin: strings.NewReader(invocation.Prompt), Stdout: stream, Stderr: stderr, GracePeriod: gracePeriod})
	if err != nil {
		cancel()
		runtime.mu.Unlock()
		return adapter.Handle{}, err
	}
	executed := &execution{done: make(chan struct{}), cancel: cancel, running: running, metadata: validated.metadata}
	runtime.executions[invocation.InvocationID] = executed
	runtime.mu.Unlock()
	go runtime.observe(runContext, executed, stream, stderr, invocation.MaxOutputBytes)
	return adapter.Handle{ID: invocation.InvocationID, PID: running.PID()}, nil
}

func (runtime *Adapter) arguments(invocation adapter.Invocation, schema []byte, resumeID string) []string {
	tools := strings.Join(invocation.ToolPolicy, ",")
	arguments := []string{runtime.binary, "-p", "--input-format", "text", "--output-format", "stream-json", "--verbose", "--json-schema", string(schema), "--permission-mode", invocation.PermissionMode, "--tools", tools, "--allowedTools", tools}
	if resumeID != "" {
		arguments = append(arguments, "--resume", resumeID)
	}
	return arguments
}

func (runtime *Adapter) Cancel(_ context.Context, handle adapter.Handle) error {
	executed, err := runtime.execution(handle)
	if err != nil {
		return err
	}
	executed.mu.Lock()
	executed.canceled = true
	executed.mu.Unlock()
	executed.cancel()
	return executed.running.Terminate()
}

func (runtime *Adapter) Wait(ctx context.Context, handle adapter.Handle) (protocol.AgentResult, error) {
	executed, err := runtime.execution(handle)
	if err != nil {
		return protocol.AgentResult{}, err
	}
	select {
	case <-ctx.Done():
		return protocol.AgentResult{}, ctx.Err()
	case <-executed.done:
		executed.mu.RLock()
		defer executed.mu.RUnlock()
		return executed.result, executed.err
	}
}

func (runtime *Adapter) SessionID(handle adapter.Handle) (string, error) {
	executed, err := runtime.execution(handle)
	if err != nil {
		return "", err
	}
	<-executed.done
	executed.mu.RLock()
	defer executed.mu.RUnlock()
	return executed.sessionID, nil
}

func (runtime *Adapter) execution(handle adapter.Handle) (*execution, error) {
	if !validComponent(handle.ID) {
		return nil, errors.New("invalid Claude invocation handle")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	executed, ok := runtime.executions[handle.ID]
	if !ok {
		return nil, fmt.Errorf("unknown Claude invocation %q", handle.ID)
	}
	return executed, nil
}

func (runtime *Adapter) observe(ctx context.Context, executed *execution, stream *stream, stderr *boundedStderr, outputLimit int64) {
	done := make(chan struct{})
	var process supervisor.Execution
	var processErr error
	go func() { process, processErr = executed.running.Wait(context.Background()); close(done) }()
	select {
	case <-ctx.Done():
		_ = executed.running.Terminate()
		<-done
	case <-done:
	}
	contextErr := ctx.Err()
	executed.cancel()
	stderrErr := stderr.persist(filepath.Join(runtime.root, "invocations", executed.metadata.InvocationID, "stderr.log"))
	result, sessionID, resultErr := stream.finalizeAgent(min64(outputLimit, maxResultBytes))
	if resultErr == nil && contextErr == nil && stderrErr == nil && processErr == nil && process.ExitCode == 0 {
		binding, err := newBinding(sessionID, executed.metadata)
		if err == nil {
			err = writeSession(runtime.root, binding)
		}
		if err != nil {
			resultErr = err
		}
	}
	var finalErr error
	switch {
	case stream.failure() != nil:
		finalErr = stream.failure()
	case stderrErr != nil:
		finalErr = stderrErr
	case contextErr != nil:
		finalErr = contextErr
	case resultErr != nil:
		finalErr = resultErr
	case processErr != nil:
		finalErr = fmt.Errorf("Claude CLI exited %d: %w", process.ExitCode, processErr)
	case process.ExitCode != 0:
		finalErr = fmt.Errorf("Claude CLI exited %d", process.ExitCode)
	}
	executed.once.Do(func() {
		executed.mu.Lock()
		if executed.canceled && finalErr == nil {
			finalErr = context.Canceled
		}
		executed.result = result
		executed.err = finalErr
		executed.sessionID = sessionID
		executed.mu.Unlock()
		close(executed.done)
	})
}

type validatedInvocation struct {
	metadata            invocationArtifact
	workDir, packetPath string
	schema              []byte
	environment         []string
}

func (runtime *Adapter) validateInvocation(inv adapter.Invocation) (validatedInvocation, error) {
	if !validComponent(inv.InvocationID) || !validComponent(inv.AttemptID) || !validComponent(inv.WorkItemID) || !validComponent(inv.ProfileID) || !validHash(inv.GoalRevisionHash, 64) || !validHash(inv.PlanRevisionHash, 64) || (!validHash(inv.BaseTree, 40) && !validHash(inv.BaseTree, 64)) || !validHash(inv.PacketHash, 64) || !inv.Role.Valid() || strings.TrimSpace(inv.Prompt) == "" || inv.Timeout <= 0 || inv.MaxOutputBytes <= 0 || inv.MaxOutputBytes > 128<<20 || inv.PermissionMode != "dontAsk" || !roleTools(inv.Role, inv.ToolPolicy) {
		return validatedInvocation{}, errors.New("invalid Claude invocation identity, permission, tools, timeout, or output limit")
	}
	workDir, err := canonicalDirectory(inv.WorkDir)
	if err != nil || workDir != inv.WorkDir {
		return validatedInvocation{}, errors.New("Claude workdir must be a canonical directory")
	}
	packetPath, packet, err := readPacket(inv.PacketPath)
	if err != nil {
		return validatedInvocation{}, err
	}
	hash, err := packet.Hash()
	if err != nil || hash != inv.PacketHash || packet.Project.Workspace != workDir || packet.WorkItem.ID != inv.WorkItemID || packet.Role != inv.Role {
		return validatedInvocation{}, errors.New("Claude work packet does not match invocation")
	}
	schema, schemaHash, err := validateSchema(inv.OutputSchema, protocol.SchemaAgentResult, protocol.AgentResultVersion)
	if err != nil {
		return validatedInvocation{}, err
	}
	environment, err := buildEnvironment(inv.Environment)
	if err != nil {
		return validatedInvocation{}, err
	}
	return validatedInvocation{metadata: metadata(inv, workDir, packetPath, schemaHash, runtime.clock.Now()), workDir: workDir, packetPath: packetPath, schema: schema, environment: environmentList(environment)}, nil
}

func roleTools(role domain.Role, tools []string) bool {
	want := []string{"Glob", "Grep", "Read"}
	if role == domain.RoleImplementer {
		want = []string{"Edit", "Glob", "Grep", "Read", "Write"}
	}
	got := append([]string(nil), tools...)
	sort.Strings(got)
	return len(got) == len(want) && strings.Join(got, "\x00") == strings.Join(want, "\x00")
}

func (runtime *Adapter) Probe(ctx context.Context, spec adapter.ProbeSpec) (adapter.Capabilities, error) {
	if !validComponent(spec.ProfileID) || (spec.Mode != adapter.ProbePassive && spec.Mode != adapter.ProbeActiveContract) {
		return adapter.Capabilities{}, errors.New("invalid Claude probe")
	}
	capabilities, err := runtime.passiveProbe(ctx, spec)
	if err != nil || spec.Mode == adapter.ProbePassive {
		return capabilities, err
	}
	if !spec.ProviderTransport || spec.Timeout <= 0 {
		return adapter.Capabilities{}, errors.New("active Claude probe requires provider transport and a positive timeout")
	}
	timeout := spec.Timeout
	id, err := randomID("probe")
	if err != nil {
		return adapter.Capabilities{}, err
	}
	directory := filepath.Join(runtime.root, "probes", id)
	if err := os.Mkdir(directory, 0o700); err != nil {
		return adapter.Capabilities{}, err
	}
	packet := protocol.WorkPacket{ProtocolVersion: protocol.WorkPacketVersion, Project: protocol.PacketProject{Name: "claude-active-probe", BaseTree: strings.Repeat("0", 40), Workspace: runtime.projectRoot}, Goal: protocol.PacketGoal{ID: "goal_claude_active_probe", Revision: 1, Summary: "verify Claude CLI contract", ContractHash: strings.Repeat("1", 64)}, WorkItem: protocol.PacketWorkItem{ID: "work_claude_active_probe", Title: "Claude active contract probe", Objective: "return one structured result without tools", ReadScope: []string{"/**"}, WriteScope: []string{"/probe/**"}, AcceptanceCriteria: []string{"AC-CLAUDE-ACTIVE"}, ValidatorIDs: []string{"adapter-contract"}}, Role: domain.RoleReviewer, Constraints: protocol.PacketConstraints{ProjectNetwork: "deny", ProjectSecrets: "deny"}, RequiredOutputSchema: protocol.AgentResultVersion}
	packetHash, err := packet.Hash()
	if err != nil {
		return adapter.Capabilities{}, err
	}
	packetContent, err := canonical.Marshal(packet)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	packetPath := filepath.Join(directory, "packet.json")
	if err := writeImmutable(packetPath, packetContent, 0o400); err != nil {
		return adapter.Capabilities{}, err
	}
	schema, err := protocol.Schema(protocol.SchemaAgentResult)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	inv := adapter.Invocation{InvocationID: id, AttemptID: "attempt_claude_active_probe", WorkItemID: packet.WorkItem.ID, ProfileID: spec.ProfileID, GoalRevisionHash: packet.Goal.ContractHash, PlanRevisionHash: strings.Repeat("2", 64), BaseTree: packet.Project.BaseTree, PacketHash: packetHash, Role: domain.RoleReviewer, WorkDir: runtime.projectRoot, PacketPath: packetPath, Prompt: activePrompt, OutputSchema: schema, Environment: runtime.environment, PermissionMode: "dontAsk", ToolPolicy: []string{"Read", "Glob", "Grep"}, Timeout: timeout, MaxOutputBytes: maxProbeOutput, SessionPolicy: adapter.SessionFresh}
	handle, err := runtime.Start(ctx, inv, nil)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	result, err := runtime.Wait(ctx, handle)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	if result.Status != protocol.ResultCompleted {
		return adapter.Capabilities{}, errors.New("active Claude probe did not complete")
	}
	executed, _ := runtime.execution(handle)
	executed.mu.RLock()
	sessionID := executed.sessionID
	executed.mu.RUnlock()
	capabilities.ProbeMode = adapter.ProbeActiveContract
	capabilities.ProviderTransport = "available"
	capabilities.ProbeRef = filepath.ToSlash(filepath.Join("probes", id+".json"))
	artifact := struct {
		ProtocolVersion string               `json:"protocol_version"`
		ID              string               `json:"id"`
		ProfileID       string               `json:"profile_id"`
		Capabilities    adapter.Capabilities `json:"capabilities"`
		Result          protocol.AgentResult `json:"result"`
		SessionID       string               `json:"session_id"`
	}{probeVersion, id, spec.ProfileID, capabilities, result, sessionID}
	content, err := canonical.Marshal(artifact)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	if err := writeImmutable(filepath.Join(runtime.root, "probes", id+".json"), content, 0o600); err != nil {
		return adapter.Capabilities{}, err
	}
	return capabilities, nil
}

func (runtime *Adapter) passiveProbe(ctx context.Context, spec adapter.ProbeSpec) (adapter.Capabilities, error) {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	version, err := runtime.runProbe(probeCtx, []string{runtime.binary, "--version"})
	if err != nil || !strings.Contains(strings.ToLower(version), "claude code") {
		return adapter.Capabilities{}, fmt.Errorf("%w: invalid Claude version output", adapter.ErrUnavailable)
	}
	help, err := runtime.runProbe(probeCtx, []string{runtime.binary, "--help"})
	if err != nil {
		return adapter.Capabilities{}, fmt.Errorf("%w: Claude help: %v", adapter.ErrUnavailable, err)
	}
	for _, required := range []string{"--print", "--output-format", "--json-schema", "--permission-mode", "--tools", "--allowedTools", "--resume"} {
		if !strings.Contains(help, required) {
			return adapter.Capabilities{}, fmt.Errorf("%w: Claude help lacks %s", adapter.ErrUnavailable, required)
		}
	}
	credential := "unknown"
	auth, authErr := runtime.runProbe(probeCtx, []string{runtime.binary, "auth", "status"})
	if authErr == nil && strings.Contains(auth, "\"loggedIn\": true") {
		credential = "available"
	} else if authErr != nil {
		credential = "missing"
	}
	return adapter.Capabilities{Version: strings.TrimSpace(version), StructuredOutput: true, StreamingEvents: true, ResumeSession: true, ToolAllowlist: true, ApprovalModes: []string{"dontAsk"}, ProbeMode: spec.Mode, ProviderTransport: "unknown", CredentialStatus: credential}, nil
}

func (runtime *Adapter) runProbe(ctx context.Context, argv []string) (string, error) {
	var stdout, stderr bytes.Buffer
	execution, err := supervisor.Run(ctx, supervisor.Command{Argv: argv, Dir: runtime.projectRoot, Env: environmentList(runtime.environment), Stdout: &limitedWriter{buffer: &stdout, limit: maxProbeOutput}, Stderr: &limitedWriter{buffer: &stderr, limit: maxProbeOutput}, GracePeriod: gracePeriod})
	output := strings.TrimSpace(redact.String(stdout.String() + "\n" + stderr.String()))
	if err != nil {
		return output, fmt.Errorf("command exited %d: %w", execution.ExitCode, err)
	}
	if execution.ExitCode != 0 {
		return output, fmt.Errorf("command exited %d", execution.ExitCode)
	}
	return output, nil
}

type limitedWriter struct {
	buffer         *bytes.Buffer
	limit, written int64
}

func (w *limitedWriter) Write(value []byte) (int, error) {
	if w.written+int64(len(value)) > w.limit {
		return len(value), errors.New("probe output exceeded its limit")
	}
	w.written += int64(len(value))
	_, err := w.buffer.Write(value)
	return len(value), err
}
func (stderr *boundedStderr) Write(value []byte) (int, error) {
	stderr.mu.Lock()
	defer stderr.mu.Unlock()
	if stderr.err != nil {
		return len(value), stderr.err
	}
	if err := stderr.limiter.consume(len(value)); err != nil {
		stderr.err = err
		stderr.cancel()
		return len(value), err
	}
	_, err := stderr.buffer.Write(value)
	if err != nil {
		stderr.err = err
		stderr.cancel()
	}
	return len(value), err
}
func (stderr *boundedStderr) persist(path string) error {
	stderr.mu.Lock()
	defer stderr.mu.Unlock()
	content := []byte(redact.String(strings.ToValidUTF8(stderr.buffer.String(), "�")))
	if err := writeImmutable(path, content, 0o600); err != nil {
		return err
	}
	return stderr.err
}

func readPacket(path string) (string, protocol.WorkPacket, error) {
	if !cleanAbsolute(path) {
		return "", protocol.WorkPacket{}, errors.New("Claude packet path must be clean and absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o222 != 0 || info.Size() > maxPacketBytes {
		return "", protocol.WorkPacket{}, errors.New("Claude packet is missing, linked, writable, or oversized")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", protocol.WorkPacket{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var packet protocol.WorkPacket
	if err := decoder.Decode(&packet); err != nil {
		return "", protocol.WorkPacket{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", protocol.WorkPacket{}, errors.New("Claude packet contains trailing JSON")
	}
	canonicalContent, err := canonical.Marshal(packet)
	if err != nil || !bytes.Equal(content, canonicalContent) {
		return "", protocol.WorkPacket{}, errors.New("Claude packet is not canonical")
	}
	return path, packet, nil
}

func validateSchema(content []byte, name, version string) ([]byte, string, error) {
	expected, err := protocol.Schema(name)
	if err != nil {
		return nil, "", err
	}
	left, err := decodeJSON(content)
	if err != nil {
		return nil, "", err
	}
	right, err := decodeJSON(expected)
	if err != nil {
		return nil, "", err
	}
	leftHash, err := canonical.Hash("agent-output-schema", version, left)
	if err != nil {
		return nil, "", err
	}
	rightHash, err := canonical.Hash("agent-output-schema", version, right)
	if err != nil || leftHash != rightHash {
		return nil, "", errors.New("Claude output schema is not the expected xgoal contract")
	}
	providerSchema := claudeSchema(right)
	providerHash, err := canonical.Hash("claude-output-schema", version, providerSchema)
	if err != nil {
		return nil, "", err
	}
	canonicalSchema, err := canonical.Marshal(providerSchema)
	return canonicalSchema, providerHash, err
}

func claudeSchema(value any) any {
	switch current := value.(type) {
	case map[string]any:
		delete(current, "$schema")
		delete(current, "$id")
		for key, child := range current {
			current[key] = claudeSchema(child)
		}
	case []any:
		for index, child := range current {
			current[index] = claudeSchema(child)
		}
	}
	return value
}
func decodeJSON(content []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON has trailing data")
	}
	return value, nil
}
func canonicalDirectory(path string) (string, error) {
	if !cleanAbsolute(path) {
		return "", errors.New("path must be clean and absolute")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", errors.New("path must name a directory")
	}
	return filepath.EvalSymlinks(path)
}
func cleanAbsolute(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\r\n\x00")
}
func buildEnvironment(input map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(input))
	for name, value := range input {
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, '\x00') {
			return nil, errors.New("invalid Claude environment")
		}
		result[name] = value
	}
	return result, nil
}
func environmentList(input map[string]string) []string {
	names := make([]string, 0, len(input))
	for name := range input {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]string, len(names))
	for i, name := range names {
		result[i] = name + "=" + input[name]
	}
	return result
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func randomID(prefix string) (string, error) {
	var entropy [12]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(entropy[:]), nil
}

var _ adapter.Adapter = (*Adapter)(nil)
