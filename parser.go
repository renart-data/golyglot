package golyglot

import (
	"fmt"
	"strings"
)

const maxDiagnostics = 100

type parser struct {
	text        string
	tokens      []Token
	pos         int
	lastEnd     int
	depth       int
	nodeCount   int
	options     ParseOptions
	diagnostics []Diagnostic
}

func (p *parser) parseStatements() []Statement {
	statements := make([]Statement, 0, 1)
	for {
		tok := p.peek()
		if tok.Kind == TokenEOF {
			break
		}
		if tok.Text == ";" {
			p.advance()
			continue
		}

		start := tok.Span.Start
		var node Node
		if tok.IsWord("SELECT") || (tok.IsWord("WITH") && !p.startsWithWithNonQuery()) {
			node = p.parseSelect()
		} else if tok.Text == "(" && (p.queryStartsAfterParen() || p.startsNestedQueryFrom()) {
			node = p.parseParenthesizedQueryStatement()
		} else if tok.IsWord("CREATE") && p.startsCreateTable() {
			node = p.parseCreateTable()
		} else if tok.IsWord("CREATE") || (tok.IsWord("WITH") && p.startsWithWithNonQuery()) || p.isStatementKeyword(tok) {
			switch {
			case tok.IsWord("INSERT") && p.startsSimpleInsert():
				node = p.parseInsert()
			case tok.IsWord("UPDATE") && p.startsSimpleUpdate():
				node = p.parseUpdate()
			case tok.IsWord("DELETE"):
				node = p.parseDelete()
			default:
				node = p.parseRawStatement()
			}
		} else if tok.IsWord("SET") || tok.IsWord("USE") {
			node = p.parseCommand()
		} else if p.isExpressionStatementStart(tok) {
			node = p.parseExpressionStatement()
		} else {
			node = p.parseUnknownStatement()
		}

		end := p.lastEnd
		if end < start {
			end = tok.Span.End
		}
		if p.peek().Text == ";" {
			end = p.advance().Span.End
		}
		if node == nil {
			node = &UnknownStmt{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Reason: "parser produced no statement"}
		}
		statements = append(statements, Statement{Node: node, Span: Span{Start: start, End: end}})

		if p.peek().Kind != TokenEOF && p.peek().Text != ";" {
			p.report(Diagnostic{
				Severity: SeverityError,
				Code:     "PARSE_UNEXPECTED_TOKEN",
				Message:  fmt.Sprintf("unexpected %s after statement", p.peek().Description()),
				Span:     p.peek().Span,
				Found:    p.peek().Kind,
				Recovery: RecoverySynchronized,
			})
			p.synchronizeStatement()
		}
	}
	return statements
}

func (p *parser) parseRawStatement() Node {
	start := p.peek().Span.Start
	keyword := p.peek().Text
	end := start
	consumed := 0
	for p.peek().Kind != TokenEOF && p.peek().Text != ";" {
		end = p.advance().Span.End
		consumed++
	}
	if consumed == 1 && !allowsEmptyRawStatement(keyword) {
		p.report(Diagnostic{
			Severity: SeverityError,
			Code:     "PARSE_INCOMPLETE_STATEMENT",
			Message:  fmt.Sprintf("incomplete %s statement; expected a statement body", strings.ToUpper(keyword)),
			Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
			Found:    p.peek().Kind,
			Recovery: RecoveryInserted,
		})
	}
	return &RawStmt{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Keyword: strings.ToUpper(keyword), Raw: p.text[start:end]}
}

func allowsEmptyRawStatement(keyword string) bool {
	switch strings.ToUpper(keyword) {
	case "BEGIN", "COMMIT", "ROLLBACK", "VACUUM", "ANALYZE":
		return true
	default:
		return false
	}
}

func (p *parser) startsCreateTable() bool {
	index := p.pos
	words := 0
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	if index >= len(p.tokens) || !p.tokens[index].IsWord("CREATE") {
		return false
	}
	index++
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	for index < len(p.tokens) && (p.tokens[index].IsWord("MATERIALIZED") || p.tokens[index].IsWord("TEMPORARY")) && words < 2 {
		words++
		index++
		for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
			index++
		}
	}
	return index < len(p.tokens) && p.tokens[index].IsWord("TABLE")
}

func (p *parser) startsWithWithNonQuery() bool {
	index := p.pos
	depth := 0
	for index < len(p.tokens) {
		tok := p.tokens[index]
		if tok.Kind == TokenComment {
			index++
			continue
		}
		if tok.Text == "(" {
			depth++
		} else if tok.Text == ")" && depth > 0 {
			depth--
		} else if depth == 0 {
			if tok.IsWord("UPDATE") || tok.IsWord("INSERT") || tok.IsWord("DELETE") || tok.IsWord("MERGE") || tok.IsWord("CREATE") {
				return true
			}
			if tok.IsWord("SELECT") {
				return false
			}
		}
		index++
	}
	return false
}

func (p *parser) startsSimpleInsert() bool {
	index := p.pos
	if index >= len(p.tokens) || !p.tokens[index].IsWord("INSERT") {
		return false
	}
	index = nextSignificantToken(p.tokens, index+1)
	if index >= len(p.tokens) || !p.tokens[index].IsWord("INTO") {
		return false
	}
	index = nextSignificantToken(p.tokens, index+1)
	if index >= len(p.tokens) || !p.isNameToken(p.tokens[index]) {
		return false
	}
	for {
		index = nextSignificantToken(p.tokens, index+1)
		if index >= len(p.tokens) || p.tokens[index].Text != "." {
			break
		}
		index = nextSignificantToken(p.tokens, index+1)
		if index >= len(p.tokens) || !p.isNameToken(p.tokens[index]) {
			return false
		}
	}
	if index < len(p.tokens) && p.tokens[index].Text == "(" {
		inner := nextSignificantToken(p.tokens, index+1)
		if inner < len(p.tokens) && (p.tokens[inner].IsWord("SELECT") || p.tokens[inner].IsWord("WITH")) {
			return false
		}
		depth := 0
		for index < len(p.tokens) {
			token := p.tokens[index]
			if token.Kind != TokenComment {
				if token.Text == "(" {
					depth++
				} else if token.Text == ")" {
					depth--
					if depth == 0 {
						index = nextSignificantToken(p.tokens, index+1)
						break
					}
				}
			}
			index++
		}
	}
	return index < len(p.tokens) && (p.tokens[index].IsWord("VALUES") || p.tokens[index].IsWord("SELECT") || p.tokens[index].IsWord("WITH"))
}

func (p *parser) startsSimpleUpdate() bool {
	index := p.pos
	if index >= len(p.tokens) || !p.tokens[index].IsWord("UPDATE") {
		return false
	}
	index = nextSignificantToken(p.tokens, index+1)
	if index >= len(p.tokens) || !p.isNameToken(p.tokens[index]) {
		return false
	}
	for {
		index = nextSignificantToken(p.tokens, index+1)
		if index >= len(p.tokens) || p.tokens[index].Text != "." {
			break
		}
		index = nextSignificantToken(p.tokens, index+1)
		if index >= len(p.tokens) || !p.isNameToken(p.tokens[index]) {
			return false
		}
	}
	return index < len(p.tokens) && p.tokens[index].IsWord("SET")
}

func nextSignificantToken(tokens []Token, index int) int {
	for index < len(tokens) && tokens[index].Kind == TokenComment {
		index++
	}
	return index
}

func (p *parser) parseParenthesizedQueryStatement() *SelectStmt {
	if p.startsNestedQueryFrom() {
		p.expectText("(", "before nested query")
		query := p.parseParenthesizedQueryStatement()
		p.expectText(")", "after nested query")
		query.Parenthesized = true
		query.ParenthesisDepth++
		if p.parseTrailingQueryClauses(query) {
			query.TailOutsideParen = true
		}
		return query
	}
	return p.parseParenthesizedSetQuery()
}

func (p *parser) parseCommand() Node {
	start := p.peek().Span.Start
	keyword := p.advance().Text
	for p.peek().Kind != TokenEOF && p.peek().Text != ";" {
		p.advance()
	}
	end := p.lastEnd
	if end < start {
		end = start
	}
	return &CommandStmt{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Keyword: strings.ToUpper(keyword), Raw: p.text[start:end]}
}

func (p *parser) parseCreateTable() Node {
	start := p.advance().Span.Start
	materialized := p.matchWord("MATERIALIZED")
	temporary := p.matchWord("TEMPORARY")
	if !p.matchWord("TABLE") {
		p.reportExpectedWord("TABLE", "after CREATE")
	}
	ifNotExists := false
	if p.matchWord("IF") {
		p.expectWord("NOT", "after IF in CREATE TABLE")
		p.expectWord("EXISTS", "after IF NOT in CREATE TABLE")
		ifNotExists = true
	}
	name, ok := p.parseNameParts()
	if !ok {
		p.reportExpectedIdentifier("after CREATE TABLE")
	}
	end := p.lastEnd
	var tail string
	if p.peek().Kind != TokenEOF && p.peek().Text != ";" {
		tailStart := p.peek().Span.Start
		for p.peek().Kind != TokenEOF && p.peek().Text != ";" {
			end = p.advance().Span.End
		}
		tail = p.text[tailStart:end]
	}
	return &CreateTableStmt{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Materialized: materialized, Temporary: temporary, IfNotExists: ifNotExists, Name: name, Tail: tail}
}

func (p *parser) parseInsert() Node {
	start := p.advance().Span.Start
	p.expectWord("INTO", "after INSERT")
	table, ok := p.parseNameParts()
	if !ok {
		p.reportExpectedIdentifier("after INSERT INTO")
	}
	var columns []Identifier
	if p.matchText("(") {
		columns = p.parseIdentifierList("INSERT columns")
		p.expectText(")", "to close INSERT columns")
	}
	stmt := &InsertStmt{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Table: table, Columns: columns}
	if p.matchWord("VALUES") {
		for {
			if !p.matchText("(") {
				p.expectText("(", "before INSERT VALUES row")
				break
			}
			row := p.parseExpressionList("INSERT VALUES")
			p.expectText(")", "to close INSERT VALUES row")
			stmt.Values = append(stmt.Values, row)
			if !p.matchText(",") || p.peek().Text != "(" {
				break
			}
		}
	} else if p.peek().IsWord("SELECT") || p.peek().IsWord("WITH") {
		stmt.Query = p.parseSelect()
	}
	p.captureStatementTail(&stmt.nodeBase, &stmt.Tail)
	return stmt
}

func (p *parser) parseUpdate() Node {
	start := p.advance().Span.Start
	table, ok := p.parseNameParts()
	if !ok {
		p.reportExpectedIdentifier("after UPDATE")
	}
	p.expectWord("SET", "after UPDATE table")
	stmt := &UpdateStmt{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Table: table}
	for {
		assignmentStart := p.peek().Span.Start
		target, ok := p.parseNameParts()
		if !ok {
			p.reportExpectedIdentifier("in UPDATE assignment")
			break
		}
		p.expectText("=", "in UPDATE assignment")
		value := p.parseRequiredExpr("after UPDATE assignment")
		stmt.Assignments = append(stmt.Assignments, Assignment{Target: target, Value: value, Span: Span{Start: assignmentStart, End: value.SourceSpan().End}})
		if !p.matchText(",") {
			break
		}
	}
	if p.matchWord("WHERE") {
		stmt.Where = p.parseRequiredExpr("after UPDATE WHERE")
	}
	p.captureStatementTail(&stmt.nodeBase, &stmt.Tail)
	return stmt
}

