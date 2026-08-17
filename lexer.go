package golyglot

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

var sqlKeywords = map[string]struct{}{
	"ALL": {}, "AND": {}, "AS": {}, "ASC": {}, "BETWEEN": {}, "BY": {}, "IF": {},
	"CASE": {}, "CAST": {}, "COLLATE": {}, "CROSS": {}, "CURRENT": {},
	"CURRENT_DATE": {}, "CURRENT_TIME": {}, "CURRENT_TIMESTAMP": {}, "CREATE": {}, "DELETE": {}, "AT": {},
	"DESC": {}, "DISTINCT": {}, "ELSE": {}, "END": {}, "ESCAPE": {},
	"EXCEPT": {}, "EXISTS": {}, "FALSE": {}, "FETCH": {}, "FIRST": {},
	"FOLLOWING": {}, "FOR": {}, "FROM": {}, "FULL": {}, "GROUP": {}, "GLOB": {},
	"HAVING": {}, "IN": {}, "INNER": {}, "INSERT": {}, "INTERSECT": {},
	"INTO": {}, "IS": {}, "JOIN": {}, "LATERAL": {}, "LAST": {}, "LEFT": {},
	"LIKE": {}, "LIMIT": {}, "NOT": {}, "NULL": {}, "NULLS": {}, "OFFSET": {},
	"ON": {}, "OR": {}, "ORDER": {}, "OPTION": {}, "OUTER": {}, "OVER": {}, "PARTITION": {}, "PIVOT": {}, "UNPIVOT": {}, "NATURAL": {}, "SEMI": {}, "ANTI": {},
	"PRECEDING": {}, "QUALIFY": {}, "RANGE": {}, "RIGHT": {}, "RETURNING": {},
	"ROWS": {}, "ROW": {}, "SELECT": {}, "TABLE": {}, "TABLESAMPLE": {}, "REPLACE": {}, "THEN": {}, "TOP": {}, "TRUE": {},
	"UNION": {}, "UNNEST": {}, "UPDATE": {}, "USING": {}, "VALUES": {},
	"WHEN": {}, "WHERE": {}, "WINDOW": {}, "WITH": {}, "ALTER": {}, "DROP": {},
	"EXCLUDE": {}, "UNDROP": {}, "MD5_HEX": {},
	"MERGE": {}, "TRUNCATE": {}, "GRANT": {}, "REVOKE": {}, "EXPLAIN": {},
	"SHOW": {}, "DESCRIBE": {}, "USE": {}, "OPEN": {}, "CACHE": {}, "UNCACHE": {}, "LOAD": {}, "COMMENT": {}, "PRAGMA": {}, "KILL": {}, "CONNECT": {}, "STRAIGHT_JOIN": {},
	"CLUSTER": {}, "SAMPLE": {}, "SETTINGS": {}, "MATCH_RECOGNIZE": {}, "INDEXED": {}, "REFRESH": {}, "DEALLOCATE": {}, "RESET": {}, "EXECUTE": {},
	"COPY": {}, "UNLOAD": {}, "PRINT": {},
	"BEGIN": {}, "START": {}, "COMMIT": {}, "ROLLBACK": {}, "VACUUM": {}, "ANALYZE": {}, "EXPORT": {}, "IMPORT": {}, "CALL": {}, "EXEC": {}, "DECLARE": {}, "LOOP": {}, "REPEAT": {}, "WHILE": {}, "MODEL": {}, "CORRESPONDING": {}, "STRICT": {},
	"ATTACH": {}, "DETACH": {}, "EXCHANGE": {}, "INSTALL": {}, "CHECKPOINT": {}, "OPTIMIZE": {}, "SUMMARIZE": {}, "SEQUENCE": {}, "FORCE": {}, "SYSTEM": {},
}

type sqlWordMetadata struct {
	text      string
	word      tokenWord
	isKeyword bool
}

var sqlWordLookup = buildSQLWordLookup()

func buildSQLWordLookup() map[uint64]sqlWordMetadata {
	lookup := make(map[uint64]sqlWordMetadata, len(sqlKeywords)+len(tokenWordDefinitions))
	for keyword := range sqlKeywords {
		hash := foldedSQLWordHash(keyword)
		if existing, ok := lookup[hash]; ok && existing.text != keyword {
			panic("SQL word hash collision")
		}
		lookup[hash] = sqlWordMetadata{text: keyword, isKeyword: true}
	}
	for _, definition := range tokenWordDefinitions {
		hash := foldedSQLWordHash(definition.text)
		metadata, ok := lookup[hash]
		if ok && metadata.text != definition.text {
			panic("SQL word hash collision")
		}
		metadata.text = definition.text
		metadata.word = definition.word
		lookup[hash] = metadata
	}
	return lookup
}

