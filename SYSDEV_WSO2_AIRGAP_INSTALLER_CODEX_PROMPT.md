# Codex development task: Sysdev Wso2 Airgap Installer

Work in the local checkout of this repository:

https://github.com/saeedsysdev/sysdev-wso2-airgap-installer

Build a maintainable, cross-platform Go terminal application for preparing and deploying the WSO2 stack described below. This is an implementation task, not a request for a proposal or a mock installer. Preserve existing work and applicable repository instructions. Inspect the actual checkout before editing; the repository may have changed since this specification was written.

The specification is self-contained. Do not assume access to an earlier conversation, ChatGPT Library attachment, private credentials, or a running Kubernetes cluster. Treat any earlier preparation scripts already present as unverified inputs to review, not proof of compatibility.

## 1. Product identity and language

- Application display name, exactly: `Sysdev Wso2 Airgap Installer`.
- Developer/support identity, exactly: `SaeedSysDev`.
- Attribution text, exactly: `Developed and Supported by SaeedSysDev`.
- Executable name: `sysdev-wso2-airgap-installer`; Windows adds `.exe`.
- Show name, version, and attribution in startup, help/about/version output, and installation reports. Keep attribution in an unobtrusive footer while the interactive wizard is active.
- All application-facing text must be English: prompts, help, validation messages, statuses, progress, logs, reports, examples, and documentation. Do not implement a Persian UI.
- Do not invent a support email, telephone number, organization, or website. Use the repository URL as the known support/project link.

## 2. Versioning

- Use Semantic Versioning. Begin development at `0.1.0-dev`; do not label an unqualified implementation production-ready.
- Maintain one authoritative application version source and a changelog. Embed application version, Git commit, build timestamp, and dirty/build state appropriately.
- Provide human-readable `version` and machine-readable `version --json` output. Show sensible development metadata when built directly with `go run`.
- Version application releases, configuration schemas, session schemas, bundle schemas, and WSO2/Keycloak compatibility profiles independently.
- Detect unsupported schema versions and migrate older formats explicitly and non-destructively. Never silently reinterpret them.
- Pin Go module dependencies, record the supported Go toolchain, and keep `go.sum` checked in. Do not use unversioned runtime dependency downloads.
- Build Windows/amd64 first, while keeping Linux/amd64 a first-class build target from the same codebase. Prepare release artifacts and checksums for both without automatically publishing a release.

## 3. Architecture and portability

- Use Go for the application and shared installer logic. Prefer a self-contained executable plus an external offline data bundle; do not embed gigabytes of image archives into the executable.
- Target a runtime without a preinstalled Go, Python, Java, Docker, PowerShell, Helm, or kubectl requirement where practical. Development and image preparation may need additional tools. Do not make implementation substantially worse merely to eliminate a justified dependency: document unavoidable dependencies and bundle portable tools when suitable.
- These are prerequisites of the installer host. Java and JDBC drivers inside the WSO2/Keycloak workload images remain normal dependencies of those products.
- Prefer maintained Go libraries for Kubernetes API access, OCI registry/archive handling, certificate handling, and structured logging. Avoid shell pipelines, command injection, or parsing human CLI output when an API is available.
- Use a maintained Go TUI framework. Bubble Tea with compatible Bubbles and Lip Gloss releases is the preferred starting point. Verify current official import paths and pin mutually compatible versions; do not mix major-version APIs from old examples.
- Keep the business model, wizard state transitions, validation, planning, and deployment logic independent of rendering and operating-system code.
- Use small interfaces around real external boundaries, including Kubernetes, registry, Oracle access, filesystem/credential storage, clock, and deployment executor. Avoid a monolithic `main.go`, an unnecessary plugin runtime, or an elaborate generic framework.
- Organize the code into coherent modules for command entrypoints, application orchestration, configuration/domain types, UI, wizard, validators, provider adapters, bundles, deployment, logs, state, and version metadata. Use OS-specific files only where genuinely necessary.
- Make adding a wizard step, validator, component, product profile, or report format straightforward. A new step must declare its dependencies and validation behavior rather than require edits throughout the application.
- Use `context.Context`, bounded network timeouts, cancellation, bounded concurrency, and typed errors. Long network checks must not freeze the UI.
- Keep implementation, comments, and repository documentation in English.

