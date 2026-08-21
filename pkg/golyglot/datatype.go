package golyglot

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// DataTypeKind is the canonical logical family of a SQL data type. Dialect
// spellings such as INT64, INTEGER, and INT are normalized to the same kind,
// while precision, scale, length, timezone, and nested type structure remain
// available for strict comparisons.
type DataTypeKind string

const (
	DataTypeUnknown   DataTypeKind = "unknown"
	DataTypeBoolean   DataTypeKind = "boolean"
	DataTypeTinyInt   DataTypeKind = "tinyint"
	DataTypeSmallInt  DataTypeKind = "smallint"
	DataTypeInteger   DataTypeKind = "integer"
	DataTypeBigInt    DataTypeKind = "bigint"
	DataTypeHugeInt   DataTypeKind = "hugeint"
	DataTypeFloat     DataTypeKind = "float"
	DataTypeDouble    DataTypeKind = "double"
	DataTypeDecimal   DataTypeKind = "decimal"
	DataTypeString    DataTypeKind = "string"
	DataTypeBinary    DataTypeKind = "binary"
	DataTypeBit       DataTypeKind = "bit"
	DataTypeDate      DataTypeKind = "date"
	DataTypeTime      DataTypeKind = "time"
	DataTypeTimestamp DataTypeKind = "timestamp"
	DataTypeInterval  DataTypeKind = "interval"
	DataTypeJSON      DataTypeKind = "json"
	DataTypeUUID      DataTypeKind = "uuid"
	DataTypeArray     DataTypeKind = "array"
	DataTypeList      DataTypeKind = "list"
	DataTypeStruct    DataTypeKind = "struct"
	DataTypeMap       DataTypeKind = "map"
	DataTypeGeometry  DataTypeKind = "geometry"
	DataTypeGeography DataTypeKind = "geography"
	DataTypeCustom    DataTypeKind = "custom"
)

// DataType is a dependency-free, dialect-normalized SQL type. Pointer fields
// distinguish an omitted modifier from an explicit zero and make it possible
// for callers to choose either wildcard-compatible or strict comparison.
type DataType struct {
	Kind         DataTypeKind    `json:"kind"`
	Name         string          `json:"name,omitempty"`
	Length       *int            `json:"length,omitempty"`
	Precision    *int            `json:"precision,omitempty"`
	Scale        *int            `json:"scale,omitempty"`
	WithTimezone bool            `json:"withTimezone,omitempty"`
	Element      *DataType       `json:"element,omitempty"`
	Key          *DataType       `json:"key,omitempty"`
	Value        *DataType       `json:"value,omitempty"`
	Fields       []DataTypeField `json:"fields,omitempty"`
	Arguments    []string        `json:"arguments,omitempty"`
}

type DataTypeField struct {
	Name string   `json:"name"`
	Type DataType `json:"type"`
}

func (t DataType) Known() bool { return t.Kind != "" && t.Kind != DataTypeUnknown }

