package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/patch"
	"github.com/monshunter/xgoal/internal/protocol"
	basestore "github.com/monshunter/xgoal/internal/store"
	"github.com/monshunter/xgoal/internal/validator"
	"github.com/monshunter/xgoal/internal/workspace"
)

type WorkspaceArtifactState string

const (
	WorkspaceArtifactActive  WorkspaceArtifactState = "ACTIVE"
	WorkspaceArtifactCleaned WorkspaceArtifactState = "CLEANED"
	WorkspaceArtifactFailed  WorkspaceArtifactState = "FAILED"
)

type WorkspaceArtifact struct {
	Snapshot workspace.Snapshot
	State    WorkspaceArtifactState
	Version  int64
}

type PatchBundleArtifact struct {
	Bundle    protocol.PatchBundle
	Path      string
	State     string
	CreatedAt time.Time
}

type EnvironmentArtifact struct {
	WorkspaceID string
	Snapshot    protocol.EnvironmentSnapshot
	Hash        string
	CreatedAt   time.Time
}

type ValidatorDefinitionArtifact struct {
	Definition validator.Definition
	CreatedAt  time.Time
}

type ValidatorRegistrationArtifact struct {
	ConfigHash     string
	BaseCommit     string
	BaseTree       string
	ValidatorID    string
	DefinitionHash string
	CreatedAt      time.Time
}

type ValidatorRunArtifact struct {
	AttemptID   string
	WorkspaceID string
	Receipt     protocol.CommandReceipt
	Hash        string
	CreatedAt   time.Time
}