## 4. Terminal experience

Create a polished text-based terminal interface, not a desktop GUI, web app, or sequence of bare `fmt.Scanln` questions.

Include:

- A readable branded header, version, progress/step indicator, navigation hints, and support attribution.
- Clear sections, aligned fields, tasteful colors, status labels, tables, spinners during actual work, and progress bars where progress can be measured.
- Explicit textual statuses in addition to color. Never use an animation or progress bar to imply work happened when it did not.
- Keyboard operation without a mouse: next, back, edit, retry, cancel, and help. Show the available shortcuts. Do not steal ordinary text-editing keys such as Backspace for global navigation.
- Responsive resizing and scrolling for narrow or short terminals. Handle Windows Terminal, PowerShell terminals, Linux terminals, and SSH-style terminals without OS-specific wizard rewrites.
- Unicode/color capability detection, a plain/ASCII fallback, `--no-color`/`NO_COLOR` support, and readable non-interactive output. Do not require special fonts, emoji, images, or internet-hosted UI assets.
- Password fields that hide input. Secrets must not appear in a review table, validation error, clipboard helper, or screenshot-oriented demo output.
- A non-interactive path using a configuration file, with the same validators and plan/executor used by the TUI. Non-interactive mode must not hang waiting for stdin. Require an explicit apply option for mutations.
- A clearly labeled `demo` mode using deterministic local fixtures so the UI can be reviewed without credentials or infrastructure. Demo must be unmistakable and cannot silently substitute fake success for real validation or installation.

## 5. Logging starts before the wizard

Initialize logging before the first user question or network operation. Immediately display the absolute log-file location, and keep that location discoverable during the wizard and in the final report.

- Use an OS-appropriate application state/log directory: per-user LocalAppData on Windows and XDG state conventions with a safe fallback on Linux. Support an explicit `--log-dir` override.
- Create a unique run ID and timestamped, human-readable operation log for each run. Structured JSONL may additionally be provided, but an operator must be able to follow the main log in a text editor.
- Log step entry/exit, sanitized configuration changes, validation attempts/results, endpoint/component identifiers, image checks, planned/apply operations, retries, durations, errors and error chains, cancellation, and a final outcome.
- Include timestamp with timezone, severity, run ID, step/operation ID, context, and a useful explanation. Preserve diagnostic causes without dumping secret-bearing requests or responses.
- Never log passwords, bearer/access/refresh tokens, client secrets, private keys, secret values, complete kubeconfigs, registry authorization headers, credentials embedded in URLs, or database connection strings containing credentials. Redact at structured boundaries, not just with a last-minute string replacement.
- Redact both successful and failed operations, nested errors, debug output, session exports, subprocess output, and support bundles. Add tests using recognizable canary secrets.
- Write files with user-restricted permissions, handling Windows ACLs appropriately. Keep logging thread-safe and terminal rendering separate from file output.
- If the log cannot be created at startup, show an actionable error and exit before operations. Flush reliably during normal exit and cancellation. Handle write/disk-full failures explicitly and stop before further mutations if the required audit trail cannot be maintained.
- Do not launch an alternate-screen UI that permanently hides the initial log path without showing it in the UI as well. Restore the terminal on exit or error.

## 6. Wizard state, validation, and Back

Model the wizard as explicit, testable state transitions with a validated configuration revision.

