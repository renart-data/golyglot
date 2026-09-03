package typeoracle

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/renart-data/golyglot/pkg/golyglot"
)

type DuckDBAdapter struct {
	Executable string
}

func (adapter DuckDBAdapter) Describe(ctx context.Context, testCase QueryCase) ([]Column, error) {
	executable := strings.TrimSpace(adapter.Executable)
	if executable == "" {
		executable = "duckdb"
	}
	schemaSQL, err := duckDBSchemaSQL(testCase.Schema)
	if err != nil {
		return nil, err
	}
	query := strings.TrimSpace(testCase.SQL)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}
	query = strings.TrimSuffix(query, ";")
	script := schemaSQL +
		`CREATE TEMP VIEW "__golyglot_oracle_query" AS ` + query + `; ` +
		`SELECT name, type, "notnull" AS not_null FROM pragma_table_info('__golyglot_oracle_query') ORDER BY cid;`
	command := exec.CommandContext(ctx, executable, "-safe", "-no-init", "-batch", "-bail", "-json", "-c", script)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("duckdb describe: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return parseDuckDBColumns(output)
}

func (adapter DuckDBAdapter) Version(ctx context.Context) (string, error) {
	executable := strings.TrimSpace(adapter.Executable)
	if executable == "" {
		executable = "duckdb"
	}
	output, err := exec.CommandContext(ctx, executable, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("duckdb version: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

type duckDBColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func parseDuckDBColumns(output []byte) ([]Column, error) {
	var raw []duckDBColumn
	if err := json.Unmarshal(output, &raw); err != nil {
		return nil, fmt.Errorf("decode duckdb schema: %w", err)
	}
	columns := make([]Column, 0, len(raw))
	for _, column := range raw {
		columns = append(columns, Column{Name: column.Name, Type: column.Type})
	}
	return columns, nil
}

func duckDBSchemaSQL(schema *golyglot.ValidationSchema) (string, error) {
	if schema == nil || len(schema.Tables) == 0 {
		return "", fmt.Errorf("duckdb oracle requires at least one schema table")
	}
	var result strings.Builder
	for _, table := range schema.Tables {
		if strings.TrimSpace(table.Name) == "" || len(table.Columns) == 0 {
			return "", fmt.Errorf("duckdb oracle requires named tables with columns")
		}
		result.WriteString("CREATE TABLE ")
		result.WriteString(duckDBQuoteIdentifier(table.Name))
		result.WriteString(" (")
		for index, column := range table.Columns {
			if index > 0 {
				result.WriteString(", ")
			}
			if strings.TrimSpace(column.Name) == "" {
				return "", fmt.Errorf("duckdb oracle requires named columns")
			}
			dataType, err := golyglot.ParseDataType(column.Type, golyglot.DialectDuckDB)
			if err != nil || !dataType.Known() {
				return "", fmt.Errorf("invalid DuckDB type %q for %s.%s", column.Type, table.Name, column.Name)
			}
			result.WriteString(duckDBQuoteIdentifier(column.Name))
			result.WriteByte(' ')
			result.WriteString(dataType.SQL())
			if column.Nullable != nil && !*column.Nullable {
				result.WriteString(" NOT NULL")
			}
		}
		result.WriteString("); ")
	}
	return result.String(), nil
}

func duckDBQuoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}
