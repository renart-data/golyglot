package golyglot

import (
	"strconv"
	"strings"
)

func normalizeSparkRawStatement(raw string) string {
	text := canonicalRawSQL(raw)
	text = strings.TrimSpace(replaceFold(text, "DECLARE VARIABLE ", "DECLARE "))
	text = strings.TrimSpace(replaceFold(text, "DECLARE VAR ", "DECLARE "))
	text = strings.TrimSpace(replaceFold(text, " DEFAULT ", " = "))
	upper := strings.ToUpper(text)
	if strings.HasPrefix(upper, "SET ") && !strings.Contains(text, "=") {
		fields := strings.Fields(text[len("SET "):])
		if len(fields) >= 2 {
			text = "SET " + fields[0] + " = " + strings.Join(fields[1:], " ")
		}
	}
	if strings.HasPrefix(strings.ToUpper(text), "SET @") {
		text = "SET " + text[len("SET @"):]
	}
	return text
}

func replaceSQLWordFold(text, word, replacement string) string {
	if word == "" {
		return text
	}
	var result strings.Builder
	for index := 0; index < len(text); {
		start := index
		switch text[index] {
		case '\'', '"', '`':
			quote := text[index]
			index++
			for index < len(text) {
				if text[index] != quote {
					index++
					continue
				}
				if index+1 < len(text) && text[index+1] == quote {
					index += 2
					continue
				}
				index++
				break
			}
			result.WriteString(text[start:index])
			continue
		case '[':
			index++
			for index < len(text) {
				if text[index] != ']' {
					index++
					continue
				}
				if index+1 < len(text) && text[index+1] == ']' {
					index += 2
					continue
				}
				index++
				break
			}
			result.WriteString(text[start:index])
			continue
		}
		if index+len(word) <= len(text) && strings.EqualFold(text[index:index+len(word)], word) &&
			(index == 0 || !isIdentifierByte(text[index-1])) &&
			(index+len(word) == len(text) || !isIdentifierByte(text[index+len(word)])) {
			result.WriteString(replacement)
			index += len(word)
			continue
		}
		result.WriteByte(text[index])
		index++
	}
	return result.String()
}

