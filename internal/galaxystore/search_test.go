package galaxystore

import "testing"

func TestEscapeLikePrefix(t *testing.T) {
	tests := map[string]string{
		"Sol":      "Sol",
		"100%":     `100\%`,
		"A_B":      `A\_B`,
		`A\B`:      `A\\B`,
		`\%_mixed`: `\\\%\_mixed`,
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			if got := escapeLikePrefix(input); got != want {
				t.Fatalf("escapeLikePrefix(%q) = %q, want %q", input, got, want)
			}
		})
	}
}
