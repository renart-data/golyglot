package golyglot

import (
	"strings"
	"testing"
)

func TestRecoverySidecarDescribesMissingSyntax(t *testing.T) {
	const sql = "SELECT * FROM users WHERE"
	result := ParseTolerant(sql, DialectPostgreSQL)
	diagnostic := requireDiagnosticCode(t, result.Diagnostics, "PARSE_EXPECTED_EXPRESSION")
	if len(diagnostic.Expected) != 1 || diagnostic.Expected[0] != (ExpectedSyntax{Kind: ExpectedExpression}) {
		t.Fatalf("diagnostic expectations = %#v, want expression", diagnostic.Expected)
	}
	if len(result.Recoveries) != 1 {
		t.Fatalf("recoveries = %#v, want one missing element", result.Recoveries)
	}
	recovery := result.Recoveries[0]
	if recovery.Kind != RecoveryMissing || recovery.Span != (Span{Start: len(sql), End: len(sql)}) {
		t.Fatalf("recovery = %#v, want zero-width missing element at EOF", recovery)
	}
	if recovery.Found.Kind != TokenEOF || recovery.DiagnosticCode != diagnostic.Code {
		t.Fatalf("recovery ownership = %#v, want EOF owned by %s", recovery, diagnostic.Code)
	}
	if len(recovery.Expected) != 1 || recovery.Expected[0] != (ExpectedSyntax{Kind: ExpectedExpression}) {
		t.Fatalf("recovery expectations = %#v, want expression", recovery.Expected)
	}
}

func TestTolerantRecoveryCollapsesInsertionCascades(t *testing.T) {
	for _, sql := range []string{
		"WITH x",
		"UPDATE t SET",
		"DELETE FROM",
		"CREATE TABLE",
	} {
		t.Run(sql, func(t *testing.T) {
			result := ParseTolerant(sql, DialectGeneric)
			if !result.HasErrors() {
				t.Fatalf("ParseTolerant(%q) has no error", sql)
			}
			for index := 1; index < len(result.Diagnostics); index++ {
				previous := result.Diagnostics[index-1]
				current := result.Diagnostics[index]
				if previous.Recovery == RecoveryInserted && current.Recovery == RecoveryInserted && previous.Span == current.Span {
					t.Fatalf("ParseTolerant(%q) retained insertion cascade: %#v", sql, result.Diagnostics)
				}
			}
		})
	}
}

func TestUnterminatedCommentDiagnosticIsOwnedByLexer(t *testing.T) {
	result := ParseTolerant("SELECT 1 /* unfinished", DialectGeneric)
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != "LEX_UNTERMINATED_COMMENT" {
		t.Fatalf("diagnostics = %#v, want only the lexical root cause", result.Diagnostics)
	}
	if len(result.Statements) != 1 {
		t.Fatalf("got %d statements, want the usable SELECT prefix", len(result.Statements))
	}
}

func TestTolerantIncompleteDMLKeepsTypedStatements(t *testing.T) {
	tests := []struct {
		sql  string
		kind NodeKind
	}{
		{sql: "INSERT", kind: NodeInsertStatement},
		{sql: "INSERT INTO", kind: NodeInsertStatement},
		{sql: "INSERT INTO events", kind: NodeInsertStatement},
		{sql: "INSERT INTO events (", kind: NodeInsertStatement},
		{sql: "UPDATE", kind: NodeUpdateStatement},
		{sql: "UPDATE accounts", kind: NodeUpdateStatement},
		{sql: "UPDATE accounts SET", kind: NodeUpdateStatement},
		{sql: "DELETE FROM", kind: NodeDeleteStatement},
	}
	for _, test := range tests {
		t.Run(test.sql, func(t *testing.T) {
			result := ParseTolerant(test.sql, DialectGeneric)
			if len(result.Statements) != 1 {
				t.Fatalf("got %d statements, want one", len(result.Statements))
			}
			if got := result.Statements[0].Node.Kind(); got != test.kind {
				t.Fatalf("statement kind = %v (%T), want %v", got, result.Statements[0].Node, test.kind)
			}
			if !result.HasErrors() || len(result.Recoveries) == 0 {
				t.Fatalf("incomplete statement has no recovery detail: %#v", result)
			}
			if result.OriginalSQL() != test.sql {
				t.Fatalf("OriginalSQL() = %q, want %q", result.OriginalSQL(), test.sql)
			}
		})
	}
}

