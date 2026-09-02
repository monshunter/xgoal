package scope

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var (
	ErrUnsafePath = errors.New("unsafe repository path")
	ErrCollision  = errors.New("repository path collision")
	ErrViolation  = errors.New("write scope violation")
)

var fold = cases.Fold()

// Policy is a compiled, root-anchored write policy. Deny patterns always win.
type Policy struct {
	write []pattern
	deny  []pattern
}

type pattern struct {
	raw      string
	segments []string
}

func NewPolicy(writePatterns, denyPatterns []string) (Policy, error) {
	if len(writePatterns) == 0 {
		return Policy{}, errors.New("write scope requires at least one pattern")
	}
	write, err := compilePatterns(writePatterns)
	if err != nil {
		return Policy{}, fmt.Errorf("compile write scope: %w", err)
	}
	deny, err := compilePatterns(denyPatterns)
	if err != nil {
		return Policy{}, fmt.Errorf("compile deny scope: %w", err)
	}
	return Policy{write: write, deny: deny}, nil
}

func (policy Policy) CheckWrite(paths []string) error {
	canonical, err := CanonicalizePaths(paths)
	if err != nil {
		return err
	}
	for repositoryPath := range canonical {
		if matchesAny(policy.deny, repositoryPath) {
			return fmt.Errorf("%w: %q matches deny scope", ErrViolation, repositoryPath)
		}
		if !matchesAny(policy.write, repositoryPath) {
			return fmt.Errorf("%w: %q is outside write scope", ErrViolation, repositoryPath)
		}
	}
	return nil
}

// NormalizeRepositoryPath validates a slash-separated repository-relative path
// and returns its Unicode NFC spelling.
func NormalizeRepositoryPath(value string) (string, error) {
	if value == "" || !utf8.ValidString(value) || path.IsAbs(value) || strings.ContainsAny(value, "\\\r\n\x00") {
		return "", fmt.Errorf("%w: %q", ErrUnsafePath, value)
	}
	canonical := norm.NFC.String(value)
	if path.Clean(canonical) != canonical {
		return "", fmt.Errorf("%w: %q is not clean", ErrUnsafePath, value)
	}
	for _, segment := range strings.Split(canonical, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.EqualFold(segment, ".git") {
			return "", fmt.Errorf("%w: %q contains a forbidden segment", ErrUnsafePath, value)
		}
	}
	return canonical, nil
}

// CanonicalizePaths returns canonical path -> original path and rejects both
// NFC aliases and Unicode case-fold aliases. This is conservative across
// case-sensitive and case-insensitive target filesystems.
func CanonicalizePaths(values []string) (map[string]string, error) {
	canonical := make(map[string]string, len(values))
	canonicalPrefixes := make(map[string]string)
	foldedPrefixes := make(map[string]string)
	for _, value := range values {
		normalized, err := NormalizeRepositoryPath(value)
		if err != nil {
			return nil, err
		}
		if previous, exists := canonical[normalized]; exists {
			return nil, fmt.Errorf("%w: %q and %q normalize to %q", ErrCollision, previous, value, normalized)
		}
		rawSegments := strings.Split(value, "/")
		normalizedSegments := strings.Split(normalized, "/")
		for index := range normalizedSegments {
			rawPrefix := strings.Join(rawSegments[:index+1], "/")
			canonicalPrefix := strings.Join(normalizedSegments[:index+1], "/")
			if previous, exists := canonicalPrefixes[canonicalPrefix]; exists && previous != rawPrefix {
				return nil, fmt.Errorf("%w: %q and %q normalize to %q", ErrCollision, previous, rawPrefix, canonicalPrefix)
			}
			foldedPrefix := fold.String(canonicalPrefix)
			if previous, exists := foldedPrefixes[foldedPrefix]; exists && previous != canonicalPrefix {
				return nil, fmt.Errorf("%w: %q and %q have the same Unicode case fold", ErrCollision, previous, canonicalPrefix)
			}
			canonicalPrefixes[canonicalPrefix] = rawPrefix
			foldedPrefixes[foldedPrefix] = canonicalPrefix
		}
		canonical[normalized] = value
	}
	return canonical, nil
}

