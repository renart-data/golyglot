package golyglot

import (
	"fmt"
	"strconv"
	"strings"
)

func transformExpr(expression Expr, target Dialect) Expr {
	return (targetTransformer{target: target}).expr(expression, target)
}

func (transformer targetTransformer) expr(expression Expr, target Dialect) Expr {
	if expression == nil {
		return nil
	}
	transformExpr := transformer.expr
	transformSelect := transformer.selectStatement
	transformOrderItem := transformer.orderItem
	transformWindow := transformer.window
	rewriteOrderItems := transformer.orderItems
	if transformer.rewritePostgreSQLSourceFunctions {
		if function, ok := expression.(*FunctionCallExpr); ok {
			if rewritten := rewriteGenericSourceFunction(function, target); rewritten != nil {
				expression = rewritten
			}
		}
	}
	switch expression := expression.(type) {
	case *LiteralExpr:
		if expression.KindValue == LiteralBoolean || expression.KindValue == LiteralNull {
			expression.Raw = strings.ToUpper(expression.Raw)
		}
		if target == DialectSpark && expression.KindValue == LiteralString && len(expression.Raw) >= 4 && expression.Raw[0] == '\'' && expression.Raw[len(expression.Raw)-1] == '\'' && strings.Contains(expression.Raw[1:len(expression.Raw)-1], "''") {
			content := strings.ReplaceAll(expression.Raw[1:len(expression.Raw)-1], "''", "'")
			expression.Raw = "'" + strings.ReplaceAll(content, "'", `\'`) + "'"
		}
		if target == DialectMySQL && expression.KindValue == LiteralString && expression.Raw == "'\\0'" {
			expression.Raw = "'" + string(byte(0)) + "'"
		}
		if target == DialectClickHouse && expression.KindValue == LiteralString && strings.Contains(expression.Raw, string(byte(0))) {
			expression.Raw = "'\\0'"
		}
		if target == DialectTSQL && expression.KindValue == LiteralBoolean {
			expression.KindValue = LiteralNumber
			if strings.EqualFold(expression.Raw, "TRUE") {
				expression.Raw = "1"
			} else {
				expression.Raw = "0"
			}
		}
		if target == DialectDuckDB && expression.KindValue == LiteralNumber {
			expression.Raw = strings.ReplaceAll(expression.Raw, "_", "")
			if strings.HasPrefix(strings.ToLower(expression.Raw), "0x") {
				if value, err := strconv.ParseUint(expression.Raw[2:], 16, 64); err == nil {
					expression.Raw = strconv.FormatUint(value, 10)
				}
			}
		}
		if target == DialectSnowflake && expression.KindValue == LiteralString {
			if value, ok := dollarQuotedValue(expression.Raw); ok {
				value = strings.ReplaceAll(value, "\\", "\\\\")
				expression.Raw = "'" + strings.ReplaceAll(value, "'", "\\'") + "'"
			} else {
				expression.Raw = strings.ReplaceAll(expression.Raw, "\n", `\n`)
				if len(expression.Raw) >= 2 {
					inner := strings.ReplaceAll(expression.Raw[1:len(expression.Raw)-1], "''", `\'`)
					expression.Raw = expression.Raw[:1] + inner + expression.Raw[len(expression.Raw)-1:]
				}
			}
		}
		if target == DialectDuckDB && expression.KindValue == LiteralString {
			if value, ok := dollarQuotedValue(expression.Raw); ok {
				expression.Raw = "'" + strings.ReplaceAll(value, "'", "''") + "'"
			}
		}
		if target == DialectBigQuery && expression.KindValue == LiteralString {
			if normalized, ok := normalizeBigQueryStringForDialect(expression.Raw, DialectBigQuery); ok {
				expression.Raw = normalized
			}
		}
		if target == DialectBigQuery && expression.KindValue == LiteralParameter && strings.HasPrefix(expression.Raw, "$") {
			expression.Raw = "@" + strings.TrimPrefix(expression.Raw, "$")
		}
		if target == DialectDuckDB && expression.KindValue == LiteralParameter && strings.HasPrefix(expression.Raw, "@") && len(expression.Raw) > 1 {
			return &FunctionCallExpr{Name: []Identifier{{Text: "ABS"}}, Args: []Expr{identifierExpr(strings.TrimPrefix(expression.Raw, "@"))}}
		}
		if (target == DialectSpark || target == DialectHive || target == DialectDatabricks) && expression.KindValue == LiteralParameter && strings.HasPrefix(expression.Raw, "@") && len(expression.Raw) > 1 {
			return &RawExpr{Raw: "${" + strings.TrimPrefix(expression.Raw, "@") + "}"}
		}
	case *IdentifierExpr:
		splitQualifiedIdentifierTarget(expression, target)
		for i := range expression.Parts {
			normalizeIdentifierTarget(&expression.Parts[i], target)
		}
		if target == DialectSnowflake && len(expression.Parts) == 1 && !expression.Parts[0].Quoted && strings.EqualFold(expression.Parts[0].Text, "LOCALTIMESTAMP") {
			expression.Parts[0].Text = "CURRENT_TIMESTAMP"
		}
		if target == DialectSpark && len(expression.Parts) == 1 && !expression.Parts[0].Quoted && strings.EqualFold(expression.Parts[0].Text, "SYSTEM_USER") {
			return &FunctionCallExpr{Name: []Identifier{{Text: "CURRENT_USER"}}}
		}
		if target == DialectBigQuery && len(expression.Parts) == 1 && strings.HasPrefix(expression.Parts[0].Text, "$") {
			expression.Parts[0].Text = "@" + strings.TrimPrefix(expression.Parts[0].Text, "$")
		}
		if target == DialectBigQuery && len(expression.Parts) == 1 && !expression.Parts[0].Quoted {
			switch strings.ToUpper(expression.Parts[0].Text) {
			case "CURRENT_DATETIME", "CURRENT_TIME", "CURRENT_TIMESTAMP":
				return &FunctionCallExpr{Name: []Identifier{{Text: strings.ToUpper(expression.Parts[0].Text)}}}
			}
		}
		if target == DialectGeneric && len(expression.Parts) == 1 && !expression.Parts[0].Quoted {
			switch strings.ToUpper(expression.Parts[0].Text) {
			case "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP", "CURRENT_DATETIME":
				expression.Parts[0].Text = strings.ToUpper(expression.Parts[0].Text)
			}
		}
		if target == DialectDuckDB && len(expression.Parts) == 1 && !expression.Parts[0].Quoted {
			switch strings.ToUpper(expression.Parts[0].Text) {
			case "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP":
				expression.Parts[0].Text = strings.ToUpper(expression.Parts[0].Text)
			}
			if target == DialectClickHouse && len(expression.Parts) == 1 && !expression.Parts[0].Quoted {
				switch strings.ToUpper(expression.Parts[0].Text) {
				case "CURRENT_DATE", "CURRENT_TIMESTAMP", "CURRENT_TIME":
					return &FunctionCallExpr{Name: []Identifier{{Text: strings.ToUpper(expression.Parts[0].Text)}}}
				}
			}
		}
	case *UnaryExpr:
		expression.Expr = transformExpr(expression.Expr, target)
		if target == DialectPresto || target == DialectTrino {
			if expression.Operator == "~" {
				return &FunctionCallExpr{Name: []Identifier{{Text: "BITWISE_NOT"}}, Args: []Expr{expression.Expr}}
			}
		}
		if target == DialectSnowflake && expression.Operator == "~" {
			return &FunctionCallExpr{Name: []Identifier{{Text: "BITNOT"}}, Args: []Expr{expression.Expr}}
		}
		if target == DialectTSQL && strings.EqualFold(expression.Operator, "NOT") {
			if _, ok := expression.Expr.(*IdentifierExpr); ok {
				return &UnaryExpr{
					Operator: "NOT",
					Expr: &BinaryExpr{
						Left:     expression.Expr,
						Operator: "<>",
						Right:    &LiteralExpr{KindValue: LiteralNumber, Raw: "0"},
					},
				}
			}
		}
	case *BinaryExpr:
		leftBoolean := false
		leftBooleanRaw := "TRUE"
		if literal, ok := expression.Left.(*LiteralExpr); ok {
			leftBoolean = literal.KindValue == LiteralBoolean
			leftBooleanRaw = literal.Raw
		}
		rightBoolean := false
		rightBooleanRaw := "TRUE"
		if literal, ok := expression.Right.(*LiteralExpr); ok {
			rightBoolean = literal.KindValue == LiteralBoolean
			rightBooleanRaw = literal.Raw
		}
		if target == DialectDuckDB {
			if value, ok := duckDBAtValue(expression.Left); ok {
				return &FunctionCallExpr{
					Name: []Identifier{{Text: "ABS"}},
					Args: []Expr{&BinaryExpr{Left: transformExpr(value, target), Operator: expression.Operator, Right: transformExpr(expression.Right, target)}},
				}
			}
			if isDuckDBAtMarker(expression.Left) && (expression.Operator == "-" || expression.Operator == "+") {
				return &FunctionCallExpr{
					Name: []Identifier{{Text: "ABS"}},
					Args: []Expr{&UnaryExpr{Operator: expression.Operator, Expr: transformExpr(expression.Right, target)}},
				}
			}
		}
		expression.Left = transformExpr(expression.Left, target)
		expression.Right = transformExpr(expression.Right, target)
		expression.Escape = transformExpr(expression.Escape, target)
		if target == DialectSnowflake {
			expression.Left = normalizeSnowflakeDateArithmetic(expression.Left)
			expression.Right = normalizeSnowflakeDateArithmetic(expression.Right)
		}
		if target == DialectSnowflake && (expression.Operator == "+" || expression.Operator == "-" || expression.Operator == "AND" || expression.Operator == "OR") {
			if identifier, ok := expression.Left.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && strings.EqualFold(identifier.Parts[0].Text, "CURRENT_TIMESTAMP") {
				expression.Left = &FunctionCallExpr{Name: []Identifier{{Text: "CURRENT_TIMESTAMP"}}}
			}
			if identifier, ok := expression.Right.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && strings.EqualFold(identifier.Parts[0].Text, "CURRENT_TIMESTAMP") {
				expression.Right = &FunctionCallExpr{Name: []Identifier{{Text: "CURRENT_TIMESTAMP"}}}
			}
		}
		if target == DialectSnowflake {
			switch expression.Operator {
			case "&":
				return &FunctionCallExpr{Name: []Identifier{{Text: "BITAND"}}, Args: []Expr{expression.Left, expression.Right}}
			case "|":
				return &FunctionCallExpr{Name: []Identifier{{Text: "BITOR"}}, Args: []Expr{expression.Left, expression.Right}}
			case "^":
				return &FunctionCallExpr{Name: []Identifier{{Text: "BITXOR"}}, Args: []Expr{expression.Left, expression.Right}}
			}
		}
		if target == DialectPresto || target == DialectTrino || target == DialectSpark {
			mapped := ""
			switch target {
			case DialectPresto, DialectTrino:
				mapped = map[string]string{
					"&":  "BITWISE_AND",
					"|":  "BITWISE_OR",
					"<<": "BITWISE_LEFT_SHIFT",
					">>": "BITWISE_RIGHT_SHIFT",
				}[expression.Operator]
			case DialectSpark:
				mapped = map[string]string{
					"<<": "SHIFTLEFT",
					">>": "SHIFTRIGHT",
				}[expression.Operator]
			}
			if mapped != "" {
				return &FunctionCallExpr{Name: []Identifier{{Text: mapped}}, Args: []Expr{expression.Left, expression.Right}}
			}
		}
		if target == DialectDuckDB && (expression.Operator == "~" || expression.Operator == "!~") {
			match := &FunctionCallExpr{
				nodeBase: nodeBase{span: expression.SourceSpan()},
				Name:     []Identifier{{Text: "REGEXP_FULL_MATCH"}},
				Args:     []Expr{expression.Left, expression.Right},
			}
			if expression.Operator == "!~" {
				return &UnaryExpr{nodeBase: nodeBase{span: expression.SourceSpan()}, Operator: "NOT", Expr: match}
			}
			return match
		}
		if target == DialectDuckDB {
			switch expression.Operator {
			case "^", "**":
				return &FunctionCallExpr{
					nodeBase: nodeBase{span: expression.SourceSpan()},
					Name:     []Identifier{{Text: "POWER"}},
					Args:     []Expr{expression.Left, expression.Right},
				}
			case "~~":
				expression.Operator = "LIKE"
			case "~~~":
				expression.Operator = "GLOB"
			case "!~~":
				expression.Operator = "NOT LIKE"
			case "!~~*":
				expression.Operator = "NOT ILIKE"
			case "^@":
				return &FunctionCallExpr{
					nodeBase: nodeBase{span: expression.SourceSpan()},
					Name:     []Identifier{{Text: "STARTS_WITH"}},
					Args:     []Expr{expression.Left, expression.Right},
				}
			}
		}
		if target == DialectDuckDB && (expression.Operator == "->" || expression.Operator == "->>") {
			expression.Right = normalizeDuckDBJSONPath(expression.Right)
		}
		if target == DialectSnowflake && (expression.Operator == "->" || expression.Operator == "->>") {
			return &FunctionCallExpr{Name: []Identifier{{Text: "GET_PATH"}}, Args: []Expr{expression.Left, normalizeSnowflakeJSONPath(expression.Right)}}
		}
		if target == DialectTSQL && (strings.EqualFold(expression.Operator, "AND") || strings.EqualFold(expression.Operator, "OR")) {
			if leftBoolean {
				expression.Left = booleanOperandTSQL(&LiteralExpr{KindValue: LiteralBoolean, Raw: leftBooleanRaw})
			} else {
				expression.Left = booleanOperandTSQL(expression.Left)
			}
			if rightBoolean {
				expression.Right = booleanOperandTSQL(&LiteralExpr{KindValue: LiteralBoolean, Raw: rightBooleanRaw})
			} else {
				expression.Right = booleanOperandTSQL(expression.Right)
			}
		}
		if target == DialectTSQL && (leftBoolean || rightBoolean) {
			switch strings.ToUpper(strings.TrimSpace(expression.Operator)) {
			case "!=":
				expression.Operator = "<>"
			case "IS":
				expression.Operator = "="
			case "IS NOT":
				return &UnaryExpr{Operator: "NOT", Expr: &BinaryExpr{Left: expression.Left, Operator: "=", Right: expression.Right}}
			}
		}
		if strings.EqualFold(expression.Operator, "AT TIME ZONE") {
			switch target {
			case DialectSnowflake:
				return &FunctionCallExpr{Name: []Identifier{{Text: "CONVERT_TIMEZONE"}}, Args: []Expr{expression.Right, expression.Left}}
			case DialectBigQuery:
				return &FunctionCallExpr{Name: []Identifier{{Text: "TIMESTAMP"}}, Args: []Expr{
					&FunctionCallExpr{Name: []Identifier{{Text: "DATETIME"}}, Args: []Expr{expression.Left, expression.Right}},
				}}
			}
		}
		if expression.Operator == "RLIKE" || expression.Operator == "REGEXP" {
			switch target {
			case DialectDuckDB:
				return &FunctionCallExpr{Name: []Identifier{{Text: "REGEXP_MATCHES"}}, Args: []Expr{expression.Left, expression.Right}}
			case DialectPresto:
				return &FunctionCallExpr{Name: []Identifier{{Text: "REGEXP_LIKE"}}, Args: []Expr{expression.Left, expression.Right}}
			case DialectHive, DialectSpark:
				expression.Operator = "RLIKE"
			case DialectExasol:
				pattern := strings.Trim(renderExpr(expression.Right), "'")
				return &RawExpr{Raw: renderExpr(expression.Left) + " REGEXP_LIKE '.*" + pattern + ".*'"}
			}
		}
		if expression.Operator == "||" && (target == DialectMySQL || target == DialectSpark) {
			return &FunctionCallExpr{
				nodeBase: nodeBase{span: expression.SourceSpan()},
				Name:     []Identifier{{Text: "CONCAT"}},
				Args:     []Expr{expression.Left, expression.Right},
			}
		}
		if target == DialectClickHouse {
			switch strings.ToUpper(strings.TrimSpace(expression.Operator)) {
			case "XOR":
				return &FunctionCallExpr{nodeBase: expression.nodeBase, Name: []Identifier{{Text: "xor"}}, Args: []Expr{expression.Left, expression.Right}}
			case "~*":
				return &FunctionCallExpr{
					Name: []Identifier{{Text: "match"}},
					Args: []Expr{expression.Left, &FunctionCallExpr{
						Name: []Identifier{{Text: "CONCAT"}},
						Args: []Expr{&LiteralExpr{KindValue: LiteralString, Raw: "'(?i)'"}, expression.Right},
					}},
				}
			case "~", "!~":
				match := &FunctionCallExpr{Name: []Identifier{{Text: "match"}}, Args: []Expr{expression.Left, expression.Right}}
				if strings.EqualFold(strings.TrimSpace(expression.Operator), "!~") {
					return &UnaryExpr{Operator: "NOT", Expr: match}
				}
				return match
			case "=", "<>", "!=":
				if has, ok := clickHouseAnyComparison(expression); ok {
					return has
				}
			}
		}
		if expression.Operator == "||" && target == DialectTSQL {
			expression.Operator = "+"
		}
		if expression.Operator == "||" && target == DialectSolr {
			expression.Operator = "OR"
		}
		if expression.Operator == "ILIKE" && (target == DialectMySQL || target == DialectTSQL) {
			lowerLeft := &FunctionCallExpr{Name: []Identifier{{Text: "LOWER"}}, Args: []Expr{expression.Left}}
			lowerRight := &FunctionCallExpr{Name: []Identifier{{Text: "LOWER"}}, Args: []Expr{expression.Right}}
			like := &BinaryExpr{nodeBase: nodeBase{span: expression.SourceSpan()}, Left: lowerLeft, Operator: "LIKE", Right: lowerRight}
			if target == DialectTSQL {
				return ilikeTSQL(lowerLeft, lowerRight)
			}
			return like
		}
		if target == DialectDataFusion {
			switch expression.Operator {
			case "!=":
				expression.Operator = "<>"
			case "~":
				return &FunctionCallExpr{
					nodeBase: nodeBase{span: expression.SourceSpan()},
					Name:     []Identifier{{Text: "REGEXP_LIKE"}},
					Args:     []Expr{expression.Left, expression.Right},
				}
			case "~*":
				expression.Operator = "REGEXP_ILIKE"
			case "NOT LIKE":
				return &UnaryExpr{
					Operator: "NOT",
					Expr:     &BinaryExpr{Left: expression.Left, Operator: "LIKE", Right: expression.Right, Escape: expression.Escape},
				}
			case "<=>":
				return &IsExpr{Value: expression.Left, Operator: "IS NOT DISTINCT FROM", Right: expression.Right}
			}
		}
	case *InExpr:
		booleanItem := false
		for _, item := range expression.Items {
			if literal, ok := item.(*LiteralExpr); ok && literal.KindValue == LiteralBoolean {
				booleanItem = true
				break
			}
		}
		expression.Value = transformExpr(expression.Value, target)
		for i := range expression.Items {
			expression.Items[i] = transformExpr(expression.Items[i], target)
		}
		if expression.Query != nil {
			transformSelect(expression.Query, target)
		}
		if (target == DialectGeneric || target == DialectDuckDB || target == DialectSnowflake || target == DialectTSQL && booleanItem) && expression.Not {
			copy := *expression
			copy.Not = false
			if target == DialectSnowflake && copy.Query != nil {
				return &BinaryExpr{
					Left:     copy.Value,
					Operator: "<>",
					Right:    &QuantifiedExpr{Keyword: "ALL", Query: copy.Query, SpaceBeforeParen: true},
				}
			}
			return &UnaryExpr{Operator: "NOT", Expr: &copy}
		}
	case *BetweenExpr:
		expression.Value = transformExpr(expression.Value, target)
		expression.Low = transformExpr(expression.Low, target)
		expression.High = transformExpr(expression.High, target)
		if expression.Symmetric && target != DialectPostgreSQL && target != DialectDremio {
			first := &BetweenExpr{Value: expression.Value, Low: expression.Low, High: expression.High}
			second := &BetweenExpr{Value: expression.Value, Low: expression.High, High: expression.Low}
			return &ParenthesizedExpr{Expr: &BinaryExpr{Left: first, Operator: "OR", Right: second}}
		}
		if expression.Asymmetric && target != DialectPostgreSQL && target != DialectDremio {
			expression.Asymmetric = false
		}
		if target == DialectGeneric && expression.Not {
			copy := *expression
			copy.Not = false
			return &UnaryExpr{Operator: "NOT", Expr: &copy}
		}
	case *IsExpr:
		rightBoolean := false
		rightBooleanRaw := "TRUE"
		if literal, ok := expression.Right.(*LiteralExpr); ok {
			rightBoolean = literal.KindValue == LiteralBoolean
			rightBooleanRaw = literal.Raw
		}
		expression.Value = transformExpr(expression.Value, target)
		expression.Right = transformExpr(expression.Right, target)
		if target == DialectTSQL && rightBoolean {
			value := "0"
			if strings.EqualFold(rightBooleanRaw, "TRUE") {
				value = "1"
			}
			comparison := &BinaryExpr{
				Left:     expression.Value,
				Operator: "=",
				Right:    &LiteralExpr{KindValue: LiteralNumber, Raw: value},
			}
			if strings.EqualFold(expression.Operator, "IS NOT") {
				return &UnaryExpr{Operator: "NOT", Expr: comparison}
			}
			if strings.EqualFold(expression.Operator, "IS") {
				return comparison
			}
		}
		if target == DialectMySQL {
			switch strings.ToUpper(expression.Operator) {
			case "IS DISTINCT FROM":
				return &UnaryExpr{Operator: "NOT", Expr: &BinaryExpr{Left: expression.Value, Operator: "<=>", Right: expression.Right}}
			case "IS NOT DISTINCT FROM":
				return &BinaryExpr{Left: expression.Value, Operator: "<=>", Right: expression.Right}
			}
		}
		if target == DialectTSQL && (strings.EqualFold(expression.Operator, "IS DISTINCT FROM") || strings.EqualFold(expression.Operator, "IS NOT DISTINCT FROM")) {
			return booleanToTSQL(expression)
		}
		if target == DialectGeneric && strings.EqualFold(expression.Operator, "IS NOT") {
			copy := *expression
			copy.Operator = "IS"
			return &UnaryExpr{Operator: "NOT", Expr: &copy}
		}
	case *FunctionCallExpr:
		if target == DialectSnowflake && expression.RawArgs != "" {
			expression.RawArgs = normalizeSnowflakeArrayConstruct(expression.RawArgs)
			if len(expression.Name) == 1 && strings.EqualFold(expression.Name[0].Text, "SEARCH") {
				expression.RawArgs = normalizeSnowflakeNamedArgs(expression.RawArgs, []string{"ANALYZER", "SEARCH_MODE"})
			}
		}
		if expression.ArrayLiteral && target != DialectBigQuery {
			normalizeStructArraySchema(expression)
		}
		hadInlineOrderBy := target == DialectSnowflake && len(expression.OrderBy) > 0
		for i := range expression.Args {
			expression.Args[i] = transformExpr(expression.Args[i], target)
		}
		expression.Having = transformExpr(expression.Having, target)
		for i := range expression.OrderBy {
			transformOrderItem(&expression.OrderBy[i], target)
		}
		for i := range expression.WithinGroup {
			transformOrderItem(&expression.WithinGroup[i], target)
		}
		if target == DialectSnowflake {
			expression.WithinGroup = rewriteOrderItems(expression.WithinGroup, target)
		}
		expression.Filter = transformExpr(expression.Filter, target)
		if target == DialectSnowflake && len(expression.OrderBy) > 0 {
			expression.WithinGroup = append(expression.WithinGroup, expression.OrderBy...)
			expression.OrderBy = nil
		}
		if target == DialectSnowflake && len(expression.Name) == 1 && strings.EqualFold(expression.Name[0].Text, "PERCENTILE_DISC") {
			for i := range expression.WithinGroup {
				if expression.WithinGroup[i].Descending && !expression.WithinGroup[i].NullsFirst {
					expression.WithinGroup[i].NullsLast = true
				}
			}
		}
		if (target == DialectDuckDB || target == DialectSnowflake) && expression.Filter != nil {
			if isNot, ok := expression.Filter.(*IsExpr); ok && strings.EqualFold(isNot.Operator, "IS NOT") {
				copy := *isNot
				copy.Operator = "IS"
				expression.Filter = &UnaryExpr{Operator: "NOT", Expr: &copy}
			}
		}
		if target == DialectSnowflake && expression.Filter != nil && hadInlineOrderBy {
			condition := expression.Filter
			if len(expression.Args) == 1 {
				expression.Args[0] = &FunctionCallExpr{
					Name: []Identifier{{Text: "IFF"}},
					Args: []Expr{condition, expression.Args[0], &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}},
				}
			}
			expression.Filter = nil
		} else if target == DialectSnowflake && expression.Filter != nil && len(expression.WithinGroup) > 0 && len(expression.Args) == 0 {
			condition := expression.Filter
			for _, item := range expression.WithinGroup {
				expression.Args = append(expression.Args, &FunctionCallExpr{
					Name: []Identifier{{Text: "IFF"}},
					Args: []Expr{condition, item.Expr, &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}},
				})
			}
			expression.WithinGroup = nil
			expression.Filter = nil
		} else if target == DialectSnowflake && expression.Filter != nil && len(expression.OrderBy) == 0 && len(expression.WithinGroup) > 0 && len(expression.Args) == 0 {
			condition := expression.Filter
			for i := range expression.WithinGroup {
				expression.WithinGroup[i].Expr = &FunctionCallExpr{
					Name: []Identifier{{Text: "IFF"}},
					Args: []Expr{condition, expression.WithinGroup[i].Expr, &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}},
				}
			}
			expression.Filter = nil
		} else if target == DialectSnowflake && expression.Filter != nil && len(expression.WithinGroup) > 0 && len(expression.Args) > 0 && len(expression.Name) == 1 && (strings.EqualFold(expression.Name[0].Text, "PERCENTILE_CONT") || strings.EqualFold(expression.Name[0].Text, "PERCENTILE_DISC")) {
			condition := expression.Filter
			for i := range expression.WithinGroup {
				expression.WithinGroup[i].Expr = &FunctionCallExpr{
					Name: []Identifier{{Text: "IFF"}},
					Args: []Expr{condition, expression.WithinGroup[i].Expr, &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}},
				}
			}
			expression.Filter = nil
		} else if target == DialectSnowflake && expression.Filter != nil && len(expression.WithinGroup) > 0 && len(expression.Args) > 0 {
			condition := expression.Filter
			for i := range expression.Args {
				expression.Args[i] = &FunctionCallExpr{
					Name: []Identifier{{Text: "IFF"}},
					Args: []Expr{condition, expression.Args[i], &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}},
				}
			}
			expression.Filter = nil
		} else if target == DialectSnowflake && expression.Filter != nil && len(expression.WithinGroup) > 0 {
			condition := expression.Filter
			for i := range expression.WithinGroup {
				expression.WithinGroup[i].Expr = &FunctionCallExpr{
					Name: []Identifier{{Text: "IFF"}},
					Args: []Expr{condition, expression.WithinGroup[i].Expr, &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}},
				}
			}
			expression.Filter = nil
		} else if target == DialectSnowflake && expression.Filter != nil && len(expression.Name) == 1 && strings.EqualFold(expression.Name[0].Text, "COUNT") && !expression.Distinct {
			condition := expression.Filter
			expression.Name = []Identifier{{Text: "COUNT_IF"}}
			expression.Args = []Expr{condition}
			expression.Star = false
			expression.Filter = nil
		} else if target == DialectSnowflake && expression.Filter != nil && len(expression.Args) == 1 {
			condition := expression.Filter
			expression.Filter = nil
			expression.Args[0] = &FunctionCallExpr{
				Name: []Identifier{{Text: "IFF"}},
				Args: []Expr{condition, expression.Args[0], &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}},
			}
		}
		if expression.Over != nil {
			transformWindow(expression.Over, target)
		}
		if target == DialectDuckDB && len(expression.Name) == 1 && len(expression.Args) == 0 {
			switch strings.ToUpper(expression.Name[0].Text) {
			case "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP":
				return identifierExpr(strings.ToUpper(expression.Name[0].Text))
			}
		}
		if target == DialectSnowflake && len(expression.Name) == 1 && strings.EqualFold(expression.Name[0].Text, "TRANSFORM") && len(expression.Args) == 2 && strings.Contains(expression.ArgumentTail, "->") {
			if lambda, ok := expression.Args[1].(*IdentifierExpr); ok && len(lambda.Parts) == 1 {
				tail := strings.TrimSpace(expression.ArgumentTail)
				arrow := strings.Index(tail, "->")
				if arrow > 0 {
					typeName := strings.ToUpper(strings.TrimSpace(tail[:arrow]))
					body := castSnowflakeLambdaVariable(strings.TrimSpace(tail[arrow+2:]), lambda.Parts[0].Text, typeName)
					expression.RawArgs = "(" + renderExpr(expression.Args[0]) + ", " + lambda.Parts[0].Text + " -> " + body + ")"
					expression.Args = nil
					expression.ArgumentTail = ""
				}
			}
		}
		canonicalizeFunctionName(expression, target)
		if target == DialectSnowflake && len(expression.Name) == 1 && strings.EqualFold(expression.Name[0].Text, "GENERATOR") {
			if expression.RawArgs == "" {
				expression.RawArgs = normalizeSnowflakeGeneratorArgs(expression.Args)
			} else {
				expression.RawArgs = normalizeSnowflakeGeneratorRawArgs(expression.RawArgs)
			}
			if expression.RawArgs != "" {
				expression.Args = nil
			}
		}
		if target == DialectBigQuery {
			normalizeBigQueryFunctionFormat(expression)
			if len(expression.Name) == 1 && strings.EqualFold(expression.Name[0].Text, "LOWER") && len(expression.Args) == 1 {
				if nested, ok := expression.Args[0].(*FunctionCallExpr); ok && len(nested.Name) == 1 && strings.EqualFold(nested.Name[0].Text, "TO_HEX") && len(nested.Args) == 1 {
					return nested
				}
			}
		}
		if rewritten := rewriteFunction(expression, target); rewritten != nil {
			return rewritten
		}
	case *CallExpr:
		genericCallee, isGenericCallee := expression.Callee.(*GenericExpr)
		expression.Callee = transformExpr(expression.Callee, target)
		for i := range expression.Args {
			expression.Args[i] = transformExpr(expression.Args[i], target)
		}
		if target == DialectDuckDB && isGenericCallee && isIdentifierNamed(genericCallee.Target, "STRUCT") {
			for index := range expression.Args {
				expression.Args[index] = normalizeDuckDBTypedStructArgument(expression.Args[index])
			}
			return &CastExpr{
				nodeBase: nodeBase{span: expression.SourceSpan()},
				Keyword:  "CAST",
				Value:    &FunctionCallExpr{Name: []Identifier{{Text: "ROW"}}, Args: expression.Args},
				Type:     &RawExpr{Raw: normalizeCreateTypeToken(renderExpr(genericCallee), DialectDuckDB)},
			}
		}
		if target == DialectDuckDB && isDuckDBAtMarker(expression.Callee) && len(expression.Args) == 1 {
			value := expression.Args[0]
			if _, alreadyParenthesized := value.(*ParenthesizedExpr); !alreadyParenthesized {
				value = &ParenthesizedExpr{Expr: value}
			}
			return &FunctionCallExpr{Name: []Identifier{{Text: "ABS"}}, Args: []Expr{value}}
		}
		if target == DialectBigQuery {
			if generic, ok := expression.Callee.(*GenericExpr); ok && isIdentifierNamed(generic.Target, "STRUCT") {
				return &CastExpr{nodeBase: nodeBase{span: expression.SourceSpan()}, Keyword: "CAST", Value: &FunctionCallExpr{Name: []Identifier{{Text: "STRUCT"}}, Args: expression.Args}, Type: generic}
			}
		}
	case *GenericExpr:
		expression.Target = transformExpr(expression.Target, target)
		for i := range expression.Arguments {
			expression.Arguments[i] = transformExpr(expression.Arguments[i], target)
		}
		if target == DialectDuckDB {
			if normalized := normalizeCreateTypeToken(renderExpr(expression), target); normalized != renderExpr(expression) {
				return &RawExpr{Raw: normalized}
			}
		}
	case *ExtractExpr:
		expression.Field = transformExpr(expression.Field, target)
		expression.Source = transformExpr(expression.Source, target)
		if target == DialectDuckDB {
			if rewritten := rewriteDuckDBDatePart(&FunctionCallExpr{Args: []Expr{expression.Field, expression.Source}}); rewritten != nil {
				return rewritten
			}
		}
		if target == DialectSpark {
			if field, ok := expression.Field.(*IdentifierExpr); ok && len(field.Parts) == 1 {
				field.Parts[0].Text = strings.ToLower(field.Parts[0].Text)
			}
		}
		if target == DialectTSQL {
			return &FunctionCallExpr{Name: []Identifier{{Text: "DATEPART"}}, Args: []Expr{expression.Field, expression.Source}}
		}
		if target == DialectSnowflake {
			return &FunctionCallExpr{Name: []Identifier{{Text: "DATE_PART"}}, Args: []Expr{expression.Field, expression.Source}}
		}
		if target == DialectGeneric {
			if field, ok := expression.Field.(*IdentifierExpr); ok && len(field.Parts) == 1 && !field.Parts[0].Quoted {
				field.Parts[0].Text = strings.ToUpper(field.Parts[0].Text)
			}
		}
	case *TupleExpr:
		for i := range expression.Items {
			expression.Items[i] = transformExpr(expression.Items[i], target)
		}
	case *AliasExpr:
		expression.Expr = transformExpr(expression.Expr, target)
		normalizeIdentifierTarget(&expression.Alias, target)
	case *IntervalExpr:
		expression.Value = transformExpr(expression.Value, target)
		for i := range expression.Qualifiers {
			expression.Qualifiers[i] = transformExpr(expression.Qualifiers[i], target)
		}
		if (target == DialectPostgreSQL || target == DialectMaterialize || target == DialectSnowflake) && len(expression.Qualifiers) == 1 {
			value := renderExpr(expression.Value)
			unitExpr := expression.Qualifiers[0]
			if target == DialectSnowflake {
				if identifier, ok := unitExpr.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted {
					identifier.Parts[0].Text = snowflakeDateUnit(identifier.Parts[0].Text)
				}
			}
			unit := renderExpr(unitExpr)
			expression.Value = &LiteralExpr{KindValue: LiteralString, Raw: "'" + strings.Trim(value, "'") + " " + unit + "'"}
			expression.Qualifiers = nil
		}
		if target == DialectGeneric {
			for i, qualifier := range expression.Qualifiers {
				if identifier, ok := qualifier.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted {
					identifier.Parts[0].Text = strings.ToUpper(identifier.Parts[0].Text)
					expression.Qualifiers[i] = identifier
				}
			}
		}
		if target == DialectClickHouse {
			for index, qualifier := range expression.Qualifiers {
				if identifier, ok := qualifier.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted {
					switch strings.ToUpper(identifier.Parts[0].Text) {
					case "US":
						identifier.Parts[0].Text = "MICROSECOND"
					case "MS":
						identifier.Parts[0].Text = "MILLISECOND"
					}
					expression.Qualifiers[index] = identifier
				}
			}
		}
		if target == DialectSpark || target == DialectBigQuery || target == DialectPresto || target == DialectTrino {
			if literal, ok := expression.Value.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
				literal.KindValue = LiteralString
				literal.Raw = "'" + literal.Raw + "'"
			}
		}
		if target == DialectDuckDB || target == DialectHive {
			if literal, ok := expression.Value.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
				literal.KindValue = LiteralString
				literal.Raw = "'" + literal.Raw + "'"
			}
		}
		if target == DialectDremio {
			for i, qualifier := range expression.Qualifiers {
				if identifier, ok := qualifier.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted {
					identifier.Parts[0].Text = strings.TrimSuffix(strings.ToUpper(identifier.Parts[0].Text), "S")
					expression.Qualifiers[i] = identifier
				}
			}
		}
	case *CastExpr:
		expression.Value = transformExpr(expression.Value, target)
		expression.Type = transformExpr(expression.Type, target)
		var snowflakeTypeIndex *IndexExpr
		if target == DialectSnowflake {
			if index, ok := expression.Type.(*IndexExpr); ok && !index.Slice && (len(index.Indices) == 1 || index.Low != nil) {
				snowflakeTypeIndex = index
				expression.Type = index.Target
			}
		}
		if target == DialectSnowflake {
			if raw, ok := expression.Value.(*RawExpr); ok {
				if entries, ok := parseDuckDBMapLiteral(raw.Raw); ok {
					args := make([]Expr, 0, len(entries)*2)
					for _, entry := range entries {
						args = append(args, &LiteralExpr{KindValue: LiteralString, Raw: "'" + strings.ReplaceAll(entry.Key, "'", "''") + "'"}, &RawExpr{Raw: entry.Value})
					}
					expression.Value = &FunctionCallExpr{Name: []Identifier{{Text: "OBJECT_CONSTRUCT"}}, Args: args}
				}
			}
		}
		originalType, _ := castTypeIdentifier(expression.Type)
		originalTypeName := ""
		if originalType != nil {
			originalTypeName = strings.ToUpper(originalType.Text)
		}
		if target == DialectGeneric {
			if identifier, ok := expression.Type.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted && strings.EqualFold(identifier.Parts[0].Text, "INTERVAL") {
				identifier.Parts[0].Text = "INTERVAL"
			}
		}
		rewriteCastType(expression, target)
		if target == DialectBigQuery {
			if generic, ok := expression.Type.(*GenericExpr); ok && (isIdentifierNamed(generic.Target, "STRUCT") || isIdentifierNamed(generic.Target, "ARRAY")) {
				expression.Type = &RawExpr{Raw: normalizeCreateTypeToken(renderExpr(generic), target)}
			}
		}
		if target == DialectSnowflake {
			if generic, ok := expression.Type.(*GenericExpr); ok && isIdentifierNamed(generic.Target, "STRUCT") {
				expression.Type = &RawExpr{Raw: normalizeSnowflakeObjectType(renderExpr(generic))}
			}
		}
		if target == DialectSnowflake && strings.EqualFold(expression.Keyword, "SAFE_CAST") {
			expression.Keyword = "CAST"
			if originalTypeName == "TIMESTAMP" {
				expression.Type = identifierExpr("TIMESTAMPTZ")
			}
		}
		if snowflakeTypeIndex != nil {
			result := &IndexExpr{nodeBase: nodeBase{span: expression.SourceSpan()}, Target: expression, Indices: snowflakeTypeIndex.Indices, Low: snowflakeTypeIndex.Low}
			return result
		}
		if target == DialectSnowflake && (originalTypeName == "GEOGRAPHY" || originalTypeName == "GEOMETRY") {
			return &FunctionCallExpr{Name: []Identifier{{Text: "TO_" + originalTypeName}}, Args: []Expr{expression.Value}}
		}
		if (target == DialectMySQL || target == DialectStarRocks) && originalTypeName == "TIMESTAMPTZ" {
			return &FunctionCallExpr{nodeBase: nodeBase{span: expression.SourceSpan()}, Name: []Identifier{{Text: "TIMESTAMP"}}, Args: []Expr{expression.Value}}
		}
		if target == DialectBigQuery && len(expression.TypeSuffix) > 0 && (originalTypeName == "DATE" || originalTypeName == "TIMESTAMP" || originalTypeName == "TIME") {
			for index, suffix := range expression.TypeSuffix {
				text := strings.TrimSpace(suffix.Text)
				if !strings.HasPrefix(strings.ToUpper(text), "FORMAT ") {
					continue
				}
				format := strings.TrimSpace(text[len("FORMAT "):])
				zone := ""
				if zoneIndex := strings.Index(strings.ToUpper(format), " AT TIME ZONE "); zoneIndex >= 0 {
					zone = strings.TrimSpace(format[zoneIndex+len(" AT TIME ZONE "):])
					format = strings.TrimSpace(format[:zoneIndex])
				} else if index+1 < len(expression.TypeSuffix) && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(expression.TypeSuffix[index+1].Text)), "AT TIME ZONE ") {
					zone = strings.TrimSpace(strings.TrimSpace(expression.TypeSuffix[index+1].Text)[len("AT TIME ZONE "):])
				}
				format = normalizeBigQueryDateFormat(format)
				if originalTypeName == "TIME" && !strings.Contains(strings.ToUpper(strings.TrimSpace(format)), "%I") {
					// BigQuery's bare HH token in TIME FORMAT is the 12-hour
					// token used by SQLGlot's PARSE_TIMESTAMP lowering.
					format = strings.ReplaceAll(format, "%H", "%I")
				}
				name := "PARSE_TIMESTAMP"
				if originalTypeName == "DATE" {
					name = "PARSE_DATE"
				}
				args := []Expr{&LiteralExpr{KindValue: LiteralString, Raw: format}, expression.Value}
				if zone != "" {
					args = append(args, &RawExpr{Raw: zone})
				}
				return &FunctionCallExpr{Name: []Identifier{{Text: name}}, Args: args}
			}
		}
	case *WindowedExpr:
		expression.Expr = transformExpr(expression.Expr, target)
		transformWindow(&expression.Over, target)
	case *ExistsExpr:
		if expression.Query != nil {
			transformSelect(expression.Query, target)
		}
	case *QuantifiedExpr:
		if expression.Query != nil {
			transformSelect(expression.Query, target)
		}
		if target == DialectGeneric {
			expression.SpaceBeforeParen = false
		}
	case *GroupingExpr:
		for i := range expression.Args {
			expression.Args[i] = transformExpr(expression.Args[i], target)
		}
	case *SetExpr:
		expression.Left = transformExpr(expression.Left, target)
		expression.Right = transformExpr(expression.Right, target)
	case *SubqueryExpr:
		if expression.Query != nil {
			transformSelect(expression.Query, target)
		}
	case *TypedLiteralExpr:
		for i := range expression.Parameters {
			expression.Parameters[i] = transformExpr(expression.Parameters[i], target)
		}
		if len(expression.TypeName) == 1 && strings.EqualFold(expression.TypeName[0].Text, "TIMESTAMP") && expression.Value != nil {
			typeName := ""
			switch target {
			case DialectMySQL, DialectBigQuery, DialectStarRocks, DialectDoris:
				typeName = "DATETIME"
			case DialectClickHouse:
				typeName = "Nullable(DateTime)"
			case DialectTSQL:
				typeName = "DATETIME2"
			case DialectSnowflake:
				typeName = "TIMESTAMPNTZ"
			case DialectSpark, DialectDatabricks:
				typeName = "TIMESTAMP_NTZ"
			}
			if typeName != "" {
				return &CastExpr{Keyword: "CAST", Value: expression.Value, Type: &RawExpr{Raw: typeName}}
			}
		}
		if target == DialectSnowflake && len(expression.TypeName) == 1 && strings.EqualFold(expression.TypeName[0].Text, "JSON") {
			if expression.Value != nil {
				return &FunctionCallExpr{Name: []Identifier{{Text: "PARSE_JSON"}}, Args: []Expr{expression.Value}}
			}
			if len(expression.Parameters) > 0 {
				return &FunctionCallExpr{Name: []Identifier{{Text: "PARSE_JSON"}}, Args: expression.Parameters}
			}
		}
		if target == DialectSpark && len(expression.TypeName) == 1 && strings.EqualFold(expression.TypeName[0].Text, "N") && expression.Value != nil {
			if strings.Contains(expression.Value.Raw, "''") {
				content := strings.ReplaceAll(expression.Value.Raw[1:len(expression.Value.Raw)-1], "''", "'")
				expression.Value.Raw = "'" + strings.ReplaceAll(content, "'", "\\'") + "'"
			}
			return expression.Value
		}
	case *CaseExpr:
		expression.Operand = transformExpr(expression.Operand, target)
		for i := range expression.Whens {
			conditionBoolean := false
			conditionBooleanRaw := "TRUE"
			if literal, ok := expression.Whens[i].Condition.(*LiteralExpr); ok {
				conditionBoolean = literal.KindValue == LiteralBoolean
				conditionBooleanRaw = literal.Raw
			}
			expression.Whens[i].Condition = transformExpr(expression.Whens[i].Condition, target)
			if target == DialectTSQL && conditionBoolean {
				expression.Whens[i].Condition = booleanOperandTSQL(&LiteralExpr{KindValue: LiteralBoolean, Raw: conditionBooleanRaw})
			}
			expression.Whens[i].Result = transformExpr(expression.Whens[i].Result, target)
		}
		expression.Else = transformExpr(expression.Else, target)
	case *IndexExpr:
		if target == DialectDuckDB {
			if generic, ok := expression.Target.(*GenericExpr); ok && isIdentifierNamed(generic.Target, "ARRAY") {
				array := &FunctionCallExpr{Name: []Identifier{{Text: "ARRAY"}}, Args: expression.Indices, ArrayLiteral: true}
				for index := range array.Args {
					array.Args[index] = normalizeDuckDBTypedArrayStructExpr(array.Args[index])
					array.Args[index] = transformExpr(array.Args[index], target)
				}
				return &CastExpr{
					nodeBase: nodeBase{span: expression.SourceSpan()},
					Keyword:  "CAST",
					Value:    array,
					Type:     &RawExpr{Raw: normalizeCreateTypeToken(renderExpr(generic), DialectDuckDB)},
				}
			}
		}
		if target != DialectBigQuery && isArrayConstructorIndex(expression) {
			array := &FunctionCallExpr{Args: expression.Indices, ArrayLiteral: true}
			normalizeStructArraySchema(array)
			expression.Indices = array.Args
		}
		expression.Target = transformExpr(expression.Target, target)
		expression.Low = transformExpr(expression.Low, target)
		expression.High = transformExpr(expression.High, target)
		expression.Step = transformExpr(expression.Step, target)
		for i := range expression.Indices {
			expression.Indices[i] = transformExpr(expression.Indices[i], target)
		}
		if target == DialectSnowflake || target == DialectPresto || target == DialectTrino {
			for i := range expression.Indices {
				expression.Indices[i] = rewriteNestedStructExpr(expression.Indices[i], target)
			}
		}
		if target == DialectBigQuery && !expression.Slice {
			if isArrayConstructorIndex(expression) {
				break
			}
			var index Expr
			if len(expression.Indices) == 1 {
				index = expression.Indices[0]
			} else if expression.Low != nil && expression.High == nil && expression.Step == nil {
				index = expression.Low
			}
			if literal, ok := index.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
				if value, err := strconv.Atoi(literal.Raw); err == nil && value > 0 {
					literal.Raw = strconv.Itoa(value - 1)
				}
			}
		}
		if target == DialectDuckDB && !expression.Slice && len(expression.Indices) > 0 {
			if identifier, ok := expression.Target.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && strings.EqualFold(identifier.Parts[0].Text, "ARRAY") {
				return &FunctionCallExpr{nodeBase: nodeBase{span: expression.SourceSpan()}, Name: []Identifier{{Text: "ARRAY"}}, Args: expression.Indices, ArrayLiteral: true}
			}
		}
	case *FieldExpr:
		expression.Target = transformExpr(expression.Target, target)
	case *ParenthesizedExpr:
		expression.Expr = transformExpr(expression.Expr, target)
	case *RawExpr:
		if target == DialectDuckDB {
			expression.Raw = normalizeDuckDBListValueRaw(expression.Raw)
		}
		if target == DialectSnowflake {
			expression.Raw = normalizeSnowflakeODBC(expression.Raw)
		}
		if (target == DialectSpark || target == DialectDuckDB || target == DialectBigQuery) && len(expression.Raw) >= 3 && (expression.Raw[0] == 'e' || expression.Raw[0] == 'E') && expression.Raw[1] == '\'' && expression.Raw[len(expression.Raw)-1] == '\'' {
			content := expression.Raw[2 : len(expression.Raw)-1]
			content = strings.ReplaceAll(content, "\r\n", `\n`)
			content = strings.ReplaceAll(content, "\n", `\n`)
			content = strings.ReplaceAll(content, "\\'", "''")
			if target == DialectBigQuery {
				return &CastExpr{Keyword: "CAST", Value: &RawExpr{Raw: "b'" + content + "'"}, Type: identifierExpr("STRING")}
			}
			expression.Raw = expression.Raw[:1] + "'" + content + "'"
		}
		if target == DialectBigQuery {
			if entries, ok := parseDuckDBMapLiteral(expression.Raw); ok {
				args := make([]Expr, 0, len(entries))
				for _, entry := range entries {
					args = append(args, &AliasExpr{Expr: &RawExpr{Raw: entry.Value}, Alias: Identifier{Text: entry.Key}})
				}
				return &FunctionCallExpr{Name: []Identifier{{Text: "STRUCT"}}, Args: args}
			}
		}
		if target == DialectPresto {
			if entries, ok := parseDuckDBMapLiteral(expression.Raw); ok {
				values := make([]Expr, 0, len(entries))
				fields := make([]string, 0, len(entries))
				for _, entry := range entries {
					values = append(values, &RawExpr{Raw: entry.Value})
					fields = append(fields, entry.Key+" "+prestoMapType(entry.Value))
				}
				return &CastExpr{
					Keyword: "CAST",
					Value:   &FunctionCallExpr{Name: []Identifier{{Text: "ROW"}}, Args: values},
					Type:    &RawExpr{Raw: "ROW(" + strings.Join(fields, ", ") + ")"},
				}
			}
		}
	}
	return expression
}

