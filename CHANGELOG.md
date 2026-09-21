# Changelog

## Unreleased

## 0.1.0-alpha.10 - 2026-09-22

- Add an offline, copy-safe builtin function metadata API with versioned DuckDB,
  PostgreSQL and ClickHouse inventories for editor completion and signature help.
  Catalog facts remain separate from argument-dependent type inference.
- Add a reproducible catalog generator and opt-in, isolated engine observations.
- Include separately verified SQL expression forms such as COALESCE and NULLIF,
  without claiming fixed return types for argument-dependent expressions.
- Accept SAMPLE as an unquoted CTE/relation name in DuckDB, PostgreSQL and
  ClickHouse, preserving expression completion inside incomplete nested calls.

## 0.1.0-alpha.9 - 2026-09-21

- Resolve higher-order lambda parameters and supported same-SELECT aliases
  lexically in schema validation, type inference, and column dependencies;
  parse Snowflake's typed lambda form without losing its body.
- Align set-operation projection facts with final output types. Resolve
  `UNION BY NAME` by dialect-aware names, including NULL padding, reordered
  columns, CTE scopes, and public output/lineage APIs.
- Add semantic grouping, aggregate-placement, and window-placement checks
  with source spans; syntax-only validation remains unchanged.
- Add per-call function/UDF catalogs with overloads, variadic signatures,
  function kinds, case policies, argument checks, and return-type inference.
- Preserve Athena/Trino/Presto semantics when translating `MAX_BY`, `MIN_BY`,
  `REGEXP_EXTRACT`, `REGEXP_REPLACE`, and supported `TO_ISO8601` inputs to
  DuckDB. Reject unsupported replacements or precision/timezone cases rather
  than silently generating a different result.
- Add opt-in DuckDB execution regressions (`GOLYGLOT_DUCKDB_ORACLE=1`). Two
  pinned Presto-to-DuckDB fixture expectations now have explicit, tested
  NULL-preserving corrections; the upstream fixture snapshot is unchanged.

## 0.1.0-alpha.8 - 2026-09-15

- Add query semantic diffs for canonical SQL, query behavior, output contracts,
  and schema-dependent input changes.
- Add an experimental, deterministic DuckDB type-inference oracle with isolated
  workers, pinned expectations, and reproducible minimized findings.
- Resolve schema-validation references in their own query scopes, preserving
  sibling CTEs, correlation, alias shadowing, and JOIN visibility.
- Expose non-projection `ColumnUses` with use-site spans, immediate references,
  physical upstream columns, and explicit resolution completeness.
- Include filter, join, grouping, window, subquery, and set-filter dependencies
  in semantic schema diffs, including type, presence, and nullability changes.
- Add time-bounded parser-prefix regressions and fuzz seeds for the malformed
  data-type and nested IF cases reported upstream in Polyglot.

## 0.1.0-alpha.7 - 2026-09-04

- Prevent interval units from being reported as column references and guard
  table-function lineage resolution against cycles, fixing a stack overflow
  for DuckDB queries such as `range(..., INTERVAL 1 HOUR)`.

## 0.1.0-alpha.6 - 2026-09-03

- Preserve semantic grouping when rendering programmatically constructed
  boolean and arithmetic expressions without changing lambda rendering.
- Add DuckDB-aware `STRUCT` field resolution, including fields exposed through
  `UNNEST`, across semantic validation, output analysis, and column lineage.
- Expand aggregate classification and inference for DuckDB `MIN`/`MAX`
  top-N arrays, and preserve safe modifiers when rewriting quantile,
  `ANY_VALUE`, and `LIST` calls.

## 0.1.0-alpha.5 - 2026-08-26

- Accept standalone `VALUES`, `VALUES` set operations, and `VALUES`-backed
  CTEs as complete query bodies during semantic validation, avoiding false
  `SEMANTIC_EMPTY_PROJECTION` diagnostics in editor integrations.

## 0.1.0-alpha.4 - 2026-08-21

- Schema-aware output inference across CTEs, subqueries, stars, set
  operations, joins, table functions, casts, expressions, and common SQL
  functions, including normalized types and nullability.
- Structured standalone SQL data-type parsing with aliases, modifiers, nested
  arrays/lists/maps/structs, and stable canonical rendering.
- Arithmetic type diagnostics and DuckDB-aware numeric, range, and temporal
  inference for editor and type-checking integrations.

## 0.1.0-alpha.3 - 2026-08-21

- Structured recovery sidecar metadata for missing, unexpected, and skipped
  syntax, including expected syntax, source spans, found tokens, and owning
  diagnostic codes.
- Typed tolerant-parser recovery for incomplete queries, DML, DDL, commands,
  expressions, names, and delimiters without noisy insertion cascades.
- Cursor-aware `SyntacticContextAt` results with parser-derived context,
  expected syntax, partial-token prefixes, and exact replacement spans for
  editor and language-server integrations.
- More precise handling of partial query, statement, and clause keywords,
  backed by parser regression tests, fuzz coverage, and updated parsing docs.

## 0.1.0-alpha.2 - 2026-08-19

- Byte-for-byte lossless parse results with zero-copy source slices, implicit
  trivia gaps, validated non-overlapping source edits, and explicit synthetic
  node spans.
- Polyglot v0.9.2-compatible strict diagnostic kind, message, display text,
  location, ASCII byte-span fixtures, and strict-validation `E005` trailing
  comma checks while preserving richer tolerant diagnostics.
- Complete compatibility coverage for the 14,091-case checked-in corpus.
- Versioned Polyglot FFI validation and matched benchmark suites.
- Deterministic corpus and private-workload benchmark manifests.
- A same-runner, fully optimized Polyglot core comparison with binary-size
  evidence and guarded Golyglot revision benchmarks.
- The canonical `github.com/renart-data/golyglot` module path, public
  `github.com/renart-data/golyglot/pkg/golyglot` package, isolated
  compatibility-test package, and fully pinned CI toolchains.

## 0.1.0-alpha.1 - 2026-08-15

- Initial pure-Go parser, formatter, and dialect transpiler.
- Tolerant parsing with source spans, recovery, and structured diagnostics.
- Compatibility and identity fixtures from Polyglot's SQLGlot test corpus.
- Fluent builders, semantic/schema validation, query analysis, AST visitors,
  column lineage, and OpenLineage-compatible payloads.
- Reproducible Polyglot benchmarks and an Astro/Starlight site with a Monaco
  editor demo backed by the Go/WASM adapter.
