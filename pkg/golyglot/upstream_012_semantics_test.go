package golyglot

import "testing"

func TestUnionOutputFactsAgree(t *testing.T) {
	for _, sql := range []string{
		`SELECT CAST(1 AS INTEGER) AS amount UNION ALL SELECT CAST(2 AS DOUBLE) AS amount`,
		`SELECT CAST(1 AS INTEGER) AS amount UNION ALL BY NAME SELECT CAST(2 AS DOUBLE) AS amount`,
		`WITH u AS ((SELECT CAST(1 AS INTEGER) AS amount UNION ALL SELECT CAST(2 AS FLOAT)) UNION ALL SELECT CAST(3 AS DOUBLE)) SELECT amount FROM u`,
	} {
		a, err := AnalyzeQuery(sql, AnalyzeQueryOptions{Dialect: DialectDuckDB})
		if err != nil {
			t.Fatal(err)
		}
		if len(a.OutputColumns) != 1 || a.OutputColumns[0].TypeHint == nil || *a.OutputColumns[0].TypeHint != "DOUBLE" {
			t.Errorf("%s: output %#v", sql, a.OutputColumns)
		}
		if len(a.Projections) != 1 || a.Projections[0].TypeHint == nil || *a.Projections[0].TypeHint != "DOUBLE" || a.Projections[0].CastType != nil {
			t.Errorf("%s: projections %#v", sql, a.Projections)
		}
	}
}

func TestUnionByNameAlignsAndPads(t *testing.T) {
	cases := []struct {
		sql                       string
		names, types, nullability []string
	}{
		{`SELECT 1 AS a, 'x' AS b UNION ALL BY NAME SELECT 'y' AS b, 2.5::DOUBLE AS a`, []string{"a", "b"}, []string{"DOUBLE", "VARCHAR"}, []string{"non_null", "non_null"}},
		{`SELECT 1 AS a UNION ALL BY NAME SELECT 'x' AS b`, []string{"a", "b"}, []string{"INTEGER", "VARCHAR"}, []string{"nullable", "nullable"}},
		{`SELECT 1 AS a UNION ALL BY NAME SELECT 2 AS a, 'x' AS b UNION ALL BY NAME SELECT 3.5::DOUBLE AS a`, []string{"a", "b"}, []string{"DOUBLE", "VARCHAR"}, []string{"non_null", "nullable"}},
	}
	for _, tc := range cases {
		a, err := AnalyzeQuery(tc.sql, AnalyzeQueryOptions{Dialect: DialectDuckDB})
		if err != nil {
			t.Fatal(err)
		}
		if !a.OutputTypesComplete || !a.OutputNamesComplete || len(a.OutputColumns) != len(tc.names) {
			t.Errorf("%s: output %#v complete=%v/%v", tc.sql, a.OutputColumns, a.OutputNamesComplete, a.OutputTypesComplete)
			continue
		}
		for i, c := range a.OutputColumns {
			if c.Name != tc.names[i] || c.TypeHint == nil || *c.TypeHint != tc.types[i] || c.Nullability != tc.nullability[i] {
				t.Errorf("%s: column %d = %#v", tc.sql, i, c)
			}
		}
		v := Validate(tc.sql, DialectDuckDB)
		if !v.Valid {
			t.Errorf("valid BY NAME rejected: %#v", v.Errors)
		}
	}
}

func TestLexicalLambdaAndLateralAliasValidation(t *testing.T) {
	strict := true
	schema := ValidationSchema{Strict: &strict, Tables: []SchemaTable{{Name: "items", Columns: []SchemaColumn{{Name: "quantity", Type: "INTEGER"}, {Name: "values", Type: "INTEGER[]"}, {Name: "obj", Type: "JSON"}}}}}
	for _, tc := range []struct {
		sql     string
		dialect Dialect
		valid   bool
	}{
		{`SELECT TRANSFORM(ARRAY_CONSTRUCT(quantity), x -> x + 1) FROM items`, DialectSnowflake, true},
		{`SELECT TRANSFORM(values, x -> x + quantity) FROM items`, DialectSnowflake, true},
		{`SELECT TRANSFORM(values, x -> x + missing) FROM items`, DialectSnowflake, false},
		{`SELECT TRANSFORM(values, x -> TRANSFORM(values, x -> x + quantity)) FROM items`, DialectSnowflake, true},
		{`SELECT TRANSFORM(values, x -> x + 1), x FROM items`, DialectSnowflake, false},
		{`SELECT quantity + 1 AS adjusted, adjusted * 2 AS doubled, doubled + 1 AS final FROM items`, DialectSnowflake, true},
		{`SELECT quantity + 1 AS adjusted, adjusted * 2 FROM items`, DialectPostgreSQL, false},
		{`SELECT adjusted * 2, quantity + 1 AS adjusted FROM items`, DialectDuckDB, false},
		{`SELECT quantity + 1 AS adjusted, adjusted * 2 FROM items`, DialectDuckDB, true},
		{`SELECT quantity AS "Adjusted", "Adjusted" + 1 FROM items`, DialectSnowflake, true},
		{`SELECT quantity AS "Adjusted", adjusted + 1 FROM items`, DialectSnowflake, false},
		{`SELECT obj -> 'field' FROM items`, DialectDuckDB, true},
		{`SELECT missing -> 'field' FROM items`, DialectDuckDB, false},
	} {
		t.Run(tc.sql+string(tc.dialect), func(t *testing.T) {
			v := ValidateWithSchema(tc.sql, schema, tc.dialect)
			if v.Valid != tc.valid {
				t.Fatalf("valid=%v want %v: %#v", v.Valid, tc.valid, v.Errors)
			}
		})
	}
}

