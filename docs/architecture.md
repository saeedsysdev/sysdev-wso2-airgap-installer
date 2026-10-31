# Architecture and decisions

## Current package map

- `cmd/sysdev-wso2-airgap-installer`: command routing, version output, startup log initialization.
- `internal/domain`: exact product identity constants.
- `internal/version`: authoritative application version and machine-readable metadata shape.
- `internal/config`: strict versioned JSON decoding for non-secret configuration.
- `internal/cluster`: read-only kubeconfig/context selection, Kubernetes API reads, per-action SelfSubjectAccessReview, selected-worker validation, and bounded one-worker-loss placement assessment.
- `internal/auditlog`: per-run file creation, synchronized writes, run IDs, and secret redaction.
- `internal/registry`: HTTPS OCI Distribution manifest checks, auth challenges, CA trust, result taxonomy, initial profile inventory.
- `internal/wizard`: Bubble Tea view and explicit wizard state/revision invalidation.
- `internal/app`: orchestration between UI, validators/providers, and audit logging.
- `internal/imagearchive`: bounded bundle and tar validation for Docker image-save and OCI image-layout formats; no image root filesystem extraction.
- `internal/plan`: construction of an integrated non-mutating registry/archive/platform/Kubernetes/capacity review with known gaps and blockers.
- `internal/oracleprep`: bounded Oracle 19c authenticated SELECT-only metadata inspection through the pinned first-party Go driver; no DDL capability is exposed.
- `internal/secretstore`: stable installation encryption key and protected private material separated from public plan output.
- `internal/certprep`: import validation, reusable local CA/leaf generation, protected keystores, and public trust outputs.
- `internal/schemaartifacts`: local WSO2 distribution/schema script hash verification with explicit source-provenance qualification.
- `internal/deploymentassets`: independently authored candidate Kubernetes and product configuration assets with secret references and typed Stage 4/5 bindings.
- `internal/preparation`: atomically generated redacted local review, asset hashes, and protected state coordination; does not apply resources.

The UI does not perform HTTP directly. Registry I/O executes as Bubble Tea commands with bounded timeouts, cancellation, and revision correlation. The initial slice deliberately checks exact references rather than requiring `_catalog` or tag enumeration. Authentication credentials exist only in memory or process environment; only schema-v1 non-secret config is read from JSON. Session persistence is not implemented yet.

Kubernetes discovery uses the Kubernetes-published `client-go` v0.35.0 module set and maintained `k8s.io/component-helpers` request calculations. It calls the selected API's version endpoint and reads nodes, pods, target namespace, selected StorageClass, and Services, then submits transient SelfSubjectAccessReview requests for planned verbs. It does not read Secret objects, create diagnostic workloads, or issue persistent writes. The capacity assessment retains nonterminal competing workloads and uses a bounded per-node CPU/memory placement search; it is not a scheduler simulation. Kubeconfig exec credential plugins may run their configured local helper.

Bundle verification validates bundle schema version 1 and explicit profile image mappings, recorded archive sizes and hashes, safe tar structure, OCI descriptor integrity or Docker-save metadata references, config platform, layer content hashes, and uncompressed DiffIDs. Tar scanning is bounded and does not extract image filesystems. Registry and archive evidence remain distinct, and no destination digest is invented before a future authorized push.

## Decisions

1. Pin the maintained Charm v2 modules as a compatible Bubble Tea stack (`bubbletea/v2` 2.0.10 and `lipgloss/v2` 2.0.6). The repository's supported minimum Go version is 1.26.0.
2. Use Go's standard HTTP/TLS, JSON, filesystem, and logging primitives for registry checks; use Kubernetes-published `client-go` v0.35.0 for read-only cluster discovery. Keep these adapters behind testable packages and never call public services as part of discovery.
3. Keep `0.1.0-dev` in one authoritative constant. Git commit, build time, and dirty state are linker-injected fields and use development values under `go run`.
4. Registry and Bearer token realms are HTTPS-only with certificate verification. The standard Bearer challenge may direct a credential exchange to a separate HTTPS realm; redirects are disabled, and credentials are never sent to an HTTP realm. A custom CA bundle can be selected in non-secret config or with `SYSDEV_REGISTRY_CA_BUNDLE`.
5. A plan can show unknown fields and blockers. Constructing or viewing it never authorizes a mutation.

## Remaining module boundaries

The `prepare` command coordinates non-secret schema-v2 configuration, optional Oracle SELECT validation, local schema-artifact verification, PKI preparation, stable protected encryption-key storage, and independent candidate asset rendering. Its output remains a preparation candidate: the Bubble Tea wizard does not yet expose Stage 3's full Oracle/certificate form fields, no exact distribution scripts are included, no Java/keytool runtime was available for external JKS interoperability, and the authored deployment assets require exact-release validation. Apply, execution/recovery and identity integration remain later milestones.
