package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestTargetSpecificCheckResults(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if err := st.EnsureSettingsSchema(); err != nil {
		t.Fatalf("ensure settings schema: %v", err)
	}
	result, err := st.ImportProxies("http://1.2.3.4:8080", "test", "auto")
	if err != nil {
		t.Fatalf("import proxy: %v", err)
	}
	if result["added"] != 1 {
		t.Fatalf("expected one added proxy, got %#v", result)
	}
	items, total, err := st.ListProxies(proxyFilter{Status: "all", TargetProfile: "generic", Limit: 10})
	if err != nil {
		t.Fatalf("list imported proxy: %v", err)
	}
	if total != 1 || len(items) != 1 {
		t.Fatalf("expected one imported proxy, total=%d len=%d", total, len(items))
	}
	proxyID := items[0].ID
	if err := st.SaveCheckResult(CheckResult{
		ProxyID:        proxyID,
		Status:         "available",
		Grade:          "A",
		SuccessRate:    1,
		TargetProfile:  "generic",
		RecommendedUse: "generic",
	}); err != nil {
		t.Fatalf("save generic result: %v", err)
	}
	if err := st.SaveCheckResult(CheckResult{
		ProxyID:        proxyID,
		Status:         "failed",
		Grade:          "F",
		SuccessRate:    0,
		TargetProfile:  "openai",
		BaseReachable:  true,
		RecommendedUse: "invalid",
	}); err != nil {
		t.Fatalf("save openai result: %v", err)
	}

	genericAvailable, genericTotal, err := st.ListProxies(proxyFilter{Status: "available", TargetProfile: "generic", Limit: 10})
	if err != nil {
		t.Fatalf("list generic available: %v", err)
	}
	if genericTotal != 1 || len(genericAvailable) != 1 || genericAvailable[0].TargetProfile != "generic" {
		t.Fatalf("expected generic available result, total=%d items=%#v", genericTotal, genericAvailable)
	}
	openAIFailed, openAITotal, err := st.ListProxies(proxyFilter{Status: "failed", TargetProfile: "openai", Limit: 10})
	if err != nil {
		t.Fatalf("list openai failed: %v", err)
	}
	if openAITotal != 1 || len(openAIFailed) != 1 || openAIFailed[0].TargetProfile != "openai" {
		t.Fatalf("expected openai failed result, total=%d items=%#v", openAITotal, openAIFailed)
	}
	openAIExports, err := st.ExportAvailable("openai", 10)
	if err != nil {
		t.Fatalf("export openai: %v", err)
	}
	if len(openAIExports) != 0 {
		t.Fatalf("expected no openai available export, got %#v", openAIExports)
	}
	genericExports, err := st.ExportAvailable("generic", 10)
	if err != nil {
		t.Fatalf("export generic: %v", err)
	}
	if len(genericExports) != 1 {
		t.Fatalf("expected one generic available export, got %#v", genericExports)
	}
	geminiCandidates, err := st.ListCheckCandidates("untested", 10, "gemini")
	if err != nil {
		t.Fatalf("list gemini untested candidates: %v", err)
	}
	if len(geminiCandidates) != 1 {
		t.Fatalf("expected one gemini untested candidate, got %#v", geminiCandidates)
	}
	geminiUntested, err := st.CountTargetProxiesByStatus("untested", "gemini")
	if err != nil || geminiUntested != 1 {
		t.Fatalf("expected target-aware untested count, count=%d err=%v", geminiUntested, err)
	}
	globalItems, globalTotal, err := st.ListProxies(proxyFilter{Status: "available", TargetProfile: "all", Limit: 10})
	if err != nil {
		t.Fatalf("list aggregate available proxy: %v", err)
	}
	if globalTotal != 1 || len(globalItems) != 1 || globalItems[0].Probe == nil || globalItems[0].Probe.Status != "available" || globalItems[0].TargetSummary["generic"].Status != "available" {
		t.Fatalf("expected probe availability and target summary, total=%d items=%#v", globalTotal, globalItems)
	}
	deleted, err := st.DeleteProxyIfNoAvailableTargets(proxyID)
	if err != nil {
		t.Fatalf("safe failed deletion: %v", err)
	}
	if deleted {
		t.Fatalf("expected proxy with a generic available target to be retained")
	}
}

func TestCloudflareBlockedResultCannotEnterTargetPool(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080", "test", "http"); err != nil {
		t.Fatalf("import proxy: %v", err)
	}
	items, _, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatalf("list proxy: items=%#v err=%v", items, err)
	}
	blocked := "blocked"
	if err := st.SaveCheckResult(CheckResult{
		ProxyID:          items[0].ID,
		Status:           "available",
		Grade:            "A",
		TargetProfile:    "openai",
		ServiceReachable: true,
		APIReachable:     boolPtr(true),
		CloudflareStatus: &blocked,
		RecommendedUse:   "web_api",
	}); err != nil {
		t.Fatalf("save blocked result: %v", err)
	}
	var status string
	if err := st.db.QueryRow("SELECT status FROM proxy_target_state WHERE proxy_id = ? AND target_profile = 'openai'", items[0].ID).Scan(&status); err != nil {
		t.Fatalf("read target status: %v", err)
	}
	if status != "failed" {
		t.Fatalf("Cloudflare-blocked result entered target state as %q", status)
	}
	upstreams, err := st.GatewayUpstreamCandidates(availableProxyFilter{TargetProfile: "openai", Limit: 10})
	if err != nil || len(upstreams) != 0 {
		t.Fatalf("Cloudflare-blocked result entered gateway pool: upstreams=%#v err=%v", upstreams, err)
	}
}

func TestStatsIncludesTargetAvailability(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080\nhttp://5.6.7.8:8080", "test", "auto"); err != nil {
		t.Fatalf("import proxies: %v", err)
	}
	items, _, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil {
		t.Fatalf("list proxies: %v", err)
	}
	if err := st.SaveCheckResult(CheckResult{
		ProxyID:          items[0].ID,
		Status:           "available",
		Grade:            "A",
		SuccessRate:      1,
		TargetProfile:    "openai",
		ServiceReachable: true,
		RecommendedUse:   "openai",
	}); err != nil {
		t.Fatalf("save openai result: %v", err)
	}
	stats, err := st.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats["available"] != 1 || stats["available_records"] != 1 {
		t.Fatalf("expected one global available proxy, got %#v", stats)
	}
	openAI := targetStatsForTest(t, stats, "openai")
	if openAI["available"] != 1 || openAI["untested"] != 1 || openAI["total"] != 2 {
		t.Fatalf("unexpected openai stats: %#v", openAI)
	}
	generic := targetStatsForTest(t, stats, "generic")
	if generic["available"] != 0 || generic["untested"] != 2 || generic["total"] != 2 {
		t.Fatalf("unexpected generic stats: %#v", generic)
	}
}

