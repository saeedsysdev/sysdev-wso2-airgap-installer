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
	apply := fs.Bool("apply", false, "Authorize mutations for a validated plan (not available in this milestone)")
	if err := fs.Parse(args[1:]); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if *apply {
		return fmt.Errorf("apply is not implemented; no changes were made")
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
		return fmt.Errorf("Apply is not implemented; no changes were made (log: %s)", logger.Path())
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
		return runConfigured(context.Background(), cmd, service, cfg, logger.Path())
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
	fmt.Printf("%s %s\n%s\nProject: https://github.com/saeedsysdev/sysdev-wso2-airgap-installer\n\nUsage: sysdev-wso2-airgap-installer <command> [options]\n\nCommands:\n  wizard         Open the English interactive review wizard\n  validate       Run read-only registry/archive/Kubernetes preflight from config\n  plan           Print an integrated non-mutating review plan\n  prepare        Verify Oracle/PKI inputs and generate a redacted local deployment plan\n  verify-archive Verify a local bundle and Docker/OCI image archives\n  apply          Explicit apply authorization is required (not available)\n  demo           Review deterministic local fixtures (no network access)\n  version        Print version metadata\n  about          Print application and support metadata\n  support        Export a sanitized support bundle (planned)\n\nOptions: --log-dir PATH, --no-color, --config PATH, --bundle PATH\n", domain.ApplicationName, appVer, domain.Attribution)
}

func isTerminal() bool { fi, e := os.Stdin.Stat(); return e == nil && fi.Mode()&os.ModeCharDevice != 0 }
