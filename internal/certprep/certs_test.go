package certprep

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pavlo-v-chernykh/keystore-go/v4"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/secretstore"
	"software.sslmate.com/src/go-pkcs12"
)

func TestGenerateReusesCAAndLeavesAndCreatesJavaCompatibleStores(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	names := Names{Namespace: "wso2", ClusterDomain: "cluster.local", APIMHost: "apim.example.com", GatewayHost: "api.example.com", KeycloakHost: "sso.example.com"}
	outA := filepath.Join(t.TempDir(), "public-a")
	first, err := Generate(state, outA, names)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(state, filepath.Join(t.TempDir(), "public-b"), names)
	if err != nil {
		t.Fatal(err)
	}
	if first.Root.FingerprintSHA256 != second.Root.FingerprintSHA256 {
		t.Fatal("installation CA changed on rerun")
	}
	for i := range first.Certificates {
		if first.Certificates[i].FingerprintSHA256 != second.Certificates[i].FingerprintSHA256 {
			t.Fatalf("leaf %s changed on identical rerun", first.Certificates[i].Purpose)
		}
	}
	changed := names
	changed.APIMHost = "new-apim.example.com"
	third, err := Generate(state, filepath.Join(t.TempDir(), "public-c"), changed)
	if err != nil {
		t.Fatal(err)
	}
	if first.Root.FingerprintSHA256 != third.Root.FingerprintSHA256 {
		t.Fatal("CA rotated when a hostname changed")
	}
	if first.Certificates[0].FingerprintSHA256 == third.Certificates[0].FingerprintSHA256 {
		t.Fatal("external leaf was not regenerated for changed DNS SAN")
	}
	if first.Certificates[3].FingerprintSHA256 != third.Certificates[3].FingerprintSHA256 {
		t.Fatal("unaffected internal certificate changed")
	}
	s, err := secretstore.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	password, err := s.Get("p12-apim-external")
	if err != nil {
		t.Fatal(err)
	}
	pfx, err := s.Get("keystore-apim-external")
	if err != nil {
		t.Fatal(err)
	}
	key, cert, chain, err := pkcs12.DecodeChain(pfx, string(password))
	if err != nil || key == nil || cert == nil || len(chain) == 0 {
		t.Fatalf("PKCS#12 service keystore round trip failed: %v", err)
	}
	jksPassword, err := s.Get("jks-apim-external")
	if err != nil {
		t.Fatal(err)
	}
	jksData, err := s.Get("jks-keystore-apim-external")
	if err != nil {
		t.Fatal(err)
	}
	jks := keystore.New(keystore.WithOrderedAliases())
	if err := jks.Load(bytes.NewReader(jksData), jksPassword); err != nil {
		t.Fatalf("JKS service keystore round trip failed: %v", err)
	}
	if !jks.IsPrivateKeyEntry("wso2carbon") {
		t.Fatalf("expected WSO2 keystore alias missing: %v", jks.Aliases())
	}
	if _, err := jks.GetPrivateKeyEntry("wso2carbon", jksPassword); err != nil {
		t.Fatalf("JKS private-key alias did not resolve: %v", err)
	}
	trustPass, err := s.Get("p12-trust-root")
	if err != nil {
		t.Fatal(err)
	}
	// The exported truststore is public and contains an aliased root entry; Go
	// parsing validates its contents. Java keytool interoperability is a separate
	// qualification gate when a JDK is available.
	trustPath := filepath.Join(outA, "application-truststore.p12")
	trustData, err := os.ReadFile(trustPath)
	if err != nil {
		t.Fatal(err)
	}
	certs, err := pkcs12.DecodeTrustStore(trustData, string(trustPass))
	if err != nil || len(certs) != 1 {
		t.Fatalf("truststore round trip failed: %v %d", err, len(certs))
	}
	jksTrustData, err := os.ReadFile(filepath.Join(outA, "application-truststore.jks"))
	if err != nil {
		t.Fatal(err)
	}
	jksTrust := keystore.New(keystore.WithOrderedAliases())
	jksTrustPass, err := s.Get("jks-trust-root")
	if err != nil {
		t.Fatal(err)
	}
	if err := jksTrust.Load(bytes.NewReader(jksTrustData), jksTrustPass); err != nil {
		t.Fatalf("JKS truststore round trip failed: %v", err)
	}
	if !jksTrust.IsTrustedCertificateEntry("sysdev-trust-1") {
		t.Fatalf("expected trust alias missing: %v", jksTrust.Aliases())
	}
}