// SQL renders the normalized logical type using stable, conventional SQL.
func (t DataType) SQL() string {
	base := ""
	switch t.Kind {
	case DataTypeBoolean:
		base = "BOOLEAN"
	case DataTypeTinyInt:
		base = "TINYINT"
	case DataTypeSmallInt:
		base = "SMALLINT"
	case DataTypeInteger:
		base = "INTEGER"
	case DataTypeBigInt:
		base = "BIGINT"
	case DataTypeHugeInt:
		base = "HUGEINT"
	case DataTypeFloat:
		base = "FLOAT"
	case DataTypeDouble:
		base = "DOUBLE"
	case DataTypeDecimal:
		base = "DECIMAL"
	case DataTypeString:
		base = "VARCHAR"
	case DataTypeBinary:
		base = "BINARY"
	case DataTypeBit:
		base = "BIT"
	case DataTypeDate:
		base = "DATE"
	case DataTypeTime:
		base = "TIME"
	case DataTypeTimestamp:
		base = "TIMESTAMP"
	case DataTypeInterval:
		base = "INTERVAL"
	case DataTypeJSON:
		base = "JSON"
	case DataTypeUUID:
		base = "UUID"
	case DataTypeGeometry:
		base = "GEOMETRY"
	case DataTypeGeography:
		base = "GEOGRAPHY"
	case DataTypeArray:
		if t.Element == nil {
			return "ARRAY"
		}
		return "ARRAY<" + t.Element.SQL() + ">"
	case DataTypeList:
		if t.Element == nil {
			return "LIST"
		}
		return "LIST<" + t.Element.SQL() + ">"
	case DataTypeMap:
		if t.Key == nil || t.Value == nil {
			return "MAP"
		}
		return "MAP<" + t.Key.SQL() + ", " + t.Value.SQL() + ">"
	case DataTypeStruct:
		fields := make([]string, 0, len(t.Fields))
		for _, field := range t.Fields {
			part := field.Type.SQL()
			if field.Name != "" {
				part = field.Name + " " + part
			}
			fields = append(fields, part)
		}
		return "STRUCT<" + strings.Join(fields, ", ") + ">"
	case DataTypeCustom:
		base = strings.ToUpper(strings.TrimSpace(t.Name))
	default:
		return ""
	}

	parameters := make([]string, 0, 2+len(t.Arguments))
	if t.Precision != nil {
		parameters = append(parameters, strconv.Itoa(*t.Precision))
		if t.Scale != nil {
			parameters = append(parameters, strconv.Itoa(*t.Scale))
		}
	} else if t.Length != nil {
		parameters = append(parameters, strconv.Itoa(*t.Length))
	}
	parameters = append(parameters, t.Arguments...)
	if len(parameters) > 0 {
		base += "(" + strings.Join(parameters, ", ") + ")"
	}
	if (t.Kind == DataTypeTime || t.Kind == DataTypeTimestamp) && t.WithTimezone {
		base += " WITH TIME ZONE"
	}
	return base
}

func (t DataType) String() string { return t.SQL() }

// ParseDataType parses a standalone SQL type without accepting trailing SQL.
// It intentionally normalizes aliases while retaining modifiers and nested
// structure, making the result suitable for schema comparison and inference.
func ParseDataType(sql string, dialect Dialect) (DataType, error) {
	if _, err := dialect.normalized(); err != nil {
		return DataType{}, err
	}
	parser, err := newDataTypeParser(sql)
	if err != nil {
		return DataType{}, err
	}
	result, err := parser.parseType()
	if err != nil {
		return DataType{}, err
	}
	if parser.peek().kind != dataTypeTokenEOF {
		return DataType{}, fmt.Errorf("golyglot: unexpected token %q after data type", parser.peek().text)
	}
	return result, nil
}

type dataTypeTokenKind uint8

const (
	dataTypeTokenEOF dataTypeTokenKind = iota
	dataTypeTokenWord
	dataTypeTokenNumber
	dataTypeTokenString
	dataTypeTokenPunctuation
)

type dataTypeToken struct {
	kind dataTypeTokenKind
	text string
}

type dataTypeParser struct {
	tokens []dataTypeToken
	index  int
}

func newDataTypeParser(sql string) (*dataTypeParser, error) {
	tokens := make([]dataTypeToken, 0, 12)
	for index := 0; index < len(sql); {
		r := rune(sql[index])
		if unicode.IsSpace(r) {
			index++
			continue
		}
		if isDataTypeWordStart(sql[index]) {
			start := index
			index++
			for index < len(sql) && isDataTypeWordPart(sql[index]) {
				index++
			}
			tokens = append(tokens, dataTypeToken{kind: dataTypeTokenWord, text: sql[start:index]})
			continue
		}
		if sql[index] >= '0' && sql[index] <= '9' {
			start := index
			for index < len(sql) && sql[index] >= '0' && sql[index] <= '9' {
				index++
			}
			tokens = append(tokens, dataTypeToken{kind: dataTypeTokenNumber, text: sql[start:index]})
			continue
		}
		if sql[index] == '\'' || sql[index] == '"' || sql[index] == '`' {
			quote := sql[index]
			start := index
			closed := false
			index++
			for index < len(sql) {
				if sql[index] != quote {
					index++
					continue
				}
				if index+1 < len(sql) && sql[index+1] == quote {
					index += 2
					continue
				}
				index++
				closed = true
				break
			}
			if !closed {
				return nil, fmt.Errorf("golyglot: unterminated quoted data-type token")
			}
			tokens = append(tokens, dataTypeToken{kind: dataTypeTokenString, text: sql[start:index]})
			continue
		}
		switch sql[index] {
		case '(', ')', '<', '>', '[', ']', ',', '.':
			tokens = append(tokens, dataTypeToken{kind: dataTypeTokenPunctuation, text: sql[index : index+1]})
			index++
		default:
			return nil, fmt.Errorf("golyglot: unexpected character %q in data type", sql[index])
		}
	}
	tokens = append(tokens, dataTypeToken{kind: dataTypeTokenEOF})
	return &dataTypeParser{tokens: tokens}, nil
}

