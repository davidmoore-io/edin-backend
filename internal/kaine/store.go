package kaine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store provides database operations for Kaine objectives.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore creates a new Kaine store with the given database connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// ListObjectives retrieves objectives based on the filter.
func (s *Store) ListObjectives(ctx context.Context, filter ListObjectivesFilter) ([]Objective, error) {
	query := `
		SELECT
			id, system_name, priority, objective_type, category, COALESCE(board, 'main'),
			COALESCE(title, ''), COALESCE(description, ''), COALESCE(bgs_notes, ''),
			COALESCE(merit_methods, '{}'), COALESCE(external_links, '[]'),
			state, access_level,
			created_at, COALESCE(created_by, ''), COALESCE(created_by_name, ''),
			updated_at, COALESCE(updated_by, ''),
			approved_at, COALESCE(approved_by, ''),
			publish_at, completed_at, archived_at,
			suggested_complete, merit_target, cycle_number
		FROM kaine.objectives
		WHERE 1=1
	`
	args := []interface{}{}
	argNum := 1

	// Filter by board
	if filter.Board != "" {
		query += fmt.Sprintf(" AND board = $%d", argNum)
		args = append(args, filter.Board)
		argNum++
	}

	// Filter by state(s)
	if len(filter.States) > 0 {
		query += fmt.Sprintf(" AND state = ANY($%d)", argNum)
		args = append(args, filter.States)
		argNum++
	} else if filter.State != "" {
		query += fmt.Sprintf(" AND state = $%d", argNum)
		args = append(args, filter.State)
		argNum++
	} else {
		// Default: show all non-archived states (unless IncludeArchived is true)
		if !filter.IncludeArchived {
			query += fmt.Sprintf(" AND state != $%d", argNum)
			args = append(args, StateArchived)
			argNum++
		}
	}

	// Filter by access levels (user can see these levels)
	if len(filter.AccessLevels) > 0 {
		query += fmt.Sprintf(" AND access_level = ANY($%d)", argNum)
		args = append(args, filter.AccessLevels)
		argNum++
	}

	// Filter by objective type
	if filter.ObjectiveType != "" {
		query += fmt.Sprintf(" AND objective_type = $%d", argNum)
		args = append(args, filter.ObjectiveType)
		argNum++
	}

	// Filter by category
	if filter.Category != "" {
		query += fmt.Sprintf(" AND category = $%d", argNum)
		args = append(args, filter.Category)
		argNum++
	}

	// Filter by cycle number
	if filter.CycleNumber != nil {
		query += fmt.Sprintf(" AND cycle_number = $%d", argNum)
		args = append(args, *filter.CycleNumber)
		argNum++
	}

	// Only show published objectives (publish_at <= now) - but always show drafts/approved to editors
	query += " AND (publish_at IS NULL OR publish_at <= NOW() OR state IN ('draft', 'approved'))"

	// Order by state (active first), then priority (high first), then by created_at
	query += ` ORDER BY
		CASE state WHEN 'active' THEN 1 WHEN 'approved' THEN 2 WHEN 'draft' THEN 3 WHEN 'completed' THEN 4 WHEN 'cancelled' THEN 5 ELSE 6 END,
		CASE priority WHEN 'high' THEN 1 WHEN 'medium' THEN 2 ELSE 3 END,
		created_at DESC`

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query objectives: %w", err)
	}
	defer rows.Close()

	var objectives []Objective
	for rows.Next() {
		obj, err := scanObjective(rows)
		if err != nil {
			return nil, err
		}
		objectives = append(objectives, *obj)
	}

	return objectives, rows.Err()
}

