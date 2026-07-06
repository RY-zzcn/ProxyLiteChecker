package main

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	gatewayStrategyRoundRobin      = "round_robin"
	gatewayStrategyLowestLatency   = "lowest_latency"
	gatewayStrategyStabilityFirst  = "stability_first"
	gatewayDefaultRetryAttempts    = 2
	gatewayDefaultFailureThreshold = 3
	gatewayDefaultFailureCooldownS = 300
)

type gatewaySelector struct {
	targetProfile string

	mu                   sync.Mutex
	index                int
	upstreams            []string
	upstreamsLoadedAt    time.Time
	lastRefreshError     string
	failures             map[string]gatewayUpstreamFailure
	strategy             string
	failureThreshold     int
	failureCooldown      time.Duration
	lastAllReleasedAt    time.Time
	lastConfigUpdateTime time.Time
}

type gatewayUpstreamFailure struct {
	Count         int
	IsolatedUntil time.Time
	LastError     string
	LastFailureAt time.Time
}

type gatewaySelectorSnapshot struct {
	Loaded                 int
	Active                 int
	Skipped                int
	LastRefreshAt          string
	LastRefreshError       string
	Strategy               string
	RetryAttempts          int
	FailureThreshold       int
	FailureCooldownSeconds int
	LastAllReleasedAt      string
	LastConfigUpdateTime   string
}

func normalizeGatewayConfig(cfg gatewayConfig) gatewayConfig {
	if cfg.RequestTimeoutS <= 0 {
		cfg.RequestTimeoutS = 20
	}
	if cfg.ProfilePortStride <= 0 {
		cfg.ProfilePortStride = 2
	}
	if cfg.RetryAttempts <= 0 {
		cfg.RetryAttempts = gatewayDefaultRetryAttempts
	}
	cfg.RetryAttempts = clampInt(cfg.RetryAttempts, 1, 5)
	if cfg.FailureThreshold <= 0 {
		cfg.FailureThreshold = gatewayDefaultFailureThreshold
	}
	cfg.FailureThreshold = clampInt(cfg.FailureThreshold, 1, 100)
	if cfg.FailureCooldownS <= 0 {
		cfg.FailureCooldownS = gatewayDefaultFailureCooldownS
	}
	cfg.FailureCooldownS = clampInt(cfg.FailureCooldownS, 1, 86400)
	cfg.UpstreamStrategy = normalizeGatewayUpstreamStrategy(cfg.UpstreamStrategy)
	cfg.Countries = normalizeCountryCodes(cfg.Countries)
	cfg.CountryPolicy = normalizeGatewayCountryPolicy(cfg.CountryPolicy)
	return cfg
}

func normalizeGatewayUpstreamStrategy(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case gatewayStrategyLowestLatency, "latency":
		return gatewayStrategyLowestLatency
	case gatewayStrategyStabilityFirst, "stability":
		return gatewayStrategyStabilityFirst
	case gatewayStrategyRoundRobin, "":
		return gatewayStrategyRoundRobin
	default:
		return gatewayStrategyRoundRobin
	}
}

func newGatewaySelector(targetProfile string, cfg gatewayConfig) *gatewaySelector {
	selector := &gatewaySelector{
		targetProfile: strings.TrimSpace(targetProfile),
		failures:      map[string]gatewayUpstreamFailure{},
	}
	selector.applyConfigLocked(cfg)
	return selector
}

func (s *gatewaySelector) applyConfigLocked(cfg gatewayConfig) {
	strategy := normalizeGatewayUpstreamStrategy(cfg.UpstreamStrategy)
	cooldown := time.Duration(clampInt(cfg.FailureCooldownS, 1, 86400)) * time.Second
	threshold := clampInt(cfg.FailureThreshold, 1, 100)
	if s.strategy == strategy && s.failureCooldown == cooldown && s.failureThreshold == threshold {
		return
	}
	s.strategy = strategy
	s.failureCooldown = cooldown
	s.failureThreshold = threshold
	s.lastConfigUpdateTime = time.Now()
}

func (s *gatewaySelector) refresh(g *gatewayServer, force bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refreshLocked(g, force)
}

func (s *gatewaySelector) refreshLocked(g *gatewayServer, force bool) error {
	cfg := g.configSnapshot()
	s.applyConfigLocked(cfg)
	if !force && len(s.upstreams) > 0 && time.Since(s.upstreamsLoadedAt) < gatewayUpstreamRefreshInterval {
		return nil
	}
	if g.store == nil {
		err := fmt.Errorf("store unavailable")
		s.lastRefreshError = err.Error()
		return err
	}
	items, err := g.store.AvailableProxyURLsFiltered(availableProxyFilter{
		TargetProfile: s.targetProfile,
		Limit:         cfg.UpstreamLimit,
		Countries:     cfg.Countries,
		CountryPolicy: cfg.CountryPolicy,
	})
	if err != nil {
		s.lastRefreshError = err.Error()
		if len(s.upstreams) > 0 {
			return nil
		}
		return err
	}
	s.upstreams = append([]string(nil), items...)
	s.upstreamsLoadedAt = time.Now()
	s.lastRefreshError = ""
	if len(s.upstreams) == 0 {
		s.index = 0
	} else {
		s.index %= len(s.upstreams)
	}
	s.dropMissingFailureStateLocked()
	return nil
}

