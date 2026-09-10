# Everything runs as a bare process: the box has no container runtime.
.DEFAULT_GOAL := help

GO      ?= go
BIN     := bin
INDEX   ?= 0
LISTEN  ?= :8080
REPLICAS ?= replica-0=http://127.0.0.1:8000
# The policy the router runs. Only this varies between benchmark runs.
#
# prefix_affinity additionally needs PREFIX_CALIBRATION to exist — see below.
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
# 307 sessions is working set 1.0 against the 629,760 tokens measured off the
# five-card fleet on 2026-09-08, at 2,048 tokens a session. Every replica reports
# 7,872 GPU blocks of 16 tokens -- 125,952 each, which is exactly a sixth of the
# 755,712 the six-card fleet reported, so the per-card figure did not drift and
# only the card count changed.
#
# Measured on the five rather than scaled from the six on principle: capacity is
# read off the replicas and summed, never extrapolated, because a figure taken
# from one card and multiplied has already moved between two bring-ups of
# identical configuration. Here the two happened to agree, which is worth knowing
# and is not a reason to have assumed it.
#
# It is written as a count rather than as -working-set because capacity is a
# measurement that moves between bring-ups: the ratio would silently derive a
# different pool on a rebuilt fleet, change this name, and split the comparison
# in two.
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
	-sessions 307 \
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

# The prefix index's calibration: aggregate fleet KV capacity, the measured
# prompt bytes-per-token ratio, and the engines' own idle-before-evict
# distribution. `make calibrate` writes it and the router is started against it.
#
# prefix_affinity will not run without it. Its node cap and TTL are modelling
# decisions about hardware the router does not own, and idea.md §4.3 is explicit
# that sizing them to the fleet is what makes them defensible rather than
# arbitrary — so they are measured off the fleet rather than defaulted in the
# source, and a router asked for that policy with no calibration refuses to
# start.
#
# CALIBRATE_FROM is a sweep to measure the bytes-per-token ratio from. The
# baselines run before prefix affinity does, so by the time this is needed there
# are cells to take it off.
#
# ⚠️ The idle-before-evict histogram needs --kv-cache-metrics and stays empty
# until the fleet has actually evicted blocks, so this runs against a fleet that
# has been under load — not one that has just come up. Never enable that flag
# mid-experiment to make this command work: it changes the engine configuration
# every cell is supposed to share, and invalidates every completed cell.
PREFIX_CALIBRATION ?= runs/prefix-calibration.json
CALIBRATE_FROM ?= $(RUN_DIR)

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
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN)/calibrate-linux-amd64 ./cmd/calibrate
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN)/divergence-linux-amd64 ./cmd/divergence

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

# The spill grid point the router runs and the cells are labelled with. Empty
# runs prefix affinity with no spill rule, which is the policy the four-policy
# comparison measured and the baseline the grid is read against.
#
# It has to be passed to both `run-router` and `bench`: the router applies it and
# the sweep labels its cells with it, and the harness refuses to start when the
# two disagree. That refusal is the whole reason it is spelled twice rather than
# inferred once — see checkGridPoint.
#
#	make run-router POLICY=prefix_affinity KV_HIGH_WATER=0.80 LOAD_IMBALANCE=2.0
#	make tunables   KV_HIGH_WATER=0.80 LOAD_IMBALANCE=2.0
KV_HIGH_WATER ?=
LOAD_IMBALANCE ?=
SPILL_ARGS = $(if $(KV_HIGH_WATER),-kv-high-water $(KV_HIGH_WATER),) $(if $(LOAD_IMBALANCE),-load-imbalance-factor $(LOAD_IMBALANCE),)
# Each half defaults to 0 — the value that disables that condition — so a
# one-sided grid point reaches the sweep as it reaches the router. Interpolating
# an empty half would emit "-spill 0.80/" and fail in ParseSpill, leaving a rule
# the router will happily run unreachable from the harness.
SPILL_LABEL = $(if $(KV_HIGH_WATER)$(LOAD_IMBALANCE),-spill $(if $(KV_HIGH_WATER),$(KV_HIGH_WATER),0)/$(if $(LOAD_IMBALANCE),$(LOAD_IMBALANCE),0),)