func TestStatsShortCacheInvalidatesAfterWrites(t *testing.T) {
	st, _ := openStore(":memory:")
	_ = st.EnsureSchema("admin", "password")
	first, err := st.Stats()
	if err != nil || anyToInt(first["total"]) != 0 || first["generated_at"] == "" {
		t.Fatalf("unexpected initial stats: %#v err=%v", first, err)
	}
	second, err := st.Stats()
	if err != nil || second["generated_at"] != first["generated_at"] {
		t.Fatalf("expected cached stats generation: first=%#v second=%#v err=%v", first, second, err)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080", "test", "http"); err != nil {
		t.Fatalf("import proxy: %v", err)
	}
	third, err := st.Stats()
	if err != nil || anyToInt(third["total"]) != 1 || anyToInt(third["untested"]) != 1 {
		t.Fatalf("import did not invalidate stats: %#v err=%v", third, err)
	}
	items, _, _ := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err := st.SaveCheckResult(CheckResult{
		ProxyID: items[0].ID, Status: "available", Grade: "A", TargetProfile: "openai", ServiceReachable: true, RecommendedUse: "web",
	}); err != nil {
		t.Fatalf("save check: %v", err)
	}
	fourth, err := st.Stats()
	if err != nil || anyToInt(fourth["available"]) != 1 || anyToInt(fourth["target_available_records"]) != 1 {
		t.Fatalf("check write did not invalidate aggregate stats: %#v err=%v", fourth, err)
	}
}

func TestCountAvailableProxyURLsDeduplicatesDetectedProtocol(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.1.1.1:8080\nsocks5://1.1.1.1:8080\nhttp://2.2.2.2:8080", "test", "auto"); err != nil {
		t.Fatalf("import proxies: %v", err)
	}
	items, _, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil {
		t.Fatalf("list proxies: %v", err)
	}
	for _, item := range items {
		if err := st.SaveCheckResult(CheckResult{
			ProxyID:          item.ID,
			Status:           "available",
			Grade:            "A",
			SuccessRate:      1,
			TargetProfile:    "openai",
			ServiceReachable: true,
			DetectedProtocol: stringPtr("http"),
			RecommendedUse:   "openai",
		}); err != nil {
			t.Fatalf("save check result: %v", err)
		}
	}
	count, err := st.CountAvailableProxyURLs("openai")
	if err != nil {
		t.Fatalf("count available proxy urls: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected two unique available proxy urls, got %d", count)
	}
	unionCount, err := st.CountAvailableProxyURLsForProfiles([]string{"openai", "grok"})
	if err != nil {
		t.Fatalf("count available proxy urls for profiles: %v", err)
	}
	if unionCount != 2 {
		t.Fatalf("expected two unique available proxy urls across profiles, got %d", unionCount)
	}
	stats, err := st.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats["available"] != 2 || stats["available_records"] != 3 {
		t.Fatalf("expected unique available count and record count, got %#v", stats)
	}
	if stats["unique_target_available"] != 2 {
		t.Fatalf("expected two globally unique target URLs, got %#v", stats)
	}
	openAI := targetStatsForTest(t, stats, "openai")
	if openAI["available"] != 2 || openAI["available_records"] != 3 {
		t.Fatalf("expected target URL dedupe without losing record count, got %#v", openAI)
	}
	urls, err := st.AvailableProxyURLs(0, "openai")
	if err != nil {
		t.Fatalf("available proxy urls: %v", err)
	}
	if len(urls) != count {
		t.Fatalf("count and exported urls differ: count=%d urls=%#v", count, urls)
	}
	limited, err := st.AvailableProxyURLs(1, "openai")
	if err != nil {
		t.Fatalf("limited available proxy urls: %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("expected limit after dedupe, got %#v", limited)
	}
}

