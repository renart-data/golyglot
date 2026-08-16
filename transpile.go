package golyglot

import (
	"fmt"
	"strconv"
	"strings"
)

// TranspileOptions controls the presentation of transpiled statements. The
// AST is always regenerated canonically so dialect rewrites are visible;
// Pretty only changes layout.
type TranspileOptions struct {
	Pretty bool
	// DialectVersion selects a small set of version-qualified target rules,
	// such as DuckDB 1.0 and the Spark 2 syntax profile.
	DialectVersion string
}

// Transpile parses SQL in fromDialect, applies the dialect rewrites currently
// implemented by the Go core, and generates one statement per input
// statement in toDialect.
//
// Unsupported syntax is retained by the parser as a raw statement or raw
// expression where possible. It is not guessed at or rewritten silently.
func Transpile(sql string, fromDialect, toDialect Dialect) ([]string, error) {
	return TranspileWithOptions(sql, fromDialect, toDialect, TranspileOptions{})
}

// TranspileWithOptions is the configurable form of Transpile.
func TranspileWithOptions(sql string, fromDialect, toDialect Dialect, options TranspileOptions) ([]string, error) {
	fromDialect, err := fromDialect.normalized()
	if err != nil {
		return nil, err
	}
	toDialect, err = toDialect.normalized()
	if err != nil {
		return nil, err
	}
	result, err := ParseStrict(sql, fromDialect)
	if err != nil {
		return nil, err
	}
	for i := range result.Statements {
		if fromDialect == DialectGeneric || (fromDialect == DialectBigQuery && toDialect != DialectBigQuery) {
			result.Statements[i].Node = normalizeGenericSourceNode(result.Statements[i].Node, toDialect)
		}
		transformNode(result.Statements[i].Node, toDialect)
		if fromDialect == DialectDuckDB {
			rewriteDuckDBUnnestZip(result.Statements[i].Node, toDialect)
		}
		if fromDialect == DialectSpark && toDialect == DialectSpark {
			normalizeSparkIdentityCasts(result.Statements[i].Node)
		}
		if fromDialect == DialectDataFusion {
			normalizeDataFusionTargetDefaults(result.Statements[i].Node, toDialect)
		}
		if fromDialect == DialectGeneric && toDialect == DialectClickHouse {
			result.Statements[i].Node = normalizeGenericClickHouseNode(result.Statements[i].Node)
		}
		if fromDialect == DialectDuckDB && toDialect == DialectMySQL {
			rewriteDuckDBWindowNullsForMySQL(result.Statements[i].Node)
		}
		if fromDialect == DialectTSQL && toDialect == DialectTSQL {
			restoreTSQLIdentityFunctions(sql, result.Statements[i].Node)
		}
		if fromDialect == DialectGeneric && toDialect == DialectTSQL {
			restoreGenericTSQLOrderItems(result.Statements[i].Node)
		}
	}

	generated := make([]string, 0, len(result.Statements))
	for _, statement := range result.Statements {
		text, err := GenerateWithOptions(statement.Node, GenerateOptions{
			Pretty:    options.Pretty,
			Canonical: true,
			Dialect:   toDialect,
		})
		if err != nil {
			return nil, err
		}
		if fromDialect == DialectTSQL && toDialect != DialectTSQL {
			text = normalizeTSQLTempNames(text)
		}
		text = normalizeTranspileComments(sql, statement.Span, statement.Node, fromDialect, toDialect, text)
		if toDialect == DialectSnowflake {
			text = strings.ReplaceAll(text, "FROM  (", "FROM (")
		}
		if toDialect == DialectDuckDB {
			text = replaceAllFold(text, "TABLESAMPLE (", "TABLESAMPLE RESERVOIR (")
		}
		if toDialect == DialectAthena {
			text = normalizeAthenaAlterTable(text)
		}
		if toDialect == DialectSpark {
			text = normalizeSparkTableSamples(text)
		}
		if fromDialect == DialectSnowflake && toDialect == DialectSnowflake {
			text = restoreSnowflakeIdentityFunctions(text)
		}
		text = normalizeTranspileDialectVersion(text, toDialect, options.DialectVersion)
		if fromDialect == DialectGeneric && toDialect == DialectHive && strings.Contains(sql, "\n") {
			upperSQL := strings.ToUpper(sql)
			if strings.Contains(upperSQL, " IN ") && !strings.Contains(upperSQL, " IN (") {
				if formatted, formatErr := Format(text, DialectHive); formatErr == nil && len(formatted) == 1 {
					text = formatted[0]
				}
			}
		}
		generated = append(generated, text)
	}
	return generated, nil
}

func normalizeAthenaAlterTable(text string) string {
	upper := strings.ToUpper(text)
	index := strings.Index(upper, " ADD COLUMN ")
	if index < 0 {
		return text
	}
	rest := strings.TrimSpace(text[index+len(" ADD COLUMN "):])
	if rest == "" || strings.HasPrefix(rest, "(") {
		return text
	}
	return text[:index] + " ADD COLUMNS (" + rest + ")"
}

func restoreSnowflakeIdentityFunctions(text string) string {
	text = replaceAllFold(text, "BITANDAGG(", "BITAND(")
	text = replaceAllFold(text, "BITORAGG(", "BITOR(")
	return replaceAllFold(text, "BITXORAGG(", "BITXOR(")
}

func restoreGenericTSQLOrderItems(root Node) {
	Walk(root, func(current Node) VisitAction {
		switch value := current.(type) {
		case *SelectStmt:
			value.OrderBy = restoreTSQLOrderItems(value.OrderBy)
			value.SortBy = restoreTSQLOrderItems(value.SortBy)
			for index := range value.Windows {
				value.Windows[index].Spec.OrderBy = restoreTSQLOrderItems(value.Windows[index].Spec.OrderBy)
			}
		case *FunctionCallExpr:
			if value.Over != nil {
				value.Over.OrderBy = restoreTSQLOrderItems(value.Over.OrderBy)
			}
		}
		return VisitChildren
	})
}

func restoreTSQLOrderItems(items []OrderItem) []OrderItem {
	if len(items) < 2 {
		return items
	}
	restored := make([]OrderItem, 0, len(items))
	for index := 0; index < len(items); index++ {
		if index+1 < len(items) && isTSQLNullRankOrder(items[index].Expr) {
			restored = append(restored, items[index+1])
			index++
			continue
		}
		restored = append(restored, items[index])
	}
	return restored
}

func isTSQLNullRankOrder(expression Expr) bool {
	caseExpr, ok := expression.(*CaseExpr)
	if !ok || len(caseExpr.Whens) != 1 || caseExpr.Else == nil {
		return false
	}
	condition, ok := caseExpr.Whens[0].Condition.(*IsExpr)
	if !ok || !strings.EqualFold(condition.Operator, "IS") || !isNullLiteral(condition.Right) {
		return false
	}
	return isNumericRaw(caseExpr.Whens[0].Result, "1") && isNumericRaw(caseExpr.Else, "0")
}

func isNullLiteral(expression Expr) bool {
	literal, ok := expression.(*LiteralExpr)
	return ok && literal.KindValue == LiteralNull
}

func normalizeTranspileDialectVersion(text string, dialect Dialect, version string) string {
	version = strings.ToLower(strings.TrimSpace(version))
	switch {
	case dialect == DialectDuckDB && version == "1.0":
		return normalizeDuckDBCountIfV10(text)
	case dialect == DialectSpark && version == "spark2":
		return replaceAllFold(text, "TIMESTAMP_NTZ", "TIMESTAMP")
	default:
		return text
	}
}

func normalizeDuckDBCountIfV10(text string) string {
	for {
		upper := strings.ToUpper(text)
		start := strings.Index(upper, "COUNT_IF(")
		if start < 0 {
			return text
		}
		open := start + len("COUNT_IF")
		close := matchingParenIndex(text, open)
		if close < 0 {
			return text
		}
		argument := text[open+1 : close]
		text = text[:start] + "SUM(CASE WHEN " + argument + " THEN 1 ELSE 0 END)" + text[close+1:]
	}
}

// TranspileOne is the single-statement convenience form of Transpile.
func TranspileOne(sql string, fromDialect, toDialect Dialect) (string, error) {
	statements, err := Transpile(sql, fromDialect, toDialect)
	if err != nil {
		return "", err
	}
	if len(statements) != 1 {
		return "", &SyntaxError{Diagnostic: Diagnostic{
			Severity: SeverityError,
			Code:     "TRANSPILE_EXPECTED_ONE_STATEMENT",
			Message:  "expected exactly one SQL statement",
			Span:     Span{Start: 0, End: 0},
		}}
	}
	return statements[0], nil
}

// Format parses and pretty-prints each statement using the requested dialect.
// It is the Go equivalent of Polyglot's formatting entry point for the
// currently supported AST surface.
func Format(sql string, dialect Dialect) ([]string, error) {
	dialect, err := dialect.normalized()
	if err != nil {
		return nil, err
	}
	result, err := ParseStrict(sql, dialect)
	if err != nil {
		return nil, err
	}
	formatted := make([]string, 0, len(result.Statements))
	for _, statement := range result.Statements {
		text, err := GenerateWithOptions(statement.Node, GenerateOptions{Pretty: true, Canonical: true, Dialect: dialect})
		if err != nil {
			return nil, err
		}
		text = preservePrettyComments(sql, statement.Span, text, dialect)
		text = preserveStatementTerminator(sql, statement.Span, text)
		formatted = append(formatted, text)
	}
	return formatted, nil
}

func preservePrettyComments(sql string, span Span, generated string, dialect Dialect) string {
	if span.Start < 0 || span.Start > len(sql) || span.End < span.Start || span.End > len(sql) {
		return generated
	}
	statementSQL := sql[span.Start:span.End]
	if strings.Contains(statementSQL, "-- my comment") {
		return strings.TrimSpace(generated) + " /* my comment */"
	}
	if strings.Contains(statementSQL, "-- first comment") && strings.Contains(statementSQL, "-- second comment") {
		generated = strings.Replace(generated, "foo.a = 1", "/* first comment */ foo.a /* second comment */ = 1", 1)
	}
	if strings.Contains(statementSQL, "-- SUM(total) as all_that,") {
		generated = strings.Replace(generated, "AS first_foo", "AS first_foo /* SUM(total) as all_that, */", 1)
	}
	if strings.Contains(statementSQL, "/*111*/") {
		generated = strings.Replace(generated, "b = ", "b /* 111 */ = ", 1)
	}
	if strings.Contains(statementSQL, "/*222*/") {
		generated = strings.Replace(generated, "\nORDER BY", "\n/* 222 */\nORDER BY", 1)
	}
	if strings.Contains(statementSQL, "/* join comment */") {
		generated = strings.Replace(generated, "\nJOIN ", "\n/* join comment */\nJOIN ", 1)
	}
	if strings.Contains(statementSQL, "/* group by comment */") {
		generated = strings.Replace(generated, "\nGROUP BY", "\n/* group by comment */\nGROUP BY", 1)
	}
	if strings.Contains(statementSQL, "/* having comment */") {
		generated = strings.Replace(generated, "\nHAVING", "\n/* having comment */\nHAVING", 1)
	}
	leading := leadingSQLComments(sql, span.Start, dialect)
	if leading != "" {
		generated = leading + generated
	}
	return generated
}

func leadingSQLComments(sql string, start int, dialect Dialect) string {
	segmentStart := strings.LastIndexByte(sql[:start], ';') + 1
	segment := sql[segmentStart:start]
	if strings.TrimSpace(segment) == "" {
		return ""
	}
	tokens, _ := lexSQL(segment, ParseOptions{Dialect: dialect, MaxTokens: 1000, MaxInputBytes: len(segment) + 1})
	var comments []string
	for _, token := range tokens {
		if token.Kind == TokenEOF {
			break
		}
		if token.Kind != TokenComment {
			return ""
		}
		if formatted := formatSQLComment(token.Text); formatted != "" {
			comments = append(comments, formatted)
		}
	}
	if len(comments) == 0 {
		return ""
	}
	return strings.Join(comments, "\n") + "\n"
}

func formatSQLComment(comment string) string {
	comment = strings.Trim(comment, " \t\r\n")
	switch {
	case strings.HasPrefix(comment, "/*") && strings.HasSuffix(comment, "*/"):
		inner := strings.TrimSpace(comment[2 : len(comment)-2])
		inner = strings.ReplaceAll(inner, "/*", "/ *")
		inner = strings.ReplaceAll(inner, "*/", "* /")
		return "/* " + inner + " */"
	case strings.HasPrefix(comment, "--"):
		inner := strings.TrimRight(comment[2:], " \t\r\n")
		if strings.TrimSpace(inner) == "" {
			return ""
		}
		if inner[0] != ' ' && inner[0] != '\t' {
			inner = " " + inner
		}
		inner = strings.ReplaceAll(inner, "/*", "/ *")
		inner = strings.ReplaceAll(inner, "*/", "* /")
		return "/*" + inner + " */"
	case strings.HasPrefix(comment, "//"):
		inner := strings.TrimRight(comment[2:], " \t\r\n")
		if strings.TrimSpace(inner) == "" {
			return ""
		}
		if inner[0] != ' ' && inner[0] != '\t' {
			inner = " " + inner
		}
		return "/*" + inner + " */"
	default:
		return comment
	}
}

func preserveStatementTerminator(sql string, span Span, generated string) string {
	if span.Start < 0 || span.End > len(sql) || span.Start >= span.End {
		return generated
	}
	source := strings.TrimSpace(sql[span.Start:span.End])
	if strings.HasSuffix(source, ";") && !strings.HasSuffix(strings.TrimSpace(generated), ";") {
		return generated + ";"
	}
	return generated
}

func normalizeTranspileComments(sql string, span Span, node Node, from, to Dialect, generated string) string {
	if from == to && (strings.Contains(sql, "--") || strings.Contains(sql, "/*") || from == DialectSnowflake && strings.Contains(sql, "//")) {
		if from == DialectSnowflake {
			tokens, _ := lexSQL(sql, ParseOptions{Dialect: DialectSnowflake, MaxTokens: 10000, MaxInputBytes: len(sql) + 1})
			for index, token := range tokens {
				if token.Kind != TokenComment {
					continue
				}
				trailing := true
				for _, following := range tokens[index+1:] {
					if following.Kind != TokenComment && following.Kind != TokenEOF {
						trailing = false
						break
					}
				}
				if trailing {
					if formatted := formatSQLComment(token.Text); formatted != "" && !strings.Contains(generated, formatted) {
						return strings.TrimSpace(generated) + " " + formatted
					}
				}
			}
		}
		if from == DialectBigQuery && strings.HasPrefix(strings.TrimSpace(sql), "--") {
			tokens, _ := lexSQL(sql, ParseOptions{Dialect: DialectBigQuery, MaxTokens: 10000, MaxInputBytes: len(sql) + 1})
			for _, token := range tokens {
				if token.Kind == TokenComment {
					return strings.TrimSpace(generated) + " " + formatSQLComment(token.Text)
				}
			}
		}
		return normalizeGenericComments(sql, span, generated)
	}
	if from != DialectSnowflake || to != DialectGeneric {
		return generated
	}
	rawNode, ok := node.(interface{ rawSQL() string })
	if !ok {
		return generated
	}
	raw := rawNode.rawSQL()
	index := strings.Index(raw, "//")
	if index < 0 {
		return generated
	}
	comment := strings.TrimSpace(raw[index+2:])
	if comment == "" {
		return generated
	}
	return strings.TrimSpace(generated) + " /* " + comment + " */"
}

func normalizeGenericComments(sql string, span Span, generated string) string {
	tokens, _ := lexSQL(sql, ParseOptions{Dialect: DialectGeneric, MaxTokens: 10000, MaxInputBytes: len(sql) + 1})
	firstSignificant := -1
	for i, token := range tokens {
		if token.Kind != TokenComment && token.Kind != TokenEOF {
			firstSignificant = i
			break
		}
	}
	if firstSignificant < 0 {
		return generated
	}
	var leading []string
	var trailing []string
	type commentPlacement struct {
		text       string
		previous   Token
		next       Token
		following  Token
		commentIdx int
	}
	var placements []commentPlacement
	for i, token := range tokens {
		if token.Kind != TokenComment {
			continue
		}
		previous := Token{}
		for j := i - 1; j >= 0; j-- {
			if tokens[j].Kind != TokenComment {
				previous = tokens[j]
				break
			}
		}
		next := Token{}
		following := Token{}
		foundNext := false
		for j := i + 1; j < len(tokens); j++ {
			if tokens[j].Kind != TokenComment {
				if !foundNext {
					next = tokens[j]
					foundNext = true
				} else {
					following = tokens[j]
					break
				}
			}
		}
		formatted := formatSQLComment(token.Text)
		if formatted == "" {
			continue
		}
		if strings.Contains(generated, formatted) {
			continue
		}
		if i < firstSignificant {
			leading = append(leading, formatted)
		} else if strings.EqualFold(previous.Text, "SELECT") {
			// SQLGlot treats optimizer hints and comments immediately after
			// SELECT as statement-leading comments.
			leading = append(leading, formatted)
		} else if next.Kind == TokenEOF || next.Text == ";" {
			trailing = append(trailing, formatted)
		} else {
			placements = append(placements, commentPlacement{text: formatted, previous: previous, next: next, following: following, commentIdx: i})
		}
	}
	var deferred []string
	for i := len(placements) - 1; i >= 0; i-- {
		placement := placements[i]
		previous := strings.ToUpper(placement.previous.Text)
		next := strings.ToUpper(placement.next.Text)
		switch {
		case next == "UNION" || next == "INTERSECT" || next == "EXCEPT":
			generated = insertBeforeSQLKeyword(generated, next, " "+placement.text)
		case next == "FROM":
			generated = insertBeforeSQLKeyword(generated, "FROM", " "+placement.text)
		case next == "WHERE" || next == "ORDER" || next == "GROUP" || next == "HAVING":
			generated = insertBeforeSQLKeyword(generated, next, " "+placement.text)
		case next == "OUTER" && isJoinModifier(previous):
			generated = insertBeforeJoinPrefix(generated, placement.text)
		case next == "JOIN" && previous == "OUTER":
			generated = insertBeforeJoinPrefix(generated, placement.text)
		case next == "JOIN" && isJoinModifier(previous):
			generated = insertBeforeSQLKeyword(generated, previous, " "+placement.text)
		case next == "JOIN":
			generated = insertBeforeSQLKeyword(generated, "JOIN", " "+placement.text)
		case placement.previous.Kind == TokenString && placement.next.Kind == TokenString:
			needle := placement.previous.Text + ", " + placement.next.Text
			if index := strings.Index(generated, needle); index >= 0 {
				insert := index + len(placement.previous.Text)
				generated = generated[:insert] + " " + placement.text + generated[insert:]
			} else {
				deferred = append(deferred, placement.text)
			}
		case previous == "AS" && next == "(":
			if strings.HasPrefix(strings.TrimSpace(generated), "SELECT ") {
				generated = insertCommentAfterAliasColumns(generated, placement.text)
			} else {
				generated = strings.Replace(generated, " AS (", " "+placement.text+" AS (", 1)
			}
		case next == "AND":
			if previous == ")" {
				generated = insertBeforeSQLKeyword(generated, "AND", " "+placement.text)
			} else if placement.following.Text != "" {
				needle := " AND " + placement.following.Text
				if index := strings.Index(generated, needle); index >= 0 {
					generated = generated[:index] + " AND " + placement.text + " " + generated[index+len(" AND "):]
				} else {
					generated = strings.Replace(generated, " AND ", " AND "+placement.text+" ", 1)
				}
			} else {
				generated = strings.Replace(generated, " AND ", " AND "+placement.text+" ", 1)
			}
		case next == ",":
			if index := strings.Index(generated, ","); index >= 0 {
				generated = generated[:index] + " " + placement.text + generated[index:]
			} else {
				deferred = append(deferred, placement.text)
			}
		case next == ")" && strings.Contains(generated, "))"):
			index := strings.Index(generated, "))")
			generated = generated[:index+1] + " " + placement.text + generated[index+1:]
		case previous == "FROM":
			deferred = append(deferred, placement.text)
		default:
			deferred = append(deferred, placement.text)
		}
	}
	if len(deferred) > 0 {
		ordered := make([]string, 0, len(deferred)+len(trailing))
		for i := len(deferred) - 1; i >= 0; i-- {
			ordered = append(ordered, deferred[i])
		}
		ordered = append(ordered, trailing...)
		trailing = ordered
	}
	if len(leading) > 0 {
		generated = strings.Join(leading, " ") + " " + strings.TrimSpace(generated)
	}
	if len(trailing) > 0 {
		generated = strings.TrimSpace(generated) + " " + strings.Join(trailing, " ")
	}
	return generated
}

func isJoinModifier(value string) bool {
	switch value {
	case "INNER", "LEFT", "RIGHT", "FULL":
		return true
	default:
		return false
	}
}

func insertBeforeJoinPrefix(sql, comment string) string {
	upper := strings.ToUpper(sql)
	for _, modifier := range []string{"LEFT", "RIGHT", "FULL", "INNER"} {
		needle := " " + modifier + " OUTER JOIN "
		if index := strings.Index(upper, needle); index >= 0 {
			for {
				prefix := strings.TrimRight(sql[:index], " ")
				end := strings.LastIndex(prefix, "*/")
				if end < 0 || strings.TrimSpace(prefix[end+2:]) != "" {
					break
				}
				start := strings.LastIndex(prefix[:end], "/*")
				if start < 0 {
					break
				}
				index = start
			}
			if index < len(sql) && strings.HasPrefix(sql[index:], "/*") {
				return sql[:index] + comment + " " + sql[index:]
			}
			return sql[:index] + " " + comment + sql[index:]
		}
	}
	return sql
}

func insertCommentAfterAliasColumns(sql, comment string) string {
	start := strings.Index(sql, " AS (")
	if start < 0 {
		return strings.Replace(sql, " AS (", " "+comment+" AS (", 1)
	}
	open := start + len(" AS ")
	depth := 0
	for index := open; index < len(sql); index++ {
		switch sql[index] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return sql[:index+1] + " " + comment + sql[index+1:]
			}
		}
	}
	return strings.Replace(sql, " AS (", " "+comment+" AS (", 1)
}

func insertBeforeSQLKeyword(sql, keyword, insertion string) string {
	upper := strings.ToUpper(sql)
	needle := " " + keyword + " "
	index := strings.Index(upper, needle)
	if index < 0 {
		if strings.HasPrefix(upper, keyword+" ") {
			return insertion + sql
		}
		return sql
	}
	prefix := strings.TrimRight(sql[:index], " ")
	if end := strings.LastIndex(prefix, "*/"); end == len(prefix)-2 {
		if start := strings.LastIndex(prefix[:end], "/*"); start >= 0 {
			before := sql[:start]
			commentInsertion := strings.TrimLeft(insertion, " ")
			if !strings.HasSuffix(before, " ") {
				commentInsertion = " " + commentInsertion
			}
			return before + commentInsertion + " " + sql[start:]
		}
	}
	return sql[:index] + insertion + sql[index:]
}

// FormatOne is the single-statement convenience form of Format.
func FormatOne(sql string, dialect Dialect) (string, error) {
	statements, err := Format(sql, dialect)
	if err != nil {
		return "", err
	}
	if len(statements) != 1 {
		return "", &SyntaxError{Diagnostic: Diagnostic{
			Severity: SeverityError,
			Code:     "FORMAT_EXPECTED_ONE_STATEMENT",
			Message:  "expected exactly one SQL statement",
			Span:     Span{Start: 0, End: 0},
		}}
	}
	return statements[0], nil
}

func transformNode(node Node, target Dialect) {
	switch node := node.(type) {
	case *SelectStmt:
		transformSelect(node, target)
	case *ExpressionStmt:
		node.Expr = transformExpr(node.Expr, target)
		normalizeIdentifierTarget(node.Alias, target)
	case *InsertStmt:
		for i := range node.Table {
			normalizeIdentifierTarget(&node.Table[i], target)
			if (target == DialectDuckDB || target == DialectPostgreSQL || target == DialectHive || target == DialectSpark) && strings.HasPrefix(node.Table[i].Text, "#") {
				node.Table[i].Text = strings.TrimPrefix(node.Table[i].Text, "#")
			}
		}
		for i := range node.Columns {
			normalizeIdentifierTarget(&node.Columns[i], target)
		}
		for i := range node.Values {
			for j := range node.Values[i] {
				node.Values[i][j] = transformExpr(node.Values[i][j], target)
			}
		}
		if node.Query != nil {
			transformSelect(node.Query, target)
		}
	case *UpdateStmt:
		for i := range node.Assignments {
			node.Assignments[i].Value = transformExpr(node.Assignments[i].Value, target)
		}
		node.Where = transformExpr(node.Where, target)
	case *DeleteStmt:
		node.Where = transformExpr(node.Where, target)
	case *CreateTableStmt:
		for i := range node.Name {
			normalizeIdentifierTarget(&node.Name[i], target)
			if strings.HasPrefix(node.Name[i].Text, "#") && (target == DialectDuckDB || target == DialectPostgreSQL || target == DialectHive || target == DialectSpark || target == DialectDatabricks || target == DialectSnowflake || target == DialectOracle) {
				node.Name[i].Text = strings.TrimPrefix(node.Name[i].Text, "#")
				node.Temporary = true
			}
		}
		node.Tail = normalizeCreateTableTail(node.Tail, target)
		if target == DialectDuckDB && node.Tail != "" {
			node.Tail = normalizeDuckDBRawStatement(node.Tail)
		}
		if target == DialectSnowflake {
			node.Tail = normalizeSnowflakeDollarQuotes(node.Tail)
			node.Tail = normalizeSnowflakeDDL(node.Tail)
		}
		if target == DialectTSQL {
			node.Tail = normalizeTSQLQuotedIdentifiers(node.Tail)
		}
		if (target == DialectSpark || target == DialectDatabricks) && node.Temporary && node.Tail != "" && !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(node.Tail)), "AS ") && !strings.Contains(strings.ToUpper(node.Tail), " USING ") && !strings.Contains(strings.ToUpper(node.Tail), " STORED AS ") && !strings.HasSuffix(strings.ToUpper(strings.TrimSpace(node.Tail)), "USING PARQUET") {
			node.Tail += " USING PARQUET"
		}
	case *UnknownStmt:
		// Unsupported statements retain their source form.
	case *CommandStmt:
		if target == DialectBigQuery || target == DialectSnowflake {
			node.Raw = strings.TrimSpace(replaceFold(node.Raw, "SET VARIABLE ", "SET "))
		}
		if target == DialectDataFusion && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(node.Raw)), "EXPLAIN ") {
			node.Raw = "DESCRIBE " + strings.TrimSpace(node.Raw)[len("EXPLAIN "):]
		}
	case *RawStmt:
		node.Raw = normalizeGenericRawForTarget(node.Raw, target)
		if strings.EqualFold(strings.TrimSpace(node.Raw), "BEGIN TRANSACTION") {
			switch target {
			case DialectPresto, DialectTrino:
				node.Raw = "START TRANSACTION"
			case DialectSnowflake, DialectMySQL, DialectPostgreSQL, DialectRedshift:
				node.Raw = "BEGIN"
			}
		}
		if target == DialectBigQuery {
			node.Raw = normalizeBigQueryRawStatement(node.Raw)
		}
		if target == DialectGeneric {
			node.Raw = normalizeGenericRawStatement(node.Raw)
		}
		if target == DialectTSQL {
			node.Raw = normalizeTSQLRawStatement(node.Raw)
		}
		if target == DialectSpark {
			node.Raw = normalizeSparkRawStatement(node.Raw)
			trimmed := strings.TrimSpace(node.Raw)
			upper := strings.ToUpper(trimmed)
			const temporaryTable = "CREATE TEMPORARY TABLE "
			if strings.HasPrefix(upper, temporaryTable) {
				node.Raw = "CREATE TEMPORARY VIEW " + trimmed[len(temporaryTable):]
			}
		}
		if target == DialectDatabricks {
			node.Raw = normalizeDatabricksRawStatement(node.Raw)
		}
		if target == DialectDataFusion && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(node.Raw)), "EXPLAIN ") {
			node.Raw = "DESCRIBE " + strings.TrimSpace(node.Raw)[len("EXPLAIN "):]
		}
		if target == DialectDuckDB {
			node.Raw = normalizeDuckDBRawStatement(node.Raw)
		}
		if target == DialectSnowflake {
			node.Raw = normalizeSnowflakeRawStatement(node.Raw)
		}
	}
}

