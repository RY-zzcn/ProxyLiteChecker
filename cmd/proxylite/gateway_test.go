package main

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

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
	endpoint.selector.upstreams = []string{"http://1.1.1.1:8080", "http://2.2.2.2:8080"}
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
	endpoint.selector.upstreams = []string{"http://1.1.1.1:8080", "http://2.2.2.2:8080"}
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
	if snapshot.Active != 2 || snapshot.Skipped != 0 || snapshot.LastAllReleasedAt == "" {
		t.Fatalf("expected all isolation state released, got %#v", snapshot)
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
	endpoint.selector.upstreams = []string{"http://1.1.1.1:8080", "http://2.2.2.2:8080"}
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
	endpoint.selector.upstreams = []string{"http://1.1.1.1:8080", "http://2.2.2.2:8080"}
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
	endpoint.selector.upstreams = []string{"http://1.1.1.1:8080", "http://2.2.2.2:8080"}
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
