export TEST_DATABASE_URL ?= postgres://dedi:dedi@localhost:5433/dedi?sslmode=disable

.PHONY: up down test build

up:
	docker compose up -d postgres

down:
	docker compose down

test:
	go test ./...

build:
	go build -o bin/dedid ./cmd/dedid
