# golyglot

Pure-Go SQL parsing, formatting, and dialect transpilation for Go 1.25+. No
cgo, WASM, FFI, or production dependencies.

Inspired by and rewritten from the original [Polyglot](https://github.com/tobilg/polyglot)
SQL library, with credit to its authors and contributors.

```sh
go get github.com/renart-data/golyglot/pkg/golyglot
```

```go
result := golyglot.ParseTolerant("SELECT * FROM users WHERE", golyglot.DialectPostgreSQL)
for _, diagnostic := range result.Diagnostics {
	println(diagnostic.Code, diagnostic.Message)
}
```

Use `Parse`/`ParseStrict` for source-aware ASTs and diagnostics, or
`Format`/`Transpile` for generated SQL. Tolerant parsing preserves partial
trees and source spans; unsupported fragments remain lossless raw nodes where
possible.

`ParseResult.OriginalSQL`, `SourceSlice`, and `SourceGapBefore` expose the
input byte-for-byte, including whitespace and comments. Use `EditForNode` and
`ApplyEdits` for lossless source rewrites; AST generation remains the
canonical-formatting path. Strict syntax errors retain Golyglot's detailed
diagnostic and also expose the Polyglot-compatible primary diagnostic through
`SyntaxError.Polyglot`.

For language-server integrations, `ParseResult.Recoveries` records missing and
skipped syntax with structured expectations, while `SyntacticContextAt`
returns cursor-local expectations plus the replacement span and partial token
prefix. Both are produced by the same parser used for strict parsing.

Supported features:

- 30+ named SQL dialects and pairwise transpilation
- Typed AST parsing and SQL generation
- Pretty-printing and canonical formatting
- Fluent builders for expressions, queries, joins, CTEs, set operations, and common DML
- Syntax, semantic, and schema-aware validation
- Column lineage, source-table discovery, and OpenLineage payloads
- Compact query facts and schema-aware output type/nullability inference
- Structured standalone SQL data-type parsing and normalization
- AST walking, transformation, search, and column-reference utilities

Alpha release. Run `make release-check`; use `make fixtures` to refresh the
checked-in Polyglot/SQLGlot and custom DataFusion test snapshots. The quick
compatibility gate is `make test-polyglot`, while `make test-polyglot-full`
checks all 14,091 cases. Reproducible Go/Polyglot comparisons and custom
workloads are documented in [benchmarks/README.md](benchmarks/README.md). The
Astro/Starlight docs and Monaco/WASM demo live under `docs/`; use
`make docs-build`.
