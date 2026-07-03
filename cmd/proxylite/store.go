package main

import (
	"database/sql"
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
	IPType           *string `json:"ip_type,omitempty"`
	ASNOrg           *string `json:"asn_org,omitempty"`
	SuccessRate      float64 `json:"success_rate"`
	TargetProfile    string  `json:"target_profile"`
	DetectedProtocol *string `json:"detected_protocol,omitempty"`
	ServiceReachable bool    `json:"service_reachable"`
	APIReachable     *bool   `json:"api_reachable,omitempty"`
	CloudflareStatus *string `json:"cloudflare_status,omitempty"`
	RecommendedUse   string  `json:"recommended_use"`
	LastError        *string `json:"last_error,omitempty"`
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
	Limit         int
	Offset        int
}

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
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
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
  ip_type TEXT,
  asn_org TEXT,
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
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  last_checked_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_proxies_status ON proxies(status);
CREATE INDEX IF NOT EXISTS idx_proxies_target ON proxies(target_profile);
CREATE INDEX IF NOT EXISTS idx_proxies_source ON proxies(source);
CREATE INDEX IF NOT EXISTS idx_proxies_quality ON proxies(status, grade, latency_ms);
`); err != nil {
		return err
	}
	username := firstNonEmpty(adminUsername, "admin")
	var count int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM users WHERE username = ?", username).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		_, err := s.db.Exec(
			"INSERT INTO users (username, password_hash, created_at, updated_at) VALUES (?, ?, datetime('now'), datetime('now'))",
			username,
			hashPassword(firstNonEmpty(adminPassword, defaultAdminPassword)),
		)
		return err
	}
	return nil
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
	rows, err := s.db.Query("SELECT status, COUNT(*) FROM proxies GROUP BY status")
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
	gradeRows, err := s.db.Query("SELECT grade, COUNT(*) FROM proxies WHERE status = 'available' AND grade != '' GROUP BY grade")
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
	return stats, nil
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
VALUES (?, ?, ?, ?, ?, ?, ?, 'untested', datetime('now'), datetime('now'))`,
			key, proxy.Host, proxy.Port, proxy.Protocol, username, password, firstNonEmpty(source, "manual"))
		return true, err
	}
	_, err := tx.Exec(`
UPDATE proxies
SET source = ?, username = ?, password = ?, updated_at = datetime('now')
WHERE proxy_key = ?`,
		firstNonEmpty(source, "manual"), username, password, key)
	return false, err
}

func (s *store) ListProxies(filter proxyFilter) ([]proxyRecord, int, error) {
	where, args := filterWhere(filter)
	totalQuery := "SELECT COUNT(*) FROM proxies" + where
	var total int
	if err := s.db.QueryRow(totalQuery, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := clampInt(filter.Limit, 1, 100000)
	offset := clampInt(filter.Offset, 0, 10000000)
	query := `
SELECT id, proxy_key, ip, port, protocol, username, password, source, status, grade, latency_ms, exit_ip, country, ip_type, asn_org,
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
	return " WHERE " + strings.Join(clauses, " AND "), args
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
	where := "WHERE enabled = 1"
	args := []any{}
	switch status {
	case "all":
	case "checked":
		where += " AND status != 'untested'"
	case "available", "failed", "untested":
		where += " AND status = ?"
		args = append(args, status)
	default:
		return nil, errors.New("status must be available, failed, untested, checked, or all")
	}
	query := `
SELECT id, proxy_key, ip, port, protocol, username, password, source
FROM proxies ` + where + `
ORDER BY
  CASE status WHEN 'untested' THEN 0 WHEN 'failed' THEN 1 WHEN 'available' THEN 2 ELSE 3 END,
  CASE WHEN status = 'untested' AND last_checked_at IS NOT NULL THEN 0 ELSE 1 END,
  COALESCE(last_checked_at, created_at) ASC,
  id ASC
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
	_, err := s.db.Exec(`
UPDATE proxies
SET status = ?,
    grade = ?,
    latency_ms = ?,
    exit_ip = ?,
    country = ?,
    ip_type = ?,
    asn_org = ?,
    success_rate = ?,
    target_profile = ?,
    detected_protocol = ?,
    service_reachable = ?,
    api_reachable = ?,
    cloudflare_status = ?,
    recommended_use = ?,
    last_error = ?,
    failure_count = `+failureExpr+`,
    updated_at = datetime('now'),
    last_checked_at = datetime('now')
WHERE id = ?`,
		status,
		result.Grade,
		nullableInt(result.LatencyMS),
		nullableString(result.ExitIP),
		nullableString(result.Country),
		nullableString(result.IPType),
		nullableString(result.ASNOrg),
		result.SuccessRate,
		firstNonEmpty(result.TargetProfile, "generic"),
		nullableString(result.DetectedProtocol),
		boolToInt(result.ServiceReachable),
		apiReachable,
		nullableString(result.CloudflareStatus),
		result.RecommendedUse,
		nullableString(result.LastError),
		result.ProxyID,
	)
	return err
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

func (s *store) RequeueExpiredAvailable(ttlHours int) (int64, error) {
	ttlHours = clampInt(ttlHours, 1, 8760)
	result, err := s.db.Exec(`
UPDATE proxies
SET status = 'untested',
    updated_at = datetime('now')
WHERE enabled = 1
  AND status = 'available'
  AND COALESCE(last_checked_at, updated_at, created_at) <= datetime('now', ?)`,
		fmt.Sprintf("-%d hours", ttlHours),
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *store) CountProxiesByStatus(status string) (int, error) {
	status = strings.ToLower(strings.TrimSpace(status))
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM proxies WHERE enabled = 1 AND status = ?", status).Scan(&count)
	return count, err
}

func (s *store) ExportAvailable(targetProfile string, limit int) ([]proxyRecord, error) {
	filter := proxyFilter{Status: "available", TargetProfile: targetProfile, Limit: 100000}
	if limit > 0 {
		filter.Limit = limit
	}
	items, _, err := s.ListProxies(filter)
	return items, err
}

func (s *store) AvailableProxyURLs(limit int, targetProfile string) ([]string, error) {
	items, err := s.ExportAvailable(targetProfile, limit)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.ProxyURL())
	}
	return out, nil
}

func scanProxy(rows *sql.Rows) (proxyRecord, error) {
	var item proxyRecord
	var username, password, exitIP, country, ipType, asnOrg, detected, cloudflare, lastError, lastChecked sql.NullString
	var latencyInt sql.NullInt64
	var apiReachableInt sql.NullInt64
	var serviceReachable int
	err := rows.Scan(
		&item.ID, &item.ProxyKey, &item.IP, &item.Port, &item.Protocol, &username, &password, &item.Source, &item.Status, &item.Grade,
		&latencyInt, &exitIP, &country, &ipType, &asnOrg, &item.SuccessRate, &item.TargetProfile, &detected, &serviceReachable,
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
	item.IPType = nullStringPtr(ipType)
	item.ASNOrg = nullStringPtr(asnOrg)
	item.DetectedProtocol = nullStringPtr(detected)
	item.ServiceReachable = serviceReachable == 1
	if apiReachableInt.Valid {
		item.APIReachable = boolPtr(apiReachableInt.Int64 == 1)
	}
	item.CloudflareStatus = nullStringPtr(cloudflare)
	item.LastError = nullStringPtr(lastError)
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

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
