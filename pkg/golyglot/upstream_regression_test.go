package golyglot

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func upstreamScopeSchema() ValidationSchema {
	strict := true
	return ValidationSchema{Strict: &strict, Tables: []SchemaTable{
		{Name: "t1", Columns: []SchemaColumn{{Name: "id", Type: "INTEGER"}, {Name: "value", Type: "INTEGER"}}},
		{Name: "t2", Columns: []SchemaColumn{{Name: "id", Type: "INTEGER"}}},
	}}
}

// Polyglot #441/#442: each query block owns its references, while sibling
// CTEs and legal correlated references retain the appropriate outer scope.
func TestSchemaValidationRespectsQueryScopes(t *testing.T) {
	tests := []struct{ name, sql string }{
		{"qualified CTE join", "WITH a AS (SELECT id FROM t1), b AS (SELECT id FROM t2) SELECT a.id FROM a JOIN b ON a.id = b.id"},
		{"correlated exists", "SELECT o.id FROM t1 o WHERE NOT EXISTS (SELECT 1 FROM t2 i WHERE i.id = o.id)"},
		{"sibling CTE projection", "WITH derived AS (SELECT value AS derived_value FROM t1), next AS (SELECT derived_value FROM derived) SELECT derived_value FROM next"},
		{"sibling CTE window", "WITH scored AS (SELECT id, value AS info_score FROM t1), ranked AS (SELECT id, ROW_NUMBER() OVER (PARTITION BY id ORDER BY info_score DESC) AS rn FROM scored) SELECT id FROM ranked"},
		{"CTE column aliases", "WITH a(x) AS (SELECT id FROM t1), b(y) AS (SELECT x FROM a) SELECT y FROM b"},
		{"CTE star", "WITH a AS (SELECT * FROM t1), b AS (SELECT value FROM a) SELECT value FROM b"},
		{"derived projection", "SELECT d.renamed FROM (SELECT value AS renamed FROM t1) d"},
		{"alias shadowing", "SELECT o.value FROM t1 o WHERE EXISTS (SELECT 1 FROM t2 o WHERE o.id = 1)"},
		{"nested CTE shadowing", "WITH x AS (SELECT value FROM t1) SELECT value FROM x WHERE EXISTS (WITH x AS (SELECT id FROM t2) SELECT id FROM x)"},
		{"set operation CTE", "WITH a AS (SELECT value FROM t1) SELECT value FROM a UNION ALL SELECT value FROM a"},
		{"correlated CTE", "SELECT o.id FROM t1 o WHERE EXISTS (WITH x AS (SELECT o.id) SELECT * FROM x)"},
		{"coalesced using column", "SELECT id FROM t1 JOIN t2 USING (id)"},
		{"lateral derived", "SELECT d.id FROM t1 o JOIN LATERAL (SELECT o.id) d ON TRUE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ValidateWithSchema(test.sql, upstreamScopeSchema(), DialectSnowflake)
			if !result.Valid || len(result.Errors) != 0 {
				t.Fatalf("valid query rejected: %#v", result.Errors)
			}
		})
	}
}

func TestSchemaValidationDoesNotLeakQueryScopes(t *testing.T) {
	tests := []struct{ name, sql string }{
		{"unknown inner column", "SELECT o.id FROM t1 o WHERE EXISTS (SELECT missing FROM t2 i)"},
		{"inner alias outside", "SELECT i.id FROM t1 o WHERE EXISTS (SELECT i.id FROM t2 i)"},
		{"shadowed alias missing column", "SELECT o.id FROM t1 o WHERE EXISTS (SELECT o.value FROM t2 o)"},
		{"ambiguous inner column", "SELECT 1 WHERE EXISTS (SELECT id FROM t1 a JOIN t2 b ON a.id = b.id)"},
		{"forward CTE", "WITH first AS (SELECT id FROM later), later AS (SELECT id FROM t1) SELECT id FROM first"},
		{"unknown qualified alias", "SELECT missing.id FROM t1"},
		{"non-lateral derived", "SELECT d.id FROM t1 o JOIN (SELECT o.id) d ON TRUE"},
		{"join forward alias", "SELECT a.id FROM t1 a JOIN t2 b ON c.id = b.id JOIN t1 c ON c.id = a.id"},
		{"correlated join forward alias", "SELECT a.id FROM t1 a JOIN t2 b ON EXISTS (SELECT 1 WHERE c.id = b.id) JOIN t1 c ON c.id = a.id"},
		{"USING cannot borrow an outer column", "SELECT a.id FROM t1 a WHERE EXISTS (SELECT 1 FROM t2 b JOIN t2 c USING (value))"},
		{"quoted pseudo-column is a real reference", `SELECT id FROM t1 WHERE "current_date" IS NOT NULL`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := ValidateWithSchema(test.sql, upstreamScopeSchema(), DialectSnowflake)
			if result.Valid {
				t.Fatal("invalid reference was accepted")
			}
		})
	}
}

