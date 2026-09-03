package golyglot

import (
	"fmt"
	"sort"
	"strings"
)

// QuerySemanticDiffOptions selects the dialect and the schema world used for
// each side of a query comparison.
type QuerySemanticDiffOptions struct {
	Dialect      Dialect           `json:"dialect,omitempty"`
	BeforeSchema *ValidationSchema `json:"beforeSchema,omitempty"`
	AfterSchema  *ValidationSchema `json:"afterSchema,omitempty"`
}

// SemanticChangeOrigin distinguishes a source edit from a change propagated
// through an otherwise identical query.
type SemanticChangeOrigin string

const (
	SemanticChangeDirect     SemanticChangeOrigin = "direct"
	SemanticChangePropagated SemanticChangeOrigin = "propagated"
)

// SemanticColumnContract is the normalized schema state of a referenced
// input column. Present distinguishes an absent column from one with an
// unknown type.
type SemanticColumnContract struct {
	Present     bool    `json:"present"`
	TypeHint    *string `json:"typeHint,omitempty"`
	Nullability string  `json:"nullability"`
}

// QuerySemanticInputChange describes a changed schema contract for a physical
// column referenced by either side of the query.
type QuerySemanticInputChange struct {
	Table              string                 `json:"table"`
	Column             string                 `json:"column"`
	Before             SemanticColumnContract `json:"before"`
	After              SemanticColumnContract `json:"after"`
	PresenceChanged    bool                   `json:"presenceChanged"`
	TypeChanged        bool                   `json:"typeChanged"`
	NullabilityChanged bool                   `json:"nullabilityChanged"`
}

// QuerySemanticOutputChange describes a changed output-column contract. The
// upstream references are facts, not a proof of causality; callers can join
// them with InputChanges to build an impact chain.
type QuerySemanticOutputChange struct {
	Index              int                    `json:"index"`
	Before             *QueryOutputColumnFact `json:"before,omitempty"`
	After              *QueryOutputColumnFact `json:"after,omitempty"`
	Origin             SemanticChangeOrigin   `json:"origin"`
	NameChanged        bool                   `json:"nameChanged"`
	TypeChanged        bool                   `json:"typeChanged"`
	NullabilityChanged bool                   `json:"nullabilityChanged"`
	Upstream           []ColumnReferenceFact  `json:"upstream"`
}

// QuerySemanticDiff compares query output and referenced-input contracts in
// two schema worlds. Complete is false whenever either output analysis or a
// referenced physical input has an unknown name or type.
type QuerySemanticDiff struct {
	SourceEqual    bool                        `json:"sourceEqual"`
	CanonicalEqual bool                        `json:"canonicalEqual"`
	Complete       bool                        `json:"complete"`
	BeforeAnalysis QueryAnalysis               `json:"beforeAnalysis"`
	AfterAnalysis  QueryAnalysis               `json:"afterAnalysis"`
	InputChanges   []QuerySemanticInputChange  `json:"inputChanges"`
	OutputChanges  []QuerySemanticOutputChange `json:"outputChanges"`
}

// DiffQuerySemantics analyzes a SELECT before and after a source/schema change.
// It deliberately reports facts rather than deployment severity: policy such
// as breaking versus warning belongs to the consuming application.
func DiffQuerySemantics(beforeSQL, afterSQL string, options QuerySemanticDiffOptions) (QuerySemanticDiff, error) {
	if strings.TrimSpace(string(options.Dialect)) == "" {
		options.Dialect = DialectGeneric
	}
	before, err := AnalyzeQuery(beforeSQL, AnalyzeQueryOptions{Dialect: options.Dialect, Schema: options.BeforeSchema})
	if err != nil {
		return QuerySemanticDiff{}, fmt.Errorf("analyze before query: %w", err)
	}
	after, err := AnalyzeQuery(afterSQL, AnalyzeQueryOptions{Dialect: options.Dialect, Schema: options.AfterSchema})
	if err != nil {
		return QuerySemanticDiff{}, fmt.Errorf("analyze after query: %w", err)
	}
	beforeCanonical, err := semanticCanonicalSQL(beforeSQL, options.Dialect)
	if err != nil {
		return QuerySemanticDiff{}, fmt.Errorf("canonicalize before query: %w", err)
	}
	afterCanonical, err := semanticCanonicalSQL(afterSQL, options.Dialect)
	if err != nil {
		return QuerySemanticDiff{}, fmt.Errorf("canonicalize after query: %w", err)
	}

	canonicalEqual := beforeCanonical == afterCanonical
	result := QuerySemanticDiff{
		SourceEqual:    beforeSQL == afterSQL,
		CanonicalEqual: canonicalEqual,
		Complete: before.OutputNamesComplete && before.OutputTypesComplete &&
			after.OutputNamesComplete && after.OutputTypesComplete &&
			semanticReferencedInputsComplete(before, options.BeforeSchema, options.Dialect) &&
			semanticReferencedInputsComplete(after, options.AfterSchema, options.Dialect),
		BeforeAnalysis: before,
		AfterAnalysis:  after,
	}
	result.InputChanges = semanticInputChanges(before, after, options)
	result.OutputChanges = semanticOutputChanges(before, after, canonicalEqual)
	return result, nil
}

