package redact

import (
	"regexp"
	"strings"
)

var patterns = []struct {
	expression  *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)(authorization["']?\s*[:=]\s*["']?(?:bearer\s+)?)[^\s,"'}]+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`(?i)(\b[A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|API_KEY|ACCESS_KEY)[A-Z0-9_]*["']?\s*[:=]\s*["']?)[^\s,"'}]+`), `${1}[REDACTED]`},
	{regexp.MustCompile(`\bsk-ant-[A-Za-z0-9_-]{12,}\b`), `[REDACTED]`},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}\b`), `[REDACTED]`},
}

// String removes common credential forms before untrusted process output is
// persisted. It is deliberately conservative and does not claim perfect
// secret detection.
func String(value string) string {
	for _, pattern := range patterns {
		value = pattern.expression.ReplaceAllString(value, pattern.replacement)
	}
	return value
}

// Value recursively redacts strings in decoded JSON while retaining unknown
// fields for forward-compatible audit artifacts.
func Value(value any) any {
	switch typed := value.(type) {
	case string:
		return String(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = Value(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveName(key) {
				result[key] = "[REDACTED]"
			} else {
				result[key] = Value(item)
			}
		}
		return result
	default:
		return value
	}
}

func sensitiveName(value string) bool {
	upper := strings.ToUpper(value)
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "API_KEY", "ACCESS_KEY", "PRIVATE_KEY", "AUTHORIZATION"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return false
}
