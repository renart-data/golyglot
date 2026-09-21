package main

import (
	"testing"

	"github.com/renart-data/golyglot/pkg/golyglot"
)

func TestNormalizeCatalog(t *testing.T) {
	rows := []catalogRow{
		{Name: "row_number", Kind: golyglot.FunctionAggregate, Result: "BIGINT"},
		{Name: "read_parquet", Kind: golyglot.FunctionTable, Names: []string{"col0", "union_by_name"}, Types: []string{"VARCHAR", "BOOLEAN"}},
		{Name: "abs", Kind: golyglot.FunctionScalar, Names: []string{"x"}, Types: []string{"INTEGER"}, Result: "INTEGER"},
		{Name: "abs", Kind: golyglot.FunctionScalar, Names: []string{"x"}, Types: []string{"INTEGER"}, Result: "INTEGER"},
	}
	catalog := normalizeCatalog(golyglot.DialectDuckDB, "test", "catalog", rows)
	if len(catalog.Functions) != 3 || catalog.Functions[0].Name != "abs" || len(catalog.Functions[0].Signatures) != 1 {
		t.Fatalf("normalization: %#v", catalog)
	}
	if catalog.Functions[2].Kind != golyglot.FunctionWindow {
		t.Fatal("window-only call classified as plain aggregate")
	}
	parquet := catalog.Functions[1].Signatures[0]
	if parquet.ReturnType != "" || parquet.Parameters[0].Named || !parquet.Parameters[1].Named || !parquet.Parameters[1].Optional {
		t.Fatalf("table argument metadata: %#v", parquet)
	}
}

func TestVariadicAndNamedParameterMetadata(t *testing.T) {
	pg := normalizeCatalog(golyglot.DialectPostgreSQL, "test", "catalog", []catalogRow{{Name: "format", Kind: golyglot.FunctionScalar, Types: []string{"text", "any"}, Variadic: "any", Result: "text"}})
	if sig := pg.Functions[0].Signatures[0]; len(sig.Parameters) != 1 || sig.VariadicType != "any" {
		t.Fatalf("duplicated variadic argument: %#v", sig)
	}
	duck := normalizeCatalog(golyglot.DialectDuckDB, "test", "catalog", []catalogRow{{Name: "read_csv", Kind: golyglot.FunctionTable, Names: []string{"col0", "columns"}, Types: []string{"VARCHAR", "ANY"}}})
	if !duck.Functions[0].Signatures[0].Parameters[1].Named {
		t.Fatal("columns is a named option, not an ordinal colN parameter")
	}
}

func TestClickHouseFirstValueRemainsAnAggregate(t *testing.T) {
	catalog := normalizeCatalog(golyglot.DialectClickHouse, "test", "catalog", []catalogRow{{Name: "first_value", Kind: golyglot.FunctionAggregate}, {Name: "last_value", Kind: golyglot.FunctionAggregate}})
	for _, fn := range catalog.Functions {
		if fn.Kind != golyglot.FunctionAggregate {
			t.Errorf("%s can execute without OVER in ClickHouse", fn.Name)
		}
	}
}
