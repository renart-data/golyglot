---
title: Transpilation semantics
description: Result-preserving Athena, Trino, and Presto translations to DuckDB.
---

`TranspileOne` parses the source dialect and regenerates SQL for the target:

```go
sql, err := golyglot.TranspileOne(
	"SELECT max_by(label, updated_at) FROM events",
	golyglot.DialectAthena,
	golyglot.DialectDuckDB,
)
// SELECT ARG_MAX_NULL(label, updated_at) FROM events
```

SQL that looks similar across engines can produce different results. The
following translations preserve specific Athena/Trino/Presto behaviors when
targeting DuckDB; they are not a claim of complete engine equivalence.

## NULL winners in MIN_BY and MAX_BY

The two-argument form uses `ARG_MIN_NULL` or `ARG_MAX_NULL`, so a winning row
whose value is NULL is not silently skipped. FILTER and OVER are retained.

The three-argument top-N form uses an ordered `LIST` followed by `LIST_SLICE`.
It excludes NULL ordering keys, retains NULL values, and combines the key
predicate with an existing FILTER. This can use more memory than DuckDB's
native top-N aggregate. A positive integer literal is required for N;
dynamic N, DISTINCT, and extra argument ordering are explicitly rejected.
Ordering between tied keys is not guaranteed.

## Regular expressions

`REGEXP_EXTRACT` uses `REGEXP_EXTRACT_ALL` and selects its first result.
Consequently, no match yields NULL, while a successful empty match stays an
empty string. Without an explicit group index it returns the whole match.

`REGEXP_REPLACE` enables DuckDB's global-replacement option. Literal Trino
replacement references such as `$1` and `${year}` are converted to DuckDB
capture references. Named captures require a literal pattern. The two-argument
form removes all matches. Dynamic/lambda replacements, invalid references,
and capture indices beyond DuckDB's replacement support return a Go error.

Trino uses Java-style regular expressions and DuckDB uses RE2. Patterns must
still fit their compatible subset; this is not a general regex-language
translator. The source behavior is documented in
[Trino's regular-expression reference](https://trino.io/docs/current/functions/regexp.html).

## ISO date and timestamp strings

`TO_ISO8601` supports DATE and TIMESTAMP without time zone. Dates use
`YYYY-MM-DD`; timestamps include `T` and the source fractional precision.
Timestamp literals and explicit precision casts are recognized up to six
fractional digits. A literal without a fraction has no decimal suffix;
`.1` and `.12` retain exactly those widths. Otherwise the translation assumes Trino's default
millisecond precision and raises a SQL error if formatting would truncate
additional fractional digits.

Time-zone-aware timestamps raise a SQL error: DuckDB does not retain the
source zone/offset identity needed to reproduce the same string. Precisions
above six digits are rejected. See
[Trino's date/time reference](https://trino.io/docs/current/functions/datetime.html).
The input is bound once with DuckDB's `LAMBDA` syntax, so a volatile
timestamp expression is not evaluated repeatedly during formatting.

## Execution tests

The normal suite asserts generated SQL. To also execute result regressions
against an installed DuckDB CLI:

```sh
GOLYGLOT_DUCKDB_ORACLE=1 go test ./pkg/golyglot
```

These tests use an in-memory database, safe mode, no startup script, and
bounded subprocesses. They do not need warehouse credentials or network
access. The checked-in full compatibility corpus retains its upstream
snapshot; two old NULL-dropping Presto expectations have explicit corrected
assertions in the test harness.
