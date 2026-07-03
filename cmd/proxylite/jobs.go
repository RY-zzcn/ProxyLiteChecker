package main

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
)

type jobManager struct {
	mu     sync.Mutex
	nextID int64
	items  map[string]*jobState
}

type jobState struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Status    string         `json:"status"`
	Message   string         `json:"message"`
	Done      int            `json:"done"`
	Total     int            `json:"total"`
	Success   int            `json:"success"`
	Failed    int            `json:"failed"`
	Error     string         `json:"error,omitempty"`
	Result    map[string]any `json:"result,omitempty"`
	StartedAt string         `json:"started_at"`
	UpdatedAt string         `json:"updated_at"`
	cancel    context.CancelFunc
}

func newJobManager() *jobManager {
	return &jobManager{items: map[string]*jobState{}}
}

func (m *jobManager) Create(jobType string, message string) (*jobState, context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now().UTC().Format(time.RFC3339)
	job := &jobState{
		ID:        strconv.FormatInt(m.nextID, 10),
		Type:      jobType,
		Status:    "running",
		Message:   message,
		StartedAt: now,
		UpdatedAt: now,
		cancel:    cancel,
	}
	m.items[job.ID] = job
	return cloneJob(job), ctx
}

func (m *jobManager) TypeRunning(jobType string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.items {
		if job.Type == jobType && job.Status == "running" {
			return true
		}
	}
	return false
}

func (m *jobManager) RunningOfTypes(jobTypes ...string) *jobState {
	m.mu.Lock()
	defer m.mu.Unlock()
	allowed := map[string]bool{}
	for _, jobType := range jobTypes {
		allowed[jobType] = true
	}
	for _, job := range m.items {
		if job.Status == "running" && (len(allowed) == 0 || allowed[job.Type]) {
			return cloneJob(job)
		}
	}
	return nil
}

func (m *jobManager) Update(id string, patch map[string]any) (*jobState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.items[id]
	if !ok {
		return nil, false
	}
	if value, ok := patch["status"].(string); ok {
		job.Status = value
		if value != "running" {
			job.cancel = nil
		}
	}
	if value, ok := patch["message"].(string); ok {
		job.Message = value
	}
	if value, ok := patch["done"]; ok {
		job.Done = anyToInt(value)
	}
	if value, ok := patch["total"]; ok {
		job.Total = anyToInt(value)
	}
	if value, ok := patch["success"]; ok {
		job.Success = anyToInt(value)
	}
	if value, ok := patch["failed"]; ok {
		job.Failed = anyToInt(value)
	}
	if value, ok := patch["error"].(string); ok {
		job.Error = value
	}
	if value, ok := patch["result"].(map[string]any); ok {
		job.Result = value
	}
	job.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return cloneJob(job), true
}

func (m *jobManager) Get(id string) (*jobState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.items[id]
	if !ok {
		return nil, false
	}
	return cloneJob(job), true
}

func (m *jobManager) Active() []*jobState {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := []*jobState{}
	for _, job := range m.items {
		if job.Status == "running" {
			items = append(items, cloneJob(job))
		}
	}
	return items
}

func (m *jobManager) Cancel(id string) (*jobState, bool) {
	m.mu.Lock()
	job, ok := m.items[id]
	if !ok {
		m.mu.Unlock()
		return nil, false
	}
	cancel := job.cancel
	if job.Status == "running" {
		job.Status = "cancelled"
		job.Message = "任务已请求停止"
		job.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		job.cancel = nil
	}
	out := cloneJob(job)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return out, true
}

func cloneJob(job *jobState) *jobState {
	if job == nil {
		return nil
	}
	copyJob := *job
	copyJob.cancel = nil
	if job.Result != nil {
		copyJob.Result = map[string]any{}
		for key, value := range job.Result {
			copyJob.Result[key] = value
		}
	}
	return &copyJob
}

func (m *jobManager) finishCancelled(id string, message string) {
	m.Update(id, map[string]any{
		"status":  "cancelled",
		"message": firstNonEmpty(message, "任务已停止"),
	})
}

func (m *jobManager) fail(id string, err error) {
	message := "任务失败"
	if err != nil {
		message = err.Error()
	}
	m.Update(id, map[string]any{
		"status":  "failed",
		"message": "任务失败",
		"error":   message,
	})
}

func (m *jobManager) complete(id string, message string, result map[string]any) {
	m.Update(id, map[string]any{
		"status":  "completed",
		"message": message,
		"result":  result,
	})
}

func jobConflict(jobType string) error {
	return fmt.Errorf("%s job is already running", jobType)
}

func runningJobConflict(job *jobState) error {
	if job == nil {
		return nil
	}
	return fmt.Errorf("已有%s任务运行中，请等待完成或先停止当前任务", jobTypeLabel(job.Type))
}

func jobTypeLabel(jobType string) string {
	switch jobType {
	case "fetch":
		return "拉取"
	case "check":
		return "检测"
	default:
		return jobType
	}
}
