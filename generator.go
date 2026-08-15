package golyglot

import (
	"fmt"
	"strings"
)

// Generate emits compact canonical SQL for a successfully parsed AST.
func Generate(node Node) (string, error) {
	if node == nil {
		return "", fmt.Errorf("cannot generate SQL from nil AST")
	}
	return GenerateWithOptions(node, GenerateOptions{})
}

type GenerateOptions struct {
	Pretty    bool
	Canonical bool
	Dialect   Dialect
}

// GenerateWithOptions emits canonical SQL. Pretty mode is intentionally
// conservative for now and formats statement-level clauses and projections;
// dialect-specific formatting will be layered on top of this API.
func GenerateWithOptions(node Node, options GenerateOptions) (string, error) {
	if node == nil {
		return "", fmt.Errorf("cannot generate SQL from nil AST")
	}
	dialect, err := options.Dialect.normalized()
	if err != nil {
		return "", err
	}
	g := generator{pretty: options.Pretty, canonical: options.Canonical, dialect: dialect}
	return g.node(node)
}

type generator struct {
	pretty    bool
	canonical bool
	dialect   Dialect
}

func (g generator) node(node Node) (string, error) {
	if !g.canonical {
		if rawNode, ok := node.(interface{ rawSQL() string }); ok {
			if raw := rawNode.rawSQL(); raw != "" {
				return raw, nil
			}
		}
	}
	switch n := node.(type) {
	case Expr:
		return g.expr(n, 0)
	case FromItem:
		return g.fromItem(n)
	case *TableExpr:
		return g.tableExpr(n)
	case *SelectStmt:
		return g.selectStmt(n)
	case *ExpressionStmt:
		text, err := g.expr(n.Expr, 0)
		if err != nil {
			return "", err
		}
		if n.Alias != nil {
			text += " AS " + generateIdentifier(*n.Alias)
		}
		if len(n.AliasColumns) > 0 {
			text += " AS ("
			for i, column := range n.AliasColumns {
				if i > 0 {
					text += ", "
				}
				text += generateIdentifier(column)
			}
			text += ")"
		}
		return text, nil
	case *CreateTableStmt:
		text := "CREATE "
		if n.Materialized {
			text += "MATERIALIZED "
		}
		if n.Temporary {
			text += "TEMPORARY "
		}
		text += "TABLE "
		if n.IfNotExists {
			text += "IF NOT EXISTS "
		}
		text += generateIdentifiers(n.Name)
		if n.Tail != "" {
			text += " " + n.Tail
		}
		return text, nil
	case *InsertStmt:
		return g.insertStmt(n)
	case *UpdateStmt:
		return g.updateStmt(n)
	case *DeleteStmt:
		return g.deleteStmt(n)
	case *CommandStmt:
		if n.Raw == "" {
			return n.Keyword, nil
		}
		return n.Raw, nil
	case *RawStmt:
		return n.Raw, nil
	case *UnknownStmt:
		return "", fmt.Errorf("cannot generate unsupported statement: %s", n.Reason)
	default:
		return "", fmt.Errorf("cannot generate node kind %s", node.Kind())
	}
}

func (g generator) insertStmt(stmt *InsertStmt) (string, error) {
	text := "INSERT INTO " + generateIdentifiers(stmt.Table)
	if len(stmt.Columns) > 0 {
		text += " ("
		for i, column := range stmt.Columns {
			if i > 0 {
				text += ", "
			}
			text += generateIdentifier(column)
		}
		text += ")"
	}
	if len(stmt.Values) > 0 {
		text += " VALUES "
		for i, row := range stmt.Values {
			if i > 0 {
				text += ", "
			}
			text += "("
			for j, value := range row {
				if j > 0 {
					text += ", "
				}
				valueText, err := g.expr(value, 0)
				if err != nil {
					return "", err
				}
				text += valueText
			}
			text += ")"
		}
	} else if stmt.Query != nil {
		query, err := g.selectStmt(stmt.Query)
		if err != nil {
			return "", err
		}
		text += " " + query
	}
	if stmt.Tail != "" {
		text += " " + strings.TrimSpace(stmt.Tail)
	}
	return text, nil
}

func (g generator) updateStmt(stmt *UpdateStmt) (string, error) {
	text := "UPDATE " + generateIdentifiers(stmt.Table) + " SET "
	for i, assignment := range stmt.Assignments {
		if i > 0 {
			text += ", "
		}
		text += generateIdentifiers(assignment.Target) + " = "
		value, err := g.expr(assignment.Value, 0)
		if err != nil {
			return "", err
		}
		text += value
	}
	if stmt.Where != nil {
		where, err := g.expr(stmt.Where, 0)
		if err != nil {
			return "", err
		}
		text += " WHERE " + where
	}
	if stmt.Tail != "" {
		text += " " + strings.TrimSpace(stmt.Tail)
	}
	return text, nil
}

func (g generator) deleteStmt(stmt *DeleteStmt) (string, error) {
	text := "DELETE FROM " + generateIdentifiers(stmt.Table)
	if stmt.Where != nil {
		where, err := g.expr(stmt.Where, 0)
		if err != nil {
			return "", err
		}
		text += " WHERE " + where
	}
	if stmt.Tail != "" {
		text += " " + strings.TrimSpace(stmt.Tail)
	}
	return text, nil
}

