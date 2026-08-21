package golyglot

import (
	"fmt"
	"strings"
)

// AnalyzeQueryOptions controls the dialect and optional schema used for
// compact query facts.
type AnalyzeQueryOptions struct {
	Dialect Dialect           `json:"dialect,omitempty"`
	Schema  *ValidationSchema `json:"schema,omitempty"`
}

// QueryAnalysis is intentionally compact: it summarizes query shape,
// projections, relations, CTEs, and set operations without exposing a second
// analysis-specific tree.
type QueryAnalysis struct {
	Shape               string                  `json:"shape"`
	CTEs                []string                `json:"ctes"`
	CTEFacts            []CTEFact               `json:"cteFacts"`
	Projections         []ProjectionFact        `json:"projections"`
	Relations           []RelationFact          `json:"relations"`
	BaseTables          []RelationFact          `json:"baseTables"`
	StarProjections     []StarProjectionFact    `json:"starProjections"`
	SetOperations       []SetOperationFact      `json:"setOperations"`
	OutputColumns       []QueryOutputColumnFact `json:"outputColumns"`
	OutputNamesComplete bool                    `json:"outputNamesComplete"`
	OutputTypesComplete bool                    `json:"outputTypesComplete"`
}

// QueryOutputColumnFact is the schema fact inferred for one concrete output
// column after wildcard expansion and CTE/subquery resolution.
type QueryOutputColumnFact struct {
	Name        string  `json:"name"`
	TypeHint    *string `json:"typeHint,omitempty"`
	Nullability string  `json:"nullability"`
}

type ProjectionFact struct {
	Index             int                    `json:"index"`
	Name              *string                `json:"name"`
	IsStar            bool                   `json:"isStar"`
	StarTable         *string                `json:"starTable"`
	TransformKind     string                 `json:"transformKind"`
	TransformFunction *TransformFunctionFact `json:"transformFunction,omitempty"`
	CastType          *string                `json:"castType"`
	TypeHint          *string                `json:"typeHint"`
	Nullability       string                 `json:"nullability"`
	Upstream          []ColumnReferenceFact  `json:"upstream"`
}

type TransformFunctionFact struct {
	Name        string                `json:"name"`
	LiteralArgs []string              `json:"literalArgs"`
	ColumnArgs  []ColumnReferenceFact `json:"columnArgs"`
}

type CTEFact struct {
	Name          string   `json:"name"`
	Columns       []string `json:"columns"`
	BodySQL       string   `json:"bodySql"`
	OutputColumns []string `json:"outputColumns"`
}

type StarProjectionFact struct {
	Index           int      `json:"index"`
	Table           *string  `json:"table"`
	ExpandedColumns []string `json:"expandedColumns"`
}

type ColumnReferenceFact struct {
	SourceName  *string `json:"sourceName"`
	SourceAlias *string `json:"sourceAlias"`
	SourceKind  string  `json:"sourceKind"`
	Table       *string `json:"table"`
	Column      string  `json:"column"`
	Unqualified bool    `json:"unqualified"`
	Confidence  string  `json:"confidence"`
}

type RelationFact struct {
	Name    string   `json:"name"`
	Alias   *string  `json:"alias"`
	Kind    string   `json:"kind"`
	Columns []string `json:"columns"`
	Catalog *string  `json:"catalog"`
	Schema  *string  `json:"schema"`
	Table   *string  `json:"table"`
}

type SetOperationFact struct {
	Kind          string                   `json:"kind"`
	All           bool                     `json:"all"`
	Distinct      bool                     `json:"distinct"`
	OutputColumns []string                 `json:"outputColumns"`
	Branches      []SetOperationBranchFact `json:"branches"`
}

type SetOperationBranchFact struct {
	Index       int                    `json:"index"`
	Role        SetOperationBranchRole `json:"role"`
	Projections []ProjectionFact       `json:"projections"`
}

type SetOperationBranchRole string

const (
	SetOperationBranchRoleValue  SetOperationBranchRole = "value"
	SetOperationBranchRoleFilter SetOperationBranchRole = "filter"
)

// AnalyzeQuery parses one statement and returns structured query facts.
func AnalyzeQuery(sql string, options AnalyzeQueryOptions) (QueryAnalysis, error) {
	if strings.TrimSpace(string(options.Dialect)) == "" {
		options.Dialect = DialectGeneric
	}
	result, err := ParseStrict(sql, options.Dialect)
	if err != nil {
		return QueryAnalysis{}, err
	}
	if len(result.Statements) != 1 {
		return QueryAnalysis{}, fmt.Errorf("analyze query expects exactly one statement, found %d", len(result.Statements))
	}
	query, ok := result.Statements[0].Node.(*SelectStmt)
	if !ok {
		return QueryAnalysis{}, fmt.Errorf("analyze query requires a SELECT statement, found %s", result.Statements[0].Node.Kind())
	}
	return analyzeSelectFacts(query, options), nil
}

