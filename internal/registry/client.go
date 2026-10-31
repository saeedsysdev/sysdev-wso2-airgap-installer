package registry

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Status string

const (
	Found            Status = "FOUND"
	Missing          Status = "MISSING/NOT VISIBLE"
	Unauthorized     Status = "UNAUTHORIZED"
	Forbidden        Status = "FORBIDDEN"
	Unreachable      Status = "UNREACHABLE"
	TLSFailure       Status = "TLS FAILURE"
	Unsupported      Status = "UNSUPPORTED MANIFEST"
	PlatformMismatch Status = "PLATFORM MISMATCH"
	DigestMismatch   Status = "DIGEST MISMATCH"
	Unknown          Status = "UNKNOWN"
)

type Platform struct{ OS, Architecture, Variant string }

type Image struct {
	Component, Reference string
	SourceReference      string
	Status               Status
	Digest, MediaType    string
	Detail               string
	Platforms            []PlatformEvidence
	PlatformStatus       string
	ConfigDigest         string
	IndexDigest          string
	LayerDigests         []string
}
type PlatformEvidence struct {
	Platform                     Platform
	Status                       string
	ManifestDigest, ConfigDigest string
	LayerDigests                 []string
	Detail                       string
}
type Credentials struct {
	Username, Password, Token string
	CABundlePath              string
}
type Client struct{ HTTP *http.Client }

var hostPattern = regexp.MustCompile(`^(?:[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?)(?::[0-9]{1,5})?$`)
var repoPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)

func NewClient() *Client { return &Client{HTTP: &http.Client{Timeout: 12 * time.Second}} }
func ValidateEndpoint(host, project string) error {
	if !hostPattern.MatchString(host) || strings.Contains(host, "..") {
		return fmt.Errorf("enter a registry host name with an optional port; do not include a scheme or path")
	}
	if i := strings.LastIndex(host, ":"); i >= 0 {
		p := host[i+1:]
		if n, e := strconv.Atoi(p); e == nil && (n < 1 || n > 65535) {
			return fmt.Errorf("registry port must be between 1 and 65535")
		}
	}
	if project != "" && !repoPattern.MatchString(project) {
		return fmt.Errorf("project/repository prefix must use lowercase OCI path segments")
	}
	return nil
}

func (c *Client) Check(ctx context.Context, host, project string, credentials Credentials, required []RequiredImage) ([]Image, error) {
	return c.CheckForPlatforms(ctx, host, project, credentials, required, nil)
}

func (c *Client) CheckForPlatforms(ctx context.Context, host, project string, credentials Credentials, required []RequiredImage, platforms []Platform) ([]Image, error) {
	if err := ValidateEndpoint(host, project); err != nil {
		return nil, err
	}
	client, err := c.client(credentials)
	if err != nil {
		return nil, err
	}
	for _, item := range required {
		if !repoPattern.MatchString(item.Repository) || item.Tag == "" {
			return nil, fmt.Errorf("invalid required image reference for %s", item.Component)
		}
	}
	results := make([]Image, len(required))
	jobs := make(chan int, len(required))
	for i := range required {
		jobs <- i
	}
	close(jobs)
	workers := min(4, len(required))
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = c.checkOne(ctx, client, host, project, credentials, required[i], platforms)
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return results, err
	}
	return results, nil
}

