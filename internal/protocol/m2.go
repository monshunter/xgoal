package protocol

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/canonical"
	"golang.org/x/text/unicode/norm"
)

const (
	PatchBundleVersion         = "xgoal.patch-bundle/v1alpha1"
	CommandReceiptVersion      = "xgoal.command-receipt/v1alpha1"
	EnvironmentSnapshotVersion = "xgoal.environment-snapshot/v1alpha1"

	SchemaPatchBundle         = "patch-bundle"
	SchemaCommandReceipt      = "command-receipt"
	SchemaEnvironmentSnapshot = "environment-snapshot"
)

type PatchKind string

const (
	PatchAdded    PatchKind = "added"
	PatchModified PatchKind = "modified"
	PatchDeleted  PatchKind = "deleted"
	PatchRenamed  PatchKind = "renamed"
)

type PatchBundle struct {
	ProtocolVersion string        `json:"protocol_version"`
	AttemptID       string        `json:"attempt_id"`
	BaseCommit      string        `json:"base_commit"`
	BaseTree        string        `json:"base_tree"`
	Entries         []PatchEntry  `json:"entries"`
	Objects         []PatchObject `json:"objects"`
	ManifestHash    string        `json:"manifest_hash"`
	BundleHash      string        `json:"bundle_hash"`
}

type PatchEntry struct {
	Path              string    `json:"path"`
	PathBefore        string    `json:"path_before,omitempty"`
	Kind              PatchKind `json:"kind"`
	ModeBefore        string    `json:"mode_before,omitempty"`
	ModeAfter         string    `json:"mode_after,omitempty"`
	ContentHashBefore string    `json:"content_hash_before,omitempty"`
	ContentHashAfter  string    `json:"content_hash_after,omitempty"`
	ObjectRef         string    `json:"object_ref,omitempty"`
}

type PatchObject struct {
	Ref    string `json:"ref"`
	Hash   string `json:"hash"`
	Length int64  `json:"length"`
}

type patchManifest struct {
	ProtocolVersion string       `json:"protocol_version"`
	AttemptID       string       `json:"attempt_id"`
	BaseCommit      string       `json:"base_commit"`
	BaseTree        string       `json:"base_tree"`
	Entries         []PatchEntry `json:"entries"`
}

type patchBundleIdentity struct {
	ProtocolVersion string        `json:"protocol_version"`
	ManifestHash    string        `json:"manifest_hash"`
	Objects         []PatchObject `json:"objects"`
}

func (bundle PatchBundle) Seal() (PatchBundle, error) {
	bundle.ManifestHash = ""
	bundle.BundleHash = ""
	if err := bundle.validateStructure(); err != nil {
		return PatchBundle{}, err
	}
	manifestHash, err := bundle.computeManifestHash()
	if err != nil {
		return PatchBundle{}, err
	}
	bundle.ManifestHash = manifestHash
	bundleHash, err := bundle.computeBundleHash()
	if err != nil {
		return PatchBundle{}, err
	}
	bundle.BundleHash = bundleHash
	return bundle, nil
}

func (bundle PatchBundle) Validate() error {
	if err := bundle.validateStructure(); err != nil {
		return err
	}
	if !validSHA256(bundle.ManifestHash) || !validSHA256(bundle.BundleHash) {
		return errors.New("patch manifest_hash and bundle_hash must be lowercase SHA-256")
	}
	manifestHash, err := bundle.computeManifestHash()
	if err != nil {
		return err
	}
	if manifestHash != bundle.ManifestHash {
		return errors.New("patch manifest_hash does not match canonical manifest")
	}
	bundleHash, err := bundle.computeBundleHash()
	if err != nil {
		return err
	}
	if bundleHash != bundle.BundleHash {
		return errors.New("patch bundle_hash does not match manifest and objects")
	}
	return nil
}

