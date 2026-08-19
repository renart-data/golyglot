---
title: Quickstart
description: Add golyglot to a Go program.
---

## Install

```sh
go get github.com/renart-data/golyglot
```

## Parse tolerant SQL

```go
package main

import (
	"fmt"

	"github.com/renart-data/golyglot"
)

func main() {
	result := golyglot.ParseTolerant(
		"SELECT customer_id, SUM(amount) FROM orders WHERE",
		golyglot.DialectPostgreSQL,
	)
	for _, diagnostic := range result.Diagnostics {
		fmt.Printf("%s: %s\n", diagnostic.Code, diagnostic.Message)
	}
}
```

Use `ParseStrict` when an incomplete or invalid statement should fail the Go call. Use `Generate` or `FormatOne` after a successful parse to emit SQL again.
