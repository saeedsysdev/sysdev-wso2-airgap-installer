package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

func writeConfig(t *testing.T, s string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if e := os.WriteFile(p, []byte(s), 0600); e != nil {
		t.Fatal(e)
	}
	return p
}

func TestLoadVersionedNonSecretConfig(t *testing.T) {
	p := writeConfig(t, `{"schema_version":1,"profile":"wso2-apim-4.7.0-keycloak-26.8.1-candidate","registry":{"host":"registry.example:5000","project":"platform/wso2"}}`)
	c, e := Load(p)
	if e != nil {
		t.Fatal(e)
	}
	if c.Registry.Host != "registry.example:5000" || c.Registry.Project != "platform/wso2" || c.SchemaVersion != SchemaVersion || c.SourceSchemaVersion != 1 {
		t.Fatalf("unexpected config %#v", c)
	}
	if b, err := os.ReadFile(p); err != nil || !strings.Contains(string(b), `"schema_version":1`) {
		t.Fatalf("Load must not rewrite the v1 file: %v %s", err, b)
	}
}

func TestOracleAndCertificateConfigurationValidation(t *testing.T) {
	valid := `{"schema_version":2,"registry":{"host":"registry.example","project":""},"oracle":{"host":"db.example","port":1521,"service_name":"apim.example","connection_mode":"tcps","wallet_path":"wallet","server_certificate_dn":"CN=db.example","schema_mode":"prepared","apim_schema":"WSO2_APIM","shared_schema":"WSO2_SHARED","keycloak_schema":"KEYCLOAK"},"certificates":{"mode":"generate","apim_hostname":"apim.example.com","gateway_hostname":"api.example.com","keycloak_hostname":"sso.example.com"},"deployment":{"cluster_dns_domain":"cluster.local"}}`
	if _, err := Load(writeConfig(t, valid)); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"schema_version":2,"registry":{"host":"registry.example","project":""},"oracle":{"host":"db.example","port":1521,"service_name":"apim","connection_mode":"tcps","schema_mode":"prepared"}}`,
		`{"schema_version":2,"registry":{"host":"registry.example","project":""},"oracle":{"host":"db.example","port":1521,"service_name":"apim","schema_mode":"initialize","apim_schema":"BAD-NAME"}}`,
		`{"schema_version":2,"registry":{"host":"registry.example","project":""},"certificates":{"mode":"import","external_certificate_path":"cert.pem"}}`,
		`{"schema_version":2,"registry":{"host":"registry.example","project":""},"certificates":{"mode":"generate","apim_hostname":"*.example.com","gateway_hostname":"api.example.com","keycloak_hostname":"sso.example.com"}}`,
		`{"schema_version":2,"registry":{"host":"registry.example","project":""},"oracle":{"host":"db.example","port":1521,"service_name":"apim","password":"canary"}}`,
		`{"schema_version":2,"registry":{"host":"registry.example","project":""},"oracle":{"connect_descriptor":"(DESCRIPTION=(CONNECT_DATA=(SERVICE_NAME=x))(PASSWORD=canary))","connection_mode":"tcp","schema_mode":"prepared"}}`,
	} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Fatalf("accepted invalid configuration: %s", body)
		}
	}
}
func TestLoadRejectsUnsupportedSchemaAndUnknownFields(t *testing.T) {
	for _, body := range []string{`{"schema_version":9,"registry":{"host":"registry.example","project":""}}`, `{"schema_version":1,"registry":{"host":"registry.example","project":""},"password":"canary"}`} {
		if _, e := Load(writeConfig(t, body)); e == nil {
			t.Fatalf("accepted unsupported config: %s", body)
		}
	}
}

func TestKubernetesPreflightOptionsRequireValidKubeconfigAndNames(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":1,"registry":{"host":"registry.example","project":""},"kubernetes":{"context":"ctx"}}`,
		`{"schema_version":1,"registry":{"host":"registry.example","project":""},"kubernetes":{"kubeconfig_path":"config","storage_class":"Bad_Name"}}`,
		`{"schema_version":1,"registry":{"host":"registry.example","project":""},"kubernetes":{"kubeconfig_path":"config","selected_workers":["worker-a","worker-a"]}}`,
		`{"schema_version":1,"registry":{"host":"registry.example","project":""},"kubernetes":{"kubeconfig_path":"config","node_port_range_min":32000,"node_port_range_max":0}}`,
	} {
		if _, err := Load(writeConfig(t, body)); err == nil {
			t.Fatalf("accepted invalid Kubernetes preflight configuration: %s", body)
		}
	}
}

func TestCandidateProfileIsVersionedData(t *testing.T) {
	if registry.Profile.SchemaVersion != 1 || registry.Profile.WSO2 != "4.7.0" || registry.Profile.Keycloak != "26.8.1" || len(registry.Profile.Images) != 4 {
		t.Fatalf("unexpected compatibility descriptor: %#v", registry.Profile)
	}
	if len(registry.Profile.Components) != 5 || len(registry.Profile.NodePorts) != 3 || len(registry.Profile.JDBCCandidates) != 1 {
		t.Fatalf("candidate deployment settings missing from profile: %#v", registry.Profile)
	}
	for _, image := range registry.Profile.Images {
		if image.SourceReference == "" {
			t.Errorf("%s has no versioned source reference", image.Component)
		}
	}
}

func TestStage3ExamplesAreValidNonSecretConfiguration(t *testing.T) {
	for _, path := range []string{"../../examples/config.prepared.json", "../../examples/config.initialize.json"} {
		if _, err := Load(path); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}
