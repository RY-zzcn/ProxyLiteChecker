package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const appSettingsKey = "runtime"

type appSettings struct {
	ProxyPageSize            int      `json:"proxy_page_size"`
	FetchLimitPerSource      int      `json:"fetch_limit_per_source"`
	AutoFetchLowStockEnabled bool     `json:"auto_fetch_low_stock_enabled"`
	AutoFetchUntestedMinimum int      `json:"auto_fetch_untested_minimum"`
	AutoFetchCooldownMinutes int      `json:"auto_fetch_cooldown_minutes"`
	CheckStatus              string   `json:"check_status"`
	CheckTargetProfile       string   `json:"check_target_profile"`
	CheckLimit               int      `json:"check_limit"`
	CheckConcurrent          int      `json:"check_concurrent"`
	CheckRounds              int      `json:"check_rounds"`
	CheckRequestTimeout      int      `json:"check_request_timeout"`
	CheckHardTimeout         int      `json:"check_hard_timeout"`
	DeleteFailedOnCheck      bool     `json:"delete_failed_on_check"`
	RecheckExpiredEnabled    bool     `json:"recheck_expired_enabled"`
	AvailableTTLHours        int      `json:"available_ttl_hours"`
	AutoFetchEnabled         bool     `json:"auto_fetch_enabled"`
	AutoFetchIntervalMinutes int      `json:"auto_fetch_interval_minutes"`
	AutoFetchSourceIDs       []string `json:"auto_fetch_source_ids"`
	AutoCheckEnabled         bool     `json:"auto_check_enabled"`
	AutoCheckIntervalMinutes int      `json:"auto_check_interval_minutes"`
}

type scheduler struct {
	srv                 *server
	mu                  sync.Mutex
	nextFetchAt         time.Time
	nextLowStockFetchAt time.Time
	nextCheckAt         time.Time
	lastFetchAt         time.Time
	lastCheckAt         time.Time
	lastMaintenanceAt   time.Time
	lastUntestedCount   int
	lastFailedDeleted   int64
	lastExpiredRequeued int64
	lastMessage         string
	lastError           string
}

type schedulerStatus struct {
	Fetch       taskScheduleStatus `json:"fetch"`
	Check       taskScheduleStatus `json:"check"`
	Maintenance maintenanceStatus  `json:"maintenance"`
	Message     string             `json:"message"`
	Error       string             `json:"error,omitempty"`
}

type taskScheduleStatus struct {
	Enabled             bool   `json:"enabled"`
	IntervalMinutes     int    `json:"interval_minutes"`
	LastRunAt           string `json:"last_run_at,omitempty"`
	NextRunAt           string `json:"next_run_at,omitempty"`
	LowStockEnabled     bool   `json:"low_stock_enabled,omitempty"`
	UntestedMinimum     int    `json:"untested_minimum,omitempty"`
	CooldownMinutes     int    `json:"cooldown_minutes,omitempty"`
	NextLowStockCheckAt string `json:"next_low_stock_check_at,omitempty"`
	LastUntestedCount   int    `json:"last_untested_count,omitempty"`
}

type maintenanceStatus struct {
	LastRunAt       string `json:"last_run_at,omitempty"`
	FailedDeleted   int64  `json:"failed_deleted"`
	ExpiredRequeued int64  `json:"expired_requeued"`
}

func defaultAppSettings() appSettings {
	return appSettings{
		ProxyPageSize:            50,
		FetchLimitPerSource:      0,
		AutoFetchLowStockEnabled: false,
		AutoFetchUntestedMinimum: 5000,
		AutoFetchCooldownMinutes: 30,
		CheckStatus:              "untested",
		CheckTargetProfile:       "generic",
		CheckLimit:               500,
		CheckConcurrent:          50,
		CheckRounds:              1,
		CheckRequestTimeout:      6,
		CheckHardTimeout:         60,
		DeleteFailedOnCheck:      false,
		RecheckExpiredEnabled:    false,
		AvailableTTLHours:        24,
		AutoFetchEnabled:         false,
		AutoFetchIntervalMinutes: 360,
		AutoFetchSourceIDs:       nil,
		AutoCheckEnabled:         false,
		AutoCheckIntervalMinutes: 120,
	}
}

