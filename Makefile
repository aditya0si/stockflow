DB_URL ?= postgres://stockflow:stockflow@localhost:5432/stockflow?sslmode=disable
TEST_DB_URL ?= postgres://stockflow:stockflow@localhost:5432/stockflow_test?sslmode=disable
DEMO_DB_URL ?= postgres://stockflow:stockflow@localhost:5432/stockflow_test?sslmode=disable

.PHONY: help up down logs db-up run seed reset fmt vet tidy \
	test test-unit test-integration test-race \
	web-install web-format-check web-typecheck web-test web-build \
	demo reconcile verify build

help:
	@echo "up               build and start app + postgres with docker compose"
	@echo "down             stop compose and delete volumes"
	@echo "db-up            start only postgres and wait until ready"
	@echo "run              run the API locally against DB_URL"
	@echo "seed             seed deterministic demo data"
	@echo "reset            wipe all rows and reseed"
	@echo "fmt              gofmt all tracked Go files"
	@echo "vet              run go vet"
	@echo "test-unit        run unit tests"
	@echo "test-integration run PostgreSQL integration tests (starts postgres; fails if the test DB is unreachable)"
	@echo "test-race        run all tests with the race detector (starts postgres; requires a C toolchain)"
	@echo "test             run unit and integration tests"
	@echo "web-install      npm ci for the operator UI"
	@echo "web-format-check prettier --check the operator UI"
	@echo "web-typecheck    tsc --noEmit for the operator UI"
	@echo "web-test         vitest run for the operator UI"
	@echo "web-build        build the operator UI into internal/webui/dist"
	@echo "demo             run the deterministic assertion demo against DEMO_DB_URL"
	@echo "reconcile        run the report-only reconciliation command once"
	@echo "verify           unit + integration + web checks + demo"
	@echo "build            build api, seed, demo, and reconcile binaries into bin/"

up:
	docker compose up -d --build

down:
	docker compose down -v

logs:
	docker compose logs -f api

db-up:
	docker compose up -d postgres
	@until docker compose exec -T postgres pg_isready -U stockflow -d stockflow_test >/dev/null 2>&1; do sleep 1; done

run:
	DATABASE_URL=$(DB_URL) go run ./cmd/api

seed:
	DATABASE_URL=$(DB_URL) go run ./cmd/seed

reset:
	DATABASE_URL=$(DB_URL) go run ./cmd/seed -reset

fmt:
	gofmt -w $(shell git ls-files '*.go')

vet:
	go vet ./...

tidy:
	go mod tidy

test-unit:
	go test ./internal/...

test-integration: db-up
	STOCKFLOW_TEST_DATABASE_URL=$(TEST_DB_URL) go test -count=1 ./tests/...

test-race: db-up
	STOCKFLOW_TEST_DATABASE_URL=$(TEST_DB_URL) go test -race -count=1 ./...

test: test-unit test-integration

web-install:
	cd web && npm ci

web-format-check:
	cd web && npm run format:check

web-typecheck:
	cd web && npm run typecheck

web-test:
	cd web && npm test

web-build:
	cd web && npm run build

demo: db-up web-build
	STOCKFLOW_ALLOW_DEMO_FIXTURES=true STOCKFLOW_DEMO_DATABASE_URL=$(DEMO_DB_URL) go run ./cmd/demo

reconcile:
	DATABASE_URL=$(DB_URL) go run ./cmd/reconcile

verify: test web-format-check web-typecheck web-test demo

build:
	go build -o bin/stockflow-api ./cmd/api
	go build -o bin/stockflow-seed ./cmd/seed
	go build -o bin/stockflow-demo ./cmd/demo
	go build -o bin/stockflow-reconcile ./cmd/reconcile
