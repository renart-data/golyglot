package golyglot

import (
	"strconv"
	"strings"
)

const (
	nullabilityUnknown  = "unknown"
	nullabilityNullable = "nullable"
	nullabilityNonNull  = "non_null"
)

type semanticColumn struct {
	name        string
	quoted      bool
	dataType    DataType
	nullability string
}

type semanticRelation struct {
	name         string
	alias        string
	kind         string
	columns      []semanticColumn
	nullable     bool
	columnsKnown bool
}

type semanticScope struct {
	parent    *semanticScope
	relations []semanticRelation
	ctes      map[string][]semanticColumn
	dialect   Dialect
	schema    *ValidationSchema
	aliases   map[string]inferredExpression
	locals    map[string]inferredExpression
	catalog   *compiledFunctionCatalog
}

type inferredExpression struct {
	dataType       DataType
	nullability    string
	integerLiteral *int64
	hasColumn      bool
}

type semanticIssue struct {
	code    string
	message string
	span    Span
}

type semanticQuery struct {
	output        []semanticColumn
	projections   []inferredExpression
	stars         map[int][]semanticColumn
	namesComplete bool
	typesComplete bool
	issues        []semanticIssue
}

func analyzeSelectSemantics(query *SelectStmt, options AnalyzeQueryOptions, parent *semanticScope) semanticQuery {
	result := semanticQuery{stars: make(map[int][]semanticColumn), namesComplete: true, typesComplete: true}
	if query == nil {
		result.namesComplete = false
		result.typesComplete = false
		return result
	}
	scope := &semanticScope{parent: parent, ctes: make(map[string][]semanticColumn), dialect: options.Dialect, schema: options.Schema, aliases: make(map[string]inferredExpression)}
	scope.catalog = options.catalog
	if scope.schema == nil && parent != nil {
		scope.schema = parent.schema
	}
	if parent != nil {
		for name, columns := range parent.ctes {
			scope.ctes[name] = cloneSemanticColumns(columns)
		}
	}
	for _, cte := range query.With {
		child := analyzeSelectSemantics(cte.Query, options, scope)
		columns := cloneSemanticColumns(child.output)
		if len(cte.Columns) > 0 {
			for index := range columns {
				if index < len(cte.Columns) {
					columns[index].name = cte.Columns[index].Text
					columns[index].quoted = cte.Columns[index].Quoted
				}
			}
		}
		scope.ctes[strings.ToLower(cte.Name.Text)] = columns
		result.issues = append(result.issues, child.issues...)
	}
	scope.relations = semanticRelations(query, options, scope, &result.issues)

	for index, projection := range query.Projections {
		inferred := inferSemanticExpression(projection.Expr, scope, &result.issues)
		if projection.Alias != nil && supportsLateralAliases(scope.dialect) {
			scope.aliases[identifierKey(*projection.Alias, scope.dialect)] = inferred
		}
		result.projections = append(result.projections, inferred)
		if isStarExpression(projection.Expr) {
			columns, complete := semanticStarColumns(projection, scope)
			result.stars[index] = cloneSemanticColumns(columns)
			if !complete {
				result.namesComplete = false
				result.typesComplete = false
			}
			for _, column := range columns {
				result.output = append(result.output, column)
				if !column.dataType.Known() {
					result.typesComplete = false
				}
			}
			continue
		}
		name := projectionName(projection)
		if name == "" {
			result.namesComplete = false
		}
		column := semanticColumn{name: name, quoted: projectionQuoted(projection), dataType: inferred.dataType, nullability: normalizedNullability(inferred.nullability)}
		result.output = append(result.output, column)
		if !column.dataType.Known() {
			result.typesComplete = false
		}
	}
	if len(query.Projections) == 0 {
		result.namesComplete = false
		result.typesComplete = false
	}
	// Predicate and ordering calls need the same typed scope as projections.
	for _, expression := range append(append([]Expr{query.Where, query.Having, query.Qualify}, query.GroupBy...), query.Limit, query.Offset) {
		inferSemanticExpression(expression, scope, &result.issues)
	}
	for _, order := range query.OrderBy {
		inferSemanticExpression(order.Expr, scope, &result.issues)
	}
	for _, table := range query.From {
		for _, join := range table.Joins {
			inferSemanticExpression(join.Condition, scope, &result.issues)
		}
	}

	if query.SetRight != nil {
		// Branches inherit the WITH namespace, but not each other's FROM
		// relations or SELECT aliases.
		branchParent := &semanticScope{parent: parent, ctes: scope.ctes, dialect: scope.dialect, schema: scope.schema, catalog: scope.catalog}
		left := result
		if query.SetLeft != nil {
			left = analyzeSelectSemantics(query.SetLeft, options, branchParent)
			result.issues = append(result.issues, left.issues...)
		}
		right := analyzeSelectSemantics(query.SetRight, options, branchParent)
		result.issues = append(result.issues, right.issues...)
		result.output = coerceSetOutput(left.output, right.output, options.Dialect)
		result.namesComplete = left.namesComplete && right.namesComplete
		result.typesComplete = left.typesComplete && right.typesComplete && len(left.output) == len(right.output)
		if setByName(query) {
			var unique bool
			result.output, unique = coerceNamedSetOutput(left.output, right.output, options.Dialect)
			result.namesComplete = result.namesComplete && unique
			result.typesComplete = left.typesComplete && right.typesComplete && result.namesComplete
		}
	}
	return result
}

func analyzeSelectSemanticsWithoutSet(query *SelectStmt, options AnalyzeQueryOptions, parent *semanticScope) semanticQuery {
	if query == nil || query.SetRight == nil {
		return analyzeSelectSemantics(query, options, parent)
	}
	copy := *query
	copy.SetLeft = nil
	copy.SetRight = nil
	copy.SetOperator = ""
	return analyzeSelectSemantics(&copy, options, parent)
}

