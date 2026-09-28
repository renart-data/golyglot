package golyglot

import (
	"strings"
	"testing"
)

func TestHANAVerticaDialectRegistration(t *testing.T) {
	for _, tc := range []struct{ name, canonical string }{
		{"hana", "hana"}, {"HANA", "hana"}, {"sap_hana", "hana"},
		{"sap-hana", "hana"}, {"saphana", "hana"}, {"vertica", "vertica"},
		{" Vertica ", "vertica"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialect, err := ParseDialect(tc.name)
			if err != nil || string(dialect) != tc.canonical {
				t.Fatalf("ParseDialect(%q) = %q, %v; want %q", tc.name, dialect, err, tc.canonical)
			}
			found := false
			for _, supported := range Dialects() {
				found = found || supported == dialect
			}
			if !found {
				t.Fatalf("%s missing from public dialect inventory", dialect)
			}
			if _, err := ParseStrict(`SELECT "price", COUNT(*) FROM "sales" GROUP BY "price"`, dialect); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHANAVerticaIdentifierSemantics(t *testing.T) {
	for _, tc := range []struct {
		dialect Dialect
		name    Identifier
		want    string
	}{
		{DialectHANA, Identifier{Text: "price"}, "PRICE"},
		{DialectHANA, Identifier{Text: "price", Quoted: true}, "price"},
		{DialectVertica, Identifier{Text: "Price", Quoted: true}, "price"},
	} {
		if got := identifierKey(tc.name, tc.dialect); got != tc.want {
			t.Errorf("%s identifier key = %q, want %q", tc.dialect, got, tc.want)
		}
	}
}

func TestHANAVerticaLogicalTypes(t *testing.T) {
	for _, tc := range []struct {
		dialect Dialect
		sql     string
		kind    DataTypeKind
	}{
		{DialectVertica, "INT", DataTypeBigInt},
		{DialectVertica, "SMALLINT", DataTypeBigInt},
		{DialectVertica, "REAL", DataTypeDouble},
		{DialectVertica, "LONG VARBINARY(200)", DataTypeBinary},
		{DialectVertica, "ARRAY[INT]", DataTypeArray},
		{DialectHANA, "SECONDDATE", DataTypeTimestamp},
		{DialectHANA, "ALPHANUM(20)", DataTypeString},
		{DialectHANA, "NCLOB", DataTypeString},
	} {
		t.Run(string(tc.dialect)+"/"+tc.sql, func(t *testing.T) {
			got, err := ParseDataType(tc.sql, tc.dialect)
			if err != nil || got.Kind != tc.kind {
				t.Fatalf("got %#v (%v), want %s", got, err, tc.kind)
			}
		})
	}
}

func TestHANAVerticaUnsupportedConversions(t *testing.T) {
	for _, tc := range []struct {
		sql      string
		from, to Dialect
	}{
		{`SELECT TRY_CAST('bad' AS INT)`, DialectDuckDB, DialectVertica},
		{`SELECT GETDATE()`, DialectVertica, DialectDuckDB},
		{`SELECT LISTAGG(city) FROM t`, DialectVertica, DialectDuckDB},
		{`SELECT STRING_AGG(city, ',') FROM t`, DialectPostgreSQL, DialectVertica},
		{`SELECT TIME_SLICE(ts, 5, 'MINUTE') FROM t`, DialectVertica, DialectDuckDB},
		{`SELECT a FROM t ORDER BY a`, DialectVertica, DialectPostgreSQL},
		{`SELECT LOCATE(s, 'x', 1, 2) FROM t`, DialectHANA, DialectDuckDB},
		{`SELECT TO_VARCHAR(d, 'YYYY-MM-DD') FROM t`, DialectHANA, DialectPostgreSQL},
		{`CALL my_proc(1)`, DialectHANA, DialectDuckDB},
		{`SELECT TO_CHAR(d, 'YYYY-MM-DD') FROM t`, DialectPostgreSQL, DialectHANA},
		{`SELECT CURRENT_DATE`, DialectPostgreSQL, DialectHANA},
		{`SELECT CAST(x AS DOUBLE) FROM t`, DialectDuckDB, DialectHANA},
		{`CREATE TABLE t (x SMALLDECIMAL)`, DialectHANA, DialectDuckDB},
		{`CREATE TABLE t (x INT) SEGMENTED BY HASH(x) ALL NODES`, DialectVertica, DialectDuckDB},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			if got, err := TranspileOne(tc.sql, tc.from, tc.to); err == nil {
				t.Fatalf("unsafe conversion unexpectedly succeeded: %s", got)
			}
		})
	}
}

func TestHANADummyScopeAndCardinality(t *testing.T) {
	for _, sql := range []string{
		`SELECT * FROM DUMMY`,
		`SELECT d.DUMMY FROM SYS.DUMMY AS d WHERE d.DUMMY = 'X'`,
		`SELECT COUNT(*) FROM DUMMY a CROSS JOIN DUMMY b`,
		`WITH DUMMY AS (SELECT * FROM DUMMY) SELECT * FROM DUMMY`,
		`SELECT (SELECT COUNT(*) FROM DUMMY) FROM t`,
	} {
		got, err := TranspileOne(sql, DialectHANA, DialectDuckDB)
		if err != nil || !strings.Contains(got, "SELECT 'X' AS DUMMY") {
			t.Errorf("DUMMY must remain a one-row, one-column relation: %s (%v)", got, err)
		}
	}
	for _, sql := range []string{
		`WITH DUMMY AS (SELECT 5 AS n) SELECT * FROM DUMMY`,
		`WITH DUMMY AS (SELECT 5 AS n), c AS (SELECT * FROM DUMMY) SELECT * FROM c`,
		`SELECT * FROM user_schema.DUMMY`,
		`SELECT * FROM "dummy"`,
	} {
		got, err := TranspileOne(sql, DialectHANA, DialectDuckDB)
		if err != nil || strings.Contains(got, "SELECT 'X'") {
			t.Errorf("shadowed/quoted/user table must not become SYS.DUMMY: %s (%v)", got, err)
		}
	}
}

func TestHANAVerticaOrderingAndBoundaries(t *testing.T) {
	for _, tc := range []struct {
		sql      string
		from, to Dialect
		contains string
	}{
		{`SELECT CAST(a AS INT) AS n FROM t ORDER BY n`, DialectVertica, DialectDuckDB, "ORDER BY n NULLS FIRST"},
		{`SELECT CAST(a AS FLOAT) AS n FROM t ORDER BY 1 DESC`, DialectVertica, DialectDuckDB, "ORDER BY 1 DESC NULLS FIRST"},
		{`SELECT CAST(a AS DECIMAL(10, 2)) AS n FROM t ORDER BY n`, DialectVertica, DialectDuckDB, "ORDER BY n NULLS FIRST"},
		{`SELECT ROW_NUMBER() OVER (ORDER BY a DESC) FROM t`, DialectVertica, DialectDuckDB, "ORDER BY a DESC NULLS FIRST"},
		{`SELECT COUNTIF(a > 0) FROM t`, DialectBigQuery, DialectVertica, "COUNT(CASE WHEN a > 0 THEN 1 END)"},
		{`SELECT CAST(2147483648 AS INT)`, DialectVertica, DialectDuckDB, "AS BIGINT"},
		{`SELECT NULLIFZERO(a) FROM t`, DialectVertica, DialectDuckDB, "NULLIF(a, 0)"},
		{`SELECT LISTAGG(city USING PARAMETERS separator='a=b', max_length=1024) FROM t`, DialectVertica, DialectVertica, "separator = 'a=b', max_length = 1024"},
		{`ALTER TABLE "INTEGER" ADD ("INTEGER" INTEGER DEFAULT 1)`, DialectHANA, DialectHANA, `("INTEGER" INT DEFAULT 1)`},
		{`SELECT CAST(x AS "Money") FROM t`, DialectHANA, DialectHANA, `AS "Money"`},
		{`CREATE TABLE t (x "Money")`, DialectHANA, DialectHANA, `x "Money"`},
		{`SELECT current_price FROM t`, DialectHANA, DialectDuckDB, "current_price"},
		{`SELECT a FROM t ORDER BY a`, DialectHANA, DialectDuckDB, "ORDER BY a NULLS FIRST"},
		{`SELECT a FROM t ORDER BY a`, DialectPostgreSQL, DialectHANA, "ORDER BY a NULLS LAST"},
		{`SELECT ROW_NUMBER() OVER (ORDER BY a) FROM t`, DialectHANA, DialectDuckDB, "ORDER BY a NULLS FIRST"},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			got, err := TranspileOne(tc.sql, tc.from, tc.to)
			if err != nil || !strings.Contains(got, tc.contains) {
				t.Fatalf("got %s (%v), want %s", got, err, tc.contains)
			}
		})
	}
	for _, tc := range []struct {
		sql      string
		from, to Dialect
	}{
		{`SELECT random() AS n FROM t ORDER BY n`, DialectPostgreSQL, DialectVertica},
		{`SELECT a FROM t ORDER BY 1`, DialectPostgreSQL, DialectVertica},
		{`SELECT a FROM t ORDER BY random()`, DialectPostgreSQL, DialectVertica},
		{`SELECT DISTINCT a FROM t ORDER BY a`, DialectPostgreSQL, DialectVertica},
		{`SELECT DISTINCT ON (a) a FROM t ORDER BY a`, DialectPostgreSQL, DialectVertica},
		{`SELECT CAST(a AS INT) AS n FROM t UNION ALL SELECT CAST(b AS FLOAT) AS n FROM u ORDER BY 1`, DialectVertica, DialectDuckDB},
		{`SELECT DATEDIFF(WEEK, a, b) FROM t`, DialectVertica, DialectDuckDB},
		{`SELECT TIMESTAMPADD(unit, 3, ts) FROM t`, DialectVertica, DialectDuckDB},
		{`SELECT CAST(x AS SMALLDECIMAL) FROM t`, DialectHANA, DialectDuckDB},
		{`SELECT STRING_AGG(x, ',') FROM t`, DialectPostgreSQL, DialectHANA},
		{`SELECT SYS.DUMMY.DUMMY FROM SYS.DUMMY`, DialectHANA, DialectDuckDB},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			if got, err := TranspileOne(tc.sql, tc.from, tc.to); err == nil {
				t.Fatalf("unsafe conversion unexpectedly succeeded: %s", got)
			}
		})
	}
}

