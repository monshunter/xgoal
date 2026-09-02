package report

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesRecoverCrashAfterFirstRename(t *testing.T) {
	root := t.TempDir()
	manager, err := NewFileManager(root)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := Render(validReport())
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare("goal-1", artifact)
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected crash")
	manager.afterRename = func(index int) error {
		if index == 0 {
			return injected
		}
		return nil
	}
	if err := manager.Commit(prepared); !errors.Is(err, injected) {
		t.Fatalf("Commit error = %v, want injected crash", err)
	}

	restarted, err := NewFileManager(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Recover(prepared); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	jsonBytes, markdownBytes, err := restarted.Read(prepared)
	if err != nil {
		t.Fatal(err)
	}
	if string(jsonBytes) != string(artifact.JSON) || string(markdownBytes) != string(artifact.Markdown) {
		t.Fatal("recovered report content changed")
	}
	assertMode(t, filepath.Dir(filepath.Join(root, prepared.JSONPath)), 0o700)
	assertMode(t, filepath.Join(root, prepared.JSONPath), 0o600)
}

func TestFilesRecoverRebuildsMissingPayloadFromRecord(t *testing.T) {
	manager, err := NewFileManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	artifact, _ := Render(validReport())
	prepared, err := manager.Prepare("goal-1", artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(manager.root, prepared.JSONTempPath)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(manager.root, prepared.MarkdownTempPath)); err != nil {
		t.Fatal(err)
	}
	if err := manager.Recover(prepared); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Read(prepared); err != nil {
		t.Fatal(err)
	}
}

func TestFilesRejectTamperedCommittedArtifact(t *testing.T) {
	manager, err := NewFileManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	artifact, _ := Render(validReport())
	prepared, err := manager.Prepare("goal-1", artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Commit(prepared); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(manager.root, prepared.JSONPath), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := manager.Read(prepared); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("Read error = %v, want artifact mismatch", err)
	}
	if err := manager.Recover(prepared); !errors.Is(err, ErrArtifactMismatch) {
		t.Fatalf("Recover error = %v, want artifact mismatch", err)
	}
}

func TestFilesRejectUnsafeGoalIDAndSymlink(t *testing.T) {
	root := t.TempDir()
	manager, _ := NewFileManager(root)
	artifact, _ := Render(validReport())
	if _, err := manager.Prepare("../escape", artifact); !errors.Is(err, ErrUnsafeReportPath) {
		t.Fatalf("unsafe goal error = %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "reports")); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Prepare("goal-1", artifact); !errors.Is(err, ErrUnsafeReportPath) {
		t.Fatalf("symlink error = %v", err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("mode %o, want %o", info.Mode().Perm(), want)
	}
}
