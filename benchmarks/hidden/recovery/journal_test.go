package recovery

import "testing"

func TestRecoveryIsIdempotent(t *testing.T) {
	var journal Journal
	journal.Apply("effect-1")
	journal.Apply("effect-1")
	entries := journal.Entries()
	if len(entries) != 1 || entries[0] != "effect-1" {
		t.Fatalf("entries = %v", entries)
	}
}
