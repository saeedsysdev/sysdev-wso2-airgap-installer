# Sysdev Wso2 Airgap Installer

**Version:** `0.1.0-dev`
**Developed and Supported by SaeedSysDev**

An English-language Go terminal application for preparing a WSO2 API Manager 4.7.0 / Keycloak 26.8.1 deployment for an air-gapped Kubernetes environment. It verifies remote registry platform coverage, local OCI/Docker image bundles, and read-only Kubernetes preflight, then presents an editable non-mutating review plan. Stage 3 adds a separate local preparation/export command for Oracle, PKI, secret references, and candidate deployment assets. Apply remains unavailable, so this is not yet a deployment installer.

## Current usable slice

- Prints the exact product identity, development version, attribution, and absolute per-run audit-log path before starting the interactive wizard.
- Provides a Bubble Tea terminal wizard with text entry, hidden credentials, Back, Edit, Retry, cancellation, resize-aware layout, and plain-text output support.
- Checks four pinned source/destination image pairs against an OCI Distribution API over verified HTTPS, supports Basic credentials and standard HTTPS Bearer challenges, resolves selected platform manifests/configs, and records the actual known digest types.
- Verifies versioned offline bundle manifests, recorded archive sizes and SHA-256, safe Docker `image save` tar archives, and OCI image-layout archives. It checks referenced configs, manifests/indexes, layers, and DiffIDs without unpacking image filesystems.
- `validate --config` and `plan --config` can read the selected kubeconfig/context, inventory cluster version, eligible workers and reservations, inspect namespace/StorageClass/Services, review planned permissions with SelfSubjectAccessReview, and assess one-worker-loss CPU/memory placement conservatively.
- Back/Edit invalidates dependent registry, platform, local archive, permissions, storage, and capacity review results. Newer configuration revisions reject obsolete asynchronous responses.
- Builds a non-mutating review with passed checks, blockers, deferred checks, imports, platform coverage, permissions, storage/network limitations, and an explicitly bounded capacity assessment.
- `prepare` validates the versioned non-secret configuration, can run bounded authenticated read-only Oracle checks, verifies locally supplied schema metadata, prepares imported or generated certificates and Java keystores, retains a reusable encryption key in protected local state, and exports deterministic candidate manifests/configuration with hashes and typed deferred bindings.
- Interactive registry checks start only after the operator enters a registry. Demo mode opens the same image-table/review screens with unmistakably labeled local fixtures and makes no network requests. Non-interactive validation contacts only the explicitly configured registry and optional Kubernetes API; no cluster, Oracle, identity-service, or registry mutation is implemented.

## Build and run

Requires Go 1.26.0 or newer compatible with the checked-in `go.mod`. The built application has no runtime Go requirement.

```powershell
go test ./...
go run ./cmd/sysdev-wso2-airgap-installer version
go run ./cmd/sysdev-wso2-airgap-installer version --json
go run ./cmd/sysdev-wso2-airgap-installer demo
go run ./cmd/sysdev-wso2-airgap-installer wizard --log-dir ./.local-logs --no-color
go run ./cmd/sysdev-wso2-airgap-installer verify-archive --bundle ./path/to/offline-bundle
go run ./cmd/sysdev-wso2-airgap-installer plan --config examples/config.example.json
go run ./cmd/sysdev-wso2-airgap-installer plan --config examples/config.cluster.example.json
go run ./cmd/sysdev-wso2-airgap-installer plan --config ./operator-config.json --bundle ./path/to/offline-bundle
go run ./cmd/sysdev-wso2-airgap-installer prepare --config ./operator-config.json --output ./prepared-run --state-dir ./protected-state
go run ./cmd/sysdev-wso2-airgap-installer prepare --config ./operator-init.json --output ./prepared-init --state-dir ./protected-state --check-oracle
go test ./internal/plan -run TestRegistryOnlyPlanDoesNotRequireArchive -v
go test ./internal/plan -run TestVerifiedLocalArchiveSatisfiesMissingImageForSelectedPlatform -v
```