func (g generator) selectStmt(stmt *SelectStmt) (string, error) {
	if stmt.Top != nil && g.dialect != DialectTSQL && stmt.Limit == nil {
		base := *stmt
		base.Limit = stmt.Top
		base.Top = nil
		return g.selectStmt(&base)
	}
	if stmt.TailOutsideParen && stmt.Parenthesized {
		base := *stmt
		base.Parenthesized = false
		base.ParenthesisDepth = 0
		base.TailOutsideParen = false
		base.OrderBy = nil
		base.Limit = nil
		base.Offset = nil
		base.Fetch = nil
		text, err := g.selectStmt(&base)
		if err != nil {
			return "", err
		}
		text = parenthesizeQuery(text, stmt)
		return g.appendQueryTail(text, stmt)
	}
	if stmt.SetOperator != "" && hasQueryTail(stmt) {
		base := *stmt
		base.OrderBy = nil
		base.Limit = nil
		base.Offset = nil
		base.Fetch = nil
		text, err := g.selectStmt(&base)
		if err != nil {
			return "", err
		}
		return g.appendQueryTail(text, stmt)
	}
	if stmt.SetLeft != nil {
		left, err := g.selectStmt(stmt.SetLeft)
		if err != nil {
			return "", err
		}
		if stmt.SetLeftParen && !stmt.SetLeft.Parenthesized {
			left = "(" + left + ")"
		}
		if stmt.SetRight == nil {
			return "", fmt.Errorf("cannot generate set operation without right query")
		}
		right, err := g.selectStmt(stmt.SetRight)
		if err != nil {
			return "", err
		}
		if stmt.SetRightParen && !stmt.SetRight.Parenthesized {
			right = "(" + right + ")"
		}
		text := left + " " + stmt.SetOperator
		if stmt.SetAll {
			text += " ALL"
		}
		text += " " + right
		text, err = g.appendQueryTail(text, stmt)
		if err != nil {
			return "", err
		}
		text = parenthesizeQuery(text, stmt)
		return text, nil
	}
	var b strings.Builder
	if len(stmt.With) > 0 {
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
			b.WriteString(" AS (")
			if cte.Query == nil {
				return "", fmt.Errorf("cannot generate CTE %s without a query", cte.Name.Text)
			}
			query, err := g.selectStmt(cte.Query)
			if err != nil {
				return "", err
			}
			b.WriteString(query)
			b.WriteByte(')')
		}
		b.WriteByte(' ')
	}
	b.WriteString("SELECT")
	if stmt.Distinct {
		b.WriteString(" DISTINCT")
		if len(stmt.DistinctOn) > 0 {
			b.WriteString(" ON (")
			if err := g.exprList(&b, stmt.DistinctOn); err != nil {
				return "", err
			}
			b.WriteByte(')')
		}
	}
	if stmt.Top != nil && g.dialect == DialectTSQL {
		top, err := g.expr(stmt.Top, 0)
		if err != nil {
			return "", err
		}
		b.WriteString(" TOP (")
		b.WriteString(top)
		b.WriteByte(')')
	}
	if len(stmt.Projections) == 0 {
		return "", fmt.Errorf("cannot generate SELECT without projections")
	}
	if g.pretty {
		for i, item := range stmt.Projections {
			if i == 0 {
				b.WriteString("\n  ")
			} else {
				b.WriteString(",\n  ")
			}
			if err := g.writeSelectItem(&b, item); err != nil {
				return "", err
			}
		}
	} else {
		b.WriteByte(' ')
		for i, item := range stmt.Projections {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := g.writeSelectItem(&b, item); err != nil {
				return "", err
			}
		}
	}
	if len(stmt.From) > 0 {
		if g.pretty {
			b.WriteString("\nFROM ")
		} else {
			b.WriteString(" FROM ")
		}
		for i, table := range stmt.From {
			if i > 0 {
				b.WriteString(", ")
			}
			text, err := g.tableExpr(&table)
			if err != nil {
				return "", err
			}
			b.WriteString(text)
		}
	}
	if stmt.Where != nil {
		where, err := g.expr(stmt.Where, 0)
		if err != nil {
			return "", err
		}
		if g.pretty {
			b.WriteString("\nWHERE ")
		} else {
			b.WriteString(" WHERE ")
		}
		b.WriteString(where)
	}
	if len(stmt.GroupBy) > 0 {
		if g.pretty {
			b.WriteString("\nGROUP BY ")
		} else {
			b.WriteString(" GROUP BY ")
		}
		if err := g.exprList(&b, stmt.GroupBy); err != nil {
			return "", err
		}
	}
	if stmt.Having != nil {
		having, err := g.expr(stmt.Having, 0)
		if err != nil {
			return "", err
		}
		if g.pretty {
			b.WriteString("\nHAVING ")
		} else {
			b.WriteString(" HAVING ")
		}
		b.WriteString(having)
	}
	if stmt.Qualify != nil {
		qualify, err := g.expr(stmt.Qualify, 0)
		if err != nil {
			return "", err
		}
		if g.pretty {
			b.WriteString("\nQUALIFY ")
		} else {
			b.WriteString(" QUALIFY ")
		}
		b.WriteString(qualify)
	}
	if stmt.ConnectBy != nil {
		connectBy, err := g.expr(stmt.ConnectBy, 0)
		if err != nil {
			return "", err
		}
		if g.pretty {
			b.WriteString("\nCONNECT BY ")
		} else {
			b.WriteString(" CONNECT BY ")
		}
		b.WriteString(connectBy)
	}
	if len(stmt.Windows) > 0 {
		if g.pretty {
			b.WriteString("\nWINDOW ")
		} else {
			b.WriteString(" WINDOW ")
		}
		for i, window := range stmt.Windows {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(generateIdentifier(window.Name))
			b.WriteString(" AS ")
			windowText, err := g.windowSpec(window.Spec)
			if err != nil {
				return "", err
			}
			b.WriteString(windowText)
		}
	}
	if len(stmt.SortBy) > 0 {
		if g.pretty {
			b.WriteString("\nSORT BY ")
		} else {
			b.WriteString(" SORT BY ")
		}
		for i, item := range stmt.SortBy {
			if i > 0 {
				b.WriteString(", ")
			}
			text, err := g.expr(item.Expr, 0)
			if err != nil {
				return "", err
			}
			b.WriteString(text)
			if item.Descending {
				b.WriteString(" DESC")
			} else if item.Ascending {
				b.WriteString(" ASC")
			}
			if item.NullsLast {
				b.WriteString(" NULLS LAST")
			}
		}
	}
	if len(stmt.OrderBy) > 0 {
		if g.pretty {
			b.WriteString("\nORDER BY ")
		} else {
			b.WriteString(" ORDER BY ")
		}
		for i, item := range stmt.OrderBy {
			if i > 0 {
				b.WriteString(", ")
			}
			text, err := g.expr(item.Expr, 0)
			if err != nil {
				return "", err
			}
			b.WriteString(text)
			if item.Descending {
				b.WriteString(" DESC")
			} else if item.Ascending {
				b.WriteString(" ASC")
			}
			if item.NullsLast {
				b.WriteString(" NULLS LAST")
			}
		}
	}
	if g.dialect == DialectTSQL && (stmt.Limit != nil || stmt.Offset != nil) {
		if stmt.Offset != nil {
			offset, err := g.expr(stmt.Offset, 0)
			if err != nil {
				return "", err
			}
			if g.pretty {
				b.WriteString("\nOFFSET ")
			} else {
				b.WriteString(" OFFSET ")
			}
			b.WriteString(offset)
			b.WriteString(" ROWS")
		}
		if stmt.Limit != nil {
			limit, err := g.expr(stmt.Limit, 0)
			if err != nil {
				return "", err
			}
			if g.pretty {
				b.WriteString("\nFETCH NEXT ")
			} else {
				b.WriteString(" FETCH NEXT ")
			}
			b.WriteString(limit)
			b.WriteString(" ROWS ONLY")
		}
	} else if stmt.Limit != nil {
		limit, err := g.expr(stmt.Limit, 0)
		if err != nil {
			return "", err
		}
		if g.pretty {
			b.WriteString("\nLIMIT ")
		} else {
			b.WriteString(" LIMIT ")
		}
		b.WriteString(limit)
	}
	if g.dialect != DialectTSQL && stmt.Offset != nil {
		offset, err := g.expr(stmt.Offset, 0)
		if err != nil {
			return "", err
		}
		if g.pretty {
			b.WriteString("\nOFFSET ")
		} else {
			b.WriteString(" OFFSET ")
		}
		b.WriteString(offset)
	}
	if stmt.Fetch != nil {
		if g.pretty {
			b.WriteString("\nFETCH ")
		} else {
			b.WriteString(" FETCH ")
		}
		if stmt.Fetch.Next {
			b.WriteString("NEXT")
		} else {
			b.WriteString("FIRST")
		}
		if stmt.Fetch.Count != nil {
			b.WriteByte(' ')
			count, err := g.expr(stmt.Fetch.Count, 0)
			if err != nil {
				return "", err
			}
			b.WriteString(count)
		}
		if stmt.Fetch.Percent {
			b.WriteString(" PERCENT")
		}
		b.WriteString(" ROWS")
		if stmt.Fetch.WithTies {
			b.WriteString(" WITH TIES")
		} else {
			b.WriteString(" ONLY")
		}
	}
	if stmt.SetOperator != "" {
		leftText := b.String()
		if stmt.SetLeftParen {
			leftText = "(" + leftText + ")"
		}
		b.Reset()
		b.WriteString(leftText)
		b.WriteByte(' ')
		b.WriteString(stmt.SetOperator)
		if stmt.SetAll {
			b.WriteString(" ALL")
		}
		if stmt.SetRight == nil {
			return "", fmt.Errorf("cannot generate set operation without right query")
		}
		right, err := g.selectStmt(stmt.SetRight)
		if err != nil {
			return "", err
		}
		if stmt.SetRightParen && !stmt.SetRight.Parenthesized {
			right = "(" + right + ")"
		}
		b.WriteByte(' ')
		b.WriteString(right)
	}
	result := b.String()
	result = parenthesizeQuery(result, stmt)
	return result, nil
}

