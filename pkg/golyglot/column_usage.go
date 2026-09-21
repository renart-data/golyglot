package golyglot

import "strings"

// ColumnUseFact describes one non-projection expression in its lexical scope.
// References retain the immediate relation (including a CTE/derived table);
// Upstream resolves it to physical columns without pretending that a filter
// contributes to the value lineage of a projected output column.
type ColumnUseFact struct {
	Context       string                `json:"context"`
	ExpressionSQL string                `json:"expressionSql"`
	Span          Span                  `json:"span"`
	References    []ColumnReferenceFact `json:"references"`
	Upstream      []ColumnReferenceFact `json:"upstream"`
	Complete      bool                  `json:"complete"`
}

func (scope *queryScope) references(expression Node, aliases bool, preferAliases ...bool) []boundReference {
	return scope.expandedReferences(expression, aliases, len(preferAliases) > 0 && preferAliases[0], make(map[*SelectItem]bool))
}

func (binding boundReference) fact() ColumnReferenceFact {
	reference := binding.reference
	span := reference.Span
	fact := ColumnReferenceFact{Column: reference.Column, Unqualified: reference.Table == "", SourceKind: "unknown", Confidence: "unknown", Span: &span}
	if reference.Table != "" {
		table := reference.Table
		fact.Table = &table
	}
	if binding.status == "ambiguous" {
		fact.Confidence = "ambiguous"
	}
	if binding.status == "coalesced" {
		fact.SourceKind, fact.Confidence = "join", "high"
	}
	if relation := binding.relation; relation != nil {
		name := relation.name
		fact.SourceName, fact.SourceKind = &name, relation.kind
		if relation.alias != "" {
			alias := relation.alias
			fact.SourceAlias = &alias
		}
		if binding.status == "resolved" {
			fact.Confidence = "high"
		}
	}
	return fact
}

type scopeSourceKey struct {
	relation *scopeRelation
	column   string
}

// outputBindings follows positional UNION outputs through both branches.
// EXCEPT/INTERSECT right-hand values are tracked separately as set filters.
func (scope *queryScope) outputBindings(index int) ([]boundReference, bool) {
	if scope == nil || index < 0 || index >= len(scope.outputs) {
		return nil, false
	}
	var result []boundReference
	complete := true
	if scope.query.SetLeft != nil {
		left := scope.bindings.queries[scope.query.SetLeft]
		leftIndex := index
		if setByName(scope.query) {
			leftIndex = left.outputIndex(scope.outputs[index])
		}
		if leftIndex >= 0 {
			result, complete = left.outputBindings(leftIndex)
		} else {
			complete = left.complete
		}
	} else if index < scope.leftOutputCount {
		output := scope.outputs[index]
		if output.relation != nil {
			result = append(result, boundReference{reference: ColumnReference{Column: output.column}, relation: output.relation, status: "resolved"})
		} else if output.expression != nil {
			result = output.owner.references(output.expression, false)
		}
	}
	if scope.query.SetRight != nil && strings.HasSuffix(strings.ToUpper(scope.query.SetOperator), "UNION") {
		rightScope := scope.bindings.queries[scope.query.SetRight]
		rightIndex := index
		if setByName(scope.query) {
			rightIndex = rightScope.outputIndex(scope.outputs[index])
		}
		if rightIndex < 0 {
			return result, complete && rightScope.complete
		}
		right, known := rightScope.outputBindings(rightIndex)
		result = append(result, right...)
		complete = complete && known
	}
	return result, complete
}

func bindingUpstream(binding boundReference, visiting map[scopeSourceKey]bool) ([]ColumnReferenceFact, bool) {
	if len(binding.coalesced) > 0 {
		var result []ColumnReferenceFact
		complete := true
		for _, relation := range binding.coalesced {
			upstream, known := bindingUpstream(boundReference{reference: binding.reference, relation: relation, status: "resolved"}, visiting)
			result = append(result, upstream...)
			complete = complete && known
		}
		return result, complete
	}
	relation := binding.relation
	if binding.status != "resolved" || relation == nil {
		return nil, false
	}
	if relation.kind == "table" {
		return []ColumnReferenceFact{binding.fact()}, true
	}
	key := scopeSourceKey{relation, strings.ToLower(binding.reference.Column)}
	if visiting[key] {
		return nil, false
	}
	visiting[key] = true
	defer delete(visiting, key)
	var bindings []boundReference
	complete := true
	if relation.kind == "table_function" {
		for _, argument := range relation.arguments {
			bindings = append(bindings, relation.owner.references(argument, false)...)
		}
	} else {
		if relation.source == nil {
			return nil, false
		}
		found := false
		for index := range relation.columns {
			if !relation.matchesColumn(index, binding.reference) || index >= len(relation.source.outputs) {
				continue
			}
			found = true
			output, known := relation.source.outputBindings(index)
			bindings = append(bindings, output...)
			complete = complete && known
		}
		if !found {
			return nil, false
		}
	}
	var result []ColumnReferenceFact
	for _, child := range bindings {
		upstream, known := bindingUpstream(child, visiting)
		complete = complete && known
		for _, fact := range upstream {
			// Show the use site, not the original CTE projection, to consumers.
			span := binding.reference.Span
			fact.Span = &span
			result = append(result, fact)
		}
	}
	return result, complete
}

