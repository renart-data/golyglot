package golyglot

import "strings"

func identifierKey(name Identifier, dialect Dialect) string {
	if dialect == DialectDuckDB || dialect == DialectSQLite || dialect == DialectTSQL {
		return strings.ToLower(name.Text)
	}
	if name.Quoted {
		return name.Text
	}
	if dialect == DialectSnowflake || dialect == DialectOracle {
		return strings.ToUpper(name.Text)
	}
	return strings.ToLower(name.Text)
}

func supportsLateralAliases(dialect Dialect) bool {
	switch dialect {
	case DialectSnowflake, DialectDuckDB, DialectRedshift, DialectDatabricks, DialectSpark:
		return true
	}
	return false
}

// Only arrow expressions in higher-order argument positions bind parameters;
// a JSON arrow must continue to read its left-hand column.
func lambdaArgument(fn *FunctionCallExpr, index int) bool {
	name := strings.ToUpper(identifiersText(fn.Name))
	switch name {
	case "TRANSFORM", "FILTER", "LIST_TRANSFORM", "LIST_APPLY", "ARRAY_APPLY", "ARRAY_TRANSFORM", "LIST_FILTER", "ARRAY_FILTER", "TRANSFORM_KEYS", "TRANSFORM_VALUES", "MAP_FILTER", "ALL_MATCH", "ANY_MATCH", "NONE_MATCH":
		return index == 1
	case "REDUCE", "AGGREGATE":
		return index == 2 || index == 3
	case "LIST_REDUCE", "ARRAY_REDUCE":
		return index == 1
	case "ZIP_WITH", "MAP_ZIP_WITH":
		return index == 2
	case "ARRAYMAP", "ARRAYFILTER", "ARRAYEXISTS", "ARRAYALL", "ARRAYFIRST", "ARRAYFIRSTINDEX", "ARRAYCOUNT":
		return index == 0
	}
	return false
}

func lambdaParts(expression Expr) ([]Identifier, Expr, bool) {
	if lambda, ok := expression.(*LambdaExpr); ok {
		names := make([]Identifier, len(lambda.Parameters))
		for i, p := range lambda.Parameters {
			names[i] = p.Name
		}
		return names, lambda.Body, len(names) > 0
	}
	if paren, ok := expression.(*ParenthesizedExpr); ok {
		return lambdaParts(paren.Expr)
	}
	arrow, ok := expression.(*BinaryExpr)
	if !ok || arrow.Operator != "->" {
		return nil, nil, false
	}
	var parameters []Identifier
	var collect func(Expr) bool
	collect = func(e Expr) bool {
		switch v := e.(type) {
		case *IdentifierExpr:
			if len(v.Parts) == 1 {
				parameters = append(parameters, v.Parts[0])
				return true
			}
		case *TupleExpr:
			for _, item := range v.Items {
				if !collect(item) {
					return false
				}
			}
			return len(v.Items) > 0
		case *ParenthesizedExpr:
			return collect(v.Expr)
		}
		return false
	}
	if !collect(arrow.Left) {
		return nil, nil, false
	}
	return parameters, arrow.Right, true
}

// Lexical expression traversal is shared by validation and dependency facts.
// The public generic AST visitor deliberately remains syntax-only.
func walkLexicalColumns(root Node, dialect Dialect, visit func(ColumnReference)) {
	var walk func(Node, map[string]bool)
	walk = func(node Node, locals map[string]bool) {
		if node == nil {
			return
		}
		switch value := node.(type) {
		case *SelectStmt:
			if node != root {
				return
			}
		case *CastExpr:
			walk(value.Value, locals)
			return
		case *IntervalExpr:
			walk(value.Value, locals)
			return
		case *FunctionCallExpr:
			lambdas := make(map[Node]bool)
			for i, argument := range value.Args {
				if !lambdaArgument(value, i) {
					continue
				}
				parameters, body, ok := lambdaParts(argument)
				if !ok {
					continue
				}
				child := make(map[string]bool, len(locals)+len(parameters))
				for name := range locals {
					child[name] = true
				}
				for _, parameter := range parameters {
					child[identifierKey(parameter, dialect)] = true
				}
				walk(body, child)
				lambdas[argument] = true
			}
			for _, child := range nodeChildren(node) {
				if !lambdas[child] {
					walk(child, locals)
				}
			}
			return
		case *IdentifierExpr:
			if len(value.Parts) == 0 || value.Parts[len(value.Parts)-1].Text == "*" || locals[identifierKey(value.Parts[0], dialect)] {
				return
			}
			reference := ColumnReference{Column: value.Parts[len(value.Parts)-1].Text, Span: value.SourceSpan()}
			if len(value.Parts) > 1 {
				reference.Table = identifiersText(value.Parts[:len(value.Parts)-1])
			} else if !value.Parts[0].Quoted {
				switch strings.ToUpper(reference.Column) {
				case "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP", "LOCALTIME", "LOCALTIMESTAMP", "CURRENT_USER":
					return
				}
			}
			visit(reference)
			return
		}
		for _, child := range nodeChildren(node) {
			if query, ok := node.(*SelectStmt); ok {
				skip := false
				for _, group := range query.GroupBy {
					skip = skip || child == group && isGroupByAll(group)
				}
				if skip {
					continue
				}
			}
			walk(child, locals)
		}
	}
	walk(root, nil)
}

