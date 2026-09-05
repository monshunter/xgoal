package validator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/gitrepo"
	"github.com/monshunter/xgoal/internal/scope"
)

var ErrTrustedFileChanged = errors.New("trusted validator file differs from frozen baseline; review and commit the new trust baseline, then create a new Goal; approve/replan cannot rebind old evidence")

type TrustedFile struct {
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256"`
}

// ProtectedPaths includes dependencies of every Validator, including validators
// not selected by this Work. Otherwise an earlier Work could weaken a later one.
func (registry *Registry) ProtectedPaths() []string {
	paths := map[string]bool{"xgoal.yaml": true}
	for _, d := range registry.definitions {
		if d.TrustedExecutablePath != "" {
			paths[d.TrustedExecutablePath] = true
		}
		for _, file := range d.TrustedFiles {
			paths[file.Path] = true
		}
	}
	result := make([]string, 0, len(paths))
	for value := range paths {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func bindTrustedFiles(ctx context.Context, repo *gitrepo.Repository, base string, cfg config.Validator, definition *Definition) error {
	files := make(map[string]bool) // value requires executable Git mode
	for _, file := range cfg.TrustedFiles {
		files[file] = false
	}
	command := cfg.Argv[0]
	if strings.Contains(command, "/") {
		if !strings.HasPrefix(command, "./") {
			return errors.New("repository executable must use a ./ relative path")
		}
		entry, err := entryPath(definition.CWD, command)
		if err != nil {
			return err
		}
		files[entry] = true
		definition.TrustedExecutablePath = entry
	} else {
		switch command {
		case "sh", "bash", "dash", "zsh", "python", "python3", "node", "ruby", "perl":
			args := cfg.Argv[1:]
			if len(args) > 0 && args[0] == "--" {
				args = args[1:]
			}
			if len(args) > 0 && args[0] != "-" && !strings.HasPrefix(args[0], "-") {
				entry, err := entryPath(definition.CWD, args[0])
				if err != nil {
					return err
				}
				if _, exists := files[entry]; !exists {
					files[entry] = false
				}
			} else if len(cfg.TrustedFiles) == 0 {
				return errors.New("TRUST_DECLARATION_REQUIRED: interpreter options/inline code require explicit trustedFiles; self-contained inline assertions may declare [xgoal.yaml]")
			}
		default:
			if len(cfg.TrustedFiles) == 0 && !builtinAssertion(cfg.Argv) {
				return errors.New("TRUST_DECLARATION_REQUIRED: unrecognized runner, wrapper or build command requires explicit trustedFiles for its control entrypoints and dependencies")
			}
		}
	}
	for filename, executable := range files {
		canonical, err := scope.NormalizeRepositoryPath(filename)
		if err != nil || canonical != filename {
			return fmt.Errorf("invalid trusted file path %q", filename)
		}
		entry, content, err := repo.ReadFileAtRevision(ctx, base, filename, maxScriptBytes)
		if err != nil {
			return fmt.Errorf("read trusted file %q: %w", filename, err)
		}
		if (entry.Mode != "100644" && entry.Mode != "100755") || (executable && entry.Mode != "100755") {
			return fmt.Errorf("trusted file %q has unsupported Git mode %s", filename, entry.Mode)
		}
		digest := sha256.Sum256(content)
		hash := hex.EncodeToString(digest[:])
		definition.TrustedFiles = append(definition.TrustedFiles, TrustedFile{Path: filename, Mode: entry.Mode, SHA256: hash})
		if filename == definition.TrustedExecutablePath {
			definition.TrustedExecutableHash = hash
		}
	}
	sort.Slice(definition.TrustedFiles, func(i, j int) bool { return definition.TrustedFiles[i].Path < definition.TrustedFiles[j].Path })
	return nil
}

// Only these host tools have a known assertion entrypoint with no repository
// control script to infer. Go test sources remain the candidate under test;
// custom launchers, wrappers and versioned interpreters require a declaration.
// Host executables/configuration are part of the trusted local environment.
func builtinAssertion(argv []string) bool {
	switch argv[0] {
	case "test", "true", "false", "sleep":
		return true
	case "git":
		return len(argv) == 3 && argv[1] == "diff" && argv[2] == "--check"
	case "go":
		if len(argv) < 2 || (argv[1] != "test" && argv[1] != "vet" && argv[1] != "version") {
			return false
		}
		for _, arg := range argv[2:] {
			if strings.HasPrefix(arg, "-exec") || strings.HasPrefix(arg, "-toolexec") || strings.HasPrefix(arg, "-vettool") {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func entryPath(cwd, entry string) (string, error) {
	entry = strings.TrimPrefix(entry, "./")
	canonical, err := scope.NormalizeRepositoryPath(entry)
	if err != nil || canonical != entry {
		return "", fmt.Errorf("invalid trusted entrypoint %q", entry)
	}
	return scope.NormalizeRepositoryPath(path.Join(cwd, entry))
}

func verifyTrustedFiles(root string, definition Definition) error {
	for _, binding := range definition.TrustedFiles {
		filename := filepath.Join(root, filepath.FromSlash(binding.Path))
		if err := rejectExecutableSymlinkParents(root, filename); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrTrustedFileChanged, binding.Path, err)
		}
		info, err := os.Lstat(filename)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maxScriptBytes || ((info.Mode().Perm()&0111 != 0) != (binding.Mode == "100755")) {
			return fmt.Errorf("%w: %s mode or size", ErrTrustedFileChanged, binding.Path)
		}
		file, err := os.Open(filename)
		if err != nil {
			return fmt.Errorf("%w: %s open", ErrTrustedFileChanged, binding.Path)
		}
		opened, statErr := file.Stat()
		content, readErr := io.ReadAll(io.LimitReader(file, maxScriptBytes+1))
		closeErr := file.Close()
		if statErr != nil || !os.SameFile(info, opened) || readErr != nil || closeErr != nil || int64(len(content)) != info.Size() {
			return fmt.Errorf("%w: %s changed while reading", ErrTrustedFileChanged, binding.Path)
		}
		digest := sha256.Sum256(content)
		if hex.EncodeToString(digest[:]) != binding.SHA256 {
			return fmt.Errorf("%w: %s content", ErrTrustedFileChanged, binding.Path)
		}
	}
	return nil
}