func hasQueryTail(stmt *SelectStmt) bool {
	return len(stmt.OrderBy) > 0 || stmt.Limit != nil || stmt.Offset != nil || stmt.Fetch != nil
}

func parenthesizeQuery(text string, stmt *SelectStmt) string {
	depth := stmt.ParenthesisDepth
	if stmt.Parenthesized && depth == 0 {
		depth = 1
	}
	for i := 0; i < depth; i++ {
		text = "(" + text + ")"
	}
	return text
}

func (g generator) appendQueryTail(text string, stmt *SelectStmt) (string, error) {
	var b strings.Builder
	b.WriteString(text)
	if len(stmt.OrderBy) > 0 {
		b.WriteString(" ORDER BY ")
		for i, item := range stmt.OrderBy {
			if i > 0 {
				b.WriteString(", ")
			}
			itemText, err := g.expr(item.Expr, 0)
			if err != nil {
				return "", err
			}
			b.WriteString(itemText)
			if item.Descending {
				b.WriteString(" DESC")
			} else if item.Ascending {
				b.WriteString(" ASC")
			}
			if item.NullsLast {
				b.WriteString(" NULLS LAST")
			}
		}
	}
	if g.dialect == DialectTSQL && (stmt.Limit != nil || stmt.Offset != nil) {
		if stmt.Offset != nil {
			offset, err := g.expr(stmt.Offset, 0)
			if err != nil {
				return "", err
			}
			b.WriteString(" OFFSET ")
			b.WriteString(offset)
			b.WriteString(" ROWS")
		}
		if stmt.Limit != nil {
			limit, err := g.expr(stmt.Limit, 0)
			if err != nil {
				return "", err
			}
			b.WriteString(" FETCH NEXT ")
			b.WriteString(limit)
			b.WriteString(" ROWS ONLY")
		}
	} else if stmt.Limit != nil {
		limit, err := g.expr(stmt.Limit, 0)
		if err != nil {
			return "", err
		}
		b.WriteString(" LIMIT ")
		b.WriteString(limit)
	}
	if g.dialect != DialectTSQL && stmt.Offset != nil {
		offset, err := g.expr(stmt.Offset, 0)
		if err != nil {
			return "", err
		}
		b.WriteString(" OFFSET ")
		b.WriteString(offset)
	}
	if stmt.Fetch != nil {
		b.WriteString(" FETCH ")
		if stmt.Fetch.Next {
			b.WriteString("NEXT")
		} else {
			b.WriteString("FIRST")
		}
		if stmt.Fetch.Count != nil {
			count, err := g.expr(stmt.Fetch.Count, 0)
			if err != nil {
				return "", err
			}
			b.WriteByte(' ')
			b.WriteString(count)
		}
		if stmt.Fetch.Percent {
			b.WriteString(" PERCENT")
		}
		b.WriteString(" ROWS")
		if stmt.Fetch.WithTies {
			b.WriteString(" WITH TIES")
		} else {
			b.WriteString(" ONLY")
		}
	}
	return b.String(), nil
}

