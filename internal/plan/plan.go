package plan

import (
	"fmt"
	"strings"
	"time"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/cluster"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/imagearchive"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

type ImageRow struct {
	Component, SourceReference, DestinationReference                        string
	RemoteAvailability, LocalAvailability, Integrity, PlatformCompatibility string
	ArchiveFile                                                             string
	ImageConfigDigest, OCIManifestDigest, OCIIndexDigest                    string
	ArchiveSHA256, DockerArchiveManifestSHA256                              string
	LayerBlobDigests, DiffIDs                                               []string
	ProposedAction                                                          string
}

type Review struct {
	CreatedAt                            time.Time
	Revision                             uint64
	Registry, Project                    string
	Profile                              registry.ProfileDescriptor
	Cluster                              *cluster.Inventory
	Archive                              *imagearchive.Result
	Capacity                             cluster.CapacityAssessment
	Images                               []registry.Image
	ImageRows                            []ImageRow
	Imports                              []string
	Operations                           []string
	Passed, Deferred, Warnings, Blockers []string
}

type Input struct {
	Revision              uint64
	RegistryHost, Project string
	Images                []registry.Image
	Cluster               *cluster.Inventory
	ClusterError          string
	Archive               *imagearchive.Result
	ArchiveError          string
	Capacity              *cluster.CapacityAssessment
}

func Build(revision uint64, registryHost, project string, images []registry.Image, clusterInfo ...*cluster.Inventory) Review {
	var inv *cluster.Inventory
	if len(clusterInfo) > 0 {
		inv = clusterInfo[0]
	}
	return BuildFull(Input{Revision: revision, RegistryHost: registryHost, Project: project, Images: images, Cluster: inv})
}

func BuildFull(in Input) Review {
	p := Review{CreatedAt: time.Now(), Revision: in.Revision, Registry: in.RegistryHost, Project: in.Project, Profile: registry.Profile, Cluster: in.Cluster, Archive: in.Archive, Images: in.Images, Operations: []string{"1. Recheck registry manifest/platform coverage and archive hashes before import.", "2. Import only required, verified images after a separate explicit Apply approval.", "3. Create namespace and required Kubernetes resources in dependency order.", "4. Wait for release-verified readiness, then configure and verify identity integrations."}}
	if in.Capacity != nil {
		p.Capacity = *in.Capacity
	} else if in.Cluster != nil {
		p.Capacity = cluster.AssessOneWorkerLoss(*in.Cluster, in.Cluster.Preflight.SelectedWorkers, registry.Profile)
	}
	platforms := []registry.Platform{}
	if in.Cluster != nil {
		platforms = cluster.PlatformsForWorkers(*in.Cluster, in.Cluster.Preflight.SelectedWorkers)
	}
	archives := map[string]imagearchive.ImageEvidence{}
	if in.Archive != nil {
		for _, e := range in.Archive.Images {
			archives[e.Component] = e
		}
	}
	for _, im := range in.Images {
		row := ImageRow{Component: im.Component, SourceReference: im.SourceReference, DestinationReference: im.Reference, RemoteAvailability: string(im.Status), LocalAvailability: "NOT SUPPLIED", Integrity: "NOT CHECKED", PlatformCompatibility: im.PlatformStatus, ImageConfigDigest: im.ConfigDigest, LayerBlobDigests: append([]string(nil), im.LayerDigests...)}
		remotePlatformLabel := im.PlatformStatus
		remotePlatformCompatible := len(platforms) == 0 || im.PlatformStatus == "VERIFIED"
		if im.IndexDigest != "" || strings.Contains(im.MediaType, "image.index") || strings.Contains(im.MediaType, "manifest.list") {
			row.OCIIndexDigest = im.Digest
		} else {
			row.OCIManifestDigest = im.Digest
		}
		for _, pr := range im.Platforms {
			if pr.ManifestDigest != "" {
				row.OCIManifestDigest = prefer(row.OCIManifestDigest, pr.ManifestDigest)
			}
			if pr.ConfigDigest != "" {
				row.ImageConfigDigest = prefer(row.ImageConfigDigest, pr.ConfigDigest)
			}
			row.LayerBlobDigests = append(row.LayerBlobDigests, pr.LayerDigests...)
		}
		if len(platforms) == 0 && row.PlatformCompatibility == "" {
			row.PlatformCompatibility = "NOT CHECKED (no selected worker platform)"
		}
		if len(im.Platforms) > 0 {
			covered := true
			for _, p := range im.Platforms {
				if p.Status != "VERIFIED" {
					covered = false
				}
			}
			if covered {
				row.PlatformCompatibility = "COMPATIBLE"
				remotePlatformCompatible = true
			} else {
				row.PlatformCompatibility = "INCOMPLETE"
				remotePlatformCompatible = false
			}
		}
		if local, ok := archives[im.Component]; ok {
			row.LocalAvailability = "PRESENT: " + local.Format
			row.Integrity = local.Integrity
			row.ArchiveFile = local.ArchiveFile
			row.ArchiveSHA256 = local.Digests.ArchiveSHA256
			row.ImageConfigDigest = prefer(row.ImageConfigDigest, local.Digests.ImageConfigDigest)
			row.DockerArchiveManifestSHA256 = local.Digests.DockerArchiveManifestSHA256
			row.LayerBlobDigests = append(row.LayerBlobDigests, local.Digests.LayerBlobDigests...)
			row.DiffIDs = append([]string(nil), local.Digests.UncompressedLayerDiffIDs...)
			local.CheckPlatforms(platforms)
			if len(platforms) == 0 {
				row.PlatformCompatibility = "REMOTE " + defaultLabel(remotePlatformLabel, "UNVERIFIED") + "; LOCAL UNVERIFIED (selected workers required)"
			} else if len(local.MissingPlatforms) == 0 {
				row.PlatformCompatibility = "REMOTE " + defaultLabel(remotePlatformLabel, "UNVERIFIED") + "; LOCAL COMPATIBLE"
			} else {
				row.PlatformCompatibility = "REMOTE " + defaultLabel(remotePlatformLabel, "UNVERIFIED") + "; LOCAL INCOMPATIBLE/INCOMPLETE"
			}
		}
		if in.ArchiveError != "" {
			row.LocalAvailability = "INVALID/UNVERIFIED"
			row.Integrity = "FAILED"
		}
		switch {
		case im.Status == registry.Found && len(platforms) == 0:
			row.ProposedAction = "USE REGISTRY IMAGE; selected worker platform remains unverified"
		case im.Status == registry.Found && remotePlatformCompatible:
			row.ProposedAction = "USE REGISTRY IMAGE; no local archive or image push is needed"
		case im.Status == registry.Missing && row.Integrity == "VERIFIED" && len(platforms) > 0 && strings.Contains(row.PlatformCompatibility, "LOCAL COMPATIBLE"):
			row.ProposedAction = "READY TO IMPORT after destination recheck and explicit Apply"
		case im.Status == registry.Missing && row.Integrity == "VERIFIED":
			row.ProposedAction = "BLOCKED: local archive verified, but destination/platform conditions are incomplete"
		case im.Status == registry.Missing:
			row.ProposedAction = "BLOCKED: missing/not-visible remote reference requires a verified, platform-compatible local archive"
		case im.Status == registry.Found:
			row.ProposedAction = "BLOCKED: remote image platform coverage is not verified; an existing tag will not be overwritten by this plan"
		default:
			row.ProposedAction = "BLOCKED: registry result must be resolved; local data does not establish destination absence"
		}
		p.ImageRows = append(p.ImageRows, row)
	}

	if in.RegistryHost == "" {
		p.Blockers = append(p.Blockers, "Registry host is not configured.")
	}
	if in.Cluster == nil {
		if in.ClusterError != "" {
			p.Blockers = append(p.Blockers, "Kubernetes discovery/preflight failed: "+in.ClusterError)
		} else {
			p.Deferred = append(p.Deferred, "Kubernetes identity, namespace, permissions, nodes, storage, networking, and capacity were not checked.")
		}
	} else {
		appendClusterChecks(&p, *in.Cluster)
	}
	for _, row := range p.ImageRows {
		if strings.HasPrefix(row.ProposedAction, "READY TO IMPORT") {
			p.Passed = append(p.Passed, row.Component+" has verified offline image data and platform coverage for import planning")
			p.Imports = append(p.Imports, row.Component+" => "+row.DestinationReference+" from "+row.ArchiveFile)
		} else if strings.HasPrefix(row.ProposedAction, "USE REGISTRY IMAGE") {
			p.Passed = append(p.Passed, row.Component+" has a readable destination image")
			if strings.Contains(row.ProposedAction, "unverified") {
				p.Deferred = append(p.Deferred, row.Component+" worker platform coverage is unverified")
			}
		} else {
			p.Blockers = append(p.Blockers, row.Component+": "+row.ProposedAction)
		}
		if row.Integrity == "VERIFIED" {
			p.Passed = append(p.Passed, row.Component+" archive file SHA-256 and image content references verified")
		}
	}
	if in.ArchiveError != "" {
		p.Deferred = append(p.Deferred, "Supplied offline bundle failed verification: "+in.ArchiveError)
	} else if in.Archive == nil {
		p.Deferred = append(p.Deferred, "No local image bundle was supplied. This is acceptable when all required registry images and platforms are available.")
	}
	p.Warnings = append(p.Warnings, "Registry access from this workstation does not establish node pull credentials, runtime CA trust, or registry write permission.", "StorageClass metadata does not prove access modes, successful volume provisioning, or node-loss failover.", "The one-worker-loss result is a conservative bounded CPU/memory placement preflight, not full scheduler simulation, HA certification, or a 400 TPS result.", "Single-control-plane and external-single-Oracle availability remain separate infrastructure limitations.", "SelfSubjectAccessReview covers the currently modeled namespace, deployment, Service, Secret and PVC actions; final resource types and verbs depend on future workload manifests.", "WSO2 4.7.0 and Keycloak 26.8.1 remain candidate pins pending exact-release integration qualification.", "This review is non-mutating. Apply, import, deployment, database, certificate, and identity operations are unavailable.")
	return p
}

func defaultLabel(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func appendClusterChecks(p *Review, c cluster.Inventory) {
	p.Passed = append(p.Passed, fmt.Sprintf("Read cluster %s context %s at %s, Kubernetes %s", c.Cluster, c.Context, c.APIEndpoint, c.KubernetesVersion))
	if c.Preflight.SelectedWorkerStatus.Status == "READY" {
		p.Passed = append(p.Passed, "Selected workers are Ready and schedulable: "+join(c.Preflight.SelectedWorkers))
	} else {
		p.Blockers = append(p.Blockers, "Selected worker eligibility: "+c.Preflight.SelectedWorkerStatus.Detail)
	}
	if c.Preflight.Namespace.Status == "EXISTS" {
		p.Passed = append(p.Passed, "Target namespace exists: "+c.Namespace)
	} else if c.Preflight.Namespace.Status == "MISSING" {
		p.Deferred = append(p.Deferred, "Target namespace is absent and will need creation after Apply.")
		if !permissionAllowed(c.Preflight.Permissions, "create", "", "namespaces", "") {
			p.Blockers = append(p.Blockers, "Namespace is missing and namespace creation permission is not allowed or remains indeterminate.")
		}
	} else {
		p.Blockers = append(p.Blockers, "Namespace status is indeterminate: "+c.Preflight.Namespace.Detail)
	}
	for _, perm := range c.Preflight.Permissions {
		if perm.Status == "DENIED" {
			p.Blockers = append(p.Blockers, fmt.Sprintf("Permission denied: %s %s/%s namespace=%s", perm.Verb, perm.Group, perm.Resource, perm.Namespace))
		} else if perm.Status == "INDETERMINATE" {
			p.Blockers = append(p.Blockers, fmt.Sprintf("Permission unverified: %s %s/%s: %s", perm.Verb, perm.Group, perm.Resource, perm.Reason))
		}
	}
	if c.Preflight.StorageClass.Status == "EXISTS" {
		p.Passed = append(p.Passed, "Selected StorageClass exists: "+c.Preflight.StorageClass.Name)
		p.Deferred = append(p.Deferred, c.Preflight.StorageClass.Deferred)
	} else if c.Preflight.StorageClass.Status == "UNSELECTED" {
		p.Deferred = append(p.Deferred, c.Preflight.StorageClass.Deferred)
	} else {
		p.Blockers = append(p.Blockers, "StorageClass "+c.Preflight.StorageClass.Name+" status: "+c.Preflight.StorageClass.Status)
	}
	for _, np := range c.Preflight.NodePorts {
		switch np.Status {
		case "AVAILABLE":
			p.Passed = append(p.Passed, fmt.Sprintf("NodePort %d: no current conflict observed", np.Port))
		case "CONFLICT", "OUT OF RANGE":
			p.Blockers = append(p.Blockers, fmt.Sprintf("NodePort %d %s: %s", np.Port, np.Status, np.Detail))
		default:
			p.Deferred = append(p.Deferred, fmt.Sprintf("NodePort %d conflict check: %s", np.Port, np.Detail))
		}
	}
	if c.PodRequestsStatus != "AVAILABLE" {
		p.Blockers = append(p.Blockers, "Existing pod resource requests are incomplete: "+c.PodRequestsStatus)
	}
	if p.Capacity.Status == "FIT UNDER MODEL" {
		p.Passed = append(p.Passed, "One-worker-loss CPU/memory placement preflight fits under the stated model")
	} else {
		p.Blockers = append(p.Blockers, "One-worker-loss capacity is "+p.Capacity.Status)
	}
	for _, s := range p.Capacity.Scenarios {
		p.Deferred = append(p.Deferred, fmt.Sprintf("Worker loss %s: %s (%s)", s.FailedWorker, s.Status, s.Detail))
	}
	if c.Preflight.ServiceInventory.Status != "AVAILABLE" {
		p.Deferred = append(p.Deferred, "Existing Service inventory/NodePort conflicts are unknown: "+c.Preflight.ServiceInventory.Detail)
	}
}

func permissionAllowed(perms []cluster.Permission, verb, group, resourceName, namespace string) bool {
	for _, p := range perms {
		if p.Verb == verb && p.Group == group && p.Resource == resourceName && p.Namespace == namespace && p.Status == "ALLOWED" {
			return true
		}
	}
	return false
}
func join(in []string) string {
	if len(in) == 0 {
		return "none"
	}
	out := ""
	for i, s := range in {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
func prefer(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func ImageAction(status registry.Status) string {
	switch status {
	case registry.Found:
		return "Use readable destination image after platform verification"
	case registry.Missing:
		return "Verified offline archive required before import can be planned"
	default:
		return "Resolve registry access or result before planning import"
	}
}
