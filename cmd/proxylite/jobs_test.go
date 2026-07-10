package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCreateIfNoRunningBlocksConflictingJobs(t *testing.T) {
	jobs := newJobManager()
	fetchJob, _, running := jobs.CreateIfNoRunning("fetch", "fetch", "fetch", "check")
	if running != nil {
		t.Fatalf("expected first job to start, got conflict %#v", running)
	}
	if fetchJob == nil || fetchJob.Type != "fetch" {
		t.Fatalf("unexpected fetch job %#v", fetchJob)
	}
	checkJob, _, running := jobs.CreateIfNoRunning("check", "check", "fetch", "check")
	if checkJob != nil {
		t.Fatalf("expected check job to be blocked, got %#v", checkJob)
	}
	if running == nil || running.Type != "fetch" {
		t.Fatalf("expected running fetch conflict, got %#v", running)
	}
	if !strings.Contains(fetchJob.StartedAt, "+08:00") {
		t.Fatalf("expected Beijing timestamp, got %q", fetchJob.StartedAt)
	}
}

func TestCancelRequestedKeepsHeavyJobConflict(t *testing.T) {
	jobs := newJobManager()
	job, _, _ := jobs.CreateIfNoRunning("check", "check", "fetch", "check")
	cancelled, ok := jobs.Cancel(job.ID)
	if !ok || cancelled.Status != jobStatusCancelRequested {
		t.Fatalf("expected cancel_requested status, got %#v", cancelled)
	}
	if next, _, running := jobs.CreateIfNoRunning("fetch", "fetch", "fetch", "check"); next != nil || running == nil || running.ID != job.ID {
		t.Fatalf("expected cancelling job to keep conflict, next=%#v running=%#v", next, running)
	}
	jobs.finishCancelled(job.ID, "stopped")
	if next, _, running := jobs.CreateIfNoRunning("fetch", "fetch", "fetch", "check"); next == nil || running != nil {
		t.Fatalf("expected new job after cancellation acknowledgement, next=%#v running=%#v", next, running)
	}
}

func TestCompletionAfterCancelRequestStaysCancelled(t *testing.T) {
	jobs := newJobManager()
	job, _ := jobs.Create("check", "check")
	jobs.Cancel(job.ID)
	jobs.complete(job.ID, "done", map[string]any{"checked": 1})
	got, _ := jobs.Get(job.ID)
	if got.Status != jobStatusCancelled || got.FinishedAt == "" || got.Message != "任务已停止" {
		t.Fatalf("expected cancelled terminal job, got %#v", got)
	}
}

func TestFetchFinalizerDistinguishesFailedAndPartial(t *testing.T) {
	srv := &server{jobs: newJobManager()}
	failedJob, _ := srv.jobs.Create("fetch", "fetch")
	srv.finalizeFetchJob(failedJob.ID, 2, 0, 0, 2, 0, 0, map[string]any{"failed_sources": 2})
	failed, _ := srv.jobs.Get(failedJob.ID)
	if failed.Status != jobStatusFailed {
		t.Fatalf("expected failed fetch status, got %#v", failed)
	}

	partialJob, _ := srv.jobs.Create("fetch", "fetch")
	srv.finalizeFetchJob(partialJob.ID, 2, 3, 1, 1, 1, 0, map[string]any{"failed_sources": 1})
	partial, _ := srv.jobs.Get(partialJob.ID)
	if partial.Status != jobStatusPartial {
		t.Fatalf("expected partial fetch status, got %#v", partial)
	}
}

func TestPersistentJobsSurviveRestartAndIDsDoNotRepeat(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	firstManager := newJobManager(st)
	first, _ := firstManager.CreateWithSpec(jobSpec{Type: "check", Trigger: "periodic", TaskKey: "check.periodic", Message: "check", Params: map[string]any{"limit": 10}})
	if first == nil {
		t.Fatalf("create first persistent job")
	}
	firstManager.Update(first.ID, map[string]any{"done": 5, "total": 10, "status": jobStatusCompleted, "result": map[string]any{"checked": 5}})

	secondManager := newJobManager(st)
	loaded, ok := secondManager.Get(first.ID)
	if !ok || loaded.Status != jobStatusCompleted || loaded.Done != 5 || anyToInt(loaded.Result["checked"]) != 5 {
		t.Fatalf("persistent job missing after restart: %#v", loaded)
	}
	second, _ := secondManager.Create("fetch", "fetch")
	firstID, _ := strconv.ParseInt(first.ID, 10, 64)
	secondID, _ := strconv.ParseInt(second.ID, 10, 64)
	if secondID <= firstID {
		t.Fatalf("job ID repeated across restart: first=%d second=%d", firstID, secondID)
	}
}

