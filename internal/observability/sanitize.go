package observability

import "strings"

// Sanitize normalises whitespace and truncates the value for logging.
func Sanitize(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	clean := strings.Join(strings.Fields(value), " ")
	if max <= 0 {
		max = 256
	}
	runes := []rune(clean)
	if len(runes) > max {
		return string(runes[:max]) + "…"
	}
	return clean
}
