package bench

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/yuchia329/kvroute/internal/gpu"
)

// DefaultSampleInterval is how often a cell samples the GPUs. Fast enough to
// catch a job that starts and finishes inside one cell, slow enough that
// shelling out to nvidia-smi is not itself load on the host.
const DefaultSampleInterval = 5 * time.Second

// ContaminationConfig configures the sampling a cell does while it runs.
type ContaminationConfig struct {
	// Prober reads the GPUs. Nil means no sampling, which is a legitimate state
	// — the harness runs against fakes on a machine with no GPU — and produces
	// a cell that says so rather than one that claims to be clean.
	Prober *gpu.Prober
	// Interval between samples.
	Interval time.Duration
	// GPUs the fleet uses. Processes on any other card are not this
	// experiment's business.
	GPUs []int
	// OwnPIDs are the replica supervisors, from ops/fleet.sh pids. Their
	// descendants are the fleet; everything else on the fleet's cards is
	// foreign.
	OwnPIDs []int
	// OwnPIDsNow, when set, is asked for the replica supervisors at every sample
	// in place of OwnPIDs. A chaos run restarts the replica it takes away, and
	// the restarted supervisor is a process nobody knew the pid of when sampling
	// began: judged against the pids read at the start, its engine would be a
	// foreign process, and every kill run would be discarded as unclean.
	OwnPIDsNow func() []int
	Log        *slog.Logger
}

// Contamination is one measurement's cleanliness evidence — a cell's, or a
// characterization probe's.
//
// The box is shared, so this is not diagnostics: it is the field that decides
// whether a finished cell is allowed to appear in the results at all.
type Contamination struct {
	// GPUSamples counts successful probes. Zero means the cards were never
	// looked at, which is why Clean is a separate field and not derived from
	// ForeignProcs being empty: "nothing was found" and "nothing was looked
	// for" must not read the same.
	GPUSamples   int      `json:"gpu_samples" parquet:"gpu_samples"`
	ProbeErrors  int      `json:"probe_errors" parquet:"probe_errors"`
	ForeignProcs []string `json:"foreign_procs,omitempty" parquet:"foreign_procs"`
	// MaxForeignGPUMemMiB is the most memory foreign processes held across the
	// fleet's cards at any one sample.
	MaxForeignGPUMemMiB int  `json:"max_foreign_gpu_mem_mib" parquet:"max_foreign_gpu_mem_mib"`
	Clean               bool `json:"clean" parquet:"clean"`
}

// Watcher samples the GPUs for the duration of one cell.
type Watcher struct {
	cfg  ContaminationConfig
	stop context.CancelFunc
	done chan struct{}

	mu       sync.Mutex
	result   Contamination
	procSeen map[int]bool
}

// Watch starts sampling and returns a watcher to stop at the end of the cell.
// A nil Prober produces a watcher that samples nothing and reports a cell whose
// cleanliness is unknown.
func Watch(ctx context.Context, cfg ContaminationConfig) *Watcher {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultSampleInterval
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	w := &Watcher{cfg: cfg, done: make(chan struct{}), procSeen: map[int]bool{}}
	if cfg.Prober == nil {
		close(w.done)
		return w
	}

	sampling, stop := context.WithCancel(context.WithoutCancel(ctx))
	w.stop = stop
	go w.sample(sampling)
	return w
}

// Stop ends sampling and returns what the cell saw.
func (w *Watcher) Stop() Contamination {
	if w.stop != nil {
		w.stop()
	}
	<-w.done

	w.mu.Lock()
	defer w.mu.Unlock()
	result := w.result
	result.ForeignProcs = slices.Clone(result.ForeignProcs)
	// Clean requires evidence: at least one successful probe, no probe that
	// failed, and nothing foreign in any of them.
	result.Clean = result.GPUSamples > 0 && result.ProbeErrors == 0 && len(result.ForeignProcs) == 0
	return result
}

func (w *Watcher) sample(ctx context.Context) {
	defer close(w.done)
	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()

	// Sample immediately: a cell short enough to finish inside one tick would
	// otherwise carry no evidence at all.
	w.once(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.once(ctx)
		}
	}
}

func (w *Watcher) once(ctx context.Context) {
	snapshot, err := w.cfg.Prober.Snapshot(ctx)
	own := w.cfg.OwnPIDs
	if w.cfg.OwnPIDsNow != nil {
		own = w.cfg.OwnPIDsNow()
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		// A probe that failed is not evidence of cleanliness, so it is counted
		// and it disqualifies the cell.
		w.result.ProbeErrors++
		w.cfg.Log.Warn("could not sample the GPUs", "err", err)
		return
	}
	if len(w.cfg.GPUs) > 0 {
		snapshot = snapshot.Limit(w.cfg.GPUs)
	}
	w.result.GPUSamples++

	for _, p := range snapshot.Foreign(own) {
		if !w.procSeen[p.PID] {
			w.procSeen[p.PID] = true
			w.result.ForeignProcs = append(w.result.ForeignProcs, p.String())
		}
	}
	if held := snapshot.ForeignMemoryMiB(own); held > w.result.MaxForeignGPUMemMiB {
		w.result.MaxForeignGPUMemMiB = held
	}
}

// Contaminated reports whether something the fleet did not start was seen on
// its GPUs, or whether looking failed. It is narrower than !Clean: a cell whose
// GPUs were never sampled is not clean either, but there is nothing to discard
// it for and re-running it would never produce a different answer.
func (c Contamination) Contaminated() bool {
	return len(c.ForeignProcs) > 0 || c.ProbeErrors > 0
}

// Reasons is why this evidence disqualifies a measurement from being averaged
// in with the others, or nothing if it does not.
func (c Contamination) Reasons() []string {
	switch {
	case c.Clean:
		return nil
	case c.GPUSamples == 0:
		return []string{"the GPUs were never sampled, so there is no cleanliness evidence"}
	case c.ProbeErrors > 0:
		return []string{fmt.Sprintf("%d GPU probes failed, so cleanliness is unproven", c.ProbeErrors)}
	default:
		return []string{fmt.Sprintf("%d foreign processes on the fleet's GPUs, peak %d MiB: %v",
			len(c.ForeignProcs), c.MaxForeignGPUMemMiB, c.ForeignProcs)}
	}
}
