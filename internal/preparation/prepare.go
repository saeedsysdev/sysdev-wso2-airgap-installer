package preparation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/certprep"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/deploymentassets"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/oracleprep"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/schemaartifacts"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/secretstore"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/version"
)

type Options struct {
	Config              config.File
	OutputDir, StateDir string
	CheckOracle         bool
	Now                 func() time.Time
}
type Report struct {
	Application               string                  `json:"application"`
	Version                   string                  `json:"version"`
	ConfigurationSchema       int                     `json:"configuration_schema"`
	SourceConfigurationSchema int                     `json:"source_configuration_schema"`
	ReadOnly                  bool                    `json:"read_only"`
	OracleTarget              OracleTarget            `json:"oracle_target"`
	Oracle                    oracleprep.Result       `json:"oracle"`
	SchemaArtifacts           *schemaartifacts.Result `json:"schema_artifacts,omitempty"`
	Certificates              certprep.Result         `json:"certificates"`
	Assets                    AssetSummary            `json:"assets"`
	Files                     map[string]string       `json:"file_sha256"`
	Blockers                  []string                `json:"blockers,omitempty"`
	Deferred                  []string                `json:"deferred,omitempty"`
	Qualification             string                  `json:"qualification"`
}
type OracleTarget struct {
	ConnectionMode          string            `json:"connection_mode"`
	Host                    string            `json:"host,omitempty"`
	Port                    int               `json:"port,omitempty"`
	ServiceName             string            `json:"service_name,omitempty"`
	SchemaMode              string            `json:"schema_mode"`
	Schemas                 map[string]string `json:"schemas"`
	OwnerAccountsConfigured bool              `json:"owner_accounts_configured"`
	TrustConfigured         bool              `json:"trust_configured"`
}
type AssetSummary struct {
	SHA256   map[string]string                  `json:"sha256"`
	Bindings []deploymentassets.DeferredBinding `json:"deferred_bindings"`
}

