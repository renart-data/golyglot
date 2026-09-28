package golyglot

import (
	"fmt"
	"strconv"
	"strings"
)

// Keep these dialects' source semantics separate from shared target spelling.
// In particular Vertica's statement clock, integer widths and NULL ordering
// are not PostgreSQL defaults, despite their similar SQL syntax.
func prepareHANAVertica(root Node, source, target Dialect) (Node, error) {
	if source != DialectHANA && source != DialectVertica && target != DialectHANA && target != DialectVertica {
		return root, nil
	}
	if source == DialectHANA && target != source {
		var err error
		root, err = lowerHANADummy(root, target)
		if err != nil {
			return nil, err
		}
	}
	var conversionErr error
	orderScopes := verticaCompoundOrderScopes(root)
	root = Transform(root, func(node Node) Node {
		if conversionErr != nil {
			return node
		}
		if conversionErr = validateHANAVerticaConversion(node, source, target); conversionErr != nil {
			return node
		}
		if source != target && source != DialectVertica && (source == DialectHANA || target == DialectHANA || target == DialectVertica) {
			if conversionErr = explicitHANAVerticaOrder(node, source); conversionErr != nil {
				return node
			}
		}
		switch value := node.(type) {
		case *RawStmt:
			if target == DialectHANA {
				value.Raw = normalizeHANAAlter(value.Raw)
			}
		case *CastExpr:
			if target == DialectVertica && (strings.EqualFold(value.Keyword, "TRY_CAST") || strings.EqualFold(value.Keyword, "SAFE_CAST")) {
				conversionErr = fmt.Errorf("golyglot: cannot transpile safe casts to Vertica: ::! does not suppress constant conversion errors")
			}
			if source == DialectVertica {
				rewriteCastType(value, DialectVertica)
			}
		case *IdentifierExpr:
			if len(value.Parts) == 1 && !value.Parts[0].Quoted && strings.EqualFold(value.Parts[0].Text, "SYSDATE") && (source == DialectVertica || target == DialectVertica && source == DialectRedshift) {
				return verticaClock("GETDATE", target, &conversionErr)
			}
		case *BinaryExpr:
			if target == DialectVertica && value.Operator == "!=" {
				value.Operator = "<>"
			}
			if source == DialectVertica && value.Operator == "//" && target == DialectPostgreSQL {
				return dialectCall("DIV", value.Left, value.Right)
			}
		case *UnaryExpr:
			if source == DialectVertica {
				if value.Operator == "|/" {
					return dialectCall("SQRT", value.Expr)
				}
				if value.Operator == "||/" {
					return dialectCall("CBRT", value.Expr)
				}
			}
		case *FunctionCallExpr:
			if source == DialectVertica && target != source {
				verticaAnalyticOrder(value.WithinGroup)
				if value.Over != nil {
					verticaAnalyticOrder(value.Over.OrderBy)
				}
			}
			if len(value.Name) != 1 || value.Name[0].Quoted || value.RawArgs != "" {
				return node
			}
			name := strings.ToUpper(value.Name[0].Text)
			if source == DialectVertica {
				switch name {
				case "GETDATE", "GETUTCDATE", "SYSDATE":
					if len(value.Args) == 0 {
						return verticaClock(name, target, &conversionErr)
					}
				case "TIMESTAMPADD":
					if target != DialectVertica && len(value.Args) == 3 {
						return verticaTimestampAdd(value, target, &conversionErr)
					}
				case "ZEROIFNULL":
					if target != DialectVertica && target != DialectSnowflake && len(value.Args) == 1 {
						name := "COALESCE"
						if target == DialectOracle {
							name = "NVL"
						}
						return dialectCall(name, value.Args[0], numberExpr(0))
					}
				case "NULLIFZERO":
					if target != DialectVertica && len(value.Args) == 1 {
						return dialectCall("NULLIF", value.Args[0], numberExpr(0))
					}
				case "DECODE":
					if target == DialectPostgreSQL || target == DialectDuckDB {
						return rewriteDecodeFunction(value)
					}
				case "APPROXIMATE_COUNT_DISTINCT":
					if target == DialectDuckDB || target == DialectSnowflake {
						setFunctionName(value, "APPROX_COUNT_DISTINCT")
					}
				}
			}
			if target == DialectVertica {
				switch name {
				case "IFNULL", "ISNULL":
					if len(value.Args) == 2 {
						setFunctionName(value, "COALESCE")
					}
				case "DATEADD", "DATE_ADD", "TIMESTAMPADD", "DATEDIFF", "TIMESTAMPDIFF":
					if name == "DATEDIFF" && source == DialectMySQL && len(value.Args) == 2 {
						value.Args = []Expr{identifierExpr("DAY"), value.Args[1], value.Args[0]}
					}
					if len(value.Args) == 3 {
						if name == "TIMESTAMPDIFF" {
							setFunctionName(value, "DATEDIFF")
						}
						if name == "DATEADD" || name == "DATE_ADD" {
							setFunctionName(value, "TIMESTAMPADD")
						}
						upperDateUnit(value.Args[0])
					}
				case "APPROX_COUNT_DISTINCT":
					setFunctionName(value, "APPROXIMATE_COUNT_DISTINCT")
				case "IF", "IFF":
					if len(value.Args) == 3 {
						return &CaseExpr{nodeBase: value.nodeBase, Whens: []CaseWhen{{Condition: value.Args[0], Result: value.Args[1]}}, Else: value.Args[2]}
					}
				case "CHARINDEX":
					if len(value.Args) == 2 {
						setFunctionName(value, "INSTR")
						value.Args[0], value.Args[1] = value.Args[1], value.Args[0]
					}
				case "COUNTIF", "COUNT_IF":
					if len(value.Args) == 1 {
						condition := value.Args[0]
						if value.Filter != nil {
							condition = &BinaryExpr{Left: condition, Operator: "AND", Right: value.Filter}
							value.Filter = nil
						}
						var otherwise Expr = numberExpr(0)
						setFunctionName(value, "SUM")
						if source == DialectBigQuery {
							setFunctionName(value, "COUNT")
							otherwise = nil
						}
						value.Args = []Expr{&CaseExpr{Whens: []CaseWhen{{Condition: condition, Result: numberExpr(1)}}, Else: otherwise}}
					}
				case "LISTAGG":
					if source == DialectVertica {
						if len(value.Args) == 2 && value.ArgumentTail == "" {
							value.ArgumentTail = "USING PARAMETERS separator = " + renderDialectExpr(value.Args[1], target)
							value.Args = value.Args[:1]
						} else if value.ArgumentTail != "" {
							value.ArgumentTail = normalizeVerticaParameters(value.ArgumentTail)
						}
					}
				}
			}
		case *WindowedExpr:
			if source == DialectVertica && target != source {
				verticaAnalyticOrder(value.Over.OrderBy)
			}
		case *SelectStmt:
			if target == DialectVertica {
				if orderScopes[value] != nil && len(value.OrderBy) > 0 {
					conversionErr = fmt.Errorf("golyglot: Vertica compound-query NULL ordering requires a derived table")
					return node
				}
				value.OrderBy, conversionErr = verticaTargetOrder(value, source)
			} else if source == DialectVertica {
				conversionErr = verticaSourceOrder(value, orderScopes[value])
				for i := range value.Windows {
					verticaAnalyticOrder(value.Windows[i].Spec.OrderBy)
				}
			}
		case *CreateTableStmt:
			if source == DialectVertica {
				value.Tail = normalizeCreateTableTail(value.Tail, DialectVertica)
			}
		}
		return node
	})
	return root, conversionErr
}