func normalizeDuckDBTypedStructArgument(expression Expr) Expr {
	raw, ok := expression.(*RawExpr)
	if !ok {
		return expression
	}
	entries, ok := parseDuckDBMapLiteral(raw.Raw)
	if !ok || len(entries) == 0 {
		return expression
	}
	args := make([]Expr, 0, len(entries))
	for _, entry := range entries {
		args = append(args, &RawExpr{Raw: entry.Value})
	}
	return &FunctionCallExpr{Name: []Identifier{{Text: "ROW"}}, Args: args}
}

func normalizeDuckDBTypedArrayStructExpr(expression Expr) Expr {
	switch value := expression.(type) {
	case *FunctionCallExpr:
		if len(value.Name) == 1 && strings.EqualFold(value.Name[0].Text, "STRUCT") {
			args := make([]Expr, len(value.Args))
			for index, argument := range value.Args {
				args[index] = normalizeDuckDBTypedArrayStructExpr(argument)
			}
			return &FunctionCallExpr{Name: []Identifier{{Text: "ROW"}}, Args: args}
		}
		for index := range value.Args {
			value.Args[index] = normalizeDuckDBTypedArrayStructExpr(value.Args[index])
		}
	case *AliasExpr:
		value.Expr = normalizeDuckDBTypedArrayStructExpr(value.Expr)
	case *IndexExpr:
		for index := range value.Indices {
			value.Indices[index] = normalizeDuckDBTypedArrayStructExpr(value.Indices[index])
		}
	}
	return expression
}

func rewriteNestedStructExpr(expression Expr, target Dialect) Expr {
	switch value := expression.(type) {
	case *AliasExpr:
		value.Expr = rewriteNestedStructExpr(value.Expr, target)
	case *FunctionCallExpr:
		for index := range value.Args {
			value.Args[index] = rewriteNestedStructExpr(value.Args[index], target)
		}
		if len(value.Name) == 1 && strings.EqualFold(value.Name[0].Text, "STRUCT") {
			if rewritten := rewriteStructFunction(value, target); rewritten != nil {
				return rewritten
			}
		}
	case *IndexExpr:
		for index := range value.Indices {
			value.Indices[index] = rewriteNestedStructExpr(value.Indices[index], target)
		}
	}
	return expression
}

func normalizeStructArraySchema(array *FunctionCallExpr) {
	if array == nil || len(array.Args) == 0 {
		return
	}
	first, ok := array.Args[0].(*FunctionCallExpr)
	if !ok || len(first.Name) != 1 || !strings.EqualFold(first.Name[0].Text, "STRUCT") {
		return
	}
	keys := make([]string, len(first.Args))
	for index, argument := range first.Args {
		alias, ok := argument.(*AliasExpr)
		if !ok || alias.Alias.Text == "" {
			return
		}
		keys[index] = alias.Alias.Text
	}
	for _, argument := range array.Args {
		structure, ok := argument.(*FunctionCallExpr)
		if !ok || len(structure.Name) != 1 || !strings.EqualFold(structure.Name[0].Text, "STRUCT") {
			continue
		}
		for index := range structure.Args {
			if index >= len(keys) || keys[index] == "" {
				continue
			}
			if alias, ok := structure.Args[index].(*AliasExpr); ok {
				if firstAlias, ok := first.Args[index].(*AliasExpr); ok {
					if nested, ok := firstAlias.Expr.(*FunctionCallExpr); ok {
						if nestedValue, ok := alias.Expr.(*FunctionCallExpr); ok {
							normalizeStructArraySchemaPair(nested, nestedValue)
						}
					}
				}
				continue
			}
			structure.Args[index] = &AliasExpr{Expr: structure.Args[index], Alias: Identifier{Text: keys[index]}}
		}
	}
}

func normalizeStructArraySchemaPair(first, value *FunctionCallExpr) {
	if first == nil || value == nil || len(first.Name) != 1 || len(value.Name) != 1 || !strings.EqualFold(first.Name[0].Text, "STRUCT") || !strings.EqualFold(value.Name[0].Text, "STRUCT") {
		return
	}
	keys := make([]string, len(first.Args))
	for index, argument := range first.Args {
		alias, ok := argument.(*AliasExpr)
		if !ok || alias.Alias.Text == "" {
			return
		}
		keys[index] = alias.Alias.Text
	}
	for index := range value.Args {
		if index >= len(keys) || keys[index] == "" {
			continue
		}
		if _, ok := value.Args[index].(*AliasExpr); !ok {
			value.Args[index] = &AliasExpr{Expr: value.Args[index], Alias: Identifier{Text: keys[index]}}
		}
	}
}

func isDuckDBAtMarker(expression Expr) bool {
	literal, ok := expression.(*LiteralExpr)
	return ok && literal.KindValue == LiteralParameter && literal.Raw == "@"
}

func duckDBAtValue(expression Expr) (Expr, bool) {
	switch expression := expression.(type) {
	case *LiteralExpr:
		if expression.KindValue == LiteralParameter && strings.HasPrefix(expression.Raw, "@") && len(expression.Raw) > 1 {
			return identifierExpr(strings.TrimPrefix(expression.Raw, "@")), true
		}
		if expression.KindValue == LiteralParameter && expression.Raw == "@" {
			return nil, false
		}
	case *CallExpr:
		if isDuckDBAtMarker(expression.Callee) && len(expression.Args) == 1 {
			value := expression.Args[0]
			if _, alreadyParenthesized := value.(*ParenthesizedExpr); !alreadyParenthesized {
				value = &ParenthesizedExpr{Expr: value}
			}
			return value, true
		}
	}
	return nil, false
}

