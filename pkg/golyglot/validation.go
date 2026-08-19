package golyglot

import (
	"fmt"
	"strings"
)

// ValidationOptions controls syntax, semantic, and schema-aware validation.
// Syntax validation is always performed. Semantic checks default to false
// when options are supplied explicitly, allowing callers to select a cheap
// syntax-only pass; Validate without options enables them.
type ValidationOptions struct {
	Dialect      Dialect
	StrictSyntax bool
	Semantic     bool
	Schema       *ValidationSchema
}

// ValidationSchema is a small, dependency-free schema contract used by the
// semantic validator, lineage resolver, and query analyzer.
type ValidationSchema struct {
	Tables []SchemaTable `json:"tables"`
	Strict *bool         `json:"strict,omitempty"`
}

// Schema is a concise alias for ValidationSchema.
type Schema = ValidationSchema

type SchemaColumnReference struct {
	Table  string `json:"table"`
	Column string `json:"column"`
	Schema string `json:"schema,omitempty"`
}

type SchemaTableReference struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
	Schema  string   `json:"schema,omitempty"`
}

type SchemaForeignKey struct {
	Name       string               `json:"name,omitempty"`
	Columns    []string             `json:"columns"`
	References SchemaTableReference `json:"references"`
}

type SchemaColumn struct {
	Name       string                 `json:"name"`
	Type       string                 `json:"type,omitempty"`
	Nullable   *bool                  `json:"nullable,omitempty"`
	PrimaryKey bool                   `json:"primaryKey,omitempty"`
	Unique     bool                   `json:"unique,omitempty"`
	References *SchemaColumnReference `json:"references,omitempty"`
}

type SchemaTable struct {
	Name        string             `json:"name"`
	Schema      string             `json:"schema,omitempty"`
	Columns     []SchemaColumn     `json:"columns"`
	Aliases     []string           `json:"aliases,omitempty"`
	PrimaryKey  []string           `json:"primaryKey,omitempty"`
	UniqueKeys  [][]string         `json:"uniqueKeys,omitempty"`
	ForeignKeys []SchemaForeignKey `json:"foreignKeys,omitempty"`
}