func (g generator) exprList(b *strings.Builder, expressions []Expr) error {
	for i, expression := range expressions {
		if i > 0 {
			b.WriteString(", ")
		}
		text, err := g.expr(expression, 0)
		if err != nil {
			return err
		}
		b.WriteString(text)
	}
	return nil
}

func (g generator) writeSelectItem(b *strings.Builder, item SelectItem) error {
	expr, err := g.expr(item.Expr, 0)
	if err != nil {
		return err
	}
	b.WriteString(expr)
	if item.Alias != nil {
		b.WriteString(" AS ")
		b.WriteString(generateIdentifier(*item.Alias))
	}
	if len(item.Except) > 0 {
		b.WriteString(" EXCEPT (")
		for i, expression := range item.Except {
			if i > 0 {
				b.WriteString(", ")
			}
			text, err := g.expr(expression, 0)
			if err != nil {
				return err
			}
			b.WriteString(text)
		}
		b.WriteByte(')')
	}
	if len(item.Replace) > 0 {
		b.WriteString(" REPLACE (")
		for i, replacement := range item.Replace {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := g.writeSelectItem(b, replacement); err != nil {
				return err
			}
		}
		b.WriteByte(')')
	}
	return nil
}

func (g generator) tableExpr(table *TableExpr) (string, error) {
	primary, err := g.fromItem(table.Primary)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(primary)
	for _, join := range table.Joins {
		b.WriteByte(' ')
		if join.JoinText != "" {
			b.WriteString(join.JoinText)
		} else {
			b.WriteString(joinKindText(join.Kind))
		}
		b.WriteByte(' ')
		right, err := g.fromItem(join.Right)
		if err != nil {
			return "", err
		}
		b.WriteString(right)
		if join.Condition != nil && !join.Late {
			condition, err := g.expr(join.Condition, 0)
			if err != nil {
				return "", err
			}
			b.WriteString(" ON ")
			b.WriteString(condition)
		} else if len(join.Using) > 0 && !join.Late {
			b.WriteString(" USING (")
			for i, column := range join.Using {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(generateIdentifier(column))
			}
			b.WriteByte(')')
		}
	}
	for _, join := range table.Joins {
		if !join.Late {
			continue
		}
		if join.Condition != nil {
			condition, err := g.expr(join.Condition, 0)
			if err != nil {
				return "", err
			}
			b.WriteString(" ON ")
			b.WriteString(condition)
		} else if len(join.Using) > 0 {
			b.WriteString(" USING (")
			for i, column := range join.Using {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(generateIdentifier(column))
			}
			b.WriteByte(')')
		}
	}
	for _, view := range table.LateralViews {
		expression, err := g.expr(view.Expression, 0)
		if err != nil {
			return "", err
		}
		b.WriteString(" LATERAL VIEW ")
		if view.Outer {
			b.WriteString("OUTER ")
		}
		b.WriteString(expression)
		if view.Alias != nil {
			b.WriteByte(' ')
			b.WriteString(generateIdentifier(*view.Alias))
		}
		if view.AliasExplicit || len(view.Columns) > 0 {
			b.WriteString(" AS ")
		}
		for i, column := range view.Columns {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(generateIdentifier(column))
		}
	}
	for _, modifier := range table.Modifiers {
		b.WriteByte(' ')
		b.WriteString(modifier)
	}
	return b.String(), nil
}

