package imagearchive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

func sum(b []byte) string { h := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(h[:]) }
func putBlob(files map[string][]byte, b []byte) ociDescriptor {
	d := ociDescriptor{MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: sum(b), Size: int64(len(b))}
	files[blobPath(d.Digest)] = b
	return d
}

func makeOCI(t *testing.T, platforms ...Platform) []byte {
	t.Helper()
	files := map[string][]byte{"oci-layout": []byte(`{"imageLayoutVersion":"1.0.0"}`)}
	layerRaw := []byte("fixture-layer-tar-content")
	var layerGz bytes.Buffer
	zw := gzip.NewWriter(&layerGz)
	_, _ = zw.Write(layerRaw)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	layer := layerGz.Bytes()
	var manifests []ociDescriptor
	for _, p := range platforms {
		cfg := map[string]any{"os": p.OS, "architecture": p.Architecture, "variant": p.Variant, "rootfs": map[string]any{"type": "layers", "diff_ids": []string{sum(layerRaw)}}}
		cfgBytes, _ := json.Marshal(cfg)
		cfgDesc := putBlob(files, cfgBytes)
		cfgDesc.MediaType = "application/vnd.oci.image.config.v1+json"
		layerDesc := ociDescriptor{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: sum(layer), Size: int64(len(layer))}
		files[blobPath(layerDesc.Digest)] = layer
		manBytes, _ := json.Marshal(ociManifest{SchemaVersion: 2, Config: cfgDesc, Layers: []ociDescriptor{layerDesc}})
		md := putBlob(files, manBytes)
		md.MediaType = "application/vnd.oci.image.manifest.v1+json"
		md.Platform = &struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
			Variant      string `json:"variant"`
		}{p.OS, p.Architecture, p.Variant}
		manifests = append(manifests, md)
	}
	indexBytes, _ := json.Marshal(ociIndex{SchemaVersion: 2, Manifests: manifests})
	indexDesc := putBlob(files, indexBytes)
	indexDesc.MediaType = "application/vnd.oci.image.index.v1+json"
	indexDesc.Annotations = map[string]string{"org.opencontainers.image.ref.name": "apim-acp-oracle:4.7.0-r1"}
	rootIndex, _ := json.Marshal(ociIndex{SchemaVersion: 2, Manifests: []ociDescriptor{indexDesc}})
	files["index.json"] = rootIndex
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for name, b := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(b)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(b); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func makeDockerSave(t *testing.T, platforms ...Platform) []byte {
	t.Helper()
	layer := []byte("docker-image-save-layer-tar")
	if len(platforms) == 0 {
		platforms = []Platform{{OS: "linux", Architecture: "amd64"}}
	}
	images := make([]dockerImage, 0, len(platforms))
	files := map[string][]byte{"layer/layer.tar": layer}
	for i, p := range platforms {
		cfg := map[string]any{"os": p.OS, "architecture": p.Architecture, "variant": p.Variant, "rootfs": map[string]any{"type": "layers", "diff_ids": []string{sum(layer)}}}
		configBytes, _ := json.Marshal(cfg)
		configPath := fmt.Sprintf("config-%d.json", i)
		files[configPath] = configBytes
		images = append(images, dockerImage{Config: configPath, RepoTags: []string{"apim-acp-oracle:4.7.0-r1"}, Layers: []string{"layer/layer.tar"}})
	}
	manifest, _ := json.Marshal(images)
	files["manifest.json"] = manifest
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestVerifySinglePlatformOCIArchive(t *testing.T) {
	root := writeBundle(t, makeOCI(t, Platform{OS: "linux", Architecture: "amd64"}), "oci-layout")
	got, err := Verify(context.Background(), root, registry.Profile.Images)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Images[0].Platforms) != 1 || got.Images[0].Platforms[0].OS != "linux" || got.Images[0].Platforms[0].Architecture != "amd64" {
		t.Fatalf("single platform coverage was not detected: %#v", got.Images[0].Platforms)
	}
}

func TestVerifyDockerSaveMultiPlatformEntriesWithSharedTag(t *testing.T) {
	root := writeBundle(t, makeDockerSave(t, Platform{OS: "linux", Architecture: "amd64"}, Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}), "docker-save")
	got, err := Verify(context.Background(), root, registry.Profile.Images)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Images[0].Platforms) != 2 {
		t.Fatalf("Docker save multi-platform metadata was rejected or lost: %#v", got.Images[0].Platforms)
	}
}

