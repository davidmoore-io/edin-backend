package discord

import (
	"strings"
)

func serviceLabel(cfgServiceLabels map[string]string, service string) string {
	if label, ok := cfgServiceLabels[service]; ok && strings.TrimSpace(label) != "" {
		return label
	}
	return humanizeServiceName(service)
}

func humanizeServiceName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "Service"
	}
	parts := strings.FieldsFunc(name, func(r rune) bool {
		switch r {
		case '-', '_', '.', ' ':
			return true
		default:
			return false
		}
	})
	for i, part := range parts {
		parts[i] = capitalize(part)
	}
	return strings.Join(parts, " ")
}

func capitalize(word string) string {
	if word == "" {
		return word
	}
	runes := []rune(strings.ToLower(word))
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
}
