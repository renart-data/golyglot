package golyglot

import "strings"

// queryBindings owns lexical name binding for validation and dependency facts.
// A query block never resolves against a flattened list of its descendants.
// Types remain the responsibility of the existing semantic inference pass.
type queryBindings struct {
	options AnalyzeQueryOptions
	scopes  []*queryScope
	queries map[*SelectStmt]*queryScope
}

type queryScope struct {
	query      *SelectStmt
	parent     *queryScope
	ctes       map[string]*scopeRelation
	relations  []*scopeRelation
	outputs    []scopeOutput
	complete   bool
	bindings   *queryBindings
	joins      map[Span]*queryScope
	using      map[Span][]boundReference
	usingOrder []Span
	coalesced  map[string][]*scopeRelation
}

type scopeRelation struct {
	name, alias, kind string
	span              Span
	columns           []string
	known             bool
	source            *queryScope
	arguments         []Expr
	owner             *queryScope
}

type scopeOutput struct {
	name       string
	expression Expr
	owner      *queryScope
	relation   *scopeRelation
	column     string
}

type boundReference struct {
	reference ColumnReference
	relation  *scopeRelation
	coalesced []*scopeRelation
	// resolved, unknown, missing, or ambiguous; unknown means that a relation
	// exists but its schema is incomplete, not that an arbitrary source won.
	status string
}

func bindQuery(query *SelectStmt, options AnalyzeQueryOptions) *queryScope {
	bindings := &queryBindings{options: options, queries: make(map[*SelectStmt]*queryScope)}
	return bindings.build(query, nil, nil)
}

