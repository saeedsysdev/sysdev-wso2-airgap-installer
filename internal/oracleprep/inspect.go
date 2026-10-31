package oracleprep

import (
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	oracle "github.com/oracle/go-oracledb/v26/oracle"
	"github.com/saeedsysdev/sysdev-wso2-airgap-installer/internal/config"
)

type Account struct{ Purpose, CredentialKey, Username, Password, TargetSchema string }
type Session interface {
	Query(context.Context, string, ...any) ([]string, error)
	Close() error
}
type Opener func(context.Context, config.Oracle, Account, time.Duration) (Session, error)
type Validator struct {
	Open Opener
	Now  func() time.Time
}
type Check struct {
	Account              string `json:"account"`
	Authenticated        bool   `json:"authenticated"`
	Service              string `json:"service,omitempty"`
	DatabaseVersion      string `json:"database_version,omitempty"`
	CharacterSet         string `json:"character_set,omitempty"`
	CharacterSetSuitable bool   `json:"character_set_suitable"`
	TargetSchema         string `json:"target_schema,omitempty"`
	TargetSchemaExists   bool   `json:"target_schema_exists"`
	ExistingObjectCount  int64  `json:"existing_object_count,omitempty"`
	RequiredPrivileges   string `json:"required_privileges"`
	Status               string `json:"status"`
	Detail               string `json:"detail"`
}
type Result struct {
	Status   string   `json:"status"`
	Checks   []Check  `json:"checks"`
	Deferred []string `json:"deferred,omitempty"`
	Blockers []string `json:"blockers,omitempty"`
	ReadOnly bool     `json:"read_only"`
}

