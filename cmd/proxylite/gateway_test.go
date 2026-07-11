package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func gatewayTestPool(urls ...string) []gatewayUpstream {
	items := make([]gatewayUpstream, 0, len(urls))
	for _, value := range urls {
		items = append(items, gatewayUpstream{URL: value, SuccessRate: 1})
	}
	return items
}

func TestGatewaySelectUpstreamRoundRobinByTarget(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.1.1.1:8080\nhttp://2.2.2.2:8080", "test", "auto"); err != nil {
		t.Fatalf("import proxies: %v", err)
	}
	items, _, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil {
		t.Fatalf("list proxies: %v", err)
	}
	for _, item := range items {
		latency := 20
		if item.ProxyKey == "http://1.1.1.1:8080" {
			latency = 10
		}
		if err := st.SaveCheckResult(CheckResult{
			ProxyID:          item.ID,
			Status:           "available",
			Grade:            "A",
			LatencyMS:        &latency,
			SuccessRate:      1,
			TargetProfile:    "openai",
			ServiceReachable: true,
			RecommendedUse:   "openai",
		}); err != nil {
			t.Fatalf("save check result: %v", err)
		}
	}
	gateway := newGatewayServer(st, gatewayConfig{
		Host:           "127.0.0.1",
		Port:           0,
		TargetProfiles: []string{"openai"},
		UpstreamLimit:  10,
	})
	if len(gateway.endpoints) != 1 {
		t.Fatalf("expected one gateway endpoint, got %d", len(gateway.endpoints))
	}
	first, err := gateway.selectUpstream(gateway.endpoints[0])
	if err != nil {
		t.Fatalf("select first upstream: %v", err)
	}
	second, err := gateway.selectUpstream(gateway.endpoints[0])
	if err != nil {
		t.Fatalf("select second upstream: %v", err)
	}
	third, err := gateway.selectUpstream(gateway.endpoints[0])
	if err != nil {
		t.Fatalf("select third upstream: %v", err)
	}
	if first != "http://1.1.1.1:8080" || second != "http://2.2.2.2:8080" || third != first {
		t.Fatalf("unexpected round-robin order: %q %q %q", first, second, third)
	}
}

func TestGatewaySelectUpstreamUsesLoadedPoolUntilRefresh(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.1.1.1:8080\nhttp://2.2.2.2:8080", "test", "auto"); err != nil {
		t.Fatalf("import proxies: %v", err)
	}
	items, _, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil {
		t.Fatalf("list proxies: %v", err)
	}
	for _, item := range items {
		latency := 20
		if item.ProxyKey == "http://1.1.1.1:8080" {
			latency = 10
		}
		if err := st.SaveCheckResult(CheckResult{
			ProxyID:          item.ID,
			Status:           "available",
			Grade:            "A",
			LatencyMS:        &latency,
			SuccessRate:      1,
			TargetProfile:    "openai",
			ServiceReachable: true,
			RecommendedUse:   "openai",
		}); err != nil {
			t.Fatalf("save check result: %v", err)
		}
	}
	gateway := newGatewayServer(st, gatewayConfig{
		Host:           "127.0.0.1",
		Port:           0,
		TargetProfiles: []string{"openai"},
		UpstreamLimit:  10,
	})
	endpoint := gateway.endpoints[0]
	first, err := gateway.selectUpstream(endpoint)
	if err != nil {
		t.Fatalf("select first upstream: %v", err)
	}
	if first != "http://1.1.1.1:8080" {
		t.Fatalf("expected first loaded upstream, got %q", first)
	}

	if _, err := st.ImportProxies("http://3.3.3.3:8080", "test", "auto"); err != nil {
		t.Fatalf("import third proxy: %v", err)
	}
	items, _, err = st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil {
		t.Fatalf("list proxies after import: %v", err)
	}
	foundThird := false
	for _, item := range items {
		if item.ProxyKey != "http://3.3.3.3:8080" {
			continue
		}
		foundThird = true
		latency := 5
		if err := st.SaveCheckResult(CheckResult{
			ProxyID:          item.ID,
			Status:           "available",
			Grade:            "A",
			LatencyMS:        &latency,
			SuccessRate:      1,
			TargetProfile:    "openai",
			ServiceReachable: true,
			RecommendedUse:   "openai",
		}); err != nil {
			t.Fatalf("save third check result: %v", err)
		}
	}
	if !foundThird {
		t.Fatalf("imported third proxy was not found")
	}

	second, err := gateway.selectUpstream(endpoint)
	if err != nil {
		t.Fatalf("select second upstream: %v", err)
	}
	if second != "http://2.2.2.2:8080" {
		t.Fatalf("expected existing loaded pool to advance, got %q", second)
	}

	endpoint.selector.mu.Lock()
	endpoint.selector.upstreamsLoadedAt = time.Now().Add(-gatewayUpstreamRefreshInterval - time.Second)
	endpoint.selector.index = 0
	previousGeneration := endpoint.selector.poolGeneration
	endpoint.selector.mu.Unlock()
	whileRefreshing, err := gateway.selectUpstream(endpoint)
	if err != nil {
		t.Fatalf("select while refreshing upstreams: %v", err)
	}
	if whileRefreshing != "http://1.1.1.1:8080" {
		t.Fatalf("expected old pool to remain selectable during refresh, got %q", whileRefreshing)
	}
	waitForCondition(t, time.Second, func() bool {
		endpoint.selector.mu.Lock()
		defer endpoint.selector.mu.Unlock()
		return endpoint.selector.poolGeneration > previousGeneration
	})
	endpoint.selector.mu.Lock()
	endpoint.selector.index = 0
	endpoint.selector.mu.Unlock()
	refreshed, err := gateway.selectUpstream(endpoint)
	if err != nil {
		t.Fatalf("select refreshed upstream: %v", err)
	}
	if refreshed != "http://3.3.3.3:8080" {
		t.Fatalf("expected refreshed pool to include lowest-latency upstream first, got %q", refreshed)
	}
}

