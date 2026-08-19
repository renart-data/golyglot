package golyglot

import (
	"fmt"
	"strings"
)

func polyglotPrimaryDiagnostic(result ParseResult) (Diagnostic, bool) {
	var primary Diagnostic
	found := false
	primaryEnd := -1
	for _, diagnostic := range result.Diagnostics {
		if diagnostic.Severity != SeverityError {
			continue
		}
		// Polyglot tokenization is fail-fast, so a lexical error always owns
		// the strict result even when Golyglot also recovered parser detail.
		if strings.HasPrefix(diagnostic.Code, "LEX_") {
			return diagnostic, true
		}
		span := polyglotPrimarySpan(result.Tokens, diagnostic)
		preferStructuralTie := found && span.End == primaryEnd && strings.HasPrefix(diagnostic.Code, "PARSE_UNCLOSED_") && !strings.HasPrefix(primary.Code, "PARSE_UNCLOSED_")
		preferAfterToleratedFromFirst := found && span.End > primaryEnd && strings.Contains(primary.Message, `in SELECT list; got "FROM"`)
		if !found || preferStructuralTie || preferAfterToleratedFromFirst {
			primary = diagnostic
			primaryEnd = span.End
			found = true
		}
	}
	return primary, found
}

func newSyntaxError(result ParseResult, dialect Dialect, diagnostic Diagnostic) *SyntaxError {
	span := polyglotPrimarySpan(result.Tokens, diagnostic)
	position := result.Source.PositionAt(span.End, PositionUTF32)
	compatibility := PolyglotDiagnostic{
		Kind:    PolyglotErrorParse,
		Message: polyglotPrimaryMessage(result.SQL, result.Tokens, dialect, diagnostic, span),
		Line:    position.Line + 1,
		Column:  position.Character + 1,
		Span:    span,
	}
	if strings.HasPrefix(diagnostic.Code, "LEX_") {
		compatibility.Kind = PolyglotErrorTokenize
	}
	return &SyntaxError{Diagnostic: diagnostic, Polyglot: compatibility}
}

func polyglotPrimarySpan(tokens []Token, diagnostic Diagnostic) Span {
	if strings.Contains(diagnostic.Message, "UNNEST requires") || diagnostic.Code == "PARSE_INVALID_FUNCTION_ARGUMENTS" {
		for i := len(tokens) - 1; i >= 0; i-- {
			token := tokens[i]
			if token.Kind != TokenComment && token.Kind != TokenEOF && token.Span.End == diagnostic.Span.End {
				return token.Span
			}
		}
	}
	if !diagnostic.Span.Empty() {
		return diagnostic.Span
	}
	position := diagnostic.Span.Start
	for _, token := range tokens {
		if token.Kind != TokenComment && token.Kind != TokenEOF && !token.Span.Empty() && token.Span.Start == position {
			return token.Span
		}
	}
	span := diagnostic.Span
	for _, token := range tokens {
		if token.Kind == TokenComment || token.Kind == TokenEOF || token.Span.Empty() || token.Span.End > position {
			continue
		}
		if span.Empty() || token.Span.End >= span.End {
			span = token.Span
		}
	}
	return span
}

