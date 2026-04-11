BINARY := vairdict
MODULE := github.com/vairdict/vairdict
VERSION := $(shell git describe --tags 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build test lint install clean release-snapshot

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/vairdict

test:
	go test ./...

lint:
	golangci-lint run ./...

install:
	go install $(LDFLAGS) ./cmd/vairdict

clean:
	rm -f $(BINARY)

release-snapshot:
	goreleaser release --snapshot --clean
