package preparation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
)

func fixtureConfig() config.File {
	return config.File{SchemaVersion: config.SchemaVersion, Registry: config.Registry{Host: "registry.example"}, Kubernetes: config.Kubernetes{Namespace: "wso2"}, Deployment: config.Deployment{ClusterDNSDomain: "cluster.local"}, Certificates: config.Certificates{Mode: "generate", APIMHostname: "apim.example.test", GatewayHostname: "api.example.test", KeycloakHostname: "sso.example.test"}, Oracle: config.Oracle{Host: "oracle.example", Port: 1521, ServiceName: "W2DB", SchemaMode: "prepared", APIMSchema: "WSO2_APIM", SharedSchema: "WSO2_SHARED", KeycloakSchema: "KEYCLOAK", APIMRuntimeAccount: "WSO2_APIM", SharedRuntimeAccount: "WSO2_SHARED", KeycloakRuntimeAccount: "KEYCLOAK"}}
}

func TestRunBuildsRepeatableRedactedPreparationAndReusesKey(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(root, "protected")
	firstOut := filepath.Join(root, "export-one")
	first, err := Run(context.Background(), Options{Config: fixtureConfig(), OutputDir: firstOut, StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	if !first.ReadOnly || len(first.Assets.SHA256) == 0 {
		t.Fatalf("incomplete report: %#v", first)
	}
	if first.Oracle.Status != "DEFERRED" {
		t.Fatalf("unexpected unauthenticated Oracle status: %s", first.Oracle.Status)
	}
	planBytes, err := os.ReadFile(filepath.Join(firstOut, "review.json"))
	if err != nil {
		t.Fatal(err)
	}
	assetBytes, err := os.ReadFile(filepath.Join(firstOut, "assets", "kubernetes", "resources.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(assetBytes), "password = \"canary") || strings.Contains(string(assetBytes), "BEGIN PRIVATE KEY") {
		t.Fatal("generated public assets contain secret material")
	}
	for _, name := range []string{"configuration.redacted.json", "review.json", "metadata.json", "checksums.sha256"} {
		if _, err := os.Stat(filepath.Join(firstOut, name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	// Same non-secret inputs and protected state produce the same deployment asset bytes.
	second, err := Run(context.Background(), Options{Config: fixtureConfig(), OutputDir: filepath.Join(root, "export-two"), StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	for n, d := range first.Assets.SHA256 {
		if second.Assets.SHA256[n] != d {
			t.Errorf("asset %s changed for identical inputs", n)
		}
	}
	key, err := os.ReadFile(filepath.Join(state, "wso2-shared-encryption-key.bin"))
	if err != nil || len(key) != 32 {
		t.Fatalf("protected key missing or invalid: %v", err)
	}
	if strings.Contains(string(planBytes), hex.EncodeToString(key)) {
		t.Fatal("protected key leaked into review")
	}
	for _, f := range []string{"certificates/apim-external.crt", "certificates/trust-root.crt"} {
		if _, err := os.Stat(filepath.Join(firstOut, f)); err != nil {
			t.Errorf("expected public cert %s: %v", f, err)
		}
	}
	if _, err := Run(context.Background(), Options{Config: fixtureConfig(), OutputDir: firstOut, StateDir: state}); err == nil {
		t.Fatal("existing public output was silently overwritten")
	}
}

func TestRunSeparatesProtectedStateFromOutput(t *testing.T) {
	root := t.TempDir()
	same := filepath.Join(root, "same")
	_, err := Run(context.Background(), Options{Config: fixtureConfig(), OutputDir: same, StateDir: same})
	if err == nil {
		t.Fatal("expected public/protected path collision rejection")
	}
}

func TestHashFixtureCanaryIsOnlyHashNotContent(t *testing.T) {
	root := t.TempDir()
	data := []byte("preparation fixture canary")
	if err := os.WriteFile(filepath.Join(root, "input.txt"), data, 0600); err != nil {
		t.Fatal(err)
	}
	m, err := hashTree(root)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if m["input.txt"] != hex.EncodeToString(sum[:]) {
		t.Fatal("wrong artifact hash")
	}
}
