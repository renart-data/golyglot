# Contributing

Run `make release-check` before submitting changes. Run `make fixtures` when
refreshing Polyglot compatibility snapshots, then run `make test-polyglot`.
The snapshots are checked in so the compatibility gates are reproducible
without SQLGlot or Rust installed.