// GetObjective retrieves a single objective by ID.
func (s *Store) GetObjective(ctx context.Context, id string) (*Objective, error) {
	query := `
		SELECT
			id, system_name, priority, objective_type, category, COALESCE(board, 'main'),
			COALESCE(title, ''), COALESCE(description, ''), COALESCE(bgs_notes, ''),
			COALESCE(merit_methods, '{}'), COALESCE(external_links, '[]'),
			state, access_level,
			created_at, COALESCE(created_by, ''), COALESCE(created_by_name, ''),
			updated_at, COALESCE(updated_by, ''),
			approved_at, COALESCE(approved_by, ''),
			publish_at, completed_at, archived_at,
			suggested_complete, merit_target, cycle_number
		FROM kaine.objectives
		WHERE id = $1
	`

	rows, err := s.pool.Query(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("query objective: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, nil // Not found
	}

	return scanObjective(rows)
}

// scanObjective scans a row into an Objective struct.
func scanObjective(rows pgx.Rows) (*Objective, error) {
	var obj Objective
	var meritMethods []string
	var externalLinksJSON []byte
	var approvedAt, publishAt, completedAt, archivedAt *time.Time
	var meritTarget *int64
	var cycleNumber *int32

	err := rows.Scan(
		&obj.ID, &obj.SystemName, &obj.Priority, &obj.ObjectiveType, &obj.Category, &obj.Board,
		&obj.Title, &obj.Description, &obj.BGSNotes,
		&meritMethods, &externalLinksJSON,
		&obj.State, &obj.AccessLevel,
		&obj.CreatedAt, &obj.CreatedBy, &obj.CreatedByName,
		&obj.UpdatedAt, &obj.UpdatedBy,
		&approvedAt, &obj.ApprovedBy,
		&publishAt, &completedAt, &archivedAt,
		&obj.SuggestedComplete, &meritTarget, &cycleNumber,
	)
	if err != nil {
		return nil, fmt.Errorf("scan objective: %w", err)
	}

	obj.MeritMethods = meritMethods
	if err := json.Unmarshal(externalLinksJSON, &obj.ExternalLinks); err != nil {
		obj.ExternalLinks = []ExternalLink{}
	}
	obj.ApprovedAt = approvedAt
	obj.PublishAt = publishAt
	obj.CompletedAt = completedAt
	obj.ArchivedAt = archivedAt
	if meritTarget != nil {
		obj.MeritTarget = meritTarget
	}
	if cycleNumber != nil {
		cn := int(*cycleNumber)
		obj.CycleNumber = &cn
	}

	return &obj, nil
}

// CreateObjective creates a new objective and returns it.
func (s *Store) CreateObjective(ctx context.Context, input CreateObjectiveInput, userID, userName string) (*Objective, error) {
	// Set defaults
	if input.Priority == "" {
		input.Priority = PriorityMedium
	}
	if input.Category == "" {
		input.Category = CategoryStandard
	}
	if input.Board == "" {
		input.Board = BoardMain
	}
	if input.AccessLevel == "" {
		input.AccessLevel = AccessPublic
	}

	// Validate
	if input.SystemName == "" {
		return nil, fmt.Errorf("system_name is required")
	}
	if input.ObjectiveType == "" {
		return nil, fmt.Errorf("objective_type is required")
	}
	if !IsValidBoard(input.Board) {
		return nil, fmt.Errorf("invalid board: %s (must be one of: main, ops, thunderdome)", input.Board)
	}

	externalLinksJSON, err := json.Marshal(input.ExternalLinks)
	if err != nil {
		externalLinksJSON = []byte("[]")
	}

	query := `
		INSERT INTO kaine.objectives (
			system_name, priority, objective_type, category, board,
			title, description, bgs_notes, merit_methods, external_links,
			state, access_level,
			created_by, created_by_name,
			merit_target, cycle_number, publish_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10,
			$11, $12,
			$13, $14,
			$15, $16, $17
		) RETURNING id, created_at, updated_at
	`

	var obj Objective
	err = s.pool.QueryRow(ctx, query,
		input.SystemName, input.Priority, input.ObjectiveType, input.Category, input.Board,
		nullableString(input.Title), nullableString(input.Description), nullableString(input.BGSNotes),
		input.MeritMethods, externalLinksJSON,
		StateDraft, input.AccessLevel,
		nullableString(userID), nullableString(userName),
		input.MeritTarget, input.CycleNumber, input.PublishAt,
	).Scan(&obj.ID, &obj.CreatedAt, &obj.UpdatedAt)

	if err != nil {
		return nil, fmt.Errorf("create objective: %w", err)
	}

	// Fill in the rest from input
	obj.SystemName = input.SystemName
	obj.Priority = input.Priority
	obj.ObjectiveType = input.ObjectiveType
	obj.Category = input.Category
	obj.Board = input.Board
	obj.Title = input.Title
	obj.Description = input.Description
	obj.BGSNotes = input.BGSNotes
	obj.MeritMethods = input.MeritMethods
	obj.ExternalLinks = input.ExternalLinks
	obj.State = StateDraft
	obj.AccessLevel = input.AccessLevel
	obj.CreatedBy = userID
	obj.CreatedByName = userName
	obj.MeritTarget = input.MeritTarget
	obj.CycleNumber = input.CycleNumber
	obj.PublishAt = input.PublishAt

	return &obj, nil
}

// UpdateObjective updates an existing objective.
// Note: For state transitions, prefer using TransitionState which validates the transition.
func (s *Store) UpdateObjective(ctx context.Context, id string, input UpdateObjectiveInput, userID string) (*Objective, error) {
	// Build dynamic update query
	setParts := []string{}
	args := []interface{}{}
	argNum := 1

	if input.SystemName != nil {
		setParts = append(setParts, fmt.Sprintf("system_name = $%d", argNum))
		args = append(args, *input.SystemName)
		argNum++
	}
	if input.Priority != nil {
		setParts = append(setParts, fmt.Sprintf("priority = $%d", argNum))
		args = append(args, *input.Priority)
		argNum++
	}
	if input.ObjectiveType != nil {
		setParts = append(setParts, fmt.Sprintf("objective_type = $%d", argNum))
		args = append(args, *input.ObjectiveType)
		argNum++
	}
	if input.Category != nil {
		setParts = append(setParts, fmt.Sprintf("category = $%d", argNum))
		args = append(args, *input.Category)
		argNum++
	}
	if input.Board != nil {
		if !IsValidBoard(*input.Board) {
			return nil, fmt.Errorf("invalid board: %s (must be one of: main, ops, thunderdome)", *input.Board)
		}
		setParts = append(setParts, fmt.Sprintf("board = $%d", argNum))
		args = append(args, *input.Board)
		argNum++
	}
	if input.Title != nil {
		setParts = append(setParts, fmt.Sprintf("title = $%d", argNum))
		args = append(args, nullableString(*input.Title))
		argNum++
	}
	if input.Description != nil {
		setParts = append(setParts, fmt.Sprintf("description = $%d", argNum))
		args = append(args, nullableString(*input.Description))
		argNum++
	}
	if input.BGSNotes != nil {
		setParts = append(setParts, fmt.Sprintf("bgs_notes = $%d", argNum))
		args = append(args, nullableString(*input.BGSNotes))
		argNum++
	}
	if input.MeritMethods != nil {
		setParts = append(setParts, fmt.Sprintf("merit_methods = $%d", argNum))
		args = append(args, input.MeritMethods)
		argNum++
	}
	if input.ExternalLinks != nil {
		linksJSON, _ := json.Marshal(input.ExternalLinks)
		setParts = append(setParts, fmt.Sprintf("external_links = $%d", argNum))
		args = append(args, linksJSON)
		argNum++
	}
	if input.State != nil {
		setParts = append(setParts, fmt.Sprintf("state = $%d", argNum))
		args = append(args, *input.State)
		argNum++
		// Set appropriate timestamps based on new state
		now := time.Now()
		switch *input.State {
		case StateApproved:
			setParts = append(setParts, fmt.Sprintf("approved_at = $%d", argNum))
			args = append(args, now)
			argNum++
			setParts = append(setParts, fmt.Sprintf("approved_by = $%d", argNum))
			args = append(args, nullableString(userID))
			argNum++
		case StateCompleted:
			setParts = append(setParts, fmt.Sprintf("completed_at = $%d", argNum))
			args = append(args, now)
			argNum++
		case StateArchived:
			setParts = append(setParts, fmt.Sprintf("archived_at = $%d", argNum))
			args = append(args, now)
			argNum++
		}
	}
	if input.AccessLevel != nil {
		setParts = append(setParts, fmt.Sprintf("access_level = $%d", argNum))
		args = append(args, *input.AccessLevel)
		argNum++
	}
	if input.MeritTarget != nil {
		setParts = append(setParts, fmt.Sprintf("merit_target = $%d", argNum))
		args = append(args, *input.MeritTarget)
		argNum++
	}
	if input.CycleNumber != nil {
		setParts = append(setParts, fmt.Sprintf("cycle_number = $%d", argNum))
		args = append(args, *input.CycleNumber)
		argNum++
	}
	if input.PublishAt != nil {
		setParts = append(setParts, fmt.Sprintf("publish_at = $%d", argNum))
		args = append(args, *input.PublishAt)
		argNum++
	}

	// Always update updated_by
	setParts = append(setParts, fmt.Sprintf("updated_by = $%d", argNum))
	args = append(args, nullableString(userID))
	argNum++

	if len(setParts) == 1 { // Only updated_by
		return s.GetObjective(ctx, id)
	}

	query := fmt.Sprintf("UPDATE kaine.objectives SET %s WHERE id = $%d",
		strings.Join(setParts, ", "), argNum)
	args = append(args, id)

	_, err := s.pool.Exec(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("update objective: %w", err)
	}

	return s.GetObjective(ctx, id)
}

// DeleteObjective soft-deletes an objective by setting state to cancelled.
func (s *Store) DeleteObjective(ctx context.Context, id, userID string) error {
	query := `UPDATE kaine.objectives SET state = $1, updated_by = $2 WHERE id = $3`
	_, err := s.pool.Exec(ctx, query, StateCancelled, nullableString(userID), id)
	if err != nil {
		return fmt.Errorf("delete objective: %w", err)
	}
	return nil
}

// SuggestComplete adds a user's suggestion that an objective is complete.
func (s *Store) SuggestComplete(ctx context.Context, objectiveID, userID, userName string) error {
	// Insert or ignore if already suggested
	query := `
		INSERT INTO kaine.objective_suggestions (objective_id, discord_user_id, discord_username)
		VALUES ($1, $2, $3)
		ON CONFLICT (objective_id, discord_user_id) DO NOTHING
	`
	_, err := s.pool.Exec(ctx, query, objectiveID, userID, nullableString(userName))
	if err != nil {
		return fmt.Errorf("suggest complete: %w", err)
	}

	// Update count on objective
	updateQuery := `
		UPDATE kaine.objectives
		SET suggested_complete = (
			SELECT COUNT(*) FROM kaine.objective_suggestions WHERE objective_id = $1
		)
		WHERE id = $1
	`
	_, err = s.pool.Exec(ctx, updateQuery, objectiveID)
	if err != nil {
		return fmt.Errorf("update suggestion count: %w", err)
	}

	return nil
}

// TransitionState changes an objective's state with validation.
// Returns an error if the transition is not allowed.
func (s *Store) TransitionState(ctx context.Context, id, newState, userID string) (*Objective, error) {
	// Get current objective to check current state
	obj, err := s.GetObjective(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get objective for transition: %w", err)
	}
	if obj == nil {
		return nil, fmt.Errorf("objective not found: %s", id)
	}

	// Validate the transition
	if !IsValidStateTransition(obj.State, newState) {
		return nil, fmt.Errorf("invalid state transition: cannot transition from '%s' to '%s'", obj.State, newState)
	}

	// Perform the update with appropriate timestamps
	input := &UpdateObjectiveInput{State: &newState}
	result, err := s.UpdateObjective(ctx, id, *input, userID)
	if err != nil {
		return nil, fmt.Errorf("transition state: %w", err)
	}

	// TODO: Discord notification placeholder
	// When transitioning to 'active' state, this is where we would notify Discord.
	// The Discord bot integration will be implemented in a future sprint.
	// Example: notifyDiscordObjectivePublished(ctx, result)
	if newState == StateActive {
		// Future: Send Discord notification for published objective
		// This will be implemented when the Discord bot is created.
		// The notification should include: board, title, system_name, priority, description
	}

	return result, nil
}

// GetObjectiveCounts returns counts of objectives by board and state.
func (s *Store) GetObjectiveCounts(ctx context.Context) (*ObjectiveCounts, error) {
	query := `
		SELECT
			COALESCE(board, 'main') as board,
			state,
			COUNT(*) as count
		FROM kaine.objectives
		GROUP BY board, state
	`

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query objective counts: %w", err)
	}
	defer rows.Close()

	counts := &ObjectiveCounts{
		ByBoard: make(map[string]int),
		ByState: make(map[string]map[string]int),
	}

	// Initialize board maps
	for _, b := range ValidBoards {
		counts.ByState[b] = make(map[string]int)
	}

	for rows.Next() {
		var board, state string
		var count int
		if err := rows.Scan(&board, &state, &count); err != nil {
			return nil, fmt.Errorf("scan objective counts: %w", err)
		}

		// Initialize board if not exists
		if _, ok := counts.ByState[board]; !ok {
			counts.ByState[board] = make(map[string]int)
		}

		counts.ByState[board][state] = count

		// ByBoard counts only active objectives (for tab badges)
		if state == StateActive {
			counts.ByBoard[board] = count
		}
	}

	return counts, rows.Err()
}

