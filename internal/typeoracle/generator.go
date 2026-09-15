package typeoracle

import (
	"fmt"
	"math/rand"
	"sort"

	"github.com/renart-data/golyglot/pkg/golyglot"
)

func GenerateCases(seed int64, count int) []QueryCase {
	if count <= 0 {
		return nil
	}
	schema := oracleSchema()
	numeric := []string{"tiny_col", "small_col", "integer_col", "bigint_col", "hugeint_col", "real_col", "double_col", "decimal_col"}
	pool := make([]QueryCase, 0, len(numeric)*8)
	appendCase := func(label, expression string, features ...string) {
		pool = append(pool, QueryCase{
			ID:               fmt.Sprintf("%s-%03d", label, len(pool)+1),
			SQL:              "SELECT " + expression + " AS value FROM oracle_values",
			Dialect:          golyglot.DialectDuckDB,
			Schema:           schema,
			GeneratorVersion: GeneratorVersion,
			Source:           CaseSourceGenerated,
			Features:         normalizedFeatures(features...),
		})
	}
	for _, column := range numeric {
		typeFeature := "input:" + oracleColumnTypeFamily(column)
		appendCase("identity", column, "identity", "projection", typeFeature)
		appendCase("sum", "SUM("+column+")", "aggregate", "aggregate:sum", typeFeature)
		appendCase("avg", "AVG("+column+")", "aggregate", "aggregate:avg", typeFeature)
		appendCase("min", "MIN("+column+")", "aggregate", "aggregate:min", typeFeature)
		appendCase("max", "MAX("+column+")", "aggregate", "aggregate:max", typeFeature)
		appendCase("add-literal", column+" + 1", "arithmetic", "arithmetic:add", "literal", typeFeature)
		appendCase("coalesce", "COALESCE("+column+", 0)", "coercion", "function:coalesce", typeFeature)
		appendCase("cast-double", "CAST("+column+" AS DOUBLE)", "cast", "cast:double", typeFeature)
	}
	for left := range numeric {
		right := (left + 1) % len(numeric)
		appendCase("add-columns", numeric[left]+" + "+numeric[right],
			"arithmetic", "arithmetic:add", "mixed-inputs",
			"input:"+oracleColumnTypeFamily(numeric[left]), "input:"+oracleColumnTypeFamily(numeric[right]))
	}
	appendCase("count", "COUNT(*)", "aggregate", "aggregate:count", "star")
	appendCase("case", "CASE WHEN boolean_col THEN integer_col ELSE bigint_col END", "case", "coercion", "input:boolean", "input:integer")
	appendCase("date-min", "MIN(date_col)", "aggregate", "aggregate:min", "input:date")
	appendCase("timestamp-max", "MAX(timestamp_col)", "aggregate", "aggregate:max", "input:timestamp")

	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	if count > len(pool) {
		count = len(pool)
	}
	return append([]QueryCase(nil), pool[:count]...)
}

func normalizedFeatures(features ...string) []string {
	seen := make(map[string]struct{}, len(features))
	result := make([]string, 0, len(features))
	for _, feature := range features {
		if feature == "" {
			continue
		}
		if _, ok := seen[feature]; ok {
			continue
		}
		seen[feature] = struct{}{}
		result = append(result, feature)
	}
	sort.Strings(result)
	return result
}

func oracleColumnTypeFamily(column string) string {
	switch column {
	case "tiny_col":
		return "tinyint"
	case "small_col":
		return "smallint"
	case "integer_col":
		return "integer"
	case "bigint_col":
		return "bigint"
	case "hugeint_col":
		return "hugeint"
	case "real_col":
		return "float"
	case "double_col":
		return "double"
	case "decimal_col":
		return "decimal"
	default:
		return column
	}
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