func foldedSQLWordHash(text string) uint64 {
	const (
		offset64 = uint64(14695981039346656037)
		prime64  = uint64(1099511628211)
	)
	hash := offset64
	for index := 0; index < len(text); index++ {
		c := text[index]
		if c >= 'a' && c <= 'z' {
			c -= 'a' - 'A'
		}
		hash ^= uint64(c)
		hash *= prime64
	}
	return hash
}

func classifySQLWord(text string) (TokenKind, tokenWord) {
	metadata, ok := sqlWordLookup[foldedSQLWordHash(text)]
	if !ok || !equalFoldASCII(text, metadata.text) {
		return TokenIdentifier, tokenWordNone
	}
	if metadata.isKeyword {
		return TokenKeyword, metadata.word
	}
	return TokenIdentifier, metadata.word
}

type lexer struct {
	text        string
	pos         int
	options     ParseOptions
	tokens      []Token
	diagnostics []Diagnostic
	budgetHit   bool
}

func lexSQL(text string, options ParseOptions) ([]Token, []Diagnostic) {
	l := lexer{text: text, options: options}
	l.run()
	return l.tokens, l.diagnostics
}

func (l *lexer) run() {
	for l.pos < len(l.text) {
		start := l.pos
		c := l.text[l.pos]

		switch {
		case isSpaceAt(l.text, l.pos):
			l.consumeWhitespace()
		case l.options.Dialect == DialectSnowflake && strings.HasPrefix(l.text[l.pos:], "//") && !snowflakeURISlash(l.text, l.pos):
			l.scanLineComment(2)
		case strings.HasPrefix(l.text[l.pos:], "--"):
			l.scanLineComment(2)
		case c == '#' && (l.options.Dialect == DialectTSQL || l.options.Dialect == DialectRedshift) && l.pos+1 < len(l.text) && (l.text[l.pos+1] == '#' || isIdentifierStartAt(l.text, l.pos+1) || isASCIIDigit(l.text[l.pos+1])):
			l.scanIdentifier()
		case c == '#' && l.options.Dialect == DialectDuckDB && l.pos+1 < len(l.text) && isASCIIDigit(l.text[l.pos+1]):
			l.scanDuckDBPositionalColumn()
		case c == '#' && (l.options.Dialect == DialectMySQL || l.options.Dialect == DialectBigQuery || l.options.Dialect == DialectClickHouse || l.options.Dialect == DialectGeneric) &&
			(l.pos == 0 || isSpace(l.text[l.pos-1])):
			l.scanLineComment(1)
		case strings.HasPrefix(l.text[l.pos:], "/*"):
			l.scanBlockComment()
		case l.options.Dialect == DialectBigQuery && (c == 'b' || c == 'B' || c == 'r' || c == 'R') && l.pos+1 < len(l.text) && (l.text[l.pos+1] == '\'' || l.text[l.pos+1] == '"'):
			l.scanBigQueryPrefixedString()
		case l.options.Dialect == DialectBigQuery && (c == '\'' || c == '"'):
			l.scanBigQueryString(0)
		case c == '\'' || c == '"' || c == '`' || (c == '[' && (l.options.Dialect == DialectTSQL || l.options.Dialect == DialectExasol || l.options.Dialect == DialectSQLite)):
			l.scanQuoted(c)
		case strings.HasPrefix(l.text[l.pos:], "${"):
			l.scanTemplateParameter()
		case c == '$' && l.scanDollarQuoted():
		case c == '$' && l.pos+1 < len(l.text) && isASCIIDigit(l.text[l.pos+1]):
			l.scanParameter()
		case c == '@':
			if strings.HasPrefix(l.text[l.pos:], "@>") ||
				(l.options.Dialect == DialectPostgreSQL && (strings.HasPrefix(l.text[l.pos:], "@?") || strings.HasPrefix(l.text[l.pos:], "@@"))) {
				l.scanOperator()
				break
			}
			l.scanAtParameter()
		case strings.HasPrefix(l.text[l.pos:], "??"):
			l.scanOperator()
		case c == '?' && l.options.Dialect == DialectPostgreSQL:
			if postgresQuestionParameter(l.text, l.pos) {
				l.pos++
				l.emit(TokenParameter, start, l.pos)
			} else {
				l.scanOperator()
			}
		case c == '?':
			l.pos++
			l.emit(TokenParameter, start, l.pos)
		case c == '%' && l.options.Dialect == DialectPostgreSQL && l.postgresPercentParameter():
			l.scanPostgresPercentParameter()
		case c == ':' && l.options.Dialect != DialectDuckDB && l.pos+1 < len(l.text) && isIdentifierStartAt(l.text, l.pos+1):
			l.scanColonParameter()
		case c == ':' && l.options.Dialect == DialectSnowflake && l.pos+1 < len(l.text) && isASCIIDigit(l.text[l.pos+1]):
			l.scanColonParameter()
		case isASCIIDigit(c) && l.numericIdentifierStart():
			// Hive-family grammars permit unquoted identifiers such as 1_a and
			// 1a_1a. Keep ordinary numerics (including scientific notation) on
			// the number path below.
			l.scanIdentifier()
		case isASCIIDigit(c) || (c == '.' && l.pos+1 < len(l.text) && isASCIIDigit(l.text[l.pos+1])):
			l.scanNumber()
		case isIdentifierStartAt(l.text, l.pos):
			l.scanIdentifier()
		case isPunctuation(c):
			l.pos++
			l.emit(TokenPunctuation, start, l.pos)
		case isOperatorChar(c):
			l.scanOperator()
		default:
			l.pos += runeWidth(l.text[l.pos:])
			l.emit(TokenUnknown, start, l.pos)
		}
	}

	l.tokens = append(l.tokens, Token{Kind: TokenEOF, Span: Span{Start: len(l.text), End: len(l.text)}})
}

