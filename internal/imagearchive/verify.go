// Package imagearchive validates offline image bundles without extracting image
// layers into a filesystem or unpacking a container root filesystem.
package imagearchive

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/registry"
)

const (
	SchemaVersion      = 1
	MaxBundleManifest  = 4 << 20
	MaxMetadataFile    = 16 << 20
	MaxMetadataTotal   = 64 << 20
	MaxArchiveEntries  = 100_000
	MaxExpandedArchive = int64(128) << 30
	MaxBundleFile      = int64(128) << 30
)

type BundleManifest struct {
	SchemaVersion int           `json:"schema_version"`
	BundleID      string        `json:"bundle_id"`
	Files         []FileRecord  `json:"files"`
	Images        []ImageRecord `json:"images"`
}
type FileRecord struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type ImageRecord struct {
	Component             string `json:"component"`
	SourceReference       string `json:"source_reference"`
	DestinationRepository string `json:"destination_repository"`
	DestinationTag        string `json:"destination_tag"`
	ArchiveFile           string `json:"archive_file"`
	Format                string `json:"format"`
}

type DigestSet struct {
	ArchiveSHA256               string
	ImageConfigDigest           string
	OCIManifestDigest           string
	OCIIndexDigest              string
	LayerBlobDigests            []string
	UncompressedLayerDiffIDs    []string
	DockerArchiveManifestSHA256 string
}
type Platform struct{ OS, Architecture, Variant string }
type ImageEvidence struct {
	Component, SourceReference, DestinationReference string
	ArchiveFile, Format, Integrity, Detail           string
	ArchiveBytes                                     int64
	Digests                                          DigestSet
	Platforms                                        []Platform
	CompatiblePlatforms                              []Platform
	MissingPlatforms                                 []Platform
}
type FileEvidence struct {
	Path, Integrity, SHA256 string
	Size                    int64
}
type Result struct {
	BundleID string
	Files    []FileEvidence
	Images   []ImageEvidence
}

var sha256Hex = regexp.MustCompile(`^[a-f0-9]{64}$`)

