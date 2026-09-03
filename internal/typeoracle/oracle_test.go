package typeoracle

import (
	"reflect"
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
		if testCase.ID == "" || testCase.SQL == "" || testCase.Schema == nil {
			t.Fatalf("incomplete case = %#v", testCase)
		}
		if seen[testCase.SQL] {
			t.Fatalf("duplicate SQL = %q", testCase.SQL)
		}
		seen[testCase.SQL] = true
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
	if len(columns) != 1 || columns[0].Name != "value" || columns[0].Type != "BIGINT" {
		t.Fatalf("columns = %#v", columns)
	}
}