func (s *Store) RecordWorkspace(ctx context.Context, snapshot workspace.Snapshot) (WorkspaceArtifact, bool, error) {
	if err := snapshot.ValidateMarkerBinding(); err != nil {
		return WorkspaceArtifact{}, false, err
	}
	if err := validateWorkspaceArtifactLayout(s.info.ProjectDir, snapshot); err != nil {
		return WorkspaceArtifact{}, false, err
	}
	disk, err := workspace.ReadMarkerSnapshot(snapshot.MarkerPath)
	if err != nil || !sameWorkspaceMarker(snapshot, disk) {
		return WorkspaceArtifact{}, false, errors.New("workspace artifact does not match its immutable marker")
	}
	created := false
	var result WorkspaceArtifact
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		attempt, err := readAttempt(ctx, tx, snapshot.AttemptID)
		if err != nil {
			return err
		}
		if attempt.ID != snapshot.AttemptID {
			return errors.New("workspace attempt binding mismatch")
		}
		existing, err := readWorkspaceArtifact(ctx, tx, snapshot.ID, false, s.info.ProjectDir)
		if err == nil {
			if !sameWorkspaceMarker(existing.Snapshot, snapshot) {
				return fmt.Errorf("workspace %q: %w", snapshot.ID, basestore.ErrIdempotencyConflict)
			}
			result = existing
			return nil
		}
		if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		kind := "ATTEMPT"
		if snapshot.Kind == workspace.Validation {
			kind = "VALIDATION"
		}
		metadata, err := canonical.Marshal(workspaceExecutionMetadata{Identity: snapshot.Identity, ExcludePaths: snapshot.ExcludePaths, InputTree: snapshot.InputTree})
		if err != nil {
			return err
		}
		if snapshot.ExecutionModel != workspace.ExecutionCurrentDirectory {
			return workspace.ErrLegacyWorkspace
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO workspaces(
    id, attempt_id, kind, path, common_dir, base_commit, base_tree,
    config_hash, marker_hash, state, version, created_at, updated_at, execution_model, execution_path, execution_metadata
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)`,
			snapshot.ID, snapshot.AttemptID, kind, filepath.Dir(snapshot.MarkerPath), snapshot.CommonDir,
			snapshot.BaseCommit, snapshot.BaseTree, snapshot.ConfigHash, snapshot.MarkerHash,
			WorkspaceArtifactActive, snapshot.CreatedAt.UTC().Format(time.RFC3339Nano),
			s.source.Now().UTC().Format(time.RFC3339Nano), snapshot.ExecutionModel, snapshot.Path, metadata,
		)
		if err != nil {
			return fmt.Errorf("insert workspace artifact %q: %w", snapshot.ID, err)
		}
		prepared, err := prepareEvent(EventInput{
			Type: "WorkspaceRecorded", ActorType: "kernel", CorrelationID: snapshot.AttemptID,
			Payload: map[string]any{"marker_hash": snapshot.MarkerHash, "kind": kind},
		})
		if err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "workspace", snapshot.ID, prepared); err != nil {
			return err
		}
		created = true
		result = WorkspaceArtifact{Snapshot: snapshot, State: WorkspaceArtifactActive, Version: 1}
		return nil
	})
	return result, created, err
}

func (s *Store) WorkspaceArtifact(ctx context.Context, id string) (WorkspaceArtifact, error) {
	if id == "" {
		return WorkspaceArtifact{}, errors.New("workspace artifact id is required")
	}
	return readWorkspaceArtifact(ctx, s.db, id, true, s.info.ProjectDir)
}

func (s *Store) TransitionWorkspaceArtifact(ctx context.Context, id string, expectedVersion int64, target WorkspaceArtifactState) (WorkspaceArtifact, error) {
	if id == "" || expectedVersion <= 0 || (target != WorkspaceArtifactCleaned && target != WorkspaceArtifactFailed) {
		return WorkspaceArtifact{}, errors.New("invalid workspace artifact transition")
	}
	var result WorkspaceArtifact
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		current, err := readWorkspaceArtifact(ctx, tx, id, false, s.info.ProjectDir)
		if err != nil {
			return err
		}
		if current.Version != expectedVersion || current.State != WorkspaceArtifactActive {
			return fmt.Errorf("workspace %q: %w", id, basestore.ErrConflict)
		}
		now := s.source.Now().UTC().Format(time.RFC3339Nano)
		updated, err := tx.ExecContext(ctx, `
UPDATE workspaces SET state = ?, version = version + 1, updated_at = ?
WHERE id = ? AND version = ? AND state = ?`, target, now, id, expectedVersion, WorkspaceArtifactActive)
		if err != nil {
			return err
		}
		if err := requireOneArtifactRow(updated, "workspace", id); err != nil {
			return err
		}
		prepared, err := prepareEvent(EventInput{Type: "Workspace" + string(target), ActorType: "kernel", Payload: map[string]any{"state": target}})
		if err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "workspace", id, prepared); err != nil {
			return err
		}
		current.State = target
		current.Version++
		result = current
		return nil
	})
	return result, err
}

func (s *Store) RecordPatchBundle(ctx context.Context, bundle protocol.PatchBundle, bundlePath string) (PatchBundleArtifact, bool, error) {
	if err := bundle.Validate(); err != nil {
		return PatchBundleArtifact{}, false, err
	}
	runtimeRoot, err := canonicalArtifactDirectory(s.info.ProjectDir)
	if err != nil {
		return PatchBundleArtifact{}, false, err
	}
	expectedPath := filepath.Join(runtimeRoot, "patches", bundle.AttemptID)
	canonicalPath, err := canonicalArtifactDirectory(bundlePath)
	if err != nil || canonicalPath != expectedPath {
		return PatchBundleArtifact{}, false, errors.New("patch bundle path does not match the project runtime layout")
	}
	patchStore, err := patch.NewStore(s.info.ProjectDir)
	if err != nil {
		return PatchBundleArtifact{}, false, err
	}
	loaded, err := patchStore.Load(bundle.AttemptID)
	if err != nil || loaded.Bundle.BundleHash != bundle.BundleHash || loaded.Bundle.ManifestHash != bundle.ManifestHash {
		return PatchBundleArtifact{}, false, errors.New("patch bundle does not match its immutable object store")
	}
	created := false
	var result PatchBundleArtifact
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		attempt, err := readAttempt(ctx, tx, bundle.AttemptID)
		if err != nil {
			return err
		}
		if attempt.BaseTree != bundle.BaseTree {
			return errors.New("patch bundle base tree does not match its attempt")
		}
		existing, err := readPatchBundleArtifact(ctx, tx, bundle.AttemptID, false, s.info.ProjectDir)
		if err == nil {
			if existing.Bundle.BundleHash != bundle.BundleHash || existing.Path != canonicalPath {
				return fmt.Errorf("patch bundle %q: %w", bundle.AttemptID, basestore.ErrIdempotencyConflict)
			}
			result = existing
			return nil
		}
		if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		createdAt := s.source.Now().UTC()
		_, err = tx.ExecContext(ctx, `
INSERT INTO patch_bundles(attempt_id, base_commit, base_tree, manifest_hash, bundle_hash, bundle_path, state, created_at)
VALUES (?, ?, ?, ?, ?, ?, 'VALID', ?)`, bundle.AttemptID, bundle.BaseCommit, bundle.BaseTree,
			bundle.ManifestHash, bundle.BundleHash, canonicalPath, createdAt.Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("insert patch bundle %q: %w", bundle.AttemptID, err)
		}
		prepared, err := prepareEvent(EventInput{Type: "PatchBundleRecorded", ActorType: "kernel", CorrelationID: bundle.AttemptID, Payload: map[string]any{"bundle_hash": bundle.BundleHash}})
		if err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "patch_bundle", bundle.AttemptID, prepared); err != nil {
			return err
		}
		created = true
		result = PatchBundleArtifact{Bundle: bundle, Path: canonicalPath, State: "VALID", CreatedAt: createdAt}
		return nil
	})
	return result, created, err
}

func (s *Store) PatchBundleArtifact(ctx context.Context, attemptID string) (PatchBundleArtifact, error) {
	if attemptID == "" {
		return PatchBundleArtifact{}, errors.New("patch bundle attempt id is required")
	}
	return readPatchBundleArtifact(ctx, s.db, attemptID, true, s.info.ProjectDir)
}

func (s *Store) RecordEnvironmentSnapshot(ctx context.Context, workspaceID string, snapshot protocol.EnvironmentSnapshot) (EnvironmentArtifact, bool, error) {
	if workspaceID == "" {
		return EnvironmentArtifact{}, false, errors.New("environment snapshot workspace id is required")
	}
	hash, err := snapshot.Hash()
	if err != nil {
		return EnvironmentArtifact{}, false, err
	}
	payload, err := canonical.Marshal(snapshot)
	if err != nil {
		return EnvironmentArtifact{}, false, err
	}
	created := false
	var result EnvironmentArtifact
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		workspaceRecord, err := readWorkspaceArtifact(ctx, tx, workspaceID, false, s.info.ProjectDir)
		if err != nil {
			return err
		}
		expectedTree := workspaceRecord.Snapshot.BaseTree
		expectedCommit := workspaceRecord.Snapshot.BaseCommit
		if workspaceRecord.Snapshot.ExecutionModel == workspace.ExecutionCurrentDirectory {
			expectedTree = workspaceRecord.Snapshot.InputTree
			expectedCommit = workspaceRecord.Snapshot.Identity.HeadCommit
		}
		if workspaceRecord.State != WorkspaceArtifactActive || expectedTree != snapshot.BaseTree || expectedCommit != snapshot.BaseCommit || workspaceRecord.Snapshot.ConfigHash != snapshot.ConfigHash {
			return errors.New("environment snapshot does not match its active workspace")
		}
		existing, err := readEnvironmentArtifact(ctx, tx, snapshot.ID)
		if err == nil {
			if existing.Hash != hash || existing.WorkspaceID != workspaceID {
				return fmt.Errorf("environment snapshot %q: %w", snapshot.ID, basestore.ErrIdempotencyConflict)
			}
			result = existing
			return nil
		}
		if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO environment_snapshots(
    id, workspace_id, snapshot_hash, goal_revision_hash, config_hash,
    base_tree, isolation_level, payload_json, created_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, snapshot.ID, workspaceID, hash, snapshot.GoalRevisionHash,
			snapshot.ConfigHash, snapshot.BaseTree, snapshot.IsolationLevel, payload,
			snapshot.CapturedAt.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("insert environment snapshot %q: %w", snapshot.ID, err)
		}
		prepared, err := prepareEvent(EventInput{Type: "EnvironmentSnapshotRecorded", ActorType: "kernel", CorrelationID: workspaceRecord.Snapshot.AttemptID, Payload: map[string]any{"snapshot_hash": hash}})
		if err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "environment_snapshot", snapshot.ID, prepared); err != nil {
			return err
		}
		created = true
		result = EnvironmentArtifact{WorkspaceID: workspaceID, Snapshot: snapshot, Hash: hash, CreatedAt: snapshot.CapturedAt.UTC()}
		return nil
	})
	return result, created, err
}

func (s *Store) EnvironmentArtifact(ctx context.Context, id string) (EnvironmentArtifact, error) {
	if id == "" {
		return EnvironmentArtifact{}, errors.New("environment snapshot id is required")
	}
	return readEnvironmentArtifact(ctx, s.db, id)
}

var ErrTrustBindingMigrationRequired = errors.New("TRUST_BINDING_MIGRATION_REQUIRED: preserve the historical Goal and Evidence; explicitly declare trustedFiles for validator entrypoints in xgoal.yaml, review and commit that configuration as a new trust baseline, then create a new Goal; approve/replan cannot upgrade this frozen registration")

func (s *Store) RecordValidatorRegistry(ctx context.Context, registry *validator.Registry) (int, error) {
	if registry == nil || !artifactObjectID(registry.BaseCommit()) || !artifactObjectID(registry.BaseTree()) || !artifactSHA256(registry.ConfigHash()) {
		return 0, errors.New("validator registry is required")
	}
	type pendingDefinition struct {
		definition validator.Definition
		payload    []byte
	}
	pending := make([]pendingDefinition, 0)
	for _, definition := range registry.Definitions() {
		if err := definition.Validate(); err != nil {
			return 0, err
		}
		payload, err := canonical.Marshal(definition)
		if err != nil {
			return 0, err
		}
		pending = append(pending, pendingDefinition{definition: definition, payload: payload})
	}
	registered := 0
	err := s.withTransaction(ctx, func(tx *sql.Tx) error {
		for _, item := range pending {
			existing, err := readValidatorDefinitionArtifact(ctx, tx, item.definition.Hash)
			if err == nil {
				if !sameValidatorDefinition(existing.Definition, item.definition) {
					return fmt.Errorf("validator definition %q: %w", item.definition.Hash, basestore.ErrIdempotencyConflict)
				}
			} else if !errors.Is(err, basestore.ErrNotFound) {
				return err
			} else {
				createdAt := s.source.Now().UTC().Format(time.RFC3339Nano)
				_, err = tx.ExecContext(ctx, `
INSERT INTO validator_definitions(
    definition_hash, id, validator_type, required, definition_json, created_at
)
				VALUES (?, ?, ?, ?, ?, ?)`, item.definition.Hash, item.definition.ID, item.definition.Type,
					item.definition.Required, item.payload, createdAt)
				if err != nil {
					return fmt.Errorf("insert validator definition %q: %w", item.definition.ID, err)
				}
				prepared, err := prepareEvent(EventInput{Type: "ValidatorDefinitionRecorded", ActorType: "kernel", Payload: map[string]any{"definition_hash": item.definition.Hash}})
				if err != nil {
					return err
				}
				if err := s.appendEvent(ctx, tx, "validator_definition", item.definition.Hash, prepared); err != nil {
					return err
				}
			}

			registration, err := readValidatorRegistrationArtifact(ctx, tx, registry.ConfigHash(), registry.BaseCommit(), item.definition.ID)
			if err == nil {
				if registration.DefinitionHash != item.definition.Hash && len(item.definition.TrustedFiles) > 0 {
					previous, err := readValidatorDefinitionArtifact(ctx, tx, registration.DefinitionHash)
					if err != nil {
						return err
					}
					if len(previous.Definition.TrustedFiles) == 0 {
						return fmt.Errorf("validator %q: %w", item.definition.ID, ErrTrustBindingMigrationRequired)
					}
				}
				if registration.BaseTree != registry.BaseTree() || registration.DefinitionHash != item.definition.Hash {
					return fmt.Errorf("validator registration %q: %w", item.definition.ID, basestore.ErrIdempotencyConflict)
				}
				continue
			}
			if !errors.Is(err, basestore.ErrNotFound) {
				return err
			}
			createdAt := s.source.Now().UTC().Format(time.RFC3339Nano)
			_, err = tx.ExecContext(ctx, `
INSERT INTO validator_registrations(
    config_hash, base_commit, base_tree, validator_id, definition_hash, created_at
)
VALUES (?, ?, ?, ?, ?, ?)`, registry.ConfigHash(), registry.BaseCommit(), registry.BaseTree(),
				item.definition.ID, item.definition.Hash, createdAt)
			if err != nil {
				return fmt.Errorf("insert validator registration %q: %w", item.definition.ID, err)
			}
			prepared, err := prepareEvent(EventInput{
				Type: "ValidatorRegistrationRecorded", ActorType: "kernel",
				Payload: map[string]any{"definition_hash": item.definition.Hash, "config_hash": registry.ConfigHash(), "base_commit": registry.BaseCommit()},
			})
			if err != nil {
				return err
			}
			registrationID := registry.ConfigHash() + "/" + registry.BaseCommit() + "/" + item.definition.ID
			if err := s.appendEvent(ctx, tx, "validator_registration", registrationID, prepared); err != nil {
				return err
			}
			registered++
		}
		return nil
	})
	return registered, err
}

func (s *Store) ValidatorDefinitionArtifact(ctx context.Context, hash string) (ValidatorDefinitionArtifact, error) {
	if hash == "" {
		return ValidatorDefinitionArtifact{}, errors.New("validator definition hash is required")
	}
	return readValidatorDefinitionArtifact(ctx, s.db, hash)
}

func (s *Store) ValidatorRegistrationArtifact(ctx context.Context, configHash, baseCommit, validatorID string) (ValidatorRegistrationArtifact, error) {
	if !artifactSHA256(configHash) || !artifactObjectID(baseCommit) || validatorID == "" {
		return ValidatorRegistrationArtifact{}, errors.New("validator registration identity is invalid")
	}
	return readValidatorRegistrationArtifact(ctx, s.db, configHash, baseCommit, validatorID)
}

func (s *Store) RecordValidatorRun(ctx context.Context, attemptID, workspaceID string, receipt protocol.CommandReceipt) (ValidatorRunArtifact, bool, error) {
	if workspaceID == "" {
		return ValidatorRunArtifact{}, false, errors.New("validator run workspace id is required")
	}
	receiptHash, err := receipt.Hash()
	if err != nil {
		return ValidatorRunArtifact{}, false, err
	}
	disk, err := validator.ReadReceipt(s.info.ProjectDir, receipt.ID)
	if err != nil {
		return ValidatorRunArtifact{}, false, err
	}
	diskHash, err := disk.Hash()
	if err != nil || diskHash != receiptHash {
		return ValidatorRunArtifact{}, false, errors.New("validator run does not match its immutable receipt")
	}
	payload, err := canonical.Marshal(receipt)
	if err != nil {
		return ValidatorRunArtifact{}, false, err
	}
	created := false
	var result ValidatorRunArtifact
	err = s.withTransaction(ctx, func(tx *sql.Tx) error {
		definition, err := readValidatorDefinitionArtifact(ctx, tx, receipt.DefinitionHash)
		if err != nil {
			return err
		}
		workspaceRecord, err := readWorkspaceArtifact(ctx, tx, workspaceID, false, s.info.ProjectDir)
		if err != nil {
			return err
		}
		registered, err := validatorRegistrationExists(ctx, tx, receipt.ConfigHash, receipt.ValidatorID, receipt.DefinitionHash)
		if err != nil {
			return err
		}
		if definition.Definition.ID != receipt.ValidatorID || !registered || workspaceRecord.Snapshot.ConfigHash != receipt.ConfigHash {
			return errors.New("validator run config does not match definition and workspace")
		}
		environmentRecord, err := readEnvironmentArtifactByHash(ctx, tx, receipt.EnvironmentHash)
		if err != nil || environmentRecord.WorkspaceID != workspaceID || environmentRecord.Snapshot.GoalRevisionHash != receipt.GoalRevisionHash {
			return errors.New("validator run environment does not match its workspace")
		}
		if attemptID != "" {
			attempt, err := readAttempt(ctx, tx, attemptID)
			if err != nil {
				return err
			}
			if attempt.ID != workspaceRecord.Snapshot.AttemptID {
				return errors.New("validator run attempt does not match its workspace")
			}
		}
		existing, err := readValidatorRunArtifact(ctx, tx, receipt.ID, false, s.info.ProjectDir)
		if err == nil {
			if existing.Hash != receiptHash || existing.WorkspaceID != workspaceID || existing.AttemptID != attemptID {
				return fmt.Errorf("validator run %q: %w", receipt.ID, basestore.ErrIdempotencyConflict)
			}
			result = existing
			return nil
		}
		if !errors.Is(err, basestore.ErrNotFound) {
			return err
		}
		_, err = tx.ExecContext(ctx, `
INSERT INTO validator_runs(
    id, definition_hash, attempt_id, workspace_id, receipt_hash,
    goal_revision_hash, config_hash, environment_hash, tree_hash,
    result, receipt_json, created_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, receipt.ID, receipt.DefinitionHash,
			nullableString(attemptID), workspaceID, receiptHash, receipt.GoalRevisionHash,
			receipt.ConfigHash, receipt.EnvironmentHash, receipt.TreeHash, receipt.Result,
			payload, receipt.FinishedAt.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return fmt.Errorf("insert validator run %q: %w", receipt.ID, err)
		}
		prepared, err := prepareEvent(EventInput{Type: "ValidatorRunRecorded", ActorType: "kernel", CorrelationID: attemptID, Payload: map[string]any{"receipt_hash": receiptHash, "result": receipt.Result}})
		if err != nil {
			return err
		}
		if err := s.appendEvent(ctx, tx, "validator_run", receipt.ID, prepared); err != nil {
			return err
		}
		created = true
		result = ValidatorRunArtifact{AttemptID: attemptID, WorkspaceID: workspaceID, Receipt: receipt, Hash: receiptHash, CreatedAt: receipt.FinishedAt.UTC()}
		return nil
	})
	return result, created, err
}

