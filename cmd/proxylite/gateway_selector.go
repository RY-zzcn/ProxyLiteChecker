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
	gatewayRuntimeEWMAAlpha        = 0.2
)

type gatewayUpstream struct {
	URL           string
	LatencyMS     int
	SuccessRate   float64
	CheckedAt     time.Time
	Capability    string
	TargetProfile string
}

type gatewaySelector struct {
	targetProfile string

	mu                   sync.Mutex
	refreshMu            sync.Mutex
	refreshing           bool
	index                int
	upstreams            []gatewayUpstream
	upstreamsLoadedAt    time.Time
	poolGeneration       uint64
	configGeneration     uint64
	lastRefreshError     string
	failures             map[string]gatewayUpstreamFailure
	strategy             string
	failureThreshold     int
	failureCooldown      time.Duration
	lastDegradedAt       time.Time
	degraded             bool
	lastConfigUpdateTime time.Time
}

type gatewayUpstreamFailure struct {
	Count            int
	IsolatedUntil    time.Time
	LastError        string
	LastFailureAt    time.Time
	SuccessEWMA      float64
	LatencyEWMA      float64
	Samples          int
	HalfOpenInFlight int
}

type gatewaySelectorSnapshot struct {
	Loaded                 int
	Active                 int
	Skipped                int
	Closed                 int
	Open                   int
	HalfOpen               int
	Degraded               bool
	PoolGeneration         uint64
	PoolAgeMS              int64
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

func (s *gatewaySelector) updateConfig(cfg gatewayConfig) {
	s.mu.Lock()
	s.applyConfigLocked(cfg)
	s.mu.Unlock()
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
	s.configGeneration++
}

func (s *gatewaySelector) refresh(g *gatewayServer, force bool) error {
	s.mu.Lock()
	s.refreshing = true
	s.mu.Unlock()
	return s.refreshReserved(g, force)
}

func (s *gatewaySelector) refreshReserved(g *gatewayServer, force bool) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	cfg := g.configSnapshot()
	s.mu.Lock()
	s.applyConfigLocked(cfg)
	if !force && len(s.upstreams) > 0 && time.Since(s.upstreamsLoadedAt) < gatewayUpstreamRefreshInterval {
		s.refreshing = false
		s.mu.Unlock()
		return nil
	}
	configGeneration := s.configGeneration
	s.mu.Unlock()

	if g.loadUpstreams == nil {
		s.finishRefreshError(fmt.Errorf("gateway upstream loader unavailable"))
		return fmt.Errorf("gateway upstream loader unavailable")
	}
	items, err := g.loadUpstreams(availableProxyFilter{
		TargetProfile: s.targetProfile,
		Limit:         cfg.UpstreamLimit,
		Countries:     cfg.Countries,
		CountryPolicy: cfg.CountryPolicy,
	})
	if err != nil {
		s.finishRefreshError(err)
		s.mu.Lock()
		hasPool := len(s.upstreams) > 0
		s.mu.Unlock()
		if hasPool {
			return nil
		}
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshing = false
	if configGeneration != s.configGeneration {
		return nil
	}
	s.upstreams = append([]gatewayUpstream(nil), items...)
	s.upstreamsLoadedAt = time.Now()
	s.poolGeneration++
	s.lastRefreshError = ""
	s.degraded = false
	if len(s.upstreams) == 0 {
		s.index = 0
	} else {
		s.index %= len(s.upstreams)
	}
	s.dropMissingFailureStateLocked()
	go g.invalidateStatus()
	return nil
}

func (s *gatewaySelector) finishRefreshError(err error) {
	s.mu.Lock()
	s.refreshing = false
	if err != nil {
		s.lastRefreshError = err.Error()
	}
	s.mu.Unlock()
}

func (s *gatewaySelector) startAsyncRefresh(g *gatewayServer) {
	s.mu.Lock()
	if s.refreshing {
		s.mu.Unlock()
		return
	}
	s.refreshing = true
	s.mu.Unlock()
	go func() { _ = s.refreshReserved(g, false) }()
}

func (s *gatewaySelector) ensurePool(g *gatewayServer) error {
	s.mu.Lock()
	hasPool := len(s.upstreams) > 0
	stale := !hasPool || time.Since(s.upstreamsLoadedAt) >= gatewayUpstreamRefreshInterval
	s.mu.Unlock()
	if !stale {
		return nil
	}
	if hasPool {
		s.startAsyncRefresh(g)
		return nil
	}
	return s.refresh(g, true)
}

func (s *gatewaySelector) next(g *gatewayServer, tried map[string]bool) (string, error) {
	s.updateConfig(g.configSnapshot())
	if err := s.ensurePool(g); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.upstreams) == 0 {
		return "", fmt.Errorf("no available %s proxies; run a check first", targetProfileLabel(s.targetProfile))
	}
	now := time.Now()
	item, skipped := s.selectLocked(now, tried)
	if item == "" && skipped > 0 {
		item = s.selectDegradedLocked(now, tried)
	}
	if item == "" {
		return "", fmt.Errorf("no active %s gateway upstreams", targetProfileLabel(s.targetProfile))
	}
	return item, nil
}

