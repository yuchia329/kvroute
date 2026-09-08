// Command router is the sole ingress to the vLLM fleet.
//
//	router -listen :8080 \
//	       -replicas replica-0=http://127.0.0.1:8000,replica-1=http://127.0.0.1:8001 \
//	       -policy round_robin \
//	       -records runs/cell.jsonl
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/httpserve"
	"github.com/yuchia329/kvroute/internal/policy"
	"github.com/yuchia329/kvroute/internal/prefix"
	"github.com/yuchia329/kvroute/internal/record"
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
	f, err := fleet.New(replicas)
	if err != nil {
		return err
	}
	options, err := prefixOptions(log, *policyName, *calibration)
	if err != nil {
		return err
	}
	chosen, err := policy.ByName(*policyName, options)
	if err != nil {
		return err
	}
	records, err := record.Open[record.Request](*recordsPath)
	if err != nil {
		return err
	}
	defer records.Close()

	rt, err := router.New(router.Config{
		Fleet:   f,
		Policy:  chosen,
		Records: records,
		Logger:  log,
	})
	if err != nil {
		return err
	}

	log.Info("router listening",
		"addr", *listen,
		"policy", chosen.Name(),
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
	return serveErr
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
