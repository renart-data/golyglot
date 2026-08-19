package golyglot

import "testing"

func TestLexerPreservesCommentsAndLiteralSpans(t *testing.T) {
	result, err := ParseStrict("SELECT 'hello', -- note\n name FROM t", DialectGeneric)
	if err != nil {
		t.Fatalf("ParseStrict returned error: %v", err)
	}
	var foundComment, foundString bool
	for _, token := range result.Tokens {
		if token.Kind == TokenComment {
			foundComment = true
			if token.Text != "-- note" {
				t.Fatalf("comment text = %q", token.Text)
			}
		}
		if token.Kind == TokenString {
			foundString = true
			if result.SQL[token.Span.Start:token.Span.End] != "'hello'" {
				t.Fatalf("string span = %#v", token.Span)
			}
		}
	}
	if !foundComment || !foundString {
		t.Fatalf("tokens = %#v, want comment and string tokens", result.Tokens)
	}
}

func TestLexerHandlesDollarQuotedAndUnicodeInput(t *testing.T) {
	result, err := ParseStrict("SELECT $$你好😀$$, 用户名 FROM 表", DialectPostgreSQL)
	if err != nil {
		t.Fatalf("ParseStrict returned error: %v", err)
	}
	if len(result.Statements) != 1 {
		t.Fatalf("got %d statements", len(result.Statements))
	}
}

func TestLexerReportsUnterminatedBlockComment(t *testing.T) {
	result := ParseTolerant("SELECT 1 /* unfinished", DialectGeneric)
	requireDiagnosticCode(t, result.Diagnostics, "LEX_UNTERMINATED_COMMENT")
}
