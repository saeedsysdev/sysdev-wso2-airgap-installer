package wizard

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/preparation"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

const (
	preparationFirstStep  = 13
	preparationLastField  = 50
	preparationReviewStep = 51
)

type preparationField struct {
	key, label, prompt string
	secret             bool
}

var preparationFields = []preparationField{
	{"oracle_target_kind", "Oracle target type", "Choose service or descriptor.", false},
	{"oracle_host", "Oracle host", "Oracle hostname or IP address.", false},
	{"oracle_port", "Oracle port", "Oracle listener port (default 1521).", false},
	{"oracle_service", "Oracle service name", "Oracle service name (not SID).", false},
	{"oracle_descriptor", "Oracle connect descriptor", "Credential-free descriptor; never include user/password fields.", false},
	{"oracle_mode", "Oracle transport", "Choose tcp or tcps.", false},
	{"oracle_dn", "Oracle server certificate DN", "Required for TCPS; match the server certificate DN.", false},
	{"oracle_wallet", "Oracle wallet path", "Optional wallet directory for TCPS.", false},
	{"oracle_trust", "Oracle trust bundle path", "PEM trust bundle for TCPS when not using a wallet.", false},
	{"oracle_connect_timeout", "Oracle connect timeout", "Connection timeout in seconds (blank uses the default).", false},
	{"oracle_read_timeout", "Oracle read timeout", "Total read timeout in seconds (blank uses the default).", false},
	{"schema_mode", "Oracle schema mode", "Choose prepared or initialize. Initialize requires verified local WSO2 SQL artifacts.", false},
	{"schemas", "Oracle schema names", "Comma-separated APIM, shared, Keycloak schema names.", false},
	{"runtime_accounts", "Oracle runtime accounts", "Comma-separated APIM, shared, Keycloak runtime account names.", false},
	{"owner_accounts", "Oracle schema-owner accounts", "Optional comma-separated APIM, shared, Keycloak owner account names; leave blank for none.", false},
	{"check_oracle", "Run Oracle validation", "Choose yes for bounded authenticated SELECT checks, or no to defer them.", false},
	{"oracle_password_apim", "APIM Oracle password", "Session-only password for the APIM runtime account.", true},
	{"oracle_password_shared", "Shared Oracle password", "Session-only password for the shared runtime account.", true},
	{"oracle_password_keycloak", "Keycloak Oracle password", "Session-only password for the Keycloak runtime account.", true},
	{"oracle_password_apim_owner", "APIM owner password", "Session-only password for the APIM schema-owner account.", true},
	{"oracle_password_shared_owner", "Shared owner password", "Session-only password for the shared schema-owner account.", true},
	{"oracle_password_keycloak_owner", "Keycloak owner password", "Session-only password for the Keycloak schema-owner account.", true},
	{"cert_mode", "Certificate mode", "Choose generate or import.", false},
	{"hostnames", "Public service hostnames", "Comma-separated APIM, gateway, Keycloak DNS names; needed for import validation too.", false},
	{"cluster_dns", "Cluster DNS domain", "For example cluster.local.", false},
	{"external_cert", "External certificate inputs", "Pipe-separated certificate path | private-key path | optional chain path | trust path.", false},
	{"internal_cert", "Internal certificate inputs", "Pipe-separated certificate path | private-key path | optional chain path | trust path.", false},
	{"distribution_path", "WSO2 distribution archive", "Path to the exact offline WSO2 distribution archive (optional for prepared schemas).", false},
	{"distribution_sha", "WSO2 distribution SHA-256", "64 lowercase hexadecimal characters when a distribution archive is supplied.", false},
	{"schema_manifest", "WSO2 schema artifact manifest", "Path to the locally supplied schema artifact manifest; required for initialize mode.", false},
	{"output_dir", "Public preparation output directory", "New directory for redacted review and deployment assets; it must not already exist.", false},
	{"state_dir", "Protected state directory", "Separate protected directory for private keys and the reusable shared encryption key.", false},
	{"throttle_host", "Distributed throttle host", "Replicated Redis/Valkey hostname; leave blank if not configured.", false},
	{"throttle_port", "Distributed throttle port", "Redis/Valkey port.", false},
	{"throttle_type", "Distributed throttle type", "Choose redis or valkey when a host is configured.", false},
	{"throttle_replication", "Distributed throttle replication", "Choose yes only for a supported replicated store; sharded mode is unsupported.", false},
	{"throttle_tls", "Distributed throttle TLS", "Choose yes to configure TLS trust.", false},
	{"throttle_trust", "Distributed throttle trust bundle", "PEM trust bundle path when distributed throttle TLS is enabled.", false},
}

