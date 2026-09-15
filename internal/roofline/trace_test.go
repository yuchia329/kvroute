package roofline_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/roofline"
)

// The headers are nsys 2026.1's own, from `nsys stats -f csv -r
// nvtx_gpu_proj_trace,cuda_gpu_trace` on a vLLM 0.28.0 replica.
const (
	rangesHeader = "Name,Projected Start (ns),Projected Duration (ns),Orig Start (ns),Orig Duration (ns),Style,PID,TID,NumGPUOps,Lvl,NumChild,RangeId,ParentId,RangeStack\n"
	opsHeader    = "Start (ns),Duration (ns),CorrId,GrdX,GrdY,GrdZ,BlkX,BlkY,BlkZ,Reg/Trd,StcSMem (MB),DymSMem (MB),Bytes (MB),Throughput (MB/s),SrcMemKd,DstMemKd,Device,Ctx,GreenCtx,Strm,Name\n"
)

func readTrace(t *testing.T, ranges, ops string) []roofline.Timed {
	t.Helper()
	steps, err := roofline.ReadTrace(strings.NewReader(rangesHeader+ranges), strings.NewReader(opsHeader+ops))
	if err != nil {
		t.Fatalf("the trace was refused: %v", err)
	}
	return steps
}

// nsys's exports of one run are hundreds of megabytes and stay on the box. What
// a roofline needs from them is one line per step, so those lines are written
// beside the figures and a committed run can be redrawn from a checkout.
func TestStepsSurviveBeingWrittenAndReadBack(t *testing.T) {
	steps := readTrace(t,
		`:execute_4_context_0(sq0sk0sqsq0sqsk0)_generation_4(sq4sk1028sqsq4sqsk1028),1000000,3000000,900000,50000,PushPop,7,7,2,0,0,2,,:2
`,
		`1000000,1000000,41,256,1,1,256,1,1,128,0.000,0.098,,,,,NVIDIA GeForce RTX 3090 (0),1,,7,gemm
2500000,500000,42,32,4,1,128,1,1,96,0.000,0.000,,,,,NVIDIA GeForce RTX 3090 (0),1,,7,attention
`)

	var written strings.Builder
	if err := roofline.WriteSteps(&written, steps); err != nil {
		t.Fatalf("the steps were not written: %v", err)
	}
	back, err := roofline.ReadSteps(strings.NewReader(written.String()))
	if err != nil {
		t.Fatalf("the written steps were refused: %v", err)
	}
	if !reflect.DeepEqual(steps, back) {
		t.Errorf("the steps came back changed:\n got %+v\nwant %+v", back, steps)
	}
}

// A decode step of four sequences whose GPU work spans 3 ms, of which the GPU
// was running something for 1.5: a 1 ms GEMM, a gap while the CPU launched the
// next kernel, and 0.5 ms of attention. The gap is the engine's cost but not
// the model's, so it is in the span and not in the busy time. Work before the
// step began or after it ended belongs to some other step, and a range that is
// not a step is not one.
func TestAStepIsTimedByTheWorkItRanNotTheGapsBetween(t *testing.T) {
	steps := readTrace(t,
		`:execute_4_context_0(sq0sk0sqsq0sqsk0)_generation_4(sq4sk1028sqsq4sqsk1028),1000000,3000000,900000,50000,PushPop,7,7,2,0,0,2,,:2
:gpu_model_runner: forward,1000000,3000000,900000,40000,PushPop,7,7,2,1,0,3,2,:2:3
`,
		`500000,100000,40,1,1,1,128,1,1,36,0.000,0.000,,,,,NVIDIA GeForce RTX 3090 (0),1,,7,previous_step_kernel
1000000,1000000,41,256,1,1,256,1,1,128,0.000,0.098,,,,,NVIDIA GeForce RTX 3090 (0),1,,7,"void marlin::Marlin<(int)256, (int)4>(int4 const*, int4*)"
2500000,500000,42,32,4,1,128,1,1,96,0.000,0.000,,,,,NVIDIA GeForce RTX 3090 (0),1,,7,"void flash_fwd_kernel<Flash_fwd_kernel_traits<128, 64>, true>(Flash_fwd_params)"
4000000,200000,43,1,1,1,128,1,1,36,0.000,0.000,,,,,NVIDIA GeForce RTX 3090 (0),1,,7,next_step_kernel
`)

	if len(steps) != 1 {
		t.Fatalf("read %d steps, want the one engine step", len(steps))
	}
	s := steps[0]
	if s.Step.Generation.Requests != 4 {
		t.Errorf("the step decoded %d sequences, want 4", s.Step.Generation.Requests)
	}
	if s.Span != 3*time.Millisecond {
		t.Errorf("span = %v, want 3ms", s.Span)
	}
	if s.Busy != 1500*time.Microsecond {
		t.Errorf("busy = %v, want 1.5ms", s.Busy)
	}
}

// A copy engine and the compute engine run at once: the step's input ids land
// on stream 13 while the previous kernel is still running on stream 7. The GPU
// was busy from 1.0 ms to 2.2 ms, not for the 1.4 ms the two durations add up
// to.
func TestWorkOverlappingOnTwoStreamsIsCountedOnce(t *testing.T) {
	steps := readTrace(t,
		`:execute_2048_context_1(sq2048sk2048sqsq4194304sqsk4194304)_generation_0(sq0sk0sqsq0sqsk0),1000000,1200000,900000,50000,PushPop,7,7,2,0,0,2,,:2
`,
		`1000000,1000000,41,256,1,1,256,1,1,128,0.000,0.098,,,,,NVIDIA GeForce RTX 3090 (0),1,,7,gemm
1800000,400000,42,,,,,,,,,,0.016,40.000,Pinned,Device,NVIDIA GeForce RTX 3090 (0),1,,13,[CUDA memcpy Host-to-Device]
`)

	if len(steps) != 1 {
		t.Fatalf("read %d steps, want 1", len(steps))
	}
	if steps[0].Busy != 1200*time.Microsecond {
		t.Errorf("busy = %v, want 1.2ms", steps[0].Busy)
	}
}
