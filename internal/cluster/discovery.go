package cluster

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/auditlog"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	resourcehelper "k8s.io/component-helpers/resource"
)

type Node struct {
	Name, OS, Architecture                   string
	Ready, Worker, Schedulable, Eligible     bool
	InternalIPs                              []string
	Labels                                   map[string]string
	Taints                                   []string
	Capacity, Allocatable, ScheduledRequests map[string]string
	ScheduledPods                            int
}
type Inventory struct {
	Context, Cluster, APIEndpoint, KubernetesVersion, Namespace string
	Nodes                                                       []Node
	Workloads                                                   []WorkloadReservation
	PodRequestsStatus                                           string
	Preflight                                                   PreflightResults
	Warnings                                                    []string
}

func Discover(ctx context.Context, kubeconfig, contextName, namespace string, options ...PreflightOptions) (Inventory, error) {
	if kubeconfig == "" {
		return Inventory{}, fmt.Errorf("kubeconfig path is required")
	}
	loading := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}
	loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, overrides)
	restConfig, err := loader.ClientConfig()
	if err != nil {
		return Inventory{}, fmt.Errorf("cannot load selected kubeconfig/context; check file, context, and authentication prerequisites")
	}
	if restConfig.Timeout == 0 || restConfig.Timeout > 12*time.Second {
		restConfig.Timeout = 12 * time.Second
	}
	restConfig.QPS = 5
	restConfig.Burst = 10
	raw, err := loading.Load()
	if err != nil {
		return Inventory{}, fmt.Errorf("cannot read selected kubeconfig")
	}
	selectedContext := contextName
	if selectedContext == "" {
		selectedContext = raw.CurrentContext
	}
	ctxEntry, ok := raw.Contexts[selectedContext]
	if !ok || ctxEntry == nil {
		return Inventory{}, fmt.Errorf("selected Kubernetes context does not exist in kubeconfig")
	}
	clusterEntry := raw.Clusters[ctxEntry.Cluster]
	if clusterEntry == nil {
		return Inventory{}, fmt.Errorf("selected Kubernetes cluster entry does not exist in kubeconfig")
	}
	if namespace == "" {
		namespace = ctxEntry.Namespace
	}
	if namespace == "" {
		namespace = "default"
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return Inventory{}, fmt.Errorf("cannot initialize Kubernetes API client")
	}
	inv, err := Inspect(ctx, client, selectedContext, ctxEntry.Cluster, auditlog.RedactURL(clusterEntry.Server), namespace, options...)
	if err != nil {
		return Inventory{}, err
	}
	return inv, nil
}

