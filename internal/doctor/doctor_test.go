package doctor_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/app"
	"github.com/monshunter/xgoal/internal/doctor"
)

func TestPassiveDoctorIgnoresForeignGitEnvironment(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "xgoal-doctor-env-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	for _, dir := range []string{a, b} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		doctorGit(t, dir, "init", "-b", "main")
		doctorGit(t, dir, "-c", "user.name=xgoal", "-c", "user.email=xgoal@example.invalid", "commit", "--allow-empty", "-m", filepath.Base(dir))
	}
	expected := doctorGit(t, a, "rev-parse", "HEAD")
	if expected == doctorGit(t, b, "rev-parse", "HEAD") {
		t.Fatal("fixture commits must differ")
	}
	paths, err := app.ResolvePaths(a, "", "")
	if err != nil {
		t.Fatal(err)
	}
	gitBinary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(gitBinary, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("GIT_DIR", filepath.Join(b, ".git"))
	t.Setenv("GIT_WORK_TREE", b)
	t.Setenv("GIT_INDEX_FILE", filepath.Join(b, ".git", "index"))
	actual := doctor.Inspect(context.Background(), paths)
	if actual["git"].(map[string]any)["head"] != expected {
		t.Fatalf("doctor used another repository: %#v", actual["git"])
	}
	if _, err := os.Stat(paths.StateDir); !os.IsNotExist(err) {
		t.Fatalf("passive doctor created state: %v", err)
	}
}

func TestOfflineDoctorDistinguishesLegacyConfigFromUnknownErrors(t *testing.T) {
	root := t.TempDir()
	doctorGit(t, root, "init", "-b", "main")
	paths, err := app.ResolvePaths(root, "", "")
	if err != nil {
		t.Fatal(err)
	}
	configuration, err := os.ReadFile("../../xgoal.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(string(configuration), "provider: current-directory", "provider: git-worktree", 1)
	gitBinary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(gitBinary, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	for _, invalid := range []bool{false, true} {
		input := legacy
		if invalid {
			input += "\nunknownField: true\n"
		}
		if err := os.WriteFile(filepath.Join(root, "xgoal.yaml"), []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		result := doctor.Inspect(context.Background(), paths)
		if result["configuration_compatible"] != false || result["config_migration_required"] != !invalid {
			t.Fatalf("incorrect migration classification: %+v", result)
		}
		want := "CONFIG_MIGRATION_REQUIRED"
		if invalid {
			want = "unknownField"
		}
		if !strings.Contains(result["config_error"].(string), want) || result["store"].(map[string]any)["opened"] != false {
			t.Fatalf("missing passive diagnosis: %+v", result)
		}
		if _, err := os.Stat(paths.StateDir); !os.IsNotExist(err) {
			t.Fatalf("offline migration diagnosis created state: %v", err)
		}
		if actual, err := os.ReadFile(filepath.Join(root, "xgoal.yaml")); err != nil || string(actual) != input {
			t.Fatalf("offline diagnosis rewrote configuration: %q %v", actual, err)
		}
	}
}

func doctorGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git: %v %s", err, output)
	}
	return strings.TrimSpace(string(output))
}