func TestGatewaySelectorSkipsIsolatedUpstream(t *testing.T) {
	gateway := newGatewayServer(nil, gatewayConfig{
		Host:             "127.0.0.1",
		Port:             0,
		TargetProfiles:   []string{"openai"},
		FailureThreshold: 2,
		FailureCooldownS: 60,
	})
	endpoint := gateway.endpoints[0]
	endpoint.selector.mu.Lock()
	endpoint.selector.upstreams = gatewayTestPool("http://1.1.1.1:8080", "http://2.2.2.2:8080")
	endpoint.selector.upstreamsLoadedAt = time.Now()
	endpoint.selector.mu.Unlock()

	first, err := gateway.selectUpstream(endpoint)
	if err != nil {
		t.Fatalf("select first upstream: %v", err)
	}
	endpoint.recordUpstreamFailure(first, errors.New("dial timeout"))
	endpoint.recordUpstreamFailure(first, errors.New("dial timeout"))

	second, err := gateway.selectUpstream(endpoint)
	if err != nil {
		t.Fatalf("select second upstream: %v", err)
	}
	if second == first {
		t.Fatalf("expected isolated upstream to be skipped, selected %q again", second)
	}
	snapshot := gateway.endpointSelectorSnapshot(endpoint)
	if snapshot.Active != 1 || snapshot.Skipped != 1 {
		t.Fatalf("expected one active and one skipped upstream, got %#v", snapshot)
	}
}

func TestGatewayImmediateFailureOpensCircuit(t *testing.T) {
	gateway := newGatewayServer(nil, gatewayConfig{
		Host:             "127.0.0.1",
		Port:             0,
		TargetProfiles:   []string{"openai"},
		FailureThreshold: 3,
		FailureCooldownS: 60,
	})
	endpoint := gateway.endpoints[0]
	upstream := "http://1.1.1.1:8080"
	endpoint.selector.mu.Lock()
	endpoint.selector.upstreams = gatewayTestPool(upstream)
	endpoint.selector.upstreamsLoadedAt = time.Now()
	endpoint.selector.mu.Unlock()
	endpoint.selector.reportImmediateFailure(upstream, errors.New("Cloudflare blocked"))
	endpoint.selector.mu.Lock()
	state := endpoint.selector.failures[upstream]
	endpoint.selector.mu.Unlock()
	if state.Count < 3 || state.IsolatedUntil.Before(time.Now()) {
		t.Fatalf("immediate failure did not open circuit: %#v", state)
	}
}

func TestGatewayHTTPResponseClassification(t *testing.T) {
	headers := make(http.Header)
	headers.Set("CF-Ray", "test")
	for _, tc := range []struct {
		name      string
		status    int
		headers   http.Header
		body      string
		failed    bool
		immediate bool
	}{
		{name: "challenge 200", status: 200, headers: headers, body: "checking your browser", failed: true, immediate: true},
		{name: "cloudflare 403", status: 403, headers: headers, body: "denied", failed: true, immediate: true},
		{name: "proxy auth", status: 407, failed: true, immediate: true},
		{name: "provider rate limit", status: 429, failed: false},
		{name: "ordinary forbidden", status: 403, failed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome := classifyGatewayHTTPResponse(tc.status, tc.headers, tc.body)
			if outcome.Failed != tc.failed || outcome.Immediate != tc.immediate {
				t.Fatalf("unexpected outcome: %#v", outcome)
			}
		})
	}
}

