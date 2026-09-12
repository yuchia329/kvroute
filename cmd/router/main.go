// Command router is the sole ingress to the vLLM fleet.
//
//	router -listen :8080 \
//	       -replicas replica-0=http://127.0.0.1:8000,replica-1=http://127.0.0.1:8001 \
//	       -policy round_robin \
//	       -records runs/cell.jsonl
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/yuchia329/kvroute/internal/belief"
	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/httpserve"
	"github.com/yuchia329/kvroute/internal/kvevents"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/prefix"
	"github.com/yuchia329/kvroute/internal/record"
	"github.com/yuchia329/kvroute/internal/residency"
	"github.com/yuchia329/kvroute/internal/router"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "router: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		listen       = flag.String("listen", ":8080", "address to listen on")
		replicaSpecs = flag.String("replicas", "", "comma-separated replica specs, id=url or bare url")
		policyName   = flag.String("policy", policy.RoundRobinName,
			"routing policy to run: "+strings.Join(policy.Order, ", ")+". Only the policy varies between benchmark runs")
		calibration = flag.String("prefix-calibration", "",
			"path to the prefix index's calibration, as written by cmd/calibrate. Required by "+policy.PrefixAffinityName+
				", which will not run on an index whose bounds were guessed")
		honouredLowWater = flag.Float64("honoured-low-water", 0,
			"share of its recently claimed tokens a replica has to be honouring for "+policy.PrefixAffinityName+" and "+policy.ExactResidencyName+" to keep the best prefix match there; below it the match is declined and the request routed on load. "+
				"A replica that has stopped honouring the index's beliefs is a replica that is evicting them (ADR-0011). "+
				"0 disables the condition, which is the policy as it was measured before the spill rule existed")
		honouredWindow = flag.Int("honoured-window", belief.DefaultWindow.Requests,
			"how many of each replica's most recent scoring requests its honoured rate is taken over")
		honouredTTL = flag.Duration("honoured-ttl", belief.DefaultWindow.TTL,
			"how old a scoring request may be and still count towards a replica's honoured rate. "+
				"It is what lets a replica the rule has spilled away from be believed again: it stops being sent matches, so nothing refreshes its rate, and the evidence has to age out on the clock")
		honouredQuorum = flag.Int("honoured-quorum", belief.DefaultWindow.Quorum,
			"the fewest scoring requests a replica's honoured rate may rest on. Below it the rate is unread, and an unread rate declines nothing")
		loadImbalanceFactor = flag.Float64("load-imbalance-factor", 0,
			"multiple of the fleet's minimum inflight above which "+policy.PrefixAffinityName+" and "+policy.ExactResidencyName+" decline the best prefix match and route on load. "+
				"0 disables the condition")
		hashLeadingBlocks = flag.Int("hash-leading-blocks", 0,
			"how many of a prompt's leading "+strconv.Itoa(prefix.BlockBytes)+"-byte prefix blocks "+policy.PrefixHashName+" hashes on. "+
				"Required by that policy and not defaulted: OpenAI's documented router hashes \"the initial tokens\" and has never published a number, so every run states its own")
		hashWeight = flag.Float64("hash-weight", 0,
			"what one step down "+policy.PrefixHashName+"'s ranking of the replicas is worth, in inflight requests. "+
				"0 routes on load alone and a weight above any imbalance the fleet can show routes on the hash alone; the axis between them is the policy")
		kvEvents = flag.String("kv-events", "",
			"comma-separated id=tcp://host:port specs naming each replica's KV cache event publisher, as `ops/fleet.sh kv-events` prints them. "+
				"Required by "+policy.ExactResidencyName+", which routes on what the engines report holding")
		kvEventsReplay = flag.String("kv-events-replay", "",
			"comma-separated id=tcp://host:port specs naming each replica's event replay socket, as `ops/fleet.sh kv-events-replay` prints them. "+
				"Without one, a gap in a replica's stream cannot be filled and that replica's residency is forgotten instead")
		kvBlockSize = flag.Int("kv-block-size", 0,
			"the engines' KV cache block size in tokens, which "+policy.ExactResidencyName+" chunks prompts at: ops/versions.env's BLOCK_SIZE. "+
				"Required by that policy and not defaulted, because a size that disagreed with the engines' would refuse every event they sent")
		kvEventsWait = flag.Duration("kv-events-wait", 30*time.Second,
			"how long to wait at startup for every replica's event stream to connect before refusing to start")
		scrapeBatchKV = flag.Bool("scrape-batch-kv", false,
			"scrape vllm:kv_cache_usage_perc onto every row. No policy routes on it — it counts the blocks held by the running batch, which the router already counts itself as inflight (ADR-0011) — so it is off unless a run wants the column, "+
				"and the run that checks the two spill signals against each other is the one that does")
		batchKVScrapeInterval = flag.Duration("batch-kv-scrape-interval", fleet.DefaultBatchKVScrapeInterval,
			"how often each replica's batch KV occupancy is re-read. Only -scrape-batch-kv reads it")
		healthInterval = flag.Duration("health-interval", fleet.DefaultHealthInterval,
			"how often every replica's /health is checked. Two failed checks in a row eject a replica and two passed ones readmit it; a request that cannot reach a replica ejects it at once")
		recordsPath  = flag.String("records", "", "path to append per-request JSONL rows to; empty discards them")
		logLevel     = flag.String("log-level", "info", "log level: debug, info, warn or error. debug logs every routing decision")
		shutdownWait = flag.Duration("shutdown-grace", 30*time.Second, "how long to let in-flight requests finish on shutdown")
	)
	flag.Parse()

	level, err := parseLevel(*logLevel)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	replicas, err := fleet.ParseSpecs(strings.Split(*replicaSpecs, ","))
	if err != nil {
		return err
	}
	// The window the honoured rate is taken over is fixed here, at the fleet,
	// because it is a property of the signal rather than of the rule that reads
	// it: every row records the rate whether or not a threshold consults it, and
	// two runs whose rates were averaged over different windows are not
	// comparable even if neither spilled.
	window := belief.Window{Requests: *honouredWindow, TTL: *honouredTTL, Quorum: *honouredQuorum}
	f, err := fleet.New(replicas, fleet.WithHonouredWindow(window))
	if err != nil {
		return err
	}
	options, err := prefixOptions(log, *policyName, *calibration)
	if err != nil {
		return err
	}
	options.Spill = policy.Spill{HonouredLowWater: *honouredLowWater, LoadImbalanceFactor: *loadImbalanceFactor}
	options.HashPoint = policy.HashPoint{LeadingBlocks: *hashLeadingBlocks, HashWeight: *hashWeight}

	// Exact residency's index is fed by every engine's event stream, and it is
	// started — and connected — before the policy routes anything on it.
	var feed *residency.Feed
	if *policyName == policy.ExactResidencyName {
		var index *residency.Index
		index, feed, err = residencyFeed(log, replicas, *kvEvents, *kvEventsReplay, *kvBlockSize)
		if err != nil {
			return err
		}
		options.ResidencyIndex = index
		// The router's own client, for the reason it dispatches with it: it
		// closes idle connections before vLLM does, so a tokenization is never
		// written onto a keep-alive connection the engine is closing.
		options.Tokenizer = &residency.EngineTokenizer{Client: router.DefaultClient()}

		feedCtx, stopFeed := context.WithCancel(context.Background())
		defer stopFeed()
		go feed.Run(feedCtx)
		readyCtx, cancel := context.WithTimeout(feedCtx, *kvEventsWait)
		err = feed.Ready(readyCtx)
		cancel()
		if err != nil {
			return err
		}
		// The tokenize timeout is logged beside the stream settings because it
		// is chosen rather than measured, and it decides how many requests are
		// routed untokenized: a run's own output has to say what it was.
		log.Info("following KV cache events", "replicas", len(replicas),
			"replay", *kvEventsReplay != "", "block_size", *kvBlockSize,
			"tokenize_timeout", residency.DefaultTokenizeTimeout)
	} else if *kvEvents != "" || *kvEventsReplay != "" || *kvBlockSize != 0 {
		// Refused for the reason a spill threshold is: a flag nothing reads is a
		// run labelled with a configuration it did not have.
		return fmt.Errorf("-kv-events, -kv-events-replay and -kv-block-size feed %s, and %s routes on none of them", policy.ExactResidencyName, *policyName)
	}

	chosen, err := policy.ByName(*policyName, options)
	if err != nil {
		return err
	}
	// A threshold handed to a policy that has no spill rule is refused rather
	// than ignored. The flags are how a grid point is set, and a run that
	// silently dropped them would produce cells labelled with thresholds nothing
	// applied — the same failure the harness's policy check exists to prevent,
	// one layer earlier. Asked of the policy itself, which is the one party that
	// knows whether it has a rule to tune.
	if _, tuned := chosen.(policy.Tuned); options.Spill.Enabled() && !tuned {
		return fmt.Errorf("-honoured-low-water and -load-imbalance-factor configure the spill rule, which %s does not have: it would ignore them and its cells would be labelled with thresholds nothing applied", *policyName)
	}
	// And the same for the hash point, one policy over. A weight set on a policy
	// that hashes nothing is a run whose cells would name a grid point no
	// decision was made at.
	if _, tuned := chosen.(policy.HashTuned); (options.HashPoint.Stated() || options.HashPoint.HashWeight != 0) && !tuned {
		return fmt.Errorf("-hash-leading-blocks and -hash-weight configure %s, and %s hashes nothing: it would ignore them and its cells would be labelled with a grid point nothing applied", policy.PrefixHashName, *policyName)
	}
	records, err := record.Open[record.Request](*recordsPath)
	if err != nil {
		return err
	}
	defer records.Close()

	// Health checks run under every policy alike. A replica that has died has to
	// leave rotation the same way whichever policy is routing, or two policies
	// would differ in how they meet a failure rather than in how they route.
	//
	// There is no way to switch them off. The request that finds a replica dead
	// ejects it whether or not anything is checking, and only these checks ever
	// put an ejected replica back: without them, one refused connection would
	// take a replica out for the life of the router.
	if *healthInterval <= 0 {
		return fmt.Errorf("-health-interval must be positive: the health checks are the only thing that readmits an ejected replica")
	}
	checkCtx, stopChecking := context.WithCancel(context.Background())
	defer stopChecking()
	go fleet.CheckHealth(checkCtx, fleet.HealthConfig{Fleet: f, Interval: *healthInterval, Log: log})
	log.Info("checking replica health", "interval", *healthInterval,
		"eject_after", fleet.EjectAfter, "readmit_after", fleet.ReadmitAfter)

	// The batch gauge is scraped only when a run asks for it. No policy routes on
	// it, and a fleet polled for a gauge nothing decides on is six extra HTTP
	// round trips per interval against the same engines the measurement comes
	// off. It is worth that cost for exactly one thing — reading it beside the
	// inflight on the same row, which is what ADR-0011's correlation is derived
	// from and what any later re-derivation of it needs — so the run that checks
	// the spill rule's signals turns it on and the rest do not.
	if *scrapeBatchKV {
		ctx, stopScraping := context.WithCancel(context.Background())
		defer stopScraping()
		go fleet.ScrapeBatchOccupancy(ctx, fleet.BatchKVScrapeConfig{
			Fleet:    f,
			Interval: *batchKVScrapeInterval,
			Log:      log,
		})
		log.Info("scraping batch KV occupancy", "interval", *batchKVScrapeInterval, "replicas", len(replicas))
	}

	rt, err := router.New(router.Config{
		Fleet:     f,
		Policy:    chosen,
		Records:   records,
		Logger:    log,
		Residency: feed,
	})
	if err != nil {
		return err
	}

	// The spill thresholds are logged with the policy because they are part of
	// what a cell measured: two runs of prefix_affinity at different grid points
	// are two different policies as far as the results table is concerned, and a
	// run whose thresholds live only in somebody's shell history cannot be read
	// back.
	log.Info("router listening",
		"addr", *listen,
		"policy", chosen.Name(),
		"spill", options.Spill,
		"honoured_window", window,
		"hash", options.HashPoint,
		"replicas", len(replicas),
		"records", *recordsPath,
	)
	serveErr := httpserve.Run(&http.Server{Addr: *listen, Handler: rt.Handler()}, *shutdownWait, log)

	// Report the router's own cost on the way out, so it is visible even when
	// nobody scraped /router/stats during the run.
	s := rt.Stats()
	log.Info("router overhead",
		"requests", s.Requests,
		"p50_us", s.RouterOverhead.P50Us,
		"p99_us", s.RouterOverhead.P99Us,
		"max_us", s.RouterOverhead.MaxUs,
	)
	// And how complete exact residency's view of each cache was, for the same
	// reason: a run whose streams lost history ran on an index that knew less
	// than it claimed, and its own log should say so.
	for _, r := range s.Residency {
		log.Info("residency",
			"replica", r.Replica,
			"blocks", r.Blocks,
			"orphaned", r.Orphaned,
			"refused", r.Refused,
			"applied", r.Stream.Applied,
			"replayed", r.Stream.Replayed,
			"lost", r.Stream.Lost,
			"resets", r.Stream.Resets,
			"connections", r.Stream.Connections,
		)
	}
	return serveErr
}