func TestAvailableProxyURLsFilteredByCountryAndFallback(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.1.1.1:8080\nhttp://2.2.2.2:8080\nhttp://3.3.3.3:8080", "test", "auto"); err != nil {
		t.Fatalf("import proxies: %v", err)
	}
	items, _, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil {
		t.Fatalf("list proxies: %v", err)
	}
	countriesByIP := map[string]string{
		"1.1.1.1": "US",
		"2.2.2.2": "JP",
		"3.3.3.3": "US",
	}
	for _, item := range items {
		country := countriesByIP[item.IP]
		if country == "" {
			t.Fatalf("missing country fixture for %s", item.IP)
		}
		if err := st.SaveCheckResult(CheckResult{
			ProxyID:          item.ID,
			Status:           "available",
			Grade:            "A",
			Country:          stringPtr(country),
			CountryName:      stringPtr(map[string]string{"US": "United States", "JP": "Japan"}[country]),
			GeoSource:        stringPtr("mmdb"),
			SuccessRate:      1,
			TargetProfile:    "openai",
			ServiceReachable: true,
			RecommendedUse:   "openai",
		}); err != nil {
			t.Fatalf("save %s result: %v", item.IP, err)
		}
	}
	for _, item := range items {
		if item.IP != "1.1.1.1" {
			continue
		}
		if err := st.SaveCheckResult(CheckResult{
			ProxyID:          item.ID,
			Status:           "available",
			Grade:            "A",
			Country:          stringPtr("US"),
			SuccessRate:      1,
			TargetProfile:    "grok",
			ServiceReachable: true,
			RecommendedUse:   "grok",
		}); err != nil {
			t.Fatalf("save grok fallback fixture: %v", err)
		}
	}

	usCount, err := st.CountAvailableProxyURLsFiltered(availableProxyFilter{TargetProfile: "openai", Countries: []string{"us"}})
	if err != nil {
		t.Fatalf("count US available urls: %v", err)
	}
	if usCount != 2 {
		t.Fatalf("expected two US upstreams, got %d", usCount)
	}
	usItems, total, err := st.ListProxies(proxyFilter{Status: "available", TargetProfile: "openai", Countries: []string{"US"}, Limit: 10})
	if err != nil {
		t.Fatalf("list US proxies: %v", err)
	}
	if total != 2 || len(usItems) != 2 {
		t.Fatalf("expected two US proxy records, total=%d items=%#v", total, usItems)
	}
	jpURLs, err := st.AvailableProxyURLsFiltered(availableProxyFilter{TargetProfile: "openai", Countries: []string{"jp"}})
	if err != nil {
		t.Fatalf("JP available urls: %v", err)
	}
	if len(jpURLs) != 1 || jpURLs[0] != "http://2.2.2.2:8080" {
		t.Fatalf("expected only JP upstream, got %#v", jpURLs)
	}
	strictMissing, err := st.AvailableProxyURLsFiltered(availableProxyFilter{TargetProfile: "openai", Countries: []string{"DE"}, CountryPolicy: gatewayCountryPolicyStrict})
	if err != nil {
		t.Fatalf("strict missing country urls: %v", err)
	}
	if len(strictMissing) != 0 {
		t.Fatalf("expected strict missing country to return no urls, got %#v", strictMissing)
	}
	fallbackMissing, err := st.AvailableProxyURLsFiltered(availableProxyFilter{TargetProfile: "openai", Countries: []string{"DE"}, CountryPolicy: gatewayCountryPolicyFallbackAny})
	if err != nil {
		t.Fatalf("fallback missing country urls: %v", err)
	}
	if len(fallbackMissing) != 3 {
		t.Fatalf("expected fallback to return all available urls, got %#v", fallbackMissing)
	}
	multiProfileCount, err := st.CountAvailableProxyURLsForProfilesFiltered([]string{"openai", "grok"}, availableProxyFilter{
		Countries:     []string{"JP"},
		CountryPolicy: gatewayCountryPolicyFallbackAny,
	})
	if err != nil {
		t.Fatalf("count multi-profile fallback urls: %v", err)
	}
	if multiProfileCount != 2 {
		t.Fatalf("expected per-profile fallback to count JP openai and fallback grok urls, got %d", multiProfileCount)
	}
}

func TestSourceHealthFailureCooldownAndRecovery(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := st.RecordSourceFetch("source_a", 0, 0, 0, errTestSourceFetch); err != nil {
			t.Fatalf("record failed source fetch: %v", err)
		}
	}
	coolingDown, disabledUntil, err := st.SourceCoolingDown("source_a")
	if err != nil {
		t.Fatalf("source cooling down: %v", err)
	}
	if !coolingDown || disabledUntil == "" {
		t.Fatalf("expected source cooldown, cooling=%v until=%q", coolingDown, disabledUntil)
	}
	if err := st.RecordSourceFetch("source_a", 10, 4, 6, nil); err != nil {
		t.Fatalf("record successful source fetch: %v", err)
	}
	coolingDown, _, err = st.SourceCoolingDown("source_a")
	if err != nil {
		t.Fatalf("source cooling down after recovery: %v", err)
	}
	if coolingDown {
		t.Fatalf("expected successful fetch to clear source cooldown")
	}
	health, err := st.SourceHealth()
	if err != nil {
		t.Fatalf("source health: %v", err)
	}
	item := health["source_a"]
	if item["failure_streak"] != 0 || item["last_new"] != 4 || item["last_updated"] != 6 {
		t.Fatalf("unexpected source health after recovery: %#v", item)
	}
}

var errTestSourceFetch = testError("fetch failed")

type testError string

func (e testError) Error() string { return string(e) }

func targetStatsForTest(t *testing.T, stats map[string]any, profile string) map[string]any {
	t.Helper()
	targets, ok := stats["by_target"].([]map[string]any)
	if !ok {
		t.Fatalf("missing by_target stats: %#v", stats["by_target"])
	}
	for _, item := range targets {
		if item["target_profile"] == profile {
			return item
		}
	}
	t.Fatalf("missing stats for target %q: %#v", profile, targets)
	return nil
}

func TestImportProxiesDeduplicatesByProxyKey(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	result, err := st.ImportProxies("http://1.2.3.4:8080\n1.2.3.4:8080", "test", "http")
	if err != nil {
		t.Fatalf("import proxies: %v", err)
	}
	if result["added"] != 1 || result["updated"] != 0 || result["total"] != 1 {
		t.Fatalf("expected duplicate lines to collapse to one added proxy, got %#v", result)
	}
	result, err = st.ImportProxies("1.2.3.4:8080", "test-again", "http")
	if err != nil {
		t.Fatalf("reimport proxy: %v", err)
	}
	if result["added"] != 0 || result["updated"] != 1 {
		t.Fatalf("expected reimport to update existing proxy, got %#v", result)
	}
	_, total, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil {
		t.Fatalf("list proxies: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected one stored proxy after duplicate import, got %d", total)
	}
}

func TestSettingsNormalizeGatewayCountries(t *testing.T) {
	settings := settingsFromPayload(defaultAppSettings(), map[string]any{
		"gateway_countries":      "jp, us, invalid,JP",
		"gateway_country_policy": "fallback",
	})
	if settings.GatewayCountryPolicy != gatewayCountryPolicyFallbackAny {
		t.Fatalf("expected fallback_any policy, got %q", settings.GatewayCountryPolicy)
	}
	if len(settings.GatewayCountries) != 2 || settings.GatewayCountries[0] != "JP" || settings.GatewayCountries[1] != "US" {
		t.Fatalf("unexpected normalized gateway countries: %#v", settings.GatewayCountries)
	}
}