# The spill thresholds are measured on two workload points, one each, rather
# than crossed on one. The generator will not let both pressures be high at
# once: concentrating the draws onto hot conversations means touching fewer
# distinct ones, so skew discounts working set — at WS 1 a cell realises 0.97 of
# its label at skew 0 and 0.40 at skew 1.4. So each threshold is measured where
# its own pressure is live and the other condition is switched off, which is
# what makes each row single-factor. See internal/bench/spillgrid.go.
#
# Each sweep opens with a spill-off cell at its own workload point. The
# four-policy comparison's prefix_affinity cells cannot serve as that reference:
# they run the frozen workload at WS 1 / skew 0, and a row measured under
# different pressure is not a baseline.
#
# KV_CAPACITY is the measured aggregate fleet KV these WS points are ratios
# against — read off the replicas, never estimated, and it moves between
# bring-ups. Take it from the characterization capacity record.
TUNABLES_DIR ?= runs/tunables
KV_CAPACITY ?= 629760

# idea.md §6 budgets the tunable sweep at a single concurrency, which is what
# keeps it affordable. One rung, chosen where the fleet is loaded enough for
# both spill conditions to be reachable: an idle fleet never crosses a
# high-water mark and has no load imbalance to speak of.
TUNABLES_CONCURRENCY ?= 32

# The two points. Everything but WS and skew is the frozen workload's geometry,
# so a tunables cell differs from a comparison cell in pressure and in nothing
# else.
TUNABLES_GEOMETRY = -workload multiturn -turns-per-session 4 -prompt-tokens 448 \
	-output-tokens 64 -branching 0.3 -shared-system-prompt 0.3 -seed 1 \
	-kv-capacity $(KV_CAPACITY)
TUNABLES_KV_WORKLOAD   = $(TUNABLES_GEOMETRY) -working-set 3 -skew 0
TUNABLES_LOAD_WORKLOAD = $(TUNABLES_GEOMETRY) -working-set 1 -skew 1.4

.PHONY: tunables-kv
tunables-kv: build ## Run one point of the KV high-water sweep (WS 3, skew 0)
	@test -n "$(KV_HIGH_WATER)" || { echo "tunables-kv: set KV_HIGH_WATER to a level from bench.KVHighWaterGrid, or to 0 for the spill-off reference cell" >&2; exit 1; }
	@test -z "$(LOAD_IMBALANCE)" || { echo "tunables-kv: LOAD_IMBALANCE must stay unset; this point measures the high-water mark alone" >&2; exit 1; }
	$(BIN)/bench -router $(ROUTER) -dir $(TUNABLES_DIR)/kv -policy prefix_affinity $(SPILL_LABEL) \
		-concurrency $(TUNABLES_CONCURRENCY) \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpu-indexes "$$(ops/fleet.sh env REPLICA_GPUS)" \
		-replicas "$$(ops/fleet.sh replicas)" \
		-repetitions $(REPS) \
		$(TUNABLES_KV_WORKLOAD) \
		$(if $(SLO_FROM),-slo-from $(SLO_FROM),) \
		$(BENCH_ARGS)

.PHONY: tunables-load
tunables-load: build ## Run one point of the load imbalance sweep (WS 1, skew 1.4)
	@test -n "$(LOAD_IMBALANCE)" || { echo "tunables-load: set LOAD_IMBALANCE to a level from bench.LoadImbalanceGrid, or to 0 for the spill-off reference cell" >&2; exit 1; }
	@test -z "$(KV_HIGH_WATER)" || { echo "tunables-load: KV_HIGH_WATER must stay unset; this point measures the imbalance factor alone" >&2; exit 1; }
	$(BIN)/bench -router $(ROUTER) -dir $(TUNABLES_DIR)/load -policy prefix_affinity $(SPILL_LABEL) \
		-concurrency $(TUNABLES_CONCURRENCY) \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpu-indexes "$$(ops/fleet.sh env REPLICA_GPUS)" \
		-replicas "$$(ops/fleet.sh replicas)" \
		-repetitions $(REPS) \
		$(TUNABLES_LOAD_WORKLOAD) \
		$(if $(SLO_FROM),-slo-from $(SLO_FROM),) \
		$(BENCH_ARGS)

