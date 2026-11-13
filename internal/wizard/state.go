package wizard

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/cluster"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/imagearchive"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/preparation"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

type State struct {
	RegistryHost, Project, AuthMode, Username string
	Password, Token                           string
	KubeconfigPath, ContextName, Namespace    string
	WorkerNames, StorageClass, BundlePath     string
	Revision                                  uint64
	Inventory                                 []registry.Image
	InventoryRevision                         uint64
	InventoryStatus                           string
	Cluster                                   *cluster.Inventory
	Archive                                   *imagearchive.Result
	ArchiveError                              string
	ClusterError                              string
	PreparationConfig                         config.File
	OracleTargetKind                          string
	OracleCredentials                         map[string]preparation.OracleCredential
	CheckOracle                               bool
	OutputDirectory, StateDirectory           string
	PreparationStatus                         string
	PreparationRevision                       uint64
	PreparationReport                         *preparation.Report
}

func (s *State) Set(field, value string) {
	changed := false
	switch field {
	case "registry":
		changed = s.RegistryHost != value
		s.RegistryHost = value
	case "project":
		changed = s.Project != value
		s.Project = value
	case "auth":
		changed = s.AuthMode != value
		s.AuthMode = value
		if changed {
			s.Username = ""
			s.Password = ""
			s.Token = ""
		}
	case "username":
		changed = s.Username != value
		s.Username = value
	case "password":
		changed = s.Password != value
		s.Password = value
	case "token":
		changed = s.Token != value
		s.Token = value
	case "kubeconfig":
		changed = s.KubeconfigPath != value
		s.KubeconfigPath = value
	case "context":
		changed = s.ContextName != value
		s.ContextName = value
	case "namespace":
		changed = s.Namespace != value
		s.Namespace = value
	case "workers":
		changed = s.WorkerNames != value
		s.WorkerNames = value
	case "storage":
		changed = s.StorageClass != value
		s.StorageClass = value
	case "bundle":
		changed = s.BundlePath != value
		s.BundlePath = value
	}
	if changed {
		s.Revision++
		s.invalidatePreparation()
		switch field {
		case "kubeconfig", "context", "namespace", "workers", "storage":
			s.Inventory = nil
			s.InventoryRevision = 0
			s.InventoryStatus = "STALE"
			s.Cluster = nil
			s.ClusterError = ""
		case "bundle":
			s.Archive = nil
			s.ArchiveError = ""
			s.InventoryStatus = "STALE"
			s.InventoryRevision = 0
		default:
			s.Inventory = nil
			s.InventoryRevision = 0
			s.InventoryStatus = "STALE"
		}
	}
}

func (s *State) invalidatePreparation() {
	s.PreparationRevision++
	s.PreparationStatus = "STALE"
	s.PreparationReport = nil
}

