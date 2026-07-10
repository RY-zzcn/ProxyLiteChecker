package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
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
	CheckTargetProfiles      []string `json:"check_target_profiles"`
	CheckLimit               int      `json:"check_limit"`
	CheckConcurrent          int      `json:"check_concurrent"`
	CheckRounds              int      `json:"check_rounds"`
	CheckRequestTimeout      int      `json:"check_request_timeout"`
	CheckHardTimeout         int      `json:"check_hard_timeout"`
	DeleteFailedOnCheck      bool     `json:"delete_failed_on_check"`
	RecheckExpiredEnabled    bool     `json:"recheck_expired_enabled"`
	AvailableTTLHours        int      `json:"available_ttl_hours"`
	DeleteExpiredUntested    bool     `json:"delete_expired_untested"`
	UntestedTTLHours         int      `json:"untested_ttl_hours"`
	AutoFetchEnabled         bool     `json:"auto_fetch_enabled"`
	AutoFetchIntervalMinutes int      `json:"auto_fetch_interval_minutes"`
	AutoFetchSourceIDs       []string `json:"auto_fetch_source_ids"`
	AutoCheckEnabled         bool     `json:"auto_check_enabled"`
	AutoCheckIntervalMinutes int      `json:"auto_check_interval_minutes"`
	TargetLowStockEnabled    bool     `json:"target_low_stock_enabled"`
	TargetLowStockProfiles   []string `json:"target_low_stock_profiles"`
	TargetLowStockMinimum    int      `json:"target_low_stock_minimum"`
	TargetCandidateMinimum   int      `json:"target_candidate_minimum"`
	CheckAfterFetchEnabled   bool     `json:"check_after_fetch_enabled"`
	GatewayTargetProfile     string   `json:"gateway_target_profile"`
	GatewayUpstreamLimit     int      `json:"gateway_upstream_limit"`
	GatewayUpstreamStrategy  string   `json:"gateway_upstream_strategy"`
	GatewayCountries         []string `json:"gateway_countries"`
	GatewayCountryPolicy     string   `json:"gateway_country_policy"`
	GatewayRetryAttempts     int      `json:"gateway_retry_attempts"`
	GatewayFailureThreshold  int      `json:"gateway_failure_threshold"`
	GatewayFailureCooldownS  int      `json:"gateway_failure_cooldown_seconds"`
	GatewayRequestTimeoutS   int      `json:"gateway_request_timeout_seconds"`
	ExportTargetProfile      string   `json:"export_target_profile"`
}

type scheduler struct {
	srv                 *server
	mu                  sync.Mutex
	now                 func() time.Time
	lastUntestedCount   int
	lastFailedDeleted   int64
	lastExpiredRequeued int64
	lastUntestedDeleted int64
	lastMessage         string
	lastError           string
}

type schedulerStatus struct {
	Fetch           taskScheduleStatus `json:"fetch"`
	Check           taskScheduleStatus `json:"check"`
	Maintenance     maintenanceStatus  `json:"maintenance"`
	States          []schedulerState   `json:"states"`
	Pending         []automaticIntent  `json:"pending"`
	BlockingJob     *jobState          `json:"blocking_job,omitempty"`
	LastGrantedType string             `json:"last_granted_type,omitempty"`
	Message         string             `json:"message"`
	Error           string             `json:"error,omitempty"`
}