func dialectCall(name string, args ...Expr) *FunctionCallExpr {
	return &FunctionCallExpr{Name: []Identifier{{Text: name}}, Args: args}
}

func numberExpr(value int) *LiteralExpr {
	return &LiteralExpr{KindValue: LiteralNumber, Raw: strconv.Itoa(value)}
}

func upperDateUnit(expression Expr) {
	if id, ok := expression.(*IdentifierExpr); ok && len(id.Parts) == 1 && !id.Parts[0].Quoted {
		id.Parts[0].Text = strings.ToUpper(id.Parts[0].Text)
	}
}

func verticaClock(name string, target Dialect, conversionErr *error) Expr {
	if name == "SYSDATE" {
		name = "GETDATE"
	}
	if target == DialectVertica || target == DialectTSQL {
		return dialectCall(name)
	}
	if target == DialectPostgreSQL {
		var value Expr = dialectCall("STATEMENT_TIMESTAMP")
		if name == "GETUTCDATE" {
			value = &BinaryExpr{Left: value, Operator: "AT TIME ZONE", Right: &LiteralExpr{KindValue: LiteralString, Raw: "'UTC'"}}
		}
		return &CastExpr{Keyword: "CAST", Value: value, Type: identifierExpr("TIMESTAMP")}
	}
	*conversionErr = fmt.Errorf("golyglot: Vertica statement-start timestamps have no verified mapping to %s", target)
	return dialectCall(name)
}

