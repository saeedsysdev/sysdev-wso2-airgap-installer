package wizard

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/app"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/auditlog"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/oracleprep"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/preparation"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

func newPreparationModel(t *testing.T) (*Model, string) {
	t.Helper()
	root := t.TempDir()
	log, err := auditlog.New(filepath.Join(root, "logs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	m := New(app.New(registry.NewClient(), log), true, log.Path())
	m.state.RegistryHost = "registry.example"
	m.state.Project = "team"
	m.state.Namespace = "wso2"
	m.state.Revision = 7
	m.state.InventoryStatus = "PASSED"
	m.state.InventoryRevision = 7
	m.state.PreparationConfig = config.File{
		SchemaVersion: config.SchemaVersion,
		Oracle:        config.Oracle{Host: "oracle.example", Port: 1521, ServiceName: "W2DB", ConnectionMode: "tcp", SchemaMode: "prepared", APIMSchema: "WSO2_APIM", SharedSchema: "WSO2_SHARED", KeycloakSchema: "KEYCLOAK", APIMRuntimeAccount: "WSO2_APIM", SharedRuntimeAccount: "WSO2_SHARED", KeycloakRuntimeAccount: "KEYCLOAK"},
		Certificates:  config.Certificates{Mode: "generate", APIMHostname: "apim.example.test", GatewayHostname: "api.example.test", KeycloakHostname: "sso.example.test"},
		Deployment:    config.Deployment{ClusterDNSDomain: "cluster.local"},
	}
	m.state.OutputDirectory = filepath.Join(root, "public-export")
	m.state.StateDirectory = filepath.Join(root, "protected-state")
	return m, log.Path()
}

func executePreparation(t *testing.T, m *Model) {
	t.Helper()
	cmd := m.runPreparation()
	if cmd == nil {
		t.Fatalf("preparation did not start: %s", m.errText)
	}
	msg := cmd()
	m.Update(msg)
}

func TestWizardPreparationExportsThroughSharedPreparationService(t *testing.T) {
	m, _ := newPreparationModel(t)
	// These are local review inputs only; the already-passed image inventory is retained.
	m.state.SetPreparation("schema_mode", "prepared")
	m.state.SetPreparation("cert_mode", "generate")
	m.state.SetPreparation("output_dir", m.state.OutputDirectory)
	m.state.SetPreparation("state_dir", m.state.StateDirectory)
	executePreparation(t, m)
	if m.state.PreparationStatus != "GENERATED" || m.state.PreparationReport == nil {
		t.Fatalf("wizard did not retain preparation result: status=%s error=%s", m.state.PreparationStatus, m.errText)
	}
	if m.state.PreparationReport.FileGenerationStatus != "GENERATED" || !strings.HasPrefix(m.state.PreparationReport.DeploymentReadiness, "BLOCKED") {
		t.Fatalf("file generation and readiness were not reported separately: %#v", m.state.PreparationReport)
	}
	for _, name := range []string{"review.json", "configuration.redacted.json", "checksums.sha256", "assets/kubernetes/resources.yaml"} {
		if _, err := os.Stat(filepath.Join(m.state.OutputDirectory, name)); err != nil {
			t.Errorf("shared preparation output missing %s: %v", name, err)
		}
	}
	if m.state.InventoryStatus != "PASSED" || m.state.InventoryRevision != m.state.Revision {
		t.Fatal("Stage 3 preparation invalidated the unrelated registry review")
	}
	if m.state.PreparationReport.Oracle.Status != "DEFERRED" {
		t.Fatalf("Oracle should be deferred when not requested: %#v", m.state.PreparationReport.Oracle)
	}
}

func TestStage3FormEntryReachesSharedPreparationService(t *testing.T) {
	m, _ := newPreparationModel(t)
	m.step = preparationFirstStep
	m.input = m.preparationValue(m.step)
	for m.step >= preparationFirstStep && m.step <= preparationLastField {
		if m.step == preparationFirstStep+12 { // schemas
			m.input = "WSO2_APIM_EDITED,WSO2_SHARED,KEYCLOAK"
		}
		m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
		if m.errText != "" {
			t.Fatalf("wizard rejected Stage 3 form input at step %d: %s", m.step, m.errText)
		}
		if m.step <= preparationLastField {
			m.input = m.preparationValue(m.step)
		}
	}
	if m.step != preparationReviewStep {
		t.Fatalf("Stage 3 form did not reach review: step=%d", m.step)
	}
	_, cmd := m.Update(tea.KeyPressMsg(tea.Key{Text: "e", Code: 'e'}))
	if cmd == nil {
		t.Fatalf("explicit export action did not start preparation: %s", m.errText)
	}
	m.Update(cmd())
	if m.state.PreparationReport == nil {
		t.Fatalf("form inputs did not reach preparation service: %s", m.errText)
	}
	data, err := os.ReadFile(filepath.Join(m.state.OutputDirectory, "configuration.redacted.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "WSO2_APIM_EDITED") {
		t.Fatal("edited wizard schema mapping was not passed to the shared preparation service")
	}
}

func TestWizardInitializeWithoutSchemaManifestExportsBlockedReview(t *testing.T) {
	m, _ := newPreparationModel(t)
	m.state.SetPreparation("schema_mode", "initialize")
	executePreparation(t, m)
	report := m.state.PreparationReport
	if report == nil || report.FileGenerationStatus != "GENERATED" {
		t.Fatalf("expected local files to be generated despite a qualification blocker: %#v", report)
	}
	if !strings.HasPrefix(report.DeploymentReadiness, "BLOCKED") {
		t.Fatalf("missing schema artifacts did not block readiness: %q", report.DeploymentReadiness)
	}
	joined := strings.Join(report.Blockers, " ")
	if !strings.Contains(joined, "no locally supplied schema artifact manifest") {
		t.Fatalf("missing schema manifest blocker not shown: %v", report.Blockers)
	}
}

func TestWizardOracleCanaryStaysInMemoryAndOutOfPublicExportAndLog(t *testing.T) {
	const canary = "wizard-oracle-password-canary"
	m, logPath := newPreparationModel(t)
	t.Setenv("SYSDEV_ORACLE_APIM_PASSWORD", "environment-value-must-not-be-used")
	m.state.CheckOracle = true
	m.state.OracleCredentials = map[string]preparation.OracleCredential{
		"APIM":     {Username: "WSO2_APIM", Password: canary},
		"SHARED":   {Username: "WSO2_SHARED", Password: "shared-canary"},
		"KEYCLOAK": {Username: "KEYCLOAK", Password: "keycloak-canary"},
	}
	var seen string
	m.oracleValidator = oracleprep.Validator{Open: func(_ context.Context, _ config.Oracle, account oracleprep.Account, _ time.Duration) (oracleprep.Session, error) {
		if account.CredentialKey == "APIM" {
			seen = account.Password
		}
		return nil, errors.New(canary)
	}}
	secretStep := preparationFirstStep + 16
	m.step = secretStep
	m.input = canary
	if got := m.View().Content; strings.Contains(got, canary) {
		t.Fatal("Oracle password appeared in wizard rendering")
	}
	executePreparation(t, m)
	if seen != canary {
		t.Fatal("per-run credential was not passed to the Oracle validation boundary")
	}
	key, err := os.ReadFile(filepath.Join(m.state.StateDirectory, "wso2-shared-encryption-key.bin"))
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{logPath}
	if err := filepath.WalkDir(m.state.OutputDirectory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{canary, "shared-canary", "keycloak-canary", "environment-value-must-not-be-used", "BEGIN PRIVATE KEY"} {
			if strings.Contains(string(data), secret) {
				t.Fatalf("sensitive material leaked into %s", path)
			}
		}
		if bytes.Contains(data, key) || strings.Contains(string(data), hex.EncodeToString(key)) {
			t.Fatalf("shared encryption key leaked into %s", path)
		}
	}
}

func TestPreparationEditInvalidationPreservesUnrelatedReviewAndRejectsStaleOrCanceledResult(t *testing.T) {
	m, _ := newPreparationModel(t)
	report := m.state.PreparationReport
	if report != nil {
		t.Fatal("test precondition")
	}
	m.state.PreparationStatus = "GENERATED"
	m.state.SetPreparation("cluster_dns", "svc.example.test")
	if m.state.PreparationStatus != "STALE" || m.state.PreparationReport != nil {
		t.Fatal("hostname/DNS edit retained a stale preparation result")
	}
	if m.state.InventoryStatus != "PASSED" || m.state.InventoryRevision != m.state.Revision {
		t.Fatal("Stage 3 edit invalidated registry evidence")
	}
	m.state.PreparationStatus = "RUNNING"
	m.preparing = true
	m.preparationGeneration = 4
	m.preparationCancel = func() {}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '\x1b'}))
	if m.preparing || m.state.PreparationStatus != "CANCELED" {
		t.Fatal("Escape did not cancel the preparation state")
	}
	late := preparationFinishedMsg{generation: 4, revision: m.state.PreparationRevision, report: preparation.Report{FileGenerationStatus: "GENERATED"}}
	m.Update(late)
	if m.state.PreparationStatus != "CANCELED" || m.state.PreparationReport != nil {
		t.Fatal("late result replaced canceled preparation")
	}
}

