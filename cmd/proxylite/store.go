package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

type proxyRecord struct {
	ID               int     `json:"id"`
	ProxyKey         string  `json:"proxy_key"`
	IP               string  `json:"ip"`
	Port             int     `json:"port"`
	Protocol         string  `json:"protocol"`
	Username         *string `json:"username,omitempty"`
	Password         *string `json:"-"`
	Source           string  `json:"source"`
	Status           string  `json:"status"`
	Grade            string  `json:"grade"`
	LatencyMS        *int    `json:"latency_ms,omitempty"`
	ExitIP           *string `json:"exit_ip,omitempty"`
	Country          *string `json:"country,omitempty"`
	CountryName      *string `json:"country_name,omitempty"`
	ContinentCode    *string `json:"continent_code,omitempty"`
	IPType           *string `json:"ip_type,omitempty"`
	ASNOrg           *string `json:"asn_org,omitempty"`
	GeoSource        *string `json:"geo_source,omitempty"`
	GeoUpdatedAt     *string `json:"geo_updated_at,omitempty"`
	SuccessRate      float64 `json:"success_rate"`
	TargetProfile    string  `json:"target_profile"`
	DetectedProtocol *string `json:"detected_protocol,omitempty"`
	ServiceReachable bool    `json:"service_reachable"`
	APIReachable     *bool   `json:"api_reachable,omitempty"`
	CloudflareStatus *string `json:"cloudflare_status,omitempty"`
	RecommendedUse   string  `json:"recommended_use"`
	LastError        *string `json:"last_error,omitempty"`
	FailureReason    string  `json:"failure_reason,omitempty"`
	FailureCount     int     `json:"failure_count"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
	LastCheckedAt    *string `json:"last_checked_at,omitempty"`
}

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
			"country_name":   "TEXT",
			"continent_code": "TEXT",
			"geo_source":     "TEXT",
			"geo_updated_at": "TEXT",
		},
		"proxy_checks": {
			"country_name":   "TEXT",
			"continent_code": "TEXT",
			"geo_source":     "TEXT",
			"geo_updated_at": "TEXT",
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
`)
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
		"total":     0,
		"available": 0,
		"failed":    0,
		"untested":  0,
		"checking":  0,
	}
	rows, err := s.db.Query("SELECT status, COUNT(*) FROM proxies WHERE enabled = 1 GROUP BY status")
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
	availableRecords := anyToInt(stats["available"])
	stats["available_records"] = availableRecords
	gradeRows, err := s.db.Query("SELECT grade, COUNT(*) FROM proxies WHERE enabled = 1 AND status = 'available' AND grade != '' GROUP BY grade")
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
	if availableURLs == 0 && availableRecords > 0 {
		availableURLs, err = s.CountAvailableProxyURLs("")
		if err != nil {
			return nil, err
		}
	}
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
LEFT JOIN proxy_checks c ON c.proxy_id = p.id AND c.target_profile = ?
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
LEFT JOIN proxy_checks c ON c.proxy_id = p.id AND c.target_profile = ?
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
		_, err := tx.Exec(`
INSERT INTO proxies (proxy_key, ip, port, protocol, username, password, source, status, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 'untested', datetime('now', '+8 hours'), datetime('now', '+8 hours'))`,
			key, proxy.Host, proxy.Port, proxy.Protocol, username, password, firstNonEmpty(source, "manual"))
		return true, err
	}
	_, err := tx.Exec(`
UPDATE proxies
SET source = ?, username = ?, password = ?, updated_at = datetime('now', '+8 hours')
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
	totalQuery := "SELECT COUNT(*) FROM proxies" + where
	var total int
	if err := s.db.QueryRow(totalQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := clampInt(filter.Limit, 1, 100000)
	offset := clampInt(filter.Offset, 0, 10000000)
	query := `
SELECT id, proxy_key, ip, port, protocol, username, password, source, status, grade, latency_ms,
       exit_ip, country, country_name, continent_code, ip_type, asn_org, geo_source, geo_updated_at,
       success_rate, target_profile, detected_protocol, service_reachable, api_reachable, cloudflare_status, recommended_use,
       last_error, failure_count, created_at, updated_at, last_checked_at
FROM proxies` + where + `
ORDER BY
  CASE status WHEN 'available' THEN 0 WHEN 'untested' THEN 1 WHEN 'failed' THEN 2 ELSE 3 END,
  CASE grade WHEN 'A' THEN 0 WHEN 'B' THEN 1 WHEN 'C' THEN 2 ELSE 3 END,
  COALESCE(latency_ms, 999999),
  updated_at DESC
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
	return items, total, rows.Err()
}