// ValidationError is the stable, serializable form of a validation issue.
type ValidationError struct {
	Message  string `json:"message"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Span     Span   `json:"span"`
	Line     *int   `json:"line,omitempty"`
	Column   *int   `json:"column,omitempty"`
	Start    *int   `json:"start,omitempty"`
	End      *int   `json:"end,omitempty"`
}

// ValidationResult contains all syntax and semantic issues found in one
// document. Warnings do not make Valid false.
type ValidationResult struct {
	Valid       bool              `json:"valid"`
	Errors      []ValidationError `json:"errors"`
	Diagnostics []Diagnostic      `json:"diagnostics,omitempty"`
}

// Validate runs syntax validation and, when no options are supplied, the
// default semantic checks as well.
func Validate(sql string, dialect Dialect, options ...ValidationOptions) ValidationResult {
	if len(options) == 0 {
		return ValidateWithOptions(sql, ValidationOptions{Dialect: dialect, Semantic: true})
	}
	selected := options[0]
	if strings.TrimSpace(string(selected.Dialect)) == "" {
		selected.Dialect = dialect
	}
	return ValidateWithOptions(sql, selected)
}

// ValidateWithSchema performs syntax, semantic, and schema-aware checks.
func ValidateWithSchema(sql string, schema ValidationSchema, dialect Dialect) ValidationResult {
	return ValidateWithOptions(sql, ValidationOptions{
		Dialect:  dialect,
		Semantic: true,
		Schema:   &schema,
	})
}

// ValidateWithOptions is the configurable validation entry point.
func ValidateWithOptions(sql string, options ValidationOptions) ValidationResult {
	if strings.TrimSpace(string(options.Dialect)) == "" {
		options.Dialect = DialectGeneric
	}
	result := ValidationResult{Valid: true}
	mode := Tolerant
	if options.StrictSyntax {
		mode = Strict
	}
	parsed, parseErr := Parse(sql, ParseOptions{Dialect: options.Dialect, Mode: mode})
	if parseErr != nil && len(parsed.Diagnostics) == 0 {
		collector := validationCollector{source: parsed.Source, result: &result}
		collector.add(SeverityError, "VALIDATION_PARSE_FAILED", parseErr.Error(), Span{})
	}
	for _, diagnostic := range parsed.Diagnostics {
		result.Diagnostics = append(result.Diagnostics, diagnostic)
		if diagnostic.Severity == SeverityError {
			appendValidationError(&result, parsed.Source, diagnostic.Severity, diagnostic.Code, diagnostic.Message, diagnostic.Span)
		}
	}
	if hasErrorDiagnostics(parsed.Diagnostics) || parseErr != nil {
		result.Valid = false
		return result
	}
	if options.StrictSyntax {
		if diagnostic, ok := polyglotStrictSyntaxDiagnostic(parsed.Tokens); ok {
			result.Diagnostics = append(result.Diagnostics, diagnostic)
			position := parsed.Source.PositionAt(diagnostic.Span.End, PositionUTF32)
			line, column := position.Line+1, position.Character+1
			result.Errors = append(result.Errors, ValidationError{
				Message:  diagnostic.Message,
				Severity: "error",
				Code:     diagnostic.Code,
				Span:     diagnostic.Span,
				Line:     &line,
				Column:   &column,
			})
			result.Valid = false
			return result
		}
	}
	if options.Semantic {
		collector := validationCollector{source: parsed.Source, result: &result, schema: options.Schema}
		for _, statement := range parsed.Statements {
			collector.statement(statement.Node)
		}
	}
	for _, issue := range result.Errors {
		if issue.Severity == "error" {
			result.Valid = false
			break
		}
	}
	return result
}

// polyglotStrictSyntaxDiagnostic mirrors Polyglot's strict_syntax E005 check.
// It runs after parsing succeeds and deliberately reports only the first
// trailing comma. Comments are trivia in Polyglot's tokenizer, so skip our
// explicit comment tokens when finding the following clause boundary.
func polyglotStrictSyntaxDiagnostic(tokens []Token) (Diagnostic, bool) {
	for index, token := range tokens {
		if token.Text != "," {
			continue
		}

		next := Token{Kind: TokenEOF}
		for following := index + 1; following < len(tokens); following++ {
			if tokens[following].Kind == TokenComment {
				continue
			}
			next = tokens[following]
			break
		}
		boundary, ok := polyglotStrictSyntaxBoundary(next)
		if !ok {
			continue
		}
		return Diagnostic{
			Severity: SeverityError,
			Code:     "E005",
			Message:  fmt.Sprintf("Trailing comma before %s is not allowed in strict syntax mode", boundary),
			Span:     token.Span,
			Found:    token.Kind,
		}, true
	}
	return Diagnostic{}, false
}

func polyglotStrictSyntaxBoundary(token Token) (string, bool) {
	if token.Kind == TokenEOF || token.Text == ";" {
		return "end of statement", true
	}
	switch {
	case token.IsWord("FROM"):
		return "FROM", true
	case token.IsWord("WHERE"):
		return "WHERE", true
	case token.IsWord("GROUP"):
		return "GROUP BY", true
	case token.IsWord("HAVING"):
		return "HAVING", true
	case token.IsWord("ORDER"):
		return "ORDER BY", true
	case token.IsWord("LIMIT"):
		return "LIMIT", true
	case token.IsWord("OFFSET"):
		return "OFFSET", true
	case token.IsWord("UNION"):
		return "UNION", true
	case token.IsWord("INTERSECT"):
		return "INTERSECT", true
	case token.IsWord("EXCEPT"):
		return "EXCEPT", true
	case token.IsWord("QUALIFY"):
		return "QUALIFY", true
	case token.IsWord("WINDOW"):
		return "WINDOW", true
	default:
		return "", false
	}
}

type validationCollector struct {
	source SourceText
	result *ValidationResult
	schema *ValidationSchema
}

func (c *validationCollector) add(severity Severity, code, message string, span Span) {
	c.result.Diagnostics = append(c.result.Diagnostics, Diagnostic{
		Severity: severity,
		Code:     code,
		Message:  message,
		Span:     span,
	})
	appendValidationError(c.result, c.source, severity, code, message, span)
}

func appendValidationError(result *ValidationResult, source SourceText, severity Severity, code, message string, span Span) {
	linePosition := source.PositionAt(span.Start, PositionUTF8)
	line, column := linePosition.Line, linePosition.Character
	start, end := span.Start, span.End
	result.Errors = append(result.Errors, ValidationError{
		Message:  message,
		Severity: validationSeverityName(severity),
		Code:     code,
		Span:     span,
		Line:     &line,
		Column:   &column,
		Start:    &start,
		End:      &end,
	})
}

func validationSeverityName(severity Severity) string {
	switch severity {
	case SeverityWarning:
		return "warning"
	case SeverityInformation:
		return "information"
	case SeverityHint:
		return "hint"
	default:
		return "error"
	}
}

func (c *validationCollector) statement(node Node) {
	switch value := node.(type) {
	case *SelectStmt:
		c.selectStatement(value)
	case *InsertStmt:
		if len(value.Table) == 0 {
			c.add(SeverityError, "SEMANTIC_INSERT_TARGET", "INSERT is missing a target table", value.SourceSpan())
		}
		if value.Query != nil {
			c.selectStatement(value.Query)
		}
	case *UpdateStmt:
		if len(value.Assignments) == 0 {
			c.add(SeverityError, "SEMANTIC_UPDATE_ASSIGNMENTS", "UPDATE requires at least one assignment", value.SourceSpan())
		}
	case *DeleteStmt:
		if len(value.Table) == 0 {
			c.add(SeverityError, "SEMANTIC_DELETE_TARGET", "DELETE is missing a target table", value.SourceSpan())
		}
	}
}

func (c *validationCollector) selectStatement(selectStmt *SelectStmt) {
	if selectStmt == nil {
		c.add(SeverityError, "SEMANTIC_MISSING_QUERY", "query is missing a SELECT body", Span{})
		return
	}
	if len(selectStmt.Projections) == 0 {
		c.add(SeverityError, "SEMANTIC_EMPTY_PROJECTION", "SELECT requires at least one projection", selectStmt.SourceSpan())
	}
	cteNames := make(map[string]bool)
	for _, cte := range selectStmt.With {
		key := strings.ToLower(cte.Name.Text)
		if cteNames[key] {
			c.add(SeverityError, "SEMANTIC_DUPLICATE_CTE", fmt.Sprintf("CTE %q is declared more than once", cte.Name.Text), cte.Span)
		}
		cteNames[key] = true
		c.selectStatement(cte.Query)
	}

	relations := collectValidationRelations(selectStmt)
	aliases := make(map[string]bool)
	for _, relation := range relations {
		key := strings.ToLower(relation.lookupName)
		if key != "" && aliases[key] {
			c.add(SeverityError, "SEMANTIC_DUPLICATE_RELATION_ALIAS", fmt.Sprintf("relation alias %q is used more than once", relation.displayName), relation.span)
		}
		if key != "" {
			aliases[key] = true
		}
	}

	projectionAliases := make(map[string]bool)
	for _, projection := range selectStmt.Projections {
		if projection.Expr == nil {
			c.add(SeverityError, "SEMANTIC_MISSING_PROJECTION", "SELECT projection is missing an expression", projection.Span)
		}
		if projection.Alias != nil {
			key := strings.ToLower(projection.Alias.Text)
			if projectionAliases[key] {
				c.add(SeverityWarning, "SEMANTIC_DUPLICATE_PROJECTION_ALIAS", fmt.Sprintf("projection alias %q is repeated", projection.Alias.Text), projection.Span)
			}
			projectionAliases[key] = true
		}
	}

	if c.schema != nil {
		c.validateSchemaRelations(relations)
		c.validateSchemaColumns(selectStmt, relations)
	}
	if selectStmt.SetLeft != nil {
		c.selectStatement(selectStmt.SetLeft)
	}
	if selectStmt.SetRight != nil {
		c.selectStatement(selectStmt.SetRight)
	}
	if selectStmt.SetRight != nil {
		left := selectStmt
		if selectStmt.SetLeft != nil {
			left = selectStmt.SetLeft
		}
		leftCount := len(left.Projections)
		rightCount := len(selectStmt.SetRight.Projections)
		if leftCount > 0 && rightCount > 0 && leftCount != rightCount {
			c.add(SeverityError, "SEMANTIC_SET_ARITY", fmt.Sprintf("%s branches project %d and %d columns", selectStmt.SetOperator, leftCount, rightCount), selectStmt.SourceSpan())
		}
	}
}

type validationRelation struct {
	lookupName  string
	displayName string
	baseName    string
	alias       string
	kind        string
	span        Span
	columns     map[string]SchemaColumn
	known       bool
}

func collectValidationRelations(selectStmt *SelectStmt) []validationRelation {
	var result []validationRelation
	cteQueries := make(map[string]*SelectStmt)
	for _, cte := range selectStmt.With {
		if cte.Query != nil {
			cteQueries[strings.ToLower(cte.Name.Text)] = cte.Query
		}
	}
	var collectFrom func(FromItem)
	collectFrom = func(item FromItem) {
		switch value := item.(type) {
		case *TableName:
			baseName := identifiersText(value.Parts)
			lookup := baseName
			if value.Alias != nil {
				lookup = value.Alias.Text
			}
			kind := "table"
			columns := map[string]SchemaColumn(nil)
			if cteQuery := cteQueries[strings.ToLower(lastIdentifier(baseName))]; cteQuery != nil {
				kind = "cte"
				columns = make(map[string]SchemaColumn)
				for _, column := range outputNames(cteQuery, nil) {
					columns[strings.ToLower(column)] = SchemaColumn{Name: column}
				}
			}
			result = append(result, validationRelation{lookupName: lookup, displayName: baseName, baseName: baseName, alias: optionalIdentifierText(value.Alias), kind: kind, columns: columns, span: value.SourceSpan()})
		case *SubqueryFrom:
			name := optionalIdentifierText(value.Alias)
			result = append(result, validationRelation{lookupName: name, displayName: name, alias: name, kind: "derived", span: value.SourceSpan()})
		case *GroupedFrom:
			for i := range value.Items {
				if value.Items[i].Primary != nil {
					collectFrom(value.Items[i].Primary)
				}
			}
		case *TableFunctionFrom:
			name := identifiersText(value.Name)
			if value.Alias != nil {
				name = value.Alias.Text
			}
			result = append(result, validationRelation{lookupName: name, displayName: name, alias: optionalIdentifierText(value.Alias), kind: "virtual", span: value.SourceSpan()})
		}
	}
	for i := range selectStmt.From {
		if selectStmt.From[i].Primary != nil {
			collectFrom(selectStmt.From[i].Primary)
		}
		for _, join := range selectStmt.From[i].Joins {
			if join.Right != nil {
				collectFrom(join.Right)
			}
		}
	}
	return result
}

func (c *validationCollector) validateSchemaRelations(relations []validationRelation) {
	strict := c.schema.Strict != nil && *c.schema.Strict
	for i := range relations {
		relation := &relations[i]
		if relation.kind != "" && relation.kind != "table" {
			relation.known = true
			continue
		}
		table, ok := findSchemaTable(*c.schema, relation.baseName)
		if !ok {
			severity := SeverityWarning
			if strict {
				severity = SeverityError
			}
			c.add(severity, "SCHEMA_UNKNOWN_TABLE", fmt.Sprintf("table %q is not present in the supplied schema", relation.baseName), relation.span)
			continue
		}
		relation.known = true
		relation.columns = make(map[string]SchemaColumn, len(table.Columns))
		for _, column := range table.Columns {
			relation.columns[strings.ToLower(column.Name)] = column
		}
	}
}

func (c *validationCollector) validateSchemaColumns(selectStmt *SelectStmt, relations []validationRelation) {
	if c.schema == nil {
		return
	}
	strict := c.schema.Strict != nil && *c.schema.Strict
	for _, reference := range Columns(selectStmt) {
		if reference.Column == "" || reference.Column == "*" {
			continue
		}
		if strings.Contains(reference.Column, "(") {
			continue
		}
		matches := make([]validationRelation, 0, len(relations))
		for _, relation := range relations {
			if reference.Table != "" && !strings.EqualFold(reference.Table, relation.lookupName) && !strings.EqualFold(reference.Table, relation.baseName) && !strings.EqualFold(reference.Table, relation.alias) {
				continue
			}
			if relation.columns == nil {
				continue
			}
			if _, ok := relation.columns[strings.ToLower(reference.Column)]; ok {
				matches = append(matches, relation)
			}
		}
		if len(matches) == 0 {
			severity := SeverityWarning
			if strict {
				severity = SeverityError
			}
			c.add(severity, "SCHEMA_UNKNOWN_COLUMN", fmt.Sprintf("column %q is not present in the supplied schema", reference.Column), reference.Span)
		} else if reference.Table == "" && len(matches) > 1 {
			c.add(SeverityError, "SEMANTIC_AMBIGUOUS_COLUMN", fmt.Sprintf("column %q is ambiguous across relations", reference.Column), reference.Span)
		}
	}
}

func findSchemaTable(schema ValidationSchema, name string) (SchemaTable, bool) {
	name = strings.ToLower(name)
	for _, table := range schema.Tables {
		candidates := []string{table.Name}
		if table.Schema != "" {
			candidates = append(candidates, table.Schema+"."+table.Name)
		}
		candidates = append(candidates, table.Aliases...)
		for _, candidate := range candidates {
			if strings.ToLower(candidate) == name || strings.ToLower(lastIdentifier(candidate)) == name {
				return table, true
			}
		}
	}
	return SchemaTable{}, false
}

func identifiersText(identifiers []Identifier) string {
	parts := make([]string, len(identifiers))
	for i, identifier := range identifiers {
		parts[i] = identifier.Text
	}
	return strings.Join(parts, ".")
}

func optionalIdentifierText(identifier *Identifier) string {
	if identifier == nil {
		return ""
	}
	return identifier.Text
}

func lastIdentifier(value string) string {
	if index := strings.LastIndex(value, "."); index >= 0 {
		return value[index+1:]
	}
	return value
}
