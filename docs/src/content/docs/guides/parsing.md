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

for _, recovery := range result.Recoveries {
	// Missing syntax has a zero-width span. Skipped syntax still points into sql.
	fmt.Println(recovery.Kind, recovery.Expected, recovery.Span)
}
```

Diagnostics and recovery elements expose structured `ExpectedSyntax` values,
so editor integrations do not need to extract keywords or grammar categories
from human-readable messages. In tolerant mode, incomplete `INSERT`, `UPDATE`,
and `DELETE` inputs retain their typed statement nodes rather than falling back
to a separate editor grammar.

For completion, ask the same parser for the grammar around a byte cursor:

```go
context, err := golyglot.SyntacticContextAt(sql, cursor, golyglot.DialectPostgreSQL)
if err != nil {
	panic(err)
}
fmt.Println(context.Kind, context.Expected)

// Replace covers the whole token intersecting the cursor while Prefix is the
// portion already typed. They can be passed directly to a completion filter.
fmt.Println(context.Replace, context.Prefix)
```

`SyntacticContextAt` uses the production lexer, statement dispatch, and Pratt
expression parser. It makes only the token intersecting the cursor virtual;
there is no second parser or duplicated SQL grammar.

The parsed document is byte-for-byte lossless even though whitespace is not
materialized as tokens. `OriginalSQL` returns the untouched input,
`SourceSlice` reads any valid byte span, and `SourceGapBefore` exposes the
whitespace between tokens (including trailing whitespace before EOF). Apply
targeted changes without reformatting the rest of the document:

```go
edit, err := result.EditForNode(node, "replacement")
if err != nil {
	panic(err) // synthetic and recovery-only nodes do not own source bytes
}
updated, err := result.ApplyEdits(edit)
```

Edits may be passed in any order and must not overlap. Comments and every
untouched byte remain exactly where they appeared in the input. Use AST
generation when canonical SQL is the desired output instead.

For complete input, generate canonical SQL from a typed node:

```go
statement, _, err := golyglot.ParseOne(sql, golyglot.ParseOptions{
	Dialect: golyglot.DialectPostgreSQL,
	Mode:    golyglot.Strict,
})
if err != nil {
	var syntaxError *golyglot.SyntaxError
	if errors.As(err, &syntaxError) {
		// Polyglot-compatible strict kind, message, 1-based position, and span.
		fmt.Println(syntaxError.Polyglot)
	}
	panic(err)
}

formatted, err := golyglot.GenerateWithOptions(statement.Node, golyglot.GenerateOptions{
	Pretty:    true,
	Canonical: true,
	Dialect:   golyglot.DialectPostgreSQL,
})
```

`TranspileOne` combines strict parsing, dialect transforms, and generation. The parser accepts dialect aliases through `ParseDialect`.