func (bundle PatchBundle) validateStructure() error {
	if bundle.ProtocolVersion != PatchBundleVersion {
		return fmt.Errorf("protocol_version must be %q", PatchBundleVersion)
	}
	if !validLabel(bundle.AttemptID) || !validLabel(bundle.BaseCommit) || !validLabel(bundle.BaseTree) || len(bundle.Entries) == 0 {
		return errors.New("patch attempt_id, base_commit, base_tree, and entries are required")
	}
	objectByRef := make(map[string]PatchObject, len(bundle.Objects))
	previousRef := ""
	for _, object := range bundle.Objects {
		if !validSHA256(object.Hash) || object.Ref != "objects/sha256/"+object.Hash || object.Length < 0 {
			return fmt.Errorf("invalid patch object %q", object.Ref)
		}
		if previousRef != "" && object.Ref <= previousRef {
			return errors.New("patch objects must be uniquely sorted by ref")
		}
		previousRef = object.Ref
		objectByRef[object.Ref] = object
	}
	usedObjects := make(map[string]struct{}, len(bundle.Objects))
	previousPath := ""
	for _, entry := range bundle.Entries {
		if !validBundlePath(entry.Path) || (previousPath != "" && entry.Path <= previousPath) {
			return errors.New("patch entries must have safe, uniquely sorted paths")
		}
		previousPath = entry.Path
		if err := validatePatchEntry(entry); err != nil {
			return fmt.Errorf("patch entry %q: %w", entry.Path, err)
		}
		if entry.ObjectRef != "" {
			object, exists := objectByRef[entry.ObjectRef]
			if !exists || object.Hash != entry.ContentHashAfter {
				return fmt.Errorf("patch entry %q references a missing or mismatched object", entry.Path)
			}
			usedObjects[entry.ObjectRef] = struct{}{}
		}
	}
	if len(usedObjects) != len(bundle.Objects) {
		return errors.New("patch bundle contains unreferenced objects")
	}
	return nil
}

func validatePatchEntry(entry PatchEntry) error {
	if entry.PathBefore != "" && !validBundlePath(entry.PathBefore) {
		return errors.New("path_before is unsafe")
	}
	switch entry.Kind {
	case PatchAdded:
		if entry.PathBefore != "" || entry.ModeBefore != "" || entry.ContentHashBefore != "" || !validMode(entry.ModeAfter) || !validSHA256(entry.ContentHashAfter) || entry.ObjectRef == "" {
			return errors.New("added entry has inconsistent before/after fields")
		}
	case PatchModified:
		if entry.PathBefore != "" || !validMode(entry.ModeBefore) || !validMode(entry.ModeAfter) || !validSHA256(entry.ContentHashBefore) || !validSHA256(entry.ContentHashAfter) || entry.ObjectRef == "" {
			return errors.New("modified entry has inconsistent before/after fields")
		}
	case PatchDeleted:
		if entry.PathBefore != "" || !validMode(entry.ModeBefore) || !validSHA256(entry.ContentHashBefore) || entry.ModeAfter != "" || entry.ContentHashAfter != "" || entry.ObjectRef != "" {
			return errors.New("deleted entry has inconsistent before/after fields")
		}
	case PatchRenamed:
		if entry.PathBefore == "" || entry.PathBefore == entry.Path || !validMode(entry.ModeBefore) || !validMode(entry.ModeAfter) || !validSHA256(entry.ContentHashBefore) || !validSHA256(entry.ContentHashAfter) || entry.ObjectRef == "" {
			return errors.New("renamed entry has inconsistent before/after fields")
		}
	default:
		return fmt.Errorf("unknown patch kind %q", entry.Kind)
	}
	if entry.ObjectRef != "" && entry.ObjectRef != "objects/sha256/"+entry.ContentHashAfter {
		return errors.New("object_ref does not match content_hash_after")
	}
	return nil
}

func (bundle PatchBundle) computeManifestHash() (string, error) {
	return canonical.Hash(SchemaPatchBundle+"-manifest", PatchBundleVersion, patchManifest{
		ProtocolVersion: bundle.ProtocolVersion,
		AttemptID:       bundle.AttemptID,
		BaseCommit:      bundle.BaseCommit,
		BaseTree:        bundle.BaseTree,
		Entries:         bundle.Entries,
	})
}