// Run builds a redacted review and deployment assets in a new output directory.
// Protected keys remain in StateDir; the method never contacts Kubernetes or
// mutates an external service. Oracle access is strictly SELECT-only and opt-in.
func Run(ctx context.Context, o Options) (Report, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.OutputDir == "" || o.StateDir == "" {
		return Report{}, errors.New("output and protected state directories are required")
	}
	if err := config.Validate(o.Config); err != nil {
		return Report{}, err
	}
	out, err := filepath.Abs(o.OutputDir)
	if err != nil {
		return Report{}, errors.New("invalid output path")
	}
	state, err := filepath.Abs(o.StateDir)
	if err != nil {
		return Report{}, errors.New("invalid protected state path")
	}
	if sameOrWithin(out, state) || sameOrWithin(state, out) {
		return Report{}, errors.New("public output and protected state directories must be separate")
	}
	if _, err := os.Lstat(out); err == nil {
		return Report{}, errors.New("output already exists; refusing to overwrite preparation assets")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Report{}, errors.New("cannot inspect output path")
	}
	parent := filepath.Dir(out)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return Report{}, errors.New("create output parent")
	}
	stage, err := os.MkdirTemp(parent, ".sysdev-prepare-*")
	if err != nil {
		return Report{}, errors.New("create temporary preparation directory")
	}
	defer os.RemoveAll(stage)
	store, err := secretstore.Open(state)
	if err != nil {
		return Report{}, err
	}
	key, err := store.GetOrCreateSharedEncryptionKey()
	if err != nil {
		return Report{}, err
	}
	clear(key)

	r := Report{Application: "Sysdev Wso2 Airgap Installer", Version: version.Current, ConfigurationSchema: config.SchemaVersion, SourceConfigurationSchema: o.Config.SourceSchemaVersion, ReadOnly: true, OracleTarget: OracleTarget{ConnectionMode: o.Config.Oracle.ConnectionMode, Host: o.Config.Oracle.Host, Port: o.Config.Oracle.Port, ServiceName: o.Config.Oracle.ServiceName, SchemaMode: o.Config.Oracle.SchemaMode, Schemas: map[string]string{"wso2_apim": o.Config.Oracle.APIMSchema, "wso2_shared": o.Config.Oracle.SharedSchema, "keycloak": o.Config.Oracle.KeycloakSchema}, OwnerAccountsConfigured: o.Config.Oracle.APIMOwnerAccount != "" && o.Config.Oracle.SharedOwnerAccount != "" && o.Config.Oracle.KeycloakOwnerAccount != "", TrustConfigured: o.Config.Oracle.WalletPath != "" || o.Config.Oracle.TrustBundlePath != ""}, Oracle: oracleprep.Result{Status: "DEFERRED", ReadOnly: true, Deferred: []string{"Oracle credentials were not supplied to this local preparation run; authenticated validation was not performed."}}, Files: map[string]string{}, Qualification: "PREPARATION ONLY; Apply unavailable; vendor integration and runtime qualification pending"}
	if o.Config.Oracle.SchemaMode != "" {
		accounts := oracleAccounts(o.Config.Oracle)
		if o.CheckOracle {
			accounts = envAccounts(accounts)
			r.Oracle = (oracleprep.Validator{}).Validate(ctx, o.Config.Oracle, accounts)
		} else {
			r.Oracle.Deferred = append(r.Oracle.Deferred, "Use --check-oracle to run bounded authenticated read-only SELECT validation.")
		}
		if o.Config.Oracle.SchemaMode == "initialize" {
			r.Oracle.Deferred = append(r.Oracle.Deferred, "Schema-owner credentials and exact release grants are not validated by the runtime-account metadata check; Stage 4 must verify them before any approved DDL.")
		}
	}
	if o.Config.Artifacts.SchemaManifestPath != "" {
		schema, e := schemaartifacts.Verify(ctx, o.Config.Artifacts.SchemaManifestPath, registry.Profile.WSO2)
		r.SchemaArtifacts = &schema
		if e != nil {
			r.Blockers = append(r.Blockers, "WSO2 schema artifact verification failed: "+safeError(e))
		} else {
			manifest, loadErr := schemaartifacts.Load(o.Config.Artifacts.SchemaManifestPath)
			if loadErr != nil {
				r.Blockers = append(r.Blockers, "WSO2 schema artifact manifest could not be loaded after verification.")
			} else {
				if o.Config.Artifacts.WSO2DistributionSHA256 != "" && !strings.EqualFold(o.Config.Artifacts.WSO2DistributionSHA256, manifest.SourceSHA256) {
					r.Blockers = append(r.Blockers, "Configured WSO2 distribution SHA-256 does not match the schema artifact manifest.")
				}
				if o.Config.Artifacts.WSO2DistributionPath != "" {
					source := filepath.Join(filepath.Dir(o.Config.Artifacts.SchemaManifestPath), filepath.FromSlash(manifest.SourceArchive))
					a, _ := filepath.Abs(source)
					b, _ := filepath.Abs(o.Config.Artifacts.WSO2DistributionPath)
					if !strings.EqualFold(a, b) {
						r.Blockers = append(r.Blockers, "Configured WSO2 distribution path does not match the verified manifest source archive.")
					}
				}
				for _, script := range schema.Scripts {
					if (strings.EqualFold(script.TargetSchema, o.Config.Oracle.APIMSchema) || strings.EqualFold(script.TargetSchema, o.Config.Oracle.SharedSchema)) == false {
						r.Blockers = append(r.Blockers, "Verified WSO2 SQL target schema does not match the configured APIM/shared schema targets.")
						break
					}
				}
			}
		}
	} else if o.Config.Oracle.SchemaMode == "initialize" {
		r.Blockers = append(r.Blockers, "Initialization selected but no locally supplied schema artifact manifest is configured.")
	}

	names := certprep.Names{Namespace: o.Config.Kubernetes.Namespace, ClusterDomain: o.Config.Deployment.ClusterDNSDomain, APIMHost: o.Config.Certificates.APIMHostname, GatewayHost: o.Config.Certificates.GatewayHostname, KeycloakHost: o.Config.Certificates.KeycloakHostname}
	certOut := filepath.Join(stage, "certificates")
	switch o.Config.Certificates.Mode {
	case "generate":
		r.Certificates, err = certprep.Generate(state, certOut, names)
	case "import":
		r.Certificates, err = certprep.PrepareImport(o.Config.Certificates, names, state, o.Now())
		if err == nil {
			err = copyPublicCertificates(o.Config.Certificates, certOut)
		}
	default:
		err = errors.New("certificate mode is required")
	}
	if err != nil {
		return r, fmt.Errorf("certificate preparation failed: %w", err)
	}

	assets, err := deploymentassets.Render(deploymentassets.Input{Config: o.Config, SelectedWorkers: append([]string(nil), o.Config.Kubernetes.SelectedWorkers...)})
	if err != nil {
		return r, fmt.Errorf("render deployment preparation assets: %w", err)
	}
	r.Assets = AssetSummary{SHA256: map[string]string{}, Bindings: append([]deploymentassets.DeferredBinding(nil), assets.Bindings...)}
	for name, data := range assets.Files {
		if err := writeSafe(stage, filepath.Join("assets", filepath.FromSlash(name)), data, 0644); err != nil {
			return r, err
		}
	}
	redacted := o.Config
	redacted.SourceSchemaVersion = 0
	configData, err := json.MarshalIndent(redacted, "", "  ")
	if err != nil {
		return r, errors.New("encode redacted configuration")
	}
	if err := writeSafe(stage, "configuration.redacted.json", append(configData, '\n'), 0644); err != nil {
		return r, err
	}
	reportPath := filepath.Join(stage, "review.json")
	// Hash public output files before adding metadata/checksum documents.
	files, err := hashTree(stage)
	if err != nil {
		return r, err
	}
	r.Files = files
	for name, digest := range files {
		if strings.HasPrefix(name, "assets/") {
			r.Assets.SHA256[strings.TrimPrefix(name, "assets/")] = digest
		}
	}
	for _, b := range assets.Blockers {
		r.Blockers = append(r.Blockers, b)
	}
	for _, d := range assets.Deferred {
		r.Deferred = append(r.Deferred, d)
	}
	for _, d := range r.Oracle.Deferred {
		r.Deferred = append(r.Deferred, d)
	}
	for _, b := range r.Oracle.Blockers {
		r.Blockers = append(r.Blockers, b)
	}
	if r.SchemaArtifacts != nil {
		r.Blockers = append(r.Blockers, r.SchemaArtifacts.Blockers...)
		r.Deferred = append(r.Deferred, r.SchemaArtifacts.Deferred...)
	}
	if r.Certificates.TrustDistribution != "" {
		r.Deferred = append(r.Deferred, r.Certificates.TrustDistribution)
	}
	sort.Strings(r.Blockers)
	sort.Strings(r.Deferred)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return r, errors.New("encode preparation review")
	}
	if err := writeSafe(stage, "review.json", append(data, '\n'), 0644); err != nil {
		return r, err
	}
	if err := writeSafe(stage, "metadata.json", []byte(fmt.Sprintf("{\n  \"application\": \"Sysdev Wso2 Airgap Installer\",\n  \"version\": %q,\n  \"configuration_schema\": %d,\n  \"qualification\": %q\n}\n", version.Current, config.SchemaVersion, r.Qualification)), 0644); err != nil {
		return r, err
	}
	_ = reportPath
	checksums, err := hashTree(stage)
	if err != nil {
		return r, err
	}
	var checksumText strings.Builder
	checksumNames := make([]string, 0, len(checksums))
	for n := range checksums {
		checksumNames = append(checksumNames, n)
	}
	sort.Strings(checksumNames)
	for _, n := range checksumNames {
		fmt.Fprintf(&checksumText, "%s  %s\n", checksums[n], n)
	}
	if err := writeSafe(stage, "checksums.sha256", []byte(checksumText.String()), 0644); err != nil {
		return r, err
	}
	if _, err := os.Lstat(out); err == nil {
		return r, errors.New("output appeared during preparation; refusing overwrite")
	}
	if err := os.Rename(stage, out); err != nil {
		return r, errors.New("atomically publish local preparation directory")
	}
	return r, nil
}

