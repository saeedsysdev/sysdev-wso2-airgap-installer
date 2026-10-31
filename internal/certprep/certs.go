package certprep

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pavlo-v-chernykh/keystore-go/v4"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/secretstore"
	"software.sslmate.com/src/go-pkcs12"
)

type Names struct{ Namespace, ClusterDomain, APIMHost, GatewayHost, KeycloakHost string }
type Summary struct {
	Purpose           string   `json:"purpose"`
	Subject           string   `json:"subject"`
	SANs              []string `json:"sans"`
	NotAfter          string   `json:"not_after"`
	FingerprintSHA256 string   `json:"fingerprint_sha256"`
	Trust             string   `json:"trust"`
}
type Result struct {
	Root              Summary   `json:"root"`
	Certificates      []Summary `json:"certificates"`
	TrustDistribution string    `json:"trust_distribution"`
	Deferred          []string  `json:"deferred,omitempty"`
}

// PrepareImport validates imported material and builds protected PKCS#12
// keystores for the Java workloads. It never writes private material to output.
func PrepareImport(c config.Certificates, n Names, stateDir string, now time.Time) (Result, error) {
	result, err := ValidateImport(c, n, now)
	if err != nil {
		return Result{}, err
	}
	store, err := secretstore.Open(stateDir)
	if err != nil {
		return Result{}, err
	}
	inputs := []struct{ name, certPath, keyPath, chainPath, trustPath string }{
		{"external", c.ExternalCertificatePath, c.ExternalPrivateKeyPath, c.ExternalChainPath, c.ExternalTrustPath},
		{"internal", c.InternalCertificatePath, c.InternalPrivateKeyPath, c.InternalChainPath, c.InternalTrustPath},
	}
	for _, item := range inputs {
		certPEM, err := boundedRead(item.certPath, 16<<20)
		if err != nil {
			return Result{}, errors.New("read imported certificate")
		}
		keyPEM, err := boundedRead(item.keyPath, 16<<20)
		if err != nil {
			return Result{}, errors.New("read imported private key")
		}
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return Result{}, errors.New("imported certificate and private key do not match")
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return Result{}, errors.New("parse imported certificate")
		}
		cas := []*x509.Certificate{}
		chainPath := item.chainPath
		if chainPath == "" {
			chainPath = item.certPath
		}
		chainPEM, err := boundedRead(chainPath, 16<<20)
		if err != nil {
			return Result{}, errors.New("read imported certificate chain")
		}
		for rest := chainPEM; len(rest) > 0; {
			block, next := pem.Decode(rest)
			rest = next
			if block == nil {
				break
			}
			if block.Type != "CERTIFICATE" {
				continue
			}
			chain, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return Result{}, errors.New("parse imported certificate chain")
			}
			if !bytesEqual(chain.Raw, leaf.Raw) {
				cas = append(cas, chain)
			}
		}
		password, err := protectedRandom(store, "p12-import-"+item.name, 24)
		if err != nil {
			return Result{}, err
		}
		pfx, err := pkcs12.Modern2023.Encode(pair.PrivateKey, leaf, cas, string(password))
		clear(password)
		if err != nil {
			return Result{}, errors.New("create imported PKCS#12 service keystore")
		}
		if err := store.Put("keystore-import-"+item.name, pfx); err != nil {
			return Result{}, err
		}
		trustPEM, err := boundedRead(item.trustPath, 16<<20)
		if err != nil {
			return Result{}, errors.New("read imported trust anchors")
		}
		roots := []*x509.Certificate{}
		for rest := trustPEM; len(rest) > 0; {
			block, next := pem.Decode(rest)
			rest = next
			if block == nil {
				break
			}
			if block.Type != "CERTIFICATE" {
				continue
			}
			root, e := x509.ParseCertificate(block.Bytes)
			if e != nil {
				return Result{}, errors.New("parse imported trust anchor")
			}
			roots = append(roots, root)
		}
		trustPass, err := protectedRandom(store, "p12-import-trust-"+item.name, 24)
		if err != nil {
			return Result{}, err
		}
		trustP12, err := pkcs12.Modern2023.EncodeTrustStoreEntries(trustEntries(roots, item.name), string(trustPass))
		clear(trustPass)
		if err != nil {
			return Result{}, errors.New("create imported Java-compatible truststore")
		}
		if err := store.Put("truststore-import-"+item.name, trustP12); err != nil {
			return Result{}, err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(pair.PrivateKey)
		if err != nil {
			return Result{}, errors.New("encode imported PKCS#8 key")
		}
		jksPassword, err := protectedRandom(store, "jks-import-"+item.name, 24)
		if err != nil {
			return Result{}, err
		}
		jksBytes, err := makeJKSKeyStore("wso2carbon", keyDER, append([]*x509.Certificate{leaf}, cas...), string(jksPassword))
		clear(jksPassword)
		if err != nil {
			return Result{}, errors.New("create imported WSO2 JKS keystore")
		}
		if err := store.Put("jks-keystore-import-"+item.name, jksBytes); err != nil {
			return Result{}, err
		}
		jksTrustPassword, err := protectedRandom(store, "jks-import-trust-"+item.name, 24)
		if err != nil {
			return Result{}, err
		}
		jksTrust, err := makeJKSTrustStore(roots, string(jksTrustPassword))
		clear(jksTrustPassword)
		if err != nil {
			return Result{}, errors.New("create imported WSO2 JKS truststore")
		}
		if err := store.Put("jks-truststore-import-"+item.name, jksTrust); err != nil {
			return Result{}, err
		}
	}
	return result, nil
}

