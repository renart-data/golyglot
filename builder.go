package golyglot

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Expression is a fluent builder value. Builder methods return derived values
// and never need a native runtime or a second SQL representation. The final
// value can be inspected with AST or rendered with BuildSQL.
type Expression struct {
	node    Node
	err     error
	dialect Dialect
	alias   string
	order   *builderOrder
}

type builderOrder struct {
	desc bool
}

// AST returns the typed node currently represented by the builder.
func (e Expression) AST() (Node, error) {
	if e.err != nil {
		return nil, e.err
	}
	if e.node == nil {
		return nil, fmt.Errorf("golyglot builder: expression is empty")
	}
	return e.node, nil
}

// Err reports the first error captured while constructing the expression.
func (e Expression) Err() error { return e.err }

// ReadDialect records the dialect used for raw SQL fragments in this builder.
// The value is also used as the default by BuildSQL when the caller passes an
// empty dialect.
func (e Expression) ReadDialect(dialect Dialect) Expression {
	e.dialect = dialect
	return e
}

// Build converts a builder expression into its AST node.
func Build(e Expression, dialect Dialect) (Node, error) {
	return e.AST()
}

// BuildSQL generates SQL from a builder expression. Expressions and FROM
// items are wrapped in the smallest useful statement for generation.
func BuildSQL(e Expression, dialect Dialect) (string, error) {
	node, err := e.AST()
	if err != nil {
		return "", err
	}
	dialect = builderDialect(e, dialect)
	if _, err := dialect.normalized(); err != nil {
		return "", err
	}
	switch value := node.(type) {
	case Expr:
		node = &ExpressionStmt{Expr: value}
	case FromItem:
		table := TableExpr{Primary: value}
		node = &SelectStmt{Projections: []SelectItem{{Expr: &StarExpr{}}}, From: []TableExpr{table}}
	}
	return GenerateWithOptions(node, GenerateOptions{Canonical: true, Dialect: dialect})
}

// SQL renders the builder using its recorded read dialect.
func (e Expression) SQL(dialect ...Dialect) (string, error) {
	selected := Dialect("")
	if len(dialect) > 0 {
		selected = dialect[0]
	}
	return BuildSQL(e, selected)
}

// String implements fmt.Stringer for convenient debugging. Rendering errors
// are represented by an empty string; callers that need the error should use
// SQL or BuildSQL.
func (e Expression) String() string {
	text, _ := e.SQL()
	return text
}

// SQLExpr inserts a trusted SQL expression fragment into a builder. It is the
// escape hatch for dialect-specific syntax not yet represented by a typed
// expression node.
func SQLExpr(sql string) Expression {
	return Expression{node: &RawExpr{Raw: strings.TrimSpace(sql)}}
}

// Condition is an explicit name for a raw boolean SQL fragment.
func Condition(sql string) Expression { return SQLExpr(sql) }

// Column creates a qualified or unqualified column reference.
func Column(name string) Expression {
	parts, err := builderIdentifiers(name)
	if err != nil {
		return Expression{err: err}
	}
	return Expression{node: &IdentifierExpr{Parts: parts}}
}

// Table creates a table reference for use with From and joins.
func Table(name string) Expression {
	parts, err := builderIdentifiers(name)
	if err != nil {
		return Expression{err: err}
	}
	return Expression{node: &TableName{Parts: parts}}
}

// Star creates a wildcard projection.
func Star() Expression { return Expression{node: &StarExpr{}} }

// Lit creates a SQL literal. Strings are quoted as SQL string literals; use
// SQLExpr or Column when the input is SQL syntax rather than a value.
func Lit(value any) Expression {
	literal, err := builderLiteral(value)
	if err != nil {
		return Expression{err: err}
	}
	return Expression{node: literal}
}

// Func creates a named function call with builder arguments.
func Func(name string, args ...any) Expression {
	identifiers, err := builderIdentifiers(name)
	if err != nil {
		return Expression{err: err}
	}
	expressions, err := builderExprs(args, true)
	if err != nil {
		return Expression{err: err}
	}
	return Expression{node: &FunctionCallExpr{Name: identifiers, Args: expressions}}
}

func builtin(name string, args ...any) Expression { return Func(name, args...) }

func Count(value any) Expression          { return builtin("COUNT", value) }
func CountStar() Expression               { return Func("COUNT", Star()) }
func CountDistinct(value any) Expression  { return Func("COUNT", Distinct(value)) }
func Sum(value any) Expression            { return builtin("SUM", value) }
func Avg(value any) Expression            { return builtin("AVG", value) }
func Min(value any) Expression            { return builtin("MIN", value) }
func Max(value any) Expression            { return builtin("MAX", value) }
func ApproxDistinct(value any) Expression { return builtin("APPROX_COUNT_DISTINCT", value) }
func Upper(value any) Expression          { return builtin("UPPER", value) }
func Lower(value any) Expression          { return builtin("LOWER", value) }
func Length(value any) Expression         { return builtin("LENGTH", value) }
func Trim(value any) Expression           { return builtin("TRIM", value) }
func LTrim(value any) Expression          { return builtin("LTRIM", value) }
func RTrim(value any) Expression          { return builtin("RTRIM", value) }
func Reverse(value any) Expression        { return builtin("REVERSE", value) }
func InitCap(value any) Expression        { return builtin("INITCAP", value) }