func semanticRelations(query *SelectStmt, options AnalyzeQueryOptions, scope *semanticScope, issues *[]semanticIssue) []semanticRelation {
	var relations []semanticRelation
	var collectItem func(FromItem) []semanticRelation
	collectItem = func(item FromItem) []semanticRelation {
		switch value := item.(type) {
		case *TableName:
			name := identifiersText(value.Parts)
			alias := optionalIdentifierText(value.Alias)
			if columns, ok := scope.ctes[strings.ToLower(lastIdentifier(name))]; ok {
				return []semanticRelation{{name: name, alias: alias, kind: "cte", columns: cloneSemanticColumns(columns), columnsKnown: true}}
			}
			table, ok := findSemanticSchemaTable(options.Schema, name)
			if !ok {
				return []semanticRelation{{name: name, alias: alias, kind: "table"}}
			}
			columns := make([]semanticColumn, 0, len(table.Columns))
			for _, column := range table.Columns {
				parsed, err := ParseDataType(column.Type, options.Dialect)
				if err != nil || strings.TrimSpace(column.Type) == "" {
					parsed = DataType{Kind: DataTypeUnknown}
				}
				nullability := nullabilityUnknown
				if column.Nullable != nil {
					if *column.Nullable {
						nullability = nullabilityNullable
					} else {
						nullability = nullabilityNonNull
					}
				}
				columns = append(columns, semanticColumn{name: column.Name, dataType: parsed, nullability: nullability})
			}
			return []semanticRelation{{name: name, alias: alias, kind: "table", columns: columns, columnsKnown: true}}
		case *SubqueryFrom:
			child := analyzeSelectSemantics(value.Query, options, scope)
			*issues = append(*issues, child.issues...)
			return []semanticRelation{{name: optionalIdentifierText(value.Alias), alias: optionalIdentifierText(value.Alias), kind: "derived", columns: cloneSemanticColumns(child.output), columnsKnown: child.namesComplete}}
		case *GroupedFrom:
			var grouped []semanticRelation
			for index := range value.Items {
				grouped = append(grouped, collectTableExpression(&value.Items[index], collectItem)...)
			}
			return grouped
		case *TableFunctionFrom:
			return []semanticRelation{semanticTableFunction(value, options, scope, issues)}
		case *RawFrom:
			return []semanticRelation{{name: value.Raw, alias: optionalIdentifierText(value.Alias), kind: "raw"}}
		default:
			return nil
		}
	}
	for index := range query.From {
		table := &query.From[index]
		left := collectItem(table.Primary)
		relations = append(relations, left...)
		scope.relations = relations
		for _, join := range table.Joins {
			before := len(relations)
			right := collectItem(join.Right)
			switch join.Kind {
			case JoinLeft:
				markSemanticRelationsNullable(right)
			case JoinRight:
				markSemanticRelationsNullable(relations[:before])
			case JoinFull:
				markSemanticRelationsNullable(relations[:before])
				markSemanticRelationsNullable(right)
			}
			relations = append(relations, right...)
			scope.relations = relations
		}
	}
	return relations
}

type semanticStructPath struct {
	relation  semanticRelation
	rootIndex int
}

func normalizeDuckDBStructFieldReferences(query *SelectStmt, options AnalyzeQueryOptions) {
	if query == nil || options.Schema == nil || options.Dialect != DialectDuckDB {
		return
	}
	scope := &semanticScope{
		ctes:    make(map[string][]semanticColumn),
		dialect: options.Dialect,
		schema:  options.Schema,
	}
	for _, cte := range query.With {
		child := analyzeSelectSemantics(cte.Query, options, scope)
		columns := cloneSemanticColumns(child.output)
		if len(cte.Columns) > 0 {
			for index := range columns {
				if index < len(cte.Columns) {
					columns[index].name = cte.Columns[index].Text
				}
			}
		}
		scope.ctes[strings.ToLower(cte.Name.Text)] = columns
	}
	var issues []semanticIssue
	scope.relations = semanticRelations(query, options, scope, &issues)

	Transform(query, func(node Node) Node {
		identifier, ok := node.(*IdentifierExpr)
		if !ok {
			return node
		}
		path, ok := resolveSemanticStructPath(identifier, scope)
		if !ok {
			return node
		}

		rootParts := append([]Identifier(nil), identifier.Parts[:path.rootIndex+1]...)
		if path.rootIndex == 0 {
			qualifier := path.relation.alias
			if qualifier == "" {
				qualifier = path.relation.name
			}
			if parts, err := builderIdentifiers(qualifier); err == nil && len(parts) > 0 {
				rootParts = append(parts, rootParts...)
			}
		}
		var expression Expr = &IdentifierExpr{
			nodeBase: nodeBase{span: identifier.SourceSpan()},
			Parts:    rootParts,
		}
		for _, field := range identifier.Parts[path.rootIndex+1:] {
			expression = &FieldExpr{
				nodeBase: nodeBase{span: identifier.SourceSpan()},
				Target:   expression,
				Field:    field,
			}
		}
		return expression
	})
}

func resolveSemanticStructPath(value *IdentifierExpr, scope *semanticScope) (semanticStructPath, bool) {
	if value == nil || len(value.Parts) < 2 {
		return semanticStructPath{}, false
	}
	for current := scope; current != nil; current = current.parent {
		for rootIndex := len(value.Parts) - 2; rootIndex >= 0; rootIndex-- {
			qualifier := identifiersText(value.Parts[:rootIndex])
			rootName := value.Parts[rootIndex].Text
			matches := make([]semanticStructPath, 0, 1)
			for _, relation := range current.relations {
				if qualifier != "" && !semanticRelationMatches(relation, qualifier) {
					continue
				}
				for _, column := range relation.columns {
					if !strings.EqualFold(column.name, rootName) {
						continue
					}
					if _, ok := semanticStructFieldType(column.dataType, value.Parts[rootIndex+1:]); ok {
						matches = append(matches, semanticStructPath{relation: relation, rootIndex: rootIndex})
					}
				}
			}
			if len(matches) == 1 {
				return matches[0], true
			}
			if len(matches) > 1 {
				return semanticStructPath{}, false
			}
		}
	}
	return semanticStructPath{}, false
}

func semanticStructFieldType(dataType DataType, fields []Identifier) (DataType, bool) {
	current := dataType
	for _, fieldName := range fields {
		if current.Kind != DataTypeStruct {
			return DataType{}, false
		}
		found := false
		for _, field := range current.Fields {
			if strings.EqualFold(field.Name, fieldName.Text) {
				current = field.Type
				found = true
				break
			}
		}
		if !found {
			return DataType{}, false
		}
	}
	return current, true
}

