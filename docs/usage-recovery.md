# Usage and recovery

## Current commands

- `version` and `version --json` report product/build identity.
- `demo` explicitly labels its fixture-only output and performs no checks.
- `wizard [--log-dir PATH] [--no-color]` starts the registry inventory and read-only review flow in an interactive terminal.
- `verify-archive --bundle PATH` validates a local bundle independently and displays distinct archive/config/manifest/index/layer/DiffID evidence.
- `validate --config PATH` and `plan --config PATH [--bundle PATH]` parse versioned configuration and run the same exact-reference registry checks, optional bundle verification, and optional Kubernetes preflight without reading stdin. Credentials can be supplied via `SYSDEV_REGISTRY_USERNAME`/`SYSDEV_REGISTRY_PASSWORD` or `SYSDEV_REGISTRY_TOKEN`; `SYSDEV_REGISTRY_CA_BUNDLE` selects a custom CA bundle.
- `prepare --config PATH --output NEW_DIRECTORY --state-dir PROTECTED_DIRECTORY [--check-oracle]` verifies local Stage 3 inputs and generates a non-mutating deployment preparation. Runtime account variables are `SYSDEV_ORACLE_APIM_USERNAME/PASSWORD`, `SYSDEV_ORACLE_SHARED_USERNAME/PASSWORD`, and `SYSDEV_ORACLE_KEYCLOAK_USERNAME/PASSWORD`. Optional initialization-owner checks use `SYSDEV_ORACLE_APIM_OWNER_USERNAME/PASSWORD`, `SYSDEV_ORACLE_SHARED_OWNER_USERNAME/PASSWORD`, and `SYSDEV_ORACLE_KEYCLOAK_OWNER_USERNAME/PASSWORD`. Values remain process-only and are never printed.
- `plan --config CONFIG --prepared DIR --execution-plan FILE` performs the existing read-only inventory and writes a versioned plan bound to the prepared files, configuration, registry digests, target context, and namespace. The plan is rejected for Apply when inputs or required checks are unresolved.
- `apply --plan FILE --confirm [--resume]` requires a specific plan and a final explicit confirmation. `status --plan FILE` reads its protected checkpoint. The shared execution journal/orchestrator and fixture adapters are implemented, but the production registry-import, Oracle/schema, and Kubernetes adapters are not connected yet; the CLI stops before mutation with an explicit adapter-unavailable error. Wizard Apply confirmation is also pending.
- `verify` and support export remain unavailable.

The wizard requests a registry host (without a scheme), optional project prefix, and anonymous/Basic/Bearer credentials. It queries only the four profile references. Password/token entry is masked and credentials remain session-only. `Esc` returns to the prior step or cancels an active check; `Ctrl-E` opens the field editor; `r` retries the inventory; `F1` shows help; `Ctrl-C` exits. The log path remains visible in the footer.

After the integrated preflight and review, press `Enter` to open the Stage 3 local preparation form. The prompts collect:

- Oracle target type (`service` or `descriptor`), host/service or credential-free connect descriptor, TCP/TCPS mode, TCPS DN and wallet/trust paths, timeouts, prepared/initialize schema mode, and APIM/shared/Keycloak schemas and runtime/optional owner accounts.
- Whether to run authenticated read-only Oracle metadata checks. Choosing `yes` asks for masked session-only runtime passwords and, when owner accounts are configured, owner passwords. Choosing `no` records Oracle validation as deferred. These wizard credentials are passed directly to the preparation service and are not written to environment variables, config, logs, or output.
- Certificate `generate` or `import` mode, public service hostnames, cluster DNS domain, and (for import) external/internal certificate, private-key, optional chain, and trust paths. Import paths are entered as four pipe-separated values; leave the optional chain slot empty, for example `cert.pem|key.pem||trust.pem`.
- Optional exact-release WSO2 distribution archive path and SHA-256, the local schema artifact manifest path, a new public output directory, and a separate protected state directory. Initialize mode without a valid schema manifest exports a blocked review so the missing artifact is visible.
- Optional distributed Redis/Valkey host, port, type, replication mode, and TLS trust inputs used by the candidate deployment asset review.

