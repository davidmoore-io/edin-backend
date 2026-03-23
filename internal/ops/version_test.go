package ops

import "testing"

func TestParseSatisfactoryVersion(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "build line with prefix",
			input:    "LogInit: Build: ++FactoryGame+rel-main-1.0.0-CL-365306",
			expected: "rel-main-1.0.0 (CL-365306)",
		},
		{
			name:     "build line without prefix",
			input:    "LogInit: Build: update9-CL-123456",
			expected: "update9 (CL-123456)",
		},
		{
			name:     "build line without changelist",
			input:    "LogInit: Build: ++FactoryGame+update10",
			expected: "update10",
		},
		{
			name:     "multi-line output",
			input:    "\nLogInit: Build: ++FactoryGame+rel-main-1.0.0-CL-365306\n\n",
			expected: "rel-main-1.0.0 (CL-365306)",
		},
		{
			name:     "non matching content",
			input:    "some other output",
			expected: "some other output",
		},
		{
			name:     "empty input",
			input:    "",
			expected: "",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := parseSatisfactoryVersion(tc.input); got != tc.expected {
				t.Fatalf("parseSatisfactoryVersion() = %q, want %q", got, tc.expected)
			}
		})
	}
}
