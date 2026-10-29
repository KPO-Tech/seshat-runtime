# seshat-runtime - development Makefile
#
#   make               -> list the targets (same as make help)
#   make build         -> build the terminal UI (bin/seshat) and the gRPC server
#   make run           -> build and start the terminal UI in this folder
#   make run-fresh     -> start it with an empty, throw-away configuration (first-run tests)
#   make test-short    -> the quick Go tests of both modules
#   make check         -> what the pre-commit hook and the CI check, before you push
#
# Works from PowerShell, cmd or Git Bash on Windows (see mk/shell.mk).

include mk/shell.mk

CMD_CLI        := ./tui/cmd/cli
CMD_GRPC       := ./cmd/grpc

# ".exe" on Windows, empty elsewhere. Without it `go build -o bin/seshat` writes a
# file named "seshat" next to the real seshat.exe, which keeps being the one that runs.
GOEXE          := $(shell go env GOEXE)
SEP            := $(if $(filter Windows_NT,$(OS)),;,:)
BIN            := bin/seshat$(GOEXE)

# Make built binaries discoverable from the repo root.
export PATH := $(CURDIR)/bin$(SEP)$(PATH)

# Arguments passed to the binary by run / run-fresh:  make run ARGS="--help"
ARGS           ?=

.PHONY: help all setup build build-cli build-grpc build_linux run run-fresh doctor where \
        test test-short test-tui test-pkg test-race fmt fmt-check vet lint check tidy hooks \
        rust-build rust-test rust-check \
        clean clean-runtime clean-all install-deepdoc-models

.DEFAULT_GOAL := help

help: ## Show the available targets
	@printf "\n  \033[1mseshat-runtime - make targets\033[0m\n\n"
	@grep -hE '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
	  | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}'
	@printf "\n"

all: build ## Same as build

# First-time setup

setup: ## First-time setup: installs ripgrep, builds the binaries, wires the git hooks (Linux/macOS; on Windows run scripts\setup.ps1)
	@case "$$(uname -s 2>/dev/null)" in \
		Darwin|Linux) bash scripts/setup.sh ;; \
		*) \
			echo "" ; \
			echo "  On Windows run, in PowerShell:" ; \
			echo "    powershell -ExecutionPolicy Bypass -File scripts\\setup.ps1" ; \
			echo "" ;; \
	esac

# Build and run

build: build-cli build-grpc ## Build the terminal UI and the gRPC server into bin/

build-cli: ## Build the terminal UI -> bin/seshat
	go build -o $(BIN) $(CMD_CLI)

build-grpc: ## Build the gRPC server -> bin/seshat-grpc
	go build -o bin/seshat-grpc$(GOEXE) $(CMD_GRPC)

build_linux: ## Build the terminal UI to /tmp/seshat (Linux/macOS)
	go build -o /tmp/seshat $(CMD_CLI)

run: build-cli ## Build and start the terminal UI in the current folder (ARGS="..." to pass arguments)
	./$(BIN) $(ARGS)

run-fresh: build-cli ## Start the terminal UI with an empty throw-away configuration, as on a first run
	@root="$$(mktemp -d)" ; \
	printf "\033[32m[seshat]\033[0m configuration in %s (deleted afterwards)\n" "$$root" ; \
	SESHAT_RUNTIME_ROOT="$$root" SESHAT_KEYCHAIN=off ./$(BIN) $(ARGS) ; status=$$? ; \
	rm -rf "$$root" ; exit $$status

doctor: build-cli ## Run seshat doctor: what this machine can and cannot do (sandbox, models, tools)
	./$(BIN) doctor

where: ## Show the binary this folder builds (inside make, bin/ comes first on PATH)
	@printf "built here : %s\n" "$(CURDIR)/$(BIN)"
	@printf "seshat found: "; command -v seshat || echo "(none)"

# Tests

test: ## Run all Go tests of both modules (the engine and the terminal UI)
	go test ./... ./tui/... -timeout 300s

