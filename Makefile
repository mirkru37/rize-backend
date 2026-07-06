.PHONY: build test lint vuln run ci

build:
	go build ./...

test:
	go test -race ./...

lint:
	golangci-lint run

vuln:
	govulncheck ./...

run:
	go run ./cmd/api

ci: build test lint vuln
