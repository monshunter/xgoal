package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/monshunter/xgoal/internal/scope"
)

// HarnessInput describes supplied project knowledge, not agent execution state.
// File discovery and path delivery do not prove that the model loaded a rule.
type HarnessInput struct {
	Type            string            `json:"type"`
	Provider        string            `json:"provider"`
	Required        bool              `json:"required"`
	Found           bool              `json:"found"`
	Compatible      bool              `json:"compatible"`
	Files           []InstructionFile `json:"files"`
	Diagnostics     []string          `json:"diagnostics,omitempty"`
	Delegation      string            `json:"delegation"`
	Delivery        string            `json:"delivery"`
	LoadObservation string            `json:"load_observation"`
}

type InstructionFile struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
}

func (input HarnessInput) Validate() error {
	if input.Provider == "" || len(input.Files) > 256 || input.Delivery != "packet-path-references" || input.LoadObservation != "unknown" || strings.TrimSpace(input.Delegation) == "" || len(input.Delegation) > 8192 {
		return fmt.Errorf("invalid project Harness input")
	}
	seen := map[string]bool{}
	for _, file := range input.Files {
		path, err := scope.NormalizeRepositoryPath(file.Path)
		if err != nil || path != file.Path || seen[path] || len(file.SHA256) != 64 || file.Kind == "" {
			return fmt.Errorf("invalid Harness file reference %q", file.Path)
		}
		for _, c := range file.SHA256 {
			if !strings.ContainsRune("0123456789abcdef", c) {
				return fmt.Errorf("invalid Harness hash")
			}
		}
		seen[path] = true
	}
	return nil
}

const DelegationInstructions = `You are a bounded worker delegated by xgoal. Follow project business rules and relevant skills within the immutable packet's role, scope and permissions.
xgoal's Kernel owns the Goal/Plan/Attempt state, approval gates, branch and commit operations, environment/service lifecycle, evidence, promotion and completion. Do not create an independent top-level Objective, Plan or PROGRESS tracking loop, commit, switch branches, install a Harness, change xgoal state files, or start/stop shared environments. Report missing capabilities, authorization or required clarification through the role's structured blocked/ambiguity result. A provided answer applies only to its gate and never expands authority.
Read the packet's Harness file references as needed for the current task. Discovery or a supplied path does not prove a rule was loaded. Your completion statement is a claim; xgoal runs trusted validators and decides completion. Projects without a Harness follow this same delegation contract.`

func DelegationHash() string {
	hash := sha256.Sum256([]byte(DelegationInstructions))
	return hex.EncodeToString(hash[:])
}
