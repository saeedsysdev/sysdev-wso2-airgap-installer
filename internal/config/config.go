package config

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

// SchemaVersion is the current non-secret configuration format. Version 1 is
// migrated in memory by Load; the source file is never rewritten implicitly.
const SchemaVersion = 2

type File struct {
	SchemaVersion       int                 `json:"schema_version"`
	Profile             string              `json:"profile"`
	Registry            Registry            `json:"registry"`
	Kubernetes          Kubernetes          `json:"kubernetes,omitempty"`
	OfflineBundlePath   string              `json:"offline_bundle_path,omitempty"`
	Oracle              Oracle              `json:"oracle,omitempty"`
	Certificates        Certificates        `json:"certificates,omitempty"`
	Deployment          Deployment          `json:"deployment,omitempty"`
	Artifacts           Artifacts           `json:"artifacts,omitempty"`
	DistributedThrottle DistributedThrottle `json:"distributed_throttle,omitempty"`
	// SourceSchemaVersion is diagnostic metadata and is never serialized.
	SourceSchemaVersion int `json:"-"`
}
type Registry struct {
	Host         string `json:"host"`
	Project      string `json:"project"`
	CABundlePath string `json:"ca_bundle_path,omitempty"`
}
type Kubernetes struct {
	KubeconfigPath   string   `json:"kubeconfig_path,omitempty"`
	Context          string   `json:"context,omitempty"`
	Namespace        string   `json:"namespace,omitempty"`
	SelectedWorkers  []string `json:"selected_workers,omitempty"`
	StorageClass     string   `json:"storage_class,omitempty"`
	NodePortRangeMin int      `json:"node_port_range_min,omitempty"`
	NodePortRangeMax int      `json:"node_port_range_max,omitempty"`
}

// Oracle contains connection and schema metadata only. Credential values are
// accepted only by the protected secret store or a credential provider.
type Oracle struct {
	Host                   string `json:"host,omitempty"`
	Port                   int    `json:"port,omitempty"`
	ServiceName            string `json:"service_name,omitempty"`
	ConnectDescriptor      string `json:"connect_descriptor,omitempty"`
	ConnectionMode         string `json:"connection_mode,omitempty"` // tcp or tcps
	ServerCertificateDN    string `json:"server_certificate_dn,omitempty"`
	ConnectTimeoutSeconds  int    `json:"connect_timeout_seconds,omitempty"`
	ReadTimeoutSeconds     int    `json:"read_timeout_seconds,omitempty"`
	SchemaMode             string `json:"schema_mode,omitempty"` // prepared or initialize
	APIMSchema             string `json:"apim_schema,omitempty"`
	SharedSchema           string `json:"shared_schema,omitempty"`
	KeycloakSchema         string `json:"keycloak_schema,omitempty"`
	APIMRuntimeAccount     string `json:"apim_runtime_account,omitempty"`
	APIMOwnerAccount       string `json:"apim_owner_account,omitempty"`
	SharedRuntimeAccount   string `json:"shared_runtime_account,omitempty"`
	SharedOwnerAccount     string `json:"shared_owner_account,omitempty"`
	KeycloakRuntimeAccount string `json:"keycloak_runtime_account,omitempty"`
	KeycloakOwnerAccount   string `json:"keycloak_owner_account,omitempty"`
	TrustBundlePath        string `json:"trust_bundle_path,omitempty"`
	WalletPath             string `json:"wallet_path,omitempty"`
}

type Certificates struct {
	Mode                    string `json:"mode,omitempty"` // import or generate
	ExternalCertificatePath string `json:"external_certificate_path,omitempty"`
	ExternalPrivateKeyPath  string `json:"external_private_key_path,omitempty"`
	ExternalChainPath       string `json:"external_chain_path,omitempty"`
	ExternalTrustPath       string `json:"external_trust_path,omitempty"`
	InternalCertificatePath string `json:"internal_certificate_path,omitempty"`
	InternalPrivateKeyPath  string `json:"internal_private_key_path,omitempty"`
	InternalChainPath       string `json:"internal_chain_path,omitempty"`
	InternalTrustPath       string `json:"internal_trust_path,omitempty"`
	APIMHostname            string `json:"apim_hostname,omitempty"`
	GatewayHostname         string `json:"gateway_hostname,omitempty"`
	KeycloakHostname        string `json:"keycloak_hostname,omitempty"`
}

type Deployment struct {
	ClusterDNSDomain string `json:"cluster_dns_domain,omitempty"`
	OutputDirectory  string `json:"output_directory,omitempty"`
}

type Artifacts struct {
	WSO2DistributionPath   string `json:"wso2_distribution_path,omitempty"`
	WSO2DistributionSHA256 string `json:"wso2_distribution_sha256,omitempty"`
	SchemaManifestPath     string `json:"schema_manifest_path,omitempty"`
}

type DistributedThrottle struct {
	Host            string `json:"host,omitempty"`
	Port            int    `json:"port,omitempty"`
	Type            string `json:"type,omitempty"` // redis or valkey
	ReplicationMode bool   `json:"replication_mode,omitempty"`
	TLS             bool   `json:"tls,omitempty"`
	TrustBundlePath string `json:"trust_bundle_path,omitempty"`
}

