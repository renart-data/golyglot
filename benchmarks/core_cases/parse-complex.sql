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
