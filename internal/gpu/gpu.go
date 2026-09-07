// Package gpu probes what is currently holding the host's GPUs.
//
// One probe, two jobs. Before the fleet starts it answers "is any card already
// holding memory", and the fleet refuses to start if one is — leftovers of your
// own have already been the actual problem once, and a replica that starts
// beside someone else's job is measuring their workload as well as its own.
// During a cell the same probe answers "did anything foreign appear", which is
// the evidence that makes a number from a shared box defensible.
//
// Both answers come from the same snapshot on purpose. Two implementations of
// "is this card clean" would drift, and the one that drifted would be the one
// deciding whether a finished cell counts.
package gpu

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner executes one command and returns its stdout. It exists so the parsing
// and ownership rules can be tested against canned nvidia-smi and ps output on
// a machine with no GPU.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// Device is one GPU as the driver reports it.
type Device struct {
	Index          int    `json:"index"`
	UUID           string `json:"uuid"`
	MemoryUsedMiB  int    `json:"memory_used_mib"`
	UtilizationPct int    `json:"utilization_pct"`
}

// Process is one process holding memory on a GPU.
//
// PID and memory come from nvidia-smi; the user, command and parent come from
// the process table, because nvidia-smi reports none of them and a refusal that
// cannot name who is holding the card is not actionable.
type Process struct {
	PID           int    `json:"pid"`
	PPID          int    `json:"ppid"`
	User          string `json:"user"`
	Command       string `json:"command"`
	GPU           int    `json:"gpu"`
	MemoryUsedMiB int    `json:"memory_used_mib"`
}

// String renders a process for a refusal message or a cell's contamination
// evidence.
func (p Process) String() string {
	return fmt.Sprintf("pid=%d user=%s gpu=%d mem=%dMiB cmd=%s", p.PID, p.User, p.GPU, p.MemoryUsedMiB, p.Command)
}

// Snapshot is one reading of the whole host.
type Snapshot struct {
	At        time.Time `json:"at"`
	Devices   []Device  `json:"devices"`
	Processes []Process `json:"processes"`

	// parents maps every pid on the host to its parent, so a GPU process can be
	// traced back to the replica that spawned it. vLLM's API server is not the
	// process holding the card — its EngineCore child is — so ownership that
	// compared pids directly would report every one of our own replicas as
	// foreign.
	parents map[int]int
}

// Prober takes snapshots.
type Prober struct {
	run Runner
	now func() time.Time
}

// New builds a prober that shells out to nvidia-smi and ps.
func New() *Prober { return NewProber(execRunner) }

// NewProber builds a prober over run. Tests supply canned output.
func NewProber(run Runner) *Prober {
	return &Prober{run: run, now: time.Now}
}

func execRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("gpu: %s: %w", name, err)
	}
	return out, nil
}

// Snapshot reads the devices, the processes holding them, and the process table
// that attributes those processes to an owner.
func (p *Prober) Snapshot(ctx context.Context) (Snapshot, error) {
	deviceOut, err := p.run(ctx, "nvidia-smi",
		"--query-gpu=index,uuid,memory.used,utilization.gpu", "--format=csv,noheader,nounits")
	if err != nil {
		return Snapshot{}, err
	}
	devices, byUUID, err := parseDevices(deviceOut)
	if err != nil {
		return Snapshot{}, err
	}

	appOut, err := p.run(ctx, "nvidia-smi",
		"--query-compute-apps=pid,gpu_uuid,used_gpu_memory", "--format=csv,noheader,nounits")
	if err != nil {
		return Snapshot{}, err
	}

	psOut, err := p.run(ctx, "ps", "-eo", "pid=,ppid=,user=,comm=")
	if err != nil {
		return Snapshot{}, err
	}
	table, err := parseProcessTable(psOut)
	if err != nil {
		return Snapshot{}, err
	}

	processes, err := parseComputeApps(appOut, byUUID, table)
	if err != nil {
		return Snapshot{}, err
	}

	parents := make(map[int]int, len(table))
	for pid, e := range table {
		parents[pid] = e.ppid
	}
	return Snapshot{At: p.now(), Devices: devices, Processes: processes, parents: parents}, nil
}

// Dirty returns every device holding at least thresholdMiB.
//
// The check is on device memory rather than on the process list because a
// process in another user's namespace can be invisible to nvidia-smi while its
// memory is not. Refusing on the memory catches the case the process list
// misses.
func (s Snapshot) Dirty(thresholdMiB int) []Device {
	var dirty []Device
	for _, d := range s.Devices {
		if d.MemoryUsedMiB >= thresholdMiB {
			dirty = append(dirty, d)
		}
	}
	return dirty
}

// Foreign returns the processes on the fleet's GPUs that the fleet did not
// start. ownPIDs are the replica supervisors, as recorded in the pid files.
//
// Ownership is by ancestry, not by pid: vLLM's API server is not the process
// holding the card, its EngineCore child is, so comparing pids directly would
// report every one of our own replicas as foreign. A replica left over from an
// earlier run is correctly foreign — it is ours, but the fleet this cell is
// measuring did not start it and it is still holding a card.
func (s Snapshot) Foreign(ownPIDs []int) []Process {
	own := make(map[int]bool, len(ownPIDs))
	for _, pid := range ownPIDs {
		own[pid] = true
	}
	var foreign []Process
	for _, p := range s.Processes {
		if !s.descendsFrom(p.PID, own) {
			foreign = append(foreign, p)
		}
	}
	return foreign
}