- Every submitted answer receives appropriate local validation immediately. For connection-dependent answers, run real bounded connectivity/authentication/compatibility checks before treating the step as validated.
- Represent validation results as pending, running, passed, failed, stale, or explicitly deferred. Store result details, checked time, relevant input revision, and dependency information.
- On failure, retain useful input and present Edit, Retry, Back, or Cancel. Do not silently proceed or show a green success status for an untested value.
- Allow Back/Edit throughout configuration, validation, and final review. Returning to a previous step must preserve other still-valid inputs.
- Changing upstream input invalidates the appropriate dependent results. For example, changing registry host, project path, credentials, or trust invalidates image inventory; changing cluster/context invalidates node, permission, storage and scheduling checks; changing domain names invalidates generated certificate plans and SSO URLs.
- Prevent stale asynchronous checks from updating a newer configuration. Cancel obsolete work and correlate each result with the step/configuration generation.
- Keep validation free of remote persistent changes. Local logs, temporary artifacts, and protected session state are allowed. Kubernetes non-persisting authorization reviews and supported server-side dry runs are allowed; resource creation is not a read-only test.
- Some checks may require a diagnostic workload inside the cluster. Use an existing explicitly selected diagnostic facility where available. Otherwise label that check deferred, explain why, and make it an early post-Apply prerequisite. Never create a test Pod, PVC, schema, or registry tag during configuration merely to claim every check passed.
- Do not claim a registry push or Kubernetes image pull was tested merely because the operator workstation could read a manifest. These are distinct checks.
- Save resumable non-secret state. Do not serialize secret fields into plaintext JSON/YAML or logs. Support session-only secrets with re-entry after restart; if persistent secret storage is implemented, use OS-protected storage or explicit authenticated encryption with a documented recovery model.

## 7. Strict boundary before Apply

Before the operator approves the final reviewed plan, do not push images, create or update Kubernetes resources, create namespaces/Secrets/PVCs, modify node configuration, run schema DDL, create Keycloak realms/clients/users, or change WSO2 configuration.

The final review must show:

- Target cluster identity, API endpoint, context, namespace, and selected workers.
- Product/application/profile versions and actual image references/digests where known.
- Image inventory, missing/importable images, archive verification and image-platform results.
- Replica/resource layout, placement constraints, storage claims/class, and one-worker-loss capacity assessment.
- Domains, selected NodePorts, load-balancer requirements, certificate/trust plan, Oracle schema targets, and SSO configuration summary.
- Ordered operations, existing-resource handling, deferred checks, warnings, and blockers, with secrets redacted.

Allow editing this review. Freeze the selected configuration revision/plan on Apply and recheck time-sensitive prerequisites before executing it. Interactive installation requires one clear final Apply action; non-interactive application requires an explicit equivalent flag. A `plan` command alone never authorizes changes.

After Apply begins, Back is not a rollback mechanism. Provide safe cancellation at operation boundaries, precise operation status, retries/resume, and honest recovery instructions. Do not automatically reverse database migrations or delete existing user resources.

## 8. Runtime environment and cluster discovery

- The destination is fully air-gapped. The final install/validate/plan/apply path must not contact public registries, package managers, Helm repositories, online certificate authorities, telemetry services, or CDNs.
- Kubernetes already exists. Minimum declared environment: one control-plane node, three workers, at least 16 CPU cores and 20 GB RAM per node. More nodes may exist. Discover actual Kubernetes version, node architecture, readiness, labels, taints, allocatable resources, and existing reservations.
- Normally authenticate through a user-selected kubeconfig file and context/API endpoint. Do not require SSH keys or an IP address for a particular master when Kubernetes API access is sufficient. Report any existing authentication-plugin prerequisites clearly.
- Workloads run on eligible workers, not on the control-plane node by default. Do not change cluster networking, container runtimes, kube-proxy, control-plane membership, or existing unrelated workloads automatically.
- Read permissions and required authorization using appropriate API checks. A missing namespace is a planned creation, not permission to create it during validation.
- Target tolerance is one worker failure in one site. A single control-plane node, single external Oracle server, non-redundant load balancer, or node-bound storage remains a separate limitation. Do not advertise full infrastructure HA or zero failed in-flight requests.
- Check available capacity and scheduling implications after losing each eligible worker, including placement/storage constraints; do not sum physical RAM or count the master's resources as application capacity.
- A target of about 400 TPS is a load-test objective, not a result established by resource arithmetic or replica counts.

