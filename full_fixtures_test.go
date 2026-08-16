package golyglot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The full compatibility corpus is opt-in because it is intentionally much
// larger than the normal package test suite. Run it with:
//
//	GOLYGLOT_FULL_FIXTURES=1 go test -run '^TestSQLGlotFullFixtures$' ./...
//
// The test remains pure Go; the released Polyglot FFI is used separately as
// an oracle when comparing versioned behavior, never as a Go test dependency.

type fullDialectFixture struct {
	Dialect       string                  `json:"dialect"`
	Identity      []fullIdentityCase      `json:"identity"`
	Transpilation []fullTranspilationCase `json:"transpilation"`
}

type fullIdentityCase struct {
	SQL      string  `json:"sql"`
	Expected *string `json:"expected"`
}

type fullTranspilationCase struct {
	SQL   string            `json:"sql"`
	Read  map[string]string `json:"read"`
	Write map[string]string `json:"write"`
}

type fullParserFixture struct {
	Roundtrips []struct {
		SQL      string  `json:"sql"`
		Expected string  `json:"expected"`
		Read     *string `json:"read"`
		Write    *string `json:"write"`
	} `json:"roundtrips"`
	Errors []struct {
		SQL  string  `json:"sql"`
		Read *string `json:"read"`
	} `json:"errors"`
}

type fullTranspileFixture struct {
	Normalization []struct {
		SQL      string `json:"sql"`
		Expected string `json:"expected"`
	} `json:"normalization"`
	Transpilation []struct {
		SQL      string  `json:"sql"`
		Expected string  `json:"expected"`
		Read     *string `json:"read"`
		Write    *string `json:"write"`
	} `json:"transpilation"`
}

type fullCustomFixture struct {
	Dialect       string                  `json:"dialect"`
	Identity      []fullIdentityCase      `json:"identity"`
	Transpilation []fullTranspilationCase `json:"transpilation"`
}

type fullFixtureFailure struct {
	ID   string
	SQL  string
	Want string
	Got  string
	Err  error
}

type fullFixtureStats struct {
	Passed   int
	Failed   int
	Failures []fullFixtureFailure
}

func (s *fullFixtureStats) record(id, sql, want, got string, err error) {
	if err == nil && got == want {
		s.Passed++
		return
	}
	s.Failed++
	if len(s.Failures) < 50 {
		s.Failures = append(s.Failures, fullFixtureFailure{ID: id, SQL: sql, Want: want, Got: got, Err: err})
	}
}

func (s *fullFixtureStats) merge(other fullFixtureStats) {
	s.Passed += other.Passed
	s.Failed += other.Failed
	for _, failure := range other.Failures {
		if len(s.Failures) == 50 {
			break
		}
		s.Failures = append(s.Failures, failure)
	}
}

func (s fullFixtureStats) total() int { return s.Passed + s.Failed }

func (s fullFixtureStats) summary() string {
	if s.total() == 0 {
		return "0/0 (no cases)"
	}
	return fmt.Sprintf("%d/%d (%.1f%%)", s.Passed, s.total(), 100*float64(s.Passed)/float64(s.total()))
}