func (p *parser) parseDelete() Node {
	start := p.advance().Span.Start
	p.expectWord("FROM", "after DELETE")
	table, ok := p.parseNameParts()
	if !ok {
		p.reportExpectedIdentifier("after DELETE FROM")
	}
	stmt := &DeleteStmt{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Table: table}
	if p.matchWord("WHERE") {
		stmt.Where = p.parseRequiredExpr("after DELETE WHERE")
	}
	p.captureStatementTail(&stmt.nodeBase, &stmt.Tail)
	return stmt
}

func (p *parser) captureStatementTail(base *nodeBase, tail *string) {
	if p.peek().Kind == TokenEOF || p.peek().Text == ";" {
		return
	}
	start := p.peek().Span.Start
	end := start
	for p.peek().Kind != TokenEOF && p.peek().Text != ";" {
		end = p.advance().Span.End
	}
	*tail = p.text[start:end]
	if end > base.span.End {
		base.span.End = end
	}
}

func (p *parser) parseExpressionStatement() Node {
	start := p.peek().Span.Start
	expr := p.parseExpression(0)
	end := p.lastEnd
	stmt := &ExpressionStmt{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Expr: expr}
	if p.matchWord("AS") {
		if p.matchText("(") {
			stmt.AliasColumns = p.parseIdentifierList("expression alias columns")
			p.expectText(")", "to close expression alias columns")
		} else if alias, ok := p.parseIdentifier(false); ok {
			stmt.Alias = &alias
		} else {
			p.reportExpectedIdentifier("after AS")
		}
		stmt.nodeBase.span.End = p.lastEnd
	}
	if p.peek().Kind != TokenEOF && p.peek().Text != ";" {
		p.report(Diagnostic{
			Severity: SeverityError,
			Code:     "PARSE_UNEXPECTED_TOKEN",
			Message:  fmt.Sprintf("unexpected %s after expression", p.peek().Description()),
			Span:     p.peek().Span,
			Found:    p.peek().Kind,
			Recovery: RecoverySynchronized,
		})
		p.synchronizeStatement()
	}
	return stmt
}

func (p *parser) parseUnknownStatement() Node {
	start := p.peek().Span.Start
	var tokens []Token
	for p.peek().Kind != TokenEOF && p.peek().Text != ";" {
		tokens = append(tokens, p.advance())
	}
	end := p.lastEnd
	if end < start {
		end = start
	}
	p.report(Diagnostic{
		Severity: SeverityError,
		Code:     "PARSE_UNSUPPORTED_STATEMENT",
		Message:  "statement is not supported by the current parser",
		Span:     Span{Start: start, End: end},
		Recovery: RecoverySynchronized,
	})
	return &UnknownStmt{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Tokens: tokens, Reason: "unsupported statement"}
}

func (p *parser) parseSelect() *SelectStmt {
	start := p.peek().Span.Start
	if !p.enter() {
		return &SelectStmt{nodeBase: nodeBase{span: Span{Start: start, End: start}}}
	}
	defer p.leave()

	stmt := &SelectStmt{}
	p.recordNode()
	if p.peek().IsWord("WITH") {
		stmt.With = p.parseCTEs()
	}
	if !p.matchWord("SELECT") {
		p.reportExpectedWord("SELECT", "at the start of a query")
	}
	if p.matchWord("DISTINCT") {
		stmt.Distinct = true
		if p.matchWord("ON") {
			p.expectText("(", "after DISTINCT ON")
			stmt.DistinctOn = p.parseExpressionList("DISTINCT ON")
			p.expectText(")", "to close DISTINCT ON")
		}
	} else {
		// ALL is the default but accepting it keeps the parser's cursor in the
		// right place for dialects that spell it explicitly.
		if p.peek().IsWord("ALL") && !p.peekTextAfter(".") {
			p.matchWord("ALL")
		}
	}
	if p.options.Dialect == DialectTSQL && p.matchWord("TOP") {
		if p.matchText("(") {
			stmt.Top = p.parseRequiredExpr("inside TOP")
			p.expectText(")", "after TOP count")
		} else {
			stmt.Top = p.parseRequiredExpr("after TOP")
		}
	}
	stmt.Projections = p.parseSelectList()

	if p.matchWord("FROM") {
		stmt.From = p.parseFromClause()
	}
	if p.matchWord("CONNECT") {
		p.expectWord("BY", "after CONNECT")
		stmt.ConnectBy = p.parseRequiredExpr("after CONNECT BY")
	}
	if p.matchWord("WHERE") {
		stmt.Where = p.parseRequiredExpr("after WHERE")
	}
	if p.matchWord("GROUP") {
		if !p.matchWord("BY") {
			p.reportExpectedWord("BY", "after GROUP")
		}
		stmt.GroupBy = p.parseGroupByList()
	}
	if p.matchWord("HAVING") {
		stmt.Having = p.parseRequiredExpr("after HAVING")
	}
	if p.matchWord("QUALIFY") {
		stmt.Qualify = p.parseRequiredExpr("after QUALIFY")
	}
	if p.matchWord("WINDOW") {
		stmt.Windows = p.parseNamedWindows()
	}
	if p.matchWord("SORT") {
		if !p.matchWord("BY") {
			p.reportExpectedWord("BY", "after SORT")
		}
		stmt.SortBy = p.parseOrderList()
	}
	if p.matchWord("ORDER") {
		if !p.matchWord("BY") {
			p.reportExpectedWord("BY", "after ORDER")
		}
		stmt.OrderBy = p.parseOrderList()
	}
	if p.matchWord("LIMIT") {
		stmt.Limit = p.parseRequiredExpr("after LIMIT")
	}
	if p.matchWord("OFFSET") {
		stmt.Offset = p.parseRequiredExpr("after OFFSET")
	}
	if p.matchWord("FETCH") {
		fetch := &FetchClause{Next: p.matchWord("NEXT")}
		if !fetch.Next {
			p.matchWord("FIRST")
		}
		if !p.peek().IsWord("ROW") && !p.peek().IsWord("ROWS") {
			fetch.Count = p.parseRequiredExpr("after FETCH FIRST/NEXT")
		}
		fetch.Percent = p.matchWord("PERCENT")
		p.matchWord("ROW")
		p.matchWord("ROWS")
		if p.matchWord("WITH") {
			if p.matchWord("TIES") {
				fetch.WithTies = true
			} else {
				p.reportExpectedWord("TIES", "after FETCH WITH")
			}
		} else {
			p.matchWord("ONLY")
		}
		stmt.Fetch = fetch
	}
	if operator, ok := p.matchSetOperator(); ok {
		stmt.SetOperator = operator
		stmt.SetAll = p.matchWord("ALL")
		if p.matchText("(") {
			stmt.SetRight = p.parseSelect()
			stmt.SetRight.Parenthesized = true
			stmt.SetRight.ParenthesisDepth++
			p.expectText(")", "after set-operation query")
		} else {
			stmt.SetRight = p.parseSelect()
		}
		p.parseTrailingQueryClauses(stmt)
	}

	end := p.lastEnd
	if end < start {
		end = start
	}
	stmt.nodeBase.span = Span{Start: start, End: end}
	return stmt
}

func (p *parser) parseTrailingQueryClauses(stmt *SelectStmt) bool {
	parsed := false
	if p.matchWord("ORDER") {
		parsed = true
		if !p.matchWord("BY") {
			p.reportExpectedWord("BY", "after ORDER")
		}
		stmt.OrderBy = p.parseOrderList()
	}
	if p.matchWord("LIMIT") {
		parsed = true
		stmt.Limit = p.parseRequiredExpr("after LIMIT")
	}
	if p.matchWord("OFFSET") {
		parsed = true
		stmt.Offset = p.parseRequiredExpr("after OFFSET")
	}
	if p.matchWord("FETCH") {
		parsed = true
		fetch := &FetchClause{Next: p.matchWord("NEXT")}
		if !fetch.Next {
			p.matchWord("FIRST")
		}
		if !p.peek().IsWord("ROW") && !p.peek().IsWord("ROWS") {
			fetch.Count = p.parseRequiredExpr("after FETCH FIRST/NEXT")
		}
		fetch.Percent = p.matchWord("PERCENT")
		p.matchWord("ROW")
		p.matchWord("ROWS")
		if p.matchWord("WITH") {
			if p.matchWord("TIES") {
				fetch.WithTies = true
			}
		} else {
			p.matchWord("ONLY")
		}
		stmt.Fetch = fetch
	}
	return parsed
}

func (p *parser) parseGroupByList() []Expr {
	var expressions []Expr
	for {
		if p.isGroupingStart() {
			expressions = append(expressions, p.parseGroupingExpr())
		} else if p.isClauseBoundary() || p.peek().Text == ")" {
			expressions = append(expressions, p.missingExpr("in GROUP BY"))
			break
		} else {
			expressions = append(expressions, p.parseExpression(0))
		}
		if !p.matchText(",") {
			break
		}
		if p.isClauseBoundary() || p.peek().Text == ")" {
			break
		}
	}
	return expressions
}

func (p *parser) isGroupingStart() bool {
	if p.peek().IsWord("CUBE") || p.peek().IsWord("ROLLUP") {
		return p.peekTextAfter("(")
	}
	if !p.peek().IsWord("GROUPING") {
		return false
	}
	index := p.pos + 1
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	if index >= len(p.tokens) || !p.tokens[index].IsWord("SETS") {
		return false
	}
	index++
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	return index < len(p.tokens) && p.tokens[index].Text == "("
}

func (p *parser) parseGroupingExpr() Expr {
	start := p.advance()
	name := strings.ToUpper(start.Text)
	if start.IsWord("GROUPING") {
		p.expectWord("SETS", "after GROUPING")
		name += " SETS"
	}
	open := p.peek()
	p.expectText("(", "after "+name)
	var args []Expr
	for p.peek().Kind != TokenEOF && p.peek().Text != ")" {
		args = append(args, p.parseExpression(0))
		if !p.matchText(",") {
			break
		}
	}
	p.expectText(")", "to close "+name)
	return &GroupingExpr{nodeBase: nodeBase{span: Span{Start: start.Span.Start, End: p.lastEnd}}, Name: name, Args: args, SpaceBeforeParen: open.Span.Start > start.Span.End}
}

func (p *parser) matchSetOperator() (string, bool) {
	for _, operator := range []string{"UNION", "INTERSECT", "EXCEPT"} {
		if p.matchWord(operator) {
			return operator, true
		}
	}
	return "", false
}

func (p *parser) parseCTEs() []CTE {
	start := p.advance().Span.Start // WITH
	recursive := p.matchWord("RECURSIVE")
	var ctes []CTE
	for {
		name, ok := p.parseIdentifier(false)
		if !ok {
			p.reportExpectedIdentifier("after WITH")
			break
		}
		cteStart := name.Span.Start
		var columns []Identifier
		if p.matchText("(") {
			columns = p.parseIdentifierList("CTE column list")
			p.expectText(")", "to close the CTE column list")
		}
		p.expectWord("AS", "after CTE name")
		p.expectText("(", "before CTE query")
		var query *SelectStmt
		if p.peek().IsWord("SELECT") || p.peek().IsWord("WITH") {
			query = p.parseSelect()
		} else if p.peek().Text == "(" && (p.queryStartsAfterParen() || p.startsNestedQueryFrom()) {
			query = p.parseParenthesizedQueryStatement()
		} else {
			p.reportExpectedQuery("inside CTE")
		}
		end := p.lastEnd
		if p.matchText(")") {
			end = p.lastEnd
		} else {
			p.report(Diagnostic{
				Severity: SeverityError,
				Code:     "PARSE_UNCLOSED_PAREN",
				Message:  "unclosed CTE query; expected )",
				Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
				Found:    p.peek().Kind,
				Recovery: RecoveryInserted,
			})
		}
		ctes = append(ctes, CTE{
			Name:      name,
			Columns:   columns,
			Query:     query,
			Recursive: recursive,
			Span:      Span{Start: cteStart, End: end},
		})
		if !p.matchText(",") {
			break
		}
		if p.peek().IsWord("SELECT") || p.peek().IsWord("WITH") {
			break
		}
	}
	_ = start
	return ctes
}

