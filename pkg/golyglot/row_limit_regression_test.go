package golyglot

import (
	"strings"
	"testing"
)

func TestRowLimitScopeAndModifiers(t *testing.T) {
	tests := []struct {
		name, sql, want string
		from, to        Dialect
	}{
		{"left top", "SELECT TOP 5 a FROM t UNION ALL SELECT a FROM u", "(SELECT a FROM t LIMIT 5) UNION ALL SELECT a FROM u", DialectTSQL, DialectDuckDB},
		{"right top", "SELECT a FROM t UNION ALL SELECT TOP 5 a FROM u", "SELECT a FROM t UNION ALL (SELECT a FROM u LIMIT 5)", DialectTSQL, DialectDuckDB},
		{"both top sqlite", "SELECT TOP 5 a FROM t EXCEPT SELECT TOP 2 a FROM u", "SELECT * FROM (SELECT a FROM t LIMIT 5) EXCEPT SELECT * FROM (SELECT a FROM u LIMIT 2)", DialectTSQL, DialectSQLite},
		{"three branches", "SELECT TOP 5 a FROM t UNION ALL SELECT TOP 2 a FROM u UNION ALL SELECT a FROM v", "(SELECT a FROM t LIMIT 5) UNION ALL (SELECT a FROM u LIMIT 2) UNION ALL SELECT a FROM v", DialectTSQL, DialectDuckDB},
		{"compound order stays outside top", "SELECT TOP 5 a FROM t UNION ALL SELECT TOP 2 a FROM u ORDER BY a", "(SELECT a FROM t LIMIT 5) UNION ALL (SELECT a FROM u LIMIT 2) ORDER BY a", DialectTSQL, DialectDuckDB},
		{"parenthesized local order", "(SELECT TOP 1 a FROM t ORDER BY a DESC) UNION ALL SELECT TOP 2 a FROM u ORDER BY a", "(SELECT a FROM t ORDER BY a DESC LIMIT 1) UNION ALL (SELECT a FROM u LIMIT 2) ORDER BY a", DialectTSQL, DialectDuckDB},
		{"cte remains visible to both arms", "WITH c AS (SELECT a FROM t) SELECT TOP 5 a FROM c INTERSECT SELECT a FROM c", "WITH c AS (SELECT a FROM t) (SELECT a FROM c LIMIT 5) INTERSECT SELECT a FROM c", DialectTSQL, DialectDuckDB},
		{"top percent", "SELECT TOP 5 PERCENT a FROM t", "SELECT a FROM t FETCH FIRST 5 PERCENT ROWS ONLY", DialectTSQL, DialectOracle},
		{"top ties", "SELECT TOP (5) WITH TIES a FROM t ORDER BY a", "SELECT a FROM t ORDER BY a FETCH FIRST 5 ROWS WITH TIES", DialectTSQL, DialectOracle},
		{"compound percent", "SELECT a FROM t UNION ALL SELECT a FROM u ORDER BY a LIMIT 12.5 PERCENT", "SELECT a FROM t UNION ALL SELECT a FROM u ORDER BY a FETCH FIRST 12.5 PERCENT ROWS ONLY", DialectDuckDB, DialectOracle},
		{"percent symbol", "SELECT a FROM t LIMIT 10%", "SELECT a FROM t FETCH FIRST 10 PERCENT ROWS ONLY", DialectDuckDB, DialectOracle},
		{"fetch percent to duckdb", "SELECT a FROM t UNION ALL SELECT a FROM u FETCH FIRST 10 PERCENT ROWS ONLY", "SELECT a FROM t UNION ALL SELECT a FROM u LIMIT 10 PERCENT", DialectOracle, DialectDuckDB},
		{"fetch ties postgres", "SELECT a FROM t UNION ALL SELECT a FROM u ORDER BY a FETCH NEXT 5 ROWS WITH TIES", "SELECT a FROM t UNION ALL SELECT a FROM u ORDER BY a FETCH NEXT 5 ROWS WITH TIES", DialectOracle, DialectPostgreSQL},
		{"omitted fetch count", "SELECT a FROM t UNION ALL SELECT a FROM u FETCH FIRST ROWS ONLY", "SELECT a FROM t UNION ALL SELECT a FROM u LIMIT 1", DialectOracle, DialectDuckDB},
		{"compound fetch to top", "SELECT a FROM t UNION ALL SELECT a FROM u FETCH FIRST 5 PERCENT ROWS ONLY", "SELECT TOP 5 PERCENT * FROM (SELECT a FROM t UNION ALL SELECT a FROM u) AS _l_0", DialectOracle, DialectTSQL},
		{"compound limit to top", "SELECT a FROM t UNION ALL SELECT a FROM u LIMIT 5", "SELECT TOP 5 * FROM (SELECT a FROM t UNION ALL SELECT a FROM u) AS _l_0", DialectPostgreSQL, DialectTSQL},
		{"parenthesized compound offset", "(SELECT a FROM t UNION ALL SELECT a FROM u) OFFSET 2 ROWS FETCH NEXT 5 ROWS ONLY", "(SELECT a FROM t UNION ALL SELECT a FROM u) OFFSET 2 ROWS FETCH NEXT 5 ROWS ONLY", DialectOracle, DialectOracle},
		{"nested compound tail", "SELECT a FROM (SELECT a FROM t UNION ALL SELECT a FROM u ORDER BY a FETCH FIRST 5 ROWS ONLY) q", "SELECT a FROM (SELECT a FROM t UNION ALL SELECT a FROM u ORDER BY a LIMIT 5) AS q", DialectOracle, DialectDuckDB},
		{"clickhouse ties", "SELECT a FROM t ORDER BY a FETCH FIRST 5 ROWS WITH TIES", "SELECT a FROM t ORDER BY a FETCH FIRST 5 ROWS WITH TIES", DialectOracle, DialectClickHouse},
		{"native top modifiers", "SELECT TOP (5) PERCENT WITH TIES a FROM t ORDER BY a DESC", "SELECT TOP (5) PERCENT WITH TIES a FROM t ORDER BY a DESC", DialectTSQL, DialectTSQL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TranspileOne(tt.sql, tt.from, tt.to)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestRowLimitASTAndBuilder(t *testing.T) {
	got, err := BuildSQL(Select(Column("a")).From(Table("t")).Limit(5).Offset(2), DialectOracle)
	if err != nil || got != "SELECT a FROM t OFFSET 2 ROWS FETCH FIRST 5 ROWS ONLY" {
		t.Fatalf("builder: %s (%v)", got, err)
	}
	for _, tt := range []struct {
		sql     string
		dialect Dialect
	}{
		{"SELECT TOP (5) PERCENT WITH TIES a FROM t ORDER BY a", DialectTSQL},
		{"SELECT a FROM t LIMIT 5%", DialectDuckDB},
		{"SELECT a FROM t UNION ALL SELECT a FROM u FETCH NEXT 5 PERCENT ROWS WITH TIES", DialectOracle},
	} {
		parsed, err := ParseStrict(tt.sql, tt.dialect)
		if err != nil {
			t.Fatal(err)
		}
		node := parsed.Statements[0].Node
		visited := 0
		Walk(node, func(n Node) VisitAction {
			if lit, ok := n.(*LiteralExpr); ok && lit.Raw == "5" {
				visited++
				span := lit.SourceSpan()
				if tt.sql[span.Start:span.End] != "5" {
					t.Fatalf("count span: %v", span)
				}
			}
			return VisitChildren
		})
		if visited != 1 {
			t.Fatalf("count visited %d times for %s", visited, tt.sql)
		}
		original, err := GenerateWithOptions(node, GenerateOptions{Canonical: true, Dialect: tt.dialect})
		if err != nil {
			t.Fatal(err)
		}
		for _, pretty := range []bool{false, true} {
			text, err := GenerateWithOptions(node, GenerateOptions{Canonical: true, Pretty: pretty, Dialect: DialectOracle})
			if err != nil || !strings.Contains(text, "FETCH") || !strings.Contains(text, "PERCENT") {
				t.Fatalf("generation: %s (%v)", text, err)
			}
		}
		after, _ := GenerateWithOptions(node, GenerateOptions{Canonical: true, Dialect: tt.dialect})
		if after != original {
			t.Fatalf("generator mutated input: %s -> %s", original, after)
		}
		node = Transform(node, func(n Node) Node {
			if lit, ok := n.(*LiteralExpr); ok && lit.Raw == "5" {
				copy := *lit
				copy.Raw = "7"
				return &copy
			}
			return n
		})
		text, err := GenerateWithOptions(node, GenerateOptions{Canonical: true, Dialect: DialectOracle})
		if err != nil || !strings.Contains(text, "7 PERCENT") {
			t.Fatalf("transformed count: %s (%v)", text, err)
		}
	}
}

func TestRowLimitRejectsDuplicates(t *testing.T) {
	for _, sql := range []string{
		"SELECT a FROM t LIMIT 1 LIMIT 2",
		"SELECT a FROM t LIMIT 1 FETCH FIRST 2 ROWS ONLY",
		"SELECT a FROM t FETCH FIRST 1 ROWS ONLY FETCH FIRST 2 ROWS ONLY",
		"SELECT a FROM t UNION SELECT a FROM u LIMIT 1 LIMIT 2",
		"(SELECT a FROM t UNION SELECT a FROM u) LIMIT 1 FETCH FIRST 2 ROWS ONLY",
	} {
		if _, err := ParseStrict(sql, DialectDuckDB); err == nil {
			t.Errorf("accepted conflicting row limits: %s", sql)
		}
	}
}

func TestRowLimitUnsupportedModifiers(t *testing.T) {
	tests := []struct {
		sql, reason string
		from, to    Dialect
	}{
		{"SELECT a FROM t ORDER BY a FETCH FIRST 5 ROWS WITH TIES", "WITH TIES", DialectOracle, DialectDuckDB},
		{"SELECT a FROM t LIMIT 10 PERCENT", "PERCENT", DialectDuckDB, DialectPostgreSQL},
		{"SELECT TOP 5 PERCENT a FROM t", "PERCENT", DialectTSQL, DialectSQLite},
		{"SELECT a FROM t OFFSET 2 ROWS FETCH FIRST 5 PERCENT ROWS ONLY", "PERCENT", DialectOracle, DialectTSQL},
	}
	for _, tt := range tests {
		t.Run(string(tt.from)+" to "+string(tt.to)+" "+tt.reason, func(t *testing.T) {
			_, err := TranspileOne(tt.sql, tt.from, tt.to)
			if err == nil || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("expected explicit %s error, got %v", tt.reason, err)
			}
		})
	}
}

func TestOracleDirectRowLimitGeneration(t *testing.T) {
	tests := []struct {
		sql, want string
		pretty    bool
	}{
		{"SELECT a FROM t LIMIT 5 OFFSET 2", "SELECT a FROM t OFFSET 2 ROWS FETCH FIRST 5 ROWS ONLY", false},
		{"SELECT a FROM t LIMIT 5 OFFSET 2", "SELECT\n  a\nFROM t\nOFFSET 2 ROWS\nFETCH FIRST 5 ROWS ONLY", true},
		{"SELECT a FROM t LIMIT ALL", "SELECT a FROM t", false},
		{"SELECT a FROM t LIMIT NULL", "SELECT a FROM t", false},
		{`SELECT a FROM t LIMIT "ALL"`, `SELECT a FROM t FETCH FIRST "ALL" ROWS ONLY`, false},
		{"SELECT a FROM t UNION ALL SELECT a FROM u LIMIT 5 OFFSET 2", "SELECT a FROM t UNION ALL SELECT a FROM u OFFSET 2 ROWS FETCH FIRST 5 ROWS ONLY", false},
		{"WITH c AS (SELECT a FROM t LIMIT 5) SELECT a FROM c LIMIT 2", "WITH c AS (SELECT a FROM t FETCH FIRST 5 ROWS ONLY) SELECT a FROM c FETCH FIRST 2 ROWS ONLY", false},
		{"INSERT INTO u SELECT a FROM t LIMIT 5", "INSERT INTO u SELECT a FROM t FETCH FIRST 5 ROWS ONLY", false},
		{"SELECT (SELECT a FROM t LIMIT 1) AS a", "SELECT (SELECT a FROM t FETCH FIRST 1 ROWS ONLY) AS a", false},
	}
	for _, tt := range tests {
		t.Run(tt.sql+" pretty="+map[bool]string{false: "false", true: "true"}[tt.pretty], func(t *testing.T) {
			parsed, err := ParseStrict(tt.sql, DialectGeneric)
			if err != nil {
				t.Fatal(err)
			}
			got, err := GenerateWithOptions(parsed.Statements[0].Node, GenerateOptions{Dialect: DialectOracle, Canonical: true, Pretty: tt.pretty})
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestDuckDBOrderByAllLowering(t *testing.T) {
	tests := []struct{ sql, want string }{
		{"SELECT a, b FROM t ORDER BY ALL", "SELECT a, b FROM t ORDER BY 1, 2"},
		{"SELECT a, b FROM t ORDER BY ALL DESC", "SELECT a, b FROM t ORDER BY 1 DESC NULLS LAST, 2 DESC NULLS LAST"},
		{"SELECT a, b FROM t ORDER BY ALL NULLS FIRST", "SELECT a, b FROM t ORDER BY 1 NULLS FIRST, 2 NULLS FIRST"},
		{`SELECT "all" FROM t ORDER BY "all"`, `SELECT "all" FROM t ORDER BY "all"`},
		{"SELECT a, b FROM t UNION ALL SELECT c, d FROM u ORDER BY ALL", "SELECT a, b FROM t UNION ALL SELECT c, d FROM u ORDER BY 1, 2"},
	}
	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			got, err := TranspileOne(tt.sql, DialectDuckDB, DialectPostgreSQL)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("got %s\nwant %s", got, tt.want)
			}
		})
	}
	if _, err := TranspileOne("SELECT * FROM t ORDER BY ALL", DialectDuckDB, DialectPostgreSQL); err == nil {
		t.Fatal("star expansion requires a known projection width")
	}
}

func TestDuckDBIntegerDivisionLowering(t *testing.T) {
	for _, tt := range []struct{ sql, want string }{
		{"SELECT 7 // 2", "SELECT 7 / NULLIF(2, 0)"},
		{"SELECT -7 // 2", "SELECT -7 / NULLIF(2, 0)"},
		{"SELECT 7 // 0", "SELECT 7 / NULLIF(0, 0)"},
	} {
		got, err := TranspileOne(tt.sql, DialectDuckDB, DialectPostgreSQL)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.sql, got, tt.want)
		}
	}
	if _, err := TranspileOne("SELECT a // b FROM t", DialectDuckDB, DialectPostgreSQL); err == nil {
		t.Fatal("untyped division must not guess integer vs floating-point semantics")
	}
	for _, sql := range []string{"SELECT 7.0 // 2", "SELECT CAST(7 AS DOUBLE) // 2", "SELECT CAST(7 AS DECIMAL(8, 2)) // 2", "SELECT 9223372036854775808 // 2", "SELECT CAST(7 AS UBIGINT) // 2", "SELECT CAST(7 AS HUGEINT) // 2", `SELECT CAST(7 AS "INTEGER") // 2`} {
		if _, err := TranspileOne(sql, DialectDuckDB, DialectPostgreSQL); err == nil {
			t.Errorf("accepted unverified division: %s", sql)
		}
	}
	if _, err := TranspileOne("x // y", DialectDuckDB, DialectHive); err == nil {
		t.Fatal("DuckDB division cannot blindly become Hive DIV")
	}
}

