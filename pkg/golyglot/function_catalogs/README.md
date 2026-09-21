# Builtin catalog snapshots

Generated structural metadata, not an inferred schema for arbitrary calls.
The first snapshots were captured with `cmd/function-catalog` on 21 September
2026 from DuckDB 1.5.1, PostgreSQL 17.9, and ClickHouse 25.8.33.6.

`BuiltinFunctionCatalogForDialect` returns a deep copy, in stable name/kind
order. Catalogs are decoded lazily once. Unsupported dialects return `false`;
PostgreSQL-compatible engines do not inherit the PostgreSQL inventory.

The generator queries `duckdb_functions()`, `pg_catalog.pg_proc`, and
ClickHouse's `system.functions` / `system.table_functions`. It never executes
discovered calls. Engine documentation prose is not mirrored in the snapshots.
`source` links identify the inventory contract. Reproduce with:

```sh
go run ./cmd/function-catalog -engine duckdb
go run ./cmd/function-catalog -engine postgresql -postgres-container YOUR_DISPOSABLE_CONTAINER
go run ./cmd/function-catalog -engine clickhouse \
  -clickhouse-image clickhouse/clickhouse-server@sha256:0152dd511befe6a2c2ef53e930726179669b08116da78500b37c51c96ff5ee77
```

The PostgreSQL fixture must have an empty, disposable database and local
`postgres` access. Do not point this command at user databases. No network or
host volume is attached to the memory-limited ClickHouse container. The DuckDB
CLI runs with safe mode and without user initialization files.

## Evidence boundaries

- Names/kinds/signatures describe that engine version and its default build.
  Catalog availability is not a guarantee that a function will work with an
  arbitrary argument, extension, connection, privilege, or engine version.
- DuckDB scalar/aggregate types and PostgreSQL types are catalog declarations.
  Polymorphic placeholders stay intact. PostgreSQL variadic tails are separate
  from fixed parameters; DuckDB table options are marked named and optional.
- DuckDB does not expose argument-dependent table schemas here. ClickHouse
  exposes syntax text, not a structured return-type contract. Unknown result
  types remain empty; no prose is parsed into a type.
- Window-only registration quirks are normalized per engine. In particular,
  ClickHouse's `first_value` / `last_value` are also ordinary aggregates.
- The runtime catalog adds separately verified grammar-level forms that do not
  appear in these snapshots: DuckDB COALESCE/NULLIF and PostgreSQL
  COALESCE/NULLIF/GREATEST/LEAST. Each has its own source link. Their result type
  stays unknown and ANY represents an expression, not a concrete overload.
  User-installed extensions, UDFs, and other dialects are not invented.
- These facts power editors, not `AnalyzeQuery` inference. The existing
  inference rules and caller-supplied `FunctionCatalogSpec` are unchanged.

`TestBuiltinFunctionEngineObservations` probes 39 fixed scalar, aggregate,
window, table-function, special-form and CTE observations using constants only.
`TestBuiltinDuckDBNamedOptions` also checks the schema and value of a temporary
CSV with explicit named options. These are an
opt-in smoke corpus, not verification of every overload. Run with
`GOLYGLOT_FUNCTION_ORACLE=1` and `GOLYGLOT_POSTGRES_CONTAINER` naming an isolated
fixture. Unit tests validate copy isolation, ordering, dialect boundaries,
variadics, named options, and honest unknown types without a database.