func dollarQuotedValue(raw string) (string, bool) {
	if len(raw) < 4 || raw[0] != '$' {
		return "", false
	}
	endTag := strings.IndexByte(raw[1:], '$')
	if endTag < 0 {
		return "", false
	}
	endTag++
	tag := raw[:endTag+1]
	if !strings.HasSuffix(raw, tag) {
		return "", false
	}
	return raw[endTag+1 : len(raw)-len(tag)], true
}

func normalizeBigQueryIndex(index *IndexExpr, target Dialect) Expr {
	if index == nil || index.Slice || target == DialectBigQuery || target == DialectSnowflake {
		return nil
	}
	if isArrayConstructorIndex(index) {
		return nil
	}
	var value Expr
	if len(index.Indices) == 1 {
		value = index.Indices[0]
	} else if index.Low != nil && index.High == nil && index.Step == nil {
		value = index.Low
	}
	if value == nil {
		return nil
	}
	if literal, ok := value.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
		if number, err := strconv.Atoi(literal.Raw); err == nil {
			literal.Raw = strconv.Itoa(number + 1)
			return index
		}
	}
	function, ok := value.(*FunctionCallExpr)
	if !ok || len(function.Name) != 1 || len(function.Args) != 1 {
		return nil
	}
	name := strings.ToUpper(function.Name[0].Text)
	if name != "OFFSET" && name != "SAFE_OFFSET" && name != "ORDINAL" && name != "SAFE_ORDINAL" {
		return nil
	}
	argument := function.Args[0]
	if name == "OFFSET" || name == "SAFE_OFFSET" {
		if literal, ok := argument.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
			if number, err := strconv.Atoi(literal.Raw); err == nil {
				literal.Raw = strconv.Itoa(number + 1)
			}
		}
	}
	if (target == DialectPresto || target == DialectTrino) && (name == "SAFE_OFFSET" || name == "SAFE_ORDINAL") {
		return &FunctionCallExpr{Name: []Identifier{{Text: "ELEMENT_AT"}}, Args: []Expr{index.Target, argument}}
	}
	if len(index.Indices) == 1 {
		index.Indices[0] = argument
	} else {
		index.Low = argument
	}
	return index
}

func isArrayConstructorIndex(index *IndexExpr) bool {
	if index == nil {
		return false
	}
	if identifier, ok := index.Target.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted && strings.EqualFold(identifier.Parts[0].Text, "ARRAY") {
		return true
	}
	generic, ok := index.Target.(*GenericExpr)
	return ok && isIdentifierNamed(generic.Target, "ARRAY")
}

func normalizeBigQueryString(raw string) (string, bool) {
	return normalizeBigQueryStringForDialect(raw, DialectDuckDB)
}

func normalizeBigQueryStringForDialect(raw string, target Dialect) (string, bool) {
	if len(raw) < 2 {
		return "", false
	}
	prefix := byte(0)
	quoteStart := 0
	if raw[0] == 'b' || raw[0] == 'B' || raw[0] == 'r' || raw[0] == 'R' {
		prefix = raw[0]
		quoteStart = 1
	}
	if quoteStart >= len(raw) || (raw[quoteStart] != '\'' && raw[quoteStart] != '"') {
		return "", false
	}
	quote := raw[quoteStart]
	delimiterLength := 1
	if quoteStart+2 < len(raw) && raw[quoteStart+1] == quote && raw[quoteStart+2] == quote {
		delimiterLength = 3
	}
	if len(raw) < quoteStart+2*delimiterLength || raw[len(raw)-delimiterLength] != quote {
		return "", false
	}
	content := raw[quoteStart+delimiterLength : len(raw)-delimiterLength]
	if prefix == 'b' || prefix == 'B' {
		content = strings.ReplaceAll(content, "'", "\\'")
		return "b'" + content + "'", true
	}
	backslashTarget := target == DialectBigQuery || target == DialectHive || target == DialectSpark
	if backslashTarget {
		if quote == '"' && prefix != 'r' && prefix != 'R' {
			content = strings.ReplaceAll(content, `\"`, `"`)
		}
		if prefix == 'r' || prefix == 'R' {
			var escaped strings.Builder
			for index := 0; index < len(content); index++ {
				switch content[index] {
				case '\\':
					escaped.WriteString(`\\`)
				case '\'':
					escaped.WriteString(`\'`)
				default:
					escaped.WriteByte(content[index])
				}
			}
			content = escaped.String()
		} else {
			content = escapeBigQueryQuote(content)
		}
		content = escapeBigQueryControlCharacters(content)
		return "'" + content + "'", true
	}
	switch prefix {
	case 'r', 'R':
		return "'" + escapeSQLSingleQuotes(content) + "'", true
	default:
		if quote == '"' {
			content = strings.ReplaceAll(content, `\"`, `"`)
		}
		content = decodeBigQueryEscapes(content)
		return "'" + escapeSQLSingleQuotes(content) + "'", true
	}
}

func escapeSQLSingleQuotes(content string) string {
	var escaped strings.Builder
	for index := 0; index < len(content); index++ {
		if content[index] != '\'' {
			escaped.WriteByte(content[index])
			continue
		}
		escaped.WriteByte('\'')
		if index+1 < len(content) && content[index+1] == '\'' {
			escaped.WriteByte('\'')
			index++
		} else {
			escaped.WriteByte('\'')
		}
	}
	return escaped.String()
}

func escapeBigQueryQuote(content string) string {
	var escaped strings.Builder
	for index := 0; index < len(content); index++ {
		if content[index] == '\'' {
			if index > 0 && content[index-1] == '\\' {
				escaped.WriteByte('\'')
			} else {
				escaped.WriteString(`\'`)
			}
			continue
		}
		escaped.WriteByte(content[index])
	}
	return escaped.String()
}

func escapeBigQueryControlCharacters(content string) string {
	content = strings.ReplaceAll(content, "\n", `\n`)
	content = strings.ReplaceAll(content, "\r", `\r`)
	content = strings.ReplaceAll(content, "\t", `\t`)
	return content
}

func decodeBigQueryEscapes(content string) string {
	var decoded strings.Builder
	for index := 0; index < len(content); index++ {
		if content[index] != '\\' || index+1 >= len(content) {
			decoded.WriteByte(content[index])
			continue
		}
		index++
		switch content[index] {
		case '\\':
			decoded.WriteByte('\\')
		case '\'':
			decoded.WriteByte('\'')
		case '"':
			decoded.WriteByte('"')
		case 'n':
			decoded.WriteByte('\n')
		case 'r':
			decoded.WriteByte('\r')
		case 't':
			decoded.WriteByte('\t')
		default:
			decoded.WriteByte('\\')
			decoded.WriteByte(content[index])
		}
	}
	return decoded.String()
}

func normalizeBigQueryDateFormat(raw string) string {
	value := strings.TrimSpace(raw)
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		content := value[1 : len(value)-1]
		content = strings.ReplaceAll(content, "%Y-%m-%d", "%F")
		content = strings.ReplaceAll(content, "%H:%M:%S", "%T")
		content = strings.ReplaceAll(content, "%x", "%D")
		content = strings.ReplaceAll(content, "YYYY-MM-DD", "%F")
		content = strings.ReplaceAll(content, "HH24:MI:SS", "%T")
		for _, replacement := range []struct{ from, to string }{
			{"YYYY", "%Y"}, {"HH24", "%H"}, {"HH12", "%I"}, {"HH", "%H"},
			{"MI", "%M"}, {"SS", "%S"}, {"MM", "%m"}, {"DD", "%d"},
			{"TZH:TZM", "%z"}, {"TZH", "%z"},
		} {
			content = strings.ReplaceAll(content, replacement.from, replacement.to)
		}
		return "'" + content + "'"
	}
	return value
}

func normalizeBigQuerySnowflakeFormat(expression Expr) Expr {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString {
		return expression
	}
	value := strings.Trim(literal.Raw, "'")
	for _, replacement := range []struct{ from, to string }{
		{"%Y", "yyyy"}, {"%y", "yy"}, {"%B", "MMMM"}, {"%b", "mon"},
		{"%m", "MM"}, {"%d", "DD"}, {"%e", "DD"}, {"%H", "HH24"},
		{"%I", "HH12"}, {"%M", "MI"}, {"%S", "SS"}, {"%f", "FF"},
		{"%z", "TZH:TZM"},
	} {
		value = strings.ReplaceAll(value, replacement.from, replacement.to)
	}
	return &LiteralExpr{KindValue: LiteralString, Raw: "'" + value + "'"}
}

func rewriteBigQueryArrayStructSubquery(function *FunctionCallExpr) Expr {
	if function == nil || len(function.Args) != 1 {
		return nil
	}
	subquery, ok := function.Args[0].(*SubqueryExpr)
	if !ok || subquery.Query == nil || !strings.EqualFold(strings.TrimSpace(subquery.Query.SelectModifier), "AS STRUCT") {
		return nil
	}
	objectArgs := make([]Expr, 0, len(subquery.Query.Projections)*2)
	for index, projection := range subquery.Query.Projections {
		key := "_" + strconv.Itoa(index)
		value := projection.Expr
		if projection.Alias != nil {
			key = projection.Alias.Text
		} else if identifier, ok := projection.Expr.(*IdentifierExpr); ok && len(identifier.Parts) > 0 {
			key = identifier.Parts[len(identifier.Parts)-1].Text
		}
		objectArgs = append(objectArgs, &LiteralExpr{KindValue: LiteralString, Raw: "'" + strings.ReplaceAll(key, "'", "''") + "'"}, value)
	}
	inner := *subquery.Query
	inner.SelectModifier = ""
	inner.Projections = []SelectItem{{Expr: &FunctionCallExpr{
		Name: []Identifier{{Text: "ARRAY_AGG"}},
		Args: []Expr{&FunctionCallExpr{
			Name: []Identifier{{Text: "OBJECT_CONSTRUCT"}},
			Args: objectArgs,
		}},
	}}}
	text, err := GenerateWithOptions(&inner, GenerateOptions{Canonical: true, Dialect: DialectSnowflake})
	if err != nil {
		return nil
	}
	return &RawExpr{Raw: "(" + text + ")"}
}

func normalizeDuckDBJSONPath(expression Expr) Expr {
	switch value := expression.(type) {
	case *LiteralExpr:
		if value.KindValue == LiteralString && len(value.Raw) >= 2 {
			content := strings.Trim(value.Raw, "'")
			if strings.HasPrefix(content, "$") || strings.HasPrefix(content, "/") {
				return value
			}
			value.Raw = "'$." + duckDBJSONPathSegments(content) + "'"
			return value
		}
		if value.KindValue == LiteralNumber {
			return &LiteralExpr{KindValue: LiteralString, Raw: "'$[" + value.Raw + "]'"}
		}
	}
	return expression
}

func duckDBJSONPathSegments(content string) string {
	var result strings.Builder
	for index := 0; index < len(content); {
		if strings.HasPrefix(content[index:], `["`) {
			end := index + 2
			for end < len(content) {
				if content[end] == '"' && (end == index+2 || content[end-1] != '\\') && end+1 < len(content) && content[end+1] == ']' {
					break
				}
				end++
			}
			if end+1 < len(content) && content[end] == '"' && content[end+1] == ']' {
				result.WriteString(`."`)
				result.WriteString(content[index+2 : end])
				result.WriteByte('"')
				index = end + 2
				continue
			}
		}
		result.WriteByte(content[index])
		index++
	}
	return strings.ReplaceAll(result.String(), "'", "''")
}

