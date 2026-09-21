package golyglot

import (
	"slices"
	"strings"
)

// These function-shaped expressions are part of the SQL grammar and absent
// from the engine's callable inventory. Keep them separate from generated
// observations. ANY means an expression here, not an overload or return-type
// promise; engine coercion determines the result. See the isolated oracle tests.
func withBuiltinSpecialForms(catalog BuiltinFunctionCatalog) BuiltinFunctionCatalog {
	var names []string
	var source string
	switch catalog.Dialect {
	case DialectDuckDB:
		names = []string{"coalesce", "nullif"}
		source = "https://duckdb.org/docs/stable/sql/functions/utility"
	case DialectPostgreSQL:
		names = []string{"coalesce", "nullif", "greatest", "least"}
		source = "https://www.postgresql.org/docs/17/functions-conditional.html"
	default:
		return catalog
	}
	for _, name := range names {
		if slices.ContainsFunc(catalog.Functions, func(fn BuiltinFunction) bool { return strings.EqualFold(fn.Name, name) && fn.Kind == FunctionScalar }) {
			continue // a future engine inventory owns its own richer metadata
		}
		sig := BuiltinSignature{Parameters: []BuiltinParameter{{Name: "value"}}, VariadicType: "ANY"}
		if name == "nullif" {
			sig.Parameters = []BuiltinParameter{{Name: "value1"}, {Name: "value2"}}
			sig.VariadicType = ""
		}
		catalog.Functions = append(catalog.Functions, BuiltinFunction{
			Name: name, Kind: FunctionScalar, Source: source,
			Description: "SQL expression form. The result type depends on its arguments and the engine's coercion rules.",
			Signatures:  []BuiltinSignature{sig},
		})
	}
	slices.SortFunc(catalog.Functions, func(a, b BuiltinFunction) int {
		return strings.Compare(strings.ToLower(a.Name)+":"+string(a.Kind), strings.ToLower(b.Name)+":"+string(b.Kind))
	})
	return catalog
}
