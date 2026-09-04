package typeoracle

import (
	"reflect"
	"sort"
	"testing"

	"github.com/renart-data/golyglot/pkg/golyglot"
)

func TestCompareClassifiesNormalizedMatchesAndMismatches(t *testing.T) {
	tests := []struct {
		name     string
		inferred []Column
		observed []Column
		want     Status
	}{
		{
			name:     "normalized match",
			inferred: []Column{{Name: "value", Type: "INTEGER"}},
			observed: []Column{{Name: "value", Type: "INT"}},
			want:     StatusMatch,
		},
		{
			name:     "kind mismatch",
			inferred: []Column{{Name: "value", Type: "BIGINT"}},
			observed: []Column{{Name: "value", Type: "HUGEINT"}},
			want:     StatusTypeMismatch,
		},
		{
			name:     "modifier mismatch",
			inferred: []Column{{Name: "value", Type: "DECIMAL(18, 2)"}},
			observed: []Column{{Name: "value", Type: "DECIMAL(38, 2)"}},
			want:     StatusModifierMismatch,
		},
		{
			name:     "unknown inference",
			inferred: []Column{{Name: "value"}},
			observed: []Column{{Name: "value", Type: "INTEGER"}},
			want:     StatusGolyglotUnknown,
		},
		{
			name:     "shape mismatch",
			inferred: []Column{{Name: "value", Type: "INTEGER"}},
			observed: []Column{{Name: "other", Type: "INTEGER"}},
			want:     StatusShapeMismatch,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Compare(QueryCase{ID: test.name, Dialect: golyglot.DialectDuckDB}, test.inferred, test.observed)
			if got.Status != test.want {
				t.Fatalf("status = %q (%s), want %q", got.Status, got.Detail, test.want)
			}
		})
	}
}

func TestGenerateCasesIsSeededAndBounded(t *testing.T) {
	first := GenerateCases(42, 12)
	second := GenerateCases(42, 12)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same seed produced different cases")
	}
	if len(first) != 12 {
		t.Fatalf("case count = %d, want 12", len(first))
	}
	seen := make(map[string]bool)
	for _, testCase := range first {
		if testCase.ID == "" || testCase.SQL == "" || testCase.Schema == nil || testCase.GeneratorVersion != GeneratorVersion || testCase.Source != CaseSourceGenerated {
			t.Fatalf("incomplete case = %#v", testCase)
		}
		if len(testCase.Features) == 0 || !sort.StringsAreSorted(testCase.Features) {
			t.Fatalf("features are missing or unstable = %#v", testCase.Features)
		}
		if seen[testCase.SQL] {
			t.Fatalf("duplicate SQL = %q", testCase.SQL)
		}
		seen[testCase.SQL] = true
	}
}

func TestCompareCarriesReproductionMetadata(t *testing.T) {
	testCase := GenerateCases(42, 1)[0]
	result := Compare(testCase, []Column{{Name: "value", Type: "INTEGER"}}, []Column{{Name: "value", Type: "DOUBLE"}})
	if result.Dialect != golyglot.DialectDuckDB || result.Schema == nil || result.Source != CaseSourceGenerated {
		t.Fatalf("result metadata = %#v", result)
	}
	if !reflect.DeepEqual(result.Features, testCase.Features) || result.GeneratorVersion != GeneratorVersion {
		t.Fatalf("result reproduction metadata = %#v", result)
	}
}

func TestReduceCaseKeepsOnlyReferencedSchemaColumns(t *testing.T) {
	testCase := QueryCase{
		ID: "reduce", SQL: "SELECT double_col + decimal_col AS value FROM oracle_values",
		Dialect: golyglot.DialectDuckDB, Schema: oracleSchema(),
	}
	reduced, err := ReduceCase(testCase)
	if err != nil {
		t.Fatal(err)
	}
	if len(reduced.Schema.Tables) != 1 {
		t.Fatalf("tables = %#v", reduced.Schema.Tables)
	}
	columns := reduced.Schema.Tables[0].Columns
	if len(columns) != 2 || columns[0].Name != "double_col" || columns[1].Name != "decimal_col" {
		t.Fatalf("reduced columns = %#v", columns)
	}
}

