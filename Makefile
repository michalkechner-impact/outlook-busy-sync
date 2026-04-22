BINARY := outlook-busy-sync
PKG := github.com/michalkechner-impact/outlook-busy-sync
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -ldflags "-X $(PKG)/internal/version.Version=$(VERSION)"

.PHONY: build test lint fmt clean install run

build:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/$(BINARY)

install:
	go install $(LDFLAGS) ./cmd/$(BINARY)

test:
	go test -race -coverprofile=coverage.txt -covermode=atomic ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -s -w .
	go mod tidy

clean:
	rm -rf bin/ dist/ coverage.txt coverage.html

run: build
	./bin/$(BINARY)
