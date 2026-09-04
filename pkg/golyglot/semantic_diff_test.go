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
	if output.Before == nil || output.After == nil || semanticDiffOutputType(*output.Before) != "HUGEINT" || semanticDiffOutputType(*output.After) != "DOUBLE" {
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

func TestDiffQuerySemanticsClassifiesBehaviorChangesWithStableOutput(t *testing.T) {
	before := "SELECT user_id, SUM(amount) AS total FROM sales WHERE status = 'paid' GROUP BY user_id HAVING SUM(amount) > 0 ORDER BY total DESC LIMIT 10"
	after := "SELECT user_id, SUM(amount) AS total FROM sales WHERE status = 'settled' GROUP BY user_id HAVING SUM(amount) > 0 ORDER BY total DESC LIMIT 10"
	schema := semanticBehaviorSchema()
	diff, err := DiffQuerySemantics(before, after, QuerySemanticDiffOptions{
		Dialect: DialectDuckDB, BeforeSchema: schema, AfterSchema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	if diff.BeforeBehavior.Fingerprint == "" || diff.AfterBehavior.Fingerprint == "" {
		t.Fatalf("missing behavior fingerprints: %#v", diff)
	}
	if diff.BeforeBehavior.Fingerprint == diff.AfterBehavior.Fingerprint {
		t.Fatalf("filter edit did not change behavior fingerprint: %#v", diff)
	}
	if len(diff.OutputChanges) != 0 {
		t.Fatalf("filter edit changed output contract: %#v", diff.OutputChanges)
	}
	if len(diff.BehaviorChanges) != 1 || diff.BehaviorChanges[0].Kind != QueryBehaviorFilter {
		t.Fatalf("behavior changes = %#v", diff.BehaviorChanges)
	}
}

func TestDiffQuerySemanticsBehaviorFingerprintIgnoresFormatting(t *testing.T) {
	before := "SELECT DISTINCT user_id FROM sales WHERE status = 'paid' ORDER BY user_id"
	after := "-- presentation only\nselect distinct\n user_id\nfrom sales\nwhere status='paid'\norder by user_id;"
	schema := semanticBehaviorSchema()
	diff, err := DiffQuerySemantics(before, after, QuerySemanticDiffOptions{
		Dialect: DialectDuckDB, BeforeSchema: schema, AfterSchema: schema,
	})
	if err != nil {
		t.Fatal(err)
	}
	if diff.BeforeBehavior.Fingerprint != diff.AfterBehavior.Fingerprint || len(diff.BehaviorChanges) != 0 {
		t.Fatalf("formatting changed behavior: %#v", diff)
	}
}

func TestDiffQuerySemanticsTracksJoinAndDirectiveBehavior(t *testing.T) {
	schema := semanticBehaviorSchema()
	joinDiff, err := DiffQuerySemantics(
		"SELECT s.user_id FROM sales AS s JOIN users AS u ON s.user_id = u.id",
		"SELECT s.user_id FROM sales AS s LEFT JOIN users AS u ON s.user_id = u.id",
		QuerySemanticDiffOptions{Dialect: DialectDuckDB, BeforeSchema: schema, AfterSchema: schema},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(joinDiff.BehaviorChanges) != 1 || joinDiff.BehaviorChanges[0].Kind != QueryBehaviorRelations {
		t.Fatalf("join behavior changes = %#v", joinDiff.BehaviorChanges)
	}

	directiveDiff, err := DiffQuerySemantics(
		"SELECT user_id FROM sales",
		"SELECT /*+ FORCE_INDEX(sales) */ user_id FROM sales",
		QuerySemanticDiffOptions{Dialect: DialectDuckDB, BeforeSchema: schema, AfterSchema: schema},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(directiveDiff.BehaviorChanges) != 1 || directiveDiff.BehaviorChanges[0].Kind != QueryBehaviorDirectives {
		t.Fatalf("directive behavior changes = %#v", directiveDiff.BehaviorChanges)
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

func semanticBehaviorSchema() *ValidationSchema {
	nullable := false
	return &ValidationSchema{Tables: []SchemaTable{
		{Name: "sales", Columns: []SchemaColumn{
			{Name: "user_id", Type: "INTEGER", Nullable: &nullable},
			{Name: "amount", Type: "DECIMAL(18, 2)", Nullable: &nullable},
			{Name: "status", Type: "VARCHAR", Nullable: &nullable},
		}},
		{Name: "users", Columns: []SchemaColumn{{Name: "id", Type: "INTEGER", Nullable: &nullable}}},
	}}
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