func Substring(value, start any, length ...any) Expression {
	args := []any{value, start}
	args = append(args, length...)
	return builtin("SUBSTRING", args...)
}

func Replace(value, old, replacement any) Expression {
	return builtin("REPLACE", value, old, replacement)
}

func ConcatWS(separator any, values ...any) Expression {
	args := append([]any{separator}, values...)
	return builtin("CONCAT_WS", args...)
}

func Coalesce(values ...any) Expression     { return builtin("COALESCE", values...) }
func NullIf(left, right any) Expression     { return builtin("NULLIF", left, right) }
func IfNull(value, fallback any) Expression { return builtin("IFNULL", value, fallback) }
func Abs(value any) Expression              { return builtin("ABS", value) }
func Round(value any, decimals ...any) Expression {
	return builtin("ROUND", append([]any{value}, decimals...)...)
}
func Floor(value any) Expression          { return builtin("FLOOR", value) }
func Ceil(value any) Expression           { return builtin("CEIL", value) }
func Power(base, exponent any) Expression { return builtin("POWER", base, exponent) }
func Sqrt(value any) Expression           { return builtin("SQRT", value) }
func Ln(value any) Expression             { return builtin("LN", value) }
func Exp(value any) Expression            { return builtin("EXP", value) }
func Sign(value any) Expression           { return builtin("SIGN", value) }
func Greatest(values ...any) Expression   { return builtin("GREATEST", values...) }
func Least(values ...any) Expression      { return builtin("LEAST", values...) }
func CurrentDate() Expression             { return builtin("CURRENT_DATE") }
func CurrentTime() Expression             { return builtin("CURRENT_TIME") }
func CurrentTimestamp() Expression        { return builtin("CURRENT_TIMESTAMP") }
func RowNumber() Expression               { return builtin("ROW_NUMBER") }
func Rank() Expression                    { return builtin("RANK") }
func DenseRank() Expression               { return builtin("DENSE_RANK") }

// Extract creates EXTRACT(field FROM value).
func Extract(field string, value any) Expression {
	fieldExpr := SQLExpr(field)
	valueExpr, err := builderExpr(value, true)
	if err != nil {
		return Expression{err: err}
	}
	fieldNode, err := builderExprNode(fieldExpr)
	if err != nil {
		return Expression{err: err}
	}
	return Expression{node: &ExtractExpr{Field: fieldNode, Source: valueExpr}}
}

// Select starts a SELECT query. An empty projection list means SELECT *.
func Select(expressions ...any) Expression {
	items := make([]SelectItem, 0, len(expressions))
	if len(expressions) == 0 {
		items = append(items, SelectItem{Expr: &StarExpr{}})
	} else {
		for _, value := range expressions {
			expression, err := builderValueExpression(value, true)
			if err != nil {
				return Expression{err: err}
			}
			items = append(items, builderSelectItem(expression))
		}
	}
	return Expression{node: &SelectStmt{Projections: items}}
}

// From starts SELECT * FROM source.
func From(source any) Expression { return Select().From(source) }

// Update starts an UPDATE statement. A map is accepted for parity with the
// Polyglot builder; keys are sorted before generation for deterministic SQL.
func Update(table string, assignments ...map[string]any) Expression {
	parts, err := builderIdentifiers(table)
	if err != nil {
		return Expression{err: err}
	}
	statement := &UpdateStmt{Table: parts}
	if len(assignments) > 1 {
		return Expression{err: fmt.Errorf("golyglot builder: Update accepts at most one assignment map")}
	}
	if len(assignments) == 1 {
		keys := make([]string, 0, len(assignments[0]))
		for key := range assignments[0] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value, valueErr := builderExpr(assignments[0][key], false)
			if valueErr != nil {
				return Expression{err: valueErr}
			}
			target, targetErr := builderIdentifiers(key)
			if targetErr != nil {
				return Expression{err: targetErr}
			}
			statement.Assignments = append(statement.Assignments, Assignment{Target: target, Value: value})
		}
	}
	return Expression{node: statement}
}

// Delete starts a DELETE FROM statement.
func Delete(table string) Expression {
	parts, err := builderIdentifiers(table)
	if err != nil {
		return Expression{err: err}
	}
	return Expression{node: &DeleteStmt{Table: parts}}
}

// Insert creates INSERT INTO target SELECT/VALUES source.
func Insert(source any, into string, columns ...string) Expression {
	table, err := builderIdentifiers(into)
	if err != nil {
		return Expression{err: err}
	}
	statement := &InsertStmt{Table: table}
	for _, column := range columns {
		parts, columnErr := builderIdentifiers(column)
		if columnErr != nil || len(parts) != 1 {
			if columnErr != nil {
				return Expression{err: columnErr}
			}
			return Expression{err: fmt.Errorf("golyglot builder: insert column %q must be a simple identifier", column)}
		}
		statement.Columns = append(statement.Columns, parts[0])
	}
	result := Expression{node: statement}
	return result.Query(source)
}

// InsertInto starts an INSERT that can be completed with Values or Query.
func InsertInto(into string) Expression {
	table, err := builderIdentifiers(into)
	if err != nil {
		return Expression{err: err}
	}
	return Expression{node: &InsertStmt{Table: table}}
}

