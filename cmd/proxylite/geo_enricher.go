package main

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/RY-zzcn/ProxyLiteChecker/internal/checkmeta"
)

type geoEnricherConfig struct {
	QueueSize    int
	RateInterval time.Duration
	CacheTTL     time.Duration
	RetryDelay   time.Duration
	Timeout      time.Duration
	Endpoint     string
}

type geoEnricher struct {
	store      *store
	client     *http.Client
	endpoint   string
	queue      chan string
	stop       chan struct{}
	done       chan struct{}
	mu         sync.Mutex
	pending    map[string]bool
	lastRemote time.Time
	now        func() time.Time
	rate       time.Duration
	ttl        time.Duration
	retry      time.Duration
	timeout    time.Duration
	lookup     func(context.Context, *http.Client, string, string) (checkmeta.Metadata, bool)
	local      func(string) (checkmeta.Metadata, bool)
	startOnce  sync.Once
	stopOnce   sync.Once
}

func newGeoEnricher(st *store) *geoEnricher {
	return newGeoEnricherWithConfig(st, geoEnricherConfig{
		QueueSize:    clampInt(envInt("PLC_IP_METADATA_QUEUE_SIZE", 128), 8, 4096),
		RateInterval: time.Duration(clampInt(envInt("PLC_IP_METADATA_INTERVAL_MS", 2000), 250, 60000)) * time.Millisecond,
		CacheTTL:     time.Duration(clampInt(envInt("PLC_IP_METADATA_TTL_HOURS", 168), 1, 24*365)) * time.Hour,
		RetryDelay:   time.Duration(clampInt(envInt("PLC_IP_METADATA_RETRY_MINUTES", 30), 1, 24*60)) * time.Minute,
		Timeout:      time.Duration(clampInt(envInt("PLC_IP_METADATA_TIMEOUT_SECONDS", 5), 1, 30)) * time.Second,
		Endpoint:     strings.TrimSpace(envString("PLC_IP_METADATA_ENDPOINT", "")),
	})
}

func newGeoEnricherWithConfig(st *store, cfg geoEnricherConfig) *geoEnricher {
	queueSize := maxInt(1, cfg.QueueSize)
	if cfg.CacheTTL <= 0 {
		cfg.CacheTTL = 7 * 24 * time.Hour
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = 30 * time.Minute
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	return &geoEnricher{
		store:    st,
		client:   &http.Client{Timeout: cfg.Timeout},
		endpoint: cfg.Endpoint,
		queue:    make(chan string, queueSize),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		pending:  map[string]bool{},
		now:      time.Now,
		rate:     maxDuration(0, cfg.RateInterval),
		ttl:      cfg.CacheTTL,
		retry:    cfg.RetryDelay,
		timeout:  cfg.Timeout,
		lookup:   checkmeta.LookupIPMetadata,
		local:    checkmeta.LookupGeoIP,
	}
}

func (g *geoEnricher) Start() {
	if g == nil {
		return
	}
	g.startOnce.Do(func() { go g.run() })
}

func (g *geoEnricher) Stop() {
	if g == nil {
		return
	}
	g.stopOnce.Do(func() { close(g.stop) })
	select {
	case <-g.done:
	case <-time.After(2 * time.Second):
	}
}

func (g *geoEnricher) LookupAndQueue(ctx context.Context, ip string) checkmeta.Metadata {
	ip = strings.TrimSpace(ip)
	metadata := checkmeta.Metadata{IPType: checkmeta.ClassifyIPType(ip)}
	if ip == "" {
		return metadata
	}
	now := g.now()
	entry, found, err := g.store.IPGeoCache(ip)
	if err == nil && found {
		metadata = mergeIPMetadata(metadata, entry.Metadata)
	}
	local, localOK := g.local(ip)
	if localOK {
		metadata = mergeIPMetadata(metadata, local)
	}
	fresh := found && !entry.ExpiresAt.IsZero() && now.Before(entry.ExpiresAt)
	retryBlocked := found && !entry.RetryAfter.IsZero() && now.Before(entry.RetryAfter)
	if !fresh && !retryBlocked && ctx.Err() == nil {
		g.Enqueue(ip)
	}
	return metadata
}

func (g *geoEnricher) Enqueue(ip string) bool {
	if g == nil {
		return false
	}
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return false
	}
	g.mu.Lock()
	if g.pending[ip] {
		g.mu.Unlock()
		return true
	}
	g.pending[ip] = true
	g.mu.Unlock()
	select {
	case g.queue <- ip:
		return true
	default:
		g.mu.Lock()
		delete(g.pending, ip)
		g.mu.Unlock()
		return false
	}
}

func (g *geoEnricher) run() {
	defer close(g.done)
	for {
		select {
		case <-g.stop:
			return
		case ip := <-g.queue:
			g.process(ip)
			g.mu.Lock()
			delete(g.pending, ip)
			g.mu.Unlock()
		}
	}
}

func (g *geoEnricher) process(ip string) {
	now := g.now()
	entry, found, err := g.store.IPGeoCache(ip)
	if err == nil && found {
		if !entry.ExpiresAt.IsZero() && now.Before(entry.ExpiresAt) {
			return
		}
		if !entry.RetryAfter.IsZero() && now.Before(entry.RetryAfter) {
			return
		}
	}
	if !g.waitRateLimit() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.timeout)
	remote, ok := g.lookup(ctx, g.client, g.endpoint, ip)
	cancel()
	now = g.now()
	if !ok {
		_ = g.store.SaveIPGeoFailure(ip, "external metadata lookup failed", now.Add(g.retry))
		return
	}
	metadata := checkmeta.Metadata{IPType: checkmeta.ClassifyIPType(ip)}
	metadata = mergeIPMetadata(metadata, remote)
	if local, localOK := g.local(ip); localOK {
		metadata = mergeIPMetadata(metadata, local)
		metadata.GeoSource = "merged"
	}
	metadata.GeoUpdatedAt = now
	_ = g.store.SaveIPGeoSuccess(ip, metadata, now, now.Add(g.ttl))
}

func (g *geoEnricher) waitRateLimit() bool {
	if g.rate <= 0 {
		return true
	}
	g.mu.Lock()
	wait := g.rate - g.now().Sub(g.lastRemote)
	g.mu.Unlock()
	if wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-g.stop:
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	g.mu.Lock()
	g.lastRemote = g.now()
	g.mu.Unlock()
	return true
}

func maxDuration(first time.Duration, second time.Duration) time.Duration {
	if first > second {
		return first
	}
	return second
}
