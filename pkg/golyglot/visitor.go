package golyglot

// VisitAction controls whether Walk descends into a node after visiting it.
type VisitAction uint8

const (
	VisitChildren VisitAction = iota
	SkipChildren
	Stop
)

// Visitor is called in pre-order for every reachable AST node.
type Visitor func(Node) VisitAction

// Walk visits root and all of its descendants in source order. Returning
// SkipChildren prunes a subtree; returning Stop ends the walk immediately.
func Walk(root Node, visitor Visitor) {
	if root == nil || visitor == nil {
		return
	}
	stopped := false
	var visit func(Node)
	visit = func(node Node) {
		if stopped || node == nil {
			return
		}
		switch visitor(node) {
		case Stop:
			stopped = true
		case SkipChildren:
			return
		default:
			for _, child := range nodeChildren(node) {
				visit(child)
				if stopped {
					return
				}
			}
		}
	}
	visit(root)
}

// WalkResult visits a parsed document without requiring callers to unpack its
// statements first.
func WalkResult(result ParseResult, visitor Visitor) {
	for _, statement := range result.Statements {
		Walk(statement.Node, visitor)
	}
}

// FindAll returns nodes matching predicate in pre-order.
func FindAll(root Node, predicate func(Node) bool) []Node {
	if predicate == nil {
		return nil
	}
	var result []Node
	Walk(root, func(node Node) VisitAction {
		if predicate(node) {
			result = append(result, node)
		}
		return VisitChildren
	})
	return result
}

