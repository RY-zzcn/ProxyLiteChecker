package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

type store struct {
	db   *sql.DB
	path string
}

// proxyRecord keeps the flat v0.3.x API fields for compatibility. From v0.4.0
// onward those quality fields are populated from Probe/TargetState; proxies and
// proxy_checks remain rollback shadows and are not authoritative reads.
type proxyRecord struct {
	ID               int                           `json:"id"`
	ProxyKey         string                        `json:"proxy_key"`
	IP               string                        `json:"ip"`
	Port             int                           `json:"port"`
	Protocol         string                        `json:"protocol"`
	Username         *string                       `json:"username,omitempty"`
	Password         *string                       `json:"-"`
	Source           string                        `json:"source"`
	Status           string                        `json:"status"`
	Grade            string                        `json:"grade"`
	LatencyMS        *int                          `json:"latency_ms,omitempty"`
	ExitIP           *string                       `json:"exit_ip,omitempty"`
	Country          *string                       `json:"country,omitempty"`
	CountryName      *string                       `json:"country_name,omitempty"`
	ContinentCode    *string                       `json:"continent_code,omitempty"`
	IPType           *string                       `json:"ip_type,omitempty"`
	ASNOrg           *string                       `json:"asn_org,omitempty"`
	GeoSource        *string                       `json:"geo_source,omitempty"`
	GeoUpdatedAt     *string                       `json:"geo_updated_at,omitempty"`
	SuccessRate      float64                       `json:"success_rate"`
	TargetProfile    string                        `json:"target_profile"`
	DetectedProtocol *string                       `json:"detected_protocol,omitempty"`
	ServiceReachable bool                          `json:"service_reachable"`
	APIReachable     *bool                         `json:"api_reachable,omitempty"`
	CloudflareStatus *string                       `json:"cloudflare_status,omitempty"`
	RecommendedUse   string                        `json:"recommended_use"`
	LastError        *string                       `json:"last_error,omitempty"`
	FailureReason    string                        `json:"failure_reason,omitempty"`
	FailureCount     int                           `json:"failure_count"`
	CreatedAt        string                        `json:"created_at"`
	UpdatedAt        string                        `json:"updated_at"`
	LastCheckedAt    *string                       `json:"last_checked_at,omitempty"`
	Probe            *proxyProbeState              `json:"probe,omitempty"`
	TargetState      *proxyTargetState             `json:"target_state,omitempty"`
	TargetSummary    map[string]proxyTargetSummary `json:"target_summary,omitempty"`
}