// ForeignMemoryMiB totals the GPU memory held by processes the fleet did not
// start, across every card in the snapshot. A cell records the largest value
// any of its samples saw.
func (s Snapshot) ForeignMemoryMiB(ownPIDs []int) int {
	total := 0
	for _, p := range s.Foreign(ownPIDs) {
		total += p.MemoryUsedMiB
	}
	return total
}

// descendsFrom reports whether pid is in own or has an ancestor that is.
func (s Snapshot) descendsFrom(pid int, own map[int]bool) bool {
	// Bounded rather than trusting the table to be acyclic: a torn read of
	// /proc during process teardown is not worth hanging a benchmark for.
	for range 64 {
		if own[pid] {
			return true
		}
		parent, ok := s.parents[pid]
		if !ok || parent == 0 || parent == pid {
			return false
		}
		pid = parent
	}
	return false
}

// Limit restricts the snapshot to the given device indexes, so cleanliness can
// be answered for a subset of the cards. Dropping to a single NUMA node is the
// escalation §10 takes if the replicas turn out not to be interchangeable.
func (s Snapshot) Limit(indexes []int) Snapshot {
	keep := make(map[int]bool, len(indexes))
	for _, i := range indexes {
		keep[i] = true
	}
	limited := Snapshot{At: s.At, parents: s.parents}
	for _, d := range s.Devices {
		if keep[d.Index] {
			limited.Devices = append(limited.Devices, d)
		}
	}
	for _, p := range s.Processes {
		if keep[p.GPU] {
			limited.Processes = append(limited.Processes, p)
		}
	}
	return limited
}

type psEntry struct {
	ppid    int
	user    string
	command string
}

func parseDevices(out []byte) ([]Device, map[string]int, error) {
	var devices []Device
	byUUID := map[string]int{}
	for line := range lines(out) {
		fields := splitCSV(line)
		if len(fields) < 4 {
			return nil, nil, fmt.Errorf("gpu: device line %q has %d fields, want 4", line, len(fields))
		}
		index, err := atoi(fields[0], "device index", line)
		if err != nil {
			return nil, nil, err
		}
		used, err := atoi(fields[2], "memory.used", line)
		if err != nil {
			return nil, nil, err
		}
		// Utilization reads as [N/A] on some driver and virtualisation
		// combinations. It is a nice-to-have beside the memory that decides
		// cleanliness, so an unreadable value records as zero rather than
		// failing the whole probe.
		util, _ := strconv.Atoi(fields[3])
		devices = append(devices, Device{Index: index, UUID: fields[1], MemoryUsedMiB: used, UtilizationPct: util})
		byUUID[fields[1]] = index
	}
	return devices, byUUID, nil
}

func parseComputeApps(out []byte, byUUID map[string]int, table map[int]psEntry) ([]Process, error) {
	var processes []Process
	for line := range lines(out) {
		fields := splitCSV(line)
		if len(fields) < 3 {
			return nil, fmt.Errorf("gpu: compute-app line %q has %d fields, want 3", line, len(fields))
		}
		pid, err := atoi(fields[0], "pid", line)
		if err != nil {
			return nil, err
		}
		index, ok := byUUID[fields[1]]
		if !ok {
			return nil, fmt.Errorf("gpu: compute-app line %q names GPU %s, which no device reported", line, fields[1])
		}
		// A process that exited between the two nvidia-smi calls is absent from
		// the table; the memory it was holding is still worth reporting.
		used, err := atoi(fields[2], "used_gpu_memory", line)
		if err != nil {
			return nil, err
		}
		entry := table[pid]
		processes = append(processes, Process{
			PID:           pid,
			PPID:          entry.ppid,
			User:          entry.user,
			Command:       entry.command,
			GPU:           index,
			MemoryUsedMiB: used,
		})
	}
	return processes, nil
}

func parseProcessTable(out []byte) (map[int]psEntry, error) {
	table := map[int]psEntry{}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		table[pid] = psEntry{ppid: ppid, user: fields[2], command: strings.Join(fields[3:], " ")}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("gpu: read process table: %w", err)
	}
	return table, nil
}

// lines yields the non-empty lines of command output.
func lines(out []byte) func(func(string) bool) {
	return func(yield func(string) bool) {
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !yield(line) {
				return
			}
		}
	}
}

// splitCSV splits nvidia-smi's ", "-separated output, whose columns are padded.
func splitCSV(line string) []string {
	fields := strings.Split(line, ",")
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}
	return fields
}

func atoi(field, name, line string) (int, error) {
	v, err := strconv.Atoi(field)
	if err != nil {
		return 0, fmt.Errorf("gpu: %s %q in line %q: %w", name, field, line, err)
	}
	return v, nil
}
