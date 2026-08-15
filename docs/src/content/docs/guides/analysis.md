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

For compact facts, call `AnalyzeQuery`. It reports projections, relations, CTEs, set operations, and base tables without requiring callers to walk the AST themselves.

`Lineage` resolves a named output column to its source columns. `OpenLineageColumnLineage` and the job/run event helpers turn those dependencies into JSON-compatible OpenLineage payloads.
