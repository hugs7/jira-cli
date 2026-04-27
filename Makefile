.PHONY: build test lint tidy run snapshot

build:
	go build -o jr ./cmd/jr

test:
	go test ./...

tidy:
	go mod tidy

run: build
	./jr --help

snapshot:
	goreleaser release --snapshot --clean