func TestSQLGlotFullFixtures(t *testing.T) {
	if os.Getenv("GOLYGLOT_FULL_FIXTURES") != "1" {
		t.Skip("set GOLYGLOT_FULL_FIXTURES=1 to run the complete checked-in compatibility corpus")
	}

	root := sqlglotFixturePath("")
	var total fullFixtureStats

	identity := runFullGenericIdentity(t, filepath.Join(root, "identity.json"))
	t.Logf("SQLGlot generic identity: %s", identity.summary())
	total.merge(identity)

	parserStats := runFullParserFixtures(t, filepath.Join(root, "parser.json"))
	t.Logf("SQLGlot parser: %s", parserStats.summary())
	total.merge(parserStats)

	transpileStats := runFullTranspileFixtures(t, filepath.Join(root, "transpile.json"))
	t.Logf("SQLGlot generic transpile/normalization: %s", transpileStats.summary())
	total.merge(transpileStats)

	prettyStats := runFullPrettyFixtures(t, filepath.Join(root, "pretty.json"))
	t.Logf("SQLGlot pretty: %s", prettyStats.summary())
	total.merge(prettyStats)

	entries, err := os.ReadDir(filepath.Join(root, "dialects"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		stats := runFullDialectFixtures(t, filepath.Join(root, "dialects", entry.Name()), name)
		t.Logf("SQLGlot dialect %s: %s", name, stats.summary())
		total.merge(stats)
	}

	customRoot := filepath.Join("testdata", "polyglot", "custom_fixtures")
	customEntries, err := os.ReadDir(customRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, dialectEntry := range customEntries {
		if !dialectEntry.IsDir() {
			continue
		}
		dialectRoot := filepath.Join(customRoot, dialectEntry.Name())
		fileEntries, err := os.ReadDir(dialectRoot)
		if err != nil {
			t.Fatal(err)
		}
		for _, fileEntry := range fileEntries {
			if fileEntry.IsDir() || filepath.Ext(fileEntry.Name()) != ".json" {
				continue
			}
			name := strings.TrimSuffix(fileEntry.Name(), ".json")
			stats := runFullCustomFixture(t, filepath.Join(dialectRoot, fileEntry.Name()), dialectEntry.Name(), name)
			t.Logf("Polyglot custom %s/%s: %s", dialectEntry.Name(), name, stats.summary())
			total.merge(stats)
		}
	}

	t.Logf("SQLGlot/Polyglot full corpus: %s", total.summary())
	if total.Failed > 0 {
		var details strings.Builder
		for _, failure := range total.Failures {
			fmt.Fprintf(&details, "\n- %s: %s", failure.ID, formatFullFailure(failure))
		}
		t.Fatalf("full fixture run has %d failures (first %d):%s", total.Failed, len(total.Failures), details.String())
	}
}

func runFullGenericIdentity(t *testing.T, path string) fullFixtureStats {
	fixture := readFullJSON[identityFixture](t, path)
	var stats fullFixtureStats
	for index, test := range fixture.Tests {
		got, err := generateFullGeneric(test.SQL, false)
		stats.record(fmt.Sprintf("generic identity:%d", index), test.SQL, test.SQL, got, err)
	}
	return stats
}

func runFullParserFixtures(t *testing.T, path string) fullFixtureStats {
	fixture := readFullJSON[fullParserFixture](t, path)
	var stats fullFixtureStats
	for index, test := range fixture.Roundtrips {
		id := fmt.Sprintf("parser roundtrip:%d", index)
		var got string
		var err error
		if test.Read != nil || test.Write != nil {
			from := fullDialectOrGeneric(test.Read)
			to := fullDialectOrGeneric(test.Write)
			got, err = fullTranspile(test.SQL, from, to, test.Expected)
		} else {
			got, err = fullTranspile(test.SQL, DialectGeneric, DialectGeneric, test.Expected)
		}
		stats.record(id, test.SQL, test.Expected, got, err)
	}
	for index, test := range fixture.Errors {
		dialect := fullDialectOrGeneric(test.Read)
		_, err := ParseStrict(test.SQL, dialect)
		if err == nil {
			stats.record(fmt.Sprintf("parser error:%d", index), test.SQL, "parse error", "parsed successfully", fmt.Errorf("expected parse error"))
		} else {
			stats.Passed++
		}
	}
	return stats
}

func runFullTranspileFixtures(t *testing.T, path string) fullFixtureStats {
	fixture := readFullJSON[fullTranspileFixture](t, path)
	var stats fullFixtureStats
	for index, test := range fixture.Normalization {
		got, err := fullTranspile(test.SQL, DialectGeneric, DialectGeneric, test.Expected)
		stats.record(fmt.Sprintf("normalization:%d", index), test.SQL, test.Expected, got, err)
	}
	for index, test := range fixture.Transpilation {
		var from, to Dialect
		switch {
		case test.Write != nil:
			from, to = DialectGeneric, fullDialectOrGeneric(test.Write)
		case test.Read != nil:
			from, to = fullDialectOrGeneric(test.Read), DialectGeneric
		default:
			continue
		}
		got, err := fullTranspile(test.SQL, from, to, test.Expected)
		stats.record(fmt.Sprintf("transpile:%d", index), test.SQL, test.Expected, got, err)
	}
	return stats
}

func runFullPrettyFixtures(t *testing.T, path string) fullFixtureStats {
	type prettyFixture struct {
		Tests []struct {
			Input    string `json:"input"`
			Expected string `json:"expected"`
		} `json:"tests"`
	}
	fixture := readFullJSON[prettyFixture](t, path)
	var stats fullFixtureStats
	for index, test := range fixture.Tests {
		got, err := FormatOne(test.Input, DialectGeneric)
		stats.record(fmt.Sprintf("pretty:%d", index), test.Input, strings.TrimSpace(test.Expected), strings.TrimSpace(got), err)
	}
	return stats
}

func runFullDialectFixtures(t *testing.T, path, fileDialect string) fullFixtureStats {
	if fileDialect == "dax" || fileDialect == "pipe_syntax" || fileDialect == "prql" {
		// These SQLGlot source grammars are not part of the Go dialect set.
		return fullFixtureStats{}
	}
	fixture := readFullJSON[fullDialectFixture](t, path)
	dialectName := fixture.Dialect
	if dialectName == "" {
		dialectName = fileDialect
	}
	dialect, err := ParseDialect(dialectName)
	if err != nil {
		if len(fixture.Identity) == 0 && len(fixture.Transpilation) == 0 {
			return fullFixtureStats{}
		}
		t.Fatalf("%s: %v", path, err)
	}

	var stats fullFixtureStats
	var identityStats fullFixtureStats
	for index, test := range fixture.Identity {
		want := test.SQL
		if test.Expected != nil {
			want = *test.Expected
		}
		got, err := fullTranspile(test.SQL, dialect, dialect, want)
		identityStats.record(fmt.Sprintf("%s identity:%d", fileDialect, index), test.SQL, want, got, err)
	}
	stats.merge(identityStats)
	var transpilationStats fullFixtureStats
	for index, test := range fixture.Transpilation {
		for targetName, want := range test.Write {
			target, err := ParseDialect(targetName)
			if err != nil {
				transpilationStats.record(fmt.Sprintf("%s write %s:%d", fileDialect, targetName, index), test.SQL, want, "", err)
				continue
			}
			got, err := fullTranspile(test.SQL, dialect, target, want)
			transpilationStats.record(fmt.Sprintf("%s write %s:%d", fileDialect, targetName, index), test.SQL, want, got, err)
		}
		for sourceName, sourceSQL := range test.Read {
			source, err := ParseDialect(sourceName)
			if err != nil {
				transpilationStats.record(fmt.Sprintf("%s read %s:%d", fileDialect, sourceName, index), sourceSQL, test.SQL, "", err)
				continue
			}
			got, err := fullTranspile(sourceSQL, source, dialect, test.SQL)
			transpilationStats.record(fmt.Sprintf("%s read %s:%d", fileDialect, sourceName, index), sourceSQL, test.SQL, got, err)
		}
	}
	stats.merge(transpilationStats)
	t.Logf("%s identity %s; transpilation %s", fileDialect, identityStats.summary(), transpilationStats.summary())
	return stats
}

func runFullCustomFixture(t *testing.T, path, dialectName, category string) fullFixtureStats {
	fixture := readFullJSON[fullCustomFixture](t, path)
	dialect, err := ParseDialect(fixture.Dialect)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var stats fullFixtureStats
	for index, test := range fixture.Identity {
		want := test.SQL
		if test.Expected != nil {
			want = *test.Expected
		}
		got, err := fullTranspile(test.SQL, dialect, dialect, want)
		stats.record(fmt.Sprintf("custom %s/%s identity:%d", dialectName, category, index), test.SQL, want, got, err)
	}
	for index, test := range fixture.Transpilation {
		for targetName, want := range test.Write {
			target, err := ParseDialect(targetName)
			if err != nil {
				stats.record(fmt.Sprintf("custom %s/%s write %s:%d", dialectName, category, targetName, index), test.SQL, want, "", err)
				continue
			}
			got, err := fullTranspile(test.SQL, dialect, target, want)
			stats.record(fmt.Sprintf("custom %s/%s write %s:%d", dialectName, category, targetName, index), test.SQL, want, got, err)
		}
		for sourceName, sourceSQL := range test.Read {
			source, err := ParseDialect(sourceName)
			if err != nil {
				stats.record(fmt.Sprintf("custom %s/%s read %s:%d", dialectName, category, sourceName, index), sourceSQL, test.SQL, "", err)
				continue
			}
			got, err := fullTranspile(sourceSQL, source, dialect, test.SQL)
			stats.record(fmt.Sprintf("custom %s/%s read %s:%d", dialectName, category, sourceName, index), sourceSQL, test.SQL, got, err)
		}
	}
	return stats
}

func fullDialectOrGeneric(name *string) Dialect {
	if name == nil {
		return DialectGeneric
	}
	dialect, err := ParseDialect(*name)
	if err != nil {
		return Dialect(*name)
	}
	return dialect
}

func generateFullGeneric(sql string, pretty bool) (string, error) {
	result, err := ParseStrict(sql, DialectGeneric)
	if err != nil {
		return "", err
	}
	if len(result.Statements) != 1 {
		return "", fmt.Errorf("expected one statement, got %d", len(result.Statements))
	}
	return GenerateWithOptions(result.Statements[0].Node, GenerateOptions{Pretty: pretty})
}

func fullTranspile(sql string, from, to Dialect, expected string) (string, error) {
	outputs, err := TranspileWithOptions(sql, from, to, TranspileOptions{Pretty: hasFormattingNewline(expected)})
	if err != nil {
		return "", err
	}
	if len(outputs) != 1 {
		return "", fmt.Errorf("expected one statement, got %d", len(outputs))
	}
	return outputs[0], nil
}

func formatFullFailure(failure fullFixtureFailure) string {
	if failure.Err != nil {
		return fmt.Sprintf("%v (sql=%q)", failure.Err, failure.SQL)
	}
	return fmt.Sprintf("want %q, got %q (sql=%q)", failure.Want, failure.Got, failure.SQL)
}

func readFullJSON[T any](t *testing.T, path string) T {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture T
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return fixture
}

func hasFormattingNewline(sql string) bool {
	inString := false
	inLineComment := false
	inBlockComment := false
	for index := 0; index < len(sql); index++ {
		current := sql[index]
		var next byte
		if index+1 < len(sql) {
			next = sql[index+1]
		}
		if inLineComment {
			if current == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if current == '*' && next == '/' {
				inBlockComment = false
				index++
			}
			continue
		}
		if !inString && current == '-' && next == '-' {
			inLineComment = true
			index++
			continue
		}
		if !inString && current == '/' && next == '*' {
			inBlockComment = true
			index++
			continue
		}
		if current == '\'' {
			if inString && index+1 < len(sql) && sql[index+1] == '\'' {
				index++
			} else {
				inString = !inString
			}
			continue
		}
		if current == '\n' && !inString {
			return true
		}
	}
	return false
}