func TestLambdaAndLateralAliasTypesAndLineage(t *testing.T) {
	schema := ValidationSchema{Tables: []SchemaTable{{Name: "items", Columns: []SchemaColumn{{Name: "quantity", Type: "INTEGER"}, {Name: "values", Type: "INTEGER[]"}}}}}
	for _, tc := range []struct{ sql, want string }{
		{`SELECT quantity + 1 AS adjusted, adjusted * 2 AS doubled FROM items`, "INTEGER"},
		{`SELECT TRANSFORM(values, x -> x + quantity) AS mapped FROM items`, "ARRAY<INTEGER>"},
	} {
		a, err := AnalyzeQuery(tc.sql, AnalyzeQueryOptions{Dialect: DialectSnowflake, Schema: &schema})
		if err != nil {
			t.Fatal(err)
		}
		last := len(a.OutputColumns) - 1
		if last < 0 || a.OutputColumns[last].TypeHint == nil || *a.OutputColumns[last].TypeHint != tc.want {
			t.Fatalf("output: %#v", a.OutputColumns)
		}
		for _, p := range a.Projections {
			for _, ref := range p.Upstream {
				if ref.Column == "x" || ref.Column == "adjusted" {
					t.Errorf("local variable leaked into lineage: %#v", ref)
				}
			}
		}
	}
}

func TestTypedLambdaAndPublicLineage(t *testing.T) {
	schema := ValidationSchema{Tables: []SchemaTable{{Name: "items", Columns: []SchemaColumn{{Name: "quantity", Type: "INTEGER"}, {Name: "values", Type: "INTEGER[]"}}}}}
	for _, sql := range []string{
		`SELECT TRANSFORM(values, x INT -> x + quantity) AS mapped FROM items`,
		`SELECT TRANSFORM(values, "Value" INT -> "Value" + quantity) AS mapped FROM items`,
		`SELECT TRANSFORM(values, x -> x + quantity) AS mapped FROM items`,
	} {
		if v := ValidateWithSchema(sql, schema, DialectSnowflake); !v.Valid {
			t.Errorf("%s: %#v", sql, v.Errors)
			continue
		}
		a, err := AnalyzeQuery(sql, AnalyzeQueryOptions{Dialect: DialectSnowflake, Schema: &schema})
		if err != nil {
			t.Fatal(err)
		}
		if len(a.OutputColumns) != 1 || a.OutputColumns[0].TypeHint == nil || *a.OutputColumns[0].TypeHint != "ARRAY<INTEGER>" {
			t.Errorf("types: %#v", a.OutputColumns)
		}
		lineage, err := LineageWithSchema("mapped", sql, schema, DialectSnowflake)
		if err != nil {
			t.Fatal(err)
		}
		if len(lineage.Downstream) != 2 {
			t.Errorf("lambda locals in public lineage: %#v", lineage.Downstream)
		}
	}
}

func TestAliasClauseVisibility(t *testing.T) {
	for _, tc := range []struct {
		sql     string
		dialect Dialect
		valid   bool
	}{
		{`SELECT id, SUM(value) AS total FROM t1 GROUP BY id HAVING total>0`, DialectDuckDB, true},
		{`SELECT value AS adjusted FROM t1 WHERE adjusted>0`, DialectDuckDB, true},
		{`SELECT id, SUM(value) AS total FROM t1 GROUP BY id HAVING total>0`, DialectPostgreSQL, false},
		{`SELECT SUM(value) AS s, SUM(s) FROM t1`, DialectDuckDB, false},
		{`SELECT ROW_NUMBER() OVER () AS n FROM t1 WHERE n>1`, DialectDuckDB, false},
	} {
		if v := ValidateWithSchema(tc.sql, upstreamScopeSchema(), tc.dialect); v.Valid != tc.valid {
			t.Errorf("%s %s: valid=%v, errors=%#v", tc.dialect, tc.sql, v.Valid, v.Errors)
		}
	}
}

