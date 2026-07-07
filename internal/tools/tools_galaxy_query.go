package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	pgquery "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	galaxyQueryRowCap           = 100
	galaxyQueryStatementTimeout = 2 * time.Second
	galaxyQueryWorkMem          = "16MB"
)

var forbiddenSQLFunctions = map[string]struct{}{
	"nextval":                   {},
	"pg_advisory_lock":          {},
	"pg_advisory_xact_lock":     {},
	"pg_notify":                 {},
	"pg_sleep":                  {},
	"pg_try_advisory_lock":      {},
	"pg_try_advisory_xact_lock": {},
	"setval":                    {},
}

// galaxyQuery executes one parser-validated, read-only SQL statement against galaxy.*.
func (e *Executor) galaxyQuery(ctx context.Context, args map[string]any) (any, error) {
	store, err := e.requireGalaxyStore()
	if err != nil {
		return nil, err
	}

	query := strings.TrimSpace(getString(args, "query"))
	if query == "" {
		return nil, errors.New("query is required")
	}

	validated, err := validateGalaxySQLQuery(query)
	if err != nil {
		return map[string]any{
			"error": err.Error(),
			"hint":  "Submit exactly one read-only PostgreSQL SELECT/WITH statement. DDL, DML, SET/RESET, transaction control, DO/CALL, row locks, and side-effect functions are rejected.",
		}, nil
	}

	params := make(map[string]any)
	if p, ok := args["parameters"].(map[string]any); ok {
		params = p
	}

	queryCtx, cancel := context.WithTimeout(ctx, galaxyQueryStatementTimeout+time.Second)
	defer cancel()

	start := time.Now()
	tx, err := store.BeginReadOnly(queryCtx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(queryCtx, fmt.Sprintf("SET LOCAL statement_timeout = '%dms'", galaxyQueryStatementTimeout.Milliseconds())); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(queryCtx, "SET LOCAL work_mem = '"+galaxyQueryWorkMem+"'"); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(queryCtx, "SET LOCAL default_transaction_read_only = on"); err != nil {
		return nil, err
	}

	bindArgs, err := pgqueryArgs(params)
	if err != nil {
		return map[string]any{
			"error": err.Error(),
			"hint":  "Use PostgreSQL positional placeholders ($1, $2, ...) and pass parameters as object keys \"1\", \"2\", ...",
		}, nil
	}
	rows, err := tx.Query(queryCtx, validated.SQL, bindArgs...)
	elapsed := time.Since(start)
	if err != nil {
		return map[string]any{
			"error":             err.Error(),
			"query":             validated.SQL,
			"execution_time_ms": elapsed.Milliseconds(),
			"source":            "postgres",
		}, nil
	}

	columns := make([]string, 0, len(rows.FieldDescriptions()))
	for _, field := range rows.FieldDescriptions() {
		columns = append(columns, field.Name)
	}
	resultRows, err := scanGalaxyRows(queryCtx, rows)
	elapsed = time.Since(start)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, err
	}

	return map[string]any{
		"query":             validated.SQL,
		"columns":           columns,
		"rows":              resultRows,
		"row_count":         len(resultRows),
		"row_cap":           galaxyQueryRowCap,
		"execution_time_ms": elapsed.Milliseconds(),
		"source":            "postgres",
	}, nil
}

type validatedGalaxySQL struct {
	SQL string
}

func validateGalaxySQLQuery(input string) (validatedGalaxySQL, error) {
	sql := strings.TrimSpace(strings.TrimSuffix(input, ";"))
	if sql == "" {
		return validatedGalaxySQL{}, errors.New("query is required")
	}

	tree, err := pgquery.Parse(sql)
	if err != nil {
		return validatedGalaxySQL{}, fmt.Errorf("SQL parse failed: %w", err)
	}
	if len(tree.GetStmts()) != 1 {
		return validatedGalaxySQL{}, errors.New("exactly one SQL statement is required")
	}

	raw := tree.GetStmts()[0]
	stmt := raw.GetStmt()
	if stmt == nil || stmt.GetSelectStmt() == nil {
		return validatedGalaxySQL{}, errors.New("only SELECT/WITH SELECT statements are allowed")
	}
	if err := validateSelectStmt(stmt.GetSelectStmt()); err != nil {
		return validatedGalaxySQL{}, err
	}
	if err := rejectForbiddenSQLNodes(stmt); err != nil {
		return validatedGalaxySQL{}, err
	}

	return validatedGalaxySQL{
		SQL: fmt.Sprintf("SELECT * FROM (%s) AS galaxy_query_result LIMIT %d", sql, galaxyQueryRowCap),
	}, nil
}

func validateSelectStmt(stmt *pgquery.SelectStmt) error {
	if stmt == nil {
		return errors.New("only SELECT statements are allowed")
	}
	if stmt.GetIntoClause() != nil {
		return errors.New("SELECT INTO is not allowed")
	}
	if len(stmt.GetLockingClause()) > 0 {
		return errors.New("row-locking SELECT clauses are not allowed")
	}
	if stmt.GetLarg() != nil {
		if err := validateSelectStmt(stmt.GetLarg()); err != nil {
			return err
		}
	}
	if stmt.GetRarg() != nil {
		if err := validateSelectStmt(stmt.GetRarg()); err != nil {
			return err
		}
	}
	if with := stmt.GetWithClause(); with != nil {
		for _, cteNode := range with.GetCtes() {
			cte := cteNode.GetCommonTableExpr()
			if cte == nil || cte.GetCtequery() == nil || cte.GetCtequery().GetSelectStmt() == nil {
				return errors.New("CTEs must be read-only SELECT statements")
			}
			if err := validateSelectStmt(cte.GetCtequery().GetSelectStmt()); err != nil {
				return err
			}
		}
	}
	return nil
}

