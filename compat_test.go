package golyglot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type parserFixture struct {
	Roundtrips []struct {
		SQL      string `json:"sql"`
		Expected string `json:"expected"`
		Read     string `json:"read"`
		Write    string `json:"write"`
	} `json:"roundtrips"`
	Errors []struct {
		SQL  string `json:"sql"`
		Read string `json:"read"`
	} `json:"errors"`
}

type identityFixture struct {
	Tests []struct {
		SQL string `json:"sql"`
	} `json:"tests"`
}

func sqlglotFixturePath(name string) string {
	if root := os.Getenv("GOLYGLOT_SQLGLOT_FIXTURES"); root != "" {
		return filepath.Join(root, name)
	}
	return filepath.Join("testdata", "polyglot", "sqlglot_fixtures", name)
}

// TestPolyglotParserFixtureShape consumes the same parser.json shape emitted
// by Polyglot's SQLGlot extraction tool. Generated full fixtures can be placed
// in testdata/polyglot without changing this test.
func TestPolyglotParserFixtureShape(t *testing.T) {
	runParserFixture(t, filepath.Join("testdata", "polyglot", "parser.json"))
}

func TestPolyglotFullParserFixtureIfPresent(t *testing.T) {
	path := sqlglotFixturePath("parser.json")
	if _, err := os.Stat(path); err != nil {
		legacyPath := filepath.Join("testdata", "polyglot", "parser.full.json")
		if _, legacyErr := os.Stat(legacyPath); legacyErr != nil {
			t.Skip("upstream fixture not present; run make fixtures")
		}
		path = legacyPath
	}
	runParserFixture(t, path)
}

func TestSQLGlotParserFixtures(t *testing.T) {
	path := sqlglotFixturePath("parser.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("SQLGlot parser fixture is missing: %v; run make fixtures", err)
	}
	runParserFixture(t, path)
}

func runParserFixture(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read parser fixture: %v", err)
	}
	var fixture parserFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode parser fixture: %v", err)
	}
	for i, test := range fixture.Roundtrips {
		t.Run("roundtrip/"+itoa(i), func(t *testing.T) {
			if test.Read != "" || test.Write != "" {
				t.Skip("dialect-specific fixture awaits dialect generator support")
			}
			result, err := ParseStrict(test.SQL, DialectGeneric)
			if err != nil {
				t.Fatalf("parse %q: %v\n%#v", test.SQL, err, result.Diagnostics)
			}
			if len(result.Statements) != 1 {
				t.Fatalf("got %d statements", len(result.Statements))
			}
			generated, err := GenerateWithOptions(result.Statements[0].Node, GenerateOptions{Pretty: strings.Contains(test.Expected, "\n")})
			if err != nil {
				t.Fatalf("generate %q: %v", test.SQL, err)
			}
			if generated != test.Expected {
				t.Fatalf("generated %q, want %q", generated, test.Expected)
			}
		})
	}
	for i, test := range fixture.Errors {
		t.Run("error/"+itoa(i), func(t *testing.T) {
			_, err := ParseStrict(test.SQL, DialectGeneric)
			if err == nil {
				t.Fatalf("ParseStrict(%q) succeeded, want syntax error", test.SQL)
			}
		})
	}
}

func TestPolyglotFullIdentityFixtureIfPresent(t *testing.T) {
	path := sqlglotFixturePath("identity.json")
	if _, err := os.Stat(path); err != nil {
		legacyPath := filepath.Join("testdata", "polyglot", "identity.full.json")
		if _, legacyErr := os.Stat(legacyPath); legacyErr != nil {
			t.Skip("upstream fixture not present; run make fixtures")
		}
		path = legacyPath
	}
	runIdentityFixture(t, path)
}

func TestSQLGlotIdentityFixtures(t *testing.T) {
	path := sqlglotFixturePath("identity.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("SQLGlot identity fixture is missing: %v; run make fixtures", err)
	}
	runIdentityFixture(t, path)
}

func runIdentityFixture(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture identityFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	failures := make([]string, 0, 20)
	for i, test := range fixture.Tests {
		result, parseErr := ParseStrict(test.SQL, DialectGeneric)
		if parseErr != nil || len(result.Statements) != 1 {
			failures = append(failures, "parse "+itoa(i)+": "+test.SQL)
		} else if generated, generateErr := Generate(result.Statements[0].Node); generateErr != nil || generated != test.SQL {
			failures = append(failures, "generate "+itoa(i)+": got "+generated+" want "+test.SQL)
		}
		if len(failures) == cap(failures) {
			break
		}
	}
	if len(failures) > 0 {
		t.Fatalf("identity fixture failures (first %d):\n%s", len(failures), strings.Join(failures, "\n"))
	}
}

func itoa(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var reversed [20]byte
	i := len(reversed)
	for value > 0 {
		i--
		reversed[i] = digits[value%10]
		value /= 10
	}
	return string(reversed[i:])
}
