package main

import (
	"context"
	"sync"
)

const (
	defaultCheckConcurrency = 100
	maxCheckConcurrency     = 300
)

type checkConcurrencyController struct {
	mu      sync.Mutex
	limit   int
	active  int
	changed chan struct{}
}

func newCheckConcurrencyController(limit int) *checkConcurrencyController {
	return &checkConcurrencyController{
		limit:   clampInt(limit, 1, maxCheckConcurrency),
		changed: make(chan struct{}),
	}
}

func (c *checkConcurrencyController) SetLimit(limit int) int {
	if c == nil {
		return clampInt(limit, 1, maxCheckConcurrency)
	}
	c.mu.Lock()
	c.limit = clampInt(limit, 1, maxCheckConcurrency)
	c.signalLocked()
	value := c.limit
	c.mu.Unlock()
	return value
}

func (c *checkConcurrencyController) Acquire(ctx context.Context) bool {
	if c == nil {
		return true
	}
	for {
		c.mu.Lock()
		if c.active < c.limit {
			c.active++
			c.mu.Unlock()
			return true
		}
		changed := c.changed
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return false
		case <-changed:
		}
	}
}

func (c *checkConcurrencyController) Release() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.active > 0 {
		c.active--
	}
	c.signalLocked()
	c.mu.Unlock()
}

func (c *checkConcurrencyController) Status() map[string]any {
	if c == nil {
		return map[string]any{"limit": defaultCheckConcurrency, "active": 0, "maximum": maxCheckConcurrency}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return map[string]any{"limit": c.limit, "active": c.active, "maximum": maxCheckConcurrency}
}

func (c *checkConcurrencyController) signalLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
}