// Verify validates the manifest, listed file size/hash, and each explicitly
// mapped image archive. It accepts OCI image-layout tar archives and Docker
// image-save tar archives. Missing profile mappings are allowed so partial
// bundles can supplement an already populated registry.
func Verify(ctx context.Context, bundleDir string, required []registry.RequiredImage) (Result, error) {
	root, err := filepath.Abs(bundleDir)
	if err != nil {
		return Result{}, fmt.Errorf("resolve bundle path: %w", err)
	}
	st, err := os.Stat(root)
	if err != nil {
		return Result{}, fmt.Errorf("bundle directory: %w", err)
	}
	if !st.IsDir() {
		return Result{}, errors.New("bundle path must be a directory containing bundle.json")
	}
	manifestPath := filepath.Join(root, "bundle.json")
	if err := ensureWithin(root, manifestPath); err != nil {
		return Result{}, fmt.Errorf("bundle manifest: %w", err)
	}
	data, err := readBounded(manifestPath, MaxBundleManifest)
	if err != nil {
		return Result{}, fmt.Errorf("read bundle.json: %w", err)
	}
	var m BundleManifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Result{}, fmt.Errorf("decode bundle.json: %w", err)
	}
	if dec.Decode(new(any)) != io.EOF {
		return Result{}, errors.New("bundle.json must contain exactly one JSON value")
	}
	if m.SchemaVersion != SchemaVersion {
		return Result{}, fmt.Errorf("unsupported bundle schema version %d", m.SchemaVersion)
	}
	if strings.TrimSpace(m.BundleID) == "" || len(m.Files) == 0 || len(m.Images) == 0 {
		return Result{}, errors.New("bundle.json requires bundle_id, files, and images")
	}

	req := make(map[string]registry.RequiredImage, len(required))
	for _, r := range required {
		req[r.Component] = r
	}
	files := make(map[string]FileRecord, len(m.Files))
	result := Result{BundleID: m.BundleID}
	for _, f := range m.Files {
		if err := validateBundlePath(f.Path); err != nil {
			return Result{}, fmt.Errorf("unsafe bundle file path: %w", err)
		}
		if f.Size < 0 || f.Size > MaxBundleFile || !sha256Hex.MatchString(f.SHA256) {
			return Result{}, fmt.Errorf("invalid size or lowercase SHA-256 for bundle file %q", f.Path)
		}
		if _, ok := files[f.Path]; ok {
			return Result{}, fmt.Errorf("duplicate bundle file record %q", f.Path)
		}
		files[f.Path] = f
	}
	seenComponents := map[string]bool{}
	seenFiles := map[string]bool{}
	for _, ir := range m.Images {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		r, ok := req[ir.Component]
		if !ok {
			return result, fmt.Errorf("bundle maps unknown image component %q", ir.Component)
		}
		if seenComponents[ir.Component] {
			return result, fmt.Errorf("duplicate image mapping for component %q", ir.Component)
		}
		seenComponents[ir.Component] = true
		if ir.SourceReference != r.SourceReference || ir.DestinationRepository != r.Repository || ir.DestinationTag != r.Tag {
			return result, fmt.Errorf("image mapping for %q does not match the pinned profile", ir.Component)
		}
		if err := validateBundlePath(ir.ArchiveFile); err != nil {
			return result, fmt.Errorf("unsafe archive path for %q: %w", ir.Component, err)
		}
		fr, ok := files[ir.ArchiveFile]
		if !ok {
			return result, fmt.Errorf("archive %q has no size/SHA-256 record", ir.ArchiveFile)
		}
		if seenFiles[ir.ArchiveFile] {
			return result, fmt.Errorf("archive file %q is mapped more than once", ir.ArchiveFile)
		}
		seenFiles[ir.ArchiveFile] = true
		if ir.Format != "oci-layout" && ir.Format != "docker-save" {
			return result, fmt.Errorf("unsupported image archive format %q", ir.Format)
		}
		archivePath := filepath.Join(root, filepath.FromSlash(ir.ArchiveFile))
		if err := ensureWithin(root, archivePath); err != nil {
			return result, err
		}
		file, err := os.Open(archivePath)
		if err != nil {
			return result, fmt.Errorf("open bundle archive %q: %w", ir.ArchiveFile, err)
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return result, err
		}
		if !info.Mode().IsRegular() {
			file.Close()
			return result, fmt.Errorf("bundle archive %q is not a regular file", ir.ArchiveFile)
		}
		if info.Size() != fr.Size {
			file.Close()
			return result, fmt.Errorf("bundle archive %q size mismatch: expected %d, observed %d", ir.ArchiveFile, fr.Size, info.Size())
		}
		if info.Size() > MaxBundleFile {
			file.Close()
			return result, fmt.Errorf("bundle archive %q exceeds the configured size limit", ir.ArchiveFile)
		}
		archiveHash := sha256.New()
		if _, err := io.Copy(archiveHash, contextReader{ctx: ctx, r: file}); err != nil {
			file.Close()
			return result, fmt.Errorf("hash bundle archive %q: %w", ir.ArchiveFile, err)
		}
		if err := file.Close(); err != nil {
			return result, err
		}
		observedHash := hex.EncodeToString(archiveHash.Sum(nil))
		if observedHash != fr.SHA256 {
			return result, fmt.Errorf("bundle archive %q SHA-256 mismatch", ir.ArchiveFile)
		}
		file, err = os.Open(archivePath)
		if err != nil {
			return result, err
		}
		scan, err := inspectTar(ctx, file)
		closeErr := file.Close()
		if err != nil {
			return result, fmt.Errorf("inspect %s archive %q: %w", ir.Format, ir.ArchiveFile, err)
		}
		if closeErr != nil {
			return result, closeErr
		}
		expectedRef := ir.DestinationRepository + ":" + ir.DestinationTag
		evidence, err := scan.match(ir.Format, ir.Component, ir.SourceReference, expectedRef)
		if err != nil {
			return result, fmt.Errorf("image mapping %q: %w", ir.Component, err)
		}
		evidence.ArchiveFile, evidence.ArchiveBytes, evidence.Digests.ArchiveSHA256 = ir.ArchiveFile, fr.Size, observedHash
		evidence.Integrity = "VERIFIED"
		result.Images = append(result.Images, evidence)
		result.Files = append(result.Files, FileEvidence{Path: fr.Path, Size: fr.Size, SHA256: observedHash, Integrity: "VERIFIED"})
	}
	for name := range files {
		if !seenFiles[name] {
			return result, fmt.Errorf("bundle file %q is not referenced by an image mapping", name)
		}
	}
	return result, nil
}