func writeBundle(t *testing.T, archive []byte, format string) string {
	t.Helper()
	root := t.TempDir()
	archivePath := filepath.Join(root, "images", "acp.tar")
	if err := os.MkdirAll(filepath.Dir(archivePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, archive, 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(archive)
	m := BundleManifest{SchemaVersion: 1, BundleID: "fixture", Files: []FileRecord{{Path: "images/acp.tar", Size: int64(len(archive)), SHA256: hex.EncodeToString(h[:])}}, Images: []ImageRecord{{Component: "API Control Plane and resident Key Manager", SourceReference: "docker.io/wso2/wso2am-acp:4.7.0", DestinationRepository: "apim-acp-oracle", DestinationTag: "4.7.0-r1", ArchiveFile: "images/acp.tar", Format: format}}}
	b, _ := json.Marshal(m)
	if err := os.WriteFile(filepath.Join(root, "bundle.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestVerifyOCIIndexDigestsAndPlatformsAreDistinct(t *testing.T) {
	root := writeBundle(t, makeOCI(t, Platform{OS: "linux", Architecture: "amd64"}, Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}), "oci-layout")
	got, err := Verify(context.Background(), root, registry.Profile.Images)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Images) != 1 {
		t.Fatalf("unexpected image results: %#v", got)
	}
	e := got.Images[0]
	if e.Integrity != "VERIFIED" || len(e.Platforms) != 2 || e.Digests.OCIIndexDigest == "" || e.Digests.OCIManifestDigest == "" || e.Digests.ImageConfigDigest == "" || len(e.Digests.LayerBlobDigests) != 2 || len(e.Digests.UncompressedLayerDiffIDs) != 2 {
		t.Fatalf("incomplete digest/platform evidence: %#v", e)
	}
	if e.Digests.OCIIndexDigest == e.Digests.OCIManifestDigest || e.Digests.ImageConfigDigest == e.Digests.OCIManifestDigest || e.Digests.LayerBlobDigests[0] == e.Digests.UncompressedLayerDiffIDs[0] {
		t.Fatalf("digest types were conflated: %#v", e.Digests)
	}
	e.CheckPlatforms([]registry.Platform{{OS: "linux", Architecture: "amd64"}, {OS: "windows", Architecture: "amd64"}})
	if len(e.CompatiblePlatforms) != 1 || len(e.MissingPlatforms) != 1 {
		t.Fatalf("wrong platform coverage: %#v", e)
	}
}

func TestVerifyDockerSaveArchiveKeepsOCIManifestDigestUnknown(t *testing.T) {
	root := writeBundle(t, makeDockerSave(t), "docker-save")
	got, err := Verify(context.Background(), root, registry.Profile.Images)
	if err != nil {
		t.Fatal(err)
	}
	e := got.Images[0]
	if e.Integrity != "VERIFIED" || len(e.Platforms) != 1 || e.Platforms[0].Architecture != "amd64" {
		t.Fatalf("Docker archive did not validate: %#v", e)
	}
	if e.Digests.ImageConfigDigest == "" || e.Digests.DockerArchiveManifestSHA256 == "" || e.Digests.OCIManifestDigest != "" || e.Digests.OCIIndexDigest != "" {
		t.Fatalf("Docker metadata was misreported as OCI digest: %#v", e.Digests)
	}
}

func TestRejectsBundleHashAndMissingReferencedBlob(t *testing.T) {
	archive := makeOCI(t, Platform{OS: "linux", Architecture: "amd64"})
	root := writeBundle(t, archive, "oci-layout")
	if err := os.WriteFile(filepath.Join(root, "images", "acp.tar"), append(archive, 0), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), root, registry.Profile.Images); err == nil {
		t.Fatal("accepted corrupted bundle file")
	}
	root = writeBundle(t, makeOCI(t, Platform{OS: "linux", Architecture: "amd64"}), "oci-layout")
	archivePath := filepath.Join(root, "images", "acp.tar")
	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(f)
	var kept []tar.Header
	var bodies [][]byte
	for {
		h, e := tr.Next()
		if e != nil {
			break
		}
		b, _ := ioReadAll(tr)
		kept = append(kept, *h)
		bodies = append(bodies, b)
	}
	f.Close()
	var modified bytes.Buffer
	tw := tar.NewWriter(&modified)
	removed := false
	for i := range kept {
		h := &kept[i]
		if !removed && h.Name != "index.json" && h.Name != "oci-layout" && h.Name != "blobs/sha256/"+strings.Repeat("0", 64) {
			if strings.HasPrefix(h.Name, "blobs/sha256/") {
				removed = true
				continue
			}
		}
		_ = tw.WriteHeader(h)
		_, _ = tw.Write(bodies[i])
	}
	_ = tw.Close()
	root = writeBundle(t, modified.Bytes(), "oci-layout")
	if _, err := Verify(context.Background(), root, registry.Profile.Images); err == nil {
		t.Fatal("accepted missing referenced content")
	}
}

func TestRejectsUnsafeTarAndBundlePaths(t *testing.T) {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	payload := []byte("bad")
	_ = tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0600, Size: int64(len(payload)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(payload)
	_ = tw.Close()
	root := writeBundle(t, b.Bytes(), "oci-layout")
	if _, err := Verify(context.Background(), root, registry.Profile.Images); err == nil {
		t.Fatal("accepted unsafe archive member path")
	}
	root = writeBundle(t, makeOCI(t, Platform{OS: "linux", Architecture: "amd64"}), "oci-layout")
	manifest := BundleManifest{SchemaVersion: 1, BundleID: "bad", Files: []FileRecord{{Path: "../escape", Size: 0, SHA256: fmt.Sprintf("%064x", 0)}}, Images: []ImageRecord{}}
	data, _ := json.Marshal(manifest)
	_ = os.WriteFile(filepath.Join(root, "bundle.json"), data, 0600)
	if _, err := Verify(context.Background(), root, registry.Profile.Images); err == nil {
		t.Fatal("accepted unsafe bundle path")
	}
}

func TestRejectsUnmappedOrUnsupportedStructures(t *testing.T) {
	root := writeBundle(t, makeOCI(t, Platform{OS: "linux", Architecture: "amd64"}), "unrecognized")
	if _, err := Verify(context.Background(), root, registry.Profile.Images); err == nil {
		t.Fatal("accepted unsupported archive format")
	}
	if _, err := Verify(context.Background(), root, nil); err == nil {
		t.Fatal("accepted mapping without profile component")
	}
}

func TestRejectsUnsupportedBundleSchemaAndRecordedSize(t *testing.T) {
	root := writeBundle(t, makeOCI(t, Platform{OS: "linux", Architecture: "amd64"}), "oci-layout")
	path := filepath.Join(root, "bundle.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest BundleManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = SchemaVersion + 1
	data, _ = json.Marshal(manifest)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), root, registry.Profile.Images); err == nil {
		t.Fatal("accepted unsupported bundle schema version")
	}

	root = writeBundle(t, makeOCI(t, Platform{OS: "linux", Architecture: "amd64"}), "oci-layout")
	path = filepath.Join(root, "bundle.json")
	data, _ = os.ReadFile(path)
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Files[0].Size++
	data, _ = json.Marshal(manifest)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), root, registry.Profile.Images); err == nil {
		t.Fatal("accepted a mismatched recorded archive size")
	}
}

func TestRejectsUnsafeTarTypesAndHonorsCanceledContext(t *testing.T) {
	for _, typ := range []byte{tar.TypeSymlink, tar.TypeLink, tar.TypeFifo} {
		var b bytes.Buffer
		tw := tar.NewWriter(&b)
		if err := tw.WriteHeader(&tar.Header{Name: "unsafe-entry", Linkname: "outside", Mode: 0600, Typeflag: typ}); err != nil {
			t.Fatal(err)
		}
		_ = tw.Close()
		root := writeBundle(t, b.Bytes(), "oci-layout")
		if _, err := Verify(context.Background(), root, registry.Profile.Images); err == nil {
			t.Fatalf("accepted unsafe tar type %d", typ)
		}
	}
	root := writeBundle(t, makeOCI(t, Platform{OS: "linux", Architecture: "amd64"}), "oci-layout")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Verify(ctx, root, registry.Profile.Images); err == nil {
		t.Fatal("ignored canceled archive verification")
	}
}

func ioReadAll(r io.Reader) ([]byte, error) { return io.ReadAll(r) }
