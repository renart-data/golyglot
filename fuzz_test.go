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