func normalizeSnowflakeDuckDBTimestampFormat(expression Expr) string {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString || len(literal.Raw) < 2 {
		return renderExpr(expression)
	}
	content := strings.Trim(literal.Raw, "'")
	upper := strings.ToUpper(content)
	tokens := []struct {
		Snowflake string
		DuckDB    string
	}{
		{"YYYY", "%Y"}, {"SYYYY", "%Y"}, {"YYY", "%Y"}, {"YY", "%y"},
		{"HH24", "%H"}, {"HH12", "%I"}, {"HH", "%H"},
		{"MONTH", "%B"}, {"MON", "%b"}, {"MM", "%m"}, {"MI", "%M"},
		{"SS", "%S"}, {"DD", "%d"}, {"FF", "%n"}, {"AM", "%p"}, {"PM", "%p"},
	}
	var result strings.Builder
	for index := 0; index < len(content); {
		if content[index] == '"' {
			end := index + 1
			for end < len(content) && content[end] != '"' {
				end++
			}
			if end < len(content) {
				result.WriteString(content[index+1 : end])
				index = end + 1
				continue
			}
		}
		matched := false
		for _, token := range tokens {
			if strings.HasPrefix(upper[index:], token.Snowflake) {
				result.WriteString(token.DuckDB)
				index += len(token.Snowflake)
				if token.Snowflake == "FF" {
					for index < len(content) && content[index] >= '0' && content[index] <= '9' {
						index++
					}
				}
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		if upper[index] == 'D' {
			result.WriteString("%d")
		} else if upper[index] == 'M' {
			result.WriteString("%m")
		} else if upper[index] == 'S' {
			result.WriteString("%S")
		} else {
			result.WriteByte(content[index])
		}
		index++
	}
	return "'" + strings.ReplaceAll(result.String(), "'", "''") + "'"
}

func snowflakeDuckDBOperand(expression Expr) string {
	text := renderExpr(expression)
	switch expression.(type) {
	case *BinaryExpr, *CaseExpr, *IsExpr, *UnaryExpr:
		return "(" + text + ")"
	default:
		return text
	}
}

func snowflakeDuckDBBitOperand(expression Expr) Expr {
	if function, ok := expression.(*FunctionCallExpr); ok && len(function.Name) == 1 {
		switch strings.ToUpper(function.Name[0].Text) {
		case "BITAND", "BITOR", "BITSHIFTLEFT", "BITSHIFTRIGHT":
			return &ParenthesizedExpr{Expr: expression}
		}
	}
	return &CastExpr{Keyword: "CAST", Value: expression, Type: identifierExpr("INT128")}
}

func snowflakeDuckDBCastIfNeeded(expression Expr, typeName string) Expr {
	text := renderExpr(expression)
	upper := strings.ToUpper(strings.TrimSpace(text))
	suffix := " AS " + strings.ToUpper(typeName) + ")"
	if strings.HasPrefix(upper, "CAST(") && strings.HasSuffix(upper, suffix) {
		return &RawExpr{Raw: text}
	}
	return &CastExpr{Keyword: "CAST", Value: expression, Type: identifierExpr(typeName)}
}

func normalizeSnowflakeJSONPath(expression Expr) Expr {
	if value, ok := expression.(*LiteralExpr); ok && value.KindValue == LiteralString && len(value.Raw) >= 2 {
		content := strings.Trim(value.Raw, "'")
		if strings.HasPrefix(content, "$") && !strings.HasPrefix(content, "$.") {
			return &LiteralExpr{KindValue: LiteralString, Raw: "'[\"" + strings.ReplaceAll(content, "'", "''") + "\"]'"}
		}
		content = strings.TrimPrefix(content, "$.")
		content = strings.TrimPrefix(content, "$")
		content = strings.ReplaceAll(content, ":", ".")
		value.Raw = "'" + content + "'"
	}
	return expression
}

func snowflakeDateUnit(unit string) string {
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "Y", "YY", "YYY", "YYYY", "YR", "YEAR", "YEARS":
		return "YEAR"
	case "Q", "QQ", "QUARTER", "QUARTERS":
		return "QUARTER"
	case "M", "MM", "MON", "MONTH", "MONTHS":
		return "MONTH"
	case "D", "DD", "DAY", "DAYS":
		return "DAY"
	case "W", "WW", "WEEK", "WEEKS":
		return "WEEK"
	case "H", "HH", "HOUR", "HOURS":
		return "HOUR"
	case "MI", "MINUTE", "MINUTES":
		return "MINUTE"
	case "S", "SS", "SECOND", "SECONDS":
		return "SECOND"
	default:
		return strings.ToUpper(strings.TrimSpace(unit))
	}
}

func snowflakeDatePartUnit(unit string) string {
	trimmed := strings.TrimSpace(unit)
	switch strings.ToUpper(trimmed) {
	case "Y", "YY", "YYY", "YYYY", "YR", "Q", "QQ", "D", "DD", "W", "WW", "H", "HH", "MI", "SS", "MS", "US", "NS":
		return snowflakeDateUnit(trimmed)
	default:
		return trimmed
	}
}

func rewriteStructFunction(function *FunctionCallExpr, target Dialect) Expr {
	if target == DialectBigQuery || target == DialectSpark || target == DialectDatabricks {
		return nil
	}
	values := make([]string, 0, len(function.Args))
	keys := make([]string, 0, len(function.Args))
	hasAliases := false
	for index, argument := range function.Args {
		key := "_" + strconv.Itoa(index)
		value := argument
		if alias, ok := argument.(*AliasExpr); ok {
			// SQLGlot treats an unqualified identifier alias as positional when
			// lowering BigQuery STRUCT to a Presto/Trino ROW. Named aliases still
			// describe the ROW field for literals and qualified expressions.
			if target == DialectPresto || target == DialectTrino {
				if identifier, ok := alias.Expr.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted {
					value = alias.Expr
					function.Args[index] = value
				} else {
					hasAliases = true
					key = alias.Alias.Text
					value = alias.Expr
				}
			} else {
				hasAliases = true
				key = alias.Alias.Text
				value = alias.Expr
			}
		} else if target != DialectSnowflake {
			if identifier, ok := argument.(*IdentifierExpr); ok && len(identifier.Parts) > 1 {
				key = identifier.Parts[len(identifier.Parts)-1].Text
			}
		}
		if target == DialectDuckDB {
			values = append(values, renderDialectExpr(value, DialectDuckDB))
		} else {
			values = append(values, renderExpr(value))
		}
		keys = append(keys, key)
	}
	switch target {
	case DialectDuckDB:
		parts := make([]string, 0, len(values))
		for index, value := range values {
			parts = append(parts, "'"+strings.ReplaceAll(keys[index], "'", "''")+"': "+value)
		}
		return &RawExpr{Raw: "{" + strings.Join(parts, ", ") + "}"}
	case DialectSnowflake:
		parts := make([]string, 0, len(values)*2)
		for index, value := range values {
			parts = append(parts, "'"+strings.ReplaceAll(keys[index], "'", "''")+"'", value)
		}
		return &RawExpr{Raw: "OBJECT_CONSTRUCT(" + strings.Join(parts, ", ") + ")"}
	case DialectHive:
		for index, argument := range function.Args {
			if alias, ok := argument.(*AliasExpr); ok {
				function.Args[index] = alias.Expr
			}
		}
		return function
	case DialectPresto, DialectTrino:
		if !hasAliases {
			function.Name = []Identifier{{Text: "ROW"}}
			return function
		}
		return &RawExpr{Raw: "CAST(ROW(" + strings.Join(values, ", ") + ") AS ROW(" + structRowFields(keys, values) + "))"}
	}
	return nil
}

func structRowFields(keys, values []string) string {
	fields := make([]string, 0, len(keys))
	for index, key := range keys {
		fields = append(fields, key+" "+prestoMapType(values[index]))
	}
	return strings.Join(fields, ", ")
}

func sparkStringOperand(expression Expr) Expr {
	if isStringLiteral(expression) {
		return expression
	}
	return rawCast(renderExpr(expression), "STRING")
}

func tsqlDateNameFormat(expression Expr, tsql bool) Expr {
	unit := strings.ToLower(strings.Trim(renderExpr(expression), "'\"[]"))
	format := "'MMMM'"
	if unit == "dw" || unit == "weekday" || unit == "dayofweek" {
		format = "'EEEE'"
		if tsql {
			format = "'dddd'"
		}
	}
	return &LiteralExpr{KindValue: LiteralString, Raw: format}
}

func tsqlScaledDateAmount(expression Expr, multiplier int) string {
	if value, ok := numericLiteral(expression); ok {
		return strconv.Itoa(value * multiplier)
	}
	amount := renderExpr(expression)
	if multiplier == 1 {
		return amount
	}
	return "(" + amount + " * " + strconv.Itoa(multiplier) + ")"
}

func rewriteTSQLDateAdd(function *FunctionCallExpr, target Dialect) Expr {
	if function == nil || len(function.Args) != 3 || target == DialectTSQL {
		return nil
	}
	unit := strings.ToUpper(strings.Trim(renderExpr(function.Args[0]), "'\"[]"))
	switch unit {
	case "Y", "YY", "YYY", "YYYY", "YR", "YEAR", "YEARS":
		unit = "YEAR"
	case "Q", "QQ", "QUARTER", "QUARTERS":
		unit = "QUARTER"
	case "M", "MM", "MON", "MONTH", "MONTHS":
		unit = "MONTH"
	case "W", "WW", "WK", "WEEK", "WEEKS":
		unit = "WEEK"
	case "D", "DD", "DAY", "DAYS":
		unit = "DAY"
	}
	value := renderExpr(function.Args[2])
	amount := renderExpr(function.Args[1])
	if target == DialectDatabricks {
		function.Args[0] = identifierExpr(unit)
		return function
	}
	switch target {
	case DialectSpark:
		switch unit {
		case "YEAR":
			return &RawExpr{Raw: "ADD_MONTHS(" + value + ", " + tsqlScaledDateAmount(function.Args[1], 12) + ")"}
		case "QUARTER":
			return &RawExpr{Raw: "ADD_MONTHS(" + value + ", " + tsqlScaledDateAmount(function.Args[1], 3) + ")"}
		case "MONTH":
			return &RawExpr{Raw: "ADD_MONTHS(" + value + ", " + amount + ")"}
		default:
			if unit == "WEEK" {
				amount = tsqlScaledDateAmount(function.Args[1], 7)
			}
			return &RawExpr{Raw: "DATE_ADD(" + value + ", " + amount + ")"}
		}
	default:
		return genericDateAdd(value, amount, unit, target)
	}
}

func tsqlCurrentTimestampText(expression Expr, target Dialect) string {
	if function, ok := expression.(*FunctionCallExpr); ok && len(function.Name) == 1 && len(function.Args) == 0 {
		name := strings.ToUpper(function.Name[0].Text)
		if name == "GETDATE" || name == "CURRENT_TIMESTAMP" {
			switch target {
			case DialectDuckDB, DialectPostgreSQL, DialectPresto:
				return "CURRENT_TIMESTAMP"
			case DialectRedshift, DialectTSQL:
				return "GETDATE()"
			default:
				return "CURRENT_TIMESTAMP()"
			}
		}
	}
	if identifier, ok := expression.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && strings.EqualFold(identifier.Parts[0].Text, "CURRENT_TIMESTAMP") {
		switch target {
		case DialectDuckDB, DialectPostgreSQL, DialectPresto:
			return "CURRENT_TIMESTAMP"
		case DialectRedshift, DialectTSQL:
			return "GETDATE()"
		default:
			return "CURRENT_TIMESTAMP()"
		}
	}
	return renderExpr(expression)
}

func rewriteTSQLEOMonth(function *FunctionCallExpr, target Dialect) Expr {
	if function == nil || len(function.Args) < 1 || len(function.Args) > 2 {
		return nil
	}
	value := function.Args[0]
	valueText := tsqlCurrentTimestampText(value, target)
	offset := "0"
	if len(function.Args) == 2 {
		offset = renderExpr(function.Args[1])
	}
	dateValue := valueText
	switch target {
	case DialectSpark:
		dateValue = "TO_DATE(" + valueText + ")"
		if offset != "0" {
			dateValue = "ADD_MONTHS(" + dateValue + ", " + offset + ")"
		}
		return &RawExpr{Raw: "LAST_DAY(" + dateValue + ")"}
	case DialectSnowflake:
		dateValue = "TO_DATE(" + valueText + ")"
		if offset != "0" {
			dateValue = "DATEADD(MONTH, " + offset + ", " + dateValue + ")"
		}
		return &RawExpr{Raw: "LAST_DAY(" + dateValue + ")"}
	case DialectBigQuery:
		dateValue = "CAST(" + valueText + " AS DATE)"
		if offset != "0" {
			dateValue = "DATE_ADD(" + dateValue + ", INTERVAL " + offset + " MONTH)"
		}
		return &RawExpr{Raw: "LAST_DAY(" + dateValue + ")"}
	case DialectClickHouse:
		dateValue = "CAST(" + valueText + " AS Nullable(DATE))"
		if offset != "0" {
			dateValue = "DATE_ADD(MONTH, " + offset + ", " + dateValue + ")"
		}
		return &RawExpr{Raw: "LAST_DAY(" + dateValue + ")"}
	case DialectDuckDB:
		dateValue = "CAST(" + valueText + " AS DATE)"
		if offset != "0" {
			dateValue += " + INTERVAL (" + offset + ") MONTH"
		}
		return &RawExpr{Raw: "LAST_DAY(" + dateValue + ")"}
	case DialectMySQL:
		dateValue = valueText
		if offset != "0" {
			dateValue = "DATE_ADD(" + dateValue + ", INTERVAL " + offset + " MONTH)"
		} else {
			dateValue = "DATE(" + dateValue + ")"
		}
		return &RawExpr{Raw: "LAST_DAY(" + dateValue + ")"}
	case DialectPresto:
		dateValue = "CAST(CAST(" + valueText + " AS TIMESTAMP) AS DATE)"
		if offset != "0" {
			dateValue = "DATE_ADD('MONTH', " + offset + ", " + dateValue + ")"
		}
		return &RawExpr{Raw: "LAST_DAY_OF_MONTH(" + dateValue + ")"}
	case DialectPostgreSQL:
		dateValue = "CAST(" + valueText + " AS DATE)"
		if offset != "0" {
			dateValue += " + INTERVAL '" + strings.Trim(offset, "'") + " MONTH'"
		}
		return &RawExpr{Raw: "CAST(DATE_TRUNC('MONTH', " + dateValue + ") + INTERVAL '1 MONTH' - INTERVAL '1 DAY' AS DATE)"}
	case DialectRedshift:
		dateValue = "CAST(" + valueText + " AS DATE)"
		if offset != "0" {
			dateValue = "DATEADD(MONTH, " + offset + ", " + dateValue + ")"
		}
		return &RawExpr{Raw: "LAST_DAY(" + dateValue + ")"}
	case DialectTSQL:
		dateValue = "CAST(" + valueText + " AS DATE)"
		if offset != "0" {
			dateValue = "DATEADD(MONTH, " + offset + ", " + dateValue + ")"
		}
		return &RawExpr{Raw: "EOMONTH(" + dateValue + ")"}
	}
	return nil
}

func rewriteTSQLFormatToSpark(function *FunctionCallExpr) Expr {
	if function == nil || len(function.Args) != 2 {
		return nil
	}
	format, ok := stringLiteralValue(function.Args[1])
	if !ok {
		return nil
	}
	if strings.EqualFold(format, "m") {
		function.Args[1] = &LiteralExpr{KindValue: LiteralString, Raw: "'MMMM d'"}
		function.Name = []Identifier{{Text: "DATE_FORMAT"}}
		return function
	}
	lower := strings.ToLower(format)
	if strings.ContainsAny(format, "#0") || lower == "c" || lower == "f" || lower == "n" || lower == "p" {
		function.Name = []Identifier{{Text: "FORMAT_NUMBER"}}
		return function
	}
	function.Name = []Identifier{{Text: "DATE_FORMAT"}}
	return function
}

func rewriteTSQLConvertToSpark(function *FunctionCallExpr) Expr {
	if function == nil || len(function.Args) < 2 {
		return nil
	}
	typeText := strings.Trim(strings.TrimSpace(renderExpr(function.Args[0])), "[]\"")
	upperType := strings.ToUpper(typeText)
	base := upperType
	suffix := ""
	if open := strings.IndexByte(upperType, '('); open >= 0 {
		base = strings.TrimSpace(upperType[:open])
		suffix = strings.TrimSpace(upperType[open:])
	}
	if suffix == "(MAX)" && (base == "VARCHAR" || base == "NVARCHAR" || base == "CHAR" || base == "NCHAR") {
		base = "STRING"
		suffix = ""
	} else {
		switch base {
		case "NVARCHAR", "VARCHAR":
			base = "VARCHAR"
		case "NCHAR", "CHAR":
			base = "CHAR"
		case "INT", "INTEGER":
			base = "INT"
		case "REAL":
			base = "FLOAT"
		case "FLOAT":
			if suffix == "(64)" {
				base = "DOUBLE"
			}
		case "BIT":
			base = "BOOLEAN"
		case "UNIQUEIDENTIFIER":
			base = "STRING"
		case "DATETIME", "DATETIME2", "DATETIMEOFFSET":
			base = "TIMESTAMP"
		case "MONEY":
			base, suffix = "DECIMAL", "(15, 4)"
		case "SMALLMONEY":
			base, suffix = "DECIMAL", "(6, 4)"
		}
		if suffix == "" && (base == "VARCHAR" || base == "CHAR") {
			suffix = "(30)"
		}
	}
	targetType := base + suffix
	value := function.Args[1]
	style := ""
	if len(function.Args) >= 3 {
		style = strings.TrimSpace(renderExpr(function.Args[2]))
	}
	if style == "120" || style == "121" {
		formatText := "yyyy-MM-dd HH:mm:ss"
		if style == "121" {
			formatText = "yyyy-MM-dd HH:mm:ss.SSSSSS"
			if base == "DATE" || base == "TIMESTAMP" {
				formatText = "yyyy-M-d H:m:s.SSSSSS"
			}
		}
		format := &LiteralExpr{KindValue: LiteralString, Raw: "'" + formatText + "'"}
		switch base {
		case "DATE":
			return &FunctionCallExpr{Name: []Identifier{{Text: "TO_DATE"}}, Args: []Expr{value, format}}
		case "TIMESTAMP":
			return &FunctionCallExpr{Name: []Identifier{{Text: "TO_TIMESTAMP"}}, Args: []Expr{value, format}}
		case "VARCHAR", "CHAR", "STRING":
			formatted := &FunctionCallExpr{Name: []Identifier{{Text: "DATE_FORMAT"}}, Args: []Expr{value, format}}
			return &CastExpr{Keyword: strings.Replace(strings.ToUpper(function.Name[0].Text), "CONVERT", "CAST", 1), Value: formatted, Type: &RawExpr{Raw: targetType}}
		}
	}
	keyword := "CAST"
	if strings.EqualFold(function.Name[0].Text, "TRY_CONVERT") {
		keyword = "TRY_CAST"
	}
	return &CastExpr{Keyword: keyword, Value: value, Type: &RawExpr{Raw: targetType}}
}

func rewriteTSQLConvertToMySQL(function *FunctionCallExpr) Expr {
	if function == nil || len(function.Args) < 2 {
		return nil
	}
	typeText := strings.Trim(strings.TrimSpace(renderExpr(function.Args[0])), "[]\"")
	upperType := strings.ToUpper(typeText)
	base := upperType
	suffix := ""
	if open := strings.IndexByte(upperType, '('); open >= 0 {
		base = strings.TrimSpace(upperType[:open])
		suffix = strings.TrimSpace(upperType[open:])
	}
	mapped := base
	switch base {
	case "VARCHAR", "NVARCHAR", "CHAR", "NCHAR":
		mapped = "CHAR"
		if suffix == "(MAX)" {
			suffix = ""
		}
	case "INT", "INTEGER":
		mapped = "SIGNED"
		suffix = ""
	case "NUMERIC", "DECIMAL":
		mapped = "DECIMAL"
	case "DATETIME", "DATETIME2", "DATE":
		mapped = base
	}
	value := function.Args[1]
	if len(function.Args) >= 3 && isNumericRaw(function.Args[2], "120") && mapped == "CHAR" {
		value = &FunctionCallExpr{
			Name: []Identifier{{Text: "DATE_FORMAT"}},
			Args: []Expr{value, &LiteralExpr{KindValue: LiteralString, Raw: "'%Y-%m-%d %T'"}},
		}
	}
	return &CastExpr{Keyword: "CAST", Value: value, Type: &RawExpr{Raw: mapped + suffix}}
}

func rewriteFunction(function *FunctionCallExpr, target Dialect) Expr {
	if len(function.Name) != 1 || function.RawArgs != "" {
		return nil
	}
	name := strings.ToUpper(function.Name[0].Text)
	if name == "FROM_ISO8601_DATE" && len(function.Args) == 1 && target != DialectPresto && target != DialectTrino {
		return &CastExpr{Keyword: "CAST", Value: function.Args[0], Type: identifierExpr("DATE")}
	}
	if name == "REPLACE" && len(function.Args) == 2 {
		function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralString, Raw: "''"})
	}
	if name == "REGEXP_REPLACE" && len(function.Args) == 2 {
		function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralString, Raw: "''"})
	}
	if name == "SQUARE" && len(function.Args) == 1 {
		mapped := "POWER"
		if target == DialectDrill {
			mapped = "POW"
		}
		return &FunctionCallExpr{Name: []Identifier{{Text: mapped}}, Args: []Expr{function.Args[0], &LiteralExpr{KindValue: LiteralNumber, Raw: "2"}}}
	}
	if target == DialectSnowflake {
		switch name {
		case "VAR_POP":
			setFunctionName(function, "VARIANCE_POP")
		case "TRY_TO_TIME", "TRY_TO_TIMESTAMP", "TRY_TO_DATE":
			if len(function.Args) == 1 {
				typeName := strings.TrimPrefix(name, "TRY_TO_")
				return &CastExpr{Keyword: "TRY_CAST", Value: function.Args[0], Type: identifierExpr(typeName)}
			}
		case "STRTOK":
			if len(function.Args) == 2 {
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralNumber, Raw: "1"})
			}
		}
	}
	if name == "BOOLOR_AGG" || name == "BOOLAND_AGG" {
		if len(function.Args) == 1 && target != DialectSnowflake {
			mapped := "BOOL_OR"
			if name == "BOOLAND_AGG" {
				mapped = "BOOL_AND"
			}
			switch target {
			case DialectSQLite, DialectOracle, DialectMySQL:
				if name == "BOOLOR_AGG" {
					mapped = "MAX"
				} else {
					mapped = "MIN"
				}
			}
			setFunctionName(function, mapped)
		}
	}
	if name == "DIV0" || name == "DIV0NULL" {
		if len(function.Args) == 2 {
			numerator, denominator := function.Args[0], function.Args[1]
			condition := Expr(&BinaryExpr{Left: denominator, Operator: "=", Right: &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}})
			if name == "DIV0" {
				condition = &BinaryExpr{Left: condition, Operator: "AND", Right: &UnaryExpr{Operator: "NOT", Expr: &IsExpr{Value: numerator, Operator: "IS", Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}}}}
			} else {
				condition = &BinaryExpr{Left: condition, Operator: "OR", Right: &IsExpr{Value: denominator, Operator: "IS", Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}}}
			}
			return &FunctionCallExpr{Name: []Identifier{{Text: conditionalFunctionName(target)}}, Args: []Expr{condition, &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}, &BinaryExpr{Left: numerator, Operator: "/", Right: denominator}}}
		}
	}
	if name == "ZEROIFNULL" && len(function.Args) == 1 {
		value := function.Args[0]
		return &FunctionCallExpr{Name: []Identifier{{Text: conditionalFunctionName(target)}}, Args: []Expr{
			&IsExpr{Value: value, Operator: "IS", Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}},
			&LiteralExpr{KindValue: LiteralNumber, Raw: "0"}, value,
		}}
	}
	if name == "NULLIFZERO" && len(function.Args) == 1 {
		value := function.Args[0]
		return &FunctionCallExpr{Name: []Identifier{{Text: conditionalFunctionName(target)}}, Args: []Expr{
			&BinaryExpr{Left: value, Operator: "=", Right: &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}},
			&LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}, value,
		}}
	}
	if name == "STRUCT" {
		if rewritten := rewriteStructFunction(function, target); rewritten != nil {
			return rewritten
		}
	}
	if rewritten, handled := rewriteTimeConversionFunction(function, target, name); handled {
		return rewritten
	}
	if name == "DATEADD" {
		if rewritten := rewriteTSQLDateAdd(function, target); rewritten != nil {
			return rewritten
		}
	}
	if name == "EOMONTH" {
		if rewritten := rewriteTSQLEOMonth(function, target); rewritten != nil {
			return rewritten
		}
	}
	if name == "NVL2" && len(function.Args) >= 2 && rewritesNVL2(target) {
		isNull := &IsExpr{Value: function.Args[0], Operator: "IS", Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}}
		condition := Expr(&UnaryExpr{Operator: "NOT", Expr: isNull})
		if target == DialectClickHouse {
			condition = &UnaryExpr{Operator: "NOT", Expr: &ParenthesizedExpr{Expr: isNull}}
		}
		when := CaseWhen{Condition: condition, Result: function.Args[1]}
		var otherwise Expr
		if len(function.Args) > 2 {
			otherwise = function.Args[2]
		}
		return &CaseExpr{Whens: []CaseWhen{when}, Else: otherwise}
	}
	if name == "IFNULL" && (target == DialectBigQuery || target == DialectSpark || target == DialectMySQL) {
		setFunctionName(function, "COALESCE")
	}
	if target == DialectGeneric && name == "IF" {
		if len(function.Args) < 2 || len(function.Args) > 3 {
			return nil
		}
		return &CaseExpr{
			nodeBase: nodeBase{span: function.SourceSpan()},
			Whens:    []CaseWhen{{Condition: function.Args[0], Result: function.Args[1]}},
			Else: func() Expr {
				if len(function.Args) == 3 {
					return function.Args[2]
				}
				return nil
			}(),
		}
	}
	if target == DialectGeneric && name == "TIME_TO_TIME_STR" && len(function.Args) == 1 {
		return &CastExpr{
			nodeBase: nodeBase{span: function.SourceSpan()},
			Keyword:  "CAST",
			Value:    function.Args[0],
			Type:     &IdentifierExpr{Parts: []Identifier{{Text: "TEXT"}}},
		}
	}
	if rewriteTimeFunction(function, target, name) {
		return nil
	}
	if name == "SCHEMA_NAME" {
		switch target {
		case DialectMySQL:
			return &FunctionCallExpr{Name: []Identifier{{Text: "SCHEMA"}}}
		case DialectPostgreSQL:
			return identifierExpr("CURRENT_SCHEMA")
		case DialectSQLite:
			return &LiteralExpr{KindValue: LiteralString, Raw: "'main'"}
		}
	}
	if name == "JSON_ARRAYAGG" && target == DialectPostgreSQL {
		setFunctionName(function, "JSON_AGG")
		function.OrderBy = postgresAggregateDefaultNullOrder(append(function.OrderBy, function.WithinGroup...))
		function.WithinGroup = nil
	}
	if name == "STRING_AGG" {
		switch target {
		case DialectMySQL:
			if len(function.Args) == 2 {
				value := renderExpr(function.Args[0])
				if function.Distinct {
					value = "DISTINCT " + value
				}
				order := renderOrderItemsCompact(function.WithinGroup)
				if order == "" {
					order = renderOrderItemsCompact(function.OrderBy)
				}
				rawArgs := "(" + value
				if order != "" {
					rawArgs += " ORDER BY " + order
				}
				rawArgs += " SEPARATOR " + renderExpr(function.Args[1]) + ")"
				function.Name = []Identifier{{Text: "GROUP_CONCAT"}}
				function.RawArgs = rawArgs
				function.Args = nil
				function.OrderBy = nil
				function.WithinGroup = nil
				function.Distinct = false
			}
		case DialectPostgreSQL:
			if len(function.WithinGroup) > 0 {
				function.OrderBy = append(function.OrderBy, function.WithinGroup...)
				function.WithinGroup = nil
			}
			function.OrderBy = postgresAggregateDefaultNullOrder(function.OrderBy)
		case DialectSQLite:
			function.Name = []Identifier{{Text: "GROUP_CONCAT"}}
			function.WithinGroup = nil
			function.OrderBy = nil
		}
	}
	if target == DialectPostgreSQL && name == "CONCAT" && len(function.Args) >= 2 {
		result := function.Args[0]
		for _, argument := range function.Args[1:] {
			result = &BinaryExpr{Left: result, Operator: "||", Right: argument}
		}
		return result
	}
	if target == DialectMySQL && name == "XOR" {
		return foldXORBinary(function)
	}
	if target == DialectSQLite && (function.IgnoreNulls || function.RespectNulls) {
		function.IgnoreNulls = false
		function.RespectNulls = false
		function.NullsInside = false
		if function.Over != nil {
			for index := range function.Over.OrderBy {
				if !function.Over.OrderBy[index].Descending && !function.Over.OrderBy[index].NullsFirst {
					function.Over.OrderBy[index].NullsLast = true
				}
			}
		}
	}
	if target == DialectMySQL && function.NullsInside {
		function.NullsInside = false
		if function.Over != nil {
			function.Over.OrderBy = rewriteMySQLNullOrder(function.Over.OrderBy)
		}
	}
	if target == DialectBigQuery && (function.IgnoreNulls || function.RespectNulls) {
		function.NullsInside = true
	}
	if (target == DialectSpark || target == DialectDatabricks || target == DialectSnowflake) && function.NullsInside {
		function.NullsInside = false
	}
	if (target == DialectSpark || target == DialectDatabricks) && name == "ARRAY_AGG" && function.IgnoreNulls && !strings.Contains(strings.ToUpper(function.ArgumentTail), "LIMIT") {
		// Spark's COLLECT_LIST does not retain BigQuery's inline ordering when
		// IGNORE NULLS is lowered.  The ordered form is retained when a LIMIT
		// is present because the limit is part of the aggregate semantics.
		function.OrderBy = nil
	}
	if target == DialectDuckDB && (function.IgnoreNulls || function.RespectNulls) {
		if function.IgnoreNulls && name == "ARRAY_AGG" && len(function.Args) == 1 {
			function.Filter = &IsExpr{Value: function.Args[0], Operator: "IS NOT", Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}}
		}
		if !function.NullsInside || name == "ARRAY_AGG" {
			function.IgnoreNulls = false
			function.RespectNulls = false
			function.NullsInside = false
		}
	}
	if (name == "FROM_ISO8601_TIMESTAMP" || name == "FROM_ISO8601_TIMESTAMP_NANOS") && len(function.Args) == 1 {
		if target == DialectDuckDB || target == DialectSnowflake {
			return &CastExpr{Keyword: "CAST", Value: function.Args[0], Type: identifierExpr("TIMESTAMPTZ")}
		}
		if target == DialectBigQuery || target == DialectSpark || target == DialectDatabricks {
			return &CastExpr{Keyword: "CAST", Value: function.Args[0], Type: identifierExpr("TIMESTAMP")}
		}
	}
	if name == "IS_ASCII" && len(function.Args) == 1 {
		switch target {
		case DialectSQLite, DialectMySQL, DialectPostgreSQL, DialectTSQL, DialectOracle:
			return asciiCheckExpression(function.Args[0], target)
		}
	}
	if target == DialectDuckDB && name == "DECODE" && len(function.Args) >= 3 {
		return rewriteDecodeFunction(function)
	}
	switch target {
	case DialectDataFusion:
		if name == "LEN" {
			setFunctionName(function, "length")
		}
	case DialectDremio:
		switch name {
		case "COUNT":
			if function.Distinct && len(function.Args) > 1 {
				whens := make([]CaseWhen, 0, len(function.Args))
				for _, argument := range function.Args {
					whens = append(whens, CaseWhen{
						Condition: &IsExpr{
							Value:    argument,
							Operator: "IS",
							Right:    &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"},
						},
						Result: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"},
					})
				}
				function.Args = []Expr{&CaseExpr{
					Whens: whens,
					Else:  &ParenthesizedExpr{Expr: &TupleExpr{Items: append([]Expr(nil), function.Args...)}},
				}}
			}
		case "DATETYPE":
			if len(function.Args) == 3 {
				if year, okYear := numericLiteral(function.Args[0]); okYear {
					if month, okMonth := numericLiteral(function.Args[1]); okMonth {
						if day, okDay := numericLiteral(function.Args[2]); okDay {
							return &FunctionCallExpr{
								Name: []Identifier{{Text: "DATE"}},
								Args: []Expr{&LiteralExpr{KindValue: LiteralString, Raw: fmt.Sprintf("'%04d-%02d-%02d'", year, month, day)}},
							}
						}
					}
				}
				return &CastExpr{
					Keyword: "CAST",
					Value: &FunctionCallExpr{
						Name: []Identifier{{Text: "CONCAT"}},
						Args: []Expr{function.Args[0], &LiteralExpr{KindValue: LiteralString, Raw: "'-'"}, function.Args[1], &LiteralExpr{KindValue: LiteralString, Raw: "'-'"}, function.Args[2]},
					},
					Type: identifierExpr("DATE"),
				}
			}
		case "CURRENT_DATE_UTC":
			return identifierExpr("CURRENT_DATE_UTC")
		case "REPEATSTR":
			setFunctionName(function, "REPEAT")
		case "DATE_FORMAT":
			setFunctionName(function, "TO_CHAR")
		case "REGEXP_MATCHES":
			setFunctionName(function, "REGEXP_LIKE")
		case "DATE_PART":
			if len(function.Args) == 2 {
				return &ExtractExpr{Field: function.Args[0], Source: function.Args[1]}
			}
		}
	case DialectTSQL:
		switch name {
		case "SHA", "SHA1", "SHA2", "MD5":
			if len(function.Args) >= 1 {
				algorithm := name
				valueArgs := function.Args
				switch name {
				case "SHA":
					algorithm = "SHA1"
				case "SHA2":
					algorithm = "SHA2_256"
					if len(function.Args) >= 2 {
						if bits, ok := stringLiteralValue(function.Args[1]); ok {
							algorithm = "SHA2_" + bits
						} else if isNumericRaw(function.Args[1], "512") {
							algorithm = "SHA2_512"
						}
					}
					valueArgs = function.Args[:1]
				}
				return &FunctionCallExpr{
					Name: []Identifier{{Text: "HASHBYTES"}},
					Args: append([]Expr{&LiteralExpr{KindValue: LiteralString, Raw: "'" + algorithm + "'"}}, valueArgs[:1]...),
				}
			}
		case "ARRAY_TO_STRING":
			setFunctionName(function, "STRING_AGG")
		case "COALESCE":
			setFunctionName(function, "ISNULL")
		case "COUNT":
			if function.Star || len(function.Args) == 1 {
				setFunctionName(function, "COUNT_BIG")
			}
		case "NOW":
			setFunctionName(function, "GETDATE")
		case "CURRENT_TIMESTAMP":
			setFunctionName(function, "GETDATE")
		case "IF", "IIF":
			if len(function.Args) == 3 {
				function.Args[0] = booleanOperandTSQL(function.Args[0])
				setFunctionName(function, "IIF")
			}
		case "MAKE_TIME", "MAKETIME", "TIME_FROM_PARTS":
			setFunctionName(function, "TIMEFROMPARTS")
			for len(function.Args) < 5 {
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralNumber, Raw: "0"})
			}
		case "TIMESTAMP_FROM_PARTS", "DATETIME_FROM_PARTS":
			setFunctionName(function, "DATETIMEFROMPARTS")
			for len(function.Args) < 7 {
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralNumber, Raw: "0"})
			}
			if len(function.Args) == 7 {
				function.Args[6] = &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}
			}
		case "LAST_DAY":
			setFunctionName(function, "EOMONTH")
		case "LOCATE":
			setFunctionName(function, "CHARINDEX")
		case "FORMAT":
			if len(function.Args) == 2 {
				if format, ok := stringLiteralValue(function.Args[1]); ok && strings.EqualFold(format, "m") {
					function.Args[1] = &LiteralExpr{KindValue: LiteralString, Raw: "'MMMM d'"}
				}
			}
		case "LENGTH":
			setFunctionName(function, "LEN")
		case "VAR_SAMP":
			setFunctionName(function, "VAR")
		case "STDDEV_SAMP":
			setFunctionName(function, "STDEV")
		case "BOOL_AND":
			if len(function.Args) == 1 {
				return booleanAggregateTSQL("MIN", function.Args[0])
			}
		case "BOOL_OR":
			if len(function.Args) == 1 {
				return booleanAggregateTSQL("MAX", function.Args[0])
			}
		case "DATE_TRUNC":
			if len(function.Args) == 2 {
				function.Args[0] = identifierExpr(strings.ToUpper(strings.Trim(renderExpr(function.Args[0]), "'")))
				setFunctionName(function, "DATETRUNC")
			}
		case "DATE_PART":
			if len(function.Args) == 2 {
				function.Args[0] = unquoteDatePart(function.Args[0])
				setFunctionName(function, "DATEPART")
			}
		case "DATEPART":
			if len(function.Args) == 2 {
				function.Args[0] = unquoteDatePart(function.Args[0])
			}
		case "DATETRUNC":
			if len(function.Args) == 2 {
				function.Args[0] = identifierExpr(strings.ToUpper(strings.Trim(renderExpr(function.Args[0]), "'\"[]")))
				if literal, ok := function.Args[1].(*LiteralExpr); ok && literal.KindValue == LiteralString {
					function.Args[1] = &CastExpr{Keyword: "CAST", Value: literal, Type: identifierExpr("DATETIME2")}
				}
			}
		case "STARTS_WITH":
			if len(function.Args) == 2 {
				return startsWithTSQL(function.Args[0], function.Args[1])
			}
		case "CONVERT", "TRY_CONVERT":
			if len(function.Args) > 0 {
				if identifier, ok := function.Args[0].(*IdentifierExpr); ok && len(identifier.Parts) == 1 && strings.EqualFold(identifier.Parts[0].Text, "INT") {
					identifier.Parts[0].Text = "INTEGER"
				}
			}
		case "JSON_QUERY", "JSON_VALUE":
			if len(function.Args) >= 1 {
				args := make([]string, len(function.Args))
				for index := range function.Args {
					args[index] = renderExpr(function.Args[index])
				}
				value := strings.Join(args, ", ")
				if len(function.Args) == 1 {
					value += ", '$'"
				}
				return &RawExpr{Raw: "ISNULL(JSON_QUERY(" + value + "), JSON_VALUE(" + value + "))"}
			}
		case "DATENAME":
			if len(function.Args) == 2 {
				format := renderExpr(tsqlDateNameFormat(function.Args[0], true))
				return &FunctionCallExpr{Name: []Identifier{{Text: "FORMAT"}}, Args: []Expr{rawCast(renderExpr(function.Args[1]), "DATETIME2"), &LiteralExpr{KindValue: LiteralString, Raw: format}}}
			}
		case "TRUNC", "TRUNCATE":
			if len(function.Args) == 1 || (len(function.Args) == 2 && numericLiteralExpr(function.Args[1])) {
				setFunctionName(function, "ROUND")
				if len(function.Args) == 1 {
					function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralNumber, Raw: "0"})
				}
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralNumber, Raw: "1"})
			}
		}
	case DialectSingleStore:
		if name == "CURTIME" {
			setFunctionName(function, "CURRENT_TIME")
		}
	case DialectRedshift:
		switch name {
		case "TEXTLEN":
			setFunctionName(function, "LENGTH")
		case "DATEDIFF":
			if len(function.Args) == 3 {
				if identifier, ok := function.Args[0].(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted {
					identifier.Parts[0].Text = strings.ToUpper(identifier.Parts[0].Text)
				}
			}
		case "STRUCT_EXTRACT":
			if len(function.Args) == 2 {
				if field, ok := stringLiteralValue(function.Args[1]); ok {
					return &FieldExpr{Target: function.Args[0], Field: Identifier{Text: field}}
				}
			}
		}
	case DialectDoris:
		switch name {
		case "DATE_ADD", "ADDDATE", "DATE_SUB", "SUBDATE":
			if len(function.Args) == 2 {
				mapped := "DATE_ADD"
				if name == "DATE_SUB" || name == "SUBDATE" {
					mapped = "DATE_SUB"
				}
				setFunctionName(function, mapped)
				if _, alreadyInterval := function.Args[1].(*IntervalExpr); !alreadyInterval {
					function.Args[1] = &IntervalExpr{Value: function.Args[1], Qualifiers: []Expr{identifierExpr("DAY")}}
				}
			}
		}
	case DialectMySQL:
		switch name {
		case "CONVERT", "TRY_CONVERT":
			if rewritten := rewriteTSQLConvertToMySQL(function); rewritten != nil {
				return rewritten
			}
		case "STRING_AGG":
			if len(function.Args) == 2 {
				function.Name = []Identifier{{Text: "GROUP_CONCAT"}}
				function.RawArgs = "(" + renderExpr(function.Args[0]) + " SEPARATOR " + renderExpr(function.Args[1]) + ")"
				function.Args = nil
			}
		case "ARRAY_AGG":
			setFunctionName(function, "GROUP_CONCAT")
		case "BOOL_AND":
			setFunctionName(function, "MIN")
		case "BOOL_OR":
			setFunctionName(function, "MAX")
		case "DATE_TRUNC":
			if len(function.Args) == 2 {
				function.Name = []Identifier{{Text: "DATE"}}
				function.Args = function.Args[1:]
			}
		}
	case DialectBigQuery:
		switch name {
		case "ISNAN":
			setFunctionName(function, "IS_NAN")
		case "ISINF":
			setFunctionName(function, "IS_INF")
		case "ARRAY_CONTAINS":
			if len(function.Args) == 2 {
				value := identifierExpr("_col")
				return &ExistsExpr{
					Query: &SelectStmt{
						Projections: []SelectItem{{Expr: &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}}},
						From:        []TableExpr{{Primary: &TableFunctionFrom{Name: []Identifier{{Text: "UNNEST"}}, Args: []Expr{function.Args[0]}, Alias: &Identifier{Text: "_col"}}}},
						Where:       &BinaryExpr{Left: value, Operator: "=", Right: function.Args[1]},
					},
				}
			}
		case "JSON_OBJECT":
			if len(function.Args) == 2 {
				keys, keysOK := function.Args[0].(*FunctionCallExpr)
				values, valuesOK := function.Args[1].(*FunctionCallExpr)
				if keysOK && valuesOK && keys.ArrayLiteral && values.ArrayLiteral && len(keys.Args) == len(values.Args) {
					args := make([]Expr, 0, len(keys.Args)*2)
					for index := range keys.Args {
						args = append(args, keys.Args[index], values.Args[index])
					}
					function.Args = args
				}
			}
		case "GROUP_CONCAT":
			setFunctionName(function, "STRING_AGG")
		case "REGEXP_SUBSTR":
			setFunctionName(function, "REGEXP_EXTRACT")
		case "MOD":
			for index, argument := range function.Args {
				if parenthesized, ok := argument.(*ParenthesizedExpr); ok {
					function.Args[index] = parenthesized.Expr
				}
			}
		case "LAST_DAY":
			if len(function.Args) == 2 {
				if week, ok := function.Args[1].(*FunctionCallExpr); ok && len(week.Name) == 1 && strings.EqualFold(week.Name[0].Text, "WEEK") {
					function.Args[1] = identifierExpr("WEEK")
				}
			}
		case "OCTET_LENGTH":
			setFunctionName(function, "BYTE_LENGTH")
		case "JSON_EXTRACT_STRING_ARRAY":
			setFunctionName(function, "JSON_VALUE_ARRAY")
		case "SPLIT":
			if len(function.Args) == 1 {
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralString, Raw: "','"})
			}
		case "COUNT_IF":
			setFunctionName(function, "COUNTIF")
		case "UUID":
			setFunctionName(function, "GENERATE_UUID")
		case "MAKE_DATE":
			setFunctionName(function, "DATE")
		case "XOR":
			if len(function.Args) == 2 {
				return &BinaryExpr{Left: function.Args[0], Operator: "^", Right: function.Args[1]}
			}
		case "STRUCT_PACK":
			if entries, ok := structPackEntries(function); ok {
				args := make([]Expr, 0, len(entries))
				for _, entry := range entries {
					args = append(args, &AliasExpr{Expr: &RawExpr{Raw: entry.Value}, Alias: Identifier{Text: entry.Key}})
				}
				return &FunctionCallExpr{Name: []Identifier{{Text: "STRUCT"}}, Args: args}
			}
		case "LIST_CONCAT":
			setFunctionName(function, "ARRAY_CONCAT")
		}
	case DialectDuckDB:
		switch name {
		case "COUNT_BIG":
			setFunctionName(function, "COUNT")
			return function
		case "DATETRUNC":
			if len(function.Args) == 2 {
				unit := strings.ToUpper(strings.Trim(renderExpr(function.Args[0]), "'\"[]"))
				function.Name = []Identifier{{Text: "DATE_TRUNC"}}
				function.Args[0] = &LiteralExpr{KindValue: LiteralString, Raw: "'" + unit + "'"}
				if literal, ok := function.Args[1].(*LiteralExpr); ok && literal.KindValue == LiteralString {
					function.Args[1] = &CastExpr{Keyword: "CAST", Value: literal, Type: identifierExpr("TIMESTAMP")}
				}
			}
		case "BIT_OR", "BIT_AND", "BIT_XOR":
			if len(function.Args) == 1 {
				if cast, ok := function.Args[0].(*CastExpr); ok {
					upperType := strings.ToUpper(renderExpr(cast.Type))
					if typeName, typeOK := castTypeIdentifier(cast.Type); typeOK {
						upperType = strings.ToUpper(typeName.Text)
					}
					value := Expr(cast)
					if upperType == "REAL" || upperType == "DOUBLE" {
						value = &FunctionCallExpr{Name: []Identifier{{Text: "ROUND"}}, Args: []Expr{value}}
					}
					function.Args[0] = &CastExpr{Keyword: "CAST", Value: value, Type: identifierExpr("INT")}
				}
			}
		case "LIST_VALUE":
			function.Name = []Identifier{{Text: "ARRAY"}}
			function.ArrayLiteral = true
		case "DATE_ADD", "DATE_SUB":
			if len(function.Args) == 2 {
				operator := "+"
				if name == "DATE_SUB" {
					operator = "-"
				}
				if _, ok := function.Args[1].(*IntervalExpr); ok {
					return &BinaryExpr{Left: function.Args[0], Operator: operator, Right: function.Args[1]}
				}
			}
		case "RANGE", "GENERATE_SERIES":
			setFunctionName(function, name)
			if len(function.Args) == 1 {
				function.Args = append([]Expr{&LiteralExpr{KindValue: LiteralNumber, Raw: "0"}}, function.Args...)
			}
		case "LEN":
			setFunctionName(function, "LENGTH")
		case "EDITDIST3":
			setFunctionName(function, "LEVENSHTEIN")
		case "STRING_TO_ARRAY":
			setFunctionName(function, "STR_SPLIT")
		case "LIST_CONTAINS":
			setFunctionName(function, "ARRAY_CONTAINS")
		case "LIST_HAS_ANY":
			if len(function.Args) == 2 {
				return &BinaryExpr{Left: function.Args[0], Operator: "&&", Right: function.Args[1]}
			}
		case "LIST_REVERSE_SORT":
			setFunctionName(function, "ARRAY_REVERSE_SORT")
		case "DATEDIFF", "DATE_DIFF":
			setFunctionName(function, "DATE_DIFF")
			if len(function.Args) > 0 {
				if literal, ok := function.Args[0].(*LiteralExpr); ok && literal.KindValue == LiteralString {
					literal.Raw = "'" + strings.ToUpper(strings.Trim(literal.Raw, "'")) + "'"
				}
			}
		case "ARRAY_COMPACT", "ARRAY_CONSTRUCT_COMPACT":
			var value Expr
			if name == "ARRAY_COMPACT" && len(function.Args) == 1 {
				value = function.Args[0]
			} else {
				value = &FunctionCallExpr{Name: []Identifier{{Text: "ARRAY"}}, Args: function.Args, ArrayLiteral: true}
			}
			return &FunctionCallExpr{Name: []Identifier{{Text: "LIST_FILTER"}}, Args: []Expr{value, duckDBNotNullLambda()}}
		case "PERCENTILE_CONT", "PERCENTILE_DISC":
			if len(function.Args) == 1 && len(function.WithinGroup) == 1 {
				order := function.WithinGroup[0]
				quantile := "QUANTILE_CONT"
				if name == "PERCENTILE_DISC" {
					quantile = "QUANTILE_DISC"
				}
				function.Name = []Identifier{{Text: quantile}}
				function.Args = []Expr{order.Expr, function.Args[0]}
				function.OrderBy = []OrderItem{order}
				function.WithinGroup = nil
			}
		case "REGEXP_EXTRACT", "REGEXP_EXTRACT_ALL":
			if len(function.Args) == 3 && isNumericRaw(function.Args[2], "0") {
				function.Args = function.Args[:2]
			}
		case "JSON_EXTRACT_STRING", "JSON_EXTRACT_PATH":
			if len(function.Args) == 2 {
				operator := "->>"
				if name == "JSON_EXTRACT_PATH" {
					operator = "->"
				}
				return &BinaryExpr{Left: function.Args[0], Operator: operator, Right: function.Args[1]}
			}
		case "JSON_EXTRACT_PATH_TEXT":
			if len(function.Args) == 2 {
				return &BinaryExpr{Left: function.Args[0], Operator: "->>", Right: function.Args[1]}
			}
		case "JSON_EXTRACT":
			if len(function.Args) == 2 {
				return &BinaryExpr{Left: function.Args[0], Operator: "->", Right: function.Args[1]}
			}
		case "LOGICAL_OR", "LOGICAL_AND":
			if len(function.Args) == 1 {
				if name == "LOGICAL_OR" {
					setFunctionName(function, "BOOL_OR")
				} else {
					setFunctionName(function, "BOOL_AND")
				}
				function.Args[0] = &CastExpr{Keyword: "CAST", Value: function.Args[0], Type: identifierExpr("BOOLEAN")}
			}
		case "CONVERT":
			if len(function.Args) == 3 && isIdentifierNamed(function.Args[0], "DATETIME") && isNumericRaw(function.Args[2], "126") {
				return &FunctionCallExpr{
					Name: []Identifier{{Text: "STRPTIME"}},
					Args: []Expr{function.Args[1], &LiteralExpr{KindValue: LiteralString, Raw: "'%Y-%m-%dT%H:%M:%S.%f'"}},
				}
			}
		case "DATETIMEFROMPARTS":
			if len(function.Args) == 7 {
				seconds := &BinaryExpr{
					Left:     function.Args[5],
					Operator: "+",
					Right:    &ParenthesizedExpr{Expr: &BinaryExpr{Left: function.Args[6], Operator: "/", Right: &LiteralExpr{KindValue: LiteralNumber, Raw: "1000.0"}}},
				}
				args := append([]Expr(nil), function.Args[:5]...)
				args = append(args, seconds)
				return &FunctionCallExpr{Name: []Identifier{{Text: "MAKE_TIMESTAMP"}}, Args: args}
			}
		case "STRUCT_PACK":
			if len(function.Args) == 1 {
				if raw, ok := function.Args[0].(*RawExpr); ok && strings.HasPrefix(strings.TrimSpace(raw.Raw), "*COLUMNS") {
					return &RawExpr{Raw: "{'_0': " + strings.TrimSpace(raw.Raw) + "}"}
				}
			}
			if entries, ok := structPackEntries(function); ok {
				parts := make([]string, 0, len(entries))
				for _, entry := range entries {
					parts = append(parts, "'"+strings.ReplaceAll(entry.Key, "'", "''")+"': "+entry.Value)
				}
				return &RawExpr{Raw: "{" + strings.Join(parts, ", ") + "}"}
			}
		case "ARRAY_GENERATE_RANGE":
			setFunctionName(function, "GENERATE_SERIES")
		case "FORMAT":
			if len(function.Args) == 2 {
				if literal, ok := function.Args[0].(*LiteralExpr); ok && literal.KindValue == LiteralString {
					literal.Raw = strings.ReplaceAll(literal.Raw, "%s", "{}")
				}
			}
		case "FROM_HEX":
			setFunctionName(function, "UNHEX")
		case "GETDATE":
			setFunctionName(function, "CURRENT_TIMESTAMP")
		case "TODAY":
			return identifierExpr("CURRENT_DATE")
		case "GET_CURRENT_TIME":
			return identifierExpr("CURRENT_TIME")
		case "CURRENT_LOCALTIMESTAMP":
			return identifierExpr("LOCALTIMESTAMP")
		case "ANY_VALUE":
			if len(function.Args) == 1 {
				if raw, ok := function.Having.(*RawExpr); ok {
					words := strings.Fields(raw.Raw)
					if len(words) == 2 && (strings.EqualFold(words[0], "MAX") || strings.EqualFold(words[0], "MIN")) {
						mapped := "ARG_MAX_NULL"
						if strings.EqualFold(words[0], "MIN") {
							mapped = "ARG_MIN_NULL"
						}
						return &FunctionCallExpr{Name: []Identifier{{Text: mapped}}, Args: []Expr{function.Args[0], identifierExpr(words[1])}}
					}
				}
			}
		case "TO_VARIANT":
			if len(function.Args) == 1 {
				return &CastExpr{Keyword: "CAST", Value: function.Args[0], Type: identifierExpr("VARIANT")}
			}
		case "DATE_PART":
			if len(function.Args) == 2 {
				if rewritten := rewriteDuckDBDatePart(function); rewritten != nil {
					return rewritten
				}
			}
		case "NOW":
			return identifierExpr("CURRENT_TIMESTAMP")
		case "STRING_AGG":
			setFunctionName(function, "LISTAGG")
		case "VAR_SAMP":
			setFunctionName(function, "VARIANCE")
		case "APPROX_DISTINCT":
			setFunctionName(function, "APPROX_COUNT_DISTINCT")
		case "BOOL_AND", "BOOL_OR":
			if len(function.Args) == 1 {
				function.Args[0] = &CastExpr{Keyword: "CAST", Value: function.Args[0], Type: identifierExpr("BOOLEAN")}
			}
		}
	case DialectSnowflake:
		switch name {
		case "HASHBYTES":
			if len(function.Args) == 2 {
				if algorithm, ok := stringLiteralValue(function.Args[0]); ok {
					switch strings.ToUpper(algorithm) {
					case "MD5", "SHA1":
						function.Name = []Identifier{{Text: strings.ToUpper(algorithm)}}
						function.Args = function.Args[1:]
					case "SHA2_256", "SHA2_512":
						function.Name = []Identifier{{Text: "SHA2"}}
						bits := strings.TrimPrefix(strings.ToUpper(algorithm), "SHA2_")
						function.Args = []Expr{function.Args[1], &LiteralExpr{KindValue: LiteralNumber, Raw: bits}}
					}
				}
			}
		case "FORMAT_DATE", "FORMAT_DATETIME", "FORMAT_TIMESTAMP":
			if len(function.Args) == 2 {
				value := function.Args[1]
				if name != "FORMAT_DATE" {
					value = rawCast(renderExpr(value), "TIMESTAMP")
				}
				return &FunctionCallExpr{
					Name: []Identifier{{Text: "TO_CHAR"}},
					Args: []Expr{value, normalizeBigQuerySnowflakeFormat(function.Args[0])},
				}
			}
		case "ARRAY":
			if rewritten := rewriteBigQueryArrayStructSubquery(function); rewritten != nil {
				return rewritten
			}
		case "MD5_HEX":
			setFunctionName(function, "MD5")
		case "LIKE", "ILIKE":
			if len(function.Args) >= 2 && len(function.Args) <= 3 {
				operator := name
				binary := &BinaryExpr{Left: function.Args[0], Operator: operator, Right: function.Args[1]}
				if len(function.Args) == 3 {
					binary.Escape = function.Args[2]
				}
				return binary
			}
		case "RLIKE":
			setFunctionName(function, "REGEXP_LIKE")
		case "ARRAY_CONSTRUCT":
			function.ArrayLiteral = true
		case "BIT_NOT":
			setFunctionName(function, "BITNOT")
		case "BIT_AND":
			setFunctionName(function, "BITANDAGG")
		case "BIT_OR":
			setFunctionName(function, "BITORAGG")
		case "BIT_XOR":
			setFunctionName(function, "BITXORAGG")
		case "SYSTIMESTAMP", "GETDATE", "LOCALTIMESTAMP":
			setFunctionName(function, "CURRENT_TIMESTAMP")
		case "TIMEDIFF", "TIMESTAMPDIFF":
			setFunctionName(function, "DATEDIFF")
		case "DATEADD", "DATEDIFF":
			if len(function.Args) > 0 {
				if identifier, ok := function.Args[0].(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted {
					identifier.Parts[0].Text = snowflakeDateUnit(identifier.Parts[0].Text)
				}
			}
		case "DATE_TRUNC":
			if len(function.Args) > 0 {
				if identifier, ok := function.Args[0].(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted {
					function.Args[0] = &LiteralExpr{KindValue: LiteralString, Raw: "'" + snowflakeDateUnit(identifier.Parts[0].Text) + "'"}
				}
			}
		case "MOD":
			if len(function.Args) == 2 {
				return &BinaryExpr{Left: function.Args[0], Operator: "%", Right: function.Args[1]}
			}
		case "IFNULL", "NVL":
			setFunctionName(function, "COALESCE")
		case "POW":
			setFunctionName(function, "POWER")
		case "SQUARE":
			if len(function.Args) == 1 {
				setFunctionName(function, "POWER")
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralNumber, Raw: "2"})
			}
		case "APPROXIMATE_JACCARD_INDEX":
			setFunctionName(function, "APPROXIMATE_SIMILARITY")
		case "APPROX_TOP_K":
			if len(function.Args) == 1 {
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralNumber, Raw: "1"})
			}
		case "STRTOK_TO_ARRAY":
			if len(function.Args) == 1 {
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralString, Raw: "' '"})
			}
		case "TO_DECIMAL", "TO_NUMERIC", "TRY_TO_DECIMAL", "TRY_TO_NUMERIC":
			setFunctionName(function, strings.Replace(name, "DECIMAL", "NUMBER", 1))
			setFunctionName(function, strings.Replace(function.Name[0].Text, "NUMERIC", "NUMBER", 1))
		case "TO_NUMBER", "TRY_TO_NUMBER":
			if len(function.Args) > 1 && !isStringLiteral(function.Args[0]) && allNumericArguments(function.Args[1:]) {
				function.Args = function.Args[:1]
			}
		case "TO_DATE":
		case "TIMESTAMP_NTZ_FROM_PARTS", "TIMESTAMPFROMPARTS", "TIMESTAMPNTZFROMPARTS":
			for index, argument := range function.Args {
				if nested, ok := argument.(*FunctionCallExpr); ok && len(nested.Name) == 1 && len(nested.Args) == 1 {
					switch strings.ToUpper(nested.Name[0].Text) {
					case "TO_DATE":
						function.Args[index] = &CastExpr{Keyword: "CAST", Value: nested.Args[0], Type: identifierExpr("DATE")}
					case "TO_TIME":
						function.Args[index] = &CastExpr{Keyword: "CAST", Value: nested.Args[0], Type: identifierExpr("TIME")}
					}
				}
			}
			setFunctionName(function, "TIMESTAMP_FROM_PARTS")
		case "GET_PATH":
			if len(function.Args) == 2 {
				function.Args[1] = normalizeSnowflakeJSONPath(function.Args[1])
			}
		case "WEEKOFYEAR":
			setFunctionName(function, "WEEK")
		case "JSON_ARRAY":
			return &FunctionCallExpr{
				Name: []Identifier{{Text: "TO_VARIANT"}},
				Args: []Expr{&FunctionCallExpr{Name: []Identifier{{Text: "ARRAY_CONSTRUCT"}}, Args: function.Args}},
			}
		case "JSON":
			setFunctionName(function, "PARSE_JSON")
		case "LIST_DISTINCT":
			if len(function.Args) == 1 {
				return &FunctionCallExpr{Name: []Identifier{{Text: "ARRAY_DISTINCT"}}, Args: []Expr{&FunctionCallExpr{Name: []Identifier{{Text: "ARRAY_COMPACT"}}, Args: function.Args}}}
			}
		case "LIST":
			setFunctionName(function, "ARRAY_AGG")
		case "LIST_CONCAT":
			setFunctionName(function, "ARRAY_CAT")
		case "QUANTILE_CONT", "QUANTILE_DISC":
			if len(function.Args) == 2 {
				percentile := "PERCENTILE_CONT"
				if name == "QUANTILE_DISC" {
					percentile = "PERCENTILE_DISC"
				}
				return &FunctionCallExpr{Name: []Identifier{{Text: percentile}}, Args: []Expr{function.Args[1]}, WithinGroup: []OrderItem{{Expr: function.Args[0]}}}
			}
		case "REGEXP_EXTRACT":
			setFunctionName(function, "REGEXP_SUBSTR")
			if len(function.Args) == 4 {
				function.Args = []Expr{function.Args[0], function.Args[1], &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}, &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}, function.Args[3], function.Args[2]}
			}
		case "JSON_EXTRACT", "JSON_EXTRACT_PATH", "JSON_EXTRACT_STRING", "JSON_EXTRACT_PATH_TEXT":
			if len(function.Args) == 2 {
				return &FunctionCallExpr{Name: []Identifier{{Text: "GET_PATH"}}, Args: []Expr{
					&FunctionCallExpr{Name: []Identifier{{Text: "PARSE_JSON"}}, Args: []Expr{function.Args[0]}},
					normalizeSnowflakeJSONPath(function.Args[1]),
				}}
			}
		case "RANGE":
			setFunctionName(function, "ARRAY_GENERATE_RANGE")
		case "STRUCT_PACK":
			if entries, ok := structPackEntries(function); ok {
				args := make([]Expr, 0, len(entries)*2)
				for _, entry := range entries {
					args = append(args, &LiteralExpr{KindValue: LiteralString, Raw: "'" + strings.ReplaceAll(entry.Key, "'", "''") + "'"}, &RawExpr{Raw: entry.Value})
				}
				return &FunctionCallExpr{
					Name: []Identifier{{Text: "OBJECT_CONSTRUCT"}},
					Args: args,
				}
			}
		case "DATETIMEFROMPARTS":
			if len(function.Args) == 7 {
				args := append([]Expr(nil), function.Args[:6]...)
				args = append(args, &BinaryExpr{Left: function.Args[6], Operator: "*", Right: &LiteralExpr{KindValue: LiteralNumber, Raw: "1000000"}})
				return &FunctionCallExpr{Name: []Identifier{{Text: "TIMESTAMP_FROM_PARTS"}}, Args: args}
			}
		case "STRING_AGG":
			setFunctionName(function, "LISTAGG")
		case "BOOL_AND":
			setFunctionName(function, "BOOLAND_AGG")
		case "BOOL_OR":
			setFunctionName(function, "BOOLOR_AGG")
		case "VAR_SAMP":
			setFunctionName(function, "VARIANCE")
		case "STDDEV_SAMP":
			setFunctionName(function, "STDDEV")
		case "APPROX_DISTINCT":
			setFunctionName(function, "APPROX_COUNT_DISTINCT")
		case "APPROX_QUANTILE":
			setFunctionName(function, "APPROX_PERCENTILE")
		case "STARTS_WITH":
			setFunctionName(function, "STARTSWITH")
		case "ENDS_WITH":
			setFunctionName(function, "ENDSWITH")
		case "NOW":
			setFunctionName(function, "CURRENT_TIMESTAMP")
		case "DATE_PART":
			if len(function.Args) > 0 {
				wasString := isStringLiteral(function.Args[0])
				if !wasString {
					if identifier, ok := function.Args[0].(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted {
						identifier.Parts[0].Text = snowflakeDatePartUnit(identifier.Parts[0].Text)
					}
				}
			}
		case "FORMAT":
			if len(function.Args) == 2 {
				function.Name = []Identifier{{Text: "TO_CHAR"}}
				function.Args = function.Args[1:]
			}
		case "CURRENT_SCHEMAS":
			function.Args = nil
		}
	case DialectHive:
		switch name {
		case "STR_SPLIT", "STRING_TO_ARRAY":
			setFunctionName(function, "SPLIT")
			if len(function.Args) == 2 {
				function.Args[1] = regexpSplitDelimiter(function.Args[1])
			}
		case "STR_SPLIT_REGEX":
			setFunctionName(function, "SPLIT")
		case "STRUCT_EXTRACT":
			if len(function.Args) == 2 {
				if field, ok := stringLiteralValue(function.Args[1]); ok {
					return &FieldExpr{Target: function.Args[0], Field: Identifier{Text: field}}
				}
			}
		case "QUANTILE":
			setFunctionName(function, "PERCENTILE")
		case "UNNEST":
			setFunctionName(function, "EXPLODE")
		case "LIST_REVERSE_SORT", "ARRAY_REVERSE_SORT", "LIST_SORT":
			function.Name = []Identifier{{Text: "SORT_ARRAY"}}
			if name == "LIST_REVERSE_SORT" || name == "ARRAY_REVERSE_SORT" {
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralBoolean, Raw: "FALSE"})
			}
		case "LIST_CONCAT":
			setFunctionName(function, "CONCAT")
		}
	case DialectSpark:
		switch name {
		case "GETDATE":
			setFunctionName(function, "CURRENT_TIMESTAMP")
			return function
		case "JSON_QUERY", "JSON_VALUE":
			if len(function.Args) == 2 {
				setFunctionName(function, "GET_JSON_OBJECT")
				return function
			}
		case "FORMAT":
			if rewritten := rewriteTSQLFormatToSpark(function); rewritten != nil {
				return rewritten
			}
		case "COUNT_BIG":
			setFunctionName(function, "COUNT")
			return function
		case "SUSER_NAME", "SUSER_SNAME", "SYSTEM_USER":
			setFunctionName(function, "CURRENT_USER")
			function.Args = nil
			return function
		case "CHARINDEX":
			setFunctionName(function, "LOCATE")
			return function
		case "LEN":
			if len(function.Args) == 1 {
				return &FunctionCallExpr{Name: []Identifier{{Text: "LENGTH"}}, Args: []Expr{sparkStringOperand(function.Args[0])}}
			}
		case "LEFT", "RIGHT":
			if len(function.Args) == 2 {
				return &FunctionCallExpr{Name: []Identifier{{Text: name}}, Args: []Expr{sparkStringOperand(function.Args[0]), function.Args[1]}}
			}
		case "REPLICATE":
			setFunctionName(function, "REPEAT")
			return function
		case "ISNULL":
			setFunctionName(function, "COALESCE")
			return function
		case "DATEFROMPARTS":
			setFunctionName(function, "MAKE_DATE")
			return function
		case "DATENAME":
			if len(function.Args) == 2 {
				return &FunctionCallExpr{Name: []Identifier{{Text: "DATE_FORMAT"}}, Args: []Expr{rawCast(renderExpr(function.Args[1]), "TIMESTAMP"), tsqlDateNameFormat(function.Args[0], false)}}
			}
		case "DATEPART":
			if len(function.Args) == 2 {
				return &RawExpr{Raw: "EXTRACT(" + renderExpr(function.Args[0]) + " FROM " + renderExpr(function.Args[1]) + ")"}
			}
		case "HASHBYTES":
			if len(function.Args) == 2 {
				algorithm := strings.ToUpper(strings.Trim(renderExpr(function.Args[0]), "'\""))
				switch algorithm {
				case "SHA", "SHA1":
					return &FunctionCallExpr{Name: []Identifier{{Text: "SHA"}}, Args: []Expr{function.Args[1]}}
				case "SHA2_256", "SHA256":
					return &FunctionCallExpr{Name: []Identifier{{Text: "SHA2"}}, Args: []Expr{function.Args[1], &LiteralExpr{KindValue: LiteralNumber, Raw: "256"}}}
				case "SHA2_512", "SHA512":
					return &FunctionCallExpr{Name: []Identifier{{Text: "SHA2"}}, Args: []Expr{function.Args[1], &LiteralExpr{KindValue: LiteralNumber, Raw: "512"}}}
				case "MD5":
					return &FunctionCallExpr{Name: []Identifier{{Text: "MD5"}}, Args: []Expr{function.Args[1]}}
				}
			}
		case "CONVERT", "TRY_CONVERT":
			if rewritten := rewriteTSQLConvertToSpark(function); rewritten != nil {
				return rewritten
			}
		case "CONCAT":
			if len(function.Args) == 1 {
				function.Args[0] = &FunctionCallExpr{Name: []Identifier{{Text: "COALESCE"}}, Args: []Expr{function.Args[0], &LiteralExpr{KindValue: LiteralString, Raw: "''"}}}
			}
		case "ENCODE":
			if len(function.Args) == 1 {
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralString, Raw: "'utf-8'"})
			}
		case "DECODE":
			if len(function.Args) == 1 {
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralString, Raw: "'utf-8'"})
			}
		case "LIST_TRANSFORM":
			setFunctionName(function, "TRANSFORM")
		case "LIST_FILTER":
			setFunctionName(function, "FILTER")
		case "STR_SPLIT", "STRING_TO_ARRAY":
			setFunctionName(function, "SPLIT")
			if len(function.Args) == 2 {
				function.Args[1] = regexpSplitDelimiter(function.Args[1])
			}
		case "STR_SPLIT_REGEX":
			setFunctionName(function, "SPLIT")
		case "STRUCT_EXTRACT":
			if len(function.Args) == 2 {
				if field, ok := stringLiteralValue(function.Args[1]); ok {
					return &FieldExpr{Target: function.Args[0], Field: Identifier{Text: field}}
				}
			}
		case "QUANTILE":
			setFunctionName(function, "PERCENTILE")
		case "UNNEST":
			setFunctionName(function, "EXPLODE")
		case "LIST_REVERSE_SORT", "ARRAY_REVERSE_SORT":
			function.Name = []Identifier{{Text: "SORT_ARRAY"}}
			function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralBoolean, Raw: "FALSE"})
		case "LIST_SORT":
			setFunctionName(function, "SORT_ARRAY")
		case "LIST_CONCAT":
			setFunctionName(function, "CONCAT")
		case "RANGE":
			if len(function.Args) >= 2 {
				start, startOK := numericLiteral(function.Args[0])
				end, endOK := numericLiteral(function.Args[1])
				step := 1
				if len(function.Args) >= 3 {
					step, _ = numericLiteral(function.Args[2])
				}
				if step == 0 {
					return &FunctionCallExpr{Name: []Identifier{{Text: "ARRAY"}}}
				}
				if startOK && endOK {
					if start == end {
						return &FunctionCallExpr{Name: []Identifier{{Text: "ARRAY"}}}
					}
					function.Name = []Identifier{{Text: "SEQUENCE"}}
					function.Args[1] = &LiteralExpr{KindValue: LiteralNumber, Raw: strconv.Itoa(end - step)}
				} else {
					endExpr := &ParenthesizedExpr{Expr: &BinaryExpr{Left: function.Args[1], Operator: "-", Right: &LiteralExpr{KindValue: LiteralNumber, Raw: strconv.Itoa(step)}}}
					return &FunctionCallExpr{Name: []Identifier{{Text: "IF"}}, Args: []Expr{
						&BinaryExpr{Left: endExpr, Operator: "<", Right: function.Args[0]},
						&FunctionCallExpr{Name: []Identifier{{Text: "ARRAY"}}},
						&FunctionCallExpr{Name: []Identifier{{Text: "SEQUENCE"}}, Args: []Expr{function.Args[0], endExpr}},
					}}
				}
			}
		case "STRUCT_PACK":
			if entries, ok := structPackEntries(function); ok {
				args := make([]Expr, 0, len(entries))
				for _, entry := range entries {
					alias := Identifier{Text: entry.Key}
					if !isBareIdentifier(entry.Key) {
						alias.Quoted = true
						alias.Quote = '`'
					}
					args = append(args, &AliasExpr{Expr: &RawExpr{Raw: entry.Value}, Alias: alias})
				}
				return &FunctionCallExpr{Name: []Identifier{{Text: "STRUCT"}}, Args: args}
			}
		case "SUBSTR":
			if len(function.Args) == 1 && function.ArgumentTail != "" {
				fields := strings.Fields(function.ArgumentTail)
				if len(fields) == 4 && strings.EqualFold(fields[0], "FROM") && strings.EqualFold(fields[2], "FOR") {
					function.Name = []Identifier{{Text: "SUBSTRING"}}
					function.RawArgs = "(" + renderExpr(function.Args[0]) + ", " + fields[1] + ", " + fields[3] + ")"
					function.Args = nil
					function.ArgumentTail = ""
					break
				}
			}
			setFunctionName(function, "SUBSTRING")
		case "STR_TO_MAP":
			if len(function.Args) == 1 {
				function.Args = append(function.Args,
					&LiteralExpr{KindValue: LiteralString, Raw: "','"},
					&LiteralExpr{KindValue: LiteralString, Raw: "':'"},
				)
			}
		case "ARRAY_TO_STRING":
			setFunctionName(function, "ARRAY_JOIN")
		case "ANY_VALUE":
			if len(function.Args) == 1 {
				function.IgnoreNulls = true
			}
		case "FIRST", "FIRST_VALUE", "LAST", "LAST_VALUE":
			if len(function.Args) == 2 && isTrueLiteral(function.Args[1]) {
				function.Args = function.Args[:1]
				function.IgnoreNulls = true
				function.NullsInside = false
			}
		case "DATE_PART":
			if len(function.Args) == 2 {
				field := unquoteDatePart(function.Args[0])
				if identifier, ok := field.(*IdentifierExpr); ok && len(identifier.Parts) == 1 {
					identifier.Parts[0].Text = strings.ToLower(identifier.Parts[0].Text)
				}
				return &ExtractExpr{Field: field, Source: function.Args[1]}
			}
		case "STRING_AGG":
			setFunctionName(function, "LISTAGG")
		case "ARRAY_AGG":
			setFunctionName(function, "COLLECT_LIST")
		case "APPROX_DISTINCT":
			setFunctionName(function, "APPROX_COUNT_DISTINCT")
		case "STARTS_WITH":
			setFunctionName(function, "STARTSWITH")
		case "ENDS_WITH":
			setFunctionName(function, "ENDSWITH")
		case "NOW":
			setFunctionName(function, "CURRENT_TIMESTAMP")
		case "UNIX_TIMESTAMP":
			if len(function.Args) == 0 {
				function.Args = []Expr{&FunctionCallExpr{Name: []Identifier{{Text: "CURRENT_TIMESTAMP"}}}}
			}
		case "BIT_GET":
			setFunctionName(function, "GETBIT")
		case "CURDATE":
			return identifierExpr("CURRENT_DATE")
		}
	case DialectTrino:
		switch name {
		case "DATEDIFF", "DATE_DIFF":
			setFunctionName(function, "DATE_DIFF")
			if len(function.Args) > 0 {
				if literal, ok := function.Args[0].(*LiteralExpr); ok && literal.KindValue == LiteralString {
					literal.Raw = "'" + strings.ToUpper(strings.Trim(literal.Raw, "'")) + "'"
				}
			}
		case "LISTAGG":
			if len(function.Args) == 1 {
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralString, Raw: "','"})
			}
		case "TRIM":
			if len(function.Args) == 1 && function.ArgumentTail != "" && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(function.ArgumentTail)), "FROM ") {
				value := strings.TrimSpace(function.ArgumentTail[len("FROM "):])
				if identifier, ok := function.Args[0].(*IdentifierExpr); ok && len(identifier.Parts) == 1 && strings.EqualFold(identifier.Parts[0].Text, "LEADING") {
					function.Name = []Identifier{{Text: "LTRIM"}}
					function.Args = []Expr{&RawExpr{Raw: value}}
				} else {
					function.RawArgs = "(" + renderExpr(function.Args[0]) + " FROM " + value + ")"
					function.Args = nil
				}
				function.ArgumentTail = ""
			} else if len(function.Args) == 2 {
				function.RawArgs = "(" + renderExpr(function.Args[1]) + " FROM " + renderExpr(function.Args[0]) + ")"
				function.Args = nil
			}
		}
	case DialectPresto:
		switch name {
		case "ENCODE":
			if len(function.Args) == 1 {
				setFunctionName(function, "TO_UTF8")
			}
		case "DECODE":
			if len(function.Args) == 1 {
				setFunctionName(function, "FROM_UTF8")
			}
		case "MOD":
			if len(function.Args) == 2 {
				return &CastExpr{
					Keyword: "CAST",
					Value: &BinaryExpr{
						Left:     function.Args[0],
						Operator: "%",
						Right:    function.Args[1],
					},
					Type: identifierExpr("BIGINT"),
				}
			}
		case "DATE_ADD":
			if len(function.Args) > 0 {
				if identifier, ok := function.Args[0].(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted {
					function.Args[0] = genericDateUnitLiteral(identifier)
				}
			}
			if len(function.Args) >= 2 && !isPrestoBigint(function.Args[1]) {
				function.Args[1] = &CastExpr{Keyword: "CAST", Value: function.Args[1], Type: identifierExpr("BIGINT")}
			}
		case "DATE_DIFF":
			if len(function.Args) > 0 {
				if identifier, ok := function.Args[0].(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted {
					function.Args[0] = genericDateUnitLiteral(identifier)
				}
			}
		case "STRING_AGG":
			if len(function.Args) == 2 {
				return &FunctionCallExpr{
					Name: []Identifier{{Text: "ARRAY_JOIN"}},
					Args: []Expr{
						&FunctionCallExpr{Name: []Identifier{{Text: "ARRAY_AGG"}}, Args: []Expr{function.Args[0]}},
						function.Args[1],
					},
				}
			}
		case "XOR":
			setFunctionName(function, "BITWISE_XOR")
		case "LIST_SORT":
			setFunctionName(function, "ARRAY_SORT")
		case "LIST_REVERSE_SORT", "ARRAY_REVERSE_SORT":
			function.Name = []Identifier{{Text: "ARRAY_SORT"}}
			function.Args = append(function.Args, reverseSortLambda())
		case "QUANTILE":
			setFunctionName(function, "APPROX_PERCENTILE")
		case "STR_SPLIT", "STRING_TO_ARRAY":
			setFunctionName(function, "SPLIT")
		case "STR_SPLIT_REGEX":
			setFunctionName(function, "REGEXP_SPLIT")
		case "LIST_CONCAT":
			setFunctionName(function, "CONCAT")
		case "STRUCT_EXTRACT":
			if len(function.Args) == 2 {
				if field, ok := stringLiteralValue(function.Args[1]); ok {
					return &FieldExpr{Target: function.Args[0], Field: Identifier{Text: field}}
				}
			}
		case "ARRAY_TO_STRING":
			setFunctionName(function, "ARRAY_JOIN")
		case "STRUCT_PACK":
			if entries, ok := structPackEntries(function); ok {
				values := make([]string, 0, len(entries))
				fields := make([]string, 0, len(entries))
				for _, entry := range entries {
					values = append(values, entry.Value)
					fields = append(fields, entry.Key+" "+prestoMapType(entry.Value))
				}
				return &RawExpr{Raw: "CAST(ROW(" + strings.Join(values, ", ") + ") AS ROW(" + strings.Join(fields, ", ") + "))"}
			}
		}
	case DialectPostgreSQL:
		if name == "XOR" && len(function.Args) == 2 {
			return &BinaryExpr{Left: function.Args[0], Operator: "#", Right: function.Args[1]}
		}
		if name == "STRUCT_EXTRACT" && len(function.Args) == 2 {
			if field, ok := stringLiteralValue(function.Args[1]); ok {
				return &FieldExpr{Target: function.Args[0], Field: Identifier{Text: field}}
			}
		}
		if name == "LIST_CONCAT" {
			setFunctionName(function, "ARRAY_CAT")
		}
		if name == "LIST_CONTAINS" && len(function.Args) == 2 {
			value := renderDialectExpr(function.Args[1], DialectPostgreSQL)
			list := renderDialectExpr(function.Args[0], DialectPostgreSQL)
			return &RawExpr{Raw: "CASE WHEN " + value + " IS NULL THEN NULL ELSE COALESCE(" + value + " = ANY(" + list + "), FALSE) END"}
		}
		if name == "LIST_HAS_ANY" && len(function.Args) == 2 {
			return &BinaryExpr{Left: function.Args[0], Operator: "&&", Right: function.Args[1]}
		}
		if (name == "QUANTILE_CONT" || name == "QUANTILE_DISC") && len(function.Args) == 2 {
			percentile := "PERCENTILE_CONT"
			if name == "QUANTILE_DISC" {
				percentile = "PERCENTILE_DISC"
			}
			return &FunctionCallExpr{Name: []Identifier{{Text: percentile}}, Args: []Expr{function.Args[1]}, WithinGroup: []OrderItem{{Expr: function.Args[0]}}}
		}
		if name == "NOW" {
			return identifierExpr("CURRENT_TIMESTAMP")
		}
		if (name == "DATE_PART" || name == "DATEPART") && len(function.Args) == 2 {
			field := unquoteDatePart(function.Args[0])
			return &ExtractExpr{Field: field, Source: function.Args[1]}
		}
	}
	if target == DialectClickHouse {
		switch name {
		case "GROUP_CONCAT":
			if len(function.Args) == 1 && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(function.ArgumentTail)), "SEPARATOR ") {
				separator := strings.TrimSpace(strings.TrimSpace(function.ArgumentTail)[len("SEPARATOR "):])
				return &CallExpr{Callee: &FunctionCallExpr{Name: []Identifier{{Text: "groupConcat"}}, Args: []Expr{&LiteralExpr{KindValue: LiteralString, Raw: separator}}}, Args: []Expr{function.Args[0]}}
			}
		case "APPROX_COUNT_DISTINCT":
			setFunctionName(function, "uniq")
		case "ANY_VALUE":
			setFunctionName(function, "any")
		case "CONTAINS", "ARRAY_CONTAINS":
			setFunctionName(function, "has")
			if len(function.Args) > 0 {
				function.Args[0] = clickHouseArrayLiteral(function.Args[0])
			}
		case "XOR":
			return foldClickHouseFunction(function)
		case "QUANTILE":
			if len(function.Args) == 2 {
				if array, ok := function.Args[1].(*FunctionCallExpr); ok && array.ArrayLiteral {
					return &CallExpr{Callee: &FunctionCallExpr{Name: []Identifier{{Text: "quantiles"}}, Args: append([]Expr(nil), array.Args...)}, Args: []Expr{function.Args[0]}}
				}
				return &CallExpr{Callee: &FunctionCallExpr{Name: []Identifier{{Text: "quantile"}}, Args: []Expr{function.Args[1]}}, Args: []Expr{function.Args[0]}}
			}
		case "MEDIAN":
			if len(function.Args) == 1 {
				return &CallExpr{Callee: &FunctionCallExpr{Name: []Identifier{{Text: "quantile"}}, Args: []Expr{&LiteralExpr{KindValue: LiteralNumber, Raw: "0.5"}}}, Args: []Expr{function.Args[0]}}
			}
		case "TRUNC":
			setFunctionName(function, "trunc")
		case "CHR":
			setFunctionName(function, "CHAR")
		case "LAG":
			setFunctionName(function, "lagInFrame")
		case "LEAD":
			setFunctionName(function, "leadInFrame")
		case "IS_NAN", "ISNAN":
			setFunctionName(function, "isNaN")
		case "STARTS_WITH", "STARTSWITH":
			setFunctionName(function, "startsWith")
		case "DATE_FORMAT":
			setFunctionName(function, "formatDateTime")
		case "TO_START_OF_DAY", "TOSTARTOFDAY":
			if len(function.Args) == 1 {
				return &FunctionCallExpr{Name: []Identifier{{Text: "dateTrunc"}}, Args: []Expr{&LiteralExpr{KindValue: LiteralString, Raw: "'DAY'"}, function.Args[0]}}
			}
		case "TO_MONDAY", "TOMONDAY":
			if len(function.Args) == 1 {
				return &FunctionCallExpr{Name: []Identifier{{Text: "dateTrunc"}}, Args: []Expr{&LiteralExpr{KindValue: LiteralString, Raw: "'WEEK'"}, function.Args[0]}}
			}
		case "DATE_ADD", "DATE_DIFF":
			setFunctionName(function, name)
			if len(function.Args) > 0 {
				if literal, ok := function.Args[0].(*LiteralExpr); ok && literal.KindValue == LiteralString {
					function.Args[0] = identifierExpr(strings.ToUpper(strings.Trim(literal.Raw, "'")))
				}
			}
		case "TO_DATE":
			if len(function.Args) >= 1 {
				value := function.Args[0]
				if substring, ok := value.(*FunctionCallExpr); ok && len(substring.Name) == 1 && strings.EqualFold(substring.Name[0].Text, "SUBSTR") {
					setFunctionName(substring, "SUBSTRING")
				}
				args := []Expr{value}
				if len(function.Args) > 1 {
					args = append(args, clickHouseDateParseFormat(function.Args[1]))
				}
				return &CastExpr{Keyword: "CAST", Value: &FunctionCallExpr{Name: []Identifier{{Text: "STR_TO_DATE"}}, Args: args}, Type: &RawExpr{Raw: "Nullable(DATE)"}}
			}
		case "SUBSTR":
			setFunctionName(function, "SUBSTRING")
		case "SPLIT", "STRING_SPLIT":
			if len(function.Args) == 2 {
				function.Name = []Identifier{{Text: "splitByString"}}
				function.Args[0], function.Args[1] = function.Args[1], function.Args[0]
			}
		case "SPLIT_REGEX", "STRING_SPLIT_REGEX":
			if len(function.Args) == 2 {
				function.Name = []Identifier{{Text: "splitByRegexp"}}
				function.Args[0], function.Args[1] = function.Args[1], function.Args[0]
				if literal, ok := function.Args[0].(*LiteralExpr); ok && literal.KindValue == LiteralString {
					literal.Raw = strings.ReplaceAll(literal.Raw, `\`, `\\`)
				}
			}
		case "IF":
			if len(function.Args) == 3 {
				return &CaseExpr{nodeBase: function.nodeBase, Whens: []CaseWhen{{Condition: function.Args[0], Result: function.Args[1]}}, Else: function.Args[2]}
			}
		case "SPLITBYSTRING":
			setFunctionName(function, "splitByString")
		case "SPLITBYREGEXP":
			setFunctionName(function, "splitByRegexp")
		case "VARIANCE":
			setFunctionName(function, "varSamp")
		case "STDDEV":
			setFunctionName(function, "stddevSamp")
		case "DATE_TRUNC":
			setFunctionName(function, "dateTrunc")
		}
	}
	if target == DialectPresto && name == "REGEXP_MATCHES" && len(function.Args) == 2 {
		setFunctionName(function, "REGEXP_LIKE")
	}
	if target == DialectHive && name == "REGEXP_MATCHES" && len(function.Args) == 2 {
		return &BinaryExpr{Left: function.Args[0], Operator: "RLIKE", Right: function.Args[1]}
	}
	if target == DialectSpark && name == "REGEXP_MATCHES" && len(function.Args) == 2 {
		return &BinaryExpr{Left: function.Args[0], Operator: "RLIKE", Right: function.Args[1]}
	}
	if usesCoalesce(target) && (name == "NVL" || name == "IFNULL") {
		setFunctionName(function, "COALESCE")
		return nil
	}
	if usesIfNull(target) && name == "NVL" {
		setFunctionName(function, "IFNULL")
		return nil
	}
	if target == DialectPostgreSQL {
		switch name {
		case "GROUP_CONCAT":
			if len(function.Args) == 1 {
				setFunctionName(function, "STRING_AGG")
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralString, Raw: "','"})
			}
		case "SUBSTR", "SUBSTRING":
			if len(function.Args) == 3 {
				generator := generator{canonical: true, dialect: target}
				first, firstErr := generator.expr(function.Args[0], 0)
				start, startErr := generator.expr(function.Args[1], 0)
				length, lengthErr := generator.expr(function.Args[2], 0)
				if firstErr == nil && startErr == nil && lengthErr == nil {
					setFunctionName(function, "SUBSTRING")
					function.RawArgs = "(" + first + " FROM " + start + " FOR " + length + ")"
				}
			}
		}
	}
	if usesMySQLArrayFunctions(target) && name == "ARRAY_AGG" {
		setFunctionName(function, "GROUP_CONCAT")
	}
	return nil
}

func conditionalFunctionName(target Dialect) string {
	switch target {
	case DialectSnowflake:
		return "IFF"
	case DialectSQLite:
		return "IIF"
	default:
		return "IF"
	}
}

func rewriteTimeConversionFunction(function *FunctionCallExpr, target Dialect, name string) (Expr, bool) {
	if len(function.Args) == 0 {
		return nil, false
	}
	switch name {
	case "EPOCH":
		if len(function.Args) != 1 {
			return nil, false
		}
		mapped := ""
		switch target {
		case DialectBigQuery:
			mapped = "TIME_TO_UNIX"
		case DialectPresto:
			mapped = "TO_UNIXTIME"
		case DialectSpark:
			mapped = "UNIX_TIMESTAMP"
		}
		if mapped != "" {
			return &FunctionCallExpr{Name: []Identifier{{Text: mapped}}, Args: function.Args}, true
		}
	case "EPOCH_MS":
		if len(function.Args) != 1 {
			return nil, false
		}
		value := function.Args[0]
		divisor := &LiteralExpr{KindValue: LiteralNumber, Raw: "10"}
		power := &FunctionCallExpr{Name: []Identifier{{Text: "POWER"}}, Args: []Expr{divisor, &LiteralExpr{KindValue: LiteralNumber, Raw: "3"}}}
		switch target {
		case DialectMySQL:
			return &FunctionCallExpr{Name: []Identifier{{Text: "FROM_UNIXTIME"}}, Args: []Expr{&BinaryExpr{Left: value, Operator: "/", Right: power}}}, true
		case DialectPostgreSQL:
			return &FunctionCallExpr{Name: []Identifier{{Text: "TO_TIMESTAMP"}}, Args: []Expr{&BinaryExpr{
				Left:     &CastExpr{Keyword: "CAST", Value: value, Type: identifierExpr("DOUBLE PRECISION")},
				Operator: "/", Right: power,
			}}}, true
		case DialectPresto:
			power.Name[0].Text = "POW"
			return &FunctionCallExpr{Name: []Identifier{{Text: "FROM_UNIXTIME"}}, Args: []Expr{&BinaryExpr{
				Left:     &CastExpr{Keyword: "CAST", Value: value, Type: identifierExpr("DOUBLE")},
				Operator: "/", Right: power,
			}}}, true
		case DialectSpark, DialectBigQuery:
			return &FunctionCallExpr{Name: []Identifier{{Text: "TIMESTAMP_MILLIS"}}, Args: []Expr{value}}, true
		case DialectClickHouse:
			return &FunctionCallExpr{Name: []Identifier{{Text: "fromUnixTimestamp64Milli"}}, Args: []Expr{&CastExpr{
				Keyword: "CAST", Value: value, Type: &RawExpr{Raw: "Nullable(Int64)"},
			}}}, true
		}
	case "STRFTIME":
		if len(function.Args) != 2 {
			return nil, false
		}
		function.Args[1] = rewriteDateFormatExpr(function.Args[1], target)
		if target == DialectSpark {
			normalizeSparkTimestampCast(function.Args[0])
		}
		switch target {
		case DialectBigQuery:
			function.Name = []Identifier{{Text: "FORMAT_DATE"}}
			function.Args[0], function.Args[1] = function.Args[1], function.Args[0]
			return nil, true
		case DialectPostgreSQL:
			function.Name = []Identifier{{Text: "TO_CHAR"}}
			return nil, true
		case DialectPresto, DialectHive, DialectSpark:
			function.Name = []Identifier{{Text: "DATE_FORMAT"}}
			return nil, true
		case DialectTSQL:
			function.Name = []Identifier{{Text: "FORMAT"}}
			return nil, true
		}
	case "STRPTIME":
		if len(function.Args) != 2 {
			return nil, false
		}
		function.Args[1] = rewriteDateParseFormatExpr(function.Args[1], target)
		switch target {
		case DialectBigQuery:
			function.Name = []Identifier{{Text: "PARSE_TIMESTAMP"}}
			function.Args[0], function.Args[1] = function.Args[1], function.Args[0]
			return nil, true
		case DialectPresto:
			function.Name = []Identifier{{Text: "DATE_PARSE"}}
			return nil, true
		case DialectSpark:
			function.Name = []Identifier{{Text: "TO_TIMESTAMP"}}
			return nil, true
		case DialectHive:
			return &CastExpr{Keyword: "CAST", Value: &FunctionCallExpr{
				Name: []Identifier{{Text: "FROM_UNIXTIME"}},
				Args: []Expr{&FunctionCallExpr{Name: []Identifier{{Text: "UNIX_TIMESTAMP"}}, Args: function.Args}},
			}, Type: identifierExpr("TIMESTAMP")}, true
		}
	case "TO_TIMESTAMP":
		if len(function.Args) == 1 {
			switch target {
			case DialectBigQuery:
				setFunctionName(function, "TIMESTAMP_SECONDS")
				return nil, true
			case DialectPresto, DialectHive:
				setFunctionName(function, "FROM_UNIXTIME")
				return nil, true
			}
		}
	}
	return nil, false
}

func rewriteDateFormatExpr(expression Expr, target Dialect) Expr {
	switch value := expression.(type) {
	case *LiteralExpr:
		if value.KindValue == LiteralString {
			value.Raw = rewriteDateFormatLiteral(value.Raw, target)
		}
	case *FunctionCallExpr:
		for index := range value.Args {
			value.Args[index] = rewriteDateFormatExpr(value.Args[index], target)
		}
	}
	return expression
}

func renderDialectExpr(expression Expr, dialect Dialect) string {
	if expression == nil {
		return ""
	}
	if text, err := (generator{canonical: true, dialect: dialect}).expr(expression, 0); err == nil {
		return text
	}
	return renderExpr(expression)
}

func rewriteDateParseFormatExpr(expression Expr, target Dialect) Expr {
	switch value := expression.(type) {
	case *LiteralExpr:
		if value.KindValue == LiteralString && len(value.Raw) >= 2 && value.Raw[0] == '\'' && value.Raw[len(value.Raw)-1] == '\'' {
			content := value.Raw[1 : len(value.Raw)-1]
			switch target {
			case DialectBigQuery:
				content = strings.ReplaceAll(content, "%-d", "%e")
			case DialectSpark, DialectHive:
				content = strings.ReplaceAll(content, "%Y", "yyyy")
				content = strings.ReplaceAll(content, "%y", "yy")
				content = strings.ReplaceAll(content, "%-m", "M")
				content = strings.ReplaceAll(content, "%-d", "d")
				content = strings.ReplaceAll(content, "%-I", "h")
				content = strings.ReplaceAll(content, "%m", "MM")
				content = strings.ReplaceAll(content, "%d", "dd")
				content = strings.ReplaceAll(content, "%H", "HH")
				content = strings.ReplaceAll(content, "%M", "m")
				content = strings.ReplaceAll(content, "%S", "s")
				content = strings.ReplaceAll(content, "%p", "a")
			default:
				return rewriteDateFormatExpr(expression, target)
			}
			value.Raw = "'" + content + "'"
		}
	case *FunctionCallExpr:
		for index := range value.Args {
			value.Args[index] = rewriteDateParseFormatExpr(value.Args[index], target)
		}
	}
	return expression
}

func normalizeSparkTimestampCast(expression Expr) {
	switch value := expression.(type) {
	case *CastExpr:
		if typeName, ok := castTypeIdentifier(value.Type); ok && strings.EqualFold(typeName.Text, "TIMESTAMP") {
			value.Type = identifierExpr("TIMESTAMP_NTZ")
		}
		normalizeSparkTimestampCast(value.Value)
	case *FunctionCallExpr:
		for _, argument := range value.Args {
			normalizeSparkTimestampCast(argument)
		}
	}
}

func rewriteDateFormatLiteral(raw string, target Dialect) string {
	if len(raw) < 2 || raw[0] != '\'' || raw[len(raw)-1] != '\'' {
		return raw
	}
	content := raw[1 : len(raw)-1]
	switch target {
	case DialectBigQuery:
		content = strings.ReplaceAll(content, "%Y-%m-%d", "%F")
		content = strings.ReplaceAll(content, "%H:%M:%S", "%T")
	case DialectPostgreSQL:
		content = strings.ReplaceAll(content, "%-m", "FMMM")
		content = strings.ReplaceAll(content, "%-d", "FMDD")
		content = strings.ReplaceAll(content, "%Y", "YYYY")
		content = strings.ReplaceAll(content, "%y", "YY")
		content = strings.ReplaceAll(content, "%m", "MM")
		content = strings.ReplaceAll(content, "%d", "DD")
		content = strings.ReplaceAll(content, "%H", "HH24")
		content = strings.ReplaceAll(content, "%M", "MI")
		content = strings.ReplaceAll(content, "%S", "SS")
	case DialectPresto:
		content = strings.ReplaceAll(content, "%H:%M:%S", "%T")
		content = strings.ReplaceAll(content, "%-m", "%c")
		content = strings.ReplaceAll(content, "%-d", "%e")
		content = strings.ReplaceAll(content, "%-I", "%l")
		content = strings.ReplaceAll(content, "%M", "%i")
		content = strings.ReplaceAll(content, "%S", "%s")
	case DialectSpark, DialectHive, DialectTSQL:
		content = strings.ReplaceAll(content, "%Y", "yyyy")
		content = strings.ReplaceAll(content, "%y", "yy")
		content = strings.ReplaceAll(content, "%-m", "M")
		content = strings.ReplaceAll(content, "%-d", "d")
		content = strings.ReplaceAll(content, "%m", "MM")
		content = strings.ReplaceAll(content, "%d", "dd")
		content = strings.ReplaceAll(content, "%H", "HH")
		content = strings.ReplaceAll(content, "%M", "mm")
		content = strings.ReplaceAll(content, "%S", "ss")
		content = strings.ReplaceAll(content, "%p", "a")
	}
	return "'" + content + "'"
}

func rewritesNVL2(target Dialect) bool {
	switch target {
	case DialectBigQuery, DialectClickHouse, DialectDoris, DialectDremio,
		DialectDrill, DialectDuckDB, DialectHive, DialectMySQL,
		DialectPostgreSQL, DialectPresto, DialectSQLite, DialectStarRocks,
		DialectTrino, DialectTSQL:
		return true
	default:
		return false
	}
}

func rewriteDecodeFunction(function *FunctionCallExpr) Expr {
	if len(function.Args) < 3 {
		return function
	}
	base := function.Args[0]
	pairEnd := len(function.Args)
	var otherwise Expr
	if (len(function.Args)-1)%2 == 1 {
		otherwise = function.Args[pairEnd-1]
		pairEnd--
	}
	whens := make([]CaseWhen, 0, (pairEnd-1)/2)
	for index := 1; index+1 < pairEnd; index += 2 {
		search := function.Args[index]
		condition := Expr(&BinaryExpr{Left: base, Operator: "=", Right: search})
		switch search := search.(type) {
		case *LiteralExpr:
			if search.KindValue == LiteralNull {
				condition = &IsExpr{Value: base, Operator: "IS", Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}}
			}
		default:
			parenthesizedSearch := search
			if _, simple := search.(*IdentifierExpr); !simple {
				parenthesizedSearch = &ParenthesizedExpr{Expr: search}
			}
			condition = &BinaryExpr{Left: base, Operator: "=", Right: parenthesizedSearch}
			condition = &BinaryExpr{
				Left:     condition,
				Operator: "OR",
				Right: &ParenthesizedExpr{Expr: &BinaryExpr{
					Left:     &IsExpr{Value: base, Operator: "IS", Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}},
					Operator: "AND",
					Right:    &IsExpr{Value: parenthesizedSearch, Operator: "IS", Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}},
				}},
			}
		}
		whens = append(whens, CaseWhen{Condition: condition, Result: function.Args[index+1]})
	}
	return &CaseExpr{nodeBase: nodeBase{span: function.SourceSpan()}, Whens: whens, Else: otherwise}
}

func asciiCheckExpression(value Expr, target Dialect) Expr {
	text := renderExpr(value)
	switch target {
	case DialectSQLite:
		return &RawExpr{Raw: "(NOT " + text + " GLOB CAST(x'2a5b5e012d7f5d2a' AS TEXT))"}
	case DialectMySQL:
		return &RawExpr{Raw: "REGEXP_LIKE(" + text + ", '^[[:ascii:]]*$')"}
	case DialectPostgreSQL:
		return &RawExpr{Raw: "(" + text + " ~ '^[[:ascii:]]*$')"}
	case DialectTSQL:
		return &RawExpr{Raw: "(PATINDEX(CONVERT(VARCHAR(MAX), 0x255b5e002d7f5d25) COLLATE Latin1_General_BIN, " + text + ") = 0)"}
	case DialectOracle:
		return &RawExpr{Raw: "NVL(REGEXP_LIKE(" + text + ", '^[' || CHR(1) || '-' || CHR(127) || ']*$'), TRUE)"}
	default:
		return nil
	}
}

func canonicalizeFunctionName(function *FunctionCallExpr, target Dialect) {
	if len(function.Name) != 1 || function.Name[0].Quoted {
		return
	}
	// ClickHouse function names are conventionally mixed-case (for example
	// arrayJoin, toDateTime, and quantileState). Preserve the spelling parsed
	// from the source unless a target rewrite explicitly changed it.
	if target == DialectClickHouse {
		return
	}
	if target == DialectBigQuery && len(function.Name[0].Text) == 1 {
		character := function.Name[0].Text[0]
		if character >= 'a' && character <= 'z' {
			return
		}
	}
	name := strings.ToUpper(function.Name[0].Text)
	if target != DialectDataFusion {
		function.Name[0].Text = name
		return
	}
	if mapped, ok := map[string]string{
		"ABS": "ABS", "CEIL": "CEIL", "FLOOR": "FLOOR", "SQRT": "SQRT", "LN": "LN", "EXP": "EXP",
		"LENGTH": "LENGTH", "UPPER": "UPPER", "LOWER": "LOWER", "TRIM": "TRIM", "SUBSTR": "SUBSTRING",
		"STARTS_WITH": "STARTS_WITH", "ENDS_WITH": "ENDS_WITH", "STRPOS": "STRPOS", "ARRAY_LENGTH": "ARRAY_LENGTH",
		"ARRAY_POSITION": "ARRAY_POSITION", "ARRAY_DISTINCT": "ARRAY_DISTINCT", "UNNEST": "UNNEST", "RANDOM": "RANDOM", "PI": "PI",
		"INITCAP": "INITCAP", "CHAR_LENGTH": "LENGTH",
	}[name]; ok {
		function.Name[0].Text = mapped
		return
	}
	if name == "STRUCT" {
		function.Name[0].Text = "ROW"
		for i := range function.Args {
			if alias, ok := function.Args[i].(*AliasExpr); ok {
				function.Args[i] = alias.Expr
			}
		}
	}
}

func rewriteMySQLNullOrder(items []OrderItem) []OrderItem {
	if len(items) == 0 {
		return items
	}
	rewritten := make([]OrderItem, 0, len(items)*2)
	for _, item := range items {
		if !item.Descending && !item.NullsFirst {
			original := item
			original.NullsFirst = false
			original.NullsLast = false
			rank := item
			rank.Expr = &CaseExpr{
				Whens: []CaseWhen{{
					Condition: &IsExpr{
						Value:    original.Expr,
						Operator: "IS",
						Right:    &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"},
					},
					Result: &LiteralExpr{KindValue: LiteralNumber, Raw: "1"},
				}},
				Else: &LiteralExpr{KindValue: LiteralNumber, Raw: "0"},
			}
			rank.Ascending = false
			rank.Descending = false
			rank.NullsFirst = false
			rank.NullsLast = false
			rewritten = append(rewritten, rank, original)
			continue
		}
		rewritten = append(rewritten, item)
	}
	return rewritten
}

type structPackEntry struct {
	Key   string
	Value string
}

func structPackEntries(function *FunctionCallExpr) ([]structPackEntry, bool) {
	if len(function.Args) != 1 {
		return nil, false
	}
	tail := strings.TrimSpace(function.ArgumentTail)
	if !strings.HasPrefix(tail, ":=") {
		return nil, false
	}
	firstKey := strings.TrimSpace(renderExpr(function.Args[0]))
	if len(firstKey) >= 2 && ((firstKey[0] == '"' && firstKey[len(firstKey)-1] == '"') || (firstKey[0] == '`' && firstKey[len(firstKey)-1] == '`')) {
		firstKey = firstKey[1 : len(firstKey)-1]
	}
	firstValue := strings.TrimSpace(tail[2:])
	segments := splitTopLevelSQL(firstValue, ',')
	if len(segments) == 0 || strings.TrimSpace(segments[0]) == "" {
		return nil, false
	}
	entries := []structPackEntry{{Key: firstKey, Value: strings.TrimSpace(segments[0])}}
	for _, segment := range segments[1:] {
		key, value, ok := splitTopLevelSQLAssignment(segment)
		if !ok || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
			return nil, false
		}
		key = strings.TrimSpace(key)
		if len(key) >= 2 && ((key[0] == '"' && key[len(key)-1] == '"') || (key[0] == '`' && key[len(key)-1] == '`')) {
			key = key[1 : len(key)-1]
		}
		entries = append(entries, structPackEntry{Key: key, Value: strings.TrimSpace(value)})
	}
	return entries, true
}

func isBareIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || index > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func splitTopLevelSQLAssignment(text string) (string, string, bool) {
	depth := 0
	var quote byte
	for index := 0; index+1 < len(text); index++ {
		c := text[index]
		if quote != 0 {
			if c == quote {
				if index+1 < len(text) && text[index+1] == quote {
					index++
				} else {
					quote = 0
				}
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
		case '(', '[', '{', '<':
			depth++
		case ')', ']', '}', '>':
			if depth > 0 {
				depth--
			}
		}
		if depth == 0 && c == ':' && text[index+1] == '=' {
			return strings.TrimSpace(text[:index]), strings.TrimSpace(text[index+2:]), true
		}
	}
	return "", "", false
}

type duckDBMapEntry struct {
	Key   string
	Value string
}

func parseDuckDBMapLiteral(raw string) ([]duckDBMapEntry, bool) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, false
	}
	parts := splitTopLevelSQL(trimmed[1:len(trimmed)-1], ',')
	entries := make([]duckDBMapEntry, 0, len(parts))
	for _, part := range parts {
		pieces := splitTopLevelSQL(part, ':')
		if len(pieces) < 2 {
			return nil, false
		}
		key := strings.TrimSpace(pieces[0])
		if len(key) >= 2 && key[0] == '\'' && key[len(key)-1] == '\'' {
			key = strings.Trim(key, "'")
		}
		entries = append(entries, duckDBMapEntry{Key: key, Value: strings.TrimSpace(strings.Join(pieces[1:], ":"))})
	}
	return entries, len(entries) > 0
}

func prestoMapType(value string) string {
	trimmed := strings.TrimSpace(value)
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "CAST(") {
		if asIndex := strings.LastIndex(upper, " AS "); asIndex >= 0 {
			typeName := strings.TrimSpace(trimmed[asIndex+len(" AS "):])
			if strings.HasSuffix(typeName, ")") {
				typeName = strings.TrimSpace(typeName[:len(typeName)-1])
			}
			if strings.HasPrefix(strings.ToUpper(typeName), "ROW(") {
				return typeName
			}
		}
	}
	if strings.HasPrefix(upper, "ROW(") {
		return trimmed
	}
	if strings.HasPrefix(trimmed, "'") {
		return "VARCHAR"
	}
	if strings.EqualFold(trimmed, "TRUE") || strings.EqualFold(trimmed, "FALSE") {
		return "BOOLEAN"
	}
	return "INTEGER"
}

func stringLiteralValue(expression Expr) (string, bool) {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString || len(literal.Raw) < 2 {
		return "", false
	}
	return strings.Trim(literal.Raw, "'"), true
}

func isStringLiteral(expression Expr) bool {
	literal, ok := expression.(*LiteralExpr)
	return ok && literal.KindValue == LiteralString
}

func duckDBNotNullLambda() Expr {
	value := identifierExpr("_u")
	return &BinaryExpr{
		Left:     value,
		Operator: "->",
		Right:    &UnaryExpr{Operator: "NOT", Expr: &IsExpr{Value: identifierExpr("_u"), Operator: "IS", Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}}},
	}
}

func regexpSplitDelimiter(expression Expr) Expr {
	return &FunctionCallExpr{
		Name: []Identifier{{Text: "CONCAT"}},
		Args: []Expr{
			&LiteralExpr{KindValue: LiteralString, Raw: "'\\\\Q'"},
			expression,
			&LiteralExpr{KindValue: LiteralString, Raw: "'\\\\E'"},
		},
	}
}

func reverseSortLambda() Expr {
	a := identifierExpr("a")
	b := identifierExpr("b")
	return &BinaryExpr{
		Left: &ParenthesizedExpr{Expr: &TupleExpr{Items: []Expr{a, b}}}, Operator: "->",
		Right: &CaseExpr{Whens: []CaseWhen{
			{Condition: &BinaryExpr{Left: identifierExpr("a"), Operator: "<", Right: identifierExpr("b")}, Result: &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}},
			{Condition: &BinaryExpr{Left: identifierExpr("a"), Operator: ">", Right: identifierExpr("b")}, Result: &LiteralExpr{KindValue: LiteralNumber, Raw: "-1"}},
		}, Else: &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}},
	}
}

func rewriteTimeFunction(function *FunctionCallExpr, target Dialect, name string) bool {
	if len(function.Args) == 0 {
		return false
	}
	setName := func(value string) {
		setFunctionName(function, value)
	}
	switch target {
	case DialectDuckDB:
		switch name {
		case "STR_TO_TIME":
			setName("STRPTIME")
		case "STR_TO_UNIX":
			inner := &FunctionCallExpr{Name: []Identifier{{Text: "STRPTIME"}}, Args: function.Args}
			function.Name = []Identifier{{Text: "EPOCH"}}
			function.Args = []Expr{inner}
		case "TIME_TO_STR":
			setName("STRFTIME")
		case "TIME_TO_UNIX":
			setName("EPOCH")
		case "UNIX_TO_STR":
			inner := &FunctionCallExpr{Name: []Identifier{{Text: "TO_TIMESTAMP"}}, Args: []Expr{function.Args[0]}}
			function.Name = []Identifier{{Text: "STRFTIME"}}
			function.Args[0] = inner
		case "UNIX_TO_TIME":
			setName("TO_TIMESTAMP")
		default:
			return false
		}
		return true
	case DialectHive:
		switch name {
		case "STR_TO_TIME":
			common := len(function.Args) > 1 && isHiveCastTimeFormat(function.Args[1])
			if len(function.Args) > 1 {
				function.Args[1] = normalizeTimeFormat(function.Args[1], "hive")
			}
			if len(function.Args) > 1 && !common {
				function.Name = []Identifier{{Text: "CAST"}}
				function.RawArgs = "(FROM_UNIXTIME(UNIX_TIMESTAMP(" + renderArgs(function.Args) + ")) AS TIMESTAMP)"
			} else {
				function.Name = []Identifier{{Text: "CAST"}}
				function.RawArgs = "(" + renderExpr(function.Args[0]) + " AS TIMESTAMP)"
			}
		case "STR_TO_UNIX":
			if len(function.Args) > 1 {
				function.Args[1] = normalizeTimeFormat(function.Args[1], "hive")
			}
			setName("UNIX_TIMESTAMP")
			if len(function.Args) > 1 && isCommonHiveTimeFormat(function.Args[1]) {
				function.Args = function.Args[:1]
			}
		case "TIME_TO_STR":
			if len(function.Args) > 1 {
				function.Args[1] = normalizeOutputTimeFormat(function.Args[1], "hive")
			}
			setName("DATE_FORMAT")
		case "TIME_TO_UNIX":
			setName("UNIX_TIMESTAMP")
		case "UNIX_TO_STR", "UNIX_TO_TIME":
			setName("FROM_UNIXTIME")
			if name == "UNIX_TO_STR" && len(function.Args) > 1 && isCommonHiveTimeFormat(function.Args[1]) {
				function.Args = function.Args[:1]
			}
		case "TIME_STR_TO_DATE":
			setName("TO_DATE")
		default:
			return false
		}
		return true
	case DialectPresto, DialectTrino:
		switch name {
		case "STR_TO_TIME":
			if len(function.Args) > 1 {
				function.Args[1] = normalizeTimeFormat(function.Args[1], "presto")
			}
			setName("DATE_PARSE")
		case "STR_TO_UNIX":
			if len(function.Args) != 2 {
				return false
			}
			value, format := function.Args[0], function.Args[1]
			fallbackFormat := prestoFallbackTimeFormat(format)
			dateParse := &FunctionCallExpr{
				Name: []Identifier{{Text: "DATE_PARSE"}},
				Args: []Expr{&CastExpr{Keyword: "CAST", Value: value, Type: identifierExpr("VARCHAR")}, format},
			}
			fallback := &FunctionCallExpr{
				Name: []Identifier{{Text: "PARSE_DATETIME"}},
				Args: []Expr{
					&FunctionCallExpr{
						Name: []Identifier{{Text: "DATE_FORMAT"}},
						Args: []Expr{&CastExpr{Keyword: "CAST", Value: value, Type: identifierExpr("TIMESTAMP")}, format},
					},
					fallbackFormat,
				},
			}
			function.Name = []Identifier{{Text: "TO_UNIXTIME"}}
			function.Args = []Expr{&FunctionCallExpr{
				Name: []Identifier{{Text: "COALESCE"}},
				Args: []Expr{&FunctionCallExpr{Name: []Identifier{{Text: "TRY"}}, Args: []Expr{dateParse}}, fallback},
			}}
		case "TIME_TO_STR":
			setName("DATE_FORMAT")
		case "TIME_TO_UNIX":
			setName("TO_UNIXTIME")
		case "UNIX_TO_TIME":
			setName("FROM_UNIXTIME")
		case "UNIX_TO_STR":
			inner := &FunctionCallExpr{Name: []Identifier{{Text: "FROM_UNIXTIME"}}, Args: []Expr{function.Args[0]}}
			function.Name = []Identifier{{Text: "DATE_FORMAT"}}
			function.Args[0] = inner
		default:
			return false
		}
		return true
	case DialectSpark:
		switch name {
		case "STR_TO_TIME":
			if len(function.Args) > 1 {
				function.Args[1] = normalizeTimeFormat(function.Args[1], "spark")
			}
			setName("TO_TIMESTAMP")
		case "STR_TO_UNIX", "TIME_TO_UNIX":
			setName("UNIX_TIMESTAMP")
		case "TIME_TO_STR":
			setName("DATE_FORMAT")
		case "UNIX_TO_STR":
			setName("FROM_UNIXTIME")
		case "UNIX_TO_TIME":
			function.Name = []Identifier{{Text: "CAST"}}
			function.RawArgs = "(FROM_UNIXTIME(" + renderArgs(function.Args) + ") AS TIMESTAMP)"
		default:
			return false
		}
		return true
	case DialectBigQuery:
		if name == "TIME_TO_STR" && len(function.Args) == 2 {
			format := function.Args[1]
			if literal, ok := format.(*LiteralExpr); ok && literal.KindValue == LiteralString {
				copy := *literal
				copy.Raw = strings.ReplaceAll(copy.Raw, "%Y-%m-%d", "%F")
				format = &copy
			}
			function.Name = []Identifier{{Text: "FORMAT_DATE"}}
			function.Args = []Expr{format, function.Args[0]}
			return true
		}
	case DialectDrill:
		if name == "TIME_TO_STR" && len(function.Args) == 2 {
			function.Args[1] = normalizeOutputTimeFormat(function.Args[1], "drill")
			setName("TO_CHAR")
			return true
		}
		if name == "STR_TO_TIME" {
			if len(function.Args) > 1 {
				function.Args[1] = normalizeTimeFormat(function.Args[1], "drill")
			}
			setName("TO_TIMESTAMP")
			return true
		}
		if name == "TIME_TO_UNIX" {
			setName("UNIX_TIMESTAMP")
			return true
		}
	case DialectTSQL:
		if name == "TIME_TO_STR" && len(function.Args) == 2 {
			function.Args[1] = normalizeOutputTimeFormat(function.Args[1], "tsql")
			setName("FORMAT")
			return true
		}
	case DialectMySQL:
		if name == "STR_TO_TIME" {
			if len(function.Args) > 1 {
				function.Args[1] = normalizeTimeFormat(function.Args[1], "mysql")
			}
			setName("STR_TO_DATE")
			return true
		}
	case DialectRedshift, DialectOracle, DialectPostgreSQL, DialectMaterialize:
		if name == "TIME_TO_STR" && len(function.Args) == 2 {
			function.Args[1] = normalizeOutputTimeFormat(function.Args[1], "oracle")
			setName("TO_CHAR")
			return true
		}
		if name == "STR_TO_TIME" {
			if len(function.Args) > 1 {
				function.Args[1] = normalizeTimeFormat(function.Args[1], "oracle")
			}
			setName("TO_TIMESTAMP")
			return true
		}
	case DialectStarRocks, DialectDoris:
		switch name {
		case "STR_TO_UNIX":
			setName("UNIX_TIMESTAMP")
			return true
		case "TIME_TO_STR":
			if target == DialectDoris {
				setName("DATE_FORMAT")
			}
			return true
		case "TIME_TO_UNIX":
			setName("UNIX_TIMESTAMP")
			return true
		case "UNIX_TO_STR":
			setName("FROM_UNIXTIME")
			return true
		case "UNIX_TO_TIME":
			setName("FROM_UNIXTIME")
			return true
		}
	default:
		return false
	}
	return false
}

func isCommonHiveTimeFormat(expression Expr) bool {
	literal, ok := expression.(*LiteralExpr)
	if !ok {
		return false
	}
	return literal.KindValue == LiteralString && (literal.Raw == "'yyyy-MM-dd'" || literal.Raw == "'yyyy-MM-dd HH:mm:ss'")
}

func isHiveCastTimeFormat(expression Expr) bool {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString {
		return false
	}
	switch literal.Raw {
	case "'yyyy-MM-dd'", "'yyyy-MM-dd HH:mm:ss'", "'%Y-%m-%d'":
		return true
	default:
		return false
	}
}

func normalizeTimeFormat(expression Expr, style string) Expr {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString || len(literal.Raw) < 2 {
		return expression
	}
	value := strings.Trim(literal.Raw, "'")
	switch style {
	case "presto", "mysql":
		value = strings.ReplaceAll(value, "%H:%M:%S", "%T")
	case "spark", "hive":
		value = strings.ReplaceAll(value, "%Y", "yyyy")
		value = strings.ReplaceAll(value, "%y", "yy")
		value = strings.ReplaceAll(value, "%m", "M")
		value = strings.ReplaceAll(value, "%d", "d")
		value = strings.ReplaceAll(value, "%H", "H")
		value = strings.ReplaceAll(value, "%M", "m")
		value = strings.ReplaceAll(value, "%S", "s")
		if style == "spark" || style == "hive" {
			value = strings.ReplaceAll(value, "yyyy-MM-dd", "yyyy-M-d")
		}
	case "drill":
		value = strings.ReplaceAll(value, "%Y", "yyyy")
		value = strings.ReplaceAll(value, "%y", "yy")
		value = strings.ReplaceAll(value, "%m", "MM")
		value = strings.ReplaceAll(value, "%d", "dd")
		value = strings.ReplaceAll(value, "%H", "HH")
		value = strings.ReplaceAll(value, "%M", "mm")
		value = strings.ReplaceAll(value, "%S", "ss")
		value = strings.ReplaceAll(value, "T", "''T''")
	case "oracle":
		value = strings.ReplaceAll(value, "%Y", "YYYY")
		value = strings.ReplaceAll(value, "%y", "YY")
		value = strings.ReplaceAll(value, "%m", "MM")
		value = strings.ReplaceAll(value, "%d", "DD")
		value = strings.ReplaceAll(value, "%H", "HH24")
		value = strings.ReplaceAll(value, "%M", "MI")
		value = strings.ReplaceAll(value, "%S", "SS")
		value = strings.ReplaceAll(value, "%f", "US")
	}
	literal.Raw = "'" + value + "'"
	return literal
}

func normalizeGenericDateFormat(expression Expr, style string) Expr {
	var raw string
	switch value := expression.(type) {
	case *LiteralExpr:
		if value.KindValue != LiteralString {
			return normalizeTimeFormat(expression, style)
		}
		raw = strings.Trim(value.Raw, "'")
	case *IdentifierExpr:
		if len(value.Parts) != 1 || !value.Parts[0].Quoted {
			return expression
		}
		raw = value.Parts[0].Text
	default:
		return normalizeTimeFormat(expression, style)
	}
	javaStyle := strings.Contains(raw, "yyyy") || strings.Contains(raw, "MM") || strings.Contains(raw, "dd")
	if javaStyle {
		switch style {
		case "generic", "duckdb":
			raw = strings.ReplaceAll(raw, "yyyy", "%Y")
			raw = strings.ReplaceAll(raw, "MM", "%m")
			raw = strings.ReplaceAll(raw, "dd", "%d")
			raw = strings.ReplaceAll(raw, "HH", "%H")
			raw = strings.ReplaceAll(raw, "mm", "%M")
			raw = strings.ReplaceAll(raw, "ss", "%S")
		case "presto":
			raw = strings.ReplaceAll(raw, "yyyy", "%Y")
			raw = strings.ReplaceAll(raw, "MM", "%m")
			raw = strings.ReplaceAll(raw, "dd", "%d")
			raw = strings.ReplaceAll(raw, "HH", "%H")
			raw = strings.ReplaceAll(raw, "mm", "%i")
			raw = strings.ReplaceAll(raw, "ss", "%s")
		case "hive":
			raw = strings.ReplaceAll(raw, "yyyy", "yyyy")
			raw = strings.ReplaceAll(raw, "MM", "MM")
			raw = strings.ReplaceAll(raw, "dd", "dd")
			raw = strings.ReplaceAll(raw, "HH", "HH")
		}
	}
	if strings.Contains(raw, "'") && style != "hive" {
		raw = strings.ReplaceAll(raw, "'", "''")
	}
	return &LiteralExpr{KindValue: LiteralString, Raw: "'" + raw + "'"}
}

func normalizeOutputTimeFormat(expression Expr, style string) Expr {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString || len(literal.Raw) < 2 {
		return expression
	}
	value := strings.Trim(literal.Raw, "'")
	switch style {
	case "hive", "drill":
		value = strings.ReplaceAll(value, "%Y", "yyyy")
		value = strings.ReplaceAll(value, "%m", "MM")
		value = strings.ReplaceAll(value, "%d", "dd")
		value = strings.ReplaceAll(value, "%H", "HH")
		value = strings.ReplaceAll(value, "%M", "mm")
		value = strings.ReplaceAll(value, "%S", "ss")
		value = strings.ReplaceAll(value, "%f", "ffffff")
	case "oracle":
		value = strings.ReplaceAll(value, "%Y", "YYYY")
		value = strings.ReplaceAll(value, "%m", "MM")
		value = strings.ReplaceAll(value, "%d", "DD")
		value = strings.ReplaceAll(value, "%H", "HH24")
		value = strings.ReplaceAll(value, "%M", "MI")
		value = strings.ReplaceAll(value, "%S", "SS")
		value = strings.ReplaceAll(value, "%f", "US")
	case "tsql":
		value = strings.ReplaceAll(value, "%Y", "yyyy")
		value = strings.ReplaceAll(value, "%m", "MM")
		value = strings.ReplaceAll(value, "%d", "dd")
		value = strings.ReplaceAll(value, "%H", "HH")
		value = strings.ReplaceAll(value, "%M", "mm")
		value = strings.ReplaceAll(value, "%S", "ss")
		value = strings.ReplaceAll(value, "%f", "ffffff")
	}
	literal.Raw = "'" + value + "'"
	return literal
}

func normalizeExasolOutputTimeFormat(expression Expr) Expr {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString || len(literal.Raw) < 2 {
		return expression
	}
	value := strings.Trim(literal.Raw, "'")
	for _, replacement := range []struct{ from, to string }{
		{"%Y", "YYYY"}, {"%y", "YY"}, {"%m", "MM"}, {"%d", "DD"},
		{"%H", "HH"}, {"%M", "MI"}, {"%S", "SS"}, {"%a", "DY"}, {"%b", "MON"},
	} {
		value = strings.ReplaceAll(value, replacement.from, replacement.to)
	}
	literal.Raw = "'" + value + "'"
	return literal
}

func normalizeDayFormat(expression Expr, style string) Expr {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString {
		return expression
	}
	copy := *literal
	switch style {
	case "presto":
		copy.Raw = strings.ReplaceAll(copy.Raw, "%-d", "%e")
	default:
		copy.Raw = strings.ReplaceAll(copy.Raw, "%-d", "d")
	}
	return &copy
}

func prestoFallbackTimeFormat(expression Expr) Expr {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString {
		return expression
	}
	copy := *literal
	copy.Raw = strings.ReplaceAll(copy.Raw, "%Y-%m-%d", "yyyy-MM-dd")
	return &copy
}

func renderExpr(expression Expr) string {
	text, err := (generator{canonical: true, dialect: DialectGeneric}).expr(expression, 0)
	if err != nil {
		return ""
	}
	return text
}

func renderArgs(args []Expr) string {
	var b strings.Builder
	for i, arg := range args {
		if i > 0 {
			b.WriteString(", ")
		}
		text, err := (generator{canonical: true, dialect: DialectGeneric}).expr(arg, 0)
		if err == nil {
			b.WriteString(text)
		}
	}
	return b.String()
}

func renderOrderItemsCompact(items []OrderItem) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		part := renderExpr(item.Expr)
		if item.Descending {
			part += " DESC"
		} else if item.Ascending {
			part += " ASC"
		}
		if item.NullsLast {
			part += " NULLS LAST"
		} else if item.NullsFirst {
			part += " NULLS FIRST"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

func identifierExpr(name string) *IdentifierExpr {
	return &IdentifierExpr{Parts: []Identifier{{Text: name}}}
}

func isTrueLiteral(expression Expr) bool {
	literal, ok := expression.(*LiteralExpr)
	return ok && literal.KindValue == LiteralBoolean && strings.EqualFold(literal.Raw, "TRUE")
}

func isFalseLiteral(expression Expr) bool {
	literal, ok := expression.(*LiteralExpr)
	return ok && literal.KindValue == LiteralBoolean && strings.EqualFold(literal.Raw, "FALSE")
}

func foldNumericExpr(expression Expr) (string, bool) {
	binary, ok := expression.(*BinaryExpr)
	if !ok || binary.Operator != "+" {
		return "", false
	}
	left, leftOK := numericLiteral(binary.Left)
	right, rightOK := numericLiteral(binary.Right)
	if !leftOK || !rightOK {
		return "", false
	}
	return strconv.Itoa(left + right), true
}

func numericLiteral(expression Expr) (int, bool) {
	if unary, ok := expression.(*UnaryExpr); ok && (unary.Operator == "-" || unary.Operator == "+") {
		value, ok := numericLiteral(unary.Expr)
		if !ok {
			return 0, false
		}
		if unary.Operator == "-" {
			return -value, true
		}
		return value, true
	}
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralNumber {
		return 0, false
	}
	value, err := strconv.Atoi(literal.Raw)
	return value, err == nil
}

func numericLiteralExpr(expression Expr) bool {
	_, ok := numericLiteral(expression)
	return ok
}

func isPrestoBigint(expression Expr) bool {
	if _, ok := numericLiteral(expression); ok {
		return true
	}
	if function, ok := expression.(*FunctionCallExpr); ok && len(function.Name) == 1 && len(function.Args) == 1 {
		name := strings.ToUpper(function.Name[0].Text)
		if name == "FLOOR" || name == "CEIL" {
			_, ok := numericLiteral(function.Args[0])
			return ok
		}
	}
	cast, ok := expression.(*CastExpr)
	if !ok {
		return false
	}
	typeName, ok := castTypeIdentifier(cast.Type)
	return ok && strings.EqualFold(typeName.Text, "BIGINT")
}

func allNumericArguments(expressions []Expr) bool {
	if len(expressions) == 0 {
		return false
	}
	for _, expression := range expressions {
		if _, ok := numericLiteral(expression); !ok {
			return false
		}
	}
	return true
}

func isNumericRaw(expression Expr, wanted string) bool {
	literal, ok := expression.(*LiteralExpr)
	return ok && literal.KindValue == LiteralNumber && literal.Raw == wanted
}

func isIdentifierNamed(expression Expr, wanted string) bool {
	identifier, ok := expression.(*IdentifierExpr)
	return ok && len(identifier.Parts) == 1 && strings.EqualFold(identifier.Parts[0].Text, wanted)
}

func unquoteDatePart(expression Expr) Expr {
	part := ""
	quoted := false
	switch value := expression.(type) {
	case *LiteralExpr:
		if value.KindValue == LiteralString && len(value.Raw) >= 2 {
			part = strings.Trim(value.Raw, "'")
		}
	case *IdentifierExpr:
		if len(value.Parts) == 1 {
			part = value.Parts[0].Text
			quoted = value.Parts[0].Quoted
		}
	}
	if part == "" {
		return expression
	}
	trimmed := strings.Trim(part, "[]\"`")
	switch strings.ToUpper(trimmed) {
	case "D", "DD", "DY":
		return identifierExpr("DAY")
	case "M", "MM", "MON":
		return identifierExpr("MONTH")
	case "Q", "QQ":
		return identifierExpr("QUARTER")
	case "H", "HH":
		return identifierExpr("HOUR")
	case "N", "MI":
		return identifierExpr("MINUTE")
	case "S", "SS":
		return identifierExpr("SECOND")
	case "DAY", "MONTH", "QUARTER", "HOUR", "MINUTE", "SECOND", "YEAR":
		if quoted {
			return identifierExpr(strings.ToUpper(trimmed))
		}
	}
	return identifierExpr(trimmed)
}

func booleanToTSQL(expression Expr) Expr {
	return &CastExpr{
		Keyword: "CAST",
		Value: &CaseExpr{
			Whens: []CaseWhen{{Condition: expression, Result: &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}}},
			Else:  &LiteralExpr{KindValue: LiteralNumber, Raw: "0"},
		},
		Type: identifierExpr("BIT"),
	}
}

func booleanOperandTSQL(expression Expr) Expr {
	switch expression := expression.(type) {
	case *LiteralExpr:
		switch expression.KindValue {
		case LiteralBoolean:
			value := "0"
			if strings.EqualFold(expression.Raw, "TRUE") {
				value = "1"
			}
			return &ParenthesizedExpr{Expr: &BinaryExpr{
				Left:     &LiteralExpr{KindValue: LiteralNumber, Raw: "1"},
				Operator: "=",
				Right:    &LiteralExpr{KindValue: LiteralNumber, Raw: value},
			}}
		case LiteralNumber:
			return &BinaryExpr{Left: expression, Operator: "<>", Right: &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}}
		}
	case *IdentifierExpr:
		return &BinaryExpr{Left: expression, Operator: "<>", Right: &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}}
	case *CastExpr:
		return &BinaryExpr{Left: expression, Operator: "<>", Right: &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}}
	}
	return expression
}

