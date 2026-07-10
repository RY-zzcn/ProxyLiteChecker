package main

import (
	"testing"
	"time"
)

func TestApplySettingsDoesNotPostponeUnrelatedSchedules(t *testing.T) {
	sc := &scheduler{}
	previous := defaultAppSettings()
	previous.AutoCheckEnabled = true
	previous.AutoFetchEnabled = true
	current := previous
	current.ExportTargetProfile = "openai"
	checkAt := beijingNow().Add(20 * time.Minute)
	fetchAt := beijingNow().Add(30 * time.Minute)
	sc.nextCheckAt = checkAt
	sc.nextFetchAt = fetchAt

	sc.ApplySettings(previous, current)

	if !sc.nextCheckAt.Equal(checkAt) {
		t.Fatalf("expected unrelated setting save to preserve check schedule: got %s want %s", sc.nextCheckAt, checkAt)
	}
	if !sc.nextFetchAt.Equal(fetchAt) {
		t.Fatalf("expected unrelated setting save to preserve fetch schedule: got %s want %s", sc.nextFetchAt, fetchAt)
	}
}

func TestApplySettingsResetsOnlyChangedSchedule(t *testing.T) {
	sc := &scheduler{}
	previous := defaultAppSettings()
	previous.AutoCheckEnabled = true
	previous.AutoFetchEnabled = true
	current := previous
	current.AutoCheckIntervalMinutes = previous.AutoCheckIntervalMinutes + 30
	checkAt := beijingNow().Add(20 * time.Minute)
	fetchAt := beijingNow().Add(30 * time.Minute)
	sc.nextCheckAt = checkAt
	sc.nextFetchAt = fetchAt

	sc.ApplySettings(previous, current)

	if sc.nextCheckAt.Equal(checkAt) {
		t.Fatalf("expected changed check interval to update check schedule")
	}
	if !sc.nextFetchAt.Equal(fetchAt) {
		t.Fatalf("expected unchanged fetch settings to preserve fetch schedule: got %s want %s", sc.nextFetchAt, fetchAt)
	}
}
