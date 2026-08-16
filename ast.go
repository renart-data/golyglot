package golyglot

// NodeKind is stable enough for consumers to switch on without relying on Go
// concrete type names.
type NodeKind string

const (
	NodeSelectStatement     NodeKind = "select_statement"
	NodeExpressionStatement NodeKind = "expression_statement"
	NodeCreateTable         NodeKind = "create_table_statement"
	NodeInsertStatement     NodeKind = "insert_statement"
	NodeUpdateStatement     NodeKind = "update_statement"
	NodeDeleteStatement     NodeKind = "delete_statement"
	NodeCommand             NodeKind = "command_statement"
	NodeRawStatement        NodeKind = "raw_statement"
	NodeUnknownStatement    NodeKind = "unknown_statement"
	NodeIdentifier          NodeKind = "identifier"
	NodeLiteral             NodeKind = "literal"
	NodeStar                NodeKind = "star"
	NodeUnary               NodeKind = "unary"
	NodeBinary              NodeKind = "binary"
	NodeFunctionCall        NodeKind = "function_call"
	NodeCall                NodeKind = "call"
	NodeGeneric             NodeKind = "generic"
	NodeRawExpression       NodeKind = "raw_expression"
	NodeExtract             NodeKind = "extract"
	NodeTuple               NodeKind = "tuple"
	NodeAlias               NodeKind = "alias"
	NodeInterval            NodeKind = "interval"
	NodeCast                NodeKind = "cast"
	NodeWindowed            NodeKind = "windowed"
	NodeSubqueryExpr        NodeKind = "subquery_expression"
	NodeExists              NodeKind = "exists"
	NodeQuantified          NodeKind = "quantified"
	NodeGrouping            NodeKind = "grouping"
	NodeSetExpression       NodeKind = "set_expression"
	NodeTypedLiteral        NodeKind = "typed_literal"
	NodeCase                NodeKind = "case"
	NodeIndex               NodeKind = "index"
	NodeField               NodeKind = "field"
	NodeParenthesized       NodeKind = "parenthesized"
	NodeIn                  NodeKind = "in"
	NodeBetween             NodeKind = "between"
	NodeIs                  NodeKind = "is"
	NodeMissing             NodeKind = "missing"
	NodeError               NodeKind = "error"
	NodeTable               NodeKind = "table"
	NodeTableFunction       NodeKind = "table_function"
	NodeSubquery            NodeKind = "subquery"
	NodeGroupedFrom         NodeKind = "grouped_from"
	NodeRawFrom             NodeKind = "raw_from"
	NodeJoin                NodeKind = "join"
)

// Node is the common source-aware AST interface.
type Node interface {
	Kind() NodeKind
	SourceSpan() Span
}

type Expr interface {
	Node
	expressionNode()
}

type FromItem interface {
	Node
	fromItemNode()
}

type nodeBase struct {
	span Span
	raw  string
}

func (n nodeBase) SourceSpan() Span { return n.span }
func (n nodeBase) rawSQL() string   { return n.raw }
func (n *nodeBase) setRaw(raw string) {
	n.raw = raw
}

type Statement struct {
	Node Node
	Span Span
}

type SelectStmt struct {
	nodeBase
	RawQuery         string
	With             []CTE
	Distinct         bool
	DistinctOn       []Expr
	SelectModifier   string
	Top              Expr
	TopParenthesized bool
	Projections      []SelectItem
	Into             []Identifier
	IntoTemporary    bool
	IntoUnlogged     bool
	From             []TableExpr
	Where            Expr
	GroupBy          []Expr
	GroupByDistinct  bool
	Having           Expr
	Qualify          Expr
	ConnectBy        Expr
	Windows          []NamedWindow
	SortBy           []OrderItem
	OrderBy          []OrderItem
	Limit            Expr
	Offset           Expr
	Fetch            *FetchClause
	// ValuesRows represents a VALUES query before dialect-specific lowering.
	// Keeping it on SelectStmt lets VALUES participate in CTEs and set
	// operations without introducing a second query root type.
	ValuesRows       [][]Expr
	ValuesAlias      *Identifier
	ValuesColumns    []Identifier
	SetOperator      string
	SetAll           bool
	SetModifier      string
	SetLeft          *SelectStmt
	SetRight         *SelectStmt
	Parenthesized    bool
	SetLeftParen     bool
	SetRightParen    bool
	ParenthesisDepth int
	TailOutsideParen bool
	// Tail preserves dialect-specific query clauses that the common grammar
	// does not model yet. It keeps parsing lossless without pretending the
	// clause has generic semantics.
	Tail string
}