func (p *parser) parseSelectList() []SelectItem {
	var items []SelectItem
	for {
		if p.peek().Text == "," {
			// Consume an extra separator so malformed lists always make
			// progress, but keep parsing the following item.
			p.reportExpectedExpression("in SELECT list")
			p.advance()
			continue
		}
		if p.isSelectListBoundary() {
			if len(items) == 0 {
				items = append(items, p.missingSelectItem())
			}
			break
		}

		start := p.peek().Span.Start
		expr := p.parseExpression(0)
		item := SelectItem{Expr: expr, Span: Span{Start: start, End: expr.SourceSpan().End}}
		if p.matchWord("AS") {
			if alias, ok := p.parseIdentifier(false); ok {
				item.Alias = &alias
				item.Span.End = alias.Span.End
			} else {
				p.reportExpectedIdentifier("after AS")
			}
		} else if p.canStartBareAlias() && !p.isWindowClauseStart() {
			alias, _ := p.parseIdentifier(false)
			item.Alias = &alias
			item.Span.End = alias.Span.End
		}
		item.Except, item.Replace = p.parseSelectItemModifiers()
		item.Span.End = maxInt(item.Span.End, p.lastEnd)
		items = append(items, item)

		if !p.matchText(",") {
			break
		}
		// Polyglot accepts a trailing projection comma before a clause.
		// Keep that behavior as a dialect policy point rather than emitting
		// a noisy diagnostic while the user is still typing.
		if p.isSelectListBoundary() {
			break
		}
	}
	return items
}

func (p *parser) parseSelectItemModifiers() ([]Expr, []SelectItem) {
	var except []Expr
	var replace []SelectItem
	if p.peek().IsWord("EXCEPT") && p.peekTextAfter("(") {
		p.advance()
		p.expectText("(", "after EXCEPT")
		for p.peek().Kind != TokenEOF && p.peek().Text != ")" {
			except = append(except, p.parseExpression(0))
			if !p.matchText(",") {
				break
			}
		}
		p.expectText(")", "to close EXCEPT")
	}
	if p.peek().IsWord("REPLACE") && p.peekTextAfter("(") {
		p.advance()
		p.expectText("(", "after REPLACE")
		for p.peek().Kind != TokenEOF && p.peek().Text != ")" {
			start := p.peek().Span.Start
			expr := p.parseExpression(0)
			item := SelectItem{Expr: expr, Span: Span{Start: start, End: expr.SourceSpan().End}}
			if p.matchWord("AS") {
				if alias, ok := p.parseIdentifier(false); ok {
					item.Alias = &alias
					item.Span.End = alias.Span.End
				} else {
					p.reportExpectedIdentifier("after AS in REPLACE")
				}
			}
			replace = append(replace, item)
			if !p.matchText(",") {
				break
			}
		}
		p.expectText(")", "to close REPLACE")
	}
	return except, replace
}

func (p *parser) missingSelectItem() SelectItem {
	expr := p.missingExpr("in SELECT list")
	return SelectItem{Expr: expr, Span: expr.SourceSpan()}
}

func (p *parser) parseFromClause() []TableExpr {
	var tables []TableExpr
	for {
		if p.isClauseBoundary() {
			p.reportExpectedTable("after FROM")
			break
		}
		table := p.parseTableExpr()
		if table != nil {
			tables = append(tables, *table)
		}
		if !p.matchText(",") {
			break
		}
		if p.isClauseBoundary() {
			p.reportExpectedTable("after comma in FROM")
			break
		}
	}
	return tables
}

func (p *parser) parseTableExpr() *TableExpr {
	start := p.peek().Span.Start
	if !p.enter() {
		return nil
	}
	defer p.leave()

	primary := p.parseFromItem()
	if primary == nil {
		return nil
	}
	table := &TableExpr{Primary: primary}
	p.recordNode()
	table.Modifiers = p.parseTableModifiers()
	for p.isJoinStart() {
		joinStart := p.peek().Span.Start
		kind, joinText := p.parseJoinKind()
		right := p.parseFromItem()
		if right == nil {
			p.reportExpectedTable("after JOIN")
			break
		}
		join := JoinClause{Kind: kind, JoinText: joinText, Right: right}
		if p.matchWord("ON") {
			join.Condition = p.parseRequiredExpr("after JOIN ... ON")
		} else if p.matchWord("USING") {
			p.expectText("(", "after USING")
			join.Using = p.parseIdentifierList("USING")
			p.expectText(")", "to close USING")
		}
		join.nodeBase.span = Span{Start: joinStart, End: p.lastEnd}
		table.Joins = append(table.Joins, join)
	}
	for p.peek().IsWord("ON") || p.peek().IsWord("USING") {
		index := -1
		for i := range table.Joins {
			if table.Joins[i].Condition == nil && len(table.Joins[i].Using) == 0 {
				index = i
				break
			}
		}
		if index < 0 {
			break
		}
		if p.matchWord("ON") {
			table.Joins[index].Condition = p.parseRequiredExpr("after JOIN ... ON")
		} else {
			p.advance() // USING
			p.expectText("(", "after USING")
			table.Joins[index].Using = p.parseIdentifierList("USING")
			p.expectText(")", "to close USING")
		}
		table.Joins[index].Late = true
		table.Joins[index].nodeBase.span.End = p.lastEnd
	}
	for p.matchWord("LATERAL") {
		p.expectWord("VIEW", "after LATERAL")
		viewStart := p.lastEnd
		outer := p.matchWord("OUTER")
		expression := p.parseRequiredExpr("after LATERAL VIEW")
		var alias *Identifier
		aliasExplicit := false
		var columns []Identifier
		if p.matchWord("AS") {
			aliasExplicit = true
			columns = p.parseIdentifierList("LATERAL VIEW columns")
		} else if p.canStartBareAlias() {
			parsed, _ := p.parseIdentifier(false)
			alias = &parsed
			if p.matchWord("AS") {
				aliasExplicit = true
				columns = p.parseIdentifierList("LATERAL VIEW columns")
			}
		}
		table.LateralViews = append(table.LateralViews, LateralView{
			Expression:    expression,
			Alias:         alias,
			AliasExplicit: aliasExplicit,
			Outer:         outer,
			Columns:       columns,
			Span:          Span{Start: viewStart, End: maxInt(p.lastEnd, maxInt(aliasEnd(alias), identifierListEnd(columns)))},
		})
	}
	table.nodeBase.span = Span{Start: start, End: p.lastEnd}
	return table
}

func (p *parser) parseTableModifiers() []string {
	var modifiers []string
	for p.peek().IsWord("PIVOT") || p.peek().IsWord("UNPIVOT") {
		start := p.advance().Span.Start
		depth := 0
		end := p.lastEnd
		for p.peek().Kind != TokenEOF {
			tok := p.peek()
			if depth == 0 && (tok.IsWord("PIVOT") || tok.IsWord("UNPIVOT") || tok.Text == "," || p.isClauseBoundary()) {
				break
			}
			tok = p.advance()
			end = tok.Span.End
			if tok.Text == "(" {
				depth++
			} else if tok.Text == ")" && depth > 0 {
				depth--
			}
		}
		if p.peek().IsWord("AS") {
			p.advance()
			end = p.lastEnd
			if p.isNameToken(p.peek()) {
				end = p.advance().Span.End
			}
		}
		modifiers = append(modifiers, p.text[start:end])
	}
	return modifiers
}

func (p *parser) parseNamedWindows() []NamedWindow {
	var windows []NamedWindow
	for {
		name, ok := p.parseIdentifier(true)
		if !ok {
			p.reportExpectedIdentifier("in WINDOW clause")
			break
		}
		p.expectWord("AS", "after window name")
		start := name.Span.Start
		spec := p.parseWindowSpec()
		windows = append(windows, NamedWindow{Name: name, Spec: spec, Span: Span{Start: start, End: p.lastEnd}})
		if !p.matchText(",") {
			break
		}
	}
	return windows
}

func (p *parser) parseWindowSpec() WindowSpec {
	var spec WindowSpec
	p.expectText("(", "before window specification")
	if p.isNameToken(p.peek()) && !p.peek().IsWord("PARTITION") && !p.peek().IsWord("ORDER") && !p.peek().IsWord("ROWS") && !p.peek().IsWord("RANGE") && !p.peek().IsWord("GROUPS") {
		if base, ok := p.parseIdentifier(true); ok {
			spec.Base = &base
		}
	}
	if p.matchWord("PARTITION") {
		p.expectWord("BY", "after PARTITION")
		spec.PartitionBy = p.parseExpressionList("PARTITION BY")
	}
	if p.matchWord("ORDER") {
		p.expectWord("BY", "after ORDER")
		spec.OrderBy = p.parseOrderList()
	}
	if p.peek().IsWord("ROWS") || p.peek().IsWord("RANGE") || p.peek().IsWord("GROUPS") {
		start := p.peek().Span.Start
		end := start
		depth := 0
		for p.peek().Kind != TokenEOF {
			tok := p.peek()
			if tok.Text == ")" && depth == 0 {
				break
			}
			tok = p.advance()
			end = tok.Span.End
			if tok.Text == "(" {
				depth++
			} else if tok.Text == ")" && depth > 0 {
				depth--
			}
		}
		spec.Frame = p.text[start:end]
	}
	if !p.matchText(")") {
		p.report(Diagnostic{
			Severity: SeverityError,
			Code:     "PARSE_UNCLOSED_PAREN",
			Message:  "unclosed window specification; expected )",
			Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
			Found:    p.peek().Kind,
			Recovery: RecoveryInserted,
		})
	}
	return spec
}

func (p *parser) parseJoinKind() (JoinKind, string) {
	if p.matchWord("STRAIGHT_JOIN") {
		return JoinInner, "STRAIGHT_JOIN"
	}
	var words []string
	for p.peek().Kind != TokenEOF && !p.peek().IsWord("JOIN") {
		words = append(words, strings.ToUpper(p.advance().Text))
	}
	if p.matchWord("JOIN") {
		words = append(words, "JOIN")
	} else {
		p.reportExpectedWord("JOIN", "after JOIN type")
	}
	kind := JoinInner
	for _, word := range words {
		switch word {
		case "LEFT":
			kind = JoinLeft
		case "RIGHT":
			kind = JoinRight
		case "FULL":
			kind = JoinFull
		case "CROSS":
			kind = JoinCross
		}
	}
	return kind, strings.Join(words, " ")
}