func TestDeleteExpiredUntestedOnlyDeletesOldUntested(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080\nhttp://5.6.7.8:8080", "test", "auto"); err != nil {
		t.Fatalf("import proxies: %v", err)
	}
	if _, err := st.db.Exec(`
UPDATE proxies
SET created_at = datetime('now', '+8 hours', '-4 hours'),
	    updated_at = datetime('now', '+8 hours', '-4 hours'),
	    status_changed_at = datetime('now', '+8 hours', '-4 hours')
WHERE proxy_key = 'http://1.2.3.4:8080';
UPDATE proxy_probe_state
SET status_changed_at = datetime('now', '+8 hours', '-4 hours'),
    updated_at = datetime('now', '+8 hours', '-4 hours')
WHERE proxy_id = (SELECT id FROM proxies WHERE proxy_key = 'http://1.2.3.4:8080')`); err != nil {
		t.Fatalf("age proxy: %v", err)
	}
	deleted, err := st.DeleteExpiredUntested(1)
	if err != nil {
		t.Fatalf("delete expired untested: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected one expired untested deleted, got %d", deleted)
	}
	_, total, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil {
		t.Fatalf("list proxies: %v", err)
	}
	if total != 1 {
		t.Fatalf("expected one fresh proxy left, got %d", total)
	}
}

func TestRequeuedProxyGetsFreshUntestedTTL(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080", "test", "http"); err != nil {
		t.Fatalf("import proxy: %v", err)
	}
	items, _, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatalf("list proxy: items=%#v err=%v", items, err)
	}
	if err := st.SaveCheckResult(CheckResult{ProxyID: items[0].ID, Status: "available", Grade: "A", TargetProfile: "generic", BaseReachable: true, RecommendedUse: "generic"}); err != nil {
		t.Fatalf("save available result: %v", err)
	}
	if _, err := st.db.Exec(`
UPDATE proxy_checks SET checked_at = datetime('now', '+8 hours', '-2 hours'), updated_at = datetime('now', '+8 hours', '-2 hours') WHERE proxy_id = ?;
UPDATE proxy_target_state SET checked_at = datetime('now', '+8 hours', '-2 hours'), updated_at = datetime('now', '+8 hours', '-2 hours') WHERE proxy_id = ?;
UPDATE proxy_probe_state SET checked_at = datetime('now', '+8 hours', '-2 hours'), updated_at = datetime('now', '+8 hours', '-2 hours') WHERE proxy_id = ?;
UPDATE proxies SET last_checked_at = datetime('now', '+8 hours', '-2 hours'), updated_at = datetime('now', '+8 hours', '-2 hours') WHERE id = ?`, items[0].ID, items[0].ID, items[0].ID, items[0].ID); err != nil {
		t.Fatalf("age available proxy: %v", err)
	}
	requeued, err := st.RequeueExpiredAvailable(1)
	if err != nil || requeued != 1 {
		t.Fatalf("expected one requeued proxy, count=%d err=%v", requeued, err)
	}
	deleted, err := st.DeleteExpiredUntested(1)
	if err != nil {
		t.Fatalf("delete expired untested: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected freshly requeued proxy to keep a full untested TTL, deleted=%d", deleted)
	}
}

func TestStoredTargetAvailabilityMigrationAndSafeFailedCleanup(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080", "test", "http"); err != nil {
		t.Fatalf("import proxy: %v", err)
	}
	items, _, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatalf("list proxy: items=%#v err=%v", items, err)
	}
	proxyID := items[0].ID
	if err := st.SaveCheckResult(CheckResult{
		ProxyID:        proxyID,
		Status:         "available",
		Grade:          "B",
		ExitIP:         stringPtr("8.8.8.8"),
		BaseReachable:  true,
		TargetProfile:  "grok",
		RecommendedUse: "base",
	}); err != nil {
		t.Fatalf("save base-only target result: %v", err)
	}
	grokFailed, total, err := st.ListProxies(proxyFilter{Status: "failed", TargetProfile: "grok", Limit: 10})
	if err != nil || total != 1 || len(grokFailed) != 1 || grokFailed[0].RecommendedUse != "base" {
		t.Fatalf("expected legacy base-only target result to become failed/base, total=%d items=%#v err=%v", total, grokFailed, err)
	}
	deleted, err := st.DeleteFailedProxies()
	if err != nil {
		t.Fatalf("delete failed proxies: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected target-only failure without generic evidence to be retained, deleted=%d", deleted)
	}
}

func TestFailedCleanupRequiresGenericFailureWithoutAvailableTarget(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080\nhttp://5.6.7.8:8080", "test", "http"); err != nil {
		t.Fatalf("import proxies: %v", err)
	}
	items, _, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil || len(items) != 2 {
		t.Fatalf("list proxies: items=%#v err=%v", items, err)
	}
	for _, item := range items {
		for attempt := 0; attempt < 2; attempt++ {
			if err := st.SaveCheckResult(CheckResult{ProxyID: item.ID, Status: "failed", Grade: "F", TargetProfile: "generic", RecommendedUse: "invalid"}); err != nil {
				t.Fatalf("save generic failure: %v", err)
			}
		}
	}
	if err := st.SaveCheckResult(CheckResult{ProxyID: items[0].ID, Status: "available", Grade: "A", TargetProfile: "grok", APIReachable: boolPtr(true), RecommendedUse: "api"}); err != nil {
		t.Fatalf("save target availability: %v", err)
	}
	deleted, err := st.DeleteFailedProxies()
	if err != nil {
		t.Fatalf("delete failed proxies: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected only globally unavailable generic failure to be deleted, got %d", deleted)
	}
}