// Transform applies transform in pre-order and recursively rewires the
// returned node's children. It is deliberately pointer-based: callers can
// mutate an existing AST in place or return a replacement node from the
// callback.
func Transform(root Node, transform func(Node) Node) Node {
	if root == nil || transform == nil {
		return root
	}
	var visit func(Node) Node
	visit = func(node Node) Node {
		if node == nil {
			return nil
		}
		node = transform(node)
		if node == nil {
			return nil
		}
		switch value := node.(type) {
		case *SelectStmt:
			for i := range value.With {
				if value.With[i].Query != nil {
					value.With[i].Query, _ = visit(value.With[i].Query).(*SelectStmt)
				}
			}
			for i := range value.Projections {
				value.Projections[i].Expr = transformExprChild(value.Projections[i].Expr, visit)
				for j := range value.Projections[i].Except {
					value.Projections[i].Except[j] = transformExprChild(value.Projections[i].Except[j], visit)
				}
				for j := range value.Projections[i].Replace {
					value.Projections[i].Replace[j].Expr = transformExprChild(value.Projections[i].Replace[j].Expr, visit)
				}
			}
			for i := range value.From {
				transformTableChild(&value.From[i], visit)
			}
			value.Where = transformExprChild(value.Where, visit)
			for i := range value.GroupBy {
				value.GroupBy[i] = transformExprChild(value.GroupBy[i], visit)
			}
			value.Having = transformExprChild(value.Having, visit)
			value.Qualify = transformExprChild(value.Qualify, visit)
			value.ConnectBy = transformExprChild(value.ConnectBy, visit)
			for i := range value.Windows {
				transformWindowChild(&value.Windows[i].Spec, visit)
			}
			for i := range value.SortBy {
				value.SortBy[i].Expr = transformExprChild(value.SortBy[i].Expr, visit)
			}
			for i := range value.OrderBy {
				value.OrderBy[i].Expr = transformExprChild(value.OrderBy[i].Expr, visit)
			}
			value.Limit = transformExprChild(value.Limit, visit)
			value.Offset = transformExprChild(value.Offset, visit)
			if value.Fetch != nil {
				value.Fetch.Count = transformExprChild(value.Fetch.Count, visit)
			}
			if value.SetLeft != nil {
				value.SetLeft, _ = visit(value.SetLeft).(*SelectStmt)
			}
			if value.SetRight != nil {
				value.SetRight, _ = visit(value.SetRight).(*SelectStmt)
			}
		case *ExpressionStmt:
			value.Expr = transformExprChild(value.Expr, visit)
		case *InsertStmt:
			for i := range value.Values {
				for j := range value.Values[i] {
					value.Values[i][j] = transformExprChild(value.Values[i][j], visit)
				}
			}
			if value.Query != nil {
				value.Query, _ = visit(value.Query).(*SelectStmt)
			}
		case *UpdateStmt:
			for i := range value.Assignments {
				value.Assignments[i].Value = transformExprChild(value.Assignments[i].Value, visit)
			}
			value.Where = transformExprChild(value.Where, visit)
		case *DeleteStmt:
			value.Where = transformExprChild(value.Where, visit)
		case *TableExpr:
			transformTableChild(value, visit)
		case *SubqueryFrom:
			if value.Query != nil {
				value.Query, _ = visit(value.Query).(*SelectStmt)
			}
		case *GroupedFrom:
			for i := range value.Items {
				transformTableChild(&value.Items[i], visit)
			}
		case *TableFunctionFrom:
			for i := range value.Args {
				value.Args[i] = transformExprChild(value.Args[i], visit)
			}
		case *UnaryExpr:
			value.Expr = transformExprChild(value.Expr, visit)
		case *BinaryExpr:
			value.Left = transformExprChild(value.Left, visit)
			value.Right = transformExprChild(value.Right, visit)
			value.Escape = transformExprChild(value.Escape, visit)
		case *InExpr:
			value.Value = transformExprChild(value.Value, visit)
			for i := range value.Items {
				value.Items[i] = transformExprChild(value.Items[i], visit)
			}
			if value.Query != nil {
				value.Query, _ = visit(value.Query).(*SelectStmt)
			}
		case *BetweenExpr:
			value.Value = transformExprChild(value.Value, visit)
			value.Low = transformExprChild(value.Low, visit)
			value.High = transformExprChild(value.High, visit)
		case *IsExpr:
			value.Value = transformExprChild(value.Value, visit)
			value.Right = transformExprChild(value.Right, visit)
		case *FunctionCallExpr:
			for i := range value.Args {
				value.Args[i] = transformExprChild(value.Args[i], visit)
			}
			for i := range value.OrderBy {
				value.OrderBy[i].Expr = transformExprChild(value.OrderBy[i].Expr, visit)
			}
			for i := range value.WithinGroup {
				value.WithinGroup[i].Expr = transformExprChild(value.WithinGroup[i].Expr, visit)
			}
			value.Filter = transformExprChild(value.Filter, visit)
			if value.Over != nil {
				transformWindowChild(value.Over, visit)
			}
		case *CallExpr:
			value.Callee = transformExprChild(value.Callee, visit)
			for i := range value.Args {
				value.Args[i] = transformExprChild(value.Args[i], visit)
			}
		case *GenericExpr:
			value.Target = transformExprChild(value.Target, visit)
			for i := range value.Arguments {
				value.Arguments[i] = transformExprChild(value.Arguments[i], visit)
			}
		case *ExtractExpr:
			value.Field = transformExprChild(value.Field, visit)
			value.Source = transformExprChild(value.Source, visit)
		case *TupleExpr:
			for i := range value.Items {
				value.Items[i] = transformExprChild(value.Items[i], visit)
			}
		case *AliasExpr:
			value.Expr = transformExprChild(value.Expr, visit)
		case *IntervalExpr:
			value.Value = transformExprChild(value.Value, visit)
			for i := range value.Qualifiers {
				value.Qualifiers[i] = transformExprChild(value.Qualifiers[i], visit)
			}
		case *CastExpr:
			value.Value = transformExprChild(value.Value, visit)
			value.Type = transformExprChild(value.Type, visit)
		case *WindowedExpr:
			value.Expr = transformExprChild(value.Expr, visit)
			transformWindowChild(&value.Over, visit)
		case *ExistsExpr:
			if value.Query != nil {
				value.Query, _ = visit(value.Query).(*SelectStmt)
			}
		case *QuantifiedExpr:
			if value.Query != nil {
				value.Query, _ = visit(value.Query).(*SelectStmt)
			}
		case *GroupingExpr:
			for i := range value.Args {
				value.Args[i] = transformExprChild(value.Args[i], visit)
			}
		case *SetExpr:
			value.Left = transformExprChild(value.Left, visit)
			value.Right = transformExprChild(value.Right, visit)
		case *SubqueryExpr:
			if value.Query != nil {
				value.Query, _ = visit(value.Query).(*SelectStmt)
			}
		case *TypedLiteralExpr:
			for i := range value.Parameters {
				value.Parameters[i] = transformExprChild(value.Parameters[i], visit)
			}
		case *CaseExpr:
			value.Operand = transformExprChild(value.Operand, visit)
			for i := range value.Whens {
				value.Whens[i].Condition = transformExprChild(value.Whens[i].Condition, visit)
				value.Whens[i].Result = transformExprChild(value.Whens[i].Result, visit)
			}
			value.Else = transformExprChild(value.Else, visit)
		case *IndexExpr:
			value.Target = transformExprChild(value.Target, visit)
			value.Low = transformExprChild(value.Low, visit)
			value.High = transformExprChild(value.High, visit)
			for i := range value.Indices {
				value.Indices[i] = transformExprChild(value.Indices[i], visit)
			}
		case *FieldExpr:
			value.Target = transformExprChild(value.Target, visit)
		case *ParenthesizedExpr:
			value.Expr = transformExprChild(value.Expr, visit)
		}
		return node
	}
	return visit(root)
}

