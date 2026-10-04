# Every target that compiles or tests goes through scripts/gate.sh (see
# AGENTS.md, "This machine has 8 GB of RAM").

GATE := scripts/gate.sh
GOFLAGS ?= -p=2
export GOFLAGS

.PHONY: dev worker lint test test-pkg build generate generate-check migrate migrate-new gate tools

dev: ## run the API with the local .env
	go run ./cmd/api

worker: ## run the background worker with the local .env
	go run ./cmd/api --mode worker

lint:
	test -z "$$(gofmt -l . | grep -v '/gen/')" || (gofmt -l . | grep -v '/gen/'; exit 1)
	go vet ./...
	staticcheck ./...

test: ## whole suite against DATABASE_URL_TEST, one package at a time
	$(GATE) go test -p 1 -count=1 ./...

test-pkg: ## one package: make test-pkg PKG=./internal/booking
	$(GATE) go test -count=1 $(PKG)

build:
	$(GATE) go build -o bin/api ./cmd/api

generate: ## regenerate the HTTP server from openapi.yaml and sqlc from internal/db/queries
	oapi-codegen -config oapi-codegen.yaml openapi.yaml
	sqlc generate

generate-check: generate
	git diff --exit-code -- internal/httpapi/gen internal/db

migrate: ## apply migrations to DATABASE_URL
	goose -dir migrations postgres "$$DATABASE_URL" up

migrate-new: ## make migrate-new NAME=add_appointments
	goose -dir migrations create $(NAME) sql

gate: ## the full local gate; identical to CI
	$(GATE) sh -c '$(MAKE) lint && $(MAKE) -o gate test build generate-check'

tools: ## install the code generators and linters used by make
	go install honnef.co/go/tools/cmd/staticcheck@latest
	go install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest
	go install github.com/pressly/goose/v3/cmd/goose@latest