type schedulerState struct {
	TaskKey             string `json:"task_key"`
	NextDueAt           string `json:"next_due_at,omitempty"`
	LastStartedAt       string `json:"last_started_at,omitempty"`
	LastFinishedAt      string `json:"last_finished_at,omitempty"`
	LastSuccessAt       string `json:"last_success_at,omitempty"`
	LastOutcome         string `json:"last_outcome,omitempty"`
	LastJobID           string `json:"last_job_id,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	BackoffUntil        string `json:"backoff_until,omitempty"`
	PendingReason       string `json:"pending_reason,omitempty"`
	UpdatedAt           string `json:"updated_at"`
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
	UntestedDeleted int64  `json:"untested_deleted"`
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
		CheckTargetProfiles:      []string{"generic"},
		CheckLimit:               500,
		CheckConcurrent:          50,
		CheckRounds:              1,
		CheckRequestTimeout:      6,
		CheckHardTimeout:         60,
		DeleteFailedOnCheck:      false,
		RecheckExpiredEnabled:    false,
		AvailableTTLHours:        24,
		DeleteExpiredUntested:    false,
		UntestedTTLHours:         72,
		AutoFetchEnabled:         false,
		AutoFetchIntervalMinutes: 360,
		AutoFetchSourceIDs:       nil,
		AutoCheckEnabled:         false,
		AutoCheckIntervalMinutes: 120,
		TargetLowStockEnabled:    false,
		TargetLowStockProfiles:   []string{"generic"},
		TargetLowStockMinimum:    50,
		TargetCandidateMinimum:   200,
		CheckAfterFetchEnabled:   true,
		GatewayTargetProfile:     "generic",
		GatewayUpstreamLimit:     200,
		GatewayUpstreamStrategy:  gatewayStrategyRoundRobin,
		GatewayCountries:         nil,
		GatewayCountryPolicy:     gatewayCountryPolicyStrict,
		GatewayRetryAttempts:     gatewayDefaultRetryAttempts,
		GatewayFailureThreshold:  gatewayDefaultFailureThreshold,
		GatewayFailureCooldownS:  gatewayDefaultFailureCooldownS,
		GatewayRequestTimeoutS:   20,
		ExportTargetProfile:      "generic",
	}
}

func (s *store) EnsureSettingsSchema() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS app_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT (datetime('now', '+8 hours'))
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
VALUES (?, ?, datetime('now', '+8 hours'))
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = datetime('now', '+8 hours')`,
		appSettingsKey, string(raw))
	return settings, err
}

func (s *store) SchedulerState(taskKey string) (schedulerState, bool, error) {
	var state schedulerState
	var nextDue, lastStarted, lastFinished, lastSuccess, backoff sql.NullString
	var lastJob sql.NullInt64
	err := s.db.QueryRow(`
SELECT task_key, next_due_at, last_started_at, last_finished_at, last_success_at,
       last_outcome, last_job_id, consecutive_failures, backoff_until,
       pending_reason, updated_at
FROM scheduler_state WHERE task_key = ?`, taskKey).Scan(
		&state.TaskKey, &nextDue, &lastStarted, &lastFinished, &lastSuccess,
		&state.LastOutcome, &lastJob, &state.ConsecutiveFailures, &backoff,
		&state.PendingReason, &state.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return state, false, nil
	}
	if err != nil {
		return state, false, err
	}
	state.NextDueAt = nullStringValue(nextDue)
	state.LastStartedAt = nullStringValue(lastStarted)
	state.LastFinishedAt = nullStringValue(lastFinished)
	state.LastSuccessAt = nullStringValue(lastSuccess)
	state.BackoffUntil = nullStringValue(backoff)
	if lastJob.Valid {
		state.LastJobID = strconv.FormatInt(lastJob.Int64, 10)
	}
	return state, true, nil
}

