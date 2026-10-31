package registry

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const manifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"sha256:abc","size":2},"layers":[]}`

func newRegistryTest(t *testing.T, handler http.HandlerFunc) (*httptest.Server, Credentials) {
	t.Helper()
	s := httptest.NewTLSServer(handler)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if e := os.WriteFile(ca, cert, 0600); e != nil {
		t.Fatal(e)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(cert) {
		t.Fatal("invalid fixture CA")
	}
	return s, Credentials{CABundlePath: ca}
}

func TestCheckExactReferenceAndDigest(t *testing.T) {
	s, cred := newRegistryTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/team/app/manifests/v1" {
			http.Error(w, "wrong path", 404)
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "application/vnd.oci.image.manifest.v1+json") {
			t.Errorf("manifest Accept header missing: %s", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Docker-Content-Digest", digestBytes([]byte(manifest)))
		w.Write([]byte(manifest))
	})
	defer s.Close()
	host := strings.TrimPrefix(s.URL, "https://")
	got, e := NewClient().Check(context.Background(), host, "team", cred, []RequiredImage{{Component: "App", Repository: "app", Tag: "v1"}})
	if e != nil {
		t.Fatal(e)
	}
	if len(got) != 1 || got[0].Status != Found || got[0].Digest != digestBytes([]byte(manifest)) {
		t.Fatalf("unexpected result: %#v", got)
	}
}

func TestCheckResolvesSelectedPlatformFromIndexAndConfig(t *testing.T) {
	configBody := []byte(`{"os":"linux","architecture":"arm64","variant":"v8"}`)
	configDescriptor := registryDescriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: digestBytes(configBody), Size: int64(len(configBody))}
	manifestBody, _ := json.Marshal(manifestDocument{SchemaVersion: 2, Config: configDescriptor})
	child := registryDescriptor{MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: digestBytes(manifestBody), Size: int64(len(manifestBody)), Platform: &Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}}
	indexBody, _ := json.Marshal(indexDocument{SchemaVersion: 2, Manifests: []registryDescriptor{child}})
	s, cred := newRegistryTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/team/app/manifests/v1":
			w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
			w.Header().Set("Docker-Content-Digest", digestBytes(indexBody))
			_, _ = w.Write(indexBody)
		case "/v2/team/app/manifests/" + child.Digest:
			w.Header().Set("Content-Type", child.MediaType)
			_, _ = w.Write(manifestBody)
		case "/v2/team/app/blobs/" + configDescriptor.Digest:
			w.Header().Set("Content-Type", configDescriptor.MediaType)
			_, _ = w.Write(configBody)
		default:
			http.NotFound(w, r)
		}
	})
	defer s.Close()
	got, err := NewClient().CheckForPlatforms(context.Background(), strings.TrimPrefix(s.URL, "https://"), "team", cred, []RequiredImage{{Component: "App", Repository: "app", Tag: "v1"}}, []Platform{{OS: "linux", Architecture: "arm64", Variant: "v8"}, {OS: "windows", Architecture: "amd64"}})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Status != Found || got[0].PlatformStatus != "INCOMPLETE" || got[0].IndexDigest != digestBytes(indexBody) {
		t.Fatalf("unexpected platform inventory: %#v", got[0])
	}
	if len(got[0].Platforms) != 2 || got[0].Platforms[0].Status != "VERIFIED" || got[0].Platforms[0].ConfigDigest != configDescriptor.Digest || got[0].Platforms[1].Status != "MISSING" {
		t.Fatalf("selected platform evidence is wrong: %#v", got[0].Platforms)
	}
}

func TestBearerChallengeAndBasicAuth(t *testing.T) {
	var base string
	s, cred := newRegistryTest(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			fmt.Fprint(w, `{"token":"temporary-fixture-token"}`)
		case "/v2/team/app/manifests/v1":
			if r.Header.Get("Authorization") != "Bearer temporary-fixture-token" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+base+`/token",service="fixture",scope="repository:team/app:pull"`)
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Write([]byte(manifest))
		default:
			http.NotFound(w, r)
		}
	})
	defer s.Close()
	base = s.URL
	got, e := NewClient().Check(context.Background(), strings.TrimPrefix(s.URL, "https://"), "team", cred, []RequiredImage{{Component: "App", Repository: "app", Tag: "v1"}})
	if e != nil {
		t.Fatal(e)
	}
	if got[0].Status != Found {
		t.Fatalf("Bearer result: %#v", got)
	}
	var seenBasic bool
	basic, bcred := newRegistryTest(t, func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		seenBasic = ok && u == "reader" && p == "pw"
		if !seenBasic {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Write([]byte(manifest))
	})
	defer basic.Close()
	bcred.Username = "reader"
	bcred.Password = "pw"
	got, e = NewClient().Check(context.Background(), strings.TrimPrefix(basic.URL, "https://"), "team", bcred, []RequiredImage{{Component: "App", Repository: "app", Tag: "v1"}})
	if e != nil {
		t.Fatal(e)
	}
	if !seenBasic || got[0].Status != Found {
		t.Fatalf("Basic result: seen=%t got=%#v", seenBasic, got)
	}
}

