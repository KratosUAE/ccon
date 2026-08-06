BIN     := ccon
PKG     := ./cmd/ccon
SRC     := cmd internal

# BINDIR is where `make install` puts the binary. Override it if your PATH
# looks different:  make install BINDIR=~/.aux/bin
BINDIR  ?= $(HOME)/.local/bin

# VERSION is only used by `make release`. A plain `make build` deliberately
# passes no version: the binary then reports "dev" plus the revision compiled
# in by the toolchain, instead of claiming a number nobody tagged.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null)

.DEFAULT_GOAL := build
.PHONY: help build release install uninstall test race cover lint check clean

help: ## show this list
	@awk 'BEGIN {FS = ":.*##"} /^[a-z][a-z-]*:.*##/ {printf "  \033[1m%-10s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

build: ## build ./ccon (version reads as "dev + revision")
	go build -o $(BIN) $(PKG)

release: ## build with an explicit version: make release VERSION=v1.1.0
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) $(PKG)

install: build ## install into BINDIR (default ~/.local/bin)
	@mkdir -p $(BINDIR)
	install -m 0755 $(BIN) $(BINDIR)/$(BIN)
	@echo "installed: $(BINDIR)/$(BIN)"

uninstall: ## remove the binary from BINDIR
	rm -f $(BINDIR)/$(BIN)

test: ## run the test suite
	go test ./...

race: ## run the suite under the race detector
	go test -race ./...

cover: ## per-package coverage
	go test -cover ./...

lint: ## gofmt + go vet + staticcheck
	@out=$$(gofmt -l $(SRC)); \
	if [ -n "$$out" ]; then echo "gofmt would rewrite:"; echo "$$out"; exit 1; fi
	go vet ./...
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck ./...; \
	else \
		echo "staticcheck not installed — skipped (go install honnef.co/go/tools/cmd/staticcheck@latest)"; \
	fi

check: lint race ## the full gate to run before committing

clean: ## remove the built binary
	rm -f $(BIN)
