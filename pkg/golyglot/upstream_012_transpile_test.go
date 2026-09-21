package golyglot

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// These cases check results, not just SQL spelling. The source expectations
// follow Athena/Trino semantics; no warehouse credentials are needed.
func TestAthenaDuckDBResults(t *testing.T) {
	if os.Getenv("GOLYGLOT_DUCKDB_ORACLE") != "1" {
		t.Skip("set GOLYGLOT_DUCKDB_ORACLE=1 for execution-based regressions")
	}
	duckdb, err := exec.LookPath("duckdb")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, sql, want string }{
		{"null winner", `SELECT max_by(v,k) AS answer FROM (VALUES ('older',1),(CAST(NULL AS VARCHAR),2)) t(v,k)`, `[{"answer":null}]`},
		{"min null winner", `SELECT min_by(v,k) AS answer FROM (VALUES ('newer',2),(CAST(NULL AS VARCHAR),1)) t(v,k)`, `[{"answer":null}]`},
		{"max top n with null", `SELECT max_by(v,k,2) AS answer FROM (VALUES ('older',1),(CAST(NULL AS VARCHAR),2),('ignored',NULL)) t(v,k)`, `[{"answer":[null,"older"]}]`},
		{"min top n with null", `SELECT min_by(v,k,2) AS answer FROM (VALUES ('newer',2),(CAST(NULL AS VARCHAR),1),('ignored',NULL)) t(v,k)`, `[{"answer":[null,"newer"]}]`},
		{"top n filter", `SELECT max_by(v,k,2) FILTER (WHERE keep OR k=1) AS answer FROM (VALUES ('older',1,false),(CAST(NULL AS VARCHAR),2,true),('ignored',3,false)) t(v,k,keep)`, `[{"answer":[null,"older"]}]`},
		{"top n empty", `SELECT max_by(v,k,2) AS answer FROM (VALUES ('ignored',CAST(NULL AS INTEGER))) t(v,k)`, `[{"answer":null}]`},
		{"top n window", `SELECT max_by(v,k,2) OVER () AS answer FROM (VALUES ('older',1),(CAST(NULL AS VARCHAR),2)) t(v,k) LIMIT 1`, `[{"answer":[null,"older"]}]`},
		{"filter", `SELECT max_by(v,k) FILTER (WHERE keep) AS answer FROM (VALUES ('kept',1,true),('ignored',2,false)) t(v,k,keep)`, `[{"answer":"kept"}]`},
		{"no regex match", `SELECT regexp_extract('no-match','([0-9]+)',1) AS answer`, `[{"answer":null}]`},
		{"empty regex match", `SELECT regexp_extract('a','(b*)',1) AS answer`, `[{"answer":""}]`},
		{"whole regex match", `SELECT regexp_extract('abc12def','[0-9]+') AS answer`, `[{"answer":"12"}]`},
		{"replace captures", `SELECT regexp_replace('2026.09','([0-9]+)[.]([0-9]+)','$1-$2') AS answer`, `[{"answer":"2026-09"}]`},
		{"named captures", `SELECT regexp_replace('2026.09','(?<year>[0-9]+)[.]([0-9]+)','${year}-$2') AS answer`, `[{"answer":"2026-09"}]`},
		{"escaped dollar", `SELECT regexp_replace('x','(x)','\$1') AS answer`, `[{"answer":"$1"}]`},
		{"optional group", `SELECT regexp_extract('b','(a)?b',1) AS answer`, `[{"answer":null}]`},
		{"replace all", `SELECT regexp_replace('a1b2','[0-9]','x') AS answer`, `[{"answer":"axbx"}]`},
		{"remove matches", `SELECT regexp_replace('a1b2','[0-9]') AS answer`, `[{"answer":"ab"}]`},
		{"timestamp", `SELECT to_iso8601(TIMESTAMP '2026-05-26 02:08:02.930') AS answer`, `[{"answer":"2026-05-26T02:08:02.930"}]`},
		{"timestamp seconds", `SELECT to_iso8601(TIMESTAMP '2026-05-26 02:08:02') AS answer`, `[{"answer":"2026-05-26T02:08:02"}]`},
		{"timestamp tenths", `SELECT to_iso8601(TIMESTAMP '2026-05-26 02:08:02.1') AS answer`, `[{"answer":"2026-05-26T02:08:02.1"}]`},
		{"timestamp hundredths", `SELECT to_iso8601(TIMESTAMP '2026-05-26 02:08:02.12') AS answer`, `[{"answer":"2026-05-26T02:08:02.12"}]`},
		{"date", `SELECT to_iso8601(DATE '2026-05-26') AS answer`, `[{"answer":"2026-05-26"}]`},
		{"null date", `SELECT to_iso8601(CAST(NULL AS DATE)) AS answer`, `[{"answer":null}]`},
		{"timestamp micros", `SELECT to_iso8601(TIMESTAMP '2026-05-26 02:08:02.123456') AS answer`, `[{"answer":"2026-05-26T02:08:02.123456"}]`},
		{"timestamp explicit precision", `SELECT to_iso8601(CAST('2026-05-26 02:08:02' AS TIMESTAMP(6))) AS answer`, `[{"answer":"2026-05-26T02:08:02.000000"}]`},
		{"timestamp null", `SELECT to_iso8601(CAST(NULL AS TIMESTAMP)) AS answer`, `[{"answer":null}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sql, err := TranspileOne(tc.sql, DialectAthena, DialectDuckDB)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			out, err := exec.CommandContext(ctx, duckdb, "-safe", "-no-init", "-batch", "-bail", "-json", "-c", sql).CombinedOutput()
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
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("%s\ngot %s, want %s", sql, gotJSON, wantJSON)
			}
		})
	}
}

func TestTrinoRewriteDoesNotHideUnsupportedInputs(t *testing.T) {
	for _, sql := range []string{
		`SELECT regexp_replace(v,'(x)',replacement) FROM t`,
		`SELECT regexp_replace(v,'(x)','$9') FROM t`,
		`SELECT regexp_replace(v,'(x)',replacement), regexp_replace(v,'(x)','$1') FROM t`,
		`SELECT max_by(v,k,-1) FROM t`,
	} {
		if result, err := TranspileOne(sql, DialectAthena, DialectDuckDB); err == nil {
			t.Errorf("unsafe rewrite accepted: %s", result)
		}
	}
}

func TestTrinoFamilyDuckDBRewrites(t *testing.T) {
	for _, dialect := range []Dialect{DialectAthena, DialectTrino, DialectPresto} {
		for _, tc := range []struct{ sql, contains string }{
			{`SELECT max_by(v,k) FILTER (WHERE keep) FROM t`, "ARG_MAX_NULL(v, k) FILTER(WHERE keep)"},
			{`SELECT min_by(v,k) FROM t`, "ARG_MIN_NULL(v, k)"},
			{`SELECT MIN_BY(a.id, a.timestamp, 3) FROM a`, "LIST_SLICE(LIST(a.id ORDER BY a.timestamp) FILTER(WHERE a.timestamp IS NOT NULL), 1, 3)"},
			{`SELECT regexp_extract(v, '[0-9]+') FROM t`, "REGEXP_EXTRACT_ALL"},
			{`SELECT regexp_replace(v, '([0-9]+)', '$1!') FROM t`, `'\1!', 'g'`},
			{`SELECT to_iso8601(v) FROM t`, "STRFTIME"},
		} {
			got, err := TranspileOne(tc.sql, dialect, DialectDuckDB)
			if err != nil || !strings.Contains(got, tc.contains) {
				t.Errorf("%s: %s: %s (%v), want %q", dialect, tc.sql, got, err, tc.contains)
			}
		}
	}
}

func TestTypedSnowflakeLambdaCanonicalCasts(t *testing.T) {
	got, err := TranspileOne(`transform(x, a int -> a + a + 1)`, DialectSnowflake, DialectSnowflake)
	want := `TRANSFORM(x, a -> CAST(a AS INT) + CAST(a AS INT) + 1)`
	if err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestTrinoISO8601EvaluatesInputOnce(t *testing.T) {
	got, err := TranspileOne(`SELECT to_iso8601(volatile_timestamp())`, DialectTrino, DialectDuckDB)
	if err != nil || strings.Count(got, "VOLATILE_TIMESTAMP()") != 1 {
		t.Fatalf("timestamp expression must be evaluated once: %s (%v)", got, err)
	}
}

// Trino's TestTimestamp.testToIso8601 fixes literal precision at the exact
// fractional width, including zero; only an unqualified TIMESTAMP cast has
// the default precision of three.
func TestTrinoTimestampLiteralPrecision(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
	}{
		{`TIMESTAMP '2020-05-01 12:34:56'`, 0},
		{`TIMESTAMP '2020-05-01 12:34:56.1'`, 1},
		{`TIMESTAMP '2020-05-01 12:34:56.12'`, 2},
		{`TIMESTAMP '2020-05-01 12:34:56.123456'`, 6},
		{`CAST('2020-05-01 12:34:56.123456' AS TIMESTAMP)`, 3},
	} {
		parsed, err := ParseStrict("SELECT "+tc.value, DialectTrino)
		if err != nil {
			t.Fatal(err)
		}
		expression := parsed.Statements[0].Node.(*SelectStmt).Projections[0].Expr
		if got := trinoTimestampPrecision(expression); got != tc.want {
			t.Errorf("%s: precision %d, want %d", tc.value, got, tc.want)
		}
	}
}
