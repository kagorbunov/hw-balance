.PHONY: help up down restart logs migrate-up migrate-down migrate-status migrate-create build clean test test-unit test-integration test-e2e

DATABASE_URL ?= postgres://app:app@postgres:5432/hw_balance?sslmode=disable
TEST_DATABASE_URL ?= postgres://app:app@localhost:5432/hw_balance?sslmode=disable
E2E_BASE_URL ?= http://localhost:18088
E2E_DATABASE_URL ?= postgres://app:app@localhost:5432/hw_balance?sslmode=disable

help:
	@echo "Available commands:"
	@echo "  make up              - Start all services"
	@echo "  make down            - Stop all services"
	@echo "  make restart         - Restart all services"
	@echo "  make logs            - Show logs for all services"
	@echo "  make build           - Build the application"
	@echo "  make clean           - Remove volumes and rebuild"
	@echo ""
	@echo "Test commands:"
	@echo "  make test            - Run all tests"
	@echo "  make test-unit       - Run unit tests only"
	@echo "  make test-integration - Run integration tests (requires DB)"
	@echo "  make test-e2e        - Run e2e tests (requires running services)"
	@echo ""
	@echo "Migration commands:"
	@echo "  make migrate-up      - Run all pending migrations"
	@echo "  make migrate-down    - Rollback last migration"
	@echo "  make migrate-status  - Show migration status"
	@echo "  make migrate-create NAME=<name> - Create new migration"

up:
	docker compose up -d

down:
	docker compose down

restart:
	docker compose down -v
	docker compose up -d --build

logs:
	docker compose logs -f

build:
	docker compose build

clean:
	docker compose down -v
	docker compose up -d --build

migrate-up:
	docker compose run --rm migrate -dir /migrations postgres "$(DATABASE_URL)" up

migrate-down:
	docker compose run --rm migrate -dir /migrations postgres "$(DATABASE_URL)" down

migrate-status:
	docker compose run --rm migrate -dir /migrations postgres "$(DATABASE_URL)" status

migrate-create:
ifndef NAME
	$(error NAME is required. Usage: make migrate-create NAME=add_users_table)
endif
	docker compose run --rm migrate create $(NAME) sql -dir /migrations

test:
	go test ./...

test-unit:
	go test ./internal/usecase/...

test-integration:
	TEST_DATABASE_URL="$(TEST_DATABASE_URL)" go test -v ./internal/integration/...

test-e2e:
	E2E_BASE_URL="$(E2E_BASE_URL)" E2E_DATABASE_URL="$(E2E_DATABASE_URL)" go test -v -count=1 ./e2e/...
