package secretstore

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// Store keeps protected preparation material outside public exports. It relies
// on current-user ACLs on Windows and 0700/0600 permissions on Unix.
type Store struct {
	dir string
	mu  sync.Mutex
}

func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("protected state directory is required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create protected state directory")
	}
	if err := secureDirectory(dir); err != nil {
		return nil, fmt.Errorf("protect state directory with current-user access controls")
	}
	return &Store{dir: dir}, nil
}

// GetOrCreateSharedEncryptionKey returns the installation's stable 32-byte
// WSO2 shared encryption key. Exclusive creation makes simultaneous first use
// converge on the same persisted key rather than replacing one another.
func (s *Store) GetOrCreateSharedEncryptionKey() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, "wso2-shared-encryption-key.bin")
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("protected WSO2 encryption key has invalid length")
		}
		if err := secureExistingFile(path); err != nil {
			return nil, fmt.Errorf("protect existing encryption key")
		}
		return append([]byte(nil), key...), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read protected encryption key")
	}
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate WSO2 encryption key")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		key, err = os.ReadFile(path)
		if err != nil || len(key) != 32 {
			clear(key)
			return nil, errors.New("another process created an invalid protected encryption key")
		}
		if err := secureExistingFile(path); err != nil {
			clear(key)
			return nil, errors.New("protect concurrently created encryption key")
		}
		return append([]byte(nil), key...), nil
	}
	if err != nil {
		return nil, fmt.Errorf("create protected encryption key")
	}
	if _, err = f.Write(key); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = secureExistingFile(path)
	}
	if err != nil {
		_ = os.Remove(path)
		clear(key)
		return nil, fmt.Errorf("persist protected encryption key")
	}
	return append([]byte(nil), key...), nil
}

// Put stores a named protected value for later Stage 4 resolution. Names map
// to fixed filenames; callers cannot provide path fragments.
func (s *Store) Put(name string, value []byte) error {
	if name == "" || len(name) > 64 {
		return errors.New("invalid protected material name")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return errors.New("invalid protected material name")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, name+".secret")
	tmp, err := os.CreateTemp(s.dir, ".secret-*")
	if err != nil {
		return errors.New("create protected material temporary file")
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := secureFile(tmpName, tmp); err != nil {
		tmp.Close()
		return errors.New("protect temporary material")
	}
	if _, err := tmp.Write(value); err != nil {
		tmp.Close()
		return errors.New("write protected material")
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return errors.New("sync protected material")
	}
	if err := tmp.Close(); err != nil {
		return errors.New("close protected material")
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errors.New("atomically replace protected material")
	}
	if err := secureExistingFile(path); err != nil {
		return errors.New("protect stored material")
	}
	return nil
}

func (s *Store) Get(name string) ([]byte, error) {
	if name == "" || len(name) > 64 {
		return nil, errors.New("invalid protected material name")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return nil, errors.New("invalid protected material name")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, name+".secret")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("protected material is unavailable")
	}
	if err := secureExistingFile(path); err != nil {
		clear(b)
		return nil, errors.New("cannot verify protected material access controls")
	}
	return b, nil
}