// residencyFeed builds the index exact residency routes on, and the feed that
// keeps it current from every replica's KV cache event stream.
//
// Every replica needs a publisher, and every publisher a replica: the feed
// refuses either mismatch, because a replica with no stream would never hold
// anything and would never be routed to on a match, silently. The replay specs
// are optional per replica.
func residencyFeed(log *slog.Logger, replicas []fleet.Replica, eventSpecs, replaySpecs string, blockSize int) (*residency.Index, *residency.Feed, error) {
	if eventSpecs == "" {
		return nil, nil, fmt.Errorf("-kv-events is required to run %s: it routes on what the engines report holding, and that report arrives on their event publishers (ops/fleet.sh kv-events)", policy.ExactResidencyName)
	}
	if blockSize <= 0 {
		return nil, nil, fmt.Errorf("-kv-block-size is required to run %s: it is the engines' block size, ops/versions.env's BLOCK_SIZE", policy.ExactResidencyName)
	}
	publishers, err := endpoints(eventSpecs)
	if err != nil {
		return nil, nil, fmt.Errorf("-kv-events: %w", err)
	}
	replays := map[string]string{}
	if replaySpecs != "" {
		if replays, err = endpoints(replaySpecs); err != nil {
			return nil, nil, fmt.Errorf("-kv-events-replay: %w", err)
		}
	}
	ids := make([]string, 0, len(replicas))
	for _, r := range replicas {
		ids = append(ids, r.ID)
	}
	index, err := residency.New(blockSize, ids)
	if err != nil {
		return nil, nil, err
	}
	transports := make(map[string]kvevents.Transport, len(publishers))
	for id, endpoint := range publishers {
		transports[id] = &kvevents.ZMQ{Endpoint: endpoint, ReplayEndpoint: replays[id]}
	}
	for id := range replays {
		if _, published := publishers[id]; !published {
			return nil, nil, fmt.Errorf("-kv-events-replay names %s, which -kv-events does not", id)
		}
	}
	feed, err := residency.NewFeed(index, transports, log)
	if err != nil {
		return nil, nil, err
	}
	return index, feed, nil
}

