package secretstore

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSharedEncryptionKeyPersistsAcrossOpen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "protected")
	one, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	want, err := one.GetOrCreateSharedEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	two, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := two.GetOrCreateSharedEncryptionKey()
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 32 || !bytes.Equal(want, got) {
		t.Fatal("installation encryption key was not reused")
	}
	if info, err := os.Stat(filepath.Join(dir, "wso2-shared-encryption-key.bin")); err != nil || info.Size() != 32 {
		t.Fatalf("protected key file invalid: %v", err)
	}
}

func TestProtectedMaterialCanaryIsOnlyInProtectedStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "protected")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const canary = "do-not-export-canary"
	if err := s.Put("oracle-runtime", []byte(canary)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("oracle-runtime")
	if err != nil || string(got) != canary {
		t.Fatalf("protected value mismatch: %v", err)
	}
	if _, err := s.Get("..\\public"); err == nil {
		t.Fatal("accepted path-traversal name")
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "public", "oracle-runtime.secret")); !os.IsNotExist(err) {
		t.Fatalf("protected material appeared outside state store: %v", err)
	}
}
