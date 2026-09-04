package typeoracle

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/renart-data/golyglot/pkg/golyglot"
)

//go:embed testdata/duckdb-v1.5.json
var duckDBCuratedCases []byte

func CuratedCases() ([]QueryCase, error) {
	var cases []QueryCase
	if err := json.Unmarshal(duckDBCuratedCases, &cases); err != nil {
		return nil, fmt.Errorf("decode curated DuckDB cases: %w", err)
	}
	for index := range cases {
		cases[index].Dialect = golyglot.DialectDuckDB
		cases[index].Schema = oracleSchema()
		cases[index].GeneratorVersion = GeneratorVersion
		cases[index].Source = CaseSourceCurated
		cases[index].Features = normalizedFeatures(cases[index].Features...)
		if cases[index].ID == "" || cases[index].SQL == "" || cases[index].Expected == nil {
			return nil, fmt.Errorf("curated DuckDB case %d is incomplete", index)
		}
	}
	return cases, nil
}
