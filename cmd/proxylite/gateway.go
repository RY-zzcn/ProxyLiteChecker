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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type gatewayConfig struct {
	Host               string
	Port               int
	Socks5Enabled      bool
	Socks5Host         string
	Socks5Port         int
	UpstreamLimit      int
	RequestTimeoutS    int
	TargetProfiles     []string
	HTTPProfilePorts   map[string]int
	Socks5ProfilePorts map[string]int
	ProfilePortStride  int
}

type gatewayServer struct {
	store     *store
	cfg       gatewayConfig
	endpoints []*gatewayEndpoint
	startedAt string
}

const gatewayRecentLimit = 5

type gatewayEndpoint struct {
	TargetProfile   string
	HTTPHost        string
	HTTPPort        int
	Socks5Host      string
	Socks5Port      int
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
}

func newGatewayServer(store *store, cfg gatewayConfig) *gatewayServer {
	if cfg.RequestTimeoutS <= 0 {
		cfg.RequestTimeoutS = 20
	}
	if cfg.ProfilePortStride <= 0 {
		cfg.ProfilePortStride = 2
	}
	profiles := normalizeTargetProfiles(cfg.TargetProfiles)
	gateway := &gatewayServer{store: store, cfg: cfg, startedAt: nowString()}
	for index, profile := range profiles {
		endpoint := &gatewayEndpoint{
			TargetProfile: profile,
			HTTPHost:      cfg.Host,
			HTTPPort:      gatewayProfilePortForProfile(profile, cfg.Port, index, cfg.HTTPProfilePorts, cfg.ProfilePortStride),
			Socks5Host:    cfg.Socks5Host,
			Socks5Port:    gatewayProfilePortForProfile(profile, cfg.Socks5Port, index, cfg.Socks5ProfilePorts, cfg.ProfilePortStride),
		}
		if !cfg.Socks5Enabled {
			endpoint.Socks5Port = 0
		}
		gateway.endpoints = append(gateway.endpoints, endpoint)
	}
	return gateway
}

func (g *gatewayServer) Start() error {
	if len(g.endpoints) == 0 {
		return nil
	}
	for _, endpoint := range g.endpoints {
		if endpoint.HTTPPort > 0 {
			if err := g.startHTTPGateway(endpoint); err != nil {
				log.Printf("HTTP gateway target=%s disabled: %v", endpoint.TargetProfile, err)
			}
		}
		if endpoint.Socks5Port > 0 {
			if err := g.startSocks5Gateway(endpoint); err != nil {
				log.Printf("SOCKS5 gateway target=%s disabled: %v", endpoint.TargetProfile, err)
			}
		}
	}
	return nil
}

func (g *gatewayServer) startHTTPGateway(endpoint *gatewayEndpoint) error {
	addr := net.JoinHostPort(endpoint.HTTPHost, strconv.Itoa(endpoint.HTTPPort))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	endpoint.http = &http.Server{
		Handler:           gatewayHTTPHandler{gateway: g, endpoint: endpoint},
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("starting HTTP gateway target=%s on %s", endpoint.TargetProfile, listener.Addr())
	go func() {
		if err := endpoint.http.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP gateway target=%s stopped: %v", endpoint.TargetProfile, err)
		}
	}()
	return nil
}

func (g *gatewayServer) startSocks5Gateway(endpoint *gatewayEndpoint) error {
	addr := net.JoinHostPort(endpoint.Socks5Host, strconv.Itoa(endpoint.Socks5Port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	endpoint.socks5Listener = listener
	log.Printf("starting SOCKS5 gateway target=%s on %s", endpoint.TargetProfile, listener.Addr())
	go g.serveSocks5Gateway(endpoint, listener)
	return nil
}

func (g *gatewayServer) serveSocks5Gateway(endpoint *gatewayEndpoint, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("SOCKS5 gateway target=%s stopped: %v", endpoint.TargetProfile, err)
			return
		}
		go g.handleSocks5Conn(conn, endpoint)
	}
}

