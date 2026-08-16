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
	indent    int
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
		if g.pretty {
			return g.prettyCreateTableStmt(n)
		}
		text := "CREATE "
		if n.Materialized {
			text += "MATERIALIZED "
		}
		if n.Temporary {
			if g.dialect == DialectOracle {
				text += "GLOBAL TEMPORARY "
			} else {
				text += "TEMPORARY "
			}
		}
		if g.dialect == DialectSpark && n.Temporary && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(n.Tail)), "AS ") {
			text += "VIEW "
		} else {
			text += "TABLE "
		}
		if n.IfNotExists {
			text += "IF NOT EXISTS "
		}
		text += generateIdentifiers(n.Name)
		if n.Tail != "" {
			if !(g.dialect == DialectSnowflake && len(n.Name) == 1 && strings.EqualFold(n.Name[0].Text, "IDENTIFIER") && strings.HasPrefix(strings.TrimSpace(n.Tail), "(")) {
				text += " "
			}
			text += n.Tail
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
		if g.canonical && strings.EqualFold(n.Keyword, "SET") {
			return normalizeSetCommand(n.Raw), nil
		}
		return n.Raw, nil
	case *RawStmt:
		if g.pretty {
			if text, ok, err := g.prettyRawStatement(n.Raw); ok || err != nil {
				return text, err
			}
		}
		if g.canonical {
			if g.dialect == DialectSnowflake && (strings.Contains(n.Raw, "\n") && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(n.Raw)), "CREATE STORAGE INTEGRATION ") || strings.Contains(n.Raw, `\n`) && strings.Contains(strings.ToUpper(strings.TrimSpace(n.Raw)), " LANGUAGE PYTHON ")) {
				return strings.TrimSpace(n.Raw), nil
			}
			if g.dialect == DialectBigQuery && strings.Contains(n.Raw, "\n") {
				return strings.TrimSpace(n.Raw), nil
			}
			if g.dialect == DialectAthena {
				return strings.TrimSpace(n.Raw), nil
			}
			if strings.Contains(n.Raw, "$$") || strings.Contains(n.Raw, "$FOO$") {
				return strings.TrimSpace(n.Raw), nil
			}
			return canonicalRawSQL(n.Raw), nil
		}
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
		if g.dialect == DialectTSQL && len(stmt.Query.With) > 0 {
			selectIndex := indexKeywordTopLevel(query, "SELECT")
			if selectIndex > 0 {
				withClause := strings.TrimSpace(query[:selectIndex])
				text = withClause + " " + text + " " + strings.TrimSpace(query[selectIndex:])
			} else {
				text += " " + query
			}
		} else {
			text += " " + query
		}
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
	if stmt.RawQuery != "" {
		return canonicalRawSQL(stmt.RawQuery), nil
	}
	if !g.pretty && g.dialect == DialectSnowflake && snowflakeSemanticViewNeedsPretty(stmt) {
		copyStmt := *stmt
		copyStmt.From = append([]TableExpr(nil), stmt.From...)
		table := copyStmt.From[0]
		function, ok := table.Primary.(*TableFunctionFrom)
		if ok {
			functionCopy := *function
			functionCopy.RawArgs = normalizeSnowflakeSemanticViewLayout(function.RawArgs)
			table.Primary = &functionCopy
			copyStmt.From[0] = table
			pretty := g
			pretty.pretty = true
			return pretty.prettySelectStmt(&copyStmt)
		}
	}
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(stmt.Tail)), "MATCH_RECOGNIZE") && strings.Contains(stmt.Tail, "\n") {
		base := *stmt
		base.Tail = ""
		pretty := g
		pretty.pretty = true
		text, err := pretty.prettySelectStmt(&base)
		if err != nil {
			return "", err
		}
		return text + "\n" + strings.TrimSpace(stmt.Tail), nil
	}
	if g.pretty {
		return g.prettySelectStmt(stmt)
	}
	if len(stmt.ValuesRows) > 0 {
		return g.valuesStmt(stmt)
	}
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
		if stmt.SetModifier != "" {
			text += " " + stmt.SetModifier
		}
		text += " " + right
		text, err = g.appendQueryTail(text, stmt)
		if err != nil {
			return "", err
		}
		text = parenthesizeQuery(text, stmt)
		return text, nil
	}
	if len(stmt.Into) > 0 && (g.dialect == DialectSnowflake || g.dialect == DialectDuckDB) {
		base := *stmt
		base.Into = nil
		inner, err := g.selectStmt(&base)
		if err != nil {
			return "", err
		}
		prefix := "CREATE TABLE "
		if stmt.IntoTemporary && (g.dialect == DialectDuckDB || g.dialect == DialectSnowflake) {
			prefix = "CREATE TEMPORARY TABLE "
		}
		return prefix + generateIdentifiers(stmt.Into) + " AS " + inner, nil
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
			if cte.Modifier != "" {
				b.WriteByte(' ')
				b.WriteString(cte.Modifier)
			}
			b.WriteString(" AS")
			if cte.Materialized != "" {
				b.WriteByte(' ')
				b.WriteString(cte.Materialized)
			}
			b.WriteString(" (")
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
	if stmt.SelectModifier != "" {
		b.WriteByte(' ')
		b.WriteString(stmt.SelectModifier)
	}
	if stmt.Top != nil && g.dialect == DialectTSQL {
		top, err := g.expr(stmt.Top, 0)
		if err != nil {
			return "", err
		}
		b.WriteString(" TOP")
		if stmt.TopParenthesized && !(strings.HasPrefix(top, "(") && strings.HasSuffix(top, ")")) {
			b.WriteString(" (")
			b.WriteString(top)
			b.WriteByte(')')
		} else {
			b.WriteByte(' ')
			b.WriteString(top)
		}
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
	if len(stmt.Into) > 0 {
		if g.dialect == DialectPostgreSQL && stmt.IntoTemporary {
			if g.pretty {
				b.WriteString("\nINTO TEMPORARY ")
			} else {
				b.WriteString(" INTO TEMPORARY ")
			}
		} else {
			if g.pretty {
				b.WriteString("\nINTO ")
			} else {
				b.WriteString(" INTO ")
			}
		}
		b.WriteString(generateIdentifiers(stmt.Into))
	}
	if len(stmt.From) > 0 {
		if g.pretty {
			b.WriteString("\nFROM ")
		} else {
			b.WriteString(" FROM ")
		}
		for i, table := range stmt.From {
			if i > 0 {
				if g.dialect == DialectBigQuery || g.dialect == DialectDatabricks || g.dialect == DialectSpark {
					b.WriteString(" CROSS JOIN ")
				} else {
					b.WriteString(", ")
				}
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
			b.WriteString("\nGROUP BY")
		} else {
			b.WriteString(" GROUP BY")
		}
		if stmt.GroupByDistinct {
			b.WriteString(" DISTINCT")
		}
		b.WriteByte(' ')
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
			} else if item.NullsFirst {
				b.WriteString(" NULLS FIRST")
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
			} else if item.NullsFirst {
				b.WriteString(" NULLS FIRST")
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
		if g.dialect == DialectOracle {
			b.WriteString(" ROWS")
		}
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
		if stmt.SetModifier != "" {
			b.WriteByte(' ')
			b.WriteString(stmt.SetModifier)
		}
		if stmt.SetRight == nil {
			return "", fmt.Errorf("cannot generate set operation without right query")
		}
		rightGenerator := g
		rightGenerator.indent++
		right, err := rightGenerator.selectStmt(stmt.SetRight)
		if err != nil {
			return "", err
		}
		if stmt.SetRightParen && !stmt.SetRight.Parenthesized {
			right = "(" + right + ")"
		}
		b.WriteByte(' ')
		b.WriteString(right)
	}
	if g.dialect == DialectTSQL && g.indent == 0 && isTSQLForQueryTail(stmt.Tail) {
		base := *stmt
		base.Tail = ""
		pretty := g
		pretty.pretty = true
		baseText, err := pretty.prettySelectStmt(&base)
		if err != nil {
			return "", err
		}
		return baseText + "\n" + formatTSQLForQueryTail(stmt.Tail), nil
	}
	if stmt.Tail != "" {
		if g.pretty {
			b.WriteString("\n")
		} else {
			b.WriteByte(' ')
		}
		b.WriteString(strings.TrimSpace(stmt.Tail))
	}
	result := b.String()
	result = parenthesizeQuery(result, stmt)
	return result, nil
}

func snowflakeSemanticViewNeedsPretty(stmt *SelectStmt) bool {
	if len(stmt.From) != 1 {
		return false
	}
	function, ok := stmt.From[0].Primary.(*TableFunctionFrom)
	return ok && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "SEMANTIC_VIEW") && strings.Contains(strings.ToUpper(function.RawArgs), " WHERE ")
}

func normalizeSnowflakeSemanticViewLayout(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '(' || trimmed[len(trimmed)-1] != ')' {
		return raw
	}
	body := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	upper := strings.ToUpper(body)
	metrics := strings.Index(upper, " METRICS ")
	dimensions := strings.Index(upper, " DIMENSIONS ")
	where := strings.Index(upper, " WHERE ")
	if metrics < 0 || dimensions < metrics || where < dimensions {
		return raw
	}
	return "(\n  " + strings.TrimSpace(body[:metrics]) + "\n  " + strings.TrimSpace(body[metrics+1:dimensions]) + "\n  " + strings.TrimSpace(body[dimensions+1:where]) + "\n  " + strings.TrimSpace(body[where+1:]) + "\n)"
}

func isTSQLForQueryTail(tail string) bool {
	upper := strings.ToUpper(strings.TrimSpace(tail))
	return strings.HasPrefix(upper, "FOR XML") || strings.HasPrefix(upper, "FOR JSON")
}

func formatTSQLForQueryTail(tail string) string {
	parts := strings.Fields(strings.TrimSpace(tail))
	if len(parts) < 2 {
		return strings.TrimSpace(tail)
	}
	kind := strings.ToUpper(parts[0] + " " + parts[1])
	rest := strings.TrimSpace(strings.TrimSpace(tail)[len(parts[0]):])
	if len(rest) >= len(parts[1]) {
		rest = strings.TrimSpace(rest[len(parts[1]):])
	}
	values := splitTopLevelSQL(rest, ',')
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
	}
	return kind + "\n  " + strings.Join(values, ",\n  ")
}

