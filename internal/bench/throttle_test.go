package bench

import (
	"testing"

	"github.com/yuchia329/kvroute/internal/gpu"
)

// busy builds one sample of one card, as the probe would have read it.
func busy(index, clock int, reasons gpu.ThrottleReasons) gpu.Device {
	return gpu.Device{Index: index, UtilizationPct: 96, SMClockMHz: clock, Throttle: reasons, ClocksRead: true}
}

// TestTheShareIsOverBusySamplesOnly. A fleet is idle for the seconds between
// cells and through part of its warm-up, and an idle card reports GpuIdle and no
// thermal reason at all. Counting those would divide a throttled card's samples
// by however long the fleet sat still and call the result healthy.
func TestTheShareIsOverBusySamplesOnly(t *testing.T) {
	var c throttleCounter
	idle := gpu.Device{Index: 3, UtilizationPct: 0, SMClockMHz: 210, Throttle: gpu.ThrottleGPUIdle, ClocksRead: true}
	for range 90 {
		c.add(idle)
	}
	for range 10 {
		c.add(busy(3, 960, gpu.ThrottleSwPowerCap|gpu.ThrottleSwThermal))
	}

	got := c.result(0)

	if len(got.GPUs) != 1 || got.GPUs[0].Samples != 10 {
		t.Fatalf("counted %+v, want 10 busy samples for GPU 3", got.GPUs)
	}
	if got.MaxThermalShare != 1 {
		t.Errorf("thermal share is %.2f, want 1.00 — the idle samples diluted it", got.MaxThermalShare)
	}
	if !got.Throttled {
		t.Error("a card thermally throttled in every busy sample was not marked throttled")
	}
}

// TestAPowerCappedFleetIsNotAThrottledOne: every card in a loaded fleet is
// power-capped, equally and by design, and a check that called that a defect
// would flag every cell the project ever runs.
func TestAPowerCappedFleetIsNotAThrottledOne(t *testing.T) {
	var c throttleCounter
	for range 20 {
		for _, index := range []int{0, 1, 2, 4, 5} {
			c.add(busy(index, 1305, gpu.ThrottleSwPowerCap))
		}
	}

	got := c.result(0)

	if got.Throttled {
		t.Errorf("a power-capped fleet was marked throttled: %+v", got.GPUs)
	}
	if len(got.Reasons()) != 0 {
		t.Errorf("a power-capped fleet gave reasons to discard the cell: %v", got.Reasons())
	}
	if got.GPUs[0].PowerCapShare != 1 {
		t.Errorf("power cap share is %.2f, want 1.00 — the cap is recorded even though it is normal", got.GPUs[0].PowerCapShare)
	}
}

// TestTheThresholdSeparatesTheHealthyCardsFromTheDefectiveOne. The measured
// numbers from #25: the five healthy cards spent up to 34% of their busy
// samples on a thermal reason, the defective one 67%. The default threshold has
// to fall between them, or it flags every cell or none.
func TestTheThresholdSeparatesTheHealthyCardsFromTheDefectiveOne(t *testing.T) {
	var c throttleCounter
	for i := range 100 {
		// GPU 2, the worst of the healthy cards: thermal in 34 of 100 samples.
		if i < 34 {
			c.add(busy(2, 1290, gpu.ThrottleSwPowerCap|gpu.ThrottleSwThermal))
		} else {
			c.add(busy(2, 1290, gpu.ThrottleSwPowerCap))
		}
		// GPU 3: thermal in 67, and power-capped in the other 35 — less often
		// than it is hot, which is the signature.
		if i < 67 {
			c.add(busy(3, 960, gpu.ThrottleSwThermal))
		} else {
			c.add(busy(3, 1200, gpu.ThrottleSwPowerCap))
		}
	}

	got := c.result(0)

	over := got.Thermal()
	if len(over) != 1 || over[0].GPU != 3 {
		t.Fatalf("flagged %+v, want GPU 3 alone", over)
	}
	if over[0].MinSMClockMHz != 960 {
		t.Errorf("GPU 3's lowest clock is %d MHz, want 960", over[0].MinSMClockMHz)
	}
	if len(got.Reasons()) != 1 {
		t.Fatalf("gave %d reasons to discard the cell, want 1", len(got.Reasons()))
	}
}

// TestTheThresholdIsRecordedBesideTheVerdict, so a cell can be read against what
// it was judged by rather than against whatever the constant says today.
func TestTheThresholdIsRecordedBesideTheVerdict(t *testing.T) {
	var c throttleCounter
	for i := range 10 {
		if i < 4 {
			c.add(busy(3, 960, gpu.ThrottleSwThermal))
		} else {
			c.add(busy(3, 1305, gpu.ThrottleSwPowerCap))
		}
	}

	lenient, strict := c.result(0), c.result(0.3)

	if lenient.Throttled {
		t.Error("40% thermal was flagged at the default threshold of 50%")
	}
	if !strict.Throttled {
		t.Error("40% thermal was not flagged at a threshold of 30%")
	}
	if strict.ThermalShareThreshold != 0.3 {
		t.Errorf("the cell records a threshold of %.2f, want the 0.30 it was judged by", strict.ThermalShareThreshold)
	}
}

// TestACardWhoseClocksWereNeverReadIsNotACardThatWasHealthy. The distinction
// GPUSamples draws for cleanliness: a driver that will not answer is silence,
// not evidence — and it is not a reason to discard the cell either, because
// re-running it would produce the same silence.
func TestACardWhoseClocksWereNeverReadIsNotACardThatWasHealthy(t *testing.T) {
	var c throttleCounter
	for range 10 {
		c.add(gpu.Device{Index: 3, UtilizationPct: 96})
	}

	got := c.result(0)

	if got.ClockSamples != 0 || len(got.GPUs) != 0 {
		t.Errorf("unread clocks were recorded as evidence: %+v", got)
	}
	if got.Throttled || len(got.Reasons()) != 0 {
		t.Errorf("a cell whose clocks nobody read was discarded for it: %v", got.Reasons())
	}
}
