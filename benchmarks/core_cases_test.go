package benchmarks_test

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tobilg/golyglot"
)

func TestCoreComparisonCases(t *testing.T) {
	manifest, err := os.Open(filepath.Join("core_cases", "cases.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	defer manifest.Close()

	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(manifest)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 3 {
			t.Fatalf("invalid core benchmark row %q", scanner.Text())
		}
		operation, name, sqlName := fields[0], fields[1], fields[2]
		key := operation + "/" + name
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate core benchmark %q", key)
		}
		seen[key] = struct{}{}
		if filepath.Base(sqlName) != sqlName || filepath.Ext(sqlName) != ".sql" {
			t.Fatalf("invalid SQL filename %q", sqlName)
		}
		input, readErr := os.ReadFile(filepath.Join("core_cases", sqlName))
		if readErr != nil {
			t.Fatalf("read %s: %v", key, readErr)
		}

		t.Run(key, func(t *testing.T) {
			sql := string(input)
			switch operation {
			case "parse":
				result, parseErr := golyglot.ParseStrict(sql, golyglot.DialectGeneric)
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				if len(result.Statements) != 1 {
					t.Fatalf("got %d statements, want 1", len(result.Statements))
				}
			case "transpile":
				result, transpileErr := golyglot.TranspileOne(
					sql,
					golyglot.DialectPostgreSQL,
					golyglot.DialectMySQL,
				)
				if transpileErr != nil {
					t.Fatal(transpileErr)
				}
				if result == "" {
					t.Fatal("transpilation returned an empty result")
				}
			default:
				t.Fatalf("unsupported operation %q", operation)
			}
		})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 6 {
		t.Fatalf("got %d core benchmark cases, want 6", len(seen))
	}
}