func TestGatewayHTTPCloudflareBlockRetriesNextUpstream(t *testing.T) {
	gateway := newGatewayServer(nil, gatewayConfig{
		Host:             "127.0.0.1",
		Port:             0,
		TargetProfiles:   []string{"openai"},
		RetryAttempts:    2,
		FailureThreshold: 3,
		FailureCooldownS: 60,
	})
	endpoint := gateway.endpoints[0]
	first := "http://1.1.1.1:8080"
	second := "http://2.2.2.2:8080"
	endpoint.selector.mu.Lock()
	endpoint.selector.upstreams = gatewayTestPool(first, second)
	endpoint.selector.upstreamsLoadedAt = time.Now()
	endpoint.selector.mu.Unlock()
	var calls atomic.Int32
	gateway.newProxyClient = func(upstream string, _ int) (*http.Client, string, error) {
		return &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			if upstream == first {
				response := testHTTPResponse(http.StatusForbidden, "denied")
				response.Header.Set("CF-Ray", "test")
				return response, nil
			}
			return testHTTPResponse(http.StatusOK, "ok"), nil
		})}, "http", nil
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/test", nil)
	recorder := httptest.NewRecorder()
	gateway.handleForward(recorder, req, endpoint)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok" || calls.Load() != 2 {
		t.Fatalf("expected retry through second upstream, code=%d body=%q calls=%d", recorder.Code, recorder.Body.String(), calls.Load())
	}
	endpoint.selector.mu.Lock()
	state := endpoint.selector.failures[first]
	endpoint.selector.mu.Unlock()
	if state.Count < 3 || state.IsolatedUntil.Before(time.Now()) {
		t.Fatalf("Cloudflare-blocked upstream was not immediately isolated: %#v", state)
	}
}

func TestGatewayBodyInspectionSkipsStreamingResponses(t *testing.T) {
	stream := &http.Response{Header: make(http.Header)}
	stream.Header.Set("CF-Ray", "test")
	stream.Header.Set("Content-Type", "text/event-stream")
	if shouldInspectGatewayResponseBody(stream) {
		t.Fatal("streaming response body must not be prefetched")
	}
	html := &http.Response{Header: make(http.Header)}
	html.Header.Set("Server", "cloudflare")
	html.Header.Set("Content-Type", "text/html; charset=utf-8")
	if !shouldInspectGatewayResponseBody(html) {
		t.Fatal("Cloudflare HTML response should be inspected for challenge markers")
	}
}

func TestGatewayTunnelOutcomeClassification(t *testing.T) {
	if outcome := classifyGatewayTunnelResult(gatewayTunnelResult{Duration: time.Second, ClientToUpstreamBytes: 128}); outcome != gatewayTunnelFailure {
		t.Fatalf("expected early zero-response tunnel failure, got %q", outcome)
	}
	if outcome := classifyGatewayTunnelResult(gatewayTunnelResult{Duration: time.Second, ClientToUpstreamBytes: 128, UpstreamToClientBytes: 64}); outcome != gatewayTunnelSuccess {
		t.Fatalf("expected upstream-response tunnel success, got %q", outcome)
	}
	if outcome := classifyGatewayTunnelResult(gatewayTunnelResult{Duration: time.Second}); outcome != gatewayTunnelNeutral {
		t.Fatalf("client closed without traffic must be neutral, got %q", outcome)
	}
	if outcome := classifyGatewayTunnelResult(gatewayTunnelResult{Duration: 10 * time.Second, ClientToUpstreamBytes: 128}); outcome != gatewayTunnelSuccess {
		t.Fatalf("long-lived upload-only tunnel must not be treated as early failure, got %q", outcome)
	}
}