func TestHANAVerticaAnalysis(t *testing.T) {
	for _, tc := range []struct {
		sql     string
		dialect Dialect
		types   []string
	}{
		{`SELECT CAST(x AS INT) AS n, CAST(x AS REAL) AS f FROM t`, DialectVertica, []string{"BIGINT", "DOUBLE"}},
		{`SELECT 1 AS n, LENGTH('abc') AS length`, DialectVertica, []string{"BIGINT", "BIGINT"}},
		{`SELECT TIMESTAMPADD(DAY, 1, ts) AS next_ts, DATEDIFF(DAY, ts, ts) AS days FROM t`, DialectVertica, []string{"TIMESTAMP", "BIGINT"}},
		{`SELECT CURRENT_UTCTIMESTAMP AS ts, CURRENT_UTCDATE AS d`, DialectHANA, []string{"TIMESTAMP", "DATE"}},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			strict := true
			schema := ValidationSchema{Strict: &strict, Tables: []SchemaTable{{Name: "t", Columns: []SchemaColumn{{Name: "x", Type: "INT"}, {Name: "ts", Type: "TIMESTAMP"}}}}}
			analysis, err := AnalyzeQuery(tc.sql, AnalyzeQueryOptions{Dialect: tc.dialect, Schema: &schema})
			if err != nil {
				t.Fatal(err)
			}
			if len(analysis.OutputColumns) != len(tc.types) {
				t.Fatalf("unexpected outputs: %#v", analysis.OutputColumns)
			}
			for i, column := range analysis.OutputColumns {
				if column.TypeHint == nil || *column.TypeHint != tc.types[i] {
					t.Errorf("column %s = %#v, want %s", column.Name, column.TypeHint, tc.types[i])
				}
			}
			validation := ValidateWithSchema(tc.sql, schema, tc.dialect)
			if !validation.Valid {
				t.Errorf("native date/time syntax reported as columns: %#v", validation.Errors)
			}
		})
	}
}

