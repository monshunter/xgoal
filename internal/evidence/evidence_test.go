package evidence_test

import (
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/evidence"
	"github.com/monshunter/xgoal/internal/protocol"
)

func TestBindingStalenessSetIdentityAndAuthorityOrder(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 2, 13, 0, 0, 0, time.UTC)
	record := evidence.Record{
		Evidence: protocol.Evidence{
			ProtocolVersion: protocol.EvidenceVersion, ID: "evidence_1", Kind: "command",
			SubjectID: "work_1", Producer: "validator_1", Authority: domain.AuthorityDeterministic,
			GoalRevisionHash: repeat('a', 64), ConfigHash: repeat('b', 64), TreeHash: repeat('c', 40),
			PayloadHash: repeat('d', 64), State: domain.EvidenceCurrent, CreatedAt: now,
		},
		DefinitionHash: repeat('e', 64), EnvironmentHash: repeat('f', 64), ReceiptHash: repeat('1', 64),
	}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	binding := evidence.BindingOf(record)
	snapshot := evidence.Snapshot{Record: record, State: domain.EvidenceCurrent}
	if !evidence.IsCurrent(snapshot, binding) || evidence.StalenessReason(snapshot, binding) != "" {
		t.Fatal("exact five-part binding was not current")
	}
	changed := binding
	changed.EnvironmentHash = repeat('2', 64)
	if evidence.IsCurrent(snapshot, changed) || evidence.StalenessReason(snapshot, changed) != "environment changed" {
		t.Fatalf("environment staleness = %q", evidence.StalenessReason(snapshot, changed))
	}
	setA, err := evidence.NewSet("set_1", evidence.SetFinal, binding.GoalRevisionHash, binding.ConfigHash, binding.TreeHash, []string{"evidence_2", "evidence_1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	setB, err := evidence.NewSet("set_1", evidence.SetFinal, binding.GoalRevisionHash, binding.ConfigHash, binding.TreeHash, []string{"evidence_1", "evidence_2"}, now)
	if err != nil || setA.Hash != setB.Hash || setA.EvidenceIDs[0] != "evidence_1" {
		t.Fatalf("canonical evidence sets = %+v / %+v, %v", setA, setB, err)
	}
	if evidence.CanOverride(domain.AuthorityDecision, domain.AuthorityDeterministic) {
		t.Fatal("deterministic evidence overrode a decision")
	}
	if !evidence.CanOverride(domain.AuthorityClaim, domain.AuthorityFact) {
		t.Fatal("fact could not override a claim")
	}
}

func repeat(value byte, count int) string {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return string(result)
}