func init() {
	for _, field := range preparationFields {
		labels = append(labels, field.label)
	}
	labels = append(labels, "Stage 3 preparation review/export")
}

func (m *Model) preparationConfig() config.File {
	c := m.state.PreparationConfig
	c.SchemaVersion = config.SchemaVersion
	c.Profile = registry.Profile.ID
	c.Registry.Host = m.state.RegistryHost
	c.Registry.Project = m.state.Project
	c.Kubernetes.Namespace = m.state.Namespace
	if c.Kubernetes.Namespace == "" {
		c.Kubernetes.Namespace = "wso2"
	}
	c.Kubernetes.SelectedWorkers = splitList(m.state.WorkerNames)
	c.OfflineBundlePath = m.state.BundlePath
	c.Deployment.OutputDirectory = m.state.OutputDirectory
	return c
}

func (m *Model) preparationField(step int) (preparationField, bool) {
	index := step - preparationFirstStep
	if index < 0 || index >= len(preparationFields) {
		return preparationField{}, false
	}
	return preparationFields[index], true
}

func (m *Model) preparationFieldVisible(step int) bool {
	f, ok := m.preparationField(step)
	if !ok {
		return false
	}
	c := m.preparationConfig()
	switch f.key {
	case "oracle_host", "oracle_port", "oracle_service":
		return m.state.OracleTargetKind != "descriptor"
	case "oracle_descriptor":
		return m.state.OracleTargetKind == "descriptor"
	case "oracle_dn", "oracle_wallet", "oracle_trust":
		return strings.EqualFold(c.Oracle.ConnectionMode, "tcps")
	case "oracle_password_apim", "oracle_password_shared", "oracle_password_keycloak":
		return m.state.CheckOracle
	case "oracle_password_apim_owner":
		return m.state.CheckOracle && c.Oracle.APIMOwnerAccount != ""
	case "oracle_password_shared_owner":
		return m.state.CheckOracle && c.Oracle.SharedOwnerAccount != ""
	case "oracle_password_keycloak_owner":
		return m.state.CheckOracle && c.Oracle.KeycloakOwnerAccount != ""
	case "throttle_trust":
		return c.DistributedThrottle.Host != "" && c.DistributedThrottle.TLS
	case "throttle_port", "throttle_type", "throttle_replication", "throttle_tls":
		return c.DistributedThrottle.Host != ""
	case "external_cert", "internal_cert":
		return strings.EqualFold(c.Certificates.Mode, "import")
	default:
		return true
	}
}

func (m *Model) nextPreparationStep(from int) int {
	for step := from + 1; step <= preparationReviewStep; step++ {
		if step == preparationReviewStep || m.preparationFieldVisible(step) {
			return step
		}
	}
	return preparationReviewStep
}

func (m *Model) previousPreparationStep(from int) int {
	for step := from - 1; step >= preparationFirstStep; step-- {
		if m.preparationFieldVisible(step) {
			return step
		}
	}
	return 12
}

