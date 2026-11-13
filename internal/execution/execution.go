// Package execution owns the reviewed-plan identity, protected progress
// journal, and shared Apply sequence. Environment adapters are deliberately
// injected so the orchestration can be tested without touching infrastructure.
package execution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/preparation"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/secretstore"
)

const PlanVersion = 1

var registryDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var fileDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Image struct {
	Component string   `json:"component"`
	Reference string   `json:"reference"`
	Digest    string   `json:"digest"`
	Platforms []string `json:"platforms"`
	Action    string   `json:"action"`
}

// Plan contains no credential values. File paths and target identifiers are
// retained because the reviewed plan is tied to one local installation.
type Plan struct {
	Version           int               `json:"execution_plan_version"`
	ID                string            `json:"plan_id"`
	CreatedAt         time.Time         `json:"created_at"`
	Configuration     config.File       `json:"configuration"`
	ConfigurationHash string            `json:"configuration_sha256"`
	PreparedDirectory string            `json:"prepared_directory"`
	StateDirectory    string            `json:"protected_state_directory"`
	Target            Target            `json:"target"`
	RegistryHost      string            `json:"registry_host"`
	RegistryProject   string            `json:"registry_project,omitempty"`
	Profile           string            `json:"profile"`
	ArtifactDigests   map[string]string `json:"artifact_sha256"`
	Images            []Image           `json:"images"`
	SchemaActions     []string          `json:"schema_actions"`
	KubernetesChanges []string          `json:"kubernetes_changes"`
	Blockers          []string          `json:"blockers,omitempty"`
}

type Target struct {
	Cluster     string `json:"cluster"`
	Context     string `json:"context"`
	APIEndpoint string `json:"api_endpoint"`
	Namespace   string `json:"namespace"`
}

