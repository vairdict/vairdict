BINARY := vairdict
MODULE := github.com/vairdict/vairdict
VERSION := $(shell git describe --tags 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"

.PHONY: build test test-integration lint install clean release-snapshot

build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/vairdict

test:
	go test ./...

test-integration:
	go test -tags=integration -count=1 ./cmd/vairdict/...

lint:
	golangci-lint run ./...

install:
	go install $(LDFLAGS) ./cmd/vairdict

clean:
	rm -f $(BINARY)

release-snapshot:
	goreleaser release --snapshot --clean