// Analyze is a concise alias for AnalyzeQuery.
func Analyze(sql string, dialect Dialect) (QueryAnalysis, error) {
	return AnalyzeQuery(sql, AnalyzeQueryOptions{Dialect: dialect})
}

func analyzeSelectFacts(selectStmt *SelectStmt, options AnalyzeQueryOptions) QueryAnalysis {
	result := QueryAnalysis{Shape: queryShape(selectStmt)}
	semantics := analyzeSelectSemantics(selectStmt, options, nil)
	for _, cte := range selectStmt.With {
		result.CTEs = append(result.CTEs, cte.Name.Text)
		fact := CTEFact{Name: cte.Name.Text}
		for _, column := range cte.Columns {
			fact.Columns = append(fact.Columns, column.Text)
		}
		if cte.Query != nil {
			fact.BodySQL, _ = GenerateWithOptions(cte.Query, GenerateOptions{Canonical: true, Dialect: options.Dialect})
			fact.OutputColumns = outputNames(cte.Query, options.Schema)
		}
		result.CTEFacts = append(result.CTEFacts, fact)
	}

	result.Projections = projectionFacts(selectStmt, options.Schema)
	for index := range result.Projections {
		if index >= len(semantics.projections) {
			break
		}
		inferred := semantics.projections[index]
		if inferred.dataType.Known() {
			value := inferred.dataType.SQL()
			result.Projections[index].TypeHint = &value
		}
		result.Projections[index].Nullability = inferred.nullability
	}
	result.Relations = relationFacts(selectStmt, options.Schema)
	for _, relation := range result.Relations {
		if relation.Kind == "table" {
			result.BaseTables = append(result.BaseTables, relation)
		}
	}
	for _, projection := range result.Projections {
		if !projection.IsStar {
			continue
		}
		star := StarProjectionFact{Index: projection.Index, Table: projection.StarTable}
		for _, column := range semantics.stars[projection.Index] {
			star.ExpandedColumns = append(star.ExpandedColumns, column.name)
		}
		result.StarProjections = append(result.StarProjections, star)
	}
	result.OutputNamesComplete = semantics.namesComplete
	result.OutputTypesComplete = semantics.typesComplete
	for _, column := range semantics.output {
		fact := QueryOutputColumnFact{Name: column.name, Nullability: column.nullability}
		if column.dataType.Known() {
			value := column.dataType.SQL()
			fact.TypeHint = &value
		}
		result.OutputColumns = append(result.OutputColumns, fact)
	}
	collectSetFacts(selectStmt, &result)
	return result
}

func queryShape(selectStmt *SelectStmt) string {
	if selectStmt == nil {
		return "unknown"
	}
	if selectStmt.SetLeft != nil || selectStmt.SetRight != nil {
		return strings.ToLower(strings.TrimSpace(selectStmt.SetOperator))
	}
	if len(selectStmt.With) > 0 {
		return "select_with_cte"
	}
	return "select"
}

func projectionFacts(selectStmt *SelectStmt, schema *ValidationSchema) []ProjectionFact {
	result := make([]ProjectionFact, 0, len(selectStmt.Projections))
	relations := relationFacts(selectStmt, schema)
	for index, item := range selectStmt.Projections {
		fact := ProjectionFact{Index: index, Nullability: "unknown"}
		expression := item.Expr
		if expression == nil {
			result = append(result, fact)
			continue
		}
		if item.Alias != nil {
			name := item.Alias.Text
			fact.Name = &name
		} else if alias, ok := expression.(*AliasExpr); ok {
			name := alias.Alias.Text
			fact.Name = &name
		} else if name := expressionOutputName(expression); name != "" {
			fact.Name = &name
		}
		fact.TransformKind = expressionTransformKind(expression)
		if isStarExpression(expression) {
			fact.IsStar = true
			if qualifier := starQualifier(expression); qualifier != "" {
				fact.StarTable = &qualifier
			}
		}
		if cast, ok := unwrapAliasExpression(expression).(*CastExpr); ok {
			castType := builderExprSQL(cast.Type)
			fact.CastType = &castType
		}
		fact.Upstream = analysisColumnReferences(expression, relations)
		if function, ok := unwrapAliasExpression(expression).(*FunctionCallExpr); ok {
			functionFact := &TransformFunctionFact{Name: strings.ToUpper(identifiersText(function.Name))}
			for _, argument := range function.Args {
				switch value := argument.(type) {
				case *LiteralExpr:
					functionFact.LiteralArgs = append(functionFact.LiteralArgs, value.Raw)
				default:
					functionFact.ColumnArgs = append(functionFact.ColumnArgs, analysisColumnReferences(argument, relations)...)
				}
			}
			fact.TransformFunction = functionFact
		}
		result = append(result, fact)
	}
	return result
}

