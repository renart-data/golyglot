package golyglot

import "fmt"

func hasNamedSetOperation(query *SelectStmt) bool {
	found := false
	Walk(query, func(node Node) VisitAction {
		if selectStmt, ok := node.(*SelectStmt); ok && setByName(selectStmt) {
			found = true
		}
		return VisitChildren
	})
	return found
}

// Name-aligned sets need the same expanded output slots as query analysis,
// including columns that exist only on the right and NULL-padded branches.
func namedSetQueryOutput(scope *queryScope) QueryOutput {
	result := QueryOutput{OrdinalComplete: scope.complete}
	for index, output := range scope.outputs {
		name, ordinal := output.name, index
		column := OutputColumn{Kind: OutputColumnNamed, Name: &name, Ordinal: &ordinal}
		if name == "" {
			column.Kind, column.Name = OutputColumnUnnamed, nil
		}
		if !scope.complete {
			column.Ordinal = nil
		}
		result.Columns = append(result.Columns, column)
	}
	return result
}

func namedSetLineage(query *SelectStmt, column string, ordinal int, sourceSQL string, options AnalyzeQueryOptions) (LineageNode, error) {
	scope := bindQuery(query, options)
	if !scope.complete {
		return LineageNode{}, fmt.Errorf("name-aligned lineage requires resolved branch outputs; supply the missing schema")
	}
	selected := ordinal
	if selected < 0 {
		for index, output := range scope.outputs {
			if output.name == column {
				selected = index
				break
			}
		}
		if selected < 0 {
			selected = scope.outputIndex(scopeOutput{name: column})
		}
	}
	if selected < 0 || selected >= len(scope.outputs) {
		return LineageNode{}, fmt.Errorf("column %q or ordinal %d is not a resolved output of the query", column, ordinal)
	}
	root := boundOutputLineage(scope, selected, sourceSQL, make(map[boundLineageKey]bool))
	root.SourceKind = "root"
	return root, nil
}

type boundLineageKey struct {
	scope *queryScope
	index int
}

func boundOutputLineage(scope *queryScope, index int, sql string, visiting map[boundLineageKey]bool) LineageNode {
	output := scope.outputs[index]
	node := LineageNode{Name: output.name, Source: lineageSQLJSON(sql), SourceKind: "branch",
		Expression: lineageSQLJSON(expressionSQL(output.expression, scope.bindings.options.Dialect))}
	key := boundLineageKey{scope, index}
	if visiting[key] {
		node.SourceKind = "unknown"
		return node
	}
	visiting[key] = true
	defer delete(visiting, key)
	if scope.query.SetRight != nil {
		left := scope.bindings.queries[scope.query.SetLeft]
		if left == nil {
			copy, query := *scope, *scope.query
			query.SetRight = nil
			copy.query, copy.outputs = &query, scope.outputs[:scope.leftOutputCount]
			left = &copy
		}
		right := scope.bindings.queries[scope.query.SetRight]
		for branchIndex, branch := range []*queryScope{left, right} {
			branchColumn := index
			if setByName(scope.query) {
				branchColumn = branch.outputIndex(output)
			}
			child := LineageNode{Name: output.name, Source: lineageSQLJSON(sql), Expression: lineageSQLJSON("NULL"), SourceKind: "branch"}
			if branchColumn >= 0 && branchColumn < len(branch.outputs) {
				child = boundOutputLineage(branch, branchColumn, sql, visiting)
			}
			child.SetBranch = &SetBranch{Operator: lineageSetOperator(scope.query.SetOperator), Ordinal: branchIndex, All: scope.query.SetAll}
			node.Downstream = append(node.Downstream, child)
		}
		return node
	}
	var refs []boundReference
	if output.relation != nil {
		refs = []boundReference{{reference: ColumnReference{Column: output.column}, relation: output.relation, status: "resolved"}}
	} else if output.expression != nil {
		refs = output.owner.references(output.expression, false)
	}
	seen := make(map[string]bool)
	for _, ref := range refs {
		// Keep the immediate CTE/derived relation in the graph, then resolve
		// its output slot using the already-built lexical scopes.
		fact := ref.fact()
		name := fact.Column
		if fact.SourceAlias != nil {
			name = *fact.SourceAlias + "." + name
		} else if fact.SourceName != nil {
			name = *fact.SourceName + "." + name
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		child := LineageNode{Name: name, ReferenceNodeName: name, SourceKind: fact.SourceKind, SourceAlias: fact.SourceAlias,
			Source: lineageSQLJSON(sql), Expression: lineageSQLJSON(fact.Column)}
		if fact.SourceName != nil {
			child.SourceName = *fact.SourceName
		}
		if relation := ref.relation; relation != nil && relation.source != nil {
			for sourceIndex := range relation.columns {
				if relation.matchesColumn(sourceIndex, ref.reference) && sourceIndex < len(relation.source.outputs) {
					child.Downstream = append(child.Downstream, boundOutputLineage(relation.source, sourceIndex, sql, visiting))
				}
			}
		}
		node.Downstream = append(node.Downstream, child)
	}
	return node
}
