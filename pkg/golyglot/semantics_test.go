package golyglot

import "testing"

func TestParseDataTypeKeepsLogicalStructure(t *testing.T) {
	tests := []struct {
		input   string
		dialect Dialect
		kind    DataTypeKind
		wantSQL string
	}{
		{input: "NUMERIC(10, 2)", dialect: DialectDuckDB, kind: DataTypeDecimal, wantSQL: "DECIMAL(10, 2)"},
		{input: "TIMESTAMPTZ", dialect: DialectPostgreSQL, kind: DataTypeTimestamp, wantSQL: "TIMESTAMP WITH TIME ZONE"},
		{input: "INT64", dialect: DialectBigQuery, kind: DataTypeBigInt, wantSQL: "BIGINT"},
		{input: "ARRAY<VARCHAR(20)>", dialect: DialectDuckDB, kind: DataTypeArray, wantSQL: "ARRAY<VARCHAR(20)>"},
		{input: "MAP<VARCHAR, BIGINT>", dialect: DialectTrino, kind: DataTypeMap, wantSQL: "MAP<VARCHAR, BIGINT>"},
		{input: "STRUCT<id BIGINT, label VARCHAR>", dialect: DialectBigQuery, kind: DataTypeStruct, wantSQL: "STRUCT<id BIGINT, label VARCHAR>"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := ParseDataType(test.input, test.dialect)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != test.kind || got.SQL() != test.wantSQL {
				t.Fatalf("ParseDataType(%q) = %#v / %q, want kind %s / %q", test.input, got, got.SQL(), test.kind, test.wantSQL)
			}
		})
	}
}

func TestParseDataTypeRejectsTrailingSQL(t *testing.T) {
	if _, err := ParseDataType("DECIMAL(10, 2) SELECT 1", DialectDuckDB); err == nil {
		t.Fatal("ParseDataType accepted trailing SQL")
	}
}

func TestParseDataTypeRejectsUnterminatedQuotedToken(t *testing.T) {
	if _, err := ParseDataType("'", DialectDuckDB); err == nil {
		t.Fatal("expected an unterminated quote error")
	}
}