func verticaTimestampAdd(function *FunctionCallExpr, target Dialect, conversionErr *error) Expr {
	unit := verticaDateUnit(strings.Trim(renderExpr(function.Args[0]), "'"))
	if unit == "" {
		*conversionErr = fmt.Errorf("golyglot: unsupported Vertica TIMESTAMPADD unit %q", renderExpr(function.Args[0]))
		return function
	}
	amount, value := function.Args[1], function.Args[2]
	interval := &IntervalExpr{Value: amount, Qualifiers: []Expr{identifierExpr(unit)}}
	switch target {
	case DialectPostgreSQL:
		var step Expr = &IntervalExpr{Value: &LiteralExpr{KindValue: LiteralString, Raw: "'1 " + unit + "'"}}
		if literal, ok := amount.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
			step = &IntervalExpr{Value: &LiteralExpr{KindValue: LiteralString, Raw: "'" + literal.Raw + " " + unit + "'"}}
		} else {
			step = &BinaryExpr{Left: step, Operator: "*", Right: amount}
		}
		return &BinaryExpr{Left: value, Operator: "+", Right: step}
	case DialectDuckDB:
		return &BinaryExpr{Left: value, Operator: "+", Right: interval}
	case DialectMySQL:
		return dialectCall("DATE_ADD", value, interval)
	case DialectSnowflake, DialectTSQL:
		return dialectCall("DATEADD", identifierExpr(unit), amount, value)
	default:
		*conversionErr = fmt.Errorf("golyglot: Vertica TIMESTAMPADD has no verified mapping to %s", target)
		return function
	}
}

// Vertica only accepts NULLS FIRST/LAST in analytic ORDER BY. At query level
// use a separate null-rank key, since its default depends on the value's type.
func verticaTargetOrder(query *SelectStmt, source Dialect) ([]OrderItem, error) {
	var result []OrderItem
	for _, item := range query.OrderBy {
		if item.NullsFirst || item.NullsLast {
			// DISTINCT requires ordering expressions to be projected. Adding
			// a hidden null-rank projection changes the public result shape.
			if query.Distinct || len(query.DistinctOn) > 0 {
				return nil, fmt.Errorf("golyglot: Vertica NULL ordering with DISTINCT requires a derived table")
			}
			// A positional key inside CASE becomes a constant. A projection
			// alias inside CASE need not be visible there, and evaluating a
			// volatile expression twice changes its result. Require a real
			// column until a derived-table lowering can preserve these cases.
			id, ok := item.Expr.(*IdentifierExpr)
			if !ok || query.SetLeft != nil {
				return nil, fmt.Errorf("golyglot: Vertica NULL ordering requires a column sort key; ordinal, expression and set-operation keys need a derived table")
			}
			if len(id.Parts) == 1 {
				for _, projection := range query.Projections {
					if projection.Alias != nil && identifierKey(*projection.Alias, source) == identifierKey(id.Parts[0], source) {
						column, ok := projection.Expr.(*IdentifierExpr)
						if !ok || len(column.Parts) != 1 || identifierKey(column.Parts[0], source) != identifierKey(id.Parts[0], source) {
							return nil, fmt.Errorf("golyglot: Vertica NULL ordering for projection aliases requires a derived table")
						}
					}
				}
			}
			first, last := 1, 0
			if item.NullsFirst {
				first, last = 0, 1
			}
			result = append(result, OrderItem{Expr: &CaseExpr{Whens: []CaseWhen{{Condition: &IsExpr{Value: item.Expr, Operator: "IS", Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}}, Result: numberExpr(first)}}, Else: numberExpr(last)}})
			item.NullsFirst, item.NullsLast = false, false
		}
		result = append(result, item)
	}
	return result, nil
}