func (*SelectStmt) Kind() NodeKind { return NodeSelectStatement }

type ExpressionStmt struct {
	nodeBase
	Expr         Expr
	Alias        *Identifier
	AliasColumns []Identifier
}

func (*ExpressionStmt) Kind() NodeKind { return NodeExpressionStatement }

type CreateTableStmt struct {
	nodeBase
	Materialized bool
	Temporary    bool
	IfNotExists  bool
	Name         []Identifier
	Tail         string
}

func (*CreateTableStmt) Kind() NodeKind { return NodeCreateTable }

type InsertStmt struct {
	nodeBase
	Table   []Identifier
	Columns []Identifier
	Values  [][]Expr
	Query   *SelectStmt
	Tail    string
}

func (*InsertStmt) Kind() NodeKind { return NodeInsertStatement }

type Assignment struct {
	Target []Identifier
	Value  Expr
	Span   Span
}

type UpdateStmt struct {
	nodeBase
	Table       []Identifier
	Assignments []Assignment
	Where       Expr
	Tail        string
}

func (*UpdateStmt) Kind() NodeKind { return NodeUpdateStatement }

type DeleteStmt struct {
	nodeBase
	Table []Identifier
	Where Expr
	Tail  string
}

func (*DeleteStmt) Kind() NodeKind { return NodeDeleteStatement }

type CommandStmt struct {
	nodeBase
	Keyword string
	Raw     string
}

func (*CommandStmt) Kind() NodeKind { return NodeCommand }

type RawStmt struct {
	nodeBase
	Keyword string
	Raw     string
}

func (*RawStmt) Kind() NodeKind { return NodeRawStatement }

type UnknownStmt struct {
	nodeBase
	Tokens []Token
	Reason string
}

func (*UnknownStmt) Kind() NodeKind { return NodeUnknownStatement }

type CTE struct {
	Name         Identifier
	Columns      []Identifier
	Modifier     string
	Query        *SelectStmt
	Recursive    bool
	Materialized string
	Span         Span
}

type SelectItem struct {
	Expr         Expr
	Alias        *Identifier
	AliasColumns []Identifier
	Except       []Expr
	Replace      []SelectItem
	Rename       bool
	Span         Span
}

type OrderItem struct {
	Expr       Expr
	Ascending  bool
	Descending bool
	NullsFirst bool
	NullsLast  bool
	Span       Span
}

type TableExpr struct {
	nodeBase
	Primary       FromItem
	Joins         []JoinClause
	LateralViews  []LateralView
	Modifiers     []string
	TrailingJoins []string
}

func (*TableExpr) Kind() NodeKind { return NodeJoin }

type JoinClause struct {
	nodeBase
	Kind      JoinKind
	JoinText  string
	Right     FromItem
	Condition Expr
	Using     []Identifier
	Late      bool
}

type LateralView struct {
	Expression    Expr
	Alias         *Identifier
	AliasExplicit bool
	Outer         bool
	Columns       []Identifier
	Span          Span
}

type NamedWindow struct {
	Name Identifier
	Spec WindowSpec
	Span Span
}

type WindowSpec struct {
	Name        *Identifier
	Base        *Identifier
	PartitionBy []Expr
	OrderBy     []OrderItem
	Frame       string
}

type JoinKind uint8

const (
	JoinInner JoinKind = iota
	JoinLeft
	JoinRight
	JoinFull
	JoinCross
)

type TableName struct {
	nodeBase
	Parts   []Identifier
	Alias   *Identifier
	Columns []Identifier
	Sample  *TableSample
	Hint    string
	Tail    string
}

func (*TableName) Kind() NodeKind { return NodeTable }
func (*TableName) fromItemNode()  {}

type SubqueryFrom struct {
	nodeBase
	Query   *SelectStmt
	Alias   *Identifier
	Columns []Identifier
	Lateral bool
}

func (*SubqueryFrom) Kind() NodeKind { return NodeSubquery }
func (*SubqueryFrom) fromItemNode()  {}

type GroupedFrom struct {
	nodeBase
	Items   []TableExpr
	Alias   *Identifier
	Columns []Identifier
}

func (*GroupedFrom) Kind() NodeKind { return NodeGroupedFrom }
func (*GroupedFrom) fromItemNode()  {}

type RawFrom struct {
	nodeBase
	Raw     string
	Alias   *Identifier
	Columns []Identifier
}

