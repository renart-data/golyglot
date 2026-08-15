package golyglot

import "strings"

// TranspileOptions controls the presentation of transpiled statements. The
// AST is always regenerated canonically so dialect rewrites are visible;
// Pretty only changes layout.
type TranspileOptions struct {
	Pretty bool
}

// Transpile parses SQL in fromDialect, applies the dialect rewrites currently
// implemented by the Go core, and generates one statement per input
// statement in toDialect.
//
// Unsupported syntax is retained by the parser as a raw statement or raw
// expression where possible. It is not guessed at or rewritten silently.
func Transpile(sql string, fromDialect, toDialect Dialect) ([]string, error) {
	return TranspileWithOptions(sql, fromDialect, toDialect, TranspileOptions{})
}

// TranspileWithOptions is the configurable form of Transpile.
func TranspileWithOptions(sql string, fromDialect, toDialect Dialect, options TranspileOptions) ([]string, error) {
	fromDialect, err := fromDialect.normalized()
	if err != nil {
		return nil, err
	}
	toDialect, err = toDialect.normalized()
	if err != nil {
		return nil, err
	}
	result, err := ParseStrict(sql, fromDialect)
	if err != nil {
		return nil, err
	}
	for i := range result.Statements {
		transformNode(result.Statements[i].Node, toDialect)
	}

	generated := make([]string, 0, len(result.Statements))
	for _, statement := range result.Statements {
		text, err := GenerateWithOptions(statement.Node, GenerateOptions{
			Pretty:    options.Pretty,
			Canonical: true,
			Dialect:   toDialect,
		})
		if err != nil {
			return nil, err
		}
		generated = append(generated, text)
	}
	return generated, nil
}

// TranspileOne is the single-statement convenience form of Transpile.
func TranspileOne(sql string, fromDialect, toDialect Dialect) (string, error) {
	statements, err := Transpile(sql, fromDialect, toDialect)
	if err != nil {
		return "", err
	}
	if len(statements) != 1 {
		return "", &SyntaxError{Diagnostic: Diagnostic{
			Severity: SeverityError,
			Code:     "TRANSPILE_EXPECTED_ONE_STATEMENT",
			Message:  "expected exactly one SQL statement",
			Span:     Span{Start: 0, End: 0},
		}}
	}
	return statements[0], nil
}

// Format parses and pretty-prints each statement using the requested dialect.
// It is the Go equivalent of Polyglot's formatting entry point for the
// currently supported AST surface.
func Format(sql string, dialect Dialect) ([]string, error) {
	dialect, err := dialect.normalized()
	if err != nil {
		return nil, err
	}
	result, err := ParseStrict(sql, dialect)
	if err != nil {
		return nil, err
	}
	formatted := make([]string, 0, len(result.Statements))
	for _, statement := range result.Statements {
		text, err := GenerateWithOptions(statement.Node, GenerateOptions{Pretty: true, Canonical: true, Dialect: dialect})
		if err != nil {
			return nil, err
		}
		formatted = append(formatted, text)
	}
	return formatted, nil
}

// FormatOne is the single-statement convenience form of Format.
func FormatOne(sql string, dialect Dialect) (string, error) {
	statements, err := Format(sql, dialect)
	if err != nil {
		return "", err
	}
	if len(statements) != 1 {
		return "", &SyntaxError{Diagnostic: Diagnostic{
			Severity: SeverityError,
			Code:     "FORMAT_EXPECTED_ONE_STATEMENT",
			Message:  "expected exactly one SQL statement",
			Span:     Span{Start: 0, End: 0},
		}}
	}
	return statements[0], nil
}

func transformNode(node Node, target Dialect) {
	switch node := node.(type) {
	case *SelectStmt:
		transformSelect(node, target)
	case *ExpressionStmt:
		node.Expr = transformExpr(node.Expr, target)
	case *InsertStmt:
		for i := range node.Values {
			for j := range node.Values[i] {
				node.Values[i][j] = transformExpr(node.Values[i][j], target)
			}
		}
		if node.Query != nil {
			transformSelect(node.Query, target)
		}
	case *UpdateStmt:
		for i := range node.Assignments {
			node.Assignments[i].Value = transformExpr(node.Assignments[i].Value, target)
		}
		node.Where = transformExpr(node.Where, target)
	case *DeleteStmt:
		node.Where = transformExpr(node.Where, target)
	case *CreateTableStmt, *CommandStmt, *RawStmt, *UnknownStmt:
		// These nodes either already retain their source form or have no
		// expression children in the current typed surface.
	}
}

