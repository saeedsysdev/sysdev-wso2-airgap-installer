package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/preparation"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

type fixtureAdapter struct {
	target      Target
	artifacts   map[string]string
	calls       []string
	fail        string
	uncertain   bool
	reconciled  bool
	importCount int
}

func (f *fixtureAdapter) Recheck(_ context.Context, p Plan) (Observation, error) {
	f.calls = append(f.calls, "recheck")
	if f.fail == "conflict" {
		return Observation{}, errors.New("ownership conflict")
	}
	return Observation{Target: f.target, ArtifactDigests: f.artifacts}, nil
}
func (f *fixtureAdapter) Reconcile(_ context.Context, _ Plan, operation string) (bool, error) {
	f.calls = append(f.calls, "reconcile:"+operation)
	return f.reconciled, nil
}
func (f *fixtureAdapter) ImportMissing(_ context.Context, p Plan) (map[string]string, error) {
	f.calls = append(f.calls, "images")
	f.importCount++
	for _, im := range p.Images {
		if im.Action != "USE EXISTING REGISTRY IMAGE; NO PUSH" {
			return nil, errors.New("registry-only path attempted an unreviewed push")
		}
	}
	if f.fail == "images" {
		return nil, errors.New("image recheck failed")
	}
	return map[string]string{"image_digest_apim": strings.Repeat("a", 64)}, nil
}
func (f *fixtureAdapter) PrepareOracle(context.Context, Plan) error {
	f.calls = append(f.calls, "oracle")
	if f.uncertain {
		f.uncertain = false
		return UncertainDDLError{Step: "schema-script-01"}
	}
	if f.fail == "oracle" {
		return errors.New("Oracle access denied")
	}
	return nil
}
func (f *fixtureAdapter) ApplyKubernetes(context.Context, Plan) error {
	f.calls = append(f.calls, "kubernetes")
	if f.fail == "kubernetes" {
		return errors.New("resource ownership conflict")
	}
	return nil
}
func (f *fixtureAdapter) WaitReady(context.Context, Plan) (map[string]string, error) {
	f.calls = append(f.calls, "readiness")
	if f.fail == "readiness" {
		return nil, errors.New("workload not ready")
	}
	return map[string]string{"component_status": "READY"}, nil
}

