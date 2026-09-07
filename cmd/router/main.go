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
		policyName   = flag.String("policy", policy.RoundRobinName, "routing policy to run")
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
	chosen, err := policy.ByName(*policyName)
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

func parseLevel(name string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(name)); err != nil {
		return 0, fmt.Errorf("unknown log level %q", name)
	}
	return level, nil
}