func transformSelect(stmt *SelectStmt, target Dialect) {
	if stmt.Top != nil && target != DialectTSQL {
		if stmt.Limit == nil {
			stmt.Limit = transformExpr(stmt.Top, target)
		}
		stmt.Top = nil
	}
	for i := range stmt.With {
		if stmt.With[i].Query != nil {
			transformSelect(stmt.With[i].Query, target)
		}
	}
	for i := range stmt.Projections {
		stmt.Projections[i].Expr = transformExpr(stmt.Projections[i].Expr, target)
		for j := range stmt.Projections[i].Except {
			stmt.Projections[i].Except[j] = transformExpr(stmt.Projections[i].Except[j], target)
		}
		for j := range stmt.Projections[i].Replace {
			stmt.Projections[i].Replace[j].Expr = transformExpr(stmt.Projections[i].Replace[j].Expr, target)
		}
	}
	for i := range stmt.From {
		transformTableExpr(&stmt.From[i], target)
	}
	stmt.Where = transformExpr(stmt.Where, target)
	for i := range stmt.GroupBy {
		stmt.GroupBy[i] = transformExpr(stmt.GroupBy[i], target)
	}
	stmt.Having = transformExpr(stmt.Having, target)
	stmt.Qualify = transformExpr(stmt.Qualify, target)
	stmt.ConnectBy = transformExpr(stmt.ConnectBy, target)
	for i := range stmt.Windows {
		transformWindow(&stmt.Windows[i].Spec, target)
	}
	for i := range stmt.SortBy {
		transformOrderItem(&stmt.SortBy[i], target)
	}
	for i := range stmt.OrderBy {
		transformOrderItem(&stmt.OrderBy[i], target)
	}
	stmt.Limit = transformExpr(stmt.Limit, target)
	stmt.Offset = transformExpr(stmt.Offset, target)
	if stmt.Fetch != nil {
		stmt.Fetch.Count = transformExpr(stmt.Fetch.Count, target)
	}
	if stmt.SetLeft != nil {
		transformSelect(stmt.SetLeft, target)
	}
	if stmt.SetRight != nil {
		transformSelect(stmt.SetRight, target)
	}
}

func transformTableExpr(table *TableExpr, target Dialect) {
	switch item := table.Primary.(type) {
	case *SubqueryFrom:
		if item.Query != nil {
			transformSelect(item.Query, target)
		}
	case *GroupedFrom:
		for i := range item.Items {
			transformTableExpr(&item.Items[i], target)
		}
	case *TableFunctionFrom:
		for i := range item.Args {
			item.Args[i] = transformExpr(item.Args[i], target)
		}
	}
	for i := range table.Joins {
		if table.Joins[i].Right != nil {
			transformFromItem(table.Joins[i].Right, target)
		}
		table.Joins[i].Condition = transformExpr(table.Joins[i].Condition, target)
	}
	for i := range table.LateralViews {
		table.LateralViews[i].Expression = transformExpr(table.LateralViews[i].Expression, target)
	}
}

func transformFromItem(item FromItem, target Dialect) {
	switch item := item.(type) {
	case *SubqueryFrom:
		if item.Query != nil {
			transformSelect(item.Query, target)
		}
	case *GroupedFrom:
		for i := range item.Items {
			transformTableExpr(&item.Items[i], target)
		}
	case *TableFunctionFrom:
		for i := range item.Args {
			item.Args[i] = transformExpr(item.Args[i], target)
		}
	}
}

func transformOrderItem(item *OrderItem, target Dialect) {
	item.Expr = transformExpr(item.Expr, target)
}

func transformWindow(window *WindowSpec, target Dialect) {
	for i := range window.PartitionBy {
		window.PartitionBy[i] = transformExpr(window.PartitionBy[i], target)
	}
	for i := range window.OrderBy {
		transformOrderItem(&window.OrderBy[i], target)
	}
}