func (b *queryBindings) build(query *SelectStmt, parent *queryScope, ctes map[string]*scopeRelation) *queryScope {
	if query == nil {
		return nil
	}
	if scope := b.queries[query]; scope != nil {
		return scope
	}
	scope := &queryScope{query: query, parent: parent, ctes: make(map[string]*scopeRelation), complete: true, bindings: b,
		joins: make(map[Span]*queryScope), using: make(map[Span][]boundReference), coalesced: make(map[string][]*scopeRelation)}
	for name, relation := range ctes {
		scope.ctes[name] = relation
	}
	b.queries[query] = scope
	b.scopes = append(b.scopes, scope)
	for _, cte := range query.With {
		name := strings.ToLower(cte.Name.Text)
		relation := &scopeRelation{name: cte.Name.Text, kind: "cte", span: cte.Span}
		if cte.Recursive {
			relation.columns = identifierTexts(cte.Columns)
			relation.known = len(relation.columns) > 0
			scope.ctes[name] = relation
		}
		// The map is copied by build: later sibling CTEs cannot leak backwards.
		relation.source = b.build(cte.Query, parent, scope.ctes)
		relation.setOutputColumns(cte.Columns)
		scope.ctes[name] = relation
	}
	var collectTable func(TableExpr)
	var collectItem func(FromItem)
	collectItem = func(item FromItem) {
		if item == nil {
			return
		}
		relation := &scopeRelation{span: item.SourceSpan(), owner: scope}
		switch value := item.(type) {
		case *TableName:
			relation.name, relation.alias, relation.kind = identifiersText(value.Parts), optionalIdentifierText(value.Alias), "table"
			// A qualified warehouse relation must not be shadowed by a CTE.
			if cte := scope.ctes[strings.ToLower(relation.name)]; len(value.Parts) == 1 && cte != nil {
				relation.kind, relation.source, relation.known = "cte", cte.source, cte.known
				relation.columns = append([]string(nil), cte.columns...)
			} else if table, ok := findSchemaTableValue(b.options.Schema, relation.name); ok {
				relation.known = true
				for _, column := range table.Columns {
					relation.columns = append(relation.columns, column.Name)
				}
			}
			if len(value.Columns) > 0 {
				relation.columns = identifierTexts(value.Columns)
			}
		case *SubqueryFrom:
			relation.name, relation.alias, relation.kind = optionalIdentifierText(value.Alias), optionalIdentifierText(value.Alias), "derived"
			outer := parent
			if value.Lateral || b.options.Dialect == DialectDuckDB {
				// Only preceding FROM items are visible to a lateral subquery.
				outer = scope.snapshot()
			}
			relation.source = b.build(value.Query, outer, scope.ctes)
			relation.setOutputColumns(value.Columns)
		case *TableFunctionFrom:
			relation.name, relation.alias, relation.kind = identifiersText(value.Name), optionalIdentifierText(value.Alias), "table_function"
			relation.arguments = value.Args
			relation.columns = identifierTexts(value.Columns)
			if len(relation.columns) == 0 && len(value.Name) > 0 {
				relation.columns = []string{strings.ToLower(value.Name[len(value.Name)-1].Text)}
			}
			relation.known = true
		case *GroupedFrom:
			for _, table := range value.Items {
				collectTable(table)
			}
			return
		case *RawFrom:
			relation.name, relation.alias, relation.kind = value.Raw, optionalIdentifierText(value.Alias), "raw"
			relation.columns = identifierTexts(value.Columns)
			relation.known = len(relation.columns) > 0
		default:
			return
		}
		scope.relations = append(scope.relations, relation)
	}
	collectTable = func(table TableExpr) {
		tableStart := len(scope.relations)
		collectItem(table.Primary)
		for _, join := range table.Joins {
			left := scope.snapshot()
			left.relations = left.relations[tableStart:]
			left.parent = nil // USING requires a column on each actual join side.
			before := len(scope.relations)
			collectItem(join.Right)
			right := scope.snapshot()
			right.relations = right.relations[before:]
			right.parent = nil
			for _, column := range join.Using {
				scope.usingOrder = append(scope.usingOrder, column.Span)
				reference := ColumnReference{Column: column.Text, Span: column.Span}
				for _, side := range []*queryScope{left, right} {
					binding := side.resolve(reference)
					if len(binding.coalesced) == 0 {
						scope.using[column.Span] = append(scope.using[column.Span], binding)
					} else {
						for _, relation := range binding.coalesced {
							scope.using[column.Span] = append(scope.using[column.Span], boundReference{reference: reference, relation: relation, status: "resolved"})
						}
					}
				}
				var merged []*scopeRelation
				for _, binding := range scope.using[column.Span] {
					if binding.status != "resolved" {
						merged = nil
						break
					}
					merged = append(merged, binding.relation)
				}
				if len(merged) > 1 {
					scope.coalesced[strings.ToLower(column.Text)] = merged
				}
			}
			if join.Condition != nil {
				visible := scope.snapshot()
				visible.relations = visible.relations[tableStart:]
				scope.joins[join.Condition.SourceSpan()] = visible
			}
		}
	}
	for _, table := range query.From {
		collectTable(table)
	}
	if query.SetLeft != nil {
		left := b.build(query.SetLeft, parent, scope.ctes)
		scope.outputs, scope.complete = left.outputs, left.complete
	} else {
		scope.collectOutputs()
	}
	b.build(query.SetRight, parent, scope.ctes)
	// Register scalar/EXISTS/IN subqueries without walking into another block.
	Walk(query, func(node Node) VisitAction {
		if child, ok := node.(*SelectStmt); ok && child != query {
			b.build(child, scope.atReference(ColumnReference{Span: child.SourceSpan()}), scope.ctes)
			return SkipChildren
		}
		return VisitChildren
	})
	return scope
}

func (relation *scopeRelation) setOutputColumns(aliases []Identifier) {
	if relation.source != nil {
		relation.columns = nil
		relation.known = relation.source.complete
		for _, output := range relation.source.outputs {
			relation.columns = append(relation.columns, output.name)
		}
	}
	for index, alias := range aliases {
		if index < len(relation.columns) {
			relation.columns[index] = alias.Text
		} else {
			relation.columns = append(relation.columns, alias.Text)
		}
	}
}

func (scope *queryScope) collectOutputs() {
	for _, projection := range scope.query.Projections {
		if !isStarExpression(projection.Expr) {
			name := projectionName(projection)
			scope.outputs = append(scope.outputs, scopeOutput{name: name, expression: projection.Expr, owner: scope})
			scope.complete = scope.complete && name != ""
			continue
		}
		qualifier := starQualifier(projection.Expr)
		matched := false
		for _, relation := range scope.relations {
			if qualifier != "" && !relation.matches(qualifier) {
				continue
			}
			matched = true
			scope.complete = scope.complete && relation.known
			for _, name := range relation.columns {
				excluded := false
				for _, expression := range projection.Except {
					excluded = excluded || strings.EqualFold(expressionOutputName(expression), name)
				}
				if excluded {
					continue
				}
				output := scopeOutput{name: name, owner: scope, relation: relation, column: name}
				for _, replacement := range projection.Replace {
					if strings.EqualFold(projectionName(replacement), name) {
						output.expression, output.relation = replacement.Expr, nil
					}
				}
				scope.outputs = append(scope.outputs, output)
			}
		}
		scope.complete = scope.complete && matched
	}
	if len(scope.query.ValuesRows) > 0 {
		for index, expression := range scope.query.ValuesRows[0] {
			name := ""
			if index < len(scope.query.ValuesColumns) {
				name = scope.query.ValuesColumns[index].Text
			}
			scope.outputs = append(scope.outputs, scopeOutput{name: name, expression: expression, owner: scope})
		}
	}
}