func (s *gatewaySelector) selectLocked(now time.Time, tried map[string]bool) (string, int) {
	s.degraded = false
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
		if tried != nil && tried[item.URL] {
			continue
		}
		if !s.markSelectableLocked(item.URL, now) {
			skipped++
			continue
		}
		s.index = (index + 1) % len(s.upstreams)
		return item.URL, skipped
	}
	return "", skipped
}

func (s *gatewaySelector) selectLowestLatencyLocked(now time.Time, tried map[string]bool) (string, int) {
	bestIndex := -1
	bestScore := float64(1 << 30)
	skipped := 0
	for index, item := range s.upstreams {
		if tried != nil && tried[item.URL] {
			continue
		}
		if !s.isSelectableLocked(item.URL, now) {
			skipped++
			continue
		}
		score := float64(item.LatencyMS)
		if item.LatencyMS <= 0 {
			score = 1 << 29
		}
		if runtime := s.failures[item.URL]; runtime.LatencyEWMA > 0 {
			score = runtime.LatencyEWMA*0.7 + score*0.3
		}
		if score < bestScore {
			bestScore = score
			bestIndex = index
		}
	}
	if bestIndex < 0 {
		return "", skipped
	}
	item := s.upstreams[bestIndex]
	s.markHalfOpenSelectedLocked(item.URL, now)
	s.index = (bestIndex + 1) % len(s.upstreams)
	return item.URL, skipped
}

func (s *gatewaySelector) selectStabilityFirstLocked(now time.Time, tried map[string]bool) (string, int) {
	bestIndex := -1
	bestScore := -1.0
	skipped := 0
	for index, item := range s.upstreams {
		if tried != nil && tried[item.URL] {
			continue
		}
		if !s.isSelectableLocked(item.URL, now) {
			skipped++
			continue
		}
		score := item.SuccessRate
		if runtime := s.failures[item.URL]; runtime.Samples > 0 {
			score = runtime.SuccessEWMA*0.7 + item.SuccessRate*0.3
		}
		if score > bestScore || (score == bestScore && upstreamLatency(item) < upstreamLatency(s.upstreams[bestIndex])) {
			bestScore = score
			bestIndex = index
		}
	}
	if bestIndex < 0 {
		return "", skipped
	}
	item := s.upstreams[bestIndex]
	s.markHalfOpenSelectedLocked(item.URL, now)
	s.index = (bestIndex + 1) % len(s.upstreams)
	return item.URL, skipped
}

func (s *gatewaySelector) selectDegradedLocked(now time.Time, tried map[string]bool) string {
	bestIndex := -1
	for index, item := range s.upstreams {
		if tried != nil && tried[item.URL] {
			continue
		}
		if s.failures[item.URL].HalfOpenInFlight > 0 {
			continue
		}
		if bestIndex < 0 || degradedBefore(s.failures[item.URL], s.failures[s.upstreams[bestIndex].URL]) {
			bestIndex = index
		}
	}
	if bestIndex < 0 {
		return ""
	}
	s.degraded = true
	s.lastDegradedAt = now
	item := s.upstreams[bestIndex]
	state := s.failures[item.URL]
	state.HalfOpenInFlight++
	s.failures[item.URL] = state
	return item.URL
}

func degradedBefore(first gatewayUpstreamFailure, second gatewayUpstreamFailure) bool {
	if first.IsolatedUntil.IsZero() != second.IsolatedUntil.IsZero() {
		return first.IsolatedUntil.IsZero()
	}
	if !first.IsolatedUntil.Equal(second.IsolatedUntil) {
		return first.IsolatedUntil.Before(second.IsolatedUntil)
	}
	if first.Count != second.Count {
		return first.Count < second.Count
	}
	return first.SuccessEWMA > second.SuccessEWMA
}

func (s *gatewaySelector) isSelectableLocked(upstream string, now time.Time) bool {
	state, ok := s.failures[upstream]
	if !ok || state.Count < s.failureThreshold {
		return true
	}
	if now.Before(state.IsolatedUntil) {
		return false
	}
	return state.HalfOpenInFlight == 0
}

func (s *gatewaySelector) markSelectableLocked(upstream string, now time.Time) bool {
	if !s.isSelectableLocked(upstream, now) {
		return false
	}
	s.markHalfOpenSelectedLocked(upstream, now)
	return true
}

func (s *gatewaySelector) markHalfOpenSelectedLocked(upstream string, now time.Time) {
	state, ok := s.failures[upstream]
	if !ok || state.Count < s.failureThreshold || now.Before(state.IsolatedUntil) {
		return
	}
	state.HalfOpenInFlight++
	s.failures[upstream] = state
}