func (s *store) SchedulerStates() ([]schedulerState, error) {
	rows, err := s.db.Query(`
SELECT task_key, next_due_at, last_started_at, last_finished_at, last_success_at,
       last_outcome, last_job_id, consecutive_failures, backoff_until,
       pending_reason, updated_at
FROM scheduler_state ORDER BY task_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []schedulerState{}
	for rows.Next() {
		var state schedulerState
		var nextDue, lastStarted, lastFinished, lastSuccess, backoff sql.NullString
		var lastJob sql.NullInt64
		if err := rows.Scan(&state.TaskKey, &nextDue, &lastStarted, &lastFinished, &lastSuccess, &state.LastOutcome, &lastJob, &state.ConsecutiveFailures, &backoff, &state.PendingReason, &state.UpdatedAt); err != nil {
			return nil, err
		}
		state.NextDueAt = nullStringValue(nextDue)
		state.LastStartedAt = nullStringValue(lastStarted)
		state.LastFinishedAt = nullStringValue(lastFinished)
		state.LastSuccessAt = nullStringValue(lastSuccess)
		state.BackoffUntil = nullStringValue(backoff)
		if lastJob.Valid {
			state.LastJobID = strconv.FormatInt(lastJob.Int64, 10)
		}
		out = append(out, state)
	}
	return out, rows.Err()
}

func (s *store) SaveSchedulerState(state schedulerState) error {
	var lastJob any
	if value, err := strconv.ParseInt(state.LastJobID, 10, 64); err == nil && value > 0 {
		lastJob = value
	}
	_, err := s.db.Exec(`
INSERT INTO scheduler_state (
  task_key, next_due_at, last_started_at, last_finished_at, last_success_at,
  last_outcome, last_job_id, consecutive_failures, backoff_until,
  pending_reason, updated_at
)
VALUES (?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''), ?, ?)
ON CONFLICT(task_key) DO UPDATE SET
  next_due_at = excluded.next_due_at,
  last_started_at = excluded.last_started_at,
  last_finished_at = excluded.last_finished_at,
  last_success_at = excluded.last_success_at,
  last_outcome = excluded.last_outcome,
  last_job_id = excluded.last_job_id,
  consecutive_failures = excluded.consecutive_failures,
  backoff_until = excluded.backoff_until,
  pending_reason = excluded.pending_reason,
  updated_at = excluded.updated_at`,
		state.TaskKey, state.NextDueAt, state.LastStartedAt, state.LastFinishedAt, state.LastSuccessAt,
		state.LastOutcome, lastJob, state.ConsecutiveFailures, state.BackoffUntil, state.PendingReason,
		firstNonEmpty(state.UpdatedAt, nowString()))
	return err
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
	if value, ok := payload["check_target_profiles"]; ok {
		settings.CheckTargetProfiles = anyToStringSlice(value)
		settings.CheckTargetProfile = ""
	}
	if value, ok := payload["check_target_profile"]; ok {
		settings.CheckTargetProfile = optionalString(value, settings.CheckTargetProfile)
		if _, hasProfiles := payload["check_target_profiles"]; !hasProfiles {
			settings.CheckTargetProfiles = anyToStringSlice(settings.CheckTargetProfile)
		}
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
	if value, ok := payload["delete_expired_untested"]; ok {
		settings.DeleteExpiredUntested = parseBool(value, settings.DeleteExpiredUntested)
	}
	if value, ok := payload["untested_ttl_hours"]; ok {
		settings.UntestedTTLHours = anyToInt(value)
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
	if value, ok := payload["target_low_stock_enabled"]; ok {
		settings.TargetLowStockEnabled = parseBool(value, settings.TargetLowStockEnabled)
	}
	if value, ok := payload["target_low_stock_profiles"]; ok {
		settings.TargetLowStockProfiles = anyToStringSlice(value)
	}
	if value, ok := payload["target_low_stock_minimum"]; ok {
		settings.TargetLowStockMinimum = anyToInt(value)
	}
	if value, ok := payload["target_candidate_minimum"]; ok {
		settings.TargetCandidateMinimum = anyToInt(value)
	}
	if value, ok := payload["check_after_fetch_enabled"]; ok {
		settings.CheckAfterFetchEnabled = parseBool(value, settings.CheckAfterFetchEnabled)
	}
	if value, ok := payload["gateway_target_profile"]; ok {
		settings.GatewayTargetProfile = optionalString(value, settings.GatewayTargetProfile)
	}
	if value, ok := payload["gateway_upstream_limit"]; ok {
		settings.GatewayUpstreamLimit = anyToInt(value)
	}
	if value, ok := payload["gateway_upstream_strategy"]; ok {
		settings.GatewayUpstreamStrategy = optionalString(value, settings.GatewayUpstreamStrategy)
	}
	if value, ok := payload["gateway_countries"]; ok {
		settings.GatewayCountries = anyToStringSlice(value)
	}
	if value, ok := payload["gateway_country_policy"]; ok {
		settings.GatewayCountryPolicy = optionalString(value, settings.GatewayCountryPolicy)
	}
	if value, ok := payload["gateway_retry_attempts"]; ok {
		settings.GatewayRetryAttempts = anyToInt(value)
	}
	if value, ok := payload["gateway_failure_threshold"]; ok {
		settings.GatewayFailureThreshold = anyToInt(value)
	}
	if value, ok := payload["gateway_failure_cooldown_seconds"]; ok {
		settings.GatewayFailureCooldownS = anyToInt(value)
	}
	if value, ok := payload["gateway_request_timeout_seconds"]; ok {
		settings.GatewayRequestTimeoutS = anyToInt(value)
	}
	if value, ok := payload["export_target_profile"]; ok {
		settings.ExportTargetProfile = optionalString(value, settings.ExportTargetProfile)
	}
	return normalizeAppSettings(settings)
}

func normalizeAppSettings(settings appSettings) appSettings {
	settings.ProxyPageSize = clampInt(settings.ProxyPageSize, 20, 500)
	settings.FetchLimitPerSource = clampInt(settings.FetchLimitPerSource, 0, 50000)
	settings.AutoFetchUntestedMinimum = clampInt(settings.AutoFetchUntestedMinimum, 100, 1000000)
	settings.AutoFetchCooldownMinutes = clampInt(settings.AutoFetchCooldownMinutes, 1, 1440)
	settings.CheckStatus = normalizeCheckStatus(settings.CheckStatus)
	checkProfiles := settings.CheckTargetProfiles
	if len(checkProfiles) == 0 {
		checkProfiles = anyToStringSlice(settings.CheckTargetProfile)
	}
	settings.CheckTargetProfiles = normalizeTargetProfiles(checkProfiles)
	settings.CheckTargetProfile = settings.CheckTargetProfiles[0]
	settings.CheckLimit = clampInt(settings.CheckLimit, 1, 100000)
	settings.CheckConcurrent = clampInt(settings.CheckConcurrent, 1, 300)
	settings.CheckRounds = clampInt(settings.CheckRounds, 1, 5)
	settings.CheckRequestTimeout = clampInt(settings.CheckRequestTimeout, 2, 60)
	settings.CheckHardTimeout = clampInt(settings.CheckHardTimeout, settings.CheckRequestTimeout, 300)
	settings.AvailableTTLHours = clampInt(settings.AvailableTTLHours, 1, 8760)
	settings.UntestedTTLHours = clampInt(settings.UntestedTTLHours, 1, 8760)
	settings.AutoFetchIntervalMinutes = clampInt(settings.AutoFetchIntervalMinutes, 5, 10080)
	settings.AutoCheckIntervalMinutes = clampInt(settings.AutoCheckIntervalMinutes, 5, 10080)
	settings.TargetLowStockProfiles = normalizeTargetProfiles(settings.TargetLowStockProfiles)
	settings.TargetLowStockMinimum = clampInt(settings.TargetLowStockMinimum, 1, 1000000)
	settings.TargetCandidateMinimum = clampInt(settings.TargetCandidateMinimum, 1, 1000000)
	settings.AutoFetchSourceIDs = validSourceIDs(settings.AutoFetchSourceIDs)
	settings.GatewayTargetProfile = normalizeTargetProfileOrAll(settings.GatewayTargetProfile, "generic")
	settings.GatewayUpstreamLimit = clampInt(settings.GatewayUpstreamLimit, 1, 2000)
	settings.GatewayUpstreamStrategy = normalizeGatewayUpstreamStrategy(settings.GatewayUpstreamStrategy)
	settings.GatewayCountries = normalizeCountryCodes(settings.GatewayCountries)
	settings.GatewayCountryPolicy = normalizeGatewayCountryPolicy(settings.GatewayCountryPolicy)
	settings.GatewayRetryAttempts = clampInt(settings.GatewayRetryAttempts, 1, 5)
	settings.GatewayFailureThreshold = clampInt(settings.GatewayFailureThreshold, 1, 100)
	settings.GatewayFailureCooldownS = clampInt(settings.GatewayFailureCooldownS, 1, 86400)
	settings.GatewayRequestTimeoutS = clampInt(settings.GatewayRequestTimeoutS, 2, 120)
	settings.ExportTargetProfile = normalizeTargetProfileOrAll(settings.ExportTargetProfile, "generic")
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
	return &scheduler{srv: srv, now: beijingNow}
}

func (sc *scheduler) Start() {
	sc.recoverInterruptedSchedules()
	go sc.run()
}

func (sc *scheduler) recoverInterruptedSchedules() {
	items, err := sc.srv.jobs.History(jobHistoryFilter{Limit: 200, Status: jobStatusInterrupted})
	if err != nil {
		return
	}
	now := sc.now()
	for _, job := range items {
		if job.TaskKey == "" {
			continue
		}
		state, _, _ := sc.srv.store.SchedulerState(job.TaskKey)
		if state.LastJobID != "" && state.LastJobID != job.ID {
			continue
		}
		if state.NextDueAt != "" {
			continue
		}
		state.TaskKey = job.TaskKey
		state.LastJobID = job.ID
		state.LastOutcome = jobStatusInterrupted
		state.LastFinishedAt = job.FinishedAt
		state.NextDueAt = formatTime(now)
		state.PendingReason = "recovery"
		state.UpdatedAt = formatTime(now)
		_ = sc.srv.store.SaveSchedulerState(state)
	}
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
	now := sc.now()
	sc.ensureSchedules(settings, now)
	sc.enqueueDue(settings, now)
	sc.dispatchNext()
	sc.tickMaintenance(settings, now)
}

func (sc *scheduler) ensureSchedules(settings appSettings, now time.Time) {
	defaults := map[string]time.Time{
		"maintenance.lifecycle": now,
	}
	if settings.AutoFetchEnabled {
		defaults["fetch.periodic"] = now.Add(time.Duration(settings.AutoFetchIntervalMinutes) * time.Minute)
	}
	if settings.AutoFetchLowStockEnabled {
		defaults["fetch.low_stock"] = now
	}
	if settings.AutoCheckEnabled {
		defaults["check.periodic"] = now.Add(time.Duration(settings.AutoCheckIntervalMinutes) * time.Minute)
	}
	if settings.TargetLowStockEnabled {
		for _, profile := range settings.TargetLowStockProfiles {
			defaults["check.low_stock."+profile] = now
		}
	}
	for key, next := range defaults {
		state, found, err := sc.srv.store.SchedulerState(key)
		if err != nil {
			sc.setError(err)
			return
		}
		if !found {
			state = schedulerState{TaskKey: key, NextDueAt: formatTime(next), UpdatedAt: formatTime(now)}
			_ = sc.srv.store.SaveSchedulerState(state)
		}
	}
}

func (sc *scheduler) enqueueDue(settings appSettings, now time.Time) {
	states, err := sc.srv.store.SchedulerStates()
	if err != nil {
		sc.setError(err)
		return
	}
	for _, state := range states {
		if strings.HasPrefix(state.TaskKey, "maintenance.") {
			continue
		}
		if !schedulerStateDue(state, now) {
			continue
		}
		intent, ok := sc.intentForState(state, settings)
		if !ok {
			continue
		}
		sc.srv.coordinator.Defer(intent)
		state.PendingReason = mergeReason(state.PendingReason, intent.Reason)
		state.UpdatedAt = formatTime(now)
		_ = sc.srv.store.SaveSchedulerState(state)
	}
	if settings.TargetLowStockEnabled {
		for _, profile := range settings.TargetLowStockProfiles {
			lowStockKey := "check.low_stock." + profile
			lowStockState, found, stateErr := sc.srv.store.SchedulerState(lowStockKey)
			if stateErr != nil || !found || !schedulerStateDue(lowStockState, now) {
				continue
			}
			available, err := sc.srv.store.CountAvailableProxyURLs(profile)
			if err != nil || available >= settings.TargetLowStockMinimum {
				continue
			}
			candidates, err := sc.srv.store.CountTargetProxiesByStatus("untested", profile)
			if err != nil {
				continue
			}
			if candidates >= settings.TargetCandidateMinimum {
				sc.srv.coordinator.Defer(sc.checkIntent(lowStockKey, "low_stock", []string{profile}, ""))
				sc.markPending(lowStockKey, "low_stock", now)
			} else {
				payload := sc.fetchPayload("fetch.low_stock", "low_stock")
				payload["pipeline_targets"] = []string{profile}
				sc.srv.coordinator.Defer(automaticIntent{Key: "fetch.low_stock", JobType: "fetch", Reason: "low_stock", TaskKey: "fetch.low_stock", Payload: payload})
				sc.markPending("fetch.low_stock", "low_stock", now)
				lowStockState.NextDueAt = formatTime(now.Add(time.Duration(settings.AutoFetchCooldownMinutes) * time.Minute))
				lowStockState.PendingReason = "waiting_for_candidates"
				lowStockState.UpdatedAt = formatTime(now)
				_ = sc.srv.store.SaveSchedulerState(lowStockState)
			}
		}
	}
}

func (sc *scheduler) markPending(taskKey string, reason string, now time.Time) {
	state, _, _ := sc.srv.store.SchedulerState(taskKey)
	state.TaskKey = taskKey
	state.PendingReason = mergeReason(state.PendingReason, reason)
	state.UpdatedAt = formatTime(now)
	_ = sc.srv.store.SaveSchedulerState(state)
}

func (sc *scheduler) intentForState(state schedulerState, settings appSettings) (automaticIntent, bool) {
	switch state.TaskKey {
	case "fetch.periodic":
		if !settings.AutoFetchEnabled {
			return automaticIntent{}, false
		}
		return automaticIntent{Key: state.TaskKey, JobType: "fetch", Reason: "periodic", TaskKey: state.TaskKey, Payload: sc.fetchPayload(state.TaskKey, "periodic")}, true
	case "fetch.low_stock":
		if !settings.AutoFetchLowStockEnabled {
			return automaticIntent{}, false
		}
		count, err := sc.srv.store.CountTargetProxiesByStatus("untested", settings.CheckTargetProfile)
		if err != nil || count >= settings.AutoFetchUntestedMinimum {
			return automaticIntent{}, false
		}
		sc.mu.Lock()
		sc.lastUntestedCount = count
		sc.mu.Unlock()
		return automaticIntent{Key: state.TaskKey, JobType: "fetch", Reason: "low_stock", TaskKey: state.TaskKey, Payload: sc.fetchPayload(state.TaskKey, "low_stock")}, true
	case "check.periodic":
		if !settings.AutoCheckEnabled {
			return automaticIntent{}, false
		}
		return sc.checkIntent(state.TaskKey, "periodic", settings.CheckTargetProfiles, ""), true
	default:
		if strings.HasPrefix(state.TaskKey, "check.low_stock.") {
			return automaticIntent{}, false
		}
	}
	return automaticIntent{}, false
}

func (sc *scheduler) fetchPayload(taskKey string, reason string) map[string]any {
	settings, _ := sc.srv.store.AppSettings()
	return map[string]any{"source_ids": settings.AutoFetchSourceIDs, "limit_per_source": settings.FetchLimitPerSource, "scheduled_trigger": "auto", "trigger": reason, "trigger_reason": reason, "task_key": taskKey}
}

func (sc *scheduler) checkIntent(taskKey string, reason string, profiles []string, parent string) automaticIntent {
	settings, _ := sc.srv.store.AppSettings()
	payload := map[string]any{
		"status": settings.CheckStatus, "target_profiles": profiles, "limit": settings.CheckLimit,
		"concurrent": settings.CheckConcurrent, "rounds": settings.CheckRounds,
		"request_timeout": settings.CheckRequestTimeout, "hard_timeout": settings.CheckHardTimeout,
		"scheduled_trigger": "auto", "trigger": reason, "trigger_reason": reason,
		"task_key": taskKey, "parent_job_id": parent,
	}
	return automaticIntent{Key: taskKey, JobType: "check", Reason: reason, TaskKey: taskKey, ParentJobID: parent, Payload: payload}
}

func (sc *scheduler) dispatchNext() {
	intent, ok := sc.srv.coordinator.PopNext()
	if !ok {
		return
	}
	var err error
	if intent.JobType == "fetch" {
		_, err = sc.srv.StartFetchSourcesJob(intent.Payload)
	} else {
		_, err = sc.srv.StartCheckJob(intent.Payload)
	}
	if err != nil {
		if err == errJobDeferred {
			sc.srv.coordinator.Defer(intent)
			return
		}
		sc.setError(err)
	}
}

func (sc *scheduler) MarkStarted(job *jobState) {
	if job == nil || job.TaskKey == "" {
		return
	}
	state, _, _ := sc.srv.store.SchedulerState(job.TaskKey)
	state.TaskKey = job.TaskKey
	state.LastStartedAt = job.StartedAt
	state.LastJobID = job.ID
	state.NextDueAt = ""
	state.PendingReason = ""
	state.UpdatedAt = job.UpdatedAt
	_ = sc.srv.store.SaveSchedulerState(state)
}

func (sc *scheduler) OnJobTerminal(job *jobState) {
	if job == nil {
		return
	}
	settings, _ := sc.srv.store.AppSettings()
	now := sc.now()
	if job.TaskKey != "" {
		state, _, _ := sc.srv.store.SchedulerState(job.TaskKey)
		state.TaskKey = job.TaskKey
		state.LastFinishedAt = job.FinishedAt
		state.LastOutcome = job.Status
		state.LastJobID = job.ID
		state.PendingReason = ""
		switch job.Status {
		case jobStatusCompleted:
			state.ConsecutiveFailures = 0
			state.BackoffUntil = ""
			state.LastSuccessAt = job.FinishedAt
			state.NextDueAt = formatTime(now.Add(sc.normalInterval(job.TaskKey, settings)))
		case jobStatusPartial:
			state.NextDueAt = formatTime(now.Add(2 * time.Minute))
		case jobStatusFailed:
			state.ConsecutiveFailures++
			backoff := schedulerBackoff(state.ConsecutiveFailures)
			state.BackoffUntil = formatTime(now.Add(backoff))
			state.NextDueAt = state.BackoffUntil
		case jobStatusCancelled, jobStatusInterrupted:
			state.NextDueAt = formatTime(now.Add(time.Minute))
		}
		state.UpdatedAt = formatTime(now)
		_ = sc.srv.store.SaveSchedulerState(state)
	}
	if job.Type == "fetch" && settings.CheckAfterFetchEnabled && anyToInt(job.Result["added"]) > 0 {
		profiles := settings.CheckTargetProfiles
		if raw, ok := job.Params["pipeline_targets"]; ok {
			profiles = normalizeTargetProfiles(anyToStringSlice(raw))
		}
		intent := sc.checkIntent("check.pipeline."+strings.Join(profiles, "_"), "pipeline", profiles, job.ID)
		sc.srv.coordinator.Defer(intent)
	}
	sc.dispatchNext()
}

func (sc *scheduler) normalInterval(taskKey string, settings appSettings) time.Duration {
	switch {
	case taskKey == "fetch.periodic":
		return time.Duration(settings.AutoFetchIntervalMinutes) * time.Minute
	case taskKey == "check.periodic":
		return time.Duration(settings.AutoCheckIntervalMinutes) * time.Minute
	case strings.Contains(taskKey, "low_stock"):
		return time.Duration(settings.AutoFetchCooldownMinutes) * time.Minute
	default:
		return 5 * time.Minute
	}
}

func schedulerBackoff(failures int) time.Duration {
	sequence := []time.Duration{time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 30 * time.Minute}
	if failures <= 0 {
		return sequence[0]
	}
	if failures > len(sequence) {
		return sequence[len(sequence)-1]
	}
	return sequence[failures-1]
}

func schedulerStateDue(state schedulerState, now time.Time) bool {
	next := parseScheduleTime(state.NextDueAt)
	backoff := parseScheduleTime(state.BackoffUntil)
	if !backoff.IsZero() && now.Before(backoff) {
		return false
	}
	return !next.IsZero() && !now.Before(next)
}

func parseScheduleTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, strings.TrimSpace(value))
	return parsed
}

func (sc *scheduler) ApplySettings(previous appSettings, settings appSettings) {
	now := sc.now()
	if previous.AutoFetchEnabled != settings.AutoFetchEnabled || previous.AutoFetchIntervalMinutes != settings.AutoFetchIntervalMinutes {
		sc.resetSchedule("fetch.periodic", settings.AutoFetchEnabled, now.Add(time.Duration(settings.AutoFetchIntervalMinutes)*time.Minute))
	}
	if previous.AutoFetchLowStockEnabled != settings.AutoFetchLowStockEnabled || previous.AutoFetchUntestedMinimum != settings.AutoFetchUntestedMinimum || previous.AutoFetchCooldownMinutes != settings.AutoFetchCooldownMinutes {
		sc.resetSchedule("fetch.low_stock", settings.AutoFetchLowStockEnabled, now)
	}
	if previous.AutoCheckEnabled != settings.AutoCheckEnabled || previous.AutoCheckIntervalMinutes != settings.AutoCheckIntervalMinutes {
		sc.resetSchedule("check.periodic", settings.AutoCheckEnabled, now.Add(time.Duration(settings.AutoCheckIntervalMinutes)*time.Minute))
	}
	if previous.TargetLowStockEnabled != settings.TargetLowStockEnabled || previous.TargetLowStockMinimum != settings.TargetLowStockMinimum || previous.TargetCandidateMinimum != settings.TargetCandidateMinimum || strings.Join(previous.TargetLowStockProfiles, ",") != strings.Join(settings.TargetLowStockProfiles, ",") {
		currentProfiles := map[string]bool{}
		for _, profile := range settings.TargetLowStockProfiles {
			currentProfiles[profile] = true
			sc.resetSchedule("check.low_stock."+profile, settings.TargetLowStockEnabled, now)
		}
		for _, profile := range previous.TargetLowStockProfiles {
			if !currentProfiles[profile] {
				sc.resetSchedule("check.low_stock."+profile, false, time.Time{})
			}
		}
	}
	sc.mu.Lock()
	sc.lastError = ""
	sc.lastMessage = "设置已保存"
	sc.mu.Unlock()
}

func (sc *scheduler) resetSchedule(key string, enabled bool, next time.Time) {
	state, _, _ := sc.srv.store.SchedulerState(key)
	state.TaskKey = key
	if enabled {
		state.NextDueAt = formatTime(next)
	} else {
		state.NextDueAt = ""
	}
	state.BackoffUntil = ""
	state.PendingReason = ""
	state.UpdatedAt = formatTime(sc.now())
	_ = sc.srv.store.SaveSchedulerState(state)
}

func (sc *scheduler) Status(settings appSettings) schedulerStatus {
	states, _ := sc.srv.store.SchedulerStates()
	stateMap := map[string]schedulerState{}
	for _, state := range states {
		stateMap[state.TaskKey] = state
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return schedulerStatus{
		Fetch:       taskScheduleStatus{Enabled: settings.AutoFetchEnabled, IntervalMinutes: settings.AutoFetchIntervalMinutes, LastRunAt: stateMap["fetch.periodic"].LastStartedAt, NextRunAt: stateMap["fetch.periodic"].NextDueAt, LowStockEnabled: settings.AutoFetchLowStockEnabled, UntestedMinimum: settings.AutoFetchUntestedMinimum, CooldownMinutes: settings.AutoFetchCooldownMinutes, NextLowStockCheckAt: stateMap["fetch.low_stock"].NextDueAt, LastUntestedCount: sc.lastUntestedCount},
		Check:       taskScheduleStatus{Enabled: settings.AutoCheckEnabled, IntervalMinutes: settings.AutoCheckIntervalMinutes, LastRunAt: stateMap["check.periodic"].LastStartedAt, NextRunAt: stateMap["check.periodic"].NextDueAt},
		Maintenance: maintenanceStatus{LastRunAt: stateMap["maintenance.lifecycle"].LastStartedAt, FailedDeleted: sc.lastFailedDeleted, ExpiredRequeued: sc.lastExpiredRequeued, UntestedDeleted: sc.lastUntestedDeleted},
		States:      states, Pending: sc.srv.coordinator.Pending(), BlockingJob: sc.srv.coordinator.Active(), LastGrantedType: sc.srv.coordinator.LastGrantedType(), Message: sc.lastMessage, Error: sc.lastError,
	}
}

func (sc *scheduler) tickMaintenance(settings appSettings, now time.Time) {
	state, found, err := sc.srv.store.SchedulerState("maintenance.lifecycle")
	if err != nil || !found || !schedulerStateDue(state, now) {
		return
	}
	if sc.srv.coordinator.Active() != nil || len(sc.srv.coordinator.Pending()) > 0 {
		state.PendingReason = "heavy_job_busy"
		state.UpdatedAt = formatTime(now)
		_ = sc.srv.store.SaveSchedulerState(state)
		return
	}
	job, _ := sc.srv.jobs.CreateWithSpec(jobSpec{Type: "maintenance", Trigger: "periodic", TriggerReason: "lifecycle", TaskKey: "maintenance.lifecycle", Message: "执行代理生命周期维护"})
	if job == nil {
		return
	}
	sc.MarkStarted(job)
	deleted, requeued, untestedDeleted, err := sc.srv.applyProxyMaintenance(settings)
	if err != nil {
		sc.srv.jobs.fail(job.ID, err)
		sc.setError(err)
		return
	}
	sc.mu.Lock()
	sc.lastFailedDeleted, sc.lastExpiredRequeued, sc.lastUntestedDeleted = deleted, requeued, untestedDeleted
	sc.lastMessage = fmt.Sprintf("自动维护：删除失败 %d，过期转待检 %d，删除待检 %d", deleted, requeued, untestedDeleted)
	sc.lastError = ""
	sc.mu.Unlock()
	sc.srv.jobs.complete(job.ID, "代理生命周期维护完成", map[string]any{"failed_deleted": deleted, "expired_requeued": requeued, "untested_deleted": untestedDeleted})
}

func (s *server) applyProxyMaintenance(settings appSettings) (int64, int64, int64, error) {
	var deleted int64
	var requeued int64
	var untestedDeleted int64
	var err error
	if settings.DeleteFailedOnCheck {
		deleted, err = s.store.DeleteFailedProxies()
		if err != nil {
			return 0, 0, 0, err
		}
		_ = s.store.RecordMaintenanceEvent("delete_failed", deleted, "", "delete_failed_on_check", settings)
	}
	if settings.RecheckExpiredEnabled {
		probeRequeued, targetRequeued, countErr := s.store.CountExpiredAvailableStates(settings.AvailableTTLHours)
		if countErr != nil {
			return deleted, 0, 0, countErr
		}
		requeued, err = s.store.RequeueExpiredAvailable(settings.AvailableTTLHours)
		if err != nil {
			return deleted, 0, 0, err
		}
		_ = s.store.RecordMaintenanceEvent("requeue_expired_probe", probeRequeued, "", "available_ttl_hours", settings)
		_ = s.store.RecordMaintenanceEvent("requeue_expired_target", targetRequeued, "all", "available_ttl_hours", settings)
	}
	if settings.DeleteExpiredUntested {
		untestedDeleted, err = s.store.DeleteExpiredUntested(settings.UntestedTTLHours)
		if err != nil {
			return deleted, requeued, 0, err
		}
		_ = s.store.RecordMaintenanceEvent("delete_expired_untested", untestedDeleted, "", "untested_ttl_hours", settings)
	}
	return deleted, requeued, untestedDeleted, nil
}

func (sc *scheduler) setError(err error) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.lastError = err.Error()
	sc.lastMessage = "自动任务状态读取失败"
}

func formatTime(value time.Time) string {
	return formatBeijingTime(value)
}