// MergeInto preserves a merge builder entry point. The currently typed AST
// does not model MERGE clauses, so the target is retained as a lossless raw
// statement until a dedicated MergeStmt is added.
func MergeInto(target string) Expression {
	return Expression{node: &RawStmt{Keyword: "MERGE", Raw: "MERGE INTO " + strings.TrimSpace(target)}}
}

// Case starts a CASE expression, optionally with an operand.
func Case(operand ...any) Expression {
	if len(operand) > 1 {
		return Expression{err: fmt.Errorf("golyglot builder: Case accepts at most one operand")}
	}
	var value Expr
	if len(operand) == 1 {
		var err error
		value, err = builderExpr(operand[0], true)
		if err != nil {
			return Expression{err: err}
		}
	}
	return Expression{node: &CaseExpr{Operand: value}}
}

func (e Expression) derived(node Node, err error) Expression {
	if err == nil {
		err = e.err
	}
	return Expression{node: node, err: err, dialect: e.dialect}
}

func (e Expression) binary(operator string, other any, parseString bool) Expression {
	left, err := builderExprNode(e)
	if err != nil {
		return Expression{err: err}
	}
	right, rightErr := builderExpr(other, parseString)
	if rightErr != nil {
		return Expression{err: firstBuilderError(e.err, rightErr)}
	}
	return e.derived(&BinaryExpr{Left: left, Operator: operator, Right: right}, nil)
}

func (e Expression) unary(operator string) Expression {
	value, err := builderExprNode(e)
	if err != nil {
		return Expression{err: err}
	}
	return e.derived(&UnaryExpr{Operator: operator, Expr: value}, nil)
}

func (e Expression) Eq(other any) Expression    { return e.binary("=", other, false) }
func (e Expression) Neq(other any) Expression   { return e.binary("<>", other, false) }
func (e Expression) LT(other any) Expression    { return e.binary("<", other, false) }
func (e Expression) LTE(other any) Expression   { return e.binary("<=", other, false) }
func (e Expression) GT(other any) Expression    { return e.binary(">", other, false) }
func (e Expression) GTE(other any) Expression   { return e.binary(">=", other, false) }
func (e Expression) Add(other any) Expression   { return e.binary("+", other, false) }
func (e Expression) Sub(other any) Expression   { return e.binary("-", other, false) }
func (e Expression) Mul(other any) Expression   { return e.binary("*", other, false) }
func (e Expression) Div(other any) Expression   { return e.binary("/", other, false) }
func (e Expression) Mod(other any) Expression   { return e.binary("%", other, false) }
func (e Expression) Is(other any) Expression    { return e.binary("IS", other, true) }
func (e Expression) Like(other any) Expression  { return e.binary("LIKE", other, false) }
func (e Expression) ILike(other any) Expression { return e.binary("ILIKE", other, false) }
func (e Expression) RLike(other any) Expression { return e.binary("RLIKE", other, false) }
func (e Expression) And(other any) Expression   { return e.binary("AND", other, true) }
func (e Expression) Or(other any) Expression    { return e.binary("OR", other, true) }
func (e Expression) Xor(other any) Expression   { return e.binary("XOR", other, true) }
func (e Expression) Not() Expression            { return e.unary("NOT") }
func (e Expression) Neg() Expression            { return e.unary("-") }
func (e Expression) IsNull() Expression         { return e.isNull(false) }
func (e Expression) IsNotNull() Expression      { return e.isNull(true) }

func (e Expression) isNull(negated bool) Expression {
	value, err := builderExprNode(e)
	if err != nil {
		return Expression{err: err}
	}
	operator := "IS"
	if negated {
		operator = "IS NOT"
	}
	null, _ := builderLiteral(nil)
	return e.derived(&IsExpr{Value: value, Operator: operator, Right: null}, nil)
}

// As annotates a projection, table, or subquery with an alias.
func (e Expression) As(alias string) Expression {
	e.alias = strings.TrimSpace(alias)
	return e
}

// Alias is the function form of As.
func Alias(value any, alias string) Expression {
	expression, err := builderValueExpression(value, true)
	if err != nil {
		return Expression{err: err}
	}
	return expression.As(alias)
}

func (e Expression) Cast(dataType string) Expression {
	value, err := builderExprNode(e)
	if err != nil {
		return Expression{err: err}
	}
	return e.derived(&CastExpr{Keyword: "CAST", Value: value, Type: &RawExpr{Raw: strings.TrimSpace(dataType)}}, nil)
}

func (e Expression) Asc() Expression {
	e.order = &builderOrder{}
	return e
}

func (e Expression) Desc() Expression {
	e.order = &builderOrder{desc: true}
	return e
}

func (e Expression) Between(low, high any) Expression {
	value, err := builderExprNode(e)
	if err != nil {
		return Expression{err: err}
	}
	lowExpr, lowErr := builderExpr(low, false)
	highExpr, highErr := builderExpr(high, false)
	if lowErr != nil || highErr != nil {
		return Expression{err: firstBuilderError(lowErr, highErr)}
	}
	return e.derived(&BetweenExpr{Value: value, Low: lowExpr, High: highExpr}, nil)
}