func (p *parser) parseFromItem() FromItem {
	start := p.peek().Span.Start
	if p.peek().IsWord("LATERAL") {
		p.advance()
		if p.matchText("(") && (p.peek().IsWord("SELECT") || p.peek().IsWord("WITH")) {
			query := p.parseSelect()
			p.expectText(")", "after LATERAL subquery")
			alias := p.parseOptionalAlias()
			columns := p.parseFromAliasColumns()
			return &SubqueryFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(p.lastEnd, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Query: query, Alias: alias, Columns: columns, Lateral: true}
		}
		p.reportExpectedQuery("after LATERAL")
		return nil
	}
	if p.startsNestedQueryFrom() {
		raw, end := p.captureBalancedFrom()
		alias := p.parseOptionalAlias()
		return &RawFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, aliasEnd(alias))}}, Raw: raw, Alias: alias}
	}
	if p.matchText("(") {
		if p.peek().IsWord("VALUES") {
			depth := 1
			end := p.lastEnd
			for depth > 0 && p.peek().Kind != TokenEOF {
				tok := p.advance()
				end = tok.Span.End
				if tok.Text == "(" {
					depth++
				} else if tok.Text == ")" {
					depth--
				}
			}
			if depth > 0 {
				p.report(Diagnostic{
					Severity: SeverityError,
					Code:     "PARSE_UNCLOSED_PAREN",
					Message:  "unclosed VALUES FROM expression; expected )",
					Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
					Found:    p.peek().Kind,
					Recovery: RecoveryInserted,
				})
			}
			alias := p.parseOptionalAlias()
			columns := p.parseFromAliasColumns()
			return &RawFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Raw: p.text[start:end], Alias: alias, Columns: columns}
		}
		if p.peek().IsWord("SELECT") || p.peek().IsWord("WITH") {
			query := p.parseSelect()
			end := p.lastEnd
			if p.matchText(")") {
				end = p.lastEnd
			} else {
				p.report(Diagnostic{
					Severity: SeverityError,
					Code:     "PARSE_UNCLOSED_PAREN",
					Message:  "unclosed subquery; expected )",
					Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
					Found:    p.peek().Kind,
					Recovery: RecoveryInserted,
				})
			}
			alias := p.parseOptionalAlias()
			columns := p.parseFromAliasColumns()
			return &SubqueryFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Query: query, Alias: alias, Columns: columns}
		}
		var items []TableExpr
		for p.peek().Kind != TokenEOF && p.peek().Text != ")" {
			table := p.parseTableExpr()
			if table != nil {
				items = append(items, *table)
			}
			if !p.matchText(",") {
				break
			}
		}
		if !p.matchText(")") {
			p.report(Diagnostic{
				Severity: SeverityError,
				Code:     "PARSE_UNCLOSED_PAREN",
				Message:  "unclosed grouped FROM expression; expected )",
				Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
				Found:    p.peek().Kind,
				Recovery: RecoveryInserted,
			})
		}
		alias := p.parseOptionalAlias()
		columns := p.parseFromAliasColumns()
		return &GroupedFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(p.lastEnd, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Items: items, Alias: alias, Columns: columns}
	}

	parts, ok := p.parseNameParts()
	if !ok {
		return nil
	}
	if p.matchText("(") {
		if len(parts) == 1 && strings.EqualFold(parts[0].Text, "VALUES") {
			openStart := p.tokens[p.pos-1].Span.Start
			depth := 1
			end := p.lastEnd
			for depth > 0 && p.peek().Kind != TokenEOF {
				tok := p.advance()
				end = tok.Span.End
				if tok.Text == "(" {
					depth++
				} else if tok.Text == ")" {
					depth--
				}
			}
			if depth > 0 {
				p.report(Diagnostic{
					Severity: SeverityError,
					Code:     "PARSE_UNCLOSED_PAREN",
					Message:  "unclosed VALUES expression; expected )",
					Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
					Found:    p.peek().Kind,
					Recovery: RecoveryInserted,
				})
			}
			alias := p.parseOptionalAlias()
			columns := p.parseFromAliasColumns()
			return &TableFunctionFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Name: parts, RawArgs: p.text[openStart:end], Alias: alias, Columns: columns}
		}
		args := p.parseCallArguments()
		if len(parts) == 1 && strings.EqualFold(parts[0].Text, "UNNEST") && len(args) == 0 {
			p.report(Diagnostic{
				Severity: SeverityError,
				Code:     "PARSE_EXPECTED_EXPRESSION",
				Message:  "UNNEST requires at least one expression",
				Span:     Span{Start: start, End: p.lastEnd},
				Recovery: RecoveryNone,
			})
		}
		withOrdinality := false
		if p.matchWord("WITH") {
			withOrdinality = true
			p.expectWord("ORDINALITY", "after WITH in table function")
		}
		alias := p.parseOptionalAlias()
		columns := p.parseFromAliasColumns()
		return &TableFunctionFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(p.lastEnd, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Name: parts, Args: args, Alias: alias, Columns: columns, WithOrdinality: withOrdinality}
	}
	alias := p.parseOptionalAlias()
	columns := p.parseFromAliasColumns()
	sample := p.parseTableSample()
	return &TableName{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(p.lastEnd, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Parts: parts, Alias: alias, Columns: columns, Sample: sample}
}

func (p *parser) startsNestedQueryFrom() bool {
	index := p.pos
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	if index >= len(p.tokens) || p.tokens[index].Text != "(" {
		return false
	}
	depth := 0
	for index < len(p.tokens) && p.tokens[index].Text == "(" {
		depth++
		index++
		for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
			index++
		}
	}
	return depth >= 2 && index < len(p.tokens) && (p.tokens[index].IsWord("SELECT") || p.tokens[index].IsWord("WITH"))
}

func (p *parser) captureBalancedFrom() (string, int) {
	start := p.peek().Span.Start
	depth := 0
	end := start
	for p.peek().Kind != TokenEOF {
		tok := p.advance()
		end = tok.Span.End
		if tok.Text == "(" {
			depth++
		} else if tok.Text == ")" {
			depth--
			if depth == 0 {
				break
			}
		}
	}
	return p.text[start:end], end
}

func (p *parser) parseTableSample() *TableSample {
	if !p.matchWord("TABLESAMPLE") {
		return nil
	}
	start := p.peek().Span.Start
	if !p.matchText("(") {
		p.report(Diagnostic{
			Severity: SeverityError,
			Code:     "PARSE_EXPECTED_TOKEN",
			Message:  "expected ( after TABLESAMPLE",
			Span:     Span{Start: start, End: start},
			Found:    p.peek().Kind,
			Recovery: RecoveryInserted,
		})
		return &TableSample{}
	}
	depth := 1
	end := p.lastEnd
	for depth > 0 && p.peek().Kind != TokenEOF {
		tok := p.advance()
		end = tok.Span.End
		if tok.Text == "(" {
			depth++
		} else if tok.Text == ")" {
			depth--
		}
	}
	if depth > 0 {
		p.report(Diagnostic{
			Severity: SeverityError,
			Code:     "PARSE_UNCLOSED_PAREN",
			Message:  "unclosed TABLESAMPLE clause; expected )",
			Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
			Found:    p.peek().Kind,
			Recovery: RecoveryInserted,
		})
	}
	return &TableSample{Raw: p.text[start:end]}
}

func (p *parser) parseOptionalAlias() *Identifier {
	if p.matchWord("AS") {
		if alias, ok := p.parseIdentifier(true); ok {
			return &alias
		}
		p.reportExpectedIdentifier("after AS")
		return nil
	}
	if p.canStartBareAlias() {
		alias, _ := p.parseIdentifier(false)
		return &alias
	}
	return nil
}

func (p *parser) parseFromAliasColumns() []Identifier {
	if !p.matchText("(") {
		return nil
	}
	columns := p.parseIdentifierList("FROM alias columns")
	p.expectText(")", "to close FROM alias columns")
	return columns
}

func identifierListEnd(identifiers []Identifier) int {
	if len(identifiers) == 0 {
		return 0
	}
	return identifiers[len(identifiers)-1].Span.End
}

func (p *parser) parseExpressionList(context string) []Expr {
	var expressions []Expr
	for {
		if p.isClauseBoundary() || p.peek().Text == ")" {
			expressions = append(expressions, p.missingExpr("in "+context))
			break
		}
		expressions = append(expressions, p.parseExpression(0))
		if !p.matchText(",") {
			break
		}
		if p.isClauseBoundary() || p.peek().Text == ")" {
			break
		}
	}
	return expressions
}

func (p *parser) parseOrderList() []OrderItem {
	var items []OrderItem
	for {
		if p.isClauseBoundary() {
			items = append(items, OrderItem{Expr: p.missingExpr("in ORDER BY")})
			break
		}
		start := p.peek().Span.Start
		expr := p.parseExpression(0)
		item := OrderItem{Expr: expr, Span: Span{Start: start, End: expr.SourceSpan().End}}
		if p.matchWord("ASC") {
			item.Ascending = true
		} else if p.matchWord("DESC") {
			item.Descending = true
		}
		if p.matchWord("NULLS") {
			if p.matchWord("LAST") {
				item.NullsLast = true
			} else if !p.matchWord("FIRST") {
				p.reportExpectedWord("FIRST or LAST", "after NULLS")
			}
		}
		item.Span.End = p.lastEnd
		items = append(items, item)
		if !p.matchText(",") {
			break
		}
		if p.isClauseBoundary() {
			break
		}
	}
	return items
}

func (p *parser) parseRequiredExpr(context string) Expr {
	if p.isExpressionBoundary() {
		return p.missingExpr(context)
	}
	return p.parseExpression(0)
}

func (p *parser) parseExpression(minPrecedence int) Expr {
	if !p.enter() {
		return p.missingExpr("in expression")
	}
	defer p.leave()

	left := p.parsePrefix()
	left = p.parsePostfix(left)
	for {
		if p.peek().IsWord("NOT") && (p.peekWordAfter("NOT", "IN") || p.peekWordAfter("NOT", "BETWEEN") || p.peekWordAfter("NOT", "LIKE") || p.peekWordAfter("NOT", "ILIKE")) {
			if minPrecedence > 3 {
				break
			}
			p.advance()
			if p.peek().IsWord("IN") {
				left = p.parseIn(left, true)
			} else if p.peek().IsWord("BETWEEN") {
				left = p.parseBetween(left, true)
			} else {
				p.advance()
				right := p.parseExpression(4)
				binary := &BinaryExpr{nodeBase: nodeBase{span: Span{Start: left.SourceSpan().Start, End: right.SourceSpan().End}}, Left: left, Operator: "NOT LIKE", Right: right}
				p.parseLikeEscape(binary)
				left = binary
			}
			continue
		}
		if p.peek().IsWord("BETWEEN") {
			if minPrecedence > 3 {
				break
			}
			left = p.parseBetween(left, false)
			continue
		}
		if p.peek().IsWord("IN") {
			if minPrecedence > 3 {
				break
			}
			left = p.parseIn(left, false)
			continue
		}
		if p.peek().IsWord("IS") {
			if minPrecedence > 3 {
				break
			}
			left = p.parseIs(left)
			continue
		}
		if p.peekWords("AT", "TIME", "ZONE") {
			if minPrecedence > 4 {
				break
			}
			start := left.SourceSpan().Start
			p.advance()
			p.expectWord("TIME", "after AT")
			p.expectWord("ZONE", "after AT TIME")
			right := p.parseExpression(5)
			left = &BinaryExpr{
				nodeBase: nodeBase{span: Span{Start: start, End: right.SourceSpan().End}},
				Left:     left,
				Operator: "AT TIME ZONE",
				Right:    right,
			}
			p.recordNode()
			left = p.parsePostfix(left)
			continue
		}

		precedence, operator, ok := infixOperator(p.peek())
		if !ok || precedence < minPrecedence {
			break
		}
		p.advance()
		if (strings.EqualFold(operator, "LIKE") || strings.EqualFold(operator, "ILIKE") || strings.EqualFold(operator, "GLOB")) && p.matchWord("ANY") {
			operator += " ANY"
		}
		right := p.parseExpression(precedence + 1)
		binary := &BinaryExpr{
			nodeBase: nodeBase{span: Span{Start: left.SourceSpan().Start, End: right.SourceSpan().End}},
			Left:     left,
			Operator: operator,
			Right:    right,
		}
		if strings.EqualFold(operator, "LIKE") || strings.EqualFold(operator, "ILIKE") || strings.EqualFold(operator, "GLOB") {
			p.parseLikeEscape(binary)
		}
		p.recordNode()
		left = binary
		left = p.parsePostfix(left)
	}
	return left
}

