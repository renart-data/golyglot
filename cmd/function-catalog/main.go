// function-catalog captures builtin metadata from disposable local engines.
// It queries system catalogs only; it never executes discovered functions.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/renart-data/golyglot/pkg/golyglot"
)

type catalogRow struct {
	Name          string                `json:"name"`
	Kind          golyglot.FunctionKind `json:"kind"`
	Names         []string              `json:"argument_names"`
	Types         []string              `json:"argument_types"`
	Result        string                `json:"result"`
	Variadic      string                `json:"variadic"`
	Optional      int                   `json:"optional"`
	Description   string                `json:"description"`
	Syntax        string                `json:"syntax"`
	CaseSensitive bool                  `json:"case_sensitive"`
}

func main() {
	engine := flag.String("engine", "", "duckdb, postgresql or clickhouse")
	duckdb := flag.String("duckdb", "duckdb", "DuckDB CLI")
	container := flag.String("postgres-container", "", "disposable PostgreSQL container name (required for postgresql)")
	image := flag.String("clickhouse-image", "clickhouse/clickhouse-server:25.8", "pinned local ClickHouse image")
	out := flag.String("out", "pkg/golyglot/function_catalogs", "generated catalog directory")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	query := func(sql string) ([]byte, error) {
		var cmd *exec.Cmd
		switch *engine {
		case "duckdb":
			cmd = exec.CommandContext(ctx, *duckdb, "-safe", "-no-init", "-batch", "-bail", "-json", "-c", sql)
		case "postgresql":
			if *container == "" {
				return nil, fmt.Errorf("use an explicitly named disposable PostgreSQL container")
			}
			cmd = exec.CommandContext(ctx, "docker", "exec", *container, "psql", "-X", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres", "-At", "-c", sql)
		case "clickhouse":
			cmd = exec.CommandContext(ctx, "docker", "run", "--rm", "--network", "none", "--memory", "512m", "--cpus", "1", "--entrypoint", "clickhouse", *image, "local", "--max_threads", "1", "--background_schedule_pool_size", "1", "--path", "/tmp/function-catalog", "--query", sql)
		default:
			return nil, fmt.Errorf("unsupported engine %q", *engine)
		}
		data, err := cmd.Output()
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				return nil, fmt.Errorf("%w: %s", err, exit.Stderr)
			}
			return nil, err
		}
		return data, nil
	}
	var rows []catalogRow
	var version, source string
	var err error
	switch *engine {
	case "duckdb":
		var versions []struct {
			Version string `json:"version"`
		}
		err = decodeQuery(query, "SELECT version() AS version", &versions)
		if err == nil && len(versions) > 0 {
			version = versions[0].Version
		}
		if err == nil {
			err = decodeQuery(query, duckdbSQL, &rows)
		}
		source = "https://duckdb.org/docs/current/sql/meta/duckdb_table_functions#duckdb_functions"
	case "postgresql":
		data, e := query("SHOW server_version")
		err, version = e, strings.TrimSpace(string(data))
		if err == nil {
			err = decodeQuery(query, postgresSQL, &rows)
		}
		source = "https://www.postgresql.org/docs/current/catalog-pg-proc.html"
	case "clickhouse":
		data, e := query("SELECT version()")
		err, version = e, strings.TrimSpace(string(data))
		var envelope struct {
			Data []catalogRow `json:"data"`
		}
		if err == nil {
			err = decodeQuery(query, clickhouseSQL, &envelope)
			rows = envelope.Data
		}
		source = "https://clickhouse.com/docs/reference/system-tables/functions"
	default:
		err = fmt.Errorf("choose -engine duckdb, postgresql or clickhouse")
	}
	if err != nil {
		fatal(err)
	}
	if version == "" || len(rows) == 0 {
		fatal(fmt.Errorf("empty engine catalog/version"))
	}
	catalog := normalizeCatalog(golyglot.Dialect(*engine), version, source, rows)
	data, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		fatal(err)
	}
	if err = os.MkdirAll(*out, 0755); err != nil {
		fatal(err)
	}
	if err = os.WriteFile(filepath.Join(*out, *engine+".json"), append(data, '\n'), 0644); err != nil {
		fatal(err)
	}
	fmt.Printf("%s %s: %d functions, %d catalog overloads\n", *engine, version, len(catalog.Functions), len(rows))
}