func (s *store) listProxiesForTarget(filter proxyFilter, targetProfile string) ([]proxyRecord, int, error) {
	where, args := targetFilterWhere(filter)
	countArgs := append([]any{targetProfile}, args...)
	totalQuery := `
SELECT COUNT(*)
FROM proxies p
LEFT JOIN proxy_checks c ON c.proxy_id = p.id AND c.target_profile = ?` + where
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
       c.latency_ms, c.exit_ip, c.country, c.country_name, c.continent_code, c.ip_type, c.asn_org, c.geo_source, c.geo_updated_at,
       COALESCE(c.success_rate, 0) AS success_rate,
       COALESCE(c.target_profile, ?) AS target_profile,
       c.detected_protocol,
       COALESCE(c.service_reachable, 0) AS service_reachable,
       c.api_reachable, c.cloudflare_status,
       COALESCE(c.recommended_use, '') AS recommended_use,
       c.last_error, p.failure_count, p.created_at, COALESCE(c.updated_at, p.updated_at) AS updated_at, c.checked_at
FROM proxies p
LEFT JOIN proxy_checks c ON c.proxy_id = p.id AND c.target_profile = ?` + where + `
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
	return items, total, rows.Err()
}

func filterWhere(filter proxyFilter) (string, []any) {
	clauses := []string{"enabled = 1"}
	args := []any{}
	status := strings.ToLower(strings.TrimSpace(filter.Status))
	if status != "" && status != "all" {
		clauses = append(clauses, "status = ?")
		args = append(args, status)
	}
	target := strings.ToLower(strings.TrimSpace(filter.TargetProfile))
	if target != "" && target != "all" {
		clauses = append(clauses, "target_profile = ?")
		args = append(args, target)
	}
	query := strings.TrimSpace(filter.Query)
	if query != "" {
		clauses = append(clauses, "(proxy_key LIKE ? OR source LIKE ? OR ip LIKE ?)")
		like := "%" + query + "%"
		args = append(args, like, like, like)
	}
	if countries := normalizeCountryCodes(filter.Countries); len(countries) > 0 {
		clauses = append(clauses, "UPPER(COALESCE(country, '')) IN ("+placeholders(len(countries))+")")
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
		clauses = append(clauses, "UPPER(COALESCE(c.country, '')) IN ("+placeholders(len(countries))+")")
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
		where = " WHERE status != 'untested'"
	} else if status != "all" {
		where = " WHERE status = ?"
		args = append(args, status)
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
LEFT JOIN proxy_checks c ON c.proxy_id = p.id AND c.target_profile = ? ` + where + `
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
	status := strings.ToLower(strings.TrimSpace(result.Status))
	if status == "" {
		status = "failed"
	}
	targetProfile := normalizeTargetProfile(result.TargetProfile)
	failureExpr := "failure_count"
	if status == "failed" {
		failureExpr = "failure_count + 1"
	} else if status == "available" {
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
	updateResult, err := tx.Exec(`
UPDATE proxies
SET status = ?,
    grade = ?,
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
		status,
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
	)
	if err != nil {
		return err
	}
	if rows, _ := updateResult.RowsAffected(); rows == 0 {
		return sql.ErrNoRows
	}
	_, err = tx.Exec(`
INSERT INTO proxy_checks (
  proxy_id, target_profile, status, grade, latency_ms, exit_ip, country, country_name, continent_code, ip_type, asn_org, geo_source, geo_updated_at,
  success_rate, detected_protocol, service_reachable, api_reachable, cloudflare_status,
  recommended_use, last_error, checked_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now', '+8 hours'), datetime('now', '+8 hours'))
ON CONFLICT(proxy_id, target_profile) DO UPDATE SET
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
		status,
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
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *store) DeleteProxyByID(id int) error {
	_, err := s.db.Exec("DELETE FROM proxies WHERE id = ?", id)
	return err
}

func (s *store) DeleteFailedProxies() (int64, error) {
	result, err := s.db.Exec("DELETE FROM proxies WHERE status = 'failed'")
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
  AND status = 'untested'
  AND COALESCE(last_checked_at, updated_at, created_at) <= datetime('now', '+8 hours', ?)`,
		fmt.Sprintf("-%d hours", ttlHours),
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *store) RequeueExpiredAvailable(ttlHours int) (int64, error) {
	ttlHours = clampInt(ttlHours, 1, 8760)
	targetResult, err := s.db.Exec(`
UPDATE proxy_checks
SET status = 'untested',
    updated_at = datetime('now', '+8 hours')
WHERE status = 'available'
  AND COALESCE(checked_at, updated_at) <= datetime('now', '+8 hours', ?)`,
		fmt.Sprintf("-%d hours", ttlHours),
	)
	if err != nil {
		return 0, err
	}
	result, err := s.db.Exec(`
UPDATE proxies
SET status = 'untested',
    updated_at = datetime('now', '+8 hours')
WHERE enabled = 1
  AND status = 'available'
  AND COALESCE(last_checked_at, updated_at, created_at) <= datetime('now', '+8 hours', ?)`,
		fmt.Sprintf("-%d hours", ttlHours),
	)
	if err != nil {
		return 0, err
	}
	proxyRows, _ := result.RowsAffected()
	targetRows, _ := targetResult.RowsAffected()
	return proxyRows + targetRows, nil
}

func (s *store) CountProxiesByStatus(status string) (int, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM proxies WHERE enabled = 1 AND status = ?", status).Scan(&count)
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
			countryClause = " AND UPPER(COALESCE(c.country, '')) IN (" + placeholders(len(countries)) + ")"
			for _, country := range countries {
				args = append(args, country)
			}
		}
		query := `
SELECT COUNT(*) FROM (
  SELECT
    CASE
      WHEN COALESCE(NULLIF(c.detected_protocol, ''), p.protocol, '') IN ('', 'auto') THEN 'http'
      ELSE COALESCE(NULLIF(c.detected_protocol, ''), p.protocol)
    END AS protocol,
    COALESCE(p.username, '') AS username,
    COALESCE(p.password, '') AS password,
    LOWER(p.ip) AS ip,
    p.port
  FROM proxies p
  LEFT JOIN proxy_checks c ON c.proxy_id = p.id AND c.target_profile = ?
  WHERE p.enabled = 1
    AND COALESCE(c.status, 'untested') = 'available'
` + countryClause + `
  GROUP BY 1, 2, 3, 4, 5
)`
		var count int
		err := s.db.QueryRow(query, args...).Scan(&count)
		return count, err
	}
	args := []any{}
	countryClause := ""
	if len(countries) > 0 {
		countryClause = " AND UPPER(COALESCE(country, '')) IN (" + placeholders(len(countries)) + ")"
		for _, country := range countries {
			args = append(args, country)
		}
	}
	query := `
SELECT COUNT(*) FROM (
  SELECT
    CASE
      WHEN COALESCE(NULLIF(detected_protocol, ''), protocol, '') IN ('', 'auto') THEN 'http'
      ELSE COALESCE(NULLIF(detected_protocol, ''), protocol)
    END AS protocol,
    COALESCE(username, '') AS username,
    COALESCE(password, '') AS password,
    LOWER(ip) AS ip,
    port
  FROM proxies
  WHERE enabled = 1
    AND status = 'available'
` + countryClause + `
  GROUP BY 1, 2, 3, 4, 5
)`
	var count int
	err := s.db.QueryRow(query, args...).Scan(&count)
	return count, err
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
		countryClause = " AND UPPER(COALESCE(c.country, '')) IN (" + placeholders(len(countries)) + ")"
		for _, country := range countries {
			args = append(args, country)
		}
	}
	query := `
SELECT COUNT(*) FROM (
  SELECT
    CASE
      WHEN COALESCE(NULLIF(c.detected_protocol, ''), p.protocol, '') IN ('', 'auto') THEN 'http'
      ELSE COALESCE(NULLIF(c.detected_protocol, ''), p.protocol)
    END AS protocol,
    COALESCE(p.username, '') AS username,
    COALESCE(p.password, '') AS password,
    LOWER(p.ip) AS ip,
    p.port
  FROM proxies p
  JOIN proxy_checks c ON c.proxy_id = p.id
  WHERE p.enabled = 1
    AND c.status = 'available'
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
	listFilter := proxyFilter{Status: "available", TargetProfile: filter.TargetProfile, Countries: filter.Countries, Limit: 100000}
	if filter.Limit > 0 {
		listFilter.Limit = filter.Limit
	}
	items, _, err := s.ListProxies(listFilter)
	return items, err
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
