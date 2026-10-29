.PHONY: setup build dev fmt lint test test-race

setup:
	go mod download

build:
	go build -o bin/polyjuice-stream ./src/cmd/polyjuice-stream

dev:
	go run ./src/cmd/polyjuice-stream serve

fmt:
	gofmt -w src tests

lint:
	@test -z "$$(gofmt -l src tests)" || (gofmt -l src tests; exit 1)
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...