// Validate issues bounded SELECT-only queries. Credential-bearing driver errors
// never escape this package; callers receive sanitized descriptions only.
func (v Validator) Validate(ctx context.Context, cfg config.Oracle, accounts []Account) Result {
	result := Result{Status: "INDETERMINATE", ReadOnly: true}
	if len(accounts) == 0 {
		result.Deferred = append(result.Deferred, "Oracle authentication not run: protected account credentials were not supplied.")
		return result
	}
	if v.Open == nil {
		v.Open = OpenDriver
	}
	if v.Now == nil {
		v.Now = time.Now
	}
	connectTimeout := time.Duration(cfg.ConnectTimeoutSeconds) * time.Second
	if connectTimeout <= 0 {
		connectTimeout = 8 * time.Second
	}
	if connectTimeout > 30*time.Second {
		connectTimeout = 30 * time.Second
	}
	totalTimeout := time.Duration(cfg.ReadTimeoutSeconds) * time.Second
	if totalTimeout <= 0 {
		totalTimeout = 60 * time.Second
	}
	if totalTimeout > 5*time.Minute {
		totalTimeout = 5 * time.Minute
	}
	for _, account := range accounts {
		check := Check{Account: account.Purpose, TargetSchema: account.TargetSchema, RequiredPrivileges: "UNVERIFIED: exact release SQL artifacts are not available"}
		versionCompatible := true
		if account.Username == "" || account.Password == "" {
			check.Status = "DEFERRED"
			check.Detail = "Protected username or password was not supplied."
			result.Deferred = append(result.Deferred, account.Purpose+" authenticated Oracle validation was not run because protected credentials are unavailable.")
			result.Checks = append(result.Checks, check)
			continue
		}
		connCtx, cancel := context.WithTimeout(ctx, totalTimeout)
		openCtx, openCancel := context.WithTimeout(connCtx, connectTimeout)
		session, err := v.Open(openCtx, cfg, account, connectTimeout)
		openCancel()
		if err != nil {
			cancel()
			check.Status = "FAILED"
			check.Detail = "Oracle authenticated connection could not be established (network, TLS, service, or credentials)."
			result.Blockers = append(result.Blockers, account.Purpose+" Oracle connection failed; the failure category is not distinguishable without a successful authenticated session.")
			result.Checks = append(result.Checks, check)
			continue
		}
		check.Authenticated = true
		read := func(q string, args ...any) ([]string, error) { return session.Query(connCtx, q, args...) }
		service, err := read("SELECT SYS_CONTEXT('USERENV','SERVICE_NAME') FROM dual")
		if err != nil || len(service) == 0 || service[0] == "" {
			closeSession(session)
			cancel()
			check.Status = "FAILED"
			check.Detail = "Oracle authentication succeeded but service identity could not be read."
			result.Blockers = append(result.Blockers, account.Purpose+" Oracle service identity is unverified.")
			result.Checks = append(result.Checks, check)
			continue
		}
		check.Service = service[0]
		if cfg.ServiceName != "" && !strings.EqualFold(cfg.ServiceName, service[0]) {
			closeSession(session)
			cancel()
			check.Status = "BLOCKED"
			check.Detail = "Authenticated session connected to a different Oracle service."
			result.Blockers = append(result.Blockers, account.Purpose+" authenticated Oracle service does not match configured service.")
			result.Checks = append(result.Checks, check)
			continue
		}
		version, err := read("SELECT VERSION FROM PRODUCT_COMPONENT_VERSION WHERE PRODUCT LIKE 'Oracle Database%' AND ROWNUM = 1")
		if err != nil || len(version) == 0 {
			closeSession(session)
			cancel()
			check.Status = "FAILED"
			check.Detail = "Oracle authentication succeeded but database version metadata is inaccessible."
			result.Blockers = append(result.Blockers, account.Purpose+" Oracle version metadata is inaccessible.")
			result.Checks = append(result.Checks, check)
			continue
		}
		check.DatabaseVersion = version[0]
		versionCompatible = strings.HasPrefix(strings.TrimSpace(version[0]), "19.")
		result.Deferred = append(result.Deferred, account.Purpose+" Oracle 19c release-update level is not proven by PRODUCT_COMPONENT_VERSION metadata.")
		charset, err := read("SELECT VALUE FROM NLS_DATABASE_PARAMETERS WHERE PARAMETER = 'NLS_CHARACTERSET'")
		if err != nil || len(charset) == 0 {
			closeSession(session)
			cancel()
			check.Status = "FAILED"
			check.Detail = "Oracle character-set metadata is inaccessible."
			result.Blockers = append(result.Blockers, account.Purpose+" Oracle character-set suitability is unverified.")
			result.Checks = append(result.Checks, check)
			continue
		}
		check.CharacterSet = charset[0]
		check.CharacterSetSuitable = strings.EqualFold(charset[0], "AL32UTF8")
		user, err := read("SELECT USER FROM dual")
		if err != nil || len(user) == 0 || !strings.EqualFold(user[0], account.Username) {
			closeSession(session)
			cancel()
			check.Status = "FAILED"
			check.Detail = "Authenticated account identity could not be confirmed."
			result.Blockers = append(result.Blockers, account.Purpose+" authenticated account identity did not match the selected account.")
			result.Checks = append(result.Checks, check)
			continue
		}
		if account.TargetSchema != "" {
			exists, err := read("SELECT USERNAME FROM ALL_USERS WHERE USERNAME = :1", strings.ToUpper(account.TargetSchema))
			if err != nil {
				closeSession(session)
				cancel()
				check.Status = "INDETERMINATE"
				check.Detail = "Account authenticated, but target schema metadata is inaccessible."
				result.Deferred = append(result.Deferred, account.Purpose+" target-schema metadata is inaccessible.")
				result.Checks = append(result.Checks, check)
				continue
			}
			check.TargetSchemaExists = len(exists) > 0
			if check.TargetSchemaExists {
				objects, err := read("SELECT COUNT(*) FROM ALL_OBJECTS WHERE OWNER = :1", strings.ToUpper(account.TargetSchema))
				if err != nil || len(objects) == 0 {
					closeSession(session)
					cancel()
					check.Status = "INDETERMINATE"
					check.Detail = "Target schema exists, but object inventory is inaccessible."
					result.Deferred = append(result.Deferred, account.Purpose+" schema object inventory is inaccessible.")
					result.Checks = append(result.Checks, check)
					continue
				}
				check.ExistingObjectCount, _ = strconv.ParseInt(objects[0], 10, 64)
			}
		}
		privileges, err := read("SELECT PRIVILEGE FROM SESSION_PRIVS")
		if err != nil {
			check.RequiredPrivileges = "INDETERMINATE: SESSION_PRIVS is inaccessible"
			result.Deferred = append(result.Deferred, account.Purpose+" privilege metadata is inaccessible.")
		} else {
			_ = privileges
			check.RequiredPrivileges = "OBSERVED: session privileges recorded; exact script requirements remain unverified"
		}
		closeSession(session)
		cancel()
		check.Status = "PASSED"
		if !versionCompatible {
			check.Status = "BLOCKED"
			check.Detail = "The authenticated database version does not match the configured Oracle 19c profile."
			result.Blockers = append(result.Blockers, account.Purpose+" Oracle version is outside the 19c profile.")
		}
		if !check.CharacterSetSuitable {
			check.Status = "BLOCKED"
			check.Detail = "Authenticated Oracle database is reachable, but its character set is not AL32UTF8."
			result.Blockers = append(result.Blockers, account.Purpose+" Oracle database character set is not AL32UTF8.")
		}
		if cfg.SchemaMode == "initialize" && check.TargetSchemaExists && check.ExistingObjectCount > 0 {
			check.Status = "BLOCKED"
			check.Detail = "Initialization mode selected for a schema containing objects; refusing to rerun DDL."
			result.Blockers = append(result.Blockers, account.Purpose+" initialization refused because target schema contains existing objects.")
		}
		if cfg.SchemaMode == "initialize" && !check.TargetSchemaExists {
			check.Status = "BLOCKED"
			check.Detail = "Selected schema account/schema is absent; this preparation flow will not create it."
			result.Blockers = append(result.Blockers, account.Purpose+" schema is absent; creating users is outside preflight authorization.")
		}
		result.Checks = append(result.Checks, check)
	}
	if len(result.Blockers) > 0 {
		result.Status = "BLOCKED"
	} else if len(result.Checks) > 0 && len(result.Deferred) == 0 && allPassed(result.Checks) {
		result.Status = "PASSED"
	} else {
		result.Status = "INDETERMINATE"
	}
	if cfg.SchemaMode == "initialize" {
		result.Deferred = append(result.Deferred, "Oracle DDL is not executed during preparation; Stage 4 must run only the verified release scripts after Apply.")
	}
	return result
}