// Generate reuses one installation CA, rotates only leaves whose SAN set has
// changed, stores all private keys and PKCS#12 keystores in the protected store,
// and writes public certificates/trust material to the new output directory.
func Generate(stateDir, outputDir string, n Names) (Result, error) {
	if !validDNS(n.Namespace) || !validDNS(n.ClusterDomain) {
		return Result{}, errors.New("namespace and cluster DNS domain are required for internal certificate SANs")
	}
	for _, host := range []string{n.APIMHost, n.GatewayHost, n.KeycloakHost} {
		if !validDNS(host) {
			return Result{}, errors.New("all external service hostnames must be valid DNS names")
		}
	}
	if err := os.Mkdir(outputDir, 0700); err != nil {
		return Result{}, errors.New("create a new, empty certificate output directory")
	}
	store, err := secretstore.Open(stateDir)
	if err != nil {
		return Result{}, err
	}
	root, rootKey, rootPEM, err := loadOrCreateCA(store)
	if err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "trust-root.crt"), rootPEM, 0644); err != nil {
		return Result{}, errors.New("write public root certificate")
	}
	out := Result{Root: summarize("private CA", root, "local installation CA"), TrustDistribution: "Distribute trust-root.crt explicitly to browser clients, external load balancers, Kubernetes node runtimes, and application truststores that must validate this installation. No system trust store was changed."}
	services := []struct {
		name string
		sans []string
	}{
		{"apim-external", []string{n.APIMHost}}, {"gateway-external", []string{n.GatewayHost}}, {"keycloak-external", []string{n.KeycloakHost}},
		{"wso2-internal", internalNames(n)}, {"keycloak-internal", []string{serviceDNS("keycloak", n), serviceDNS("keycloak-headless", n)}},
		{"traffic-manager-1", []string{serviceDNS("apim-tm-1", n), serviceDNS("apim-tm-headless", n)}},
		{"traffic-manager-2", []string{serviceDNS("apim-tm-2", n), serviceDNS("apim-tm-headless", n)}},
	}
	for _, spec := range services {
		cert, key, certPEM, err := loadOrCreateLeaf(store, root, rootKey, spec.name, spec.sans)
		if err != nil {
			return Result{}, err
		}
		if err := os.WriteFile(filepath.Join(outputDir, spec.name+".crt"), certPEM, 0644); err != nil {
			return Result{}, errors.New("write public service certificate")
		}
		out.Certificates = append(out.Certificates, summarize(spec.name, cert, "issued by installation CA"))
		password, err := protectedRandom(store, "p12-"+spec.name, 24)
		if err != nil {
			return Result{}, err
		}
		pfx, err := pkcs12.Modern2023.Encode(key, cert, []*x509.Certificate{root}, string(password))
		clear(password)
		if err != nil {
			return Result{}, fmt.Errorf("create Java-compatible PKCS#12 service keystore: %w", err)
		}
		if err := store.Put("keystore-"+spec.name, pfx); err != nil {
			return Result{}, err
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return Result{}, errors.New("encode generated PKCS#8 service key")
		}
		jksPassword, err := protectedRandom(store, "jks-"+spec.name, 24)
		if err != nil {
			return Result{}, err
		}
		jksBytes, err := makeJKSKeyStore("wso2carbon", keyDER, []*x509.Certificate{cert, root}, string(jksPassword))
		clear(jksPassword)
		if err != nil {
			return Result{}, errors.New("create WSO2 JKS service keystore")
		}
		if err := store.Put("jks-keystore-"+spec.name, jksBytes); err != nil {
			return Result{}, err
		}
	}
	trustPassword, err := protectedRandom(store, "p12-trust-root", 24)
	if err != nil {
		return Result{}, err
	}
	trustP12, err := pkcs12.Modern2023.EncodeTrustStoreEntries([]pkcs12.TrustStoreEntry{{Cert: root, FriendlyName: "sysdev-installation-root"}}, string(trustPassword))
	clear(trustPassword)
	if err != nil {
		return Result{}, errors.New("create Java-compatible PKCS#12 truststore")
	}
	// Truststores contain only public certificates; they are safe for the public
	// export. Their password remains protected and is resolved during Stage 4.
	if err := os.WriteFile(filepath.Join(outputDir, "application-truststore.p12"), trustP12, 0644); err != nil {
		return Result{}, errors.New("write public truststore")
	}
	jksPassword, err := protectedRandom(store, "jks-trust-root", 24)
	if err != nil {
		return Result{}, err
	}
	jksTrust, err := makeJKSTrustStore([]*x509.Certificate{root}, string(jksPassword))
	clear(jksPassword)
	if err != nil {
		return Result{}, errors.New("create WSO2 JKS truststore")
	}
	if err := os.WriteFile(filepath.Join(outputDir, "application-truststore.jks"), jksTrust, 0644); err != nil {
		return Result{}, errors.New("write public JKS truststore")
	}
	return out, nil
}