func transformExprChild(expr Expr, visit func(Node) Node) Expr {
	if expr == nil {
		return nil
	}
	result := visit(expr)
	if result == nil {
		return nil
	}
	converted, _ := result.(Expr)
	return converted
}

func transformTableChild(table *TableExpr, visit func(Node) Node) {
	if table == nil {
		return
	}
	if table.Primary != nil {
		primary := visit(table.Primary)
		table.Primary, _ = primary.(FromItem)
	}
	for i := range table.Joins {
		if table.Joins[i].Right != nil {
			right := visit(table.Joins[i].Right)
			table.Joins[i].Right, _ = right.(FromItem)
		}
		table.Joins[i].Condition = transformExprChild(table.Joins[i].Condition, visit)
	}
	for i := range table.LateralViews {
		table.LateralViews[i].Expression = transformExprChild(table.LateralViews[i].Expression, visit)
	}
}

func transformWindowChild(window *WindowSpec, visit func(Node) Node) {
	if window == nil {
		return
	}
	for i := range window.PartitionBy {
		window.PartitionBy[i] = transformExprChild(window.PartitionBy[i], visit)
	}
	for i := range window.OrderBy {
		window.OrderBy[i].Expr = transformExprChild(window.OrderBy[i].Expr, visit)
	}
}

