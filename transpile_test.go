package golyglot

import "testing"

func TestTranspileFunctionRewrites(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		from Dialect
		to   Dialect
		want string
	}{
		{name: "nvl to mysql", sql: "SELECT NVL(a, b)", from: DialectGeneric, to: DialectMySQL, want: "SELECT IFNULL(a, b)"},
		{name: "nvl to postgres", sql: "SELECT NVL(a, b)", from: DialectGeneric, to: DialectPostgreSQL, want: "SELECT COALESCE(a, b)"},
		{name: "ifnull to postgres", sql: "SELECT IFNULL(a, b)", from: DialectGeneric, to: DialectPostgreSQL, want: "SELECT COALESCE(a, b)"},
		{name: "group concat", sql: "SELECT GROUP_CONCAT(name)", from: DialectMySQL, to: DialectPostgreSQL, want: "SELECT STRING_AGG(name, ',')"},
		{name: "array agg", sql: "SELECT ARRAY_AGG(name)", from: DialectGeneric, to: DialectMySQL, want: "SELECT GROUP_CONCAT(name)"},
		{name: "substring", sql: "SELECT SUBSTR(name, 1, 5)", from: DialectGeneric, to: DialectPostgreSQL, want: "SELECT SUBSTRING(name FROM 1 FOR 5)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := TranspileOne(test.sql, test.from, test.to)
			if err != nil {
				t.Fatalf("transpile error: %v", err)
			}
			if got != test.want {
				t.Fatalf("transpiled SQL = %q, want %q", got, test.want)
			}
		})
	}
}

func TestTranspileTSQLPagination(t *testing.T) {
	got, err := TranspileOne("SELECT a FROM t ORDER BY a LIMIT 10 OFFSET 5", DialectGeneric, DialectTSQL)
	if err != nil {
		t.Fatalf("transpile error: %v", err)
	}
	want := "SELECT a FROM t ORDER BY a OFFSET 5 ROWS FETCH NEXT 10 ROWS ONLY"
	if got != want {
		t.Fatalf("transpiled SQL = %q, want %q", got, want)
	}
}

func TestTranspileTSQLTopToLimit(t *testing.T) {
	got, err := TranspileOne("SELECT TOP (10) a FROM t", DialectTSQL, DialectGeneric)
	if err != nil {
		t.Fatalf("transpile error: %v", err)
	}
	if got != "SELECT a FROM t LIMIT 10" {
		t.Fatalf("transpiled SQL = %q, want %q", got, "SELECT a FROM t LIMIT 10")
	}
}

func TestFormatOne(t *testing.T) {
	got, err := FormatOne("SELECT a,b FROM t", DialectGeneric)
	if err != nil {
		t.Fatalf("format error: %v", err)
	}
	want := "SELECT\n  a,\n  b\nFROM t"
	if got != want {
		t.Fatalf("formatted SQL = %q, want %q", got, want)
	}
}

func TestTranspileRejectsMultipleStatementsInOne(t *testing.T) {
	if _, err := TranspileOne("SELECT 1; SELECT 2", DialectGeneric, DialectPostgreSQL); err == nil {
		t.Fatal("TranspileOne succeeded for multiple statements")
	}
}

func TestCommonDMLRoundTrip(t *testing.T) {
	tests := []string{
		"INSERT INTO t (a, b) VALUES (1, 2), (3, 4)",
		"UPDATE t SET a = 1, b = 2 WHERE c = 3",
		"DELETE FROM t WHERE a = 1",
	}
	for _, sql := range tests {
		result, err := ParseStrict(sql, DialectGeneric)
		if err != nil {
			t.Fatalf("parse %q: %v", sql, err)
		}
		generated, err := Generate(result.Statements[0].Node)
		if err != nil {
			t.Fatalf("generate %q: %v", sql, err)
		}
		if generated != sql {
			t.Fatalf("roundtrip %q = %q", sql, generated)
		}
	}
}
