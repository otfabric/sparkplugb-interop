.DEFAULT_GOAL := help

.PHONY: help fmt fmt-check lint lint-ci vet vuln test coverage cover coverage-gate e2e e2e-down check clean

help: ## This help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z0-9_-]+:.*?## / {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

export GOWORK := off

GO_MOD_VERSION := $(shell sed -nE 's/^go ([0-9]+\.[0-9]+).*/\1/p' go.mod | head -n1)
GOFMT_CI = $(shell GOTOOLCHAIN=go$(GO_MOD_VERSION).0 go env GOROOT)/bin/gofmt

# Library packages: exclude e2e tests (./tests/...) which need Docker.
TEST_PKGS := $(shell go list ./... | grep -v '/tests$$')
COVER_PKGS := $(shell go list ./... | grep -vE '/tests$$|/cmd/' | paste -sd, -)
COVER_MIN ?= 80

GO_FILES = $(shell git ls-files --cached --others --exclude-standard -- '*.go')

fmt: ## Format Go code with the gofmt of the Go version in go.mod
	@echo "Running gofmt (go$(GO_MOD_VERSION))"
	@$(GOFMT_CI) -w $(GO_FILES)

fmt-check: ## Fail if gofmt would change any file
	@echo "Checking formatting (gofmt go$(GO_MOD_VERSION) and local $$(go env GOVERSION))"
	@ci="$$($(GOFMT_CI) -l $(GO_FILES))"; loc="$$(gofmt -l $(GO_FILES))"; \
	if [ -n "$$ci$$loc" ]; then \
		[ -z "$$ci" ] || { echo "Not formatted for gofmt go$(GO_MOD_VERSION) (CI):"; echo "$$ci"; }; \
		[ -z "$$loc" ] || { echo "Not formatted for the local gofmt:"; echo "$$loc"; }; \
		echo "Run 'make fmt'."; \
		exit 1; \
	fi

lint: ## Run staticcheck
	@echo "Running staticcheck"
	@staticcheck ./...

lint-ci: ## Run golangci-lint (see .golangci.yaml)
	@echo "Running golangci-lint"
	@golangci-lint run ./...

vet: ## Run go vet
	@echo "Running go vet"
	@go vet ./...

vuln: ## Run govulncheck
	@echo "Running govulncheck"
	@govulncheck ./...

test: ## Run the harness tests with the race detector
	@echo "Running tests"
	@go test -count=1 -race $(TEST_PKGS)

coverage: ## Run tests with coverage (writes coverage.out)
	@echo "Running coverage"
	@go test -count=1 -race -coverprofile=coverage.out -covermode=atomic -coverpkg=$(COVER_PKGS) $(TEST_PKGS)
	@go tool cover -func=coverage.out | tail -n1

coverage-gate: coverage ## Fail if coverage is below COVER_MIN percent
	@total=$$(go tool cover -func=coverage.out | awk '/^total:/ {sub("%", "", $$3); print $$3}'); \
	if awk -v t="$$total" -v m="$(COVER_MIN)" 'BEGIN { exit !(t + 0 >= m + 0) }'; then \
		echo "Coverage $$total% meets the gate of $(COVER_MIN)%"; \
	else \
		echo "Coverage $$total% is below the gate of $(COVER_MIN)%"; exit 1; \
	fi

cover: coverage ## Open coverage report in browser
	@echo "Opening coverage report"
	@go tool cover -html=coverage.out

e2e: ## Run E2E tests against the broker matrix (needs Docker)
	@echo "Starting broker matrix"
	@docker compose pull
	@docker compose up -d --wait --wait-timeout 180
	@echo "Running E2E tests"
	@go test -tags e2e ./tests/... -v -timeout=120s; rc=$$?; \
	docker compose down -v; exit $$rc

e2e-down: ## Tear down the broker matrix
	@docker compose down -v

check: fmt fmt-check lint lint-ci vet vuln test coverage-gate ## Run format + lint + vet + vuln + test + coverage gate

clean: ## Remove coverage artifacts
	@echo "Cleaning up"
	@rm -rf coverage.out coverage.html
