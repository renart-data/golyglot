---
title: SAP HANA and Vertica
description: Native syntax, type normalization, verified examples, and cross-dialect limits.
---

Use `DialectHANA` or `DialectVertica` with the existing parsing, formatting,
analysis and transpilation APIs. `ParseDialect` accepts `hana`, `sap_hana`,
`sap-hana`, `saphana`, and `vertica` case-insensitively. `Dialects()` includes
both canonical names.

```go
sql, err := golyglot.TranspileOne(
    "SELECT CAST(2147483648 AS INT)",
    golyglot.DialectVertica,
    golyglot.DialectDuckDB,
)
// SELECT CAST(2147483648 AS BIGINT)
```

## Native SQL and analysis

The checked-in coverage includes SELECTs, CTEs, joins, windows, common DML,
table definitions, native function calls, casts and interval forms. HANA
function names and arguments retain their native spelling. Vertica adds
`MINUS`, `::` casts, factorial/absolute-value operators and `LISTAGG` with
`USING PARAMETERS`.

Type normalization is dialect-aware: Vertica integer aliases are 64-bit,
its floating-point aliases normalize to double precision, and HANA
`SECONDDATE`, `ALPHANUM` and `NCLOB` have logical timestamp/string types.
`ParseDataType("ARRAY[INT]", DialectVertica)` retains the nested element type.
HANA folds unquoted names to uppercase and retains quoted case; Vertica
identifier matching is case-insensitive, including quoted identifiers.

This is an initial dialect surface, not a complete implementation of HANA
SQLScript, hierarchy/calculation-view features or Vertica's physical-design,
COPY/EXPORT, pattern-matching and time-series extensions. Tolerant parsing
still preserves unsupported text and diagnostics. An opaque node is not
evidence that its contents can be analyzed or translated.

## Selected translations

Vertica translations cover common NULL-handling functions, conditional
expressions, integer-width preservation, `TIMESTAMPADD`, approximate distinct
counts and statement clocks for targets with matching primitives. For example,
`GETDATE()` maps to `CAST(STATEMENT_TIMESTAMP() AS TIMESTAMP)` in PostgreSQL;
it does not become a transaction-start clock in DuckDB.

HANA's `SYS.DUMMY` becomes a one-row derived table with the column `DUMMY`
and value `'X'` when targeting DuckDB. This preserves stars, joins, predicates
and aggregate cardinality. CTEs can shadow bare `DUMMY`; explicitly qualified
`SYS.DUMMY` remains the system relation. Other targets and unsupported table
modifiers are rejected pending identifier-binding verification.

When crossing the HANA boundary, implicit NULL ordering is made explicit for
queries and windows. HANA places NULLs first in ascending order and last in
descending order; see SAP's
[expression reference](https://help.sap.com/docs/PRODUCT_ID/4fe29514fd584807ac9f2a04f6754767/20a4389775191014b5a6bf2ccc0df2ed.html).

Vertica query-level NULL placement depends on the sort-key type; analytic
window ordering uses different defaults. Golyglot emits explicit ordering
when the type can be established from a cast, literal or output expression.
Unknown or ambiguous types return an error. When targeting Vertica, simple
column sort keys use a separate CASE null-rank key. Ordinals, computed aliases,
volatile expressions, DISTINCT queries and compound ordering require a
derived-table rewrite that is not implemented yet. See the vendor's
[NULL ordering reference](https://docs.vertica.com/25.4.x/en/data-analysis/query-optimization/analytic-functions/null-sort-order/).

## Conservative boundaries

Known native features without a verified mapping return an error instead of
falling through to an unrelated function with the same name. These include
HANA-specific casts, native date/format functions, procedures and opaque
extensions; Vertica safe casts, boundary-counting date differences, time-slice
functions and unsupported function parameters; and string aggregation across
these dialect boundaries. Vertica `LISTAGG` has byte-length limits and overflow
behavior that generic string concatenation does not reproduce. See its
[LISTAGG reference](https://docs.vertica.com/25.3.x/en/sql-reference/functions/aggregate-functions/listagg/).

HANA table definitions and Vertica-source table definitions cannot currently
be translated across dialects: their opaque constraint/default/storage tails
are preserved for native SQL, not treated as portable declarations.

Qualified or quoted user-defined functions are not treated as vendor builtins.
As with other dialects, callers remain responsible for providing those
functions on the target engine. Offline builtin-function catalogs currently
cover DuckDB, PostgreSQL and ClickHouse, not HANA or Vertica.

## Verification

The default tests include 231 unchanged upstream fixture expectations pinned
independently from the older SQLGlot/DataFusion corpus, plus focused tests for
type inference, lexical scope and unsupported conversions. The full fixture
gate includes both snapshots.

```sh
make test-polyglot-full
GOLYGLOT_DUCKDB_ORACLE=1 go test ./pkg/golyglot -run TestHANAVerticaDuckDBResults
```

The optional execution tests run the generated SQL in an isolated in-memory
DuckDB database. They do not connect to SAP HANA or Vertica: source expectations
come from the upstream cases and vendor documentation. Native-engine
differential verification is still outstanding.
