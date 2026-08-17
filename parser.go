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
		if p.options.Dialect == DialectDuckDB && ((tok.IsWord("WITH") && p.hasTopLevelDuckDBKeyword("PIVOT")) || (tok.Text == "(" && p.hasTopLevelDuckDBSetOperator())) {
			node = p.parseRawStatement()
		} else if p.options.Dialect == DialectTeradata && p.teradataRawStatementStart(tok) {
			node = p.parseRawStatement()
		} else if p.options.Dialect == DialectRedshift && tok.Kind == TokenNumber && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].IsWord("DIV") {
			node = p.parseRawStatement()
		} else if p.options.Dialect == DialectTeradata && ((tok.IsWord("SELECT") || tok.IsWord("CAST")) && p.hasWordFromCurrent("FORMAT")) {
			node = p.parseRawStatement()
		} else if tok.IsWord("SELECT") || (tok.IsWord("VALUES") && p.isValuesQueryStart()) || (tok.IsWord("WITH") && !p.startsWithWithNonQuery()) {
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
			case tok.IsWord("DELETE") && p.options.Dialect == DialectTSQL && !p.peekWordAfter("DELETE", "FROM"):
				node = p.parseRawStatement()
			case tok.IsWord("DELETE"):
				node = p.parseDelete()
			default:
				node = p.parseRawStatement()
			}
		} else if tok.IsWord("SET") || tok.IsWord("USE") {
			node = p.parseCommand()
		} else if p.options.Dialect == DialectMySQL && (tok.IsWord("LOCK") || tok.IsWord("UNLOCK")) {
			node = p.parseRawStatement()
		} else if p.options.Dialect == DialectMySQL && (tok.IsWord("MATCH") || strings.HasPrefix(strings.ToUpper(tok.Text), "_UTF8") || strings.HasPrefix(strings.ToUpper(tok.Text), "_LATIN1")) {
			node = p.parseRawStatement()
		} else if p.options.Dialect == DialectSQLite && (tok.IsWord("REPLACE") || tok.IsWord("ATTACH") || tok.IsWord("DETACH")) {
			node = p.parseRawStatement()
		} else if tok.IsWord("USING") && p.options.Dialect == DialectAthena {
			node = p.parseRawStatement()
		} else if (tok.IsWord("PIVOT") || tok.IsWord("PIVOT_WIDER") || tok.IsWord("UNPIVOT")) && p.options.Dialect == DialectDuckDB {
			node = p.parseRawStatement()
		} else if (tok.IsWord("RM") || tok.IsWord("REMOVE") || tok.IsWord("PUT") || (tok.IsWord("GET") && !p.peekTextAfter("(")) || tok.IsWord("DESC") || tok.IsWord("UNDROP")) && p.options.Dialect == DialectSnowflake {
			node = p.parseRawStatement()
		} else if tok.IsWord("FROM") && p.options.Dialect == DialectDuckDB {
			node = p.parseRawStatement()
		} else if p.options.Dialect == DialectDuckDB && (tok.IsWord("ATTACH") || tok.IsWord("DETACH") || tok.IsWord("INSTALL") || tok.IsWord("CHECKPOINT") || tok.IsWord("SUMMARIZE") || tok.IsWord("FORCE") || tok.IsWord("UNPIVOT")) {
			node = p.parseRawStatement()
		} else if p.isExpressionStatementStart(tok) {
			node = p.parseExpressionStatement()
		} else {
			node = p.parseUnknownStatement()
		}

		end := p.lastEnd
		if end < start {
			end = tok.Span.End
		}
		terminated := false
		if p.peek().Text == ";" {
			end = p.advance().Span.End
			terminated = true
		}
		if node == nil {
			node = &UnknownStmt{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Reason: "parser produced no statement"}
		}
		statements = append(statements, Statement{Node: node, Span: Span{Start: start, End: end}})

		if !terminated && p.peek().Kind != TokenEOF && p.peek().Text != ";" {
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
	compoundDepth := 0
	compound := p.options.Dialect == DialectTSQL && (p.peek().IsWord("CREATE") || p.peek().IsWord("IF"))
	for p.peek().Kind != TokenEOF {
		if p.peek().Text == ";" && (!compound || compoundDepth == 0) {
			break
		}
		if compound {
			if p.peek().IsWord("BEGIN") {
				compoundDepth++
			} else if p.peek().IsWord("END") && compoundDepth > 0 {
				compoundDepth--
			}
		}
		end = p.advance().Span.End
		consumed++
		if compound && compoundDepth == 0 && p.peek().Text == ";" {
			break
		}
		if compound && compoundDepth > 0 && p.peek().Kind == TokenEOF {
			break
		}
		if compound && compoundDepth == 0 && p.peek().Kind == TokenEOF {
			break
		}
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

func (p *parser) teradataRawStatementStart(tok Token) bool {
	return tok.IsWord("LOCKING") || tok.IsWord("COLLECT") || tok.IsWord("HELP") || tok.IsWord("REPLACE") || tok.IsWord("RENAME") || tok.IsWord("SEL") || tok.IsWord("UPD") || tok.IsWord("DEL") || tok.IsWord("INS")
}

func (p *parser) hasTopLevelWordFromCurrent(word string) bool {
	depth := 0
	for index := p.pos; index < len(p.tokens); index++ {
		tok := p.tokens[index]
		if tok.Kind == TokenComment {
			continue
		}
		switch tok.Text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && tok.IsWord(word) {
				return true
			}
		}
	}
	return false
}

func (p *parser) hasWordFromCurrent(word string) bool {
	for index := p.pos; index < len(p.tokens); index++ {
		if p.tokens[index].Kind != TokenComment && p.tokens[index].IsWord(word) {
			return true
		}
	}
	return false
}

func (p *parser) hasTopLevelDuckDBKeyword(word string) bool {
	depth := 0
	for index := p.pos; index < len(p.tokens); index++ {
		tok := p.tokens[index]
		if tok.Kind == TokenComment {
			continue
		}
		if tok.Text == "(" {
			depth++
		} else if tok.Text == ")" {
			if depth > 0 {
				depth--
			}
		} else if depth == 0 && tok.IsWord(word) {
			return true
		}
	}
	return false
}

func (p *parser) hasTopLevelDuckDBSetOperator() bool {
	depth := 0
	for index := p.pos; index < len(p.tokens); index++ {
		tok := p.tokens[index]
		if tok.Kind == TokenComment {
			continue
		}
		switch tok.Text {
		case "(":
			depth++
		case ")":
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && (tok.IsWord("UNION") || tok.IsWord("INTERSECT") || tok.IsWord("EXCEPT")) {
				return true
			}
		}
	}
	return false
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
	for index < len(p.tokens) && (p.tokens[index].IsWord("MATERIALIZED") || p.tokens[index].IsWord("TEMPORARY") || p.tokens[index].IsWord("TEMP")) && words < 2 {
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
			if tok.IsWord("SELECT") || tok.IsWord("VALUES") {
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
	temporary := p.matchWord("TEMPORARY") || p.matchWord("TEMP")
	if !p.matchWord("TABLE") {
		p.reportExpectedWord("TABLE", "after CREATE")
	}
	ifNotExists := false
	if p.matchWord("IF") {
		p.expectWord("NOT", "after IF in CREATE TABLE")
		p.expectWord("EXISTS", "after IF NOT in CREATE TABLE")
		ifNotExists = true
	}
	var name []Identifier
	var ok bool
	if p.options.Dialect == DialectDuckDB && (p.peek().Kind == TokenString || p.peek().Kind == TokenUnterminatedString) {
		tok := p.advance()
		name = []Identifier{{Text: strings.Trim(tok.Text, "'"), Quoted: true, Quote: '\''}}
		ok = true
	} else {
		name, ok = p.parseNameParts()
	}
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
		if p.options.Dialect == DialectClickHouse {
			columns = p.parseClickHouseInsertColumns()
		} else {
			columns = p.parseIdentifierList("INSERT columns")
		}
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
	} else if p.isQueryStart() {
		stmt.Query = p.parseSelect()
	}
	p.captureStatementTail(&stmt.nodeBase, &stmt.Tail)
	return stmt
}

func (p *parser) parseClickHouseInsertColumns() []Identifier {
	var columns []Identifier
	for p.peek().Kind != TokenEOF && p.peek().Text != ")" {
		start := p.peek().Span.Start
		parts, ok := p.parseNameParts()
		if !ok {
			p.reportExpectedIdentifier("in ClickHouse INSERT columns")
			break
		}
		text := make([]string, len(parts))
		for index, part := range parts {
			text[index] = part.Text
		}
		end := p.lastEnd
		columns = append(columns, Identifier{Text: strings.Join(text, "."), Span: Span{Start: start, End: end}})
		if !p.matchText(",") {
			break
		}
	}
	return columns
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
	hasFrom := p.matchWord("FROM")
	table, ok := p.parseNameParts()
	if !ok {
		p.reportExpectedIdentifier("after DELETE")
	}
	var alias *Identifier
	if !p.peek().IsWord("USING") && !p.peek().IsWord("WHERE") {
		alias = p.parseOptionalAlias()
	}
	stmt := &DeleteStmt{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Table: table, HasFrom: hasFrom, Alias: alias}
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
	} else if p.canStartBareAlias() {
		alias, _ := p.parseIdentifier(true)
		stmt.Alias = &alias
		stmt.nodeBase.span.End = alias.Span.End
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
	if p.options.Dialect == DialectDuckDB && p.peek().IsWord("FROM") {
		return p.parseDuckDBFromFirstQuery()
	}
	if p.isValuesQueryStart() {
		return p.parseValuesQuery()
	}
	start := p.peek().Span.Start
	if !p.enter() {
		return &SelectStmt{nodeBase: nodeBase{span: Span{Start: start, End: start}}}
	}
	defer p.leave()

	stmt := &SelectStmt{}
	p.recordNode()
	if p.peek().IsWord("WITH") {
		if p.options.Dialect == DialectClickHouse && p.clickHouseWithExpressionStart() {
			return p.parseClickHouseRawWithQuery(start)
		}
		stmt.With = p.parseCTEs()
		if p.options.Dialect == DialectPostgreSQL && p.peek().IsWord("CYCLE") {
			stmt.WithTail = p.captureWithTailBeforeQuery()
		}
		if p.options.Dialect == DialectDuckDB && p.peek().IsWord("FROM") {
			query := p.parseDuckDBFromFirstQuery()
			query.With = stmt.With
			query.nodeBase.span.Start = start
			return query
		}
		if p.queryStartsAfterParen() || p.startsNestedQueryFrom() {
			query := p.parseParenthesizedQueryStatement()
			query.With = stmt.With
			query.Parenthesized = false
			query.ParenthesisDepth = 0
			query.nodeBase.span.Start = start
			return query
		}
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
	} else if p.options.Dialect == DialectOracle && p.matchWord("UNIQUE") {
		// Oracle's legacy SELECT UNIQUE spelling is the same projection
		// modifier as DISTINCT. Keeping it structural avoids treating the
		// first projection as a bare alias.
		stmt.Distinct = true
	} else {
		// ALL is the default but accepting it keeps the parser's cursor in the
		// right place for dialects that spell it explicitly.
		if p.peek().IsWord("ALL") && !p.peekTextAfter(".") {
			p.matchWord("ALL")
		}
	}
	if p.options.Dialect == DialectBigQuery && p.matchWord("AS") {
		if p.matchWord("STRUCT") {
			stmt.SelectModifier = "AS STRUCT"
		} else if p.matchWord("VALUE") {
			stmt.SelectModifier = "AS VALUE"
		} else {
			p.reportExpectedWord("STRUCT or VALUE", "after AS in SELECT")
		}
	}
	if (p.options.Dialect == DialectTSQL || p.options.Dialect == DialectTeradata || p.options.Dialect == DialectSnowflake) && p.matchWord("TOP") {
		if p.matchText("(") {
			stmt.TopParenthesized = true
			if p.isQueryStart() {
				query := p.parseSelect()
				stmt.Top = &SubqueryExpr{nodeBase: nodeBase{span: query.SourceSpan()}, Query: query}
			} else {
				stmt.Top = p.parseRequiredExpr("inside TOP")
			}
			p.expectText(")", "after TOP count")
		} else {
			// An unparenthesized TOP count ends before the projection star or
			// the next select-list expression. Parsing a full infix expression
			// here would incorrectly consume that star as multiplication.
			stmt.Top = p.parsePostfix(p.parsePrefix())
		}
	}
	if p.options.Dialect == DialectTSQL && stmt.Top != nil && p.matchWord("PERCENT") {
		if p.matchWord("WITH") {
			p.matchWord("TIES")
		}
		if p.peek().Kind == TokenEOF || p.peek().Text == ";" || p.peek().Text == ")" {
			stmt.RawQuery = strings.TrimSpace(p.text[start:p.lastEnd])
			return stmt
		}
	}
	stmt.Projections = p.parseSelectList()

	if p.matchWord("INTO") {
		if p.matchWord("TEMPORARY") || p.matchWord("TEMP") {
			stmt.IntoTemporary = true
		}
		if p.matchWord("UNLOGGED") {
			stmt.IntoUnlogged = true
		}
		stmt.Into, _ = p.parseNameParts()
	}
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
		stmt.GroupByDistinct = p.matchWord("DISTINCT")
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
	if p.peek().IsWord("LIMIT") && p.hasClauseExpression() && p.matchWord("LIMIT") {
		stmt.Limit = p.parseLimitExpr()
		if p.matchText(",") {
			offset := stmt.Limit
			stmt.Limit = p.parseRequiredExpr("after LIMIT offset comma")
			stmt.Offset = offset
		}
	}
	if p.peek().IsWord("OFFSET") && p.hasClauseExpression() && p.matchWord("OFFSET") {
		stmt.Offset = p.parseRequiredExpr("after OFFSET")
		p.matchWord("ROW")
		p.matchWord("ROWS")
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
	if operator, ok := p.matchQuerySetOperator(); ok {
		stmt.SetOperator = operator
		stmt.SetAll = p.matchWord("ALL")
		strict := p.matchWord("STRICT")
		if p.matchWord("DISTINCT") {
			stmt.SetModifier = "DISTINCT"
		} else {
			stmt.SetModifier = p.parseSetModifier()
		}
		if p.matchWord("CORRESPONDING") {
			if !strict && !strings.HasPrefix(stmt.SetOperator, "LEFT ") && !strings.HasPrefix(stmt.SetOperator, "INNER ") {
				stmt.SetOperator = "INNER " + stmt.SetOperator
			}
			stmt.SetModifier = p.parseCorrespondingModifier()
		}
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
	if p.options.Dialect != DialectGeneric && p.peek().Kind != TokenEOF && p.peek().Text != ";" && p.peek().Text != ")" {
		tailStart := p.peek().Span.Start
		depth := 0
		for p.peek().Kind != TokenEOF && p.peek().Text != ";" {
			if p.peek().Text == ")" && depth == 0 {
				break
			}
			if p.peek().Text == "(" {
				depth++
			} else if p.peek().Text == ")" && depth > 0 {
				depth--
			}
			p.advance()
		}
		if p.lastEnd > tailStart {
			rawTail := strings.TrimSpace(p.text[tailStart:p.lastEnd])
			if strings.HasPrefix(strings.ToUpper(rawTail), "MATCH_RECOGNIZE") && strings.Contains(rawTail, "\n") {
				stmt.Tail = rawTail
			} else {
				stmt.Tail = strings.Join(strings.Fields(rawTail), " ")
			}
		}
	}

	end := p.lastEnd
	if end < start {
		end = start
	}
	stmt.nodeBase.span = Span{Start: start, End: end}
	return stmt
}

func (p *parser) parseDuckDBFromFirstQuery() *SelectStmt {
	start := p.peek().Span.Start
	index := p.pos
	depth := 0
	end := start
	for index < len(p.tokens) {
		tok := p.tokens[index]
		if tok.Kind == TokenComment {
			index++
			continue
		}
		if tok.Kind == TokenEOF || tok.Text == ";" {
			break
		}
		if tok.Text == ")" && depth == 0 {
			break
		}
		if tok.Text == "(" {
			depth++
		} else if tok.Text == ")" && depth > 0 {
			depth--
		}
		end = tok.Span.End
		index++
	}
	raw := p.text[start:end]
	text := normalizeDuckDBRawStatement(raw)
	parsed := ParseTolerant(text, DialectDuckDB)
	p.pos = index
	p.lastEnd = end
	for _, statement := range parsed.Statements {
		if query, ok := statement.Node.(*SelectStmt); ok {
			return query
		}
	}
	return &SelectStmt{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Projections: []SelectItem{{Expr: &StarExpr{}}}}
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
	if p.peek().IsWord("LIMIT") && p.hasClauseExpression() && p.matchWord("LIMIT") {
		parsed = true
		stmt.Limit = p.parseRequiredExpr("after LIMIT")
	}
	if p.peek().IsWord("OFFSET") && p.hasClauseExpression() && p.matchWord("OFFSET") {
		parsed = true
		stmt.Offset = p.parseRequiredExpr("after OFFSET")
		if p.options.Dialect == DialectTSQL {
			p.matchWord("ROW")
			p.matchWord("ROWS")
		}
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
	if (p.options.Dialect == DialectExasol || p.options.Dialect == DialectRedshift || p.options.Dialect == DialectTeradata || p.options.Dialect == DialectSpark || p.options.Dialect == DialectDatabricks || p.options.Dialect == DialectSnowflake || p.options.Dialect == DialectOracle) && p.matchWord("MINUS") {
		return "EXCEPT", true
	}
	return "", false
}

func (p *parser) matchQuerySetOperator() (string, bool) {
	prefix := ""
	if (p.peek().IsWord("LEFT") || p.peek().IsWord("INNER")) && p.peekTextAfter("UNION") {
		prefix = strings.ToUpper(p.advance().Text) + " "
	} else if p.peek().IsWord("STRICT") && p.peekTextAfter("UNION") {
		p.advance()
	}
	operator, ok := p.matchSetOperator()
	if !ok {
		return "", false
	}
	return prefix + operator, true
}

func (p *parser) parseCorrespondingModifier() string {
	if !p.matchWord("BY") {
		return "BY NAME"
	}
	if p.matchWord("NAME") {
		return "BY NAME"
	}
	if !p.matchText("(") {
		return "BY NAME"
	}
	columns := p.parseIdentifierList("CORRESPONDING columns")
	p.expectText(")", "to close CORRESPONDING columns")
	parts := make([]string, len(columns))
	for index, column := range columns {
		parts[index] = generateIdentifier(column)
	}
	return "BY NAME ON (" + strings.Join(parts, ", ") + ")"
}

func (p *parser) parseSetModifier() string {
	if !p.peekWords("BY", "NAME") {
		return ""
	}
	p.advance()
	p.advance()
	return "BY NAME"
}

func (p *parser) parseCTEs() []CTE {
	start := p.advance().Span.Start // WITH
	recursive := p.matchWord("RECURSIVE")
	var ctes []CTE
	for {
		var name Identifier
		var ok bool
		if (p.options.Dialect == DialectBigQuery || p.options.Dialect == DialectDuckDB) && (p.peek().Kind == TokenString || p.peek().Kind == TokenUnterminatedString) {
			tok := p.advance()
			name = Identifier{Text: strings.Trim(tok.Text, "'"), Quoted: true, Quote: '"', Span: tok.Span}
			ok = true
		} else {
			name, ok = p.parseIdentifier(false)
		}
		if !ok {
			p.reportExpectedIdentifier("after WITH")
			break
		}
		cteStart := name.Span.Start
		var columns []Identifier
		missingASQuery := (p.options.Dialect == DialectSnowflake || p.options.Dialect == DialectDatabricks) && p.peek().Text == "(" && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].IsWord("SELECT")
		if p.matchText("(") && !missingASQuery {
			columns = p.parseIdentifierList("CTE column list")
			p.expectText(")", "to close the CTE column list")
		}
		modifier := ""
		if p.options.Dialect == DialectDuckDB && p.matchWord("USING") {
			if p.matchWord("KEY") && p.matchText("(") {
				rawArgs, _ := p.captureBalancedFunctionArguments()
				modifier = "USING KEY " + rawArgs
			} else {
				p.reportExpectedWord("KEY", "after USING in CTE")
			}
		}
		if !missingASQuery {
			p.expectWord("AS", "after CTE name")
		}
		materialized := ""
		if p.matchWord("MATERIALIZED") {
			materialized = "MATERIALIZED"
		} else if p.matchWord("NOT") {
			if p.matchWord("MATERIALIZED") {
				materialized = "NOT MATERIALIZED"
			} else {
				p.reportExpectedWord("MATERIALIZED", "after AS NOT")
			}
		}
		if !missingASQuery {
			p.expectText("(", "before CTE query")
		}
		var query *SelectStmt
		if p.isQueryStart() {
			query = p.parseSelect()
		} else if p.peek().Text == "(" && (p.queryStartsAfterParen() || p.startsNestedQueryFrom()) {
			query = p.parseParenthesizedQueryStatement()
		} else if p.options.Dialect == DialectDuckDB && (p.peek().IsWord("PIVOT") || p.peek().IsWord("UNPIVOT")) {
			query = p.parseDuckDBRawQuery()
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
			Name:         name,
			Columns:      columns,
			Modifier:     modifier,
			Query:        query,
			Recursive:    recursive,
			Materialized: materialized,
			Span:         Span{Start: cteStart, End: end},
		})
		if !p.matchText(",") {
			// SQLGlot accepts a repeated WITH between CTE definitions and
			// normalizes it to a single WITH clause.
			if p.matchWord("WITH") {
				continue
			}
			break
		}
		if p.matchWord("WITH") {
			continue
		}
		if p.isQueryStart() {
			break
		}
	}
	_ = start
	return ctes
}

// captureWithTailBeforeQuery keeps a dialect-specific clause between a WITH
// list and the main query lossless without making the common SELECT grammar
// pretend to understand its semantics.
func (p *parser) captureWithTailBeforeQuery() string {
	start := p.peek().Span.Start
	end := start
	depth := 0
	for p.peek().Kind != TokenEOF && p.peek().Text != ";" {
		tok := p.peek()
		if depth == 0 && tok.IsWord("SELECT") {
			break
		}
		p.advance()
		end = tok.Span.End
		switch tok.Text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			if depth > 0 {
				depth--
			}
		}
	}
	return strings.TrimSpace(p.text[start:end])
}

// ClickHouse also permits a WITH expression without the SQL-standard CTE
// shape.  These forms are useful in the editor and are intentionally kept as
// a lossless query node until the AST has a dedicated representation for
// ClickHouse's expression aliases.
func (p *parser) clickHouseWithExpressionStart() bool {
	index := p.pos + 1
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	if index >= len(p.tokens) {
		return false
	}
	tok := p.tokens[index]
	if tok.Kind == TokenString || tok.Kind == TokenUnterminatedString || tok.Text == "[" || tok.Text == "(" {
		return true
	}
	if !p.isNameToken(tok) {
		return false
	}
	index++
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	return index < len(p.tokens) && p.tokens[index].Text == "("
}

func (p *parser) parseClickHouseRawWithQuery(start int) *SelectStmt {
	index := p.pos
	depth := 0
	end := start
	for index < len(p.tokens) {
		tok := p.tokens[index]
		if tok.Kind == TokenEOF || tok.Text == ";" || (tok.Text == ")" && depth == 0) {
			break
		}
		if tok.Text == "(" {
			depth++
		} else if tok.Text == ")" && depth > 0 {
			depth--
		}
		end = tok.Span.End
		index++
	}
	p.pos = index
	p.lastEnd = end
	return &SelectStmt{
		nodeBase: nodeBase{span: Span{Start: start, End: end}},
		RawQuery: strings.TrimSpace(p.text[start:end]),
	}
}

func (p *parser) parseDuckDBRawQuery() *SelectStmt {
	start := p.peek().Span.Start
	index := p.pos
	depth := 0
	end := start
	for index < len(p.tokens) {
		tok := p.tokens[index]
		if tok.Kind == TokenEOF || (tok.Text == ")" && depth == 0) {
			break
		}
		if tok.Text == "(" {
			depth++
		} else if tok.Text == ")" && depth > 0 {
			depth--
		}
		end = tok.Span.End
		index++
	}
	p.pos = index
	p.lastEnd = end
	return &SelectStmt{nodeBase: nodeBase{span: Span{Start: start, End: end}}, RawQuery: strings.TrimSpace(p.text[start:end])}
}

func (p *parser) isQueryStart() bool {
	return p.peek().IsWord("SELECT") || p.peek().IsWord("WITH") || p.isValuesQueryStart() || (p.options.Dialect == DialectDuckDB && p.peek().IsWord("FROM"))
}

func (p *parser) isValuesQueryStart() bool {
	if !p.peek().IsWord("VALUES") {
		return false
	}
	index := p.pos + 1
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	return index < len(p.tokens) && p.tokens[index].Kind != TokenEOF && p.tokens[index].Text != "." && p.tokens[index].Text != ";" && p.tokens[index].Text != ")" && p.tokens[index].Text != "," && !p.tokens[index].IsWord("AS")
}

// parseValuesQuery parses the scalar and row forms accepted by SQLGlot. A
// VALUES query is represented as a query root so it can appear in a CTE or on
// either side of a set operation.
func (p *parser) parseValuesQuery() *SelectStmt {
	start := p.advance().Span.Start
	stmt := &SelectStmt{ValuesRows: make([][]Expr, 0, 1)}
	p.recordNode()
	for {
		if p.matchText("(") {
			var row []Expr
			if p.options.Dialect == DialectHive {
				row = p.parseHiveValuesRow()
			} else {
				row = p.parseExpressionList("VALUES row")
			}
			p.expectText(")", "to close VALUES row")
			stmt.ValuesRows = append(stmt.ValuesRows, row)
		} else {
			// The unparenthesized form is a sequence of one-column rows.
			value := p.parseRequiredExpr("after VALUES")
			stmt.ValuesRows = append(stmt.ValuesRows, []Expr{value})
		}
		if !p.matchText(",") {
			break
		}
		if p.isClauseBoundary() || p.peek().IsWord("UNION") || p.peek().IsWord("INTERSECT") || p.peek().IsWord("EXCEPT") {
			break
		}
	}
	stmt.nodeBase.span = Span{Start: start, End: p.lastEnd}
	if p.matchWord("AS") {
		if alias, ok := p.parseIdentifier(false); ok {
			stmt.ValuesAlias = &alias
			if p.matchText("(") {
				stmt.ValuesColumns = p.parseIdentifierList("VALUES alias columns")
				p.expectText(")", "to close VALUES alias columns")
			}
		}
	}
	if operator, ok := p.matchSetOperator(); ok {
		all := p.matchWord("ALL")
		modifier := p.parseSetModifier()
		right := p.parseSelect()
		stmt = &SelectStmt{
			nodeBase:      nodeBase{span: Span{Start: start, End: right.SourceSpan().End}},
			SetLeft:       stmt,
			SetOperator:   operator,
			SetAll:        all,
			SetModifier:   modifier,
			SetRight:      right,
			SetLeftParen:  false,
			SetRightParen: false,
		}
	}
	return stmt
}

func (p *parser) parseHiveValuesRow() []Expr {
	var expressions []Expr
	for {
		if p.isClauseBoundary() || p.peek().Text == ")" {
			expressions = append(expressions, p.missingExpr("in VALUES row"))
			break
		}
		expression := p.parseExpression(0)
		if p.matchWord("AS") {
			if alias, ok := p.parseIdentifier(false); ok {
				expression = &AliasExpr{
					nodeBase: nodeBase{span: Span{Start: expression.SourceSpan().Start, End: alias.Span.End}},
					Expr:     expression,
					Alias:    alias,
				}
			} else {
				p.reportExpectedIdentifier("after AS in VALUES row")
			}
		}
		expressions = append(expressions, expression)
		if !p.matchText(",") {
			break
		}
		if p.isClauseBoundary() || p.peek().Text == ")" {
			break
		}
	}
	return expressions
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
		if p.options.Dialect == DialectBigQuery && isStringLiteralExpr(expr) && p.peek().Kind == TokenString {
			right := p.parsePrefix()
			expr = &FunctionCallExpr{
				nodeBase: nodeBase{span: Span{Start: expr.SourceSpan().Start, End: right.SourceSpan().End}},
				Name:     []Identifier{{Text: "CONCAT"}},
				Args:     []Expr{expr, right},
			}
		}
		item := SelectItem{Expr: expr, Span: Span{Start: start, End: expr.SourceSpan().End}}
		duckDBColonAlias := false
		if p.options.Dialect == DialectDuckDB && p.matchText(":") {
			if aliasExpr, ok := expr.(*IdentifierExpr); ok && len(aliasExpr.Parts) == 1 {
				alias := aliasExpr.Parts[0]
				value := p.parseRequiredExpr("after DuckDB colon alias")
				item.Expr = value
				item.Alias = &alias
				item.Span.End = value.SourceSpan().End
				duckDBColonAlias = true
			}
		}
		if p.options.Dialect == DialectTSQL {
			if assignment, ok := expr.(*BinaryExpr); ok && assignment.Operator == "=" {
				if identifier, ok := assignment.Left.(*IdentifierExpr); ok && len(identifier.Parts) == 1 {
					alias := identifier.Parts[0]
					item.Expr = assignment.Right
					item.Alias = &alias
					item.Span.End = assignment.Right.SourceSpan().End
				}
			}
		}
		if duckDBColonAlias {
			// DuckDB's `alias: expression` form is already represented above.
		} else if p.matchWord("AS") {
			if p.matchText("(") {
				item.AliasColumns = p.parseIdentifierList("SELECT alias columns")
				p.expectText(")", "to close SELECT alias columns")
			} else if alias, ok := p.parseIdentifier(false); ok {
				item.Alias = &alias
				item.Span.End = alias.Span.End
			} else {
				p.reportExpectedIdentifier("after AS")
			}
		} else if p.peek().IsWord("IS") && p.isBareAliasContinuation() {
			alias, _ := p.parseIdentifier(true)
			item.Alias = &alias
			item.Span.End = alias.Span.End
		} else if p.isStringProjectionAlias() {
			tok := p.advance()
			item.Alias = &Identifier{Text: strings.Trim(tok.Text, "'\""), Quoted: true, Quote: '"', Span: tok.Span}
			item.Span.End = tok.Span.End
		} else if p.isTerminalProjectionAlias() {
			alias, _ := p.parseIdentifier(true)
			item.Alias = &alias
			item.Span.End = alias.Span.End
		} else if p.canStartBareAlias() && !p.isWindowClauseStart() && !(p.options.Dialect == DialectSpark && p.peek().IsWord("ROW")) && !(p.options.Dialect == DialectSnowflake && p.peek().IsWord("RENAME")) {
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

func isStringLiteralExpr(expression Expr) bool {
	literal, ok := expression.(*LiteralExpr)
	return ok && literal.KindValue == LiteralString
}

func (p *parser) parseSelectItemModifiers() ([]Expr, []SelectItem) {
	var except []Expr
	var replace []SelectItem
	if (p.peek().IsWord("EXCEPT") || p.peek().IsWord("EXCLUDE")) && p.peekTextAfter("(") {
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
	if p.options.Dialect == DialectSnowflake && p.peek().IsWord("EXCLUDE") && !p.peekTextAfter("(") {
		p.advance()
		except = append(except, p.parseRequiredExpr("after EXCLUDE"))
	}
	if p.options.Dialect == DialectSnowflake && p.peek().IsWord("RENAME") {
		p.advance()
		parenthesized := p.matchText("(")
		for {
			start := p.peek().Span.Start
			expr := p.parseRequiredExpr("after RENAME")
			item := SelectItem{Expr: expr, Rename: true, Span: Span{Start: start, End: expr.SourceSpan().End}}
			if p.matchWord("AS") {
				if alias, ok := p.parseIdentifier(false); ok {
					item.Alias = &alias
					item.Span.End = alias.Span.End
				}
			}
			replace = append(replace, item)
			if !parenthesized || !p.matchText(",") {
				break
			}
		}
		if parenthesized {
			p.expectText(")", "to close RENAME")
		}
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
			if p.matchText("(") {
				join.Using = p.parseUsingList()
				p.expectText(")", "to close USING")
			} else if p.options.Dialect == DialectClickHouse {
				join.Using = p.parseUsingList()
			} else {
				p.expectText("(", "after USING")
			}
		}
		join.nodeBase.span = Span{Start: joinStart, End: p.lastEnd}
		table.Joins = append(table.Joins, join)
		if p.options.Dialect == DialectClickHouse && strings.Contains(strings.ToUpper(joinText), "ARRAY JOIN") {
			for p.matchText(",") {
				rightStart := p.peek().Span.Start
				right := p.parseFromItem()
				if right == nil {
					p.reportExpectedTable("after ARRAY JOIN comma")
					break
				}
				table.Joins = append(table.Joins, JoinClause{
					nodeBase: nodeBase{span: Span{Start: rightStart, End: p.lastEnd}},
					Kind:     kind,
					JoinText: joinText,
					Right:    right,
				})
			}
		}
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
			if p.matchText("(") {
				table.Joins[index].Using = p.parseUsingList()
				p.expectText(")", "to close USING")
			} else if p.options.Dialect == DialectClickHouse {
				// ClickHouse accepts the compact `USING id, name` form and
				// canonicalizes it to a parenthesized list when generating SQL.
				table.Joins[index].Using = p.parseUsingList()
			} else {
				p.expectText("(", "after USING")
			}
		}
		table.Joins[index].Late = true
		table.Joins[index].nodeBase.span.End = p.lastEnd
	}
	// A delayed ON/USING clause can be followed by another join, as in
	// `JOIN b JOIN c ON ... ON ... CROSS JOIN d`. Continue the join chain
	// after attaching the delayed predicate instead of returning to SELECT.
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
			if p.matchText("(") {
				join.Using = p.parseIdentifierList("USING")
				p.expectText(")", "to close USING")
			} else if p.options.Dialect == DialectClickHouse {
				join.Using = p.parseIdentifierList("USING")
			} else {
				p.expectText("(", "after USING")
			}
		}
		join.nodeBase.span = Span{Start: joinStart, End: p.lastEnd}
		table.Joins = append(table.Joins, join)
		if p.options.Dialect == DialectClickHouse && strings.Contains(strings.ToUpper(joinText), "ARRAY JOIN") {
			for p.matchText(",") {
				rightStart := p.peek().Span.Start
				right := p.parseFromItem()
				if right == nil {
					p.reportExpectedTable("after ARRAY JOIN comma")
					break
				}
				table.Joins = append(table.Joins, JoinClause{
					nodeBase: nodeBase{span: Span{Start: rightStart, End: p.lastEnd}},
					Kind:     kind,
					JoinText: joinText,
					Right:    right,
				})
			}
		}
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
	if p.options.Dialect == DialectHive && p.peek().IsWord("DISTRIBUTE") {
		p.advance()
		p.expectWord("BY", "after DISTRIBUTE")
		spec.PartitionBy = p.parseExpressionList("DISTRIBUTE BY")
		if p.matchWord("SORT") {
			p.expectWord("BY", "after SORT")
			spec.OrderBy = p.parseOrderList()
		}
		p.expectText(")", "to close window specification")
		return spec
	}
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
	for p.peek().Kind != TokenEOF && !p.peek().IsWord("JOIN") && !(p.options.Dialect == DialectTSQL && p.peek().IsWord("APPLY")) {
		words = append(words, strings.ToUpper(p.advance().Text))
	}
	if p.matchWord("JOIN") {
		words = append(words, "JOIN")
	} else if p.options.Dialect == DialectTSQL && p.matchWord("APPLY") {
		words = append(words, "APPLY")
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
		if p.matchText("(") && p.isQueryStart() {
			query := p.parseSelect()
			p.expectText(")", "after LATERAL subquery")
			alias := p.parseOptionalAlias()
			columns := p.parseFromAliasColumns()
			return &SubqueryFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(p.lastEnd, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Query: query, Alias: alias, Columns: columns, Lateral: true}
		}
		if p.isNameToken(p.peek()) {
			relation := p.parseFromItem()
			if function, ok := relation.(*TableFunctionFrom); ok {
				function.Lateral = true
				function.nodeBase.span.Start = start
				return function
			}
			if raw, ok := relation.(*RawFrom); ok {
				raw.Raw = "LATERAL " + raw.Raw
				raw.nodeBase.span.Start = start
				return raw
			}
		}
		p.reportExpectedQuery("after LATERAL")
		return nil
	}
	if p.startsNestedQueryFrom() {
		raw, end := p.captureBalancedFrom()
		alias := p.parseOptionalAlias()
		return &RawFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, aliasEnd(alias))}}, Raw: raw, Alias: alias}
	}
	if p.options.Dialect == DialectDuckDB && p.startsRawDuckDBQueryFrom() {
		raw, end := p.captureBalancedFrom()
		alias := p.parseOptionalAlias()
		return &RawFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, aliasEnd(alias))}}, Raw: raw, Alias: alias}
	}
	if p.options.Dialect == DialectSnowflake && p.startsSnowflakeStageFrom() {
		return p.parseSnowflakeStageFrom(start)
	}
	if p.options.Dialect == DialectDuckDB && (p.peek().Kind == TokenString || p.peek().Kind == TokenUnterminatedString) {
		tok := p.advance()
		path := strings.Trim(tok.Text, "'")
		part := Identifier{Text: path, Quoted: true, Quote: '"', Span: tok.Span}
		alias := p.parseOptionalAlias()
		sample := p.parseTableSample()
		return &TableName{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(p.lastEnd, aliasEnd(alias))}}, Parts: []Identifier{part}, Alias: alias, Sample: sample}
	}
	if p.options.Dialect == DialectClickHouse && p.peek().Text == "[" {
		expression := p.parsePrefix()
		raw := strings.TrimSpace(p.text[start:p.lastEnd])
		if raw == "" {
			raw = renderExpr(expression)
		}
		alias := p.parseOptionalAlias()
		columns := p.parseFromAliasColumns()
		return &RawFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(p.lastEnd, identifierListEnd(columns))}}, Raw: raw, Alias: alias, Columns: columns}
	}
	if p.peek().Text == "{" && (p.options.Dialect == DialectGeneric || p.options.Dialect == DialectClickHouse || p.options.Dialect == DialectSpark || p.options.Dialect == DialectDatabricks) {
		depth := 0
		end := start
		for p.peek().Kind != TokenEOF {
			tok := p.advance()
			end = tok.Span.End
			switch tok.Text {
			case "{":
				depth++
			case "}":
				depth--
				if depth == 0 {
					alias := p.parseOptionalAlias()
					return &RawFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, aliasEnd(alias))}}, Raw: p.text[start:end], Alias: alias}
				}
			}
		}
		p.reportExpectedTable("after FROM")
		return nil
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
		if p.options.Dialect == DialectTSQL && p.peek().IsWord("MERGE") {
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
					Message:  "unclosed MERGE FROM expression; expected )",
					Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
					Found:    p.peek().Kind,
					Recovery: RecoveryInserted,
				})
			}
			alias := p.parseOptionalAlias()
			columns := p.parseFromAliasColumns()
			return &RawFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Raw: p.text[start:end], Alias: alias, Columns: columns}
		}
		if p.isQueryStart() {
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

	parts, ok := p.parseFromNameParts()
	if !ok {
		return nil
	}
	if p.options.Dialect == DialectDuckDB && p.matchText(":") {
		// DuckDB also permits `alias: relation` and `alias: table_function(...)`
		// in FROM. Parse the relation normally, then attach the left-hand name
		// as its alias so joins and table-function rewrites remain structural.
		alias := parts[len(parts)-1]
		relation := p.parseFromItem()
		if relation == nil {
			return &TableName{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Parts: parts, Alias: &alias}
		}
		switch item := relation.(type) {
		case *TableName:
			item.Alias = &alias
		case *TableFunctionFrom:
			item.Alias = &alias
		case *SubqueryFrom:
			item.Alias = &alias
		case *GroupedFrom:
			item.Alias = &alias
		case *RawFrom:
			item.Alias = &alias
		}
		return relation
	}
	if len(parts) == 1 && strings.EqualFold(parts[0].Text, "VALUES") && p.peek().Text == "(" {
		end := p.lastEnd
		depth := 0
		for p.peek().Kind != TokenEOF {
			if depth == 0 && p.peek().IsWord("AS") {
				break
			}
			tok := p.advance()
			end = tok.Span.End
			switch tok.Text {
			case "(":
				depth++
			case ")":
				if depth > 0 {
					depth--
				}
			}
		}
		alias := p.parseOptionalAlias()
		columns := p.parseFromAliasColumns()
		return &RawFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Raw: p.text[start:end], Alias: alias, Columns: columns}
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
		if p.options.Dialect == DialectSnowflake && len(parts) == 1 && strings.EqualFold(parts[0].Text, "SEMANTIC_VIEW") {
			rawArgs, end := p.captureBalancedFunctionArguments()
			alias := p.parseOptionalAlias()
			columns := p.parseFromAliasColumns()
			return &TableFunctionFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Name: parts, RawArgs: rawArgs, Alias: alias, Columns: columns}
		}
		if p.options.Dialect == DialectSnowflake && len(parts) == 1 && strings.EqualFold(parts[0].Text, "FLATTEN") {
			rawArgs, end := p.captureBalancedFunctionArguments()
			alias := p.parseOptionalAlias()
			columns, columnsRaw := p.parseFromAliasColumnsWithRaw()
			return &TableFunctionFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Name: parts, RawArgs: rawArgs, Alias: alias, Columns: columns, ColumnsRaw: columnsRaw}
		}
		if (p.options.Dialect == DialectBigQuery && p.requiresRawFunctionArguments()) || (p.options.Dialect == DialectMySQL && len(parts) == 1 && strings.EqualFold(parts[0].Text, "JSON_TABLE")) {
			rawArgs, end := p.captureBalancedFunctionArguments()
			alias := p.parseOptionalAlias()
			columns := p.parseFromAliasColumns()
			return &TableFunctionFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Name: parts, RawArgs: rawArgs, Alias: alias, Columns: columns}
		}
		if (p.options.Dialect == DialectPostgreSQL || p.options.Dialect == DialectOracle) && len(parts) == 1 && (strings.EqualFold(parts[0].Text, "XMLTABLE") || (p.options.Dialect == DialectOracle && strings.EqualFold(parts[0].Text, "JSON_TABLE"))) {
			rawArgs, end := p.captureBalancedFunctionArguments()
			alias := p.parseOptionalAlias()
			columns, columnsRaw := p.parseFromAliasColumnsWithRaw()
			return &TableFunctionFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Name: parts, RawArgs: rawArgs, Alias: alias, Columns: columns, ColumnsRaw: columnsRaw}
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
		if p.options.Dialect == DialectTSQL && p.matchWord("WITH") && p.peek().Text == "(" {
			p.advance()
			_, end := p.captureBalancedFunctionArguments()
			callText := p.text[parts[0].Span.Start:end]
			if open := strings.IndexByte(callText, '('); open >= 0 {
				rawArgs := callText[open:]
				alias := p.parseOptionalAlias()
				columns := p.parseFromAliasColumns()
				return &TableFunctionFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Name: parts, RawArgs: rawArgs, Alias: alias, Columns: columns}
			}
		}
		withOrdinality := false
		withOffset := false
		if p.matchWord("WITH") {
			if p.matchWord("ORDINALITY") {
				withOrdinality = true
			} else if p.options.Dialect == DialectBigQuery && p.matchWord("OFFSET") {
				withOffset = true
			} else {
				p.reportExpectedWord("ORDINALITY", "after WITH in table function")
			}
		}
		alias := p.parseOptionalAlias()
		columns, columnsRaw := p.parseFromAliasColumnsWithRaw()
		return &TableFunctionFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(p.lastEnd, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Name: parts, Args: args, Alias: alias, Columns: columns, ColumnsRaw: columnsRaw, WithOrdinality: withOrdinality, WithOffset: withOffset}
	}
	var tail string
	if p.options.Dialect == DialectSnowflake && (p.peek().IsWord("CHANGES") || p.peek().IsWord("BEFORE") || p.peek().IsWord("AT")) {
		tail = p.captureSnowflakeFromTail()
	}
	if (p.options.Dialect == DialectHive || p.options.Dialect == DialectSpark || p.options.Dialect == DialectTrino) && p.isTimeTravelTableTailStart() {
		tail = p.captureTimeTravelTableTail()
	}
	var hint string
	if p.options.Dialect == DialectTSQL && p.peek().IsWord("WITH") && p.peekTextAfter("(") {
		hintStart := p.advance().Span.Start
		if p.matchText("(") {
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
			hint = strings.TrimSpace(p.text[hintStart:end])
		}
	}
	alias := p.parseOptionalAlias()
	if p.options.Dialect == DialectMySQL && tail == "" && p.isMySQLTableTailStart() {
		tail = p.captureMySQLTableTail()
	}
	columns := p.parseFromAliasColumns()
	sample := p.parseTableSample()
	return &TableName{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(p.lastEnd, maxInt(aliasEnd(alias), identifierListEnd(columns)))}}, Parts: parts, Alias: alias, Columns: columns, Sample: sample, Hint: hint, Tail: tail}
}

