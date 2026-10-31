package cluster

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	authnv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestReadOnlyClusterInventory(t *testing.T) {
	worker := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-a", Labels: map[string]string{"zone": "a"}}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{OperatingSystem: "linux", Architecture: "amd64"}, Addresses: []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: "10.0.0.11"}}, Capacity: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("32Gi")}, Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("7600m"), corev1.ResourceMemory: resource.MustParse("30Gi")}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}, Spec: corev1.NodeSpec{Taints: []corev1.Taint{{Key: "special", Value: "workload", Effect: corev1.TaintEffectNoSchedule}}}}
	master := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "control-1", Labels: map[string]string{"node-role.kubernetes.io/control-plane": ""}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"}, Spec: corev1.PodSpec{NodeName: "worker-a", Overhead: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")}, Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi")}}}}, InitContainers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("2Gi")}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	client := fake.NewSimpleClientset(worker, master, pod)
	client.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.35.0"}
	got, err := Inspect(context.Background(), client, "ctx", "cluster", "https://10.0.0.1:6443", "wso2")
	if err != nil {
		t.Fatal(err)
	}
	if got.KubernetesVersion != "v1.35.0" || got.Context != "ctx" || got.Namespace != "wso2" || len(got.Nodes) != 2 {
		t.Fatalf("bad cluster identity/nodes: %#v", got)
	}
	var w Node
	for _, n := range got.Nodes {
		if n.Name == "worker-a" {
			w = n
		}
	}
	if !w.Worker || !w.Ready || w.Schedulable || w.Eligible || w.Architecture != "amd64" || w.ScheduledPods != 1 || w.ScheduledRequests[string(corev1.ResourceCPU)] != "1" {
		t.Fatalf("worker inventory inaccurate: %#v", w)
	}
	if got := resource.MustParse(w.ScheduledRequests[string(corev1.ResourceMemory)]); got.Cmp(resource.MustParse("2304Mi")) != 0 {
		t.Fatalf("pod overhead/init-container memory requests were not included: %s", got.String())
	}
	if len(w.Taints) != 1 || w.Taints[0] != "special=workload:NoSchedule" {
		t.Fatalf("taints missing: %#v", w.Taints)
	}
	for _, a := range client.Actions() {
		switch a.GetVerb() {
		case "list", "get":
		default:
			t.Fatalf("unexpected mutating client action: %s %s", a.GetVerb(), a.GetResource().Resource)
		}
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("expected minimum-worker warning: %#v", got.Warnings)
	}
}

func TestForbiddenErrorIsActionableAndSanitized(t *testing.T) {
	err := classifyError(apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", nil))
	if err == nil || err.Error() == "" {
		t.Fatal("expected actionable forbidden error")
	}
}

func TestRemainingCapacitySubtractsScheduledRequests(t *testing.T) {
	got := Remaining(map[string]string{"cpu": "7600m", "memory": "30Gi"}, map[string]string{"cpu": "1500m", "memory": "3Gi"})
	if got["cpu"] != "6100m" || got["memory"] != "27Gi" {
		t.Fatalf("incorrect remaining capacity: %#v", got)
	}
}

func TestKubeconfigDiscoveryUsesReadsAndTransientAuthorizationReviews(t *testing.T) {
	var methods []string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer fixture-token-canary" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			json.NewEncoder(w).Encode(map[string]string{"major": "1", "minor": "35", "gitVersion": "v1.35.0"})
		case "/api/v1/nodes":
			json.NewEncoder(w).Encode(&corev1.NodeList{Items: []corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "worker"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{OperatingSystem: "linux", Architecture: "amd64"}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}}})
		case "/api/v1/pods":
			json.NewEncoder(w).Encode(&corev1.PodList{})
		case "/api/v1/namespaces/wso2":
			json.NewEncoder(w).Encode(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "wso2"}})
		case "/api/v1/services":
			json.NewEncoder(w).Encode(&corev1.ServiceList{})
		case "/apis/storage.k8s.io/v1/storageclasses/fast":
			json.NewEncoder(w).Encode(&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "fast"}, Provisioner: "fixture.csi"})
		case "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews":
			if r.Method != http.MethodPost {
				t.Errorf("authorization review used unexpected method %s", r.Method)
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{"apiVersion":"authorization.k8s.io/v1","kind":"SelfSubjectAccessReview","status":{"allowed":true}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := clientcmdapi.NewConfig()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	cfg.Clusters["fixture"] = &clientcmdapi.Cluster{Server: server.URL, CertificateAuthorityData: ca}
	cfg.Contexts["fixture-context"] = &clientcmdapi.Context{Cluster: "fixture", AuthInfo: "fixture-user", Namespace: "wso2"}
	cfg.AuthInfos["fixture-user"] = &clientcmdapi.AuthInfo{Token: "fixture-token-canary"}
	cfg.CurrentContext = "fixture-context"
	kubeconfig := filepath.Join(t.TempDir(), "config")
	if err := clientcmd.WriteToFile(*cfg, kubeconfig); err != nil {
		t.Fatal(err)
	}
	got, err := Discover(context.Background(), kubeconfig, "", "", PreflightOptions{Namespace: "wso2", SelectedWorkers: []string{"worker"}, StorageClass: "fast"})
	if err != nil {
		t.Fatal(err)
	}
	if got.KubernetesVersion != "v1.35.0" || got.Namespace != "wso2" || len(got.Nodes) != 1 {
		t.Fatalf("unexpected discovered inventory: %#v", got)
	}
	if got.Preflight.Namespace.Status != "EXISTS" || got.Preflight.StorageClass.Status != "EXISTS" || got.Preflight.SelectedWorkerStatus.Status != "READY" || len(got.Preflight.Permissions) == 0 {
		t.Fatalf("read-only preflight facts were not returned by the live API adapter: %#v", got.Preflight)
	}
	if len(methods) <= 3 {
		t.Fatalf("expected discovery, metadata and authorization review requests; got %#v", methods)
	}
	for _, method := range methods {
		if strings.HasPrefix(method, "POST ") && method != "POST /apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			t.Fatalf("non-authorization-review write observed: %s", method)
		}
		if !strings.HasPrefix(method, "GET ") && method != "POST /apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			t.Fatalf("unexpected Kubernetes API request: %s", method)
		}
	}
}