func normalizeDatabricksRawStatement(raw string) string {
	text := canonicalRawSQL(raw)
	upper := strings.ToUpper(text)
	if strings.HasPrefix(upper, "SET @") {
		text = "SET " + text[len("SET @"):]
		upper = strings.ToUpper(text)
	}
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
	upperText := strings.ToUpper(text)
	if strings.HasPrefix(upperText, "SET ") && !strings.Contains(text, "=") {
		fields := strings.Fields(text[len("SET "):])
		if len(fields) >= 2 {
			text = "SET " + fields[0] + " = " + strings.Join(fields[1:], " ")
		}
	}
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

func normalizeSnowflakeDuckDBTableTail(tail string) string {
	text := normalizeSnowflakeTableTail(tail)
	text = replaceAllFold(text, "SAMPLE (", "TABLESAMPLE RESERVOIR (")
	text = replaceAllFold(text, ") SEED (", ") REPEATABLE (")
	text = replaceAllFold(text, ") AS SEED (", ") REPEATABLE (")
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
	if strings.EqualFold(prefix, "LATERAL FLATTEN") {
		return prefix + "(" + strings.TrimSpace(text[open+1:close]) + ")" + strings.TrimSpace(text[close+1:])
	}
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

func normalizeTSQLFetchStyle(root Node, source Dialect) {
	Walk(root, func(current Node) VisitAction {
		if statement, ok := current.(*SelectStmt); ok {
			if statement.Fetch != nil {
				statement.Fetch.Next = false
			}
			if statement.Fetch == nil && statement.Limit != nil && statement.Offset != nil {
				statement.Fetch = &FetchClause{Count: statement.Limit, Next: source == DialectGeneric || source == DialectDataFusion}
				statement.Limit = nil
			}
		}
		return VisitChildren
	})
}

func normalizeGenericRawStatement(raw string) string {
	trimmed := strings.TrimSpace(raw)
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "START TRANSACTION") {
		return strings.TrimSpace("BEGIN" + trimmed[len("START TRANSACTION"):])
	}
	if strings.HasPrefix(upper, "CREATE INDEX ") || strings.HasPrefix(upper, "CREATE UNIQUE INDEX ") {
		return replaceFold(trimmed, " ON TABLE ", " ON ")
	}
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
	if upperCanonical == "BEGIN TRAN" || strings.HasPrefix(upperCanonical, "BEGIN TRAN ") {
		return "BEGIN TRANSACTION" + canonical[len("BEGIN TRAN"):]
	}
	if upperCanonical == "COMMIT" || upperCanonical == "COMMIT TRAN" || strings.HasPrefix(upperCanonical, "COMMIT TRAN ") {
		if upperCanonical == "COMMIT" {
			return "COMMIT TRANSACTION"
		}
		return "COMMIT TRANSACTION" + canonical[len("COMMIT TRAN"):]
	}
	if upperCanonical == "ROLLBACK" || upperCanonical == "ROLLBACK TRAN" || strings.HasPrefix(upperCanonical, "ROLLBACK TRAN ") {
		if upperCanonical == "ROLLBACK" {
			return "ROLLBACK TRANSACTION"
		}
		return "ROLLBACK TRANSACTION" + canonical[len("ROLLBACK TRAN"):]
	}
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
		text = replaceAllFold(text, " AS ", " ")
		text = replaceSQLWordFold(text, "INT", "INTEGER")
		for _, keyword := range []string{"DECLARE", "CONSTRAINT", "PRIMARY", "KEY", "NOT", "NULL", "SELECT", "FROM", "WHERE", "VARCHAR", "INTEGER"} {
			text = replaceSQLWordFold(text, keyword, strings.ToUpper(keyword))
		}
		if strings.HasPrefix(raw, "DECLARE ") && strings.Contains(raw, " TABLE ") {
			if tableIndex := strings.Index(text, " TABLE "); tableIndex >= 0 {
				open := strings.IndexByte(text[tableIndex+len(" TABLE "):], '(')
				if open >= 0 {
					open += tableIndex + len(" TABLE ")
					if close := matchingParenIndex(text, open); close > open {
						body := replaceSQLWordFold(text[open+1:close], "INTEGER", "INT")
						text = text[:open+1] + body + text[close:]
					}
				}
			}
		}
	} else if strings.HasPrefix(strings.ToUpper(text), "CREATE PROCEDURE ") {
		if open := strings.IndexByte(text, '('); open >= 0 {
			if close := matchingParenIndex(text, open); close > open {
				text = text[:open+1] + strings.ReplaceAll(text[open+1:close], " AS ", " ") + text[close:]
			}
		}
	}
	text = normalizeTSQLIfCondition(text)
	text = replaceFold(text, "CURRENT_TIMESTAMP", "GETDATE()")
	return text
}

func normalizeTSQLIfCondition(text string) string {
	upper := strings.ToUpper(text)
	if !strings.HasPrefix(upper, "IF ") {
		return text
	}
	begin := strings.Index(upper, " BEGIN")
	if begin < 0 {
		return text
	}
	condition := strings.TrimSpace(text[len("IF "):begin])
	if len(condition) < 4 || !strings.HasPrefix(condition, "((") || !strings.HasSuffix(condition, "))") {
		return text
	}
	return "IF " + condition[1:len(condition)-1] + text[begin:]
}

func normalizeTSQLIfStatement(raw string, target Dialect) (string, bool) {
	text := canonicalRawSQL(raw)
	upper := strings.ToUpper(text)
	if !strings.HasPrefix(upper, "IF OBJECT_ID(") {
		return raw, false
	}
	begin := strings.Index(upper, " BEGIN")
	if begin < 0 {
		return raw, false
	}
	if target == DialectSpark || target == DialectDatabricks {
		drop := strings.Index(upper[begin:], "DROP TABLE")
		if drop < 0 {
			return raw, false
		}
		name := strings.TrimSpace(text[begin+drop+len("DROP TABLE"):])
		if end := strings.IndexAny(name, ";"); end >= 0 {
			name = name[:end]
		}
		name = strings.TrimSpace(name)
		name = strings.TrimPrefix(name, "#")
		name = strings.Trim(name, "[]\"`")
		if name == "" {
			return raw, false
		}
		return "DROP TABLE IF EXISTS " + name, true
	}
	if target == DialectTSQL {
		condition := strings.TrimSpace(text[len("IF "):begin])
		if strings.HasSuffix(strings.ToUpper(condition), " IS NOT NULL") {
			condition = strings.TrimSpace(condition[:len(condition)-len(" IS NOT NULL")])
			return "IF NOT " + condition + " IS NULL" + text[begin:], true
		}
	}
	return raw, false
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
	normalizedColumns := make([]string, 0, len(columns))
	for _, column := range columns {
		if normalized := normalizeCreateTableColumn(column, target); strings.TrimSpace(normalized) != "" {
			normalizedColumns = append(normalizedColumns, normalized)
		}
	}
	return "(" + strings.Join(normalizedColumns, ", ") + ")"
}