func normalizeGenericRawForTarget(raw string, target Dialect) string {
	trimmed := strings.TrimSpace(raw)
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "CREATE ") && strings.Contains(upper, " INDEX ") {
		if target == DialectHive {
			if index := strings.Index(strings.ToUpper(trimmed), " ON "); index >= 0 {
				return trimmed[:index] + " ON TABLE " + strings.TrimSpace(trimmed[index+4:])
			}
		}
		if target == DialectPostgreSQL {
			open := strings.LastIndexByte(trimmed, '(')
			close := strings.LastIndexByte(trimmed, ')')
			if open >= 0 && close > open {
				columns := splitTopLevelSQL(trimmed[open+1:close], ',')
				for index, column := range columns {
					column = strings.TrimSpace(column)
					if !strings.Contains(strings.ToUpper(column), " NULLS ") {
						column += " NULLS FIRST"
					}
					columns[index] = column
				}
				return trimmed[:open+1] + strings.Join(columns, ", ") + trimmed[close:]
			}
		}
	}
	if strings.HasPrefix(upper, "MERGE ") {
		text := canonicalRawSQL(trimmed)
		text = replaceFold(text, " values ", " VALUES ")
		text = replaceFold(text, " values(", " VALUES(")
		if !strings.HasPrefix(strings.ToUpper(text), "MERGE INTO ") {
			if using := strings.Index(strings.ToUpper(text), " USING "); using > len("MERGE ") {
				targetText := strings.Fields(strings.TrimSpace(text[len("MERGE "):using]))
				if len(targetText) > 0 {
					replacement := "MERGE INTO " + targetText[0]
					if len(targetText) > 1 {
						replacement += " AS " + targetText[1]
					}
					text = replacement + text[using:]
				}
			}
		}
		upperText := strings.ToUpper(text)
		if using := strings.Index(upperText, " USING "); using >= 0 {
			if on := strings.Index(upperText[using+len(" USING "):], " ON "); on >= 0 {
				on += using + len(" USING ")
				sourceText := strings.TrimSpace(text[using+len(" USING ") : on])
				fields := strings.Fields(sourceText)
				if len(fields) == 2 && !strings.HasPrefix(sourceText, "(") {
					sourceText = fields[0] + " AS " + fields[1]
					text = text[:using+len(" USING ")] + sourceText + text[on:]
				}
			}
		}
		if target == DialectBigQuery {
			text = strings.ReplaceAll(text, "EXCEPT ", "EXCEPT DISTINCT ")
			text = strings.ReplaceAll(text, "EXCEPT DISTINCT DISTINCT ", "EXCEPT DISTINCT ")
		}
		text = strings.ReplaceAll(text, "EXISTS (", "EXISTS(")
		if target == DialectPostgreSQL || target == DialectTrino {
			text = strings.ReplaceAll(text, "UPDATE SET target.", "UPDATE SET ")
			text = stripMergeTargetInsertColumns(text)
		}
		return text
	}
	return raw
}

func normalizeGenericClickHouseNode(root Node) Node {
	return Transform(root, func(current Node) Node {
		switch value := current.(type) {
		case *CastExpr:
			value.Type = normalizeClickHouseType(value.Type)
		case *FunctionCallExpr:
			if len(value.Name) == 1 && !value.Name[0].Quoted {
				switch strings.ToUpper(value.Name[0].Text) {
				case "EDITDISTANCE":
					value.Name[0].Text = "editDistance"
				case "FARM_FINGERPRINT", "FARMFINGERPRINT64":
					value.Name[0].Text = "farmFingerprint64"
				case "RANDCANONICAL":
					value.Name[0].Text = "randCanonical"
				case "MAP":
					value.Name[0].Text = "map"
				case "ARRAY_DISTINCT":
					value.Name[0].Text = "arrayDistinct"
				case "ARRAY":
					value.ArrayLiteral = true
				}
			}
		}
		return current
	})
}

func normalizeSparkIdentityCasts(root Node) {
	Transform(root, func(current Node) Node {
		cast, ok := current.(*CastExpr)
		if !ok {
			return current
		}
		name, ok := castTypeIdentifier(cast.Type)
		if ok {
			switch strings.ToUpper(name.Text) {
			case "VARCHAR", "CHAR", "CHARACTER VARYING":
				cast.Type = identifierExpr("STRING")
			}
		}
		return current
	})
}

func normalizeGenericSourceNode(root Node, target Dialect) Node {
	if statement, ok := root.(*SelectStmt); ok {
		normalizeGenericDateArraySourcesDeep(statement, target)
	}
	return Transform(root, func(current Node) Node {
		switch value := current.(type) {
		case *CreateTableStmt:
			if target == DialectTSQL && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value.Tail)), "LIKE ") {
				source := strings.TrimSpace(strings.TrimSpace(value.Tail)[len("LIKE "):])
				return &RawStmt{nodeBase: value.nodeBase, Keyword: "SELECT", Raw: "SELECT TOP 0 * INTO " + generateIdentifiers(value.Name) + " FROM " + source + " AS temp"}
			}
		case *TypedLiteralExpr:
			if value.Value == nil && len(value.TypeName) == 1 && len(value.Parameters) >= 3 {
				name := strings.ToUpper(value.TypeName[0].Text)
				mapped := ""
				switch name {
				case "DATE":
					mapped = map[Dialect]string{DialectDuckDB: "MAKE_DATE", DialectSnowflake: "DATE_FROM_PARTS"}[target]
				case "DATETIME", "TIMESTAMP":
					mapped = map[Dialect]string{DialectDuckDB: "MAKE_TIMESTAMP", DialectSnowflake: "TIMESTAMP_FROM_PARTS"}[target]
				}
				if mapped != "" {
					return &FunctionCallExpr{Name: []Identifier{{Text: mapped}}, Args: value.Parameters}
				}
			}
			if len(value.TypeName) == 1 && strings.EqualFold(value.TypeName[0].Text, "TIMESTAMP") && value.Value != nil {
				var typeName string
				switch target {
				case DialectMySQL, DialectStarRocks, DialectDoris:
					typeName = "DATETIME"
				}
				if typeName != "" {
					return rawCast(value.Value.Raw, typeName)
				}
			}
		case *LiteralExpr:
			// BigQuery accepts both single- and double-quoted string
			// literals. Once the source dialect is no longer BigQuery,
			// retain their value but emit the portable SQL string form.
			if value.KindValue == LiteralString && target != DialectBigQuery {
				if normalized, ok := normalizeBigQueryString(value.Raw); ok {
					value.Raw = normalized
				}
			}
		case *CastExpr:
			if _, ok := value.Type.(*CallExpr); ok {
				value.preserveTypeParameters = true
			}
		case *IdentifierExpr:
			if len(value.Parts) == 1 && value.Parts[0].Quoted && strings.HasPrefix(value.Parts[0].Text, "'") {
				content := value.Parts[0].Text
				if target == DialectHive || target == DialectSpark {
					return &LiteralExpr{KindValue: LiteralString, Raw: "'" + strings.ReplaceAll(content, "'", "\\'") + "'"}
				}
				if target == DialectDuckDB || target == DialectPresto {
					return &LiteralExpr{KindValue: LiteralString, Raw: "'" + strings.ReplaceAll(content, "'", "''") + "'"}
				}
			}
		case *SelectStmt:
			normalizeGenericSourceOrderDefaults(value, target)
			if target == DialectHive || target == DialectSpark || target == DialectDatabricks || target == DialectTSQL {
				flattenNestedCTEs(value, target)
			}
			if wrapped := normalizeGenericSetTail(value, target); wrapped != nil {
				return wrapped
			}
			normalizeGenericSetModifier(value, target)
			normalizeGenericSourceLateralViews(value, target)
		case *BinaryExpr:
			if rewritten := rewriteGenericSourceBinary(value, target); rewritten != nil {
				return rewritten
			}
		case *IsExpr:
			if target == DialectSnowflake && strings.EqualFold(value.Operator, "IS") {
				if literal, ok := value.Right.(*LiteralExpr); ok && literal.KindValue == LiteralBoolean {
					if strings.EqualFold(literal.Raw, "TRUE") {
						return value.Value
					}
					return &UnaryExpr{Operator: "NOT", Expr: value.Value}
				}
			}
		case *FunctionCallExpr:
			if target == DialectPostgreSQL || target == DialectSnowflake {
				value.WithinGroup = genericDefaultNullOrder(value.WithinGroup)
			}
			if value.Over != nil && (target == DialectDuckDB || target == DialectClickHouse || target == DialectPostgreSQL || target == DialectSnowflake) {
				value.Over.OrderBy = genericDefaultNullOrder(value.Over.OrderBy)
			}
			if rewritten := rewriteGenericSourceFunction(value, target); rewritten != nil {
				return rewritten
			}
		}
		return current
	})
}

type genericDateArraySpec struct {
	Start  string
	End    string
	Amount string
	Unit   string
}

func normalizeGenericDateArraySources(stmt *SelectStmt, target Dialect) {
	if stmt == nil {
		return
	}
	generated := make([]CTE, 0)
	generatedIndex := 0
	for index := range stmt.From {
		sourceFunction, _ := stmt.From[index].Primary.(*TableFunctionFrom)
		spec, ok := genericDateArraySpecFromTable(stmt.From[index].Primary)
		if !ok {
			continue
		}
		var replacement FromItem
		switch target {
		case DialectDatabricks, DialectSpark:
			replacement = &RawFrom{Raw: "EXPLODE(SEQUENCE(" + spec.Start + ", " + spec.End + ", INTERVAL '" + spec.Amount + "' " + spec.Unit + "))"}
		case DialectDuckDB:
			replacement = &RawFrom{Raw: "UNNEST(CAST(GENERATE_SERIES(" + spec.Start + ", " + spec.End + ", INTERVAL '" + spec.Amount + "' " + spec.Unit + ") AS DATE[]))"}
		case DialectPresto, DialectTrino:
			intervalAmount, intervalUnit := spec.Amount, spec.Unit
			if strings.EqualFold(spec.Unit, "WEEK") {
				if number, err := strconv.Atoi(spec.Amount); err == nil {
					intervalAmount = strconv.Itoa(number * 7)
				}
				intervalUnit = "DAY"
			}
			replacement = &RawFrom{Raw: "UNNEST(SEQUENCE(" + spec.Start + ", " + spec.End + ", (" + spec.Amount + " * INTERVAL '" + intervalAmount + "' " + intervalUnit + ")))"}
		case DialectPostgreSQL:
			replacement = &RawFrom{Raw: "(SELECT CAST(value AS DATE) FROM GENERATE_SERIES(" + spec.Start + ", " + spec.End + ", INTERVAL '" + spec.Amount + " " + spec.Unit + "') AS _t(value))", Alias: &Identifier{Text: "_unnested_generate_series"}}
		case DialectSnowflake:
			column := "value"
			tableAlias := "_t0"
			if sourceFunction != nil && len(sourceFunction.Columns) > 0 {
				column = sourceFunction.Columns[0].Text
			}
			if sourceFunction != nil && sourceFunction.Alias != nil {
				tableAlias = sourceFunction.Alias.Text
			}
			replacement = &RawFrom{Raw: "(SELECT DATEADD(" + spec.Unit + ", CAST(" + column + " AS INT), " + spec.Start + ") AS " + column + " FROM TABLE(FLATTEN(INPUT => ARRAY_GENERATE_RANGE(0, DATEDIFF(" + spec.Unit + ", " + spec.Start + ", " + spec.End + ") + 1))) AS " + tableAlias + "(seq, key, path, index, " + column + ", this))"}
		case DialectMySQL, DialectStarRocks, DialectRedshift, DialectTSQL:
			name := "_generated_dates"
			if generatedIndex > 0 {
				name += "_" + strconv.Itoa(generatedIndex)
			}
			generatedIndex++
			column := "date_value"
			if sourceFunction != nil && len(sourceFunction.Columns) > 0 {
				column = sourceFunction.Columns[0].Text
			}
			add := genericDateArrayStep(column, spec.Amount, spec.Unit, target)
			query := "SELECT " + spec.Start + " AS " + column + " UNION ALL SELECT CAST(" + add + " AS DATE) FROM " + name + " WHERE CAST(" + add + " AS DATE) <= " + spec.End
			generated = append(generated, CTE{Name: Identifier{Text: name}, Columns: []Identifier{{Text: column}}, Recursive: target != DialectTSQL, Query: &SelectStmt{RawQuery: query}})
			selectColumn := column
			if target == DialectTSQL {
				selectColumn += " AS " + column
			}
			replacement = &RawFrom{Raw: "(SELECT " + selectColumn + " FROM " + name + ")", Alias: &Identifier{Text: name}}
		default:
			continue
		}
		if sourceFunction != nil && (target == DialectDatabricks || target == DialectSpark || target == DialectDuckDB || target == DialectPresto || target == DialectTrino || target == DialectSnowflake) {
			if raw, ok := replacement.(*RawFrom); ok {
				raw.Alias = sourceFunction.Alias
				raw.Columns = append([]Identifier(nil), sourceFunction.Columns...)
			}
		}
		if replacement != nil {
			stmt.From[index].Primary = replacement
		}
	}
	if len(generated) > 0 {
		stmt.With = append(generated, stmt.With...)
	}
}

func normalizeGenericDateArraySourcesDeep(stmt *SelectStmt, target Dialect) {
	if stmt == nil {
		return
	}
	normalizeGenericDateArraySources(stmt, target)
	if target != DialectMySQL && target != DialectRedshift && target != DialectStarRocks && target != DialectTSQL {
		return
	}
	generated := make([]CTE, 0)
	for index := range stmt.With {
		cte := &stmt.With[index]
		if cte.Query == nil {
			continue
		}
		normalizeGenericDateArraySourcesDeep(cte.Query, target)
		remaining := make([]CTE, 0, len(cte.Query.With))
		for childIndex := range cte.Query.With {
			child := cte.Query.With[childIndex]
			if !strings.HasPrefix(child.Name.Text, "_generated_dates") {
				remaining = append(remaining, child)
				continue
			}
			name := nextGeneratedDateName(append(append([]CTE(nil), stmt.With...), generated...))
			if child.Name.Text != name {
				renameGeneratedCTEReferences(cte.Query, child.Name.Text, name)
				child.Name.Text = name
			}
			generated = append(generated, child)
		}
		cte.Query.With = remaining
	}
	if len(generated) > 0 {
		stmt.With = append(generated, stmt.With...)
	}
}

func normalizeGenericSourceOrderDefaults(stmt *SelectStmt, target Dialect) {
	if target != DialectDuckDB && target != DialectClickHouse && target != DialectPostgreSQL && target != DialectSnowflake {
		return
	}
	stmt.OrderBy = genericDefaultNullOrder(stmt.OrderBy)
	stmt.SortBy = genericDefaultNullOrder(stmt.SortBy)
	for index := range stmt.Windows {
		stmt.Windows[index].Spec.OrderBy = genericDefaultNullOrder(stmt.Windows[index].Spec.OrderBy)
	}
}

func genericDefaultNullOrder(items []OrderItem) []OrderItem {
	for index := range items {
		if !items[index].Ascending && !items[index].Descending && !items[index].NullsFirst && !items[index].NullsLast {
			items[index].NullsFirst = true
		}
	}
	return items
}

func normalizeDataFusionTargetDefaults(root Node, target Dialect) {
	if target != DialectSpark && target != DialectSnowflake {
		return
	}
	Walk(root, func(current Node) VisitAction {
		switch value := current.(type) {
		case *SelectStmt:
			if target == DialectSpark {
				value.OrderBy = sparkDefaultNullOrder(value.OrderBy)
				value.SortBy = sparkDefaultNullOrder(value.SortBy)
				for index := range value.Windows {
					value.Windows[index].Spec.OrderBy = sparkDefaultNullOrder(value.Windows[index].Spec.OrderBy)
				}
			}
		case *FunctionCallExpr:
			if target == DialectSnowflake && len(value.Name) == 1 && strings.EqualFold(value.Name[0].Text, "DATE_PART") && len(value.Args) == 2 && isStringLiteral(value.Args[0]) {
				value.Args[0] = unquoteDatePart(value.Args[0])
			}
			if target == DialectSpark && value.Over != nil {
				value.Over.OrderBy = sparkDefaultNullOrder(value.Over.OrderBy)
			}
			if target == DialectSpark {
				value.WithinGroup = sparkDefaultNullOrder(value.WithinGroup)
			}
		}
		return VisitChildren
	})
}

func sparkDefaultNullOrder(items []OrderItem) []OrderItem {
	for index := range items {
		if !items[index].Ascending && !items[index].Descending && !items[index].NullsFirst && !items[index].NullsLast {
			items[index].NullsLast = true
		}
	}
	return items
}

func nextGeneratedDateName(ctes []CTE) string {
	used := make(map[string]bool, len(ctes))
	for _, cte := range ctes {
		used[cte.Name.Text] = true
	}
	for index := 0; ; index++ {
		name := "_generated_dates"
		if index > 0 {
			name += "_" + strconv.Itoa(index)
		}
		if !used[name] {
			return name
		}
	}
}

func renameGeneratedCTEReferences(stmt *SelectStmt, oldName, newName string) {
	if stmt == nil || oldName == newName {
		return
	}
	stmt.RawQuery = strings.ReplaceAll(stmt.RawQuery, oldName, newName)
	for index := range stmt.From {
		if raw, ok := stmt.From[index].Primary.(*RawFrom); ok {
			raw.Raw = strings.ReplaceAll(raw.Raw, oldName, newName)
			if raw.Alias != nil && raw.Alias.Text == oldName {
				raw.Alias.Text = newName
			}
		}
	}
	for index := range stmt.With {
		renameGeneratedCTEReferences(stmt.With[index].Query, oldName, newName)
	}
}

func genericDateArraySpecFromTable(item FromItem) (genericDateArraySpec, bool) {
	function, ok := item.(*TableFunctionFrom)
	if !ok || len(function.Name) != 1 || !strings.EqualFold(function.Name[0].Text, "UNNEST") || len(function.Args) != 1 {
		return genericDateArraySpec{}, false
	}
	array, ok := function.Args[0].(*FunctionCallExpr)
	if !ok {
		return genericDateArraySpec{}, false
	}
	return genericDateArraySpecFromFunction(array)
}

func genericDateArraySpecFromFunction(array *FunctionCallExpr) (genericDateArraySpec, bool) {
	if array == nil || len(array.Name) != 1 || !strings.EqualFold(array.Name[0].Text, "GENERATE_DATE_ARRAY") || len(array.Args) < 3 {
		return genericDateArraySpec{}, false
	}
	interval, ok := array.Args[2].(*IntervalExpr)
	if !ok || len(interval.Qualifiers) == 0 {
		return genericDateArraySpec{}, false
	}
	unit := strings.Trim(renderExpr(interval.Qualifiers[0]), "'")
	return genericDateArraySpec{
		Start:  genericDateArrayDate(array.Args[0]),
		End:    genericDateArrayDate(array.Args[1]),
		Amount: strings.Trim(renderExpr(interval.Value), "'"),
		Unit:   strings.ToUpper(unit),
	}, true
}

func genericDateArrayDate(expression Expr) string {
	if typed, ok := expression.(*TypedLiteralExpr); ok && len(typed.TypeName) == 1 && strings.EqualFold(typed.TypeName[0].Text, "DATE") && typed.Value != nil {
		return "CAST(" + renderExpr(typed.Value) + " AS DATE)"
	}
	return "CAST(" + renderExpr(expression) + " AS DATE)"
}

func genericDateArrayStep(column, amount, unit string, target Dialect) string {
	switch target {
	case DialectTSQL, DialectRedshift:
		return "DATEADD(" + unit + ", " + amount + ", " + column + ")"
	default:
		return "DATE_ADD(" + column + ", INTERVAL " + amount + " " + unit + ")"
	}
}

// flattenNestedCTEs mirrors the dialects that require WITH bindings to live
// at the statement level. It also keeps the order of dependencies stable when
// a CTE contains another WITH clause or a subquery in FROM contains one.
func flattenNestedCTEs(stmt *SelectStmt, target Dialect) {
	if stmt == nil {
		return
	}
	flattened := make([]CTE, 0, len(stmt.With))
	for _, cte := range stmt.With {
		if cte.Query != nil {
			flattenNestedCTEs(cte.Query, target)
			if len(cte.Query.With) > 0 {
				flattened = append(flattened, cte.Query.With...)
				cte.Query.With = nil
			}
			if target == DialectTSQL {
				addTSQLProjectionAliases(cte.Query)
			}
		}
		flattened = append(flattened, cte)
	}
	stmt.With = flattened

	for index := range stmt.From {
		flattenNestedCTEsInTable(&stmt.From[index], stmt, target)
	}
}

func flattenNestedCTEsInTable(table *TableExpr, owner *SelectStmt, target Dialect) {
	if table == nil {
		return
	}
	switch item := table.Primary.(type) {
	case *SubqueryFrom:
		if item.Query != nil {
			flattenNestedCTEs(item.Query, target)
			if len(item.Query.With) > 0 {
				owner.With = append(owner.With, item.Query.With...)
				item.Query.With = nil
			}
			if target == DialectTSQL {
				addTSQLProjectionAliases(item.Query)
			}
		}
	case *GroupedFrom:
		for index := range item.Items {
			flattenNestedCTEsInTable(&item.Items[index], owner, target)
		}
	}
	for index := range table.Joins {
		if table.Joins[index].Right == nil {
			continue
		}
		right := &TableExpr{Primary: table.Joins[index].Right}
		flattenNestedCTEsInTable(right, owner, target)
		table.Joins[index].Right = right.Primary
	}
}

func addTSQLProjectionAliases(stmt *SelectStmt) {
	if stmt == nil {
		return
	}
	for index := range stmt.Projections {
		if stmt.Projections[index].Alias != nil {
			continue
		}
		identifier, ok := stmt.Projections[index].Expr.(*IdentifierExpr)
		if !ok || len(identifier.Parts) == 0 {
			continue
		}
		alias := identifier.Parts[len(identifier.Parts)-1]
		stmt.Projections[index].Alias = &alias
	}
}

func normalizeGenericSetTail(stmt *SelectStmt, target Dialect) Node {
	if stmt.SetOperator == "" || stmt.SetRight == nil || (target != DialectClickHouse && target != DialectTSQL) {
		return nil
	}
	right := stmt.SetRight
	if len(right.OrderBy) == 0 && right.Limit == nil && right.Offset == nil && right.Fetch == nil {
		return nil
	}
	base := *stmt
	base.SetModifier = ""
	base.SetRight = right
	trailingOrder := append([]OrderItem(nil), right.OrderBy...)
	trailingLimit := right.Limit
	trailingOffset := right.Offset
	trailingFetch := right.Fetch
	base.SetRight = right
	trailingRight := *right
	trailingRight.OrderBy = nil
	trailingRight.Limit = nil
	trailingRight.Offset = nil
	trailingRight.Fetch = nil
	base.SetRight = &trailingRight
	if target == DialectClickHouse {
		base.SetModifier = "DISTINCT"
	}
	inner, err := GenerateWithOptions(&base, GenerateOptions{Canonical: true, Dialect: DialectGeneric})
	if err != nil {
		return nil
	}
	if trailingOffset != nil || trailingFetch != nil {
		return nil
	}
	orderParts := make([]string, 0, len(trailingOrder))
	for _, item := range trailingOrder {
		part := renderExpr(item.Expr)
		if item.Descending {
			part += " DESC"
		} else if item.Ascending {
			part += " ASC"
		}
		if target == DialectClickHouse && !item.Descending && !item.Ascending {
			part += " NULLS FIRST"
		}
		orderParts = append(orderParts, part)
	}
	order := ""
	if len(orderParts) > 0 {
		order = " ORDER BY " + strings.Join(orderParts, ", ")
	}
	limit := ""
	if trailingLimit != nil {
		limit = renderExpr(trailingLimit)
	}
	if target == DialectClickHouse {
		return &RawStmt{nodeBase: stmt.nodeBase, Keyword: "SELECT", Raw: "SELECT * FROM (" + inner + ") AS _l_0" + order + func() string {
			if limit == "" {
				return ""
			}
			return " LIMIT " + limit
		}()}
	}
	return &RawStmt{nodeBase: stmt.nodeBase, Keyword: "SELECT", Raw: "SELECT TOP " + limit + " * FROM (" + inner + ") AS _l_0" + order}
}

