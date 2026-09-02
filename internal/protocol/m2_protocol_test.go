package protocol_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/protocol"
)

func TestPatchBundleSealsAndRejectsUnsafeOrInconsistentEntries(t *testing.T) {
	bundle := protocol.PatchBundle{
		ProtocolVersion: protocol.PatchBundleVersion,
		AttemptID:       "attempt_1",
		BaseCommit:      "commit-base",
		BaseTree:        "tree-base",
		Entries: []protocol.PatchEntry{
			{
				Path: "internal/a.go", Kind: protocol.PatchModified,
				ModeBefore: "100644", ModeAfter: "100644",
				ContentHashBefore: hashOf('a'), ContentHashAfter: hashOf('b'),
				ObjectRef: "objects/sha256/" + hashOf('b'),
			},
		},
		Objects: []protocol.PatchObject{{Ref: "objects/sha256/" + hashOf('b'), Hash: hashOf('b'), Length: 12}},
	}
	sealed, err := bundle.Seal()
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	if sealed.ManifestHash == "" || sealed.BundleHash == "" || sealed.ManifestHash == sealed.BundleHash {
		t.Fatalf("sealed hashes = %q / %q", sealed.ManifestHash, sealed.BundleHash)
	}
	if err := sealed.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	unsafe := sealed
	unsafe.Entries = append([]protocol.PatchEntry(nil), sealed.Entries...)
	unsafe.Entries[0].Path = "../escape"
	if err := unsafe.Validate(); err == nil {
		t.Fatal("Validate() accepted path escape")
	}
	unsafe.Entries[0].Path = "nested/.GIT/config"
	if err := unsafe.Validate(); err == nil {
		t.Fatal("Validate() accepted nested Git metadata")
	}
	unsafe.Entries[0].Path = "docs/cafe\u0301.txt"
	if err := unsafe.Validate(); err == nil {
		t.Fatal("Validate() accepted a non-NFC patch path")
	}
	inconsistent := bundle
	inconsistent.Entries[0].Kind = protocol.PatchDeleted
	if _, err := inconsistent.Seal(); err == nil {
		t.Fatal("Seal() accepted deleted entry with after object")
	}
}

func TestCommandReceiptAndEnvironmentSnapshotValidateAndHash(t *testing.T) {
	now := time.Date(2026, 9, 2, 20, 0, 0, 0, time.UTC)
	receipt := protocol.CommandReceipt{
		ProtocolVersion: protocol.CommandReceiptVersion,
		ID:              "run_1", ValidatorID: "go-test", DefinitionHash: hashOf('d'), GoalRevisionHash: hashOf('g'),
		ConfigHash: hashOf('c'), TreeHash: hashOf('t'), Argv: []string{"go", "test", "./..."}, CWD: ".",
		EnvironmentHash: hashOf('e'), StartedAt: now, FinishedAt: now.Add(time.Second), ExitCode: 0,
		StdoutRef: "logs/run_1.stdout", StderrRef: "logs/run_1.stderr", OutputHash: hashOf('o'), Result: protocol.CommandPassed,
	}
	first, err := receipt.Hash()
	if err != nil {
		t.Fatalf("CommandReceipt.Hash() error = %v", err)
	}
	if second, err := receipt.Hash(); err != nil || first == "" || first != second {
		t.Fatalf("CommandReceipt.Hash() = %q / %q, %v", first, second, err)
	}
	receipt.Argv = nil
	if err := receipt.Validate(); err == nil {
		t.Fatal("CommandReceipt.Validate() accepted empty argv")
	}

	snapshot := protocol.EnvironmentSnapshot{
		ProtocolVersion: protocol.EnvironmentSnapshotVersion,
		ID:              "env_1", OS: "darwin", Kernel: "Darwin 25", Arch: "arm64", IsolationLevel: "L0",
		GitVersion: "git version 2", BaseCommit: "commit-base", BaseTree: "tree-base",
		ToolVersions: map[string]string{"go": "go1.25.13"}, LockfileHashes: map[string]string{"go.sum": hashOf('l')},
		ConfigHash: hashOf('c'), GoalRevisionHash: hashOf('g'), EnvironmentNames: []string{"GOCACHE", "PATH"},
		CapturedAt: now,
	}
	hash, err := snapshot.Hash()
	if err != nil || hash == "" {
		t.Fatalf("EnvironmentSnapshot.Hash() = %q, %v", hash, err)
	}
	snapshot.EnvironmentNames = []string{"PATH", "GOCACHE"}
	if err := snapshot.Validate(); err == nil {
		t.Fatal("EnvironmentSnapshot.Validate() accepted unsorted environment names")
	}
}

func hashOf(value byte) string {
	return strings.Repeat(fmt.Sprintf("%x", value%16), 64)
}
