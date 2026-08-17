.PHONY: \
	bench-golyglot bench-golyglot-corpus bench-polyglot \
	bench-polyglot-ffi bench-polyglot-ffi-corpus bench-polyglot-transpile \
	bench-workload build-polyglot-ffi-bench check-polyglot-ffi \
	check-polyglot-source ci docs-build docs-dev docs-install \
	fetch-polyglot-ffi fixtures fmt-check release-check \
	select-benchmark-corpus test test-cgo-free test-polyglot \
	test-polyglot-ffi-extended test-polyglot-full test-polyglot-identity \
	test-polyglot-oracle test-race test-tools

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
POLYGLOT_FFI_BENCH_BIN ?= .cache/polyglot-ffi-bench
POLYGLOT_FFI_FIXTURE_MANIFEST ?= benchmarks/fixture_cases.json
POLYGLOT_FFI_CORPUS_MANIFEST ?= benchmarks/corpus_cases.json
POLYGLOT_BENCH_ENV ?= CARGO_PROFILE_BENCH_LTO=false CARGO_PROFILE_BENCH_CODEGEN_UNITS=16
POLYGLOT_BENCH_PROFILE ?= dev
BENCH_COUNT ?= 5
BENCH_TIME ?= 1s
CORPUS_BENCH_COUNT ?= 3
CORPUS_BENCH_TIME ?= 250ms
CORPUS_FFI_BENCH_DURATION_MS ?= 250
BENCH_WORKLOAD ?=

test:
	go test ./...

test-tools:
	python3 -m unittest discover -s tools -p 'test_*.py'
	python3 -m py_compile tools/polyglot_ffi_oracle.py tools/polyglot_ffi_fixture_bench.py tools/select_polyglot_benchmark_cases.py tools/compare_benchmarks.py

fmt-check:
	test -z "$$(gofmt -l .)"

test-cgo-free:
	CGO_ENABLED=0 go test ./...
	CGO_ENABLED=0 go vet ./...
	CGO_ENABLED=0 go build ./...

test-race:
	go test -race ./...

ci: fmt-check test-tools
	go test ./...
	CGO_ENABLED=0 go test ./...
	CGO_ENABLED=0 go vet ./...
	CGO_ENABLED=0 go build ./...
	go test -race ./...
	$(MAKE) test-polyglot-full

release-check: ci

test-polyglot:
	go test -run '^(TestSQLGlot(FixtureCorpusShape|FixtureSourceIsPresent|ParserFixtures|IdentityFixtures)|TestPolyglotCustomFixtureCorpusShape)$$' -count=1 ./...

test-polyglot-full:
	GOMAXPROCS=2 CGO_ENABLED=0 GOLYGLOT_FULL_FIXTURES=1 go test -p 1 -run '^TestSQLGlotFullFixtures$$' -count=1 ./...

check-polyglot-ffi:
	@test -f "$(POLYGLOT_FFI_PATH)" || { echo "missing $(POLYGLOT_FFI_PATH); run make fetch-polyglot-ffi" >&2; exit 1; }

test-polyglot-ffi-extended: check-polyglot-ffi
	python3 tools/polyglot_ffi_oracle.py --library "$(POLYGLOT_FFI_PATH)"

# Backward-compatible alias. This is an extended public-FFI comparison, not
# the pass rate of Polyglot's narrower tagged Rust compatibility suite.
test-polyglot-oracle: test-polyglot-ffi-extended

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
# versioned FFI comparison. This target never installs or links it into golyglot.
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

select-benchmark-corpus: check-polyglot-ffi
	python3 tools/select_polyglot_benchmark_cases.py --library "$(POLYGLOT_FFI_PATH)" --output "$(POLYGLOT_FFI_CORPUS_MANIFEST)"

bench-golyglot:
	go test ./benchmarks -run '^$$' -bench '^(BenchmarkParse|BenchmarkTranspile|BenchmarkFormat|BenchmarkFixtureTranspile)$$' -benchmem -count="$(BENCH_COUNT)" -benchtime="$(BENCH_TIME)"

bench-golyglot-corpus:
	go test ./benchmarks -run '^$$' -bench '^BenchmarkCorpusTranspile$$' -benchmem -count="$(CORPUS_BENCH_COUNT)" -benchtime="$(CORPUS_BENCH_TIME)"