func (*RawFrom) Kind() NodeKind { return NodeRawFrom }
func (*RawFrom) fromItemNode()  {}

type TableFunctionFrom struct {
	nodeBase
	Name           []Identifier
	Args           []Expr
	RawArgs        string
	Alias          *Identifier
	Columns        []Identifier
	WithOrdinality bool
	WithOffset     bool
}

type TableSample struct {
	Method string
	Args   []Expr
	On     Expr
	Raw    string
}

func (*TableFunctionFrom) Kind() NodeKind { return NodeTableFunction }
func (*TableFunctionFrom) fromItemNode()  {}

type Identifier struct {
	Text   string
	Quoted bool
	// Quote retains the source delimiter for lossless dialect identity
	// generation. It is one of '"', '`', or '[' for bracketed identifiers.
	Quote byte
	Span  Span
}

type IdentifierExpr struct {
	nodeBase
	Parts []Identifier
}

func (*IdentifierExpr) Kind() NodeKind  { return NodeIdentifier }
func (*IdentifierExpr) expressionNode() {}

type LiteralKind uint8

const (
	LiteralString LiteralKind = iota
	LiteralNumber
	LiteralNull
	LiteralBoolean
	LiteralParameter
)

type LiteralExpr struct {
	nodeBase
	KindValue LiteralKind
	Raw       string
}

func (*LiteralExpr) Kind() NodeKind  { return NodeLiteral }
func (*LiteralExpr) expressionNode() {}

type StarExpr struct{ nodeBase }

func (*StarExpr) Kind() NodeKind  { return NodeStar }
func (*StarExpr) expressionNode() {}

type UnaryExpr struct {
	nodeBase
	Operator string
	Expr     Expr
}

func (*UnaryExpr) Kind() NodeKind  { return NodeUnary }
func (*UnaryExpr) expressionNode() {}

type BinaryExpr struct {
	nodeBase
	Left     Expr
	Operator string
	Right    Expr
	Escape   Expr
}

func (*BinaryExpr) Kind() NodeKind  { return NodeBinary }
func (*BinaryExpr) expressionNode() {}

type InExpr struct {
	nodeBase
	Value Expr
	Not   bool
	Items []Expr
	Query *SelectStmt
}

func (*InExpr) Kind() NodeKind  { return NodeIn }
func (*InExpr) expressionNode() {}

type BetweenExpr struct {
	nodeBase
	Value      Expr
	Not        bool
	Low        Expr
	High       Expr
	Symmetric  bool
	Asymmetric bool
}

func (*BetweenExpr) Kind() NodeKind  { return NodeBetween }
func (*BetweenExpr) expressionNode() {}

type IsExpr struct {
	nodeBase
	Value    Expr
	Operator string
	Right    Expr
}

func (*IsExpr) Kind() NodeKind  { return NodeIs }
func (*IsExpr) expressionNode() {}

type FunctionCallExpr struct {
	nodeBase
	Name         []Identifier
	RawArgs      string
	Distinct     bool
	Args         []Expr
	Having       Expr
	ArgumentTail string
	Star         bool
	ArrayLiteral bool
	OrderBy      []OrderItem
	IgnoreNulls  bool
	RespectNulls bool
	NullsInside  bool
	WithinGroup  []OrderItem
	Filter       Expr
	Over         *WindowSpec
}

func (*FunctionCallExpr) Kind() NodeKind  { return NodeFunctionCall }
func (*FunctionCallExpr) expressionNode() {}

// CallExpr represents a call whose callee is itself an expression, such as
// a[0].field() or make_caller()(arg). Named calls use FunctionCallExpr so
// callers can inspect their qualified name and window clause directly.
type CallExpr struct {
	nodeBase
	Callee Expr
	Args   []Expr
}

func (*CallExpr) Kind() NodeKind  { return NodeCall }
func (*CallExpr) expressionNode() {}

// GenericExpr preserves type-like angle-bracket expressions such as
// ARRAY<TEXT> without coupling the public AST to one dialect's type system.
type GenericExpr struct {
	nodeBase
	Target    Expr
	Arguments []Expr
}

func (*GenericExpr) Kind() NodeKind  { return NodeGeneric }
func (*GenericExpr) expressionNode() {}

// RawExpr preserves valid dialect-specific expression syntax that does not
// yet have a dedicated AST node, while retaining its source span.
type RawExpr struct {
	nodeBase
	Raw string
}

func (*RawExpr) Kind() NodeKind  { return NodeRawExpression }
func (*RawExpr) expressionNode() {}

