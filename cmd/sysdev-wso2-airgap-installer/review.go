package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/app"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/cluster"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/domain"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/imagearchive"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/plan"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/version"
)

func runConfigured(ctx context.Context, cmd string, service *app.Service, cfg config.File, logPath string) error {
	caPath := cfg.Registry.CABundlePath
	if caPath == "" {
		caPath = os.Getenv("SYSDEV_REGISTRY_CA_BUNDLE")
	}
	cred := registry.Credentials{Username: os.Getenv("SYSDEV_REGISTRY_USERNAME"), Password: os.Getenv("SYSDEV_REGISTRY_PASSWORD"), Token: os.Getenv("SYSDEV_REGISTRY_TOKEN"), CABundlePath: caPath}
	if cred.Token != "" && (cred.Username != "" || cred.Password != "") {
		return fmt.Errorf("set either SYSDEV_REGISTRY_TOKEN or SYSDEV_REGISTRY_USERNAME/SYSDEV_REGISTRY_PASSWORD, not both (log: %s)", logPath)
	}

	var clusterInfo *cluster.Inventory
	clusterError := ""
	if cfg.Kubernetes.KubeconfigPath != "" {
		service.Log.Event("INFO", "cluster-preflight", "Starting read-only Kubernetes discovery and authorization reviews", map[string]string{"context": cfg.Kubernetes.Context, "namespace": cfg.Kubernetes.Namespace})
		if err := service.Log.Error(); err != nil {
			return fmt.Errorf("required audit log is unavailable; stopping before Kubernetes API checks: %w", err)
		}
		ports := make([]int, 0, len(registry.Profile.NodePorts))
		for _, port := range registry.Profile.NodePorts {
			ports = append(ports, port.Port)
		}
		clusterCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		inv, err := cluster.Discover(clusterCtx, cfg.Kubernetes.KubeconfigPath, cfg.Kubernetes.Context, cfg.Kubernetes.Namespace, cluster.PreflightOptions{Namespace: cfg.Kubernetes.Namespace, SelectedWorkers: cfg.Kubernetes.SelectedWorkers, StorageClass: cfg.Kubernetes.StorageClass, NodePorts: ports, NodePortMin: cfg.Kubernetes.NodePortRangeMin, NodePortMax: cfg.Kubernetes.NodePortRangeMax})
		cancel()
		if err != nil {
			service.Log.Event("ERROR", "cluster-preflight", "Kubernetes read-only preflight failed", map[string]string{"error": err.Error()})
			clusterError = err.Error()
		} else {
			clusterInfo = &inv
			service.Log.Event("INFO", "cluster-preflight", "Kubernetes read-only preflight completed", map[string]string{"context": inv.Context, "cluster": inv.Cluster, "api_endpoint": inv.APIEndpoint, "version": inv.KubernetesVersion, "nodes": fmt.Sprint(len(inv.Nodes)), "permissions": fmt.Sprint(len(inv.Preflight.Permissions))})
		}
	}
	targetPlatforms := []registry.Platform{}
	if clusterInfo != nil && clusterInfo.Preflight.SelectedWorkerStatus.Status == "READY" {
		targetPlatforms = cluster.PlatformsForWorkers(*clusterInfo, clusterInfo.Preflight.SelectedWorkers)
	}
	images, err := service.CheckImages(ctx, cfg.Registry.Host, cfg.Registry.Project, cred, 1, targetPlatforms)
	if err != nil {
		return fmt.Errorf("registry validation failed: %w (log: %s)", err, logPath)
	}
	var archiveResult *imagearchive.Result
	archiveError := ""
	if cfg.OfflineBundlePath != "" {
		res, verifyErr := imagearchive.Verify(ctx, cfg.OfflineBundlePath, registry.Profile.Images)
		if verifyErr != nil {
			archiveError = verifyErr.Error()
			service.Log.Event("ERROR", "offline-bundle", "Offline image bundle verification failed", map[string]string{"error": archiveError})
		} else {
			for i := range res.Images {
				res.Images[i].CheckPlatforms(targetPlatforms)
			}
			archiveResult = &res
			service.Log.Event("INFO", "offline-bundle", "Offline image bundle verified", map[string]string{"bundle_id": res.BundleID, "images": fmt.Sprint(len(res.Images))})
		}
	}
	p := plan.BuildFull(plan.Input{Revision: 1, RegistryHost: cfg.Registry.Host, Project: cfg.Registry.Project, Images: images, Cluster: clusterInfo, ClusterError: clusterError, Archive: archiveResult, ArchiveError: archiveError})
	fmt.Printf("%s %s\n%s\nIntegrated read-only preflight | configuration schema %d\nLog: %s\n", domain.ApplicationName, version.Current, domain.Attribution, cfg.SchemaVersion, logPath)
	printInventory(p)
	if cmd == "validate" && len(p.Blockers) > 0 {
		return fmt.Errorf("preflight has %d blocker(s); no changes were made (log: %s)", len(p.Blockers), logPath)
	}
	return nil
}

