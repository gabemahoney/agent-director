.PHONY: all build test generate lint

all: generate build

build:
	CGO_ENABLED=0 go build -o ./bin/claude-director ./cmd/claude-director

test:
	go test ./...

generate:
	go generate ./...

lint:
	go vet ./...
