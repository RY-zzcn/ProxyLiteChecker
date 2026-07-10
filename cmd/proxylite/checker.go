package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RY-zzcn/ProxyLiteChecker/internal/checkmeta"
)

type CheckConfig struct {
	TargetProfile  string
	Limit          int
	Concurrent     int
	Rounds         int
	RequestTimeout int
	HardTimeout    int
	DeleteFailed   bool
}

type ProxyTask struct {
	ID       int     `json:"id"`
	Proxy    string  `json:"proxy"`
	IP       string  `json:"ip"`
	Port     int     `json:"port"`
	Protocol string  `json:"protocol"`
	Username *string `json:"username,omitempty"`
	Password *string `json:"-"`
	Source   string  `json:"source"`
}

type CheckResult struct {
	ProxyID          int     `json:"proxy_id"`
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
	BaseReachable    bool    `json:"base_reachable"`
}

type TargetProfile struct {
	ServiceURL       string            `json:"service_url"`
	APIURL           string            `json:"api_url"`
	ExpectedStatuses []int             `json:"expected_statuses,omitempty"`
	Keyword          string            `json:"keyword,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	TimeoutSeconds   int               `json:"timeout_seconds,omitempty"`
}

type singleCheckResult struct {
	CheckResult
	BaseReachable bool
}

type checkPlan struct {
	Item           ProxyTask
	TargetProfiles []string
}

type checkBundle struct {
	ProxyID int
	Results []CheckResult
}

type targetProbeResult struct {
	ServiceReachable bool
	APIReachable     *bool
	LatencyMS        *int
	CloudflareStatus *string
	Successes        int
	Total            int
}

type checkGeoCache struct {
	mu     sync.Mutex
	values map[string]*checkGeoCacheEntry
	lookup func(context.Context, string) checkmeta.Metadata
}

type checkGeoCacheEntry struct {
	metadata checkmeta.Metadata
	ready    chan struct{}
}

type proxyCheckOutcome struct {
	Expected      int
	Seen          int
	AllFailed     bool
	BaseReachable bool
	Persisted     bool
}

var targetProfileOrder = []string{"generic", "openai", "grok", "gemini", "claude"}

var targetProfiles = map[string]TargetProfile{
	"generic": {ServiceURL: "https://example.com/"},
	"openai":  {ServiceURL: "https://chat.openai.com/", APIURL: "https://api.openai.com/v1/models"},
	"grok":    {ServiceURL: "https://grok.com/", APIURL: "https://api.x.ai/v1/models"},
	"gemini":  {ServiceURL: "https://gemini.google.com/", APIURL: "https://generativelanguage.googleapis.com/v1beta/models"},
	"claude":  {ServiceURL: "https://claude.ai/", APIURL: "https://api.anthropic.com/v1/models"},
}

var checkProxyHTTPClient = proxyHTTPClient
var checkExitIPTargets = exitIPTargets
var checkEnrichIP = checkmeta.EnrichIP

func (s *server) StartCheckJob(payload map[string]any) (map[string]any, error) {
	scheduled := strings.EqualFold(optionalString(payload["scheduled_trigger"], ""), "auto")
	trigger := optionalString(payload["trigger"], "manual")
	if scheduled && trigger == "manual" {
		trigger = "periodic"
	}
	reason := optionalString(payload["trigger_reason"], trigger)
	taskKey := optionalString(payload["task_key"], "")
	intent := automaticIntent{Key: firstNonEmpty(taskKey, "check."+trigger), JobType: "check", Reason: reason, TaskKey: taskKey, ParentJobID: optionalString(payload["parent_job_id"], ""), Payload: cloneMap(payload)}
	if s.coordinator != nil {
		acquired, running := s.coordinator.TryAcquire("check", scheduled, intent)
		if !acquired {
			if scheduled {
				return nil, errJobDeferred
			}
			if running == nil {
				running = s.jobs.RunningOfTypes("fetch", "check")
			}
			if running == nil {
				return nil, jobConflict("heavy")
			}
			return nil, runningJobConflict(running)
		}
	}
	settings, err := s.store.AppSettings()
	if err != nil {
		if s.coordinator != nil {
			s.coordinator.AbortReservation()
		}
		return nil, err
	}
	if _, _, _, err := s.applyProxyMaintenance(settings); err != nil {
		if s.coordinator != nil {
			s.coordinator.AbortReservation()
		}
		return nil, err
	}
	fallbackProfiles := settings.CheckTargetProfiles
	if len(fallbackProfiles) == 0 {
		fallbackProfiles = []string{settings.CheckTargetProfile}
	}
	targetProfiles := targetProfilesFromPayload(payload, fallbackProfiles)
	cfg := CheckConfig{
		Limit:          optionalLimit(payload["limit"], 200, 100000),
		Concurrent:     optionalLimit(payload["concurrent"], settings.CheckConcurrent, maxCheckConcurrency),
		Rounds:         optionalLimit(payload["rounds"], 1, 5),
		RequestTimeout: optionalLimit(payload["request_timeout"], 6, 60),
		HardTimeout:    optionalLimit(payload["hard_timeout"], 60, 300),
		DeleteFailed:   settings.DeleteFailedOnCheck,
	}
	if s.checkConcurrency != nil {
		cfg.Concurrent = s.checkConcurrency.SetLimit(cfg.Concurrent)
	}
	status := optionalString(payload["status"], "untested")
	job, ctx := s.jobs.CreateWithSpec(jobSpec{
		Type: "check", Trigger: trigger, TriggerReason: reason, TaskKey: taskKey,
		ParentJobID: optionalString(payload["parent_job_id"], ""), Message: "准备本机检测",
		Params: map[string]any{"status": status, "target_profiles": targetProfiles, "limit": cfg.Limit, "concurrent": cfg.Concurrent, "rounds": cfg.Rounds, "request_timeout": cfg.RequestTimeout, "hard_timeout": cfg.HardTimeout},
	})
	if job == nil {
		if s.coordinator != nil {
			s.coordinator.AbortReservation()
		}
		return nil, fmt.Errorf("创建检测任务失败")
	}
	if s.runtime != nil {
		s.runtime.Add("info", fmt.Sprintf("检测任务 #%s 已启动：目标 %s，批量 %d，并发 %d", job.ID, targetProfileLabels(targetProfiles), cfg.Limit, cfg.Concurrent))
	}
	if s.coordinator != nil {
		s.coordinator.Bind(job)
	}
	if s.scheduler != nil {
		s.scheduler.MarkStarted(job)
	}
	candidates := make(map[string][]ProxyTask, len(targetProfiles))
	for _, profile := range targetProfiles {
		items, err := s.store.ListCheckCandidates(status, cfg.Limit, profile)
		if err != nil {
			s.jobs.fail(job.ID, err)
			return nil, err
		}
		candidates[profile] = items
	}
	plans := buildProxyFirstCheckPlans(targetProfiles, candidates)
	total := totalCheckPlanItems(plans)
	profiles := checkPlanProfiles(plans)
	s.jobs.Update(job.ID, map[string]any{
		"total":   total,
		"message": fmt.Sprintf("本机检测排队：%d 个代理 / %d 项目标检测，目标 %s，并发 %d", len(plans), total, targetProfileLabels(profiles), cfg.Concurrent),
		"result":  initialCheckProgress(plans, profiles, cfg.Concurrent),
	})
	go s.runCheckJob(ctx, job.ID, plans, cfg)
	return map[string]any{"job_id": job.ID, "count": total, "proxy_count": len(plans), "target_profiles": targetProfiles}, nil
}

func (s *server) runCheckJob(ctx context.Context, jobID string, plans []checkPlan, cfg CheckConfig) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.jobs.fail(jobID, fmt.Errorf("检测任务异常退出：%v", recovered))
		}
	}()
	total := totalCheckPlanItems(plans)
	if total == 0 {
		s.jobs.complete(jobID, "没有符合条件的代理需要检测", map[string]any{"checked": 0, "noop": true})
		return
	}
	done := 0
	proxyDone := 0
	available := 0
	failed := 0
	checkFailed := 0
	persisted := 0
	persistenceFailed := 0
	deletedFailed := 0
	outcomes := buildProxyCheckOutcomes(plans)
	profiles := checkPlanProfiles(plans)
	targetProgress := newTargetCheckProgress(plans, profiles)
	geoCache := newCheckGeoCache()
	if s.geoEnricher != nil {
		geoCache = newCheckGeoCacheWithLookup(s.geoEnricher.LookupAndQueue)
	}
	for bundle := range checkBatchStream(ctx, plans, cfg, geoCache, s.checkConcurrency) {
		if len(bundle.Results) == 0 {
			continue
		}
		outcome := outcomes[bundle.ProxyID]
		if outcome == nil {
			outcome = &proxyCheckOutcome{Expected: len(bundle.Results), AllFailed: true, Persisted: true}
			outcomes[bundle.ProxyID] = outcome
		}
		for _, result := range bundle.Results {
			updateTargetCheckProgress(targetProgress, result)
			outcome.Seen++
			outcome.BaseReachable = outcome.BaseReachable || result.BaseReachable
			if result.Status == "available" {
				outcome.AllFailed = false
			}
		}
		proxyDone++
		done += len(bundle.Results)
		persistenceCtx := ctx
		cancelPersistence := func() {}
		if ctx.Err() != nil {
			persistenceCtx, cancelPersistence = context.WithTimeout(context.Background(), 5*time.Second)
		}
		err := s.store.SaveProxyCheckBundleContext(persistenceCtx, bundle.ProxyID, bundle.Results)
		cancelPersistence()
		if err != nil {
			failed += len(bundle.Results)
			persistenceFailed += len(bundle.Results)
			outcome.Persisted = false
		} else {
			for _, result := range bundle.Results {
				persisted++
				if result.Status == "available" {
					available++
				} else {
					failed++
					checkFailed++
				}
			}
		}
		if cfg.DeleteFailed && shouldDeleteFailedProxy(outcome) {
			deleted, err := s.store.DeleteProxyIfNoAvailableTargets(bundle.ProxyID)
			if err != nil {
				persistenceFailed++
				outcome.Persisted = false
			} else if deleted {
				deletedFailed++
			}
		}
		s.jobs.Update(jobID, map[string]any{
			"done":    done,
			"total":   total,
			"success": available,
			"failed":  failed,
			"message": checkProgressMessage(proxyDone, len(plans), done, total, profiles, targetProgress, s.checkConcurrency, cfg.Concurrent),
			"result":  checkProgressResult(proxyDone, len(plans), done, total, profiles, targetProgress, s.checkConcurrency, cfg.Concurrent),
		})
	}
	result := map[string]any{
		"checked":            done,
		"available":          available,
		"failed":             failed,
		"network_failed":     checkFailed,
		"persisted":          persisted,
		"persistence_failed": persistenceFailed,
		"deleted_failed":     deletedFailed,
		"target_profiles":    profiles,
		"proxy_done":         proxyDone,
		"proxy_total":        len(plans),
		"target_progress":    targetProgress,
		"execution_mode":     "proxy_parallel_target_ordered",
		"check_concurrency":  concurrencyStatus(s.checkConcurrency, cfg.Concurrent),
	}
	s.jobs.Update(jobID, map[string]any{"done": done, "total": total, "success": available, "failed": failed, "result": result})
	if ctx.Err() != nil {
		s.jobs.finishCancelled(jobID, "本机检测已停止")
		return
	}
	s.finalizeCheckJob(jobID, available, failed, persisted, persistenceFailed, deletedFailed, cfg.DeleteFailed, result)
}

func (s *server) finalizeCheckJob(jobID string, available int, failed int, persisted int, persistenceFailed int, deletedFailed int, deleteFailed bool, result map[string]any) {
	if persistenceFailed > 0 {
		message := fmt.Sprintf("检测部分完成：可用 %d，失败 %d，写入异常 %d", available, failed, persistenceFailed)
		if persisted == 0 {
			s.jobs.failWithResult(jobID, fmt.Errorf("所有检测结果均未能持久化"), result)
			return
		}
		s.jobs.partial(jobID, message, result)
		return
	}
	s.jobs.complete(jobID, checkCompleteMessage(available, failed, deletedFailed, deleteFailed), result)
}

func buildProxyCheckOutcomes(plans []checkPlan) map[int]*proxyCheckOutcome {
	out := map[int]*proxyCheckOutcome{}
	for _, plan := range plans {
		state := out[plan.Item.ID]
		if state == nil {
			state = &proxyCheckOutcome{AllFailed: true, Persisted: true}
			out[plan.Item.ID] = state
		}
		state.Expected += len(plan.TargetProfiles)
	}
	return out
}

func shouldDeleteFailedProxy(outcome *proxyCheckOutcome) bool {
	return outcome != nil && outcome.Seen == outcome.Expected && outcome.AllFailed && !outcome.BaseReachable && outcome.Persisted
}

func checkProgressMessage(proxyDone int, proxyTotal int, done int, total int, profiles []string, progress map[string]map[string]int, limiter *checkConcurrencyController, fallbackConcurrency int) string {
	status := concurrencyStatus(limiter, fallbackConcurrency)
	return fmt.Sprintf(
		"本机检测进行中：代理 %d/%d，目标项 %d/%d，%s，并发 %d/%d",
		proxyDone, proxyTotal, done, total, targetProgressText(profiles, progress),
		anyToInt(status["active"]), anyToInt(status["limit"]),
	)
}

func initialCheckProgress(plans []checkPlan, profiles []string, concurrency int) map[string]any {
	progress := newTargetCheckProgress(plans, profiles)
	return checkProgressResult(0, len(plans), 0, totalCheckPlanItems(plans), profiles, progress, nil, concurrency)
}

func checkProgressResult(proxyDone int, proxyTotal int, done int, total int, profiles []string, progress map[string]map[string]int, limiter *checkConcurrencyController, fallbackConcurrency int) map[string]any {
	return map[string]any{
		"checked":           done,
		"proxy_done":        proxyDone,
		"proxy_total":       proxyTotal,
		"target_item_done":  done,
		"target_item_total": total,
		"target_profiles":   append([]string(nil), profiles...),
		"target_progress":   progress,
		"execution_mode":    "proxy_parallel_target_ordered",
		"check_concurrency": concurrencyStatus(limiter, fallbackConcurrency),
	}
}

func newTargetCheckProgress(plans []checkPlan, profiles []string) map[string]map[string]int {
	progress := make(map[string]map[string]int, len(profiles))
	for _, profile := range profiles {
		progress[profile] = map[string]int{"done": 0, "total": 0, "available": 0, "failed": 0}
	}
	for _, plan := range plans {
		for _, profile := range plan.TargetProfiles {
			if progress[profile] == nil {
				progress[profile] = map[string]int{"done": 0, "total": 0, "available": 0, "failed": 0}
			}
			progress[profile]["total"]++
		}
	}
	return progress
}

func updateTargetCheckProgress(progress map[string]map[string]int, result CheckResult) {
	profile := normalizeTargetProfile(result.TargetProfile)
	if progress[profile] == nil {
		progress[profile] = map[string]int{"done": 0, "total": 0, "available": 0, "failed": 0}
	}
	progress[profile]["done"]++
	if result.Status == "available" {
		progress[profile]["available"]++
	} else {
		progress[profile]["failed"]++
	}
}

func targetProgressText(profiles []string, progress map[string]map[string]int) string {
	parts := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		item := progress[profile]
		parts = append(parts, fmt.Sprintf("%s %d/%d", targetProfileLabel(profile), item["done"], item["total"]))
	}
	return strings.Join(parts, " · ")
}

func targetProfileLabels(profiles []string) string {
	labels := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		labels = append(labels, targetProfileLabel(profile))
	}
	return strings.Join(labels, "、")
}

func concurrencyStatus(limiter *checkConcurrencyController, fallback int) map[string]any {
	if limiter != nil {
		return limiter.Status()
	}
	return map[string]any{
		"limit":   clampInt(fallback, 1, maxCheckConcurrency),
		"active":  0,
		"maximum": maxCheckConcurrency,
	}
}

func checkCompleteMessage(available int, failed int, deletedFailed int, deleteFailed bool) string {
	if deleteFailed {
		return fmt.Sprintf("检测完成：可用 %d，失败删除 %d", available, deletedFailed)
	}
	return fmt.Sprintf("检测完成：可用 %d，失败 %d", available, failed)
}

func normalizeTargetProfile(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if _, ok := targetProfiles[value]; ok {
		return value
	}
	return "generic"
}

func normalizeTargetProfileOrAll(value string, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "all" {
		return "all"
	}
	if _, ok := targetProfiles[value]; ok {
		return value
	}
	return normalizeTargetProfile(fallback)
}

func normalizeTargetProfiles(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "all" {
			return append([]string{}, targetProfileOrder...)
		}
		if _, ok := targetProfiles[value]; ok && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return []string{"generic"}
	}
	return out
}

func normalizeTargetProfilesOrNil(value string) []string {
	values := anyToStringSlice(value)
	if len(values) == 0 {
		return nil
	}
	return normalizeTargetProfiles(values)
}

func targetProfilesFromPayload(payload map[string]any, fallback []string) []string {
	values := anyToStringSlice(payload["target_profiles"])
	if len(values) == 0 {
		values = anyToStringSlice(payload["target_profile"])
	}
	if len(values) == 0 {
		values = fallback
	}
	return normalizeTargetProfiles(values)
}

func targetProfileLabel(value string) string {
	switch normalizeTargetProfile(value) {
	case "openai":
		return "OpenAI"
	case "grok":
		return "Grok"
	case "gemini":
		return "Gemini"
	case "claude":
		return "Claude"
	default:
		return "常规"
	}
}

func totalCheckPlanItems(plans []checkPlan) int {
	total := 0
	for _, plan := range plans {
		total += len(plan.TargetProfiles)
	}
	return total
}

func checkPlanProfiles(plans []checkPlan) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, plan := range plans {
		for _, profile := range plan.TargetProfiles {
			if !seen[profile] {
				seen[profile] = true
				out = append(out, profile)
			}
		}
	}
	return out
}

func buildProxyFirstCheckPlans(profiles []string, candidates map[string][]ProxyTask) []checkPlan {
	profiles = normalizeTargetProfiles(profiles)
	plans := []checkPlan{}
	byProxyID := map[int]int{}
	for _, profile := range profiles {
		for _, item := range candidates[profile] {
			index, ok := byProxyID[item.ID]
			if !ok {
				byProxyID[item.ID] = len(plans)
				plans = append(plans, checkPlan{Item: item, TargetProfiles: []string{profile}})
				continue
			}
			if !stringSliceContains(plans[index].TargetProfiles, profile) {
				plans[index].TargetProfiles = append(plans[index].TargetProfiles, profile)
			}
		}
	}
	return plans
}

func checkBatchStream(ctx context.Context, plans []checkPlan, cfg CheckConfig, geoCache *checkGeoCache, limiters ...*checkConcurrencyController) <-chan checkBundle {
	workers := minInt(maxInt(1, cfg.Concurrent), len(plans))
	var limiter *checkConcurrencyController
	if len(limiters) > 0 {
		limiter = limiters[0]
	}
	if limiter != nil {
		workers = minInt(maxCheckConcurrency, len(plans))
	}
	results := make(chan checkBundle, maxInt(1, workers))
	jobs := make(chan checkPlan)
	if workers == 0 {
		close(results)
		return results
	}
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case plan, ok := <-jobs:
					if !ok {
						return
					}
					if !limiter.Acquire(ctx) {
						return
					}
					bundle := checkProxyPlan(ctx, plan, cfg, geoCache)
					limiter.Release()
					select {
					case results <- bundle:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, plan := range plans {
			select {
			case jobs <- plan:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	return results
}

func checkProxyPlan(ctx context.Context, plan checkPlan, cfg CheckConfig, geoCache *checkGeoCache) checkBundle {
	profiles := normalizeTargetProfiles(plan.TargetProfiles)
	bundle := checkBundle{ProxyID: plan.Item.ID, Results: make([]CheckResult, 0, len(profiles))}
	proxyCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.HardTimeout)*time.Second)
	defer cancel()
	rounds := maxInt(1, cfg.Rounds)
	byProfile := make(map[string][]singleCheckResult, len(profiles))
	for round := 0; round < rounds; round++ {
		if proxyCtx.Err() != nil {
			break
		}
		results := checkProxyPlanOnce(proxyCtx, plan.Item, profiles, cfg, geoCache)
		if len(results) == 0 {
			break
		}
		for _, result := range results {
			byProfile[result.TargetProfile] = append(byProfile[result.TargetProfile], result)
		}
	}
	for _, profile := range profiles {
		results := byProfile[profile]
		if len(results) == 0 {
			if proxyCtx.Err() != nil {
				continue
			}
			failed := failedResult(plan.Item.ID, profile, firstNonEmpty(errorString(proxyCtx.Err()), "all protocol detection failed"))
			bundle.Results = append(bundle.Results, failed)
			continue
		}
		bundle.Results = append(bundle.Results, combineRoundResults(profile, results))
	}
	return bundle
}

func checkProxyPlanOnce(ctx context.Context, task ProxyTask, profiles []string, cfg CheckConfig, geoCache *checkGeoCache) []singleCheckResult {
	lastError := "all protocol detection failed"
	for _, candidate := range candidateProxyURLs(task) {
		client, detected, err := checkProxyHTTPClient(candidate, cfg.RequestTimeout)
		if err != nil {
			lastError = err.Error()
			continue
		}
		exitIP, baseLatency := probeBaseWithClient(ctx, client)
		targetResults := probeTargetsWithClient(ctx, client, profiles)
		if ctx.Err() != nil {
			closeHTTPClientIdleConnections(client)
			return nil
		}
		baseReachable := exitIP != nil
		if !baseReachable && !anyTargetProbeReachable(targetResults) {
			lastError = detected + " probe failed"
			closeHTTPClientIdleConnections(client)
			continue
		}
		metadata := checkmeta.Metadata{}
		if exitIP != nil {
			metadata = geoCache.Lookup(ctx, *exitIP)
		}
		results := make([]singleCheckResult, 0, len(profiles))
		for _, profile := range profiles {
			results = append(results, buildTargetCheckResult(task.ID, profile, detected, exitIP, baseLatency, metadata, targetResults[profile]))
		}
		closeHTTPClientIdleConnections(client)
		return results
	}
	results := make([]singleCheckResult, 0, len(profiles))
	for _, profile := range profiles {
		results = append(results, singleCheckResult{CheckResult: failedResult(task.ID, profile, lastError)})
	}
	return results
}

func probeBaseWithClient(ctx context.Context, client *http.Client) (*string, *int) {
	started := time.Now()
	for _, target := range checkExitIPTargets() {
		if value, ok := fetchExitIP(ctx, client, target); ok {
			latency := int(time.Since(started).Milliseconds())
			return &value, &latency
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, nil
}

func probeTargetsWithClient(ctx context.Context, client *http.Client, profiles []string) map[string]targetProbeResult {
	profiles = normalizeTargetProfiles(profiles)
	results := make([]targetProbeResult, len(profiles))
	for index, profile := range profiles {
		if ctx.Err() != nil {
			break
		}
		results[index] = probeTargetWithClient(ctx, client, targetProfiles[profile])
	}
	return targetProbeMap(profiles, results)
}

func probeTargetWithClient(ctx context.Context, client *http.Client, profile TargetProfile) targetProbeResult {
	started := time.Now()
	probeCtx, cancel := targetContext(ctx, profile)
	defer cancel()
	result := targetProbeResult{Total: 1}
	if ok, cloudflare := serviceOKStatus(probeCtx, client, profile, profile.ServiceURL, serviceAllowedStatus(profile)); ok {
		result.ServiceReachable = true
		result.Successes++
		latency := int(time.Since(started).Milliseconds())
		result.LatencyMS = &latency
		result.CloudflareStatus = stringPtr(cloudflare)
	} else if cloudflare != "" {
		result.CloudflareStatus = &cloudflare
	}
	if profile.APIURL != "" {
		result.Total++
		ok := okStatus(probeCtx, client, profile, profile.APIURL, apiAllowedStatus(profile))
		result.APIReachable = &ok
		if ok {
			result.Successes++
			if result.LatencyMS == nil {
				latency := int(time.Since(started).Milliseconds())
				result.LatencyMS = &latency
			}
		}
	}
	return result
}

func buildTargetCheckResult(proxyID int, targetProfile string, detected string, exitIP *string, baseLatency *int, metadata checkmeta.Metadata, target targetProbeResult) singleCheckResult {
	baseReachable := exitIP != nil
	latency := target.LatencyMS
	if latency == nil {
		latency = baseLatency
	}
	successes := target.Successes
	if baseReachable {
		successes++
	}
	total := maxInt(1, target.Total+1)
	successRate := float64(successes) / float64(total)
	status := targetAvailabilityStatus(targetProfile, baseReachable, target.ServiceReachable, target.APIReachable)
	grade := gradeResult(latency, successRate, target.ServiceReachable, target.APIReachable)
	if status == "failed" {
		grade = "F"
	}
	var lastError *string
	if status == "failed" {
		message := formatFailureError("target_unreachable", "proxy check failed")
		lastError = &message
	}
	result := CheckResult{
		ProxyID:          proxyID,
		Status:           status,
		Grade:            grade,
		LatencyMS:        latency,
		ExitIP:           exitIP,
		Country:          nonEmptyStringPtr(metadata.Country),
		CountryName:      nonEmptyStringPtr(metadata.CountryName),
		ContinentCode:    nonEmptyStringPtr(metadata.ContinentCode),
		IPType:           nonEmptyStringPtr(metadata.IPType),
		ASNOrg:           nonEmptyStringPtr(metadata.ASNOrg),
		GeoSource:        nonEmptyStringPtr(metadata.GeoSource),
		SuccessRate:      successRate,
		TargetProfile:    targetProfile,
		DetectedProtocol: stringPtr(detected),
		ServiceReachable: target.ServiceReachable,
		APIReachable:     target.APIReachable,
		CloudflareStatus: target.CloudflareStatus,
		RecommendedUse:   recommendUse(targetProfile, baseReachable, target.ServiceReachable, target.APIReachable, status),
		LastError:        lastError,
		BaseReachable:    baseReachable,
	}
	if !metadata.GeoUpdatedAt.IsZero() {
		result.GeoUpdatedAt = stringPtr(formatBeijingTime(metadata.GeoUpdatedAt))
	}
	return singleCheckResult{CheckResult: result, BaseReachable: baseReachable}
}

func newCheckGeoCache() *checkGeoCache {
	return newCheckGeoCacheWithLookup(func(ctx context.Context, ip string) checkmeta.Metadata {
		return checkEnrichIP(ctx, nil, "", ip)
	})
}

func newCheckGeoCacheWithLookup(lookup func(context.Context, string) checkmeta.Metadata) *checkGeoCache {
	return &checkGeoCache{values: map[string]*checkGeoCacheEntry{}, lookup: lookup}
}

func (c *checkGeoCache) Lookup(ctx context.Context, ip string) checkmeta.Metadata {
	if c == nil || strings.TrimSpace(ip) == "" {
		return checkmeta.Metadata{}
	}
	c.mu.Lock()
	if entry := c.values[ip]; entry != nil {
		ready := entry.ready
		c.mu.Unlock()
		select {
		case <-ready:
			return entry.metadata
		case <-ctx.Done():
			return checkmeta.Metadata{}
		}
	}
	entry := &checkGeoCacheEntry{ready: make(chan struct{})}
	c.values[ip] = entry
	c.mu.Unlock()
	if c.lookup != nil {
		entry.metadata = c.lookup(ctx, ip)
	}
	close(entry.ready)
	return entry.metadata
}

func targetProbeMap(profiles []string, values []targetProbeResult) map[string]targetProbeResult {
	out := make(map[string]targetProbeResult, len(profiles))
	for index, profile := range profiles {
		out[profile] = values[index]
	}
	return out
}

func anyTargetProbeReachable(results map[string]targetProbeResult) bool {
	for _, result := range results {
		if result.ServiceReachable || (result.APIReachable != nil && *result.APIReachable) {
			return true
		}
	}
	return false
}

func closeHTTPClientIdleConnections(client *http.Client) {
	if client != nil {
		client.CloseIdleConnections()
	}
}

func nonEmptyStringPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func stringSliceContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func targetAvailabilityStatus(profile string, baseReachable bool, serviceReachable bool, apiReachable *bool) string {
	if normalizeTargetProfile(profile) == "generic" {
		if baseReachable || serviceReachable {
			return "available"
		}
		return "failed"
	}
	if serviceReachable || (apiReachable != nil && *apiReachable) {
		return "available"
	}
	return "failed"
}

func proxyHTTPClient(proxyURL string, requestTimeout int) (*http.Client, string, error) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, "", err
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" && scheme != "socks4" && scheme != "socks5" && scheme != "socks5h" {
		return nil, "", fmt.Errorf("unsupported proxy scheme: %s", scheme)
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(parsed),
		TLSHandshakeTimeout:   time.Duration(requestTimeout) * time.Second,
		ResponseHeaderTimeout: time.Duration(requestTimeout) * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if scheme == "socks5" || scheme == "socks5h" {
		dialer, err := newSocks5Dialer(parsed, time.Duration(requestTimeout)*time.Second)
		if err != nil {
			return nil, "", err
		}
		transport.Proxy = nil
		transport.DialContext = dialer.DialContext
	}
	if scheme == "socks4" {
		dialer, err := newSocks4Dialer(parsed, time.Duration(requestTimeout)*time.Second)
		if err != nil {
			return nil, "", err
		}
		transport.Proxy = nil
		transport.DialContext = dialer.DialContext
	}
	return &http.Client{Transport: transport, Timeout: time.Duration(requestTimeout) * time.Second}, scheme, nil
}

type socks4Dialer struct {
	proxyAddress string
	userID       string
	timeout      time.Duration
}

func newSocks4Dialer(proxy *url.URL, timeout time.Duration) (*socks4Dialer, error) {
	if proxy.Host == "" {
		return nil, errors.New("missing socks4 proxy host")
	}
	host := proxy.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "1080")
	}
	dialer := &socks4Dialer{proxyAddress: host, timeout: timeout}
	if proxy.User != nil {
		dialer.userID = proxy.User.Username()
	}
	return dialer, nil
}

func (d *socks4Dialer) DialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	baseDialer := &net.Dialer{Timeout: d.timeout}
	conn, err := baseDialer.DialContext(ctx, network, d.proxyAddress)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(d.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	if err := d.handshake(conn, address); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (d *socks4Dialer) handshake(conn net.Conn, address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid target port")
	}
	request := []byte{0x04, 0x01, byte(port >> 8), byte(port)}
	if ip := net.ParseIP(host).To4(); ip != nil {
		request = append(request, ip...)
		request = append(request, []byte(d.userID)...)
		request = append(request, 0x00)
	} else {
		request = append(request, 0x00, 0x00, 0x00, 0x01)
		request = append(request, []byte(d.userID)...)
		request = append(request, 0x00)
		request = append(request, []byte(host)...)
		request = append(request, 0x00)
	}
	if _, err := conn.Write(request); err != nil {
		return err
	}
	reply := make([]byte, 8)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[1] != 0x5a {
		return fmt.Errorf("socks4 connect failed: reply=%d", reply[1])
	}
	return nil
}

type socks5Dialer struct {
	proxyAddress string
	username     string
	password     string
	timeout      time.Duration
}

func newSocks5Dialer(proxy *url.URL, timeout time.Duration) (*socks5Dialer, error) {
	if proxy.Host == "" {
		return nil, errors.New("missing socks5 proxy host")
	}
	host := proxy.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "1080")
	}
	dialer := &socks5Dialer{proxyAddress: host, timeout: timeout}
	if proxy.User != nil {
		dialer.username = proxy.User.Username()
		dialer.password, _ = proxy.User.Password()
	}
	return dialer, nil
}

func (d *socks5Dialer) DialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	baseDialer := &net.Dialer{Timeout: d.timeout}
	conn, err := baseDialer.DialContext(ctx, network, d.proxyAddress)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(d.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	if err := d.handshake(conn, address); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (d *socks5Dialer) handshake(conn net.Conn, address string) error {
	methods := []byte{0x00}
	if d.username != "" {
		methods = append(methods, 0x02)
	}
	greeting := append([]byte{0x05, byte(len(methods))}, methods...)
	if _, err := conn.Write(greeting); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[0] != 0x05 {
		return errors.New("invalid socks5 version")
	}
	switch reply[1] {
	case 0x00:
	case 0x02:
		if err := d.authenticate(conn); err != nil {
			return err
		}
	default:
		return errors.New("socks5 authentication method rejected")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid target port")
	}
	request := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			request = append(request, 0x01)
			request = append(request, ipv4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("target host too long")
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, []byte(host)...)
	}
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		return err
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return err
	}
	if header[0] != 0x05 {
		return errors.New("invalid socks5 response")
	}
	if header[1] != 0x00 {
		return fmt.Errorf("socks5 connect failed: reply=%d", header[1])
	}
	switch header[3] {
	case 0x01:
		_, err = io.CopyN(io.Discard, conn, 4)
	case 0x03:
		length := make([]byte, 1)
		if _, err = io.ReadFull(conn, length); err == nil {
			_, err = io.CopyN(io.Discard, conn, int64(length[0]))
		}
	case 0x04:
		_, err = io.CopyN(io.Discard, conn, 16)
	default:
		err = errors.New("invalid socks5 address type")
	}
	if err != nil {
		return err
	}
	_, err = io.CopyN(io.Discard, conn, 2)
	return err
}

func (d *socks5Dialer) authenticate(conn net.Conn) error {
	if len(d.username) > 255 || len(d.password) > 255 {
		return errors.New("socks5 credentials too long")
	}
	request := []byte{0x01, byte(len(d.username))}
	request = append(request, []byte(d.username)...)
	request = append(request, byte(len(d.password)))
	request = append(request, []byte(d.password)...)
	if _, err := conn.Write(request); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[0] != 0x01 || reply[1] != 0x00 {
		return errors.New("socks5 authentication failed")
	}
	return nil
}

func candidateProxyURLs(task ProxyTask) []string {
	if task.Protocol != "" && task.Protocol != "auto" {
		return []string{buildProxyURLWithProtocol(task, task.Protocol)}
	}
	if raw := strings.TrimSpace(task.Proxy); raw != "" {
		if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" && parsed.Host != "" && parsed.Scheme != "auto" {
			return []string{raw}
		}
	}
	protocols := []string{"http", "https", "socks5", "socks5h", "socks4"}
	values := make([]string, 0, len(protocols))
	for _, protocol := range protocols {
		values = append(values, buildProxyURLWithProtocol(task, protocol))
	}
	return values
}

func buildProxyURLWithProtocol(task ProxyTask, protocol string) string {
	auth := ""
	if task.Username != nil && *task.Username != "" {
		password := ""
		if task.Password != nil {
			password = *task.Password
		}
		auth = url.UserPassword(*task.Username, password).String() + "@"
	}
	return fmt.Sprintf("%s://%s%s:%d", protocol, auth, task.IP, task.Port)
}

func probeClient(ctx context.Context, client *http.Client, profile TargetProfile) bool {
	probeCtx, cancel := targetContext(ctx, profile)
	defer cancel()
	for _, target := range exitIPTargets() {
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, target, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
	}
	target := profile.APIURL
	allowed := apiAllowedStatus(profile)
	if target == "" {
		target = profile.ServiceURL
		allowed = serviceAllowedStatus(profile)
	}
	return okStatus(probeCtx, client, profile, target, allowed)
}

func okStatus(ctx context.Context, client *http.Client, profile TargetProfile, target string, allowed map[int]bool) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false
	}
	applyProfileHeaders(req, profile)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	_, _ = io.Copy(io.Discard, resp.Body)
	return allowed[resp.StatusCode] && keywordMatched(profile, string(raw))
}

func serviceOKStatus(ctx context.Context, client *http.Client, profile TargetProfile, target string, allowed map[int]bool) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, ""
	}
	applyProfileHeaders(req, profile)
	resp, err := client.Do(req)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	_, _ = io.Copy(io.Discard, resp.Body)
	body := string(raw)
	return allowed[resp.StatusCode] && keywordMatched(profile, body), checkmeta.DetectCloudflareStatus(resp.StatusCode, resp.Header, body)
}

func targetContext(ctx context.Context, profile TargetProfile) (context.Context, context.CancelFunc) {
	timeout := clampInt(profile.TimeoutSeconds, 0, 60)
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
}

func serviceAllowedStatus(profile TargetProfile) map[int]bool {
	if len(profile.ExpectedStatuses) > 0 {
		return statusSet(profile.ExpectedStatuses)
	}
	return map[int]bool{200: true, 204: true, 301: true, 302: true, 303: true, 307: true, 308: true}
}

func apiAllowedStatus(profile TargetProfile) map[int]bool {
	if len(profile.ExpectedStatuses) > 0 {
		return statusSet(profile.ExpectedStatuses)
	}
	return map[int]bool{200: true, 401: true, 403: true}
}

func statusSet(values []int) map[int]bool {
	out := map[int]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func applyProfileHeaders(req *http.Request, profile TargetProfile) {
	for key, value := range profile.Headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			req.Header.Set(key, value)
		}
	}
}

func keywordMatched(profile TargetProfile, body string) bool {
	keyword := strings.TrimSpace(profile.Keyword)
	if keyword == "" {
		return true
	}
	return strings.Contains(strings.ToLower(body), strings.ToLower(keyword))
}

func fetchExitIP(ctx context.Context, client *http.Client, target string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return "", false
	}
	var payload map[string]string
	if err := json.Unmarshal(raw, &payload); err != nil {
		value := strings.TrimSpace(string(raw))
		return value, value != "" && len(value) <= 64
	}
	value := strings.TrimSpace(payload["ip"])
	if value == "" {
		value = strings.Split(strings.TrimSpace(payload["origin"]), ",")[0]
	}
	return value, value != ""
}

func exitIPTargets() []string {
	return []string{"https://api.ipify.org?format=json", "https://httpbin.org/ip"}
}

func failedResult(proxyID int, profile string, message string) CheckResult {
	reason := classifyFailureReason(message)
	return CheckResult{
		ProxyID:        proxyID,
		Status:         "failed",
		Grade:          "C",
		SuccessRate:    0,
		TargetProfile:  profile,
		RecommendedUse: "invalid",
		LastError:      stringPtr(formatFailureError(reason, message)),
	}
}

func combineRoundResults(profile string, results []singleCheckResult) CheckResult {
	if len(results) == 0 {
		return failedResult(0, profile, "no check result")
	}
	available := make([]singleCheckResult, 0, len(results))
	for _, result := range results {
		if result.Status == "available" {
			available = append(available, result)
		}
	}
	status := "failed"
	if len(available)*2 >= len(results) {
		status = "available"
	}
	candidates := available
	if len(candidates) == 0 {
		candidates = results
	}
	best := candidates[0]
	for _, item := range candidates[1:] {
		bestLatency := latencySortValue(best.LatencyMS)
		itemLatency := latencySortValue(item.LatencyMS)
		if item.Status == "available" && best.Status != "available" {
			best = item
			continue
		}
		if itemLatency < bestLatency || (itemLatency == bestLatency && item.SuccessRate > best.SuccessRate) {
			best = item
		}
	}
	successRate := 0.0
	baseReachable := false
	serviceReachable := false
	apiSeen := false
	apiReachableValue := false
	var cloudflareStatus *string
	for _, result := range results {
		successRate += result.SuccessRate
		baseReachable = baseReachable || result.BaseReachable
		serviceReachable = serviceReachable || result.ServiceReachable
		if cloudflareStatus == nil && result.CloudflareStatus != nil {
			cloudflareStatus = result.CloudflareStatus
		}
		if result.APIReachable != nil {
			apiSeen = true
			apiReachableValue = apiReachableValue || *result.APIReachable
		}
	}
	successRate = successRate / float64(len(results))
	availableRatio := float64(len(available)) / float64(len(results))
	var apiReachable *bool
	if apiSeen {
		apiReachable = &apiReachableValue
	}
	grade := gradeResult(best.LatencyMS, successRate, serviceReachable, apiReachable)
	output := best.CheckResult
	output.Status = status
	output.SuccessRate = successRate
	if status == "available" {
		output.SuccessRate = availableRatio
	}
	output.Grade = grade
	if status == "failed" {
		output.Grade = "F"
	}
	output.ServiceReachable = serviceReachable
	output.APIReachable = apiReachable
	output.CloudflareStatus = cloudflareStatus
	output.BaseReachable = baseReachable
	output.RecommendedUse = recommendUse(profile, baseReachable, serviceReachable, apiReachable, status)
	if status == "failed" {
		message := "available_rounds=" + strconv.Itoa(len(available)) + "/" + strconv.Itoa(len(results))
		message = formatFailureError("rounds", message)
		output.LastError = &message
	}
	return output
}

func formatFailureError(reason string, message string) string {
	reason = strings.TrimSpace(reason)
	message = strings.TrimSpace(message)
	if reason == "" {
		reason = "unknown"
	}
	if strings.HasPrefix(message, "[") {
		return message
	}
	if message == "" {
		message = "proxy check failed"
	}
	return "[" + reason + "] " + message
}

func failureReasonFromMessage(message string) string {
	message = strings.TrimSpace(message)
	if strings.HasPrefix(message, "[") {
		if end := strings.Index(message, "]"); end > 1 {
			return strings.TrimSpace(message[1:end])
		}
	}
	return classifyFailureReason(message)
}

func classifyFailureReason(message string) string {
	text := strings.ToLower(strings.TrimSpace(message))
	switch {
	case text == "":
		return "unknown"
	case strings.Contains(text, "timeout") || strings.Contains(text, "deadline exceeded") || strings.Contains(text, "i/o timeout"):
		return "timeout"
	case strings.Contains(text, "no such host") || strings.Contains(text, "dns"):
		return "dns"
	case strings.Contains(text, "tls") || strings.Contains(text, "certificate") || strings.Contains(text, "handshake failure"):
		return "tls"
	case strings.Contains(text, "authentication failed") || strings.Contains(text, "proxy authentication") || strings.Contains(text, "407"):
		return "proxy_auth"
	case strings.Contains(text, "connection refused") || strings.Contains(text, "connect: cannot assign requested address") || strings.Contains(text, "network is unreachable"):
		return "tcp"
	case strings.Contains(text, "http ") || strings.Contains(text, "status"):
		return "http_status"
	case strings.Contains(text, "keyword"):
		return "keyword_mismatch"
	case strings.Contains(text, "cloudflare"):
		return "cloudflare"
	case strings.Contains(text, "probe failed") || strings.Contains(text, "target") || strings.Contains(text, "available_rounds"):
		return "target_unreachable"
	case strings.Contains(text, "unsupported proxy scheme") || strings.Contains(text, "protocol detection"):
		return "proxy_protocol"
	default:
		return "network"
	}
}

func gradeResult(latency *int, successRate float64, serviceReachable bool, apiReachable *bool) string {
	if successRate >= 0.9 && latency != nil && *latency <= 1500 && serviceReachable && (apiReachable == nil || *apiReachable) {
		return "A"
	}
	if successRate >= 0.65 && latency != nil && *latency <= 4000 {
		return "B"
	}
	if successRate > 0 {
		return "C"
	}
	return "F"
}

func latencySortValue(value *int) int {
	if value == nil {
		return 1 << 30
	}
	return *value
}

func recommendUse(profile string, baseReachable bool, serviceReachable bool, apiReachable *bool, status string) string {
	if profile == "generic" {
		if status != "available" {
			return "invalid"
		}
		if baseReachable && serviceReachable {
			return "generic"
		}
		if baseReachable || serviceReachable {
			return "unstable"
		}
		return "invalid"
	}
	if serviceReachable && apiReachable != nil && *apiReachable {
		return "web_api"
	}
	if apiReachable != nil && *apiReachable {
		return "api"
	}
	if serviceReachable {
		return "web"
	}
	if baseReachable {
		return "base"
	}
	return "invalid"
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}
