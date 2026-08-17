# Benchmarks

These benchmarks compare the native Go core with the original Polyglot Rust
core using matching query-size cases and a PostgreSQL-to-MySQL transpilation
path. Results are machine-specific; keep the Go and Rust runs on the same
machine and record the commit, Go/Rust versions, CPU, and OS with any report.

The original comparison target is Polyglot commit `d5aa0d4` (the repository
revision used while this benchmark was added). It is the Rust library itself,
not the original cgo/FFI client.

## Go

```sh
make bench-golyglot
```

`BENCH_COUNT` and `BENCH_TIME` control the sample count and duration. The
default target includes the synthetic and 12 manually stratified cases, but
keeps the larger corpus sample separate so an ordinary run remains reasonably
short.

## Released Polyglot shared library

Download and checksum the pinned Linux x86-64 release artifact, then run the
same parse, transpile, and format inputs through its public C ABI:

```sh
make fetch-polyglot-ffi
make bench-polyglot-ffi
```

The harness reports five approximately one-second samples per case. It
includes the FFI call, Polyglot's JSON serialization and result allocation,
the status check, and `polyglot_free_result`. It does not parse the returned
JSON in the C caller. In particular, parse timings are end-to-end FFI timings,
not a direct in-memory parser-core comparison with `golyglot.ParseStrict`.

## Compatibility counts and the extended FFI comparison

Polyglot v0.9.1 reports 100% for its tagged 11,333-case SQLGlot Rust suite.
That claim is consistent with the suite defined in that tag; it is not the
same population or execution path as this repository's 14,091-case full run.
The count reconciles as the tagged 11,333 SQLGlot categories, 623 custom cases,
and 2,135 additional SQLGlot mappings: 1,802 reverse `read` mappings, 332 extra
target `write` mappings, and one generic case skipped by the tagged suite.

The optional released-library comparison can be run with:

```sh
make test-polyglot-ffi-extended
```

It deliberately sends that larger population through the public FFI. In
particular, dialect identity uses same-dialect transpilation, whereas
Polyglot's tagged identity runner directly performs parse, dialect transform,
and generation, with fixture-sensitive identifier quoting and ClickHouse
keyword casing. The tagged transpilation runner also has deliberate expected
output overrides and known-case exclusions. Therefore the FFI comparison's
exact-output percentage must not be presented as Polyglot's compatibility
suite pass rate. `test-polyglot-oracle` remains only as a compatibility alias
for the renamed target.

## Fixture-derived benchmark set

The synthetic simple/medium/complex cases are supplemented by 12
fixture-derived transpilation cases listed in `fixture_cases.json`. They cover
11 source dialects, six targets, and a spread of DML, DDL, nested types,
dates and intervals, arrays, filtered aggregates, lateral joins, JSON paths,
windows, and `QUALIFY` lowering.

The manifest stores fixture paths and indices instead of copied SQL. The Go
benchmark resolves those references and checks its output against the fixture
before starting the timer. The Polyglot FFI runner resolves the same references
and refuses to benchmark unless released v0.9.1 produces the same expected
output. This keeps query selection synchronized across both harnesses.

These cases are intentionally a common-correct performance set, not a random
sample or a compatibility score. Filtering for exact agreement and manually
stratifying by feature makes the results useful for comparing implementation
cost on shared behavior, but it does not estimate either project's speed over
the full corpus or a production workload distribution.

## Deterministic corpus sample

`corpus_cases.json` is a separate 64-case mapping-weighted sample. Its
selection population is every cross-dialect `write` mapping in the checked-in
dialect fixtures for which Golyglot and released Polyglot v0.9.1 both produce
the exact expected output. Each eligible mapping gets one SHA-256 rank under a
fixed seed, and the lowest 64 ranks are selected. The checked-in sample was
drawn from 4,495 exact matches among 4,642 candidate mappings and covers 13
source dialects and 17 targets.

Run both implementations with shorter defaults appropriate for the broader
set:

```sh
make bench-golyglot-corpus
make bench-polyglot-ffi-corpus
```

Tune the Go run with `CORPUS_BENCH_COUNT` and `CORPUS_BENCH_TIME`; tune the FFI
run with `CORPUS_BENCH_COUNT` and `CORPUS_FFI_BENCH_DURATION_MS`. Regenerate
the manifest after intentionally changing the fixture or Polyglot version:

```sh
make select-benchmark-corpus
```

The generated manifest records its seed and population sizes. Review those
metadata and the selected-case diff when regenerating it. This is closer to a
corpus-weighted comparison than the hand-stratified set, but it is still not a
substitute for a private workload made from anonymized production queries.

For that production-shaped measurement, copy `workload.example.json` outside
the repository, replace its SQL with anonymized queries, and run:

```sh
make bench-workload BENCH_WORKLOAD=/secure/path/workload.json
```

Workload entries support `parse`, `format`, and `transpile`. `source` defaults
to `generic`; transpilation also requires `target`. An optional `expected`
value is checked before timing so a benchmark cannot silently measure the
wrong output. The manifest path is passed through the environment and its SQL
does not need to be committed.

## Comparing revisions

`tools/compare_benchmarks.py` compares the median `ns/op` values in two Go
benchmark outputs and emits a Markdown table:

```sh
python3 tools/compare_benchmarks.py baseline.txt current.txt
python3 tools/compare_benchmarks.py baseline.txt current.txt \
  --fail-regression-percent 10
```

Use at least three samples from the same machine. A threshold is useful on a
stable dedicated runner, but should remain reporting-only on shared runners
unless repeated measurements establish their normal variance.

## Original Polyglot

The local checkout must be at the pinned revision:

```sh
git clone https://github.com/tobilg/polyglot .cache/polyglot
git -C .cache/polyglot checkout d5aa0d4
```

Run the parser Criterion suite:

```sh
make bench-polyglot
```

The default target uses Polyglot's `dev` profile with debug info disabled and
features disabled. This keeps the baseline runnable on a development machine
with limited memory. Set `POLYGLOT_BENCH_PROFILE=bench` for an optimized
parser build on a larger machine. The optional transpiler suite is:

```sh
make bench-polyglot-transpile
```

It enables Polyglot's full benchmark profile and may need more memory than the
parser-only target.

The manually dispatched `.github/workflows/benchmarks.yml` workflow moves this
memory-heavy work to GitHub Actions. It alternates five Go samples against a
chosen baseline ref, uploads the raw results and comparison table, and can run
both pinned Polyglot Criterion suites with two Cargo build jobs. The runner
input defaults to `ubuntu-latest`; repositories with a configured larger
runner can supply its label when dispatching the workflow. Criterion output,
resource-usage logs, and generated reports are retained as workflow artifacts.
The regression threshold defaults to zero, meaning report without failing.

Polyglot's lineage suite is not wired into the default comparison: the Go
public lineage API intentionally reparses SQL, so it should be reported
separately rather than presented as an apples-to-apples AST-only comparison.
