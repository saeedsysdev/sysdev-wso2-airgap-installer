# Configuration, session, and bundle formats

Application versioning is independent from all persisted-format versions. The application starts at `0.1.0-dev`; configuration, session, offline bundle, and compatibility profile schema versions will each have their own explicit version fields and migration rules.

Configuration schema version 2 retains version 1 registry/Kubernetes/image inputs and adds non-secret Oracle connection/schema metadata, certificate input paths/mode and public hostnames, deployment DNS/output metadata, exact WSO2 distribution/schema-manifest paths, and a replicated Redis/Valkey throttling endpoint. Version 1 loads through an in-memory migration and is never rewritten by `Load`. Unknown JSON fields remain rejected. Oracle and registry passwords are never accepted in configuration JSON; registry uses `SYSDEV_REGISTRY_*`, Oracle validation uses `SYSDEV_ORACLE_APIM_USERNAME/PASSWORD`, `SYSDEV_ORACLE_SHARED_USERNAME/PASSWORD`, and `SYSDEV_ORACLE_KEYCLOAK_USERNAME/PASSWORD`. Do not put secret values in configuration or generated public exports.

`prepare --config PATH --output NEW_DIRECTORY --state-dir PROTECTED_DIRECTORY` creates a new public review/assets directory atomically and a separate protected state directory. `--check-oracle` opts into bounded authenticated read-only Oracle SELECT checks. The state directory contains the installation's stable 32-byte WSO2 shared encryption key and generated private material, protected by current-user ACLs on Windows or 0700/0600 Unix permissions. Output directories must not exist; no overwrite or automatic root trust installation occurs. The review records a preparation-only qualification status; Apply remains unavailable.

Oracle mode is `prepared` or `initialize`. Host/port plus service name, or a credential-free connect descriptor, selects the connection target. TCPS requires wallet/trust and server-certificate DN verification. Schema names and runtime account identifiers are editable non-secret fields. Initialize requires a locally provided schema artifact manifest; preparation verifies file integrity/provenance metadata but does not execute scripts or establish vendor origin from a self-recorded hash.

Certificates use `import` or `generate`. Import paths can name separate external and internal cert/key/chain/trust material. Private keys are used only for protected Java keystore preparation. Generation reuses one protected per-installation CA and exports only public trust/certificates. Output includes PKCS#12/JKS application trust stores and public certs; Java runtime interoperability remains a qualification check when `keytool` is unavailable.

The embedded candidate profile is JSON schema version 1 and is pinned independently at `0.1.0-candidate.1`. It records product/JDK/Oracle candidates, JDBC candidate notes, component replica/resource starting points, NodePorts, and all four source-to-destination image mappings. These settings are review data only; runtime compatibility and deployable manifests remain unqualified.

## Offline bundle schema version 1

The operator supplies a directory containing `bundle.json` and each listed archive file. The verifier uses strict JSON decoding and requires at least one explicitly mapped image. It allows a partial bundle so registry-only images do not need local copies.

```json
{
  "schema_version": 1,
  "bundle_id": "release-2026-11",
  "files": [
    {"path": "images/apim.tar", "size": 123456, "sha256": "<64 lowercase hex characters>"}
  ],
  "images": [
    {
      "component": "API Control Plane and resident Key Manager",
      "source_reference": "docker.io/wso2/wso2am-acp:4.7.0",
      "destination_repository": "apim-acp-oracle",
      "destination_tag": "4.7.0-r1",
      "archive_file": "images/apim.tar",
      "format": "docker-save"
    }
  ]
}
```

Each image entry must exactly match a component/source/destination mapping in the embedded compatibility profile. `archive_file` must point to exactly one file entry, and no listed file may be unused. Paths are normalized relative POSIX paths; absolute paths, parent traversal, Windows separators, symlinks, hardlinks, special tar members, and duplicate entries are rejected. Archive file size and SHA-256 must match the record.

Supported `format` values:

- `docker-save`: a tar archive produced by Docker image save with a `manifest.json` entry. Every referenced image config and layer is checked; an explicit source or destination `RepoTag` must match. Multi-platform entries sharing a tag are supported.
- `oci-layout`: an OCI image-layout tar archive with `oci-layout`, `index.json`, and `blobs/sha256/...`. Tagged descriptors, nested platform indexes, manifests, configs, and layers are validated against descriptor size/SHA-256. Config rootfs DiffIDs are checked against each layer's uncompressed content hash.

The verifier hashes layer blobs and calculates DiffIDs without extracting container root filesystems. Gzip and uncompressed layers are supported; zstd-compressed layers and unsupported archive structures fail explicitly. Resource limits are 128 GiB per listed/expanded archive, 100,000 tar entries, 16 MiB per captured metadata file, and 64 MiB aggregate captured metadata. Context cancellation is checked during hashing and scanning.

Digest fields are deliberately separate: `archive_sha256` identifies the whole tar file; image config digest identifies the config JSON; OCI manifest/index digests come only from OCI descriptors; layer blob digests identify stored compressed/raw blobs; DiffIDs identify uncompressed layer bytes. Docker save has no OCI manifest/index digest and instead reports a separate SHA-256 for its `manifest.json` metadata. No destination registry digest is inferred for an image that has not been pushed.

Platform evidence comes from image configs (and is cross-checked against OCI descriptor platform metadata). Each selected worker's actual OS/architecture must be covered. Missing or incompatible platforms remain blockers; an amd64 image is never substituted silently for another architecture. A verified local archive only becomes `READY TO IMPORT` when the destination registry check is missing/not visible and every selected worker platform is covered.