func rejectForbiddenSQLNodes(node *pgquery.Node) error {
	if node == nil {
		return nil
	}
	switch typed := node.GetNode().(type) {
	case *pgquery.Node_InsertStmt,
		*pgquery.Node_DeleteStmt,
		*pgquery.Node_UpdateStmt,
		*pgquery.Node_MergeStmt,
		*pgquery.Node_CopyStmt,
		*pgquery.Node_CallStmt,
		*pgquery.Node_DoStmt,
		*pgquery.Node_CreateSchemaStmt,
		*pgquery.Node_CreateStmt,
		*pgquery.Node_CreateTableAsStmt,
		*pgquery.Node_AlterTableStmt,
		*pgquery.Node_DropStmt,
		*pgquery.Node_TruncateStmt,
		*pgquery.Node_GrantStmt,
		*pgquery.Node_GrantRoleStmt,
		*pgquery.Node_VariableSetStmt,
		*pgquery.Node_VariableShowStmt,
		*pgquery.Node_TransactionStmt,
		*pgquery.Node_NotifyStmt,
		*pgquery.Node_ListenStmt,
		*pgquery.Node_UnlistenStmt,
		*pgquery.Node_LockStmt,
		*pgquery.Node_ExplainStmt,
		*pgquery.Node_VacuumStmt,
		*pgquery.Node_PrepareStmt,
		*pgquery.Node_ExecuteStmt,
		*pgquery.Node_DeallocateStmt,
		*pgquery.Node_DiscardStmt,
		*pgquery.Node_LoadStmt,
		*pgquery.Node_CheckPointStmt,
		*pgquery.Node_DeclareCursorStmt,
		*pgquery.Node_FetchStmt,
		*pgquery.Node_ClosePortalStmt,
		*pgquery.Node_IndexStmt,
		*pgquery.Node_RuleStmt,
		*pgquery.Node_ViewStmt,
		*pgquery.Node_CreateExtensionStmt,
		*pgquery.Node_AlterExtensionStmt,
		*pgquery.Node_CreateRoleStmt,
		*pgquery.Node_AlterRoleStmt,
		*pgquery.Node_DropRoleStmt,
		*pgquery.Node_AlterSystemStmt:
		return fmt.Errorf("statement type %T is not allowed", typed)
	case *pgquery.Node_LockingClause:
		return errors.New("row-locking SELECT clauses are not allowed")
	case *pgquery.Node_NextValueExpr:
		return errors.New("sequence functions are not allowed")
	case *pgquery.Node_FuncCall:
		name := normalizedFuncName(typed.FuncCall)
		if _, blocked := forbiddenSQLFunctions[name]; blocked {
			return fmt.Errorf("function %s is not allowed", name)
		}
	}
	return walkSQLNode(node.ProtoReflect(), rejectForbiddenSQLNodes)
}

func walkSQLNode(msg protoreflect.Message, fn func(*pgquery.Node) error) error {
	var walkErr error
	msg.Range(func(fd protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if walkErr != nil {
			return false
		}
		if fd.IsList() {
			list := value.List()
			for i := 0; i < list.Len(); i++ {
				if err := walkSQLValue(fd, list.Get(i), fn); err != nil {
					walkErr = err
					return false
				}
			}
			return true
		}
		if err := walkSQLValue(fd, value, fn); err != nil {
			walkErr = err
			return false
		}
		return true
	})
	return walkErr
}

func walkSQLValue(fd protoreflect.FieldDescriptor, value protoreflect.Value, fn func(*pgquery.Node) error) error {
	if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
		return nil
	}
	msg := value.Message()
	if !msg.IsValid() {
		return nil
	}
	if node, ok := msg.Interface().(*pgquery.Node); ok {
		return fn(node)
	}
	return walkSQLNode(msg, fn)
}

func normalizedFuncName(call *pgquery.FuncCall) string {
	var parts []string
	for _, part := range call.GetFuncname() {
		if s := part.GetString_(); s != nil {
			parts = append(parts, strings.ToLower(s.GetSval()))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

func pgqueryArgs(params map[string]any) ([]any, error) {
	if len(params) == 0 {
		return nil, nil
	}
	type keyedParam struct {
		key string
		num int
		val any
	}
	items := make([]keyedParam, 0, len(params))
	for key := range params {
		num, err := strconv.Atoi(key)
		if err != nil || num <= 0 {
			return nil, fmt.Errorf("parameter key %q is invalid; use \"1\", \"2\", ... for SQL placeholders $1, $2, ...", key)
		}
		items = append(items, keyedParam{key: key, num: num, val: params[key]})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].num == items[j].num {
			return items[i].key < items[j].key
		}
		return items[i].num < items[j].num
	})
	out := make([]any, 0, len(items))
	for i, item := range items {
		if item.num != i+1 {
			return nil, fmt.Errorf("parameter keys must be contiguous from \"1\"; missing %q", strconv.Itoa(i+1))
		}
		out = append(out, item.val)
	}
	return out, nil
}