func TestGatewayNeutralTunnelReleasesHalfOpenSlot(t *testing.T) {
	gateway := newGatewayServer(nil, gatewayConfig{Host: "127.0.0.1", Port: 0, TargetProfiles: []string{"openai"}, FailureThreshold: 1, FailureCooldownS: 60})
	endpoint := gateway.endpoints[0]
	upstream := "http://1.1.1.1:8080"
	endpoint.selector.mu.Lock()
	endpoint.selector.failures[upstream] = gatewayUpstreamFailure{Count: 1, HalfOpenInFlight: 1}
	endpoint.selector.mu.Unlock()
	gateway.recordTunnelResult(endpoint, "http_connect", "127.0.0.1", upstream, time.Millisecond, gatewayTunnelResult{Duration: time.Millisecond})
	endpoint.selector.mu.Lock()
	state := endpoint.selector.failures[upstream]
	endpoint.selector.mu.Unlock()
	if state.HalfOpenInFlight != 0 || state.Count != 1 {
		t.Fatalf("neutral tunnel did not release half-open slot without changing failures: %#v", state)
	}
}

func TestPipeBidirectionalReportsTraffic(t *testing.T) {
	clientApp, gatewayClient := net.Pipe()
	gatewayUpstream, upstreamApp := net.Pipe()
	done := make(chan gatewayTunnelResult, 1)
	pipeBidirectional(gatewayClient, gatewayUpstream, func(result gatewayTunnelResult) {
		done <- result
	})

	go func() {
		_, _ = clientApp.Write([]byte("hello"))
	}()
	request := make([]byte, 5)
	if _, err := io.ReadFull(upstreamApp, request); err != nil || string(request) != "hello" {
		t.Fatalf("upstream request=%q err=%v", request, err)
	}
	go func() {
		_, _ = upstreamApp.Write([]byte("world"))
	}()
	response := make([]byte, 5)
	if _, err := io.ReadFull(clientApp, response); err != nil || string(response) != "world" {
		t.Fatalf("client response=%q err=%v", response, err)
	}
	_ = clientApp.Close()
	_ = upstreamApp.Close()
	select {
	case result := <-done:
		if result.ClientToUpstreamBytes < 5 || result.UpstreamToClientBytes < 5 || result.Duration <= 0 {
			t.Fatalf("unexpected tunnel result: %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tunnel result")
	}
}

func TestGatewaySelectorReleasesWhenAllUpstreamsIsolated(t *testing.T) {
	gateway := newGatewayServer(nil, gatewayConfig{
		Host:             "127.0.0.1",
		Port:             0,
		TargetProfiles:   []string{"openai"},
		FailureThreshold: 1,
		FailureCooldownS: 60,
	})
	endpoint := gateway.endpoints[0]
	endpoint.selector.mu.Lock()
	endpoint.selector.upstreams = gatewayTestPool("http://1.1.1.1:8080", "http://2.2.2.2:8080")
	endpoint.selector.upstreamsLoadedAt = time.Now()
	endpoint.selector.mu.Unlock()
	endpoint.recordUpstreamFailure("http://1.1.1.1:8080", errors.New("dial timeout"))
	endpoint.recordUpstreamFailure("http://2.2.2.2:8080", errors.New("dial timeout"))

	selected, err := gateway.selectUpstream(endpoint)
	if err != nil {
		t.Fatalf("select after all isolated: %v", err)
	}
	if selected == "" {
		t.Fatalf("expected an upstream after releasing isolation window")
	}
	snapshot := gateway.endpointSelectorSnapshot(endpoint)
	if !snapshot.Degraded || snapshot.Open != 2 || snapshot.LastAllReleasedAt == "" {
		t.Fatalf("expected explicit degraded selection without clearing isolation, got %#v", snapshot)
	}
}

func TestGatewaySelectorStrategySwitchAppliesImmediately(t *testing.T) {
	gateway := newGatewayServer(nil, gatewayConfig{
		Host:             "127.0.0.1",
		Port:             0,
		TargetProfiles:   []string{"openai"},
		FailureThreshold: 3,
		FailureCooldownS: 60,
		UpstreamStrategy: gatewayStrategyRoundRobin,
	})
	endpoint := gateway.endpoints[0]
	endpoint.selector.mu.Lock()
	endpoint.selector.upstreams = gatewayTestPool("http://1.1.1.1:8080", "http://2.2.2.2:8080")
	endpoint.selector.upstreamsLoadedAt = time.Now()
	endpoint.selector.index = 0
	endpoint.selector.mu.Unlock()
	endpoint.recordUpstreamFailure("http://1.1.1.1:8080", errors.New("temporary failure"))

	gateway.cfg.UpstreamStrategy = gatewayStrategyStabilityFirst
	selected, err := gateway.selectUpstream(endpoint)
	if err != nil {
		t.Fatalf("select stability-first upstream: %v", err)
	}
	if selected != "http://2.2.2.2:8080" {
		t.Fatalf("expected stability-first strategy to prefer upstream with fewer failures, got %q", selected)
	}
	snapshot := gateway.endpointSelectorSnapshot(endpoint)
	if snapshot.Strategy != gatewayStrategyStabilityFirst {
		t.Fatalf("expected strategy switch in snapshot, got %#v", snapshot)
	}
}

func TestGatewayOpenTunnelRetriesNextUpstream(t *testing.T) {
	gateway := newGatewayServer(nil, gatewayConfig{
		Host:             "127.0.0.1",
		Port:             0,
		TargetProfiles:   []string{"openai"},
		RetryAttempts:    2,
		FailureThreshold: 1,
		FailureCooldownS: 60,
	})
	endpoint := gateway.endpoints[0]
	endpoint.selector.mu.Lock()
	endpoint.selector.upstreams = gatewayTestPool("http://1.1.1.1:8080", "http://2.2.2.2:8080")
	endpoint.selector.upstreamsLoadedAt = time.Now()
	endpoint.selector.mu.Unlock()
	calls := []string{}
	gateway.dialProxy = func(ctx context.Context, proxyURL string, target string, timeout time.Duration) (net.Conn, error) {
		calls = append(calls, proxyURL)
		if proxyURL == "http://1.1.1.1:8080" {
			return nil, errors.New("dial timeout")
		}
		left, right := net.Pipe()
		_ = right.Close()
		return left, nil
	}

	conn, upstream, err := gateway.openTunnelWithRetry(context.Background(), endpoint, "example.com:443")
	if err != nil {
		t.Fatalf("open tunnel with retry: %v", err)
	}
	_ = conn.Close()
	if upstream != "http://2.2.2.2:8080" {
		t.Fatalf("expected retry to use second upstream, got %q", upstream)
	}
	if len(calls) != 2 || calls[0] != "http://1.1.1.1:8080" || calls[1] != "http://2.2.2.2:8080" {
		t.Fatalf("unexpected dial calls: %#v", calls)
	}
	snapshot := gateway.endpointSelectorSnapshot(endpoint)
	if snapshot.Active != 1 || snapshot.Skipped != 1 {
		t.Fatalf("expected failed upstream to be isolated, got %#v", snapshot)
	}
}

func TestGatewayOpenTunnelDoesNotRepeatTriedUpstreams(t *testing.T) {
	gateway := newGatewayServer(nil, gatewayConfig{
		Host:             "127.0.0.1",
		Port:             0,
		TargetProfiles:   []string{"openai"},
		RetryAttempts:    3,
		FailureThreshold: 10,
		FailureCooldownS: 60,
	})
	endpoint := gateway.endpoints[0]
	endpoint.selector.mu.Lock()
	endpoint.selector.upstreams = gatewayTestPool("http://1.1.1.1:8080", "http://2.2.2.2:8080")
	endpoint.selector.upstreamsLoadedAt = time.Now()
	endpoint.selector.mu.Unlock()
	calls := []string{}
	gateway.dialProxy = func(ctx context.Context, proxyURL string, target string, timeout time.Duration) (net.Conn, error) {
		calls = append(calls, proxyURL)
		return nil, errors.New("dial timeout")
	}

	conn, upstream, err := gateway.openTunnelWithRetry(context.Background(), endpoint, "example.com:443")
	if err == nil {
		_ = conn.Close()
		t.Fatalf("expected retry exhaustion error")
	}
	if upstream != "http://2.2.2.2:8080" {
		t.Fatalf("expected last attempted upstream to be second upstream, got %q", upstream)
	}
	if len(calls) != 2 || calls[0] != "http://1.1.1.1:8080" || calls[1] != "http://2.2.2.2:8080" {
		t.Fatalf("expected each upstream to be tried once, got %#v", calls)
	}
}

func TestGatewayStatusReportsLoadedAndAvailableUpstreams(t *testing.T) {
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
	for _, item := range items {
		if err := st.SaveCheckResult(CheckResult{
			ProxyID:          item.ID,
			Status:           "available",
			Grade:            "A",
			SuccessRate:      1,
			TargetProfile:    "openai",
			ServiceReachable: true,
			RecommendedUse:   "openai",
		}); err != nil {
			t.Fatalf("save check result: %v", err)
		}
	}
	gateway := newGatewayServer(st, gatewayConfig{
		Host:           "127.0.0.1",
		Port:           0,
		TargetProfiles: []string{"openai"},
		UpstreamLimit:  2,
	})
	status := gateway.endpointStatus(gateway.endpoints[0])
	if status["upstreams"] != 2 || status["loaded_upstreams"] != 2 {
		t.Fatalf("expected two loaded upstreams, got %#v", status)
	}
	if status["available_upstreams"] != 3 {
		t.Fatalf("expected three available upstreams, got %#v", status)
	}
	if status["upstream_limited"] != true {
		t.Fatalf("expected upstream_limited, got %#v", status)
	}
}

func TestGatewaySelectorFiltersUpstreamsByCountry(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.1.1.1:8080\nhttp://2.2.2.2:8080", "test", "auto"); err != nil {
		t.Fatalf("import proxies: %v", err)
	}
	items, _, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil {
		t.Fatalf("list proxies: %v", err)
	}
	for _, item := range items {
		country := "US"
		if item.IP == "2.2.2.2" {
			country = "JP"
		}
		if err := st.SaveCheckResult(CheckResult{
			ProxyID:          item.ID,
			Status:           "available",
			Grade:            "A",
			Country:          stringPtr(country),
			SuccessRate:      1,
			TargetProfile:    "openai",
			ServiceReachable: true,
			RecommendedUse:   "openai",
		}); err != nil {
			t.Fatalf("save check result: %v", err)
		}
	}
	gateway := newGatewayServer(st, gatewayConfig{
		Host:           "127.0.0.1",
		Port:           0,
		TargetProfiles: []string{"openai"},
		UpstreamLimit:  10,
		Countries:      []string{"JP"},
		CountryPolicy:  gatewayCountryPolicyStrict,
	})
	selected, err := gateway.selectUpstream(gateway.endpoints[0])
	if err != nil {
		t.Fatalf("select country-filtered upstream: %v", err)
	}
	if selected != "http://2.2.2.2:8080" {
		t.Fatalf("expected JP upstream, got %q", selected)
	}
	status := gateway.endpointStatus(gateway.endpoints[0])
	if status["available_upstreams"] != 1 || status["country_limited"] != true {
		t.Fatalf("expected country-limited status with one upstream, got %#v", status)
	}
}

