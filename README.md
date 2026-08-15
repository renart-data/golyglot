# golyglot

Pure-Go SQL parsing, formatting, and dialect transpilation for Go 1.25+. No
cgo, WASM, FFI, or production dependencies.

Inspired by and rewritten from the original [Polyglot](https://github.com/tobilg/polyglot)
SQL library, with credit to its authors and contributors.

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

Supported features:

- 30+ named SQL dialects and pairwise transpilation
- Typed AST parsing and SQL generation
- Pretty-printing and canonical formatting
- Fluent builders for expressions, queries, joins, CTEs, set operations, and common DML
- Syntax, semantic, and schema-aware validation
- Column lineage, source-table discovery, and OpenLineage payloads
- Compact query facts for projections, relations, CTEs, and set operations
- AST walking, transformation, search, and column-reference utilities

Alpha release. Run `make release-check`; use `make fixtures` for optional
upstream Polyglot compatibility fixtures.
