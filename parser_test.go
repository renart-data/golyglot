package golyglot

import (
	"strings"
	"testing"
)

func TestStrictSelectRoundTrip(t *testing.T) {
	result, err := ParseStrict("SELECT a, b AS value FROM users WHERE id = 1 ORDER BY value DESC LIMIT 10", DialectPostgreSQL)
	if err != nil {
		t.Fatalf("ParseStrict returned error: %v\n%#v", err, result.Diagnostics)
	}
	if len(result.Statements) != 1 {
		t.Fatalf("got %d statements, want 1", len(result.Statements))
	}
	selectStmt, ok := result.Statements[0].Node.(*SelectStmt)
	if !ok {
		t.Fatalf("got %T, want *SelectStmt", result.Statements[0].Node)
	}
	if len(selectStmt.Projections) != 2 || len(selectStmt.From) != 1 || selectStmt.Where == nil || len(selectStmt.OrderBy) != 1 {
		t.Fatalf("unexpected select shape: %#v", selectStmt)
	}
	generated, err := Generate(selectStmt)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	want := "SELECT a, b AS value FROM users WHERE id = 1 ORDER BY value DESC LIMIT 10"
	if generated != want {
		t.Fatalf("generated SQL = %q, want %q", generated, want)
	}
}

func TestCanonicalGenerationCanDropPreservedTrivia(t *testing.T) {
	result, err := ParseStrict("SELECT a /* comment */ FROM users", DialectGeneric)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if generated, err := Generate(result.Statements[0].Node); err != nil || generated != "SELECT a /* comment */ FROM users" {
		t.Fatalf("identity generation = %q, %v", generated, err)
	}
	generated, err := GenerateWithOptions(result.Statements[0].Node, GenerateOptions{Canonical: true})
	if err != nil {
		t.Fatalf("canonical generation error: %v", err)
	}
	if generated != "SELECT a FROM users" {
		t.Fatalf("canonical generation = %q, want %q", generated, "SELECT a FROM users")
	}
}

func TestTolerantIncompleteWhereProducesActionableDiagnostic(t *testing.T) {
	result := ParseTolerant("SELECT * FROM users WHERE", DialectPostgreSQL)
	if len(result.Statements) != 1 {
		t.Fatalf("got %d statements, want one partial statement", len(result.Statements))
	}
	diagnostic := requireDiagnosticCode(t, result.Diagnostics, "PARSE_EXPECTED_EXPRESSION")
	if !strings.Contains(diagnostic.Message, "after WHERE") {
		t.Fatalf("diagnostic = %q, want context about WHERE", diagnostic.Message)
	}
	if diagnostic.Span.Start != len(result.SQL) || diagnostic.Span.End != len(result.SQL) {
		t.Fatalf("diagnostic span = %#v, want zero-width EOF", diagnostic.Span)
	}
	stmt := result.Statements[0].Node.(*SelectStmt)
	if _, ok := stmt.Where.(*MissingExpr); !ok {
		t.Fatalf("WHERE node = %T, want *MissingExpr", stmt.Where)
	}
}

func TestTolerantIncompleteOperators(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		code string
	}{
		{name: "and", sql: "SELECT * FROM users WHERE a AND", code: "PARSE_EXPECTED_EXPRESSION"},
		{name: "between", sql: "SELECT x BETWEEN 1 AND", code: "PARSE_EXPECTED_EXPRESSION"},
		{name: "in", sql: "SELECT x IN", code: "PARSE_EXPECTED_TOKEN"},
		{name: "like", sql: "SELECT x LIKE", code: "PARSE_EXPECTED_EXPRESSION"},
		{name: "order", sql: "SELECT * FROM users ORDER BY", code: "PARSE_EXPECTED_EXPRESSION"},
		{name: "join", sql: "SELECT * FROM users JOIN", code: "PARSE_EXPECTED_TABLE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ParseTolerant(test.sql, DialectGeneric)
			if len(result.Statements) != 1 {
				t.Fatalf("got %d statements, want one", len(result.Statements))
			}
			requireDiagnosticCode(t, result.Diagnostics, test.code)
		})
	}
}

func TestMalformedProjectionDoesNotStopFollowingItems(t *testing.T) {
	result := ParseTolerant("SELECT a,, b FROM users", DialectGeneric)
	requireDiagnosticCode(t, result.Diagnostics, "PARSE_EXPECTED_EXPRESSION")
	stmt := result.Statements[0].Node.(*SelectStmt)
	if len(stmt.Projections) != 2 {
		t.Fatalf("got %d projections, want 2 usable projections", len(stmt.Projections))
	}
	if _, ok := stmt.Projections[1].Expr.(*IdentifierExpr); !ok {
		t.Fatalf("second projection = %T, want identifier", stmt.Projections[1].Expr)
	}
}

