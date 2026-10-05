package golyglot

import (
	"fmt"
	"strings"
)

func nativeTop(target Dialect) bool {
	return target == DialectTSQL || target == DialectTeradata || target == DialectSnowflake
}

func validateRowLimit(stmt *SelectStmt, target Dialect) error {
	percent := stmt.TopPercent || stmt.LimitPercent || stmt.Fetch != nil && stmt.Fetch.Percent
	ties := stmt.TopWithTies || stmt.Fetch != nil && stmt.Fetch.WithTies
	if percent {
		switch target {
		case DialectGeneric, DialectOracle, DialectDuckDB:
		case DialectTSQL:
			if stmt.Offset != nil {
				return fmt.Errorf("golyglot: cannot translate PERCENT with OFFSET to TSQL")
			}
		default:
			return fmt.Errorf("golyglot: %s does not support PERCENT row limits", target)
		}
	}
	if ties {
		switch target {
		case DialectGeneric, DialectOracle, DialectPostgreSQL, DialectPresto, DialectTrino, DialectClickHouse:
		case DialectTSQL:
			if stmt.Offset != nil {
				return fmt.Errorf("golyglot: cannot translate WITH TIES with OFFSET to TSQL")
			}
		default:
			return fmt.Errorf("golyglot: %s does not support WITH TIES row limits", target)
		}
	}
	return nil
}

// Validate before source normalizers can lower a query to opaque SQL. The
// generator performs the same check for AST and builder callers.
func validateRowLimits(root Node, target Dialect) error {
	var err error
	Walk(root, func(node Node) VisitAction {
		if query, ok := node.(*SelectStmt); ok {
			err = validateRowLimit(query, target)
			if err != nil {
				return Stop
			}
		}
		return VisitChildren
	})
	return err
}

// Row-limit spelling belongs to generation, not just transpilation: builders,
// formatting and direct AST generation must obey exactly the same rules.
// Work on copies so rendering never changes a caller-owned AST.
func prepareSelectRowLimits(query *SelectStmt, target Dialect) (*SelectStmt, error) {
	if err := validateRowLimit(query, target); err != nil {
		return nil, err
	}
	stmt := *query
	if target == DialectTSQL && stmt.SetOperator != "" {
		base, tail := detachCompoundTail(&stmt)
		if (tail.Limit != nil || tail.Fetch != nil) && tail.Offset == nil {
			// A trailing LIMIT/FETCH belongs to the whole set; putting TOP on
			// its last arm would change the selected rows.
			with, withTail := base.With, base.WithTail
			base.With, base.WithTail = nil, ""
			stmt = SelectStmt{
				nodeBase: query.nodeBase, With: with, WithTail: withTail,
				Projections: []SelectItem{{Expr: &StarExpr{}}},
				From:        []TableExpr{{Primary: &SubqueryFrom{Query: base, Alias: &Identifier{Text: "_l_0"}}}},
				OrderBy:     tail.OrderBy, Limit: tail.Limit, LimitPercent: tail.LimitPercent,
				Fetch: tail.Fetch, Tail: tail.Tail,
			}
		}
	}
	if stmt.SetOperator != "" && !nativeTop(target) && compoundHasTop(&stmt) {
		stmt = *scopeCompoundTop(&stmt)
	}
	if stmt.Top != nil && !nativeTop(target) {
		if stmt.TopWithTies || stmt.TopPercent && target != DialectDuckDB {
			stmt.Fetch = &FetchClause{Count: stmt.Top, Percent: stmt.TopPercent, WithTies: stmt.TopWithTies}
		} else {
			stmt.Limit, stmt.LimitPercent = stmt.Top, stmt.TopPercent
		}
		stmt.Top, stmt.TopPercent, stmt.TopWithTies = nil, false, false
	}
	if target == DialectOracle && stmt.Limit != nil {
		if stmt.LimitPercent || !unboundedRowLimit(stmt.Limit) {
			stmt.Fetch = &FetchClause{Count: stmt.Limit, Percent: stmt.LimitPercent}
		}
		stmt.Limit, stmt.LimitPercent = nil, false
	}
	if stmt.Fetch != nil {
		switch target {
		case DialectGeneric, DialectOracle, DialectTSQL, DialectPostgreSQL, DialectPresto, DialectTrino:
		case DialectClickHouse:
			if !stmt.Fetch.WithTies {
				stmt.Limit = stmt.Fetch.Count
				if stmt.Limit == nil {
					stmt.Limit = numberExpr(1)
				}
				stmt.Fetch = nil
			}
		default:
			stmt.Limit, stmt.LimitPercent = stmt.Fetch.Count, stmt.Fetch.Percent
			if stmt.Limit == nil {
				stmt.Limit = numberExpr(1)
			}
			stmt.Fetch = nil
		}
	}
	if target == DialectTSQL && stmt.Top == nil && stmt.Offset == nil {
		if stmt.Limit != nil {
			stmt.Top, stmt.TopPercent = stmt.Limit, stmt.LimitPercent
			stmt.Limit, stmt.LimitPercent = nil, false
		} else if stmt.Fetch != nil {
			stmt.Top, stmt.TopPercent, stmt.TopWithTies = stmt.Fetch.Count, stmt.Fetch.Percent, stmt.Fetch.WithTies
			if stmt.Top == nil {
				stmt.Top = numberExpr(1)
			}
			stmt.Fetch = nil
		}
	}
	return &stmt, nil
}

