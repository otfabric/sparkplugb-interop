.PHONY: test e2e lint clean

test:
	go test ./harness/... -count=1 -v

e2e: compose-up
	@echo "==> Waiting for brokers…"
	go run ./cmd/wait-brokers
	@echo "==> Running E2E tests…"
	go test -tags e2e ./tests/... -count=1 -v -timeout=120s
	@$(MAKE) compose-down

lint:
	go vet ./...

compose-up:
	docker compose up -d
	@echo "==> Brokers starting…"

compose-down:
	docker compose down

clean: compose-down
	rm -f coverage.out