func isGroupByAll(expression Expr) bool {
	id, ok := expression.(*IdentifierExpr)
	return ok && len(id.Parts) == 1 && !id.Parts[0].Quoted && strings.EqualFold(id.Parts[0].Text, "ALL")
}

func (scope *queryScope) aliasForReference(reference ColumnReference, aliases, prefer bool) *SelectItem {
	if reference.Table != "" {
		return nil
	}
	visible := scope.atReference(reference)
	if visible == scope.compoundOrder {
		return nil // Compound output aliases are columns, not last-arm expressions.
	}
	binding := visible.resolve(reference)
	if !prefer && binding.status != "missing" {
		return nil
	}
	limit := 0
	contains := func(e Expr) bool {
		return e != nil && reference.Span.Start >= e.SourceSpan().Start && reference.Span.End <= e.SourceSpan().End
	}
	for clause, expression := range map[string]Expr{"where": scope.query.Where, "having": scope.query.Having, "qualify": scope.query.Qualify} {
		if contains(expression) {
			aliases = clauseAllowsAliases(scope.bindings.options.Dialect, clause)
		}
	}
	for _, group := range scope.query.GroupBy {
		if contains(group) {
			aliases = clauseAllowsAliases(scope.bindings.options.Dialect, "group")
			if scope.bindings.options.Dialect == DialectPostgreSQL && group.SourceSpan() != reference.Span {
				aliases = false // PostgreSQL permits a bare alias, not an expression over it.
			}
		}
	}
	if aliases {
		limit = len(scope.query.Projections)
	}
	if supportsLateralAliases(scope.bindings.options.Dialect) {
		for index, projection := range scope.query.Projections {
			span := projection.Expr.SourceSpan()
			if reference.Span.Start >= span.Start && reference.Span.End <= span.End {
				limit = index
				break
			}
		}
	}
	name := scope.bindings.identifiers[reference.Span]
	if name.Text == "" {
		name.Text = reference.Column
	}
	for index := 0; index < limit; index++ {
		projection := &scope.query.Projections[index]
		if projection.Alias != nil && identifierKey(*projection.Alias, scope.bindings.options.Dialect) == identifierKey(name, scope.bindings.options.Dialect) {
			return projection
		}
	}
	return nil
}

func clauseAllowsAliases(dialect Dialect, clause string) bool {
	if clause == "order" || clause == "qualify" {
		return true
	}
	if clause == "group" {
		return dialect != DialectTrino && dialect != DialectPresto && dialect != DialectAthena && dialect != DialectTSQL
	}
	if clause == "having" {
		switch dialect {
		case DialectPostgreSQL, DialectTrino, DialectPresto, DialectAthena, DialectTSQL:
			return false
		}
		return true
	}
	if clause == "where" {
		return dialect == DialectDuckDB || dialect == DialectSnowflake || dialect == DialectSQLite
	}
	return false
}

func (scope *queryScope) expandedReferences(expression Node, aliases, prefer bool, visiting map[*SelectItem]bool) []boundReference {
	var result []boundReference
	walkLexicalColumns(expression, scope.bindings.options.Dialect, func(reference ColumnReference) {
		if projection := scope.aliasForReference(reference, aliases, prefer); projection != nil && !visiting[projection] {
			visiting[projection] = true
			for _, binding := range scope.expandedReferences(projection.Expr, false, false, visiting) {
				binding.reference.Span = reference.Span
				result = append(result, binding)
			}
			delete(visiting, projection)
			return
		}
		result = append(result, scope.atReference(reference).resolve(reference))
	})
	return result
}
