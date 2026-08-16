# Contributing

Run `make release-check` before submitting changes. Run `make fixtures` when
refreshing Polyglot compatibility snapshots, then run `make test-polyglot`.
The snapshots are checked in so the compatibility gates are reproducible
without SQLGlot or Rust installed.

The opt-in complete corpus run is `make test-polyglot-full`. For a released
Polyglot reference run, use `make fetch-polyglot-ffi` followed by
`make test-polyglot-oracle`; the downloaded FFI remains an external test
oracle and is never linked into golyglot.
