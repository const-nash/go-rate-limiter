# Targets are meant to be runnable both from a terminal and as
# one-click run configurations in an IDE (GoLand picks them up from
# this file directly). Run `make` on its own for the list.
#
# Every variable below can be overridden per invocation, e.g.
#   make test RUN=TestMapStore_Delete
#   make bench BENCH=SlidingWindow CPU=8 COUNT=3

GO        ?= go
PKGS      ?= ./...
RUN       ?=
BENCH     ?= .
COUNT     ?= 10
CPU       ?= 1,8
BENCHTIME ?=
BASE      ?= HEAD
OUT       ?= .bench
WORKTREE  := $(OUT)/base

# go install puts tools in GOPATH/bin, which an IDE's environment often
# doesn't have on PATH, so fall back to the absolute path.
BENCHSTAT := $(shell command -v benchstat 2>/dev/null || echo $(shell $(GO) env GOPATH)/bin/benchstat)

TESTFLAGS  = $(if $(RUN),-run '$(RUN)',)
BENCHFLAGS = -run '^$$' -bench '$(BENCH)' -benchmem -count $(COUNT) -cpu $(CPU) \
             $(if $(BENCHTIME),-benchtime $(BENCHTIME),)

.DEFAULT_GOAL := help
.PHONY: help build test test-race test-short cover cover-html bench bench-smoke \
        bench-save bench-compare fmt fmt-check vet tidy tools check clean

help: ## List the available targets
	@grep -hE '^[a-z][a-z-]*:.*?## ' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[1m%-14s\033[0m %s\n", $$1, $$2}'

## --- build and test -------------------------------------------------

build: ## Compile every package (a library, so nothing is produced)
	$(GO) build $(PKGS)

test: ## Run the tests (RUN=Regexp narrows them)
	$(GO) test $(TESTFLAGS) $(PKGS)

test-race: ## Run the tests under the race detector, ignoring the cache
	$(GO) test -race -count=1 $(TESTFLAGS) $(PKGS)

test-short: ## Run the tests, skipping the slow ones (-short)
	$(GO) test -short -count=1 $(TESTFLAGS) $(PKGS)

cover: ## Report test coverage per function
	@mkdir -p $(OUT)
	$(GO) test -coverprofile=$(OUT)/cover.out -covermode=atomic $(PKGS)
	$(GO) tool cover -func=$(OUT)/cover.out | tail -n 20

cover-html: cover ## Open the coverage report in a browser
	$(GO) tool cover -html=$(OUT)/cover.out

## --- benchmarks -----------------------------------------------------

bench: ## Run the benchmarks (BENCH=Regexp, COUNT, CPU, BENCHTIME)
	$(GO) test $(BENCHFLAGS) $(PKGS)

bench-smoke: ## Run every benchmark once, just to check they still work
	$(GO) test -run '^$$' -bench . -benchtime 1x $(PKGS)

bench-save: ## Run the benchmarks and save them for a later comparison
	@mkdir -p $(OUT)
	$(GO) test $(BENCHFLAGS) $(PKGS) | tee $(OUT)/new.txt

bench-compare: tools ## Compare the working tree against BASE (default: HEAD)
	@mkdir -p $(OUT)
	@git cat-file -e $(BASE):store_bench_test.go 2>/dev/null || { \
		echo 'BASE ($(BASE)) has no benchmarks committed — there is nothing to compare against.'; \
		echo 'Commit them, or point BASE at a commit that has them.'; exit 1; }
	@git worktree remove --force $(WORKTREE) 2>/dev/null || true
	git worktree add --detach $(WORKTREE) $(BASE)
	@echo '--- $(BASE) ---'
	cd $(WORKTREE) && $(GO) test $(BENCHFLAGS) $(PKGS) > $(CURDIR)/$(OUT)/old.txt
	@echo '--- working tree ---'
	$(GO) test $(BENCHFLAGS) $(PKGS) > $(OUT)/new.txt
	@git worktree remove --force $(WORKTREE)
	$(BENCHSTAT) $(OUT)/old.txt $(OUT)/new.txt

## --- housekeeping ---------------------------------------------------

fmt: ## Format the sources
	$(GO) fmt $(PKGS)

fmt-check: ## Fail if anything is unformatted
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo 'not gofmt-ed:'; echo "$$unformatted"; exit 1; \
	fi

vet: ## Run go vet
	$(GO) vet $(PKGS)

tidy: ## Tidy go.mod
	$(GO) mod tidy

tools: ## Install the tools the targets need (benchstat)
	@[ -x '$(BENCHSTAT)' ] || $(GO) install golang.org/x/perf/cmd/benchstat@latest

check: fmt-check vet test-race bench-smoke ## Everything CI should run

clean: ## Remove build and benchmark artefacts
	@git worktree remove --force $(WORKTREE) 2>/dev/null || true
	rm -rf $(OUT)
	$(GO) clean -cache -testcache
