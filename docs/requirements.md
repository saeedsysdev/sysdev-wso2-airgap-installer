# Requirements and acceptance evidence

The complete normative specification is kept in the repository root at [SYSDEV_WSO2_AIRGAP_INSTALLER_CODEX_PROMPT.md](../SYSDEV_WSO2_AIRGAP_INSTALLER_CODEX_PROMPT.md). This document turns its major constraints into implementation gates.

## Product and runtime

- Display name, support identity, attribution, executable name, English-only UI, and `0.1.0-dev` must be consistent across startup, help, version, reports, and documentation.
- The primary implementation is Go with pinned dependencies and supported Windows/amd64 and Linux/amd64 builds. A bundled external offline data bundle is preferred to embedding large archives.
- Keep domain, wizard transitions, validation, planning, providers, UI, and execution independently testable. Use contexts, bounded timeouts, cancellation, typed outcomes, and bounded parallel work.

## Wizard and audit

- Initialize a per-run human-readable audit log before prompts or network access and display its absolute path. Use restricted file permissions and redact credentials at input, provider, logging, state, and export boundaries.
- Wizard answers are revisioned. Upstream edits invalidate dependent validations; asynchronous results must match the active revision. Validation and review do not persist remote resources.
- Back, edit, retry, cancel, help, keyboard-only navigation, resize behavior, no-color output, and non-interactive configuration must use shared validators and planning logic.
- Demo mode uses conspicuous local fixtures and cannot imply real checks passed.

## Registry inventory

- Query exact references for the pinned custom ACP/KM, custom Keycloak, Traffic Manager, and Universal Gateway images. Support anonymous, Basic, and standard Bearer challenge flows using HTTPS and custom CA trust.
- Report distinct missing/not-visible, unauthorized, forbidden, unreachable, TLS, unsupported-manifest, platform, digest, and found outcomes. Never infer a push or Kubernetes pull from a workstation GET.
- Prefer an image source from the registry when all references exist; allow validated offline archives to cover missing references. Push only after explicit Apply.
- Support the versioned bundle schema plus Docker image-save and OCI image-layout tar formats; validate archive hashes and every referenced image config, manifest/index, layer blob, and DiffID without unpacking image filesystems.
- Compare both remote and local image evidence against actual selected worker OS/architectures. `READY TO IMPORT` requires a missing/not-visible destination, verified archive content, selected worker platforms, and complete platform coverage.

## Plan and Apply boundary

- A plan is review-only. Before a distinct final Apply approval, do not push images, create/update cluster resources, initialize schemas, configure Keycloak, or modify WSO2 settings.
- Review includes cluster/workers/capacity, product/image references, archive and platform evidence, placement/storage, NodePorts/domains/TLS, Oracle/SSO, ordered operations, deferred checks, warnings, and blockers. Unknown inputs must be called unknown.
- Kubernetes preflight uses the selected API context, read-only inventory and per-action SelfSubjectAccessReview; it does not create diagnostic resources or read Secret contents. Unavailable evidence remains denied, indeterminate, or deferred.
- One-worker-loss capacity uses actual selected eligible workers, maintained Kubernetes pod-request calculations, existing reservations, per-node placement, and conservative retention of competing failed-node workloads. It must be labeled a bounded preflight assessment, not scheduler simulation, HA certification, or throughput proof.
- Apply must freeze a validated revision, recheck time-sensitive inputs, checkpoint operations, support safe cancellation/retry/resume, and avoid destructive cleanup or reverse migrations.

## Stage 3 local preparation boundary

- Configuration schema v2 carries non-secret Oracle, schema, PKI, DNS, artifact provenance, and distributed-throttle inputs; schema v1 migrates in memory only.
- `prepare` creates a new public output directory atomically and keeps generated private material and the reusable 256-bit WSO2 encryption key in a separate protected state directory. Existing output and key material are never silently overwritten.
- Oracle validation, when explicitly requested, authenticates using process-environment credentials and performs bounded read-only `SELECT` metadata checks. Preparation cannot execute DDL or create a diagnostic object.
- Local WSO2 script artifacts must match a versioned manifest, source archive size/hash, script hashes, safe paths, target schema names and execution order. Hash integrity is not proof of vendor origin. Keycloak remains on its supported built-in schema migration path in Stage 4.
- Certificate imports must match keys, SANs, server use, validity and supplied trust anchors. Local generation reuses a protected CA; only public trust/certificate files are exported. The CA is not installed into operating-system or external trust stores.
- Generated manifests use references to protected secrets, include typed unresolved bindings and checksums, and remain preparation candidates. Apply and all external mutations remain unavailable.

## Qualification gates

- Prove exact-release Oracle/JDBC compatibility, image build provenance, schema handling, certificates, Kubernetes permissions/capacity, SSO/roles/token behavior, distributed throttling, external load-balancer routing, and worker-loss behavior with real, explicitly authorized qualification environments.
- Keep test evidence separated into implemented, unit-tested, integration-tested, and pending. Do not claim 400 TPS, HA, immediate revocation, or complete installation without measured evidence.
