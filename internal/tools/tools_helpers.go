package tools

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// systemIdentifiers extracts system_name and system_id from args.
func systemIdentifiers(args map[string]any) (string, string) {
	return strings.TrimSpace(getString(args, "system_name")), strings.TrimSpace(getString(args, "system_id"))
}

func getString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		switch typed := v.(type) {
		case string:
			return typed
		case fmt.Stringer:
			return typed.String()
		case json.Number:
			return typed.String()
		case float64, float32, int, int64, uint, uint64:
			return fmt.Sprintf("%v", typed)
		}
	}
	return ""
}

func getInt(m map[string]any, key string, fallback int) int {
	if m == nil {
		return fallback
	}
	if v, ok := m[key]; ok {
		switch typed := v.(type) {
		case float64:
			return int(typed)
		case float32:
			return int(typed)
		case int:
			return typed
		case int64:
			return int(typed)
		case json.Number:
			if n, err := typed.Int64(); err == nil {
				return int(n)
			}
		case string:
			var parsed int
			if _, err := fmt.Sscanf(typed, "%d", &parsed); err == nil {
				return parsed
			}
		}
	}
	return fallback
}

func getBool(m map[string]any, key string, fallback bool) bool {
	if m == nil {
		return fallback
	}
	if v, ok := m[key]; ok {
		return asBool(v)
	}
	return fallback
}

func getFloatArg(m map[string]any, key string, fallback float64) float64 {
	if m == nil {
		return fallback
	}
	if v, ok := m[key]; ok {
		return asFloat64(v)
	}
	return fallback
}

func asMap(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}
	return nil
}

func asSlice(v any) []any {
	if v == nil {
		return nil
	}
	if s, ok := v.([]any); ok {
		return s
	}
	if s, ok := v.([]interface{}); ok {
		return s
	}
	return nil
}

func asFloat64(v any) float64 {
	switch typed := v.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case uint:
		return float64(typed)
	case uint64:
		return float64(typed)
	case json.Number:
		if f, err := typed.Float64(); err == nil {
			return f
		}
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return f
		}
	}
	return 0
}

func asBool(v any) bool {
	switch typed := v.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case uint:
		return typed != 0
	case uint64:
		return typed != 0
	case json.Number:
		if i, err := typed.Int64(); err == nil {
			return i != 0
		}
	case string:
		trim := strings.TrimSpace(strings.ToLower(typed))
		if trim == "true" || trim == "yes" || trim == "y" || trim == "1" {
			return true
		}
	}
	return false
}

func chunkStrings(lines []string, max int) []string {
	if max <= 0 {
		max = 1800
	}
	var chunks []string
	var builder strings.Builder

	flush := func() {
		if builder.Len() > 0 {
			chunks = append(chunks, builder.String())
			builder.Reset()
		}
	}

	for _, line := range lines {
		text := strings.TrimSpace(line)
		if text == "" {
			continue
		}
		appendLine := text
		if builder.Len() > 0 {
			appendLine = "\n" + appendLine
		}
		if builder.Len()+len(appendLine) > max {
			flush()
		}
		if builder.Len() == 0 {
			builder.WriteString(text)
		} else {
			builder.WriteString("\n")
			builder.WriteString(text)
		}
	}
	flush()
	return chunks
}

// extractSpanshResultForCG extracts the result from spansh response data.
func extractSpanshResultForCG(data map[string]any) map[string]any {
	if result, ok := data["result"].(map[string]any); ok {
		return result
	}
	if results, ok := data["results"].([]any); ok && len(results) > 0 {
		if first, ok := results[0].(map[string]any); ok {
			return first
		}
	}
	return nil
}
