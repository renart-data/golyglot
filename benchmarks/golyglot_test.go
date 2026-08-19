package benchmarks_test

import (
	"testing"

	"github.com/renart-data/golyglot/pkg/golyglot"
)

const simpleSelect = "SELECT a, b, c FROM table1"

const mediumSelect = `
SELECT
    u.id,
    u.name,
    u.email,
    COUNT(o.id) AS order_count,
    SUM(o.total) AS total_spent
FROM users u
LEFT JOIN orders o ON u.id = o.user_id
WHERE u.created_at > '2024-01-01'
    AND u.status = 'active'
GROUP BY u.id, u.name, u.email
HAVING COUNT(o.id) > 5
ORDER BY total_spent DESC
LIMIT 100
`

const complexSelect = `
WITH
    active_users AS (
        SELECT
            u.id,
            u.name,
            u.email,
            u.created_at
        FROM users u
        WHERE u.status = 'active'
            AND u.last_login > CURRENT_DATE - INTERVAL '30 days'
    ),
    user_orders AS (
        SELECT
            o.user_id,
            COUNT(*) AS order_count,
            SUM(o.total) AS total_spent,
            AVG(o.total) AS avg_order_value,
            MAX(o.created_at) AS last_order_date
        FROM orders o
        WHERE o.status = 'completed'
        GROUP BY o.user_id
    )
SELECT
    au.id AS user_id,
    au.name AS user_name,
    au.email,
    COALESCE(uo.order_count, 0) AS total_orders,
    COALESCE(uo.total_spent, 0) AS lifetime_value,
    COALESCE(uo.avg_order_value, 0) AS average_order,
    uo.last_order_date,
    CASE
        WHEN uo.total_spent > 10000 THEN 'VIP'
        WHEN uo.total_spent > 1000 THEN 'Premium'
        ELSE 'Regular'
    END AS customer_tier
FROM active_users au
LEFT JOIN user_orders uo ON au.id = uo.user_id
ORDER BY uo.total_spent DESC NULLS LAST
LIMIT 1000
`

// These three inputs mirror crates/polyglot-sql/benches/parsing.rs so the
// parser comparison uses the original project's own query-size suite.
const polyglotParseMedium = `
SELECT
    u.id,
    u.name,
    u.email,
    COUNT(o.id) as order_count,
    SUM(o.total) as total_spent
FROM users u
LEFT JOIN orders o ON u.id = o.user_id
WHERE u.created_at > '2024-01-01'
    AND u.status = 'active'
GROUP BY u.id, u.name, u.email
HAVING COUNT(o.id) > 5
ORDER BY total_spent DESC
LIMIT 100
`

const polyglotParseComplex = `
WITH
    active_users AS (
        SELECT
            u.id,
            u.name,
            u.email,
            u.created_at
        FROM users u
        WHERE u.status = 'active'
            AND u.last_login > CURRENT_DATE - INTERVAL '30 days'
    ),
    user_orders AS (
        SELECT
            o.user_id,
            COUNT(*) as order_count,
            SUM(o.total) as total_spent,
            AVG(o.total) as avg_order_value,
            MAX(o.created_at) as last_order_date
        FROM orders o
        WHERE o.status = 'completed'
        GROUP BY o.user_id
    ),
    product_categories AS (
        SELECT DISTINCT
            p.category_id,
            c.name as category_name
        FROM products p
        JOIN categories c ON p.category_id = c.id
        WHERE p.is_active = true
    )
SELECT
    au.id as user_id,
    au.name as user_name,
    au.email,
    COALESCE(uo.order_count, 0) as total_orders,
    COALESCE(uo.total_spent, 0) as lifetime_value,
    COALESCE(uo.avg_order_value, 0) as average_order,
    uo.last_order_date,
    CASE
        WHEN uo.total_spent > 10000 THEN 'VIP'
        WHEN uo.total_spent > 1000 THEN 'Premium'
        WHEN uo.total_spent > 100 THEN 'Regular'
        ELSE 'New'
    END as customer_tier,
    (
        SELECT STRING_AGG(pc.category_name, ', ')
        FROM user_orders uo2
        JOIN order_items oi ON uo2.user_id = oi.order_id
        JOIN products p ON oi.product_id = p.id
        JOIN product_categories pc ON p.category_id = pc.category_id
        WHERE uo2.user_id = au.id
    ) as preferred_categories
FROM active_users au
LEFT JOIN user_orders uo ON au.id = uo.user_id
WHERE (uo.order_count IS NULL OR uo.order_count < 100)
ORDER BY uo.total_spent DESC NULLS LAST, au.created_at
LIMIT 1000 OFFSET 0
`

func BenchmarkParse(b *testing.B) {
	benchmarkParse(b, "simple", simpleSelect)
	benchmarkParse(b, "medium", polyglotParseMedium)
	benchmarkParse(b, "complex", polyglotParseComplex)
}

func benchmarkParse(b *testing.B, name, sql string) {
	b.Run(name, func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(sql)))
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			result, err := golyglot.ParseStrict(sql, golyglot.DialectGeneric)
			if err != nil {
				b.Fatal(err)
			}
			if len(result.Statements) != 1 {
				b.Fatalf("got %d statements, want 1", len(result.Statements))
			}
		}
	})
}

func BenchmarkTranspile(b *testing.B) {
	benchmarkTranspile(b, "simple", simpleSelect)
	benchmarkTranspile(b, "medium", mediumSelect)
	benchmarkTranspile(b, "complex", complexSelect)
}

func benchmarkTranspile(b *testing.B, name, sql string) {
	b.Run(name, func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(sql)))
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := golyglot.TranspileOne(sql, golyglot.DialectPostgreSQL, golyglot.DialectMySQL); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkFormat(b *testing.B) {
	benchmarkFormat(b, "simple", simpleSelect)
	benchmarkFormat(b, "medium", mediumSelect)
	benchmarkFormat(b, "complex", complexSelect)
}

func benchmarkFormat(b *testing.B, name, sql string) {
	b.Run(name, func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(sql)))
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := golyglot.FormatOne(sql, golyglot.DialectGeneric); err != nil {
				b.Fatal(err)
			}
		}
	})
}
