package main

import (
	"errors"
	"sort"
	"strings"
	"sync"
)

var errJobDeferred = errors.New("automatic job deferred")

type automaticIntent struct {
	Key         string         `json:"key"`
	JobType     string         `json:"job_type"`
	Reason      string         `json:"reason"`
	TaskKey     string         `json:"task_key"`
	ParentJobID string         `json:"parent_job_id,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
}

type workCoordinator struct {
	mu              sync.Mutex
	active          *jobState
	reservedType    string
	pending         map[string]automaticIntent
	lastGrantedType string
	consecutive     int
	store           *store
}

func newWorkCoordinator(stores ...*store) *workCoordinator {
	coordinator := &workCoordinator{pending: map[string]automaticIntent{}}
	if len(stores) > 0 {
		coordinator.store = stores[0]
		_ = coordinator.store.db.QueryRow("SELECT value FROM coordinator_state WHERE key = 'last_granted_type'").Scan(&coordinator.lastGrantedType)
		_ = coordinator.store.db.QueryRow("SELECT value FROM coordinator_state WHERE key = 'consecutive_grants'").Scan(&coordinator.consecutive)
	}
	return coordinator
}

func (c *workCoordinator) TryAcquire(jobType string, automatic bool, intent automaticIntent) (bool, *jobState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil || c.reservedType != "" {
		if automatic && intent.Key != "" {
			c.mergePendingLocked(intent)
		}
		return false, cloneJob(c.active)
	}
	c.reservedType = jobType
	return true, nil
}

func (c *workCoordinator) Bind(job *jobState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reservedType = ""
	c.active = cloneJob(job)
	if job == nil {
		return
	}
	if c.lastGrantedType == job.Type {
		c.consecutive++
	} else {
		c.lastGrantedType = job.Type
		c.consecutive = 1
	}
	c.persistFairnessLocked()
}

func (c *workCoordinator) persistFairnessLocked() {
	if c.store == nil {
		return
	}
	now := nowString()
	_, _ = c.store.db.Exec(`
INSERT INTO coordinator_state (key, value, updated_at) VALUES ('last_granted_type', ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, c.lastGrantedType, now)
	_, _ = c.store.db.Exec(`
INSERT INTO coordinator_state (key, value, updated_at) VALUES ('consecutive_grants', ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, c.consecutive, now)
}

func (c *workCoordinator) AbortReservation() {
	c.mu.Lock()
	c.reservedType = ""
	c.mu.Unlock()
}

func (c *workCoordinator) Release(job *jobState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if job == nil || c.active == nil || c.active.ID == job.ID {
		c.active = nil
		c.reservedType = ""
	}
}

func (c *workCoordinator) Active() *jobState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return cloneJob(c.active)
}

func (c *workCoordinator) Defer(intent automaticIntent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mergePendingLocked(intent)
}

func (c *workCoordinator) mergePendingLocked(intent automaticIntent) {
	intent.Key = strings.TrimSpace(intent.Key)
	if intent.Key == "" {
		return
	}
	if existing, ok := c.pending[intent.Key]; ok {
		existing.Reason = mergeReason(existing.Reason, intent.Reason)
		if existing.ParentJobID == "" {
			existing.ParentJobID = intent.ParentJobID
		}
		if existing.Payload == nil {
			existing.Payload = cloneMap(intent.Payload)
		}
		c.pending[intent.Key] = existing
		return
	}
	intent.Payload = cloneMap(intent.Payload)
	c.pending[intent.Key] = intent
}

func (c *workCoordinator) PopNext() (automaticIntent, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active != nil || c.reservedType != "" || len(c.pending) == 0 {
		return automaticIntent{}, false
	}
	keys := make([]string, 0, len(c.pending))
	for key := range c.pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	selected := keys[0]
	for _, key := range keys {
		intent := c.pending[key]
		if intent.JobType != c.lastGrantedType || c.consecutive < 2 {
			selected = key
			break
		}
	}
	intent := c.pending[selected]
	delete(c.pending, selected)
	return intent, true
}

func (c *workCoordinator) Pending() []automaticIntent {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.pending))
	for key := range c.pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]automaticIntent, 0, len(keys))
	for _, key := range keys {
		out = append(out, c.pending[key])
	}
	return out
}

func (c *workCoordinator) LastGrantedType() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastGrantedType
}

func mergeReason(left string, right string) string {
	seen := map[string]bool{}
	parts := []string{}
	for _, value := range []string{left, right} {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part != "" && !seen[part] {
				seen[part] = true
				parts = append(parts, part)
			}
		}
	}
	return strings.Join(parts, ",")
}
