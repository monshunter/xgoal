package exporter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/invocation"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func exportFixture(t *testing.T) (*sqlite.Store, string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := sqlite.Open(context.Background(), filepath.Join(base, "state"), clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	for _, id := range []string{"goal_one", "goal_two"} {
		if err := s.CreateGoal(context.Background(), domain.Goal{ID: id, State: domain.GoalDraft, Version: 1}, sqlite.EventInput{Type: "GoalCreated", ActorType: "kernel", Payload: map[string]any{}}); err != nil {
			t.Fatal(err)
		}
	}
	return s, base
}
func exportWrite(t *testing.T, root, path string, data []byte) {
	t.Helper()
	p := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func exportInvocation(t *testing.T, s *sqlite.Store, id, goal string) invocation.Input {
	t.Helper()
	packet := []byte(`{"input":"fixture"}`)
	dir, _ := invocation.Directory("codex-cli", "implementer", id)
	in := invocation.Input{ID: id, GoalID: goal, OwnerKind: "attempt", OwnerID: "attempt_" + id, Generation: 1, Role: "implementer", ProfileID: "implementer", Provider: "codex-cli", GoalRevisionHash: strings.Repeat("a", 64), InputTree: strings.Repeat("b", 40), PacketPath: "fixture/" + id + ".json", PacketHash: strings.Repeat("c", 64), PacketSHA256: invocation.SHA256(packet), ProviderDir: dir, SchemaSHA256: strings.Repeat("d", 64), DelegationHash: protocolDelegation(), ExecutionConfig: config.ExecutionConfig{ProfileID: "implementer", Provider: "codex-cli", Role: "implementer"}}
	exportWrite(t, s.Info().ProjectDir, in.PacketPath, packet)
	if _, err := s.RegisterInvocation(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	return in
}
func protocolDelegation() string { return strings.Repeat("e", 64) }

func TestExportFreezesAllGoalsAndPublicLogBoundaries(t *testing.T) {
	s, base := exportFixture(t)
	ctx := context.Background()
	for _, pair := range [][2]string{{"call_one", "goal_one"}, {"call_two", "goal_two"}} {
		in := exportInvocation(t, s, pair[0], pair[1])
		exportWrite(t, s.Info().ProjectDir, filepath.Join(in.ProviderDir, "events/000001.json"), []byte(`{"type":"item.completed","item":{"type":"reasoning","text":"PRIVATE THOUGHT"}}`))
		if _, err := s.RefreshInvocation(ctx, in.ID); err != nil {
			t.Fatal(err)
		}
		// This newer file is durable but outside the frozen database cursor.
		exportWrite(t, s.Info().ProjectDir, filepath.Join(in.ProviderDir, "events/000002.json"), []byte(`{"type":"item.completed","item":{"type":"agent_message","text":"newer"}}`))
	}
	destination := filepath.Join(base, "export")
	r, err := Export(ctx, s, filepath.Join(base, "project"), "goal_one", destination)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "complete" {
		t.Fatal(r)
	}
	data, err := os.ReadFile(filepath.Join(destination, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if invocation.SHA256(data) != r.ManifestSHA256 {
		t.Fatal("manifest hash")
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Logs) != 2 || m.Boundary.EventCount != 2 {
		t.Fatalf("project scope omitted records: %+v", m)
	}
	for _, f := range m.Files {
		data, err := os.ReadFile(filepath.Join(destination, f.Path))
		if err != nil || invocation.SHA256(data) != f.SHA256 || int64(len(data)) != f.Size {
			t.Fatalf("file mismatch %+v %v", f, err)
		}
		if strings.Contains(string(data), "PRIVATE THOUGHT") {
			t.Fatal("private reasoning exported")
		}
		if strings.HasSuffix(f.Path, "000002.json") {
			t.Fatal("log past frozen cursor exported")
		}
	}
	if err := s.UpdateGoalState(ctx, "goal_two", 1, domain.GoalCancelled, sqlite.EventInput{Type: "GoalCancelled", ActorType: "human", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(destination, "snapshot/state.db")}).String() + "?mode=ro&immutable=1"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state string
	if err := db.QueryRow("SELECT state FROM goals WHERE id='goal_two'").Scan(&state); err != nil || state != "DRAFT" {
		t.Fatalf("snapshot moved: %s %v", state, err)
	}
	if _, err := Export(ctx, s, filepath.Join(base, "project"), "goal_one", destination); err == nil {
		t.Fatal("existing export overwritten")
	}
}

func TestExportFailsClosedForMissingOrLinkedArtifactAndPreservesUserFiles(t *testing.T) {
	for _, fault := range []string{"missing", "symlink", "changed"} {
		t.Run(fault, func(t *testing.T) {
			s, base := exportFixture(t)
			in := exportInvocation(t, s, "call_bad", "goal_two")
			path := filepath.Join(s.Info().ProjectDir, in.PacketPath)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			secret := filepath.Join(base, "private")
			if err := os.WriteFile(secret, []byte("host-private"), 0600); err != nil {
				t.Fatal(err)
			}
			if fault == "symlink" {
				if err := os.Symlink(secret, path); err != nil {
					t.Fatal(err)
				}
			} else if fault == "changed" {
				if err := os.WriteFile(path, []byte(`{"changed":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			destination := filepath.Join(base, "export")
			_, err := Export(context.Background(), s, filepath.Join(base, "project"), "goal_one", destination)
			var incomplete *IncompleteError
			if !errors.As(err, &incomplete) {
				t.Fatalf("not incomplete: %v", err)
			}
			if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("incomplete export published")
			}
			if _, err := os.Stat(filepath.Join(incomplete.Path, "manifest.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("complete manifest published for missing artifact")
			}
			data, _ := os.ReadFile(secret)
			if string(data) != "host-private" {
				t.Fatal("source changed")
			}
		})
	}
}