func (c *Client) checkOne(ctx context.Context, client *http.Client, host, project string, credentials Credentials, item RequiredImage, platforms []Platform) Image {
	repo := strings.TrimSuffix(project, "/")
	if repo != "" {
		repo += "/"
	}
	repo += item.Repository
	res := Image{Component: item.Component, Reference: host + "/" + repo + ":" + item.Tag, SourceReference: item.SourceReference, Status: Unknown}
	path := "https://" + host + "/v2/" + repo + "/manifests/" + url.PathEscape(item.Tag)
	accept := strings.Join([]string{"application/vnd.oci.image.index.v1+json", "application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json", "application/vnd.docker.distribution.manifest.v2+json"}, ", ")
	resp, e := c.get(ctx, client, path, repo, item.Tag, accept, credentials)
	if e != nil {
		if strings.Contains(strings.ToLower(e.Error()), "authentication required") || strings.Contains(strings.ToLower(e.Error()), "credentials were rejected") {
			res.Status = Unauthorized
		} else if strings.Contains(strings.ToLower(e.Error()), "certificate") || strings.Contains(strings.ToLower(e.Error()), "tls") {
			res.Status = TLSFailure
		} else {
			res.Status = Unreachable
		}
		res.Detail = e.Error()
		return res
	}
	if resp.StatusCode == http.StatusUnauthorized {
		challenge := resp.Header.Get("WWW-Authenticate")
		resp.Body.Close()
		if strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
			resp, e = c.bearerRequest(ctx, client, path, challenge, repo, item.Tag, credentials)
		} else if strings.HasPrefix(strings.ToLower(challenge), "basic ") && credentials.Username != "" {
			retry, _ := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
			retry.Header.Set("Accept", accept)
			retry.SetBasicAuth(credentials.Username, credentials.Password)
			resp, e = client.Do(retry)
		} else {
			e = fmt.Errorf("registry authentication required")
		}
		if e != nil {
			res.Status = classifyError(e)
			res.Detail = e.Error()
			return res
		}
	}
	body, e := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	resp.Body.Close()
	if e != nil {
		res.Detail = "cannot read manifest response"
		return res
	}
	switch resp.StatusCode {
	case http.StatusOK:
		media := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
		digest := resp.Header.Get("Docker-Content-Digest")
		sum := sha256.Sum256(body)
		observed := "sha256:" + hex.EncodeToString(sum[:])
		if digest != "" && digest != observed {
			res.Status = DigestMismatch
			res.Detail = "registry Docker-Content-Digest does not match the returned manifest bytes"
		} else if digest == "" {
			digest = observed
		}
		if !supported(media) {
			res.Status = Unsupported
			res.Detail = "registry returned an unrecognized manifest media type"
		} else if res.Status != DigestMismatch {
			res.Status = Found
			res.Detail = "exact manifest reference is readable"
		}
		res.Digest = digest
		res.MediaType = media
		if res.Status == Found && len(platforms) > 0 {
			res.PlatformStatus = "CHECKING"
			res.IndexDigest, res.ConfigDigest, res.LayerDigests, res.Platforms, res.PlatformStatus, res.Detail = c.verifyPlatforms(ctx, client, host, repo, item.Tag, body, media, digest, platforms, credentials)
		}
	case http.StatusNotFound:
		res.Status = Missing
		res.Detail = "registry reports the exact reference is unavailable"
	case http.StatusForbidden:
		res.Status = Forbidden
		res.Detail = "registry denied access; image existence is unknown"
	case http.StatusUnauthorized:
		res.Status = Unauthorized
		res.Detail = "registry authentication was rejected"
	default:
		res.Detail = fmt.Sprintf("registry returned HTTP %d", resp.StatusCode)
	}
	return res
}

func (c *Client) get(ctx context.Context, client *http.Client, target, repo, reference, accept string, cred Credentials) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if cred.Username != "" {
		req.SetBasicAuth(cred.Username, cred.Password)
	} else if cred.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cred.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	_ = resp.Body.Close()
	if strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return c.bearerRequest(ctx, client, target, challenge, repo, reference, cred)
	}
	if strings.HasPrefix(strings.ToLower(challenge), "basic ") && cred.Username != "" {
		retry, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if accept != "" {
			retry.Header.Set("Accept", accept)
		}
		retry.SetBasicAuth(cred.Username, cred.Password)
		return client.Do(retry)
	}
	return nil, errors.New("registry authentication required")
}