func (e Expression) In(values ...any) Expression    { return e.in(false, values) }
func (e Expression) NotIn(values ...any) Expression { return e.in(true, values) }

func (e Expression) in(negated bool, values []any) Expression {
	value, err := builderExprNode(e)
	if err != nil {
		return Expression{err: err}
	}
	if len(values) == 0 {
		return Expression{err: fmt.Errorf("golyglot builder: IN requires at least one value")}
	}
	if len(values) == 1 {
		if query, ok := values[0].(Expression); ok {
			if selectStmt, selectOK := query.node.(*SelectStmt); selectOK {
				return e.derived(&InExpr{Value: value, Not: negated, Query: selectStmt}, nil)
			}
		}
	}
	items, itemErr := builderExprs(values, false)
	if itemErr != nil {
		return Expression{err: itemErr}
	}
	return e.derived(&InExpr{Value: value, Not: negated, Items: items}, nil)
}

func Distinct(value any) Expression {
	expression, err := builderExpr(value, true)
	if err != nil {
		return Expression{err: err}
	}
	return Expression{node: &RawExpr{Raw: "DISTINCT " + mustBuilderSQL(Expression{node: expression})}}
}

// Select appends projections when called on an existing SELECT expression.
func (e Expression) Select(values ...any) Expression {
	selectStmt, err := builderSelectNode(e)
	if err != nil {
		return Expression{err: err}
	}
	selectStmt.Projections = nil
	for _, value := range values {
		expression, expressionErr := builderValueExpression(value, true)
		if expressionErr != nil {
			return Expression{err: expressionErr}
		}
		selectStmt.Projections = append(selectStmt.Projections, builderSelectItem(expression))
	}
	if len(selectStmt.Projections) == 0 {
		selectStmt.Projections = append(selectStmt.Projections, SelectItem{Expr: &StarExpr{}})
	}
	return e.derived(selectStmt, nil)
}

func (e Expression) From(source any) Expression {
	selectStmt, err := builderSelectNode(e)
	if err != nil {
		return Expression{err: err}
	}
	item, itemErr := builderFromItem(source)
	if itemErr != nil {
		return Expression{err: itemErr}
	}
	selectStmt.From = append(selectStmt.From, TableExpr{Primary: item})
	return e.derived(selectStmt, nil)
}

func (e Expression) Join(source any, on any, joinType ...string) Expression {
	selectStmt, err := builderSelectNode(e)
	if err != nil {
		return Expression{err: err}
	}
	if len(selectStmt.From) == 0 {
		return Expression{err: fmt.Errorf("golyglot builder: Join requires a FROM source")}
	}
	right, rightErr := builderFromItem(source)
	if rightErr != nil {
		return Expression{err: rightErr}
	}
	kind, kindErr := builderJoinKind(joinType)
	if kindErr != nil {
		return Expression{err: kindErr}
	}
	var condition Expr
	if on != nil {
		condition, err = builderExpr(on, true)
		if err != nil {
			return Expression{err: err}
		}
	}
	last := len(selectStmt.From) - 1
	selectStmt.From[last].Joins = append(selectStmt.From[last].Joins, JoinClause{Kind: kind, Right: right, Condition: condition})
	return e.derived(selectStmt, nil)
}

func (e Expression) LeftJoin(source, on any) Expression  { return e.Join(source, on, "left") }
func (e Expression) RightJoin(source, on any) Expression { return e.Join(source, on, "right") }
func (e Expression) FullJoin(source, on any) Expression  { return e.Join(source, on, "full") }
func (e Expression) CrossJoin(source any) Expression     { return e.Join(source, nil, "cross") }

func (e Expression) Where(values ...any) Expression {
	return e.whereLike("where", values)
}

func (e Expression) GroupBy(values ...any) Expression {
	return e.whereLike("group_by", values)
}

func (e Expression) Having(values ...any) Expression {
	return e.whereLike("having", values)
}

func (e Expression) Qualify(value any) Expression {
	return e.whereLike("qualify", []any{value})
}

func (e Expression) whereLike(kind string, values []any) Expression {
	selectStmt, err := builderSelectNode(e)
	if err != nil {
		return Expression{err: err}
	}
	if len(values) == 0 {
		return Expression{err: fmt.Errorf("golyglot builder: %s requires at least one expression", kind)}
	}
	expressions, expressionErr := builderExprs(values, true)
	if expressionErr != nil {
		return Expression{err: expressionErr}
	}
	switch kind {
	case "where":
		selectStmt.Where = appendBuilderConditions(selectStmt.Where, expressions)
	case "group_by":
		selectStmt.GroupBy = append(selectStmt.GroupBy, expressions...)
	case "having":
		selectStmt.Having = appendBuilderConditions(selectStmt.Having, expressions)
	case "qualify":
		selectStmt.Qualify = appendBuilderConditions(selectStmt.Qualify, expressions)
	}
	return e.derived(selectStmt, nil)
}

func (e Expression) OrderBy(values ...any) Expression {
	return e.orderLike(false, values)
}

func (e Expression) SortBy(values ...any) Expression {
	return e.orderLike(true, values)
}