func collectTableExpression(table *TableExpr, collect func(FromItem) []semanticRelation) []semanticRelation {
	if table == nil {
		return nil
	}
	result := collect(table.Primary)
	for _, join := range table.Joins {
		before := len(result)
		right := collect(join.Right)
		switch join.Kind {
		case JoinLeft:
			markSemanticRelationsNullable(right)
		case JoinRight:
			markSemanticRelationsNullable(result[:before])
		case JoinFull:
			markSemanticRelationsNullable(result[:before])
			markSemanticRelationsNullable(right)
		}
		result = append(result, right...)
	}
	return result
}

func markSemanticRelationsNullable(relations []semanticRelation) {
	for index := range relations {
		relations[index].nullable = true
	}
}

func semanticTableFunction(value *TableFunctionFrom, options AnalyzeQueryOptions, scope *semanticScope, issues *[]semanticIssue) semanticRelation {
	name := identifiersText(value.Name)
	alias := optionalIdentifierText(value.Alias)
	result := semanticRelation{name: name, alias: alias, kind: "table_function"}
	columnNames := value.Columns
	functionName := strings.ToUpper(lastIdentifier(name))
	var columns []semanticColumn
	switch functionName {
	case "RANGE":
		columns = []semanticColumn{{name: "range", dataType: DataType{Kind: DataTypeBigInt}, nullability: nullabilityNonNull}}
	case "GENERATE_SERIES":
		columns = []semanticColumn{{name: "generate_series", dataType: DataType{Kind: DataTypeBigInt}, nullability: nullabilityNonNull}}
	case "UNNEST", "EXPLODE":
		inferred := inferredExpression{dataType: DataType{Kind: DataTypeUnknown}, nullability: nullabilityUnknown}
		if len(value.Args) > 0 {
			inferred = inferSemanticExpression(value.Args[0], scope, issues)
			if inferred.dataType.Kind == DataTypeArray || inferred.dataType.Kind == DataTypeList {
				if inferred.dataType.Element != nil {
					inferred.dataType = *inferred.dataType.Element
				}
			}
		}
		columns = []semanticColumn{{name: strings.ToLower(functionName), dataType: inferred.dataType, nullability: inferred.nullability}}
	}
	for index := range columnNames {
		if index < len(columns) {
			columns[index].name = columnNames[index].Text
		} else {
			columns = append(columns, semanticColumn{name: columnNames[index].Text, dataType: DataType{Kind: DataTypeUnknown}, nullability: nullabilityUnknown})
		}
	}
	result.columns = columns
	result.columnsKnown = len(columns) > 0
	return result
}

func semanticStarColumns(projection SelectItem, scope *semanticScope) ([]semanticColumn, bool) {
	qualifier := starQualifier(projection.Expr)
	complete := true
	var columns []semanticColumn
	for _, relation := range scope.relations {
		if qualifier != "" && !semanticRelationMatches(relation, qualifier) {
			continue
		}
		if !relation.columnsKnown {
			complete = false
		}
		for _, column := range relation.columns {
			if semanticProjectionExcludes(projection, column.name) {
				continue
			}
			if relation.nullable {
				column.nullability = nullabilityNullable
			}
			columns = appendSemanticColumn(columns, column, scope.dialect)
		}
	}
	for _, replacement := range projection.Replace {
		name := projectionName(replacement)
		if name == "" {
			continue
		}
		inferred := inferSemanticExpression(replacement.Expr, scope, nil)
		column := semanticColumn{name: name, quoted: projectionQuoted(replacement), dataType: inferred.dataType, nullability: inferred.nullability}
		replaced := false
		for index := range columns {
			if strings.EqualFold(columns[index].name, name) {
				columns[index] = column
				replaced = true
				break
			}
		}
		if !replaced {
			columns = append(columns, column)
		}
	}
	if qualifier != "" {
		matched := false
		for _, relation := range scope.relations {
			matched = matched || semanticRelationMatches(relation, qualifier)
		}
		complete = complete && matched
	}
	return columns, complete
}

func semanticProjectionExcludes(projection SelectItem, name string) bool {
	for _, expression := range projection.Except {
		identifier, ok := expression.(*IdentifierExpr)
		if ok && len(identifier.Parts) > 0 && strings.EqualFold(identifier.Parts[len(identifier.Parts)-1].Text, name) {
			return true
		}
	}
	return false
}

