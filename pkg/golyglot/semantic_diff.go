package golyglot

import (
	"crypto/sha256"
	"encoding/binary"
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

// QueryBehaviorKind names one independently comparable part of query
// behavior. Values are stable report identifiers rather than UI labels.
type QueryBehaviorKind string

const (
	QueryBehaviorDefinitions QueryBehaviorKind = "definitions"
	QueryBehaviorProjection  QueryBehaviorKind = "projection"
	QueryBehaviorDistinct    QueryBehaviorKind = "distinct"
	QueryBehaviorRelations   QueryBehaviorKind = "relations"
	QueryBehaviorFilter      QueryBehaviorKind = "filter"
	QueryBehaviorGrouping    QueryBehaviorKind = "grouping"
	QueryBehaviorWindowing   QueryBehaviorKind = "windowing"
	QueryBehaviorSet         QueryBehaviorKind = "set_operation"
	QueryBehaviorOrdering    QueryBehaviorKind = "ordering"
	QueryBehaviorLimit       QueryBehaviorKind = "limit"
	QueryBehaviorTarget      QueryBehaviorKind = "target"
	QueryBehaviorDirectives  QueryBehaviorKind = "directives"
	QueryBehaviorOpaque      QueryBehaviorKind = "opaque"
)

const queryBehaviorFingerprintVersion = "v1"

// QueryBehaviorFacts contains canonical, formatting-insensitive query
// components. Fingerprint is versioned and covers every component.
type QueryBehaviorFacts struct {
	Version     string `json:"version"`
	Fingerprint string `json:"fingerprint"`
	Definitions string `json:"definitions,omitempty"`
	Projection  string `json:"projection,omitempty"`
	Distinct    string `json:"distinct,omitempty"`
	Relations   string `json:"relations,omitempty"`
	Filter      string `json:"filter,omitempty"`
	Grouping    string `json:"grouping,omitempty"`
	Windowing   string `json:"windowing,omitempty"`
	Set         string `json:"setOperation,omitempty"`
	Ordering    string `json:"ordering,omitempty"`
	Limit       string `json:"limit,omitempty"`
	Target      string `json:"target,omitempty"`
	Directives  string `json:"directives,omitempty"`
	Opaque      string `json:"opaque,omitempty"`
}

// QuerySemanticBehaviorChange describes one canonical query-behavior
// component that changed while keeping severity policy outside Golyglot.
type QuerySemanticBehaviorChange struct {
	Kind   QueryBehaviorKind    `json:"kind"`
	Before string               `json:"before,omitempty"`
	After  string               `json:"after,omitempty"`
	Origin SemanticChangeOrigin `json:"origin"`
}

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
	SourceEqual     bool                          `json:"sourceEqual"`
	CanonicalEqual  bool                          `json:"canonicalEqual"`
	Complete        bool                          `json:"complete"`
	BeforeAnalysis  QueryAnalysis                 `json:"beforeAnalysis"`
	AfterAnalysis   QueryAnalysis                 `json:"afterAnalysis"`
	BeforeBehavior  QueryBehaviorFacts            `json:"beforeBehavior"`
	AfterBehavior   QueryBehaviorFacts            `json:"afterBehavior"`
	InputChanges    []QuerySemanticInputChange    `json:"inputChanges"`
	OutputChanges   []QuerySemanticOutputChange   `json:"outputChanges"`
	BehaviorChanges []QuerySemanticBehaviorChange `json:"behaviorChanges"`
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
	beforeBehavior, err := AnalyzeQueryBehavior(beforeSQL, options.Dialect)
	if err != nil {
		return QuerySemanticDiff{}, fmt.Errorf("analyze before behavior: %w", err)
	}
	afterBehavior, err := AnalyzeQueryBehavior(afterSQL, options.Dialect)
	if err != nil {
		return QuerySemanticDiff{}, fmt.Errorf("analyze after behavior: %w", err)
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
		BeforeBehavior: beforeBehavior,
		AfterBehavior:  afterBehavior,
	}
	result.InputChanges = semanticInputChanges(before, after, options)
	result.OutputChanges = semanticOutputChanges(before, after, canonicalEqual)
	result.BehaviorChanges = semanticBehaviorChanges(beforeBehavior, afterBehavior)
	return result, nil
}

