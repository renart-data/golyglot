package golyglot

import (
	"fmt"
	"strings"
)

// SyntacticContextKind identifies the grammar region surrounding a cursor.
// It is intentionally syntactic: name resolution and catalog-aware completion
// can be layered on top without changing parser recovery.
type SyntacticContextKind uint8

const (
	ContextDocument SyntacticContextKind = iota + 1
	ContextStatement
	ContextCTE
	ContextSelectList
	ContextFrom
	ContextJoin
	ContextWhere
	ContextGroupBy
	ContextHaving
	ContextQualify
	ContextWindow
	ContextOrderBy
	ContextLimit
	ContextInsert
	ContextUpdate
	ContextDelete
	ContextExpression
)

// SyntacticContext describes what the parser can accept at one byte cursor.
// Replace covers a partially typed token when one intersects the cursor;
// Prefix is the source text between Replace.Start and Cursor.
type SyntacticContext struct {
	Cursor   int
	Replace  Span
	Prefix   string
	Kind     SyntacticContextKind
	Expected []ExpectedSyntax
}

type contextCollector struct {
	kind     SyntacticContextKind
	priority int
	expected []ExpectedSyntax
}

func (c *contextCollector) record(kind SyntacticContextKind, priority int, expected ...ExpectedSyntax) {
	if priority < c.priority {
		return
	}
	if priority > c.priority {
		c.kind = kind
		c.priority = priority
		c.expected = c.expected[:0]
	}
	c.expected = mergeExpectedSyntax(c.expected, expected)
}

func (p *parser) recordCursorContext(kind SyntacticContextKind, priority int, expected ...ExpectedSyntax) {
	if p.sidecar == nil || p.sidecar.context == nil || p.peek().Kind != TokenEOF {
		return
	}
	p.sidecar.context.record(kind, priority, expected...)
}

func (p *parser) recordSelectContext(statement *SelectStmt) {
	const priority = 40
	setOperators := expectedKeywords("UNION", "INTERSECT", "EXCEPT")
	switch {
	case statement.Fetch != nil || statement.Offset != nil || statement.Limit != nil:
		p.recordCursorContext(ContextLimit, priority, append(expectedKeywords("OFFSET", "FETCH"), setOperators...)...)
	case len(statement.OrderBy) > 0 || len(statement.SortBy) > 0:
		expected := []ExpectedSyntax{{Kind: ExpectedToken, Text: ","}}
		expected = append(expected, expectedKeywords("LIMIT", "OFFSET", "FETCH")...)
		expected = append(expected, setOperators...)
		p.recordCursorContext(ContextOrderBy, priority, expected...)
	case len(statement.Windows) > 0:
		p.recordCursorContext(ContextWindow, priority, append(expectedKeywords("QUALIFY", "ORDER", "LIMIT", "OFFSET", "FETCH"), setOperators...)...)
	case statement.Qualify != nil:
		p.recordCursorContext(ContextQualify, priority, append(expectedKeywords("AND", "OR", "WINDOW", "ORDER", "LIMIT", "OFFSET", "FETCH"), setOperators...)...)
	case statement.Having != nil:
		p.recordCursorContext(ContextHaving, priority, append(expectedKeywords("AND", "OR", "QUALIFY", "WINDOW", "ORDER", "LIMIT", "OFFSET", "FETCH"), setOperators...)...)
	case len(statement.GroupBy) > 0:
		expected := []ExpectedSyntax{{Kind: ExpectedToken, Text: ","}}
		expected = append(expected, expectedKeywords("HAVING", "QUALIFY", "WINDOW", "ORDER", "LIMIT", "OFFSET", "FETCH")...)
		expected = append(expected, setOperators...)
		p.recordCursorContext(ContextGroupBy, priority, expected...)
	case statement.Where != nil:
		p.recordCursorContext(ContextWhere, priority, append(expectedKeywords("AND", "OR", "GROUP", "HAVING", "QUALIFY", "WINDOW", "ORDER", "LIMIT", "OFFSET", "FETCH"), setOperators...)...)
	case len(statement.From) > 0:
		expected := []ExpectedSyntax{{Kind: ExpectedToken, Text: ","}}
		expected = append(expected, expectedKeywords("JOIN", "INNER", "LEFT", "RIGHT", "FULL", "CROSS", "WHERE", "GROUP", "HAVING", "QUALIFY", "WINDOW", "ORDER", "LIMIT", "OFFSET", "FETCH")...)
		expected = append(expected, setOperators...)
		p.recordCursorContext(ContextFrom, priority, expected...)
	default:
		expected := []ExpectedSyntax{{Kind: ExpectedToken, Text: ","}}
		expected = append(expected, expectedKeywords("FROM", "WHERE", "GROUP", "HAVING", "QUALIFY", "WINDOW", "ORDER", "LIMIT", "OFFSET", "FETCH")...)
		expected = append(expected, setOperators...)
		p.recordCursorContext(ContextSelectList, priority, expected...)
	}
}

