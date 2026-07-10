package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestRuntimeMonitorKeepsOnlyLatestThirtyLogs(t *testing.T) {
	monitor := newRuntimeMonitor()
	monitor.startedAt = time.Now().Add(-90 * time.Second)
	for i := 0; i < 35; i++ {
		monitor.Add("info", "entry "+strconv.Itoa(i))
	}
	payload := monitor.Payload(map[string]any{"limit": 100, "active": 3, "maximum": 300})
	logs, ok := payload["logs"].([]runtimeLogEntry)
	if !ok || len(logs) != runtimeLogLimit {
		t.Fatalf("logs=%#v", payload["logs"])
	}
	if logs[0].Message != "entry 5" || logs[len(logs)-1].Message != "entry 34" {
		t.Fatalf("unexpected bounded log range: first=%q last=%q", logs[0].Message, logs[len(logs)-1].Message)
	}
	if anyToInt(payload["uptime_seconds"]) < 89 {
		t.Fatalf("uptime=%v", payload["uptime_seconds"])
	}
}

func TestRuntimeAPIPayload(t *testing.T) {
	monitor := newRuntimeMonitor()
	monitor.Add("warning", "test warning")
	srv := &server{runtime: monitor, checkConcurrency: newCheckConcurrencyController(100)}
	recorder := httptest.NewRecorder()
	srv.handleRuntime(recorder, httptest.NewRequest(http.MethodGet, "/api/runtime", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		LogLimit         int               `json:"log_limit"`
		Logs             []runtimeLogEntry `json:"logs"`
		CheckConcurrency struct {
			Limit   int `json:"limit"`
			Maximum int `json:"maximum"`
		} `json:"check_concurrency"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode runtime payload: %v", err)
	}
	if payload.LogLimit != 30 || len(payload.Logs) != 1 || payload.CheckConcurrency.Limit != 100 || payload.CheckConcurrency.Maximum != 300 {
		t.Fatalf("unexpected runtime payload: %#v", payload)
	}
}

func TestRuntimeLogPrefixAndStatusLabels(t *testing.T) {
	if got := stripRuntimeLogPrefix("2026/07/11 00:35:04 starting proxylite"); got != "starting proxylite" {
		t.Fatalf("prefix was not stripped: %q", got)
	}
	if got := runtimeJobStatusLabel(jobStatusCompleted); got != "已完成" {
		t.Fatalf("unexpected status label: %q", got)
	}
	if got := localizeRuntimeLogMessage("starting HTTP gateway target=openai on [::]:18082"); got != "启动 HTTP 网关：目标 OpenAI，监听 [::]:18082" {
		t.Fatalf("unexpected localized gateway message: %q", got)
	}
	if got := localizeRuntimeLogMessage("database schema version: 402001"); got != "数据库结构版本：402001" {
		t.Fatalf("unexpected localized schema message: %q", got)
	}
}