func fixturePlan(t *testing.T) (Plan, *fixtureAdapter) {
	t.Helper()
	root := t.TempDir()
	prepared, state := filepath.Join(root, "prepared"), filepath.Join(root, "protected")
	if err := os.MkdirAll(prepared, 0700); err != nil {
		t.Fatal(err)
	}
	artifactName := "assets/resources.yaml"
	data := []byte("apiVersion: v1\nkind: ConfigMap\n")
	if err := os.MkdirAll(filepath.Join(prepared, "assets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prepared, artifactName), data, 0600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	report := preparation.Report{FileGenerationStatus: "GENERATED", Files: map[string]string{artifactName: hex.EncodeToString(sum[:])}}
	review, _ := json.Marshal(report)
	if err := os.WriteFile(filepath.Join(prepared, "review.json"), review, 0600); err != nil {
		t.Fatal(err)
	}
	metadata := []byte("fixture preparation metadata\n")
	if err := os.WriteFile(filepath.Join(prepared, "metadata.json"), metadata, 0600); err != nil {
		t.Fatal(err)
	}
	reviewSum := sha256.Sum256(review)
	metadataSum := sha256.Sum256(metadata)
	checksums := hex.EncodeToString(sum[:]) + "  " + artifactName + "\n" + hex.EncodeToString(reviewSum[:]) + "  review.json\n" + hex.EncodeToString(metadataSum[:]) + "  metadata.json\n"
	if err := os.WriteFile(filepath.Join(prepared, "checksums.sha256"), []byte(checksums), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.File{SchemaVersion: config.SchemaVersion, Profile: registry.Profile.ID, Registry: config.Registry{Host: "registry.example.test", Project: "team"}, Kubernetes: config.Kubernetes{Namespace: "wso2"}}
	target := Target{Cluster: "fixture-cluster", Context: "fixture", APIEndpoint: "https://kube.example.test", Namespace: "wso2"}
	images := make([]registry.Image, 0, len(registry.Profile.Images))
	for i, required := range registry.Profile.Images {
		images = append(images, registry.Image{Component: required.Component, Reference: "registry.example.test/team/" + required.Repository + ":" + required.Tag, Status: registry.Found, Digest: "sha256:" + strings.Repeat(string(rune('a'+i)), 64), PlatformStatus: "VERIFIED", Platforms: []registry.PlatformEvidence{{Platform: registry.Platform{OS: "linux", Architecture: "amd64"}, Status: "VERIFIED"}}})
	}
	p, err := BuildPlan(cfg, prepared, state, target, images, nil)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &fixtureAdapter{target: target, artifacts: p.ArtifactDigests, reconciled: true}
	return p, adapter
}

func TestRegistryOnlyApplyUsesDigestPinnedPlanInOrder(t *testing.T) {
	p, adapter := fixturePlan(t)
	if len(p.Blockers) != 0 {
		t.Fatalf("fixture plan unexpectedly blocked: %v", p.Blockers)
	}
	for _, im := range p.Images {
		if im.Action != "USE EXISTING REGISTRY IMAGE; NO PUSH" || !strings.HasPrefix(im.Digest, "sha256:") {
			t.Fatalf("registry-only image was not digest pinned: %#v", im)
		}
	}
	if _, err := Apply(context.Background(), p, false, false, adapter); err == nil {
		t.Fatal("Apply ran without final authorization")
	}
	result, err := Apply(context.Background(), p, true, false, adapter)
	if err != nil || result.Status != "SUCCEEDED" {
		t.Fatalf("fixture Apply failed: %#v %v", result, err)
	}
	if got, want := adapter.calls, []string{"recheck", "images", "oracle", "kubernetes", "readiness"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("operation order=%v, want %v", got, want)
	}
	if adapter.importCount != 1 {
		t.Fatal("registry-only path did not pass exactly once through no-push import check")
	}
	if _, err := Apply(context.Background(), p, true, false, adapter); err == nil || adapter.importCount != 1 {
		t.Fatal("rerunning a completed plan repeated image work")
	}
	if _, err := CheckpointStatus(p.StateDirectory, p.ID); err != nil {
		t.Fatalf("protected status checkpoint missing: %v", err)
	}
}

func TestBlockedPlanAndChangedArtifactPerformNoWrites(t *testing.T) {
	p, adapter := fixturePlan(t)
	p.Blockers = []string{"missing exact release evidence"}
	p.ID = "sha256:" + planHash(p)
	if _, err := Apply(context.Background(), p, true, false, adapter); err == nil || adapter.importCount != 0 {
		t.Fatal("blocked plan performed writes")
	}
	p, adapter = fixturePlan(t)
	path := filepath.Join(p.PreparedDirectory, "assets", "resources.yaml")
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), p, true, false, adapter); err == nil || len(adapter.calls) != 0 {
		t.Fatal("changed prepared artifact was accepted")
	}
}

func TestChangedTargetAndOwnershipConflictStopBeforeMutation(t *testing.T) {
	p, adapter := fixturePlan(t)
	adapter.target.Context = "other-context"
	if _, err := Apply(context.Background(), p, true, false, adapter); err == nil || adapter.importCount != 0 {
		t.Fatal("changed target was accepted")
	}
	p, adapter = fixturePlan(t)
	adapter.fail = "conflict"
	if _, err := Apply(context.Background(), p, true, false, adapter); err == nil || adapter.importCount != 0 {
		t.Fatal("preflight ownership conflict did not stop execution")
	}
}

func TestResumeReconcilesCompletedStepsAndDoesNotDuplicateImageWork(t *testing.T) {
	p, adapter := fixturePlan(t)
	adapter.fail = "kubernetes"
	if _, err := Apply(context.Background(), p, true, false, adapter); err == nil {
		t.Fatal("expected Kubernetes fixture failure")
	}
	adapter.fail = ""
	result, err := Apply(context.Background(), p, true, true, adapter)
	if err != nil || result.Status != "SUCCEEDED" || adapter.importCount != 1 {
		t.Fatalf("resume duplicated or failed completed work: result=%#v import=%d err=%v", result, adapter.importCount, err)
	}
	if !containsCall(adapter.calls, "reconcile:images") || !containsCall(adapter.calls, "reconcile:oracle") {
		t.Fatalf("resume did not reconcile completed steps: %v", adapter.calls)
	}
}

func TestUncertainDDLRequiresReconciliationBeforeResume(t *testing.T) {
	p, adapter := fixturePlan(t)
	adapter.uncertain = true
	if _, err := Apply(context.Background(), p, true, false, adapter); err == nil {
		t.Fatal("expected uncertain DDL result")
	}
	adapter.reconciled = false
	if _, err := Apply(context.Background(), p, true, true, adapter); err == nil {
		t.Fatal("uncertain DDL was replayed without reconciliation")
	}
	before := adapter.importCount
	adapter.reconciled = true
	result, err := Apply(context.Background(), p, true, true, adapter)
	if err != nil || result.Status != "SUCCEEDED" || adapter.importCount != before {
		t.Fatalf("reconciled DDL did not resume without duplicating prior work: %#v %v", result, err)
	}
}

func TestFailedReadinessIsCheckpointedForResumeAndResultsAreRedacted(t *testing.T) {
	p, adapter := fixturePlan(t)
	adapter.fail = "readiness"
	if _, err := Apply(context.Background(), p, true, false, adapter); err == nil {
		t.Fatal("expected readiness failure")
	}
	cp, err := CheckpointStatus(p.StateDirectory, p.ID)
	if err != nil || cp.Completed["readiness"] || cp.Completed["kubernetes"] != true {
		t.Fatalf("incorrect readiness checkpoint: %#v %v", cp, err)
	}
	data, err := os.ReadFile(filepath.Join(p.StateDirectory, "apply-checkpoint-"+strings.TrimPrefix(p.ID, "sha256:")+".json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"canary-password", "PRIVATE KEY", "encryption-key-material"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("checkpoint leaked sensitive material %q", secret)
		}
	}
}

func TestCanceledApplyCanResumeAtOperationBoundary(t *testing.T) {
	p, adapter := fixturePlan(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Apply(ctx, p, true, false, adapter); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not reported: %v", err)
	}
	result, err := Apply(context.Background(), p, true, true, adapter)
	if err != nil || result.Status != "SUCCEEDED" {
		t.Fatalf("canceled operation could not resume: %#v %v", result, err)
	}
}

func containsCall(calls []string, target string) bool {
	for _, call := range calls {
		if call == target {
			return true
		}
	}
	return false
}