func (p *parser) recordInsertContext(statement *InsertStmt) {
	if len(statement.Values) == 0 && statement.Query == nil {
		return
	}
	expected := []ExpectedSyntax{{Kind: ExpectedToken, Text: ","}}
	expected = append(expected, expectedKeywords("ON", "RETURNING")...)
	p.recordCursorContext(ContextInsert, 40, expected...)
}

func (p *parser) recordUpdateContext(statement *UpdateStmt) {
	if len(statement.Assignments) == 0 {
		return
	}
	expected := []ExpectedSyntax{{Kind: ExpectedToken, Text: ","}}
	expected = append(expected, expectedKeywords("FROM", "WHERE", "RETURNING")...)
	p.recordCursorContext(ContextUpdate, 40, expected...)
}

func (p *parser) recordDeleteContext(statement *DeleteStmt) {
	if len(statement.Table) == 0 {
		return
	}
	p.recordCursorContext(ContextDelete, 40, expectedKeywords("USING", "WHERE", "RETURNING")...)
}

// SyntacticContextAt parses the document prefix at cursor with the same
// recursive-descent and Pratt parser used by Parse. A partial token at the
// cursor is made virtual for this parse so its replacement span and prefix do
// not distort the surrounding grammar.
func SyntacticContextAt(sql string, cursor int, dialect Dialect) (SyntacticContext, error) {
	context := SyntacticContext{
		Cursor:  cursor,
		Replace: Span{Start: cursor, End: cursor},
		Kind:    ContextDocument,
	}
	if cursor < 0 || cursor > len(sql) {
		return context, fmt.Errorf("cursor byte offset %d is outside a %d-byte SQL document", cursor, len(sql))
	}
	if cursor > 0 && cursor < len(sql) && sql[cursor]&0xc0 == 0x80 {
		return context, fmt.Errorf("cursor byte offset %d splits a UTF-8 code point", cursor)
	}
	options, err := (ParseOptions{Dialect: dialect, Mode: Tolerant}).normalized()
	if err != nil {
		return context, err
	}
	if len(sql) > options.MaxInputBytes {
		return context, &GuardError{
			Code:    "GUARD_INPUT_TOO_LARGE",
			Message: fmt.Sprintf("SQL input is %d bytes; maximum is %d", len(sql), options.MaxInputBytes),
		}
	}

	tokens, _ := lexSQL(sql, options)
	view, replace, prefix := contextTokenView(sql, cursor, tokens)
	context.Replace = replace
	context.Prefix = prefix

	collector := &contextCollector{}
	p := parser{
		text:        sql[:cursor],
		tokens:      view,
		tokensOwned: true,
		options:     options,
		sidecar:     &parserSidecar{context: collector},
	}
	statements := p.parseStatements()
	if len(statements) > 0 {
		collector.record(ContextStatement, 1)
	}
	for _, diagnostic := range p.diagnostics {
		if diagnostic.Span.Start != cursor || len(diagnostic.Expected) == 0 {
			continue
		}
		kind, priority := contextKindForDiagnostic(diagnostic)
		collector.record(kind, priority, diagnostic.Expected...)
	}
	if len(collector.expected) == 0 {
		collector.record(ContextDocument, 1, statementExpectations()...)
	}
	context.Kind = collector.kind
	context.Expected = append([]ExpectedSyntax(nil), collector.expected...)
	return context, nil
}

