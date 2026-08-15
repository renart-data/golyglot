.PHONY: test fmt-check test-cgo-free test-race ci release-check test-polyglot test-polyglot-identity fixtures bench-golyglot bench-polyglot bench-polyglot-transpile docs-install docs-dev docs-build

POLYGLOT_REF ?= main
POLYGLOT_CACHE ?= .cache/polyglot
POLYGLOT_FIXTURES ?= testdata/polyglot
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
	GOLYGLOT_RUN_FULL_FIXTURES=1 go test -run TestPolyglotFullParserFixtureIfPresent -count=1 ./...

test-polyglot-identity:
	GOLYGLOT_RUN_IDENTITY_FIXTURES=1 go test -run TestPolyglotFullIdentityFixtureIfPresent -count=1 ./...

# Extract the upstream SQLGlot fixture JSON without adding SQLGlot, Rust, or
# any native library to the Go module. The generated files remain test data and
# are intentionally not production dependencies.
fixtures:
	@mkdir -p "$(dir $(POLYGLOT_CACHE))"
	@if [ ! -d "$(POLYGLOT_CACHE)/.git" ]; then git clone --depth=1 --branch "$(POLYGLOT_REF)" https://github.com/tobilg/polyglot.git "$(POLYGLOT_CACHE)"; fi
	@cd "$(POLYGLOT_CACHE)" && make setup-sqlglot extract-fixtures
	@mkdir -p "$(POLYGLOT_FIXTURES)"
	@cp "$(POLYGLOT_CACHE)/crates/polyglot-sql/tests/sqlglot_fixtures/parser.json" "$(POLYGLOT_FIXTURES)/parser.full.json"
	@cp "$(POLYGLOT_CACHE)/crates/polyglot-sql/tests/sqlglot_fixtures/identity.json" "$(POLYGLOT_FIXTURES)/identity.full.json"
	@cp "$(POLYGLOT_CACHE)/crates/polyglot-sql/tests/sqlglot_fixtures/pretty.json" "$(POLYGLOT_FIXTURES)/pretty.full.json"

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