func validateBundlePath(p string) error {
	if p == "" || strings.ContainsAny(p, `\:`) || strings.HasPrefix(p, "/") || path.IsAbs(p) || path.Clean(p) != p {
		return fmt.Errorf("%q must be a normalized relative POSIX path", p)
	}
	for _, segment := range strings.Split(p, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("%q contains an unsafe path segment", p)
		}
	}
	return nil
}

func ensureWithin(root, target string) error {
	rootEval, err := filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	targetEval, err := filepath.EvalSymlinks(target)
	if err != nil {
		return fmt.Errorf("resolve bundle archive: %w", err)
	}
	rel, err := filepath.Rel(rootEval, targetEval)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("bundle archive resolves outside the bundle directory")
	}
	return nil
}

func readBounded(p string, max int64) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > max {
		return nil, errors.New("metadata file is not regular or exceeds size limit")
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err == nil && int64(len(b)) > max {
		err = errors.New("metadata file exceeds size limit")
	}
	return b, err
}

type entry struct {
	size   int64
	digest string
	diffID string
	data   []byte
}
type archiveScan struct {
	entries       map[string]entry
	totalMetadata int64
	totalExpanded int64
	fileNames     map[string]bool
}
type limitReader struct {
	r    io.Reader
	left int64
}

func (r *limitReader) Read(p []byte) (int, error) {
	if r.left < 0 {
		return 0, errors.New("archive exceeds expanded-size limit")
	}
	if int64(len(p)) > r.left+1 {
		p = p[:r.left+1]
	}
	n, err := r.r.Read(p)
	r.left -= int64(n)
	if r.left < 0 {
		return n, errors.New("archive exceeds expanded-size limit")
	}
	return n, err
}

func inspectTar(ctx context.Context, file *os.File) (*archiveScan, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	br := bufio.NewReader(file)
	var src io.Reader = br
	magic, _ := br.Peek(2)
	if len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("invalid gzip archive: %w", err)
		}
		defer gz.Close()
		src = gz
	}
	limited := &limitReader{r: contextReader{ctx: ctx, r: src}, left: MaxExpandedArchive}
	tr := tar.NewReader(limited)
	scan := &archiveScan{entries: map[string]entry{}, fileNames: map[string]bool{}}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid tar structure: %w", err)
		}
		if len(scan.fileNames) >= MaxArchiveEntries {
			return nil, errors.New("archive exceeds entry-count limit")
		}
		name := h.Name
		if h.Typeflag == tar.TypeDir {
			name = strings.TrimSuffix(name, "/")
		}
		if err := validateBundlePath(name); err != nil {
			return nil, fmt.Errorf("unsafe tar entry path: %w", err)
		}
		if scan.fileNames[name] {
			return nil, fmt.Errorf("duplicate tar entry %q", name)
		}
		scan.fileNames[name] = true
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			return nil, fmt.Errorf("unsupported tar entry type %d for %q", h.Typeflag, name)
		}
		if h.Size < 0 || h.Size > MaxBundleFile {
			return nil, fmt.Errorf("invalid or oversized tar entry %q", name)
		}
		capture := isMetadataPath(name) && h.Size <= MaxMetadataFile
		var buf bytes.Buffer
		var capW io.Writer = io.Discard
		if capture {
			capW = &buf
		}
		rawHash := sha256.New()
		tee := io.TeeReader(io.TeeReader(tr, rawHash), capW)
		var diff string
		if strings.HasPrefix(name, "blobs/sha256/") || strings.HasSuffix(name, "/layer.tar") || name == "layer.tar" {
			diff, err = diffIDForLayer(tee, h.Size)
		} else {
			_, err = io.Copy(io.Discard, tee)
		}
		if err != nil {
			return nil, fmt.Errorf("read tar entry %q: %w", name, err)
		}
		if int64(buf.Len()) > MaxMetadataFile {
			return nil, fmt.Errorf("metadata file %q exceeds size limit", name)
		}
		scan.totalExpanded += h.Size
		if scan.totalExpanded > MaxExpandedArchive {
			return nil, errors.New("archive exceeds expanded-size limit")
		}
		scan.totalMetadata += int64(buf.Len())
		if scan.totalMetadata > MaxMetadataTotal {
			return nil, errors.New("archive metadata exceeds aggregate size limit")
		}
		e := entry{size: h.Size, digest: "sha256:" + hex.EncodeToString(rawHash.Sum(nil)), diffID: diff}
		if capture {
			e.data = append([]byte(nil), buf.Bytes()...)
		}
		scan.entries[name] = e
	}
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return nil, fmt.Errorf("read archive trailer: %w", err)
	}
	return scan, nil
}