func inferSemanticExpression(expression Expr, scope *semanticScope, issues *[]semanticIssue) inferredExpression {
	unknown := inferredExpression{dataType: DataType{Kind: DataTypeUnknown}, nullability: nullabilityUnknown}
	if expression == nil {
		return unknown
	}
	switch value := expression.(type) {
	case *AliasExpr:
		return inferSemanticExpression(value.Expr, scope, issues)
	case *ParenthesizedExpr:
		return inferSemanticExpression(value.Expr, scope, issues)
	case *WindowedExpr:
		if fn, ok := value.Expr.(*FunctionCallExpr); ok {
			copy := *fn
			copy.Over = &value.Over
			return inferSemanticFunction(&copy, scope, issues)
		}
		return inferSemanticExpression(value.Expr, scope, issues)
	case *IdentifierExpr:
		return resolveSemanticIdentifier(value, scope)
	case *LiteralExpr:
		return inferSemanticLiteral(value)
	case *TypedLiteralExpr:
		parsed := dataTypeForName(identifiersText(value.TypeName))
		return inferredExpression{dataType: parsed, nullability: nullabilityNonNull}
	case *UnaryExpr:
		operand := inferSemanticExpression(value.Expr, scope, issues)
		if strings.EqualFold(value.Operator, "NOT") {
			operand.dataType = DataType{Kind: DataTypeBoolean}
		}
		if value.Operator == "-" && operand.integerLiteral != nil {
			literal := -*operand.integerLiteral
			operand.integerLiteral = &literal
		}
		return operand
	case *BinaryExpr:
		return inferSemanticBinary(value, scope, issues)
	case *InExpr:
		base := inferSemanticExpression(value.Value, scope, issues)
		for _, item := range value.Items {
			other := inferSemanticExpression(item, scope, issues)
			base.nullability = combineNullability(base.nullability, other.nullability)
		}
		if value.Query != nil {
			child := analyzeSelectSemantics(value.Query, AnalyzeQueryOptions{Dialect: scope.dialect, Schema: scope.schema, catalog: scope.catalog}, scope)
			if issues != nil {
				*issues = append(*issues, child.issues...)
			}
			if len(child.output) > 0 {
				base.nullability = combineNullability(base.nullability, child.output[0].nullability)
			}
		}
		base.dataType = DataType{Kind: DataTypeBoolean}
		base.integerLiteral = nil
		return base
	case *BetweenExpr:
		base := inferSemanticExpression(value.Value, scope, issues)
		low := inferSemanticExpression(value.Low, scope, issues)
		high := inferSemanticExpression(value.High, scope, issues)
		return inferredExpression{dataType: DataType{Kind: DataTypeBoolean}, nullability: combineNullability(base.nullability, low.nullability, high.nullability)}
	case *ExistsExpr:
		child := analyzeSelectSemantics(value.Query, AnalyzeQueryOptions{Dialect: scope.dialect, Schema: scope.schema, catalog: scope.catalog}, scope)
		if issues != nil {
			*issues = append(*issues, child.issues...)
		}
		return inferredExpression{dataType: DataType{Kind: DataTypeBoolean}, nullability: nullabilityNonNull}
	case *IsExpr:
		inferSemanticExpression(value.Value, scope, issues)
		inferSemanticExpression(value.Right, scope, issues)
		return inferredExpression{dataType: DataType{Kind: DataTypeBoolean}, nullability: nullabilityNonNull}
	case *FunctionCallExpr:
		return inferSemanticFunction(value, scope, issues)
	case *CallExpr:
		if len(value.Args) > 0 {
			return inferSemanticExpression(value.Args[0], scope, issues)
		}
		return unknown
	case *CastExpr:
		typeSQL := builderExprSQL(value.Type)
		parsed, err := ParseDataType(typeSQL, scope.dialect)
		if err != nil {
			parsed = DataType{Kind: DataTypeUnknown}
		}
		inner := inferSemanticExpression(value.Value, scope, issues)
		inner.dataType = parsed
		inner.integerLiteral = nil
		if strings.Contains(strings.ToUpper(value.Keyword), "TRY") || strings.Contains(value.Operator, "!") {
			inner.nullability = nullabilityNullable
		}
		return inner
	case *CaseExpr:
		inferSemanticExpression(value.Operand, scope, issues)
		result := unknown
		first := true
		for _, branch := range value.Whens {
			inferSemanticExpression(branch.Condition, scope, issues)
			candidate := inferSemanticExpression(branch.Result, scope, issues)
			if first {
				result = candidate
				first = false
			} else {
				result = coerceSemanticExpressions(result, candidate, scope.dialect, "")
			}
		}
		if value.Else != nil {
			candidate := inferSemanticExpression(value.Else, scope, issues)
			if first {
				result = candidate
			} else {
				result = coerceSemanticExpressions(result, candidate, scope.dialect, "")
			}
		} else {
			result.nullability = nullabilityNullable
		}
		return result
	case *ExtractExpr:
		return inferredExpression{dataType: DataType{Kind: DataTypeInteger}, nullability: inferSemanticExpression(value.Source, scope, issues).nullability}
	case *IntervalExpr:
		return inferredExpression{dataType: DataType{Kind: DataTypeInterval}, nullability: inferSemanticExpression(value.Value, scope, issues).nullability}
	case *SubqueryExpr:
		child := analyzeSelectSemantics(value.Query, AnalyzeQueryOptions{Dialect: scope.dialect, Schema: scope.schema, catalog: scope.catalog}, scope)
		if issues != nil {
			*issues = append(*issues, child.issues...)
		}
		if len(child.output) > 0 {
			return inferredExpression{dataType: child.output[0].dataType, nullability: child.output[0].nullability}
		}
		return unknown
	case *IndexExpr:
		base := inferSemanticExpression(value.Target, scope, issues)
		if (base.dataType.Kind == DataTypeArray || base.dataType.Kind == DataTypeList) && base.dataType.Element != nil {
			base.dataType = *base.dataType.Element
		} else if base.dataType.Kind == DataTypeMap && base.dataType.Value != nil {
			base.dataType = *base.dataType.Value
		} else if base.dataType.Kind == DataTypeJSON {
			base.dataType = DataType{Kind: DataTypeJSON}
		} else {
			base.dataType = DataType{Kind: DataTypeUnknown}
		}
		return base
	case *FieldExpr:
		base := inferSemanticExpression(value.Target, scope, issues)
		if base.dataType.Kind == DataTypeStruct {
			for _, field := range base.dataType.Fields {
				if strings.EqualFold(field.Name, value.Field.Text) {
					base.dataType = field.Type
					return base
				}
			}
		}
		base.dataType = DataType{Kind: DataTypeUnknown}
		return base
	case *TupleExpr:
		return unknown
	default:
		return unknown
	}
}

func inferSemanticLiteral(value *LiteralExpr) inferredExpression {
	result := inferredExpression{nullability: nullabilityNonNull}
	switch value.KindValue {
	case LiteralString:
		result.dataType = DataType{Kind: DataTypeString}
	case LiteralNumber:
		if strings.ContainsAny(value.Raw, ".eE") {
			result.dataType = DataType{Kind: DataTypeDouble}
		} else if number, err := strconv.ParseInt(strings.TrimSpace(value.Raw), 10, 64); err == nil {
			result.integerLiteral = &number
			if number >= -2147483648 && number <= 2147483647 {
				result.dataType = DataType{Kind: DataTypeInteger}
			} else {
				result.dataType = DataType{Kind: DataTypeBigInt}
			}
		} else {
			result.dataType = DataType{Kind: DataTypeDecimal}
		}
	case LiteralBoolean:
		result.dataType = DataType{Kind: DataTypeBoolean}
	case LiteralNull:
		result.dataType = DataType{Kind: DataTypeUnknown}
		result.nullability = nullabilityNullable
	default:
		result.dataType = DataType{Kind: DataTypeUnknown}
		result.nullability = nullabilityUnknown
	}
	return result
}