func contextTokenView(sql string, cursor int, tokens []Token) ([]Token, Span, string) {
	replace := Span{Start: cursor, End: cursor}
	prefix := ""
	view := make([]Token, 0, len(tokens))
	for _, token := range tokens {
		if token.Kind == TokenEOF || token.Span.Start >= cursor {
			break
		}
		if token.Span.End > cursor {
			if isCursorPrefixToken(token) {
				replace = token.Span
				prefix = sql[token.Span.Start:cursor]
			}
			break
		}
		if token.Span.End == cursor && cursor == len(sql) && token.Kind != TokenKeyword && isCursorPrefixToken(token) {
			replace = token.Span
			prefix = token.Text
			break
		}
		if token.Kind == TokenUnterminatedComment {
			token.Kind = TokenComment
		}
		view = append(view, token)
	}
	view = append(view, Token{Kind: TokenEOF, Span: Span{Start: cursor, End: cursor}})
	return view, replace, prefix
}

func isCursorPrefixToken(token Token) bool {
	switch token.Kind {
	case TokenIdentifier, TokenQuotedIdentifier, TokenKeyword, TokenParameter:
		return true
	default:
		return false
	}
}

func statementExpectations() []ExpectedSyntax {
	return []ExpectedSyntax{
		{Kind: ExpectedKeyword, Text: "SELECT"},
		{Kind: ExpectedKeyword, Text: "WITH"},
		{Kind: ExpectedKeyword, Text: "VALUES"},
		{Kind: ExpectedKeyword, Text: "INSERT"},
		{Kind: ExpectedKeyword, Text: "UPDATE"},
		{Kind: ExpectedKeyword, Text: "DELETE"},
		{Kind: ExpectedKeyword, Text: "CREATE"},
		{Kind: ExpectedKeyword, Text: "SET"},
		{Kind: ExpectedKeyword, Text: "USE"},
	}
}

func contextKindForDiagnostic(diagnostic Diagnostic) (SyntacticContextKind, int) {
	message := strings.ToUpper(diagnostic.Message)
	switch diagnostic.Code {
	case "PARSE_EXPECTED_INSERT_SOURCE":
		return ContextInsert, 90
	case "PARSE_INCOMPLETE_COMMAND":
		return ContextStatement, 90
	case "PARSE_EXPECTED_TABLE":
		if strings.Contains(message, "JOIN") {
			return ContextJoin, 90
		}
		return ContextFrom, 90
	case "PARSE_EXPECTED_QUERY":
		if strings.Contains(message, "CTE") {
			return ContextCTE, 90
		}
		return ContextStatement, 80
	case "PARSE_EXPECTED_IDENTIFIER":
		switch {
		case strings.Contains(message, "UPDATE"):
			return ContextUpdate, 90
		case strings.Contains(message, "INSERT"):
			return ContextInsert, 90
		case strings.Contains(message, "DELETE"):
			return ContextDelete, 90
		case strings.Contains(message, "CTE") || strings.Contains(message, "WITH"):
			return ContextCTE, 90
		case strings.Contains(message, "FROM") || strings.Contains(message, "JOIN"):
			return ContextFrom, 90
		default:
			return ContextExpression, 80
		}
	case "PARSE_EXPECTED_EXPRESSION":
		switch {
		case strings.Contains(message, "WHERE"):
			return ContextWhere, 90
		case strings.Contains(message, "HAVING"):
			return ContextHaving, 90
		case strings.Contains(message, "QUALIFY"):
			return ContextQualify, 90
		case strings.Contains(message, "ORDER"):
			return ContextOrderBy, 90
		case strings.Contains(message, "GROUP"):
			return ContextGroupBy, 90
		case strings.Contains(message, "UPDATE"):
			return ContextUpdate, 90
		case strings.Contains(message, "INSERT"):
			return ContextInsert, 90
		case strings.Contains(message, "SELECT"):
			return ContextSelectList, 90
		default:
			return ContextExpression, 80
		}
	default:
		return ContextStatement, 70
	}
}
