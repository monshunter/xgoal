// Package harness inventories project knowledge without installing governance
// files or owning project/Goal state. Native providers still load business rules.
package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/scope"
)

var ErrRequired = errors.New("PROJECT_HARNESS_REQUIRED")

type manifest struct {
	Owner         string            `json:"owner"`
	SchemaVersion int               `json:"schema_version"`
	Version       string            `json:"autogo_version"`
	Pack          string            `json:"harness_pack"`
	Agent         string            `json:"agent"`
	Files         []string          `json:"managed_files"`
	Blocks        map[string]string `json:"managed_blocks"`
}

func Discover(projectRoot, provider string, requirement config.Harness) (input protocol.HarnessInput, resultErr error) {
	input = protocol.HarnessInput{Type: requirement.Type, Provider: provider, Required: requirement.Required, Compatible: !requirement.Required, Files: []protocol.InstructionFile{}, Delegation: protocol.DelegationInstructions, Delivery: "packet-path-references", LoadObservation: "unknown"}
	defer func() {
		if resultErr != nil && requirement.Required {
			input.Compatible = false
			if !errors.Is(resultErr, ErrRequired) {
				input.Diagnostics = append(input.Diagnostics, resultErr.Error())
				resultErr = errors.Join(ErrRequired, resultErr)
			}
		}
	}()
	agent, entry, skills := "", "", ""
	switch provider {
	case "codex-cli":
		agent, entry, skills = "codex", "AGENTS.md", ".agents/skills/"
	case "claude-cli":
		agent, entry, skills = "claude", "CLAUDE.md", ".claude/skills/"
	}
	finish := func() (protocol.HarnessInput, error) {
		sort.Slice(input.Files, func(i, j int) bool { return input.Files[i].Path < input.Files[j].Path })
		if requirement.Required && !input.Compatible {
			return input, fmt.Errorf("%w: install or reconcile project-local AutoGo core for %s (%s and .autogo/manifests/%s.json), then run xgoal doctor: %s", ErrRequired, provider, entry, agent, strings.Join(input.Diagnostics, "; "))
		}
		return input, nil
	}
	if agent == "" {
		input.Diagnostics = append(input.Diagnostics, "provider has no supported project Harness discovery contract")
		return finish()
	}
	root, err := os.OpenRoot(projectRoot)
	if err != nil {
		return input, err
	}
	defer root.Close()
	remaining := int64(8 << 20)
	seen := map[string]bool{}
	read := func(name, kind string) ([]byte, error) {
		if seen[name] {
			return nil, nil
		}
		if len(seen) >= 256 {
			return nil, errors.New("project Harness inventory exceeds 256 files")
		}
		body, err := readKnowledge(root, name, min(remaining, 1<<20))
		if err != nil {
			return nil, err
		}
		remaining -= int64(len(body))
		seen[name] = true
		digest := sha256.Sum256(body)
		input.Files = append(input.Files, protocol.InstructionFile{Path: name, Kind: kind, SHA256: hex.EncodeToString(digest[:])})
		return body, nil
	}
	entryFound := false
	if _, err := read(entry, "provider-entry"); err == nil {
		entryFound = true
		input.Found = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return input, err
	}
	// AGENTS.md may contain useful project knowledge for a Claude installation,
	// but is never substituted for Claude's native entrypoint.
	if entry != "AGENTS.md" {
		if _, err := read("AGENTS.md", "project-rule"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return input, err
		}
	}
	manifestPath := ".autogo/manifests/" + agent + ".json"
	body, err := read(manifestPath, "manifest")
	if errors.Is(err, os.ErrNotExist) {
		input.Diagnostics = append(input.Diagnostics, "native AutoGo manifest is absent")
		input.Diagnostics = append(input.Diagnostics, fmt.Sprintf("To prepare optional AutoGo support, install the project-local core for %s with %s, %s and %s, then run xgoal doctor", provider, entry, skills, manifestPath))
		if requirement.Type == "autogo" {
			input.Compatible = false
		}
		return finish()
	}
	if err != nil {
		return input, err
	}
	input.Found = true
	input.Type = "autogo"
	input.Compatible = false
	var installed manifest
	if err := json.Unmarshal(body, &installed); err != nil || installed.Owner != "autogo" || installed.SchemaVersion != 3 || installed.Agent != agent || installed.Pack != "core" || installed.Version == "" {
		input.Diagnostics = append(input.Diagnostics, "manifest requires owner autogo, schema_version 3, harness_pack core, version and matching native agent")
		return finish()
	}
	if len(installed.Files)+len(installed.Blocks) > 254 {
		return input, errors.New("project Harness manifest exceeds 254 declared references")
	}
	if !entryFound {
		input.Diagnostics = append(input.Diagnostics, "native instruction entrypoint is absent")
	}
	paths := append([]string(nil), installed.Files...)
	for filename := range installed.Blocks {
		paths = append(paths, filename)
	}
	sort.Strings(paths)
	skillCount := 0
	for _, filename := range paths {
		if !knowledgePath(filename) {
			return input, fmt.Errorf("unsafe or unsupported Harness knowledge reference %q", filename)
		}
		kind := "knowledge"
		isSkill := strings.HasPrefix(filename, skills) && strings.HasSuffix(filename, "/SKILL.md")
		if isSkill {
			kind = "skill"
		}
		if _, err := read(filename, kind); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return input, err
			}
			input.Diagnostics = append(input.Diagnostics, "declared knowledge file is absent: "+filename)
		} else if isSkill {
			skillCount++
		}
	}
	if skillCount == 0 {
		input.Diagnostics = append(input.Diagnostics, "no native project skill entrypoint is available")
	}
	input.Compatible = len(input.Diagnostics) == 0
	return finish()
}

func knowledgePath(filename string) bool {
	canonical, err := scope.NormalizeRepositoryPath(filename)
	if err != nil || canonical != filename {
		return false
	}
	if filename == "AGENTS.md" || filename == "CLAUDE.md" || filename == "README.md" {
		return true
	}
	return strings.HasPrefix(filename, "docs/") || strings.HasPrefix(filename, ".autogo/") || strings.HasPrefix(filename, ".agents/skills/") || strings.HasPrefix(filename, ".claude/skills/")
}

func readKnowledge(root *os.Root, filename string, limit int64) ([]byte, error) {
	canonical, err := scope.NormalizeRepositoryPath(filename)
	if err != nil || canonical != filename {
		return nil, fmt.Errorf("unsafe Harness path %q", filename)
	}
	// Root prevents escapes during open; checking every component also rejects
	// in-repository symlinks, so inventory identity is an exact regular file.
	parts := strings.Split(filename, "/")
	var expected os.FileInfo
	for i := range parts {
		info, err := root.Lstat(path.Join(parts[:i+1]...))
		expected = info
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) || (i == len(parts)-1 && !info.Mode().IsRegular()) {
			return nil, fmt.Errorf("Harness file %q is linked or not regular", filename)
		}
	}
	file, err := root.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(expected, info) || info.Size() > limit || limit <= 0 {
		return nil, fmt.Errorf("Harness file %q exceeds bounded inventory size", filename)
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) != info.Size() || int64(len(body)) > limit {
		return nil, fmt.Errorf("Harness file %q changed during inventory", filename)
	}
	current, err := root.Lstat(filename)
	if err != nil || !os.SameFile(info, current) || current.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("Harness file %q changed during inventory", filename)
	}
	return body, nil
}
