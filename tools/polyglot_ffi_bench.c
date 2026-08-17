#define _POSIX_C_SOURCE 200809L

/* Standalone C-ABI benchmark; this file is intentionally outside Go packages. */

#include <errno.h>
#include <inttypes.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "polyglot_sql.h"

enum operation {
    OP_PARSE,
    OP_TRANSPILE,
    OP_FORMAT,
};

struct benchmark_case {
    enum operation operation;
    const char *operation_name;
    const char *case_name;
    const char *sql;
    const char *source_dialect;
    const char *target_dialect;
};

static volatile unsigned int result_sink;

static const char simple_select[] = "SELECT a, b, c FROM table1";

static const char medium_select[] =
    "\n"
    "SELECT\n"
    "    u.id,\n"
    "    u.name,\n"
    "    u.email,\n"
    "    COUNT(o.id) AS order_count,\n"
    "    SUM(o.total) AS total_spent\n"
    "FROM users u\n"
    "LEFT JOIN orders o ON u.id = o.user_id\n"
    "WHERE u.created_at > '2024-01-01'\n"
    "    AND u.status = 'active'\n"
    "GROUP BY u.id, u.name, u.email\n"
    "HAVING COUNT(o.id) > 5\n"
    "ORDER BY total_spent DESC\n"
    "LIMIT 100\n";

static const char complex_select[] =
    "\n"
    "WITH\n"
    "    active_users AS (\n"
    "        SELECT\n"
    "            u.id,\n"
    "            u.name,\n"
    "            u.email,\n"
    "            u.created_at\n"
    "        FROM users u\n"
    "        WHERE u.status = 'active'\n"
    "            AND u.last_login > CURRENT_DATE - INTERVAL '30 days'\n"
    "    ),\n"
    "    user_orders AS (\n"
    "        SELECT\n"
    "            o.user_id,\n"
    "            COUNT(*) AS order_count,\n"
    "            SUM(o.total) AS total_spent,\n"
    "            AVG(o.total) AS avg_order_value,\n"
    "            MAX(o.created_at) AS last_order_date\n"
    "        FROM orders o\n"
    "        WHERE o.status = 'completed'\n"
    "        GROUP BY o.user_id\n"
    "    )\n"
    "SELECT\n"
    "    au.id AS user_id,\n"
    "    au.name AS user_name,\n"
    "    au.email,\n"
    "    COALESCE(uo.order_count, 0) AS total_orders,\n"
    "    COALESCE(uo.total_spent, 0) AS lifetime_value,\n"
    "    COALESCE(uo.avg_order_value, 0) AS average_order,\n"
    "    uo.last_order_date,\n"
    "    CASE\n"
    "        WHEN uo.total_spent > 10000 THEN 'VIP'\n"
    "        WHEN uo.total_spent > 1000 THEN 'Premium'\n"
    "        ELSE 'Regular'\n"
    "    END AS customer_tier\n"
    "FROM active_users au\n"
    "LEFT JOIN user_orders uo ON au.id = uo.user_id\n"
    "ORDER BY uo.total_spent DESC NULLS LAST\n"
    "LIMIT 1000\n";

static const char polyglot_parse_medium[] =
    "\n"
    "SELECT\n"
    "    u.id,\n"
    "    u.name,\n"
    "    u.email,\n"
    "    COUNT(o.id) as order_count,\n"
    "    SUM(o.total) as total_spent\n"
    "FROM users u\n"
    "LEFT JOIN orders o ON u.id = o.user_id\n"
    "WHERE u.created_at > '2024-01-01'\n"
    "    AND u.status = 'active'\n"
    "GROUP BY u.id, u.name, u.email\n"
    "HAVING COUNT(o.id) > 5\n"
    "ORDER BY total_spent DESC\n"
    "LIMIT 100\n";

