# The store ID from the fga CLI's JSON: the store just created or imported,
# or the newest one listed. Prints nothing when there is none.
STORE_ID = jq -r '(.store // .stores[-1]).id // empty'
# The sample store (the newest named explorer-demo), for the integration targets.
STORE ?= $(shell fga store list --name explorer-demo --api-url http://localhost:8080 2>/dev/null | $(STORE_ID))
# Development tools live in tools.mod, apart from the explorer's dependencies.
GITLEAKS = go tool -modfile=tools.mod gitleaks
STATICCHECK = go tool -modfile=tools.mod staticcheck
MODERNIZE = go tool -modfile=tools.mod modernize
GOVULNCHECK = go tool -modfile=tools.mod govulncheck
DATASTORE_URI ?= postgres://openfga:openfga@localhost:5433/openfga?sslmode=disable

.PHONY: build test test-short test-js test-integration cover lint vuln leaks seed-data run up seed seed-modular
build:
	go build -o bin/fgaexplorer ./cmd/fgaexplorer

# Everything that runs without Docker: Go tests against an in-process OpenFGA
# (including the browser smoke tests when Chrome is installed) and the JS
# unit tests.
test:
	go vet ./... && go test ./...
	$(MAKE) test-js

# Fast feedback: skips the full walker/server comparison and browser tests.
test-short:
	go vet ./... && go test -short ./...

# JS unit tests: Node 18+, nothing to install (internal/web/package.json only
# marks the modules as ES modules).
test-js:
	node --test internal/web/jstest/

# Against the Docker stack (make up seed): the Postgres datastore and search
# tests, and the walker/server comparison against a real deployment.
test-integration:
	FGA_TEST_API_URL=http://localhost:8080 FGA_TEST_STORE=$(STORE) FGA_TEST_DATASTORE_URI='$(DATASTORE_URI)' \
	go test -count=1 ./internal/trace ./internal/search ./internal/tuples ./internal/datastore -v -run 'Server|BruteForce|Reachability|DatastoreMatches|Datastore'

# Coverage across packages (cross-package, so handler tests count toward the
# packages they exercise), with the Docker stack when it is up.
cover:
	FGA_TEST_DATASTORE_URI='$(DATASTORE_URI)' FGA_TEST_STORE=$(STORE) \
	go test -count=1 -coverpkg=./... -coverprofile=cover.out ./... && go tool cover -func=cover.out | tail -1

# Static checks (staticcheck and modernize, pinned in tools.mod and run with
# go tool): bugs and dead code, and code the current Go version or standard
# library writes more simply. Fails on any finding; `$(MODERNIZE) -fix ./...`
# applies modernize's suggestions.
lint:
	go vet ./...
	$(STATICCHECK) ./...
	$(MODERNIZE) ./...

# Known vulnerabilities (govulncheck, pinned in tools.mod): in the code the
# explorer and its tests reach (the browser tests' Chrome driver has its own
# dependencies), then in each pinned developer tool (built to a temporary
# directory and scanned as a binary). Needs the Go vulnerability database
# (vuln.go.dev), which is sent the list of modules; not part of make test.
vuln:
	$(GOVULNCHECK) -test ./...
	@d=$$(mktemp -d); trap 'rm -rf "$$d"' EXIT; \
	for t in $$(awk '/^tool \(/{f=1; next} f && /^\)/{f=0} f{print $$1}' tools.mod); do \
		b="$$d/$$(echo $$t | tr / _)"; \
		echo "== $$t"; \
		go build -modfile=tools.mod -o "$$b" "$$t" && $(GOVULNCHECK) -mode=binary "$$b" || exit 1; \
	done

# Secret scan (gitleaks, pinned in tools.mod and run with go tool: nothing to
# install): the commit history, then the working tree (honoring .gitignore).
# Fails on any finding.
leaks:
	$(GITLEAKS) git . --redact --no-banner
	$(GITLEAKS) dir . --redact --no-banner

# Regenerate the sample tuples (deploy/seed/*.json) after changing the generator.
seed-data:
	go run ./deploy/seed/gen

up:
	docker compose -f deploy/docker-compose.yml -p fgaexplorer up -d

# The sample store: imports the model and tuples, then adds the collation
# tuples to the store just created (by its ID, not whichever is listed first).
seed:
	@id=$$(fga store import --file deploy/seed/store.fga.yaml --api-url http://localhost:8080 | $(STORE_ID)) && test -n "$$id" && \
	fga tuple write --store-id $$id --api-url http://localhost:8080 --file deploy/seed/collation-tuples.json --max-tuples-per-write 100 >/dev/null && \
	echo "explorer-demo store: $$id"

run: build
	./bin/fgaexplorer serve --config explorer.example.yaml --datastore-uri '$(DATASTORE_URI)'

# A modular model (fga.mod + module files) for trying module views.
seed-modular:
	@id=$$(fga store create --name modular-demo --model deploy/seed/modular/fga.mod --api-url http://localhost:8080 | $(STORE_ID)) && test -n "$$id" && \
	fga tuple write --store-id $$id --api-url http://localhost:8080 --file deploy/seed/modular/tuples.json && echo "modular-demo store: $$id"