func TestTaskSchedulerMigrationFromV040IsIdempotent(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.db.Exec(`DROP TABLE coordinator_state; DROP TABLE scheduler_state; DROP TABLE job_runs`); err != nil {
		t.Fatalf("drop v0.4.1 tables: %v", err)
	}
	if _, err := st.db.Exec("DELETE FROM schema_migrations WHERE version = ?", taskSchedulerMigrationVersion); err != nil {
		t.Fatalf("clear v0.4.1 migration: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("upgrade from v0.4.0: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("repeat v0.4.1 migration: %v", err)
	}
	version, err := st.SchemaVersion()
	if err != nil || version != taskSchedulerMigrationVersion {
		t.Fatalf("unexpected schema version=%d err=%v", version, err)
	}
	for _, table := range []string{"job_runs", "scheduler_state", "coordinator_state"} {
		var count int
		if err := st.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing table %s count=%d err=%v", table, count, err)
		}
	}
}

func TestRestartMarksActiveJobsInterrupted(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	manager := newJobManager(st)
	job, _ := manager.Create("check", "check")
	restarted := newJobManager(st)
	loaded, ok := restarted.Get(job.ID)
	if !ok || loaded.Status != jobStatusInterrupted || loaded.ErrorCode != "process_restarted" || loaded.FinishedAt == "" {
		t.Fatalf("expected interrupted recovery, got %#v", loaded)
	}
}

func TestPersistentJobHistoryFiltersAndPaginates(t *testing.T) {
	st, _ := openStore(":memory:")
	_ = st.EnsureSchema("admin", "password")
	manager := newJobManager(st)
	for index := 0; index < 5; index++ {
		jobType := "check"
		if index%2 == 0 {
			jobType = "fetch"
		}
		job, _ := manager.Create(jobType, jobType)
		manager.complete(job.ID, "done", map[string]any{"index": index})
	}
	items, err := manager.History(jobHistoryFilter{Limit: 2, Type: "fetch"})
	if err != nil || len(items) != 2 || items[0].Type != "fetch" {
		t.Fatalf("unexpected filtered history: %#v err=%v", items, err)
	}
	before, _ := strconv.ParseInt(items[len(items)-1].ID, 10, 64)
	next, err := manager.History(jobHistoryFilter{Limit: 2, Type: "fetch", BeforeID: before})
	if err != nil || len(next) != 1 {
		t.Fatalf("unexpected history page: %#v err=%v", next, err)
	}
}

func TestPersistentJobProgressIsThrottled(t *testing.T) {
	st, _ := openStore(":memory:")
	_ = st.EnsureSchema("admin", "password")
	manager := newJobManager(st)
	job, _ := manager.Create("check", "check")
	manager.Update(job.ID, map[string]any{"done": 1})
	var persisted int
	if err := st.db.QueryRow("SELECT done FROM job_runs WHERE id = ?", job.ID).Scan(&persisted); err != nil || persisted != 0 {
		t.Fatalf("small progress update should be throttled, done=%d err=%v", persisted, err)
	}
	manager.Update(job.ID, map[string]any{"done": 10})
	if err := st.db.QueryRow("SELECT done FROM job_runs WHERE id = ?", job.ID).Scan(&persisted); err != nil || persisted != 10 {
		t.Fatalf("ten-item progress should persist, done=%d err=%v", persisted, err)
	}
}

func TestAllJobTerminalStatusesPersist(t *testing.T) {
	st, _ := openStore(":memory:")
	_ = st.EnsureSchema("admin", "password")
	manager := newJobManager(st)
	for _, status := range []string{jobStatusCompleted, jobStatusPartial, jobStatusFailed, jobStatusCancelled, jobStatusInterrupted} {
		job, _ := manager.Create("check", status)
		if status == jobStatusCancelled {
			manager.Cancel(job.ID)
			manager.finishCancelled(job.ID, "cancelled")
		} else {
			manager.Update(job.ID, map[string]any{"status": status, "message": status, "result": map[string]any{"terminal": status}})
		}
		loaded, ok := manager.Get(job.ID)
		if !ok || loaded.Status != status || loaded.FinishedAt == "" {
			t.Fatalf("terminal status %s not persisted: %#v", status, loaded)
		}
	}
}

func TestCoordinatorMergesDeferredAndAlternatesFairly(t *testing.T) {
	coordinator := newWorkCoordinator()
	acquired, _ := coordinator.TryAcquire("fetch", false, automaticIntent{})
	if !acquired {
		t.Fatalf("expected initial slot")
	}
	coordinator.Bind(&jobState{ID: "1", Type: "fetch", Status: jobStatusRunning})
	coordinator.Defer(automaticIntent{Key: "fetch.low_stock", JobType: "fetch", Reason: "periodic"})
	coordinator.Defer(automaticIntent{Key: "fetch.low_stock", JobType: "fetch", Reason: "low_stock"})
	coordinator.Defer(automaticIntent{Key: "check.periodic", JobType: "check", Reason: "periodic"})
	pending := coordinator.Pending()
	if len(pending) != 2 {
		t.Fatalf("expected merged pending intents, got %#v", pending)
	}
	coordinator.Release(&jobState{ID: "1"})
	next, ok := coordinator.PopNext()
	if !ok || next.JobType != "check" {
		t.Fatalf("expected fairness to grant check after fetch, got %#v", next)
	}
}

