package exporter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/invocation"
	"github.com/monshunter/xgoal/internal/planner"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestExportLegacyPlannerPacketChecksFrozenValidatorsAndMissingInputs(t *testing.T) {
	for _, change := range []string{"none", "validators", "mode", "missing"} {
		t.Run(change, func(t *testing.T) {
			s, base := exportFixture(t)
			ctx := context.Background()
			r := planner.Request{ProtocolVersion: planner.RequestVersion, GoalID: "goal_two", RawGoal: "plan", Generation: 1, Mode: "standard", ConfigHash: strings.Repeat("a", 64), TrustedValidatorIDs: []string{"business-test"}}
			event := sqlite.EventInput{Type: "PlanningObserved", ActorType: "kernel", Payload: map[string]any{}}
			effect, _, err := s.RequestEffect(ctx, sqlite.EffectRequest{ID: "effect_planner", Key: "planner", Type: "planner", Request: r}, event)
			if err != nil {
				t.Fatal(err)
			}
			o := planner.Observation{InvocationID: "planner_call", SessionID: "legacy_session", ExecutionStopped: true}
			if _, err := s.UpdateEffect(ctx, effect.ID, effect.Version, domain.EffectExecuting, o, event); err != nil {
				t.Fatal(err)
			}
			p := planner.Packet{ProtocolVersion: planner.PacketVersion, GoalID: r.GoalID, RawGoal: r.RawGoal, Mode: r.Mode, ConfigHash: r.ConfigHash, TrustedValidators: r.TrustedValidatorIDs, ProjectRoot: filepath.Join(base, "project"), ProjectNetwork: "deny", ProjectSecrets: "deny"}
			if change != "missing" {
				path, _, err := planner.PrepareInvocation(s.Info().ProjectDir, o.InvocationID, r.Generation, p)
				if err != nil {
					t.Fatal(err)
				}
				if change == "validators" {
					p.TrustedValidators = []string{"format-only"}
				}
				if change == "mode" {
					p.Mode = "fast"
				}
				if change != "none" {
					data, err := canonical.Marshal(p)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, data, 0400); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, 0400); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err = Export(ctx, s, p.ProjectRoot, "goal_one", filepath.Join(base, "export"))
			if (err == nil) != (change == "none") {
				t.Fatalf("legacy %s export: %v", change, err)
			}
		})
	}
}

func TestExportPlanningPreflightAndSuppliedProposalHaveNoSealedPacket(t *testing.T) {
	for _, kind := range []string{"project_harness_required", "planner_failed", "crash-before-packet", "provided-proposal"} {
		t.Run(kind, func(t *testing.T) {
			s, base := exportFixture(t)
			ctx := context.Background()
			r := planner.Request{ProtocolVersion: planner.RequestVersion, GoalID: "goal_two", RawGoal: "plan", Generation: 1}
			if kind == "provided-proposal" {
				r.Proposal = &planner.Proposal{ProtocolVersion: planner.ProposalVersion}
			}
			event := sqlite.EventInput{Type: "PlanningObserved", ActorType: "kernel", Payload: map[string]any{}}
			effect, _, err := s.RequestEffect(ctx, sqlite.EffectRequest{ID: "effect_planner", Key: "planner", Type: "planner", Request: r}, event)
			if err != nil {
				t.Fatal(err)
			}
			o := planner.Observation{InvocationID: "planner_call", FailureCode: kind, ExecutionStopped: kind != "crash-before-packet"}
			if _, err := s.UpdateEffect(ctx, effect.ID, effect.Version, domain.EffectExecuting, o, event); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(base, "export")
			if _, err := Export(ctx, s, filepath.Join(base, "project"), "goal_one", output); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(output, "manifest.json"))
			if err != nil {
				t.Fatal(err)
			}
			var m Manifest
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, owner := range m.Owners {
				if owner.Kind == "planner" {
					found = owner.ArtifactStatus != ""
				}
			}
			if !found {
				t.Fatal("missing explicit uncreated or unknown Packet state")
			}
		})
	}
}

func TestExclusiveExportPublicationNeverReplacesExistingDirectory(t *testing.T) {
	root := t.TempDir()
	from, to := filepath.Join(root, "incomplete"), filepath.Join(root, "user")
	if err := os.Mkdir(from, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(to, 0700); err != nil {
		t.Fatal(err)
	}
	exportWrite(t, from, "manifest.json", []byte("complete"))
	if err := renameExclusive(from, to); err == nil {
		t.Fatal("replaced an existing empty user directory")
	}
	if _, err := os.Stat(filepath.Join(from, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(to)
	if err != nil || len(entries) != 0 {
		t.Fatal("user directory changed")
	}
}

func TestReportRenameWindowUsesFrozenBlobButCommittedCorruptionFails(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("frozen Markdown\n")
	source := filepath.Join(base, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"PENDING_RENAME", "COMMITTED"} {
		t.Run(state, func(t *testing.T) {
			destination := filepath.Join(base, state)
			if err := os.Mkdir(destination, 0700); err != nil {
				t.Fatal(err)
			}
			e := &writer{ctx: context.Background(), source: source, root: destination, files: map[string]File{}}
			a := sqlite.SnapshotArtifact{Kind: "report_markdown", ID: "goal", State: state, Path: filepath.Join(source, "reports/goal/report.md"), Hash: invocation.SHA256(data), Data: data}
			err := e.reportFile(a)
			if state == "COMMITTED" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing published file masked with blob: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(destination, "files/reports/goal/report.md"))
			if err != nil || string(got) != string(data) {
				t.Fatalf("snapshot blob not copied %q %v", got, err)
			}
			if _, err := os.Stat(a.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("export recovered or wrote source report")
			}
		})
	}
}

func TestExportRejectsCheckoutStateSymlinkAndCancelledRequests(t *testing.T) {
	s, base := exportFixture(t)
	project := filepath.Join(base, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{filepath.Join(project, "export"), filepath.Join(s.Info().ProjectDir, "export")} {
		if _, err := Export(context.Background(), s, project, "goal_one", destination); err == nil {
			t.Fatal("export writes into active inputs")
		}
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(project, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(context.Background(), s, project, "goal_one", filepath.Join(alias, "export")); err == nil {
		t.Fatal("linked destination accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	destination := filepath.Join(base, "cancelled")
	if _, err := Export(ctx, s, project, "goal_one", destination); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled export published")
	}
}