func inferSemanticBinary(value *BinaryExpr, scope *semanticScope, issues *[]semanticIssue) inferredExpression {
	left := inferSemanticExpression(value.Left, scope, issues)
	right := inferSemanticExpression(value.Right, scope, issues)
	operator := strings.ToUpper(strings.TrimSpace(value.Operator))
	switch operator {
	case "=", "==", "!=", "<>", "<", "<=", ">", ">=", "LIKE", "ILIKE", "RLIKE", "REGEXP", "AND", "OR", "IS", "IS NOT", "IS DISTINCT FROM", "IS NOT DISTINCT FROM":
		return inferredExpression{dataType: DataType{Kind: DataTypeBoolean}, nullability: combineNullability(left.nullability, right.nullability)}
	case "||":
		return inferredExpression{dataType: DataType{Kind: DataTypeString}, nullability: combineNullability(left.nullability, right.nullability)}
	case "+", "-", "*", "/", "%", "MOD":
		if left.dataType.Known() && right.dataType.Known() && !semanticArithmeticCompatible(left.dataType, right.dataType, operator) && issues != nil {
			*issues = append(*issues, semanticIssue{
				code:    "E210",
				message: "Arithmetic operation expects numeric-compatible operands, found " + semanticTypeFamilyName(left.dataType) + " and " + semanticTypeFamilyName(right.dataType),
				span:    value.SourceSpan(),
			})
		}
		return coerceSemanticExpressions(left, right, scope.dialect, operator)
	case "->":
		return inferredExpression{dataType: DataType{Kind: DataTypeJSON}, nullability: combineNullability(left.nullability, right.nullability)}
	case "->>":
		return inferredExpression{dataType: DataType{Kind: DataTypeString}, nullability: combineNullability(left.nullability, right.nullability)}
	default:
		return coerceSemanticExpressions(left, right, scope.dialect, operator)
	}
}

