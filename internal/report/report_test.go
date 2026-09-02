package report

import (
	"bytes"
	"testing"
)

func TestRenderIsDeterministicAndPreservesExecutionMetrics(t *testing.T) {
	report := validReport()
	report.Criteria = []CriterionTrace{report.Criteria[1], report.Criteria[0]}
	report.Execution = []ExecutionMetric{
		{Name: "wall_time", Unit: "millisecond", Known: true, Value: 1250, Authority: "FACT"},
		{Name: "human_gates", Unit: "gate", Known: true, Value: 1, Authority: "FACT"},
	}

	first, err := Render(report)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Render(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.JSON, second.JSON) || !bytes.Equal(first.Markdown, second.Markdown) || first.ReportHash != second.ReportHash {
		t.Fatal("rendering is not deterministic")
	}
	if !bytes.Contains(first.Markdown, []byte("| wall_time | 1250 | millisecond | FACT |")) {
		t.Fatalf("markdown does not preserve execution metric:\n%s", first.Markdown)
	}
	if !bytes.Contains(first.Markdown, []byte("flaky=true")) {
		t.Fatalf("markdown does not disclose flaky validator policy:\n%s", first.Markdown)
	}
	if bytes.Contains(first.JSON, []byte("token")) || bytes.Contains(first.JSON, []byte("cost")) {
		t.Fatalf("json retained provider accounting: %s", first.JSON)
	}
}

func TestRenderBindsEveryArtifactIdentityInput(t *testing.T) {
	base, err := Render(validReport())
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*Report){
		func(value *Report) { value.Goal.RevisionHash = hash("7") },
		func(value *Report) { value.Goal.ConfigHash = hash("8") },
		func(value *Report) { value.Final.Tree = hash("9") },
		func(value *Report) { value.Final.EvidenceSetID = "evidence-final-2" },
		func(value *Report) { value.Criteria[0].EvidenceIDs = []string{"evidence-99"} },
	}
	for index, mutate := range mutations {
		candidate := validReport()
		mutate(&candidate)
		artifact, renderErr := Render(candidate)
		if renderErr != nil {
			t.Fatalf("mutation %d: %v", index, renderErr)
		}
		if artifact.ReportHash == base.ReportHash {
			t.Fatalf("mutation %d did not change report hash", index)
		}
	}
}

func TestRenderRejectsInvalidOrAmbiguousContent(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Report)
	}{
		{"missing evidence", func(value *Report) { value.Criteria[0].EvidenceIDs = nil }},
		{"duplicate criterion", func(value *Report) { value.Criteria[1].ID = value.Criteria[0].ID }},
		{"unknown with value", func(value *Report) {
			value.Execution = []ExecutionMetric{{Name: "wall_time", Unit: "millisecond", Known: false, Value: 1, Authority: "FACT"}}
		}},
		{"invalid authority", func(value *Report) { value.Goal.Authority = "CLAIMED_FACT" }},
		{"successful attempt without result tree", func(value *Report) { value.Attempts[0].ResultTree = "" }},
		{"non-terminal attempt", func(value *Report) { value.Attempts[0].State = "RUNNING" }},
		{"invalid time", func(value *Report) { value.Timestamps.CompletedAt = "yesterday" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validReport()
			test.mutate(&candidate)
			if _, err := Render(candidate); err == nil {
				t.Fatal("invalid report unexpectedly rendered")
			}
		})
	}
}

func TestRenderPreservesFailedAttemptWithoutResultTree(t *testing.T) {
	report := validReport()
	report.Attempts = append(report.Attempts, AttemptTrace{
		ID: "attempt-2", WorkID: "work-1", Role: "implementer", Provider: "claude",
		State: "FAILED", PacketHash: hash("7"), Authority: "FACT",
	})
	artifact, err := Render(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(artifact.JSON, []byte(`"id":"attempt-2"`)) || !bytes.Contains(artifact.Markdown, []byte("attempt-2")) {
		t.Fatalf("failed attempt is absent from report history: %s", artifact.JSON)
	}
}

func validReport() Report {
	return Report{
		ProtocolVersion: ProtocolVersion,
		Goal:            GoalTrace{ID: "goal-1", Raw: "fix the defect", Revision: 1, RevisionHash: hash("1"), ConfigHash: hash("2"), CreatedBy: "human", Authority: "DECISION"},
		Work:            []WorkTrace{{ID: "work-1", State: "COMPLETED", Title: "fix", Required: true, Authority: "FACT"}},
		Attempts:        []AttemptTrace{{ID: "attempt-1", WorkID: "work-1", Role: "implementer", Provider: "codex", State: "SUCCEEDED", PacketHash: hash("3"), ResultTree: hash("4"), Authority: "FACT"}},
		Final:           FinalTrace{Commit: hash40("5"), Tree: hash("4"), EvidenceSetID: "evidence-final-1", Scope: []string{"internal/report/**"}, Decisions: []Statement{{Text: "canonical report", Authority: "DECISION"}}, Authority: "FACT"},
		Criteria: []CriterionTrace{
			{ID: "AC-2", Description: "second", Status: "PASS", EvidenceIDs: []string{"evidence-2"}, ValidatorIDs: []string{"validator-2"}, Authority: "DETERMINISTIC"},
			{ID: "AC-1", Description: "first", Status: "PASS", EvidenceIDs: []string{"evidence-1"}, ValidatorIDs: []string{"validator-1"}, Authority: "DETERMINISTIC"},
		},
		Validators:  []ValidatorTrace{{ID: "validator-1", Command: []string{"go", "test", "./..."}, ReceiptHash: hash("6"), Result: "PASSED", Reproduction: []string{"go", "test", "./..."}, Flaky: true, Authority: "DETERMINISTIC"}},
		Execution:   []ExecutionMetric{{Name: "wall_time", Unit: "millisecond", Known: true, Value: 1250, Authority: "FACT"}},
		Limitations: []Statement{{Text: "L0 isolation", Authority: "FACT"}},
		Timestamps:  TimestampTrace{StartedAt: "2026-09-02T00:00:00Z", CompletedAt: "2026-09-02T00:01:00Z", Authority: "FACT"},
	}
}

func hash(character string) string   { return repeat(character, 64) }
func hash40(character string) string { return repeat(character, 40) }

func repeat(value string, count int) string {
	result := ""
	for range count {
		result += value
	}
	return result
}
