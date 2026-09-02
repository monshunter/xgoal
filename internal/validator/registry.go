package validator

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/scope"
)

const (
	definitionVersion = "xgoal.validator-definition/v1alpha1"
	maxConfigBytes    = 8 << 20
	maxScriptBytes    = 8 << 20
)

type Definition struct {
	ID                    string        `json:"id"`
	Type                  string        `json:"type"`
	Phases                []string      `json:"phases"`
	Argv                  []string      `json:"argv,omitempty"`
	CWD                   string        `json:"cwd"`
	Timeout               time.Duration `json:"timeout_nanos"`
	ExpectedExitCodes     []int         `json:"expected_exit_codes"`
	EnvironmentAllowlist  []string      `json:"environment_allowlist,omitempty"`
	Required              bool          `json:"required"`
	Flaky                 bool          `json:"flaky"`
	TrustedExecutablePath string        `json:"trusted_executable_path,omitempty"`
	TrustedExecutableHash string        `json:"trusted_executable_hash,omitempty"`
	Hash                  string        `json:"hash"`
}

type definitionIdentity struct {
	ID                    string   `json:"id"`
	Type                  string   `json:"type"`
	Phases                []string `json:"phases"`
	Argv                  []string `json:"argv,omitempty"`
	CWD                   string   `json:"cwd"`
	TimeoutNanos          int64    `json:"timeout_nanos"`
	ExpectedExitCodes     []int    `json:"expected_exit_codes"`
	EnvironmentAllowlist  []string `json:"environment_allowlist,omitempty"`
	Required              bool     `json:"required"`
	Flaky                 bool     `json:"flaky"`
	TrustedExecutablePath string   `json:"trusted_executable_path,omitempty"`
	TrustedExecutableHash string   `json:"trusted_executable_hash,omitempty"`
}

type Registry struct {
	baseCommit  string
	baseTree    string
	configHash  string
	definitions map[string]Definition
}

func LoadRegistry(ctx context.Context, repository *gitrepo.Repository, baseCommit, configPath string) (*Registry, error) {
	if repository == nil || baseCommit == "" {
		return nil, errors.New("repository and base commit are required")
	}
	canonicalPath, err := scope.NormalizeRepositoryPath(configPath)
	if err != nil || canonicalPath != configPath {
		return nil, errors.New("validator config path must be canonical and repository-relative")
	}
	base, err := repository.ResolveRevision(ctx, baseCommit)
	if err != nil {
		return nil, err
	}
	entry, content, err := repository.ReadFileAtRevision(ctx, base.Commit, canonicalPath, maxConfigBytes)
	if err != nil {
		return nil, err
	}
	if entry.Mode != "100644" && entry.Mode != "100755" {
		return nil, errors.New("validator config must be a regular Git file")
	}
	cfg, err := config.Load(bytes.NewReader(content))
	if err != nil {
		return nil, err
	}
	configHash, err := cfg.Hash()
	if err != nil {
		return nil, err
	}
	definitions := make(map[string]Definition, len(cfg.Validators))
	for _, configured := range cfg.Validators {
		definition, err := buildDefinition(ctx, repository, base.Commit, configured)
		if err != nil {
			return nil, fmt.Errorf("validator %q: %w", configured.ID, err)
		}
		definitions[definition.ID] = definition
	}
	return &Registry{baseCommit: base.Commit, baseTree: base.Tree, configHash: configHash, definitions: definitions}, nil
}

func (registry *Registry) BaseCommit() string { return registry.baseCommit }
func (registry *Registry) BaseTree() string   { return registry.baseTree }
func (registry *Registry) ConfigHash() string { return registry.configHash }

func (definition Definition) Validate() error {
	if definition.ID == "" || strings.TrimSpace(definition.ID) != definition.ID || strings.ContainsAny(definition.ID, "\r\n\x00") ||
		definition.CWD == "" || definition.Timeout <= 0 || len(definition.Phases) == 0 || len(definition.ExpectedExitCodes) == 0 {
		return errors.New("validator definition identity, cwd, phases, timeout, and exit codes are required")
	}
	switch definition.Type {
	case "scope", "command", "file_assertion", "runtime_probe", "git_assertion":
	default:
		return fmt.Errorf("unsupported validator type %q", definition.Type)
	}
	if len(definition.Argv) == 0 {
		return errors.New("deterministic validator requires argv")
	}
	if !sortedUniqueStrings(definition.Phases) || !sortedUniqueStrings(definition.EnvironmentAllowlist) {
		return errors.New("validator phases and environment names must be uniquely sorted")
	}
	for index, exitCode := range definition.ExpectedExitCodes {
		if exitCode < 0 || exitCode > 255 || (index > 0 && definition.ExpectedExitCodes[index-1] >= exitCode) {
			return errors.New("validator expected exit codes must be uniquely sorted from 0 through 255")
		}
	}
	if (definition.TrustedExecutablePath == "") != (definition.TrustedExecutableHash == "") ||
		(definition.TrustedExecutableHash != "" && !validDefinitionHash(definition.TrustedExecutableHash)) {
		return errors.New("validator trusted executable binding is incomplete")
	}
	hash, err := canonical.Hash("validator-definition", definitionVersion, definition.identity())
	if err != nil {
		return err
	}
	if hash != definition.Hash {
		return errors.New("validator definition hash mismatch")
	}
	return nil
}