type registryDescriptor struct {
	MediaType string    `json:"mediaType"`
	Digest    string    `json:"digest"`
	Size      int64     `json:"size"`
	Platform  *Platform `json:"platform"`
}
type indexDocument struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Manifests     []registryDescriptor `json:"manifests"`
}
type manifestDocument struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Config        registryDescriptor   `json:"config"`
	Layers        []registryDescriptor `json:"layers"`
}
type platformConfig struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	Variant      string `json:"variant"`
}

func (c *Client) verifyPlatforms(ctx context.Context, client *http.Client, host, repo, tag string, top []byte, media, topDigest string, wanted []Platform, cred Credentials) (string, string, []string, []PlatformEvidence, string, string) {
	results := make([]PlatformEvidence, 0, len(wanted))
	layers, configs, manifests := []string{}, []string{}, []string{}
	indexDigest := ""
	if strings.Contains(media, "image.index") || strings.Contains(media, "manifest.list") {
		indexDigest = topDigest
	}
	allOK := true
	for _, p := range dedupePlatforms(wanted) {
		if err := ctx.Err(); err != nil {
			return indexDigest, strings.Join(configs, ","), layers, results, "UNKNOWN", "platform check canceled"
		}
		body, actualMedia := top, media
		if indexDigest != "" {
			var idx indexDocument
			if err := json.Unmarshal(body, &idx); err != nil {
				return indexDigest, strings.Join(configs, ","), layers, results, "UNKNOWN", "cannot decode registry image index"
			}
			desc, detail := findPlatformDescriptor(idx, p)
			if desc == nil {
				results = append(results, PlatformEvidence{Platform: p, Status: "MISSING", Detail: detail})
				allOK = false
				continue
			}
			var err error
			body, actualMedia, err = c.fetchDescriptor(ctx, client, host, repo, desc.Digest, desc.MediaType, desc.Size, cred)
			if err != nil {
				results = append(results, PlatformEvidence{Platform: p, Status: "UNKNOWN", Detail: err.Error()})
				allOK = false
				continue
			}
			manifests = append(manifests, desc.Digest)
		}
		if strings.Contains(actualMedia, "image.index") || strings.Contains(actualMedia, "manifest.list") {
			pe, nested, err := c.resolveNestedIndex(ctx, client, host, repo, body, p, 0, cred)
			if err != nil {
				pe = PlatformEvidence{Platform: p, Status: "UNKNOWN", Detail: err.Error()}
				allOK = false
			} else if pe.Status != "VERIFIED" {
				allOK = false
			}
			results = append(results, pe)
			if nested != "" {
				manifests = append(manifests, nested)
			}
			if pe.ConfigDigest != "" {
				configs = append(configs, pe.ConfigDigest)
			}
			layers = append(layers, pe.LayerDigests...)
			continue
		}
		pe, err := c.inspectRemoteManifest(ctx, client, host, repo, body, p, cred)
		if err != nil {
			pe = PlatformEvidence{Platform: p, Status: "UNKNOWN", Detail: err.Error()}
			allOK = false
		} else if pe.Status != "VERIFIED" {
			allOK = false
		}
		if pe.ManifestDigest != "" {
			manifests = append(manifests, pe.ManifestDigest)
		}
		if pe.ConfigDigest != "" {
			configs = append(configs, pe.ConfigDigest)
		}
		layers = append(layers, pe.LayerDigests...)
		results = append(results, pe)
	}
	status, detail := "VERIFIED", "selected worker platforms resolved to verified manifest/config digests"
	if !allOK {
		status, detail = "INCOMPLETE", "one or more selected worker platforms are absent or could not be verified"
	}
	return indexDigest, strings.Join(unique(configs), ","), unique(layers), results, status, detail
}