func (s *gatewaySelector) next(g *gatewayServer, tried map[string]bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.refreshLocked(g, false); err != nil && len(s.upstreams) == 0 {
		return "", err
	}
	if len(s.upstreams) == 0 {
		return "", fmt.Errorf("no available %s proxies; run a check first", targetProfileLabel(s.targetProfile))
	}
	now := time.Now()
	item, skipped := s.selectLocked(now, tried)
	if item == "" && skipped > 0 {
		s.releaseAllLocked()
		item, _ = s.selectLocked(now, tried)
	}
	if item == "" {
		return "", fmt.Errorf("no active %s gateway upstreams", targetProfileLabel(s.targetProfile))
	}
	return item, nil
}

func (s *gatewaySelector) selectLocked(now time.Time, tried map[string]bool) (string, int) {
	switch s.strategy {
	case gatewayStrategyLowestLatency:
		return s.selectLowestLatencyLocked(now, tried)
	case gatewayStrategyStabilityFirst:
		return s.selectStabilityFirstLocked(now, tried)
	default:
		return s.selectRoundRobinLocked(now, tried)
	}
}

func (s *gatewaySelector) selectRoundRobinLocked(now time.Time, tried map[string]bool) (string, int) {
	skipped := 0
	start := 0
	if len(s.upstreams) > 0 {
		start = s.index % len(s.upstreams)
	}
	for offset := range s.upstreams {
		index := (start + offset) % len(s.upstreams)
		item := s.upstreams[index]
		if s.isIsolatedLocked(item, now) {
			skipped++
			continue
		}
		if tried != nil && tried[item] {
			continue
		}
		s.index = (index + 1) % len(s.upstreams)
		return item, skipped
	}
	return "", skipped
}

func (s *gatewaySelector) selectLowestLatencyLocked(now time.Time, tried map[string]bool) (string, int) {
	skipped := 0
	for index, item := range s.upstreams {
		if s.isIsolatedLocked(item, now) {
			skipped++
			continue
		}
		if tried != nil && tried[item] {
			continue
		}
		s.index = (index + 1) % len(s.upstreams)
		return item, skipped
	}
	return "", skipped
}

func (s *gatewaySelector) selectStabilityFirstLocked(now time.Time, tried map[string]bool) (string, int) {
	minFailures := -1
	skipped := 0
	for _, item := range s.upstreams {
		if s.isIsolatedLocked(item, now) {
			skipped++
			continue
		}
		if tried != nil && tried[item] {
			continue
		}
		failures := s.failureCountLocked(item)
		if minFailures == -1 || failures < minFailures {
			minFailures = failures
		}
	}
	if minFailures == -1 {
		return "", skipped
	}
	start := 0
	if len(s.upstreams) > 0 {
		start = s.index % len(s.upstreams)
	}
	for offset := range s.upstreams {
		index := (start + offset) % len(s.upstreams)
		item := s.upstreams[index]
		if s.isIsolatedLocked(item, now) {
			continue
		}
		if tried != nil && tried[item] {
			continue
		}
		if s.failureCountLocked(item) != minFailures {
			continue
		}
		s.index = (index + 1) % len(s.upstreams)
		return item, skipped
	}
	return "", skipped
}

func (s *gatewaySelector) reportSuccess(upstream string) {
	if strings.TrimSpace(upstream) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, upstream)
}

func (s *gatewaySelector) reportFailure(upstream string, err error) {
	if strings.TrimSpace(upstream) == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.failures[upstream]
	state.Count++
	state.LastFailureAt = now
	if err != nil {
		state.LastError = err.Error()
	}
	if state.Count >= s.failureThreshold {
		state.IsolatedUntil = now.Add(s.failureCooldown)
	}
	s.failures[upstream] = state
}

func (s *gatewaySelector) snapshot(g *gatewayServer) gatewaySelectorSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.refreshLocked(g, false)
	now := time.Now()
	active := 0
	skipped := 0
	for _, item := range s.upstreams {
		if s.isIsolatedLocked(item, now) {
			skipped++
			continue
		}
		active++
	}
	return gatewaySelectorSnapshot{
		Loaded:                 len(s.upstreams),
		Active:                 active,
		Skipped:                skipped,
		LastRefreshAt:          formatBeijingTime(s.upstreamsLoadedAt),
		LastRefreshError:       s.lastRefreshError,
		Strategy:               s.strategy,
		RetryAttempts:          g.gatewayRetryAttempts(),
		FailureThreshold:       s.failureThreshold,
		FailureCooldownSeconds: int(s.failureCooldown / time.Second),
		LastAllReleasedAt:      formatBeijingTime(s.lastAllReleasedAt),
		LastConfigUpdateTime:   formatBeijingTime(s.lastConfigUpdateTime),
	}
}

func (s *gatewaySelector) isIsolatedLocked(upstream string, now time.Time) bool {
	state, ok := s.failures[upstream]
	if !ok {
		return false
	}
	if state.IsolatedUntil.IsZero() {
		return false
	}
	if now.After(state.IsolatedUntil) {
		delete(s.failures, upstream)
		return false
	}
	return true
}

func (s *gatewaySelector) failureCountLocked(upstream string) int {
	state, ok := s.failures[upstream]
	if !ok {
		return 0
	}
	return state.Count
}

func (s *gatewaySelector) releaseAllLocked() {
	if len(s.failures) == 0 {
		return
	}
	s.failures = map[string]gatewayUpstreamFailure{}
	s.lastAllReleasedAt = time.Now()
}

func (s *gatewaySelector) dropMissingFailureStateLocked() {
	if len(s.failures) == 0 {
		return
	}
	seen := map[string]bool{}
	for _, item := range s.upstreams {
		seen[item] = true
	}
	for item := range s.failures {
		if !seen[item] {
			delete(s.failures, item)
		}
	}
}