// endpoints parses id=endpoint specs, the form -replicas takes.
func endpoints(specs string) (map[string]string, error) {
	parsed, err := fleet.ParseSpecs(strings.Split(specs, ","))
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(parsed))
	for _, p := range parsed {
		if _, dup := out[p.ID]; dup {
			return nil, fmt.Errorf("%s named twice", p.ID)
		}
		out[p.ID] = p.BaseURL
	}
	return out, nil
}

// prefixOptions builds the prefix index the chosen policy needs, from a
// calibration measured off the fleet.
//
// Only prefix affinity reads it, and it is loaded only for that policy: a
// round-robin router should not fail to start because a file it will never
// consult is missing. The refusal when it *is* missing is the point of the flag
// — an index whose node cap and TTL were guessed would put an arbitrary constant
// at the centre of the policy this project's claim rests on, and nothing in the
// resulting cells would show that it had.
func prefixOptions(log *slog.Logger, policyName, calibrationPath string) (policy.Options, error) {
	if policyName != policy.PrefixAffinityName {
		return policy.Options{}, nil
	}
	if calibrationPath == "" {
		return policy.Options{}, fmt.Errorf("-prefix-calibration is required to run %s: the index's node cap and TTL are measured off the fleet rather than defaulted, and cmd/calibrate writes the file", policy.PrefixAffinityName)
	}
	measured, err := prefix.Load(calibrationPath)
	if err != nil {
		return policy.Options{}, err
	}
	cfg, err := measured.Config()
	if err != nil {
		return policy.Options{}, err
	}
	index, err := prefix.New(cfg)
	if err != nil {
		return policy.Options{}, err
	}
	// Logged so the bounds the router actually ran with are in the run's own
	// output, next to the measurements they were derived from. A calibration
	// that only exists in a file beside the binary is one nobody reads back.
	log.Info("prefix index calibrated",
		"from", calibrationPath,
		"node_cap", cfg.NodeCap,
		"ttl", cfg.TTL,
		"ttl_source", measured.TTLSource(),
		"fleet_tokens", measured.FleetTokens,
		"prompt_bytes_per_token", measured.PromptBytesPerToken,
	)
	return policy.Options{PrefixIndex: index}, nil
}

func parseLevel(name string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(name)); err != nil {
		return 0, fmt.Errorf("unknown log level %q", name)
	}
	return level, nil
}
