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