func (m *Model) preparationValue(step int) string {
	f, ok := m.preparationField(step)
	if !ok {
		return ""
	}
	c := m.preparationConfig()
	o := c.Oracle
	switch f.key {
	case "oracle_target_kind":
		if m.state.OracleTargetKind == "" {
			return "service"
		}
		return m.state.OracleTargetKind
	case "oracle_host":
		return o.Host
	case "oracle_port":
		if o.Port == 0 {
			return "1521"
		}
		return strconv.Itoa(o.Port)
	case "oracle_service":
		return o.ServiceName
	case "oracle_descriptor":
		return o.ConnectDescriptor
	case "oracle_mode":
		if o.ConnectionMode == "" {
			return "tcp"
		}
		return o.ConnectionMode
	case "oracle_dn":
		return o.ServerCertificateDN
	case "oracle_wallet":
		return o.WalletPath
	case "oracle_trust":
		return o.TrustBundlePath
	case "oracle_connect_timeout":
		if o.ConnectTimeoutSeconds == 0 {
			return ""
		}
		return strconv.Itoa(o.ConnectTimeoutSeconds)
	case "oracle_read_timeout":
		if o.ReadTimeoutSeconds == 0 {
			return ""
		}
		return strconv.Itoa(o.ReadTimeoutSeconds)
	case "schema_mode":
		return o.SchemaMode
	case "schemas":
		return strings.Join([]string{o.APIMSchema, o.SharedSchema, o.KeycloakSchema}, ",")
	case "runtime_accounts":
		return strings.Join([]string{o.APIMRuntimeAccount, o.SharedRuntimeAccount, o.KeycloakRuntimeAccount}, ",")
	case "owner_accounts":
		return strings.Join([]string{o.APIMOwnerAccount, o.SharedOwnerAccount, o.KeycloakOwnerAccount}, ",")
	case "check_oracle":
		if m.state.CheckOracle {
			return "yes"
		}
		return "no"
	case "oracle_password_apim":
		return m.state.OracleCredentials["APIM"].Password
	case "oracle_password_shared":
		return m.state.OracleCredentials["SHARED"].Password
	case "oracle_password_keycloak":
		return m.state.OracleCredentials["KEYCLOAK"].Password
	case "oracle_password_apim_owner":
		return m.state.OracleCredentials["APIM_OWNER"].Password
	case "oracle_password_shared_owner":
		return m.state.OracleCredentials["SHARED_OWNER"].Password
	case "oracle_password_keycloak_owner":
		return m.state.OracleCredentials["KEYCLOAK_OWNER"].Password
	case "cert_mode":
		return c.Certificates.Mode
	case "hostnames":
		return strings.Join([]string{c.Certificates.APIMHostname, c.Certificates.GatewayHostname, c.Certificates.KeycloakHostname}, ",")
	case "cluster_dns":
		return c.Deployment.ClusterDNSDomain
	case "external_cert":
		return strings.Join([]string{c.Certificates.ExternalCertificatePath, c.Certificates.ExternalPrivateKeyPath, c.Certificates.ExternalChainPath, c.Certificates.ExternalTrustPath}, "|")
	case "internal_cert":
		return strings.Join([]string{c.Certificates.InternalCertificatePath, c.Certificates.InternalPrivateKeyPath, c.Certificates.InternalChainPath, c.Certificates.InternalTrustPath}, "|")
	case "distribution_path":
		return c.Artifacts.WSO2DistributionPath
	case "distribution_sha":
		return c.Artifacts.WSO2DistributionSHA256
	case "schema_manifest":
		return c.Artifacts.SchemaManifestPath
	case "output_dir":
		return m.state.OutputDirectory
	case "state_dir":
		return m.state.StateDirectory
	case "throttle_host":
		return c.DistributedThrottle.Host
	case "throttle_port":
		if c.DistributedThrottle.Port == 0 {
			return ""
		}
		return strconv.Itoa(c.DistributedThrottle.Port)
	case "throttle_type":
		return c.DistributedThrottle.Type
	case "throttle_replication":
		if c.DistributedThrottle.ReplicationMode {
			return "yes"
		}
		return "no"
	case "throttle_tls":
		if c.DistributedThrottle.TLS {
			return "yes"
		}
		return "no"
	case "throttle_trust":
		return c.DistributedThrottle.TrustBundlePath
	}
	return ""
}

