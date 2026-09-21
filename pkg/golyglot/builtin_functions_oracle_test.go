package golyglot

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This is deliberately a curated, side-effect-free set of invocations, not an
// attempt to execute every function found in a catalog. No user data is used.
func TestBuiltinFunctionEngineObservations(t *testing.T) {
	if os.Getenv("GOLYGLOT_FUNCTION_ORACLE") != "1" {
		t.Skip("opt-in disposable engine observations")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	duckQuery := `WITH sample AS (SELECT 2 AS rounding) SELECT typeof(abs(-3)) a, typeof(sum(i)) b, typeof(count(*)) c, typeof(lower('X')) d, typeof(row_number() OVER ()) e, typeof(range(3)) f, (SELECT typeof(range) FROM range(1)) g, (SELECT round(round(rounding))::VARCHAR FROM sample) h, typeof(coalesce(NULL::INTEGER, 1)) i, typeof(coalesce(NULL::VARCHAR, 'x')) j, typeof(nullif(1, 2.2)) k, typeof(greatest(1, 2.2)) l, typeof(least(1, 2.2)) m FROM (VALUES (1::INTEGER)) v(i)`
	pgQuery := `WITH sample AS (SELECT 2 AS rounding) SELECT row_to_json(t) FROM (SELECT pg_typeof(abs(-3))::text a, pg_typeof(sum(i))::text b, pg_typeof(count(*))::text c, pg_typeof(lower('X'))::text d, pg_typeof(row_number() OVER ())::text e, (SELECT pg_typeof(generate_series)::text FROM generate_series(1,1)) f, (SELECT pg_typeof(unnest)::text FROM unnest(ARRAY[1])) g, (SELECT round(round(rounding))::text FROM sample) h, pg_typeof(coalesce(NULL::integer, 1))::text i, pg_typeof(coalesce(NULL::text, 'x'))::text j, pg_typeof(nullif(1, 2.2))::text k, pg_typeof(greatest(1, 2.2))::text l, pg_typeof(least(1, 2.2))::text m FROM (VALUES(1::INTEGER)) v(i)) t`
	chQuery := `WITH sample AS (SELECT 2 AS rounding) SELECT toTypeName(abs(toInt32(-3))) a, toTypeName(sum(toUInt8(number))) b, toTypeName(count()) c, toTypeName(lower('X')) d, toTypeName(row_number() OVER ()) e, toTypeName(range(toUInt64(3))) f, (SELECT toTypeName(number) FROM numbers(1)) g, (SELECT toString(round(round(rounding))) FROM sample) h, toTypeName(coalesce(CAST(NULL, 'Nullable(Int32)'), toInt32(1))) i, toTypeName(coalesce(CAST(NULL, 'Nullable(String)'), 'x')) j, toTypeName(nullIf(toInt32(1), toFloat64(2.2))) k, toTypeName(greatest(toInt32(1), toFloat64(2.2))) l, toTypeName(least(toInt32(1), toFloat64(2.2))) m FROM numbers(1) FORMAT JSONEachRow`
	for _, tc := range []struct {
		name  string
		cmd   []string
		want  map[string]string
		array bool
	}{
		{"duckdb", []string{"duckdb", "-safe", "-no-init", "-batch", "-bail", "-json", "-c", duckQuery}, map[string]string{"a": "INTEGER", "b": "HUGEINT", "c": "BIGINT", "d": "VARCHAR", "e": "BIGINT", "f": "BIGINT[]", "g": "BIGINT", "h": "2", "i": "INTEGER", "j": "VARCHAR", "k": "INTEGER", "l": "DECIMAL(11,1)", "m": "DECIMAL(11,1)"}, true},
		{"postgresql", []string{"docker", "exec", os.Getenv("GOLYGLOT_POSTGRES_CONTAINER"), "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-At", "-c", pgQuery}, map[string]string{"a": "integer", "b": "bigint", "c": "bigint", "d": "text", "e": "bigint", "f": "integer", "g": "integer", "h": "2", "i": "integer", "j": "text", "k": "numeric", "l": "numeric", "m": "numeric"}, false},
		{"clickhouse", []string{"docker", "run", "--rm", "--network", "none", "--memory", "512m", "--cpus", "1", "--entrypoint", "clickhouse", "clickhouse/clickhouse-server@sha256:0152dd511befe6a2c2ef53e930726179669b08116da78500b37c51c96ff5ee77", "local", "--max_threads", "1", "--background_schedule_pool_size", "1", "--path", "/tmp/function-oracle", "--query", chQuery}, map[string]string{"a": "UInt32", "b": "UInt64", "c": "UInt64", "d": "String", "e": "UInt64", "f": "Array(UInt64)", "g": "UInt64", "h": "2", "i": "Int32", "j": "String", "k": "Nullable(Int32)", "l": "Float64", "m": "Float64"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "postgresql" && os.Getenv("GOLYGLOT_POSTGRES_CONTAINER") == "" {
				t.Fatal("set GOLYGLOT_POSTGRES_CONTAINER to a disposable fixture")
			}
			output, err := exec.CommandContext(ctx, tc.cmd[0], tc.cmd[1:]...).CombinedOutput()
			if err != nil {
				t.Fatalf("engine observation: %v: %s", err, output)
			}
			var got map[string]string
			if tc.array {
				var rows []map[string]string
				err = json.Unmarshal(output, &rows)
				if len(rows) == 1 {
					got = rows[0]
				}
			} else {
				err = json.Unmarshal(output, &got)
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("observed %v, want %v (decode %v)", got, tc.want, err)
			}
			t.Logf("thirteen scalar, aggregate, window, table-function and grammar observations: %v", got)
		})
	}
}

func TestBuiltinDuckDBNamedOptions(t *testing.T) {
	if os.Getenv("GOLYGLOT_FUNCTION_ORACLE") != "1" {
		t.Skip("opt-in disposable engine observations")
	}
	path := filepath.Join(t.TempDir(), "costs.csv")
	if err := os.WriteFile(path, []byte("cost\n1.25\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	query := "SELECT typeof(cost) AS type, cost FROM read_csv('" + strings.ReplaceAll(path, "'", "''") + "', delim = ',', columns = {'cost': 'DOUBLE'}, header = true)"
	// This one query needs file access, restricted to the synthetic test fixture;
	// all other observations use safe mode and constants only.
	output, err := exec.CommandContext(ctx, "duckdb", "-no-init", "-batch", "-bail", "-json", "-c", query).CombinedOutput()
	if err != nil {
		t.Fatalf("named option observation: %v: %s", err, output)
	}
	var rows []struct {
		Type string  `json:"type"`
		Cost float64 `json:"cost"`
	}
	if err := json.Unmarshal(output, &rows); err != nil || len(rows) != 1 || rows[0].Type != "DOUBLE" || rows[0].Cost != 1.25 {
		t.Fatalf("unexpected CSV schema/value: %s (%v)", output, err)
	}
}