func (s *gatewaySelector) reportSuccess(upstream string, latency time.Duration) {
	if strings.TrimSpace(upstream) == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.failures[upstream]
	state.Samples++
	state.SuccessEWMA = ewma(state.SuccessEWMA, 1, state.Samples)
	if latency > 0 {
		state.LatencyEWMA = ewma(state.LatencyEWMA, float64(latency.Milliseconds()), state.Samples)
	}
	state.Count = 0
	state.IsolatedUntil = time.Time{}
	state.LastError = ""
	state.HalfOpenInFlight = 0
	s.failures[upstream] = state
}

func (s *gatewaySelector) reportFailure(upstream string, err error) {
	if strings.TrimSpace(upstream) == "" {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.failures[upstream]
	state.Samples++
	state.SuccessEWMA = ewma(state.SuccessEWMA, 0, state.Samples)
	state.Count++
	state.LastFailureAt = now
	state.HalfOpenInFlight = 0
	if err != nil {
		state.LastError = err.Error()
	}
	if state.Count >= s.failureThreshold {
		state.IsolatedUntil = now.Add(s.failureCooldown)
	}
	s.failures[upstream] = state
}

func ewma(previous float64, sample float64, samples int) float64 {
	if samples <= 1 {
		return sample
	}
	return gatewayRuntimeEWMAAlpha*sample + (1-gatewayRuntimeEWMAAlpha)*previous
}

func (s *gatewaySelector) snapshot(g *gatewayServer) gatewaySelectorSnapshot {
	s.ensureSnapshotRefresh(g)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	closed := 0
	open := 0
	halfOpen := 0
	for _, item := range s.upstreams {
		state := s.failures[item.URL]
		switch {
		case state.Count < s.failureThreshold:
			closed++
		case now.Before(state.IsolatedUntil):
			open++
		default:
			halfOpen++
		}
	}
	poolAge := int64(0)
	if !s.upstreamsLoadedAt.IsZero() {
		poolAge = now.Sub(s.upstreamsLoadedAt).Milliseconds()
	}
	return gatewaySelectorSnapshot{
		Loaded:                 len(s.upstreams),
		Active:                 closed + halfOpen,
		Skipped:                open,
		Closed:                 closed,
		Open:                   open,
		HalfOpen:               halfOpen,
		Degraded:               s.degraded,
		PoolGeneration:         s.poolGeneration,
		PoolAgeMS:              poolAge,
		LastRefreshAt:          formatBeijingTime(s.upstreamsLoadedAt),
		LastRefreshError:       s.lastRefreshError,
		Strategy:               s.strategy,
		RetryAttempts:          g.gatewayRetryAttempts(),
		FailureThreshold:       s.failureThreshold,
		FailureCooldownSeconds: int(s.failureCooldown / time.Second),
		LastAllReleasedAt:      formatBeijingTime(s.lastDegradedAt),
		LastConfigUpdateTime:   formatBeijingTime(s.lastConfigUpdateTime),
	}
}

func (s *gatewaySelector) ensureSnapshotRefresh(g *gatewayServer) {
	s.mu.Lock()
	hasPool := len(s.upstreams) > 0
	stale := !hasPool || time.Since(s.upstreamsLoadedAt) >= gatewayUpstreamRefreshInterval
	s.mu.Unlock()
	if !stale {
		return
	}
	if hasPool {
		s.startAsyncRefresh(g)
		return
	}
	_ = s.refresh(g, true)
}

func (s *gatewaySelector) dropMissingFailureStateLocked() {
	if len(s.failures) == 0 {
		return
	}
	seen := map[string]bool{}
	for _, item := range s.upstreams {
		seen[item.URL] = true
	}
	for item := range s.failures {
		if !seen[item] {
			delete(s.failures, item)
		}
	}
}

func upstreamLatency(item gatewayUpstream) int {
	if item.LatencyMS <= 0 {
		return 1 << 30
	}
	return item.LatencyMS
}

func (s *store) GatewayUpstreamCandidates(filter availableProxyFilter) ([]gatewayUpstream, error) {
	items, err := s.ExportAvailableFiltered(filter)
	if err != nil {
		return nil, err
	}
	out := make([]gatewayUpstream, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		proxyURL := item.ProxyURL()
		if seen[proxyURL] {
			continue
		}
		seen[proxyURL] = true
		latency := 0
		if item.LatencyMS != nil {
			latency = *item.LatencyMS
		}
		capability := ""
		if item.TargetState != nil {
			capability = item.TargetState.Capability
		}
		checkedAt := time.Time{}
		if item.LastCheckedAt != nil {
			checkedAt = parseScheduleTime(*item.LastCheckedAt)
		}
		out = append(out, gatewayUpstream{
			URL:           proxyURL,
			LatencyMS:     latency,
			SuccessRate:   item.SuccessRate,
			CheckedAt:     checkedAt,
			Capability:    capability,
			TargetProfile: item.TargetProfile,
		})
	}
	return out, nil
}
