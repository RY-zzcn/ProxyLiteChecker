package checkmeta

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
)

func TestDetectCloudflareStatusAndBlockingSemantics(t *testing.T) {
	headers := make(http.Header)
	headers.Set("CF-Ray", "test-ray")
	if got := DetectCloudflareStatus(http.StatusOK, headers, "checking your browser before accessing"); got != "challenge" {
		t.Fatalf("expected challenge, got %q", got)
	}
	if got := DetectCloudflareStatus(http.StatusForbidden, headers, "denied"); got != "blocked" {
		t.Fatalf("expected blocked, got %q", got)
	}
	if got := DetectCloudflareStatus(http.StatusOK, headers, "ok"); got != "behind_cf" {
		t.Fatalf("expected behind_cf, got %q", got)
	}
	if !CloudflareBlocksAccess("challenge") || !CloudflareBlocksAccess("blocked") {
		t.Fatal("challenge and blocked must reject target access")
	}
	if CloudflareBlocksAccess("behind_cf") || CloudflareBlocksAccess("not_cf") {
		t.Fatal("behind_cf and not_cf must remain usable")
	}
	if got := MergeCloudflareStatus("not_cf", "behind_cf", "challenge", "blocked"); got != "blocked" {
		t.Fatalf("expected worst Cloudflare status blocked, got %q", got)
	}
}

func TestGeoIPInitializationRetriesAfterFailure(t *testing.T) {
	originalGeoIP := geoIP
	originalLoader := loadGeoIPDatabasesFromEnv
	t.Cleanup(func() {
		geoIP = originalGeoIP
		loadGeoIPDatabasesFromEnv = originalLoader
	})
	geoIP = &geoIPReaders{enabled: true}
	var calls atomic.Int32
	loadGeoIPDatabasesFromEnv = func() error {
		if calls.Add(1) == 1 {
			return errors.New("initial load failed")
		}
		return nil
	}
	if err := InitializeGeoIPDatabasesFromEnv(); err == nil {
		t.Fatalf("expected first initialization to fail")
	}
	if err := InitializeGeoIPDatabasesFromEnv(); err != nil {
		t.Fatalf("expected initialization retry to succeed: %v", err)
	}
	if err := InitializeGeoIPDatabasesFromEnv(); err != nil {
		t.Fatalf("expected initialized state to stay successful: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected one failure and one retry, calls=%d", got)
	}
}