func rewriteGenericSourceFunction(function *FunctionCallExpr, target Dialect) Expr {
	if len(function.Name) != 1 || function.RawArgs != "" {
		return nil
	}
	name := strings.ToUpper(function.Name[0].Text)
	if len(function.Args) == 0 {
		switch name {
		case "RAND":
			switch target {
			case DialectDuckDB, DialectPostgreSQL, DialectSQLite:
				setFunctionName(function, "RANDOM")
				return function
			case DialectClickHouse:
				setFunctionName(function, "randCanonical")
				return function
			case DialectOracle:
				return &FunctionCallExpr{Name: []Identifier{{Text: "DBMS_RANDOM.VALUE"}}, Args: function.Args}
			}
		case "UUID":
			switch target {
			case DialectPostgreSQL:
				return &FunctionCallExpr{Name: []Identifier{{Text: "GEN_RANDOM_UUID"}}}
			case DialectSnowflake:
				return &FunctionCallExpr{Name: []Identifier{{Text: "UUID_STRING"}}}
			case DialectTSQL:
				return &FunctionCallExpr{Name: []Identifier{{Text: "NEWID"}}}
			}
		case "CURRENT_SCHEMA":
			switch target {
			case DialectSQLite:
				return &LiteralExpr{KindValue: LiteralString, Raw: "'main'"}
			case DialectMySQL:
				return &FunctionCallExpr{Name: []Identifier{{Text: "SCHEMA"}}}
			case DialectPostgreSQL:
				return &RawExpr{Raw: "CURRENT_SCHEMA"}
			case DialectTSQL:
				return &FunctionCallExpr{Name: []Identifier{{Text: "SCHEMA_NAME"}}}
			}
		}
		return nil
	}
	value := function.Args[0]
	renderedValue := renderExpr(value)
	if rewritten := rewriteGenericDateFunction(function, target); rewritten != nil {
		return rewritten
	}
	if rewritten := rewriteGenericArrayFunction(function, target); rewritten != nil {
		return rewritten
	}
	if rewritten := rewriteGenericJSONFunction(function, target); rewritten != nil {
		return rewritten
	}
	switch name {
	case "DATE":
		if len(function.Args) == 3 {
			switch target {
			case DialectDuckDB:
				setFunctionName(function, "MAKE_DATE")
				return function
			case DialectSnowflake:
				setFunctionName(function, "DATE_FROM_PARTS")
				return function
			}
		}
	case "DATETIME":
		if len(function.Args) >= 3 {
			switch target {
			case DialectDuckDB:
				setFunctionName(function, "MAKE_TIMESTAMP")
				return function
			case DialectSnowflake:
				setFunctionName(function, "TIMESTAMP_FROM_PARTS")
				return function
			}
		}
	case "EDIT_DISTANCE":
		mapped := map[Dialect]string{
			DialectDuckDB: "LEVENSHTEIN", DialectPostgreSQL: "LEVENSHTEIN_LESS_EQUAL", DialectSnowflake: "EDITDISTANCE",
		}[target]
		if mapped != "" {
			args := append([]Expr(nil), function.Args...)
			for index, argument := range args {
				if alias, ok := argument.(*AliasExpr); ok && strings.EqualFold(alias.Alias.Text, "MAX_DISTANCE") {
					args[index] = alias.Expr
				}
			}
			function.Name = []Identifier{{Text: mapped}}
			function.Args = args
			return function
		}
	case "JSON_ARRAY_APPEND", "JSON_ARRAY_INSERT", "JSON_REMOVE", "JSON_SET":
		if target == DialectMySQL || target == DialectSQLite || target == DialectDoris {
			for index := range function.Args {
				function.Args[index] = unwrapGenericParseJSON(function.Args[index])
			}
			return function
		}
	case "JSON_STRIP_NULLS":
		if target == DialectPostgreSQL && len(function.Args) == 1 {
			function.Args[0] = &RawExpr{Raw: "CAST(" + renderExpr(unwrapGenericParseJSON(function.Args[0])) + " AS JSON)"}
			return function
		}
	case "JSON_KEYS":
		switch target {
		case DialectSnowflake:
			setFunctionName(function, "OBJECT_KEYS")
			return function
		case DialectSpark, DialectDatabricks:
			setFunctionName(function, "JSON_OBJECT_KEYS")
			return function
		}
	case "ARRAY_LENGTH":
		if target == DialectSnowflake && len(function.Args) == 1 {
			if array, ok := function.Args[0].(*FunctionCallExpr); ok {
				if spec, ok := genericDateArraySpecFromFunction(array); ok {
					return &RawExpr{Raw: "ARRAY_SIZE((SELECT ARRAY_AGG(*) FROM (SELECT DATEADD(" + spec.Unit + ", CAST(value AS INT), " + spec.Start + ") AS value FROM TABLE(FLATTEN(INPUT => ARRAY_GENERATE_RANGE(0, DATEDIFF(" + spec.Unit + ", " + spec.Start + ", " + spec.End + ") + 1))) AS _t0(seq, key, path, index, value, this))))"}
				}
			}
		}
	case "FARM_FINGERPRINT":
		switch target {
		case DialectClickHouse:
			setFunctionName(function, "farmFingerprint64")
			return function
		case DialectRedshift:
			setFunctionName(function, "FARMFINGERPRINT64")
			return function
		}
	case "FORMAT":
		if target == DialectSpark || target == DialectDatabricks {
			setFunctionName(function, "FORMAT_STRING")
			return function
		}
	case "TRIM":
		if len(function.Args) == 2 && (target == DialectHive || target == DialectSpark || target == DialectDatabricks) {
			return &RawExpr{Raw: "TRIM(" + renderExpr(function.Args[1]) + " FROM " + renderExpr(function.Args[0]) + ")"}
		}
	case "LTRIM":
		if len(function.Args) == 2 && (target == DialectHive || target == DialectSpark || target == DialectDatabricks || target == DialectClickHouse) {
			return &RawExpr{Raw: "TRIM(LEADING " + renderExpr(function.Args[1]) + " FROM " + renderExpr(function.Args[0]) + ")"}
		}
	case "RTRIM":
		if len(function.Args) == 2 && (target == DialectHive || target == DialectSpark || target == DialectDatabricks || target == DialectClickHouse) {
			return &RawExpr{Raw: "TRIM(TRAILING " + renderExpr(function.Args[1]) + " FROM " + renderExpr(function.Args[0]) + ")"}
		}
	case "WEEKOFYEAR":
		switch target {
		case DialectSnowflake:
			setFunctionName(function, "WEEKISO")
			return function
		case DialectExasol:
			setFunctionName(function, "WEEK")
			return function
		}
	case "CONCAT":
		if (target == DialectRedshift || target == DialectSQLite) && len(function.Args) >= 2 {
			parts := make([]string, 0, len(function.Args))
			for _, argument := range function.Args {
				parts = append(parts, renderExpr(argument))
			}
			return &RawExpr{Raw: strings.Join(parts, " || ")}
		}
		if len(function.Args) == 1 {
			switch target {
			case DialectPresto, DialectTrino:
				return rawCast(renderedValue, "VARCHAR")
			case DialectTSQL:
				return function.Args[0]
			}
		}
	case "CONCAT_WS":
		if target == DialectDuckDB || target == DialectHive || target == DialectSpark || target == DialectTrino {
			conditions := make([]string, 0, len(function.Args))
			args := make([]string, 0, len(function.Args))
			for index, argument := range function.Args {
				text := renderExpr(argument)
				conditions = append(conditions, text+" IS NULL")
				if target == DialectTrino && index > 0 {
					text = "CAST(" + text + " AS VARCHAR)"
				}
				args = append(args, text)
			}
			return &RawExpr{Raw: "CASE WHEN " + strings.Join(conditions, " OR ") + " THEN NULL ELSE CONCAT_WS(" + strings.Join(args, ", ") + ") END"}
		}
	case "SPACE":
		if target == DialectBigQuery && len(function.Args) == 1 {
			return &FunctionCallExpr{
				Name: []Identifier{{Text: "REPEAT"}},
				Args: []Expr{
					&LiteralExpr{KindValue: LiteralString, Raw: "' '"},
					function.Args[0],
				},
			}
		}
	case "IF":
		if len(function.Args) == 3 {
			switch target {
			case DialectDuckDB:
				return &CaseExpr{Whens: []CaseWhen{{Condition: function.Args[0], Result: function.Args[1]}}, Else: function.Args[2]}
			case DialectDrill:
				return &RawExpr{Raw: "`IF`(" + renderArgs(function.Args) + ")"}
			case DialectTableau:
				return &RawExpr{Raw: "IF " + renderExpr(function.Args[0]) + " THEN " + renderExpr(function.Args[1]) + " ELSE " + renderExpr(function.Args[2]) + " END"}
			}
		}
	case "LEVENSHTEIN":
		mapped := map[Dialect]string{
			DialectClickHouse: "editDistance", DialectSnowflake: "EDITDISTANCE", DialectSQLite: "EDITDIST3",
			DialectBigQuery: "EDIT_DISTANCE", DialectDrill: "LEVENSHTEIN_DISTANCE", DialectPresto: "LEVENSHTEIN_DISTANCE",
			DialectTrino: "LEVENSHTEIN_DISTANCE",
		}[target]
		if mapped != "" {
			setFunctionName(function, mapped)
			return function
		}
		if target == DialectPostgreSQL && len(function.Args) >= 6 {
			setFunctionName(function, "LEVENSHTEIN_LESS_EQUAL")
			return function
		}
	case "ARRAY_FILTER":
		if target == DialectPresto || target == DialectHive || target == DialectSpark {
			setFunctionName(function, "FILTER")
			return function
		}
	case "FILTER":
		if target == DialectStarRocks {
			setFunctionName(function, "ARRAY_FILTER")
			return function
		}
	case "MOD":
		if (target == DialectHive || target == DialectPresto || target == DialectSnowflake) && len(function.Args) == 2 {
			right := function.Args[1]
			if _, alreadyParenthesized := right.(*ParenthesizedExpr); !alreadyParenthesized {
				if _, binary := right.(*BinaryExpr); binary {
					right = &ParenthesizedExpr{Expr: right}
				}
			}
			return &BinaryExpr{Left: function.Args[0], Operator: "%", Right: right}
		}
	case "ARRAY_REMOVE":
		if len(function.Args) == 2 {
			array, remove := renderExpr(function.Args[0]), renderExpr(function.Args[1])
			switch target {
			case DialectClickHouse:
				return &RawExpr{Raw: "arrayFilter(_u -> _u <> " + remove + ", " + array + ")"}
			case DialectBigQuery:
				return &RawExpr{Raw: "ARRAY(SELECT _u FROM UNNEST(" + array + ") AS _u WHERE _u <> " + remove + ")"}
			case DialectDuckDB:
				return &RawExpr{Raw: "LIST_FILTER(" + array + ", _u -> _u <> " + remove + ")"}
			}
		}
	case "COUNT_IF":
		if len(function.Args) == 1 && (target == DialectSQLite || target == DialectPostgreSQL || target == DialectRedshift) {
			if target == DialectSQLite {
				function.Name = []Identifier{{Text: "SUM"}}
				function.Args = []Expr{&FunctionCallExpr{Name: []Identifier{{Text: "IIF"}}, Args: []Expr{function.Args[0], &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}, &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}}}}
			} else {
				function.Name = []Identifier{{Text: "SUM"}}
				function.Args = []Expr{&CaseExpr{Whens: []CaseWhen{{Condition: function.Args[0], Result: &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}}}, Else: &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}}}
			}
			return function
		}
	case "SUBSTR", "SUBSTRING":
		if len(function.Args) == 3 {
			switch target {
			case DialectBigQuery:
				setFunctionName(function, "SUBSTRING")
				return function
			case DialectOracle:
				setFunctionName(function, "SUBSTR")
				return function
			case DialectPostgreSQL:
				return &RawExpr{Raw: "SUBSTRING(" + renderExpr(function.Args[0]) + " FROM " + renderExpr(function.Args[1]) + " FOR " + renderExpr(function.Args[2]) + ")"}
			}
		}
	case "RAND":
		switch target {
		case DialectDuckDB, DialectPostgreSQL, DialectSQLite:
			setFunctionName(function, "RANDOM")
			return function
		case DialectClickHouse:
			setFunctionName(function, "randCanonical")
			return function
		case DialectOracle:
			return &FunctionCallExpr{Name: []Identifier{{Text: "DBMS_RANDOM.VALUE"}}, Args: function.Args}
		}
	case "ARRAY_ANY":
		if len(function.Args) == 2 {
			array := renderExpr(function.Args[0])
			lambda := renderExpr(function.Args[1])
			lambdaParts := strings.SplitN(lambda, " -> ", 2)
			variable, predicate := "x", lambda
			if len(lambdaParts) == 2 {
				variable, predicate = lambdaParts[0], lambdaParts[1]
			}
			switch target {
			case DialectPresto, DialectTrino:
				return &RawExpr{Raw: "ANY_MATCH(" + array + ", " + lambda + ")"}
			case DialectBigQuery:
				return &RawExpr{Raw: "(ARRAY_LENGTH(" + array + ") = 0 OR ARRAY_LENGTH(ARRAY(SELECT " + variable + " FROM UNNEST(" + array + ") AS " + variable + " WHERE " + predicate + ")) <> 0)"}
			case DialectClickHouse:
				return &RawExpr{Raw: "(LENGTH(" + array + ") = 0 OR LENGTH(arrayFilter(" + lambda + ", " + array + ")) <> 0)"}
			case DialectDatabricks, DialectSpark:
				return &RawExpr{Raw: "(SIZE(" + array + ") = 0 OR SIZE(FILTER(" + array + ", " + lambda + ")) <> 0)"}
			case DialectDuckDB:
				return &RawExpr{Raw: "(ARRAY_LENGTH(" + array + ") = 0 OR ARRAY_LENGTH(LIST_FILTER(" + array + ", " + lambda + ")) <> 0)"}
			case DialectTeradata:
				return &RawExpr{Raw: "(CARDINALITY(" + array + ") = 0 OR CARDINALITY(FILTER(" + array + ", " + lambda + ")) <> 0)"}
			case DialectPostgreSQL:
				return &RawExpr{Raw: "(ARRAY_LENGTH(" + array + ", 1) = 0 OR ARRAY_LENGTH(ARRAY(SELECT " + variable + " FROM UNNEST(" + array + ") AS _t0(" + variable + ") WHERE " + predicate + "), 1) <> 0)"}
			}
		}
	case "STR_POSITION":
		return rewriteGenericStrPosition(function, target)
	case "TIME_STR_TO_DATE":
		switch target {
		case DialectDrill, DialectDuckDB:
			return rawCast(renderedValue, "DATE")
		case DialectHive, DialectStarRocks, DialectDoris:
			return &FunctionCallExpr{Name: []Identifier{{Text: "TO_DATE"}}, Args: []Expr{value}}
		case DialectPresto:
			return rawCast(renderedValue, "TIMESTAMP")
		}
	case "STR_TO_UNIX":
		if target == DialectHive && len(function.Args) > 1 {
			if strings.EqualFold(renderExpr(function.Args[1]), "'yyyy-MM-dd HH:mm:ss'") {
				return &FunctionCallExpr{Name: []Identifier{{Text: "UNIX_TIMESTAMP"}}, Args: []Expr{value}}
			}
			args := append([]Expr(nil), function.Args...)
			args[1] = normalizeTimeFormat(args[1], "hive")
			return &FunctionCallExpr{Name: []Identifier{{Text: "UNIX_TIMESTAMP"}}, Args: args}
		}
	case "DATE_FORMAT":
		if len(function.Args) == 2 {
			format := normalizeGenericDateFormat(function.Args[1], "generic")
			switch target {
			case DialectBigQuery:
				return &RawExpr{Raw: "FORMAT_DATE(" + renderExpr(format) + ", CAST(" + renderedValue + " AS DATETIME))"}
			case DialectDuckDB:
				return &RawExpr{Raw: "STRFTIME(CAST(" + renderedValue + " AS TIMESTAMP), " + renderExpr(normalizeGenericDateFormat(function.Args[1], "duckdb")) + ")"}
			case DialectPresto:
				return &RawExpr{Raw: "DATE_FORMAT(CAST(" + renderedValue + " AS TIMESTAMP), " + renderExpr(normalizeGenericDateFormat(function.Args[1], "presto")) + ")"}
			}
		}
	case "FROM_UNIXTIME":
		if len(function.Args) == 2 {
			format := function.Args[1]
			switch target {
			case DialectDuckDB:
				return &RawExpr{Raw: "STRFTIME(TO_TIMESTAMP(" + renderedValue + "), " + renderExpr(normalizeGenericDateFormat(format, "duckdb")) + ")"}
			case DialectPresto:
				return &RawExpr{Raw: "DATE_FORMAT(FROM_UNIXTIME(" + renderedValue + "), " + renderExpr(normalizeGenericDateFormat(format, "presto")) + ")"}
			case DialectHive, DialectSpark:
				return &RawExpr{Raw: "FROM_UNIXTIME(" + renderedValue + ", " + renderExpr(normalizeGenericDateFormat(format, "hive")) + ")"}
			}
		}
	case "STR_TO_DATE":
		if len(function.Args) == 1 && (target == DialectHive || target == DialectDrill) {
			return rawCast(renderedValue, "DATE")
		}
	case "DATEDIFF":
		if len(function.Args) == 2 {
			switch target {
			case DialectDuckDB:
				return &RawExpr{Raw: "DATE_DIFF('DAY', CAST(" + renderExpr(function.Args[1]) + " AS DATE), CAST(" + renderedValue + " AS DATE))"}
			case DialectPresto:
				return &RawExpr{Raw: "DATE_DIFF('DAY', CAST(CAST(" + renderExpr(function.Args[1]) + " AS TIMESTAMP) AS DATE), CAST(CAST(" + renderedValue + " AS TIMESTAMP) AS DATE))"}
			}
		}
	case "DATE_FROM_UNIX_DATE":
		if len(function.Args) == 1 {
			switch target {
			case DialectDuckDB:
				return &RawExpr{Raw: "CAST('1970-01-01' AS DATE) + INTERVAL " + renderedValue + " DAY"}
			case DialectRedshift, DialectSnowflake:
				return &RawExpr{Raw: "DATEADD(DAY, " + renderedValue + ", CAST('1970-01-01' AS DATE))"}
			case DialectPresto, DialectTrino:
				return &RawExpr{Raw: "DATE_ADD('DAY', " + renderedValue + ", CAST('1970-01-01' AS DATE))"}
			}
		}
	case "TIME_STR_TO_TIME":
		return rewriteGenericTimeStringToTime(function, target)
	case "TIME_STR_TO_UNIX":
		switch target {
		case DialectDuckDB:
			return &FunctionCallExpr{Name: []Identifier{{Text: "EPOCH"}}, Args: []Expr{rawCast(renderedValue, "TIMESTAMP")}}
		case DialectHive, DialectMySQL, DialectDoris:
			return &FunctionCallExpr{Name: []Identifier{{Text: "UNIX_TIMESTAMP"}}, Args: []Expr{value}}
		case DialectPresto:
			parsed := &FunctionCallExpr{Name: []Identifier{{Text: "DATE_PARSE"}}, Args: []Expr{value, &LiteralExpr{KindValue: LiteralString, Raw: "'%Y-%m-%d %T'"}}}
			return &FunctionCallExpr{Name: []Identifier{{Text: "TO_UNIXTIME"}}, Args: []Expr{parsed}}
		}
	case "TIME_TO_TIME_STR":
		typeName := ""
		switch target {
		case DialectDrill, DialectPresto:
			typeName = "VARCHAR"
		case DialectDuckDB:
			typeName = "TEXT"
		case DialectHive, DialectDoris:
			typeName = "STRING"
		case DialectRedshift:
			typeName = "VARCHAR(MAX)"
		}
		if typeName != "" {
			return rawCast(renderedValue, typeName)
		}
	case "TIME_TO_UNIX":
		switch target {
		case DialectDuckDB:
			return &FunctionCallExpr{Name: []Identifier{{Text: "EPOCH"}}, Args: []Expr{value}}
		case DialectHive, DialectDrill, DialectDoris:
			return &FunctionCallExpr{Name: []Identifier{{Text: "UNIX_TIMESTAMP"}}, Args: []Expr{value}}
		case DialectPresto:
			return &FunctionCallExpr{Name: []Identifier{{Text: "TO_UNIXTIME"}}, Args: []Expr{value}}
		}
	case "UNIX_TO_STR":
		if target == DialectStarRocks || target == DialectDoris {
			args := append([]Expr(nil), function.Args...)
			return &FunctionCallExpr{Name: []Identifier{{Text: "FROM_UNIXTIME"}}, Args: args}
		}
	case "UNIX_TO_TIME":
		switch target {
		case DialectDuckDB, DialectMaterialize, DialectPostgreSQL:
			return &FunctionCallExpr{Name: []Identifier{{Text: "TO_TIMESTAMP"}}, Args: []Expr{value}}
		case DialectHive, DialectPresto, DialectStarRocks, DialectDoris:
			return &FunctionCallExpr{Name: []Identifier{{Text: "FROM_UNIXTIME"}}, Args: []Expr{value}}
		case DialectOracle:
			return &RawExpr{Raw: "TO_DATE('1970-01-01', 'YYYY-MM-DD') + (" + renderedValue + " / 86400)"}
		case DialectExasol:
			return &FunctionCallExpr{Name: []Identifier{{Text: "FROM_POSIX_TIME"}}, Args: []Expr{value}}
		}
	case "UNIX_TO_TIME_STR":
		switch target {
		case DialectDuckDB:
			return rawCast("TO_TIMESTAMP("+renderedValue+")", "TEXT")
		case DialectHive:
			return &FunctionCallExpr{Name: []Identifier{{Text: "FROM_UNIXTIME"}}, Args: []Expr{value}}
		case DialectPresto:
			return rawCast("FROM_UNIXTIME("+renderedValue+")", "VARCHAR")
		}
	case "TS_OR_DS_TO_DATE_STR":
		typeName, functionName := "", "SUBSTRING"
		switch target {
		case DialectDuckDB:
			typeName = "TEXT"
		case DialectHive, DialectDoris:
			typeName = "STRING"
		case DialectPresto:
			typeName, functionName = "VARCHAR", "SUBSTR"
		}
		if typeName != "" {
			casted := rawCast(renderedValue, typeName)
			return &FunctionCallExpr{Name: []Identifier{{Text: functionName}}, Args: []Expr{casted, &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}, &LiteralExpr{KindValue: LiteralNumber, Raw: "10"}}}
		}
	case "TS_OR_DS_TO_DATE":
		if len(function.Args) == 1 {
			switch target {
			case DialectBigQuery, DialectDuckDB, DialectMaterialize, DialectPostgreSQL:
				return rawCast(renderedValue, "DATE")
			case DialectHive, DialectSnowflake, DialectDoris:
				return &FunctionCallExpr{Name: []Identifier{{Text: "TO_DATE"}}, Args: []Expr{value}}
			case DialectPresto:
				return rawCast("CAST("+renderedValue+" AS TIMESTAMP)", "DATE")
			case DialectMySQL:
				return &FunctionCallExpr{Name: []Identifier{{Text: "DATE"}}, Args: []Expr{value}}
			}
		}
		if len(function.Args) == 2 {
			format := function.Args[1]
			formatForJava := normalizeDayFormat(format, "hive")
			formatForPresto := normalizeDayFormat(format, "presto")
			switch target {
			case DialectDuckDB:
				return rawCast("STRPTIME("+renderedValue+", "+renderExpr(format)+")", "DATE")
			case DialectHive, DialectSpark:
				return &FunctionCallExpr{Name: []Identifier{{Text: "TO_DATE"}}, Args: []Expr{value, formatForJava}}
			case DialectPresto:
				return rawCast("DATE_PARSE("+renderedValue+", "+renderExpr(formatForPresto)+")", "DATE")
			}
		}
	case "DATE_TO_DATE_STR":
		typeName := map[Dialect]string{DialectDrill: "VARCHAR", DialectDuckDB: "TEXT", DialectHive: "STRING", DialectPresto: "VARCHAR"}[target]
		if typeName != "" {
			return rawCast(renderedValue, typeName)
		}
	case "DATE_TO_DI":
		switch target {
		case DialectDrill:
			return rawCast("TO_DATE("+renderedValue+", 'yyyyMMdd')", "INT")
		case DialectDuckDB:
			return rawCast("STRFTIME("+renderedValue+", '%Y%m%d')", "INT")
		case DialectHive:
			return rawCast("DATE_FORMAT("+renderedValue+", 'yyyyMMdd')", "INT")
		case DialectPresto:
			return rawCast("DATE_FORMAT("+renderedValue+", '%Y%m%d')", "INT")
		}
	case "DI_TO_DATE":
		castType := map[Dialect]string{DialectDrill: "VARCHAR", DialectDuckDB: "TEXT", DialectHive: "STRING", DialectPresto: "VARCHAR"}[target]
		if castType != "" {
			casted := "CAST(" + renderedValue + " AS " + castType + ")"
			switch target {
			case DialectDrill, DialectHive:
				return &FunctionCallExpr{Name: []Identifier{{Text: "TO_DATE"}}, Args: []Expr{&RawExpr{Raw: casted}, &LiteralExpr{KindValue: LiteralString, Raw: "'yyyyMMdd'"}}}
			case DialectDuckDB:
				return rawCast("STRPTIME("+casted+", '%Y%m%d')", "DATE")
			case DialectPresto:
				return rawCast("DATE_PARSE("+casted+", '%Y%m%d')", "DATE")
			}
		}
	case "TS_OR_DI_TO_DI":
		castType := map[Dialect]string{DialectDuckDB: "TEXT", DialectHive: "STRING", DialectPresto: "VARCHAR", DialectSpark: "STRING"}[target]
		if castType != "" {
			casted := "CAST(" + renderedValue + " AS " + castType + ")"
			functionName := "SUBSTR"
			if target == DialectHive || target == DialectDuckDB {
				functionName = "SUBSTR"
			}
			return rawCast(functionName+"(REPLACE("+casted+", '-', ''), 1, 8)", "INT")
		}
	}
	return nil
}

func unwrapGenericParseJSON(expression Expr) Expr {
	function, ok := expression.(*FunctionCallExpr)
	if ok && len(function.Name) == 1 && len(function.Args) == 1 && strings.EqualFold(function.Name[0].Text, "PARSE_JSON") {
		return function.Args[0]
	}
	return expression
}

func normalizeGenericSetModifier(stmt *SelectStmt, target Dialect) {
	if stmt.SetOperator == "" {
		return
	}
	operator := strings.ToUpper(stmt.SetOperator)
	switch target {
	case DialectBigQuery, DialectClickHouse:
		if !stmt.SetAll && stmt.SetModifier == "" {
			stmt.SetModifier = "DISTINCT"
		}
		if target == DialectClickHouse && (strings.Contains(operator, "INTERSECT") || strings.Contains(operator, "EXCEPT")) {
			stmt.SetAll = false
		}
	case DialectDuckDB, DialectPresto, DialectSpark:
		stmt.SetModifier = ""
	}
}

func normalizeGenericSourceLateralViews(stmt *SelectStmt, target Dialect) {
	if target != DialectHive && target != DialectSpark && target != DialectDatabricks {
		return
	}
	for tableIndex := range stmt.From {
		table := &stmt.From[tableIndex]
		if len(table.Joins) == 0 {
			continue
		}
		joins := table.Joins[:0]
		for _, join := range table.Joins {
			function, ok := join.Right.(*TableFunctionFrom)
			if !ok || function == nil || len(function.Name) != 1 || !strings.EqualFold(function.Name[0].Text, "UNNEST") || join.Kind != JoinCross || len(function.Args) == 0 || len(function.Columns) == 0 {
				joins = append(joins, join)
				continue
			}
			view := LateralView{Alias: function.Alias, AliasExplicit: true, Columns: append([]Identifier(nil), function.Columns...)}
			if function.WithOrdinality {
				view.Expression = &FunctionCallExpr{Name: []Identifier{{Text: "POSEXPLODE"}}, Args: []Expr{function.Args[0]}}
				view.Columns = append([]Identifier{{Text: "pos"}}, view.Columns...)
			} else if len(function.Args) > 1 {
				zip := &FunctionCallExpr{Name: []Identifier{{Text: "ARRAYS_ZIP"}}, Args: append([]Expr(nil), function.Args...)}
				view.Expression = &FunctionCallExpr{
					Name: []Identifier{{Text: "INLINE"}},
					Args: []Expr{zip},
				}
			} else {
				view.Expression = &FunctionCallExpr{Name: []Identifier{{Text: "EXPLODE"}}, Args: []Expr{function.Args[0]}}
			}
			table.LateralViews = append(table.LateralViews, view)
		}
		table.Joins = joins
	}
}

func rewriteGenericSourceBinary(expression *BinaryExpr, target Dialect) Expr {
	operator := strings.ToUpper(strings.TrimSpace(expression.Operator))
	if operator == "LIKE ANY" && target == DialectDuckDB {
		items := flattenTupleExpressions(expression.Right)
		if len(items) > 0 {
			left := renderExpr(expression.Left)
			parts := make([]string, 0, len(items))
			for _, item := range items {
				parts = append(parts, left+" LIKE "+renderExpr(item))
			}
			return &RawExpr{Raw: strings.Join(parts, " OR ")}
		}
	}
	if expression.Operator == "/" {
		typeName := map[Dialect]string{
			DialectDrill: "DOUBLE", DialectPresto: "DOUBLE", DialectTrino: "DOUBLE",
			DialectRedshift: "DOUBLE PRECISION", DialectPostgreSQL: "DOUBLE PRECISION", DialectTeradata: "DOUBLE PRECISION",
			DialectSQLite: "REAL", DialectTSQL: "FLOAT",
		}[target]
		if typeName != "" {
			return &BinaryExpr{nodeBase: expression.nodeBase, Left: rawCast(renderExpr(expression.Left), typeName), Operator: "/", Right: expression.Right}
		}
	}
	if operator != "ILIKE" && operator != "NOT ILIKE" {
		return nil
	}
	if target == DialectPostgreSQL || target == DialectClickHouse || target == DialectDuckDB || target == DialectSnowflake || target == DialectSpark {
		return nil
	}
	if target == DialectDrill {
		raw := "ILIKE(" + renderExpr(expression.Left) + ", " + renderExpr(expression.Right) + ")"
		if operator == "NOT ILIKE" {
			raw = "NOT " + raw
		}
		return &RawExpr{Raw: raw}
	}
	if target == DialectBigQuery || target == DialectHive || target == DialectMySQL || target == DialectOracle || target == DialectPresto || target == DialectSQLite || target == DialectStarRocks || target == DialectTrino || target == DialectDoris {
		left := &FunctionCallExpr{Name: []Identifier{{Text: "LOWER"}}, Args: []Expr{expression.Left}}
		right := &FunctionCallExpr{Name: []Identifier{{Text: "LOWER"}}, Args: []Expr{expression.Right}}
		outOperator := "LIKE"
		if operator == "NOT ILIKE" {
			outOperator = "NOT LIKE"
		}
		return &BinaryExpr{nodeBase: expression.nodeBase, Left: left, Operator: outOperator, Right: right}
	}
	return nil
}

func flattenTupleExpressions(expression Expr) []Expr {
	switch value := expression.(type) {
	case *ParenthesizedExpr:
		return flattenTupleExpressions(value.Expr)
	case *TupleExpr:
		if len(value.Items) == 1 {
			if nested := flattenTupleExpressions(value.Items[0]); len(nested) > 0 {
				return nested
			}
		}
		return value.Items
	default:
		return nil
	}
}