## 9. Registry and image inventory

Ask for the registry host/port, project/repository prefix, anonymous or authenticated access, optional username/password/token as applicable, and custom CA trust information. Never hardcode an organization registry address.

After the registry details are submitted:

- Validate syntax, reachability, TLS trust, the registry protocol, and actual authorized access.
- Handle standard registry Bearer challenges as well as supported basic authentication. Do not classify an authentication challenge as network failure.
- Use exact required repository/reference manifest checks; do not depend on permission to list the registry catalog or all tags.
- Distinguish missing/not visible, unauthorized, forbidden, unreachable, TLS failure, unsupported manifest, platform mismatch, digest mismatch, found, and unknown. Registry authorization can deliberately mask existence; do not assert nonexistence when the response cannot establish it.
- Display a useful image-status table with component, repository/reference, expected/observed digest when available, platform, remote status, local archive availability, and proposed action.
- Missing remote images are recoverable when a validated offline archive contains the required images: mark `READY TO IMPORT`, include import in the final plan, and perform the push only after Apply.
- Registry contents alone must also be a supported source when all required images are already present; do not require unnecessary archives.
- Validate the final deployed image's manifest for the nodes' OS/architecture. Keep tag, archive hash, image configuration digest, OCI manifest digest, and index digest distinct. A locally built image may not yet have a destination registry manifest digest; record the real digest after push rather than inventing it.
- Use bounded concurrent checks, cancellation, and clear retry options.
- HTTPS and certificate verification are the default. Support custom internal CAs. Any deliberately supported insecure registry mode must be explicit and visible, never an automatic fallback after TLS failure.
- Distinguish workstation trust, in-cluster runtime trust, pull authorization, and registry write access. Creating an imagePullSecret does not install a registry CA into node runtimes.
- Do not automatically modify all node runtimes to install registry trust; report the required infrastructure preparation separately.

## 10. Product compatibility and initial images

Initial compatibility profile, subject to actual integration qualification:

- WSO2 API Manager `4.7.0`, fully distributed Pattern 4, Classic/Universal Gateway.
- External Oracle `19c`, minimum `19.3`, already installed on a dedicated server outside Kubernetes.
- Keycloak candidate `26.8.1`, installed by this application inside Kubernetes, with Oracle as its database.
- Candidate Oracle JDBC artifacts from the earlier design: `ojdbc17` and `orai18n`, version `23.26.2.0.0`. Verify them against official vendor guidance and the chosen runtimes; do not claim that the earlier choice proves WSO2 compatibility.

Known base/source images:

```text
docker.io/wso2/wso2am-acp:4.7.0
docker.io/wso2/wso2am-tm:4.7.0
docker.io/wso2/wso2am-universal-gw:4.7.0
quay.io/keycloak/keycloak:26.8.1
```

Proposed custom image outputs, mapped to the user's destination registry:

```text
apim-acp-oracle:4.7.0-r1
keycloak-oracle:26.8.1-r1
```

The custom ACP image is reused for the separated resident WSO2 Key Manager. The runtime bundle therefore starts with four images: custom ACP/KM, custom Keycloak, stock Traffic Manager, and stock Universal Gateway. Any additional actual runtime/helper image must be declared, prepared, verified, and included in the inventory; hidden downloads are unacceptable.

- Keep image mappings, digests, schemas, artifact hashes, and compatibility data in versioned descriptors rather than scattered constants.
- Perform online acquisition/building only in an explicitly separate preparation workflow on a connected machine. Use local COPY inputs and pinned sources for image builds. Never bake environment passwords, private certificates, or kubeconfigs into images.
- The vendor's Helm deployment repository has separate commercial licensing. Do not copy or redistribute commercially licensed Helm templates while presenting them as open-source project code. Author independent deployment assets against documented runtime interfaces, preserve third-party notices, and investigate any license uncertainty explicitly.
- Verify WSO2 runtime configuration, component ports, Oracle schema scripts, Keycloak integration behavior and health endpoints from the exact release's official documentation/source. Do not invent API endpoints or reuse old profiles without validation.
- Keep the default installation profile pinned. Do not choose `latest` dynamically during offline installation. Future product versions must be new/updated compatibility profiles.

