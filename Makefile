BINARY  ?= gate
PREFIX  ?=
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: help build install test test-short lint fix fix-diff cover clean

help:
	@echo "Targets:"
	@echo "  build              build ./cmd/gate into ./$(BINARY)"
	@echo "  install            go install ./cmd/gate into GOBIN"
	@echo "  test               go test ./...            (full suite)"
	@echo "  test-short         go test -short ./...     (unit lane)"
	@echo "  lint               golangci-lint run ./...  (.golangci.yml)"
	@echo "  fix-diff           go fix -diff ./...       (preview)"
	@echo "  fix                go fix ./... twice       (fixes can unlock fixes)"
	@echo "  cover              go test -coverpkg=./... and print the total"
	@echo "  clean              remove build outputs"

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/gate

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/gate

test:
	go test ./...

test-short:
	go test -short ./...

lint:
	golangci-lint run ./...

fix-diff:
	go fix -diff ./...

fix:
	go fix ./...
	go fix ./...

# -coverpkg is not optional: much of this suite drives internal/detect and
# internal/doctor from internal/app, which report 0.0% without it.
cover:
	go test -coverpkg=./... -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

clean:
	rm -f $(BINARY) coverage.out
	rm -rf dist/
