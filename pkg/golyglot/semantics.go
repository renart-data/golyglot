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
	scope := &semanticScope{parent: parent, ctes: make(map[string][]semanticColumn), dialect: options.Dialect, schema: options.Schema}
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
				}
			}
		}
		scope.ctes[strings.ToLower(cte.Name.Text)] = columns
		result.issues = append(result.issues, child.issues...)
	}
	scope.relations = semanticRelations(query, options, scope, &result.issues)

	for index, projection := range query.Projections {
		inferred := inferSemanticExpression(projection.Expr, scope, &result.issues)
		result.projections = append(result.projections, inferred)
		if isStarExpression(projection.Expr) {
			columns, complete := semanticStarColumns(projection, scope)
			result.stars[index] = cloneSemanticColumns(columns)
			if !complete {
				result.namesComplete = false
				result.typesComplete = false
			}
			for _, column := range columns {
				result.output = appendSemanticColumn(result.output, column)
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
		column := semanticColumn{name: name, dataType: inferred.dataType, nullability: normalizedNullability(inferred.nullability)}
		result.output = appendSemanticColumn(result.output, column)
		if !column.dataType.Known() {
			result.typesComplete = false
		}
	}
	if len(query.Projections) == 0 {
		result.namesComplete = false
		result.typesComplete = false
	}

	if query.SetRight != nil {
		leftQuery := query
		if query.SetLeft != nil {
			leftQuery = query.SetLeft
		}
		left := analyzeSelectSemanticsWithoutSet(leftQuery, options, parent)
		right := analyzeSelectSemantics(query.SetRight, options, parent)
		result.issues = append(result.issues, left.issues...)
		result.issues = append(result.issues, right.issues...)
		result.output = coerceSetOutput(left.output, right.output, options.Dialect)
		result.namesComplete = left.namesComplete
		result.typesComplete = left.typesComplete && right.typesComplete && len(left.output) == len(right.output)
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
		relations = append(relations, collectTableExpression(&query.From[index], collectItem)...)
	}
	return relations
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
			columns = appendSemanticColumn(columns, column)
		}
	}
	for _, replacement := range projection.Replace {
		name := projectionName(replacement)
		if name == "" {
			continue
		}
		inferred := inferSemanticExpression(replacement.Expr, scope, nil)
		column := semanticColumn{name: name, dataType: inferred.dataType, nullability: inferred.nullability}
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
		base.dataType = DataType{Kind: DataTypeBoolean}
		base.integerLiteral = nil
		return base
	case *BetweenExpr:
		base := inferSemanticExpression(value.Value, scope, issues)
		low := inferSemanticExpression(value.Low, scope, issues)
		high := inferSemanticExpression(value.High, scope, issues)
		return inferredExpression{dataType: DataType{Kind: DataTypeBoolean}, nullability: combineNullability(base.nullability, low.nullability, high.nullability)}
	case *IsExpr, *ExistsExpr:
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
		result := unknown
		first := true
		for _, branch := range value.Whens {
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
		child := analyzeSelectSemantics(value.Query, AnalyzeQueryOptions{Dialect: scope.dialect, Schema: scope.schema}, scope)
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
	name := strings.ToUpper(lastIdentifier(identifiersText(value.Name)))
	args := make([]inferredExpression, 0, len(value.Args))
	for _, argument := range value.Args {
		args = append(args, inferSemanticExpression(argument, scope, issues))
	}
	arg := func(index int) inferredExpression {
		if index >= 0 && index < len(args) {
			return args[index]
		}
		return inferredExpression{dataType: DataType{Kind: DataTypeUnknown}, nullability: nullabilityUnknown}
	}
	known := func(kind DataTypeKind, nullable string) inferredExpression {
		return inferredExpression{dataType: DataType{Kind: kind}, nullability: nullable}
	}
	switch name {
	case "COUNT", "COUNT_IF", "ROW_NUMBER", "RANK", "DENSE_RANK", "NTILE":
		return known(DataTypeBigInt, nullabilityNonNull)
	case "SUM", "SUM_IF":
		result := arg(0)
		switch result.dataType.Kind {
		case DataTypeTinyInt, DataTypeSmallInt, DataTypeInteger:
			result.dataType = DataType{Kind: DataTypeBigInt}
		case DataTypeFloat:
			result.dataType = DataType{Kind: DataTypeDouble}
		case DataTypeBigInt, DataTypeHugeInt, DataTypeDouble, DataTypeDecimal:
		default:
			result.dataType = DataType{Kind: DataTypeDecimal}
		}
		result.nullability = nullabilityUnknown
		return result
	case "AVG", "STDDEV", "STDDEV_POP", "STDDEV_SAMP", "VARIANCE", "VAR_POP", "VAR_SAMP":
		return known(DataTypeDouble, nullabilityUnknown)
	case "MIN", "MAX", "FIRST", "LAST", "FIRST_VALUE", "LAST_VALUE", "ANY_VALUE", "MEDIAN", "PERCENTILE_CONT", "PERCENTILE_DISC":
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
	case "ARRAY_AGG", "LIST", "ARRAY":
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
				if !strings.EqualFold(column.name, columnName) {
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
		if qualifier != "" {
			for _, relation := range current.relations {
				if semanticRelationMatches(relation, qualifier) && !relation.columnsKnown {
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

func appendSemanticColumn(columns []semanticColumn, column semanticColumn) []semanticColumn {
	for index := range columns {
		if strings.EqualFold(columns[index].name, column.name) {
			return columns
		}
	}
	return append(columns, column)
}

func cloneSemanticColumns(columns []semanticColumn) []semanticColumn {
	return append([]semanticColumn(nil), columns...)
}
