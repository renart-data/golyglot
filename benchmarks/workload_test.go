package benchmarks_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/renart-data/golyglot/pkg/golyglot"
)

const workloadManifestEnvironment = "GOLYGLOT_BENCH_WORKLOAD"

type workloadManifest struct {
	Cases []workloadReference `json:"cases"`
}

type workloadReference struct {
	Name      string  `json:"name"`
	Operation string  `json:"operation"`
	SQL       string  `json:"sql"`
	Source    string  `json:"source"`
	Target    string  `json:"target"`
	Expected  *string `json:"expected"`
}

type workloadCase struct {
	Name      string
	Operation string
	SQL       string
	Expected  *string
	Source    golyglot.Dialect
	Target    golyglot.Dialect
}

var (
	workloadStringSink    string
	workloadStatementSink int
)

func loadWorkloadCases(path string) ([]workloadCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workload manifest: %w", err)
	}
	var manifest workloadManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("decode workload manifest: %w", err)
	}
	if len(manifest.Cases) == 0 {
		return nil, fmt.Errorf("workload manifest has no cases")
	}

	names := make(map[string]struct{}, len(manifest.Cases))
	cases := make([]workloadCase, 0, len(manifest.Cases))
	for _, reference := range manifest.Cases {
		operation := strings.ToLower(strings.TrimSpace(reference.Operation))
		if reference.Name == "" || reference.SQL == "" || operation == "" {
			return nil, fmt.Errorf("workload case must include name, operation, and SQL: %+v", reference)
		}
		key := operation + "/" + reference.Name
		if _, duplicate := names[key]; duplicate {
			return nil, fmt.Errorf("duplicate workload case %q", key)
		}
		names[key] = struct{}{}

		sourceName := strings.TrimSpace(reference.Source)
		if sourceName == "" {
			sourceName = "generic"
		}
		source, err := golyglot.ParseDialect(sourceName)
		if err != nil {
			return nil, fmt.Errorf("parse source dialect for %q: %w", key, err)
		}
		target := source
		switch operation {
		case "parse", "format":
		case "transpile":
			if strings.TrimSpace(reference.Target) == "" {
				return nil, fmt.Errorf("transpile workload %q has no target dialect", key)
			}
			target, err = golyglot.ParseDialect(reference.Target)
			if err != nil {
				return nil, fmt.Errorf("parse target dialect for %q: %w", key, err)
			}
		default:
			return nil, fmt.Errorf("workload %q has unsupported operation %q", key, operation)
		}
		cases = append(cases, workloadCase{
			Name:      reference.Name,
			Operation: operation,
			SQL:       reference.SQL,
			Expected:  reference.Expected,
			Source:    source,
			Target:    target,
		})
	}
	return cases, nil
}

func BenchmarkWorkload(b *testing.B) {
	manifestPath := strings.TrimSpace(os.Getenv(workloadManifestEnvironment))
	if manifestPath == "" {
		b.Skipf("set %s to benchmark a private workload manifest", workloadManifestEnvironment)
	}
	cases, err := loadWorkloadCases(manifestPath)
	if err != nil {
		b.Fatal(err)
	}
	for _, testCase := range cases {
		b.Run(testCase.Operation+"/"+testCase.Name, func(b *testing.B) {
			benchmarkWorkloadCase(b, testCase)
		})
	}
}

func benchmarkWorkloadCase(b *testing.B, testCase workloadCase) {
	b.Helper()
	var run func() (string, int, error)
	switch testCase.Operation {
	case "parse":
		run = func() (string, int, error) {
			result, err := golyglot.ParseStrict(testCase.SQL, testCase.Source)
			if err != nil {
				return "", 0, err
			}
			return "", len(result.Statements), nil
		}
	case "format":
		run = func() (string, int, error) {
			result, err := golyglot.FormatOne(testCase.SQL, testCase.Source)
			return result, 0, err
		}
	case "transpile":
		run = func() (string, int, error) {
			result, err := golyglot.TranspileOne(testCase.SQL, testCase.Source, testCase.Target)
			return result, 0, err
		}
	}

	result, statements, err := run()
	if err != nil {
		b.Fatal(err)
	}
	if testCase.Expected != nil && result != *testCase.Expected {
		b.Fatalf("preflight output mismatch\nwant: %s\n got: %s", *testCase.Expected, result)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(testCase.SQL)))
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		result, statements, err = run()
		if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	workloadStringSink = result
	workloadStatementSink = statements
}

func TestLoadWorkloadCases(t *testing.T) {
	expected := "SELECT 1"
	data, err := json.Marshal(workloadManifest{Cases: []workloadReference{
		{Name: "parse", Operation: "parse", SQL: "SELECT 1"},
		{Name: "transpile", Operation: "transpile", SQL: "SELECT 1", Source: "postgres", Target: "mysql", Expected: &expected},
	}})
	if err != nil {
		t.Fatal(err)
	}
	path := t.TempDir() + "/workload.json"
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	cases, err := loadWorkloadCases(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cases) != 2 {
		t.Fatalf("got %d workload cases, want 2", len(cases))
	}
	if cases[0].Source != golyglot.DialectGeneric {
		t.Fatalf("default source dialect = %s, want generic", cases[0].Source)
	}
	if cases[1].Source != golyglot.DialectPostgreSQL || cases[1].Target != golyglot.DialectMySQL {
		t.Fatalf("unexpected transpile dialects: %s -> %s", cases[1].Source, cases[1].Target)
	}
}