func (p *parser) isMySQLTableTailStart() bool {
	return p.peek().IsWord("USE") || p.peek().IsWord("FORCE") || p.peek().IsWord("IGNORE") || p.peek().IsWord("PARTITION")
}

func (p *parser) captureMySQLTableTail() string {
	start := p.peek().Span.Start
	depth := 0
	end := start
	for p.peek().Kind != TokenEOF && p.peek().Text != ";" {
		tok := p.peek()
		if depth == 0 && (tok.IsWord("WHERE") || tok.IsWord("GROUP") || tok.IsWord("HAVING") || tok.IsWord("ORDER") || tok.IsWord("LIMIT") || tok.IsWord("QUALIFY") || tok.IsWord("JOIN") || tok.IsWord("INNER") || tok.IsWord("LEFT") || tok.IsWord("RIGHT") || tok.IsWord("FULL") || tok.IsWord("CROSS")) {
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
	return strings.TrimSpace(p.text[start:end])
}

func (p *parser) captureSnowflakeFromTail() string {
	start := p.peek().Span.Start
	depth := 0
	end := start
	for p.peek().Kind != TokenEOF && p.peek().Text != ";" {
		tok := p.peek()
		if depth == 0 && (tok.IsWord("AS") || tok.IsWord("WHERE") || tok.IsWord("GROUP") || tok.IsWord("ORDER") || tok.IsWord("LIMIT") || tok.IsWord("QUALIFY") || tok.IsWord("JOIN") || tok.IsWord("INNER") || tok.IsWord("LEFT") || tok.IsWord("RIGHT") || tok.IsWord("FULL") || tok.IsWord("CROSS")) {
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
	return strings.TrimSpace(p.text[start:end])
}

func (p *parser) isTimeTravelTableTailStart() bool {
	return p.peekWords("VERSION", "AS", "OF") ||
		p.peekWords("TIMESTAMP", "AS", "OF") ||
		(p.options.Dialect == DialectTrino && p.peekWords("FOR", "VERSION", "AS", "OF"))
}

func (p *parser) captureTimeTravelTableTail() string {
	start := p.peek().Span.Start
	depth := 0
	end := start
	for p.peek().Kind != TokenEOF && p.peek().Text != ";" {
		tok := p.peek()
		if depth == 0 && (tok.IsWord("WHERE") || tok.IsWord("GROUP") || tok.IsWord("HAVING") || tok.IsWord("ORDER") || tok.IsWord("LIMIT") || tok.IsWord("QUALIFY") || tok.IsWord("JOIN") || tok.IsWord("INNER") || tok.IsWord("LEFT") || tok.IsWord("RIGHT") || tok.IsWord("FULL") || tok.IsWord("CROSS") || tok.IsWord("UNION") || tok.IsWord("INTERSECT") || tok.IsWord("EXCEPT")) {
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
	return strings.TrimSpace(p.text[start:end])
}

func (p *parser) isHiveQueryTailStart() bool {
	return p.peek().IsWord("DISTRIBUTE") || p.peek().IsWord("CLUSTER") || p.peek().IsWord("SORT")
}

func (p *parser) captureHiveQueryTail() string {
	start := p.peek().Span.Start
	end := start
	depth := 0
	for p.peek().Kind != TokenEOF && p.peek().Text != ";" {
		tok := p.advance()
		end = tok.Span.End
		switch tok.Text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			if depth > 0 {
				depth--
			}
		}
	}
	return strings.Join(strings.Fields(p.text[start:end]), " ")
}

func (p *parser) startsSnowflakeStageFrom() bool {
	tok := p.peek()
	if tok.Kind == TokenString && strings.HasPrefix(tok.Text, "'@") {
		return true
	}
	return tok.Kind == TokenParameter && strings.HasPrefix(tok.Text, "@")
}

func (p *parser) parseSnowflakeStageFrom(start int) FromItem {
	end := start
	depth := 0
	for p.peek().Kind != TokenEOF && p.peek().Text != ";" {
		tok := p.peek()
		if depth == 0 && (tok.Text == ")" || tok.Text == "," || tok.IsWord("WHERE") || tok.IsWord("GROUP") || tok.IsWord("ORDER") || tok.IsWord("LIMIT") || tok.IsWord("QUALIFY") || tok.IsWord("JOIN") || tok.IsWord("INNER") || tok.IsWord("LEFT") || tok.IsWord("RIGHT") || tok.IsWord("FULL") || tok.IsWord("CROSS") || tok.IsWord("AS")) {
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
	alias := p.parseOptionalAlias()
	return &RawFrom{nodeBase: nodeBase{span: Span{Start: start, End: maxInt(end, aliasEnd(alias))}}, Raw: p.text[start:end], Alias: alias}
}

func (p *parser) parseFromNameParts() ([]Identifier, bool) {
	if p.options.Dialect != DialectBigQuery {
		return p.parseNameParts()
	}
	first, ok := p.parseIdentifier(true)
	if !ok {
		p.reportExpectedIdentifier("in name")
		return nil, false
	}
	parts := []Identifier{first}
	for {
		if p.peek().Kind == TokenNumber && strings.HasPrefix(p.peek().Text, ".") {
			number := p.advance()
			text := strings.TrimPrefix(number.Text, ".")
			end := number.Span.End
			if p.peek().Kind == TokenIdentifier && p.peek().Span.Start == end {
				text += p.advance().Text
				end = p.lastEnd
			}
			parts = append(parts, Identifier{Text: text, Quoted: true, Quote: '`', Span: Span{Start: number.Span.Start, End: end}})
			continue
		}
		if p.matchText("-") {
			if p.peek().Kind == TokenNumber || p.isNameToken(p.peek()) {
				part := p.advance()
				partText := strings.TrimSuffix(part.Text, ".")
				parts[len(parts)-1].Text += "-" + partText
				if strings.HasSuffix(part.Text, ".") {
					if p.peek().Kind == TokenIdentifier {
						next := p.advance()
						parts = append(parts, Identifier{Text: identifierText(next), Quoted: next.Kind == TokenQuotedIdentifier, Span: next.Span})
					}
				}
				continue
			}
			p.pos--
		}
		if !p.matchText(".") {
			break
		}
		if p.peek().Text == "*" {
			star := p.advance()
			parts = append(parts, Identifier{Text: "*", Span: star.Span})
			break
		}
		if p.peek().Kind == TokenNumber {
			number := p.advance()
			text := number.Text
			end := number.Span.End
			if p.peek().Kind == TokenIdentifier && p.peek().Span.Start == end {
				text += p.advance().Text
				end = p.lastEnd
			}
			parts = append(parts, Identifier{Text: text, Quoted: true, Quote: '`', Span: Span{Start: number.Span.Start, End: end}})
			continue
		}
		part, ok := p.parseIdentifier(true)
		if !ok {
			break
		}
		parts = append(parts, part)
	}
	if p.peek().Text == "*" && len(parts) > 0 && p.peek().Span.Start == parts[len(parts)-1].Span.End {
		p.advance()
		parts[len(parts)-1].Text += "*"
	}
	return parts, true
}

func (p *parser) startsRawDuckDBQueryFrom() bool {
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
	return index < len(p.tokens) && (p.tokens[index].IsWord("PIVOT") || p.tokens[index].IsWord("UNPIVOT") || p.tokens[index].IsWord("DESCRIBE"))
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
	if p.isNameToken(p.peek()) && p.peekTextAfter("(") {
		start = p.advance().Span.Start
	}
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
		// Table functions and derived relations may use `AS (col)` without a
		// relation alias. Leave the opening parenthesis for
		// parseFromAliasColumns so this valid shape does not produce a spurious
		// diagnostic.
		if p.peek().Text == "(" {
			return nil
		}
		if alias, ok := p.parseIdentifier(true); ok {
			return &alias
		}
		p.reportExpectedIdentifier("after AS")
		return nil
	}
	if p.isJoinStart() {
		return nil
	}
	if p.options.Dialect == DialectSnowflake && (p.peek().IsWord("OFFSET") || p.peek().IsWord("LIMIT")) && p.peekWordAfterAny("MATCH_CONDITION") {
		alias, _ := p.parseIdentifier(true)
		return &alias
	}
	// QUALIFY is normally a clause boundary, but Presto accepts it as a
	// terminal unquoted relation alias. Keep the clause interpretation when
	// another token follows it.
	if p.peek().IsWord("QUALIFY") && p.isTerminalBareAlias() {
		alias, _ := p.parseIdentifier(true)
		return &alias
	}
	if p.canStartBareAlias() {
		alias, _ := p.parseIdentifier(false)
		return &alias
	}
	return nil
}

func (p *parser) isTerminalBareAlias() bool {
	index := p.pos + 1
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	if index >= len(p.tokens) {
		return true
	}
	tok := p.tokens[index]
	return tok.Kind == TokenEOF || tok.Text == ";" || tok.Text == ")" || tok.Text == ","
}

func (p *parser) parseFromAliasColumns() []Identifier {
	if !p.matchText("(") {
		return nil
	}
	columns := p.parseIdentifierList("FROM alias columns")
	p.expectText(")", "to close FROM alias columns")
	return columns
}

// parseFromAliasColumnsWithRaw keeps typed relation-column declarations such
// as AS t("rank" INT) intact. The structural names remain available to
// analysis while the raw suffix prevents identity generation from dropping
// PostgreSQL/XMLTABLE type information.
func (p *parser) parseFromAliasColumnsWithRaw() ([]Identifier, string) {
	if !p.matchText("(") {
		return nil, ""
	}
	start := p.tokens[p.pos-1].Span.Start
	var columns []Identifier
	typed := false
	for p.peek().Kind != TokenEOF && p.peek().Text != ")" {
		identifier, ok := p.parseIdentifier(false)
		if !ok {
			p.reportExpectedIdentifier("in FROM alias columns")
			break
		}
		columns = append(columns, identifier)
		depth := 0
		for p.peek().Kind != TokenEOF {
			tok := p.peek()
			if depth == 0 && (tok.Text == "," || tok.Text == ")") {
				break
			}
			typed = true
			p.advance()
			switch tok.Text {
			case "(", "[", "{":
				depth++
			case ")", "]", "}":
				if depth > 0 {
					depth--
				}
			}
		}
		if !p.matchText(",") {
			break
		}
	}
	end := p.lastEnd
	p.expectText(")", "to close FROM alias columns")
	if !typed {
		return columns, ""
	}
	end = p.lastEnd
	return columns, p.text[start:end]
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
			} else if p.matchWord("FIRST") {
				item.NullsFirst = true
			} else {
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

func (p *parser) parseLimitExpr() Expr {
	if p.options.Dialect == DialectDuckDB && p.peek().Kind == TokenNumber && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].Text == "%" {
		start := p.peek().Span.Start
		end := p.advance().Span.End
		p.advance() // percent marker
		end = p.lastEnd
		return &RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Raw: strings.TrimSpace(p.text[start:end-1]) + " PERCENT"}
	}
	return p.parseRequiredExpr("after LIMIT")
}

func (p *parser) parseExpression(minPrecedence int) Expr {
	if !p.enter() {
		return p.missingExpr("in expression")
	}
	defer p.leave()

	left := p.parsePrefix()
	left = p.parsePostfix(left)
	for {
		if p.options.Dialect == DialectPostgreSQL {
			if first, ok := left.(*LiteralExpr); ok && first.KindValue == LiteralString && p.peek().Kind == TokenString {
				args := []Expr{first}
				for p.peek().Kind == TokenString {
					args = append(args, p.parsePrefix())
				}
				left = &FunctionCallExpr{nodeBase: nodeBase{span: Span{Start: first.SourceSpan().Start, End: args[len(args)-1].SourceSpan().End}}, Name: []Identifier{{Text: "CONCAT"}}, Args: args}
				p.recordNode()
				continue
			}
		}
		if p.options.Dialect == DialectClickHouse && minPrecedence == 0 && p.peek().Text == "?" {
			start := left.SourceSpan().Start
			p.advance()
			thenExpr := p.parseRequiredExpr("after ? in ternary expression")
			p.expectText(":", "after ? expression")
			elseExpr := p.parseRequiredExpr("after : in ternary expression")
			left = &CaseExpr{
				nodeBase: nodeBase{span: Span{Start: start, End: elseExpr.SourceSpan().End}},
				Whens:    []CaseWhen{{Condition: left, Result: thenExpr}},
				Else:     elseExpr,
			}
			p.recordNode()
			left = p.parsePostfix(left)
			continue
		}
		// `?` is ternary syntax in ClickHouse. The shared infix table also
		// contains it for PostgreSQL JSON operators, so do not let a nested
		// higher-precedence parse consume it first.
		if p.options.Dialect == DialectClickHouse && p.peek().Text == "?" && minPrecedence > 0 {
			break
		}
		if p.peek().Text == "::" || (p.options.Dialect == DialectSingleStore && (p.peek().Text == ":>" || p.peek().Text == "!:>")) {
			if minPrecedence > 7 {
				break
			}
			operator := p.advance().Text
			var typeExpr Expr
			var suffix []Identifier
			if p.options.Dialect == DialectSingleStore && operator != "::" {
				typeExpr, suffix = p.parseSingleStoreCastType()
			} else if p.options.Dialect == DialectSingleStore {
				typeExpr, suffix = p.parseSingleStoreCastType()
			} else {
				typeExpr, suffix = p.parseCastType()
			}
			castOperator := ""
			if operator != "::" {
				castOperator = operator
			}
			left = &CastExpr{
				nodeBase:   nodeBase{span: Span{Start: left.SourceSpan().Start, End: maxInt(p.lastEnd, typeExpr.SourceSpan().End)}},
				Keyword:    "CAST",
				Operator:   castOperator,
				Value:      left,
				Type:       typeExpr,
				TypeSuffix: suffix,
			}
			p.recordNode()
			continue
		}
		if p.options.Dialect == DialectDatabricks && p.peek().Text == "?" && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].Text == "::" {
			p.advance()
			p.advance()
			typeExpr, suffix := p.parseCastType()
			left = &CastExpr{
				nodeBase:   nodeBase{span: Span{Start: left.SourceSpan().Start, End: maxInt(p.lastEnd, typeExpr.SourceSpan().End)}},
				Keyword:    "TRY_CAST",
				Value:      left,
				Type:       typeExpr,
				TypeSuffix: suffix,
			}
			p.recordNode()
			continue
		}
		if p.options.Dialect == DialectTeradata && ((p.peek().IsWord("NOT") && p.peekTextAfter("=")) || (p.peek().Text == "^" && p.peekTextAfter("="))) {
			if minPrecedence > 3 {
				break
			}
			p.advance()
			p.advance()
			right := p.parseExpression(4)
			left = &BinaryExpr{
				nodeBase: nodeBase{span: Span{Start: left.SourceSpan().Start, End: right.SourceSpan().End}},
				Left:     left, Operator: "<>", Right: right,
			}
			p.recordNode()
			continue
		}
		if p.peek().IsWord("NOT") && (p.peekWordAfter("NOT", "IN") || p.peekWordAfter("NOT", "BETWEEN") || p.peekWordAfter("NOT", "LIKE") || p.peekWordAfter("NOT", "ILIKE") || p.peekWordAfter("NOT", "RLIKE") || p.peekWordAfter("NOT", "REGEXP") || p.peekWordAfter("NOT", "SIMILAR")) {
			if minPrecedence > 3 {
				break
			}
			p.advance()
			if p.peek().IsWord("IN") {
				left = p.parseIn(left, true)
			} else if p.peek().IsWord("BETWEEN") {
				left = p.parseBetween(left, true)
			} else {
				notOperator := "LIKE"
				if p.peek().IsWord("ILIKE") {
					notOperator = "ILIKE"
				} else if p.peek().IsWord("RLIKE") {
					notOperator = "RLIKE"
				} else if p.peek().IsWord("REGEXP") {
					notOperator = "REGEXP"
				} else if p.peek().IsWord("SIMILAR") {
					notOperator = "SIMILAR"
				}
				p.advance()
				if notOperator == "SIMILAR" {
					p.matchWord("TO")
					notOperator = "SIMILAR TO"
				}
				right := p.parseExpression(4)
				binary := &BinaryExpr{nodeBase: nodeBase{span: Span{Start: left.SourceSpan().Start, End: right.SourceSpan().End}}, Left: left, Operator: "NOT " + notOperator, Right: right}
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
		if p.options.Dialect == DialectPostgreSQL && (p.peek().IsWord("ISNULL") || p.peek().IsWord("NOTNULL")) {
			if minPrecedence > 3 {
				break
			}
			not := p.peek().IsWord("NOTNULL")
			p.advance()
			left = &IsExpr{
				nodeBase: nodeBase{span: Span{Start: left.SourceSpan().Start, End: p.lastEnd}},
				Value:    left,
				Operator: map[bool]string{true: "IS NOT", false: "IS"}[not],
				Right:    &LiteralExpr{nodeBase: nodeBase{span: Span{Start: p.lastEnd, End: p.lastEnd}}, KindValue: LiteralNull, Raw: "NULL"},
			}
			p.recordNode()
			continue
		}
		if p.peek().IsWord("IS") && !p.isBareAliasContinuation() {
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
		if p.options.Dialect == DialectBigQuery && p.peek().IsWord("OVERLAPS") && p.wordAtExpressionEnd("OVERLAPS") {
			break
		}

		precedence, operator, ok := 0, "", false
		if p.peek().IsWord("OPERATOR") && p.peekTextAfter("(") {
			if minPrecedence > 3 {
				break
			}
			precedence, operator, ok = 3, p.parseOperatorName(), true
			operator = p.consumeOperatorComments(operator)
		} else {
			if p.options.Dialect == DialectTeradata {
				switch strings.ToUpper(p.peek().Text) {
				case "LT":
					precedence, operator, ok = 3, "<", true
				case "LE":
					precedence, operator, ok = 3, "<=", true
				case "GT":
					precedence, operator, ok = 3, ">", true
				case "GE":
					precedence, operator, ok = 3, ">=", true
				case "NE":
					precedence, operator, ok = 3, "<>", true
				case "EQ":
					precedence, operator, ok = 3, "=", true
				}
			}
			if p.options.Dialect == DialectMySQL && p.peek().IsWord("MOD") {
				precedence, operator, ok = 6, "%", true
			} else if !ok {
				precedence, operator, ok = infixOperator(p.peek())
			}
			if p.options.Dialect == DialectExasol && p.peek().IsWord("REGEXP_LIKE") {
				precedence, operator, ok = 3, "REGEXP_LIKE", true
			}
		}
		if !ok || precedence < minPrecedence {
			break
		}
		if !strings.HasPrefix(operator, "OPERATOR(") {
			p.advance()
		}
		if strings.EqualFold(operator, "SIMILAR") {
			p.matchWord("TO")
			operator = "SIMILAR TO"
		}
		if (strings.EqualFold(operator, "LIKE") || strings.EqualFold(operator, "ILIKE") || strings.EqualFold(operator, "GLOB")) && p.matchWord("ANY") {
			operator += " ANY"
		}
		rightPrecedence := precedence + 1
		if operator == "->" && p.arrowBodyNeedsLowPrecedence() {
			// Lambda bodies include comparisons and boolean expressions. JSON
			// arrows do not need this treatment because their right operand is
			// normally a literal path or a single expression.
			rightPrecedence = 0
		}
		right := p.parseExpression(rightPrecedence)
		binary := &BinaryExpr{
			nodeBase: nodeBase{span: Span{Start: left.SourceSpan().Start, End: right.SourceSpan().End}},
			Left:     left,
			Operator: operator,
			Right:    right,
		}
		if strings.EqualFold(operator, "LIKE") || strings.EqualFold(operator, "ILIKE") || strings.EqualFold(operator, "GLOB") || strings.EqualFold(operator, "LIKE ANY") || strings.EqualFold(operator, "ILIKE ANY") || strings.EqualFold(operator, "GLOB ANY") || strings.EqualFold(operator, "SIMILAR TO") {
			p.parseLikeEscape(binary)
		}
		p.recordNode()
		left = binary
		left = p.parsePostfix(left)
	}
	return left
}

func (p *parser) arrowBodyNeedsLowPrecedence() bool {
	depth := 0
	for index := p.pos; index < len(p.tokens); index++ {
		tok := p.tokens[index]
		if tok.Kind == TokenComment {
			continue
		}
		switch tok.Text {
		case "(", "[", "{":
			depth++
		case ")", "]", "}":
			if depth == 0 {
				return false
			}
			depth--
		case ",":
			if depth == 0 {
				return false
			}
		}
		if depth > 0 {
			continue
		}
		if tok.IsWord("LIKE") || tok.IsWord("ILIKE") || tok.IsWord("GLOB") || tok.IsWord("IS") || tok.IsWord("IN") || tok.IsWord("BETWEEN") || tok.IsWord("AND") || tok.IsWord("OR") {
			return true
		}
		if tok.Text == "->" || tok.Text == "->>" {
			continue
		}
		if _, _, ok := infixOperator(tok); ok {
			return true
		}
	}
	return false
}

func (p *parser) parseOperatorName() string {
	start := p.advance().Span.Start
	p.expectText("(", "after OPERATOR")
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
	return strings.TrimSpace(p.text[start:end])
}

func (p *parser) consumeOperatorComments(operator string) string {
	index := p.pos
	end := p.lastEnd
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		end = p.tokens[index].Span.End
		index++
	}
	if index == p.pos {
		return operator
	}
	operator += p.text[p.lastEnd:end]
	p.pos = index
	p.lastEnd = end
	return operator
}

func (p *parser) parseLikeEscape(expression *BinaryExpr) {
	if !p.matchWord("ESCAPE") {
		return
	}
	// ESCAPE takes one scalar expression. Parsing it with the normal
	// expression entry point would consume a following WHERE predicate as
	// part of the escape value, e.g. `LIKE 'x' ESCAPE '#' AND flag`.
	// Prefix/postfix parsing preserves the following boolean operator for the
	// surrounding expression while still accepting identifiers and calls.
	if p.isExpressionBoundary() {
		expression.Escape = p.missingExpr("after ESCAPE")
	} else {
		expression.Escape = p.parsePrefix()
		expression.Escape = p.parsePostfix(expression.Escape)
	}
	expression.nodeBase.span.End = expression.Escape.SourceSpan().End
}

func (p *parser) parsePostfix(left Expr) Expr {
	for {
		if (p.options.Dialect == DialectOracle || p.options.Dialect == DialectRedshift) && p.peek().Text == "(" && p.pos+2 < len(p.tokens) && p.tokens[p.pos+1].Text == "+" && p.tokens[p.pos+2].Text == ")" {
			start := left.SourceSpan().Start
			p.advance()
			p.advance()
			p.advance()
			end := p.lastEnd
			left = &RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Raw: strings.TrimSpace(p.text[start:end])}
			continue
		}
		if p.options.Dialect == DialectSnowflake && p.matchText("!") {
			start := left.SourceSpan().Start
			end := p.lastEnd
			if p.isNameToken(p.peek()) {
				end = p.advance().Span.End
				if p.matchText("(") {
					_, end = p.captureBalancedFunctionArguments()
				}
			}
			left = &RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Raw: p.text[start:end]}
			continue
		}
		if (p.options.Dialect == DialectSnowflake || p.options.Dialect == DialectDatabricks) && p.hasSnowflakePathPrefix() {
			path, ok := p.parseSnowflakePath()
			if ok {
				left = &FunctionCallExpr{
					nodeBase: nodeBase{span: Span{Start: left.SourceSpan().Start, End: p.lastEnd}},
					Name:     []Identifier{{Text: "GET_PATH"}},
					Args:     []Expr{left, &LiteralExpr{KindValue: LiteralString, Raw: "'" + strings.ReplaceAll(path, "'", "''") + "'"}},
				}
				p.recordNode()
				continue
			}
		}
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
		if p.options.Dialect == DialectMaterialize && p.peek().Text == "[" {
			start := left.SourceSpan().Start
			end := p.captureMaterializeBracketSuffix()
			left = &RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Raw: strings.TrimSpace(p.text[start:end])}
			p.recordNode()
			continue
		}
		if p.matchText("[") {
			start := left.SourceSpan().Start
			var low, high, step Expr
			var indices []Expr
			slice := false
			if p.peek().Text != ":" && p.peek().Text != "]" {
				low = p.parseExpression(0)
			}
			if p.matchText(":") {
				slice = true
				if p.peek().Text == "-" && p.peekTextAfter(":") {
					p.advance()
					high = &LiteralExpr{nodeBase: nodeBase{span: p.tokens[p.pos-1].Span}, KindValue: LiteralNumber, Raw: "-1"}
					p.matchText(":")
					if p.peek().Text != "]" {
						step = p.parseExpression(0)
					}
				} else if p.peek().Text != "]" && p.peek().Text != ":" {
					high = p.parseExpression(0)
					if p.matchText(":") && p.peek().Text != "]" {
						step = p.parseExpression(0)
					}
				} else if p.matchText(":") && p.peek().Text != "]" {
					step = p.parseExpression(0)
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
			// DuckDB's parser normalizes an omitted stop in a reverse slice to
			// an explicit one. Retaining that normalization in the AST also
			// lets the generator emit the same canonical form as SQLGlot.
			if p.options.Dialect == DialectDuckDB && slice && high == nil && step != nil {
				high = &LiteralExpr{nodeBase: nodeBase{span: Span{Start: start, End: start}}, KindValue: LiteralNumber, Raw: "1"}
			}
			left = &IndexExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Target: left, Low: low, High: high, Step: step, Slice: slice, Indices: indices}
			p.recordNode()
			continue
		}
		if p.matchText(".") {
			if p.options.Dialect == DialectClickHouse && p.peek().Text == ":" && p.pos+1 < len(p.tokens) {
				next := p.tokens[p.pos+1]
				if next.Kind == TokenQuotedIdentifier || next.Kind == TokenString || next.Kind == TokenUnterminatedString {
					colon := p.advance()
					value := p.advance()
					field := Identifier{
						Text: ":" + value.Text,
						Span: Span{Start: colon.Span.Start, End: value.Span.End},
					}
					left = &FieldExpr{nodeBase: nodeBase{span: Span{Start: left.SourceSpan().Start, End: value.Span.End}}, Target: left, Field: field}
					p.recordNode()
					continue
				}
			}
			if p.options.Dialect == DialectClickHouse && p.matchText("^") {
				field, ok := p.parseIdentifier(true)
				if ok {
					field.Quoted = false
					field.Quote = 0
					field.Text = "^" + field.Text
					left = &FieldExpr{nodeBase: nodeBase{span: Span{Start: left.SourceSpan().Start, End: field.Span.End}}, Target: left, Field: field}
					p.recordNode()
					continue
				}
				p.reportExpectedIdentifier("after .^")
				return left
			}
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
			if p.options.Dialect == DialectOracle && p.peek().Text == "+" && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].Text == ")" {
				start := left.SourceSpan().Start
				p.advance()
				p.advance()
				end := p.lastEnd
				left = &RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Raw: strings.TrimSpace(p.text[start:end])}
				continue
			}
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

func (p *parser) hasSnowflakePathPrefix() bool {
	if p.peek().Kind == TokenParameter && strings.HasPrefix(p.peek().Text, ":") {
		return true
	}
	return p.peek().Text == ":" && p.pos+1 < len(p.tokens) && (p.isNameToken(p.tokens[p.pos+1]) || p.tokens[p.pos+1].Text == "[")
}

func (p *parser) parseSnowflakePath() (string, bool) {
	var path strings.Builder
	appendSegment := func(segment string) bool {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return false
		}
		if path.Len() > 0 {
			path.WriteByte('.')
		}
		path.WriteString(segment)
		return true
	}
	appendIdentifier := func(identifier Identifier) bool {
		if !identifier.Quoted {
			return appendSegment(identifier.Text)
		}
		content := strings.ReplaceAll(identifier.Text, `"`, `\"`)
		path.WriteString(`["`)
		path.WriteString(content)
		path.WriteString(`"]`)
		return true
	}

	if p.peek().Kind == TokenParameter && strings.HasPrefix(p.peek().Text, ":") {
		if !appendSegment(strings.TrimPrefix(p.advance().Text, ":")) {
			return "", false
		}
	} else if p.matchText(":") {
		if p.peek().Text == "[" {
			open := p.advance()
			depth := 1
			end := open.Span.End
			for p.peek().Kind != TokenEOF && depth > 0 {
				tok := p.advance()
				end = tok.Span.End
				if tok.Text == "[" {
					depth++
				} else if tok.Text == "]" {
					depth--
				}
			}
			if depth > 0 {
				p.report(Diagnostic{Severity: SeverityError, Code: "PARSE_UNCLOSED_BRACKET", Message: "unclosed JSON path index; expected ]", Span: Span{Start: end, End: end}, Found: p.peek().Kind, Recovery: RecoveryInserted})
			}
			path.WriteString(p.text[open.Span.Start:end])
		} else {
			segment, ok := p.parseIdentifier(true)
			if !ok || !appendIdentifier(segment) {
				return "", false
			}
		}
	} else {
		return "", false
	}

	for {
		if p.matchText("[") {
			start := p.peek().Span.Start
			depth := 1
			end := start
			for p.peek().Kind != TokenEOF && depth > 0 {
				tok := p.advance()
				if tok.Text == "[" {
					depth++
				} else if tok.Text == "]" {
					depth--
					if depth == 0 {
						end = tok.Span.Start
						break
					}
				}
				end = tok.Span.End
			}
			if depth > 0 {
				p.report(Diagnostic{
					Severity: SeverityError,
					Code:     "PARSE_UNCLOSED_BRACKET",
					Message:  "unclosed Snowflake JSON path index; expected ]",
					Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
					Found:    p.peek().Kind,
					Recovery: RecoveryInserted,
				})
			}
			path.WriteByte('[')
			path.WriteString(strings.TrimSpace(p.text[start:end]))
			path.WriteByte(']')
			continue
		}
		if p.matchText(".") {
			segment, ok := p.parseIdentifier(true)
			if !ok {
				return path.String(), true
			}
			if segment.Quoted {
				if !appendIdentifier(segment) {
					return path.String(), true
				}
			} else {
				path.WriteByte('.')
				path.WriteString(segment.Text)
			}
			continue
		}
		if p.peek().Kind == TokenParameter && strings.HasPrefix(p.peek().Text, ":") {
			if !appendSegment(strings.TrimPrefix(p.advance().Text, ":")) {
				return path.String(), true
			}
			continue
		}
		if p.matchText(":") {
			segment, ok := p.parseIdentifier(true)
			if !ok || !appendIdentifier(segment) {
				return path.String(), true
			}
			continue
		}
		break
	}
	return path.String(), true
}

func (p *parser) hasClosingAngle() bool {
	index := p.pos + 1
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	if index >= len(p.tokens) {
		return false
	}
	// A comparison such as `z < -1 OR ... > ...` must not be consumed as a
	// generic type. The first token after `<` is a reliable recovery signal
	// for the common operator-led and literal forms.
	if p.tokens[index].Kind == TokenNumber || p.tokens[index].Kind == TokenString || p.tokens[index].Kind == TokenOperator || p.tokens[index].Text == "(" {
		return false
	}
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
	p.splitDoubleGreaterToken()
	return p.matchText(">")
}

func (p *parser) splitDoubleGreaterToken() {
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
}

func (p *parser) consumeGenericRemainder() int {
	depth := 0
	end := p.lastEnd
	for p.peek().Kind != TokenEOF {
		tok := p.peek()
		if (tok.Text == ">" || tok.Text == ">>") && depth == 0 {
			break
		}
		if tok.Text == ">>" && depth == 1 {
			p.splitDoubleGreaterToken()
			tok = p.advance()
			end = tok.Span.End
			depth = 0
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
	asymmetric := false
	if !symmetric {
		asymmetric = p.matchWord("ASYMMETRIC")
	}
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
	return &BetweenExpr{nodeBase: nodeBase{span: Span{Start: start, End: high.SourceSpan().End}}, Value: left, Not: not, Low: low, High: high, Symmetric: symmetric, Asymmetric: asymmetric}
}

func (p *parser) parseIn(left Expr, not bool) Expr {
	start := left.SourceSpan().Start
	p.expectWord("IN", "in IN expression")
	if p.options.Dialect == DialectBigQuery && p.peek().IsWord("UNNEST") {
		bodyStart := p.peek().Span.Start
		p.parseExpression(0)
		if p.matchWord("AS") {
			p.parseIdentifier(true)
		}
		end := p.lastEnd
		operator := "IN"
		if not {
			operator = "NOT IN"
		}
		return &BinaryExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Left: left, Operator: operator, Right: &RawExpr{nodeBase: nodeBase{span: Span{Start: bodyStart, End: end}}, Raw: strings.TrimSpace(p.text[bodyStart:end])}}
	}
	if p.isExpressionBoundary() {
		// Keep an incomplete IN expression structurally useful for editor
		// consumers while reporting the token that would begin its RHS.
		p.expectText("(", "after IN")
		expression := &InExpr{
			nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}},
			Value:    left,
			Not:      not,
		}
		p.recordNode()
		return expression
	}
	if p.peek().Text != "(" {
		right := p.parseExpression(4)
		return &BinaryExpr{nodeBase: nodeBase{span: Span{Start: start, End: right.SourceSpan().End}}, Left: left, Operator: "IN", Right: right}
	}
	p.expectText("(", "after IN")
	expr := &InExpr{Value: left, Not: not}
	if p.isQueryStart() {
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
		modifier := p.parseSetModifier()
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
				SetModifier:   modifier,
				SetRight:      right,
				SetLeftParen:  true,
				SetRightParen: rightParenthesized,
			}
		} else {
			left.SetOperator = operator
			left.SetAll = all
			left.SetModifier = modifier
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
	if p.options.Dialect == DialectHive && p.isHiveQueryTailStart() {
		left.Tail = p.captureHiveQueryTail()
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
	if p.options.Dialect == DialectPostgreSQL && p.matchWord("JSON") {
		operator += " JSON"
		if p.peek().IsWord("VALUE") || p.peek().IsWord("SCALAR") || p.peek().IsWord("OBJECT") {
			operator += " " + strings.ToUpper(p.advance().Text)
		} else if p.matchWord("ARRAY") {
			operator += " ARRAY"
			if p.peek().IsWord("WITH") || p.peek().IsWord("WITHOUT") {
				operator += " " + strings.ToUpper(p.advance().Text)
				if p.matchWord("UNIQUE") {
					operator += " UNIQUE"
					if p.matchWord("KEYS") {
						operator += " KEYS"
					}
				}
			} else if p.matchWord("UNIQUE") {
				operator += " UNIQUE"
				if p.matchWord("KEYS") {
					operator += " KEYS"
				}
			}
		}
		return &IsExpr{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Value: left, Operator: operator, Right: &RawExpr{Raw: ""}}
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
	if tok.IsWord("NOT") || tok.Text == "+" || tok.Text == "-" || tok.Text == "~" || tok.Text == "|/" || tok.Text == "||/" {
		p.advance()
		right := p.parseExpression(7)
		return &UnaryExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: right.SourceSpan().End}}, Operator: tok.Text, Expr: right}
	}
	if p.options.Dialect == DialectOracle && (tok.IsWord("PRIOR") || tok.IsWord("CONNECT_BY_ROOT")) {
		start := p.advance().Span.Start
		operand := p.parsePrefix()
		operand = p.parsePostfix(operand)
		return &RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: operand.SourceSpan().End}}, Raw: strings.TrimSpace(p.text[start:operand.SourceSpan().End])}
	}
	if tok.Text == "(" {
		p.advance()
		if p.peek().Text == "(" && p.isParenthesizedSetQuery() {
			query := p.parseParenthesizedQueryStatement()
			p.expectText(")", "after nested scalar subquery")
			return &SubqueryExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Query: query, Parenthesized: true}
		}
		if p.isQueryStart() {
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
			return &SubqueryExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Query: query, Parenthesized: true}
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
	if tok.Text == "*" && p.options.Dialect == DialectDuckDB && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].IsWord("COLUMNS") {
		start := p.advance().Span.Start
		p.matchWord("COLUMNS")
		if p.matchText("(") {
			rawArgs, end := p.captureBalancedFunctionArguments()
			return &RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Raw: "*COLUMNS" + rawArgs}
		}
	}
	if tok.Text == "*" {
		p.advance()
		return &StarExpr{nodeBase: nodeBase{span: tok.Span}}
	}
	if tok.Text == "[" {
		if p.options.Dialect == DialectDuckDB && p.duckDBBracketContainsFor() {
			return p.parseDuckDBRawBracket()
		}
		p.advance()
		var items []Expr
		for p.peek().Kind != TokenEOF && p.peek().Text != "]" {
			items = append(items, p.parseExpression(0))
			if !p.matchText(",") {
				break
			}
		}
		p.expectText("]", "to close array literal")
		return &FunctionCallExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Name: []Identifier{{Text: "ARRAY"}}, Args: items, ArrayLiteral: true}
	}
	switch tok.Kind {
	case TokenString, TokenUnterminatedString:
		p.advance()
		return &LiteralExpr{nodeBase: nodeBase{span: tok.Span}, KindValue: LiteralString, Raw: tok.Text}
	case TokenNumber:
		p.advance()
		if p.options.Dialect == DialectClickHouse && strings.HasSuffix(tok.Text, "_") && p.peek().Kind == TokenIdentifier && tok.Span.End == p.peek().Span.Start {
			tail := p.advance()
			return &RawExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: tail.Span.End}}, Raw: tok.Text + tail.Text}
		}
		if p.options.Dialect == DialectHive && p.peek().Kind == TokenIdentifier && tok.Span.End == p.peek().Span.Start {
			suffix := p.peek().Text
			typeName := ""
			switch strings.ToUpper(suffix) {
			case "S":
				typeName = "SMALLINT"
			case "Y":
				typeName = "TINYINT"
			case "L":
				typeName = "BIGINT"
			case "BD":
				typeName = "DECIMAL"
			}
			if typeName != "" {
				tail := p.advance()
				return &TypedLiteralExpr{
					nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: tail.Span.End}},
					TypeName: []Identifier{{Text: typeName}},
					Value:    &LiteralExpr{nodeBase: nodeBase{span: tok.Span}, KindValue: LiteralNumber, Raw: tok.Text},
				}
			}
		}
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
	if p.options.Dialect == DialectDuckDB && tok.IsWord("MAP") && p.peekTextAfter("{") {
		start := p.advance().Span.Start
		p.matchText("{")
		depth := 1
		end := p.lastEnd
		for depth > 0 && p.peek().Kind != TokenEOF {
			part := p.advance()
			end = part.Span.End
			if part.Text == "{" {
				depth++
			} else if part.Text == "}" {
				depth--
			}
		}
		return &RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Raw: p.text[start:end]}
	}
	if tok.Text == "{" {
		start := p.advance().Span.Start
		depth := 1
		end := p.lastEnd
		for depth > 0 && p.peek().Kind != TokenEOF {
			part := p.advance()
			end = part.Span.End
			if part.Text == "{" {
				depth++
			} else if part.Text == "}" {
				depth--
			}
		}
		if depth > 0 {
			p.report(Diagnostic{Severity: SeverityError, Code: "PARSE_UNCLOSED_BRACE", Message: "unclosed map or struct literal; expected }", Span: Span{Start: end, End: end}, Found: p.peek().Kind, Recovery: RecoveryInserted})
		}
		if p.options.Dialect == DialectExasol {
			raw := strings.TrimSpace(p.text[start:end])
			if len(raw) >= 4 && raw[0] == '{' && raw[len(raw)-1] == '}' {
				body := raw[1 : len(raw)-1]
				if len(body) >= 3 && (strings.HasPrefix(strings.ToLower(body), "d'") || strings.HasPrefix(strings.ToLower(body), "ts'")) && strings.HasSuffix(body, "'") {
					kind := "TO_DATE"
					prefixLength := 2
					if strings.HasPrefix(strings.ToLower(body), "ts'") {
						kind = "TO_TIMESTAMP"
						prefixLength = 3
					}
					value := body[prefixLength : len(body)-1]
					return &FunctionCallExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Name: []Identifier{{Text: kind}}, Args: []Expr{&LiteralExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, KindValue: LiteralString, Raw: "'" + strings.ReplaceAll(value, "'", "''") + "'"}}}
				}
			}
		}
		return &RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Raw: p.text[start:end]}
	}
	if p.isNameToken(tok) && (!p.isStructuralKeyword(tok) || tok.IsWord("END") || tok.IsWord("GROUP") || tok.IsWord("LEFT") || tok.IsWord("RIGHT") || tok.IsWord("OVERLAPS") || tok.IsWord("STRAIGHT_JOIN") || (tok.IsWord("VALUES") && p.peekTextAfter(".")) || (tok.IsWord("REPLACE") && p.peekTextAfter("(")) || (tok.IsWord("EXCLUDE") && (p.peekTextAfter(":=") || p.options.Dialect == DialectRedshift)) || (p.options.Dialect == DialectClickHouse && (p.peekTextAfter("(") || tok.IsWord("LIKE"))) || ((p.options.Dialect == DialectSQLite || p.options.Dialect == DialectSpark || p.options.Dialect == DialectDatabricks || p.options.Dialect == DialectHive) && (tok.IsWord("LIKE") || tok.IsWord("ILIKE") || tok.IsWord("GLOB")) && p.peekTextAfter("(")) || p.options.Dialect == DialectSnowflake && (tok.IsWord("REPLACE") || tok.IsWord("EXCLUDE") || tok.IsWord("RENAME") || tok.IsWord("LIKE") || tok.IsWord("ILIKE") || tok.IsWord("GROUP"))) {
		parts, _ := p.parseNameParts()
		if p.options.Dialect == DialectDuckDB && len(parts) == 1 && strings.EqualFold(parts[0].Text, "COLUMNS") && p.matchText("(") {
			rawArgs, end := p.captureBalancedFunctionArguments()
			return &FunctionCallExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: end}}, Name: parts, RawArgs: rawArgs}
		}
		if p.options.Dialect == DialectDuckDB && len(parts) == 1 && strings.EqualFold(parts[0].Text, "ARRAY") && p.peek().Text == "[" {
			return p.parseDuckDBArrayLiteral(tok.Span.Start)
		}
		if (p.options.Dialect == DialectSnowflake || p.options.Dialect == DialectTeradata || p.options.Dialect == DialectRedshift) && len(parts) == 1 && strings.EqualFold(parts[0].Text, "X") && p.peek().Kind == TokenString && parts[0].Span.End == p.peek().Span.Start {
			value := p.advance()
			return &RawExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: value.Span.End}}, Raw: p.text[tok.Span.Start:value.Span.End]}
		}
		if p.options.Dialect == DialectSnowflake && len(parts) == 1 && strings.EqualFold(parts[0].Text, "CONNECT_BY_ROOT") && !p.isExpressionBoundary() && p.peek().Kind != TokenEOF {
			operand := p.parsePrefix()
			operand = p.parsePostfix(operand)
			return &RawExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: operand.SourceSpan().End}}, Raw: p.text[tok.Span.Start:operand.SourceSpan().End]}
		}
		if len(parts) == 1 && strings.EqualFold(parts[0].Text, "NEXT") && p.matchWord("VALUE") {
			p.expectWord("FOR", "after NEXT VALUE")
			p.parseNameParts()
			if p.matchWord("OVER") {
				p.parseWindowSpecPtr()
			}
			return &RawExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Raw: strings.TrimSpace(p.text[tok.Span.Start:p.lastEnd])}
		}
		if len(parts) == 1 && strings.EqualFold(parts[0].Text, "E") && p.peek().Kind == TokenString && parts[0].Span.End == p.peek().Span.Start {
			value := p.advance()
			return &RawExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: value.Span.End}}, Raw: "e" + value.Text}
		}
		if len(parts) == 1 && strings.EqualFold(parts[0].Text, "R") && p.options.Dialect == DialectDatabricks && p.peek().Kind == TokenQuotedIdentifier && parts[0].Span.End == p.peek().Span.Start {
			value := p.advance()
			raw := value.Text
			if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
				content := raw[1 : len(raw)-1]
				content = strings.ReplaceAll(content, `\`, `\\`)
				content = strings.ReplaceAll(content, "'", "''")
				raw = "'" + content + "'"
			}
			return &LiteralExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: value.Span.End}}, KindValue: LiteralString, Raw: raw}
		}
		if len(parts) == 1 && strings.EqualFold(parts[0].Text, "R") && p.options.Dialect == DialectSpark && p.peek().Kind == TokenString && parts[0].Span.End == p.peek().Span.Start {
			value := p.advance()
			return &LiteralExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: value.Span.End}}, KindValue: LiteralString, Raw: value.Text}
		}
		if len(parts) == 1 && p.options.Dialect == DialectBigQuery && p.peek().Kind == TokenString && parts[0].Span.End == p.peek().Span.Start && strings.EqualFold(parts[0].Text, "B") {
			value := p.advance()
			return &RawExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: value.Span.End}}, Raw: "b" + value.Text}
		}
		if len(parts) == 1 && p.options.Dialect == DialectBigQuery && p.peek().Kind == TokenString && parts[0].Span.End == p.peek().Span.Start && strings.EqualFold(parts[0].Text, "R") {
			value := p.advance()
			return &LiteralExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: value.Span.End}}, KindValue: LiteralString, Raw: value.Text}
		}
		if len(parts) == 1 && strings.EqualFold(parts[0].Text, "EXISTS") && p.queryStartsAfterParen() && p.matchText("(") {
			var query *SelectStmt
			if p.isQueryStart() {
				query = p.parseSelect()
			} else {
				p.reportExpectedQuery("inside EXISTS")
			}
			p.expectText(")", "to close EXISTS")
			return &ExistsExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Query: query}
		}
		if len(parts) == 1 && (strings.EqualFold(parts[0].Text, "ANY") || strings.EqualFold(parts[0].Text, "ALL") || strings.EqualFold(parts[0].Text, "SOME")) && p.peek().Text == "(" && p.queryStartsAfterParen() && p.matchText("(") {
			spaceBeforeParen := p.peek().Span.Start > parts[0].Span.End
			var query *SelectStmt
			if p.isQueryStart() {
				query = p.parseSelect()
			} else {
				p.reportExpectedQuery("inside " + parts[0].Text)
			}
			p.expectText(")", "to close "+parts[0].Text)
			keyword := strings.ToUpper(parts[0].Text)
			if keyword == "SOME" {
				keyword = "ANY"
			}
			return &QuantifiedExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Keyword: keyword, Query: query, SpaceBeforeParen: spaceBeforeParen}
		}
		if len(parts) == 1 && strings.EqualFold(parts[0].Text, "IF") && p.peek().IsWord("THEN") == false && p.peek().Text != "(" && !p.isExpressionBoundary() {
			condition := p.parseRequiredExpr("after IF")
			p.expectWord("THEN", "after IF condition")
			result := p.parseRequiredExpr("after IF THEN")
			var elseExpr Expr
			if p.matchWord("ELSE") {
				elseExpr = p.parseRequiredExpr("after IF ELSE")
			}
			if p.options.Dialect == DialectExasol {
				p.expectWord("ENDIF", "to close IF expression")
			} else {
				p.expectWord("END", "to close IF expression")
			}
			return &CaseExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Whens: []CaseWhen{{Condition: condition, Result: result}}, Else: elseExpr}
		}
		if p.options.Dialect == DialectOracle && len(parts) == 1 && strings.EqualFold(parts[0].Text, "CAST") && p.peek().Text == "(" && p.oracleCastNeedsRawArguments() {
			p.advance()
			_, end := p.captureBalancedFunctionArguments()
			return &RawExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: end}}, Raw: p.text[tok.Span.Start:end]}
		}
		if len(parts) == 1 && (strings.EqualFold(parts[0].Text, "CAST") || strings.EqualFold(parts[0].Text, "TRY_CAST")) && p.matchText("(") {
			return p.parseCast(tok, parts[0].Text)
		}
		if len(parts) == 1 && strings.EqualFold(parts[0].Text, "EXTRACT") && p.options.Dialect != DialectClickHouse && p.matchText("(") {
			field := p.parseRequiredExpr("inside EXTRACT")
			if p.options.Dialect == DialectSnowflake && p.matchText(",") {
				source := p.parseRequiredExpr("after EXTRACT field")
				p.expectText(")", "to close EXTRACT")
				return &ExtractExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Field: field, Source: source}
			}
			p.expectWord("FROM", "inside EXTRACT")
			source := p.parseRequiredExpr("after EXTRACT FROM")
			p.expectText(")", "to close EXTRACT")
			return &ExtractExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Field: field, Source: source}
		}
		if len(parts) == 1 && strings.EqualFold(parts[0].Text, "INTERVAL") && p.intervalValueStart() {
			return p.parseInterval(tok, parts)
		}
		if p.isTypedLiteralName(parts) && !(p.peek().Text == "(" && isFunctionLikeTypeName(parts)) {
			var parameters []Expr
			if p.matchText("(") {
				parameters = p.parseCallArguments()
			}
			qualifiers := p.parseTypeQualifiers()
			if p.peek().Kind == TokenString || p.peek().Kind == TokenUnterminatedString || p.peek().Kind == TokenNumber || p.peek().Kind == TokenParameter {
				valueToken := p.advance()
				kind := LiteralString
				if valueToken.Kind == TokenNumber {
					kind = LiteralNumber
				} else if valueToken.Kind == TokenParameter {
					kind = LiteralParameter
				}
				value := &LiteralExpr{nodeBase: nodeBase{span: valueToken.Span}, KindValue: kind, Raw: valueToken.Text}
				if len(parts) == 1 && strings.EqualFold(parts[0].Text, "TIMESTAMP") && len(qualifiers) == 0 && (p.options.Dialect == DialectPresto || p.options.Dialect == DialectTrino || p.options.Dialect == DialectAthena) && timestampLiteralHasZone(value.Raw) {
					qualifiers = []string{"WITH", "TIME", "ZONE"}
				}
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
		if (p.options.Dialect == DialectOracle || p.options.Dialect == DialectRedshift) && p.peek().Text == "(" && p.pos+2 < len(p.tokens) && p.tokens[p.pos+1].Text == "+" && p.tokens[p.pos+2].Text == ")" {
			p.advance()
			p.advance()
			end := p.advance().Span.End
			return &RawExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: end}}, Raw: strings.TrimSpace(p.text[tok.Span.Start:end])}
		}
		if p.matchText("(") {
			if p.options.Dialect == DialectSnowflake && len(parts) == 1 && strings.EqualFold(parts[0].Text, "DATE_PART") && !p.peek().IsWord("DISTINCT") {
				position := p.pos
				lastEnd := p.lastEnd
				diagnosticCount := len(p.diagnostics)
				field := p.parseRequiredExpr("inside DATE_PART")
				if p.matchWord("FROM") {
					source := p.parseRequiredExpr("after DATE_PART FROM")
					p.expectText(")", "to close DATE_PART")
					return &FunctionCallExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Name: parts, Args: []Expr{field, source}}
				}
				p.pos = position
				p.lastEnd = lastEnd
				p.diagnostics = p.diagnostics[:diagnosticCount]
			}
			if p.options.Dialect == DialectSnowflake && len(parts) == 1 && strings.EqualFold(parts[0].Text, "SEMANTIC_VIEW") {
				rawArgs, end := p.captureBalancedFunctionArguments()
				return &FunctionCallExpr{
					nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: end}},
					Name:     parts,
					RawArgs:  rawArgs,
				}
			}
			if (p.options.Dialect == DialectBigQuery || p.options.Dialect == DialectSnowflake) && p.requiresRawFunctionArguments() {
				rawArgs, end := p.captureBalancedFunctionArguments()
				return &FunctionCallExpr{
					nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: end}},
					Name:     parts,
					RawArgs:  rawArgs,
				}
			}
			distinct := p.matchWord("DISTINCT")
			args, orderBy, having, argumentTail, ignoreNulls, respectNulls, nullsInside := p.parseFunctionArguments()
			function := &FunctionCallExpr{nodeBase: nodeBase{span: Span{Start: tok.Span.Start, End: p.lastEnd}}, Name: parts, Distinct: distinct, Args: args, OrderBy: orderBy, Having: having, ArgumentTail: argumentTail, IgnoreNulls: ignoreNulls, RespectNulls: respectNulls, NullsInside: nullsInside}
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
				if !p.matchWord("WHERE") && p.options.Dialect != DialectDuckDB {
					p.reportExpectedWord("WHERE", "inside FILTER")
				}
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
			if p.options.Dialect == DialectSnowflake && len(parts) == 1 && strings.EqualFold(parts[0].Text, "NTH_VALUE") && (p.peekWords("FROM", "FIRST") || p.peekWords("FROM", "LAST")) {
				// Snowflake places NTH_VALUE's FROM FIRST/LAST modifier after
				// the closing argument parenthesis. Without this lookahead, the
				// ordinary SELECT parser treats FROM FIRST as the query source.
				p.advance() // FROM
				p.advance() // FIRST or LAST; targets do not retain this modifier.
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
				}
				function.nodeBase.span.End = p.lastEnd
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

func (p *parser) duckDBBracketContainsFor() bool {
	depth := 0
	for index := p.pos; index < len(p.tokens); index++ {
		tok := p.tokens[index]
		if tok.Kind == TokenComment {
			continue
		}
		switch tok.Text {
		case "[":
			depth++
		case "]":
			depth--
			if depth == 0 {
				return false
			}
		default:
			if depth == 1 && tok.IsWord("FOR") {
				return true
			}
		}
	}
	return false
}

func (p *parser) parseDuckDBRawBracket() Expr {
	start := p.advance().Span.Start
	depth := 1
	end := p.lastEnd
	for depth > 0 && p.peek().Kind != TokenEOF {
		tok := p.advance()
		end = tok.Span.End
		if tok.Text == "[" {
			depth++
		} else if tok.Text == "]" {
			depth--
		}
	}
	if depth > 0 {
		p.report(Diagnostic{Severity: SeverityError, Code: "PARSE_UNCLOSED_BRACKET", Message: "unclosed DuckDB list comprehension; expected ]", Span: Span{Start: end, End: end}, Found: p.peek().Kind, Recovery: RecoveryInserted})
	}
	return &RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Raw: strings.TrimSpace(p.text[start:end])}
}

func (p *parser) parseDuckDBArrayLiteral(start int) Expr {
	if !p.matchText("[") {
		return &IdentifierExpr{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Parts: []Identifier{{Text: "ARRAY"}}}
	}
	var items []Expr
	for p.peek().Kind != TokenEOF && p.peek().Text != "]" {
		items = append(items, p.parseExpression(0))
		if !p.matchText(",") {
			break
		}
	}
	p.expectText("]", "to close DuckDB ARRAY literal")
	return &FunctionCallExpr{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Name: []Identifier{{Text: "ARRAY"}}, Args: items, ArrayLiteral: true}
}

func timestampLiteralHasZone(raw string) bool {
	value := strings.Trim(raw, "'")
	return strings.Contains(value, "/") || strings.Contains(value, " +") || strings.Contains(value, " -")
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
	if p.options.Dialect == DialectClickHouse && p.matchText(",") {
		typeExpr := p.parseRequiredExpr("after comma in " + keyword)
		p.expectText(")", "to close "+keyword)
		return &FunctionCallExpr{
			nodeBase: nodeBase{span: Span{Start: start.Span.Start, End: p.lastEnd}},
			Name:     []Identifier{{Text: strings.ToUpper(keyword)}},
			Args:     []Expr{value, typeExpr},
		}
	}
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
	if len(parts) == 1 && (strings.EqualFold(parts[0].Text, "CHARACTER") || strings.EqualFold(parts[0].Text, "CHAR") || strings.EqualFold(parts[0].Text, "NCHAR")) && p.peek().IsWord("VARYING") {
		p.advance()
		parts[0].Text = "CHARACTER VARYING"
	}
	if p.options.Dialect == DialectExasol && len(parts) == 1 && strings.EqualFold(parts[0].Text, "LONG") && p.peek().IsWord("VARCHAR") {
		p.advance()
		parts[0].Text = "LONG VARCHAR"
	}
	var typeExpr Expr = &IdentifierExpr{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Parts: parts}
	if p.options.Dialect == DialectMaterialize && p.peek().Text == "[" {
		end := p.captureMaterializeBracketSuffix()
		typeExpr = &RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Raw: strings.TrimSpace(p.text[start:end])}
		return typeExpr, nil
	}
	if p.matchText("(") {
		var args []Expr
		if isStructuredCastType(parts) {
			raw, end := p.captureBalancedFunctionArguments()
			inner := raw
			if len(inner) >= 2 && inner[0] == '(' && inner[len(inner)-1] == ')' {
				inner = inner[1 : len(inner)-1]
			}
			args = []Expr{&RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Raw: strings.TrimSpace(inner)}}
		} else {
			args = p.parseCallArguments()
		}
		typeExpr = &CallExpr{nodeBase: nodeBase{span: Span{Start: start, End: p.lastEnd}}, Callee: typeExpr, Args: args}
	}
	typeExpr = p.parsePostfix(typeExpr)
	var suffix []Identifier
	if p.options.Dialect == DialectMySQL && len(parts) == 1 && (strings.EqualFold(parts[0].Text, "SIGNED") || strings.EqualFold(parts[0].Text, "UNSIGNED")) {
		// MySQL accepts SIGNED/UNSIGNED INTEGER, but its canonical cast type
		// is the shorter SIGNED/UNSIGNED spelling.
		p.matchWord("INTEGER")
	}
	if p.options.Dialect == DialectMySQL && len(parts) == 1 && (strings.EqualFold(parts[0].Text, "CHAR") || strings.EqualFold(parts[0].Text, "CHARACTER")) && p.matchWord("CHARACTER") {
		p.expectWord("SET", "after CHARACTER in CAST type")
		suffix = append(suffix, Identifier{Text: "CHARACTER SET"})
		if charset, ok := p.parseIdentifier(true); ok {
			suffix = append(suffix, charset)
		}
	}
	if p.options.Dialect == DialectMaterialize {
		for p.peek().IsWord("LIST") {
			suffix = append(suffix, Identifier{Text: strings.ToUpper(p.advance().Text), Span: p.tokens[p.pos-1].Span})
		}
	}
	if p.peek().IsWord("WITH") || p.peek().IsWord("WITHOUT") {
		for p.isNameToken(p.peek()) && !p.isStructuralKeyword(p.peek()) && p.peek().Text != ")" {
			identifier, ok := p.parseIdentifier(true)
			if !ok {
				break
			}
			suffix = append(suffix, identifier)
		}
	} else if len(parts) == 1 && strings.EqualFold(parts[0].Text, "DOUBLE") && p.peek().IsWord("PRECISION") {
		identifier, _ := p.parseIdentifier(true)
		suffix = append(suffix, identifier)
	} else if len(parts) == 1 && strings.EqualFold(parts[0].Text, "INTERVAL") && p.isNameToken(p.peek()) && !p.isStructuralKeyword(p.peek()) {
		for p.isNameToken(p.peek()) && p.peek().Text != ")" {
			if p.isStructuralKeyword(p.peek()) && !p.peek().IsWord("TO") {
				break
			}
			identifier, ok := p.parseIdentifier(true)
			if !ok {
				break
			}
			suffix = append(suffix, identifier)
		}
	}
	if p.options.Dialect == DialectSnowflake && (p.peek().IsWord("RENAME") || p.peek().IsWord("ADD")) {
		start := p.advance().Span.Start
		end := p.lastEnd
		if p.isNameToken(p.peek()) {
			end = p.advance().Span.End
		}
		suffix = append(suffix, Identifier{Text: strings.TrimSpace(p.text[start:end]), Span: Span{Start: start, End: end}})
	}
	if p.options.Dialect == DialectBigQuery && p.matchWord("FORMAT") {
		parenthesized := p.matchText("(")
		if p.peek().Kind == TokenString || p.peek().Kind == TokenUnterminatedString {
			format := p.advance()
			suffix = append(suffix, Identifier{Text: "FORMAT " + format.Text})
			if parenthesized {
				p.expectText(")", "after FORMAT string")
			}
		}
	}
	if p.options.Dialect == DialectBigQuery && p.peekWords("AT", "TIME", "ZONE") {
		p.advance()
		p.advance()
		p.advance()
		if p.peek().Kind == TokenString || p.peek().Kind == TokenUnterminatedString {
			suffix = append(suffix, Identifier{Text: "AT TIME ZONE " + p.advance().Text})
		}
	}
	for p.matchWord("COLLATE") {
		suffix = append(suffix, Identifier{Text: "COLLATE"})
		if p.options.Dialect == DialectSingleStore && (p.peek().Kind == TokenString || p.peek().Kind == TokenUnterminatedString) {
			suffix = append(suffix, Identifier{Text: p.advance().Text})
		} else if identifier, ok := p.parseIdentifier(true); ok {
			suffix = append(suffix, identifier)
		} else {
			p.reportExpectedIdentifier("after COLLATE in CAST")
			break
		}
	}
	return typeExpr, suffix
}

func (p *parser) parseSingleStoreCastType() (Expr, []Identifier) {
	start := p.peek().Span.Start
	if p.peek().Kind == TokenParameter || p.peek().Kind == TokenNumber || p.peek().Text == "%" {
		end := p.advance().Span.End
		if p.peek().Span.Start == end && p.isNameToken(p.peek()) {
			end = p.advance().Span.End
		}
		return &RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Raw: strings.TrimSpace(p.text[start:end])}, nil
	}
	if p.isNameToken(p.peek()) && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].Text == "(" {
		p.advance()
		p.advance()
		_, end := p.captureBalancedFunctionArguments()
		return &RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Raw: strings.TrimSpace(p.text[start:end])}, nil
	}
	return p.parseCastType()
}

func (p *parser) captureMaterializeBracketSuffix() int {
	depth := 0
	end := p.lastEnd
	for p.peek().Kind != TokenEOF {
		tok := p.advance()
		end = tok.Span.End
		switch tok.Text {
		case "[":
			depth++
		case "]":
			depth--
			if depth == 0 {
				return end
			}
		}
	}
	if depth > 0 {
		p.report(Diagnostic{Severity: SeverityError, Code: "PARSE_UNCLOSED_BRACKET", Message: "unclosed Materialize type; expected ]", Span: Span{Start: end, End: end}, Found: p.peek().Kind, Recovery: RecoveryInserted})
	}
	return end
}

func isStructuredCastType(parts []Identifier) bool {
	if len(parts) != 1 {
		return false
	}
	switch strings.ToUpper(parts[0].Text) {
	case "ARRAY", "LIST", "MAP", "OBJECT", "ROW", "STRUCT", "NESTED", "JSON", "TUPLE", "VARCHAR2", "NVARCHAR2", "NUMBER", "BINARY_FLOAT", "BINARY_DOUBLE":
		return true
	default:
		return false
	}
}

func (p *parser) intervalValueStart() bool {
	tok := p.peek()
	return tok.Kind == TokenString || tok.Kind == TokenUnterminatedString || tok.Kind == TokenNumber || tok.Text == "(" || tok.Text == "+" || tok.Text == "-" || (p.isNameToken(tok) && !p.isStructuralKeyword(tok) && !tok.IsWord("AS") && !isWindowFrameWord(tok))
}

func isWindowFrameWord(tok Token) bool {
	for _, word := range []string{"ROWS", "RANGE", "GROUPS", "PRECEDING", "FOLLOWING", "CURRENT", "EXCLUDE"} {
		if tok.IsWord(word) {
			return true
		}
	}
	return false
}

func (p *parser) parseInterval(start Token, parts []Identifier) Expr {
	value := p.parsePrefix()
	value = p.parsePostfix(value)
	if p.options.Dialect == DialectSpark {
		qualifier := p.parseIntervalQualifier()
		current := Expr(&IntervalExpr{
			nodeBase:   nodeBase{span: Span{Start: start.Span.Start, End: value.SourceSpan().End}},
			Value:      value,
			Qualifiers: qualifier,
		})
		for {
			hadPlus := p.matchText("+")
			if !hadPlus && !p.isIntervalAmountStart() {
				break
			}
			partStart := p.peek().Span.Start
			partValue := p.parsePrefix()
			partValue = p.parsePostfix(partValue)
			partQualifier := p.parseIntervalQualifier()
			if len(partQualifier) == 0 {
				// Do not consume a non-interval expression following an
				// interval. If a plus was present, keep the parser recoverable
				// by retaining the missing qualifier diagnostic.
				if hadPlus {
					p.reportExpectedIdentifier("after + in INTERVAL")
				}
				_ = partStart
				break
			}
			partEnd := partValue.SourceSpan().End
			partEnd = partQualifier[len(partQualifier)-1].SourceSpan().End
			current = &BinaryExpr{
				nodeBase: nodeBase{span: Span{Start: current.SourceSpan().Start, End: partEnd}},
				Left:     current,
				Operator: "+",
				Right: &IntervalExpr{
					nodeBase:   nodeBase{span: Span{Start: partValue.SourceSpan().Start, End: partEnd}},
					Value:      partValue,
					Qualifiers: partQualifier,
				},
			}
		}
		return current
	}
	var qualifiers []Expr
	for p.isNameToken(p.peek()) && !p.isStructuralKeyword(p.peek()) && !p.peek().IsWord("AS") && !isWindowFrameWord(p.peek()) {
		qualifier := p.parsePrefix()
		qualifier = p.parsePostfix(qualifier)
		qualifiers = append(qualifiers, qualifier)
	}
	if len(qualifiers) == 0 {
		if literal, ok := value.(*LiteralExpr); ok && literal.KindValue == LiteralString {
			fields := strings.Fields(strings.Trim(literal.Raw, "'"))
			if len(fields) == 2 {
				literal.Raw = "'" + fields[0] + "'"
				qualifiers = append(qualifiers, identifierExpr(strings.ToUpper(fields[1])))
			}
		}
	}
	end := value.SourceSpan().End
	if len(qualifiers) > 0 {
		end = qualifiers[len(qualifiers)-1].SourceSpan().End
	}
	return &IntervalExpr{nodeBase: nodeBase{span: Span{Start: start.Span.Start, End: end}}, Value: value, Qualifiers: qualifiers}
}

func (p *parser) parseIntervalQualifier() []Expr {
	if !p.isNameToken(p.peek()) || p.isStructuralKeyword(p.peek()) || p.peek().IsWord("AS") {
		return nil
	}
	qualifier := p.parsePrefix()
	qualifier = p.parsePostfix(qualifier)
	return []Expr{qualifier}
}

func (p *parser) isIntervalAmountStart() bool {
	tok := p.peek()
	return tok.Kind == TokenString || tok.Kind == TokenUnterminatedString || tok.Kind == TokenNumber || tok.Text == "+" || tok.Text == "-"
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
	case "DATE", "TIME", "TIMESTAMP", "DATETIME", "JSON", "INTERVAL", "UUID", "INET", "POINT", "N",
		"INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "FLOAT", "DOUBLE", "DECIMAL", "NUMERIC",
		"BOOLEAN", "BOOL", "VARCHAR", "CHAR", "STRING", "TEXT":
		return true
	default:
		return false
	}
}

func isFunctionLikeTypeName(parts []Identifier) bool {
	if len(parts) != 1 {
		return false
	}
	switch strings.ToUpper(parts[0].Text) {
	case "DATE", "CHAR", "VARCHAR", "STRING", "TEXT", "INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "FLOAT", "DOUBLE", "DECIMAL", "NUMERIC", "BOOLEAN", "BOOL", "UUID":
		return true
	default:
		return false
	}
}

// oracleCastNeedsRawArguments reports whether an Oracle CAST contains
// dialect-specific clauses that are not represented by the generic CAST AST
// yet (for example DEFAULT ... ON CONVERSION ERROR). Keeping the complete
// argument text makes identity generation lossless while still allowing the
// ordinary typed CAST path for standard casts.
func (p *parser) oracleCastNeedsRawArguments() bool {
	depth := 0
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
				return false
			}
		}
		if depth > 0 && token.IsWord("DEFAULT") {
			return true
		}
	}
	return false
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
			if (p.options.Dialect == DialectOracle || p.options.Dialect == DialectPresto || p.options.Dialect == DialectTrino) && depth == 1 && (token.IsWord("KEY") || token.IsWord("IS")) {
				return true
			}
			if depth == 1 {
				content = true
			}
		}
	}
	return true
}

func (p *parser) parseCallArguments() []Expr {
	var args []Expr
	starIsColumns := p.options.Dialect == DialectDuckDB && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].IsWord("COLUMNS")
	if p.peek().Text == "*" && !starIsColumns && p.matchText("*") {
		// A star argument is represented on the function node by the caller
		// only for future specialized handling; preserving it as an expression
		// keeps the generic parser composable.
		args = append(args, &StarExpr{nodeBase: nodeBase{span: p.tokens[p.pos-1].Span}})
		p.expectText(")", "to close function call")
		return args
	}
	if p.isQueryStart() {
		query := p.parseSelect()
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
		return []Expr{&SubqueryExpr{nodeBase: nodeBase{span: query.SourceSpan()}, Query: query}}
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

func (p *parser) requiresRawFunctionArguments() bool {
	depth := 1
	firstArgument := true
	for index := p.pos; index < len(p.tokens); index++ {
		token := p.tokens[index]
		switch token.Text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return false
			}
		case "=>":
			if depth == 1 {
				return true
			}
		}
		if depth == 1 && token.Kind != TokenComment {
			if firstArgument && (token.IsWord("MODEL") || token.IsWord("TABLE") || (p.options.Dialect == DialectSnowflake && token.IsWord("METRICS"))) {
				return true
			}
			if token.Text != "," {
				firstArgument = false
			}
		}
	}
	return false
}

func (p *parser) captureBalancedFunctionArguments() (string, int) {
	open := p.tokens[p.pos-1]
	depth := 1
	end := open.Span.End
	for depth > 0 && p.peek().Kind != TokenEOF {
		token := p.advance()
		end = token.Span.End
		switch token.Text {
		case "(":
			depth++
		case ")":
			depth--
		}
	}
	if depth > 0 {
		p.report(Diagnostic{
			Severity: SeverityError,
			Code:     "PARSE_UNCLOSED_PAREN",
			Message:  "unclosed function call; expected )",
			Span:     Span{Start: p.peek().Span.Start, End: p.peek().Span.Start},
			Found:    p.peek().Kind,
			Recovery: RecoveryInserted,
		})
	}
	return p.text[open.Span.Start:end], end
}

func (p *parser) parseFunctionArguments() ([]Expr, []OrderItem, Expr, string, bool, bool, bool) {
	var args []Expr
	var orderBy []OrderItem
	var having Expr
	var argumentTail string
	var ignoreNulls, respectNulls bool
	nullsInside := false
	starIsColumns := p.options.Dialect == DialectDuckDB && p.pos+1 < len(p.tokens) && p.tokens[p.pos+1].IsWord("COLUMNS")
	if !starIsColumns && p.matchText("*") {
		args = append(args, &StarExpr{nodeBase: nodeBase{span: p.tokens[p.pos-1].Span}})
		p.expectText(")", "to close function call")
		return args, nil, nil, "", false, false, false
	}
	if p.isQueryStart() {
		query := p.parseSelect()
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
		return []Expr{&SubqueryExpr{nodeBase: nodeBase{span: query.SourceSpan()}, Query: query}}, nil, nil, "", false, false, false
	}
	for {
		if p.peek().Text == ")" {
			break
		}
		if p.peek().IsWord("ORDER") {
			p.advance()
			p.expectWord("BY", "after ORDER in function call")
			orderBy = p.parseOrderList()
			argumentTail = p.captureFunctionTail()
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
			argumentTail = p.captureFunctionTail()
			break
		}
		if p.matchWord("HAVING") {
			start := p.peek().Span.Start
			for p.peek().Kind != TokenEOF && p.peek().Text != ")" && p.peek().Text != "," {
				p.advance()
			}
			end := p.lastEnd
			having = &RawExpr{nodeBase: nodeBase{span: Span{Start: start, End: end}}, Raw: strings.TrimSpace(p.text[start:end])}
			break
		}
		if p.matchWord("IGNORE") {
			if p.matchWord("NULLS") {
				ignoreNulls = true
				nullsInside = true
			}
			if p.peek().IsWord("ORDER") {
				p.advance()
				p.expectWord("BY", "after ORDER in function call")
				orderBy = p.parseOrderList()
			}
			argumentTail = p.captureFunctionTail()
			break
		}
		if p.matchWord("RESPECT") {
			if p.matchWord("NULLS") {
				respectNulls = true
				nullsInside = true
			}
			if p.peek().IsWord("ORDER") {
				p.advance()
				p.expectWord("BY", "after ORDER in function call")
				orderBy = p.parseOrderList()
			}
			argumentTail = p.captureFunctionTail()
			break
		}
		if p.peek().Text != ")" && p.peek().Text != "," {
			start := p.peek().Span.Start
			depth := 0
			for p.peek().Kind != TokenEOF {
				if p.peek().Text == ")" && depth == 0 {
					break
				}
				if p.peek().Text == "(" {
					depth++
				} else if p.peek().Text == ")" && depth > 0 {
					depth--
				}
				p.advance()
			}
			if p.lastEnd > start {
				argumentTail = strings.TrimSpace(p.text[start:p.lastEnd])
			}
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
	return args, orderBy, having, argumentTail, ignoreNulls, respectNulls, nullsInside
}

func (p *parser) captureFunctionTail() string {
	if p.peek().Kind == TokenEOF || p.peek().Text == ")" {
		return ""
	}
	start := p.peek().Span.Start
	depth := 0
	for p.peek().Kind != TokenEOF {
		if p.peek().Text == ")" && depth == 0 {
			break
		}
		if p.peek().Text == "(" {
			depth++
		} else if p.peek().Text == ")" && depth > 0 {
			depth--
		}
		p.advance()
	}
	return strings.TrimSpace(p.text[start:p.lastEnd])
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
		if len(function.Args) != 2 && len(function.Args) != 3 {
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
			if p.options.Dialect == DialectTSQL && next < len(p.tokens) && p.tokens[next].Text == "." {
				p.advance()
				parts = append(parts, Identifier{Text: "", Span: p.tokens[p.pos-1].Span})
				continue
			}
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
	if p.options.Dialect == DialectMySQL && tok.Kind == TokenNumber {
		nextIndex := p.pos + 1
		if nextIndex < len(p.tokens) && tok.Span.End == p.tokens[nextIndex].Span.Start && (p.tokens[nextIndex].Kind == TokenIdentifier || p.tokens[nextIndex].Kind == TokenKeyword) {
			next := p.tokens[nextIndex]
			p.advance()
			p.advance()
			return Identifier{Text: tok.Text + next.Text, Span: Span{Start: tok.Span.Start, End: next.Span.End}}, true
		}
	}
	if tok.Kind != TokenIdentifier && tok.Kind != TokenQuotedIdentifier && !(tok.Kind == TokenKeyword && (allowKeyword || !p.isStructuralKeyword(tok) || tok.IsWord("WINDOW") || (p.options.Dialect == DialectBigQuery && (tok.IsWord("AT") || tok.IsWord("SAMPLE"))) || (p.options.Dialect == DialectRedshift && tok.IsWord("EXCLUDE")))) {
		return Identifier{}, false
	}
	p.advance()
	identifier := Identifier{Text: identifierText(tok), Quoted: tok.Kind == TokenQuotedIdentifier, Span: tok.Span}
	if identifier.Quoted && len(tok.Text) > 0 {
		identifier.Quote = tok.Text[0]
		if p.options.Dialect == DialectExasol && identifier.Quote == '[' {
			identifier.Quote = '"'
		}
	}
	return identifier, true
}

func identifierText(tok Token) string {
	if tok.Kind != TokenQuotedIdentifier || len(tok.Text) < 2 {
		return tok.Text
	}
	if tok.Text[0] == '[' && tok.Text[len(tok.Text)-1] == ']' {
		return strings.ReplaceAll(tok.Text[1:len(tok.Text)-1], "]]", "]")
	}
	text := tok.Text[1 : len(tok.Text)-1]
	if tok.Text[0] == '`' {
		text = strings.ReplaceAll(text, "\\`", "`")
	}
	return strings.ReplaceAll(text, tok.Text[:1]+tok.Text[:1], tok.Text[:1])
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

func (p *parser) parseUsingList() []Identifier {
	if p.options.Dialect != DialectSnowflake {
		return p.parseIdentifierList("USING")
	}
	var identifiers []Identifier
	for {
		parts, ok := p.parseNameParts()
		if !ok {
			p.reportExpectedIdentifier("in USING")
			break
		}
		identifiers = append(identifiers, parts[len(parts)-1])
		if !p.matchText(",") {
			break
		}
	}
	return identifiers
}

func (p *parser) canStartBareAlias() bool {
	tok := p.peek()
	if tok.IsWord("WINDOW") {
		return p.isWindowAliasContinuation()
	}
	if p.options.Dialect == DialectOracle && (tok.IsWord("BULK") || tok.IsWord("KEEP")) {
		return false
	}
	// ClickHouse uses FINAL as a relation modifier (`FROM table FINAL`), not
	// as a projection or relation alias. Keep it out of the global clause
	// boundary set so a CTE named `final` remains usable in FROM, but reject it
	// here where aliases are considered.
	if p.options.Dialect == DialectClickHouse && tok.IsWord("FINAL") {
		return false
	}
	if p.options.Dialect == DialectClickHouse && tok.IsWord("APPLY") && !p.peekTextAfter("(") {
		return true
	}
	if !p.isNameToken(tok) || p.isClauseBoundary() {
		return false
	}
	return !p.isStructuralKeyword(tok) || isAliasKeyword(tok) || (p.options.Dialect == DialectRedshift && tok.IsWord("EXCLUDE"))
}

func (p *parser) isStringProjectionAlias() bool {
	if p.options.Dialect != DialectMySQL && p.options.Dialect != DialectSQLite && p.options.Dialect != DialectTSQL {
		return false
	}
	return p.peek().Kind == TokenString || p.peek().Kind == TokenUnterminatedString
}

func isAliasKeyword(tok Token) bool {
	return tok.IsWord("IS")
}

func (p *parser) isWindowAliasContinuation() bool {
	index := p.pos + 1
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	if index >= len(p.tokens) {
		return true
	}
	tok := p.tokens[index]
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
	if tok.Text == "(" || tok.Text == "{" || tok.Text == "[" || tok.Text == "+" || tok.Text == "-" || tok.Text == "~" || tok.Text == "|/" || tok.Text == "||/" || tok.Text == "*" {
		return true
	}
	return p.isNameToken(tok) && !p.isStatementKeyword(tok)
}

func (p *parser) isStatementKeyword(tok Token) bool {
	if tok.Kind != TokenKeyword {
		return false
	}
	// PostgreSQL uses END as the spelling of a transaction terminator in a
	// number of fixtures.  In T-SQL and the other procedural dialects, a bare
	// END is also a valid incomplete/raw statement boundary, so do not make it
	// a universal statement starter.
	if strings.EqualFold(tok.Text, "END") {
		return p.options.Dialect == DialectPostgreSQL
	}
	switch strings.ToUpper(tok.Text) {
	case "CREATE", "ALTER", "DROP", "INSERT", "UPDATE", "DELETE", "MERGE", "GRANT", "REVOKE", "EXPLAIN", "SHOW", "DESCRIBE", "USE", "CACHE", "UNCACHE", "LOAD", "COMMENT", "PRAGMA", "KILL", "BEGIN", "START", "COMMIT", "ROLLBACK", "VACUUM", "ANALYZE", "EXPORT", "IMPORT", "CALL", "EXEC", "EXECUTE", "REFRESH", "DEALLOCATE", "RESET", "COPY", "UNLOAD", "DECLARE", "PRINT", "FOR", "LOOP", "REPEAT", "WHILE":
		return true
	case "OPEN":
		return p.options.Dialect == DialectExasol
	case "REPLACE":
		return p.options.Dialect == DialectMySQL
	case "ATTACH", "DETACH", "EXCHANGE", "OPTIMIZE", "SYSTEM":
		return p.options.Dialect == DialectClickHouse
	case "LOCK", "UNLOCK":
		return p.options.Dialect == DialectMySQL
	case "IF":
		return p.options.Dialect == DialectTSQL
	case "TRUNCATE":
		return !p.peekTextAfter("(")
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
	switch tok.word {
	case tokenWordSelect, tokenWordFrom, tokenWordWhere, tokenWordGroup,
		tokenWordHaving, tokenWordOrder, tokenWordLimit, tokenWordFetch,
		tokenWordTableSample, tokenWordPivot, tokenWordUnpivot, tokenWordReplace,
		tokenWordUnion, tokenWordIntersect, tokenWordExcept, tokenWordExclude,
		tokenWordJoin, tokenWordStraightJoin, tokenWordInner, tokenWordLeft,
		tokenWordRight, tokenWordFull, tokenWordCross, tokenWordNatural,
		tokenWordOuter, tokenWordSemi, tokenWordAnti, tokenWordOn,
		tokenWordUsing, tokenWordAnd, tokenWordOr, tokenWordNot, tokenWordIn,
		tokenWordBetween, tokenWordIs, tokenWordLike, tokenWordILike,
		tokenWordAs, tokenWordFor, tokenWordWhen, tokenWordThen, tokenWordElse,
		tokenWordEnd, tokenWordLateral, tokenWordConnect, tokenWordCluster,
		tokenWordSample, tokenWordSettings, tokenWordMatchRecognize,
		tokenWordIndexed, tokenWordWindow, tokenWordAt:
		return true
	default:
		return false
	}
}

func isSelectClauseBoundaryWord(word tokenWord) bool {
	switch word {
	case tokenWordFrom, tokenWordWhere, tokenWordGroup, tokenWordHaving,
		tokenWordOrder, tokenWordLimit, tokenWordOffset, tokenWordFetch,
		tokenWordQualify, tokenWordUnion, tokenWordIntersect, tokenWordExcept,
		tokenWordFor, tokenWordCluster, tokenWordSample, tokenWordSettings,
		tokenWordMatchRecognize, tokenWordIndexed, tokenWordWindow, tokenWordAt:
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
	if tok.word == tokenWordInto {
		return true
	}
	if p.options.Dialect != DialectGeneric && p.isDialectClauseBoundary(tok) {
		return true
	}
	if tok.word == tokenWordSample && p.options.Dialect == DialectBigQuery {
		return false
	}
	return isSelectClauseBoundaryWord(tok.word) || tok.word == tokenWordOption
}

func (p *parser) isSelectListBoundary() bool {
	tok := p.peek()
	if p.isTerminalProjectionAlias() {
		return false
	}
	if tok.Kind == TokenEOF || tok.Text == ";" || tok.Text == ")" {
		return true
	}
	if tok.word == tokenWordInto {
		return true
	}
	if p.options.Dialect != DialectGeneric && p.isDialectClauseBoundary(tok) {
		return true
	}
	return isSelectClauseBoundaryWord(tok.word)
}

func (p *parser) isDialectClauseBoundary(tok Token) bool {
	switch p.options.Dialect {
	case DialectHive:
		return tok.word == tokenWordDistribute || tok.word == tokenWordSort
	case DialectExasol, DialectRedshift, DialectTeradata, DialectSpark, DialectDatabricks, DialectSnowflake:
		return tok.word == tokenWordMinus
	case DialectOracle:
		return tok.word == tokenWordMinus || tok.word == tokenWordBulk || tok.word == tokenWordKeep
	case DialectClickHouse:
		switch tok.word {
		case tokenWordFormat:
			// FORMAT is a query tail only when it has a format name. A bare
			// `SELECT FORMAT` is a valid identifier expression.
			return p.clickHouseFormatClauseStart()
		case tokenWordAny:
			if p.peekTextAfter("(") {
				return false
			}
			return true
		case tokenWordPrewhere, tokenWordFill, tokenWordWith, tokenWordApply,
			tokenWordArray, tokenWordGlobal, tokenWordAsof, tokenWordSettings,
			tokenWordInterpolate:
			return true
		}
	case DialectMySQL:
		switch tok.word {
		case tokenWordUse, tokenWordForce, tokenWordIgnore, tokenWordPartition,
			tokenWordMember, tokenWordSounds, tokenWordMod, tokenWordReturning:
			return true
		}
	case DialectPostgreSQL:
		if tok.word == tokenWordReturning {
			return true
		}
	}
	return false
}

func (p *parser) clickHouseFormatClauseStart() bool {
	index := p.pos + 1
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	if index >= len(p.tokens) {
		return false
	}
	next := p.tokens[index]
	return next.Kind != TokenEOF && next.Text != ";" && next.Text != ")" && next.Text != ","
}

func (p *parser) isTerminalProjectionAlias() bool {
	word := p.peek().word
	if word != tokenWordLimit && word != tokenWordOffset {
		return false
	}
	return !p.hasClauseExpression()
}

func (p *parser) hasClauseExpression() bool {
	index := p.pos + 1
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	if index >= len(p.tokens) {
		return false
	}
	tok := p.tokens[index]
	if tok.Kind == TokenEOF || tok.Text == ";" || tok.Text == ")" || tok.Text == "," {
		return false
	}
	return true
}

func (p *parser) isExpressionBoundary() bool {
	if p.isClauseBoundary() {
		return true
	}
	switch p.peek().word {
	case tokenWordThen, tokenWordElse, tokenWordEnd, tokenWordJoin, tokenWordOn:
		return true
	default:
		return false
	}
}

func (p *parser) isBareAliasContinuation() bool {
	if !p.peek().IsWord("IS") {
		return false
	}
	index := p.pos + 1
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	if index >= len(p.tokens) {
		return true
	}
	tok := p.tokens[index]
	if tok.Kind == TokenEOF || tok.Text == ";" || tok.Text == "," || tok.Text == ")" {
		return true
	}
	for _, word := range []string{"FROM", "WHERE", "GROUP", "HAVING", "ORDER", "LIMIT", "OFFSET", "FETCH", "QUALIFY", "UNION", "INTERSECT", "EXCEPT"} {
		if tok.IsWord(word) {
			return true
		}
	}
	return false
}

func (p *parser) wordAtExpressionEnd(word string) bool {
	if !p.peek().IsWord(word) {
		return false
	}
	index := p.pos + 1
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	if index >= len(p.tokens) {
		return true
	}
	tok := p.tokens[index]
	if tok.Kind == TokenEOF || tok.Text == ";" || tok.Text == ")" || tok.Text == "," {
		return true
	}
	for _, clause := range []string{"FROM", "WHERE", "GROUP", "HAVING", "ORDER", "LIMIT", "OFFSET", "FETCH", "QUALIFY", "UNION", "INTERSECT", "EXCEPT", "WINDOW"} {
		if tok.IsWord(clause) {
			return true
		}
	}
	return false
}

func (p *parser) isJoinStart() bool {
	for _, word := range []string{"JOIN", "STRAIGHT_JOIN", "INNER", "LEFT", "RIGHT", "FULL", "CROSS", "NATURAL", "OUTER", "SEMI", "ANTI", "POSITIONAL", "ASOF"} {
		if p.peek().IsWord(word) {
			return true
		}
	}
	if p.options.Dialect == DialectClickHouse {
		for _, word := range []string{"GLOBAL", "ANY", "ARRAY"} {
			if p.peek().IsWord(word) {
				return true
			}
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

func (p *parser) peekWordAfterAny(wanted string) bool {
	index := p.pos + 1
	for index < len(p.tokens) && p.tokens[index].Kind == TokenComment {
		index++
	}
	return index < len(p.tokens) && p.tokens[index].IsWord(wanted)
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
	if tok.IsWord("XOR") {
		return 3, "XOR", true
	}
	if tok.IsWord("LIKE") || tok.IsWord("ILIKE") || tok.IsWord("GLOB") || tok.IsWord("RLIKE") || tok.IsWord("REGEXP") {
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
	if tok.IsWord("SIMILAR") {
		return 3, "SIMILAR", true
	}
	if tok.IsWord("DIV") {
		return 6, "DIV", true
	}
	if tok.IsWord("MOD") {
		return 6, "MOD", true
	}
	switch tok.Text {
	case "=", "==", "<>", "!=", "<", "<=", ">", ">=", "~", "~*", "!~", "!~*", "~~", "~~*", "~~~", "!~~", "!~~*", "@>", "@?", "@@", "?", "?&", "?|", "<@", "&&", "^@", "<=>", "<->", "<<->>", "^=":
		operator := strings.ToUpper(tok.Text)
		if operator == "==" || operator == "^=" {
			operator = "="
		}
		if tok.Text == "^=" {
			operator = "<>"
		}
		return 3, operator, true
	case "||", "->", "->>", "#>", "#>>", "#-":
		return 4, tok.Text, true
	case "??":
		return 3, tok.Text, true
	case ":>", "!:>":
		return 7, tok.Text, true
	case "-|-":
		return 3, tok.Text, true
	case "+", "-", "&", "|", "^", "#", "<<", ">>":
		return 5, tok.Text, true
	case "*", "/", "%", "**", "//":
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
