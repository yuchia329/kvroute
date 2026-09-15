package bench

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/yuchia329/kvroute/internal/gpu"
)

// DefaultThermalShareThreshold is the share of its busy samples a card may
// spend thermally throttled before the cell it ran under is flagged.
//
// Half, and the number is not arbitrary. In the six-card measurement behind #25
// the five healthy cards spent 0–34% of their busy samples on a thermal reason
// while sitting almost entirely on the normal, equal power cap; the defective
// one spent 67%, more than it spent power-capped. So a card past half its
// samples is one whose *dominant* limit is heat, which is the signature that
// separated GPU 3 from its siblings with margin on both sides. A threshold
// below 34% would flag healthy cards under ordinary load, and a flag that fires
// on every cell stops being read.
const DefaultThermalShareThreshold = 0.5

// GPUThrottle is one card's clock evidence over one measurement.
//
// Shares are over *busy* samples — the card had non-zero utilization — for the
// same reason the original analysis was: an idle card reports GpuIdle and no
// thermal reason at all, so counting the fleet's idle moments would dilute
// every share by however long the warm-up took.
type GPUThrottle struct {
	GPU int `json:"gpu" parquet:"gpu"`
	// Samples is how many busy samples this card contributed, and the
	// denominator of both shares below. Zero means a card that was never seen
	// doing anything, which has no share rather than a share of zero.
	Samples int `json:"samples" parquet:"samples"`
	// ThermalShare is the share of those samples the driver held this card back
	// on a thermal reason, and PowerCapShare the share it held it at the power
	// limit. Both are recorded because the comparison between them is the
	// diagnosis: every loaded card is power-capped, and a card whose thermal
	// share approaches or passes its power share is limited by something its
	// siblings are not.
	ThermalShare  float64 `json:"thermal_share" parquet:"thermal_share"`
	PowerCapShare float64 `json:"power_cap_share" parquet:"power_cap_share"`
	// MinSMClockMHz and MaxSMClockMHz bracket what the card actually ran at
	// while busy. The minimum is the one that matters — a card that dropped to
	// 960 MHz while the fleet held 1305 was serving this cell's requests slower
	// than its siblings for the whole time it was down there.
	MinSMClockMHz int `json:"min_sm_clock_mhz" parquet:"min_sm_clock_mhz"`
	MaxSMClockMHz int `json:"max_sm_clock_mhz" parquet:"max_sm_clock_mhz"`
	// Reasons names every throttle reason seen on this card, in the driver's own
	// words, so a cell can be read against nvidia-smi's output without a
	// bitmask lookup.
	Reasons []string `json:"reasons,omitempty" parquet:"reasons"`
}

// String renders one card for a flag message.
func (g GPUThrottle) String() string {
	return fmt.Sprintf("gpu=%d thermal=%.0f%% of %d busy samples, power-capped=%.0f%%, sm clock %d-%d MHz",
		g.GPU, 100*g.ThermalShare, g.Samples, 100*g.PowerCapShare, g.MinSMClockMHz, g.MaxSMClockMHz)
}

// Throttle is what the fleet's cards said about their own clocks over one
// measurement — a cell's, or a chaos run's.
//
// It is the second half of the evidence a cell carries about the hardware it ran
// on, and it is deliberately not part of Contamination. Cleanliness is about
// foreign processes; this is about a card that is slower than its siblings for a
// reason of its own. A cell can be perfectly clean and thermally throttled at
// the same time, and until this existed only the first of those was recorded —
// which is how a card that cost a sixth of the fleet 16% of its throughput went
// unnoticed for a week (#25).
type Throttle struct {
	// ClockSamples counts the samples in which at least one of the fleet's cards
	// answered with a clock and a reason. Zero means the clocks were never read,
	// which is why Throttled is a separate field and not derived from the shares:
	// "no card was throttled" and "no card was asked" must not read the same.
	ClockSamples int `json:"clock_samples" parquet:"clock_samples"`
	// GPUs is one entry per card that was seen busy, in card order.
	GPUs []GPUThrottle `json:"gpus,omitempty" parquet:"gpus"`
	// MaxThermalShare is the worst card's thermal share, held against
	// ThermalShareThreshold to decide Throttled. Recorded beside the flag so a
	// cell can be read against the threshold it was judged by, exactly as a
	// schedule lag is.
	MaxThermalShare       float64 `json:"max_thermal_share" parquet:"max_thermal_share"`
	ThermalShareThreshold float64 `json:"thermal_share_threshold" parquet:"thermal_share_threshold"`
	// Throttled marks a measurement at least one of whose cards spent more than
	// the threshold share of its busy samples limited by heat.
	Throttled bool `json:"throttled" parquet:"throttled"`
}

