package fleet

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultHealthInterval is how often every replica is asked whether it is alive.
//
// Once a second. The checks are not what catches a replica dying under traffic —
// the first request that cannot reach it does that, the moment it happens — so
// their job is the two cases traffic cannot cover: a replica that dies while
// nothing is being sent to it, and a replica coming back, which nothing is sent
// to until it is known to be back. A second is small beside the ~40 s a replica
// takes to restart, and one GET a second against an engine is nothing beside the
// requests it is serving.
const DefaultHealthInterval = time.Second

// EjectAfter is how many health checks in a row a replica must fail before it
// is taken out of rotation.
//
// Two rather than one, because the checks run beside the measurement against
// the engines being measured: an engine busy enough to miss a single check is
// one under load, not one that has gone, and ejecting it would change the fleet
// a cell was measuring because the cell was loading it. A dead replica fails
// every check, so the second costs one interval of detection and nothing else.
const EjectAfter = 2

// ReadmitAfter is how many health checks in a row an ejected replica must pass
// before it is put back. Two, so that a replica still coming up — answering
// once, then not — is not put back into rotation between the two.
const ReadmitAfter = 2

// HealthConfig configures the loop that ejects and readmits replicas on their
// health checks. Fleet is required.
type HealthConfig struct {
	Fleet *Fleet
	// Interval is how often every replica is checked. Zero uses
	// DefaultHealthInterval.
	Interval time.Duration
	// Timeout bounds one check. Zero derives it from Interval, so a replica that
	// has stopped answering cannot hold a round of checks open into the next.
	Timeout time.Duration
	Client  *http.Client
	Log     *slog.Logger
}

func (c HealthConfig) interval() time.Duration {
	if c.Interval <= 0 {
		return DefaultHealthInterval
	}
	return c.Interval
}

func (c HealthConfig) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return c.interval() * 4 / 5
}

// HealthURL is a replica's liveness endpoint, the one vLLM serves and the fake
// replica imitates.
func HealthURL(baseURL string) string {
	return strings.TrimSuffix(baseURL, "/") + "/health"
}

// CheckHealth checks every replica until the context is done, ejecting the ones
// that stop answering and readmitting the ones that start again. It blocks, so
// callers run it in a goroutine.
//
// This is the active half of the fleet's health tracking. The passive half is
// the router's: a request that cannot reach its replica ejects it on the spot,
// and the replica comes back through these checks like any other.
//
// Every replica is checked, including the ones out of rotation. A drained
// replica is checked so that its ejection stays true while it is out, and a
// replica restored by its operator after it died is not put back dead.
func CheckHealth(ctx context.Context, cfg HealthConfig) {
	if cfg.Fleet == nil {
		return
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: cfg.timeout()}
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}

	// streaks is each replica's current run of checks that disagreed with where
	// it is — failures while in rotation, passes while ejected. A check that
	// agrees ends the run, which is what makes the thresholds "in a row".
	streaks := map[string]int{}
	ticker := time.NewTicker(cfg.interval())
	defer ticker.Stop()
	cfg.checkOnce(ctx, streaks)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg.checkOnce(ctx, streaks)
		}
	}
}

// checkOnce checks every replica in parallel, then applies what each check
// found. In parallel for the reason the KV scrape is: a replica that has
// stopped answering would otherwise hold every other replica's check back for
// its whole timeout.
func (c HealthConfig) checkOnce(ctx context.Context, streaks map[string]int) {
	round, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	members := c.Fleet.Members()
	passed := make([]bool, len(members))
	var wg sync.WaitGroup
	for i, m := range members {
		wg.Add(1)
		go func() {
			defer wg.Done()
			passed[i] = c.check(round, m.BaseURL)
		}()
	}
	wg.Wait()
	// A round cut short by shutdown failed every check it made, and none of
	// those failures is the replica's.
	if ctx.Err() != nil {
		return
	}

	for i, m := range members {
		if passed[i] != m.Ejected {
			streaks[m.ID] = 0
			continue
		}
		streaks[m.ID]++
		switch {
		case !m.Ejected && streaks[m.ID] >= EjectAfter:
			if ejected, _ := c.Fleet.Eject(m.ID); ejected {
				c.Log.Warn("ejected a replica that stopped answering its health checks", "replica", m.ID, "failed_checks", streaks[m.ID])
			}
			streaks[m.ID] = 0
		case m.Ejected && streaks[m.ID] >= ReadmitAfter:
			if readmitted, _ := c.Fleet.Readmit(m.ID); readmitted {
				c.Log.Info("readmitted a replica that is answering its health checks again", "replica", m.ID, "passed_checks", streaks[m.ID])
			}
			streaks[m.ID] = 0
		}
	}
}

// check reports whether one replica answered its health check.
func (c HealthConfig) check(ctx context.Context, baseURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, HealthURL(baseURL), nil)
	if err != nil {
		return false
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode == http.StatusOK
}