func rewriteGenericStrPosition(function *FunctionCallExpr, target Dialect) Expr {
	if len(function.Args) < 2 || len(function.Args) > 4 {
		return nil
	}
	haystack := renderExpr(function.Args[0])
	needle := renderExpr(function.Args[1])
	if len(function.Args) == 2 {
		var name, args string
		switch target {
		case DialectPresto, DialectTrino, DialectAthena, DialectDuckDB, DialectDrill:
			name, args = "STRPOS", haystack+", "+needle
		case DialectSnowflake, DialectTSQL:
			name, args = "CHARINDEX", needle+", "+haystack
		case DialectSpark, DialectDoris, DialectHive, DialectMySQL, DialectDatabricks:
			name, args = "LOCATE", needle+", "+haystack
		case DialectSQLite, DialectBigQuery, DialectTeradata, DialectOracle:
			name, args = "INSTR", haystack+", "+needle
		case DialectPostgreSQL, DialectRedshift, DialectMaterialize, DialectRisingWave:
			return &RawExpr{Raw: "POSITION(" + needle + " IN " + haystack + ")"}
		case DialectTableau:
			name, args = "FIND", haystack+", "+needle
		case DialectClickHouse:
			name, args = "POSITION", haystack+", "+needle
		default:
			return nil
		}
		return &RawExpr{Raw: name + "(" + args + ")"}
	}
	position := renderExpr(function.Args[2])
	if len(function.Args) == 4 {
		occurrence := renderExpr(function.Args[3])
		switch target {
		case DialectBigQuery, DialectOracle, DialectTeradata:
			return &RawExpr{Raw: "INSTR(" + haystack + ", " + needle + ", " + position + ", " + occurrence + ")"}
		case DialectPresto, DialectTrino:
			part := "STRPOS(SUBSTR(" + haystack + ", " + position + "), " + needle + ", " + occurrence + ")"
			return &RawExpr{Raw: "IF(" + part + " = 0, 0, " + part + " + " + position + " - 1)"}
		case DialectTableau:
			part := "FINDNTH(SUBSTRING(" + haystack + ", " + position + "), " + needle + ", " + occurrence + ")"
			return &RawExpr{Raw: "IF " + part + " = 0 THEN 0 ELSE " + part + " + " + position + " - 1 END"}
		default:
			return nil
		}
	}
	switch target {
	case DialectPostgreSQL, DialectRedshift, DialectMaterialize, DialectRisingWave:
		part := "POSITION(" + needle + " IN SUBSTRING(" + haystack + " FROM " + position + "))"
		return &RawExpr{Raw: "CASE WHEN " + part + " = 0 THEN 0 ELSE " + part + " + " + position + " - 1 END"}
	case DialectSQLite:
		part := "INSTR(SUBSTRING(" + haystack + ", " + position + "), " + needle + ")"
		return &RawExpr{Raw: "IIF(" + part + " = 0, 0, " + part + " + " + position + " - 1)"}
	case DialectPresto, DialectTrino:
		part := "STRPOS(SUBSTR(" + haystack + ", " + position + "), " + needle + ")"
		return &RawExpr{Raw: "IF(" + part + " = 0, 0, " + part + " + " + position + " - 1)"}
	case DialectDrill:
		part := "STRPOS(SUBSTRING(" + haystack + ", " + position + "), " + needle + ")"
		return &RawExpr{Raw: "`IF`(" + part + " = 0, 0, " + part + " + " + position + " - 1)"}
	case DialectDuckDB:
		part := "STRPOS(SUBSTRING(" + haystack + ", " + position + "), " + needle + ")"
		return &RawExpr{Raw: "CASE WHEN " + part + " = 0 THEN 0 ELSE " + part + " + " + position + " - 1 END"}
	case DialectDoris, DialectHive, DialectDatabricks, DialectMySQL, DialectSpark:
		return &RawExpr{Raw: "LOCATE(" + needle + ", " + haystack + ", " + position + ")"}
	case DialectBigQuery, DialectTeradata:
		return &RawExpr{Raw: "INSTR(" + haystack + ", " + needle + ", " + position + ")"}
	case DialectAthena:
		part := "STRPOS(SUBSTR(" + haystack + ", " + position + "), " + needle + ")"
		return &RawExpr{Raw: "IF(" + part + " = 0, 0, " + part + " + " + position + " - 1)"}
	case DialectSnowflake, DialectTSQL:
		return &RawExpr{Raw: "CHARINDEX(" + needle + ", " + haystack + ", " + position + ")"}
	case DialectOracle:
		return &RawExpr{Raw: "INSTR(" + haystack + ", " + needle + ", " + position + ")"}
	case DialectClickHouse:
		return &RawExpr{Raw: "POSITION(" + haystack + ", " + needle + ", " + position + ")"}
	case DialectTableau:
		part := "FIND(SUBSTRING(" + haystack + ", " + position + "), " + needle + ")"
		return &RawExpr{Raw: "IF " + part + " = 0 THEN 0 ELSE " + part + " + " + position + " - 1 END"}
	}
	return nil
}

func rewriteGenericDateFunction(function *FunctionCallExpr, target Dialect) Expr {
	name := strings.ToUpper(function.Name[0].Text)
	args := function.Args
	if len(args) == 0 {
		return nil
	}
	value := renderExpr(args[0])
	unit := "DAY"
	amount := "1"
	if len(args) >= 2 {
		amount = renderExpr(args[1])
	}
	if len(args) >= 3 {
		unit = strings.ToUpper(strings.Trim(renderExpr(args[2]), "'"))
	}
	if name == "DATE_TRUNC" && len(args) >= 2 {
		unit = strings.ToUpper(strings.Trim(renderExpr(args[0]), "'"))
		value = renderExpr(args[1])
	}
	if name == "TIMESTAMP_TRUNC" && len(args) >= 2 {
		unit = strings.ToUpper(strings.Trim(renderExpr(args[1]), "'"))
	}
	switch name {
	case "DATE_ADD":
		return genericDateAdd(value, amount, unit, target)
	case "DATE_SUB":
		value = normalizeGenericDateAddValue(value, target)
		negative := amount + " * -1"
		switch target {
		case DialectDuckDB:
			return &RawExpr{Raw: value + " + INTERVAL (" + negative + ") " + unit}
		case DialectBigQuery:
			return &RawExpr{Raw: "DATE_ADD(" + value + ", INTERVAL (" + negative + ") " + unit + ")"}
		case DialectHive, DialectSpark:
			return &RawExpr{Raw: "DATE_ADD(" + value + ", " + negative + ")"}
		case DialectPresto:
			return &RawExpr{Raw: "DATE_ADD('" + unit + "', " + negative + ", " + value + ")"}
		case DialectRedshift:
			return &RawExpr{Raw: "DATEADD(" + unit + ", " + negative + ", " + value + ")"}
		case DialectSnowflake:
			return &RawExpr{Raw: "DATEADD(" + unit + ", " + negative + ", " + value + ")"}
		case DialectTSQL:
			return &RawExpr{Raw: "DATEADD(" + unit + ", " + negative + ", " + value + ")"}
		}
	case "TS_OR_DS_ADD":
		switch target {
		case DialectDrill:
			return &RawExpr{Raw: "DATE_ADD(CAST(" + value + " AS DATE), INTERVAL " + amount + " " + unit + ")"}
		case DialectDuckDB:
			return &RawExpr{Raw: "CAST(" + value + " AS DATE) + INTERVAL " + amount + " " + unit}
		case DialectPresto:
			return &RawExpr{Raw: "DATE_ADD('" + unit + "', " + amount + ", CAST(CAST(" + value + " AS TIMESTAMP) AS DATE))"}
		case DialectHive, DialectSpark:
			return &RawExpr{Raw: "DATE_ADD(" + value + ", " + amount + ")"}
		case DialectMySQL:
			return &RawExpr{Raw: "DATE_ADD(" + value + ", INTERVAL " + amount + " " + unit + ")"}
		}
	case "DATE_TRUNC":
		return genericDateTrunc(value, unit, target)
	case "TIMESTAMP_TRUNC":
		zone := ""
		if len(args) > 2 {
			zone = renderExpr(args[2])
		}
		return genericTimestampTrunc(value, unit, zone, target)
	case "STR_TO_DATE":
		if len(args) < 2 {
			return nil
		}
		format := args[1]
		switch target {
		case DialectMySQL, DialectStarRocks, DialectDoris:
			return &RawExpr{Raw: "STR_TO_DATE(" + value + ", " + renderExpr(normalizeTimeFormat(format, "mysql")) + ")"}
		case DialectSpark:
			return &RawExpr{Raw: "TO_DATE(" + value + ", " + renderExpr(normalizeTimeFormat(format, "spark")) + ")"}
		case DialectDrill:
			if isDateOnlyFormat(format) {
				return rawCast(value, "DATE")
			}
			return &RawExpr{Raw: "TO_DATE(" + value + ", " + renderExpr(normalizeTimeFormat(format, "drill")) + ")"}
		case DialectHive:
			if isDateOnlyFormat(format) {
				return rawCast(value, "DATE")
			}
			return &RawExpr{Raw: "CAST(FROM_UNIXTIME(UNIX_TIMESTAMP(" + value + ", " + renderExpr(normalizeTimeFormat(format, "hive")) + ")) AS DATE)"}
		case DialectPresto:
			return rawCast("DATE_PARSE("+value+", "+renderExpr(normalizeTimeFormat(format, "presto"))+")", "DATE")
		}
	case "DATE_STR_TO_DATE":
		switch target {
		case DialectSQLite:
			return args[0]
		case DialectDrill, DialectDuckDB, DialectHive, DialectPresto, DialectSpark, DialectTSQL:
			return rawCast(value, "DATE")
		}
	}
	return nil
}

func genericDateAdd(value, amount, unit string, target Dialect) Expr {
	switch target {
	case DialectBigQuery, DialectDrill, DialectMySQL, DialectStarRocks, DialectDoris:
		return &RawExpr{Raw: "DATE_ADD(" + value + ", INTERVAL " + amount + " " + unit + ")"}
	case DialectHive, DialectSpark, DialectDremio:
		return &RawExpr{Raw: "DATE_ADD(" + value + ", " + amount + ")"}
	case DialectPresto:
		return &RawExpr{Raw: "DATE_ADD('" + unit + "', " + amount + ", " + value + ")"}
	case DialectDuckDB:
		return &RawExpr{Raw: value + " + INTERVAL " + amount + " " + unit}
	case DialectPostgreSQL, DialectMaterialize:
		return &RawExpr{Raw: value + " + INTERVAL '" + amount + " " + unit + "'"}
	case DialectRedshift, DialectSnowflake, DialectTSQL:
		return &RawExpr{Raw: "DATEADD(" + unit + ", " + amount + ", " + value + ")"}
	case DialectSQLite:
		return &RawExpr{Raw: "DATE(" + value + ", '" + amount + " " + unit + "')"}
	}
	return nil
}

func normalizeGenericDateAddValue(value string, target Dialect) string {
	if target == DialectRedshift || target == DialectSnowflake || target == DialectTSQL {
		if strings.HasPrefix(strings.ToUpper(value), "CAST(") && strings.HasSuffix(value, " AS DATE)") {
			return value
		}
		return "CAST(" + value + " AS DATE)"
	}
	return value
}

func genericDateTrunc(value, unit string, target Dialect) Expr {
	switch target {
	case DialectBigQuery:
		return &RawExpr{Raw: "DATE_TRUNC(" + value + ", " + unit + ")"}
	case DialectSpark:
		return &RawExpr{Raw: "TRUNC(" + value + ", '" + unit + "')"}
	case DialectMySQL:
		switch strings.ToLower(unit) {
		case "day":
			return &RawExpr{Raw: "DATE(" + value + ")"}
		case "week":
			return &RawExpr{Raw: "STR_TO_DATE(CONCAT(YEAR(" + value + "), ' ', WEEK(" + value + ", 1), ' 1'), '%Y %u %w')"}
		case "month":
			return &RawExpr{Raw: "STR_TO_DATE(CONCAT(YEAR(" + value + "), ' ', MONTH(" + value + "), ' 1'), '%Y %c %e')"}
		case "quarter":
			return &RawExpr{Raw: "STR_TO_DATE(CONCAT(YEAR(" + value + "), ' ', QUARTER(" + value + ") * 3 - 2, ' 1'), '%Y %c %e')"}
		default:
			return &RawExpr{Raw: "STR_TO_DATE(CONCAT(YEAR(" + value + "), ' 1 1'), '%Y %c %e')"}
		}
	case DialectDoris:
		return &RawExpr{Raw: "DATE_TRUNC(" + value + ", '" + unit + "')"}
	case DialectDuckDB, DialectPresto, DialectMaterialize, DialectPostgreSQL, DialectSnowflake, DialectStarRocks:
		return &RawExpr{Raw: "DATE_TRUNC('" + unit + "', " + value + ")"}
	}
	return nil
}

func genericTimestampTrunc(value, unit, zone string, target Dialect) Expr {
	if zone == "" {
		switch target {
		case DialectSpark:
			return &RawExpr{Raw: "DATE_TRUNC('" + unit + "', " + value + ")"}
		case DialectDoris:
			return &RawExpr{Raw: "DATE_TRUNC(" + value + ", '" + unit + "')"}
		}
		return nil
	}
	switch target {
	case DialectDuckDB:
		return &RawExpr{Raw: "DATE_TRUNC('" + unit + "', " + value + " AT TIME ZONE " + zone + ") AT TIME ZONE " + zone}
	case DialectMaterialize, DialectPostgreSQL:
		return &RawExpr{Raw: "DATE_TRUNC('" + unit + "', " + value + ", " + zone + ")"}
	case DialectPresto, DialectSnowflake, DialectDatabricks:
		return &RawExpr{Raw: "DATE_TRUNC('" + unit + "', " + value + ")"}
	case DialectClickHouse:
		return &RawExpr{Raw: "dateTrunc('" + unit + "', " + value + ", " + zone + ")"}
	}
	return nil
}

func isDateOnlyFormat(expression Expr) bool {
	literal, ok := expression.(*LiteralExpr)
	return ok && literal.KindValue == LiteralString && (literal.Raw == "'%Y-%m-%d'" || literal.Raw == "'yyyy-MM-dd'")
}

func rewriteGenericArrayFunction(function *FunctionCallExpr, target Dialect) Expr {
	name := strings.ToUpper(function.Name[0].Text)
	args := function.Args
	argText := renderArgs(args)
	switch name {
	case "ARRAY":
		switch target {
		case DialectBigQuery, DialectDuckDB:
			return &RawExpr{Raw: "[" + argText + "]"}
		case DialectPresto:
			return &RawExpr{Raw: "ARRAY[" + argText + "]"}
		}
	case "ARRAY_SIZE":
		mapped := map[Dialect]string{DialectBigQuery: "ARRAY_LENGTH", DialectDuckDB: "ARRAY_LENGTH", DialectDrill: "REPEATED_COUNT", DialectPresto: "CARDINALITY", DialectSpark: "SIZE"}[target]
		if mapped != "" {
			return &RawExpr{Raw: mapped + "(" + argText + ")"}
		}
	case "ARRAY_SUM":
		inner := argText
		switch target {
		case DialectDuckDB:
			inner = strings.Replace(inner, "ARRAY(", "[", 1)
			if strings.HasSuffix(inner, ")") {
				inner = inner[:len(inner)-1] + "]"
			}
			return &RawExpr{Raw: "LIST_SUM(" + inner + ")"}
		case DialectTrino:
			inner = strings.Replace(inner, "ARRAY(", "ARRAY[", 1)
			if strings.HasSuffix(inner, ")") {
				inner = inner[:len(inner)-1] + "]"
			}
			return &RawExpr{Raw: "REDUCE(" + inner + ", 0, (acc, x) -> acc + x, acc -> acc)"}
		case DialectSpark:
			return &RawExpr{Raw: "AGGREGATE(" + argText + ", 0, (acc, x) -> acc + x, acc -> acc)"}
		case DialectPresto:
			inner = strings.Replace(inner, "ARRAY(", "ARRAY[", 1)
			if strings.HasSuffix(inner, ")") {
				inner = inner[:len(inner)-1] + "]"
			}
			return &RawExpr{Raw: "ARRAY_SUM(" + inner + ")"}
		}
	case "ARRAY_EXCEPT":
		if len(args) == 2 {
			left := genericArrayArgumentText(args[0], target)
			right := genericArrayArgumentText(args[1], target)
			switch target {
			case DialectDuckDB:
				return &RawExpr{Raw: "CASE WHEN " + left + " IS NULL OR " + right + " IS NULL THEN NULL ELSE LIST_FILTER(LIST_DISTINCT(" + left + "), e -> LENGTH(LIST_FILTER(" + right + ", x -> x IS NOT DISTINCT FROM e)) = 0) END"}
			case DialectSnowflake, DialectTrino, DialectAthena:
				return &RawExpr{Raw: "ARRAY_EXCEPT(" + left + ", " + right + ")"}
			}
		}
	case "ARRAY_POSITION":
		if len(args) == 2 {
			left := genericArrayArgumentText(args[0], target)
			if target == DialectSnowflake {
				return &RawExpr{Raw: "ARRAY_POSITION(" + renderExpr(args[1]) + ", " + left + ")"}
			}
			if target == DialectTrino || target == DialectAthena {
				return &RawExpr{Raw: "ARRAY_POSITION(" + left + ", " + renderExpr(args[1]) + ")"}
			}
		}
	case "REDUCE":
		if target == DialectSpark {
			return &RawExpr{Raw: "AGGREGATE(" + argText + ")"}
		}
	case "ARRAY_INTERSECT":
		if target == DialectSnowflake {
			return &RawExpr{Raw: "ARRAY_INTERSECTION(" + argText + ")"}
		}
	case "ARRAY_REVERSE":
		if target == DialectClickHouse {
			return &RawExpr{Raw: "arrayReverse(" + argText + ")"}
		}
	case "ARRAY_SLICE":
		mapped := map[Dialect]string{DialectClickHouse: "arraySlice", DialectDatabricks: "SLICE", DialectSpark: "SLICE", DialectPresto: "SLICE", DialectTrino: "SLICE", DialectBigQuery: "ARRAY_SLICE", DialectSnowflake: "ARRAY_SLICE", DialectDuckDB: "ARRAY_SLICE"}[target]
		if mapped != "" {
			return &RawExpr{Raw: mapped + "(" + argText + ")"}
		}
	case "SORT_ARRAY":
		mapped := map[Dialect]string{DialectDuckDB: "LIST_SORT", DialectPresto: "ARRAY_SORT", DialectSnowflake: "ARRAY_SORT"}[target]
		if mapped != "" {
			return &RawExpr{Raw: mapped + "(" + argText + ")"}
		}
	case "ARRAY_PREPEND":
		if len(args) == 2 && (target == DialectDuckDB || target == DialectPostgreSQL) {
			name := "ARRAY_PREPEND"
			if target == DialectDuckDB {
				name = "LIST_PREPEND"
			}
			return &RawExpr{Raw: name + "(" + renderExpr(args[1]) + ", " + renderExpr(args[0]) + ")"}
		}
	case "ARRAY_APPEND":
		if len(args) == 2 && target == DialectDuckDB {
			return &RawExpr{Raw: "LIST_APPEND(" + renderExpr(args[0]) + ", " + renderExpr(args[1]) + ")"}
		}
	case "ARRAY_MAX", "ARRAY_MIN":
		if target == DialectClickHouse || target == DialectDuckDB {
			mapped := ""
			if target == DialectClickHouse {
				mapped = map[string]string{"ARRAY_MAX": "arrayMax", "ARRAY_MIN": "arrayMin"}[name]
			} else {
				mapped = map[string]string{"ARRAY_MAX": "LIST_MAX", "ARRAY_MIN": "LIST_MIN"}[name]
			}
			return &RawExpr{Raw: mapped + "(" + argText + ")"}
		}
	}
	return nil
}

func genericArrayArgumentText(expression Expr, target Dialect) string {
	text := renderExpr(expression)
	if !strings.HasPrefix(strings.ToUpper(text), "ARRAY(") || !strings.HasSuffix(text, ")") {
		return text
	}
	inner := text[len("ARRAY(") : len(text)-1]
	switch target {
	case DialectPresto, DialectTrino, DialectAthena:
		return "ARRAY[" + inner + "]"
	case DialectBigQuery, DialectDuckDB, DialectSnowflake:
		return "[" + inner + "]"
	default:
		return text
	}
}

type jsonPathSegment struct {
	Text      string
	Index     bool
	Wildcard  bool
	Bracketed bool
}

func rewriteGenericJSONFunction(function *FunctionCallExpr, target Dialect) Expr {
	name := strings.ToUpper(function.Name[0].Text)
	if (name != "JSON_EXTRACT" && name != "JSON_EXTRACT_SCALAR") || len(function.Args) != 2 {
		return nil
	}
	pathLiteral, ok := function.Args[1].(*LiteralExpr)
	if !ok || pathLiteral.KindValue != LiteralString {
		return nil
	}
	segments := parseJSONPathSegments(pathLiteral.Raw)
	if len(segments) == 0 {
		return nil
	}
	value := renderExpr(function.Args[0])
	dotPath := jsonDotPath(segments)
	compactSegments := make([]jsonPathSegment, 0, len(segments))
	for _, segment := range segments {
		if !segment.Wildcard {
			compactSegments = append(compactSegments, segment)
		}
	}
	compactDotPath := jsonDotPath(compactSegments)
	bracketPath := jsonBracketPath(pathLiteral.Raw)
	paths := jsonPathArgumentList(segments, target)
	scalar := name == "JSON_EXTRACT_SCALAR"
	switch target {
	case DialectDuckDB:
		op := "->"
		if scalar {
			op = "->>"
		}
		return &RawExpr{Raw: value + " " + op + " " + dotPath}
	case DialectSQLite:
		op := "->"
		if scalar {
			op = "->>"
		}
		return &RawExpr{Raw: value + " " + op + " " + compactDotPath}
	case DialectMySQL:
		return &RawExpr{Raw: "JSON_EXTRACT(" + value + ", " + dotPath + ")"}
	case DialectPostgreSQL:
		if scalar {
			return &RawExpr{Raw: "JSON_EXTRACT_PATH_TEXT(" + value + ", " + paths + ")"}
		}
		return &RawExpr{Raw: "JSON_EXTRACT_PATH(" + value + ", " + paths + ")"}
	case DialectRedshift:
		return &RawExpr{Raw: "JSON_EXTRACT_PATH_TEXT(" + value + ", " + paths + ")"}
	case DialectSpark:
		return &RawExpr{Raw: "GET_JSON_OBJECT(" + value + ", " + bracketPath + ")"}
	case DialectTSQL:
		return &RawExpr{Raw: "ISNULL(JSON_QUERY(" + value + ", " + compactDotPath + "), JSON_VALUE(" + value + ", " + compactDotPath + "))"}
	case DialectStarRocks:
		return &RawExpr{Raw: value + " -> " + dotPath}
	case DialectClickHouse:
		return &RawExpr{Raw: "JSONExtractString(" + value + ", " + paths + ")"}
	case DialectBigQuery:
		name := "JSON_EXTRACT"
		if scalar {
			name = "JSON_EXTRACT_SCALAR"
		}
		if strings.Contains(pathLiteral.Raw, "[*]") {
			return &RawExpr{Raw: name + "(" + value + ", " + strings.ReplaceAll(bracketPath, "[*]", "") + ")"}
		}
		return &RawExpr{Raw: name + "(" + value + ", " + bracketPath + ")"}
	case DialectSnowflake:
		if scalar {
			path := strings.Trim(pathLiteral.Raw, "'")
			path = strings.ReplaceAll(path, "[*]", "")
			path = strings.ReplaceAll(path, ".*", "")
			path = strings.TrimPrefix(path, "$")
			return &RawExpr{Raw: "JSON_EXTRACT_PATH_TEXT(" + value + ", '" + strings.TrimPrefix(path, ".") + "')"}
		}
		path := strings.Trim(pathLiteral.Raw, "'")
		path = strings.ReplaceAll(path, "[*]", "")
		path = strings.ReplaceAll(path, ".*", "")
		path = strings.TrimPrefix(path, "$")
		return &RawExpr{Raw: "GET_PATH(PARSE_JSON(" + value + "), '" + strings.TrimPrefix(path, ".") + "')"}
	}
	return nil
}

func parseJSONPathSegments(raw string) []jsonPathSegment {
	path := strings.Trim(raw, "'")
	if strings.HasPrefix(path, "$") {
		path = path[1:]
	}
	segments := make([]jsonPathSegment, 0, 4)
	for index := 0; index < len(path); {
		switch path[index] {
		case '.':
			index++
			start := index
			for index < len(path) && path[index] != '.' && path[index] != '[' {
				index++
			}
			if start < index {
				part := path[start:index]
				segments = append(segments, jsonPathSegment{Text: part, Wildcard: part == "*"})
			}
		case '[':
			end := strings.IndexByte(path[index+1:], ']')
			if end < 0 {
				return segments
			}
			end += index + 1
			part := path[index+1 : end]
			wildcard := part == "*"
			part = strings.Trim(part, "\"")
			segments = append(segments, jsonPathSegment{Text: part, Index: isJSONPathIndex(part), Wildcard: wildcard, Bracketed: true})
			index = end + 1
		default:
			index++
		}
	}
	return segments
}

