package policy_test

import (
	"testing"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/policy"
)

func TestProviderTransportDoesNotAuthorizeProjectNetwork(t *testing.T) {
	provider, err := policy.Evaluate(policy.Request{Role: domain.RoleImplementer, Action: domain.ActionConnectProvider, TrustedProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	network, err := policy.Evaluate(policy.Request{Role: domain.RoleImplementer, Action: domain.ActionAccessProjectNetwork, TrustedProfile: true})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Decision != policy.Allow || network.Decision != policy.RequireGate {
		t.Fatalf("provider=%+v network=%+v", provider, network)
	}
}

func TestRoleAndScopeDefaults(t *testing.T) {
	reviewer, _ := policy.Evaluate(policy.Request{Role: domain.RoleReviewer, Action: domain.ActionWriteFile, Resources: []string{"internal/a.go"}, WriteScope: []string{"internal"}})
	if reviewer.Decision != policy.Deny {
		t.Fatalf("reviewer write = %+v", reviewer)
	}
	inside, _ := policy.Evaluate(policy.Request{Role: domain.RoleImplementer, Action: domain.ActionWriteFile, Resources: []string{"internal/a.go"}, WriteScope: []string{"internal"}})
	outside, _ := policy.Evaluate(policy.Request{Role: domain.RoleImplementer, Action: domain.ActionWriteFile, Resources: []string{"docs/a.md"}, WriteScope: []string{"internal"}})
	if inside.Decision != policy.Allow || outside.Decision != policy.RequireGate {
		t.Fatalf("inside=%+v outside=%+v", inside, outside)
	}
}

func TestExplicitCredentialNeedsGateButCLISessionDoesNot(t *testing.T) {
	cli, _ := policy.Evaluate(policy.Request{Role: domain.RolePlanner, Action: domain.ActionUseProviderCredential, TrustedProfile: true, CLISession: true})
	explicit, _ := policy.Evaluate(policy.Request{Role: domain.RolePlanner, Action: domain.ActionUseProviderCredential, TrustedProfile: true})
	if cli.Decision != policy.Allow || explicit.Decision != policy.RequireGate {
		t.Fatalf("cli=%+v explicit=%+v", cli, explicit)
	}
}