func (s *Store) ValidatorRunArtifact(ctx context.Context, id string) (ValidatorRunArtifact, error) {
	if id == "" {
		return ValidatorRunArtifact{}, errors.New("validator run id is required")
	}
	return readValidatorRunArtifact(ctx, s.db, id, true, s.info.ProjectDir)
}

type artifactQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type workspaceExecutionMetadata struct {
	Identity     gitrepo.CheckoutIdentity `json:"identity"`
	ExcludePaths []string                 `json:"exclude_paths"`
	InputTree    string                   `json:"input_tree"`
}

func readWorkspaceArtifact(ctx context.Context, queryer artifactQueryer, id string, verifyDisk bool, runtimeRoot string) (WorkspaceArtifact, error) {
	var result WorkspaceArtifact
	var kind, createdAt, storedPath, executionPath string
	var metadata []byte
	err := queryer.QueryRowContext(ctx, `
SELECT id,attempt_id,kind,path,common_dir,base_commit,base_tree,config_hash,marker_hash,state,version,created_at,
 execution_model,execution_path,execution_metadata FROM workspaces WHERE id=?`, id).Scan(
		&result.Snapshot.ID, &result.Snapshot.AttemptID, &kind, &storedPath, &result.Snapshot.CommonDir, &result.Snapshot.BaseCommit, &result.Snapshot.BaseTree,
		&result.Snapshot.ConfigHash, &result.Snapshot.MarkerHash, &result.State, &result.Version, &createdAt,
		&result.Snapshot.ExecutionModel, &executionPath, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkspaceArtifact{}, fmt.Errorf("workspace %q: %w", id, basestore.ErrNotFound)
	}
	if err != nil {
		return WorkspaceArtifact{}, err
	}
	switch kind {
	case "ATTEMPT":
		result.Snapshot.Kind = workspace.Attempt
	case "VALIDATION":
		result.Snapshot.Kind = workspace.Validation
	default:
		return WorkspaceArtifact{}, errors.New("invalid workspace kind")
	}
	canonicalRuntime, err := canonicalArtifactDirectory(runtimeRoot)
	if err != nil {
		return WorkspaceArtifact{}, err
	}
	directory := "attempts"
	if result.Snapshot.Kind == workspace.Validation {
		directory = "validation"
	}
	container := filepath.Join(canonicalRuntime, "workspaces", directory, result.Snapshot.ID)
	result.Snapshot.MarkerPath = filepath.Join(container, "marker.json")
	switch result.Snapshot.ExecutionModel {
	case workspace.ExecutionLegacyWorktree:
		if storedPath != filepath.Join(container, "tree") || executionPath != "" {
			return WorkspaceArtifact{}, errors.New("persisted legacy workspace layout mismatch")
		}
		result.Snapshot.Path = storedPath
	case workspace.ExecutionCurrentDirectory:
		if storedPath != container {
			return WorkspaceArtifact{}, errors.New("persisted workspace metadata path mismatch")
		}
		var decoded workspaceExecutionMetadata
		if err := decodeCanonical(metadata, &decoded); err != nil {
			return WorkspaceArtifact{}, err
		}
		result.Snapshot.Path = executionPath
		result.Snapshot.Identity = decoded.Identity
		result.Snapshot.ExcludePaths = decoded.ExcludePaths
		result.Snapshot.InputTree = decoded.InputTree
		result.Snapshot.HeadCommit = decoded.Identity.HeadCommit
		result.Snapshot.HeadTree = decoded.Identity.HeadTree
	default:
		return WorkspaceArtifact{}, errors.New("unknown workspace execution model")
	}
	result.Snapshot.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil || result.Version <= 0 || (result.State != WorkspaceArtifactActive && result.State != WorkspaceArtifactCleaned && result.State != WorkspaceArtifactFailed) {
		return WorkspaceArtifact{}, errors.New("invalid persisted workspace")
	}
	if err := result.Snapshot.ValidateMarkerBinding(); err != nil {
		return WorkspaceArtifact{}, err
	}
	if verifyDisk && result.State == WorkspaceArtifactActive {
		disk, err := workspace.ReadMarkerSnapshot(result.Snapshot.MarkerPath)
		if err != nil || !sameWorkspaceMarker(result.Snapshot, disk) {
			return WorkspaceArtifact{}, errors.New("active workspace marker does not match persisted state")
		}
	}
	return result, nil
}

