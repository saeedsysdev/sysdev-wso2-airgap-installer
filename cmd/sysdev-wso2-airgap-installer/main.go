package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/app"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/auditlog"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/domain"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/execution"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/imagearchive"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/preparation"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/version"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/wizard"
)

var (
	commit  = "development"
	builtAt = "unknown"
	dirty   = "unknown"
	appVer  = version.Current
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", auditlog.Redact(err.Error()))
		os.Exit(1)
	}
}

func run(args []string) (result error) {
	if len(args) > 0 && (args[0] == "version" || args[0] == "about" || args[0] == "--version" || args[0] == "-version") {
		if len(args) > 1 && args[1] == "--json" {
			return json.NewEncoder(os.Stdout).Encode(version.Info{Application: domain.ApplicationName, Version: appVer, Commit: commit, BuiltAt: builtAt, Dirty: dirty, Attribution: domain.Attribution})
		}
		fmt.Printf("%s %s\n%s\nCommit: %s | Built: %s | Dirty: %s\n", domain.ApplicationName, appVer, domain.Attribution, commit, builtAt, dirty)
		return nil
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp()
		return nil
	}
	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	logDir := fs.String("log-dir", "", "Override the application log directory")
	noColor := fs.Bool("no-color", os.Getenv("NO_COLOR") != "", "Disable terminal colors")
	configPath := fs.String("config", "", "Non-secret JSON configuration file")
	outputDir := fs.String("output", "", "New public preparation output directory (must not exist)")
	stateDir := fs.String("state-dir", "", "Protected local secret state directory")
	checkOracle := fs.Bool("check-oracle", false, "Run bounded authenticated read-only Oracle checks using SYSDEV_ORACLE_* environment credentials")
	bundlePath := fs.String("bundle", "", "Offline image bundle directory")
	preparedDir := fs.String("prepared", "", "Stage 3 preparation output directory for reviewed execution-plan creation")
	executionPlanPath := fs.String("execution-plan", "", "Write a versioned, artifact-bound execution plan (plan command)")
	planPath := fs.String("plan", "", "Specific reviewed execution plan for apply/status")
	confirmApply := fs.Bool("confirm", false, "Explicitly authorize the Apply operation")
	resumeApply := fs.Bool("resume", false, "Resume a checkpointed Apply after reconciliation")
	applyFlag := fs.Bool("apply", false, "Deprecated: use apply --plan PATH --confirm")
	if err := fs.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if *applyFlag {
		return fmt.Errorf("--apply is not an authorization; use apply --plan PATH --confirm")
	}
	if cmd != "wizard" && cmd != "install" && cmd != "validate" && cmd != "plan" && cmd != "status" && cmd != "verify" && cmd != "support" && cmd != "demo" && cmd != "apply" && cmd != "verify-archive" && cmd != "prepare" {
		return fmt.Errorf("unknown command %q; use --help", cmd)
	}
	path, err := auditlog.DefaultPath(*logDir)
	if err != nil {
		return err
	}
	logger, err := auditlog.New(path)
	if err != nil {
		return fmt.Errorf("cannot initialize the required audit log: %w", err)
	}
	defer func() {
		if err := logger.Close(); err != nil {
			result = errors.Join(result, fmt.Errorf("flush required audit log: %w", err))
		}
	}()
	fmt.Printf("%s %s\n%s\nRun log: %s\n", domain.ApplicationName, appVer, domain.Attribution, logger.Path())
	logger.Event("INFO", "startup", "Application started", map[string]string{"command": cmd})
	if err := logger.Error(); err != nil {
		return fmt.Errorf("required audit log failed at startup: %w", err)
	}
	service := app.New(registry.NewClient(), logger)
	if cmd == "demo" {
		if !isTerminal() {
			return fmt.Errorf("demo requires an interactive terminal (log: %s)", logger.Path())
		}
		model := wizard.NewDemo(service, *noColor, logger.Path())
		return wizard.Run(context.Background(), model)
	}
	if cmd == "apply" {
		if *planPath == "" {
			return fmt.Errorf("apply requires --plan PATH naming a reviewed execution plan (log: %s)", logger.Path())
		}
		p, err := execution.LoadPlan(*planPath)
		if err != nil {
			return fmt.Errorf("reviewed execution plan rejected: %w (log: %s)", err, logger.Path())
		}
		printExecutionPlan(p)
		if !*confirmApply {
			return fmt.Errorf("final authorization not supplied; repeat with --confirm after reviewing this exact plan (log: %s)", logger.Path())
		}
		_, err = execution.Apply(context.Background(), p, true, *resumeApply, nil)
		return fmt.Errorf("Apply stopped before mutation: %w (log: %s)", err, logger.Path())
	}
	if cmd == "status" {
		if *planPath == "" {
			return fmt.Errorf("status requires --plan PATH (log: %s)", logger.Path())
		}
		p, err := execution.LoadPlan(*planPath)
		if err != nil {
			return fmt.Errorf("execution plan rejected: %w (log: %s)", err, logger.Path())
		}
		cp, err := execution.CheckpointStatus(p.StateDirectory, p.ID)
		if err != nil {
			return fmt.Errorf("Apply checkpoint unavailable: %w (log: %s)", err, logger.Path())
		}
		fmt.Printf("Plan %s | status %s | uncertain operation %s (%s) | updated %s\n", cp.PlanID, cp.Status, cp.Uncertain, cp.UncertainDetail, cp.UpdatedAt.Format(time.RFC3339))
		for name, done := range cp.Completed {
			fmt.Printf("%s: completed=%t\n", name, done)
		}
		return nil
	}
	if cmd == "verify-archive" {
		if *bundlePath == "" {
			return fmt.Errorf("verify-archive requires --bundle PATH (log: %s)", logger.Path())
		}
		res, err := imagearchive.Verify(context.Background(), *bundlePath, registry.Profile.Images)
		if err != nil {
			return fmt.Errorf("offline bundle verification failed: %w (log: %s)", err, logger.Path())
		}
		printArchive(res)
		return nil
	}
	if cmd == "prepare" {
		if *configPath == "" || *outputDir == "" {
			return fmt.Errorf("prepare requires --config PATH and --output NEW_DIRECTORY (log: %s)", logger.Path())
		}
		cfg, err := config.Load(*configPath)
		if err != nil {
			return fmt.Errorf("%w (log: %s)", err, logger.Path())
		}
		state := *stateDir
		if state == "" {
			base, e := os.UserConfigDir()
			if e != nil {
				return fmt.Errorf("resolve protected state directory (log: %s)", logger.Path())
			}
			state = base + string(os.PathSeparator) + "SysdevWso2AirgapInstaller" + string(os.PathSeparator) + "state"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		report, err := preparation.Run(ctx, preparation.Options{Config: cfg, OutputDir: *outputDir, StateDir: state, CheckOracle: *checkOracle})
		if err != nil {
			return fmt.Errorf("preparation failed: %w (log: %s)", err, logger.Path())
		}
		fmt.Printf("Preparation output: %s\nProtected state: %s\nQualification: %s\n", *outputDir, state, report.Qualification)
		for _, b := range report.Blockers {
			fmt.Println("BLOCKER:", b)
		}
		for _, d := range report.Deferred {
			fmt.Println("DEFERRED:", d)
		}
		fmt.Println("No cluster, registry, database schema, or identity service was modified.")
		return nil
	}
	if cmd == "validate" || cmd == "plan" || *configPath != "" {
		if *configPath == "" {
			return fmt.Errorf("--config PATH is required for %s; no changes were made (log: %s)", cmd, logger.Path())
		}
		if cmd != "validate" && cmd != "plan" && cmd != "wizard" && cmd != "install" {
			return fmt.Errorf("--config is not supported for %s (log: %s)", cmd, logger.Path())
		}
		cfg, err := config.Load(*configPath)
		if err != nil {
			return fmt.Errorf("%w (log: %s)", err, logger.Path())
		}
		if *bundlePath != "" {
			cfg.OfflineBundlePath = *bundlePath
		}
		return runConfigured(context.Background(), cmd, service, cfg, logger.Path(), ReviewedPlanOptions{PreparedDirectory: *preparedDir, StateDirectory: *stateDir, PlanPath: *executionPlanPath})
	}
	if cmd != "wizard" && cmd != "install" && cmd != "validate" && cmd != "plan" {
		return fmt.Errorf("%s is not implemented yet (log: %s)", cmd, logger.Path())
	}
	if !isTerminal() {
		return fmt.Errorf("interactive wizard requires a terminal; use --config for non-interactive validation (log: %s)", logger.Path())
	}
	model := wizard.New(service, *noColor, logger.Path())
	return wizard.Run(context.Background(), model)
}

func printHelp() {
	fmt.Printf("%s %s\n%s\nProject: https://github.com/saeedsysdev/sysdev-wso2-airgap-installer\n\nUsage: sysdev-wso2-airgap-installer <command> [options]\n\nCommands:\n  wizard         Open the English interactive review wizard\n  validate       Run read-only registry/archive/Kubernetes preflight from config\n  plan           Print review; optionally write --execution-plan using --prepared output\n  prepare        Verify Oracle/PKI inputs and generate a redacted local deployment plan\n  verify-archive Verify a local bundle and Docker/OCI image archives\n  apply          Review and explicitly authorize a specific plan (backend currently gated)\n  status         Inspect protected Apply checkpoint status\n  demo           Review deterministic local fixtures (no network access)\n  version        Print version metadata\n  about          Print application and support metadata\n  support        Export a sanitized support bundle (planned)\n\nOptions: --log-dir PATH, --no-color, --config PATH, --bundle PATH, --plan PATH, --confirm\n", domain.ApplicationName, appVer, domain.Attribution)
}

func printExecutionPlan(p execution.Plan) {
	fmt.Printf("\nEXECUTION PLAN %s v%d | profile %s\nTarget: cluster=%s context=%s namespace=%s API=%s\nRegistry: %s/%s\n", p.ID, p.Version, p.Profile, p.Target.Cluster, p.Target.Context, p.Target.Namespace, p.Target.APIEndpoint, p.RegistryHost, p.RegistryProject)
	for _, image := range p.Images {
		fmt.Printf("IMAGE %s: %s@%s | %s | platforms=%v\n", image.Component, image.Reference, image.Digest, image.Action, image.Platforms)
	}
	for _, action := range p.SchemaActions {
		fmt.Println("SCHEMA:", action)
	}
	for _, change := range p.KubernetesChanges {
		fmt.Println("KUBERNETES:", change)
	}
	for _, blocker := range p.Blockers {
		fmt.Println("BLOCKER:", blocker)
	}
}

func isTerminal() bool { fi, e := os.Stdin.Stat(); return e == nil && fi.Mode()&os.ModeCharDevice != 0 }