func (c *Client) resolveNestedIndex(ctx context.Context, client *http.Client, host, repo string, body []byte, wanted Platform, depth int, cred Credentials) (PlatformEvidence, string, error) {
	if depth > 3 {
		return PlatformEvidence{}, "", errors.New("registry index nesting exceeds supported depth")
	}
	var idx indexDocument
	if err := json.Unmarshal(body, &idx); err != nil {
		return PlatformEvidence{}, "", errors.New("cannot decode nested registry index")
	}
	desc, detail := findPlatformDescriptor(idx, wanted)
	if desc == nil {
		return PlatformEvidence{Platform: wanted, Status: "MISSING", Detail: detail}, "", nil
	}
	child, media, err := c.fetchDescriptor(ctx, client, host, repo, desc.Digest, desc.MediaType, desc.Size, cred)
	if err != nil {
		return PlatformEvidence{}, "", err
	}
	if strings.Contains(media, "image.index") || strings.Contains(media, "manifest.list") {
		return c.resolveNestedIndex(ctx, client, host, repo, child, wanted, depth+1, cred)
	}
	pe, err := c.inspectRemoteManifest(ctx, client, host, repo, child, wanted, cred)
	if err != nil {
		return pe, desc.Digest, err
	}
	pe.ManifestDigest = desc.Digest
	if pe.Status == "VERIFIED" && desc.Platform != nil && !matchesPlatform(*desc.Platform, wanted) {
		return pe, desc.Digest, errors.New("index platform descriptor does not match selected worker")
	}
	return pe, desc.Digest, nil
}

func findPlatformDescriptor(idx indexDocument, wanted Platform) (*registryDescriptor, string) {
	for i := range idx.Manifests {
		d := &idx.Manifests[i]
		if d.Platform != nil && matchesPlatform(*d.Platform, wanted) {
			return d, ""
		}
	}
	return nil, fmt.Sprintf("registry index has no %s/%s%s descriptor", wanted.OS, wanted.Architecture, func() string {
		if wanted.Variant != "" {
			return "/" + wanted.Variant
		}
		return ""
	}())
}

func (c *Client) fetchDescriptor(ctx context.Context, client *http.Client, host, repo, digest, media string, size int64, cred Credentials) ([]byte, string, error) {
	if !strings.HasPrefix(digest, "sha256:") || size < 0 {
		return nil, "", errors.New("registry returned an unsupported or invalid descriptor")
	}
	u := "https://" + host + "/v2/" + repo + "/manifests/" + url.PathEscape(digest)
	resp, err := c.get(ctx, client, u, repo, digest, "application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.docker.distribution.manifest.v2+json", cred)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", fmt.Errorf("registry returned HTTP %d while resolving platform manifest", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20+1))
	resp.Body.Close()
	if err != nil {
		return nil, "", errors.New("cannot read platform manifest")
	}
	if len(body) > 8<<20 {
		return nil, "", errors.New("platform manifest exceeds size limit")
	}
	if int64(len(body)) != size || digestBytes(body) != digest {
		return nil, "", errors.New("platform manifest digest or size mismatch")
	}
	actual := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if actual == "" {
		actual = media
	}
	return body, actual, nil
}