func isMetadataPath(name string) bool {
	return name == "index.json" || name == "oci-layout" || name == "manifest.json" || strings.HasSuffix(name, ".json") || strings.HasPrefix(name, "blobs/sha256/")
}

func diffIDForLayer(r io.Reader, size int64) (string, error) {
	br := bufio.NewReader(io.LimitReader(r, size))
	magic, _ := br.Peek(4)
	if len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return "", fmt.Errorf("invalid gzip-compressed layer: %w", err)
		}
		h := sha256.New()
		_, copyErr := io.Copy(h, gz)
		closeErr := gz.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if _, err := io.Copy(io.Discard, br); err != nil {
			return "", err
		}
		return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
	}
	if len(magic) == 4 && bytes.Equal(magic, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		return "", errors.New("zstd-compressed OCI layers are not supported by this bundle verifier")
	}
	h := sha256.New()
	if _, err := io.Copy(h, br); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

type ociDescriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	Annotations map[string]string `json:"annotations"`
	Platform    *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
	} `json:"platform"`
}
type ociIndex struct {
	SchemaVersion int             `json:"schemaVersion"`
	Manifests     []ociDescriptor `json:"manifests"`
}
type ociManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	Config        ociDescriptor   `json:"config"`
	Layers        []ociDescriptor `json:"layers"`
}
type imageConfig struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant"`
	RootFS       struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
}

func (s *archiveScan) match(format, component, source, dest string) (ImageEvidence, error) {
	switch format {
	case "oci-layout":
		return s.matchOCI(component, source, dest)
	case "docker-save":
		return s.matchDocker(component, source, dest)
	default:
		return ImageEvidence{}, fmt.Errorf("unsupported format %q", format)
	}
}

func (s *archiveScan) matchOCI(component, source, dest string) (ImageEvidence, error) {
	layout, ok := s.entries["oci-layout"]
	if !ok || !json.Valid(layout.data) {
		return ImageEvidence{}, errors.New("oci-layout marker is missing or invalid")
	}
	var marker struct {
		ImageLayoutVersion string `json:"imageLayoutVersion"`
	}
	if err := json.Unmarshal(layout.data, &marker); err != nil || marker.ImageLayoutVersion != "1.0.0" {
		return ImageEvidence{}, errors.New("unsupported OCI image-layout version")
	}
	root, ok := s.entries["index.json"]
	if !ok {
		return ImageEvidence{}, errors.New("OCI layout index.json is missing")
	}
	var idx ociIndex
	if err := json.Unmarshal(root.data, &idx); err != nil {
		return ImageEvidence{}, fmt.Errorf("decode OCI layout index: %w", err)
	}
	if idx.SchemaVersion != 2 {
		return ImageEvidence{}, fmt.Errorf("unsupported OCI layout index schema %d", idx.SchemaVersion)
	}
	out := ImageEvidence{Component: component, SourceReference: source, DestinationReference: dest, Format: "oci-layout"}
	matched := false
	for _, d := range idx.Manifests {
		ref := d.Annotations["org.opencontainers.image.ref.name"]
		if ref != dest && ref != source {
			continue
		}
		matched = true
		if strings.Contains(d.MediaType, "image.index") || strings.Contains(d.MediaType, "manifest.list") {
			out.Digests.OCIIndexDigest = d.Digest
			platforms, manifests, configs, layers, diffIDs, err := s.walkIndex(d, 0)
			if err != nil {
				return out, err
			}
			out.Platforms = append(out.Platforms, platforms...)
			out.Digests.OCIManifestDigest = strings.Join(manifests, ",")
			out.Digests.ImageConfigDigest = strings.Join(configs, ",")
			out.Digests.LayerBlobDigests = append(out.Digests.LayerBlobDigests, layers...)
			out.Digests.UncompressedLayerDiffIDs = append(out.Digests.UncompressedLayerDiffIDs, diffIDs...)
		} else {
			p, manifest, config, layers, diffIDs, err := s.inspectManifest(d)
			if err != nil {
				return out, err
			}
			out.Platforms = append(out.Platforms, p)
			out.Digests.OCIManifestDigest = manifest
			out.Digests.ImageConfigDigest = config
			out.Digests.LayerBlobDigests = append(out.Digests.LayerBlobDigests, layers...)
			out.Digests.UncompressedLayerDiffIDs = append(out.Digests.UncompressedLayerDiffIDs, diffIDs...)
		}
	}
	if !matched {
		return out, fmt.Errorf("OCI index has no descriptor tagged %q or %q", dest, source)
	}
	out.Detail = "OCI descriptors, configs, layers, and DiffIDs verified without unpacking image filesystems"
	return out, nil
}

