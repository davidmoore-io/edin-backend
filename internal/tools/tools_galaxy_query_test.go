package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateGalaxySQLQueryAcceptsReadOnlySelects(t *testing.T) {
	tests := []string{
		`SELECT name FROM galaxy.system_catalog WHERE name ILIKE 'Sol'`,
		`WITH systems AS (SELECT name FROM galaxy.system_catalog LIMIT 5) SELECT * FROM systems`,
		`SELECT c.name, count(*) FROM galaxy.system_catalog c GROUP BY c.name ORDER BY c.name LIMIT 10`,
	}

	for _, query := range tests {
		t.Run(query, func(t *testing.T) {
			validated, err := validateGalaxySQLQuery(query)
			require.NoError(t, err)
			require.Contains(t, validated.SQL, "AS galaxy_query_result LIMIT 100")
		})
	}
}

func TestValidateGalaxySQLQueryRejectsUnsafeSQL(t *testing.T) {
	tests := map[string]string{
		"multi_statement":    `SELECT 1; SELECT 2`,
		"set":                `SET statement_timeout = 0`,
		"reset":              `RESET statement_timeout`,
		"transaction":        `BEGIN`,
		"do":                 `DO $$ BEGIN RAISE NOTICE 'x'; END $$`,
		"ddl":                `CREATE TABLE galaxy.bad (id int)`,
		"dml":                `UPDATE galaxy.system SET population = 1`,
		"data_modifying_cte": `WITH changed AS (DELETE FROM galaxy.system RETURNING id64) SELECT * FROM changed`,
		"row_locking":        `SELECT * FROM galaxy.system_catalog FOR UPDATE`,
		"side_effect_func":   `SELECT pg_sleep(1)`,
		"advisory_lock":      `SELECT pg_advisory_lock(1)`,
		"sequence_func":      `SELECT nextval('galaxy.commodity_commodity_id_seq')`,
		"explain":            `EXPLAIN SELECT * FROM galaxy.system_catalog`,
		"show":               `SHOW statement_timeout`,
		"copy":               `COPY galaxy.system_catalog TO STDOUT`,
		"call":               `CALL some_proc()`,
		"select_into":        `SELECT * INTO TEMP tmp_systems FROM galaxy.system_catalog`,
	}

	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := validateGalaxySQLQuery(query)
			require.Error(t, err)
		})
	}
}

func TestValidateGalaxySQLQueryStripsOneTrailingSemicolon(t *testing.T) {
	validated, err := validateGalaxySQLQuery(`SELECT 1;`)
	require.NoError(t, err)
	require.False(t, strings.Contains(validated.SQL, "SELECT 1;"))
}

func TestGalaxyQueryArgsRequireNumericContiguousKeys(t *testing.T) {
	args, err := pgqueryArgs(map[string]any{"2": "two", "1": "one"})
	require.NoError(t, err)
	require.Equal(t, []any{"one", "two"}, args)

	_, err = pgqueryArgs(map[string]any{"name": "Sol"})
	require.Error(t, err)

	_, err = pgqueryArgs(map[string]any{"2": "two"})
	require.Error(t, err)
}
