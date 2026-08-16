.PHONY: test fmt-check test-cgo-free test-race ci release-check test-polyglot test-polyglot-full test-polyglot-oracle test-polyglot-identity fetch-polyglot-ffi fixtures bench-golyglot bench-polyglot bench-polyglot-transpile docs-install docs-dev docs-build

POLYGLOT_REF ?= d5aa0d493c281398c9fdbc6febd3577f10ceac2f
POLYGLOT_CACHE ?= .cache/polyglot
POLYGLOT_FIXTURES ?= testdata/polyglot/sqlglot_fixtures
POLYGLOT_CUSTOM_FIXTURES ?= testdata/polyglot/custom_fixtures
POLYGLOT_FFI_VERSION ?= v0.9.1
POLYGLOT_FFI_PLATFORM ?= linux-x86_64
POLYGLOT_FFI_CACHE ?= .cache/polyglot-release
POLYGLOT_FFI_ARCHIVE ?= $(POLYGLOT_FFI_CACHE)/$(POLYGLOT_FFI_VERSION)/polyglot-sql-ffi-$(POLYGLOT_FFI_PLATFORM).tar.gz
POLYGLOT_FFI_DIR ?= $(POLYGLOT_FFI_CACHE)/$(POLYGLOT_FFI_VERSION)/polyglot-sql-ffi-$(POLYGLOT_FFI_PLATFORM)
POLYGLOT_FFI_PATH ?= $(POLYGLOT_FFI_DIR)/libpolyglot_sql_ffi.so
POLYGLOT_BENCH_ENV ?= CARGO_PROFILE_BENCH_LTO=false CARGO_PROFILE_BENCH_CODEGEN_UNITS=16
POLYGLOT_BENCH_PROFILE ?= dev

test:
	go test ./...

fmt-check:
	test -z "$$(gofmt -l .)"

test-cgo-free:
	CGO_ENABLED=0 go test ./...
	CGO_ENABLED=0 go vet ./...
	CGO_ENABLED=0 go build ./...

test-race:
	go test -race ./...

ci: fmt-check
	go test ./...
	CGO_ENABLED=0 go test ./...
	CGO_ENABLED=0 go vet ./...
	CGO_ENABLED=0 go build ./...
	go test -race ./...

release-check: ci

test-polyglot:
	go test -run '^(TestSQLGlot(FixtureCorpusShape|FixtureSourceIsPresent|ParserFixtures|IdentityFixtures)|TestPolyglotCustomFixtureCorpusShape)$$' -count=1 ./...

test-polyglot-full:
	GOMAXPROCS=2 CGO_ENABLED=0 GOLYGLOT_FULL_FIXTURES=1 go test -p 1 -run '^TestSQLGlotFullFixtures$$' -count=1 ./...

test-polyglot-oracle:
	python3 tools/polyglot_ffi_oracle.py --library "$(POLYGLOT_FFI_PATH)"

test-polyglot-identity:
	go test -run '^TestSQLGlotIdentityFixtures$$' -count=1 ./...

# Extract the complete upstream SQLGlot and Polyglot custom fixture snapshots
# without adding SQLGlot, Rust, or any native library to the Go module. The
# generated files are test data only and are intentionally not production
# dependencies.
fixtures:
	@mkdir -p "$(dir $(POLYGLOT_CACHE))"
	@if [ ! -d "$(POLYGLOT_CACHE)/.git" ]; then \
		git clone --filter=blob:none --no-checkout https://github.com/tobilg/polyglot.git "$(POLYGLOT_CACHE)"; \
		git -C "$(POLYGLOT_CACHE)" fetch --depth=1 origin "$(POLYGLOT_REF)"; \
		git -C "$(POLYGLOT_CACHE)" checkout --detach FETCH_HEAD; \
	else \
		actual=$$(git -C "$(POLYGLOT_CACHE)" rev-parse HEAD) || exit 1; \
		requested=$$(git -C "$(POLYGLOT_CACHE)" rev-parse "$(POLYGLOT_REF)" 2>/dev/null) || { echo "cannot resolve POLYGLOT_REF=$(POLYGLOT_REF) in $(POLYGLOT_CACHE)" >&2; exit 1; }; \
		test "$$actual" = "$$requested" || { echo "$(POLYGLOT_CACHE) is at $$actual, want $$requested; use POLYGLOT_CACHE=/path/to/fresh/cache" >&2; exit 1; }; \
	fi
	@cd "$(POLYGLOT_CACHE)" && make setup-sqlglot extract-fixtures
	@mkdir -p "$(POLYGLOT_FIXTURES)" "$(POLYGLOT_CUSTOM_FIXTURES)/datafusion"
	@cp -R "$(POLYGLOT_CACHE)/crates/polyglot-sql/tests/sqlglot_fixtures/." "$(POLYGLOT_FIXTURES)/"
	@cp -R "$(POLYGLOT_CACHE)/crates/polyglot-sql/tests/custom_fixtures/datafusion/." "$(POLYGLOT_CUSTOM_FIXTURES)/datafusion/"

