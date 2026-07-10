package main

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RY-zzcn/ProxyLiteChecker/internal/checkmeta"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func testHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestFailureReasonFormattingAndParsing(t *testing.T) {
	formatted := formatFailureError(classifyFailureReason("i/o timeout"), "i/o timeout")
	if formatted != "[timeout] i/o timeout" {
		t.Fatalf("unexpected formatted failure: %q", formatted)
	}
	if reason := failureReasonFromMessage(formatted); reason != "timeout" {
		t.Fatalf("expected timeout reason, got %q", reason)
	}
	if reason := failureReasonFromMessage("socks5 authentication failed"); reason != "proxy_auth" {
		t.Fatalf("expected proxy_auth reason, got %q", reason)
	}
}

func TestNamedTargetRequiresTargetReachability(t *testing.T) {
	apiUnavailable := false
	if status := targetAvailabilityStatus("grok", true, false, &apiUnavailable); status != "failed" {
		t.Fatalf("expected base-only Grok result to fail target availability, got %q", status)
	}
	if use := recommendUse("grok", true, false, &apiUnavailable, "failed"); use != "base" {
		t.Fatalf("expected base-only capability to be preserved, got %q", use)
	}
	apiAvailable := true
	if status := targetAvailabilityStatus("grok", false, false, &apiAvailable); status != "available" {
		t.Fatalf("expected reachable Grok API to be available, got %q", status)
	}
	if status := targetAvailabilityStatus("generic", true, false, nil); status != "available" {
		t.Fatalf("expected generic base reachability to remain available, got %q", status)
	}
}

func TestFailedProxyDeletionRequiresCompleteBaseFailure(t *testing.T) {
	if !shouldDeleteFailedProxy(&proxyCheckOutcome{Expected: 2, Seen: 2, AllFailed: true, Persisted: true}) {
		t.Fatalf("expected complete persisted base failure to be deletable")
	}
	for name, outcome := range map[string]*proxyCheckOutcome{
		"target still pending": {Expected: 2, Seen: 1, AllFailed: true, Persisted: true},
		"target available":     {Expected: 2, Seen: 2, AllFailed: false, Persisted: true},
		"base reachable":       {Expected: 2, Seen: 2, AllFailed: true, BaseReachable: true, Persisted: true},
		"persistence failed":   {Expected: 2, Seen: 2, AllFailed: true, Persisted: false},
	} {
		if shouldDeleteFailedProxy(outcome) {
			t.Fatalf("expected %s outcome not to be deletable", name)
		}
	}
}

func TestCheckFinalizerReportsPersistenceOutcome(t *testing.T) {
	srv := &server{jobs: newJobManager()}
	partialJob, _ := srv.jobs.Create("check", "check")
	srv.finalizeCheckJob(partialJob.ID, 1, 1, 1, 1, 0, false, map[string]any{"checked": 2})
	partial, _ := srv.jobs.Get(partialJob.ID)
	if partial.Status != jobStatusPartial {
		t.Fatalf("expected partial check status, got %#v", partial)
	}

	failedJob, _ := srv.jobs.Create("check", "check")
	srv.finalizeCheckJob(failedJob.ID, 0, 2, 0, 2, 0, false, map[string]any{"checked": 2})
	failed, _ := srv.jobs.Get(failedJob.ID)
	if failed.Status != jobStatusFailed || failed.Result["checked"] != 2 {
		t.Fatalf("expected failed check status with result, got %#v", failed)
	}
}

func TestBuildProxyFirstCheckPlansMergesCandidatesByProxy(t *testing.T) {
	first := ProxyTask{ID: 1, IP: "192.0.2.1", Port: 8080}
	second := ProxyTask{ID: 2, IP: "192.0.2.2", Port: 8080}
	plans := buildProxyFirstCheckPlans([]string{"openai", "grok"}, map[string][]ProxyTask{
		"openai": {first, second},
		"grok":   {first},
	})
	if len(plans) != 2 {
		t.Fatalf("expected two unique proxy plans, got %#v", plans)
	}
	if plans[0].Item.ID != 1 || strings.Join(plans[0].TargetProfiles, ",") != "openai,grok" {
		t.Fatalf("expected merged first proxy targets, got %#v", plans[0])
	}
	if plans[1].Item.ID != 2 || strings.Join(plans[1].TargetProfiles, ",") != "openai" {
		t.Fatalf("unexpected second proxy plan: %#v", plans[1])
	}
	if total := totalCheckPlanItems(plans); total != 3 {
		t.Fatalf("expected target-result total 3, got %d", total)
	}
}

func TestMultiTargetProgressReportsEverySelectedTarget(t *testing.T) {
	plans := []checkPlan{
		{Item: ProxyTask{ID: 1}, TargetProfiles: []string{"openai", "grok"}},
		{Item: ProxyTask{ID: 2}, TargetProfiles: []string{"openai"}},
	}
	profiles := []string{"openai", "grok"}
	progress := newTargetCheckProgress(plans, profiles)
	updateTargetCheckProgress(progress, CheckResult{TargetProfile: "openai", Status: "available"})
	updateTargetCheckProgress(progress, CheckResult{TargetProfile: "grok", Status: "failed"})
	limiter := newCheckConcurrencyController(100)
	if !limiter.Acquire(context.Background()) {
		t.Fatal("acquire concurrency slot")
	}
	message := checkProgressMessage(1, 2, 2, 3, profiles, progress, limiter, 100)
	limiter.Release()
	for _, expected := range []string{"代理 1/2", "目标项 2/3", "OpenAI 1/2", "Grok 1/1", "并发 1/100"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message %q does not contain %q", message, expected)
		}
	}
	result := checkProgressResult(1, 2, 2, 3, profiles, progress, limiter, 100)
	if result["execution_mode"] != "proxy_parallel_target_ordered" || anyToInt(result["proxy_total"]) != 2 {
		t.Fatalf("unexpected progress result: %#v", result)
	}
}