func (bundle PatchBundle) computeBundleHash() (string, error) {
	return canonical.Hash(SchemaPatchBundle, PatchBundleVersion, patchBundleIdentity{
		ProtocolVersion: bundle.ProtocolVersion,
		ManifestHash:    bundle.ManifestHash,
		Objects:         bundle.Objects,
	})
}

type CommandResult string

const (
	CommandPassed      CommandResult = "PASSED"
	CommandFailed      CommandResult = "FAILED"
	CommandTimedOut    CommandResult = "TIMED_OUT"
	CommandUnavailable CommandResult = "UNAVAILABLE"
)

type CommandReceipt struct {
	ProtocolVersion  string        `json:"protocol_version"`
	ID               string        `json:"id"`
	ValidatorID      string        `json:"validator_id"`
	DefinitionHash   string        `json:"definition_hash"`
	GoalRevisionHash string        `json:"goal_revision_hash"`
	ConfigHash       string        `json:"config_hash"`
	TreeHash         string        `json:"tree_hash"`
	Argv             []string      `json:"argv"`
	CWD              string        `json:"cwd"`
	EnvironmentHash  string        `json:"environment_hash"`
	StartedAt        time.Time     `json:"started_at"`
	FinishedAt       time.Time     `json:"finished_at"`
	ExitCode         int           `json:"exit_code"`
	StdoutRef        string        `json:"stdout_ref"`
	StderrRef        string        `json:"stderr_ref"`
	OutputHash       string        `json:"output_hash"`
	Result           CommandResult `json:"result"`
}

func (receipt CommandReceipt) Validate() error {
	if receipt.ProtocolVersion != CommandReceiptVersion {
		return fmt.Errorf("protocol_version must be %q", CommandReceiptVersion)
	}
	if !validLabel(receipt.ID) || !validLabel(receipt.ValidatorID) || len(receipt.Argv) == 0 {
		return errors.New("command receipt identity and argv are required")
	}
	for _, value := range []string{receipt.DefinitionHash, receipt.GoalRevisionHash, receipt.ConfigHash, receipt.EnvironmentHash, receipt.OutputHash} {
		if !validSHA256(value) {
			return errors.New("command receipt hashes must be lowercase SHA-256")
		}
	}
	if !validGitObjectID(receipt.TreeHash) {
		return errors.New("command receipt tree_hash must be a lowercase SHA-1 or SHA-256 Git object id")
	}
	for _, argument := range receipt.Argv {
		if argument == "" || strings.ContainsRune(argument, '\x00') || !utf8.ValidString(argument) {
			return errors.New("command receipt argv contains an invalid argument")
		}
	}
	if !validRelativePath(receipt.CWD, true) || !validRelativePath(receipt.StdoutRef, false) || !validRelativePath(receipt.StderrRef, false) {
		return errors.New("command receipt cwd or log refs are unsafe")
	}
	if receipt.StartedAt.IsZero() || receipt.FinishedAt.Before(receipt.StartedAt) || !utcTime(receipt.StartedAt) || !utcTime(receipt.FinishedAt) {
		return errors.New("command receipt times must be ordered UTC values")
	}
	if receipt.Result != CommandPassed && receipt.Result != CommandFailed && receipt.Result != CommandTimedOut && receipt.Result != CommandUnavailable {
		return fmt.Errorf("invalid command result %q", receipt.Result)
	}
	return nil
}

func (receipt CommandReceipt) Hash() (string, error) {
	if err := receipt.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash(SchemaCommandReceipt, CommandReceiptVersion, receipt)
}

