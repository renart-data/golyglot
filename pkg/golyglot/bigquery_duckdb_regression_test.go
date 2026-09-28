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

func TestBigQueryDuckDBNumericPrecision(t *testing.T) {
	for _, tc := range []struct{ source, want string }{
		{"NUMERIC(10, 2)", "DECIMAL(10, 2)"},
		{"DECIMAL(10, 2)", "DECIMAL(10, 2)"},
		{"NUMERIC(10)", "DECIMAL(10)"},
		{"NUMERIC", "DECIMAL(38, 9)"},
		{"DECIMAL", "DECIMAL(38, 9)"},
	} {
		for _, cast := range []string{"CAST", "SAFE_CAST"} {
			t.Run(cast+"_"+tc.source, func(t *testing.T) {
				got, err := TranspileOne("SELECT "+cast+"(1.005 AS "+tc.source+") AS answer", DialectBigQuery, DialectDuckDB)
				if err != nil || !strings.Contains(got, " AS "+tc.want+")") {
					t.Fatalf("got %q (%v), want type %s", got, err, tc.want)
				}
			})
		}
	}
}

func TestBigQueryDuckDBIntervalExpressions(t *testing.T) {
	for _, tc := range []struct{ sql, contains string }{
		{`SELECT DATE_ADD(d, INTERVAL n MONTH) FROM t`, `INTERVAL (n) MONTH`},
		{`SELECT DATE_SUB(d, INTERVAL n MONTH) FROM t`, `INTERVAL (n) MONTH`},
		{`SELECT TIMESTAMP_ADD(ts, INTERVAL n HOUR) FROM t`, `INTERVAL (n) HOUR`},
		{`SELECT DATETIME_SUB(dt, INTERVAL n HOUR) FROM t`, `INTERVAL (n) HOUR`},
		{`SELECT TIME_ADD(tm, INTERVAL n MINUTE) FROM t`, `INTERVAL (n) MINUTE`},
		{`SELECT INTERVAL n DAY FROM t`, `INTERVAL (n) DAY`},
		{`SELECT INTERVAL (n + 1) MONTH FROM t`, `INTERVAL (n + 1) MONTH`},
		{`SELECT DATE_ADD(d, INTERVAL @months MONTH) FROM t`, `INTERVAL ($months) MONTH`},
		{`SELECT GENERATE_DATE_ARRAY(d, e, INTERVAL n MONTH) FROM t`, `INTERVAL (n) MONTH`},
		{`SELECT DATE_ADD(d, INTERVAL DATE_DIFF(e, d, MONTH) MONTH) FROM t`, `INTERVAL (DATE_DIFF(`},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			got, err := TranspileOne(tc.sql, DialectBigQuery, DialectDuckDB)
			if err != nil || !strings.Contains(got, tc.contains) {
				t.Fatalf("got %q (%v), want %q", got, err, tc.contains)
			}
		})
	}
}

func TestBigQueryDuckDBDatePreservesTimeZone(t *testing.T) {
	got, err := TranspileOne(`SELECT DATE(TIMESTAMP '2026-01-31 23:30:00+00', 'Europe/Berlin')`, DialectBigQuery, DialectDuckDB)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "AS TIMESTAMPTZ) AS TIMESTAMP)") {
		t.Fatalf("DATE discarded the instant before timezone conversion: %s", got)
	}
	if !strings.Contains(got, "AT TIME ZONE 'Europe/Berlin'") {
		t.Fatalf("DATE lost its explicit timezone: %s", got)
	}
}

func TestBigQueryDuckDBDateEvaluatesArgumentsOnce(t *testing.T) {
	got, err := TranspileOne(`SELECT DATE(volatile_timestamp(), volatile_zone())`, DialectBigQuery, DialectDuckDB)
	if err != nil || strings.Count(got, "VOLATILE_TIMESTAMP()") != 1 || strings.Count(got, "VOLATILE_ZONE()") != 1 {
		t.Fatalf("DATE must evaluate each argument once: %s (%v)", got, err)
	}
}

func TestBigQuerySafeCastFormatRemainsSafe(t *testing.T) {
	const sql = `SELECT SAFE_CAST(value AS DATE FORMAT 'YYYY-MM-DD') AS answer`
	got, err := TranspileOne(sql, DialectBigQuery, DialectBigQuery)
	if err != nil || got != sql {
		t.Fatalf("safe conversion must not become an error-raising parse: %s (%v)", got, err)
	}
}