func (l *lexer) consumeWhitespace() {
	for l.pos < len(l.text) {
		if c := l.text[l.pos]; c < utf8.RuneSelf {
			if !isSpace(c) {
				break
			}
			l.pos++
			continue
		}
		r, size := utf8.DecodeRuneInString(l.text[l.pos:])
		if !unicode.IsSpace(r) {
			break
		}
		l.pos += size
	}
}

// snowflakeURISlash prevents the second slash in URI schemes such as
// file:///path and s3://bucket from being mistaken for a SQL line comment.
// Snowflake still treats a standalone // after whitespace as a comment.
func snowflakeURISlash(text string, pos int) bool {
	return pos > 0 && (text[pos-1] == ':' || (text[pos-1] == '/' && pos > 1 && text[pos-2] == ':'))
}

func (l *lexer) scanLineComment(prefix int) {
	start := l.pos
	l.pos += prefix
	for l.pos < len(l.text) && l.text[l.pos] != '\n' {
		l.pos++
	}
	l.emit(TokenComment, start, l.pos)
}

func (l *lexer) scanBlockComment() {
	start := l.pos
	l.pos += 2
	if l.options.Dialect == DialectSnowflake || l.options.Dialect == DialectOracle {
		for l.pos < len(l.text) && !strings.HasPrefix(l.text[l.pos:], "*/") {
			l.pos += runeWidth(l.text[l.pos:])
		}
		if strings.HasPrefix(l.text[l.pos:], "*/") {
			l.pos += 2
			l.emit(TokenComment, start, l.pos)
			return
		}
		l.emit(TokenUnterminatedComment, start, l.pos)
		l.diagnostics = append(l.diagnostics, Diagnostic{
			Severity: SeverityError,
			Code:     "LEX_UNTERMINATED_COMMENT",
			Message:  "unterminated block comment; expected */",
			Span:     Span{Start: start, End: l.pos},
			Found:    TokenUnterminatedComment,
		})
		return
	}
	depth := 1
	for l.pos < len(l.text) && depth > 0 {
		switch {
		case strings.HasPrefix(l.text[l.pos:], "/*"):
			depth++
			l.pos += 2
		case strings.HasPrefix(l.text[l.pos:], "*/"):
			depth--
			l.pos += 2
		default:
			l.pos += runeWidth(l.text[l.pos:])
		}
	}
	if depth != 0 {
		l.emit(TokenUnterminatedComment, start, l.pos)
		l.diagnostics = append(l.diagnostics, Diagnostic{
			Severity: SeverityError,
			Code:     "LEX_UNTERMINATED_COMMENT",
			Message:  "unterminated block comment; expected */",
			Span:     Span{Start: start, End: l.pos},
			Found:    TokenUnterminatedComment,
		})
		return
	}
	l.emit(TokenComment, start, l.pos)
}

