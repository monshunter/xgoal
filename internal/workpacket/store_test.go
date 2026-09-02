package workpacket_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/workpacket"
)

func TestStorePublishesImmutableCanonicalPacketAndRejectsTampering(t *testing.T) {
	t.Parallel()

	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	store, err := workpacket.NewStore(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	packet := protocol.WorkPacket{
		ProtocolVersion: protocol.WorkPacketVersion,
		Project:         protocol.PacketProject{Name: "fixture", BaseTree: strings.Repeat("a", 40), Workspace: filepath.Join(runtimeRoot, "workspace")},
		Goal:            protocol.PacketGoal{ID: "goal_1", Revision: 1, Summary: "goal", ContractHash: strings.Repeat("b", 64)},
		WorkItem: protocol.PacketWorkItem{
			ID: "work_1", Title: "work", Objective: "test packet store", ReadScope: []string{"/**"}, WriteScope: []string{"/**"},
			AcceptanceCriteria: []string{"AC-1"}, ValidatorIDs: []string{"validator"},
		},
		Role:                 domain.RoleImplementer,
		Constraints:          protocol.PacketConstraints{ProjectNetwork: "deny", ProjectSecrets: "deny"},
		RequiredOutputSchema: protocol.AgentResultVersion,
	}
	artifact, created, err := store.Save("attempt_1", packet)
	if err != nil || !created || artifact.Hash == "" {
		t.Fatalf("Save() = %+v, %v, %v", artifact, created, err)
	}
	if repeated, created, err := store.Save("attempt_1", packet); err != nil || created || repeated.Hash != artifact.Hash {
		t.Fatalf("idempotent Save() = %+v, %v, %v", repeated, created, err)
	}
	if loaded, err := store.Load("attempt_1"); err != nil || loaded.Hash != artifact.Hash {
		t.Fatalf("Load() = %+v, %v", loaded, err)
	}
	if info, err := os.Stat(artifact.Path); err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("packet permissions = %v, %v", info, err)
	}
	if err := os.Chmod(artifact.Path, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("attempt_1"); err == nil {
		t.Fatal("Load() accepted writable packet")
	}
}