func (g generator) prettySelectStmt(stmt *SelectStmt) (string, error) {
	if stmt.RawQuery != "" {
		return canonicalRawSQL(stmt.RawQuery), nil
	}
	if len(stmt.ValuesRows) > 0 {
		return g.valuesStmt(stmt)
	}
	prefix := indentString(g.indent)
	if stmt.SetLeft != nil {
		leftGenerator := g
		leftGenerator.indent++
		left, err := leftGenerator.selectStmt(stmt.SetLeft)
		if err != nil {
			return "", err
		}
		if stmt.SetLeftParen {
			left = prefix + "(\n" + left + "\n" + prefix + ")"
		}
		right, err := g.selectStmt(stmt.SetRight)
		if err != nil {
			return "", err
		}
		if stmt.SetRightParen {
			right = prefix + "(\n" + indentLines(right, 1) + "\n" + prefix + ")"
		}
		text := left + "\n" + prefix + stmt.SetOperator
		if stmt.SetAll {
			text += " ALL"
		}
		if stmt.SetModifier != "" {
			text += " " + stmt.SetModifier
		}
		text += "\n" + right
		return parenthesizeQuery(text, stmt), nil
	}
	if stmt.SetOperator != "" {
		leftStmt := *stmt
		leftStmt.SetOperator = ""
		leftStmt.SetRight = nil
		leftStmt.SetLeft = nil
		leftStmt.Parenthesized = false
		leftStmt.ParenthesisDepth = 0
		leftGenerator := g
		if stmt.SetLeftParen {
			leftGenerator.indent++
		}
		left, err := leftGenerator.selectStmt(&leftStmt)
		if err != nil {
			return "", err
		}
		if stmt.SetLeftParen {
			left = prefix + "(\n" + left + "\n" + prefix + ")"
		}
		rightStmt := *stmt.SetRight
		rightStmt.Parenthesized = false
		rightStmt.ParenthesisDepth = 0
		right, err := g.selectStmt(&rightStmt)
		if err != nil {
			return "", err
		}
		if stmt.SetRightParen {
			right = prefix + "(\n" + indentLines(right, 1) + "\n" + prefix + ")"
		}
		text := left + "\n" + prefix + stmt.SetOperator
		if stmt.SetAll {
			text += " ALL"
		}
		text += "\n" + right
		return text, nil
	}

	var b strings.Builder
	if len(stmt.With) > 0 {
		b.WriteString(prefix)
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
				b.WriteByte(' ')
				b.WriteString(cte.Modifier)
			}
			b.WriteString(" AS")
			if cte.Materialized != "" {
				b.WriteByte(' ')
				b.WriteString(cte.Materialized)
			}
			b.WriteString(" (")
			if cte.Query == nil {
				return "", fmt.Errorf("cannot generate CTE %s without a query", cte.Name.Text)
			}
			queryGenerator := g
			queryGenerator.indent++
			query, err := queryGenerator.selectStmt(cte.Query)
			if err != nil {
				return "", err
			}
			b.WriteByte('\n')
			b.WriteString(query)
			b.WriteByte('\n')
			b.WriteString(prefix)
			b.WriteByte(')')
		}
		b.WriteByte('\n')
	}
	b.WriteString(prefix)
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
		b.WriteString(" TOP")
		if stmt.TopParenthesized {
			b.WriteString(" (")
		} else {
			b.WriteByte(' ')
		}
		b.WriteString(top)
		if stmt.TopParenthesized {
			b.WriteByte(')')
		}
	}
	if len(stmt.Projections) == 0 {
		return "", fmt.Errorf("cannot generate SELECT without projections")
	}
	for i, item := range stmt.Projections {
		b.WriteByte('\n')
		b.WriteString(indentString(g.indent + 1))
		if err := g.withIndent(g.indent+1).writeSelectItem(&b, item); err != nil {
			return "", err
		}
		if i+1 < len(stmt.Projections) {
			b.WriteByte(',')
		}
	}
	if len(stmt.From) > 0 {
		b.WriteByte('\n')
		b.WriteString(prefix)
		b.WriteString("FROM ")
		for i, table := range stmt.From {
			if i > 0 {
				b.WriteString(", ")
			}
			text, err := g.prettyTableExpr(&table)
			if err != nil {
				return "", err
			}
			b.WriteString(text)
		}
	}
	if stmt.Where != nil {
		b.WriteByte('\n')
		b.WriteString(prefix)
		b.WriteString("WHERE\n")
		b.WriteString(indentString(g.indent + 1))
		text, err := g.withIndent(g.indent+1).expr(stmt.Where, 0)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
	}
	if len(stmt.GroupBy) > 0 {
		b.WriteByte('\n')
		b.WriteString(prefix)
		if len(stmt.GroupBy) == 1 && isAllExpression(stmt.GroupBy[0]) {
			b.WriteString("GROUP BY")
			if stmt.GroupByDistinct {
				b.WriteString(" DISTINCT")
			}
			b.WriteString(" ALL")
		} else {
			b.WriteString("GROUP BY")
			if stmt.GroupByDistinct {
				b.WriteString(" DISTINCT")
			}
			b.WriteByte('\n')
			if err := g.prettyExprList(&b, stmt.GroupBy, g.indent+1); err != nil {
				return "", err
			}
		}
	}
	if stmt.Having != nil {
		b.WriteByte('\n')
		b.WriteString(prefix)
		b.WriteString("HAVING\n")
		b.WriteString(indentString(g.indent + 1))
		text, err := g.withIndent(g.indent+1).expr(stmt.Having, 0)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
	}
	if stmt.Qualify != nil {
		b.WriteByte('\n')
		b.WriteString(prefix)
		b.WriteString("QUALIFY\n")
		b.WriteString(indentString(g.indent + 1))
		text, err := g.withIndent(g.indent+1).expr(stmt.Qualify, 0)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
	}
	if len(stmt.OrderBy) > 0 {
		b.WriteByte('\n')
		b.WriteString(prefix)
		b.WriteString("ORDER BY\n")
		if err := g.prettyOrderList(&b, stmt.OrderBy, g.indent+1); err != nil {
			return "", err
		}
	}
	if stmt.Limit != nil {
		b.WriteByte('\n')
		b.WriteString(prefix)
		b.WriteString("LIMIT ")
		text, err := g.expr(stmt.Limit, 0)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
	}
	if stmt.Offset != nil {
		b.WriteByte('\n')
		b.WriteString(prefix)
		b.WriteString("OFFSET ")
		text, err := g.expr(stmt.Offset, 0)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
	}
	result := b.String()
	return parenthesizeQuery(result, stmt), nil
}

