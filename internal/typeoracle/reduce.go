package typeoracle

import (
	"fmt"
	"strings"

	"github.com/renart-data/golyglot/pkg/golyglot"
)

// ReduceCase removes schema tables and columns that the parsed query cannot
// reference. It deliberately leaves the SQL AST unchanged; expression
// shrinking is a later reducer stage and must preserve the same finding.
func ReduceCase(testCase QueryCase) (QueryCase, error) {
	parsed, err := golyglot.ParseStrict(testCase.SQL, testCase.Dialect)
	if err != nil {
		return QueryCase{}, err
	}
	if len(parsed.Statements) != 1 {
		return QueryCase{}, fmt.Errorf("reduce case expects exactly one statement, found %d", len(parsed.Statements))
	}
	if testCase.Schema == nil {
		return testCase, nil
	}

	analysis, err := golyglot.AnalyzeQuery(testCase.SQL, golyglot.AnalyzeQueryOptions{Dialect: testCase.Dialect, Schema: testCase.Schema})
	if err != nil {
		return QueryCase{}, err
	}
	usedTables := make(map[string]struct{})
	aliases := make(map[string]string)
	for _, relation := range analysis.BaseTables {
		name := strings.ToLower(relation.Name)
		usedTables[name] = struct{}{}
		if relation.Table != nil {
			usedTables[strings.ToLower(*relation.Table)] = struct{}{}
		}
		if relation.Alias != nil {
			aliases[strings.ToLower(*relation.Alias)] = name
		}
	}

	unqualified := make(map[string]struct{})
	qualified := make(map[string]map[string]struct{})
	for _, reference := range golyglot.Columns(parsed.Statements[0].Node) {
		column := strings.ToLower(reference.Column)
		if reference.Table == "" {
			unqualified[column] = struct{}{}
			continue
		}
		table := strings.ToLower(reference.Table)
		if resolved, ok := aliases[table]; ok {
			table = resolved
		}
		if qualified[table] == nil {
			qualified[table] = make(map[string]struct{})
		}
		qualified[table][column] = struct{}{}
	}

	reducedSchema := &golyglot.ValidationSchema{Strict: testCase.Schema.Strict}
	for _, table := range testCase.Schema.Tables {
		if len(usedTables) > 0 && !oracleTableIsUsed(table, usedTables) {
			continue
		}
		copyTable := table
		copyTable.Columns = nil
		for _, column := range table.Columns {
			columnName := strings.ToLower(column.Name)
			_, keep := unqualified[columnName]
			if !keep {
				keep = oracleQualifiedColumnUsed(table, columnName, qualified)
			}
			if keep {
				copyTable.Columns = append(copyTable.Columns, column)
			}
		}
		if len(copyTable.Columns) == 0 && len(table.Columns) > 0 {
			copyTable.Columns = append(copyTable.Columns, table.Columns[0])
		}
		reducedSchema.Tables = append(reducedSchema.Tables, copyTable)
	}
	reduced := testCase
	reduced.Schema = reducedSchema
	return reduced, nil
}

func oracleTableIsUsed(table golyglot.SchemaTable, used map[string]struct{}) bool {
	names := []string{strings.ToLower(table.Name)}
	if table.Schema != "" {
		names = append(names, strings.ToLower(table.Schema+"."+table.Name))
	}
	names = append(names, lowerStrings(table.Aliases)...)
	for _, name := range names {
		if _, ok := used[name]; ok {
			return true
		}
	}
	return false
}

func oracleQualifiedColumnUsed(table golyglot.SchemaTable, column string, qualified map[string]map[string]struct{}) bool {
	names := []string{strings.ToLower(table.Name)}
	if table.Schema != "" {
		names = append(names, strings.ToLower(table.Schema+"."+table.Name))
	}
	names = append(names, lowerStrings(table.Aliases)...)
	for _, name := range names {
		if _, ok := qualified[name][column]; ok {
			return true
		}
	}
	return false
}

func lowerStrings(values []string) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strings.ToLower(value)
	}
	return result
}
