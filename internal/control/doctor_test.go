package control_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/monshunter/xgoal/internal/clock"
	"github.com/monshunter/xgoal/internal/control"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

func TestPassiveDoctorDoesNotCreateAdaptersOrFollowGitEnvironment(t *testing.T) {
	ctx := context.Background()
	root, other := t.TempDir(), t.TempDir()
	for _, dir := range []string{root, other} {
		if out, err := exec.Command("git", "-C", dir, "init", "--quiet").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
	}
	data, err := os.ReadFile("../../xgoal.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, ".xgoal")
	store, err := sqlite.Open(ctx, state, clock.Real{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service, err := control.New(store, root)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	result := service.Doctor(ctx)
	after, err := os.ReadDir(state)
	if err != nil {
		t.Fatal(err)
	}
	names := func(entries []os.DirEntry) []string {
		var out []string
		for _, entry := range entries {
			out = append(out, entry.Name())
		}
		return out
	}
	if !reflect.DeepEqual(names(before), names(after)) {
		t.Fatalf("doctor changed runtime directories: %v -> %v", names(before), names(after))
	}
	canonical, _ := filepath.EvalSymlinks(root)
	facts := result["git"].(map[string]any)
	if facts["top_level"] != canonical {
		t.Fatalf("doctor followed another repository: %+v", facts)
	}
	if result["model_calls"] != 0 {
		t.Fatalf("doctor: %+v", result)
	}
}
