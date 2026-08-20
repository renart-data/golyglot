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
