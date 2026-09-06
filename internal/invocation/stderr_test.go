package invocation

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStderrIsDurableBeforeCloseAndNeverWritesPartialSecrets(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	log := NewStderrLog(root, root)
	if _, err := log.Write([]byte("api_key=split-")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "stderr/events/000001.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial line persisted")
	}
	if _, err := log.Write([]byte("sentinel\nvisible diagnostic\n")); err != nil {
		t.Fatal(err)
	}
	// Read without Close, as if the daemon was killed after the last write.
	page, err := ReadPage(context.Background(), root, "stderr", 0, 2, 10)
	if err != nil || len(page) != 2 || strings.Contains(string(page[0].Data), "sentinel") || !strings.Contains(string(page[1].Data), "visible diagnostic") {
		t.Fatalf("live: %+v %v", page, err)
	}
	if _, err := log.Write(bytes.Repeat([]byte("x"), maxStderrLine+1)); err == nil {
		t.Fatal("long line accepted")
	}
	data, _, err := ReadFile(root, "stderr-status.json", 4096)
	if err != nil || !strings.Contains(string(data), `"truncated":true`) {
		t.Fatalf("marker: %s %v", data, err)
	}
	if err := log.Close(); err == nil {
		t.Fatal("failure lost on close")
	}
}
func TestStderrIOFailureIsReported(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	os.WriteFile(filepath.Join(root, "stderr"), []byte("obstruction"), 0600)
	log := NewStderrLog(root, root)
	if _, err := log.Write([]byte("visible\n")); err == nil {
		t.Fatal("I/O failure swallowed")
	}
	if _, err := os.Stat(filepath.Join(root, "stderr-status.json")); err != nil {
		t.Fatal(err)
	}
}

func TestLogWritesRejectIntermediateSymlinksWithoutExternalFiles(t *testing.T) {
	for _, kind := range []string{"stderr-parent", "provider-parent"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			os.Chmod(root, 0700)
			outside := t.TempDir()
			os.Chmod(outside, 0700)
			dir := filepath.Join(root, "adapters", "codex", "invocations", "invoke_1")
			if kind == "stderr-parent" {
				os.MkdirAll(dir, 0700)
				os.Symlink(outside, filepath.Join(dir, "stderr"))
			} else {
				os.Mkdir(filepath.Join(root, "adapters"), 0700)
				os.Symlink(outside, filepath.Join(root, "adapters", "codex"))
			}
			log := NewStderrLog(root, dir)
			if _, err := log.Write([]byte("diagnostic\n")); err == nil {
				t.Fatal("linked parent accepted")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("wrote outside runtime: %v %v", entries, err)
			}
		})
	}
}
