package schemaartifacts

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const ManifestVersion = 1
const MaxEntries = 100000
const MaxScriptSize int64 = 64 << 20
const MaxSourceSize int64 = 128 << 30

type Manifest struct {
	SchemaVersion  int      `json:"schema_version"`
	Product        string   `json:"product"`
	ProductVersion string   `json:"product_version"`
	SourceArchive  string   `json:"source_archive"`
	SourceSHA256   string   `json:"source_sha256"`
	SourceSize     int64    `json:"source_size"`
	SourceIdentity string   `json:"source_identity"`
	Scripts        []Script `json:"scripts"`
}
type Script struct {
	Path               string   `json:"path"`
	SHA256             string   `json:"sha256"`
	TargetSchema       string   `json:"target_schema"`
	Order              int      `json:"order"`
	RequiredPrivileges []string `json:"required_privileges"`
	Prerequisites      []string `json:"prerequisites,omitempty"`
}
type ScriptResult struct {
	Path               string   `json:"path"`
	TargetSchema       string   `json:"target_schema"`
	SHA256             string   `json:"sha256"`
	Size               int64    `json:"size"`
	Order              int      `json:"order"`
	RequiredPrivileges []string `json:"required_privileges"`
}
type Result struct {
	Status         string         `json:"status"`
	Product        string         `json:"product"`
	Version        string         `json:"version"`
	SourceIdentity string         `json:"source_identity"`
	SourceSHA256   string         `json:"source_sha256"`
	Scripts        []ScriptResult `json:"scripts,omitempty"`
	Deferred       []string       `json:"deferred,omitempty"`
	Blockers       []string       `json:"blockers,omitempty"`
}

