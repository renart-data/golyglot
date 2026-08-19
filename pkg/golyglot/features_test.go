package golyglot

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDialectDiscoveryAndBuilder(t *testing.T) {
	if DialectCount() <= 30 {
		t.Fatalf("DialectCount() = %d, want more than 30", DialectCount())
	}
	dialects := Dialects()
	if len(dialects) != DialectCount() {
		t.Fatalf("Dialects() returned %d names, count is %d", len(dialects), DialectCount())
	}
	for _, from := range dialects {
		for _, to := range dialects {
			if _, err := TranspileOne("SELECT 1", from, to); err != nil {
				t.Fatalf("transpile %s -> %s: %v", from, to, err)
			}
		}
	}

	query := Select(Column("u.id").As("user_id"), Upper(Column("u.name")).As("name"))
	query = query.From(Table("users").As("u")).Where(Column("u.id").GT(1)).OrderBy(Column("u.id").Desc()).Limit(10)
	got, err := BuildSQL(query, DialectPostgreSQL)
	if err != nil {
		t.Fatal(err)
	}
	want := "SELECT u.id AS user_id, UPPER(u.name) AS name FROM users AS u WHERE u.id > 1 ORDER BY u.id DESC LIMIT 10"
	if got != want {
		t.Fatalf("BuildSQL() = %q, want %q", got, want)
	}

	insert, err := BuildSQL(InsertInto("users").Values([]any{1, "Ada"}), DialectGeneric)
	if err != nil {
		t.Fatal(err)
	}
	if insert != "INSERT INTO users VALUES (1, 'Ada')" {
		t.Fatalf("insert = %q", insert)
	}

	column, err := Column("users.id").AST()
	if err != nil {
		t.Fatal(err)
	}
	generated, err := Generate(column)
	if err != nil || generated != "users.id" {
		t.Fatalf("Generate(expression) = %q, %v", generated, err)
	}
	base := Select(Column("id"))
	derived := base.From(Table("users"))
	baseSQL, _ := BuildSQL(base, DialectGeneric)
	derivedSQL, _ := BuildSQL(derived, DialectGeneric)
	if baseSQL != "SELECT id" || derivedSQL != "SELECT id FROM users" {
		t.Fatalf("builder values were not independent: base=%q derived=%q", baseSQL, derivedSQL)
	}
}

