package golyglot

import "testing"

func TestSemanticGroupingAggregateWindowRules(t *testing.T) {
	schema := upstreamScopeSchema()
	for _, tc := range []struct{ sql, code string }{
		{`SELECT id, SUM(value) FROM t1`, "E230"},
		{`SELECT value FROM t1 GROUP BY id`, "E230"},
		{`SELECT id FROM t1 WHERE SUM(value) > 0`, "E231"},
		{`SELECT SUM(MAX(value)) FROM t1`, "E231"},
		{`SELECT SUM(value) FROM t1 GROUP BY 1`, "E231"},
		{`SELECT id FROM t1 WHERE ROW_NUMBER() OVER (ORDER BY id) = 1`, "E232"},
		{`SELECT SUM(ROW_NUMBER() OVER (ORDER BY id)) FROM t1`, "E232"},
		{`SELECT ROW_NUMBER() OVER (ORDER BY ROW_NUMBER() OVER ()) FROM t1`, "E232"},
		{`SELECT SUM(value) OVER (PARTITION BY id) FROM t1 GROUP BY value`, "E230"},
		{`SELECT SUM(value) FROM t1 LIMIT SUM(id)`, "E231"},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			v := ValidateWithOptions(tc.sql, ValidationOptions{Dialect: DialectDuckDB, Semantic: true, Schema: &schema})
			if v.Valid || !hasValidationCode(v.Errors, tc.code) {
				t.Fatalf("want %s: %#v", tc.code, v.Errors)
			}
			for _, e := range v.Errors {
				if e.Code == tc.code && (e.Span.End <= e.Span.Start || e.Span.End > len(tc.sql)) {
					t.Errorf("invalid diagnostic span: %#v", e)
				}
			}
			if syntax := ValidateWithOptions(tc.sql, ValidationOptions{Dialect: DialectDuckDB}); !syntax.Valid {
				t.Errorf("semantic rule leaked into syntax-only validation: %#v", syntax.Errors)
			}
		})
	}
}

func TestGroupAliasDialectVisibility(t *testing.T) {
	for _, dialect := range []Dialect{DialectTrino, DialectPresto, DialectAthena, DialectTSQL} {
		v := ValidateWithSchema(`SELECT id + 1 AS adjusted, COUNT(*) FROM t1 GROUP BY adjusted`, upstreamScopeSchema(), dialect)
		if v.Valid {
			t.Errorf("%s allowed an unavailable GROUP BY alias", dialect)
		}
	}
}

func TestCompoundOrderByUsesCombinedOutputNamespace(t *testing.T) {
	schema := upstreamScopeSchema()
	strict := true
	schema.Strict = &strict
	for _, dialect := range []Dialect{DialectDuckDB, DialectPostgreSQL} {
		for _, sql := range []string{
			`SELECT 1 AS step_order, COUNT(*) AS n FROM t1 UNION ALL SELECT 2, COUNT(*) FROM t1 ORDER BY step_order`,
			`SELECT id AS key, COUNT(*) AS n FROM t1 GROUP BY id UNION ALL SELECT value, COUNT(*) FROM t1 GROUP BY value ORDER BY key`,
			`SELECT 1 AS step_order, COUNT(*) AS n FROM t1 UNION ALL SELECT 2, COUNT(*) FROM t1 UNION ALL SELECT 3, COUNT(*) FROM t1 ORDER BY step_order`,
			`SELECT 1 AS step_order, COUNT(*) AS n FROM t1 UNION ALL (SELECT 2, COUNT(*) FROM t1) ORDER BY step_order`,
			`SELECT id AS key FROM t1 UNION ALL (SELECT value FROM t1 ORDER BY value) ORDER BY key`,
		} {
			t.Run(string(dialect)+"/"+sql, func(t *testing.T) {
				v := ValidateWithSchema(sql, schema, dialect)
				if !v.Valid || len(v.Errors) != 0 {
					t.Fatalf("valid compound ordering rejected: %#v", v.Errors)
				}
			})
		}
	}
	for _, tc := range []struct{ sql, code string }{
		{`SELECT 1 AS step_order, COUNT(*) FROM t1 UNION ALL SELECT id, COUNT(*) FROM t1 ORDER BY step_order`, "E230"},
		{`SELECT 1 AS step_order, COUNT(*) FROM t1 UNION ALL (SELECT 2, COUNT(*) FROM t1 ORDER BY id)`, "E230"},
		{`SELECT id AS key FROM t1 UNION ALL SELECT value FROM t1 ORDER BY missing`, "SCHEMA_UNKNOWN_COLUMN"},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			v := ValidateWithSchema(tc.sql, schema, DialectDuckDB)
			if v.Valid || !hasValidationCode(v.Errors, tc.code) {
				t.Fatalf("want %s: %#v", tc.code, v.Errors)
			}
		})
	}
}