func readPatchBundleArtifact(ctx context.Context, queryer artifactQueryer, attemptID string, verifyDisk bool, runtimeRoot string) (PatchBundleArtifact, error) {
	var result PatchBundleArtifact
	var createdAt string
	err := queryer.QueryRowContext(ctx, `
SELECT base_commit, base_tree, manifest_hash, bundle_hash, bundle_path, state, created_at
FROM patch_bundles WHERE attempt_id = ?`, attemptID).Scan(&result.Bundle.BaseCommit, &result.Bundle.BaseTree,
		&result.Bundle.ManifestHash, &result.Bundle.BundleHash, &result.Path, &result.State, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return PatchBundleArtifact{}, fmt.Errorf("patch bundle %q: %w", attemptID, basestore.ErrNotFound)
	}
	if err != nil {
		return PatchBundleArtifact{}, err
	}
	result.Bundle.AttemptID = attemptID
	result.Bundle.ProtocolVersion = protocol.PatchBundleVersion
	canonicalRuntime, err := canonicalArtifactDirectory(runtimeRoot)
	if err != nil {
		return PatchBundleArtifact{}, err
	}
	if result.Path != filepath.Join(canonicalRuntime, "patches", attemptID) {
		return PatchBundleArtifact{}, errors.New("persisted patch path is outside the project runtime layout")
	}
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil || result.State != "VALID" {
		return PatchBundleArtifact{}, errors.New("persisted patch bundle metadata is invalid")
	}
	if verifyDisk {
		store, err := patch.NewStore(runtimeRoot)
		if err != nil {
			return PatchBundleArtifact{}, err
		}
		captured, err := store.Load(attemptID)
		if err != nil || captured.Bundle.BundleHash != result.Bundle.BundleHash || captured.Bundle.ManifestHash != result.Bundle.ManifestHash ||
			captured.Bundle.BaseCommit != result.Bundle.BaseCommit || captured.Bundle.BaseTree != result.Bundle.BaseTree {
			return PatchBundleArtifact{}, errors.New("persisted patch metadata does not match immutable bundle")
		}
		result.Bundle = captured.Bundle
	}
	return result, nil
}