func TestBigQueryDuckDBTimestampPreservesQuotedContent(t *testing.T) {
	got, err := TranspileOne(`SELECT TIMESTAMP "'2026-01-01'"`, DialectBigQuery, DialectDuckDB)
	if err != nil || !strings.Contains(got, `'''2026-01-01'''`) || strings.Contains(got, "UTC") {
		t.Fatalf("timestamp normalization must not strip quotes from the value: %s (%v)", got, err)
	}
}

// Opt-in execution regressions for https://github.com/tobilg/polyglot/issues/479,
// /476 and /477. Expectations follow BigQuery's NUMERIC, DATE and INTERVAL
// semantics; the generated SQL is executed in an isolated in-memory DuckDB.
func TestBigQueryDuckDBRegressionResults(t *testing.T) {
	if os.Getenv("GOLYGLOT_DUCKDB_ORACLE") != "1" {
		t.Skip("set GOLYGLOT_DUCKDB_ORACLE=1 for execution-based regressions")
	}
	duckdb, err := exec.LookPath("duckdb")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ name, sql, want string }{
		{"numeric parameters", `SELECT CAST(CAST(1.005 AS NUMERIC(10, 2)) AS STRING) AS answer`, `[{"answer":"1.01"}]`},
		{"decimal alias parameters", `SELECT CAST(CAST(-1.005 AS DECIMAL(10, 2)) AS STRING) AS answer`, `[{"answer":"-1.01"}]`},
		{"numeric default scale", `SELECT CAST(ROUND(CAST(0.1249 AS NUMERIC), 2) AS STRING) AS answer`, `[{"answer":"0.12"}]`},
		{"decimal default scale", `SELECT CAST(ROUND(CAST(0.1249 AS DECIMAL), 2) AS STRING) AS answer`, `[{"answer":"0.12"}]`},
		{"numeric nanounit", `SELECT CAST(CAST(0.000000001 AS NUMERIC) AS STRING) AS answer`, `[{"answer":"0.000000001"}]`},
		{"numeric precision only", `SELECT CAST(CAST(1.6 AS NUMERIC(10)) AS STRING) AS answer`, `[{"answer":"2"}]`},
		{"safe numeric overflow", `SELECT SAFE_CAST(1000 AS NUMERIC(3, 0)) AS answer`, `[{"answer":null}]`},
		{"safe numeric invalid", `SELECT SAFE_CAST('invalid' AS NUMERIC) AS answer`, `[{"answer":null}]`},
		{"date with zone", `SELECT DATE(TIMESTAMP '2026-01-31 23:30:00+00', 'Europe/Berlin') AS answer`, `[{"answer":"2026-02-01"}]`},
		{"date timestamp constructor", `SELECT DATE(TIMESTAMP('2026-01-31 23:30:00+00'), 'Europe/Berlin') AS answer`, `[{"answer":"2026-02-01"}]`},
		{"date default UTC", `SELECT DATE(TIMESTAMP '2026-02-01 00:30:00+00') AS answer`, `[{"answer":"2026-02-01"}]`},
		{"date literal implicit UTC", `SELECT DATE(TIMESTAMP '2026-01-31 23:30:00', 'Europe/Berlin') AS answer`, `[{"answer":"2026-02-01"}]`},
		{"date constructor implicit UTC", `SELECT DATE(TIMESTAMP('2026-01-31 23:30:00'), 'Europe/Berlin') AS answer`, `[{"answer":"2026-02-01"}]`},
		{"date negative offset", `SELECT DATE(TIMESTAMP '2026-01-31 18:30:00-05', 'Europe/Berlin') AS answer`, `[{"answer":"2026-02-01"}]`},
		{"date string literal", `SELECT DATE('2026-01-31 23:30:00', 'Europe/Berlin') AS answer`, `[{"answer":"2026-02-01"}]`},
		{"date string offset", `SELECT DATE('2026-02-01 00:30:00+05', 'UTC') AS answer`, `[{"answer":"2026-01-31"}]`},
		{"date bare null", `SELECT DATE(NULL) AS answer`, `[{"answer":null}]`},
		{"date from column", `WITH t AS (SELECT TIMESTAMP '2026-01-31 23:30:00+00' AS ts) SELECT DATE(ts, 'Europe/Berlin') AS answer FROM t`, `[{"answer":"2026-02-01"}]`},
		{"date implicit UTC column", `WITH t AS (SELECT TIMESTAMP '2026-01-31 23:30:00' AS ts) SELECT DATE(ts, 'Europe/Berlin') AS answer FROM t`, `[{"answer":"2026-02-01"}]`},
		{"date daylight saving", `SELECT DATE(TIMESTAMP '2026-07-01 22:30:00+00', 'Europe/Berlin') AS answer`, `[{"answer":"2026-07-02"}]`},
		{"date datetime", `SELECT DATE(DATETIME '2026-01-31 23:30:00') AS answer`, `[{"answer":"2026-01-31"}]`},
		{"date datetime column", `WITH t AS (SELECT DATETIME '2026-01-31 23:30:00' AS dt) SELECT DATE(dt) AS answer FROM t`, `[{"answer":"2026-01-31"}]`},
		{"date epoch", `SELECT DATE(TIMESTAMP_SECONDS(0), 'America/Los_Angeles') AS answer`, `[{"answer":"1969-12-31"}]`},
		{"date null", `SELECT DATE(CAST(NULL AS TIMESTAMP), 'Europe/Berlin') AS answer`, `[{"answer":null}]`},
		{"date null zone", `SELECT DATE(TIMESTAMP '2026-01-31 23:30:00+00', CAST(NULL AS STRING)) AS answer`, `[{"answer":null}]`},
		{"date calendar", `SELECT DATE(2026, 1, 31) AS answer`, `[{"answer":"2026-01-31"}]`},
		{"interval column", `SELECT CAST(DATE_ADD(DATE '2026-01-31', INTERVAL n MONTH) AS DATE) AS answer FROM (SELECT 1 AS n) t`, `[{"answer":"2026-02-28"}]`},
		{"interval subtract", `SELECT CAST(DATE_SUB(DATE '2026-03-31', INTERVAL n MONTH) AS DATE) AS answer FROM (SELECT 1 AS n) t`, `[{"answer":"2026-02-28"}]`},
		{"interval expression", `SELECT CAST(DATE_ADD(DATE '2026-01-01', INTERVAL (n + 1) MONTH) AS DATE) AS answer FROM (SELECT 1 AS n) t`, `[{"answer":"2026-03-01"}]`},
		{"interval negative", `SELECT CAST(DATE_ADD(DATE '2026-01-01', INTERVAL -n MONTH) AS DATE) AS answer FROM (SELECT 1 AS n) t`, `[{"answer":"2025-12-01"}]`},
		{"interval nested function", `SELECT CAST(DATE_ADD(DATE '2026-01-01', INTERVAL DATE_DIFF(DATE '2026-03-01', DATE '2026-01-01', MONTH) MONTH) AS DATE) AS answer`, `[{"answer":"2026-03-01"}]`},
		{"interval literal", `SELECT CAST(DATE_ADD(DATE '2026-01-01', INTERVAL 2 MONTH) AS DATE) AS answer`, `[{"answer":"2026-03-01"}]`},
		{"interval negative literal", `SELECT CAST(DATE_ADD(DATE '2026-01-01', INTERVAL -1 MONTH) AS DATE) AS answer`, `[{"answer":"2025-12-01"}]`},
		{"interval null", `SELECT CAST(DATE_ADD(DATE '2026-01-01', INTERVAL n MONTH) AS DATE) AS answer FROM (SELECT CAST(NULL AS INT64) AS n) t`, `[{"answer":null}]`},
		{"interval time", `SELECT TIME_ADD(TIME '23:30:00', INTERVAL n MINUTE) AS answer FROM (SELECT 60 AS n) t`, `[{"answer":"00:30:00"}]`},
		{"interval date array", `SELECT ARRAY_LENGTH(GENERATE_DATE_ARRAY(DATE '2026-01-01', DATE '2026-03-01', INTERVAL n MONTH)) AS answer FROM (SELECT 1 AS n) t`, `[{"answer":3}]`},
	}
	for _, zone := range []string{"UTC", "America/New_York", "Asia/Tokyo"} {
		for _, tc := range cases {
			t.Run(zone+"/"+tc.name, func(t *testing.T) {
				sql, err := TranspileOne(tc.sql, DialectBigQuery, DialectDuckDB)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				// Safe mode locks TimeZone, so disable startup files and extension
				// installation explicitly instead. No persistent database is opened.
				setup := "SET autoinstall_known_extensions=false; SET TimeZone='" + zone + "'; "
				out, err := exec.CommandContext(ctx, duckdb, ":memory:", "-no-init", "-batch", "-bail", "-json", "-c", setup+sql).CombinedOutput()
				if err != nil {
					t.Fatalf("%s: %v\n%s", sql, err, out)
				}
				var got, want any
				if err := json.Unmarshal(out, &got); err != nil {
					t.Fatalf("decode result: %v\n%s", err, out)
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
}