test-short: ## Run the quick tests only (go test -short)
	go test -short ./... ./tui/... -timeout 300s

test-tui: ## Run the tests of the terminal UI module only
	go test ./tui/... -timeout 300s

test-pkg: ## Run the tests of one package (PKG=./internal/tools/bash/...)
	@test -n "$(PKG)" || (echo "usage: make test-pkg PKG=./internal/tools/bash/..." && exit 1)
	go test $(PKG) -timeout 300s

test-race: ## Run all Go tests with the race detector
	go test -race ./... ./tui/... -timeout 300s

# Code quality

fmt: ## Format the Go sources with gofmt
	gofmt -w .

fmt-check: ## Fail when a Go file is not gofmt-formatted
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "not formatted:"; echo "$$out"; exit 1; fi

vet: ## Run go vet on both modules
	go vet ./... ./tui/...

lint: ## Run golangci-lint on both modules
	@command -v golangci-lint > /dev/null 2>&1 \
		|| (echo "golangci-lint not installed - run: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest" && exit 1)
	golangci-lint run ./...
	cd tui && golangci-lint run ./...

# Rust (rust/sandboxd, the execution sidecar). Not part of `check`: a Go contributor
# who never touches the sandbox should not need a Rust toolchain. CI runs these.
rust-build: ## Build the sandbox sidecar -> rust/target/release/seshat-sandboxd
	cd rust && cargo build --release

rust-test: ## Run the Rust tests of the sandbox sidecar
	cd rust && cargo test

rust-check: ## Format check, clippy and tests of the sidecar (what CI runs)
	cd rust && cargo fmt --check && cargo clippy --all-targets -- -D warnings && cargo test

check: fmt-check vet test-short ## Before you push: format check, vet and the quick tests

tidy: ## go mod tidy for both modules
	GOWORK=off go mod tidy
	cd tui && GOWORK=off go mod tidy

hooks: ## Install the git hooks of .githooks/
	git config core.hooksPath .githooks
	@echo "Git hooks installed from .githooks/"

# Maintenance

clean: ## Remove the compiled binaries (bin/ and rust/target/)
	rm -rf bin/ rust/target/

# WARNING: credentials and session history cannot be recovered - you will need to re-run
# `seshat login` and `seshat config` afterwards. Uses SESHAT_RUNTIME_ROOT if set, otherwise ~/.config/seshat-*.
clean-runtime: ## Erase ALL seshat runtime data (database, credentials, sessions, logs)
	@confdir="$${SESHAT_RUNTIME_ROOT:-$${XDG_CONFIG_HOME:-$$HOME/.config}}" ; \
	for d in seshat-cli seshat-tui ; do \
	    target="$$confdir/$$d" ; \
	    if [ -d "$$target" ]; then \
	        rm -rf "$$target" && echo "  removed $$target" ; \
	    fi ; \
	done
	@echo "Runtime data cleared."

clean-all: clean clean-runtime ## Binaries and runtime data: a completely fresh start

# pkg/nativedoc (optional, opt-in)
#
# Fully native PDF/DOCX/XLSX conversion (including OCR/layout for scanned PDFs) with no external service - not wired in
# anywhere by default, an embedder opts in via sdk.ClientConfig.DocumentConverter. See pkg/nativedoc's own doc comment and
# docs/issues/document-intelligence-roadmap.md.
#
# install-deepdoc-models fetches the ONNX models. Building anything that imports pkg/nativedoc also needs native libs
# (pdf_oxide/pdfium/onnxruntime) and the `static` build tag - that is scripts/setup-nativedoc-cgo.sh, which prints shell
# exports rather than being a Make target (Make cannot export into your calling shell):
#   source <(./scripts/setup-nativedoc-cgo.sh)
#   go build -tags "static nativedoc" ./your/package/...

install-deepdoc-models: ## Download the ONNX models of the optional native document reading
	@./scripts/install-deepdoc-models.sh
