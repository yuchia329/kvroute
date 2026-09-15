package fleet_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/fleet"
	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// kvOf pulls one replica's reading out of a snapshot.
func kvOf(t *testing.T, state fleet.State, id string) vllmmetrics.BatchOccupancy {
	t.Helper()
	c, present := state.Candidate(id)
	if !present {
		t.Fatalf("no replica %s in the snapshot", id)
	}
	return c.BatchKV
}

func TestAReplicaNobodyHasScrapedYetIsUnreadRatherThanEmpty(t *testing.T) {
	f, err := fleet.New([]fleet.Replica{{ID: "replica-0", BaseURL: "http://127.0.0.1:8000"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := kvOf(t, f.State(), "replica-0"); got.Read {
		t.Errorf("a replica nobody scraped read %+v, want unread", got)
	}
}

func TestAScrapedReadingReachesThePolicysSnapshot(t *testing.T) {
	f, err := fleet.New([]fleet.Replica{{ID: "replica-0", BaseURL: "http://127.0.0.1:8000"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := f.ObserveBatchOccupancy("replica-0", vllmmetrics.BatchOccupancy{Fraction: 0.72, Read: true}); err != nil {
		t.Fatalf("ObserveBatchOccupancy: %v", err)
	}

	got := kvOf(t, f.State(), "replica-0")
	if !got.Read || got.Fraction != 0.72 {
		t.Errorf("snapshot carries %+v, want a read 0.72", got)
	}
}

// A replica the fleet does not have is a mis-wired scraper, not a routing
// problem, so it is an error rather than a silently discarded reading.
func TestAReadingForAReplicaTheFleetDoesNotHaveIsRefused(t *testing.T) {
	f, _ := fleet.New([]fleet.Replica{{ID: "replica-0", BaseURL: "http://127.0.0.1:8000"}})

	if err := f.ObserveBatchOccupancy("replica-9", vllmmetrics.BatchOccupancy{Read: true}); err == nil {
		t.Error("a reading for a replica the fleet does not front was accepted")
	}
}

// The failure that matters: a replica that answered once and then stopped must
// not go on being believed at the reading it last gave. A stale 10% would keep
// pulling declined requests onto a replica whose cache is now full.
func TestAReplicaThatStopsAnsweringGoesBackToUnread(t *testing.T) {
	var up atomic.Bool
	up.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !up.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, "%s{model_name=\"m\"} 0.10\n", vllmmetrics.BatchKVUsage)
	}))
	defer srv.Close()

	f, _ := fleet.New([]fleet.Replica{{ID: "replica-0", BaseURL: srv.URL}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go fleet.ScrapeBatchOccupancy(ctx, fleet.BatchKVScrapeConfig{Fleet: f, Interval: 5 * time.Millisecond})

	waitFor(t, func() bool { return kvOf(t, f.State(), "replica-0").Read })

	up.Store(false)
	waitFor(t, func() bool { return !kvOf(t, f.State(), "replica-0").Read })
}

// One replica falling over must not take the fleet's other readings with it:
// routing carries on for everyone the scraper can still reach.
func TestOneReplicaFailingLeavesTheRestScraped(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s{model_name=\"m\"} 0.44\n", vllmmetrics.BatchKVUsage)
	}))
	defer good.Close()

	f, _ := fleet.New([]fleet.Replica{
		{ID: "replica-0", BaseURL: good.URL},
		{ID: "replica-1", BaseURL: "http://127.0.0.1:1"},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go fleet.ScrapeBatchOccupancy(ctx, fleet.BatchKVScrapeConfig{Fleet: f, Interval: 5 * time.Millisecond, Timeout: 50 * time.Millisecond})

	waitFor(t, func() bool { return kvOf(t, f.State(), "replica-0").Fraction == 0.44 })
	if got := kvOf(t, f.State(), "replica-1"); got.Read {
		t.Errorf("an unreachable replica read %+v, want unread", got)
	}
}

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition was never reached")
}
