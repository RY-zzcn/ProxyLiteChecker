package main

import (
	"testing"
	"time"
)

func TestCheckConcurrencyDefaultsToOneHundredAndClampsAtThreeHundred(t *testing.T) {
	settings := defaultAppSettings()
	if settings.CheckConcurrent != defaultCheckConcurrency {
		t.Fatalf("default concurrency=%d want %d", settings.CheckConcurrent, defaultCheckConcurrency)
	}
	settings.CheckConcurrent = 999
	settings = normalizeAppSettings(settings)
	if settings.CheckConcurrent != maxCheckConcurrency {
		t.Fatalf("clamped concurrency=%d want %d", settings.CheckConcurrent, maxCheckConcurrency)
	}
}

func newSchedulerTestServer(t *testing.T) (*server, *scheduler) {
	t.Helper()
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
	srv := &server{store: st, coordinator: newWorkCoordinator()}
	srv.jobs = newJobManager(st)
	sc := newScheduler(srv)
	srv.scheduler = sc
	srv.jobs.SetTerminalCallback(func(job *jobState) {
		srv.coordinator.Release(job)
		sc.OnJobTerminal(job)
	})
	return srv, sc
}

func TestApplySettingsDoesNotPostponeUnrelatedSchedules(t *testing.T) {
	srv, sc := newSchedulerTestServer(t)
	fixedNow := time.Date(2026, 7, 10, 10, 0, 0, 0, beijingLocation)
	sc.now = func() time.Time { return fixedNow }
	previous := defaultAppSettings()
	previous.AutoCheckEnabled = true
	previous.AutoFetchEnabled = true
	current := previous
	current.ExportTargetProfile = "openai"
	checkAt := fixedNow.Add(20 * time.Minute)
	fetchAt := fixedNow.Add(30 * time.Minute)
	_ = srv.store.SaveSchedulerState(schedulerState{TaskKey: "check.periodic", NextDueAt: formatTime(checkAt), UpdatedAt: formatTime(fixedNow)})
	_ = srv.store.SaveSchedulerState(schedulerState{TaskKey: "fetch.periodic", NextDueAt: formatTime(fetchAt), UpdatedAt: formatTime(fixedNow)})

	sc.ApplySettings(previous, current)

	checkState, _, _ := srv.store.SchedulerState("check.periodic")
	fetchState, _, _ := srv.store.SchedulerState("fetch.periodic")
	if checkState.NextDueAt != formatTime(checkAt) {
		t.Fatalf("expected unrelated save to preserve check schedule: got %s", checkState.NextDueAt)
	}
	if fetchState.NextDueAt != formatTime(fetchAt) {
		t.Fatalf("expected unrelated save to preserve fetch schedule: got %s", fetchState.NextDueAt)
	}
}

func TestApplySettingsResetsOnlyChangedSchedule(t *testing.T) {
	srv, sc := newSchedulerTestServer(t)
	fixedNow := time.Date(2026, 7, 10, 10, 0, 0, 0, beijingLocation)
	sc.now = func() time.Time { return fixedNow }
	previous := defaultAppSettings()
	previous.AutoCheckEnabled = true
	previous.AutoFetchEnabled = true
	current := previous
	current.AutoCheckIntervalMinutes = previous.AutoCheckIntervalMinutes + 30
	checkAt := fixedNow.Add(20 * time.Minute)
	fetchAt := fixedNow.Add(30 * time.Minute)
	_ = srv.store.SaveSchedulerState(schedulerState{TaskKey: "check.periodic", NextDueAt: formatTime(checkAt), UpdatedAt: formatTime(fixedNow)})
	_ = srv.store.SaveSchedulerState(schedulerState{TaskKey: "fetch.periodic", NextDueAt: formatTime(fetchAt), UpdatedAt: formatTime(fixedNow)})

	sc.ApplySettings(previous, current)

	checkState, _, _ := srv.store.SchedulerState("check.periodic")
	fetchState, _, _ := srv.store.SchedulerState("fetch.periodic")
	if checkState.NextDueAt == formatTime(checkAt) {
		t.Fatalf("expected changed check interval to update check schedule")
	}
	if fetchState.NextDueAt != formatTime(fetchAt) {
		t.Fatalf("expected unchanged fetch schedule, got %s", fetchState.NextDueAt)
	}
}