func (p *parser) parseLikeEscape(expression *BinaryExpr) {
	if !p.matchWord("ESCAPE") {
		return
	}
	expression.Escape = p.parseRequiredExpr("after ESCAPE")
	expression.nodeBase.span.End = expression.Escape.SourceSpan().End
}

func (p *parser) parsePostfix(left Expr) Expr {
	for {
		if p.peek().Text == "<" && p.hasClosingAngle() {
			start := left.SourceSpan().Start
			p.advance()
			var arguments []Expr
			for p.peek().Kind != TokenEOF && p.peek().Text != ">" && p.peek().Text != ">>" {
				argumentStart := p.peek().Span.Start
				argument := p.parsePrefix()
				argument = p.parsePostfix(argument)
				arguments = append(arguments, argument)
				if !p.matchText(",") {
					if p.peek().Text != ">" && p.peek().Text != ">>" && p.peek().Kind != TokenEOF {
						end := p.consumeGenericRemainder()
						arguments[len(arguments)-1] = &RawExpr{
							nodeBase: nodeBase{span: Span{Start: argumentStart, End: end}},
							Raw:      strings.TrimSpace(p.text[argumentStart:end]),
						}
					}
					break
				}
			}
			if !p.matchGenericClose() {
				p.report(Diagnostic{
					Severity: SeverityError,
					Code:     "PARSE_EXPECTED_TOKEN",
					Message:  "expected > to close generic expression",
					Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
					Found:    p.peek().Kind,
					Recovery: RecoveryInserted,
				})
			}
			left = &GenericExpr{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Target: left, Arguments: arguments}
			p.recordNode()
			continue
		}
		if p.matchText("[") {
			start := left.SourceSpan().Start
			var low, high Expr
			var indices []Expr
			slice := false
			if p.peek().Text != ":" && p.peek().Text != "]" {
				low = p.parseExpression(0)
			}
			if p.matchText(":") {
				slice = true
				if p.peek().Text != "]" {
					high = p.parseExpression(0)
				}
			} else if low != nil && p.matchText(",") {
				indices = append(indices, low)
				for p.peek().Kind != TokenEOF && p.peek().Text != "]" {
					indices = append(indices, p.parseExpression(0))
					if !p.matchText(",") {
						break
					}
				}
			}
			if !p.matchText("]") {
				p.report(Diagnostic{
					Severity: SeverityError,
					Code:     "PARSE_UNCLOSED_BRACKET",
					Message:  "unclosed index expression; expected ]",
					Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
					Found:    p.peek().Kind,
					Recovery: RecoveryInserted,
				})
			}
			end := p.lastEnd
			left = &IndexExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Target: left, Low: low, High: high, Slice: slice, Indices: indices}
			p.recordNode()
			continue
		}
		if p.matchText(".") {
			field, ok := p.parseIdentifier(true)
			if !ok && p.peek().Kind == TokenNumber {
				tok := p.advance()
				field = Identifier{Text: tok.Text, Span: tok.Span}
				ok = true
			}
			if !ok && p.matchText("*") {
				field = Identifier{Text: "*", Span: p.tokens[p.pos-1].Span}
				ok = true
			}
			if !ok {
				p.reportExpectedIdentifier("after .")
				return left
			}
			left = &FieldExpr{nodeBase: nodeBase{span: Span{Start: left.SourceSpan().Start, End: field.Span.End}}, Target: left, Field: field}
			p.recordNode()
			continue
		}
		if p.peek().Kind == TokenNumber && strings.HasPrefix(p.peek().Text, ".") {
			tok := p.advance()
			field := Identifier{Text: tok.Text[1:], Span: tok.Span}
			left = &FieldExpr{nodeBase: nodeBase{span: Span{Start: left.SourceSpan().Start, End: tok.Span.End}}, Target: left, Field: field}
			p.recordNode()
			continue
		}
		if p.matchWord("OVER") {
			spec := p.parseWindowSpecPtr()
			left = &WindowedExpr{nodeBase: nodeBase{span: Span{Start: left.SourceSpan().Start, End: p.lastEnd}}, Expr: left, Over: *spec}
			p.recordNode()
			continue
		}
		if p.matchText("(") {
			args := p.parseCallArguments()
			left = &CallExpr{
				nodeBase: nodeBase{span: Span{Start: left.SourceSpan().Start, End: p.lastEnd}},
				Callee:   left,
				Args:     args,
			}
			p.recordNode()
			continue
		}
		return left
	}
}

func (p *parser) hasClosingAngle() bool {
	depth := 0
	for index := p.pos; index < len(p.tokens); index++ {
		tok := p.tokens[index]
		if tok.Kind == TokenComment {
			continue
		}
		switch tok.Text {
		case "<":
			depth++
		case ">":
			depth--
			if depth == 0 {
				return true
			}
		case ">>":
			depth -= 2
			if depth <= 0 {
				return true
			}
		case ";":
			return false
		}
	}
	return false
}

func (p *parser) matchGenericClose() bool {
	if p.matchText(">") {
		return true
	}
	if p.peek().Text != ">>" {
		return false
	}
	tok := p.tokens[p.pos]
	first := tok
	first.Text = ">"
	first.Span.End = first.Span.Start + 1
	second := tok
	second.Text = ">"
	second.Span.Start = second.Span.Start + 1
	second.Span.End = second.Span.Start + 1
	p.tokens = append(p.tokens, Token{})
	copy(p.tokens[p.pos+2:], p.tokens[p.pos+1:])
	p.tokens[p.pos] = first
	p.tokens[p.pos+1] = second
	return p.matchText(">")
}

func (p *parser) consumeGenericRemainder() int {
	depth := 0
	end := p.lastEnd
	for p.peek().Kind != TokenEOF {
		tok := p.peek()
		if (tok.Text == ">" || tok.Text == ">>") && depth == 0 {
			break
		}
		p.advance()
		end = tok.Span.End
		switch tok.Text {
		case "<":
			depth++
		case ">":
			depth--
		case ">>":
			depth -= 2
		}
	}
	return end
}

func (p *parser) parseBetween(left Expr, not bool) Expr {
	start := left.SourceSpan().Start
	p.expectWord("BETWEEN", "in BETWEEN expression")
	symmetric := p.matchWord("SYMMETRIC")
	var low Expr
	if p.isExpressionBoundary() {
		low = p.missingExpr("after BETWEEN")
	} else {
		low = p.parseExpression(3)
	}
	if !p.matchWord("AND") {
		p.reportExpectedWord("AND", "between BETWEEN bounds")
	}
	var high Expr
	if p.isExpressionBoundary() {
		high = p.missingExpr("after BETWEEN ... AND")
	} else {
		high = p.parseExpression(3)
	}
	return &BetweenExpr{nodeBase: nodeBase{span: Span{Start: start, End: high.SourceSpan().End}}, Value: left, Not: not, Low: low, High: high, Symmetric: symmetric}
}

func (p *parser) parseIn(left Expr, not bool) Expr {
	start := left.SourceSpan().Start
	p.expectWord("IN", "in IN expression")
	p.expectText("(", "after IN")
	expr := &InExpr{Value: left, Not: not}
	if p.peek().IsWord("SELECT") || p.peek().IsWord("WITH") {
		expr.Query = p.parseSelect()
	} else if p.peek().Text == "(" && p.isParenthesizedSetQuery() {
		expr.Query = p.parseParenthesizedSetQuery()
	} else if p.peek().Text == ")" {
		p.reportExpectedExpression("inside IN")
	} else {
		expr.Items = p.parseExpressionList("IN")
	}
	if p.matchText(")") {
		expr.nodeBase.span = Span{Start: start, End: p.lastEnd}
	} else {
		expr.nodeBase.span = Span{Start: start, End: p.lastEnd}
		p.report(Diagnostic{
			Severity: SeverityError,
			Code:     "PARSE_UNCLOSED_PAREN",
			Message:  "unclosed IN expression; expected )",
			Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
			Found:    p.peek().Kind,
			Recovery: RecoveryInserted,
		})
	}
	p.recordNode()
	return expr
}

func (p *parser) queryStartsAfterParen() bool {
	index := p.pos
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	if index >= len(p.tokens) || p.tokens[index].Text != "(" {
		return false
	}
	index++
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	return index < len(p.tokens) && (p.tokens[index].IsWord("SELECT") || p.tokens[index].IsWord("WITH"))
}

func (p *parser) isParenthesizedSetQuery() bool {
	if !p.queryStartsAfterParen() {
		return false
	}
	index := p.pos
	depth := 0
	for index < len(p.tokens) {
		tok := p.tokens[index]
		if tok.Kind == TokenComment {
			index++
			continue
		}
		if tok.Text == "(" {
			depth++
		} else if tok.Text == ")" {
			depth--
			if depth == 0 {
				index++
				for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
					index++
				}
				if index >= len(p.tokens) {
					return true
				}
				switch {
				case p.tokens[index].Text == ")":
					return true
				case p.tokens[index].IsWord("UNION"), p.tokens[index].IsWord("INTERSECT"), p.tokens[index].IsWord("EXCEPT"):
					return true
				case p.tokens[index].IsWord("ORDER"), p.tokens[index].IsWord("LIMIT"), p.tokens[index].IsWord("OFFSET"), p.tokens[index].IsWord("FETCH"):
					return true
				default:
					return false
				}
			}
		}
		index++
	}
	return false
}

func (p *parser) parseParenthesizedSetQuery() *SelectStmt {
	p.expectText("(", "before subquery")
	left := p.parseSelect()
	p.expectText(")", "after subquery")
	setOperation := false
	for {
		operator, ok := p.matchSetOperator()
		if !ok {
			break
		}
		setOperation = true
		all := p.matchWord("ALL")
		var right *SelectStmt
		rightParenthesized := false
		if p.matchText("(") {
			rightParenthesized = true
			right = p.parseSelect()
			p.expectText(")", "after set-operation query")
		} else {
			right = p.parseSelect()
		}
		if left.SetOperator != "" {
			if rightParenthesized {
				right.Parenthesized = true
				right.ParenthesisDepth++
			}
			left = &SelectStmt{
				nodeBase:      nodeBase{span: Span{Start: left.SourceSpan().Start, End: right.SourceSpan().End}},
				SetLeft:       left,
				SetOperator:   operator,
				SetAll:        all,
				SetRight:      right,
				SetLeftParen:  true,
				SetRightParen: rightParenthesized,
			}
		} else {
			left.SetOperator = operator
			left.SetAll = all
			left.SetRight = right
			left.SetLeftParen = true
			left.SetRightParen = rightParenthesized
			if rightParenthesized {
				right.Parenthesized = true
				right.ParenthesisDepth++
			}
			left.nodeBase.span.End = right.SourceSpan().End
		}
	}
	if p.parseTrailingQueryClauses(left) {
		left.TailOutsideParen = true
	}
	if !setOperation {
		left.Parenthesized = true
		left.ParenthesisDepth++
	}
	return left
}

