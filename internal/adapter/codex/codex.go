package codex

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
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/adapter"
	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
	callindex "github.com/monshunter/xgoal/internal/invocation"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/redact"
	"github.com/monshunter/xgoal/internal/supervisor"
)

const (
	adapterID           = "codex-cli"
	maxProbeOutputBytes = 4 << 20
	maxPacketBytes      = 8 << 20
	maxResultBytes      = 1 << 20
	defaultGracePeriod  = 2 * time.Second
	activeProbePrompt   = "Return a completed xgoal AgentResult JSON object. Use summary 'codex active contract probe'. Do not run tools and do not inspect or modify files."
)

type Config struct {
	Binary      string
	RuntimeRoot string
	ProjectRoot string
	Environment map[string]string
	Clock       clock.Clock
}

type Adapter struct {
	binary      string
	root        string
	projectRoot string
	environment map[string]string
	clock       clock.Clock

	mu         sync.Mutex
	executions map[string]*execution
}

type execution struct {
	done     chan struct{}
	cancel   context.CancelFunc
	running  *supervisor.Running
	metadata invocationArtifact

	mu        sync.RWMutex
	result    protocol.AgentResult
	err       error
	sessionID string
	canceled  bool
	once      sync.Once
}

type boundedStderr struct {
	live    *callindex.StderrLog
	mu      sync.Mutex
	buffer  bytes.Buffer
	limiter *outputLimiter
	cancel  func()
	err     error
}