func printInventory(p plan.Review) {
	fmt.Printf("\nCANDIDATE PROFILE %s v%s | WSO2 APIM %s / JDK %s | Keycloak %s / JDK %s | Oracle %s\n", p.Profile.ID, p.Profile.Version, p.Profile.WSO2, p.Profile.WSO2JDK, p.Profile.Keycloak, p.Profile.KeycloakJDK, p.Profile.Oracle)
	fmt.Printf("Registry %s | Project %s\n", p.Registry, p.Project)
	if p.Cluster != nil {
		fmt.Printf("Cluster %s | Context %s | API %s | Kubernetes %s | Namespace %s (%s)\n", p.Cluster.Cluster, p.Cluster.Context, p.Cluster.APIEndpoint, p.Cluster.KubernetesVersion, p.Cluster.Namespace, p.Cluster.Preflight.Namespace.Status)
		fmt.Printf("Selected workers (%s): %s\n", p.Cluster.Preflight.SelectedWorkerStatus.Status, strings.Join(p.Cluster.Preflight.SelectedWorkers, ", "))
		for _, n := range p.Cluster.Nodes {
			remaining := cluster.Remaining(n.Allocatable, n.ScheduledRequests)
			fmt.Printf("NODE %s worker=%t ready=%t schedulable=%t eligible=%t platform=%s/%s labels=%v taints=%v allocatable=%s CPU/%s memory reserved=%s CPU/%s memory remaining=%s CPU/%s memory\n", n.Name, n.Worker, n.Ready, n.Schedulable, n.Eligible, n.OS, n.Architecture, n.Labels, n.Taints, n.Allocatable["cpu"], n.Allocatable["memory"], n.ScheduledRequests["cpu"], n.ScheduledRequests["memory"], remaining["cpu"], remaining["memory"])
		}
		for _, perm := range p.Cluster.Preflight.Permissions {
			fmt.Printf("PERMISSION %s %s/%s namespace=%s: %s (%s)\n", perm.Verb, perm.Group, perm.Resource, perm.Namespace, perm.Status, perm.Reason)
		}
		fmt.Printf("STORAGE CLASS %s %s provisioner=%s binding=%s reclaim=%s topologies=%v | %s\n", p.Cluster.Preflight.StorageClass.Name, p.Cluster.Preflight.StorageClass.Status, p.Cluster.Preflight.StorageClass.Provisioner, p.Cluster.Preflight.StorageClass.VolumeBindingMode, p.Cluster.Preflight.StorageClass.ReclaimPolicy, p.Cluster.Preflight.StorageClass.AllowedTopologies, p.Cluster.Preflight.StorageClass.Deferred)
		fmt.Println("NODEPORT RANGE:", p.Cluster.Preflight.NodePortRange)
		for _, np := range p.Cluster.Preflight.NodePorts {
			fmt.Printf("NODEPORT %d: %s (%s)\n", np.Port, np.Status, np.Detail)
		}
		fmt.Printf("ONE-WORKER-LOSS CAPACITY %s\nASSUMPTIONS: %s\nLIMITATIONS: %s\n", p.Capacity.Status, p.Capacity.Assumptions, p.Capacity.Limitations)
		for _, s := range p.Capacity.Scenarios {
			fmt.Printf("LOSS %s: %s — %s; nodes used=%s\n", s.FailedWorker, s.Status, s.Detail, strings.Join(s.NodesUsed, ","))
		}
	}
	fmt.Println("\nIMAGE INVENTORY")
	fmt.Println("COMPONENT | SOURCE | DESTINATION | REMOTE | LOCAL | INTEGRITY | PLATFORM | CONFIG DIGEST | OCI MANIFEST DIGEST | OCI INDEX DIGEST | ARCHIVE SHA-256 | ACTION")
	for _, r := range p.ImageRows {
		fmt.Printf("%s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s\n", r.Component, r.SourceReference, r.DestinationReference, r.RemoteAvailability, r.LocalAvailability, r.Integrity, r.PlatformCompatibility, r.ImageConfigDigest, r.OCIManifestDigest, r.OCIIndexDigest, r.ArchiveSHA256, r.ProposedAction)
		if r.DockerArchiveManifestSHA256 != "" {
			fmt.Printf("  Docker archive manifest metadata SHA-256: %s\n", r.DockerArchiveManifestSHA256)
		}
		if len(r.LayerBlobDigests) > 0 {
			fmt.Printf("  Layer blob digests: %s\n", strings.Join(r.LayerBlobDigests, ", "))
		}
		if len(r.DiffIDs) > 0 {
			fmt.Printf("  Uncompressed layer DiffIDs: %s\n", strings.Join(r.DiffIDs, ", "))
		}
	}
	fmt.Println("\nREVIEW RESULTS")
	for _, x := range p.Operations {
		fmt.Println("PROPOSED ORDER (not executed):", x)
	}
	for _, x := range p.Imports {
		fmt.Println("REQUIRED IMAGE IMPORT (not executed):", x)
	}
	for _, x := range p.Passed {
		fmt.Println("PASSED:", x)
	}
	for _, x := range p.Blockers {
		fmt.Println("BLOCKER:", x)
	}
	for _, x := range p.Deferred {
		fmt.Println("DEFERRED/UNVERIFIED:", x)
	}
	for _, x := range p.Warnings {
		fmt.Println("WARNING:", x)
	}
	fmt.Println("No persistent Kubernetes resources, registry images, Oracle schemas, certificates, or identity configuration were created or modified.")
}

func printArchive(r imagearchive.Result) {
	fmt.Printf("OFFLINE BUNDLE %s | VERIFIED FILES %d\n", r.BundleID, len(r.Files))
	for _, im := range r.Images {
		fmt.Printf("%s | %s | %s | INTEGRITY=%s | ARCHIVE_SHA256=%s | CONFIG=%s | OCI_MANIFEST=%s | OCI_INDEX=%s | PLATFORMS=%v\n", im.Component, im.Format, im.DestinationReference, im.Integrity, im.Digests.ArchiveSHA256, im.Digests.ImageConfigDigest, im.Digests.OCIManifestDigest, im.Digests.OCIIndexDigest, im.Platforms)
		if len(im.Digests.LayerBlobDigests) > 0 {
			fmt.Println("  LAYER BLOB DIGESTS:", strings.Join(im.Digests.LayerBlobDigests, ", "))
		}
		if len(im.Digests.UncompressedLayerDiffIDs) > 0 {
			fmt.Println("  UNCOMPRESSED LAYER DIFFIDS:", strings.Join(im.Digests.UncompressedLayerDiffIDs, ", "))
		}
	}
}
