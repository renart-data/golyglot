# Differential type-inference oracle

Status: DuckDB vertical slice implemented on the experiment branch.

## Outcome

Continuously compare Golyglot's inferred output types with the types assigned
by real open-source database binders. The lab should discover concrete,
reproducible inference gaps without adding a database dependency to the core
Golyglot package or executing user data.

## Current vertical slice

```bash
make type-oracle-duckdb
go run ./cmd/golyglot-type-oracle --seed 42 --cases 24
go run ./cmd/golyglot-type-oracle --seed 42 --cases 24 --json
```

It provides:

- a deterministic, type-directed expression corpus over numeric, boolean,
  string, date, and timestamp columns;
- isolated Golyglot worker processes with a per-case stack cap and timeout;
- a DuckDB 1.5-compatible CLI adapter using `-safe -no-init` and an in-memory
  schema/view;
- normalized type comparison with match, kind mismatch, modifier mismatch,
  shape mismatch, unknown, timeout, and crash outcomes;
- human and JSON reports; mismatches are informational unless
  `--fail-on-mismatch` is selected;
- no CGO, Python package, network, extension, file, or production database
  dependency.

The prototype compares types only. DuckDB query description does not preserve
reliable nullability metadata, so nullability must not be scored as a match.

## Architecture

```text
seed + capability profile
  -> typed query generator
  -> isolated Golyglot inference worker
  -> database binder adapter
  -> normalized comparator
  -> mismatch corpus + metrics
  -> reducer (next phase)
```

### Typed generator

- Generate an AST/expression from a known input schema rather than arbitrary
  strings.
- Select only signatures supported by the target dialect capability profile.
- Keep a curated regression corpus beside generated cases; generation alone
  can share blind spots with inference.
- Record seed, generator version, dialect profile, engine version, schema, and
  SQL for exact reproduction.
- Grow in layers: scalar expressions and aggregates, then CASE/coercions,
  CTEs/set operations, joins, windows, nested types, and dialect functions.

### Golyglot worker

Each case runs outside the controller process. This is required because a Go
stack overflow is fatal and cannot be recovered. The parent applies a timeout,
caps captured diagnostics, records crash/timeout as a first-class finding, and
continues with the next case. A production-scale runner may reuse a worker for
a small bounded batch, then recycle it.

### Engine adapter

Conceptual contract:

```text
Name() / Version()
Describe(context, schema, query) -> ordered output columns
```

- DuckDB: bind a temporary view and inspect its ordered column types. The CLI
  runs in safe mode without init files.
- PostgreSQL: use the extended-query Parse/Bind/Describe flow and consume
  RowDescription type OIDs without Execute.
- Trino: use `DESCRIBE OUTPUT (query)`.
- Generic `database/sql.ColumnTypes` is only a fallback because driver metadata
  is optional and may normalize away important details.

### Comparator

Normalize both sides through `golyglot.ParseDataType`, while retaining the raw
engine spelling. Classify:

- exact/alias-normalized match;
- same logical kind but different modifier/precision/scale;
- different type kind;
- output count/name/order mismatch;
- Golyglot unknown (coverage gap, never a pass);
- controlled parse/bind rejection;
- Golyglot or engine crash/timeout.

Report denominators separately: generated, accepted by both, known inference,
normalized matches, mismatches, rejections, crashes, and timeouts. Break them
down by dialect, feature, operator/function, and input type family.

## Safety model

- Run only generated SELECT statements against generated temporary schemas.
- Use in-memory/temporary databases, underprivileged processes, no init files,
  DuckDB safe mode, and no extension auto-installation or external access.
- Never point an adapter at production credentials or a user warehouse.
- Enforce query depth/size, case count, worker memory/stack, and wall-clock
  limits.
- Treat engine error text and generated SQL as untrusted report data.
- Updating committed mismatch fixtures requires an explicit `--update` mode;
  normal runs are read-only.

## Reducer and regression workflow

For every stable mismatch:

1. Re-run it to exclude nondeterminism.
2. Minimize the AST by removing projections/clauses and replacing expressions
   while the same classification persists.
3. Save the smallest schema + SQL + expected engine metadata + pinned engine
   version as a proposed fixture.
4. Decide whether Golyglot is wrong, the adapter lost information, or the
   dialect intentionally differs.
5. Write a focused failing unit test, implement the inference fix, then move
   the minimized case into the permanent corpus.

Crash cases are prioritized above type mismatches. The alpha.6
`range(..., INTERVAL 1 HOUR)` incident is the model regression: a worker crash
must become one bounded result, not terminate the lab controller.

## CI rollout

### Pull requests

- Pure unit tests with fake/fixture metadata; no database binary required.
- Small checked-in curated corpus against Golyglot only.
- Optional local DuckDB integration via `make type-oracle-duckdb`.

### Nightly

- Pinned DuckDB version and fixed seed set, concurrency one on constrained
  hosts.
- Upload JSON reports and minimized repro candidates.
- Gate only existing accepted regressions; new engine-version differences are
  informational until triaged.

### Matrix expansion

- DuckDB first, then PostgreSQL and Trino containers one at a time.
- Keep engine/version baselines separate; never compare a new engine release
  to an old golden file as if Golyglot changed.
- Add weekly rotating seeds for discovery and fixed seeds for trend metrics.

## Near-term backlog

1. Add expression feature tags and per-feature metrics.
2. Implement AST-aware reduction and fixture emission.
3. Add curated cases for the first DuckDB mismatches.
4. Expand decimal arithmetic/aggregate rules and rerun fixed seeds.
5. Add PostgreSQL Describe adapter and OID-to-type normalization.
6. Add Trino `DESCRIBE OUTPUT` adapter.
7. Add nested/list/struct/map and temporal interval grammars.
8. Publish a compact nightly trend artifact, not a noisy issue per mismatch.

## Acceptance criteria

- Same seed/version yields byte-stable case order and JSON semantics.
- An alias spelling such as INT vs INTEGER is a match.
- Precision/scale changes are distinct from kind changes.
- Unknown inference cannot increase the match rate.
- One worker crash or timeout does not stop subsequent cases.
- No generated query can access files, extensions, network, or persistent DBs.
- Every mismatch includes seed, SQL, schema, raw types, normalized types, and
  engine version.
- A minimized accepted mismatch becomes a failing test before its fix.

## Primary references

- [DuckDB DESCRIBE](https://duckdb.org/docs/current/sql/statements/describe)
- [PostgreSQL extended query protocol](https://www.postgresql.org/docs/current/protocol-flow.html)
- [PostgreSQL RowDescription](https://www.postgresql.org/docs/17/protocol-message-formats.html)
- [Trino DESCRIBE OUTPUT](https://trino.io/docs/current/sql/describe-output.html)
- [SQLancer architecture and reducers](https://github.com/sqlancer/sqlancer)
- [DuckDB SQLSmith](https://duckdb.org/docs/current/core_extensions/sqlsmith)