// ValidateSymlinkTarget rejects absolute, escaping, Git-metadata, and malformed
// targets without dereferencing the link.
func ValidateSymlinkTarget(repositoryPath, target string) error {
	canonicalPath, err := NormalizeRepositoryPath(repositoryPath)
	if err != nil {
		return err
	}
	if target == "" || !utf8.ValidString(target) || strings.ContainsAny(target, "\\\r\n\x00") || path.IsAbs(target) {
		return fmt.Errorf("%w: symlink target is empty, absolute, or malformed", ErrUnsafePath)
	}
	resolved := path.Clean(path.Join(path.Dir(canonicalPath), norm.NFC.String(target)))
	if resolved == "." {
		return nil
	}
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("%w: symlink target escapes the repository", ErrUnsafePath)
	}
	if _, err := NormalizeRepositoryPath(resolved); err != nil {
		return fmt.Errorf("%w: unsafe symlink target: %v", ErrUnsafePath, err)
	}
	return nil
}

func compilePatterns(values []string) ([]pattern, error) {
	result := make([]pattern, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		compiled, err := compilePattern(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[compiled.raw]; exists {
			return nil, fmt.Errorf("duplicate scope pattern %q", value)
		}
		seen[compiled.raw] = struct{}{}
		result = append(result, compiled)
	}
	return result, nil
}

func compilePattern(value string) (pattern, error) {
	if value == "" || !utf8.ValidString(value) || !strings.HasPrefix(value, "/") || strings.ContainsAny(value, "\\\r\n\x00") {
		return pattern{}, fmt.Errorf("invalid root-anchored scope pattern %q", value)
	}
	canonical := norm.NFC.String(value)
	if canonical != "/" && strings.HasSuffix(canonical, "/") {
		return pattern{}, fmt.Errorf("scope pattern %q has a trailing slash", value)
	}
	segments := strings.Split(strings.TrimPrefix(canonical, "/"), "/")
	if canonical == "/" {
		segments = nil
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || (strings.Contains(segment, "**") && segment != "**") {
			return pattern{}, fmt.Errorf("scope pattern %q contains an invalid segment", value)
		}
		if segment != "**" {
			if _, err := path.Match(segment, "probe"); err != nil {
				return pattern{}, fmt.Errorf("scope pattern %q: %w", value, err)
			}
		}
	}
	return pattern{raw: canonical, segments: segments}, nil
}

func matchesAny(patterns []pattern, repositoryPath string) bool {
	segments := strings.Split(repositoryPath, "/")
	for _, candidate := range patterns {
		if matchSegments(candidate.segments, segments, 0, 0, make(map[[2]int]bool), make(map[[2]int]bool)) {
			return true
		}
	}
	return false
}

func matchSegments(patternSegments, pathSegments []string, patternIndex, pathIndex int, memo, visited map[[2]int]bool) bool {
	key := [2]int{patternIndex, pathIndex}
	if visited[key] {
		return memo[key]
	}
	visited[key] = true
	matched := false
	switch {
	case patternIndex == len(patternSegments):
		matched = pathIndex == len(pathSegments)
	case patternSegments[patternIndex] == "**":
		matched = matchSegments(patternSegments, pathSegments, patternIndex+1, pathIndex, memo, visited) ||
			(pathIndex < len(pathSegments) && matchSegments(patternSegments, pathSegments, patternIndex, pathIndex+1, memo, visited))
	case pathIndex < len(pathSegments):
		segmentMatched, _ := path.Match(patternSegments[patternIndex], pathSegments[pathIndex])
		matched = segmentMatched && matchSegments(patternSegments, pathSegments, patternIndex+1, pathIndex+1, memo, visited)
	}
	memo[key] = matched
	return matched
}