func (c *Client) inspectRemoteManifest(ctx context.Context, client *http.Client, host, repo string, body []byte, wanted Platform, cred Credentials) (PlatformEvidence, error) {
	var m manifestDocument
	if err := json.Unmarshal(body, &m); err != nil {
		return PlatformEvidence{Platform: wanted, Status: "UNKNOWN"}, errors.New("cannot decode registry image manifest")
	}
	if m.SchemaVersion != 2 {
		return PlatformEvidence{Platform: wanted, Status: "UNKNOWN"}, errors.New("unsupported registry image manifest schema")
	}
	configURL := "https://" + host + "/v2/" + repo + "/blobs/" + url.PathEscape(m.Config.Digest)
	resp, err := c.get(ctx, client, configURL, repo, m.Config.Digest, "", cred)
	if err != nil {
		return PlatformEvidence{Platform: wanted, Status: "UNKNOWN"}, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return PlatformEvidence{Platform: wanted, Status: "UNKNOWN"}, fmt.Errorf("registry returned HTTP %d while reading image configuration", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20+1))
	resp.Body.Close()
	if err != nil || len(data) > 4<<20 {
		return PlatformEvidence{Platform: wanted, Status: "UNKNOWN"}, errors.New("image configuration could not be read within size limit")
	}
	if int64(len(data)) != m.Config.Size || digestBytes(data) != m.Config.Digest {
		return PlatformEvidence{Platform: wanted, Status: "UNKNOWN"}, errors.New("registry image configuration digest or size mismatch")
	}
	var cfg platformConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return PlatformEvidence{Platform: wanted, Status: "UNKNOWN"}, errors.New("cannot decode image platform configuration")
	}
	actual := Platform{OS: cfg.OS, Architecture: cfg.Architecture, Variant: cfg.Variant}
	if !matchesPlatform(actual, wanted) {
		return PlatformEvidence{Platform: wanted, Status: "MISMATCH", ManifestDigest: digestBytes(body), ConfigDigest: m.Config.Digest, Detail: fmt.Sprintf("image config is %s/%s, selected worker is %s/%s", actual.OS, actual.Architecture, wanted.OS, wanted.Architecture)}, nil
	}
	pe := PlatformEvidence{Platform: wanted, Status: "VERIFIED", ManifestDigest: digestBytes(body), ConfigDigest: m.Config.Digest, Detail: "image configuration matches selected worker OS/architecture"}
	for _, l := range m.Layers {
		pe.LayerDigests = append(pe.LayerDigests, l.Digest)
	}
	return pe, nil
}

func digestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func dedupePlatforms(in []Platform) []Platform {
	seen := map[string]bool{}
	out := make([]Platform, 0, len(in))
	for _, p := range in {
		k := p.OS + "/" + p.Architecture + "/" + p.Variant
		if !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return out
}
func matchesPlatform(a, b Platform) bool {
	return a.OS == b.OS && a.Architecture == b.Architecture && (b.Variant == "" || a.Variant == b.Variant)
}
func unique(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func (c *Client) client(cred Credentials) (*http.Client, error) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if cred.CABundlePath != "" {
		b, e := os.ReadFile(cred.CABundlePath)
		if e != nil {
			return nil, fmt.Errorf("read registry CA bundle: %w", e)
		}
		roots, e := x509.SystemCertPool()
		if e != nil || roots == nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("registry CA bundle contains no usable certificates")
		}
		base.TLSClientConfig.RootCAs = roots
	}
	timeout := 12 * time.Second
	if c.HTTP != nil && c.HTTP.Timeout > 0 {
		timeout = c.HTTP.Timeout
	}
	return &http.Client{Transport: base, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

func (c *Client) bearerRequest(ctx context.Context, client *http.Client, target, challenge, repo, tag string, cred Credentials) (*http.Response, error) {
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return nil, fmt.Errorf("registry requires unsupported authentication challenge")
	}
	attrs := parseChallenge(challenge[len("Bearer "):])
	realm := attrs["realm"]
	if realm == "" {
		return nil, fmt.Errorf("registry Bearer challenge has no token realm")
	}
	u, e := url.Parse(realm)
	if e != nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("registry token realm must use HTTPS")
	}
	if cred.Token != "" {
		return nil, fmt.Errorf("provided bearer token was rejected by the registry")
	}
	q := u.Query()
	if attrs["service"] != "" {
		q.Set("service", attrs["service"])
	}
	scope := attrs["scope"]
	if scope == "" {
		scope = "repository:" + repo + ":pull"
	}
	q.Set("scope", scope)
	u.RawQuery = q.Encode()
	treq, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return nil, e
	}
	if cred.Username != "" {
		treq.SetBasicAuth(cred.Username, cred.Password)
	}
	tr, e := client.Do(treq)
	if e != nil {
		return nil, e
	}
	defer tr.Body.Close()
	if tr.StatusCode < 200 || tr.StatusCode >= 300 {
		return nil, fmt.Errorf("registry token service returned HTTP %d", tr.StatusCode)
	}
	var payload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if e = json.NewDecoder(io.LimitReader(tr.Body, 1<<20)).Decode(&payload); e != nil {
		return nil, fmt.Errorf("invalid registry token response")
	}
	token := payload.Token
	if token == "" {
		token = payload.AccessToken
	}
	if token == "" {
		return nil, fmt.Errorf("registry token response contained no token")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Accept", strings.Join([]string{"application/vnd.oci.image.index.v1+json", "application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json", "application/vnd.docker.distribution.manifest.v2+json"}, ", "))
	req.Header.Set("Authorization", "Bearer "+token)
	return client.Do(req)
}
func parseChallenge(s string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(s, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) == 2 {
			m[strings.ToLower(kv[0])] = strings.Trim(kv[1], `"`)
		}
	}
	return m
}
func supported(s string) bool {
	switch s {
	case "application/vnd.oci.image.index.v1+json", "application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json", "application/vnd.docker.distribution.manifest.v2+json":
		return true
	}
	return false
}
func classifyError(e error) Status {
	s := strings.ToLower(e.Error())
	if strings.Contains(s, "authentication") || strings.Contains(s, "bearer token was rejected") {
		return Unauthorized
	}
	if strings.Contains(s, "forbidden") {
		return Forbidden
	}
	var nerr net.Error
	if errors.As(e, &nerr) {
		return Unreachable
	}
	if strings.Contains(s, "certificate") || strings.Contains(s, "tls") {
		return TLSFailure
	}
	return Unknown
}