func TestAnalyzeQueryInfersSchemaTypesAndNullability(t *testing.T) {
	notNullable := false
	schema := ValidationSchema{Tables: []SchemaTable{
		{Name: "orders", Columns: []SchemaColumn{{Name: "id", Type: "BIGINT", Nullable: &notNullable}, {Name: "amount", Type: "DECIMAL(10,2)", Nullable: &notNullable}, {Name: "user_id", Type: "BIGINT", Nullable: &notNullable}}},
		{Name: "users", Columns: []SchemaColumn{{Name: "id", Type: "BIGINT", Nullable: &notNullable}, {Name: "name", Type: "VARCHAR", Nullable: &notNullable}}},
	}}
	analysis, err := AnalyzeQuery(
		"SELECT o.id, o.amount + 1 AS gross, u.name FROM orders o LEFT JOIN users u ON o.user_id = u.id",
		AnalyzeQueryOptions{Dialect: DialectDuckDB, Schema: &schema},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.OutputNamesComplete || !analysis.OutputTypesComplete || len(analysis.OutputColumns) != 3 {
		t.Fatalf("analysis completeness/output = %#v", analysis)
	}
	if got := *analysis.OutputColumns[0].TypeHint; got != "BIGINT" {
		t.Fatalf("id type = %q", got)
	}
	if got := *analysis.OutputColumns[1].TypeHint; got != "DECIMAL(10, 2)" {
		t.Fatalf("gross type = %q", got)
	}
	if analysis.OutputColumns[0].Nullability != nullabilityNonNull || analysis.OutputColumns[2].Nullability != nullabilityNullable {
		t.Fatalf("outer join nullability = %#v", analysis.OutputColumns)
	}
}

func TestAnalyzeQueryInfersCTEStarsAndDuckDBRangeArithmetic(t *testing.T) {
	schema := ValidationSchema{Tables: []SchemaTable{{Name: "orders", Columns: []SchemaColumn{{Name: "id", Type: "INTEGER"}, {Name: "amount", Type: "BIGINT"}}}}}
	analysis, err := AnalyzeQuery(
		"WITH base AS (SELECT * FROM orders) SELECT * FROM base",
		AnalyzeQueryOptions{Dialect: DialectDuckDB, Schema: &schema},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.OutputNamesComplete || !analysis.OutputTypesComplete || len(analysis.OutputColumns) != 2 {
		t.Fatalf("CTE star analysis = %#v", analysis)
	}

	rangeAnalysis, err := AnalyzeQuery(
		"SELECT range, range * 2 AS double_range FROM range(1, 2, 1)",
		AnalyzeQueryOptions{Dialect: DialectDuckDB},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(rangeAnalysis.OutputColumns) != 2 || rangeAnalysis.OutputColumns[1].TypeHint == nil || *rangeAnalysis.OutputColumns[1].TypeHint != "BIGINT" {
		t.Fatalf("range arithmetic analysis = %#v", rangeAnalysis)
	}
}

func TestAnalyzeQueryInfersDuckDBRangeUnnestAndTemporalArithmetic(t *testing.T) {
	analysis, err := AnalyzeQuery(
		`SELECT event_id, current_date - 2 AS event_date, current_timestamp - (event_id * INTERVAL '10 minutes') AS observed_at FROM (SELECT unnest(range(1, 3)) AS event_id) AS events`,
		AnalyzeQueryOptions{Dialect: DialectDuckDB},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"BIGINT", "DATE", "TIMESTAMP"}
	if len(analysis.OutputColumns) != len(want) {
		t.Fatalf("output = %#v", analysis.OutputColumns)
	}
	for index, expected := range want {
		if analysis.OutputColumns[index].TypeHint == nil || *analysis.OutputColumns[index].TypeHint != expected {
			actual := "<unknown>"
			if analysis.OutputColumns[index].TypeHint != nil {
				actual = *analysis.OutputColumns[index].TypeHint
			}
			t.Fatalf("output[%d] = %s (%#v), want %s", index, actual, analysis.OutputColumns[index], expected)
		}
	}
	validation := ValidateWithOptions(
		`SELECT current_timestamp - (event_id * INTERVAL '10 minutes') FROM (SELECT unnest(range(1, 3)) AS event_id) AS events`,
		ValidationOptions{Dialect: DialectDuckDB, Semantic: true},
	)
	if hasValidationCode(validation.Errors, "E210") {
		t.Fatalf("interval arithmetic validation = %#v", validation.Errors)
	}
}

func TestAnalyzeQueryKeepsIntegerTypeAcrossSetBranches(t *testing.T) {
	const query = "SELECT 1 AS step_order UNION ALL SELECT 2"
	parsed, err := ParseStrict(query, DialectDuckDB)
	if err != nil {
		t.Fatal(err)
	}
	selectStmt := parsed.Statements[0].Node.(*SelectStmt)
	left := analyzeSelectSemanticsWithoutSet(selectStmt, AnalyzeQueryOptions{Dialect: DialectDuckDB}, nil)
	right := analyzeSelectSemantics(selectStmt.SetRight, AnalyzeQueryOptions{Dialect: DialectDuckDB}, nil)
	if len(left.output) != 1 || len(right.output) != 1 {
		t.Fatalf("set branch outputs: left=%#v right=%#v", left.output, right.output)
	}
	if left.output[0].dataType.Kind != DataTypeInteger || right.output[0].dataType.Kind != DataTypeInteger {
		t.Fatalf("set branch types: left=%#v right=%#v", left.output[0].dataType, right.output[0].dataType)
	}
	analysis, err := AnalyzeQuery(query, AnalyzeQueryOptions{Dialect: DialectDuckDB})
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.OutputColumns) != 1 || analysis.OutputColumns[0].TypeHint == nil || *analysis.OutputColumns[0].TypeHint != "INTEGER" {
		t.Fatalf("set output = %#v", analysis.OutputColumns)
	}
}

func TestValidateWithSchemaReportsArithmeticTypeMismatch(t *testing.T) {
	strict := true
	schema := ValidationSchema{Strict: &strict, Tables: []SchemaTable{{Name: "values_table", Columns: []SchemaColumn{{Name: "id", Type: "INTEGER"}}}}}
	result := ValidateWithSchema("SELECT id + 'not a number' FROM values_table", schema, DialectDuckDB)
	if result.Valid || !hasValidationCode(result.Errors, "E210") {
		t.Fatalf("validation result = %#v", result)
	}
}

func TestValidateReportsLiteralArithmeticTypeMismatchWithoutSchema(t *testing.T) {
	result := Validate("SELECT 1 + 'not a number'", DialectDuckDB)
	if result.Valid || !hasValidationCode(result.Errors, "E210") {
		t.Fatalf("validation result = %#v", result)
	}
}