type proxyProbeState struct {
	ProxyID             int     `json:"proxy_id"`
	Status              string  `json:"status"`
	StatusChangedAt     string  `json:"status_changed_at"`
	DetectedProtocol    *string `json:"detected_protocol,omitempty"`
	BaseReachable       bool    `json:"base_reachable"`
	ExitIP              *string `json:"exit_ip,omitempty"`
	LatencyMS           *int    `json:"latency_ms,omitempty"`
	SuccessRate         float64 `json:"success_rate"`
	Country             *string `json:"country,omitempty"`
	CountryName         *string `json:"country_name,omitempty"`
	ContinentCode       *string `json:"continent_code,omitempty"`
	IPType              *string `json:"ip_type,omitempty"`
	ASNOrg              *string `json:"asn_org,omitempty"`
	GeoSource           *string `json:"geo_source,omitempty"`
	GeoUpdatedAt        *string `json:"geo_updated_at,omitempty"`
	FailureReason       string  `json:"failure_reason,omitempty"`
	LastError           *string `json:"last_error,omitempty"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	CheckedAt           *string `json:"checked_at,omitempty"`
	UpdatedAt           string  `json:"updated_at"`
}

type proxyTargetState struct {
	ProxyID             int     `json:"proxy_id"`
	TargetProfile       string  `json:"target_profile"`
	Status              string  `json:"status"`
	StatusChangedAt     string  `json:"status_changed_at"`
	Capability          string  `json:"capability"`
	ServiceReachable    bool    `json:"service_reachable"`
	APIReachable        *bool   `json:"api_reachable,omitempty"`
	LatencyMS           *int    `json:"latency_ms,omitempty"`
	SuccessRate         float64 `json:"success_rate"`
	Grade               string  `json:"grade"`
	CloudflareStatus    *string `json:"cloudflare_status,omitempty"`
	RecommendedUse      string  `json:"recommended_use"`
	FailureReason       string  `json:"failure_reason,omitempty"`
	LastError           *string `json:"last_error,omitempty"`
	ConsecutiveFailures int     `json:"consecutive_failures"`
	CheckedAt           *string `json:"checked_at,omitempty"`
	UpdatedAt           string  `json:"updated_at"`
}

type proxyTargetSummary struct {
	Status     string `json:"status"`
	Capability string `json:"capability"`
}

const stateModelMigrationVersion = 400001
const taskSchedulerMigrationVersion = 401001

type parsedProxy struct {
	Host     string
	Port     int
	Protocol string
	Username *string
	Password *string
}

type proxyFilter struct {
	Status        string
	TargetProfile string
	Query         string
	Countries     []string
	Limit         int
	Offset        int
}

type availableProxyFilter struct {
	TargetProfile string
	Limit         int
	Countries     []string
	CountryPolicy string
}

const (
	gatewayCountryPolicyStrict      = "strict"
	gatewayCountryPolicyFallbackAny = "fallback_any"
)

var proxyPattern = regexp.MustCompile(`(?i)(?:(https?|socks4|socks5h?)://)?(?:(?P<user>[^:\s/@]+)(?::(?P<pass>[^@\s/]+))?@)?(?P<host>(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?|\d{1,3}(?:\.\d{1,3}){3}):(?P<port>\d{1,5})`)

func openStore(databaseURL string) (*store, error) {
	path, ok := sqlitePathFromDatabaseURL(databaseURL)
	if !ok {
		return nil, fmt.Errorf("unsupported DATABASE_URL %q", databaseURL)
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}
	db, err := sql.Open("sqlite", sqliteOpenDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(5)
	if _, err := db.Exec("PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if path != ":memory:" {
		if _, err := db.Exec("PRAGMA journal_mode = WAL"); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return &store{db: db, path: path}, nil
}

func sqlitePathFromDatabaseURL(databaseURL string) (string, bool) {
	value := strings.TrimSpace(databaseURL)
	if value == "" {
		return "", false
	}
	if strings.HasPrefix(value, "sqlite:///") {
		return strings.TrimPrefix(value, "sqlite:///"), true
	}
	if strings.HasPrefix(value, "sqlite://") {
		return strings.TrimPrefix(value, "sqlite://"), true
	}
	return value, !strings.Contains(value, "://")
}

func sqliteOpenDSN(path string) string {
	if path == ":memory:" {
		return path
	}
	options := "_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	if strings.HasPrefix(path, "file:") {
		if strings.Contains(path, "?") {
			return path + "&" + options
		}
		return path + "?" + options
	}
	return "file:" + path + "?" + options
}

func (s *store) EnsureSchema(adminUsername string, adminPassword string) error {
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS users (
  username TEXT PRIMARY KEY,
  password_hash TEXT NOT NULL,
  is_active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT (datetime('now', '+8 hours')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now', '+8 hours'))
);
CREATE TABLE IF NOT EXISTS proxies (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  proxy_key TEXT NOT NULL UNIQUE,
  ip TEXT NOT NULL,
  port INTEGER NOT NULL,
  protocol TEXT NOT NULL,
  username TEXT,
  password TEXT,
  source TEXT NOT NULL DEFAULT 'manual',
  status TEXT NOT NULL DEFAULT 'untested',
  status_changed_at TEXT,
  grade TEXT NOT NULL DEFAULT '',
  latency_ms INTEGER,
  exit_ip TEXT,
  country TEXT,
  country_name TEXT,
  continent_code TEXT,
  ip_type TEXT,
  asn_org TEXT,
  geo_source TEXT,
  geo_updated_at TEXT,
  success_rate REAL NOT NULL DEFAULT 0,
  target_profile TEXT NOT NULL DEFAULT 'generic',
  detected_protocol TEXT,
  service_reachable INTEGER NOT NULL DEFAULT 0,
  api_reachable INTEGER,
  cloudflare_status TEXT,
  recommended_use TEXT NOT NULL DEFAULT '',
  last_error TEXT,
  failure_count INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL DEFAULT (datetime('now', '+8 hours')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now', '+8 hours')),
  first_seen_at TEXT,
  last_seen_at TEXT,
  last_checked_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_proxies_status ON proxies(status);
CREATE INDEX IF NOT EXISTS idx_proxies_target ON proxies(target_profile);
CREATE INDEX IF NOT EXISTS idx_proxies_source ON proxies(source);
CREATE INDEX IF NOT EXISTS idx_proxies_country ON proxies(country);
CREATE INDEX IF NOT EXISTS idx_proxies_quality ON proxies(status, grade, latency_ms);
CREATE TABLE IF NOT EXISTS proxy_checks (
  proxy_id INTEGER NOT NULL,
  target_profile TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'untested',
  status_changed_at TEXT,
  grade TEXT NOT NULL DEFAULT '',
  latency_ms INTEGER,
  exit_ip TEXT,
  country TEXT,
  country_name TEXT,
  continent_code TEXT,
  ip_type TEXT,
  asn_org TEXT,
  geo_source TEXT,
  geo_updated_at TEXT,
  success_rate REAL NOT NULL DEFAULT 0,
  detected_protocol TEXT,
  service_reachable INTEGER NOT NULL DEFAULT 0,
  api_reachable INTEGER,
  cloudflare_status TEXT,
  recommended_use TEXT NOT NULL DEFAULT '',
  last_error TEXT,
  checked_at TEXT,
  updated_at TEXT NOT NULL DEFAULT (datetime('now', '+8 hours')),
  PRIMARY KEY (proxy_id, target_profile),
  FOREIGN KEY (proxy_id) REFERENCES proxies(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_proxy_checks_target_status ON proxy_checks(target_profile, status);
CREATE INDEX IF NOT EXISTS idx_proxy_checks_target_country_status ON proxy_checks(target_profile, country, status);
CREATE TABLE IF NOT EXISTS source_health (
  source_id TEXT PRIMARY KEY,
  last_fetch_at TEXT,
  last_fetch_status TEXT NOT NULL DEFAULT '',
  last_imported INTEGER NOT NULL DEFAULT 0,
  last_new INTEGER NOT NULL DEFAULT 0,
  last_updated INTEGER NOT NULL DEFAULT 0,
  failure_streak INTEGER NOT NULL DEFAULT 0,
  disabled_until TEXT,
  last_error TEXT,
  updated_at TEXT NOT NULL DEFAULT (datetime('now', '+8 hours'))
);
CREATE TABLE IF NOT EXISTS maintenance_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  action TEXT NOT NULL,
  count INTEGER NOT NULL DEFAULT 0,
  target_profile TEXT NOT NULL DEFAULT '',
  reason TEXT NOT NULL DEFAULT '',
  settings_snapshot TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (datetime('now', '+8 hours'))
);
CREATE INDEX IF NOT EXISTS idx_maintenance_events_created ON maintenance_events(created_at DESC);
INSERT OR IGNORE INTO proxy_checks (
  proxy_id, target_profile, status, grade, latency_ms, exit_ip, country, ip_type, asn_org,
  success_rate, detected_protocol, service_reachable, api_reachable, cloudflare_status,
  recommended_use, last_error, checked_at, updated_at
)
SELECT id, target_profile, status, grade, latency_ms, exit_ip, country, ip_type, asn_org,
       success_rate, detected_protocol, service_reachable, api_reachable, cloudflare_status,
       recommended_use, last_error, last_checked_at, updated_at
FROM proxies
WHERE last_checked_at IS NOT NULL AND target_profile != '';
`); err != nil {
		return err
	}
	if err := s.addMissingColumns(); err != nil {
		return err
	}
	if err := s.normalizeStoredProxyState(); err != nil {
		return err
	}
	if err := s.applySchemaMigrations(); err != nil {
		return err
	}
	username := firstNonEmpty(adminUsername, "admin")
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users WHERE username = ?", username).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		_, err := s.db.Exec(
			"INSERT INTO users (username, password_hash, created_at, updated_at) VALUES (?, ?, datetime('now', '+8 hours'), datetime('now', '+8 hours'))",
			username,
			hashPassword(firstNonEmpty(adminPassword, defaultAdminPassword)),
		)
		return err
	}
	return nil
}

func (s *store) addMissingColumns() error {
	additions := map[string]map[string]string{
		"proxies": {
			"country_name":      "TEXT",
			"continent_code":    "TEXT",
			"geo_source":        "TEXT",
			"geo_updated_at":    "TEXT",
			"status_changed_at": "TEXT",
			"first_seen_at":     "TEXT",
			"last_seen_at":      "TEXT",
		},
		"proxy_checks": {
			"country_name":      "TEXT",
			"continent_code":    "TEXT",
			"geo_source":        "TEXT",
			"geo_updated_at":    "TEXT",
			"status_changed_at": "TEXT",
		},
	}
	for table, columns := range additions {
		for column, definition := range columns {
			exists, err := s.columnExists(table, column)
			if err != nil {
				return err
			}
			if exists {
				continue
			}
			if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition)); err != nil {
				if !strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
					return err
				}
			}
		}
	}
	_, err := s.db.Exec(`
CREATE INDEX IF NOT EXISTS idx_proxies_country ON proxies(country);
CREATE INDEX IF NOT EXISTS idx_proxy_checks_target_country_status ON proxy_checks(target_profile, country, status);
UPDATE proxies
SET first_seen_at = COALESCE(first_seen_at, created_at, updated_at, datetime('now', '+8 hours')),
    last_seen_at = COALESCE(last_seen_at, updated_at, created_at, datetime('now', '+8 hours'));
`)
	return err
}

func (s *store) applySchemaMigrations() error {
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS schema_migrations (
  version INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  applied_at TEXT NOT NULL,
  app_version TEXT NOT NULL
)`); err != nil {
		return err
	}
	var applied int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", stateModelMigrationVersion).Scan(&applied); err != nil {
		return err
	}
	if applied == 0 {
		if err := s.applyStateModelMigration(); err != nil {
			return err
		}
		log.Printf("database migration applied: version=%d name=v0.4.0_state_model", stateModelMigrationVersion)
	}
	if err := s.applyTaskSchedulerMigrationIfNeeded(); err != nil {
		return err
	}
	version, err := s.SchemaVersion()
	if err != nil {
		return err
	}
	log.Printf("database schema version: %d", version)
	return nil
}

func (s *store) applyTaskSchedulerMigrationIfNeeded() error {
	var applied int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", taskSchedulerMigrationVersion).Scan(&applied); err != nil {
		return err
	}
	if applied > 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.Exec(`
CREATE TABLE job_runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  type TEXT NOT NULL,
  trigger TEXT NOT NULL DEFAULT 'manual',
  trigger_reason TEXT NOT NULL DEFAULT '',
  task_key TEXT NOT NULL DEFAULT '',
  parent_job_id INTEGER,
  status TEXT NOT NULL,
  params_json TEXT NOT NULL DEFAULT '{}',
  done INTEGER NOT NULL DEFAULT 0,
  total INTEGER NOT NULL DEFAULT 0,
  success INTEGER NOT NULL DEFAULT 0,
  failed INTEGER NOT NULL DEFAULT 0,
  message TEXT NOT NULL DEFAULT '',
  error_code TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  result_json TEXT NOT NULL DEFAULT '{}',
  instance_id TEXT NOT NULL,
  started_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  finished_at TEXT,
  FOREIGN KEY (parent_job_id) REFERENCES job_runs(id)
);
CREATE INDEX idx_job_runs_status_id ON job_runs(status, id DESC);
CREATE INDEX idx_job_runs_type_id ON job_runs(type, id DESC);
CREATE INDEX idx_job_runs_task_key_id ON job_runs(task_key, id DESC);
CREATE TABLE scheduler_state (
  task_key TEXT PRIMARY KEY,
  next_due_at TEXT,
  last_started_at TEXT,
  last_finished_at TEXT,
  last_success_at TEXT,
  last_outcome TEXT NOT NULL DEFAULT '',
  last_job_id INTEGER,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  backoff_until TEXT,
  pending_reason TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL,
  FOREIGN KEY (last_job_id) REFERENCES job_runs(id)
);
CREATE TABLE coordinator_state (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
INSERT INTO schema_migrations (version, name, applied_at, app_version)
VALUES (?, 'v0.4.1_jobs_scheduler', datetime('now', '+8 hours'), ?)
`, taskSchedulerMigrationVersion, appVersion); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	log.Printf("database migration applied: version=%d name=v0.4.1_jobs_scheduler", taskSchedulerMigrationVersion)
	return nil
}

func (s *store) SchemaVersion() (int, error) {
	var version sql.NullInt64
	if err := s.db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version); err != nil {
		return 0, err
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

func (s *store) applyStateModelMigration() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.Exec(`
CREATE TABLE proxy_probe_state (
  proxy_id INTEGER PRIMARY KEY,
  status TEXT NOT NULL DEFAULT 'untested' CHECK (status IN ('untested', 'available', 'failed')),
  status_changed_at TEXT NOT NULL,
  detected_protocol TEXT,
  base_reachable INTEGER NOT NULL DEFAULT 0,
  exit_ip TEXT,
  latency_ms INTEGER,
  success_rate REAL NOT NULL DEFAULT 0,
  country TEXT,
  country_name TEXT,
  continent_code TEXT,
  ip_type TEXT,
  asn_org TEXT,
  geo_source TEXT,
  geo_updated_at TEXT,
  failure_reason TEXT NOT NULL DEFAULT '',
  last_error TEXT,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  checked_at TEXT,
  updated_at TEXT NOT NULL,
  FOREIGN KEY (proxy_id) REFERENCES proxies(id) ON DELETE CASCADE
);
CREATE INDEX idx_proxy_probe_status_changed ON proxy_probe_state(status, status_changed_at);
CREATE INDEX idx_proxy_probe_exit_ip ON proxy_probe_state(exit_ip);
CREATE INDEX idx_proxy_probe_country_status ON proxy_probe_state(country, status);
CREATE INDEX idx_proxy_probe_checked ON proxy_probe_state(checked_at);
CREATE TABLE proxy_target_state (
  proxy_id INTEGER NOT NULL,
  target_profile TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'untested' CHECK (status IN ('untested', 'available', 'failed')),
  status_changed_at TEXT NOT NULL,
  capability TEXT NOT NULL DEFAULT 'none' CHECK (capability IN ('none', 'base', 'web', 'api', 'web_api')),
  service_reachable INTEGER NOT NULL DEFAULT 0,
  api_reachable INTEGER,
  latency_ms INTEGER,
  success_rate REAL NOT NULL DEFAULT 0,
  grade TEXT NOT NULL DEFAULT '',
  cloudflare_status TEXT,
  recommended_use TEXT NOT NULL DEFAULT '',
  failure_reason TEXT NOT NULL DEFAULT '',
  last_error TEXT,
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  checked_at TEXT,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (proxy_id, target_profile),
  FOREIGN KEY (proxy_id) REFERENCES proxies(id) ON DELETE CASCADE
);
CREATE INDEX idx_proxy_target_quality ON proxy_target_state(target_profile, status, grade, latency_ms);
CREATE INDEX idx_proxy_target_status_changed ON proxy_target_state(target_profile, status_changed_at);
CREATE INDEX idx_proxy_target_capability ON proxy_target_state(target_profile, capability, status);
CREATE INDEX idx_proxy_target_proxy_status ON proxy_target_state(proxy_id, status);
`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
INSERT INTO proxy_target_state (
  proxy_id, target_profile, status, status_changed_at, capability, service_reachable,
  api_reachable, latency_ms, success_rate, grade, cloudflare_status, recommended_use,
  failure_reason, last_error, consecutive_failures, checked_at, updated_at
)
SELECT
  c.proxy_id,
  c.target_profile,
  CASE
    WHEN c.target_profile = 'generic' AND c.status = 'available'
      AND (c.service_reachable = 1 OR COALESCE(c.api_reachable, 0) = 1 OR COALESCE(c.exit_ip, '') != '') THEN 'available'
    WHEN c.target_profile != 'generic' AND c.status = 'available'
      AND (c.service_reachable = 1 OR COALESCE(c.api_reachable, 0) = 1) THEN 'available'
    WHEN c.status = 'untested' THEN 'untested'
    WHEN c.status = 'available' AND c.target_profile = 'generic' THEN 'untested'
    ELSE 'failed'
  END,
  COALESCE(c.status_changed_at, c.checked_at, c.updated_at, p.updated_at, p.created_at, datetime('now', '+8 hours')),
  CASE
    WHEN c.service_reachable = 1 AND COALESCE(c.api_reachable, 0) = 1 THEN 'web_api'
    WHEN c.service_reachable = 1 THEN 'web'
    WHEN COALESCE(c.api_reachable, 0) = 1 THEN 'api'
    WHEN COALESCE(c.exit_ip, '') != '' THEN 'base'
    ELSE 'none'
  END,
  c.service_reachable, c.api_reachable, c.latency_ms, c.success_rate, c.grade,
  c.cloudflare_status, c.recommended_use,
  CASE WHEN c.last_error IS NULL THEN '' ELSE
    CASE
      WHEN instr(c.last_error, '[') = 1 AND instr(c.last_error, ']') > 2
        THEN substr(c.last_error, 2, instr(c.last_error, ']') - 2)
      ELSE ''
    END
  END,
  c.last_error,
  CASE WHEN c.status = 'failed' THEN 1 ELSE 0 END,
  c.checked_at,
  c.updated_at
FROM proxy_checks c
JOIN proxies p ON p.id = c.proxy_id;
`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
INSERT INTO proxy_probe_state (
  proxy_id, status, status_changed_at, detected_protocol, base_reachable, exit_ip,
  latency_ms, success_rate, country, country_name, continent_code, ip_type, asn_org,
  geo_source, geo_updated_at, failure_reason, last_error, consecutive_failures,
  checked_at, updated_at
)
SELECT
  p.id,
  CASE
    WHEN EXISTS (
      SELECT 1 FROM proxy_checks c
      WHERE c.proxy_id = p.id
        AND (COALESCE(c.exit_ip, '') != '' OR c.service_reachable = 1 OR COALESCE(c.api_reachable, 0) = 1)
    ) THEN 'available'
    WHEN EXISTS (
      SELECT 1 FROM proxy_checks c
      WHERE c.proxy_id = p.id AND c.target_profile = 'generic' AND c.status = 'failed'
    ) THEN 'failed'
    ELSE 'untested'
  END,
  COALESCE(p.status_changed_at, p.last_checked_at, p.updated_at, p.created_at, datetime('now', '+8 hours')),
  (SELECT c.detected_protocol FROM proxy_checks c WHERE c.proxy_id = p.id
    ORDER BY CASE WHEN COALESCE(c.exit_ip, '') != '' THEN 0 ELSE 1 END, COALESCE(c.checked_at, c.updated_at) DESC LIMIT 1),
  CASE WHEN EXISTS (
    SELECT 1 FROM proxy_checks c WHERE c.proxy_id = p.id
      AND (COALESCE(c.exit_ip, '') != '' OR c.service_reachable = 1 OR COALESCE(c.api_reachable, 0) = 1)
  ) THEN 1 ELSE 0 END,
  (SELECT c.exit_ip FROM proxy_checks c WHERE c.proxy_id = p.id AND COALESCE(c.exit_ip, '') != ''
    ORDER BY COALESCE(c.checked_at, c.updated_at) DESC LIMIT 1),
  (SELECT c.latency_ms FROM proxy_checks c WHERE c.proxy_id = p.id
    ORDER BY CASE WHEN COALESCE(c.exit_ip, '') != '' THEN 0 ELSE 1 END, COALESCE(c.checked_at, c.updated_at) DESC LIMIT 1),
  COALESCE((SELECT c.success_rate FROM proxy_checks c WHERE c.proxy_id = p.id
    ORDER BY CASE WHEN COALESCE(c.exit_ip, '') != '' THEN 0 ELSE 1 END, COALESCE(c.checked_at, c.updated_at) DESC LIMIT 1), 0),
  (SELECT c.country FROM proxy_checks c WHERE c.proxy_id = p.id AND COALESCE(c.country, '') != '' ORDER BY COALESCE(c.checked_at, c.updated_at) DESC LIMIT 1),
  (SELECT c.country_name FROM proxy_checks c WHERE c.proxy_id = p.id AND COALESCE(c.country_name, '') != '' ORDER BY COALESCE(c.checked_at, c.updated_at) DESC LIMIT 1),
  (SELECT c.continent_code FROM proxy_checks c WHERE c.proxy_id = p.id AND COALESCE(c.continent_code, '') != '' ORDER BY COALESCE(c.checked_at, c.updated_at) DESC LIMIT 1),
  (SELECT c.ip_type FROM proxy_checks c WHERE c.proxy_id = p.id AND COALESCE(c.ip_type, '') != '' ORDER BY COALESCE(c.checked_at, c.updated_at) DESC LIMIT 1),
  (SELECT c.asn_org FROM proxy_checks c WHERE c.proxy_id = p.id AND COALESCE(c.asn_org, '') != '' ORDER BY COALESCE(c.checked_at, c.updated_at) DESC LIMIT 1),
  (SELECT c.geo_source FROM proxy_checks c WHERE c.proxy_id = p.id AND COALESCE(c.geo_source, '') != '' ORDER BY COALESCE(c.checked_at, c.updated_at) DESC LIMIT 1),
  (SELECT c.geo_updated_at FROM proxy_checks c WHERE c.proxy_id = p.id AND COALESCE(c.geo_updated_at, '') != '' ORDER BY COALESCE(c.checked_at, c.updated_at) DESC LIMIT 1),
  CASE WHEN NOT EXISTS (SELECT 1 FROM proxy_checks c WHERE c.proxy_id = p.id AND (COALESCE(c.exit_ip, '') != '' OR c.service_reachable = 1 OR COALESCE(c.api_reachable, 0) = 1))
    AND EXISTS (SELECT 1 FROM proxy_checks c WHERE c.proxy_id = p.id AND c.target_profile = 'generic' AND c.status = 'failed')
    THEN 'base_unreachable' ELSE '' END,
  CASE WHEN NOT EXISTS (SELECT 1 FROM proxy_checks c WHERE c.proxy_id = p.id AND (COALESCE(c.exit_ip, '') != '' OR c.service_reachable = 1 OR COALESCE(c.api_reachable, 0) = 1))
    AND EXISTS (SELECT 1 FROM proxy_checks c WHERE c.proxy_id = p.id AND c.target_profile = 'generic' AND c.status = 'failed')
    THEN (SELECT c.last_error FROM proxy_checks c WHERE c.proxy_id = p.id AND c.target_profile = 'generic' ORDER BY COALESCE(c.checked_at, c.updated_at) DESC LIMIT 1)
    ELSE NULL END,
  CASE WHEN NOT EXISTS (SELECT 1 FROM proxy_checks c WHERE c.proxy_id = p.id AND (COALESCE(c.exit_ip, '') != '' OR c.service_reachable = 1 OR COALESCE(c.api_reachable, 0) = 1))
    AND EXISTS (SELECT 1 FROM proxy_checks c WHERE c.proxy_id = p.id AND c.target_profile = 'generic' AND c.status = 'failed') THEN 1 ELSE 0 END,
  (SELECT c.checked_at FROM proxy_checks c WHERE c.proxy_id = p.id ORDER BY COALESCE(c.checked_at, c.updated_at) DESC LIMIT 1),
  COALESCE(p.updated_at, p.created_at, datetime('now', '+8 hours'))
FROM proxies p;
`); err != nil {
		return err
	}
	if _, err := tx.Exec(`
INSERT INTO schema_migrations (version, name, applied_at, app_version)
VALUES (?, 'v0.4.0_state_model', datetime('now', '+8 hours'), ?)
`, stateModelMigrationVersion, appVersion); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

const proxyAggregateStatusExpression = `(SELECT CASE
  WHEN SUM(CASE WHEN c.status = 'available' THEN 1 ELSE 0 END) > 0 THEN 'available'
  WHEN SUM(CASE WHEN c.status = 'untested' THEN 1 ELSE 0 END) > 0 THEN 'untested'
  WHEN COUNT(*) > 0 THEN 'failed'
  ELSE 'untested'
END FROM proxy_target_state c WHERE c.proxy_id = proxies.id)`

const legacyProxyAggregateStatusExpression = `(SELECT CASE
  WHEN SUM(CASE WHEN c.status = 'available' THEN 1 ELSE 0 END) > 0 THEN 'available'
  WHEN SUM(CASE WHEN c.status = 'untested' THEN 1 ELSE 0 END) > 0 THEN 'untested'
  WHEN COUNT(*) > 0 THEN 'failed'
  ELSE 'untested'
END FROM proxy_checks c WHERE c.proxy_id = proxies.id)`

func (s *store) normalizeStoredProxyState() error {
	if _, err := s.db.Exec(`
UPDATE proxies
SET status_changed_at = COALESCE(last_checked_at, updated_at, created_at, datetime('now', '+8 hours'))
WHERE status_changed_at IS NULL;
UPDATE proxy_checks
SET status_changed_at = COALESCE(checked_at, updated_at, datetime('now', '+8 hours'))
WHERE status_changed_at IS NULL;
UPDATE proxy_checks
SET status = 'failed',
    grade = 'F',
    recommended_use = CASE WHEN COALESCE(exit_ip, '') != '' THEN 'base' ELSE recommended_use END,
    last_error = COALESCE(NULLIF(last_error, ''), '[target_unreachable] target service and API unreachable'),
    status_changed_at = datetime('now', '+8 hours'),
    updated_at = datetime('now', '+8 hours')
WHERE target_profile != 'generic'
  AND status = 'available'
  AND service_reachable = 0
  AND COALESCE(api_reachable, 0) = 0;`); err != nil {
		return err
	}
	_, err := s.db.Exec(`
UPDATE proxies
SET status_changed_at = CASE
      WHEN status != ` + legacyProxyAggregateStatusExpression + ` THEN datetime('now', '+8 hours')
      ELSE COALESCE(status_changed_at, last_checked_at, updated_at, created_at, datetime('now', '+8 hours'))
    END,
    status = ` + legacyProxyAggregateStatusExpression + `
WHERE EXISTS (SELECT 1 FROM proxy_checks c WHERE c.proxy_id = proxies.id)
  AND status != ` + legacyProxyAggregateStatusExpression + `;`)
	return err
}

func (s *store) columnExists(table string, column string) (bool, error) {
	rows, err := s.db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *store) UserPasswordHash(username string) (string, bool, error) {
	var hash string
	err := s.db.QueryRow("SELECT password_hash FROM users WHERE username = ? AND is_active = 1", strings.TrimSpace(username)).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return hash, true, nil
}

func (s *store) Stats() (map[string]any, error) {
	stats := map[string]any{
		"total":                    0,
		"available":                0,
		"failed":                   0,
		"untested":                 0,
		"checking":                 0,
		"transport_available":      0,
		"unique_target_available":  0,
		"target_available_records": 0,
	}
	rows, err := s.db.Query(`
SELECT COALESCE(ps.status, 'untested'), COUNT(*)
FROM proxies p
LEFT JOIN proxy_probe_state ps ON ps.proxy_id = p.id
WHERE p.enabled = 1
GROUP BY COALESCE(ps.status, 'untested')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		stats[status] = count
		total += count
	}
	stats["total"] = total
	transportAvailable := anyToInt(stats["available"])
	stats["transport_available"] = transportAvailable
	gradeRows, err := s.db.Query(`
SELECT ts.grade, COUNT(*)
FROM proxy_target_state ts
JOIN proxies p ON p.id = ts.proxy_id
WHERE p.enabled = 1 AND ts.status = 'available' AND ts.grade != ''
GROUP BY ts.grade`)
	if err != nil {
		return nil, err
	}
	defer gradeRows.Close()
	byGrade := map[string]int{}
	for gradeRows.Next() {
		var grade string
		var count int
		if err := gradeRows.Scan(&grade, &count); err != nil {
			return nil, err
		}
		byGrade[grade] = count
	}
	stats["by_grade"] = byGrade
	byTarget, err := s.TargetStats()
	if err != nil {
		return nil, err
	}
	stats["by_target"] = byTarget
	availableURLs, err := s.CountAvailableProxyURLsForProfiles(targetProfileOrder)
	if err != nil {
		return nil, err
	}
	var targetAvailableRecords int
	if err := s.db.QueryRow(`
SELECT COUNT(*) FROM proxy_target_state ts
JOIN proxies p ON p.id = ts.proxy_id
WHERE p.enabled = 1 AND ts.status = 'available'`).Scan(&targetAvailableRecords); err != nil {
		return nil, err
	}
	stats["target_available_records"] = targetAvailableRecords
	stats["available_records"] = targetAvailableRecords
	stats["unique_target_available"] = availableURLs
	stats["available"] = availableURLs
	return stats, nil
}

func (s *store) TargetStats() ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(targetProfileOrder))
	for _, profile := range targetProfileOrder {
		item := map[string]any{
			"target_profile": profile,
			"label":          targetProfileLabel(profile),
			"total":          0,
			"available":      0,
			"failed":         0,
			"untested":       0,
			"checking":       0,
			"by_grade":       map[string]int{},
		}
		rows, err := s.db.Query(`
SELECT COALESCE(c.status, 'untested') AS status, COUNT(*)
FROM proxies p
LEFT JOIN proxy_target_state c ON c.proxy_id = p.id AND c.target_profile = ?
WHERE p.enabled = 1
GROUP BY COALESCE(c.status, 'untested')`, profile)
		if err != nil {
			return nil, err
		}
		total := 0
		for rows.Next() {
			var status string
			var count int
			if err := rows.Scan(&status, &count); err != nil {
				_ = rows.Close()
				return nil, err
			}
			item[status] = count
			total += count
		}
		availableRecords := anyToInt(item["available"])
		item["available_records"] = availableRecords
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		item["total"] = total

		gradeRows, err := s.db.Query(`
SELECT COALESCE(c.grade, '') AS grade, COUNT(*)
FROM proxies p
LEFT JOIN proxy_target_state c ON c.proxy_id = p.id AND c.target_profile = ?
WHERE p.enabled = 1
  AND COALESCE(c.status, 'untested') = 'available'
  AND COALESCE(c.grade, '') != ''
GROUP BY COALESCE(c.grade, '')`, profile)
		if err != nil {
			return nil, err
		}
		byGrade := map[string]int{}
		for gradeRows.Next() {
			var grade string
			var count int
			if err := gradeRows.Scan(&grade, &count); err != nil {
				_ = gradeRows.Close()
				return nil, err
			}
			byGrade[grade] = count
		}
		if err := gradeRows.Close(); err != nil {
			return nil, err
		}
		if err := gradeRows.Err(); err != nil {
			return nil, err
		}
		item["by_grade"] = byGrade
		availableURLs, err := s.CountAvailableProxyURLs(profile)
		if err != nil {
			return nil, err
		}
		if availableURLs == 0 && availableRecords > 0 {
			availableURLs = availableRecords
		}
		item["available"] = availableURLs
		out = append(out, item)
	}
	return out, nil
}

func (s *store) ImportProxies(text string, source string, defaultProtocol string) (map[string]any, error) {
	items := parseProxyText(text, defaultProtocol)
	if len(items) == 0 {
		return nil, errors.New("no valid proxies found")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	added := 0
	updated := 0
	for _, item := range items {
		created, err := s.upsertProxy(tx, item, source)
		if err != nil {
			return nil, err
		}
		if created {
			added++
		} else {
			updated++
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	committed = true
	return map[string]any{"total": len(items), "added": added, "updated": updated, "skipped": 0}, nil
}

func (s *store) SourceHealth() (map[string]map[string]any, error) {
	rows, err := s.db.Query(`
SELECT source_id, last_fetch_at, last_fetch_status, last_imported, last_new, last_updated,
       failure_streak, disabled_until, last_error, updated_at
FROM source_health`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]any{}
	for rows.Next() {
		var sourceID, status, updatedAt string
		var lastFetchAt, disabledUntil, lastError sql.NullString
		var imported, added, updated, failureStreak int
		if err := rows.Scan(&sourceID, &lastFetchAt, &status, &imported, &added, &updated, &failureStreak, &disabledUntil, &lastError, &updatedAt); err != nil {
			return nil, err
		}
		out[sourceID] = map[string]any{
			"source_id":         sourceID,
			"last_fetch_at":     nullStringValue(lastFetchAt),
			"last_fetch_status": status,
			"last_imported":     imported,
			"last_new":          added,
			"last_updated":      updated,
			"failure_streak":    failureStreak,
			"disabled_until":    nullStringValue(disabledUntil),
			"last_error":        nullStringValue(lastError),
			"updated_at":        updatedAt,
		}
	}
	return out, rows.Err()
}

func (s *store) RecordSourceFetch(sourceID string, imported int, added int, updated int, err error) error {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return nil
	}
	if err == nil {
		_, execErr := s.db.Exec(`
INSERT INTO source_health (
  source_id, last_fetch_at, last_fetch_status, last_imported, last_new, last_updated,
  failure_streak, disabled_until, last_error, updated_at
)
VALUES (?, datetime('now', '+8 hours'), 'success', ?, ?, ?, 0, NULL, NULL, datetime('now', '+8 hours'))
ON CONFLICT(source_id) DO UPDATE SET
  last_fetch_at = excluded.last_fetch_at,
  last_fetch_status = excluded.last_fetch_status,
  last_imported = excluded.last_imported,
  last_new = excluded.last_new,
  last_updated = excluded.last_updated,
  failure_streak = 0,
  disabled_until = NULL,
  last_error = NULL,
  updated_at = excluded.updated_at`,
			sourceID, imported, added, updated)
		return execErr
	}
	var streak int
	_ = s.db.QueryRow("SELECT failure_streak FROM source_health WHERE source_id = ?", sourceID).Scan(&streak)
	streak++
	disabledUntilExpr := "NULL"
	if streak >= 3 {
		disabledUntilExpr = "datetime('now', '+8 hours', '+60 minutes')"
	}
	_, execErr := s.db.Exec(`
INSERT INTO source_health (
  source_id, last_fetch_at, last_fetch_status, last_imported, last_new, last_updated,
  failure_streak, disabled_until, last_error, updated_at
)
VALUES (?, datetime('now', '+8 hours'), 'failed', 0, 0, 0, ?, `+disabledUntilExpr+`, ?, datetime('now', '+8 hours'))
ON CONFLICT(source_id) DO UPDATE SET
  last_fetch_at = excluded.last_fetch_at,
  last_fetch_status = excluded.last_fetch_status,
  last_imported = 0,
  last_new = 0,
  last_updated = 0,
  failure_streak = excluded.failure_streak,
  disabled_until = excluded.disabled_until,
  last_error = excluded.last_error,
  updated_at = excluded.updated_at`,
		sourceID, streak, err.Error())
	return execErr
}

func (s *store) SourceCoolingDown(sourceID string) (bool, string, error) {
	var disabledUntil string
	err := s.db.QueryRow(`
SELECT COALESCE(disabled_until, '')
FROM source_health
WHERE source_id = ?
  AND disabled_until IS NOT NULL
  AND disabled_until > datetime('now', '+8 hours')`, strings.TrimSpace(sourceID)).Scan(&disabledUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, disabledUntil, nil
}

func (s *store) RecordMaintenanceEvent(action string, count int64, targetProfile string, reason string, settings appSettings) error {
	if count == 0 {
		return nil
	}
	raw, _ := json.Marshal(settings)
	_, err := s.db.Exec(`
INSERT INTO maintenance_events (action, count, target_profile, reason, settings_snapshot, created_at)
VALUES (?, ?, ?, ?, ?, datetime('now', '+8 hours'))`,
		strings.TrimSpace(action), count, strings.TrimSpace(targetProfile), strings.TrimSpace(reason), string(raw))
	return err
}

func (s *store) upsertProxy(tx *sql.Tx, proxy parsedProxy, source string) (bool, error) {
	if err := validateParsedProxy(proxy); err != nil {
		return false, err
	}
	key := proxyKey(proxy)
	var exists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM proxies WHERE proxy_key = ?", key).Scan(&exists); err != nil {
		return false, err
	}
	username := nullableString(proxy.Username)
	password := nullableString(proxy.Password)
	if exists == 0 {
		result, err := tx.Exec(`
INSERT INTO proxies (proxy_key, ip, port, protocol, username, password, source, status, status_changed_at, created_at, updated_at, first_seen_at, last_seen_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 'untested', datetime('now', '+8 hours'), datetime('now', '+8 hours'), datetime('now', '+8 hours'), datetime('now', '+8 hours'), datetime('now', '+8 hours'))`,
			key, proxy.Host, proxy.Port, proxy.Protocol, username, password, firstNonEmpty(source, "manual"))
		if err != nil {
			return false, err
		}
		proxyID, err := result.LastInsertId()
		if err != nil {
			return false, err
		}
		_, err = tx.Exec(`
INSERT INTO proxy_probe_state (proxy_id, status, status_changed_at, base_reachable, updated_at)
VALUES (?, 'untested', datetime('now', '+8 hours'), 0, datetime('now', '+8 hours'))`, proxyID)
		return true, err
	}
	_, err := tx.Exec(`
UPDATE proxies
SET source = ?, username = ?, password = ?, updated_at = datetime('now', '+8 hours'), last_seen_at = datetime('now', '+8 hours')
WHERE proxy_key = ?`,
		firstNonEmpty(source, "manual"), username, password, key)
	return false, err
}

func (s *store) ListProxies(filter proxyFilter) ([]proxyRecord, int, error) {
	target := strings.ToLower(strings.TrimSpace(filter.TargetProfile))
	if target != "" && target != "all" {
		return s.listProxiesForTarget(filter, normalizeTargetProfile(target))
	}
	where, args := filterWhere(filter)
	totalQuery := "SELECT COUNT(*) FROM proxies p LEFT JOIN proxy_probe_state ps ON ps.proxy_id = p.id" + where
	var total int
	if err := s.db.QueryRow(totalQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := clampInt(filter.Limit, 1, 100000)
	offset := clampInt(filter.Offset, 0, 10000000)
	query := `
SELECT p.id, p.proxy_key, p.ip, p.port, p.protocol, p.username, p.password, p.source, p.status, p.grade, p.latency_ms,
       p.exit_ip, p.country, p.country_name, p.continent_code, p.ip_type, p.asn_org, p.geo_source, p.geo_updated_at,
       p.success_rate, p.target_profile, p.detected_protocol, p.service_reachable, p.api_reachable, p.cloudflare_status, p.recommended_use,
       p.last_error, p.failure_count, p.created_at, p.updated_at, p.last_checked_at
FROM proxies p
LEFT JOIN proxy_probe_state ps ON ps.proxy_id = p.id` + where + `
ORDER BY
  CASE COALESCE(ps.status, 'untested') WHEN 'available' THEN 0 WHEN 'untested' THEN 1 WHEN 'failed' THEN 2 ELSE 3 END,
  COALESCE(ps.latency_ms, 999999),
  p.updated_at DESC
LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []proxyRecord{}
	for rows.Next() {
		item, err := scanProxy(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := s.hydrateProxyRecords(items, ""); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (s *store) listProxiesForTarget(filter proxyFilter, targetProfile string) ([]proxyRecord, int, error) {
	where, args := targetFilterWhere(filter)
	countArgs := append([]any{targetProfile}, args...)
	totalQuery := `
SELECT COUNT(*)
FROM proxies p
LEFT JOIN proxy_target_state c ON c.proxy_id = p.id AND c.target_profile = ?
LEFT JOIN proxy_probe_state ps ON ps.proxy_id = p.id` + where

	var total int
	if err := s.db.QueryRow(totalQuery, countArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := clampInt(filter.Limit, 1, 100000)
	offset := clampInt(filter.Offset, 0, 10000000)
	query := `
SELECT p.id, p.proxy_key, p.ip, p.port, p.protocol, p.username, p.password, p.source,
       COALESCE(c.status, 'untested') AS status,
       COALESCE(c.grade, '') AS grade,
       c.latency_ms, ps.exit_ip, ps.country, ps.country_name, ps.continent_code, ps.ip_type, ps.asn_org, ps.geo_source, ps.geo_updated_at,
       COALESCE(c.success_rate, 0) AS success_rate,
       COALESCE(c.target_profile, ?) AS target_profile,
       ps.detected_protocol,
       COALESCE(c.service_reachable, 0) AS service_reachable,
       c.api_reachable, c.cloudflare_status,
       COALESCE(c.recommended_use, '') AS recommended_use,
       c.last_error, p.failure_count, p.created_at, COALESCE(c.updated_at, p.updated_at) AS updated_at, c.checked_at
FROM proxies p
LEFT JOIN proxy_target_state c ON c.proxy_id = p.id AND c.target_profile = ?
LEFT JOIN proxy_probe_state ps ON ps.proxy_id = p.id` + where + `
ORDER BY
  CASE COALESCE(c.status, 'untested') WHEN 'available' THEN 0 WHEN 'untested' THEN 1 WHEN 'failed' THEN 2 ELSE 3 END,
  CASE COALESCE(c.grade, '') WHEN 'A' THEN 0 WHEN 'B' THEN 1 WHEN 'C' THEN 2 ELSE 3 END,
  COALESCE(c.latency_ms, 999999),
  COALESCE(c.updated_at, p.updated_at) DESC
LIMIT ? OFFSET ?`
	queryArgs := append([]any{targetProfile, targetProfile}, args...)
	queryArgs = append(queryArgs, limit, offset)
	rows, err := s.db.Query(query, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []proxyRecord{}
	for rows.Next() {
		item, err := scanProxy(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := s.hydrateProxyRecords(items, targetProfile); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func filterWhere(filter proxyFilter) (string, []any) {
	clauses := []string{"p.enabled = 1"}
	args := []any{}
	status := strings.ToLower(strings.TrimSpace(filter.Status))
	if status != "" && status != "all" {
		if status == "checked" {
			clauses = append(clauses, "COALESCE(ps.status, 'untested') != 'untested'")
		} else {
			clauses = append(clauses, "COALESCE(ps.status, 'untested') = ?")
			args = append(args, status)
		}
	}
	query := strings.TrimSpace(filter.Query)
	if query != "" {
		clauses = append(clauses, "(p.proxy_key LIKE ? OR p.source LIKE ? OR p.ip LIKE ?)")
		like := "%" + query + "%"
		args = append(args, like, like, like)
	}
	if countries := normalizeCountryCodes(filter.Countries); len(countries) > 0 {
		clauses = append(clauses, "UPPER(COALESCE(ps.country, '')) IN ("+placeholders(len(countries))+")")
		for _, country := range countries {
			args = append(args, country)
		}
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func targetFilterWhere(filter proxyFilter) (string, []any) {
	clauses := []string{"p.enabled = 1"}
	args := []any{}
	status := strings.ToLower(strings.TrimSpace(filter.Status))
	if status != "" && status != "all" {
		if status == "checked" {
			clauses = append(clauses, "COALESCE(c.status, 'untested') != 'untested'")
		} else {
			clauses = append(clauses, "COALESCE(c.status, 'untested') = ?")
			args = append(args, status)
		}
	}
	query := strings.TrimSpace(filter.Query)
	if query != "" {
		clauses = append(clauses, "(p.proxy_key LIKE ? OR p.source LIKE ? OR p.ip LIKE ?)")
		like := "%" + query + "%"
		args = append(args, like, like, like)
	}
	if countries := normalizeCountryCodes(filter.Countries); len(countries) > 0 {
		clauses = append(clauses, "UPPER(COALESCE(ps.country, '')) IN ("+placeholders(len(countries))+")")
		for _, country := range countries {
			args = append(args, country)
		}
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func normalizeCountryCodes(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		for _, part := range strings.Split(strings.ReplaceAll(value, "，", ","), ",") {
			code := strings.ToUpper(strings.TrimSpace(part))
			if code == "" || code == "ALL" || seen[code] {
				continue
			}
			if len(code) != 2 {
				continue
			}
			seen[code] = true
			out = append(out, code)
		}
	}
	sort.Strings(out)
	return out
}

func normalizeGatewayCountryPolicy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case gatewayCountryPolicyFallbackAny, "fallback", "fallback-any", "any":
		return gatewayCountryPolicyFallbackAny
	default:
		return gatewayCountryPolicyStrict
	}
}

func normalizeAvailableProxyFilter(filter availableProxyFilter) availableProxyFilter {
	target := strings.ToLower(strings.TrimSpace(filter.TargetProfile))
	if target != "" && target != "all" {
		target = normalizeTargetProfile(target)
	}
	filter.TargetProfile = target
	filter.Countries = normalizeCountryCodes(filter.Countries)
	filter.CountryPolicy = normalizeGatewayCountryPolicy(filter.CountryPolicy)
	if filter.Limit > 0 {
		filter.Limit = clampInt(filter.Limit, 1, 100000)
	}
	return filter
}

func shouldFallbackAnyCountry(filter availableProxyFilter) bool {
	return len(filter.Countries) > 0 && normalizeGatewayCountryPolicy(filter.CountryPolicy) == gatewayCountryPolicyFallbackAny
}

func placeholders(count int) string {
	if count <= 0 {
		return "NULL"
	}
	items := make([]string, count)
	for index := range items {
		items[index] = "?"
	}
	return strings.Join(items, ",")
}

func (s *store) DeleteProxiesByStatus(status string) (map[string]any, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	allowed := map[string]bool{"available": true, "failed": true, "untested": true, "checked": true, "all": true}
	if !allowed[status] {
		return nil, errors.New("status must be available, failed, untested, checked, or all")
	}
	where := ""
	args := []any{}
	if status == "checked" {
		where = " WHERE EXISTS (SELECT 1 FROM proxy_probe_state ps WHERE ps.proxy_id = proxies.id AND ps.status != 'untested')"
	} else if status == "available" {
		where = " WHERE EXISTS (SELECT 1 FROM proxy_target_state ts WHERE ts.proxy_id = proxies.id AND ts.status = 'available')"
	} else if status == "failed" {
		where = " WHERE EXISTS (SELECT 1 FROM proxy_probe_state ps WHERE ps.proxy_id = proxies.id AND ps.status = 'failed') AND NOT EXISTS (SELECT 1 FROM proxy_target_state ts WHERE ts.proxy_id = proxies.id AND ts.status = 'available')"
	} else if status == "untested" {
		where = " WHERE EXISTS (SELECT 1 FROM proxy_probe_state ps WHERE ps.proxy_id = proxies.id AND ps.status = 'untested')"
	} else if status != "all" {
		return nil, errors.New("unsupported status")
	}
	result, err := s.db.Exec("DELETE FROM proxies"+where, args...)
	if err != nil {
		return nil, err
	}
	deleted, _ := result.RowsAffected()
	return map[string]any{"status": status, "deleted": deleted}, nil
}

func (s *store) ListCheckCandidates(status string, limit int, targetProfile string) ([]ProxyTask, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	if status == "" {
		status = "untested"
	}
	targetProfile = normalizeTargetProfile(targetProfile)
	where := "WHERE p.enabled = 1"
	args := []any{targetProfile}
	switch status {
	case "all":
	case "checked":
		where += " AND COALESCE(c.status, 'untested') != 'untested'"
	case "available", "failed", "untested":
		where += " AND COALESCE(c.status, 'untested') = ?"
		args = append(args, status)
	default:
		return nil, errors.New("status must be available, failed, untested, checked, or all")
	}
	query := `
SELECT p.id, p.proxy_key, p.ip, p.port, p.protocol, p.username, p.password, p.source
FROM proxies p
LEFT JOIN proxy_target_state c ON c.proxy_id = p.id AND c.target_profile = ? ` + where + `
ORDER BY
  CASE COALESCE(c.status, 'untested') WHEN 'untested' THEN 0 WHEN 'failed' THEN 1 WHEN 'available' THEN 2 ELSE 3 END,
  CASE WHEN COALESCE(c.status, 'untested') = 'untested' AND c.checked_at IS NOT NULL THEN 0 ELSE 1 END,
  COALESCE(c.checked_at, p.created_at) ASC,
  p.id ASC
LIMIT ?`
	args = append(args, clampInt(limit, 1, 100000))
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ProxyTask{}
	for rows.Next() {
		var item ProxyTask
		var username, password sql.NullString
		if err := rows.Scan(&item.ID, &item.Proxy, &item.IP, &item.Port, &item.Protocol, &username, &password, &item.Source); err != nil {
			return nil, err
		}
		item.Username = nullStringPtr(username)
		item.Password = nullStringPtr(password)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *store) SaveCheckResult(result CheckResult) error {
	targetProfile := normalizeTargetProfile(result.TargetProfile)
	capability := targetCapability(result)
	if targetProfile == "generic" && strings.EqualFold(strings.TrimSpace(result.Status), "available") && capability == "none" {
		capability = "base"
	}
	targetStatus := normalizeTargetStateStatus(targetProfile, result.Status, capability)
	probeStatus := normalizeProbeStateStatus(result)
	failureReason := ""
	if result.LastError != nil {
		failureReason = failureReasonFromMessage(*result.LastError)
	}
	failureExpr := "failure_count"
	if targetStatus == "failed" {
		failureExpr = "failure_count + 1"
	} else if targetStatus == "available" {
		failureExpr = "0"
	}
	apiReachable := any(nil)
	if result.APIReachable != nil {
		apiReachable = boolToInt(*result.APIReachable)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var proxyExists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM proxies WHERE id = ?", result.ProxyID).Scan(&proxyExists); err != nil {
		return err
	}
	if proxyExists == 0 {
		return sql.ErrNoRows
	}
	preserveRootSnapshot := false
	if targetStatus == "failed" {
		var otherAvailable int
		if err := tx.QueryRow(`
SELECT COUNT(*)
FROM proxy_target_state
WHERE proxy_id = ?
  AND target_profile != ?
  AND status = 'available'`, result.ProxyID, targetProfile).Scan(&otherAvailable); err != nil {
			return err
		}
		preserveRootSnapshot = otherAvailable > 0
	}
	probeFailureIncrement := 0
	if probeStatus == "failed" {
		probeFailureIncrement = 1
	}
	if _, err := tx.Exec(`
INSERT INTO proxy_probe_state (
  proxy_id, status, status_changed_at, detected_protocol, base_reachable, exit_ip,
  latency_ms, success_rate, country, country_name, continent_code, ip_type, asn_org,
  geo_source, geo_updated_at, failure_reason, last_error, consecutive_failures,
  checked_at, updated_at
)
VALUES (?, ?, datetime('now', '+8 hours'), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now', '+8 hours'), datetime('now', '+8 hours'))
ON CONFLICT(proxy_id) DO UPDATE SET
  status_changed_at = CASE
    WHEN proxy_probe_state.status != excluded.status THEN excluded.status_changed_at
    ELSE proxy_probe_state.status_changed_at
  END,
  status = excluded.status,
  detected_protocol = COALESCE(excluded.detected_protocol, proxy_probe_state.detected_protocol),
  base_reachable = excluded.base_reachable,
  exit_ip = COALESCE(excluded.exit_ip, proxy_probe_state.exit_ip),
  latency_ms = excluded.latency_ms,
  success_rate = excluded.success_rate,
  country = COALESCE(excluded.country, proxy_probe_state.country),
  country_name = COALESCE(excluded.country_name, proxy_probe_state.country_name),
  continent_code = COALESCE(excluded.continent_code, proxy_probe_state.continent_code),
  ip_type = COALESCE(excluded.ip_type, proxy_probe_state.ip_type),
  asn_org = COALESCE(excluded.asn_org, proxy_probe_state.asn_org),
  geo_source = COALESCE(excluded.geo_source, proxy_probe_state.geo_source),
  geo_updated_at = COALESCE(excluded.geo_updated_at, proxy_probe_state.geo_updated_at),
  failure_reason = CASE WHEN excluded.status = 'failed' THEN excluded.failure_reason ELSE '' END,
  last_error = CASE WHEN excluded.status = 'failed' THEN excluded.last_error ELSE NULL END,
  consecutive_failures = CASE
    WHEN excluded.status = 'available' THEN 0
    WHEN excluded.status = 'failed' THEN proxy_probe_state.consecutive_failures + 1
    ELSE proxy_probe_state.consecutive_failures
  END,
  checked_at = excluded.checked_at,
  updated_at = excluded.updated_at`,
		result.ProxyID,
		probeStatus,
		nullableString(result.DetectedProtocol),
		boolToInt(probeStatus == "available"),
		nullableString(result.ExitIP),
		nullableInt(result.LatencyMS),
		result.SuccessRate,
		nullableString(result.Country),
		nullableString(result.CountryName),
		nullableString(result.ContinentCode),
		nullableString(result.IPType),
		nullableString(result.ASNOrg),
		nullableString(result.GeoSource),
		nullableString(result.GeoUpdatedAt),
		failureReason,
		nullableString(result.LastError),
		probeFailureIncrement,
	); err != nil {
		return err
	}
	targetFailureIncrement := 0
	if targetStatus == "failed" {
		targetFailureIncrement = 1
	}
	if _, err := tx.Exec(`
INSERT INTO proxy_target_state (
  proxy_id, target_profile, status, status_changed_at, capability, service_reachable,
  api_reachable, latency_ms, success_rate, grade, cloudflare_status, recommended_use,
  failure_reason, last_error, consecutive_failures, checked_at, updated_at
)
VALUES (?, ?, ?, datetime('now', '+8 hours'), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now', '+8 hours'), datetime('now', '+8 hours'))
ON CONFLICT(proxy_id, target_profile) DO UPDATE SET
  status_changed_at = CASE
    WHEN proxy_target_state.status != excluded.status THEN excluded.status_changed_at
    ELSE proxy_target_state.status_changed_at
  END,
  status = excluded.status,
  capability = excluded.capability,
  service_reachable = excluded.service_reachable,
  api_reachable = excluded.api_reachable,
  latency_ms = excluded.latency_ms,
  success_rate = excluded.success_rate,
  grade = excluded.grade,
  cloudflare_status = excluded.cloudflare_status,
  recommended_use = excluded.recommended_use,
  failure_reason = CASE WHEN excluded.status = 'failed' THEN excluded.failure_reason ELSE '' END,
  last_error = CASE WHEN excluded.status = 'failed' THEN excluded.last_error ELSE NULL END,
  consecutive_failures = CASE
    WHEN excluded.status = 'available' THEN 0
    WHEN excluded.status = 'failed' THEN proxy_target_state.consecutive_failures + 1
    ELSE proxy_target_state.consecutive_failures
  END,
  checked_at = excluded.checked_at,
  updated_at = excluded.updated_at`,
		result.ProxyID, targetProfile, targetStatus, capability,
		boolToInt(result.ServiceReachable), apiReachable, nullableInt(result.LatencyMS), result.SuccessRate,
		result.Grade, nullableString(result.CloudflareStatus), result.RecommendedUse,
		failureReason, nullableString(result.LastError), targetFailureIncrement,
	); err != nil {
		return err
	}
	if !preserveRootSnapshot {
		if _, err := tx.Exec(`
UPDATE proxies
SET grade = ?,
    latency_ms = ?,
    exit_ip = ?,
    country = ?,
    country_name = ?,
    continent_code = ?,
    ip_type = ?,
    asn_org = ?,
    geo_source = ?,
    geo_updated_at = ?,
    success_rate = ?,
    target_profile = ?,
    detected_protocol = ?,
    service_reachable = ?,
    api_reachable = ?,
    cloudflare_status = ?,
    recommended_use = ?,
    last_error = ?,
    failure_count = `+failureExpr+`,
    updated_at = datetime('now', '+8 hours'),
    last_checked_at = datetime('now', '+8 hours')
WHERE id = ?`,
			result.Grade,
			nullableInt(result.LatencyMS),
			nullableString(result.ExitIP),
			nullableString(result.Country),
			nullableString(result.CountryName),
			nullableString(result.ContinentCode),
			nullableString(result.IPType),
			nullableString(result.ASNOrg),
			nullableString(result.GeoSource),
			nullableString(result.GeoUpdatedAt),
			result.SuccessRate,
			targetProfile,
			nullableString(result.DetectedProtocol),
			boolToInt(result.ServiceReachable),
			apiReachable,
			nullableString(result.CloudflareStatus),
			result.RecommendedUse,
			nullableString(result.LastError),
			result.ProxyID,
		); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`
INSERT INTO proxy_checks (
  proxy_id, target_profile, status, status_changed_at, grade, latency_ms, exit_ip, country, country_name, continent_code, ip_type, asn_org, geo_source, geo_updated_at,
  success_rate, detected_protocol, service_reachable, api_reachable, cloudflare_status,
  recommended_use, last_error, checked_at, updated_at
)
VALUES (?, ?, ?, datetime('now', '+8 hours'), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now', '+8 hours'), datetime('now', '+8 hours'))
ON CONFLICT(proxy_id, target_profile) DO UPDATE SET
  status_changed_at = CASE
    WHEN proxy_checks.status != excluded.status THEN excluded.status_changed_at
    ELSE COALESCE(proxy_checks.status_changed_at, excluded.status_changed_at)
  END,
  status = excluded.status,
  grade = excluded.grade,
  latency_ms = excluded.latency_ms,
  exit_ip = excluded.exit_ip,
  country = excluded.country,
  country_name = excluded.country_name,
  continent_code = excluded.continent_code,
  ip_type = excluded.ip_type,
  asn_org = excluded.asn_org,
  geo_source = excluded.geo_source,
  geo_updated_at = excluded.geo_updated_at,
  success_rate = excluded.success_rate,
  detected_protocol = excluded.detected_protocol,
  service_reachable = excluded.service_reachable,
  api_reachable = excluded.api_reachable,
  cloudflare_status = excluded.cloudflare_status,
  recommended_use = excluded.recommended_use,
  last_error = excluded.last_error,
  checked_at = excluded.checked_at,
  updated_at = excluded.updated_at`,
		result.ProxyID,
		targetProfile,
		targetStatus,
		result.Grade,
		nullableInt(result.LatencyMS),
		nullableString(result.ExitIP),
		nullableString(result.Country),
		nullableString(result.CountryName),
		nullableString(result.ContinentCode),
		nullableString(result.IPType),
		nullableString(result.ASNOrg),
		nullableString(result.GeoSource),
		nullableString(result.GeoUpdatedAt),
		result.SuccessRate,
		nullableString(result.DetectedProtocol),
		boolToInt(result.ServiceReachable),
		apiReachable,
		nullableString(result.CloudflareStatus),
		result.RecommendedUse,
		nullableString(result.LastError),
	)
	if err != nil {
		return err
	}
	if err := syncProxyAggregateStatusTx(tx, result.ProxyID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func targetCapability(result CheckResult) string {
	apiReachable := result.APIReachable != nil && *result.APIReachable
	switch {
	case result.ServiceReachable && apiReachable:
		return "web_api"
	case result.ServiceReachable:
		return "web"
	case apiReachable:
		return "api"
	case result.BaseReachable || (result.ExitIP != nil && strings.TrimSpace(*result.ExitIP) != ""):
		return "base"
	default:
		return "none"
	}
}

func normalizeTargetStateStatus(targetProfile string, requested string, capability string) string {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "untested" {
		return "untested"
	}
	if requested != "available" {
		return "failed"
	}
	if targetProfile == "generic" {
		if capability != "none" {
			return "available"
		}
		return "untested"
	}
	if capability == "web" || capability == "api" || capability == "web_api" {
		return "available"
	}
	return "failed"
}

func normalizeProbeStateStatus(result CheckResult) string {
	if result.BaseReachable || result.ServiceReachable || (result.APIReachable != nil && *result.APIReachable) ||
		(result.ExitIP != nil && strings.TrimSpace(*result.ExitIP) != "") || strings.EqualFold(result.Status, "available") {
		return "available"
	}
	if strings.EqualFold(result.Status, "untested") {
		return "untested"
	}
	return "failed"
}

func syncProxyAggregateStatusTx(tx *sql.Tx, proxyID int) error {
	_, err := tx.Exec(`
UPDATE proxies
SET status_changed_at = CASE
      WHEN status != `+proxyAggregateStatusExpression+` THEN datetime('now', '+8 hours')
      ELSE COALESCE(status_changed_at, last_checked_at, updated_at, created_at, datetime('now', '+8 hours'))
    END,
    status = `+proxyAggregateStatusExpression+`
WHERE id = ?`, proxyID)
	return err
}

func syncAllProxyAggregateStatusesTx(tx *sql.Tx) error {
	_, err := tx.Exec(`
UPDATE proxies
SET status_changed_at = CASE
      WHEN status != ` + proxyAggregateStatusExpression + ` THEN datetime('now', '+8 hours')
      ELSE COALESCE(status_changed_at, last_checked_at, updated_at, created_at, datetime('now', '+8 hours'))
    END,
    status = ` + proxyAggregateStatusExpression + `
WHERE EXISTS (SELECT 1 FROM proxy_target_state c WHERE c.proxy_id = proxies.id)
  AND status != ` + proxyAggregateStatusExpression + `;`)
	return err
}

func (s *store) DeleteProxyIfNoAvailableTargets(id int) (bool, error) {
	result, err := s.db.Exec(`
DELETE FROM proxies
WHERE id = ?
  AND EXISTS (
    SELECT 1 FROM proxy_probe_state ps
    WHERE ps.proxy_id = proxies.id
      AND ps.status = 'failed'
      AND ps.base_reachable = 0
  )
  AND NOT EXISTS (
    SELECT 1 FROM proxy_target_state c
    WHERE c.proxy_id = proxies.id
      AND c.status = 'available'
  )`, id)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

func (s *store) DeleteFailedProxies() (int64, error) {
	result, err := s.db.Exec(`
DELETE FROM proxies
WHERE enabled = 1
  AND EXISTS (
    SELECT 1 FROM proxy_probe_state ps
    WHERE ps.proxy_id = proxies.id
      AND ps.status = 'failed'
      AND ps.consecutive_failures >= 2
  )
  AND NOT EXISTS (
    SELECT 1 FROM proxy_target_state c
    WHERE c.proxy_id = proxies.id
      AND c.status = 'available'
  )`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *store) DeleteExpiredUntested(ttlHours int) (int64, error) {
	ttlHours = clampInt(ttlHours, 1, 8760)
	result, err := s.db.Exec(`
DELETE FROM proxies
WHERE enabled = 1
  AND EXISTS (
    SELECT 1 FROM proxy_probe_state ps
    WHERE ps.proxy_id = proxies.id
      AND ps.status = 'untested'
      AND ps.status_changed_at <= datetime('now', '+8 hours', ?)
  )
  AND NOT EXISTS (
    SELECT 1 FROM proxy_target_state ts
    WHERE ts.proxy_id = proxies.id AND ts.status = 'available'
  )`,
		fmt.Sprintf("-%d hours", ttlHours),
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *store) RequeueExpiredAvailable(ttlHours int) (int64, error) {
	ttlHours = clampInt(ttlHours, 1, 8760)
	modifier := fmt.Sprintf("-%d hours", ttlHours)
	var affected int64
	if err := s.db.QueryRow(`
SELECT COUNT(*) FROM (
  SELECT proxy_id AS id
  FROM proxy_target_state
  WHERE status = 'available'
    AND COALESCE(checked_at, updated_at) <= datetime('now', '+8 hours', ?)
  UNION
  SELECT proxy_id AS id
  FROM proxy_probe_state
  WHERE status = 'available'
    AND COALESCE(checked_at, updated_at) <= datetime('now', '+8 hours', ?)
)`, modifier, modifier).Scan(&affected); err != nil {
		return 0, err
	}
	if affected == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.Exec(`
UPDATE proxy_target_state
SET status = 'untested',
    status_changed_at = datetime('now', '+8 hours'),
    updated_at = datetime('now', '+8 hours')
WHERE status = 'available'
  AND COALESCE(checked_at, updated_at) <= datetime('now', '+8 hours', ?)`,
		modifier); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
UPDATE proxy_checks
SET status = 'untested',
    status_changed_at = datetime('now', '+8 hours'),
    updated_at = datetime('now', '+8 hours')
WHERE status = 'available'
  AND COALESCE(checked_at, updated_at) <= datetime('now', '+8 hours', ?)`, modifier); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`
UPDATE proxy_probe_state
SET status = 'untested',
    status_changed_at = datetime('now', '+8 hours'),
    updated_at = datetime('now', '+8 hours')
WHERE status = 'available'
  AND COALESCE(checked_at, updated_at) <= datetime('now', '+8 hours', ?)`,
		modifier); err != nil {
		return 0, err
	}
	if err := syncAllProxyAggregateStatusesTx(tx); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	committed = true
	return affected, nil
}

func (s *store) CountExpiredAvailableStates(ttlHours int) (int64, int64, error) {
	ttlHours = clampInt(ttlHours, 1, 8760)
	modifier := fmt.Sprintf("-%d hours", ttlHours)
	var probeCount, targetCount int64
	if err := s.db.QueryRow(`
SELECT COUNT(*) FROM proxy_probe_state
WHERE status = 'available'
  AND COALESCE(checked_at, updated_at) <= datetime('now', '+8 hours', ?)`, modifier).Scan(&probeCount); err != nil {
		return 0, 0, err
	}
	if err := s.db.QueryRow(`
SELECT COUNT(*) FROM proxy_target_state
WHERE status = 'available'
  AND COALESCE(checked_at, updated_at) <= datetime('now', '+8 hours', ?)`, modifier).Scan(&targetCount); err != nil {
		return 0, 0, err
	}
	return probeCount, targetCount, nil
}

func (s *store) CountProxiesByStatus(status string) (int, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	var count int
	err := s.db.QueryRow(`
SELECT COUNT(*)
FROM proxies p
LEFT JOIN proxy_probe_state ps ON ps.proxy_id = p.id
WHERE p.enabled = 1 AND COALESCE(ps.status, 'untested') = ?`, status).Scan(&count)
	return count, err
}

func (s *store) CountTargetProxiesByStatus(status string, targetProfile string) (int, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	targetProfile = normalizeTargetProfile(targetProfile)
	if status != "available" && status != "failed" && status != "untested" {
		return 0, errors.New("status must be available, failed, or untested")
	}
	var count int
	err := s.db.QueryRow(`
SELECT COUNT(*)
FROM proxies p
LEFT JOIN proxy_target_state c ON c.proxy_id = p.id AND c.target_profile = ?
WHERE p.enabled = 1
  AND COALESCE(c.status, 'untested') = ?`, targetProfile, status).Scan(&count)
	return count, err
}

func (s *store) CountAvailableProxyURLs(targetProfile string) (int, error) {
	return s.CountAvailableProxyURLsFiltered(availableProxyFilter{TargetProfile: targetProfile})
}

func (s *store) CountAvailableProxyURLsFiltered(filter availableProxyFilter) (int, error) {
	filter = normalizeAvailableProxyFilter(filter)
	count, err := s.countAvailableProxyURLsFiltered(filter)
	if err != nil || count > 0 || !shouldFallbackAnyCountry(filter) {
		return count, err
	}
	filter.Countries = nil
	return s.countAvailableProxyURLsFiltered(filter)
}

func (s *store) countAvailableProxyURLsFiltered(filter availableProxyFilter) (int, error) {
	target := strings.ToLower(strings.TrimSpace(filter.TargetProfile))
	countries := normalizeCountryCodes(filter.Countries)
	if target != "" && target != "all" {
		target = normalizeTargetProfile(target)
		args := []any{target}
		countryClause := ""
		if len(countries) > 0 {
			countryClause = " AND UPPER(COALESCE(ps.country, '')) IN (" + placeholders(len(countries)) + ")"
			for _, country := range countries {
				args = append(args, country)
			}
		}
		query := `
SELECT COUNT(*) FROM (
  SELECT
    CASE
      WHEN COALESCE(NULLIF(ps.detected_protocol, ''), p.protocol, '') IN ('', 'auto') THEN 'http'
      ELSE COALESCE(NULLIF(ps.detected_protocol, ''), p.protocol)
    END AS protocol,
    COALESCE(p.username, '') AS username,
    COALESCE(p.password, '') AS password,
    LOWER(p.ip) AS ip,
    p.port
  FROM proxies p
  JOIN proxy_target_state c ON c.proxy_id = p.id AND c.target_profile = ?
  LEFT JOIN proxy_probe_state ps ON ps.proxy_id = p.id
  WHERE p.enabled = 1
    AND c.status = 'available'
    AND ((c.target_profile = 'generic' AND c.capability IN ('base', 'web', 'api', 'web_api'))
      OR (c.target_profile != 'generic' AND c.capability IN ('web', 'api', 'web_api')))
` + countryClause + `
  GROUP BY 1, 2, 3, 4, 5
)`
		var count int
		err := s.db.QueryRow(query, args...).Scan(&count)
		return count, err
	}
	return s.countAvailableProxyURLsForProfilesFiltered(targetProfileOrder, filter)
}

func (s *store) CountAvailableProxyURLsForProfiles(targetProfiles []string) (int, error) {
	return s.CountAvailableProxyURLsForProfilesFiltered(targetProfiles, availableProxyFilter{})
}

func (s *store) CountAvailableProxyURLsForProfilesFiltered(targetProfiles []string, filter availableProxyFilter) (int, error) {
	filter = normalizeAvailableProxyFilter(filter)
	if shouldFallbackAnyCountry(filter) {
		return s.countAvailableProxyURLsForProfilesWithPerProfileFallback(targetProfiles, filter)
	}
	return s.countAvailableProxyURLsForProfilesFiltered(targetProfiles, filter)
}

func (s *store) countAvailableProxyURLsForProfilesWithPerProfileFallback(targetProfiles []string, filter availableProxyFilter) (int, error) {
	profiles := normalizeTargetProfiles(targetProfiles)
	seen := map[string]bool{}
	for _, profile := range profiles {
		urls, err := s.AvailableProxyURLsFiltered(availableProxyFilter{
			TargetProfile: profile,
			Countries:     filter.Countries,
			CountryPolicy: filter.CountryPolicy,
		})
		if err != nil {
			return 0, err
		}
		for _, proxyURL := range urls {
			seen[proxyURL] = true
		}
	}
	return len(seen), nil
}

func (s *store) countAvailableProxyURLsForProfilesFiltered(targetProfiles []string, filter availableProxyFilter) (int, error) {
	profiles := normalizeTargetProfiles(targetProfiles)
	args := make([]any, 0, len(profiles))
	for _, profile := range profiles {
		args = append(args, profile)
	}
	countries := normalizeCountryCodes(filter.Countries)
	countryClause := ""
	if len(countries) > 0 {
		countryClause = " AND UPPER(COALESCE(ps.country, '')) IN (" + placeholders(len(countries)) + ")"
		for _, country := range countries {
			args = append(args, country)
		}
	}
	query := `
SELECT COUNT(*) FROM (
  SELECT
    CASE
      WHEN COALESCE(NULLIF(ps.detected_protocol, ''), p.protocol, '') IN ('', 'auto') THEN 'http'
      ELSE COALESCE(NULLIF(ps.detected_protocol, ''), p.protocol)
    END AS protocol,
    COALESCE(p.username, '') AS username,
    COALESCE(p.password, '') AS password,
    LOWER(p.ip) AS ip,
    p.port
  FROM proxies p
  JOIN proxy_target_state c ON c.proxy_id = p.id
  LEFT JOIN proxy_probe_state ps ON ps.proxy_id = p.id
  WHERE p.enabled = 1
    AND c.status = 'available'
    AND ((c.target_profile = 'generic' AND c.capability IN ('base', 'web', 'api', 'web_api'))
      OR (c.target_profile != 'generic' AND c.capability IN ('web', 'api', 'web_api')))
    AND c.target_profile IN (` + placeholders(len(profiles)) + `)
` + countryClause + `
  GROUP BY 1, 2, 3, 4, 5
)`
	var count int
	err := s.db.QueryRow(query, args...).Scan(&count)
	return count, err
}

func (s *store) ExportAvailable(targetProfile string, limit int) ([]proxyRecord, error) {
	return s.ExportAvailableFiltered(availableProxyFilter{TargetProfile: targetProfile, Limit: limit})
}

func (s *store) ExportAvailableFiltered(filter availableProxyFilter) ([]proxyRecord, error) {
	filter = normalizeAvailableProxyFilter(filter)
	items, err := s.exportAvailableFiltered(filter)
	if err != nil || len(items) > 0 || !shouldFallbackAnyCountry(filter) {
		return items, err
	}
	filter.Countries = nil
	return s.exportAvailableFiltered(filter)
}

func (s *store) exportAvailableFiltered(filter availableProxyFilter) ([]proxyRecord, error) {
	profiles := normalizeTargetProfilesOrNil(filter.TargetProfile)
	if len(profiles) > 0 {
		return s.exportAvailableForProfilesFiltered(profiles, filter)
	}
	return s.exportAvailableForProfilesFiltered(targetProfileOrder, filter)
}

func (s *store) ExportAvailableForProfiles(targetProfiles []string, limit int) ([]proxyRecord, error) {
	return s.ExportAvailableForProfilesFiltered(targetProfiles, availableProxyFilter{Limit: limit})
}

func (s *store) ExportAvailableForProfilesFiltered(targetProfiles []string, filter availableProxyFilter) ([]proxyRecord, error) {
	filter = normalizeAvailableProxyFilter(filter)
	items, err := s.exportAvailableForProfilesFiltered(targetProfiles, filter)
	if err != nil || len(items) > 0 || !shouldFallbackAnyCountry(filter) {
		return items, err
	}
	filter.Countries = nil
	return s.exportAvailableForProfilesFiltered(targetProfiles, filter)
}

func (s *store) exportAvailableForProfilesFiltered(targetProfiles []string, filter availableProxyFilter) ([]proxyRecord, error) {
	targetProfiles = normalizeTargetProfiles(targetProfiles)
	maxItems := 100000
	if filter.Limit > 0 {
		maxItems = clampInt(filter.Limit, 1, 100000)
	}
	items := []proxyRecord{}
	for _, profile := range targetProfiles {
		remaining := maxItems - len(items)
		if remaining <= 0 {
			break
		}
		batch, _, err := s.ListProxies(proxyFilter{Status: "available", TargetProfile: profile, Countries: filter.Countries, Limit: remaining})
		if err != nil {
			return nil, err
		}
		items = append(items, batch...)
	}
	return items, nil
}

func (s *store) AvailableProxyURLs(limit int, targetProfile string) ([]string, error) {
	return s.AvailableProxyURLsFiltered(availableProxyFilter{TargetProfile: targetProfile, Limit: limit})
}

func (s *store) AvailableProxyURLsFiltered(filter availableProxyFilter) ([]string, error) {
	filter = normalizeAvailableProxyFilter(filter)
	exportFilter := filter
	exportFilter.Limit = 0
	items, err := s.ExportAvailableFiltered(exportFilter)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		proxyURL := item.ProxyURL()
		if !seen[proxyURL] {
			seen[proxyURL] = true
			out = append(out, proxyURL)
		}
	}
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func scanProxy(rows *sql.Rows) (proxyRecord, error) {
	var item proxyRecord
	var username, password, exitIP, country, countryName, continentCode, ipType, asnOrg, geoSource, geoUpdatedAt, detected, cloudflare, lastError, lastChecked sql.NullString
	var latencyInt sql.NullInt64
	var apiReachableInt sql.NullInt64
	var serviceReachable int
	err := rows.Scan(
		&item.ID, &item.ProxyKey, &item.IP, &item.Port, &item.Protocol, &username, &password, &item.Source, &item.Status, &item.Grade,
		&latencyInt, &exitIP, &country, &countryName, &continentCode, &ipType, &asnOrg, &geoSource, &geoUpdatedAt, &item.SuccessRate, &item.TargetProfile, &detected, &serviceReachable,
		&apiReachableInt, &cloudflare, &item.RecommendedUse, &lastError, &item.FailureCount, &item.CreatedAt, &item.UpdatedAt, &lastChecked,
	)
	if err != nil {
		return item, err
	}
	item.Username = nullStringPtr(username)
	item.Password = nullStringPtr(password)
	if latencyInt.Valid {
		item.LatencyMS = intPtr(int(latencyInt.Int64))
	}
	item.ExitIP = nullStringPtr(exitIP)
	item.Country = nullStringPtr(country)
	item.CountryName = nullStringPtr(countryName)
	item.ContinentCode = nullStringPtr(continentCode)
	item.IPType = nullStringPtr(ipType)
	item.ASNOrg = nullStringPtr(asnOrg)
	item.GeoSource = nullStringPtr(geoSource)
	item.GeoUpdatedAt = nullStringPtr(geoUpdatedAt)
	item.DetectedProtocol = nullStringPtr(detected)
	item.ServiceReachable = serviceReachable == 1
	if apiReachableInt.Valid {
		item.APIReachable = boolPtr(apiReachableInt.Int64 == 1)
	}
	item.CloudflareStatus = nullStringPtr(cloudflare)
	item.LastError = nullStringPtr(lastError)
	if item.LastError != nil {
		item.FailureReason = failureReasonFromMessage(*item.LastError)
	}
	item.LastCheckedAt = nullStringPtr(lastChecked)
	return item, nil
}

func (s *store) hydrateProxyRecords(items []proxyRecord, targetProfile string) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]any, 0, len(items))
	byID := make(map[int]int, len(items))
	for index := range items {
		ids = append(ids, items[index].ID)
		byID[items[index].ID] = index
		if targetProfile == "" {
			items[index].TargetSummary = make(map[string]proxyTargetSummary, len(targetProfileOrder))
			for _, profile := range targetProfileOrder {
				items[index].TargetSummary[profile] = proxyTargetSummary{Status: "untested", Capability: "none"}
			}
		}
	}
	probeRows, err := s.db.Query(`
SELECT proxy_id, status, status_changed_at, detected_protocol, base_reachable, exit_ip,
       latency_ms, success_rate, country, country_name, continent_code, ip_type, asn_org,
       geo_source, geo_updated_at, failure_reason, last_error, consecutive_failures,
       checked_at, updated_at
FROM proxy_probe_state
WHERE proxy_id IN (`+placeholders(len(ids))+`)`, ids...)
	if err != nil {
		return err
	}
	for probeRows.Next() {
		probe, err := scanProxyProbeState(probeRows)
		if err != nil {
			_ = probeRows.Close()
			return err
		}
		if index, ok := byID[probe.ProxyID]; ok {
			items[index].Probe = &probe
			applyProbeCompatibility(&items[index], probe)
		}
	}
	if err := probeRows.Close(); err != nil {
		return err
	}
	if err := probeRows.Err(); err != nil {
		return err
	}
	args := append([]any{}, ids...)
	targetClause := ""
	if targetProfile != "" {
		targetClause = " AND target_profile = ?"
		args = append(args, targetProfile)
	}
	targetRows, err := s.db.Query(`
SELECT proxy_id, target_profile, status, status_changed_at, capability, service_reachable,
       api_reachable, latency_ms, success_rate, grade, cloudflare_status, recommended_use,
       failure_reason, last_error, consecutive_failures, checked_at, updated_at
FROM proxy_target_state
WHERE proxy_id IN (`+placeholders(len(ids))+`)`+targetClause, args...)
	if err != nil {
		return err
	}
	for targetRows.Next() {
		state, err := scanProxyTargetState(targetRows)
		if err != nil {
			_ = targetRows.Close()
			return err
		}
		index, ok := byID[state.ProxyID]
		if !ok {
			continue
		}
		if targetProfile != "" {
			items[index].TargetState = &state
			applyTargetCompatibility(&items[index], state)
			continue
		}
		items[index].TargetSummary[state.TargetProfile] = proxyTargetSummary{Status: state.Status, Capability: state.Capability}
	}
	if err := targetRows.Close(); err != nil {
		return err
	}
	return targetRows.Err()
}

func scanProxyProbeState(rows *sql.Rows) (proxyProbeState, error) {
	var state proxyProbeState
	var detected, exitIP, country, countryName, continentCode, ipType, asnOrg, geoSource, geoUpdatedAt, lastError, checkedAt sql.NullString
	var latency sql.NullInt64
	var baseReachable int
	err := rows.Scan(
		&state.ProxyID, &state.Status, &state.StatusChangedAt, &detected, &baseReachable, &exitIP,
		&latency, &state.SuccessRate, &country, &countryName, &continentCode, &ipType, &asnOrg,
		&geoSource, &geoUpdatedAt, &state.FailureReason, &lastError, &state.ConsecutiveFailures,
		&checkedAt, &state.UpdatedAt,
	)
	if err != nil {
		return state, err
	}
	state.DetectedProtocol = nullStringPtr(detected)
	state.BaseReachable = baseReachable == 1
	state.ExitIP = nullStringPtr(exitIP)
	if latency.Valid {
		state.LatencyMS = intPtr(int(latency.Int64))
	}
	state.Country = nullStringPtr(country)
	state.CountryName = nullStringPtr(countryName)
	state.ContinentCode = nullStringPtr(continentCode)
	state.IPType = nullStringPtr(ipType)
	state.ASNOrg = nullStringPtr(asnOrg)
	state.GeoSource = nullStringPtr(geoSource)
	state.GeoUpdatedAt = nullStringPtr(geoUpdatedAt)
	state.LastError = nullStringPtr(lastError)
	state.CheckedAt = nullStringPtr(checkedAt)
	return state, nil
}

func scanProxyTargetState(rows *sql.Rows) (proxyTargetState, error) {
	var state proxyTargetState
	var apiReachable, latency sql.NullInt64
	var cloudflare, lastError, checkedAt sql.NullString
	var serviceReachable int
	err := rows.Scan(
		&state.ProxyID, &state.TargetProfile, &state.Status, &state.StatusChangedAt, &state.Capability, &serviceReachable,
		&apiReachable, &latency, &state.SuccessRate, &state.Grade, &cloudflare, &state.RecommendedUse,
		&state.FailureReason, &lastError, &state.ConsecutiveFailures, &checkedAt, &state.UpdatedAt,
	)
	if err != nil {
		return state, err
	}
	state.ServiceReachable = serviceReachable == 1
	if apiReachable.Valid {
		state.APIReachable = boolPtr(apiReachable.Int64 == 1)
	}
	if latency.Valid {
		state.LatencyMS = intPtr(int(latency.Int64))
	}
	state.CloudflareStatus = nullStringPtr(cloudflare)
	state.LastError = nullStringPtr(lastError)
	state.CheckedAt = nullStringPtr(checkedAt)
	return state, nil
}

func applyProbeCompatibility(item *proxyRecord, state proxyProbeState) {
	item.Status = state.Status
	item.Grade = ""
	item.LatencyMS = state.LatencyMS
	item.ExitIP = state.ExitIP
	item.Country = state.Country
	item.CountryName = state.CountryName
	item.ContinentCode = state.ContinentCode
	item.IPType = state.IPType
	item.ASNOrg = state.ASNOrg
	item.GeoSource = state.GeoSource
	item.GeoUpdatedAt = state.GeoUpdatedAt
	item.SuccessRate = state.SuccessRate
	item.TargetProfile = ""
	item.DetectedProtocol = state.DetectedProtocol
	item.ServiceReachable = false
	item.APIReachable = nil
	item.CloudflareStatus = nil
	item.RecommendedUse = ""
	item.LastError = state.LastError
	item.FailureReason = state.FailureReason
	item.FailureCount = state.ConsecutiveFailures
	item.LastCheckedAt = state.CheckedAt
	item.UpdatedAt = state.UpdatedAt
}

func applyTargetCompatibility(item *proxyRecord, state proxyTargetState) {
	item.Status = state.Status
	item.Grade = state.Grade
	item.LatencyMS = state.LatencyMS
	item.SuccessRate = state.SuccessRate
	item.TargetProfile = state.TargetProfile
	item.ServiceReachable = state.ServiceReachable
	item.APIReachable = state.APIReachable
	item.CloudflareStatus = state.CloudflareStatus
	item.RecommendedUse = state.RecommendedUse
	item.LastError = state.LastError
	item.FailureReason = state.FailureReason
	item.FailureCount = state.ConsecutiveFailures
	item.LastCheckedAt = state.CheckedAt
	item.UpdatedAt = state.UpdatedAt
}

func parseProxyText(text string, defaultProtocol string) []parsedProxy {
	defaultProtocol = normalizeProtocol(defaultProtocol)
	seen := map[string]bool{}
	items := []parsedProxy{}
	matches := proxyPattern.FindAllStringSubmatch(text, -1)
	names := proxyPattern.SubexpNames()
	for _, match := range matches {
		values := map[string]string{}
		for i, name := range names {
			if i > 0 && name != "" && i < len(match) {
				values[name] = match[i]
			}
		}
		scheme := ""
		if len(match) > 1 {
			scheme = match[1]
		}
		port, err := strconv.Atoi(values["port"])
		if err != nil {
			continue
		}
		protocol := normalizeProtocol(scheme)
		if protocol == "" {
			protocol = defaultProtocol
		}
		if protocol == "" {
			protocol = "auto"
		}
		item := parsedProxy{
			Host:     strings.ToLower(strings.TrimSpace(values["host"])),
			Port:     port,
			Protocol: protocol,
			Username: stringPtr(values["user"]),
			Password: stringPtr(values["pass"]),
		}
		if err := validateParsedProxy(item); err != nil {
			continue
		}
		key := proxyKey(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool { return proxyKey(items[i]) < proxyKey(items[j]) })
	return items
}

func normalizeProtocol(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "http", "https", "socks4", "socks5", "socks5h", "auto":
		return value
	default:
		return ""
	}
}

func validateParsedProxy(proxy parsedProxy) error {
	if proxy.Host == "" {
		return errors.New("host is required")
	}
	if proxy.Port < 1 || proxy.Port > 65535 {
		return errors.New("port out of range")
	}
	if normalizeProtocol(proxy.Protocol) == "" {
		return errors.New("unsupported protocol")
	}
	if ip := net.ParseIP(proxy.Host); ip != nil {
		if ip.IsUnspecified() {
			return errors.New("unspecified ip is not valid")
		}
		return nil
	}
	if strings.ContainsAny(proxy.Host, " /\\@") {
		return errors.New("invalid host")
	}
	return nil
}

func proxyKey(proxy parsedProxy) string {
	auth := ""
	if proxy.Username != nil && *proxy.Username != "" {
		password := ""
		if proxy.Password != nil {
			password = *proxy.Password
		}
		auth = url.UserPassword(*proxy.Username, password).String() + "@"
	}
	protocol := normalizeProtocol(proxy.Protocol)
	if protocol == "" {
		protocol = "auto"
	}
	return fmt.Sprintf("%s://%s%s:%d", protocol, auth, strings.ToLower(proxy.Host), proxy.Port)
}

func (p proxyRecord) ProxyURL() string {
	auth := ""
	if p.Username != nil && *p.Username != "" {
		password := ""
		if p.Password != nil {
			password = *p.Password
		}
		auth = url.UserPassword(*p.Username, password).String() + "@"
	}
	protocol := normalizeProtocol(firstNonEmpty(p.DetectedProtocolValue(), p.Protocol))
	if protocol == "" || protocol == "auto" {
		protocol = "http"
	}
	return fmt.Sprintf("%s://%s%s:%d", protocol, auth, p.IP, p.Port)
}

func (p proxyRecord) DetectedProtocolValue() string {
	if p.DetectedProtocol == nil {
		return ""
	}
	return *p.DetectedProtocol
}

func nullableString(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return strings.TrimSpace(*value)
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	text := value.String
	return &text
}

func nullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
