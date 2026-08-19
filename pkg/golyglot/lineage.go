package golyglot

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// SetOperator identifies a set-operation branch in a lineage graph.
type SetOperator string

const (
	SetOperatorUnion     SetOperator = "union"
	SetOperatorIntersect SetOperator = "intersect"
	SetOperatorExcept    SetOperator = "except"
)

type SetBranch struct {
	Operator SetOperator `json:"operator"`
	Ordinal  int         `json:"ordinal"`
	All      bool        `json:"all"`
}

// LineageNode is a column dependency graph node. Expression and Source are
// JSON strings containing generated SQL so the result is serializable without
// exposing parser-internal pointer layouts.
type LineageNode struct {
	Name              string          `json:"name"`
	Expression        json.RawMessage `json:"expression"`
	Source            json.RawMessage `json:"source"`
	Downstream        []LineageNode   `json:"downstream"`
	SourceName        string          `json:"source_name"`
	SourceKind        string          `json:"source_kind"`
	SourceAlias       *string         `json:"source_alias,omitempty"`
	SetBranch         *SetBranch      `json:"set_branch,omitempty"`
	ReferenceNodeName string          `json:"reference_node_name"`
}

// Walk returns this node followed by all downstream nodes in depth-first
// source order.
func (node LineageNode) Walk() []LineageNode {
	result := make([]LineageNode, 0)
	var visit func(LineageNode)
	visit = func(value LineageNode) {
		result = append(result, value)
		for _, child := range value.Downstream {
			visit(child)
		}
	}
	visit(node)
	return result
}

// DownstreamNames returns the immediate dependency names.
func (node LineageNode) DownstreamNames() []string {
	result := make([]string, 0, len(node.Downstream))
	for _, child := range node.Downstream {
		result = append(result, child.Name)
	}
	return result
}

type OutputColumnKind string

const (
	OutputColumnNamed    OutputColumnKind = "named"
	OutputColumnUnnamed  OutputColumnKind = "unnamed"
	OutputColumnWildcard OutputColumnKind = "wildcard"
)

type OutputColumn struct {
	Kind         OutputColumnKind `json:"kind"`
	Name         *string          `json:"name,omitempty"`
	Ordinal      *int             `json:"ordinal"`
	Qualifier    *string          `json:"qualifier,omitempty"`
	StartOrdinal *int             `json:"startOrdinal,omitempty"`
}

type QueryOutput struct {
	Columns         []OutputColumn `json:"columns"`
	OrdinalComplete bool           `json:"ordinalComplete"`
}

// Lineage builds a dependency graph for a named output column.
func Lineage(column, sql string, dialect Dialect) (LineageNode, error) {
	return lineageFor(sql, dialect, column, -1, nil)
}

// LineageAt builds a dependency graph for a zero-based output ordinal.
func LineageAt(ordinal int, sql string, dialect Dialect) (LineageNode, error) {
	if ordinal < 0 {
		return LineageNode{}, fmt.Errorf("golyglot: output ordinal must be non-negative")
	}
	return lineageFor(sql, dialect, "", ordinal, nil)
}

// LineageWithSchema resolves unqualified columns and wildcard projections
// with supplied schema metadata.
func LineageWithSchema(column, sql string, schema ValidationSchema, dialect Dialect) (LineageNode, error) {
	return lineageFor(sql, dialect, column, -1, &schema)
}

// LineageAtWithSchema is the schema-aware ordinal form of LineageAt.
func LineageAtWithSchema(ordinal int, sql string, schema ValidationSchema, dialect Dialect) (LineageNode, error) {
	if ordinal < 0 {
		return LineageNode{}, fmt.Errorf("golyglot: output ordinal must be non-negative")
	}
	return lineageFor(sql, dialect, "", ordinal, &schema)
}

// OutputColumns describes the ordered result columns of a query.
func OutputColumns(sql string, dialect Dialect) (QueryOutput, error) {
	return outputColumnsFor(sql, dialect, nil)
}

// OutputColumnsWithSchema expands known wildcard columns using schema data.
func OutputColumnsWithSchema(sql string, schema ValidationSchema, dialect Dialect) (QueryOutput, error) {
	return outputColumnsFor(sql, dialect, &schema)
}