func allPassed(checks []Check) bool {
	for _, c := range checks {
		if c.Status != "PASSED" {
			return false
		}
	}
	return true
}

func OpenDriver(ctx context.Context, cfg config.Oracle, account Account, timeout time.Duration) (Session, error) {
	driverCfg := oracle.NewOracleDriverConfig()
	driverCfg.Credentials.User = account.Username
	driverCfg.Credentials.Password = account.Password
	mode := strings.ToLower(cfg.ConnectionMode)
	if mode == "" {
		mode = "tcp"
	}
	var descriptor string
	if cfg.ConnectDescriptor != "" {
		descriptor = cfg.ConnectDescriptor
	} else {
		scheme := mode
		descriptor = fmt.Sprintf("%s://%s:%d/%s", scheme, cfg.Host, cfg.Port, url.PathEscape(cfg.ServiceName))
	}
	walletPath := ""
	if mode == "tcps" {
		walletPath = cfg.WalletPath
		if walletPath == "" && cfg.TrustBundlePath != "" {
			var err error
			walletPath, err = walletFromTrustBundle(cfg.TrustBundlePath)
			if err != nil {
				return nil, err
			}
		}
		driverCfg.ConnectionProperties.WalletLocation = walletPath
		driverCfg.ConnectionProperties.SSLServerCertDN = cfg.ServerCertificateDN
		driverCfg.ConnectionProperties.SSLServerDNMatch = true
	}
	driverCfg.ConnectionProperties.ConnectTimeout = int(timeout.Seconds())
	driverCfg.ConnectionProperties.TransportConnectTimeout = int(timeout.Seconds())
	driverCfg.ConnectDescriptor = descriptor
	connector, err := oracle.NewOracleConnector(driverCfg)
	if err != nil {
		return nil, errors.New("Oracle driver rejected the connection configuration")
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	db.SetConnMaxLifetime(time.Second)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		if isTemporaryWallet(walletPath) {
			_ = os.RemoveAll(walletPath)
		}
		return nil, errors.New("Oracle authenticated connection failed")
	}
	cleanup := ""
	if isTemporaryWallet(walletPath) {
		cleanup = walletPath
	}
	return sqlSession{db: db, cleanup: cleanup}, nil
}

func isTemporaryWallet(path string) bool {
	return path != "" && strings.HasPrefix(path, os.TempDir()+string(os.PathSeparator)+"sysdev-oracle-wallet-")
}

type sqlSession struct {
	db      *sql.DB
	cleanup string
}

func (s sqlSession) Query(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, errors.New("read-only Oracle metadata query failed")
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var x any
		if err := rows.Scan(&x); err != nil {
			return nil, errors.New("read-only Oracle metadata decode failed")
		}
		switch v := x.(type) {
		case nil:
			out = append(out, "")
		case []byte:
			out = append(out, string(v))
		default:
			out = append(out, fmt.Sprint(v))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("read-only Oracle metadata query failed")
	}
	return out, nil
}
func (s sqlSession) Close() error {
	err := s.db.Close()
	if s.cleanup != "" {
		_ = os.RemoveAll(s.cleanup)
	}
	return err
}
func closeSession(s Session) { _ = s.Close() }

func walletFromTrustBundle(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", errors.New("Oracle trust bundle is unavailable")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		return "", errors.New("Oracle trust bundle contains no PEM certificates")
	}
	dir, err := os.MkdirTemp("", "sysdev-oracle-wallet-")
	if err != nil {
		return "", errors.New("create temporary Oracle trust wallet")
	}
	if err := os.Chmod(dir, 0700); err != nil {
		os.RemoveAll(dir)
		return "", errors.New("protect temporary Oracle trust wallet")
	}
	if err := os.WriteFile(filepath.Join(dir, "ewallet.pem"), b, 0600); err != nil {
		os.RemoveAll(dir)
		return "", errors.New("prepare Oracle PEM wallet")
	}
	return dir, nil
}