func oracleAccounts(o config.Oracle) []oracleprep.Account {
	a := []oracleprep.Account{{Purpose: "WSO2 APIM runtime", CredentialKey: "APIM", Username: o.APIMRuntimeAccount, TargetSchema: o.APIMSchema}, {Purpose: "WSO2 shared runtime", CredentialKey: "SHARED", Username: o.SharedRuntimeAccount, TargetSchema: o.SharedSchema}, {Purpose: "Keycloak runtime", CredentialKey: "KEYCLOAK", Username: o.KeycloakRuntimeAccount, TargetSchema: o.KeycloakSchema}}
	if o.APIMOwnerAccount != "" {
		a = append(a, oracleprep.Account{Purpose: "WSO2 APIM schema owner", CredentialKey: "APIM_OWNER", Username: o.APIMOwnerAccount, TargetSchema: o.APIMSchema})
	}
	if o.SharedOwnerAccount != "" {
		a = append(a, oracleprep.Account{Purpose: "WSO2 shared schema owner", CredentialKey: "SHARED_OWNER", Username: o.SharedOwnerAccount, TargetSchema: o.SharedSchema})
	}
	if o.KeycloakOwnerAccount != "" {
		a = append(a, oracleprep.Account{Purpose: "Keycloak schema owner", CredentialKey: "KEYCLOAK_OWNER", Username: o.KeycloakOwnerAccount, TargetSchema: o.KeycloakSchema})
	}
	return a
}
func envAccounts(in []oracleprep.Account) []oracleprep.Account {
	for i := range in {
		if user := os.Getenv("SYSDEV_ORACLE_" + in[i].CredentialKey + "_USERNAME"); user != "" {
			in[i].Username = user
		}
		in[i].Password = os.Getenv("SYSDEV_ORACLE_" + in[i].CredentialKey + "_PASSWORD")
	}
	return in
}
func safeError(error) string {
	return "input validation or artifact processing failed; inspect sanitized review details"
}
func sameOrWithin(a, b string) bool {
	rel, e := filepath.Rel(a, b)
	return e == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))))
}
func writeSafe(root, rel string, data []byte, mode os.FileMode) error {
	if filepath.IsAbs(rel) || strings.Contains(rel, ".."+string(filepath.Separator)) {
		return errors.New("unsafe generated output path")
	}
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return errors.New("create generated asset directory")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return errors.New("create generated public asset")
	}
	_, e := f.Write(data)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return errors.New("write generated public asset")
	}
	return nil
}
func hashTree(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		sum := sha256.Sum256(b)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	return out, err
}
func copyPublicCertificates(c config.Certificates, out string) error {
	if err := os.Mkdir(out, 0700); err != nil {
		return errors.New("create certificate output")
	}
	pairs := []struct{ name, path string }{{"external.crt", c.ExternalCertificatePath}, {"external-chain.crt", c.ExternalChainPath}, {"external-trust.crt", c.ExternalTrustPath}, {"internal.crt", c.InternalCertificatePath}, {"internal-chain.crt", c.InternalChainPath}, {"internal-trust.crt", c.InternalTrustPath}}
	for _, p := range pairs {
		if p.path == "" {
			continue
		}
		b, e := os.ReadFile(p.path)
		if e != nil {
			return errors.New("read imported public certificate material")
		}
		if len(b) > 16<<20 {
			return errors.New("imported certificate material exceeds limit")
		}
		if e := writeSafe(out, p.name, b, 0644); e != nil {
			return e
		}
	}
	return nil
}