type Checkpoint struct {
	Version         int               `json:"checkpoint_version"`
	PlanID          string            `json:"plan_id"`
	OperationID     string            `json:"operation_id"`
	Status          string            `json:"status"`
	StartedAt       time.Time         `json:"started_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	Completed       map[string]bool   `json:"completed,omitempty"`
	Uncertain       string            `json:"uncertain,omitempty"`
	UncertainDetail string            `json:"uncertain_detail,omitempty"`
	Results         map[string]string `json:"results,omitempty"`
}

type Observation struct {
	Target          Target
	ArtifactDigests map[string]string
}

type Adapter interface {
	// Recheck is read-only: it verifies target identity, current registry state,
	// permissions, drift, and any supported server-side dry runs.
	Recheck(context.Context, Plan) (Observation, error)
	Reconcile(context.Context, Plan, string) (bool, error)
	ImportMissing(context.Context, Plan) (map[string]string, error)
	PrepareOracle(context.Context, Plan) error
	ApplyKubernetes(context.Context, Plan) error
	WaitReady(context.Context, Plan) (map[string]string, error)
}

type UncertainDDLError struct{ Step string }

func (e UncertainDDLError) Error() string {
	return "Oracle DDL outcome requires reconciliation at " + e.Step
}

type Result struct {
	PlanID    string
	Status    string
	Operation string
	Results   map[string]string
}

var processLocks sync.Map

func BuildPlan(cfg config.File, preparedDir, stateDir string, target Target, images []registry.Image, blockers []string) (Plan, error) {
	if err := config.Validate(cfg); err != nil {
		return Plan{}, err
	}
	prepared, err := filepath.Abs(preparedDir)
	if err != nil || preparedDir == "" {
		return Plan{}, errors.New("prepared directory is required")
	}
	state, err := filepath.Abs(stateDir)
	if err != nil || stateDir == "" || samePath(prepared, state) || within(prepared, state) || within(state, prepared) {
		return Plan{}, errors.New("prepared output and protected state directories must be separate")
	}
	if target.Cluster == "" || target.Context == "" || target.APIEndpoint == "" || target.Namespace == "" {
		blockers = append(blockers, "Kubernetes cluster, context, API endpoint, and namespace identity must be verified before Apply.")
	}
	if target.Namespace != cfg.Kubernetes.Namespace {
		blockers = append(blockers, "Kubernetes namespace differs from the namespace in the reviewed configuration.")
	}
	var report preparation.Report
	if err := decodeStrict(filepath.Join(prepared, "review.json"), &report); err != nil {
		return Plan{}, errors.New("prepared review is missing or invalid")
	}
	if report.FileGenerationStatus != "GENERATED" {
		blockers = append(blockers, "Stage 3 preparation output is not complete.")
	}
	for _, b := range report.Blockers {
		blockers = append(blockers, b)
	}
	configBytes, err := json.Marshal(cfg)
	if err != nil {
		return Plan{}, errors.New("encode reviewed configuration")
	}
	configSum := sha256.Sum256(configBytes)
	p := Plan{Version: PlanVersion, CreatedAt: time.Now().UTC(), Configuration: cfg, ConfigurationHash: hex.EncodeToString(configSum[:]), PreparedDirectory: prepared, StateDirectory: state, Target: target, RegistryHost: cfg.Registry.Host, RegistryProject: cfg.Registry.Project, Profile: cfg.Profile, ArtifactDigests: map[string]string{}, SchemaActions: []string{}, KubernetesChanges: []string{"Create only explicitly owned Stage 4 Secrets and reviewed namespace resources.", "Reconcile the dedicated control plane and resident Key Manager before dependent workloads.", "Reconcile Traffic Managers, gateway, and Keycloak in the reviewed order.", "Check workload readiness and report application endpoint reachability separately."}, Blockers: dedupe(blockers)}
	checksums, err := readChecksums(filepath.Join(prepared, "checksums.sha256"))
	if err != nil {
		return Plan{}, errors.New("prepared checksums are missing or invalid")
	}
	for path, digest := range report.Files {
		if checksums[filepath.ToSlash(path)] != digest {
			return Plan{}, fmt.Errorf("prepared review digest does not match checksums for %s", path)
		}
	}
	for path, digest := range checksums {
		p.ArtifactDigests[path] = digest
	}
	if cfg.Oracle.SchemaMode == "initialize" {
		p.SchemaActions = append(p.SchemaActions, "Execute only independently verified exact-release WSO2 schema scripts against the reviewed APIM/shared schema targets.")
	} else if cfg.Oracle.SchemaMode == "prepared" {
		p.SchemaActions = append(p.SchemaActions, "Verify the prepared APIM/shared/Keycloak objects and runtime grants; do not execute DDL.")
	}
	for _, im := range images {
		if im.Status != registry.Found || !registryDigest.MatchString(im.Digest) {
			p.Blockers = append(p.Blockers, "Every required registry image must be visible with a verified destination digest before Apply: "+im.Component)
			continue
		}
		if len(im.Platforms) == 0 {
			p.Blockers = append(p.Blockers, "Registry platform evidence is missing for "+im.Component)
		}
		expected, ok := expectedReference(cfg, im.Component)
		if !ok || im.Reference != expected {
			p.Blockers = append(p.Blockers, "Registry image reference does not match the selected profile mapping: "+im.Component)
			continue
		}
		platforms := make([]string, 0, len(im.Platforms))
		for _, platform := range im.Platforms {
			if platform.Status != "VERIFIED" {
				p.Blockers = append(p.Blockers, "Registry platform coverage is incomplete for "+im.Component)
			}
			platforms = append(platforms, platform.Platform.OS+"/"+platform.Platform.Architecture)
		}
		p.Images = append(p.Images, Image{Component: im.Component, Reference: im.Reference, Digest: im.Digest, Platforms: platforms, Action: "USE EXISTING REGISTRY IMAGE; NO PUSH"})
	}
	if len(p.Images) != len(registry.Profile.Images) {
		p.Blockers = append(p.Blockers, "The plan must contain all profile images; partial registry inventories are not applicable.")
	}
	p.Blockers = dedupe(p.Blockers)
	p.ID = "sha256:" + planHash(p)
	return p, nil
}

func WritePlan(path string, p Plan) error {
	if err := VerifyPlan(p); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return errors.New("encode execution plan")
	}
	if err := atomicWriteNew(path, append(data, '\n'), 0600); err != nil {
		return errors.New("write execution plan")
	}
	return nil
}

func LoadPlan(path string) (Plan, error) {
	var p Plan
	if err := decodeStrict(path, &p); err != nil {
		return Plan{}, errors.New("read execution plan")
	}
	if err := VerifyPlan(p); err != nil {
		return Plan{}, err
	}
	return p, nil
}

func VerifyPlan(p Plan) error {
	if p.Version != PlanVersion || p.ID != "sha256:"+planHash(p) {
		return errors.New("reviewed execution plan identity is invalid or unsupported")
	}
	if err := config.Validate(p.Configuration); err != nil {
		return errors.New("reviewed execution configuration is invalid")
	}
	configBytes, err := json.Marshal(p.Configuration)
	if err != nil {
		return errors.New("reviewed execution configuration cannot be encoded")
	}
	configSum := sha256.Sum256(configBytes)
	if hex.EncodeToString(configSum[:]) != p.ConfigurationHash || p.RegistryHost != p.Configuration.Registry.Host || p.RegistryProject != p.Configuration.Registry.Project || p.Profile != p.Configuration.Profile {
		return errors.New("reviewed execution configuration identity does not match its bound fields")
	}
	for name, expected := range p.ArtifactDigests {
		clean := filepath.Clean(filepath.FromSlash(name))
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return errors.New("reviewed plan contains an unsafe prepared artifact path")
		}
		path := filepath.Join(p.PreparedDirectory, clean)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("prepared artifact is missing: %s", name)
		}
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != expected {
			return fmt.Errorf("prepared artifact changed after review: %s", name)
		}
	}
	return nil
}

func readChecksums(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "  ", 2)
		if len(parts) != 2 || !fileDigest.MatchString(parts[0]) || filepath.IsAbs(filepath.FromSlash(parts[1])) {
			return nil, errors.New("invalid checksum line")
		}
		name := filepath.ToSlash(filepath.Clean(filepath.FromSlash(parts[1])))
		if name == "." || name == ".." || strings.HasPrefix(name, "../") || name == "checksums.sha256" {
			return nil, errors.New("unsafe checksum path")
		}
		if _, exists := out[name]; exists {
			return nil, errors.New("duplicate checksum path")
		}
		out[name] = parts[0]
	}
	if len(out) == 0 {
		return nil, errors.New("empty checksum manifest")
	}
	return out, nil
}

func expectedReference(cfg config.File, component string) (string, bool) {
	for _, required := range registry.Profile.Images {
		if required.Component != component {
			continue
		}
		repo := cfg.Registry.Host + "/"
		if cfg.Registry.Project != "" {
			repo += cfg.Registry.Project + "/"
		}
		return repo + required.Repository + ":" + required.Tag, true
	}
	return "", false
}

func Apply(ctx context.Context, p Plan, confirmed, resume bool, adapter Adapter) (Result, error) {
	if !confirmed {
		return Result{}, errors.New("explicit Apply confirmation is required")
	}
	if err := VerifyPlan(p); err != nil {
		return Result{}, err
	}
	if len(p.Blockers) > 0 {
		return Result{PlanID: p.ID, Status: "BLOCKED"}, fmt.Errorf("reviewed plan has %d unresolved blocker(s); no changes were made", len(p.Blockers))
	}
	if adapter == nil {
		return Result{}, errors.New("Apply adapter is unavailable")
	}
	lockKey := filepath.Clean(p.StateDirectory)
	if runtime.GOOS == "windows" {
		lockKey = strings.ToLower(lockKey)
	}
	if _, loaded := processLocks.LoadOrStore(lockKey, struct{}{}); loaded {
		return Result{}, errors.New("another Apply operation for this plan is already running in this process")
	}
	defer processLocks.Delete(lockKey)
	if _, err := secretstore.Open(p.StateDirectory); err != nil {
		return Result{}, errors.New("protected state directory is unavailable or not access-controlled")
	}
	cpPath := filepath.Join(p.StateDirectory, "apply-checkpoint-"+strings.TrimPrefix(p.ID, "sha256:")+".json")
	cp, err := loadCheckpoint(cpPath, p.ID, resume)
	if err != nil {
		return Result{}, err
	}
	observation, err := adapter.Recheck(ctx, p)
	if err != nil {
		return Result{PlanID: p.ID, Status: "PREFLIGHT FAILED"}, errors.New("Apply recheck failed; no mutation was started")
	}
	if observation.Target != p.Target {
		return Result{PlanID: p.ID, Status: "STALE TARGET"}, errors.New("current cluster/context/API endpoint/namespace differs from the reviewed plan; create a new review")
	}
	if !equalMap(observation.ArtifactDigests, p.ArtifactDigests) {
		return Result{PlanID: p.ID, Status: "STALE ARTIFACTS"}, errors.New("prepared artifact digests changed after review; create a new review")
	}
	if cp.Uncertain != "" {
		ok, e := adapter.Reconcile(ctx, p, cp.Uncertain)
		if e != nil || !ok {
			return Result{PlanID: p.ID, Status: "RECONCILIATION REQUIRED", Operation: cp.Uncertain}, errors.New("an earlier Oracle operation has an uncertain outcome; reconcile it before replay")
		}
		cp.Completed[cp.Uncertain] = true
		cp.Uncertain = ""
		cp.UncertainDetail = ""
		if err := saveCheckpoint(cpPath, cp); err != nil {
			return Result{}, err
		}
	}
	steps := []struct {
		name string
		run  func(context.Context) (map[string]string, error)
	}{
		{"images", func(c context.Context) (map[string]string, error) { return adapter.ImportMissing(c, p) }},
		{"oracle", func(c context.Context) (map[string]string, error) { return nil, adapter.PrepareOracle(c, p) }},
		{"kubernetes", func(c context.Context) (map[string]string, error) { return nil, adapter.ApplyKubernetes(c, p) }},
		{"readiness", func(c context.Context) (map[string]string, error) { return adapter.WaitReady(c, p) }},
	}
	cp.Status = "RUNNING"
	if cp.OperationID == "" {
		cp.OperationID = p.ID + ":" + time.Now().UTC().Format("20060102T150405.000000000Z")
		cp.StartedAt = time.Now().UTC()
	}
	if err := saveCheckpoint(cpPath, cp); err != nil {
		return Result{}, err
	}
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			cp.Status = "CANCELED"
			_ = saveCheckpoint(cpPath, cp)
			return resultFrom(cp), err
		}
		if cp.Completed[step.name] {
			ok, e := adapter.Reconcile(ctx, p, step.name)
			if e != nil || !ok {
				delete(cp.Completed, step.name)
				if err := saveCheckpoint(cpPath, cp); err != nil {
					return resultFrom(cp), err
				}
			} else {
				continue
			}
		}
		values, runErr := step.run(ctx)
		if runErr != nil {
			cp.Status = "FAILED"
			if step.name == "oracle" {
				var uncertain UncertainDDLError
				if errors.As(runErr, &uncertain) {
					cp.Uncertain = "oracle"
					cp.UncertainDetail = safeStep(uncertain.Step)
				}
			}
			_ = saveCheckpoint(cpPath, cp)
			if cp.Uncertain != "" {
				return resultFrom(cp), errors.New("Oracle DDL outcome is uncertain at reviewed step " + cp.UncertainDetail + "; reconcile before replay")
			}
			return resultFrom(cp), fmt.Errorf("Apply operation %s failed; inspect protected status and reconcile before resume", step.name)
		}
		cp.Completed[step.name] = true
		for k, v := range values {
			if safeResult(k, v) {
				cp.Results[k] = v
			}
		}
		cp.UpdatedAt = time.Now().UTC()
		if err := saveCheckpoint(cpPath, cp); err != nil {
			return resultFrom(cp), err
		}
	}
	cp.Status = "SUCCEEDED"
	if err := saveCheckpoint(cpPath, cp); err != nil {
		return resultFrom(cp), err
	}
	return resultFrom(cp), nil
}

func loadCheckpoint(path, planID string, resume bool) (Checkpoint, error) {
	cp := Checkpoint{Version: 1, PlanID: planID, Status: "PENDING", Completed: map[string]bool{}, Results: map[string]string{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if resume {
			return cp, errors.New("no checkpoint exists to resume")
		}
		return cp, nil
	}
	if err != nil {
		return cp, errors.New("cannot read protected Apply checkpoint")
	}
	if err := json.Unmarshal(b, &cp); err != nil || cp.Version != 1 || cp.PlanID != planID {
		return cp, errors.New("protected Apply checkpoint does not match the reviewed plan")
	}
	if cp.Status == "SUCCEEDED" && !resume {
		return cp, errors.New("this plan already completed; use status or a new reviewed plan")
	}
	if cp.Status != "PENDING" && !resume {
		return cp, errors.New("a checkpoint exists for this plan; use explicit resume after review")
	}
	return cp, nil
}

func saveCheckpoint(path string, cp Checkpoint) error {
	cp.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return errors.New("encode protected Apply checkpoint")
	}
	if err := atomicReplace(path, append(data, '\n'), 0600); err != nil {
		return errors.New("write protected Apply checkpoint")
	}
	return nil
}

func CheckpointStatus(stateDir, planID string) (Checkpoint, error) {
	if !strings.HasPrefix(planID, "sha256:") {
		return Checkpoint{}, errors.New("invalid plan identity")
	}
	var cp Checkpoint
	if err := decodeStrict(filepath.Join(stateDir, "apply-checkpoint-"+strings.TrimPrefix(planID, "sha256:")+".json"), &cp); err != nil || cp.PlanID != planID {
		return Checkpoint{}, errors.New("matching protected checkpoint is unavailable")
	}
	return cp, nil
}

func planHash(p Plan) string {
	p.ID = ""
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func decodeStrict(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("trailing JSON value")
	}
	return nil
}

func atomicWriteNew(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}

func atomicReplace(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".apply-checkpoint-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err == nil {
		err = f.Sync()
	}
	if err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func resultFrom(cp Checkpoint) Result {
	copy := map[string]string{}
	for k, v := range cp.Results {
		copy[k] = v
	}
	return Result{PlanID: cp.PlanID, Status: cp.Status, Operation: cp.Uncertain, Results: copy}
}

func safeStep(step string) string {
	if len(step) == 0 || len(step) > 100 {
		return "schema-step"
	}
	for _, r := range step {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return "schema-step"
		}
	}
	return step
}

func safeResult(key, value string) bool {
	switch key {
	case "component_status", "endpoint_status", "schema_status", "registry_status":
		switch value {
		case "READY", "NOT_READY", "REACHABLE", "UNREACHABLE", "VERIFIED", "DEFERRED", "APPLIED", "BLOCKED":
			return true
		}
	case "image_digest_apim", "image_digest_keycloak", "image_digest_tm", "image_digest_gateway":
		return registryDigest.MatchString(value)
	}
	return false
}

func dedupe(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func equalMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func samePath(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }

func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
