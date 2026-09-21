package golyglot

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Lower these functions after ordinary target conversion so synthesized SQL
// uses target expressions. Keep aggregate modifiers on the original AST node.
func normalizeTrinoDuckDBFunctions(root Node) (Node, error) {
	var failure error
	// Post-order: an outer call must see already lowered arguments.
	var calls []*FunctionCallExpr
	Walk(root, func(node Node) VisitAction {
		if fn, ok := node.(*FunctionCallExpr); ok {
			calls = append(calls, fn)
		}
		return VisitChildren
	})
	replacements := make(map[Node]Node)
	for i := len(calls) - 1; i >= 0; i-- {
		fn := calls[i]
		if len(fn.Name) != 1 || fn.Name[0].Quoted {
			continue
		}
		name := strings.ToUpper(fn.Name[0].Text)
		if name != "MAX_BY" && name != "MIN_BY" && name != "REGEXP_EXTRACT" && name != "REGEXP_REPLACE" && name != "TO_ISO8601" {
			continue
		}
		Transform(fn, func(node Node) Node {
			if replacement := replacements[node]; replacement != nil {
				return replacement
			}
			return node
		})
		switch name {
		case "MAX_BY", "MIN_BY":
			if len(fn.Args) == 2 {
				setFunctionName(fn, map[string]string{"MAX_BY": "ARG_MAX_NULL", "MIN_BY": "ARG_MIN_NULL"}[name])
			} else if len(fn.Args) == 3 {
				count, ok := fn.Args[2].(*LiteralExpr)
				if !ok || count.KindValue != LiteralNumber {
					failure = fmt.Errorf("cannot safely transpile %s to DuckDB: top-N requires a positive integer literal", name)
					continue
				}
				n, err := strconv.ParseInt(count.Raw, 10, 64)
				if err != nil || n <= 0 || fn.Distinct || len(fn.OrderBy) > 0 {
					failure = fmt.Errorf("cannot safely transpile %s to DuckDB: top-N requires a positive integer literal without DISTINCT or argument ordering", name)
					continue
				}
				// ARG_MAX/MIN(..., n) discard NULL values. Ordered LIST keeps
				// them, but excludes rows with NULL keys, like Trino does.
				var filter Expr = &IsExpr{Value: fn.Args[1], Operator: "IS NOT", Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}}
				if fn.Filter != nil {
					filter = &BinaryExpr{Left: filter, Operator: "AND", Right: fn.Filter}
				}
				list := &FunctionCallExpr{Name: []Identifier{{Text: "LIST"}}, Args: fn.Args[:1],
					OrderBy: []OrderItem{{Expr: fn.Args[1], Descending: name == "MAX_BY"}}, Filter: filter, Over: fn.Over}
				replacements[fn] = &FunctionCallExpr{Name: []Identifier{{Text: "LIST_SLICE"}}, Args: []Expr{list, &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}, count}}
			} else {
				failure = fmt.Errorf("cannot safely transpile %s to DuckDB: expected two or three arguments", name)
			}
		case "REGEXP_EXTRACT":
			if len(fn.Args) == 2 || len(fn.Args) == 3 {
				args := append([]Expr(nil), fn.Args...)
				if len(args) == 2 {
					args = append(args, &LiteralExpr{KindValue: LiteralNumber, Raw: "0"})
				}
				// Unlike NULLIF(extract, ''), this preserves successful empty and
				// unmatched optional captures, while no match yields SQL NULL.
				replacements[fn] = &FunctionCallExpr{Name: []Identifier{{Text: "LIST_EXTRACT"}}, Args: []Expr{
					&FunctionCallExpr{Name: []Identifier{{Text: "REGEXP_EXTRACT_ALL"}}, Args: args},
					&LiteralExpr{KindValue: LiteralNumber, Raw: "1"},
				}}
			}
		case "REGEXP_REPLACE":
			if len(fn.Args) < 2 || len(fn.Args) > 3 {
				continue
			}
			if len(fn.Args) == 2 {
				fn.Args = append(fn.Args, sqlStringLiteral(""))
			}
			replacement, ok := fn.Args[2].(*LiteralExpr)
			if !ok || replacement.KindValue != LiteralString {
				failure = fmt.Errorf("cannot safely transpile REGEXP_REPLACE to DuckDB: replacement must be a string literal (dynamic and lambda replacements are not supported)")
				continue
			}
			var pattern *regexp.Regexp
			if literal, ok := fn.Args[1].(*LiteralExpr); ok && literal.KindValue == LiteralString {
				var err error
				pattern, err = regexp.Compile(unquoteSQLString(literal.Raw))
				if err != nil {
					failure = fmt.Errorf("cannot translate regex pattern to DuckDB: %w", err)
					continue
				}
				fn.Args[1] = sqlStringLiteral(duckDBNamedRegexGroups(unquoteSQLString(literal.Raw)))
			}
			converted, err := trinoRegexReplacement(unquoteSQLString(replacement.Raw), pattern)
			if err != nil {
				failure = err
				continue
			}
			fn.Args[2] = sqlStringLiteral(converted)
			fn.Args = append(fn.Args, sqlStringLiteral("g"))
		case "TO_ISO8601":
			if len(fn.Args) != 1 {
				continue
			}
			const value = "__golyglot_iso8601_value"
			precision := trinoTimestampPrecision(fn.Args[0])
			if precision > 6 {
				failure = fmt.Errorf("TO_ISO8601 timestamp precision %d cannot be preserved in DuckDB", precision)
				continue
			}
			format := "STRFTIME(" + value + ", '%Y-%m-%dT%H:%M:%S')"
			if precision > 0 {
				format += " || '.' || LEFT(STRFTIME(" + value + ", '%f'), " + strconv.Itoa(precision) + ")"
			}
			factor := 1
			for i := precision; i < 6; i++ {
				factor *= 10
			}
			format = "CASE WHEN DATE_PART('microsecond', " + value + ") % " + strconv.Itoa(factor) + " <> 0 THEN ERROR('TO_ISO8601: source timestamp precision is unknown; provide an explicit TIMESTAMP precision') ELSE " + format + " END"
			// DuckDB has no source timezone identity on TIMESTAMPTZ values.
			// Fail explicitly instead of silently returning a different offset.
			body := &RawExpr{Raw: "CASE WHEN TYPEOF(" + value + ") = 'DATE' THEN STRFTIME(" + value + ", '%Y-%m-%d') WHEN TYPEOF(" + value + ") IN ('TIMESTAMP', 'TIMESTAMP_MS', 'TIMESTAMP_S') THEN " + format + " ELSE ERROR('TO_ISO8601: this DuckDB translation supports DATE and TIMESTAMP without time zone') END"}
			// Bind the input once: repeated formatting must not re-evaluate
			// volatile functions. A single-element list also retains SQL NULL.
			mapped := &FunctionCallExpr{Name: []Identifier{{Text: "LIST_TRANSFORM"}}, Args: []Expr{
				&FunctionCallExpr{Name: []Identifier{{Text: "ARRAY"}}, ArrayLiteral: true, Args: fn.Args},
				&RawExpr{Raw: "LAMBDA " + value + ": " + body.Raw},
			}}
			replacements[fn] = &FunctionCallExpr{Name: []Identifier{{Text: "LIST_EXTRACT"}}, Args: []Expr{mapped, &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}}}
		}
	}
	if failure != nil {
		return nil, failure
	}
	return Transform(root, func(node Node) Node {
		if replacement := replacements[node]; replacement != nil {
			return replacement
		}
		return node
	}), nil
}