func readEnvironmentArtifact(ctx context.Context, queryer artifactQueryer, id string) (EnvironmentArtifact, error) {
	return readEnvironmentArtifactWhere(ctx, queryer, "id", id)
}

func readEnvironmentArtifactByHash(ctx context.Context, queryer artifactQueryer, hash string) (EnvironmentArtifact, error) {
	return readEnvironmentArtifactWhere(ctx, queryer, "snapshot_hash", hash)
}

func readEnvironmentArtifactWhere(ctx context.Context, queryer artifactQueryer, column, value string) (EnvironmentArtifact, error) {
	var result EnvironmentArtifact
	var payload []byte
	var goalHash, configHash, baseTree, isolation, createdAt string
	query := `SELECT workspace_id, snapshot_hash, goal_revision_hash, config_hash, base_tree,
       isolation_level, payload_json, created_at FROM environment_snapshots WHERE ` + column + ` = ?`
	err := queryer.QueryRowContext(ctx, query, value).Scan(&result.WorkspaceID, &result.Hash, &goalHash, &configHash,
		&baseTree, &isolation, &payload, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return EnvironmentArtifact{}, fmt.Errorf("environment artifact %q: %w", value, basestore.ErrNotFound)
	}
	if err != nil {
		return EnvironmentArtifact{}, err
	}
	if err := decodeCanonical(payload, &result.Snapshot); err != nil {
		return EnvironmentArtifact{}, err
	}
	hash, err := result.Snapshot.Hash()
	if err != nil || hash != result.Hash || result.Snapshot.GoalRevisionHash != goalHash || result.Snapshot.ConfigHash != configHash ||
		result.Snapshot.BaseTree != baseTree || result.Snapshot.IsolationLevel != isolation {
		return EnvironmentArtifact{}, errors.New("persisted environment snapshot violates its contract")
	}
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil || !result.CreatedAt.Equal(result.Snapshot.CapturedAt) {
		return EnvironmentArtifact{}, errors.New("persisted environment snapshot time is invalid")
	}
	return result, nil
}