func (l *lexer) scanQuoted(quote byte) {
	start := l.pos
	backslashEscapes := quote == '\'' && ((start > 0 && (l.text[start-1] == 'e' || l.text[start-1] == 'E') && (start == 1 || !isIdentifierPart(rune(l.text[start-2])))) || l.options.Dialect == DialectAthena || l.options.Dialect == DialectMySQL || l.options.Dialect == DialectGeneric || l.options.Dialect == DialectClickHouse || l.options.Dialect == DialectHive || l.options.Dialect == DialectSpark || l.options.Dialect == DialectDatabricks || l.options.Dialect == DialectDuckDB || l.options.Dialect == DialectPresto || l.options.Dialect == DialectTrino)
	l.pos++
	closed := false
	for l.pos < len(l.text) {
		if quote == '[' {
			if l.text[l.pos] == ']' {
				l.pos++
				closed = true
				break
			}
			l.pos += runeWidth(l.text[l.pos:])
			continue
		}
		if l.text[l.pos] == '\\' && (backslashEscapes || quote == '`') && l.pos+1 < len(l.text) {
			// SQLGlot accepts a one-character backslash string such as
			// `ESCAPE '\\'`. In that form the quote after the backslash is the
			// string terminator, not an escaped quote. Keep normal escaped
			// quotes intact when another string character follows them.
			if quote == '\'' && l.options.Dialect == DialectGeneric && l.text[l.pos+1] == quote && quotedStringDelimiter(l.text, l.pos+2) {
				// Let the normal quote branch below close the token.
			} else {
				// PostgreSQL-style E strings use backslash escapes. Treat the
				// escaped byte as part of the token so an escaped quote cannot
				// prematurely terminate the string or quoted identifier.
				l.pos += 2
				continue
			}
		}
		if l.text[l.pos] != quote {
			l.pos += runeWidth(l.text[l.pos:])
			continue
		}
		if l.pos+1 < len(l.text) && l.text[l.pos+1] == quote {
			l.pos += 2
			continue
		}
		l.pos++
		closed = true
		break
	}

	if !closed {
		kind := TokenUnterminatedString
		code := "LEX_UNTERMINATED_STRING"
		message := "unterminated string literal; expected a closing quote"
		if quote != '\'' {
			kind = TokenUnknown
			code = "LEX_UNTERMINATED_QUOTED_IDENTIFIER"
			message = "unterminated quoted identifier; expected a closing quote"
		}
		l.emit(kind, start, l.pos)
		l.diagnostics = append(l.diagnostics, Diagnostic{
			Severity: SeverityError,
			Code:     code,
			Message:  message,
			Span:     Span{Start: start, End: l.pos},
			Found:    kind,
		})
		return
	}
	if quote == '\'' {
		l.emit(TokenString, start, l.pos)
	} else {
		l.emit(TokenQuotedIdentifier, start, l.pos)
	}
}

func quotedStringDelimiter(text string, pos int) bool {
	if pos >= len(text) {
		return true
	}
	return isSpace(text[pos]) || isPunctuation(text[pos]) || isOperatorChar(text[pos])
}

func (l *lexer) scanBigQueryPrefixedString() {
	l.scanBigQueryString(1)
}