func (g *gatewayServer) Status() map[string]any {
	profiles := make([]map[string]any, 0, len(g.endpoints))
	total := int64(0)
	success := int64(0)
	failed := int64(0)
	upstreamCount := 0
	targetAvailableUpstreamCount := 0
	targetProfiles := make([]string, 0, len(g.endpoints))
	for _, endpoint := range g.endpoints {
		item := g.endpointStatus(endpoint)
		profiles = append(profiles, item)
		targetProfiles = append(targetProfiles, endpoint.TargetProfile)
		total += atomic.LoadInt64(&endpoint.totalRequests)
		success += atomic.LoadInt64(&endpoint.successRequests)
		failed += atomic.LoadInt64(&endpoint.failedRequests)
		upstreamCount += anyToInt(item["upstreams"])
		targetAvailableUpstreamCount += anyToInt(item["available_upstreams"])
	}
	uniqueAvailableUpstreamCount := targetAvailableUpstreamCount
	if g.store != nil {
		if count, err := g.store.CountAvailableProxyURLsForProfiles(targetProfiles); err == nil {
			uniqueAvailableUpstreamCount = count
		}
	}
	successRate := 0.0
	if total > 0 {
		successRate = float64(success) / float64(total)
	}
	primary := map[string]any{}
	if len(profiles) > 0 {
		primary = profiles[0]
	}
	return map[string]any{
		"bind":                       primary["http_bind"],
		"http_bind":                  primary["http_bind"],
		"http_host":                  primary["http_host"],
		"http_port":                  primary["http_port"],
		"socks5_enabled":             g.cfg.Socks5Enabled,
		"socks5_bind":                primary["socks5_bind"],
		"socks5_host":                primary["socks5_host"],
		"socks5_port":                primary["socks5_port"],
		"target_profile":             primary["target_profile"],
		"upstreams":                  upstreamCount,
		"loaded_upstreams":           upstreamCount,
		"available_upstreams":        uniqueAvailableUpstreamCount,
		"unique_available_upstreams": uniqueAvailableUpstreamCount,
		"target_available_upstreams": targetAvailableUpstreamCount,
		"upstream_limit":             g.cfg.UpstreamLimit,
		"total_requests":             total,
		"success_requests":           success,
		"failed_requests":            failed,
		"success_rate":               successRate,
		"last_upstream":              primary["last_upstream"],
		"recent_upstreams":           primary["recent_upstreams"],
		"last_error":                 primary["last_error"],
		"profiles":                   profiles,
		"started_at":                 g.startedAt,
	}
}

func (g *gatewayServer) endpointStatus(endpoint *gatewayEndpoint) map[string]any {
	total := atomic.LoadInt64(&endpoint.totalRequests)
	success := atomic.LoadInt64(&endpoint.successRequests)
	failed := atomic.LoadInt64(&endpoint.failedRequests)
	successRate := 0.0
	if total > 0 {
		successRate = float64(success) / float64(total)
	}
	upstreamCount := 0
	availableUpstreamCount := 0
	if g.store != nil {
		items, err := g.store.AvailableProxyURLs(g.cfg.UpstreamLimit, endpoint.TargetProfile)
		if err == nil {
			upstreamCount = len(items)
		}
		total, err := g.store.CountAvailableProxyURLs(endpoint.TargetProfile)
		if err == nil {
			availableUpstreamCount = total
		} else {
			availableUpstreamCount = upstreamCount
		}
	}
	return map[string]any{
		"target_profile":      endpoint.TargetProfile,
		"label":               targetProfileLabel(endpoint.TargetProfile),
		"http_enabled":        endpoint.HTTPPort > 0 && endpoint.http != nil,
		"http_bind":           net.JoinHostPort(endpoint.HTTPHost, strconv.Itoa(endpoint.HTTPPort)),
		"http_host":           endpoint.HTTPHost,
		"http_port":           endpoint.HTTPPort,
		"socks5_enabled":      endpoint.Socks5Port > 0 && endpoint.socks5Listener != nil,
		"socks5_bind":         net.JoinHostPort(endpoint.Socks5Host, strconv.Itoa(endpoint.Socks5Port)),
		"socks5_host":         endpoint.Socks5Host,
		"socks5_port":         endpoint.Socks5Port,
		"upstreams":           upstreamCount,
		"loaded_upstreams":    upstreamCount,
		"available_upstreams": availableUpstreamCount,
		"upstream_limit":      g.cfg.UpstreamLimit,
		"upstream_limited":    availableUpstreamCount > upstreamCount,
		"total_requests":      total,
		"success_requests":    success,
		"failed_requests":     failed,
		"success_rate":        successRate,
		"last_upstream":       valueString(endpoint.lastUpstream.Load()),
		"recent_upstreams":    endpoint.recentSnapshot(),
		"last_error":          valueString(endpoint.lastError.Load()),
	}
}

type gatewayHTTPHandler struct {
	gateway  *gatewayServer
	endpoint *gatewayEndpoint
}

func (h gatewayHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	atomic.AddInt64(&h.endpoint.totalRequests, 1)
	if r.Method == http.MethodConnect {
		h.gateway.handleConnect(w, r, h.endpoint)
		return
	}
	h.gateway.handleForward(w, r, h.endpoint)
}

