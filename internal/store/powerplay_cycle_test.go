package store

import (
	"testing"
	"time"
)

func TestGetCurrentCycleStart(t *testing.T) {
	tests := []struct {
		name     string
		now      time.Time
		expected time.Time
	}{
		{
			name:     "Thursday after tick",
			now:      time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC), // Thursday 10:00
			expected: time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC),  // Same Thursday 07:00
		},
		{
			name:     "Thursday before tick",
			now:      time.Date(2026, 1, 15, 6, 0, 0, 0, time.UTC), // Thursday 06:00
			expected: time.Date(2026, 1, 8, 7, 0, 0, 0, time.UTC),  // Previous Thursday 07:00
		},
		{
			name:     "Friday",
			now:      time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC), // Friday 12:00
			expected: time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC),  // Thursday 07:00
		},
		{
			name:     "Wednesday",
			now:      time.Date(2026, 1, 14, 12, 0, 0, 0, time.UTC), // Wednesday 12:00
			expected: time.Date(2026, 1, 8, 7, 0, 0, 0, time.UTC),   // Previous Thursday 07:00
		},
		{
			name:     "Monday",
			now:      time.Date(2026, 1, 12, 12, 0, 0, 0, time.UTC), // Monday 12:00
			expected: time.Date(2026, 1, 8, 7, 0, 0, 0, time.UTC),   // Previous Thursday 07:00
		},
		{
			name:     "Sunday before Thursday",
			now:      time.Date(2026, 1, 11, 12, 0, 0, 0, time.UTC), // Sunday 12:00
			expected: time.Date(2026, 1, 8, 7, 0, 0, 0, time.UTC),   // Previous Thursday 07:00
		},
		{
			name:     "Saturday",
			now:      time.Date(2026, 1, 17, 12, 0, 0, 0, time.UTC), // Saturday 12:00
			expected: time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC),  // Thursday 07:00
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetCurrentCycleStart(tt.now)
			if !got.Equal(tt.expected) {
				t.Errorf("GetCurrentCycleStart(%v) = %v, want %v", tt.now, got, tt.expected)
			}
		})
	}
}

func TestGetCycleBoundaries(t *testing.T) {
	now := time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC) // Friday 12:00

	// Current cycle
	current := GetCycleBoundaries(now, 0)
	expectedStart := time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC)
	if !current.StartTime.Equal(expectedStart) {
		t.Errorf("Current cycle start = %v, want %v", current.StartTime, expectedStart)
	}
	if !current.IsCurrent {
		t.Error("Current cycle should have IsCurrent=true")
	}
	if current.CycleNumber != 0 {
		t.Errorf("Current cycle number = %d, want 0", current.CycleNumber)
	}

	// Previous cycle
	prev := GetCycleBoundaries(now, -1)
	expectedPrevStart := time.Date(2026, 1, 8, 7, 0, 0, 0, time.UTC)
	expectedPrevEnd := time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC)
	if !prev.StartTime.Equal(expectedPrevStart) {
		t.Errorf("Previous cycle start = %v, want %v", prev.StartTime, expectedPrevStart)
	}
	if !prev.EndTime.Equal(expectedPrevEnd) {
		t.Errorf("Previous cycle end = %v, want %v", prev.EndTime, expectedPrevEnd)
	}
	if prev.IsCurrent {
		t.Error("Previous cycle should have IsCurrent=false")
	}
	if prev.CycleNumber != -1 {
		t.Errorf("Previous cycle number = %d, want -1", prev.CycleNumber)
	}
}

func TestGetCycleForTime(t *testing.T) {
	now := time.Date(2026, 1, 16, 12, 0, 0, 0, time.UTC) // Friday 12:00

	tests := []struct {
		name      string
		eventTime time.Time
		expected  int
	}{
		{
			name:      "Event in current cycle",
			eventTime: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
			expected:  0,
		},
		{
			name:      "Event in previous cycle",
			eventTime: time.Date(2026, 1, 10, 10, 0, 0, 0, time.UTC),
			expected:  -1,
		},
		{
			name:      "Event two cycles ago",
			eventTime: time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC),
			expected:  -2,
		},
		{
			name:      "Event at cycle boundary",
			eventTime: time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC),
			expected:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetCycleForTime(tt.eventTime, now)
			if got != tt.expected {
				t.Errorf("GetCycleForTime(%v, %v) = %d, want %d", tt.eventTime, now, got, tt.expected)
			}
		})
	}
}

func TestIsInMaintenanceWindow(t *testing.T) {
	tests := []struct {
		name     string
		time     time.Time
		expected bool
	}{
		{
			name:     "Thursday 07:00 (start of window)",
			time:     time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC),
			expected: true,
		},
		{
			name:     "Thursday 07:30 (middle of window)",
			time:     time.Date(2026, 1, 15, 7, 30, 0, 0, time.UTC),
			expected: true,
		},
		{
			name:     "Thursday 08:30 (end of window)",
			time:     time.Date(2026, 1, 15, 8, 30, 0, 0, time.UTC),
			expected: true,
		},
		{
			name:     "Thursday 08:31 (after window)",
			time:     time.Date(2026, 1, 15, 8, 31, 0, 0, time.UTC),
			expected: false,
		},
		{
			name:     "Thursday 06:59 (before window)",
			time:     time.Date(2026, 1, 15, 6, 59, 0, 0, time.UTC),
			expected: false,
		},
		{
			name:     "Friday 07:30 (wrong day)",
			time:     time.Date(2026, 1, 16, 7, 30, 0, 0, time.UTC),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsInMaintenanceWindow(tt.time)
			if got != tt.expected {
				t.Errorf("IsInMaintenanceWindow(%v) = %v, want %v", tt.time, got, tt.expected)
			}
		})
	}
}