func TestStateModelSchemaVersionAndMigrationIdempotency(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	version, err := st.SchemaVersion()
	if err != nil || version != cloudflareTargetMigrationVersion {
		t.Fatalf("unexpected schema version=%d err=%v", version, err)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080", "legacy", "http"); err != nil {
		t.Fatalf("import proxy: %v", err)
	}
	items, _, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatalf("list proxy: items=%#v err=%v", items, err)
	}
	proxyID := items[0].ID
	if _, err := st.db.Exec(`
INSERT INTO proxy_checks (
  proxy_id, target_profile, status, status_changed_at, grade, exit_ip,
  service_reachable, api_reachable, recommended_use, checked_at, updated_at
)
VALUES (?, 'grok', 'available', datetime('now', '+8 hours', '-1 hour'), 'B', '8.8.8.8', 0, 0, 'base', datetime('now', '+8 hours'), datetime('now', '+8 hours'))
ON CONFLICT(proxy_id, target_profile) DO UPDATE SET
  status = excluded.status, exit_ip = excluded.exit_ip, service_reachable = 0,
  api_reachable = 0, recommended_use = 'base', checked_at = excluded.checked_at,
  updated_at = excluded.updated_at;
DROP TABLE proxy_target_state;
DROP TABLE proxy_probe_state`, proxyID); err != nil {
		t.Fatalf("prepare legacy state: %v", err)
	}
	if _, err := st.db.Exec("DELETE FROM schema_migrations WHERE version = ?", stateModelMigrationVersion); err != nil {
		t.Fatalf("clear migration record: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("migrate legacy state: %v", err)
	}
	var targetStatus, capability string
	if err := st.db.QueryRow(`
SELECT status, capability FROM proxy_target_state WHERE proxy_id = ? AND target_profile = 'grok'`, proxyID).Scan(&targetStatus, &capability); err != nil {
		t.Fatalf("read migrated target: %v", err)
	}
	if targetStatus != "failed" || capability != "base" {
		t.Fatalf("expected strict base-only migration, status=%q capability=%q", targetStatus, capability)
	}
	var probeCount, targetCount int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM proxy_probe_state WHERE proxy_id = ?", proxyID).Scan(&probeCount); err != nil {
		t.Fatalf("count probe rows: %v", err)
	}
	if err := st.db.QueryRow("SELECT COUNT(*) FROM proxy_target_state WHERE proxy_id = ? AND target_profile = 'grok'", proxyID).Scan(&targetCount); err != nil {
		t.Fatalf("count target rows: %v", err)
	}
	if probeCount != 1 || targetCount != 1 {
		t.Fatalf("unexpected migrated counts probe=%d target=%d", probeCount, targetCount)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	if err := st.db.QueryRow("SELECT COUNT(*) FROM proxy_probe_state WHERE proxy_id = ?", proxyID).Scan(&probeCount); err != nil || probeCount != 1 {
		t.Fatalf("migration was not idempotent, count=%d err=%v", probeCount, err)
	}
	var integrity string
	if err := st.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity check=%q err=%v", integrity, err)
	}
}

func TestCloudflareTargetMigrationReclassifiesLegacyAvailability(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080", "legacy", "http"); err != nil {
		t.Fatalf("import proxy: %v", err)
	}
	items, _, _ := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	proxyID := items[0].ID
	if err := st.SaveCheckResult(CheckResult{ProxyID: proxyID, Status: "available", Grade: "A", TargetProfile: "openai", ServiceReachable: true, RecommendedUse: "web"}); err != nil {
		t.Fatalf("save target: %v", err)
	}
	if _, err := st.db.Exec("UPDATE proxy_target_state SET status = 'available', grade = 'A', cloudflare_status = 'challenge', failure_reason = '', last_error = NULL WHERE proxy_id = ? AND target_profile = 'openai'", proxyID); err != nil {
		t.Fatalf("prepare legacy target state: %v", err)
	}
	if _, err := st.db.Exec("UPDATE proxy_checks SET status = 'available', grade = 'A', cloudflare_status = 'challenge', last_error = NULL WHERE proxy_id = ? AND target_profile = 'openai'", proxyID); err != nil {
		t.Fatalf("prepare legacy shadow state: %v", err)
	}
	if _, err := st.db.Exec("DELETE FROM schema_migrations WHERE version = ?", cloudflareTargetMigrationVersion); err != nil {
		t.Fatalf("prepare legacy Cloudflare state: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("apply Cloudflare migration: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("repeat Cloudflare migration: %v", err)
	}
	var targetStatus, shadowStatus, reason string
	if err := st.db.QueryRow("SELECT status, failure_reason FROM proxy_target_state WHERE proxy_id = ? AND target_profile = 'openai'", proxyID).Scan(&targetStatus, &reason); err != nil {
		t.Fatalf("read target state: %v", err)
	}
	if err := st.db.QueryRow("SELECT status FROM proxy_checks WHERE proxy_id = ? AND target_profile = 'openai'", proxyID).Scan(&shadowStatus); err != nil {
		t.Fatalf("read shadow state: %v", err)
	}
	if targetStatus != "failed" || shadowStatus != "failed" || reason != "cloudflare" {
		t.Fatalf("legacy Cloudflare availability not reclassified: target=%q shadow=%q reason=%q", targetStatus, shadowStatus, reason)
	}
	var migrationCount int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", cloudflareTargetMigrationVersion).Scan(&migrationCount); err != nil || migrationCount != 1 {
		t.Fatalf("migration is not idempotent: count=%d err=%v", migrationCount, err)
	}
}

func TestProbeAndTargetStateRemainIndependent(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080", "test", "http"); err != nil {
		t.Fatalf("import proxy: %v", err)
	}
	items, _, _ := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	proxyID := items[0].ID
	if err := st.SaveCheckResult(CheckResult{
		ProxyID: proxyID, Status: "available", Grade: "A", TargetProfile: "openai",
		BaseReachable: true, ServiceReachable: true, RecommendedUse: "web",
	}); err != nil {
		t.Fatalf("save openai availability: %v", err)
	}
	if err := st.SaveCheckResult(CheckResult{
		ProxyID: proxyID, Status: "failed", Grade: "F", TargetProfile: "grok",
		BaseReachable: true, RecommendedUse: "base",
	}); err != nil {
		t.Fatalf("save grok failure: %v", err)
	}
	var probeStatus, openAIStatus, grokStatus string
	if err := st.db.QueryRow("SELECT status FROM proxy_probe_state WHERE proxy_id = ?", proxyID).Scan(&probeStatus); err != nil {
		t.Fatalf("read probe: %v", err)
	}
	if err := st.db.QueryRow("SELECT status FROM proxy_target_state WHERE proxy_id = ? AND target_profile = 'openai'", proxyID).Scan(&openAIStatus); err != nil {
		t.Fatalf("read openai: %v", err)
	}
	if err := st.db.QueryRow("SELECT status FROM proxy_target_state WHERE proxy_id = ? AND target_profile = 'grok'", proxyID).Scan(&grokStatus); err != nil {
		t.Fatalf("read grok: %v", err)
	}
	if probeStatus != "available" || openAIStatus != "available" || grokStatus != "failed" {
		t.Fatalf("state dimensions overwrote each other: probe=%s openai=%s grok=%s", probeStatus, openAIStatus, grokStatus)
	}
	if _, err := st.db.Exec("UPDATE proxy_checks SET status = 'available' WHERE proxy_id = ? AND target_profile = 'grok'", proxyID); err != nil {
		t.Fatalf("mutate legacy shadow: %v", err)
	}
	exported, err := st.ExportAvailable("grok", 10)
	if err != nil || len(exported) != 0 {
		t.Fatalf("legacy shadow influenced export: items=%#v err=%v", exported, err)
	}
}