// nullableString returns nil for empty strings, otherwise the string pointer.
func nullableString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ============================================================================
// Mining Maps CRUD
// ============================================================================

// ListMiningMaps retrieves mining maps based on the filter.
// Note: power_state is NOT in the database - it comes from Memgraph via the HTTP handler.
func (s *Store) ListMiningMaps(ctx context.Context, filter ListMiningMapsFilter) ([]MiningMap, error) {
	query := `
		SELECT
			id, system_name, body,
			COALESCE(ring_type, ''), COALESCE(reserve_level, ''),
			COALESCE(res_sites, ''), COALESCE(hotspots, '{}'),
			COALESCE(map_1, ''), COALESCE(map_1_title, ''), COALESCE(map_1_commodity, '{}'),
			COALESCE(map_2, ''), COALESCE(map_2_title, ''), COALESCE(map_2_commodity, '{}'),
			COALESCE(map_3, ''), COALESCE(map_3_title, ''), COALESCE(map_3_commodity, '{}'),
			COALESCE(search_url, ''),
			COALESCE(expansion_faction, ''), COALESCE(notes, ''),
			created_at, updated_at, COALESCE(created_by, '')
		FROM kaine.mining_maps
		WHERE 1=1
	`
	args := []interface{}{}
	argNum := 1

	if filter.SystemName != "" {
		query += fmt.Sprintf(" AND system_name ILIKE $%d", argNum)
		args = append(args, "%"+filter.SystemName+"%")
		argNum++
	}

	// Note: PowerState filter is applied in HTTP handler after Memgraph enrichment

	if filter.RingType != "" {
		query += fmt.Sprintf(" AND ring_type = $%d", argNum)
		args = append(args, filter.RingType)
		argNum++
	}

	if filter.ReserveLevel != "" {
		query += fmt.Sprintf(" AND reserve_level = $%d", argNum)
		args = append(args, filter.ReserveLevel)
		argNum++
	}

	if filter.Hotspot != "" {
		query += fmt.Sprintf(" AND $%d = ANY(hotspots)", argNum)
		args = append(args, filter.Hotspot)
		argNum++
	}

	query += " ORDER BY system_name, body"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query mining maps: %w", err)
	}
	defer rows.Close()

	var maps []MiningMap
	for rows.Next() {
		m, err := scanMiningMap(rows)
		if err != nil {
			return nil, err
		}
		maps = append(maps, *m)
	}

	return maps, rows.Err()
}