bench-workload:
	@test -n "$(BENCH_WORKLOAD)" || (echo "set BENCH_WORKLOAD=/path/to/workload.json" && exit 1)
	GOLYGLOT_BENCH_WORKLOAD="$(abspath $(BENCH_WORKLOAD))" go test ./benchmarks -run '^$$' -bench '^BenchmarkWorkload$$' -benchmem -count="$(BENCH_COUNT)" -benchtime="$(BENCH_TIME)"

build-polyglot-ffi-bench: check-polyglot-ffi
	@test -f "$(POLYGLOT_FFI_DIR)/polyglot_sql.h" || { echo "missing $(POLYGLOT_FFI_DIR)/polyglot_sql.h; run make fetch-polyglot-ffi" >&2; exit 1; }
	@mkdir -p "$(dir $(POLYGLOT_FFI_BENCH_BIN))"
	$(CC) -O2 -std=c11 -Wall -Wextra -Wpedantic -Werror -I"$(POLYGLOT_FFI_DIR)" tools/polyglot_ffi_bench.c -L"$(POLYGLOT_FFI_DIR)" -lpolyglot_sql_ffi -o "$(POLYGLOT_FFI_BENCH_BIN)"

bench-polyglot-ffi: build-polyglot-ffi-bench
	LD_LIBRARY_PATH="$(POLYGLOT_FFI_DIR):$${LD_LIBRARY_PATH:-}" "$(POLYGLOT_FFI_BENCH_BIN)"
	LD_LIBRARY_PATH="$(POLYGLOT_FFI_DIR):$${LD_LIBRARY_PATH:-}" python3 tools/polyglot_ffi_fixture_bench.py --binary "$(POLYGLOT_FFI_BENCH_BIN)" --library "$(POLYGLOT_FFI_PATH)" --manifest "$(POLYGLOT_FFI_FIXTURE_MANIFEST)"

bench-polyglot-ffi-corpus: build-polyglot-ffi-bench
	POLYGLOT_BENCH_SAMPLES="$(CORPUS_BENCH_COUNT)" POLYGLOT_BENCH_DURATION_MS="$(CORPUS_FFI_BENCH_DURATION_MS)" LD_LIBRARY_PATH="$(POLYGLOT_FFI_DIR):$${LD_LIBRARY_PATH:-}" python3 tools/polyglot_ffi_fixture_bench.py --binary "$(POLYGLOT_FFI_BENCH_BIN)" --library "$(POLYGLOT_FFI_PATH)" --manifest "$(POLYGLOT_FFI_CORPUS_MANIFEST)"

check-polyglot-source:
	@test -d "$(POLYGLOT_CACHE)/.git" || (echo "missing $(POLYGLOT_CACHE); see benchmarks/README.md" && exit 1)
	@actual=$$(git -C "$(POLYGLOT_CACHE)" rev-parse HEAD) || exit 1; \
	requested=$$(git -C "$(POLYGLOT_CACHE)" rev-parse "$(POLYGLOT_REF)" 2>/dev/null) || { echo "cannot resolve POLYGLOT_REF=$(POLYGLOT_REF) in $(POLYGLOT_CACHE)" >&2; exit 1; }; \
	test "$$actual" = "$$requested" || { echo "$(POLYGLOT_CACHE) is at $$actual, want $$requested" >&2; exit 1; }

bench-polyglot: check-polyglot-source
	@cd "$(POLYGLOT_CACHE)" && CARGO_PROFILE_DEV_DEBUG=0 cargo bench --profile "$(POLYGLOT_BENCH_PROFILE)" -p polyglot-sql --no-default-features --bench parsing -- --noplot

bench-polyglot-transpile: check-polyglot-source
	@cd "$(POLYGLOT_CACHE)" && $(POLYGLOT_BENCH_ENV) cargo bench -p polyglot-sql --bench transpile -- --noplot

docs-install:
	CI=true corepack pnpm --dir docs install --frozen-lockfile

docs-dev:
	corepack pnpm --dir docs dev

docs-build:
	corepack pnpm --dir docs build