func (s *archiveScan) walkIndex(d ociDescriptor, depth int) ([]Platform, []string, []string, []string, []string, error) {
	if depth > 4 {
		return nil, nil, nil, nil, nil, errors.New("nested OCI indexes exceed supported depth")
	}
	b, err := s.descriptorBytes(d)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	var idx ociIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("decode OCI image index: %w", err)
	}
	var ps []Platform
	var ms, cs, ls, ds []string
	for _, child := range idx.Manifests {
		if strings.Contains(child.MediaType, "image.index") || strings.Contains(child.MediaType, "manifest.list") {
			p, m, c, l, d, e := s.walkIndex(child, depth+1)
			if e != nil {
				return nil, nil, nil, nil, nil, e
			}
			ps = append(ps, p...)
			ms = append(ms, m...)
			cs = append(cs, c...)
			ls = append(ls, l...)
			ds = append(ds, d...)
			continue
		}
		p, m, c, l, diff, e := s.inspectManifest(child)
		if e != nil {
			return nil, nil, nil, nil, nil, e
		}
		ps = append(ps, p)
		ms = append(ms, m)
		cs = append(cs, c)
		ls = append(ls, l...)
		ds = append(ds, diff...)
	}
	return ps, ms, cs, ls, ds, nil
}

func (s *archiveScan) inspectManifest(d ociDescriptor) (Platform, string, string, []string, []string, error) {
	b, err := s.descriptorBytes(d)
	if err != nil {
		return Platform{}, "", "", nil, nil, err
	}
	var m ociManifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Platform{}, "", "", nil, nil, fmt.Errorf("decode OCI image manifest: %w", err)
	}
	if m.SchemaVersion != 2 {
		return Platform{}, "", "", nil, nil, fmt.Errorf("unsupported image manifest schema %d", m.SchemaVersion)
	}
	cb, err := s.descriptorBytes(m.Config)
	if err != nil {
		return Platform{}, "", "", nil, nil, fmt.Errorf("image config: %w", err)
	}
	var cfg imageConfig
	if err := json.Unmarshal(cb, &cfg); err != nil {
		return Platform{}, "", "", nil, nil, fmt.Errorf("decode image config: %w", err)
	}
	if cfg.OS == "" || cfg.Architecture == "" {
		return Platform{}, "", "", nil, nil, errors.New("image config lacks OS or architecture")
	}
	if cfg.RootFS.Type != "layers" || len(cfg.RootFS.DiffIDs) != len(m.Layers) {
		return Platform{}, "", "", nil, nil, errors.New("image config rootfs.diff_ids do not match manifest layer count")
	}
	var layers, diffs []string
	for i, layer := range m.Layers {
		if _, err := s.descriptorBytes(layer); err != nil {
			return Platform{}, "", "", nil, nil, fmt.Errorf("layer %d: %w", i, err)
		}
		entry := s.entries[blobPath(layer.Digest)]
		if entry.diffID == "" || entry.diffID != cfg.RootFS.DiffIDs[i] {
			return Platform{}, "", "", nil, nil, fmt.Errorf("layer %d DiffID mismatch or unsupported compression", i)
		}
		layers = append(layers, layer.Digest)
		diffs = append(diffs, entry.diffID)
	}
	p := Platform{OS: cfg.OS, Architecture: cfg.Architecture, Variant: cfg.Variant}
	if d.Platform != nil && (d.Platform.OS != p.OS || d.Platform.Architecture != p.Architecture || (d.Platform.Variant != "" && d.Platform.Variant != p.Variant)) {
		return Platform{}, "", "", nil, nil, errors.New("index platform descriptor disagrees with referenced image configuration")
	}
	return p, d.Digest, m.Config.Digest, layers, diffs, nil
}