func (e Expression) orderLike(sortBy bool, values []any) Expression {
	selectStmt, err := builderSelectNode(e)
	if err != nil {
		return Expression{err: err}
	}
	if len(values) == 0 {
		return Expression{err: fmt.Errorf("golyglot builder: order clause requires at least one expression")}
	}
	for _, value := range values {
		expression, expressionErr := builderExpr(value, true)
		if expressionErr != nil {
			return Expression{err: expressionErr}
		}
		item := OrderItem{Expr: expression}
		if expressionValue, ok := value.(Expression); ok && expressionValue.order != nil {
			item.Descending = expressionValue.order.desc
			item.Ascending = !item.Descending
		}
		if sortBy {
			selectStmt.SortBy = append(selectStmt.SortBy, item)
		} else {
			selectStmt.OrderBy = append(selectStmt.OrderBy, item)
		}
	}
	return e.derived(selectStmt, nil)
}

func (e Expression) Limit(value any) Expression {
	selectStmt, err := builderSelectNode(e)
	if err != nil {
		return Expression{err: err}
	}
	expression, expressionErr := builderExpr(value, false)
	if expressionErr != nil {
		return Expression{err: expressionErr}
	}
	selectStmt.Limit = expression
	return e.derived(selectStmt, nil)
}

func (e Expression) Offset(value any) Expression {
	selectStmt, err := builderSelectNode(e)
	if err != nil {
		return Expression{err: err}
	}
	expression, expressionErr := builderExpr(value, false)
	if expressionErr != nil {
		return Expression{err: expressionErr}
	}
	selectStmt.Offset = expression
	return e.derived(selectStmt, nil)
}

func (e Expression) Distinct() Expression {
	selectStmt, err := builderSelectNode(e)
	if err != nil {
		return Expression{err: err}
	}
	selectStmt.Distinct = true
	return e.derived(selectStmt, nil)
}

func (e Expression) DistinctOn(values ...any) Expression {
	selectStmt, err := builderSelectNode(e)
	if err != nil {
		return Expression{err: err}
	}
	expressions, expressionErr := builderExprs(values, true)
	if expressionErr != nil {
		return Expression{err: expressionErr}
	}
	selectStmt.Distinct = true
	selectStmt.DistinctOn = expressions
	return e.derived(selectStmt, nil)
}

func (e Expression) Union(other Expression) Expression { return e.setOperation(other, "UNION", false) }
func (e Expression) UnionAll(other Expression) Expression {
	return e.setOperation(other, "UNION", true)
}
func (e Expression) Intersect(other Expression) Expression {
	return e.setOperation(other, "INTERSECT", false)
}
func (e Expression) Except(other Expression) Expression {
	return e.setOperation(other, "EXCEPT", false)
}

func (e Expression) setOperation(other Expression, operator string, all bool) Expression {
	left, err := builderSelectNode(e)
	if err != nil {
		return Expression{err: err}
	}
	right, rightErr := builderSelectNode(other)
	if rightErr != nil {
		return Expression{err: rightErr}
	}
	return e.derived(&SelectStmt{SetLeft: left, SetRight: right, SetOperator: operator, SetAll: all}, nil)
}

func (e Expression) With(name string, query Expression, columns ...string) Expression {
	selectStmt, err := builderSelectNode(e)
	if err != nil {
		return Expression{err: err}
	}
	cteQuery, queryErr := builderSelectNode(query)
	if queryErr != nil {
		return Expression{err: queryErr}
	}
	identifier, identifierErr := builderIdentifiers(name)
	if identifierErr != nil || len(identifier) != 1 {
		if identifierErr != nil {
			return Expression{err: identifierErr}
		}
		return Expression{err: fmt.Errorf("golyglot builder: CTE name %q must be a simple identifier", name)}
	}
	cte := CTE{Name: identifier[0], Query: cteQuery}
	for _, column := range columns {
		parts, partsErr := builderIdentifiers(column)
		if partsErr != nil || len(parts) != 1 {
			if partsErr != nil {
				return Expression{err: partsErr}
			}
			return Expression{err: fmt.Errorf("golyglot builder: CTE column %q must be a simple identifier", column)}
		}
		cte.Columns = append(cte.Columns, parts[0])
	}
	selectStmt.With = append([]CTE{cte}, selectStmt.With...)
	return e.derived(selectStmt, nil)
}

func (e Expression) Values(rows ...[]any) Expression {
	insert, ok := e.node.(*InsertStmt)
	if !ok {
		return Expression{err: fmt.Errorf("golyglot builder: Values requires an INSERT expression")}
	}
	insert = cloneInsertStmt(insert)
	for _, row := range rows {
		values, err := builderExprs(row, false)
		if err != nil {
			return Expression{err: err}
		}
		insert.Values = append(insert.Values, values)
	}
	return e.derived(insert, nil)
}

func (e Expression) Query(query any) Expression {
	insert, ok := e.node.(*InsertStmt)
	if !ok {
		return Expression{err: fmt.Errorf("golyglot builder: Query requires an INSERT expression")}
	}
	insert = cloneInsertStmt(insert)
	selectStmt, err := builderSelectNodeFromValue(query)
	if err != nil {
		return Expression{err: err}
	}
	insert.Query = selectStmt
	return e.derived(insert, nil)
}