## 11. HA component layout and resources

Initial layout:

- API Control Plane: 2 instances.
- Resident WSO2 Key Manager: 2 instances, isolated from the control plane.
- Traffic Manager: 2 independently addressable instances.
- Universal Gateway: 2 instances.
- Keycloak: 3 instances with the supported single-site clustering mode.

Retain the isolated resident Key Manager in the initial profile until end-to-end tests demonstrate that it can safely be removed for the selected integrations. Keycloak is the external identity/token authority; this does not by itself prove every internal WSO2 key-management function disappears.

Use node anti-affinity/topology spreading, appropriate disruption budgets, supported probes, graceful termination, and sufficient startup periods. Stable per-instance addressing and supported event/decision duplication are essential for the Traffic Managers; a generic two-replica Service is not proof of correct throttling HA.

Provisional per-instance resource starting points:

```text
Component   Count   CPU request   Memory request   Memory limit
ACP         2       1500m         3Gi              3584Mi
KM          2       1000m         2Gi              2560Mi
TM          2       1500m         3Gi              3584Mi
Gateway     2       2000m         3Gi              3584Mi
Keycloak    3       500m          1Gi              1536Mi
```

This totals approximately 25 GiB requested memory, not including existing cluster workloads. Treat these values as editable test starting points. Tune heap/native memory consistently with limits. Record performance and failure-tolerance claims as unqualified until exercised against real deployments.

## 12. External networking

- The operator has an external load balancer and wants fixed ports reachable on every worker. Use independent NodePort Services; an extra Ingress controller is not required for the initial profile.
- Suggested editable mappings: control plane/portals `30043 -> 9443`, gateway HTTPS `30044 -> 8243`, Keycloak HTTPS `30045 -> 8443`.
- Default `externalTrafficPolicy: Cluster` so a worker without a local endpoint can forward to a ready endpoint on another worker. Validate service-port availability and the actual permitted NodePort range, including server-side dry run where supported.
- NodePort may also be exposed on control-plane nodes by cluster configuration; do not claim worker-only exposure. The external load balancer's selected pool contains workers, with appropriate infrastructure network restrictions.
- Keep Traffic Manager messaging, synchronization, resident Key Manager internals and management/metrics endpoints internal unless a specific supported requirement justifies exposure.
- Ask for a base domain and allow overriding derived hosts such as `apim.<domain>`, `api.<domain>`, and `sso.<domain>`. Portals can use `/publisher`, `/devportal`, and `/admin` on the control-plane hostname.
- Ask for required public-facing URLs and load-balancer TLS mode. Support a documented passthrough arrangement using appropriate VIPs/SNI routing, or verified TLS re-encryption after L7 termination.
- Require stable issuer/redirect URLs and correct internal/external DNS reachability before declaring SSO verified. Do not change token issuer merely because the installer used a NodePort during setup.
- `Cluster` traffic policy can SNAT source IP. Do not promise original client IP preservation. Trust forwarding headers only from explicitly trusted L7 proxies, and do not pretend an L4 TLS passthrough proxy can insert HTTP headers.
- Produce a load-balancer handoff report with hosts, ports, selected worker addresses, TLS/SNI expectations, and release-verified health-check guidance. Do not automatically configure an unspecified external load-balancer product.

## 13. Oracle and storage