func TestImportedCertificatesCheckMismatchSANExpiryAndChain(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		externalSAN                 []string
		expired, badTrust, mismatch bool
	}{
		{"valid", []string{"apim.example.com", "api.example.com", "sso.example.com"}, false, false, false},
		{"missing SAN", []string{"apim.example.com", "sso.example.com"}, false, false, false},
		{"expired", []string{"apim.example.com", "api.example.com", "sso.example.com"}, true, false, false},
		{"invalid chain", []string{"apim.example.com", "api.example.com", "sso.example.com"}, false, true, false},
		{"key mismatch", []string{"apim.example.com", "api.example.com", "sso.example.com"}, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			n := Names{Namespace: "wso2", ClusterDomain: "cluster.local", APIMHost: "apim.example.com", GatewayHost: "api.example.com", KeycloakHost: "sso.example.com"}
			now := time.Now().UTC()
			rootPEM, rootKey := makeRoot(t, now)
			rootBlock, _ := pem.Decode(rootPEM)
			rootCert, _ := x509.ParseCertificate(rootBlock.Bytes)
			trusted := rootPEM
			if tc.badTrust {
				trusted, _ = makeRoot(t, now)
			}
			extCert, extKey := makeLeaf(t, rootCert, rootKey, now, tc.expired, tc.externalSAN)
			intCert, intKey := makeLeaf(t, rootCert, rootKey, now, false, internalNames(n))
			paths := map[string]string{}
			write := func(name string, b []byte) string {
				p := filepath.Join(dir, name)
				if e := os.WriteFile(p, b, 0600); e != nil {
					t.Fatal(e)
				}
				return p
			}
			paths["ec"] = write("external.crt", extCert)
			paths["ek"] = write("external.key", extKey)
			paths["et"] = write("external-trust.crt", trusted)
			paths["ic"] = write("internal.crt", intCert)
			paths["ik"] = write("internal.key", intKey)
			paths["it"] = write("internal-trust.crt", rootPEM)
			if tc.mismatch {
				_, wrong := makeLeaf(t, rootCert, rootKey, now, false, tc.externalSAN)
				paths["ek"] = write("wrong.key", wrong)
			}
			cfg := config.Certificates{Mode: "import", ExternalCertificatePath: paths["ec"], ExternalPrivateKeyPath: paths["ek"], ExternalTrustPath: paths["et"], InternalCertificatePath: paths["ic"], InternalPrivateKeyPath: paths["ik"], InternalTrustPath: paths["it"]}
			_, err := ValidateImport(cfg, n, now)
			if tc.name == "valid" && err != nil {
				t.Fatal(err)
			} else if tc.name == "valid" {
				state := filepath.Join(dir, "protected")
				if _, err := PrepareImport(cfg, n, state, now); err != nil {
					t.Fatal(err)
				}
				store, err := secretstore.Open(state)
				if err != nil {
					t.Fatal(err)
				}
				password, err := store.Get("p12-import-external")
				if err != nil {
					t.Fatal(err)
				}
				pfx, err := store.Get("keystore-import-external")
				if err != nil {
					t.Fatal(err)
				}
				if _, _, _, err := pkcs12.DecodeChain(pfx, string(password)); err != nil {
					t.Fatalf("imported keystore did not round trip: %v", err)
				}
				trustPassword, err := store.Get("p12-import-trust-external")
				if err != nil {
					t.Fatal(err)
				}
				trust, err := store.Get("truststore-import-external")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := pkcs12.DecodeTrustStore(trust, string(trustPassword)); err != nil {
					t.Fatalf("imported truststore did not round trip: %v", err)
				}
			}
			if tc.name != "valid" && err == nil {
				t.Fatal("invalid imported certificate passed validation")
			}
		})
	}
}

func TestGeneratedOutputRefusesOverwrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	n := Names{Namespace: "wso2", ClusterDomain: "cluster.local", APIMHost: "apim.example.com", GatewayHost: "api.example.com", KeycloakHost: "sso.example.com"}
	if _, err := Generate(filepath.Join(t.TempDir(), "state"), dir, n); err == nil {
		t.Fatal("existing output was overwritten")
	}
}

func makeRoot(t *testing.T, now time.Time) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Fixture Root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(2, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, e := x509.CreateCertificate(rand.Reader, tmpl, tmpl, k.Public(), k)
	if e != nil {
		t.Fatal(e)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), k
}
func makeLeaf(t *testing.T, root *x509.Certificate, rootKey *ecdsa.PrivateKey, now time.Time, expired bool, sans []string) ([]byte, []byte) {
	t.Helper()
	k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	before, after := now.Add(-time.Hour), now.AddDate(1, 0, 0)
	if expired {
		before, after = now.AddDate(-3, 0, 0), now.AddDate(-1, 0, 0)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: sans[0]}, DNSNames: sans, NotBefore: before, NotAfter: after, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, tmpl, root, k.Public(), rootKey)
	if e != nil {
		t.Fatal(e)
	}
	keyDER, e := x509.MarshalPKCS8PrivateKey(k)
	if e != nil {
		t.Fatal(e)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}
