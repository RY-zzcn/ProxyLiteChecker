package main

import (
	"context"
	"fmt"
	"strconv"
	"sync"
)

type jobManager struct {
	mu     sync.Mutex
	nextID int64
	items  map[string]*jobState
}

const jobHistoryLimit = 200

const (
	jobStatusRunning         = "running"
	jobStatusCancelRequested = "cancel_requested"
	jobStatusCompleted       = "completed"
	jobStatusPartial         = "partial"
	jobStatusFailed          = "failed"
	jobStatusCancelled       = "cancelled"
)

type jobState struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Status     string         `json:"status"`
	Message    string         `json:"message"`
	Done       int            `json:"done"`
	Total      int            `json:"total"`
	Success    int            `json:"success"`
	Failed     int            `json:"failed"`
	Error      string         `json:"error,omitempty"`
	Result     map[string]any `json:"result,omitempty"`
	StartedAt  string         `json:"started_at"`
	UpdatedAt  string         `json:"updated_at"`
	FinishedAt string         `json:"finished_at,omitempty"`
	cancel     context.CancelFunc
}

func newJobManager() *jobManager {
	return &jobManager{items: map[string]*jobState{}}
}

func (m *jobManager) Create(jobType string, message string) (*jobState, context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createLocked(jobType, message)
}

func (m *jobManager) CreateIfNoRunning(jobType string, message string, blockingTypes ...string) (*jobState, context.Context, *jobState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if running := m.runningOfTypesLocked(blockingTypes...); running != nil {
		return nil, nil, cloneJob(running)
	}
	job, ctx := m.createLocked(jobType, message)
	return job, ctx, nil
}

func (m *jobManager) createLocked(jobType string, message string) (*jobState, context.Context) {
	m.pruneLocked()
	m.nextID++
	ctx, cancel := context.WithCancel(context.Background())
	now := nowString()
	job := &jobState{
		ID:        strconv.FormatInt(m.nextID, 10),
		Type:      jobType,
		Status:    jobStatusRunning,
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
		if job.Type == jobType && isActiveJobStatus(job.Status) {
			return true
		}
	}
	return false
}

func (m *jobManager) RunningOfTypes(jobTypes ...string) *jobState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if running := m.runningOfTypesLocked(jobTypes...); running != nil {
		return cloneJob(running)
	}
	return nil
}

func (m *jobManager) runningOfTypesLocked(jobTypes ...string) *jobState {
	allowed := map[string]bool{}
	for _, jobType := range jobTypes {
		allowed[jobType] = true
	}
	for _, job := range m.items {
		if isActiveJobStatus(job.Status) && (len(allowed) == 0 || allowed[job.Type]) {
			return cloneJob(job)
		}
	}
	return nil
}

func (m *jobManager) Update(id string, patch map[string]any) (*jobState, bool) {
	m.mu.Lock()
	job, ok := m.items[id]
	if !ok {
		m.mu.Unlock()
		return nil, false
	}
	if isTerminalJobStatus(job.Status) {
		out := cloneJob(job)
		m.mu.Unlock()
		return out, true
	}

	targetStatus := job.Status
	completionCancelled := false
	if value, ok := patch["status"].(string); ok {
		targetStatus = normalizeJobStatus(value, job.Status)
		if job.Status == jobStatusCancelRequested && (targetStatus == jobStatusCompleted || targetStatus == jobStatusPartial) {
			targetStatus = jobStatusCancelled
			completionCancelled = true
		}
		job.Status = targetStatus
	}
	if value, ok := patch["message"].(string); ok && !completionCancelled && (job.Status != jobStatusCancelRequested || isTerminalJobStatus(targetStatus)) {
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
	now := nowString()
	job.UpdatedAt = now
	var cancel context.CancelFunc
	if isTerminalJobStatus(job.Status) {
		if completionCancelled || (job.Status == jobStatusCancelled && job.Message == "") {
			job.Message = "任务已停止"
		}
		job.FinishedAt = now
		cancel = job.cancel
		job.cancel = nil
	}
	out := cloneJob(job)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return out, true
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
		if isActiveJobStatus(job.Status) {
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
	if job.Status == jobStatusRunning {
		job.Status = jobStatusCancelRequested
		job.Message = "任务正在停止，等待当前操作退出"
		job.UpdatedAt = nowString()
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
		"status":  jobStatusCancelled,
		"message": firstNonEmpty(message, "任务已停止"),
	})
}

func (m *jobManager) fail(id string, err error) {
	message := "任务失败"
	if err != nil {
		message = err.Error()
	}
	m.Update(id, map[string]any{
		"status":  jobStatusFailed,
		"message": "任务失败",
		"error":   message,
	})
}

func (m *jobManager) failWithResult(id string, err error, result map[string]any) {
	message := "任务失败"
	if err != nil {
		message = err.Error()
	}
	m.Update(id, map[string]any{
		"status":  jobStatusFailed,
		"message": "任务失败",
		"error":   message,
		"result":  result,
	})
}

func (m *jobManager) complete(id string, message string, result map[string]any) {
	m.Update(id, map[string]any{
		"status":  jobStatusCompleted,
		"message": message,
		"result":  result,
	})
}

func (m *jobManager) partial(id string, message string, result map[string]any) {
	m.Update(id, map[string]any{
		"status":  jobStatusPartial,
		"message": message,
		"result":  result,
	})
}

func (m *jobManager) pruneLocked() {
	for len(m.items) >= jobHistoryLimit {
		oldestID := ""
		oldestValue := int64(0)
		for id, job := range m.items {
			if !isTerminalJobStatus(job.Status) {
				continue
			}
			value, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				continue
			}
			if oldestID == "" || value < oldestValue {
				oldestID = id
				oldestValue = value
			}
		}
		if oldestID == "" {
			return
		}
		delete(m.items, oldestID)
	}
}

func isActiveJobStatus(status string) bool {
	return status == jobStatusRunning || status == jobStatusCancelRequested
}

func isTerminalJobStatus(status string) bool {
	switch status {
	case jobStatusCompleted, jobStatusPartial, jobStatusFailed, jobStatusCancelled:
		return true
	default:
		return false
	}
}

func normalizeJobStatus(status string, fallback string) string {
	switch status {
	case jobStatusRunning, jobStatusCancelRequested, jobStatusCompleted, jobStatusPartial, jobStatusFailed, jobStatusCancelled:
		return status
	default:
		return fallback
	}
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
