package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	jobStatusQueued          = "queued"
	jobStatusRunning         = "running"
	jobStatusCancelRequested = "cancel_requested"
	jobStatusCancelling      = "cancelling"
	jobStatusCompleted       = "completed"
	jobStatusPartial         = "partial"
	jobStatusFailed          = "failed"
	jobStatusCancelled       = "cancelled"
	jobStatusInterrupted     = "interrupted"
	jobResultJSONLimit       = 64 * 1024
)

type jobSpec struct {
	Type          string
	Trigger       string
	TriggerReason string
	TaskKey       string
	ParentJobID   string
	Message       string
	Params        map[string]any
}

type jobState struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Trigger       string         `json:"trigger"`
	TriggerReason string         `json:"trigger_reason,omitempty"`
	TaskKey       string         `json:"task_key,omitempty"`
	ParentJobID   string         `json:"parent_job_id,omitempty"`
	Status        string         `json:"status"`
	Params        map[string]any `json:"params,omitempty"`
	Message       string         `json:"message"`
	Done          int            `json:"done"`
	Total         int            `json:"total"`
	Success       int            `json:"success"`
	Failed        int            `json:"failed"`
	ErrorCode     string         `json:"error_code,omitempty"`
	Error         string         `json:"error,omitempty"`
	Result        map[string]any `json:"result,omitempty"`
	InstanceID    string         `json:"instance_id,omitempty"`
	StartedAt     string         `json:"started_at"`
	UpdatedAt     string         `json:"updated_at"`
	FinishedAt    string         `json:"finished_at,omitempty"`
	cancel        context.CancelFunc
	persistedDone int
	lastPersistAt time.Time
}

type jobHistoryFilter struct {
	Limit    int
	Type     string
	Status   string
	BeforeID int64
}

type jobManager struct {
	mu         sync.Mutex
	store      *store
	instanceID string
	nextID     int64
	items      map[string]*jobState
	onTerminal func(*jobState)
}

func newJobManager(stores ...*store) *jobManager {
	manager := &jobManager{items: map[string]*jobState{}}
	if len(stores) > 0 {
		manager.store = stores[0]
	}
	token, err := randomTokenURLSafe(12)
	if err != nil {
		token = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	manager.instanceID = token
	if manager.store != nil {
		_ = manager.recoverInterrupted()
	}
	return manager
}

func (m *jobManager) SetTerminalCallback(callback func(*jobState)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onTerminal = callback
}

func (m *jobManager) InstanceID() string { return m.instanceID }

func (m *jobManager) Create(jobType string, message string) (*jobState, context.Context) {
	return m.CreateWithSpec(jobSpec{Type: jobType, Trigger: "manual", Message: message})
}

func (m *jobManager) CreateWithSpec(spec jobSpec) (*jobState, context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.createLocked(spec)
}

func (m *jobManager) CreateIfNoRunning(jobType string, message string, blockingTypes ...string) (*jobState, context.Context, *jobState) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if running := m.runningOfTypesLocked(blockingTypes...); running != nil {
		return nil, nil, cloneJob(running)
	}
	job, ctx := m.createLocked(jobSpec{Type: jobType, Trigger: "manual", Message: message})
	return job, ctx, nil
}

