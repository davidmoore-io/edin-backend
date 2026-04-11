package kaine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SystemPromptVersion represents a versioned snapshot of the Kaine AI system prompt.
type SystemPromptVersion struct {
	ID            int       `json:"id"`
	Content       string    `json:"content,omitempty"` // omitted in list; included in individual fetch
	Label         *string   `json:"label"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
	CreatedBy     string    `json:"created_by"`
	CreatedByName string    `json:"created_by_name"`
}

// SeedAndLoadSystemPrompt ensures at least one system prompt version exists in the database.
// If the table is empty, it inserts the provided defaultContent as the initial version.
// It then returns the content of the active system prompt version.
func (s *Store) SeedAndLoadSystemPrompt(ctx context.Context, defaultContent string) (string, error) {
	var count int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM kaine.system_prompt_versions`).Scan(&count)
	if err != nil {
		return "", fmt.Errorf("count system prompt versions: %w", err)
	}

	if count == 0 {
		var content string
		err = s.pool.QueryRow(ctx, `
			INSERT INTO kaine.system_prompt_versions
				(content, label, is_active, created_by, created_by_name)
			VALUES
				($1, 'Initial version (compiled default)', TRUE, 'system', 'system')
			RETURNING content
		`, defaultContent).Scan(&content)
		if err != nil {
			return "", fmt.Errorf("seed system prompt: %w", err)
		}
		return content, nil
	}

	var content string
	err = s.pool.QueryRow(ctx, `
		SELECT content FROM kaine.system_prompt_versions
		WHERE is_active = TRUE
		LIMIT 1
	`).Scan(&content)
	if err == nil {
		return content, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("load active system prompt: %w", err)
	}

	// No active version found even though rows exist (e.g. manual intervention
	// deactivated all rows). Fall back to the most recently created version so
	// the server always starts with a usable prompt.
	err = s.pool.QueryRow(ctx, `
		SELECT content FROM kaine.system_prompt_versions
		ORDER BY id DESC LIMIT 1
	`).Scan(&content)
	if err != nil {
		return "", fmt.Errorf("load fallback system prompt (no active row): %w", err)
	}
	return content, nil
}

// ListSystemPromptVersions returns all system prompt versions (without content) ordered newest first,
// along with the content of the currently active version.
func (s *Store) ListSystemPromptVersions(ctx context.Context) ([]SystemPromptVersion, string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, label, is_active, created_at,
		       COALESCE(created_by, ''), COALESCE(created_by_name, '')
		FROM kaine.system_prompt_versions
		ORDER BY id DESC
	`)
	if err != nil {
		return nil, "", fmt.Errorf("list system prompt versions: %w", err)
	}
	defer rows.Close()

	var versions []SystemPromptVersion
	for rows.Next() {
		var v SystemPromptVersion
		if err := rows.Scan(&v.ID, &v.Label, &v.IsActive, &v.CreatedAt, &v.CreatedBy, &v.CreatedByName); err != nil {
			return nil, "", fmt.Errorf("scan system prompt version: %w", err)
		}
		versions = append(versions, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("iterate system prompt versions: %w", err)
	}

	var activeContent string
	err = s.pool.QueryRow(ctx, `
		SELECT content FROM kaine.system_prompt_versions
		WHERE is_active = TRUE
		LIMIT 1
	`).Scan(&activeContent)
	if err != nil && err != pgx.ErrNoRows {
		return nil, "", fmt.Errorf("load active system prompt content: %w", err)
	}

	return versions, activeContent, nil
}

// GetSystemPromptVersion retrieves a single system prompt version by ID, including its content.
// Returns nil, pgx.ErrNoRows (wrapped) if the version does not exist.
func (s *Store) GetSystemPromptVersion(ctx context.Context, id int) (*SystemPromptVersion, error) {
	var v SystemPromptVersion
	err := s.pool.QueryRow(ctx, `
		SELECT id, content, label, is_active, created_at,
		       COALESCE(created_by, ''), COALESCE(created_by_name, '')
		FROM kaine.system_prompt_versions
		WHERE id = $1
	`, id).Scan(&v.ID, &v.Content, &v.Label, &v.IsActive, &v.CreatedAt, &v.CreatedBy, &v.CreatedByName)
	if err != nil {
		return nil, fmt.Errorf("get system prompt version: %w", err)
	}
	return &v, nil
}

// SaveSystemPromptVersion creates a new system prompt version and makes it active.
// All existing active versions are deactivated within the same transaction.
// If label is an empty string, it is stored as NULL.
func (s *Store) SaveSystemPromptVersion(ctx context.Context, content, label, createdBy, createdByName string) (*SystemPromptVersion, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	_, err = tx.Exec(ctx, `
		UPDATE kaine.system_prompt_versions SET is_active = FALSE WHERE is_active = TRUE
	`)
	if err != nil {
		return nil, fmt.Errorf("deactivate existing system prompts: %w", err)
	}

	var labelPtr *string
	if label != "" {
		labelPtr = &label
	}

	var v SystemPromptVersion
	err = tx.QueryRow(ctx, `
		INSERT INTO kaine.system_prompt_versions
			(content, label, is_active, created_by, created_by_name)
		VALUES
			($1, $2, TRUE, $3, $4)
		RETURNING id, content, label, is_active, created_at,
		          COALESCE(created_by, ''), COALESCE(created_by_name, '')
	`, content, labelPtr, nullableString(createdBy), nullableString(createdByName),
	).Scan(&v.ID, &v.Content, &v.Label, &v.IsActive, &v.CreatedAt, &v.CreatedBy, &v.CreatedByName)
	if err != nil {
		return nil, fmt.Errorf("insert system prompt version: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit system prompt version: %w", err)
	}

	return &v, nil
}

// ActivateSystemPromptVersion makes the specified version active and deactivates all others.
// Returns wrapped pgx.ErrNoRows if the version does not exist.
func (s *Store) ActivateSystemPromptVersion(ctx context.Context, id int) (*SystemPromptVersion, error) {
	// Verify the version exists before starting a transaction.
	var existingID int
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM kaine.system_prompt_versions WHERE id = $1
	`, id).Scan(&existingID)
	if err != nil {
		return nil, fmt.Errorf("check system prompt version exists: %w", err)
	}

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	_, err = tx.Exec(ctx, `
		UPDATE kaine.system_prompt_versions SET is_active = FALSE WHERE is_active = TRUE
	`)
	if err != nil {
		return nil, fmt.Errorf("deactivate existing system prompts: %w", err)
	}

	var v SystemPromptVersion
	err = tx.QueryRow(ctx, `
		UPDATE kaine.system_prompt_versions
		SET is_active = TRUE
		WHERE id = $1
		RETURNING id, content, label, is_active, created_at,
		          COALESCE(created_by, ''), COALESCE(created_by_name, '')
	`, id).Scan(&v.ID, &v.Content, &v.Label, &v.IsActive, &v.CreatedAt, &v.CreatedBy, &v.CreatedByName)
	if err != nil {
		return nil, fmt.Errorf("activate system prompt version: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit activate system prompt version: %w", err)
	}

	return &v, nil
}
