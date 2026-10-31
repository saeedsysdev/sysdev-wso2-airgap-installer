package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/app"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/auditlog"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/imagearchive"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

func TestNonInteractivePlanUsesRealRegistryProviderWithoutMutation(t *testing.T) {
	const body = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:fixture","size":1},"layers":[]}`
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/manifests/") {
			t.Errorf("unexpected registry path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	caPath := filepath.Join(t.TempDir(), "registry-ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(mustRead(t, caPath)) {
		t.Fatal("fixture CA did not parse")
	}
	t.Setenv("SYSDEV_REGISTRY_CA_BUNDLE", caPath)
	t.Setenv("SYSDEV_REGISTRY_USERNAME", "")
	t.Setenv("SYSDEV_REGISTRY_PASSWORD", "")
	t.Setenv("SYSDEV_REGISTRY_TOKEN", "")
	log, err := auditlog.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	service := app.New(registry.NewClient(), log)
	cfg := config.File{SchemaVersion: config.SchemaVersion, Registry: config.Registry{Host: strings.TrimPrefix(server.URL, "https://"), Project: "team"}}
	if err := runConfigured(context.Background(), "plan", service, cfg, log.Path()); err != nil {
		t.Fatal(err)
	}
}

func TestSubcommandHelpIsSuccessful(t *testing.T) {
	if err := run([]string{"plan", "--help"}); err != nil {
		t.Fatalf("subcommand help returned an error: %v", err)
	}
}

func TestVerifyArchiveCommandRunsRealBundleVerifier(t *testing.T) {
	bundle := makeCommandDockerBundle(t)
	if err := run([]string{"verify-archive", "--bundle", bundle, "--log-dir", t.TempDir()}); err != nil {
		t.Fatalf("verify-archive command rejected a valid local fixture: %v", err)
	}
}

func makeCommandDockerBundle(t *testing.T) string {
	t.Helper()
	required := registry.Profile.Images[0]
	sum := func(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
	layer := []byte("fixture layer")
	config, _ := json.Marshal(map[string]any{"os": "linux", "architecture": "amd64", "rootfs": map[string]any{"type": "layers", "diff_ids": []string{"sha256:" + sum(layer)}}})
	manifest, _ := json.Marshal([]map[string]any{{"Config": "config.json", "RepoTags": []string{required.Repository + ":" + required.Tag}, "Layers": []string{"layer/layer.tar"}}})
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	for name, data := range map[string][]byte{"manifest.json": manifest, "config.json": config, "layer/layer.tar": layer} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
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
	if err := os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(archive.Bytes())
	data, _ := json.Marshal(imagearchive.BundleManifest{SchemaVersion: 1, BundleID: "cli-fixture", Files: []imagearchive.FileRecord{{Path: "images/apim.tar", Size: int64(archive.Len()), SHA256: hex.EncodeToString(h[:])}}, Images: []imagearchive.ImageRecord{{Component: required.Component, SourceReference: required.SourceReference, DestinationRepository: required.Repository, DestinationTag: required.Tag, ArchiveFile: "images/apim.tar", Format: "docker-save"}}})
	if err := os.WriteFile(filepath.Join(root, "bundle.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
