package main

import "testing"

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
			ProxyID:        item.ID,
			Status:         "available",
			Grade:          "A",
			LatencyMS:      &latency,
			SuccessRate:    1,
			TargetProfile:  "openai",
			RecommendedUse: "openai",
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