func decodeQuery(query func(string) ([]byte, error), sql string, target any) error {
	data, err := query(sql)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func normalizeCatalog(dialect golyglot.Dialect, version, source string, rows []catalogRow) golyglot.BuiltinFunctionCatalog {
	result := golyglot.BuiltinFunctionCatalog{Dialect: dialect, EngineVersion: version, Source: source}
	byName := map[string]*golyglot.BuiltinFunction{}
	for _, row := range rows {
		// These window-only names are registered as aggregates by DuckDB and
		// ClickHouse. Registration kind alone must not advertise them in WHERE.
		windowOnly := slices.Contains([]string{"row_number", "rank", "dense_rank", "percent_rank", "cume_dist", "ntile", "lag", "lead", "nth_value", "laginframe", "leadinframe"}, strings.ToLower(row.Name))
		windowOnly = windowOnly || (dialect == golyglot.DialectDuckDB && slices.Contains([]string{"first_value", "last_value"}, strings.ToLower(row.Name)))
		if row.Kind == golyglot.FunctionAggregate && windowOnly {
			row.Kind = golyglot.FunctionWindow
		}
		key := strings.ToLower(row.Name) + ":" + string(row.Kind)
		fn := byName[key]
		if fn == nil {
			// Ship structural observations, not a mirrored engine documentation
			// corpus. Source links retain provenance without bulky copied prose.
			fn = &golyglot.BuiltinFunction{Name: row.Name, Kind: row.Kind, CaseSensitive: row.CaseSensitive}
			byName[key] = fn
		}
		sig := golyglot.BuiltinSignature{ReturnType: row.Result, VariadicType: row.Variadic, Syntax: row.Syntax}
		if dialect == golyglot.DialectPostgreSQL && row.Variadic != "" && len(row.Types) > 0 {
			// pg_proc includes the variadic array in proargtypes. It is not an
			// additional fixed parameter before the repeated element type.
			row.Types = row.Types[:len(row.Types)-1]
		}
		for i, typ := range row.Types {
			name := fmt.Sprintf("arg%d", i+1)
			if i < len(row.Names) && row.Names[i] != "" {
				name = row.Names[i]
			}
			ordinal, hasCol := strings.CutPrefix(name, "col")
			isOrdinal := hasCol && ordinal != "" && strings.Trim(ordinal, "0123456789") == ""
			named := dialect == golyglot.DialectDuckDB && row.Kind == golyglot.FunctionTable && !isOrdinal
			sig.Parameters = append(sig.Parameters, golyglot.BuiltinParameter{Name: name, Type: typ, Optional: i >= len(row.Types)-row.Optional || named, Named: named})
		}
		// ClickHouse syntax is prose, not a structured parameter/type contract.
		if dialect != golyglot.DialectClickHouse || row.Syntax != "" {
			fn.Signatures = append(fn.Signatures, sig)
		}
	}
	for _, fn := range byName {
		slices.SortFunc(fn.Signatures, func(a, b golyglot.BuiltinSignature) int {
			left, _ := json.Marshal(a)
			right, _ := json.Marshal(b)
			return strings.Compare(string(left), string(right))
		})
		fn.Signatures = slices.CompactFunc(fn.Signatures, func(a, b golyglot.BuiltinSignature) bool {
			left, _ := json.Marshal(a)
			right, _ := json.Marshal(b)
			return string(left) == string(right)
		})
		result.Functions = append(result.Functions, *fn)
	}
	slices.SortFunc(result.Functions, func(a, b golyglot.BuiltinFunction) int {
		return strings.Compare(strings.ToLower(a.Name)+":"+string(a.Kind), strings.ToLower(b.Name)+":"+string(b.Kind))
	})
	return result
}

func fatal(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }

const duckdbSQL = `SELECT function_name AS name, function_type AS kind,
parameters AS argument_names, parameter_types AS argument_types,
coalesce(return_type, '') AS result, coalesce(varargs, '') AS variadic,
coalesce(description, '') AS description
FROM duckdb_functions() WHERE internal AND function_type IN ('scalar','aggregate','table')
AND NOT starts_with(function_name, '__') ORDER BY function_name, function_type, parameter_types::VARCHAR`

const postgresSQL = `SELECT coalesce(json_agg(row_to_json(f)), '[]'::json) FROM (
SELECT p.proname AS name,
CASE WHEN p.prokind='a' THEN 'aggregate' WHEN p.prokind='w' THEN 'window'
WHEN p.proretset THEN 'table' ELSE 'scalar' END AS kind,
ARRAY(SELECT format_type(arg, NULL) FROM unnest(p.proargtypes::oid[]) arg) AS argument_types,
pg_get_function_result(p.oid) AS result, p.pronargdefaults AS optional,
CASE WHEN p.provariadic=0 THEN '' ELSE format_type(p.provariadic,NULL) END AS variadic,
coalesce(obj_description(p.oid,'pg_proc'),'') AS description
FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
WHERE n.nspname='pg_catalog' AND p.prokind IN ('f','a','w')
ORDER BY p.proname, p.proargtypes::text) f`

const clickhouseSQL = `SELECT name, if(is_aggregate, 'aggregate', 'scalar') AS kind,
description, syntax, CAST(NOT case_insensitive AS Bool) AS case_sensitive FROM system.functions
UNION ALL SELECT name, 'table' AS kind, '' AS description, '' AS syntax, true AS case_sensitive
FROM system.table_functions ORDER BY name, kind FORMAT JSON`
