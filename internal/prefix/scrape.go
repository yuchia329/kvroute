package prefix

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/yuchia329/kvroute/internal/vllmmetrics"
)

// ScrapeBlockIdle reads the fleet's idle-before-evict distribution off every
// replica and pools it.
//
// Pooled rather than taken from one card, for the same reason aggregate fleet
// KV capacity is summed rather than multiplied up from one replica: the index
// models the whole fleet, and one replica's eviction behaviour under its own
// share of the traffic is not the fleet's.
//
// The reading is empty until the fleet has actually evicted blocks, which means
// this is not something to run against a fleet that has just come up. The fleet
// is brought down between policy passes, so the calibration is measured during a
// run and carried forward in a file rather than re-derived at each bring-up —
// see Load.
func ScrapeBlockIdle(ctx context.Context, client *http.Client, replicaBaseURLs []string) vllmmetrics.Distribution {
	return scrapeResidency(ctx, client, replicaBaseURLs, vllmmetrics.BlockIdleBeforeEvict)
}

// ScrapeBlockLifetime reads the fleet's block-lifetime distribution, which is
// recorded beside the idle tail as the check on it rather than derived from.
func ScrapeBlockLifetime(ctx context.Context, client *http.Client, replicaBaseURLs []string) vllmmetrics.Distribution {
	return scrapeResidency(ctx, client, replicaBaseURLs, vllmmetrics.BlockLifetime)
}

func scrapeResidency(ctx context.Context, client *http.Client, replicaBaseURLs []string, family string) vllmmetrics.Distribution {
	if len(replicaBaseURLs) == 0 {
		return vllmmetrics.Distribution{}
	}
	readings := make([]vllmmetrics.Distribution, 0, len(replicaBaseURLs))
	for _, base := range replicaBaseURLs {
		url := strings.TrimSuffix(base, "/") + "/metrics"
		readings = append(readings, vllmmetrics.ReadHistogram(ctx, client, url, family))
	}
	return vllmmetrics.PoolDistributions(readings)
}

// Save writes a calibration to disk.
//
// It is a file rather than a set of flags because it is evidence. The two bounds
// the router runs with are derived from these three measurements, and a run
// whose calibration lives only in somebody's shell history cannot be defended
// later — which is the whole objection to an arbitrary constant that this type
// exists to answer.
func (c Calibration) Save(path string) error {
	body, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("prefix: encode calibration: %w", err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		return fmt.Errorf("prefix: write calibration: %w", err)
	}
	return nil
}

// Load reads a calibration written by Save, and checks it can produce an index
// before returning it.
//
// Checked here rather than at first use, because the first use is the router
// starting under a policy it cannot run: failing at load means a mis-calibrated
// router refuses to come up, instead of coming up and routing every request cold
// while reporting itself as prefix affinity.
func Load(path string) (Calibration, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Calibration{}, fmt.Errorf("prefix: read calibration: %w", err)
	}
	var c Calibration
	if err := json.Unmarshal(body, &c); err != nil {
		return Calibration{}, fmt.Errorf("prefix: parse calibration %s: %w", path, err)
	}
	if _, err := c.Config(); err != nil {
		return Calibration{}, fmt.Errorf("prefix: calibration %s is not usable: %w", path, err)
	}
	return c, nil
}
