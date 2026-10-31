package cluster

import (
	"context"
	"fmt"
	"sort"

	authnv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

type WorkloadReservation struct {
	Namespace, Name, Node                string
	Requests                             map[string]string
	NodeSelector                         map[string]string
	HasAffinity, HasTolerations, HasPVCs bool
}

type PreflightOptions struct {
	Namespace                string
	SelectedWorkers          []string
	StorageClass             string
	NodePorts                []int
	NodePortMin, NodePortMax int
}

type Check struct{ Status, Detail string }
type Permission struct {
	Verb, Group, Resource, Namespace string
	Status, Reason                   string
}
type StorageClassInfo struct {
	Name, Status, Provisioner, ReclaimPolicy, VolumeBindingMode string
	AllowedTopologies                                           []string
	Deferred                                                    string
}
type NodePortCheck struct {
	Port           int
	Status, Detail string
}
type PreflightResults struct {
	Namespace            Check
	PodReservations      Check
	ServiceInventory     Check
	AccessReviewAPI      Check
	Permissions          []Permission
	StorageClass         StorageClassInfo
	NodePorts            []NodePortCheck
	SelectedWorkers      []string
	SelectedWorkerStatus Check
	NodePortRange        string
}

type accessRequest struct{ verb, group, resource, namespace, name string }

func collectPreflight(ctx context.Context, client kubernetes.Interface, inv *Inventory, opts PreflightOptions) PreflightResults {
	r := PreflightResults{Namespace: Check{Status: "INDETERMINATE", Detail: "namespace was not checked"}, PodReservations: Check{Status: inv.PodRequestsStatus}, ServiceInventory: Check{Status: "INDETERMINATE", Detail: "services were not checked"}, AccessReviewAPI: Check{Status: "INDETERMINATE", Detail: "SelfSubjectAccessReview was not checked"}}
	ns := opts.Namespace
	if ns == "" {
		ns = inv.Namespace
	}
	_, err := client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	switch {
	case err == nil:
		r.Namespace = Check{Status: "EXISTS", Detail: "target namespace exists"}
	case apierrors.IsNotFound(err):
		r.Namespace = Check{Status: "MISSING", Detail: "namespace is absent; preflight did not create it"}
	case err != nil:
		r.Namespace = Check{Status: "INDETERMINATE", Detail: classifyError(err).Error()}
	}

	if opts.StorageClass != "" {
		sc, err := client.StorageV1().StorageClasses().Get(ctx, opts.StorageClass, metav1.GetOptions{})
		if err == nil {
			item := StorageClassInfo{Name: sc.Name, Status: "EXISTS", Provisioner: sc.Provisioner, VolumeBindingMode: "Immediate", Deferred: "Access modes, provisioning success, topology behavior, and node-loss failover are not established by StorageClass metadata."}
			if sc.ReclaimPolicy != nil {
				item.ReclaimPolicy = string(*sc.ReclaimPolicy)
			}
			if sc.VolumeBindingMode != nil {
				item.VolumeBindingMode = string(*sc.VolumeBindingMode)
			}
			for _, term := range sc.AllowedTopologies {
				for _, e := range term.MatchLabelExpressions {
					item.AllowedTopologies = append(item.AllowedTopologies, fmt.Sprintf("%s in %v", e.Key, e.Values))
				}
			}
			r.StorageClass = item
		} else if apierrors.IsNotFound(err) {
			r.StorageClass = StorageClassInfo{Name: opts.StorageClass, Status: "MISSING", Deferred: "No StorageClass with this name exists."}
		} else {
			r.StorageClass = StorageClassInfo{Name: opts.StorageClass, Status: "INDETERMINATE", Deferred: classifyError(err).Error()}
		}
	} else {
		r.StorageClass = StorageClassInfo{Status: "UNSELECTED", Deferred: "StorageClass choice has not been configured."}
	}

	services, err := client.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	used := map[int]string{}
	if err != nil {
		r.ServiceInventory = Check{Status: "INDETERMINATE", Detail: classifyError(err).Error()}
	} else {
		r.ServiceInventory = Check{Status: "AVAILABLE", Detail: "read-only Service inventory completed"}
		for _, svc := range services.Items {
			for _, sp := range svc.Spec.Ports {
				if sp.NodePort > 0 {
					used[int(sp.NodePort)] = svc.Namespace + "/" + svc.Name
				}
			}
		}
	}
	minPort, maxPort := opts.NodePortMin, opts.NodePortMax
	rangeSource := "declared"
	if minPort == 0 && maxPort == 0 {
		minPort, maxPort = 30000, 32767
		rangeSource = "Kubernetes default assumption (API server range is not discoverable here)"
	}
	r.NodePortRange = fmt.Sprintf("%d-%d (%s)", minPort, maxPort, rangeSource)
	for _, p := range opts.NodePorts {
		check := NodePortCheck{Port: p}
		if p < minPort || p > maxPort {
			check.Status = "OUT OF RANGE"
			check.Detail = "outside the configured/assumed NodePort range"
		} else if owner, ok := used[p]; ok {
			check.Status = "CONFLICT"
			check.Detail = "already assigned to Service " + owner
		} else if err != nil {
			check.Status = "INDETERMINATE"
			check.Detail = "could not list Services to check conflicts"
		} else {
			check.Status = "AVAILABLE"
			check.Detail = "no current Service conflict observed; API server range uses " + rangeSource
		}
		r.NodePorts = append(r.NodePorts, check)
	}

	selected := map[string]bool{}
	for _, n := range inv.Nodes {
		if n.Eligible {
			selected[n.Name] = true
		}
	}
	if len(opts.SelectedWorkers) > 0 {
		selected = map[string]bool{}
		for _, name := range opts.SelectedWorkers {
			selected[name] = true
		}
	}
	for name := range selected {
		found := false
		for _, n := range inv.Nodes {
			if n.Name == name {
				found = true
				if !n.Eligible {
					r.SelectedWorkerStatus = Check{Status: "BLOCKED", Detail: "selected worker is not Ready, schedulable, and free of untolerated NoSchedule/NoExecute taints"}
				}
				break
			}
		}
		if !found {
			r.SelectedWorkerStatus = Check{Status: "BLOCKED", Detail: "selected worker was not discovered: " + name}
		}
	}
	for name := range selected {
		r.SelectedWorkers = append(r.SelectedWorkers, name)
	}
	sort.Strings(r.SelectedWorkers)
	if r.SelectedWorkerStatus.Status == "" {
		if len(r.SelectedWorkers) == 0 {
			r.SelectedWorkerStatus = Check{Status: "BLOCKED", Detail: "no eligible worker selected or discovered"}
		} else {
			r.SelectedWorkerStatus = Check{Status: "READY", Detail: "selected workers are Ready, schedulable, and eligible under current profile taints"}
		}
	}

	requests := []accessRequest{{"get", "", "namespaces", "", ""}, {"list", "", "nodes", "", ""}, {"list", "", "pods", "", ""}, {"list", "", "services", "", ""}, {"create", "apps", "deployments", ns, ""}, {"get", "apps", "deployments", ns, ""}, {"list", "apps", "deployments", ns, ""}, {"patch", "apps", "deployments", ns, ""}, {"create", "", "services", ns, ""}, {"get", "", "services", ns, ""}, {"patch", "", "services", ns, ""}, {"create", "", "secrets", ns, ""}, {"get", "", "secrets", ns, ""}, {"patch", "", "secrets", ns, ""}, {"create", "", "persistentvolumeclaims", ns, ""}, {"get", "", "persistentvolumeclaims", ns, ""}}
	if r.Namespace.Status == "MISSING" {
		requests = append(requests, accessRequest{"create", "", "namespaces", "", ""})
	}
	if opts.StorageClass != "" {
		requests = append(requests, accessRequest{"get", "storage.k8s.io", "storageclasses", "", opts.StorageClass})
	}
	for _, q := range requests {
		review := &authnv1.SelfSubjectAccessReview{Spec: authnv1.SelfSubjectAccessReviewSpec{ResourceAttributes: &authnv1.ResourceAttributes{Verb: q.verb, Group: q.group, Resource: q.resource, Namespace: q.namespace, Name: q.name}}}
		res, e := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
		perm := Permission{Verb: q.verb, Group: q.group, Resource: q.resource, Namespace: q.namespace, Status: "INDETERMINATE"}
		if e != nil {
			perm.Reason = classifyError(e).Error()
			r.Permissions = append(r.Permissions, perm)
			for _, rest := range requests[len(r.Permissions):] {
				r.Permissions = append(r.Permissions, Permission{Verb: rest.verb, Group: rest.group, Resource: rest.resource, Namespace: rest.namespace, Status: "INDETERMINATE", Reason: "authorization review request failed; remaining permissions were not checked"})
			}
			r.AccessReviewAPI = Check{Status: "INDETERMINATE", Detail: "SelfSubjectAccessReview could not be completed: " + classifyError(e).Error()}
			break
		}
		if res.Status.EvaluationError != "" {
			perm.Reason = res.Status.EvaluationError
		} else if res.Status.Allowed {
			perm.Status = "ALLOWED"
			perm.Reason = "API server authorized the requested action"
		} else if res.Status.Denied {
			perm.Status = "DENIED"
			perm.Reason = "API server denied the requested action"
		} else {
			perm.Reason = "API server returned neither allowed nor denied"
		}
		r.Permissions = append(r.Permissions, perm)
		r.AccessReviewAPI = Check{Status: "AVAILABLE", Detail: "SelfSubjectAccessReview returned per-action authorization decisions"}
	}
	return r
}