func ValidateImport(c config.Certificates, n Names, now time.Time) (Result, error) {
	if c.Mode != "import" {
		return Result{}, errors.New("certificate mode is not import")
	}
	storeSpecs := []struct {
		purpose, certPath, keyPath, chainPath, trustPath string
		names                                            []string
	}{
		{"external", c.ExternalCertificatePath, c.ExternalPrivateKeyPath, c.ExternalChainPath, c.ExternalTrustPath, []string{n.APIMHost, n.GatewayHost, n.KeycloakHost}},
		{"internal", c.InternalCertificatePath, c.InternalPrivateKeyPath, c.InternalChainPath, c.InternalTrustPath, internalNames(n)},
	}
	result := Result{TrustDistribution: "Imported trust anchors are operator supplied. Distribution to browsers, load balancers, node runtimes, and application truststores remains an operator action."}
	for _, spec := range storeSpecs {
		certPEM, err := boundedRead(spec.certPath, 16<<20)
		if err != nil {
			return Result{}, fmt.Errorf("read %s certificate input", spec.purpose)
		}
		keyPEM, err := boundedRead(spec.keyPath, 16<<20)
		if err != nil {
			return Result{}, fmt.Errorf("read %s private-key input", spec.purpose)
		}
		pair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return Result{}, fmt.Errorf("%s certificate/private key do not form a supported matching PEM pair", spec.purpose)
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return Result{}, fmt.Errorf("parse %s leaf certificate", spec.purpose)
		}
		if err := validateLeaf(leaf, spec.names, now); err != nil {
			return Result{}, fmt.Errorf("%s certificate: %w", spec.purpose, err)
		}
		trustPath := spec.trustPath
		if trustPath == "" {
			return Result{}, fmt.Errorf("%s trust anchor is required", spec.purpose)
		}
		trustBytes, err := boundedRead(trustPath, 16<<20)
		if err != nil {
			return Result{}, fmt.Errorf("read %s trust anchor", spec.purpose)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(trustBytes) {
			return Result{}, fmt.Errorf("%s trust anchor contains no supported PEM certificates", spec.purpose)
		}
		intermediates := x509.NewCertPool()
		chainBytes := certPEM
		if spec.chainPath != "" {
			chainBytes, err = boundedRead(spec.chainPath, 16<<20)
			if err != nil {
				return Result{}, fmt.Errorf("read %s certificate chain", spec.purpose)
			}
		}
		for rest := chainBytes; len(rest) > 0; {
			block, next := pem.Decode(rest)
			rest = next
			if block == nil {
				break
			}
			if block.Type == "CERTIFICATE" {
				x, e := x509.ParseCertificate(block.Bytes)
				if e != nil {
					return Result{}, fmt.Errorf("parse %s certificate chain", spec.purpose)
				}
				if !bytesEqual(x.Raw, leaf.Raw) {
					intermediates.AddCert(x)
				}
			}
		}
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, CurrentTime: now}); err != nil {
			return Result{}, fmt.Errorf("%s certificate chain is not trusted", spec.purpose)
		}
		result.Certificates = append(result.Certificates, summarize(spec.purpose, leaf, "chain verified against supplied trust anchor"))
	}
	return result, nil
}