func (e Expression) Set(column string, value any) Expression {
	update, ok := e.node.(*UpdateStmt)
	if !ok {
		return Expression{err: fmt.Errorf("golyglot builder: Set requires an UPDATE expression")}
	}
	update = cloneUpdateStmt(update)
	target, err := builderIdentifiers(column)
	if err != nil {
		return Expression{err: err}
	}
	expression, expressionErr := builderExpr(value, false)
	if expressionErr != nil {
		return Expression{err: expressionErr}
	}
	update.Assignments = append(update.Assignments, Assignment{Target: target, Value: expression})
	return e.derived(update, nil)
}

func (e Expression) Returning(values ...any) Expression {
	valuesExpr, err := builderExprs(values, true)
	if err != nil {
		return Expression{err: err}
	}
	parts := make([]string, 0, len(valuesExpr))
	for _, value := range valuesExpr {
		parts = append(parts, mustBuilderSQL(Expression{node: value}))
	}
	return e.appendTail("RETURNING " + strings.Join(parts, ", "))
}

func (e Expression) When(condition, value any) Expression {
	caseExpr, ok := e.node.(*CaseExpr)
	if !ok {
		return Expression{err: fmt.Errorf("golyglot builder: When requires a CASE expression")}
	}
	caseExpr = cloneCaseExpr(caseExpr)
	conditionExpr, conditionErr := builderExpr(condition, true)
	valueExpr, valueErr := builderExpr(value, true)
	if conditionErr != nil || valueErr != nil {
		return Expression{err: firstBuilderError(conditionErr, valueErr)}
	}
	caseExpr.Whens = append(caseExpr.Whens, CaseWhen{Condition: conditionExpr, Result: valueExpr})
	return e.derived(caseExpr, nil)
}

func (e Expression) Else(value any) Expression {
	caseExpr, ok := e.node.(*CaseExpr)
	if !ok {
		return Expression{err: fmt.Errorf("golyglot builder: Else requires a CASE expression")}
	}
	caseExpr = cloneCaseExpr(caseExpr)
	expression, err := builderExpr(value, true)
	if err != nil {
		return Expression{err: err}
	}
	caseExpr.Else = expression
	return e.derived(caseExpr, nil)
}

func (e Expression) Over(spec WindowSpec) Expression {
	value, err := builderExprNode(e)
	if err != nil {
		return Expression{err: err}
	}
	return e.derived(&WindowedExpr{Expr: value, Over: spec}, nil)
}

func (e Expression) appendTail(tail string) Expression {
	tail = strings.TrimSpace(tail)
	var result Node
	switch node := e.node.(type) {
	case *InsertStmt:
		node = cloneInsertStmt(node)
		node.Tail = strings.TrimSpace(strings.Join([]string{node.Tail, tail}, " "))
		result = node
	case *UpdateStmt:
		node = cloneUpdateStmt(node)
		node.Tail = strings.TrimSpace(strings.Join([]string{node.Tail, tail}, " "))
		result = node
	case *DeleteStmt:
		node = cloneDeleteStmt(node)
		node.Tail = strings.TrimSpace(strings.Join([]string{node.Tail, tail}, " "))
		result = node
	default:
		return Expression{err: fmt.Errorf("golyglot builder: %T does not support a trailing clause", e.node)}
	}
	return e.derived(result, nil)
}

func builderDialect(e Expression, dialect Dialect) Dialect {
	if strings.TrimSpace(string(dialect)) == "" {
		if strings.TrimSpace(string(e.dialect)) != "" {
			return e.dialect
		}
		return DialectGeneric
	}
	return dialect
}

func builderSelectNode(e Expression) (*SelectStmt, error) {
	if e.err != nil {
		return nil, e.err
	}
	selectStmt, ok := e.node.(*SelectStmt)
	if !ok || selectStmt == nil {
		return nil, fmt.Errorf("golyglot builder: expected a SELECT expression")
	}
	return cloneSelectStmt(selectStmt), nil
}

func cloneSelectStmt(source *SelectStmt) *SelectStmt {
	if source == nil {
		return nil
	}
	copy := *source
	copy.With = append([]CTE(nil), source.With...)
	copy.Projections = append([]SelectItem(nil), source.Projections...)
	for i := range copy.Projections {
		copy.Projections[i].Except = append([]Expr(nil), source.Projections[i].Except...)
		copy.Projections[i].Replace = append([]SelectItem(nil), source.Projections[i].Replace...)
	}
	copy.From = append([]TableExpr(nil), source.From...)
	for i := range copy.From {
		copy.From[i].Joins = append([]JoinClause(nil), source.From[i].Joins...)
		copy.From[i].LateralViews = append([]LateralView(nil), source.From[i].LateralViews...)
		copy.From[i].Modifiers = append([]string(nil), source.From[i].Modifiers...)
		copy.From[i].TrailingJoins = append([]string(nil), source.From[i].TrailingJoins...)
	}
	copy.DistinctOn = append([]Expr(nil), source.DistinctOn...)
	copy.GroupBy = append([]Expr(nil), source.GroupBy...)
	copy.Windows = append([]NamedWindow(nil), source.Windows...)
	copy.SortBy = append([]OrderItem(nil), source.SortBy...)
	copy.OrderBy = append([]OrderItem(nil), source.OrderBy...)
	if source.Fetch != nil {
		fetch := *source.Fetch
		copy.Fetch = &fetch
	}
	return &copy
}