func TestVisitorWalkFindAndTransform(t *testing.T) {
	result, err := ParseStrict("SELECT a + b AS total FROM source", DialectGeneric)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []NodeKind
	Walk(result.Statements[0].Node, func(node Node) VisitAction {
		kinds = append(kinds, node.Kind())
		return VisitChildren
	})
	if len(kinds) < 5 {
		t.Fatalf("walk visited %d nodes, want a typed expression tree", len(kinds))
	}
	if found := FindAll(result.Statements[0].Node, func(node Node) bool { return node.Kind() == NodeIdentifier }); len(found) != 2 {
		t.Fatalf("FindAll identifiers = %d, want 2", len(found))
	}
	transformed := Transform(result.Statements[0].Node, func(node Node) Node {
		identifier, ok := node.(*IdentifierExpr)
		if ok && len(identifier.Parts) == 1 && identifier.Parts[0].Text == "a" {
			identifier.Parts[0].Text = "renamed"
		}
		return node
	})
	got, err := GenerateWithOptions(transformed, GenerateOptions{Canonical: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "renamed + b") {
		t.Fatalf("transformed SQL = %q", got)
	}
}

func TestValidationAndAnalysis(t *testing.T) {
	strict := true
	schema := ValidationSchema{Strict: &strict, Tables: []SchemaTable{{Name: "users", Columns: []SchemaColumn{{Name: "id"}, {Name: "name"}}}}}
	valid := ValidateWithSchema("SELECT id, name FROM users", schema, DialectGeneric)
	if !valid.Valid || len(valid.Errors) != 0 {
		t.Fatalf("valid query result = %#v", valid)
	}
	invalid := ValidateWithSchema("SELECT missing FROM users", schema, DialectGeneric)
	if invalid.Valid || !hasValidationCode(invalid.Errors, "SCHEMA_UNKNOWN_COLUMN") {
		t.Fatalf("invalid query result = %#v", invalid)
	}

	analysis, err := AnalyzeQuery("WITH recent AS (SELECT id FROM users) SELECT id AS user_id FROM recent", AnalyzeQueryOptions{Dialect: DialectGeneric, Schema: &schema})
	if err != nil {
		t.Fatal(err)
	}
	if analysis.Shape != "select_with_cte" || len(analysis.CTEs) != 1 || len(analysis.Projections) != 1 {
		t.Fatalf("analysis = %#v", analysis)
	}
	if len(analysis.Relations) == 0 || len(analysis.CTEFacts) != 1 {
		t.Fatalf("analysis relations/CTEs = %#v", analysis)
	}
}

func TestLineageAndOpenLineage(t *testing.T) {
	sql := "SELECT id AS user_id, UPPER(name) AS display_name FROM users"
	node, err := Lineage("display_name", sql, DialectGeneric)
	if err != nil {
		t.Fatal(err)
	}
	if len(node.Downstream) != 1 || node.Downstream[0].SourceName != "users" {
		t.Fatalf("lineage = %#v", node)
	}
	tables, err := SourceTables("display_name", sql, DialectGeneric)
	if err != nil || len(tables) != 1 || tables[0] != "users" {
		t.Fatalf("source tables = %#v, %v", tables, err)
	}
	output, err := OutputColumns(sql, DialectGeneric)
	if err != nil || len(output.Columns) != 2 || !output.OrdinalComplete {
		t.Fatalf("output = %#v, %v", output, err)
	}

	payload, err := OpenLineageColumnLineage(sql, OpenLineageOptions{
		Producer:         "https://example.test/golyglot",
		DatasetNamespace: "warehouse",
		OutputDataset:    &OpenLineageDatasetID{Namespace: "warehouse", Name: "report"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload.Facet.Fields["display_name"].InputFields[0].Name != "users" {
		t.Fatalf("OpenLineage payload = %#v", payload)
	}
	event, err := OpenLineageRunEvent(sql, OpenLineageOptions{
		Producer:         "https://example.test/golyglot",
		DatasetNamespace: "warehouse",
		OutputDataset:    &OpenLineageDatasetID{Namespace: "warehouse", Name: "report"},
		JobNamespace:     "jobs",
		JobName:          "report",
		EventTime:        "2026-08-15T00:00:00Z",
		RunID:            "run-1",
		EventType:        OpenLineageRunEventComplete,
	})
	if err != nil || !json.Valid(event.Event) {
		t.Fatalf("event = %s, %v", event.Event, err)
	}
}

func TestSchemaOutputAndSetAnalysis(t *testing.T) {
	schema := ValidationSchema{Tables: []SchemaTable{{Name: "users", Columns: []SchemaColumn{{Name: "id"}, {Name: "name"}}}}}
	output, err := OutputColumnsWithSchema("SELECT * FROM users", schema, DialectGeneric)
	if err != nil || !output.OrdinalComplete || len(output.Columns) != 2 {
		t.Fatalf("schema output = %#v, %v", output, err)
	}
	analysis, err := AnalyzeQuery("SELECT id FROM users UNION ALL SELECT id FROM users", AnalyzeQueryOptions{Dialect: DialectGeneric})
	if err != nil {
		t.Fatal(err)
	}
	if len(analysis.SetOperations) != 1 || !analysis.SetOperations[0].All {
		t.Fatalf("set analysis = %#v", analysis)
	}
	node, err := LineageAt(0, "SELECT id FROM users UNION ALL SELECT id FROM users", DialectGeneric)
	if err != nil || len(node.Downstream) != 2 {
		t.Fatalf("set lineage = %#v, %v", node, err)
	}
	cteNode, err := Lineage("user_id", "WITH recent AS (SELECT id AS user_id FROM users) SELECT user_id FROM recent", DialectGeneric)
	if err != nil || len(cteNode.Downstream) != 1 || len(cteNode.Downstream[0].Downstream) != 1 || cteNode.Downstream[0].Downstream[0].SourceName != "users" {
		t.Fatalf("CTE lineage = %#v, %v", cteNode, err)
	}
}

func hasValidationCode(errors []ValidationError, code string) bool {
	for _, issue := range errors {
		if issue.Code == code {
			return true
		}
	}
	return false
}
