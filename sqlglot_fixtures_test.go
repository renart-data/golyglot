package golyglot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type sqlglotFixtureFile struct {
	Dialect       string            `json:"dialect"`
	Identity      []json.RawMessage `json:"identity"`
	Transpilation []json.RawMessage `json:"transpilation"`
}

func TestSQLGlotFixtureCorpusShape(t *testing.T) {
	root := sqlglotFixturePath("")
	root = filepath.Clean(root)

	rootExpectations := map[string]map[string]int{
		"parser.json":   {"roundtrips": 25, "errors": 7},
		"identity.json": {"tests": 977},
		"pretty.json":   {"tests": 23},
		"transpile.json": {
			"normalization": 119,
			"transpilation": 36,
		},
	}
	for name, expected := range rootExpectations {
		path := filepath.Join(root, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		for key, expectedCount := range expected {
			raw, ok := object[key]
			if !ok {
				t.Fatalf("%s is missing %q", path, key)
			}
			var values []json.RawMessage
			if err := json.Unmarshal(raw, &values); err != nil {
				t.Fatalf("decode %s.%s: %v", path, key, err)
			}
			if len(values) != expectedCount {
				t.Fatalf("%s.%s has %d cases, want %d", path, key, len(values), expectedCount)
			}
		}
	}

	expectedDialects := map[string][2]int{
		"athena":      {52, 1},
		"bigquery":    {340, 266},
		"clickhouse":  {285, 74},
		"databricks":  {139, 32},
		"dax":         {0, 0},
		"doris":       {41, 18},
		"dremio":      {42, 7},
		"drill":       {4, 8},
		"druid":       {10, 0},
		"duckdb":      {384, 232},
		"dune":        {2, 0},
		"exasol":      {84, 62},
		"fabric":      {45, 2},
		"generic":     {39, 280},
		"hive":        {61, 91},
		"materialize": {18, 6},
		"mysql":       {291, 98},
		"oracle":      {173, 29},
		"pipe_syntax": {60, 0},
		"postgres":    {371, 87},
		"presto":      {48, 142},
		"prql":        {0, 0},
		"redshift":    {115, 46},
		"risingwave":  {7, 0},
		"singlestore": {106, 96},
		"snowflake":   {822, 528},
		"solr":        {3, 0},
		"spark":       {104, 134},
		"sqlite":      {90, 32},
		"starrocks":   {67, 16},
		"tableau":     {0, 7},
		"teradata":    {74, 24},
		"trino":       {55, 3},
		"tsql":        {214, 200},
	}

	for name, expected := range expectedDialects {
		path := filepath.Join(root, "dialects", name+".json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var fixture sqlglotFixtureFile
		if err := json.Unmarshal(data, &fixture); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if fixture.Dialect == "" && name != "generic" && name != "pipe_syntax" && name != "prql" {
			t.Fatalf("%s has no dialect name", path)
		}
		if len(fixture.Identity) != expected[0] || len(fixture.Transpilation) != expected[1] {
			t.Fatalf("%s has %d identity and %d transpilation cases, want %d and %d", path, len(fixture.Identity), len(fixture.Transpilation), expected[0], expected[1])
		}
	}

	entries, err := os.ReadDir(filepath.Join(root, "dialects"))
	if err != nil {
		t.Fatal(err)
	}
	jsonFiles := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			jsonFiles++
		}
	}
	if jsonFiles != len(expectedDialects) {
		t.Fatalf("dialect fixture directory has %d JSON files, want %d", jsonFiles, len(expectedDialects))
	}
}

func TestSQLGlotFixtureSourceIsPresent(t *testing.T) {
	path := filepath.Join(sqlglotFixturePath(""), "SOURCE.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("SOURCE.md is empty")
	}
	if !strings.Contains(string(data), "d5aa0d493c281398c9fdbc6febd3577f10ceac2f") ||
		!strings.Contains(string(data), "make fixtures") {
		t.Fatalf("SOURCE.md does not identify the fixture source: %s", path)
	}
}

func TestPolyglotCustomFixtureCorpusShape(t *testing.T) {
	root := filepath.Join("testdata", "polyglot", "custom_fixtures", "datafusion")
	expectedIdentity := map[string]int{
		"ddl.json":           22,
		"dml.json":           10,
		"functions.json":     97,
		"identity.json":      54,
		"operators.json":     34,
		"select.json":        30,
		"transpilation.json": 0,
		"types.json":         29,
	}
	identityTotal := 0
	transpilationTotal := 0
	for name, wantIdentity := range expectedIdentity {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read custom fixture %s: %v", name, err)
		}
		var fixture struct {
			Dialect       string            `json:"dialect"`
			Identity      []json.RawMessage `json:"identity"`
			Transpilation []struct {
				Read  map[string]json.RawMessage `json:"read"`
				Write map[string]json.RawMessage `json:"write"`
			} `json:"transpilation"`
		}
		if err := json.Unmarshal(data, &fixture); err != nil {
			t.Fatalf("decode custom fixture %s: %v", name, err)
		}
		if fixture.Dialect != "datafusion" {
			t.Fatalf("custom fixture %s has dialect %q, want datafusion", name, fixture.Dialect)
		}
		if len(fixture.Identity) != wantIdentity {
			t.Fatalf("custom fixture %s has %d identity cases, want %d", name, len(fixture.Identity), wantIdentity)
		}
		identityTotal += len(fixture.Identity)
		for _, test := range fixture.Transpilation {
			transpilationTotal += len(test.Read) + len(test.Write)
		}
	}
	if identityTotal != 276 {
		t.Fatalf("custom fixture identity corpus has %d cases, want 276", identityTotal)
	}
	if transpilationTotal != 347 {
		t.Fatalf("custom fixture transpilation corpus has %d cases, want 347", transpilationTotal)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	jsonFiles := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".json" {
			jsonFiles++
		}
	}
	if jsonFiles != len(expectedIdentity) {
		t.Fatalf("custom fixture directory has %d JSON files, want %d", jsonFiles, len(expectedIdentity))
	}
}