func (s *store) EnsureSettingsSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS app_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`)
	return err
}

func (s *store) AppSettings() (appSettings, error) {
	settings := defaultAppSettings()
	var raw string
	err := s.db.QueryRow("SELECT value FROM app_settings WHERE key = ?", appSettingsKey).Scan(&raw)
	if err != nil {
		if err == sql.ErrNoRows {
			return settings, nil
		}
		return settings, err
	}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		return defaultAppSettings(), err
	}
	return normalizeAppSettings(settings), nil
}

func (s *store) SaveAppSettings(settings appSettings) (appSettings, error) {
	settings = normalizeAppSettings(settings)
	raw, err := json.Marshal(settings)
	if err != nil {
		return settings, err
	}
	_, err = s.db.Exec(`
INSERT INTO app_settings (key, value, updated_at)
VALUES (?, ?, datetime('now'))
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = datetime('now')`,
		appSettingsKey, string(raw))
	return settings, err
}

func settingsFromPayload(current appSettings, payload map[string]any) appSettings {
	settings := current
	if value, ok := payload["proxy_page_size"]; ok {
		settings.ProxyPageSize = anyToInt(value)
	}
	if value, ok := payload["fetch_limit_per_source"]; ok {
		settings.FetchLimitPerSource = anyToInt(value)
	}
	if value, ok := payload["auto_fetch_low_stock_enabled"]; ok {
		settings.AutoFetchLowStockEnabled = parseBool(value, settings.AutoFetchLowStockEnabled)
	}
	if value, ok := payload["auto_fetch_untested_minimum"]; ok {
		settings.AutoFetchUntestedMinimum = anyToInt(value)
	}
	if value, ok := payload["auto_fetch_cooldown_minutes"]; ok {
		settings.AutoFetchCooldownMinutes = anyToInt(value)
	}
	if value, ok := payload["check_status"]; ok {
		settings.CheckStatus = optionalString(value, settings.CheckStatus)
	}
	if value, ok := payload["check_target_profile"]; ok {
		settings.CheckTargetProfile = optionalString(value, settings.CheckTargetProfile)
	}
	if value, ok := payload["check_limit"]; ok {
		settings.CheckLimit = anyToInt(value)
	}
	if value, ok := payload["check_concurrent"]; ok {
		settings.CheckConcurrent = anyToInt(value)
	}
	if value, ok := payload["check_rounds"]; ok {
		settings.CheckRounds = anyToInt(value)
	}
	if value, ok := payload["check_request_timeout"]; ok {
		settings.CheckRequestTimeout = anyToInt(value)
	}
	if value, ok := payload["check_hard_timeout"]; ok {
		settings.CheckHardTimeout = anyToInt(value)
	}
	if value, ok := payload["delete_failed_on_check"]; ok {
		settings.DeleteFailedOnCheck = parseBool(value, settings.DeleteFailedOnCheck)
	}
	if value, ok := payload["recheck_expired_enabled"]; ok {
		settings.RecheckExpiredEnabled = parseBool(value, settings.RecheckExpiredEnabled)
	}
	if value, ok := payload["available_ttl_hours"]; ok {
		settings.AvailableTTLHours = anyToInt(value)
	}
	if value, ok := payload["auto_fetch_enabled"]; ok {
		settings.AutoFetchEnabled = parseBool(value, settings.AutoFetchEnabled)
	}
	if value, ok := payload["auto_fetch_interval_minutes"]; ok {
		settings.AutoFetchIntervalMinutes = anyToInt(value)
	}
	if value, ok := payload["auto_fetch_source_ids"]; ok {
		settings.AutoFetchSourceIDs = anyToStringSlice(value)
	}
	if value, ok := payload["auto_check_enabled"]; ok {
		settings.AutoCheckEnabled = parseBool(value, settings.AutoCheckEnabled)
	}
	if value, ok := payload["auto_check_interval_minutes"]; ok {
		settings.AutoCheckIntervalMinutes = anyToInt(value)
	}
	return normalizeAppSettings(settings)
}

func normalizeAppSettings(settings appSettings) appSettings {
	settings.ProxyPageSize = clampInt(settings.ProxyPageSize, 20, 500)
	settings.FetchLimitPerSource = clampInt(settings.FetchLimitPerSource, 0, 50000)
	settings.AutoFetchUntestedMinimum = clampInt(settings.AutoFetchUntestedMinimum, 100, 1000000)
	settings.AutoFetchCooldownMinutes = clampInt(settings.AutoFetchCooldownMinutes, 1, 1440)
	settings.CheckStatus = normalizeCheckStatus(settings.CheckStatus)
	settings.CheckTargetProfile = normalizeTargetProfile(settings.CheckTargetProfile)
	settings.CheckLimit = clampInt(settings.CheckLimit, 1, 100000)
	settings.CheckConcurrent = clampInt(settings.CheckConcurrent, 1, 300)
	settings.CheckRounds = clampInt(settings.CheckRounds, 1, 5)
	settings.CheckRequestTimeout = clampInt(settings.CheckRequestTimeout, 2, 60)
	settings.CheckHardTimeout = clampInt(settings.CheckHardTimeout, settings.CheckRequestTimeout, 300)
	settings.AvailableTTLHours = clampInt(settings.AvailableTTLHours, 1, 8760)
	settings.AutoFetchIntervalMinutes = clampInt(settings.AutoFetchIntervalMinutes, 5, 10080)
	settings.AutoCheckIntervalMinutes = clampInt(settings.AutoCheckIntervalMinutes, 5, 10080)
	settings.AutoFetchSourceIDs = validSourceIDs(settings.AutoFetchSourceIDs)
	return settings
}

func normalizeCheckStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "all", "checked", "available", "failed", "untested":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return "untested"
	}
}

func validSourceIDs(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	known := sourceMap()
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "all" {
			return nil
		}
		if _, ok := known[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func newScheduler(srv *server) *scheduler {
	return &scheduler{srv: srv}
}

func (sc *scheduler) Start() {
	go sc.run()
}

func (sc *scheduler) run() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	sc.tick()
	for range ticker.C {
		sc.tick()
	}
}

func (sc *scheduler) tick() {
	settings, err := sc.srv.store.AppSettings()
	if err != nil {
		sc.setError(err)
		return
	}
	now := time.Now()
	sc.tickMaintenance(settings, now)
	sc.tickFetch(settings, now)
	sc.tickCheck(settings, now)
}

func (sc *scheduler) tickFetch(settings appSettings, now time.Time) {
	interval := time.Duration(settings.AutoFetchIntervalMinutes) * time.Minute
	cooldown := time.Duration(settings.AutoFetchCooldownMinutes) * time.Minute
	periodicDue := false
	lowStockDue := false
	lowStockBlocked := false

	sc.mu.Lock()
	if !settings.AutoFetchEnabled {
		sc.nextFetchAt = time.Time{}
	} else if sc.nextFetchAt.IsZero() {
		sc.nextFetchAt = now.Add(interval)
	} else if !now.Before(sc.nextFetchAt) {
		periodicDue = true
	}

	if !settings.AutoFetchLowStockEnabled {
		sc.nextLowStockFetchAt = time.Time{}
	} else if !sc.nextLowStockFetchAt.IsZero() && now.Before(sc.nextLowStockFetchAt) {
		lowStockBlocked = true
	}
	sc.mu.Unlock()

	if settings.AutoFetchLowStockEnabled && !lowStockBlocked {
		count, err := sc.srv.store.CountProxiesByStatus("untested")
		sc.mu.Lock()
		sc.lastUntestedCount = count
		sc.mu.Unlock()
		if err != nil {
			sc.setError(err)
			return
		}
		lowStockDue = count < settings.AutoFetchUntestedMinimum
	}

	if !periodicDue && !lowStockDue {
		return
	}

	_, err := sc.srv.StartFetchSourcesJob(map[string]any{
		"source_ids":        settings.AutoFetchSourceIDs,
		"limit_per_source":  settings.FetchLimitPerSource,
		"scheduled_trigger": "auto",
	})

	sc.mu.Lock()
	defer sc.mu.Unlock()
	if err != nil {
		sc.lastError = err.Error()
		sc.lastMessage = fmt.Sprintf("自动拉取延后：%s", err.Error())
		if periodicDue {
			sc.nextFetchAt = now.Add(time.Minute)
		}
		if lowStockDue {
			sc.nextLowStockFetchAt = now.Add(time.Minute)
		}
		return
	}
	sc.lastError = ""
	sc.lastFetchAt = now
	if periodicDue {
		sc.nextFetchAt = now.Add(interval)
	}
	if lowStockDue {
		sc.nextLowStockFetchAt = now.Add(cooldown)
	}
	if lowStockDue && !periodicDue {
		sc.lastMessage = fmt.Sprintf("待检代理低于 %d，自动拉取任务已启动", settings.AutoFetchUntestedMinimum)
		return
	}
	sc.lastMessage = "自动拉取任务已启动"
}

func (sc *scheduler) tickCheck(settings appSettings, now time.Time) {
	interval := time.Duration(settings.AutoCheckIntervalMinutes) * time.Minute
	sc.mu.Lock()
	if !settings.AutoCheckEnabled {
		sc.nextCheckAt = time.Time{}
		sc.mu.Unlock()
		return
	}
	if sc.nextCheckAt.IsZero() {
		sc.nextCheckAt = now.Add(interval)
		sc.mu.Unlock()
		return
	}
	if now.Before(sc.nextCheckAt) {
		sc.mu.Unlock()
		return
	}
	sc.mu.Unlock()

	_, err := sc.srv.StartCheckJob(map[string]any{
		"status":            settings.CheckStatus,
		"target_profile":    settings.CheckTargetProfile,
		"limit":             settings.CheckLimit,
		"concurrent":        settings.CheckConcurrent,
		"rounds":            settings.CheckRounds,
		"request_timeout":   settings.CheckRequestTimeout,
		"hard_timeout":      settings.CheckHardTimeout,
		"scheduled_trigger": "auto",
	})

	sc.mu.Lock()
	defer sc.mu.Unlock()
	if err != nil {
		sc.lastError = err.Error()
		sc.lastMessage = fmt.Sprintf("自动检测延后：%s", err.Error())
		sc.nextCheckAt = now.Add(time.Minute)
		return
	}
	sc.lastError = ""
	sc.lastCheckAt = now
	sc.nextCheckAt = now.Add(interval)
	sc.lastMessage = "自动检测任务已启动"
}

func (sc *scheduler) Reset(settings appSettings) {
	now := time.Now()
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if settings.AutoFetchEnabled {
		sc.nextFetchAt = now.Add(time.Duration(settings.AutoFetchIntervalMinutes) * time.Minute)
	} else {
		sc.nextFetchAt = time.Time{}
	}
	if settings.AutoFetchLowStockEnabled {
		sc.nextLowStockFetchAt = now
	} else {
		sc.nextLowStockFetchAt = time.Time{}
	}
	if settings.AutoCheckEnabled {
		sc.nextCheckAt = now.Add(time.Duration(settings.AutoCheckIntervalMinutes) * time.Minute)
	} else {
		sc.nextCheckAt = time.Time{}
	}
	sc.lastError = ""
	sc.lastMessage = "设置已保存"
}

func (sc *scheduler) Status(settings appSettings) schedulerStatus {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return schedulerStatus{
		Fetch: taskScheduleStatus{
			Enabled:             settings.AutoFetchEnabled,
			IntervalMinutes:     settings.AutoFetchIntervalMinutes,
			LastRunAt:           formatTime(sc.lastFetchAt),
			NextRunAt:           formatTime(sc.nextFetchAt),
			LowStockEnabled:     settings.AutoFetchLowStockEnabled,
			UntestedMinimum:     settings.AutoFetchUntestedMinimum,
			CooldownMinutes:     settings.AutoFetchCooldownMinutes,
			NextLowStockCheckAt: formatTime(sc.nextLowStockFetchAt),
			LastUntestedCount:   sc.lastUntestedCount,
		},
		Check: taskScheduleStatus{
			Enabled:         settings.AutoCheckEnabled,
			IntervalMinutes: settings.AutoCheckIntervalMinutes,
			LastRunAt:       formatTime(sc.lastCheckAt),
			NextRunAt:       formatTime(sc.nextCheckAt),
		},
		Maintenance: maintenanceStatus{
			LastRunAt:       formatTime(sc.lastMaintenanceAt),
			FailedDeleted:   sc.lastFailedDeleted,
			ExpiredRequeued: sc.lastExpiredRequeued,
		},
		Message: sc.lastMessage,
		Error:   sc.lastError,
	}
}

func (sc *scheduler) tickMaintenance(settings appSettings, now time.Time) {
	deleted, requeued, err := sc.srv.applyProxyMaintenance(settings)
	if err != nil {
		sc.setError(err)
		return
	}
	if deleted == 0 && requeued == 0 {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.lastMaintenanceAt = now
	sc.lastFailedDeleted = deleted
	sc.lastExpiredRequeued = requeued
	sc.lastError = ""
	sc.lastMessage = fmt.Sprintf("自动维护：删除失败 %d，过期转待检 %d", deleted, requeued)
}

func (s *server) applyProxyMaintenance(settings appSettings) (int64, int64, error) {
	var deleted int64
	var requeued int64
	var err error
	if settings.DeleteFailedOnCheck {
		deleted, err = s.store.DeleteFailedProxies()
		if err != nil {
			return 0, 0, err
		}
	}
	if settings.RecheckExpiredEnabled {
		requeued, err = s.store.RequeueExpiredAvailable(settings.AvailableTTLHours)
		if err != nil {
			return deleted, 0, err
		}
	}
	return deleted, requeued, nil
}

func (sc *scheduler) setError(err error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.lastError = err.Error()
	sc.lastMessage = "自动任务状态读取失败"
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}
