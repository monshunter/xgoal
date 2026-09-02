package protocol

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
)

const (
	WorkPacketVersion  = "xgoal.work-packet/v1alpha1"
	AgentResultVersion = "xgoal.agent-result/v1alpha1"
	AgentEventVersion  = "xgoal.agent-event/v1alpha1"
	EvidenceVersion    = "xgoal.evidence/v1alpha1"

	SchemaWorkPacket  = "work-packet"
	SchemaAgentResult = "agent-result"
	SchemaAgentEvent  = "agent-event"
	SchemaEvidence    = "evidence"
)

type WorkPacket struct {
	ProtocolVersion      string              `json:"protocol_version"`
	Project              PacketProject       `json:"project"`
	Goal                 PacketGoal          `json:"goal"`
	WorkItem             PacketWorkItem      `json:"work_item"`
	Role                 domain.Role         `json:"role"`
	Constraints          PacketConstraints   `json:"constraints"`
	Environment          PacketEnvironment   `json:"environment"`
	PriorAttempt         *PacketPriorAttempt `json:"prior_attempt,omitempty"`
	RequiredOutputSchema string              `json:"required_output_schema"`
}

type PacketProject struct {
	Name      string `json:"name"`
	BaseTree  string `json:"base_tree"`
	Workspace string `json:"workspace"`
}

type PacketGoal struct {
	ID           string `json:"id"`
	Revision     int64  `json:"revision"`
	Summary      string `json:"summary"`
	ContractHash string `json:"contract_hash"`
}

