package cluster

import (
	"fmt"
	"sort"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type LossScenario struct {
	FailedWorker string
	Status       string
	Detail       string
	NodesUsed    []string
}
type CapacityAssessment struct {
	Status, Assumptions, Limitations string
	Scenarios                        []LossScenario
}
type placementTask struct {
	component string
	requests  corev1.ResourceList
}
type capacityNode struct {
	name                      string
	cpu, memory               int64
	initialCPU, initialMemory int64
}

// AssessOneWorkerLoss performs a bounded CPU/memory placement preflight. It
// retains every live or pending competitor request, including pods assigned to
// the failed node. It is deliberately not presented as kube-scheduler output.
func AssessOneWorkerLoss(inv Inventory, selected []string, profile registry.ProfileDescriptor) CapacityAssessment {
	a := CapacityAssessment{Status: "UNVERIFIED", Assumptions: "All nonterminal existing pod requests survive or must be rescheduled; the full requests of the failed worker's workloads are retained. Candidate replicas are added to that workload set.", Limitations: "Bounded CPU/memory bin packing only; not the Kubernetes scheduler. It does not model disk/ephemeral storage, pod-count/IP limits, PVC topology, admission, priority/preemption, PDBs, DaemonSet replacement semantics, component-specific selectors/affinity, or all scheduler plugins. It is not HA certification or a throughput result."}
	if inv.PodRequestsStatus != "AVAILABLE" {
		a.Status = "UNVERIFIED"
		a.Limitations += " Pod reservation discovery is incomplete: " + inv.PodRequestsStatus
		return a
	}
	if len(selected) == 0 {
		a.Limitations += " No eligible selected worker set is available."
		return a
	}
	selectedSet := map[string]bool{}
	for _, name := range selected {
		selectedSet[name] = true
	}
	nodes := map[string]Node{}
	for _, n := range inv.Nodes {
		if selectedSet[n.Name] && n.Eligible {
			nodes[n.Name] = n
		}
	}
	if len(nodes) != len(selected) {
		a.Limitations += " One or more selected workers are not eligible."
		return a
	}
	if len(nodes) < 2 {
		a.Status = "NO FIT"
		for _, name := range selected {
			a.Scenarios = append(a.Scenarios, LossScenario{FailedWorker: name, Status: "NO FIT", Detail: "one-worker loss leaves no selected worker available"})
		}
		return a
	}
	tasksByNode := map[string][]placementTask{}
	globalPending := []placementTask{}
	for _, w := range inv.Workloads {
		if w.Node != "" && !selectedSet[w.Node] {
			continue
		}
		t := placementTask{component: "existing " + w.Namespace + "/" + w.Name, requests: parseResources(w.Requests)}
		if w.Node == "" {
			globalPending = append(globalPending, t)
		} else {
			tasksByNode[w.Node] = append(tasksByNode[w.Node], t)
		}
	}
	componentTasks := []placementTask{}
	for _, c := range profile.Components {
		cpu, err1 := resource.ParseQuantity(c.CPURequest)
		mem, err2 := resource.ParseQuantity(c.MemoryRequest)
		if err1 != nil || err2 != nil {
			a.Limitations += " Candidate resource request could not be parsed."
			return a
		}
		for i := 0; i < c.Replicas; i++ {
			componentTasks = append(componentTasks, placementTask{component: c.Name, requests: corev1.ResourceList{corev1.ResourceCPU: cpu.DeepCopy(), corev1.ResourceMemory: mem.DeepCopy()}})
		}
	}
	allFit := true
	for _, failed := range selected {
		s := LossScenario{FailedWorker: failed, Status: "FIT UNDER MODEL"}
		survivors := []capacityNode{}
		for name, n := range nodes {
			if name == failed {
				continue
			}
			remaining := Remaining(n.Allocatable, n.ScheduledRequests)
			cpu := parseMilli(remaining["cpu"])
			mem := parseBytes(remaining["memory"])
			if cpu < 0 || mem < 0 {
				s.Status = "UNVERIFIED"
				s.Detail = "allocatable or existing request quantity is missing or invalid"
				break
			}
			survivors = append(survivors, capacityNode{name: name, cpu: cpu, memory: mem, initialCPU: cpu, initialMemory: mem})
		}
		if s.Status == "UNVERIFIED" {
			allFit = false
			a.Scenarios = append(a.Scenarios, s)
			continue
		}
		jobs := append([]placementTask{}, globalPending...)
		jobs = append(jobs, tasksByNode[failed]...)
		jobs = append(jobs, componentTasks...)
		constraintsUnknown := false
		for _, w := range inv.Workloads {
			if (w.Node == failed || w.Node == "") && (len(w.NodeSelector) > 0 || w.HasAffinity || w.HasTolerations || w.HasPVCs) {
				constraintsUnknown = true
				break
			}
		}
		if constraintsUnknown {
			s.Status = "UNVERIFIED"
			s.Detail = "a rescheduled existing workload has node selectors, affinity, tolerations, or PVCs that this preflight does not simulate"
			allFit = false
			a.Scenarios = append(a.Scenarios, s)
			continue
		}
		if len(jobs) > 128 {
			s.Status = "UNVERIFIED"
			s.Detail = "more than 128 pending/recovery/candidate pods exceed the bounded placement search"
			allFit = false
			a.Scenarios = append(a.Scenarios, s)
			continue
		}
		sort.SliceStable(jobs, func(i, j int) bool { return taskScore(jobs[i]) > taskScore(jobs[j]) })
		search := boundedPlacement{nodes: survivors, jobs: jobs, budget: 100_000}
		fit, used, exhausted := search.place(0)
		switch {
		case exhausted:
			s.Status = "UNVERIFIED"
			s.Detail = "bounded placement search reached its state limit"
			allFit = false
		case !fit:
			s.Status = "NO FIT"
			s.Detail = "all retained competitor and candidate requests could not be placed per eligible worker under this resource-only model"
			allFit = false
		default:
			s.Status = "FIT UNDER MODEL"
			s.Detail = "all retained competitor and candidate CPU/memory requests fit the surviving eligible workers"
			for name := range used {
				s.NodesUsed = append(s.NodesUsed, name)
			}
			sort.Strings(s.NodesUsed)
		}
		a.Scenarios = append(a.Scenarios, s)
	}
	if allFit {
		a.Status = "FIT UNDER MODEL"
	} else if len(a.Scenarios) > 0 {
		hasUnknown := false
		for _, s := range a.Scenarios {
			if s.Status == "UNVERIFIED" {
				hasUnknown = true
			}
		}
		if hasUnknown {
			a.Status = "UNVERIFIED"
		} else {
			a.Status = "NO FIT"
		}
	}
	return a
}

type boundedPlacement struct {
	nodes  []capacityNode
	jobs   []placementTask
	budget int
	states int
}

func (s *boundedPlacement) place(i int) (bool, map[string]bool, bool) {
	if i == len(s.jobs) {
		return true, map[string]bool{}, false
	}
	s.states++
	if s.states > s.budget {
		return false, nil, true
	}
	t := s.jobs[i]
	seen := map[string]bool{}
	for n := range s.nodes {
		node := &s.nodes[n]
		key := fmt.Sprintf("%d/%d", node.cpu, node.memory)
		if seen[key] {
			continue
		}
		seen[key] = true
		cpu := taskMilli(t)
		mem := taskBytes(t)
		if cpu > node.cpu || mem > node.memory {
			continue
		}
		node.cpu -= cpu
		node.memory -= mem
		ok, used, exhausted := s.place(i + 1)
		node.cpu += cpu
		node.memory += mem
		if exhausted {
			return false, nil, true
		}
		if ok {
			if used == nil {
				used = map[string]bool{}
			}
			used[node.name] = true
			return true, used, false
		}
	}
	return false, nil, false
}
func taskScore(t placementTask) int64 { return taskMilli(t)*1024 + taskBytes(t)/(1<<20) }
func taskMilli(t placementTask) int64 { q := t.requests[corev1.ResourceCPU]; return q.MilliValue() }
func taskBytes(t placementTask) int64 { q := t.requests[corev1.ResourceMemory]; return q.Value() }
func parseMilli(s string) int64 {
	if s == "" {
		return 0
	}
	q, e := resource.ParseQuantity(s)
	if e != nil {
		return -1
	}
	return q.MilliValue()
}
func parseBytes(s string) int64 {
	if s == "" {
		return 0
	}
	q, e := resource.ParseQuantity(s)
	if e != nil {
		return -1
	}
	return q.Value()
}
func parseResources(in map[string]string) corev1.ResourceList {
	out := corev1.ResourceList{}
	for k, v := range in {
		if q, e := resource.ParseQuantity(v); e == nil {
			out[corev1.ResourceName(k)] = q
		}
	}
	return out
}

func PlatformsForWorkers(inv Inventory, selected []string) []registry.Platform {
	set := map[string]bool{}
	for _, n := range selected {
		set[n] = true
	}
	seen := map[string]bool{}
	out := []registry.Platform{}
	for _, n := range inv.Nodes {
		if !set[n.Name] || !n.Eligible || n.OS == "" || n.Architecture == "" {
			continue
		}
		p := registry.Platform{OS: n.OS, Architecture: n.Architecture}
		k := p.OS + "/" + p.Architecture
		if !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OS == out[j].OS {
			return out[i].Architecture < out[j].Architecture
		}
		return out[i].OS < out[j].OS
	})
	return out
}
