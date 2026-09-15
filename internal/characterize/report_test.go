package characterize_test

import (
	"strings"
	"testing"
	"time"

	"github.com/yuchia329/kvroute/internal/bench"
	"github.com/yuchia329/kvroute/internal/characterize"
	"github.com/yuchia329/kvroute/internal/record"
)

// A spread computed from a figure that is missing is not a spread. The report
// has to say the comparison is absent rather than print a percentage derived
// from a replica that never answered.
func TestTheReportDoesNotPrintASpreadForALevelWithASilentReplica(t *testing.T) {
	var rows []bench.Result
	for i, id := range sixReplicaIDs() {
		for repetition := 1; repetition <= 2; repetition++ {
			batch := replicaRows(id, 1, 20, 320*time.Millisecond, 8*time.Millisecond)
			if i == 4 {
				for j := range batch {
					batch[j].Outcome = record.OutcomeDropped
					batch[j].TTFTNs, batch[j].ITLP50Ns = 0, 0
				}
			}
			rows = append(rows, repeated(batch, repetition)...)
		}
	}
	topo := hostTopology(t)
	c := characterize.Characterization{
		Symmetry: characterize.CompareReplicas(rows, characterize.Placements(sixReplicaIDs(), topo), topo, 0),
		Topology: topo,
	}

	report := characterize.Report(c)
	if strings.Contains(report, "-100.0%") {
		t.Error("the report printed a spread computed from a replica that produced nothing")
	}
	if !strings.Contains(report, "No comparison") {
		t.Error("the report did not say the comparison was absent")
	}
	if !strings.Contains(report, "replica-4") {
		t.Error("the report did not name the replica that answered nothing")
	}
}