func nodeChildren(node Node) []Node {
	var children []Node
	appendExpr := func(expr Expr) {
		if expr != nil {
			children = append(children, expr)
		}
	}
	appendSelect := func(selectStmt *SelectStmt) {
		if selectStmt != nil {
			children = append(children, selectStmt)
		}
	}
	appendTable := func(table *TableExpr) {
		if table != nil {
			children = append(children, table)
		}
	}
	switch value := node.(type) {
	case *SelectStmt:
		for i := range value.With {
			appendSelect(value.With[i].Query)
		}
		for i := range value.Projections {
			appendExpr(value.Projections[i].Expr)
			for j := range value.Projections[i].Except {
				appendExpr(value.Projections[i].Except[j])
			}
			for j := range value.Projections[i].Replace {
				appendExpr(value.Projections[i].Replace[j].Expr)
			}
		}
		for i := range value.From {
			appendTable(&value.From[i])
		}
		appendExpr(value.Where)
		for i := range value.GroupBy {
			appendExpr(value.GroupBy[i])
		}
		appendExpr(value.Having)
		appendExpr(value.Qualify)
		appendExpr(value.ConnectBy)
		for i := range value.Windows {
			appendWindowChildren(&children, &value.Windows[i].Spec)
		}
		for i := range value.SortBy {
			appendExpr(value.SortBy[i].Expr)
		}
		for i := range value.OrderBy {
			appendExpr(value.OrderBy[i].Expr)
		}
		appendExpr(value.Limit)
		appendExpr(value.Offset)
		if value.Fetch != nil {
			appendExpr(value.Fetch.Count)
		}
		appendSelect(value.SetLeft)
		appendSelect(value.SetRight)
	case *ExpressionStmt:
		appendExpr(value.Expr)
	case *InsertStmt:
		for i := range value.Values {
			for j := range value.Values[i] {
				appendExpr(value.Values[i][j])
			}
		}
		appendSelect(value.Query)
	case *UpdateStmt:
		for i := range value.Assignments {
			appendExpr(value.Assignments[i].Value)
		}
		appendExpr(value.Where)
	case *DeleteStmt:
		appendExpr(value.Where)
	case *TableExpr:
		if value.Primary != nil {
			children = append(children, value.Primary)
		}
		for i := range value.Joins {
			if value.Joins[i].Right != nil {
				children = append(children, value.Joins[i].Right)
			}
			appendExpr(value.Joins[i].Condition)
		}
		for i := range value.LateralViews {
			appendExpr(value.LateralViews[i].Expression)
		}
	case *TableName:
		if value.Sample != nil {
			for i := range value.Sample.Args {
				appendExpr(value.Sample.Args[i])
			}
			appendExpr(value.Sample.On)
		}
	case *SubqueryFrom:
		appendSelect(value.Query)
	case *GroupedFrom:
		for i := range value.Items {
			appendTable(&value.Items[i])
		}
	case *TableFunctionFrom:
		for i := range value.Args {
			appendExpr(value.Args[i])
		}
	case *UnaryExpr:
		appendExpr(value.Expr)
	case *BinaryExpr:
		appendExpr(value.Left)
		appendExpr(value.Right)
		appendExpr(value.Escape)
	case *InExpr:
		appendExpr(value.Value)
		for i := range value.Items {
			appendExpr(value.Items[i])
		}
		appendSelect(value.Query)
	case *BetweenExpr:
		appendExpr(value.Value)
		appendExpr(value.Low)
		appendExpr(value.High)
	case *IsExpr:
		appendExpr(value.Value)
		appendExpr(value.Right)
	case *FunctionCallExpr:
		for i := range value.Args {
			appendExpr(value.Args[i])
		}
		for i := range value.OrderBy {
			appendExpr(value.OrderBy[i].Expr)
		}
		for i := range value.WithinGroup {
			appendExpr(value.WithinGroup[i].Expr)
		}
		appendExpr(value.Filter)
		if value.Over != nil {
			appendWindowChildren(&children, value.Over)
		}
	case *CallExpr:
		appendExpr(value.Callee)
		for i := range value.Args {
			appendExpr(value.Args[i])
		}
	case *GenericExpr:
		appendExpr(value.Target)
		for i := range value.Arguments {
			appendExpr(value.Arguments[i])
		}
	case *ExtractExpr:
		appendExpr(value.Field)
		appendExpr(value.Source)
	case *TupleExpr:
		for i := range value.Items {
			appendExpr(value.Items[i])
		}
	case *AliasExpr:
		appendExpr(value.Expr)
	case *IntervalExpr:
		appendExpr(value.Value)
		for i := range value.Qualifiers {
			appendExpr(value.Qualifiers[i])
		}
	case *CastExpr:
		appendExpr(value.Value)
		appendExpr(value.Type)
	case *WindowedExpr:
		appendExpr(value.Expr)
		appendWindowChildren(&children, &value.Over)
	case *ExistsExpr:
		appendSelect(value.Query)
	case *QuantifiedExpr:
		appendSelect(value.Query)
	case *GroupingExpr:
		for i := range value.Args {
			appendExpr(value.Args[i])
		}
	case *SetExpr:
		appendExpr(value.Left)
		appendExpr(value.Right)
	case *SubqueryExpr:
		appendSelect(value.Query)
	case *TypedLiteralExpr:
		for i := range value.Parameters {
			appendExpr(value.Parameters[i])
		}
	case *CaseExpr:
		appendExpr(value.Operand)
		for i := range value.Whens {
			appendExpr(value.Whens[i].Condition)
			appendExpr(value.Whens[i].Result)
		}
		appendExpr(value.Else)
	case *IndexExpr:
		appendExpr(value.Target)
		appendExpr(value.Low)
		appendExpr(value.High)
		for i := range value.Indices {
			appendExpr(value.Indices[i])
		}
	case *FieldExpr:
		appendExpr(value.Target)
	case *ParenthesizedExpr:
		appendExpr(value.Expr)
	}
	return children
}

func appendWindowChildren(children *[]Node, window *WindowSpec) {
	if window == nil {
		return
	}
	for i := range window.PartitionBy {
		if window.PartitionBy[i] != nil {
			*children = append(*children, window.PartitionBy[i])
		}
	}
	for i := range window.OrderBy {
		if window.OrderBy[i].Expr != nil {
			*children = append(*children, window.OrderBy[i].Expr)
		}
	}
}

// ColumnReference is the normalized name of a column expression found by
// Columns. Table is empty for an unqualified reference.
type ColumnReference struct {
	Table  string
	Column string
	Span   Span
}

// Columns returns column references in source order. Function names and table
// names are not reported as column references.
func Columns(root Node) []ColumnReference {
	var result []ColumnReference
	Walk(root, func(node Node) VisitAction {
		identifier, ok := node.(*IdentifierExpr)
		if !ok || len(identifier.Parts) == 0 {
			return VisitChildren
		}
		column := identifier.Parts[len(identifier.Parts)-1]
		reference := ColumnReference{Column: column.Text, Span: identifier.SourceSpan()}
		if len(identifier.Parts) > 1 {
			reference.Table = identifier.Parts[len(identifier.Parts)-2].Text
		}
		result = append(result, reference)
		return VisitChildren
	})
	return result
}
