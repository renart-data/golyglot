package golyglot

import "testing"

func TestSyntacticContextAtIncompleteSQL(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		cursor   int
		kind     SyntacticContextKind
		expected ExpectedSyntax
		prefix   string
		replace  Span
	}{
		{
			name:     "empty document",
			sql:      "",
			kind:     ContextDocument,
			expected: ExpectedSyntax{Kind: ExpectedKeyword, Text: "SELECT"},
			replace:  Span{},
		},
		{
			name:     "partial statement keyword",
			sql:      "SEL",
			kind:     ContextDocument,
			expected: ExpectedSyntax{Kind: ExpectedKeyword, Text: "SELECT"},
			prefix:   "SEL",
			replace:  Span{Start: 0, End: 3},
		},
		{
			name:     "select needs projection",
			sql:      "SELECT",
			kind:     ContextSelectList,
			expected: ExpectedSyntax{Kind: ExpectedExpression},
			replace:  Span{Start: 6, End: 6},
		},
		{
			name:     "projection prefix",
			sql:      "SELECT account",
			kind:     ContextSelectList,
			expected: ExpectedSyntax{Kind: ExpectedExpression},
			prefix:   "account",
			replace:  Span{Start: 7, End: 14},
		},
		{
			name:     "projection continuation",
			sql:      "SELECT account ",
			kind:     ContextSelectList,
			expected: ExpectedSyntax{Kind: ExpectedKeyword, Text: "FROM"},
			replace:  Span{Start: 15, End: 15},
		},
		{
			name:     "partial from keyword",
			sql:      "SELECT account FR",
			kind:     ContextSelectList,
			expected: ExpectedSyntax{Kind: ExpectedKeyword, Text: "FROM"},
			prefix:   "FR",
			replace:  Span{Start: 15, End: 17},
		},
		{
			name:     "table prefix",
			sql:      "SELECT * FROM us",
			kind:     ContextFrom,
			expected: ExpectedSyntax{Kind: ExpectedTable},
			prefix:   "us",
			replace:  Span{Start: 14, End: 16},
		},
		{
			name:     "from continuation",
			sql:      "SELECT * FROM users ",
			kind:     ContextFrom,
			expected: ExpectedSyntax{Kind: ExpectedKeyword, Text: "JOIN"},
			replace:  Span{Start: 20, End: 20},
		},
		{
			name:     "keyword-shaped table prefix",
			sql:      "SELECT * FROM order",
			kind:     ContextFrom,
			expected: ExpectedSyntax{Kind: ExpectedTable},
			prefix:   "order",
			replace:  Span{Start: 14, End: 19},
		},
		{
			name:     "complete order keyword",
			sql:      "SELECT * FROM users ORDER",
			kind:     ContextOrderBy,
			expected: ExpectedSyntax{Kind: ExpectedKeyword, Text: "BY"},
			replace:  Span{Start: 25, End: 25},
		},
		{
			name:     "where expression",
			sql:      "SELECT * FROM users WHERE ",
			kind:     ContextWhere,
			expected: ExpectedSyntax{Kind: ExpectedExpression},
			replace:  Span{Start: 26, End: 26},
		},
		{
			name:     "where continuation",
			sql:      "SELECT * FROM users WHERE active ",
			kind:     ContextWhere,
			expected: ExpectedSyntax{Kind: ExpectedKeyword, Text: "AND"},
			replace:  Span{Start: 33, End: 33},
		},
		{
			name:     "trailing projection comma",
			sql:      "SELECT a, ",
			kind:     ContextSelectList,
			expected: ExpectedSyntax{Kind: ExpectedExpression},
			replace:  Span{Start: 10, End: 10},
		},
		{
			name:     "update assignment prefix",
			sql:      "UPDATE accounts SET na",
			kind:     ContextUpdate,
			expected: ExpectedSyntax{Kind: ExpectedIdentifier},
			prefix:   "na",
			replace:  Span{Start: 20, End: 22},
		},
		{
			name:     "insert target prefix",
			sql:      "INSERT INTO ev",
			kind:     ContextInsert,
			expected: ExpectedSyntax{Kind: ExpectedIdentifier},
			prefix:   "ev",
			replace:  Span{Start: 12, End: 14},
		},
		{
			name:     "values expression",
			sql:      "VALUES",
			kind:     ContextExpression,
			expected: ExpectedSyntax{Kind: ExpectedExpression},
			replace:  Span{Start: 6, End: 6},
		},
		{
			name:     "keyword-shaped expression prefix",
			sql:      "SELECT CURRENT_DATE - IN",
			kind:     ContextExpression,
			expected: ExpectedSyntax{Kind: ExpectedExpression},
			prefix:   "IN",
			replace:  Span{Start: 22, End: 24},
		},
		{
			name:     "create object prefix",
			sql:      "CREATE T",
			kind:     ContextStatement,
			expected: ExpectedSyntax{Kind: ExpectedKeyword, Text: "TABLE"},
			prefix:   "T",
			replace:  Span{Start: 7, End: 8},
		},
		{
			name:     "set value",
			sql:      "SET ",
			kind:     ContextStatement,
			expected: ExpectedSyntax{Kind: ExpectedIdentifier},
			replace:  Span{Start: 4, End: 4},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cursor := test.cursor
			if cursor == 0 && test.sql != "" {
				cursor = len(test.sql)
			}
			context, err := SyntacticContextAt(test.sql, cursor, DialectGeneric)
			if err != nil {
				t.Fatalf("SyntacticContextAt returned error: %v", err)
			}
			if context.Kind != test.kind {
				t.Fatalf("kind = %v, want %v; context=%#v", context.Kind, test.kind, context)
			}
			if context.Prefix != test.prefix || context.Replace != test.replace {
				t.Fatalf("prefix/replacement = %q/%#v, want %q/%#v", context.Prefix, context.Replace, test.prefix, test.replace)
			}
			if !containsExpectedSyntax(context.Expected, test.expected) {
				t.Fatalf("expected syntax = %#v, want %#v", context.Expected, test.expected)
			}
		})
	}
}

