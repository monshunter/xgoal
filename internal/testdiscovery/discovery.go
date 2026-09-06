// Package testdiscovery reports existing test entrypoints without executing
// project code, installing dependencies or changing validator authority.
package testdiscovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"syscall"

	"github.com/monshunter/xgoal/internal/config"
)

type Entry struct {
	Source           string   `json:"source"`
	Argv             []string `json:"suggested_argv"`
	CommandAvailable bool     `json:"command_available"`
	Configured       bool     `json:"configured"`
}

type Result struct {
	GeneratedValidationPolicy string                       `json:"generated_validation_policy"`
	Status                    string                       `json:"status"`
	Entries                   []Entry                      `json:"entries"`
	ConfiguredValidators      []config.ValidatorCapability `json:"configured_validators"`
	Coverage                  string                       `json:"coverage"`
	Preparation               []string                     `json:"preparation"`
	Diagnostics               []string                     `json:"diagnostics"`
}

func Inspect(projectRoot string, configuration *config.Config) Result {
	r := Result{Status: "unknown", Entries: []Entry{}, ConfiguredValidators: []config.ValidatorCapability{}, Coverage: "not_verified", Diagnostics: []string{}, GeneratedValidationPolicy: "allow", Preparation: []string{
		"Provide the goal, hard constraints and necessary environment authorization. Acceptance criteria and tests are optional inputs.",
		"The Planner derives missing acceptance criteria and executable checks, freezes them with the Goal, then implementation, independent review and final validation use that baseline.",
		"Optionally pass --acceptance-file or configure planning.acceptanceFiles for existing scripts or Markdown requirements; project validators remain authoritative.",
	}}
	if configuration != nil {
		r.ConfiguredValidators = configuration.ValidationCapabilities().Validators
		r.GeneratedValidationPolicy = configuration.GeneratedValidationPolicy()
	}
	switch r.GeneratedValidationPolicy {
	case "human-gate":
		r.Preparation = append(r.Preparation, "This project requires one explicit approval of the exact generated acceptance plan before implementation; inspect its context and use approve --resume.")
	case "deny":
		r.Preparation[1] = "Generated checks are disabled by project policy. Provide trusted business validators, or explicitly change planning.generatedValidators before starting a new Goal."
	}

	root, err := os.OpenRoot(projectRoot)
	if err != nil {
		r.Diagnostics = append(r.Diagnostics, "project root is unavailable")
		return r
	}
	defer root.Close()
	read := func(name string) []byte {
		f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			r.Diagnostics = append(r.Diagnostics, name+": unavailable or linked")
			return nil
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			r.Diagnostics = append(r.Diagnostics, name+": not a bounded regular file")
			return nil
		}
		data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		if err != nil || len(data) > 1<<20 {
			r.Diagnostics = append(r.Diagnostics, name+": cannot read within limit")
			return nil
		}
		return data
	}
	add := func(source string, argv ...string) {
		_, err := exec.LookPath(argv[0])
		entry := Entry{Source: source, Argv: argv, CommandAvailable: err == nil}
		if configuration != nil {
			for _, v := range configuration.Validators {
				if v.CWD == "" && slices.Equal(v.Argv, argv) {
					entry.Configured = true
				}
			}
		}
		r.Entries = append(r.Entries, entry)
	}
	if read("go.mod") != nil {
		add("go.mod", "go", "test", "./...")
	}
	if data := read("package.json"); data != nil {
		var p struct {
			Scripts        map[string]string `json:"scripts"`
			PackageManager string            `json:"packageManager"`
		}
		if json.Unmarshal(data, &p) != nil {
			r.Diagnostics = append(r.Diagnostics, "package.json: invalid package metadata")
		} else if strings.TrimSpace(p.Scripts["test"]) != "" && !strings.Contains(p.Scripts["test"], "no test specified") {
			manager := "npm"
			if strings.HasPrefix(p.PackageManager, "pnpm@") {
				manager = "pnpm"
			} else if strings.HasPrefix(p.PackageManager, "yarn@") {
				manager = "yarn"
			}
			add("package.json scripts.test", manager, "test")
		}
	}
	if read("Cargo.toml") != nil {
		add("Cargo.toml", "cargo", "test")
	}
	pytest := read("pytest.ini") != nil
	if data := read("pyproject.toml"); data != nil && bytes.Contains(data, []byte("[tool.pytest.ini_options]")) {
		pytest = true
	}
	if pytest {
		add("pytest configuration", "python3", "-m", "pytest")
	}
	if data := read("Makefile"); data != nil && regexp.MustCompile(`(?m)^test[ \t]*:`).Match(data) {
		add("Makefile test target", "make", "test")
	}
	if len(r.Entries) > 0 {
		r.Status = "entrypoints_detected"
	} else {
		r.Preparation = append([]string{"No recognized test entrypoint was found. git-diff-check only checks whitespace; it does not establish business coverage. Missing checks are prepared during planning when project policy permits."}, r.Preparation...)
	}
	return r
}