func expressionOutputName(expression Expr) string {
	expression = unwrapAliasExpression(expression)
	switch value := expression.(type) {
	case *IdentifierExpr:
		if len(value.Parts) > 0 {
			return value.Parts[len(value.Parts)-1].Text
		}
	case *FunctionCallExpr:
		if len(value.Name) > 0 {
			return value.Name[len(value.Name)-1].Text
		}
	case *CastExpr:
		return expressionOutputName(value.Value)
	}
	return ""
}

func expressionTransformKind(expression Expr) string {
	if isStarExpression(expression) {
		return "star"
	}
	switch unwrapAliasExpression(expression).(type) {
	case *IdentifierExpr:
		return "identity"
	case *FunctionCallExpr:
		return "function"
	case *CastExpr:
		return "cast"
	case *LiteralExpr:
		return "literal"
	default:
		return "expression"
	}
}

func unwrapAliasExpression(expression Expr) Expr {
	for {
		alias, ok := expression.(*AliasExpr)
		if !ok {
			return expression
		}
		expression = alias.Expr
	}
}

func isStarExpression(expression Expr) bool {
	if _, ok := unwrapAliasExpression(expression).(*StarExpr); ok {
		return true
	}
	identifier, ok := unwrapAliasExpression(expression).(*IdentifierExpr)
	return ok && len(identifier.Parts) > 0 && identifier.Parts[len(identifier.Parts)-1].Text == "*"
}

func starQualifier(expression Expr) string {
	identifier, ok := unwrapAliasExpression(expression).(*IdentifierExpr)
	if !ok || len(identifier.Parts) < 2 {
		return ""
	}
	return identifiersText(identifier.Parts[:len(identifier.Parts)-1])
}

func relationFacts(selectStmt *SelectStmt, schema *ValidationSchema) []RelationFact {
	var result []RelationFact
	cteNames := make(map[string]*SelectStmt)
	for _, cte := range selectStmt.With {
		if cte.Query != nil {
			cteNames[strings.ToLower(cte.Name.Text)] = cte.Query
		}
	}
	var collect func(FromItem)
	collect = func(item FromItem) {
		switch value := item.(type) {
		case *TableName:
			name := identifiersText(value.Parts)
			kind := "table"
			fact := relationFactFromName(name, kind, optionalIdentifierText(value.Alias))
			var cteQuery *SelectStmt
			if candidate := cteNames[strings.ToLower(lastIdentifier(name))]; candidate != nil {
				cteQuery = candidate
				fact.Kind = "cte"
				fact.Columns = outputNames(cteQuery, schema)
			} else if table, ok := findSchemaTableValue(schema, name); ok {
				for _, column := range table.Columns {
					fact.Columns = append(fact.Columns, column.Name)
				}
			}
			result = append(result, fact)
			if cteQuery != nil {
				result = append(result, relationFacts(cteQuery, schema)...)
			}
		case *SubqueryFrom:
			name := optionalIdentifierText(value.Alias)
			fact := relationFactFromName(name, "derived", name)
			if value.Query != nil {
				fact.Columns = outputNames(value.Query, schema)
			}
			result = append(result, fact)
			if value.Query != nil {
				result = append(result, relationFacts(value.Query, schema)...)
			}
		case *GroupedFrom:
			for i := range value.Items {
				if value.Items[i].Primary != nil {
					collect(value.Items[i].Primary)
				}
			}
		case *TableFunctionFrom:
			name := identifiersText(value.Name)
			if value.Alias != nil {
				name = value.Alias.Text
			}
			result = append(result, relationFactFromName(name, "table_function", optionalIdentifierText(value.Alias)))
		case *RawFrom:
			result = append(result, relationFactFromName(value.Raw, "raw", optionalIdentifierText(value.Alias)))
		}
	}
	for i := range selectStmt.From {
		if selectStmt.From[i].Primary != nil {
			collect(selectStmt.From[i].Primary)
		}
		for _, join := range selectStmt.From[i].Joins {
			if join.Right != nil {
				collect(join.Right)
			}
		}
	}
	return result
}