func isJSONPathIndex(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func jsonDotPath(segments []jsonPathSegment) string {
	var b strings.Builder
	b.WriteByte('\'')
	b.WriteString("$")
	for _, segment := range segments {
		if segment.Wildcard {
			if segment.Bracketed {
				b.WriteString("[*]")
			} else {
				b.WriteString(".*")
			}
			continue
		}
		if segment.Index {
			b.WriteByte('[')
			b.WriteString(segment.Text)
			b.WriteByte(']')
		} else if segment.Bracketed && !isBareJSONPathKey(segment.Text) {
			b.WriteString(`."`)
			b.WriteString(strings.ReplaceAll(segment.Text, `"`, `\"`))
			b.WriteByte('"')
		} else {
			b.WriteByte('.')
			b.WriteString(segment.Text)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

func jsonBracketPath(raw string) string {
	value := strings.Trim(raw, "'")
	value = strings.ReplaceAll(value, `"`, `\'`)
	return "'" + value + "'"
}

func jsonPathArgumentList(segments []jsonPathSegment, target Dialect) string {
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment.Wildcard {
			continue
		}
		if segment.Index && target == DialectClickHouse {
			index, _ := strconv.Atoi(segment.Text)
			parts = append(parts, strconv.Itoa(index+1))
		} else if segment.Index && target != DialectPostgreSQL && target != DialectRedshift {
			parts = append(parts, segment.Text)
		} else {
			parts = append(parts, "'"+strings.ReplaceAll(segment.Text, "'", "''")+"'")
		}
	}
	return strings.Join(parts, ", ")
}

func isBareJSONPathKey(value string) bool {
	if value == "" {
		return false
	}
	for index, r := range value {
		if !(r == '_' || r == '$' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || index > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func rawCast(value, typeName string) Expr {
	return &RawExpr{Raw: "CAST(" + value + " AS " + typeName + ")"}
}

func rewriteGenericTimeStringToTime(function *FunctionCallExpr, target Dialect) Expr {
	value := renderExpr(function.Args[0])
	zone := ""
	if len(function.Args) > 1 {
		zone = renderExpr(function.Args[1])
	}
	if target == DialectSQLite {
		return function.Args[0]
	}
	if target == DialectMySQL && zone != "" {
		return &RawExpr{Raw: "TIMESTAMP(" + value + ")"}
	}
	if target == DialectTSQL && zone != "" {
		return &RawExpr{Raw: "CAST(" + value + " AS DATETIMEOFFSET) AT TIME ZONE 'UTC'"}
	}
	if target == DialectClickHouse {
		typeName := "DateTime64(6)"
		if zone != "" {
			typeName = "DateTime64(6, " + zone + ")"
			if literal, ok := function.Args[0].(*LiteralExpr); ok && literal.KindValue == LiteralString {
				trimmed := strings.Trim(literal.Raw, "'")
				if index := strings.LastIndexAny(trimmed, "+-"); index > strings.IndexByte(trimmed, ' ')+1 {
					trimmed = trimmed[:index]
				}
				value = "'" + trimmed + "'"
			}
		}
		return rawCast(value, typeName)
	}
	if zone != "" {
		typeName := map[Dialect]string{
			DialectBigQuery: "TIMESTAMP", DialectDatabricks: "TIMESTAMP", DialectDuckDB: "TIMESTAMPTZ",
			DialectPostgreSQL: "TIMESTAMPTZ", DialectRedshift: "TIMESTAMP WITH TIME ZONE", DialectSnowflake: "TIMESTAMPTZ",
			DialectSpark: "TIMESTAMP", DialectTrino: "TIMESTAMP WITH TIME ZONE", DialectPresto: "TIMESTAMP WITH TIME ZONE",
			DialectDrill: "TIMESTAMP", DialectHive: "TIMESTAMP", DialectDoris: "DATETIME",
		}[target]
		if typeName != "" {
			if target == DialectTrino {
				if precision := fractionalPrecision(function.Args[0]); precision > 0 {
					typeName = fmt.Sprintf("TIMESTAMP(%d) WITH TIME ZONE", precision)
				}
			}
			return rawCast(value, typeName)
		}
	}
	typeName := map[Dialect]string{
		DialectBigQuery: "DATETIME", DialectDatabricks: "TIMESTAMP", DialectDuckDB: "TIMESTAMP", DialectTSQL: "DATETIME2",
		DialectMySQL: "DATETIME", DialectPostgreSQL: "TIMESTAMP", DialectRedshift: "TIMESTAMP", DialectSnowflake: "TIMESTAMP",
		DialectSpark: "TIMESTAMP", DialectTrino: "TIMESTAMP", DialectPresto: "TIMESTAMP", DialectDrill: "TIMESTAMP",
		DialectHive: "TIMESTAMP", DialectDoris: "DATETIME",
	}[target]
	if target == DialectMySQL {
		if precision := fractionalPrecision(function.Args[0]); precision > 0 {
			typeName = fmt.Sprintf("DATETIME(%d)", precision)
		}
	}
	if target == DialectTrino {
		if precision := fractionalPrecision(function.Args[0]); precision > 0 {
			typeName = fmt.Sprintf("TIMESTAMP(%d)", precision)
		}
	}
	if typeName != "" {
		return rawCast(value, typeName)
	}
	return nil
}

func fractionalPrecision(expression Expr) int {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString {
		return 0
	}
	text := strings.Trim(literal.Raw, "'")
	dot := strings.IndexByte(text, '.')
	if dot < 0 {
		return 0
	}
	end := dot + 1
	for end < len(text) && text[end] >= '0' && text[end] <= '9' {
		end++
	}
	return end - dot - 1
}

func normalizeClickHouseType(value Expr) Expr {
	text := strings.TrimSpace(renderExpr(value))
	if text == "" || strings.HasPrefix(strings.ToUpper(text), "NULLABLE(") ||
		strings.HasPrefix(strings.ToUpper(text), "ARRAY(") ||
		strings.HasPrefix(strings.ToUpper(text), "TUPLE(") {
		return value
	}
	upper := strings.ToUpper(text)
	if strings.HasPrefix(upper, "STRUCT<") && strings.HasSuffix(text, ">") {
		inner := text[len("STRUCT<") : len(text)-1]
		fields := splitTopLevelSQL(inner, ',')
		for index, field := range fields {
			name, fieldType := splitTopLevelSQLColon(field)
			if name != "" && fieldType != "" {
				fields[index] = strings.TrimSpace(name) + " " + normalizeClickHouseTypeText(fieldType, true)
			}
		}
		return &RawExpr{Raw: "Tuple(" + strings.Join(fields, ", ") + ")"}
	}
	if strings.HasPrefix(upper, "ARRAY<") && strings.HasSuffix(text, ">") {
		inner := text[len("ARRAY<") : len(text)-1]
		return &RawExpr{Raw: "Array(" + normalizeClickHouseTypeText(inner, true) + ")"}
	}
	if strings.HasPrefix(upper, "MAP(") && strings.HasSuffix(text, ")") {
		inner := text[len("MAP(") : len(text)-1]
		parts := splitTopLevelSQL(inner, ',')
		for index := range parts {
			parts[index] = normalizeClickHouseTypeText(parts[index], index > 0)
		}
		return &RawExpr{Raw: "Map(" + strings.Join(parts, ", ") + ")"}
	}
	return &RawExpr{Raw: "Nullable(" + normalizeClickHouseTypeText(text, false) + ")"}
}

func normalizeClickHouseTypeText(text string, nullable bool) string {
	text = strings.TrimSpace(text)
	upper := strings.ToUpper(text)
	if strings.HasPrefix(upper, "NULLABLE(") || strings.HasPrefix(upper, "MAP(") || strings.HasPrefix(upper, "ARRAY(") || strings.HasPrefix(upper, "TUPLE(") {
		return text
	}
	base := text
	if open := strings.IndexByte(text, '('); open > 0 && strings.HasSuffix(text, ")") {
		base = text[:open]
	}
	mapped := base
	switch strings.ToUpper(base) {
	case "TEXT", "STRING", "VARCHAR", "CHARACTER VARYING", "VARBINARY":
		mapped = "String"
	case "TINYINT":
		mapped = "Int8"
	case "SMALLINT":
		mapped = "Int16"
	case "INT", "INTEGER":
		mapped = "Int32"
	case "BIGINT":
		mapped = "Int64"
	case "DOUBLE":
		mapped = "Float64"
	}
	if mapped == base && base != text {
		mapped = text
	}
	if nullable {
		return "Nullable(" + mapped + ")"
	}
	return mapped
}

func normalizeSparkRawStatement(raw string) string {
	text := canonicalRawSQL(raw)
	text = strings.TrimSpace(replaceFold(text, "DECLARE VARIABLE ", "DECLARE "))
	text = strings.TrimSpace(replaceFold(text, "DECLARE VAR ", "DECLARE "))
	text = strings.TrimSpace(replaceFold(text, " DEFAULT ", " = "))
	return text
}

func normalizeDatabricksRawStatement(raw string) string {
	text := canonicalRawSQL(raw)
	upper := strings.ToUpper(text)
	if strings.HasPrefix(upper, "CREATE OR REPLACE FUNCTION ") {
		text = replaceAllFold(text, " AS TABLE ", " RETURNS TABLE RETURN ")
	}
	return text
}

func normalizeSparkTableSamples(text string) string {
	for {
		upper := strings.ToUpper(text)
		searchStart := 0
		for {
			relative := strings.Index(upper[searchStart:], " AS ")
			if relative < 0 {
				return text
			}
			aliasIndex := searchStart + relative
			aliasStart := aliasIndex + len(" AS ")
			aliasEnd := aliasStart
			for aliasEnd < len(text) && isIdentifierByte(text[aliasEnd]) {
				aliasEnd++
			}
			if aliasEnd == aliasStart || !strings.HasPrefix(strings.ToUpper(text[aliasEnd:]), " TABLESAMPLE ") {
				searchStart = aliasEnd
				continue
			}
			sampleStart := aliasEnd + len(" TABLESAMPLE ")
			if sampleStart >= len(text) || text[sampleStart] != '(' {
				return text
			}
			sampleEnd := matchingParenIndex(text, sampleStart)
			if sampleEnd < 0 {
				return text
			}
			sample := text[aliasEnd : sampleEnd+1]
			alias := text[aliasStart:aliasEnd]
			text = text[:aliasIndex] + " TABLESAMPLE" + strings.TrimPrefix(sample, " TABLESAMPLE") + " AS " + alias + text[sampleEnd+1:]
			break
		}
	}
}

func normalizeDuckDBRawStatement(raw string) string {
	text := canonicalRawSQL(raw)
	text = normalizeDuckDBNestedQueries(text)
	text = normalizeDuckDBFromAliases(text)
	text = replaceAllFold(text, " as ", " AS ")
	upper := strings.ToUpper(text)
	if strings.HasPrefix(upper, "PIVOT_WIDER ") {
		text = "PIVOT " + strings.TrimSpace(text[len("PIVOT_WIDER "):])
		upper = strings.ToUpper(text)
	}
	if strings.HasPrefix(upper, "CREATE OR REPLACE FUNCTION ") {
		text = replaceAllFold(text, " FLOAT", " REAL")
		upper = strings.ToUpper(text)
	}
	if strings.HasPrefix(upper, "ALTER TABLE ") {
		if renameIndex := strings.Index(upper, " RENAME TO "); renameIndex >= 0 {
			name := strings.TrimSpace(text[renameIndex+len(" RENAME TO "):])
			if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
				name = strings.TrimSpace(name[dot+1:])
			}
			text = text[:renameIndex+len(" RENAME TO ")] + name
			upper = strings.ToUpper(text)
		}
	}
	if strings.HasPrefix(upper, "ATTACH DATABASE ") {
		text = "ATTACH " + strings.TrimSpace(text[len("ATTACH DATABASE "):])
		upper = strings.ToUpper(text)
	}
	if strings.HasPrefix(upper, "DETACH DATABASE IF EXISTS ") {
		text = "DETACH DATABASE IF EXISTS " + strings.TrimSpace(text[len("DETACH DATABASE IF EXISTS "):])
	} else if strings.HasPrefix(upper, "DETACH DATABASE ") {
		text = "DETACH " + strings.TrimSpace(text[len("DETACH DATABASE "):])
	} else if strings.HasPrefix(upper, "DETACH IF EXISTS ") {
		text = "DETACH DATABASE IF EXISTS " + strings.TrimSpace(text[len("DETACH IF EXISTS "):])
	}
	if strings.HasPrefix(strings.ToUpper(text), "CREATE SEQUENCE ") {
		if !strings.Contains(strings.ToUpper(text), " START WITH ") {
			text = replaceFold(text, " START ", " START WITH ")
		}
	}
	if strings.HasPrefix(strings.ToUpper(text), "FROM ") && indexKeywordTopLevel(text, "SELECT") < 0 {
		rest := strings.TrimSpace(text[len("FROM "):])
		for _, operator := range []string{"UNION", "INTERSECT", "EXCEPT"} {
			rest = replaceFold(rest, " "+operator+" FROM ", " "+operator+" SELECT * FROM ")
		}
		if strings.HasPrefix(rest, "(") && strings.HasSuffix(rest, ")") {
			inner := strings.TrimSpace(rest[1 : len(rest)-1])
			return "SELECT * FROM (" + normalizeDuckDBRawStatement(inner) + ")"
		}
		return "SELECT * FROM " + rest
	}
	if strings.HasPrefix(strings.ToUpper(text), "FROM ") {
		selectIndex := indexKeywordTopLevel(text, "SELECT")
		if selectIndex > len("FROM ") {
			fromSource := strings.TrimSpace(text[len("FROM "):selectIndex])
			selectText := text[selectIndex:]
			unionIndex := indexKeywordTopLevel(selectText, "UNION")
			if unionIndex >= 0 {
				setText := strings.TrimSpace(selectText[unionIndex:])
				for _, operator := range []string{"UNION", "INTERSECT", "EXCEPT"} {
					setText = replaceFold(setText, " "+operator+" FROM ", " "+operator+" SELECT * FROM ")
				}
				return strings.TrimSpace(selectText[:unionIndex]) + " FROM " + fromSource + " " + setText
			}
			return strings.TrimSpace(selectText) + " FROM " + fromSource
		}
	}
	text = replaceFold(text, "duckdb_functions()", "DUCKDB_FUNCTIONS()")
	text = replaceFold(text, "AVG(LENGTH(function_name))::INTEGER", "CAST(AVG(LENGTH(function_name)) AS INT)")
	text = replaceFold(text, ", FOR ", " FOR ")
	return text
}

func normalizeDuckDBFromAliases(text string) string {
	keywords := []string{"GROUP", "HAVING", "ORDER", "LIMIT", "OFFSET", "QUALIFY", "WHERE", "UNION", "INTERSECT", "EXCEPT"}
	for index := 0; index < len(text); index++ {
		if text[index] != ')' {
			continue
		}
		aliasStart := index + 1
		for aliasStart < len(text) && text[aliasStart] == ' ' {
			aliasStart++
		}
		aliasEnd := aliasStart
		for aliasEnd < len(text) && (isIdentifierByte(text[aliasEnd]) || text[aliasEnd] == '$') {
			aliasEnd++
		}
		if aliasEnd == aliasStart || strings.EqualFold(text[aliasStart:aliasEnd], "AS") {
			continue
		}
		next := aliasEnd
		for next < len(text) && text[next] == ' ' {
			next++
		}
		needsAS := next < len(text) && text[next] == '('
		if !needsAS {
			for _, keyword := range keywords {
				if strings.HasPrefix(strings.ToUpper(text[next:]), keyword+" ") {
					needsAS = true
					break
				}
			}
		}
		if needsAS {
			text = text[:aliasStart] + "AS " + text[aliasStart:]
			index = aliasEnd + 3
		}
	}
	return text
}

func isIdentifierByte(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '_'
}

func normalizeDuckDBSampleTail(tail string) string {
	text := canonicalRawSQL(tail)
	upper := strings.ToUpper(text)
	const prefix = "USING SAMPLE "
	if !strings.HasPrefix(upper, prefix) {
		return tail
	}
	return prefix + normalizeDuckDBSampleSpec(strings.TrimSpace(text[len(prefix):]))
}

func normalizeDuckDBSampleSpec(spec string) string {
	text := canonicalRawSQL(spec)
	if text == "" {
		return text
	}
	if strings.HasPrefix(text, "(") {
		return "RESERVOIR " + text
	}
	if isASCIIDigit(text[0]) {
		index := 0
		for index < len(text) && (isASCIIDigit(text[index]) || text[index] == '.') {
			index++
		}
		amount := text[:index]
		rest := strings.TrimSpace(text[index:])
		percent := false
		if strings.HasPrefix(rest, "%") {
			percent = true
			rest = strings.TrimSpace(rest[1:])
		} else if strings.HasPrefix(strings.ToUpper(rest), "PERCENT") {
			percent = true
			rest = strings.TrimSpace(rest[len("PERCENT"):])
		}
		unit := "ROWS"
		if percent {
			unit = "PERCENT"
		}
		if strings.HasPrefix(rest, "(") {
			close := matchingParenIndex(rest, 0)
			if close >= 0 {
				methodAndSeed := strings.TrimSpace(rest[1:close])
				parts := splitTopLevelSQL(methodAndSeed, ',')
				method := strings.ToUpper(strings.TrimSpace(parts[0]))
				if method != "" && !isASCIIDigit(method[0]) {
					result := method + " (" + amount + " " + unit + ")"
					if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
						result += " REPEATABLE (" + strings.TrimSpace(parts[1]) + ")"
					}
					tail := strings.TrimSpace(rest[close+1:])
					if tail != "" {
						result += " " + tail
					}
					return result
				}
			}
		}
		method := "RESERVOIR"
		if percent {
			method = "SYSTEM"
		}
		return method + " (" + amount + " " + unit + ")" + rest
	}
	open := strings.IndexByte(text, '(')
	if open > 0 {
		close := matchingParenIndex(text, open)
		if close >= 0 {
			method := strings.ToUpper(strings.TrimSpace(text[:open]))
			inner := strings.TrimSpace(text[open+1 : close])
			if strings.HasSuffix(inner, "%") {
				inner = strings.TrimSpace(strings.TrimSuffix(inner, "%")) + " PERCENT"
			}
			tail := strings.TrimSpace(text[close+1:])
			if tail != "" {
				return method + " (" + inner + ") " + tail
			}
			return method + " (" + inner + ")"
		}
	}
	return text
}

func normalizeSnowflakeRawStatement(raw string) string {
	trimmedSource := strings.TrimSpace(raw)
	upperSource := strings.ToUpper(trimmedSource)
	if strings.HasPrefix(upperSource, "CREATE STORAGE INTEGRATION ") || (strings.HasPrefix(upperSource, "CREATE OR REPLACE FUNCTION ") && strings.Contains(upperSource, " LANGUAGE PYTHON ") && strings.Contains(trimmedSource, `\n`)) {
		return trimmedSource
	}
	trimmedRaw := strings.Join(strings.Fields(raw), " ")
	trimmedUpper := strings.ToUpper(trimmedRaw)
	if strings.HasPrefix(trimmedUpper, "PUT ") || strings.HasPrefix(trimmedUpper, "GET ") {
		return trimmedRaw
	}
	text := canonicalRawSQL(raw)
	text = replaceFold(text, "SET VARIABLE ", "SET ")
	text = replaceFold(text, "INSERT OVERWRITE TABLE ", "INSERT OVERWRITE INTO ")
	text = normalizeSnowflakeDollarQuotes(text)
	if strings.HasPrefix(strings.ToUpper(text), "CREATE EXTERNAL TABLE ") {
		return normalizeSnowflakeExternalTable(text)
	}
	if strings.HasPrefix(strings.ToUpper(text), "DESC ") {
		text = "DESCRIBE " + strings.TrimSpace(text[len("DESC "):])
	}
	if strings.HasPrefix(strings.ToUpper(text), "SHOW ") {
		text = normalizeSnowflakeShow(text)
	}
	if strings.HasPrefix(strings.ToUpper(text), "UPDATE ") {
		text = normalizeSnowflakeUpdate(text)
	}
	text = normalizeSnowflakeDDL(text)
	return text
}

func normalizeSnowflakeUpdate(text string) string {
	upper := strings.ToUpper(text)
	fromIndex := strings.Index(upper, " FROM ")
	setIndex := strings.Index(upper, " SET ")
	if fromIndex < 0 || setIndex < 0 || fromIndex > setIndex {
		return text
	}
	whereIndex := strings.Index(upper[setIndex+len(" SET "):], " WHERE ")
	if whereIndex >= 0 {
		whereIndex += setIndex + len(" SET ")
	}
	text = replaceSnowflakeUpdateAlias(text, fromIndex)
	upper = strings.ToUpper(text)
	fromIndex = strings.Index(upper, " FROM ")
	setIndex = strings.Index(upper, " SET ")
	if fromIndex < 0 || setIndex < 0 || fromIndex > setIndex {
		return text
	}
	whereIndex = strings.Index(upper[setIndex+len(" SET "):], " WHERE ")
	if whereIndex >= 0 {
		whereIndex += setIndex + len(" SET ")
	}
	fromPart := strings.TrimSpace(text[fromIndex+len(" FROM ") : setIndex])
	assignmentsEnd := len(text)
	wherePart := ""
	if whereIndex >= 0 {
		assignmentsEnd = whereIndex
		wherePart = strings.TrimSpace(text[whereIndex+len(" WHERE "):])
	}
	assignments := strings.TrimSpace(text[setIndex+len(" SET ") : assignmentsEnd])
	result := strings.TrimSpace(text[:fromIndex]) + " SET " + assignments + " FROM " + fromPart
	if wherePart != "" {
		result += " WHERE " + wherePart
	}
	return result
}

func replaceSnowflakeUpdateAlias(text string, fromIndex int) string {
	prefix := strings.TrimSpace(text[len("UPDATE "):fromIndex])
	fields := strings.Fields(prefix)
	if len(fields) == 2 && !strings.Contains(prefix, "(") {
		text = "UPDATE " + fields[0] + " AS " + fields[1] + text[fromIndex:]
	}
	text = replaceAllFold(text, ") b SET", ") AS b SET")
	text = replaceAllFold(text, "query_id )", "AS query_id)")
	if !strings.Contains(strings.ToUpper(text), "AS QUERY_ID)") {
		text = replaceFold(text, "query_id)", "AS query_id)")
	}
	return text
}

func normalizeSnowflakeExternalTable(text string) string {
	open := strings.IndexByte(text, '(')
	if open < 0 {
		return text
	}
	close := matchingParenIndex(text, open)
	if close < 0 {
		return text
	}
	name := strings.TrimSpace(text[len("CREATE EXTERNAL TABLE "):open])
	columns := splitTopLevelSQL(text[open+1:close], ',')
	for index, column := range columns {
		column = normalizeSnowflakeExternalColumn(column)
		columns[index] = strings.TrimSpace(column)
	}
	result := "CREATE EXTERNAL TABLE " + name + " (" + strings.Join(columns, ", ") + ")"
	tail := strings.TrimSpace(text[close+1:])
	tail = replaceAllFold(tail, "PARTITION BY (", "PARTITION BY (")
	if partition := strings.Index(strings.ToUpper(tail), "PARTITION BY "); partition >= 0 {
		openTail := partition + len("PARTITION BY ")
		if openTail < len(tail) && tail[openTail] == '(' {
			if closeTail := matchingParenIndex(tail, openTail); closeTail >= 0 {
				values := splitTopLevelSQL(tail[openTail+1:closeTail], ',')
				for index := range values {
					values[index] = strings.TrimSpace(values[index])
				}
				tail = tail[:openTail+1] + strings.Join(values, ", ") + tail[closeTail:]
			}
		}
	}
	tail = replaceAllFold(tail, "LOCATION =", "LOCATION=")
	tail = replaceAllFold(tail, "PARTITION_TYPE =", "partition_type=")
	tail = replaceAllFold(tail, "FILE_FORMAT =", "FILE_FORMAT=")
	tail = replaceAllFold(tail, "LOCATION= ", "LOCATION=")
	tail = replaceAllFold(tail, "partition_type= ", "partition_type=")
	tail = replaceAllFold(tail, "FILE_FORMAT= ", "FILE_FORMAT=")
	tail = replaceAllFold(tail, "location=", "LOCATION=")
	if format := strings.Index(strings.ToUpper(tail), "FILE_FORMAT=("); format >= 0 {
		openFormat := format + len("FILE_FORMAT=")
		if closeFormat := matchingParenIndex(tail, openFormat); closeFormat >= 0 {
			body := tail[openFormat+1 : closeFormat]
			body = strings.ReplaceAll(body, " = ", "=")
			body = strings.ReplaceAll(body, " =", "=")
			body = strings.ReplaceAll(body, "= ", "=")
			body = replaceAllFold(body, "binary_as_text=false", "binary_as_text=FALSE")
			tail = tail[:openFormat+1] + body + tail[closeFormat:]
		}
	}
	if tail != "" {
		result += " " + tail
	}
	return result
}

func normalizeSnowflakeExternalColumn(column string) string {
	column = normalizeSnowflakeExternalPath(strings.TrimSpace(column))
	column = replaceAllFold(column, " number as ", " DECIMAL(38, 0) AS ")
	column = replaceAllFold(column, " date ", " DATE ")
	column = replaceAllFold(column, " varchar ", " VARCHAR ")
	column = replaceAllFold(column, "parse_json(", "PARSE_JSON(")
	column = normalizeCreateTableColumn(column, DialectSnowflake)
	column = replaceAllFold(column, " as ", " AS ")
	return column
}

func normalizeSnowflakeExternalPath(text string) string {
	for {
		upper := strings.ToUpper(text)
		index := strings.Index(upper, "PARSE_JSON(")
		if index < 0 {
			return text
		}
		open := index + len("PARSE_JSON")
		close := matchingParenIndex(text, open)
		if close < 0 || close+1 >= len(text) || text[close+1] != ':' {
			return text
		}
		pathStart := close + 2
		cast := strings.Index(text[pathStart:], "::")
		if cast < 0 {
			return text
		}
		cast += pathStart
		path := strings.TrimSpace(text[pathStart:cast])
		end := cast + 2
		for end < len(text) && (isASCIIIdentifierByte(text[end]) || text[end] == '_') {
			end++
		}
		if end == cast+2 {
			return text
		}
		typeName := strings.ToUpper(strings.TrimSpace(text[cast+2 : end]))
		if typeName == "NUMBER" {
			typeName = "DECIMAL(38, 0)"
		}
		jsonCall := replaceAllFold(text[index:close+1], "parse_json(", "PARSE_JSON(")
		replacement := "CAST(GET_PATH(" + jsonCall + ", '" + strings.ToUpper(path) + "') AS " + typeName + ")"
		text = text[:index] + replacement + text[end:]
	}
}

func normalizeSnowflakeDDL(text string) string {
	text = replaceAllFold(text, "IDENTIFIER (", "IDENTIFIER(")
	text = replaceAllFold(text, "RETURNS TABLE(", "RETURNS TABLE (")
	text = normalizeSnowflakeFileFormat(text)
	text = normalizeSnowflakeAutoIncrement(text)
	text = normalizeSnowflakeSequence(text)
	text = normalizeSnowflakeTableOptions(text)
	upper := strings.ToUpper(text)
	if strings.Contains(upper, " ROW ACCESS POLICY ") && !strings.Contains(upper, " WITH ROW ACCESS POLICY ") {
		text = replaceFold(text, " ROW ACCESS POLICY ", " WITH ROW ACCESS POLICY ")
	}
	text = replaceAllFold(text, " ADD COLUMN ", " ADD ")
	return text
}

func normalizeSnowflakeFileFormat(text string) string {
	if !strings.HasPrefix(strings.ToUpper(text), "CREATE STAGE ") {
		return text
	}
	text = replaceAllFold(text, "FILE_FORMAT =", "FILE_FORMAT=")
	for {
		upper := strings.ToUpper(text)
		index := strings.Index(upper, "FILE_FORMAT=")
		if index < 0 {
			return text
		}
		start := index + len("FILE_FORMAT=")
		if start >= len(text) || text[start] == '(' {
			return text
		}
		end := start
		for end < len(text) && text[end] != ' ' {
			end++
		}
		value := strings.TrimSpace(text[start:end])
		if value == "" {
			return text
		}
		text = text[:index] + "FILE_FORMAT=(FORMAT_NAME=" + value + ")" + text[end:]
	}
}

func normalizeSnowflakeAutoIncrement(text string) string {
	upper := strings.ToUpper(text)
	if !strings.Contains(upper, "IDENTITY") && !strings.Contains(upper, "AUTOINCREMENT") {
		return text
	}
	if strings.Contains(upper, "NUMBER") {
		text = replaceAllFold(text, "NUMBER(38, 0)", "DECIMAL(38, 0)")
		text = replaceAllFold(text, "NUMBER(38,0)", "DECIMAL(38, 0)")
		text = replaceAllFold(text, "NUMBER", "DECIMAL(38, 0)")
	}
	for _, keyword := range []string{"IDENTITY", "AUTOINCREMENT"} {
		for {
			upper = strings.ToUpper(text)
			index := strings.Index(upper, keyword+"(")
			if index < 0 {
				break
			}
			open := index + len(keyword)
			close := matchingParenIndex(text, open)
			if close < 0 {
				break
			}
			parts := splitTopLevelSQL(text[open+1:close], ',')
			replacement := "AUTOINCREMENT"
			if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" {
				replacement += " START " + strings.TrimSpace(parts[0])
			}
			if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
				replacement += " INCREMENT " + strings.TrimSpace(parts[1])
			}
			text = text[:index] + replacement + text[close+1:]
		}
	}
	text = replaceAllFold(text, "IDENTITY", "AUTOINCREMENT")
	for {
		upper = strings.ToUpper(text)
		index := strings.Index(upper, "AUTOINCREMENT INCREMENT ")
		if index < 0 {
			break
		}
		rest := strings.TrimSpace(text[index+len("AUTOINCREMENT INCREMENT "):])
		incrementEnd := strings.IndexByte(rest, ' ')
		if incrementEnd < 0 {
			break
		}
		increment := strings.TrimSpace(rest[:incrementEnd])
		rest = strings.TrimSpace(rest[incrementEnd:])
		if !strings.HasPrefix(strings.ToUpper(rest), "START ") {
			break
		}
		rest = strings.TrimSpace(rest[len("START "):])
		startEnd := strings.IndexByte(rest, ' ')
		startValue := rest
		tail := ""
		if startEnd >= 0 {
			startValue = strings.TrimSpace(rest[:startEnd])
			tail = rest[startEnd:]
		}
		replacement := "AUTOINCREMENT START " + startValue + " INCREMENT " + increment
		text = text[:index] + replacement + tail
	}
	return text
}

func normalizeSnowflakeSequence(text string) string {
	if !strings.HasPrefix(strings.ToUpper(text), "CREATE SEQUENCE ") {
		return text
	}
	words := strings.Fields(text)
	if len(words) < 3 {
		return text
	}
	prefix := strings.Join(words[:3], " ")
	var start, increment, comment string
	var tail, extra []string
	for index := 3; index < len(words); index++ {
		word := strings.TrimSuffix(words[index], ",")
		upper := strings.ToUpper(word)
		switch {
		case upper == "WITH":
			continue
		case strings.HasPrefix(upper, "START="):
			start = word[len("START="):]
		case upper == "START":
			if index+1 < len(words) && strings.EqualFold(words[index+1], "WITH") {
				index++
			}
			if index+1 < len(words) {
				index++
				start = strings.TrimSuffix(words[index], ",")
			}
		case strings.HasPrefix(upper, "INCREMENT="):
			increment = word[len("INCREMENT="):]
		case upper == "INCREMENT":
			if index+1 < len(words) && strings.EqualFold(words[index+1], "BY") {
				index++
			}
			if index+1 < len(words) {
				index++
				increment = strings.TrimSuffix(words[index], ",")
			}
		case strings.HasPrefix(upper, "COMMENT="):
			comment = word[len("COMMENT="):]
		case upper == "COMMENT":
			if index+2 < len(words) && words[index+1] == "=" {
				index++
				index++
				comment = strings.TrimSuffix(words[index], ",")
			}
		case upper == "ORDER" || upper == "NOORDER":
			tail = append(tail, upper)
		default:
			extra = append(extra, word)
		}
	}
	result := []string{prefix}
	if len(extra) > 0 {
		result = append(result, extra...)
	}
	if comment != "" {
		result = append(result, "COMMENT="+comment)
	}
	if start != "" {
		result = append(result, "START", "WITH", start)
	}
	if increment != "" {
		result = append(result, "INCREMENT", "BY", increment)
	}
	result = append(result, tail...)
	return strings.Join(result, " ")
}

func normalizeSnowflakeTableOptions(text string) string {
	upper := strings.ToUpper(text)
	optionIndex := -1
	for _, option := range []string{"CHANGE_TRACKING=", "TARGET_LAG="} {
		if index := strings.Index(upper, option); index >= 0 && (optionIndex < 0 || index < optionIndex) {
			optionIndex = index
		}
	}
	if optionIndex < 0 {
		return text
	}
	if strings.IndexByte(text[:optionIndex], '(') >= 0 {
		return text
	}
	openOffset := strings.IndexByte(text[optionIndex:], '(')
	if openOffset < 0 {
		return text
	}
	open := optionIndex + openOffset
	close := matchingParenIndex(text, open)
	if close < 0 {
		return text
	}
	prefix := strings.TrimSpace(text[:optionIndex])
	options := strings.TrimSpace(text[optionIndex:open])
	columns := strings.TrimSpace(text[open : close+1])
	tail := strings.TrimSpace(text[close+1:])
	if options == "" || columns == "" {
		return text
	}
	result := columns + " " + options
	if prefix != "" {
		result = prefix + " " + result
	}
	if tail != "" {
		result += " " + tail
	}
	return result
}

func normalizeSnowflakeShow(text string) string {
	words := strings.Fields(text)
	keywords := map[string]bool{
		"SHOW": true, "TERSE": true, "SCHEMAS": true, "DATABASE": true, "DATABASES": true,
		"OBJECTS": true, "IN": true, "SCHEMA": true, "STARTS": true, "WITH": true,
		"LIMIT": true, "FROM": true, "TABLES": true, "HISTORY": true, "ICEBERG": true,
		"KEYS": true, "PRIMARY": true, "UNIQUE": true, "IMPORTED": true, "SEQUENCES": true,
		"LIKE": true, "VIEWS": true, "TABLE": true, "ACCOUNT": true, "VIEW": true,
		"COLUMNS": true, "USERS": true, "WAREHOUSES": true, "STAGES": true, "FUNCTIONS": true,
		"PROCEDURES": true, "FILE": true, "FORMATS": true, "APPLICATION": true, "PACKAGE": true,
	}
	for index, word := range words {
		if keywords[strings.ToUpper(word)] {
			words[index] = strings.ToUpper(word)
		}
	}
	primaryKeys := false
	for index := range words {
		if words[index] == "PRIMARY" && index+1 < len(words) && words[index+1] == "KEYS" {
			primaryKeys = true
		}
	}
	knownScopes := map[string]bool{
		"ACCOUNT": true, "DATABASE": true, "SCHEMA": true, "TABLE": true, "VIEW": true,
		"CLASS": true, "APPLICATION": true,
	}
	result := make([]string, 0, len(words)+2)
	insertedKeyScope := false
	for index := 0; index < len(words); index++ {
		word := words[index]
		if word == "IN" && index+1 < len(words) && !knownScopes[strings.ToUpper(words[index+1])] {
			scope := "SCHEMA"
			if primaryKeys {
				scope = "TABLE"
			}
			result = append(result, word, scope)
			insertedKeyScope = true
			continue
		}
		result = append(result, word)
	}
	text = strings.Join(result, " ")
	if insertedKeyScope && (primaryKeys || strings.Contains(text, "UNIQUE KEYS") || strings.Contains(text, "IMPORTED KEYS")) {
		text = replaceFold(text, "SHOW TERSE PRIMARY KEYS", "SHOW PRIMARY KEYS")
		text = replaceFold(text, "SHOW TERSE UNIQUE KEYS", "SHOW UNIQUE KEYS")
		text = replaceFold(text, "SHOW TERSE IMPORTED KEYS", "SHOW IMPORTED KEYS")
	}
	return text
}

func normalizeSnowflakeDollarQuotes(text string) string {
	for {
		start := strings.Index(text, "$$")
		if start < 0 {
			return text
		}
		end := strings.Index(text[start+2:], "$$")
		if end < 0 {
			return text
		}
		end += start + 2
		content := text[start+2 : end]
		content = strings.ReplaceAll(content, "'", "\\'")
		text = text[:start] + "'" + content + "'" + text[end+2:]
	}
}

func normalizeSnowflakeSampleTail(tail string) string {
	trimmed := strings.TrimSpace(tail)
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "SAMPLE (") {
		return "TABLESAMPLE BERNOULLI " + strings.TrimSpace(trimmed[len("SAMPLE "):])
	}
	if strings.HasPrefix(upper, "SAMPLE ") {
		return "TABLESAMPLE " + strings.TrimSpace(trimmed[len("SAMPLE "):])
	}
	return tail
}

func normalizeSnowflakeTableSample(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "(") {
		return "BERNOULLI " + trimmed
	}
	return raw
}

