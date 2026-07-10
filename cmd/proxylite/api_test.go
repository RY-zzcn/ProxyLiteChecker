package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStateModelAPICompatibilitySmoke(t *testing.T) {
	st, err := openStore(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := st.EnsureSchema("admin", "password"); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	if _, err := st.ImportProxies("http://1.2.3.4:8080", "test", "http"); err != nil {
		t.Fatalf("import proxy: %v", err)
	}
	items, _, err := st.ListProxies(proxyFilter{Status: "all", Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatalf("list proxy: items=%#v err=%v", items, err)
	}
	if err := st.SaveCheckResult(CheckResult{
		ProxyID: items[0].ID, Status: "available", Grade: "A", TargetProfile: "openai",
		BaseReachable: true, ServiceReachable: true, RecommendedUse: "web",
	}); err != nil {
		t.Fatalf("save result: %v", err)
	}
	srv := &server{store: st}

	proxyRequest := httptest.NewRequest(http.MethodGet, "/api/proxies?status=available&target_profile=openai&limit=10", nil)
	proxyResponse := httptest.NewRecorder()
	srv.handleProxies(proxyResponse, proxyRequest)
	if proxyResponse.Code != http.StatusOK {
		t.Fatalf("proxy API status=%d body=%s", proxyResponse.Code, proxyResponse.Body.String())
	}
	var proxiesPayload struct {
		Items []proxyRecord `json:"items"`
		Total int           `json:"total"`
	}
	if err := json.Unmarshal(proxyResponse.Body.Bytes(), &proxiesPayload); err != nil {
		t.Fatalf("decode proxy API: %v", err)
	}
	if proxiesPayload.Total != 1 || len(proxiesPayload.Items) != 1 || proxiesPayload.Items[0].Probe == nil ||
		proxiesPayload.Items[0].TargetState == nil || proxiesPayload.Items[0].TargetState.Capability != "web" ||
		proxiesPayload.Items[0].Status != "available" {
		t.Fatalf("unexpected proxy API payload: %#v", proxiesPayload)
	}

	statsResponse := httptest.NewRecorder()
	srv.handleStats(statsResponse, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if statsResponse.Code != http.StatusOK {
		t.Fatalf("stats API status=%d body=%s", statsResponse.Code, statsResponse.Body.String())
	}
	var stats map[string]any
	if err := json.Unmarshal(statsResponse.Body.Bytes(), &stats); err != nil {
		t.Fatalf("decode stats API: %v", err)
	}
	if stats["transport_available"] != float64(1) || stats["unique_target_available"] != float64(1) || stats["available"] != float64(1) {
		t.Fatalf("unexpected stats API payload: %#v", stats)
	}

	profilesResponse := httptest.NewRecorder()
	srv.handleTargetProfiles(profilesResponse, httptest.NewRequest(http.MethodGet, "/api/target-profiles", nil))
	if profilesResponse.Code != http.StatusOK || !json.Valid(profilesResponse.Body.Bytes()) {
		t.Fatalf("target profiles API status=%d body=%s", profilesResponse.Code, profilesResponse.Body.String())
	}
}

func TestPersistentJobsAndSchedulerAPISmoke(t *testing.T) {
	srv, _ := newSchedulerTestServer(t)
	job, _ := srv.jobs.CreateWithSpec(jobSpec{Type: "check", Trigger: "manual", Message: "check", Params: map[string]any{"limit": 5}})
	srv.jobs.complete(job.ID, "done", map[string]any{"checked": 0, "noop": true})

	jobsResponse := httptest.NewRecorder()
	srv.handleJobs(jobsResponse, httptest.NewRequest(http.MethodGet, "/api/jobs?limit=10&type=check&status=completed", nil))
	if jobsResponse.Code != http.StatusOK {
		t.Fatalf("jobs API status=%d body=%s", jobsResponse.Code, jobsResponse.Body.String())
	}
	var jobsPayload struct {
		Items []*jobState `json:"items"`
	}
	if err := json.Unmarshal(jobsResponse.Body.Bytes(), &jobsPayload); err != nil || len(jobsPayload.Items) != 1 || jobsPayload.Items[0].ID != job.ID {
		t.Fatalf("unexpected jobs payload=%#v err=%v", jobsPayload, err)
	}

	settings := defaultAppSettings()
	_, _ = srv.store.SaveAppSettings(settings)
	_ = srv.store.SaveSchedulerState(schedulerState{TaskKey: "check.periodic", NextDueAt: nowString(), PendingReason: "busy", UpdatedAt: nowString()})
	schedulerResponse := httptest.NewRecorder()
	srv.handleSchedulerStatus(schedulerResponse, httptest.NewRequest(http.MethodGet, "/api/scheduler/status", nil))
	if schedulerResponse.Code != http.StatusOK || !json.Valid(schedulerResponse.Body.Bytes()) {
		t.Fatalf("scheduler API status=%d body=%s", schedulerResponse.Code, schedulerResponse.Body.String())
	}
}
