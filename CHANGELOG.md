# Changelog

## 0.1.0-dev - 2026-11-13

- Added a versioned Stage 4 execution-plan and protected checkpoint model with configuration, target, profile, registry digest, and prepared-artifact binding.
- Added a shared fail-closed Apply orchestrator with explicit confirmation, pre-mutation rechecks, ordered image/Oracle/Kubernetes/readiness steps, cancellation boundaries, reconciliation before resume, and uncertain-DDL handling; added fixture adapter coverage for registry-only image reuse and recovery.
- Added CLI execution-plan export, explicit `apply --plan ... --confirm`, and checkpoint `status` inspection. Production registry-import, Oracle, and Kubernetes adapters and the wizard Apply confirmation remain gated and are not claimed as implemented.
- Fixed protected PKCS#12/JKS password generation to use printable Java-compatible characters and replace legacy non-encodable random bytes; added regression coverage.
- Documented the current execution boundary, recovery limits, and remaining Stage 4 qualification work.

## 0.1.0-dev - 2026-11-13

- Added conditional Stage 3 Oracle, schema/account, certificate, artifact, and export-directory forms to the Bubble Tea wizard, with Back/Edit invalidation while retaining the existing registry and Kubernetes review evidence.
- Connected the explicit Stage 3 export action to the existing `preparation.Run` service. Wizard Oracle passwords remain masked and session-only; the CLI continues to use its environment credential source.
- Added cancellable, revision-correlated preparation results showing Oracle/schema/certificate findings, generated public files, blockers, deferred checks, and independent file-generation/deployment-readiness statuses.
- Added fixture tests for wizard export, initialize-mode missing schema artifacts, deferred Oracle validation, credential redaction, cancellation, stale-result rejection, and conditional field editing; documented the interactive workflow and remaining qualification gates.

## 0.1.0-dev - 2026-11-01

- Added configuration schema v2 for Oracle, PKI, deployment DNS, WSO2 schema artifact provenance, and replicated distributed-throttling endpoint inputs, with in-memory v1 migration.
- Added opt-in bounded read-only Oracle authentication and metadata checks using the maintained first-party Go driver; validation exposes only SELECT operations, and no Oracle DDL is executed.
- Added local WSO2 schema distribution/script verification, reusable protected 256-bit shared encryption key, certificate import/generation, public trust exports, Java keystore preparation, and independent candidate deployment configuration rendering.
- Added `prepare --config ... --output ... --state-dir ... [--check-oracle]` for atomic redacted public output separated from protected private state. Apply remains unavailable.
- Added fixture tests for generated preparation, stable asset hashes, private-state reuse, output overwrite refusal, sanitized Secret references, and deployment asset references.
- Exact WSO2 SQL source artifacts, Java interoperability, live Oracle checks, Stage 3 TUI forms, and exact-release deployment qualification remain pending or explicitly blocked/deferred.

- Added bundle schema version 1 and bounded, non-extracting verification for OCI image-layout and Docker image-save archives, including archive hash, referenced content, config platform, layer blob, and DiffID validation.
- Added worker-platform resolution for registry manifests and local archives; incomplete selected-platform coverage blocks use/import planning.
- Added read-only Kubernetes namespace, selected StorageClass, Service/NodePort, worker eligibility, and per-action SelfSubjectAccessReview checks without reading Secret objects or creating persistent resources.
- Added a conservative bounded one-worker-loss CPU/memory placement assessment using Kubernetes pod request semantics and retaining competing workload reservations.
- Integrated archive, registry, platform, cluster, permissions, and capacity evidence into the editable wizard and CLI review. Bundle and cluster changes invalidate dependent review results; stale async results cannot replace newer revisions.
- Apply, imports, deployment, Oracle initialization, certificate deployment, and SSO configuration remain unavailable.

## 0.1.0-dev - 2026-10-31

- Established the Go 1.26 module, version metadata, branded CLI, and release build script foundation.
- Added startup audit logging with run IDs, absolute log path, serialized writes, and secret-canary tests.
- Added a Bubble Tea terminal wizard with registry input, Basic/Bearer auth, Back/Edit, cancel/retry, and dependent validation invalidation.
- Added HTTPS OCI Distribution exact-reference checks and a non-mutating image review plan.
- Added read-only Kubernetes kubeconfig/context discovery for version, nodes, pods, taints, allocatable resources, and current requests, with a local HTTPS API fixture.
- Versioned the candidate deployment profile data for product/JDK/Oracle pins, four source-to-destination images, replica/resource starting points, NodePorts, and JDBC qualification notes.
- Added real-request local TLS registry coverage for auth challenges and registry failure outcomes, plus profile/review rendering assertions.
- Added repository requirements, architecture, usage, format, and qualification records.