func TestReadOnlyPreflightReviewsPermissionsNamespaceStorageAndNodePorts(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-a"}, Spec: corev1.NodeSpec{}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{OperatingSystem: "linux", Architecture: "amd64"}, Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("32Gi")}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "existing"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeNodePort, Ports: []corev1.ServicePort{{NodePort: 30044}}}},
		&storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "fast"}, Provisioner: "example.csi", VolumeBindingMode: ptr(storagev1.VolumeBindingWaitForFirstConsumer)},
	)
	client.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.35.0"}
	client.PrependReactor("create", "selfsubjectaccessreviews", func(a k8stesting.Action) (bool, runtime.Object, error) {
		obj := a.(k8stesting.CreateAction).GetObject().(*authnv1.SelfSubjectAccessReview).DeepCopy()
		obj.Status.Allowed = !(obj.Spec.ResourceAttributes.Resource == "secrets" && obj.Spec.ResourceAttributes.Verb == "get")
		obj.Status.Denied = !obj.Status.Allowed
		return true, obj, nil
	})
	inv, err := Inspect(context.Background(), client, "ctx", "cluster", "https://cluster.example", "wso2", PreflightOptions{Namespace: "wso2", SelectedWorkers: []string{"worker-a"}, StorageClass: "fast", NodePorts: []int{30043, 30044}})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Preflight.Namespace.Status != "MISSING" || inv.Preflight.SelectedWorkerStatus.Status != "READY" || inv.Preflight.StorageClass.Status != "EXISTS" {
		t.Fatalf("unexpected preflight facts: %#v", inv.Preflight)
	}
	if inv.Preflight.NodePorts[0].Status != "AVAILABLE" || inv.Preflight.NodePorts[1].Status != "CONFLICT" {
		t.Fatalf("NodePort conflicts not detected: %#v", inv.Preflight.NodePorts)
	}
	var denied bool
	for _, p := range inv.Preflight.Permissions {
		if p.Resource == "secrets" && p.Verb == "get" && p.Status == "DENIED" {
			denied = true
		}
		if p.Resource == "secrets" && p.Verb == "list" {
			t.Fatal("preflight must not list/read Secret contents")
		}
	}
	if !denied {
		t.Fatal("denied SelfSubjectAccessReview was not reported")
	}
	for _, a := range client.Actions() {
		if a.GetVerb() == "create" && a.GetResource().Resource != "selfsubjectaccessreviews" {
			t.Fatalf("persistent create performed during preflight: %s", a.GetResource().Resource)
		}
	}
}

func TestAuthorizationReviewFailureIsIndeterminateAndPodListFailureIsVisible(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-a"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{OperatingSystem: "linux", Architecture: "amd64"}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}})
	client.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.35.0"}
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", nil)
	})
	client.PrependReactor("create", "selfsubjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "selfsubjectaccessreviews"}, "", nil)
	})
	inv, err := Inspect(context.Background(), client, "ctx", "cluster", "https://cluster.example", "default", PreflightOptions{Namespace: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(inv.PodRequestsStatus, "INDETERMINATE") || inv.Preflight.AccessReviewAPI.Status != "INDETERMINATE" {
		t.Fatalf("incomplete read/authz checks were not reported: %#v", inv)
	}
	for _, p := range inv.Preflight.Permissions {
		if p.Status != "INDETERMINATE" {
			t.Fatalf("permission fabricated after review API failure: %#v", p)
		}
	}
}

func ptr[T any](v T) *T { return &v }