func TestUnionByNameDependenciesAndUnknownColumns(t *testing.T) {
	schema := upstreamScopeSchema()
	query := `WITH u AS (SELECT id, value FROM t1 UNION ALL BY NAME SELECT id FROM t2) SELECT value FROM u WHERE value>0`
	a, err := AnalyzeQuery(query, AnalyzeQueryOptions{Dialect: DialectDuckDB, Schema: &schema})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.OutputColumns) != 1 || a.OutputColumns[0].TypeHint == nil || *a.OutputColumns[0].TypeHint != "INTEGER" || a.OutputColumns[0].Nullability != "nullable" {
		t.Errorf("padded CTE types: %#v", a.OutputColumns)
	}
	for _, use := range a.ColumnUses {
		if use.Context == "filter" && (!use.Complete || len(use.Upstream) != 1 || use.Upstream[0].Column != "value") {
			t.Errorf("padded lineage: %#v", use)
		}
	}
	bad := `SELECT id AS value, id AS value FROM t1 UNION ALL BY NAME SELECT id AS value FROM t2`
	if v := ValidateWithSchema(bad, schema, DialectDuckDB); v.Valid {
		t.Error("ambiguous duplicate UNION name accepted")
	}
	unknown := `SELECT * FROM unknown_table UNION ALL BY NAME SELECT 1 AS known`
	unknownAnalysis, err := AnalyzeQuery(unknown, AnalyzeQueryOptions{Dialect: DialectDuckDB})
	if err != nil {
		t.Fatal(err)
	}
	if unknownAnalysis.OutputTypesComplete || unknownAnalysis.OutputNamesComplete {
		t.Error("unknown wildcard must stay incomplete")
	}
}

func TestUnionByNameQuotedNames(t *testing.T) {
	query := `SELECT 1 AS "Value" UNION ALL BY NAME SELECT 'x' AS value`
	a, err := AnalyzeQuery(query, AnalyzeQueryOptions{Dialect: DialectSnowflake})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.OutputColumns) != 2 {
		t.Fatalf("quoted and unquoted Snowflake names were conflated: %#v", a.OutputColumns)
	}
	for _, column := range a.OutputColumns {
		if column.Nullability != "nullable" {
			t.Errorf("missing NULL padding: %#v", column)
		}
	}
	schema := upstreamScopeSchema()
	cte := `WITH u AS (SELECT value AS "Value" FROM t1 UNION ALL BY NAME SELECT id AS value FROM t2) SELECT "Value" FROM u`
	a, err = AnalyzeQuery(cte, AnalyzeQueryOptions{Dialect: DialectSnowflake, Schema: &schema})
	if err != nil || !a.OutputTypesComplete || a.OutputColumns[0].TypeHint == nil || *a.OutputColumns[0].TypeHint != "INTEGER" {
		t.Errorf("quoted CTE output: %#v, %v", a.OutputColumns, err)
	}
	lineage, err := LineageWithSchema("Value", cte, schema, DialectSnowflake)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range lineage.Walk() {
		if node.SourceKind == "table" && node.SourceName != "t1" {
			t.Errorf("quoted CTE includes wrong branch: %s", node.Name)
		}
	}
}

func TestSetBranchesRetainCTEScope(t *testing.T) {
	a, err := AnalyzeQuery(`WITH c AS (SELECT CAST(1 AS DOUBLE) AS v) SELECT 0 AS v UNION ALL SELECT v FROM c`, AnalyzeQueryOptions{Dialect: DialectDuckDB})
	if err != nil {
		t.Fatal(err)
	}
	if !a.OutputTypesComplete || len(a.OutputColumns) != 1 || a.OutputColumns[0].TypeHint == nil || *a.OutputColumns[0].TypeHint != "DOUBLE" {
		t.Fatalf("set branch lost its WITH scope: %#v", a.OutputColumns)
	}
}

func TestUnionByNamePublicOutputsAndLineage(t *testing.T) {
	schema := upstreamScopeSchema()
	for _, query := range []string{
		`SELECT value AS a FROM t1 UNION ALL BY NAME SELECT id AS b FROM t2`,
		`WITH u AS (SELECT value AS a FROM t1 UNION ALL BY NAME SELECT id AS b FROM t2) SELECT * FROM u`,
	} {
		output, err := OutputColumnsWithSchema(query, schema, DialectDuckDB)
		if err != nil || !output.OrdinalComplete || len(output.Columns) != 2 {
			t.Errorf("%s: output = %#v, %v", query, output, err)
		}
		lineage, err := LineageWithSchema("b", query, schema, DialectDuckDB)
		if err != nil {
			t.Error(err)
			continue
		}
		found := false
		for _, node := range lineage.Walk() {
			if node.SourceKind == "table" {
				if node.SourceName != "t2" {
					t.Errorf("wrong positional dependency for b: %#v", node)
				}
				found = found || node.SourceName == "t2"
			}
		}
		if !found {
			t.Errorf("missing dependency for b: %#v", lineage)
		}
	}
	if _, err := Lineage("known", `SELECT * FROM unknown_table UNION ALL BY NAME SELECT 1 AS known`, DialectDuckDB); err == nil {
		t.Error("unknown branch must not be represented as known NULL padding")
	}
}
