package main

import "testing"

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
		ProxyID:        items[0].ID,
		Status:         "available",
		Grade:          "A",
		SuccessRate:    1,
		TargetProfile:  "openai",
		RecommendedUse: "openai",
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
			ProxyID:        item.ID,
			Status:         "available",
			Grade:          "A",
			Country:        stringPtr(country),
			CountryName:    stringPtr(map[string]string{"US": "United States", "JP": "Japan"}[country]),
			GeoSource:      stringPtr("mmdb"),
			SuccessRate:    1,
			TargetProfile:  "openai",
			RecommendedUse: "openai",
		}); err != nil {
			t.Fatalf("save %s result: %v", item.IP, err)
		}
	}
	for _, item := range items {
		if item.IP != "1.1.1.1" {
			continue
		}
		if err := st.SaveCheckResult(CheckResult{
			ProxyID:        item.ID,
			Status:         "available",
			Grade:          "A",
			Country:        stringPtr("US"),
			SuccessRate:    1,
			TargetProfile:  "grok",
			RecommendedUse: "grok",
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
    updated_at = datetime('now', '+8 hours', '-4 hours')
WHERE proxy_key = 'http://1.2.3.4:8080'`); err != nil {
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