func Load(path string) (File, error) {
	f, err := os.Open(path)
	if err != nil {
		return File{}, fmt.Errorf("open configuration: %w", err)
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	var c File
	if err := dec.Decode(&c); err != nil {
		return File{}, fmt.Errorf("read configuration: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return File{}, fmt.Errorf("configuration must contain exactly one JSON value")
	}
	if c.SchemaVersion != 1 && c.SchemaVersion != SchemaVersion {
		return File{}, fmt.Errorf("unsupported configuration schema version %d; supported version is %d", c.SchemaVersion, SchemaVersion)
	}
	c.SourceSchemaVersion = c.SchemaVersion
	if c.SchemaVersion == 1 {
		// Explicit, non-destructive in-memory migration. Load never writes the
		// user's file; `config migrate` is the only future write path.
		c.SchemaVersion = SchemaVersion
	}
	if c.Profile == "" {
		c.Profile = registry.Profile.ID
	}
	if err := Validate(c); err != nil {
		return File{}, err
	}
	return c, nil
}

// Validate checks an already decoded non-secret configuration without writing
// it. Load uses the same validator after the in-memory schema migration.
func Validate(c File) error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("configuration must be migrated to schema %d before validation", SchemaVersion)
	}
	if c.Profile != "" && c.Profile != registry.Profile.ID {
		return fmt.Errorf("unsupported compatibility profile %q; this build supports %q", c.Profile, registry.Profile.ID)
	}
	if c.Profile == "" {
		c.Profile = registry.Profile.ID
	}
	if err := registry.ValidateEndpoint(c.Registry.Host, c.Registry.Project); err != nil {
		return fmt.Errorf("invalid registry configuration: %w", err)
	}
	if (c.Kubernetes.Context != "" || len(c.Kubernetes.SelectedWorkers) > 0 || c.Kubernetes.StorageClass != "" || c.Kubernetes.NodePortRangeMin != 0) && c.Kubernetes.KubeconfigPath == "" {
		return fmt.Errorf("kubernetes.kubeconfig_path is required when Kubernetes preflight options are set")
	}
	if c.Kubernetes.Namespace != "" && !namespacePattern.MatchString(c.Kubernetes.Namespace) {
		return fmt.Errorf("Kubernetes namespace must be a lowercase DNS label")
	}
	if c.Kubernetes.StorageClass != "" && !storageClassPattern.MatchString(c.Kubernetes.StorageClass) {
		return fmt.Errorf("Kubernetes StorageClass name must be a lowercase DNS subdomain")
	}
	if (c.Kubernetes.NodePortRangeMin == 0) != (c.Kubernetes.NodePortRangeMax == 0) || c.Kubernetes.NodePortRangeMin < 0 || c.Kubernetes.NodePortRangeMax > 65535 || (c.Kubernetes.NodePortRangeMin != 0 && c.Kubernetes.NodePortRangeMin >= c.Kubernetes.NodePortRangeMax) {
		return fmt.Errorf("Kubernetes NodePort range must provide valid minimum and maximum values")
	}
	seenWorkers := map[string]bool{}
	for _, name := range c.Kubernetes.SelectedWorkers {
		if !workerPattern.MatchString(name) || seenWorkers[name] {
			return fmt.Errorf("Kubernetes selected_workers must contain unique DNS node names")
		}
		seenWorkers[name] = true
	}
	if err := validateOracle(c.Oracle); err != nil {
		return err
	}
	if err := validateCertificates(c.Certificates); err != nil {
		return err
	}
	if c.Deployment.ClusterDNSDomain != "" && !validDNSName(c.Deployment.ClusterDNSDomain) {
		return fmt.Errorf("deployment.cluster_dns_domain must be a lowercase DNS domain")
	}
	if c.Artifacts.WSO2DistributionSHA256 != "" && !sha256Pattern.MatchString(c.Artifacts.WSO2DistributionSHA256) {
		return fmt.Errorf("artifacts.wso2_distribution_sha256 must be 64 lowercase hexadecimal characters")
	}
	if err := validateDistributedThrottle(c.DistributedThrottle); err != nil {
		return err
	}
	return nil
}

func validateDistributedThrottle(d DistributedThrottle) error {
	if d.Host == "" && d.Port == 0 && d.Type == "" && !d.ReplicationMode {
		return nil
	}
	if !oracleHostPattern.MatchString(d.Host) || d.Port < 1 || d.Port > 65535 || (d.Type != "redis" && d.Type != "valkey") {
		return fmt.Errorf("distributed_throttle requires a valid Redis/Valkey host, port, and type")
	}
	if !d.ReplicationMode {
		return fmt.Errorf("distributed_throttle requires replication_mode=true; sharded stores are unsupported")
	}
	if d.TLS && d.TrustBundlePath == "" {
		return fmt.Errorf("distributed_throttle TLS requires trust_bundle_path")
	}
	return nil
}