- Do not install or replace Oracle. Collect server/service/connection details and credentials for separate WSO2 API management, WSO2 shared, and Keycloak schemas. Suggested editable names: `WSO2_APIM`, `WSO2_SHARED`, `KEYCLOAK`.
- Separate schema-owner/migration and runtime privileges where supported. Support existing prepared schemas as well as deliberate initialization, without destructive reset on rerun.
- Validate connection, database version, relevant grants/metadata, and character-set suitability using read-only operations when accessible. `AL32UTF8` is the target for Unicode suitability; do not alter an existing database's character set automatically.
- Support TCPS and certificate/wallet/trust inputs where required. Do not claim TLS was enabled on Oracle by editing only application settings.
- If the workstation cannot reach Oracle but the cluster can, show the validation limitation accurately and execute the necessary cluster-origin check after Apply, before dependent schema/workload operations. Do not fake success or silently demand a new network topology.
- Use the exact WSO2 Oracle scripts from the chosen distribution and a script runner that correctly handles Oracle/PLSQL and the script's command semantics. Do not naively split all SQL on semicolons.
- Initialize/migrate Keycloak through the supported mechanism with one initial instance, then scale it. Avoid racing initialization across all replicas.
- Ask for a StorageClass, enumerate/validate available classes where authorized, and use it for required persistent volumes.
- Do not assume RWX support or share one RWO claim across replicas. Examine provisioner, binding/topology behavior and node-loss implications. Node-local storage cannot silently count as HA storage.
- Persist actual required data, such as independent control-plane search indexes where needed; do not invent a PVC for every container directory. Prefer stdout logging for workloads and existing cluster collection.

## 14. Keycloak, SSO and authorization

- Configure Keycloak for normal human authentication in Publisher, Developer Portal and Admin Portal wherever supported by the exact release. Implement/test each portal flow separately; do not infer Admin Portal completion from a Publisher login.
- Configure Keycloak as the external Key Manager for API consumer OAuth/OIDC tokens, using the supported connector and exact-version integration configuration.
- Implement explicit claim, group and role mapping. Test subscriber, publisher, administrator and denied-role cases. Authentication must not automatically grant administrative privileges.
- Test client/application registration, scopes, access/refresh token flows, issuer/audience/signature checks, logout and the selected revocation behavior.
- Distinguish local JWT verification from introspection and state the practical revocation behavior; local signature verification alone does not establish immediate token revocation.
- Use least-privilege service accounts. Some internal WSO2 protocols still require supported product credentials or other authentication: do not force unsupported Keycloak authentication into every internal connection.
- Keep tightly restricted local bootstrap/break-glass access where necessary, rather than making normal users maintain duplicate passwords. Rotate/remove temporary bootstrap credentials when supported.
- Prefer supported single-site Keycloak clustering, including its embedded cache and discovery configuration; do not introduce an external cache cluster without a demonstrated requirement.

## 15. Certificates and encryption

- Prompt for existing certificate, private-key and full-chain file paths. Validate parseability, key match, SANs, intended use, validity period and trust chain.
- If certificates are unavailable, generate a dedicated private CA and suitable service certificates. Include the actual external and internal service DNS SANs needed by the deployment.
- Generated certificates are not automatically trusted by browsers, Windows, load balancers or node runtimes. Produce explicit trust outputs/instructions and configure application trust stores correctly.
- Do not implement `InsecureSkipVerify`, trust-all hostname verification, or internet certificate issuance as the default fix for certificate failures.
- Protect private keys and the CA key outside image layers; do not distribute the root CA private key to workloads.
- WSO2 4.7.0 requires a consistent shared 256-bit encryption key. Generate it once, store it securely, use it across the applicable components, and preserve it on resume/rerun. Never rotate it merely because the wizard restarted.

## 16. Apply, resume and reports

- Build an ordered deployment plan from the validated immutable configuration. Use only the selected internal registry and verified local artifacts.
- Apply operations should be idempotent where supported, ownership-aware, namespace-scoped where possible, checkpointed, and resumable after interrupted execution.
- Determine whether existing resources belong to this installation. Do not overwrite unrelated resources that happen to share names, delete namespaces, reset schemas, or run destructive cleanup on failure.
- Import missing images after final Apply; record actual destination digests. Keep image pull credentials in appropriate Kubernetes Secrets and inject configuration/credentials without exposing them in generated public reports.
- Apply component configuration and dependencies in supported order; wait for actual readiness, then configure/verify identity integrations. `Pod Running` alone is not a successful install.
- Separate cluster-internal readiness from external load-balancer/DNS/SSO verification. If external infrastructure is not yet configured, clearly report the remaining work rather than claiming full end-to-end success.
- Record operation outcomes, deferred/skipped checks, product versions, redacted configuration, endpoints, actual manifests/digests, log locations and recovery actions in the final report.
- Provide a sanitized support export. Exclude secrets, private keys and full kubeconfigs by default.
- Design commands coherently, including wizard/install, validate, plan, apply, status, verify, version, demo and support export. Shared logic must not fork between CLI and TUI. Preparation, if provided, is a separate explicitly online workflow.