static const char polyglot_parse_complex[] =
    "\n"
    "WITH\n"
    "    active_users AS (\n"
    "        SELECT\n"
    "            u.id,\n"
    "            u.name,\n"
    "            u.email,\n"
    "            u.created_at\n"
    "        FROM users u\n"
    "        WHERE u.status = 'active'\n"
    "            AND u.last_login > CURRENT_DATE - INTERVAL '30 days'\n"
    "    ),\n"
    "    user_orders AS (\n"
    "        SELECT\n"
    "            o.user_id,\n"
    "            COUNT(*) as order_count,\n"
    "            SUM(o.total) as total_spent,\n"
    "            AVG(o.total) as avg_order_value,\n"
    "            MAX(o.created_at) as last_order_date\n"
    "        FROM orders o\n"
    "        WHERE o.status = 'completed'\n"
    "        GROUP BY o.user_id\n"
    "    ),\n"
    "    product_categories AS (\n"
    "        SELECT DISTINCT\n"
    "            p.category_id,\n"
    "            c.name as category_name\n"
    "        FROM products p\n"
    "        JOIN categories c ON p.category_id = c.id\n"
    "        WHERE p.is_active = true\n"
    "    )\n"
    "SELECT\n"
    "    au.id as user_id,\n"
    "    au.name as user_name,\n"
    "    au.email,\n"
    "    COALESCE(uo.order_count, 0) as total_orders,\n"
    "    COALESCE(uo.total_spent, 0) as lifetime_value,\n"
    "    COALESCE(uo.avg_order_value, 0) as average_order,\n"
    "    uo.last_order_date,\n"
    "    CASE\n"
    "        WHEN uo.total_spent > 10000 THEN 'VIP'\n"
    "        WHEN uo.total_spent > 1000 THEN 'Premium'\n"
    "        WHEN uo.total_spent > 100 THEN 'Regular'\n"
    "        ELSE 'New'\n"
    "    END as customer_tier,\n"
    "    (\n"
    "        SELECT STRING_AGG(pc.category_name, ', ')\n"
    "        FROM user_orders uo2\n"
    "        JOIN order_items oi ON uo2.user_id = oi.order_id\n"
    "        JOIN products p ON oi.product_id = p.id\n"
    "        JOIN product_categories pc ON p.category_id = pc.category_id\n"
    "        WHERE uo2.user_id = au.id\n"
    "    ) as preferred_categories\n"
    "FROM active_users au\n"
    "LEFT JOIN user_orders uo ON au.id = uo.user_id\n"
    "WHERE (uo.order_count IS NULL OR uo.order_count < 100)\n"
    "ORDER BY uo.total_spent DESC NULLS LAST, au.created_at\n"
    "LIMIT 1000 OFFSET 0\n";

static uint64_t monotonic_nanoseconds(void) {
    struct timespec value;
    if (clock_gettime(CLOCK_MONOTONIC, &value) != 0) {
        perror("clock_gettime");
        exit(EXIT_FAILURE);
    }
    return (uint64_t)value.tv_sec * UINT64_C(1000000000) + (uint64_t)value.tv_nsec;
}

static uint64_t environment_uint64(
    const char *name,
    uint64_t default_value,
    uint64_t minimum,
    uint64_t maximum
) {
    const char *raw = getenv(name);
    if (raw == NULL || raw[0] == '\0') {
        return default_value;
    }
    errno = 0;
    char *end = NULL;
    unsigned long long value = strtoull(raw, &end, 10);
    if (errno != 0 || end == raw || *end != '\0' || value < minimum || value > maximum) {
        fprintf(stderr, "%s must be an integer from %" PRIu64 " to %" PRIu64 "\n", name, minimum, maximum);
        exit(EXIT_FAILURE);
    }
    return (uint64_t)value;
}

static polyglot_result_t invoke(const struct benchmark_case *benchmark) {
    switch (benchmark->operation) {
    case OP_PARSE:
        return polyglot_parse(benchmark->sql, benchmark->source_dialect);
    case OP_TRANSPILE:
        return polyglot_transpile(
            benchmark->sql,
            benchmark->source_dialect,
            benchmark->target_dialect
        );
    case OP_FORMAT:
        return polyglot_format(benchmark->sql, benchmark->source_dialect);
    }
    fputs("unknown benchmark operation\n", stderr);
    exit(EXIT_FAILURE);
}

static void fail_result(const struct benchmark_case *benchmark, polyglot_result_t result) {
    fprintf(
        stderr,
        "%s/%s failed with status %" PRId32 ": %s\n",
        benchmark->operation_name,
        benchmark->case_name,
        result.status,
        result.error == NULL ? "unknown error" : result.error
    );
    polyglot_free_result(result);
    exit(EXIT_FAILURE);
}