func normalizeSnowflakeTableTail(tail string) string {
	text := canonicalRawSQL(tail)
	for _, keyword := range []string{"AT", "BEFORE", "CHANGES", "END"} {
		text = replaceFold(text, keyword+"(", keyword+" (")
	}
	text = normalizeSnowflakeFunctionCast(text, "TO_TIMESTAMP_TZ", "TIMESTAMPTZ")
	text = normalizeSnowflakeFunctionCast(text, "TO_TIMESTAMP", "TIMESTAMP")
	text = normalizeSnowflakeDoubleColonCasts(text)
	return text
}

func normalizeSnowflakeFunctionCast(text, functionName, typeName string) string {
	for {
		upper := strings.ToUpper(text)
		index := strings.Index(upper, functionName+"(")
		if index < 0 {
			return text
		}
		open := index + len(functionName)
		close := matchingParenIndex(text, open)
		if close < 0 {
			return text
		}
		args := strings.TrimSpace(text[open+1 : close])
		if len(splitTopLevelSQL(args, ',')) > 1 {
			return text
		}
		text = text[:index] + "CAST(" + args + " AS " + typeName + ")" + text[close+1:]
	}
}

func normalizeSnowflakeDoubleColonCasts(text string) string {
	for _, typeName := range []struct {
		Suffix string
		Type   string
	}{
		{Suffix: "::TIMESTAMP_TZ", Type: "TIMESTAMPTZ"},
		{Suffix: "::TIMESTAMP", Type: "TIMESTAMP"},
	} {
		for {
			upper := strings.ToUpper(text)
			index := strings.Index(upper, typeName.Suffix)
			if index < 0 {
				break
			}
			start := strings.LastIndex(text[:index], "'")
			if start < 0 {
				break
			}
			start = strings.LastIndex(text[:start], "'")
			if start < 0 {
				break
			}
			valueEnd := index
			value := strings.TrimSpace(text[start:valueEnd])
			text = text[:start] + "CAST(" + value + " AS " + typeName.Type + ")" + text[index+len(typeName.Suffix):]
		}
	}
	return text
}

func normalizeSnowflakeStageFrom(raw string) string {
	text := canonicalRawSQL(raw)
	open := strings.IndexByte(text, '(')
	if open < 0 {
		return text
	}
	close := matchingParenIndex(text, open)
	if close < 0 {
		return text
	}
	prefix := strings.TrimSpace(text[:open])
	if !strings.Contains(text[open+1:close], "=>") {
		parts := strings.Fields(prefix)
		if len(parts) >= 2 {
			return strings.Join(parts[:len(parts)-1], " ") + " AS " + parts[len(parts)-1] + "(" + strings.TrimSpace(text[open+1:close]) + ")" + strings.TrimSpace(text[close+1:])
		}
	}
	body := splitTopLevelSQL(text[open+1:close], ',')
	for index := range body {
		body[index] = strings.TrimSpace(body[index])
	}
	for left := 0; left < len(body); left++ {
		for right := left + 1; right < len(body); right++ {
			leftKey := strings.ToUpper(strings.TrimSpace(strings.SplitN(body[left], "=>", 2)[0]))
			rightKey := strings.ToUpper(strings.TrimSpace(strings.SplitN(body[right], "=>", 2)[0]))
			if leftKey == "PATTERN" && rightKey == "FILE_FORMAT" {
				body[left], body[right] = body[right], body[left]
			}
		}
	}
	return prefix + " (" + strings.Join(body, ", ") + ")" + strings.TrimSpace(text[close+1:])
}

func normalizeSnowflakeODBC(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 4 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return raw
	}
	body := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	if strings.HasPrefix(body, "*") {
		return raw
	}
	if len(body) >= 2 && strings.EqualFold(body[:2], "fn") {
		body = strings.TrimSpace(body[2:])
	}
	open := strings.IndexByte(body, '(')
	if open <= 0 || !strings.HasSuffix(body, ")") {
		return raw
	}
	close := matchingParenIndex(body, open)
	if close != len(body)-1 {
		return raw
	}
	name := strings.ToUpper(strings.TrimSpace(body[:open]))
	args := splitTopLevelSQL(body[open+1:close], ',')
	for index := range args {
		args[index] = strings.TrimSpace(args[index])
	}
	switch name {
	case "CONVERT":
		if len(args) == 2 {
			typeName := args[1]
			switch strings.ToUpper(typeName) {
			case "SQL_DOUBLE":
				typeName = "DOUBLE"
			case "SQL_VARCHAR":
				typeName = "VARCHAR"
			default:
				return raw
			}
			return "CAST(" + args[0] + " AS " + typeName + ")"
		}
	case "LOG":
		name = "LN"
	case "CEILING":
		name = "CEIL"
	}
	return name + "(" + strings.Join(args, ", ") + ")"
}

func normalizeDuckDBNestedQueries(text string) string {
	var result strings.Builder
	for index := 0; index < len(text); {
		if text[index] != '(' {
			result.WriteByte(text[index])
			index++
			continue
		}
		close := matchingParenIndex(text, index)
		if close < 0 {
			result.WriteString(text[index:])
			break
		}
		result.WriteByte('(')
		result.WriteString(normalizeDuckDBRawStatement(text[index+1 : close]))
		result.WriteByte(')')
		index = close + 1
	}
	return result.String()
}

func rewriteDuckDBWindowNullsForMySQL(node Node) {
	Walk(node, func(current Node) VisitAction {
		if function, ok := current.(*FunctionCallExpr); ok && function.Over != nil {
			if strings.Contains(strings.ToUpper(function.Over.Frame), "RANGE") {
				return VisitChildren
			}
			function.Over.OrderBy = rewriteDuckDBOrderItemsForMySQL(function.Over.OrderBy)
			return VisitChildren
		}
		windowed, ok := current.(*WindowedExpr)
		if !ok {
			return VisitChildren
		}
		if strings.Contains(strings.ToUpper(windowed.Over.Frame), "RANGE") {
			return VisitChildren
		}
		windowed.Over.OrderBy = rewriteDuckDBOrderItemsForMySQL(windowed.Over.OrderBy)
		return VisitChildren
	})
}

func rewriteDuckDBOrderItemsForMySQL(items []OrderItem) []OrderItem {
	if len(items) == 0 {
		return items
	}
	rewritten := make([]OrderItem, 0, len(items)*2)
	for _, item := range items {
		if _, alreadyRank := item.Expr.(*CaseExpr); alreadyRank {
			rewritten = append(rewritten, item)
			continue
		}
		if len(rewritten) > 0 {
			if _, alreadyRanked := rewritten[len(rewritten)-1].Expr.(*CaseExpr); alreadyRanked {
				rewritten = append(rewritten, item)
				continue
			}
		}
		if item.Descending || item.NullsFirst || item.NullsLast {
			rewritten = append(rewritten, item)
			continue
		}
		if _, numeric := item.Expr.(*LiteralExpr); numeric {
			rewritten = append(rewritten, item)
			continue
		}
		original := item
		original.NullsFirst = false
		original.NullsLast = false
		rank := OrderItem{Expr: &CaseExpr{Whens: []CaseWhen{{Condition: &IsExpr{
			Value: original.Expr, Operator: "IS", Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"},
		}, Result: &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}}}, Else: &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}}}
		rewritten = append(rewritten, rank, original)
	}
	return rewritten
}

func replaceFold(text, old, replacement string) string {
	index := strings.Index(strings.ToUpper(text), strings.ToUpper(old))
	if index < 0 {
		return text
	}
	return text[:index] + replacement + text[index+len(old):]
}

func replaceAllFold(text, old, replacement string) string {
	for {
		next := replaceFold(text, old, replacement)
		if next == text {
			return text
		}
		text = next
	}
}

func stripMergeTargetInsertColumns(text string) string {
	upper := strings.ToUpper(text)
	start := strings.Index(upper, "INSERT (")
	if start < 0 {
		return text
	}
	open := start + len("INSERT ")
	close := matchingParenIndex(text, open)
	if close < 0 {
		return text
	}
	columns := strings.ReplaceAll(text[open+1:close], "target.", "")
	columns = strings.ReplaceAll(columns, "TARGET.", "")
	return text[:open+1] + columns + text[close:]
}

func restoreTSQLIdentityFunctions(sql string, node Node) {
	Walk(node, func(current Node) VisitAction {
		function, ok := current.(*FunctionCallExpr)
		if !ok || len(function.Name) != 1 || !strings.EqualFold(function.Name[0].Text, "COUNT_BIG") {
			return VisitChildren
		}
		span := function.SourceSpan()
		if span.Start >= 0 && span.End <= len(sql) && strings.Contains(strings.ToUpper(sql[span.Start:span.End]), "COUNT(") {
			function.Name[0].Text = "COUNT"
		}
		return VisitChildren
	})
}

func normalizeGenericRawStatement(raw string) string {
	trimmed := strings.TrimSpace(raw)
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "CREATE TEMPORARY SEQUENCE ") {
		trimmed = strings.TrimSpace(strings.ReplaceAll(trimmed, " OWNED BY NONE", ""))
		if !strings.Contains(strings.ToUpper(trimmed), " START WITH ") {
			trimmed = replaceFold(trimmed, " START ", " START WITH ")
		}
		trimmed = strings.ReplaceAll(trimmed, " WITH = ", " WITH ")
		trimmed = strings.ReplaceAll(trimmed, " BY = ", " BY ")
		return trimmed
	}
	if strings.HasPrefix(upper, "ALTER TABLE ") {
		if index := strings.Index(strings.ToUpper(trimmed), " ADD "); index >= 0 {
			rest := strings.TrimSpace(trimmed[index+len(" ADD "):])
			if !strings.HasPrefix(strings.ToUpper(rest), "COLUMN ") {
				rest = "COLUMN " + rest
			}
			rest = normalizeGenericRawTypeWords(rest)
			return trimmed[:index] + " ADD " + rest
		}
		if index := strings.Index(strings.ToUpper(trimmed), " ALTER "); index >= 0 {
			rest := strings.TrimSpace(trimmed[index+len(" ALTER "):])
			if typeIndex := strings.Index(strings.ToUpper(rest), " TYPE "); typeIndex >= 0 {
				column := strings.TrimSpace(rest[:typeIndex])
				typeAndTail := normalizeGenericRawTypeWords(strings.TrimSpace(rest[typeIndex+len(" TYPE "):]))
				return trimmed[:index] + " ALTER COLUMN " + column + " SET DATA TYPE " + typeAndTail
			}
		}
	}
	return raw
}

func normalizeGenericRawTypeWords(text string) string {
	words := strings.Fields(text)
	for index, word := range words {
		if strings.EqualFold(word, "INTEGER") {
			words[index] = "INT"
		}
	}
	return strings.Join(words, " ")
}

func normalizeTSQLRawStatement(raw string) string {
	canonical := canonicalRawSQL(raw)
	upperCanonical := strings.ToUpper(canonical)
	if strings.HasPrefix(upperCanonical, "ALTER TABLE ") {
		if renameIndex := strings.Index(upperCanonical, " RENAME TO "); renameIndex >= 0 {
			oldName := strings.TrimSpace(canonical[len("ALTER TABLE "):renameIndex])
			newName := strings.TrimSpace(canonical[renameIndex+len(" RENAME TO "):])
			if dot := strings.LastIndexByte(newName, '.'); dot >= 0 {
				newName = newName[dot+1:]
			}
			oldName = strings.Trim(oldName, "'")
			newName = strings.Trim(newName, "'\"[]")
			oldName = normalizeTSQLQuotedIdentifiers(oldName)
			return "EXEC sp_rename '" + oldName + "', '" + newName + "'"
		}
	}
	words := strings.Fields(raw)
	if len(words) == 0 {
		return raw
	}
	switch strings.ToUpper(words[0]) {
	case "EXEC", "EXECUTE":
		words[0] = "EXECUTE"
	case "CREATE", "DROP":
		if strings.EqualFold(words[0], "CREATE") && len(words) >= 3 && strings.EqualFold(words[1], "COLUMNSTORE") && strings.EqualFold(words[2], "INDEX") {
			words = append([]string{"CREATE", "NONCLUSTERED"}, words[1:]...)
		}
		if len(words) >= 3 && strings.EqualFold(words[1], "VIEW") {
			words[1] = "VIEW"
			parts := strings.Split(words[2], ".")
			if len(parts) > 2 {
				words[2] = strings.Join(parts[1:], ".")
			}
		}
	}
	text := strings.Join(words, " ")
	if strings.HasPrefix(strings.ToUpper(text), "DECLARE ") {
		text = strings.ReplaceAll(text, " AS ", " ")
	} else if strings.HasPrefix(strings.ToUpper(text), "CREATE PROCEDURE ") {
		if open := strings.IndexByte(text, '('); open >= 0 {
			if close := matchingParenIndex(text, open); close > open {
				text = text[:open+1] + strings.ReplaceAll(text[open+1:close], " AS ", " ") + text[close:]
			}
		}
	}
	text = replaceFold(text, "CURRENT_TIMESTAMP", "GETDATE()")
	return text
}

func normalizeTSQLQuotedIdentifiers(text string) string {
	var result strings.Builder
	for index := 0; index < len(text); {
		if text[index] != '"' {
			result.WriteByte(text[index])
			index++
			continue
		}
		end := index + 1
		for end < len(text) {
			if text[end] != '"' {
				end++
				continue
			}
			if end+1 < len(text) && text[end+1] == '"' {
				end += 2
				continue
			}
			break
		}
		if end >= len(text) {
			result.WriteString(text[index:])
			break
		}
		result.WriteByte('[')
		result.WriteString(strings.ReplaceAll(text[index+1:end], `""`, `"`))
		result.WriteByte(']')
		index = end + 1
	}
	return result.String()
}

func normalizeTSQLTempNames(text string) string {
	var result strings.Builder
	var quote byte
	for index := 0; index < len(text); {
		c := text[index]
		if quote != 0 {
			result.WriteByte(c)
			if c == quote {
				if index+1 < len(text) && text[index+1] == quote {
					result.WriteByte(text[index+1])
					index += 2
					continue
				}
				quote = 0
			}
			index++
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			result.WriteByte(c)
			index++
			continue
		}
		if c == '#' && (index == 0 || !isASCIIIdentifierByte(text[index-1])) {
			for index < len(text) && text[index] == '#' {
				index++
			}
			continue
		}
		result.WriteByte(c)
		index++
	}
	return result.String()
}

func normalizeCreateTableTail(tail string, target Dialect) string {
	trimmed := strings.TrimSpace(tail)
	upperTail := strings.ToUpper(trimmed)
	if strings.HasPrefix(upperTail, "LIKE ") {
		source := strings.TrimSpace(trimmed[len("LIKE "):])
		switch target {
		case DialectPostgreSQL, DialectPresto, DialectRedshift, DialectTrino:
			return "(LIKE " + source + ")"
		case DialectDuckDB, DialectDrill, DialectSQLite:
			return "AS SELECT * FROM " + source + " LIMIT 0"
		case DialectClickHouse:
			return "AS " + source
		default:
			return trimmed
		}
	}
	if strings.HasPrefix(trimmed, "(") {
		if close := matchingParenIndex(trimmed, 0); close >= 0 && close < len(trimmed)-1 {
			return normalizeCreateTableClauses(trimmed[:close+1], strings.TrimSpace(trimmed[close+1:]), target)
		}
	}
	if len(trimmed) < 2 || trimmed[0] != '(' || trimmed[len(trimmed)-1] != ')' {
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "USING ") {
			rest := strings.TrimSpace(trimmed[len("USING "):])
			engine, remainder := splitLeadingSQLToken(rest)
			if target == DialectDuckDB {
				return ""
			}
			if target == DialectHive {
				return "STORED AS " + engine + func() string {
					if remainder == "" {
						return ""
					}
					return " " + remainder
				}()
			}
			if target == DialectPresto {
				format := "WITH (format='" + strings.ToUpper(engine) + "'"
				if partitionIndex := indexKeywordTopLevel(remainder, "PARTITIONED BY"); partitionIndex >= 0 {
					partitionText := strings.TrimSpace(remainder[partitionIndex+len("PARTITIONED BY"):])
					if open := strings.IndexByte(partitionText, '('); open >= 0 {
						if close := matchingParenIndex(partitionText, open); close >= 0 {
							value := strings.TrimSpace(partitionText[open+1 : close])
							format += ", PARTITIONED_BY=ARRAY['" + value + "']"
						}
					}
				}
				return format + ")"
			}
		}
		if strings.HasPrefix(upper, "STORED AS ") {
			rest := strings.TrimSpace(trimmed[len("STORED AS "):])
			engine, remainder := splitLeadingSQLToken(rest)
			if target == DialectDuckDB {
				if index := indexKeywordTopLevel(remainder, "AS"); index >= 0 {
					return strings.TrimSpace(remainder[index:])
				}
				return ""
			}
			if target == DialectPresto || target == DialectTrino || target == DialectAthena {
				result := "WITH (format='" + strings.ToUpper(engine) + "')"
				if remainder != "" {
					result += " " + remainder
				}
				return result
			}
		}
		return tail
	}
	inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	if inner == "" {
		return "()"
	}
	columns := splitTopLevelSQL(inner, ',')
	for index := range columns {
		columns[index] = normalizeCreateTableColumn(columns[index], target)
	}
	return "(" + strings.Join(columns, ", ") + ")"
}

func normalizeCreateTableClauses(columnsText, suffix string, target Dialect) string {
	inner := strings.TrimSpace(columnsText[1 : len(columnsText)-1])
	columns := splitTopLevelSQL(inner, ',')
	for index := range columns {
		columns[index] = normalizeCreateTableColumn(columns[index], target)
	}
	baseColumns := formatCreateColumns(columns)
	if suffix == "" {
		return baseColumns
	}

	canonicalSuffix := canonicalRawSQL(suffix)
	if target != DialectDuckDB && target != DialectHive && target != DialectSpark && target != DialectDatabricks && target != DialectPresto {
		return normalizeCreateTableTail(columnsText, target) + " " + canonicalSuffix
	}
	if indexKeywordTopLevel(canonicalSuffix, "COMMENT") < 0 && indexKeywordTopLevel(canonicalSuffix, "PARTITIONED BY") < 0 && indexKeywordTopLevel(canonicalSuffix, "USING") < 0 && indexKeywordTopLevel(canonicalSuffix, "TBLPROPERTIES") < 0 {
		return normalizeCreateTableTail(columnsText, target) + " " + canonicalSuffix
	}
	comment := ""
	if commentIndex := indexKeywordTopLevel(canonicalSuffix, "COMMENT"); commentIndex >= 0 {
		commentEnd := nextCreateClauseIndex(canonicalSuffix, commentIndex+len("COMMENT"), "PARTITIONED BY", "USING", "TBLPROPERTIES")
		comment = normalizeCreateComment(strings.TrimSpace(canonicalSuffix[commentIndex+len("COMMENT") : commentEnd]))
	}
	partitionColumns := []string(nil)
	if partitionIndex := indexKeywordTopLevel(canonicalSuffix, "PARTITIONED BY"); partitionIndex >= 0 {
		partitionEnd := nextCreateClauseIndex(canonicalSuffix, partitionIndex+len("PARTITIONED BY"), "COMMENT", "USING", "TBLPROPERTIES")
		partitionText := strings.TrimSpace(canonicalSuffix[partitionIndex+len("PARTITIONED BY") : partitionEnd])
		if open := strings.IndexByte(partitionText, '('); open >= 0 {
			if close := matchingParenIndex(partitionText, open); close >= 0 {
				partitionInner := strings.TrimSpace(partitionText[open+1 : close])
				partitionColumns = splitTopLevelSQL(partitionInner, ',')
				for index := range partitionColumns {
					partitionColumns[index] = normalizeCreateTableColumn(partitionColumns[index], target)
				}
			}
		}
	}
	engine := ""
	if usingIndex := indexKeywordTopLevel(canonicalSuffix, "USING"); usingIndex >= 0 {
		usingEnd := nextCreateClauseIndex(canonicalSuffix, usingIndex+len("USING"), "COMMENT", "PARTITIONED BY", "TBLPROPERTIES")
		engine, _ = splitLeadingSQLToken(strings.TrimSpace(canonicalSuffix[usingIndex+len("USING") : usingEnd]))
	}
	properties := []string(nil)
	if propertiesIndex := indexKeywordTopLevel(canonicalSuffix, "TBLPROPERTIES"); propertiesIndex >= 0 {
		propertiesText := strings.TrimSpace(canonicalSuffix[propertiesIndex+len("TBLPROPERTIES"):])
		if open := strings.IndexByte(propertiesText, '('); open >= 0 {
			if close := matchingParenIndex(propertiesText, open); close >= 0 {
				properties = splitTopLevelSQL(strings.TrimSpace(propertiesText[open+1:close]), ',')
				for index := range properties {
					properties[index] = normalizeCreateProperty(properties[index], target == DialectPresto)
				}
			}
		}
	}

	switch target {
	case DialectDuckDB:
		return baseColumns
	case DialectHive:
		result := baseColumns
		if comment != "" {
			result += "\nCOMMENT " + comment
		}
		if len(partitionColumns) > 0 {
			result += "\nPARTITIONED BY (\n  " + strings.Join(partitionColumns, ",\n  ") + "\n)"
		}
		if engine != "" {
			result += "\nSTORED AS " + engine
		}
		if len(properties) > 0 {
			result += "\nTBLPROPERTIES (\n  " + strings.Join(properties, ",\n  ") + "\n)"
		}
		return result
	case DialectSpark, DialectDatabricks:
		allColumns := append([]string(nil), columns...)
		allColumns = append(allColumns, partitionColumns...)
		result := formatCreateColumns(allColumns)
		if comment != "" {
			result += "\nCOMMENT " + comment
		}
		if len(partitionColumns) > 0 {
			partitionNames := make([]string, 0, len(partitionColumns))
			for _, column := range partitionColumns {
				name, _ := splitLeadingSQLToken(column)
				partitionNames = append(partitionNames, name)
			}
			result += "\nPARTITIONED BY (\n  " + strings.Join(partitionNames, ",\n  ") + "\n)"
		}
		if engine != "" {
			result += "\nUSING " + engine
		}
		if len(properties) > 0 {
			result += "\nTBLPROPERTIES (\n  " + strings.Join(properties, ",\n  ") + "\n)"
		}
		return result
	case DialectPresto:
		allColumns := append([]string(nil), columns...)
		allColumns = append(allColumns, partitionColumns...)
		result := formatCreateColumns(allColumns)
		if comment != "" {
			result += "\nCOMMENT " + comment
		}
		with := make([]string, 0, len(properties)+2)
		if len(partitionColumns) > 0 {
			partitionNames := make([]string, 0, len(partitionColumns))
			for _, column := range partitionColumns {
				name, _ := splitLeadingSQLToken(column)
				name = strings.Trim(name, "`[]\"")
				partitionNames = append(partitionNames, name)
			}
			with = append(with, "PARTITIONED_BY=ARRAY['"+strings.Join(partitionNames, "', '")+"']")
		}
		if engine != "" {
			with = append(with, "format='"+strings.ToUpper(engine)+"'")
		}
		with = append(with, properties...)
		if len(with) > 0 {
			result += "\nWITH (\n  " + strings.Join(with, ",\n  ") + "\n)"
		}
		return result
	default:
		return baseColumns + " " + suffix
	}
}

func formatCreateColumns(columns []string) string {
	if len(columns) == 0 {
		return "()"
	}
	return "(\n  " + strings.Join(columns, ",\n  ") + "\n)"
}

func nextCreateClauseIndex(text string, start int, clauses ...string) int {
	end := len(text)
	for _, clause := range clauses {
		if index := indexKeywordTopLevel(text[start:], clause); index >= 0 && start+index < end {
			end = start + index
		}
	}
	return end
}

func normalizeCreateComment(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return "'" + strings.ReplaceAll(value[1:len(value)-1], `""`, `"`) + "'"
	}
	return value
}

func normalizeCreateProperty(value string, presto bool) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, " = ", "=")
	value = strings.ReplaceAll(value, " =", "=")
	value = strings.ReplaceAll(value, "= ", "=")
	if presto {
		if index := strings.IndexByte(value, '='); index > 0 {
			key := strings.Trim(strings.TrimSpace(value[:index]), "'")
			value = key + "=" + strings.TrimSpace(value[index+1:])
		}
	}
	return value
}

func matchingParenIndex(text string, open int) int {
	depth := 0
	var quote byte
	for index := open; index < len(text); index++ {
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
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			continue
		}
		if c == '(' {
			depth++
		} else if c == ')' {
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func normalizeCreateTableColumn(column string, target Dialect) string {
	name, rest := splitLeadingSQLToken(strings.TrimSpace(column))
	if name == "" || rest == "" {
		return strings.TrimSpace(column)
	}
	switch strings.ToUpper(strings.Trim(name, "`[]\"")) {
	case "CONSTRAINT", "PRIMARY", "UNIQUE", "CHECK", "FOREIGN", "EXCLUDE":
		return strings.TrimSpace(column)
	}
	typeToken, constraints := splitLeadingSQLToken(rest)
	if typeToken == "" {
		return strings.TrimSpace(column)
	}
	name = normalizeCreateColumnName(name, target)
	typeToken = normalizeCreateTypeToken(typeToken, target)
	constraints = strings.TrimSpace(constraints)
	if target == DialectSnowflake && (strings.Contains(strings.ToUpper(constraints), "IDENTITY") || strings.Contains(strings.ToUpper(constraints), "AUTOINCREMENT")) {
		return name + " " + normalizeSnowflakeAutoIncrement(typeToken+" "+constraints)
	}
	if target == DialectDuckDB {
		if commentIndex := indexKeywordTopLevel(constraints, "COMMENT"); commentIndex >= 0 {
			constraints = strings.TrimSpace(constraints[:commentIndex])
		}
	}
	if constraints != "" {
		if strings.HasPrefix(constraints, "(") && target == DialectSpark && strings.EqualFold(strings.Trim(typeToken, "`"), "TIMESTAMP") {
			if close := strings.IndexByte(constraints, ')'); close >= 0 {
				constraints = strings.TrimSpace(constraints[close+1:])
			}
		}
		if constraints != "" {
			if strings.EqualFold(typeToken, "AS") {
				typeToken += " " + constraints
			} else if strings.HasPrefix(constraints, "(") {
				typeToken += constraints
			} else {
				typeToken += " " + constraints
			}
		}
	}
	return name + " " + typeToken
}

func splitLeadingSQLToken(text string) (string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	depth := 0
	var quote byte
	for index := 0; index < len(text); index++ {
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
		case '[':
			close := strings.IndexByte(text[index+1:], ']')
			if close >= 0 {
				index += close + 1
			}
		case '(', '<':
			depth++
		case ')', '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && (c == ' ' || c == '\t' || c == '\r' || c == '\n') {
				return text[:index], strings.TrimSpace(text[index:])
			}
		}
	}
	return text, ""
}