func (l *lexer) scanBigQueryString(prefixLength int) {
	start := l.pos
	if prefixLength > 0 {
		l.pos += prefixLength
	}
	quote := l.text[l.pos]
	l.pos++
	delimiterLength := 1
	if l.pos+1 < len(l.text) && l.text[l.pos] == quote && l.text[l.pos+1] == quote {
		delimiterLength = 3
		l.pos += 2
	}
	closed := false
	for l.pos < len(l.text) {
		if delimiterLength == 3 {
			if l.text[l.pos] == '\\' && l.pos+1 < len(l.text) {
				l.pos += 2
				continue
			}
			if l.pos+2 < len(l.text) && l.text[l.pos] == quote && l.text[l.pos+1] == quote && l.text[l.pos+2] == quote {
				l.pos += 3
				closed = true
				break
			}
			l.pos += runeWidth(l.text[l.pos:])
			continue
		}
		// BigQuery accepts a backslash-escaped quote in prefixed strings as
		// well.  Keeping it in the token is important for raw literals such as
		// r'x\''; the raw/string distinction is handled by transpilation after
		// lexing.
		if l.text[l.pos] == '\\' && l.pos+1 < len(l.text) {
			l.pos += 2
			continue
		}
		if l.text[l.pos] == quote {
			if l.pos+1 < len(l.text) && l.text[l.pos+1] == quote {
				l.pos += 2
				continue
			}
			l.pos++
			closed = true
			break
		}
		l.pos += runeWidth(l.text[l.pos:])
	}
	if !closed {
		l.emit(TokenUnterminatedString, start, l.pos)
		l.diagnostics = append(l.diagnostics, Diagnostic{
			Severity: SeverityError,
			Code:     "LEX_UNTERMINATED_STRING",
			Message:  "unterminated BigQuery string literal; expected a closing quote",
			Span:     Span{Start: start, End: l.pos},
			Found:    TokenUnterminatedString,
		})
		return
	}
	l.emit(TokenString, start, l.pos)
}

func (l *lexer) scanDollarQuoted() bool {
	start := l.pos
	endTag := strings.IndexByte(l.text[l.pos+1:], '$')
	if endTag < 0 {
		return false
	}
	endTag += l.pos + 1
	tag := l.text[l.pos : endTag+1]
	if len(tag) < 2 {
		return false
	}
	for offset, r := range tag[1 : len(tag)-1] {
		if offset == 0 && unicode.IsDigit(r) && l.options.Dialect != DialectClickHouse {
			return false
		}
		// PostgreSQL spells tags as identifiers, while DuckDB also accepts
		// Unicode symbol tags (for example $🦆$...$🦆$). Keep the lexer
		// permissive for those tags without mistaking punctuation or whitespace
		// for a dollar-quote opener.
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.Is(unicode.Symbol, r) {
			return false
		}
	}
	contentStart := endTag + 1
	closing := strings.Index(l.text[contentStart:], tag)
	if closing < 0 {
		l.pos = len(l.text)
		l.emit(TokenUnterminatedString, start, l.pos)
		l.diagnostics = append(l.diagnostics, Diagnostic{
			Severity: SeverityError,
			Code:     "LEX_UNTERMINATED_STRING",
			Message:  "unterminated dollar-quoted string; expected " + tag,
			Span:     Span{Start: start, End: l.pos},
			Found:    TokenUnterminatedString,
		})
		return true
	}
	l.pos = contentStart + closing + len(tag)
	l.emit(TokenString, start, l.pos)
	return true
}

func (l *lexer) scanParameter() {
	start := l.pos
	l.pos++
	for l.pos < len(l.text) && isASCIIDigit(l.text[l.pos]) {
		l.pos++
	}
	l.emit(TokenParameter, start, l.pos)
}

func (l *lexer) scanDuckDBPositionalColumn() {
	start := l.pos
	l.pos++
	for l.pos < len(l.text) && isASCIIDigit(l.text[l.pos]) {
		l.pos++
	}
	l.emit(TokenIdentifier, start, l.pos)
}

func (l *lexer) scanColonParameter() {
	start := l.pos
	l.pos++
	for l.pos < len(l.text) && isIdentifierContinueAt(l.text, l.pos) {
		l.pos += runeWidth(l.text[l.pos:])
	}
	l.emit(TokenParameter, start, l.pos)
}

func (l *lexer) scanAtParameter() {
	start := l.pos
	for l.pos < len(l.text) && l.text[l.pos] == '@' {
		l.pos++
	}
	if l.pos < len(l.text) && (l.text[l.pos] == '\'' || l.text[l.pos] == '"') {
		quote := l.text[l.pos]
		l.pos++
		for l.pos < len(l.text) {
			if l.text[l.pos] == quote {
				if l.pos+1 < len(l.text) && l.text[l.pos+1] == quote {
					l.pos += 2
					continue
				}
				l.pos++
				break
			}
			l.pos += runeWidth(l.text[l.pos:])
		}
	} else {
		for l.pos < len(l.text) {
			r, size := utf8.DecodeRuneInString(l.text[l.pos:])
			if !isIdentifierPart(r) && !unicode.IsDigit(r) {
				break
			}
			l.pos += size
		}
	}
	l.emit(TokenParameter, start, l.pos)
}

