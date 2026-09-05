package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/environment"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/validator"
)

func TestLegacyValidatorBindingRequiresReviewedConfiguration(t *testing.T) {
	for _, command := range []string{"[./check.sh]", "[sh, check.sh]"} {
		t.Run(command, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			project := filepath.Join(root, "project")
			initializeArtifactRepository(t, project)
			configPath := filepath.Join(project, "xgoal.yaml")
			content, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			content = []byte(strings.Replace(string(content), "argv: [go, version]", "argv: "+command, 1))
			if err := os.WriteFile(configPath, content, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(project, "check.sh"), []byte("#!/bin/sh\nprintf trusted\n"), 0755); err != nil {
				t.Fatal(err)
			}
			runArtifactGit(t, project, "add", "xgoal.yaml", "check.sh")
			runArtifactGit(t, project, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "old baseline")
			repo, err := gitrepo.Open(ctx, project)
			if err != nil {
				t.Fatal(err)
			}
			registry, err := validator.LoadRegistry(ctx, repo, "HEAD", "xgoal.yaml")
			if err != nil {
				t.Fatal(err)
			}
			legacy := registry.Definitions()[0]
			legacy.TrustedFiles = nil
			payload, err := canonical.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			var identity map[string]json.RawMessage
			if err := json.Unmarshal(payload, &identity); err != nil {
				t.Fatal(err)
			}
			delete(identity, "hash")
			legacy.Hash, err = canonical.Hash("validator-definition", "xgoal.validator-definition/v1alpha1", identity)
			if err != nil {
				t.Fatal(err)
			}
			if err := legacy.Validate(); err != nil {
				t.Fatal(err)
			}
			payload, err = canonical.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			migrations, err := loadMigrations()
			if err != nil {
				t.Fatal(err)
			}
			runtimeRoot := filepath.Join(root, "state")
			old, err := openWithMigrations(ctx, runtimeRoot, clock.Real{}, migrations[:10])
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			now := time.Now().UTC().Format(time.RFC3339Nano)
			if _, err := old.db.ExecContext(ctx, `INSERT INTO validator_definitions VALUES(?,?,?,?,?,?)`, legacy.Hash, legacy.ID, legacy.Type, legacy.Required, payload, now); err != nil {
				t.Fatal(err)
			}
			if _, err := old.db.ExecContext(ctx, `INSERT INTO validator_registrations VALUES(?,?,?,?,?,?)`, registry.ConfigHash(), registry.BaseCommit(), registry.BaseTree(), legacy.ID, legacy.Hash, now); err != nil {
				t.Fatal(err)
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			current, err := Open(ctx, runtimeRoot, clock.Real{})
			if err != nil {
				t.Fatal(err)
			}
			defer current.Close()
			if _, err := current.RecordValidatorRegistry(ctx, registry); !errors.Is(err, ErrTrustBindingMigrationRequired) {
				t.Fatalf("missing migration diagnosis: %v", err)
			}
			preserved, err := current.ValidatorRegistrationArtifact(ctx, registry.ConfigHash(), registry.BaseCommit(), legacy.ID)
			if err != nil || preserved.DefinitionHash != legacy.Hash {
				t.Fatalf("historical registration overwritten: %+v %v", preserved, err)
			}
			definition, err := current.ValidatorDefinitionArtifact(ctx, legacy.Hash)
			if err != nil || !reflect.DeepEqual(definition.Definition, legacy) {
				t.Fatalf("historical definition unreadable: %+v %v", definition, err)
			}
			// Follow the diagnosed migration: a reviewed explicit declaration and
			// committed baseline creates a new key without rewriting the old one.
			content = []byte(strings.Replace(string(content), "    argv:", "    trustedFiles: [check.sh]\n    argv:", 1))
			if err := os.WriteFile(configPath, content, 0600); err != nil {
				t.Fatal(err)
			}
			runArtifactGit(t, project, "add", "xgoal.yaml")
			runArtifactGit(t, project, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--no-verify", "-m", "reviewed explicit trust baseline")
			updated, err := validator.LoadRegistry(ctx, repo, "HEAD", "xgoal.yaml")
			if err != nil {
				t.Fatal(err)
			}
			if count, err := current.RecordValidatorRegistry(ctx, updated); err != nil || count != 1 {
				t.Fatalf("reviewed baseline could not register: %d %v", count, err)
			}
			provider, err := environment.NewLocal(filepath.Join(root, "environment"), repo, clock.Real{})
			if err != nil {
				t.Fatal(err)
			}
			checkout, err := repo.ReadCheckoutIdentity(ctx)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := provider.Prepare(ctx, environment.Spec{ID: "new_baseline", WorktreePath: repo.Root(), BaseCommit: updated.BaseCommit(), BaseTree: updated.BaseTree(), Identity: checkout, ConfigHash: updated.ConfigHash(), GoalRevisionHash: strings.Repeat("a", 64), ToolProbes: []environment.ToolProbe{{Name: "git", Argv: []string{"git", "--version"}, Required: true}}})
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Cleanup(ctx, handle)
			runner, err := validator.NewCommandRunner(filepath.Join(root, "run"), updated, provider, handle)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := runner.Run(ctx, validator.CommandRequest{RunID: "new_goal", ValidatorID: legacy.ID, GoalRevisionHash: strings.Repeat("a", 64), ConfigHash: updated.ConfigHash(), TreeHash: updated.BaseTree(), EnvironmentHash: strings.Repeat("b", 64), MaxOutputBytes: 1 << 20})
			if err != nil || receipt.Result != protocol.CommandPassed {
				t.Fatalf("new baseline did not execute: %+v %v", receipt, err)
			}
			if receipt.DefinitionHash == legacy.Hash {
				t.Fatal("new receipt revived old Evidence identity")
			}
		})
	}
}