func TestCoordinatorFairnessStatePersists(t *testing.T) {
	st, _ := openStore(":memory:")
	_ = st.EnsureSchema("admin", "password")
	coordinator := newWorkCoordinator(st)
	coordinator.Bind(&jobState{ID: "1", Type: "fetch", Status: jobStatusRunning})
	restarted := newWorkCoordinator(st)
	if restarted.LastGrantedType() != "fetch" {
		t.Fatalf("fairness state not restored: %q", restarted.LastGrantedType())
	}
}

func TestCancelRequestedAndCancellingRemainActive(t *testing.T) {
	jobs := newJobManager()
	job, _ := jobs.Create("check", "check")
	jobs.Cancel(job.ID)
	jobs.Update(job.ID, map[string]any{"status": jobStatusCancelling})
	if running := jobs.RunningOfTypes("check"); running == nil || running.Status != jobStatusCancelling {
		t.Fatalf("expected cancelling job to remain active: %#v", running)
	}
	jobs.finishCancelled(job.ID, "stopped")
	if running := jobs.RunningOfTypes("check"); running != nil {
		t.Fatalf("expected terminal cancellation to release active state: %#v", running)
	}
}

func TestSchedulerBackoffSequenceAndSuccessReset(t *testing.T) {
	srv, sc := newSchedulerTestServer(t)
	fixedNow := time.Date(2026, 7, 10, 10, 0, 0, 0, beijingLocation)
	sc.now = func() time.Time { return fixedNow }
	job, _ := srv.jobs.CreateWithSpec(jobSpec{Type: "check", Trigger: "periodic", TaskKey: "check.periodic", Message: "check"})
	srv.coordinator.Bind(job)
	srv.jobs.fail(job.ID, testError("failed"))
	state, _, _ := srv.store.SchedulerState("check.periodic")
	if state.ConsecutiveFailures != 1 || state.BackoffUntil != formatTime(fixedNow.Add(time.Minute)) {
		t.Fatalf("unexpected first backoff: %#v", state)
	}
	fixedNow = fixedNow.Add(time.Minute)
	job, _ = srv.jobs.CreateWithSpec(jobSpec{Type: "check", Trigger: "periodic", TaskKey: "check.periodic", Message: "check"})
	srv.coordinator.Bind(job)
	srv.jobs.fail(job.ID, testError("failed again"))
	state, _, _ = srv.store.SchedulerState("check.periodic")
	if state.ConsecutiveFailures != 2 || state.BackoffUntil != formatTime(fixedNow.Add(2*time.Minute)) {
		t.Fatalf("unexpected second backoff: %#v", state)
	}
	fixedNow = fixedNow.Add(2 * time.Minute)
	job, _ = srv.jobs.CreateWithSpec(jobSpec{Type: "check", Trigger: "periodic", TaskKey: "check.periodic", Message: "check"})
	srv.coordinator.Bind(job)
	srv.jobs.complete(job.ID, "done", map[string]any{})
	state, _, _ = srv.store.SchedulerState("check.periodic")
	if state.ConsecutiveFailures != 0 || state.BackoffUntil != "" || state.LastSuccessAt == "" {
		t.Fatalf("success did not reset backoff: %#v", state)
	}
}

func TestCoordinatorSlotHeldUntilCancellationTerminal(t *testing.T) {
	coordinator := newWorkCoordinator()
	jobs := newJobManager()
	jobs.SetTerminalCallback(coordinator.Release)
	acquired, _ := coordinator.TryAcquire("check", false, automaticIntent{})
	if !acquired {
		t.Fatalf("acquire initial slot")
	}
	job, _ := jobs.Create("check", "check")
	coordinator.Bind(job)
	jobs.Cancel(job.ID)
	if acquired, _ := coordinator.TryAcquire("fetch", false, automaticIntent{}); acquired {
		t.Fatalf("cancel_requested released slot too early")
	}
	jobs.Update(job.ID, map[string]any{"status": jobStatusCancelling})
	if acquired, _ := coordinator.TryAcquire("fetch", false, automaticIntent{}); acquired {
		t.Fatalf("cancelling released slot too early")
	}
	jobs.finishCancelled(job.ID, "stopped")
	if acquired, _ := coordinator.TryAcquire("fetch", false, automaticIntent{}); !acquired {
		t.Fatalf("terminal cancellation did not release slot")
	}
}