func validateOracle(o Oracle) error {
	if o.Host == "" && o.ServiceName == "" && o.ConnectDescriptor == "" && o.SchemaMode == "" {
		return nil
	}
	if (o.ServiceName == "") == (o.ConnectDescriptor == "") {
		return fmt.Errorf("Oracle requires exactly one of service_name or connect_descriptor")
	}
	upperDescriptor := strings.ToUpper(o.ConnectDescriptor)
	if o.ConnectDescriptor != "" && (strings.Contains(o.ConnectDescriptor, "@") || strings.Contains(o.ConnectDescriptor, "\n") || strings.Contains(o.ConnectDescriptor, "\r") || strings.Contains(upperDescriptor, "PASSWORD=") || strings.Contains(upperDescriptor, "PWD=") || strings.Contains(upperDescriptor, "USERNAME=") || strings.Contains(upperDescriptor, "USER=")) {
		return fmt.Errorf("Oracle connect_descriptor must not contain credentials or line breaks")
	}
	mode := o.ConnectionMode
	if mode == "" {
		mode = "tcp"
	}
	if mode != "tcp" && mode != "tcps" {
		return fmt.Errorf("Oracle connection_mode must be tcp or tcps")
	}
	descriptorTCPS := strings.HasPrefix(strings.ToLower(strings.TrimSpace(o.ConnectDescriptor)), "tcps:") || strings.Contains(strings.ToUpper(o.ConnectDescriptor), "PROTOCOL=TCPS")
	if o.ConnectDescriptor != "" && descriptorTCPS != (mode == "tcps") {
		return fmt.Errorf("Oracle connection_mode must match the connect descriptor transport")
	}
	if o.ConnectDescriptor == "" {
		if !oracleHostPattern.MatchString(o.Host) || o.Port < 1 || o.Port > 65535 {
			return fmt.Errorf("Oracle host and port must be a valid host and TCP port")
		}
	} else if o.Host != "" || o.Port != 0 {
		return fmt.Errorf("Oracle host and port cannot be combined with connect_descriptor")
	}
	for name, seconds := range map[string]int{"connect_timeout_seconds": o.ConnectTimeoutSeconds, "read_timeout_seconds": o.ReadTimeoutSeconds} {
		if seconds < 0 || seconds > 300 {
			return fmt.Errorf("Oracle %s must be between 0 and 300", name)
		}
	}
	if o.SchemaMode != "" && o.SchemaMode != "prepared" && o.SchemaMode != "initialize" {
		return fmt.Errorf("Oracle schema_mode must be prepared or initialize")
	}
	for _, schema := range []string{o.APIMSchema, o.SharedSchema, o.KeycloakSchema, o.APIMRuntimeAccount, o.APIMOwnerAccount, o.SharedRuntimeAccount, o.SharedOwnerAccount, o.KeycloakRuntimeAccount, o.KeycloakOwnerAccount} {
		if schema != "" && !oracleIdentifierPattern.MatchString(schema) {
			return fmt.Errorf("Oracle schema and account names must be unquoted Oracle identifiers")
		}
	}
	if mode == "tcps" && o.ServerCertificateDN == "" {
		return fmt.Errorf("Oracle TCPS requires server_certificate_dn for authenticated server identity matching")
	}
	if mode == "tcps" && o.WalletPath == "" && o.TrustBundlePath == "" {
		return fmt.Errorf("Oracle TCPS requires a supported wallet directory or PEM trust bundle")
	}
	return nil
}

func validateCertificates(c Certificates) error {
	if c.Mode == "" {
		return nil
	}
	if c.Mode != "import" && c.Mode != "generate" {
		return fmt.Errorf("certificates.mode must be import or generate")
	}
	if c.Mode == "import" && (c.ExternalCertificatePath == "" || c.ExternalPrivateKeyPath == "" || c.ExternalTrustPath == "" || c.InternalCertificatePath == "" || c.InternalPrivateKeyPath == "" || c.InternalTrustPath == "") {
		return fmt.Errorf("certificate import mode requires external and internal certificate/private-key/trust paths")
	}
	if c.Mode == "generate" && (c.APIMHostname == "" || c.GatewayHostname == "" || c.KeycloakHostname == "") {
		return fmt.Errorf("certificate generation requires APIM, gateway, and Keycloak hostnames")
	}
	for _, host := range []string{c.APIMHostname, c.GatewayHostname, c.KeycloakHostname} {
		if host != "" && !validDNSName(host) {
			return fmt.Errorf("certificate hostnames must be valid DNS names")
		}
	}
	return nil
}

var namespacePattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9]{0,61}[a-z0-9])?$`)
var workerPattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9.]{0,251}[a-z0-9])?$`)
var storageClassPattern = regexp.MustCompile(`^[a-z0-9](?:[-a-z0-9.]{0,251}[a-z0-9])?$`)
var dnsDomainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
var oracleHostPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`)
var oracleIdentifierPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_$#]{0,29}$`)
var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validDNSName(name string) bool {
	if len(name) > 253 || !dnsDomainPattern.MatchString(name) || strings.Contains(name, "..") {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
	}
	return true
}