func (g generator) fromItem(item FromItem) (string, error) {
	switch item := item.(type) {
	case *TableName:
		text := generateIdentifiers(item.Parts)
		if item.Alias != nil {
			text += " AS " + generateIdentifier(*item.Alias)
		}
		if len(item.Columns) > 0 {
			text += "("
			for i, column := range item.Columns {
				if i > 0 {
					text += ", "
				}
				text += generateIdentifier(column)
			}
			text += ")"
		}
		if item.Sample != nil {
			text += " TABLESAMPLE "
			if item.Sample.Raw != "" {
				text += strings.TrimSpace(item.Sample.Raw)
			}
		}
		return text, nil
	case *SubqueryFrom:
		if item.Query == nil {
			return "", fmt.Errorf("cannot generate empty subquery")
		}
		query, err := g.selectStmt(item.Query)
		if err != nil {
			return "", err
		}
		prefix := ""
		if item.Lateral {
			prefix = "LATERAL "
		}
		text := prefix + "(" + query + ")"
		if item.Alias != nil {
			text += " AS " + generateIdentifier(*item.Alias)
		}
		if len(item.Columns) > 0 {
			text += "("
			for i, column := range item.Columns {
				if i > 0 {
					text += ", "
				}
				text += generateIdentifier(column)
			}
			text += ")"
		}
		return text, nil
	case *GroupedFrom:
		text := "("
		for i, table := range item.Items {
			if i > 0 {
				text += ", "
			}
			tableText, err := g.tableExpr(&table)
			if err != nil {
				return "", err
			}
			text += tableText
		}
		text += ")"
		if item.Alias != nil {
			text += " AS " + generateIdentifier(*item.Alias)
		}
		if len(item.Columns) > 0 {
			text += "("
			for i, column := range item.Columns {
				if i > 0 {
					text += ", "
				}
				text += generateIdentifier(column)
			}
			text += ")"
		}
		return text, nil
	case *TableFunctionFrom:
		text := generateIdentifiers(item.Name)
		if item.RawArgs != "" {
			text += item.RawArgs
		} else {
			text += "("
			for i, arg := range item.Args {
				if i > 0 {
					text += ", "
				}
				argText, err := g.expr(arg, 0)
				if err != nil {
					return "", err
				}
				text += argText
			}
			text += ")"
		}
		if item.WithOrdinality {
			text += " WITH ORDINALITY"
		}
		if item.Alias != nil {
			text += " AS " + generateIdentifier(*item.Alias)
		}
		if len(item.Columns) > 0 {
			text += "("
			for i, column := range item.Columns {
				if i > 0 {
					text += ", "
				}
				text += generateIdentifier(column)
			}
			text += ")"
		}
		return text, nil
	case *RawFrom:
		text := item.Raw
		if item.Alias != nil {
			text += " AS " + generateIdentifier(*item.Alias)
		}
		if len(item.Columns) > 0 {
			text += "("
			for i, column := range item.Columns {
				if i > 0 {
					text += ", "
				}
				text += generateIdentifier(column)
			}
			text += ")"
		}
		return text, nil
	default:
		return "", fmt.Errorf("cannot generate unknown FROM item")
	}
}

