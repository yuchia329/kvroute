package bench_test

import (
	"context"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/gpu/gputest"
)

// A chaos run restarts the replica it kills, and the restarted replica's
// supervisor is a process nobody knew the pid of when sampling began. Told how
// to re-read the fleet's pids, the watcher does so at every sample, and the
// restarted engine is the fleet's own rather than a foreign process that would
// mark every kill run unclean.
func TestAReplicaRestartedDuringAMeasurementIsStillTheFleetsOwn(t *testing.T) {
	w := bench.Watch(context.Background(), bench.ContaminationConfig{
		Prober:   gputest.Fleet(),
		Interval: time.Hour,
		// Only the first replica's supervisor was known when sampling began; the
		// second has been restarted since, under a pid read off its new pid file.
		OwnPIDs:    []int{12100},
		OwnPIDsNow: func() []int { return gputest.OwnFleetPIDs },
	})

	got := w.Stop()

	if len(got.ForeignProcs) != 0 || !got.Clean {
		t.Errorf("a replica restarted mid-measurement was booked as foreign: %+v", got)
	}
}

// Re-reading the pids is still reading the fleet's pids: a process nobody in the
// fleet started is foreign however often the list is refreshed.
func TestRefreshingTheFleetsPIDsStillCatchesAForeignProcess(t *testing.T) {
	w := bench.Watch(context.Background(), bench.ContaminationConfig{
		Prober:     gputest.Contaminated(),
		Interval:   time.Hour,
		OwnPIDsNow: func() []int { return gputest.OwnFleetPIDs },
	})

	if got := w.Stop(); len(got.ForeignProcs) == 0 || got.Clean {
		t.Errorf("another user's process went unseen: %+v", got)
	}
}