func (g generator) withIndent(indent int) generator {
	g.indent = indent
	return g
}

func indentString(indent int) string {
	if indent <= 0 {
		return ""
	}
	return strings.Repeat("  ", indent)
}

func indentLines(text string, indent int) string {
	prefix := indentString(indent)
	lines := strings.Split(text, "\n")
	for i := range lines {
		if lines[i] != "" {
			lines[i] = prefix + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

func (g generator) prettyExprList(b *strings.Builder, expressions []Expr, indent int) error {
	for i, expression := range expressions {
		if i > 0 {
			b.WriteByte(',')
			b.WriteByte('\n')
		}
		b.WriteString(indentString(indent))
		text, err := g.withIndent(indent).expr(expression, 0)
		if err != nil {
			return err
		}
		b.WriteString(text)
	}
	return nil
}

func (g generator) prettyOrderList(b *strings.Builder, items []OrderItem, indent int) error {
	for i, item := range items {
		if i > 0 {
			b.WriteByte(',')
			b.WriteByte('\n')
		}
		b.WriteString(indentString(indent))
		text, err := g.withIndent(indent).expr(item.Expr, 0)
		if err != nil {
			return err
		}
		b.WriteString(text)
		if item.Descending {
			b.WriteString(" DESC")
		} else if item.Ascending {
			b.WriteString(" ASC")
		}
		if item.NullsLast {
			b.WriteString(" NULLS LAST")
		} else if item.NullsFirst {
			b.WriteString(" NULLS FIRST")
		}
	}
	return nil
}

func isAllExpression(expression Expr) bool {
	identifier, ok := expression.(*IdentifierExpr)
	return ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted && strings.EqualFold(identifier.Parts[0].Text, "ALL")
}

func (g generator) prettyTableExpr(table *TableExpr) (string, error) {
	primary, err := g.prettyFromItem(table.Primary)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(primary)
	lateRendered := make([]bool, len(table.Joins))
	nestedSegment := true
	writeCondition := func(join JoinClause, depth int) error {
		if join.Condition != nil {
			b.WriteByte('\n')
			b.WriteString(indentString(g.indent + depth))
			b.WriteString("ON ")
			condition, err := g.withIndent(g.indent+depth).expr(join.Condition, 0)
			if err != nil {
				return err
			}
			b.WriteString(condition)
			return nil
		}
		if len(join.Using) > 0 {
			b.WriteByte('\n')
			b.WriteString(indentString(g.indent + depth))
			b.WriteString("USING (")
			for j, column := range join.Using {
				if j > 0 {
					b.WriteString(", ")
				}
				b.WriteString(generateIdentifier(column))
			}
			b.WriteByte(')')
		}
		return nil
	}
	for i, join := range table.Joins {
		joinDepth := 0
		if i > 0 && nestedSegment {
			joinDepth = 1
		}
		b.WriteByte('\n')
		b.WriteString(indentString(g.indent + joinDepth))
		if join.JoinText != "" {
			b.WriteString(join.JoinText)
		} else {
			b.WriteString(joinKindText(join.Kind))
		}
		b.WriteByte(' ')
		right, err := g.prettyFromItem(join.Right)
		if err != nil {
			return "", err
		}
		b.WriteString(right)
		if join.Condition != nil && !join.Late {
			if err := writeCondition(join, joinDepth+1); err != nil {
				return "", err
			}
		} else if len(join.Using) > 0 && !join.Late {
			if err := writeCondition(join, joinDepth+1); err != nil {
				return "", err
			}
		}
		if !join.Late && (join.Condition != nil || len(join.Using) > 0) {
			for lateIndex, lateJoin := range table.Joins {
				if lateJoin.Late && !lateRendered[lateIndex] && lateJoin.Condition != nil && lateIndex < i {
					if err := writeCondition(lateJoin, joinDepth); err != nil {
						return "", err
					}
					lateRendered[lateIndex] = true
					nestedSegment = false
				}
			}
		}
	}
	for i, join := range table.Joins {
		if !join.Late || lateRendered[i] {
			continue
		}
		if err := writeCondition(join, 1); err != nil {
			return "", err
		}
	}
	for _, view := range table.LateralViews {
		b.WriteByte('\n')
		b.WriteString(indentString(g.indent))
		b.WriteString("LATERAL VIEW")
		if view.Outer {
			b.WriteString(" OUTER")
		}
		b.WriteByte('\n')
		b.WriteString(indentString(g.indent))
		expression, err := g.withIndent(g.indent+1).expr(view.Expression, 0)
		if err != nil {
			return "", err
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
	return b.String(), nil
}

func (g generator) prettyFromItem(item FromItem) (string, error) {
	subquery, ok := item.(*SubqueryFrom)
	if !ok || subquery.Query == nil {
		return g.fromItem(item)
	}
	queryGenerator := g
	queryGenerator.indent++
	query, err := queryGenerator.selectStmt(subquery.Query)
	if err != nil {
		return "", err
	}
	text := "(\n" + query + "\n" + indentString(g.indent) + ")"
	if subquery.Alias != nil {
		text += " AS " + generateIdentifier(*subquery.Alias)
	}
	return text, nil
}

func functionNeedsPrettyLayout(function *FunctionCallExpr) bool {
	if function.RawArgs != "" || len(function.Args) >= 5 {
		return true
	}
	compact, err := (generator{canonical: true, dialect: DialectGeneric}).expr(function, 0)
	return err == nil && len(compact) > 48
}

func (g generator) functionArgument(function *FunctionCallExpr, arg Expr) (string, error) {
	if len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "ARRAY") {
		if subquery, ok := arg.(*SubqueryExpr); ok && subquery.Query != nil && !subquery.Parenthesized {
			return g.selectStmt(subquery.Query)
		}
	}
	return g.expr(arg, 0)
}

func (g generator) prettyFunctionCall(function *FunctionCallExpr, parentPrecedence int) (string, error) {
	name := generateFunctionName(function.Name)
	if len(function.Name) > 0 && !function.Name[len(function.Name)-1].Quoted {
		parts := make([]Identifier, len(function.Name))
		copy(parts, function.Name)
		parts[len(parts)-1].Text = strings.ToUpper(parts[len(parts)-1].Text)
		name = generateFunctionName(parts)
	}
	if function.RawArgs != "" {
		return name + function.RawArgs, nil
	}
	var b strings.Builder
	b.WriteString(name)
	b.WriteString("(\n")
	if function.Distinct {
		b.WriteString(indentString(g.indent + 1))
		b.WriteString("DISTINCT ")
	}
	for i, arg := range function.Args {
		if i > 0 {
			b.WriteString(",\n")
		}
		if !function.Distinct {
			b.WriteString(indentString(g.indent + 1))
		}
		argText, err := g.withIndent(g.indent+1).functionArgument(function, arg)
		if err != nil {
			return "", err
		}
		b.WriteString(argText)
	}
	b.WriteByte('\n')
	b.WriteString(indentString(g.indent))
	b.WriteByte(')')
	if len(function.WithinGroup) > 0 {
		b.WriteString(" WITHIN GROUP (ORDER BY ")
		for i, item := range function.WithinGroup {
			if i > 0 {
				b.WriteString(", ")
			}
			itemText, err := g.expr(item.Expr, 0)
			if err != nil {
				return "", err
			}
			b.WriteString(itemText)
		}
		b.WriteByte(')')
	}
	if function.Over != nil {
		window, err := g.windowSpec(*function.Over)
		if err != nil {
			return "", err
		}
		b.WriteString(" OVER ")
		b.WriteString(window)
	}
	text := b.String()
	if expressionPrecedence(function) < parentPrecedence {
		return "(" + text + ")", nil
	}
	return text, nil
}

func (g generator) prettyGroupingExpr(expression *GroupingExpr) (string, error) {
	var b strings.Builder
	b.WriteString(expression.Name)
	b.WriteByte(' ')
	b.WriteString("(\n")
	for i, argument := range expression.Args {
		if i > 0 {
			b.WriteString(",\n")
		}
		b.WriteString(indentString(g.indent + 1))
		text, err := g.withIndent(g.indent+1).expr(argument, 0)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
	}
	b.WriteByte('\n')
	b.WriteString(indentString(g.indent))
	b.WriteByte(')')
	return b.String(), nil
}

func (g generator) prettyCaseExpr(expression *CaseExpr, parentPrecedence int) (string, error) {
	if len(expression.Whens) <= 1 && expression.Operand == nil {
		// Short CASE expressions are more readable inline and match the
		// canonical SQL form used inside ordinary function arguments.
		compact := g
		compact.pretty = false
		return compact.expr(expression, parentPrecedence)
	}
	var b strings.Builder
	b.WriteString("CASE")
	if expression.Operand != nil {
		operand, err := g.expr(expression.Operand, 0)
		if err != nil {
			return "", err
		}
		b.WriteByte(' ')
		b.WriteString(operand)
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
		b.WriteByte('\n')
		b.WriteString(indentString(g.indent + 1))
		b.WriteString("WHEN ")
		b.WriteString(condition)
		b.WriteByte('\n')
		b.WriteString(indentString(g.indent + 1))
		b.WriteString("THEN ")
		b.WriteString(value)
	}
	if expression.Else != nil {
		elseText, err := g.expr(expression.Else, 0)
		if err != nil {
			return "", err
		}
		b.WriteByte('\n')
		b.WriteString(indentString(g.indent + 1))
		b.WriteString("ELSE ")
		b.WriteString(elseText)
	}
	b.WriteByte('\n')
	b.WriteString(indentString(g.indent))
	b.WriteString("END")
	text := b.String()
	if expressionPrecedence(expression) < parentPrecedence {
		return "(" + text + ")", nil
	}
	return text, nil
}

func isComplexPrettyExpr(expression Expr) bool {
	switch expression.(type) {
	case *BinaryExpr, *InExpr, *BetweenExpr, *IsExpr, *SubqueryExpr, *ExistsExpr, *CaseExpr:
		return true
	default:
		return false
	}
}

func (g generator) valuesStmt(stmt *SelectStmt) (string, error) {
	var values strings.Builder
	values.WriteString("VALUES ")
	for i, row := range stmt.ValuesRows {
		if i > 0 {
			values.WriteString(", ")
		}
		values.WriteByte('(')
		if err := g.exprList(&values, row); err != nil {
			return "", err
		}
		values.WriteByte(')')
	}
	if g.dialect == DialectGeneric {
		alias := "_values"
		if stmt.ValuesAlias != nil {
			alias = generateIdentifier(*stmt.ValuesAlias)
		}
		if len(stmt.ValuesColumns) > 0 {
			alias += "("
			for i, column := range stmt.ValuesColumns {
				if i > 0 {
					alias += ", "
				}
				alias += generateIdentifier(column)
			}
			alias += ")"
		}
		return "SELECT * FROM (" + values.String() + ") AS " + alias, nil
	}
	return values.String(), nil
}

func (g generator) prettyCreateTableStmt(stmt *CreateTableStmt) (string, error) {
	prefix := "CREATE "
	if stmt.Materialized {
		prefix += "MATERIALIZED "
	}
	if stmt.Temporary {
		prefix += "TEMPORARY "
	}
	prefix += "TABLE "
	if stmt.IfNotExists {
		prefix += "IF NOT EXISTS "
	}
	prefix += generateIdentifiers(stmt.Name)
	tail := strings.TrimSpace(stmt.Tail)
	if tail == "" || !strings.HasPrefix(tail, "(") || !strings.HasSuffix(tail, ")") {
		if tail != "" {
			prefix += " " + tail
		}
		return prefix, nil
	}
	inner := strings.TrimSpace(tail[1 : len(tail)-1])
	columns := splitTopLevelSQL(inner, ',')
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString(" (\n")
	for i, column := range columns {
		if i > 0 {
			b.WriteString(",\n")
		}
		b.WriteString(indentString(1))
		b.WriteString(prettyCreateColumn(column))
	}
	b.WriteString("\n)")
	return b.String(), nil
}

func prettyCreateColumn(column string) string {
	column = strings.TrimSpace(column)
	index := strings.IndexAny(column, " \t\r\n")
	if index < 0 {
		return strings.ToUpper(column)
	}
	name := strings.TrimSpace(column[:index])
	typeAndConstraints := strings.TrimSpace(column[index:])
	return name + " " + strings.ToUpper(typeAndConstraints)
}

func (g generator) prettyRawStatement(raw string) (string, bool, error) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(raw), ";"))
	upper := strings.ToUpper(trimmed)
	switch {
	case strings.HasPrefix(upper, "INSERT OVERWRITE TABLE "):
		return g.prettyInsertOverwrite(trimmed)
	case strings.HasPrefix(upper, "INSERT INTO TABLE ") && indexKeywordTopLevel(trimmed, "REPLACE") >= 0:
		return g.prettyInsertReplace(trimmed)
	case strings.HasPrefix(upper, "INSERT FIRST "):
		return g.prettyInsertFirst(trimmed)
	case strings.HasPrefix(upper, "MERGE INTO "):
		return g.prettyMerge(trimmed)
	case strings.HasPrefix(upper, "ALTER TABLE "):
		return g.prettyAlter(trimmed)
	default:
		return "", false, nil
	}
}

func (g generator) prettyInsertOverwrite(sql string) (string, bool, error) {
	valuesIndex := indexKeywordTopLevel(sql, "VALUES")
	if valuesIndex < 0 {
		return sql, true, nil
	}
	header := strings.TrimSpace(sql[:valuesIndex])
	rows := splitTopLevelSQL(strings.TrimSpace(sql[valuesIndex+len("VALUES"):]), ',')
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\nVALUES")
	for i, row := range rows {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		b.WriteString("\n  ")
		b.WriteString(row)
		if i+1 < len(rows) {
			b.WriteByte(',')
		}
	}
	return b.String(), true, nil
}

func (g generator) prettyInsertReplace(sql string) (string, bool, error) {
	replaceIndex := indexKeywordTopLevel(sql, "REPLACE")
	selectIndex := indexKeywordTopLevel(sql, "SELECT")
	if replaceIndex < 0 || selectIndex < 0 || selectIndex <= replaceIndex {
		return sql, true, nil
	}
	header := strings.TrimSpace(sql[:replaceIndex])
	header = strings.Replace(header, "INSERT INTO TABLE ", "INSERT INTO ", 1)
	replace := strings.TrimSpace(sql[replaceIndex:selectIndex])
	query, err := g.prettyRawSelect(sql[selectIndex:])
	if err != nil {
		return "", true, err
	}
	return header + "\n" + replace + "\n" + query, true, nil
}

func (g generator) prettyInsertFirst(sql string) (string, bool, error) {
	selectIndex := indexKeywordTopLevel(sql, "SELECT")
	whenIndex := indexKeywordTopLevel(sql, "WHEN")
	if selectIndex < 0 || whenIndex < 0 || whenIndex >= selectIndex {
		return sql, true, nil
	}
	clauses := splitTopLevelKeyword(sql[whenIndex:selectIndex], "WHEN")
	query, err := g.prettyRawSelect(sql[selectIndex:])
	if err != nil {
		return "", true, err
	}
	var b strings.Builder
	b.WriteString(strings.TrimSpace(sql[:whenIndex]))
	for _, clause := range clauses {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		b.WriteString("\n  ")
		b.WriteString(clause)
	}
	b.WriteByte('\n')
	b.WriteString(query)
	return b.String(), true, nil
}

func (g generator) prettyRawSelect(sql string) (string, error) {
	result, err := ParseStrict(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";")), g.dialect)
	if err != nil {
		return "", err
	}
	if len(result.Statements) != 1 {
		return "", fmt.Errorf("expected one SELECT statement in pretty raw statement")
	}
	query, ok := result.Statements[0].Node.(*SelectStmt)
	if !ok {
		return "", fmt.Errorf("expected SELECT statement in pretty raw statement")
	}
	return g.selectStmt(query)
}

