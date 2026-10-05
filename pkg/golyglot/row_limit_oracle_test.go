package golyglot

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRowLimitDuckDBResults(t *testing.T) {
	if os.Getenv("GOLYGLOT_DUCKDB_ORACLE") != "1" {
		t.Skip("set GOLYGLOT_DUCKDB_ORACLE=1 for execution-based regressions")
	}
	for _, tt := range []struct{ name, sql, want string }{
		{"left branch", "SELECT TOP 1 1 AS a UNION ALL SELECT 2 AS a UNION ALL SELECT 3 AS a ORDER BY a", `[{"a":1},{"a":2},{"a":3}]`},
		{"right branch", "SELECT 1 AS a UNION ALL SELECT TOP 1 2 AS a ORDER BY a", `[{"a":1},{"a":2}]`},
		{"parenthesized right branch", "SELECT 1 AS a UNION ALL (SELECT TOP 0 2 AS a)", `[{"a":1}]`},
		{"parenthesized fetch", "SELECT 1 AS a UNION ALL (SELECT 2 AS a OFFSET 0 ROWS FETCH FIRST 0 ROWS ONLY)", `[{"a":1}]`},
		{"both branches", "SELECT TOP 1 1 AS a UNION ALL SELECT TOP 1 2 AS a ORDER BY a", `[{"a":1},{"a":2}]`},
		{"cte scope", "WITH c AS (SELECT 1 AS a UNION ALL SELECT 1 AS a) SELECT TOP 1 a FROM c UNION ALL SELECT a FROM c ORDER BY a", `[{"a":1},{"a":1},{"a":1}]`},
		{"local order", "WITH c AS (SELECT 1 AS a UNION ALL SELECT 2 AS a) (SELECT TOP 1 a FROM c ORDER BY a DESC) UNION ALL SELECT TOP 1 3 AS a ORDER BY a", `[{"a":2},{"a":3}]`},
		{"intersect", "SELECT TOP 1 1 AS a INTERSECT SELECT 1 AS a", `[{"a":1}]`},
		{"except", "SELECT 1 AS a EXCEPT SELECT TOP 0 1 AS a", `[{"a":1}]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, pretty := range []bool{false, true} {
				queries, err := TranspileWithOptions(tt.sql, DialectTSQL, DialectDuckDB, TranspileOptions{Pretty: pretty})
				if err != nil {
					t.Fatal(err)
				}
				got := runRowLimitSQL(t, queries[0], false)
				if !reflect.DeepEqual(got, decodeRowLimitJSON(t, []byte(tt.want))) {
					t.Fatalf("%s: got %v, want %s", queries[0], got, tt.want)
				}
			}
		})
	}
}

func TestDuckDBPostgresSemanticResults(t *testing.T) {
	if os.Getenv("GOLYGLOT_POSTGRES_CONTAINER") == "" {
		t.Skip("set GOLYGLOT_POSTGRES_CONTAINER to an isolated PostgreSQL fixture")
	}
	for _, sql := range []string{
		"SELECT 7 // 2 AS answer",
		"SELECT -7 // 2 AS answer",
		"SELECT 7 // -2 AS answer",
		"SELECT 7 // 0 AS answer",
		"SELECT 2147483648 // 2 AS answer",
		"SELECT CAST(7 AS SMALLINT) // CAST(2 AS SMALLINT) AS answer",
		"SELECT CAST(7 AS BIGINT) // CAST(2 AS INTEGER) AS answer",
		"SELECT CAST(NULL AS INTEGER) // 2 AS answer",
		"SELECT 7 // CAST(NULL AS INTEGER) AS answer",
		"SELECT (7 // 2) // 2 AS answer",
		"SELECT 7 // (4 // 2) AS answer",
		"SELECT CAST(a AS INTEGER) // CAST(b AS BIGINT) AS answer FROM (VALUES (7, 2), (-7, 2), (7, 0), (NULL, 2)) AS t(a, b) ORDER BY answer",
		"SELECT a, b FROM (VALUES (NULL, 2), (1, 3), (2, 1), (1, NULL)) AS t(a, b) ORDER BY ALL",
		"SELECT a, b FROM (VALUES (NULL, 2), (1, 3), (2, 1), (1, NULL)) AS t(a, b) ORDER BY ALL DESC",
		"SELECT a, b FROM (VALUES (NULL, 2), (1, 3), (2, 1), (1, NULL)) AS t(a, b) ORDER BY ALL DESC NULLS FIRST",
		"SELECT 1 AS a, 2 AS b UNION ALL SELECT 2, NULL UNION ALL SELECT NULL, 3 ORDER BY ALL DESC",
	} {
		t.Run(sql, func(t *testing.T) {
			want := runRowLimitSQL(t, sql, false)
			for _, pretty := range []bool{false, true} {
				queries, err := TranspileWithOptions(sql, DialectDuckDB, DialectPostgreSQL, TranspileOptions{Pretty: pretty})
				if err != nil {
					t.Fatal(err)
				}
				got := runRowLimitSQL(t, queries[0], true)
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("%s: got %v, DuckDB returned %v", queries[0], got, want)
				}
			}
		})
	}
	for _, tt := range []struct{ expression, want string }{
		{"7 // 2", "INTEGER"},
		{"CAST(7 AS SMALLINT) // CAST(2 AS SMALLINT)", "SMALLINT"},
		{"2147483648 // 2", "BIGINT"},
	} {
		sql := "SELECT " + tt.expression + " AS answer"
		generated, err := TranspileOne(sql, DialectDuckDB, DialectPostgreSQL)
		if err != nil {
			t.Fatal(err)
		}
		duckType := runRowLimitSQL(t, "SELECT UPPER(typeof(answer)) AS type FROM ("+sql+") q", false)
		pgType := runRowLimitSQL(t, "SELECT UPPER(CAST(pg_typeof(answer) AS TEXT)) AS type FROM ("+generated+") q", true)
		want := decodeRowLimitJSON(t, []byte(`[{"type":"`+tt.want+`"}]`))
		if !reflect.DeepEqual(duckType, want) || !reflect.DeepEqual(pgType, want) {
			t.Fatalf("division type: DuckDB %v, PostgreSQL %v, want %v", duckType, pgType, want)
		}
	}
}

func TestRowLimitSQLiteResults(t *testing.T) {
	if os.Getenv("GOLYGLOT_DUCKDB_ORACLE") != "1" {
		t.Skip("opt-in local engine checks")
	}
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 CLI is not installed")
	}
	for _, sql := range []string{
		"SELECT TOP 1 1 AS a UNION ALL SELECT TOP 1 2 AS a ORDER BY a",
		"SELECT 1 AS a UNION ALL (SELECT TOP 0 2 AS a)",
		"SELECT 1 AS a UNION ALL (SELECT 2 AS a OFFSET 0 ROWS FETCH FIRST 0 ROWS ONLY)",
		"WITH c AS (SELECT 1 AS a UNION ALL SELECT 2 AS a) (SELECT TOP 1 a FROM c ORDER BY a DESC) UNION ALL SELECT TOP 1 3 AS a ORDER BY a",
	} {
		duck, err := TranspileOne(sql, DialectTSQL, DialectDuckDB)
		if err != nil {
			t.Fatal(err)
		}
		want := runRowLimitSQL(t, duck, false)
		for _, pretty := range []bool{false, true} {
			queries, err := TranspileWithOptions(sql, DialectTSQL, DialectSQLite, TranspileOptions{Pretty: pretty})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			out, err := exec.CommandContext(ctx, sqlite, "-safe", "-json", ":memory:", queries[0]).CombinedOutput()
			cancel()
			if err != nil {
				t.Fatalf("%s: %v\n%s", queries[0], err, out)
			}
			if got := decodeRowLimitJSON(t, out); !reflect.DeepEqual(got, want) {
				t.Fatalf("SQLite %v, DuckDB %v", got, want)
			}
		}
	}
}

func runRowLimitSQL(t *testing.T, sql string, postgres bool) any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if postgres {
		sql = "SELECT COALESCE(json_agg(row_to_json(result)), '[]'::json) FROM (" + sql + ") AS result"
		cmd = exec.CommandContext(ctx, "docker", "exec", os.Getenv("GOLYGLOT_POSTGRES_CONTAINER"), "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-At", "-c", sql)
	} else {
		cmd = exec.CommandContext(ctx, "duckdb", ":memory:", "-safe", "-no-init", "-batch", "-bail", "-json", "-c", sql)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", sql, err, out)
	}
	return decodeRowLimitJSON(t, out)
}

func decodeRowLimitJSON(t *testing.T, out []byte) any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(string(out)))
	decoder.UseNumber()
	var result any
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("JSON %s: %v", out, err)
	}
	return result
}