// AnalyzeQueryBehavior returns stable, explainable fingerprints for the
// behavior-bearing parts of one SELECT. It ignores presentation comments and
// formatting while retaining executable/directive comments.
func AnalyzeQueryBehavior(sql string, dialect Dialect) (QueryBehaviorFacts, error) {
	if strings.TrimSpace(string(dialect)) == "" {
		dialect = DialectGeneric
	}
	commentFreeSQL, directives, err := semanticCanonicalSource(sql, dialect)
	if err != nil {
		return QueryBehaviorFacts{}, err
	}
	parsed, err := ParseStrict(commentFreeSQL, dialect)
	if err != nil {
		return QueryBehaviorFacts{}, err
	}
	if len(parsed.Statements) != 1 {
		return QueryBehaviorFacts{}, fmt.Errorf("analyze query behavior expects exactly one statement, found %d", len(parsed.Statements))
	}
	query, ok := parsed.Statements[0].Node.(*SelectStmt)
	if !ok {
		return QueryBehaviorFacts{}, fmt.Errorf("analyze query behavior requires a SELECT statement, found %s", parsed.Statements[0].Node.Kind())
	}
	facts, err := semanticBehaviorFacts(query, dialect, directives)
	if err != nil {
		return QueryBehaviorFacts{}, err
	}
	facts.Fingerprint = semanticBehaviorFingerprint(facts)
	return facts, nil
}

func semanticBehaviorFacts(query *SelectStmt, dialect Dialect, directives []string) (QueryBehaviorFacts, error) {
	facts := QueryBehaviorFacts{Version: queryBehaviorFingerprintVersion}
	var err error
	facts.Definitions, err = semanticBehaviorQuery(dialect, func(part *SelectStmt) {
		part.With = append([]CTE(nil), query.With...)
		part.WithTail = query.WithTail
	})
	if err != nil {
		return QueryBehaviorFacts{}, fmt.Errorf("canonicalize definitions: %w", err)
	}
	if len(query.With) == 0 && strings.TrimSpace(query.WithTail) == "" {
		facts.Definitions = ""
	}
	facts.Projection, err = semanticBehaviorQuery(dialect, func(part *SelectStmt) {
		part.Projections = append([]SelectItem(nil), query.Projections...)
		part.ValuesRows = append([][]Expr(nil), query.ValuesRows...)
		part.ValuesAlias = query.ValuesAlias
		part.ValuesColumns = append([]Identifier(nil), query.ValuesColumns...)
	})
	if err != nil {
		return QueryBehaviorFacts{}, fmt.Errorf("canonicalize projection: %w", err)
	}
	facts.Distinct, err = semanticBehaviorQuery(dialect, func(part *SelectStmt) {
		part.Distinct = query.Distinct
		part.DistinctOn = append([]Expr(nil), query.DistinctOn...)
	})
	if err != nil {
		return QueryBehaviorFacts{}, fmt.Errorf("canonicalize distinct: %w", err)
	}
	if !query.Distinct && len(query.DistinctOn) == 0 {
		facts.Distinct = ""
	}
	facts.Relations, err = semanticBehaviorQuery(dialect, func(part *SelectStmt) {
		part.From = append([]TableExpr(nil), query.From...)
	})
	if err != nil {
		return QueryBehaviorFacts{}, fmt.Errorf("canonicalize relations: %w", err)
	}
	if len(query.From) == 0 {
		facts.Relations = ""
	}
	facts.Filter, err = semanticBehaviorQuery(dialect, func(part *SelectStmt) {
		part.Where = query.Where
	})
	if err != nil {
		return QueryBehaviorFacts{}, fmt.Errorf("canonicalize filter: %w", err)
	}
	if query.Where == nil {
		facts.Filter = ""
	}
	facts.Grouping, err = semanticBehaviorQuery(dialect, func(part *SelectStmt) {
		part.GroupBy = append([]Expr(nil), query.GroupBy...)
		part.GroupByDistinct = query.GroupByDistinct
		part.Having = query.Having
	})
	if err != nil {
		return QueryBehaviorFacts{}, fmt.Errorf("canonicalize grouping: %w", err)
	}
	if len(query.GroupBy) == 0 && query.Having == nil {
		facts.Grouping = ""
	}
	facts.Windowing, err = semanticBehaviorQuery(dialect, func(part *SelectStmt) {
		part.Qualify = query.Qualify
		part.ConnectBy = query.ConnectBy
		part.Windows = append([]NamedWindow(nil), query.Windows...)
	})
	if err != nil {
		return QueryBehaviorFacts{}, fmt.Errorf("canonicalize windowing: %w", err)
	}
	if query.Qualify == nil && query.ConnectBy == nil && len(query.Windows) == 0 {
		facts.Windowing = ""
	}
	if query.SetRight != nil {
		setQuery := cloneSelectStmt(query)
		setQuery.With = nil
		setQuery.WithTail = ""
		setQuery.Top = nil
		setQuery.Into = nil
		setQuery.SortBy = nil
		setQuery.OrderBy = nil
		setQuery.Limit = nil
		setQuery.Offset = nil
		setQuery.Fetch = nil
		facts.Set, err = GenerateWithOptions(setQuery, GenerateOptions{Canonical: true, Dialect: dialect})
		if err != nil {
			return QueryBehaviorFacts{}, fmt.Errorf("canonicalize set operation: %w", err)
		}
	}
	facts.Ordering, err = semanticBehaviorQuery(dialect, func(part *SelectStmt) {
		part.SortBy = append([]OrderItem(nil), query.SortBy...)
		part.OrderBy = append([]OrderItem(nil), query.OrderBy...)
	})
	if err != nil {
		return QueryBehaviorFacts{}, fmt.Errorf("canonicalize ordering: %w", err)
	}
	if len(query.SortBy) == 0 && len(query.OrderBy) == 0 {
		facts.Ordering = ""
	}
	facts.Limit, err = semanticBehaviorQuery(dialect, func(part *SelectStmt) {
		part.Top = query.Top
		part.TopParenthesized = query.TopParenthesized
		part.Limit = query.Limit
		part.Offset = query.Offset
		part.Fetch = query.Fetch
	})
	if err != nil {
		return QueryBehaviorFacts{}, fmt.Errorf("canonicalize limit: %w", err)
	}
	if query.Top == nil && query.Limit == nil && query.Offset == nil && query.Fetch == nil {
		facts.Limit = ""
	}
	facts.Target, err = semanticBehaviorQuery(dialect, func(part *SelectStmt) {
		part.Into = append([]Identifier(nil), query.Into...)
		part.IntoTemporary = query.IntoTemporary
		part.IntoUnlogged = query.IntoUnlogged
	})
	if err != nil {
		return QueryBehaviorFacts{}, fmt.Errorf("canonicalize target: %w", err)
	}
	if len(query.Into) == 0 {
		facts.Target = ""
	}
	facts.Directives = strings.Join(directives, "\n")
	facts.Opaque = strings.Join([]string{
		strings.TrimSpace(query.SelectModifier),
		strings.TrimSpace(query.SetModifier),
		strings.TrimSpace(query.Tail),
	}, "\x00")
	if strings.Trim(facts.Opaque, "\x00") == "" {
		facts.Opaque = ""
	}
	return facts, nil
}

