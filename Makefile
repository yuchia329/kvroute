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
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN)/pressuremap-linux-amd64 ./cmd/pressuremap
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN)/chaos-linux-amd64 ./cmd/chaos
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN)/recovery-linux-amd64 ./cmd/recovery

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

# The pressure grid: idea.md §6's headline, and the most expensive sweep in the
# project at 4 policies x 4 WS x 3 skew x 3 reps = 144 cells, ~17 GPU-hours.
#
# Two axes because the two pressures are physically different and fire different
# branches of the spill rule: working set ratio drives eviction and trips the KV
# high-water mark, skew drives load imbalance and trips the imbalance factor.
# Holding either fixed would sweep the pressure that evicts while leaving the
# pressure that unbalances untested.
#
# One point per invocation, and one directory per point, because each point
# offers its own workload and `compare` refuses to put two workloads in one
# table. Sweep every point of bench.PressureGrid() for every policy, then draw
# the map across the directories:
#
#     for ws in 0.25 1 3 8; do for skew in 0 1 1.4; do \
#       make pressure-grid POLICY=session_affinity PRESSURE_WS=$ws PRESSURE_SKEW=$skew; \
#     done; done
#     make pressure-map
#
# ⚠️ KV_CAPACITY is what makes a cell state its WS point at all. Without it the
# pool is a plain session count with no denominator to be a ratio against, the
# cells record no working set, and the map has no axis to draw — see
# GridPoint.Stated. It is carried by the geometry below, which is why that is
# shared with the tunable sweep rather than retyped.
PRESSURE_DIR ?= runs/pressure
PRESSURE_WS ?=
PRESSURE_SKEW ?=

# The single load rung the whole grid runs at, and it must equal
# bench.PressureConcurrency — a test pins the axes, and this is the one
# parameter of the grid that lives only here.
#
# 32 for the two reasons spelled out in internal/bench/pressuregrid.go: spill
# only fires under pressure, so a rung too low would report policy 4 collapsing
# into policy 3 everywhere; and the spill thresholds this grid runs with are
# chosen on the tunable sweep at 32, so another rung would apply thresholds at a
# load they were not chosen at.
PRESSURE_CONCURRENCY ?= 32

# The same geometry the tunable sweep runs, deliberately: everything but WS and
# skew is the frozen workload's, so a grid cell differs from a comparison cell in
# pressure and in nothing else. Shared rather than retyped, so the two cannot
# drift into sending different bytes under names that claim they did not.
PRESSURE_GEOMETRY = $(TUNABLES_GEOMETRY)

# The frozen cell geometry (ADR-0007), stated here because the bench command's
# own defaults are 60 s with a 10 s warm-up and nothing else on this target
# would override them.
#
# The 50 s warm-up is the one that matters most to this grid, and it is not a
# margin for comfort. Prefix affinity's index starts empty and fills during the
# cell, and because every cell deliberately sends prompts the fleet has not seen
# (ADR-0004) it cannot be pre-warmed across cells. At the old 25 s warm-up, all
# three of prefix affinity's cells at 128 users were discarded for warm-up drift
# while none of session affinity's were: the check penalises the only policy with
# something to learn. Running this grid at the 10 s default would silently throw
# away the policy the grid exists to measure.
#
# 150 s with 50 s of it forfeited leaves 100 s measured per cell. At 144 cells
# that is about 6.8 hours of cell time, plus one fleet restart per policy.
PRESSURE_CELL = -cell-duration 150s -warmup 50s -settle 10s

# The spill configuration policy 4 runs under, settled by #16's tunable sweep and
# recorded in code as bench.Chosen. Stated here as the same pair of numbers
# because make cannot read a Go variable; drift is caught at runtime rather than
# trusted, because bench checks this label against what the router actually
# reports before it runs a single cell.
#
# The KV half is 0 — the condition is OFF, and that is a finding rather than an
# omission. vllm:kv_cache_usage_perc counts blocks held by running requests, so it
# reads the active batch and not cache residency: #16 measured
# kv = 0.02128 + 0.02135 x inflight at r = 0.973, which makes the KV branch the
# load branch on this fleet. Any non-zero value either cannot fire or fires on
# load, which the imbalance factor already covers. It also means this grid cannot
# answer #18's separability criterion; that waits on #28, and the map says so
# rather than reporting a column of zeros as a result.
#
# Only prefix affinity has a spill rule, so only prefix affinity is labelled with
# one. The other three policies have no valve and must not be labelled as if they
# did.
PRESSURE_SPILL ?= 0/2
PRESSURE_SPILL_LABEL = $(if $(filter prefix_affinity,$(POLICY)),-spill $(PRESSURE_SPILL),)