func cloneInsertStmt(source *InsertStmt) *InsertStmt {
	copy := *source
	copy.Table = append([]Identifier(nil), source.Table...)
	copy.Columns = append([]Identifier(nil), source.Columns...)
	copy.Values = make([][]Expr, len(source.Values))
	for i := range source.Values {
		copy.Values[i] = append([]Expr(nil), source.Values[i]...)
	}
	return &copy
}

func cloneUpdateStmt(source *UpdateStmt) *UpdateStmt {
	copy := *source
	copy.Table = append([]Identifier(nil), source.Table...)
	copy.Assignments = append([]Assignment(nil), source.Assignments...)
	return &copy
}

func cloneDeleteStmt(source *DeleteStmt) *DeleteStmt {
	copy := *source
	copy.Table = append([]Identifier(nil), source.Table...)
	return &copy
}

func cloneCaseExpr(source *CaseExpr) *CaseExpr {
	copy := *source
	copy.Whens = append([]CaseWhen(nil), source.Whens...)
	return &copy
}

func builderSelectNodeFromValue(value any) (*SelectStmt, error) {
	switch value := value.(type) {
	case Expression:
		return builderSelectNode(value)
	case *SelectStmt:
		return value, nil
	default:
		return nil, fmt.Errorf("golyglot builder: expected a SELECT expression, got %T", value)
	}
}

func builderExpr(value any, parseString bool) (Expr, error) {
	node, err := builderNode(value, parseString)
	if err != nil {
		return nil, err
	}
	expression, ok := node.(Expr)
	if !ok {
		return nil, fmt.Errorf("golyglot builder: %T is not an expression", value)
	}
	return expression, nil
}

func builderValueExpression(value any, parseString bool) (Expression, error) {
	if expression, ok := value.(Expression); ok {
		if expression.err != nil {
			return Expression{}, expression.err
		}
		if expression.node == nil {
			return Expression{}, fmt.Errorf("golyglot builder: expression is empty")
		}
		return expression, nil
	}
	node, err := builderNode(value, parseString)
	if err != nil {
		return Expression{}, err
	}
	return Expression{node: node}, nil
}

func builderExprNode(e Expression) (Expr, error) {
	if e.err != nil {
		return nil, e.err
	}
	expression, ok := e.node.(Expr)
	if !ok || expression == nil {
		return nil, fmt.Errorf("golyglot builder: expected an expression, got %T", e.node)
	}
	return expression, nil
}

func builderExprs(values []any, parseStrings bool) ([]Expr, error) {
	result := make([]Expr, 0, len(values))
	for _, value := range values {
		expression, err := builderExpr(value, parseStrings)
		if err != nil {
			return nil, err
		}
		result = append(result, expression)
	}
	return result, nil
}

func builderNode(value any, parseString bool) (Node, error) {
	switch value := value.(type) {
	case Expression:
		if value.err != nil {
			return nil, value.err
		}
		if value.node == nil {
			return nil, fmt.Errorf("golyglot builder: expression is empty")
		}
		return value.node, nil
	case Node:
		return value, nil
	case string:
		if parseString {
			return &RawExpr{Raw: strings.TrimSpace(value)}, nil
		}
		return builderLiteral(value)
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return builderLiteral(value)
	default:
		return nil, fmt.Errorf("golyglot builder: unsupported expression input %T", value)
	}
}

func builderFromItem(value any) (FromItem, error) {
	var node Node
	var alias string
	switch value := value.(type) {
	case Expression:
		if value.err != nil {
			return nil, value.err
		}
		node = value.node
		alias = value.alias
	case string:
		return builderFromItem(Table(value))
	case FromItem:
		node = value
	case *SelectStmt:
		node = value
	default:
		return nil, fmt.Errorf("golyglot builder: unsupported FROM input %T", value)
	}
	if node == nil {
		return nil, fmt.Errorf("golyglot builder: FROM input is empty")
	}
	if selectStmt, ok := node.(*SelectStmt); ok {
		return &SubqueryFrom{Query: selectStmt, Alias: builderOptionalIdentifier(alias)}, nil
	}
	if item, ok := node.(FromItem); ok {
		return builderAliasFromItem(item, alias), nil
	}
	if expression, ok := node.(Expr); ok {
		return &RawFrom{Raw: mustBuilderSQL(Expression{node: expression}), Alias: builderOptionalIdentifier(alias)}, nil
	}
	return nil, fmt.Errorf("golyglot builder: %T cannot be used in FROM", value)
}

func builderAliasFromItem(item FromItem, alias string) FromItem {
	if alias == "" {
		return item
	}
	identifier := builderOptionalIdentifier(alias)
	switch value := item.(type) {
	case *TableName:
		copy := *value
		copy.Alias = identifier
		return &copy
	case *SubqueryFrom:
		copy := *value
		copy.Alias = identifier
		return &copy
	case *GroupedFrom:
		copy := *value
		copy.Alias = identifier
		return &copy
	case *RawFrom:
		copy := *value
		copy.Alias = identifier
		return &copy
	case *TableFunctionFrom:
		copy := *value
		copy.Alias = identifier
		return &copy
	default:
		return item
	}
}

