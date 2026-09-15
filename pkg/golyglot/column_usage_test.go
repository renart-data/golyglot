package golyglot

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestColumnUsesResolveCTEsWithoutMixingValueLineage(t *testing.T) {
	sql := "WITH a(amount, id) AS (SELECT value, id FROM t1), b AS (SELECT * FROM a) SELECT id FROM b WHERE amount > 0 ORDER BY id"
	schema := upstreamScopeSchema()
	analysis, err := AnalyzeQuery(sql, AnalyzeQueryOptions{Dialect: DialectDuckDB, Schema: &schema})
	if err != nil {
		t.Fatal(err)
	}
	var filter *ColumnUseFact
	for index := range analysis.ColumnUses {
		if analysis.ColumnUses[index].Context == "filter" {
			filter = &analysis.ColumnUses[index]
		}
	}
	if filter == nil || !filter.Complete || len(filter.References) != 1 || len(filter.Upstream) != 1 {
		t.Fatalf("filter facts = %#v", filter)
	}
	if reference := filter.References[0]; reference.SourceKind != "cte" || reference.SourceName == nil || *reference.SourceName != "b" || reference.Column != "amount" {
		t.Fatalf("immediate reference = %#v", reference)
	}
	if reference := filter.Upstream[0]; reference.SourceKind != "table" || reference.SourceName == nil || *reference.SourceName != "t1" || reference.Column != "value" {
		t.Fatalf("physical reference = %#v", reference)
	}
	if got := sql[filter.Span.Start:filter.Span.End]; got != "amount > 0" {
		t.Fatalf("expression span = %q", got)
	}
	for _, reference := range append(filter.References, filter.Upstream...) {
		if reference.Span == nil || sql[reference.Span.Start:reference.Span.End] != "amount" {
			t.Fatalf("reference use-site span = %#v", reference.Span)
		}
	}
	for _, reference := range analysis.Projections[0].Upstream {
		if reference.Column == "value" || reference.Column == "amount" {
			t.Fatal("predicate dependency leaked into projected value lineage")
		}
	}
	encoded, err := json.Marshal(analysis)
	if err != nil || !strings.Contains(string(encoded), `"columnUses"`) {
		t.Fatalf("serialized column uses: %s, %v", encoded, err)
	}
}

func TestColumnUsesRemainConservativeAndMarkDiffIncomplete(t *testing.T) {
	for _, sql := range []string{
		"SELECT 1 AS matched FROM t1 a JOIN t2 b ON a.id = b.id WHERE id > 0",
		"SELECT 1 AS matched FROM t1 WHERE missing > 0",
		"SELECT 1 AS matched FROM unknown_table WHERE id > 0",
		"SELECT 1 AS matched FROM t1 NATURAL JOIN t2",
	} {
		t.Run(sql, func(t *testing.T) {
			schema := upstreamScopeSchema()
			diff, err := DiffQuerySemantics(sql, sql, QuerySemanticDiffOptions{Dialect: DialectDuckDB, BeforeSchema: &schema, AfterSchema: &schema})
			if err != nil {
				t.Fatal(err)
			}
			if diff.Complete {
				t.Fatal("unresolved predicate was reported as a complete semantic comparison")
			}
			for _, use := range diff.BeforeAnalysis.ColumnUses {
				if use.Context == "filter" && (use.Complete || len(use.Upstream) != 0) {
					t.Fatalf("guessed a predicate source: %#v", use)
				}
			}
		})
	}
}

func TestColumnUsesFollowBothUnionBranches(t *testing.T) {
	for _, query := range []string{
		"WITH u AS (SELECT id FROM t2 UNION ALL SELECT value FROM t1) SELECT 1 FROM u WHERE id > 0",
		"WITH u AS ((SELECT id FROM t2 UNION ALL SELECT value FROM t1) UNION ALL SELECT id FROM t2) SELECT 1 FROM u WHERE id > 0",
		"WITH u AS (SELECT id FROM t2 UNION ALL SELECT id FROM t2 UNION ALL SELECT value FROM t1) SELECT 1 FROM u WHERE id > 0",
	} {
		t.Run(query, func(t *testing.T) {
			before, after := upstreamScopeSchema(), upstreamScopeSchema()
			after.Tables[0].Columns[1].Type = "DOUBLE"
			diff, err := DiffQuerySemantics(query, query, QuerySemanticDiffOptions{Dialect: DialectDuckDB, BeforeSchema: &before, AfterSchema: &after})
			if err != nil {
				t.Fatal(err)
			}
			if len(diff.InputChanges) != 1 || diff.InputChanges[0].Table != "t1" || diff.InputChanges[0].Column != "value" || !diff.InputChanges[0].TypeChanged {
				t.Fatalf("UNION predicate lost the right input: %#v", diff.InputChanges)
			}
		})
	}
}

func TestColumnUsesDoNotGuessNameAlignedUnionLineage(t *testing.T) {
	schema := upstreamScopeSchema()
	query := "WITH u AS (SELECT id AS amount FROM t2 UNION ALL BY NAME SELECT value AS amount FROM t1) SELECT 1 AS matched FROM u WHERE amount > 0"
	diff, err := DiffQuerySemantics(query, query, QuerySemanticDiffOptions{Dialect: DialectDuckDB, BeforeSchema: &schema, AfterSchema: &schema})
	if err != nil {
		t.Fatal(err)
	}
	if diff.Complete {
		t.Fatal("unmodeled name-aligned lineage was reported as complete")
	}
	for _, use := range diff.BeforeAnalysis.ColumnUses {
		if use.Context == "filter" && !use.Complete {
			return
		}
	}
	t.Fatal("missing incomplete predicate fact")
}