func TestNormalizeTargetLowStockSettings(t *testing.T) {
	settings := settingsFromPayload(defaultAppSettings(), map[string]any{
		"target_low_stock_enabled":  true,
		"target_low_stock_profiles": []any{"openai", "invalid", "grok", "openai"},
		"target_low_stock_minimum":  -1,
		"target_candidate_minimum":  99999999,
		"check_after_fetch_enabled": false,
	})
	if !settings.TargetLowStockEnabled || settings.CheckAfterFetchEnabled {
		t.Fatalf("unexpected boolean normalization: %#v", settings)
	}
	if len(settings.TargetLowStockProfiles) != 2 || settings.TargetLowStockProfiles[0] != "openai" || settings.TargetLowStockProfiles[1] != "grok" {
		t.Fatalf("unexpected target profiles: %#v", settings.TargetLowStockProfiles)
	}
	if settings.TargetLowStockMinimum != 1 || settings.TargetCandidateMinimum != 1000000 {
		t.Fatalf("unexpected thresholds: %#v", settings)
	}
}

func TestTargetLowStockChecksCandidatesBeforeFetching(t *testing.T) {
	srv, sc := newSchedulerTestServer(t)
	fixedNow := time.Date(2026, 7, 10, 10, 0, 0, 0, beijingLocation)
	sc.now = func() time.Time { return fixedNow }
	if _, err := srv.store.ImportProxies("http://1.1.1.1:8080\nhttp://2.2.2.2:8080", "test", "http"); err != nil {
		t.Fatalf("import proxies: %v", err)
	}
	settings := defaultAppSettings()
	settings.TargetLowStockEnabled = true
	settings.TargetLowStockProfiles = []string{"openai"}
	settings.TargetLowStockMinimum = 50
	settings.TargetCandidateMinimum = 1
	_, _ = srv.store.SaveAppSettings(settings)
	_ = srv.store.SaveSchedulerState(schedulerState{TaskKey: "check.low_stock.openai", NextDueAt: formatTime(fixedNow), UpdatedAt: formatTime(fixedNow)})
	sc.enqueueDue(settings, fixedNow)
	pending := srv.coordinator.Pending()
	if len(pending) != 1 || pending[0].JobType != "check" || pending[0].TaskKey != "check.low_stock.openai" {
		t.Fatalf("expected low-stock check intent, got %#v", pending)
	}

	srv.coordinator = newWorkCoordinator()
	settings.TargetCandidateMinimum = 200
	sc.srv.coordinator = srv.coordinator
	sc.enqueueDue(settings, fixedNow)
	pending = srv.coordinator.Pending()
	if len(pending) == 0 || pending[0].JobType != "fetch" {
		t.Fatalf("expected fetch when candidates are insufficient, got %#v", pending)
	}
}

func TestSchedulerStateRestoresAcrossSchedulerInstances(t *testing.T) {
	srv, sc := newSchedulerTestServer(t)
	fixedNow := time.Date(2026, 7, 10, 10, 0, 0, 0, beijingLocation)
	next := fixedNow.Add(45 * time.Minute)
	_ = srv.store.SaveSchedulerState(schedulerState{TaskKey: "fetch.periodic", NextDueAt: formatTime(next), BackoffUntil: formatTime(next.Add(time.Minute)), ConsecutiveFailures: 2, UpdatedAt: formatTime(fixedNow)})
	restarted := newScheduler(srv)
	restarted.now = sc.now
	state, found, err := srv.store.SchedulerState("fetch.periodic")
	if err != nil || !found || state.NextDueAt != formatTime(next) || state.ConsecutiveFailures != 2 {
		t.Fatalf("scheduler state did not restore: %#v found=%v err=%v", state, found, err)
	}
}

