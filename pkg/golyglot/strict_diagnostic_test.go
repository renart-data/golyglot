package golyglot

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type polyglotStrictDiagnosticCase struct {
	Dialect string `json:"dialect"`
	SQL     string `json:"sql"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Display string `json:"display"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
}

type polyglotStrictValidationCase struct {
	Dialect string `json:"dialect"`
	SQL     string `json:"sql"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
}

type polyglotStrictFixture struct {
	PolyglotVersion  string                         `json:"polyglot_version"`
	PolyglotCommit   string                         `json:"polyglot_commit"`
	Cases            []polyglotStrictDiagnosticCase `json:"cases"`
	StrictValidation []polyglotStrictValidationCase `json:"strict_validation_cases"`
}

func loadPolyglotStrictFixture(t *testing.T) polyglotStrictFixture {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "polyglot", "strict_diagnostics_v0.9.2.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read strict diagnostic fixture: %v", err)
	}
	var oracle polyglotStrictFixture
	if err := json.Unmarshal(data, &oracle); err != nil {
		t.Fatalf("decode strict diagnostic fixture: %v", err)
	}
	if oracle.PolyglotVersion != "0.9.2" || oracle.PolyglotCommit != "44ab8f9473039f143a2907e0e64001606b3c05e4" {
		t.Fatalf("unexpected Polyglot oracle: version %q commit %q", oracle.PolyglotVersion, oracle.PolyglotCommit)
	}
	return oracle
}

func TestStrictDiagnosticsMatchPolyglot(t *testing.T) {
	oracle := loadPolyglotStrictFixture(t)

	for _, test := range oracle.Cases {
		t.Run(test.Dialect+"/"+test.Kind+"/"+test.Message, func(t *testing.T) {
			dialect, err := ParseDialect(test.Dialect)
			if err != nil {
				t.Fatal(err)
			}
			result, err := ParseStrict(test.SQL, dialect)
			var syntaxError *SyntaxError
			if !errors.As(err, &syntaxError) {
				t.Fatalf("ParseStrict error = %T %v, want *SyntaxError", err, err)
			}
			wantKind := PolyglotErrorParse
			if test.Kind == "tokenize" {
				wantKind = PolyglotErrorTokenize
			}
			got := syntaxError.Polyglot
			if got.Kind != wantKind || got.Message != test.Message || got.Line != test.Line || got.Column != test.Column || got.Span != (Span{Start: test.Start, End: test.End}) {
				t.Fatalf("Polyglot diagnostic = %#v, want kind=%v message=%q line=%d column=%d span=[%d,%d)", got, wantKind, test.Message, test.Line, test.Column, test.Start, test.End)
			}
			if err.Error() != test.Display {
				t.Fatalf("strict error = %q, want %q", err.Error(), test.Display)
			}
			preserved := false
			for _, diagnostic := range result.Diagnostics {
				if diagnostic.Code == syntaxError.Diagnostic.Code && diagnostic.Message == syntaxError.Diagnostic.Message && diagnostic.Span == syntaxError.Diagnostic.Span {
					preserved = true
					break
				}
			}
			if !preserved {
				t.Fatalf("native diagnostic was not preserved: result=%#v error=%#v", result.Diagnostics, syntaxError.Diagnostic)
			}
		})
	}
}

func TestStrictValidationDiagnosticsMatchPolyglot(t *testing.T) {
	oracle := loadPolyglotStrictFixture(t)
	for _, test := range oracle.StrictValidation {
		t.Run(test.Message, func(t *testing.T) {
			dialect, err := ParseDialect(test.Dialect)
			if err != nil {
				t.Fatal(err)
			}
			result := ValidateWithOptions(test.SQL, ValidationOptions{
				Dialect:      dialect,
				StrictSyntax: true,
			})
			if result.Valid || len(result.Errors) != 1 {
				t.Fatalf("strict validation = %#v, want one error", result)
			}
			got := result.Errors[0]
			if got.Code != test.Code || got.Message != test.Message || got.Severity != "error" {
				t.Fatalf("strict validation error = %#v, want code=%q message=%q", got, test.Code, test.Message)
			}
			if got.Line == nil || *got.Line != test.Line || got.Column == nil || *got.Column != test.Column {
				t.Fatalf("strict validation location = %v:%v, want %d:%d", got.Line, got.Column, test.Line, test.Column)
			}
			if got.Start != nil || got.End != nil {
				t.Fatalf("strict E005 offsets = %v:%v, want omitted like Polyglot", got.Start, got.End)
			}
			comma := strings.Index(test.SQL, ",")
			wantSpan := Span{Start: comma, End: comma + 1}
			if got.Span != wantSpan || len(result.Diagnostics) != 1 || result.Diagnostics[0].Code != test.Code || result.Diagnostics[0].Span != wantSpan {
				t.Fatalf("strict validation span/diagnostic = %#v / %#v, want comma span %#v", got.Span, result.Diagnostics, wantSpan)
			}

			permissive := ValidateWithOptions(test.SQL, ValidationOptions{Dialect: dialect})
			if !permissive.Valid {
				t.Fatalf("permissive validation rejected Polyglot-compatible trailing comma: %#v", permissive)
			}
		})
	}
}

func TestStrictProjectionPreservesTolerantDiagnostics(t *testing.T) {
	const sql = "SELECT FROM x ORDER BY"
	strict, err := ParseStrict(sql, DialectGeneric)
	if err == nil {
		t.Fatal("ParseStrict succeeded, want a syntax error")
	}
	tolerant := ParseTolerant(sql, DialectGeneric)
	if !reflect.DeepEqual(strict.Diagnostics, tolerant.Diagnostics) {
		t.Fatalf("strict diagnostics = %#v, tolerant diagnostics = %#v", strict.Diagnostics, tolerant.Diagnostics)
	}
}

func TestStrictDiagnosticUsesByteSpanForUnicode(t *testing.T) {
	result, err := ParseStrict("SELECT 😀 FROM t WHERE", DialectGeneric)
	var syntaxError *SyntaxError
	if !errors.As(err, &syntaxError) {
		t.Fatalf("ParseStrict error = %T %v, want *SyntaxError", err, err)
	}
	want := PolyglotDiagnostic{
		Kind:    PolyglotErrorTokenize,
		Message: "Unexpected character: '😀'",
		Line:    1,
		Column:  9,
		Span:    Span{Start: 7, End: 11},
	}
	if syntaxError.Polyglot != want {
		t.Fatalf("Polyglot diagnostic = %#v, want %#v", syntaxError.Polyglot, want)
	}
	if got := result.SQL[want.Span.Start:want.Span.End]; got != "😀" {
		t.Fatalf("diagnostic source = %q, want emoji", got)
	}
}

func TestStrictDiagnosticPreservesPolyglotOperatorTokenName(t *testing.T) {
	result, err := ParseStrict("SELECT 1 >", DialectGeneric)
	var syntaxError *SyntaxError
	if !errors.As(err, &syntaxError) {
		t.Fatalf("ParseStrict error = %T %v, want *SyntaxError", err, err)
	}
	if got, want := syntaxError.Polyglot.Message, "Unexpected token: Gt"; got != want {
		t.Fatalf("Polyglot diagnostic message = %q, want %q; diagnostics: %#v", got, want, result.Diagnostics)
	}
}
