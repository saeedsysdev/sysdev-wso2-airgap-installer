package oracleprep

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
)

type fixtureSession struct {
	values  map[string][]string
	queries []string
	closed  bool
}

func (s *fixtureSession) Query(_ context.Context, q string, _ ...any) ([]string, error) {
	s.queries = append(s.queries, q)
	if !strings.HasPrefix(strings.TrimSpace(strings.ToUpper(q)), "SELECT ") {
		return nil, errors.New("fixture rejected non-read query")
	}
	v, ok := s.values[q]
	if !ok {
		return nil, errors.New("metadata unavailable")
	}
	return v, nil
}
func (s *fixtureSession) Close() error { s.closed = true; return nil }

func TestValidateAuthenticatedReadOnlyOracleMetadata(t *testing.T) {
	s := &fixtureSession{values: map[string][]string{
		"SELECT SYS_CONTEXT('USERENV','SERVICE_NAME') FROM dual":                                             {"APIMSVC"},
		"SELECT VERSION FROM PRODUCT_COMPONENT_VERSION WHERE PRODUCT LIKE 'Oracle Database%' AND ROWNUM = 1": {"19.0.0.0.0"},
		"SELECT VALUE FROM NLS_DATABASE_PARAMETERS WHERE PARAMETER = 'NLS_CHARACTERSET'":                     {"AL32UTF8"},
		"SELECT USER FROM dual":                              {"WSO2_APIM"},
		"SELECT USERNAME FROM ALL_USERS WHERE USERNAME = :1": {"WSO2_APIM"},
		"SELECT COUNT(*) FROM ALL_OBJECTS WHERE OWNER = :1":  {"0"},
		"SELECT PRIVILEGE FROM SESSION_PRIVS":                {"CREATE SESSION", "CREATE TABLE"},
	}}
	var gotAccount Account
	v := Validator{Open: func(_ context.Context, _ config.Oracle, a Account, d time.Duration) (Session, error) {
		gotAccount = a
		if d <= 0 || d > 30*time.Second {
			t.Fatalf("timeout not bounded: %v", d)
		}
		return s, nil
	}}
	result := v.Validate(context.Background(), config.Oracle{ConnectionMode: "tcp", ServiceName: "APIMSVC", SchemaMode: "initialize"}, []Account{{Purpose: "APIM runtime", Username: "WSO2_APIM", Password: "canary-password", TargetSchema: "WSO2_APIM"}})
	if !result.ReadOnly || len(result.Checks) != 1 || !result.Checks[0].Authenticated || result.Checks[0].Status != "PASSED" || result.Checks[0].ExistingObjectCount != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if gotAccount.Password != "canary-password" {
		t.Fatal("credential was not passed to connection provider")
	}
	if !s.closed {
		t.Fatal("session was not closed")
	}
	for _, q := range s.queries {
		if !strings.HasPrefix(strings.TrimSpace(strings.ToUpper(q)), "SELECT ") {
			t.Fatalf("non-read-only statement observed: %s", q)
		}
	}
}

func TestValidateBlocksUnsafeOrUnsupportedDatabaseState(t *testing.T) {
	for _, tc := range []struct {
		name, version, charset, service string
		objects                         string
		mode                            string
		want                            string
	}{
		{"wrong version", "18.0.0.0.0", "AL32UTF8", "APIMSVC", "0", "prepared", "BLOCKED"},
		{"wrong charset", "19.0.0.0.0", "WE8MSWIN1252", "APIMSVC", "0", "prepared", "BLOCKED"},
		{"wrong service", "19.0.0.0.0", "AL32UTF8", "OTHER", "0", "prepared", "BLOCKED"},
		{"existing schema objects", "19.0.0.0.0", "AL32UTF8", "APIMSVC", "9", "initialize", "BLOCKED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &fixtureSession{values: map[string][]string{
				"SELECT SYS_CONTEXT('USERENV','SERVICE_NAME') FROM dual":                                             {tc.service},
				"SELECT VERSION FROM PRODUCT_COMPONENT_VERSION WHERE PRODUCT LIKE 'Oracle Database%' AND ROWNUM = 1": {tc.version},
				"SELECT VALUE FROM NLS_DATABASE_PARAMETERS WHERE PARAMETER = 'NLS_CHARACTERSET'":                     {tc.charset},
				"SELECT USER FROM dual":                              {"WSO2_APIM"},
				"SELECT USERNAME FROM ALL_USERS WHERE USERNAME = :1": {"WSO2_APIM"},
				"SELECT COUNT(*) FROM ALL_OBJECTS WHERE OWNER = :1":  {tc.objects},
				"SELECT PRIVILEGE FROM SESSION_PRIVS":                {"CREATE SESSION"},
			}}
			v := Validator{Open: func(context.Context, config.Oracle, Account, time.Duration) (Session, error) { return s, nil }}
			r := v.Validate(context.Background(), config.Oracle{ServiceName: "APIMSVC", SchemaMode: tc.mode}, []Account{{Purpose: "APIM", Username: "WSO2_APIM", Password: "secret", TargetSchema: "WSO2_APIM"}})
			if r.Status != tc.want {
				t.Fatalf("status %s, want %s: %#v", r.Status, tc.want, r)
			}
		})
	}
}

func TestOracleErrorsDoNotExposeCredentials(t *testing.T) {
	const canary = "oracle-password-canary"
	v := Validator{Open: func(context.Context, config.Oracle, Account, time.Duration) (Session, error) {
		return nil, errors.New(canary)
	}}
	r := v.Validate(context.Background(), config.Oracle{}, []Account{{Purpose: "APIM", Username: "user", Password: canary}})
	if strings.Contains(strings.Join(r.Blockers, " ")+r.Checks[0].Detail, canary) {
		t.Fatal("Oracle validation leaked credential")
	}
	if r.Status != "BLOCKED" {
		t.Fatalf("connection failure not blocked: %#v", r)
	}
}

func TestMissingCredentialsAreDeferred(t *testing.T) {
	r := (Validator{}).Validate(context.Background(), config.Oracle{}, []Account{{Purpose: "APIM"}})
	if r.Status != "INDETERMINATE" || len(r.Deferred) == 0 {
		t.Fatalf("missing credentials should be explicit deferred: %#v", r)
	}
}
