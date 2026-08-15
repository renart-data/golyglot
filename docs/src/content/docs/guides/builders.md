---
title: Fluent builders
description: Construct SQL without assembling strings by hand.
---

Builders are immutable-by-value: deriving a query leaves the original expression available for reuse.

```go
query := golyglot.Select(
	golyglot.Column("customer_id"),
	golyglot.Func("sum", golyglot.Column("amount")).As("total"),
).From(golyglot.Table("orders")).
	Where(golyglot.Column("status").Eq(golyglot.Lit("paid"))).
	GroupBy(golyglot.Column("customer_id"))

sql, err := query.SQL(golyglot.DialectPostgreSQL)
```

The same builder values can be passed to `Generate` through `AST()` when an application needs to inspect or transform the typed tree.