func TestGatewayStatusReportsUniqueAvailableAcrossTargets(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.1.1.1:8080\nhttp://2.2.2.2:8080", "test", "auto"); err != nil {
		t.Fatalf("import proxies: %v", err)
	}
	items, _, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil {
		t.Fatalf("list proxies: %v", err)
	}
	for _, item := range items {
		for _, profile := range []string{"openai", "grok"} {
			if err := st.SaveCheckResult(CheckResult{
				ProxyID:          item.ID,
				Status:           "available",
				Grade:            "A",
				SuccessRate:      1,
				TargetProfile:    profile,
				ServiceReachable: true,
				RecommendedUse:   profile,
			}); err != nil {
				t.Fatalf("save %s result: %v", profile, err)
			}
		}
	}
	gateway := newGatewayServer(st, gatewayConfig{
		Host:           "127.0.0.1",
		Port:           0,
		TargetProfiles: []string{"openai", "grok"},
		UpstreamLimit:  1,
	})
	status := gateway.Status()
	if status["loaded_upstreams"] != 2 {
		t.Fatalf("expected two loaded target slots, got %#v", status)
	}
	if status["target_available_upstreams"] != 4 {
		t.Fatalf("expected four target-available upstreams, got %#v", status)
	}
	if status["unique_available_upstreams"] != 2 || status["available_upstreams"] != 2 {
		t.Fatalf("expected two unique available upstreams, got %#v", status)
	}
}

