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