func (g *gatewayServer) handleForward(w http.ResponseWriter, r *http.Request, endpoint *gatewayEndpoint) {
	upstream, err := g.selectUpstream(endpoint)
	if err != nil {
		endpoint.recordFailure("", err)
		errorResponse(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	client, _, err := proxyHTTPClient(upstream, g.cfg.RequestTimeoutS)
	if err != nil {
		endpoint.recordFailure(upstream, err)
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
		endpoint.recordFailure(upstream, err)
		errorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	endpoint.recordSuccess(upstream)
}

func (g *gatewayServer) handleConnect(w http.ResponseWriter, r *http.Request, endpoint *gatewayEndpoint) {
	upstream, err := g.selectUpstream(endpoint)
	if err != nil {
		endpoint.recordFailure("", err)
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
		endpoint.recordFailure(upstream, err)
		errorResponse(w, http.StatusBadGateway, err.Error())
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstreamConn.Close()
		endpoint.recordFailure(upstream, fmt.Errorf("hijacking not supported"))
		errorResponse(w, http.StatusInternalServerError, "hijacking not supported")
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		_ = upstreamConn.Close()
		endpoint.recordFailure(upstream, err)
		return
	}
	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	endpoint.recordSuccess(upstream)
	pipeBidirectional(clientConn, upstreamConn)
}

func (g *gatewayServer) selectUpstream(endpoint *gatewayEndpoint) (string, error) {
	if g.store == nil {
		return "", fmt.Errorf("store unavailable")
	}
	items, err := g.store.AvailableProxyURLs(g.cfg.UpstreamLimit, endpoint.TargetProfile)
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", fmt.Errorf("no available %s proxies; run a check first", targetProfileLabel(endpoint.TargetProfile))
	}
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	item := items[endpoint.index%len(items)]
	endpoint.index++
	endpoint.rememberUpstreamLocked(item)
	return item, nil
}

func (e *gatewayEndpoint) recordSuccess(upstream string) {
	atomic.AddInt64(&e.successRequests, 1)
	e.lastUpstream.Store(maskProxyURL(upstream))
	e.lastError.Store("")
}

func (e *gatewayEndpoint) recordFailure(upstream string, err error) {
	atomic.AddInt64(&e.failedRequests, 1)
	if upstream != "" {
		e.lastUpstream.Store(maskProxyURL(upstream))
	}
	if err != nil {
		e.lastError.Store(err.Error())
	}
}

func (e *gatewayEndpoint) rememberUpstreamLocked(upstream string) {
	masked := maskProxyURL(upstream)
	e.lastUpstream.Store(masked)
	if len(e.recentUpstreams) == 0 || e.recentUpstreams[len(e.recentUpstreams)-1] != masked {
		e.recentUpstreams = append(e.recentUpstreams, masked)
	}
	if len(e.recentUpstreams) > gatewayRecentLimit {
		e.recentUpstreams = append([]string{}, e.recentUpstreams[len(e.recentUpstreams)-gatewayRecentLimit:]...)
	}
}

func (e *gatewayEndpoint) recentSnapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.recentUpstreams))
	for index := len(e.recentUpstreams) - 1; index >= 0; index-- {
		out = append(out, e.recentUpstreams[index])
	}
	return out
}

func (g *gatewayServer) handleSocks5Conn(client net.Conn, endpoint *gatewayEndpoint) {
	atomic.AddInt64(&endpoint.totalRequests, 1)
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))
	target, err := socks5Handshake(client)
	if err != nil {
		_ = client.Close()
		endpoint.recordFailure("", err)
		return
	}
	upstream, err := g.selectUpstream(endpoint)
	if err != nil {
		_ = writeSocks5Reply(client, 0x01)
		_ = client.Close()
		endpoint.recordFailure("", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(g.cfg.RequestTimeoutS)*time.Second)
	defer cancel()
	upstreamConn, err := dialThroughProxy(ctx, upstream, target, time.Duration(g.cfg.RequestTimeoutS)*time.Second)
	if err != nil {
		_ = writeSocks5Reply(client, 0x05)
		_ = client.Close()
		endpoint.recordFailure(upstream, err)
		return
	}
	if err := writeSocks5Reply(client, 0x00); err != nil {
		_ = client.Close()
		_ = upstreamConn.Close()
		endpoint.recordFailure(upstream, err)
		return
	}
	_ = client.SetDeadline(time.Time{})
	endpoint.recordSuccess(upstream)
	pipeBidirectional(client, upstreamConn)
}

func gatewayProfilePortForProfile(profile string, base int, index int, profilePorts map[string]int, stride int) int {
	if port := profilePorts[strings.ToLower(strings.TrimSpace(profile))]; port > 0 {
		return port
	}
	if base <= 0 {
		return 0
	}
	port := base + index*stride
	if port > 65535 {
		return 0
	}
	return port
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