// SourceTables returns the distinct input relations contributing to a named
// output column.
func SourceTables(column, sql string, dialect Dialect) ([]string, error) {
	node, err := Lineage(column, sql, dialect)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var result []string
	for _, value := range node.Walk() {
		if value.SourceName == "" || seen[value.SourceName] {
			continue
		}
		seen[value.SourceName] = true
		result = append(result, value.SourceName)
	}
	return result, nil
}

func lineageFor(sql string, dialect Dialect, column string, ordinal int, schema *ValidationSchema) (LineageNode, error) {
	query, err := lineageQuery(sql, dialect)
	if err != nil {
		return LineageNode{}, err
	}
	output, err := outputColumnsForQuery(query, schema)
	if err != nil {
		return LineageNode{}, err
	}
	selected := ordinal
	if selected < 0 {
		selected = -1
		for index, projection := range query.Projections {
			if projectionName(projection) == column {
				selected = index
				break
			}
		}
		if selected < 0 {
			for index, projection := range query.Projections {
				if strings.EqualFold(projectionName(projection), column) {
					selected = index
					break
				}
			}
		}
		if selected < 0 && len(query.Projections) == 1 {
			selected = 0
		}
	}
	if selected < 0 || selected >= len(query.Projections) {
		return LineageNode{}, fmt.Errorf("column %q is not an output of the query", column)
	}
	name := projectionName(query.Projections[selected])
	if name == "" {
		if selected < len(output.Columns) && output.Columns[selected].Name != nil {
			name = *output.Columns[selected].Name
		} else {
			name = fmt.Sprintf("_%d", selected)
		}
	}
	root := LineageNode{
		Name:       name,
		Expression: lineageSQLJSON(expressionSQL(query.Projections[selected].Expr, dialect)),
		Source:     lineageSQLJSON(sql),
		SourceKind: "root",
	}
	if query.SetRight != nil {
		left := query
		if query.SetLeft != nil {
			left = query.SetLeft
		}
		branches := []*SelectStmt{left, query.SetRight}
		operator := lineageSetOperator(query.SetOperator)
		for index, branch := range branches {
			if selected >= len(branch.Projections) {
				continue
			}
			branchNode := lineageForProjection(branch, selected, sql, schema)
			branchNode.SetBranch = &SetBranch{Operator: operator, Ordinal: index, All: query.SetAll}
			root.Downstream = append(root.Downstream, branchNode)
		}
		return root, nil
	}
	root.Downstream = lineageChildren(query.Projections[selected].Expr, query, sql, schema)
	return root, nil
}

func lineageQuery(sql string, dialect Dialect) (*SelectStmt, error) {
	result, err := ParseStrict(sql, dialect)
	if err != nil {
		return nil, err
	}
	if len(result.Statements) != 1 {
		return nil, fmt.Errorf("lineage expects exactly one statement, found %d", len(result.Statements))
	}
	switch node := result.Statements[0].Node.(type) {
	case *SelectStmt:
		return node, nil
	case *InsertStmt:
		if node.Query != nil {
			return node.Query, nil
		}
	default:
		return nil, fmt.Errorf("lineage requires a SELECT query, found %s", result.Statements[0].Node.Kind())
	}
	return nil, fmt.Errorf("lineage query is empty")
}

func lineageForProjection(query *SelectStmt, index int, sourceSQL string, schema *ValidationSchema) LineageNode {
	projection := query.Projections[index]
	name := projectionName(projection)
	if name == "" {
		name = fmt.Sprintf("_%d", index)
	}
	node := LineageNode{
		Name:       name,
		Expression: lineageSQLJSON(expressionSQL(projection.Expr, DialectGeneric)),
		Source:     lineageSQLJSON(sourceSQL),
		SourceKind: "branch",
	}
	node.Downstream = lineageChildren(projection.Expr, query, sourceSQL, schema)
	return node
}