func inferSemanticFunction(value *FunctionCallExpr, scope *semanticScope, issues *[]semanticIssue) inferredExpression {
	inferSemanticExpression(value.Filter, scope, issues)
	inferSemanticExpression(value.Having, scope, issues)
	for _, order := range value.OrderBy {
		inferSemanticExpression(order.Expr, scope, issues)
	}
	for _, order := range value.WithinGroup {
		inferSemanticExpression(order.Expr, scope, issues)
	}
	if value.Over != nil {
		for _, partition := range value.Over.PartitionBy {
			inferSemanticExpression(partition, scope, issues)
		}
		for _, order := range value.Over.OrderBy {
			inferSemanticExpression(order.Expr, scope, issues)
		}
	}
	name := strings.ToUpper(lastIdentifier(identifiersText(value.Name)))
	args := make([]inferredExpression, 0, len(value.Args))
	for index, argument := range value.Args {
		parameters, body, lambda := lambdaParts(argument)
		if lambda && lambdaArgument(value, index) {
			child := *scope
			child.locals = make(map[string]inferredExpression)
			for name, local := range scope.locals {
				child.locals[name] = local
			}
			for parameterIndex, parameter := range parameters {
				local := inferredExpression{dataType: DataType{Kind: DataTypeUnknown}, nullability: nullabilityUnknown}
				arrayIndex := parameterIndex
				if index == 0 {
					arrayIndex++
				}
				if arrayIndex < len(value.Args) {
					array := inferSemanticExpression(value.Args[arrayIndex], scope, issues)
					if array.dataType.Element != nil {
						local.dataType = *array.dataType.Element
					}
				}
				child.locals[identifierKey(parameter, scope.dialect)] = local
				if lambda, ok := argument.(*LambdaExpr); ok && parameterIndex < len(lambda.Parameters) {
					if declared, err := ParseDataType(lambda.Parameters[parameterIndex].Type, scope.dialect); err == nil {
						local.dataType = declared
						child.locals[identifierKey(parameter, scope.dialect)] = local
					}
				}
			}
			args = append(args, inferSemanticExpression(body, &child, issues))
		} else {
			args = append(args, inferSemanticExpression(argument, scope, issues))
		}
	}
	arg := func(index int) inferredExpression {
		if index >= 0 && index < len(args) {
			return args[index]
		}
		return inferredExpression{dataType: DataType{Kind: DataTypeUnknown}, nullability: nullabilityUnknown}
	}
	if inferred, handled := scope.catalog.infer(value, args, issues); handled {
		return inferred
	}
	known := func(kind DataTypeKind, nullable string) inferredExpression {
		return inferredExpression{dataType: DataType{Kind: kind}, nullability: nullable}
	}
	if scope.dialect == DialectDuckDB && name == "EPOCH" && len(value.Name) == 1 {
		return known(DataTypeDouble, combinedArgumentNullability(args))
	}
	switch name {
	case "TRANSFORM", "LIST_TRANSFORM", "LIST_APPLY", "ARRAY_APPLY", "ARRAY_TRANSFORM", "ZIP_WITH", "ARRAYMAP":
		index := len(args) - 1
		if name == "ARRAYMAP" {
			index = 0
		}
		element := arg(index).dataType
		return inferredExpression{dataType: DataType{Kind: DataTypeArray, Element: &element}, nullability: arg(0).nullability}
	case "FILTER", "LIST_FILTER", "ARRAY_FILTER":
		return arg(0)
	case "COUNT", "COUNT_IF", "ROW_NUMBER", "RANK", "DENSE_RANK", "NTILE":
		return known(DataTypeBigInt, nullabilityNonNull)
	case "SUM", "SUM_IF":
		result := arg(0)
		switch result.dataType.Kind {
		case DataTypeTinyInt, DataTypeSmallInt, DataTypeInteger:
			if scope.dialect == DialectDuckDB {
				result.dataType = DataType{Kind: DataTypeHugeInt}
			} else {
				result.dataType = DataType{Kind: DataTypeBigInt}
			}
		case DataTypeBigInt:
			if scope.dialect == DialectDuckDB {
				result.dataType = DataType{Kind: DataTypeHugeInt}
			}
		case DataTypeFloat:
			result.dataType = DataType{Kind: DataTypeDouble}
		case DataTypeDecimal:
			if scope.dialect == DialectDuckDB {
				precision := 38
				result.dataType.Precision = &precision
			}
		case DataTypeHugeInt, DataTypeDouble:
		default:
			result.dataType = DataType{Kind: DataTypeDecimal}
		}
		result.nullability = nullabilityUnknown
		return result
	case "AVG", "STDDEV", "STDDEV_POP", "STDDEV_SAMP", "VARIANCE", "VAR_POP", "VAR_SAMP":
		return known(DataTypeDouble, nullabilityUnknown)
	case "MIN", "MAX":
		result := arg(0)
		if scope.dialect == DialectDuckDB && len(args) == 2 && result.dataType.Known() {
			element := result.dataType
			result.dataType = DataType{Kind: DataTypeArray, Element: &element}
		}
		result.nullability = nullabilityUnknown
		return result
	case "FIRST", "LAST", "FIRST_VALUE", "LAST_VALUE", "ANY_VALUE", "MEDIAN", "PERCENTILE_CONT", "PERCENTILE_DISC", "ARG_MAX", "ARG_MIN", "ARGMAX", "ARGMIN", "MAX_BY", "MIN_BY", "ARG_MAX_NULL", "ARG_MIN_NULL":
		result := arg(0)
		result.nullability = nullabilityUnknown
		return result
	case "UPPER", "LOWER", "TRIM", "LTRIM", "RTRIM", "SUBSTRING", "SUBSTR", "REPLACE", "CONCAT", "CONCAT_WS", "STRING_AGG", "GROUP_CONCAT", "LISTAGG", "DATE_FORMAT", "FORMAT_DATE", "TIME_TO_STR", "TO_CHAR":
		return known(DataTypeString, combinedArgumentNullability(args))
	case "LENGTH", "CHAR_LENGTH", "OCTET_LENGTH", "YEAR", "MONTH", "DAY", "HOUR", "MINUTE", "SECOND", "DATE_DIFF", "DATEDIFF", "EXTRACT":
		return known(DataTypeInteger, combinedArgumentNullability(args))
	case "NOW", "CURRENT_TIMESTAMP", "LOCALTIMESTAMP", "TO_TIMESTAMP":
		return known(DataTypeTimestamp, combinedArgumentNullability(args))
	case "CURRENT_DATE", "DATE", "TO_DATE", "DATE_ADD", "DATE_SUB":
		return known(DataTypeDate, combinedArgumentNullability(args))
	case "CURRENT_TIME":
		return known(DataTypeTime, combinedArgumentNullability(args))
	case "ABS", "SIGN":
		return arg(0)
	case "FLOOR", "CEIL", "CEILING":
		result := arg(0)
		if isSemanticInteger(result.dataType) {
			result.dataType = DataType{Kind: DataTypeDouble}
		}
		return result
	case "ROUND":
		result := arg(0)
		if !result.dataType.Known() {
			result.dataType = DataType{Kind: DataTypeDouble}
		}
		return result
	case "SQRT", "CBRT", "POWER", "POW", "LOG", "LN", "EXP":
		return known(DataTypeDouble, combinedArgumentNullability(args))
	case "BOOL_AND", "BOOL_OR", "EVERY":
		return known(DataTypeBoolean, nullabilityUnknown)
	case "COALESCE", "IFNULL", "NVL", "GREATEST", "LEAST":
		result := inferredExpression{dataType: DataType{Kind: DataTypeUnknown}, nullability: nullabilityNullable}
		for _, candidate := range args {
			result = coerceSemanticExpressions(result, candidate, scope.dialect, "")
			if candidate.nullability == nullabilityNonNull {
				result.nullability = nullabilityNonNull
			}
		}
		return result
	case "NULLIF":
		result := arg(0)
		result.nullability = nullabilityNullable
		return result
	case "IF", "IIF", "IFF":
		return coerceSemanticExpressions(arg(1), arg(2), scope.dialect, "")
	case "ARRAY_AGG", "LIST", "ARRAY", "ARRAY_CONSTRUCT":
		element := arg(0).dataType
		return inferredExpression{dataType: DataType{Kind: DataTypeArray, Element: &element}, nullability: nullabilityUnknown}
	case "RANGE", "GENERATE_SERIES":
		element := DataType{Kind: DataTypeBigInt}
		return inferredExpression{dataType: DataType{Kind: DataTypeList, Element: &element}, nullability: nullabilityNonNull}
	case "UNNEST", "EXPLODE":
		result := arg(0)
		if (result.dataType.Kind == DataTypeArray || result.dataType.Kind == DataTypeList) && result.dataType.Element != nil {
			result.dataType = *result.dataType.Element
		}
		result.nullability = nullabilityUnknown
		return result
	case "JSON", "PARSE_JSON", "JSON_PARSE", "JSON_OBJECT", "JSON_ARRAY":
		return known(DataTypeJSON, combinedArgumentNullability(args))
	default:
		result := arg(0)
		result.nullability = nullabilityUnknown
		return result
	}
}