func readValidatorDefinitionArtifact(ctx context.Context, queryer artifactQueryer, hash string) (ValidatorDefinitionArtifact, error) {
	var result ValidatorDefinitionArtifact
	var payload []byte
	var id, validatorType, createdAt string
	var required bool
	err := queryer.QueryRowContext(ctx, `
SELECT id, validator_type, required, definition_json, created_at
FROM validator_definitions WHERE definition_hash = ?`, hash).Scan(&id, &validatorType, &required, &payload, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ValidatorDefinitionArtifact{}, fmt.Errorf("validator definition %q: %w", hash, basestore.ErrNotFound)
	}
	if err != nil {
		return ValidatorDefinitionArtifact{}, err
	}
	if err := decodeCanonical(payload, &result.Definition); err != nil {
		return ValidatorDefinitionArtifact{}, err
	}
	if err := result.Definition.Validate(); err != nil || result.Definition.Hash != hash || result.Definition.ID != id ||
		result.Definition.Type != validatorType || result.Definition.Required != required {
		return ValidatorDefinitionArtifact{}, errors.New("persisted validator definition violates its contract")
	}
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return ValidatorDefinitionArtifact{}, err
	}
	return result, nil
}

func readValidatorRegistrationArtifact(ctx context.Context, queryer artifactQueryer, configHash, baseCommit, validatorID string) (ValidatorRegistrationArtifact, error) {
	var result ValidatorRegistrationArtifact
	var createdAt string
	err := queryer.QueryRowContext(ctx, `
SELECT config_hash, base_commit, base_tree, validator_id, definition_hash, created_at
FROM validator_registrations
WHERE config_hash = ? AND base_commit = ? AND validator_id = ?`, configHash, baseCommit, validatorID).Scan(
		&result.ConfigHash, &result.BaseCommit, &result.BaseTree, &result.ValidatorID, &result.DefinitionHash, &createdAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ValidatorRegistrationArtifact{}, fmt.Errorf("validator registration %q: %w", validatorID, basestore.ErrNotFound)
	}
	if err != nil {
		return ValidatorRegistrationArtifact{}, err
	}
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil || !artifactSHA256(result.ConfigHash) || !artifactObjectID(result.BaseCommit) || !artifactObjectID(result.BaseTree) ||
		result.ValidatorID == "" || !artifactSHA256(result.DefinitionHash) {
		return ValidatorRegistrationArtifact{}, errors.New("persisted validator registration violates its contract")
	}
	definition, err := readValidatorDefinitionArtifact(ctx, queryer, result.DefinitionHash)
	if err != nil || definition.Definition.ID != result.ValidatorID {
		return ValidatorRegistrationArtifact{}, errors.New("persisted validator registration does not match its definition")
	}
	return result, nil
}