.PHONY: bench
bench: build ## Sweep concurrency against the running fleet, resuming from RUN_DIR
	$(BIN)/bench -router $(ROUTER) -dir $(RUN_DIR) -policy $(POLICY) $(SPILL_LABEL) \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpu-indexes "$$(ops/fleet.sh env REPLICA_GPUS)" \
		-replicas "$$(ops/fleet.sh replicas)" \
		-repetitions $(REPS) \
		$(WORKLOAD_ARGS) \
		$(if $(SLO_FROM),-slo-from $(SLO_FROM),) \
		$(BENCH_ARGS)

.PHONY: goodput
goodput: build ## Offer a ladder of arrival rates open-loop and record goodput at each, resuming from GOODPUT_DIR
	$(BIN)/bench -router $(ROUTER) -dir $(GOODPUT_DIR) -policy $(POLICY) -driver open_loop $(SPILL_LABEL) \
		$(if $(GOODPUT_RATES),-arrival-rates $(GOODPUT_RATES),) \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpu-indexes "$$(ops/fleet.sh env REPLICA_GPUS)" \
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

# Belief divergence: how far the router's prefix index was from what the engines
# actually held, per request. Like compare, it reads the rows a sweep wrote and
# needs no fleet and no GPU.
#
# DIVERGENCE_JSON is the half of the output the router consumes rather than a
# person: the reading `make calibrate` folds into the next calibration, which
# scales the index's node cap by the share of its belief the engines honoured
# (ADR-0007). `make calibrate` picks it up automatically once it exists, so the
# order is sweep, divergence, calibrate, sweep again.
#
# Name several directories to draw the working-set axis across the points of a
# pressure grid, which is where that axis actually varies:
#
#     make divergence DIVERGENCE_DIRS="runs/ws0.25 runs/ws1 runs/ws3 runs/ws8"
#
# ⚠️ A sweep states its WS point only if it was given the measured capacity the
# ratio is against. Pass -kv-capacity in BENCH_ARGS and the ratio is derived from
# the session pool without changing a byte of what the cell sends — the workload
# name, and so the comparison, is untouched.
DIVERGENCE_DIRS ?= $(RUN_DIR) $(GOODPUT_DIR)
DIVERGENCE_OUT ?= runs/divergence.md
DIVERGENCE_JSON ?= runs/divergence.json

.PHONY: compare
compare: build ## Put the swept policies' goodput in one comparison table
	$(BIN)/compare -out $(COMPARE_OUT) $(COMPARE_DIRS)

.PHONY: characterize
characterize: build ## Measure KV capacity, the latency floor, the SLO and replica symmetry
	$(BIN)/characterize -dir $(CHAR_DIR) \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpu-indexes "$$(ops/fleet.sh env REPLICA_GPUS)" \
		-replicas "$$(ops/fleet.sh replicas)" \
		$(CHAR_ARGS)

.PHONY: contention
contention: build ## Drive all six replicas at once and compare NUMA nodes
	$(BIN)/characterize -dir $(CONTENTION_DIR) -schedule together \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpu-indexes "$$(ops/fleet.sh env REPLICA_GPUS)" \
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

.PHONY: calibrate
calibrate: build ## Measure the prefix index's node cap and TTL off the fleet and a sweep
	$(BIN)/calibrate -replicas "$$(ops/fleet.sh replicas)" \
		-from $(CALIBRATE_FROM) \
		$(if $(wildcard $(DIVERGENCE_JSON)),-divergence $(DIVERGENCE_JSON),) \
		-out $(PREFIX_CALIBRATION)

.PHONY: divergence
divergence: build ## Measure how far the router's index was from what the engines held
	$(BIN)/divergence -out $(DIVERGENCE_OUT) -calibration-out $(DIVERGENCE_JSON) $(DIVERGENCE_DIRS)

.PHONY: run-router
run-router: build ## Run the router against REPLICAS, keeping its own rows in RECORDS
	@mkdir -p $(dir $(RECORDS))
	$(BIN)/router -listen $(LISTEN) -replicas $(REPLICAS) -policy $(POLICY) -records $(RECORDS) \
		$(if $(wildcard $(PREFIX_CALIBRATION)),-prefix-calibration $(PREFIX_CALIBRATION),) \
		$(SPILL_ARGS)

.PHONY: run-fake
run-fake: build ## Run one fake replica on :8000, for driving the router without a GPU
	$(BIN)/fakereplica -listen :8000 -id replica-0 -ttft 200ms -inter-token 20ms -output-tokens 64

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN)
