package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/canonical"
	"go.yaml.in/yaml/v3"
)

const (
	APIVersion                        = "xgoal.dev/v1alpha1"
	Kind                              = "Project"
	WorkspaceProviderCurrentDirectory = "current-directory"
)

var ErrMigrationRequired = errors.New("CONFIG_MIGRATION_REQUIRED: workspace.provider git-worktree is no longer executable; review the current-directory contract, update xgoal.yaml to current-directory, and restart the daemon; historical status and reports remain readable")

type Config struct {
	APIVersion    string        `yaml:"apiVersion" json:"apiVersion"`
	Kind          string        `yaml:"kind" json:"kind"`
	Metadata      Metadata      `yaml:"metadata" json:"metadata"`
	Project       Project       `yaml:"project" json:"project"`
	Orchestration Orchestration `yaml:"orchestration" json:"orchestration"`
	Agents        []Agent       `yaml:"agents" json:"agents"`
	Workspace     Workspace     `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Runtime       Runtime       `yaml:"runtime" json:"runtime"`
	ScopePolicy   ScopePolicy   `yaml:"scopePolicy,omitempty" json:"scopePolicy,omitempty"`
	Bootstrap     Bootstrap     `yaml:"bootstrap,omitempty" json:"bootstrap,omitempty"`
	Validators    []Validator   `yaml:"validators" json:"validators"`
	Review        Review        `yaml:"review,omitempty" json:"review,omitempty"`
	Policy        Policy        `yaml:"policy,omitempty" json:"policy,omitempty"`
	Report        Report        `yaml:"report,omitempty" json:"report,omitempty"`
}

type Metadata struct {
	Name string `yaml:"name" json:"name"`
}

type Project struct {
	BaseBranch        string  `yaml:"baseBranch" json:"baseBranch"`
	TrustedRepository bool    `yaml:"trustedRepository" json:"trustedRepository"`
	Harness           Harness `yaml:"harness,omitempty" json:"harness,omitempty"`
}

type Harness struct {
	Type     string `yaml:"type,omitempty" json:"type,omitempty"`
	Required bool   `yaml:"required,omitempty" json:"required,omitempty"`
}

type Orchestration struct {
	DefaultMode             string   `yaml:"defaultMode" json:"defaultMode"`
	MaxParallel             int      `yaml:"maxParallel" json:"maxParallel"`
	LeaseTTL                Duration `yaml:"leaseTTL" json:"leaseTTL"`
	HeartbeatInterval       Duration `yaml:"heartbeatInterval" json:"heartbeatInterval"`
	NoProgressLimit         int      `yaml:"noProgressLimit,omitempty" json:"noProgressLimit,omitempty"`
	IntegrationBranchPrefix string   `yaml:"integrationBranchPrefix,omitempty" json:"integrationBranchPrefix,omitempty"`
}

type Agent struct {
	ID                   string   `yaml:"id" json:"id"`
	Adapter              string   `yaml:"adapter" json:"adapter"`
	Command              string   `yaml:"command" json:"command"`
	Roles                []string `yaml:"roles" json:"roles"`
	Timeout              Duration `yaml:"timeout" json:"timeout"`
	Sandbox              string   `yaml:"sandbox,omitempty" json:"sandbox,omitempty"`
	PermissionMode       string   `yaml:"permissionMode,omitempty" json:"permissionMode,omitempty"`
	ProviderTransport    string   `yaml:"providerTransport" json:"providerTransport"`
	CredentialSource     string   `yaml:"credentialSource" json:"credentialSource"`
	ActiveProbe          string   `yaml:"activeProbe" json:"activeProbe"`
	AllowedTools         []string `yaml:"allowedTools,omitempty" json:"allowedTools,omitempty"`
	EnvironmentAllowlist []string `yaml:"environmentAllowlist,omitempty" json:"environmentAllowlist,omitempty"`
}

type Workspace struct {
	Provider              string   `yaml:"provider,omitempty" json:"provider,omitempty"`
	KeepFailed            bool     `yaml:"keepFailed,omitempty" json:"keepFailed,omitempty"`
	CleanupCompletedAfter Duration `yaml:"cleanupCompletedAfter,omitempty" json:"cleanupCompletedAfter,omitempty"`
}

type Runtime struct {
	Provider               string `yaml:"provider" json:"provider"`
	IsolationLevelRequired string `yaml:"isolationLevelRequired" json:"isolationLevelRequired"`
	ProjectNetwork         string `yaml:"projectNetwork" json:"projectNetwork"`
	ProjectSecrets         string `yaml:"projectSecrets" json:"projectSecrets"`
}

type ScopePolicy struct {
	Deny             []string `yaml:"deny,omitempty" json:"deny,omitempty"`
	ValidatorChanges string   `yaml:"validatorChanges,omitempty" json:"validatorChanges,omitempty"`
}

type Bootstrap struct {
	Commands []Command `yaml:"commands,omitempty" json:"commands,omitempty"`
}

type Command struct {
	ID      string   `yaml:"id" json:"id"`
	Argv    []string `yaml:"argv" json:"argv"`
	CWD     string   `yaml:"cwd,omitempty" json:"cwd,omitempty"`
	Timeout Duration `yaml:"timeout" json:"timeout"`
	Network string   `yaml:"network,omitempty" json:"network,omitempty"`
}

type Validator struct {
	ID                string      `yaml:"id" json:"id"`
	Type              string      `yaml:"type" json:"type"`
	Phases            []string    `yaml:"phases" json:"phases"`
	Argv              []string    `yaml:"argv,omitempty" json:"argv,omitempty"`
	CWD               string      `yaml:"cwd,omitempty" json:"cwd,omitempty"`
	Timeout           Duration    `yaml:"timeout" json:"timeout"`
	ExpectedExitCodes []int       `yaml:"expectedExitCodes,omitempty" json:"expectedExitCodes,omitempty"`
	Environment       Environment `yaml:"env,omitempty" json:"env,omitempty"`
	Required          bool        `yaml:"required" json:"required"`
	Flaky             Flaky       `yaml:"flaky,omitempty" json:"flaky,omitempty"`
}

type Environment struct {
	Allow []string `yaml:"allow,omitempty" json:"allow,omitempty"`
}

type Flaky struct {
	Enabled bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

type Review struct {
	RequiredInStandard        bool     `yaml:"requiredInStandard,omitempty" json:"requiredInStandard,omitempty"`
	BlockSeverities           []string `yaml:"blockSeverities,omitempty" json:"blockSeverities,omitempty"`
	RequireIndependentSession bool     `yaml:"requireIndependentSession,omitempty" json:"requireIndependentSession,omitempty"`
	PreferDifferentProvider   bool     `yaml:"preferDifferentProvider,omitempty" json:"preferDifferentProvider,omitempty"`
}

type Policy struct {
	GitPush             string `yaml:"gitPush,omitempty" json:"gitPush,omitempty"`
	PublishArtifact     string `yaml:"publishArtifact,omitempty" json:"publishArtifact,omitempty"`
	Production          string `yaml:"production,omitempty" json:"production,omitempty"`
	DestructiveCommands string `yaml:"destructiveCommands,omitempty" json:"destructiveCommands,omitempty"`
	ExpandScope         string `yaml:"expandScope,omitempty" json:"expandScope,omitempty"`
}

type Report struct {
	Formats                     []string `yaml:"formats,omitempty" json:"formats,omitempty"`
	IncludeAgentRawLogs         bool     `yaml:"includeAgentRawLogs,omitempty" json:"includeAgentRawLogs,omitempty"`
	IncludeReproductionCommands bool     `yaml:"includeReproductionCommands,omitempty" json:"includeReproductionCommands,omitempty"`
}

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a scalar")
	}
	parsed, err := time.ParseDuration(node.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", node.Value, err)
	}
	d.Duration = parsed
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("%q", d.String())), nil
}

func Load(reader io.Reader) (Config, error) {
	decoder := yaml.NewDecoder(reader)
	decoder.KnownFields(true)

	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, fmt.Errorf("decode config: multiple YAML documents are not allowed")
		}
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func LoadFile(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	return Load(file)
}

func (c Config) Hash() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash("project-config", APIVersion, c)
}

func (c Config) Validate() error {
	if c.APIVersion != APIVersion {
		return fmt.Errorf("apiVersion must be %q", APIVersion)
	}
	if c.Kind != Kind {
		return fmt.Errorf("kind must be %q", Kind)
	}
	if strings.TrimSpace(c.Metadata.Name) == "" {
		return fmt.Errorf("metadata.name is required")
	}
	if strings.TrimSpace(c.Project.BaseBranch) == "" {
		return fmt.Errorf("project.baseBranch is required")
	}
	if !c.Project.TrustedRepository {
		return fmt.Errorf("project.trustedRepository must be true for v0.1 L0 execution")
	}
	if c.Project.Harness.Type != "" && c.Project.Harness.Type != "autogo" {
		return fmt.Errorf("project.harness.type must be autogo when set")
	}
	if c.Project.Harness.Required && c.Project.Harness.Type == "" {
		return fmt.Errorf("project.harness.type is required when harness.required is true")
	}
	if c.Orchestration.DefaultMode != "fast" && c.Orchestration.DefaultMode != "standard" {
		return fmt.Errorf("orchestration.defaultMode must be fast or standard")
	}
	if c.Orchestration.MaxParallel != 1 {
		return fmt.Errorf("orchestration.maxParallel must be 1 in v0.1")
	}
	if c.Orchestration.LeaseTTL.Duration <= 0 || c.Orchestration.HeartbeatInterval.Duration <= 0 || c.Orchestration.LeaseTTL.Duration <= c.Orchestration.HeartbeatInterval.Duration {
		return fmt.Errorf("orchestration.leaseTTL must be greater than heartbeatInterval")
	}
	if c.Orchestration.NoProgressLimit < 0 {
		return fmt.Errorf("orchestration.noProgressLimit must be non-negative")
	}
	if len(c.Agents) == 0 {
		return fmt.Errorf("at least one agent profile is required")
	}
	if err := validateAgents(c.Agents); err != nil {
		return err
	}
	legacyWorkspace := c.Workspace.Provider == "git-worktree"
	if c.Workspace.Provider != "" && c.Workspace.Provider != WorkspaceProviderCurrentDirectory && !legacyWorkspace {
		return fmt.Errorf("workspace.provider must be current-directory when set")
	}
	if c.Workspace.CleanupCompletedAfter.Duration < 0 {
		return fmt.Errorf("workspace.cleanupCompletedAfter must be non-negative")
	}
	if c.Runtime.Provider != "local-process" {
		return fmt.Errorf("runtime.provider must be local-process in v0.1")
	}
	if c.Runtime.IsolationLevelRequired != "L0" {
		return fmt.Errorf("runtime.isolationLevelRequired must be L0 in v0.1")
	}
	if !oneOf(c.Runtime.ProjectNetwork, "deny", "require-gate", "allow") {
		return fmt.Errorf("runtime.projectNetwork must be deny, require-gate, or allow")
	}
	if !oneOf(c.Runtime.ProjectSecrets, "deny", "require-gate") {
		return fmt.Errorf("runtime.projectSecrets must be deny or require-gate")
	}
	if err := validateScopePolicy(c.ScopePolicy); err != nil {
		return err
	}
	if err := validateBootstrap(c.Bootstrap); err != nil {
		return err
	}
	if err := validateValidators(c.Validators); err != nil {
		return err
	}
	if err := validateReview(c.Review); err != nil {
		return err
	}
	if err := validatePolicy(c.Policy); err != nil {
		return err
	}
	if err := validateReport(c.Report); err != nil {
		return err
	}
	// Migration is a recognized, otherwise-valid historical configuration.
	// Do not turn unknown fields or unrelated validation failures into a
	// read-only daemon startup that appears to have accepted those errors.
	if legacyWorkspace {
		return ErrMigrationRequired
	}
	return nil
}

func validateAgents(agents []Agent) error {
	seen := make(map[string]struct{}, len(agents))
	for i, agent := range agents {
		prefix := fmt.Sprintf("agents[%d]", i)
		if agent.ID == "" {
			return fmt.Errorf("%s.id is required", prefix)
		}
		if _, exists := seen[agent.ID]; exists {
			return fmt.Errorf("duplicate agent id %q", agent.ID)
		}
		seen[agent.ID] = struct{}{}
		if !oneOf(agent.Adapter, "codex-cli", "claude-cli", "fake") {
			return fmt.Errorf("%s.adapter is unsupported", prefix)
		}
		if strings.TrimSpace(agent.Command) == "" || len(agent.Roles) == 0 || agent.Timeout.Duration <= 0 {
			return fmt.Errorf("%s command, roles, and positive timeout are required", prefix)
		}
		roles := make(map[string]struct{}, len(agent.Roles))
		for _, role := range agent.Roles {
			if !oneOf(role, "planner", "implementer", "reviewer") {
				return fmt.Errorf("%s has unsupported role %q", prefix, role)
			}
			if _, exists := roles[role]; exists {
				return fmt.Errorf("%s has duplicate role %q", prefix, role)
			}
			roles[role] = struct{}{}
		}
		if err := validateNames(prefix+".environmentAllowlist", agent.EnvironmentAllowlist, validEnvironmentName); err != nil {
			return err
		}
		if err := validateNames(prefix+".allowedTools", agent.AllowedTools, nonBlank); err != nil {
			return err
		}
		if agent.Adapter == "fake" {
			continue
		}
		if agent.Sandbox != "" && !oneOf(agent.Sandbox, "read-only", "workspace-write") {
			return fmt.Errorf("%s.sandbox is unsupported", prefix)
		}
		if agent.PermissionMode != "" && !oneOf(agent.PermissionMode, "acceptEdits", "bypassPermissions", "default", "dontAsk", "plan") {
			return fmt.Errorf("%s.permissionMode is unsupported", prefix)
		}
		if agent.ProviderTransport != "allow" {
			return fmt.Errorf("%s.providerTransport must be allow for provider CLI adapters", prefix)
		}
		if !oneOf(agent.CredentialSource, "cli-session", "secret-provider") {
			return fmt.Errorf("%s.credentialSource is unsupported", prefix)
		}
		if !oneOf(agent.ActiveProbe, "explicit", "disabled") {
			return fmt.Errorf("%s.activeProbe must be explicit or disabled", prefix)
		}
	}
	return nil
}

func validateValidators(validators []Validator) error {
	seen := make(map[string]struct{}, len(validators))
	for i, validator := range validators {
		prefix := fmt.Sprintf("validators[%d]", i)
		if validator.ID == "" {
			return fmt.Errorf("%s.id is required", prefix)
		}
		if _, exists := seen[validator.ID]; exists {
			return fmt.Errorf("duplicate validator id %q", validator.ID)
		}
		seen[validator.ID] = struct{}{}
		if !oneOf(validator.Type, "scope", "command", "file_assertion", "runtime_probe", "git_assertion") {
			return fmt.Errorf("%s.type is unsupported", prefix)
		}
		if len(validator.Phases) == 0 {
			return fmt.Errorf("%s.phases is required", prefix)
		}
		if err := validateNames(prefix+".phases", validator.Phases, func(value string) bool { return oneOf(value, "change", "final") }); err != nil {
			return err
		}
		if len(validator.Argv) == 0 || strings.TrimSpace(validator.Argv[0]) == "" {
			return fmt.Errorf("%s.argv is required for deterministic validators", prefix)
		}
		if validator.Timeout.Duration <= 0 {
			return fmt.Errorf("%s.timeout must be positive", prefix)
		}
		if !validRelativePath(validator.CWD) {
			return fmt.Errorf("%s.cwd must be a safe repository-relative path", prefix)
		}
		if err := validateNames(prefix+".env.allow", validator.Environment.Allow, validEnvironmentName); err != nil {
			return err
		}
	}
	return nil
}

func validateScopePolicy(policy ScopePolicy) error {
	for _, pattern := range policy.Deny {
		if !validScopePattern(pattern) {
			return fmt.Errorf("scopePolicy.deny contains invalid pattern %q", pattern)
		}
	}
	if policy.ValidatorChanges != "" && !oneOf(policy.ValidatorChanges, "deny", "human-gate") {
		return fmt.Errorf("scopePolicy.validatorChanges must be deny or human-gate")
	}
	return nil
}

func validateBootstrap(bootstrap Bootstrap) error {
	seen := make(map[string]struct{}, len(bootstrap.Commands))
	for i, command := range bootstrap.Commands {
		prefix := fmt.Sprintf("bootstrap.commands[%d]", i)
		if strings.TrimSpace(command.ID) == "" {
			return fmt.Errorf("%s.id is required", prefix)
		}
		if _, exists := seen[command.ID]; exists {
			return fmt.Errorf("duplicate bootstrap command id %q", command.ID)
		}
		seen[command.ID] = struct{}{}
		if len(command.Argv) == 0 || strings.TrimSpace(command.Argv[0]) == "" {
			return fmt.Errorf("%s.argv is required", prefix)
		}
		if command.Timeout.Duration <= 0 {
			return fmt.Errorf("%s.timeout must be positive", prefix)
		}
		if !validRelativePath(command.CWD) {
			return fmt.Errorf("%s.cwd must be a safe repository-relative path", prefix)
		}
		if command.Network != "" && !oneOf(command.Network, "deny", "require-gate", "allow") {
			return fmt.Errorf("%s.network must be deny, require-gate, or allow", prefix)
		}
	}
	return nil
}

func validateReview(review Review) error {
	return validateNames("review.blockSeverities", review.BlockSeverities, func(value string) bool {
		return oneOf(value, "blocker", "high", "medium", "low")
	})
}

func validatePolicy(policy Policy) error {
	if policy.GitPush != "" && policy.GitPush != "deny" {
		return fmt.Errorf("policy.gitPush must be deny in v0.1")
	}
	if policy.PublishArtifact != "" && policy.PublishArtifact != "deny" {
		return fmt.Errorf("policy.publishArtifact must be deny in v0.1")
	}
	if policy.Production != "" && policy.Production != "deny" {
		return fmt.Errorf("policy.production must be deny in v0.1")
	}
	if policy.DestructiveCommands != "" && !oneOf(policy.DestructiveCommands, "deny", "human-gate") {
		return fmt.Errorf("policy.destructiveCommands must be deny or human-gate")
	}
	if policy.ExpandScope != "" && !oneOf(policy.ExpandScope, "deny", "human-gate") {
		return fmt.Errorf("policy.expandScope must be deny or human-gate")
	}
	return nil
}

func validateReport(report Report) error {
	return validateNames("report.formats", report.Formats, func(value string) bool { return oneOf(value, "markdown", "json") })
}

func validateNames(field string, values []string, valid func(string) bool) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !valid(value) {
			return fmt.Errorf("%s contains invalid value %q", field, value)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contains duplicate value %q", field, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func nonBlank(value string) bool { return strings.TrimSpace(value) != "" }

func validEnvironmentName(value string) bool {
	if value == "" || !((value[0] >= 'A' && value[0] <= 'Z') || value[0] == '_') {
		return false
	}
	for i := 1; i < len(value); i++ {
		if !((value[i] >= 'A' && value[i] <= 'Z') || (value[i] >= '0' && value[i] <= '9') || value[i] == '_') {
			return false
		}
	}
	return true
}

func validRelativePath(value string) bool {
	if value == "" || value == "." {
		return true
	}
	if !utf8.ValidString(value) || path.IsAbs(value) || strings.Contains(value, "\\") || strings.ContainsRune(value, '\x00') || strings.Contains(value, "//") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	cleaned := path.Clean(value)
	return cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func validScopePattern(value string) bool {
	if !utf8.ValidString(value) || !strings.HasPrefix(value, "/") || value == "/" || strings.Contains(value, "\\") || strings.ContainsRune(value, '\x00') || strings.Contains(value[1:], "//") {
		return false
	}
	for _, segment := range strings.Split(value[1:], "/") {
		if segment == "" || segment == "." || segment == ".." || (strings.Contains(segment, "**") && segment != "**") {
			return false
		}
	}
	return true
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
