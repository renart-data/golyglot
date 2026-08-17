# Changelog

## Unreleased

- Complete compatibility coverage for the 14,091-case checked-in corpus.
- Versioned Polyglot FFI validation and matched benchmark suites.
- Deterministic corpus and private-workload benchmark manifests.
- A manually dispatched workflow for memory-heavy benchmark builds.

## 0.1.0-alpha.1 - 2026-08-15

- Initial pure-Go parser, formatter, and dialect transpiler.
- Tolerant parsing with source spans, recovery, and structured diagnostics.
- Compatibility and identity fixtures from Polyglot's SQLGlot test corpus.
- Fluent builders, semantic/schema validation, query analysis, AST visitors,
  column lineage, and OpenLineage-compatible payloads.
- Reproducible Polyglot benchmarks and an Astro/Starlight site with a Monaco
  editor demo backed by the Go/WASM adapter.