func TestTrailingProjectionCommaMatchesPolyglotTolerance(t *testing.T) {
	result := ParseTolerant("SELECT a, b, FROM users", DialectGeneric)
	if result.HasErrors() {
		t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
	}
}

func TestUnclosedStringAndParenthesisDiagnostics(t *testing.T) {
	result := ParseTolerant("SELECT ('unfinished", DialectPostgreSQL)
	requireDiagnosticCode(t, result.Diagnostics, "LEX_UNTERMINATED_STRING")
	requireDiagnosticCode(t, result.Diagnostics, "PARSE_UNCLOSED_PAREN")
}

func TestNestedQueryAndJoin(t *testing.T) {
	result, err := ParseStrict("SELECT u.id FROM (SELECT id FROM users) AS u LEFT JOIN orders o ON u.id = o.user_id", DialectPostgreSQL)
	if err != nil {
		t.Fatalf("ParseStrict returned error: %v\n%#v", err, result.Diagnostics)
	}
	stmt := result.Statements[0].Node.(*SelectStmt)
	if len(stmt.From) != 1 || len(stmt.From[0].Joins) != 1 {
		t.Fatalf("unexpected FROM shape: %#v", stmt.From)
	}
	if _, ok := stmt.From[0].Primary.(*SubqueryFrom); !ok {
		t.Fatalf("primary FROM item = %T, want subquery", stmt.From[0].Primary)
	}
}

func TestPolyglotCommonExtensions(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{name: "window function", sql: "SELECT ROW() OVER(PARTITION BY x) FROM x", want: "SELECT ROW() OVER (PARTITION BY x) FROM x"},
		{name: "named windows", sql: "SELECT 1 WINDOW a AS (PARTITION BY x), b AS (ORDER BY y)", want: "SELECT 1 WINDOW a AS (PARTITION BY x), b AS (ORDER BY y)"},
		{name: "typed timestamp", sql: "TIMESTAMP '2022-01-01'", want: "CAST('2022-01-01' AS TIMESTAMP)"},
		{name: "json literal", sql: `JSON '{"x":"y"}'`, want: `PARSE_JSON('{"x":"y"}')`},
		{name: "coalesce operator", sql: "SELECT a ?? b FROM t", want: "SELECT COALESCE(a, b) FROM t"},
		{name: "create materialized table", sql: "create materialized table x", want: "CREATE MATERIALIZED TABLE x"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := ParseStrict(test.sql, DialectGeneric)
			if err != nil {
				t.Fatalf("parse error: %v\n%#v", err, result.Diagnostics)
			}
			generated, err := Generate(result.Statements[0].Node)
			if err != nil {
				t.Fatalf("generate error: %v", err)
			}
			if generated != test.want {
				t.Fatalf("generated %q, want %q", generated, test.want)
			}
		})
	}
}

func TestFunctionAndTableValidationDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  string
	}{
		{name: "if too few", sql: "IF(a > 0)"},
		{name: "if too many", sql: "IF(a > 0, a, b, c)"},
		{name: "json object odd arguments", sql: "SELECT JSON_OBJECT('a')"},
		{name: "empty unnest", sql: "SELECT * FROM UNNEST()"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := ParseTolerant(test.sql, DialectPostgreSQL)
			if !result.HasErrors() {
				t.Fatalf("expected diagnostics for %q", test.sql)
			}
		})
	}
}

func TestDialectAliases(t *testing.T) {
	for _, name := range []string{"POSTGRES", "postgresql", "pgsql"} {
		dialect, err := ParseDialect(name)
		if err != nil || dialect != DialectPostgreSQL {
			t.Fatalf("ParseDialect(%q) = %q, %v", name, dialect, err)
		}
	}
	if _, err := ParseDialect("not-a-dialect"); err == nil {
		t.Fatal("expected invalid dialect error")
	}
}

func TestSourcePositionsSupportUTF16(t *testing.T) {
	source := NewSourceText("😀\nSELECT")
	got := source.Range(Span{Start: 0, End: 4}, PositionUTF16)
	if got.Start != (Position{Line: 0, Character: 0}) || got.End != (Position{Line: 0, Character: 2}) {
		t.Fatalf("UTF-16 range = %#v, want emoji width two", got)
	}
	selectStart := source.Range(Span{Start: 5, End: 5}, PositionUTF16)
	if selectStart.Start != (Position{Line: 1, Character: 0}) {
		t.Fatalf("line position = %#v, want line 1 column 0", selectStart.Start)
	}
}

func requireDiagnosticCode(t *testing.T, diagnostics []Diagnostic, code string) Diagnostic {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return diagnostic
		}
	}
	t.Fatalf("diagnostics %#v do not contain %s", diagnostics, code)
	return Diagnostic{}
}
