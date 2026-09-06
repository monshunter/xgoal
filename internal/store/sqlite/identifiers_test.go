package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/invocation"
	basestore "github.com/monshunter/xgoal/internal/store"
)

func TestIdentifierKindsLiteralPrefixAndBoundedCandidates(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "state"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	goal, work := seedReadyWork(t, s, "work_identity")
	gate, err := s.CreateGate(ctx, GateDraft{ID: "gate_identity", GoalID: goal.ID, WorkItemID: work.ID, ReasonCode: "SCOPE", Facts: []any{}, Unknowns: []any{}, Options: []any{"deny"}, Recommendation: "deny", Action: domain.ActionExpandScope, Scope: []string{"docs"}, ExpiresAt: time.Now().Add(time.Hour), MaxUses: 1, Revocable: true}, EventInput{Type: "GateOpened", ActorType: "kernel", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	in := invocation.Input{ID: "invocation_identity", GoalID: goal.ID, OwnerKind: "attempt", OwnerID: "attempt", Generation: 1, Role: "implementer", ProfileID: "p", Provider: "codex-cli", GoalRevisionHash: "revision", InputTree: "tree", PacketPath: "packet.json", PacketHash: "packet", PacketSHA256: strings.Repeat("a", 64), SchemaSHA256: strings.Repeat("b", 64), DelegationHash: "delegation", ProviderDir: "adapters/codex/invocations/invocation_identity", ExecutionConfig: config.ExecutionConfig{ProfileID: "p", Provider: "codex-cli", Role: "implementer"}}
	if _, err := s.RegisterInvocation(ctx, in); err != nil {
		t.Fatal(err)
	}
	for kind, id := range map[string]string{"goal": goal.ID, "work": work.ID, "gate": gate.ID, "invocation": in.ID} {
		item, err := s.ResolveIdentifier(ctx, kind, id[:len(id)-3])
		if err != nil || item.ID != id || item.Kind != kind || item.Version <= 0 || item.GoalID != goal.ID {
			t.Fatalf("%s lookup=%+v err=%v", kind, item, err)
		}
	}
	for _, id := range []string{"literal_a", "literal_abc", "literalX", "literal%one"} {
		if err := s.CreateGoal(ctx, domain.Goal{ID: id, State: domain.GoalDraft, Version: 1}, EventInput{Type: "GoalCreated", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.FindIdentifiers(ctx, "goal", "literal_", 1)
	if err != nil || len(page.Items) != 1 || !page.More || page.Items[0].ID != "literal_a" {
		t.Fatalf("page=%+v %v", page, err)
	}
	page, err = s.FindIdentifiers(ctx, "goal", "literal%", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "literal%one" {
		t.Fatalf("literal wildcard=%+v %v", page, err)
	}
	if item, err := s.ResolveIdentifier(ctx, "goal", "literal_a"); err != nil || item.ID != "literal_a" {
		t.Fatalf("exact=%+v %v", item, err)
	}
	var ambiguous *AmbiguousIdentifier
	if _, err := s.ResolveIdentifier(ctx, "goal", "literal"); !errors.As(err, &ambiguous) || len(ambiguous.Candidates) != 4 {
		t.Fatalf("ambiguity=%v", err)
	}
	if _, err := s.ResolveIdentifier(ctx, "goal", "missing"); !errors.Is(err, basestore.ErrNotFound) {
		t.Fatalf("missing=%v", err)
	}
	if _, err := s.FindIdentifiers(ctx, "goals; DROP TABLE goals", "", 10); err == nil {
		t.Fatal("invalid kind accepted")
	}
}