func TestTolerantCreateTableReportsUnclosedBody(t *testing.T) {
	const sql = "CREATE TABLE events (id BIGINT"
	result := ParseTolerant(sql, DialectGeneric)
	diagnostic := requireDiagnosticCode(t, result.Diagnostics, "PARSE_UNCLOSED_PAREN")
	if len(diagnostic.Expected) != 1 || diagnostic.Expected[0] != (ExpectedSyntax{Kind: ExpectedToken, Text: ")"}) {
		t.Fatalf("expectations = %#v, want closing parenthesis", diagnostic.Expected)
	}
	if result.Statements[0].Node.Kind() != NodeCreateTable {
		t.Fatalf("statement = %T, want *CreateTableStmt", result.Statements[0].Node)
	}
}

func TestStatementSynchronizationIgnoresNestedStarters(t *testing.T) {
	const sql = "SELECT 1 alias (SELECT 2) SELECT 3"
	result := ParseTolerant(sql, DialectGeneric)
	if len(result.Statements) != 2 {
		t.Fatalf("got %d statements, want recovery to resume at the top-level SELECT: %#v", len(result.Statements), result.Statements)
	}
	if result.Statements[1].Node.Kind() != NodeSelectStatement {
		t.Fatalf("second statement = %T, want *SelectStmt", result.Statements[1].Node)
	}
	foundSkippedGroup := false
	for _, recovery := range result.Recoveries {
		if recovery.Kind != RecoverySkipped {
			continue
		}
		text, ok := result.SourceSlice(recovery.Span)
		if ok && text == "(SELECT 2)" {
			foundSkippedGroup = true
		}
	}
	if !foundSkippedGroup {
		t.Fatalf("recoveries = %#v, want nested SELECT skipped as one group", result.Recoveries)
	}
}

func TestTrailingQualifiedNameRecordsMissingIdentifier(t *testing.T) {
	const sql = "SELECT account."
	result := ParseTolerant(sql, DialectGeneric)
	diagnostic := requireDiagnosticCode(t, result.Diagnostics, "PARSE_EXPECTED_IDENTIFIER")
	if diagnostic.Span != (Span{Start: len(sql), End: len(sql)}) {
		t.Fatalf("diagnostic span = %#v, want EOF insertion", diagnostic.Span)
	}
}

func TestRepresentativePrefixesStayLosslessAndMakeProgress(t *testing.T) {
	statements := []string{
		"SELECT customer_id, SUM(total) FROM orders WHERE paid = TRUE GROUP BY customer_id ORDER BY customer_id",
		"WITH recent AS (SELECT * FROM events WHERE created_at > CURRENT_DATE - INTERVAL '7 DAY') SELECT * FROM recent",
		"SELECT CASE WHEN score > 90 THEN 'a' WHEN score > 80 THEN 'b' ELSE 'c' END FROM grades",
		"SELECT users.id FROM users LEFT JOIN orders ON users.id = orders.user_id",
		"INSERT INTO events (id, payload) VALUES (1, JSON_OBJECT('ok', TRUE))",
		"UPDATE accounts SET active = FALSE, updated_at = CURRENT_TIMESTAMP WHERE id = 42",
		"DELETE FROM sessions WHERE expires_at < CURRENT_TIMESTAMP",
		"CREATE TABLE events (id BIGINT, payload JSON)",
	}

	for _, statement := range statements {
		for end := 0; end <= len(statement); end++ {
			sql := statement[:end]
			result := ParseTolerant(sql, DialectGeneric)
			if result.OriginalSQL() != sql {
				t.Fatalf("OriginalSQL changed prefix %q", sql)
			}
			var reconstructed strings.Builder
			for tokenIndex, token := range result.Tokens {
				gap, ok := result.SourceGapBefore(tokenIndex)
				if !ok {
					t.Fatalf("invalid gap before token %d in prefix %q", tokenIndex, sql)
				}
				gapText, _ := result.SourceSlice(gap)
				reconstructed.WriteString(gapText)
				if token.Kind != TokenEOF {
					tokenText, ok := result.SourceSlice(token.Span)
					if !ok || tokenText != token.Text {
						t.Fatalf("token %d is not source-backed in prefix %q: %#v", tokenIndex, sql, token)
					}
					reconstructed.WriteString(tokenText)
				}
			}
			if got := reconstructed.String(); got != sql {
				t.Fatalf("reconstructed prefix = %q, want %q", got, sql)
			}
			if len(result.Diagnostics) >= maxDiagnostics {
				t.Fatalf("prefix %q exhausted diagnostic budget", sql)
			}
		}
	}
}
