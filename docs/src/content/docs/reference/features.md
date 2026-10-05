---
title: Feature reference
description: The current alpha surface.
---

The alpha release currently exposes:

- `Parse`, `ParseTolerant`, and `ParseStrict`
- `Generate`, `Format`, and `Transpile`
- 36 registered dialect names plus aliases, including SAP HANA and Vertica
- Typed AST nodes with source spans
- Fluent expressions, SELECT queries, CTEs, set operations, and common DML builders
- Syntax, semantic, and schema-aware validation
- Per-call function/UDF catalogs with typed overloads and function kinds
- `AnalyzeQuery` facts plus schema-aware output type and nullability inference
- `ParseDataType` for normalized scalar and nested SQL data types
- `Lineage`, source-table discovery, and OpenLineage column/job/run payloads
- `Walk`, `FindAll`, `Transform`, and column-reference helpers

The public package is intentionally independent of cgo, WASM, FFI, and LSP protocol packages. The browser demo is a separate adapter around the same Go API.

Registration is not a claim of complete vendor SQL or arbitrary cross-engine
equivalence. See the [dialect reference](/reference/dialects/) for HANA/Vertica
coverage and unsupported conversions.

## Row limits

`SelectStmt.TopPercent`, `TopWithTies` and `LimitPercent` retain modifiers
separately from their count expressions. `FetchClause` retains its count,
FIRST/NEXT spelling, percentage and ties flags. `Walk` and `Transform` visit the
count expressions, and semantic diffs distinguish a row count from a percentage
or a ties-preserving limit.

Canonical generation applies the target dialect's syntax even when called
directly or through `BuildSQL`. Oracle uses `OFFSET ... ROWS FETCH FIRST ... ROWS
ONLY`; unbounded `LIMIT ALL` and `LIMIT NULL` omit FETCH. Pretty-printing uses the
same rules. A branch-local TOP stays inside its set-operation operand, while a
compound's trailing limit applies to the whole set.

Unsupported modifiers return an error. For example, PostgreSQL cannot accept a
percentage limit, DuckDB cannot accept WITH TIES, and TSQL cannot combine OFFSET
with a percentage/ties TOP. Percentage modifiers are retained, not emulated:
rounding and boundary behavior remain those of the target engine.

## DuckDB-specific translations

`ORDER BY ALL` expands to ordinal positions when the projection width is known.
This is supported for PostgreSQL, Oracle, Presto, Trino, Snowflake, Redshift,
SQLite and generic SQL. Direction and explicit NULL placement are preserved;
implicit ordering assumes DuckDB's documented default, NULLS LAST in either
direction. A quoted `"all"` remains an identifier. Stars, COLUMNS, UNNEST and
other schema-dependent projections require explicit expansion first.

DuckDB's `//` is not universally integer division: floating-point operands use
floating-point division. PostgreSQL lowering currently accepts signed integer
literals, nested divisions and explicit SMALLINT/INTEGER/BIGINT casts (including
INT2/INT4/INT8 aliases). It uses integer `/` with `NULLIF(divisor, 0)`, preserving
truncation towards zero, integer result types and NULL on division by zero.
Unknown, decimal, floating-point, unsigned and wider integer operands, and other
target dialects, return an error rather than guessing. See the
[DuckDB numeric operators](https://duckdb.org/docs/current/sql/functions/numeric#division-and-modulo-operators)
and [PostgreSQL numeric operators](https://www.postgresql.org/docs/current/functions-math.html).

Native DuckDB generation keeps both features. Optional result checks use only
synthetic data in an in-memory DuckDB database and a disposable PostgreSQL
container supplied by the caller:

```sh
GOLYGLOT_DUCKDB_ORACLE=1 go test ./pkg/golyglot -run TestRowLimitDuckDBResults
GOLYGLOT_POSTGRES_CONTAINER=my-isolated-test-db go test ./pkg/golyglot -run TestDuckDBPostgresSemanticResults
```

These checks are not native Oracle, TSQL or Hive engine verification.
