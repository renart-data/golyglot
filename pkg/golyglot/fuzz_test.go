package golyglot

import "testing"

func FuzzTolerantParseNeverPanics(f *testing.F) {
	for _, seed := range []string{
		"",
		"SELECT",
		"SELECT * FROM users WHERE",
		"SELECT ((1 + 2)",
		"SELECT 'unfinished",
		"WITH x AS (SELECT 1) SELECT * FROM x",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		result := ParseTolerant(sql, DialectGeneric)
		if len(result.SQL) != len(sql) {
			t.Fatalf("result SQL changed during parse")
		}
	})
}

func FuzzSyntacticContextNeverPanics(f *testing.F) {
	for _, seed := range []string{
		"",
		"SEL",
		"SELECT account FR",
		"SELECT * FROM users WHERE",
		"UPDATE accounts SET",
		"WITH recent AS (SELECT 1) SELECT * FROM recent",
	} {
		f.Add(seed, len(seed))
	}
	f.Fuzz(func(t *testing.T, sql string, cursor int) {
		if cursor < 0 {
			cursor = -cursor
			if cursor < 0 {
				cursor = 0
			}
		}
		cursor %= len(sql) + 1
		_, _ = SyntacticContextAt(sql, cursor, DialectGeneric)
	})
}
