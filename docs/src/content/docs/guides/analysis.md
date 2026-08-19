---
title: Validation, analysis, and lineage
description: Add schema checks and explain where query outputs come from.
---

Schema-aware validation combines parser diagnostics with semantic checks:

```go
result := golyglot.ValidateWithSchema(sql, golyglot.ValidationSchema{
	Tables: []golyglot.SchemaTable{{
		Name:    "orders",
		Columns: []golyglot.SchemaColumn{{Name: "customer_id"}, {Name: "amount"}},
	}},
}, golyglot.DialectPostgreSQL)
```

To mirror Polyglot's strict-syntax validation, enable `StrictSyntax`. This
rejects otherwise tolerated trailing commas with the same `E005` code,
message, and 1-based comma location while leaving ordinary validation
permissive:

```go
result := golyglot.ValidateWithOptions(sql, golyglot.ValidationOptions{
	Dialect:      golyglot.DialectPostgreSQL,
	StrictSyntax: true,
})
```

For compact facts, call `AnalyzeQuery`. It reports projections, relations, CTEs, set operations, and base tables without requiring callers to walk the AST themselves.

`Lineage` resolves a named output column to its source columns. `OpenLineageColumnLineage` and the job/run event helpers turn those dependencies into JSON-compatible OpenLineage payloads.
