# Usage and recovery

## Current commands

- `version` and `version --json` report product/build identity.
- `demo` explicitly labels its fixture-only output and performs no checks.
- `wizard [--log-dir PATH] [--no-color]` starts the registry inventory and read-only review flow in an interactive terminal.
- `verify-archive --bundle PATH` validates a local bundle independently and displays distinct archive/config/manifest/index/layer/DiffID evidence.
- `validate --config PATH` and `plan --config PATH [--bundle PATH]` parse versioned configuration and run the same exact-reference registry checks, optional bundle verification, and optional Kubernetes preflight without reading stdin. Credentials can be supplied via `SYSDEV_REGISTRY_USERNAME`/`SYSDEV_REGISTRY_PASSWORD` or `SYSDEV_REGISTRY_TOKEN`; `SYSDEV_REGISTRY_CA_BUNDLE` selects a custom CA bundle.
- `prepare --config PATH --output NEW_DIRECTORY --state-dir PROTECTED_DIRECTORY [--check-oracle]` verifies local Stage 3 inputs and generates a non-mutating deployment preparation. Runtime account variables are `SYSDEV_ORACLE_APIM_USERNAME/PASSWORD`, `SYSDEV_ORACLE_SHARED_USERNAME/PASSWORD`, and `SYSDEV_ORACLE_KEYCLOAK_USERNAME/PASSWORD`. Optional initialization-owner checks use `SYSDEV_ORACLE_APIM_OWNER_USERNAME/PASSWORD`, `SYSDEV_ORACLE_SHARED_OWNER_USERNAME/PASSWORD`, and `SYSDEV_ORACLE_KEYCLOAK_OWNER_USERNAME/PASSWORD`. Values remain process-only and are never printed.
- `status`, `verify`, support export, and `apply` are not available yet. They fail with a clear message and perform no installation mutation.

The wizard requests a registry host (without a scheme), optional project prefix, and anonymous/Basic/Bearer credentials. It queries only the four profile references. Password/token entry is masked and credentials remain session-only. `Esc` returns to the prior step or cancels an active registry check; `Ctrl-E` selects a field to edit; `r` retries the inventory; `F1` shows help; `Ctrl-C` exits. The log path remains visible in the footer.

## Interpreting a registry result

- `FOUND` means this workstation could read the exact remote manifest. When selected workers are known, the client resolves platform descriptors and verifies config digests/platforms. It does not establish that a cluster node can pull the image.
- `MISSING/NOT VISIBLE` can also reflect registry policy. A local archive can satisfy planning only when the bundle mapping, archive hash, referenced content, config platform, and every selected worker architecture have been verified.
- `UNAUTHORIZED` and `FORBIDDEN` are distinct. A registry may deliberately hide image existence from unauthorized users.
- `TLS FAILURE`, `UNREACHABLE`, `UNSUPPORTED MANIFEST`, and `UNKNOWN` remain blockers until corrected or explicitly handled by a later plan policy.
- The displayed remote digest is the registry's Docker-Content-Digest when valid, or SHA-256 of the returned manifest body. Platform child-manifest, config, and layer descriptor digests are shown separately when known. Local archive hashes, image configs, OCI manifests/indexes, layer blob hashes, and DiffIDs are separate values.

Kubernetes preflight reads metadata and submits SelfSubjectAccessReview calls for specific planned verbs. The checks are not permission to apply. StorageClass metadata does not establish RWX, successful provisioning, or failover. The NodePort default range is an explicit `30000-32767` assumption unless an operator-supplied range is configured. Capacity is a conservative bounded CPU/memory assessment and not the scheduler, HA certification, or a 400 TPS result. The single-control-plane and external-single-Oracle limitations remain visible.

The review plan is not an install authorization. Apply and automated recovery are not implemented. No cleanup or reverse migration is performed by this version.

The Stage 3 examples show the required field shapes. The prepared-schema/import example needs operator-supplied PEM files whose SANs cover the configured external and internal endpoints. The initialization/generated-certificate example emits public artifacts and protected keys while explicitly blocking on its missing offline WSO2 SQL artifact manifest. Preparation does not contact Kubernetes even when the config contains a kubeconfig path; only `validate`/`plan` perform read-only Kubernetes discovery.
