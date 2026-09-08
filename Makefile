# Everything runs as a bare process: the box has no container runtime.
.DEFAULT_GOAL := help

GO      ?= go
BIN     := bin
INDEX   ?= 0
LISTEN  ?= :8080
REPLICAS ?= replica-0=http://127.0.0.1:8000
POLICY  ?= round_robin

# How many times each load level is repeated. Three is the floor: a p99 over one
# run is noise, and a difference smaller than the spread across runs is a
# difference between a policy and itself.
#
# session_affinity wants more than the others, and it is the one policy that
# does. Its replica choice is a hash of the session id, so each repetition —
# which sends a different slice of the workload's user space, and therefore
# different session ids — lands the sessions on the replicas differently. Some
# repetitions draw an even split and some draw a lumpy one, and that spread is
# on top of the run-to-run noise every policy has. Left at three, a real
# difference between session affinity and prefix affinity can land inside the
# spread and be reported as too small to call, which is the one comparison this
# project exists to make. Five narrows it.
#
#     make bench POLICY=session_affinity REPS=5
#
# Repetitions are their own cells, so raising this later adds runs rather than
# redoing the ones already on disk.
REPS    ?= 3

# The concurrency sweep. RUN_DIR is where cells land and where an interrupted
# sweep resumes from.
#
# SLO_FROM is a characterization to take the derived SLO from — set it to CHAR_DIR
# once that pass has run, and the threshold every cell is judged against is the one
# that was derived rather than one retyped off a table. Unset, no SLO is applied
# unless BENCH_ARGS states one, and a cell run without one records that fact rather
# than reporting a goodput that was never checked against anything.
RUN_DIR   ?= runs/concurrency
SLO_FROM  ?=
ROUTER    ?= http://127.0.0.1:8080
BENCH_ARGS ?=

# The workload every policy is compared on. Frozen on purpose.
#
# A comparison is only a comparison if both sides sent the same bytes, and the
# harness enforces that: compare refuses cells whose workload names differ,
# because the difference between the policies would otherwise include a
# difference in prompts. So this lives in one variable rather than in each
# operator's shell history, and it does not change once a sweep has run under it.
#
# multiturn, not fixed. Under the fixed workload every request is one standalone
# message and nothing is resent, so there is no shared prefix for a policy to
# preserve: session affinity would pin each session to a replica that has
# nothing cached for it, score the same as round-robin, and look like a real
# measurement. The first two-policy comparison (2026-09-08) ran on fixed, which
# was sound for two policies that ignore caching and is not reusable for any
# policy that does not.
#
# 369 sessions is working set 1.0 against the 755,712 tokens measured off all six
# replicas, at 2,048 tokens a session. It is written as a count rather than as
# -working-set because capacity is a measurement that moves between bring-ups:
# the ratio would silently derive a different pool on a rebuilt fleet, change
# this name, and split the comparison in two.
#
# branching is on because idea.md §5 names branched conversations as a place
# prefix affinity should separate from session affinity -- they share an ancestor
# under a new session id, so a hash scatters them and an index finds them. Turned
# off, one of the two mechanisms the project rests on is absent from the trace.
# The shared system prompt is there for realism, not for the result: §5 strikes
# it from that list, because a hash scatters those evenly and every replica
# caches the prefix independently.
#
# skew stays 0 here. It is the pressure grid's own axis, and pinning the headline
# comparison to one arbitrary point of an axis that is about to be swept in full
# would answer a question #18 is asking properly.
WORKLOAD_ARGS ?= -workload multiturn \
	-sessions 369 \
	-turns-per-session 4 \
	-prompt-tokens 448 \
	-output-tokens 64 \
	-branching 0.3 \
	-shared-system-prompt 0.3 \
	-skew 0 \
	-seed 1

# The router's own per-request rows. They carry accept-to-dispatch overhead and
# the router's view of each outcome, and join to the harness rows by request id,
# so they are kept by default rather than discarded: a sweep that threw them away
# could not report router overhead per request or, later, belief divergence.
RECORDS ?= $(RUN_DIR)/router.jsonl

# The live replica the contract test runs against, or the whole fleet's spec —
# `ops/fleet.sh replicas` — to assert the contract against every replica. Point
# it at replicas of the pinned engine version during bring-up.
CONTRACT_REPLICA ?=

# Where the characterization lands: aggregate KV capacity, host topology, the
# hardware latency floor, the SLO derived from it, and the replica symmetry
# verdict. Everything downstream scales off these, so they get their own
# directory rather than sharing a sweep's.
CHAR_DIR ?= runs/characterization
CHAR_ARGS ?=

# The headline goodput number: the open-loop driver at a ladder of arrival
# rates. Its own directory because it is a different axis of a different driver
# from RUN_DIR's concurrency sweep, and the two are read side by side rather
# than merged. GOODPUT_ARGS carries the SLO, which this measurement is
# meaningless without: goodput is requests per second that met it.
# GOODPUT_RATES overrides the ladder; unset, the command uses the package's own,
# which is the single copy of it.
GOODPUT_DIR ?= runs/goodput
GOODPUT_RATES ?=
GOODPUT_ARGS ?=

# The contention experiment: all six replicas driven at once, compared by NUMA
# node. Separate from CHAR_DIR because it is a different measurement of a
# different thing — a solo latency and a contended one are not comparable, and
# sharing a directory would invite averaging them.
CONTENTION_DIR ?= runs/contention
CONTENTION_ARGS ?=

