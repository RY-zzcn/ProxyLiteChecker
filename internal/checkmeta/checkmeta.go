package checkmeta

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/geoip2-golang"
)

const defaultIPMetadataEndpoint = "http://ip-api.com/json"
const defaultGeoIPCountryURL = "https://github.com/P3TERX/GeoLite.mmdb/raw/download/GeoLite2-Country.mmdb"

type Metadata struct {
	Country       string
	CountryName   string
	ContinentCode string
	IPType        string
	ASNOrg        string
	GeoSource     string
	GeoUpdatedAt  time.Time
}

type GeoIPStatus struct {
	Enabled           bool   `json:"enabled"`
	CountryLoaded     bool   `json:"country_loaded"`
	CountryPath       string `json:"country_path,omitempty"`
	CountryBuildEpoch uint   `json:"country_build_epoch,omitempty"`
	CountrySHA256     string `json:"country_sha256,omitempty"`
	UpdateEnabled     bool   `json:"update_enabled"`
	UpdateInterval    string `json:"update_interval,omitempty"`
	LastLoadError     string `json:"last_load_error,omitempty"`
	LastLoadAt        string `json:"last_load_at,omitempty"`
	LastUpdateAt      string `json:"last_update_at,omitempty"`
	CountrySourceHint string `json:"country_source_hint"`
}

type geoIPReaders struct {
	mu             sync.RWMutex
	initMu         sync.Mutex
	initialized    bool
	updateOnce     sync.Once
	updateMu       sync.Mutex
	enabled        bool
	country        *geoip2.Reader
	countryPath    string
	countryURL     string
	countryBuild   uint
	countrySHA256  string
	updateEnabled  bool
	updateInterval time.Duration
	lastLoadAt     time.Time
	lastUpdateAt   time.Time
	lastLoadError  string
}

var geoIP = &geoIPReaders{enabled: true}
var loadGeoIPDatabasesFromEnv = LoadGeoIPDatabasesFromEnv

func DetectCloudflareStatus(statusCode int, headers http.Header, body string) string {
	server := strings.ToLower(headers.Get("Server"))
	hasCFHeader := headers.Get("CF-Ray") != "" || strings.Contains(server, "cloudflare")
	text := strings.ToLower(body)
	if len(text) > 2000 {
		text = text[:2000]
	}
	challengeMarkers := []string{
		"cf-browser-verification",
		"cf-challenge",
		"checking your browser",
		"turnstile",
		"challenge-platform",
	}
	for _, marker := range challengeMarkers {
		if strings.Contains(text, marker) {
			return "challenge"
		}
	}
	if hasCFHeader && (statusCode == http.StatusForbidden || statusCode == http.StatusTooManyRequests || statusCode == http.StatusServiceUnavailable) {
		return "blocked"
	}
	if hasCFHeader {
		return "behind_cf"
	}
	return "not_cf"
}

func EnrichIP(ctx context.Context, client *http.Client, endpoint string, ip string) Metadata {
	ip = strings.TrimSpace(ip)
	metadata := Metadata{IPType: ClassifyIPType(ip)}
	if ip == "" {
		return metadata
	}
	local, localOK := LookupGeoIP(ip)
	if localOK {
		metadata = mergeMetadata(metadata, local)
	}
	remote, remoteOK := LookupIPMetadata(ctx, client, endpoint, ip)
	if remoteOK {
		metadata = mergeMetadata(metadata, remote)
	}
	if localOK {
		metadata = mergeMetadata(metadata, local)
	}
	return metadata
}

func ClassifyIPType(ip string) string {
	value := net.ParseIP(strings.TrimSpace(ip))
	if value == nil {
		if strings.TrimSpace(ip) == "" {
			return ""
		}
		return "unknown"
	}
	if value.IsPrivate() {
		return "private"
	}
	if value.IsLoopback() {
		return "loopback"
	}
	if value.IsMulticast() {
		return "multicast"
	}
	if value.IsUnspecified() || value.IsLinkLocalUnicast() || !value.IsGlobalUnicast() {
		return "reserved"
	}
	return "public"
}

