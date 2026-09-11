package roofline

import (
	"regexp"
	"strconv"
)

// Phase is one side of an engine step as vLLM annotates it: the requests
// still prefilling their prompt (context) or the requests producing tokens
// (generation). It carries the per-request sums vLLM computes for exactly this
// purpose, so the step's work can be counted without the engine's internals.
type Phase struct {
	// Requests is how many requests of this phase the step scheduled.
	Requests int64
	// Tokens is the new tokens scheduled across them: a whole prompt or a chunk
	// of one for context, one each for generation.
	Tokens int64
	// SeqLen is the sum of each request's sequence length after this step —
	// every key its attention reads.
	SeqLen int64
	// QQ is the sum over requests of new tokens squared, and QK the sum of new
	// tokens times sequence length. Together they give the causal attention's
	// query-key pairs exactly.
	QQ, QK int64
}

// Step is one engine step, read off the NVTX range vLLM wraps it in when its
// profiler runs with detailed_trace_annotation.
type Step struct {
	Context    Phase
	Generation Phase
}

// The name gpu_worker.py builds, in vLLM 0.28.0:
//
//	execute_<scheduled>_context_<n>(sq<t>sk<k>sqsq<qq>sqsk<qk>)_generation_<n>(sq..sk..sqsq..sqsk..)
var stepName = regexp.MustCompile(`^execute_(\d+)_context_(\d+)\(sq(\d+)sk(\d+)sqsq(\d+)sqsk(\d+)\)_generation_(\d+)\(sq(\d+)sk(\d+)sqsq(\d+)sqsk(\d+)\)$`)

// ParseStep reads an engine step from its NVTX range name. It reports false for
// any other range, so a trace can be read range by range without knowing what
// else was annotated in it.
func ParseStep(name string) (Step, bool) {
	m := stepName.FindStringSubmatch(name)
	if m == nil {
		return Step{}, false
	}
	n := make([]int64, len(m))
	for i := 1; i < len(m); i++ {
		v, err := strconv.ParseInt(m[i], 10, 64)
		if err != nil {
			return Step{}, false
		}
		n[i] = v
	}
	return Step{
		Context:    Phase{Requests: n[2], Tokens: n[3], SeqLen: n[4], QQ: n[5], QK: n[6]},
		Generation: Phase{Requests: n[7], Tokens: n[8], SeqLen: n[9], QQ: n[10], QK: n[11]},
	}, true
}
