BINARY  ?= gate
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

# Pin shared with CI. `workflows` is convention-only (actionlint on PATH), so
# local `gate` does not pick this target up; `make workflows` installs the
# same version CI runs, then invokes that binary.
ACTIONLINT_VERSION ?= v1.7.12

.PHONY: help build install test lint workflows fix fix-diff cover clean

help:
	@echo "Targets:"
	@echo "  build              build ./cmd/gate into ./$(BINARY)"
	@echo "  install            go install ./cmd/gate into GOBIN"
	@echo "  test               go test -race ./...      (full suite)"
	@echo "  lint               golangci-lint run ./...  (.golangci.yml)"
	@echo "  workflows          actionlint at $(ACTIONLINT_VERSION)"
	@echo "  fix-diff           go fix -diff ./...       (preview)"
	@echo "  fix                go fix ./... twice       (fixes can unlock fixes)"
	@echo "  cover              go test -coverpkg=./... and print the total"
	@echo "  clean              remove build outputs"

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/gate

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/gate

test:
	go test -race ./...

lint:
	golangci-lint run --allow-parallel-runners ./...

workflows:
	go install github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)
	actionlint

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