func TestGatewayStatusShortCacheAndGeneration(t *testing.T) {
	st, _ := openStore(":memory:")
	_ = st.EnsureSchema("admin", "password")
	gateway := newGatewayServer(st, gatewayConfig{Host: "127.0.0.1", Port: 0, TargetProfiles: []string{"openai"}, UpstreamLimit: 10})
	first := gateway.Status()
	if first["generated_at"] == "" || anyToInt(first["total_connections"]) != 0 {
		t.Fatalf("unexpected initial gateway status: %#v", first)
	}
	atomic.AddInt64(&gateway.endpoints[0].totalConnections, 1)
	second := gateway.Status()
	if anyToInt(second["total_connections"]) != 0 || second["generated_at"] != first["generated_at"] {
		t.Fatalf("expected short cached gateway status: %#v", second)
	}
	gateway.invalidateStatus()
	third := gateway.Status()
	if anyToInt(third["total_connections"]) != 1 {
		t.Fatalf("gateway generation did not invalidate cache: %#v", third)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080", "test", "http"); err != nil {
		t.Fatalf("import proxy: %v", err)
	}
	items, _, _ := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err := st.SaveCheckResult(CheckResult{ProxyID: items[0].ID, Status: "available", Grade: "A", TargetProfile: "openai", ServiceReachable: true, RecommendedUse: "web"}); err != nil {
		t.Fatalf("save target: %v", err)
	}
	fourth := gateway.Status()
	if anyToInt(fourth["available_upstreams"]) != 1 {
		t.Fatalf("store generation did not invalidate gateway cache: %#v", fourth)
	}
}

