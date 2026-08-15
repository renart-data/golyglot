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
go test ./benchmarks -run '^$' -bench . -benchmem -count=5
```

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

Polyglot's lineage suite is not wired into the default comparison: the Go
public lineage API intentionally reparses SQL, so it should be reported
separately rather than presented as an apples-to-apples AST-only comparison.
