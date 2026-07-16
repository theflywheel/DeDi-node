export TEST_DATABASE_URL ?= postgres://dedi:dedi@localhost:5433/dedi?sslmode=disable

.PHONY: up down test build keygen contract-test

up:
	docker compose up -d postgres

down:
	docker compose down

test:
	# -p 1: all packages share one test database; parallel packages race on TRUNCATE
	go test -p 1 ./...

build:
	go build -o bin/dedid ./cmd/dedid

contract-test: build
	cd test/onix-contract && DEDID_BIN=$(CURDIR)/bin/dedid go test -count=1 -v ./...

keygen:
	mkdir -p keys && go run ./cmd/dedid keygen -out keys/dedid.key -name dev.dedi.local