func (s *State) SetPreparation(field, value string) {
	c := &s.PreparationConfig
	changed := false
	switch field {
	case "oracle_target_kind":
		changed = s.OracleTargetKind != value
		s.OracleTargetKind = value
		if changed {
			if value == "descriptor" {
				c.Oracle.Host, c.Oracle.Port, c.Oracle.ServiceName = "", 0, ""
			} else {
				c.Oracle.ConnectDescriptor = ""
			}
		}
	case "oracle_host":
		changed = c.Oracle.Host != value
		c.Oracle.Host = value
	case "oracle_port":
		changed = fmt.Sprint(c.Oracle.Port) != value
		c.Oracle.Port, _ = strconv.Atoi(value)
	case "oracle_service":
		changed = c.Oracle.ServiceName != value
		c.Oracle.ServiceName = value
	case "oracle_descriptor":
		changed = c.Oracle.ConnectDescriptor != value
		c.Oracle.ConnectDescriptor = value
	case "oracle_mode":
		changed = c.Oracle.ConnectionMode != value
		c.Oracle.ConnectionMode = value
	case "oracle_dn":
		changed = c.Oracle.ServerCertificateDN != value
		c.Oracle.ServerCertificateDN = value
	case "oracle_wallet":
		changed = c.Oracle.WalletPath != value
		c.Oracle.WalletPath = value
	case "oracle_trust":
		changed = c.Oracle.TrustBundlePath != value
		c.Oracle.TrustBundlePath = value
	case "oracle_connect_timeout":
		changed = fmt.Sprint(c.Oracle.ConnectTimeoutSeconds) != value
		c.Oracle.ConnectTimeoutSeconds, _ = strconv.Atoi(value)
	case "oracle_read_timeout":
		changed = fmt.Sprint(c.Oracle.ReadTimeoutSeconds) != value
		c.Oracle.ReadTimeoutSeconds, _ = strconv.Atoi(value)
	case "schema_mode":
		changed = c.Oracle.SchemaMode != value
		c.Oracle.SchemaMode = value
	case "schemas":
		changed = false
		values := strings.Split(value, ",")
		if len(values) == 3 {
			changed = c.Oracle.APIMSchema != strings.TrimSpace(values[0]) || c.Oracle.SharedSchema != strings.TrimSpace(values[1]) || c.Oracle.KeycloakSchema != strings.TrimSpace(values[2])
			c.Oracle.APIMSchema, c.Oracle.SharedSchema, c.Oracle.KeycloakSchema = strings.TrimSpace(values[0]), strings.TrimSpace(values[1]), strings.TrimSpace(values[2])
		}
	case "runtime_accounts":
		changed = false
		values := strings.Split(value, ",")
		if len(values) == 3 {
			changed = c.Oracle.APIMRuntimeAccount != strings.TrimSpace(values[0]) || c.Oracle.SharedRuntimeAccount != strings.TrimSpace(values[1]) || c.Oracle.KeycloakRuntimeAccount != strings.TrimSpace(values[2])
			c.Oracle.APIMRuntimeAccount, c.Oracle.SharedRuntimeAccount, c.Oracle.KeycloakRuntimeAccount = strings.TrimSpace(values[0]), strings.TrimSpace(values[1]), strings.TrimSpace(values[2])
			if changed {
				s.clearOracleCredentials("APIM", "SHARED", "KEYCLOAK")
			}
		}
	case "owner_accounts":
		changed = false
		values := strings.Split(value, ",")
		if len(values) == 3 {
			changed = c.Oracle.APIMOwnerAccount != strings.TrimSpace(values[0]) || c.Oracle.SharedOwnerAccount != strings.TrimSpace(values[1]) || c.Oracle.KeycloakOwnerAccount != strings.TrimSpace(values[2])
			c.Oracle.APIMOwnerAccount, c.Oracle.SharedOwnerAccount, c.Oracle.KeycloakOwnerAccount = strings.TrimSpace(values[0]), strings.TrimSpace(values[1]), strings.TrimSpace(values[2])
			if changed {
				s.clearOracleCredentials("APIM_OWNER", "SHARED_OWNER", "KEYCLOAK_OWNER")
			}
		}
	case "check_oracle":
		changed = (value == "yes") != s.CheckOracle
		s.CheckOracle = value == "yes"
	case "cert_mode":
		changed = c.Certificates.Mode != value
		c.Certificates.Mode = value
	case "hostnames":
		values := strings.Split(value, ",")
		if len(values) == 3 {
			changed = c.Certificates.APIMHostname != strings.TrimSpace(values[0]) || c.Certificates.GatewayHostname != strings.TrimSpace(values[1]) || c.Certificates.KeycloakHostname != strings.TrimSpace(values[2])
			c.Certificates.APIMHostname, c.Certificates.GatewayHostname, c.Certificates.KeycloakHostname = strings.TrimSpace(values[0]), strings.TrimSpace(values[1]), strings.TrimSpace(values[2])
		}
	case "cluster_dns":
		changed = c.Deployment.ClusterDNSDomain != value
		c.Deployment.ClusterDNSDomain = value
	case "external_cert":
		changed = false
		values := strings.Split(value, "|")
		if len(values) == 4 {
			changed = c.Certificates.ExternalCertificatePath != values[0] || c.Certificates.ExternalPrivateKeyPath != values[1] || c.Certificates.ExternalChainPath != values[2] || c.Certificates.ExternalTrustPath != values[3]
			c.Certificates.ExternalCertificatePath, c.Certificates.ExternalPrivateKeyPath, c.Certificates.ExternalChainPath, c.Certificates.ExternalTrustPath = values[0], values[1], values[2], values[3]
		}
	case "internal_cert":
		changed = false
		values := strings.Split(value, "|")
		if len(values) == 4 {
			changed = c.Certificates.InternalCertificatePath != values[0] || c.Certificates.InternalPrivateKeyPath != values[1] || c.Certificates.InternalChainPath != values[2] || c.Certificates.InternalTrustPath != values[3]
			c.Certificates.InternalCertificatePath, c.Certificates.InternalPrivateKeyPath, c.Certificates.InternalChainPath, c.Certificates.InternalTrustPath = values[0], values[1], values[2], values[3]
		}
	case "distribution_path":
		changed = c.Artifacts.WSO2DistributionPath != value
		c.Artifacts.WSO2DistributionPath = value
	case "distribution_sha":
		changed = c.Artifacts.WSO2DistributionSHA256 != value
		c.Artifacts.WSO2DistributionSHA256 = value
	case "schema_manifest":
		changed = c.Artifacts.SchemaManifestPath != value
		c.Artifacts.SchemaManifestPath = value
	case "output_dir":
		changed = s.OutputDirectory != value
		s.OutputDirectory = value
		c.Deployment.OutputDirectory = value
	case "state_dir":
		changed = s.StateDirectory != value
		s.StateDirectory = value
	case "throttle_host":
		changed = c.DistributedThrottle.Host != value
		c.DistributedThrottle.Host = value
	case "throttle_port":
		changed = fmt.Sprint(c.DistributedThrottle.Port) != value
		c.DistributedThrottle.Port, _ = strconv.Atoi(value)
	case "throttle_type":
		changed = c.DistributedThrottle.Type != value
		c.DistributedThrottle.Type = value
	case "throttle_replication":
		changed = c.DistributedThrottle.ReplicationMode != (value == "yes")
		c.DistributedThrottle.ReplicationMode = value == "yes"
	case "throttle_tls":
		changed = c.DistributedThrottle.TLS != (value == "yes")
		c.DistributedThrottle.TLS = value == "yes"
	case "throttle_trust":
		changed = c.DistributedThrottle.TrustBundlePath != value
		c.DistributedThrottle.TrustBundlePath = value
	}
	if changed {
		s.invalidatePreparation()
	}
}

func (s *State) SetOracleCredential(key, username, password string) {
	if s.OracleCredentials == nil {
		s.OracleCredentials = make(map[string]preparation.OracleCredential)
	}
	old := s.OracleCredentials[key]
	if old.Username == username && old.Password == password {
		return
	}
	s.OracleCredentials[key] = preparation.OracleCredential{Username: username, Password: password}
	s.invalidatePreparation()
}

func (s *State) clearOracleCredentials(keys ...string) {
	for _, key := range keys {
		delete(s.OracleCredentials, key)
	}
}

func (s State) Credentials() registry.Credentials {
	switch s.AuthMode {
	case "basic":
		return registry.Credentials{Username: s.Username, Password: s.Password}
	case "token":
		return registry.Credentials{Token: s.Token}
	}
	return registry.Credentials{}
}