.PHONY: help
help: ## List targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the router and the fake replica for this machine
	$(GO) build -o $(BIN)/ ./cmd/...

.PHONY: router-linux
router-linux: ## Cross-compile a static router for the GPU box
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN)/router-linux-amd64 ./cmd/router

.PHONY: linux
linux: ## Cross-compile every command for the GPU box, which has no Go toolchain
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN)/router-linux-amd64 ./cmd/router
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN)/bench-linux-amd64 ./cmd/bench
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN)/preflight-linux-amd64 ./cmd/preflight
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN)/characterize-linux-amd64 ./cmd/characterize
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN)/compare-linux-amd64 ./cmd/compare

.PHONY: test
test: ## Run the full suite under the race detector
	$(GO) test -race -count=1 ./...

.PHONY: contract
contract: ## Run the replica contract test, against a live replica when CONTRACT_REPLICA is set
	KVROUTE_CONTRACT_REPLICA=$(CONTRACT_REPLICA) $(GO) test -v -count=1 ./test/contract/

.PHONY: fmt
fmt: ## Format
	$(GO) fmt ./...

.PHONY: vet
vet: ## Vet
	$(GO) vet ./...

.PHONY: check
check: fmt vet test ## Format, vet and test

.PHONY: preflight
preflight: ## Refuse to proceed if any GPU already holds memory
	ops/fleet.sh preflight

.PHONY: fleet-up
fleet-up: ## Preflight, then bring up all six replicas one at a time
	ops/fleet.sh up

.PHONY: fleet-down
fleet-down: ## Stop every replica
	ops/fleet.sh down

.PHONY: fleet-status
fleet-status: ## Show which replicas are running
	ops/fleet.sh status

.PHONY: bench
bench: build ## Sweep concurrency against the running fleet, resuming from RUN_DIR
	$(BIN)/bench -router $(ROUTER) -dir $(RUN_DIR) -policy $(POLICY) \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpus "$$(ops/fleet.sh env REPLICA_COUNT)" \
		-replicas "$$(ops/fleet.sh replicas)" \
		-repetitions $(REPS) \
		$(WORKLOAD_ARGS) \
		$(if $(SLO_FROM),-slo-from $(SLO_FROM),) \
		$(BENCH_ARGS)

.PHONY: goodput
goodput: build ## Offer a ladder of arrival rates open-loop and record goodput at each, resuming from GOODPUT_DIR
	$(BIN)/bench -router $(ROUTER) -dir $(GOODPUT_DIR) -policy $(POLICY) -driver open_loop \
		$(if $(GOODPUT_RATES),-arrival-rates $(GOODPUT_RATES),) \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpus "$$(ops/fleet.sh env REPLICA_COUNT)" \
		-replicas "$$(ops/fleet.sh replicas)" \
		-repetitions $(REPS) \
		$(WORKLOAD_ARGS) \
		$(if $(SLO_FROM),-slo-from $(SLO_FROM),) \
		$(GOODPUT_ARGS)

# The policy comparison: the table the project's claim is made in. It reads cell
# records only, so it needs no fleet and no GPU — a checkout is enough. Both
# policies' cells can live in one RUN_DIR, because a cell id carries its policy;
# name several directories here if they were swept separately.
COMPARE_DIRS ?= $(RUN_DIR) $(GOODPUT_DIR)
COMPARE_OUT ?= runs/comparison.md

.PHONY: compare
compare: build ## Put the swept policies' goodput in one comparison table
	$(BIN)/compare -out $(COMPARE_OUT) $(COMPARE_DIRS)

.PHONY: characterize
characterize: build ## Measure KV capacity, the latency floor, the SLO and replica symmetry
	$(BIN)/characterize -dir $(CHAR_DIR) \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpus "$$(ops/fleet.sh env REPLICA_COUNT)" \
		-replicas "$$(ops/fleet.sh replicas)" \
		$(CHAR_ARGS)

.PHONY: contention
contention: build ## Drive all six replicas at once and compare NUMA nodes
	$(BIN)/characterize -dir $(CONTENTION_DIR) -schedule together \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpus "$$(ops/fleet.sh env REPLICA_COUNT)" \
		-replicas "$$(ops/fleet.sh replicas)" \
		$(CONTENTION_ARGS)

.PHONY: replica-up
replica-up: ## Start one vLLM replica on GPU INDEX with the pinned engine and forced backend
	ops/replica.sh up $(INDEX)

.PHONY: replica-down
replica-down: ## Stop the replica on GPU INDEX
	ops/replica.sh down $(INDEX)

.PHONY: replica-status
replica-status: ## Show which replicas are running
	ops/replica.sh status

.PHONY: run-router
run-router: build ## Run the router against REPLICAS, keeping its own rows in RECORDS
	@mkdir -p $(dir $(RECORDS))
	$(BIN)/router -listen $(LISTEN) -replicas $(REPLICAS) -policy $(POLICY) -records $(RECORDS)

.PHONY: run-fake
run-fake: build ## Run one fake replica on :8000, for driving the router without a GPU
	$(BIN)/fakereplica -listen :8000 -id replica-0 -ttft 200ms -inter-token 20ms -output-tokens 64

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN)