func New(configuration Config) (*Adapter, error) {
	if configuration.Binary == "" || !filepath.IsAbs(configuration.RuntimeRoot) || filepath.Clean(configuration.RuntimeRoot) != configuration.RuntimeRoot ||
		!filepath.IsAbs(configuration.ProjectRoot) || filepath.Clean(configuration.ProjectRoot) != configuration.ProjectRoot {
		return nil, errors.New("Codex adapter requires binary, runtime root, and project root")
	}
	binary, err := exec.LookPath(configuration.Binary)
	if err != nil {
		return nil, fmt.Errorf("%w: locate Codex CLI: %v", adapter.ErrUnavailable, err)
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(binary)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("%w: Codex binary is not executable", adapter.ErrUnavailable)
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
	root := filepath.Join(runtimeRoot, "adapters", "codex")
	for _, directory := range []string{root, filepath.Join(root, "invocations"), filepath.Join(root, "plans"), filepath.Join(root, "acceptances"), filepath.Join(root, "reviews"), filepath.Join(root, "sessions"), filepath.Join(root, "probes")} {
		if err := ensurePrivateDirectory(directory); err != nil {
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
	return &Adapter{
		binary: binary, root: root, projectRoot: projectRoot,
		environment: environment, clock: configuration.Clock, executions: make(map[string]*execution),
	}, nil
}

func (runtime *Adapter) ID() string { return adapterID }

func (runtime *Adapter) Probe(ctx context.Context, spec adapter.ProbeSpec) (adapter.Capabilities, error) {
	if !validComponent(spec.ProfileID) {
		return adapter.Capabilities{}, errors.New("Codex probe profile id is invalid")
	}
	if spec.Mode != adapter.ProbePassive && spec.Mode != adapter.ProbeActiveContract {
		return adapter.Capabilities{}, fmt.Errorf("unsupported Codex probe mode %q", spec.Mode)
	}
	passive, err := runtime.passiveProbe(ctx, spec)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	if spec.Mode == adapter.ProbePassive {
		return passive, nil
	}
	if !spec.ProviderTransport || spec.Timeout <= 0 {
		return adapter.Capabilities{}, errors.New("active Codex probe requires provider transport and a positive timeout")
	}
	effective, err := adapter.ProbeExecution(spec, "codex-cli", passive.Version)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	passive.ExecutionConfig = &effective
	timeout := spec.Timeout
	probeID, err := randomID("probe")
	if err != nil {
		return adapter.Capabilities{}, err
	}
	probeDir := filepath.Join(runtime.root, "probes", probeID)
	if err := os.Mkdir(probeDir, 0o700); err != nil {
		return adapter.Capabilities{}, err
	}
	packet := protocol.WorkPacket{
		ProtocolVersion: protocol.WorkPacketVersion,
		Project:         protocol.PacketProject{Name: "codex-active-probe", BaseTree: strings.Repeat("0", 40), Workspace: runtime.projectRoot},
		Goal:            protocol.PacketGoal{ID: "goal_codex_active_probe", Revision: 1, Summary: "verify Codex CLI contract", ContractHash: strings.Repeat("1", 64)},
		WorkItem: protocol.PacketWorkItem{
			ID: "work_codex_active_probe", Title: "Codex active contract probe", Objective: "return one structured result without tools",
			ReadScope: []string{"/**"}, WriteScope: []string{"/probe/**"}, AcceptanceCriteria: []string{"AC-CODEX-ACTIVE"}, ValidatorIDs: []string{"adapter-contract"},
		},
		Role:                 domain.RoleReviewer,
		Constraints:          protocol.PacketConstraints{ProjectNetwork: "deny", ProjectSecrets: "deny"},
		RequiredOutputSchema: protocol.AgentResultVersion,
	}
	packetHash, err := packet.Hash()
	if err != nil {
		return adapter.Capabilities{}, err
	}
	packetContent, err := canonical.Marshal(packet)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	packetPath := filepath.Join(probeDir, "packet.json")
	if err := writeImmutable(packetPath, packetContent, 0o400); err != nil {
		return adapter.Capabilities{}, err
	}
	schema, err := protocol.Schema(protocol.SchemaAgentResult)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	invocation := adapter.Invocation{
		ExecutionConfig: &effective, PermissionMode: effective.PermissionMode,
		InvocationID: probeID, AttemptID: "attempt_codex_active_probe", WorkItemID: packet.WorkItem.ID,
		ProfileID: spec.ProfileID, GoalRevisionHash: packet.Goal.ContractHash, PlanRevisionHash: strings.Repeat("2", 64),
		BaseTree: packet.Project.BaseTree, PacketHash: packetHash, Role: domain.RoleReviewer,
		WorkDir: runtime.projectRoot, PacketPath: packetPath, Prompt: activeProbePrompt,
		OutputSchema: schema, Environment: runtime.environment, SandboxPolicy: "read-only",
		Timeout: timeout, MaxOutputBytes: maxProbeOutputBytes, SessionPolicy: adapter.SessionFresh,
	}
	handle, err := runtime.Start(ctx, invocation, nil)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	result, err := runtime.Wait(ctx, handle)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	if result.Status != protocol.ResultCompleted {
		return adapter.Capabilities{}, fmt.Errorf("active Codex probe returned status %q", result.Status)
	}
	sessionID, err := runtime.SessionID(handle)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	passive.ProbeMode = adapter.ProbeActiveContract
	passive.ProviderTransport = "available"
	passive.ProbeRef = filepath.ToSlash(filepath.Join("probes", probeID+".json"))
	probe := probeArtifact{
		ProtocolVersion: probeArtifactVersion, ID: probeID, ProfileID: spec.ProfileID,
		Mode: spec.Mode, Capabilities: passive, Result: result, SessionID: sessionID, CreatedAt: runtime.clock.Now().UTC(),
	}
	probeHash, err := canonical.Hash("codex-probe", probeArtifactVersion, probe)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	probe.ArtifactHash = probeHash
	content, err := canonical.Marshal(probe)
	if err != nil {
		return adapter.Capabilities{}, err
	}
	if err := writeImmutable(filepath.Join(runtime.root, "probes", probeID+".json"), content, 0o600); err != nil {
		return adapter.Capabilities{}, err
	}
	return passive, nil
}

func (runtime *Adapter) Start(ctx context.Context, invocation adapter.Invocation, sink adapter.EventSink) (adapter.Handle, error) {
	invocation.ExecutionConfig = invocation.ExecutionConfig.Clone()
	if invocation.SessionPolicy != adapter.SessionFresh {
		return adapter.Handle{}, errors.New("new Codex invocation requires fresh session policy")
	}
	return runtime.start(ctx, invocation, "", sink)
}

func (runtime *Adapter) Resume(ctx context.Context, invocation adapter.Invocation, sessionID string, sink adapter.EventSink) (adapter.Handle, error) {
	invocation.ExecutionConfig = invocation.ExecutionConfig.Clone()
	if invocation.ExecutionConfig == nil || !invocation.ExecutionConfig.Resumable() {
		return adapter.Handle{}, fmt.Errorf("%w: explicit model, reasoningEffort and CLI version are required for resume; start a fresh invocation", adapter.ErrSessionMismatch)
	}
	if invocation.SessionPolicy != adapter.SessionResumeCompatible || !validSessionID(sessionID) {
		return adapter.Handle{}, fmt.Errorf("%w: Codex resume policy or session id is invalid", adapter.ErrSessionMismatch)
	}
	probe, err := runtime.passiveProbe(ctx, adapter.ProbeSpec{Mode: adapter.ProbePassive, ProfileID: invocation.ProfileID, Timeout: 10 * time.Second})
	if err != nil || probe.Version != invocation.ExecutionConfig.CLIVersion {
		return adapter.Handle{}, fmt.Errorf("%w: current codex CLI version differs from session identity or could not be verified: %v", adapter.ErrSessionMismatch, err)
	}

	return runtime.start(ctx, invocation, sessionID, sink)
}

func (runtime *Adapter) start(ctx context.Context, invocation adapter.Invocation, resumeSessionID string, sink adapter.EventSink) (adapter.Handle, error) {
	validated, err := runtime.validateInvocation(invocation)
	if err != nil {
		return adapter.Handle{}, err
	}
	if resumeSessionID != "" {
		stored, err := readSession(runtime.root, resumeSessionID)
		if err != nil || !sameSessionBinding(stored, validated.metadata) {
			return adapter.Handle{}, fmt.Errorf("%w: persisted Codex session does not match invocation", adapter.ErrSessionMismatch)
		}
	}
	runtime.mu.Lock()
	if _, exists := runtime.executions[invocation.InvocationID]; exists {
		runtime.mu.Unlock()
		return adapter.Handle{}, fmt.Errorf("Codex invocation %q already exists", invocation.InvocationID)
	}
	invocationDir := filepath.Join(runtime.root, "invocations", invocation.InvocationID)
	if err := os.Mkdir(invocationDir, 0o700); err != nil {
		runtime.mu.Unlock()
		return adapter.Handle{}, fmt.Errorf("create Codex invocation directory: %w", err)
	}
	eventsDir := filepath.Join(invocationDir, "events")
	if err := os.Mkdir(eventsDir, 0o700); err != nil {
		runtime.mu.Unlock()
		return adapter.Handle{}, err
	}
	metadataContent, err := canonical.Marshal(validated.metadata)
	if err != nil {
		runtime.mu.Unlock()
		return adapter.Handle{}, err
	}
	if err := writeImmutable(filepath.Join(invocationDir, "invocation.json"), metadataContent, 0o600); err != nil {
		runtime.mu.Unlock()
		return adapter.Handle{}, err
	}
	schemaPath := filepath.Join(invocationDir, "output-schema.json")
	if err := writeImmutable(schemaPath, validated.schema, 0o400); err != nil {
		runtime.mu.Unlock()
		return adapter.Handle{}, err
	}
	runContext, cancel := context.WithTimeout(ctx, invocation.Timeout)
	limiter := &outputLimiter{remaining: invocation.MaxOutputBytes}
	stream := newJSONLStream(eventsDir, filepath.ToSlash(filepath.Join("invocations", invocation.InvocationID)), sink, runtime.clock, limiter, cancel)
	stream.runtimeRoot = filepath.Dir(filepath.Dir(runtime.root))
	stderr := &boundedStderr{limiter: limiter, cancel: cancel, live: callindex.NewStderrLog(filepath.Dir(filepath.Dir(runtime.root)), invocationDir)}
	arguments := runtime.arguments(invocation, schemaPath, resumeSessionID)
	running, err := supervisor.StartContext(runContext, supervisor.Command{
		Argv: arguments, Dir: validated.workDir, Env: validated.environment,
		Stdin: strings.NewReader(invocation.Prompt), Stdout: stream, Stderr: stderr,
		GracePeriod: defaultGracePeriod,
	})
	if err != nil {
		cancel()
		runtime.mu.Unlock()
		return adapter.Handle{}, err
	}
	executed := &execution{done: make(chan struct{}), cancel: cancel, running: running, metadata: validated.metadata}
	runtime.executions[invocation.InvocationID] = executed
	runtime.mu.Unlock()

	go runtime.observeExecution(runContext, executed, stream, stderr, invocation.MaxOutputBytes)
	return adapter.Handle{ID: invocation.InvocationID, PID: running.PID()}, nil
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
		return nil, errors.New("invalid Codex invocation handle")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	executed, exists := runtime.executions[handle.ID]
	if !exists {
		return nil, fmt.Errorf("unknown Codex invocation %q", handle.ID)
	}
	return executed, nil
}

func (runtime *Adapter) arguments(invocation adapter.Invocation, schemaPath, resumeSessionID string) []string {
	arguments := []string{
		runtime.binary, "--ask-for-approval", "never", "--sandbox", invocation.SandboxPolicy,
		"--cd", invocation.WorkDir,
	}
	arguments = append(arguments, executionArguments(invocation.ExecutionConfig)...)
	arguments = append(arguments, "exec")
	if resumeSessionID == "" {
		return append(arguments, "--json", "--output-schema", schemaPath, "--color", "never", "-")
	}
	return append(arguments, "resume", "--json", "--output-schema", schemaPath, resumeSessionID, "-")
}

func (runtime *Adapter) observeExecution(runContext context.Context, executed *execution, stream *jsonlStream, stderr *boundedStderr, maxOutputBytes int64) {
	process, processErr := executed.running.WaitAndStop(runContext)
	contextErr := runContext.Err()
	executed.cancel()
	stderrErr := stderr.persist(filepath.Join(runtime.root, "invocations", executed.metadata.InvocationID, "stderr.log"))
	result, sessionID, resultErr := stream.Finalize(min64(maxOutputBytes, maxResultBytes))
	streamErr := stream.failure()
	if resultErr == nil && contextErr == nil && stderrErr == nil && processErr == nil && process.ExitCode == 0 && sessionID != "" && stream.resumable() {
		binding, err := newSessionBinding(sessionID, executed.metadata)
		if err == nil {
			err = writeSession(runtime.root, binding)
		}
		if err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}
	var finalErr error
	switch {
	case errors.Is(processErr, supervisor.ErrProcessUnconfirmed):
		finalErr = processErr
	case streamErr != nil:
		finalErr = streamErr
	case stderrErr != nil:
		finalErr = stderrErr
	case contextErr != nil:
		finalErr = contextErr
	case resultErr != nil:
		finalErr = resultErr
	case processErr != nil:
		finalErr = fmt.Errorf("Codex CLI exited %d: %w", process.ExitCode, processErr)
	case process.ExitCode != 0:
		finalErr = fmt.Errorf("Codex CLI exited %d", process.ExitCode)
	}
	executed.finish(result, sessionID, finalErr)
}

func (executed *execution) finish(result protocol.AgentResult, sessionID string, err error) {
	executed.once.Do(func() {
		executed.mu.Lock()
		if executed.canceled && err == nil {
			err = context.Canceled
		}
		executed.result = result
		executed.err = err
		executed.sessionID = sessionID
		executed.mu.Unlock()
		close(executed.done)
	})
}

func (stderr *boundedStderr) Write(value []byte) (int, error) {
	stderr.mu.Lock()
	defer stderr.mu.Unlock()
	if stderr.err != nil {
		return len(value), stderr.err
	}
	if err := stderr.limiter.consume(len(value)); err != nil {
		stderr.err = err
		if stderr.live != nil {
			stderr.live.Fail(err)
		}
		stderr.cancel()
		return len(value), err
	}
	if stderr.live != nil {
		if _, err := stderr.live.Write(value); err != nil {
			stderr.err = err
			stderr.cancel()
			return len(value), err
		}
	}
	_, err := stderr.buffer.Write(value)
	if err != nil {
		stderr.err = err
		if stderr.live != nil {
			stderr.live.Fail(err)
		}
		stderr.cancel()
	}
	return len(value), err
}

func (stderr *boundedStderr) persist(filename string) error {
	stderr.mu.Lock()
	defer stderr.mu.Unlock()
	if stderr.live != nil {
		stderr.err = errors.Join(stderr.err, stderr.live.Close())
	}
	content := []byte(redact.String(strings.ToValidUTF8(stderr.buffer.String(), "�")))
	if err := writeImmutable(filename, content, 0o600); err != nil {
		return err
	}
	return stderr.err
}

type validatedInvocation struct {
	metadata    invocationArtifact
	workDir     string
	packetPath  string
	schema      []byte
	environment []string
}

func (runtime *Adapter) validateInvocation(invocation adapter.Invocation) (validatedInvocation, error) {
	if err := adapter.ValidateExecution(invocation.ExecutionConfig, invocation.ProfileID, "codex-cli", string(invocation.Role)); err != nil {
		return validatedInvocation{}, err
	}
	if e := invocation.ExecutionConfig; e != nil && (e.Sandbox != invocation.SandboxPolicy || e.PermissionMode != invocation.PermissionMode) {
		return validatedInvocation{}, errors.New("Codex effective permissions differ from invocation")
	}

	if !validComponent(invocation.InvocationID) || !validComponent(invocation.AttemptID) || !validComponent(invocation.WorkItemID) ||
		!validComponent(invocation.ProfileID) || !validHash(invocation.GoalRevisionHash, 64) || !validHash(invocation.PlanRevisionHash, 64) ||
		(!validHash(invocation.BaseTree, 40) && !validHash(invocation.BaseTree, 64)) || !validHash(invocation.PacketHash, 64) ||
		!invocation.Role.Valid() || strings.TrimSpace(invocation.Prompt) == "" || invocation.Timeout <= 0 ||
		invocation.MaxOutputBytes <= 0 || invocation.MaxOutputBytes > 128<<20 || len(invocation.ToolPolicy) != 0 {
		return validatedInvocation{}, errors.New("invalid Codex invocation identity, policy, timeout, or output limit")
	}
	if (invocation.Role == domain.RoleImplementer && invocation.SandboxPolicy != "workspace-write") ||
		((invocation.Role == domain.RolePlanner || invocation.Role == domain.RoleReviewer) && invocation.SandboxPolicy != "read-only") {
		return validatedInvocation{}, errors.New("Codex role and sandbox policy do not match")
	}
	workDir, err := canonicalDirectory(invocation.WorkDir)
	if err != nil || workDir != invocation.WorkDir {
		return validatedInvocation{}, errors.New("Codex workdir must be a canonical directory")
	}
	packetPath, packet, err := readPacket(invocation.PacketPath)
	if err != nil {
		return validatedInvocation{}, err
	}
	packetHash, err := packet.Hash()
	if err != nil || packetHash != invocation.PacketHash || packet.Project.Workspace != workDir || packet.WorkItem.ID != invocation.WorkItemID || packet.Role != invocation.Role {
		return validatedInvocation{}, errors.New("Codex work packet does not match invocation")
	}
	schema, schemaHash, err := validateOutputSchema(invocation.OutputSchema)
	if err != nil {
		return validatedInvocation{}, err
	}
	environment, err := buildEnvironment(invocation.Environment)
	if err != nil {
		return validatedInvocation{}, err
	}
	metadata := invocationMetadata(invocation, workDir, packetPath, schemaHash, runtime.clock.Now().UTC())
	return validatedInvocation{metadata: metadata, workDir: workDir, packetPath: packetPath, schema: schema, environment: environmentList(environment)}, nil
}

func (runtime *Adapter) passiveProbe(ctx context.Context, spec adapter.ProbeSpec) (adapter.Capabilities, error) {
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	probeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	version, err := runtime.runProbeCommand(probeContext, []string{runtime.binary, "--version"})
	if err != nil {
		return adapter.Capabilities{}, fmt.Errorf("%w: Codex version: %w", adapter.ErrUnavailable, err)
	}
	if !strings.Contains(version, "codex-cli ") {
		return adapter.Capabilities{}, fmt.Errorf("%w: invalid Codex version output", adapter.ErrUnavailable)
	}
	help, err := runtime.runProbeCommand(probeContext, []string{runtime.binary, "exec", "--help"})
	if err != nil {
		return adapter.Capabilities{}, fmt.Errorf("%w: Codex exec help: %w", adapter.ErrUnavailable, err)
	}
	resumeHelp, err := runtime.runProbeCommand(probeContext, []string{runtime.binary, "exec", "resume", "--help"})
	if err != nil {
		return adapter.Capabilities{}, fmt.Errorf("%w: Codex resume help: %w", adapter.ErrUnavailable, err)
	}
	for _, required := range []string{"--json", "--output-schema", "--sandbox"} {
		if required == "--sandbox" {
			if !strings.Contains(help, "--sandbox") {
				return adapter.Capabilities{}, fmt.Errorf("%w: Codex exec lacks %s", adapter.ErrUnavailable, required)
			}
			continue
		}
		if !strings.Contains(help, required) || !strings.Contains(resumeHelp, required) {
			return adapter.Capabilities{}, fmt.Errorf("%w: Codex exec/resume lacks %s", adapter.ErrUnavailable, required)
		}
	}
	credentialStatus := "unknown"
	login, loginErr := runtime.runProbeCommand(probeContext, []string{runtime.binary, "login", "status"})
	if errors.Is(loginErr, supervisor.ErrProcessUnconfirmed) || probeContext.Err() != nil {
		return adapter.Capabilities{}, fmt.Errorf("%w: Codex login status: %w", adapter.ErrUnavailable, errors.Join(loginErr, probeContext.Err()))
	}
	if loginErr == nil && strings.Contains(strings.ToLower(login), "logged in") {
		credentialStatus = "available"
	} else if loginErr != nil {
		credentialStatus = "missing"
	}
	return adapter.Capabilities{
		Version: strings.TrimSpace(version), StructuredOutput: true, StreamingEvents: true, ResumeSession: true,
		SandboxModes:  []string{"read-only", "workspace-write"},
		ToolAllowlist: false, ApprovalModes: []string{"never"}, ProbeMode: spec.Mode,
		ProviderTransport: "unknown", CredentialStatus: credentialStatus,
	}, nil
}

func (runtime *Adapter) runProbeCommand(ctx context.Context, argv []string) (string, error) {
	var stdout, stderr bytes.Buffer
	execution, err := supervisor.Run(ctx, supervisor.Command{
		Argv: argv, Dir: runtime.projectRoot, Env: environmentList(runtime.environment),
		Stdout:      &limitedBuffer{buffer: &stdout, limit: maxProbeOutputBytes},
		Stderr:      &limitedBuffer{buffer: &stderr, limit: maxProbeOutputBytes},
		GracePeriod: defaultGracePeriod,
	})
	output := strings.TrimSpace(redact.String(stdout.String() + "\n" + stderr.String()))
	if err != nil {
		return output, fmt.Errorf("command exited %d: %w", execution.ExitCode, err)
	}
	if execution.ExitCode != 0 {
		return output, fmt.Errorf("command exited %d", execution.ExitCode)
	}
	return output, nil
}

type limitedBuffer struct {
	buffer   *bytes.Buffer
	limit    int64
	written  int64
	exceeded bool
}

func (buffer *limitedBuffer) Write(value []byte) (int, error) {
	if buffer.written+int64(len(value)) > buffer.limit {
		buffer.exceeded = true
		return len(value), errors.New("probe output exceeded its limit")
	}
	buffer.written += int64(len(value))
	_, err := buffer.buffer.Write(value)
	return len(value), err
}

func readPacket(filename string) (string, protocol.WorkPacket, error) {
	if !filepath.IsAbs(filename) || filepath.Clean(filename) != filename {
		return "", protocol.WorkPacket{}, errors.New("Codex packet path must be clean and absolute")
	}
	info, err := os.Lstat(filename)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o222 != 0 || info.Size() > maxPacketBytes {
		return "", protocol.WorkPacket{}, errors.New("Codex packet is missing, linked, writable, or oversized")
	}
	content, err := os.ReadFile(filename)
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
		return "", protocol.WorkPacket{}, errors.New("Codex packet contains trailing JSON")
	}
	canonicalContent, err := canonical.Marshal(packet)
	if err != nil || !bytes.Equal(content, canonicalContent) {
		return "", protocol.WorkPacket{}, errors.New("Codex packet is not canonical")
	}
	return filename, packet, nil
}

func validateOutputSchema(content []byte) ([]byte, string, error) {
	return validateSchemaContract(content, protocol.SchemaAgentResult, protocol.AgentResultVersion)
}

func validateSchemaContract(content []byte, schemaName, schemaVersion string) ([]byte, string, error) {
	model, err := decodeJSONModel(content)
	if err != nil {
		return nil, "", fmt.Errorf("invalid Codex output schema: %w", err)
	}
	expected, err := protocol.Schema(schemaName)
	if err != nil {
		return nil, "", err
	}
	expectedModel, err := decodeJSONModel(expected)
	if err != nil {
		return nil, "", err
	}
	inputHash, err := canonical.Hash("agent-output-schema", schemaVersion, model)
	if err != nil {
		return nil, "", err
	}
	expectedHash, err := canonical.Hash("agent-output-schema", schemaVersion, expectedModel)
	if err != nil || inputHash != expectedHash {
		return nil, "", errors.New("Codex output schema is not the xgoal AgentResult contract")
	}
	providerModel, err := codexOutputSchema(expectedModel)
	if err != nil {
		return nil, "", err
	}
	hash, err := canonical.Hash("codex-output-schema", schemaVersion, providerModel)
	if err != nil {
		return nil, "", err
	}
	canonicalSchema, err := canonical.Marshal(providerModel)
	return canonicalSchema, hash, err
}

// codexOutputSchema converts the public validation schema into the strict
// structured-output subset required by the Codex provider. Optional result
// fields become required but retain their ordinary zero-value representations,
// so the decoded AgentResult contract is unchanged.
func codexOutputSchema(value any) (any, error) {
	switch current := value.(type) {
	case map[string]any:
		delete(current, "$schema")
		delete(current, "$id")
		for key, child := range current {
			converted, err := codexOutputSchema(child)
			if err != nil {
				return nil, err
			}
			current[key] = converted
		}
		if _, exists := current["type"]; !exists {
			if constant, ok := current["const"]; ok {
				current["type"] = jsonType(constant)
			} else if values, ok := current["enum"].([]any); ok && len(values) > 0 {
				kind := jsonType(values[0])
				for _, item := range values[1:] {
					if jsonType(item) != kind {
						return nil, errors.New("Codex output schema enum mixes JSON types")
					}
				}
				current["type"] = kind
			}
		}
		if current["type"] == "object" {
			properties, ok := current["properties"].(map[string]any)
			if !ok {
				return nil, errors.New("Codex output schema object lacks properties")
			}
			keys := make([]string, 0, len(properties))
			for key := range properties {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			required := make([]any, len(keys))
			for index, key := range keys {
				required[index] = key
			}
			current["required"] = required
			current["additionalProperties"] = false
		}
		return current, nil
	case []any:
		for index, child := range current {
			converted, err := codexOutputSchema(child)
			if err != nil {
				return nil, err
			}
			current[index] = converted
		}
		return current, nil
	default:
		return value, nil
	}
}

func jsonType(value any) string {
	switch value.(type) {
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	case nil:
		return "null"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return ""
	}
}

func decodeJSONModel(content []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var model any
	if err := decoder.Decode(&model); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON has trailing data")
	}
	return model, nil
}

func buildEnvironment(values map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(values)+2)
	for name, value := range values {
		if !validEnvironmentName(name) || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || sensitiveEnvironmentName(name) {
			return nil, fmt.Errorf("unsafe Codex environment entry %q", name)
		}
		result[name] = value
	}
	if _, exists := result["LANG"]; !exists {
		result["LANG"] = "C"
	}
	if _, exists := result["LC_ALL"]; !exists {
		result["LC_ALL"] = "C"
	}
	return result, nil
}

func environmentList(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	sort.Strings(result)
	return result
}

func validEnvironmentName(value string) bool {
	if value == "" || (value[0] != '_' && (value[0] < 'A' || value[0] > 'Z') && (value[0] < 'a' || value[0] > 'z')) {
		return false
	}
	for _, character := range value[1:] {
		if character != '_' && (character < 'A' || character > 'Z') && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func sensitiveEnvironmentName(value string) bool {
	upper := strings.ToUpper(value)
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "API_KEY", "ACCESS_KEY", "PRIVATE_KEY", "AUTHORIZATION"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}

func canonicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("directory must be clean and absolute")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("directory is missing")
	}
	return resolved, nil
}

func randomID(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(value), nil
}

func min64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func (stream *jsonlStream) resumable() bool {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	return stream.parseErr == nil && len(stream.pending) == 0 && stream.sessionID != ""
}

var _ adapter.Adapter = (*Adapter)(nil)