func TestFetchCompletionCreatesParentedPipelineCheck(t *testing.T) {
	srv, _ := newSchedulerTestServer(t)
	settings := defaultAppSettings()
	settings.CheckAfterFetchEnabled = true
	settings.CheckTargetProfiles = []string{"openai"}
	_, _ = srv.store.SaveAppSettings(settings)
	job, _ := srv.jobs.CreateWithSpec(jobSpec{Type: "fetch", Trigger: "low_stock", TaskKey: "fetch.low_stock", Message: "fetch", Params: map[string]any{"pipeline_targets": []string{"openai"}}})
	srv.coordinator.Bind(job)
	srv.jobs.complete(job.ID, "done", map[string]any{"added": 3})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		items, err := srv.jobs.History(jobHistoryFilter{Limit: 10, Type: "check"})
		if err == nil && len(items) > 0 {
			if items[0].ParentJobID != job.ID || items[0].Trigger != "pipeline" {
				t.Fatalf("unexpected pipeline job: %#v", items[0])
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pipeline check job was not created")
}

func TestDeferredAutomaticWorkDoesNotCountAsFailure(t *testing.T) {
	srv, sc := newSchedulerTestServer(t)
	fixedNow := time.Date(2026, 7, 10, 10, 0, 0, 0, beijingLocation)
	sc.now = func() time.Time { return fixedNow }
	settings := defaultAppSettings()
	settings.AutoCheckEnabled = true
	_, _ = srv.store.SaveAppSettings(settings)
	active, _ := srv.jobs.Create("fetch", "manual fetch")
	srv.coordinator.Bind(active)
	_ = srv.store.SaveSchedulerState(schedulerState{TaskKey: "check.periodic", NextDueAt: formatTime(fixedNow), UpdatedAt: formatTime(fixedNow)})
	sc.enqueueDue(settings, fixedNow)
	sc.dispatchNext()
	state, _, err := srv.store.SchedulerState("check.periodic")
	if err != nil || state.ConsecutiveFailures != 0 || state.PendingReason != "periodic" {
		t.Fatalf("deferred work changed failure state: %#v err=%v", state, err)
	}
	if len(srv.coordinator.Pending()) != 1 {
		t.Fatalf("expected one merged pending intent: %#v", srv.coordinator.Pending())
	}
}

func TestInterruptedScheduledJobGetsSingleRecoveryDue(t *testing.T) {
	srv, sc := newSchedulerTestServer(t)
	fixedNow := time.Date(2026, 7, 10, 10, 0, 0, 0, beijingLocation)
	sc.now = func() time.Time { return fixedNow }
	job, _ := srv.jobs.CreateWithSpec(jobSpec{Type: "check", Trigger: "periodic", TaskKey: "check.periodic", Message: "check"})
	sc.MarkStarted(job)
	restartedJobs := newJobManager(srv.store)
	srv.jobs = restartedJobs
	restarted := newScheduler(srv)
	restarted.now = func() time.Time { return fixedNow }
	restarted.recoverInterruptedSchedules()
	state, _, _ := srv.store.SchedulerState("check.periodic")
	if state.LastOutcome != jobStatusInterrupted || state.NextDueAt != formatTime(fixedNow) || state.PendingReason != "recovery" {
		t.Fatalf("unexpected recovery state: %#v", state)
	}
	restarted.recoverInterruptedSchedules()
	again, _, _ := srv.store.SchedulerState("check.periodic")
	if again.NextDueAt != state.NextDueAt {
		t.Fatalf("recovery catch-up changed on second pass: before=%#v after=%#v", state, again)
	}
}