.PHONY: pressure-grid
pressure-grid: build ## Run one point of the pressure grid: PRESSURE_WS x PRESSURE_SKEW at one concurrency
	@test -n "$(PRESSURE_WS)" || { echo "pressure-grid: set PRESSURE_WS to a point of bench.PressureWorkingSets (0.25, 1, 3 or 8)" >&2; exit 1; }
	@test -n "$(PRESSURE_SKEW)" || { echo "pressure-grid: set PRESSURE_SKEW to a point of bench.PressureSkews (0, 1 or 1.4)" >&2; exit 1; }
	@test -n "$(KV_CAPACITY)" || { echo "pressure-grid: KV_CAPACITY is required, or the cells state no working set and the map has no axis" >&2; exit 1; }
	$(BIN)/bench -router $(ROUTER) -dir $(PRESSURE_DIR)/ws$(PRESSURE_WS)-skew$(PRESSURE_SKEW) \
		-policy $(POLICY) $(PRESSURE_SPILL_LABEL) \
		-concurrency $(PRESSURE_CONCURRENCY) \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpu-indexes "$$(ops/fleet.sh env REPLICA_GPUS)" \
		-replicas "$$(ops/fleet.sh replicas)" \
		-repetitions $(REPS) \
		$(PRESSURE_CELL) \
		$(PRESSURE_GEOMETRY) -working-set $(PRESSURE_WS) -skew $(PRESSURE_SKEW) \
		$(if $(SLO_FROM),-slo-from $(SLO_FROM),) \
		$(BENCH_ARGS)

# The map itself. Like compare and divergence it reads cell records only, so it
# needs no fleet and no GPU — a checkout is enough.
#
# It exits non-zero on one condition: a grid where nothing separated anywhere and
# the mechanism never fired either. That is not a null result but a workload that
# applied no pressure, and §6 says to correct it before touching any policy.
PRESSURE_MAP_DIRS ?= $(wildcard $(PRESSURE_DIR)/*)
PRESSURE_MAP_OUT ?= runs/pressuremap.md

.PHONY: pressure-map
pressure-map: build ## Draw the pressure map across every point of the grid that has been run
	@test -n "$(PRESSURE_MAP_DIRS)" || { echo "pressure-map: no grid points under $(PRESSURE_DIR); run pressure-grid first" >&2; exit 1; }
	$(BIN)/pressuremap -out $(PRESSURE_MAP_OUT) $(PRESSURE_MAP_DIRS)

# The chaos test (#19, idea.md §7): one replica taken away under steady
# open-loop load and brought back, once per policy, and the recovery curves
# compared. ops/chaos.sh runs one policy and says what the operator does between
# them: bring the fleet down and up, and start the router on the next policy.
#
# Kill and drain are separate runs into separate directories. A kill forces the
# reroute and produces the drops nothing can save; a drain should cost nothing.
#
# The run's shape is fixed here, so both policies' runs line up bucket for
# bucket — which the comparison checks rather than trusts: a 50 s warm-up for
# the prefix index to fill (ADR-0007's), 50 s of healthy baseline, the fault at
# 100 s, the replica restarted at 160 s — a restart takes about 40 s, so it is
# back in rotation near 200 s — and 100 s after that to see whether goodput comes
# back and stays.
#
# CHAOS_RATE is a judgement, and is stated as one. It has to sit where the whole
# fleet holds goodput comfortably, or there is no healthy baseline to recover
# to: take it from the goodput sweep, below both affinity policies' knees.
CHAOS_DIR   ?= runs/chaos
CHAOS_FAULT ?= kill
CHAOS_GPU   ?= 2
CHAOS_RATE  ?= 8
CHAOS_ARGS  ?=
CHAOS_RUN    = -duration 300s -warmup 50s -fault-at 100s -recover-at 160s -bucket 5s
# A kill is ops/replica.sh kill: SIGKILL to the replica and its engine at once. A
# drain is stopped with ops/replica.sh down, and only after the router has
# drained it — the run waits for its in-flight count to reach zero first.
CHAOS_STOP   = $(if $(filter drain,$(CHAOS_FAULT)),ops/replica.sh down $(CHAOS_GPU),ops/replica.sh kill $(CHAOS_GPU))

.PHONY: chaos
chaos: build ## Take one replica away under load and record its recovery: CHAOS_FAULT=kill|drain
	@test -n "$(SLO_FROM)" || { echo "chaos: SLO_FROM is required: goodput is defined by the SLO, and a recovery curve without one has nothing to plot" >&2; exit 1; }
	$(BIN)/chaos -router $(ROUTER) -dir $(CHAOS_DIR)/$(CHAOS_FAULT)-$(POLICY) -policy $(POLICY) \
		-replica replica-$(CHAOS_GPU) -fault $(CHAOS_FAULT) \
		-stop-cmd "$(CHAOS_STOP)" -start-cmd "ops/replica.sh up $(CHAOS_GPU)" \
		-arrival-rate $(CHAOS_RATE) $(CHAOS_RUN) \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpu-indexes "$$(ops/fleet.sh env REPLICA_GPUS)" \
		$(WORKLOAD_ARGS) \
		-slo-from $(SLO_FROM) \
		$(CHAOS_ARGS)

# The comparison. Like compare it reads records only, so a checkout is enough.
.PHONY: recovery
recovery: build ## Compare the recovery curves of every policy that ran CHAOS_FAULT
	@test -n "$(wildcard $(CHAOS_DIR)/$(CHAOS_FAULT)-*)" || { echo "recovery: no $(CHAOS_FAULT) runs under $(CHAOS_DIR); run ops/chaos.sh first" >&2; exit 1; }
	$(BIN)/recovery -out runs/recovery-$(CHAOS_FAULT).md $(wildcard $(CHAOS_DIR)/$(CHAOS_FAULT)-*)

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