func TestColumnUsesTrackPresenceNullabilityAndIgnoreUnusedCTEs(t *testing.T) {
	query := "SELECT id FROM t1 WHERE value > 0"
	before, after := upstreamScopeSchema(), upstreamScopeSchema()
	beforeNullable, afterNullable := false, true
	before.Tables[0].Columns[1].Nullable = &beforeNullable
	after.Tables[0].Columns[1].Nullable = &afterNullable
	diff, err := DiffQuerySemantics(query, query, QuerySemanticDiffOptions{Dialect: DialectDuckDB, BeforeSchema: &before, AfterSchema: &after})
	if err != nil || len(diff.InputChanges) != 1 || !diff.InputChanges[0].NullabilityChanged {
		t.Fatalf("nullability change = %#v, %v", diff.InputChanges, err)
	}
	after.Tables[0].Columns = after.Tables[0].Columns[:1]
	diff, err = DiffQuerySemantics(query, query, QuerySemanticDiffOptions{Dialect: DialectDuckDB, BeforeSchema: &before, AfterSchema: &after})
	if err != nil || len(diff.InputChanges) != 1 || !diff.InputChanges[0].PresenceChanged || diff.Complete {
		t.Fatalf("removed predicate column = %#v, complete=%v, %v", diff.InputChanges, diff.Complete, err)
	}
	unused := "WITH unused AS (SELECT id FROM t1 WHERE value > 0) SELECT id FROM t2"
	diff, err = DiffQuerySemantics(unused, unused, QuerySemanticDiffOptions{Dialect: DialectDuckDB, BeforeSchema: &before, AfterSchema: &after})
	if err != nil || len(diff.InputChanges) != 0 {
		t.Fatalf("unused CTE changed the result dependencies: %#v, %v", diff.InputChanges, err)
	}
}

func TestColumnUsesPreserveFormattingIndependenceAndAliasResolution(t *testing.T) {
	schema := upstreamScopeSchema()
	before := "SELECT id AS renamed FROM t1 WHERE value > 0 ORDER BY renamed"
	after := "-- heading\nselect id as renamed\nfrom t1\nwhere value > 0\norder by renamed;"
	diff, err := DiffQuerySemantics(before, after, QuerySemanticDiffOptions{Dialect: DialectDuckDB, BeforeSchema: &schema, AfterSchema: &schema})
	if err != nil || !diff.CanonicalEqual || !diff.Complete || len(diff.InputChanges) != 0 || len(diff.OutputChanges) != 0 {
		t.Fatalf("format/alias comparison = %#v, %v", diff, err)
	}
	if result := ValidateWithSchema(before, schema, DialectDuckDB); !result.Valid {
		t.Fatalf("ORDER BY output alias failed validation: %#v", result.Errors)
	}
}

func TestColumnUsesIncludeJoinUsingAndNamedWindowContexts(t *testing.T) {
	schema := upstreamScopeSchema()
	query := "SELECT ROW_NUMBER() OVER w FROM t1 a JOIN t2 b USING (id) WINDOW w AS (PARTITION BY a.value ORDER BY b.id)"
	analysis, err := AnalyzeQuery(query, AnalyzeQueryOptions{Dialect: DialectDuckDB, Schema: &schema})
	if err != nil {
		t.Fatal(err)
	}
	contexts := map[string]bool{}
	for _, use := range analysis.ColumnUses {
		contexts[use.Context] = true
		if use.Context == "join" && (len(use.Upstream) != 2 || !use.Complete) {
			t.Fatalf("USING should resolve both sides: %#v", use)
		}
	}
	for _, context := range []string{"join", "window_partition", "window_order"} {
		if !contexts[context] {
			t.Fatalf("missing %s in %#v", context, analysis.ColumnUses)
		}
	}
}

func TestColumnUsesDoNotBindEarlierJoinToLaterTables(t *testing.T) {
	schema := upstreamScopeSchema()
	schema.Tables = append(schema.Tables, SchemaTable{Name: "t3", Columns: []SchemaColumn{{Name: "id", Type: "INTEGER"}}})
	query := "SELECT a.id FROM t1 a JOIN t2 b USING (id) JOIN t3 c ON a.id = c.id"
	analysis, err := AnalyzeQuery(query, AnalyzeQueryOptions{Dialect: DialectDuckDB, Schema: &schema})
	if err != nil {
		t.Fatal(err)
	}
	for _, use := range analysis.ColumnUses {
		if use.Context != "join" || !strings.HasPrefix(use.ExpressionSQL, "USING") {
			continue
		}
		if len(use.Upstream) != 2 {
			t.Fatalf("first join must only use its two sides: %#v", use.Upstream)
		}
		for _, reference := range use.Upstream {
			if reference.SourceName != nil && *reference.SourceName == "t3" {
				t.Fatal("future join table leaked into earlier USING")
			}
		}
	}
}