type ExtractExpr struct {
	nodeBase
	Field  Expr
	Source Expr
}

func (*ExtractExpr) Kind() NodeKind  { return NodeExtract }
func (*ExtractExpr) expressionNode() {}

type TupleExpr struct {
	nodeBase
	Items []Expr
}

func (*TupleExpr) Kind() NodeKind  { return NodeTuple }
func (*TupleExpr) expressionNode() {}

type AliasExpr struct {
	nodeBase
	Expr  Expr
	Alias Identifier
}

func (*AliasExpr) Kind() NodeKind  { return NodeAlias }
func (*AliasExpr) expressionNode() {}

type IntervalExpr struct {
	nodeBase
	Value      Expr
	Qualifiers []Expr
}

func (*IntervalExpr) Kind() NodeKind  { return NodeInterval }
func (*IntervalExpr) expressionNode() {}

type CastExpr struct {
	nodeBase
	Keyword                string
	Value                  Expr
	Type                   Expr
	TypeSuffix             []Identifier
	preserveTypeParameters bool
}

func (*CastExpr) Kind() NodeKind  { return NodeCast }
func (*CastExpr) expressionNode() {}

type WindowedExpr struct {
	nodeBase
	Expr Expr
	Over WindowSpec
}

func (*WindowedExpr) Kind() NodeKind  { return NodeWindowed }
func (*WindowedExpr) expressionNode() {}

type ExistsExpr struct {
	nodeBase
	Not   bool
	Query *SelectStmt
}

func (*ExistsExpr) Kind() NodeKind  { return NodeExists }
func (*ExistsExpr) expressionNode() {}

type QuantifiedExpr struct {
	nodeBase
	Keyword          string
	Query            *SelectStmt
	SpaceBeforeParen bool
}

func (*QuantifiedExpr) Kind() NodeKind  { return NodeQuantified }
func (*QuantifiedExpr) expressionNode() {}

type GroupingExpr struct {
	nodeBase
	Name             string
	Args             []Expr
	SpaceBeforeParen bool
}

func (*GroupingExpr) Kind() NodeKind  { return NodeGrouping }
func (*GroupingExpr) expressionNode() {}

type SetExpr struct {
	nodeBase
	Left     Expr
	Operator string
	All      bool
	Right    Expr
}

func (*SetExpr) Kind() NodeKind  { return NodeSetExpression }
func (*SetExpr) expressionNode() {}

type SubqueryExpr struct {
	nodeBase
	Query         *SelectStmt
	Parenthesized bool
}

func (*SubqueryExpr) Kind() NodeKind  { return NodeSubqueryExpr }
func (*SubqueryExpr) expressionNode() {}

type FetchClause struct {
	Next     bool
	Count    Expr
	Percent  bool
	WithTies bool
}

type TypedLiteralExpr struct {
	nodeBase
	TypeName   []Identifier
	Parameters []Expr
	Qualifiers []string
	Value      *LiteralExpr
}

func (*TypedLiteralExpr) Kind() NodeKind  { return NodeTypedLiteral }
func (*TypedLiteralExpr) expressionNode() {}

type CaseWhen struct {
	Condition Expr
	Result    Expr
	Span      Span
}

type CaseExpr struct {
	nodeBase
	Operand Expr
	Whens   []CaseWhen
	Else    Expr
}

func (*CaseExpr) Kind() NodeKind  { return NodeCase }
func (*CaseExpr) expressionNode() {}

type IndexExpr struct {
	nodeBase
	Target  Expr
	Low     Expr
	High    Expr
	Step    Expr
	Slice   bool
	Indices []Expr
}

func (*IndexExpr) Kind() NodeKind  { return NodeIndex }
func (*IndexExpr) expressionNode() {}

type FieldExpr struct {
	nodeBase
	Target Expr
	Field  Identifier
}

func (*FieldExpr) Kind() NodeKind  { return NodeField }
func (*FieldExpr) expressionNode() {}

type ParenthesizedExpr struct {
	nodeBase
	Expr Expr
}

func (*ParenthesizedExpr) Kind() NodeKind  { return NodeParenthesized }
func (*ParenthesizedExpr) expressionNode() {}

type MissingExpr struct {
	nodeBase
	Expected string
}

func (*MissingExpr) Kind() NodeKind  { return NodeMissing }
func (*MissingExpr) expressionNode() {}

type ErrorExpr struct {
	nodeBase
	Tokens  []Token
	Message string
}

func (*ErrorExpr) Kind() NodeKind  { return NodeError }
func (*ErrorExpr) expressionNode() {}
