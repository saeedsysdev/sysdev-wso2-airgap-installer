package plan

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/cluster"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/imagearchive"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

func TestRegistryOnlyPlanDoesNotRequireArchive(t *testing.T) {
	images := make([]registry.Image, 0, len(registry.Profile.Images))
	for _, required := range registry.Profile.Images {
		images = append(images, registry.Image{Component: required.Component, SourceReference: required.SourceReference, Reference: "registry.example/" + required.Repository + ":" + required.Tag, Status: registry.Found})
	}
	review := BuildFull(Input{RegistryHost: "registry.example", Images: images})
	for _, row := range review.ImageRows {
		if row.ProposedAction != "USE REGISTRY IMAGE; selected worker platform remains unverified" {
			t.Fatalf("registry-only plan unexpectedly requires local archive: %#v", row)
		}
	}
	if len(review.Deferred) == 0 {
		t.Fatal("missing platform qualification was not shown as deferred")
	}
}

func TestVerifiedLocalArchiveSatisfiesMissingImageForSelectedPlatform(t *testing.T) {
	root := writeDockerBundle(t)
	archive, err := imagearchive.Verify(t.Context(), root, registry.Profile.Images)
	if err != nil {
		t.Fatal(err)
	}
	clusterInfo := cluster.Inventory{PodRequestsStatus: "AVAILABLE", Preflight: cluster.PreflightResults{SelectedWorkers: []string{"worker-a", "worker-b", "worker-c"}, SelectedWorkerStatus: cluster.Check{Status: "READY"}}, Nodes: []cluster.Node{
		planWorker("worker-a"), planWorker("worker-b"), planWorker("worker-c"),
	}}
	for i := range archive.Images {
		archive.Images[i].CheckPlatforms(cluster.PlatformsForWorkers(clusterInfo, clusterInfo.Preflight.SelectedWorkers))
	}
	images := make([]registry.Image, 0, len(registry.Profile.Images))
	missing := registry.Profile.Images[0]
	for i, required := range registry.Profile.Images {
		status := registry.Found
		if i == 0 {
			status = registry.Missing
		}
		images = append(images, registry.Image{Component: required.Component, SourceReference: required.SourceReference, Reference: "registry.example/" + required.Repository + ":" + required.Tag, Status: status, PlatformStatus: "VERIFIED", Platforms: []registry.PlatformEvidence{{Platform: registry.Platform{OS: "linux", Architecture: "amd64"}, Status: "VERIFIED"}}})
	}
	review := BuildFull(Input{RegistryHost: "registry.example", Images: images, Cluster: &clusterInfo, Archive: &archive})
	for _, row := range review.ImageRows {
		if row.Component == missing.Component {
			if row.Integrity != "VERIFIED" || row.ProposedAction != "READY TO IMPORT after destination recheck and explicit Apply" {
				t.Fatalf("verified fixture was not made import-ready: %#v", row)
			}
			if row.ArchiveSHA256 == "" || row.ImageConfigDigest == "" || row.DockerArchiveManifestSHA256 == "" || row.OCIManifestDigest != "" || row.OCIIndexDigest != "" {
				t.Fatalf("archive digest categories were not preserved in review: %#v", row)
			}
			if len(review.Imports) != 1 || !strings.Contains(review.Imports[0], row.DestinationReference) {
				t.Fatalf("required import plan was not summarized explicitly: %#v", review.Imports)
			}
			return
		}
	}
	t.Fatal("missing image component absent from review")
}

func TestMissingRemoteImageWithoutVerifiedPlatformArchiveIsBlocked(t *testing.T) {
	required := registry.Profile.Images[0]
	image := registry.Image{Component: required.Component, SourceReference: required.SourceReference, Reference: "registry.example/" + required.Repository + ":" + required.Tag, Status: registry.Missing}
	review := BuildFull(Input{RegistryHost: "registry.example", Images: []registry.Image{image}})
	if review.ImageRows[0].ProposedAction == "READY TO IMPORT after destination recheck and explicit Apply" {
		t.Fatal("missing archive/platform evidence was incorrectly import-ready")
	}
}

func planWorker(name string) cluster.Node {
	return cluster.Node{Name: name, OS: "linux", Architecture: "amd64", Worker: true, Ready: true, Schedulable: true, Eligible: true, Allocatable: map[string]string{"cpu": "64", "memory": "256Gi"}, ScheduledRequests: map[string]string{}}
}

func writeDockerBundle(t *testing.T) string {
	t.Helper()
	required := registry.Profile.Images[0]
	dest := required.Repository + ":" + required.Tag
	layer := []byte("minimal docker layer payload")
	sum := func(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }
	config, _ := json.Marshal(map[string]any{"os": "linux", "architecture": "amd64", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{sum(layer)}}})
	manifest, _ := json.Marshal([]map[string]any{{"Config": "config.json", "RepoTags": []string{dest}, "Layers": []string{"layer/layer.tar"}}})
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for name, body := range map[string][]byte{"manifest.json": manifest, "config.json": config, "layer/layer.tar": layer} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	archivePath := filepath.Join(root, "images", "apim.tar")
	if err := os.MkdirAll(filepath.Dir(archivePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, raw.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	bundleHash := sha256.Sum256(raw.Bytes())
	bundle, _ := json.Marshal(imagearchive.BundleManifest{SchemaVersion: 1, BundleID: "local-fixture", Files: []imagearchive.FileRecord{{Path: "images/apim.tar", Size: int64(raw.Len()), SHA256: hex.EncodeToString(bundleHash[:])}}, Images: []imagearchive.ImageRecord{{Component: required.Component, SourceReference: required.SourceReference, DestinationRepository: required.Repository, DestinationTag: required.Tag, ArchiveFile: "images/apim.tar", Format: "docker-save"}}})
	if err := os.WriteFile(filepath.Join(root, "bundle.json"), bundle, 0600); err != nil {
		t.Fatal(err)
	}
	return root
}