func unboundedRowLimit(count Expr) bool {
	if isNullLiteral(count) {
		return true
	}
	id, ok := count.(*IdentifierExpr)
	return ok && len(id.Parts) == 1 && !id.Parts[0].Quoted && strings.EqualFold(id.Parts[0].Text, "ALL")
}

func compoundHasTop(stmt *SelectStmt) bool {
	if stmt == nil {
		return false
	}
	if stmt.Top != nil {
		return true
	}
	return !stmt.SetRightParen && stmt.SetRight != nil && !stmt.SetRight.Parenthesized && compoundHasTop(stmt.SetRight)
}

// The legacy AST stores SELECT's first arm on the compound node and the
// compound's final ORDER/LIMIT on its last unparenthesized arm. TOP is local
// to an arm. Separate those scopes before moving TOP to a trailing clause.
func scopeCompoundTop(stmt *SelectStmt) *SelectStmt {
	base, outer := detachCompoundTail(stmt)
	root := splitTopSetArms(base)
	root.OrderBy, root.Limit, root.LimitPercent, root.Offset, root.Fetch, root.Tail = outer.OrderBy, outer.Limit, outer.LimitPercent, outer.Offset, outer.Fetch, outer.Tail
	return root
}

func detachCompoundTail(stmt *SelectStmt) (*SelectStmt, SelectStmt) {
	base := *stmt
	tail := &base
	for !hasQueryTail(tail) && tail.SetRight != nil && !tail.SetRightParen && !tail.SetRight.Parenthesized {
		right := *tail.SetRight
		tail.SetRight = &right
		tail = &right
	}
	outer := SelectStmt{OrderBy: tail.OrderBy, Limit: tail.Limit, LimitPercent: tail.LimitPercent, Offset: tail.Offset, Fetch: tail.Fetch, Tail: tail.Tail}
	tail.OrderBy, tail.Limit, tail.LimitPercent, tail.Offset, tail.Fetch, tail.Tail = nil, nil, false, nil, nil, ""
	return &base, outer
}

func splitTopSetArms(stmt *SelectStmt) *SelectStmt {
	if stmt.SetOperator == "" || stmt.SetLeft != nil {
		return stmt
	}
	left := *stmt
	left.With, left.WithTail = nil, ""
	left.SetOperator, left.SetAll, left.SetModifier, left.SetRight = "", false, "", nil
	left.Parenthesized, left.ParenthesisDepth, left.SetLeftParen, left.SetRightParen = false, 0, false, false
	root := &SelectStmt{
		nodeBase: stmt.nodeBase, With: stmt.With, WithTail: stmt.WithTail,
		SetOperator: stmt.SetOperator, SetAll: stmt.SetAll, SetModifier: stmt.SetModifier,
		SetLeft: &left, SetLeftParen: stmt.SetLeftParen || left.Top != nil,
		SetRight: stmt.SetRight, SetRightParen: stmt.SetRightParen,
		Parenthesized: stmt.Parenthesized, ParenthesisDepth: stmt.ParenthesisDepth,
		TailOutsideParen: stmt.TailOutsideParen,
	}
	if root.SetRight != nil && !root.SetRightParen && !root.SetRight.Parenthesized {
		root.SetRight = splitTopSetArms(root.SetRight)
		root.SetRightParen = root.SetRight.SetOperator == "" && root.SetRight.Top != nil
	}
	return root
}

