BINARY := vairdict
MODULE := github.com/vairdict/vairdict

.PHONY: build test lint install clean

build:
	go build -o $(BINARY) ./cmd/vairdict

test:
	go test ./...

lint:
	golangci-lint run ./...

install:
	go install ./cmd/vairdict

clean:
	rm -f $(BINARY)