static uint64_t run_iterations(const struct benchmark_case *benchmark, uint64_t iterations) {
    uint64_t started = monotonic_nanoseconds();
    for (uint64_t index = 0; index < iterations; index++) {
        polyglot_result_t result = invoke(benchmark);
        if (result.status != STATUS_SUCCESS || result.data == NULL) {
            fail_result(benchmark, result);
        }
        result_sink ^= (unsigned char)result.data[0];
        polyglot_free_result(result);
    }
    return monotonic_nanoseconds() - started;
}

static uint64_t calibrated_iterations(
    const struct benchmark_case *benchmark,
    uint64_t sample_time
) {
    uint64_t calibration_time = sample_time / UINT64_C(10);
    if (calibration_time < UINT64_C(1000000)) {
        calibration_time = UINT64_C(1000000);
    }
    uint64_t iterations = 1;
    uint64_t elapsed = 0;

    do {
        elapsed = run_iterations(benchmark, iterations);
        if (elapsed < calibration_time) {
            iterations *= 2;
        }
    } while (elapsed < calibration_time);

    long double scaled = (long double)iterations * (long double)sample_time / (long double)elapsed;
    if (scaled < 1.0L) {
        return 1;
    }
    return (uint64_t)scaled;
}

static void run_benchmark(const struct benchmark_case *benchmark) {
    uint64_t samples = environment_uint64("POLYGLOT_BENCH_SAMPLES", 5, 1, 100);
    uint64_t duration_ms = environment_uint64(
        "POLYGLOT_BENCH_DURATION_MS", 1000, 1, 60000
    );
    uint64_t sample_time = duration_ms * UINT64_C(1000000);
    uint64_t iterations = calibrated_iterations(benchmark, sample_time);
    for (uint64_t sample = 1; sample <= samples; sample++) {
        uint64_t elapsed = run_iterations(benchmark, iterations);
        double nanoseconds_per_operation = (double)elapsed / (double)iterations;
        printf(
            "PolyglotFFI/%s/%s sample=%" PRIu64 " iterations=%" PRIu64 " bytes=%zu %.0f ns/op\n",
            benchmark->operation_name,
            benchmark->case_name,
            sample,
            iterations,
            strlen(benchmark->sql),
            nanoseconds_per_operation
        );
    }
}

int main(int argc, char **argv) {
    if (argc == 6 && strcmp(argv[1], "--fixture") == 0) {
        const struct benchmark_case fixture = {
            OP_TRANSPILE,
            "FixtureTranspile",
            argv[2],
            argv[5],
            argv[3],
            argv[4],
        };
        setvbuf(stdout, NULL, _IOLBF, 0);
        run_benchmark(&fixture);
        return result_sink == UINT32_MAX ? EXIT_FAILURE : EXIT_SUCCESS;
    }
    if (argc != 1) {
        fprintf(
            stderr,
            "usage: %s [--fixture NAME SOURCE_DIALECT TARGET_DIALECT SQL]\n",
            argv[0]
        );
        return EXIT_FAILURE;
    }

    const struct benchmark_case benchmarks[] = {
        {OP_PARSE, "Parse", "simple", simple_select, "generic", NULL},
        {OP_PARSE, "Parse", "medium", polyglot_parse_medium, "generic", NULL},
        {OP_PARSE, "Parse", "complex", polyglot_parse_complex, "generic", NULL},
        {OP_TRANSPILE, "Transpile", "simple", simple_select, "postgres", "mysql"},
        {OP_TRANSPILE, "Transpile", "medium", medium_select, "postgres", "mysql"},
        {OP_TRANSPILE, "Transpile", "complex", complex_select, "postgres", "mysql"},
        {OP_FORMAT, "Format", "simple", simple_select, "generic", NULL},
        {OP_FORMAT, "Format", "medium", medium_select, "generic", NULL},
        {OP_FORMAT, "Format", "complex", complex_select, "generic", NULL},
    };

    setvbuf(stdout, NULL, _IOLBF, 0);
    printf("Polyglot FFI %s\n", polyglot_version());
    for (size_t index = 0; index < sizeof(benchmarks) / sizeof(benchmarks[0]); index++) {
        run_benchmark(&benchmarks[index]);
    }
    return result_sink == UINT32_MAX ? EXIT_FAILURE : EXIT_SUCCESS;
}