type RequiredImage struct {
	Component       string `json:"component"`
	SourceReference string `json:"source_reference"`
	Repository      string `json:"repository"`
	Tag             string `json:"tag"`
}
type ComponentProfile struct {
	Name          string `json:"name"`
	Replicas      int    `json:"replicas"`
	CPURequest    string `json:"cpu_request"`
	MemoryRequest string `json:"memory_request"`
	MemoryLimit   string `json:"memory_limit"`
}
type NodePortProfile struct {
	Name       string `json:"name"`
	Port       int    `json:"port"`
	TargetPort int    `json:"target_port"`
}
type JDBCCandidate struct {
	Runtime       string   `json:"runtime"`
	Artifacts     []string `json:"artifacts"`
	Version       string   `json:"version"`
	Qualification string   `json:"qualification"`
}
type ProfileDescriptor struct {
	SchemaVersion  int                `json:"schema_version"`
	ID             string             `json:"profile_id"`
	Version        string             `json:"profile_version"`
	WSO2           string             `json:"wso2_api_manager"`
	WSO2JDK        string             `json:"wso2_jdk"`
	Keycloak       string             `json:"keycloak"`
	KeycloakJDK    string             `json:"keycloak_container_jdk"`
	Oracle         string             `json:"oracle"`
	JDBCCandidates []JDBCCandidate    `json:"oracle_jdbc_candidates"`
	Components     []ComponentProfile `json:"components"`
	NodePorts      []NodePortProfile  `json:"node_ports"`
	Images         []RequiredImage    `json:"images"`
}

//go:embed profiles/initial-candidate.json
var profileJSON []byte

var Profile = loadProfile()

func loadProfile() ProfileDescriptor {
	var p ProfileDescriptor
	if err := json.Unmarshal(profileJSON, &p); err != nil {
		panic("invalid embedded compatibility profile: " + err.Error())
	}
	if p.SchemaVersion != 1 || p.ID == "" || len(p.Images) == 0 {
		panic("embedded compatibility profile has invalid schema or no images")
	}
	return p
}
