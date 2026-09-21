package golyglot

import (
	"slices"
	"strings"
	"testing"
)

func TestBuiltinFunctionCatalogs(t *testing.T) {
	for _, tc := range []struct {
		dialect Dialect
		table   string
	}{
		{DialectDuckDB, "range"}, {DialectPostgreSQL, "generate_series"}, {DialectClickHouse, "numbers"},
	} {
		t.Run(string(tc.dialect), func(t *testing.T) {
			catalog, ok := BuiltinFunctionCatalogForDialect(tc.dialect)
			if !ok || catalog.Dialect != tc.dialect || catalog.EngineVersion == "" || catalog.Source == "" || len(catalog.Functions) < 100 {
				t.Fatalf("missing versioned engine inventory: %#v", catalog)
			}
			seen := map[string]bool{}
			previous := ""
			for _, fn := range catalog.Functions {
				key := strings.ToLower(fn.Name) + ":" + string(fn.Kind)
				if seen[key] || key < previous {
					t.Fatalf("unstable/duplicate entry %q", key)
				}
				seen[key], previous = true, key
				if fn.Kind == FunctionTable {
					for _, sig := range fn.Signatures {
						if sig.ReturnType == "UNKNOWN" {
							t.Fatal("invented table schema")
						}
					}
				}
			}
			for _, key := range []string{"count:aggregate", tc.table + ":table", "row_number:window"} {
				if !seen[key] {
					t.Errorf("missing %s", key)
				}
			}
		})
	}
}

func TestBuiltinCatalogIsolationAndCopySafety(t *testing.T) {
	for _, dialect := range []Dialect{DialectGeneric, DialectRedshift, Dialect("not-a-dialect")} {
		if _, ok := BuiltinFunctionCatalogForDialect(dialect); ok {
			t.Fatalf("unverified dialect fallback: %s", dialect)
		}
	}
	first, _ := BuiltinFunctionCatalogForDialect(DialectDuckDB)
	index := slices.IndexFunc(first.Functions, func(fn BuiltinFunction) bool { return fn.Name == "abs" })
	if index < 0 || len(first.Functions[index].Signatures[0].Parameters) == 0 {
		t.Fatal("missing abs signature")
	}
	first.Functions[index].Name = "corrupted"
	first.Functions[index].Signatures[0].Parameters[0].Type = "corrupted"
	second, _ := BuiltinFunctionCatalogForDialect(DialectDuckDB)
	if second.Functions[index].Name != "abs" || second.Functions[index].Signatures[0].Parameters[0].Type == "corrupted" {
		t.Fatal("callers can mutate shared catalog data")
	}
	ch, _ := BuiltinFunctionCatalogForDialect(DialectClickHouse)
	for _, fn := range ch.Functions {
		for _, sig := range fn.Signatures {
			if sig.ReturnType != "" {
				t.Fatal("ClickHouse prose was promoted to an inferred SQL type")
			}
		}
	}
}

func TestBuiltinSpecialForms(t *testing.T) {
	for _, dialect := range []Dialect{DialectDuckDB, DialectPostgreSQL} {
		t.Run(string(dialect), func(t *testing.T) {
			catalog, _ := BuiltinFunctionCatalogForDialect(dialect)
			for _, name := range []string{"coalesce", "nullif", "greatest", "least"} {
				index := slices.IndexFunc(catalog.Functions, func(fn BuiltinFunction) bool { return fn.Name == name && fn.Kind == FunctionScalar })
				if index < 0 {
					t.Errorf("missing expression form %s", name)
					continue
				}
				fn := catalog.Functions[index]
				if len(fn.Signatures) == 0 || len(fn.Signatures[0].Parameters) == 0 {
					t.Errorf("%s has no parameter help", name)
				}
				// The concrete result depends on coercion and all arguments.
				if name == "coalesce" || name == "nullif" {
					if fn.Signatures[0].ReturnType != "" {
						t.Errorf("%s invents a concrete result type", name)
					}
				}
			}
		})
	}
}