func (s *archiveScan) descriptorBytes(d ociDescriptor) ([]byte, error) {
	if !strings.HasPrefix(d.Digest, "sha256:") || !sha256Hex.MatchString(strings.TrimPrefix(d.Digest, "sha256:")) || d.Size < 0 {
		return nil, errors.New("unsupported or malformed OCI descriptor")
	}
	e, ok := s.entries[blobPath(d.Digest)]
	if !ok {
		return nil, fmt.Errorf("referenced blob %s is missing", d.Digest)
	}
	if e.digest != d.Digest || e.size != d.Size {
		return nil, fmt.Errorf("referenced blob %s digest or size mismatch", d.Digest)
	}
	if e.data == nil {
		return nil, fmt.Errorf("referenced metadata blob %s exceeds supported metadata size", d.Digest)
	}
	return e.data, nil
}

func blobPath(d string) string { return "blobs/sha256/" + strings.TrimPrefix(d, "sha256:") }

type dockerImage struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}
type dockerConfig struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant"`
	RootFS       struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
}

func (s *archiveScan) matchDocker(component, source, dest string) (ImageEvidence, error) {
	meta, ok := s.entries["manifest.json"]
	if !ok {
		return ImageEvidence{}, errors.New("Docker image-save manifest.json is missing")
	}
	var list []dockerImage
	if err := json.Unmarshal(meta.data, &list); err != nil {
		return ImageEvidence{}, fmt.Errorf("decode Docker archive manifest: %w", err)
	}
	out := ImageEvidence{Component: component, SourceReference: source, DestinationReference: dest, Format: "docker-save"}
	found := false
	var configDigests []string
	for _, im := range list {
		match := false
		for _, tag := range im.RepoTags {
			if tag == dest || tag == source {
				match = true
			}
		}
		if !match {
			continue
		}
		found = true
		if err := validateBundlePath(im.Config); err != nil {
			return out, fmt.Errorf("unsafe Docker config path: %w", err)
		}
		ce, ok := s.entries[im.Config]
		if !ok || ce.data == nil {
			return out, fmt.Errorf("Docker image config %q is missing or too large", im.Config)
		}
		var cfg dockerConfig
		if err := json.Unmarshal(ce.data, &cfg); err != nil {
			return out, fmt.Errorf("decode Docker image config: %w", err)
		}
		if cfg.OS == "" || cfg.Architecture == "" || cfg.RootFS.Type != "layers" || len(cfg.RootFS.DiffIDs) != len(im.Layers) {
			return out, errors.New("Docker config has invalid platform or rootfs.diff_ids")
		}
		configDigests = append(configDigests, ce.digest)
		out.Digests.DockerArchiveManifestSHA256 = meta.digest
		out.Platforms = append(out.Platforms, Platform{OS: cfg.OS, Architecture: cfg.Architecture, Variant: cfg.Variant})
		for i, lp := range im.Layers {
			if err := validateBundlePath(lp); err != nil {
				return out, fmt.Errorf("unsafe Docker layer path: %w", err)
			}
			le, ok := s.entries[lp]
			if !ok {
				return out, fmt.Errorf("Docker layer %q is missing", lp)
			}
			if le.diffID == "" || le.diffID != cfg.RootFS.DiffIDs[i] {
				return out, fmt.Errorf("Docker layer %d DiffID mismatch", i)
			}
			out.Digests.LayerBlobDigests = append(out.Digests.LayerBlobDigests, le.digest)
			out.Digests.UncompressedLayerDiffIDs = append(out.Digests.UncompressedLayerDiffIDs, le.diffID)
		}
	}
	if !found {
		return out, fmt.Errorf("Docker archive does not contain explicit tag %q or %q", dest, source)
	}
	out.Digests.ImageConfigDigest = strings.Join(configDigests, ",")
	out.Detail = "Docker save config and every referenced layer verified; this format has no OCI manifest or index digest"
	return out, nil
}

func compatible(p Platform, required registry.Platform) bool {
	return p.OS == required.OS && p.Architecture == required.Architecture && (required.Variant == "" || p.Variant == required.Variant)
}

// CheckPlatforms marks coverage for the selected worker OS/architecture list.
func (e *ImageEvidence) CheckPlatforms(required []registry.Platform) {
	e.CompatiblePlatforms = nil
	e.MissingPlatforms = nil
	for _, rp := range required {
		found := false
		for _, p := range e.Platforms {
			if compatible(p, rp) {
				found = true
				break
			}
		}
		if found {
			e.CompatiblePlatforms = append(e.CompatiblePlatforms, Platform{OS: rp.OS, Architecture: rp.Architecture, Variant: rp.Variant})
		} else {
			e.MissingPlatforms = append(e.MissingPlatforms, Platform{OS: rp.OS, Architecture: rp.Architecture, Variant: rp.Variant})
		}
	}
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
