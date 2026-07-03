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
	Host            string
	Port            int
	Socks5Enabled   bool
	Socks5Host      string
	Socks5Port      int
	UpstreamLimit   int
	RequestTimeoutS int
}

type gatewayServer struct {
	store           *store
	cfg             gatewayConfig
	http            *http.Server
	socks5Listener  net.Listener
	mu              sync.Mutex
	index           int
	recentUpstreams []string

	totalRequests   int64
	successRequests int64
	failedRequests  int64
	lastUpstream    atomic.Value
	lastError       atomic.Value
	startedAt       string
}

func newGatewayServer(store *store, cfg gatewayConfig) *gatewayServer {
	if cfg.RequestTimeoutS <= 0 {
		cfg.RequestTimeoutS = 20
	}
	gateway := &gatewayServer{store: store, cfg: cfg, startedAt: time.Now().UTC().Format(time.RFC3339)}
	gateway.http = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Handler:           gateway,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return gateway
}

func (g *gatewayServer) Start() error {
	if g.cfg.Socks5Enabled {
		go func() {
			if err := g.startSocks5Gateway(); err != nil {
				log.Printf("SOCKS5 gateway stopped: %v", err)
			}
		}()
	}
	log.Printf("starting HTTP gateway on %s", g.http.Addr)
	return g.http.ListenAndServe()
}

func (g *gatewayServer) startSocks5Gateway() error {
	addr := fmt.Sprintf("%s:%d", g.cfg.Socks5Host, g.cfg.Socks5Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	g.socks5Listener = listener
	log.Printf("starting SOCKS5 gateway on %s", addr)
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go g.handleSocks5Conn(conn)
	}
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
	targetProfile := g.targetProfile()
	if g.store != nil {
		items, err := g.store.AvailableProxyURLs(g.cfg.UpstreamLimit, targetProfile)
		if err == nil {
			upstreamCount = len(items)
		}
	}
	return map[string]any{
		"bind":             g.http.Addr,
		"http_bind":        g.http.Addr,
		"http_host":        g.cfg.Host,
		"http_port":        g.cfg.Port,
		"socks5_enabled":   g.cfg.Socks5Enabled,
		"socks5_bind":      fmt.Sprintf("%s:%d", g.cfg.Socks5Host, g.cfg.Socks5Port),
		"socks5_host":      g.cfg.Socks5Host,
		"socks5_port":      g.cfg.Socks5Port,
		"target_profile":   targetProfile,
		"upstreams":        upstreamCount,
		"total_requests":   total,
		"success_requests": success,
		"failed_requests":  failed,
		"success_rate":     successRate,
		"last_upstream":    valueString(g.lastUpstream.Load()),
		"recent_upstreams": g.recentSnapshot(),
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
	client, _, err := proxyHTTPClient(upstream, g.cfg.RequestTimeoutS)
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
	upstreamConn, err := dialThroughProxy(ctx, upstream, target, time.Duration(g.cfg.RequestTimeoutS)*time.Second)
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
	items, err := g.store.AvailableProxyURLs(g.cfg.UpstreamLimit, g.targetProfile())
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
	g.rememberUpstreamLocked(item)
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

func (g *gatewayServer) targetProfile() string {
	if g.store == nil {
		return "generic"
	}
	settings, err := g.store.AppSettings()
	if err != nil {
		return "generic"
	}
	return normalizeTargetProfileOrAll(settings.GatewayTargetProfile, "generic")
}

func (g *gatewayServer) rememberUpstreamLocked(upstream string) {
	masked := maskProxyURL(upstream)
	g.lastUpstream.Store(masked)
	if len(g.recentUpstreams) == 0 || g.recentUpstreams[len(g.recentUpstreams)-1] != masked {
		g.recentUpstreams = append(g.recentUpstreams, masked)
	}
	if len(g.recentUpstreams) > 8 {
		g.recentUpstreams = append([]string{}, g.recentUpstreams[len(g.recentUpstreams)-8:]...)
	}
}

func (g *gatewayServer) recentSnapshot() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, len(g.recentUpstreams))
	copy(out, g.recentUpstreams)
	return out
}

func (g *gatewayServer) handleSocks5Conn(client net.Conn) {
	atomic.AddInt64(&g.totalRequests, 1)
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))
	target, err := socks5Handshake(client)
	if err != nil {
		_ = client.Close()
		g.recordFailure("", err)
		return
	}
	upstream, err := g.selectUpstream()
	if err != nil {
		_ = writeSocks5Reply(client, 0x01)
		_ = client.Close()
		g.recordFailure("", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(g.cfg.RequestTimeoutS)*time.Second)
	defer cancel()
	upstreamConn, err := dialThroughProxy(ctx, upstream, target, time.Duration(g.cfg.RequestTimeoutS)*time.Second)
	if err != nil {
		_ = writeSocks5Reply(client, 0x05)
		_ = client.Close()
		g.recordFailure(upstream, err)
		return
	}
	if err := writeSocks5Reply(client, 0x00); err != nil {
		_ = client.Close()
		_ = upstreamConn.Close()
		g.recordFailure(upstream, err)
		return
	}
	_ = client.SetDeadline(time.Time{})
	g.recordSuccess(upstream)
	pipeBidirectional(client, upstreamConn)
}

func socks5Handshake(conn net.Conn) (string, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", err
	}
	if header[0] != 0x05 {
		return "", fmt.Errorf("unsupported SOCKS version: %d", header[0])
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", err
	}
	noAuth := false
	for _, method := range methods {
		if method == 0x00 {
			noAuth = true
			break
		}
	}
	if !noAuth {
		_, _ = conn.Write([]byte{0x05, 0xff})
		return "", fmt.Errorf("SOCKS5 no-auth method not offered")
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return "", err
	}
	requestHeader := make([]byte, 4)
	if _, err := io.ReadFull(conn, requestHeader); err != nil {
		return "", err
	}
	if requestHeader[0] != 0x05 {
		return "", fmt.Errorf("invalid SOCKS5 request version: %d", requestHeader[0])
	}
	if requestHeader[1] != 0x01 {
		_ = writeSocks5Reply(conn, 0x07)
		return "", fmt.Errorf("unsupported SOCKS5 command: %d", requestHeader[1])
	}
	host, err := readSocks5Address(conn, requestHeader[3])
	if err != nil {
		_ = writeSocks5Reply(conn, 0x08)
		return "", err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return "", err
	}
	port := int(portBytes[0])<<8 | int(portBytes[1])
	if port < 1 || port > 65535 {
		_ = writeSocks5Reply(conn, 0x08)
		return "", fmt.Errorf("invalid SOCKS5 target port")
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port)), nil
}

func readSocks5Address(conn net.Conn, atyp byte) (string, error) {
	switch atyp {
	case 0x01:
		raw := make([]byte, 4)
		if _, err := io.ReadFull(conn, raw); err != nil {
			return "", err
		}
		return net.IP(raw).String(), nil
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return "", err
		}
		if length[0] == 0 {
			return "", fmt.Errorf("empty SOCKS5 domain")
		}
		raw := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, raw); err != nil {
			return "", err
		}
		return string(raw), nil
	case 0x04:
		raw := make([]byte, 16)
		if _, err := io.ReadFull(conn, raw); err != nil {
			return "", err
		}
		return net.IP(raw).String(), nil
	default:
		return "", fmt.Errorf("unsupported SOCKS5 address type: %d", atyp)
	}
}

func writeSocks5Reply(conn net.Conn, reply byte) error {
	_, err := conn.Write([]byte{0x05, reply, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return err
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