func (p *parser) parseIs(left Expr) Expr {
	start := left.SourceSpan().Start
	p.advance() // IS
	not := p.matchWord("NOT")
	operator := "IS"
	if not {
		operator = "IS NOT"
	}
	if p.matchWord("DISTINCT") {
		operator += " DISTINCT"
		if !p.matchWord("FROM") {
			p.reportExpectedWord("FROM", "after IS DISTINCT")
		}
		operator += " FROM"
	}
	if p.isExpressionBoundary() {
		return &IsExpr{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Value: left, Operator: operator, Right: p.missingExpr("after " + operator)}
	}
	right := p.parseExpression(4)
	p.recordNode()
	return &IsExpr{nodeBase: nodeBase{span: Span{Start: start, End: right.SourceSpan().End}}, Value: left, Operator: operator, Right: right}
}

func (p *parser) parsePrefix() Expr {
	tok := p.peek()
	if tok.IsWord("CASE") && !p.peekTextAfter(".") {
		return p.parseCase()
	}
	if p.isGroupingStart() {
		return p.parseGroupingExpr()
	}
	if tok.IsWord("NOT") || tok.Text == "+" || tok.Text == "-" || tok.Text == "~" {
		p.advance()
		right := p.parseExpression(7)
		return &UnaryExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: right.SourceSpan().End}}, Operator: tok.Text, Expr: right}
	}
	if tok.Text == "(" {
		p.advance()
		if p.peek().Text == "(" && p.isParenthesizedSetQuery() {
			query := p.parseParenthesizedQueryStatement()
			p.expectText(")", "after nested scalar subquery")
			return &SubqueryExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Query: query}
		}
		if p.peek().IsWord("SELECT") || p.peek().IsWord("WITH") {
			query := p.parseSelect()
			if !p.matchText(")") {
				p.report(Diagnostic{
					Severity: SeverityError,
					Code:     "PARSE_UNCLOSED_PAREN",
					Message:  "unclosed scalar subquery; expected )",
					Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
					Found:    p.peek().Kind,
					Recovery: RecoveryInserted,
				})
			}
			return &SubqueryExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Query: query}
		}
		var inner Expr
		if p.peek().Text == ")" {
			inner = &TupleExpr{nodeBase: nodeBase{span: Span{Start: p.peek().Span.Start, End: p.peek().Span.Start}}}
			p.recordNode()
		} else {
			inner = p.parseRequiredExpr("inside parentheses")
		}
		inner = p.parseExpressionAlias(inner)
		if p.matchText(",") {
			items := []Expr{inner}
			for {
				if p.peek().Text == ")" {
					p.reportExpectedExpression("after comma in tuple")
					break
				}
				item := p.parseRequiredExpr("after comma in tuple")
				items = append(items, p.parseExpressionAlias(item))
				if !p.matchText(",") {
					break
				}
			}
			inner = &TupleExpr{nodeBase: nodeBase{span: Span{Start: inner.SourceSpan().Start, End: p.lastEnd}}, Items: items}
			p.recordNode()
		}
		if !p.matchText(")") {
			p.report(Diagnostic{
				Severity: SeverityError,
				Code:     "PARSE_UNCLOSED_PAREN",
				Message:  "unclosed parenthesized expression; expected )",
				Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
				Found:    p.peek().Kind,
				Recovery: RecoveryInserted,
			})
		}
		return &ParenthesizedExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: maxInt(p.lastEnd, tok.Span.End)}}, Expr: inner}
	}
	if tok.Text == "*" {
		p.advance()
		return &StarExpr{nodeBase: nodeBase{span: tok.Span}}
	}
	switch tok.Kind {
	case TokenString, TokenUnterminatedString:
		p.advance()
		return &LiteralExpr{nodeBase: nodeBase{span: tok.Span}, KindValue: LiteralString, Raw: tok.Text}
	case TokenNumber:
		p.advance()
		return &LiteralExpr{nodeBase: nodeBase{span: tok.Span}, KindValue: LiteralNumber, Raw: tok.Text}
	case TokenParameter:
		p.advance()
		return &LiteralExpr{nodeBase: nodeBase{span: tok.Span}, KindValue: LiteralParameter, Raw: tok.Text}
	}
	if tok.IsWord("NULL") || tok.IsWord("TRUE") || tok.IsWord("FALSE") {
		p.advance()
		kind := LiteralNull
		if !tok.IsWord("NULL") {
			kind = LiteralBoolean
		}
		return &LiteralExpr{nodeBase: nodeBase{span: tok.Span}, KindValue: kind, Raw: tok.Text}
	}
	if p.isNameToken(tok) && (!p.isStructuralKeyword(tok) || tok.IsWord("END") || tok.IsWord("LEFT") || tok.IsWord("RIGHT") || tok.IsWord("OVERLAPS") || (tok.IsWord("REPLACE") && p.peekTextAfter("("))) {
		parts, _ := p.parseNameParts()
		if len(parts) == 1 && strings.EqualFold(parts[0].Text, "EXISTS") && p.matchText("(") {
			var query *SelectStmt
			if p.peek().IsWord("SELECT") || p.peek().IsWord("WITH") {
				query = p.parseSelect()
			} else {
				p.reportExpectedQuery("inside EXISTS")
			}
			p.expectText(")", "to close EXISTS")
			return &ExistsExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Query: query}
		}
		if len(parts) == 1 && (strings.EqualFold(parts[0].Text, "ANY") || strings.EqualFold(parts[0].Text, "ALL")) && p.peek().Text == "(" && p.queryStartsAfterParen() && p.matchText("(") {
			var query *SelectStmt
			if p.peek().IsWord("SELECT") || p.peek().IsWord("WITH") {
				query = p.parseSelect()
			} else {
				p.reportExpectedQuery("inside " + parts[0].Text)
			}
			p.expectText(")", "to close "+parts[0].Text)
			return &QuantifiedExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Keyword: strings.ToUpper(parts[0].Text), Query: query}
		}
		if len(parts) == 1 && (strings.EqualFold(parts[0].Text, "CAST") || strings.EqualFold(parts[0].Text, "TRY_CAST")) && p.matchText("(") {
			return p.parseCast(tok, parts[0].Text)
		}
		if len(parts) == 1 && strings.EqualFold(parts[0].Text, "EXTRACT") && p.matchText("(") {
			field := p.parseRequiredExpr("inside EXTRACT")
			p.expectWord("FROM", "inside EXTRACT")
			source := p.parseRequiredExpr("after EXTRACT FROM")
			p.expectText(")", "to close EXTRACT")
			return &ExtractExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Field: field, Source: source}
		}
		if len(parts) == 1 && strings.EqualFold(parts[0].Text, "INTERVAL") && p.intervalValueStart() {
			return p.parseInterval(tok, parts)
		}
		if p.isTypedLiteralName(parts) {
			var parameters []Expr
			if p.matchText("(") {
				parameters = p.parseCallArguments()
			}
			qualifiers := p.parseTypeQualifiers()
			if p.peek().Kind == TokenString || p.peek().Kind == TokenUnterminatedString {
				valueToken := p.advance()
				value := &LiteralExpr{nodeBase: nodeBase{span: valueToken.Span}, KindValue: LiteralString, Raw: valueToken.Text}
				return &TypedLiteralExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: valueToken.Span.End}}, TypeName: parts, Parameters: parameters, Qualifiers: qualifiers, Value: value}
			}
			if len(parameters) > 0 || len(qualifiers) > 0 {
				return &TypedLiteralExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, TypeName: parts, Parameters: parameters, Qualifiers: qualifiers}
			}
		}
		if len(parts) == 1 && strings.EqualFold(parts[0].Text, "JSON_OBJECT") && p.peek().Text == "(" && p.jsonObjectNeedsRawArgs() {
			open := p.advance()
			depth := 1
			end := open.Span.End
			for depth > 0 && p.peek().Kind != TokenEOF {
				token := p.advance()
				end = token.Span.End
				if token.Text == "(" {
					depth++
				} else if token.Text == ")" {
					depth--
				}
			}
			if depth > 0 {
				p.report(Diagnostic{
					Severity: SeverityError,
					Code:     "PARSE_UNCLOSED_PAREN",
					Message:  "unclosed JSON_OBJECT call; expected )",
					Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
					Found:    p.peek().Kind,
					Recovery: RecoveryInserted,
				})
			}
			return &FunctionCallExpr{
				nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: end}},
				Name:     parts,
				RawArgs:  p.text[open.Span.Start:end],
			}
		}
		if p.matchText("(") {
			distinct := p.matchWord("DISTINCT")
			args, orderBy := p.parseFunctionArguments()
			function := &FunctionCallExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Name: parts, Distinct: distinct, Args: args, OrderBy: orderBy}
			p.validateFunctionArguments(function)
			if p.matchWord("WITHIN") {
				p.expectWord("GROUP", "after WITHIN in aggregate function")
				p.expectText("(", "before WITHIN GROUP order")
				p.expectWord("ORDER", "inside WITHIN GROUP")
				p.expectWord("BY", "after ORDER in WITHIN GROUP")
				function.WithinGroup = p.parseOrderList()
				p.expectText(")", "to close WITHIN GROUP")
			}
			if p.matchWord("FILTER") {
				p.expectText("(", "after FILTER")
				p.expectWord("WHERE", "inside FILTER")
				function.Filter = p.parseRequiredExpr("after FILTER WHERE")
				p.expectText(")", "to close FILTER")
			}
			if p.matchWord("IGNORE") {
				if p.matchWord("NULLS") {
					function.IgnoreNulls = true
				}
			} else if p.matchWord("RESPECT") {
				if p.matchWord("NULLS") {
					function.RespectNulls = true
				}
			}
			if p.matchWord("OVER") {
				function.Over = p.parseWindowSpecPtr()
				function.nodeBase.span.End = p.lastEnd
			}
			return function
		}
		return &IdentifierExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Parts: parts}
	}

	if tok.Kind == TokenEOF || p.isExpressionBoundary() {
		return p.missingExpr("in expression")
	}
	p.advance()
	p.report(Diagnostic{
		Severity: SeverityError,
		Code:     "PARSE_EXPECTED_EXPRESSION",
		Message:  fmt.Sprintf("unexpected %s; expected an expression", tok.Description()),
		Span:     tok.Span,
		Found:    tok.Kind,
		Recovery: RecoveryDeleted,
	})
	return &ErrorExpr{nodeBase: nodeBase{span: tok.Span}, Tokens: []Token{tok}, Message: "unexpected token in expression"}
}

func (p *parser) parseExpressionAlias(expression Expr) Expr {
	if !p.matchWord("AS") {
		return expression
	}
	if alias, ok := p.parseIdentifier(false); ok {
		p.recordNode()
		return &AliasExpr{nodeBase: nodeBase{span: Span{Start: expression.SourceSpan().Start, End: alias.Span.End}}, Expr: expression, Alias: alias}
	}
	p.reportExpectedIdentifier("after AS")
	return expression
}

