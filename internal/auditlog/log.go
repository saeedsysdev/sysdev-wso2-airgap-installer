package auditlog

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

var (
	secretPattern = regexp.MustCompile(`(?i)(password|token|secret|authorization|private[_-]?key|kubeconfig)(\s*[:=]\s*)([^\s,;]+)`)
	urlCredential = regexp.MustCompile(`(https?://)[^/@\s]+:[^/@\s]+@`)
)

type Logger struct {
	mu     sync.Mutex
	f      *os.File
	path   string
	runID  string
	failed error
}

func DefaultPath(override string) (string, error) {
	var dir string
	if override != "" {
		dir = override
	} else if runtime.GOOS == "windows" {
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			base, _ = os.UserConfigDir()
		}
		if base == "" {
			return "", fmt.Errorf("LocalAppData is unavailable; pass --log-dir")
		}
		dir = filepath.Join(base, "SaeedSysDev", "Sysdev Wso2 Airgap Installer", "logs")
	} else {
		base := os.Getenv("XDG_STATE_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("cannot determine home for log directory: %w", err)
			}
			base = filepath.Join(home, ".local", "state")
		}
		dir = filepath.Join(base, "sysdev-wso2-airgap-installer", "logs")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return abs, nil
}

func New(dir string) (*Logger, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := secureDirectory(dir); err != nil {
		return nil, fmt.Errorf("secure log directory: %w", err)
	}
	name := "run-" + time.Now().Format("20060102-150405.000000000") + ".log"
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err := secureFile(path, f); err != nil {
		f.Close()
		return nil, fmt.Errorf("secure audit log file: %w", err)
	}
	var id [8]byte
	if _, err := rand.Read(id[:]); err != nil {
		f.Close()
		return nil, fmt.Errorf("create unique run ID: %w", err)
	}
	l := &Logger{f: f, path: path, runID: hex.EncodeToString(id[:])}
	l.Event("INFO", "run", "Audit log initialized", nil)
	if err := l.Error(); err != nil {
		f.Close()
		return nil, fmt.Errorf("write initial audit event: %w", err)
	}
	return l, nil
}

func (l *Logger) Path() string { return l.path }

func (l *Logger) Event(level, operation, message string, fields map[string]string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failed != nil || l.f == nil {
		return
	}
	var b strings.Builder
	b.WriteString(time.Now().Format(time.RFC3339Nano))
	b.WriteString(" ")
	b.WriteString(strings.ToUpper(level))
	b.WriteString(" run=")
	b.WriteString(l.runID)
	b.WriteString(" op=")
	b.WriteString(safe(operation))
	b.WriteString(" message=")
	b.WriteString(quote(safe(message)))
	for k, v := range fields {
		b.WriteString(" ")
		b.WriteString(safe(k))
		b.WriteString("=")
		b.WriteString(quote(safeField(k, v)))
	}
	b.WriteByte('\n')
	_, l.failed = l.f.WriteString(b.String())
	if l.failed == nil {
		l.failed = l.f.Sync()
	}
}

func (l *Logger) Error() error { l.mu.Lock(); defer l.mu.Unlock(); return l.failed }
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return l.failed
	}
	if err := l.f.Sync(); err != nil && l.failed == nil {
		l.failed = err
	}
	if err := l.f.Close(); err != nil && l.failed == nil {
		l.failed = err
	}
	l.f = nil
	return l.failed
}

func Redact(s string) string {
	s = urlCredential.ReplaceAllString(s, "$1[REDACTED]@")
	return secretPattern.ReplaceAllString(s, "$1$2[REDACTED]")
}
func safeField(k, v string) string {
	lk := strings.ToLower(k)
	if strings.Contains(lk, "password") || strings.Contains(lk, "token") || strings.Contains(lk, "secret") || strings.Contains(lk, "authorization") || strings.Contains(lk, "key") || strings.Contains(lk, "kubeconfig") {
		return "[REDACTED]"
	}
	return Redact(v)
}
func safe(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(Redact(s), "\n", " "), "\r", " ")
}
func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"` }

// URL redaction is kept here as a boundary helper so providers never log credential-bearing references.
func RedactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return Redact(raw)
	}
	if u.User != nil {
		u.User = url.UserPassword("[REDACTED]", "[REDACTED]")
	}
	return u.String()
}
