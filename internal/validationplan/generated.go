package validationplan

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

type Generated struct {
	ID             string `json:"id"`
	Description    string `json:"description"`
	Runtime        string `json:"runtime"`
	Script         string `json:"script"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func ValidateGenerated(scripts []Generated) error {
	if len(scripts) > 16 {
		return fmt.Errorf("generated validators exceeds 16 entries")
	}
	seen := map[string]bool{}
	for _, s := range scripts {
		if s.ID == "" || len(s.ID) > 160 || strings.IndexFunc(s.ID, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
		}) >= 0 || seen[s.ID] {
			return fmt.Errorf("generated validator ID is invalid or duplicate: %q", s.ID)
		}
		seen[s.ID] = true
		if s.Runtime != "sh" && s.Runtime != "node" && s.Runtime != "python3" {
			return fmt.Errorf("generated validator %s requires runtime sh, node, or python3", s.ID)
		}
		if strings.TrimSpace(s.Description) == "" || len(s.Description) > 8000 || strings.ContainsRune(s.Description, '\x00') || !utf8.ValidString(s.Description) || strings.TrimSpace(s.Script) == "" || len(s.Script) > 64<<10 || strings.ContainsRune(s.Script, '\x00') || !utf8.ValidString(s.Script) || s.TimeoutSeconds < 1 || s.TimeoutSeconds > 120 {
			return fmt.Errorf("generated validator %s requires bounded script, description and timeout 1..120 seconds", s.ID)
		}
	}
	return nil
}

func (g Generated) Argv() []string {
	switch g.Runtime {
	case "node":
		return []string{"node", "-e", g.Script}
	case "python3":
		return []string{"python3", "-B", "-c", g.Script}
	default:
		return []string{"sh", "-c", g.Script}
	}
}