# Download the pinned Linux x86_64 release artifact used by the optional
# versioned oracle. This target never installs or links it into golyglot.
fetch-polyglot-ffi:
	@test "$(POLYGLOT_FFI_PLATFORM)" = linux-x86_64 || { echo "fetch-polyglot-ffi currently supports POLYGLOT_FFI_PLATFORM=linux-x86_64" >&2; exit 1; }
	@mkdir -p "$(POLYGLOT_FFI_CACHE)/$(POLYGLOT_FFI_VERSION)"
	@curl --fail --location --retry 3 --output "$(POLYGLOT_FFI_ARCHIVE)" "https://github.com/tobilg/polyglot/releases/download/$(POLYGLOT_FFI_VERSION)/polyglot-sql-ffi-$(POLYGLOT_FFI_PLATFORM).tar.gz"
	@case "$(POLYGLOT_FFI_VERSION)" in \
		v0.9.1) expected=1b83ab550997f44ea270c0f73c591a1b33d6168dffc25745ead59002d49d57a3 ;; \
		*) echo "no pinned checksum for POLYGLOT_FFI_VERSION=$(POLYGLOT_FFI_VERSION)" >&2; exit 1 ;; \
	esac; \
	actual=$$(sha256sum "$(POLYGLOT_FFI_ARCHIVE)" | awk '{print $$1}') || exit 1; \
	test "$$actual" = "$$expected" || { echo "checksum mismatch for $(POLYGLOT_FFI_ARCHIVE)" >&2; exit 1; }
	@tar -xzf "$(POLYGLOT_FFI_ARCHIVE)" -C "$(POLYGLOT_FFI_CACHE)/$(POLYGLOT_FFI_VERSION)"

bench-golyglot:
	go test ./benchmarks -run '^$$' -bench . -benchmem -count=5

bench-polyglot:
	@test -d "$(POLYGLOT_CACHE)/.git" || (echo "missing $(POLYGLOT_CACHE); see benchmarks/README.md" && exit 1)
	@test "$$(git -C "$(POLYGLOT_CACHE)" rev-parse HEAD)" = "$$(git -C "$(POLYGLOT_CACHE)" rev-parse d5aa0d4)" || (echo "$(POLYGLOT_CACHE) must be at Polyglot d5aa0d4" && exit 1)
	@cd "$(POLYGLOT_CACHE)" && CARGO_PROFILE_DEV_DEBUG=0 cargo bench --profile "$(POLYGLOT_BENCH_PROFILE)" -p polyglot-sql --no-default-features --bench parsing -- --noplot

bench-polyglot-transpile:
	@test -d "$(POLYGLOT_CACHE)/.git" || (echo "missing $(POLYGLOT_CACHE); see benchmarks/README.md" && exit 1)
	@test "$$(git -C "$(POLYGLOT_CACHE)" rev-parse HEAD)" = "$$(git -C "$(POLYGLOT_CACHE)" rev-parse d5aa0d4)" || (echo "$(POLYGLOT_CACHE) must be at Polyglot d5aa0d4" && exit 1)
	@cd "$(POLYGLOT_CACHE)" && $(POLYGLOT_BENCH_ENV) cargo bench -p polyglot-sql --bench transpile -- --noplot

docs-install:
	CI=true corepack pnpm --dir docs install --frozen-lockfile

docs-dev:
	corepack pnpm --dir docs dev

docs-build:
	corepack pnpm --dir docs build