func validatorRegistrationExists(ctx context.Context, queryer artifactQueryer, configHash, validatorID, definitionHash string) (bool, error) {
	var count int
	err := queryer.QueryRowContext(ctx, `
	SELECT COUNT(*)
	FROM validator_registrations registration
	JOIN validator_definitions definition ON definition.definition_hash = registration.definition_hash
	WHERE registration.config_hash = ? AND registration.validator_id = ? AND registration.definition_hash = ?
	  AND definition.id = registration.validator_id`, configHash, validatorID, definitionHash).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func readValidatorRunArtifact(ctx context.Context, queryer artifactQueryer, id string, verifyDisk bool, runtimeRoot string) (ValidatorRunArtifact, error) {
	var result ValidatorRunArtifact
	var attempt sql.NullString
	var payload []byte
	var definitionHash, goalHash, configHash, environmentHash, treeHash, commandResult, createdAt string
	err := queryer.QueryRowContext(ctx, `
SELECT attempt_id, workspace_id, receipt_hash, definition_hash, goal_revision_hash,
       config_hash, environment_hash, tree_hash, result, receipt_json, created_at
FROM validator_runs WHERE id = ?`, id).Scan(&attempt, &result.WorkspaceID, &result.Hash, &definitionHash,
		&goalHash, &configHash, &environmentHash, &treeHash, &commandResult, &payload, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ValidatorRunArtifact{}, fmt.Errorf("validator run %q: %w", id, basestore.ErrNotFound)
	}
	if err != nil {
		return ValidatorRunArtifact{}, err
	}
	result.AttemptID = attempt.String
	if err := decodeCanonical(payload, &result.Receipt); err != nil {
		return ValidatorRunArtifact{}, err
	}
	hash, err := result.Receipt.Hash()
	if err != nil || hash != result.Hash || result.Receipt.ID != id || result.Receipt.DefinitionHash != definitionHash ||
		result.Receipt.GoalRevisionHash != goalHash || result.Receipt.ConfigHash != configHash ||
		result.Receipt.EnvironmentHash != environmentHash || result.Receipt.TreeHash != treeHash || string(result.Receipt.Result) != commandResult {
		return ValidatorRunArtifact{}, errors.New("persisted validator run violates its receipt contract")
	}
	registered, err := validatorRegistrationExists(ctx, queryer, result.Receipt.ConfigHash, result.Receipt.ValidatorID, result.Receipt.DefinitionHash)
	if err != nil || !registered {
		return ValidatorRunArtifact{}, errors.New("persisted validator run has no matching frozen registry registration")
	}
	definition, err := readValidatorDefinitionArtifact(ctx, queryer, result.Receipt.DefinitionHash)
	if err != nil || definition.Definition.ID != result.Receipt.ValidatorID {
		return ValidatorRunArtifact{}, errors.New("persisted validator run does not match its immutable definition")
	}
	result.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil || !result.CreatedAt.Equal(result.Receipt.FinishedAt) {
		return ValidatorRunArtifact{}, errors.New("persisted validator run time is invalid")
	}
	if verifyDisk {
		disk, err := validator.ReadReceipt(runtimeRoot, id)
		if err != nil {
			return ValidatorRunArtifact{}, err
		}
		diskHash, err := disk.Hash()
		if err != nil || diskHash != result.Hash {
			return ValidatorRunArtifact{}, errors.New("persisted validator run does not match its immutable receipt")
		}
	}
	return result, nil
}

func decodeCanonical(content []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("persisted artifact contains trailing data")
	}
	want, err := canonical.Marshal(destination)
	if err != nil || !bytes.Equal(content, want) {
		return errors.New("persisted artifact is not canonical")
	}
	return nil
}