func TestProxyFirstCheckRunsBaseProbeOncePerRound(t *testing.T) {
	originalClientFactory := checkProxyHTTPClient
	originalExitTargets := checkExitIPTargets
	originalEnrichIP := checkEnrichIP
	originalProfiles := targetProfiles
	t.Cleanup(func() {
		checkProxyHTTPClient = originalClientFactory
		checkExitIPTargets = originalExitTargets
		checkEnrichIP = originalEnrichIP
		targetProfiles = originalProfiles
	})

	targetProfiles = map[string]TargetProfile{}
	for _, profile := range targetProfileOrder {
		targetProfiles[profile] = TargetProfile{
			ServiceURL: "https://" + profile + ".test/web",
			APIURL:     "https://" + profile + ".test/api",
		}
	}
	var clientCount atomic.Int32
	var exitCount atomic.Int32
	var targetCount atomic.Int32
	var geoCount atomic.Int32
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "exit.test" {
			exitCount.Add(1)
			return testHTTPResponse(http.StatusOK, `{"ip":"203.0.113.7"}`), nil
		}
		targetCount.Add(1)
		return testHTTPResponse(http.StatusOK, `{}`), nil
	})
	checkProxyHTTPClient = func(string, int) (*http.Client, string, error) {
		clientCount.Add(1)
		return &http.Client{Transport: transport}, "http", nil
	}
	checkExitIPTargets = func() []string { return []string{"https://exit.test/ip"} }
	checkEnrichIP = func(context.Context, *http.Client, string, string) checkmeta.Metadata {
		geoCount.Add(1)
		return checkmeta.Metadata{Country: "US", GeoSource: "test", GeoUpdatedAt: time.Now()}
	}

	plan := checkPlan{
		Item:           ProxyTask{ID: 7, IP: "192.0.2.7", Port: 8080, Protocol: "http"},
		TargetProfiles: append([]string{}, targetProfileOrder...),
	}
	bundle := checkProxyPlan(context.Background(), plan, CheckConfig{Rounds: 2, RequestTimeout: 1, HardTimeout: 5}, newCheckGeoCache())
	if len(bundle.Results) != len(targetProfileOrder) {
		t.Fatalf("expected one result per target, got %#v", bundle.Results)
	}
	if got := clientCount.Load(); got != 2 {
		t.Fatalf("expected one protocol client per round, got %d", got)
	}
	if got := exitCount.Load(); got != 2 {
		t.Fatalf("expected one logical exit probe per round, got %d", got)
	}
	if got := geoCount.Load(); got != 1 {
		t.Fatalf("expected unique exit IP metadata lookup once, got %d", got)
	}
	if got := targetCount.Load(); got != int32(len(targetProfileOrder)*2*2) {
		t.Fatalf("expected service/API probes once per target and round, got %d", got)
	}
	for _, result := range bundle.Results {
		if result.Status != "available" || result.ExitIP == nil || *result.ExitIP != "203.0.113.7" || result.Country == nil || *result.Country != "US" {
			t.Fatalf("unexpected proxy-first target result: %#v", result)
		}
	}
}

func TestExitIPTargetsAreFallbacksAndStopAfterSuccess(t *testing.T) {
	originalExitTargets := checkExitIPTargets
	t.Cleanup(func() { checkExitIPTargets = originalExitTargets })
	checkExitIPTargets = func() []string {
		return []string{"https://exit.test/first", "https://exit.test/second", "https://exit.test/third"}
	}
	counts := map[string]int{}
	var mu sync.Mutex
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		counts[req.URL.Path]++
		mu.Unlock()
		if req.URL.Path == "/second" {
			return testHTTPResponse(http.StatusOK, `{"ip":"198.51.100.4"}`), nil
		}
		return testHTTPResponse(http.StatusBadGateway, ""), nil
	})}
	exitIP, _ := probeBaseWithClient(context.Background(), client)
	if exitIP == nil || *exitIP != "198.51.100.4" {
		t.Fatalf("expected second fallback endpoint to succeed, got %#v", exitIP)
	}
	if counts["/first"] != 1 || counts["/second"] != 1 || counts["/third"] != 0 {
		t.Fatalf("expected fallback to stop after success, counts=%#v", counts)
	}
}

func TestTargetsWithinOneProxyRunSequentially(t *testing.T) {
	originalProfiles := targetProfiles
	t.Cleanup(func() { targetProfiles = originalProfiles })
	targetProfiles = map[string]TargetProfile{}
	profiles := []string{"generic", "openai", "grok", "gemini", "claude"}
	for _, profile := range profiles {
		targetProfiles[profile] = TargetProfile{ServiceURL: "https://" + profile + ".test/web"}
	}
	var active atomic.Int32
	var maximum atomic.Int32
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		current := active.Add(1)
		for {
			seen := maximum.Load()
			if current <= seen || maximum.CompareAndSwap(seen, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		active.Add(-1)
		return testHTTPResponse(http.StatusOK, ""), nil
	})}
	results := probeTargetsWithClient(context.Background(), client, profiles)
	if len(results) != len(profiles) {
		t.Fatalf("expected all target results, got %#v", results)
	}
	if got := maximum.Load(); got != 1 {
		t.Fatalf("expected one in-flight target request per proxy, got %d", got)
	}
}
