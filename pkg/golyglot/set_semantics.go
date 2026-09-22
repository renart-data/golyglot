package golyglot

import "strings"

func setByName(query *SelectStmt) bool {
	return query != nil && query.SetRight != nil && strings.Contains(strings.ToUpper(query.SetModifier), "BY NAME")
}

func coerceNamedSetOutput(left, right []semanticColumn, dialect Dialect) ([]semanticColumn, bool) {
	result := cloneSemanticColumns(left)
	indices := make(map[string]int)
	unique := true
	for index, column := range left {
		key := identifierKey(Identifier{Text: column.name, Quoted: column.quoted}, dialect)
		if _, exists := indices[key]; exists || key == "" {
			unique = false
		}
		indices[key] = index
		result[index].nullability = nullabilityNullable // padded until matched
	}
	seen := make(map[string]bool)
	for _, column := range right {
		key := identifierKey(Identifier{Text: column.name, Quoted: column.quoted}, dialect)
		if seen[key] || key == "" {
			unique = false
		}
		seen[key] = true
		if index, exists := indices[key]; exists {
			result[index] = coerceSetOutput(left[index:index+1], []semanticColumn{column}, dialect)[0]
		} else {
			column.nullability = nullabilityNullable
			result = append(result, column)
		}
	}
	if !unique {
		for i := range result {
			result[i].dataType = DataType{Kind: DataTypeUnknown}
		}
	}
	return result, unique
}

func (scope *queryScope) outputIndex(column scopeOutput) int {
	dialect := scope.bindings.options.Dialect
	key := identifierKey(Identifier{Text: column.name, Quoted: column.quoted}, dialect)
	for i, output := range scope.outputs {
		if identifierKey(Identifier{Text: output.name, Quoted: output.quoted}, dialect) == key {
			return i
		}
	}
	return -1
}

func projectionQuoted(projection SelectItem) bool {
	if projection.Alias != nil {
		return projection.Alias.Quoted
	}
	if identifier, ok := projection.Expr.(*IdentifierExpr); ok && len(identifier.Parts) > 0 {
		return identifier.Parts[len(identifier.Parts)-1].Quoted
	}
	return false
}

// Schema names retain the existing warehouse matching policy. Derived names
// additionally carry SQL quoting, so Snowflake "Value" cannot match VALUE.
func (relation *scopeRelation) matchesColumn(index int, reference ColumnReference) bool {
	if relation.source == nil || relation.owner == nil {
		return strings.EqualFold(relation.columns[index], reference.Column)
	}
	column := Identifier{Text: relation.columns[index], Quoted: index < len(relation.columnQuotes) && relation.columnQuotes[index]}
	name := relation.owner.bindings.identifiers[reference.Span]
	if name.Text == "" {
		name = Identifier{Text: reference.Column}
		// Expanded wildcard references already contain an output name, not
		// an unquoted token from the source text.
		if reference.Span == (Span{}) {
			return column.Text == reference.Column
		}
	}
	dialect := relation.owner.bindings.options.Dialect
	if relation.columnAliasIndexes != nil {
		position, exists := relation.columnAliasIndexes[identifierKey(name, dialect)]
		return exists && position == index
	}
	return identifierKey(column, dialect) == identifierKey(name, dialect)
}
