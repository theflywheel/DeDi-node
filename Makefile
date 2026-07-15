export TEST_DATABASE_URL ?= postgres://dedi:dedi@localhost:5433/dedi?sslmode=disable

.PHONY: up down test build keygen

up:
	docker compose up -d postgres

down:
	docker compose down

test:
	# -p 1: all packages share one test database; parallel packages race on TRUNCATE
	go test -p 1 ./...

build:
	go build -o bin/dedid ./cmd/dedid

keygen:
	mkdir -p keys && go run ./cmd/dedid keygen -out keys/dedid.key -name dev.dedi.local
