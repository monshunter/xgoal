package evidence

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
)

type Binding struct {
	GoalRevisionHash string
	ConfigHash       string
	DefinitionHash   string
	EnvironmentHash  string
	TreeHash         string
}

type Record struct {
	Evidence        protocol.Evidence
	DefinitionHash  string
	EnvironmentHash string
	ReceiptHash     string
}

type Snapshot struct {
	Record
	State          domain.EvidenceState
	StateSequence  int64
	StateReason    string
	StateChangedAt time.Time
}

type SetPhase string

const (
	SetChange SetPhase = "CHANGE"
	SetFinal  SetPhase = "FINAL"
)

type Set struct {
	ID               string
	Phase            SetPhase
	GoalRevisionHash string
	ConfigHash       string
	TreeHash         string
	EvidenceIDs      []string
	Hash             string
	CreatedAt        time.Time
}

type setIdentity struct {
	ID               string   `json:"id"`
	Phase            SetPhase `json:"phase"`
	GoalRevisionHash string   `json:"goal_revision_hash"`
	ConfigHash       string   `json:"config_hash"`
	TreeHash         string   `json:"tree_hash"`
	EvidenceIDs      []string `json:"evidence_ids"`
}

func (binding Binding) Validate() error {
	for _, hash := range []string{binding.GoalRevisionHash, binding.ConfigHash, binding.DefinitionHash, binding.EnvironmentHash} {
		if !validHex(hash, 64) {
			return errors.New("evidence binding hashes must be lowercase SHA-256")
		}
	}
	if !validHex(binding.TreeHash, 40) && !validHex(binding.TreeHash, 64) {
		return errors.New("evidence tree binding must be a lowercase Git object id")
	}
	return nil
}

func (record Record) Validate() error {
	if err := record.Evidence.Validate(); err != nil {
		return err
	}
	binding := Binding{
		GoalRevisionHash: record.Evidence.GoalRevisionHash,
		ConfigHash:       record.Evidence.ConfigHash,
		DefinitionHash:   record.DefinitionHash,
		EnvironmentHash:  record.EnvironmentHash,
		TreeHash:         record.Evidence.TreeHash,
	}
	if err := binding.Validate(); err != nil {
		return err
	}
	if !validHex(record.Evidence.PayloadHash, 64) || (record.ReceiptHash != "" && !validHex(record.ReceiptHash, 64)) {
		return errors.New("evidence payload and receipt hashes must be lowercase SHA-256")
	}
	return nil
}

func BindingOf(record Record) Binding {
	return Binding{
		GoalRevisionHash: record.Evidence.GoalRevisionHash,
		ConfigHash:       record.Evidence.ConfigHash,
		DefinitionHash:   record.DefinitionHash,
		EnvironmentHash:  record.EnvironmentHash,
		TreeHash:         record.Evidence.TreeHash,
	}
}

func IsCurrent(snapshot Snapshot, expected Binding) bool {
	return snapshot.State == domain.EvidenceCurrent && BindingOf(snapshot.Record) == expected
}

func StalenessReason(snapshot Snapshot, expected Binding) string {
	if snapshot.State != domain.EvidenceCurrent {
		return "evidence state is " + string(snapshot.State)
	}
	actual := BindingOf(snapshot.Record)
	switch {
	case actual.GoalRevisionHash != expected.GoalRevisionHash:
		return "goal revision changed"
	case actual.ConfigHash != expected.ConfigHash:
		return "config changed"
	case actual.DefinitionHash != expected.DefinitionHash:
		return "validator definition changed"
	case actual.EnvironmentHash != expected.EnvironmentHash:
		return "environment changed"
	case actual.TreeHash != expected.TreeHash:
		return "subject tree changed"
	default:
		return ""
	}
}

func NewSet(id string, phase SetPhase, goalRevisionHash, configHash, treeHash string, evidenceIDs []string, createdAt time.Time) (Set, error) {
	if id == "" || (phase != SetChange && phase != SetFinal) || !validHex(goalRevisionHash, 64) || !validHex(configHash, 64) ||
		(!validHex(treeHash, 40) && !validHex(treeHash, 64)) || createdAt.IsZero() || len(evidenceIDs) == 0 {
		return Set{}, errors.New("invalid evidence set")
	}
	ids := append([]string(nil), evidenceIDs...)
	sort.Strings(ids)
	for index, evidenceID := range ids {
		if evidenceID == "" || (index > 0 && ids[index-1] == evidenceID) {
			return Set{}, errors.New("evidence set ids must be non-empty and unique")
		}
	}
	set := Set{
		ID: id, Phase: phase, GoalRevisionHash: goalRevisionHash, ConfigHash: configHash,
		TreeHash: treeHash, EvidenceIDs: ids, CreatedAt: createdAt.UTC(),
	}
	hash, err := canonical.Hash("evidence-set", "xgoal.evidence-set/v1alpha1", setIdentity{
		ID: set.ID, Phase: set.Phase, GoalRevisionHash: set.GoalRevisionHash,
		ConfigHash: set.ConfigHash, TreeHash: set.TreeHash, EvidenceIDs: set.EvidenceIDs,
	})
	if err != nil {
		return Set{}, fmt.Errorf("hash evidence set: %w", err)
	}
	set.Hash = hash
	return set, nil
}

func AuthorityRank(authority domain.Authority) int {
	switch authority {
	case domain.AuthorityDecision:
		return 5
	case domain.AuthorityDeterministic:
		return 4
	case domain.AuthorityFact:
		return 3
	case domain.AuthorityInference:
		return 2
	case domain.AuthorityClaim:
		return 1
	default:
		return 0
	}
}

func CanOverride(existing, incoming domain.Authority) bool {
	return existing.Valid() && incoming.Valid() && AuthorityRank(incoming) >= AuthorityRank(existing)
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