func TestGatewaySlowRefreshDoesNotBlockExistingPool(t *testing.T) {
	gateway := newGatewayServer(nil, gatewayConfig{Host: "127.0.0.1", Port: 0, TargetProfiles: []string{"openai"}, UpstreamLimit: 10})
	endpoint := gateway.endpoints[0]
	endpoint.selector.mu.Lock()
	endpoint.selector.upstreams = gatewayTestPool("http://1.1.1.1:8080")
	endpoint.selector.upstreamsLoadedAt = time.Now().Add(-gatewayUpstreamRefreshInterval - time.Second)
	endpoint.selector.poolGeneration = 1
	endpoint.selector.mu.Unlock()
	started := make(chan struct{})
	release := make(chan struct{})
	gateway.loadUpstreams = func(availableProxyFilter) ([]gatewayUpstream, error) {
		close(started)
		<-release
		return gatewayTestPool("http://2.2.2.2:8080"), nil
	}
	before := time.Now()
	selected, err := gateway.selectUpstream(endpoint)
	if err != nil || selected != "http://1.1.1.1:8080" {
		t.Fatalf("expected old pool during refresh, selected=%q err=%v", selected, err)
	}
	if elapsed := time.Since(before); elapsed > 100*time.Millisecond {
		t.Fatalf("slow refresh blocked old-pool selection: %s", elapsed)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatalf("background refresh did not start")
	}
	close(release)
	waitForCondition(t, time.Second, func() bool {
		endpoint.selector.mu.Lock()
		defer endpoint.selector.mu.Unlock()
		return endpoint.selector.poolGeneration == 2
	})
	selected, err = gateway.selectUpstream(endpoint)
	if err != nil || selected != "http://2.2.2.2:8080" {
		t.Fatalf("refreshed pool not installed atomically, selected=%q err=%v", selected, err)
	}
}

func TestGatewayHalfOpenAllowsOneProbeAndPreservesEWMA(t *testing.T) {
	gateway := newGatewayServer(nil, gatewayConfig{Host: "127.0.0.1", Port: 0, TargetProfiles: []string{"openai"}, FailureThreshold: 1, FailureCooldownS: 1})
	endpoint := gateway.endpoints[0]
	upstream := "http://1.1.1.1:8080"
	endpoint.selector.mu.Lock()
	endpoint.selector.upstreams = gatewayTestPool(upstream)
	endpoint.selector.upstreamsLoadedAt = time.Now()
	endpoint.selector.mu.Unlock()
	endpoint.selector.reportFailure(upstream, errors.New("failed"))
	endpoint.selector.mu.Lock()
	state := endpoint.selector.failures[upstream]
	state.IsolatedUntil = time.Now().Add(-time.Millisecond)
	endpoint.selector.failures[upstream] = state
	endpoint.selector.mu.Unlock()
	selected, err := gateway.selectUpstream(endpoint)
	if err != nil || selected != upstream {
		t.Fatalf("expected one half-open probe, selected=%q err=%v", selected, err)
	}
	if _, err := gateway.selectUpstream(endpoint); err == nil {
		t.Fatalf("expected concurrent half-open probe to be rejected")
	}
	endpoint.selector.reportSuccess(upstream, 25*time.Millisecond)
	selected, err = gateway.selectUpstream(endpoint)
	if err != nil || selected != upstream {
		t.Fatalf("expected successful half-open to close circuit, selected=%q err=%v", selected, err)
	}
	endpoint.selector.mu.Lock()
	state = endpoint.selector.failures[upstream]
	endpoint.selector.mu.Unlock()
	if state.Samples != 2 || state.SuccessEWMA <= 0 || state.SuccessEWMA >= 1 || state.LatencyEWMA <= 0 || state.Count != 0 {
		t.Fatalf("runtime EWMA history was cleared or not updated: %#v", state)
	}
}