func LookupIPMetadata(ctx context.Context, client *http.Client, endpoint string, ip string) (Metadata, bool) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return Metadata{}, false
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("PLC_IP_METADATA_ENDPOINT"))
	}
	if endpoint == "" {
		endpoint = defaultIPMetadataEndpoint
	}
	requestURL, err := buildMetadataURL(endpoint, ip)
	if err != nil {
		return Metadata{}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return Metadata{}, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return Metadata{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Metadata{}, false
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Metadata{}, false
	}
	if strings.ToLower(strings.TrimSpace(stringValue(payload["status"]))) != "success" {
		return Metadata{}, false
	}
	return Metadata{
		Country:       truncateString(strings.ToUpper(stringValue(payload["countryCode"])), 64),
		CountryName:   truncateString(stringValue(payload["country"]), 128),
		ContinentCode: truncateString(strings.ToUpper(stringValue(payload["continentCode"])), 16),
		IPType:        ClassifyIPMetadataType(payload),
		ASNOrg:        truncateString(firstNonEmpty(stringValue(payload["org"]), stringValue(payload["isp"]), stringValue(payload["as"])), 128),
		GeoSource:     "endpoint",
		GeoUpdatedAt:  time.Now(),
	}, true
}

func buildMetadataURL(endpoint string, ip string) (string, error) {
	if strings.Contains(endpoint, "{ip}") {
		endpoint = strings.ReplaceAll(endpoint, "{ip}", url.PathEscape(ip))
	} else {
		endpoint = strings.TrimRight(endpoint, "/") + "/" + url.PathEscape(ip)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	if query.Get("fields") == "" {
		query.Set("fields", "status,country,countryCode,continentCode,isp,org,as,proxy,hosting,mobile,query")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func ClassifyIPMetadataType(data map[string]any) string {
	if boolValue(data["mobile"]) {
		return "mobile"
	}
	if boolValue(data["proxy"]) {
		return "proxy"
	}
	if boolValue(data["hosting"]) {
		return "datacenter"
	}
	org := strings.Join([]string{
		stringValue(data["org"]),
		stringValue(data["isp"]),
		stringValue(data["as"]),
	}, " ")
	return ClassifyOrganizationIPType(org)
}

func ClassifyOrganizationIPType(org string) string {
	org = strings.ToLower(strings.TrimSpace(org))
	datacenterKeywords := []string{
		"amazon", "aws", "google", "cloudflare", "azure", "microsoft",
		"digitalocean", "linode", "vultr", "hetzner", "ovh", "oracle",
		"alibaba", "tencent", "datacenter", "hosting", "server", "cloud",
	}
	for _, keyword := range datacenterKeywords {
		if strings.Contains(org, keyword) {
			return "datacenter"
		}
	}
	residentialKeywords := []string{
		"broadband", "cable", "communications", "fiber", "fibre", "ftth",
		"isp", "telecom", "telecommunications", "wireless", "internet service",
	}
	for _, keyword := range residentialKeywords {
		if strings.Contains(org, keyword) {
			return "residential"
		}
	}
	return "public"
}

func LookupGeoIP(ip string) (Metadata, bool) {
	_ = InitializeGeoIPDatabasesFromEnv()
	value := net.ParseIP(strings.TrimSpace(ip))
	if value == nil {
		return Metadata{}, false
	}
	geoIP.mu.RLock()
	defer geoIP.mu.RUnlock()
	reader := geoIP.country
	enabled := geoIP.enabled
	if !enabled || reader == nil {
		return Metadata{}, false
	}
	record, err := reader.Country(value)
	if err != nil {
		return Metadata{}, false
	}
	country := strings.ToUpper(strings.TrimSpace(firstNonEmpty(record.Country.IsoCode, record.RegisteredCountry.IsoCode)))
	if country == "" {
		return Metadata{}, false
	}
	return Metadata{
		Country:       country,
		CountryName:   truncateString(firstNonEmpty(geoName(record.Country.Names), geoName(record.RegisteredCountry.Names)), 128),
		ContinentCode: truncateString(strings.ToUpper(strings.TrimSpace(record.Continent.Code)), 16),
		GeoSource:     "mmdb",
		GeoUpdatedAt:  time.Now(),
	}, true
}

func InitializeGeoIPDatabasesFromEnv() error {
	geoIP.initMu.Lock()
	defer geoIP.initMu.Unlock()
	if geoIP.initialized {
		return nil
	}
	if err := loadGeoIPDatabasesFromEnv(); err != nil {
		return err
	}
	geoIP.initialized = true
	return nil
}

func LoadGeoIPDatabasesFromEnv() error {
	cfg := geoIPConfigFromEnv()
	if !cfg.Enabled {
		return LoadGeoIPDatabase("", "", false)
	}
	return LoadGeoIPDatabase(cfg.CountryPath, cfg.CountryURL, cfg.AutoDownload)
}

func LoadGeoIPDatabase(countryPath string, countryURL string, autoDownload bool) error {
	countryPath = strings.TrimSpace(countryPath)
	countryURL = strings.TrimSpace(countryURL)
	if countryPath == "" {
		replaceGeoIPReader(nil, "", "", 0, "")
		return nil
	}
	if _, err := os.Stat(countryPath); err != nil {
		if os.IsNotExist(err) && autoDownload && countryURL != "" {
			if downloadErr := downloadGeoIPDatabase(countryURL, countryPath); downloadErr != nil {
				setGeoIPLoadError(downloadErr)
				return downloadErr
			}
		} else if !os.IsNotExist(err) {
			setGeoIPLoadError(err)
			return err
		}
	}
	reader, build, checksum, err := openGeoIPDatabase(countryPath)
	if err != nil {
		if os.IsNotExist(err) {
			replaceGeoIPReader(nil, countryPath, countryURL, 0, "")
			return nil
		}
		setGeoIPLoadError(err)
		return err
	}
	replaceGeoIPReader(reader, countryPath, countryURL, build, checksum)
	return nil
}

func StartGeoIPAutoUpdateFromEnv() {
	cfg := geoIPConfigFromEnv()
	if !cfg.Enabled || !cfg.UpdateEnabled || cfg.CountryPath == "" || cfg.CountryURL == "" || cfg.UpdateInterval <= 0 {
		geoIP.mu.Lock()
		geoIP.updateEnabled = false
		geoIP.updateInterval = cfg.UpdateInterval
		geoIP.mu.Unlock()
		return
	}
	geoIP.mu.Lock()
	geoIP.updateEnabled = true
	geoIP.updateInterval = cfg.UpdateInterval
	geoIP.mu.Unlock()
	geoIP.updateOnce.Do(func() {
		go func() {
			ticker := time.NewTicker(cfg.UpdateInterval)
			defer ticker.Stop()
			for range ticker.C {
				if err := UpdateGeoIPDatabaseFromEnv(); err != nil {
					setGeoIPLoadError(err)
				}
			}
		}()
	})
}

func UpdateGeoIPDatabaseFromEnv() error {
	geoIP.updateMu.Lock()
	defer geoIP.updateMu.Unlock()
	cfg := geoIPConfigFromEnv()
	if !cfg.Enabled || cfg.CountryPath == "" || cfg.CountryURL == "" {
		return nil
	}
	if err := downloadGeoIPDatabase(cfg.CountryURL, cfg.CountryPath); err != nil {
		return err
	}
	if err := LoadGeoIPDatabase(cfg.CountryPath, cfg.CountryURL, false); err != nil {
		return err
	}
	geoIP.mu.Lock()
	geoIP.lastUpdateAt = time.Now()
	geoIP.mu.Unlock()
	return nil
}

func GeoIPReaderStatus() GeoIPStatus {
	_ = InitializeGeoIPDatabasesFromEnv()
	geoIP.mu.RLock()
	defer geoIP.mu.RUnlock()
	sourceHint := "endpoint"
	if geoIP.country != nil {
		sourceHint = "mmdb"
	}
	return GeoIPStatus{
		Enabled:           geoIP.enabled,
		CountryLoaded:     geoIP.country != nil,
		CountryPath:       geoIP.countryPath,
		CountryBuildEpoch: geoIP.countryBuild,
		CountrySHA256:     geoIP.countrySHA256,
		UpdateEnabled:     geoIP.updateEnabled,
		UpdateInterval:    geoIP.updateInterval.String(),
		LastLoadError:     geoIP.lastLoadError,
		LastLoadAt:        formatTime(geoIP.lastLoadAt),
		LastUpdateAt:      formatTime(geoIP.lastUpdateAt),
		CountrySourceHint: sourceHint,
	}
}

func openGeoIPDatabase(path string) (*geoip2.Reader, uint, string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, 0, "", nil
	}
	if _, err := os.Stat(path); err != nil {
		return nil, 0, "", err
	}
	reader, err := geoip2.Open(path)
	if err != nil {
		return nil, 0, "", err
	}
	if _, err := reader.Country(net.ParseIP("8.8.8.8")); err != nil {
		_ = reader.Close()
		return nil, 0, "", err
	}
	checksum, err := fileSHA256(path)
	if err != nil {
		_ = reader.Close()
		return nil, 0, "", err
	}
	return reader, reader.Metadata().BuildEpoch, checksum, nil
}

func replaceGeoIPReader(reader *geoip2.Reader, path string, downloadURL string, build uint, checksum string) {
	geoIP.mu.Lock()
	old := geoIP.country
	geoIP.enabled = path != ""
	geoIP.country = reader
	geoIP.countryPath = path
	geoIP.countryURL = downloadURL
	geoIP.countryBuild = build
	geoIP.countrySHA256 = checksum
	geoIP.lastLoadAt = time.Now()
	geoIP.lastLoadError = ""
	geoIP.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

func downloadGeoIPDatabase(downloadURL string, dbPath string) error {
	downloadURL = strings.TrimSpace(downloadURL)
	dbPath = strings.TrimSpace(dbPath)
	if downloadURL == "" || dbPath == "" {
		return fmt.Errorf("geoip download url or database path is empty")
	}
	dir := filepath.Dir(dbPath)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	client := &http.Client{Timeout: time.Duration(clampInt(envInt("PLC_GEOIP_DOWNLOAD_TIMEOUT_SECONDS", 60), 5, 600)) * time.Second}
	if proxyURL := strings.TrimSpace(os.Getenv("PLC_GEOIP_UPDATE_PROXY")); proxyURL != "" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return err
		}
		client.Transport = &http.Transport{Proxy: http.ProxyURL(parsed)}
	}
	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("geoip download HTTP %s", resp.Status)
	}
	tempFile, err := os.CreateTemp(dir, ".geoip-*.mmdb")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	cleanup := true
	defer func() {
		if tempFile != nil {
			_ = tempFile.Close()
		}
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	written, err := io.Copy(tempFile, resp.Body)
	if err != nil {
		return err
	}
	if resp.ContentLength > 0 && written < resp.ContentLength {
		return fmt.Errorf("incomplete geoip download (%d/%d bytes)", written, resp.ContentLength)
	}
	if err := tempFile.Sync(); err != nil {
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	tempFile = nil
	reader, _, _, err := openGeoIPDatabase(tempPath)
	if err != nil {
		return err
	}
	_ = reader.Close()
	if err := os.Rename(tempPath, dbPath); err != nil {
		return err
	}
	cleanup = false
	return nil
}

type geoIPConfig struct {
	Enabled        bool
	CountryPath    string
	CountryURL     string
	AutoDownload   bool
	UpdateEnabled  bool
	UpdateInterval time.Duration
}

func geoIPConfigFromEnv() geoIPConfig {
	dbDir := strings.TrimSpace(os.Getenv("PLC_GEOIP_DB_DIR"))
	if dbDir == "" {
		dbDir = filepath.Join("data", "geoip")
	}
	countryPath := strings.TrimSpace(os.Getenv("PLC_GEOIP_COUNTRY_DB"))
	if countryPath == "" {
		countryPath = filepath.Join(dbDir, "current", "GeoLite2-Country.mmdb")
	}
	updateHours := clampInt(envInt("PLC_GEOIP_UPDATE_INTERVAL_HOURS", 24), 1, 24*30)
	return geoIPConfig{
		Enabled:        envBool("PLC_GEOIP_ENABLED", true),
		CountryPath:    countryPath,
		CountryURL:     strings.TrimSpace(firstNonEmpty(os.Getenv("PLC_GEOIP_COUNTRY_URL"), defaultGeoIPCountryURL)),
		AutoDownload:   envBool("PLC_GEOIP_AUTO_DOWNLOAD", true),
		UpdateEnabled:  envBool("PLC_GEOIP_UPDATE_ENABLED", true),
		UpdateInterval: time.Duration(updateHours) * time.Hour,
	}
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func setGeoIPLoadError(err error) {
	if err == nil {
		return
	}
	geoIP.mu.Lock()
	defer geoIP.mu.Unlock()
	geoIP.lastLoadAt = time.Now()
	geoIP.lastLoadError = err.Error()
}

func geoName(names map[string]string) string {
	if len(names) == 0 {
		return ""
	}
	if value := strings.TrimSpace(names["en"]); value != "" {
		return value
	}
	for _, value := range names {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func mergeMetadata(base Metadata, next Metadata) Metadata {
	if next.Country != "" {
		base.Country = next.Country
	}
	if next.CountryName != "" {
		base.CountryName = next.CountryName
	}
	if next.ContinentCode != "" {
		base.ContinentCode = next.ContinentCode
	}
	if next.IPType != "" {
		base.IPType = next.IPType
	}
	if next.ASNOrg != "" {
		base.ASNOrg = next.ASNOrg
	}
	if next.GeoSource != "" {
		base.GeoSource = next.GeoSource
	}
	if !next.GeoUpdatedAt.IsZero() {
		base.GeoUpdatedAt = next.GeoUpdatedAt
	}
	return base
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "1" || strings.EqualFold(typed, "true") || strings.EqualFold(typed, "yes")
	case float64:
		return typed != 0
	default:
		return false
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func truncateString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit > 0 && len(value) > limit {
		return value[:limit]
	}
	return value
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	switch strings.ToLower(value) {
	case "1", "true", "yes", "y", "on", "enabled":
		return true
	case "0", "false", "no", "n", "off", "disabled":
		return false
	default:
		return fallback
	}
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func clampInt(value int, minValue int, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Format(time.RFC3339)
}