func TestCancelBeforePreparationCommandRunsCreatesNoExport(t *testing.T) {
	m, _ := newPreparationModel(t)
	cmd := m.runPreparation()
	if cmd == nil {
		t.Fatalf("preparation did not start: %s", m.errText)
	}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: '\x1b'}))
	if m.state.PreparationStatus != "CANCELED" {
		t.Fatal("canceled UI state was not recorded")
	}
	late := cmd()
	m.Update(late)
	if m.state.PreparationStatus != "CANCELED" || m.state.PreparationReport != nil {
		t.Fatal("canceled command was presented as successful")
	}
	if _, err := os.Stat(m.state.OutputDirectory); !os.IsNotExist(err) {
		t.Fatalf("canceled command created a public export: %v", err)
	}
}

func TestPreparationConditionalFieldsAndEditMenuIncludeStage3Inputs(t *testing.T) {
	m, _ := newPreparationModel(t)
	m.state.OracleTargetKind = "descriptor"
	if m.preparationFieldVisible(preparationFirstStep+1) || !m.preparationFieldVisible(preparationFirstStep+4) {
		t.Fatal("Oracle target selection did not conditionally show descriptor inputs")
	}
	m.state.PreparationConfig.Oracle.ConnectionMode = "tcps"
	if !m.preparationFieldVisible(preparationFirstStep + 6) {
		t.Fatal("TCPS inputs are not shown")
	}
	m.state.PreparationConfig.Certificates.Mode = "import"
	if !m.preparationFieldVisible(preparationFirstStep + 25) {
		t.Fatal("certificate import paths are not shown")
	}
	if m.preparationFieldVisible(preparationLastField) {
		t.Fatal("distributed throttle trust path appeared while TLS is disabled")
	}
	m.state.PreparationConfig.DistributedThrottle.Host = "redis.example.test"
	m.state.PreparationConfig.DistributedThrottle.TLS = true
	if !m.preparationFieldVisible(preparationLastField) {
		t.Fatal("distributed throttle TLS trust path was not shown")
	}
	steps := m.editableSteps()
	found := false
	for _, step := range steps {
		if step == preparationFirstStep+29 {
			found = true
		}
	}
	if !found {
		t.Fatal("Ctrl-E edit menu omitted the schema artifact manifest input")
	}
}
