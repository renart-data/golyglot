package golyglot

import (
	"fmt"
	"strconv"
	"strings"
)

// Semantic correctness is checked per bound query block, never by flattening
// descendants. Syntax-only callers do not run this pass.
func (c *validationCollector) validateQueryRules(scope *queryScope) {
	q := scope.query
	aggregateQuery := len(q.GroupBy) > 0 || q.Having != nil
	groups := make([]Expr, 0, len(q.GroupBy))
	for _, group := range q.GroupBy {
		groups = append(groups, scope.clauseExpression(group, true))
	}
	var placement func(Node, bool, bool, int, bool)
	visitingAliases := make(map[*SelectItem]bool)
	placement = func(node Node, allowAggregate, allowWindow bool, aggregateDepth int, inWindow bool) {
		if node == nil {
			return
		}
		switch value := node.(type) {
		case *IdentifierExpr:
			if len(value.Parts) == 1 {
				ref := ColumnReference{Column: value.Parts[0].Text, Span: value.SourceSpan()}
				if projection := scope.aliasForReference(ref, true, false); projection != nil && !visitingAliases[projection] {
					visitingAliases[projection] = true
					placement(projection.Expr, allowAggregate, allowWindow, aggregateDepth, inWindow)
					delete(visitingAliases, projection)
				}
			}
			return
		case *SelectStmt:
			return
		case *CastExpr:
			placement(value.Value, allowAggregate, allowWindow, aggregateDepth, inWindow)
			return
		case *IntervalExpr:
			placement(value.Value, allowAggregate, allowWindow, aggregateDepth, inWindow)
			return
		case *WindowedExpr:
			if fn, ok := value.Expr.(*FunctionCallExpr); ok {
				copy := *fn
				copy.Over = &value.Over
				placement(&copy, allowAggregate, allowWindow, aggregateDepth, inWindow)
				return
			}
		case *FunctionCallExpr:
			window := value.Over != nil
			aggregate := c.aggregateFunction(value) && !window
			if window && (!allowWindow || inWindow || aggregateDepth > 0) {
				c.add(SeverityError, "E232", "window function is not allowed in this clause or nested expression", value.SourceSpan())
			}
			if aggregate {
				aggregateQuery = true
				if !allowAggregate || aggregateDepth > 0 {
					c.add(SeverityError, "E231", "aggregate function is not allowed in this clause or inside another aggregate", value.SourceSpan())
				}
				aggregateDepth++
			}
			definition := c.catalog.lookup(identifiersText(value.Name))
			if !window && (windowOnlyFunction(identifiersText(value.Name)) || definition != nil && definition.kind == FunctionWindow) {
				c.add(SeverityError, "E232", "window function requires an OVER clause", value.SourceSpan())
			}
			for _, argument := range value.Args {
				placement(argument, allowAggregate, allowWindow, aggregateDepth, inWindow || window)
			}
			placement(value.Filter, false, false, aggregateDepth, inWindow || window)
			placement(value.Having, false, false, aggregateDepth, inWindow || window)
			for _, order := range append(append([]OrderItem(nil), value.OrderBy...), value.WithinGroup...) {
				placement(order.Expr, allowAggregate, false, aggregateDepth, inWindow || window)
			}
			if value.Over != nil {
				var children []Node
				appendWindowChildren(&children, value.Over)
				for _, child := range children {
					placement(child, true, false, 0, true)
				}
			}
			return
		}
		for _, child := range nodeChildren(node) {
			placement(child, allowAggregate, allowWindow, aggregateDepth, inWindow)
		}
	}
	for _, projection := range q.Projections {
		placement(projection.Expr, true, true, 0, false)
	}
	placement(q.Where, false, false, 0, false)
	placement(q.Limit, false, false, 0, false)
	placement(q.Offset, false, false, 0, false)
	for _, group := range groups {
		if !isGroupByAll(group) {
			placement(group, false, false, 0, false)
		}
	}
	placement(q.Having, true, false, 0, false)
	placement(q.Qualify, true, true, 0, false)
	for _, order := range q.OrderBy {
		placement(order.Expr, true, true, 0, false)
	}
	for _, window := range q.Windows {
		var children []Node
		appendWindowChildren(&children, &window.Spec)
		for _, child := range children {
			placement(child, true, false, 0, true)
		}
	}
	for _, table := range q.From {
		for _, join := range table.Joins {
			placement(join.Condition, false, false, 0, false)
		}
	}
	if !aggregateQuery || c.dialect == DialectSQLite || c.dialect == DialectMySQL {
		return
	}
	for _, group := range groups {
		if isGroupByAll(group) {
			return
		}
	}

	groupSQL := make(map[string]bool)
	groupColumns := make(map[string]bool)
	var addGroup func(Expr)
	addGroup = func(group Expr) {
		if group == nil {
			return
		}
		switch value := group.(type) {
		case *GroupingExpr:
			for _, item := range value.Args {
				addGroup(item)
			}
			return
		case *TupleExpr:
			for _, item := range value.Items {
				addGroup(item)
			}
			return
		case *ParenthesizedExpr:
			addGroup(value.Expr)
			return
		}
		groupSQL[expressionSQL(group, c.dialect)] = true
		if _, ok := group.(*IdentifierExpr); ok {
			for _, ref := range scope.references(group, true) {
				groupColumns[groupReferenceKey(ref)] = true
			}
		}
	}
	for _, group := range groups {
		addGroup(group)
	}
	functional := make(map[*scopeRelation]bool)
	if c.dialect == DialectPostgreSQL && c.schema != nil {
		for _, relation := range scope.relations {
			if relation.kind != "table" {
				continue
			}
			table, ok := findSchemaTable(*c.schema, relation.name)
			if !ok {
				continue
			}
			keys := append([]string(nil), table.PrimaryKey...)
			for _, column := range table.Columns {
				if column.PrimaryKey {
					keys = append(keys, column.Name)
				}
			}
			covered := len(keys) > 0
			for _, key := range keys {
				covered = covered && groupColumns[groupReferenceKey(boundReference{relation: relation, reference: ColumnReference{Column: key}})]
			}
			functional[relation] = covered
		}
	}
	var checkGrouped func(Node, map[string]bool, map[*SelectItem]bool)
	checkGrouped = func(node Node, locals map[string]bool, aliases map[*SelectItem]bool) {
		if node == nil {
			return
		}
		if expression, ok := node.(Expr); ok && groupSQL[expressionSQL(expression, c.dialect)] {
			return
		}
		switch value := node.(type) {
		case *SelectStmt:
			return
		case *CastExpr:
			checkGrouped(value.Value, locals, aliases)
			return
		case *IntervalExpr:
			checkGrouped(value.Value, locals, aliases)
			return
		case *RawExpr:
			c.add(SeverityWarning, "W002", "grouping could not be checked for this expression", value.SourceSpan())
			return
		case *StarExpr:
			c.add(SeverityWarning, "W002", "grouping of an unexpanded wildcard could not be checked", value.SourceSpan())
			return
		case *IdentifierExpr:
			if len(value.Parts) == 0 || locals[identifierKey(value.Parts[0], c.dialect)] {
				return
			}
			if len(value.Parts) == 1 {
				raw := ColumnReference{Column: value.Parts[0].Text, Span: value.SourceSpan()}
				if projection := scope.aliasForReference(raw, true, false); projection != nil && !aliases[projection] {
					aliases[projection] = true
					checkGrouped(projection.Expr, locals, aliases)
					delete(aliases, projection)
					return
				}
			}
			refs := scope.references(value, false)
			if len(refs) == 0 {
				return
			}
			ref := refs[0]
			if projection := scope.aliasForReference(ref.reference, true, false); projection != nil && !aliases[projection] {
				aliases[projection] = true
				checkGrouped(projection.Expr, locals, aliases)
				delete(aliases, projection)
				return
			}
			if !groupColumns[groupReferenceKey(ref)] && !functional[ref.relation] {
				c.add(SeverityError, "E230", fmt.Sprintf("column %q must appear in GROUP BY or an aggregate", ref.reference.Column), value.SourceSpan())
			}
			return
		case *FunctionCallExpr:
			if value.Over == nil && c.aggregateFunction(value) {
				return
			}
			lambdas := make(map[Node]bool)
			for index, argument := range value.Args {
				parameters, body, ok := lambdaParts(argument)
				if !ok || !lambdaArgument(value, index) {
					continue
				}
				child := make(map[string]bool)
				for key := range locals {
					child[key] = true
				}
				for _, parameter := range parameters {
					child[identifierKey(parameter, c.dialect)] = true
				}
				checkGrouped(body, child, aliases)
				lambdas[argument] = true
			}
			for _, child := range nodeChildren(value) {
				if !lambdas[child] {
					checkGrouped(child, locals, aliases)
				}
			}
			return
		}
		for _, child := range nodeChildren(node) {
			checkGrouped(child, locals, aliases)
		}
	}
	for _, projection := range q.Projections {
		checkGrouped(projection.Expr, nil, make(map[*SelectItem]bool))
	}
	checkGrouped(q.Having, nil, make(map[*SelectItem]bool))
	checkGrouped(q.Qualify, nil, make(map[*SelectItem]bool))
	if scope.compoundOrder == nil {
		for _, order := range q.OrderBy {
			checkGrouped(scope.clauseExpression(order.Expr, false), nil, make(map[*SelectItem]bool))
		}
	}
	for _, window := range q.Windows {
		var children []Node
		appendWindowChildren(&children, &window.Spec)
		for _, child := range children {
			checkGrouped(child, nil, make(map[*SelectItem]bool))
		}
	}
}

func (scope *queryScope) clauseExpression(expression Expr, group bool) Expr {
	if literal, ok := expression.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
		if index, err := strconv.Atoi(literal.Raw); err == nil && index > 0 && index <= len(scope.query.Projections) {
			return scope.query.Projections[index-1].Expr
		}
	}
	if id, ok := expression.(*IdentifierExpr); ok && len(id.Parts) == 1 {
		ref := ColumnReference{Column: id.Parts[0].Text, Span: id.SourceSpan()}
		if projection := scope.aliasForReference(ref, true, !group); projection != nil {
			return projection.Expr
		}
	}
	return expression
}

func groupReferenceKey(reference boundReference) string {
	if reference.relation != nil {
		return fmt.Sprintf("%p:%s", reference.relation, strings.ToLower(reference.reference.Column))
	}
	return strings.ToLower(reference.reference.Table + "." + reference.reference.Column)
}

func windowOnlyFunction(name string) bool {
	switch strings.ToUpper(name) {
	case "ROW_NUMBER", "RANK", "DENSE_RANK", "PERCENT_RANK", "CUME_DIST", "NTILE", "LAG", "LEAD", "NTH_VALUE":
		return true
	}
	return false
}
