package typeoracle

import (
	"fmt"
	"math/rand"

	"github.com/renart-data/golyglot/pkg/golyglot"
)

func GenerateCases(seed int64, count int) []QueryCase {
	if count <= 0 {
		return nil
	}
	schema := oracleSchema()
	numeric := []string{"tiny_col", "small_col", "integer_col", "bigint_col", "hugeint_col", "real_col", "double_col", "decimal_col"}
	pool := make([]QueryCase, 0, len(numeric)*8)
	appendCase := func(label, expression string) {
		pool = append(pool, QueryCase{
			ID:      fmt.Sprintf("%s-%03d", label, len(pool)+1),
			SQL:     "SELECT " + expression + " AS value FROM oracle_values",
			Dialect: golyglot.DialectDuckDB,
			Schema:  schema,
		})
	}
	for _, column := range numeric {
		appendCase("identity", column)
		appendCase("sum", "SUM("+column+")")
		appendCase("avg", "AVG("+column+")")
		appendCase("min", "MIN("+column+")")
		appendCase("max", "MAX("+column+")")
		appendCase("add-literal", column+" + 1")
		appendCase("coalesce", "COALESCE("+column+", 0)")
		appendCase("cast-double", "CAST("+column+" AS DOUBLE)")
	}
	for left := range numeric {
		right := (left + 1) % len(numeric)
		appendCase("add-columns", numeric[left]+" + "+numeric[right])
	}
	appendCase("count", "COUNT(*)")
	appendCase("case", "CASE WHEN boolean_col THEN integer_col ELSE bigint_col END")
	appendCase("date-min", "MIN(date_col)")
	appendCase("timestamp-max", "MAX(timestamp_col)")

	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	if count > len(pool) {
		count = len(pool)
	}
	return append([]QueryCase(nil), pool[:count]...)
}

func oracleSchema() *golyglot.ValidationSchema {
	nullable := true
	columns := []golyglot.SchemaColumn{
		{Name: "tiny_col", Type: "TINYINT", Nullable: &nullable},
		{Name: "small_col", Type: "SMALLINT", Nullable: &nullable},
		{Name: "integer_col", Type: "INTEGER", Nullable: &nullable},
		{Name: "bigint_col", Type: "BIGINT", Nullable: &nullable},
		{Name: "hugeint_col", Type: "HUGEINT", Nullable: &nullable},
		{Name: "real_col", Type: "FLOAT", Nullable: &nullable},
		{Name: "double_col", Type: "DOUBLE", Nullable: &nullable},
		{Name: "decimal_col", Type: "DECIMAL(18, 4)", Nullable: &nullable},
		{Name: "boolean_col", Type: "BOOLEAN", Nullable: &nullable},
		{Name: "text_col", Type: "VARCHAR", Nullable: &nullable},
		{Name: "date_col", Type: "DATE", Nullable: &nullable},
		{Name: "timestamp_col", Type: "TIMESTAMP", Nullable: &nullable},
	}
	return &golyglot.ValidationSchema{Tables: []golyglot.SchemaTable{{Name: "oracle_values", Columns: columns}}}
}