func resolveSemanticIdentifier(value *IdentifierExpr, scope *semanticScope) inferredExpression {
	if value == nil || len(value.Parts) == 0 || value.Parts[len(value.Parts)-1].Text == "*" {
		return inferredExpression{dataType: DataType{Kind: DataTypeUnknown}, nullability: nullabilityUnknown}
	}
	if local, ok := scope.locals[identifierKey(value.Parts[0], scope.dialect)]; ok {
		for _, field := range value.Parts[1:] {
			found := false
			for _, candidate := range local.dataType.Fields {
				if strings.EqualFold(candidate.Name, field.Text) {
					local.dataType = candidate.Type
					found = true
					break
				}
			}
			if !found {
				local.dataType = DataType{Kind: DataTypeUnknown}
				break
			}
		}
		return local
	}
	columnName := value.Parts[len(value.Parts)-1].Text
	qualifier := ""
	if len(value.Parts) > 1 {
		qualifier = identifiersText(value.Parts[:len(value.Parts)-1])
	} else {
		switch strings.ToUpper(columnName) {
		case "CURRENT_DATE":
			return inferredExpression{dataType: DataType{Kind: DataTypeDate}, nullability: nullabilityNonNull}
		case "CURRENT_TIME", "LOCALTIME":
			return inferredExpression{dataType: DataType{Kind: DataTypeTime}, nullability: nullabilityNonNull}
		case "CURRENT_TIMESTAMP", "LOCALTIMESTAMP":
			return inferredExpression{dataType: DataType{Kind: DataTypeTimestamp}, nullability: nullabilityNonNull}
		}
	}
	for current := scope; current != nil; current = current.parent {
		var matches []semanticColumn
		for _, relation := range current.relations {
			if qualifier != "" && !semanticRelationMatches(relation, qualifier) {
				continue
			}
			for _, column := range relation.columns {
				matchesName := strings.EqualFold(column.name, columnName)
				if relation.kind == "cte" || relation.kind == "derived" {
					matchesName = identifierKey(Identifier{Text: column.name, Quoted: column.quoted}, scope.dialect) == identifierKey(value.Parts[len(value.Parts)-1], scope.dialect)
				}
				if !matchesName {
					continue
				}
				if relation.nullable {
					column.nullability = nullabilityNullable
				}
				matches = append(matches, column)
			}
		}
		if len(matches) == 1 {
			return inferredExpression{dataType: matches[0].dataType, nullability: normalizedNullability(matches[0].nullability), hasColumn: true}
		}
		if len(matches) > 1 {
			return inferredExpression{dataType: DataType{Kind: DataTypeUnknown}, nullability: nullabilityUnknown}
		}
		if qualifier == "" {
			if alias, ok := current.aliases[identifierKey(value.Parts[0], current.dialect)]; ok {
				return alias
			}
		}
		if qualifier != "" {
			for _, relation := range current.relations {
				if semanticRelationMatches(relation, qualifier) {
					// A known local alias shadows an outer alias even when its
					// schema does not contain the requested column.
					return inferredExpression{dataType: DataType{Kind: DataTypeUnknown}, nullability: nullabilityUnknown, hasColumn: true}
				}
			}
		}
	}
	return inferredExpression{dataType: DataType{Kind: DataTypeUnknown}, nullability: nullabilityUnknown}
}

func semanticRelationMatches(relation semanticRelation, qualifier string) bool {
	if relation.alias != "" {
		return strings.EqualFold(relation.alias, qualifier)
	}
	return strings.EqualFold(relation.name, qualifier) || strings.EqualFold(lastIdentifier(relation.name), qualifier)
}

func coerceSemanticExpressions(left, right inferredExpression, dialect Dialect, operator string) inferredExpression {
	result := inferredExpression{nullability: combineNullability(left.nullability, right.nullability), hasColumn: left.hasColumn || right.hasColumn}
	if !left.dataType.Known() {
		result.dataType = right.dataType
		result.integerLiteral = right.integerLiteral
		return result
	}
	if !right.dataType.Known() {
		result.dataType = left.dataType
		result.integerLiteral = left.integerLiteral
		return result
	}
	if semanticDataTypesEqual(left.dataType, right.dataType) {
		result.dataType = left.dataType
		return result
	}
	if (operator == "+" || operator == "-") && ((isSemanticTemporal(left.dataType) && right.dataType.Kind == DataTypeInterval) || (isSemanticTemporal(right.dataType) && left.dataType.Kind == DataTypeInterval)) {
		if isSemanticTemporal(left.dataType) {
			result.dataType = left.dataType
		} else {
			result.dataType = right.dataType
		}
		return result
	}
	if operator == "*" && ((isSemanticNumeric(left.dataType) && right.dataType.Kind == DataTypeInterval) || (left.dataType.Kind == DataTypeInterval && isSemanticNumeric(right.dataType))) {
		result.dataType = DataType{Kind: DataTypeInterval}
		return result
	}
	if operator == "/" && left.dataType.Kind == DataTypeInterval && isSemanticNumeric(right.dataType) {
		result.dataType = DataType{Kind: DataTypeInterval}
		return result
	}
	if isSemanticNumeric(left.dataType) && isSemanticNumeric(right.dataType) {
		if dialect == DialectDuckDB {
			if floating, ok := duckDBFloatingDecimalType(left.dataType, right.dataType); ok {
				result.dataType = floating
				return result
			}
			if left.hasColumn && right.integerLiteral != nil && integerFitsSemanticType(*right.integerLiteral, left.dataType) {
				result.dataType = left.dataType
				return result
			}
			if right.hasColumn && left.integerLiteral != nil && integerFitsSemanticType(*left.integerLiteral, right.dataType) {
				result.dataType = right.dataType
				return result
			}
		}
		if operator == "/" && dialect != DialectPostgreSQL && isSemanticInteger(left.dataType) && isSemanticInteger(right.dataType) {
			result.dataType = DataType{Kind: DataTypeDouble}
			return result
		}
		if semanticNumericRank(left.dataType) >= semanticNumericRank(right.dataType) {
			result.dataType = left.dataType
		} else {
			result.dataType = right.dataType
		}
		return result
	}
	if left.dataType.Kind == DataTypeString && right.dataType.Kind == DataTypeString {
		result.dataType = left.dataType
		return result
	}
	result.dataType = left.dataType
	return result
}

func duckDBFloatingDecimalType(left, right DataType) (DataType, bool) {
	if left.Kind == DataTypeDecimal && (right.Kind == DataTypeFloat || right.Kind == DataTypeDouble) {
		return right, true
	}
	if right.Kind == DataTypeDecimal && (left.Kind == DataTypeFloat || left.Kind == DataTypeDouble) {
		return left, true
	}
	return DataType{}, false
}

func semanticDataTypesEqual(left, right DataType) bool {
	if left.Kind != right.Kind || left.Name != right.Name || left.WithTimezone != right.WithTimezone || !equalOptionalInt(left.Length, right.Length) || !equalOptionalInt(left.Precision, right.Precision) || !equalOptionalInt(left.Scale, right.Scale) || len(left.Arguments) != len(right.Arguments) || len(left.Fields) != len(right.Fields) {
		return false
	}
	for index := range left.Arguments {
		if left.Arguments[index] != right.Arguments[index] {
			return false
		}
	}
	if !equalOptionalDataType(left.Element, right.Element) || !equalOptionalDataType(left.Key, right.Key) || !equalOptionalDataType(left.Value, right.Value) {
		return false
	}
	for index := range left.Fields {
		if left.Fields[index].Name != right.Fields[index].Name || !semanticDataTypesEqual(left.Fields[index].Type, right.Fields[index].Type) {
			return false
		}
	}
	return true
}

