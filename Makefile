DB_URL ?= postgres://stockflow:stockflow@localhost:5432/stockflow?sslmode=disable
TEST_DB_URL ?= postgres://stockflow:stockflow@localhost:5432/stockflow_test?sslmode=disable

.PHONY: help up down logs db-up run seed reset fmt vet tidy test test-unit test-integration test-race build

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
	@echo "build            build api and seed binaries into bin/"

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

build:
	go build -o bin/stockflow-api ./cmd/api
	go build -o bin/stockflow-seed ./cmd/seed