func TestCompoundOrderByRetainsBothBranchesInLineage(t *testing.T) {
	schema := ValidationSchema{Tables: []SchemaTable{
		{Name: "first_table", Columns: []SchemaColumn{{Name: "amount", Type: "INTEGER"}}},
		{Name: "second_table", Columns: []SchemaColumn{{Name: "cost", Type: "INTEGER"}}},
	}}
	analysis, err := AnalyzeQuery(`SELECT SUM(amount) AS total FROM first_table UNION ALL SELECT SUM(cost) FROM second_table ORDER BY total`, AnalyzeQueryOptions{Dialect: DialectDuckDB, Schema: &schema})
	if err != nil {
		t.Fatal(err)
	}
	for _, use := range analysis.ColumnUses {
		if use.Context != "order" {
			continue
		}
		if !use.Complete || len(use.Upstream) != 2 {
			t.Fatalf("compound ORDER BY lineage = %#v", use)
		}
		return
	}
	t.Fatal("missing compound ORDER BY column use")
}

func TestSemanticRulesAcceptValidScopes(t *testing.T) {
	schema := upstreamScopeSchema()
	for _, sql := range []string{
		`SELECT id, SUM(value) FROM t1 GROUP BY id`,
		`SELECT id + 1, SUM(value) FROM t1 GROUP BY id`,
		`SELECT id + 1 AS k, COUNT(*) FROM t1 GROUP BY k`,
		`SELECT id % 2, COUNT(*) FROM t1 GROUP BY id % 2`,
		`SELECT ROW_NUMBER() OVER (ORDER BY id) FROM t1`,
		`SELECT SUM(SUM(value)) OVER () FROM t1 GROUP BY id`,
		`SELECT (SELECT MAX(value) FROM t1) FROM t2`,
		`SELECT SUM(value) FROM t1 HAVING SUM(value) > 1`,
		`SELECT id, COUNT(*) FROM t1 GROUP BY ROLLUP(id)`,
		`SELECT id, value, COUNT(*) FROM t1 GROUP BY GROUPING SETS ((id), (value))`,
		`SELECT COUNT(*), SUM(value) FROM t1`,
		`SELECT COUNT(*) FROM t1 ORDER BY 1`,
		`SELECT value, COUNT(*) FROM t1 GROUP BY 1`,
		`SELECT id, SUM(value) FROM t1 GROUP BY ALL`,
		`SELECT id, COUNT(*) AS n FROM t1 GROUP BY id ORDER BY n`,
		`SELECT id, list_transform([1,2], x -> x + id), COUNT(*) FROM t1 GROUP BY id`,
	} {
		t.Run(sql, func(t *testing.T) {
			if v := ValidateWithSchema(sql, schema, DialectDuckDB); !v.Valid {
				t.Fatalf("valid query rejected: %#v", v.Errors)
			}
		})
	}
	schema.Tables[0].PrimaryKey = []string{"id"}
	if v := ValidateWithSchema(`SELECT id, value, COUNT(*) FROM t1 GROUP BY id`, schema, DialectPostgreSQL); !v.Valid {
		t.Fatalf("primary-key functional dependency rejected: %#v", v.Errors)
	}
	if v := ValidateWithSchema(`SELECT id, MAX(value) FROM t1`, schema, DialectSQLite); !v.Valid {
		t.Fatalf("SQLite bare columns rejected: %#v", v.Errors)
	}
}