// Thermal names the cards over the threshold, worst first.
func (t Throttle) Thermal() []GPUThrottle {
	var over []GPUThrottle
	for _, g := range t.GPUs {
		if g.ThermalShare > t.ThermalShareThreshold {
			over = append(over, g)
		}
	}
	slices.SortFunc(over, func(a, b GPUThrottle) int {
		if a.ThermalShare == b.ThermalShare {
			return a.GPU - b.GPU
		}
		if a.ThermalShare > b.ThermalShare {
			return -1
		}
		return 1
	})
	return over
}

// Reasons is why this evidence disqualifies a measurement from being averaged in
// with the others, or nothing if it does not.
//
// Unread clocks are not one of them. A cell whose cards were never asked is a
// cell with nothing to discard it for — re-running it on a host whose driver
// does not report throttle reasons would produce the same silence — so that
// case is recorded in ClockSamples and left to the reader, the way an unsampled
// cell's cleanliness is.
func (t Throttle) Reasons() []string {
	over := t.Thermal()
	if len(over) == 0 {
		return nil
	}
	named := make([]string, 0, len(over))
	for _, g := range over {
		named = append(named, g.String())
	}
	return []string{fmt.Sprintf(
		"%d of the fleet's GPUs spent more than %.0f%% of their busy samples thermally throttled (%s), "+
			"so this cell measured a fleet with a card slower than its siblings for a reason that is not the workload. "+
			"Re-run it on a fleet whose cards agree, or exclude it",
		len(over), 100*t.ThermalShareThreshold, strings.Join(named, "; "))}
}

// throttleAccumulator collects one card's samples while a measurement runs.
type throttleAccumulator struct {
	samples  int
	thermal  int
	powerCap int
	minClock int
	maxClock int
	reasons  gpu.ThrottleReasons
}

// throttleCounter accumulates every card's samples, keyed by card index.
type throttleCounter struct {
	samples int
	byGPU   map[int]*throttleAccumulator
}

// add folds one sample of one card in.
//
// Only busy samples count, and only cards whose driver answered. An idle card
// is not evidence about what the fleet did under load, and a card the driver
// will not answer for is not evidence at all.
func (c *throttleCounter) add(d gpu.Device) {
	if !d.ClocksRead || d.UtilizationPct <= 0 {
		return
	}
	if c.byGPU == nil {
		c.byGPU = map[int]*throttleAccumulator{}
	}
	a := c.byGPU[d.Index]
	if a == nil {
		a = &throttleAccumulator{minClock: d.SMClockMHz, maxClock: d.SMClockMHz}
		c.byGPU[d.Index] = a
	}
	a.samples++
	if d.Throttle.Thermal() {
		a.thermal++
	}
	if d.Throttle.Has(gpu.ThrottleSwPowerCap) {
		a.powerCap++
	}
	a.minClock = min(a.minClock, d.SMClockMHz)
	a.maxClock = max(a.maxClock, d.SMClockMHz)
	a.reasons |= d.Throttle
}

// result renders what was accumulated, judged against threshold.
func (c *throttleCounter) result(threshold float64) Throttle {
	if threshold <= 0 {
		threshold = DefaultThermalShareThreshold
	}
	t := Throttle{ClockSamples: c.samples, ThermalShareThreshold: threshold}
	for _, index := range slices.Sorted(maps.Keys(c.byGPU)) {
		a := c.byGPU[index]
		share := float64(a.thermal) / float64(a.samples)
		t.GPUs = append(t.GPUs, GPUThrottle{
			GPU:           index,
			Samples:       a.samples,
			ThermalShare:  share,
			PowerCapShare: float64(a.powerCap) / float64(a.samples),
			MinSMClockMHz: a.minClock,
			MaxSMClockMHz: a.maxClock,
			Reasons:       a.reasons.Names(),
		})
		t.MaxThermalShare = max(t.MaxThermalShare, share)
	}
	t.Throttled = t.MaxThermalShare > threshold
	return t
}