func ilikeTSQL(left, right Expr) Expr {
	like := &BinaryExpr{Left: left, Operator: "LIKE", Right: right}
	notLike := &UnaryExpr{Operator: "NOT", Expr: &BinaryExpr{Left: left, Operator: "LIKE", Right: right}}
	return &CastExpr{
		Keyword: "CAST",
		Value: &CaseExpr{
			Whens: []CaseWhen{
				{Condition: like, Result: &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}},
				{Condition: notLike, Result: &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}},
			},
			Else: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"},
		},
		Type: identifierExpr("BIT"),
	}
}

func booleanAggregateTSQL(name string, value Expr) Expr {
	nonZero := &BinaryExpr{Left: value, Operator: "<>", Right: &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}}
	return &CastExpr{
		Keyword: "CAST",
		Value: &FunctionCallExpr{
			Name: []Identifier{{Text: name}},
			Args: []Expr{&CaseExpr{
				Whens: []CaseWhen{
					{Condition: nonZero, Result: &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}},
					{Condition: &UnaryExpr{Operator: "NOT", Expr: nonZero}, Result: &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}},
				},
				Else: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"},
			}},
		},
		Type: identifierExpr("BIT"),
	}
}

func startsWithTSQL(value, prefix Expr) Expr {
	left := &FunctionCallExpr{Name: []Identifier{{Text: "LEFT"}}, Args: []Expr{
		value,
		&FunctionCallExpr{Name: []Identifier{{Text: "LEN"}}, Args: []Expr{prefix}},
	}}
	match := &BinaryExpr{Left: left, Operator: "=", Right: prefix}
	notMatch := &UnaryExpr{Operator: "NOT", Expr: &BinaryExpr{Left: left, Operator: "=", Right: prefix}}
	return &CastExpr{
		Keyword: "CAST",
		Value: &CaseExpr{
			Whens: []CaseWhen{
				{Condition: match, Result: &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}},
				{Condition: notMatch, Result: &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}},
			},
			Else: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"},
		},
		Type: identifierExpr("BIT"),
	}
}

