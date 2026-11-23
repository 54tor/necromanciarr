.PHONY: build test

build:
	CGO_ENABLED=0 go build ./cmd/necromanciarr

test:
	go test ./...