func builderSelectItem(expression Expression) SelectItem {
	result := SelectItem{}
	if expression.alias != "" {
		result.Alias = builderOptionalIdentifier(expression.alias)
	}
	result.Expr, _ = builderExprNode(expression)
	return result
}

func builderJoinKind(values []string) (JoinKind, error) {
	value := ""
	if len(values) > 1 {
		return JoinInner, fmt.Errorf("golyglot builder: Join accepts at most one join type")
	}
	if len(values) == 1 {
		value = strings.ToLower(strings.TrimSpace(values[0]))
	}
	switch value {
	case "", "join", "inner", "inner join":
		return JoinInner, nil
	case "left", "left join", "left outer", "left outer join":
		return JoinLeft, nil
	case "right", "right join", "right outer", "right outer join":
		return JoinRight, nil
	case "full", "full join", "full outer", "full outer join":
		return JoinFull, nil
	case "cross", "cross join":
		return JoinCross, nil
	default:
		return JoinInner, fmt.Errorf("golyglot builder: unsupported join type %q", value)
	}
}

func appendBuilderConditions(existing Expr, values []Expr) Expr {
	result := existing
	for _, value := range values {
		if result == nil {
			result = value
		} else {
			result = &BinaryExpr{Left: result, Operator: "AND", Right: value}
		}
	}
	return result
}

func builderLiteral(value any) (*LiteralExpr, error) {
	literal := &LiteralExpr{}
	switch value := value.(type) {
	case nil:
		literal.KindValue = LiteralNull
		literal.Raw = "NULL"
	case string:
		literal.KindValue = LiteralString
		literal.Raw = "'" + strings.ReplaceAll(value, "'", "''") + "'"
	case bool:
		literal.KindValue = LiteralBoolean
		literal.Raw = strconv.FormatBool(value)
	case int:
		literal.KindValue = LiteralNumber
		literal.Raw = strconv.Itoa(value)
	case int8:
		literal.KindValue = LiteralNumber
		literal.Raw = strconv.FormatInt(int64(value), 10)
	case int16:
		literal.KindValue = LiteralNumber
		literal.Raw = strconv.FormatInt(int64(value), 10)
	case int32:
		literal.KindValue = LiteralNumber
		literal.Raw = strconv.FormatInt(int64(value), 10)
	case int64:
		literal.KindValue = LiteralNumber
		literal.Raw = strconv.FormatInt(value, 10)
	case uint:
		literal.KindValue = LiteralNumber
		literal.Raw = strconv.FormatUint(uint64(value), 10)
	case uint8:
		literal.KindValue = LiteralNumber
		literal.Raw = strconv.FormatUint(uint64(value), 10)
	case uint16:
		literal.KindValue = LiteralNumber
		literal.Raw = strconv.FormatUint(uint64(value), 10)
	case uint32:
		literal.KindValue = LiteralNumber
		literal.Raw = strconv.FormatUint(uint64(value), 10)
	case uint64:
		literal.KindValue = LiteralNumber
		literal.Raw = strconv.FormatUint(value, 10)
	case float32:
		literal.KindValue = LiteralNumber
		literal.Raw = strconv.FormatFloat(float64(value), 'g', -1, 32)
	case float64:
		literal.KindValue = LiteralNumber
		literal.Raw = strconv.FormatFloat(value, 'g', -1, 64)
	default:
		return nil, fmt.Errorf("golyglot builder: unsupported literal type %T", value)
	}
	return literal, nil
}

func builderIdentifiers(value string) ([]Identifier, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("golyglot builder: identifier is empty")
	}
	parts := splitBuilderIdentifier(value)
	identifiers := make([]Identifier, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("golyglot builder: invalid identifier %q", value)
		}
		quoted := false
		if len(part) >= 2 {
			first, last := part[0], part[len(part)-1]
			if (first == '"' && last == '"') || (first == '`' && last == '`') || (first == '[' && last == ']') {
				quoted = true
				part = part[1 : len(part)-1]
			}
		}
		identifiers = append(identifiers, Identifier{Text: part, Quoted: quoted})
	}
	return identifiers, nil
}

func splitBuilderIdentifier(value string) []string {
	parts := make([]string, 0, 3)
	start := 0
	var quote byte
	for i := 0; i < len(value); i++ {
		char := value[i]
		if quote != 0 {
			if char == quote {
				quote = 0
			}
			continue
		}
		switch char {
		case '"', '`', '[':
			quote = char
		case '.':
			parts = append(parts, value[start:i])
			start = i + 1
		}
	}
	parts = append(parts, value[start:])
	return parts
}

func builderOptionalIdentifier(value string) *Identifier {
	parts, err := builderIdentifiers(value)
	if err != nil || len(parts) != 1 {
		return nil
	}
	return &parts[0]
}

func builderExprSQL(expression Expr) string {
	return mustBuilderSQL(Expression{node: expression})
}

func mustBuilderSQL(expression Expression) string {
	text, _ := BuildSQL(expression, DialectGeneric)
	return text
}

func firstBuilderError(errors ...error) error {
	for _, err := range errors {
		if err != nil {
			return err
		}
	}
	return nil
}