func (m *jobManager) createLocked(spec jobSpec) (*jobState, context.Context) {
	spec.Type = strings.TrimSpace(spec.Type)
	if spec.Trigger == "" {
		spec.Trigger = "manual"
	}
	ctx, cancel := context.WithCancel(context.Background())
	now := nowString()
	job := &jobState{
		Type:          spec.Type,
		Trigger:       spec.Trigger,
		TriggerReason: strings.TrimSpace(spec.TriggerReason),
		TaskKey:       strings.TrimSpace(spec.TaskKey),
		ParentJobID:   strings.TrimSpace(spec.ParentJobID),
		Status:        jobStatusRunning,
		Params:        cloneMap(spec.Params),
		Message:       spec.Message,
		InstanceID:    m.instanceID,
		StartedAt:     now,
		UpdatedAt:     now,
		cancel:        cancel,
		lastPersistAt: time.Now(),
	}
	if m.store == nil {
		m.nextID++
		job.ID = strconv.FormatInt(m.nextID, 10)
	} else {
		paramsJSON := boundedJSON(job.Params)
		var parent any
		if value, err := strconv.ParseInt(job.ParentJobID, 10, 64); err == nil && value > 0 {
			parent = value
		}
		result, err := m.store.db.Exec(`
INSERT INTO job_runs (
  type, trigger, trigger_reason, task_key, parent_job_id, status, params_json,
  message, instance_id, started_at, updated_at
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			job.Type, job.Trigger, job.TriggerReason, job.TaskKey, parent, job.Status,
			paramsJSON, job.Message, job.InstanceID, job.StartedAt, job.UpdatedAt)
		if err != nil {
			cancel()
			return nil, nil
		}
		id, err := result.LastInsertId()
		if err != nil {
			cancel()
			return nil, nil
		}
		job.ID = strconv.FormatInt(id, 10)
	}
	m.items[job.ID] = job
	return cloneJob(job), ctx
}

func (m *jobManager) TypeRunning(jobType string) bool {
	return m.RunningOfTypes(jobType) != nil
}

func (m *jobManager) RunningOfTypes(jobTypes ...string) *jobState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneJob(m.runningOfTypesLocked(jobTypes...))
}

func (m *jobManager) runningOfTypesLocked(jobTypes ...string) *jobState {
	allowed := map[string]bool{}
	for _, jobType := range jobTypes {
		allowed[jobType] = true
	}
	for _, job := range m.items {
		if isActiveJobStatus(job.Status) && (len(allowed) == 0 || allowed[job.Type]) {
			return job
		}
	}
	if m.store == nil {
		return nil
	}
	clauses := []string{"status IN ('queued','running','cancel_requested','cancelling')"}
	args := []any{}
	if len(allowed) > 0 {
		clauses = append(clauses, "type IN ("+placeholders(len(allowed))+")")
		for jobType := range allowed {
			args = append(args, jobType)
		}
	}
	row := m.store.db.QueryRow(`
SELECT id, type, trigger, trigger_reason, task_key, parent_job_id, status, params_json,
       done, total, success, failed, message, error_code, error_message, result_json,
       instance_id, started_at, updated_at, finished_at
FROM job_runs WHERE `+strings.Join(clauses, " AND ")+` ORDER BY id DESC LIMIT 1`, args...)
	job, err := scanJob(row)
	if err != nil {
		return nil
	}
	return job
}

func (m *jobManager) Update(id string, patch map[string]any) (*jobState, bool) {
	m.mu.Lock()
	job := m.items[id]
	if job == nil && m.store != nil {
		loaded, ok := m.getPersistentLocked(id)
		if ok {
			job = loaded
		}
	}
	if job == nil {
		m.mu.Unlock()
		return nil, false
	}
	if isTerminalJobStatus(job.Status) {
		out := cloneJob(job)
		m.mu.Unlock()
		return out, true
	}
	previousStatus := job.Status
	targetStatus := job.Status
	completionCancelled := false
	if value, ok := patch["status"].(string); ok {
		targetStatus = normalizeJobStatus(value, job.Status)
		if (job.Status == jobStatusCancelRequested || job.Status == jobStatusCancelling) &&
			(targetStatus == jobStatusCompleted || targetStatus == jobStatusPartial) {
			targetStatus = jobStatusCancelled
			completionCancelled = true
		}
		job.Status = targetStatus
	}
	if value, ok := patch["message"].(string); ok && !completionCancelled {
		job.Message = truncateText(value, 2048)
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
	if value, ok := patch["error_code"].(string); ok {
		job.ErrorCode = truncateText(value, 128)
	}
	if value, ok := patch["error"].(string); ok {
		job.Error = truncateText(value, 4096)
	}
	if value, ok := patch["result"].(map[string]any); ok {
		job.Result = cloneMap(value)
	}
	now := nowString()
	job.UpdatedAt = now
	var cancel context.CancelFunc
	terminal := isTerminalJobStatus(job.Status)
	if terminal {
		if completionCancelled || (job.Status == jobStatusCancelled && job.Message == "") {
			job.Message = "任务已停止"
		}
		job.FinishedAt = now
		cancel = job.cancel
		job.cancel = nil
	}
	shouldPersist := true
	if m.store != nil && !terminal {
		_, hasDone := patch["done"]
		_, hasStatus := patch["status"]
		_, hasTotal := patch["total"]
		if hasDone && !hasStatus && !hasTotal && job.Done-job.persistedDone < 10 && time.Since(job.lastPersistAt) < time.Second {
			shouldPersist = false
		}
	}
	if m.store != nil && shouldPersist {
		finished := any(nil)
		if job.FinishedAt != "" {
			finished = job.FinishedAt
		}
		_, err := m.store.db.Exec(`
UPDATE job_runs SET status = ?, done = ?, total = ?, success = ?, failed = ?, message = ?,
  error_code = ?, error_message = ?, result_json = ?, updated_at = ?, finished_at = ?
WHERE id = ?`, job.Status, job.Done, job.Total, job.Success, job.Failed, job.Message,
			job.ErrorCode, job.Error, boundedJSON(job.Result), job.UpdatedAt, finished, job.ID)
		if err != nil {
			job.Status = previousStatus
			m.mu.Unlock()
			return nil, false
		}
		job.persistedDone = job.Done
		job.lastPersistAt = time.Now()
	}
	out := cloneJob(job)
	callback := m.onTerminal
	if terminal && m.store != nil {
		delete(m.items, id)
	}
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if terminal && callback != nil {
		callback(out)
	}
	return out, true
}

func (m *jobManager) Get(id string) (*jobState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if job := m.items[id]; job != nil {
		return cloneJob(job), true
	}
	return m.getPersistentLocked(id)
}

func (m *jobManager) getPersistentLocked(id string) (*jobState, bool) {
	if m.store == nil {
		return nil, false
	}
	row := m.store.db.QueryRow(`
SELECT id, type, trigger, trigger_reason, task_key, parent_job_id, status, params_json,
       done, total, success, failed, message, error_code, error_message, result_json,
       instance_id, started_at, updated_at, finished_at
FROM job_runs WHERE id = ?`, id)
	job, err := scanJob(row)
	return job, err == nil
}

func (m *jobManager) Active() []*jobState {
	items, _ := m.History(jobHistoryFilter{Limit: 200, Status: "active"})
	return items
}

func (m *jobManager) History(filter jobHistoryFilter) ([]*jobState, error) {
	if m.store == nil {
		m.mu.Lock()
		defer m.mu.Unlock()
		out := []*jobState{}
		for _, job := range m.items {
			if filter.Type != "" && job.Type != filter.Type {
				continue
			}
			if filter.Status != "" && filter.Status != "active" && job.Status != filter.Status {
				continue
			}
			out = append(out, cloneJob(job))
		}
		return out, nil
	}
	limit := clampInt(filter.Limit, 1, 200)
	clauses := []string{"1=1"}
	args := []any{}
	if filter.Type != "" {
		clauses = append(clauses, "type = ?")
		args = append(args, filter.Type)
	}
	if filter.Status == "active" {
		clauses = append(clauses, "status IN ('queued','running','cancel_requested','cancelling')")
	} else if filter.Status != "" {
		clauses = append(clauses, "status = ?")
		args = append(args, filter.Status)
	}
	if filter.BeforeID > 0 {
		clauses = append(clauses, "id < ?")
		args = append(args, filter.BeforeID)
	}
	args = append(args, limit)
	rows, err := m.store.db.Query(`
SELECT id, type, trigger, trigger_reason, task_key, parent_job_id, status, params_json,
       done, total, success, failed, message, error_code, error_message, result_json,
       instance_id, started_at, updated_at, finished_at
FROM job_runs WHERE `+strings.Join(clauses, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*jobState{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (m *jobManager) Cancel(id string) (*jobState, bool) {
	m.mu.Lock()
	job := m.items[id]
	if job == nil {
		loaded, ok := m.getPersistentLocked(id)
		if !ok {
			m.mu.Unlock()
			return nil, false
		}
		job = loaded
	}
	if job.Status == jobStatusRunning || job.Status == jobStatusQueued {
		job.Status = jobStatusCancelRequested
		job.Message = "任务正在停止，等待当前操作退出"
		job.UpdatedAt = nowString()
		if m.store != nil {
			if _, err := m.store.db.Exec("UPDATE job_runs SET status = ?, message = ?, updated_at = ? WHERE id = ?", job.Status, job.Message, job.UpdatedAt, job.ID); err != nil {
				m.mu.Unlock()
				return nil, false
			}
		}
	}
	cancel := job.cancel
	out := cloneJob(job)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return out, true
}

func (m *jobManager) finishCancelled(id string, message string) {
	m.Update(id, map[string]any{"status": jobStatusCancelling, "message": "正在完成已开始的结果写入"})
	m.Update(id, map[string]any{"status": jobStatusCancelled, "message": firstNonEmpty(message, "任务已停止")})
}

func (m *jobManager) fail(id string, err error) {
	m.failWithResult(id, err, nil)
}

func (m *jobManager) failWithResult(id string, err error, result map[string]any) {
	message := "任务失败"
	if err != nil {
		message = err.Error()
	}
	m.Update(id, map[string]any{"status": jobStatusFailed, "message": "任务失败", "error_code": "job_failed", "error": message, "result": result})
}

func (m *jobManager) complete(id string, message string, result map[string]any) {
	m.Update(id, map[string]any{"status": jobStatusCompleted, "message": message, "result": result})
}

func (m *jobManager) partial(id string, message string, result map[string]any) {
	m.Update(id, map[string]any{"status": jobStatusPartial, "message": message, "result": result})
}

func (m *jobManager) recoverInterrupted() error {
	if m.store == nil {
		return nil
	}
	_, err := m.store.db.Exec(`
UPDATE job_runs
SET status = 'interrupted', error_code = 'process_restarted',
    error_message = '任务因服务重启中断', message = '任务因服务重启中断',
    updated_at = datetime('now', '+8 hours'), finished_at = datetime('now', '+8 hours')
WHERE status IN ('queued','running','cancel_requested','cancelling')`)
	return err
}

func scanJob(scanner interface{ Scan(...any) error }) (*jobState, error) {
	var job jobState
	var id int64
	var parent sql.NullInt64
	var paramsJSON, resultJSON string
	var finished sql.NullString
	if err := scanner.Scan(
		&id, &job.Type, &job.Trigger, &job.TriggerReason, &job.TaskKey, &parent, &job.Status, &paramsJSON,
		&job.Done, &job.Total, &job.Success, &job.Failed, &job.Message, &job.ErrorCode, &job.Error,
		&resultJSON, &job.InstanceID, &job.StartedAt, &job.UpdatedAt, &finished,
	); err != nil {
		return nil, err
	}
	job.ID = strconv.FormatInt(id, 10)
	if parent.Valid {
		job.ParentJobID = strconv.FormatInt(parent.Int64, 10)
	}
	_ = json.Unmarshal([]byte(paramsJSON), &job.Params)
	_ = json.Unmarshal([]byte(resultJSON), &job.Result)
	if finished.Valid {
		job.FinishedAt = finished.String
	}
	return &job, nil
}

func cloneJob(job *jobState) *jobState {
	if job == nil {
		return nil
	}
	copyJob := *job
	copyJob.cancel = nil
	copyJob.Params = cloneMap(job.Params)
	copyJob.Result = cloneMap(job.Result)
	return &copyJob
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func boundedJSON(value map[string]any) string {
	if value == nil {
		return "{}"
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	if len(raw) <= jobResultJSONLimit {
		return string(raw)
	}
	truncated, _ := json.Marshal(map[string]any{"truncated": true, "original_bytes": len(raw)})
	return string(truncated)
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}

func isActiveJobStatus(status string) bool {
	switch status {
	case jobStatusQueued, jobStatusRunning, jobStatusCancelRequested, jobStatusCancelling:
		return true
	default:
		return false
	}
}

func isTerminalJobStatus(status string) bool {
	switch status {
	case jobStatusCompleted, jobStatusPartial, jobStatusFailed, jobStatusCancelled, jobStatusInterrupted:
		return true
	default:
		return false
	}
}

func normalizeJobStatus(status string, fallback string) string {
	switch status {
	case jobStatusQueued, jobStatusRunning, jobStatusCancelRequested, jobStatusCancelling,
		jobStatusCompleted, jobStatusPartial, jobStatusFailed, jobStatusCancelled, jobStatusInterrupted:
		return status
	default:
		return fallback
	}
}

func jobConflict(jobType string) error { return fmt.Errorf("%s job is already running", jobType) }

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
	case "maintenance":
		return "维护"
	default:
		return jobType
	}
}
