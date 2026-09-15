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
		{Name: "customer_id", Type: "BIGINT", Nullable: &notNull},
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

`ColumnUses` adds dependencies outside the select list. Each entry describes
one expression and its context: `join`, `filter`, `group`, `having`, `qualify`,
`window_partition`, `window_order`, `order`, `connect_by`, `subquery`, or
`set_filter` (the filtering side of `EXCEPT`/`INTERSECT`).

```go
analysis, err := golyglot.AnalyzeQuery(
	"SELECT customer_id FROM orders WHERE amount > 100",
	golyglot.AnalyzeQueryOptions{Dialect: golyglot.DialectDuckDB, Schema: &schema},
)
for _, use := range analysis.ColumnUses {
	// References identifies the immediate relation; Upstream follows CTE and
	// derived-table outputs to physical source columns.
	fmt.Println(use.Context, use.ExpressionSQL, use.Upstream, use.Complete)
}
```

Expression and reference spans are byte offsets into the original SQL. An
upstream reference retains the location where it is used, even when its
physical column name differs from a CTE alias. Unknown and ambiguous bindings
remain explicit; `Complete` is false when a use cannot be fully resolved.
Implicit `NATURAL JOIN` keys and name-aligned set-operation lineage are not
yet resolved; uses that depend on them are marked incomplete.
Unused CTE definitions do not contribute result dependencies. These facts
are separate from output-value lineage: a filter column is not added to a
projection merely because it controls which rows survive.

`DiffQuerySemantics` includes these physical dependencies when comparing
schemas. An unchanged query can therefore report a changed type, nullability,
or presence for a column used only in a predicate. Its `Complete` flag also
accounts for unresolved predicate dependencies.

Schema validation uses the same lexical bindings: CTEs see earlier siblings,
correlated subqueries see legal outer references, and a local alias shadows
the same alias outside its query block. Invalid references are still reported
at their original source locations.

`Lineage` resolves a named output column to its source columns.
`OpenLineageColumnLineage` and the job/run event helpers turn those
dependencies into JSON-compatible OpenLineage payloads.