func TestTargetAndProbeTTLRequeueIndependently(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080", "test", "http"); err != nil {
		t.Fatalf("import proxy: %v", err)
	}
	items, _, _ := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	proxyID := items[0].ID
	if err := st.SaveCheckResult(CheckResult{
		ProxyID: proxyID, Status: "available", Grade: "A", TargetProfile: "openai",
		BaseReachable: true, ServiceReachable: true, RecommendedUse: "web",
	}); err != nil {
		t.Fatalf("save result: %v", err)
	}
	if _, err := st.db.Exec(`
UPDATE proxy_target_state SET checked_at = datetime('now', '+8 hours', '-2 hours'), updated_at = datetime('now', '+8 hours', '-2 hours') WHERE proxy_id = ?;
UPDATE proxy_probe_state SET checked_at = datetime('now', '+8 hours'), updated_at = datetime('now', '+8 hours') WHERE proxy_id = ?`, proxyID, proxyID); err != nil {
		t.Fatalf("age target only: %v", err)
	}
	if _, err := st.RequeueExpiredAvailable(1); err != nil {
		t.Fatalf("requeue target: %v", err)
	}
	var probeStatus, targetStatus, targetChanged string
	if err := st.db.QueryRow("SELECT status FROM proxy_probe_state WHERE proxy_id = ?", proxyID).Scan(&probeStatus); err != nil {
		t.Fatalf("read probe: %v", err)
	}
	if err := st.db.QueryRow("SELECT status, status_changed_at FROM proxy_target_state WHERE proxy_id = ? AND target_profile = 'openai'", proxyID).Scan(&targetStatus, &targetChanged); err != nil {
		t.Fatalf("read target: %v", err)
	}
	if probeStatus != "available" || targetStatus != "untested" || targetChanged == "" {
		t.Fatalf("TTL dimensions not independent: probe=%s target=%s changed=%q", probeStatus, targetStatus, targetChanged)
	}
}

func TestCheckStateWriteIsAtomic(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080", "test", "http"); err != nil {
		t.Fatalf("import proxy: %v", err)
	}
	items, _, _ := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	proxyID := items[0].ID
	if _, err := st.db.Exec(`
CREATE TRIGGER reject_target_state_insert
BEFORE INSERT ON proxy_target_state
BEGIN
  SELECT RAISE(ABORT, 'target write rejected');
END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	err = st.SaveCheckResult(CheckResult{
		ProxyID: proxyID, Status: "available", Grade: "A", TargetProfile: "openai",
		BaseReachable: true, ServiceReachable: true, RecommendedUse: "web",
	})
	if err == nil {
		t.Fatalf("expected target state write failure")
	}
	var probeStatus string
	if err := st.db.QueryRow("SELECT status FROM proxy_probe_state WHERE proxy_id = ?", proxyID).Scan(&probeStatus); err != nil {
		t.Fatalf("read probe status: %v", err)
	}
	if probeStatus != "untested" {
		t.Fatalf("probe write escaped rolled-back transaction: %s", probeStatus)
	}
	var targetCount int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM proxy_target_state WHERE proxy_id = ?", proxyID).Scan(&targetCount); err != nil || targetCount != 0 {
		t.Fatalf("unexpected target rows=%d err=%v", targetCount, err)
	}
}

func TestProxyCheckBundleWritesProbeAndTargetsAtomically(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080", "test", "http"); err != nil {
		t.Fatalf("import proxy: %v", err)
	}
	items, _, _ := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	proxyID := items[0].ID
	if _, err := st.db.Exec(`
CREATE TRIGGER reject_grok_target_state
BEFORE INSERT ON proxy_target_state
WHEN NEW.target_profile = 'grok'
BEGIN
  SELECT RAISE(ABORT, 'grok target write rejected');
END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	err = st.SaveProxyCheckBundle(proxyID, []CheckResult{
		{ProxyID: proxyID, Status: "available", Grade: "A", TargetProfile: "openai", BaseReachable: true, ServiceReachable: true, RecommendedUse: "web"},
		{ProxyID: proxyID, Status: "available", Grade: "A", TargetProfile: "grok", BaseReachable: true, ServiceReachable: true, RecommendedUse: "web"},
	})
	if err == nil {
		t.Fatalf("expected bundle write failure")
	}
	var probeStatus string
	if err := st.db.QueryRow("SELECT status FROM proxy_probe_state WHERE proxy_id = ?", proxyID).Scan(&probeStatus); err != nil {
		t.Fatalf("read probe status: %v", err)
	}
	if probeStatus != "untested" {
		t.Fatalf("probe write escaped bundle rollback: %s", probeStatus)
	}
	for _, table := range []string{"proxy_target_state", "proxy_checks"} {
		var count int
		if err := st.db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE proxy_id = ?", proxyID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("unexpected %s rows=%d err=%v", table, count, err)
		}
	}
}

func TestProxyCheckBundleUpdatesProbeOnce(t *testing.T) {
	st, _ := openStore(":memory:")
	_ = st.EnsureSchema("admin", "password")
	_, _ = st.ImportProxies("http://1.2.3.4:8080", "test", "http")
	items, _, _ := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	proxyID := items[0].ID
	errorMessage := "[connect] failed"
	if err := st.SaveProxyCheckBundle(proxyID, []CheckResult{
		{ProxyID: proxyID, Status: "failed", Grade: "F", TargetProfile: "openai", RecommendedUse: "invalid", LastError: &errorMessage},
		{ProxyID: proxyID, Status: "failed", Grade: "F", TargetProfile: "grok", RecommendedUse: "invalid", LastError: &errorMessage},
	}); err != nil {
		t.Fatalf("save failed bundle: %v", err)
	}
	var probeFailures, targetCount int
	if err := st.db.QueryRow("SELECT consecutive_failures FROM proxy_probe_state WHERE proxy_id = ?", proxyID).Scan(&probeFailures); err != nil {
		t.Fatalf("read probe failures: %v", err)
	}
	if err := st.db.QueryRow("SELECT COUNT(*) FROM proxy_target_state WHERE proxy_id = ?", proxyID).Scan(&targetCount); err != nil {
		t.Fatalf("count targets: %v", err)
	}
	if probeFailures != 1 || targetCount != 2 {
		t.Fatalf("expected one probe transition and two target writes, probe=%d targets=%d", probeFailures, targetCount)
	}
}

