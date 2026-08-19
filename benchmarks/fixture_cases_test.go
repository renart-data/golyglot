package benchmarks_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/renart-data/golyglot"
)

const (
	fixtureBenchmarkManifestPath = "fixture_cases.json"
	corpusBenchmarkManifestPath  = "corpus_cases.json"
)

type fixtureBenchmarkManifest struct {
	Cases []fixtureBenchmarkReference `json:"cases"`
}

type fixtureBenchmarkReference struct {
	Name    string `json:"name"`
	Feature string `json:"feature"`
	Fixture string `json:"fixture"`
	Index   int    `json:"index"`
	Target  string `json:"target"`
}

type fixtureBenchmarkFile struct {
	Dialect       string `json:"dialect"`
	Transpilation []struct {
		SQL   string            `json:"sql"`
		Write map[string]string `json:"write"`
	} `json:"transpilation"`
}

type fixtureBenchmarkCase struct {
	Name     string
	Feature  string
	SQL      string
	Expected string
	Source   golyglot.Dialect
	Target   golyglot.Dialect
}

var fixtureBenchmarkOutputSink string

func loadFixtureBenchmarkCases(requestedManifestPath string) ([]fixtureBenchmarkCase, error) {
	manifestPath := requestedManifestPath
	manifestData, err := os.ReadFile(manifestPath)
	if os.IsNotExist(err) {
		manifestPath = filepath.Join("benchmarks", requestedManifestPath)
		manifestData, err = os.ReadFile(manifestPath)
	}
	if err != nil {
		return nil, fmt.Errorf("read benchmark manifest: %w", err)
	}
	var manifest fixtureBenchmarkManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("decode benchmark manifest: %w", err)
	}
	if len(manifest.Cases) == 0 {
		return nil, fmt.Errorf("benchmark manifest has no cases")
	}

	fixtureRoot := filepath.Join(filepath.Dir(manifestPath), "..", "testdata", "polyglot", "sqlglot_fixtures")
	files := make(map[string]fixtureBenchmarkFile)
	names := make(map[string]struct{}, len(manifest.Cases))
	cases := make([]fixtureBenchmarkCase, 0, len(manifest.Cases))
	for _, reference := range manifest.Cases {
		if reference.Name == "" || reference.Feature == "" || reference.Target == "" {
			return nil, fmt.Errorf("benchmark reference must include name, feature, and target: %+v", reference)
		}
		if _, duplicate := names[reference.Name]; duplicate {
			return nil, fmt.Errorf("duplicate benchmark name %q", reference.Name)
		}
		names[reference.Name] = struct{}{}

		cleanFixture := filepath.Clean(reference.Fixture)
		if cleanFixture == "." || filepath.IsAbs(cleanFixture) || cleanFixture == ".." || strings.HasPrefix(cleanFixture, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("invalid benchmark fixture path %q", reference.Fixture)
		}
		fixture, ok := files[cleanFixture]
		if !ok {
			fixtureData, readErr := os.ReadFile(filepath.Join(fixtureRoot, cleanFixture))
			if readErr != nil {
				return nil, fmt.Errorf("read fixture %q: %w", cleanFixture, readErr)
			}
			if err := json.Unmarshal(fixtureData, &fixture); err != nil {
				return nil, fmt.Errorf("decode fixture %q: %w", cleanFixture, err)
			}
			files[cleanFixture] = fixture
		}
		if reference.Index < 0 || reference.Index >= len(fixture.Transpilation) {
			return nil, fmt.Errorf("fixture %q has no transpilation index %d", cleanFixture, reference.Index)
		}
		testCase := fixture.Transpilation[reference.Index]
		expected, ok := testCase.Write[reference.Target]
		if !ok {
			return nil, fmt.Errorf("fixture %q index %d has no %q target", cleanFixture, reference.Index, reference.Target)
		}
		source, err := golyglot.ParseDialect(fixture.Dialect)
		if err != nil {
			return nil, fmt.Errorf("parse source dialect for %q: %w", reference.Name, err)
		}
		target, err := golyglot.ParseDialect(reference.Target)
		if err != nil {
			return nil, fmt.Errorf("parse target dialect for %q: %w", reference.Name, err)
		}
		cases = append(cases, fixtureBenchmarkCase{
			Name:     reference.Name,
			Feature:  reference.Feature,
			SQL:      testCase.SQL,
			Expected: expected,
			Source:   source,
			Target:   target,
		})
	}
	return cases, nil
}

func TestFixtureBenchmarkCasesMatch(t *testing.T) {
	testFixtureBenchmarkCasesMatch(t, fixtureBenchmarkManifestPath)
}

func TestCorpusBenchmarkCasesMatch(t *testing.T) {
	testFixtureBenchmarkCasesMatch(t, corpusBenchmarkManifestPath)
}

func testFixtureBenchmarkCasesMatch(t *testing.T, manifestPath string) {
	t.Helper()
	cases, err := loadFixtureBenchmarkCases(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range cases {
		t.Run(testCase.Name, func(t *testing.T) {
			got, err := golyglot.TranspileOne(testCase.SQL, testCase.Source, testCase.Target)
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.Expected {
				t.Fatalf("unexpected fixture output\nwant: %s\n got: %s", testCase.Expected, got)
			}
		})
	}
}

func BenchmarkFixtureTranspile(b *testing.B) {
	benchmarkFixtureTranspile(b, fixtureBenchmarkManifestPath)
}

func BenchmarkCorpusTranspile(b *testing.B) {
	benchmarkFixtureTranspile(b, corpusBenchmarkManifestPath)
}

func benchmarkFixtureTranspile(b *testing.B, manifestPath string) {
	b.Helper()
	cases, err := loadFixtureBenchmarkCases(manifestPath)
	if err != nil {
		b.Fatal(err)
	}
	for _, testCase := range cases {
		b.Run(testCase.Name, func(b *testing.B) {
			got, err := golyglot.TranspileOne(testCase.SQL, testCase.Source, testCase.Target)
			if err != nil {
				b.Fatal(err)
			}
			if got != testCase.Expected {
				b.Fatalf("preflight output mismatch\nwant: %s\n got: %s", testCase.Expected, got)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(testCase.SQL)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				got, err = golyglot.TranspileOne(testCase.SQL, testCase.Source, testCase.Target)
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			fixtureBenchmarkOutputSink = got
		})
	}
}