func TestHANAVerticaUserFunctions(t *testing.T) {
	for _, tc := range []struct {
		sql      string
		from, to Dialect
	}{
		{`SELECT "ZEROIFNULL"(a) FROM t`, DialectVertica, DialectDuckDB},
		{`SELECT "IFNULL"(a, b) FROM t`, DialectMySQL, DialectVertica},
		{`SELECT custom.LISTAGG(a) FROM t`, DialectVertica, DialectDuckDB},
	} {
		got, err := TranspileOne(tc.sql, tc.from, tc.to)
		if err != nil || got != tc.sql {
			t.Errorf("user function changed: %s (%v), want %s", got, err, tc.sql)
		}
	}
	for _, sql := range []string{`SELECT GETDATE AS n FROM t`, `SELECT "CURRENT_UTCDATE" AS n FROM t`} {
		analysis, err := AnalyzeQuery(sql, AnalyzeQueryOptions{Dialect: DialectVertica})
		if err != nil {
			t.Fatal(err)
		}
		if analysis.OutputTypesComplete {
			t.Errorf("column incorrectly inferred as a clock: %#v", analysis.OutputColumns)
		}
	}
}

func TestHANAVerticaCTENamesAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		sql      string
		dialect  Dialect
		complete bool
	}{
		{`WITH t AS (SELECT 1 AS price) SELECT PRICE FROM t`, DialectHANA, true},
		{`WITH t AS (SELECT 1 AS "price") SELECT "price" FROM t`, DialectHANA, true},
		{`WITH t AS (SELECT 1 AS "price") SELECT PRICE FROM t`, DialectHANA, false},
		{`WITH t AS (SELECT 1 AS "Price") SELECT "PRICE" FROM t`, DialectVertica, true},
	} {
		analysis, err := AnalyzeQuery(tc.sql, AnalyzeQueryOptions{Dialect: tc.dialect})
		if err != nil || analysis.OutputTypesComplete != tc.complete {
			t.Errorf("%s: complete = %t (%v), want %t", tc.sql, analysis.OutputTypesComplete, err, tc.complete)
		}
	}
	for _, tc := range []struct {
		sql     string
		dialect Dialect
	}{
		{`SELECT "pricé", add_days(ts, 1) FROM "orders" WHERE`, DialectHANA},
		{`SELECT !!5, @-x, LISTAGG(city USING PARAMETERS separator='|') FROM t`, DialectVertica},
		{`SELECT CAST(x AS INTERVAL DAY TO SECOND(3)) FROM t`, DialectVertica},
	} {
		for i := 0; i <= len(tc.sql); i++ {
			parsed := ParseTolerant(tc.sql[:i], tc.dialect)
			if parsed.OriginalSQL() != tc.sql[:i] {
				t.Fatalf("lost original SQL at prefix %d", i)
			}
			for _, token := range parsed.Tokens {
				if !token.Span.Valid(i) {
					t.Fatalf("invalid token span %#v at prefix %d", token.Span, i)
				}
			}
		}
	}
	pretty, err := TranspileWithOptions(`SELECT add_days(a, 1), Add_Months(a, 2) FROM t`, DialectHANA, DialectHANA, TranspileOptions{Pretty: true})
	if err != nil || len(pretty) != 1 || !strings.Contains(pretty[0], "add_days(") || !strings.Contains(pretty[0], "Add_Months(") {
		t.Fatalf("HANA pretty formatting changed native functions: %v (%v)", pretty, err)
	}
}