func (p *parser) parseCast(start Token, keyword string) Expr {
	value := p.parseRequiredExpr("inside " + keyword)
	p.expectWord("AS", "inside "+keyword)
	typeExpr, suffix := p.parseCastType()
	p.expectText(")", "to close "+keyword)
	return &CastExpr{
		nodeBase:   nodeBase{span: Span{Start: start.Span.Start, End: p.lastEnd}},
		Keyword:    strings.ToUpper(keyword),
		Value:      value,
		Type:       typeExpr,
		TypeSuffix: suffix,
	}
}

func (p *parser) parseCastType() (Expr, []Identifier) {
	start := p.peek().Span.Start
	parts, ok := p.parseNameParts()
	if !ok {
		p.reportExpectedIdentifier("after AS in CAST")
		return p.missingExpr("after AS in CAST"), nil
	}
	var typeExpr Expr = &IdentifierExpr{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Parts: parts}
	if p.matchText("(") {
		args := p.parseCallArguments()
		typeExpr = &CallExpr{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Callee: typeExpr, Args: args}
	}
	typeExpr = p.parsePostfix(typeExpr)
	var suffix []Identifier
	for p.isNameToken(p.peek()) && !p.isStructuralKeyword(p.peek()) && !p.peek().IsWord("AS") && p.peek().Text != ")" {
		identifier, ok := p.parseIdentifier(true)
		if !ok {
			break
		}
		suffix = append(suffix, identifier)
	}
	return typeExpr, suffix
}

func (p *parser) intervalValueStart() bool {
	tok := p.peek()
	return tok.Kind == TokenString || tok.Kind == TokenUnterminatedString || tok.Kind == TokenNumber || tok.Text == "(" || tok.Text == "+" || tok.Text == "-" || (p.isNameToken(tok) && !p.isStructuralKeyword(tok) && !tok.IsWord("AS"))
}

func (p *parser) parseInterval(start Token, parts []Identifier) Expr {
	value := p.parsePrefix()
	value = p.parsePostfix(value)
	var qualifiers []Expr
	for p.isNameToken(p.peek()) && !p.isStructuralKeyword(p.peek()) && !p.peek().IsWord("AS") {
		qualifier := p.parsePrefix()
		qualifier = p.parsePostfix(qualifier)
		qualifiers = append(qualifiers, qualifier)
	}
	end := value.SourceSpan().End
	if len(qualifiers) > 0 {
		end = qualifiers[len(qualifiers)-1].SourceSpan().End
	}
	return &IntervalExpr{nodeBase: nodeBase{span: Span{Start: start.Span.Start, End: end}}, Value: value, Qualifiers: qualifiers}
}

func (p *parser) parseCase() Expr {
	start := p.advance().Span.Start
	var operand Expr
	if !p.peek().IsWord("WHEN") {
		if !p.isExpressionBoundary() {
			operand = p.parseExpression(0)
		}
	}
	var whens []CaseWhen
	for p.matchWord("WHEN") {
		whenStart := p.lastEnd
		condition := p.parseRequiredExpr("after WHEN")
		p.expectWord("THEN", "after CASE condition")
		value := p.parseRequiredExpr("after THEN")
		whens = append(whens, CaseWhen{Condition: condition, Result: value, Span: Span{Start: whenStart, End: value.SourceSpan().End}})
	}
	var elseExpr Expr
	if p.matchWord("ELSE") {
		elseExpr = p.parseRequiredExpr("after ELSE")
	}
	if !p.matchWord("END") {
		p.reportExpectedWord("END", "to close CASE expression")
	}
	return &CaseExpr{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Operand: operand, Whens: whens, Else: elseExpr}
}

func (p *parser) parseWindowSpecPtr() *WindowSpec {
	if p.peek().Text != "(" {
		if name, ok := p.parseIdentifier(true); ok {
			return &WindowSpec{Name: &name}
		}
	}
	spec := p.parseWindowSpec()
	return &spec
}

func (p *parser) parseTypeQualifiers() []string {
	var qualifiers []string
	if p.matchWord("WITH") || p.matchWord("WITHOUT") {
		qualifiers = append(qualifiers, strings.ToUpper(p.tokens[p.pos-1].Text))
		if p.matchWord("LOCAL") {
			qualifiers = append(qualifiers, "LOCAL")
		}
		if p.matchWord("TIME") {
			qualifiers = append(qualifiers, "TIME")
		}
		if p.matchWord("ZONE") {
			qualifiers = append(qualifiers, "ZONE")
		}
	}
	return qualifiers
}

func (p *parser) isTypedLiteralName(parts []Identifier) bool {
	if len(parts) != 1 {
		return false
	}
	switch strings.ToUpper(parts[0].Text) {
	case "DATE", "TIME", "TIMESTAMP", "DATETIME", "JSON", "INTERVAL", "UUID", "N":
		return true
	default:
		return false
	}
}

func (p *parser) jsonObjectNeedsRawArgs() bool {
	depth := 0
	content := false
	for index := p.pos; index < len(p.tokens); index++ {
		token := p.tokens[index]
		if token.Kind == TokenComment {
			continue
		}
		switch token.Text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return !content
			}
		case ":", "*":
			if depth == 1 {
				return true
			}
		default:
			if depth == 1 {
				content = true
			}
		}
	}
	return true
}

func (p *parser) parseCallArguments() []Expr {
	var args []Expr
	if p.matchText("*") {
		// A star argument is represented on the function node by the caller
		// only for future specialized handling; preserving it as an expression
		// keeps the generic parser composable.
		args = append(args, &StarExpr{nodeBase: nodeBase{span: p.tokens[p.pos-1].Span}})
		p.expectText(")", "to close function call")
		return args
	}
	for {
		if p.peek().Text == ")" {
			break
		}
		if p.peek().Text == "," {
			p.reportExpectedExpression("in function arguments")
			p.advance()
			continue
		}
		args = append(args, p.parseExpressionAlias(p.parseExpressionWithSet()))
		if !p.matchText(",") {
			break
		}
		if p.peek().Text == ")" {
			break
		}
	}
	if !p.matchText(")") {
		p.report(Diagnostic{
			Severity: SeverityError,
			Code:     "PARSE_UNCLOSED_PAREN",
			Message:  "unclosed function call; expected )",
			Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
			Found:    p.peek().Kind,
			Recovery: RecoveryInserted,
		})
	}
	return args
}

func (p *parser) parseFunctionArguments() ([]Expr, []OrderItem) {
	var args []Expr
	var orderBy []OrderItem
	if p.matchText("*") {
		args = append(args, &StarExpr{nodeBase: nodeBase{span: p.tokens[p.pos-1].Span}})
		p.expectText(")", "to close function call")
		return args, nil
	}
	for {
		if p.peek().Text == ")" {
			break
		}
		if p.peek().IsWord("ORDER") {
			p.advance()
			p.expectWord("BY", "after ORDER in function call")
			orderBy = p.parseOrderList()
			break
		}
		if p.peek().Text == "," {
			p.reportExpectedExpression("in function arguments")
			p.advance()
			continue
		}
		args = append(args, p.parseExpressionAlias(p.parseExpressionWithSet()))
		if p.peek().IsWord("ORDER") {
			p.advance()
			p.expectWord("BY", "after ORDER in function call")
			orderBy = p.parseOrderList()
			break
		}
		if !p.matchText(",") {
			break
		}
		if p.peek().Text == ")" {
			break
		}
	}
	if !p.matchText(")") {
		p.report(Diagnostic{
			Severity: SeverityError,
			Code:     "PARSE_UNCLOSED_PAREN",
			Message:  "unclosed function call; expected )",
			Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
			Found:    p.peek().Kind,
			Recovery: RecoveryInserted,
		})
	}
	return args, orderBy
}

func (p *parser) parseExpressionWithSet() Expr {
	left := p.parseExpression(0)
	operator, ok := p.matchSetOperator()
	if !ok {
		return left
	}
	all := p.matchWord("ALL")
	right := p.parseExpression(0)
	return &SetExpr{
		nodeBase: nodeBase{span: Span{Start: left.SourceSpan().Start, End: right.SourceSpan().End}},
		Left:     left,
		Operator: operator,
		All:      all,
		Right:    right,
	}
}

func (p *parser) validateFunctionArguments(function *FunctionCallExpr) {
	if len(function.Name) != 1 {
		return
	}
	switch strings.ToUpper(function.Name[0].Text) {
	case "IF":
		if len(function.Args) != 3 {
			p.report(Diagnostic{
				Severity: SeverityError,
				Code:     "PARSE_INVALID_FUNCTION_ARGUMENTS",
				Message:  fmt.Sprintf("IF expects 3 arguments, got %d", len(function.Args)),
				Span:     function.SourceSpan(),
				Recovery: RecoveryNone,
			})
		}
	case "JSON_OBJECT":
		if len(function.Args) < 2 || len(function.Args)%2 != 0 {
			p.report(Diagnostic{
				Severity: SeverityError,
				Code:     "PARSE_INVALID_FUNCTION_ARGUMENTS",
				Message:  "JSON_OBJECT expects key/value pairs",
				Span:     function.SourceSpan(),
				Recovery: RecoveryNone,
			})
		}
	}
}

func (p *parser) parseNameParts() ([]Identifier, bool) {
	first, ok := p.parseIdentifier(true)
	if !ok {
		p.reportExpectedIdentifier("in name")
		return nil, false
	}
	parts := []Identifier{first}
	for {
		index := p.pos
		for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
			index++
		}
		if index >= len(p.tokens) || p.tokens[index].Text != "." {
			break
		}
		next := index + 1
		for next < len(p.tokens) && p.tokens[next].Kind == TokenComment {
			next++
		}
		if next >= len(p.tokens) || !p.isNameToken(p.tokens[next]) {
			break
		}
		p.advance()
		part, ok := p.parseIdentifier(true)
		if !ok {
			p.reportExpectedIdentifier("after .")
			break
		}
		parts = append(parts, part)
	}
	return parts, true
}

func (p *parser) parseIdentifier(allowKeyword bool) (Identifier, bool) {
	tok := p.peek()
	if tok.Kind == TokenParameter {
		p.advance()
		return Identifier{Text: tok.Text, Span: tok.Span}, true
	}
	if tok.Kind != TokenIdentifier && tok.Kind != TokenQuotedIdentifier && !(tok.Kind == TokenKeyword && (allowKeyword || !p.isStructuralKeyword(tok))) {
		return Identifier{}, false
	}
	p.advance()
	return Identifier{Text: identifierText(tok), Quoted: tok.Kind == TokenQuotedIdentifier, Span: tok.Span}, true
}

func identifierText(tok Token) string {
	if tok.Kind != TokenQuotedIdentifier || len(tok.Text) < 2 {
		return tok.Text
	}
	if tok.Text[0] == '[' && tok.Text[len(tok.Text)-1] == ']' {
		return strings.ReplaceAll(tok.Text[1:len(tok.Text)-1], "]]", "]")
	}
	return strings.ReplaceAll(tok.Text[1:len(tok.Text)-1], tok.Text[:1]+tok.Text[:1], tok.Text[:1])
}

func (p *parser) parseIdentifierList(context string) []Identifier {
	var identifiers []Identifier
	for {
		identifier, ok := p.parseIdentifier(false)
		if !ok {
			p.reportExpectedIdentifier("in " + context)
			break
		}
		identifiers = append(identifiers, identifier)
		if !p.matchText(",") {
			break
		}
	}
	return identifiers
}

func (p *parser) canStartBareAlias() bool {
	tok := p.peek()
	return p.isNameToken(tok) && !p.isStructuralKeyword(tok) && !p.isClauseBoundary()
}