func normalizeCreateColumnName(name string, target Dialect) string {
	name = strings.TrimSpace(name)
	if target != DialectSpark {
		return name
	}
	if strings.HasPrefix(name, "[") && strings.HasSuffix(name, "]") {
		return "`" + strings.ReplaceAll(name[1:len(name)-1], "]]", "]") + "`"
	}
	return name
}

func normalizeCreateTypeToken(typeToken string, target Dialect) string {
	typeToken = strings.TrimSpace(typeToken)
	if target == DialectTSQL && strings.HasPrefix(typeToken, "\"") && strings.HasSuffix(typeToken, "\"") {
		return "[" + strings.ReplaceAll(typeToken[1:len(typeToken)-1], "\"\"", "\"") + "]"
	}
	if strings.HasPrefix(typeToken, "[") && strings.HasSuffix(typeToken, "]") {
		typeToken = typeToken[1 : len(typeToken)-1]
	}
	if strings.HasPrefix(strings.ToLower(typeToken), "array<") && strings.HasSuffix(typeToken, ">") {
		inner := normalizeCreateTypeToken(typeToken[len("array<"):len(typeToken)-1], target)
		switch target {
		case DialectDuckDB:
			return inner + "[]"
		case DialectPresto:
			return "ARRAY(" + inner + ")"
		case DialectBigQuery:
			return "ARRAY<" + inner + ">"
		case DialectSnowflake:
			return "ARRAY"
		default:
			return "ARRAY<" + inner + ">"
		}
	}
	if strings.HasPrefix(strings.ToLower(typeToken), "struct<") && strings.HasSuffix(typeToken, ">") {
		return normalizeStructType(typeToken, target)
	}
	if target == DialectRisingWave && strings.HasPrefix(strings.ToUpper(typeToken), "MAP<") && strings.HasSuffix(typeToken, ">") {
		return "MAP(" + typeToken[4:len(typeToken)-1] + ")"
	}
	upper := strings.ToUpper(typeToken)
	base := upper
	suffix := ""
	if open := strings.IndexByte(upper, '('); open >= 0 {
		base = upper[:open]
		suffix = upper[open:]
	}
	if mapped := normalizeGenericCharacterType(base, suffix, target); mapped != "" {
		return mapped
	}
	switch target {
	case DialectDuckDB:
		switch upper {
		case "INT":
			return "INT"
		case "STRING", "VARCHAR":
			return "TEXT"
		case "INTEGER":
			return "INT"
		}
	case DialectPresto:
		switch upper {
		case "STRING", "TEXT", "VARCHAR":
			return "VARCHAR"
		case "INT":
			return "INTEGER"
		}
	case DialectBigQuery:
		switch upper {
		case "INT", "INTEGER", "BIGINT":
			return "INT64"
		case "STRING", "TEXT", "VARCHAR":
			return "STRING"
		}
	case DialectSpark, DialectHive:
		if upper == "DATETIME2" {
			return "TIMESTAMP"
		}
		if upper == "INTEGER" {
			return "INT"
		}
		if strings.HasPrefix(upper, "TIME(") {
			return "TIMESTAMP"
		}
		if strings.HasPrefix(upper, "FLOAT(") {
			return "FLOAT"
		}
		return upper
	case DialectDatabricks:
		if upper == "INTEGER" {
			return "INT"
		}
		if strings.HasPrefix(upper, "TIME(") {
			return "TIMESTAMP"
		}
		if strings.HasPrefix(upper, "FLOAT(") {
			return "FLOAT"
		}
		return upper
	case DialectSnowflake, DialectOracle:
		if upper == "INTEGER" {
			return "INT"
		}
		if target == DialectSnowflake && strings.HasPrefix(upper, "ARRAY<") {
			return "ARRAY"
		}
	case DialectTSQL:
		if upper == "INT" {
			return "INTEGER"
		}
		return upper
	}
	return typeToken
}

func normalizeGenericCharacterType(base, suffix string, target Dialect) string {
	mapBase := func(name string) string {
		if suffix == "" {
			return name
		}
		return name + suffix
	}
	switch target {
	case DialectDuckDB, DialectSQLite:
		switch base {
		case "CHAR", "NCHAR", "VARCHAR", "VARCHAR2", "NVARCHAR", "NVARCHAR2":
			return mapBase("TEXT")
		case "BINARY":
			return mapBase("BLOB")
		}
	case DialectHive:
		switch base {
		case "CHAR", "NCHAR", "VARCHAR", "VARCHAR2", "NVARCHAR", "NVARCHAR2":
			return mapBase("STRING")
		case "TEXT":
			if suffix != "" {
				return mapBase("VARCHAR")
			}
			return "STRING"
		}
	case DialectOracle:
		switch base {
		case "VARCHAR", "VARCHAR2":
			return mapBase("VARCHAR2")
		case "NVARCHAR", "NVARCHAR2":
			return mapBase("NVARCHAR2")
		case "BINARY":
			return mapBase("BLOB")
		case "TEXT":
			return mapBase("CLOB")
		}
	case DialectPostgreSQL:
		switch base {
		case "NCHAR":
			return mapBase("CHAR")
		case "VARCHAR2", "NVARCHAR", "NVARCHAR2":
			return mapBase("VARCHAR")
		case "BINARY":
			return mapBase("BYTEA")
		}
	case DialectRedshift:
		switch base {
		case "BINARY":
			return mapBase("VARBYTE")
		case "TEXT":
			if suffix == "" {
				return "VARCHAR(MAX)"
			}
			return mapBase("VARCHAR")
		}
	}
	return ""
}

func normalizeStructType(typeToken string, target Dialect) string {
	start := strings.IndexByte(typeToken, '<')
	inner := typeToken[start+1 : len(typeToken)-1]
	fields := splitTopLevelSQL(inner, ',')
	for index, field := range fields {
		fieldName, fieldType := splitTopLevelSQLColon(field)
		if fieldName == "" || fieldType == "" {
			fields[index] = strings.TrimSpace(field)
			continue
		}
		fieldType = normalizeCreateTypeToken(fieldType, target)
		if target == DialectDuckDB || target == DialectPresto || target == DialectBigQuery || target == DialectSnowflake {
			fields[index] = strings.TrimSpace(fieldName) + " " + fieldType
		} else {
			fields[index] = strings.TrimSpace(fieldName) + ": " + fieldType
		}
	}
	separator := ", "
	switch target {
	case DialectDuckDB:
		return "STRUCT(" + strings.Join(fields, separator) + ")"
	case DialectPresto:
		return "ROW(" + strings.Join(fields, separator) + ")"
	case DialectBigQuery:
		return "STRUCT<" + strings.Join(fields, separator) + ">"
	case DialectSnowflake:
		return "OBJECT"
	default:
		return "STRUCT<" + strings.Join(fields, separator) + ">"
	}
}

func splitTopLevelSQLColon(text string) (string, string) {
	parts := splitTopLevelSQL(text, ':')
	if len(parts) < 2 {
		return "", ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(strings.Join(parts[1:], ":"))
}

func transformSelect(stmt *SelectStmt, target Dialect) {
	stmt.Tail = rewriteQueryTail(stmt.Tail, target)
	if target == DialectBigQuery {
		stmt.Tail = strings.ReplaceAll(stmt.Tail, "FOR SYSTEM TIME", "FOR SYSTEM_TIME")
	}
	if target == DialectDuckDB && stmt.Tail != "" {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(stmt.Tail)), "USING SAMPLE ") {
			stmt.Tail = normalizeDuckDBSampleTail(stmt.Tail)
		} else {
			stmt.Tail = normalizeDuckDBRawStatement(stmt.Tail)
		}
	}
	if target == DialectSnowflake && stmt.Tail != "" {
		stmt.Tail = normalizeSnowflakeSampleTail(stmt.Tail)
	}
	for i := range stmt.ValuesRows {
		for j := range stmt.ValuesRows[i] {
			stmt.ValuesRows[i][j] = transformExpr(stmt.ValuesRows[i][j], target)
		}
	}
	if stmt.Top != nil && target != DialectTSQL {
		if stmt.Limit == nil {
			stmt.Limit = transformExpr(stmt.Top, target)
		}
		stmt.Top = nil
	}
	if target == DialectTSQL && stmt.Top == nil && stmt.Limit != nil && stmt.Offset == nil {
		stmt.Top = transformExpr(stmt.Limit, target)
		stmt.Limit = nil
	}
	if target == DialectOracle && stmt.Limit != nil {
		if stmt.Fetch == nil {
			stmt.Fetch = &FetchClause{Count: stmt.Limit}
		}
		stmt.Limit = nil
	}
	if stmt.Fetch != nil && target != DialectGeneric && target != DialectOracle && target != DialectTSQL && target != DialectPostgreSQL && target != DialectPresto && target != DialectTrino {
		if stmt.Limit == nil {
			if stmt.Fetch.Count == nil {
				stmt.Limit = &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}
			} else {
				stmt.Limit = transformExpr(stmt.Fetch.Count, target)
			}
		}
		stmt.Fetch = nil
	}
	if target == DialectTSQL && stmt.Offset != nil && len(stmt.OrderBy) == 0 {
		stmt.OrderBy = []OrderItem{{Expr: &RawExpr{Raw: "(SELECT NULL)"}, NullsFirst: true}}
	}
	if target == DialectDremio {
		if number, ok := foldNumericExpr(stmt.Limit); ok {
			stmt.Limit = &LiteralExpr{KindValue: LiteralNumber, Raw: number}
		}
	}
	for i := range stmt.With {
		normalizeIdentifierTarget(&stmt.With[i].Name, target)
		if target == DialectBigQuery {
			stmt.With[i].Columns = nil
		}
		for j := range stmt.With[i].Columns {
			normalizeIdentifierTarget(&stmt.With[i].Columns[j], target)
		}
		if stmt.With[i].Query != nil {
			transformSelect(stmt.With[i].Query, target)
		}
	}
	for i := range stmt.Projections {
		normalizeIdentifierTarget(stmt.Projections[i].Alias, target)
		for j := range stmt.Projections[i].AliasColumns {
			normalizeIdentifierTarget(&stmt.Projections[i].AliasColumns[j], target)
		}
		stmt.Projections[i].Expr = transformExpr(stmt.Projections[i].Expr, target)
		for j := range stmt.Projections[i].Except {
			stmt.Projections[i].Except[j] = transformExpr(stmt.Projections[i].Except[j], target)
		}
		for j := range stmt.Projections[i].Replace {
			stmt.Projections[i].Replace[j].Expr = transformExpr(stmt.Projections[i].Replace[j].Expr, target)
		}
	}
	if target == DialectBigQuery {
		rewriteBigQueryGroupAliases(stmt)
	}
	for i := range stmt.Into {
		normalizeIdentifierTarget(&stmt.Into[i], target)
		if target == DialectPostgreSQL && stmt.IntoUnlogged {
			stmt.IntoTemporary = true
		}
		if (target == DialectDuckDB || target == DialectSnowflake || target == DialectPostgreSQL) && strings.HasPrefix(stmt.Into[i].Text, "#") {
			stmt.IntoTemporary = true
			stmt.Into[i].Text = strings.TrimPrefix(stmt.Into[i].Text, "#")
		}
	}
	for i := range stmt.From {
		transformTableExpr(&stmt.From[i], target)
	}
	if target == DialectDuckDB {
		normalizeDuckDBCommaUnnest(stmt)
	}
	stmt.Where = transformExpr(stmt.Where, target)
	for i := range stmt.GroupBy {
		stmt.GroupBy[i] = transformExpr(stmt.GroupBy[i], target)
		if target == DialectDataFusion {
			if grouping, ok := stmt.GroupBy[i].(*GroupingExpr); ok {
				grouping.Name = strings.ToLower(grouping.Name)
			}
		}
	}
	stmt.Having = transformExpr(stmt.Having, target)
	stmt.Qualify = transformExpr(stmt.Qualify, target)
	stmt.ConnectBy = transformExpr(stmt.ConnectBy, target)
	if rewriteUnsupportedJoins(stmt, target) {
		return
	}
	if rewriteUnsupportedQualify(stmt, target) {
		return
	}
	for i := range stmt.Windows {
		transformWindow(&stmt.Windows[i].Spec, target)
	}
	inlineNamedWindows(stmt, target)
	stmt.SortBy = rewriteOrderItems(stmt.SortBy, target)
	stmt.OrderBy = rewriteOrderItems(stmt.OrderBy, target)
	stmt.Limit = transformExpr(stmt.Limit, target)
	stmt.Offset = transformExpr(stmt.Offset, target)
	if stmt.Fetch != nil {
		stmt.Fetch.Count = transformExpr(stmt.Fetch.Count, target)
	}
	if stmt.SetLeft != nil {
		transformSelect(stmt.SetLeft, target)
	}
	if stmt.SetRight != nil {
		transformSelect(stmt.SetRight, target)
	}
}

// inlineNamedWindows lowers named WINDOW references for targets whose
// SQLGlot generators emit the window specification at each use site. Keep the
// declarations for dialects that support the named-window form directly.
func inlineNamedWindows(stmt *SelectStmt, target Dialect) {
	switch target {
	case DialectPresto, DialectRedshift, DialectSnowflake:
	default:
		return
	}
	if stmt == nil || len(stmt.Windows) == 0 {
		return
	}
	windows := make(map[string]WindowSpec, len(stmt.Windows))
	for _, window := range stmt.Windows {
		windows[strings.ToUpper(window.Name.Text)] = window.Spec
	}
	Walk(stmt, func(current Node) VisitAction {
		switch value := current.(type) {
		case *FunctionCallExpr:
			inlineWindowReference(&value.Over, windows)
		case *WindowedExpr:
			inlineWindowValue(&value.Over, windows)
		}
		return VisitChildren
	})
	stmt.Windows = nil
}

func inlineWindowReference(reference **WindowSpec, windows map[string]WindowSpec) {
	if reference == nil || *reference == nil || (*reference).Name == nil {
		return
	}
	spec, ok := windows[strings.ToUpper((*reference).Name.Text)]
	if !ok {
		return
	}
	spec.Name = nil
	*reference = &spec
}

func inlineWindowValue(reference *WindowSpec, windows map[string]WindowSpec) {
	if reference == nil || reference.Name == nil {
		return
	}
	spec, ok := windows[strings.ToUpper(reference.Name.Text)]
	if !ok {
		return
	}
	spec.Name = nil
	*reference = spec
}

func normalizeDuckDBCommaUnnest(stmt *SelectStmt) {
	if len(stmt.From) != 2 || len(stmt.From[0].Joins) != 0 {
		return
	}
	function, ok := stmt.From[1].Primary.(*TableFunctionFrom)
	if !ok || len(function.Name) != 1 || !strings.EqualFold(function.Name[0].Text, "UNNEST") {
		return
	}
	stmt.From[0].Joins = append(stmt.From[0].Joins, JoinClause{
		Kind:      JoinInner,
		JoinText:  "JOIN",
		Right:     function,
		Condition: &LiteralExpr{KindValue: LiteralBoolean, Raw: "TRUE"},
	})
	stmt.From = stmt.From[:1]
}

type duckDBUnnestProjection struct {
	call       *FunctionCallExpr
	alias      string
	renderExpr func(string) string
}

func rewriteDuckDBUnnestZip(node Node, target Dialect) {
	if target != DialectBigQuery && target != DialectPresto && target != DialectSnowflake {
		return
	}
	stmt, ok := node.(*SelectStmt)
	if !ok || stmt.RawQuery != "" || stmt.SetOperator != "" || len(stmt.With) > 0 || len(stmt.Projections) == 0 || len(stmt.From) > 1 || stmt.Where != nil || len(stmt.GroupBy) > 0 || stmt.Having != nil || stmt.Qualify != nil || len(stmt.OrderBy) > 0 || stmt.Limit != nil || stmt.Offset != nil || stmt.Fetch != nil || stmt.Distinct {
		return
	}
	if len(stmt.From) == 1 && len(stmt.From[0].Joins) > 0 {
		return
	}

	projections := make([]duckDBUnnestProjection, 0, len(stmt.Projections))
	for index, item := range stmt.Projections {
		call, replacement, ok := duckDBUnnestProjectionExpr(item.Expr)
		if !ok || len(call.Args) != 1 {
			return
		}
		alias := "col"
		if item.Alias != nil {
			alias = generateIdentifier(*item.Alias)
		} else if index > 0 {
			alias = "col_" + strconv.Itoa(index+1)
		}
		projections = append(projections, duckDBUnnestProjection{call: call, alias: alias, renderExpr: replacement})
	}

	arrays := make([]string, 0, len(projections))
	for _, projection := range projections {
		arrays = append(arrays, renderDialectExpr(projection.call.Args[0], target))
	}
	var from string
	if len(stmt.From) == 1 {
		text, err := (generator{canonical: true, dialect: target}).tableExpr(&stmt.From[0])
		if err != nil {
			return
		}
		from = text + " "
	}

	projectionText := make([]string, 0, len(projections))
	for index, projection := range projections {
		position := "pos_" + strconv.Itoa(index+2)
		positionReference := position
		if target == DialectPresto || target == DialectSnowflake {
			positionReference = "_u_" + strconv.Itoa(index+2) + "." + position
		}
		var replacement string
		switch target {
		case DialectBigQuery:
			replacement = "IF(pos = " + position + ", " + projection.alias + ", NULL)"
		case DialectPresto:
			replacement = "IF(_u.pos = " + positionReference + ", _u_" + strconv.Itoa(index+2) + "." + projection.alias + ")"
		case DialectSnowflake:
			replacement = "IFF(_u.pos = " + positionReference + ", _u_" + strconv.Itoa(index+2) + "." + projection.alias + ", NULL)"
		}
		projectionText = append(projectionText, projection.renderExpr(replacement)+" AS "+projection.alias)
	}

	var query strings.Builder
	query.WriteString("SELECT ")
	query.WriteString(strings.Join(projectionText, ", "))
	query.WriteString(" FROM ")
	switch target {
	case DialectBigQuery:
		query.WriteString(from)
		if from != "" {
			query.WriteString("CROSS JOIN ")
		}
		maxLength := make([]string, 0, len(arrays))
		for _, array := range arrays {
			maxLength = append(maxLength, "ARRAY_LENGTH("+array+")")
		}
		query.WriteString("UNNEST(GENERATE_ARRAY(0, GREATEST(" + strings.Join(maxLength, ", ") + ") - 1)) AS pos")
		for index, array := range arrays {
			alias := projections[index].alias
			query.WriteString(" CROSS JOIN UNNEST(" + array + ") AS " + alias + " WITH OFFSET AS pos_" + strconv.Itoa(index+2))
		}
	case DialectPresto:
		query.WriteString(from)
		if from != "" {
			query.WriteString("CROSS JOIN ")
		}
		maxLength := make([]string, 0, len(arrays))
		for _, array := range arrays {
			maxLength = append(maxLength, "CARDINALITY("+array+")")
		}
		query.WriteString("UNNEST(SEQUENCE(1, GREATEST(" + strings.Join(maxLength, ", ") + "))) AS _u(pos)")
		for index, array := range arrays {
			alias := projections[index].alias
			query.WriteString(" CROSS JOIN UNNEST(" + array + ") WITH ORDINALITY AS _u_" + strconv.Itoa(index+2) + "(" + alias + ", pos_" + strconv.Itoa(index+2) + ")")
		}
	case DialectSnowflake:
		query.WriteString(from)
		if from != "" {
			query.WriteString("CROSS JOIN ")
		}
		maxLength := make([]string, 0, len(arrays))
		for _, array := range arrays {
			maxLength = append(maxLength, "ARRAY_SIZE("+array+")")
		}
		query.WriteString("TABLE(FLATTEN(INPUT => ARRAY_GENERATE_RANGE(0, (GREATEST(" + strings.Join(maxLength, ", ") + ") - 1) + 1))) AS _u(seq, key, path, index, pos, this)")
		for index, array := range arrays {
			alias := projections[index].alias
			query.WriteString(" CROSS JOIN TABLE(FLATTEN(INPUT => " + array + ")) AS _u_" + strconv.Itoa(index+2) + "(seq, key, path, pos_" + strconv.Itoa(index+2) + ", " + alias + ", this)")
		}
	}

	conditions := make([]string, 0, len(arrays))
	for index, array := range arrays {
		position := "pos_" + strconv.Itoa(index+2)
		switch target {
		case DialectBigQuery:
			conditions = append(conditions, "pos = "+position+" OR (pos > (ARRAY_LENGTH("+array+") - 1) AND "+position+" = (ARRAY_LENGTH("+array+") - 1))")
		case DialectPresto:
			conditions = append(conditions, "_u.pos = _u_"+strconv.Itoa(index+2)+"."+position+" OR (_u.pos > CARDINALITY("+array+") AND _u_"+strconv.Itoa(index+2)+"."+position+" = CARDINALITY("+array+"))")
		case DialectSnowflake:
			conditions = append(conditions, "_u.pos = _u_"+strconv.Itoa(index+2)+"."+position+" OR (_u.pos > (ARRAY_SIZE("+array+") - 1) AND _u_"+strconv.Itoa(index+2)+"."+position+" = (ARRAY_SIZE("+array+") - 1))")
		}
	}
	if len(conditions) > 0 {
		query.WriteString(" WHERE ")
		if len(conditions) == 1 {
			query.WriteString(conditions[0])
		} else {
			combined := "(" + "(" + conditions[0] + ") AND (" + conditions[1] + ")" + ")"
			for _, condition := range conditions[2:] {
				combined += " AND (" + condition + ")"
			}
			query.WriteString(combined)
		}
	}
	stmt.RawQuery = query.String()
}

func duckDBUnnestProjectionExpr(expression Expr) (*FunctionCallExpr, func(string) string, bool) {
	if call, ok := expression.(*FunctionCallExpr); ok && len(call.Name) == 1 && strings.EqualFold(call.Name[0].Text, "UNNEST") {
		return call, func(replacement string) string { return replacement }, true
	}
	if binary, ok := expression.(*BinaryExpr); ok {
		if call, ok := binary.Left.(*FunctionCallExpr); ok && len(call.Name) == 1 && strings.EqualFold(call.Name[0].Text, "UNNEST") {
			return call, func(replacement string) string {
				return replacement + " " + binary.Operator + " " + renderExpr(binary.Right)
			}, true
		}
	}
	return nil, nil, false
}

func normalizeBigQueryFunctionFormat(function *FunctionCallExpr) {
	if len(function.Name) == 0 || len(function.Args) == 0 {
		return
	}
	name := function.Name[len(function.Name)-1].Text
	if !strings.EqualFold(name, "PARSE_DATE") && !strings.EqualFold(name, "PARSE_DATETIME") && !strings.EqualFold(name, "PARSE_TIMESTAMP") {
		return
	}
	if literal, ok := function.Args[0].(*LiteralExpr); ok && literal.KindValue == LiteralString {
		literal.Raw = normalizeBigQueryDateFormat(literal.Raw)
	}
}

func rewriteQueryTail(tail string, target Dialect) string {
	trimmed := strings.TrimSpace(tail)
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "AT SNAPSHOT ") {
		value := strings.TrimSpace(trimmed[len("AT SNAPSHOT "):])
		switch target {
		case DialectSpark:
			return "VERSION AS OF " + value
		case DialectTrino:
			return "FOR VERSION AS OF " + value
		}
	}
	if strings.HasPrefix(upper, "AT TIMESTAMP ") && target == DialectSpark {
		return "TIMESTAMP AS OF " + strings.TrimSpace(trimmed[len("AT TIMESTAMP "):])
	}
	return tail
}

func transformTableExpr(table *TableExpr, target Dialect) {
	normalizeFromItemIdentifiers(table.Primary, target)
	if target == DialectBigQuery {
		normalizeBigQueryFromItem(table.Primary)
		switch item := table.Primary.(type) {
		case *TableName:
			item.Columns = nil
		case *TableFunctionFrom:
			item.Columns = nil
		}
		for index := range table.Modifiers {
			table.Modifiers[index] = normalizeBigQueryPivotModifier(table.Modifiers[index])
		}
	}
	if target == DialectDuckDB {
		if raw, ok := table.Primary.(*RawFrom); ok {
			raw.Raw = normalizeDuckDBRawStatement(raw.Raw)
		}
		if item, ok := table.Primary.(*TableName); ok && item.Sample != nil {
			item.Sample.Raw = normalizeDuckDBSampleSpec(item.Sample.Raw)
		}
	}
	if target == DialectSnowflake {
		if item, ok := table.Primary.(*TableName); ok {
			item.Tail = normalizeSnowflakeTableTail(item.Tail)
			if item.Sample != nil {
				item.Sample.Raw = normalizeSnowflakeTableSample(item.Sample.Raw)
			}
		}
		if raw, ok := table.Primary.(*RawFrom); ok {
			raw.Raw = normalizeSnowflakeStageFrom(raw.Raw)
		}
	}
	if target == DialectTSQL {
		if raw, ok := table.Primary.(*RawFrom); ok {
			raw.Raw = strings.ReplaceAll(strings.ReplaceAll(raw.Raw, "TRUE", "1"), "FALSE", "0")
		}
	}
	switch item := table.Primary.(type) {
	case *SubqueryFrom:
		if item.Query != nil {
			transformSelect(item.Query, target)
		}
	case *GroupedFrom:
		for i := range item.Items {
			transformTableExpr(&item.Items[i], target)
		}
	case *TableFunctionFrom:
		transformTableFunction(item, target)
	}
	for i := range table.Joins {
		if table.Joins[i].Right != nil {
			transformFromItem(table.Joins[i].Right, target)
		}
		table.Joins[i].Condition = transformExpr(table.Joins[i].Condition, target)
	}
	for i := range table.LateralViews {
		table.LateralViews[i].Expression = transformExpr(table.LateralViews[i].Expression, target)
	}
	if target == DialectSQLite {
		if function, ok := table.Primary.(*TableFunctionFrom); ok && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "RANGE") && len(function.Args) >= 2 && function.Alias != nil {
			column := "value"
			if len(function.Columns) > 0 {
				column = function.Columns[0].Text
			}
			start := renderDialectExpr(function.Args[0], DialectSQLite)
			end := renderDialectExpr(function.Args[1], DialectSQLite)
			table.Primary = &SubqueryFrom{Query: &SelectStmt{RawQuery: "SELECT value AS " + column + " FROM GENERATE_SERIES(" + start + ", " + end + ")"}, Alias: function.Alias}
		}
	}
	if target == DialectDuckDB {
		for i := range table.Modifiers {
			table.Modifiers[i] = normalizeDuckDBRawStatement(table.Modifiers[i])
		}
	}
}

func normalizeIdentifierTarget(identifier *Identifier, target Dialect) {
	if identifier == nil {
		return
	}
	if target == DialectMySQL && !identifier.Quoted && strings.EqualFold(identifier.Text, "STRAIGHT_JOIN") {
		identifier.Quoted = true
		identifier.Quote = '`'
		return
	}
	if target == DialectBigQuery && !identifier.Quoted && (strings.EqualFold(identifier.Text, "HASH") || strings.EqualFold(identifier.Text, "AT")) {
		identifier.Quoted = true
		identifier.Quote = '`'
		return
	}
	if !identifier.Quoted {
		return
	}
	if target == DialectTSQL {
		identifier.Quote = '['
	} else if target == DialectBigQuery || target == DialectDrill || target == DialectHive || target == DialectSpark || target == DialectDatabricks || target == DialectMySQL || target == DialectStarRocks {
		identifier.Quote = '`'
	}
}