Mode-specific questions are skipped. Use `Esc` to go back, `Ctrl-E` then Up/Down and `Enter` to edit a field, and `Backspace` to correct input. Editing Stage 3 settings invalidates the preparation review while preserving unrelated registry results. Namespace, hostname, and cluster DNS changes invalidate dependent certificate/assets results. At **Stage 3 preparation review/export**, inspect the Oracle, schema, certificate, file, blocker, and deferred-check results, then press `e` for the explicit local export. Export runs asynchronously and can be canceled with `Esc`; a canceled or stale result is not shown as generated. File generation and deployment readiness are reported separately. A generated export can still be blocked or unqualified; this path never applies resources or runs schema DDL.

The deterministic `demo` command remains fixture-only and exits from its review without offering a real preparation export. Use `wizard` for operator-supplied Stage 3 inputs.

## Interpreting a registry result

- `FOUND` means this workstation could read the exact remote manifest. When selected workers are known, the client resolves platform descriptors and verifies config digests/platforms. It does not establish that a cluster node can pull the image.
- `MISSING/NOT VISIBLE` can also reflect registry policy. A local archive can satisfy planning only when the bundle mapping, archive hash, referenced content, config platform, and every selected worker architecture have been verified.
- `UNAUTHORIZED` and `FORBIDDEN` are distinct. A registry may deliberately hide image existence from unauthorized users.
- `TLS FAILURE`, `UNREACHABLE`, `UNSUPPORTED MANIFEST`, and `UNKNOWN` remain blockers until corrected or explicitly handled by a later plan policy.
- The displayed remote digest is the registry's Docker-Content-Digest when valid, or SHA-256 of the returned manifest body. Platform child-manifest, config, and layer descriptor digests are shown separately when known. Local archive hashes, image configs, OCI manifests/indexes, layer blob hashes, and DiffIDs are separate values.

Kubernetes preflight reads metadata and submits SelfSubjectAccessReview calls for specific planned verbs. The checks are not permission to apply. StorageClass metadata does not establish RWX, successful provisioning, or failover. The NodePort default range is an explicit `30000-32767` assumption unless an operator-supplied range is configured. Capacity is a conservative bounded CPU/memory assessment and not the scheduler, HA certification, or a 400 TPS result. The single-control-plane and external-single-Oracle limitations remain visible.

The generated execution plan is a review artifact, not an authorization. Inspect its target, recorded registry image digests/platforms, schema actions, Kubernetes changes, and blockers before using `apply --plan FILE --confirm`. Workload assets are not yet rewritten to those digests because the production Apply adapter is pending. Current production Apply remains gated because the real environment adapters, secret materialization, exact-release evidence checks, and wizard final confirmation are pending. The fixture execution path demonstrates ordering and resume behavior only; it does not deploy resources. Checkpoints are protected by the selected state-directory ACL and keyed to the plan identity. The current lock prevents concurrent Apply for one protected state directory within one process; independent processes are not fenced. A process crash can leave a checkpoint; use `status --plan FILE`, then explicitly resume only after read-only reconciliation. Uncertain Oracle DDL requires operator reconciliation. No schema is dropped and no uncertain DDL is replayed automatically.

Suggested command sequence (all config and artifact paths are operator supplied):

```text
sysdev-wso2-airgap-installer prepare --config CONFIG --output PREPARED --state-dir PROTECTED_STATE
sysdev-wso2-airgap-installer plan --config CONFIG --prepared PREPARED --state-dir PROTECTED_STATE --execution-plan REVIEWED_PLAN
sysdev-wso2-airgap-installer apply --plan REVIEWED_PLAN --confirm
sysdev-wso2-airgap-installer status --plan REVIEWED_PLAN
sysdev-wso2-airgap-installer apply --plan REVIEWED_PLAN --confirm --resume
```

The last three commands remain fail-closed for real changes until the production adapters are qualified and connected. `prepare`, `plan`, and `demo` remain non-mutating with respect to external systems.

The Stage 3 examples show the required field shapes. The prepared-schema/import example needs operator-supplied PEM files whose SANs cover the configured external and internal endpoints. The initialization/generated-certificate example emits public artifacts and protected keys while explicitly blocking on its missing offline WSO2 SQL artifact manifest. Preparation does not contact Kubernetes even when the config contains a kubeconfig path; only `validate`/`plan` perform read-only Kubernetes discovery.