func semanticReferencedInputsComplete(analysis QueryAnalysis, schema *ValidationSchema, dialect Dialect) bool {
	for _, projection := range analysis.Projections {
		for _, reference := range projection.Upstream {
			if reference.SourceKind != "table" || reference.SourceName == nil {
				continue
			}
			input := semanticInputReference{table: strings.TrimSpace(*reference.SourceName), column: strings.TrimSpace(reference.Column)}
			contract := semanticSchemaContract(schema, input, dialect)
			if !contract.Present || contract.TypeHint == nil {
				return false
			}
		}
	}
	return true
}

func semanticCanonicalSQL(sql string, dialect Dialect) (string, error) {
	commentFreeSQL, directives, err := semanticCanonicalSource(sql, dialect)
	if err != nil {
		return "", err
	}
	statements, err := Transpile(commentFreeSQL, dialect, dialect)
	if err != nil {
		return "", err
	}
	if len(statements) != 1 {
		return "", fmt.Errorf("semantic diff expects exactly one statement, found %d", len(statements))
	}
	canonical := statements[0]
	if len(directives) != 0 {
		canonical += "\x00directives\x00" + strings.Join(directives, "\x00")
	}
	return canonical, nil
}

// semanticCanonicalSource removes presentation-only comments before parsing
// the canonical form. Directive comments remain part of the identity through
// a separate token-position fingerprint: moving or changing a hint is not
// mistaken for formatting, while comments inside string literals are never
// touched.
func semanticCanonicalSource(sql string, dialect Dialect) (string, []string, error) {
	parsed, err := Parse(sql, ParseOptions{Dialect: dialect, Mode: Strict})
	if err != nil {
		return "", nil, err
	}

	var builder strings.Builder
	last := 0
	tokenPosition := 0
	var directives []string
	for _, token := range parsed.Tokens {
		if token.Kind != TokenComment {
			if token.Kind != TokenEOF {
				tokenPosition++
			}
			continue
		}
		if !token.Span.Valid(len(sql)) || token.Span.Start < last {
			return "", nil, fmt.Errorf("comment has invalid source span %d:%d", token.Span.Start, token.Span.End)
		}
		builder.WriteString(sql[last:token.Span.Start])
		builder.WriteByte(' ')
		last = token.Span.End
		if semanticDirectiveComment(token.Text) {
			directives = append(directives, fmt.Sprintf("%d:%s", tokenPosition, strings.TrimSpace(token.Text)))
		}
	}
	if last == 0 {
		return sql, directives, nil
	}
	builder.WriteString(sql[last:])
	return builder.String(), directives, nil
}

func semanticDirectiveComment(comment string) bool {
	comment = strings.TrimSpace(comment)
	return strings.HasPrefix(comment, "/*+") ||
		strings.HasPrefix(comment, "/*!") ||
		strings.HasPrefix(comment, "--+") ||
		strings.HasPrefix(comment, "//+") ||
		strings.HasPrefix(comment, "#+")
}

type semanticInputReference struct {
	table  string
	column string
}