func rewriteCastType(expression *CastExpr, target Dialect) {
	typeExpression := expression.Type
	arrayDepth := 0
	for {
		index, ok := typeExpression.(*IndexExpr)
		if !ok || index.Slice || len(index.Indices) != 0 || index.Low != nil || index.High != nil || index.Step != nil {
			break
		}
		arrayDepth++
		typeExpression = index.Target
	}
	name, ok := castTypeIdentifier(typeExpression)
	if !ok {
		return
	}
	arrayType := strings.TrimSpace(name.Text)
	for strings.HasSuffix(arrayType, "[]") {
		arrayDepth++
		arrayType = strings.TrimSpace(strings.TrimSuffix(arrayType, "[]"))
	}
	if arrayDepth > 0 {
		if target != DialectPresto && target != DialectHive && target != DialectSpark && target != DialectDatabricks && target != DialectSnowflake {
			base := strings.ToUpper(arrayType)
			if _, simple := typeExpression.(*IdentifierExpr); !simple {
				if rendered := renderDialectExpr(typeExpression, target); rendered != "" {
					base = rendered
				}
			}
			expression.Type = &RawExpr{Raw: base + strings.Repeat("[]", arrayDepth)}
			return
		}
		base := strings.ToUpper(arrayType)
		for index := 0; index < arrayDepth; index++ {
			if target == DialectHive || target == DialectSpark || target == DialectDatabricks {
				base = "ARRAY<" + base + ">"
			} else {
				base = "ARRAY(" + base + ")"
			}
		}
		expression.Type = &RawExpr{Raw: base}
		return
	}
	upper := strings.ToUpper(name.Text)
	mapped := name.Text
	switch target {
	case DialectGeneric:
		switch upper {
		case "BOOLEAN", "BOOL", "INT", "BIGINT", "SMALLINT", "TINYINT",
			"FLOAT", "DOUBLE", "REAL", "DECIMAL", "VARCHAR", "CHAR", "TEXT",
			"DATE", "TIME", "TIMESTAMP", "TIMESTAMPTZ", "VARBINARY", "BINARY":
			mapped = upper
		case "DOUBLE PRECISION":
			mapped = "DOUBLE"
		case "NUMBER":
			mapped = "DECIMAL"
		case "INTEGER":
			mapped = "INT"
		case "NUMERIC":
			mapped = "DECIMAL"
		case "STRING":
			mapped = "TEXT"
		case "CHARACTER VARYING":
			mapped = "VARCHAR"
		}
		if upper == "DOUBLE" && hasIdentifierSuffix(expression.TypeSuffix, "PRECISION") {
			mapped = "DOUBLE"
			expression.TypeSuffix = nil
		}
		if upper == "TIMESTAMP" && len(expression.TypeSuffix) > 0 {
			if hasIdentifierSuffix(expression.TypeSuffix, "WITH") && hasIdentifierSuffix(expression.TypeSuffix, "TIME") && hasIdentifierSuffix(expression.TypeSuffix, "ZONE") {
				mapped = "TIMESTAMPTZ"
				expression.TypeSuffix = nil
			} else if hasIdentifierSuffix(expression.TypeSuffix, "WITHOUT") && hasIdentifierSuffix(expression.TypeSuffix, "TIME") && hasIdentifierSuffix(expression.TypeSuffix, "ZONE") {
				mapped = "TIMESTAMP"
				expression.TypeSuffix = nil
			}
		}
	case DialectPostgreSQL:
		if upper == "DOUBLE" || upper == "FLOAT" {
			mapped = "DOUBLE PRECISION"
		}
		if upper == "TINYINT" {
			mapped = "SMALLINT"
		}
		if upper == "DATETIME" {
			mapped = "TIMESTAMP"
		}
		if upper == "STRING" || upper == "TEXT" {
			mapped = "TEXT"
		}
		if upper == "CHARACTER VARYING" {
			mapped = "VARCHAR"
		}
		if upper == "BINARY" || upper == "VARBINARY" {
			mapped = "BYTEA"
		}
		if upper == "VARCHAR" && !expression.preserveTypeParameters {
			if call, ok := expression.Type.(*CallExpr); ok && len(call.Args) > 0 {
				expression.Type = &RawExpr{Raw: "TEXT"}
			}
		}
		if (upper == "DECIMAL" || upper == "NUMERIC") && len(expression.TypeSuffix) == 0 {
			if call, ok := expression.Type.(*CallExpr); !ok || len(call.Args) == 0 {
				expression.Type = &RawExpr{Raw: "DECIMAL(18, 3)"}
			}
		}
	case DialectMySQL:
		switch upper {
		case "BOOLEAN", "BOOL", "INT", "INTEGER", "BIGINT", "TINYINT":
			mapped = "SIGNED"
		case "VARCHAR", "TEXT", "STRING":
			mapped = "CHAR"
		case "TIMESTAMP":
			mapped = "DATETIME"
		case "SMALLINT":
			mapped = "SIGNED"
		case "CHARACTER VARYING":
			mapped = "CHAR"
		}
	case DialectDuckDB:
		switch upper {
		case "DECFLOAT":
			expression.Type = &RawExpr{Raw: "DECIMAL(38, 5)"}
			return
		case "BOOLEAN", "BOOL", "INT", "BIGINT", "SMALLINT", "TINYINT", "DOUBLE", "REAL", "DECIMAL", "TEXT", "DATE", "TIME", "TIMESTAMP", "TIMESTAMPTZ", "UUID":
			mapped = upper
		case "TIMESTAMP_US":
			mapped = "TIMESTAMP"
		case "INT64":
			mapped = "BIGINT"
		case "INT32", "INT4", "INTEGER", "SIGNED":
			mapped = "INT"
		case "INT16":
			mapped = "SMALLINT"
		case "INT8":
			mapped = "BIGINT"
		case "NUMERIC":
			mapped = "DECIMAL"
		case "NUMBER":
			if call, ok := expression.Type.(*CallExpr); ok && len(call.Args) > 0 {
				mapped = "DECIMAL"
			} else {
				mapped = "DECIMAL(38, 0)"
			}
		case "HUGEINT":
			mapped = "INT128"
		case "UHUGEINT":
			mapped = "UINT128"
		case "CHAR", "BPCHAR":
			mapped = "TEXT"
		case "INT1":
			mapped = "TINYINT"
		case "FLOAT4":
			mapped = "REAL"
		case "BYTEA":
			mapped = "BLOB"
		case "LOGICAL":
			mapped = "BOOLEAN"
		case "JSON":
			mapped = "JSON"
		case "ROW":
			mapped = "STRUCT"
		case "VARCHAR", "STRING":
			mapped = "TEXT"
		case "BINARY", "VARBINARY":
			mapped = "BLOB"
		case "BYTES":
			mapped = "BLOB"
		case "CHARACTER VARYING":
			mapped = "TEXT"
		case "FLOAT":
			mapped = "REAL"
		}
		if upper == "DECIMAL" || upper == "NUMERIC" {
			if call, ok := expression.Type.(*CallExpr); !ok || len(call.Args) == 0 {
				expression.Type = &RawExpr{Raw: "DECIMAL(18, 3)"}
			}
		}
		if upper == "TIMESTAMP" {
			if hasIdentifierSuffix(expression.TypeSuffix, "WITH") && hasIdentifierSuffix(expression.TypeSuffix, "TIME") && hasIdentifierSuffix(expression.TypeSuffix, "ZONE") {
				mapped = "TIMESTAMPTZ"
			}
			expression.TypeSuffix = nil
		}
	case DialectBigQuery:
		switch upper {
		case "UUID", "CHAR", "NCHAR", "NVARCHAR", "VARCHAR", "TEXT", "CHARACTER VARYING":
			mapped = "STRING"
		case "BINARY", "VARBINARY":
			mapped = "BYTES"
		case "SMALLINT", "INTEGER", "INT", "TINYINT":
			mapped = "INT64"
		case "DOUBLE", "REAL":
			mapped = "FLOAT64"
		case "TIMESTAMPTZ":
			mapped = "TIMESTAMP"
		case "RECORD":
			mapped = "STRUCT"
		case "BYTEINT":
			mapped = "INT64"
		}
	case DialectSpark:
		switch upper {
		case "INT64":
			mapped = "BIGINT"
		case "FLOAT":
			if call, ok := expression.Type.(*CallExpr); ok && len(call.Args) == 1 && isNumericRaw(call.Args[0], "64") {
				mapped = "DOUBLE"
			}
		case "REAL":
			mapped = "FLOAT"
		case "MONEY":
			expression.Type = &RawExpr{Raw: "DECIMAL(15, 4)"}
			return
		case "SMALLMONEY":
			expression.Type = &RawExpr{Raw: "DECIMAL(6, 4)"}
			return
		case "NCHAR":
			mapped = "CHAR"
		case "NVARCHAR":
			mapped = "VARCHAR"
		case "UNIQUEIDENTIFIER":
			mapped = "STRING"
		case "TIME", "DATETIME", "DATETIME2", "DATETIMEOFFSET":
			mapped = "TIMESTAMP"
		case "BIT":
			mapped = "BOOLEAN"
		case "BYTES":
			mapped = "BINARY"
		case "NUMERIC":
			mapped = "DECIMAL"
		case "VARCHAR", "CHAR":
			if _, isCall := expression.Type.(*CallExpr); isCall {
				mapped = upper
			} else {
				mapped = "STRING"
			}
		case "TEXT":
			mapped = "STRING"
		case "CHARACTER VARYING":
			if _, isCall := expression.Type.(*CallExpr); isCall {
				mapped = "VARCHAR"
			} else {
				mapped = "STRING"
			}
		case "VARBINARY":
			mapped = "BINARY"
		}
		if (upper == "FLOAT" && mapped == "DOUBLE") || upper == "TIME" || upper == "DATETIME" || upper == "DATETIME2" || upper == "DATETIMEOFFSET" {
			expression.Type = identifierExpr(mapped)
			return
		}
	case DialectHive:
		switch upper {
		case "DATETIME", "DATETIME2", "TIMESTAMP_NTZ", "TIMESTAMP_LTZ":
			expression.Type = identifierExpr("TIMESTAMP")
			return
		case "ROWVERSION":
			expression.Type = identifierExpr("BINARY")
			return
		case "FLOAT":
			if call, ok := expression.Type.(*CallExpr); ok && len(call.Args) == 1 {
				if precision, ok := numericLiteral(call.Args[0]); ok && precision > 32 {
					expression.Type = identifierExpr("DOUBLE")
					return
				}
				expression.Type = identifierExpr("FLOAT")
				return
			}
		case "INT64":
			mapped = "BIGINT"
		case "BYTES":
			mapped = "BINARY"
		case "NUMERIC":
			mapped = "DECIMAL"
		case "VARCHAR", "CHAR":
			if _, isCall := expression.Type.(*CallExpr); isCall {
				mapped = upper
			} else {
				mapped = "STRING"
			}
		case "TEXT":
			mapped = "STRING"
		case "CHARACTER VARYING":
			if _, isCall := expression.Type.(*CallExpr); isCall {
				mapped = "VARCHAR"
			} else {
				mapped = "STRING"
			}
		case "VARBINARY":
			mapped = "BINARY"
		}
	case DialectPresto, DialectTrino, DialectDrill:
		switch upper {
		case "TEXT", "STRING":
			mapped = "VARCHAR"
		case "BINARY", "BYTES":
			mapped = "VARBINARY"
		case "INT64":
			mapped = "BIGINT"
		case "NUMERIC":
			mapped = "DECIMAL"
		case "DATETIME":
			mapped = "TIMESTAMP"
		case "CHARACTER VARYING":
			mapped = "VARCHAR"
		}
		if target == DialectDrill && upper == "SMALLINT" {
			mapped = "INTEGER"
		}
	case DialectDoris, DialectStarRocks:
		switch upper {
		case "TEXT":
			mapped = "STRING"
		case "CHARACTER VARYING":
			mapped = "VARCHAR"
		case "TIMESTAMP":
			mapped = "DATETIME"
		case "TIMESTAMPTZ":
			mapped = "DATETIME"
		}
	case DialectOracle:
		switch upper {
		case "TEXT", "STRING":
			mapped = "CLOB"
		case "VARCHAR":
			mapped = "VARCHAR2"
		case "BINARY", "VARBINARY":
			mapped = "BLOB"
		case "DOUBLE":
			mapped = "DOUBLE PRECISION"
		case "CHARACTER VARYING":
			mapped = "VARCHAR2"
		case "TINYINT":
			mapped = "SMALLINT"
		case "BIGINT":
			mapped = "INT"
		case "DECIMAL":
			mapped = "NUMBER"
		}
	case DialectRedshift:
		switch upper {
		case "TEXT", "STRING":
			mapped = "VARCHAR(MAX)"
		case "BINARY", "VARBINARY":
			mapped = "VARBYTE"
		case "DOUBLE":
			mapped = "DOUBLE PRECISION"
		case "TIMESTAMPTZ":
			mapped = "TIMESTAMP WITH TIME ZONE"
		case "CHARACTER VARYING":
			mapped = "VARCHAR"
		}
	case DialectSQLite:
		switch upper {
		case "BINARY", "VARBINARY":
			mapped = "BLOB"
		case "SMALLINT":
			mapped = "INTEGER"
		}
	case DialectMaterialize:
		switch upper {
		case "STRING":
			mapped = "TEXT"
		case "BINARY", "VARBINARY":
			mapped = "BYTEA"
		case "DOUBLE":
			mapped = "DOUBLE PRECISION"
		case "CHARACTER VARYING":
			mapped = "VARCHAR"
		}
	case DialectTSQL:
		switch upper {
		case "BOOLEAN", "BOOL":
			mapped = "BIT"
		case "INT":
			mapped = "INTEGER"
		case "DOUBLE":
			mapped = "FLOAT"
		case "TEXT":
			mapped = "VARCHAR(MAX)"
		case "TIMESTAMP":
			mapped = "DATETIME2"
		case "DECIMAL":
			mapped = "NUMERIC"
		case "STRING":
			mapped = "VARCHAR(MAX)"
		case "CHARACTER VARYING":
			mapped = "VARCHAR"
		case "VARCHAR", "CHAR", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "UTINYINT", "FLOAT", "REAL", "MONEY", "SMALLMONEY", "UNIQUEIDENTIFIER", "XML", "IMAGE", "SQL_VARIANT", "BIT", "DATE", "TIME", "DATETIME2", "ROWVERSION", "NUMERIC":
			mapped = upper
			if upper == "REAL" {
				mapped = "FLOAT"
			}
			if upper == "UTINYINT" {
				mapped = "TINYINT"
			}
		}
	case DialectSnowflake:
		switch upper {
		case "BOOLEAN", "BOOL", "INT", "INTEGER", "BIGINT", "SMALLINT", "TINYINT", "FLOAT", "DOUBLE", "REAL", "DECIMAL", "NUMBER", "DATE", "TIME", "TIMESTAMP", "TIMESTAMPTZ", "VARIANT", "GEOGRAPHY", "GEOMETRY":
			mapped = upper
		case "TIMESTAMP_NTZ":
			mapped = "TIMESTAMPNTZ"
		case "TIMESTAMP_LTZ":
			mapped = "TIMESTAMPLTZ"
		case "NVARCHAR", "NCHAR", "STRING", "TEXT":
			mapped = "VARCHAR"
		case "BYTEINT":
			mapped = "INT"
		case "CHARACTER VARYING":
			mapped = "VARCHAR"
		case "JSON":
			mapped = "VARIANT"
		}
		if (upper == "DECIMAL" || upper == "NUMERIC") && len(expression.TypeSuffix) == 0 {
			if call, ok := expression.Type.(*CallExpr); !ok || len(call.Args) == 0 {
				expression.Type = &RawExpr{Raw: "DECIMAL(18, 3)"}
			}
		}
	case DialectDataFusion:
		switch upper {
		case "INTEGER":
			mapped = "INT"
		case "NUMERIC":
			mapped = "DECIMAL"
		case "BYTEA":
			mapped = "VARBINARY"
		case "DATETIME2":
			mapped = "TIMESTAMP"
		case "NVARCHAR":
			mapped = "VARCHAR"
		}
	case DialectDremio:
		switch upper {
		case "SMALLINT", "TINYINT":
			mapped = "INT"
		case "BINARY":
			mapped = "VARBINARY"
		case "TEXT", "NCHAR", "CHAR":
			mapped = "VARCHAR"
		case "TIMESTAMPNTZ", "DATETIME":
			mapped = "TIMESTAMP"
		case "ARRAY":
			mapped = "LIST"
		case "BIT":
			mapped = "BOOLEAN"
		}
	}
	if call, isCall := expression.Type.(*CallExpr); isCall {
		if target == DialectDuckDB && upper == "ROW" && len(call.Args) == 1 {
			if raw, ok := call.Args[0].(*RawExpr); ok {
				raw.Raw = normalizeDuckDBStructuredTypeRaw(raw.Raw)
			}
		}
		if target == DialectDuckDB && !expression.preserveTypeParameters && (upper == "VARCHAR" || upper == "CHAR" || upper == "CHARACTER VARYING" || upper == "NVARCHAR" || upper == "STRING") {
			expression.Type = identifierExpr(mapped)
		}
		if target == DialectBigQuery || ((target == DialectSpark || target == DialectHive) && mapped == "STRING") || (target == DialectSnowflake && len(call.Args) == 0) {
			expression.Type = identifierExpr(mapped)
		}
	}
	name.Text = mapped
	name.Quoted = false
	if strings.EqualFold(expression.Keyword, "TRY_CAST") && (target == DialectGeneric || target == DialectPostgreSQL || target == DialectMySQL) {
		expression.Keyword = "CAST"
	}
}

