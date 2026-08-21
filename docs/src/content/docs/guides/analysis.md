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

Column types and nullability participate in expression validation and output
inference when they are supplied:

```go
notNull := false
schema := golyglot.ValidationSchema{Tables: []golyglot.SchemaTable{{
	Name: "orders",
	Columns: []golyglot.SchemaColumn{
		{Name: "amount", Type: "DECIMAL(10, 2)", Nullable: &notNull},
	},
}}}

analysis, err := golyglot.AnalyzeQuery(
	"SELECT amount + 1 AS gross FROM orders",
	golyglot.AnalyzeQueryOptions{
		Dialect: golyglot.DialectDuckDB,
		Schema:  &schema,
	},
)
// analysis.OutputColumns[0] describes gross as DECIMAL(10, 2).
```

`OutputNamesComplete` and `OutputTypesComplete` distinguish a fully resolved
schema from partial facts. Wildcards are expanded only when their relation
schema is known; CTE and derived-table outputs are propagated into downstream
scopes.

For type metadata outside a query, `ParseDataType` normalizes dialect aliases
while retaining precision, scale, length, timezone, and nested element/field
types:

```go
dataType, err := golyglot.ParseDataType("numeric(12, 2)", golyglot.DialectPostgreSQL)
fmt.Println(dataType.Kind, dataType.SQL()) // decimal DECIMAL(12, 2)
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

For compact facts, call `AnalyzeQuery`. It reports projections, relations,
CTEs, set operations, base tables, and inferred output columns without
requiring callers to walk the AST themselves.

`Lineage` resolves a named output column to its source columns.
`OpenLineageColumnLineage` and the job/run event helpers turn those
dependencies into JSON-compatible OpenLineage payloads.
