package compatibility

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Keep the new dialect snapshots in the default suite as well as in the full
// corpus. They are independently pinned; see custom_fixtures/SOURCE.md.
func TestHANAVerticaFixtures(t *testing.T) {
	for _, dialect := range []string{"hana", "vertica"} {
		t.Run(dialect, func(t *testing.T) {
			total := 0
			root := polyglotTestdataPath("custom_fixtures", dialect)
			entries, err := os.ReadDir(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if filepath.Ext(entry.Name()) != ".json" {
					continue
				}
				category := strings.TrimSuffix(entry.Name(), ".json")
				t.Run(category, func(t *testing.T) {
					stats := runFullCustomFixture(t, filepath.Join(root, entry.Name()), dialect, category)
					total += stats.total()
					t.Log(stats.summary())
					for _, failure := range stats.Failures {
						t.Errorf("%s: %s", failure.ID, formatFullFailure(failure))
					}
					if stats.Failed > 0 {
						t.Fatalf("%d fixture failures", stats.Failed)
					}
				})
			}
			if want := map[string]int{"hana": 132, "vertica": 99}[dialect]; total != want {
				t.Errorf("fixture snapshot has %d cases, want %d", total, want)
			}
		})
	}
}
