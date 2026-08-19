package golyglot

import (
	"strings"
	"testing"
)

func TestLosslessSourceReconstruction(t *testing.T) {
	sql := "\ufeff  -- leading\r\nSELECT\t用户, /* middle */ '😀'  FROM 表;\n\nSELECT 2  \t"
	result := ParseTolerant(sql, DialectGeneric)
	if result.OriginalSQL() != sql {
		t.Fatalf("OriginalSQL() = %q, want %q", result.OriginalSQL(), sql)
	}

	var reconstructed strings.Builder
	for i, token := range result.Tokens {
		gap, ok := result.SourceGapBefore(i)
		if !ok {
			t.Fatalf("SourceGapBefore(%d) was invalid for token %#v", i, token)
		}
		gapText, ok := result.SourceSlice(gap)
		if !ok {
			t.Fatalf("SourceSlice(%#v) rejected a reported gap", gap)
		}
		reconstructed.WriteString(gapText)
		if token.Kind != TokenEOF {
			tokenText, ok := result.SourceSlice(token.Span)
			if !ok {
				t.Fatalf("SourceSlice(%#v) rejected token %d", token.Span, i)
			}
			if tokenText != token.Text {
				t.Fatalf("token %d source = %q, token text = %q", i, tokenText, token.Text)
			}
			reconstructed.WriteString(tokenText)
		}
	}
	if got := reconstructed.String(); got != sql {
		t.Fatalf("reconstructed SQL = %q, want %q", got, sql)
	}
}

func TestParserTokenSplitsDoNotMutateLosslessTokenStream(t *testing.T) {
	sql := "SELECT CAST(value AS ARRAY<ARRAY<INT>>) FROM t"
	result, err := ParseStrict(sql, DialectDatabricks)
	if err != nil {
		t.Fatalf("ParseStrict returned error: %v", err)
	}
	foundDoubleCloser := false
	var reconstructed strings.Builder
	for i, token := range result.Tokens {
		gap, ok := result.SourceGapBefore(i)
		if !ok {
			t.Fatalf("invalid gap before token %d", i)
		}
		gapText, _ := result.SourceSlice(gap)
		reconstructed.WriteString(gapText)
		if token.Kind != TokenEOF {
			tokenText, ok := result.SourceSlice(token.Span)
			if !ok || tokenText != token.Text {
				t.Fatalf("token %d is not source-backed: %#v source=%q", i, token, tokenText)
			}
			reconstructed.WriteString(tokenText)
		}
		foundDoubleCloser = foundDoubleCloser || token.Text == ">>"
	}
	if !foundDoubleCloser {
		t.Fatal("public token stream lost the original >> token")
	}
	if got := reconstructed.String(); got != sql {
		t.Fatalf("reconstructed SQL = %q, want %q", got, sql)
	}
}

func TestApplyEditsPreservesUntouchedSource(t *testing.T) {
	sql := "/* lead */\nSELECT  a,\tb -- keep\r\nFROM t;  \n"
	result, err := ParseStrict(sql, DialectGeneric)
	if err != nil {
		t.Fatalf("ParseStrict returned error: %v", err)
	}

	var aSpan, tableSpan Span
	for _, token := range result.Tokens {
		switch token.Text {
		case "a":
			aSpan = token.Span
		case "t":
			tableSpan = token.Span
		}
	}
	edited, err := result.ApplyEdits(
		TextEdit{Span: tableSpan, NewText: "source_table"},
		TextEdit{Span: aSpan, NewText: "alpha"},
	)
	if err != nil {
		t.Fatalf("ApplyEdits returned error: %v", err)
	}
	want := "/* lead */\nSELECT  alpha,\tb -- keep\r\nFROM source_table;  \n"
	if edited != want {
		t.Fatalf("edited SQL = %q, want %q", edited, want)
	}
}

func TestEditForNodePreservesSurroundingTrivia(t *testing.T) {
	sql := "-- lead\nSELECT  a /* keep */ FROM t\n"
	result, err := ParseStrict(sql, DialectGeneric)
	if err != nil {
		t.Fatalf("ParseStrict returned error: %v", err)
	}
	var target Node
	WalkResult(result, func(node Node) VisitAction {
		identifier, ok := node.(*IdentifierExpr)
		if ok && len(identifier.Parts) == 1 && identifier.Parts[0].Text == "a" {
			target = node
			return Stop
		}
		return VisitChildren
	})
	if target == nil {
		t.Fatal("did not find identifier a")
	}
	edit, err := result.EditForNode(target, "renamed")
	if err != nil {
		t.Fatalf("EditForNode returned error: %v", err)
	}
	edited, err := result.ApplyEdits(edit)
	if err != nil {
		t.Fatalf("ApplyEdits returned error: %v", err)
	}
	if want := "-- lead\nSELECT  renamed /* keep */ FROM t\n"; edited != want {
		t.Fatalf("edited SQL = %q, want %q", edited, want)
	}
}