func polyglotPrimaryMessage(sql string, tokens []Token, dialect Dialect, diagnostic Diagnostic, span Span) string {
	offending, _ := tokenAtSpan(tokens, span)
	switch diagnostic.Code {
	case "LEX_UNEXPECTED_CHARACTER":
		text := offending.Text
		if text == "" && diagnostic.Span.Valid(len(sql)) {
			text = sql[diagnostic.Span.Start:diagnostic.Span.End]
		}
		return fmt.Sprintf("Unexpected character: '%s'", text)
	case "LEX_UNTERMINATED_COMMENT":
		return "Unterminated block comment"
	case "LEX_UNTERMINATED_STRING":
		if diagnostic.Span.Valid(len(sql)) {
			text := sql[diagnostic.Span.Start:diagnostic.Span.End]
			switch {
			case strings.HasPrefix(text, `"""`) || strings.HasPrefix(text, `'''`):
				return "Unterminated triple-quoted string"
			case strings.HasPrefix(text, `"`):
				return "Unterminated double-quoted string"
			}
		}
		return "Unterminated string"
	case "PARSE_UNCLOSED_PAREN":
		return polyglotExpectedMessage(sql, tokens, diagnostic, "RParen")
	case "PARSE_UNCLOSED_BRACKET":
		return polyglotExpectedMessage(sql, tokens, diagnostic, "RBracket")
	case "PARSE_UNCLOSED_BRACE":
		return polyglotExpectedMessage(sql, tokens, diagnostic, "RBrace")
	case "PARSE_UNEXPECTED_TOKEN":
		if strings.Contains(diagnostic.Message, "after statement") {
			return "Invalid expression / Unexpected token"
		}
		return "Unexpected token: " + polyglotTokenName(offending)
	case "PARSE_EXPECTED_TABLE":
		return "Expected table name or subquery, got " + polyglotTokenName(offending)
	case "PARSE_EXPECTED_IDENTIFIER":
		if offending.IsWord("JOIN") {
			return "Expected table name or subquery, got Join"
		}
	case "PARSE_EXPECTED_EXPRESSION":
		if strings.Contains(diagnostic.Message, "UNNEST requires") {
			return "Expected expression in UNNEST"
		}
		if dialect == DialectClickHouse && offending.Text == ":" && strings.Contains(sql, "?") {
			return "Expected true expression after ? in ClickHouse ternary"
		}
		return "Unexpected token: " + polyglotTokenName(offending)
	case "PARSE_EXPECTED_TOKEN":
		if offending.IsWord("IN") {
			return "Unexpected token: In"
		}
	case "PARSE_EXPECTED_KEYWORD":
		if strings.Contains(diagnostic.Message, "END to close CASE") {
			return "Unexpected token: " + polyglotTokenName(offending)
		}
		if strings.Contains(diagnostic.Message, "SELECT at the start of a query") && strings.Contains(diagnostic.Message, "end of input") {
			return "Unexpected end of input"
		}
	case "PARSE_INVALID_FUNCTION_ARGUMENTS":
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(sql)), "IF(") {
			return "IF function requires 2 or 3 arguments"
		}
	}
	return diagnostic.Message
}

func polyglotExpectedMessage(sql string, tokens []Token, diagnostic Diagnostic, expected string) string {
	significant := make([]Token, 0, len(tokens))
	current := -1
	for _, token := range tokens {
		if token.Kind == TokenComment || token.Kind == TokenEOF {
			continue
		}
		if current < 0 && token.Span.Start == diagnostic.Span.Start {
			current = len(significant)
		}
		significant = append(significant, token)
	}
	atEnd := current < 0
	if atEnd {
		current = len(significant)
	}
	got := "end of input"
	gotText := ""
	if !atEnd {
		got = polyglotTokenName(significant[current])
		gotText = significant[current].Text
	}
	start := current - 3
	if start < 0 {
		start = 0
	}
	end := current + 4
	if end > len(significant) {
		end = len(significant)
	}
	context := ""
	if start < end {
		contextStart := significant[start].Span.Start
		contextEnd := significant[end-1].Span.End
		if (Span{Start: contextStart, End: contextEnd}).Valid(len(sql)) {
			context = strings.Join(strings.Fields(sql[contextStart:contextEnd]), " ")
		}
	}
	return fmt.Sprintf("Expected %s, got %s ('%s') near [%s]", expected, got, gotText, context)
}

func tokenAtSpan(tokens []Token, span Span) (Token, bool) {
	for _, token := range tokens {
		if token.Kind != TokenComment && token.Kind != TokenEOF && token.Span == span {
			return token, true
		}
	}
	return Token{Kind: TokenEOF}, false
}

func polyglotTokenName(token Token) string {
	switch token.Text {
	case "(":
		return "LParen"
	case ")":
		return "RParen"
	case "[":
		return "LBracket"
	case "]":
		return "RBracket"
	case "{":
		return "LBrace"
	case "}":
		return "RBrace"
	case ",":
		return "Comma"
	case ";":
		return "Semicolon"
	case ":":
		return "Colon"
	}
	if token.Kind == TokenEOF {
		return "Eof"
	}
	if token.Kind == TokenKeyword {
		parts := strings.Split(strings.ToLower(token.Text), "_")
		for i := range parts {
			if parts[i] != "" {
				parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
			}
		}
		return strings.Join(parts, "")
	}
	switch token.Kind {
	case TokenIdentifier:
		return "Var"
	case TokenQuotedIdentifier:
		return "QuotedIdentifier"
	case TokenString, TokenUnterminatedString:
		return "String"
	case TokenNumber:
		return "Number"
	case TokenParameter:
		return "Placeholder"
	case TokenOperator:
		return "Operator"
	case TokenUnknown:
		return "Unknown"
	default:
		return token.Kind.String()
	}
}