func TestSyntacticContextReplacesTokenIntersectingCursor(t *testing.T) {
	const sql = "SELECT * FRM users"
	cursor := len("SELECT * FR")
	context, err := SyntacticContextAt(sql, cursor, DialectGeneric)
	if err != nil {
		t.Fatal(err)
	}
	if context.Prefix != "FR" || context.Replace != (Span{Start: 9, End: 12}) {
		t.Fatalf("context = %#v, want FR replacing the complete FRM token", context)
	}
	if !containsExpectedSyntax(context.Expected, ExpectedSyntax{Kind: ExpectedKeyword, Text: "FROM"}) {
		t.Fatalf("expected syntax = %#v, want FROM", context.Expected)
	}
}

func TestSyntacticContextDoesNotExposeSequentialCascadeAsAlternatives(t *testing.T) {
	const sql = "INSERT INTO ev"
	context, err := SyntacticContextAt(sql, len(sql), DialectGeneric)
	if err != nil {
		t.Fatal(err)
	}
	if !containsExpectedSyntax(context.Expected, ExpectedSyntax{Kind: ExpectedIdentifier}) {
		t.Fatalf("expected syntax = %#v, want INSERT target identifier", context.Expected)
	}
	if containsExpectedSyntax(context.Expected, ExpectedSyntax{Kind: ExpectedKeyword, Text: "VALUES"}) {
		t.Fatalf("expected syntax = %#v, VALUES is only valid after the missing target", context.Expected)
	}
}

func TestSyntacticContextRejectsInvalidCursor(t *testing.T) {
	if _, err := SyntacticContextAt("SELECT", 7, DialectGeneric); err == nil {
		t.Fatal("out-of-range cursor was accepted")
	}
	if _, err := SyntacticContextAt("SELECT 😀", len("SELECT ")+1, DialectGeneric); err == nil {
		t.Fatal("cursor splitting a UTF-8 code point was accepted")
	}
}

func containsExpectedSyntax(expected []ExpectedSyntax, wanted ExpectedSyntax) bool {
	for _, item := range expected {
		if item == wanted {
			return true
		}
	}
	return false
}
