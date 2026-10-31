package deploymentassets

import (
	"bytes"
	"strings"
	"testing"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
)

func TestRenderProducesStableSecretReferencedProfileAndTrafficManagerEndpoints(t *testing.T) {
	c := config.File{SchemaVersion: config.SchemaVersion, Registry: config.Registry{Host: "registry.local:5000", Project: "wso2"}, Kubernetes: config.Kubernetes{Namespace: "wso2"}, Deployment: config.Deployment{ClusterDNSDomain: "cluster.local"}, Oracle: config.Oracle{APIMRuntimeAccount: "WSO2_APIM", SharedRuntimeAccount: "WSO2_SHARED", KeycloakRuntimeAccount: "KEYCLOAK", APIMSchema: "WSO2_APIM", SharedSchema: "WSO2_SHARED", KeycloakSchema: "KEYCLOAK"}, Certificates: config.Certificates{APIMHostname: "apim.example.test", GatewayHostname: "api.example.test", KeycloakHostname: "sso.example.test"}, DistributedThrottle: config.DistributedThrottle{Host: "redis.example", Port: 6379, Type: "redis", ReplicationMode: true}}
	a, err := Render(Input{Config: c})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Render(Input{Config: c})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Files) != len(b.Files) {
		t.Fatal("asset file count changed")
	}
	for n, x := range a.Files {
		if !bytes.Equal(x, b.Files[n]) {
			t.Errorf("%s is not deterministic", n)
		}
	}
	manifest := string(a.Files["kubernetes/resources.yaml"])
	for _, needle := range []string{"kind: StatefulSet", "name: apim-tm-1", "name: apim-tm-2", "name: keycloak", "name: registry-pull"} {
		if !strings.Contains(manifest, needle) {
			t.Errorf("manifest missing %q", needle)
		}
	}
	for n, data := range a.Files {
		if strings.Contains(string(data), "BEGIN PRIVATE KEY") || strings.Contains(string(data), "canary-password") {
			t.Errorf("%s contains secret material", n)
		}
	}
	for _, name := range []string{"apim-cp", "apim-km", "apim-tm-1", "apim-tm-2", "apim-gateway"} {
		text := string(a.Files["configs/"+name+"/deployment.toml"])
		if !strings.Contains(text, "$env{APIM_ENCRYPTION_KEY}") {
			t.Errorf("%s missing shared encryption key reference", name)
		}
	}
	tm := string(a.Files["configs/apim-cp/deployment.toml"])
	for _, needle := range []string{"apim-tm-1.wso2.svc.cluster.local:9611", "apim-tm-2.wso2.svc.cluster.local:9611", "apim-tm-1.wso2.svc.cluster.local:9711", "apim-tm-2.wso2.svc.cluster.local:9711"} {
		if !strings.Contains(tm, needle) {
			t.Errorf("TM communication missing %s", needle)
		}
	}
	if len(a.Bindings) == 0 {
		t.Fatal("expected explicit Stage 4 deferred bindings")
	}
}

func TestRenderBlocksUnreplicatedDistributedThrottle(t *testing.T) {
	c := config.File{Registry: config.Registry{Host: "registry.local"}, Kubernetes: config.Kubernetes{Namespace: "wso2"}, Certificates: config.Certificates{APIMHostname: "apim.example.test", GatewayHostname: "api.example.test", KeycloakHostname: "sso.example.test"}}
	r, err := Render(Input{Config: c})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Blockers) == 0 {
		t.Fatal("expected distributed throttle qualification blocker")
	}
}