func duckDBNamedRegexGroups(pattern string) string {
	var out strings.Builder
	class := false
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' && i+1 < len(pattern) {
			out.WriteString(pattern[i : i+2])
			i++
			continue
		}
		if pattern[i] == '[' {
			class = true
		}
		if pattern[i] == ']' {
			class = false
		}
		if !class && strings.HasPrefix(pattern[i:], "(?<") {
			out.WriteString("(?P<")
			i += 2
		} else {
			out.WriteByte(pattern[i])
		}
	}
	return out.String()
}

func trinoTimestampPrecision(expression Expr) int {
	if paren, ok := expression.(*ParenthesizedExpr); ok {
		return trinoTimestampPrecision(paren.Expr)
	}
	if typed, ok := expression.(*TypedLiteralExpr); ok {
		// Timestamp literals carry exactly their written fractional width.
		// An unqualified TIMESTAMP cast instead defaults to precision three.
		value := unquoteSQLString(typed.Value.Raw)
		if dot := strings.LastIndexByte(value, '.'); dot >= 0 {
			digits := 0
			for i := dot + 1; i < len(value) && value[i] >= '0' && value[i] <= '9'; i++ {
				digits++
			}
			return digits
		}
		return 0
	}
	if cast, ok := expression.(*CastExpr); ok {
		typeSQL := builderExprSQL(cast.Type)
		if open := strings.IndexByte(typeSQL, '('); open >= 0 && strings.Contains(strings.ToUpper(typeSQL[:open]), "TIMESTAMP") {
			if close := strings.IndexByte(typeSQL[open:], ')'); close > 0 {
				if p, err := strconv.Atoi(strings.TrimSpace(typeSQL[open+1 : open+close])); err == nil {
					return p
				}
			}
		}
	}
	return 3
}

func sqlStringLiteral(value string) *LiteralExpr {
	return &LiteralExpr{KindValue: LiteralString, Raw: "'" + strings.ReplaceAll(value, "'", "''") + "'"}
}

func unquoteSQLString(value string) string {
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	return value
}

func trinoRegexReplacement(value string, pattern *regexp.Regexp) (string, error) {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case '\\':
			i++
			if i == len(value) {
				return "", fmt.Errorf("REGEXP_REPLACE replacement has an unfinished escape")
			}
			if value[i] == '\\' {
				out.WriteByte('\\')
			}
			out.WriteByte(value[i])
		case '$':
			i++
			if i == len(value) {
				return "", fmt.Errorf("REGEXP_REPLACE replacement has an unfinished group reference")
			}
			group := -1
			if value[i] == '{' {
				end := strings.IndexByte(value[i:], '}')
				if end < 0 || pattern == nil {
					return "", fmt.Errorf("REGEXP_REPLACE named groups require a literal pattern and a complete name")
				}
				group = pattern.SubexpIndex(value[i+1 : i+end])
				i += end
			} else if value[i] >= '0' && value[i] <= '9' {
				group = int(value[i] - '0')
				for i+1 < len(value) && value[i+1] >= '0' && value[i+1] <= '9' {
					if pattern == nil {
						return "", fmt.Errorf("REGEXP_REPLACE multi-digit groups require a literal pattern")
					}
					next := group*10 + int(value[i+1]-'0')
					if next > pattern.NumSubexp() {
						break
					}
					group = next
					i++
				}
			}
			if group < 0 || group > 9 || (pattern != nil && group > pattern.NumSubexp()) {
				return "", fmt.Errorf("REGEXP_REPLACE capture group cannot be represented safely in DuckDB")
			}
			out.WriteByte('\\')
			out.WriteString(strconv.Itoa(group))
		default:
			out.WriteByte(value[i])
		}
	}
	return out.String(), nil
}