func semanticInputChanges(before, after QueryAnalysis, options QuerySemanticDiffOptions) []QuerySemanticInputChange {
	referencesByKey := make(map[string]semanticInputReference)
	collect := func(analysis QueryAnalysis) {
		for _, projection := range analysis.Projections {
			for _, reference := range projection.Upstream {
				if reference.SourceKind != "table" || reference.SourceName == nil {
					continue
				}
				table := strings.TrimSpace(*reference.SourceName)
				column := strings.TrimSpace(reference.Column)
				if table == "" || column == "" {
					continue
				}
				key := strings.ToLower(table) + "\x00" + strings.ToLower(column)
				referencesByKey[key] = semanticInputReference{table: table, column: column}
			}
		}
	}
	collect(before)
	collect(after)

	references := make([]semanticInputReference, 0, len(referencesByKey))
	for _, reference := range referencesByKey {
		references = append(references, reference)
	}
	sort.Slice(references, func(i, j int) bool {
		if !strings.EqualFold(references[i].table, references[j].table) {
			return strings.ToLower(references[i].table) < strings.ToLower(references[j].table)
		}
		return strings.ToLower(references[i].column) < strings.ToLower(references[j].column)
	})

	var changes []QuerySemanticInputChange
	for _, reference := range references {
		beforeContract := semanticSchemaContract(options.BeforeSchema, reference, options.Dialect)
		afterContract := semanticSchemaContract(options.AfterSchema, reference, options.Dialect)
		change := QuerySemanticInputChange{
			Table:              reference.table,
			Column:             reference.column,
			Before:             beforeContract,
			After:              afterContract,
			PresenceChanged:    beforeContract.Present != afterContract.Present,
			TypeChanged:        !semanticStringPointersEqual(beforeContract.TypeHint, afterContract.TypeHint),
			NullabilityChanged: beforeContract.Nullability != afterContract.Nullability,
		}
		if change.PresenceChanged || change.TypeChanged || change.NullabilityChanged {
			changes = append(changes, change)
		}
	}
	return changes
}

func semanticSchemaContract(schema *ValidationSchema, reference semanticInputReference, dialect Dialect) SemanticColumnContract {
	result := SemanticColumnContract{Nullability: nullabilityUnknown}
	table, ok := findSchemaTableValue(schema, reference.table)
	if !ok {
		return result
	}
	for _, column := range table.Columns {
		if !strings.EqualFold(column.Name, reference.column) {
			continue
		}
		result.Present = true
		if dataType := strings.TrimSpace(column.Type); dataType != "" {
			normalized := strings.ToUpper(dataType)
			if parsed, err := ParseDataType(dataType, dialect); err == nil && parsed.Known() {
				normalized = parsed.SQL()
			}
			result.TypeHint = &normalized
		}
		if column.Nullable != nil {
			if *column.Nullable {
				result.Nullability = nullabilityNullable
			} else {
				result.Nullability = nullabilityNonNull
			}
		}
		return result
	}
	return result
}

func semanticOutputChanges(before, after QueryAnalysis, canonicalEqual bool) []QuerySemanticOutputChange {
	origin := SemanticChangeDirect
	if canonicalEqual {
		origin = SemanticChangePropagated
	}
	count := max(len(before.OutputColumns), len(after.OutputColumns))
	var changes []QuerySemanticOutputChange
	for index := 0; index < count; index++ {
		var beforeColumn, afterColumn *QueryOutputColumnFact
		if index < len(before.OutputColumns) {
			value := before.OutputColumns[index]
			beforeColumn = &value
		}
		if index < len(after.OutputColumns) {
			value := after.OutputColumns[index]
			afterColumn = &value
		}
		change := QuerySemanticOutputChange{Index: index, Before: beforeColumn, After: afterColumn, Origin: origin}
		switch {
		case beforeColumn == nil || afterColumn == nil:
			change.NameChanged = true
			change.TypeChanged = true
			change.NullabilityChanged = true
		case beforeColumn != nil && afterColumn != nil:
			change.NameChanged = beforeColumn.Name != afterColumn.Name
			change.TypeChanged = !semanticStringPointersEqual(beforeColumn.TypeHint, afterColumn.TypeHint)
			change.NullabilityChanged = beforeColumn.Nullability != afterColumn.Nullability
		}
		if !change.NameChanged && !change.TypeChanged && !change.NullabilityChanged {
			continue
		}
		if afterColumn != nil {
			change.Upstream = semanticOutputUpstream(after, index)
		} else {
			change.Upstream = semanticOutputUpstream(before, index)
		}
		changes = append(changes, change)
	}
	return changes
}

func semanticOutputUpstream(analysis QueryAnalysis, outputIndex int) []ColumnReferenceFact {
	position := 0
	for _, projection := range analysis.Projections {
		width := 1
		if projection.IsStar {
			width = 0
			for _, star := range analysis.StarProjections {
				if star.Index == projection.Index {
					width = len(star.ExpandedColumns)
					break
				}
			}
			if width == 0 {
				width = 1
			}
		}
		if outputIndex >= position && outputIndex < position+width {
			return append([]ColumnReferenceFact(nil), projection.Upstream...)
		}
		position += width
	}
	return nil
}

func semanticStringPointersEqual(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