The default Windows log directory is `%LOCALAPPDATA%\SaeedSysDev\Sysdev Wso2 Airgap Installer\logs`. Linux uses `$XDG_STATE_HOME/sysdev-wso2-airgap-installer/logs` or `~/.local/state/sysdev-wso2-airgap-installer/logs`. Pass `--log-dir` to override it. Registry hostnames require HTTPS and certificate validation. `validate --config PATH` and `plan --config PATH` use the same registry validator and do not wait for stdin. If the config includes `kubernetes.kubeconfig_path`, those commands also read the selected Kubernetes API version, nodes, and pods. Set optional registry credentials in `SYSDEV_REGISTRY_USERNAME` and `SYSDEV_REGISTRY_PASSWORD`, or set `SYSDEV_REGISTRY_TOKEN`; a custom CA path can be set in `registry.ca_bundle_path` or `SYSDEV_REGISTRY_CA_BUNDLE`. Credentials are never read from the JSON file. `apply` is unavailable and cannot perform mutations.

`verify-archive --bundle PATH` checks a local bundle independently. The bundle format and limits are documented in [formats](docs/formats.md). For registry-only planning, omit `offline_bundle_path` and provide exact destination image read access; when worker OS/architecture is known the registry images are checked for those platforms. For a missing registry image, add `offline_bundle_path` (or pass `--bundle`) and a bundle with explicit component mappings; the review shows `READY TO IMPORT` only for verified, platform-compatible local content and a registry reference reported missing/not visible. A destination image found remotely is never replaced by a local archive in this plan.

Stage 3 configuration examples are [prepared schemas with imported certificates](examples/config.prepared.json) and [initialization planning with generated certificates](examples/config.initialize.json). Run the command above with a new output directory. Runtime Oracle credentials use `SYSDEV_ORACLE_APIM_*`, `SYSDEV_ORACLE_SHARED_*`, and `SYSDEV_ORACLE_KEYCLOAK_*`; optional owner checks use corresponding `*_OWNER_*` names. Generated private keys and the stable shared key remain in `--state-dir`; review/assets contain public certificates and Secret references. No database object, Kubernetes resource, image, trust store, or identity service is changed. The initialization example intentionally has no WSO2 distribution artifact bundled and will report that blocker until separately verified offline artifacts are supplied.

The two `go test` commands above demonstrate both review paths with deterministic local fixtures: all destination references readable with no local archive, and one missing reference satisfied by a verified local Docker-save bundle on selected `linux/amd64` workers. They do not contact or modify live services.

The first run of `wizard` asks for a registry host and optional project prefix, then optional Kubernetes and archive inputs. A `MISSING/NOT VISIBLE` status is not proof that a private image does not exist. Workstation manifest access does not establish image-pull access or write access from Kubernetes nodes. The one-worker-loss result is a conservative bounded CPU/memory placement assessment; configured candidate components currently have no profile-level selectors or affinities, so candidate replicas are modeled as placeable on any selected eligible worker. Existing workload placement constraints are reported as unverified during rescheduling. This is not scheduler simulation, HA certification, or proof of the 400 TPS target.

## Repository guide

- [Full project specification](SYSDEV_WSO2_AIRGAP_INSTALLER_CODEX_PROMPT.md)
- [Requirements and acceptance evidence](docs/requirements.md)
- [Architecture and decisions](docs/architecture.md)
- [Formats and schema policy](docs/formats.md)
- [Testing and qualification status](docs/qualification.md)
- [Usage, recovery, and current limitations](docs/usage-recovery.md)
- [Changelog](CHANGELOG.md)

See [CI](.github/workflows/ci.yml) for native Windows and Linux checks. Build artifacts and checksums are local outputs only; the release script never publishes them.