func equalOptionalInt(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalOptionalDataType(left, right *DataType) bool {
	return left == nil && right == nil || left != nil && right != nil && semanticDataTypesEqual(*left, *right)
}

func coerceSetOutput(left, right []semanticColumn, dialect Dialect) []semanticColumn {
	result := cloneSemanticColumns(left)
	for index := range result {
		if index >= len(right) {
			break
		}
		coerced := coerceSemanticExpressions(
			inferredExpression{dataType: result[index].dataType, nullability: result[index].nullability},
			inferredExpression{dataType: right[index].dataType, nullability: right[index].nullability},
			dialect,
			"",
		)
		result[index].dataType = coerced.dataType
		result[index].nullability = coerced.nullability
	}
	return result
}

func semanticArithmeticCompatible(left, right DataType, operator string) bool {
	if isSemanticNumeric(left) && isSemanticNumeric(right) {
		return true
	}
	if (operator == "+" || operator == "-") && ((isSemanticTemporal(left) && (isSemanticNumeric(right) || right.Kind == DataTypeInterval)) || (isSemanticTemporal(right) && (isSemanticNumeric(left) || left.Kind == DataTypeInterval))) {
		return true
	}
	if operator == "*" && ((isSemanticNumeric(left) && right.Kind == DataTypeInterval) || (left.Kind == DataTypeInterval && isSemanticNumeric(right))) {
		return true
	}
	if operator == "/" && left.Kind == DataTypeInterval && isSemanticNumeric(right) {
		return true
	}
	return false
}

func semanticTypeFamilyName(dataType DataType) string {
	switch {
	case isSemanticNumeric(dataType):
		return "numeric"
	case dataType.Kind == DataTypeString:
		return "string"
	case dataType.Kind == DataTypeBoolean:
		return "boolean"
	case isSemanticTemporal(dataType):
		return "temporal"
	default:
		return string(dataType.Kind)
	}
}

func isSemanticNumeric(dataType DataType) bool {
	switch dataType.Kind {
	case DataTypeTinyInt, DataTypeSmallInt, DataTypeInteger, DataTypeBigInt, DataTypeHugeInt, DataTypeFloat, DataTypeDouble, DataTypeDecimal:
		return true
	default:
		return false
	}
}

func isSemanticInteger(dataType DataType) bool {
	switch dataType.Kind {
	case DataTypeTinyInt, DataTypeSmallInt, DataTypeInteger, DataTypeBigInt, DataTypeHugeInt:
		return true
	default:
		return false
	}
}

func isSemanticTemporal(dataType DataType) bool {
	return dataType.Kind == DataTypeDate || dataType.Kind == DataTypeTime || dataType.Kind == DataTypeTimestamp
}

func semanticNumericRank(dataType DataType) int {
	switch dataType.Kind {
	case DataTypeTinyInt:
		return 1
	case DataTypeSmallInt:
		return 2
	case DataTypeInteger:
		return 3
	case DataTypeBigInt:
		return 4
	case DataTypeHugeInt:
		return 5
	case DataTypeFloat:
		return 6
	case DataTypeDouble:
		return 7
	case DataTypeDecimal:
		return 8
	default:
		return 0
	}
}

func integerFitsSemanticType(value int64, dataType DataType) bool {
	switch dataType.Kind {
	case DataTypeTinyInt:
		return value >= -128 && value <= 127
	case DataTypeSmallInt:
		return value >= -32768 && value <= 32767
	case DataTypeInteger:
		return value >= -2147483648 && value <= 2147483647
	case DataTypeBigInt, DataTypeHugeInt:
		return true
	default:
		return false
	}
}

func combinedArgumentNullability(args []inferredExpression) string {
	values := make([]string, 0, len(args))
	for _, argument := range args {
		values = append(values, argument.nullability)
	}
	return combineNullability(values...)
}

func combineNullability(values ...string) string {
	unknown := false
	for _, value := range values {
		switch normalizedNullability(value) {
		case nullabilityNullable:
			return nullabilityNullable
		case nullabilityUnknown:
			unknown = true
		}
	}
	if unknown {
		return nullabilityUnknown
	}
	return nullabilityNonNull
}

func normalizedNullability(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case nullabilityNullable:
		return nullabilityNullable
	case nullabilityNonNull:
		return nullabilityNonNull
	default:
		return nullabilityUnknown
	}
}

func findSemanticSchemaTable(schema *ValidationSchema, name string) (SchemaTable, bool) {
	if schema == nil {
		return SchemaTable{}, false
	}
	for _, table := range schema.Tables {
		for _, candidate := range semanticSchemaTableNames(table) {
			if strings.EqualFold(candidate, name) {
				return table, true
			}
		}
	}
	short := lastIdentifier(name)
	var found SchemaTable
	matches := 0
	for _, table := range schema.Tables {
		for _, candidate := range semanticSchemaTableNames(table) {
			if strings.EqualFold(lastIdentifier(candidate), short) {
				found = table
				matches++
				break
			}
		}
	}
	return found, matches == 1
}

func semanticSchemaTableNames(table SchemaTable) []string {
	result := []string{table.Name}
	if table.Schema != "" {
		result = append(result, table.Schema+"."+table.Name)
	}
	return append(result, table.Aliases...)
}

func appendSemanticColumn(columns []semanticColumn, column semanticColumn, dialect Dialect) []semanticColumn {
	for index := range columns {
		if identifierKey(Identifier{Text: columns[index].name, Quoted: columns[index].quoted}, dialect) == identifierKey(Identifier{Text: column.name, Quoted: column.quoted}, dialect) {
			return columns
		}
	}
	return append(columns, column)
}

func cloneSemanticColumns(columns []semanticColumn) []semanticColumn {
	return append([]semanticColumn(nil), columns...)
}
