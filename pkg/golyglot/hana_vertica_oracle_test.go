package golyglot

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"testing"
	"time"
)

// Execute generated SQL against an isolated target engine. These are result
// regressions based on documented source behavior, not claims of live HANA
// or Vertica differential testing.
func TestHANAVerticaDuckDBResults(t *testing.T) {
	if os.Getenv("GOLYGLOT_DUCKDB_ORACLE") != "1" {
		t.Skip("set GOLYGLOT_DUCKDB_ORACLE=1 for execution-based regressions")
	}
	duckdb, err := exec.LookPath("duckdb")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, sql, want string
		dialect         Dialect
	}{
		{"dummy star", `SELECT * FROM DUMMY`, `[{"DUMMY":"X"}]`, DialectHANA},
		{"dummy alias", `SELECT d.DUMMY AS answer FROM SYS.DUMMY d`, `[{"answer":"X"}]`, DialectHANA},
		{"dummy join", `SELECT COUNT(*) AS answer FROM DUMMY a CROSS JOIN DUMMY b`, `[{"answer":1}]`, DialectHANA},
		{"dummy filtered", `SELECT COUNT(*) AS answer FROM DUMMY WHERE DUMMY = 'Y'`, `[{"answer":0}]`, DialectHANA},
		{"dummy cte body", `WITH DUMMY AS (SELECT * FROM DUMMY) SELECT * FROM DUMMY`, `[{"DUMMY":"X"}]`, DialectHANA},
		{"dummy shadow", `WITH DUMMY AS (SELECT 7 AS answer) SELECT * FROM DUMMY`, `[{"answer":7}]`, DialectHANA},
		{"dummy sibling scope", `WITH DUMMY AS (SELECT 7 AS answer), c AS (SELECT * FROM DUMMY) SELECT * FROM c`, `[{"answer":7}]`, DialectHANA},
		{"dummy explicit system", `WITH DUMMY AS (SELECT 7 AS answer) SELECT * FROM SYS.DUMMY`, `[{"DUMMY":"X"}]`, DialectHANA},
		{"dummy quoted shadow", `WITH "dummy" AS (SELECT 9 AS answer) SELECT * FROM "dummy"`, `[{"answer":9}]`, DialectHANA},
		{"hana null order", `WITH t AS (SELECT 1 AS v FROM DUMMY UNION ALL SELECT NULL AS v FROM DUMMY) SELECT v AS answer FROM t ORDER BY v`, `[{"answer":null},{"answer":1}]`, DialectHANA},
		{"wide integer", `SELECT CAST(2147483648 AS INT) AS answer`, `[{"answer":2147483648}]`, DialectVertica},
		{"zeroifnull", `SELECT ZEROIFNULL(NULL) AS answer`, `[{"answer":0}]`, DialectVertica},
		{"decode null", `SELECT DECODE(NULL, NULL, 'same', 'different') AS answer`, `[{"answer":"same"}]`, DialectVertica},
		{"timestamp literal", `SELECT TIMESTAMPADD(DAY, 3, TIMESTAMP '2026-01-01 12:00:00') AS answer`, `[{"answer":"2026-01-04 12:00:00"}]`, DialectVertica},
		{"timestamp expression", `SELECT TIMESTAMPADD(MONTH, n + 1, TIMESTAMP '2026-01-31 12:00:00') AS answer FROM (SELECT 0 AS n) t`, `[{"answer":"2026-02-28 12:00:00"}]`, DialectVertica},
		{"timestamp unit alias", `SELECT TIMESTAMPADD(SQL_TSI_DAY, 1, TIMESTAMP '2026-01-01') AS answer`, `[{"answer":"2026-01-02 00:00:00"}]`, DialectVertica},
		{"integer division", `SELECT 7 // 2 AS answer`, `[{"answer":3}]`, DialectVertica},
		{"factorial", `SELECT CAST(5! AS VARCHAR) AS answer`, `[{"answer":"120"}]`, DialectVertica},
		{"integer nulls", `WITH t AS (SELECT 1 AS v UNION ALL SELECT NULL UNION ALL SELECT 2) SELECT CAST(v AS INT) AS answer FROM t ORDER BY answer`, `[{"answer":null},{"answer":1},{"answer":2}]`, DialectVertica},
		{"float nulls descending", `WITH t AS (SELECT 1 AS v UNION ALL SELECT NULL UNION ALL SELECT 2) SELECT CAST(v AS FLOAT) AS answer FROM t ORDER BY answer DESC`, `[{"answer":null},{"answer":2},{"answer":1}]`, DialectVertica},
		{"compound integer nulls", `SELECT CAST(NULL AS INT) AS answer UNION ALL SELECT CAST(1 AS INT) AS answer ORDER BY 1`, `[{"answer":null},{"answer":1}]`, DialectVertica},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, err := TranspileOne(tc.sql, tc.dialect, DialectDuckDB)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, duckdb, ":memory:", "-safe", "-no-init", "-batch", "-bail", "-json", "-c", sql).CombinedOutput()
			if err != nil {
				t.Fatalf("%s: %v\n%s", sql, err, out)
			}
			var got, want any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s\ngot %s, want %s", sql, out, tc.want)
			}
		})
	}
}
