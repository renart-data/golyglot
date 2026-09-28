package golyglot

import "fmt"

// SYS.DUMMY is a one-row relation, not syntactic noise. Identify it using
// lexical CTE scope before replacing any tables, including nested subqueries.
func lowerHANADummy(root Node, target Dialect) (Node, error) {
	tables := make(map[*TableName]bool)
	var visit func(Node, bool) error
	visit = func(node Node, shadowed bool) error {
		cteBodies := make(map[Node]bool)
		if query, ok := node.(*SelectStmt); ok {
			for _, cte := range query.With {
				if cte.Recursive && identifierKey(cte.Name, DialectHANA) == "DUMMY" {
					shadowed = true
				}
			}
			for _, cte := range query.With {
				if cte.Query != nil {
					if err := visit(cte.Query, shadowed); err != nil {
						return err
					}
					cteBodies[cte.Query] = true
				}
				shadowed = shadowed || identifierKey(cte.Name, DialectHANA) == "DUMMY"
			}
		}
		if id, ok := node.(*IdentifierExpr); ok && len(id.Parts) >= 3 && identifierKey(id.Parts[0], DialectHANA) == "SYS" && identifierKey(id.Parts[1], DialectHANA) == "DUMMY" {
			return fmt.Errorf("golyglot: schema-qualified HANA DUMMY column references require an explicit table alias")
		}
		if table, ok := node.(*TableName); ok && len(table.Parts) > 0 && identifierKey(table.Parts[len(table.Parts)-1], DialectHANA) == "DUMMY" {
			system := len(table.Parts) == 1 && !shadowed || len(table.Parts) == 2 && identifierKey(table.Parts[0], DialectHANA) == "SYS"
			if system {
				// DuckDB resolves quoted and unquoted identifiers alike. Other
				// targets need additional case/binding verification before this
				// replacement is safe for quoted HANA column references.
				if target != DialectDuckDB || table.Sample != nil || table.Hint != "" || table.Tail != "" {
					return fmt.Errorf("golyglot: HANA DUMMY has no verified mapping to %s with these table modifiers", target)
				}
				tables[table] = true
			}
		}
		for _, child := range nodeChildren(node) {
			if !cteBodies[child] {
				if err := visit(child, shadowed); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(root, false); err != nil {
		return nil, err
	}
	return Transform(root, func(node Node) Node {
		table, ok := node.(*TableName)
		if !ok || !tables[table] {
			return node
		}
		alias := table.Alias
		if alias == nil {
			name := table.Parts[len(table.Parts)-1]
			alias = &name
		}
		return &SubqueryFrom{
			nodeBase: table.nodeBase, Alias: alias, Columns: table.Columns,
			Query: &SelectStmt{Projections: []SelectItem{{Expr: &LiteralExpr{KindValue: LiteralString, Raw: "'X'"}, Alias: &Identifier{Text: "DUMMY"}}}},
		}
	}), nil
}
