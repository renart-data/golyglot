package golyglot

import "testing"

func TestValidateAcceptsValuesQueries(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		dialect Dialect
	}{
		{
			name:    "standalone values",
			sql:     "VALUES (1), (2)",
			dialect: DialectGeneric,
		},
		{
			name:    "values set operation",
			sql:     "VALUES (1) UNION ALL VALUES (2)",
			dialect: DialectDuckDB,
		},
		{
			name: "values CTE",
			sql: `WITH seed(day, channel, revenue, orders) AS (
  VALUES
    (DATE '2026-08-18', 'Organic', 12400, 182),
    (DATE '2026-08-18', 'Paid', 8150, 104),
    (DATE '2026-08-19', 'Organic', 13150, 191),
    (DATE '2026-08-19', 'Paid', 9025, 119)
)
SELECT day, channel, revenue, orders
FROM seed
ORDER BY day, channel`,
			dialect: DialectDuckDB,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := Validate(test.sql, test.dialect)
			if !result.Valid || len(result.Errors) != 0 {
				t.Fatalf("Validate() = %#v, want a valid VALUES query", result)
			}
		})
	}
}
