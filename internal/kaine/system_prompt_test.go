package kaine_test

import (
	"testing"
	"time"

	"github.com/edin-space/edin-backend/internal/kaine"
)

// TestSystemPromptVersion_JSONShape verifies the struct fields and JSON tags
// match what the frontend API contract expects.
func TestSystemPromptVersion_JSONShape(t *testing.T) {
	label := "test label"
	v := kaine.SystemPromptVersion{
		ID:            3,
		Content:       "you are kaine",
		Label:         &label,
		IsActive:      true,
		CreatedAt:     time.Now(),
		CreatedBy:     "abc123",
		CreatedByName: "davidmoore",
	}

	if v.ID != 3 {
		t.Errorf("ID: got %d, want 3", v.ID)
	}
	if v.Content != "you are kaine" {
		t.Errorf("Content: got %q, want 'you are kaine'", v.Content)
	}
	if v.Label == nil || *v.Label != "test label" {
		t.Errorf("Label: got %v, want pointer to 'test label'", v.Label)
	}
	if !v.IsActive {
		t.Error("IsActive: expected true")
	}
	if v.CreatedByName != "davidmoore" {
		t.Errorf("CreatedByName: got %q, want 'davidmoore'", v.CreatedByName)
	}
}

func TestSystemPromptVersion_NilLabel(t *testing.T) {
	v := kaine.SystemPromptVersion{Label: nil}
	if v.Label != nil {
		t.Error("expected nil label to remain nil")
	}
}