func normalizeHANAAlter(raw string) string {
	text := canonicalRawSQL(raw)
	if !strings.HasPrefix(strings.ToUpper(text), "ALTER TABLE ") {
		return text
	}
	add := indexKeywordTopLevel(text, "ADD")
	if add < 0 {
		return text
	}
	tail := strings.TrimSpace(text[add+len("ADD"):])
	if strings.HasPrefix(tail, "(") && matchingParenIndex(tail, 0) == len(tail)-1 {
		return text[:add] + "ADD " + normalizeCreateTableTail(tail, DialectHANA)
	}
	return text
}

func normalizeVerticaParameters(tail string) string {
	const prefix = "USING PARAMETERS"
	text := canonicalRawSQL(tail)
	if !strings.HasPrefix(strings.ToUpper(text), prefix+" ") {
		return text
	}
	parameters := splitTopLevelSQL(strings.TrimSpace(text[len(prefix):]), ',')
	for i, parameter := range parameters {
		assignment := splitTopLevelSQL(parameter, '=')
		if len(assignment) != 2 {
			return text
		}
		parameters[i] = strings.TrimSpace(assignment[0]) + " = " + strings.TrimSpace(assignment[1])
	}
	return prefix + " " + strings.Join(parameters, ", ")
}

func hanaVerticaTypeName(name string, dialect Dialect) string {
	if strings.HasPrefix(strings.TrimSpace(name), "\"") {
		return name
	}
	upper := strings.ToUpper(strings.Join(strings.Fields(name), " "))
	if dialect == DialectHANA {
		if upper == "INTEGER" {
			return "INT"
		}
		return upper
	}
	switch upper {
	case "INT", "INTEGER", "INT8", "INT4", "INT2", "SMALLINT", "TINYINT", "BIGINT":
		return "BIGINT"
	case "REAL", "FLOAT", "FLOAT8", "FLOAT4", "DOUBLE", "DOUBLE PRECISION":
		return "DOUBLE PRECISION"
	case "TEXT":
		return "LONG VARCHAR"
	case "BYTEA", "BINARY VARYING":
		return "VARBINARY"
	case "NUMERIC":
		return "DECIMAL"
	case "TIMESTAMP WITH TIME ZONE":
		return "TIMESTAMPTZ"
	case "TIME WITH TIME ZONE":
		return "TIMETZ"
	}
	return upper
}

func hanaVerticaDDLType(typeToken, constraints string, dialect Dialect) (string, string) {
	if dialect != DialectVertica {
		return typeToken, constraints
	}
	first, rest := splitLeadingSQLToken(strings.TrimSpace(constraints))
	switch strings.ToUpper(typeToken) {
	case "DOUBLE":
		if strings.EqualFold(first, "PRECISION") {
			return "DOUBLE PRECISION", rest
		}
	case "BINARY":
		if strings.HasPrefix(strings.ToUpper(first), "VARYING") {
			return "VARBINARY" + first[len("VARYING"):], rest
		}
	case "TIMESTAMP", "TIME":
		if strings.HasPrefix(strings.ToUpper(constraints), "WITH TIME ZONE") {
			return strings.ToUpper(typeToken) + "TZ", strings.TrimSpace(constraints[len("WITH TIME ZONE"):])
		}
	}
	return typeToken, constraints
}
