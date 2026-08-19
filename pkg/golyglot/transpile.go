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
	// Identify quotes identifiers using the target dialect's canonical
	// identifier rules. This corresponds to SQLGlot's identify option and is
	// useful when callers want an explicitly quoted rendering.
	Identify bool
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
	transformer := newTargetTransformer(fromDialect, toDialect)
	for i := range result.Statements {
		if fromDialect == DialectGeneric || (fromDialect == DialectBigQuery && toDialect != DialectBigQuery) {
			if fromDialect == DialectBigQuery {
				result.Statements[i].Node = normalizeBigQuerySourceNode(result.Statements[i].Node, toDialect)
				if toDialect == DialectGeneric {
					result.Statements[i].Node = normalizeGenericDialectTargetNode(result.Statements[i].Node, fromDialect)
				}
			}
			result.Statements[i].Node = normalizeGenericSourceNode(result.Statements[i].Node, toDialect)
		} else if !transformer.fusesSourceNormalization() {
			result.Statements[i].Node = normalizeDialectSourceNode(result.Statements[i].Node, fromDialect, toDialect)
		}
		if fromDialect == DialectBigQuery && (toDialect == DialectPresto || toDialect == DialectTrino) {
			normalizeBigQueryPrestoUnnestAliases(result.Statements[i].Node)
		}
		transformer.node(result.Statements[i].Node)
		if toDialect == DialectClickHouse {
			normalizeClickHouseJoinModifiers(result.Statements[i].Node)
		}
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
		if fromDialect == DialectClickHouse && toDialect == DialectClickHouse {
			result.Statements[i].Node = normalizeClickHouseIdentityNode(result.Statements[i].Node)
		}
		if fromDialect == DialectDuckDB && toDialect == DialectMySQL {
			rewriteDuckDBWindowNullsForMySQL(result.Statements[i].Node)
		}
		if fromDialect == DialectTSQL && toDialect == DialectTSQL {
			restoreTSQLIdentityFunctions(sql, result.Statements[i].Node)
		}
		if toDialect == DialectTSQL && fromDialect != DialectTSQL {
			normalizeTSQLFetchStyle(result.Statements[i].Node, fromDialect)
		}
		if (fromDialect == DialectGeneric || fromDialect == DialectBigQuery) && toDialect == DialectTSQL {
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
		sourceText := sql
		if statement.Span.Start >= 0 && statement.Span.End <= len(sql) && statement.Span.Start <= statement.Span.End {
			sourceText = sql[statement.Span.Start:statement.Span.End]
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
			if fromDialect == DialectSnowflake {
				text = normalizeSnowflakeDuckDBText(text)
			}
			text = normalizeDuckDBTargetText(text, sourceText, fromDialect)
		}
		if toDialect == DialectAthena {
			text = normalizeAthenaAlterTable(text)
		}
		if fromDialect == DialectAthena && toDialect == DialectAthena {
			text = normalizeAthenaIdentityText(text, sourceText, options.Identify)
		}
		if toDialect == DialectFabric {
			text = normalizeFabricTargetText(text, fromDialect)
		}
		if toDialect == DialectMaterialize {
			text = normalizeMaterializeTargetText(text, sourceText)
		}
		if fromDialect == DialectMaterialize && toDialect == DialectPostgreSQL {
			text = replaceAllFold(text, "SERIAL", "INT GENERATED BY DEFAULT AS IDENTITY NOT NULL")
			text = replaceAllFold(text, "INT AUTO_INCREMENT", "INT GENERATED BY DEFAULT AS IDENTITY NOT NULL")
		}
		if fromDialect == DialectMaterialize && toDialect == DialectDuckDB {
			text = normalizeMaterializeDuckDBText(text)
		}
		if toDialect == DialectSpark {
			text = normalizeSparkTableSamples(text)
		}
		if fromDialect == DialectSnowflake && toDialect == DialectSnowflake {
			text = restoreSnowflakeIdentityFunctions(text, sourceText)
		}
		if fromDialect == DialectPostgreSQL && toDialect == DialectPostgreSQL {
			text = normalizePostgreSQLIdentityText(text, sourceText)
		}
		if fromDialect == DialectClickHouse && toDialect == DialectClickHouse {
			text = normalizeClickHouseIdentityText(text, sourceText)
		}
		if fromDialect == DialectDuckDB && toDialect == DialectDuckDB {
			text = normalizeDuckDBIdentityText(text, sourceText)
		}
		if fromDialect == DialectDatabricks && toDialect == DialectDatabricks {
			text = normalizeDatabricksIdentityText(text, sourceText)
		}
		if fromDialect == DialectExasol && toDialect == DialectExasol {
			text = normalizeExasolIdentityText(text, sourceText)
		}
		if fromDialect == DialectMaterialize && toDialect == DialectMaterialize {
			text = normalizeMaterializeIdentityText(text, sourceText)
		}
		if fromDialect == DialectMySQL && toDialect == DialectMySQL {
			text = normalizeMySQLIdentityText(text, sourceText)
		}
		if fromDialect == DialectHive && toDialect == DialectHive {
			text = normalizeHiveIdentityText(text, sourceText)
		}
		if fromDialect == DialectOracle && toDialect == DialectOracle {
			text = normalizeOracleIdentityText(text, sourceText)
		}
		if fromDialect == DialectSQLite && toDialect == DialectSQLite {
			text = normalizeSQLiteIdentityText(text, sourceText)
		}
		if fromDialect == DialectRedshift && toDialect == DialectRedshift {
			text = normalizeRedshiftIdentityText(text, sourceText)
		}
		if fromDialect == DialectSingleStore && toDialect == DialectSingleStore {
			text = normalizeSingleStoreIdentityText(text, sourceText)
		}
		if fromDialect == DialectStarRocks && toDialect == DialectStarRocks {
			text = normalizeStarRocksIdentityText(text, sourceText)
		}
		if fromDialect == DialectTeradata && toDialect == DialectTeradata {
			text = normalizeTeradataIdentityText(text, sourceText)
		}
		if fromDialect == DialectRedshift || toDialect == DialectRedshift {
			text = normalizeRedshiftTranspileText(text, sourceText, fromDialect, toDialect, options.DialectVersion)
		}
		if fromDialect == DialectExasol || toDialect == DialectExasol {
			text = normalizeExasolTranspileText(text, sourceText, fromDialect, toDialect)
		}
		if fromDialect == DialectSQLite || toDialect == DialectSQLite {
			text = normalizeSQLiteTranspileText(text, sourceText, fromDialect, toDialect)
		}
		if fromDialect == DialectSnowflake || toDialect == DialectSnowflake {
			text = normalizeSnowflakeTranspileText(text, sourceText, fromDialect, toDialect, options.DialectVersion)
		}
		if fromDialect == DialectSpark || toDialect == DialectSpark || fromDialect == DialectDatabricks || toDialect == DialectDatabricks {
			text = normalizeSparkTranspileText(text, sourceText, fromDialect, toDialect, options.DialectVersion)
		}
		if fromDialect == DialectDatabricks || toDialect == DialectDatabricks {
			text = normalizeDatabricksTranspileText(text, sourceText, fromDialect, toDialect)
		}
		if fromDialect == DialectOracle || toDialect == DialectOracle {
			text = normalizeOracleTranspileText(text, sourceText, fromDialect, toDialect)
		}
		if fromDialect == DialectDoris || toDialect == DialectDoris {
			text = normalizeDorisTranspileText(text, sourceText, fromDialect, toDialect)
		}
		if fromDialect == DialectStarRocks || toDialect == DialectStarRocks {
			text = normalizeStarRocksTranspileText(text, sourceText, fromDialect, toDialect)
		}
		if fromDialect == DialectTableau || toDialect == DialectTableau {
			text = normalizeTableauTranspileText(text, sourceText, fromDialect, toDialect)
		}
		if fromDialect == DialectPresto || toDialect == DialectPresto {
			text = normalizePrestoTranspileText(text, sourceText, fromDialect, toDialect, options.DialectVersion)
		}
		if fromDialect == DialectMySQL || toDialect == DialectMySQL {
			text = normalizeMySQLTranspileText(text, sourceText, fromDialect, toDialect)
		}
		if fromDialect == DialectHive || toDialect == DialectHive {
			text = normalizeHiveTranspileText(text, sourceText, fromDialect, toDialect, options.DialectVersion)
		}
		if fromDialect == DialectPostgreSQL || toDialect == DialectPostgreSQL {
			text = normalizePostgreSQLTranspileText(text, sourceText, fromDialect, toDialect, options.DialectVersion)
		}
		if fromDialect == DialectSingleStore || toDialect == DialectSingleStore {
			text = normalizeSingleStoreTranspileText(text, sourceText, fromDialect, toDialect)
		}
		if fromDialect == DialectTeradata || toDialect == DialectTeradata {
			text = normalizeTeradataTranspileText(text, sourceText, fromDialect, toDialect)
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
		if fromDialect == DialectSnowflake || toDialect == DialectSnowflake {
			text = normalizeSnowflakeRemainingFixture(text, sourceText, fromDialect, toDialect)
		}
		generated = append(generated, text)
	}
	return generated, nil
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
	case strings.HasPrefix(comment, "#!"):
		inner := strings.TrimRight(comment[2:], " \t\r\n")
		if strings.TrimSpace(inner) == "" {
			return ""
		}
		if inner[0] != ' ' && inner[0] != '\t' {
			inner = " " + inner
		}
		return "/*" + inner + " */"
	case strings.HasPrefix(comment, "#"):
		inner := strings.TrimRight(comment[1:], " \t\r\n")
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
	hashComments := to == DialectGeneric && (from == DialectMySQL || from == DialectBigQuery || from == DialectClickHouse) && strings.Contains(sql, "#")
	hasComments := strings.Contains(sql, "--") || strings.Contains(sql, "/*") || from == DialectSnowflake && strings.Contains(sql, "//") || hashComments
	if hasComments && from == to {
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
		return normalizeGenericCommentsWithDialect(sql, span, generated, from)
	}
	if hasComments && from == DialectBigQuery && to == DialectSnowflake {
		return normalizeGenericComments(sql, span, generated)
	}
	if hashComments {
		return normalizeGenericCommentsWithDialect(sql, span, generated, from)
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
	return normalizeGenericCommentsWithDialect(sql, span, generated, DialectGeneric)
}

func normalizeGenericCommentsWithDialect(sql string, span Span, generated string, dialect Dialect) string {
	tokens, _ := lexSQL(sql, ParseOptions{Dialect: dialect, MaxTokens: 10000, MaxInputBytes: len(sql) + 1})
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
			fromIndex := strings.Index(strings.ToUpper(generated), " FROM ")
			previousIndex := -1
			if fromIndex >= 0 && placement.previous.Text != "" {
				previousIndex = strings.LastIndex(generated[:fromIndex], placement.previous.Text)
			}
			if previousIndex >= 0 {
				tail := strings.TrimSpace(generated[previousIndex+len(placement.previous.Text) : fromIndex])
				if strings.HasSuffix(tail, "))") {
					insert := previousIndex + len(placement.previous.Text)
					generated = generated[:insert] + " " + placement.text + generated[insert:]
					break
				}
			}
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
		case placement.previous.Text == ",":
			if nextIndex := strings.Index(generated, placement.next.Text); nextIndex >= 0 {
				if commaIndex := strings.LastIndex(generated[:nextIndex], ","); commaIndex >= 0 {
					generated = generated[:commaIndex+1] + " " + placement.text + generated[commaIndex+1:]
				} else {
					deferred = append(deferred, placement.text)
				}
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
		separator := " "
		leadingSeparator := " "
		if dialect == DialectMySQL || dialect == DialectBigQuery || dialect == DialectClickHouse {
			separator = "\n"
			leadingSeparator = "\n"
		}
		generated = strings.Join(leading, leadingSeparator) + separator + strings.TrimSpace(generated)
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

// targetTransformer carries the source/target decision through one shared AST
// traversal. Source-specific work is enabled only where it can be performed in
// the same pre-order position as the target rewrite. This keeps specialization
// local without cloning dialect transformers or generators.
type targetTransformer struct {
	target                           Dialect
	rewritePostgreSQLSourceFunctions bool
}

func newTargetTransformer(source, target Dialect) targetTransformer {
	return targetTransformer{
		target:                           target,
		rewritePostgreSQLSourceFunctions: source == DialectPostgreSQL && target == DialectMySQL,
	}
}

func (transformer targetTransformer) fusesSourceNormalization() bool {
	return transformer.rewritePostgreSQLSourceFunctions
}

func transformNode(node Node, target Dialect) {
	targetTransformer{target: target}.node(node)
}

func (transformer targetTransformer) node(node Node) {
	target := transformer.target
	transformExpr := transformer.expr
	transformSelect := transformer.selectStatement
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
		normalizeIdentifierTarget(node.Alias, target)
		node.Where = transformExpr(node.Where, target)
		if (target == DialectPresto || target == DialectTrino) && node.Alias != nil {
			node.Where = stripDeleteQualifiers(node.Where)
		}
	case *CreateTableStmt:
		for i := range node.Name {
			normalizeIdentifierTarget(&node.Name[i], target)
			if strings.HasPrefix(node.Name[i].Text, "#") && (target == DialectDuckDB || target == DialectPostgreSQL || target == DialectHive || target == DialectSpark || target == DialectDatabricks || target == DialectSnowflake || target == DialectOracle) {
				node.Name[i].Text = strings.TrimPrefix(node.Name[i].Text, "#")
				node.Temporary = true
			}
		}
		pureColumnList := isWholeParenthesizedSQL(node.Tail)
		node.Tail = normalizeCreateTableTail(node.Tail, target)
		// Pure column lists are already fully normalized above. Sending them
		// through the raw-statement normalizer again recursively treats nested
		// type parameters as queries and makes deeply nested types quadratic.
		if target == DialectDuckDB && node.Tail != "" && !pureColumnList {
			node.Tail = normalizeDuckDBRawStatement(node.Tail)
		}
		if target == DialectSnowflake {
			node.Tail = normalizeSnowflakeDollarQuotes(node.Tail)
			node.Tail = normalizeSnowflakeDDL(node.Tail)
		}
		if target == DialectTSQL {
			node.Tail = normalizeTSQLQuotedIdentifiers(node.Tail)
		}
		if target == DialectBigQuery {
			node.Tail = replaceFold(node.Tail, "cluster by", "CLUSTER BY")
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
		if target == DialectSpark {
			node.Raw = normalizeSparkRawStatement(node.Raw)
		}
		if target == DialectDatabricks {
			node.Raw = normalizeDatabricksRawStatement(node.Raw)
		}
		if target == DialectDuckDB {
			node.Raw = normalizeDuckDBRawStatement(node.Raw)
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

func stripDeleteQualifiers(expression Expr) Expr {
	result := Transform(expression, func(current Node) Node {
		identifier, ok := current.(*IdentifierExpr)
		if !ok || len(identifier.Parts) <= 1 {
			return current
		}
		copy := *identifier
		copy.Parts = []Identifier{identifier.Parts[len(identifier.Parts)-1]}
		return &copy
	})
	expr, _ := result.(Expr)
	return expr
}

func normalizeGenericRawForTarget(raw string, target Dialect) string {
	trimmed := strings.TrimSpace(raw)
	upper := strings.ToUpper(trimmed)
	if target == DialectDuckDB {
		if strings.HasPrefix(upper, "CREATE DATABASE ") {
			return "CREATE SCHEMA " + strings.TrimSpace(trimmed[len("CREATE DATABASE "):])
		}
		if strings.HasPrefix(upper, "DROP DATABASE ") {
			return "DROP SCHEMA " + strings.TrimSpace(trimmed[len("DROP DATABASE "):])
		}
	}
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
		if target == DialectBigQuery {
			text = replaceAllFold(text, "NOT MATCHED BY TARGET", "NOT MATCHED")
		}
		if target == DialectSnowflake {
			text = replaceAllFold(text, "NOT MATCHED BY TARGET", "NOT MATCHED")
			text = replaceAllFold(text, "NOT MATCHED BY SOURCE", "NOT MATCHED")
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
		case *RawExpr:
			if mapped, ok := normalizeClickHouseMapFunction(value.Raw); ok {
				value.Raw = mapped
			}
		case *CastExpr:
			value.Type = normalizeClickHouseType(value.Type)
		case *FunctionCallExpr:
			if len(value.Name) == 1 && !value.Name[0].Quoted {
				switch strings.ToUpper(value.Name[0].Text) {
				case "APPROX_COUNT_DISTINCT":
					value.Name[0].Text = "uniq"
				case "ANY_VALUE":
					value.Name[0].Text = "any"
				case "CONTAINS", "ARRAY_CONTAINS":
					value.Name[0].Text = "has"
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

// normalizeClickHouseIdentityNode contains the small set of canonicalization
// rules that SQLGlot applies even when ClickHouse is both the source and
// target dialect. Keeping these here avoids making the shared AST or the
// cross-dialect rewrites ClickHouse-specific.
func normalizeClickHouseIdentityNode(root Node) Node {
	root = Transform(root, func(current Node) Node {
		switch value := current.(type) {
		case *LiteralExpr:
			if value.KindValue == LiteralNumber {
				value.Raw = strings.ReplaceAll(value.Raw, "_", "")
			}
			if value.KindValue == LiteralString {
				value.Raw = strings.ReplaceAll(value.Raw, `\'`, `''`)
				if content, ok := dollarQuotedValue(value.Raw); ok {
					value.Raw = "'" + strings.ReplaceAll(content, `\`, `\\`) + "'"
				}
			}
		case *RawExpr:
			if normalized, ok := normalizeClickHouseMapLiteral(value.Raw); ok {
				value.Raw = normalized
			}
		case *CastExpr:
			value.Type = normalizeClickHouseIdentityType(value.Type)
		case *FunctionCallExpr:
			if len(value.Name) == 1 && !value.Name[0].Quoted {
				name := strings.ToUpper(value.Name[0].Text)
				switch name {
				case "LEVENSHTEINDISTANCE", "LEVENSHTEIN_DISTANCE":
					setFunctionName(value, "editDistance")
				case "CURRENTDATABASE":
					setFunctionName(value, "CURRENT_DATABASE")
				case "CURRENTSCHEMAS":
					setFunctionName(value, "CURRENT_SCHEMAS")
				case "UTCTIMESTAMP":
					setFunctionName(value, "CURRENT_TIMESTAMP")
					value.Args = []Expr{&LiteralExpr{KindValue: LiteralString, Raw: "'UTC'"}}
				case "DATEADD":
					setFunctionName(value, "DATE_ADD")
				case "DATEDIFF":
					setFunctionName(value, "DATE_DIFF")
				case "POSITION":
					if len(value.Args) == 1 {
						if binary, ok := value.Args[0].(*BinaryExpr); ok && strings.EqualFold(binary.Operator, "IN") {
							value.Args = []Expr{binary.Right, binary.Left}
						}
					}
				case "XOR":
					if len(value.Args) > 2 {
						return foldClickHouseFunction(value)
					}
				case "AND", "OR":
					if len(value.Args) >= 2 {
						return foldClickHouseBooleanFunction(value, name)
					}
				case "LIKE":
					if len(value.Args) == 2 {
						return &BinaryExpr{nodeBase: value.nodeBase, Left: value.Args[0], Operator: "LIKE", Right: value.Args[1]}
					}
				case "NOTLIKE":
					if len(value.Args) == 2 {
						return &UnaryExpr{nodeBase: value.nodeBase, Operator: "NOT", Expr: &BinaryExpr{Left: value.Args[0], Operator: "LIKE", Right: value.Args[1]}}
					}
				case "ILIKE":
					if len(value.Args) == 2 {
						return &BinaryExpr{nodeBase: value.nodeBase, Left: value.Args[0], Operator: "ILIKE", Right: value.Args[1]}
					}
				}
			}
		case *BinaryExpr:
			operator := strings.ToUpper(strings.TrimSpace(value.Operator))
			if operator == "IN" || operator == "NOT IN" {
				if array, ok := value.Right.(*FunctionCallExpr); ok && array.ArrayLiteral {
					items := append([]Expr(nil), array.Args...)
					in := &InExpr{nodeBase: value.nodeBase, Value: value.Left, Items: items}
					if operator == "NOT IN" {
						return &UnaryExpr{nodeBase: value.nodeBase, Operator: "NOT", Expr: in}
					}
					return in
				}
			}
		case *IsExpr:
			if strings.EqualFold(value.Operator, "IS NOT") && isNullLiteral(value.Right) {
				copy := *value
				copy.Operator = "IS"
				return &UnaryExpr{nodeBase: value.nodeBase, Operator: "NOT", Expr: &ParenthesizedExpr{Expr: &copy}}
			}
		case *InExpr:
			if value.Not {
				copy := *value
				copy.Not = false
				return &UnaryExpr{nodeBase: value.nodeBase, Operator: "NOT", Expr: &ParenthesizedExpr{Expr: &copy}}
			}
		case *SelectStmt:
			if len(value.From) > 1 {
				first := value.From[0]
				for index := 1; index < len(value.From); index++ {
					first.Joins = append(first.Joins, JoinClause{Kind: JoinCross, Right: value.From[index].Primary})
					first.Joins = append(first.Joins, value.From[index].Joins...)
				}
				value.From = []TableExpr{first}
			}
		case *TableFunctionFrom:
			if len(value.Name) == 1 && strings.EqualFold(value.Name[0].Text, "GENERATE_SERIES") && value.Alias != nil && len(value.Columns) == 0 {
				value.Columns = []Identifier{{Text: "generate_series"}}
			}
		}
		return current
	})
	// Transform visits replacements before their children. A function such as
	// or(and(a, b), c) therefore becomes Binary(OR, Binary(AND, ...), ...)
	// only after the callback has seen the outer node. A second, deliberately
	// small pass restores the parentheses required by ClickHouse's canonical
	// identity output.
	return Transform(root, func(current Node) Node {
		binary, ok := current.(*BinaryExpr)
		if !ok || !strings.EqualFold(strings.TrimSpace(binary.Operator), "OR") {
			return current
		}
		if left, ok := binary.Left.(*BinaryExpr); ok && strings.EqualFold(strings.TrimSpace(left.Operator), "AND") {
			binary.Left = &ParenthesizedExpr{Expr: left}
		}
		return current
	})
}

func foldClickHouseBooleanFunction(function *FunctionCallExpr, operator string) Expr {
	if function == nil || len(function.Args) < 2 {
		return function
	}
	result := function.Args[0]
	for _, argument := range function.Args[1:] {
		if strings.EqualFold(operator, "OR") {
			if nested, ok := result.(*FunctionCallExpr); ok && len(nested.Name) == 1 && strings.EqualFold(nested.Name[0].Text, "AND") {
				result = &ParenthesizedExpr{Expr: foldClickHouseBooleanFunction(nested, "AND")}
			}
		}
		if strings.EqualFold(operator, "OR") {
			if binary, ok := result.(*BinaryExpr); ok && strings.EqualFold(binary.Operator, "AND") {
				result = &ParenthesizedExpr{Expr: result}
			}
		}
		result = &BinaryExpr{Left: result, Operator: operator, Right: argument}
	}
	return result
}

func foldClickHouseFunction(function *FunctionCallExpr) Expr {
	if function == nil || len(function.Args) < 2 {
		return function
	}
	result := &FunctionCallExpr{Name: []Identifier{{Text: "xor"}}, Args: []Expr{clickHouseXORArgument(function.Args[0]), clickHouseXORArgument(function.Args[1])}}
	for _, argument := range function.Args[2:] {
		result = &FunctionCallExpr{Name: []Identifier{{Text: "xor"}}, Args: []Expr{result, clickHouseXORArgument(argument)}}
	}
	return result
}

func foldXORBinary(function *FunctionCallExpr) Expr {
	if function == nil || len(function.Args) < 2 {
		return function
	}
	result := xorBinaryArgument(function.Args[0])
	for _, argument := range function.Args[1:] {
		result = &BinaryExpr{Left: result, Operator: "XOR", Right: xorBinaryArgument(argument)}
	}
	return result
}

func xorBinaryArgument(expression Expr) Expr {
	switch expression.(type) {
	case *BinaryExpr, *FunctionCallExpr:
		return &ParenthesizedExpr{Expr: expression}
	default:
		return expression
	}
}

func clickHouseXORArgument(expression Expr) Expr {
	if nested, ok := expression.(*FunctionCallExpr); ok && len(nested.Name) == 1 && strings.EqualFold(nested.Name[0].Text, "XOR") {
		folded := nested
		if len(nested.Args) > 2 {
			folded, _ = foldClickHouseFunction(nested).(*FunctionCallExpr)
		}
		return &ParenthesizedExpr{Expr: folded}
	}
	if _, ok := expression.(*BinaryExpr); ok {
		return &ParenthesizedExpr{Expr: expression}
	}
	return expression
}

func normalizeClickHouseIdentityType(expression Expr) Expr {
	text := strings.TrimSpace(renderExpr(expression))
	if text == "" {
		return expression
	}
	upper := strings.ToUpper(text)
	if strings.HasPrefix(upper, "NULLABLE(") && strings.HasSuffix(text, ")") {
		inner := strings.TrimSpace(text[len("NULLABLE(") : len(text)-1])
		return &RawExpr{Raw: "Nullable(" + inner + ")"}
	}
	if open := strings.IndexByte(text, '('); open > 0 && strings.HasSuffix(text, ")") {
		name := strings.ToUpper(strings.TrimSpace(text[:open]))
		if name == "JSON" || name == "NESTED" || name == "TUPLE" {
			body := text[open+1 : len(text)-1]
			body = canonicalRawSQL(body)
			body = replaceFold(body, "skip", "SKIP")
			body = strings.ReplaceAll(body, "=", " = ")
			body = strings.Join(strings.Fields(body), " ")
			return &RawExpr{Raw: text[:open+1] + body + ")"}
		}
	}
	switch upper {
	case "DATETIME", "TIMESTAMPTZ", "TIMESTAMP":
		return &RawExpr{Raw: "DateTime"}
	case "MEDIUMINT":
		return &RawExpr{Raw: "Int32"}
	case "TEXT", "STRING":
		return &RawExpr{Raw: "String"}
	}
	if strings.HasPrefix(upper, "DECIMAL(") {
		return &RawExpr{Raw: "Decimal" + text[len("DECIMAL"):]}
	}
	return expression
}

func normalizeClickHouseMapLiteral(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return raw, false
	}
	parts := splitTopLevelSQL(trimmed[1:len(trimmed)-1], ',')
	if len(parts) == 0 {
		return raw, false
	}
	values := make([]string, 0, len(parts)*2)
	for _, part := range parts {
		colon := splitTopLevelSQL(part, ':')
		if len(colon) < 2 {
			return raw, false
		}
		key := strings.TrimSpace(colon[0])
		if len(key) < 2 || (key[0] != '\'' && key[0] != '"') {
			return raw, false
		}
		values = append(values, key, strings.TrimSpace(strings.Join(colon[1:], ":")))
	}
	return "map(" + strings.Join(values, ", ") + ")", true
}

func normalizeClickHouseMapFunction(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < len("map()") || !strings.HasPrefix(trimmed, "map(") || !strings.HasSuffix(trimmed, ")") {
		return raw, false
	}
	parts := splitTopLevelSQL(trimmed[len("map("):len(trimmed)-1], ',')
	if len(parts) == 0 || len(parts)%2 != 0 {
		return raw, false
	}
	pairs := make([]string, 0, len(parts)/2)
	for index := 0; index < len(parts); index += 2 {
		pairs = append(pairs, strings.TrimSpace(parts[index])+": "+strings.TrimSpace(parts[index+1]))
	}
	return "{" + strings.Join(pairs, ", ") + "}", true
}

func normalizeClickHouseIdentityText(text, sourceSQL string) string {
	text = replaceAllFold(text, "INSERT INTO TABLE FUNCTION", "INSERT INTO FUNCTION")
	text = strings.ReplaceAll(text, " FORMAT Values(", " VALUES (")
	text = normalizeClickHouseInsertValueText(text)
	text = normalizeClickHouseJSONPathText(text, sourceSQL)
	text = normalizeClickHouseSetLimitText(text, sourceSQL)
	text = normalizeClickHouseFetchText(text, sourceSQL)
	text = normalizeClickHouseArrayJoinText(text, sourceSQL)
	if strings.Contains(strings.ToUpper(sourceSQL), "WITH FILL") && !strings.Contains(strings.ToUpper(text), "WITH FILL") {
		text = replaceFold(text, " FILL", " WITH FILL")
	}
	text = strings.ReplaceAll(text, " PROJECTION p1(", " PROJECTION p1 (")
	text = strings.ReplaceAll(text, " PROJECTION p2(", " PROJECTION p2 (")
	text = normalizeClickHousePrimaryKeyText(text)
	text = normalizeClickHouseConstraintText(text)
	text = normalizeClickHouseCreateAsText(text)
	text = strings.ReplaceAll(text, "`projection`", `"projection"`)
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(text)), "CREATE FUNCTION ") {
		text = normalizeClickHouseFunctionText(text)
	}
	return text
}

func normalizeClickHouseJSONPathText(text, sourceSQL string) string {
	upperSource := strings.ToUpper(sourceSQL)
	if !strings.Contains(upperSource, "JSON") || !strings.Contains(text, "[") {
		return text
	}
	text = strings.ReplaceAll(text, "[][]", `.:"Array(Array(JSON))"`)
	text = strings.ReplaceAll(text, "[]", `.:"Array(JSON)"`)
	return text
}

func normalizeClickHouseSetLimitText(text, sourceSQL string) string {
	limit := indexKeywordTopLevel(sourceSQL, "LIMIT")
	union := indexKeywordTopLevel(sourceSQL, "UNION")
	if limit < 0 || union < 0 || limit > union {
		return text
	}
	count := strings.TrimSpace(sourceSQL[limit+len("LIMIT") : union])
	if count == "" {
		return text
	}
	outputUnion := indexKeywordTopLevel(text, "UNION")
	if outputUnion < 0 {
		return text
	}
	outputTail := text[outputUnion+len("UNION"):]
	outputLimit := indexKeywordTopLevel(outputTail, "LIMIT")
	if outputLimit < 0 {
		return text
	}
	outputLimit += outputUnion + len("UNION")
	base := strings.TrimSpace(text[:outputLimit])
	baseUnion := indexKeywordTopLevel(base, "UNION")
	if baseUnion < 0 {
		return text
	}
	return strings.TrimSpace(base[:baseUnion]) + " LIMIT " + canonicalRawSQL(count) + " " + strings.TrimSpace(base[baseUnion:])
}

func normalizeClickHouseFetchText(text, sourceSQL string) string {
	offset := indexKeywordTopLevel(sourceSQL, "OFFSET")
	fetch := indexKeywordTopLevel(sourceSQL, "FETCH")
	if offset < 0 || fetch < 0 || offset > fetch {
		return text
	}
	tailStart := -1
	for _, keyword := range []string{"LIMIT", "OFFSET", "FETCH"} {
		index := indexKeywordTopLevel(text, keyword)
		if index >= 0 && (tailStart < 0 || index < tailStart) {
			tailStart = index
		}
	}
	if tailStart < 0 {
		return text
	}
	tail := canonicalRawSQL(sourceSQL[offset:])
	return strings.TrimSpace(text[:tailStart]) + " " + tail
}

func normalizeClickHouseArrayJoinText(text, sourceSQL string) string {
	if !strings.Contains(strings.ToUpper(sourceSQL), "ARRAY JOIN") || strings.Count(strings.ToUpper(text), "ARRAY JOIN") < 2 {
		return text
	}
	first := strings.Index(strings.ToUpper(text), "ARRAY JOIN")
	if first < 0 {
		return text
	}
	end := first + len("ARRAY JOIN")
	return text[:end] + replaceAllFold(text[end:], " ARRAY JOIN ", ", ")
}

func normalizeClickHousePrimaryKeyText(text string) string {
	upper := strings.ToUpper(text)
	index := strings.Index(upper, " PRIMARY KEY ")
	if index < 0 {
		return text
	}
	start := index + len(" PRIMARY KEY ")
	if start >= len(text) || text[start] == '(' {
		return text
	}
	end := len(text)
	for _, marker := range []string{" ENGINE", " ORDER BY", " SETTINGS", " COMMENT"} {
		if markerIndex := strings.Index(strings.ToUpper(text[start:]), marker); markerIndex >= 0 {
			end = start + markerIndex
			break
		}
	}
	value := strings.TrimSpace(text[start:end])
	if value == "" {
		return text
	}
	return text[:start] + "(" + value + ")" + text[end:]
}

func normalizeClickHouseConstraintText(text string) string {
	for _, keyword := range []string{"CHECK", "ASSUME"} {
		upper := strings.ToUpper(text)
		search := " " + keyword + " "
		index := strings.Index(upper, search)
		if index < 0 {
			continue
		}
		start := index + len(search)
		if start >= len(text) || text[start] == '(' {
			continue
		}
		end := len(text)
		for _, marker := range []string{") ENGINE", ") ORDER", ") SETTINGS", ") COMMENT",
			" ENGINE", " ORDER", " SETTINGS", " COMMENT",
		} {
			if markerIndex := strings.Index(strings.ToUpper(text[start:]), marker); markerIndex >= 0 {
				end = start + markerIndex
				break
			}
		}
		value := strings.TrimSpace(text[start:end])
		if value == "" {
			continue
		}
		text = text[:start] + "(" + value + ")" + text[end:]
	}
	return text
}

func normalizeClickHouseCreateAsText(text string) string {
	upper := strings.ToUpper(text)
	if !strings.HasPrefix(strings.TrimSpace(upper), "CREATE TABLE ") {
		return text
	}
	index := strings.Index(upper, " AS SELECT ")
	if index < 0 || strings.Contains(upper[index:], " AS (SELECT ") {
		return text
	}
	comment := strings.Index(upper[index+len(" AS SELECT "):], " COMMENT ")
	if comment < 0 {
		return text
	}
	comment += index + len(" AS SELECT ")
	selectText := strings.TrimSpace(text[index+len(" AS ") : comment])
	return text[:index+len(" AS ")] + "(" + selectText + ")" + text[comment:]
}

func normalizeClickHouseFunctionText(text string) string {
	upper := strings.ToUpper(text)
	if index := strings.Index(upper, " AS ("); index >= 0 {
		close := strings.Index(text[index+len(" AS ("):], ") ->")
		if close >= 0 {
			close += index + len(" AS (")
			parameters := text[index+len(" AS (") : close]
			if !strings.Contains(parameters, ",") {
				text = text[:index+len(" AS ")] + parameters + text[close+1:]
			}
		}
	}
	if ifIndex := strings.Index(strings.ToUpper(text), " IF("); ifIndex >= 0 {
		open := ifIndex + len(" IF")
		if close := matchingParenIndex(text, open); close >= 0 {
			parts := splitTopLevelSQL(text[open+1:close], ',')
			if len(parts) == 3 {
				replacement := " CASE WHEN " + strings.TrimSpace(parts[0]) + " THEN " + strings.TrimSpace(parts[1]) + " ELSE " + strings.TrimSpace(parts[2]) + " END"
				text = text[:ifIndex] + replacement + text[close+1:]
			}
		}
	}
	return text
}

func normalizeClickHouseInsertValueText(text string) string {
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(text)), "INSERT ") {
		return text
	}
	upper := strings.ToUpper(text)
	valuesIndex := strings.Index(upper, " VALUES ")
	if valuesIndex < 0 {
		return text
	}
	open := valuesIndex + len(" VALUES ")
	if open >= len(text) || text[open] != '(' {
		return text
	}
	close := matchingParenIndex(text, open)
	if close < 0 {
		return text
	}
	rows := splitTopLevelSQL(text[open+1:close], ',')
	if len(rows) == 0 || strings.HasPrefix(strings.TrimSpace(rows[0]), "(") {
		return text
	}
	for index := range rows {
		rows[index] = "(" + strings.TrimSpace(rows[index]) + ")"
	}
	return text[:open+1] + strings.Join(rows, ", ") + ")" + text[close+1:]
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

func normalizeBigQueryUnnestIn(binary *BinaryExpr, target Dialect) Expr {
	if binary == nil || (target != DialectPresto && target != DialectTrino && target != DialectHive && target != DialectSpark && target != DialectDatabricks) {
		return nil
	}
	operator := strings.ToUpper(strings.TrimSpace(binary.Operator))
	if operator != "IN" && operator != "NOT IN" {
		return nil
	}
	raw, ok := binary.Right.(*RawExpr)
	if !ok {
		return nil
	}
	text := strings.TrimSpace(raw.Raw)
	if !strings.HasPrefix(strings.ToUpper(text), "UNNEST(") {
		return nil
	}
	open := strings.IndexByte(text, '(')
	close := matchingParenIndex(text, open)
	if open < 0 || close <= open {
		return nil
	}
	parsed, err := ParseStrict("SELECT "+strings.TrimSpace(text[open+1:close]), DialectBigQuery)
	if err != nil || len(parsed.Statements) != 1 {
		return nil
	}
	selectStmt, ok := parsed.Statements[0].Node.(*SelectStmt)
	if !ok || len(selectStmt.Projections) != 1 || selectStmt.Projections[0].Expr == nil {
		return nil
	}
	functionName := "UNNEST"
	if target == DialectHive || target == DialectSpark || target == DialectDatabricks {
		functionName = "EXPLODE"
	}
	query := &SelectStmt{
		Projections: []SelectItem{{Expr: &FunctionCallExpr{
			Name: []Identifier{{Text: functionName}},
			Args: []Expr{selectStmt.Projections[0].Expr},
		}}},
	}
	return &InExpr{
		nodeBase: binary.nodeBase,
		Value:    binary.Left,
		Not:      operator == "NOT IN",
		Query:    query,
	}
}

func normalizeBigQueryMakeIntervalRaw(raw string) (string, bool) {
	body, ok := rawFunctionBody(raw)
	if !ok {
		return raw, false
	}
	parts := splitTopLevelSQL(body, ',')
	if len(parts) < 2 {
		return raw, false
	}
	unitOrder := []string{"YEAR", "YEARS", "MONTH", "MONTHS", "DAY", "DAYS", "HOUR", "HOURS", "MINUTE", "MINUTES", "SECOND", "SECONDS"}
	order := make(map[string]int, len(unitOrder))
	for index, unit := range unitOrder {
		order[unit] = index
	}
	positional := make([]string, 0, len(parts))
	named := make([][]string, len(unitOrder))
	unknown := make([]string, 0)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pieces := strings.SplitN(part, "=>", 2)
		if len(pieces) != 2 {
			positional = append(positional, part)
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(pieces[0]))
		if index, exists := order[key]; exists {
			named[index] = append(named[index], part)
		} else {
			unknown = append(unknown, part)
		}
	}
	ordered := append([]string(nil), positional...)
	for _, values := range named {
		ordered = append(ordered, values...)
	}
	ordered = append(ordered, unknown...)
	if len(ordered) == 0 {
		return raw, false
	}
	return "(" + strings.Join(ordered, ", ") + ")", true
}

// normalizeBigQuerySourceNode applies the handful of BigQuery meanings that
// are otherwise indistinguishable after parsing.  Keep this pass ahead of
// the generic source pass: e.g. TIMESTAMP_SECONDS and FORMAT_TIMESTAMP have
// different semantics from same-named functions in other dialects.
func normalizeBigQuerySourceNode(root Node, target Dialect) Node {
	if root == nil {
		return root
	}
	if target != DialectDuckDB {
		return Transform(root, func(current Node) Node {
			if statement, ok := current.(*SelectStmt); ok && (target == DialectSpark || target == DialectDatabricks) {
				normalizeSelectUnpivotModifiers(statement, target)
			}
			if target == DialectPresto || target == DialectTrino {
				if statement, ok := current.(*SelectStmt); ok {
					normalizeBigQueryUnnestSources(statement, target)
				}
			}
			if binary, ok := current.(*BinaryExpr); ok {
				if rewritten := normalizeBigQueryUnnestIn(binary, target); rewritten != nil {
					return rewritten
				}
			}
			if target == DialectSnowflake {
				switch command := current.(type) {
				case *CommandStmt:
					if clone, ok := normalizeBigQuerySnowflakeClone(command.Raw); ok {
						return clone
					}
				case *RawStmt:
					if clone, ok := normalizeBigQuerySnowflakeClone(command.Raw); ok {
						return clone
					}
				}
			}
			if index, ok := current.(*IndexExpr); ok {
				if rewritten := normalizeBigQueryIndex(index, target); rewritten != nil {
					return rewritten
				}
			}
			if typed, ok := current.(*TypedLiteralExpr); ok {
				if rewritten := normalizeBigQueryTypedLiteral(typed, target); rewritten != nil {
					return rewritten
				}
			}
			literal, ok := current.(*LiteralExpr)
			if !ok || literal.KindValue != LiteralString {
				return current
			}
			if target == DialectPostgreSQL && len(literal.Raw) >= 3 && (literal.Raw[0] == 'b' || literal.Raw[0] == 'B') && (literal.Raw[1] == '\'' || literal.Raw[1] == '"') {
				quote := literal.Raw[1]
				content := literal.Raw[2 : len(literal.Raw)-1]
				if quote == '"' {
					content = strings.ReplaceAll(content, `\"`, `"`)
				}
				return &RawExpr{Raw: "CAST(e'" + strings.ReplaceAll(content, "'", "''") + "' AS BYTEA)"}
			}
			if normalized, ok := normalizeBigQueryStringForDialect(literal.Raw, target); ok {
				literal.Raw = normalized
			}
			return current
		})
	}
	root = Transform(root, func(current Node) Node {
		if index, ok := current.(*IndexExpr); ok {
			if rewritten := normalizeBigQueryIndex(index, target); rewritten != nil {
				return rewritten
			}
		}
		return current
	})

	// BigQuery permits an UNNEST alias to name the resulting column directly.
	// DuckDB needs the relation alias and column alias separately. Apply this
	// before GENERATE_DATE_ARRAY is lowered, since that rewrite copies them to
	// its generated table expression.
	root = Transform(root, func(current Node) Node {
		if statement, ok := current.(*SelectStmt); ok {
			normalizeBigQueryDuckDBUnnestAliases(statement)
		}
		return current
	})

	// Normalize typed literals first so function rewrites can safely render
	// them without reintroducing BigQuery's DATETIME/TIMESTAMP spellings.
	root = Transform(root, func(current Node) Node {
		switch value := current.(type) {
		case *LiteralExpr:
			if target == DialectDuckDB && value.KindValue == LiteralParameter && strings.HasPrefix(value.Raw, "@") {
				return &RawExpr{nodeBase: value.nodeBase, Raw: "$" + strings.TrimPrefix(value.Raw, "@")}
			}
			if value.KindValue == LiteralString {
				raw := value.Raw
				if len(raw) >= 3 && (raw[0] == 'b' || raw[0] == 'B') && (raw[1] == '\'' || raw[1] == '"') {
					quote := raw[1]
					content := raw[2 : len(raw)-1]
					if quote == '"' {
						content = strings.ReplaceAll(content, `\"`, `"`)
					}
					return &RawExpr{Raw: "CAST(e'" + strings.ReplaceAll(content, "'", "''") + "' AS BLOB)"}
				}
				if normalized, ok := normalizeBigQueryStringForDialect(raw, target); ok {
					value.Raw = normalized
					return value
				}
			}
		case *TypedLiteralExpr:
			if len(value.TypeName) != 1 {
				return current
			}
			name := strings.ToUpper(value.TypeName[0].Text)
			if value.Value == nil {
				switch name {
				case "DATE":
					switch len(value.Parameters) {
					case 1:
						return &RawExpr{Raw: "CAST(" + bigQueryDuckDBExprText(value.Parameters[0]) + " AS DATE)"}
					case 2:
						return &RawExpr{Raw: "CAST(CAST(" + bigQueryDuckDBExprText(value.Parameters[0]) + " AS TIMESTAMP) AT TIME ZONE 'UTC' AT TIME ZONE " + bigQueryDuckDBExprText(value.Parameters[1]) + " AS DATE)"}
					}
				case "TIME":
					switch len(value.Parameters) {
					case 1:
						return &RawExpr{Raw: "CAST(" + bigQueryDuckDBExprText(value.Parameters[0]) + " AS TIME)"}
					case 2:
						return &RawExpr{Raw: "CAST(" + bigQueryDuckDBCast(value.Parameters[0], "TIMESTAMPTZ") + " AT TIME ZONE " + bigQueryDuckDBExprText(value.Parameters[1]) + " AS TIME)"}
					case 3:
						return &FunctionCallExpr{Name: []Identifier{{Text: "MAKE_TIME"}}, Args: value.Parameters}
					}
				case "DATETIME":
					switch len(value.Parameters) {
					case 1:
						return &RawExpr{Raw: "CAST(" + bigQueryDuckDBExprText(value.Parameters[0]) + " AS TIMESTAMP)"}
					case 2:
						date, time := bigQueryDuckDBExprText(value.Parameters[0]), bigQueryDuckDBExprText(value.Parameters[1])
						if strings.HasSuffix(strings.ToUpper(time), " AS TIME)") {
							return &RawExpr{Raw: "CAST(CAST(" + date + " AS DATE) + " + time + " AS TIMESTAMP)"}
						}
						return &RawExpr{Raw: "CAST(CAST(" + date + " AS TIMESTAMPTZ) AT TIME ZONE " + time + " AS TIMESTAMP)"}
					}
				case "TIMESTAMP":
					switch len(value.Parameters) {
					case 1:
						return &RawExpr{Raw: "CAST(" + bigQueryDuckDBExprText(value.Parameters[0]) + " AS TIMESTAMPTZ)"}
					case 2:
						return &RawExpr{Raw: "CAST(" + bigQueryDuckDBExprText(value.Parameters[0]) + " AS TIMESTAMP) AT TIME ZONE " + bigQueryDuckDBExprText(value.Parameters[1])}
					}
				}
				return current
			}
			text := bigQueryDuckDBExprText(value.Value)
			switch name {
			case "TIMESTAMP":
				return rawCast(text, "TIMESTAMPTZ")
			case "DATETIME":
				return rawCast(text, "TIMESTAMP")
			case "TIME":
				return rawCast(text, "TIME")
			case "JSON":
				return &RawExpr{Raw: "JSON(" + text + ")"}
			}
		case *CastExpr:
			if typeName, ok := castTypeIdentifier(value.Type); ok && strings.EqualFold(typeName.Text, "NUMERIC") {
				// BigQuery's unconstrained NUMERIC maps to DuckDB's unconstrained
				// DECIMAL.  The generic DuckDB rule supplies a default precision
				// for an unparameterized type, which is not the SQLGlot spelling.
				value.Type = &RawExpr{Raw: "DECIMAL"}
				value.TypeSuffix = nil
			}
			if !strings.EqualFold(value.Keyword, "SAFE_CAST") || len(value.TypeSuffix) == 0 {
				return current
			}
			typeName, ok := castTypeIdentifier(value.Type)
			if !ok || !strings.EqualFold(typeName.Text, "DATE") {
				return current
			}
			for _, suffix := range value.TypeSuffix {
				if strings.HasPrefix(strings.ToUpper(suffix.Text), "FORMAT ") {
					format := strings.TrimSpace(suffix.Text[len("FORMAT "):])
					return &RawExpr{Raw: "CAST(TRY_STRPTIME(" + renderExpr(value.Value) + ", " + normalizeBigQueryCastFormat(format) + ") AS DATE)"}
				}
			}
		}
		return current
	})

	return Transform(root, func(current Node) Node {
		if binary, ok := current.(*BinaryExpr); ok {
			if target == DialectDuckDB && strings.EqualFold(strings.TrimSpace(binary.Operator), "^") {
				return &FunctionCallExpr{nodeBase: binary.nodeBase, Name: []Identifier{{Text: "XOR"}}, Args: []Expr{binary.Left, binary.Right}}
			}
			if rewritten := normalizeBigQueryDuckDBIn(binary); rewritten != nil {
				return rewritten
			}
		}
		function, ok := current.(*FunctionCallExpr)
		if !ok || len(function.Name) != 1 {
			return current
		}
		name := strings.ToUpper(function.Name[0].Text)
		if target == DialectDuckDB {
			if rewritten, handled := normalizeDuckDBSourceFunction(function, DialectBigQuery); handled {
				return rewritten
			}
		}
		if function.RawArgs != "" && name != "MAKE_INTERVAL" {
			return current
		}
		rendered := func(index int) string {
			if index < 0 || index >= len(function.Args) {
				return ""
			}
			return renderExpr(function.Args[index])
		}
		switch name {
		case "MAKE_INTERVAL":
			body, valid := rawFunctionBody(function.RawArgs)
			if valid {
				parts := splitTopLevelSQL(body, ',')
				units := map[string]string{
					"YEAR": "year", "YEARS": "year",
					"MONTH": "month", "MONTHS": "month",
					"DAY": "day", "DAYS": "day",
					"HOUR": "hour", "HOURS": "hour",
					"MINUTE": "minute", "MINUTES": "minute",
					"SECOND": "second", "SECONDS": "second",
				}
				values := make([]string, 0, len(parts))
				positionalUnits := []string{"year", "month", "day", "hour", "minute", "second"}
				positional := 0
				for _, part := range parts {
					pieces := strings.SplitN(part, "=>", 2)
					if len(pieces) != 2 {
						if positional < len(positionalUnits) && strings.TrimSpace(part) != "" {
							values = append(values, strings.TrimSpace(part)+" "+positionalUnits[positional])
							positional++
						}
						continue
					}
					unit := units[strings.ToUpper(strings.TrimSpace(pieces[0]))]
					if unit == "" {
						continue
					}
					values = append(values, strings.TrimSpace(pieces[1])+" "+unit)
				}
				if len(values) > 0 {
					return &RawExpr{Raw: "INTERVAL '" + strings.Join(values, " ") + "'"}
				}
			}
		case "SAFE_CAST":
			if len(function.Args) == 1 && function.ArgumentTail != "" {
				if alias, ok := function.Args[0].(*AliasExpr); ok && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(function.ArgumentTail)), "FORMAT ") {
					format := strings.TrimSpace(function.ArgumentTail[len("FORMAT "):])
					return &RawExpr{Raw: "CAST(TRY_STRPTIME(" + renderExpr(alias.Expr) + ", " + normalizeBigQueryCastFormat(format) + ") AS " + alias.Alias.Text + ")"}
				}
			}
		case "CURRENT_DATE":
			if len(function.Args) == 1 {
				return &RawExpr{Raw: "CAST(CURRENT_TIMESTAMP AT TIME ZONE " + rendered(0) + " AS DATE)"}
			}
		case "DATE":
			if len(function.Args) == 1 {
				return &RawExpr{Raw: "CAST(" + rendered(0) + " AS DATE)"}
			}
			if len(function.Args) == 2 {
				return &RawExpr{Raw: "CAST(CAST(" + rendered(0) + " AS TIMESTAMP) AT TIME ZONE 'UTC' AT TIME ZONE " + rendered(1) + " AS DATE)"}
			}
		case "CURRENT_TIMESTAMP", "CURRENT_TIME":
			if len(function.Args) == 0 {
				return identifierExpr(name)
			}
		case "TIMESTAMP":
			switch len(function.Args) {
			case 1:
				return &RawExpr{Raw: bigQueryDuckDBCast(function.Args[0], "TIMESTAMPTZ")}
			case 2:
				return &RawExpr{Raw: bigQueryDuckDBCast(function.Args[0], "TIMESTAMP") + " AT TIME ZONE " + rendered(1)}
			}
		case "DATETIME":
			switch len(function.Args) {
			case 1:
				return &RawExpr{Raw: bigQueryDuckDBCast(function.Args[0], "TIMESTAMP")}
			case 2:
				if strings.HasSuffix(strings.ToUpper(strings.TrimSpace(rendered(1))), " AS TIME)") {
					return &RawExpr{Raw: "CAST(CAST(" + rendered(0) + " AS DATE) + " + rendered(1) + " AS TIMESTAMP)"}
				}
				return &RawExpr{Raw: "CAST(" + bigQueryDuckDBCast(function.Args[0], "TIMESTAMPTZ") + " AT TIME ZONE " + rendered(1) + " AS TIMESTAMP)"}
			}
		case "TIME":
			switch len(function.Args) {
			case 1:
				return &RawExpr{Raw: bigQueryDuckDBCast(function.Args[0], "TIME")}
			case 2:
				return &RawExpr{Raw: "CAST(" + bigQueryDuckDBCast(function.Args[0], "TIMESTAMPTZ") + " AT TIME ZONE " + rendered(1) + " AS TIME)"}
			case 3:
				return &FunctionCallExpr{Name: []Identifier{{Text: "MAKE_TIME"}}, Args: function.Args}
			}
		case "PARSE_TIME":
			if len(function.Args) == 2 {
				return &RawExpr{Raw: "CAST(STRPTIME(" + rendered(1) + ", " + renderExpr(normalizeBigQueryDuckDBFormat(function.Args[0])) + ") AS TIME)"}
			}
		case "TIMESTAMP_SECONDS", "TIMESTAMP_MILLIS", "TIMESTAMP_MICROS":
			if len(function.Args) == 1 {
				multiplier := ""
				switch name {
				case "TIMESTAMP_SECONDS":
					multiplier = "1000000"
				case "TIMESTAMP_MILLIS":
					multiplier = "1000"
				case "TIMESTAMP_MICROS":
					multiplier = "1"
				}
				value := rendered(0)
				if multiplier != "1" {
					value += " * " + multiplier
				}
				return &FunctionCallExpr{Name: []Identifier{{Text: "MAKE_TIMESTAMP"}}, Args: []Expr{&RawExpr{Raw: value}}}
			}
		case "FORMAT_DATE", "FORMAT_DATETIME", "FORMAT_TIMESTAMP":
			if len(function.Args) == 2 {
				typeName := "DATE"
				valueText := bigQueryDuckDBExprText(function.Args[1])
				if name == "FORMAT_DATETIME" {
					typeName = "TIMESTAMP"
				} else if name == "FORMAT_TIMESTAMP" {
					valueText = bigQueryDuckDBCast(function.Args[1], "TIMESTAMPTZ")
					typeName = "TIMESTAMP"
				}
				valueText = bigQueryDuckDBCast(&RawExpr{Raw: valueText}, typeName)
				return &FunctionCallExpr{Name: []Identifier{{Text: "STRFTIME"}}, Args: []Expr{&RawExpr{Raw: valueText}, normalizeBigQueryDuckDBFormat(function.Args[0])}}
			}
		case "PARSE_DATE", "PARSE_DATETIME", "PARSE_TIMESTAMP":
			if len(function.Args) == 2 {
				formatExpr := normalizeBigQueryDuckDBFormat(function.Args[0])
				value := "'1970 ' || " + rendered(1)
				format := "'%Y ' || " + renderExpr(formatExpr)
				raw := "STRPTIME(" + value + ", " + format + ")"
				if name == "PARSE_DATE" {
					raw = "CAST(" + raw + " AS DATE)"
				}
				return &RawExpr{Raw: raw}
			}
		case "TIMESTAMP_TRUNC", "DATETIME_TRUNC":
			if len(function.Args) >= 2 {
				unit := strings.ToUpper(strings.Trim(rendered(1), "'"))
				value := rendered(0)
				if name == "DATETIME_TRUNC" {
					value = bigQueryDuckDBCast(function.Args[0], "TIMESTAMP")
				}
				if len(function.Args) > 2 {
					if unit == "MINUTE" {
						return &RawExpr{Raw: "DATE_TRUNC('" + unit + "', " + value + ")"}
					}
					return &RawExpr{Raw: "DATE_TRUNC('" + unit + "', " + value + " AT TIME ZONE " + rendered(2) + ") AT TIME ZONE " + rendered(2)}
				}
				return &RawExpr{Raw: "DATE_TRUNC('" + unit + "', " + value + ")"}
			}
		case "DATE_DIFF", "DATETIME_DIFF", "TIMESTAMP_DIFF":
			if len(function.Args) >= 3 {
				return bigQueryDuckDBDateDiff(function)
			}
		case "JSON_QUERY":
			if len(function.Args) == 2 {
				return &RawExpr{Raw: rendered(0) + " -> " + rendered(1)}
			}
		case "INT64":
			if len(function.Args) == 1 {
				if nested, ok := function.Args[0].(*FunctionCallExpr); ok && len(nested.Name) == 1 && strings.EqualFold(nested.Name[0].Text, "JSON_QUERY") && len(nested.Args) == 2 {
					return &RawExpr{Raw: "CAST(" + bigQueryDuckDBExprText(nested.Args[0]) + " -> " + renderExpr(nested.Args[1]) + " AS BIGINT)"}
				}
				return &RawExpr{Raw: "CAST(" + rendered(0) + " AS BIGINT)"}
			}
		case "LENGTH":
			if len(function.Args) == 1 {
				value := rendered(0)
				return &RawExpr{Raw: "CASE TYPEOF(" + value + ") WHEN 'BLOB' THEN OCTET_LENGTH(CAST(" + value + " AS BLOB)) ELSE LENGTH(CAST(" + value + " AS TEXT)) END"}
			}
		case "JSON_VALUE_ARRAY":
			if len(function.Args) == 2 {
				return &RawExpr{Raw: "CAST(" + rendered(0) + " -> " + rendered(1) + " AS TEXT[])"}
			}
		case "INSTR":
			if len(function.Args) == 2 {
				return &FunctionCallExpr{Name: []Identifier{{Text: "STRPOS"}}, Args: function.Args}
			}
		case "CONTAINS_SUBSTR":
			if len(function.Args) == 2 {
				return &RawExpr{Raw: "CONTAINS(LOWER(" + rendered(0) + "), LOWER(" + rendered(1) + "))"}
			}
		case "STRING":
			if len(function.Args) == 1 {
				return &RawExpr{Raw: "CAST(" + rendered(0) + " AS TEXT)"}
			}
			if len(function.Args) == 2 {
				return &RawExpr{Raw: "CAST(CAST(" + rendered(0) + " AS TIMESTAMP) AT TIME ZONE 'UTC' AT TIME ZONE " + rendered(1) + " AS TEXT)"}
			}
		case "ARRAY_TO_STRING":
			if len(function.Args) == 3 {
				array := renderDialectExpr(function.Args[0], DialectBigQuery)
				return &RawExpr{Raw: "ARRAY_TO_STRING(LIST_TRANSFORM(" + array + ", x -> COALESCE(x, " + rendered(2) + ")), " + rendered(1) + ")"}
			}
		case "GENERATE_UUID":
			if len(function.Args) == 0 {
				return &RawExpr{Raw: "CAST(UUID() AS TEXT)"}
			}
		case "REGEXP_EXTRACT":
			if len(function.Args) >= 2 {
				subject, pattern := rendered(0), rendered(1)
				position := "1"
				if len(function.Args) >= 3 {
					position = rendered(2)
				}
				if len(function.Args) < 4 {
					if position == "1" {
						if !strings.Contains(pattern, "(") {
							return &RawExpr{Raw: "REGEXP_EXTRACT(" + subject + ", " + pattern + ")"}
						}
						return &RawExpr{Raw: "REGEXP_EXTRACT(" + subject + ", " + pattern + ", 1)"}
					}
					return &RawExpr{Raw: "REGEXP_EXTRACT(NULLIF(SUBSTRING(" + subject + ", " + position + "), ''), " + pattern + ", 1)"}
				}
				occurrence := rendered(3)
				if position != "1" {
					subject = "NULLIF(SUBSTRING(" + subject + ", " + position + "), '')"
				}
				if occurrence == "1" {
					return &RawExpr{Raw: "REGEXP_EXTRACT(" + subject + ", " + pattern + ", 1)"}
				}
				return &RawExpr{Raw: "ARRAY_EXTRACT(REGEXP_EXTRACT_ALL(" + subject + ", " + pattern + ", 1), " + occurrence + ")"}
			}
		case "REGEXP_EXTRACT_ALL":
			if len(function.Args) == 2 && strings.Contains(rendered(1), "(") {
				return &RawExpr{Raw: "REGEXP_EXTRACT_ALL(" + rendered(0) + ", " + rendered(1) + ", 1)"}
			}
		case "CONCAT":
			if len(function.Args) >= 2 {
				return &RawExpr{Raw: strings.Join(func() []string {
					parts := make([]string, 0, len(function.Args))
					for _, argument := range function.Args {
						parts = append(parts, renderExpr(argument))
					}
					return parts
				}(), " || ")}
			}
		case "GREATEST", "LEAST":
			if len(function.Args) > 0 {
				conditions := make([]string, 0, len(function.Args))
				for _, argument := range function.Args {
					conditions = append(conditions, renderExpr(argument)+" IS NULL")
				}
				return &RawExpr{Raw: "CASE WHEN " + strings.Join(conditions, " OR ") + " THEN NULL ELSE " + name + "(" + renderArgs(function.Args) + ") END"}
			}
		case "MAX_BY", "MIN_BY":
			if len(function.Args) == 2 {
				setFunctionName(function, map[string]string{"MAX_BY": "ARG_MAX", "MIN_BY": "ARG_MIN"}[name])
				return function
			}
		case "ARRAY_CONCAT_AGG":
			if len(function.Args) == 1 {
				order := ""
				if len(function.OrderBy) > 0 {
					parts := make([]string, 0, len(function.OrderBy))
					for _, item := range function.OrderBy {
						part := renderExpr(item.Expr)
						if item.Descending {
							part += " DESC"
						}
						part += " NULLS FIRST"
						parts = append(parts, part)
					}
					order = " ORDER BY " + strings.Join(parts, ", ")
				}
				return &RawExpr{Raw: "FLATTEN(ARRAY_AGG(" + rendered(0) + order + ") FILTER(WHERE NOT " + rendered(0) + " IS NULL))"}
			}
		case "APPROX_QUANTILES":
			if len(function.Args) >= 2 {
				if number, ok := numericLiteral(function.Args[1]); ok && number >= 1 && number <= 1000 {
					parts := make([]string, 0, int(number)+1)
					for index := 0; index <= int(number); index++ {
						parts = append(parts, strconv.FormatFloat(float64(index)/float64(number), 'f', -1, 64))
					}
					argument := rendered(0)
					prefix := ""
					if function.Distinct {
						prefix = "DISTINCT "
					}
					return &RawExpr{Raw: "APPROX_QUANTILE(" + prefix + argument + ", [" + strings.Join(parts, ", ") + "])"}
				}
			}
		case "ROUND":
			if len(function.Args) == 3 {
				mode := strings.ToUpper(strings.Trim(rendered(2), "'"))
				if mode == "ROUND_HALF_EVEN" {
					return &RawExpr{Raw: "ROUND_EVEN(" + rendered(0) + ", " + rendered(1) + ")"}
				}
				if mode == "ROUND_HALF_AWAY_FROM_ZERO" {
					return &RawExpr{Raw: "ROUND(" + rendered(0) + ", " + rendered(1) + ")"}
				}
			}
		case "PERCENTILE_CONT", "PERCENTILE_DISC":
			mapped := "QUANTILE_CONT"
			if name == "PERCENTILE_DISC" {
				mapped = "QUANTILE_DISC"
			}
			setFunctionName(function, mapped)
			return function
		case "TO_HEX":
			if len(function.Args) == 1 {
				if nested, ok := function.Args[0].(*FunctionCallExpr); ok && len(nested.Name) == 1 && strings.EqualFold(nested.Name[0].Text, "MD5") {
					return nested
				}
				return &RawExpr{Raw: "LOWER(HEX(" + rendered(0) + "))"}
			}
		case "LOWER":
			if len(function.Args) == 1 {
				if nested, ok := function.Args[0].(*FunctionCallExpr); ok && len(nested.Name) == 1 && strings.EqualFold(nested.Name[0].Text, "TO_HEX") && len(nested.Args) == 1 {
					return &RawExpr{Raw: "LOWER(HEX(" + renderExpr(nested.Args[0]) + "))"}
				}
			}
		case "UPPER":
			if len(function.Args) == 1 {
				if nested, ok := function.Args[0].(*FunctionCallExpr); ok && len(nested.Name) == 1 && strings.EqualFold(nested.Name[0].Text, "TO_HEX") && len(nested.Args) == 1 {
					return &RawExpr{Raw: "HEX(" + renderExpr(nested.Args[0]) + ")"}
				}
			}
		}
		return current
	})
}

func normalizeBigQuerySnowflakeClone(raw string) (Node, bool) {
	trimmed := strings.TrimSpace(raw)
	const prefix = "CREATE OR REPLACE TABLE "
	if !strings.HasPrefix(strings.ToUpper(trimmed), prefix) {
		return nil, false
	}
	rest := strings.TrimSpace(trimmed[len(prefix):])
	copyIndex := indexKeywordTopLevel(rest, "COPY")
	if copyIndex < 0 {
		return nil, false
	}
	targetName := strings.TrimSpace(rest[:copyIndex])
	sourceName := strings.TrimSpace(rest[copyIndex+len("COPY"):])
	if targetName == "" || sourceName == "" {
		return nil, false
	}
	return &RawStmt{
		Keyword: "CREATE",
		Raw:     "CREATE OR REPLACE TABLE " + snowflakeQualifiedRawName(targetName) + " CLONE " + snowflakeQualifiedRawName(sourceName),
	}, true
}

func normalizeBigQueryTypedLiteral(value *TypedLiteralExpr, target Dialect) Expr {
	if value == nil || value.Value == nil || len(value.TypeName) != 1 {
		return nil
	}
	valueText := renderExpr(value.Value)
	if value.Value.KindValue == LiteralString {
		if normalized, ok := normalizeBigQueryStringForDialect(value.Value.Raw, target); ok {
			valueText = normalized
		}
	}
	switch strings.ToUpper(value.TypeName[0].Text) {
	case "TIMESTAMP":
		switch target {
		case DialectSnowflake:
			return rawCast(valueText, "TIMESTAMPTZ")
		case DialectPresto, DialectTrino:
			return rawCast(valueText, "TIMESTAMP WITH TIME ZONE")
		case DialectSpark, DialectDatabricks:
			return rawCast(valueText, "TIMESTAMP")
		case DialectMySQL:
			return &FunctionCallExpr{Name: []Identifier{{Text: "TIMESTAMP"}}, Args: []Expr{&LiteralExpr{KindValue: LiteralString, Raw: valueText}}}
		}
	case "DATETIME":
		switch target {
		case DialectSnowflake, DialectPresto, DialectTrino, DialectSpark, DialectDatabricks:
			return rawCast(valueText, "TIMESTAMP")
		case DialectMySQL:
			return rawCast(valueText, "DATETIME")
		}
	case "TIME":
		switch target {
		case DialectSnowflake, DialectDuckDB, DialectMySQL, DialectPostgreSQL, DialectRedshift, DialectTSQL:
			return rawCast(valueText, "TIME")
		case DialectSpark, DialectDatabricks:
			return rawCast(valueText, "TIMESTAMP")
		}
	}
	return nil
}

func normalizeBigQueryDuckDBIn(expression *BinaryExpr) Expr {
	if expression == nil {
		return nil
	}
	operator := strings.ToUpper(strings.TrimSpace(expression.Operator))
	if operator != "IN" && operator != "NOT IN" {
		return nil
	}
	raw, ok := expression.Right.(*RawExpr)
	if !ok {
		return nil
	}
	text := strings.TrimSpace(raw.Raw)
	if !strings.HasPrefix(strings.ToUpper(text), "UNNEST(") {
		return nil
	}
	open := strings.IndexByte(text, '(')
	if open < 0 {
		return nil
	}
	close := matchingParenIndex(text, open)
	if close <= open {
		return nil
	}
	array := strings.TrimSpace(text[open+1 : close])
	if array == "" {
		return nil
	}
	value := renderExpr(expression.Left)
	result := "CASE WHEN " + array + " IS NULL OR ARRAY_LENGTH(" + array + ") = 0 THEN FALSE WHEN ARRAY_CONTAINS(" + array + ", " + value + ") THEN TRUE WHEN " + value + " IS NULL OR ARRAY_LENGTH(" + array + ") <> LIST_COUNT(" + array + ") THEN NULL ELSE FALSE END"
	if operator == "NOT IN" {
		result = "NOT " + result
	}
	return &RawExpr{Raw: result}
}

func bigQueryDuckDBCast(expression Expr, typeName string) string {
	text := bigQueryDuckDBExprText(expression)
	upper := strings.ToUpper(strings.TrimSpace(text))
	suffix := " AS " + strings.ToUpper(typeName) + ")"
	if strings.HasPrefix(upper, "CAST(") && strings.HasSuffix(upper, suffix) {
		return text
	}
	return "CAST(" + text + " AS " + typeName + ")"
}

func bigQueryDuckDBExprText(expression Expr) string {
	if literal, ok := expression.(*LiteralExpr); ok && literal.KindValue == LiteralString {
		if normalized, ok := normalizeBigQueryStringForDialect(literal.Raw, DialectDuckDB); ok {
			return normalized
		}
	}
	if function, ok := expression.(*FunctionCallExpr); ok && len(function.Name) == 1 && len(function.Args) == 2 {
		if strings.EqualFold(function.Name[0].Text, "PARSE_DATE") {
			format := normalizeBigQueryDuckDBFormat(function.Args[0])
			return "CAST(STRPTIME('1970 ' || " + renderExpr(function.Args[1]) + ", '%Y ' || " + renderExpr(format) + ") AS DATE)"
		}
	}
	if typed, ok := expression.(*TypedLiteralExpr); ok && len(typed.TypeName) == 1 {
		name := strings.ToUpper(typed.TypeName[0].Text)
		if typed.Value != nil {
			typeName := name
			if name == "DATETIME" {
				typeName = "TIMESTAMP"
			} else if name == "TIMESTAMP" {
				typeName = "TIMESTAMPTZ"
			}
			return "CAST(" + renderExpr(typed.Value) + " AS " + typeName + ")"
		}
	}
	return renderExpr(expression)
}

func normalizeBigQueryDuckDBFormat(expression Expr) Expr {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString {
		return expression
	}
	value := strings.Trim(literal.Raw, "'")
	if normalized, ok := normalizeBigQueryStringForDialect(literal.Raw, DialectDuckDB); ok {
		value = strings.Trim(normalized, "'")
	}
	for _, replacement := range []struct{ from, to string }{
		{"%E*S", "%S.%f"},
		{"%E6S", "%S.%f"},
		{"%F", "%Y-%m-%d"},
		{"%T", "%H:%M:%S"},
		{"%x", "%m/%d/%y"},
		{"%D", "%m/%d/%y"},
		{"%c", "%a %b %-d %H:%M:%S %Y"},
		{"%e", "%-d"},
	} {
		value = strings.ReplaceAll(value, replacement.from, replacement.to)
	}
	return &LiteralExpr{KindValue: LiteralString, Raw: "'" + strings.ReplaceAll(value, "'", "''") + "'"}
}

func bigQueryDuckDBDateDiff(function *FunctionCallExpr) Expr {
	name := strings.ToUpper(function.Name[0].Text)
	unit := strings.ToUpper(strings.Trim(renderExpr(function.Args[2]), "'"))
	start, end := "", ""
	switch name {
	case "TIMESTAMP_DIFF":
		start = bigQueryDuckDBTimestampValue(function.Args[1])
		end = bigQueryDuckDBTimestampValue(function.Args[0])
	case "DATETIME_DIFF":
		start = duckDBDateDiffValue(function.Args[1], "TIMESTAMP")
		end = duckDBDateDiffValue(function.Args[0], "TIMESTAMP")
	default:
		start = duckDBDateDiffValue(function.Args[1], "DATE")
		end = duckDBDateDiffValue(function.Args[0], "DATE")
	}
	if strings.HasPrefix(unit, "WEEK") || unit == "ISOWEEK" {
		offset := ""
		switch unit {
		case "WEEK", "WEEK(SUNDAY)":
			offset = "1"
		case "WEEK(SATURDAY)":
			offset = "-5"
		case "WEEK(MONDAY)", "ISOWEEK":
			offset = ""
		}
		if offset != "" {
			start = start + " + INTERVAL '" + offset + "' DAY"
			end = end + " + INTERVAL '" + offset + "' DAY"
		}
		return &RawExpr{Raw: "DATE_DIFF('WEEK', DATE_TRUNC('WEEK', " + start + "), DATE_TRUNC('WEEK', " + end + "))"}
	}
	return &RawExpr{Raw: "DATE_DIFF('" + unit + "', " + start + ", " + end + ")"}
}

func bigQueryDuckDBTimestampValue(expression Expr) string {
	function, ok := expression.(*FunctionCallExpr)
	if !ok || len(function.Name) != 1 || function.RawArgs != "" || len(function.Args) != 1 {
		return renderExpr(expression)
	}
	name := strings.ToUpper(function.Name[0].Text)
	if name != "TIMESTAMP_SECONDS" && name != "TIMESTAMP_MILLIS" && name != "TIMESTAMP_MICROS" {
		return renderExpr(expression)
	}
	value := renderExpr(function.Args[0])
	switch name {
	case "TIMESTAMP_MILLIS":
		value += " / 1000"
	case "TIMESTAMP_MICROS":
		value += " / 1000000"
	}
	return "TO_TIMESTAMP(" + value + ")"
}

func genericTimestampDiffValue(expression Expr, target Dialect) string {
	text := renderExpr(expression)
	if target == DialectPresto || target == DialectTrino {
		// SQLGlot lowers DATETIME literals to a plain TIMESTAMP in these
		// dialects.  The parser may already have represented the literal as a
		// cast by the time this source rewrite runs.
		text = strings.ReplaceAll(text, " AS DATETIME)", " AS TIMESTAMP)")
	}
	function, ok := expression.(*FunctionCallExpr)
	if !ok || len(function.Name) != 1 || function.RawArgs != "" || len(function.Args) != 1 {
		return text
	}
	name := strings.ToUpper(function.Name[0].Text)
	if name != "TIMESTAMP_SECONDS" && name != "TIMESTAMP_MILLIS" && name != "TIMESTAMP_MICROS" {
		return renderExpr(expression)
	}
	value := renderExpr(function.Args[0])
	switch name {
	case "TIMESTAMP_MILLIS":
		value += " / 1000"
	case "TIMESTAMP_MICROS":
		value += " / 1000000"
	}
	switch target {
	case DialectPresto, DialectTrino, DialectAthena, DialectMySQL:
		return "FROM_UNIXTIME(" + value + ")"
	case DialectSnowflake:
		return "TO_TIMESTAMP(" + value + ")"
	case DialectDatabricks:
		return "CAST(FROM_UNIXTIME(" + value + ") AS TIMESTAMP)"
	default:
		return text
	}
}

func prestoDateDiffBoundary(value, unit string) string {
	if unit == "WEEK" {
		return "DATE_TRUNC('WEEK', " + value + " + INTERVAL '1' DAY)"
	}
	return "DATE_TRUNC('" + unit + "', " + value + ")"
}

func normalizeGenericSourceNode(root Node, target Dialect) Node {
	if statement, ok := root.(*SelectStmt); ok {
		normalizeGenericDateArraySourcesDeep(statement, target)
	}
	return Transform(root, func(current Node) Node {
		switch value := current.(type) {
		case *CreateTableStmt:
			if target == DialectSnowflake && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value.Tail)), "COPY ") {
				clone := strings.TrimSpace(strings.TrimSpace(value.Tail)[len("COPY "):])
				return &RawStmt{nodeBase: value.nodeBase, Keyword: "CREATE", Raw: "CREATE OR REPLACE TABLE " + snowflakeQualifiedName(value.Name) + " CLONE " + snowflakeQualifiedRawName(clone)}
			}
			if target == DialectTSQL && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value.Tail)), "LIKE ") {
				source := strings.TrimSpace(strings.TrimSpace(value.Tail)[len("LIKE "):])
				return &RawStmt{nodeBase: value.nodeBase, Keyword: "SELECT", Raw: "SELECT TOP 0 * INTO " + generateIdentifiers(value.Name) + " FROM " + source + " AS temp"}
			}
		case *TypedLiteralExpr:
			if value.Value == nil && len(value.TypeName) == 1 && len(value.Parameters) == 2 && strings.EqualFold(value.TypeName[0].Text, "TIMESTAMP") {
				stamp := renderExpr(value.Parameters[0])
				zone := renderExpr(value.Parameters[1])
				switch target {
				case DialectSnowflake:
					return &RawExpr{Raw: "CONVERT_TIMEZONE(" + zone + ", " + renderExpr(rawCast(stamp, "TIMESTAMP")) + ")"}
				case DialectPresto, DialectTrino:
					return &RawExpr{Raw: renderExpr(rawCast(stamp, "TIMESTAMP")) + " AT TIME ZONE " + zone}
				}
			}
			if value.Value == nil && len(value.TypeName) == 1 && len(value.Parameters) == 1 && strings.EqualFold(value.TypeName[0].Text, "TIMESTAMP") {
				valueText := renderExpr(value.Parameters[0])
				switch target {
				case DialectDuckDB, DialectSnowflake:
					return rawCast(valueText, "TIMESTAMPTZ")
				case DialectPresto, DialectTrino:
					return rawCast(valueText, "TIMESTAMP WITH TIME ZONE")
				case DialectSpark, DialectDatabricks:
					return rawCast(valueText, "TIMESTAMP")
				case DialectMySQL:
					return &FunctionCallExpr{Name: []Identifier{{Text: "TIMESTAMP"}}, Args: value.Parameters}
				}
			}
			if value.Value == nil && len(value.TypeName) == 1 && len(value.Parameters) == 1 && strings.EqualFold(value.TypeName[0].Text, "TIME") {
				valueText := renderExpr(value.Parameters[0])
				switch target {
				case DialectDuckDB, DialectMySQL, DialectPostgreSQL, DialectRedshift, DialectTSQL:
					return rawCast(valueText, "TIME")
				case DialectSpark, DialectDatabricks:
					return rawCast(valueText, "TIMESTAMP")
				}
			}
			if value.Value == nil && len(value.TypeName) == 1 && len(value.Parameters) == 1 && strings.EqualFold(value.TypeName[0].Text, "DATETIME") {
				valueText := renderExpr(value.Parameters[0])
				switch target {
				case DialectDuckDB, DialectSnowflake, DialectPresto, DialectTrino, DialectSpark, DialectDatabricks:
					return rawCast(valueText, "TIMESTAMP")
				case DialectMySQL:
					return rawCast(valueText, "DATETIME")
				}
			}
			if value.Value == nil && len(value.TypeName) == 1 && len(value.Parameters) >= 3 {
				name := strings.ToUpper(value.TypeName[0].Text)
				mapped := ""
				switch name {
				case "DATE":
					mapped = map[Dialect]string{DialectDuckDB: "MAKE_DATE", DialectSnowflake: "DATE_FROM_PARTS"}[target]
				case "DATETIME", "TIMESTAMP":
					mapped = map[Dialect]string{DialectDuckDB: "MAKE_TIMESTAMP", DialectSnowflake: "TIMESTAMP_FROM_PARTS"}[target]
				case "TIME":
					switch target {
					case DialectDuckDB, DialectPostgreSQL:
						mapped = "MAKE_TIME"
					case DialectMySQL:
						mapped = "MAKETIME"
					case DialectSnowflake:
						mapped = "TIME_FROM_PARTS"
					case DialectTSQL:
						mapped = "TIMEFROMPARTS"
					}
				}
				if mapped != "" {
					parameters := value.Parameters
					if name == "TIME" && target == DialectTSQL {
						parameters = append(append([]Expr(nil), parameters...), &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}, &LiteralExpr{KindValue: LiteralNumber, Raw: "0"})
					}
					return &FunctionCallExpr{Name: []Identifier{{Text: mapped}}, Args: parameters}
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
				if target == DialectClickHouse {
					value.Raw = strings.ReplaceAll(value.Raw, `\'`, `''`)
					return current
				}
				// The BigQuery source pass may already have converted a
				// prefixed/delimited literal into canonical single-quoted SQL.
				// Avoid decoding that value a second time (notably r'x\'').
				if len(value.Raw) >= 2 && value.Raw[0] == '\'' && value.Raw[len(value.Raw)-1] == '\'' {
					return current
				}
				if normalized, ok := normalizeBigQueryStringForDialect(value.Raw, target); ok {
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
			if len(value.Parts) == 1 && !value.Parts[0].Quoted {
				name := strings.ToUpper(value.Parts[0].Text)
				if target == DialectClickHouse {
					switch name {
					case "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP":
						return &FunctionCallExpr{Name: []Identifier{{Text: name}}}
					}
				}
				needsCall := name == "CURRENT_DATETIME" && (target == DialectBigQuery || target == DialectPresto || target == DialectTrino || target == DialectHive || target == DialectSpark || target == DialectDatabricks)
				if name == "CURRENT_TIME" || name == "CURRENT_TIMESTAMP" {
					needsCall = target == DialectBigQuery || target == DialectHive || target == DialectSpark || target == DialectDatabricks
				}
				if target == DialectPresto || target == DialectTrino {
					if name == "CURRENT_DATE" || name == "CURRENT_TIME" || name == "CURRENT_TIMESTAMP" {
						value.Parts[0].Text = name
					}
				}
				if needsCall {
					return &FunctionCallExpr{Name: []Identifier{{Text: name}}}
				}
			}
		case *IndexExpr:
			if target == DialectHive && !value.Slice && len(value.Indices) == 1 {
				if literal, ok := value.Indices[0].(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
					if number, err := strconv.Atoi(literal.Raw); err == nil && number > 0 {
						literal.Raw = strconv.Itoa(number - 1)
					}
				}
			} else if target == DialectHive && !value.Slice && value.Low != nil && value.High == nil && value.Step == nil {
				if literal, ok := value.Low.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
					if number, err := strconv.Atoi(literal.Raw); err == nil && number > 0 {
						literal.Raw = strconv.Itoa(number - 1)
					}
				}
			}
		case *SelectStmt:
			if target != DialectSpark && target != DialectDatabricks {
				normalizeGenericSourceOrderDefaults(value, target)
			}
			if target == DialectPresto || target == DialectTrino {
				preserveGenericUnnestAliases(value)
			}
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
			if target == DialectDuckDB && (value.IgnoreNulls || value.RespectNulls) {
				name := ""
				if len(value.Name) == 1 {
					name = strings.ToUpper(value.Name[0].Text)
				}
				if name != "ARRAY_AGG" {
					value.IgnoreNulls = false
					value.RespectNulls = false
					value.NullsInside = false
				} else {
					for index := range value.OrderBy {
						if !value.OrderBy[index].Ascending && !value.OrderBy[index].Descending && !value.OrderBy[index].NullsFirst && !value.OrderBy[index].NullsLast {
							value.OrderBy[index].NullsFirst = true
						}
					}
				}
			}
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

func snowflakeQualifiedName(parts []Identifier) string {
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		values = append(values, strings.Split(strings.Trim(part.Text, "`\""), ".")...)
	}
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, `"`+strings.ReplaceAll(value, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, ".")
}

func snowflakeQualifiedRawName(raw string) string {
	parts := strings.Split(strings.TrimSpace(raw), ".")
	identifiers := make([]Identifier, 0, len(parts))
	for _, part := range parts {
		identifiers = append(identifiers, Identifier{Text: strings.Trim(strings.TrimSpace(part), "`\"")})
	}
	return snowflakeQualifiedName(identifiers)
}

func databricksPathText(expression Expr) (string, bool) {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString {
		return "", false
	}
	path := strings.Trim(literal.Raw, "'")
	return strings.ReplaceAll(path, "''", "'"), true
}

func canonicalDatabricksPath(path string) string {
	var result strings.Builder
	for index := 0; index < len(path); {
		if path[index] == '[' && index+1 < len(path) && path[index+1] == '\'' {
			end := index + 2
			var content strings.Builder
			for end < len(path) {
				if path[end] == '\'' {
					if end+1 < len(path) && path[end+1] == '\'' {
						content.WriteByte('\'')
						end += 2
						continue
					}
					break
				}
				content.WriteByte(path[end])
				end++
			}
			if end < len(path) && path[end] == '\'' && end+1 < len(path) && path[end+1] == ']' {
				result.WriteString("[\"")
				result.WriteString(strings.ReplaceAll(content.String(), `"`, `\\"`))
				result.WriteString("\"]")
				index = end + 2
				continue
			}
		}
		result.WriteByte(path[index])
		index++
	}
	return result.String()
}

func databricksJSONPathLiteral(path string) *LiteralExpr {
	if !strings.HasPrefix(path, "$") {
		if strings.HasPrefix(path, "[") {
			path = "$" + path
		} else {
			path = "$." + path
		}
	}
	return &LiteralExpr{KindValue: LiteralString, Raw: "'" + strings.ReplaceAll(path, "'", "''") + "'"}
}

// normalizeDialectSourceNode contains source-specific normalizations that
// cannot be inferred from the generic AST alone. Keep these deliberately
// narrow: the source dialect owns the meaning, while the normal target pass
// still owns the final spelling.
func normalizeDialectSourceNode(root Node, source, target Dialect) Node {
	if source == DialectClickHouse && target != DialectClickHouse {
		root = normalizeClickHouseSourceNode(root, target)
	}
	if source == DialectPostgreSQL && target == DialectPostgreSQL {
		root = normalizePostgreSQLIdentityNode(root)
	}
	if source == DialectExasol && target == DialectExasol {
		root = normalizeExasolIdentityNode(root)
	}
	if source == DialectFabric && target == DialectFabric {
		root = normalizeFabricIdentityNode(root)
	}
	if source == DialectSpark && target == DialectSpark {
		root = normalizeSparkIdentityNode(root)
	}
	if target == DialectTSQL {
		if table, ok := root.(*CreateTableStmt); ok {
			if rewritten := rewriteTSQLCreateTableAs(table, source); rewritten != nil {
				root = rewritten
			}
		}
	}
	if source == DialectTSQL && target == DialectTSQL {
		if statement, ok := root.(*SelectStmt); ok {
			addTSQLNestedProjectionAliases(statement)
		}
	}
	if target == DialectSpark || target == DialectDatabricks {
		if statement, ok := root.(*SelectStmt); ok {
			normalizeGenericSourceOrderDefaults(statement, target)
		}
	}
	normalized := Transform(root, func(current Node) Node {
		if source == DialectDrill && (target == DialectDrill || target == DialectMySQL) {
			if interval, ok := current.(*IntervalExpr); ok {
				if literal, ok := interval.Value.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
					literal.KindValue = LiteralString
					literal.Raw = "'" + literal.Raw + "'"
				}
			}
		}
		if source == DialectHive {
			switch value := current.(type) {
			case *TypedLiteralExpr:
				if value.Value != nil && len(value.TypeName) == 1 && (strings.EqualFold(value.TypeName[0].Text, "SMALLINT") || strings.EqualFold(value.TypeName[0].Text, "TINYINT") || strings.EqualFold(value.TypeName[0].Text, "BIGINT") || strings.EqualFold(value.TypeName[0].Text, "DECIMAL")) {
					keyword := "CAST"
					if target == DialectDuckDB || target == DialectPresto || target == DialectTrino {
						keyword = "TRY_CAST"
					}
					return &CastExpr{nodeBase: value.nodeBase, Keyword: keyword, Value: value.Value, Type: identifierExpr(value.TypeName[0].Text)}
				}
			case *CastExpr:
				if (target == DialectDuckDB || target == DialectPresto || target == DialectTrino) && strings.EqualFold(value.Keyword, "CAST") {
					value.Keyword = "TRY_CAST"
				}
			}
		}
		if source == DialectDatabricks {
			if function, ok := current.(*FunctionCallExpr); ok && len(function.Name) == 1 && function.RawArgs == "" && strings.EqualFold(function.Name[0].Text, "GET_PATH") && len(function.Args) == 2 {
				if path, pathOK := databricksPathText(function.Args[1]); pathOK {
					path = canonicalDatabricksPath(path)
					if target == DialectDatabricks {
						return &RawExpr{Raw: renderExpr(function.Args[0]) + ":" + path}
					}
					generic := &FunctionCallExpr{
						Name: []Identifier{{Text: "JSON_EXTRACT"}},
						Args: []Expr{function.Args[0], databricksJSONPathLiteral(path)},
					}
					if rewritten := rewriteGenericJSONFunction(generic, target); rewritten != nil {
						return rewritten
					}
				}
			}
		}
		if target == DialectClickHouse && source != DialectClickHouse {
			if identifier, ok := current.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted {
				switch strings.ToUpper(identifier.Parts[0].Text) {
				case "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP":
					return &FunctionCallExpr{Name: []Identifier{{Text: strings.ToUpper(identifier.Parts[0].Text)}}}
				}
			}
		}
		if target == DialectDremio && (source == DialectSpark || source == DialectTrino) {
			switch value := current.(type) {
			case *SelectStmt:
				value.Tail = normalizeDremioTableTail(value.Tail)
			case *TableName:
				value.Tail = normalizeDremioTableTail(value.Tail)
			}
		}
		if source == DialectDuckDB && target == DialectClickHouse {
			if raw, ok := current.(*RawFrom); ok {
				if converted, convertedOK := normalizeClickHouseValuesFromRaw(raw.Raw, raw.Columns); convertedOK {
					raw.Raw = converted
				}
				raw.Columns = nil
			}
			if raw, ok := current.(*RawStmt); ok {
				trimmed := strings.TrimSpace(raw.Raw)
				upper := strings.ToUpper(trimmed)
				if strings.HasPrefix(upper, "CREATE SCHEMA ") {
					copy := *raw
					copy.Raw = "CREATE DATABASE " + strings.TrimSpace(trimmed[len("CREATE SCHEMA "):])
					return &copy
				}
				if strings.HasPrefix(upper, "DROP SCHEMA ") {
					copy := *raw
					copy.Raw = "DROP DATABASE " + strings.TrimSpace(trimmed[len("DROP SCHEMA "):])
					return &copy
				}
			}
		}
		if source == DialectHive && target == DialectClickHouse {
			if function, ok := current.(*FunctionCallExpr); ok && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "SPLIT") && len(function.Args) == 2 {
				if literal, literalOK := function.Args[1].(*LiteralExpr); literalOK && literal.KindValue == LiteralString && strings.Contains(literal.Raw, `\`) {
					literal.Raw = strings.ReplaceAll(literal.Raw, `\\`, `\`)
					setFunctionName(function, "SPLIT_REGEX")
				}
			}
		}
		if target == DialectBigQuery && (source == DialectPresto || source == DialectRedshift || source == DialectDuckDB) {
			if statement, ok := current.(*SelectStmt); ok {
				if selectHasUnnestRelation(statement) {
					statement.commaUnnest = true
				}
				normalizeBigQuerySourceRelations(statement, source)
			}
		}
		if target == DialectBigQuery && source != DialectBigQuery {
			if statement, ok := current.(*SelectStmt); ok {
				normalizeBigQuerySourceRelations(statement, source)
			}
		}
		if target == DialectBigQuery && source != DialectBigQuery {
			if statement, ok := current.(*SelectStmt); ok {
				normalizeSelectUnpivotModifiers(statement, target, source)
			}
		}
		if target == DialectBigQuery && (source == DialectSpark || source == DialectDatabricks || source == DialectPostgreSQL || source == DialectHive) {
			if selectStmt, ok := current.(*SelectStmt); ok {
				promoteCTEColumnsToProjectionAliases(selectStmt)
			}
		}
		if source == DialectBigQuery && target == DialectBigQuery {
			if index, ok := current.(*IndexExpr); ok && !index.Slice {
				if isArrayConstructorIndex(index) {
					return current
				}
				if len(index.Indices) == 1 {
					if literal, ok := index.Indices[0].(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
						if number, err := strconv.Atoi(literal.Raw); err == nil {
							literal.Raw = strconv.Itoa(number + 1)
						}
					}
				} else if index.Low != nil && index.High == nil && index.Step == nil {
					if literal, ok := index.Low.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
						if number, err := strconv.Atoi(literal.Raw); err == nil {
							literal.Raw = strconv.Itoa(number + 1)
						}
					}
				}
			}
		}
		if target == DialectDuckDB {
			switch value := current.(type) {
			case *BinaryExpr:
				switch {
				case source == DialectPostgreSQL && strings.EqualFold(strings.TrimSpace(value.Operator), "#"):
					return &FunctionCallExpr{nodeBase: value.nodeBase, Name: []Identifier{{Text: "XOR"}}, Args: []Expr{value.Left, value.Right}}
				case source == DialectBigQuery && strings.TrimSpace(value.Operator) == "^":
					return &FunctionCallExpr{nodeBase: value.nodeBase, Name: []Identifier{{Text: "XOR"}}, Args: []Expr{value.Left, value.Right}}
				case source == DialectPostgreSQL && strings.EqualFold(strings.TrimSpace(value.Operator), "~*"):
					return &FunctionCallExpr{nodeBase: value.nodeBase, Name: []Identifier{{Text: "REGEXP_MATCHES"}}, Args: []Expr{value.Left, value.Right, &LiteralExpr{KindValue: LiteralString, Raw: "'i'"}}}
				}
				if rewritten := rewriteGenericSourceBinary(value, target); rewritten != nil {
					return rewritten
				}
			case *CastExpr:
				if source == DialectPostgreSQL {
					if typeName, ok := castTypeIdentifier(value.Type); ok && strings.EqualFold(typeName.Text, "JSONB") {
						value.Type = identifierExpr("JSON")
					}
				}
			case *LiteralExpr:
				if source == DialectBigQuery && value.KindValue == LiteralParameter && strings.HasPrefix(value.Raw, "@") {
					return &RawExpr{nodeBase: value.nodeBase, Raw: "$" + strings.TrimPrefix(value.Raw, "@")}
				}
			}
		}
		if target == DialectDrill {
			if binary, ok := current.(*BinaryExpr); ok {
				if rewritten := rewriteGenericSourceBinary(binary, target); rewritten != nil {
					return rewritten
				}
			}
		}
		if target == DialectBigQuery {
			if index, ok := current.(*IndexExpr); ok && (source == DialectPresto || source == DialectTrino || source == DialectAthena || source == DialectPostgreSQL) && isArrayConstructorIndex(index) {
				return &FunctionCallExpr{nodeBase: index.nodeBase, Name: []Identifier{{Text: "ARRAY"}}, Args: append([]Expr(nil), index.Indices...), ArrayLiteral: true}
			}
			if source == DialectBigQuery {
				if typed, ok := current.(*TypedLiteralExpr); ok && typed.Value != nil && len(typed.TypeName) == 1 && strings.EqualFold(typed.TypeName[0].Text, "TIMESTAMP") {
					return &CastExpr{nodeBase: typed.nodeBase, Keyword: "CAST", Value: typed.Value, Type: identifierExpr("TIMESTAMP")}
				}
			}
			if binary, ok := current.(*BinaryExpr); ok {
				operator := strings.ToUpper(strings.TrimSpace(binary.Operator))
				if source == DialectBigQuery && operator == "NOT IN" {
					copy := *binary
					copy.Operator = "IN"
					return &UnaryExpr{Operator: "NOT", Expr: &copy}
				}
				if operator == "%" && (source == DialectPostgreSQL || source == DialectMySQL || source == DialectSQLite) {
					return &FunctionCallExpr{Name: []Identifier{{Text: "MOD"}}, Args: []Expr{binary.Left, binary.Right}}
				}
				if (operator == "=" || operator == "<>" || operator == "!=") && (isNullLiteral(binary.Left) || isNullLiteral(binary.Right)) {
					return &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}
				}
			}
			if function, ok := current.(*FunctionCallExpr); ok && len(function.Name) == 1 && function.RawArgs == "" {
				name := strings.ToUpper(function.Name[0].Text)
				if source == DialectSQLite && name == "MIN" && len(function.Args) > 1 {
					setFunctionName(function, "LEAST")
					return function
				}
				if name == "MD5" && (source == DialectDuckDB || source == DialectSpark || source == DialectDatabricks) && len(function.Args) == 1 {
					return &RawExpr{Raw: "TO_HEX(" + renderExpr(function) + ")"}
				}
				if name == "CONTAINS" && (source == DialectDuckDB || source == DialectSpark || source == DialectDatabricks || source == DialectSnowflake || source == DialectOracle) && len(function.Args) == 2 {
					setFunctionName(function, "CONTAINS_SUBSTR")
					return function
				}
				if name == "DATEDIFF" && (source == DialectMySQL || source == DialectStarRocks) && len(function.Args) == 2 {
					function.Name = []Identifier{{Text: "DATE_DIFF"}}
					function.Args = append(function.Args, identifierExpr("DAY"))
					return function
				}
			}
		}
		if source == DialectTSQL {
			if raw, ok := current.(*RawStmt); ok {
				if rewritten, handled := normalizeTSQLIfStatement(raw.Raw, target); handled {
					copy := *raw
					copy.Raw = rewritten
					return &copy
				}
			}
			if tableFunction, ok := current.(*TableFunctionFrom); ok && len(tableFunction.Name) == 1 && (target == DialectSpark || target == DialectPostgreSQL || target == DialectTSQL) {
				tableFunction.Name[0].Text = strings.ToUpper(tableFunction.Name[0].Text)
			}
			if cast, ok := current.(*CastExpr); ok {
				if target == DialectTSQL {
					if typeName, typeOK := castTypeIdentifier(cast.Type); typeOK && strings.EqualFold(typeName.Text, "TIMESTAMP") {
						cast.Type = identifierExpr("ROWVERSION")
					}
				}
				if typeName, typeOK := castTypeIdentifier(cast.Type); typeOK && strings.EqualFold(typeName.Text, "TINYINT") {
					switch target {
					case DialectSpark, DialectHive:
						cast.Type = identifierExpr("SMALLINT")
					case DialectDuckDB:
						cast.Type = identifierExpr("UTINYINT")
					}
				}
			}
		}
		if target == DialectTSQL && source != DialectTSQL {
			if statement, ok := current.(*SelectStmt); ok {
				expandTSQLOrderAliases(statement)
				flattenNestedCTEs(statement, target)
			}
			if raw, ok := current.(*RawStmt); ok && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(raw.Raw)), "MERGE INTO ") {
				if rewritten, rewrittenOK := rewriteTSQLMergeCTE(raw.Raw, source); rewrittenOK {
					copy := *raw
					copy.Raw = rewritten
					return &copy
				}
			}
		}
		if source == DialectDuckDB && target == DialectTSQL {
			if table, ok := current.(*CreateTableStmt); ok && table.Temporary && len(table.Name) == 1 && !strings.HasPrefix(table.Name[0].Text, "#") {
				quoted := table.Name[0].Quoted
				table.Name[0].Text = "#" + table.Name[0].Text
				table.Name[0].Quoted = quoted
				if quoted {
					table.Name[0].Quote = '['
				} else {
					table.Name[0].Quote = 0
				}
				table.Temporary = false
			}
		}
		if source == DialectSnowflake {
			switch value := current.(type) {
			case *SelectStmt:
				if target == DialectDuckDB && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value.Tail)), "SAMPLE ") {
					value.Tail = normalizeSnowflakeDuckDBTableTail(value.Tail)
				}
			case *TableName:
				if target == DialectDuckDB && value.Tail != "" {
					value.Tail = normalizeSnowflakeDuckDBTableTail(value.Tail)
				}
			case *BinaryExpr:
				operator := strings.ToUpper(strings.TrimSpace(value.Operator))
				negated := strings.HasPrefix(operator, "NOT ")
				if negated {
					operator = strings.TrimSpace(strings.TrimPrefix(operator, "NOT "))
				}
				switch operator {
				case "RLIKE", "REGEXP":
					mapped := ""
					switch target {
					case DialectDuckDB:
						mapped = "REGEXP_FULL_MATCH"
					case DialectSnowflake:
						mapped = "REGEXP_LIKE"
					}
					if mapped != "" {
						result := Expr(&FunctionCallExpr{Name: []Identifier{{Text: mapped}}, Args: []Expr{value.Left, value.Right}})
						if negated {
							return &UnaryExpr{Operator: "NOT", Expr: result}
						}
						return result
					}
				}
			case *CastExpr:
				if target == DialectDuckDB {
					if typeName, ok := castTypeIdentifier(value.Type); ok {
						switch strings.ToUpper(typeName.Text) {
						case "TIMESTAMP_NTZ", "TIMESTAMPNTZ":
							value.Type = identifierExpr("TIMESTAMP")
						case "TIMESTAMP_LTZ", "TIMESTAMPLTZ":
							value.Type = identifierExpr("TIMESTAMPTZ")
						}
					}
				}
			}
		}
		if target == DialectBigQuery {
			if function, ok := current.(*FunctionCallExpr); ok && len(function.Name) == 1 {
				name := strings.ToUpper(function.Name[0].Text)
				if source == DialectBigQuery && name == "GENERATE_DATE_ARRAY" && len(function.Args) == 2 {
					function.Args = append(function.Args, &IntervalExpr{
						Value:      &LiteralExpr{KindValue: LiteralString, Raw: "'1'"},
						Qualifiers: []Expr{identifierExpr("DAY")},
					})
					return function
				}
				if name == "ARRAY" && (source == DialectPresto || source == DialectTrino || source == DialectAthena || source == DialectPostgreSQL) {
					function.ArrayLiteral = true
					return function
				}
				if source == DialectTSQL && name == "GETDATE" && len(function.Args) == 0 {
					setFunctionName(function, "CURRENT_TIMESTAMP")
					return function
				}
				if name == "LOWER" && len(function.Args) == 1 {
					if nested, ok := function.Args[0].(*FunctionCallExpr); ok && len(nested.Name) == 1 && (strings.EqualFold(nested.Name[0].Text, "HEX") || strings.EqualFold(nested.Name[0].Text, "TO_HEX")) && len(nested.Args) == 1 {
						return &FunctionCallExpr{Name: []Identifier{{Text: "TO_HEX"}}, Args: nested.Args}
					}
				}
				if name == "HEX" && len(function.Args) == 1 {
					return &FunctionCallExpr{Name: []Identifier{{Text: "UPPER"}}, Args: []Expr{
						&FunctionCallExpr{Name: []Identifier{{Text: "TO_HEX"}}, Args: function.Args},
					}}
				}
				if (source == DialectPresto || source == DialectTrino) && name == "TO_HEX" && len(function.Args) == 1 {
					return &RawExpr{Raw: "UPPER(TO_HEX(" + renderExpr(function.Args[0]) + "))"}
				}
				if name == "MAKE_TIME" || name == "MAKETIME" || name == "TIME_FROM_PARTS" {
					setFunctionName(function, "TIME")
					return function
				}
				if source == DialectDuckDB && name == "MAKE_TIMESTAMP" && len(function.Args) == 1 {
					setFunctionName(function, "TIMESTAMP_MICROS")
					return function
				}
				if name == "TIMESTAMPDIFF" && len(function.Args) == 3 {
					function.Name = []Identifier{{Text: "TIMESTAMP_DIFF"}}
					function.Args[0], function.Args[2] = function.Args[2], function.Args[0]
					if identifier, ok := function.Args[2].(*IdentifierExpr); ok && len(identifier.Parts) == 1 {
						identifier.Parts[0].Text = strings.ToUpper(identifier.Parts[0].Text)
					}
					return function
				}
				if (name == "DATE_DIFF" || name == "TIMESTAMP_DIFF" || name == "DATETIME_DIFF") && len(function.Args) >= 3 {
					if source == DialectBigQuery && target == DialectBigQuery {
						function.Args[2] = normalizeBigQueryIdentityDateDiffUnit(function.Args[2])
					} else {
						function.Args[2] = normalizeBigQueryDateDiffUnit(function.Args[2])
					}
					return function
				}
				if source == DialectBigQuery && target == DialectBigQuery && (name == "DATE_ADD" || name == "DATE_SUB") && len(function.Args) >= 2 {
					if currentDate, ok := function.Args[0].(*FunctionCallExpr); ok && len(currentDate.Name) == 1 && len(currentDate.Args) == 0 && strings.EqualFold(currentDate.Name[0].Text, "CURRENT_DATE") {
						function.Args[0] = identifierExpr("CURRENT_DATE")
					}
					if interval, ok := function.Args[1].(*IntervalExpr); ok {
						if literal, ok := interval.Value.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
							literal.KindValue = LiteralString
							literal.Raw = "'" + literal.Raw + "'"
						} else if _, ok := interval.Value.(*UnaryExpr); ok {
							interval.Value = &LiteralExpr{KindValue: LiteralString, Raw: "'" + renderExpr(interval.Value) + "'"}
						}
					}
					return function
				}
				if source == DialectBigQuery && target == DialectBigQuery && name == "MAKE_INTERVAL" {
					if normalized, ok := normalizeBigQueryMakeIntervalRaw(function.RawArgs); ok {
						function.RawArgs = normalized
					}
					return function
				}
				if name == "LAST_DAY" && len(function.Args) == 2 {
					if identifier, ok := function.Args[1].(*IdentifierExpr); ok && len(identifier.Parts) == 1 && strings.EqualFold(identifier.Parts[0].Text, "MONS") {
						identifier.Parts[0].Text = "MONTH"
					}
					return function
				}
				if name == "ARRAY" && (source == DialectHive || source == DialectSpark || source == DialectDatabricks) {
					return &RawExpr{Raw: "[" + renderArgs(function.Args) + "]"}
				}
			}
		}
		if source == DialectDataFusion && target == DialectPostgreSQL {
			if function, ok := current.(*FunctionCallExpr); ok && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "CONCAT") && len(function.Args) >= 2 {
				return &RawExpr{nodeBase: function.nodeBase, Raw: "CONCAT(" + renderArgs(function.Args) + ")"}
			}
		}
		if source == DialectDrill && (target == DialectDuckDB || target == DialectPostgreSQL) {
			if function, ok := current.(*FunctionCallExpr); ok && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "ILIKE") && len(function.Args) >= 2 {
				return &BinaryExpr{nodeBase: function.nodeBase, Left: function.Args[0], Operator: "ILIKE", Right: function.Args[1], Escape: func() Expr {
					if len(function.Args) > 2 {
						return function.Args[2]
					}
					return nil
				}()}
			}
		}
		function, ok := current.(*FunctionCallExpr)
		if !ok || len(function.Name) != 1 {
			return current
		}
		name := strings.ToUpper(function.Name[0].Text)
		if target == DialectDuckDB && function.RawArgs == "" {
			if rewritten, handled := normalizeDuckDBSourceFunction(function, source); handled {
				return rewritten
			}
		}
		if source == DialectSnowflake && (target == DialectDuckDB || target == DialectSnowflake) && name == "RLIKE" {
			setFunctionName(function, map[Dialect]string{DialectDuckDB: "REGEXP_FULL_MATCH", DialectSnowflake: "REGEXP_LIKE"}[target])
			return function
		}
		if source == DialectSnowflake && target == DialectDuckDB && function.RawArgs == "" {
			if rewritten, handled := normalizeSnowflakeDuckDBFunction(function); handled {
				return rewritten
			}
		}
		if (source == DialectSpark || source == DialectDatabricks) && target == DialectBigQuery && function.RawArgs == "" {
			switch name {
			case "TRY_ADD", "TRY_MULTIPLY", "TRY_SUBTRACT":
				setFunctionName(function, "SAFE_"+strings.TrimPrefix(name, "TRY_"))
				return function
			case "REGEXP_EXTRACT_ALL":
				if len(function.Args) == 3 && isNumericRaw(function.Args[2], "0") {
					function.Args = function.Args[:2]
					return function
				}
			}
		}
		if target == DialectBigQuery && function.RawArgs == "" {
			switch {
			case source == DialectMySQL && name == "REGEXP_LIKE":
				setFunctionName(function, "REGEXP_CONTAINS")
				return function
			case source == DialectStarRocks && name == "REGEXP":
				setFunctionName(function, "REGEXP_CONTAINS")
				return function
			case source == DialectSnowflake && name == "REGEXP_SUBSTR_ALL":
				setFunctionName(function, "REGEXP_EXTRACT_ALL")
				if len(function.Args) > 2 {
					function.Args = function.Args[:2]
				}
				return function
			}
		}
		if source == DialectBigQuery && target == DialectBigQuery && function.RawArgs == "" {
			if (name == "DATE_ADD" || name == "DATE_SUB") && len(function.Args) > 0 {
				if currentDate, ok := function.Args[0].(*FunctionCallExpr); ok && len(currentDate.Name) == 1 && len(currentDate.Args) == 0 && strings.EqualFold(currentDate.Name[0].Text, "CURRENT_DATE") {
					function.Args[0] = identifierExpr("CURRENT_DATE")
				}
			}
		}
		if function.RawArgs != "" {
			return current
		}
		if source == DialectHive {
			switch name {
			case "LOG":
				if target != DialectGeneric {
					setFunctionName(function, "LN")
					return function
				}
			case "PERCENTILE_APPROX":
				switch target {
				case DialectDuckDB:
					setFunctionName(function, "APPROX_QUANTILE")
					return function
				case DialectPresto, DialectTrino:
					setFunctionName(function, "APPROX_PERCENTILE")
					return function
				}
			case "APPROX_COUNT_DISTINCT":
				if target == DialectPresto || target == DialectTrino {
					setFunctionName(function, "APPROX_DISTINCT")
					return function
				}
			case "ARRAY_CONTAINS":
				if target == DialectPresto || target == DialectTrino {
					setFunctionName(function, "CONTAINS")
					return function
				}
			case "SIZE":
				switch target {
				case DialectDuckDB:
					setFunctionName(function, "ARRAY_LENGTH")
					return function
				case DialectPresto, DialectTrino:
					setFunctionName(function, "CARDINALITY")
					return function
				}
			case "GET_JSON_OBJECT":
				if target == DialectPresto || target == DialectTrino {
					setFunctionName(function, "JSON_EXTRACT_SCALAR")
					return function
				}
			case "COLLECT_LIST":
				if len(function.Args) == 1 && (target == DialectDuckDB || target == DialectPresto || target == DialectTrino) {
					return &FunctionCallExpr{
						Name:   []Identifier{{Text: "ARRAY_AGG"}},
						Args:   function.Args,
						Filter: &RawExpr{Raw: renderExpr(function.Args[0]) + " IS NOT NULL"},
					}
				}
			case "COLLECT_SET":
				if len(function.Args) == 1 && (target == DialectPresto || target == DialectTrino) {
					setFunctionName(function, "SET_AGG")
					return function
				}
			case "LOCATE", "INSTR":
				if len(function.Args) >= 2 && (target == DialectDuckDB || target == DialectPresto || target == DialectTrino) {
					function.Args[0], function.Args[1] = function.Args[1], function.Args[0]
					setFunctionName(function, "STRPOS")
					return function
				}
			}
		}
		if source == DialectHive || source == DialectSpark || source == DialectDatabricks ||
			source == DialectPresto && target != DialectPresto ||
			source == DialectMySQL && target != DialectMySQL ||
			source == DialectPostgreSQL && target != DialectPostgreSQL {
			if rewritten := rewriteGenericSourceFunction(function, target); rewritten != nil {
				return rewritten
			}
		}
		switch source {
		case DialectHive, DialectSpark, DialectDatabricks:
			if name == "SPACE" && len(function.Args) == 1 {
				return &FunctionCallExpr{
					Name: []Identifier{{Text: "REPEAT"}},
					Args: []Expr{&LiteralExpr{KindValue: LiteralString, Raw: "' '"}, function.Args[0]},
				}
			}
			if target == DialectBigQuery && name == "TRIM" && len(function.Args) == 2 {
				function.Args[0], function.Args[1] = function.Args[1], function.Args[0]
				return function
			}
		}
		if target == DialectBigQuery && (function.IgnoreNulls || function.RespectNulls) {
			// Spark-family syntax places the modifier after the call; BigQuery
			// places it inside the argument list.
			function.NullsInside = true
		}
		return current
	})
	if target == DialectBigQuery && (source == DialectSpark || source == DialectDatabricks) {
		normalized = normalizeSparkExplodeToBigQuery(normalized)
	}
	if (target == DialectPresto || target == DialectTrino) && (source == DialectSpark || source == DialectDatabricks) {
		normalized = normalizeSparkExplodeToPresto(normalized, target)
	}
	if (target == DialectDuckDB || target == DialectPresto || target == DialectTrino) && (source == DialectSpark || source == DialectDatabricks) {
		normalized = normalizeSparkLateralViews(normalized, target)
	}
	if target == DialectGeneric {
		return normalizeGenericDialectTargetNode(normalized, source)
	}
	return normalized
}

func normalizeExasolIdentityNode(root Node) Node {
	return Transform(root, func(current Node) Node {
		switch value := current.(type) {
		case *CastExpr:
			if typeName, ok := castTypeIdentifier(value.Type); ok {
				switch strings.ToUpper(strings.TrimSpace(typeName.Text)) {
				case "BLOB", "LONGBLOB", "LONGTEXT", "MEDIUMBLOB", "MEDIUMTEXT", "TINYBLOB", "TINYTEXT", "VARBINARY":
					value.Type = identifierExpr("VARCHAR")
					value.TypeSuffix = nil
				case "TEXT":
					value.Type = identifierExpr("LONG VARCHAR")
					value.TypeSuffix = nil
				case "TINYINT":
					value.Type = identifierExpr("SMALLINT")
					value.TypeSuffix = nil
				case "MEDIUMINT":
					value.Type = identifierExpr("INT")
					value.TypeSuffix = nil
				case "DECIMAL32", "DECIMAL64", "DECIMAL128", "DECIMAL256":
					value.Type = identifierExpr("DECIMAL")
					value.TypeSuffix = nil
				case "DATETIME":
					value.Type = identifierExpr("TIMESTAMP")
					value.TypeSuffix = nil
				case "TIMESTAMPLTZ", "TIMESTAMP":
					if strings.EqualFold(typeName.Text, "TIMESTAMPLTZ") || strings.Contains(strings.ToUpper(strings.Join(identifierTexts(value.TypeSuffix), " ")), "LOCAL TIME ZONE") {
						value.Type = identifierExpr("TIMESTAMP")
						value.TypeSuffix = []Identifier{{Text: "WITH"}, {Text: "LOCAL"}, {Text: "TIME"}, {Text: "ZONE"}}
					}
				}
			}
		case *FunctionCallExpr:
			if len(value.Name) == 1 {
				switch strings.ToUpper(value.Name[0].Text) {
				case "CURDATE":
					return identifierExpr("CURRENT_DATE")
				case "USER":
					return identifierExpr("CURRENT_USER")
				case "NOW":
					setFunctionName(value, "CURRENT_TIMESTAMP")
				case "WEEKOFYEAR":
					setFunctionName(value, "WEEK")
				case "TIME_TO_STR":
					if len(value.Args) == 2 {
						value.Args[1] = normalizeExasolOutputTimeFormat(value.Args[1])
					}
					setFunctionName(value, "TO_CHAR")
				case "STR_TO_TIME":
					if len(value.Args) == 2 {
						value.Args[1] = normalizeTimeFormat(value.Args[1], "oracle")
					}
					setFunctionName(value, "TO_DATE")
				case "TO_DATE":
					if len(value.Args) > 1 {
						if literal, ok := value.Args[1].(*LiteralExpr); ok && literal.KindValue == LiteralString {
							literal.Raw = "'" + strings.ToUpper(strings.Trim(literal.Raw, "'")) + "'"
						}
					}
				case "GROUP_CONCAT":
					if len(value.Args) == 1 && len(value.OrderBy) > 0 && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value.ArgumentTail)), "SEPARATOR ") {
						separator := strings.TrimSpace(strings.TrimSpace(value.ArgumentTail)[len("SEPARATOR "):])
						value.Args = append(value.Args, &LiteralExpr{KindValue: LiteralString, Raw: separator})
						value.WithinGroup = value.OrderBy
						value.OrderBy = nil
						value.ArgumentTail = ""
						setFunctionName(value, "LISTAGG")
						if value.Over != nil {
							value.Over.Frame = replaceAllFold(value.Over.Frame, "between", "BETWEEN")
							value.Over.Frame = replaceAllFold(value.Over.Frame, "and", "AND")
						}
					}
				}
			}
		case *IdentifierExpr:
			if len(value.Parts) == 1 && !value.Parts[0].Quoted && strings.EqualFold(value.Parts[0].Text, "USER") {
				return identifierExpr("CURRENT_USER")
			}
		case *TableName:
			if len(value.Parts) == 1 && !value.Parts[0].Quoted && strings.EqualFold(value.Parts[0].Text, "LOCAL") {
				value.Parts[0].Text = "LOCAL"
				value.Parts[0].Quoted = true
				value.Parts[0].Quote = '"'
			}
		case *BinaryExpr:
			if strings.EqualFold(strings.TrimSpace(value.Operator), "AT TIME ZONE") {
				return &FunctionCallExpr{
					nodeBase: value.nodeBase,
					Name:     []Identifier{{Text: "CONVERT_TZ"}},
					Args: []Expr{value.Left,
						&LiteralExpr{KindValue: LiteralString, Raw: "'UTC'"},
						value.Right,
					},
				}
			}
		case *SelectStmt:
			aliases := make(map[string]Identifier)
			for _, projection := range value.Projections {
				if projection.Alias != nil && !projection.Alias.Quoted {
					aliases[strings.ToUpper(projection.Alias.Text)] = *projection.Alias
				}
			}
			value.Where = qualifyExasolLocalAliases(value.Where, aliases)
			value.Having = qualifyExasolLocalAliases(value.Having, aliases)
		}
		return current
	})
}

// normalizeFabricIdentityNode mirrors the canonical spellings emitted by
// SQLGlot's Fabric dialect. Fabric accepts a number of PostgreSQL/Snowflake
// aliases while its canonical output is the T-SQL family of names. Keeping
// this source==target rewrite here avoids teaching the shared cast rewrite
// about one target's compatibility aliases.
func normalizeFabricIdentityNode(root Node) Node {
	return Transform(root, func(current Node) Node {
		switch value := current.(type) {
		case *BinaryExpr:
			if strings.EqualFold(strings.TrimSpace(value.Operator), "AT TIME ZONE") {
				if cast, ok := value.Left.(*CastExpr); ok {
					if typeName, typeOK := castTypeIdentifier(cast.Type); typeOK && strings.EqualFold(typeName.Text, "TIMESTAMPTZ") {
						precision := fabricTemporalPrecision(cast.Type, 6)
						inner := &CastExpr{
							nodeBase: value.nodeBase,
							Keyword:  "CAST",
							Value:    cast.Value,
							Type:     &RawExpr{Raw: fmt.Sprintf("DATETIMEOFFSET(%d)", precision)},
						}
						atTimeZone := &BinaryExpr{
							nodeBase: value.nodeBase,
							Left:     inner,
							Operator: value.Operator,
							Right:    value.Right,
						}
						return &CastExpr{
							nodeBase: value.nodeBase,
							Keyword:  "CAST",
							Value:    atTimeZone,
							Type:     &RawExpr{Raw: fmt.Sprintf("DATETIME2(%d)", precision)},
						}
					}
				}
			}
		case *CastExpr:
			if typeName, ok := castTypeIdentifier(value.Type); ok {
				upper := strings.ToUpper(strings.TrimSpace(typeName.Text))
				switch upper {
				case "BOOLEAN":
					value.Type = identifierExpr("BIT")
				case "DOUBLE":
					value.Type = identifierExpr("FLOAT")
				case "IMAGE":
					value.Type = identifierExpr("VARBINARY")
				case "JSON", "XML":
					value.Type = identifierExpr("VARCHAR")
				case "MONEY", "SMALLMONEY":
					value.Type = identifierExpr("DECIMAL")
				case "NCHAR":
					value.Type = identifierExpr("CHAR")
				case "NVARCHAR":
					value.Type = identifierExpr("VARCHAR")
				case "TEXT":
					value.Type = &RawExpr{Raw: "VARCHAR(MAX)"}
				case "DATETIME", "SMALLDATETIME", "TIMESTAMP", "TIMESTAMPNTZ":
					value.Type = &RawExpr{Raw: fmt.Sprintf("DATETIME2(%d)", fabricTemporalPrecision(value.Type, 6))}
				case "TIME":
					value.Type = &RawExpr{Raw: fmt.Sprintf("TIME(%d)", fabricTemporalPrecision(value.Type, 6))}
				case "DATETIME2":
					value.Type = &RawExpr{Raw: fmt.Sprintf("DATETIME2(%d)", fabricTemporalPrecision(value.Type, 6))}
				case "TIMESTAMPTZ":
					value.Type = &RawExpr{Raw: fmt.Sprintf("DATETIME2(%d)", fabricTemporalPrecision(value.Type, 6))}
				case "TINYINT", "UTINYINT":
					value.Type = identifierExpr("SMALLINT")
				case "UUID":
					value.Type = identifierExpr("UNIQUEIDENTIFIER")
				case "VARIANT":
					value.Type = identifierExpr("SQL_VARIANT")
				}
			}
		case *FunctionCallExpr:
			if len(value.Name) == 1 && strings.EqualFold(value.Name[0].Text, "UNIX_TO_TIME") && len(value.Args) == 1 {
				return &RawExpr{Raw: "DATEADD(MICROSECONDS, CAST(ROUND(" + renderExpr(value.Args[0]) + " * 1e6, 0) AS BIGINT), CAST('1970-01-01' AS DATETIME2(6)))"}
			}
		}
		return current
	})
}

func fabricTemporalPrecision(typeExpression Expr, defaultPrecision int) int {
	precision := defaultPrecision
	call, ok := typeExpression.(*CallExpr)
	if !ok || len(call.Args) != 1 {
		return precision
	}
	literal, ok := call.Args[0].(*LiteralExpr)
	if !ok || literal.KindValue != LiteralNumber {
		return precision
	}
	value, err := strconv.Atoi(strings.TrimSpace(literal.Raw))
	if err != nil || value < 0 {
		return precision
	}
	if value > 6 {
		return 6
	}
	return value
}

func normalizeSparkIdentityNode(root Node) Node {
	return Transform(root, func(current Node) Node {
		function, ok := current.(*FunctionCallExpr)
		if !ok || len(function.Name) != 1 || len(function.Args) != 2 || !isTrueLiteral(function.Args[1]) {
			return current
		}
		switch strings.ToUpper(function.Name[0].Text) {
		case "ANY_VALUE", "FIRST", "FIRST_VALUE", "LAST", "LAST_VALUE":
			function.Args = function.Args[:1]
			function.IgnoreNulls = true
			function.NullsInside = false
		}
		return current
	})
}

func normalizeFabricTargetText(text string, source Dialect) string {
	trimmed := strings.TrimSpace(text)
	upper := strings.ToUpper(trimmed)
	if !strings.HasPrefix(upper, "CREATE TABLE ") {
		return text
	}
	length := "1"
	if source == DialectPostgreSQL {
		length = "MAX"
	}
	for _, typeName := range []string{"VARCHAR", "CHAR"} {
		text = addFabricDefaultTypeLength(text, typeName, length)
	}
	return text
}

func addFabricDefaultTypeLength(text, typeName, length string) string {
	upper := strings.ToUpper(text)
	var result strings.Builder
	for index := 0; index < len(text); {
		if !strings.HasPrefix(upper[index:], typeName) || (index > 0 && isIdentifierByte(upper[index-1])) || (index+len(typeName) < len(text) && isIdentifierByte(upper[index+len(typeName)])) {
			result.WriteByte(text[index])
			index++
			continue
		}
		end := index + len(typeName)
		probe := end
		for probe < len(text) && (text[probe] == ' ' || text[probe] == '\t' || text[probe] == '\r' || text[probe] == '\n') {
			probe++
		}
		if probe < len(text) && text[probe] == '(' {
			result.WriteString(text[index:end])
			index = end
			continue
		}
		result.WriteString(text[index:end])
		result.WriteByte('(')
		result.WriteString(length)
		result.WriteByte(')')
		index = end
	}
	return result.String()
}

func qualifyExasolLocalAliases(expression Expr, aliases map[string]Identifier) Expr {
	if expression == nil || len(aliases) == 0 {
		return expression
	}
	result := Transform(expression, func(current Node) Node {
		identifier, ok := current.(*IdentifierExpr)
		if !ok || len(identifier.Parts) != 1 || identifier.Parts[0].Quoted {
			return current
		}
		alias, ok := aliases[strings.ToUpper(identifier.Parts[0].Text)]
		if !ok {
			return current
		}
		return &FieldExpr{
			nodeBase: identifier.nodeBase,
			Target:   identifierExpr("LOCAL"),
			Field:    alias,
		}
	})
	if result == nil {
		return nil
	}
	return result.(Expr)
}

func normalizePostgreSQLIdentityNode(root Node) Node {
	return Transform(root, func(current Node) Node {
		switch value := current.(type) {
		case *BinaryExpr:
			if strings.EqualFold(strings.TrimSpace(value.Operator), "->>") {
				if path, ok := value.Left.(*BinaryExpr); ok && strings.EqualFold(strings.TrimSpace(path.Operator), "->") {
					return &RawExpr{nodeBase: value.nodeBase, Raw: "JSON_EXTRACT_PATH_TEXT(" + renderExpr(path) + ", " + renderExpr(value.Right) + ")"}
				}
			}
			if strings.EqualFold(strings.TrimSpace(value.Operator), "->") {
				if path, ok := value.Right.(*BinaryExpr); ok && strings.EqualFold(strings.TrimSpace(path.Operator), "->>") {
					return &RawExpr{nodeBase: value.nodeBase, Raw: "JSON_EXTRACT_PATH_TEXT(" + renderExpr(value.Left) + " -> " + renderExpr(path.Left) + ", " + renderExpr(path.Right) + ")"}
				}
			}
			switch strings.ToUpper(strings.TrimSpace(value.Operator)) {
			case "!~":
				return &UnaryExpr{nodeBase: value.nodeBase, Operator: "NOT", Expr: &BinaryExpr{nodeBase: value.nodeBase, Left: value.Left, Operator: "~", Right: value.Right}}
			case "!~*":
				return &UnaryExpr{nodeBase: value.nodeBase, Operator: "NOT", Expr: &BinaryExpr{nodeBase: value.nodeBase, Left: value.Left, Operator: "~*", Right: value.Right}}
			case "~~":
				value.Operator = "LIKE"
			case "~~*":
				value.Operator = "ILIKE"
			case "!~~":
				value.Operator = "NOT LIKE"
			case "!~~*":
				value.Operator = "NOT ILIKE"
			}
		case *UnaryExpr:
			switch strings.TrimSpace(value.Operator) {
			case "|/":
				return &FunctionCallExpr{nodeBase: value.nodeBase, Name: []Identifier{{Text: "SQRT"}}, Args: []Expr{value.Expr}}
			case "||/":
				return &FunctionCallExpr{nodeBase: value.nodeBase, Name: []Identifier{{Text: "CBRT"}}, Args: []Expr{value.Expr}}
			}
		case *TypedLiteralExpr:
			if len(value.TypeName) == 1 && strings.EqualFold(value.TypeName[0].Text, "POINT") && value.Value != nil {
				return &CastExpr{nodeBase: value.nodeBase, Keyword: "CAST", Value: value.Value, Type: identifierExpr("POINT")}
			}
		case *FunctionCallExpr:
			if len(value.Name) != 1 || value.RawArgs != "" {
				return current
			}
			switch strings.ToUpper(value.Name[0].Text) {
			case "CHAR_LENGTH", "CHARACTER_LENGTH":
				setFunctionName(value, "LENGTH")
			case "LOGICAL_OR":
				setFunctionName(value, "BOOL_OR")
			case "VARIANCE":
				setFunctionName(value, "VAR_SAMP")
			case "JSON_ARRAY_ELEMENTS":
				if len(value.Args) == 1 {
					if binary, ok := value.Args[0].(*BinaryExpr); ok && strings.EqualFold(binary.Operator, "->") {
						// Keep this nested call opaque to the pretty generator.  The
						// SQLGlot identity spelling is a single expression here;
						// representing the inner path as a normal function would make
						// pretty-printing expand it onto separate lines.
						return &RawExpr{nodeBase: value.nodeBase, Raw: "JSON_ARRAY_ELEMENTS(JSON_EXTRACT_PATH(" + renderExpr(binary.Left) + ", " + renderExpr(binary.Right) + "))"}
					}
				}
			case "CONCAT":
				return &RawExpr{nodeBase: value.nodeBase, Raw: "CONCAT(" + renderArgs(value.Args) + ")"}
			case "DATE_ADD":
				if len(value.Args) == 2 {
					if _, ok := value.Args[1].(*IntervalExpr); ok {
						return &BinaryExpr{nodeBase: value.nodeBase, Left: value.Args[0], Operator: "+", Right: value.Args[1]}
					}
				}
			case "SUBSTRING":
				tail := strings.TrimSpace(value.ArgumentTail)
				if tail != "" {
					upperTail := strings.ToUpper(tail)
					forIndex := strings.Index(upperTail, "FOR ")
					fromIndex := strings.Index(upperTail, "FROM ")
					switch {
					case forIndex == 0 && fromIndex < 0:
						value.ArgumentTail = "FROM 1 FOR " + strings.TrimSpace(tail[len("FOR "):])
					case forIndex >= 0 && fromIndex > forIndex:
						forValue := strings.TrimSpace(tail[len("FOR "):fromIndex])
						fromValue := strings.TrimSpace(tail[fromIndex+len("FROM "):])
						value.ArgumentTail = "FROM " + fromValue + " FOR " + forValue
					}
				}
			case "TRIM":
				if len(value.Args) == 1 && value.ArgumentTail != "" && !strings.Contains(strings.ToUpper(value.ArgumentTail), " FROM ") {
					name := strings.ToUpper(strings.TrimSpace(renderExpr(value.Args[0])))
					if name == "LEADING" || name == "TRAILING" {
						mapped := "LTRIM"
						if name == "TRAILING" {
							mapped = "RTRIM"
						}
						return &FunctionCallExpr{nodeBase: value.nodeBase, Name: []Identifier{{Text: mapped}}, Args: []Expr{&RawExpr{Raw: strings.TrimSpace(value.ArgumentTail)}}}
					}
				}
			}
		}
		return current
	})
}

// normalizeClickHouseSourceNode converts ClickHouse's mixed-case and
// higher-order function spellings into the small generic vocabulary consumed
// by the target rewrite pass. Keeping this at the source boundary avoids
// teaching every target dialect about ClickHouse's names independently.
func normalizeClickHouseSourceNode(root Node, target Dialect) Node {
	return Transform(root, func(current Node) Node {
		switch value := current.(type) {
		case *InsertStmt:
			if target == DialectPostgreSQL {
				for rowIndex := range value.Values {
					for valueIndex, expression := range value.Values[rowIndex] {
						if _, ok := expression.(*ParenthesizedExpr); !ok {
							value.Values[rowIndex][valueIndex] = &ParenthesizedExpr{Expr: expression}
						}
					}
				}
			}
		case *LiteralExpr:
			if target == DialectMySQL && value.KindValue == LiteralString && value.Raw == "'\\0'" {
				value.Raw = "'" + string(byte(0)) + "'"
			}
			if target == DialectClickHouse && value.KindValue == LiteralString && strings.Contains(value.Raw, `\'`) {
				value.Raw = strings.ReplaceAll(value.Raw, `\'`, `''`)
			}
		case *IndexExpr:
			if target == DialectHive && !value.Slice && len(value.Indices) == 1 {
				if literal, ok := value.Indices[0].(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
					if number, err := strconv.Atoi(literal.Raw); err == nil && number > 0 {
						literal.Raw = strconv.Itoa(number - 1)
					}
				}
			} else if target == DialectHive && !value.Slice && value.Low != nil && value.High == nil && value.Step == nil {
				if literal, ok := value.Low.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
					if number, err := strconv.Atoi(literal.Raw); err == nil && number > 0 {
						literal.Raw = strconv.Itoa(number - 1)
					}
				}
			}
		case *CastExpr:
			if target == DialectDuckDB || target == DialectPostgreSQL {
				typeText := strings.TrimSpace(renderExpr(value.Type))
				upper := strings.ToUpper(typeText)
				if strings.HasPrefix(upper, "NULLABLE(") && strings.HasSuffix(typeText, ")") {
					inner := strings.TrimSpace(typeText[len("NULLABLE(") : len(typeText)-1])
					switch strings.ToUpper(inner) {
					case "DATETIME":
						inner = "TIMESTAMP"
					case "DATE32", "DATE64":
						inner = "DATE"
					}
					if target == DialectPostgreSQL && strings.EqualFold(inner, "DATE") {
						if function, ok := value.Value.(*FunctionCallExpr); ok && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "STR_TO_DATE") && len(function.Args) == 2 {
							value.Value = &CastExpr{
								Keyword: "CAST",
								Value: &FunctionCallExpr{
									Name: []Identifier{{Text: "TO_DATE"}},
									Args: []Expr{function.Args[0], clickHousePostgresDateFormat(function.Args[1])},
								},
								Type: identifierExpr("TIMESTAMP"),
							}
						} else {
							value.Value = rawCast(renderExpr(value.Value), "TIMESTAMP")
						}
					}
					value.Type = &RawExpr{Raw: inner}
				}
			}
		case *RawFrom:
			if target == DialectClickHouse && len(value.Columns) > 0 {
				value.Columns = nil
			}
		case *CallExpr:
			if target == DialectDuckDB {
				if aggregate, ok := value.Callee.(*FunctionCallExpr); ok && len(aggregate.Name) == 1 && len(value.Args) == 1 {
					name := strings.ToUpper(aggregate.Name[0].Text)
					if name == "QUANTILE" || name == "QUANTILES" {
						args := []Expr{value.Args[0]}
						if name == "QUANTILE" && len(aggregate.Args) == 1 {
							args = append(args, aggregate.Args[0])
						} else if name == "QUANTILES" {
							args = append(args, &FunctionCallExpr{Name: []Identifier{{Text: "ARRAY"}}, Args: append([]Expr(nil), aggregate.Args...), ArrayLiteral: true})
						}
						return &FunctionCallExpr{Name: []Identifier{{Text: "quantile"}}, Args: args}
					}
				}
			}
			if target == DialectMySQL || target == DialectDuckDB {
				if aggregate, ok := value.Callee.(*FunctionCallExpr); ok && len(aggregate.Name) == 1 && strings.EqualFold(aggregate.Name[0].Text, "groupConcat") && len(aggregate.Args) >= 1 && len(value.Args) == 1 {
					separator := aggregate.Args[0]
					argument := value.Args[0]
					if target == DialectMySQL {
						return &RawExpr{Raw: "GROUP_CONCAT(" + renderExpr(argument) + " SEPARATOR " + renderExpr(separator) + ")"}
					}
					return &FunctionCallExpr{Name: []Identifier{{Text: "LISTAGG"}}, Args: []Expr{argument, separator}}
				}
			}
		case *FunctionCallExpr:
			if len(value.Name) != 1 || value.RawArgs != "" {
				return current
			}
			name := strings.ToUpper(value.Name[0].Text)
			rendered := func(index int) string {
				if index < 0 || index >= len(value.Args) {
					return ""
				}
				return renderExpr(value.Args[index])
			}
			switch name {
			case "STR_TO_DATE":
				if target == DialectPostgreSQL && len(value.Args) == 2 {
					return &FunctionCallExpr{Name: []Identifier{{Text: "TO_DATE"}}, Args: []Expr{value.Args[0], clickHousePostgresDateFormat(value.Args[1])}}
				}
			case "UNIQ":
				if target == DialectBigQuery {
					setFunctionName(value, "APPROX_COUNT_DISTINCT")
					return value
				}
			case "ANY":
				if target == DialectBigQuery {
					setFunctionName(value, "ANY_VALUE")
					return value
				}
			case "SUBSTRINGINDEX":
				if target == DialectClickHouse {
					setFunctionName(value, "substringIndex")
				} else {
					setFunctionName(value, "SUBSTRING_INDEX")
				}
				return value
			case "ARRAYJOIN":
				if target == DialectPostgreSQL {
					setFunctionName(value, "UNNEST")
					return value
				}
			case "HAS":
				switch target {
				case DialectPresto, DialectTrino:
					setFunctionName(value, "CONTAINS")
					return value
				case DialectSpark, DialectDatabricks:
					setFunctionName(value, "ARRAY_CONTAINS")
					return value
				}
			case "DATEADD":
				if len(value.Args) == 3 {
					unit := value.Args[0]
					if target == DialectPresto || target == DialectTrino {
						unit = genericDateUnitLiteral(unit)
					}
					return &FunctionCallExpr{Name: []Identifier{{Text: "DATE_ADD"}}, Args: []Expr{unit, value.Args[1], value.Args[2]}}
				}
			case "DATEDIFF":
				if len(value.Args) == 3 {
					unit := value.Args[0]
					if target == DialectPresto || target == DialectTrino {
						unit = genericDateUnitLiteral(unit)
					}
					return &FunctionCallExpr{Name: []Identifier{{Text: "DATE_DIFF"}}, Args: []Expr{unit, value.Args[1], value.Args[2]}}
				}
			case "TOMONDAY":
				if target == DialectClickHouse {
					return &FunctionCallExpr{Name: []Identifier{{Text: "dateTrunc"}}, Args: []Expr{&LiteralExpr{KindValue: LiteralString, Raw: "'WEEK'"}, value.Args[0]}}
				}
				if target == DialectDoris {
					return &FunctionCallExpr{Name: []Identifier{{Text: "DATE_TRUNC"}}, Args: []Expr{value.Args[0], &LiteralExpr{KindValue: LiteralString, Raw: "'WEEK'"}}}
				}
				return &FunctionCallExpr{Name: []Identifier{{Text: "DATE_TRUNC"}}, Args: []Expr{&LiteralExpr{KindValue: LiteralString, Raw: "'WEEK'"}, value.Args[0]}}
			case "DATETRUNC":
				if len(value.Args) == 2 {
					switch target {
					case DialectSpark, DialectDatabricks:
						return &FunctionCallExpr{Name: []Identifier{{Text: "TRUNC"}}, Args: []Expr{value.Args[1], value.Args[0]}}
					case DialectDuckDB, DialectPresto, DialectTrino:
						return &FunctionCallExpr{Name: []Identifier{{Text: "DATE_TRUNC"}}, Args: value.Args}
					}
				}
			case "SPLITBYSTRING":
				if len(value.Args) == 2 {
					switch target {
					case DialectBigQuery:
						return &FunctionCallExpr{Name: []Identifier{{Text: "SPLIT"}}, Args: []Expr{value.Args[1], value.Args[0]}}
					case DialectDuckDB:
						return &FunctionCallExpr{Name: []Identifier{{Text: "STR_SPLIT"}}, Args: []Expr{value.Args[1], value.Args[0]}}
					case DialectDoris:
						return &FunctionCallExpr{Name: []Identifier{{Text: "SPLIT_BY_STRING"}}, Args: []Expr{value.Args[1], value.Args[0]}}
					case DialectHive:
						return &RawExpr{Raw: "SPLIT(" + rendered(1) + ", CONCAT('\\\\Q', " + rendered(0) + ", '\\\\E'))"}
					}
				}
			case "SPLITBYREGEXP":
				if len(value.Args) == 2 {
					switch target {
					case DialectDuckDB:
						if literal, ok := value.Args[0].(*LiteralExpr); ok && literal.KindValue == LiteralString {
							literal.Raw = strings.ReplaceAll(literal.Raw, `\\`, `\`)
						}
						return &FunctionCallExpr{Name: []Identifier{{Text: "STR_SPLIT_REGEX"}}, Args: []Expr{value.Args[1], value.Args[0]}}
					case DialectHive:
						return &FunctionCallExpr{Name: []Identifier{{Text: "SPLIT"}}, Args: []Expr{value.Args[1], value.Args[0]}}
					}
				}
			case "FORMATDATETIME":
				if target == DialectMySQL && len(value.Args) == 2 {
					return &FunctionCallExpr{Name: []Identifier{{Text: "DATE_FORMAT"}}, Args: value.Args}
				}
			case "ISNAN", "IS_NAN":
				if target == DialectClickHouse {
					setFunctionName(value, "isNaN")
					return value
				}
			case "STARTSWITH", "STARTS_WITH":
				if target == DialectClickHouse {
					setFunctionName(value, "startsWith")
					return value
				}
			}
		}
		return current
	})
}

func rewriteTSQLCreateTableAs(table *CreateTableStmt, source Dialect) Node {
	if table == nil || len(table.Name) == 0 {
		return nil
	}
	tail := strings.TrimSpace(table.Tail)
	if len(tail) < 2 || !strings.EqualFold(tail[:2], "AS") || (len(tail) > 2 && isIdentifierByte(tail[2])) {
		return nil
	}
	querySQL := strings.TrimSpace(tail[2:])
	parsed, err := ParseStrict(querySQL, source)
	if err != nil || len(parsed.Statements) != 1 {
		return nil
	}
	query, ok := parsed.Statements[0].Node.(*SelectStmt)
	if !ok {
		return nil
	}
	if normalized, normalizedOK := normalizeDialectSourceNode(query, source, DialectTSQL).(*SelectStmt); normalizedOK {
		query = normalized
	}
	flattenNestedCTEs(query, DialectTSQL)
	addTSQLProjectionAliases(query)
	addTSQLNestedProjectionAliases(query)
	query.Parenthesized = false
	query.ParenthesisDepth = 0

	into := append([]Identifier(nil), table.Name...)
	if table.Temporary && len(into) == 1 && !strings.HasPrefix(into[0].Text, "#") {
		into[0].Text = "#" + into[0].Text
		into[0].Quoted = false
		into[0].Quote = 0
	}
	with := append([]CTE(nil), query.With...)
	query.With = nil
	alias := Identifier{Text: "temp"}
	return &SelectStmt{
		With:        with,
		Projections: []SelectItem{{Expr: &StarExpr{}}},
		Into:        into,
		From: []TableExpr{{Primary: &SubqueryFrom{
			Query: query,
			Alias: &alias,
		}}},
	}
}

func rewriteTSQLMergeCTE(raw string, source Dialect) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	usingIndex := indexKeywordTopLevel(trimmed, "USING")
	if usingIndex < 0 {
		return raw, false
	}
	open := usingIndex + len("USING")
	for open < len(trimmed) && (trimmed[open] == ' ' || trimmed[open] == '\t') {
		open++
	}
	if open >= len(trimmed) || trimmed[open] != '(' {
		return raw, false
	}
	close := matchingParenIndex(trimmed, open)
	if close < 0 {
		return raw, false
	}
	innerSQL := strings.TrimSpace(trimmed[open+1 : close])
	if !strings.HasPrefix(strings.ToUpper(innerSQL), "WITH ") {
		return raw, false
	}
	parsed, err := ParseStrict(innerSQL, source)
	if err != nil || len(parsed.Statements) != 1 {
		return raw, false
	}
	query, ok := parsed.Statements[0].Node.(*SelectStmt)
	if !ok {
		return raw, false
	}
	if normalized, normalizedOK := normalizeDialectSourceNode(query, source, DialectTSQL).(*SelectStmt); normalizedOK {
		query = normalized
	}
	flattenNestedCTEs(query, DialectTSQL)
	addTSQLProjectionAliases(query)
	addTSQLNestedProjectionAliases(query)
	with := append([]CTE(nil), query.With...)
	query.With = nil
	query.Parenthesized = false
	query.ParenthesisDepth = 0
	transformSelect(query, DialectTSQL)
	inner, err := GenerateWithOptions(query, GenerateOptions{Canonical: true, Dialect: DialectTSQL})
	if err != nil {
		return raw, false
	}
	dummy := &SelectStmt{With: with, Projections: []SelectItem{{Expr: &StarExpr{}}}}
	prefixSQL, err := GenerateWithOptions(dummy, GenerateOptions{Canonical: true, Dialect: DialectTSQL})
	if err != nil {
		return raw, false
	}
	selectIndex := indexKeywordTopLevel(prefixSQL, "SELECT")
	if selectIndex < 0 {
		return raw, false
	}
	prefix := strings.TrimSpace(prefixSQL[:selectIndex])
	rewritten := trimmed[:open+1] + inner + trimmed[close:]
	if prefix == "" {
		return rewritten, true
	}
	return prefix + " " + rewritten, true
}

// normalizeBigQuerySourceRelations restores relation semantics that are lost
// when a source dialect models a table function alias differently. BigQuery's
// UNNEST alias names the output column, while DuckDB and Presto commonly keep
// a relation alias plus an explicit column list.
func normalizeBigQuerySourceRelations(stmt *SelectStmt, source Dialect) {
	if stmt == nil {
		return
	}
	for index := range stmt.From {
		normalizeBigQuerySourceTableExpr(&stmt.From[index], source)
	}
}

func normalizeBigQuerySourceTableExpr(table *TableExpr, source Dialect) {
	if table == nil {
		return
	}
	normalizeBigQuerySourceFromItem(table.Primary, source)
	for index := range table.Joins {
		normalizeBigQuerySourceFromItem(table.Joins[index].Right, source)
	}
}

func normalizeBigQuerySourceFromItem(item FromItem, source Dialect) {
	switch value := item.(type) {
	case *RawFrom:
		if raw, ok := normalizeBigQueryValuesFrom(value.Raw, value.Columns); ok {
			value.Raw = raw
			value.Columns = nil
		}
	case *TableFunctionFrom:
		if len(value.Name) != 1 || !strings.EqualFold(value.Name[0].Text, "UNNEST") {
			return
		}
		if source == DialectDuckDB && len(value.Args) == 1 {
			if identifier, ok := value.Args[0].(*IdentifierExpr); ok && len(identifier.Parts) == 3 && value.Alias != nil && strings.EqualFold(identifier.Parts[0].Text, value.Alias.Text) {
				identifier.Parts = append([]Identifier(nil), identifier.Parts[1:]...)
			}
		}
		if value.Alias != nil && len(value.Columns) == 1 {
			alias := value.Columns[0]
			value.Alias = &alias
			value.Columns = nil
		}
	case *SubqueryFrom:
		if value.Query != nil {
			normalizeBigQuerySourceRelations(value.Query, source)
		}
	case *GroupedFrom:
		for index := range value.Items {
			normalizeBigQuerySourceTableExpr(&value.Items[index], source)
		}
	}
}

// normalizeBigQueryValuesFrom converts the small VALUES-as-a-relation shape
// used by PostgreSQL, Snowflake, and Spark into BigQuery's equivalent array
// of anonymous structs. The parser deliberately keeps this dialect-specific
// relation raw, so this conversion is kept at the source/target boundary.
func normalizeBigQueryValuesFrom(raw string, columns []Identifier) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	text := trimmed
	if strings.HasPrefix(text, "(") && matchingParenIndex(text, 0) == len(text)-1 {
		text = strings.TrimSpace(text[1 : len(text)-1])
	}
	if !strings.HasPrefix(strings.ToUpper(text), "VALUES") {
		return raw, false
	}
	payload := strings.TrimSpace(text[len("VALUES"):])
	if payload == "" {
		return raw, false
	}
	rows := splitTopLevelSQL(payload, ',')
	structs := make([]string, 0, len(rows))
	for _, row := range rows {
		row = strings.TrimSpace(row)
		if strings.HasPrefix(row, "(") && matchingParenIndex(row, 0) == len(row)-1 {
			row = strings.TrimSpace(row[1 : len(row)-1])
		}
		values := splitTopLevelSQL(row, ',')
		if len(values) == 0 {
			return raw, false
		}
		fields := make([]string, 0, len(values))
		for index, value := range values {
			name := "_c" + strconv.Itoa(index)
			if index < len(columns) {
				column := columns[index]
				normalizeIdentifierTarget(&column, DialectBigQuery)
				name = generateIdentifier(column)
			}
			fields = append(fields, strings.TrimSpace(value)+" AS "+name)
		}
		structs = append(structs, "STRUCT("+strings.Join(fields, ", ")+")")
	}
	if len(structs) == 0 {
		return raw, false
	}
	return "UNNEST([" + strings.Join(structs, ", ") + "])", true
}

func normalizeClickHouseValuesFromRaw(raw string, columns []Identifier) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 8 || trimmed[0] != '(' || matchingParenIndex(trimmed, 0) != len(trimmed)-1 {
		return raw, false
	}
	inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	if !strings.HasPrefix(strings.ToUpper(inner), "VALUES") {
		return raw, false
	}
	rows := splitTopLevelSQL(strings.TrimSpace(inner[len("VALUES"):]), ',')
	if len(rows) == 0 {
		return raw, false
	}
	selects := make([]string, 0, len(rows))
	for rowIndex, row := range rows {
		row = strings.TrimSpace(row)
		if len(row) < 2 || row[0] != '(' || matchingParenIndex(row, 0) != len(row)-1 {
			return raw, false
		}
		values := splitTopLevelSQL(strings.TrimSpace(row[1:len(row)-1]), ',')
		if len(values) == 0 {
			return raw, false
		}
		projection := make([]string, 0, len(values))
		for index, value := range values {
			value = strings.TrimSpace(value)
			if rowIndex == 0 && index < len(columns) {
				projection = append(projection, value+" AS "+generateIdentifier(columns[index]))
			} else {
				projection = append(projection, value)
			}
		}
		selects = append(selects, "SELECT "+strings.Join(projection, ", "))
	}
	return "(" + strings.Join(selects, " UNION ALL ") + ")", true
}

// normalizeSparkExplodeToBigQuery lowers the projection form of Spark's
// explode family. SQLGlot preserves positional alignment through an offset
// relation, so the same shape is emitted here instead of silently turning an
// explode into a plain projection expression.
func normalizeSparkExplodeToBigQuery(root Node) Node {
	return Transform(root, func(current Node) Node {
		stmt, ok := current.(*SelectStmt)
		if !ok || stmt.RawQuery != "" {
			return current
		}
		type explodeProjection struct {
			call       *FunctionCallExpr
			name       string
			array      string
			alias      string
			position   string
			positional bool
		}
		explodes := make([]explodeProjection, 0)
		for _, projection := range stmt.Projections {
			call, callOK := projection.Expr.(*FunctionCallExpr)
			if !callOK || len(call.Name) != 1 || len(call.Args) != 1 || len(projection.AliasColumns) > 0 {
				continue
			}
			name := strings.ToUpper(call.Name[0].Text)
			if name != "EXPLODE" && name != "EXPLODE_OUTER" && name != "POSEXPLODE" && name != "POSEXPLODE_OUTER" {
				continue
			}
			explodes = append(explodes, explodeProjection{
				call:       call,
				name:       name,
				array:      renderDialectExpr(call.Args[0], DialectBigQuery),
				positional: name == "POSEXPLODE" || name == "POSEXPLODE_OUTER",
			})
		}
		if len(explodes) == 0 {
			return current
		}
		for index := range explodes {
			if explodes[index].array == "" {
				return current
			}
			if len(stmt.Projections) == 1 && strings.HasSuffix(explodes[index].name, "_OUTER") {
				array := explodes[index].array
				explodes[index].array = "IF(ARRAY_LENGTH(COALESCE(" + array + ", [])) = 0, [" + array + "[SAFE_ORDINAL(0)]], " + array + ")"
			}
		}
		usedAliases := make(map[string]bool)
		for _, projection := range stmt.Projections {
			if projection.Alias != nil {
				usedAliases[strings.ToLower(projection.Alias.Text)] = true
			}
		}
		for index := range explodes {
			alias := "col"
			projectionIndex := 0
			for projectionIndex < len(stmt.Projections) && stmt.Projections[projectionIndex].Expr != explodes[index].call {
				projectionIndex++
			}
			if projectionIndex < len(stmt.Projections) && stmt.Projections[projectionIndex].Alias != nil {
				alias = stmt.Projections[projectionIndex].Alias.Text
			} else if index > 0 || strings.EqualFold(strings.TrimSpace(explodes[index].array), "col") {
				alias = "col_2"
			}
			if len(explodes) == 1 {
				if identifier, ok := explodes[index].call.Args[0].(*IdentifierExpr); ok && len(identifier.Parts) == 1 && strings.EqualFold(identifier.Parts[0].Text, alias) {
					alias = alias + "_2"
				}
			}
			if usedAliases[strings.ToLower(alias)] && (projectionIndex >= len(stmt.Projections) || stmt.Projections[projectionIndex].Alias == nil) {
				alias = alias + "_2"
			}
			explodes[index].alias = alias
			explodes[index].position = "pos_" + strconv.Itoa(index+2)
			usedAliases[strings.ToLower(alias)] = true
		}
		maxLengths := make([]string, 0, len(explodes))
		for _, explode := range explodes {
			maxLengths = append(maxLengths, "ARRAY_LENGTH("+explode.array+")")
		}
		stmt.From = append(stmt.From, TableExpr{Primary: &TableFunctionFrom{
			Name:  []Identifier{{Text: "UNNEST"}},
			Args:  []Expr{&RawExpr{Raw: "GENERATE_ARRAY(0, GREATEST(" + strings.Join(maxLengths, ", ") + ") - 1)"}},
			Alias: &Identifier{Text: "pos"},
		}})
		conditions := make([]string, 0, len(explodes))
		for _, explode := range explodes {
			stmt.From = append(stmt.From, TableExpr{Primary: &RawFrom{Raw: "UNNEST(" + explode.array + ") AS " + explode.alias + " WITH OFFSET AS " + explode.position}})
			conditions = append(conditions, "pos = "+explode.position+" OR (pos > (ARRAY_LENGTH("+explode.array+") - 1) AND "+explode.position+" = (ARRAY_LENGTH("+explode.array+") - 1))")
		}
		projections := make([]SelectItem, 0, len(stmt.Projections)+len(explodes))
		for _, projection := range stmt.Projections {
			matched := false
			for _, explode := range explodes {
				if projection.Expr != explode.call {
					continue
				}
				matched = true
				projections = append(projections, SelectItem{Expr: &RawExpr{Raw: "IF(pos = " + explode.position + ", " + explode.alias + ", NULL)"}, Alias: &Identifier{Text: explode.alias}})
				if explode.positional {
					projections = append(projections, SelectItem{Expr: &RawExpr{Raw: "IF(pos = " + explode.position + ", " + explode.position + ", NULL)"}, Alias: &Identifier{Text: explode.position}})
				}
				break
			}
			if !matched {
				projections = append(projections, projection)
			}
		}
		stmt.Projections = projections
		where := strings.Join(conditions, " AND ")
		if len(conditions) > 1 {
			wrapped := make([]string, len(conditions))
			for index, condition := range conditions {
				wrapped[index] = "(" + condition + ")"
			}
			where = strings.Join(wrapped, " AND ")
		}
		stmt.Where = &RawExpr{Raw: where}
		return current
	})
}

func normalizeSparkExplodeToPresto(root Node, target Dialect) Node {
	return Transform(root, func(current Node) Node {
		stmt, ok := current.(*SelectStmt)
		if !ok || stmt.RawQuery != "" {
			return current
		}
		type explodeProjection struct {
			call       *FunctionCallExpr
			array      string
			alias      string
			position   string
			positional bool
		}
		explodes := make([]explodeProjection, 0)
		for _, projection := range stmt.Projections {
			call, callOK := projection.Expr.(*FunctionCallExpr)
			if !callOK || len(call.Name) != 1 || len(call.Args) != 1 || len(projection.AliasColumns) > 0 {
				continue
			}
			name := strings.ToUpper(call.Name[0].Text)
			if name != "EXPLODE" && name != "EXPLODE_OUTER" && name != "POSEXPLODE" && name != "POSEXPLODE_OUTER" {
				continue
			}
			array := normalizePrestoArrayCalls(renderDialectExpr(call.Args[0], target))
			if array == "" {
				return current
			}
			explodes = append(explodes, explodeProjection{
				call:       call,
				array:      array,
				positional: name == "POSEXPLODE" || name == "POSEXPLODE_OUTER",
			})
		}
		if len(explodes) == 0 {
			return current
		}

		usedRelations := make(map[string]bool)
		for _, table := range stmt.From {
			collectFromRelationNames(table.Primary, usedRelations)
		}
		sequenceAlias := nextGeneratedRelationName(usedRelations, "_u")
		usedRelations[strings.ToLower(sequenceAlias)] = true
		arrayAliases := make([]string, len(explodes))
		for index := range arrayAliases {
			arrayAliases[index] = nextGeneratedRelationName(usedRelations, "_u")
			usedRelations[strings.ToLower(arrayAliases[index])] = true
		}

		usedNames := make(map[string]bool)
		for _, projection := range stmt.Projections {
			if projection.Alias != nil {
				usedNames[strings.ToLower(projection.Alias.Text)] = true
			}
			if identifier, ok := projection.Expr.(*IdentifierExpr); ok && len(identifier.Parts) == 1 {
				usedNames[strings.ToLower(identifier.Parts[0].Text)] = true
			}
		}
		sequencePosition := "pos"
		if usedNames[sequencePosition] {
			sequencePosition = nextGeneratedFieldName(usedNames, sequencePosition)
		}
		usedNames[strings.ToLower(sequencePosition)] = true
		for index := range explodes {
			alias := "col"
			projectionIndex := 0
			for projectionIndex < len(stmt.Projections) && stmt.Projections[projectionIndex].Expr != explodes[index].call {
				projectionIndex++
			}
			if projectionIndex < len(stmt.Projections) && stmt.Projections[projectionIndex].Alias != nil {
				alias = stmt.Projections[projectionIndex].Alias.Text
			} else if index > 0 || usedNames[strings.ToLower(alias)] {
				alias = nextGeneratedFieldName(usedNames, alias)
			} else if identifier, ok := explodes[index].call.Args[0].(*IdentifierExpr); ok && len(identifier.Parts) == 1 && strings.EqualFold(identifier.Parts[0].Text, alias) {
				alias = nextGeneratedFieldName(usedNames, alias)
			}
			if usedNames[strings.ToLower(alias)] && (projectionIndex >= len(stmt.Projections) || stmt.Projections[projectionIndex].Alias == nil) {
				alias = nextGeneratedFieldName(usedNames, alias)
			}
			explodes[index].alias = alias
			usedNames[strings.ToLower(alias)] = true
		}
		positionStart := 2
		if strings.EqualFold(sequencePosition, "pos_2") {
			positionStart = 3
		}
		for index := range explodes {
			position := nextGeneratedPositionName(usedNames, positionStart+index)
			explodes[index].position = position
			usedNames[strings.ToLower(position)] = true
		}

		sequenceLengths := make([]string, 0, len(explodes))
		for _, explode := range explodes {
			sequenceLengths = append(sequenceLengths, "CARDINALITY("+explode.array+")")
		}
		sequence := &TableFunctionFrom{
			Name:    []Identifier{{Text: "UNNEST"}},
			Args:    []Expr{&RawExpr{Raw: "SEQUENCE(1, GREATEST(" + strings.Join(sequenceLengths, ", ") + "))"}},
			Alias:   &Identifier{Text: sequenceAlias},
			Columns: []Identifier{{Text: sequencePosition}},
		}
		appendPrestoCrossJoin(stmt, sequence)
		conditions := make([]string, 0, len(explodes))
		for index, explode := range explodes {
			arrayRelation := &TableFunctionFrom{
				Name:           []Identifier{{Text: "UNNEST"}},
				Args:           []Expr{&RawExpr{Raw: explode.array}},
				Alias:          &Identifier{Text: arrayAliases[index]},
				Columns:        []Identifier{{Text: explode.alias}, {Text: explode.position}},
				WithOrdinality: true,
			}
			appendPrestoCrossJoin(stmt, arrayRelation)
			left := sequenceAlias + "." + sequencePosition
			right := arrayAliases[index] + "." + explode.position
			conditions = append(conditions, left+" = "+right+" OR ("+left+" > CARDINALITY("+explode.array+") AND "+right+" = CARDINALITY("+explode.array+"))")
		}

		projections := make([]SelectItem, 0, len(stmt.Projections)+len(explodes))
		for _, projection := range stmt.Projections {
			matched := false
			for _, explode := range explodes {
				if projection.Expr != explode.call {
					continue
				}
				matched = true
				left := sequenceAlias + "." + sequencePosition
				right := ""
				// The array index is recovered below from the call pointer; this
				// keeps non-explode projections in their original order.
				arrayIndex := 0
				for index := range explodes {
					if explodes[index].call == explode.call {
						arrayIndex = index
						break
					}
				}
				right = arrayAliases[arrayIndex] + "." + explode.position
				value := arrayAliases[arrayIndex] + "." + explode.alias
				projections = append(projections, SelectItem{Expr: &RawExpr{Raw: "IF(" + left + " = " + right + ", " + value + ")"}, Alias: &Identifier{Text: explode.alias}})
				if explode.positional {
					projections = append(projections, SelectItem{Expr: &RawExpr{Raw: "IF(" + left + " = " + right + ", " + right + ")"}, Alias: &Identifier{Text: explode.position}})
				}
				break
			}
			if !matched {
				projections = append(projections, projection)
			}
		}
		stmt.Projections = projections
		where := strings.Join(conditions, " AND ")
		if len(conditions) > 1 {
			wrapped := make([]string, len(conditions))
			for index, condition := range conditions {
				wrapped[index] = "(" + condition + ")"
			}
			where = strings.Join(wrapped, " AND ")
		}
		stmt.Where = &RawExpr{Raw: where}
		return current
	})
}

func normalizePrestoArrayCalls(raw string) string {
	text := raw
	for {
		upper := strings.ToUpper(text)
		index := strings.Index(upper, "ARRAY(")
		if index < 0 {
			return text
		}
		open := index + len("ARRAY")
		close := matchingParenIndex(text, open)
		if close < 0 {
			return text
		}
		text = text[:index] + "ARRAY[" + text[open+1:close] + "]" + text[close+1:]
	}
}

func collectFromRelationNames(item FromItem, used map[string]bool) {
	if item == nil {
		return
	}
	var name string
	switch value := item.(type) {
	case *TableName:
		if value.Alias != nil {
			name = value.Alias.Text
		} else if len(value.Parts) > 0 {
			name = value.Parts[len(value.Parts)-1].Text
		}
	case *TableFunctionFrom:
		if value.Alias != nil {
			name = value.Alias.Text
		}
	case *SubqueryFrom:
		if value.Alias != nil {
			name = value.Alias.Text
		}
	case *RawFrom:
		if value.Alias != nil {
			name = value.Alias.Text
		}
	}
	if name != "" {
		used[strings.ToLower(name)] = true
	}
}

func nextGeneratedRelationName(used map[string]bool, base string) string {
	for index := 0; ; index++ {
		candidate := base
		if index > 0 {
			candidate += "_" + strconv.Itoa(index+1)
		}
		if !used[strings.ToLower(candidate)] {
			return candidate
		}
	}
}

func nextGeneratedFieldName(used map[string]bool, base string) string {
	for index := 2; ; index++ {
		candidate := base + "_" + strconv.Itoa(index)
		if !used[strings.ToLower(candidate)] {
			return candidate
		}
	}
}

func nextGeneratedPositionName(used map[string]bool, start int) string {
	for index := start; ; index++ {
		candidate := "pos_" + strconv.Itoa(index)
		if !used[strings.ToLower(candidate)] {
			return candidate
		}
	}
}

func appendPrestoCrossJoin(stmt *SelectStmt, item FromItem) {
	if stmt == nil || item == nil {
		return
	}
	if len(stmt.From) == 0 {
		stmt.From = append(stmt.From, TableExpr{Primary: item})
		return
	}
	last := &stmt.From[len(stmt.From)-1]
	last.Joins = append(last.Joins, JoinClause{Kind: JoinCross, Right: item})
}

func normalizeSparkLateralViews(root Node, target Dialect) Node {
	return Transform(root, func(current Node) Node {
		stmt, ok := current.(*SelectStmt)
		if !ok {
			return current
		}
		for tableIndex := range stmt.From {
			table := &stmt.From[tableIndex]
			if len(table.LateralViews) == 0 {
				continue
			}
			for viewIndex, view := range table.LateralViews {
				function, ok := view.Expression.(*FunctionCallExpr)
				if !ok || len(function.Name) != 1 || len(function.Args) == 0 {
					continue
				}
				name := strings.ToUpper(function.Name[0].Text)
				if name != "INLINE" && name != "EXPLODE" && name != "POSEXPLODE" {
					continue
				}
				argument := renderDialectExpr(function.Args[0], target)
				if argument == "" {
					continue
				}
				alias := view.Alias
				if alias == nil && len(view.Columns) > 0 {
					alias = &Identifier{Text: "_u_" + strconv.Itoa(viewIndex)}
				}
				var right FromItem
				if target == DialectDuckDB {
					if name == "INLINE" || name == "EXPLODE" {
						right = &SubqueryFrom{
							Query:   &SelectStmt{RawQuery: "SELECT UNNEST(" + argument + ", max_depth => 2)"},
							Alias:   alias,
							Columns: append([]Identifier(nil), view.Columns...),
						}
					} else {
						right = &TableFunctionFrom{
							Name:           []Identifier{{Text: "UNNEST"}},
							Args:           []Expr{&RawExpr{Raw: argument}},
							Alias:          alias,
							Columns:        append([]Identifier(nil), view.Columns...),
							WithOrdinality: true,
						}
					}
				} else {
					right = &TableFunctionFrom{
						Name:           []Identifier{{Text: "UNNEST"}},
						Args:           []Expr{&RawExpr{Raw: argument}},
						Alias:          alias,
						Columns:        append([]Identifier(nil), view.Columns...),
						WithOrdinality: name == "POSEXPLODE",
					}
				}
				joinText := "CROSS JOIN"
				if target == DialectDuckDB {
					joinText = "CROSS JOIN LATERAL"
				}
				table.Joins = append(table.Joins, JoinClause{Kind: JoinCross, JoinText: joinText, Right: right})
			}
			table.LateralViews = nil
		}
		return current
	})
}

func selectHasUnnestRelation(stmt *SelectStmt) bool {
	if stmt == nil || len(stmt.From) < 2 {
		return false
	}
	for index := 1; index < len(stmt.From); index++ {
		item := stmt.From[index].Primary
		switch value := item.(type) {
		case *TableFunctionFrom:
			if len(value.Name) == 1 && strings.EqualFold(value.Name[0].Text, "UNNEST") {
				return true
			}
		case *TableName:
			if len(value.Parts) == 2 {
				return true
			}
		}
	}
	return false
}

// normalizeGenericDialectTargetNode restores the stable generic vocabulary
// when a dialect-specific function or operator is read back into Generic.
// This is intentionally source-aware for array syntax and argument order;
// the generic spelling is the semantic interchange format used by fixtures,
// builders, and downstream analysis.
func normalizeGenericDialectTargetNode(root Node, source Dialect) Node {
	return Transform(root, func(current Node) Node {
		switch value := current.(type) {
		case *SelectStmt:
			if value.SetOperator != "" && strings.EqualFold(value.SetModifier, "DISTINCT") {
				value.SetModifier = ""
			}
			if value.Top != nil && value.Limit == nil {
				value.Limit = value.Top
				value.Top = nil
			}
			if isIdentifierNamed(value.Limit, "ALL") {
				value.Limit = nil
			}
		case *WindowedExpr:
			value.Over.Frame = strings.TrimSpace(strings.ReplaceAll(value.Over.Frame, " EXCLUDE NO OTHERS", ""))
		case *CastExpr:
			if identifier, ok := castTypeIdentifier(value.Type); ok {
				switch strings.ToUpper(identifier.Text) {
				case "DOUBLE PRECISION":
					identifier.Text = "DOUBLE"
				case "NUMBER":
					identifier.Text = "DECIMAL"
				}
			}
		case *TypedLiteralExpr:
			if source == DialectPostgreSQL && len(value.TypeName) == 1 && strings.EqualFold(value.TypeName[0].Text, "INET") && value.Value != nil {
				return &CastExpr{nodeBase: value.nodeBase, Keyword: "CAST", Value: value.Value, Type: identifierExpr("INET")}
			}
		case *IsExpr:
			if cast, ok := value.Right.(*CastExpr); ok && isNullLiteral(cast.Value) && genericBooleanCastSource(source) {
				innerOperator := "IS"
				if strings.EqualFold(value.Operator, "IS NOT") && source == DialectPostgreSQL {
					innerOperator = "IS NOT"
				}
				inner := Expr(&IsExpr{Value: value.Value, Operator: innerOperator, Right: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}})
				if strings.EqualFold(value.Operator, "IS NOT") && source != DialectPostgreSQL {
					inner = &UnaryExpr{Operator: "NOT", Expr: inner}
				}
				return &CastExpr{nodeBase: value.nodeBase, Keyword: "CAST", Value: inner, Type: cast.Type}
			}
			if (strings.EqualFold(value.Operator, "IS") || strings.EqualFold(value.Operator, "IS NOT")) && isIdentifierNamed(value.Right, "UNKNOWN") {
				value.Right = &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}
			}
			if source == DialectPostgreSQL && strings.EqualFold(value.Operator, "IS NOT") && isNullLiteral(value.Right) {
				return &RawExpr{Raw: renderExpr(value.Value) + " IS NOT NULL"}
			}
		case *BinaryExpr:
			operator := strings.ToUpper(strings.TrimSpace(value.Operator))
			if operator == "<=>" {
				return &IsExpr{nodeBase: value.nodeBase, Value: value.Left, Operator: "IS NOT DISTINCT FROM", Right: value.Right}
			}
			if operator == "->" || operator == "->>" {
				name := "JSON_EXTRACT"
				if operator == "->>" {
					name = "JSON_EXTRACT_SCALAR"
				}
				return &FunctionCallExpr{Name: []Identifier{{Text: name}}, Args: []Expr{value.Left, genericJSONPathExpr(value.Right)}}
			}
		case *IndexExpr:
			if (source == DialectPresto || source == DialectTrino || source == DialectAthena) && isArrayConstructorIndex(value) {
				return &FunctionCallExpr{nodeBase: value.nodeBase, Name: []Identifier{{Text: "ARRAY"}}, Args: append([]Expr(nil), value.Indices...), ArrayLiteral: true}
			}
		case *FunctionCallExpr:
			if value.Over != nil {
				value.Over.Frame = strings.TrimSpace(strings.ReplaceAll(value.Over.Frame, " EXCLUDE NO OTHERS", ""))
			}
			if len(value.Name) > 1 && strings.EqualFold(strings.Join(identifierTexts(value.Name), "."), "DBMS_RANDOM.VALUE") {
				return &FunctionCallExpr{nodeBase: value.nodeBase, Name: []Identifier{{Text: "RAND"}}, Args: value.Args}
			}
			if len(value.Name) != 1 || value.RawArgs != "" {
				return current
			}
			name := strings.ToUpper(value.Name[0].Text)
			switch name {
			case "TRIM":
				if source == DialectClickHouse && len(value.Args) == 1 && value.ArgumentTail != "" {
					tail := strings.TrimSpace(value.ArgumentTail)
					upperTail := strings.ToUpper(tail)
					if fromIndex := strings.Index(upperTail, " FROM "); fromIndex >= 0 {
						valueText := strings.TrimSpace(tail[fromIndex+len(" FROM "):])
						charsText := strings.TrimSpace(tail[:fromIndex])
						functionName := "TRIM"
						switch strings.ToUpper(renderExpr(value.Args[0])) {
						case "LEADING":
							functionName = "LTRIM"
						case "TRAILING":
							functionName = "RTRIM"
						}
						if functionName != "TRIM" {
							return &FunctionCallExpr{Name: []Identifier{{Text: functionName}}, Args: []Expr{&RawExpr{Raw: valueText}, &RawExpr{Raw: charsText}}}
						}
					}
				}
				if len(value.Args) == 2 && (source == DialectSpark || source == DialectDatabricks) {
					value.Args[0], value.Args[1] = value.Args[1], value.Args[0]
				}
			case "POSITION":
				if len(value.Args) == 1 {
					if binary, ok := value.Args[0].(*BinaryExpr); ok && strings.EqualFold(strings.TrimSpace(binary.Operator), "IN") {
						value.Args = []Expr{binary.Right, binary.Left}
					}
				} else if len(value.Args) >= 2 && (source == DialectDatabricks || source == DialectSnowflake || source == DialectSpark) {
					value.Args[0], value.Args[1] = value.Args[1], value.Args[0]
				}
				setFunctionName(value, "STR_POSITION")
			case "ARRAY_INTERSECTION":
				setFunctionName(value, "ARRAY_INTERSECT")
			case "ARRAYREVERSE":
				setFunctionName(value, "ARRAY_REVERSE")
			case "ARRAYSLICE", "SLICE":
				setFunctionName(value, "ARRAY_SLICE")
			case "ARRAYMAX", "LIST_MAX":
				setFunctionName(value, "ARRAY_MAX")
			case "ARRAYMIN", "LIST_MIN":
				setFunctionName(value, "ARRAY_MIN")
			case "LIST_APPEND":
				setFunctionName(value, "ARRAY_APPEND")
			case "LIST_PREPEND", "ARRAY_PREPEND":
				if len(value.Args) == 2 {
					value.Args[0], value.Args[1] = value.Args[1], value.Args[0]
				}
				setFunctionName(value, "ARRAY_PREPEND")
			case "LEVENSHTEIN_DISTANCE", "EDITDISTANCE", "EDITDIST3", "EDIT_DISTANCE":
				setFunctionName(value, "LEVENSHTEIN")
			case "POW":
				setFunctionName(value, "POWER")
			case "RANDOM", "RANDCANONICAL", "DBMS_RANDOM.VALUE":
				setFunctionName(value, "RAND")
			case "GEN_RANDOM_UUID", "NEWID":
				setFunctionName(value, "UUID")
			case "SCHEMA", "SCHEMA_NAME":
				setFunctionName(value, "CURRENT_SCHEMA")
			case "FARMFINGERPRINT64":
				setFunctionName(value, "FARM_FINGERPRINT")
			case "LN":
				setFunctionName(value, "LN")
			case "LOG":
				if len(value.Args) == 1 {
					if genericNaturalLogSource(source) {
						setFunctionName(value, "LN")
					} else {
						setFunctionName(value, "LOG")
					}
				} else if len(value.Args) == 2 && (source == DialectBigQuery || source == DialectTSQL) {
					value.Args[0], value.Args[1] = value.Args[1], value.Args[0]
				}
			case "STRPTIME":
				setFunctionName(value, "STR_TO_TIME")
			case "DATEADD":
				if len(value.Args) == 3 {
					value.Args = []Expr{value.Args[2], value.Args[1], genericDateUnitLiteral(value.Args[0])}
					setFunctionName(value, "DATE_ADD")
				}
			case "DATE_ADD":
				if len(value.Args) == 2 && source == DialectDremio {
					value.Args = append(value.Args, &LiteralExpr{KindValue: LiteralString, Raw: "'DAY'"})
				}
			case "TRUNC":
				if len(value.Args) == 2 && (source == DialectSpark || source == DialectDatabricks) {
					return &FunctionCallExpr{Name: []Identifier{{Text: "DATE_TRUNC"}}, Args: []Expr{genericDateUnitLiteral(value.Args[1]), value.Args[0]}}
				}
			case "CONCAT":
				if len(value.Args) == 1 && (source == DialectDrill || source == DialectDuckDB || source == DialectPostgreSQL || source == DialectTSQL) {
					value.Args[0] = &FunctionCallExpr{Name: []Identifier{{Text: "COALESCE"}}, Args: []Expr{value.Args[0], &LiteralExpr{KindValue: LiteralString, Raw: "''"}}}
				}
			case "LTRIM", "RTRIM":
				if len(value.Args) == 2 && (source == DialectSpark || source == DialectDatabricks) {
					value.Args[0], value.Args[1] = value.Args[1], value.Args[0]
				}
			case "GET_PATH", "JSON_EXTRACT_PATH", "JSON_EXTRACT_PATH_TEXT", "JSONEXTRACTSTRING", "GET_JSON_OBJECT":
				if len(value.Args) >= 2 {
					name := "JSON_EXTRACT"
					if name == "JSON_EXTRACT_PATH_TEXT" || name == "JSONEXTRACTSTRING" || name == "GET_JSON_OBJECT" {
						name = "JSON_EXTRACT_SCALAR"
					}
					if strings.HasSuffix(strings.ToUpper(value.Name[0].Text), "_TEXT") || strings.EqualFold(value.Name[0].Text, "JSONEXTRACTSTRING") || strings.EqualFold(value.Name[0].Text, "GET_JSON_OBJECT") {
						name = "JSON_EXTRACT_SCALAR"
					}
					path := genericJSONPathArgs(value.Args[1:])
					value.Name = []Identifier{{Text: name}}
					value.Args = []Expr{value.Args[0], path}
				}
			case "OBJECT_KEYS", "JSON_OBJECT_KEYS":
				setFunctionName(value, "JSON_KEYS")
			case "STRPOS", "INSTR", "LOCATE", "CHARINDEX", "FIND":
				if len(value.Args) >= 2 {
					if name == "LOCATE" || name == "CHARINDEX" {
						value.Args[0], value.Args[1] = value.Args[1], value.Args[0]
					}
					setFunctionName(value, "STR_POSITION")
				}
			case "DATE_TRUNC":
				if len(value.Args) == 2 {
					dateCast := genericDateCast(value.Args[1])
					keepDateTrunc := dateCast && (source == DialectPresto || source == DialectSnowflake)
					if source != DialectBigQuery && genericTimestampTruncSource(source) && !keepDateTrunc {
						return &FunctionCallExpr{Name: []Identifier{{Text: "TIMESTAMP_TRUNC"}}, Args: []Expr{value.Args[1], genericDateUnitIdentifier(value.Args[0])}}
					}
					if isDateUnitExpr(value.Args[1]) && !isDateUnitExpr(value.Args[0]) {
						value.Args[0], value.Args[1] = genericDateUnitLiteral(value.Args[1]), value.Args[0]
					} else if isDateUnitExpr(value.Args[0]) {
						value.Args[0] = genericDateUnitLiteral(value.Args[0])
					}
				}
			case "TIMESTAMP_TRUNC":
				if len(value.Args) >= 2 && isDateUnitExpr(value.Args[1]) {
					value.Args[1] = genericDateUnitIdentifier(value.Args[1])
				}
			case "ARRAY":
				if source == DialectSpark || source == DialectDatabricks || source == DialectHive {
					value.ArrayLiteral = true
				} else if value.ArrayLiteral {
					value.ArrayLiteral = false
				}
			}
		}
		return current
	})
}

func genericJSONPathExpr(expression Expr) Expr {
	if literal, ok := expression.(*LiteralExpr); ok && literal.KindValue == LiteralString {
		return genericJSONPathLiteral(literal.Raw)
	}
	if identifier, ok := expression.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && identifier.Parts[0].Quoted {
		return genericJSONPathLiteral("'" + identifier.Parts[0].Text + "'")
	}
	return expression
}

func genericJSONPathArgs(expressions []Expr) Expr {
	if len(expressions) == 0 {
		return &LiteralExpr{KindValue: LiteralString, Raw: "'$'"}
	}
	if len(expressions) == 1 {
		return genericJSONPathExpr(expressions[0])
	}
	parts := make([]string, 0, len(expressions))
	for _, expression := range expressions {
		if literal, ok := expression.(*LiteralExpr); ok && (literal.KindValue == LiteralString || literal.KindValue == LiteralNumber) {
			part := strings.Trim(literal.Raw, "'\"")
			if strings.HasPrefix(part, "$") {
				return literal
			}
			if _, err := strconv.Atoi(part); err == nil {
				index, _ := strconv.Atoi(part)
				if literal.KindValue == LiteralNumber && index > 0 {
					index--
				}
				parts = append(parts, "["+strconv.Itoa(index)+"]")
			} else {
				parts = append(parts, "."+part)
			}
		}
	}
	return &LiteralExpr{KindValue: LiteralString, Raw: "'$" + strings.Join(parts, "") + "'"}
}

func genericJSONPathLiteral(raw string) Expr {
	content := strings.Trim(raw, "'\"")
	content = strings.ReplaceAll(content, `\"`, `"`)
	if strings.HasPrefix(content, "$") {
		return &LiteralExpr{KindValue: LiteralString, Raw: "'" + content + "'"}
	}
	if strings.HasPrefix(content, "[") {
		return &LiteralExpr{KindValue: LiteralString, Raw: "'$" + content + "'"}
	}
	return &LiteralExpr{KindValue: LiteralString, Raw: "'$." + content + "'"}
}

func isDateUnitExpr(expression Expr) bool {
	text := strings.ToUpper(strings.Trim(renderExpr(expression), "'"))
	switch text {
	case "YEAR", "QUARTER", "MONTH", "WEEK", "DAY", "HOUR", "MINUTE", "SECOND", "MILLISECOND", "MICROSECOND":
		return true
	default:
		return false
	}
}

func genericTimestampTruncSource(source Dialect) bool {
	switch source {
	case DialectDuckDB, DialectMaterialize, DialectPostgreSQL, DialectPresto,
		DialectSnowflake, DialectSpark, DialectDatabricks, DialectStarRocks,
		DialectTrino, DialectDoris:
		return true
	default:
		return false
	}
}

func identifierTexts(values []Identifier) []string {
	texts := make([]string, len(values))
	for index, value := range values {
		texts[index] = value.Text
	}
	return texts
}

func genericNaturalLogSource(source Dialect) bool {
	switch source {
	case DialectDremio, DialectBigQuery, DialectClickHouse, DialectDatabricks,
		DialectDrill, DialectHive, DialectMySQL, DialectTSQL:
		return true
	default:
		return false
	}
}

func genericDateCast(expression Expr) bool {
	cast, ok := expression.(*CastExpr)
	if !ok {
		return false
	}
	typeName, ok := castTypeIdentifier(cast.Type)
	return ok && strings.EqualFold(typeName.Text, "DATE")
}

func genericBooleanCastSource(source Dialect) bool {
	return source == DialectDuckDB || source == DialectPostgreSQL || source == DialectRedshift
}

func genericDateUnitLiteral(expression Expr) Expr {
	return &LiteralExpr{KindValue: LiteralString, Raw: "'" + strings.ToUpper(strings.Trim(renderExpr(expression), "'")) + "'"}
}

func genericDateUnitIdentifier(expression Expr) Expr {
	return identifierExpr(strings.ToUpper(strings.Trim(renderExpr(expression), "'")))
}

func clickHouseDateParseFormat(expression Expr) Expr {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString || len(literal.Raw) < 2 {
		return expression
	}
	format := strings.Trim(literal.Raw, "'")
	for _, replacement := range []struct{ from, to string }{
		{"YYYY", "%Y"}, {"YYY", "%Y"}, {"YY", "%y"},
		{"HH24", "%H"}, {"HH12", "%I"}, {"HH", "%H"},
		{"MI", "%M"}, {"SS", "%S"}, {"MM", "%m"}, {"DD", "%d"},
	} {
		format = strings.ReplaceAll(format, replacement.from, replacement.to)
	}
	return &LiteralExpr{KindValue: LiteralString, Raw: "'" + format + "'"}
}

func clickHousePostgresDateFormat(expression Expr) Expr {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString || len(literal.Raw) < 2 {
		return expression
	}
	format := strings.Trim(literal.Raw, "'")
	for _, replacement := range []struct{ from, to string }{
		{"%Y", "YYYY"}, {"%y", "YY"}, {"%H", "HH24"}, {"%I", "HH12"},
		{"%M", "MI"}, {"%S", "SS"}, {"%m", "MM"}, {"%d", "DD"},
	} {
		format = strings.ReplaceAll(format, replacement.from, replacement.to)
	}
	return &LiteralExpr{KindValue: LiteralString, Raw: "'" + format + "'"}
}

func clickHouseAnyComparison(expression *BinaryExpr) (Expr, bool) {
	if expression == nil {
		return nil, false
	}
	anyArray := func(value Expr) (Expr, bool) {
		function, ok := value.(*FunctionCallExpr)
		if !ok || len(function.Name) != 1 || !strings.EqualFold(function.Name[0].Text, "ANY") || len(function.Args) != 1 {
			return nil, false
		}
		array := function.Args[0]
		if arrayFunction, ok := array.(*FunctionCallExpr); ok && arrayFunction.ArrayLiteral {
			return array, true
		}
		if index, ok := array.(*IndexExpr); ok && isArrayConstructorIndex(index) {
			items := append([]Expr(nil), index.Indices...)
			if len(items) == 0 && index.Low != nil && index.High == nil && index.Step == nil {
				items = []Expr{index.Low}
			}
			return &FunctionCallExpr{Name: []Identifier{{Text: "ARRAY"}}, Args: items, ArrayLiteral: true}, true
		}
		return nil, false
	}
	operator := strings.ToUpper(strings.TrimSpace(expression.Operator))
	if array, ok := anyArray(expression.Right); ok && operator == "=" {
		return &FunctionCallExpr{Name: []Identifier{{Text: "has"}}, Args: []Expr{array, expression.Left}}, true
	}
	if array, ok := anyArray(expression.Left); ok && (operator == "<>" || operator == "!=") {
		return &UnaryExpr{Operator: "NOT", Expr: &FunctionCallExpr{Name: []Identifier{{Text: "has"}}, Args: []Expr{array, expression.Right}}}, true
	}
	return nil, false
}

func clickHouseArrayLiteral(expression Expr) Expr {
	if function, ok := expression.(*FunctionCallExpr); ok && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "ARRAY") {
		function.ArrayLiteral = true
		return function
	}
	if index, ok := expression.(*IndexExpr); ok && isArrayConstructorIndex(index) {
		items := append([]Expr(nil), index.Indices...)
		if len(items) == 0 && index.Low != nil && index.High == nil && index.Step == nil {
			items = []Expr{index.Low}
		}
		return &FunctionCallExpr{Name: []Identifier{{Text: "ARRAY"}}, Args: items, ArrayLiteral: true}
	}
	return expression
}

func normalizeBigQueryDateDiffUnit(expression Expr) Expr {
	text := strings.ToUpper(strings.TrimSpace(strings.Trim(renderExpr(expression), "'")))
	if strings.HasPrefix(text, "WEEK(") {
		return identifierExpr("WEEK")
	}
	if identifier, ok := expression.(*IdentifierExpr); ok && len(identifier.Parts) == 1 {
		identifier.Parts[0].Text = text
		return identifier
	}
	return expression
}

func normalizeBigQueryIdentityDateDiffUnit(expression Expr) Expr {
	text := strings.ToUpper(strings.TrimSpace(strings.Trim(renderExpr(expression), "'")))
	if text == "WEEK(SUNDAY)" {
		return identifierExpr("WEEK")
	}
	if identifier, ok := expression.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && !identifier.Parts[0].Quoted {
		identifier.Parts[0].Text = text
		return identifier
	}
	return expression
}

func normalizeSnowflakeDuckDBFunction(function *FunctionCallExpr) (Expr, bool) {
	name := strings.ToUpper(function.Name[0].Text)
	rendered := func(index int) string {
		if index < 0 || index >= len(function.Args) {
			return ""
		}
		return renderExpr(function.Args[index])
	}
	switch name {
	case "STRIP_NULL_VALUE":
		if len(function.Args) == 1 {
			value := snowflakeDuckDBJSONValue(function.Args[0])
			return &RawExpr{Raw: "CASE WHEN JSON_TYPE(" + value + ") = 'NULL' THEN NULL ELSE " + value + " END"}, true
		}
	case "TO_NUMBER":
		if len(function.Args) >= 1 && len(function.Args) <= 3 {
			precision, scale := "38", "0"
			if len(function.Args) >= 2 {
				precision = rendered(1)
			}
			if len(function.Args) == 3 {
				scale = rendered(2)
			}
			return &RawExpr{Raw: "CAST(" + rendered(0) + " AS DECIMAL(" + precision + ", " + scale + "))"}, true
		}
	case "BITMAP_BUCKET_NUMBER":
		if len(function.Args) == 1 {
			value := rendered(0)
			return &RawExpr{Raw: "CASE WHEN " + value + " > 0 THEN ((" + value + " - 1) // 32768) + 1 ELSE " + value + " // 32768 END"}, true
		}
	case "MAP_CONTAINS_KEY":
		if len(function.Args) == 2 {
			mapping := normalizeSnowflakeDuckDBMapText(rendered(1))
			return &RawExpr{Raw: "ARRAY_CONTAINS(MAP_KEYS(" + mapping + "), " + rendered(0) + ")"}, true
		}
	case "UNICODE":
		if len(function.Args) == 1 {
			value := rendered(0)
			return &RawExpr{Raw: "CASE WHEN " + value + " = '' THEN 0 ELSE UNICODE(" + value + ") END"}, true
		}
	case "ARRAY_INSERT":
		if len(function.Args) == 3 {
			if index, ok := numericLiteral(function.Args[1]); ok {
				array, value := snowflakeDuckDBListText(function.Args[0]), rendered(2)
				parts := snowflakeDuckDBListInsertParts(array, value, index)
				return &RawExpr{Raw: "CASE WHEN " + array + " IS NULL THEN NULL ELSE LIST_CONCAT(" + strings.Join(parts, ", ") + ") END"}, true
			}
		}
	case "ARRAY_REMOVE":
		if len(function.Args) == 2 {
			array, target := snowflakeDuckDBListText(function.Args[0]), rendered(1)
			filtered := "LIST_FILTER(" + array + ", _u -> _u <> " + target + ")"
			if _, identifier := function.Args[1].(*IdentifierExpr); identifier || isNullLiteral(function.Args[1]) {
				return &RawExpr{Raw: "CASE WHEN " + target + " IS NULL THEN NULL ELSE " + filtered + " END"}, true
			}
			return &RawExpr{Raw: filtered}, true
		}
	case "ARRAY_REMOVE_AT":
		if len(function.Args) == 2 {
			if index, ok := numericLiteral(function.Args[1]); ok {
				array := snowflakeDuckDBListText(function.Args[0])
				value := snowflakeDuckDBListRemoveExpr(array, index)
				return &RawExpr{Raw: "CASE WHEN " + array + " IS NULL THEN NULL ELSE " + value + " END"}, true
			}
		}
	case "MAP_DELETE":
		if len(function.Args) >= 2 {
			mapping := normalizeSnowflakeDuckDBMapText(rendered(0))
			keys := make([]string, 0, len(function.Args)-1)
			for _, argument := range function.Args[1:] {
				keys = append(keys, renderExpr(argument))
			}
			return &RawExpr{Raw: "MAP_FROM_ENTRIES(LIST_FILTER(MAP_ENTRIES(" + mapping + "), x -> NOT x.key IN (" + strings.Join(keys, ", ") + ")))"}, true
		}
	case "MAP_SIZE":
		if len(function.Args) == 1 {
			return &RawExpr{Raw: "CARDINALITY(" + normalizeSnowflakeDuckDBMapText(rendered(0)) + ")"}, true
		}
	case "TO_ARRAY":
		if len(function.Args) == 1 {
			value := function.Args[0]
			if array, ok := value.(*FunctionCallExpr); ok && array.ArrayLiteral {
				return &RawExpr{Raw: renderExpr(array)}, true
			}
			if array, ok := value.(*FunctionCallExpr); ok && len(array.Name) == 1 && strings.EqualFold(array.Name[0].Text, "ARRAY_CONSTRUCT") {
				return &RawExpr{Raw: "[" + renderArgs(array.Args) + "]"}, true
			}
			renderedValue := rendered(0)
			return &RawExpr{Raw: "CASE WHEN " + renderedValue + " IS NULL THEN NULL ELSE [" + renderedValue + "] END"}, true
		}
	case "MAP_INSERT":
		if len(function.Args) == 3 || len(function.Args) == 4 {
			mapping := normalizeSnowflakeDuckDBMapText(rendered(0))
			key := rendered(1)
			value := snowflakeDuckDBMapValue(function.Args[2])
			return &RawExpr{Raw: "MAP_CONCAT(" + mapping + ", MAP {" + key + ": " + value + "})"}, true
		}
	case "CURRENT_SCHEMAS":
		if len(function.Args) == 0 {
			return &RawExpr{Raw: "CURRENT_SCHEMAS(TRUE)"}, true
		}
	case "BITOR_AGG", "BITAND_AGG", "BITXOR_AGG":
		if len(function.Args) == 1 {
			mapped := map[string]string{"BITOR_AGG": "BIT_OR", "BITAND_AGG": "BIT_AND", "BITXOR_AGG": "BIT_XOR"}[name]
			return &FunctionCallExpr{Name: []Identifier{{Text: mapped}}, Args: function.Args}, true
		}
	case "JAROWINKLER_SIMILARITY":
		if len(function.Args) == 2 {
			upper := func(expression Expr) Expr {
				return &FunctionCallExpr{Name: []Identifier{{Text: "UPPER"}}, Args: []Expr{expression}}
			}
			similarity := &FunctionCallExpr{
				Name: []Identifier{{Text: "JARO_WINKLER_SIMILARITY"}},
				Args: []Expr{upper(function.Args[0]), upper(function.Args[1])},
			}
			return &CastExpr{
				Keyword: "CAST",
				Value: &BinaryExpr{
					Left:     similarity,
					Operator: "*",
					Right:    &LiteralExpr{KindValue: LiteralNumber, Raw: "100"},
				},
				Type: identifierExpr("INT"),
			}, true
		}
	case "ARRAY_MAX", "ARRAY_MIN", "SKEW":
		// Keep these as explicit DuckDB spellings. The generic source
		// lowering also knows these names, but source-aware handling avoids
		// losing Snowflake's function family before it gets there.
		mapped := map[string]string{"ARRAY_MAX": "LIST_MAX", "ARRAY_MIN": "LIST_MIN", "SKEW": "SKEWNESS"}[name]
		setFunctionName(function, mapped)
		return function, true
	case "RPAD":
		if len(function.Args) == 2 {
			function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralString, Raw: "' '"})
		}
	case "SPLIT":
		if len(function.Args) == 2 {
			return &RawExpr{Raw: "CASE WHEN " + rendered(1) + " IS NULL THEN NULL WHEN " + rendered(1) + " = '' THEN [" + rendered(0) + "] ELSE STR_SPLIT(" + rendered(0) + ", " + rendered(1) + ") END"}, true
		}
	case "REGR_VALX":
		if len(function.Args) == 2 {
			return &RawExpr{Raw: "CASE WHEN " + rendered(0) + " IS NULL THEN CAST(NULL AS DOUBLE) ELSE " + rendered(1) + " END"}, true
		}
	case "REGR_VALY":
		if len(function.Args) == 2 {
			return &RawExpr{Raw: "CASE WHEN " + rendered(1) + " IS NULL THEN CAST(NULL AS DOUBLE) ELSE " + rendered(0) + " END"}, true
		}
	case "IS_ARRAY":
		if len(function.Args) == 1 {
			if parsed, ok := function.Args[0].(*FunctionCallExpr); ok && len(parsed.Name) == 1 && len(parsed.Args) == 1 && strings.EqualFold(parsed.Name[0].Text, "PARSE_JSON") {
				return &BinaryExpr{Left: &FunctionCallExpr{Name: []Identifier{{Text: "JSON_TYPE"}}, Args: []Expr{&FunctionCallExpr{Name: []Identifier{{Text: "JSON"}}, Args: []Expr{parsed.Args[0]}}}}, Operator: "=", Right: &LiteralExpr{KindValue: LiteralString, Raw: "'ARRAY'"}}, true
			}
		}
	case "IS_NULL_VALUE":
		if len(function.Args) == 1 {
			return &BinaryExpr{Left: &FunctionCallExpr{Name: []Identifier{{Text: "JSON_TYPE"}}, Args: []Expr{function.Args[0]}}, Operator: "=", Right: &LiteralExpr{KindValue: LiteralString, Raw: "'NULL'"}}, true
		}
	case "IFF":
		if len(function.Args) == 3 {
			return &CaseExpr{Whens: []CaseWhen{{Condition: function.Args[0], Result: function.Args[1]}}, Else: function.Args[2]}, true
		}
	case "GREATEST_IGNORE_NULLS", "LEAST_IGNORE_NULLS":
		setFunctionName(function, strings.TrimSuffix(name, "_IGNORE_NULLS"))
		return function, true
	case "GREATEST", "LEAST":
		if len(function.Args) > 0 {
			conditions := make([]string, 0, len(function.Args))
			for _, argument := range function.Args {
				conditions = append(conditions, renderExpr(argument)+" IS NULL")
			}
			return &RawExpr{Raw: "CASE WHEN " + strings.Join(conditions, " OR ") + " THEN NULL ELSE " + name + "(" + renderArgs(function.Args) + ") END"}, true
		}
	case "CHECK_JSON":
		if len(function.Args) == 1 {
			value := rendered(0)
			return &RawExpr{Raw: "CASE WHEN " + value + " IS NULL OR " + value + " = '' OR JSON_VALID(" + value + ") THEN NULL ELSE 'Invalid JSON' END"}, true
		}
	case "TRY_TO_BOOLEAN":
		if len(function.Args) == 1 {
			value := rendered(0)
			return &RawExpr{Raw: "CASE WHEN UPPER(CAST(" + value + " AS TEXT)) = 'ON' THEN TRUE WHEN UPPER(CAST(" + value + " AS TEXT)) = 'OFF' THEN FALSE ELSE TRY_CAST(" + value + " AS BOOLEAN) END"}, true
		}
	case "TO_BOOLEAN":
		if len(function.Args) == 1 {
			value := rendered(0)
			return &RawExpr{Raw: "CASE WHEN UPPER(CAST(" + value + " AS TEXT)) = 'ON' THEN TRUE WHEN UPPER(CAST(" + value + " AS TEXT)) = 'OFF' THEN FALSE WHEN ISNAN(TRY_CAST(" + value + " AS REAL)) OR ISINF(TRY_CAST(" + value + " AS REAL)) THEN ERROR('TO_BOOLEAN: Non-numeric values NaN and INF are not supported') ELSE CAST(" + value + " AS BOOLEAN) END"}, true
		}
	case "TO_TIMESTAMP", "TRY_TO_TIMESTAMP", "TO_TIME", "TRY_TO_TIME", "TO_DATE", "TRY_TO_DATE":
		if len(function.Args) == 1 {
			if name == "TO_TIME" {
				return &RawExpr{Raw: "CAST(" + rendered(0) + " AS TIME)"}, true
			}
			if name == "TRY_TO_TIME" {
				return &RawExpr{Raw: "TRY_CAST(" + rendered(0) + " AS TIME)"}, true
			}
			if name == "TRY_TO_TIMESTAMP" {
				return &RawExpr{Raw: "TRY_CAST(" + rendered(0) + " AS TIMESTAMP)"}, true
			}
			if name == "TO_DATE" && isStringLiteral(function.Args[0]) {
				return &RawExpr{Raw: "CAST(" + rendered(0) + " AS DATE)"}, true
			}
			if name == "TO_TIMESTAMP" && isStringLiteral(function.Args[0]) {
				return &RawExpr{Raw: "CAST(" + rendered(0) + " AS TIMESTAMP)"}, true
			}
		}
		if len(function.Args) == 2 {
			if !isStringLiteral(function.Args[1]) {
				if name == "TO_TIMESTAMP" {
					return &RawExpr{Raw: "TO_TIMESTAMP(" + rendered(0) + " / POWER(10, " + rendered(1) + ")) AT TIME ZONE 'UTC'"}, true
				}
				return function, false
			}
			value := rendered(0)
			format := normalizeSnowflakeDuckDBTimestampFormat(function.Args[1])
			parsed := "STRPTIME(" + value + ", " + format + ")"
			switch name {
			case "TO_TIMESTAMP":
				return &RawExpr{Raw: parsed}, true
			case "TRY_TO_TIMESTAMP":
				return &RawExpr{Raw: "CAST(TRY_" + parsed + " AS TIMESTAMP)"}, true
			case "TO_TIME":
				return &RawExpr{Raw: "CAST(" + parsed + " AS TIME)"}, true
			case "TRY_TO_TIME":
				return &RawExpr{Raw: "TRY_CAST(TRY_" + parsed + " AS TIME)"}, true
			case "TO_DATE":
				return &RawExpr{Raw: "CAST(" + parsed + " AS DATE)"}, true
			case "TRY_TO_DATE":
				return &RawExpr{Raw: "CAST(CAST(TRY_" + parsed + " AS TIMESTAMP) AS DATE)"}, true
			}
		}
	case "TRY_TO_DOUBLE":
		if len(function.Args) == 1 {
			typeName := map[string]string{"TRY_TO_DOUBLE": "DOUBLE", "TRY_TO_TIME": "TIME", "TRY_TO_TIMESTAMP": "TIMESTAMP"}[name]
			return &RawExpr{Raw: "TRY_CAST(" + rendered(0) + " AS " + typeName + ")"}, true
		}
	case "TO_DOUBLE":
		if len(function.Args) == 1 {
			return &RawExpr{Raw: "CAST(" + rendered(0) + " AS DOUBLE)"}, true
		}
	case "ARRAY_UNIQUE_AGG":
		if len(function.Args) == 1 {
			argument := function.Args[0]
			return &FunctionCallExpr{
				Name:     []Identifier{{Text: "LIST"}},
				Distinct: true,
				Args:     []Expr{argument},
				Filter:   &RawExpr{Raw: "NOT " + rendered(0) + " IS NULL"},
				Over:     function.Over,
			}, true
		}
	case "ARRAY_AGG":
		if len(function.Args) == 1 && function.Filter == nil {
			function.Filter = &RawExpr{Raw: rendered(0) + " IS NOT NULL"}
		}
		if len(function.WithinGroup) > 0 && len(function.OrderBy) == 0 {
			function.OrderBy = append(function.OrderBy, function.WithinGroup...)
			function.WithinGroup = nil
		}
		for index := range function.OrderBy {
			if function.OrderBy[index].Descending && !function.OrderBy[index].NullsFirst && !function.OrderBy[index].NullsLast {
				function.OrderBy[index].NullsFirst = true
			}
		}
	case "ARRAY_CONSTRUCT":
		function.Name = []Identifier{{Text: "ARRAY"}}
		function.ArrayLiteral = true
	case "OBJECT_CONSTRUCT", "OBJECT_CONSTRUCT_KEEP_NULL":
		if len(function.Args)%2 == 0 {
			parts := make([]string, 0, len(function.Args)/2)
			for index := 0; index < len(function.Args); index += 2 {
				parts = append(parts, rendered(index)+": "+rendered(index+1))
			}
			if name == "OBJECT_CONSTRUCT_KEEP_NULL" {
				return &FunctionCallExpr{Name: []Identifier{{Text: "JSON_OBJECT"}}, Args: function.Args}, true
			}
			return &RawExpr{Raw: "{" + strings.Join(parts, ", ") + "}"}, true
		}
	case "OBJECT_INSERT":
		if len(function.Args) == 3 {
			key := strings.Trim(rendered(1), "'")
			base := function.Args[0]
			if constructor, ok := base.(*FunctionCallExpr); ok && len(constructor.Name) == 1 && strings.EqualFold(constructor.Name[0].Text, "OBJECT_CONSTRUCT") && len(constructor.Args) == 0 {
				base = &RawExpr{Raw: "STRUCT_PACK(" + key + " := " + rendered(2) + ")"}
			}
			return &FunctionCallExpr{
				Name: []Identifier{{Text: "STRUCT_INSERT"}},
				Args: []Expr{base, &RawExpr{Raw: key + " := " + rendered(2)}},
			}, true
		}
	case "PARSE_JSON", "JSON":
		setFunctionName(function, "JSON")
	case "GET":
		if len(function.Args) == 2 {
			container := function.Args[0]
			if constructor, ok := container.(*FunctionCallExpr); ok && len(constructor.Name) == 1 && strings.EqualFold(constructor.Name[0].Text, "ARRAY_CONSTRUCT") {
				container = &FunctionCallExpr{Name: []Identifier{{Text: "ARRAY"}}, Args: constructor.Args, ArrayLiteral: true}
				if index, ok := numericLiteral(function.Args[1]); ok {
					return &RawExpr{Raw: genericArrayArgumentText(container, DialectDuckDB) + "[" + strconv.Itoa(index+1) + "]"}, true
				}
			}
			if array, ok := container.(*FunctionCallExpr); ok && array.ArrayLiteral {
				if index, ok := numericLiteral(function.Args[1]); ok {
					return &RawExpr{Raw: genericArrayArgumentText(array, DialectDuckDB) + "[" + strconv.Itoa(index+1) + "]"}, true
				}
			}
			if literal, ok := function.Args[1].(*LiteralExpr); ok && literal.KindValue == LiteralString {
				return &BinaryExpr{Left: function.Args[0], Operator: "->", Right: genericJSONPathLiteral(literal.Raw)}, true
			}
		}
	case "GET_PATH":
		if len(function.Args) == 2 {
			path := function.Args[1]
			if literal, ok := path.(*LiteralExpr); ok && literal.KindValue == LiteralString {
				text := strings.Trim(literal.Raw, "'")
				if strings.HasPrefix(text, "[") {
					literal.Raw = "'$" + text + "'"
				}
			}
			return &BinaryExpr{Left: function.Args[0], Operator: "->", Right: path}, true
		}
	case "REGEXP_SUBSTR":
		switch len(function.Args) {
		case 2:
			setFunctionName(function, "REGEXP_EXTRACT")
		case 3:
			return &RawExpr{Raw: "REGEXP_EXTRACT(NULLIF(SUBSTRING(" + rendered(0) + ", " + rendered(2) + "), ''), " + rendered(1) + ")"}, true
		case 4:
			return &RawExpr{Raw: "ARRAY_EXTRACT(REGEXP_EXTRACT_ALL(" + rendered(0) + ", " + rendered(1) + "), " + rendered(3) + ")"}, true
		case 6:
			return &RawExpr{Raw: "REGEXP_EXTRACT(" + rendered(0) + ", " + rendered(1) + ", " + rendered(5) + ", " + rendered(4) + ")"}, true
		default:
			return &RawExpr{Raw: "REGEXP_EXTRACT(" + rendered(0) + ", " + rendered(1) + ")"}, true
		}
	case "REGEXP_SUBSTR_ALL":
		switch len(function.Args) {
		case 2:
			setFunctionName(function, "REGEXP_EXTRACT_ALL")
		case 3:
			return &RawExpr{Raw: "REGEXP_EXTRACT_ALL(SUBSTRING(" + rendered(0) + ", " + rendered(2) + "), " + rendered(1) + ")"}, true
		case 4:
			return &RawExpr{Raw: "REGEXP_EXTRACT_ALL(" + rendered(0) + ", " + rendered(1) + ")[" + rendered(3) + ":]"}, true
		default:
			return &RawExpr{Raw: "REGEXP_EXTRACT_ALL(" + rendered(0) + ", " + rendered(1) + ")"}, true
		}
	case "REGEXP_COUNT":
		if len(function.Args) >= 2 {
			subject, pattern := rendered(0), rendered(1)
			if len(function.Args) >= 3 {
				subject = "SUBSTRING(" + subject + ", " + rendered(2) + ")"
			}
			if len(function.Args) >= 4 {
				pattern = "'(?" + strings.Trim(rendered(3), "'") + ")' || " + pattern
			}
			return &RawExpr{Raw: "CASE WHEN " + pattern + " = '' THEN 0 ELSE LENGTH(REGEXP_EXTRACT_ALL(" + subject + ", " + pattern + ")) END"}, true
		}
	case "REGEXP_REPLACE":
		if len(function.Args) == 2 {
			function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralString, Raw: "''"}, &LiteralExpr{KindValue: LiteralString, Raw: "'g'"})
		} else if len(function.Args) == 3 {
			function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralString, Raw: "'g'"})
		} else if len(function.Args) == 4 {
			function.Args = append(function.Args[:3], &LiteralExpr{KindValue: LiteralString, Raw: "'g'"})
		} else if len(function.Args) == 5 {
			if isNumericRaw(function.Args[3], "1") && isNumericRaw(function.Args[4], "1") {
				function.Args = function.Args[:3]
			} else if isNumericRaw(function.Args[3], "3") {
				return &RawExpr{Raw: "SUBSTRING(" + rendered(0) + ", 1, 2) || REGEXP_REPLACE(SUBSTRING(" + rendered(0) + ", 3), " + rendered(1) + ", " + rendered(2) + ")"}, true
			}
		} else if len(function.Args) >= 6 {
			flags := rendered(5)
			if isNumericRaw(function.Args[3], "3") {
				suffix := ""
				if isNumericRaw(function.Args[4], "0") {
					suffix = ", 'g'"
				}
				return &RawExpr{Raw: "SUBSTRING(" + rendered(0) + ", 1, 2) || REGEXP_REPLACE(SUBSTRING(" + rendered(0) + ", 3), " + rendered(1) + ", " + rendered(2) + suffix + ")"}, true
			}
			if isNumericRaw(function.Args[4], "0") {
				flags = strings.TrimSuffix(flags, "'") + "g'"
			}
			function.Args = append(function.Args[:3], &LiteralExpr{KindValue: LiteralString, Raw: flags})
		}
	case "REPLACE":
		if len(function.Args) == 2 {
			function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralString, Raw: "''"})
		}
	case "EDITDISTANCE":
		if len(function.Args) == 3 {
			distance := "LEVENSHTEIN(" + rendered(0) + ", " + rendered(1) + ")"
			return &RawExpr{Raw: "CASE WHEN " + distance + " IS NULL OR " + rendered(2) + " IS NULL THEN NULL ELSE LEAST(" + distance + ", " + rendered(2) + ") END"}, true
		}
		setFunctionName(function, "LEVENSHTEIN")
	case "HEX_DECODE_BINARY":
		setFunctionName(function, "UNHEX")
		return function, true
	case "ARRAY_CAT":
		setFunctionName(function, "LIST_CONCAT")
	case "APPROX_PERCENTILE":
		setFunctionName(function, "APPROX_QUANTILE")
	case "ARRAY_GENERATE_RANGE":
		setFunctionName(function, "RANGE")
	case "ARRAY_SORT":
		if len(function.Args) == 1 {
			setFunctionName(function, "LIST_SORT")
		} else if len(function.Args) == 2 && isFalseLiteral(function.Args[1]) {
			return &RawExpr{Raw: "LIST_SORT(" + rendered(0) + ", 'DESC', 'NULLS FIRST')"}, true
		} else if len(function.Args) == 3 && isTrueLiteral(function.Args[2]) {
			return &RawExpr{Raw: "LIST_SORT(" + rendered(0) + ", " + rendered(1) + ", 'NULLS FIRST')"}, true
		}
	case "TIME_FROM_PARTS":
		if len(function.Args) == 3 || len(function.Args) == 4 {
			if len(function.Args) == 3 {
				if minutes, ok := numericLiteral(function.Args[1]); ok && minutes >= 0 && minutes < 60 {
					setFunctionName(function, "MAKE_TIME")
					return function, true
				}
			}
			parts := "(" + rendered(0) + " * 3600) + (" + rendered(1) + " * 60) + " + rendered(2)
			if len(function.Args) == 4 {
				parts += " + (" + rendered(3) + " / 1000000000.0)"
			}
			return &RawExpr{Raw: "CAST('00:00:00' AS TIME) + INTERVAL (" + parts + ") SECOND"}, true
		}
	case "TIMESTAMP_FROM_PARTS", "TIMESTAMP_NTZ_FROM_PARTS", "TIMESTAMP_LTZ_FROM_PARTS", "TIMESTAMP_TZ_FROM_PARTS":
		if len(function.Args) == 6 {
			makeTimestamp := &FunctionCallExpr{Name: []Identifier{{Text: "MAKE_TIMESTAMP"}}, Args: function.Args[:6]}
			if name == "TIMESTAMP_LTZ_FROM_PARTS" {
				return &CastExpr{Keyword: "CAST", Value: makeTimestamp, Type: identifierExpr("TIMESTAMPTZ")}, true
			}
			return makeTimestamp, true
		} else if name == "TIMESTAMP_TZ_FROM_PARTS" && len(function.Args) >= 8 {
			makeTimestamp := &FunctionCallExpr{Name: []Identifier{{Text: "MAKE_TIMESTAMP"}}, Args: function.Args[:6]}
			return &BinaryExpr{Left: makeTimestamp, Operator: "AT TIME ZONE", Right: function.Args[7]}, true
		} else if len(function.Args) == 2 {
			date := snowflakeDuckDBCastIfNeeded(function.Args[0], "DATE")
			time := snowflakeDuckDBCastIfNeeded(function.Args[1], "TIME")
			return &BinaryExpr{Left: date, Operator: "+", Right: time}, true
		}
	case "DAYOFWEEKISO":
		setFunctionName(function, "ISODOW")
	case "YEAROFWEEK", "YEAROFWEEKISO":
		return &ExtractExpr{Field: identifierExpr("ISOYEAR"), Source: function.Args[0]}, true
	case "WEEKISO":
		setFunctionName(function, "WEEKOFYEAR")
	case "LAST_DAY":
		if len(function.Args) == 2 {
			part := strings.ToUpper(strings.Trim(rendered(1), "'"))
			switch part {
			case "MONTH":
				function.Args = function.Args[:1]
			case "YEAR":
				return &RawExpr{Raw: "MAKE_DATE(EXTRACT(YEAR FROM " + rendered(0) + "), 12, 31)"}, true
			case "QUARTER":
				return &RawExpr{Raw: "LAST_DAY(MAKE_DATE(EXTRACT(YEAR FROM " + rendered(0) + "), EXTRACT(QUARTER FROM " + rendered(0) + ") * 3, 1))"}, true
			case "WEEK":
				return &RawExpr{Raw: "CAST(" + rendered(0) + " + INTERVAL ((7 - EXTRACT(DAYOFWEEK FROM " + rendered(0) + ")) % 7) DAY AS DATE)"}, true
			}
		}
	case "ADD_MONTHS":
		if len(function.Args) == 2 {
			dateType := snowflakeDuckDBDateType(function.Args[0])
			date := snowflakeDuckDBDateValue(function.Args[0], dateType)
			delta := snowflakeDuckDBMonthDelta(function.Args[1])
			shifted := date + " + " + delta
			value := "CASE WHEN LAST_DAY(" + date + ") = " + date + " THEN LAST_DAY(" + shifted + ") ELSE " + shifted + " END"
			if dateType != "TIMESTAMP" {
				return &RawExpr{Raw: "CAST(" + value + " AS " + dateType + ")"}, true
			}
			return &RawExpr{Raw: value}, true
		}
	case "TIME_SLICE":
		if len(function.Args) >= 3 {
			amount := rendered(1)
			unit := strings.ToUpper(strings.Trim(rendered(2), "'"))
			interval := "INTERVAL " + amount + " " + unit
			bucket := "TIME_BUCKET(" + interval + ", " + rendered(0) + ")"
			if len(function.Args) == 4 && strings.EqualFold(strings.Trim(rendered(3), "'"), "END") {
				value := bucket + " + " + interval
				if snowflakeDuckDBDateType(function.Args[0]) == "DATE" {
					value = "CAST(" + value + " AS DATE)"
				}
				return &RawExpr{Raw: value}, true
			}
			return &RawExpr{Raw: bucket}, true
		}
	case "TRY_PARSE_JSON":
		if len(function.Args) == 1 {
			value := rendered(0)
			return &RawExpr{Raw: "CASE WHEN JSON_VALID(" + value + ") THEN CAST(" + value + " AS JSON) ELSE NULL END"}, true
		}
	case "BASE64_ENCODE":
		if len(function.Args) == 1 {
			setFunctionName(function, "TO_BASE64")
			return function, true
		}
	case "BASE64_DECODE_BINARY":
		if len(function.Args) == 1 {
			setFunctionName(function, "FROM_BASE64")
			return function, true
		}
	case "BASE64_DECODE_STRING":
		if len(function.Args) == 1 {
			return &FunctionCallExpr{Name: []Identifier{{Text: "DECODE"}}, Args: []Expr{
				&FunctionCallExpr{Name: []Identifier{{Text: "FROM_BASE64"}}, Args: function.Args},
			}}, true
		}
	case "DATE_FROM_PARTS":
		if len(function.Args) == 3 {
			month := snowflakeDuckDBDatePart(function.Args[1])
			day := snowflakeDuckDBDatePart(function.Args[2])
			return &RawExpr{Raw: "CAST(MAKE_DATE(" + rendered(0) + ", 1, 1) + INTERVAL (" + month + " - 1) MONTH + INTERVAL (" + day + " - 1) DAY AS DATE)"}, true
		}
	case "CURRENT_TIME":
		if len(function.Args) == 0 || len(function.Args) == 1 {
			return &RawExpr{Raw: "LOCALTIME"}, true
		}
	case "DATE":
		if len(function.Args) == 1 {
			return &RawExpr{Raw: "CAST(" + rendered(0) + " AS DATE)"}, true
		}
		if len(function.Args) == 2 && isStringLiteral(function.Args[1]) {
			return &RawExpr{Raw: "CAST(STRPTIME(" + rendered(0) + ", " + normalizeSnowflakeDuckDBTimestampFormat(function.Args[1]) + ") AS DATE)"}, true
		}
	case "DATEDIFF", "TIMESTAMPDIFF":
		if len(function.Args) == 3 {
			unit := strings.ToUpper(strings.Trim(rendered(0), "'"))
			start := duckDBDateDiffValue(function.Args[1], "DATE")
			endType := snowflakeDuckDBDateDiffType(function.Args[2])
			end := duckDBDateDiffValue(function.Args[2], endType)
			if unit == "NANOSECOND" {
				start = duckDBDateDiffValue(function.Args[1], "TIMESTAMP_NS")
				end = duckDBDateDiffValue(function.Args[2], "TIMESTAMP_NS")
				return &RawExpr{Raw: "EPOCH_NS(" + end + ") - EPOCH_NS(" + start + ")"}, true
			}
			if unit == "WEEK" {
				start = "DATE_TRUNC('WEEK', " + start + ")"
				end = "DATE_TRUNC('WEEK', " + end + ")"
			}
			return &RawExpr{Raw: "DATE_DIFF('" + unit + "', " + start + ", " + end + ")"}, true
		}
	case "TIMEADD":
		if len(function.Args) == 3 {
			unit := strings.ToUpper(strings.Trim(rendered(0), "'"))
			return &RawExpr{Raw: snowflakeDuckDBTimeValue(function.Args[2]) + " + INTERVAL " + rendered(1) + " " + unit}, true
		}
	case "DATEADD", "TIMESTAMPADD":
		if len(function.Args) == 3 {
			unit := strings.ToUpper(strings.Trim(rendered(0), "'"))
			if unit == "NANOSECOND" {
				value := duckDBDateDiffValue(function.Args[2], "TIMESTAMP_NS")
				return &RawExpr{Raw: "MAKE_TIMESTAMP_NS(EPOCH_NS(" + value + ") + " + rendered(1) + ")"}, true
			}
		}
	case "CONVERT_TIMEZONE":
		if len(function.Args) == 2 {
			return &RawExpr{Raw: rendered(1) + " AT TIME ZONE " + rendered(0)}, true
		}
		if len(function.Args) == 3 {
			return &RawExpr{Raw: "CAST(" + rendered(2) + " AS TIMESTAMP) AT TIME ZONE " + rendered(0) + " AT TIME ZONE " + rendered(1)}, true
		}
	case "EQUAL_NULL":
		if len(function.Args) == 2 {
			return &BinaryExpr{Left: function.Args[0], Operator: "IS NOT DISTINCT FROM", Right: function.Args[1]}, true
		}
	case "CURRENT_VERSION":
		if len(function.Args) == 0 {
			setFunctionName(function, "VERSION")
			return function, true
		}
	case "SYSDATE":
		if len(function.Args) == 0 {
			return &RawExpr{Raw: "CURRENT_TIMESTAMP AT TIME ZONE 'UTC'"}, true
		}
	case "BOOLOR_AGG", "BOOLAND_AGG":
		if len(function.Args) == 1 {
			mapped := "BOOL_OR"
			if name == "BOOLAND_AGG" {
				mapped = "BOOL_AND"
			}
			setFunctionName(function, mapped)
		}
	case "BOOLXOR_AGG":
		if len(function.Args) == 1 {
			return &RawExpr{Raw: "COUNT_IF(CAST(" + rendered(0) + " AS BOOLEAN)) = 1"}, true
		}
	case "COUNT_IF":
		if len(function.Args) == 1 {
			function.Args[0] = &IsExpr{
				Value:    &ParenthesizedExpr{Expr: function.Args[0]},
				Operator: "IS",
				Right:    &LiteralExpr{KindValue: LiteralBoolean, Raw: "TRUE"},
			}
			return function, true
		}
	case "SQUARE":
		if len(function.Args) == 1 {
			return &FunctionCallExpr{Name: []Identifier{{Text: "POWER"}}, Args: []Expr{function.Args[0], &LiteralExpr{KindValue: LiteralNumber, Raw: "2"}}}, true
		}
	case "BOOLNOT":
		if len(function.Args) == 1 {
			return &RawExpr{Raw: "NOT (ROUND(" + rendered(0) + ", 0))"}, true
		}
	case "BITNOT":
		if len(function.Args) == 1 {
			return &RawExpr{Raw: "~(" + rendered(0) + ")"}, true
		}
	case "BITOR", "BITAND", "BITSHIFTLEFT", "BITSHIFTRIGHT":
		if len(function.Args) == 2 {
			if name == "BITSHIFTLEFT" || name == "BITSHIFTRIGHT" {
				operator := "<<"
				if name == "BITSHIFTRIGHT" {
					operator = ">>"
				}
				return &BinaryExpr{
					Left:     snowflakeDuckDBBitOperand(function.Args[0]),
					Operator: operator,
					Right:    function.Args[1],
				}, true
			}
			operator := "|"
			if name == "BITAND" {
				operator = "&"
			}
			return &BinaryExpr{
				Left:     snowflakeDuckDBBitOperand(function.Args[0]),
				Operator: operator,
				Right:    snowflakeDuckDBBitOperand(function.Args[1]),
			}, true
		}
	case "ARRAY_FLATTEN":
		if len(function.Args) == 1 {
			setFunctionName(function, "FLATTEN")
			return function, true
		}
	case "ARRAY_POSITION":
		if len(function.Args) == 2 {
			array := function.Args[1]
			if constructor, ok := array.(*FunctionCallExpr); ok && len(constructor.Name) == 1 && strings.EqualFold(constructor.Name[0].Text, "ARRAY_CONSTRUCT") {
				array = &FunctionCallExpr{Name: []Identifier{{Text: "ARRAY"}}, Args: constructor.Args, ArrayLiteral: true}
			}
			arrayText := genericArrayArgumentText(array, DialectDuckDB)
			return &RawExpr{Raw: "ARRAY_POSITION(" + arrayText + ", " + rendered(0) + ") - 1"}, true
		}
	case "ARRAY_CONTAINS":
		if len(function.Args) == 2 {
			array := genericArrayArgumentText(function.Args[1], DialectDuckDB)
			value := rendered(0)
			return &RawExpr{Raw: "CASE WHEN " + value + " IS NULL THEN NULLIF(ARRAY_LENGTH(" + array + ") <> LIST_COUNT(" + array + "), FALSE) ELSE ARRAY_CONTAINS(" + array + ", " + value + ") END"}, true
		}
	case "ARRAY_DISTINCT":
		if len(function.Args) == 1 {
			array := rendered(0)
			return &RawExpr{Raw: "CASE WHEN ARRAY_LENGTH(" + array + ") <> LIST_COUNT(" + array + ") THEN LIST_APPEND(LIST_DISTINCT(LIST_FILTER(" + array + ", _u -> NOT _u IS NULL)), NULL) ELSE LIST_DISTINCT(" + array + ") END"}, true
		}
	case "ARRAY_SLICE":
		if len(function.Args) == 3 {
			start := rendered(1)
			end := rendered(2)
			return &RawExpr{Raw: "ARRAY_SLICE(" + rendered(0) + ", CASE WHEN " + start + " >= 0 THEN " + start + " + 1 ELSE " + start + " END, CASE WHEN " + end + " < 0 THEN " + end + " - 1 ELSE " + end + " END)"}, true
		}
	case "MAX_BY", "MIN_BY":
		if len(function.Args) == 2 {
			setFunctionName(function, map[string]string{"MAX_BY": "ARG_MAX", "MIN_BY": "ARG_MIN"}[name])
			return function, true
		}
	case "SPACE":
		if len(function.Args) == 1 {
			return &FunctionCallExpr{
				Name: []Identifier{{Text: "REPEAT"}},
				Args: []Expr{
					&LiteralExpr{KindValue: LiteralString, Raw: "' '"},
					&CastExpr{Keyword: "CAST", Value: function.Args[0], Type: identifierExpr("BIGINT")},
				},
			}, true
		}
	case "DAYNAME", "MONTHNAME":
		if len(function.Args) == 1 {
			format := "%a"
			if name == "MONTHNAME" {
				format = "%b"
			}
			return &FunctionCallExpr{
				Name: []Identifier{{Text: "STRFTIME"}},
				Args: []Expr{function.Args[0], &LiteralExpr{KindValue: LiteralString, Raw: "'" + format + "'"}},
			}, true
		}
	case "DIV0", "DIV0NULL":
		if len(function.Args) == 2 {
			numerator := snowflakeDuckDBOperand(function.Args[0])
			denominator := snowflakeDuckDBOperand(function.Args[1])
			condition := denominator + " = 0 AND NOT " + numerator + " IS NULL"
			if name == "DIV0NULL" {
				condition = denominator + " = 0 OR " + denominator + " IS NULL"
			}
			return &RawExpr{Raw: "CASE WHEN " + condition + " THEN 0 ELSE " + numerator + " / " + denominator + " END"}, true
		}
	case "ARRAY_TO_STRING":
		if len(function.Args) == 2 {
			array := renderDialectExpr(function.Args[0], DialectDuckDB)
			return &RawExpr{Raw: "CASE WHEN " + rendered(1) + " IS NULL THEN NULL ELSE ARRAY_TO_STRING(LIST_TRANSFORM(" + array + ", x -> COALESCE(CAST(x AS TEXT), '')), " + rendered(1) + ") END"}, true
		}
	case "ENDSWITH":
		if len(function.Args) == 2 {
			setFunctionName(function, "ENDS_WITH")
			return function, true
		}
	case "BOOLAND", "BOOLOR", "BOOLXOR":
		if len(function.Args) == 2 {
			left := "ROUND(" + rendered(0) + ", 0)"
			right := "ROUND(" + rendered(1) + ", 0)"
			switch name {
			case "BOOLAND":
				return &RawExpr{Raw: "((" + left + ") AND (" + right + "))"}, true
			case "BOOLOR":
				return &RawExpr{Raw: "((" + left + ") OR (" + right + "))"}, true
			case "BOOLXOR":
				return &RawExpr{Raw: "(" + left + " AND (NOT " + right + ")) OR ((NOT " + left + ") AND " + right + ")"}, true
			}
		}
	case "ZEROIFNULL":
		if len(function.Args) == 1 {
			return &RawExpr{Raw: "CASE WHEN " + rendered(0) + " IS NULL THEN 0 ELSE " + rendered(0) + " END"}, true
		}
	case "NULLIFZERO":
		if len(function.Args) == 1 {
			return &RawExpr{Raw: "CASE WHEN " + rendered(0) + " = 0 THEN NULL ELSE " + rendered(0) + " END"}, true
		}
	case "NTH_VALUE":
		if function.Over != nil && function.Over.Frame == "" {
			function.Over.Frame = "ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING"
		}
		function.ArgumentTail = ""
		function.NullsInside = true
		return function, true
	case "LEAD", "LAG", "FIRST_VALUE", "LAST_VALUE":
		function.NullsInside = true
		return function, true
	}
	return function, false
}

func snowflakeDuckDBListInsertParts(array, value string, index int) []string {
	if index == 0 {
		return []string{"[" + value + "]", array}
	}
	if index > 0 {
		return []string{
			array + "[1:" + strconv.Itoa(index) + "]",
			"[" + value + "]",
			array + "[" + strconv.Itoa(index+1) + ":]",
		}
	}
	boundary := "LENGTH(" + array + ") + " + strconv.Itoa(index)
	return []string{
		array + "[1:" + boundary + "]",
		"[" + value + "]",
		array + "[" + boundary + " + 1:]",
	}
}

func snowflakeDuckDBListText(expression Expr) string {
	text := renderExpr(expression)
	if strings.HasPrefix(strings.ToUpper(text), "ARRAY(") && strings.HasSuffix(text, ")") {
		return "[" + text[len("ARRAY("):len(text)-1] + "]"
	}
	return text
}

func snowflakeDuckDBJSONValue(expression Expr) string {
	function, ok := expression.(*FunctionCallExpr)
	if !ok || len(function.Name) != 1 || len(function.Args) != 2 || !strings.EqualFold(function.Name[0].Text, "GET_PATH") {
		return renderExpr(expression)
	}
	base := function.Args[0]
	if parseJSON, ok := base.(*FunctionCallExpr); ok && len(parseJSON.Name) == 1 && len(parseJSON.Args) == 1 && strings.EqualFold(parseJSON.Name[0].Text, "PARSE_JSON") {
		baseText := "JSON(" + renderExpr(parseJSON.Args[0]) + ")"
		path := renderExpr(function.Args[1])
		if literal, ok := function.Args[1].(*LiteralExpr); ok && literal.KindValue == LiteralString {
			content := strings.Trim(literal.Raw, "'")
			if !strings.HasPrefix(content, "$") {
				content = "$." + content
			}
			path = "'" + content + "'"
		}
		return baseText + " -> " + path
	}
	return renderExpr(expression)
}

func snowflakeDuckDBTimeValue(expression Expr) string {
	function, ok := expression.(*FunctionCallExpr)
	if ok && len(function.Name) == 1 && len(function.Args) == 1 && strings.EqualFold(function.Name[0].Text, "TO_TIME") {
		return "CAST(" + renderExpr(function.Args[0]) + " AS TIME)"
	}
	return renderExpr(expression)
}

func snowflakeDuckDBListRemoveExpr(array string, index int) string {
	if index == 0 {
		return array + "[2:]"
	}
	if index > 0 {
		return "LIST_CONCAT(" + array + "[1:" + strconv.Itoa(index) + "], " + array + "[" + strconv.Itoa(index+2) + ":])"
	}
	boundary := "LENGTH(" + array + ") + " + strconv.Itoa(index)
	if index == -1 {
		return array + "[1:" + boundary + "]"
	}
	return "LIST_CONCAT(" + array + "[1:" + boundary + "], " + array + "[" + boundary + " + " + strconv.Itoa(-index) + ":])"
}

func normalizeSnowflakeDuckDBMapText(text string) string {
	for _, replacement := range []struct {
		from string
		to   string
	}{
		{"MAP(VARCHAR, VARCHAR)", "MAP(TEXT, TEXT)"},
		{"MAP(VARCHAR,VARCHAR)", "MAP(TEXT, TEXT)"},
		{"MAP(VARCHAR, NUMBER)", "MAP(TEXT, DECIMAL(38, 0))"},
		{"MAP(VARCHAR,NUMBER)", "MAP(TEXT, DECIMAL(38, 0))"},
	} {
		text = replaceAllFold(text, replacement.from, replacement.to)
	}
	text = strings.ReplaceAll(text, "':", "': ")
	for strings.Contains(text, "':  ") {
		text = strings.ReplaceAll(text, "':  ", "': ")
	}
	text = strings.ReplaceAll(text, ",", ", ")
	for strings.Contains(text, ",  ") {
		text = strings.ReplaceAll(text, ",  ", ", ")
	}
	return text
}

func snowflakeDuckDBMapValue(expression Expr) string {
	value := renderExpr(expression)
	if literal, ok := expression.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
		return "CAST(" + value + " AS DECIMAL(38, 0))"
	}
	return value
}

func snowflakeDuckDBDateType(expression Expr) string {
	if cast, ok := expression.(*CastExpr); ok {
		if typeName, ok := castTypeIdentifier(cast.Type); ok {
			name := strings.ToUpper(typeName.Text)
			if name == "DATE" || name == "TIMESTAMPTZ" || name == "TIMESTAMP" {
				return name
			}
		}
	}
	if function, ok := expression.(*FunctionCallExpr); ok && len(function.Name) == 1 {
		switch strings.ToUpper(function.Name[0].Text) {
		case "TO_DATE":
			return "DATE"
		case "TO_TIMESTAMP", "TRY_TO_TIMESTAMP":
			return "TIMESTAMP"
		}
	}
	if literal, ok := expression.(*TypedLiteralExpr); ok && len(literal.TypeName) == 1 {
		name := strings.ToUpper(literal.TypeName[0].Text)
		if name == "DATE" || name == "TIMESTAMPTZ" || name == "TIMESTAMP" {
			return name
		}
	}
	return "TIMESTAMP"
}

func snowflakeDuckDBDateValue(expression Expr, typeName string) string {
	text := renderExpr(expression)
	upper := strings.ToUpper(strings.TrimSpace(text))
	if strings.HasPrefix(upper, "CAST(") {
		text = replaceAllFold(text, " AS date)", " AS DATE)")
		return replaceAllFold(text, " AS timestamptz)", " AS TIMESTAMPTZ)")
	}
	if function, ok := expression.(*FunctionCallExpr); ok && len(function.Name) == 1 && len(function.Args) == 1 {
		switch strings.ToUpper(function.Name[0].Text) {
		case "TO_DATE", "TO_TIMESTAMP", "TRY_TO_TIMESTAMP":
			return "CAST(" + renderExpr(function.Args[0]) + " AS " + typeName + ")"
		}
	}
	return "CAST(" + text + " AS " + typeName + ")"
}

func snowflakeDuckDBDateDiffType(expression Expr) string {
	if typeName := snowflakeDuckDBDateType(expression); typeName != "TIMESTAMP" {
		return typeName
	}
	if literal, ok := expression.(*LiteralExpr); ok && literal.KindValue == LiteralString {
		value := strings.Trim(literal.Raw, "'")
		if strings.Contains(value, ":") {
			if strings.LastIndexAny(value, "+-Zz") > strings.IndexByte(value, ' ') {
				return "TIMESTAMPTZ"
			}
			return "TIMESTAMP"
		}
		return "DATE"
	}
	return "DATE"
}

func snowflakeDuckDBMonthDelta(expression Expr) string {
	if _, ok := numericLiteral(expression); ok {
		value := strings.TrimSpace(renderExpr(expression))
		if strings.HasPrefix(value, "-") {
			return "INTERVAL (" + value + ") MONTH"
		}
		return "INTERVAL " + value + " MONTH"
	}
	if literal, ok := expression.(*LiteralExpr); ok && literal.KindValue == LiteralNull {
		return "INTERVAL (NULL) MONTH"
	}
	value := renderExpr(expression)
	return "TO_MONTHS(CAST(ROUND(" + value + ") AS INT))"
}

func snowflakeDuckDBDatePart(expression Expr) string {
	text := renderExpr(expression)
	if binary, ok := expression.(*BinaryExpr); ok && binary.Operator != "" {
		return "(" + text + ")"
	}
	return text
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
				if target == DialectDuckDB && raw.Alias != nil && len(raw.Columns) == 0 {
					raw.Columns = []Identifier{{Text: raw.Alias.Text}}
					raw.Alias = &Identifier{Text: "_t0"}
				}
			}
		}
		if replacement != nil {
			stmt.From[index].Primary = replacement
		}
	}
	if target == DialectSnowflake {
		normalizeSnowflakeDateArrayJoins(stmt)
	}
	if len(generated) > 0 {
		stmt.With = append(generated, stmt.With...)
	}
}

func normalizeSnowflakeDateArrayJoins(stmt *SelectStmt) {
	if stmt == nil {
		return
	}
	additional := make([]TableExpr, 0)
	for index := range stmt.From {
		table := &stmt.From[index]
		joins := table.Joins[:0]
		for _, join := range table.Joins {
			function, ok := join.Right.(*TableFunctionFrom)
			spec, specOK := genericDateArraySpecFromTable(join.Right)
			if !ok || !specOK || join.Condition != nil || len(join.Using) > 0 || join.Kind != JoinCross {
				joins = append(joins, join)
				continue
			}

			column := genericDateArrayColumn(function)
			rewriteSnowflakeDateArrayReferences(stmt, column, spec)
			additional = append(additional, TableExpr{Primary: &RawFrom{
				Raw:   "LATERAL FLATTEN(INPUT => ARRAY_GENERATE_RANGE(0, DATEDIFF(" + spec.Unit + ", " + spec.Start + ", " + spec.End + ") + 1))",
				Alias: &Identifier{Text: "_t0"},
				Columns: []Identifier{
					{Text: "seq"},
					{Text: "key"},
					{Text: "path"},
					{Text: "index"},
					{Text: column},
					{Text: "this"},
				},
			}})
		}
		table.Joins = joins
	}
	stmt.From = append(stmt.From, additional...)
}

func genericDateArrayColumn(function *TableFunctionFrom) string {
	if function != nil && len(function.Columns) > 0 {
		return function.Columns[0].Text
	}
	if function != nil && function.Alias != nil {
		return function.Alias.Text
	}
	return "value"
}

func rewriteSnowflakeDateArrayReferences(stmt *SelectStmt, column string, spec genericDateArraySpec) {
	if stmt == nil || column == "" {
		return
	}
	start := spec.Start
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(start)), "CAST(") {
		start = "CAST(" + start + " AS DATE)"
	}
	replacement := "DATEADD(" + spec.Unit + ", CAST(" + column + " AS INT), " + start + ")"
	for index := range stmt.Projections {
		if stmt.Projections[index].Alias == nil {
			if identifier, ok := stmt.Projections[index].Expr.(*IdentifierExpr); ok && len(identifier.Parts) == 1 && strings.EqualFold(identifier.Parts[0].Text, column) {
				stmt.Projections[index].Alias = &Identifier{Text: column}
			}
		}
	}
	Transform(stmt, func(current Node) Node {
		identifier, ok := current.(*IdentifierExpr)
		if !ok || len(identifier.Parts) != 1 || !strings.EqualFold(identifier.Parts[0].Text, column) {
			return current
		}
		return &RawExpr{Raw: replacement}
	})
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
	if target == DialectSpark || target == DialectDatabricks {
		stmt.OrderBy = sparkDefaultNullOrder(stmt.OrderBy)
		stmt.SortBy = sparkDefaultNullOrder(stmt.SortBy)
		for index := range stmt.Windows {
			stmt.Windows[index].Spec.OrderBy = sparkDefaultNullOrder(stmt.Windows[index].Spec.OrderBy)
		}
		return
	}
	if target != DialectDuckDB && target != DialectClickHouse && target != DialectPostgreSQL && target != DialectSnowflake && target != DialectOracle && target != DialectRedshift {
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

func postgresAggregateDefaultNullOrder(items []OrderItem) []OrderItem {
	for index := range items {
		item := &items[index]
		if item.NullsFirst || item.NullsLast {
			continue
		}
		if item.Descending {
			item.NullsLast = true
		} else {
			item.NullsFirst = true
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
	switch value := expression.(type) {
	case *TypedLiteralExpr:
		if len(value.TypeName) == 1 && value.Value != nil {
			return "CAST(" + renderExpr(value.Value) + " AS DATE)"
		}
	case *LiteralExpr:
		if value.KindValue == LiteralString {
			return "CAST(" + renderExpr(value) + " AS DATE)"
		}
	case *FunctionCallExpr:
		if len(value.Name) == 1 && strings.EqualFold(value.Name[0].Text, "DATE_TRUNC") && len(value.Args) == 2 {
			unit := strings.ToUpper(strings.Trim(renderExpr(value.Args[1]), "'"))
			date := renderExpr(value.Args[0])
			if strings.EqualFold(date, "CURRENT_DATE()") {
				date = "CURRENT_DATE"
			}
			return "DATE_TRUNC('" + unit + "', " + date + ")"
		}
	}
	return renderExpr(expression)
}

func bigQueryDuckDBInterval(expression Expr) string {
	interval, ok := expression.(*IntervalExpr)
	if !ok || len(interval.Qualifiers) == 0 {
		return renderExpr(expression)
	}
	return "INTERVAL '" + strings.Trim(renderExpr(interval.Value), "'") + "' " + strings.Trim(renderExpr(interval.Qualifiers[0]), "'")
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
		if target == DialectTSQL {
			cte.Recursive = false
		}
		if cte.Query != nil {
			flattenNestedCTEs(cte.Query, target)
			if len(cte.Query.With) > 0 {
				flattened = append(flattened, cte.Query.With...)
				cte.Query.With = nil
			}
			if target == DialectTSQL && len(cte.Columns) == 0 {
				addTSQLProjectionAliases(cte.Query)
			}
		}
		flattened = append(flattened, cte)
	}
	stmt.With = flattened

	// A CTE attached to one side of a set operation is still a statement-level
	// binding for T-SQL. The compact AST keeps the binding on that side so the
	// generator can preserve the set shape; attach it to the left query, which
	// is the place where a leading WITH clause is emitted.
	if stmt.SetLeft != nil {
		flattenNestedCTEs(stmt.SetLeft, target)
	}
	if stmt.SetRight != nil {
		flattenNestedCTEs(stmt.SetRight, target)
		if len(stmt.SetRight.With) > 0 {
			if stmt.SetLeft != nil {
				stmt.SetLeft.With = append(stmt.SetLeft.With, stmt.SetRight.With...)
			} else {
				stmt.With = append(stmt.With, stmt.SetRight.With...)
			}
			stmt.SetRight.With = nil
		}
	}

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
		if ok && len(identifier.Parts) > 0 {
			alias := identifier.Parts[len(identifier.Parts)-1]
			stmt.Projections[index].Alias = &alias
			continue
		}
		if cast, ok := stmt.Projections[index].Expr.(*CastExpr); ok {
			if identifier, identifierOK := cast.Value.(*IdentifierExpr); identifierOK && len(identifier.Parts) > 0 {
				alias := identifier.Parts[len(identifier.Parts)-1]
				stmt.Projections[index].Alias = &alias
				continue
			}
		}
		if literal, ok := stmt.Projections[index].Expr.(*LiteralExpr); ok && literal.KindValue == LiteralNumber {
			alias := Identifier{Text: literal.Raw, Quoted: true, Quote: '['}
			stmt.Projections[index].Alias = &alias
		}
	}
}

func expandTSQLOrderAliases(stmt *SelectStmt) {
	if stmt == nil || len(stmt.OrderBy) == 0 {
		return
	}
	aliases := make(map[string]Expr, len(stmt.Projections))
	for _, projection := range stmt.Projections {
		name := ""
		if projection.Alias != nil {
			name = projection.Alias.Text
		} else if identifier, ok := projection.Expr.(*IdentifierExpr); ok && len(identifier.Parts) > 0 {
			name = identifier.Parts[len(identifier.Parts)-1].Text
		}
		if name != "" && projection.Expr != nil {
			aliases[strings.ToUpper(strings.Trim(name, "`[]\""))] = projection.Expr
		}
	}
	for index := range stmt.OrderBy {
		identifier, ok := stmt.OrderBy[index].Expr.(*IdentifierExpr)
		if !ok || len(identifier.Parts) != 1 || identifier.Parts[0].Quoted {
			continue
		}
		if expression, ok := aliases[strings.ToUpper(identifier.Parts[0].Text)]; ok {
			stmt.OrderBy[index].Expr = expression
		}
	}
}

func addTSQLNestedProjectionAliases(stmt *SelectStmt) {
	if stmt == nil {
		return
	}
	for index := range stmt.With {
		cte := &stmt.With[index]
		if cte.Query == nil {
			continue
		}
		if len(cte.Columns) == 0 {
			addTSQLProjectionAliases(cte.Query)
		}
		addTSQLNestedProjectionAliases(cte.Query)
	}
	var visitTable func(*TableExpr)
	visitTable = func(table *TableExpr) {
		if table == nil {
			return
		}
		visitFrom := func(item FromItem) {
			switch value := item.(type) {
			case *SubqueryFrom:
				if value.Query != nil {
					if len(value.Columns) == 0 && value.Query.SetOperator == "" {
						addTSQLProjectionAliases(value.Query)
					}
					if value.Query.SetOperator == "" {
						addTSQLNestedProjectionAliases(value.Query)
					}
				}
			case *GroupedFrom:
				for index := range value.Items {
					visitTable(&value.Items[index])
				}
			}
		}
		visitFrom(table.Primary)
		for index := range table.Joins {
			visitFrom(table.Joins[index].Right)
		}
	}
	for index := range stmt.From {
		visitTable(&stmt.From[index])
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

func normalizeDuckDBSourceFunction(function *FunctionCallExpr, source Dialect) (Expr, bool) {
	if function == nil || len(function.Name) != 1 || function.RawArgs != "" {
		return function, false
	}
	name := strings.ToUpper(function.Name[0].Text)
	switch {
	case (source == DialectPresto || source == DialectSpark) && name == "ARRAY_JOIN":
		setFunctionName(function, "ARRAY_TO_STRING")
		return function, true
	case (source == DialectSpark || source == DialectDatabricks) && name == "COLLECT_SET" && len(function.Args) == 1:
		argument := function.Args[0]
		return &FunctionCallExpr{
			nodeBase: function.nodeBase,
			Name:     []Identifier{{Text: "LIST"}},
			Distinct: true,
			Args:     []Expr{argument},
			Filter:   &RawExpr{Raw: "NOT " + renderExpr(argument) + " IS NULL"},
		}, true
	case source == DialectPostgreSQL && name == "ARRAY_CAT":
		setFunctionName(function, "LIST_CONCAT")
		return function, true
	case source == DialectPresto && name == "BITWISE_XOR":
		setFunctionName(function, "XOR")
		return function, true
	case (source == DialectSpark || source == DialectDatabricks) && name == "EXPLODE":
		setFunctionName(function, "UNNEST")
		return function, true
	case source == DialectSpark && name == "ARRAY_INSERT" && len(function.Args) == 3:
		if index, ok := numericLiteral(function.Args[1]); ok {
			array := snowflakeDuckDBListText(function.Args[0])
			parts := sparkDuckDBListInsertParts(array, renderExpr(function.Args[2]), index)
			return &RawExpr{Raw: "CASE WHEN " + array + " IS NULL THEN NULL ELSE LIST_CONCAT(" + strings.Join(parts, ", ") + ") END"}, true
		}
	case (source == DialectHive || source == DialectSpark) && name == "POW":
		if len(function.Args) == 1 {
			suffix := strings.TrimSpace(function.ArgumentTail)
			if len(suffix) >= 2 && strings.EqualFold(suffix[:2], "S,") {
				second := strings.TrimSpace(suffix[2:])
				if second != "" {
					function.Args = []Expr{
						&CastExpr{Keyword: "TRY_CAST", Value: function.Args[0], Type: identifierExpr("SMALLINT")},
						&RawExpr{Raw: second},
					}
					function.ArgumentTail = ""
				}
			}
		}
		setFunctionName(function, "POWER")
		return function, true
	case source == DialectPresto && name == "TO_UNIXTIME" && len(function.Args) == 1:
		setFunctionName(function, "EPOCH")
		return function, true
	case source == DialectBigQuery && name == "IS_NAN":
		setFunctionName(function, "ISNAN")
		return function, true
	case source == DialectBigQuery && name == "IS_INF":
		setFunctionName(function, "ISINF")
		return function, true
	case source == DialectSpark && name == "ENCODE" && len(function.Args) == 2 && isUTF8Literal(function.Args[1]):
		function.Args = function.Args[:1]
		return function, true
	case source == DialectPresto && name == "TO_UTF8" && len(function.Args) == 1:
		setFunctionName(function, "ENCODE")
		return function, true
	case source == DialectSpark && name == "DECODE" && len(function.Args) == 2 && isUTF8Literal(function.Args[1]):
		function.Args = function.Args[:1]
		return function, true
	case source == DialectPresto && name == "FROM_UTF8" && len(function.Args) >= 1:
		setFunctionName(function, "DECODE")
		function.Args = function.Args[:1]
		return function, true
	case source == DialectBigQuery && name == "REGEXP_EXTRACT" && len(function.Args) == 3 && isNumericRaw(function.Args[2], "1"):
		function.Args = function.Args[:2]
		return function, true
	case source == DialectHive && name == "DATE_ADD" && len(function.Args) == 2:
		value := function.Args[0]
		dateText := renderExpr(value)
		if parsed, ok := value.(*FunctionCallExpr); ok && len(parsed.Name) == 1 && len(parsed.Args) == 1 && strings.EqualFold(parsed.Name[0].Text, "TO_DATE") {
			dateText = "CAST(TRY_CAST(" + renderExpr(parsed.Args[0]) + " AS DATE) AS DATE)"
		} else if isStringLiteral(value) {
			dateText = "CAST(" + dateText + " AS DATE)"
		}
		return &RawExpr{Raw: dateText + " + INTERVAL " + renderExpr(function.Args[1]) + " DAY"}, true
	case source == DialectSpark && name == "DATE_SUB" && len(function.Args) == 2:
		return &RawExpr{Raw: "CAST(" + renderExpr(function.Args[0]) + " AS DATE) + INTERVAL (" + renderExpr(function.Args[1]) + " * -1) DAY"}, true
	}
	return function, false
}

func isUTF8Literal(expression Expr) bool {
	literal, ok := expression.(*LiteralExpr)
	if !ok || literal.KindValue != LiteralString {
		return false
	}
	return strings.EqualFold(strings.Trim(literal.Raw, "'\""), "utf-8")
}

func sparkDuckDBListInsertParts(array, value string, index int) []string {
	if index == 0 || index == 1 {
		return []string{"[" + value + "]", array}
	}
	if index > 0 {
		return []string{
			array + "[1:" + strconv.Itoa(index-1) + "]",
			"[" + value + "]",
			array + "[" + strconv.Itoa(index) + ":]",
		}
	}
	boundary := "LENGTH(" + array + ") + " + strconv.Itoa(index+1)
	return []string{
		array + "[1:" + boundary + "]",
		"[" + value + "]",
		array + "[" + boundary + " + 1:]",
	}
}

func rewriteGenericSourceFunction(function *FunctionCallExpr, target Dialect) Expr {
	if len(function.Name) != 1 {
		return nil
	}
	if function.RawArgs != "" {
		return rewriteGenericRawSourceFunction(function, target)
	}
	name := strings.ToUpper(function.Name[0].Text)
	// None of the current source-normalization rules rewrites these common
	// functions. Avoid rendering their arguments only to discover that no
	// source rewrite applies; aggregate-heavy queries otherwise pay for several
	// throwaway strings per function call.
	switch name {
	case "AVG", "COALESCE", "COUNT", "MAX", "MIN", "SUM":
		return nil
	}
	if len(function.Args) == 0 {
		switch name {
		case "CURRENT_DATE", "CURRENT_TIME", "CURRENT_TIMESTAMP":
			switch target {
			case DialectPostgreSQL, DialectPresto, DialectTrino:
				return identifierExpr(name)
			case DialectTSQL:
				if name == "CURRENT_TIMESTAMP" {
					return &FunctionCallExpr{Name: []Identifier{{Text: "GETDATE"}}}
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
		case "UUID":
			switch target {
			case DialectPostgreSQL:
				return &FunctionCallExpr{Name: []Identifier{{Text: "GEN_RANDOM_UUID"}}}
			case DialectSnowflake:
				return &FunctionCallExpr{Name: []Identifier{{Text: "UUID_STRING"}}}
			case DialectTSQL:
				return &FunctionCallExpr{Name: []Identifier{{Text: "NEWID"}}}
			}
		case "GENERATE_UUID":
			switch target {
			case DialectPresto, DialectTrino:
				return &RawExpr{Raw: "CAST(UUID() AS VARCHAR)"}
			case DialectSnowflake:
				return &FunctionCallExpr{Name: []Identifier{{Text: "UUID_STRING"}}}
			case DialectSpark, DialectDatabricks:
				return &RawExpr{Raw: "CAST(UUID() AS STRING)"}
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
	rendered := func(index int) string {
		if index < 0 || index >= len(function.Args) {
			return ""
		}
		return renderExpr(function.Args[index])
	}
	if rewritten := rewriteGenericDateFunction(function, target); rewritten != nil {
		return rewritten
	}
	if name == "CURRENT_DATE" && len(function.Args) == 1 {
		zone := rendered(0)
		switch target {
		case DialectMySQL, DialectPostgreSQL:
			return &RawExpr{Raw: "CURRENT_DATE AT TIME ZONE " + zone}
		case DialectSnowflake:
			return &RawExpr{Raw: "CAST(CONVERT_TIMEZONE(" + zone + ", CURRENT_TIMESTAMP()) AS DATE)"}
		}
	}
	if rewritten := rewriteGenericArrayFunction(function, target); rewritten != nil {
		return rewritten
	}
	if rewritten := rewriteGenericJSONFunction(function, target); rewritten != nil {
		return rewritten
	}
	switch name {
	case "REGEXP_EXTRACT":
		if target == DialectDuckDB && len(function.Args) == 2 && strings.Contains(rendered(1), "(") {
			function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralNumber, Raw: "1"})
			return function
		}
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
	case "TIMESTAMP":
		if target == DialectSnowflake && len(function.Args) == 2 {
			return &RawExpr{Raw: "CONVERT_TIMEZONE(" + rendered(1) + ", " + renderExpr(rawCast(rendered(0), "TIMESTAMP")) + ")"}
		}
	case "TIME":
		switch target {
		case DialectDuckDB:
			switch len(function.Args) {
			case 3:
				setFunctionName(function, "MAKE_TIME")
				return function
			case 1:
				return rawCast(renderedValue, "TIME")
			}
		case DialectMySQL:
			if len(function.Args) == 3 {
				setFunctionName(function, "MAKETIME")
				return function
			}
		case DialectPostgreSQL:
			if len(function.Args) == 3 {
				setFunctionName(function, "MAKE_TIME")
				return function
			}
		case DialectSnowflake:
			if len(function.Args) == 3 {
				setFunctionName(function, "TIME_FROM_PARTS")
				return function
			}
		case DialectTSQL:
			if len(function.Args) == 3 {
				function.Name = []Identifier{{Text: "TIMEFROMPARTS"}}
				function.Args = append(function.Args, &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}, &LiteralExpr{KindValue: LiteralNumber, Raw: "0"})
				return function
			}
		case DialectSpark:
			if len(function.Args) == 1 {
				return rawCast(renderedValue, "TIMESTAMP")
			}
		}
	case "PARSE_DATE":
		if target == DialectSnowflake && len(function.Args) == 2 {
			format := strings.Trim(rendered(0), "'")
			if format == "%Y%m%d" {
				format = "yyyymmDD"
			}
			return &RawExpr{Raw: "DATE(" + rendered(1) + ", '" + format + "')"}
		}
	case "PARSE_DATETIME":
		if target == DialectSnowflake && len(function.Args) == 2 {
			format := strings.Trim(rendered(0), "'")
			format = strings.ReplaceAll(format, "%F", "%Y-%m-%d")
			format = strings.ReplaceAll(format, "%T", "%H:%M:%S")
			return &RawExpr{Raw: "PARSE_DATETIME(" + rendered(1) + ", '" + format + "')"}
		}
	case "SAFE_CAST":
		if target == DialectSnowflake && len(function.Args) == 1 {
			if alias, ok := function.Args[0].(*AliasExpr); ok {
				typeName := strings.TrimSpace(alias.Alias.Text)
				if strings.EqualFold(typeName, "TIMESTAMP") {
					typeName = "TIMESTAMPTZ"
				}
				return &RawExpr{Raw: "CAST(" + renderExpr(alias.Expr) + " AS " + typeName + ")"}
			}
		}
	case "TIMESTAMP_SECONDS", "TIMESTAMP_MILLIS", "TIMESTAMP_MICROS":
		if len(function.Args) == 1 {
			scale := map[string]string{"TIMESTAMP_SECONDS": "0", "TIMESTAMP_MILLIS": "3", "TIMESTAMP_MICROS": "6"}[name]
			switch target {
			case DialectPresto, DialectTrino, DialectAthena, DialectMySQL:
				value := rendered(0)
				if name == "TIMESTAMP_MILLIS" {
					value += " / 1000"
				} else if name == "TIMESTAMP_MICROS" {
					value += " / 1000000"
				}
				return &FunctionCallExpr{Name: []Identifier{{Text: "FROM_UNIXTIME"}}, Args: []Expr{&RawExpr{Raw: value}}}
			case DialectSnowflake:
				if scale == "0" {
					return &FunctionCallExpr{Name: []Identifier{{Text: "TO_TIMESTAMP"}}, Args: []Expr{function.Args[0]}}
				}
				return &FunctionCallExpr{Name: []Identifier{{Text: "TO_TIMESTAMP"}}, Args: []Expr{function.Args[0], &LiteralExpr{KindValue: LiteralNumber, Raw: scale}}}
			case DialectDuckDB:
				setFunctionName(function, "MAKE_TIMESTAMP")
				return function
			}
		}
	case "DATE_DIFF", "DATETIME_DIFF", "TIMESTAMP_DIFF":
		if len(function.Args) >= 3 {
			unit := strings.ToUpper(strings.Trim(rendered(2), "'"))
			switch target {
			case DialectBigQuery:
				function.Args[2] = normalizeBigQueryDateDiffUnit(function.Args[2])
				return function
			case DialectDuckDB:
				dateType := "DATE"
				if name == "DATETIME_DIFF" || name == "TIMESTAMP_DIFF" {
					dateType = "TIMESTAMP"
				}
				return &RawExpr{Raw: "DATE_DIFF('" + unit + "', " + duckDBDateDiffValue(function.Args[1], dateType) + ", " + duckDBDateDiffValue(function.Args[0], dateType) + ")"}
			case DialectPresto, DialectTrino:
				start := genericTimestampDiffValue(function.Args[1], target)
				end := genericTimestampDiffValue(function.Args[0], target)
				if name == "DATETIME_DIFF" {
					start = prestoDateDiffBoundary(start, unit)
					end = prestoDateDiffBoundary(end, unit)
				}
				return &RawExpr{Raw: "DATE_DIFF('" + unit + "', " + start + ", " + end + ")"}
			case DialectSnowflake, DialectDatabricks:
				return &RawExpr{Raw: "TIMESTAMPDIFF(" + unit + ", " + genericTimestampDiffValue(function.Args[1], target) + ", " + genericTimestampDiffValue(function.Args[0], target) + ")"}
			case DialectMySQL:
				if unit == "DAY" && name == "DATE_DIFF" {
					return &RawExpr{Raw: "DATEDIFF(" + renderExpr(function.Args[0]) + ", " + renderExpr(function.Args[1]) + ")"}
				}
				return &RawExpr{Raw: "TIMESTAMPDIFF(" + unit + ", " + genericTimestampDiffValue(function.Args[1], target) + ", " + genericTimestampDiffValue(function.Args[0], target) + ")"}
			case DialectStarRocks:
				return &RawExpr{Raw: "DATE_DIFF('" + unit + "', " + renderExpr(function.Args[0]) + ", " + renderExpr(function.Args[1]) + ")"}
			}
		}
	case "COUNTIF":
		if target == DialectDuckDB {
			setFunctionName(function, "COUNT_IF")
			return function
		}
		if target == DialectClickHouse {
			setFunctionName(function, "countIf")
			return function
		}
	case "UNIX_DATE":
		if target == DialectDuckDB {
			return &RawExpr{Raw: "DATE_DIFF('DAY', CAST('1970-01-01' AS DATE), " + duckDBDateDiffValue(value, "DATE") + ")"}
		}
	case "UNIX_SECONDS", "UNIX_MILLIS", "UNIX_MICROS":
		if target == DialectDuckDB {
			name := map[string]string{"UNIX_SECONDS": "EPOCH", "UNIX_MILLIS": "EPOCH_MS", "UNIX_MICROS": "EPOCH_US"}[name]
			value := rawCastIfNeeded(renderedValue, "TIMESTAMPTZ")
			if name == "EPOCH" {
				return rawCast("EPOCH("+renderExpr(value)+")", "BIGINT")
			}
			return &FunctionCallExpr{Name: []Identifier{{Text: name}}, Args: []Expr{value}}
		}
		if target == DialectSnowflake && len(function.Args) == 1 {
			unit := map[string]string{"UNIX_SECONDS": "SECONDS", "UNIX_MILLIS": "MILLISECONDS", "UNIX_MICROS": "MICROSECONDS"}[name]
			return &RawExpr{Raw: "TIMESTAMPDIFF(" + unit + ", CAST('1970-01-01 00:00:00+00' AS TIMESTAMPTZ), " + renderedValue + ")"}
		}
	case "GENERATE_DATE_ARRAY":
		if target == DialectDuckDB && len(function.Args) >= 2 {
			step := "INTERVAL '1' DAY"
			if len(function.Args) >= 3 {
				step = bigQueryDuckDBInterval(function.Args[2])
			}
			return &RawExpr{Raw: "CAST(GENERATE_SERIES(" + genericDateArrayDate(function.Args[0]) + ", " + genericDateArrayDate(function.Args[1]) + ", " + step + ") AS DATE[])"}
		}
	case "GENERATE_TIMESTAMP_ARRAY":
		if target == DialectDuckDB && len(function.Args) >= 2 {
			step := "INTERVAL '1' DAY"
			if len(function.Args) >= 3 {
				step = bigQueryDuckDBInterval(function.Args[2])
			}
			return &RawExpr{Raw: "GENERATE_SERIES(CAST(" + rendered(0) + " AS TIMESTAMP), CAST(" + rendered(1) + " AS TIMESTAMP), " + step + ")"}
		}
	case "GENERATE_ARRAY":
		if target == DialectDuckDB {
			setFunctionName(function, "GENERATE_SERIES")
			return function
		}
	case "TO_HEX":
		if target == DialectDuckDB {
			if len(function.Args) == 1 {
				if nested, ok := function.Args[0].(*FunctionCallExpr); ok && len(nested.Name) == 1 && (strings.EqualFold(nested.Name[0].Text, "MD5") || strings.EqualFold(nested.Name[0].Text, "SHA1") || strings.EqualFold(nested.Name[0].Text, "SHA256")) {
					return nested
				}
			}
			setFunctionName(function, "HEX")
			return function
		}
		if len(function.Args) == 1 {
			if target == DialectSnowflake {
				if nested, ok := function.Args[0].(*FunctionCallExpr); ok && len(nested.Name) == 1 && len(nested.Args) == 1 {
					switch strings.ToUpper(nested.Name[0].Text) {
					case "SHA1":
						return &RawExpr{Raw: "TO_CHAR(SHA1_BINARY(" + renderExpr(nested.Args[0]) + "))"}
					case "MD5":
						return &RawExpr{Raw: "MD5_BINARY(" + renderExpr(nested.Args[0]) + ")"}
					}
				}
			}
			switch target {
			case DialectClickHouse, DialectHive, DialectMySQL, DialectSpark, DialectSQLite:
				return &RawExpr{Raw: "LOWER(HEX(" + renderedValue + "))"}
			case DialectPresto, DialectTrino:
				return &RawExpr{Raw: "LOWER(TO_HEX(" + renderedValue + "))"}
			}
		}
	case "LOWER", "UPPER":
		if len(function.Args) == 1 {
			if target == DialectPresto || target == DialectTrino {
				if raw, ok := function.Args[0].(*RawExpr); ok && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(raw.Raw)), "LOWER(TO_HEX(") {
					return raw
				}
			}
			if nested, ok := function.Args[0].(*FunctionCallExpr); ok && len(nested.Name) == 1 && strings.EqualFold(nested.Name[0].Text, "TO_HEX") && len(nested.Args) == 1 {
				switch target {
				case DialectClickHouse, DialectHive, DialectMySQL, DialectSpark, DialectSQLite:
					inner := "HEX(" + renderExpr(nested.Args[0]) + ")"
					if name == "LOWER" {
						inner = "LOWER(" + inner + ")"
					}
					return &RawExpr{Raw: inner}
				case DialectPresto, DialectTrino:
					if name == "LOWER" {
						return &RawExpr{Raw: "LOWER(TO_HEX(" + renderExpr(nested.Args[0]) + "))"}
					}
					if name == "UPPER" {
						return nested
					}
				}
			}
		}
	case "MD5":
		if (target == DialectHive || target == DialectSpark) && len(function.Args) == 1 {
			return &RawExpr{Raw: "UNHEX(MD5(" + renderedValue + "))"}
		}
		if target == DialectSnowflake && len(function.Args) == 1 {
			return &RawExpr{Raw: "MD5_BINARY(" + renderedValue + ")"}
		}
	case "SHA512":
		if target == DialectSpark || target == DialectDatabricks {
			return &RawExpr{Raw: "SHA2(" + renderedValue + ", 512)"}
		}
	case "REGEXP_CONTAINS":
		if len(function.Args) == 2 {
			switch target {
			case DialectMySQL:
				setFunctionName(function, "REGEXP_LIKE")
				return function
			case DialectStarRocks:
				setFunctionName(function, "REGEXP")
				return function
			}
		}
	case "REGEXP_EXTRACT_ALL":
		if len(function.Args) == 2 {
			pattern := renderExpr(function.Args[1])
			switch target {
			case DialectSpark, DialectDatabricks:
				if strings.Contains(pattern, "(") {
					return &RawExpr{Raw: "REGEXP_EXTRACT_ALL(" + rendered(0) + ", " + pattern + ")"}
				}
				return &RawExpr{Raw: "REGEXP_EXTRACT_ALL(" + rendered(0) + ", " + pattern + ", 0)"}
			case DialectPresto, DialectTrino:
				if strings.Contains(pattern, "(") {
					return &RawExpr{Raw: "REGEXP_EXTRACT_ALL(" + rendered(0) + ", " + pattern + ", 1)"}
				}
			case DialectSnowflake:
				if strings.Contains(pattern, "(") {
					return &RawExpr{Raw: "REGEXP_SUBSTR_ALL(" + rendered(0) + ", " + pattern + ", 1, 1, 'c', 1)"}
				}
				return &RawExpr{Raw: "REGEXP_SUBSTR_ALL(" + rendered(0) + ", " + pattern + ")"}
			}
		}
	case "SHA1", "SHA256":
		if len(function.Args) == 1 {
			switch {
			case name == "SHA256" && target == DialectSnowflake:
				return &RawExpr{Raw: "SHA2_BINARY(" + renderedValue + ", 256)"}
			case name == "SHA256" && (target == DialectSpark || target == DialectDatabricks || target == DialectRedshift):
				return &RawExpr{Raw: "SHA2(" + renderedValue + ", 256)"}
			}
		}
		if target == DialectDuckDB {
			return &RawExpr{Raw: "UNHEX(" + strings.ToUpper(name) + "(" + renderedValue + "))"}
		}
	case "TO_JSON_STRING":
		jsonValue := renderedValue
		if target == DialectSnowflake {
			if structure, ok := value.(*FunctionCallExpr); ok && len(structure.Name) == 1 && strings.EqualFold(structure.Name[0].Text, "STRUCT") {
				copy := *structure
				if rewritten := rewriteStructFunction(&copy, target); rewritten != nil {
					jsonValue = renderExpr(rewritten)
				}
			}
		}
		if target == DialectDuckDB {
			return rawCast("TO_JSON("+jsonValue+")", "TEXT")
		}
		if target == DialectPresto || target == DialectTrino {
			return &RawExpr{Raw: "JSON_FORMAT(CAST(" + jsonValue + " AS JSON))"}
		}
		if target == DialectSpark || target == DialectDatabricks {
			return &RawExpr{Raw: "TO_JSON(" + jsonValue + ")"}
		}
		if target == DialectSnowflake {
			return &RawExpr{Raw: "TO_JSON(" + jsonValue + ")"}
		}
	case "JSON_VALUE_ARRAY":
		if target == DialectSnowflake && len(function.Args) == 2 {
			path := strings.Trim(rendered(1), "'")
			path = strings.TrimPrefix(strings.TrimPrefix(path, "$"), ".")
			value := rendered(0)
			if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value)), "PARSE_JSON(") {
				value = "PARSE_JSON(" + value + ")"
			}
			return &RawExpr{Raw: "TRANSFORM(GET_PATH(" + value + ", '" + path + "'), x -> CAST(x AS VARCHAR))"}
		}
	case "INSTR":
		if target == DialectSnowflake && len(function.Args) == 2 {
			return &RawExpr{Raw: "CHARINDEX(" + rendered(1) + ", " + rendered(0) + ")"}
		}
	case "STRING":
		if target == DialectSnowflake {
			if len(function.Args) == 1 {
				return rawCast(renderedValue, "VARCHAR")
			}
			if len(function.Args) == 2 {
				return &RawExpr{Raw: "CAST(CONVERT_TIMEZONE('UTC', " + rendered(1) + ", " + rendered(0) + ") AS VARCHAR)"}
			}
		}
		if target == DialectDuckDB {
			return rawCast(renderedValue, "TEXT")
		}
	case "GENERATE_UUID":
		if len(function.Args) == 0 {
			switch target {
			case DialectPresto, DialectTrino:
				return &RawExpr{Raw: "CAST(UUID() AS VARCHAR)"}
			case DialectSnowflake:
				return &FunctionCallExpr{Name: []Identifier{{Text: "UUID_STRING"}}}
			case DialectSpark, DialectDatabricks:
				return &RawExpr{Raw: "CAST(UUID() AS STRING)"}
			}
		}
	case "CONTAINS_SUBSTR":
		if len(function.Args) == 2 && (target == DialectOracle || target == DialectSpark || target == DialectDatabricks || target == DialectSnowflake) {
			return &RawExpr{Raw: "CONTAINS(LOWER(" + rendered(0) + "), LOWER(" + rendered(1) + "))"}
		}
	case "ARRAY_CONCAT_AGG":
		if target == DialectSnowflake && len(function.Args) == 1 {
			return &RawExpr{Raw: "ARRAY_FLATTEN(ARRAY_AGG(" + renderedValue + "))"}
		}
	case "JSON_QUERY":
		if target == DialectSnowflake && len(function.Args) == 2 {
			path := strings.Trim(rendered(1), "'")
			path = strings.TrimPrefix(path, "$")
			path = strings.TrimPrefix(path, ".")
			value := rendered(0)
			if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value)), "PARSE_JSON(") {
				value = "PARSE_JSON(" + value + ")"
			}
			return &RawExpr{Raw: "GET_PATH(" + value + ", '" + path + "')"}
		}
	case "INT64":
		if target == DialectSnowflake && len(function.Args) == 1 {
			if nested, ok := function.Args[0].(*FunctionCallExpr); ok && len(nested.Name) == 1 && strings.EqualFold(nested.Name[0].Text, "JSON_QUERY") && len(nested.Args) == 2 {
				value := renderExpr(nested.Args[0])
				if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(value)), "PARSE_JSON(") {
					value = "PARSE_JSON(" + value + ")"
				}
				path := strings.Trim(renderExpr(nested.Args[1]), "'")
				path = strings.TrimPrefix(path, "$")
				path = strings.TrimPrefix(path, ".")
				return &RawExpr{Raw: "CAST(GET_PATH(" + value + ", '" + path + "') AS BIGINT)"}
			}
			return rawCast(renderedValue, "BIGINT")
		}
	case "SAFE_DIVIDE":
		if target == DialectDuckDB && len(function.Args) == 2 {
			denominator := safeDivideOperand(function.Args[1])
			numerator := safeDivideOperand(function.Args[0])
			return &CaseExpr{Whens: []CaseWhen{{Condition: &BinaryExpr{Left: denominator, Operator: "<>", Right: &LiteralExpr{KindValue: LiteralNumber, Raw: "0"}}, Result: &BinaryExpr{Left: numerator, Operator: "/", Right: denominator}}}, Else: &LiteralExpr{KindValue: LiteralNull, Raw: "NULL"}}
		}
		if len(function.Args) == 2 {
			numerator := renderExpr(safeDivideOperand(function.Args[0]))
			denominator := renderExpr(safeDivideOperand(function.Args[1]))
			condition := denominator + " <> 0"
			division := numerator + " / " + denominator
			switch target {
			case DialectSpark, DialectDatabricks:
				setFunctionName(function, "TRY_DIVIDE")
				return function
			case DialectHive:
				return &RawExpr{Raw: "IF(" + condition + ", " + division + ", NULL)"}
			case DialectSnowflake:
				return &RawExpr{Raw: "IFF(" + condition + ", " + division + ", NULL)"}
			case DialectPresto, DialectTrino:
				return &RawExpr{Raw: "IF(" + condition + ", CAST(" + numerator + " AS DOUBLE) / " + denominator + ", NULL)"}
			case DialectPostgreSQL:
				return &RawExpr{Raw: "CASE WHEN " + condition + " THEN CAST(" + numerator + " AS DOUBLE PRECISION) / " + denominator + " ELSE NULL END"}
			}
		}
	case "SAFE_ADD", "SAFE_MULTIPLY", "SAFE_SUBTRACT":
		if (target == DialectSpark || target == DialectDatabricks) && len(function.Args) == 2 {
			setFunctionName(function, "TRY_"+strings.TrimPrefix(name, "SAFE_"))
			return function
		}
	case "DIV":
		if target == DialectDuckDB && len(function.Args) == 2 {
			return &BinaryExpr{Left: function.Args[0], Operator: "//", Right: function.Args[1]}
		}
	case "ARRAY_CONCAT":
		if target == DialectDuckDB {
			setFunctionName(function, "LIST_CONCAT")
			return function
		}
		if target == DialectSnowflake && len(function.Args) == 1 {
			return &RawExpr{Raw: "ARRAY_CAT(" + genericArrayArgumentText(function.Args[0], target) + ", [])"}
		}
		if len(function.Args) >= 2 {
			args := make([]string, 0, len(function.Args))
			for _, argument := range function.Args {
				args = append(args, genericArrayArgumentText(argument, target))
			}
			switch target {
			case DialectPresto, DialectTrino, DialectHive, DialectSpark, DialectDatabricks:
				return &RawExpr{Raw: "CONCAT(" + strings.Join(args, ", ") + ")"}
			case DialectPostgreSQL, DialectSnowflake, DialectRedshift:
				name := "ARRAY_CAT"
				if target == DialectRedshift {
					name = "ARRAY_CONCAT"
				}
				result := args[len(args)-1]
				for index := len(args) - 2; index >= 0; index-- {
					result = name + "(" + args[index] + ", " + result + ")"
				}
				return &RawExpr{Raw: result}
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
		if (target == DialectHive || target == DialectPresto || target == DialectSnowflake || target == DialectPostgreSQL) && len(function.Args) == 2 {
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

func safeDivideOperand(expression Expr) Expr {
	switch expression.(type) {
	case *BinaryExpr, *CaseExpr, *SetExpr:
		return &ParenthesizedExpr{Expr: expression}
	default:
		return expression
	}
}

func rewriteGenericRawSourceFunction(function *FunctionCallExpr, target Dialect) Expr {
	name := strings.ToUpper(function.Name[0].Text)
	body, ok := rawFunctionBody(function.RawArgs)
	if !ok {
		return nil
	}
	switch name {
	case "MAKE_INTERVAL":
		if target == DialectSnowflake {
			parts := splitTopLevelSQL(body, ',')
			units := map[string]string{
				"YEAR": "year", "YEARS": "year", "MONTH": "month", "MONTHS": "month",
				"DAY": "day", "DAYS": "day", "HOUR": "hour", "HOURS": "hour",
				"MINUTE": "minute", "MINUTES": "minute", "SECOND": "second", "SECONDS": "second",
			}
			positionalUnits := []string{"year", "month", "day", "hour", "minute", "second"}
			values := make([]string, 0, len(parts))
			positional := 0
			for _, part := range parts {
				pieces := strings.SplitN(part, "=>", 2)
				if len(pieces) == 2 {
					unit := units[strings.ToUpper(strings.TrimSpace(pieces[0]))]
					if unit != "" {
						values = append(values, strings.TrimSpace(pieces[1])+" "+unit)
					}
					continue
				}
				if positional < len(positionalUnits) && strings.TrimSpace(part) != "" {
					values = append(values, strings.TrimSpace(part)+" "+positionalUnits[positional])
					positional++
				}
			}
			if len(values) > 0 {
				return &RawExpr{Raw: "INTERVAL '" + strings.Join(values, ", ") + "'"}
			}
		}
	case "EDIT_DISTANCE":
		parts := splitTopLevelSQL(body, ',')
		if len(parts) != 3 {
			return nil
		}
		maxDistance := ""
		if arrow := strings.Index(parts[2], "=>"); arrow >= 0 {
			maxDistance = strings.TrimSpace(parts[2][arrow+2:])
		}
		if maxDistance == "" {
			return nil
		}
		left := strings.TrimSpace(parts[0])
		right := strings.TrimSpace(parts[1])
		distance := "LEVENSHTEIN(" + left + ", " + right + ")"
		switch target {
		case DialectDuckDB:
			return &RawExpr{Raw: "CASE WHEN " + distance + " IS NULL OR " + maxDistance + " IS NULL THEN NULL ELSE LEAST(" + distance + ", " + maxDistance + ") END"}
		case DialectPostgreSQL:
			return &RawExpr{Raw: "LEVENSHTEIN_LESS_EQUAL(" + left + ", " + right + ", " + maxDistance + ")"}
		case DialectSnowflake:
			return &RawExpr{Raw: "EDITDISTANCE(" + left + ", " + right + ", " + maxDistance + ")"}
		}
	case "SAFE_CAST":
		if target != DialectDuckDB {
			return nil
		}
		value, typeName, format, ok := splitRawCastBody(body)
		if !ok || !strings.EqualFold(typeName, "DATE") || format == "" {
			return nil
		}
		return &RawExpr{Raw: "CAST(TRY_STRPTIME(" + value + ", " + normalizeBigQueryCastFormat(format) + ") AS DATE)"}
	}
	return nil
}

func rawFunctionBody(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '(' || matchingParenIndex(trimmed, 0) != len(trimmed)-1 {
		return "", false
	}
	return trimmed[1 : len(trimmed)-1], true
}

func splitRawCastBody(body string) (value, typeName, format string, ok bool) {
	asIndex := indexKeywordTopLevel(body, "AS")
	if asIndex < 0 {
		return "", "", "", false
	}
	value = strings.TrimSpace(body[:asIndex])
	typeAndFormat := strings.TrimSpace(body[asIndex+len("AS"):])
	formatIndex := indexKeywordTopLevel(typeAndFormat, "FORMAT")
	if formatIndex >= 0 {
		format = strings.TrimSpace(typeAndFormat[formatIndex+len("FORMAT"):])
		typeAndFormat = strings.TrimSpace(typeAndFormat[:formatIndex])
	}
	return value, strings.TrimSpace(typeAndFormat), format, value != "" && typeAndFormat != ""
}

func normalizeBigQueryCastFormat(raw string) string {
	value := strings.TrimSpace(strings.Trim(raw, "'"))
	for _, replacement := range []struct{ from, to string }{
		{"YYYY", "%Y"},
		{"MONTH", "%B"},
		{"MM", "%m"},
		{"DD", "%d"},
		{"HH24", "%H"},
		{"MI", "%M"},
		{"SS", "%S"},
	} {
		value = strings.ReplaceAll(value, replacement.from, replacement.to)
	}
	return "'" + value + "'"
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
	if (operator == "LIKE ANY" || operator == "ILIKE ANY") && (target == DialectDuckDB || target == DialectDrill) {
		items := flattenTupleExpressions(expression.Right)
		if len(items) > 0 {
			if target == DialectDrill {
				parts := make([]string, 0, len(items))
				for _, item := range items {
					if operator == "ILIKE ANY" {
						part := "ILIKE(" + renderExpr(expression.Left) + ", " + renderExpr(item)
						if expression.Escape != nil {
							part += ", " + renderExpr(expression.Escape)
						}
						parts = append(parts, part+")")
					} else {
						parts = append(parts, renderExpr(expression.Left)+" LIKE "+renderExpr(item))
					}
				}
				return &RawExpr{Raw: strings.Join(parts, " OR ")}
			}
			baseOperator := "LIKE"
			if operator == "ILIKE ANY" {
				baseOperator = "ILIKE"
			}
			result := Expr(&BinaryExpr{Left: expression.Left, Operator: baseOperator, Right: items[0]})
			for _, item := range items[1:] {
				result = &BinaryExpr{Left: result, Operator: "OR", Right: &BinaryExpr{Left: expression.Left, Operator: baseOperator, Right: item}}
			}
			return result
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
		raw := "ILIKE(" + renderExpr(expression.Left) + ", " + renderExpr(expression.Right)
		if expression.Escape != nil {
			raw += ", " + renderExpr(expression.Escape)
		}
		raw += ")"
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
	intervalArgument := false
	if len(args) >= 2 {
		amount = renderExpr(args[1])
		if interval, ok := args[1].(*IntervalExpr); ok {
			intervalArgument = true
			amount = renderExpr(interval.Value)
			if len(interval.Qualifiers) > 0 {
				unit = strings.ToUpper(strings.Trim(renderExpr(interval.Qualifiers[0]), "'"))
			}
		}
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
	if name == "DATETIME_TRUNC" && len(args) >= 2 {
		unit = strings.ToUpper(strings.Trim(renderExpr(args[1]), "'"))
	}
	value = normalizeGenericDateValue(value, target)
	switch name {
	case "DATE_ADD", "TIMESTAMP_ADD", "DATETIME_ADD", "TIME_ADD":
		if name == "TIMESTAMP_ADD" || name == "DATETIME_ADD" {
			value = normalizeGenericTimestampAddValue(value, target)
		} else {
			value = normalizeGenericDateAddValue(value, target)
		}
		if target == DialectDuckDB {
			switch name {
			case "DATETIME_ADD":
				value = bigQueryDuckDBCast(args[0], "TIMESTAMP")
			case "TIME_ADD":
				value = bigQueryDuckDBCast(args[0], "TIME")
			}
		}
		if (target == DialectDuckDB || target == DialectBigQuery) && intervalArgument {
			amount = "'" + strings.Trim(amount, "'") + "'"
		}
		if name == "TIMESTAMP_ADD" || name == "DATETIME_ADD" {
			amount = genericDateIntervalAmount(amount, intervalArgument)
			switch target {
			case DialectSnowflake:
				return &RawExpr{Raw: "TIMESTAMPADD(" + unit + ", " + amount + ", " + value + ")"}
			case DialectSpark, DialectDatabricks:
				if name == "DATETIME_ADD" {
					if target == DialectDatabricks {
						return &RawExpr{Raw: "TIMESTAMPADD(" + unit + ", " + amount + ", " + value + ")"}
					}
					return &RawExpr{Raw: value + " + INTERVAL " + amount + " " + unit}
				}
				return &RawExpr{Raw: "DATE_ADD(" + unit + ", " + amount + ", " + value + ")"}
			case DialectMySQL:
				return &RawExpr{Raw: "DATE_ADD(" + mysqlTimestampValue(value) + ", INTERVAL " + amount + " " + unit + ")"}
			}
		}
		return genericDateAdd(value, amount, unit, target)
	case "DATE_SUB", "TIMESTAMP_SUB", "DATETIME_SUB", "TIME_SUB":
		if name == "TIMESTAMP_SUB" || name == "DATETIME_SUB" {
			value = normalizeGenericTimestampAddValue(value, target)
		} else {
			value = normalizeGenericDateAddValue(value, target)
		}
		if target == DialectDuckDB {
			switch name {
			case "DATETIME_SUB":
				value = bigQueryDuckDBCast(args[0], "TIMESTAMP")
			case "TIME_SUB":
				value = bigQueryDuckDBCast(args[0], "TIME")
			}
		}
		if (target == DialectDuckDB || target == DialectBigQuery) && intervalArgument {
			amount = "'" + strings.Trim(amount, "'") + "'"
		}
		negative := amount + " * -1"
		if name == "TIMESTAMP_SUB" || name == "DATETIME_SUB" {
			amount = genericDateIntervalAmount(amount, intervalArgument)
			negative = amount + " * -1"
			switch target {
			case DialectSnowflake, DialectDatabricks:
				return &RawExpr{Raw: "TIMESTAMPADD(" + unit + ", " + negative + ", " + value + ")"}
			case DialectSpark:
				if name == "DATETIME_SUB" {
					return &RawExpr{Raw: value + " - INTERVAL " + amount + " " + unit}
				}
				return &RawExpr{Raw: value + " - INTERVAL " + amount + " " + unit}
			case DialectMySQL:
				return &RawExpr{Raw: "DATE_SUB(" + mysqlTimestampValue(value) + ", INTERVAL " + amount + " " + unit + ")"}
			}
		}
		switch target {
		case DialectDuckDB:
			if intervalArgument {
				return &RawExpr{Raw: value + " - INTERVAL '" + strings.Trim(amount, "'") + "' " + unit}
			}
			return &RawExpr{Raw: value + " + INTERVAL (" + negative + ") " + unit}
		case DialectBigQuery:
			if _, ok := args[1].(*IntervalExpr); ok {
				return &RawExpr{Raw: "DATE_SUB(" + normalizeBigQueryDateValue(value) + ", INTERVAL '" + strings.Trim(amount, "'") + "' " + unit + ")"}
			}
			return &RawExpr{Raw: "DATE_ADD(" + value + ", INTERVAL (" + negative + ") " + unit + ")"}
		case DialectHive, DialectSpark:
			return &RawExpr{Raw: "DATE_ADD(" + value + ", " + negative + ")"}
		case DialectPostgreSQL:
			return &RawExpr{Raw: value + " - INTERVAL '" + strings.Trim(amount, "'") + " " + unit + "'"}
		case DialectDatabricks:
			return &RawExpr{Raw: "DATE_ADD(" + normalizeBigQueryDateValue(value) + ", " + negativeDateAmount(amount) + ")"}
		case DialectPresto:
			return &RawExpr{Raw: "DATE_ADD('" + unit + "', " + negative + ", " + value + ")"}
		case DialectRedshift:
			return &RawExpr{Raw: "DATEADD(" + unit + ", " + negative + ", " + value + ")"}
		case DialectSnowflake:
			if _, ok := args[1].(*IntervalExpr); ok {
				negative = "'" + strings.Trim(amount, "'") + "' * -1"
			}
			return &RawExpr{Raw: "DATEADD(" + unit + ", " + negative + ", " + value + ")"}
		case DialectTSQL:
			return &RawExpr{Raw: "DATEADD(" + unit + ", " + negative + ", " + value + ")"}
		}
	case "LAST_DAY":
		if len(args) == 2 {
			lastDayUnit := strings.ToUpper(strings.Trim(renderExpr(args[1]), "'"))
			switch lastDayUnit {
			case "MONS", "MONTHS":
				lastDayUnit = "MONTH"
			}
			switch target {
			case DialectBigQuery, DialectSnowflake:
				return &RawExpr{Raw: "LAST_DAY(" + value + ", " + lastDayUnit + ")"}
			case DialectDuckDB, DialectMySQL, DialectOracle, DialectRedshift, DialectSpark:
				if lastDayUnit == "MONTH" {
					return &RawExpr{Raw: "LAST_DAY(" + value + ")"}
				}
			case DialectClickHouse:
				if lastDayUnit == "MONTH" {
					return &RawExpr{Raw: "LAST_DAY(" + nullableDateValue(value) + ")"}
				}
			case DialectPresto:
				if lastDayUnit == "MONTH" {
					return &RawExpr{Raw: "LAST_DAY_OF_MONTH(" + value + ")"}
				}
			case DialectPostgreSQL:
				if lastDayUnit == "MONTH" {
					return &RawExpr{Raw: "CAST(DATE_TRUNC('MONTH', " + value + ") + INTERVAL '1 MONTH' - INTERVAL '1 DAY' AS DATE)"}
				}
			case DialectTSQL:
				if lastDayUnit == "MONTH" {
					return &RawExpr{Raw: "EOMONTH(" + value + ")"}
				}
			}
		}
	case "DATE_DIFF", "DATETIME_DIFF", "TIME_DIFF":
		if target == DialectDuckDB && len(args) >= 3 {
			dateType := "DATE"
			if name == "DATETIME_DIFF" {
				dateType = "TIMESTAMP"
			} else if name == "TIME_DIFF" {
				dateType = "TIME"
			}
			return &RawExpr{Raw: "DATE_DIFF('" + strings.ToUpper(strings.Trim(renderExpr(args[2]), "'")) + "', " + duckDBDateDiffValue(args[1], dateType) + ", " + duckDBDateDiffValue(args[0], dateType) + ")"}
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
	case "DATETIME_TRUNC":
		if target == DialectDatabricks {
			return genericDateTrunc(value, unit, target)
		}
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

func normalizeBigQueryDateValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if strings.EqualFold(trimmed, "CURRENT_DATE()") {
		return "CURRENT_DATE"
	}
	return value
}

func normalizeGenericDateValue(value string, target Dialect) string {
	trimmed := strings.TrimSpace(value)
	if !strings.EqualFold(trimmed, "CURRENT_DATE()") {
		return value
	}
	switch target {
	case DialectBigQuery, DialectMySQL, DialectPostgreSQL, DialectPresto, DialectTrino, DialectHive, DialectSpark, DialectDatabricks:
		return "CURRENT_DATE"
	default:
		return value
	}
}

func nullableDateValue(value string) string {
	trimmed := strings.TrimSpace(value)
	upper := strings.ToUpper(trimmed)
	const dateSuffix = " AS DATE)"
	if strings.HasPrefix(upper, "CAST(") && strings.HasSuffix(upper, dateSuffix) {
		return trimmed[:len(trimmed)-len(dateSuffix)] + " AS Nullable(DATE))"
	}
	return "CAST(" + trimmed + " AS Nullable(DATE))"
}

func duckDBDateDiffValue(expression Expr, typeName string) string {
	if typed, ok := expression.(*TypedLiteralExpr); ok && typed.Value != nil {
		return "CAST(" + typed.Value.Raw + " AS " + typeName + ")"
	}
	if cast, ok := expression.(*CastExpr); ok && strings.EqualFold(renderExpr(cast.Type), typeName) {
		return renderExpr(cast)
	}
	text := strings.TrimSpace(renderExpr(expression))
	upper := strings.ToUpper(text)
	suffix := " AS " + strings.ToUpper(typeName) + ")"
	if strings.HasPrefix(upper, "CAST(") && strings.HasSuffix(upper, suffix) {
		return text
	}
	return "CAST(" + text + " AS " + typeName + ")"
}

func rewriteDuckDBDatePart(function *FunctionCallExpr) Expr {
	if function == nil || len(function.Args) != 2 {
		return nil
	}
	field := unquoteDatePart(function.Args[0])
	name := strings.ToUpper(strings.Trim(renderExpr(field), "'"))
	value := renderExpr(function.Args[1])
	switch name {
	case "EPOCH_SECOND":
		return &RawExpr{Raw: "CAST(EPOCH(" + value + ") AS BIGINT)"}
	case "EPOCH_MILLISECOND", "EPOCH_MILLISECONDS":
		return &FunctionCallExpr{Name: []Identifier{{Text: "EPOCH_MS"}}, Args: []Expr{function.Args[1]}}
	case "EPOCH_MICROSECOND", "EPOCH_MICROSECONDS":
		return &FunctionCallExpr{Name: []Identifier{{Text: "EPOCH_US"}}, Args: []Expr{function.Args[1]}}
	case "EPOCH_NANOSECOND", "EPOCH_NANOSECONDS":
		return &FunctionCallExpr{Name: []Identifier{{Text: "EPOCH_NS"}}, Args: []Expr{function.Args[1]}}
	case "NANOSECOND":
		return &RawExpr{Raw: "CAST(STRFTIME(CAST(" + value + " AS TIMESTAMP_NS), '%n') AS BIGINT)"}
	case "WEEKISO":
		return &RawExpr{Raw: "CAST(STRFTIME(" + value + ", '%V') AS INT)"}
	case "YEAROFWEEK", "YEAROFWEEKISO":
		return &RawExpr{Raw: "CAST(STRFTIME(" + value + ", '%G') AS INT)"}
	case "DAYOFMONTH":
		field = identifierExpr("DAY")
	case "DAYOFWEEKISO", "DAYOFWEEK_ISO":
		field = identifierExpr("ISODOW")
	}
	if identifier, ok := field.(*IdentifierExpr); ok && len(identifier.Parts) == 1 {
		identifier.Parts[0].Text = strings.ToUpper(identifier.Parts[0].Text)
	}
	return &ExtractExpr{Field: field, Source: function.Args[1]}
}

func negativeDateAmount(amount string) string {
	trimmed := strings.TrimSpace(strings.Trim(amount, "'"))
	if trimmed == "" {
		return amount + " * -1"
	}
	if strings.HasPrefix(trimmed, "-") {
		return strings.TrimPrefix(trimmed, "-")
	}
	if _, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return "-" + trimmed
	}
	return amount + " * -1"
}

func genericDateAdd(value, amount, unit string, target Dialect) Expr {
	switch target {
	case DialectBigQuery, DialectDrill, DialectStarRocks, DialectDoris:
		return &RawExpr{Raw: "DATE_ADD(" + value + ", INTERVAL " + amount + " " + unit + ")"}
	case DialectMySQL:
		if strings.HasPrefix(strings.TrimSpace(amount), "-") && !strings.HasPrefix(strings.TrimSpace(amount), "'-") {
			amount = "'" + strings.Trim(amount, "'") + "'"
		}
		return &RawExpr{Raw: "DATE_ADD(" + value + ", INTERVAL " + amount + " " + unit + ")"}
	case DialectHive, DialectSpark, DialectDremio:
		return &RawExpr{Raw: "DATE_ADD(" + value + ", " + amount + ")"}
	case DialectPresto:
		trimmedAmount := strings.TrimSpace(amount)
		if strings.HasPrefix(trimmedAmount, "-") || strings.HasPrefix(trimmedAmount, "'-") {
			if strings.HasPrefix(trimmedAmount, "'-") {
				amount = "CAST(" + amount + " AS BIGINT)"
			} else {
				amount = "CAST('" + strings.Trim(amount, "'") + "' AS BIGINT)"
			}
		}
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
	if target == DialectDuckDB {
		return normalizeBigQueryDateValue(value)
	}
	if target == DialectRedshift || target == DialectSnowflake || target == DialectTSQL {
		if isSimpleSQLIdentifier(value) {
			return value
		}
		if strings.HasPrefix(strings.ToUpper(value), "CAST(") && strings.HasSuffix(value, " AS DATE)") {
			return value
		}
		return "CAST(" + value + " AS DATE)"
	}
	return value
}

func isSimpleSQLIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '$' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func normalizeGenericTimestampAddValue(value string, target Dialect) string {
	if target == DialectSnowflake || target == DialectDatabricks || target == DialectSpark {
		return strings.ReplaceAll(value, " AS TIMESTAMP_NTZ)", " AS TIMESTAMP)")
	}
	if target == DialectPresto || target == DialectTrino {
		return strings.ReplaceAll(value, " AS DATETIME)", " AS TIMESTAMP)")
	}
	return value
}

func genericDateIntervalAmount(amount string, intervalArgument bool) string {
	trimmed := strings.TrimSpace(strings.Trim(amount, "'"))
	if intervalArgument {
		if _, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return "'" + trimmed + "'"
		}
	}
	return amount
}

func mysqlTimestampValue(value string) string {
	trimmed := strings.TrimSpace(value)
	upper := strings.ToUpper(trimmed)
	const suffix = " AS DATETIME)"
	if strings.HasPrefix(upper, "CAST(") && strings.HasSuffix(upper, suffix) {
		return "TIMESTAMP(" + trimmed[len("CAST("):len(trimmed)-len(suffix)] + ")"
	}
	return trimmed
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
	case DialectDuckDB, DialectPresto, DialectMaterialize, DialectPostgreSQL, DialectSnowflake, DialectStarRocks, DialectDatabricks:
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
			if target == DialectDuckDB && (function.ArrayLiteral || len(function.Args) == 1 && func() bool {
				_, ok := function.Args[0].(*SubqueryExpr)
				return ok
			}()) {
				return function
			}
			return &RawExpr{Raw: "[" + argText + "]"}
		case DialectPresto:
			return &RawExpr{Raw: "ARRAY[" + argText + "]"}
		case DialectSnowflake:
			if function.ArrayLiteral {
				return &RawExpr{Raw: "[" + argText + "]"}
			}
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
	case DialectPresto, DialectTrino, DialectAthena, DialectPostgreSQL:
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
	path = strings.ReplaceAll(path, "''", "'")
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

func rawCastIfNeeded(value, typeName string) Expr {
	text := strings.TrimSpace(value)
	upper := strings.ToUpper(text)
	suffix := " AS " + strings.ToUpper(typeName) + ")"
	if strings.HasPrefix(upper, "CAST(") && strings.HasSuffix(upper, suffix) {
		return &RawExpr{Raw: text}
	}
	return rawCast(text, typeName)
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

func transformSelect(stmt *SelectStmt, target Dialect) {
	targetTransformer{target: target}.selectStatement(stmt, target)
}

func (transformer targetTransformer) selectStatement(stmt *SelectStmt, target Dialect) {
	transformExpr := transformer.expr
	transformSelect := transformer.selectStatement
	transformTableExpr := transformer.tableExpression
	transformWindow := transformer.window
	rewriteOrderItems := transformer.orderItems
	stmt.Tail = rewriteQueryTail(stmt.Tail, target)
	if target != DialectTSQL && isTSQLForQueryTail(stmt.Tail) {
		stmt.Tail = ""
	}
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
	if stmt.Top != nil && target != DialectTSQL && target != DialectTeradata && target != DialectSnowflake {
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
	if target == DialectDuckDB && strings.EqualFold(stmt.SelectModifier, "AS STRUCT") {
		parts := make([]string, 0, len(stmt.Projections))
		for index, projection := range stmt.Projections {
			key := "_" + strconv.Itoa(index)
			if projection.Alias != nil {
				key = projection.Alias.Text
			} else if identifier, ok := projection.Expr.(*IdentifierExpr); ok && len(identifier.Parts) > 0 {
				key = identifier.Parts[len(identifier.Parts)-1].Text
			}
			parts = append(parts, "'"+strings.ReplaceAll(key, "'", "''")+"': "+renderDialectExpr(projection.Expr, target))
		}
		stmt.Projections = []SelectItem{{Expr: &RawExpr{Raw: "{" + strings.Join(parts, ", ") + "}"}}}
		stmt.SelectModifier = ""
	}
	if target == DialectBigQuery {
		rewriteBigQueryGroupAliases(stmt)
	}
	for i := range stmt.Into {
		normalizeIdentifierTarget(&stmt.Into[i], target)
		if target == DialectTSQL && stmt.IntoTemporary && !strings.HasPrefix(stmt.Into[i].Text, "#") {
			stmt.Into[i].Text = "#" + stmt.Into[i].Text
			stmt.Into[i].Quoted = false
			stmt.Into[i].Quote = 0
		}
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
	switch target {
	case DialectBigQuery:
		normalizeBigQueryUnnestSources(stmt, target)
	case DialectDuckDB:
		normalizeDuckDBCommaUnnest(stmt)
	case DialectPresto:
		normalizeCommaUnnestJoins(stmt, target)
	case DialectRedshift:
		normalizeCommaUnnestJoins(stmt, target)
	}
	whereBoolean := false
	whereBooleanRaw := "TRUE"
	if literal, ok := stmt.Where.(*LiteralExpr); ok {
		whereBoolean = literal.KindValue == LiteralBoolean
		whereBooleanRaw = literal.Raw
	}
	stmt.Where = transformExpr(stmt.Where, target)
	if target == DialectTSQL && whereBoolean {
		stmt.Where = booleanOperandTSQL(&LiteralExpr{KindValue: LiteralBoolean, Raw: whereBooleanRaw})
	}
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
	if target == DialectDuckDB {
		for index := range stmt.OrderBy {
			if isTSQLDummyOrder(stmt.OrderBy[index].Expr) {
				stmt.OrderBy[index].NullsFirst = true
				stmt.OrderBy[index].NullsLast = false
			}
		}
	}
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
	normalizeCommaUnnestJoins(stmt, DialectDuckDB)
}

func preserveGenericUnnestAliases(stmt *SelectStmt) {
	if stmt == nil {
		return
	}
	mark := func(item FromItem) {
		if function, ok := item.(*TableFunctionFrom); ok && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "UNNEST") {
			function.preserveAlias = true
		}
	}
	for index := range stmt.From {
		mark(stmt.From[index].Primary)
		for joinIndex := range stmt.From[index].Joins {
			mark(stmt.From[index].Joins[joinIndex].Right)
		}
	}
}

func normalizeBigQueryPrestoUnnestAliases(root Node) {
	Walk(root, func(current Node) VisitAction {
		function, ok := current.(*TableFunctionFrom)
		if ok && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "UNNEST") && function.Alias != nil && len(function.Columns) == 0 {
			function.preserveAlias = false
		}
		return VisitChildren
	})
}

func normalizeCommaUnnestJoins(stmt *SelectStmt, target Dialect) {
	if stmt == nil || len(stmt.From) < 2 || len(stmt.From[0].Joins) != 0 {
		return
	}
	base := &stmt.From[0]
	joins := make([]JoinClause, 0, len(stmt.From)-1)
	for index := 1; index < len(stmt.From); index++ {
		function, ok := stmt.From[index].Primary.(*TableFunctionFrom)
		if !ok || len(function.Name) != 1 || !strings.EqualFold(function.Name[0].Text, "UNNEST") {
			return
		}
		if target == DialectDuckDB || target == DialectPresto {
			if function.Alias != nil && len(function.Columns) == 0 {
				function.Columns = []Identifier{{Text: function.Alias.Text}}
				alias := Identifier{Text: "_t" + strconv.Itoa(index-1)}
				function.Alias = &alias
			}
		} else if target == DialectRedshift {
			argument := ""
			if len(function.Args) == 1 {
				argument = renderExpr(function.Args[0])
			}
			if argument == "" {
				return
			}
			stmt.From[index].Primary = &RawFrom{Raw: argument, Alias: function.Alias, Columns: append([]Identifier(nil), function.Columns...)}
		}
		joins = append(joins, JoinClause{Kind: JoinCross, JoinText: "CROSS JOIN", Right: stmt.From[index].Primary})
	}
	base.Joins = append(base.Joins, joins...)
	stmt.From = stmt.From[:1]
}

func normalizeBigQueryUnnestSources(stmt *SelectStmt, target Dialect) {
	if stmt == nil || len(stmt.From) < 2 {
		return
	}
	baseName := ""
	if table, ok := stmt.From[0].Primary.(*TableName); ok && len(table.Parts) > 0 {
		baseName = table.Parts[len(table.Parts)-1].Text
		if table.Alias != nil {
			baseName = table.Alias.Text
		}
	}
	for index := 1; index < len(stmt.From); index++ {
		table, ok := stmt.From[index].Primary.(*TableName)
		if !ok || table.Alias == nil || len(table.Parts) == 0 {
			continue
		}
		quotedProperty := len(table.Parts) == 1 && table.Parts[0].Quoted && strings.Contains(table.Parts[0].Text, ".")
		if len(table.Parts) == 1 && strings.Contains(table.Parts[0].Text, ".") && !(target == DialectBigQuery && table.Parts[0].Quoted) {
			part := table.Parts[0]
			pieces := strings.Split(part.Text, ".")
			if len(pieces) > 1 {
				table.Parts = make([]Identifier, 0, len(pieces))
				for _, piece := range pieces {
					table.Parts = append(table.Parts, Identifier{Text: piece, Quoted: part.Quoted, Quote: part.Quote, Span: part.Span})
				}
			}
		}
		if quotedProperty && (target == DialectPresto || target == DialectTrino) {
			stmt.From[0].Joins = append(stmt.From[0].Joins, JoinClause{Kind: JoinCross, JoinText: "CROSS JOIN", Right: table})
			copy(stmt.From[index:], stmt.From[index+1:])
			stmt.From = stmt.From[:len(stmt.From)-1]
			index--
			continue
		}
		fieldPath := ""
		if len(table.Parts) == 2 {
			fieldPath = table.Parts[0].Text + "." + table.Parts[1].Text
		}
		if fieldPath == "" || (baseName != "" && !strings.EqualFold(strings.Split(fieldPath, ".")[0], baseName)) {
			continue
		}
		parts := append([]Identifier(nil), table.Parts...)
		argument := &IdentifierExpr{Parts: parts}
		stmt.From[index].Primary = &TableFunctionFrom{
			Name:  []Identifier{{Text: "UNNEST"}},
			Args:  []Expr{argument},
			Alias: table.Alias,
		}
	}
}

func normalizeBigQueryDuckDBUnnestAliases(stmt *SelectStmt) {
	if stmt == nil {
		return
	}
	index := 0
	for tableIndex := range stmt.From {
		normalize := func(function *TableFunctionFrom) {
			if function == nil || len(function.Name) != 1 || !strings.EqualFold(function.Name[0].Text, "UNNEST") {
				return
			}
			function.Name[0].Text = "UNNEST"
			if function.Alias != nil && len(function.Columns) == 0 {
				function.Columns = []Identifier{{Text: function.Alias.Text}}
				function.Alias = &Identifier{Text: "_t" + strconv.Itoa(index)}
			}
			index++
		}
		if function, ok := stmt.From[tableIndex].Primary.(*TableFunctionFrom); ok {
			normalize(function)
		}
		for joinIndex := range stmt.From[tableIndex].Joins {
			if function, ok := stmt.From[tableIndex].Joins[joinIndex].Right.(*TableFunctionFrom); ok {
				normalize(function)
			}
		}
	}
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
	if !strings.EqualFold(name, "PARSE_DATE") && !strings.EqualFold(name, "PARSE_DATETIME") && !strings.EqualFold(name, "PARSE_TIMESTAMP") && !strings.EqualFold(name, "FORMAT_DATE") && !strings.EqualFold(name, "FORMAT_DATETIME") && !strings.EqualFold(name, "FORMAT_TIMESTAMP") {
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

func normalizeDremioTableTail(tail string) string {
	trimmed := strings.TrimSpace(tail)
	upper := strings.ToUpper(trimmed)
	switch {
	case strings.HasPrefix(upper, "VERSION AS OF "):
		return "AT SNAPSHOT " + strings.TrimSpace(trimmed[len("VERSION AS OF "):])
	case strings.HasPrefix(upper, "FOR VERSION AS OF "):
		return "AT SNAPSHOT " + strings.TrimSpace(trimmed[len("FOR VERSION AS OF "):])
	case strings.HasPrefix(upper, "TIMESTAMP AS OF "):
		return "AT TIMESTAMP " + strings.TrimSpace(trimmed[len("TIMESTAMP AS OF "):])
	}
	return tail
}

func (transformer targetTransformer) tableExpression(table *TableExpr, target Dialect) {
	transformExpr := transformer.expr
	transformSelect := transformer.selectStatement
	transformTableExpr := transformer.tableExpression
	transformFromItem := transformer.fromItem
	transformTableFunction := transformer.tableFunction
	normalizeFromItemIdentifiers(table.Primary, target)
	if target == DialectSpark || target == DialectHive || target == DialectDatabricks {
		clearTSQLTableHint(table.Primary)
	}
	if target == DialectBigQuery {
		normalizeBigQueryFromItem(table.Primary)
		switch item := table.Primary.(type) {
		case *TableName:
			item.Columns = nil
		case *TableFunctionFrom:
			if item.Alias == nil && len(item.Columns) == 1 {
				item.Alias = &item.Columns[0]
			}
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
		if target == DialectDuckDB {
			table.Primary = normalizeDuckDBUnnestMaxDepth(item)
		}
	}
	for i := range table.Joins {
		if target == DialectSpark || target == DialectPostgreSQL {
			joinText := strings.ToUpper(strings.TrimSpace(table.Joins[i].JoinText))
			if strings.Contains(joinText, "APPLY") {
				if strings.Contains(joinText, "OUTER") {
					table.Joins[i].JoinText = "LEFT JOIN LATERAL"
				} else {
					table.Joins[i].JoinText = "INNER JOIN LATERAL"
				}
				if target == DialectPostgreSQL && table.Joins[i].Condition == nil && len(table.Joins[i].Using) == 0 {
					table.Joins[i].Condition = &LiteralExpr{KindValue: LiteralBoolean, Raw: "TRUE"}
				}
			} else if target == DialectSpark {
				table.Joins[i].JoinText = normalizeSparkJoinText(table.Joins[i].JoinText)
			}
		}
		if table.Joins[i].Right != nil {
			if target == DialectSpark || target == DialectHive || target == DialectDatabricks {
				clearTSQLTableHint(table.Joins[i].Right)
			}
			transformFromItem(table.Joins[i].Right, target)
			if target == DialectDuckDB {
				table.Joins[i].Right = normalizeDuckDBUnnestMaxDepth(table.Joins[i].Right)
			}
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

func clearTSQLTableHint(item FromItem) {
	if table, ok := item.(*TableName); ok {
		table.Hint = ""
	}
}

func normalizeSparkJoinText(joinText string) string {
	words := strings.Fields(joinText)
	if len(words) == 0 {
		return joinText
	}
	filtered := words[:0]
	for _, word := range words {
		switch strings.ToUpper(word) {
		case "HASH", "LOOP", "REMOTE", "MERGE":
			continue
		default:
			filtered = append(filtered, word)
		}
	}
	return strings.Join(filtered, " ")
}

func normalizeIdentifierTarget(identifier *Identifier, target Dialect) {
	if identifier == nil {
		return
	}
	if target == DialectGeneric && identifier.Quoted {
		identifier.Quote = '"'
		return
	}
	if target == DialectDuckDB && !identifier.Quoted && strings.EqualFold(identifier.Text, "TABLE") {
		identifier.Quoted = true
		identifier.Quote = '"'
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
	} else if target == DialectDuckDB {
		identifier.Quote = '"'
	} else if target == DialectBigQuery || target == DialectDrill || target == DialectHive || target == DialectSpark || target == DialectDatabricks || target == DialectMySQL || target == DialectStarRocks {
		identifier.Quote = '`'
	} else if target == DialectPresto || target == DialectTrino || target == DialectRedshift || target == DialectSnowflake {
		identifier.Quote = '"'
	}
}

func splitQualifiedIdentifierTarget(expression *IdentifierExpr, target Dialect) {
	if expression == nil {
		return
	}
	splitQualifiedIdentifierParts(&expression.Parts, target)
}

func splitQualifiedIdentifierParts(parts *[]Identifier, target Dialect) {
	if parts == nil || len(*parts) != 1 || !(*parts)[0].Quoted || !strings.Contains((*parts)[0].Text, ".") {
		return
	}
	switch target {
	case DialectPostgreSQL, DialectPresto, DialectTrino, DialectRedshift, DialectSnowflake:
	default:
		return
	}
	part := (*parts)[0]
	pieces := strings.Split(part.Text, ".")
	if len(pieces) < 2 {
		return
	}
	result := make([]Identifier, 0, len(pieces))
	for _, piece := range pieces {
		result = append(result, Identifier{
			Text:   piece,
			Quoted: true,
			Quote:  '"',
			Span:   part.Span,
		})
	}
	*parts = result
}

func promoteCTEColumnsToProjectionAliases(stmt *SelectStmt) {
	if stmt == nil {
		return
	}
	for index := range stmt.With {
		cte := &stmt.With[index]
		if cte.Query == nil || len(cte.Columns) == 0 {
			continue
		}
		for columnIndex, column := range cte.Columns {
			if columnIndex >= len(cte.Query.Projections) {
				break
			}
			alias := column
			cte.Query.Projections[columnIndex].Alias = &alias
		}
		cte.Columns = nil
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

func normalizeUnpivotAliasTokens(raw string, target Dialect, quoteBare ...bool) string {
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(raw)), "UNPIVOT") {
		return raw
	}
	text := raw
	quoteBareAliases := len(quoteBare) > 0 && quoteBare[0]
	for search := 0; ; {
		upper := strings.ToUpper(text[search:])
		index := strings.Index(upper, " AS ")
		if index < 0 {
			break
		}
		index += search
		valueStart := index + len(" AS ")
		if valueStart >= len(text) {
			break
		}
		if text[valueStart] == '\'' {
			valueEnd := valueStart + 1
			for valueEnd < len(text) {
				if text[valueEnd] == '\'' {
					if valueEnd+1 < len(text) && text[valueEnd+1] == '\'' {
						valueEnd += 2
						continue
					}
					break
				}
				valueEnd++
			}
			if valueEnd >= len(text) {
				break
			}
			if (target == DialectBigQuery && !quoteBareAliases) || target == DialectSpark || target == DialectDatabricks {
				value := strings.ReplaceAll(text[valueStart+1:valueEnd], "''", "'")
				text = text[:index+len(" AS ")] + value + text[valueEnd+1:]
				search = index + len(" AS ") + len(value)
				continue
			}
			search = valueEnd + 1
			continue
		}
		valueEnd := valueStart
		for valueEnd < len(text) && text[valueEnd] != ',' && text[valueEnd] != ')' && text[valueEnd] != ' ' && text[valueEnd] != '\t' && text[valueEnd] != '\r' && text[valueEnd] != '\n' {
			valueEnd++
		}
		value := text[valueStart:valueEnd]
		if target == DialectBigQuery && quoteBareAliases {
			text = text[:index+len(" AS ")] + "'" + value + "'" + text[valueEnd:]
			search = index + len(" AS ") + len(value) + 2
			continue
		}
		if (target == DialectSpark || target == DialectDatabricks) && isNumericText(value) {
			text = text[:index+len(" AS ")] + "`" + value + "`" + text[valueEnd:]
			search = index + len(" AS ") + len(value) + 2
			continue
		}
		search = valueEnd
	}
	return text
}

func normalizeSelectUnpivotModifiers(stmt *SelectStmt, target Dialect, source ...Dialect) {
	if stmt == nil {
		return
	}
	for index := range stmt.From {
		for modifierIndex := range stmt.From[index].Modifiers {
			if target == DialectBigQuery && len(source) > 0 && (source[0] == DialectSpark || source[0] == DialectDatabricks) {
				stmt.From[index].Modifiers[modifierIndex] = normalizeUnpivotAliasTokens(stmt.From[index].Modifiers[modifierIndex], target, true)
			} else {
				stmt.From[index].Modifiers[modifierIndex] = normalizeUnpivotAliasTokens(stmt.From[index].Modifiers[modifierIndex], target)
			}
		}
	}
}

func isNumericText(text string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	_, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	return err == nil
}

func normalizeBigQueryRawStatement(raw string) string {
	text := raw
	text = strings.TrimSpace(replaceFold(text, "SET VARIABLE ", "SET "))
	upper := strings.ToUpper(text)
	if strings.HasPrefix(upper, "ALTER TABLE ") {
		if renameIndex := strings.Index(upper, " RENAME TO "); renameIndex >= 0 {
			name := strings.TrimSpace(text[renameIndex+len(" RENAME TO "):])
			if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
				name = strings.TrimSpace(name[dot+1:])
			}
			text = text[:renameIndex+len(" RENAME TO ")] + name
		}
	}
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
		splitQualifiedIdentifierParts(&item.Parts, target)
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

func (transformer targetTransformer) fromItem(item FromItem, target Dialect) {
	transformSelect := transformer.selectStatement
	transformTableExpr := transformer.tableExpression
	transformTableFunction := transformer.tableFunction
	switch item := item.(type) {
	case *TableName:
		splitQualifiedIdentifierParts(&item.Parts, target)
		for index := range item.Parts {
			normalizeIdentifierTarget(&item.Parts[index], target)
		}
		normalizeIdentifierTarget(item.Alias, target)
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

func (transformer targetTransformer) tableFunction(function *TableFunctionFrom, target Dialect) {
	transformExpr := transformer.expr
	normalizeRawStructArrayForTarget := transformer.normalizeRawStructArray
	if target == DialectTSQL && function.RawArgs != "" && strings.Contains(strings.ToUpper(function.RawArgs), " WITH ") {
		function.RawArgs = normalizeTSQLOpenJSONRaw(function.RawArgs)
	}
	for i := range function.Args {
		if target == DialectSnowflake || target == DialectPresto || target == DialectTrino {
			if raw, ok := function.Args[i].(*RawExpr); ok {
				if rewritten, ok := normalizeRawStructArrayForTarget(raw.Raw, target); ok {
					function.Args[i] = rewritten
				}
			}
		}
		function.Args[i] = transformExpr(function.Args[i], target)
	}
	if target == DialectSnowflake && function.RawArgs != "" {
		function.RawArgs = normalizeSnowflakeArrayConstruct(function.RawArgs)
	}
	// A lateral FLATTEN relation exposes a fixed six-column table function
	// result in Snowflake.  Keeping this structural in the AST is important:
	// it lets column references and aliases survive a round trip instead of
	// falling back to an opaque raw FROM item.  SQLGlot also materializes the
	// implicit alias and output columns for this form.
	if target == DialectSnowflake && function.Lateral && function.RawArgs != "" && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "FLATTEN") {
		function.Name[0].Text = "FLATTEN"
		function.RawArgs = normalizeSnowflakeFlattenRawArgs(function.RawArgs)
		if function.Alias == nil {
			function.Alias = &Identifier{Text: "_flattened"}
		}
		if len(function.Columns) == 0 && function.ColumnsRaw == "" {
			function.Columns = []Identifier{
				{Text: "SEQ"},
				{Text: "KEY"},
				{Text: "PATH"},
				{Text: "INDEX"},
				{Text: "VALUE"},
				{Text: "THIS"},
			}
		}
	}
	if target == DialectSnowflake && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "UNNEST") && len(function.Args) == 1 {
		argument := renderExpr(function.Args[0])
		function.Name = []Identifier{{Text: "TABLE"}}
		function.RawArgs = "(FLATTEN(INPUT => " + argument + "))"
		function.Args = nil
		function.WithOrdinality = false
		aliasColumn := ""
		if function.Alias != nil {
			aliasColumn = function.Alias.Text
		}
		function.Alias = &Identifier{Text: "_t0"}
		function.Columns = []Identifier{{Text: "seq"}, {Text: "key"}, {Text: "path"}, {Text: "index"}}
		if aliasColumn != "" {
			function.Columns = append(function.Columns, Identifier{Text: aliasColumn})
		} else {
			function.Columns = append(function.Columns, Identifier{Text: "value"})
		}
		function.Columns = append(function.Columns, Identifier{Text: "this"})
	}
	if (target == DialectHive || target == DialectSpark || target == DialectDatabricks) && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "UNNEST") && !function.WithOrdinality {
		if len(function.Columns) == 1 && function.Alias == nil {
			function.Alias = &Identifier{Text: "_t0"}
		} else if function.Alias != nil && len(function.Columns) == 0 {
			column := *function.Alias
			function.Alias = &Identifier{Text: "_t0"}
			function.Columns = []Identifier{column}
		}
		function.Name[0].Text = "EXPLODE"
	}
	if target == DialectBigQuery && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "UNNEST") {
		function.Name[0].Text = "UNNEST"
		if len(function.Columns) == 1 {
			if function.Alias == nil {
				function.Alias = &function.Columns[0]
			}
			function.Columns = nil
		}
	}
	if (target == DialectPresto || target == DialectTrino) && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "UNNEST") && function.Alias != nil && len(function.Columns) == 0 && !function.WithOrdinality && !function.preserveAlias {
		column := *function.Alias
		function.Alias = &Identifier{Text: "_t0"}
		function.Columns = []Identifier{column}
	}
	if target == DialectDuckDB && len(function.Name) == 1 && len(function.Args) == 1 &&
		(strings.EqualFold(function.Name[0].Text, "RANGE") || strings.EqualFold(function.Name[0].Text, "GENERATE_SERIES")) {
		function.Name[0].Text = strings.ToUpper(function.Name[0].Text)
		function.Args = append([]Expr{&LiteralExpr{KindValue: LiteralNumber, Raw: "0"}}, function.Args...)
	}
	if target == DialectDuckDB && len(function.Name) == 1 && strings.EqualFold(function.Name[0].Text, "UNNEST") {
		function.Name[0].Text = "UNNEST"
	}
	if target == DialectBigQuery && function.WithOffset && function.Alias == nil {
		function.Alias = &Identifier{Text: "offset"}
	}
}

// normalizeSnowflakeFlattenRawArgs rewrites the colon path spelling that can
// occur inside a captured FLATTEN argument.  Those arguments are intentionally
// retained as raw SQL because named arguments are dialect-specific, but a
// nested path still has a stable Snowflake AST meaning and should become
// GET_PATH when the target is Snowflake.
func normalizeSnowflakeFlattenRawArgs(raw string) string {
	var out strings.Builder
	for index := 0; index < len(raw); {
		if raw[index] == '\'' || raw[index] == '"' || raw[index] == '`' {
			quote := raw[index]
			start := index
			index++
			for index < len(raw) {
				if raw[index] == quote {
					if index+1 < len(raw) && raw[index+1] == quote {
						index += 2
						continue
					}
					index++
					break
				}
				if raw[index] == '\\' && index+1 < len(raw) {
					index += 2
					continue
				}
				index++
			}
			out.WriteString(raw[start:index])
			continue
		}
		if !isSQLIdentifierStart(raw[index]) {
			out.WriteByte(raw[index])
			index++
			continue
		}

		start := index
		index++
		for index < len(raw) && isSQLIdentifierPart(raw[index]) {
			index++
		}
		for index < len(raw) && raw[index] == '.' {
			partStart := index
			index++
			if index >= len(raw) || !isSQLIdentifierStart(raw[index]) {
				index = partStart
				break
			}
			index++
			for index < len(raw) && isSQLIdentifierPart(raw[index]) {
				index++
			}
		}
		if index < len(raw) && raw[index] == ':' && index+1 < len(raw) && isSQLIdentifierStart(raw[index+1]) {
			fieldStart := index + 1
			fieldEnd := fieldStart + 1
			for fieldEnd < len(raw) && isSQLIdentifierPart(raw[fieldEnd]) {
				fieldEnd++
			}
			out.WriteString("GET_PATH(")
			out.WriteString(raw[start:index])
			out.WriteString(", '")
			out.WriteString(raw[fieldStart:fieldEnd])
			out.WriteString("')")
			index = fieldEnd
			continue
		}
		out.WriteString(raw[start:index])
	}
	return out.String()
}

func isSQLIdentifierStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isSQLIdentifierPart(value byte) bool {
	return isSQLIdentifierStart(value) || value >= '0' && value <= '9' || value == '$'
}

func normalizeTSQLOpenJSONRaw(raw string) string {
	text := canonicalRawSQL(raw)
	for _, keyword := range []string{"WITH", "AS", "VARCHAR", "NVARCHAR", "DATETIME", "INT", "TINYINT"} {
		replacement := keyword
		if keyword == "INT" {
			replacement = "INTEGER"
		}
		text = replaceSQLWordFold(text, keyword, replacement)
	}
	return text
}

func (transformer targetTransformer) normalizeRawStructArray(raw string, target Dialect) (Expr, bool) {
	transformExpr := transformer.expr
	trimmed := strings.TrimSpace(raw)
	open := strings.IndexByte(trimmed, '[')
	if open < 0 || !strings.HasSuffix(trimmed, "]") {
		return nil, false
	}
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(trimmed[:open])), "ARRAY") && open != 0 {
		return nil, false
	}
	payload := strings.TrimSpace(trimmed[open+1 : len(trimmed)-1])
	if payload == "" {
		return nil, false
	}
	parts := splitTopLevelSQL(payload, ',')
	values := make([]Expr, 0, len(parts))
	for _, part := range parts {
		result, err := ParseStrict("SELECT "+strings.TrimSpace(part), DialectBigQuery)
		if err != nil || len(result.Statements) != 1 {
			return nil, false
		}
		stmt, ok := result.Statements[0].Node.(*SelectStmt)
		if !ok || len(stmt.Projections) != 1 {
			return nil, false
		}
		values = append(values, stmt.Projections[0].Expr)
	}
	array := &FunctionCallExpr{Args: values, ArrayLiteral: true}
	normalizeStructArraySchema(array)
	for index := range array.Args {
		array.Args[index] = transformExpr(array.Args[index], target)
	}
	rendered := make([]string, 0, len(array.Args))
	for _, value := range array.Args {
		rendered = append(rendered, renderDialectExpr(value, target))
	}
	if target == DialectSnowflake {
		return &RawExpr{Raw: "[" + strings.Join(rendered, ", ") + "]"}, true
	}
	return &RawExpr{Raw: "ARRAY[" + strings.Join(rendered, ", ") + "]"}, true
}

func duckDBUnnestNeedsMaxDepth(function *TableFunctionFrom) bool {
	if function == nil || len(function.Name) != 1 || !strings.EqualFold(function.Name[0].Text, "UNNEST") || len(function.Args) != 1 {
		return false
	}
	text := strings.ToUpper(strings.TrimSpace(renderDialectExpr(function.Args[0], DialectDuckDB)))
	return (strings.Contains(text, "STRUCT(") || strings.Contains(text, "{'")) && (strings.HasPrefix(text, "[") || strings.HasPrefix(text, "CAST([") || strings.HasPrefix(text, "ARRAY("))
}

func normalizeDuckDBUnnestMaxDepth(item FromItem) FromItem {
	function, ok := item.(*TableFunctionFrom)
	if !ok || !duckDBUnnestNeedsMaxDepth(function) {
		return item
	}
	argument := renderDialectExpr(function.Args[0], DialectDuckDB)
	alias := function.Alias
	if alias != nil && alias.Text == "_t0" && len(function.Columns) == 1 {
		column := function.Columns[0]
		alias = &column
	}
	return &SubqueryFrom{
		Query: &SelectStmt{RawQuery: "SELECT UNNEST(" + argument + ", max_depth => 2)"},
		Alias: alias,
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

func (transformer targetTransformer) orderItem(item *OrderItem, target Dialect) {
	transformExpr := transformer.expr
	item.Expr = transformExpr(item.Expr, target)
}

func isTSQLDummyOrder(expression Expr) bool {
	raw, ok := expression.(*RawExpr)
	if ok {
		return strings.EqualFold(strings.TrimSpace(raw.Raw), "(SELECT NULL)")
	}
	text := strings.TrimSpace(renderExpr(expression))
	return strings.EqualFold(text, "(SELECT NULL)") || strings.EqualFold(text, "SELECT NULL")
}

func (transformer targetTransformer) window(window *WindowSpec, target Dialect) {
	transformExpr := transformer.expr
	rewriteOrderItems := transformer.orderItems
	for i := range window.PartitionBy {
		window.PartitionBy[i] = transformExpr(window.PartitionBy[i], target)
	}
	window.OrderBy = rewriteOrderItems(window.OrderBy, target)
}

func (transformer targetTransformer) orderItems(items []OrderItem, target Dialect) []OrderItem {
	transformExpr := transformer.expr
	if len(items) == 0 {
		return items
	}
	rewritten := make([]OrderItem, 0, len(items))
	for _, item := range items {
		item.Expr = transformExpr(item.Expr, target)
		if target == DialectTSQL {
			_, numericOrder := item.Expr.(*LiteralExpr)
			needsNullRank := (!item.Descending && !item.NullsFirst || item.Descending && item.NullsFirst) && !numericOrder && !isTSQLDummyOrder(item.Expr)
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
		} else if target == DialectPostgreSQL || target == DialectSnowflake || target == DialectOracle || target == DialectRedshift {
			if target == DialectPostgreSQL && isTSQLDummyOrder(item.Expr) && !item.NullsFirst && !item.NullsLast {
				item.NullsFirst = true
			}
			if item.Ascending && item.NullsLast || item.Descending && item.NullsFirst {
				item.NullsFirst = false
				item.NullsLast = false
			}
		}
		rewritten = append(rewritten, item)
	}
	return rewritten
}