func (g generator) prettyMerge(sql string) (string, bool, error) {
	usingIndex := indexKeywordTopLevel(sql, "USING")
	onIndex := indexKeywordTopLevel(sql, "ON")
	whenIndex := indexKeywordTopLevel(sql, "WHEN")
	if usingIndex < 0 || onIndex < 0 || whenIndex < 0 || usingIndex >= onIndex || onIndex >= whenIndex {
		return sql, true, nil
	}
	tail := strings.TrimSpace(sql[whenIndex:])
	setIndex := indexKeywordTopLevel(tail, "SET")
	if setIndex < 0 {
		return strings.TrimSpace(sql[:usingIndex]) + "\n" + strings.TrimSpace(sql[usingIndex:onIndex]) + "\n" + strings.TrimSpace(sql[onIndex:whenIndex]) + "\n" + tail, true, nil
	}
	assignments := splitTopLevelSQL(strings.TrimSpace(tail[setIndex+len("SET"):]), ',')
	var b strings.Builder
	b.WriteString(strings.TrimSpace(sql[:usingIndex]))
	b.WriteByte('\n')
	b.WriteString(strings.TrimSpace(sql[usingIndex:onIndex]))
	b.WriteByte('\n')
	b.WriteString(strings.TrimSpace(sql[onIndex:whenIndex]))
	b.WriteByte('\n')
	b.WriteString(strings.TrimSpace(tail[:setIndex+len("SET")]))
	for i, assignment := range assignments {
		assignment = strings.TrimSpace(assignment)
		if assignment == "" {
			continue
		}
		b.WriteByte('\n')
		b.WriteString(indentString(1))
		b.WriteString(assignment)
		if i+1 < len(assignments) {
			b.WriteByte(',')
		}
	}
	return b.String(), true, nil
}

