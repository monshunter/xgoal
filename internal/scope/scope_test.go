package scope_test

import (
	"errors"
	"testing"

	"github.com/monshunter/xgoal/internal/scope"
)

func TestCanonicalizePathsRejectsNFCAndCaseFoldCollisions(t *testing.T) {
	t.Parallel()

	if got, err := scope.NormalizeRepositoryPath("docs/cafe\u0301.txt"); err != nil || got != "docs/caf\u00e9.txt" {
		t.Fatalf("NormalizeRepositoryPath() = %q, %v", got, err)
	}
	for name, paths := range map[string][]string{
		"nfc":            {"docs/caf\u00e9.txt", "docs/cafe\u0301.txt"},
		"case":           {"Stra\u00dfe.txt", "STRASSE.txt"},
		"parent-case":    {"Source/one.txt", "source/two.txt"},
		"parent-nfc":     {"caf\u00e9/one.txt", "cafe\u0301/two.txt"},
		"same-parent-ok": nil,
	} {
		paths := paths
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if paths == nil {
				if _, err := scope.CanonicalizePaths([]string{"docs/one.txt", "docs/two.txt"}); err != nil {
					t.Fatalf("CanonicalizePaths(same parent) error = %v", err)
				}
				return
			}
			if _, err := scope.CanonicalizePaths(paths); !errors.Is(err, scope.ErrCollision) {
				t.Fatalf("CanonicalizePaths() error = %v, want ErrCollision", err)
			}
		})
	}
}

func TestPolicyUsesRootedGlobAndDenyPrecedence(t *testing.T) {
	t.Parallel()

	policy, err := scope.NewPolicy([]string{"/internal/**", "/README.*"}, []string{"/internal/private/**"})
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.CheckWrite([]string{"internal/store/store.go", "README.md"}); err != nil {
		t.Fatalf("CheckWrite() error = %v", err)
	}
	for _, paths := range [][]string{{"pkg/outside.go"}, {"internal/private/key.txt"}} {
		if err := policy.CheckWrite(paths); !errors.Is(err, scope.ErrViolation) {
			t.Fatalf("CheckWrite(%v) error = %v, want ErrViolation", paths, err)
		}
	}
	if _, err := scope.NewPolicy([]string{"internal/**"}, nil); err == nil {
		t.Fatal("NewPolicy() accepted an unrooted pattern")
	}
	if _, err := scope.NewPolicy([]string{"/internal/a**b"}, nil); err == nil {
		t.Fatal("NewPolicy() accepted a partial ** segment")
	}
}

func TestPathAndSymlinkSafety(t *testing.T) {
	t.Parallel()

	for _, candidate := range []string{"../escape", "/absolute", "nested/.GIT/config", "bad\\path", "a//b"} {
		if _, err := scope.NormalizeRepositoryPath(candidate); !errors.Is(err, scope.ErrUnsafePath) {
			t.Errorf("NormalizeRepositoryPath(%q) error = %v, want ErrUnsafePath", candidate, err)
		}
	}
	for _, target := range []string{"../../outside", "/absolute", ".git/config", "dir/../../.git/config"} {
		if err := scope.ValidateSymlinkTarget("nested/link", target); !errors.Is(err, scope.ErrUnsafePath) {
			t.Errorf("ValidateSymlinkTarget(%q) error = %v, want ErrUnsafePath", target, err)
		}
	}
	if err := scope.ValidateSymlinkTarget("nested/link", "../README.md"); err != nil {
		t.Fatalf("ValidateSymlinkTarget(safe) error = %v", err)
	}
}
