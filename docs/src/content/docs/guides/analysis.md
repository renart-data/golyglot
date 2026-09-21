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
Implicit `NATURAL JOIN` keys are not yet resolved; uses that depend on them
are marked incomplete. `UNION BY NAME` aligns dependencies by output name,
including missing columns that the set operation pads with NULL.
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

## Builtin metadata for editors

`BuiltinFunctionCatalogForDialect` exposes versioned builtin inventories for
DuckDB, PostgreSQL and ClickHouse without executing SQL:

```go
catalog, available := golyglot.BuiltinFunctionCatalogForDialect(golyglot.DialectDuckDB)
if available {
	for _, function := range catalog.Functions {
		fmt.Println(function.Name, function.Kind, function.Signatures)
	}
}
```

Each result is an independent copy. Unsupported dialects return `false` rather
than borrowing another vendor's functions. The catalog records the observed
engine version; installed extensions and user-defined functions are separate.

The inventory also includes verified grammar-level forms absent from engine
catalogs: DuckDB `COALESCE`/`NULLIF` and PostgreSQL
`COALESCE`/`NULLIF`/`GREATEST`/`LEAST`. These entries include a separate source
link and leave the argument-dependent result type unknown.

These are **catalog declarations**, not inferred result types for the current
query. Polymorphic types remain placeholders, file-dependent table schemas stay
unknown, and ClickHouse syntax text is not promoted to a return type. Use
`AnalyzeQuery` for inference and `FunctionCatalogSpec` for supplied UDF rules.

## Lambdas and aliases

Higher-order functions such as `TRANSFORM(values, x -> x + quantity)` bind
`x` locally. It is not a table column and does not appear in lineage. The
captured `quantity` still needs to resolve in the surrounding query. Nested
lambdas can shadow outer parameters; Snowflake's `x INT -> ...` form also
supplies a local type.

Same-SELECT aliases are available to later expressions where the dialect
supports them, including Snowflake and DuckDB. An input column takes
precedence over a same-named alias, forward references are not accepted,
and quoted names retain the dialect's case rules. Clause visibility is
checked separately: for example, PostgreSQL does not expose SELECT aliases
to WHERE or HAVING.

## Set-operation contracts

For a set operation, `Projections` describes the **combined output**, not
just the first branch. Its type and nullability agree with `OutputColumns`;
branch-specific expressions remain available in `SetOperations.Branches`.

`UNION BY NAME` matches names instead of ordinals, preserves left-hand
column order, and appends right-only names. Missing values are nullable.
Duplicate names are rejected as ambiguous. Unknown wildcard schemas remain
incomplete rather than being treated as empty branches. Public
`OutputColumns` and `Lineage` use the same name alignment.
`Lineage` returns an error when a name-aligned branch's wildcard cannot be
resolved; it does not invent NULL padding for an unknown schema.

## Grouping and function placement

Semantic validation reports:

| Code | Meaning |
| --- | --- |
| `E230` | A column is neither grouped nor aggregated. |
| `E231` | An aggregate is in a disallowed clause or inside another aggregate. |
| `E232` | A window function has invalid placement/nesting or needs OVER. |
| `W002` | Grouping could not be checked for an opaque expression or unexpanded wildcard. |

The checks account for grouping expressions, aliases, ordinals, grouping
sets, and aggregate expressions inside windows. Declared PostgreSQL primary
keys can establish functional dependency. SQLite and MySQL bare-column
behavior is not rejected: MySQL's server SQL mode is not known here.
These checks are not a replacement for the warehouse's own validator.

Use `ValidationOptions{Semantic: false}` for syntax-only validation. The
convenience `Validate` and `ValidateWithSchema` calls enable semantic checks.

## Function and UDF catalogs

Supply warehouse-specific signatures without connecting to the warehouse:

```go
catalog := &golyglot.FunctionCatalogSpec{
	Functions: []golyglot.FunctionSpec{{
		Name: "normalize_account",
		Kind: golyglot.FunctionScalar,
		Overloads: []golyglot.FunctionSignature{{
			Parameters: []string{"VARCHAR"},
			ReturnType: "VARCHAR",
		}},
	}},
}

validation := golyglot.ValidateWithOptions(sql, golyglot.ValidationOptions{
	Dialect:         golyglot.DialectDuckDB,
	Semantic:        true,
	Schema:          &schema,
	FunctionCatalog: catalog,
})
analysis, err := golyglot.AnalyzeQuery(sql, golyglot.AnalyzeQueryOptions{
	Dialect:         golyglot.DialectDuckDB,
	Schema:          &schema,
	FunctionCatalog: catalog,
})
```

Catalogs extend builtin inference by default. `Strict: true` instead
requires every function name to be declared. `CaseSensitive` controls name
matching globally, and a function's optional `CaseSensitive` overrides it.
`Kind` is `scalar` (default), `aggregate`, or `window`, and participates in
placement and grouping checks.

`Parameters` and `ReturnType` use SQL type names. `ANY` is a wildcard
parameter. `Variadic: true` repeats the final parameter one or more times.
Exact argument matches are preferred to compatible conversions and `ANY`;
unresolved overload ties with different return types remain unknown. The
optional `Nullable` declares return nullability; omitting it leaves that
fact unknown.

Validation uses `E220` for undeclared strict-catalog functions, `E221` for
argument-count mismatches, and `E222` for incompatible argument types.
Invalid catalog definitions yield `FUNCTION_CATALOG_INVALID` during
validation or a Go error from `AnalyzeQuery`. Catalogs are local to the call;
they do not mutate a process-wide registry.