func relationFactFromName(name, kind, alias string) RelationFact {
	fact := RelationFact{Name: name, Kind: kind}
	if alias != "" {
		fact.Alias = &alias
	}
	parts := strings.Split(name, ".")
	if len(parts) >= 1 && parts[len(parts)-1] != "" {
		table := parts[len(parts)-1]
		fact.Table = &table
	}
	if len(parts) >= 2 {
		schema := parts[len(parts)-2]
		fact.Schema = &schema
	}
	if len(parts) >= 3 {
		catalog := parts[len(parts)-3]
		fact.Catalog = &catalog
	}
	return fact
}

func findSchemaTableValue(schema *ValidationSchema, name string) (SchemaTable, bool) {
	if schema == nil {
		return SchemaTable{}, false
	}
	return findSchemaTable(*schema, name)
}

func schemaColumnsForRelation(relations []RelationFact, name string) []string {
	for _, relation := range relations {
		if strings.EqualFold(relation.Name, name) || (relation.Alias != nil && strings.EqualFold(*relation.Alias, name)) || (relation.Table != nil && strings.EqualFold(*relation.Table, name)) {
			return append([]string(nil), relation.Columns...)
		}
	}
	return nil
}

func outputNames(selectStmt *SelectStmt, schema *ValidationSchema) []string {
	if selectStmt == nil {
		return nil
	}
	result := make([]string, 0, len(selectStmt.Projections))
	for _, projection := range selectStmt.Projections {
		if projection.Expr == nil {
			continue
		}
		if isStarExpression(projection.Expr) {
			qualifier := starQualifier(projection.Expr)
			if qualifier != "" {
				result = append(result, schemaColumnsForRelation(relationFacts(selectStmt, schema), qualifier)...)
			} else {
				for _, relation := range relationFacts(selectStmt, schema) {
					result = append(result, relation.Columns...)
				}
			}
			continue
		}
		if projection.Alias != nil {
			result = append(result, projection.Alias.Text)
			continue
		}
		if name := expressionOutputName(projection.Expr); name != "" {
			result = append(result, name)
		} else {
			result = append(result, fmt.Sprintf("_%d", len(result)))
		}
	}
	return result
}

func analysisColumnReferences(expression Expr, relations []RelationFact) []ColumnReferenceFact {
	var result []ColumnReferenceFact
	for _, reference := range Columns(expression) {
		fact := ColumnReferenceFact{Column: reference.Column, Unqualified: reference.Table == "", Confidence: "low", SourceKind: "unknown"}
		if reference.Table != "" {
			table := reference.Table
			fact.Table = &table
			fact.Confidence = "high"
		}
		for _, relation := range relations {
			matches := strings.EqualFold(relation.Name, reference.Table) || (relation.Alias != nil && strings.EqualFold(*relation.Alias, reference.Table)) || (relation.Table != nil && strings.EqualFold(*relation.Table, reference.Table))
			if reference.Table == "" && len(relations) == 1 {
				matches = true
				fact.Confidence = "medium"
			}
			if !matches {
				continue
			}
			name := relation.Name
			fact.SourceName = &name
			fact.SourceKind = relation.Kind
			if relation.Alias != nil {
				alias := *relation.Alias
				fact.SourceAlias = &alias
			}
			break
		}
		result = append(result, fact)
	}
	return result
}

func collectSetFacts(selectStmt *SelectStmt, result *QueryAnalysis) {
	if selectStmt == nil || selectStmt.SetRight == nil {
		return
	}
	fact := SetOperationFact{Kind: strings.ToLower(selectStmt.SetOperator), All: selectStmt.SetAll, Distinct: !selectStmt.SetAll}
	left := selectStmt
	if selectStmt.SetLeft != nil {
		left = selectStmt.SetLeft
	}
	fact.OutputColumns = outputNames(left, nil)
	fact.Branches = append(fact.Branches,
		SetOperationBranchFact{Index: 0, Role: SetOperationBranchRoleValue, Projections: projectionFacts(left, nil)},
		SetOperationBranchFact{Index: 1, Role: SetOperationBranchRoleValue, Projections: projectionFacts(selectStmt.SetRight, nil)},
	)
	result.SetOperations = append(result.SetOperations, fact)
	if selectStmt.SetLeft != nil {
		collectSetFacts(selectStmt.SetLeft, result)
	}
	collectSetFacts(selectStmt.SetRight, result)
}