func (m *Model) setPreparationValue(step int, value string) error {
	f, ok := m.preparationField(step)
	if !ok {
		return fmt.Errorf("unknown preparation form field")
	}
	value = strings.TrimSpace(value)
	if f.secret {
		keys := map[string]string{"oracle_password_apim": "APIM", "oracle_password_shared": "SHARED", "oracle_password_keycloak": "KEYCLOAK", "oracle_password_apim_owner": "APIM_OWNER", "oracle_password_shared_owner": "SHARED_OWNER", "oracle_password_keycloak_owner": "KEYCLOAK_OWNER"}
		key := keys[f.key]
		username := ""
		switch key {
		case "APIM":
			username = m.preparationConfig().Oracle.APIMRuntimeAccount
		case "SHARED":
			username = m.preparationConfig().Oracle.SharedRuntimeAccount
		case "KEYCLOAK":
			username = m.preparationConfig().Oracle.KeycloakRuntimeAccount
		case "APIM_OWNER":
			username = m.preparationConfig().Oracle.APIMOwnerAccount
		case "SHARED_OWNER":
			username = m.preparationConfig().Oracle.SharedOwnerAccount
		case "KEYCLOAK_OWNER":
			username = m.preparationConfig().Oracle.KeycloakOwnerAccount
		}
		m.state.SetOracleCredential(key, username, value)
		return nil
	}
	switch f.key {
	case "oracle_target_kind":
		value = strings.ToLower(value)
		if value != "service" && value != "descriptor" {
			return fmt.Errorf("choose service or descriptor")
		}
		m.state.SetPreparation("oracle_target_kind", value)
	case "oracle_port", "oracle_connect_timeout", "oracle_read_timeout":
		if value != "" {
			n, err := strconv.Atoi(value)
			maxValue := 300
			if f.key == "oracle_port" {
				maxValue = 65535
			}
			if err != nil || n < 1 || n > maxValue {
				if f.key == "oracle_port" {
					return fmt.Errorf("Oracle port must be between 1 and 65535")
				}
				return fmt.Errorf("timeout must be between 1 and 300 seconds")
			}
		}
		if f.key == "oracle_port" && value == "" {
			value = "1521"
		}
		m.state.SetPreparation(f.key, value)
	case "oracle_mode":
		value = strings.ToLower(value)
		if value != "tcp" && value != "tcps" {
			return fmt.Errorf("choose tcp or tcps")
		}
		m.state.SetPreparation(f.key, value)
	case "schema_mode":
		value = strings.ToLower(value)
		if value != "prepared" && value != "initialize" {
			return fmt.Errorf("choose prepared or initialize")
		}
		m.state.SetPreparation(f.key, value)
	case "check_oracle":
		value = strings.ToLower(value)
		if value != "yes" && value != "no" {
			return fmt.Errorf("choose yes or no")
		}
		m.state.SetPreparation(f.key, value)
	case "cert_mode":
		value = strings.ToLower(value)
		if value != "import" && value != "generate" {
			return fmt.Errorf("choose import or generate")
		}
		m.state.SetPreparation(f.key, value)
	case "schemas", "runtime_accounts", "hostnames":
		if len(strings.Split(value, ",")) != 3 {
			return fmt.Errorf("enter exactly three comma-separated values in the requested order")
		}
		m.state.SetPreparation(f.key, value)
	case "external_cert", "internal_cert":
		if len(strings.Split(value, "|")) != 4 {
			return fmt.Errorf("enter four pipe-separated paths; the chain path may be blank")
		}
		m.state.SetPreparation(f.key, value)
	case "owner_accounts":
		if value != "" && len(strings.Split(value, ",")) != 3 {
			return fmt.Errorf("enter three comma-separated owner accounts or leave blank")
		}
		if value == "" {
			value = ",,"
		}
		m.state.SetPreparation(f.key, value)
	case "throttle_port":
		if value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 65535 {
				return fmt.Errorf("distributed throttle port must be between 1 and 65535")
			}
		}
		m.state.SetPreparation(f.key, value)
	case "throttle_type":
		value = strings.ToLower(value)
		if value != "redis" && value != "valkey" {
			return fmt.Errorf("choose redis or valkey")
		}
		m.state.SetPreparation(f.key, value)
	case "throttle_replication", "throttle_tls":
		value = strings.ToLower(value)
		if value != "yes" && value != "no" {
			return fmt.Errorf("choose yes or no")
		}
		m.state.SetPreparation(f.key, value)
	default:
		m.state.SetPreparation(f.key, value)
	}
	return nil
}

