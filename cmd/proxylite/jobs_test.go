package main

import (
	"strings"
	"testing"
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
