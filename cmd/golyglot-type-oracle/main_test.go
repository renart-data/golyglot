package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/renart-data/golyglot/internal/typeoracle"
)

func TestSummarizeResults(t *testing.T) {
	summary := summarizeResults([]typeoracle.Result{
		{Status: typeoracle.StatusMatch, Features: []string{"aggregate", "sum"}},
		{Status: typeoracle.StatusTypeMismatch, Features: []string{"aggregate", "sum"}},
		{Status: typeoracle.StatusModifierMismatch, Features: []string{"aggregate", "sum"}},
		{Status: typeoracle.StatusGolyglotUnknown, Features: []string{"case"}},
		{Status: typeoracle.StatusGolyglotCrash, Features: []string{"case"}},
	})
	if summary.Total != 5 || summary.Matches != 1 || summary.Mismatches != 2 || summary.Unknown != 1 || summary.Errors != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	if got := summary.ByFeature["sum"]; got.Total != 3 || got.Matches != 1 || got.Mismatches != 2 {
		t.Fatalf("sum feature summary = %#v", got)
	}
	if got := summary.ByFeature["case"]; got.Total != 2 || got.Unknown != 1 || got.Errors != 1 {
		t.Fatalf("case feature summary = %#v", got)
	}
}

func TestWriteFindingsWritesOnlyReducedNonMatches(t *testing.T) {
	cases := typeoracle.GenerateCases(42, 2)
	results := []typeoracle.Result{
		typeoracle.Compare(cases[0], []typeoracle.Column{{Name: "value", Type: "INTEGER"}}, []typeoracle.Column{{Name: "value", Type: "INTEGER"}}),
		typeoracle.Compare(cases[1], []typeoracle.Column{{Name: "value", Type: "BIGINT"}}, []typeoracle.Column{{Name: "value", Type: "HUGEINT"}}),
	}
	report := oracleReport{
		Seed: 42, GeneratorVersion: typeoracle.GeneratorVersion,
		Engine: "duckdb", EngineVersion: "v1.5.1", Results: results,
	}
	path := filepath.Join(t.TempDir(), "nested", "findings.json")
	if err := writeFindings(path, report, cases); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bundle findingBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Version != typeoracle.FindingVersion || len(bundle.Findings) != 1 {
		t.Fatalf("bundle = %#v", bundle)
	}
	if got := bundle.Findings[0].Case.Schema.Tables[0].Columns; len(got) >= len(cases[1].Schema.Tables[0].Columns) {
		t.Fatalf("finding schema was not reduced: %d >= %d", len(got), len(cases[1].Schema.Tables[0].Columns))
	}
}

func TestSummarizeResultsTreatsStaleApplicableExpectationAsUnexpected(t *testing.T) {
	summary := summarizeResults([]typeoracle.Result{{
		Status: typeoracle.StatusMatch,
		Expectation: &typeoracle.ExpectationEvaluation{
			Applicable: true,
			Matched:    false,
			Detail:     "status=match, expected=type_mismatch",
		},
	}})
	if summary.Unexpected != 1 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestWriteFindingsIncludesExpectationDriftOnMatch(t *testing.T) {
	cases := typeoracle.GenerateCases(42, 1)
	result := typeoracle.Compare(
		cases[0],
		[]typeoracle.Column{{Name: "value", Type: "INTEGER"}},
		[]typeoracle.Column{{Name: "value", Type: "INTEGER"}},
	)
	result.Expectation = &typeoracle.ExpectationEvaluation{
		Applicable: true,
		Matched:    false,
		Detail:     "status=match, expected=type_mismatch",
	}
	report := oracleReport{
		Seed: 42, GeneratorVersion: typeoracle.GeneratorVersion,
		Engine: "duckdb", EngineVersion: "v1.5.1", Results: []typeoracle.Result{result},
	}
	path := filepath.Join(t.TempDir(), "findings.json")
	if err := writeFindings(path, report, cases); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bundle findingBundle
	if err := json.Unmarshal(data, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Findings) != 1 {
		t.Fatalf("findings = %#v", bundle.Findings)
	}
}
