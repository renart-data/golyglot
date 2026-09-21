package golyglot

import "testing"

func testFunctionCatalog() *FunctionCatalogSpec {
	return &FunctionCatalogSpec{Strict: true, Functions: []FunctionSpec{
		{Name: "convert_value", Overloads: []FunctionSignature{
			{Parameters: []string{"INTEGER"}, ReturnType: "VARCHAR"},
			{Parameters: []string{"VARCHAR"}, ReturnType: "BIGINT"},
		}},
		{Name: "join_values", Overloads: []FunctionSignature{{Parameters: []string{"VARCHAR"}, Variadic: true, ReturnType: "VARCHAR"}}},
		{Name: "custom_sum", Kind: FunctionAggregate, Overloads: []FunctionSignature{{Parameters: []string{"INTEGER"}, ReturnType: "BIGINT"}}},
	}}
}

func TestFunctionCatalogValidationAndInference(t *testing.T) {
	catalog := testFunctionCatalog()
	for _, tc := range []struct{ sql, want, code string }{
		{`SELECT convert_value(1) AS value`, "VARCHAR", ""},
		{`SELECT CONVERT_VALUE('x') AS value`, "BIGINT", ""},
		{`SELECT join_values('a','b','c') AS value`, "VARCHAR", ""},
		{`WITH c AS (SELECT convert_value(1) AS x) SELECT convert_value(x) AS value FROM c`, "BIGINT", ""},
		{`SELECT convert_value()`, "", "E221"},
		{`SELECT convert_value(TRUE)`, "", "E222"},
		{`SELECT missing_function(1)`, "", "E220"},
		{`SELECT 1 WHERE convert_value(TRUE) = 'x'`, "", "E222"},
		{`SELECT 1 WHERE EXISTS (SELECT convert_value(TRUE))`, "", "E222"},
		{`SELECT 1 WHERE 1 IN (SELECT convert_value(TRUE))`, "", "E222"},
		{`SELECT CASE WHEN convert_value(TRUE) = 'x' THEN 1 ELSE 2 END`, "", "E222"},
		{`SELECT custom_sum(1) FILTER (WHERE convert_value(TRUE) = 'x')`, "", "E222"},
		{`SELECT custom_sum(1) OVER (ORDER BY convert_value(TRUE))`, "", "E222"},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			v := ValidateWithOptions(tc.sql, ValidationOptions{Dialect: DialectDuckDB, Semantic: true, FunctionCatalog: catalog})
			if tc.code != "" {
				if v.Valid || !hasValidationCode(v.Errors, tc.code) {
					t.Fatalf("want %s: %#v", tc.code, v.Errors)
				}
				return
			}
			if !v.Valid {
				t.Fatalf("valid UDF rejected: %#v", v.Errors)
			}
			a, err := AnalyzeQuery(tc.sql, AnalyzeQueryOptions{Dialect: DialectDuckDB, FunctionCatalog: catalog})
			if err != nil {
				t.Fatal(err)
			}
			if !a.OutputTypesComplete || len(a.OutputColumns) != 1 || a.OutputColumns[0].TypeHint == nil || *a.OutputColumns[0].TypeHint != tc.want {
				t.Fatalf("want %s: %#v", tc.want, a.OutputColumns)
			}
		})
	}
	schema := upstreamScopeSchema()
	for _, tc := range []struct {
		sql   string
		valid bool
	}{
		{`SELECT custom_sum(value) FROM t1`, true},
		{`SELECT id, custom_sum(value) FROM t1`, false},
		{`SELECT id FROM t1 WHERE custom_sum(value)>0`, false},
	} {
		v := ValidateWithOptions(tc.sql, ValidationOptions{Dialect: DialectDuckDB, Semantic: true, Schema: &schema, FunctionCatalog: catalog})
		if v.Valid != tc.valid {
			t.Errorf("%s: %#v", tc.sql, v.Errors)
		}
	}
}

func TestFunctionCatalogPrefersExactOverload(t *testing.T) {
	catalog := &FunctionCatalogSpec{Functions: []FunctionSpec{{Name: "f", Overloads: []FunctionSignature{
		{Parameters: []string{"DOUBLE"}, ReturnType: "DOUBLE"},
		{Parameters: []string{"INTEGER"}, ReturnType: "INTEGER"},
		{Parameters: []string{"ANY"}, ReturnType: "VARCHAR"},
	}}}}
	a, err := AnalyzeQuery(`SELECT f(1) AS n`, AnalyzeQueryOptions{Dialect: DialectDuckDB, FunctionCatalog: catalog})
	if err != nil || !a.OutputTypesComplete || a.OutputColumns[0].TypeHint == nil || *a.OutputColumns[0].TypeHint != "INTEGER" {
		t.Fatalf("exact overload: %#v, %v", a.OutputColumns, err)
	}
}

func TestFunctionCatalogContracts(t *testing.T) {
	for _, catalog := range []*FunctionCatalogSpec{
		{Functions: []FunctionSpec{{Name: ""}}},
		{Functions: []FunctionSpec{{Name: "f"}}},
		{Functions: []FunctionSpec{{Name: "f", Overloads: []FunctionSignature{{ReturnType: ""}}}}},
		{Functions: []FunctionSpec{{Name: "f", Overloads: []FunctionSignature{{ReturnType: "INTEGER); SELECT 1"}}}}},
		{Functions: []FunctionSpec{{Name: "f", Overloads: []FunctionSignature{{ReturnType: "INTEGER", Variadic: true}}}}},
		{Functions: []FunctionSpec{{Name: "f", Overloads: []FunctionSignature{{ReturnType: "INTEGER"}}}, {Name: "F", Overloads: []FunctionSignature{{ReturnType: "INTEGER"}}}}},
	} {
		if _, err := AnalyzeQuery("SELECT 1", AnalyzeQueryOptions{Dialect: DialectDuckDB, FunctionCatalog: catalog}); err == nil {
			t.Errorf("invalid catalog accepted: %#v", catalog)
		}
		if v := ValidateWithOptions("SELECT 1", ValidationOptions{Dialect: DialectDuckDB, Semantic: true, FunctionCatalog: catalog}); v.Valid || !hasValidationCode(v.Errors, "FUNCTION_CATALOG_INVALID") {
			t.Errorf("invalid catalog validation: %#v", v.Errors)
		}
	}
	catalog := testFunctionCatalog()
	catalog.CaseSensitive = true
	if v := ValidateWithOptions("SELECT CONVERT_VALUE(1)", ValidationOptions{Dialect: DialectDuckDB, Semantic: true, FunctionCatalog: catalog}); v.Valid {
		t.Fatal("case policy ignored")
	}
	insensitive := false
	catalog.Functions[0].CaseSensitive = &insensitive
	if v := ValidateWithOptions("SELECT CONVERT_VALUE(1)", ValidationOptions{Dialect: DialectDuckDB, Semantic: true, FunctionCatalog: catalog}); !v.Valid {
		t.Fatalf("per-function policy ignored: %#v", v.Errors)
	}
	catalog.Strict = false
	if v := ValidateWithOptions("SELECT abs(-1)", ValidationOptions{Dialect: DialectDuckDB, Semantic: true, FunctionCatalog: catalog}); !v.Valid {
		t.Fatalf("extending catalog rejected builtin: %#v", v.Errors)
	}
}
