package policy

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monshunter/xgoal/internal/domain"
)

type Decision string

const (
	Allow       Decision = "ALLOW"
	Deny        Decision = "DENY"
	RequireGate Decision = "REQUIRE_GATE"
)

type Request struct {
	Role           domain.Role
	Action         domain.PolicyAction
	Resources      []string
	WriteScope     []string
	TrustedProfile bool
	CLISession     bool
}

type Result struct {
	Decision Decision `json:"decision"`
	Reason   string   `json:"reason"`
}

// Evaluate applies the v0.1 role defaults without considering a human authorization.
func Evaluate(request Request) (Result, error) {
	if !request.Role.Valid() || !request.Action.Valid() {
		return Result{}, errors.New("valid role and action are required")
	}
	switch request.Action {
	case domain.ActionReadFile:
		return Result{Decision: Allow, Reason: "project reads are allowed for all roles"}, nil
	case domain.ActionWriteFile:
		if request.Role != domain.RoleImplementer {
			return Result{Decision: Deny, Reason: "only implementers may write the business workspace"}, nil
		}
		if withinScope(request.Resources, request.WriteScope) {
			return Result{Decision: Allow, Reason: "implementer writes are within frozen scope"}, nil
		}
		return Result{Decision: RequireGate, Reason: "write expands frozen scope"}, nil
	case domain.ActionExecCommand:
		if request.Role == domain.RoleReviewer {
			return Result{Decision: Deny, Reason: "reviewers do not execute project commands"}, nil
		}
		return Result{Decision: Allow, Reason: "local command remains subject to environment policy"}, nil
	case domain.ActionConnectProvider:
		if request.TrustedProfile {
			return Result{Decision: Allow, Reason: "trusted profile may use provider transport"}, nil
		}
		return Result{Decision: Deny, Reason: "provider transport requires a trusted profile"}, nil
	case domain.ActionUseProviderCredential:
		if request.TrustedProfile && request.CLISession {
			return Result{Decision: Allow, Reason: "CLI-owned session credential is not exposed to the agent"}, nil
		}
		return Result{Decision: RequireGate, Reason: "explicit provider credential injection requires finite authorization"}, nil
	case domain.ActionAccessProjectNetwork, domain.ActionUseProjectSecret, domain.ActionModifyValidator, domain.ActionPublishArtifact, domain.ActionExpandScope:
		return Result{Decision: RequireGate, Reason: "high-risk action requires finite authorization"}, nil
	case domain.ActionReadEnv:
		return Result{Decision: RequireGate, Reason: "environment reads require an explicit variable allowlist"}, nil
	case domain.ActionModifyGitHistory, domain.ActionPushRemote, domain.ActionDeployProduction, domain.ActionDeleteExternalData:
		return Result{Decision: Deny, Reason: "v0.1 does not authorize destructive or external write actions"}, nil
	default:
		return Result{}, errors.New("unhandled policy action")
	}
}

// ScopeContains reports whether every requested resource is equal to or nested under an authorized resource.
func ScopeContains(authorized, requested []string) bool {
	return withinScope(requested, authorized)
}

func withinScope(requested, authorized []string) bool {
	if len(requested) == 0 || len(authorized) == 0 {
		return false
	}
	clean := append([]string(nil), authorized...)
	sort.Strings(clean)
	for _, item := range requested {
		item = filepath.Clean(strings.TrimSpace(item))
		if item == "." || item == "" {
			return false
		}
		matched := false
		for _, root := range clean {
			root = filepath.Clean(strings.TrimSpace(root))
			relative, err := filepath.Rel(root, item)
			if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}