func (g generator) prettyAlter(sql string) (string, bool, error) {
	addIndex := indexKeywordTopLevel(sql, "ADD")
	if addIndex < 0 {
		return sql, true, nil
	}
	header := strings.TrimSpace(sql[:addIndex])
	clause := strings.TrimSpace(sql[addIndex:])
	referencesIndex := indexKeywordTopLevel(clause, "REFERENCES")
	if referencesIndex < 0 {
		return header + "\n  " + clause, true, nil
	}
	open := strings.IndexByte(clause[referencesIndex:], '(')
	if open < 0 {
		return header + "\n  " + clause, true, nil
	}
	open += referencesIndex
	close := strings.LastIndexByte(clause, ')')
	if close <= open {
		return header + "\n  " + clause, true, nil
	}
	inner := strings.TrimSpace(clause[open+1 : close])
	suffix := strings.TrimSpace(clause[close+1:])
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("\n  ")
	b.WriteString(strings.TrimSpace(clause[:open]))
	b.WriteString(" (\n    ")
	b.WriteString(inner)
	b.WriteString("\n  )")
	if suffix != "" {
		b.WriteByte(' ')
		b.WriteString(suffix)
	}
	return b.String(), true, nil
}

func indexKeywordTopLevel(text, keyword string) int {
	depth := 0
	var quote byte
	for i := 0; i+len(keyword) <= len(text); i++ {
		c := text[i]
		if quote != 0 {
			if c == quote {
				if i+1 < len(text) && text[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		}
		if depth == 0 && strings.EqualFold(text[i:i+len(keyword)], keyword) && keywordBoundary(text, i, i+len(keyword)) {
			return i
		}
	}
	return -1
}

func splitTopLevelKeyword(text, keyword string) []string {
	var starts []int
	for offset := 0; offset < len(text); {
		index := indexKeywordTopLevel(text[offset:], keyword)
		if index < 0 {
			break
		}
		index += offset
		starts = append(starts, index)
		offset = index + len(keyword)
	}
	if len(starts) == 0 {
		return []string{text}
	}
	parts := make([]string, 0, len(starts))
	for i, start := range starts {
		end := len(text)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		parts = append(parts, text[start:end])
	}
	return parts
}

func keywordBoundary(text string, start, end int) bool {
	if start > 0 && isASCIIIdentifierByte(text[start-1]) {
		return false
	}
	return end >= len(text) || !isASCIIIdentifierByte(text[end])
}

func isASCIIIdentifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
}

func normalizeSetCommand(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 3 || !strings.EqualFold(trimmed[:3], "SET") {
		return raw
	}
	rest := strings.TrimSpace(trimmed[3:])
	if len(rest) >= 2 && strings.EqualFold(rest[:2], "TO") {
		return "SET " + strings.TrimSpace(rest[2:])
	}
	if index := strings.Index(strings.ToUpper(rest), " TO "); index >= 0 {
		return "SET " + strings.TrimSpace(rest[:index]) + " = " + strings.TrimSpace(rest[index+4:])
	}
	return trimmed
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
			} else if item.NullsFirst {
				b.WriteString(" NULLS FIRST")
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
	if len(item.AliasColumns) > 0 {
		b.WriteString(" AS (")
		for i, column := range item.AliasColumns {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(generateIdentifier(column))
		}
		b.WriteByte(')')
	}
	if len(item.Except) > 0 {
		if g.dialect == DialectDuckDB || g.dialect == DialectSnowflake {
			b.WriteString(" EXCLUDE (")
		} else {
			b.WriteString(" EXCEPT (")
		}
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
		keyword := "REPLACE"
		if item.Replace[0].Rename {
			keyword = "RENAME"
		}
		b.WriteString(" " + keyword + " (")
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
		if g.dialect == DialectGeneric {
			modifier = strings.Replace(modifier, "PIVOT (", "PIVOT(", 1)
			modifier = strings.Replace(modifier, "UNPIVOT (", "UNPIVOT(", 1)
		}
		b.WriteByte(' ')
		b.WriteString(modifier)
	}
	return b.String(), nil
}

func (g generator) fromItem(item FromItem) (string, error) {
	switch item := item.(type) {
	case *TableName:
		text := generateIdentifiers(item.Parts)
		if item.Tail != "" {
			text += " " + strings.TrimSpace(item.Tail)
		}
		if item.Alias != nil {
			if g.dialect == DialectOracle {
				text += " " + generateIdentifier(*item.Alias)
			} else if (g.dialect == DialectSpark || g.dialect == DialectDatabricks) && len(item.Parts) == 1 && strings.EqualFold(item.Parts[0].Text, "STREAM") {
				text += " " + generateIdentifier(*item.Alias)
			} else {
				text += " AS " + generateIdentifier(*item.Alias)
			}
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
		if item.Hint != "" {
			text += " " + strings.TrimSpace(item.Hint)
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
			if g.dialect == DialectOracle {
				text += " " + generateIdentifier(*item.Alias)
			} else {
				text += " AS " + generateIdentifier(*item.Alias)
			}
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
		if g.dialect == DialectDataFusion && len(item.Name) == 1 && !item.Name[0].Quoted && strings.EqualFold(item.Name[0].Text, "UNNEST") {
			text = "UNNEST"
		}
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
		} else if item.WithOffset {
			text += " WITH OFFSET"
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
		if normalized, ok := normalizeValuesFromRaw(text); ok {
			text = normalized
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
	default:
		return "", fmt.Errorf("cannot generate unknown FROM item")
	}
}

func normalizeValuesFromRaw(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 8 || trimmed[0] != '(' || !strings.EqualFold(trimmed[1:7], "VALUES") {
		return raw, false
	}
	inner := strings.TrimSpace(trimmed[7 : len(trimmed)-1])
	if !strings.HasSuffix(trimmed, ")") || inner == "" {
		return raw, false
	}
	// The parser intentionally keeps dialect-specific VALUES FROM forms raw.
	// Normalize the common scalar form without attempting to split nested SQL.
	parts := splitTopLevelSQL(inner, ',')
	if len(parts) == 0 {
		return raw, false
	}
	var b strings.Builder
	b.WriteString("(VALUES ")
	for i, part := range parts {
		if i > 0 {
			b.WriteString(", ")
		}
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "(") && strings.HasSuffix(part, ")") {
			b.WriteString(part)
		} else {
			b.WriteByte('(')
			b.WriteString(part)
			b.WriteByte(')')
		}
	}
	b.WriteString(")")
	return b.String(), true
}

func splitTopLevelSQL(text string, separator rune) []string {
	var parts []string
	start, depth := 0, 0
	var quote byte
	for i := 0; i < len(text); i++ {
		c := text[i]
		if quote != 0 {
			if c == quote {
				if i+1 < len(text) && text[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '(', '[', '<':
			depth++
		case ')', ']', '>':
			if depth > 0 {
				depth--
			}
		default:
			if rune(c) == separator && depth == 0 {
				parts = append(parts, text[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, text[start:])
	return parts
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
		if g.pretty && len(expression.Parts) == 1 && !expression.Parts[0].Quoted {
			if strings.EqualFold(expression.Parts[0].Text, "ALL") {
				text = "ALL"
			}
		}
	case *LiteralExpr:
		text = expression.Raw
	case *StarExpr:
		text = "*"
	case *UnaryExpr:
		rightPrecedence := precedence
		if strings.EqualFold(expression.Operator, "NOT") {
			rightPrecedence = 0
		}
		right, err := g.expr(expression.Expr, rightPrecedence)
		if err != nil {
			return "", err
		}
		if expression.Operator == "+" {
			text = right
		} else if strings.EqualFold(expression.Operator, "NOT") {
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
			queryGenerator := g
			if g.pretty {
				queryGenerator.indent++
			}
			query, err := queryGenerator.selectStmt(expression.Query)
			if err != nil {
				return "", err
			}
			if g.pretty {
				text += "\n" + query + "\n" + indentString(g.indent)
			} else {
				text += query
			}
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
		} else if expression.Asymmetric {
			text += " ASYMMETRIC"
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
		if g.pretty && functionNeedsPrettyLayout(expression) && !(expression.ArrayLiteral && g.dialect == DialectBigQuery) {
			return g.prettyFunctionCall(expression, parentPrecedence)
		}
		text = generateFunctionName(expression.Name)
		if g.pretty && len(expression.Name) > 0 && !expression.Name[len(expression.Name)-1].Quoted {
			parts := make([]Identifier, len(expression.Name))
			copy(parts, expression.Name)
			parts[len(parts)-1].Text = strings.ToUpper(parts[len(parts)-1].Text)
			text = generateFunctionName(parts)
		}
		if expression.ArrayLiteral && arrayLiteralUsesBrackets(g.dialect) {
			if g.dialect == DialectBigQuery && bigQueryArrayNeedsPrettyLayout(expression) {
				return g.bigQueryArrayLiteral(expression)
			}
			text = "["
			if g.dialect == DialectPresto || g.dialect == DialectTrino || g.dialect == DialectPostgreSQL {
				text = "ARRAY["
			}
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
			text += "]"
			break
		}
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
				argText, err := g.functionArgument(expression, arg)
				if err != nil {
					return "", err
				}
				text += argText
			}
		}
		if expression.Having != nil {
			having, err := g.expr(expression.Having, 0)
			if err != nil {
				return "", err
			}
			text += " HAVING " + having
		}
		if expression.NullsInside {
			if expression.IgnoreNulls {
				text += " IGNORE NULLS"
			} else if expression.RespectNulls {
				text += " RESPECT NULLS"
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
				if item.NullsLast {
					text += " NULLS LAST"
				} else if item.NullsFirst {
					text += " NULLS FIRST"
				}
			}
		}
		if expression.ArgumentTail != "" {
			text += " " + expression.ArgumentTail
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
				if item.NullsLast {
					text += " NULLS LAST"
				} else if item.NullsFirst {
					text += " NULLS FIRST"
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
		if !expression.NullsInside && expression.IgnoreNulls {
			text += " IGNORE NULLS"
		} else if !expression.NullsInside && expression.RespectNulls {
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
		open, close := "<", ">"
		if g.dialect == DialectRisingWave {
			open, close = "(", ")"
		}
		text = target + open
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
		text += close
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
		if g.pretty && len(expression.Items) > 2 {
			var b strings.Builder
			b.WriteString("(\n")
			for i, item := range expression.Items {
				if i > 0 {
					b.WriteString(",\n")
				}
				b.WriteString(indentString(g.indent + 1))
				itemText, err := g.withIndent(g.indent+1).expr(item, 0)
				if err != nil {
					return "", err
				}
				b.WriteString(itemText)
			}
			b.WriteByte('\n')
			b.WriteString(indentString(g.indent))
			b.WriteByte(')')
			return b.String(), nil
		}
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
		if g.dialect == DialectGeneric {
			if literal, ok := expression.Value.(*LiteralExpr); ok {
				value, expression.Qualifiers = normalizeIntervalLiteral(literal, expression.Qualifiers)
			}
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
		queryGenerator := g
		queryGenerator.indent++
		query, err := queryGenerator.selectStmt(expression.Query)
		if err != nil {
			return "", err
		}
		if g.pretty {
			text = "(\n" + query + "\n" + indentString(g.indent) + ")"
		} else {
			text = "(" + query + ")"
		}
	case *ExistsExpr:
		if expression.Query == nil {
			return "", fmt.Errorf("cannot generate EXISTS without a query")
		}
		queryGenerator := g
		if g.pretty {
			queryGenerator.indent++
		}
		query, err := queryGenerator.selectStmt(expression.Query)
		if err != nil {
			return "", err
		}
		if g.pretty {
			text = "EXISTS(\n" + query + "\n" + indentString(g.indent) + ")"
		} else {
			text = "EXISTS(" + query + ")"
		}
	case *QuantifiedExpr:
		if expression.Query == nil {
			return "", fmt.Errorf("cannot generate %s without a query", expression.Keyword)
		}
		queryGenerator := g
		if g.pretty {
			queryGenerator.indent++
		}
		query, err := queryGenerator.selectStmt(expression.Query)
		if err != nil {
			return "", err
		}
		separator := ""
		if expression.SpaceBeforeParen {
			separator = " "
		}
		if g.pretty {
			text = expression.Keyword + separator + "(\n" + query + "\n" + indentString(g.indent) + ")"
		} else {
			text = expression.Keyword + separator + "(" + query + ")"
		}
	case *GroupingExpr:
		if g.pretty {
			return g.prettyGroupingExpr(expression)
		}
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
		typeName := canonicalTypeName(generateIdentifiers(expression.TypeName), g.dialect)
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
			text = canonicalTimestampTypeForDialect(typeName, expression.Qualifiers, g.dialect)
			return text, nil
		}
		if strings.EqualFold(typeName, "JSON") {
			text = "PARSE_JSON(" + expression.Value.Raw + ")"
		} else if strings.EqualFold(typeName, "N") {
			text = "N" + expression.Value.Raw
		} else {
			text = "CAST(" + expression.Value.Raw + " AS " + canonicalTimestampTypeForDialect(typeName, expression.Qualifiers, g.dialect) + ")"
		}
	case *CaseExpr:
		if g.pretty {
			return g.prettyCaseExpr(expression, parentPrecedence)
		}
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
		if g.pretty && isComplexPrettyExpr(expression.Expr) {
			text = "(\n" + indentString(g.indent+1) + inner + "\n" + indentString(g.indent) + ")"
		} else {
			text = "(" + inner + ")"
		}
		if g.pretty && strings.HasPrefix(inner, "(\n") {
			text = inner
		}
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
			if expression.Step != nil {
				text += ":"
				step, err := g.expr(expression.Step, 0)
				if err != nil {
					return "", err
				}
				text += step
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
	if binary, ok := expression.(*BinaryExpr); ok && (binary.Operator == "->" || binary.Operator == "->>") && parentPrecedence == 3 {
		return "(" + text + ")", nil
	}
	if precedence < parentPrecedence {
		return "(" + text + ")", nil
	}
	return text, nil
}

func arrayLiteralUsesBrackets(dialect Dialect) bool {
	switch dialect {
	case DialectAthena, DialectBigQuery, DialectClickHouse, DialectDataFusion, DialectDuckDB, DialectPostgreSQL, DialectPresto, DialectSnowflake, DialectStarRocks, DialectTrino:
		return true
	default:
		return false
	}
}

func bigQueryArrayNeedsPrettyLayout(expression *FunctionCallExpr) bool {
	if len(expression.Args) >= 7 {
		return true
	}
	width := 2
	for _, arg := range expression.Args {
		compact, err := (generator{canonical: true, dialect: DialectBigQuery}).expr(arg, 0)
		if err != nil {
			return false
		}
		width += len(compact) + 2
	}
	return width > 80
}

func (g generator) bigQueryArrayLiteral(expression *FunctionCallExpr) (string, error) {
	var b strings.Builder
	b.WriteString("[\n")
	for i, arg := range expression.Args {
		if i > 0 {
			b.WriteString(",\n")
		}
		b.WriteString(indentString(g.indent + 1))
		if function, ok := arg.(*FunctionCallExpr); ok && function.RawArgs == "" && len(function.Args) >= 5 {
			b.WriteString(generateFunctionName(function.Name))
			b.WriteString("(\n")
			for j, functionArg := range function.Args {
				if j > 0 {
					b.WriteString(",\n")
				}
				b.WriteString(indentString(g.indent + 2))
				argText, err := g.expr(functionArg, 0)
				if err != nil {
					return "", err
				}
				b.WriteString(argText)
			}
			b.WriteByte('\n')
			b.WriteString(indentString(g.indent + 1))
			b.WriteByte(')')
			continue
		}
		argText, err := g.expr(arg, 0)
		if err != nil {
			return "", err
		}
		b.WriteString(argText)
	}
	b.WriteByte('\n')
	b.WriteString(indentString(g.indent))
	b.WriteByte(']')
	return b.String(), nil
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
			if item.NullsLast {
				b.WriteString(" NULLS LAST")
			} else if item.NullsFirst {
				b.WriteString(" NULLS FIRST")
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

func generateFunctionName(identifiers []Identifier) string {
	if len(identifiers) == 0 {
		return ""
	}
	parts := make([]string, len(identifiers))
	for i, identifier := range identifiers {
		parts[i] = generateIdentifier(identifier)
	}
	return strings.Join(parts, ".")
}

func normalizeIntervalLiteral(literal *LiteralExpr, qualifiers []Expr) (string, []Expr) {
	value := literal.Raw
	if literal.KindValue == LiteralNumber {
		value = "'" + value + "'"
		return value, qualifiers
	}
	if literal.KindValue != LiteralString || len(value) < 2 || value[0] != '\'' || value[len(value)-1] != '\'' {
		return value, qualifiers
	}
	content := value[1 : len(value)-1]
	fields := strings.Fields(content)
	if len(fields) < 2 {
		return value, qualifiers
	}
	unit := fields[len(fields)-1]
	if !isIntervalUnit(unit) {
		return value, qualifiers
	}
	amount := strings.TrimSpace(content[:len(content)-len(unit)])
	if amount == "" {
		return value, qualifiers
	}
	return "'" + amount + "'", append(qualifiers, &IdentifierExpr{Parts: []Identifier{{Text: strings.ToUpper(unit)}}})
}

func isIntervalUnit(value string) bool {
	switch strings.ToUpper(value) {
	case "MICROSECOND", "MICROSECONDS", "MILLISECOND", "MILLISECONDS", "SECOND", "SECONDS", "MINUTE", "MINUTES", "HOUR", "HOURS", "DAY", "DAYS", "WEEK", "WEEKS", "MONTH", "MONTHS", "QUARTER", "QUARTERS", "YEAR", "YEARS":
		return true
	default:
		return false
	}
}

func canonicalTypeName(typeName string, dialect Dialect) string {
	upper := strings.ToUpper(typeName)
	if dialect == DialectGeneric {
		switch upper {
		case "INTEGER":
			return "INT"
		case "NUMERIC":
			return "DECIMAL"
		case "STRING":
			return "TEXT"
		}
	}
	if dialect == DialectDremio {
		switch upper {
		case "DATE", "TIME", "TIMESTAMP", "TIMESTAMPTZ", "INT", "INTEGER", "BIGINT", "BOOLEAN", "VARCHAR":
			return upper
		}
	}
	return typeName
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

func canonicalTimestampTypeForDialect(typeName string, qualifiers []string, dialect Dialect) string {
	if len(qualifiers) > 0 && strings.EqualFold(qualifiers[0], "WITH") && (dialect == DialectAthena || dialect == DialectPresto || dialect == DialectTrino) {
		return "TIMESTAMP WITH TIME ZONE"
	}
	return canonicalTimestampType(typeName, qualifiers)
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
		case "=", "<>", "!=", "<", "<=", ">", ">=", "~", "~*", "!~", "!~*", "~~", "~~~", "!~~", "!~~*", "@>", "<@", "&&", "^@", "<=>", "LIKE", "LIKE ANY", "ILIKE", "ILIKE ANY", "GLOB", "IN", "BETWEEN", "IS", "IS NOT", "OVERLAPS":
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
		case "*", "/", "%", "**":
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
	switch identifier.Quote {
	case '`':
		return "`" + strings.ReplaceAll(identifier.Text, "`", "``") + "`"
	case '[':
		return "[" + strings.ReplaceAll(identifier.Text, "]", "]]") + "]"
	default:
		return `"` + strings.ReplaceAll(identifier.Text, `"`, `""`) + `"`
	}
}

func canonicalRawSQL(raw string) string {
	text := strings.Join(strings.Fields(raw), " ")
	text = strings.ReplaceAll(text, "( ", "(")
	text = strings.ReplaceAll(text, " ,", ",")
	text = strings.ReplaceAll(text, " )", ")")
	return text
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
