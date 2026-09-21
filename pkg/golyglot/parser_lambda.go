package golyglot

import "strings"

func canonicalLambdaType(typeSQL string) string {
	tokens, _, _ := Tokenize(typeSQL, DialectSnowflake)
	var result strings.Builder
	start := 0
	for _, token := range tokens {
		if token.Kind != TokenKeyword && token.Kind != TokenIdentifier {
			continue
		}
		result.WriteString(typeSQL[start:token.Span.Start])
		result.WriteString(strings.ToUpper(token.Text))
		start = token.Span.End
	}
	result.WriteString(typeSQL[start:])
	return canonicalRawSQL(result.String())
}

// Recognize the typed Snowflake form before the generic function-tail fallback
// can swallow its body. Lookahead never reparses prefixes or crosses a delimiter.
func (p *parser) parseFunctionArgument() Expr {
	start := p.pos
	if p.options.Dialect == DialectSnowflake && start+2 < len(p.tokens) && (p.tokens[start].Kind == TokenIdentifier || p.tokens[start].Kind == TokenQuotedIdentifier) && (p.tokens[start+1].Kind == TokenIdentifier || p.tokens[start+1].Kind == TokenKeyword) {
		depth := 0
		for index := start + 1; index < len(p.tokens); index++ {
			token := p.tokens[index]
			if token.Kind == TokenEOF || depth == 0 && (token.Text == "," || token.Text == ")") {
				break
			}
			if token.Text == "(" || token.Text == "<" || token.Text == "[" {
				depth++
			}
			if token.Text == ")" || token.Text == ">" || token.Text == "]" {
				depth--
			}
			if token.Text == "->" && depth == 0 {
				typeSQL := strings.TrimSpace(p.text[p.tokens[start+1].Span.Start:token.Span.Start])
				if _, err := ParseDataType(typeSQL, p.options.Dialect); err != nil || typeSQL == "" {
					break
				}
				name, _ := p.parseIdentifier(false)
				for p.pos <= index {
					p.advance()
				}
				body := p.parseExpression(0)
				p.recordNode()
				return &LambdaExpr{nodeBase: nodeBase{span: Span{Start: p.tokens[start].Span.Start, End: body.SourceSpan().End}}, Parameters: []LambdaParameter{{Name: name, Type: typeSQL}}, Body: body}
			}
		}
	}
	return p.parseExpressionAlias(p.parseExpressionWithSet())
}