func (l *lexer) postgresPercentParameter() bool {
	if l.pos+1 >= len(l.text) {
		return false
	}
	next := l.text[l.pos+1]
	return next == '(' || isIdentifierStartAt(l.text, l.pos+1)
}

func (l *lexer) scanPostgresPercentParameter() {
	start := l.pos
	l.pos++
	if l.pos < len(l.text) && l.text[l.pos] == '(' {
		for l.pos < len(l.text) {
			l.pos++
			if l.text[l.pos-1] == ')' {
				break
			}
		}
		if l.pos < len(l.text) && isIdentifierStartAt(l.text, l.pos) {
			for l.pos < len(l.text) && isIdentifierContinueAt(l.text, l.pos) {
				l.pos += runeWidth(l.text[l.pos:])
			}
		}
	} else {
		for l.pos < len(l.text) && isIdentifierContinueAt(l.text, l.pos) {
			l.pos += runeWidth(l.text[l.pos:])
		}
	}
	l.emit(TokenParameter, start, l.pos)
}

func postgresQuestionParameter(text string, pos int) bool {
	index := pos - 1
	for index >= 0 && isSpace(text[index]) {
		index--
	}
	if index < 0 {
		return true
	}
	switch text[index] {
	case '=', '(', ',', ':':
		return true
	default:
		return false
	}
}

func (l *lexer) scanTemplateParameter() {
	start := l.pos
	l.pos += 2
	for l.pos < len(l.text) && l.text[l.pos] != '}' {
		l.pos += runeWidth(l.text[l.pos:])
	}
	if l.pos < len(l.text) {
		l.pos++
	} else {
		l.diagnostics = append(l.diagnostics, Diagnostic{
			Severity: SeverityError,
			Code:     "LEX_UNTERMINATED_TEMPLATE",
			Message:  "unterminated ${...} template parameter; expected }",
			Span:     Span{Start: start, End: l.pos},
			Found:    TokenUnknown,
		})
	}
	l.emit(TokenIdentifier, start, l.pos)
}

func (l *lexer) scanNumber() {
	start := l.pos
	if l.pos+1 < len(l.text) && l.text[l.pos] == '0' && (l.text[l.pos+1] == 'x' || l.text[l.pos+1] == 'X') {
		l.pos += 2
		for l.pos < len(l.text) && (isASCIIDigit(l.text[l.pos]) || (l.text[l.pos] >= 'a' && l.text[l.pos] <= 'f') || (l.text[l.pos] >= 'A' && l.text[l.pos] <= 'F') || l.text[l.pos] == '_') {
			l.pos++
		}
		l.emit(TokenNumber, start, l.pos)
		return
	}
	hadDot := false
	if l.text[l.pos] == '.' {
		hadDot = true
		l.pos++
	}
	for l.pos < len(l.text) && (isASCIIDigit(l.text[l.pos]) || l.text[l.pos] == '_') {
		l.pos++
	}
	if !hadDot && l.pos < len(l.text) && l.text[l.pos] == '.' {
		hadDot = true
		l.pos++
		for l.pos < len(l.text) && (isASCIIDigit(l.text[l.pos]) || l.text[l.pos] == '_') {
			l.pos++
		}
	}
	if l.pos < len(l.text) && (l.text[l.pos] == 'e' || l.text[l.pos] == 'E') {
		exponent := l.pos
		l.pos++
		if l.pos < len(l.text) && (l.text[l.pos] == '+' || l.text[l.pos] == '-') {
			l.pos++
		}
		digits := l.pos
		for l.pos < len(l.text) && (isASCIIDigit(l.text[l.pos]) || l.text[l.pos] == '_') {
			l.pos++
		}
		if digits == l.pos {
			l.pos = exponent
		}
	}
	l.emit(TokenNumber, start, l.pos)
}

func (l *lexer) scanIdentifier() {
	start := l.pos
	for l.pos < len(l.text) {
		if c := l.text[l.pos]; c < utf8.RuneSelf {
			if !isASCIIIdentifierPart(c) || (c == '#' && l.options.Dialect == DialectPostgreSQL) {
				break
			}
			l.pos++
			continue
		}
		r, size := utf8.DecodeRuneInString(l.text[l.pos:])
		if !isIdentifierPart(r) {
			break
		}
		l.pos += size
	}
	text := l.text[start:l.pos]
	kind, word := classifySQLWord(text)
	before := len(l.tokens)
	l.emit(kind, start, l.pos)
	if len(l.tokens) > before {
		l.tokens[len(l.tokens)-1].word = word
	}
}