func semanticBehaviorQuery(dialect Dialect, configure func(*SelectStmt)) (string, error) {
	query := &SelectStmt{Projections: []SelectItem{{Expr: &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}}}}
	configure(query)
	if len(query.Projections) == 0 && len(query.ValuesRows) == 0 {
		query.Projections = []SelectItem{{Expr: &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}}}
	}
	return GenerateWithOptions(query, GenerateOptions{Canonical: true, Dialect: dialect})
}

func semanticBehaviorFingerprint(facts QueryBehaviorFacts) string {
	hasher := sha256.New()
	values := semanticBehaviorValues(facts)
	for _, value := range append([]string{facts.Version}, values...) {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(value)))
		_, _ = hasher.Write(size[:])
		_, _ = hasher.Write([]byte(value))
	}
	return facts.Version + ":" + fmt.Sprintf("%x", hasher.Sum(nil))
}

func semanticBehaviorChanges(before, after QueryBehaviorFacts) []QuerySemanticBehaviorChange {
	kinds := []QueryBehaviorKind{
		QueryBehaviorDefinitions, QueryBehaviorProjection, QueryBehaviorDistinct,
		QueryBehaviorRelations, QueryBehaviorFilter, QueryBehaviorGrouping,
		QueryBehaviorWindowing, QueryBehaviorSet, QueryBehaviorOrdering,
		QueryBehaviorLimit, QueryBehaviorTarget, QueryBehaviorDirectives,
		QueryBehaviorOpaque,
	}
	beforeValues := semanticBehaviorValues(before)
	afterValues := semanticBehaviorValues(after)
	var changes []QuerySemanticBehaviorChange
	for index, kind := range kinds {
		if beforeValues[index] == afterValues[index] {
			continue
		}
		changes = append(changes, QuerySemanticBehaviorChange{
			Kind: kind, Before: beforeValues[index], After: afterValues[index], Origin: SemanticChangeDirect,
		})
	}
	return changes
}

func semanticBehaviorValues(facts QueryBehaviorFacts) []string {
	return []string{
		facts.Definitions, facts.Projection, facts.Distinct, facts.Relations,
		facts.Filter, facts.Grouping, facts.Windowing, facts.Set, facts.Ordering,
		facts.Limit, facts.Target, facts.Directives, facts.Opaque,
	}
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