func TestRegistryFailureClasses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		want   Status
	}{{"missing", 404, Missing}, {"forbidden", 403, Forbidden}, {"unauthorized", 401, Unauthorized}, {"unsupported", 200, Unsupported}} {
		t.Run(tc.name, func(t *testing.T) {
			s, cred := newRegistryTest(t, func(w http.ResponseWriter, r *http.Request) {
				if tc.status == 200 {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{}`)
					return
				}
				w.WriteHeader(tc.status)
			})
			defer s.Close()
			got, e := NewClient().Check(context.Background(), strings.TrimPrefix(s.URL, "https://"), "team", cred, []RequiredImage{{Component: "App", Repository: "app", Tag: "v1"}})
			if e != nil {
				t.Fatal(e)
			}
			if got[0].Status != tc.want {
				t.Fatalf("got %s want %s (%s)", got[0].Status, tc.want, got[0].Detail)
			}
		})
	}
}

func TestInvalidEndpoint(t *testing.T) {
	if e := ValidateEndpoint("https://registry.example", "team"); e == nil {
		t.Fatal("accepted URL scheme in registry host")
	}
	if e := ValidateEndpoint("registry.example", "Team/Bad"); e == nil {
		t.Fatal("accepted invalid project prefix")
	}
}

func TestChecksUseBoundedParallelism(t *testing.T) {
	var active, maxActive atomic.Int32
	s, cred := newRegistryTest(t, func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := maxActive.Load()
			if n <= old || maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		fmt.Fprint(w, manifest)
	})
	defer s.Close()
	got, e := NewClient().Check(context.Background(), strings.TrimPrefix(s.URL, "https://"), "team", cred, Profile.Images)
	if e != nil {
		t.Fatal(e)
	}
	if len(got) != 4 {
		t.Fatalf("checked %d images", len(got))
	}
	if maxActive.Load() < 2 || maxActive.Load() > 4 {
		t.Fatalf("observed concurrency %d, want bounded 2..4", maxActive.Load())
	}
}

func TestBearerChallengeMayUseTrustedHTTPSAuthRealm(t *testing.T) {
	var authRequests atomic.Int32
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authRequests.Add(1)
		u, p, ok := r.BasicAuth()
		if !ok || u != "reader" || p != "password-canary-481" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"token":"scoped-fixture-token"}`)
	}))
	defer foreign.Close()
	primary, cred := newRegistryTest(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer scoped-fixture-token" {
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			fmt.Fprint(w, manifest)
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+foreign.URL+`/token",service="fixture",scope="repository:team/app:pull"`)
		w.WriteHeader(401)
	})
	defer primary.Close()
	ca, err := os.ReadFile(cred.CABundlePath)
	if err != nil {
		t.Fatal(err)
	}
	ca = append(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: foreign.Certificate().Raw})...)
	if err = os.WriteFile(cred.CABundlePath, ca, 0600); err != nil {
		t.Fatal(err)
	}
	cred.Username = "reader"
	cred.Password = "password-canary-481"
	got, e := NewClient().Check(context.Background(), strings.TrimPrefix(primary.URL, "https://"), "team", cred, []RequiredImage{{Component: "App", Repository: "app", Tag: "v1"}})
	if e != nil {
		t.Fatal(e)
	}
	if got[0].Status != Found || authRequests.Load() != 1 {
		t.Fatalf("Bearer result=%#v auth requests=%d", got, authRequests.Load())
	}
}