func TestProxyCheckBundleHonorsCancelledContext(t *testing.T) {
	st, _ := openStore(":memory:")
	_ = st.EnsureSchema("admin", "password")
	_, _ = st.ImportProxies("http://1.2.3.4:8080", "test", "http")
	items, _, _ := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	proxyID := items[0].ID
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := st.SaveProxyCheckBundleContext(ctx, proxyID, []CheckResult{{
		ProxyID: proxyID, Status: "available", Grade: "A", TargetProfile: "openai", BaseReachable: true, ServiceReachable: true, RecommendedUse: "web",
	}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancelled persistence, got %v", err)
	}
}

func TestExternalDatabaseMigrationChain(t *testing.T) {
	path := os.Getenv("PLC_TEST_MIGRATION_DB")
	if path == "" {
		t.Skip("PLC_TEST_MIGRATION_DB is not set")
	}
	st, err := openStore(path)
	if err != nil {
		t.Fatalf("open external migration database: %v", err)
	}
	defer st.db.Close()
	var before int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM proxies").Scan(&before); err != nil {
		t.Fatalf("count pre-migration proxies: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("migrate external database: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("repeat external migration: %v", err)
	}
	version, err := st.SchemaVersion()
	if err != nil || version != cloudflareTargetMigrationVersion {
		t.Fatalf("unexpected migrated schema version=%d err=%v", version, err)
	}
	var proxies, probes int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM proxies").Scan(&proxies); err != nil {
		t.Fatalf("count migrated proxies: %v", err)
	}
	if err := st.db.QueryRow("SELECT COUNT(*) FROM proxy_probe_state").Scan(&probes); err != nil {
		t.Fatalf("count migrated probes: %v", err)
	}
	if proxies != before || probes != proxies {
		t.Fatalf("migration lost identities or probes: before=%d proxies=%d probes=%d", before, proxies, probes)
	}
	var integrity string
	if err := st.db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity check failed: %q err=%v", integrity, err)
	}
	var foreignKeyErrors int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM pragma_foreign_key_check").Scan(&foreignKeyErrors); err != nil || foreignKeyErrors != 0 {
		t.Fatalf("foreign key check failed: count=%d err=%v", foreignKeyErrors, err)
	}
}

func TestExternalDatabasePerformance(t *testing.T) {
	path := os.Getenv("PLC_TEST_PERF_DB")
	if path == "" {
		t.Skip("PLC_TEST_PERF_DB is not set")
	}
	started := time.Now()
	st, err := openStore(path)
	if err != nil {
		t.Fatalf("open performance database: %v", err)
	}
	defer st.db.Close()
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure performance schema: %v", err)
	}
	var count int
	if err := st.db.QueryRow("SELECT COUNT(*) FROM proxies").Scan(&count); err != nil {
		t.Fatalf("count performance proxies: %v", err)
	}
	if count < 100000 {
		if _, err := st.db.Exec("PRAGMA synchronous = OFF"); err != nil {
			t.Fatalf("set performance pragma: %v", err)
		}
		tx, err := st.db.Begin()
		if err != nil {
			t.Fatalf("begin performance fixture: %v", err)
		}
		fixtureSQL := `
WITH digits(d) AS (VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9)),
numbers(n) AS (
  SELECT a.d + b.d*10 + c.d*100 + d.d*1000 + e.d*10000 + f.d*100000 + 1
  FROM digits a CROSS JOIN digits b CROSS JOIN digits c CROSS JOIN digits d CROSS JOIN digits e CROSS JOIN digits f
)
INSERT INTO proxies (
  proxy_key, ip, port, protocol, source, status, status_changed_at, enabled,
  created_at, updated_at, first_seen_at, last_seen_at
)
SELECT printf('http://perf-%06d.invalid:%d', n, 10000 + (n % 50000)),
       printf('198.18.%d.%d', (n / 256) % 256, n % 256),
       10000 + (n % 50000), 'http', 'perf', 'untested', datetime('now', '+8 hours'), 1,
       datetime('now', '+8 hours'), datetime('now', '+8 hours'), datetime('now', '+8 hours'), datetime('now', '+8 hours')
FROM numbers WHERE n <= 100000;

INSERT INTO proxy_probe_state (
  proxy_id, status, status_changed_at, detected_protocol, base_reachable, exit_ip,
  latency_ms, success_rate, country, updated_at, checked_at
)
SELECT id,
       CASE id % 3 WHEN 0 THEN 'available' WHEN 1 THEN 'failed' ELSE 'untested' END,
       datetime('now', '+8 hours'), 'http', CASE WHEN id % 3 = 0 THEN 1 ELSE 0 END,
       CASE WHEN id % 3 = 0 THEN printf('203.0.113.%d', id % 250 + 1) ELSE NULL END,
       20 + (id % 500), CASE WHEN id % 3 = 0 THEN 1 ELSE 0 END,
       CASE WHEN id % 2 = 0 THEN 'US' ELSE 'JP' END,
       datetime('now', '+8 hours'), datetime('now', '+8 hours')
FROM proxies;

WITH profiles(target_profile, ordinal) AS (
  VALUES ('generic', 1), ('openai', 2), ('grok', 3), ('gemini', 4), ('claude', 5)
)
INSERT INTO proxy_target_state (
  proxy_id, target_profile, status, status_changed_at, capability, service_reachable,
  latency_ms, success_rate, grade, recommended_use, checked_at, updated_at
)
SELECT p.id, profiles.target_profile,
       CASE (p.id + profiles.ordinal) % 3 WHEN 0 THEN 'available' WHEN 1 THEN 'failed' ELSE 'untested' END,
       datetime('now', '+8 hours'),
       CASE WHEN (p.id + profiles.ordinal) % 3 = 0 THEN 'web' ELSE 'none' END,
       CASE WHEN (p.id + profiles.ordinal) % 3 = 0 THEN 1 ELSE 0 END,
       30 + ((p.id + profiles.ordinal) % 700),
       CASE WHEN (p.id + profiles.ordinal) % 3 = 0 THEN 1 ELSE 0 END,
       CASE (p.id + profiles.ordinal) % 3 WHEN 0 THEN 'A' WHEN 1 THEN 'F' ELSE '' END,
       CASE WHEN (p.id + profiles.ordinal) % 3 = 0 THEN profiles.target_profile ELSE 'invalid' END,
       datetime('now', '+8 hours'), datetime('now', '+8 hours')
FROM proxies p CROSS JOIN profiles;`
		if _, err := tx.Exec(fixtureSQL); err != nil {
			_ = tx.Rollback()
			t.Fatalf("create 100k performance fixture: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit performance fixture: %v", err)
		}
		st.InvalidateStats()
		count = 100000
	}
	fixtureDuration := time.Since(started)
	t.Logf("performance fixture ready count=%d setup=%s", count, fixtureDuration)

	st.InvalidateStats()
	statsStarted := time.Now()
	stats, err := st.Stats()
	statsDuration := time.Since(statsStarted)
	if err != nil || anyToInt(stats["total"]) != count {
		t.Fatalf("100k stats failed: total=%v err=%v", stats["total"], err)
	}
	t.Logf("performance stats complete duration=%s", statsDuration)
	cachedStarted := time.Now()
	_, err = st.Stats()
	cachedDuration := time.Since(cachedStarted)
	if err != nil {
		t.Fatalf("cached stats failed: %v", err)
	}
	pageStarted := time.Now()
	_, _, err = st.ListProxies(proxyFilter{Status: "all", TargetProfile: "openai", Limit: 100, Offset: 50000})
	pageDuration := time.Since(pageStarted)
	if err != nil {
		t.Fatalf("100k page query failed: %v", err)
	}
	candidateStarted := time.Now()
	_, err = st.ListCheckCandidates("untested", 500, "openai")
	candidateDuration := time.Since(candidateStarted)
	if err != nil {
		t.Fatalf("100k candidate query failed: %v", err)
	}
	gatewayStarted := time.Now()
	_, err = st.GatewayUpstreamCandidates(availableProxyFilter{TargetProfile: "openai", Limit: 200})
	gatewayDuration := time.Since(gatewayStarted)
	if err != nil {
		t.Fatalf("100k gateway query failed: %v", err)
	}
	t.Logf("performance count=%d fixture=%s stats=%s cached_stats=%s page=%s candidates=%s gateway=%s", count, fixtureDuration, statsDuration, cachedDuration, pageDuration, candidateDuration, gatewayDuration)
	for name, duration := range map[string]time.Duration{
		"stats": statsDuration, "page": pageDuration, "candidates": candidateDuration, "gateway": gatewayDuration,
	} {
		if duration > 10*time.Second {
			t.Fatalf("%s query exceeded 10s acceptance: %s", name, duration)
		}
	}
}