func normalizeDuckDBStructuredTypeRaw(raw string) string {
	text := canonicalRawSQL(raw)
	for _, typeName := range []string{"INTEGER", "INT64", "INT32", "INT16", "INT8"} {
		mapped := "INT"
		switch typeName {
		case "INT64":
			mapped = "BIGINT"
		case "INT16":
			mapped = "SMALLINT"
		case "INT8":
			mapped = "BIGINT"
		}
		text = replaceAllFold(text, " "+typeName, " "+mapped)
	}
	return text
}

func normalizeDuckDBListValueRaw(raw string) string {
	text := raw
	for {
		upper := strings.ToUpper(text)
		index := strings.Index(upper, "LIST_VALUE(")
		if index < 0 {
			return text
		}
		open := index + len("LIST_VALUE")
		close := matchingParenIndex(text, open)
		if close < 0 {
			return text
		}
		text = text[:index] + "[" + text[open+1:close] + "]" + text[close+1:]
	}
}

func hasIdentifierSuffix(values []Identifier, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value.Text, wanted) {
			return true
		}
	}
	return false
}

func castTypeIdentifier(expression Expr) (*Identifier, bool) {
	switch expression := expression.(type) {
	case *IdentifierExpr:
		if len(expression.Parts) == 1 {
			return &expression.Parts[0], true
		}
	case *CallExpr:
		return castTypeIdentifier(expression.Callee)
	case *IndexExpr:
		return castTypeIdentifier(expression.Target)
	}
	return nil, false
}

func setFunctionName(function *FunctionCallExpr, name string) {
	function.Name[0].Text = name
	function.Name[0].Quoted = false
}

func usesCoalesce(dialect Dialect) bool {
	switch dialect {
	case DialectPostgreSQL, DialectDuckDB, DialectSQLite, DialectTSQL,
		DialectPresto, DialectTrino, DialectAthena, DialectMaterialize,
		DialectRisingWave, DialectRedshift, DialectCockroachDB:
		return true
	default:
		return false
	}
}

func usesIfNull(dialect Dialect) bool {
	switch dialect {
	case DialectMySQL, DialectDoris, DialectStarRocks, DialectTiDB:
		return true
	default:
		return false
	}
}

func usesMySQLArrayFunctions(dialect Dialect) bool {
	return usesIfNull(dialect)
}