func validateLeaf(c *x509.Certificate, names []string, now time.Time) error {
	if now.Before(c.NotBefore) || now.After(c.NotAfter) {
		return errors.New("certificate is outside its validity period")
	}
	if c.IsCA || c.KeyUsage&x509.KeyUsageDigitalSignature == 0 || (c.KeyUsage&x509.KeyUsageKeyEncipherment == 0 && c.PublicKeyAlgorithm == x509.RSA) {
		return errors.New("certificate key usage is not suitable for a TLS server")
	}
	for _, name := range names {
		if err := c.VerifyHostname(name); err != nil {
			return fmt.Errorf("required SAN %q is not covered", name)
		}
	}
	return nil
}

func loadOrCreateCA(s *secretstore.Store) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	certBytes, certErr := s.Get("pki-root-cert")
	keyBytes, keyErr := s.Get("pki-root-key")
	if certErr == nil && keyErr == nil {
		cert, err := parseCert(certBytes)
		if err != nil {
			return nil, nil, nil, errors.New("stored installation CA certificate is invalid")
		}
		key, err := parseECKey(keyBytes)
		if err != nil {
			return nil, nil, nil, errors.New("stored installation CA key is invalid")
		}
		if !publicMatches(cert, key) || !cert.IsCA {
			return nil, nil, nil, errors.New("stored installation CA material does not match")
		}
		return cert, key, certBytes, nil
	}
	if certErr == nil || keyErr == nil {
		return nil, nil, nil, errors.New("incomplete existing installation CA; refusing to replace private key material")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, errors.New("generate installation CA key")
	}
	now := time.Now().UTC()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, nil, errors.New("generate installation CA serial")
	}
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Sysdev WSO2 Airgap Installer Installation Root"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0), KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, IsCA: true, BasicConstraintsValid: true, MaxPathLen: 1}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, nil, nil, errors.New("create installation CA certificate")
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, errors.New("parse generated installation CA")
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, nil, errors.New("encode installation CA key")
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := s.Put("pki-root-key", keyPEM); err != nil {
		return nil, nil, nil, err
	}
	if err := s.Put("pki-root-cert", certPEM); err != nil {
		return nil, nil, nil, err
	}
	return cert, key, certPEM, nil
}

func loadOrCreateLeaf(s *secretstore.Store, ca *x509.Certificate, caKey *ecdsa.PrivateKey, name string, sans []string) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	sans = canonical(sans)
	sum := sha256.Sum256([]byte(strings.Join(sans, "\n")))
	id := name + "-" + hex.EncodeToString(sum[:4])
	certName := "pki-leaf-cert-" + id
	keyName := "pki-leaf-key-" + id
	cb, ce := s.Get(certName)
	kb, ke := s.Get(keyName)
	if ce == nil && ke == nil {
		cert, err := parseCert(cb)
		if err != nil {
			return nil, nil, nil, errors.New("stored service certificate is invalid")
		}
		key, err := parseECKey(kb)
		if err != nil || !publicMatches(cert, key) {
			return nil, nil, nil, errors.New("stored service certificate key does not match")
		}
		if err := validateLeaf(cert, sans, time.Now()); err != nil {
			return nil, nil, nil, fmt.Errorf("stored service certificate is no longer usable: %w", err)
		}
		return cert, key, cb, nil
	}
	if ce == nil || ke == nil {
		return nil, nil, nil, errors.New("incomplete stored service certificate; refusing silent key replacement")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, errors.New("generate service certificate key")
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, nil, errors.New("generate service certificate serial")
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: sans[0]}, DNSNames: sans, NotBefore: now.Add(-5 * time.Minute), NotAfter: minTime(now.AddDate(2, 0, 0), ca.NotAfter), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, key.Public(), caKey)
	if err != nil {
		return nil, nil, nil, errors.New("create service certificate")
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, errors.New("parse service certificate")
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, nil, errors.New("encode service private key")
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := s.Put(keyName, keyPEM); err != nil {
		return nil, nil, nil, err
	}
	if err := s.Put(certName, certPEM); err != nil {
		return nil, nil, nil, err
	}
	return cert, key, certPEM, nil
}

