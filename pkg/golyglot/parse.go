package golyglot

import "fmt"

// ParseResult contains the syntax tree, original tokens, and all diagnostics
// produced while processing one SQL document.
type ParseResult struct {
	SQL         string
	Source      SourceText
	Tokens      []Token
	Statements  []Statement
	Diagnostics []Diagnostic
	Recoveries  []RecoveryElement
}

func (r ParseResult) HasErrors() bool { return hasErrorDiagnostics(r.Diagnostics) }

func (r ParseResult) DiagnosticRange(diagnostic Diagnostic, encoding PositionEncoding) Range {
	return r.Source.Range(diagnostic.Span, encoding)
}

// Parse parses one SQL document. In Strict mode, syntax diagnostics are also
// returned as *SyntaxError. In Tolerant mode, syntax diagnostics remain in the
// result and a partial tree is returned whenever possible.
func Parse(sql string, options ParseOptions) (ParseResult, error) {
	options, err := options.normalized()
	result := ParseResult{SQL: sql, Source: NewSourceText(sql)}
	if err != nil {
		return result, err
	}
	if len(sql) > options.MaxInputBytes {
		diagnostic := Diagnostic{
			Severity: SeverityError,
			Code:     "GUARD_INPUT_TOO_LARGE",
			Message:  fmt.Sprintf("SQL input is %d bytes; maximum is %d", len(sql), options.MaxInputBytes),
			Span:     Span{Start: 0, End: len(sql)},
		}
		result.Diagnostics = []Diagnostic{diagnostic}
		return result, &GuardError{Code: diagnostic.Code, Message: diagnostic.Message}
	}

	tokens, lexicalDiagnostics := lexSQL(sql, options)
	result.Tokens = tokens
	result.Diagnostics = append(result.Diagnostics, lexicalDiagnostics...)
	parserTokens := tokens
	parserTokensOwned := false
	if len(lexicalDiagnostics) > 0 {
		parserTokens, parserTokensOwned = parserTokenView(tokens, lexicalDiagnostics)
	}

	p := parser{
		text:        sql,
		tokens:      parserTokens,
		tokensOwned: parserTokensOwned,
		options:     options,
	}
	result.Statements = p.parseStatements()
	result.Diagnostics = append(result.Diagnostics, p.diagnostics...)
	if p.recovery != nil {
		result.Recoveries = p.recovery.elements
	}
	if len(result.Statements) == 1 && (hasCommentToken(tokens) || parserTokensOwned) {
		if rawNode, ok := result.Statements[0].Node.(interface{ setRaw(string) }); ok {
			rawNode.setRaw(sql)
		}
	}

	if options.Mode == Strict && hasErrorDiagnostics(result.Diagnostics) {
		if diagnostic, ok := polyglotPrimaryDiagnostic(result); ok {
			return result, newSyntaxError(result, options.Dialect, diagnostic)
		}
	}
	return result, nil
}

func parserTokenView(tokens []Token, diagnostics []Diagnostic) ([]Token, bool) {
	unterminatedComment := false
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == "LEX_UNTERMINATED_COMMENT" {
			unterminatedComment = true
			break
		}
	}
	if !unterminatedComment {
		return tokens, false
	}
	view := append([]Token(nil), tokens...)
	for index := range view {
		if view[index].Kind == TokenUnterminatedComment {
			view[index].Kind = TokenComment
		}
	}
	return view, true
}

func hasCommentToken(tokens []Token) bool {
	for _, token := range tokens {
		if token.Kind == TokenComment {
			return true
		}
	}
	return false
}

// ParseTolerant is a convenience API for editor integrations. Invalid
// dialects and guard failures are represented by a diagnostic in the result;
// callers that need to distinguish those conditions can use Parse directly.
func ParseTolerant(sql string, dialect Dialect) ParseResult {
	result, err := Parse(sql, ParseOptions{Dialect: dialect, Mode: Tolerant})
	if err != nil && len(result.Diagnostics) == 0 {
		result.Diagnostics = append(result.Diagnostics, Diagnostic{
			Severity: SeverityError,
			Code:     "PARSE_FAILED",
			Message:  err.Error(),
			Span:     Span{Start: 0, End: 0},
		})
	}
	return result
}

func ParseStrict(sql string, dialect Dialect) (ParseResult, error) {
	return Parse(sql, ParseOptions{Dialect: dialect, Mode: Strict})
}

// Tokenize exposes the same recoverable lexer used by Parse. Lexical problems
// are returned as diagnostics; only invalid options and guard failures are
// returned as Go errors.
func Tokenize(sql string, dialect Dialect) ([]Token, []Diagnostic, error) {
	options, err := (ParseOptions{Dialect: dialect, Mode: Tolerant}).normalized()
	if err != nil {
		return nil, nil, err
	}
	if len(sql) > options.MaxInputBytes {
		return nil, nil, &GuardError{
			Code:    "GUARD_INPUT_TOO_LARGE",
			Message: fmt.Sprintf("SQL input is %d bytes; maximum is %d", len(sql), options.MaxInputBytes),
		}
	}
	tokens, diagnostics := lexSQL(sql, options)
	return tokens, diagnostics, nil
}

// ParseOne parses exactly one non-empty statement. The full ParseResult is
// returned as well so callers can inspect diagnostics and source tokens.
func ParseOne(sql string, options ParseOptions) (Statement, ParseResult, error) {
	result, err := Parse(sql, options)
	if err != nil {
		return Statement{}, result, err
	}
	if len(result.Statements) != 1 {
		return Statement{}, result, fmt.Errorf("expected one SQL statement, got %d", len(result.Statements))
	}
	return result.Statements[0], result, nil
}