func (g generator) writeRowLimit(b *strings.Builder, stmt *SelectStmt) error {
	separator := " "
	if g.pretty {
		separator = "\n" + indentString(g.indent)
	}
	if stmt.Limit != nil && g.dialect != DialectTSQL {
		limit, err := g.expr(stmt.Limit, 0)
		if err != nil {
			return err
		}
		b.WriteString(separator + "LIMIT " + limit)
		if stmt.LimitPercent {
			b.WriteString(" PERCENT")
		}
	}
	if stmt.Offset != nil {
		offset, err := g.expr(stmt.Offset, 0)
		if err != nil {
			return err
		}
		b.WriteString(separator + "OFFSET " + offset)
		if g.dialect == DialectTSQL || g.dialect == DialectOracle || g.dialect == DialectClickHouse && stmt.Fetch != nil {
			b.WriteString(" ROWS")
		}
	}
	fetch := stmt.Fetch
	if fetch == nil && g.dialect == DialectTSQL && stmt.Limit != nil {
		fetch = &FetchClause{Count: stmt.Limit, Next: true, Percent: stmt.LimitPercent}
	}
	if fetch != nil {
		b.WriteString(separator + "FETCH ")
		if fetch.Next {
			b.WriteString("NEXT")
		} else {
			b.WriteString("FIRST")
		}
		if fetch.Count != nil {
			count, err := g.expr(fetch.Count, 0)
			if err != nil {
				return err
			}
			b.WriteString(" " + count)
		}
		if fetch.Percent {
			b.WriteString(" PERCENT")
		}
		b.WriteString(" ROWS")
		if fetch.WithTies {
			b.WriteString(" WITH TIES")
		} else {
			b.WriteString(" ONLY")
		}
	}
	return nil
}

func writeTopModifiers(b *strings.Builder, stmt *SelectStmt) {
	if stmt.TopPercent {
		b.WriteString(" PERCENT")
	}
	if stmt.TopWithTies {
		b.WriteString(" WITH TIES")
	}
}

func (g generator) setOperand(stmt *SelectStmt, parentheses bool) (string, error) {
	if stmt == nil {
		return "", fmt.Errorf("cannot generate set operation without a query")
	}
	parentheses = parentheses || stmt.Parenthesized || !nativeTop(g.dialect) && stmt.Top != nil && stmt.SetOperator == ""
	base := *stmt
	if parentheses {
		base.Parenthesized, base.ParenthesisDepth = false, 0
	}
	inner := g
	if parentheses && g.pretty {
		inner.indent++
	}
	text, err := inner.selectStmt(&base)
	if err != nil || !parentheses {
		return text, err
	}
	if g.pretty {
		text = indentString(g.indent) + "(\n" + text + "\n" + indentString(g.indent) + ")"
	} else {
		text = "(" + text + ")"
	}
	if g.dialect == DialectSQLite {
		text = "SELECT * FROM " + text
	}
	return text, nil
}

func (g generator) withPrefix(stmt *SelectStmt) (string, error) {
	if len(stmt.With) == 0 {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("WITH ")
	if stmt.With[0].Recursive {
		b.WriteString("RECURSIVE ")
	}
	for i, cte := range stmt.With {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(generateIdentifier(cte.Name))
		if len(cte.Columns) > 0 {
			b.WriteByte('(')
			for j, column := range cte.Columns {
				if j > 0 {
					b.WriteString(", ")
				}
				b.WriteString(generateIdentifier(column))
			}
			b.WriteByte(')')
		}
		if cte.Modifier != "" {
			b.WriteString(" " + cte.Modifier)
		}
		b.WriteString(" AS")
		if cte.Materialized != "" {
			b.WriteString(" " + cte.Materialized)
		}
		if cte.Query == nil {
			return "", fmt.Errorf("cannot generate CTE %s without a query", cte.Name.Text)
		}
		text, err := g.selectStmt(cte.Query)
		if err != nil {
			return "", err
		}
		b.WriteString(" (" + text + ")")
	}
	if stmt.WithTail != "" {
		b.WriteString(" " + stmt.WithTail)
	}
	if g.pretty {
		b.WriteByte('\n')
	} else {
		b.WriteByte(' ')
	}
	return b.String(), nil
}