func transformExpr(expression Expr, target Dialect) Expr {
	if expression == nil {
		return nil
	}
	switch expression := expression.(type) {
	case *UnaryExpr:
		expression.Expr = transformExpr(expression.Expr, target)
	case *BinaryExpr:
		expression.Left = transformExpr(expression.Left, target)
		expression.Right = transformExpr(expression.Right, target)
		expression.Escape = transformExpr(expression.Escape, target)
	case *InExpr:
		expression.Value = transformExpr(expression.Value, target)
		for i := range expression.Items {
			expression.Items[i] = transformExpr(expression.Items[i], target)
		}
		if expression.Query != nil {
			transformSelect(expression.Query, target)
		}
	case *BetweenExpr:
		expression.Value = transformExpr(expression.Value, target)
		expression.Low = transformExpr(expression.Low, target)
		expression.High = transformExpr(expression.High, target)
	case *IsExpr:
		expression.Value = transformExpr(expression.Value, target)
		expression.Right = transformExpr(expression.Right, target)
	case *FunctionCallExpr:
		for i := range expression.Args {
			expression.Args[i] = transformExpr(expression.Args[i], target)
		}
		for i := range expression.OrderBy {
			transformOrderItem(&expression.OrderBy[i], target)
		}
		for i := range expression.WithinGroup {
			transformOrderItem(&expression.WithinGroup[i], target)
		}
		expression.Filter = transformExpr(expression.Filter, target)
		if expression.Over != nil {
			transformWindow(expression.Over, target)
		}
		rewriteFunction(expression, target)
	case *CallExpr:
		expression.Callee = transformExpr(expression.Callee, target)
		for i := range expression.Args {
			expression.Args[i] = transformExpr(expression.Args[i], target)
		}
	case *GenericExpr:
		expression.Target = transformExpr(expression.Target, target)
		for i := range expression.Arguments {
			expression.Arguments[i] = transformExpr(expression.Arguments[i], target)
		}
	case *ExtractExpr:
		expression.Field = transformExpr(expression.Field, target)
		expression.Source = transformExpr(expression.Source, target)
	case *TupleExpr:
		for i := range expression.Items {
			expression.Items[i] = transformExpr(expression.Items[i], target)
		}
	case *AliasExpr:
		expression.Expr = transformExpr(expression.Expr, target)
	case *IntervalExpr:
		expression.Value = transformExpr(expression.Value, target)
		for i := range expression.Qualifiers {
			expression.Qualifiers[i] = transformExpr(expression.Qualifiers[i], target)
		}
	case *CastExpr:
		expression.Value = transformExpr(expression.Value, target)
		expression.Type = transformExpr(expression.Type, target)
	case *WindowedExpr:
		expression.Expr = transformExpr(expression.Expr, target)
		transformWindow(&expression.Over, target)
	case *ExistsExpr:
		if expression.Query != nil {
			transformSelect(expression.Query, target)
		}
	case *QuantifiedExpr:
		if expression.Query != nil {
			transformSelect(expression.Query, target)
		}
	case *GroupingExpr:
		for i := range expression.Args {
			expression.Args[i] = transformExpr(expression.Args[i], target)
		}
	case *SetExpr:
		expression.Left = transformExpr(expression.Left, target)
		expression.Right = transformExpr(expression.Right, target)
	case *SubqueryExpr:
		if expression.Query != nil {
			transformSelect(expression.Query, target)
		}
	case *TypedLiteralExpr:
		for i := range expression.Parameters {
			expression.Parameters[i] = transformExpr(expression.Parameters[i], target)
		}
	case *CaseExpr:
		expression.Operand = transformExpr(expression.Operand, target)
		for i := range expression.Whens {
			expression.Whens[i].Condition = transformExpr(expression.Whens[i].Condition, target)
			expression.Whens[i].Result = transformExpr(expression.Whens[i].Result, target)
		}
		expression.Else = transformExpr(expression.Else, target)
	case *IndexExpr:
		expression.Target = transformExpr(expression.Target, target)
		expression.Low = transformExpr(expression.Low, target)
		expression.High = transformExpr(expression.High, target)
		for i := range expression.Indices {
			expression.Indices[i] = transformExpr(expression.Indices[i], target)
		}
	case *FieldExpr:
		expression.Target = transformExpr(expression.Target, target)
	case *ParenthesizedExpr:
		expression.Expr = transformExpr(expression.Expr, target)
	}
	return expression
}

func rewriteFunction(function *FunctionCallExpr, target Dialect) {
	if len(function.Name) != 1 || function.RawArgs != "" {
		return
	}
	name := strings.ToUpper(function.Name[0].Text)
	if usesCoalesce(target) && (name == "NVL" || name == "IFNULL") {
		setFunctionName(function, "COALESCE")
		return
	}
	if usesIfNull(target) && name == "NVL" {
		setFunctionName(function, "IFNULL")
		return
	}
	if target == DialectPostgreSQL {
		switch name {
		case "GROUP_CONCAT":
			if len(function.Args) == 1 {
				setFunctionName(function, "STRING_AGG")
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralString, Raw: "','"})
			}
		case "SUBSTR":
			if len(function.Args) == 3 {
				generator := generator{canonical: true, dialect: target}
				first, firstErr := generator.expr(function.Args[0], 0)
				start, startErr := generator.expr(function.Args[1], 0)
				length, lengthErr := generator.expr(function.Args[2], 0)
				if firstErr == nil && startErr == nil && lengthErr == nil {
					setFunctionName(function, "SUBSTRING")
					function.RawArgs = "(" + first + " FROM " + start + " FOR " + length + ")"
				}
			}
		}
	}
	if usesMySQLArrayFunctions(target) && name == "ARRAY_AGG" {
		setFunctionName(function, "GROUP_CONCAT")
	}
}

func setFunctionName(function *FunctionCallExpr, name string) {
	function.Name[0].Text = name
	function.Name[0].Quoted = false
}

func usesCoalesce(dialect Dialect) bool {
	switch dialect {
	case DialectPostgreSQL, DialectDuckDB, DialectSQLite, DialectTSQL,
		DialectPresto, DialectTrino, DialectAthena, DialectMaterialize,
		DialectRisingWave, DialectRedshift, DialectCockroachDB:
		return true
	default:
		return false
	}
}

func usesIfNull(dialect Dialect) bool {
	switch dialect {
	case DialectMySQL, DialectDoris, DialectStarRocks, DialectTiDB:
		return true
	default:
		return false
	}
}

func usesMySQLArrayFunctions(dialect Dialect) bool {
	return usesIfNull(dialect)
}