func normalizeCreateTableClauses(columnsText, suffix string, target Dialect) string {
	inner := strings.TrimSpace(columnsText[1 : len(columnsText)-1])
	columns := splitTopLevelSQL(inner, ',')
	normalizedColumns := make([]string, 0, len(columns))
	for _, column := range columns {
		if normalized := normalizeCreateTableColumn(column, target); strings.TrimSpace(normalized) != "" {
			normalizedColumns = append(normalizedColumns, normalized)
		}
	}
	columns = normalizedColumns
	baseColumns := formatCreateColumns(columns)
	if suffix == "" {
		return baseColumns
	}

	canonicalSuffix := canonicalRawSQL(suffix)
	if (target == DialectSpark || target == DialectHive || target == DialectDatabricks) && strings.HasPrefix(strings.ToUpper(canonicalSuffix), "ON ") {
		return baseColumns
	}
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

func isWholeParenthesizedSQL(text string) bool {
	text = strings.TrimSpace(text)
	return len(text) >= 2 && text[0] == '(' && matchingParenIndex(text, 0) == len(text)-1
}

func normalizeCreateTableColumn(column string, target Dialect) string {
	name, rest := splitLeadingSQLToken(strings.TrimSpace(column))
	if name == "" || rest == "" {
		return strings.TrimSpace(column)
	}
	switch strings.ToUpper(strings.Trim(name, "`[]\"")) {
	case "CONSTRAINT":
		if target == DialectSpark || target == DialectHive || target == DialectDatabricks {
			return normalizeTSQLCreateConstraint(column, target)
		}
		return strings.TrimSpace(column)
	case "PRIMARY", "UNIQUE", "CHECK", "FOREIGN", "EXCLUDE":
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
	if target == DialectSpark || target == DialectHive || target == DialectDatabricks {
		constraints = normalizeTSQLCreateColumnConstraints(constraints)
	}
	if target == DialectTSQL {
		constraints = replaceAllFold(constraints, "AUTO_INCREMENT", "IDENTITY")
	}
	if target == DialectDatabricks && (strings.EqualFold(typeToken, "INT") || strings.EqualFold(typeToken, "INTEGER")) && strings.Contains(strings.ToUpper(constraints), "IDENTITY") {
		typeToken = "BIGINT"
	}
	constraints = normalizeCreateIdentityConstraints(constraints, target)
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

func normalizeTSQLCreateColumnConstraints(constraints string) string {
	words := strings.Fields(constraints)
	if len(words) == 0 {
		return ""
	}
	result := make([]string, 0, len(words))
	for index := 0; index < len(words); index++ {
		upper := strings.ToUpper(words[index])
		if upper == "NULL" {
			if index == 0 || !strings.EqualFold(words[index-1], "NOT") {
				continue
			}
		}
		if upper == "NOT" && index+2 < len(words) && strings.EqualFold(words[index+1], "FOR") && strings.EqualFold(words[index+2], "REPLICATION") {
			index += 2
			continue
		}
		result = append(result, words[index])
	}
	return strings.Join(result, " ")
}

func normalizeCreateIdentityConstraints(constraints string, target Dialect) string {
	text := strings.TrimSpace(constraints)
	upper := strings.ToUpper(text)
	identityIndex := strings.Index(upper, "IDENTITY")
	if identityIndex < 0 {
		return text
	}
	open := identityIndex + len("IDENTITY")
	for open < len(text) && (text[open] == ' ' || text[open] == '\t') {
		open++
	}
	startValue := "1"
	incrementValue := "1"
	hadParentheses := false
	prefix := strings.TrimSpace(text[:identityIndex])
	if generated := strings.Index(strings.ToUpper(prefix), "GENERATED"); generated >= 0 {
		prefix = strings.TrimSpace(prefix[:generated])
	}
	rest := strings.TrimSpace(text[open:])
	if open < len(text) && text[open] == '(' {
		hadParentheses = true
		close := matchingParenIndex(text, open)
		if close < 0 {
			return text
		}
		inner := strings.TrimSpace(text[open+1 : close])
		parts := splitTopLevelSQL(inner, ',')
		if len(parts) > 0 && strings.TrimSpace(parts[0]) != "" && !strings.Contains(strings.ToUpper(inner), "START ") {
			startValue = strings.TrimSpace(parts[0])
			if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
				incrementValue = strings.TrimSpace(parts[1])
			}
		} else {
			words := strings.Fields(inner)
			for index := 0; index+2 < len(words); index++ {
				switch strings.ToUpper(words[index]) {
				case "START":
					if strings.EqualFold(words[index+1], "WITH") {
						startValue = strings.Trim(words[index+2], ",")
					}
				case "INCREMENT":
					if strings.EqualFold(words[index+1], "BY") {
						incrementValue = strings.Trim(words[index+2], ",")
					}
				}
			}
		}
		rest = strings.TrimSpace(text[close+1:])
	}
	switch target {
	case DialectPostgreSQL, DialectDatabricks:
		value := "GENERATED BY DEFAULT AS IDENTITY (START WITH " + startValue + " INCREMENT BY " + incrementValue + ")"
		return strings.TrimSpace(prefix + " " + value + " " + rest)
	case DialectTSQL:
		if !hadParentheses && startValue == "1" && incrementValue == "1" {
			return strings.TrimSpace(prefix + " IDENTITY" + func() string {
				if rest == "" {
					return ""
				}
				return " " + rest
			}())
		}
		return strings.TrimSpace(prefix + " IDENTITY(" + startValue + ", " + incrementValue + ")" + func() string {
			if rest == "" {
				return ""
			}
			return " " + rest
		}())
	default:
		return text
	}
}

func normalizeTSQLCreateConstraint(column string, target Dialect) string {
	text := strings.TrimSpace(column)
	upper := strings.ToUpper(text)
	if strings.Contains(upper, " UNIQUE ") || strings.HasSuffix(upper, " UNIQUE") || strings.Contains(upper, " UNIQUE(") {
		return ""
	}
	if !strings.Contains(upper, " PRIMARY KEY") {
		return ""
	}
	rest := strings.TrimSpace(text[len("CONSTRAINT "):])
	constraintName, rest := splitLeadingSQLToken(rest)
	constraintName = normalizeCreateColumnName(constraintName, target)
	primaryIndex := strings.Index(strings.ToUpper(rest), "PRIMARY KEY")
	if primaryIndex < 0 {
		return ""
	}
	keyTail := strings.TrimSpace(rest[primaryIndex+len("PRIMARY KEY"):])
	open := strings.IndexByte(keyTail, '(')
	if open < 0 {
		return "CONSTRAINT " + constraintName + " PRIMARY KEY"
	}
	close := matchingParenIndex(keyTail, open)
	if close < 0 {
		return "CONSTRAINT " + constraintName + " PRIMARY KEY " + strings.TrimSpace(keyTail[:open]) + keyTail[open:]
	}
	columnText := splitTopLevelSQL(keyTail[open+1:close], ',')
	for index, value := range columnText {
		fields := strings.Fields(strings.TrimSpace(value))
		if len(fields) > 0 {
			columnText[index] = normalizeCreateColumnName(fields[0], target)
		}
	}
	return "CONSTRAINT " + constraintName + " PRIMARY KEY (" + strings.Join(columnText, ", ") + ")"
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
	if target != DialectSpark && target != DialectHive && target != DialectDatabricks {
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
	bracketedBase := false
	if strings.HasPrefix(typeToken, "[") {
		if close := strings.IndexByte(typeToken, ']'); close >= 0 {
			bracketedBase = true
			typeToken = strings.ReplaceAll(typeToken[1:close], "]]", "]") + typeToken[close+1:]
		}
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
		if bracketedBase && target == DialectHive && suffix != "" && (base == "VARCHAR" || base == "NVARCHAR" || base == "CHAR" || base == "NCHAR") {
			return "VARCHAR" + suffix
		}
		return mapped
	}
	switch target {
	case DialectDuckDB:
		switch upper {
		case "DECFLOAT":
			return "DECIMAL(38, 5)"
		case "FLOAT":
			return "DOUBLE"
		case "INT":
			return "INT"
		case "INT64":
			return "BIGINT"
		case "FLOAT64":
			return "DOUBLE"
		case "DATETIME":
			return "TIMESTAMP"
		case "STRING", "VARCHAR":
			return "TEXT"
		case "INTEGER":
			return "INT"
		case "UNIQUEIDENTIFIER":
			return "UUID"
		case "VARBINARY":
			return "BLOB"
		}
	case DialectPresto:
		switch upper {
		case "STRING", "TEXT", "VARCHAR":
			return "VARCHAR"
		case "INT":
			return "INTEGER"
		case "INT64":
			return "BIGINT"
		case "UNIQUEIDENTIFIER":
			return "UUID"
		}
	case DialectBigQuery:
		switch upper {
		case "INT", "INTEGER", "BIGINT":
			return "INT64"
		case "STRING", "TEXT", "VARCHAR":
			return "STRING"
		}
	case DialectPostgreSQL:
		switch upper {
		case "INT", "INTEGER":
			return "INT"
		case "UNIQUEIDENTIFIER":
			return "UUID"
		case "VARBINARY":
			return "BYTEA"
		}
	case DialectSpark, DialectHive:
		if upper == "INT64" {
			return "BIGINT"
		}
		if target == DialectSpark {
			switch base {
			case "UNIQUEIDENTIFIER":
				return "STRING"
			case "VARBINARY":
				if suffix == "" {
					return "BINARY"
				}
				return "BINARY" + suffix
			case "NVARCHAR", "VARCHAR":
				if suffix == "(MAX)" {
					return "STRING"
				}
				if suffix == "" {
					return "VARCHAR"
				}
				return "VARCHAR" + suffix
			case "NCHAR", "CHAR":
				if suffix == "" {
					return "CHAR"
				}
				return "CHAR" + suffix
			case "FLOAT":
				if suffix == "(64)" {
					return "DOUBLE"
				}
			}
		}
		if upper == "DATETIME2" {
			return "TIMESTAMP"
		}
		if upper == "ROWVERSION" {
			return "BINARY"
		}
		if upper == "INTEGER" {
			return "INT"
		}
		if strings.HasPrefix(upper, "TIME(") {
			return "TIMESTAMP"
		}
		if base == "FLOAT" && suffix != "" {
			precision, err := strconv.Atoi(strings.Trim(suffix, "() "))
			if err == nil && precision > 32 {
				return "DOUBLE"
			}
			return "FLOAT"
		}
		return upper
	case DialectDatabricks:
		if upper == "INT64" {
			return "BIGINT"
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
		if base == "TIMESTAMP_NTZ" || base == "TIMESTAMP_LTZ" {
			return "DATETIME2" + suffix
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

func normalizeSnowflakeObjectType(typeToken string) string {
	text := strings.TrimSpace(typeToken)
	upper := strings.ToUpper(text)
	if !strings.HasPrefix(upper, "STRUCT<") || !strings.HasSuffix(text, ">") {
		return normalizeCreateTypeToken(text, DialectSnowflake)
	}
	inner := text[len("STRUCT<") : len(text)-1]
	fields := splitTopLevelSQL(inner, ',')
	objects := make([]string, 0, len(fields))
	for _, field := range fields {
		name, fieldType := splitTopLevelSQLColon(field)
		if name == "" || fieldType == "" {
			return "OBJECT"
		}
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(fieldType)), "STRUCT<") {
			fieldType = normalizeSnowflakeObjectType(fieldType)
		} else {
			fieldType = normalizeCreateTypeToken(fieldType, DialectSnowflake)
		}
		objects = append(objects, strings.TrimSpace(name)+" "+fieldType)
	}
	if len(objects) == 0 {
		return "OBJECT"
	}
	return "OBJECT(" + strings.Join(objects, ", ") + ")"
}

func splitTopLevelSQLColon(text string) (string, string) {
	parts := splitTopLevelSQL(text, ':')
	if len(parts) >= 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(strings.Join(parts[1:], ":"))
	}
	parts = splitTopLevelSQL(text, ' ')
	compact := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			compact = append(compact, part)
		}
	}
	if len(compact) < 2 {
		return "", ""
	}
	return compact[0], strings.Join(compact[1:], " ")
}