func (g generator) expr(expression Expr, parentPrecedence int) (string, error) {
	if expression == nil {
		return "", fmt.Errorf("cannot generate nil expression")
	}
	precedence := expressionPrecedence(expression)
	var text string
	switch expression := expression.(type) {
	case *IdentifierExpr:
		text = generateIdentifiers(expression.Parts)
	case *LiteralExpr:
		text = expression.Raw
	case *StarExpr:
		text = "*"
	case *UnaryExpr:
		right, err := g.expr(expression.Expr, precedence)
		if err != nil {
			return "", err
		}
		if strings.EqualFold(expression.Operator, "NOT") {
			text = "NOT " + right
		} else if len(right) > 0 && strings.ContainsRune("+-~", rune(right[0])) {
			// Keep adjacent symbolic unary operators unambiguous while avoiding
			// unnecessary whitespace for ordinary expressions (-x, ~x).
			text = expression.Operator + " " + right
		} else {
			text = expression.Operator + right
		}
	case *BinaryExpr:
		left, err := g.expr(expression.Left, precedence)
		if err != nil {
			return "", err
		}
		right, err := g.expr(expression.Right, 0)
		if err != nil {
			return "", err
		}
		if expression.Operator == "??" {
			text = "COALESCE(" + left + ", " + right + ")"
		} else if expression.Operator == "AGAINST" && strings.HasPrefix(right, "(") {
			text = left + " AGAINST" + right
		} else {
			text = left + " " + expression.Operator + " " + right
			if expression.Escape != nil {
				escape, err := g.expr(expression.Escape, precedence+1)
				if err != nil {
					return "", err
				}
				text += " ESCAPE " + escape
			}
		}
	case *InExpr:
		left, err := g.expr(expression.Value, precedence)
		if err != nil {
			return "", err
		}
		text = left
		if expression.Not {
			text += " NOT"
		}
		text += " IN ("
		if expression.Query != nil {
			query, err := g.selectStmt(expression.Query)
			if err != nil {
				return "", err
			}
			text += query
		} else {
			for i, item := range expression.Items {
				if i > 0 {
					text += ", "
				}
				itemText, err := g.expr(item, 0)
				if err != nil {
					return "", err
				}
				text += itemText
			}
		}
		text += ")"
	case *BetweenExpr:
		value, err := g.expr(expression.Value, precedence)
		if err != nil {
			return "", err
		}
		low, err := g.expr(expression.Low, precedence+1)
		if err != nil {
			return "", err
		}
		high, err := g.expr(expression.High, precedence+1)
		if err != nil {
			return "", err
		}
		text = value
		if expression.Not {
			text += " NOT"
		}
		text += " BETWEEN"
		if expression.Symmetric {
			text += " SYMMETRIC"
		}
		text += " " + low + " AND " + high
	case *IsExpr:
		value, err := g.expr(expression.Value, precedence)
		if err != nil {
			return "", err
		}
		right, err := g.expr(expression.Right, precedence+1)
		if err != nil {
			return "", err
		}
		text = value + " " + expression.Operator + " " + right
	case *FunctionCallExpr:
		text = generateIdentifiers(expression.Name)
		if expression.RawArgs != "" {
			text += expression.RawArgs
			break
		}
		text += "("
		if expression.Distinct {
			text += "DISTINCT "
		}
		if expression.Star {
			text += "*"
		} else {
			for i, arg := range expression.Args {
				if i > 0 {
					text += ", "
				}
				argText, err := g.expr(arg, 0)
				if err != nil {
					return "", err
				}
				text += argText
			}
		}
		if len(expression.OrderBy) > 0 {
			text += " ORDER BY "
			for i, item := range expression.OrderBy {
				if i > 0 {
					text += ", "
				}
				itemText, err := g.expr(item.Expr, 0)
				if err != nil {
					return "", err
				}
				text += itemText
				if item.Descending {
					text += " DESC"
				} else if item.Ascending {
					text += " ASC"
				}
			}
		}
		text += ")"
		if len(expression.WithinGroup) > 0 {
			text += " WITHIN GROUP (ORDER BY "
			for i, item := range expression.WithinGroup {
				if i > 0 {
					text += ", "
				}
				itemText, err := g.expr(item.Expr, 0)
				if err != nil {
					return "", err
				}
				text += itemText
				if item.Descending {
					text += " DESC"
				} else if item.Ascending {
					text += " ASC"
				}
			}
			text += ")"
		}
		if expression.Filter != nil {
			filter, err := g.expr(expression.Filter, 0)
			if err != nil {
				return "", err
			}
			text += " FILTER(WHERE " + filter + ")"
		}
		if expression.IgnoreNulls {
			text += " IGNORE NULLS"
		} else if expression.RespectNulls {
			text += " RESPECT NULLS"
		}
		if expression.Over != nil {
			windowText, err := g.windowSpec(*expression.Over)
			if err != nil {
				return "", err
			}
			text += " OVER " + windowText
		}
	case *CallExpr:
		callee, err := g.expr(expression.Callee, precedence)
		if err != nil {
			return "", err
		}
		text = callee + "("
		for i, arg := range expression.Args {
			if i > 0 {
				text += ", "
			}
			argText, err := g.expr(arg, 0)
			if err != nil {
				return "", err
			}
			text += argText
		}
		text += ")"
	case *GenericExpr:
		target, err := g.expr(expression.Target, precedence)
		if err != nil {
			return "", err
		}
		text = target + "<"
		for i, argument := range expression.Arguments {
			if i > 0 {
				text += ", "
			}
			argumentText, err := g.expr(argument, 0)
			if err != nil {
				return "", err
			}
			text += argumentText
		}
		text += ">"
	case *RawExpr:
		text = expression.Raw
	case *ExtractExpr:
		field, err := g.expr(expression.Field, 0)
		if err != nil {
			return "", err
		}
		source, err := g.expr(expression.Source, 0)
		if err != nil {
			return "", err
		}
		text = "EXTRACT(" + field + " FROM " + source + ")"
	case *TupleExpr:
		for i, item := range expression.Items {
			if i > 0 {
				text += ", "
			}
			itemText, err := g.expr(item, 0)
			if err != nil {
				return "", err
			}
			text += itemText
		}
	case *AliasExpr:
		inner, err := g.expr(expression.Expr, 0)
		if err != nil {
			return "", err
		}
		text = inner + " AS " + generateIdentifier(expression.Alias)
	case *IntervalExpr:
		value, err := g.expr(expression.Value, 0)
		if err != nil {
			return "", err
		}
		text = "INTERVAL " + value
		for _, qualifier := range expression.Qualifiers {
			qualifierText, err := g.expr(qualifier, 0)
			if err != nil {
				return "", err
			}
			text += " " + qualifierText
		}
	case *CastExpr:
		value, err := g.expr(expression.Value, 0)
		if err != nil {
			return "", err
		}
		typeText, err := g.expr(expression.Type, 0)
		if err != nil {
			return "", err
		}
		text = expression.Keyword + "(" + value + " AS " + typeText
		for _, suffix := range expression.TypeSuffix {
			text += " " + generateIdentifier(suffix)
		}
		text += ")"
	case *WindowedExpr:
		inner, err := g.expr(expression.Expr, precedence)
		if err != nil {
			return "", err
		}
		windowText, err := g.windowSpec(expression.Over)
		if err != nil {
			return "", err
		}
		text = inner + " OVER " + windowText
	case *SubqueryExpr:
		if expression.Query == nil {
			return "", fmt.Errorf("cannot generate subquery without a query")
		}
		query, err := g.selectStmt(expression.Query)
		if err != nil {
			return "", err
		}
		text = "(" + query + ")"
	case *ExistsExpr:
		if expression.Query == nil {
			return "", fmt.Errorf("cannot generate EXISTS without a query")
		}
		query, err := g.selectStmt(expression.Query)
		if err != nil {
			return "", err
		}
		text = "EXISTS(" + query + ")"
	case *QuantifiedExpr:
		if expression.Query == nil {
			return "", fmt.Errorf("cannot generate %s without a query", expression.Keyword)
		}
		query, err := g.selectStmt(expression.Query)
		if err != nil {
			return "", err
		}
		text = expression.Keyword + " (" + query + ")"
	case *GroupingExpr:
		text = expression.Name
		if expression.SpaceBeforeParen {
			text += " ("
		} else {
			text += "("
		}
		for i, argument := range expression.Args {
			if i > 0 {
				text += ", "
			}
			argumentText, err := g.expr(argument, 0)
			if err != nil {
				return "", err
			}
			text += argumentText
		}
		text += ")"
	case *SetExpr:
		left, err := g.expr(expression.Left, precedence)
		if err != nil {
			return "", err
		}
		right, err := g.expr(expression.Right, 0)
		if err != nil {
			return "", err
		}
		text = left + " " + expression.Operator
		if expression.All {
			text += " ALL"
		}
		text += " " + right
	case *TypedLiteralExpr:
		typeName := generateIdentifiers(expression.TypeName)
		if len(expression.Parameters) > 0 {
			typeName += "("
			for i, parameter := range expression.Parameters {
				if i > 0 {
					typeName += ", "
				}
				parameterText, err := g.expr(parameter, 0)
				if err != nil {
					return "", err
				}
				typeName += parameterText
			}
			typeName += ")"
		}
		if expression.Value == nil {
			if len(expression.Qualifiers) == 0 {
				return typeName, nil
			}
			text = canonicalTimestampType(typeName, expression.Qualifiers)
			return text, nil
		}
		if strings.EqualFold(typeName, "JSON") {
			text = "PARSE_JSON(" + expression.Value.Raw + ")"
		} else if strings.EqualFold(typeName, "N") {
			text = "N" + expression.Value.Raw
		} else {
			text = "CAST(" + expression.Value.Raw + " AS " + canonicalTimestampType(typeName, expression.Qualifiers) + ")"
		}
	case *CaseExpr:
		text = "CASE"
		if expression.Operand != nil {
			operand, err := g.expr(expression.Operand, 0)
			if err != nil {
				return "", err
			}
			text += " " + operand
		}
		for _, when := range expression.Whens {
			condition, err := g.expr(when.Condition, 0)
			if err != nil {
				return "", err
			}
			value, err := g.expr(when.Result, 0)
			if err != nil {
				return "", err
			}
			text += " WHEN " + condition + " THEN " + value
		}
		if expression.Else != nil {
			elseText, err := g.expr(expression.Else, 0)
			if err != nil {
				return "", err
			}
			text += " ELSE " + elseText
		}
		text += " END"
	case *ParenthesizedExpr:
		inner, err := g.expr(expression.Expr, 0)
		if err != nil {
			return "", err
		}
		text = "(" + inner + ")"
	case *IndexExpr:
		target, err := g.expr(expression.Target, precedence)
		if err != nil {
			return "", err
		}
		text = target + "["
		if len(expression.Indices) > 0 {
			for i, index := range expression.Indices {
				if i > 0 {
					text += ", "
				}
				indexText, err := g.expr(index, 0)
				if err != nil {
					return "", err
				}
				text += indexText
			}
		} else if expression.Low != nil {
			low, err := g.expr(expression.Low, 0)
			if err != nil {
				return "", err
			}
			text += low
		}
		if expression.Slice {
			text += ":"
			if expression.High != nil {
				high, err := g.expr(expression.High, 0)
				if err != nil {
					return "", err
				}
				text += high
			}
		}
		text += "]"
	case *FieldExpr:
		target, err := g.expr(expression.Target, precedence)
		if err != nil {
			return "", err
		}
		text = target + "." + generateIdentifier(expression.Field)
	case *MissingExpr:
		return "", fmt.Errorf("cannot generate recovered missing %s", expression.Expected)
	case *ErrorExpr:
		return "", fmt.Errorf("cannot generate recovered expression: %s", expression.Message)
	default:
		return "", fmt.Errorf("cannot generate expression kind %s", expression.Kind())
	}
	if precedence < parentPrecedence {
		return "(" + text + ")", nil
	}
	return text, nil
}

