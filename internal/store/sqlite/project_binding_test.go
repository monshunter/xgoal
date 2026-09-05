package sqlite

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/domain"
)

func bindingFixture(t *testing.T) (string, ProjectBinding) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, ".xgoal"), ProjectBinding{ProjectID: "project_test", CommonDir: filepath.Join(root, ".git"), ProjectRoot: root}
}

func TestProjectBindingPreflightIsReadOnly(t *testing.T) {
	state, binding := bindingFixture(t)
	if err := CheckProjectBinding(context.Background(), state, binding, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preflight created state: %v", err)
	}
	if err := CheckProjectBinding(context.Background(), "relative", binding, true); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("relative path: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CheckProjectBinding(ctx, state, binding, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled preflight: %v", err)
	}
}

func TestProjectBindingRejectsHardLinkedDatabase(t *testing.T) {
	ctx := context.Background()
	state, binding := bindingFixture(t)
	store, err := OpenProject(ctx, state, clock.Real{}, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(state, "state.db"), filepath.Join(binding.ProjectRoot, "alias.db")); err != nil {
		t.Fatal(err)
	}
	if err := CheckProjectBinding(ctx, state, binding, true); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("hard linked database accepted: %v", err)
	}
}

func TestProjectBindingSurvivesReopenAndRejectsAnotherProjectBeforeWriting(t *testing.T) {
	ctx := context.Background()
	state, binding := bindingFixture(t)
	s, err := OpenProject(ctx, state, clock.Real{}, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGoal(ctx, domain.Goal{ID: "retained", State: domain.GoalDraft, Version: 1}, EventInput{Type: "GoalCreated", ActorType: "human", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	wrong := binding
	wrong.ProjectID = "project_other"
	if _, err := OpenProject(ctx, state, clock.Real{}, wrong, true); !errors.Is(err, ErrProjectBinding) {
		t.Fatalf("wrong project accepted: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(state, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("rejected open changed database")
	}
	s, err = OpenProject(ctx, state, clock.Real{}, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Goal(ctx, "retained"); err != nil {
		t.Fatal(err)
	}
}

func TestProjectBindingAdoptsAndMigratesDefaultLegacyState(t *testing.T) {
	ctx := context.Background()
	state, binding := bindingFixture(t)
	migrations, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	s, err := openWithMigrations(ctx, state, clock.Real{}, migrations[:1])
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateGoal(ctx, domain.Goal{ID: "legacy", State: domain.GoalDraft, Version: 1}, EventInput{Type: "GoalCreated", ActorType: "human", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenProject(ctx, state, clock.Real{}, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Goal(ctx, "legacy"); err != nil {
		t.Fatal(err)
	}
	if s.Info().SchemaVersion != len(migrations) {
		t.Fatalf("schema %d", s.Info().SchemaVersion)
	}
	backups, err := os.ReadDir(filepath.Join(state, "backups"))
	if err != nil || len(backups) == 0 {
		t.Fatalf("migration backup missing: %v", err)
	}
	var metadata string
	if err := s.db.QueryRowContext(ctx, `SELECT value FROM store_metadata WHERE key = 'project_binding'`).Scan(&metadata); err != nil || !strings.Contains(metadata, binding.ProjectID) {
		t.Fatalf("binding not persisted: %q, %v", metadata, err)
	}
}

func TestProjectBindingRejectsExternalUnboundLegacyState(t *testing.T) {
	ctx := context.Background()
	_, binding := bindingFixture(t)
	state := filepath.Join(binding.ProjectRoot, "external")
	s, err := Open(ctx, state, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	for _, allow := range []bool{false, true} {
		if err := CheckProjectBinding(ctx, state, binding, allow); !errors.Is(err, ErrProjectBinding) {
			t.Fatalf("external legacy accepted (allow=%v): %v", allow, err)
		}
	}
}

func TestProjectBindingRejectsConflictingLegacyArtifacts(t *testing.T) {
	t.Run("planner packet", func(t *testing.T) {
		ctx := context.Background()
		state, binding := bindingFixture(t)
		s, err := Open(ctx, state, clock.Real{})
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
		path := filepath.Join(state, "planner", "old", "packet.json")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"project_root":"/other/project"}`), 0400); err != nil {
			t.Fatal(err)
		}
		if err := CheckProjectBinding(ctx, state, binding, true); !errors.Is(err, ErrProjectBinding) {
			t.Fatalf("foreign packet accepted: %v", err)
		}
	})
	t.Run("workspace", func(t *testing.T) {
		ctx := context.Background()
		state, binding := bindingFixture(t)
		s, err := Open(ctx, state, clock.Real{})
		if err != nil {
			t.Fatal(err)
		}
		_, work := seedReadyWork(t, s, "work_binding")
		_, err = s.ClaimWork(ctx, work.ID, work.Version, LeaseDraft{ID: "binding_lease", Holder: "test", TTL: time.Hour}, domain.Attempt{ID: "binding_attempt", WorkItemID: work.ID, AgentProfileID: "fake", State: domain.AttemptCreated, BaseTree: strings.Repeat("1", 40), PacketHash: strings.Repeat("2", 64), Version: 1}, EventInput{Type: "Claimed", ActorType: "kernel", Payload: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.db.ExecContext(ctx, `INSERT INTO workspaces(id,attempt_id,kind,path,common_dir,base_commit,base_tree,config_hash,marker_hash,state,version,created_at,updated_at) VALUES ('w','binding_attempt','ATTEMPT',?,'/other/.git',?,?,?,?, 'ACTIVE',1,'now','now')`, filepath.Join(state, "workspaces", "w"), strings.Repeat("1", 40), strings.Repeat("1", 40), strings.Repeat("2", 64), strings.Repeat("3", 64))
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
		if err := CheckProjectBinding(ctx, state, binding, true); !errors.Is(err, ErrProjectBinding) {
			t.Fatalf("foreign workspace accepted: %v", err)
		}
	})
}

func TestProjectBindingRejectsMalformedMetadataAndLinkedDatabase(t *testing.T) {
	ctx := context.Background()
	state, binding := bindingFixture(t)
	s, err := OpenProject(ctx, state, clock.Real{}, binding, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE store_metadata SET value = ? WHERE key = 'project_binding'`, []byte(`{"project_id":"project_test"} {}`)); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := CheckProjectBinding(ctx, state, binding, true); !errors.Is(err, ErrProjectBinding) {
		t.Fatalf("malformed binding accepted: %v", err)
	}
	other := filepath.Join(binding.ProjectRoot, "linked")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(state, "state.db"), filepath.Join(other, "state.db")); err != nil {
		t.Fatal(err)
	}
	if err := CheckProjectBinding(ctx, other, binding, false); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("linked database accepted: %v", err)
	}
}
