.PHONY: build test lint tidy run

build:
	go build -o jr ./cmd/jr

test:
	go test ./...

tidy:
	go mod tidy

run: build
	./jr --help
