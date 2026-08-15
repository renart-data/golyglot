---
title: Parsing and formatting
description: Work with typed ASTs and editor-friendly diagnostics.
---

`Parse` returns the source text, tokens, statements, and diagnostics together. In tolerant mode, the parser attempts to preserve a partial tree after an error, which makes it suitable for editor integrations.

```go
result := golyglot.ParseTolerant(sql, golyglot.DialectPostgreSQL)
for _, diagnostic := range result.Diagnostics {
	// diagnostic.Span is a half-open byte range in sql.
	fmt.Println(diagnostic.Code, diagnostic.Message, diagnostic.Span)
}
```

For complete input, generate canonical SQL from a typed node:

```go
statement, _, err := golyglot.ParseOne(sql, golyglot.ParseOptions{
	Dialect: golyglot.DialectPostgreSQL,
	Mode:    golyglot.Strict,
})
if err != nil {
	panic(err)
}

formatted, err := golyglot.GenerateWithOptions(statement.Node, golyglot.GenerateOptions{
	Pretty:    true,
	Canonical: true,
	Dialect:   golyglot.DialectPostgreSQL,
})
```

`TranspileOne` combines strict parsing, dialect transforms, and generation. The parser accepts dialect aliases through `ParseDialect`.