func queryColumnUses(root *queryScope) []ColumnUseFact {
	var uses []ColumnUseFact
	seen := make(map[*queryScope]bool)
	var visit func(*queryScope, string)
	visit = func(scope *queryScope, projectionContext string) {
		if scope == nil || seen[scope] {
			return
		}
		seen[scope] = true
		query := scope.query
		emit := func(context string, expression Expr, aliases bool) {
			if expression == nil {
				return
			}
			use := ColumnUseFact{Context: context, ExpressionSQL: expressionSQL(expression, scope.bindings.options.Dialect), Span: expression.SourceSpan(), Complete: true}
			bindings := scope.references(expression, aliases, context == "order")
			if isStarExpression(expression) {
				// EXCEPT/INTERSECT and subquery results depend on every expanded
				// column, including SELECT * through a derived table or CTE.
				for _, output := range scope.outputs {
					if output.relation != nil {
						bindings = append(bindings, boundReference{reference: ColumnReference{Column: output.column, Span: expression.SourceSpan()}, relation: output.relation, status: "resolved"})
					} else if output.expression != nil {
						bindings = append(bindings, output.owner.references(output.expression, false)...)
					}
				}
				use.Complete = scope.complete
			}
			for _, binding := range bindings {
				use.References = append(use.References, binding.fact())
				upstream, complete := bindingUpstream(binding, make(map[scopeSourceKey]bool))
				use.Upstream = append(use.Upstream, upstream...)
				use.Complete = use.Complete && complete
			}
			if len(use.References) > 0 || !use.Complete {
				uses = append(uses, use)
			}
		}
		window := func(spec *WindowSpec) {
			if spec == nil {
				return
			}
			for _, expression := range spec.PartitionBy {
				emit("window_partition", expression, false)
			}
			for _, order := range spec.OrderBy {
				emit("window_order", order.Expr, false)
			}
		}
		var table func(TableExpr)
		table = func(source TableExpr) {
			if grouped, ok := source.Primary.(*GroupedFrom); ok {
				for _, child := range grouped.Items {
					table(child)
				}
			}
			for _, join := range source.Joins {
				if strings.Contains(strings.ToUpper(join.JoinText), "NATURAL") {
					// Implicit join keys are not yet represented by the binder.
					uses = append(uses, ColumnUseFact{Context: "join", ExpressionSQL: join.JoinText, Span: join.SourceSpan(), Complete: false})
				}
				emit("join", join.Condition, false)
				for _, column := range join.Using {
					use := ColumnUseFact{Context: "join", ExpressionSQL: "USING (" + column.Text + ")", Span: column.Span, Complete: true}
					for _, binding := range scope.using[column.Span] {
						use.References = append(use.References, binding.fact())
						upstream, complete := bindingUpstream(binding, make(map[scopeSourceKey]bool))
						use.Upstream = append(use.Upstream, upstream...)
						use.Complete = use.Complete && complete
					}
					uses = append(uses, use)
				}
			}
		}
		for _, source := range query.From {
			table(source)
		}
		emit("filter", query.Where, false)
		for _, expression := range query.GroupBy {
			emit("group", expression, true)
		}
		emit("having", query.Having, true)
		emit("qualify", query.Qualify, true)
		emit("connect_by", query.ConnectBy, false)
		for _, order := range query.OrderBy {
			emit("order", order.Expr, true)
		}
		for _, order := range query.SortBy {
			emit("order", order.Expr, true)
		}
		for _, named := range query.Windows {
			window(&named.Spec)
		}
		if projectionContext != "" {
			for _, projection := range query.Projections {
				emit(projectionContext, projection.Expr, false)
			}
		}
		// Visit only referenced CTE definitions, never unused sibling CTEs.
		for _, relation := range scope.relations {
			visit(relation.source, "")
		}
		cteQueries := make(map[*SelectStmt]bool)
		for _, cte := range query.With {
			cteQueries[cte.Query] = true
		}
		Walk(query, func(node Node) VisitAction {
			switch value := node.(type) {
			case *SubqueryExpr:
				visit(scope.bindings.queries[value.Query], "subquery")
				return SkipChildren
			case *ExistsExpr:
				visit(scope.bindings.queries[value.Query], "")
				return SkipChildren
			case *InExpr:
				visit(scope.bindings.queries[value.Query], "subquery")
			case *QuantifiedExpr:
				visit(scope.bindings.queries[value.Query], "subquery")
			case *SelectStmt:
				if value != query {
					if !cteQueries[value] {
						context := projectionContext
						if value == query.SetRight && (strings.EqualFold(query.SetOperator, "EXCEPT") || strings.EqualFold(query.SetOperator, "INTERSECT")) {
							context = "set_filter"
						}
						visit(scope.bindings.queries[value], context)
					}
					return SkipChildren
				}
			case *FunctionCallExpr:
				window(value.Over)
				emit("filter", value.Filter, false)
				emit("having", value.Having, false)
				for _, order := range value.OrderBy {
					emit("order", order.Expr, false)
				}
				for _, order := range value.WithinGroup {
					emit("order", order.Expr, false)
				}
			}
			return VisitChildren
		})
	}
	visit(root, "")
	return uses
}
