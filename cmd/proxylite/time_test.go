package main

import (
	"strings"
	"testing"
	"time"
)

func TestTimeHelpersUseBeijingOffset(t *testing.T) {
	if !strings.Contains(nowString(), "+08:00") {
		t.Fatalf("expected nowString to use Beijing offset, got %q", nowString())
	}
	value := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
	if got := formatBeijingTime(value); got != "2026-07-03T08:00:00+08:00" {
		t.Fatalf("unexpected Beijing time: %s", got)
	}
}