// GetMiningMap retrieves a single mining map by ID.
// Note: power_state is NOT in the database - it comes from Memgraph.
func (s *Store) GetMiningMap(ctx context.Context, id int) (*MiningMap, error) {
	query := `
		SELECT
			id, system_name, body,
			COALESCE(ring_type, ''), COALESCE(reserve_level, ''),
			COALESCE(res_sites, ''), COALESCE(hotspots, '{}'),
			COALESCE(map_1, ''), COALESCE(map_1_title, ''), COALESCE(map_1_commodity, '{}'),
			COALESCE(map_2, ''), COALESCE(map_2_title, ''), COALESCE(map_2_commodity, '{}'),
			COALESCE(map_3, ''), COALESCE(map_3_title, ''), COALESCE(map_3_commodity, '{}'),
			COALESCE(search_url, ''),
			COALESCE(expansion_faction, ''), COALESCE(notes, ''),
			created_at, updated_at, COALESCE(created_by, '')
		FROM kaine.mining_maps
		WHERE id = $1
	`

	rows, err := s.pool.Query(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("query mining map: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, nil // Not found
	}

	return scanMiningMap(rows)
}

// GetMiningMapBySystemBody retrieves a mining map by system name and body.
// Note: power_state is NOT in the database - it comes from Memgraph.
func (s *Store) GetMiningMapBySystemBody(ctx context.Context, systemName, body string) (*MiningMap, error) {
	query := `
		SELECT
			id, system_name, body,
			COALESCE(ring_type, ''), COALESCE(reserve_level, ''),
			COALESCE(res_sites, ''), COALESCE(hotspots, '{}'),
			COALESCE(map_1, ''), COALESCE(map_1_title, ''), COALESCE(map_1_commodity, '{}'),
			COALESCE(map_2, ''), COALESCE(map_2_title, ''), COALESCE(map_2_commodity, '{}'),
			COALESCE(map_3, ''), COALESCE(map_3_title, ''), COALESCE(map_3_commodity, '{}'),
			COALESCE(search_url, ''),
			COALESCE(expansion_faction, ''), COALESCE(notes, ''),
			created_at, updated_at, COALESCE(created_by, '')
		FROM kaine.mining_maps
		WHERE system_name = $1 AND body = $2
	`

	rows, err := s.pool.Query(ctx, query, systemName, body)
	if err != nil {
		return nil, fmt.Errorf("query mining map: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, nil // Not found
	}

	return scanMiningMap(rows)
}

// scanMiningMap scans a row into a MiningMap struct.
// Note: power_state is NOT in the query - it comes from Memgraph.
func scanMiningMap(rows pgx.Rows) (*MiningMap, error) {
	var m MiningMap
	var hotspots []string
	var map1Commodity []string
	var map2Commodity []string
	var map3Commodity []string

	err := rows.Scan(
		&m.ID, &m.SystemName, &m.Body,
		&m.RingType, &m.ReserveLevel,
		&m.RESSites, &hotspots,
		&m.Map1, &m.Map1Title, &map1Commodity,
		&m.Map2, &m.Map2Title, &map2Commodity,
		&m.Map3, &m.Map3Title, &map3Commodity,
		&m.SearchURL,
		&m.ExpansionFaction, &m.Notes,
		&m.CreatedAt, &m.UpdatedAt, &m.CreatedBy,
	)
	if err != nil {
		return nil, fmt.Errorf("scan mining map: %w", err)
	}

	m.Hotspots = hotspots
	m.Map1Commodity = map1Commodity
	m.Map2Commodity = map2Commodity
	m.Map3Commodity = map3Commodity
	// m.PowerState is populated from Memgraph by the HTTP handler
	return &m, nil
}

// CreateMiningMap creates a new mining map and returns it.
// Note: power_state is NOT stored - it comes from Memgraph.
func (s *Store) CreateMiningMap(ctx context.Context, input CreateMiningMapInput, userID string) (*MiningMap, error) {
	// Validate required fields
	if input.SystemName == "" {
		return nil, fmt.Errorf("system_name is required")
	}
	if input.Body == "" {
		return nil, fmt.Errorf("body is required")
	}

	query := `
		INSERT INTO kaine.mining_maps (
			system_name, body, ring_type, reserve_level,
			res_sites, hotspots, map_1, map_1_title, map_1_commodity,
			map_2, map_2_title, map_2_commodity,
			map_3, map_3_title, map_3_commodity, search_url,
			expansion_faction, notes, created_by
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8, $9,
			$10, $11, $12,
			$13, $14, $15, $16,
			$17, $18, $19
		) RETURNING id, created_at, updated_at
	`

	var m MiningMap
	err := s.pool.QueryRow(ctx, query,
		input.SystemName, input.Body,
		nullableString(input.RingType), nullableString(input.ReserveLevel),
		nullableString(input.RESSites), input.Hotspots,
		nullableString(input.Map1), nullableString(input.Map1Title), input.Map1Commodity,
		nullableString(input.Map2), nullableString(input.Map2Title), input.Map2Commodity,
		nullableString(input.Map3), nullableString(input.Map3Title), input.Map3Commodity,
		nullableString(input.SearchURL),
		nullableString(input.ExpansionFaction), nullableString(input.Notes), nullableString(userID),
	).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)

	if err != nil {
		// Check for unique constraint violation
		if strings.Contains(err.Error(), "unique constraint") || strings.Contains(err.Error(), "duplicate key") {
			return nil, fmt.Errorf("mining map for %s %s already exists", input.SystemName, input.Body)
		}
		return nil, fmt.Errorf("create mining map: %w", err)
	}

	// Fill in the rest from input
	m.SystemName = input.SystemName
	m.Body = input.Body
	m.RingType = input.RingType
	m.ReserveLevel = input.ReserveLevel
	// m.PowerState is populated from Memgraph by the HTTP handler
	m.RESSites = input.RESSites
	m.Hotspots = input.Hotspots
	m.Map1 = input.Map1
	m.Map1Title = input.Map1Title
	m.Map1Commodity = input.Map1Commodity
	m.Map2 = input.Map2
	m.Map2Title = input.Map2Title
	m.Map2Commodity = input.Map2Commodity
	m.Map3 = input.Map3
	m.Map3Title = input.Map3Title
	m.Map3Commodity = input.Map3Commodity
	m.SearchURL = input.SearchURL
	m.ExpansionFaction = input.ExpansionFaction
	m.Notes = input.Notes
	m.CreatedBy = userID

	return &m, nil
}

// UpdateMiningMap updates an existing mining map.
func (s *Store) UpdateMiningMap(ctx context.Context, id int, input UpdateMiningMapInput) (*MiningMap, error) {
	// Build dynamic update query
	setParts := []string{}
	args := []interface{}{}
	argNum := 1

	if input.SystemName != nil {
		setParts = append(setParts, fmt.Sprintf("system_name = $%d", argNum))
		args = append(args, *input.SystemName)
		argNum++
	}
	if input.Body != nil {
		setParts = append(setParts, fmt.Sprintf("body = $%d", argNum))
		args = append(args, *input.Body)
		argNum++
	}
	if input.RingType != nil {
		setParts = append(setParts, fmt.Sprintf("ring_type = $%d", argNum))
		args = append(args, nullableString(*input.RingType))
		argNum++
	}
	if input.ReserveLevel != nil {
		setParts = append(setParts, fmt.Sprintf("reserve_level = $%d", argNum))
		args = append(args, nullableString(*input.ReserveLevel))
		argNum++
	}
	// Note: PowerState is not stored in TimescaleDB - it comes from Memgraph
	if input.RESSites != nil {
		setParts = append(setParts, fmt.Sprintf("res_sites = $%d", argNum))
		args = append(args, nullableString(*input.RESSites))
		argNum++
	}
	if input.Hotspots != nil {
		setParts = append(setParts, fmt.Sprintf("hotspots = $%d", argNum))
		args = append(args, input.Hotspots)
		argNum++
	}
	if input.Map1 != nil {
		setParts = append(setParts, fmt.Sprintf("map_1 = $%d", argNum))
		args = append(args, nullableString(*input.Map1))
		argNum++
	}
	if input.Map1Title != nil {
		setParts = append(setParts, fmt.Sprintf("map_1_title = $%d", argNum))
		args = append(args, nullableString(*input.Map1Title))
		argNum++
	}
	if input.Map1Commodity != nil {
		setParts = append(setParts, fmt.Sprintf("map_1_commodity = $%d", argNum))
		args = append(args, input.Map1Commodity)
		argNum++
	}
	if input.Map2 != nil {
		setParts = append(setParts, fmt.Sprintf("map_2 = $%d", argNum))
		args = append(args, nullableString(*input.Map2))
		argNum++
	}
	if input.Map2Title != nil {
		setParts = append(setParts, fmt.Sprintf("map_2_title = $%d", argNum))
		args = append(args, nullableString(*input.Map2Title))
		argNum++
	}
	if input.Map2Commodity != nil {
		setParts = append(setParts, fmt.Sprintf("map_2_commodity = $%d", argNum))
		args = append(args, input.Map2Commodity)
		argNum++
	}
	if input.Map3 != nil {
		setParts = append(setParts, fmt.Sprintf("map_3 = $%d", argNum))
		args = append(args, nullableString(*input.Map3))
		argNum++
	}
	if input.Map3Title != nil {
		setParts = append(setParts, fmt.Sprintf("map_3_title = $%d", argNum))
		args = append(args, nullableString(*input.Map3Title))
		argNum++
	}
	if input.Map3Commodity != nil {
		setParts = append(setParts, fmt.Sprintf("map_3_commodity = $%d", argNum))
		args = append(args, input.Map3Commodity)
		argNum++
	}
	if input.SearchURL != nil {
		setParts = append(setParts, fmt.Sprintf("search_url = $%d", argNum))
		args = append(args, nullableString(*input.SearchURL))
		argNum++
	}
	if input.ExpansionFaction != nil {
		setParts = append(setParts, fmt.Sprintf("expansion_faction = $%d", argNum))
		args = append(args, nullableString(*input.ExpansionFaction))
		argNum++
	}
	if input.Notes != nil {
		setParts = append(setParts, fmt.Sprintf("notes = $%d", argNum))
		args = append(args, nullableString(*input.Notes))
		argNum++
	}

	if len(setParts) == 0 {
		return s.GetMiningMap(ctx, id)
	}

	// Always update updated_at
	setParts = append(setParts, fmt.Sprintf("updated_at = NOW()"))

	query := fmt.Sprintf("UPDATE kaine.mining_maps SET %s WHERE id = $%d",
		strings.Join(setParts, ", "), argNum)
	args = append(args, id)

	_, err := s.pool.Exec(ctx, query, args...)
	if err != nil {
		if strings.Contains(err.Error(), "unique constraint") || strings.Contains(err.Error(), "duplicate key") {
			return nil, fmt.Errorf("mining map already exists for that system/body combination")
		}
		return nil, fmt.Errorf("update mining map: %w", err)
	}

	return s.GetMiningMap(ctx, id)
}

// UpsertMiningMap creates or updates a mining map based on (system_name, body).
// Returns the mining map, whether it was created (true) or updated (false), and any error.
func (s *Store) UpsertMiningMap(ctx context.Context, input CreateMiningMapInput, userID string) (*MiningMap, bool, error) {
	existing, err := s.GetMiningMapBySystemBody(ctx, input.SystemName, input.Body)
	if err != nil {
		return nil, false, fmt.Errorf("check existing mining map: %w", err)
	}

	if existing == nil {
		m, err := s.CreateMiningMap(ctx, input, userID)
		if err != nil {
			return nil, false, err
		}
		return m, true, nil
	}

	// Build update input from all fields
	update := UpdateMiningMapInput{
		SystemName:       &input.SystemName,
		Body:             &input.Body,
		RingType:         &input.RingType,
		ReserveLevel:     &input.ReserveLevel,
		RESSites:         &input.RESSites,
		Hotspots:         input.Hotspots,
		Map1:             &input.Map1,
		Map1Title:        &input.Map1Title,
		Map1Commodity:    input.Map1Commodity,
		Map2:             &input.Map2,
		Map2Title:        &input.Map2Title,
		Map2Commodity:    input.Map2Commodity,
		SearchURL:        &input.SearchURL,
		ExpansionFaction: &input.ExpansionFaction,
		Notes:            &input.Notes,
	}

	m, err := s.UpdateMiningMap(ctx, existing.ID, update)
	if err != nil {
		return nil, false, err
	}
	return m, false, nil
}

// DeleteMiningMap deletes a mining map by ID.
func (s *Store) DeleteMiningMap(ctx context.Context, id int) error {
	query := `DELETE FROM kaine.mining_maps WHERE id = $1`
	result, err := s.pool.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("delete mining map: %w", err)
	}
	if result.RowsAffected() == 0 {
		return fmt.Errorf("mining map not found")
	}
	return nil
}

// GetMiningMapStats returns summary statistics for mining maps.
// Note: by_power_state is calculated in the HTTP handler from Memgraph data.
func (s *Store) GetMiningMapStats(ctx context.Context) (map[string]interface{}, error) {
	stats := make(map[string]interface{})

	// Total count
	var total int
	err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM kaine.mining_maps").Scan(&total)
	if err != nil {
		return nil, fmt.Errorf("count mining maps: %w", err)
	}
	stats["total"] = total

	// Note: by_power_state is calculated in the HTTP handler from Memgraph data

	// By ring type
	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(ring_type, 'Unknown'), COUNT(*)
		FROM kaine.mining_maps
		GROUP BY ring_type
		ORDER BY COUNT(*) DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("query ring type stats: %w", err)
	}
	defer rows.Close()

	byRingType := make(map[string]int)
	for rows.Next() {
		var ringType string
		var count int
		if err := rows.Scan(&ringType, &count); err != nil {
			return nil, fmt.Errorf("scan ring type stats: %w", err)
		}
		byRingType[ringType] = count
	}
	stats["by_ring_type"] = byRingType

	return stats, nil
}
