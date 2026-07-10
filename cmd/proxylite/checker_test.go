package main

import "testing"

func TestFailureReasonFormattingAndParsing(t *testing.T) {
	formatted := formatFailureError(classifyFailureReason("i/o timeout"), "i/o timeout")
	if formatted != "[timeout] i/o timeout" {
		t.Fatalf("unexpected formatted failure: %q", formatted)
	}
	if reason := failureReasonFromMessage(formatted); reason != "timeout" {
		t.Fatalf("expected timeout reason, got %q", reason)
	}
	if reason := failureReasonFromMessage("socks5 authentication failed"); reason != "proxy_auth" {
		t.Fatalf("expected proxy_auth reason, got %q", reason)
	}
}

func TestNamedTargetRequiresTargetReachability(t *testing.T) {
	apiUnavailable := false
	if status := targetAvailabilityStatus("grok", true, false, &apiUnavailable); status != "failed" {
		t.Fatalf("expected base-only Grok result to fail target availability, got %q", status)
	}
	if use := recommendUse("grok", true, false, &apiUnavailable, "failed"); use != "base" {
		t.Fatalf("expected base-only capability to be preserved, got %q", use)
	}
	apiAvailable := true
	if status := targetAvailabilityStatus("grok", false, false, &apiAvailable); status != "available" {
		t.Fatalf("expected reachable Grok API to be available, got %q", status)
	}
	if status := targetAvailabilityStatus("generic", true, false, nil); status != "available" {
		t.Fatalf("expected generic base reachability to remain available, got %q", status)
	}
}

func TestFailedProxyDeletionRequiresCompleteBaseFailure(t *testing.T) {
	if !shouldDeleteFailedProxy(&proxyCheckOutcome{Expected: 2, Seen: 2, AllFailed: true, Persisted: true}) {
		t.Fatalf("expected complete persisted base failure to be deletable")
	}
	for name, outcome := range map[string]*proxyCheckOutcome{
		"target still pending": {Expected: 2, Seen: 1, AllFailed: true, Persisted: true},
		"target available":     {Expected: 2, Seen: 2, AllFailed: false, Persisted: true},
		"base reachable":       {Expected: 2, Seen: 2, AllFailed: true, BaseReachable: true, Persisted: true},
		"persistence failed":   {Expected: 2, Seen: 2, AllFailed: true, Persisted: false},
	} {
		if shouldDeleteFailedProxy(outcome) {
			t.Fatalf("expected %s outcome not to be deletable", name)
		}
	}
}

func TestCheckFinalizerReportsPersistenceOutcome(t *testing.T) {
	srv := &server{jobs: newJobManager()}
	partialJob, _ := srv.jobs.Create("check", "check")
	srv.finalizeCheckJob(partialJob.ID, 1, 1, 1, 1, 0, false, map[string]any{"checked": 2})
	partial, _ := srv.jobs.Get(partialJob.ID)
	if partial.Status != jobStatusPartial {
		t.Fatalf("expected partial check status, got %#v", partial)
	}

	failedJob, _ := srv.jobs.Create("check", "check")
	srv.finalizeCheckJob(failedJob.ID, 0, 2, 0, 2, 0, false, map[string]any{"checked": 2})
	failed, _ := srv.jobs.Get(failedJob.ID)
	if failed.Status != jobStatusFailed || failed.Result["checked"] != 2 {
		t.Fatalf("expected failed check status with result, got %#v", failed)
	}
}
