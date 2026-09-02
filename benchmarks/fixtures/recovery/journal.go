package recovery

type Journal struct {
	entries []string
}

func (journal *Journal) Apply(effectID string) {
	journal.entries = append(journal.entries, effectID)
}

func (journal *Journal) Entries() []string {
	return append([]string(nil), journal.entries...)
}