func TestAnalyzeQueryDoesNotInferFromShadowedOuterAlias(t *testing.T) {
	schema := upstreamScopeSchema()
	analysis, err := AnalyzeQuery("SELECT (SELECT o.value FROM t2 o) AS nested FROM t1 o", AnalyzeQueryOptions{Dialect: DialectSnowflake, Schema: &schema})
	if err != nil {
		t.Fatal(err)
	}
	if analysis.OutputTypesComplete || len(analysis.OutputColumns) != 1 || analysis.OutputColumns[0].TypeHint != nil {
		t.Fatalf("missing inner column borrowed the outer type: %#v", analysis.OutputColumns)
	}
}

func TestSemanticDiffFindsNonProjectionTypeChanges(t *testing.T) {
	tests := []struct{ name, sql string }{
		{"filter", "SELECT id FROM t1 WHERE value > 0"},
		{"join", "SELECT a.id FROM t1 a JOIN t2 b ON a.value = b.id"},
		{"group", "SELECT COUNT(*) FROM t1 GROUP BY value"},
		{"having", "SELECT id FROM t1 GROUP BY id HAVING MAX(value) > 0"},
		{"qualify", "SELECT id FROM t1 QUALIFY ROW_NUMBER() OVER (ORDER BY value) = 1"},
		{"order", "SELECT id FROM t1 ORDER BY value"},
		{"window", "SELECT ROW_NUMBER() OVER (PARTITION BY value ORDER BY id) FROM t1"},
		{"CTE predicate", "WITH a AS (SELECT id, value AS amount FROM t1), b AS (SELECT * FROM a) SELECT id FROM b WHERE amount > 0"},
		{"derived predicate", "SELECT d.id FROM (SELECT id, value AS amount FROM t1) d WHERE d.amount > 0"},
		{"exists predicate", "SELECT id FROM t2 WHERE EXISTS (SELECT 1 FROM t1 WHERE value > 0)"},
		{"set filter", "SELECT id FROM t2 EXCEPT SELECT value FROM t1"},
		{"scalar predicate", "SELECT id FROM t2 WHERE id > (SELECT MAX(value) FROM t1)"},
		{"in predicate", "SELECT id FROM t2 WHERE id IN (SELECT value FROM t1)"},
		{"quantified predicate", "SELECT id FROM t2 WHERE id > ALL (SELECT value FROM t1)"},
		{"set filter star", "SELECT id FROM t2 EXCEPT SELECT * FROM (SELECT value FROM t1) d"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before, after := upstreamScopeSchema(), upstreamScopeSchema()
			after.Tables[0].Columns[1].Type = "DOUBLE"
			diff, err := DiffQuerySemantics(test.sql, test.sql, QuerySemanticDiffOptions{Dialect: DialectDuckDB, BeforeSchema: &before, AfterSchema: &after})
			if err != nil {
				t.Fatal(err)
			}
			if len(diff.InputChanges) != 1 || diff.InputChanges[0].Table != "t1" || diff.InputChanges[0].Column != "value" || !diff.InputChanges[0].TypeChanged {
				t.Fatalf("predicate type change was not tracked: %#v", diff.InputChanges)
			}
		})
	}
}

// Keep adversarial cases in a subprocess: a regression must fail with a
// bounded timeout, not leave a spinning goroutine in the suite (Polyglot #447).
func TestParserTruncatedInputsStayBounded(t *testing.T) {
	const worker = "GOLYGLOT_TEST_PREFIX_WORKER"
	if os.Getenv(worker) == "1" {
		statements := []string{
			"SELECT a.:CustomType(10)",
			"SELECT CAST(x AS UserDefinedType(10))",
			"CREATE TABLE t (a VARCHAR2(10))",
			"ALTER TABLE t ADD COLUMN c NVARCHAR2(10)",
			"SELECT JSON_VALUE(a, '$.b' RETURNING VARCHAR2(10))",
			"SELECT CAST(x AS STRUCT(a INTEGER, b VARCHAR))",
			"WITH a AS (SELECT 1 AS id) SELECT id FROM a WHERE id IN (SELECT 1)",
			"SELECT IF(x > 1, 2, 3) FROM t",
			strings.Repeat("IF~", 32) + "I?{",
			strings.Repeat("IF+", 32) + "*",
			strings.Repeat("IF-", 32) + "SELECT",
		}
		for _, dialect := range []Dialect{DialectGeneric, DialectDuckDB, DialectClickHouse, DialectTSQL} {
			for _, sql := range statements {
				for length := 0; length <= len(sql); length++ {
					input := sql[:length]
					_, _ = ParseStrict(input, dialect)
					parsed := ParseTolerant(input, dialect)
					if parsed.SQL != input {
						t.Fatalf("parser changed source for %s %q", dialect, input)
					}
				}
			}
		}
		for _, sql := range []string{"CustomType(", "VARCHAR2(", "STRUCT(a INT,", "MAP(VARCHAR,"} {
			if _, err := ParseDataType(sql, DialectGeneric); err == nil {
				t.Fatalf("accepted truncated type %q", sql)
			}
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestParserTruncatedInputsStayBounded$")
	command.Env = append(os.Environ(), worker+"=1", "GOMEMLIMIT=128MiB")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("parser prefix worker failed (timeout=%v): %v\n%s", ctx.Err(), err, output)
	}
}