func sameWorkspaceMarker(left, right workspace.Snapshot) bool {
	return left.ID == right.ID && left.AttemptID == right.AttemptID && left.Kind == right.Kind && left.Path == right.Path && left.MarkerPath == right.MarkerPath && left.CommonDir == right.CommonDir && left.BaseCommit == right.BaseCommit && left.BaseTree == right.BaseTree && left.ConfigHash == right.ConfigHash && left.MarkerHash == right.MarkerHash && left.CreatedAt.Equal(right.CreatedAt) && left.ExecutionModel == right.ExecutionModel && left.InputTree == right.InputTree && left.Identity == right.Identity && equalStrings(left.ExcludePaths, right.ExcludePaths)
}
func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
func validateWorkspaceArtifactLayout(runtimeRoot string, snapshot workspace.Snapshot) error {
	canonicalRuntime, err := canonicalArtifactDirectory(runtimeRoot)
	if err != nil {
		return err
	}
	directory := "attempts"
	if snapshot.Kind == workspace.Validation {
		directory = "validation"
	}
	container := filepath.Join(canonicalRuntime, "workspaces", directory, snapshot.ID)
	if snapshot.ExecutionModel != workspace.ExecutionCurrentDirectory {
		return workspace.ErrLegacyWorkspace
	}
	if snapshot.MarkerPath != filepath.Join(container, "marker.json") || snapshot.Path != snapshot.Identity.Root || snapshot.CommonDir != snapshot.Identity.CommonDir || !equalStrings(snapshot.ExcludePaths, []string{canonicalRuntime}) {
		return errors.New("workspace artifact does not match current-directory runtime layout")
	}
	canonicalContainer, err := canonicalArtifactDirectory(container)
	if err != nil || canonicalContainer != container {
		return errors.New("workspace metadata directory is missing or linked")
	}
	canonicalPath, err := canonicalArtifactDirectory(snapshot.Path)
	if err != nil || canonicalPath != snapshot.Path {
		return errors.New("workspace execution directory is missing or linked")
	}
	return nil
}

func sameValidatorDefinition(left, right validator.Definition) bool {
	leftJSON, leftErr := canonical.Marshal(left)
	rightJSON, rightErr := canonical.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func artifactSHA256(value string) bool {
	return artifactHex(value, 64)
}

func artifactObjectID(value string) bool {
	return artifactHex(value, 40) || artifactHex(value, 64)
}

func artifactHex(value string, length int) bool {
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

func canonicalArtifactDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("artifact path must be a clean absolute path")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("artifact directory is missing")
	}
	return resolved, nil
}

func requireOneArtifactRow(result sql.Result, aggregate, id string) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return fmt.Errorf("%s artifact %q: %w", aggregate, id, basestore.ErrConflict)
	}
	return nil
}
