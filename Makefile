# Shortcuts for running and testing Strata. Run `make` (or `make help`) to list them.
#
# Typical use:
#   make up                       server + object store + a stream of generated logs
#   make ui                       the search page, http://127.0.0.1:7080
#   make search Q="lapack"        search from the command line
#   make clean                    stop everything and delete the data
#
# Settings you can change on the command line, e.g. `make up PROFILE=busy`:
#   PROFILE   generated-traffic workload: demo (default), busy or peak
#   RATE      lines per second, overriding the profile's rate
#   DURATION  stop the generator after this long, e.g. 10m (empty = keep going)

SHELL := /bin/bash
.DEFAULT_GOAL := help

COMPOSE  ?= docker compose
PROFILE  ?= demo
RATE     ?=
DURATION ?=
STRATA_ADDR ?= 127.0.0.1:$(or $(STRATA_PORT),7070)
UI_ADDR  ?= 127.0.0.1:7080
BGL      ?= $(HOME)/strata-datasets/BGL.log

# Read by docker-compose.yml to configure the generator container.
export GEN_PROFILE  := $(PROFILE)
export GEN_RATE     := $(RATE)
export GEN_DURATION := $(DURATION)

GEN_FLAGS := -addr $(STRATA_ADDR) -profile $(PROFILE) $(if $(RATE),-rate $(RATE))
UI_REPO   := https://github.com/dharmikchandel/strata\#readme

.PHONY: help up server down clean ps logs ui ui-bgl search gen soak ingest-bgl \
        build test test-e2e check fmt vet proto

help: ## Show this list
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z_-]+:.*## / {printf "  \033[1m%-11s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@echo
	@echo "Settings: PROFILE=demo|busy|peak  RATE=<lines/s>  DURATION=<e.g. 10m>   (example: make up PROFILE=busy)"

##@ Run

up: ## Start the server, object store AND the live log generator (PROFILE=demo|busy|peak)
	$(COMPOSE) up -d --build --wait strata rustfs
	$(COMPOSE) --profile live up -d generator
	@if [ "$(PROFILE)" != "demo" ] && [ -z "$(DURATION)" ]; then \
	  echo; echo "NOTE: the '$(PROFILE)' profile adds data fast (see 'strata-gen -h') and Strata has no retention;"; \
	  echo "      set DURATION=10m to stop it, or run 'make clean' when you are done."; fi
	@echo
	@echo "Running. Generated logs are being sent and become searchable within about 5 seconds."
	@echo "  make ui                  the search page ($(UI_ADDR))"
	@echo "  make search Q=\"lapack\"   search from the command line"
	@echo "  make logs                follow the server and the generator"
	@echo "  make clean               stop and delete all data"

server: ## Start only the server and object store (no generated traffic)
	$(COMPOSE) up -d --build --wait strata rustfs

down: ## Stop everything, keeping the data
	$(COMPOSE) --profile live down

clean: ## Stop everything and DELETE all data
	$(COMPOSE) --profile live down -v

ps: ## Show what is running
	$(COMPOSE) --profile live ps

logs: ## Follow the server and generator logs (Ctrl-C to leave)
	$(COMPOSE) --profile live logs -f --tail 40 strata generator

##@ Use

ui: ## Serve the search page for generated logs (http://127.0.0.1:7080)
	go run ./cmd/stratactl ui -server $(STRATA_ADDR) -listen $(UI_ADDR) \
	  -corpus-name "Generated logs (modelled on a real supercomputer log)" \
	  -examples bench/demo/live-examples.json \
	  -credit "The log lines are made up; their message shapes and frequencies come from the BGL supercomputer log (LogHub; Oliner and Stearley, DSN 2007)." \
	  -about-url $(UI_REPO)

ui-bgl: ## Serve the search page for the real BGL dataset (after `make ingest-bgl`)
	go run ./cmd/stratactl ui -server $(STRATA_ADDR) -listen $(UI_ADDR) \
	  -corpus-name "BGL supercomputer logs" -examples bench/demo/bgl-examples.json \
	  -credit "Data: BGL logs from the BlueGene/L supercomputer at Lawrence Livermore National Laboratory (Oliner and Stearley, DSN 2007), via LogHub." \
	  -about-url $(UI_REPO)

search: ## Search from the command line: make search Q="timeout error"
	@test -n "$(Q)" || { echo 'usage: make search Q="words to find"'; exit 1; }
	go run ./cmd/stratactl search -addr $(STRATA_ADDR) -text "$(Q)" -limit 10

ingest-bgl: ## Load the real BGL dataset (BGL=path, default ~/strata-datasets/BGL.log), about 3 minutes
	go run ./cmd/stratactl ingest -addr $(STRATA_ADDR) -file $(BGL) -format bgl

##@ Generated traffic from your shell (the container does this too, with `make up`)

gen: ## Run the generator in this terminal until Ctrl-C
	go run ./cmd/strata-gen $(GEN_FLAGS) $(if $(DURATION),-duration $(DURATION))

soak: ## Timed generator run, then check no acknowledged line was lost (DURATION=2m)
	go run ./cmd/strata-gen $(GEN_FLAGS) -duration $(or $(DURATION),2m) -fail-on-loss

##@ Develop

build: ## Compile everything
	go build ./...

test: ## Run the tests with the race detector (start `make server` first for the S3 tests)
	go test -race -count=1 ./...

test-e2e: ## Test the real containers (builds the image, about 2 minutes)
	go test -tags e2e -count=1 -timeout 15m ./e2e

fmt: ## Fail if any Go file is not gofmt-formatted
	@test -z "$$(gofmt -l .)" || { echo "not formatted:"; gofmt -l .; exit 1; }

vet: ## go vet, including the end-to-end tests
	go vet ./... && go vet -tags e2e ./e2e

check: fmt vet test ## Formatting, vet and tests

proto: ## Regenerate the gRPC code after editing proto/
	go run github.com/bufbuild/buf/cmd/buf@v1.73.0 generate
