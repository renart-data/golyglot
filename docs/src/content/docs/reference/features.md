---
title: Feature reference
description: The current alpha surface.
---

The alpha release currently exposes:

- `Parse`, `ParseTolerant`, and `ParseStrict`
- `Generate`, `Format`, and `Transpile`
- 34 registered dialect names plus aliases
- Typed AST nodes with source spans
- Fluent expressions, SELECT queries, CTEs, set operations, and common DML builders
- Syntax, semantic, and schema-aware validation
- `AnalyzeQuery` facts plus schema-aware output type and nullability inference
- `ParseDataType` for normalized scalar and nested SQL data types
- `Lineage`, source-table discovery, and OpenLineage column/job/run payloads
- `Walk`, `FindAll`, `Transform`, and column-reference helpers

The public package is intentionally independent of cgo, WASM, FFI, and LSP protocol packages. The browser demo is a separate adapter around the same Go API.
