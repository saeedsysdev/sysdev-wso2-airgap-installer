package schemaartifacts

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyOfflineOracleScriptsAndProvenance(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "wso2am-4.7.0.zip")
	script := []byte("-- fixture only\nCREATE TABLE EXAMPLE (ID NUMBER);\n")
	if err := writeZip(source, map[string][]byte{"wso2am-4.7.0/dbscripts/oracle.sql": script}); err != nil {
		t.Fatal(err)
	}
	archive, _ := os.ReadFile(source)
	sh := sha256.Sum256(archive)
	sqlh := sha256.Sum256(script)
	m := Manifest{SchemaVersion: 1, Product: "wso2-apim", ProductVersion: "4.7.0", SourceArchive: filepath.Base(source), SourceSHA256: hex.EncodeToString(sh[:]), SourceSize: int64(len(archive)), SourceIdentity: filepath.Base(source), Scripts: []Script{{Path: "wso2am-4.7.0/dbscripts/oracle.sql", SHA256: hex.EncodeToString(sqlh[:]), TargetSchema: "WSO2_APIM", Order: 1, RequiredPrivileges: []string{"CREATE TABLE"}}}}
	manifest := writeManifest(t, dir, m)
	r, err := Verify(context.Background(), manifest, "4.7.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Status, "PROVENANCE UNVERIFIED") || len(r.Scripts) != 1 || r.Scripts[0].TargetSchema != "WSO2_APIM" || len(r.Deferred) == 0 {
		t.Fatalf("unexpected result: %#v", r)
	}
}

func TestVerifyRejectsCorruptMissingUnsafeAndWrongVersion(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		mutate     func(*Manifest)
		version    string
		corrupt    bool
		want       string
	}{
		{"unsafe path", "../oracle.sql", nil, "4.7.0", false, "unsafe"},
		{"missing script", "missing/oracle.sql", nil, "4.7.0", false, "missing"},
		{"wrong product version", "wso2/dbscripts/oracle.sql", nil, "4.8.0", false, "version"},
		{"wrong script digest", "wso2/dbscripts/oracle.sql", func(m *Manifest) { m.Scripts[0].SHA256 = strings.Repeat("0", 64) }, "4.7.0", false, "hash"},
		{"corrupt archive", "wso2/dbscripts/oracle.sql", nil, "4.7.0", true, "SHA-256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "release.zip")
			if e := writeZip(source, map[string][]byte{"wso2/dbscripts/oracle.sql": []byte("SELECT 1 FROM dual;\n")}); e != nil {
				t.Fatal(e)
			}
			raw, _ := os.ReadFile(source)
			sh := sha256.Sum256(raw)
			sql := []byte("SELECT 1 FROM dual;\n")
			qh := sha256.Sum256(sql)
			m := Manifest{SchemaVersion: 1, Product: "wso2-apim", ProductVersion: "4.7.0", SourceArchive: "release.zip", SourceSHA256: hex.EncodeToString(sh[:]), SourceSize: int64(len(raw)), SourceIdentity: "release.zip", Scripts: []Script{{Path: tc.path, SHA256: hex.EncodeToString(qh[:]), TargetSchema: "WSO2_SHARED", Order: 1}}}
			if tc.mutate != nil {
				tc.mutate(&m)
			}
			p := writeManifest(t, dir, m)
			if tc.corrupt {
				f, e := os.OpenFile(source, os.O_WRONLY, 0)
				if e != nil {
					t.Fatal(e)
				}
				_, _ = f.WriteAt([]byte("X"), 0)
				f.Close()
			}
			_, e := Verify(context.Background(), p, tc.version)
			if e == nil {
				t.Fatal("invalid artifact passed verification")
			}
		})
	}
}

func TestVerifyRejectsZipSlipAndDuplicateEntry(t *testing.T) {
	for name, entries := range map[string][][2]string{
		"zip-slip":  {{"../outside.sql", "x"}},
		"duplicate": {{"db/oracle.sql", "x"}, {"db/oracle.sql", "y"}},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "release.zip")
			f, e := os.Create(source)
			if e != nil {
				t.Fatal(e)
			}
			zw := zip.NewWriter(f)
			for _, entry := range entries {
				w, e := zw.Create(entry[0])
				if e != nil {
					t.Fatal(e)
				}
				_, _ = w.Write([]byte(entry[1]))
			}
			_ = zw.Close()
			_ = f.Close()
			raw, _ := os.ReadFile(source)
			sh := sha256.Sum256(raw)
			m := Manifest{SchemaVersion: 1, Product: "wso2-apim", ProductVersion: "4.7.0", SourceArchive: "release.zip", SourceSHA256: hex.EncodeToString(sh[:]), SourceSize: int64(len(raw)), SourceIdentity: "release.zip", Scripts: []Script{{Path: "db/oracle.sql", SHA256: strings.Repeat("0", 64), TargetSchema: "WSO2_APIM", Order: 1}}}
			_, err := Verify(context.Background(), writeManifest(t, dir, m), "4.7.0")
			if err == nil {
				t.Fatal("unsafe archive passed verification")
			}
		})
	}
}

func writeZip(path string, files map[string][]byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	z := zip.NewWriter(f)
	for name, data := range files {
		w, err := z.Create(name)
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	if err := z.Close(); err != nil {
		return err
	}
	return f.Close()
}
func writeManifest(t *testing.T, dir string, m Manifest) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "schema-artifacts.json")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
