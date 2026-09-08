package vllmmetrics

import (
	"context"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Distribution is one histogram family as a replica exposes it: the cumulative
// bucket counts, and the sum and count that go with them.
//
// It exists because two of the figures this project derives are distribution
// shapes rather than totals — how long a KV block sits idle before the engine
// evicts it, and how long one lives — and a mean computed from sum over count
// would answer neither. The prefix index's TTL is a claim about the tail: it
// must stop believing at the point the engine has almost certainly already
// evicted, and only a quantile can say where that is.
type Distribution struct {
	// Bounds and Cumulative are the buckets, in ascending bound order, with
	// Cumulative[i] the count of observations at or below Bounds[i]. The
	// trailing +Inf bucket is dropped: its bound carries no information and its
	// count is Count.
	Bounds     []float64 `json:"bounds"`
	Cumulative []float64 `json:"cumulative"`
	Sum        float64   `json:"sum"`
	Count      float64   `json:"count"`
	// Read is false when the family was not exposed at all. The three KV
	// residency families need --kv-cache-metrics, which is off by default, so a
	// scrape of a replica started without it finds nothing rather than zeros —
	// and a calibration derived from an absent histogram would be a guess
	// reported as a measurement. See ADR-0001.
	Read bool `json:"read"`
}

// Observed reports whether this histogram says anything about a distribution. A
// family that is exposed but empty is read and has nothing in it, which is what
// a replica that has evicted no blocks yet publishes.
func (h Distribution) Observed() bool { return h.Read && h.Count > 0 }

// Quantile returns the value at q, interpolated linearly inside the bucket it
// falls in — the same estimator Prometheus itself uses, and subject to the same
// limit: the answer can be no finer than the bucket boundaries the engine chose.
//
// An observation in the trailing +Inf bucket has no upper bound to interpolate
// against, so a quantile that lands there returns the largest finite bound and
// reports false. A TTL derived from a tail that ran off the end of the buckets
// would be a number the exposition cannot support.
func (h Distribution) Quantile(q float64) (float64, bool) {
	if !h.Observed() || q <= 0 || q > 1 || len(h.Bounds) == 0 {
		return 0, false
	}
	rank := q * h.Count
	previousBound, previousCount := 0.0, 0.0
	for i, bound := range h.Bounds {
		count := h.Cumulative[i]
		if count < rank {
			previousBound, previousCount = bound, count
			continue
		}
		if count == previousCount {
			return bound, true
		}
		within := (rank - previousCount) / (count - previousCount)
		return previousBound + within*(bound-previousBound), true
	}
	// The rank sits in the +Inf bucket: more than (1-q) of the observations are
	// past the last finite bound, so the exposition does not locate this
	// quantile at all.
	return h.Bounds[len(h.Bounds)-1], false
}

// ReadHistogramFrom parses one histogram family out of a Prometheus text
// exposition.
//
// Series carrying labels are pooled across them. A replica exposes one series
// set per model, and this project runs one model per replica, so pooling is the
// identity in practice — but summing is the right answer if that ever stops
// being true, and picking the first labelled series would silently report one
// slice of a replica as the whole of it.
func ReadHistogramFrom(body, name string) Distribution {
	buckets := map[float64]float64{}
	h := Distribution{}
	for line := range strings.SplitSeq(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		series, raw, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			continue
		}
		base, labels, _ := strings.Cut(series, "{")
		switch base {
		case name + "_sum":
			h.Sum, h.Read = h.Sum+value, true
		case name + "_count":
			h.Count, h.Read = h.Count+value, true
		case name + "_bucket":
			le, ok := bucketBound(labels)
			if !ok {
				continue
			}
			h.Read = true
			if math.IsInf(le, 1) {
				continue
			}
			buckets[le] += value
		}
	}
	if !h.Read {
		return Distribution{}
	}
	for bound := range buckets {
		h.Bounds = append(h.Bounds, bound)
	}
	sort.Float64s(h.Bounds)
	for _, bound := range h.Bounds {
		h.Cumulative = append(h.Cumulative, buckets[bound])
	}
	return h
}

// bucketBound reads the le label off a bucket series' label set.
func bucketBound(labels string) (float64, bool) {
	end := strings.LastIndex(labels, "}")
	if end < 0 {
		return 0, false
	}
	le, ok := parseLabelSet(labels[:end])["le"]
	if !ok {
		return 0, false
	}
	bound, err := strconv.ParseFloat(le, 64)
	return bound, err == nil
}

// ReadHistogram scrapes one histogram family from a replica's /metrics URL. A
// nil client uses http.DefaultClient.
//
// As with the prefix-cache counters, a scrape that does not answer is not an
// error: it is a reading that says nothing, and the caller reports that rather
// than losing what it did measure.
func ReadHistogram(ctx context.Context, client *http.Client, metricsURL, name string) Distribution {
	body, ok := scrape(ctx, client, metricsURL)
	if !ok {
		return Distribution{}
	}
	return ReadHistogramFrom(body, name)
}

// scrape fetches a replica's metrics text, reporting whether it answered.
func scrape(ctx context.Context, client *http.Client, metricsURL string) (string, bool) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
	if err != nil {
		return "", false
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		return "", false
	}
	return string(body), true
}

// BlockIdleBeforeEvict is the histogram the prefix index's TTL is calibrated
// against: how long a KV block sat unused before the engine evicted it.
//
// It is the right family for that job because it measures the thing the TTL is
// a claim about. The index believes a replica holds a block until the TTL
// expires, and it should stop believing at the point the engine has almost
// certainly already dropped it — which is a quantile of this distribution and
// not of block lifetime, which counts a block's whole life including the time
// it was being reused.
//
// It needs --kv-cache-metrics. See ADR-0001: the flag is set from the first
// cell, because turning it on later would change the engine configuration every
// cell is supposed to share.
const BlockIdleBeforeEvict = "vllm:kv_block_idle_before_evict_seconds"

// PoolDistributions adds several replicas' readings into one fleet-wide
// distribution.
//
// Buckets are summed bound by bound, which is only meaningful because every
// replica in this fleet runs the pinned engine on the same configuration and
// therefore publishes the same boundaries. A replica whose bounds differ would
// be a different engine build in the fleet, so its buckets are not merged into
// the pool: the reading is refused rather than silently added to bounds it does
// not share.
//
// One unread replica makes the whole pool unread, for the reason it does in
// PoolPrefixCache: a fleet-wide tail missing a replica is not a smaller
// measurement of the fleet, it is a measurement of something else.
func PoolDistributions(readings []Distribution) Distribution {
	if len(readings) == 0 {
		return Distribution{}
	}
	pooled := Distribution{Read: true}
	for _, r := range readings {
		if !r.Read {
			return Distribution{}
		}
		if pooled.Bounds == nil {
			pooled.Bounds = append([]float64(nil), r.Bounds...)
			pooled.Cumulative = make([]float64, len(r.Bounds))
		}
		if len(r.Bounds) != len(pooled.Bounds) {
			return Distribution{}
		}
		for i, bound := range r.Bounds {
			if bound != pooled.Bounds[i] {
				return Distribution{}
			}
			pooled.Cumulative[i] += r.Cumulative[i]
		}
		pooled.Sum += r.Sum
		pooled.Count += r.Count
	}
	return pooled
}