func lineageChildren(expression Expr, query *SelectStmt, sourceSQL string, schema *ValidationSchema) []LineageNode {
	if expression == nil {
		return nil
	}
	relations := relationFacts(query, schema)
	var result []LineageNode
	seen := make(map[string]bool)
	for _, reference := range Columns(expression) {
		relation := lineageRelationForReference(reference, relations, schema)
		name := reference.Column
		sourceName := ""
		sourceKind := "unknown"
		var sourceAlias *string
		if relation != nil {
			sourceName = relation.Name
			sourceKind = relation.Kind
			if relation.Alias != nil {
				alias := *relation.Alias
				sourceAlias = &alias
			}
			if relation.Alias != nil {
				name = *relation.Alias + "." + reference.Column
			} else if relation.Name != "" {
				name = relation.Name + "." + reference.Column
			}
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		child := LineageNode{
			Name:              name,
			Expression:        lineageSQLJSON(reference.Column),
			Source:            lineageSQLJSON(sourceSQL),
			SourceName:        sourceName,
			SourceKind:        sourceKind,
			SourceAlias:       sourceAlias,
			ReferenceNodeName: name,
		}
		if relation != nil {
			child.Downstream = lineageReferenceChildren(reference.Column, *relation, query, sourceSQL, schema)
		}
		result = append(result, child)
	}
	return result
}

func lineageReferenceChildren(column string, relation RelationFact, query *SelectStmt, sourceSQL string, schema *ValidationSchema) []LineageNode {
	if relation.Kind != "derived" && relation.Kind != "cte" {
		return nil
	}
	childQuery := relationQuery(query, relation)
	if childQuery == nil {
		return nil
	}
	for _, projection := range childQuery.Projections {
		if !strings.EqualFold(projectionName(projection), column) {
			continue
		}
		return lineageChildren(projection.Expr, childQuery, sourceSQL, schema)
	}
	return nil
}

func relationQuery(query *SelectStmt, relation RelationFact) *SelectStmt {
	if query == nil {
		return nil
	}
	for _, cte := range query.With {
		if strings.EqualFold(cte.Name.Text, relation.Name) && cte.Query != nil {
			return cte.Query
		}
	}
	var find func([]TableExpr) *SelectStmt
	find = func(tables []TableExpr) *SelectStmt {
		for _, table := range tables {
			if subquery, ok := table.Primary.(*SubqueryFrom); ok && subquery.Query != nil && (relation.Alias == nil || strings.EqualFold(optionalIdentifierText(subquery.Alias), *relation.Alias)) {
				return subquery.Query
			}
			for _, join := range table.Joins {
				if subquery, ok := join.Right.(*SubqueryFrom); ok && subquery.Query != nil && (relation.Alias == nil || strings.EqualFold(optionalIdentifierText(subquery.Alias), *relation.Alias)) {
					return subquery.Query
				}
			}
		}
		return nil
	}
	return find(query.From)
}

func lineageRelationForReference(reference ColumnReference, relations []RelationFact, schema *ValidationSchema) *RelationFact {
	for index := range relations {
		relation := &relations[index]
		if reference.Table != "" && (strings.EqualFold(reference.Table, relation.Name) || (relation.Alias != nil && strings.EqualFold(reference.Table, *relation.Alias)) || (relation.Table != nil && strings.EqualFold(reference.Table, *relation.Table))) {
			return relation
		}
	}
	if reference.Table == "" {
		var match *RelationFact
		for index := range relations {
			relation := &relations[index]
			if len(relation.Columns) == 0 {
				continue
			}
			for _, column := range relation.Columns {
				if strings.EqualFold(column, reference.Column) {
					if match != nil {
						return nil
					}
					match = relation
				}
			}
		}
		if match != nil {
			return match
		}
		if len(relations) == 1 {
			return &relations[0]
		}
	}
	return nil
}

func outputColumnsFor(sql string, dialect Dialect, schema *ValidationSchema) (QueryOutput, error) {
	query, err := lineageQuery(sql, dialect)
	if err != nil {
		return QueryOutput{}, err
	}
	return outputColumnsForQuery(query, schema)
}

func outputColumnsForQuery(query *SelectStmt, schema *ValidationSchema) (QueryOutput, error) {
	if query == nil {
		return QueryOutput{}, fmt.Errorf("query is empty")
	}
	if query.SetLeft != nil {
		return outputColumnsForQuery(query.SetLeft, schema)
	}
	result := QueryOutput{OrdinalComplete: true}
	ordinal := 0
	relations := relationFacts(query, schema)
	for _, projection := range query.Projections {
		if projection.Expr == nil {
			result.OrdinalComplete = false
			result.Columns = append(result.Columns, OutputColumn{Kind: OutputColumnUnnamed})
			continue
		}
		if isStarExpression(projection.Expr) {
			qualifier := starQualifier(projection.Expr)
			columns := []string(nil)
			if qualifier != "" {
				columns = schemaColumnsForRelation(relations, qualifier)
			} else {
				for _, relation := range relations {
					columns = append(columns, relation.Columns...)
				}
			}
			if len(columns) > 0 {
				for _, column := range columns {
					name := column
					position := ordinal
					result.Columns = append(result.Columns, OutputColumn{Kind: OutputColumnNamed, Name: &name, Ordinal: &position})
					ordinal++
				}
				continue
			}
			var qualifierPointer *string
			if qualifier != "" {
				qualifierPointer = &qualifier
			}
			start := ordinal
			result.Columns = append(result.Columns, OutputColumn{Kind: OutputColumnWildcard, Qualifier: qualifierPointer, StartOrdinal: &start})
			result.OrdinalComplete = false
			continue
		}
		name := projectionName(projection)
		position := ordinal
		if name == "" {
			result.Columns = append(result.Columns, OutputColumn{Kind: OutputColumnUnnamed, Ordinal: &position})
		} else {
			result.Columns = append(result.Columns, OutputColumn{Kind: OutputColumnNamed, Name: &name, Ordinal: &position})
		}
		ordinal++
	}
	return result, nil
}

func projectionName(projection SelectItem) string {
	if projection.Alias != nil {
		return projection.Alias.Text
	}
	return expressionOutputName(projection.Expr)
}

func expressionSQL(expression Expr, dialect Dialect) string {
	if expression == nil {
		return ""
	}
	text, err := GenerateWithOptions(&ExpressionStmt{Expr: expression}, GenerateOptions{Canonical: true, Dialect: dialect})
	if err != nil {
		return ""
	}
	return text
}

func lineageSQLJSON(text string) json.RawMessage {
	encoded, _ := json.Marshal(text)
	return json.RawMessage(encoded)
}

func lineageSetOperator(operator string) SetOperator {
	switch strings.ToLower(operator) {
	case "intersect":
		return SetOperatorIntersect
	case "except":
		return SetOperatorExcept
	default:
		return SetOperatorUnion
	}
}

// OpenLineage-compatible constants and payload types.
const (
	OpenLineageSchemaURL        = "https://openlineage.io/spec/2-0-2/OpenLineage.json"
	ColumnLineageFacetSchemaURL = "https://openlineage.io/spec/facets/1-2-0/ColumnLineageDatasetFacet.json"
	SQLJobFacetSchemaURL        = "https://openlineage.io/spec/facets/1-1-0/SQLJobFacet.json"
	JobTypeJobFacetSchemaURL    = "https://openlineage.io/spec/facets/2-0-3/JobTypeJobFacet.json"
	SchemaDatasetFacetSchemaURL = "https://openlineage.io/spec/facets/1-2-0/SchemaDatasetFacet.json"
)

type OpenLineageDatasetID struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

func NewOpenLineageDatasetID(namespace, name string) OpenLineageDatasetID {
	return OpenLineageDatasetID{Namespace: namespace, Name: name}
}

type OpenLineageRunEventType string

const (
	OpenLineageRunEventStart    OpenLineageRunEventType = "START"
	OpenLineageRunEventRunning  OpenLineageRunEventType = "RUNNING"
	OpenLineageRunEventComplete OpenLineageRunEventType = "COMPLETE"
	OpenLineageRunEventAbort    OpenLineageRunEventType = "ABORT"
	OpenLineageRunEventFail     OpenLineageRunEventType = "FAIL"
	OpenLineageRunEventOther    OpenLineageRunEventType = "OTHER"
)

type OpenLineageOptions struct {
	Dialect          Dialect                         `json:"dialect,omitempty"`
	Producer         string                          `json:"producer"`
	DatasetNamespace string                          `json:"datasetNamespace,omitempty"`
	DatasetMappings  map[string]OpenLineageDatasetID `json:"datasetMappings,omitempty"`
	OutputDataset    *OpenLineageDatasetID           `json:"outputDataset,omitempty"`
	Schema           *ValidationSchema               `json:"schema,omitempty"`
	JobNamespace     string                          `json:"jobNamespace,omitempty"`
	JobName          string                          `json:"jobName,omitempty"`
	EventTime        string                          `json:"eventTime,omitempty"`
	RunID            string                          `json:"runId,omitempty"`
	EventType        OpenLineageRunEventType         `json:"eventType,omitempty"`
}

type OpenLineageWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type OpenLineageTransformation struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	Description string `json:"description,omitempty"`
	Masking     *bool  `json:"masking,omitempty"`
}