func Load(path string) (Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, errors.New("cannot open schema artifact manifest")
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 1<<20))
	dec.DisallowUnknownFields()
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, errors.New("schema artifact manifest is invalid")
	}
	var x any
	if dec.Decode(&x) != io.EOF {
		return Manifest{}, errors.New("schema artifact manifest must contain one JSON object")
	}
	if err := validateManifest(m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func Verify(ctx context.Context, manifestPath string, expectedProductVersion string) (Result, error) {
	m, err := Load(manifestPath)
	if err != nil {
		return Result{Status: "BLOCKED", Blockers: []string{err.Error()}}, err
	}
	if m.ProductVersion != expectedProductVersion {
		return Result{Status: "BLOCKED", Blockers: []string{"schema artifact product version does not match the pinned profile"}}, errors.New("schema artifact product version does not match pinned profile")
	}
	base := filepath.Dir(manifestPath)
	source := filepath.Join(base, filepath.FromSlash(m.SourceArchive))
	if !within(base, source) {
		return Result{Status: "BLOCKED"}, errors.New("schema source archive path escapes manifest directory")
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		return Result{Status: "BLOCKED", Blockers: []string{"recorded WSO2 distribution artifact is unavailable"}}, errors.New("recorded WSO2 distribution artifact is unavailable")
	}
	if info.Size() != m.SourceSize || info.Size() > MaxSourceSize {
		return Result{Status: "BLOCKED"}, errors.New("WSO2 distribution size does not match manifest or resource bound")
	}
	sourceHash, err := hashFile(ctx, source, MaxSourceSize)
	if err != nil {
		return Result{Status: "BLOCKED"}, err
	}
	if sourceHash != m.SourceSHA256 {
		return Result{Status: "BLOCKED"}, errors.New("WSO2 distribution SHA-256 does not match manifest")
	}
	lookup, err := openArtifact(source)
	if err != nil {
		return Result{Status: "BLOCKED"}, err
	}
	defer lookup.Close()
	result := Result{Status: "INTEGRITY VERIFIED; SOURCE PROVENANCE UNVERIFIED", Product: m.Product, Version: m.ProductVersion, SourceIdentity: m.SourceIdentity, SourceSHA256: sourceHash, Deferred: []string{"The observed local file hash does not authenticate vendor origin/signature. Compare with an independently verified vendor release checksum before using these scripts.", "Oracle DDL is not executed by this verifier."}}
	for _, script := range m.Scripts {
		if err := ctx.Err(); err != nil {
			return Result{Status: "CANCELED"}, err
		}
		data, size, err := lookup.Read(script.Path)
		if err != nil {
			return Result{Status: "BLOCKED", Blockers: []string{"referenced schema script is missing, unsafe, or outside resource limits"}}, fmt.Errorf("schema script reference cannot be verified: %s", script.Path)
		}
		sum := sha256.Sum256(data)
		digest := hex.EncodeToString(sum[:])
		if digest != script.SHA256 {
			return Result{Status: "BLOCKED", Blockers: []string{"referenced schema script hash does not match manifest"}}, fmt.Errorf("schema script hash mismatch: %s", script.Path)
		}
		result.Scripts = append(result.Scripts, ScriptResult{Path: script.Path, TargetSchema: script.TargetSchema, SHA256: digest, Size: size, Order: script.Order, RequiredPrivileges: append([]string(nil), script.RequiredPrivileges...)})
	}
	sort.Slice(result.Scripts, func(i, j int) bool { return result.Scripts[i].Order < result.Scripts[j].Order })
	return result, nil
}

func validateManifest(m Manifest) error {
	if m.SchemaVersion != ManifestVersion {
		return fmt.Errorf("unsupported schema artifact manifest version %d", m.SchemaVersion)
	}
	if m.Product != "wso2-apim" || m.ProductVersion == "" || m.SourceIdentity == "" {
		return errors.New("schema artifact manifest must identify WSO2 API Manager product, version, and source filename")
	}
	if !digestPattern.MatchString(m.SourceSHA256) || m.SourceSize < 1 || m.SourceSize > MaxSourceSize {
		return errors.New("schema source size or SHA-256 is invalid")
	}
	if !safePath(m.SourceArchive) {
		return errors.New("schema source archive path is unsafe")
	}
	if len(m.Scripts) == 0 {
		return errors.New("schema artifact manifest has no SQL scripts")
	}
	seen := map[string]bool{}
	orders := map[int]bool{}
	for _, s := range m.Scripts {
		if !safePath(s.Path) || !strings.HasSuffix(strings.ToLower(s.Path), ".sql") || !digestPattern.MatchString(s.SHA256) || !schemaName.MatchString(s.TargetSchema) || s.Order < 1 || seen[s.Path] || orders[s.Order] {
			return errors.New("schema script path, digest, target schema, or order is invalid")
		}
		seen[s.Path] = true
		orders[s.Order] = true
	}
	return nil
}

type artifact interface {
	Read(string) ([]byte, int64, error)
	Close() error
}
type zipArtifact struct {
	r     *zip.ReadCloser
	files map[string]*zip.File
}

func openArtifact(path string) (artifact, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".zip" {
		r, err := zip.OpenReader(path)
		if err != nil {
			return nil, errors.New("WSO2 distribution ZIP is unreadable")
		}
		if len(r.File) > MaxEntries {
			r.Close()
			return nil, errors.New("WSO2 distribution ZIP exceeds entry limit")
		}
		a := &zipArtifact{r: r, files: map[string]*zip.File{}}
		for _, f := range r.File {
			if !safePath(f.Name) || f.Mode()&os.ModeSymlink != 0 {
				r.Close()
				return nil, errors.New("WSO2 distribution ZIP contains an unsafe path or link")
			}
			name := strings.TrimSuffix(f.Name, "/")
			if name == "" {
				continue
			}
			if _, ok := a.files[name]; ok {
				r.Close()
				return nil, errors.New("WSO2 distribution ZIP contains duplicate paths")
			}
			if !f.FileInfo().IsDir() {
				a.files[name] = f
			}
		}
		return a, nil
	}
	if ext == ".sql" || ext == "" {
		return &directoryArtifact{root: filepath.Dir(path)}, nil
	}
	return nil, errors.New("unsupported WSO2 distribution format; supply a directory or ZIP")
}
func (a *zipArtifact) Read(name string) ([]byte, int64, error) {
	f, ok := a.files[name]
	if !ok {
		return nil, 0, os.ErrNotExist
	}
	if f.UncompressedSize64 > uint64(MaxScriptSize) {
		return nil, 0, errors.New("SQL script exceeds size limit")
	}
	r, err := f.Open()
	if err != nil {
		return nil, 0, err
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, MaxScriptSize+1))
	if err != nil || int64(len(b)) > MaxScriptSize {
		return nil, 0, errors.New("SQL script exceeds size limit")
	}
	return b, int64(len(b)), nil
}
func (a *zipArtifact) Close() error { return a.r.Close() }

type directoryArtifact struct{ root string }

func (a *directoryArtifact) Read(name string) ([]byte, int64, error) {
	if !safePath(name) {
		return nil, 0, errors.New("unsafe script path")
	}
	path := filepath.Join(a.root, filepath.FromSlash(name))
	if !within(a.root, path) {
		return nil, 0, errors.New("script path escapes root")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > MaxScriptSize {
		return nil, 0, errors.New("SQL script is missing, linked, or oversized")
	}
	b, err := os.ReadFile(path)
	return b, int64(len(b)), err
}
func (a *directoryArtifact) Close() error { return nil }
func hashFile(ctx context.Context, path string, max int64) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("cannot read source artifact")
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 128*1024)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, e := f.Read(buf)
		total += int64(n)
		if total > max {
			return "", errors.New("source artifact exceeds size limit")
		}
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", errors.New("source artifact read failed")
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func safePath(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && !strings.Contains(p, "\\") && !strings.Contains(p, ":") && !filepath.IsAbs(p) && filepath.Clean(filepath.FromSlash(p)) != ".." && !strings.HasPrefix(filepath.Clean(filepath.FromSlash(p)), ".."+string(filepath.Separator))
}
func within(base, path string) bool {
	b, e := filepath.Abs(base)
	if e != nil {
		return false
	}
	p, e := filepath.Abs(path)
	if e != nil {
		return false
	}
	rel, e := filepath.Rel(b, p)
	return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var schemaName = regexp.MustCompile(`^[A-Z][A-Z0-9_$#]{0,29}$`)
