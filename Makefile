# Everything runs as a bare process: the box has no container runtime.
.DEFAULT_GOAL := help

GO      ?= go
BIN     := bin

# Which machine's binaries the run targets invoke.
#
# A workstation has a Go toolchain and a checkout, so `build` compiles $(BIN)/bench
# and `make bench` runs it. The fleet host has neither: ~/kvroute there is scp'd
# files rather than a clone, and the only binaries on it are the
# $(BIN)/*$(LINUX_EXE) that `make linux` cross-compiled and someone copied across.
# So every run target names $(BIN)/<command>$(EXE), and on the box that resolves to
# the cross-compiled one.
#
# The switch is the toolchain rather than the operating system, because the box's
# mark is that it cannot build: a Linux workstation with Go should behave like
# this Mac, and does. Override it either way when the guess is wrong —
# `make chaos BOX=1 ...` to force the cross-compiled binaries, BOX=0 to force
# this machine's.
LINUX_EXE := -linux-amd64
BOX ?= $(if $(shell command -v $(GO) 2>/dev/null),0,1)
EXE := $(if $(filter 1,$(BOX)),$(LINUX_EXE),)
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

# Every run target depends on this, so on the box it has to step aside rather
# than fail: there is nothing to build there and nothing that needs building.
# It still refuses when the cross-compiled binaries are absent, because the
# alternative is each target failing separately with "no such file".
.PHONY: build
build: ## Build every command for this machine, or on the box check the cross-compiled ones are there
ifeq ($(BOX),1)
	@ls $(BIN)/*$(EXE) >/dev/null 2>&1 || { echo "build: no Go toolchain here and no $(BIN)/*$(EXE) to run. Run 'make box-sync' from a machine with Go and a checkout." >&2; exit 1; }
	@echo "build: no Go toolchain here, so running the $(BIN)/*$(EXE) that 'make linux' cross-compiled"
else
	$(GO) build -o $(BIN)/ ./cmd/...
endif

.PHONY: router-linux
router-linux: ## Cross-compile a static router for the GPU box
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN)/router$(LINUX_EXE) ./cmd/router

# Read from cmd/ rather than listed. A target that names $(BIN)/x$(EXE) and finds
# nothing there is the same failure #32 was, and the list this target used to
# carry had already fallen four commands behind cmd/ — disagg, overhead,
# spillsignal and fakereplica were all missing, so four targets could never have
# run on the box however they were spelled.
LINUX_CMDS = $(notdir $(wildcard cmd/*))

.PHONY: linux
linux: ## Cross-compile every command for the GPU box, which has no Go toolchain
	@for cmd in $(LINUX_CMDS); do \
	  echo "CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN)/$$cmd$(LINUX_EXE) ./cmd/$$cmd"; \
	  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath -o $(BIN)/$$cmd$(LINUX_EXE) ./cmd/$$cmd || exit 1; \
	done

# Putting the repo on the fleet host. ~/kvroute there is not a checkout and never
# becomes one: it is the Makefile, ops/ and the cross-compiled binaries, beside
# the run directories and logs the sweeps have written. This copies exactly the
# first three.
#
# The Makefile is one of them because without it none of the run targets exist
# there, which is what #32 was: every run on the box was driven by a hand-written
# script because `make bench` was not a thing you could type on it.
#
# No --delete, ever. The box's runs/ and its logs sit in the same directory and
# are the only copy of hours of measurement. Nothing at the box's top level is
# touched either, which is where its own drivers run from — bash reads a script
# as it executes it, so overwriting a running one corrupts it mid-run. Copy those
# across by hand, one at a time, when nothing is running: see ops/box/README.md.
BOX_HOST ?= nlp
BOX_DIR  ?= kvroute

.PHONY: box-sync
box-sync: linux ## Cross-compile, then copy the Makefile, ops/ and the box's binaries to BOX_HOST
	# --stats rather than --info=stats1: the latter is rsync 3.1+, and macOS
	# ships openrsync, which reports itself as 2.6.9-compatible and refuses it.
	# This target had never been run before 2026-09-12 and failed on its first
	# use for exactly that reason. --stats is understood by all three.
	rsync -az --stats --relative Makefile ops $(BIN)/*$(LINUX_EXE) $(BOX_HOST):$(BOX_DIR)/
	@echo ""
	@echo "== on $(BOX_HOST), from ~/$(BOX_DIR), the run targets now work as they do here:"
	@echo "     make bench SLO_FROM=... POLICY=..."
	@echo "     ops/pressure-grid.sh <policy>"
	@echo "     ops/chaos.sh <policy> kill SLO_FROM=..."

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

# The live view of a run: Prometheus on the fleet host, Grafana on the
# workstation behind an ssh tunnel (idea.md §3). Optional by construction — no
# other target here reads it, and the router and the replicas only answer its
# scrapes — so a sweep runs the same with all of it stopped. Its ports and
# versions live in ops/observability.env rather than ops/versions.env, because
# none of it is part of a cell.
#
#   on the fleet host:    ops/prometheus.sh up
#   on the workstation:   make dashboards-up, then http://127.0.0.1:3000/d/kvroute
.PHONY: prometheus-up
prometheus-up: ## On the fleet host: run Prometheus on loopback, scraping the router at ROUTER and every replica
	ROUTER_ADDR=$(patsubst http://%,%,$(ROUTER)) ops/prometheus.sh up

.PHONY: prometheus-down
prometheus-down: ## Stop Prometheus
	ops/prometheus.sh down

.PHONY: prometheus-status
prometheus-status: ## Show whether Prometheus is up and whether its targets answer
	ops/prometheus.sh status

.PHONY: dashboards-up
dashboards-up: ## On the workstation: tunnel to the fleet host's Prometheus and start Grafana with the five panels
	ops/dashboards.sh up

.PHONY: dashboards-down
dashboards-down: ## Stop Grafana and close the tunnel
	ops/dashboards.sh down

.PHONY: dashboards-status
dashboards-status: ## Show the tunnel, Prometheus and Grafana
	ops/dashboards.sh status

# The spill grid point the router runs and the cells are labelled with. Empty
# runs prefix affinity with no spill rule, which is the policy the four-policy
# comparison measured and the baseline the grid is read against.
#
# It has to be passed to both `run-router` and `bench`: the router applies it and
# the sweep labels its cells with it, and the harness refuses to start when the
# two disagree. That refusal is the whole reason it is spelled twice rather than
# inferred once — see checkGridPoint.
#
#	make run-router POLICY=prefix_affinity HIT_RATE_LOW_WATER=0.40 LOAD_IMBALANCE=2.0
#	make tunables   HIT_RATE_LOW_WATER=0.40 LOAD_IMBALANCE=2.0
HIT_RATE_LOW_WATER ?=
LOAD_IMBALANCE ?=

# What the load imbalance factor is a multiple of: the fleet's mean inflight when
# set, and its minimum — the rule as #18 settled it — when empty.
#
# It is a grid axis rather than a fix, because which one is better is a goodput
# question nothing has answered yet. The minimum is a small integer, and at low
# open-loop load it is 0 or 1 on most decisions, so a multiple of it is an
# absolute inflight count: at 6 req/s the settled factor of 2 declined a fifth of
# later turns against 0.674% at the rung it was chosen at (#31).
#
# The denominator rung is one point of the open-loop ladder, so it is the goodput
# target rather than a target of its own:
#
#	make goodput POLICY=prefix_affinity GOODPUT_RATES=6                          # the observing pass, spill off
#	make goodput POLICY=prefix_affinity GOODPUT_RATES=6 LOAD_IMBALANCE=2         # the settled point
#	make goodput POLICY=prefix_affinity GOODPUT_RATES=6 LOAD_IMBALANCE=2 MEAN_DENOMINATOR=1
#
# Read the first with `make load-comparison` before running the rest: it says
# which points can fire at that rung at all. See internal/bench/loaddenominatorgrid.go.
MEAN_DENOMINATOR ?=
SPILL_ARGS = $(if $(HIT_RATE_LOW_WATER),-hit-rate-low-water $(HIT_RATE_LOW_WATER),) $(if $(LOAD_IMBALANCE),-load-imbalance-factor $(LOAD_IMBALANCE),) $(if $(MEAN_DENOMINATOR),-mean-inflight-denominator,)

# Whether the router reads each replica's /metrics. One GET per replica per tick
# gives both the prefix-cache counters the residency branch routes on and the
# batch gauge kept as a column.
#
# The router turns it on by itself whenever HIT_RATE_LOW_WATER is set, because a
# threshold on an unscraped fleet is a condition silently disabled for a whole
# run — #28's original defect. This flag is for the other case: the observing
# pass, which runs with the rule OFF and still needs both signals on every row,
# because the correlations are only comparable when they were recorded against
# the same decisions.
#
#	make run-router POLICY=prefix_affinity SCRAPE_REPLICAS=1
SCRAPE_REPLICAS ?=
SCRAPE_ARGS = $(if $(SCRAPE_REPLICAS),-scrape-replicas,)
# Each half defaults to 0 — the value that disables that condition — so a
# one-sided grid point reaches the sweep as it reaches the router. Interpolating
# an empty half would emit "-spill 0.80/" and fail in ParseSpill, leaving a rule
# the router will happily run unreachable from the harness.
# The denominator is appended only when it is the new one, so every invocation
# written before #31 emits the spec it always emitted and labels the cells it
# always labelled: ParseSpill reads a two-part spec as the fleet minimum.
SPILL_LABEL = $(if $(HIT_RATE_LOW_WATER)$(LOAD_IMBALANCE),-spill $(if $(HIT_RATE_LOW_WATER),$(HIT_RATE_LOW_WATER),0)/$(if $(LOAD_IMBALANCE),$(LOAD_IMBALANCE),0)$(if $(MEAN_DENOMINATOR),/mean,),)

# The stateless prefix hash's own grid point (#26): how many leading 64-byte
# prefix blocks it hashes, and what that hash is worth against load in inflight
# requests. It is spelled twice for the reason the spill point is — the router
# applies it, the sweep labels its cells with it, and the harness refuses to
# start when the two disagree.
#
# Neither is defaulted here or in the router. The window is the number OpenAI has
# never published for "the initial tokens", and defaulting it would make a guess
# look like a citation; the weight is the axis this policy is swept along, so a
# default would be one point of it wearing the name of the policy.
#
#	make run-router POLICY=prefix_hash HASH_BLOCKS=16 HASH_WEIGHT=4
#	make hash-weight HASH_BLOCKS=16 HASH_WEIGHT=4
HASH_BLOCKS ?=
HASH_WEIGHT ?=
HASH_ARGS = $(if $(HASH_BLOCKS),-hash-leading-blocks $(HASH_BLOCKS),) $(if $(HASH_WEIGHT),-hash-weight $(HASH_WEIGHT),)
# A weight of zero is a real point — least-outstanding with a hash that decides
# nothing — so the label defaults the weight rather than the window: an empty
# window is no policy at all and is left to the router to refuse.
HASH_LABEL = $(if $(HASH_BLOCKS),-hash $(HASH_BLOCKS)/$(if $(HASH_WEIGHT),$(HASH_WEIGHT),0),)

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
TUNABLES_RESIDENCY_WORKLOAD = $(TUNABLES_GEOMETRY) -working-set 3 -skew 0
TUNABLES_LOAD_WORKLOAD = $(TUNABLES_GEOMETRY) -working-set 1 -skew 1.4

# The residency sweep's own point. Its levels are not chosen yet: #16's were cut
# against a gauge nobody had seen the range of and two of the three could not
# fire, so bench.HitRateLowWaterGrid is empty until a run says where the
# honoured rate sits. Run this at HIT_RATE_LOW_WATER=0 first — that is the
# observing pass — then read the range off it with `make spill-signal`.
.PHONY: tunables-residency
tunables-residency: build ## Run one point of the residency sweep (WS 3, skew 0)
	@test -n "$(HIT_RATE_LOW_WATER)" || { echo "tunables-residency: set HIT_RATE_LOW_WATER to a level from bench.HitRateLowWaterGrid, or to 0 for the spill-off reference cell that the grid is cut from" >&2; exit 1; }
	@test -z "$(LOAD_IMBALANCE)" || { echo "tunables-residency: LOAD_IMBALANCE must stay unset; this point measures the low-water mark alone" >&2; exit 1; }
	$(BIN)/bench$(EXE) -router $(ROUTER) -dir $(TUNABLES_DIR)/residency -policy prefix_affinity $(SPILL_LABEL) $(FLEET_KV_EVENTS_LABEL) \
		-concurrency $(TUNABLES_CONCURRENCY) \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpu-indexes "$$(ops/fleet.sh env REPLICA_GPUS)" \
		-replicas "$$(ops/fleet.sh replicas)" \
		-repetitions $(REPS) \
		$(TUNABLES_RESIDENCY_WORKLOAD) \
		$(if $(SLO_FROM),-slo-from $(SLO_FROM),) \
		$(BENCH_ARGS)

.PHONY: tunables-load
tunables-load: build ## Run one point of the load imbalance sweep (WS 1, skew 1.4)
	@test -n "$(LOAD_IMBALANCE)" || { echo "tunables-load: set LOAD_IMBALANCE to a level from bench.LoadImbalanceGrid, or to 0 for the spill-off reference cell" >&2; exit 1; }
	@test -z "$(HIT_RATE_LOW_WATER)" || { echo "tunables-load: HIT_RATE_LOW_WATER must stay unset; this point measures the imbalance factor alone" >&2; exit 1; }
	$(BIN)/bench$(EXE) -router $(ROUTER) -dir $(TUNABLES_DIR)/load -policy prefix_affinity $(SPILL_LABEL) $(FLEET_KV_EVENTS_LABEL) \
		-concurrency $(TUNABLES_CONCURRENCY) \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpu-indexes "$$(ops/fleet.sh env REPLICA_GPUS)" \
		-replicas "$$(ops/fleet.sh replicas)" \
		-repetitions $(REPS) \
		$(TUNABLES_LOAD_WORKLOAD) \
		$(if $(SLO_FROM),-slo-from $(SLO_FROM),) \
		$(BENCH_ARGS)

# The stateless prefix hash's weight sweep (#26): one point per invocation, at
# the load point's own pressure.
#
# It runs at WS 1 / skew 1.4 — the same point the load imbalance factor is swept
# at — because the balance between the hash and the load term is the same
# tradeoff the spill rule's load condition makes, and it is only live where
# conversations pile up. At skew 0 the hash lands traffic evenly by construction
# and every weight would read the same.
#
# Each point goes into its own directory. A cell's id names the policy, the load
# level and the repetition and nothing about the weight, so two weights swept
# into one directory would resume each other — the harness refuses that now, and
# the layout here is what keeps it from coming up.
HASH_DIR ?= runs/hash-weight

.PHONY: hash-weight
hash-weight: build ## Run one point of the stateless hash's weight sweep (WS 1, skew 1.4)
	@test -n "$(HASH_BLOCKS)" || { echo "hash-weight: set HASH_BLOCKS to the window the run states, bench.HashLeadingBlocks" >&2; exit 1; }
	@test -n "$(HASH_WEIGHT)" || { echo "hash-weight: set HASH_WEIGHT to a level from bench.HashWeightGrid; 0 is the least-outstanding end of the axis and is a point, not an unset flag" >&2; exit 1; }
	$(BIN)/bench$(EXE) -router $(ROUTER) -dir $(HASH_DIR)/w$(HASH_WEIGHT) -policy prefix_hash $(HASH_LABEL) $(FLEET_KV_EVENTS_LABEL) \
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
# Whether the fleet publishes its KV cache events, read once from versions.env —
# the file ops/replica.sh reads it from when it starts the engines — and recorded
# on every cell. It is an engine setting (ADR-0010): a sweep refuses to resume
# cells recorded under the other value, and compare refuses to put the two in one
# table.
FLEET_KV_EVENTS := $(shell ops/fleet.sh env KV_EVENTS 2>/dev/null)
FLEET_KV_EVENTS_LABEL = -fleet-kv-events=$(if $(filter 1,$(FLEET_KV_EVENTS)),true,false)

# An events-on fleet's grid is swept into a directory of its own. #18's grid in
# runs/pressure ran without the events and stays that way — the events-off
# reference — so #24's re-measurement of the other policies never resumes into it
# (which the sweep would refuse anyway) and is never merged with it.
PRESSURE_DIR ?= runs/pressure$(if $(filter 1,$(FLEET_KV_EVENTS)),-kv-events,)
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
# Only prefix affinity and exact residency have a spill rule — the same rule, at
# the same thresholds, since #24 compares the two to find what exact knowledge of
# the caches is worth and nothing else — so only they are labelled with one. The
# other three policies have no valve and must not be labelled as if they did.
PRESSURE_SPILL ?= 0/2
PRESSURE_SPILL_LABEL = $(if $(filter prefix_affinity exact_residency,$(POLICY)),-spill $(PRESSURE_SPILL),)

# And the same for the one policy with a hash point rather than a spill rule.
# PRESSURE_HASH is the point the grid arm runs at, which is the weight the weight
# sweep settled on: the grid compares policies at one setting each, and the axis
# is swept separately by `make hash-weight`.
PRESSURE_HASH ?= 16/4
PRESSURE_HASH_LABEL = $(if $(filter prefix_hash,$(POLICY)),-hash $(PRESSURE_HASH),)

.PHONY: pressure-grid
pressure-grid: build ## Run one point of the pressure grid: PRESSURE_WS x PRESSURE_SKEW at one concurrency
	@test -n "$(PRESSURE_WS)" || { echo "pressure-grid: set PRESSURE_WS to a point of bench.PressureWorkingSets (0.25, 1, 3 or 8)" >&2; exit 1; }
	@test -n "$(PRESSURE_SKEW)" || { echo "pressure-grid: set PRESSURE_SKEW to a point of bench.PressureSkews (0, 1 or 1.4)" >&2; exit 1; }
	@test -n "$(KV_CAPACITY)" || { echo "pressure-grid: KV_CAPACITY is required, or the cells state no working set and the map has no axis" >&2; exit 1; }
	$(BIN)/bench$(EXE) -router $(ROUTER) -dir $(PRESSURE_DIR)/ws$(PRESSURE_WS)-skew$(PRESSURE_SKEW) \
		-policy $(POLICY) $(PRESSURE_SPILL_LABEL) $(PRESSURE_HASH_LABEL) $(FLEET_KV_EVENTS_LABEL) \
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
# The headline pair. Unset draws session affinity against prefix affinity, which
# is #18's map; #24's is the exact policy against the approximate one, drawn over
# the events-on grid:
#
#     make pressure-map PRESSURE_MAP_ARGS="-baseline prefix_affinity -challenger exact_residency"
#
# Either way, a grid holding session affinity, prefix affinity and exact
# residency also reports how much of the gain the approximate index keeps.
PRESSURE_MAP_ARGS ?=

.PHONY: pressure-map
pressure-map: build ## Draw the pressure map across every point of the grid that has been run
	@test -n "$(PRESSURE_MAP_DIRS)" || { echo "pressure-map: no grid points under $(PRESSURE_DIR); run pressure-grid first" >&2; exit 1; }
	$(BIN)/pressuremap$(EXE) -out $(PRESSURE_MAP_OUT) $(PRESSURE_MAP_ARGS) $(PRESSURE_MAP_DIRS)

# The regime map: a view over the same grid, naming the winner at each point
# rather than one baseline's delta. Reads the same directories as pressure-map
# and nothing else, so it needs the same guard.
REGIME_MAP_OUT ?= runs/regimemap.md

.PHONY: regime-map
regime-map: build ## Name the winning policy at each point of the grid that has been run
	@test -n "$(PRESSURE_MAP_DIRS)" || { echo "regime-map: no grid points under $(PRESSURE_DIR); run pressure-grid first" >&2; exit 1; }
	$(BIN)/regimemap$(EXE) -out $(REGIME_MAP_OUT) $(PRESSURE_MAP_DIRS)

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
	$(BIN)/chaos$(EXE) -router $(ROUTER) -dir $(CHAOS_DIR)/$(CHAOS_FAULT)-$(POLICY) -policy $(POLICY) \
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
	$(BIN)/recovery$(EXE) -out runs/recovery-$(CHAOS_FAULT).md $(wildcard $(CHAOS_DIR)/$(CHAOS_FAULT)-*)

# idea.md §8's arithmetic (#22): what moving a request's KV from one card of
# this host to another costs, next to the prefill before it. Like compare it
# reads records only, so a checkout is enough; the records are what
# ops/pcie/measure.sh leaves on the box, which has to have all six cards free
# while it runs.
DISAGG_DIR ?= docs/measurements/2026-09-11-pcie-arithmetic

.PHONY: disagg
disagg: build ## Set a request's KV transfer against its prefill, from DISAGG_DIR's measured bandwidth and prefill
	@test -d "$(DISAGG_DIR)" || { echo "disagg: no measurement in $(DISAGG_DIR); run ops/pcie/measure.sh on the box first" >&2; exit 1; }
	$(BIN)/disagg$(EXE) -out $(DISAGG_DIR)/report.md $(DISAGG_DIR)

.PHONY: bench
bench: build ## Sweep concurrency against the running fleet, resuming from RUN_DIR
	$(BIN)/bench$(EXE) -router $(ROUTER) -dir $(RUN_DIR) -policy $(POLICY) $(SPILL_LABEL) $(FLEET_KV_EVENTS_LABEL) \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpu-indexes "$$(ops/fleet.sh env REPLICA_GPUS)" \
		-replicas "$$(ops/fleet.sh replicas)" \
		-repetitions $(REPS) \
		$(WORKLOAD_ARGS) \
		$(if $(SLO_FROM),-slo-from $(SLO_FROM),) \
		$(BENCH_ARGS)

.PHONY: goodput
goodput: build ## Offer a ladder of arrival rates open-loop and record goodput at each, resuming from GOODPUT_DIR
	$(BIN)/bench$(EXE) -router $(ROUTER) -dir $(GOODPUT_DIR) -policy $(POLICY) -driver open_loop $(SPILL_LABEL) $(FLEET_KV_EVENTS_LABEL) \
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
	$(BIN)/compare$(EXE) -out $(COMPARE_OUT) $(COMPARE_DIRS)

.PHONY: characterize
characterize: build ## Measure KV capacity, the latency floor, the SLO and replica symmetry
	$(BIN)/characterize$(EXE) -dir $(CHAR_DIR) \
		-model "$$(ops/fleet.sh env MODEL)" \
		-gpu-indexes "$$(ops/fleet.sh env REPLICA_GPUS)" \
		-replicas "$$(ops/fleet.sh replicas)" \
		$(CHAR_ARGS)

.PHONY: contention
contention: build ## Drive all six replicas at once and compare NUMA nodes
	$(BIN)/characterize$(EXE) -dir $(CONTENTION_DIR) -schedule together \
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
	$(BIN)/calibrate$(EXE) -replicas "$$(ops/fleet.sh replicas)" \
		-from $(CALIBRATE_FROM) \
		$(if $(wildcard $(DIVERGENCE_JSON)),-divergence $(DIVERGENCE_JSON),) \
		-out $(PREFIX_CALIBRATION)

.PHONY: divergence
divergence: build ## Measure how far the router's index was from what the engines held
	$(BIN)/divergence$(EXE) -out $(DIVERGENCE_OUT) -calibration-out $(DIVERGENCE_JSON) $(DIVERGENCE_DIRS)

# Re-judging recorded cells from their recorded rows. RESCORE_DIRS defaults to
# every open-loop run this repository holds the rows for; name others to check a
# run whose rows are still on the box.
#
# The target writes nothing back into the cell records, so it is safe to run over
# anything: a recorded cell says what was concluded when it ran, and the report
# is the account of what the current check would say instead. Writing the
# verdict back is `bin/rescore -write`, run by hand once that report is published.
RESCORE_DIRS ?= docs/measurements/2026-09-08-policy-comparison/goodput docs/measurements/2026-09-08-three-policy-multiturn/goodput
RESCORE_OUT  ?= runs/rescore.md

.PHONY: rescore
rescore: build ## Re-judge recorded cells from their own rows and report which verdicts move
	@mkdir -p $(dir $(RESCORE_OUT))
	$(BIN)/rescore$(EXE) -out $(RESCORE_OUT) $(RESCORE_DIRS)

# Where the residency grid's levels come from, and the check that the spill
# rule's two branches read two signals rather than one in two units.
#
# SPILL_SIGNAL_ROWS names the router rows to read: the observing pass at WS 3,
# skew 0 with the condition off. Start the router for that pass with
# -scrape-replicas, so all three signals land on the same rows and their
# correlations are measured against the same decisions. The router turns the
# scrape on by itself once a residency threshold is set, but the observing pass
# runs the rule off, so it has to ask.
SPILL_SIGNAL_ROWS ?= $(TUNABLES_DIR)/residency/router.jsonl
SPILL_SIGNAL_OUT  ?= $(TUNABLES_DIR)/residency/spill-signal.md

.PHONY: spill-signal
spill-signal: build ## Report what the spill rule's two branches were reading, and the range its grid is cut from
	$(BIN)/spillsignal$(EXE) -out $(SPILL_SIGNAL_OUT) $(SPILL_SIGNAL_ROWS)

# What the load condition was comparing against, and which points could fire at
# the rung a run was made at (#31).
#
# LOAD_COMPARISON_ROWS names the router rows to read: the open-loop observing
# pass, spill off, at LoadDenominatorRate. The rule has to be off for the same
# reason it is off for the residency pass — a condition that is declining matches
# is changing the fleet whose load is being observed.
LOAD_COMPARISON_ROWS ?= $(GOODPUT_DIR)/router.jsonl
LOAD_COMPARISON_OUT  ?= $(GOODPUT_DIR)/load-comparison.md

.PHONY: load-comparison
load-comparison: build ## Report what the spill rule's load condition compared against, and which points reach this rung
	$(BIN)/loadcomparison$(EXE) -out $(LOAD_COMPARISON_OUT) $(LOAD_COMPARISON_ROWS)

# Every published figure, regenerated from the committed measurements with one
# command — no fleet and no GPU, only a checkout, Go and uv. The Go commands
# compute every number and write it as figure data; analysis/figures.py draws from
# that and does no arithmetic of its own, so a figure cannot disagree with the
# table printed beside it. uv installs the script's pinned matplotlib on first
# use, which needs the network once; the pin is exact because the same data must
# draw the same bytes on every machine.
#
# The inputs are reference runs under docs/measurements.
#
# FIGURES_RECOVERY is #19's kill scenario, and the pair named here is deliberate:
# session affinity against prefix affinity with the spill rule OFF. The spill-on
# arm ran too, and at this load the rule fired on 19% of later turns and dropped
# the policy's own baseline to 5.33/s against 5.98/s — that curve measures the
# rule rather than the fault (issue #31), and the measurement's README says so.
#
# Router overhead is drawn twice because no single run carries every policy. The
# 2026-09-08 run is the one the comparison figures come from and covers the three
# policies swept there; the chaos runs are where an affinity pair ran the same
# scenario, and are the only committed rows for prefix affinity. They are separate
# figures rather than one, because a p50 from a 32-user sweep and one from a 6
# req/s open-loop run through a replica failure are not the same measurement.
FIGURES_DIR ?= docs/figures
FIGURES_PRESSURE ?= $(wildcard docs/measurements/2026-09-11-pressure-grid/grid/ws*-skew*)
FIGURES_COMPARE ?= docs/measurements/2026-09-08-three-policy-multiturn/concurrency docs/measurements/2026-09-08-three-policy-multiturn/goodput
FIGURES_ROUTER_ROWS ?= $(wildcard docs/measurements/2026-09-08-three-policy-multiturn/router-*.jsonl.gz)
FIGURES_CHAOS_ROUTER_ROWS ?= docs/measurements/2026-09-11-chaos-recovery/evidence/router-session_affinity.jsonl docs/measurements/2026-09-11-chaos-recovery/evidence/router-prefix_affinity-spilloff.jsonl
FIGURES_RECOVERY ?= docs/measurements/2026-09-11-chaos-recovery/kill-session_affinity docs/measurements/2026-09-11-chaos-recovery/kill-prefix_affinity-spilloff
FIGURES_PYTHON ?= 3.12

# Workstation only, and the one target that stays that way: it compiles the four
# commands it needs for this machine rather than naming $(EXE), because a figure
# is drawn from committed records on a checkout and never on the box.
.PHONY: figures
figures: ## Regenerate every published figure from the committed measurements, into FIGURES_DIR
	@command -v uv >/dev/null || { echo "figures: needs uv, which runs analysis/figures.py with its own pinned matplotlib" >&2; exit 1; }
	$(GO) build -o $(BIN)/ ./cmd/pressuremap ./cmd/compare ./cmd/overhead ./cmd/recovery ./cmd/regimemap
	rm -f $(FIGURES_DIR)/data/*.json $(FIGURES_DIR)/*.svg
	$(BIN)/pressuremap -data $(FIGURES_DIR)/data/pressuremap.json $(FIGURES_PRESSURE) >/dev/null
	$(BIN)/regimemap -data $(FIGURES_DIR)/data/regimemap.json $(FIGURES_PRESSURE) >/dev/null
	$(BIN)/regimemap -load-axis -data $(FIGURES_DIR)/data/regimemap-load.json $(FIGURES_COMPARE) >/dev/null
	$(BIN)/compare -data $(FIGURES_DIR)/data/comparison.json $(FIGURES_COMPARE) >/dev/null
	$(BIN)/overhead -data $(FIGURES_DIR)/data/overhead.json $(FIGURES_ROUTER_ROWS) >/dev/null
	$(if $(FIGURES_CHAOS_ROUTER_ROWS),$(BIN)/overhead -data $(FIGURES_DIR)/data/overhead-chaos.json $(FIGURES_CHAOS_ROUTER_ROWS) >/dev/null,)
	$(if $(FIGURES_RECOVERY),$(BIN)/recovery -data $(FIGURES_DIR)/data/recovery-kill.json $(FIGURES_RECOVERY) >/dev/null,@echo "figures: FIGURES_RECOVERY is empty, so no recovery graph" >&2)
	uv run --python $(FIGURES_PYTHON) analysis/figures.py $(FIGURES_DIR)/data $(FIGURES_DIR)

# The script's tests. The matplotlib pin is the one in the script's own header.
.PHONY: figures-test
figures-test: ## Test the plotting script
	uv run --no-project --python $(FIGURES_PYTHON) --with matplotlib==3.11.2 --with pytest pytest analysis -q -p no:cacheprovider

.PHONY: run-router
run-router: build ## Run the router against REPLICAS, keeping its own rows in RECORDS
	@mkdir -p $(dir $(RECORDS))
	$(BIN)/router$(EXE) -listen $(LISTEN) -replicas $(REPLICAS) -policy $(POLICY) -records $(RECORDS) \
		$(if $(wildcard $(PREFIX_CALIBRATION)),-prefix-calibration $(PREFIX_CALIBRATION),) \
		$(if $(filter exact_residency,$(POLICY)),$(KV_EVENTS_ARGS),) \
		$(SPILL_ARGS) $(HASH_ARGS) $(SCRAPE_ARGS)

# exact_residency follows every engine's KV cache events, so it needs a fleet
# brought up with KV_EVENTS=1 (ops/versions.env) and the endpoints that fleet
# publishes on. Read off versions.env through fleet.sh rather than retyped, like
# every other engine setting: the block size especially, since a router chunking
# at a size the engines do not use would refuse every event they send.
KV_EVENTS_ARGS = -kv-events "$$(ops/fleet.sh kv-events)" \
	-kv-events-replay "$$(ops/fleet.sh kv-events-replay)" \
	-kv-block-size "$$(ops/fleet.sh env BLOCK_SIZE)"

.PHONY: run-fake
run-fake: build ## Run one fake replica on :8000, for driving the router without a GPU
	$(BIN)/fakereplica$(EXE) -listen :8000 -id replica-0 -ttft 200ms -inter-token 20ms -output-tokens 64

.PHONY: clean
clean: ## Remove build output
	rm -rf $(BIN)