func (registry *Registry) Definition(id string) (Definition, bool) {
	definition, exists := registry.definitions[id]
	if !exists {
		return Definition{}, false
	}
	return cloneDefinition(definition), true
}

func (registry *Registry) Definitions() []Definition {
	result := make([]Definition, 0, len(registry.definitions))
	for _, definition := range registry.definitions {
		result = append(result, cloneDefinition(definition))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func buildDefinition(ctx context.Context, repository *gitrepo.Repository, baseCommit string, configured config.Validator) (Definition, error) {
	phases := append([]string(nil), configured.Phases...)
	sort.Strings(phases)
	environment := append([]string(nil), configured.Environment.Allow...)
	sort.Strings(environment)
	expected := append([]int(nil), configured.ExpectedExitCodes...)
	if len(expected) == 0 {
		expected = []int{0}
	}
	sort.Ints(expected)
	for index, exitCode := range expected {
		if exitCode < 0 || exitCode > 255 || (index > 0 && expected[index-1] == exitCode) {
			return Definition{}, errors.New("expected exit codes must be unique values from 0 through 255")
		}
	}
	cwd := configured.CWD
	if cwd == "" {
		cwd = "."
	}
	definition := Definition{
		ID: configured.ID, Type: configured.Type, Phases: phases,
		Argv: append([]string(nil), configured.Argv...), CWD: cwd, Timeout: configured.Timeout.Duration,
		ExpectedExitCodes: expected, EnvironmentAllowlist: environment,
		Required: configured.Required, Flaky: configured.Flaky.Enabled,
	}
	if strings.Contains(configured.Argv[0], "/") {
		if !strings.HasPrefix(configured.Argv[0], "./") {
			return Definition{}, errors.New("repository executable must use a ./ relative path")
		}
		executablePath, err := scope.NormalizeRepositoryPath(strings.TrimPrefix(configured.Argv[0], "./"))
		if err != nil {
			return Definition{}, fmt.Errorf("invalid repository executable: %w", err)
		}
		entry, content, err := repository.ReadFileAtRevision(ctx, baseCommit, executablePath, maxScriptBytes)
		if err != nil {
			return Definition{}, fmt.Errorf("read trusted executable: %w", err)
		}
		if entry.Mode != "100755" {
			return Definition{}, errors.New("trusted repository executable must have Git mode 100755")
		}
		digest := sha256.Sum256(content)
		definition.TrustedExecutablePath = executablePath
		definition.TrustedExecutableHash = hex.EncodeToString(digest[:])
	}
	hash, err := canonical.Hash("validator-definition", definitionVersion, definition.identity())
	if err != nil {
		return Definition{}, err
	}
	definition.Hash = hash
	if err := definition.Validate(); err != nil {
		return Definition{}, err
	}
	return definition, nil
}

func sortedUniqueStrings(values []string) bool {
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}

func validDefinitionHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (definition Definition) identity() definitionIdentity {
	return definitionIdentity{
		ID: definition.ID, Type: definition.Type, Phases: definition.Phases, Argv: definition.Argv,
		CWD: definition.CWD, TimeoutNanos: int64(definition.Timeout), ExpectedExitCodes: definition.ExpectedExitCodes,
		EnvironmentAllowlist: definition.EnvironmentAllowlist, Required: definition.Required, Flaky: definition.Flaky,
		TrustedExecutablePath: definition.TrustedExecutablePath, TrustedExecutableHash: definition.TrustedExecutableHash,
	}
}

func cloneDefinition(definition Definition) Definition {
	definition.Phases = append([]string(nil), definition.Phases...)
	definition.Argv = append([]string(nil), definition.Argv...)
	definition.ExpectedExitCodes = append([]int(nil), definition.ExpectedExitCodes...)
	definition.EnvironmentAllowlist = append([]string(nil), definition.EnvironmentAllowlist...)
	return definition
}