## 17. Testing and completion evidence

Implement meaningful automated tests around the failure-prone behavior:

- Wizard Back/Edit/Retry/Cancel transitions; downstream invalidation; outdated asynchronous results; no real mutation before Apply; immutable plan revisions.
- Registry anonymous and authenticated flows, Bearer challenges, known-reference queries, missing/hidden images, TLS/auth/network failures, digest/platform mismatches, importable archives and cancellation.
- Secret redaction across nested errors, logs, sessions and support export; startup/log-write failures; simultaneous writes.
- Archive path safety, integrity and manifest/config/index digest distinctions.
- Configuration/schema migrations, idempotent resume, existing-resource ownership and typed actionable errors.
- Terminal rendering/navigation/resize/plain output and non-interactive behavior.
- Provider/deployment ordering with fakes or test servers, and explicitly separate opt-in real-infrastructure tests.

Run formatting, `go vet`, relevant tests and Windows/Linux builds. Run the race detector where supported by the test toolchain; do not confuse its build requirements with the release binary's runtime requirements. CI must exercise native Windows and Linux tests. Avoid tests that merely duplicate implementation details.

Use actual integration evidence before certifying Oracle compatibility, all portal SSO flows, correct distributed throttling, 400 TPS or one-worker-loss behavior. Do not fabricate benchmark results, image digests or passing integration tests. Missing infrastructure should produce a documented unexecuted qualification test, not a fake pass or a destructive deployment attempt.

## 18. Repository deliverables and execution approach

Create/update:

- Working Go source and pinned module files.
- A short root `AGENTS.md` with durable project rules, branding, safety boundaries, real build/test commands and pointers to detailed documents. Keep the complete specification in project documentation, not duplicated verbatim into `AGENTS.md`.
- README, requirements, architecture/decisions, configuration/bundle formats, testing/qualification, usage/recovery guidance and changelog.
- Version/build scripts, Windows/Linux CI, example non-secret configuration, deterministic demo fixtures and independent deployment assets.
- A small capability/qualification status record distinguishing implemented, unit-tested, integration-tested and pending behavior.

Work in coherent milestones: executable foundation and logging; real validated wizard with Back navigation and registry image inventory; plan/bundle/Kubernetes/Oracle/PKI adapters; deployment and identity integration; qualification and packaging. Implement and test usable vertical slices rather than producing a large set of empty interfaces or documents and stopping.

Begin by briefly stating your implementation approach, then create the application and run checks. Make ordinary implementation choices autonomously and continue through achievable work. Ask only for an actual unresolved product decision or missing requirement that changes the design; do not request approval just to write normal project code.

The first usable milestone must actually run, show the exact branding and version plus the log location, navigate forward/back, invalidate dependent results, query a real registry through a production adapter, display the image table, and generate a non-mutating review plan. Demo and local test servers must make this behavior reviewable without live credentials. Do not call this milestone a complete WSO2 deployment installer until the apply and qualification requirements are fulfilled.

For development, do not contact or mutate a real production cluster, registry, Oracle server or identity service without explicit environment-specific authorization. Use fakes, local test services and dry runs where appropriate. Do not push commits, create remote tags or publish releases unless instructed.

Finish each implementation milestone with a concise report of what now works, exact commands executed and results, artifacts produced, and precise remaining limitations. Preserve progress/decisions in the repository so future feature changes do not require reconstructing this conversation.