func (l *lexer) scanOperator() {
	start := l.pos
	for _, operator := range []string{
		"!~~*", "!~~", "~~~", "||/", "<<->>", "@?", "#>>", "#-", "@@", "|/", "~~*", "~~", "^@", "!~*", "~*", "->>", "-|-", "<=>", "<->", "??", "!~", "!:>", ":>", "::", "^=", ">=", "<=", "<>", "!=", "==", "||", "&&", "->", "=>", ":=", "<<", ">>", "**", "//", "#>", "@>", "<@", "?&", "?|",
	} {
		if strings.HasPrefix(l.text[l.pos:], operator) {
			l.pos += len(operator)
			l.emit(TokenOperator, start, l.pos)
			return
		}
	}
	l.pos += runeWidth(l.text[l.pos:])
	l.emit(TokenOperator, start, l.pos)
}

func (l *lexer) numericIdentifierStart() bool {
	if l.options.Dialect != DialectHive && l.options.Dialect != DialectSpark && l.options.Dialect != DialectDatabricks {
		return false
	}
	index := l.pos
	for index < len(l.text) && isASCIIDigit(l.text[index]) {
		index++
	}
	if index >= len(l.text) || (l.text[index] != '_' && !isASCIILetter(l.text[index])) {
		return false
	}
	if l.text[index] == 'e' || l.text[index] == 'E' {
		exponent := index + 1
		if exponent < len(l.text) && (l.text[exponent] == '+' || l.text[exponent] == '-') {
			exponent++
		}
		if exponent < len(l.text) && isASCIIDigit(l.text[exponent]) {
			return false
		}
	}
	return true
}

func (l *lexer) emit(kind TokenKind, start, end int) {
	if len(l.tokens) >= l.options.MaxTokens {
		if !l.budgetHit {
			l.budgetHit = true
			l.diagnostics = append(l.diagnostics, Diagnostic{
				Severity: SeverityError,
				Code:     "GUARD_TOKEN_BUDGET_EXCEEDED",
				Message:  "maximum token budget exceeded",
				Span:     Span{Start: start, End: end},
				Found:    kind,
				Recovery: RecoverySynchronized,
			})
		}
		l.pos = len(l.text)
		return
	}
	l.tokens = append(l.tokens, Token{Kind: kind, Text: l.text[start:end], Span: Span{Start: start, End: end}})
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f'
}

func isSpaceAt(text string, pos int) bool {
	if pos < 0 || pos >= len(text) {
		return false
	}
	if c := text[pos]; c < utf8.RuneSelf {
		return isSpace(c)
	}
	r, _ := utf8.DecodeRuneInString(text[pos:])
	return unicode.IsSpace(r)
}

func isPunctuation(c byte) bool {
	switch c {
	case '(', ')', ',', '.', ';', '[', ']':
		return true
	default:
		return false
	}
}

func isOperatorChar(c byte) bool {
	switch c {
	case '+', '-', '*', '/', '%', '=', '<', '>', '!', '|', '&', '^', '~', '?', ':', '@', '#':
		return true
	default:
		return false
	}
}

func isASCIILetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

func isASCIIIdentifierPart(c byte) bool {
	return isASCIILetter(c) || isASCIIDigit(c) || c == '_' || c == '$' || c == '#' || c == '@'
}

func isIdentifierStartAt(text string, pos int) bool {
	if c := text[pos]; c < utf8.RuneSelf {
		return c == '_' || c == '$' || isASCIILetter(c)
	}
	r, _ := utf8.DecodeRuneInString(text[pos:])
	return r == '_' || r == '$' || unicode.IsLetter(r)
}

func isIdentifierContinueAt(text string, pos int) bool {
	if c := text[pos]; c < utf8.RuneSelf {
		return isASCIIIdentifierPart(c)
	}
	r, _ := utf8.DecodeRuneInString(text[pos:])
	return isIdentifierPart(r)
}

func isIdentifierPart(r rune) bool {
	return r == '_' || r == '$' || r == '#' || r == '@' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}

func runeWidth(text string) int {
	if text != "" && text[0] < utf8.RuneSelf {
		return 1
	}
	_, size := utf8.DecodeRuneInString(text)
	if size == 0 {
		return 1
	}
	return size
}
