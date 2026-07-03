package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type gatewayConfig struct {
	Host          string
	Port          int
	UpstreamLimit int
}

type gatewayServer struct {
	store *store
	cfg   gatewayConfig
	http  *http.Server
	mu    sync.Mutex
	index int

	totalRequests   int64
	successRequests int64
	failedRequests  int64
	lastUpstream    atomic.Value
	lastError       atomic.Value
	startedAt       string
}

func newGatewayServer(store *store, cfg gatewayConfig) *gatewayServer {
	gateway := &gatewayServer{store: store, cfg: cfg, startedAt: time.Now().UTC().Format(time.RFC3339)}
	gateway.http = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Handler:           gateway,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return gateway
}

func (g *gatewayServer) Start() error {
	log.Printf("starting local HTTP gateway on %s", g.http.Addr)
	return g.http.ListenAndServe()
}

func (g *gatewayServer) Status() map[string]any {
	total := atomic.LoadInt64(&g.totalRequests)
	success := atomic.LoadInt64(&g.successRequests)
	failed := atomic.LoadInt64(&g.failedRequests)
	successRate := 0.0
	if total > 0 {
		successRate = float64(success) / float64(total)
	}
	upstreamCount := 0
	if g.store != nil {
		items, err := g.store.AvailableProxyURLs(g.cfg.UpstreamLimit, "")
		if err == nil {
			upstreamCount = len(items)
		}
	}
	return map[string]any{
		"bind":             g.http.Addr,
		"upstreams":        upstreamCount,
		"total_requests":   total,
		"success_requests": success,
		"failed_requests":  failed,
		"success_rate":     successRate,
		"last_upstream":    valueString(g.lastUpstream.Load()),
		"last_error":       valueString(g.lastError.Load()),
		"started_at":       g.startedAt,
	}
}

func (g *gatewayServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&g.totalRequests, 1)
	if r.Method == http.MethodConnect {
		g.handleConnect(w, r)
		return
	}
	g.handleForward(w, r)
}

func (g *gatewayServer) handleForward(w http.ResponseWriter, r *http.Request) {
	upstream, err := g.selectUpstream()
	if err != nil {
		g.recordFailure("", err)
		errorResponse(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	client, _, err := proxyHTTPClient(upstream, 20)
	if err != nil {
		g.recordFailure(upstream, err)
		errorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	outReq.URL.Scheme = firstNonEmpty(outReq.URL.Scheme, "http")
	outReq.URL.Host = firstNonEmpty(outReq.URL.Host, r.Host)
	outReq.Header.Del("Proxy-Connection")
	outReq.Header.Del("Proxy-Authorization")
	resp, err := client.Do(outReq)
	if err != nil {
		g.recordFailure(upstream, err)
		errorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	g.recordSuccess(upstream)
}

func (g *gatewayServer) handleConnect(w http.ResponseWriter, r *http.Request) {
	upstream, err := g.selectUpstream()
	if err != nil {
		g.recordFailure("", err)
		errorResponse(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	target := r.Host
	if !strings.Contains(target, ":") {
		target = net.JoinHostPort(target, "443")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	upstreamConn, err := dialThroughProxy(ctx, upstream, target, 20*time.Second)
	if err != nil {
		g.recordFailure(upstream, err)
		errorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstreamConn.Close()
		g.recordFailure(upstream, fmt.Errorf("hijacking not supported"))
		errorResponse(w, http.StatusInternalServerError, "hijacking not supported")
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		_ = upstreamConn.Close()
		g.recordFailure(upstream, err)
		return
	}
	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	g.recordSuccess(upstream)
	pipeBidirectional(clientConn, upstreamConn)
}

func (g *gatewayServer) selectUpstream() (string, error) {
	if g.store == nil {
		return "", fmt.Errorf("store unavailable")
	}
	items, err := g.store.AvailableProxyURLs(g.cfg.UpstreamLimit, "")
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", fmt.Errorf("no available proxies; run a check first")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	item := items[g.index%len(items)]
	g.index++
	return item, nil
}

func (g *gatewayServer) recordSuccess(upstream string) {
	atomic.AddInt64(&g.successRequests, 1)
	g.lastUpstream.Store(maskProxyURL(upstream))
	g.lastError.Store("")
}

func (g *gatewayServer) recordFailure(upstream string, err error) {
	atomic.AddInt64(&g.failedRequests, 1)
	if upstream != "" {
		g.lastUpstream.Store(maskProxyURL(upstream))
	}
	if err != nil {
		g.lastError.Store(err.Error())
	}
}

func dialThroughProxy(ctx context.Context, proxyURL string, target string, timeout time.Duration) (net.Conn, error) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(parsed.Scheme) {
	case "socks5", "socks5h":
		dialer, err := newSocks5Dialer(parsed, timeout)
		if err != nil {
			return nil, err
		}
		return dialer.DialContext(ctx, "tcp", target)
	case "socks4":
		dialer, err := newSocks4Dialer(parsed, timeout)
		if err != nil {
			return nil, err
		}
		return dialer.DialContext(ctx, "tcp", target)
	case "http", "https", "":
		return dialThroughHTTPProxy(ctx, parsed, target, timeout)
	default:
		return nil, fmt.Errorf("unsupported gateway upstream scheme: %s", parsed.Scheme)
	}
}

func dialThroughHTTPProxy(ctx context.Context, proxy *url.URL, target string, timeout time.Duration) (net.Conn, error) {
	address := ensureProxyAddress(proxy)
	var conn net.Conn
	var err error
	dialer := &net.Dialer{Timeout: timeout}
	if strings.EqualFold(proxy.Scheme, "https") {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: proxy.Hostname(), MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: target},
		Host:   target,
		Header: http.Header{},
	}
	if proxy.User != nil {
		username := proxy.User.Username()
		password, _ := proxy.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(username+":"+password)))
	}
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.CopyN(io.Discard, resp.Body, 1024)
		_ = resp.Body.Close()
		_ = conn.Close()
		return nil, fmt.Errorf("upstream CONNECT returned HTTP %d", resp.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	if reader.Buffered() > 0 {
		return &bufferedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) {
	if c.reader != nil && c.reader.Buffered() > 0 {
		return c.reader.Read(p)
	}
	return c.Conn.Read(p)
}

func ensureProxyAddress(proxy *url.URL) string {
	host := proxy.Host
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	port := "80"
	if strings.EqualFold(proxy.Scheme, "https") {
		port = "443"
	}
	return net.JoinHostPort(proxy.Hostname(), port)
}

func pipeBidirectional(left net.Conn, right net.Conn) {
	var once sync.Once
	closeBoth := func() {
		_ = left.Close()
		_ = right.Close()
	}
	go func() {
		_, _ = io.Copy(left, right)
		once.Do(closeBoth)
	}()
	go func() {
		_, _ = io.Copy(right, left)
		once.Do(closeBoth)
	}()
}

func copyHeader(dst http.Header, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func maskProxyURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User == nil {
		return value
	}
	parsed.User = url.UserPassword(parsed.User.Username(), "***")
	return parsed.String()
}

func valueString(value any) string {
	if value == nil {
		return ""
	}
	text, _ := value.(string)
	return text
}
