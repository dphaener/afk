BINARY := afk
PREFIX  ?= $(HOME)/bin

# `make check` is the single entry point that .github/workflows/ci.yml runs,
# so local and CI runs cannot drift apart.
GOFILES := $(shell find . -name '*.go' -not -path './vendor/*')

.DEFAULT_GOAL := build

.PHONY: build
build: ## Build ./afk in the repo root
	go build -o $(BINARY) .

.PHONY: install
install: ## Build straight into $(PREFIX) (default ~/bin)
# Built directly to the destination, never built-then-copied. Go gives the
# binary a linker-signed ad-hoc signature bound to the path it was created
# at, so on arm64 a `cp` to another path invalidates it and the kernel
# SIGKILLs the copy — same bytes, exit 137, no output. Building in place
# signs it at its final path. Recover a copied binary with:
#   codesign --force --sign - $(PREFIX)/$(BINARY)
	@mkdir -p $(PREFIX)
	go build -o $(PREFIX)/$(BINARY) .
	@echo "installed $(PREFIX)/$(BINARY)"

.PHONY: test
test: ## Run the full test suite
	go test ./...

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: fmt
fmt: ## Format all Go files in place
	gofmt -w $(GOFILES)

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is unformatted
# `gofmt -l` exits 0 even when it lists files, so the listing has to be
# turned into a failure by hand.
	@unformatted=$$(gofmt -l $(GOFILES)); \
	if [ -n "$$unformatted" ]; then \
		echo "unformatted files:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: plugin-test
plugin-test: ## Run the plugin node and ruby test suites and the prompt size check
	ruby plugins/afk/scripts/test/plan_to_args_test.rb
	bash plugins/afk/scripts/check-cat-sizes.sh
	node --test plugins/afk/workflows/test/*.test.mjs
	cd plugins/afk-opencode && npm test

.PHONY: hook-test
hook-test: ## Run the plugin hook shell tests
# A failing script stops the loop and the target on the first non-zero exit.
	@for t in plugins/afk/hooks/*.test.sh; do \
		echo "bash $$t"; \
		bash "$$t" || exit 1; \
	done

.PHONY: install-script-test
install-script-test: ## Run the install.sh shell test and shellcheck
# shellcheck is optional on a laptop but required in CI: GitHub's runners ship
# it and set CI, so a missing one there means the lint silently stopped running.
	bash scripts/install.test.sh
	@if command -v shellcheck >/dev/null 2>&1; then \
		echo "shellcheck install.sh"; \
		shellcheck install.sh; \
	elif [ -n "$$CI" ]; then \
		echo "shellcheck is required in CI and is not installed"; exit 1; \
	else \
		echo "shellcheck not installed; skipped"; \
	fi

.PHONY: web-js-test
web-js-test: ## Run the web page script tests (node --test)
	node --test internal/web/jstest/*.test.mjs

.PHONY: release-script-test
release-script-test: ## Run the release.sh and release-decide.sh shell tests and shellcheck
# Optional locally, required in CI, as for install-script-test.
	bash scripts/release.test.sh
	bash scripts/release-decide.test.sh
	@if command -v shellcheck >/dev/null 2>&1; then \
		echo "shellcheck release.sh release.test.sh release-decide.sh release-decide.test.sh"; \
		shellcheck scripts/release.sh scripts/release.test.sh scripts/release-decide.sh scripts/release-decide.test.sh; \
	elif [ -n "$$CI" ]; then \
		echo "shellcheck is required in CI and is not installed"; exit 1; \
	else \
		echo "shellcheck not installed; skipped"; \
	fi

.PHONY: check
check: fmt-check vet test plugin-test hook-test install-script-test release-script-test web-js-test ## Everything CI runs

.PHONY: clean
clean: ## Remove the locally built binary
	rm -f $(BINARY)

.PHONY: help
help: ## List targets
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk -F':.*?## ' '{printf "  %-12s %s\n", $$1, $$2}'