func (relation *scopeRelation) matches(qualifier string) bool {
	if relation.alias != "" {
		return strings.EqualFold(relation.alias, qualifier)
	}
	return strings.EqualFold(relation.name, qualifier) || strings.EqualFold(lastIdentifier(relation.name), qualifier)
}

func (scope *queryScope) resolve(reference ColumnReference) boundReference {
	result := boundReference{reference: reference, status: "missing"}
	for current := scope; current != nil; current = current.parent {
		var matches, unknown []*scopeRelation
		qualified := false
		for _, relation := range current.relations {
			if reference.Table != "" && !relation.matches(reference.Table) {
				continue
			}
			qualified = qualified || reference.Table != ""
			if !relation.known {
				unknown = append(unknown, relation)
				continue
			}
			for _, column := range relation.columns {
				if strings.EqualFold(column, reference.Column) {
					matches = append(matches, relation)
				}
			}
		}
		switch {
		case len(matches) > 1:
			if reference.Table == "" && len(unknown) == 0 && sameScopeRelations(matches, current.coalesced[strings.ToLower(reference.Column)]) {
				result.status, result.coalesced = "coalesced", matches
				return result
			}
			result.status = "ambiguous"
			return result
		case len(unknown) > 0:
			result.status = "unknown"
			if len(matches) == 0 && len(unknown) == 1 {
				result.relation = unknown[0]
			}
			return result
		case len(matches) == 1:
			result.relation, result.status = matches[0], "resolved"
			return result
		case qualified:
			// A local alias shadows the outer alias even if the column is absent.
			return result
		}
	}
	return result
}

func sameScopeRelations(left, right []*scopeRelation) bool {
	if len(left) != len(right) {
		return false
	}
	for _, candidate := range left {
		found := false
		for _, relation := range right {
			found = found || candidate == relation
		}
		if !found {
			return false
		}
	}
	return true
}

func (scope *queryScope) snapshot() *queryScope {
	visible := *scope
	visible.relations = append([]*scopeRelation(nil), scope.relations...)
	visible.coalesced = make(map[string][]*scopeRelation, len(scope.coalesced))
	for name, relations := range scope.coalesced {
		visible.coalesced[name] = append([]*scopeRelation(nil), relations...)
	}
	return &visible
}

func (scope *queryScope) atReference(reference ColumnReference) *queryScope {
	for span, joined := range scope.joins {
		if reference.Span.Start >= span.Start && reference.Span.End <= span.End {
			return joined
		}
	}
	return scope
}

// walkScopeColumns prunes nested queries and syntax-only identifiers (CAST
// types and interval units). It never changes the generic public AST visitor.
func walkScopeColumns(node Node, visit func(ColumnReference)) {
	Walk(node, func(child Node) VisitAction {
		switch value := child.(type) {
		case *SelectStmt:
			if child != node {
				return SkipChildren
			}
		case *CastExpr:
			walkScopeColumns(value.Value, visit)
			return SkipChildren
		case *IntervalExpr:
			walkScopeColumns(value.Value, visit)
			return SkipChildren
		case *IdentifierExpr:
			if len(value.Parts) == 0 || value.Parts[len(value.Parts)-1].Text == "*" {
				return SkipChildren
			}
			reference := ColumnReference{Column: value.Parts[len(value.Parts)-1].Text, Span: value.SourceSpan()}
			if len(value.Parts) > 1 {
				reference.Table = identifiersText(value.Parts[:len(value.Parts)-1])
			} else if !value.Parts[0].Quoted {
				switch strings.ToUpper(reference.Column) {
				case "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP", "LOCALTIME", "LOCALTIMESTAMP", "CURRENT_USER":
					return SkipChildren
				}
			}
			visit(reference)
		}
		return VisitChildren
	})
}