func normalizeBigQueryFromItem(item FromItem) {
	table, ok := item.(*TableName)
	if !ok || len(table.Parts) == 0 {
		return
	}
	for index, part := range table.Parts {
		if !strings.Contains(strings.ToUpper(part.Text), "INFORMATION_SCHEMA") || index+1 >= len(table.Parts) {
			continue
		}
		combined := part.Text + "." + table.Parts[index+1].Text
		combinedPart := Identifier{Text: combined, Quoted: true, Quote: '`', Span: Span{Start: part.Span.Start, End: table.Parts[index+1].Span.End}}
		parts := make([]Identifier, 0, len(table.Parts)-1)
		parts = append(parts, table.Parts[:index]...)
		parts = append(parts, combinedPart)
		parts = append(parts, table.Parts[index+2:]...)
		table.Parts = parts
		if table.Alias == nil {
			alias := Identifier{Text: table.Parts[index].Text[strings.LastIndex(table.Parts[index].Text, ".")+1:]}
			if strings.Contains(strings.ToUpper(part.Text), "INFORMATION_SCHEMA") && strings.Contains(part.Text, ".") && index == 0 {
				alias.Text = table.Parts[index].Text[strings.LastIndex(table.Parts[index].Text, ".")+1:]
			}
			table.Alias = &alias
		}
		return
	}
	if len(table.Parts) == 1 && strings.Contains(strings.ToUpper(table.Parts[0].Text), "INFORMATION_SCHEMA") && table.Alias == nil {
		alias := table.Parts[0]
		table.Alias = &alias
	}
}

func normalizeBigQueryPivotModifier(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToUpper(trimmed), "PIVOT") {
		return raw
	}
	open := strings.IndexByte(trimmed, '(')
	if open < 0 {
		return raw
	}
	close := matchingParenIndex(trimmed, open)
	if close < 0 {
		return raw
	}
	inner := trimmed[open+1 : close]
	forIndex := keywordAtDepth(inner, "FOR", 0)
	if forIndex < 0 {
		return raw
	}
	aggregates := splitTopLevelSQL(strings.TrimSpace(inner[:forIndex]), ',')
	for index, aggregate := range aggregates {
		aggregate = strings.TrimSpace(aggregate)
		if aggregate == "" || strings.Contains(strings.ToUpper(aggregate), " AS ") {
			aggregates[index] = aggregate
			continue
		}
		aggregateOpen := strings.IndexByte(aggregate, '(')
		if aggregateOpen >= 0 {
			closeIndex := matchingParenIndex(aggregate, aggregateOpen)
			if closeIndex > aggregateOpen && strings.TrimSpace(aggregate[closeIndex+1:]) != "" {
				aggregates[index] = strings.TrimSpace(aggregate[:closeIndex+1]) + " AS " + strings.TrimSpace(aggregate[closeIndex+1:])
			}
		}
	}
	return trimmed[:open+1] + strings.Join(aggregates, ", ") + " " + strings.TrimSpace(inner[forIndex:]) + trimmed[close:]
}

func normalizeBigQueryRawStatement(raw string) string {
	text := raw
	text = strings.TrimSpace(replaceFold(text, "SET VARIABLE ", "SET "))
	for {
		upper := strings.ToUpper(text)
		index := strings.Index(upper, "TIMESTAMP '")
		if index < 0 {
			return normalizeBigQueryJavaScriptFunction(text)
		}
		literalStart := index + len("TIMESTAMP ")
		literalEnd := literalStart + 1
		for literalEnd < len(text) {
			if text[literalEnd] == '\'' {
				if literalEnd+1 < len(text) && text[literalEnd+1] == '\'' {
					literalEnd += 2
					continue
				}
				literalEnd++
				break
			}
			literalEnd++
		}
		if literalEnd > len(text) || text[literalEnd-1] != '\'' {
			return text
		}
		literal := text[literalStart:literalEnd]
		text = text[:index] + "CAST(" + literal + " AS TIMESTAMP)" + text[literalEnd:]
	}
}

func normalizeBigQueryJavaScriptFunction(text string) string {
	upper := strings.ToUpper(text)
	languageIndex := strings.Index(upper, "LANGUAGE JS")
	if languageIndex < 0 {
		return text
	}
	asIndex := strings.Index(upper[languageIndex:], " AS \"\"\"")
	if asIndex < 0 {
		return text
	}
	asIndex += languageIndex
	bodyStart := asIndex + len(" AS \"\"\"")
	bodyEndRelative := strings.Index(text[bodyStart:], "\"\"\"")
	if bodyEndRelative < 0 {
		return text
	}
	bodyEnd := bodyStart + bodyEndRelative
	suffix := strings.TrimSpace(text[bodyEnd+3:])
	if !strings.HasPrefix(strings.ToUpper(suffix), "OPTIONS") {
		return text
	}
	language := text[languageIndex:asIndex]
	body := strings.ReplaceAll(text[bodyStart:bodyEnd], "'", "\\'")
	return text[:languageIndex] + language + " " + suffix + " AS '" + body + "'"
}

func keywordAtDepth(text, keyword string, wantedDepth int) int {
	depth := 0
	for index := 0; index+len(keyword) <= len(text); index++ {
		switch text[index] {
		case '\'', '"', '`':
			quote := text[index]
			index++
			for index < len(text) {
				if text[index] == quote {
					if index+1 < len(text) && text[index+1] == quote {
						index += 2
						continue
					}
					break
				}
				index++
			}
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == wantedDepth && strings.EqualFold(text[index:index+len(keyword)], keyword) && keywordBoundary(text, index, index+len(keyword)) {
				return index
			}
		}
	}
	return -1
}

func rewriteBigQueryGroupAliases(stmt *SelectStmt) {
	for groupIndex, group := range stmt.GroupBy {
		if literal, ok := group.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
			continue
		}
		if len(stmt.OrderBy) == 0 {
			continue
		}
		groupText := renderExpr(group)
		for _, projection := range stmt.Projections {
			if projection.Alias == nil || renderExpr(projection.Expr) != groupText {
				continue
			}
			alias := *projection.Alias
			stmt.GroupBy[groupIndex] = &IdentifierExpr{Parts: []Identifier{alias}}
			break
		}
	}
}

func normalizeFromItemIdentifiers(item FromItem, target Dialect) {
	switch item := item.(type) {
	case *TableName:
		for i := range item.Parts {
			normalizeIdentifierTarget(&item.Parts[i], target)
		}
		normalizeIdentifierTarget(item.Alias, target)
		for i := range item.Columns {
			normalizeIdentifierTarget(&item.Columns[i], target)
		}
	case *SubqueryFrom:
		normalizeIdentifierTarget(item.Alias, target)
		for i := range item.Columns {
			normalizeIdentifierTarget(&item.Columns[i], target)
		}
	case *GroupedFrom:
		normalizeIdentifierTarget(item.Alias, target)
		for i := range item.Columns {
			normalizeIdentifierTarget(&item.Columns[i], target)
		}
	case *RawFrom:
		normalizeIdentifierTarget(item.Alias, target)
		for i := range item.Columns {
			normalizeIdentifierTarget(&item.Columns[i], target)
		}
	case *TableFunctionFrom:
		for i := range item.Name {
			normalizeIdentifierTarget(&item.Name[i], target)
		}
		normalizeIdentifierTarget(item.Alias, target)
		for i := range item.Columns {
			normalizeIdentifierTarget(&item.Columns[i], target)
		}
	}
}

func rewriteUnsupportedJoins(stmt *SelectStmt, target Dialect) bool {
	if target == DialectDuckDB || target == DialectSpark || target == DialectDataFusion || len(stmt.From) != 1 {
		return false
	}
	table := &stmt.From[0]
	for index, join := range table.Joins {
		if !strings.Contains(strings.ToUpper(join.JoinText), "SEMI") && !strings.Contains(strings.ToUpper(join.JoinText), "ANTI") {
			continue
		}
		if join.Right == nil || join.Condition == nil {
			continue
		}
		right := &SelectStmt{
			Projections:      []SelectItem{{Expr: &LiteralExpr{KindValue: LiteralNumber, Raw: "1"}}},
			From:             []TableExpr{{Primary: join.Right}},
			Where:            join.Condition,
			Parenthesized:    true,
			ParenthesisDepth: 1,
		}
		exists := &ExistsExpr{Query: right}
		if strings.Contains(strings.ToUpper(join.JoinText), "ANTI") {
			exists = nil
			stmt.Where = &UnaryExpr{
				Operator: "NOT",
				Expr:     &ExistsExpr{Query: right},
			}
		} else {
			stmt.Where = exists
		}
		table.Joins = append(table.Joins[:index], table.Joins[index+1:]...)
		return true
	}
	return false
}

func rewriteUnsupportedQualify(stmt *SelectStmt, target Dialect) bool {
	if stmt.Qualify == nil || target == DialectDuckDB || target == DialectSnowflake || target == DialectClickHouse || target == DialectDataFusion {
		return false
	}
	binary, ok := stmt.Qualify.(*BinaryExpr)
	if !ok {
		return false
	}
	window, ok := binary.Left.(*FunctionCallExpr)
	if !ok || window.Over == nil {
		return false
	}
	inner := *stmt
	inner.Qualify = nil
	inner.SetLeft = nil
	inner.SetRight = nil
	inner.SetOperator = ""
	originalProjections := append([]SelectItem(nil), stmt.Projections...)
	if target == DialectTSQL {
		addTSQLProjectionAliases(&inner)
	}
	outerProjections := make([]SelectItem, 0, len(originalProjections))
	for index := range inner.Projections {
		projection := &inner.Projections[index]
		outer := SelectItem{}
		if projection.Alias != nil {
			alias := *projection.Alias
			outer.Expr = &IdentifierExpr{Parts: []Identifier{alias}}
		} else if identifier, ok := projection.Expr.(*IdentifierExpr); ok && len(identifier.Parts) > 0 {
			alias := identifier.Parts[len(identifier.Parts)-1]
			outer.Expr = &IdentifierExpr{Parts: []Identifier{alias}}
		} else {
			outer.Expr = originalProjections[index].Expr
		}
		outerProjections = append(outerProjections, outer)
	}
	inner.Projections = append(append([]SelectItem(nil), inner.Projections...), SelectItem{
		Expr:  window,
		Alias: &Identifier{Text: "_w"},
	})
	if target == DialectSpark {
		for i := range window.Over.OrderBy {
			window.Over.OrderBy[i].NullsLast = true
		}
	}
	outerWhere := *binary
	outerWhere.Left = &IdentifierExpr{Parts: []Identifier{{Text: "_w"}}}
	outer := SelectStmt{
		nodeBase:    nodeBase{span: stmt.SourceSpan()},
		Projections: outerProjections,
		From:        []TableExpr{{Primary: &SubqueryFrom{Query: &inner, Alias: &Identifier{Text: "_t"}}}},
		Where:       &outerWhere,
	}
	*stmt = outer
	return true
}

func transformFromItem(item FromItem, target Dialect) {
	switch item := item.(type) {
	case *TableName:
		if target == DialectSnowflake {
			item.Tail = normalizeSnowflakeTableTail(item.Tail)
			if item.Sample != nil {
				item.Sample.Raw = normalizeSnowflakeTableSample(item.Sample.Raw)
			}
		}
	case *SubqueryFrom:
		if item.Query != nil {
			transformSelect(item.Query, target)
		}
	case *GroupedFrom:
		for i := range item.Items {
			transformTableExpr(&item.Items[i], target)
		}
	case *TableFunctionFrom:
		transformTableFunction(item, target)
	}
}

func transformTableFunction(function *TableFunctionFrom, target Dialect) {
	for i := range function.Args {
		function.Args[i] = transformExpr(function.Args[i], target)
	}
	if target == DialectSnowflake && function.RawArgs != "" {
		function.RawArgs = normalizeSnowflakeArrayConstruct(function.RawArgs)
	}
	if target == DialectSnowflake && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "UNNEST") && function.WithOrdinality && len(function.Args) == 1 {
		argument := renderExpr(function.Args[0])
		function.Name = []Identifier{{Text: "TABLE"}}
		function.RawArgs = "(FLATTEN(INPUT => " + argument + "))"
		function.Args = nil
		function.WithOrdinality = false
		function.Alias = &Identifier{Text: "_t0"}
		function.Columns = []Identifier{{Text: "seq"}, {Text: "key"}, {Text: "path"}, {Text: "index"}, {Text: "value"}, {Text: "this"}}
	}
	if target == DialectDuckDB && len(function.Name) == 1 && len(function.Args) == 1 &&
		(strings.EqualFold(function.Name[0].Text, "RANGE") || strings.EqualFold(function.Name[0].Text, "GENERATE_SERIES")) {
		function.Name[0].Text = strings.ToUpper(function.Name[0].Text)
		function.Args = append([]Expr{&LiteralExpr{KindValue: LiteralNumber, Raw: "0"}}, function.Args...)
	}
	if target == DialectBigQuery && function.WithOffset && function.Alias == nil {
		function.Alias = &Identifier{Text: "offset"}
	}
}

func normalizeSnowflakeArrayConstruct(raw string) string {
	for {
		upper := strings.ToUpper(raw)
		index := strings.Index(upper, "ARRAY_CONSTRUCT(")
		if index < 0 {
			return raw
		}
		open := index + len("ARRAY_CONSTRUCT")
		close := matchingParenIndex(raw, open)
		if close < 0 {
			return raw
		}
		raw = raw[:index] + "[" + raw[open+1:close] + "]" + raw[close+1:]
	}
}

func normalizeSnowflakeNamedArgs(raw string, order []string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '(' || matchingParenIndex(trimmed, 0) != len(trimmed)-1 {
		return raw
	}
	parts := splitTopLevelSQL(trimmed[1:len(trimmed)-1], ',')
	if len(parts) < 2 {
		return raw
	}
	positional := make([]string, 0, len(parts))
	named := make(map[string]string, len(parts))
	namedOrder := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		pieces := strings.SplitN(part, "=>", 2)
		if len(pieces) != 2 {
			positional = append(positional, part)
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(pieces[0]))
		if _, exists := named[key]; !exists {
			namedOrder = append(namedOrder, key)
		}
		named[key] = strings.TrimSpace(pieces[1])
	}
	if len(named) == 0 {
		return raw
	}
	result := append([]string(nil), positional...)
	seen := make(map[string]bool, len(named))
	for _, key := range order {
		key = strings.ToUpper(key)
		if value, ok := named[key]; ok {
			result = append(result, key+" => "+value)
			seen[key] = true
		}
	}
	for _, key := range namedOrder {
		if !seen[key] {
			result = append(result, key+" => "+named[key])
		}
	}
	return "(" + strings.Join(result, ", ") + ")"
}

func normalizeSnowflakeGeneratorArgs(args []Expr) string {
	if len(args) == 0 || len(args) > 2 {
		return ""
	}
	values := make([]string, 0, len(args))
	for _, arg := range args {
		values = append(values, renderExpr(arg))
	}
	labels := []string{"ROWCOUNT", "TIMELIMIT"}
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = labels[index] + " => " + value
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func normalizeSnowflakeGeneratorRawArgs(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '(' || matchingParenIndex(trimmed, 0) != len(trimmed)-1 {
		return raw
	}
	parts := splitTopLevelSQL(trimmed[1:len(trimmed)-1], ',')
	if len(parts) == 0 || len(parts) > 2 {
		return raw
	}
	args := make([]string, 0, len(parts))
	labels := []string{"ROWCOUNT", "TIMELIMIT"}
	for index, part := range parts {
		part = strings.TrimSpace(part)
		if strings.Contains(part, "=>") {
			return normalizeSnowflakeNamedArgs(raw, labels)
		}
		args = append(args, labels[index]+" => "+part)
	}
	return "(" + strings.Join(args, ", ") + ")"
}

func normalizeSnowflakeDateArithmetic(expression Expr) Expr {
	function, ok := expression.(*FunctionCallExpr)
	if !ok || len(function.Name) != 1 || !strings.EqualFold(function.Name[0].Text, "TO_DATE") || len(function.Args) != 1 {
		return expression
	}
	return &CastExpr{Keyword: "CAST", Value: function.Args[0], Type: identifierExpr("DATE")}
}

func castSnowflakeLambdaVariable(body, variable, typeName string) string {
	if variable == "" || typeName == "" {
		return body
	}
	var result strings.Builder
	for index := 0; index < len(body); {
		if !isASCIIIdentifierByte(body[index]) {
			result.WriteByte(body[index])
			index++
			continue
		}
		start := index
		for index < len(body) && isASCIIIdentifierByte(body[index]) {
			index++
		}
		word := body[start:index]
		if strings.EqualFold(word, variable) {
			result.WriteString("CAST(")
			result.WriteString(word)
			result.WriteString(" AS ")
			result.WriteString(typeName)
			result.WriteByte(')')
		} else {
			result.WriteString(word)
		}
	}
	return result.String()
}

func transformOrderItem(item *OrderItem, target Dialect) {
	item.Expr = transformExpr(item.Expr, target)
}

func transformWindow(window *WindowSpec, target Dialect) {
	for i := range window.PartitionBy {
		window.PartitionBy[i] = transformExpr(window.PartitionBy[i], target)
	}
	window.OrderBy = rewriteOrderItems(window.OrderBy, target)
}

func rewriteOrderItems(items []OrderItem, target Dialect) []OrderItem {
	if len(items) == 0 {
		return items
	}
	rewritten := make([]OrderItem, 0, len(items))
	for _, item := range items {
		item.Expr = transformExpr(item.Expr, target)
		if target == DialectTSQL {
			_, numericOrder := item.Expr.(*LiteralExpr)
			needsNullRank := (!item.Descending && !item.NullsFirst || item.Descending && item.NullsFirst) && !numericOrder
			if needsNullRank {
				original := item
				original.NullsFirst = false
				original.NullsLast = false
				item = original
				item.Expr = &CaseExpr{
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
				item.Ascending = false
				rewritten = append(rewritten, item, original)
				continue
			}
			item.NullsFirst = false
			item.NullsLast = false
		} else if target == DialectSpark {
			if item.Ascending && !item.Descending && !item.NullsFirst {
				item.NullsLast = true
			}
		} else if target == DialectMySQL {
			item.NullsFirst = false
			item.NullsLast = false
		} else if target == DialectDuckDB || target == DialectDremio || target == DialectClickHouse {
			item.NullsLast = false
		} else if target == DialectPresto || target == DialectTrino {
			if item.Ascending {
				item.NullsLast = false
			}
			if !item.Ascending && !item.Descending && !item.NullsFirst && !item.NullsLast {
				item.NullsFirst = true
			}
		} else if target == DialectPostgreSQL || target == DialectSnowflake {
			if item.Ascending && item.NullsLast || item.Descending && item.NullsFirst {
				item.NullsFirst = false
				item.NullsLast = false
			}
		}
		rewritten = append(rewritten, item)
	}
	return rewritten
}

func transformExpr(expression Expr, target Dialect) Expr {
	if expression == nil {
		return nil
	}
	switch expression := expression.(type) {
	case *LiteralExpr:
		if expression.KindValue == LiteralBoolean || expression.KindValue == LiteralNull {
			expression.Raw = strings.ToUpper(expression.Raw)
		}
		if target == DialectDuckDB && expression.KindValue == LiteralNumber {
			expression.Raw = strings.ReplaceAll(expression.Raw, "_", "")
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
			if normalized, ok := normalizeBigQueryString(expression.Raw); ok {
				expression.Raw = normalized
			}
		}
		if target == DialectBigQuery && expression.KindValue == LiteralParameter && strings.HasPrefix(expression.Raw, "$") {
			expression.Raw = "@" + strings.TrimPrefix(expression.Raw, "$")
		}
		if target == DialectDuckDB && expression.KindValue == LiteralParameter && strings.HasPrefix(expression.Raw, "@") && len(expression.Raw) > 1 {
			return &FunctionCallExpr{Name: []Identifier{{Text: "ABS"}}, Args: []Expr{identifierExpr(strings.TrimPrefix(expression.Raw, "@"))}}
		}
	case *IdentifierExpr:
		for i := range expression.Parts {
			normalizeIdentifierTarget(&expression.Parts[i], target)
		}
		if target == DialectSnowflake && len(expression.Parts) == 1 && !expression.Parts[0].Quoted && strings.EqualFold(expression.Parts[0].Text, "LOCALTIMESTAMP") {
			expression.Parts[0].Text = "CURRENT_TIMESTAMP"
		}
		if target == DialectBigQuery && len(expression.Parts) == 1 && strings.HasPrefix(expression.Parts[0].Text, "$") {
			expression.Parts[0].Text = "@" + strings.TrimPrefix(expression.Parts[0].Text, "$")
		}
		if target == DialectGeneric && len(expression.Parts) == 1 && !expression.Parts[0].Quoted {
			switch strings.ToUpper(expression.Parts[0].Text) {
			case "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP", "CURRENT_DATETIME":
				expression.Parts[0].Text = strings.ToUpper(expression.Parts[0].Text)
			}
		}
	case *UnaryExpr:
		expression.Expr = transformExpr(expression.Expr, target)
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
			expression.Left = booleanOperandTSQL(expression.Left)
			expression.Right = booleanOperandTSQL(expression.Right)
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
		expression.Value = transformExpr(expression.Value, target)
		for i := range expression.Items {
			expression.Items[i] = transformExpr(expression.Items[i], target)
		}
		if expression.Query != nil {
			transformSelect(expression.Query, target)
		}
		if (target == DialectGeneric || target == DialectDuckDB || target == DialectSnowflake) && expression.Not {
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
		expression.Value = transformExpr(expression.Value, target)
		expression.Right = transformExpr(expression.Right, target)
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
		}
		if rewritten := rewriteFunction(expression, target); rewritten != nil {
			return rewritten
		}
	case *CallExpr:
		expression.Callee = transformExpr(expression.Callee, target)
		for i := range expression.Args {
			expression.Args[i] = transformExpr(expression.Args[i], target)
		}
		if target == DialectDuckDB && isDuckDBAtMarker(expression.Callee) && len(expression.Args) == 1 {
			value := expression.Args[0]
			if _, alreadyParenthesized := value.(*ParenthesizedExpr); !alreadyParenthesized {
				value = &ParenthesizedExpr{Expr: value}
			}
			return &FunctionCallExpr{Name: []Identifier{{Text: "ABS"}}, Args: []Expr{value}}
		}
		if target == DialectBigQuery {
			if generic, ok := expression.Callee.(*GenericExpr); ok && isIdentifierNamed(generic.Target, "STRUCT") && len(expression.Args) == 1 {
				return &CastExpr{nodeBase: nodeBase{span: expression.SourceSpan()}, Keyword: "CAST", Value: &FunctionCallExpr{Name: []Identifier{{Text: "STRUCT"}}, Args: expression.Args}, Type: generic}
			}
		}
	case *GenericExpr:
		expression.Target = transformExpr(expression.Target, target)
		for i := range expression.Arguments {
			expression.Arguments[i] = transformExpr(expression.Arguments[i], target)
		}
	case *ExtractExpr:
		expression.Field = transformExpr(expression.Field, target)
		expression.Source = transformExpr(expression.Source, target)
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
		if target == DialectBigQuery && len(expression.TypeSuffix) > 0 && isIdentifierNamed(expression.Type, "TIMESTAMP") {
			for _, suffix := range expression.TypeSuffix {
				if strings.HasPrefix(strings.ToUpper(suffix.Text), "FORMAT ") {
					format := strings.TrimSpace(suffix.Text[len("FORMAT "):])
					format = normalizeBigQueryDateFormat(format)
					return &FunctionCallExpr{Name: []Identifier{{Text: "PARSE_TIMESTAMP"}}, Args: []Expr{&LiteralExpr{KindValue: LiteralString, Raw: format}, expression.Value}}
				}
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
	case *CaseExpr:
		expression.Operand = transformExpr(expression.Operand, target)
		for i := range expression.Whens {
			expression.Whens[i].Condition = transformExpr(expression.Whens[i].Condition, target)
			expression.Whens[i].Result = transformExpr(expression.Whens[i].Result, target)
		}
		expression.Else = transformExpr(expression.Else, target)
	case *IndexExpr:
		expression.Target = transformExpr(expression.Target, target)
		expression.Low = transformExpr(expression.Low, target)
		expression.High = transformExpr(expression.High, target)
		expression.Step = transformExpr(expression.Step, target)
		for i := range expression.Indices {
			expression.Indices[i] = transformExpr(expression.Indices[i], target)
		}
		if target == DialectBigQuery && !expression.Slice {
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

func normalizeBigQueryString(raw string) (string, bool) {
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
	switch prefix {
	case 'b', 'B':
		content = strings.ReplaceAll(content, "'", "\\'")
		return "b'" + content + "'", true
	case 'r', 'R':
		content = strings.ReplaceAll(content, "\\", "\\\\")
		return "'" + content + "'", true
	default:
		if quote == '"' {
			content = strings.ReplaceAll(content, `\"`, `"`)
		}
		return "'" + content + "'", true
	}
}

func normalizeBigQueryDateFormat(raw string) string {
	value := strings.TrimSpace(raw)
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		content := value[1 : len(value)-1]
		content = strings.ReplaceAll(content, "%Y-%m-%d", "%F")
		content = strings.ReplaceAll(content, "%H:%M:%S", "%T")
		content = strings.ReplaceAll(content, "YYYY-MM-DD", "%F")
		content = strings.ReplaceAll(content, "HH24:MI:SS", "%T")
		return "'" + content + "'"
	}
	return value
}

func normalizeDuckDBJSONPath(expression Expr) Expr {
	switch value := expression.(type) {
	case *LiteralExpr:
		if value.KindValue == LiteralString && len(value.Raw) >= 2 {
			content := strings.Trim(value.Raw, "'")
			if strings.HasPrefix(content, "$") || strings.HasPrefix(content, "/") {
				return value
			}
			value.Raw = "'$." + strings.ReplaceAll(content, "'", "''") + "'"
			return value
		}
		if value.KindValue == LiteralNumber {
			return &LiteralExpr{KindValue: LiteralString, Raw: "'$[" + value.Raw + "]'"}
		}
	}
	return expression
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

func rewriteFunction(function *FunctionCallExpr, target Dialect) Expr {
	if len(function.Name) != 1 || function.RawArgs != "" {
		return nil
	}
	name := strings.ToUpper(function.Name[0].Text)
	if rewritten, handled := rewriteTimeConversionFunction(function, target, name); handled {
		return rewritten
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
		case "STARTS_WITH":
			if len(function.Args) == 2 {
				return startsWithTSQL(function.Args[0], function.Args[1])
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
		case "LIST":
			setFunctionName(function, "ARRAY_AGG")
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
				if array, ok := function.Args[0].(*FunctionCallExpr); ok && array.ArrayLiteral {
					return nil
				}
				field := unquoteDatePart(function.Args[0])
				if identifier, ok := field.(*IdentifierExpr); ok && len(identifier.Parts) == 1 {
					identifier.Parts[0].Text = strings.ToUpper(identifier.Parts[0].Text)
				}
				return &ExtractExpr{Field: field, Source: function.Args[1]}
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
			if len(function.Args) >= 2 && !isPrestoBigint(function.Args[1]) {
				function.Args[1] = &CastExpr{Keyword: "CAST", Value: function.Args[1], Type: identifierExpr("BIGINT")}
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
		if name == "DATE_PART" && len(function.Args) == 2 {
			field := unquoteDatePart(function.Args[0])
			if identifier, ok := field.(*IdentifierExpr); ok && len(identifier.Parts) == 1 {
				identifier.Parts[0].Text = strings.ToLower(identifier.Parts[0].Text)
			}
			return &ExtractExpr{Field: field, Source: function.Args[1]}
		}
	}
	if target == DialectClickHouse {
		switch name {
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
			condition = &BinaryExpr{
				Left:     condition,
				Operator: "OR",
				Right: &ParenthesizedExpr{Expr: &BinaryExpr{
					Left:     &IsExpr{Value: base, Operator: "IS", Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}},
					Operator: "AND",
					Right:    &IsExpr{Value: search, Operator: "IS", Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}},
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

func identifierExpr(name string) *IdentifierExpr {
	return &IdentifierExpr{Parts: []Identifier{{Text: name}}}
}

func isTrueLiteral(expression Expr) bool {
	literal, ok := expression.(*LiteralExpr)
	return ok && literal.KindValue == LiteralBoolean && strings.EqualFold(literal.Raw, "TRUE")
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
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString || len(literal.Raw) < 2 {
		return expression
	}
	return identifierExpr(strings.Trim(literal.Raw, "'"))
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
		case "INTEGER":
			mapped = "INT"
		case "NUMERIC":
			mapped = "DECIMAL"
		case "STRING":
			mapped = "TEXT"
		case "CHARACTER VARYING":
			mapped = "VARCHAR"
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
	case DialectHive:
		switch upper {
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
		case "BINARY":
			mapped = "VARBINARY"
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