func (g generator) windowSpec(spec WindowSpec) (string, error) {
	if spec.Name != nil {
		return generateIdentifier(*spec.Name), nil
	}
	var b strings.Builder
	b.WriteByte('(')
	if spec.Base != nil {
		b.WriteString(generateIdentifier(*spec.Base))
	}
	if len(spec.PartitionBy) > 0 {
		if spec.Base != nil {
			b.WriteByte(' ')
		}
		b.WriteString("PARTITION BY ")
		if err := g.exprList(&b, spec.PartitionBy); err != nil {
			return "", err
		}
	}
	if len(spec.OrderBy) > 0 {
		if len(spec.PartitionBy) > 0 || spec.Base != nil {
			b.WriteByte(' ')
		}
		b.WriteString("ORDER BY ")
		for i, item := range spec.OrderBy {
			if i > 0 {
				b.WriteString(", ")
			}
			text, err := g.expr(item.Expr, 0)
			if err != nil {
				return "", err
			}
			b.WriteString(text)
			if item.Descending {
				b.WriteString(" DESC")
			} else if item.Ascending {
				b.WriteString(" ASC")
			}
		}
	}
	if spec.Frame != "" {
		if len(spec.PartitionBy) > 0 || len(spec.OrderBy) > 0 || spec.Base != nil {
			b.WriteByte(' ')
		}
		b.WriteString(spec.Frame)
	}
	b.WriteByte(')')
	return b.String(), nil
}