func (m *Model) editableSteps() []int {
	steps := make([]int, 0, 11+len(preparationFields))
	for step := 0; step <= 10; step++ {
		steps = append(steps, step)
	}
	for step := preparationFirstStep; step <= preparationLastField; step++ {
		if m.preparationFieldVisible(step) {
			steps = append(steps, step)
		}
	}
	return steps
}

func (m *Model) runPreparation() tea.Cmd {
	if m.preparing {
		return nil
	}
	cfg := m.preparationConfig()
	if cfg.Oracle.SchemaMode == "" {
		m.errText = "Choose prepared or initialize schema mode before export."
		return nil
	}
	if err := config.Validate(cfg); err != nil {
		m.errText = err.Error()
		return nil
	}
	if m.state.CheckOracle {
		for _, key := range []string{"APIM", "SHARED", "KEYCLOAK"} {
			if m.state.OracleCredentials[key].Password == "" {
				m.errText = "Oracle validation is enabled; enter all three runtime passwords before export."
				return nil
			}
		}
		for key, account := range map[string]string{"APIM_OWNER": cfg.Oracle.APIMOwnerAccount, "SHARED_OWNER": cfg.Oracle.SharedOwnerAccount, "KEYCLOAK_OWNER": cfg.Oracle.KeycloakOwnerAccount} {
			if account != "" && m.state.OracleCredentials[key].Password == "" {
				m.errText = "Oracle validation is enabled; enter a password for each configured schema-owner account."
				return nil
			}
		}
	}
	if m.state.OutputDirectory == "" || m.state.StateDirectory == "" {
		m.errText = "Set separate public output and protected state directories before export."
		return nil
	}
	if samePath(m.state.OutputDirectory, m.state.StateDirectory) {
		m.errText = "Public output and protected state directories must be separate."
		return nil
	}
	m.preparing = true
	m.state.PreparationStatus = "RUNNING"
	m.state.PreparationReport = nil
	m.preparationGeneration++
	generation, revision := m.preparationGeneration, m.state.PreparationRevision
	checkOracle := m.state.CheckOracle
	credentials := make(map[string]preparation.OracleCredential, len(m.state.OracleCredentials))
	for key, value := range m.state.OracleCredentials {
		credentials[key] = value
	}
	ctx := m.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	workCtx, cancel := context.WithCancel(ctx)
	m.preparationCancel = cancel
	outputDir, stateDir := m.state.OutputDirectory, m.state.StateDirectory
	m.service.Log.Event("INFO", "stage3-prepare", "Local preparation started", map[string]string{"revision": fmt.Sprint(revision), "oracle_check_requested": fmt.Sprint(checkOracle)})
	return func() tea.Msg {
		bounded, done := context.WithTimeout(workCtx, 10*time.Minute)
		defer done()
		report, err := preparation.Run(bounded, preparation.Options{Config: cfg, OutputDir: outputDir, StateDir: stateDir, CheckOracle: checkOracle, OracleCredentials: credentials, OracleValidator: m.oracleValidator})
		return preparationFinishedMsg{generation: generation, revision: revision, report: report, err: err}
	}
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	return errA == nil && errB == nil && strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}

func defaultStateDirectory() string {
	base, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "SysdevWso2AirgapInstaller", "state")
}