type OpenLineageInputField struct {
	Namespace       string                      `json:"namespace"`
	Name            string                      `json:"name"`
	Field           string                      `json:"field"`
	Transformations []OpenLineageTransformation `json:"transformations,omitempty"`
}

type OpenLineageColumnLineageField struct {
	InputFields []OpenLineageInputField `json:"inputFields"`
}

type OpenLineageColumnLineageFacet struct {
	Producer  string                                   `json:"_producer"`
	SchemaURL string                                   `json:"_schemaURL"`
	Fields    map[string]OpenLineageColumnLineageField `json:"fields"`
}

type OpenLineageDataset struct {
	Namespace string                     `json:"namespace"`
	Name      string                     `json:"name"`
	Facets    map[string]json.RawMessage `json:"facets,omitempty"`
}

type OpenLineageColumnLineageResult struct {
	Facet    OpenLineageColumnLineageFacet `json:"facet"`
	Inputs   []OpenLineageDataset          `json:"inputs"`
	Outputs  []OpenLineageDataset          `json:"outputs"`
	Warnings []OpenLineageWarning          `json:"warnings"`
}

type OpenLineageEventResult struct {
	Event    json.RawMessage      `json:"event"`
	Warnings []OpenLineageWarning `json:"warnings"`
}

// OpenLineageColumnLineage produces a columnLineage facet and inferred input
// and output datasets.
func OpenLineageColumnLineage(sql string, options OpenLineageOptions) (OpenLineageColumnLineageResult, error) {
	if strings.TrimSpace(options.Producer) == "" {
		return OpenLineageColumnLineageResult{}, fmt.Errorf("missing required option: producer")
	}
	if strings.TrimSpace(string(options.Dialect)) == "" {
		options.Dialect = DialectGeneric
	}
	query, err := lineageQuery(sql, options.Dialect)
	if err != nil {
		return OpenLineageColumnLineageResult{}, err
	}
	output, err := outputColumnsForQuery(query, options.Schema)
	if err != nil {
		return OpenLineageColumnLineageResult{}, err
	}
	outputDataset := options.OutputDataset
	warnings := make([]OpenLineageWarning, 0)
	if outputDataset == nil {
		if strings.TrimSpace(options.DatasetNamespace) == "" {
			return OpenLineageColumnLineageResult{}, fmt.Errorf("missing required option: outputDataset or datasetNamespace")
		}
		outputDataset = &OpenLineageDatasetID{Namespace: options.DatasetNamespace, Name: "query-output"}
		warnings = append(warnings, OpenLineageWarning{Code: "W_DEFAULT_OUTPUT_DATASET", Message: "outputDataset was not supplied; using query-output"})
	}
	fields := make(map[string]OpenLineageColumnLineageField)
	inputSet := make(map[string]OpenLineageDataset)
	for index, outputColumn := range output.Columns {
		name := fmt.Sprintf("_%d", index)
		if outputColumn.Name != nil {
			name = *outputColumn.Name
		}
		lineage, lineageErr := LineageAtWithSchema(index, sql, schemaValue(options.Schema), options.Dialect)
		if options.Schema == nil {
			lineage, lineageErr = LineageAt(index, sql, options.Dialect)
		}
		if lineageErr != nil {
			warnings = append(warnings, OpenLineageWarning{Code: "W_UNRESOLVED_OUTPUT_FIELD", Message: lineageErr.Error()})
			fields[name] = OpenLineageColumnLineageField{}
			continue
		}
		inputFields := make([]OpenLineageInputField, 0)
		for _, node := range lineage.Walk()[1:] {
			if node.SourceName == "" || len(node.Downstream) != 0 {
				continue
			}
			dataset := openLineageDataset(node.SourceName, options)
			key := dataset.Namespace + "\x00" + dataset.Name
			inputSet[key] = dataset
			transformation := OpenLineageTransformation{Type: "DIRECT", Subtype: "IDENTITY"}
			if len(lineage.Downstream) > 0 {
				transformation = OpenLineageTransformation{Type: "TRANSFORMATION", Subtype: "SQL"}
			}
			inputFields = append(inputFields, OpenLineageInputField{Namespace: dataset.Namespace, Name: dataset.Name, Field: lastIdentifier(node.Name), Transformations: []OpenLineageTransformation{transformation}})
		}
		fields[name] = OpenLineageColumnLineageField{InputFields: inputFields}
	}
	inputKeys := make([]string, 0, len(inputSet))
	for key := range inputSet {
		inputKeys = append(inputKeys, key)
	}
	sort.Strings(inputKeys)
	inputs := make([]OpenLineageDataset, 0, len(inputKeys))
	for _, key := range inputKeys {
		inputs = append(inputs, inputSet[key])
	}
	outputs := []OpenLineageDataset{{Namespace: outputDataset.Namespace, Name: outputDataset.Name}}
	return OpenLineageColumnLineageResult{
		Facet:    OpenLineageColumnLineageFacet{Producer: options.Producer, SchemaURL: ColumnLineageFacetSchemaURL, Fields: fields},
		Inputs:   inputs,
		Outputs:  outputs,
		Warnings: warnings,
	}, nil
}