func isDataTypeWordStart(value byte) bool {
	return value == '_' || value == '$' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func isDataTypeWordPart(value byte) bool {
	return isDataTypeWordStart(value) || value >= '0' && value <= '9'
}

func (p *dataTypeParser) peek() dataTypeToken { return p.tokens[p.index] }

func (p *dataTypeParser) take() dataTypeToken {
	value := p.peek()
	if value.kind != dataTypeTokenEOF {
		p.index++
	}
	return value
}

func (p *dataTypeParser) match(text string) bool {
	if !strings.EqualFold(p.peek().text, text) {
		return false
	}
	p.take()
	return true
}

func (p *dataTypeParser) expect(text string) error {
	if !p.match(text) {
		return fmt.Errorf("golyglot: expected %q in data type, found %q", text, p.peek().text)
	}
	return nil
}

func (p *dataTypeParser) parseType() (DataType, error) {
	name, err := p.parseTypeName()
	if err != nil {
		return DataType{}, err
	}
	upper := strings.ToUpper(strings.Join(strings.Fields(name), " "))
	result := dataTypeForName(upper)

	if p.match("<") {
		if err := p.parseNestedTypeArguments(&result, ">"); err != nil {
			return DataType{}, err
		}
	} else if p.match("(") {
		if result.Kind == DataTypeStruct {
			if err := p.parseStructFields(&result, ")"); err != nil {
				return DataType{}, err
			}
		} else if result.Kind == DataTypeMap {
			if err := p.parseMapArguments(&result, ")"); err != nil {
				return DataType{}, err
			}
		} else if result.Kind == DataTypeArray || result.Kind == DataTypeList {
			element, parseErr := p.parseType()
			if parseErr != nil {
				return DataType{}, parseErr
			}
			result.Element = &element
			if err := p.expect(")"); err != nil {
				return DataType{}, err
			}
		} else {
			if err := p.parseScalarArguments(&result); err != nil {
				return DataType{}, err
			}
		}
	}

	if result.Kind == DataTypeTime || result.Kind == DataTypeTimestamp {
		if p.match("WITH") {
			if err := p.expect("TIME"); err != nil {
				return DataType{}, err
			}
			if err := p.expect("ZONE"); err != nil {
				return DataType{}, err
			}
			result.WithTimezone = true
		} else if p.match("WITHOUT") {
			if err := p.expect("TIME"); err != nil {
				return DataType{}, err
			}
			if err := p.expect("ZONE"); err != nil {
				return DataType{}, err
			}
		}
	}

	for {
		switch {
		case p.match("["):
			if p.peek().kind == dataTypeTokenNumber {
				p.take()
			}
			if err := p.expect("]"); err != nil {
				return DataType{}, err
			}
			element := result
			result = DataType{Kind: DataTypeArray, Element: &element}
		case p.match("ARRAY"):
			element := result
			result = DataType{Kind: DataTypeArray, Element: &element}
		case p.match("LIST"):
			element := result
			result = DataType{Kind: DataTypeList, Element: &element}
		default:
			return result, nil
		}
	}
}

func (p *dataTypeParser) parseTypeName() (string, error) {
	if p.peek().kind != dataTypeTokenWord && p.peek().kind != dataTypeTokenString {
		return "", fmt.Errorf("golyglot: expected data type, found %q", p.peek().text)
	}
	parts := []string{unquoteDataTypeName(p.take().text)}
	for p.match(".") {
		if p.peek().kind != dataTypeTokenWord && p.peek().kind != dataTypeTokenString {
			return "", fmt.Errorf("golyglot: expected name after '.' in data type")
		}
		parts = append(parts, ".", unquoteDataTypeName(p.take().text))
	}
	first := strings.ToUpper(strings.Join(parts, ""))
	switch first {
	case "DOUBLE":
		if p.match("PRECISION") {
			return "DOUBLE PRECISION", nil
		}
	case "CHARACTER":
		if p.match("VARYING") {
			return "CHARACTER VARYING", nil
		}
	case "NATIONAL":
		if p.match("CHARACTER") {
			if p.match("VARYING") {
				return "NATIONAL CHARACTER VARYING", nil
			}
			return "NATIONAL CHARACTER", nil
		}
	case "TIMESTAMP", "TIME":
		// WITH/WITHOUT TIME ZONE is parsed after optional precision.
	case "LONG":
		if p.match("VARCHAR") {
			return "LONG VARCHAR", nil
		}
	}
	return first, nil
}

func unquoteDataTypeName(value string) string {
	if len(value) >= 2 && (value[0] == '"' || value[0] == '`') && value[len(value)-1] == value[0] {
		return strings.ReplaceAll(value[1:len(value)-1], string([]byte{value[0], value[0]}), string(value[0]))
	}
	return value
}

func dataTypeForName(name string) DataType {
	normalized := strings.NewReplacer("_", " ", "-", " ").Replace(strings.ToUpper(strings.TrimSpace(name)))
	normalized = strings.Join(strings.Fields(normalized), " ")
	switch normalized {
	case "BOOL", "BOOLEAN":
		return DataType{Kind: DataTypeBoolean}
	case "TINYINT", "INT8 T", "BYTEINT":
		return DataType{Kind: DataTypeTinyInt}
	case "SMALLINT", "INT2", "INT16":
		return DataType{Kind: DataTypeSmallInt}
	case "INT", "INT4", "INT32", "INTEGER", "MEDIUMINT":
		return DataType{Kind: DataTypeInteger}
	case "BIGINT", "BIG INT", "INT8", "INT64", "LONG":
		return DataType{Kind: DataTypeBigInt}
	case "HUGEINT", "INT128", "UHUGEINT":
		return DataType{Kind: DataTypeHugeInt}
	case "REAL", "FLOAT", "FLOAT4", "FLOAT32", "BINARY FLOAT":
		return DataType{Kind: DataTypeFloat}
	case "DOUBLE", "DOUBLE PRECISION", "FLOAT8", "FLOAT64", "BINARY DOUBLE":
		return DataType{Kind: DataTypeDouble}
	case "DEC", "DECIMAL", "NUMERIC", "NUMBER", "BIGNUMERIC", "BIGDECIMAL":
		return DataType{Kind: DataTypeDecimal}
	case "CHAR", "CHARACTER", "CHARACTER VARYING", "CLOB", "LONG VARCHAR", "NATIONAL CHARACTER", "NATIONAL CHARACTER VARYING", "NCHAR", "NTEXT", "NVARCHAR", "NVARCHAR2", "STRING", "TEXT", "VARCHAR", "VARCHAR2":
		return DataType{Kind: DataTypeString}
	case "BINARY", "BLOB", "BYTEA", "BYTES", "IMAGE", "VARBINARY":
		return DataType{Kind: DataTypeBinary}
	case "BIT", "VARBIT", "BIT VARYING":
		return DataType{Kind: DataTypeBit}
	case "DATE":
		return DataType{Kind: DataTypeDate}
	case "TIME":
		return DataType{Kind: DataTypeTime}
	case "TIMESTAMP", "DATETIME", "DATETIME2", "SMALLDATETIME":
		return DataType{Kind: DataTypeTimestamp}
	case "TIMESTAMPTZ", "TIMESTAMP TZ", "DATETIMEOFFSET":
		return DataType{Kind: DataTypeTimestamp, WithTimezone: true}
	case "TIMETZ":
		return DataType{Kind: DataTypeTime, WithTimezone: true}
	case "INTERVAL":
		return DataType{Kind: DataTypeInterval}
	case "JSON", "JSONB", "OBJECT", "VARIANT":
		return DataType{Kind: DataTypeJSON}
	case "UUID", "UNIQUEIDENTIFIER":
		return DataType{Kind: DataTypeUUID}
	case "ARRAY":
		return DataType{Kind: DataTypeArray}
	case "LIST":
		return DataType{Kind: DataTypeList}
	case "STRUCT", "ROW", "TUPLE":
		return DataType{Kind: DataTypeStruct}
	case "MAP":
		return DataType{Kind: DataTypeMap}
	case "GEOMETRY":
		return DataType{Kind: DataTypeGeometry}
	case "GEOGRAPHY":
		return DataType{Kind: DataTypeGeography}
	default:
		return DataType{Kind: DataTypeCustom, Name: name}
	}
}

func (p *dataTypeParser) parseScalarArguments(result *DataType) error {
	arguments := make([]string, 0, 2)
	for !p.match(")") {
		if p.peek().kind == dataTypeTokenEOF {
			return fmt.Errorf("golyglot: unterminated data-type arguments")
		}
		arguments = append(arguments, p.take().text)
		if p.match(")") {
			break
		}
		if err := p.expect(","); err != nil {
			return err
		}
	}
	parseNumber := func(index int) *int {
		if index >= len(arguments) {
			return nil
		}
		value, err := strconv.Atoi(arguments[index])
		if err != nil {
			return nil
		}
		return &value
	}
	switch result.Kind {
	case DataTypeDecimal, DataTypeFloat, DataTypeDouble:
		result.Precision = parseNumber(0)
		result.Scale = parseNumber(1)
	case DataTypeString, DataTypeBinary, DataTypeBit, DataTypeTinyInt, DataTypeSmallInt, DataTypeInteger, DataTypeBigInt, DataTypeTime, DataTypeTimestamp:
		result.Length = parseNumber(0)
		if result.Kind == DataTypeTime || result.Kind == DataTypeTimestamp {
			result.Precision = result.Length
			result.Length = nil
		}
	default:
		result.Arguments = arguments
	}
	for _, argument := range arguments {
		if _, err := strconv.Atoi(argument); err != nil {
			result.Arguments = append([]string(nil), arguments...)
			break
		}
	}
	return nil
}

func (p *dataTypeParser) parseNestedTypeArguments(result *DataType, close string) error {
	switch result.Kind {
	case DataTypeArray, DataTypeList:
		element, err := p.parseType()
		if err != nil {
			return err
		}
		result.Element = &element
		return p.expect(close)
	case DataTypeMap:
		return p.parseMapArguments(result, close)
	case DataTypeStruct:
		return p.parseStructFields(result, close)
	default:
		return fmt.Errorf("golyglot: type %s does not accept nested type arguments", result.SQL())
	}
}

func (p *dataTypeParser) parseMapArguments(result *DataType, close string) error {
	key, err := p.parseType()
	if err != nil {
		return err
	}
	if err := p.expect(","); err != nil {
		return err
	}
	value, err := p.parseType()
	if err != nil {
		return err
	}
	if err := p.expect(close); err != nil {
		return err
	}
	result.Key = &key
	result.Value = &value
	return nil
}

func (p *dataTypeParser) parseStructFields(result *DataType, close string) error {
	for !p.match(close) {
		if p.peek().kind == dataTypeTokenEOF {
			return fmt.Errorf("golyglot: unterminated struct type")
		}
		fieldName := ""
		start := p.index
		if p.peek().kind == dataTypeTokenWord || p.peek().kind == dataTypeTokenString {
			fieldName = unquoteDataTypeName(p.take().text)
		}
		fieldType, err := p.parseType()
		if err != nil {
			p.index = start
			fieldName = ""
			fieldType, err = p.parseType()
		}
		if err != nil {
			return err
		}
		result.Fields = append(result.Fields, DataTypeField{Name: fieldName, Type: fieldType})
		if p.match(close) {
			break
		}
		if err := p.expect(","); err != nil {
			return err
		}
	}
	return nil
}
