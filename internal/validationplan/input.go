// Package validationplan defines bounded, immutable acceptance inputs and scripts.
package validationplan

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/scope"
)

const MaxInputBytes = 256 << 10

type Input struct {
	Path    string `json:"path"`
	Mode    string `json:"mode"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

func Paths(values []string) ([]string, error) {
	if len(values) > 32 {
		return nil, errors.New("acceptance files exceeds 32 entries")
	}
	seen := map[string]bool{}
	for _, value := range values {
		p, err := scope.NormalizeRepositoryPath(value)
		if err != nil || p != value || value == ".xgoal" || strings.HasPrefix(value, ".xgoal/") {
			return nil, fmt.Errorf("acceptance file must be a canonical repository path outside Git and xgoal state: %q", value)
		}
		seen[value] = true
	}
	var result []string
	for p := range seen {
		result = append(result, p)
	}
	sort.Strings(result)
	return result, nil
}

func NewInput(path, mode string, content []byte) (Input, error) {
	digest := sha256.Sum256(content)
	i := Input{Path: path, Mode: mode, SHA256: hex.EncodeToString(digest[:]), Content: string(content)}
	return i, i.Validate()
}

func (i Input) Validate() error {
	if _, err := Paths([]string{i.Path}); err != nil {
		return err
	}
	if (i.Mode != "100644" && i.Mode != "100755") || len(i.Content) > MaxInputBytes || !utf8.ValidString(i.Content) || strings.ContainsRune(i.Content, '\x00') {
		return fmt.Errorf("acceptance input %q must be bounded regular UTF-8 text", i.Path)
	}
	digest := sha256.Sum256([]byte(i.Content))
	if i.SHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("acceptance input hash mismatch")
	}
	return nil
}

func ValidateInputs(inputs []Input) error {
	if len(inputs) > 32 {
		return errors.New("acceptance inputs exceeds 32 entries")
	}
	for n, i := range inputs {
		if err := i.Validate(); err != nil {
			return err
		}
		if n > 0 && inputs[n-1].Path >= i.Path {
			return errors.New("acceptance inputs must be uniquely sorted")
		}
	}
	return nil
}