func TestExternalExistingDatabasePerformance(t *testing.T) {
	path := os.Getenv("PLC_TEST_EXISTING_DB")
	if path == "" {
		t.Skip("PLC_TEST_EXISTING_DB is not set")
	}
	st, err := openStore(path)
	if err != nil {
		t.Fatalf("open existing performance database: %v", err)
	}
	defer st.db.Close()
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure existing performance schema: %v", err)
	}
	var count int
	_ = st.db.QueryRow("SELECT COUNT(*) FROM proxies").Scan(&count)
	st.InvalidateStats()
	statsStarted := time.Now()
	_, err = st.Stats()
	statsDuration := time.Since(statsStarted)
	if err != nil {
		t.Fatalf("existing stats: %v", err)
	}
	pageStarted := time.Now()
	_, _, err = st.ListProxies(proxyFilter{Status: "all", TargetProfile: "openai", Limit: 100, Offset: maxInt(0, count/2)})
	pageDuration := time.Since(pageStarted)
	if err != nil {
		t.Fatalf("existing page: %v", err)
	}
	candidateStarted := time.Now()
	_, err = st.ListCheckCandidates("untested", 500, "openai")
	candidateDuration := time.Since(candidateStarted)
	if err != nil {
		t.Fatalf("existing candidates: %v", err)
	}
	gatewayStarted := time.Now()
	_, err = st.GatewayUpstreamCandidates(availableProxyFilter{TargetProfile: "openai", Limit: 200})
	gatewayDuration := time.Since(gatewayStarted)
	if err != nil {
		t.Fatalf("existing gateway candidates: %v", err)
	}
	planStarted := time.Now()
	candidatesByProfile := map[string][]ProxyTask{}
	for _, profile := range targetProfileOrder {
		items, err := st.ListCheckCandidates("all", 100000, profile)
		if err != nil {
			t.Fatalf("existing %s candidate plan: %v", profile, err)
		}
		candidatesByProfile[profile] = items
	}
	plans := buildProxyFirstCheckPlans(targetProfileOrder, candidatesByProfile)
	planDuration := time.Since(planStarted)
	if len(plans) != count {
		t.Fatalf("proxy-first plan did not deduplicate real database: plans=%d proxies=%d", len(plans), count)
	}
	selectorStarted := time.Now()
	gateway := newGatewayServer(st, gatewayConfig{Host: "127.0.0.1", Port: 0, TargetProfiles: []string{"grok"}, UpstreamLimit: 200})
	endpoint := gateway.endpoints[0]
	if err := endpoint.selector.refresh(gateway, true); err != nil {
		t.Fatalf("load real gateway pool: %v", err)
	}
	for index := 0; index < 10000; index++ {
		if _, err := endpoint.selector.next(gateway, nil); err != nil {
			t.Fatalf("gateway in-memory pressure selection %d: %v", index, err)
		}
	}
	selectorDuration := time.Since(selectorStarted)
	t.Logf("existing performance count=%d stats=%s page=%s candidates=%s gateway=%s proxy_first_plan=%s gateway_10k_select=%s", count, statsDuration, pageDuration, candidateDuration, gatewayDuration, planDuration, selectorDuration)
}
