package golyglot

import "testing"

func TestDiffQuerySemanticsFindsPropagatedTypeChange(t *testing.T) {
	query := "SELECT SUM(total_amount) AS total FROM lineitems"
	diff, err := DiffQuerySemantics(query, query, QuerySemanticDiffOptions{
		Dialect:      DialectDuckDB,
		BeforeSchema: semanticDiffSchema("INTEGER"),
		AfterSchema:  semanticDiffSchema("DOUBLE"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !diff.SourceEqual || !diff.CanonicalEqual || !diff.Complete {
		t.Fatalf("identity/completeness = %#v", diff)
	}
	if len(diff.InputChanges) != 1 {
		t.Fatalf("input changes = %#v", diff.InputChanges)
	}
	input := diff.InputChanges[0]
	if input.Table != "lineitems" || input.Column != "total_amount" || !input.TypeChanged {
		t.Fatalf("input change = %#v", input)
	}
	if semanticDiffType(input.Before) != "INTEGER" || semanticDiffType(input.After) != "DOUBLE" {
		t.Fatalf("input contract = %#v -> %#v", input.Before, input.After)
	}
	if len(diff.OutputChanges) != 1 {
		t.Fatalf("output changes = %#v", diff.OutputChanges)
	}
	output := diff.OutputChanges[0]
	if !output.TypeChanged || output.Origin != SemanticChangePropagated {
		t.Fatalf("output change = %#v", output)
	}
	if output.Before == nil || output.After == nil || semanticDiffOutputType(*output.Before) != "BIGINT" || semanticDiffOutputType(*output.After) != "DOUBLE" {
		t.Fatalf("output contract = %#v -> %#v", output.Before, output.After)
	}
	if len(output.Upstream) != 1 || output.Upstream[0].Column != "total_amount" {
		t.Fatalf("output upstream = %#v", output.Upstream)
	}
}

func TestDiffQuerySemanticsShowsCastContainingOutputTypeChange(t *testing.T) {
	query := "SELECT CAST(SUM(total_amount) AS DECIMAL(18, 2)) AS total FROM lineitems"
	diff, err := DiffQuerySemantics(query, query, QuerySemanticDiffOptions{
		Dialect:      DialectDuckDB,
		BeforeSchema: semanticDiffSchema("INTEGER"),
		AfterSchema:  semanticDiffSchema("DOUBLE"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.InputChanges) != 1 || len(diff.OutputChanges) != 0 {
		t.Fatalf("changes = inputs %#v / outputs %#v", diff.InputChanges, diff.OutputChanges)
	}
}

func TestDiffQuerySemanticsTreatsFormattingAsCanonicalIdentity(t *testing.T) {
	before := "SELECT SUM(total_amount) AS total FROM lineitems"
	formatted := []string{
		"SELECT\n  SUM(total_amount) AS total\nFROM lineitems",
		"-- formatter comment\nselect\n\tsum( total_amount ) as total\nfrom lineitems;",
	}
	for _, after := range formatted {
		diff, err := DiffQuerySemantics(before, after, QuerySemanticDiffOptions{
			Dialect:      DialectDuckDB,
			BeforeSchema: semanticDiffSchema("INTEGER"),
			AfterSchema:  semanticDiffSchema("INTEGER"),
		})
		if err != nil {
			t.Fatal(err)
		}
		if diff.SourceEqual || !diff.CanonicalEqual {
			beforeCanonical, _ := semanticCanonicalSQL(before, DialectDuckDB)
			afterCanonical, _ := semanticCanonicalSQL(after, DialectDuckDB)
			t.Fatalf("source/canonical identity for %q: %q != %q", after, beforeCanonical, afterCanonical)
		}
		if len(diff.InputChanges) != 0 || len(diff.OutputChanges) != 0 {
			t.Fatalf("formatting produced semantic changes for %q: %#v", after, diff)
		}
	}
}

func TestDiffQuerySemanticsDoesNotStripCommentMarkersInsideStrings(t *testing.T) {
	diff, err := DiffQuerySemantics(
		"SELECT '-- not a comment' AS marker",
		"select\n  '-- not a comment' as marker;",
		QuerySemanticDiffOptions{Dialect: DialectDuckDB},
	)
	if err != nil {
		t.Fatal(err)
	}
	if diff.SourceEqual || !diff.CanonicalEqual || len(diff.OutputChanges) != 0 {
		t.Fatalf("string literal was not canonicalized safely: %#v", diff)
	}
}

func TestDiffQuerySemanticsRetainsOptimizerHintsInCanonicalIdentity(t *testing.T) {
	before := "SELECT SUM(total_amount) AS total FROM lineitems"
	after := "SELECT /*+ USE_HASH(lineitems) */ SUM(total_amount) AS total FROM lineitems"
	diff, err := DiffQuerySemantics(before, after, QuerySemanticDiffOptions{
		Dialect:      DialectDuckDB,
		BeforeSchema: semanticDiffSchema("INTEGER"),
		AfterSchema:  semanticDiffSchema("INTEGER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if diff.SourceEqual || diff.CanonicalEqual {
		t.Fatalf("optimizer hint was treated as formatting: %#v", diff)
	}
	if len(diff.InputChanges) != 0 || len(diff.OutputChanges) != 0 {
		t.Fatalf("optimizer hint changed the inferred contract: %#v", diff)
	}

	formattedHint := "select\n  /*+ USE_HASH(lineitems) */\n  sum( total_amount ) as total\nfrom lineitems;"
	formattedDiff, err := DiffQuerySemantics(after, formattedHint, QuerySemanticDiffOptions{
		Dialect:      DialectDuckDB,
		BeforeSchema: semanticDiffSchema("INTEGER"),
		AfterSchema:  semanticDiffSchema("INTEGER"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if formattedDiff.SourceEqual || !formattedDiff.CanonicalEqual {
		t.Fatalf("formatting around the same hint changed canonical identity: %#v", formattedDiff)
	}
}

func TestDiffQuerySemanticsDoesNotClaimUnknownTypesAreComplete(t *testing.T) {
	query := "SELECT SUM(total_amount) AS total FROM lineitems"
	unknown := semanticDiffSchema("")
	diff, err := DiffQuerySemantics(query, query, QuerySemanticDiffOptions{
		Dialect:      DialectDuckDB,
		BeforeSchema: unknown,
		AfterSchema:  unknown,
	})
	if err != nil {
		t.Fatal(err)
	}
	if diff.Complete {
		t.Fatalf("unknown inference reported complete: %#v", diff)
	}
}

func semanticDiffSchema(dataType string) *ValidationSchema {
	nullable := false
	return &ValidationSchema{Tables: []SchemaTable{{
		Name: "lineitems",
		Columns: []SchemaColumn{{
			Name: "total_amount", Type: dataType, Nullable: &nullable,
		}},
	}}}
}

func semanticDiffType(contract SemanticColumnContract) string {
	if contract.TypeHint == nil {
		return ""
	}
	return *contract.TypeHint
}

func semanticDiffOutputType(column QueryOutputColumnFact) string {
	if column.TypeHint == nil {
		return ""
	}
	return *column.TypeHint
}
