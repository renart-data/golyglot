# Changelog

## Unreleased

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
