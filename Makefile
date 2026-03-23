.PHONY: build test lint build-api build-bot build-exporter

build:
	go build ./...

build-api:
	CGO_ENABLED=0 go build -o bin/control-api ./cmd/control-api

build-bot:
	CGO_ENABLED=0 go build -o bin/discord-bot ./cmd/discord-bot

build-exporter:
	CGO_ENABLED=0 go build -o bin/galaxy-exporter ./cmd/galaxy-exporter

test:
	go test ./...

lint:
	golangci-lint run ./...
