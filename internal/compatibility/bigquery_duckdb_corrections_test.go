package compatibility

import (
	"testing"

	"github.com/renart-data/golyglot/pkg/golyglot"
)

// Keep the vendored snapshot immutable. These exact expectations predate the
// BigQuery NUMERIC precision and timezone corrections (Polyglot #479 and #476).
// The timestamp cases retain BigQuery's implicit UTC even outside DATE; the
// three DATE cases use the same session-independent lowering for both timestamp
// representations. TestBigQueryDuckDBRegressionResults executes these behaviors
// against DuckDB in multiple session timezones, independently of SQL spelling.
var bigQueryDuckDBFixtureCorrections = map[string]struct {
	pinned    string
	corrected string
}{
	// bigquery write duckdb:86
	"CAST(a AS NUMERIC)": {
		pinned:    "CAST(a AS DECIMAL)",
		corrected: "CAST(a AS DECIMAL(38, 9))",
	},
	// bigquery write duckdb:167
	"TIMESTAMP_TRUNC(TIMESTAMP '2024-03-15 14:35:47.123456', DAY, 'America/New_York')": {
		pinned:    "DATE_TRUNC('DAY', CAST('2024-03-15 14:35:47.123456' AS TIMESTAMPTZ) AT TIME ZONE 'America/New_York') AT TIME ZONE 'America/New_York'",
		corrected: "DATE_TRUNC('DAY', CAST('2024-03-15 14:35:47.123456 UTC' AS TIMESTAMPTZ) AT TIME ZONE 'America/New_York') AT TIME ZONE 'America/New_York'",
	},
	// bigquery write duckdb:168
	"TIMESTAMP_TRUNC(TIMESTAMP '2024-03-15 14:35:00', MINUTE, 'America/New_York')": {
		pinned:    "DATE_TRUNC('MINUTE', CAST('2024-03-15 14:35:00' AS TIMESTAMPTZ))",
		corrected: "DATE_TRUNC('MINUTE', CAST('2024-03-15 14:35:00 UTC' AS TIMESTAMPTZ))",
	},
	// bigquery write duckdb:169
	"TIMESTAMP_TRUNC(TIMESTAMP '2024-03-15 14:35:47.123456', DAY)": {
		pinned:    "DATE_TRUNC('DAY', CAST('2024-03-15 14:35:47.123456' AS TIMESTAMPTZ))",
		corrected: "DATE_TRUNC('DAY', CAST('2024-03-15 14:35:47.123456 UTC' AS TIMESTAMPTZ))",
	},
	// bigquery write duckdb:170
	"TIMESTAMP_TRUNC(TIMESTAMP '2025-01-01 14:35:47.123456', MINUTE)": {
		pinned:    "DATE_TRUNC('MINUTE', CAST('2025-01-01 14:35:47.123456' AS TIMESTAMPTZ))",
		corrected: "DATE_TRUNC('MINUTE', CAST('2025-01-01 14:35:47.123456 UTC' AS TIMESTAMPTZ))",
	},
	// bigquery write duckdb:171
	"WITH sample AS (SELECT * FROM UNNEST([TIMESTAMP '2024-03-15 14:35:46', TIMESTAMP '2024-03-16 01:12:03']) AS ts) SELECT ts, TIMESTAMP_TRUNC(ts, DAY, 'America/New_York') AS truncated_ts FROM sample": {
		pinned:    "WITH sample AS (SELECT * FROM UNNEST([CAST('2024-03-15 14:35:46' AS TIMESTAMPTZ), CAST('2024-03-16 01:12:03' AS TIMESTAMPTZ)]) AS _t0(ts)) SELECT ts, DATE_TRUNC('DAY', ts AT TIME ZONE 'America/New_York') AT TIME ZONE 'America/New_York' AS truncated_ts FROM sample",
		corrected: "WITH sample AS (SELECT * FROM UNNEST([CAST('2024-03-15 14:35:46 UTC' AS TIMESTAMPTZ), CAST('2024-03-16 01:12:03 UTC' AS TIMESTAMPTZ)]) AS _t0(ts)) SELECT ts, DATE_TRUNC('DAY', ts AT TIME ZONE 'America/New_York') AT TIME ZONE 'America/New_York' AS truncated_ts FROM sample",
	},
	// bigquery write duckdb:172
	"WITH sample AS (SELECT ts FROM UNNEST([TIMESTAMP '2024-03-15 14:35:46', TIMESTAMP '2024-03-16 01:12:03']) AS ts) SELECT ts, TIMESTAMP_TRUNC(ts, DAY) AS truncated_ts FROM sample": {
		pinned:    "WITH sample AS (SELECT ts FROM UNNEST([CAST('2024-03-15 14:35:46' AS TIMESTAMPTZ), CAST('2024-03-16 01:12:03' AS TIMESTAMPTZ)]) AS _t0(ts)) SELECT ts, DATE_TRUNC('DAY', ts) AS truncated_ts FROM sample",
		corrected: "WITH sample AS (SELECT ts FROM UNNEST([CAST('2024-03-15 14:35:46 UTC' AS TIMESTAMPTZ), CAST('2024-03-16 01:12:03 UTC' AS TIMESTAMPTZ)]) AS _t0(ts)) SELECT ts, DATE_TRUNC('DAY', ts) AS truncated_ts FROM sample",
	},
	// bigquery write duckdb:173
	"WITH sample AS (SELECT * FROM UNNEST([TIMESTAMP '2024-03-15 14:35:46', TIMESTAMP '2024-03-16 01:12:03']) AS ts) SELECT ts, TIMESTAMP_TRUNC(ts, MINUTE, 'America/New_York') AS truncated_ts FROM sample": {
		pinned:    "WITH sample AS (SELECT * FROM UNNEST([CAST('2024-03-15 14:35:46' AS TIMESTAMPTZ), CAST('2024-03-16 01:12:03' AS TIMESTAMPTZ)]) AS _t0(ts)) SELECT ts, DATE_TRUNC('MINUTE', ts) AS truncated_ts FROM sample",
		corrected: "WITH sample AS (SELECT * FROM UNNEST([CAST('2024-03-15 14:35:46 UTC' AS TIMESTAMPTZ), CAST('2024-03-16 01:12:03 UTC' AS TIMESTAMPTZ)]) AS _t0(ts)) SELECT ts, DATE_TRUNC('MINUTE', ts) AS truncated_ts FROM sample",
	},
	// bigquery write duckdb:174
	"WITH sample AS (SELECT * FROM UNNEST([TIMESTAMP '2024-03-15 14:35:46', TIMESTAMP '2024-03-16 01:12:03']) AS ts) SELECT ts, TIMESTAMP_TRUNC(ts, MINUTE) AS truncated_ts FROM sample": {
		pinned:    "WITH sample AS (SELECT * FROM UNNEST([CAST('2024-03-15 14:35:46' AS TIMESTAMPTZ), CAST('2024-03-16 01:12:03' AS TIMESTAMPTZ)]) AS _t0(ts)) SELECT ts, DATE_TRUNC('MINUTE', ts) AS truncated_ts FROM sample",
		corrected: "WITH sample AS (SELECT * FROM UNNEST([CAST('2024-03-15 14:35:46 UTC' AS TIMESTAMPTZ), CAST('2024-03-16 01:12:03 UTC' AS TIMESTAMPTZ)]) AS _t0(ts)) SELECT ts, DATE_TRUNC('MINUTE', ts) AS truncated_ts FROM sample",
	},
	// duckdb read bigquery:160
	"SELECT DATE(DATETIME '2016-12-25 23:59:59')": {
		pinned:    "SELECT CAST(CAST('2016-12-25 23:59:59' AS TIMESTAMP) AS DATE)",
		corrected: "SELECT CAST(MAKE_TIMESTAMPTZ(EPOCH_US(CAST('2016-12-25 23:59:59' AS TIMESTAMP))) AT TIME ZONE 'UTC' AS DATE)",
	},
	// duckdb read bigquery:161
	"SELECT DATE(TIMESTAMP '2016-12-25', 'America/Los_Angeles')": {
		pinned:    "SELECT CAST(CAST(CAST('2016-12-25' AS TIMESTAMPTZ) AS TIMESTAMP) AT TIME ZONE 'UTC' AT TIME ZONE 'America/Los_Angeles' AS DATE)",
		corrected: "SELECT CAST(MAKE_TIMESTAMPTZ(EPOCH_US(CAST('2016-12-25 UTC' AS TIMESTAMPTZ))) AT TIME ZONE 'America/Los_Angeles' AS DATE)",
	},
	// duckdb read bigquery:162
	"SELECT DATE('2024-01-15 23:30:00', 'Europe/Berlin')": {
		pinned:    "SELECT CAST(CAST('2024-01-15 23:30:00' AS TIMESTAMP) AT TIME ZONE 'UTC' AT TIME ZONE 'Europe/Berlin' AS DATE)",
		corrected: "SELECT CAST(MAKE_TIMESTAMPTZ(EPOCH_US(CAST('2024-01-15 23:30:00 UTC' AS TIMESTAMPTZ))) AT TIME ZONE 'Europe/Berlin' AS DATE)",
	},
}

func correctedBigQueryDuckDBFixture(t *testing.T, sql, want string) string {
	t.Helper()
	correction, ok := bigQueryDuckDBFixtureCorrections[sql]
	if !ok {
		return want
	}
	if want != correction.pinned {
		t.Fatalf("upstream expectation changed; review or remove the correction for %q: %q", sql, want)
	}
	t.Logf("checking semantic correction to pinned BigQuery -> DuckDB expectation: %s", sql)
	return correction.corrected
}

// Run the corrected exact-output assertions in the default suite too, even
// when the much larger upstream compatibility corpus is not enabled.
func TestBigQueryDuckDBPinnedCorrections(t *testing.T) {
	for sql, correction := range bigQueryDuckDBFixtureCorrections {
		t.Run(sql, func(t *testing.T) {
			got, err := golyglot.TranspileOne(sql, golyglot.DialectBigQuery, golyglot.DialectDuckDB)
			if err != nil || got != correction.corrected {
				t.Fatalf("got %q (%v), want %q", got, err, correction.corrected)
			}
		})
	}
}
