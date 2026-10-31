package auditlog

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestRedactsCanariesAndConcurrentWrites(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	const password = "canary-password-3492"
	const token = "canary-token-7621"
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l.Event("INFO", "test", "password="+password, map[string]string{"token": token, "worker": string(rune('A' + i))})
		}(i)
	}
	wg.Wait()
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(l.Path())
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if strings.Contains(s, password) || strings.Contains(s, token) {
		t.Fatalf("secret canary leaked into log: %s", s)
	}
	if n := strings.Count(s, "op=test"); n != 16 {
		t.Fatalf("got %d complete concurrent log events, want 16", n)
	}
	if st, err := os.Stat(l.Path()); err != nil {
		t.Fatal(err)
	} else if st.Size() == 0 {
		t.Fatal("log is empty")
	}
}

func TestRunLogUsesUniquePaths(t *testing.T) {
	dir := filepath.Clean(t.TempDir())
	a, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := New(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if a.Path() == b.Path() {
		t.Fatal("run log names are not unique")
	}
}

func TestRestrictedPermissionsAndClosedLogger(t *testing.T) {
	dir := t.TempDir()
	l, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if st, e := os.Stat(dir); e != nil {
			t.Fatal(e)
		} else if st.Mode().Perm() != 0700 {
			t.Fatalf("directory permissions %o, want 700", st.Mode().Perm())
		}
		if st, e := os.Stat(l.Path()); e != nil {
			t.Fatal(e)
		} else if st.Mode().Perm() != 0600 {
			t.Fatalf("file permissions %o, want 600", st.Mode().Perm())
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l.Event("INFO", "closed", "ignored", nil)
}