func TestEditForNodeRejectsSyntheticNode(t *testing.T) {
	result, err := ParseStrict("SELECT r NOTNULL FROM t", DialectPostgreSQL)
	if err != nil {
		t.Fatalf("ParseStrict returned error: %v", err)
	}
	var synthetic Node
	WalkResult(result, func(node Node) VisitAction {
		if node.SourceSpan().IsSynthetic() {
			synthetic = node
			return Stop
		}
		return VisitChildren
	})
	if synthetic == nil {
		t.Fatal("did not find normalized synthetic node")
	}
	if _, err := result.EditForNode(synthetic, "NULL"); err == nil {
		t.Fatal("EditForNode accepted a synthetic node")
	}
}

func TestSuccessfulParseNodeSpansAreSourceBackedOrSynthetic(t *testing.T) {
	tests := []struct {
		dialect Dialect
		sql     string
	}{
		{DialectGeneric, "SELECT a FROM test GROUP BY GROUPING SETS (x, ())"},
		{DialectDatabricks, "SELECT c1:price, c1:price.foo, c1:price.bar[1]"},
		{DialectPostgreSQL, "SELECT INTERVAL '-1 MONTH'"},
		{DialectPostgreSQL, "SELECT r NOTNULL FROM t"},
		{DialectPostgreSQL, "SELECT js IS JSON ARRAY WITH UNIQUE KEYS FROM t"},
		{DialectTSQL, "SELECT TOP 10 PERCENT WITH TIES"},
	}
	for _, test := range tests {
		result, err := ParseStrict(test.sql, test.dialect)
		if err != nil {
			t.Fatalf("ParseStrict(%q, %s): %v", test.sql, test.dialect, err)
		}
		WalkResult(result, func(node Node) VisitAction {
			span := node.SourceSpan()
			if span.IsSynthetic() {
				return VisitChildren
			}
			if !span.Valid(len(test.sql)) || span.Empty() {
				t.Errorf("%T has non-source span %#v for %q", node, span, test.sql)
			}
			return VisitChildren
		})
	}
}

func TestApplyEditsInsertionsAtSameOffsetAreStable(t *testing.T) {
	result := ParseResult{SQL: "SELECT 1"}
	edited, err := result.ApplyEdits(
		TextEdit{Span: Span{Start: 6, End: 6}, NewText: " first"},
		TextEdit{Span: Span{Start: 6, End: 6}, NewText: " second"},
	)
	if err != nil {
		t.Fatalf("ApplyEdits returned error: %v", err)
	}
	if want := "SELECT first second 1"; edited != want {
		t.Fatalf("edited SQL = %q, want %q", edited, want)
	}
}

func TestApplyEditsRejectsInvalidAndOverlappingSpans(t *testing.T) {
	result := ParseResult{SQL: "SELECT 1"}
	tests := []struct {
		name  string
		edits []TextEdit
	}{
		{name: "negative", edits: []TextEdit{{Span: Span{Start: -1, End: 0}}}},
		{name: "past end", edits: []TextEdit{{Span: Span{Start: 0, End: 9}}}},
		{name: "overlap", edits: []TextEdit{
			{Span: Span{Start: 0, End: 4}, NewText: "A"},
			{Span: Span{Start: 3, End: 6}, NewText: "B"},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := result.ApplyEdits(test.edits...); err == nil {
				t.Fatal("ApplyEdits succeeded, want an error")
			}
		})
	}
}

func TestApplyEditsNoOpDoesNotAllocate(t *testing.T) {
	result := ParseResult{SQL: "SELECT 1"}
	if allocations := testing.AllocsPerRun(100, func() {
		if got, err := result.ApplyEdits(); err != nil || got != result.SQL {
			t.Fatalf("ApplyEdits() = %q, %v", got, err)
		}
	}); allocations != 0 {
		t.Fatalf("ApplyEdits() allocated %.1f times, want 0", allocations)
	}
}

func FuzzLosslessTokenCoverage(f *testing.F) {
	for _, sql := range []string{
		"",
		" \t\r\n",
		"SELECT 1",
		"-- comment\nSELECT /* middle */ 用户 FROM 表  ",
		"SELECT 'unterminated",
	} {
		f.Add(sql)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		result := ParseTolerant(sql, DialectGeneric)
		if result.OriginalSQL() != sql {
			t.Fatalf("OriginalSQL() changed the input")
		}
		var reconstructed strings.Builder
		for i, token := range result.Tokens {
			gap, ok := result.SourceGapBefore(i)
			if !ok {
				t.Fatalf("invalid source gap before token %d: %#v", i, token)
			}
			gapText, _ := result.SourceSlice(gap)
			reconstructed.WriteString(gapText)
			if token.Kind != TokenEOF {
				tokenText, ok := result.SourceSlice(token.Span)
				if !ok {
					t.Fatalf("invalid token span at %d: %#v", i, token)
				}
				reconstructed.WriteString(tokenText)
			}
		}
		if got := reconstructed.String(); got != sql {
			t.Fatalf("token/gap reconstruction changed the input: got %q, want %q", got, sql)
		}
	})
}
