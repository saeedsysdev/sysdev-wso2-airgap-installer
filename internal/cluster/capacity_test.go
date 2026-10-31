package cluster

import (
	"testing"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

func worker(name, cpu, memory string) Node {
	return Node{Name: name, Worker: true, Ready: true, Schedulable: true, Eligible: true, Allocatable: map[string]string{"cpu": cpu, "memory": memory}, ScheduledRequests: map[string]string{}}
}

func TestOneWorkerLossRetainsFailedNodeWorkloadRequests(t *testing.T) {
	inv := Inventory{PodRequestsStatus: "AVAILABLE", Nodes: []Node{worker("worker-a", "12", "32Gi"), worker("worker-b", "12", "32Gi"), worker("worker-c", "12", "32Gi")}, Workloads: []WorkloadReservation{{Namespace: "critical", Name: "competitor", Node: "worker-c", Requests: map[string]string{"cpu": "2", "memory": "40Gi"}}}}
	got := AssessOneWorkerLoss(inv, []string{"worker-a", "worker-b", "worker-c"}, registry.Profile)
	for _, s := range got.Scenarios {
		if s.FailedWorker == "worker-c" && s.Status != "NO FIT" {
			t.Fatalf("failed-node competitor request was discarded: %#v", s)
		}
	}
}

func TestOneWorkerLossChecksPerNodeCPUAndMemoryPlacement(t *testing.T) {
	inv := Inventory{PodRequestsStatus: "AVAILABLE", Nodes: []Node{worker("worker-lost", "32", "64Gi"), worker("worker-cpu", "20", "2Gi"), worker("worker-memory", "1", "40Gi")}}
	got := AssessOneWorkerLoss(inv, []string{"worker-lost", "worker-cpu", "worker-memory"}, registry.Profile)
	if got.Status != "NO FIT" {
		t.Fatalf("aggregate-only placement incorrectly passed: %#v", got)
	}
	var found bool
	for _, s := range got.Scenarios {
		if s.FailedWorker == "worker-lost" {
			found = true
			if s.Status != "NO FIT" {
				t.Fatalf("per-node placement result: %#v", s)
			}
		}
	}
	if !found {
		t.Fatalf("missing worker-loss scenario: %#v", got.Scenarios)
	}
}

func TestOneWorkerLossMarksIncompleteReservationsUnverified(t *testing.T) {
	inv := Inventory{PodRequestsStatus: "INDETERMINATE: forbidden", Nodes: []Node{worker("a", "8", "20Gi"), worker("b", "8", "20Gi")}}
	got := AssessOneWorkerLoss(inv, []string{"a", "b"}, registry.Profile)
	if got.Status != "UNVERIFIED" {
		t.Fatalf("missing pod reservations were treated as zero: %#v", got)
	}
}
