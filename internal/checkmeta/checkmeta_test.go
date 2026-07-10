package checkmeta

import (
	"errors"
	"sync/atomic"
	"testing"
)

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
