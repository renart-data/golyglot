# Polyglot custom fixture snapshot

The DataFusion fixtures are copied from
[Polyglot](https://github.com/tobilg/polyglot) at revision
`d5aa0d493c281398c9fdbc6febd3577f10ceac2f`. They are test data only; refresh
them with `make fixtures`.

The SAP HANA (`hana/`) and Vertica (`vertica/`) fixtures are copied unchanged
from `crates/polyglot-sql/tests/custom_fixtures/` at Polyglot revision
[`a1a49b961d68256b410602a6607619986e25dacc`](https://github.com/tobilg/polyglot/tree/a1a49b961d68256b410602a6607619986e25dacc/crates/polyglot-sql/tests/custom_fixtures).
These snapshots are pinned independently of the older SQLGlot/DataFusion
corpus. Polyglot is MIT-licensed. The default test suite and full compatibility
gate both check all of these cases; a refresh must retain the source pin and
review any changed expectations.
