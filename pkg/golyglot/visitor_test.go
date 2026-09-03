package golyglot

import "testing"

func TestColumnsIgnoresIntervalUnits(t *testing.T) {
	parsed, err := ParseStrict("SELECT INTERVAL 1 HOUR AS bucket", DialectDuckDB)
	if err != nil {
		t.Fatal(err)
	}
	query, ok := parsed.Statements[0].Node.(*SelectStmt)
	if !ok || len(query.Projections) != 1 {
		t.Fatalf("query = %#v", parsed.Statements[0].Node)
	}

	if got := Columns(query.Projections[0].Expr); len(got) != 0 {
		t.Fatalf("interval columns = %#v, want none", got)
	}
}

func TestAnalyzeQueryHandlesIntervalArgumentInTableFunction(t *testing.T) {
	analysis, err := AnalyzeQuery(
		`select range as time from range(
			timestamp '2026-09-04 00:00:00',
			timestamp '2026-09-04 01:00:00',
			interval 1 hour
		)`,
		AnalyzeQueryOptions{Dialect: DialectDuckDB},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Projections) != 1 {
		t.Fatalf("projections = %#v", analysis.Projections)
	}
	for _, upstream := range analysis.Projections[0].Upstream {
		if upstream.Column == "HOUR" {
			t.Fatalf("interval unit leaked into lineage: %#v", analysis.Projections[0].Upstream)
		}
	}
}

func TestAnalyzeQueryBreaksTableFunctionLineageCycles(t *testing.T) {
	analysis, err := AnalyzeQuery(
		"SELECT range FROM range(start_at)",
		AnalyzeQueryOptions{Dialect: DialectDuckDB},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.Projections) != 1 || len(analysis.Projections[0].Upstream) != 1 {
		t.Fatalf("projections = %#v", analysis.Projections)
	}
	upstream := analysis.Projections[0].Upstream[0]
	if upstream.Column != "start_at" || upstream.SourceKind != "unknown" {
		t.Fatalf("cycle fallback = %#v, want unknown start_at", upstream)
	}
}