func TestGatewayRefreshFailureKeepsOldPool(t *testing.T) {
	gateway := newGatewayServer(nil, gatewayConfig{Host: "127.0.0.1", Port: 0, TargetProfiles: []string{"openai"}})
	endpoint := gateway.endpoints[0]
	endpoint.selector.mu.Lock()
	endpoint.selector.upstreams = gatewayTestPool("http://1.1.1.1:8080")
	endpoint.selector.upstreamsLoadedAt = time.Now().Add(-gatewayUpstreamRefreshInterval - time.Second)
	endpoint.selector.mu.Unlock()
	gateway.loadUpstreams = func(availableProxyFilter) ([]gatewayUpstream, error) {
		return nil, errors.New("database unavailable")
	}
	selected, err := gateway.selectUpstream(endpoint)
	if err != nil || selected != "http://1.1.1.1:8080" {
		t.Fatalf("expected old pool after refresh failure, selected=%q err=%v", selected, err)
	}
	waitForCondition(t, time.Second, func() bool {
		endpoint.selector.mu.Lock()
		defer endpoint.selector.mu.Unlock()
		return endpoint.selector.lastRefreshError != ""
	})
	snapshot := gateway.endpointSelectorSnapshot(endpoint)
	if snapshot.Loaded != 1 || snapshot.LastRefreshError == "" {
		t.Fatalf("old pool or refresh error missing: %#v", snapshot)
	}
}

func TestGatewayRefreshDiscardsStaleConfigGeneration(t *testing.T) {
	gateway := newGatewayServer(nil, gatewayConfig{Host: "127.0.0.1", Port: 0, TargetProfiles: []string{"openai"}, UpstreamStrategy: gatewayStrategyRoundRobin})
	endpoint := gateway.endpoints[0]
	endpoint.selector.mu.Lock()
	endpoint.selector.upstreams = gatewayTestPool("http://1.1.1.1:8080")
	endpoint.selector.upstreamsLoadedAt = time.Now().Add(-gatewayUpstreamRefreshInterval - time.Second)
	endpoint.selector.poolGeneration = 1
	endpoint.selector.mu.Unlock()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	gateway.loadUpstreams = func(availableProxyFilter) ([]gatewayUpstream, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return gatewayTestPool("http://stale.invalid:8080"), nil
		}
		return gatewayTestPool("http://fresh.invalid:8080"), nil
	}
	if _, err := gateway.selectUpstream(endpoint); err != nil {
		t.Fatalf("start stale refresh: %v", err)
	}
	<-started
	applied := make(chan struct{})
	go func() {
		cfg := gateway.configSnapshot()
		cfg.UpstreamStrategy = gatewayStrategyStabilityFirst
		gateway.ApplyRuntimeConfig(cfg)
		close(applied)
	}()
	waitForCondition(t, time.Second, func() bool {
		endpoint.selector.mu.Lock()
		defer endpoint.selector.mu.Unlock()
		return endpoint.selector.configGeneration >= 2
	})
	close(release)
	select {
	case <-applied:
	case <-time.After(time.Second):
		t.Fatalf("new configuration refresh did not finish")
	}
	endpoint.selector.mu.Lock()
	defer endpoint.selector.mu.Unlock()
	if len(endpoint.selector.upstreams) != 1 || endpoint.selector.upstreams[0].URL != "http://fresh.invalid:8080" || endpoint.selector.poolGeneration != 2 {
		t.Fatalf("stale refresh replaced new configuration pool: %#v generation=%d", endpoint.selector.upstreams, endpoint.selector.poolGeneration)
	}
}

func TestGatewayRecentSnapshotNewestFirstLimit(t *testing.T) {
	endpoint := &gatewayEndpoint{}
	for _, upstream := range []string{
		"http://1.1.1.1:8080",
		"http://2.2.2.2:8080",
		"http://3.3.3.3:8080",
		"http://4.4.4.4:8080",
		"http://5.5.5.5:8080",
		"http://6.6.6.6:8080",
	} {
		endpoint.mu.Lock()
		endpoint.rememberUpstreamLocked(upstream)
		endpoint.mu.Unlock()
	}
	got := endpoint.recentSnapshot()
	want := []string{
		"http://6.6.6.6:8080",
		"http://5.5.5.5:8080",
		"http://4.4.4.4:8080",
		"http://3.3.3.3:8080",
		"http://2.2.2.2:8080",
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d recent upstreams, got %d: %#v", len(want), len(got), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("unexpected recent upstreams: got %#v want %#v", got, want)
		}
	}
}