func Inspect(ctx context.Context, client kubernetes.Interface, contextName, clusterName, endpoint, namespace string, options ...PreflightOptions) (Inventory, error) {
	inv := Inventory{Context: contextName, Cluster: clusterName, APIEndpoint: endpoint, Namespace: namespace}
	server, err := client.Discovery().ServerVersion()
	if err != nil {
		return inv, classifyError(err)
	}
	inv.KubernetesVersion = server.GitVersion
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return inv, classifyError(err)
	}
	pods, err := client.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		inv.PodRequestsStatus = "INDETERMINATE: " + classifyError(err).Error()
		inv.Warnings = append(inv.Warnings, inv.PodRequestsStatus)
		pods = &corev1.PodList{}
	} else {
		inv.PodRequestsStatus = "AVAILABLE"
	}
	requests := map[string]corev1.ResourceList{}
	podCounts := map[string]int{}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		req := resourcehelper.PodRequests(pod, resourcehelper.PodResourcesOptions{})
		inv.Workloads = append(inv.Workloads, WorkloadReservation{Namespace: pod.Namespace, Name: pod.Name, Node: pod.Spec.NodeName, Requests: quantities(req), NodeSelector: copyLabels(pod.Spec.NodeSelector), HasAffinity: pod.Spec.Affinity != nil, HasTolerations: len(pod.Spec.Tolerations) > 0, HasPVCs: podUsesPVC(pod)})
		if pod.Spec.NodeName == "" {
			continue
		}
		if requests[pod.Spec.NodeName] == nil {
			requests[pod.Spec.NodeName] = corev1.ResourceList{}
		}
		addResources(requests[pod.Spec.NodeName], req)
		podCounts[pod.Spec.NodeName]++
	}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		item := Node{Name: n.Name, OS: n.Status.NodeInfo.OperatingSystem, Architecture: n.Status.NodeInfo.Architecture, Ready: nodeReady(n), Worker: !controlPlane(n), Schedulable: !n.Spec.Unschedulable, Labels: copyLabels(n.Labels), Capacity: quantities(n.Status.Capacity), Allocatable: quantities(n.Status.Allocatable), ScheduledRequests: quantities(requests[n.Name]), ScheduledPods: podCounts[n.Name]}
		for _, a := range n.Status.Addresses {
			if a.Type == corev1.NodeInternalIP {
				item.InternalIPs = append(item.InternalIPs, a.Address)
			}
		}
		for _, t := range n.Spec.Taints {
			item.Taints = append(item.Taints, fmt.Sprintf("%s=%s:%s", t.Key, t.Value, t.Effect))
			if t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute {
				item.Schedulable = false
			}
		}
		item.Eligible = item.Worker && item.Ready && item.Schedulable
		sort.Strings(item.InternalIPs)
		sort.Strings(item.Taints)
		inv.Nodes = append(inv.Nodes, item)
	}
	sort.Slice(inv.Nodes, func(i, j int) bool { return inv.Nodes[i].Name < inv.Nodes[j].Name })
	workers := 0
	readyWorkers := 0
	for _, n := range inv.Nodes {
		if n.Worker {
			workers++
			if n.Ready {
				readyWorkers++
			}
		}
	}
	if workers == 0 {
		inv.Warnings = append(inv.Warnings, "No worker nodes were discovered; workloads require eligible worker capacity.")
	} else if readyWorkers < 3 {
		inv.Warnings = append(inv.Warnings, fmt.Sprintf("Only %d ready worker nodes were discovered; the profile requests a minimum of three.", readyWorkers))
	}
	if len(options) > 0 {
		inv.Preflight = collectPreflight(ctx, client, &inv, options[0])
	}
	return inv, nil
}

func classifyError(err error) error {
	if apierrors.IsUnauthorized(err) {
		return fmt.Errorf("Kubernetes API authentication failed; verify the selected context and credential prerequisites")
	}
	if apierrors.IsForbidden(err) {
		return fmt.Errorf("Kubernetes API denied a required read operation; grant read access to version, nodes, and pods")
	}
	if apierrors.IsTimeout(err) || apierrors.IsServerTimeout(err) {
		return fmt.Errorf("Kubernetes API read timed out; retry after confirming network reachability")
	}
	return fmt.Errorf("Kubernetes API discovery failed (%T); check endpoint reachability, TLS trust, and credentials", err)
}
func controlPlane(n *corev1.Node) bool {
	_, cp := n.Labels["node-role.kubernetes.io/control-plane"]
	_, master := n.Labels["node-role.kubernetes.io/master"]
	return cp || master
}
func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
func copyLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}
func quantities(values corev1.ResourceList) map[string]string {
	out := map[string]string{}
	for k, v := range values {
		out[string(k)] = v.String()
	}
	return out
}
func addResources(dst, src corev1.ResourceList) {
	for k, v := range src {
		if old, ok := dst[k]; ok {
			old.Add(v)
			dst[k] = old
		} else {
			dst[k] = v.DeepCopy()
		}
	}
}

func conservativePodRequests(p *corev1.Pod) corev1.ResourceList {
	return resourcehelper.PodRequests(p, resourcehelper.PodResourcesOptions{})
}

func podUsesPVC(p *corev1.Pod) bool {
	for _, v := range p.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			return true
		}
	}
	return false
}

func Remaining(allocatable, requests map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range allocatable {
		a, e := resource.ParseQuantity(v)
		if e != nil {
			continue
		}
		r := resource.MustParse("0")
		if raw, ok := requests[k]; ok {
			if parsed, err := resource.ParseQuantity(raw); err == nil {
				r = parsed
			}
		}
		a.Sub(r)
		out[k] = a.String()
	}
	return out
}
