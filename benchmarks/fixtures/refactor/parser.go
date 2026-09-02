package refactor

import "strings"

func Normalize(input string) string {
	result := strings.TrimSpace(strings.ToLower(input))
	for strings.Contains(result, "  ") {
		result = strings.ReplaceAll(result, "  ", " ")
	}
	return result
}
