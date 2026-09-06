package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/goalcompile"
	"github.com/monshunter/xgoal/internal/planner"
	"github.com/monshunter/xgoal/internal/protocol"
)

// Fixed bytes and hashes were produced by an independent build of 8059cfc,
// before acceptance fields existed. They must not be regenerated from this code.
func TestLegacyAcceptanceFieldsPreserveCanonicalIdentity(t *testing.T) {
	data, err := os.ReadFile("testdata/legacy-pre-acceptance.json")
	if err != nil {
		t.Fatal(err)
	}
	var goldens []struct {
		Name        string          `json:"name"`
		Kind        string          `json:"kind"`
		Schema      string          `json:"schema"`
		Hash        string          `json:"hash"`
		BytesSHA256 string          `json:"bytes_sha256"`
		Bytes       json.RawMessage `json:"bytes"`
	}
	if err := json.Unmarshal(data, &goldens); err != nil {
		t.Fatal(err)
	}
	if len(goldens) != 7 {
		t.Fatal("legacy samples were removed")
	}
	for _, g := range goldens {
		t.Run(g.Name, func(t *testing.T) {
			var value any
			switch g.Name {
			case "config":
				cfg, err := config.LoadFile("../../xgoal.example.yaml")
				if err != nil {
					t.Fatal(err)
				}
				value = cfg
			case "planning_request":
				value = &planner.Request{}
			case "goal_contract":
				value = &goalcompile.Contract{}
			case "goal_envelope":
				value = &struct {
					ProtocolVersion string               `json:"protocol_version"`
					Contract        goalcompile.Contract `json:"contract"`
					ConfigHash      string               `json:"config_hash"`
					CreatedBy       string               `json:"created_by"`
					Mode            string               `json:"mode"`
				}{}
			case "planner_packet":
				value = &planner.Packet{}
			case "work_packet":
				value = &protocol.WorkPacket{}
			case "review_packet":
				value = &protocol.ReviewPacket{}
			default:
				t.Fatalf("unknown legacy sample %s", g.Name)
			}
			if g.Name != "config" {
				if err := json.Unmarshal(g.Bytes, value); err != nil {
					t.Fatal(err)
				}
			}
			actual, err := canonical.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(actual)
			if hex.EncodeToString(sum[:]) != g.BytesSHA256 {
				t.Fatalf("legacy canonical bytes changed: %s", actual)
			}
			hash, err := canonical.Hash(g.Kind, g.Schema, value)
			if err != nil || hash != g.Hash {
				t.Fatalf("legacy identity changed: %s %v", hash, err)
			}
		})
	}
}