func TestOracleRowLimitFormatterAndCompoundBuilder(t *testing.T) {
	for _, sql := range []string{
		"SELECT a FROM t LIMIT 5 OFFSET 2",
		"SELECT a FROM t UNION ALL SELECT a FROM u FETCH FIRST 5 ROWS WITH TIES",
		"WITH c AS (SELECT a FROM t LIMIT 5) SELECT a FROM c LIMIT 2",
	} {
		formatted, err := FormatOne(sql, DialectOracle)
		if err != nil || strings.Contains(formatted, "LIMIT") || !strings.Contains(formatted, "FETCH") {
			t.Fatalf("FormatOne(%s) = %s (%v)", sql, formatted, err)
		}
		if _, err := ParseStrict(formatted, DialectOracle); err != nil {
			t.Fatal(err)
		}
	}
	query := Select(Column("a")).From(Table("t")).UnionAll(Select(Column("a")).From(Table("u"))).Limit(5)
	for _, pretty := range []bool{false, true} {
		ast, err := query.AST()
		if err != nil {
			t.Fatal(err)
		}
		generated, err := GenerateWithOptions(ast, GenerateOptions{Canonical: true, Dialect: DialectOracle, Pretty: pretty})
		if err != nil || !strings.HasSuffix(generated, "FETCH FIRST 5 ROWS ONLY") {
			t.Fatalf("compound builder: %s (%v)", generated, err)
		}
	}
}

