.PHONY: test fmt-check test-cgo-free test-race ci release-check test-polyglot test-polyglot-identity fixtures

POLYGLOT_REF ?= main
POLYGLOT_CACHE ?= .cache/polyglot
POLYGLOT_FIXTURES ?= testdata/polyglot

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
