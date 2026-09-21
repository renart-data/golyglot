package golyglot

import "testing"

func FuzzTolerantParseNeverPanics(f *testing.F) {
	for _, seed := range []string{
		"",
		"SELECT",
		"SELECT * FROM users WHERE",
		"SELECT ((1 + 2)",
		"SELECT 'unfinished",
		"WITH x AS (SELECT 1) SELECT * FROM x",
		"a.:S1(",
		"SELECT CAST(x AS UserDefinedType(",
		"SELECT JSON_VALUE(a, '$.b' RETURNING VARCHAR2(",
		"IF~IF~IF~IF~I?{",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		result := ParseTolerant(sql, DialectGeneric)
		if len(result.SQL) != len(sql) {
			t.Fatalf("result SQL changed during parse")
		}
	})
}

func FuzzSyntacticContextNeverPanics(f *testing.F) {
	for _, seed := range []string{
		"",
		"SEL",
		"SELECT account FR",
		"SELECT * FROM users WHERE",
		"UPDATE accounts SET",
		"WITH recent AS (SELECT 1) SELECT * FROM recent",
	} {
		f.Add(seed, len(seed))
	}
	f.Fuzz(func(t *testing.T, sql string, cursor int) {
		if cursor < 0 {
			cursor = -cursor
			if cursor < 0 {
				cursor = 0
			}
		}
		cursor %= len(sql) + 1
		_, _ = SyntacticContextAt(sql, cursor, DialectGeneric)
	})
}

func FuzzSchemaScopeAnalysisNeverPanics(f *testing.F) {
	for _, sql := range []string{
		"SELECT o.id FROM t1 o WHERE EXISTS (SELECT 1 FROM t2 i WHERE i.id = o.id)",
		"WITH a AS (SELECT value AS amount FROM t1), b AS (SELECT * FROM a) SELECT id FROM b WHERE amount > 0",
		"WITH RECURSIVE a(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM a WHERE n < 3) SELECT n FROM a",
		"SELECT id FROM t1 JOIN t2 USING (id)",
		"SELECT id FROM t2 WHERE id IN (SELECT value FROM t1)",
		"SELECT list_transform([1,2], x -> x + 1) AS values",
		"SELECT value + 1 AS adjusted, adjusted * 2 FROM t1",
		"WITH u AS (SELECT id FROM t1 UNION ALL BY NAME SELECT value FROM t2) SELECT * FROM u",
	} {
		f.Add(sql)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		if len(sql) > 4096 {
			t.Skip()
		}
		schema := upstreamScopeSchema()
		ValidateWithSchema(sql, schema, DialectDuckDB)
		_, _ = AnalyzeQuery(sql, AnalyzeQueryOptions{Dialect: DialectDuckDB, Schema: &schema})
	})
}

func FuzzSnowflakeLambdaNeverPanics(f *testing.F) {
	for _, sql := range []string{
		"SELECT TRANSFORM(values, x INT -> x + quantity) FROM items",
		"SELECT TRANSFORM(values, x INT ->)",
		"SELECT TRANSFORM(values, x NUMBER(10,2) -> TRANSFORM(values, x -> x + 1))",
		`SELECT TRANSFORM(values, "Value" INT -> "Value" + 1)`,
	} {
		f.Add(sql)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		if len(sql) > 2048 {
			t.Skip()
		}
		ParseTolerant(sql, DialectSnowflake)
		Validate(sql, DialectSnowflake)
		_, _ = AnalyzeQuery(sql, AnalyzeQueryOptions{Dialect: DialectSnowflake})
		_, _ = TranspileOne(sql, DialectSnowflake, DialectSnowflake)
	})
}
