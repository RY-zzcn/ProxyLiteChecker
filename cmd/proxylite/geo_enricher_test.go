package main

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/RY-zzcn/ProxyLiteChecker/internal/checkmeta"
)

func TestGeoCacheMigrationAndMetadataUpdate(t *testing.T) {
	st, _ := openStore(":memory:")
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("repeat schema: %v", err)
	}
	var tableCount, migrationCount int
	_ = st.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='ip_geo_cache'").Scan(&tableCount)
	_ = st.db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", geoCacheMigrationVersion).Scan(&migrationCount)
	if tableCount != 1 || migrationCount != 1 {
		t.Fatalf("geo cache migration not idempotent: table=%d migration=%d", tableCount, migrationCount)
	}
	_, _ = st.ImportProxies("http://1.2.3.4:8080", "test", "http")
	items, _, _ := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	proxyID := items[0].ID
	exitIP := "203.0.113.9"
	if err := st.SaveCheckResult(CheckResult{
		ProxyID: proxyID, Status: "available", Grade: "A", TargetProfile: "generic",
		BaseReachable: true, ExitIP: &exitIP, RecommendedUse: "generic",
	}); err != nil {
		t.Fatalf("save base result: %v", err)
	}
	now := time.Date(2026, 7, 10, 15, 0, 0, 0, beijingLocation)
	if err := st.SaveIPGeoSuccess(exitIP, checkmeta.Metadata{Country: "US", ASNOrg: "Example ASN", IPType: "datacenter", GeoSource: "endpoint"}, now, now.Add(time.Hour)); err != nil {
		t.Fatalf("save geo success: %v", err)
	}
	var status, country, asn string
	if err := st.db.QueryRow("SELECT status, country, asn_org FROM proxy_probe_state WHERE proxy_id = ?", proxyID).Scan(&status, &country, &asn); err != nil {
		t.Fatalf("read updated probe: %v", err)
	}
	if status != "available" || country != "US" || asn != "Example ASN" {
		t.Fatalf("geo update changed state or missed metadata: status=%s country=%s asn=%s", status, country, asn)
	}
}

func TestGeoEnricherMergesConcurrentRequestsAndUsesFreshCache(t *testing.T) {
	st, _ := openStore(":memory:")
	_ = st.EnsureSchema("admin", "password")
	g := newGeoEnricherWithConfig(st, geoEnricherConfig{QueueSize: 8, CacheTTL: time.Hour, RetryDelay: time.Minute, Timeout: time.Second})
	g.local = func(string) (checkmeta.Metadata, bool) { return checkmeta.Metadata{}, false }
	var calls atomic.Int32
	called := make(chan struct{}, 1)
	g.lookup = func(context.Context, *http.Client, string, string) (checkmeta.Metadata, bool) {
		calls.Add(1)
		select {
		case called <- struct{}{}:
		default:
		}
		return checkmeta.Metadata{Country: "JP", ASNOrg: "Example", GeoSource: "endpoint"}, true
	}
	g.Start()
	t.Cleanup(g.Stop)
	for index := 0; index < 20; index++ {
		if !g.Enqueue("198.51.100.8") {
			t.Fatalf("duplicate enqueue unexpectedly rejected")
		}
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatalf("metadata lookup did not run")
	}
	waitForCondition(t, time.Second, func() bool {
		entry, found, _ := st.IPGeoCache("198.51.100.8")
		return found && entry.Metadata.Country == "JP"
	})
	metadata := g.LookupAndQueue(context.Background(), "198.51.100.8")
	if metadata.Country != "JP" {
		t.Fatalf("fresh cache was not returned: %#v", metadata)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected singleflight lookup, calls=%d", got)
	}
	if _, err := st.db.Exec("UPDATE ip_geo_cache SET expires_at = ? WHERE ip = ?", formatBeijingTime(time.Now().Add(-time.Minute)), "198.51.100.8"); err != nil {
		t.Fatalf("expire cache: %v", err)
	}
	g.LookupAndQueue(context.Background(), "198.51.100.8")
	waitForCondition(t, time.Second, func() bool { return calls.Load() == 2 })
}