func TestRowLimitSemanticDiffModifiers(t *testing.T) {
	for _, tt := range []struct {
		before, after string
		dialect       Dialect
	}{
		{"SELECT 1 LIMIT 5", "SELECT 1 LIMIT 5 PERCENT", DialectDuckDB},
		{"SELECT TOP 5 1", "SELECT TOP 5 PERCENT 1", DialectTSQL},
		{"SELECT TOP 5 1 ORDER BY 1", "SELECT TOP 5 WITH TIES 1 ORDER BY 1", DialectTSQL},
	} {
		diff, err := DiffQuerySemantics(tt.before, tt.after, QuerySemanticDiffOptions{Dialect: tt.dialect})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, change := range diff.BehaviorChanges {
			found = found || change.Kind == QueryBehaviorLimit
		}
		if !found {
			t.Errorf("no row-limit behavior change for %s -> %s", tt.before, tt.after)
		}
	}
}

func TestRowLimitPrettyKeepsOperandBoundaries(t *testing.T) {
	for _, target := range []Dialect{DialectDuckDB, DialectPostgreSQL, DialectSQLite} {
		for _, sql := range []string{
			"SELECT 1 AS a UNION ALL (SELECT TOP 0 2 AS a)",
			"SELECT 1 AS a UNION ALL (SELECT 2 AS a OFFSET 0 ROWS FETCH FIRST 0 ROWS ONLY)",
		} {
			queries, err := TranspileWithOptions(sql, DialectTSQL, target, TranspileOptions{Pretty: true})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(strings.TrimSpace(queries[0]), ")") {
				t.Errorf("%s lost a branch boundary: %s", target, queries[0])
			}
		}
	}
}
