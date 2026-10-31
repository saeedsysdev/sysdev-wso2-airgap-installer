# Project guidance

- Product name: `Sysdev Wso2 Airgap Installer`; support identity: `SaeedSysDev`; attribution: `Developed and Supported by SaeedSysDev`.
- Keep all application-facing text and repository documentation in English. Application version is `0.1.0-dev`; update the single source in `internal/version/version.go` and record changes in `CHANGELOG.md`.
- Read `SYSDEV_WSO2_AIRGAP_INSTALLER_CODEX_PROMPT.md` before changing scope. `docs/requirements.md`, `docs/architecture.md`, and `docs/qualification.md` summarize implementation contracts and known gaps.
- Keep UI, wizard state, validation, registry/provider code, plans, logging, and execution boundaries separate. Add provider fakes or local test servers for tests; never target a real cluster, registry, Oracle service, or identity service during development without environment-specific authorization.
- Configuration validation, image checks, and plans must remain non-mutating. Mutations require a distinct explicit Apply path; do not push images or create/update Kubernetes, Oracle, Keycloak, or certificate resources during review.
- Never log or persist credentials, kubeconfigs, private keys, authorization headers, or credential-bearing URLs. Add canary-based redaction tests when secret-handling paths change.
- Treat pinned WSO2/Keycloak/JDBC versions as candidates until exact-release integration qualification is documented. Do not claim HA, SSO, throughput, Oracle compatibility, or one-worker-loss results without evidence.
- Supported toolchain: Go 1.26.0 or newer compatible with the pinned module. Commands from repository root:

  ```powershell
  Get-ChildItem cmd,internal -Recurse -Filter *.go | ForEach-Object { gofmt -w $_.FullName }
  go test ./...
  go vet ./...
  $env:GOOS='windows'; $env:GOARCH='amd64'; go build -o dist/sysdev-wso2-airgap-installer.exe ./cmd/sysdev-wso2-airgap-installer
  $env:GOOS='linux'; $env:GOARCH='amd64'; go build -o dist/sysdev-wso2-airgap-installer ./cmd/sysdev-wso2-airgap-installer
  ```

  Run `go test -race ./...` on Linux/WSL with CGO and a C compiler enabled.

- Use `scripts/build-release.ps1` to produce both local cross-platform binaries and SHA-256 checksums. It does not publish or tag releases.