func TestGeoEnricherFailureBackoffAndBoundedQueue(t *testing.T) {
	st, _ := openStore(":memory:")
	_ = st.EnsureSchema("admin", "password")
	fixedNow := time.Date(2026, 7, 10, 15, 0, 0, 0, beijingLocation)
	g := newGeoEnricherWithConfig(st, geoEnricherConfig{QueueSize: 1, CacheTTL: time.Hour, RetryDelay: 30 * time.Minute, Timeout: time.Second})
	g.now = func() time.Time { return fixedNow }
	g.local = func(string) (checkmeta.Metadata, bool) { return checkmeta.Metadata{}, false }
	var calls atomic.Int32
	g.lookup = func(context.Context, *http.Client, string, string) (checkmeta.Metadata, bool) {
		calls.Add(1)
		return checkmeta.Metadata{}, false
	}
	g.process("192.0.2.20")
	g.process("192.0.2.20")
	entry, found, err := st.IPGeoCache("192.0.2.20")
	if err != nil || !found || !entry.RetryAfter.Equal(fixedNow.Add(30*time.Minute)) {
		t.Fatalf("failure backoff not stored: entry=%#v found=%v err=%v", entry, found, err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("retry_after did not suppress duplicate lookup, calls=%d", got)
	}
	if !g.Enqueue("192.0.2.21") {
		t.Fatalf("expected first bounded queue item")
	}
	if g.Enqueue("192.0.2.22") {
		t.Fatalf("expected full enrichment queue to reject second item")
	}
}

func TestGeoEnricherRateLimit(t *testing.T) {
	st, _ := openStore(":memory:")
	_ = st.EnsureSchema("admin", "password")
	g := newGeoEnricherWithConfig(st, geoEnricherConfig{QueueSize: 2, RateInterval: 20 * time.Millisecond, CacheTTL: time.Hour, RetryDelay: time.Minute, Timeout: time.Second})
	g.local = func(string) (checkmeta.Metadata, bool) { return checkmeta.Metadata{}, false }
	calledAt := []time.Time{}
	g.lookup = func(context.Context, *http.Client, string, string) (checkmeta.Metadata, bool) {
		calledAt = append(calledAt, time.Now())
		return checkmeta.Metadata{Country: "US", GeoSource: "endpoint"}, true
	}
	g.process("192.0.2.31")
	g.process("192.0.2.32")
	if len(calledAt) != 2 || calledAt[1].Sub(calledAt[0]) < 15*time.Millisecond {
		t.Fatalf("external lookup rate limit not enforced: %#v", calledAt)
	}
}

func TestGeoEnrichmentDoesNotBlockDetectionLookup(t *testing.T) {
	st, _ := openStore(":memory:")
	_ = st.EnsureSchema("admin", "password")
	g := newGeoEnricherWithConfig(st, geoEnricherConfig{QueueSize: 2, CacheTTL: time.Hour, RetryDelay: time.Minute, Timeout: time.Second})
	g.local = func(string) (checkmeta.Metadata, bool) { return checkmeta.Metadata{}, false }
	started := make(chan struct{})
	release := make(chan struct{})
	g.lookup = func(context.Context, *http.Client, string, string) (checkmeta.Metadata, bool) {
		close(started)
		<-release
		return checkmeta.Metadata{Country: "US", GeoSource: "endpoint"}, true
	}
	g.Start()
	t.Cleanup(g.Stop)
	before := time.Now()
	metadata := g.LookupAndQueue(context.Background(), "192.0.2.40")
	if elapsed := time.Since(before); elapsed > 100*time.Millisecond {
		t.Fatalf("detection lookup blocked on external enrichment: %s", elapsed)
	}
	if metadata.IPType == "" {
		t.Fatalf("expected synchronous local classification")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatalf("background lookup did not start")
	}
	close(release)
}

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatalf("condition was not met within %s", timeout)
		case <-ticker.C:
			if condition() {
				return
			}
		}
	}
}
