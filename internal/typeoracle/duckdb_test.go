package typeoracle

import (
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/renart-data/golyglot/pkg/golyglot"
)

func TestParseDuckDBColumns(t *testing.T) {
	columns, err := parseDuckDBColumns([]byte(`[
		{"name":"value","type":"HUGEINT","not_null":"false"},
		{"name":"label","type":"VARCHAR","not_null":"false"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 2 || columns[0].Name != "value" || columns[0].Type != "HUGEINT" || columns[1].Type != "VARCHAR" {
		t.Fatalf("columns = %#v", columns)
	}
}

func TestDuckDBAdapterDescribe(t *testing.T) {
	if os.Getenv("GOLYGLOT_DUCKDB_ORACLE") != "1" {
		t.Skip("set GOLYGLOT_DUCKDB_ORACLE=1 to run the local DuckDB oracle")
	}
	executable, err := exec.LookPath("duckdb")
	if err != nil {
		t.Skip("duckdb executable is not installed")
	}
	testCase := GenerateCases(1, 1)[0]
	testCase.SQL = "SELECT SUM(integer_col) AS value FROM oracle_values"
	columns, err := (DuckDBAdapter{Executable: executable}).Describe(context.Background(), testCase)
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 1 || columns[0].Name != "value" || columns[0].Type != "HUGEINT" {
		t.Fatalf("columns = %#v", columns)
	}
}

func TestDuckDBSchemaSQLRejectsInvalidTypes(t *testing.T) {
	_, err := duckDBSchemaSQL(&golyglot.ValidationSchema{Tables: []golyglot.SchemaTable{{
		Name: "source", Columns: []golyglot.SchemaColumn{{Name: "value", Type: "INTEGER); DROP TABLE source; --"}},
	}}})
	if err == nil {
		t.Fatal("unsafe data type was accepted")
	}
}