func (p *parser) isWindowClauseStart() bool {
	if !p.peek().IsWord("WINDOW") {
		return false
	}
	index := p.pos + 1
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	if index >= len(p.tokens) || !p.isNameToken(p.tokens[index]) {
		return false
	}
	index++
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	return index < len(p.tokens) && p.tokens[index].IsWord("AS")
}

func (p *parser) isExpressionStatementStart(tok Token) bool {
	if tok.Kind == TokenString || tok.Kind == TokenUnterminatedString || tok.Kind == TokenNumber || tok.Kind == TokenParameter {
		return true
	}
	if tok.Text == "(" || tok.Text == "+" || tok.Text == "-" || tok.Text == "~" || tok.Text == "*" {
		return true
	}
	return p.isNameToken(tok) && !p.isStatementKeyword(tok)
}

func (p *parser) isStatementKeyword(tok Token) bool {
	if tok.Kind != TokenKeyword {
		return false
	}
	switch strings.ToUpper(tok.Text) {
	case "CREATE", "ALTER", "DROP", "INSERT", "UPDATE", "DELETE", "MERGE", "TRUNCATE", "GRANT", "REVOKE", "EXPLAIN", "SHOW", "DESCRIBE", "USE", "CACHE", "UNCACHE", "LOAD", "COMMENT", "PRAGMA", "KILL", "BEGIN", "START", "COMMIT", "ROLLBACK", "VACUUM", "ANALYZE", "EXPORT", "IMPORT", "CALL", "EXEC":
		return true
	default:
		return false
	}
}

func (p *parser) isNameToken(tok Token) bool {
	return tok.Kind == TokenIdentifier || tok.Kind == TokenQuotedIdentifier || tok.Kind == TokenKeyword
}

func (p *parser) isStructuralKeyword(tok Token) bool {
	if tok.Kind != TokenKeyword {
		return false
	}
	switch strings.ToUpper(tok.Text) {
	case "SELECT", "FROM", "WHERE", "GROUP", "HAVING", "ORDER", "LIMIT", "FETCH", "TABLESAMPLE", "PIVOT", "UNPIVOT", "REPLACE", "UNION", "INTERSECT", "EXCEPT", "JOIN", "STRAIGHT_JOIN", "INNER", "LEFT", "RIGHT", "FULL", "CROSS", "NATURAL", "OUTER", "SEMI", "ANTI", "ON", "USING", "AND", "OR", "NOT", "IN", "BETWEEN", "IS", "LIKE", "ILIKE", "AS", "WHEN", "THEN", "ELSE", "END", "LATERAL", "CONNECT":
		return true
	default:
		return false
	}
}

func (p *parser) isClauseBoundary() bool {
	tok := p.peek()
	if tok.Kind == TokenEOF || tok.Text == ";" || tok.Text == ")" || tok.Text == "," {
		return true
	}
	for _, word := range []string{"FROM", "WHERE", "GROUP", "HAVING", "ORDER", "LIMIT", "OFFSET", "FETCH", "QUALIFY", "UNION", "INTERSECT", "EXCEPT"} {
		if tok.IsWord(word) {
			return true
		}
	}
	return false
}

func (p *parser) isSelectListBoundary() bool {
	tok := p.peek()
	if tok.Kind == TokenEOF || tok.Text == ";" || tok.Text == ")" {
		return true
	}
	for _, word := range []string{"FROM", "WHERE", "GROUP", "HAVING", "ORDER", "LIMIT", "OFFSET", "FETCH", "QUALIFY", "UNION", "INTERSECT", "EXCEPT"} {
		if tok.IsWord(word) {
			return true
		}
	}
	return false
}

func (p *parser) isExpressionBoundary() bool {
	return p.isClauseBoundary() || p.peek().IsWord("THEN") || p.peek().IsWord("ELSE") || p.peek().IsWord("END") || p.peek().IsWord("JOIN") || p.peek().IsWord("ON")
}

func (p *parser) isJoinStart() bool {
	for _, word := range []string{"JOIN", "STRAIGHT_JOIN", "INNER", "LEFT", "RIGHT", "FULL", "CROSS", "NATURAL", "OUTER", "SEMI", "ANTI"} {
		if p.peek().IsWord(word) {
			return true
		}
	}
	return false
}

func (p *parser) peekWordAfter(first, second string) bool {
	if !p.peek().IsWord(first) {
		return false
	}
	index := p.pos + 1
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	return index < len(p.tokens) && p.tokens[index].IsWord(second)
}

func (p *parser) peekWords(words ...string) bool {
	index := p.pos
	for _, word := range words {
		for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
			index++
		}
		if index >= len(p.tokens) || !p.tokens[index].IsWord(word) {
			return false
		}
		index++
	}
	return true
}

func (p *parser) peekTextAfter(wanted string) bool {
	index := p.pos
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	if index >= len(p.tokens) {
		return false
	}
	index++
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	return index < len(p.tokens) && p.tokens[index].Text == wanted
}

func infixOperator(tok Token) (int, string, bool) {
	if tok.IsWord("OR") {
		return 1, "OR", true
	}
	if tok.IsWord("AND") {
		return 2, "AND", true
	}
	if tok.IsWord("LIKE") || tok.IsWord("ILIKE") || tok.IsWord("GLOB") {
		return 3, strings.ToUpper(tok.Text), true
	}
	if tok.IsWord("COLLATE") {
		return 7, "COLLATE", true
	}
	if tok.IsWord("OVERLAPS") {
		return 3, "OVERLAPS", true
	}
	if tok.IsWord("AGAINST") {
		return 3, "AGAINST", true
	}
	switch tok.Text {
	case "=", "<>", "!=", "<", "<=", ">", ">=", "~", "~*", "!~", "!~*":
		return 3, strings.ToUpper(tok.Text), true
	case "||", "->", "->>", "#>", "#>>":
		return 4, tok.Text, true
	case "??":
		return 3, tok.Text, true
	case "-|-":
		return 3, tok.Text, true
	case "+", "-", "&", "|", "^", "<<", ">>":
		return 5, tok.Text, true
	case "*", "/", "%":
		return 6, tok.Text, true
	default:
		return 0, "", false
	}
}

func (p *parser) peek() Token {
	p.skipComments()
	if p.pos >= len(p.tokens) {
		return Token{Kind: TokenEOF, Span: Span{Start: len(p.text), End: len(p.text)}}
	}
	return p.tokens[p.pos]
}

func (p *parser) advance() Token {
	tok := p.peek()
	if p.pos < len(p.tokens) {
		p.pos++
		p.lastEnd = tok.Span.End
	}
	return tok
}

func (p *parser) skipComments() {
	for p.pos < len(p.tokens) && p.tokens[p.pos].Kind == TokenComment {
		p.pos++
	}
}

func (p *parser) matchText(text string) bool {
	if p.peek().Text != text {
		return false
	}
	p.advance()
	return true
}

func (p *parser) matchWord(word string) bool {
	if !p.peek().IsWord(word) {
		return false
	}
	p.advance()
	return true
}

func (p *parser) expectText(text, context string) bool {
	if p.matchText(text) {
		return true
	}
	p.report(Diagnostic{
		Severity: SeverityError,
		Code:     "PARSE_EXPECTED_TOKEN",
		Message:  fmt.Sprintf("expected %s %s", text, context),
		Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
		Found:    p.peek().Kind,
		Recovery: RecoveryInserted,
	})
	return false
}

func (p *parser) expectWord(word, context string) bool {
	if p.matchWord(word) {
		return true
	}
	p.reportExpectedWord(word, context)
	return false
}

func (p *parser) reportExpectedWord(word, context string) {
	p.report(Diagnostic{
		Severity: SeverityError,
		Code:     "PARSE_EXPECTED_KEYWORD",
		Message:  fmt.Sprintf("expected %s %s; got %s", word, context, p.peek().Description()),
		Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
		Found:    p.peek().Kind,
		Recovery: RecoveryInserted,
	})
}

func (p *parser) reportExpectedIdentifier(context string) {
	p.report(Diagnostic{
		Severity: SeverityError,
		Code:     "PARSE_EXPECTED_IDENTIFIER",
		Message:  fmt.Sprintf("expected an identifier %s; got %s", context, p.peek().Description()),
		Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
		Found:    p.peek().Kind,
		Recovery: RecoveryInserted,
	})
}

func (p *parser) reportExpectedQuery(context string) {
	p.report(Diagnostic{
		Severity: SeverityError,
		Code:     "PARSE_EXPECTED_QUERY",
		Message:  fmt.Sprintf("expected a query %s; got %s", context, p.peek().Description()),
		Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
		Found:    p.peek().Kind,
		Recovery: RecoveryInserted,
	})
}

func (p *parser) reportExpectedTable(context string) {
	p.report(Diagnostic{
		Severity: SeverityError,
		Code:     "PARSE_EXPECTED_TABLE",
		Message:  fmt.Sprintf("expected a table expression %s; got %s", context, p.peek().Description()),
		Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
		Found:    p.peek().Kind,
		Recovery: RecoveryInserted,
	})
}

func (p *parser) reportExpectedExpression(context string) {
	p.report(Diagnostic{
		Severity: SeverityError,
		Code:     "PARSE_EXPECTED_EXPRESSION",
		Message:  fmt.Sprintf("expected an expression %s; got %s", context, p.peek().Description()),
		Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
		Found:    p.peek().Kind,
		Recovery: RecoveryInserted,
	})
}

func (p *parser) missingExpr(context string) Expr {
	start := p.peek().Span.Start
	p.reportExpectedExpression(context)
	p.recordNode()
	return &MissingExpr{nodeBase: nodeBase{span: Span{Start: start, End: start}}, Expected: "expression"}
}

func (p *parser) synchronizeStatement() {
	p.synchronizeTo(";", "SELECT", "WITH")
}

func (p *parser) synchronizeTo(words ...string) {
	for p.peek().Kind != TokenEOF {
		for _, word := range words {
			if p.peek().Text == word || p.peek().IsWord(word) {
				return
			}
		}
		p.advance()
	}
}

func (p *parser) enter() bool {
	p.depth++
	if p.depth <= p.options.MaxDepth {
		return true
	}
	p.depth--
	p.report(Diagnostic{
		Severity: SeverityError,
		Code:     "GUARD_NESTING_DEPTH_EXCEEDED",
		Message:  fmt.Sprintf("maximum parser nesting depth of %d exceeded", p.options.MaxDepth),
		Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
		Found:    p.peek().Kind,
		Recovery: RecoverySynchronized,
	})
	return false
}

func (p *parser) leave() {
	if p.depth > 0 {
		p.depth--
	}
}

func (p *parser) recordNode() {
	p.nodeCount++
	if p.nodeCount == p.options.MaxASTNodes+1 {
		p.report(Diagnostic{
			Severity: SeverityError,
			Code:     "GUARD_AST_BUDGET_EXCEEDED",
			Message:  fmt.Sprintf("maximum AST node budget of %d exceeded", p.options.MaxASTNodes),
			Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
			Found:    p.peek().Kind,
			Recovery: RecoverySynchronized,
		})
	}
}

func (p *parser) report(diagnostic Diagnostic) {
	if len(p.diagnostics) >= maxDiagnostics {
		return
	}
	p.diagnostics = append(p.diagnostics, diagnostic)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func aliasEnd(alias *Identifier) int {
	if alias == nil {
		return 0
	}
	return alias.Span.End
}