func protectedRandom(s *secretstore.Store, name string, n int) ([]byte, error) {
	b, err := s.Get(name)
	if err == nil {
		if len(b) != n {
			return nil, errors.New("stored keystore password has invalid length")
		}
		return b, nil
	}
	b = make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, errors.New("generate keystore password")
	}
	if err := s.Put(name, b); err != nil {
		clear(b)
		return nil, err
	}
	return b, nil
}
func parseCert(b []byte) (*x509.Certificate, error) {
	p, _ := pem.Decode(b)
	if p == nil || p.Type != "CERTIFICATE" {
		return nil, errors.New("not a PEM certificate")
	}
	return x509.ParseCertificate(p.Bytes)
}
func parseECKey(b []byte) (*ecdsa.PrivateKey, error) {
	p, _ := pem.Decode(b)
	if p == nil {
		return nil, errors.New("not a PEM private key")
	}
	v, err := x509.ParsePKCS8PrivateKey(p.Bytes)
	if err != nil {
		return nil, err
	}
	k, ok := v.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("unsupported key type")
	}
	return k, nil
}
func publicMatches(c *x509.Certificate, k *ecdsa.PrivateKey) bool {
	if c == nil || k == nil {
		return false
	}
	pub, ok := c.PublicKey.(*ecdsa.PublicKey)
	return ok && pub.X.Cmp(k.PublicKey.X) == 0 && pub.Y.Cmp(k.PublicKey.Y) == 0
}
func internalNames(n Names) []string {
	out := []string{}
	for _, svc := range []string{"apim-cp", "apim-km", "apim-gateway", "keycloak", "keycloak-headless", "apim-tm-1", "apim-tm-2", "apim-tm-headless"} {
		out = append(out, serviceDNS(svc, n))
	}
	return canonical(out)
}
func serviceDNS(service string, n Names) string {
	return service + "." + n.Namespace + ".svc." + n.ClusterDomain
}
func canonical(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	j := 0
	for _, v := range out {
		if j == 0 || out[j-1] != v {
			out[j] = v
			j++
		}
	}
	return out[:j]
}
func validDNS(v string) bool {
	if len(v) == 0 || len(v) > 253 || strings.ToLower(v) != v {
		return false
	}
	for _, p := range strings.Split(v, ".") {
		if len(p) == 0 || len(p) > 63 || p[0] == '-' || p[len(p)-1] == '-' {
			return false
		}
		for _, r := range p {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}
func summarize(name string, c *x509.Certificate, trust string) Summary {
	sum := sha256.Sum256(c.Raw)
	sans := append([]string(nil), c.DNSNames...)
	for _, ip := range c.IPAddresses {
		sans = append(sans, ip.String())
	}
	return Summary{Purpose: name, Subject: c.Subject.String(), SANs: sans, NotAfter: c.NotAfter.UTC().Format(time.RFC3339), FingerprintSHA256: hex.EncodeToString(sum[:]), Trust: trust}
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func boundedRead(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, errors.New("certificate input exceeds size limit")
	}
	return b, nil
}
func trustEntries(certs []*x509.Certificate, purpose string) []pkcs12.TrustStoreEntry {
	out := make([]pkcs12.TrustStoreEntry, 0, len(certs))
	for i, c := range certs {
		name := fmt.Sprintf("sysdev-%s-trust-%d", purpose, i+1)
		out = append(out, pkcs12.TrustStoreEntry{Cert: c, FriendlyName: name})
	}
	return out
}

func makeJKSKeyStore(alias string, keyDER []byte, chain []*x509.Certificate, password string) ([]byte, error) {
	ks := keystore.New(keystore.WithOrderedAliases())
	certs := make([]keystore.Certificate, 0, len(chain))
	for _, c := range chain {
		certs = append(certs, keystore.Certificate{Type: "X.509", Content: c.Raw})
	}
	if err := ks.SetPrivateKeyEntry(alias, keystore.PrivateKeyEntry{CreationTime: time.Now(), PrivateKey: keyDER, CertificateChain: certs}, []byte(password)); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := ks.Store(&b, []byte(password)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func makeJKSTrustStore(certs []*x509.Certificate, password string) ([]byte, error) {
	ks := keystore.New(keystore.WithOrderedAliases())
	for i, c := range certs {
		alias := fmt.Sprintf("sysdev-trust-%d", i+1)
		entry := keystore.TrustedCertificateEntry{CreationTime: time.Now(), Certificate: keystore.Certificate{Type: "X.509", Content: c.Raw}}
		if err := ks.SetTrustedCertificateEntry(alias, entry); err != nil {
			return nil, err
		}
	}
	var b bytes.Buffer
	if err := ks.Store(&b, []byte(password)); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