type EnvironmentSnapshot struct {
	ProtocolVersion  string            `json:"protocol_version"`
	ID               string            `json:"id"`
	OS               string            `json:"os"`
	Kernel           string            `json:"kernel"`
	Arch             string            `json:"arch"`
	IsolationLevel   string            `json:"isolation_level"`
	GitVersion       string            `json:"git_version"`
	BaseCommit       string            `json:"base_commit"`
	BaseTree         string            `json:"base_tree"`
	ToolVersions     map[string]string `json:"tool_versions"`
	LockfileHashes   map[string]string `json:"lockfile_hashes"`
	ConfigHash       string            `json:"config_hash"`
	GoalRevisionHash string            `json:"goal_revision_hash"`
	EnvironmentNames []string          `json:"environment_names"`
	BootstrapHash    string            `json:"bootstrap_hash,omitempty"`
	CapturedAt       time.Time         `json:"captured_at"`
}

func (snapshot EnvironmentSnapshot) Validate() error {
	if snapshot.ProtocolVersion != EnvironmentSnapshotVersion {
		return fmt.Errorf("protocol_version must be %q", EnvironmentSnapshotVersion)
	}
	for _, value := range []string{snapshot.ID, snapshot.OS, snapshot.Kernel, snapshot.Arch, snapshot.GitVersion, snapshot.BaseCommit, snapshot.BaseTree} {
		if !validLabel(value) {
			return errors.New("environment snapshot identity and platform fields are required")
		}
	}
	if snapshot.IsolationLevel != "L0" {
		return errors.New("environment snapshot isolation_level must be L0 in v0.1")
	}
	if !validSHA256(snapshot.ConfigHash) || !validSHA256(snapshot.GoalRevisionHash) || (snapshot.BootstrapHash != "" && !validSHA256(snapshot.BootstrapHash)) {
		return errors.New("environment snapshot hashes must be lowercase SHA-256")
	}
	if !utcTime(snapshot.CapturedAt) {
		return errors.New("environment snapshot captured_at must be a non-zero UTC value")
	}
	if !sort.StringsAreSorted(snapshot.EnvironmentNames) || !uniqueNonEmpty(snapshot.EnvironmentNames) {
		return errors.New("environment snapshot environment_names must be non-empty, unique, and sorted")
	}
	for key, value := range snapshot.ToolVersions {
		if !validLabel(key) || !validLabel(value) {
			return errors.New("environment snapshot tool_versions contains an invalid entry")
		}
	}
	for filename, hash := range snapshot.LockfileHashes {
		if !validBundlePath(filename) || !validSHA256(hash) {
			return errors.New("environment snapshot lockfile_hashes contains an invalid entry")
		}
	}
	return nil
}

func (snapshot EnvironmentSnapshot) Hash() (string, error) {
	if err := snapshot.Validate(); err != nil {
		return "", err
	}
	return canonical.Hash(SchemaEnvironmentSnapshot, EnvironmentSnapshotVersion, snapshot)
}

func validSHA256(value string) bool {
	return validLowerHex(value, 64)
}

func validGitObjectID(value string) bool {
	return validLowerHex(value, 40) || validLowerHex(value, 64)
}

func validLowerHex(value string, length int) bool {
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

func validMode(value string) bool {
	return value == "100644" || value == "100755" || value == "120000"
}

func validBundlePath(value string) bool {
	if !validRelativePath(value, false) || norm.NFC.String(value) != value || strings.ContainsAny(value, "\r\n") {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if strings.EqualFold(segment, ".git") {
			return false
		}
	}
	return true
}

func validRelativePath(value string, allowDot bool) bool {
	if value == "." && allowDot {
		return true
	}
	return value != "" && utf8.ValidString(value) && !strings.Contains(value, "\\") && !strings.ContainsRune(value, '\x00') &&
		!strings.HasPrefix(value, "/") && path.Clean(value) == value && value != "." && value != ".." && !strings.HasPrefix(value, "../")
}

func validLabel(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func uniqueNonEmpty(values []string) bool {
	previous := ""
	for index, value := range values {
		if !validLabel(value) || (index > 0 && value == previous) {
			return false
		}
		previous = value
	}
	return true
}

func utcTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}