func canonicalTimestampType(typeName string, qualifiers []string) string {
	if len(qualifiers) == 0 {
		return typeName
	}
	first := strings.ToUpper(qualifiers[0])
	if strings.HasPrefix(strings.ToUpper(typeName), "TIMESTAMP") {
		switch {
		case first == "WITH" && containsString(qualifiers, "LOCAL"):
			return "TIMESTAMPLTZ" + typeParametersSuffix(typeName)
		case first == "WITH":
			return "TIMESTAMPTZ" + typeParametersSuffix(typeName)
		default:
			return "TIMESTAMP" + typeParametersSuffix(typeName)
		}
	}
	return typeName
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

func typeParametersSuffix(typeName string) string {
	index := strings.IndexByte(typeName, '(')
	if index < 0 {
		return ""
	}
	return typeName[index:]
}

func expressionPrecedence(expression Expr) int {
	switch expression := expression.(type) {
	case *BinaryExpr:
		switch strings.ToUpper(expression.Operator) {
		case "OR":
			return 1
		case "AND":
			return 2
		case "=", "<>", "!=", "<", "<=", ">", ">=", "~", "~*", "!~", "!~*", "LIKE", "LIKE ANY", "ILIKE", "ILIKE ANY", "GLOB", "IN", "BETWEEN", "IS", "IS NOT", "OVERLAPS":
			return 3
		case "COLLATE":
			return 7
		case "??", "-|-":
			return 3
		case "AGAINST":
			return 3
		case "||", "->", "->>", "#>", "#>>", "AT TIME ZONE":
			return 4
		case "+", "-", "&", "|", "^", "<<", ">>":
			return 5
		case "*", "/", "%":
			return 6
		default:
			return 3
		}
	case *InExpr, *BetweenExpr, *IsExpr:
		return 3
	case *UnaryExpr:
		return 7
	case *CallExpr, *IndexExpr, *FieldExpr:
		return 8
	case *GenericExpr, *RawExpr, *ExtractExpr, *TupleExpr, *AliasExpr, *IntervalExpr, *CastExpr, *WindowedExpr:
		return 8
	case *SubqueryExpr, *ExistsExpr, *QuantifiedExpr, *GroupingExpr, *SetExpr:
		return 8
	default:
		return 8
	}
}

func generateIdentifiers(identifiers []Identifier) string {
	parts := make([]string, len(identifiers))
	for i, identifier := range identifiers {
		parts[i] = generateIdentifier(identifier)
	}
	return strings.Join(parts, ".")
}

func generateIdentifier(identifier Identifier) string {
	if !identifier.Quoted {
		return identifier.Text
	}
	return `"` + strings.ReplaceAll(identifier.Text, `"`, `""`) + `"`
}

func joinKindText(kind JoinKind) string {
	switch kind {
	case JoinLeft:
		return "LEFT JOIN"
	case JoinRight:
		return "RIGHT JOIN"
	case JoinFull:
		return "FULL JOIN"
	case JoinCross:
		return "CROSS JOIN"
	default:
		return "JOIN"
	}
}