type PacketWorkItem struct {
	ID                 string   `json:"id"`
	Title              string   `json:"title"`
	Objective          string   `json:"objective"`
	Dependencies       []string `json:"dependencies,omitempty"`
	ReadScope          []string `json:"read_scope"`
	WriteScope         []string `json:"write_scope"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	ValidatorIDs       []string `json:"validator_ids"`
}

type PacketConstraints struct {
	ProjectNetwork string `json:"project_network"`
	ProjectSecrets string `json:"project_secrets"`
	GitPush        bool   `json:"git_push"`
	Production     bool   `json:"production"`
}

type PacketEnvironment struct {
	OS             string            `json:"os,omitempty"`
	Arch           string            `json:"arch,omitempty"`
	GitCommit      string            `json:"git_commit,omitempty"`
	ToolVersions   map[string]string `json:"tool_versions,omitempty"`
	LockfileHashes map[string]string `json:"lockfile_hashes,omitempty"`
}

type PacketPriorAttempt struct {
	FailureClass       string   `json:"failure_class"`
	FailureFingerprint string   `json:"failure_fingerprint"`
	EvidenceRefs       []string `json:"evidence_refs,omitempty"`
}

func (packet WorkPacket) Validate() error {
	if packet.ProtocolVersion != WorkPacketVersion {
		return fmt.Errorf("protocol_version must be %q", WorkPacketVersion)
	}
	if packet.Project.Name == "" || packet.Project.BaseTree == "" || !filepath.IsAbs(packet.Project.Workspace) {
		return fmt.Errorf("project name, base_tree, and absolute workspace are required")
	}
	if packet.Goal.ID == "" || packet.Goal.Revision <= 0 || packet.Goal.Summary == "" || packet.Goal.ContractHash == "" {
		return fmt.Errorf("goal id, positive revision, summary, and contract_hash are required")
	}
	if packet.WorkItem.ID == "" || packet.WorkItem.Title == "" || packet.WorkItem.Objective == "" {
		return fmt.Errorf("work_item id, title, and objective are required")
	}
	if len(packet.WorkItem.ReadScope) == 0 || len(packet.WorkItem.WriteScope) == 0 {
		return fmt.Errorf("work_item read_scope and write_scope are required")
	}
	for _, scope := range packet.WorkItem.ReadScope {
		if !validScope(scope) {
			return fmt.Errorf("invalid read_scope %q", scope)
		}
	}
	for _, scope := range packet.WorkItem.WriteScope {
		if !validScope(scope) {
			return fmt.Errorf("invalid write_scope %q", scope)
		}
	}
	if len(packet.WorkItem.AcceptanceCriteria) == 0 || len(packet.WorkItem.ValidatorIDs) == 0 {
		return fmt.Errorf("work_item acceptance_criteria and validator_ids are required")
	}
	if !packet.Role.Valid() {
		return fmt.Errorf("invalid role %q", packet.Role)
	}
	if !oneOf(packet.Constraints.ProjectNetwork, "deny", "require-gate", "allow") {
		return fmt.Errorf("constraints.project_network must be deny, require-gate, or allow")
	}
	if !oneOf(packet.Constraints.ProjectSecrets, "deny", "require-gate") {
		return fmt.Errorf("constraints.project_secrets must be deny or require-gate")
	}
	if packet.Constraints.GitPush || packet.Constraints.Production {
		return fmt.Errorf("constraints.git_push and constraints.production must be false in v0.1")
	}
	if packet.PriorAttempt != nil && (strings.TrimSpace(packet.PriorAttempt.FailureClass) == "" || strings.TrimSpace(packet.PriorAttempt.FailureFingerprint) == "") {
		return fmt.Errorf("prior_attempt failure_class and failure_fingerprint are required")
	}
	if packet.RequiredOutputSchema != AgentResultVersion {
		return fmt.Errorf("required_output_schema must be %q", AgentResultVersion)
	}
	return nil
}

func (packet WorkPacket) Hash() (string, error) {
	if err := packet.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash(SchemaWorkPacket, WorkPacketVersion, packet)
}

func validScope(scope string) bool {
	if !utf8.ValidString(scope) || !strings.HasPrefix(scope, "/") || scope == "/" || strings.Contains(scope, "\\") || strings.ContainsRune(scope, '\x00') {
		return false
	}
	if strings.Contains(scope[1:], "//") {
		return false
	}
	for _, segment := range strings.Split(scope[1:], "/") {
		if segment == "" || segment == "." || segment == ".." || segment == ".git" {
			return false
		}
		if strings.Contains(segment, "**") && segment != "**" {
			return false
		}
	}
	return true
}

type ResultStatus string

const (
	ResultCompleted ResultStatus = "completed"
	ResultBlocked   ResultStatus = "blocked"
	ResultFailed    ResultStatus = "failed"
)

type AgentResult struct {
	ProtocolVersion       string       `json:"protocol_version"`
	Status                ResultStatus `json:"status"`
	Summary               string       `json:"summary"`
	ChangedFilesClaimed   []string     `json:"changed_files_claimed,omitempty"`
	ChecksClaimed         []CheckClaim `json:"checks_claimed,omitempty"`
	Blockers              []string     `json:"blockers,omitempty"`
	Assumptions           []string     `json:"assumptions,omitempty"`
	RecommendedNextAction string       `json:"recommended_next_action,omitempty"`
}

type CheckClaim struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

func (result AgentResult) Validate() error {
	if result.ProtocolVersion != AgentResultVersion {
		return fmt.Errorf("protocol_version must be %q", AgentResultVersion)
	}
	if result.Status != ResultCompleted && result.Status != ResultBlocked && result.Status != ResultFailed {
		return fmt.Errorf("invalid result status %q", result.Status)
	}
	if strings.TrimSpace(result.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	return nil
}

func DecodeAgentResult(reader io.Reader, maxBytes int64) (AgentResult, error) {
	if maxBytes <= 0 {
		return AgentResult{}, fmt.Errorf("maxBytes must be positive")
	}
	limited := &io.LimitedReader{R: reader, N: maxBytes + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var result AgentResult
	if err := decoder.Decode(&result); err != nil {
		return AgentResult{}, fmt.Errorf("decode agent result: %w", err)
	}
	if limited.N == 0 {
		return AgentResult{}, fmt.Errorf("agent result exceeds %d bytes", maxBytes)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return AgentResult{}, fmt.Errorf("decode agent result: multiple JSON values are not allowed")
		}
		return AgentResult{}, fmt.Errorf("decode agent result: %w", err)
	}
	if err := result.Validate(); err != nil {
		return AgentResult{}, err
	}
	return result, nil
}

func (AgentResult) Authority() domain.Authority { return domain.AuthorityClaim }

type AgentEvent struct {
	ProtocolVersion string           `json:"protocol_version"`
	Type            string           `json:"type"`
	At              time.Time        `json:"at"`
	SessionID       string           `json:"session_id,omitempty"`
	Summary         string           `json:"summary,omitempty"`
	Command         *CommandClaim    `json:"command,omitempty"`
	FileChange      *FileChangeClaim `json:"file_change,omitempty"`
	Usage           *Usage           `json:"usage,omitempty"`
	RawRef          string           `json:"raw_ref,omitempty"`
}

type CommandClaim struct {
	Argv       []string `json:"argv"`
	ExitStatus *int     `json:"exit_status,omitempty"`
}

type FileChangeClaim struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type Usage struct {
	InputTokens  *int64 `json:"input_tokens,omitempty"`
	OutputTokens *int64 `json:"output_tokens,omitempty"`
	CostMicros   *int64 `json:"cost_micros,omitempty"`
	Currency     string `json:"currency,omitempty"`
}

func (event AgentEvent) Validate() error {
	if event.ProtocolVersion != AgentEventVersion || strings.TrimSpace(event.Type) == "" || event.At.IsZero() {
		return fmt.Errorf("agent event protocol_version, type, and at are required")
	}
	if event.Command != nil && len(event.Command.Argv) == 0 {
		return fmt.Errorf("agent event command.argv is required")
	}
	if event.FileChange != nil && (strings.TrimSpace(event.FileChange.Path) == "" || strings.TrimSpace(event.FileChange.Kind) == "") {
		return fmt.Errorf("agent event file_change path and kind are required")
	}
	if event.Usage != nil && (negative(event.Usage.InputTokens) || negative(event.Usage.OutputTokens) || negative(event.Usage.CostMicros)) {
		return fmt.Errorf("agent event usage values must be non-negative")
	}
	return nil
}

func negative(value *int64) bool { return value != nil && *value < 0 }

type Evidence struct {
	ProtocolVersion  string               `json:"protocol_version"`
	ID               string               `json:"id"`
	Kind             string               `json:"kind"`
	SubjectID        string               `json:"subject_id"`
	Producer         string               `json:"producer"`
	Authority        domain.Authority     `json:"authority"`
	GoalRevisionHash string               `json:"goal_revision_hash"`
	ConfigHash       string               `json:"config_hash"`
	TreeHash         string               `json:"tree_hash"`
	PayloadHash      string               `json:"payload_hash"`
	State            domain.EvidenceState `json:"state"`
	CreatedAt        time.Time            `json:"created_at"`
}

func (evidence Evidence) Validate() error {
	if evidence.ProtocolVersion != EvidenceVersion {
		return fmt.Errorf("protocol_version must be %q", EvidenceVersion)
	}
	if evidence.ID == "" || evidence.Kind == "" || evidence.SubjectID == "" || evidence.Producer == "" || !evidence.Authority.Valid() {
		return fmt.Errorf("evidence identity and authority fields are required")
	}
	if evidence.GoalRevisionHash == "" || evidence.ConfigHash == "" || evidence.TreeHash == "" || evidence.PayloadHash == "" {
		return fmt.Errorf("evidence hashes are required")
	}
	if !evidence.State.Valid() || evidence.CreatedAt.IsZero() {
		return fmt.Errorf("evidence state and created_at are required")
	}
	return nil
}

//go:embed schema/*.json
var schemaFiles embed.FS

func Schema(name string) ([]byte, error) {
	file, ok := map[string]string{
		SchemaWorkPacket:          "schema/work-packet-v1alpha1.json",
		SchemaAgentResult:         "schema/agent-result-v1alpha1.json",
		SchemaAgentEvent:          "schema/agent-event-v1alpha1.json",
		SchemaEvidence:            "schema/evidence-v1alpha1.json",
		SchemaPatchBundle:         "schema/patch-bundle-v1alpha1.json",
		SchemaCommandReceipt:      "schema/command-receipt-v1alpha1.json",
		SchemaEnvironmentSnapshot: "schema/environment-snapshot-v1alpha1.json",
	}[name]
	if !ok {
		return nil, fmt.Errorf("unknown schema %q", name)
	}
	data, err := schemaFiles.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("read embedded schema: %w", err)
	}
	return data, nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