// OpenLineageJobEvent creates an OpenLineage JobEvent payload.
func OpenLineageJobEvent(sql string, options OpenLineageOptions) (OpenLineageEventResult, error) {
	if options.JobNamespace == "" || options.JobName == "" || options.EventTime == "" {
		return OpenLineageEventResult{}, fmt.Errorf("jobNamespace, jobName, and eventTime are required")
	}
	result, err := OpenLineageColumnLineage(sql, options)
	if err != nil {
		return OpenLineageEventResult{}, err
	}
	event := map[string]any{
		"eventTime": options.EventTime,
		"producer":  options.Producer,
		"schemaURL": OpenLineageSchemaURL,
		"job":       map[string]any{"namespace": options.JobNamespace, "name": options.JobName, "facets": map[string]any{"sql": map[string]any{"_producer": options.Producer, "_schemaURL": SQLJobFacetSchemaURL, "query": sql}}},
		"inputs":    result.Inputs,
		"outputs":   result.Outputs,
	}
	data, _ := json.Marshal(event)
	return OpenLineageEventResult{Event: data, Warnings: result.Warnings}, nil
}

// OpenLineageRunEvent creates an OpenLineage RunEvent payload.
func OpenLineageRunEvent(sql string, options OpenLineageOptions) (OpenLineageEventResult, error) {
	if options.RunID == "" || options.EventType == "" {
		return OpenLineageEventResult{}, fmt.Errorf("runId and eventType are required")
	}
	if options.JobNamespace == "" || options.JobName == "" || options.EventTime == "" {
		return OpenLineageEventResult{}, fmt.Errorf("jobNamespace, jobName, and eventTime are required")
	}
	result, err := OpenLineageColumnLineage(sql, options)
	if err != nil {
		return OpenLineageEventResult{}, err
	}
	event := map[string]any{
		"eventTime": options.EventTime,
		"eventType": options.EventType,
		"producer":  options.Producer,
		"schemaURL": OpenLineageSchemaURL,
		"run":       map[string]any{"runId": options.RunID, "facets": map[string]any{}},
		"job":       map[string]any{"namespace": options.JobNamespace, "name": options.JobName, "facets": map[string]any{"sql": map[string]any{"_producer": options.Producer, "_schemaURL": SQLJobFacetSchemaURL, "query": sql}}},
		"inputs":    result.Inputs,
		"outputs":   result.Outputs,
	}
	data, _ := json.Marshal(event)
	return OpenLineageEventResult{Event: data, Warnings: result.Warnings}, nil
}

func openLineageDataset(sourceName string, options OpenLineageOptions) OpenLineageDataset {
	if options.DatasetMappings != nil {
		for key, value := range options.DatasetMappings {
			if strings.EqualFold(key, sourceName) || strings.EqualFold(lastIdentifier(key), lastIdentifier(sourceName)) {
				return OpenLineageDataset{Namespace: value.Namespace, Name: value.Name}
			}
		}
	}
	namespace := options.DatasetNamespace
	if namespace == "" {
		namespace = "urn:golyglot:dataset"
	}
	return OpenLineageDataset{Namespace: namespace, Name: sourceName}
}

func schemaValue(schema *ValidationSchema) ValidationSchema {
	if schema == nil {
		return ValidationSchema{}
	}
	return *schema
}