func TestCuratedCasesEncodeKnownDuckDBFindings(t *testing.T) {
	cases, err := CuratedCases()
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 6 {
		t.Fatalf("curated case count = %d, want 6", len(cases))
	}
	for _, testCase := range cases {
		if testCase.Source != CaseSourceCurated || testCase.Expected == nil || testCase.Expected.Engine != "duckdb" || len(testCase.Expected.Observed) != 1 {
			t.Fatalf("incomplete curated case = %#v", testCase)
		}
		if len(testCase.Features) == 0 || !sort.StringsAreSorted(testCase.Features) {
			t.Fatalf("curated features = %#v", testCase.Features)
		}
	}
}

func TestEvaluateExpectationUsesPinnedEngineVersion(t *testing.T) {
	testCase := QueryCase{Expected: &ExpectedOutcome{
		Engine: "duckdb", EngineVersionPrefix: "v1.5.", Status: StatusTypeMismatch,
		Observed: []Column{{Name: "value", Type: "HUGEINT"}},
	}}
	result := Result{Status: StatusTypeMismatch, Observed: []Column{{Name: "value", Type: "HUGEINT"}}}
	matched := EvaluateExpectation(result, testCase, "duckdb", "v1.5.1")
	if matched == nil || !matched.Applicable || !matched.Matched {
		t.Fatalf("expectation = %#v", matched)
	}
	notApplicable := EvaluateExpectation(result, testCase, "duckdb", "v1.6.0")
	if notApplicable == nil || notApplicable.Applicable {
		t.Fatalf("version drift expectation = %#v", notApplicable)
	}
}

func TestNewFindingContainsReducedReproduction(t *testing.T) {
	testCase := QueryCase{
		ID: "sum-integer", SQL: "SELECT SUM(integer_col) AS value FROM oracle_values",
		Dialect: golyglot.DialectDuckDB, Schema: oracleSchema(),
		GeneratorVersion: GeneratorVersion, Source: CaseSourceGenerated,
		Features: []string{"aggregate", "aggregate:sum", "input:integer"},
	}
	result := Compare(testCase,
		[]Column{{Name: "value", Type: "BIGINT"}},
		[]Column{{Name: "value", Type: "HUGEINT"}},
	)
	finding, err := NewFinding(42, "duckdb", "v1.5.1", testCase, result)
	if err != nil {
		t.Fatal(err)
	}
	if finding.Version != FindingVersion || finding.Seed != 42 || finding.EngineVersion != "v1.5.1" {
		t.Fatalf("finding metadata = %#v", finding)
	}
	columns := finding.Case.Schema.Tables[0].Columns
	if len(columns) != 1 || columns[0].Name != "integer_col" {
		t.Fatalf("finding schema was not reduced: %#v", columns)
	}
	if !reflect.DeepEqual(finding.Result.Schema, finding.Case.Schema) {
		t.Fatalf("result schema does not match reduced case: %#v", finding)
	}
}

func TestInferUsesGolyglotOutputTypes(t *testing.T) {
	columns, err := Infer(QueryCase{
		ID:      "sum-integer",
		SQL:     "SELECT SUM(integer_col) AS value FROM oracle_values",
		Dialect: golyglot.DialectDuckDB,
		Schema: &golyglot.ValidationSchema{Tables: []golyglot.SchemaTable{{
			Name: "oracle_values", Columns: []golyglot.SchemaColumn{{Name: "integer_col", Type: "INTEGER"}},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(columns) != 1 || columns[0].Name != "value" || columns[0].Type != "HUGEINT" {
		t.Fatalf("columns = %#v", columns)
	}
}
