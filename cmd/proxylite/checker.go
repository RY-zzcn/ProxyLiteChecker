package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RY-zzcn/ProxyLiteChecker/internal/checkmeta"
)

type CheckConfig struct {
	TargetProfile  string
	Limit          int
	Concurrent     int
	Rounds         int
	RequestTimeout int
	HardTimeout    int
	DeleteFailed   bool
}

type ProxyTask struct {
	ID       int     `json:"id"`
	Proxy    string  `json:"proxy"`
	IP       string  `json:"ip"`
	Port     int     `json:"port"`
	Protocol string  `json:"protocol"`
	Username *string `json:"username,omitempty"`
	Password *string `json:"-"`
	Source   string  `json:"source"`
}

type CheckResult struct {
	ProxyID          int     `json:"proxy_id"`
	Status           string  `json:"status"`
	Grade            string  `json:"grade"`
	LatencyMS        *int    `json:"latency_ms,omitempty"`
	ExitIP           *string `json:"exit_ip,omitempty"`
	Country          *string `json:"country,omitempty"`
	IPType           *string `json:"ip_type,omitempty"`
	ASNOrg           *string `json:"asn_org,omitempty"`
	SuccessRate      float64 `json:"success_rate"`
	TargetProfile    string  `json:"target_profile"`
	DetectedProtocol *string `json:"detected_protocol,omitempty"`
	ServiceReachable bool    `json:"service_reachable"`
	APIReachable     *bool   `json:"api_reachable,omitempty"`
	CloudflareStatus *string `json:"cloudflare_status,omitempty"`
	RecommendedUse   string  `json:"recommended_use"`
	LastError        *string `json:"last_error,omitempty"`
}

type TargetProfile struct {
	ServiceURL       string            `json:"service_url"`
	APIURL           string            `json:"api_url"`
	ExpectedStatuses []int             `json:"expected_statuses,omitempty"`
	Keyword          string            `json:"keyword,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	TimeoutSeconds   int               `json:"timeout_seconds,omitempty"`
}

type singleCheckResult struct {
	CheckResult
	BaseReachable bool
}

type checkPlan struct {
	TargetProfile string
	Items         []ProxyTask
}

var targetProfileOrder = []string{"generic", "openai", "grok", "gemini", "claude"}

var targetProfiles = map[string]TargetProfile{
	"generic": {ServiceURL: "https://example.com/"},
	"openai":  {ServiceURL: "https://chat.openai.com/", APIURL: "https://api.openai.com/v1/models"},
	"grok":    {ServiceURL: "https://grok.com/", APIURL: "https://api.x.ai/v1/models"},
	"gemini":  {ServiceURL: "https://gemini.google.com/", APIURL: "https://generativelanguage.googleapis.com/v1beta/models"},
	"claude":  {ServiceURL: "https://claude.ai/", APIURL: "https://api.anthropic.com/v1/models"},
}

func (s *server) StartCheckJob(payload map[string]any) (map[string]any, error) {
	if running := s.jobs.RunningOfTypes("fetch", "check"); running != nil {
		return nil, runningJobConflict(running)
	}
	settings, err := s.store.AppSettings()
	if err != nil {
		return nil, err
	}
	if _, _, err := s.applyProxyMaintenance(settings); err != nil {
		return nil, err
	}
	fallbackProfiles := settings.CheckTargetProfiles
	if len(fallbackProfiles) == 0 {
		fallbackProfiles = []string{settings.CheckTargetProfile}
	}
	targetProfiles := targetProfilesFromPayload(payload, fallbackProfiles)
	cfg := CheckConfig{
		Limit:          optionalLimit(payload["limit"], 200, 100000),
		Concurrent:     optionalLimit(payload["concurrent"], 30, 300),
		Rounds:         optionalLimit(payload["rounds"], 1, 5),
		RequestTimeout: optionalLimit(payload["request_timeout"], 6, 60),
		HardTimeout:    optionalLimit(payload["hard_timeout"], 60, 300),
		DeleteFailed:   settings.DeleteFailedOnCheck,
	}
	status := optionalString(payload["status"], "untested")
	plans := make([]checkPlan, 0, len(targetProfiles))
	total := 0
	for _, profile := range targetProfiles {
		items, err := s.store.ListCheckCandidates(status, cfg.Limit, profile)
		if err != nil {
			return nil, err
		}
		plans = append(plans, checkPlan{TargetProfile: profile, Items: items})
		total += len(items)
	}
	job, ctx := s.jobs.Create("check", "准备本机检测")
	s.jobs.Update(job.ID, map[string]any{
		"total":   total,
		"message": fmt.Sprintf("本机检测排队：%d 条 / %d 个目标", total, len(targetProfiles)),
	})
	go s.runCheckJob(ctx, job.ID, plans, cfg)
	return map[string]any{"job_id": job.ID, "count": total, "target_profiles": targetProfiles}, nil
}

func (s *server) runCheckJob(ctx context.Context, jobID string, plans []checkPlan, cfg CheckConfig) {
	total := totalCheckPlanItems(plans)
	if total == 0 {
		s.jobs.complete(jobID, "没有符合条件的代理需要检测", map[string]any{"checked": 0})
		return
	}
	done := 0
	available := 0
	failed := 0
	deletedFailed := 0
	deletedIDs := map[int]bool{}
	for _, plan := range plans {
		if ctx.Err() != nil {
			break
		}
		items := filterDeletedCheckItems(plan.Items, deletedIDs)
		if len(items) == 0 {
			continue
		}
		planCfg := cfg
		planCfg.TargetProfile = plan.TargetProfile
		for result := range checkBatchStream(ctx, items, planCfg) {
			if result.Status == "failed" && planCfg.DeleteFailed {
				if err := s.store.DeleteProxyByID(result.ProxyID); err != nil {
					failed++
				} else {
					failed++
					deletedFailed++
					deletedIDs[result.ProxyID] = true
				}
			} else if err := s.store.SaveCheckResult(result); err != nil {
				failed++
			} else if result.Status == "available" {
				available++
			} else {
				failed++
			}
			done++
			if done == 1 || done%10 == 0 || done == total {
				s.jobs.Update(jobID, map[string]any{
					"done":    done,
					"total":   total,
					"success": available,
					"failed":  failed,
					"message": checkProgressMessage(done, total, plan.TargetProfile, available, failed, deletedFailed, cfg.DeleteFailed),
				})
			}
		}
	}
	if ctx.Err() != nil {
		s.jobs.finishCancelled(jobID, "本机检测已停止")
		return
	}
	result := map[string]any{"checked": done, "available": available, "failed": failed, "deleted_failed": deletedFailed, "target_profiles": checkPlanProfiles(plans)}
	s.jobs.Update(jobID, map[string]any{"done": done, "total": total, "success": available, "failed": failed})
	s.jobs.complete(jobID, checkCompleteMessage(available, failed, deletedFailed, cfg.DeleteFailed), result)
}

func checkProgressMessage(done int, total int, targetProfile string, available int, failed int, deletedFailed int, deleteFailed bool) string {
	targetText := targetProfileLabel(targetProfile)
	if deleteFailed {
		return fmt.Sprintf("本机检测进行中：%d/%d，目标 %s，可用 %d，失败删除 %d", done, total, targetText, available, deletedFailed)
	}
	return fmt.Sprintf("本机检测进行中：%d/%d，目标 %s，可用 %d，失败 %d", done, total, targetText, available, failed)
}

func checkCompleteMessage(available int, failed int, deletedFailed int, deleteFailed bool) string {
	if deleteFailed {
		return fmt.Sprintf("检测完成：可用 %d，失败删除 %d", available, deletedFailed)
	}
	return fmt.Sprintf("检测完成：可用 %d，失败 %d", available, failed)
}

func normalizeTargetProfile(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if _, ok := targetProfiles[value]; ok {
		return value
	}
	return "generic"
}

func normalizeTargetProfileOrAll(value string, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "all" {
		return "all"
	}
	if _, ok := targetProfiles[value]; ok {
		return value
	}
	return normalizeTargetProfile(fallback)
}

func normalizeTargetProfiles(values []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "all" {
			return append([]string{}, targetProfileOrder...)
		}
		if _, ok := targetProfiles[value]; ok && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return []string{"generic"}
	}
	return out
}

func normalizeTargetProfilesOrNil(value string) []string {
	values := anyToStringSlice(value)
	if len(values) == 0 {
		return nil
	}
	return normalizeTargetProfiles(values)
}

func targetProfilesFromPayload(payload map[string]any, fallback []string) []string {
	values := anyToStringSlice(payload["target_profiles"])
	if len(values) == 0 {
		values = anyToStringSlice(payload["target_profile"])
	}
	if len(values) == 0 {
		values = fallback
	}
	return normalizeTargetProfiles(values)
}

func targetProfileLabel(value string) string {
	switch normalizeTargetProfile(value) {
	case "openai":
		return "OpenAI"
	case "grok":
		return "Grok"
	case "gemini":
		return "Gemini"
	case "claude":
		return "Claude"
	default:
		return "常规"
	}
}

func totalCheckPlanItems(plans []checkPlan) int {
	total := 0
	for _, plan := range plans {
		total += len(plan.Items)
	}
	return total
}

func checkPlanProfiles(plans []checkPlan) []string {
	out := make([]string, 0, len(plans))
	for _, plan := range plans {
		out = append(out, plan.TargetProfile)
	}
	return out
}

func filterDeletedCheckItems(items []ProxyTask, deletedIDs map[int]bool) []ProxyTask {
	if len(deletedIDs) == 0 {
		return items
	}
	out := make([]ProxyTask, 0, len(items))
	for _, item := range items {
		if !deletedIDs[item.ID] {
			out = append(out, item)
		}
	}
	return out
}

func checkBatchStream(ctx context.Context, items []ProxyTask, cfg CheckConfig) <-chan CheckResult {
	results := make(chan CheckResult, maxInt(1, minInt(cfg.Concurrent, len(items))))
	jobs := make(chan ProxyTask)
	workers := minInt(cfg.Concurrent, len(items))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case item, ok := <-jobs:
					if !ok {
						return
					}
					result := checkProxy(ctx, item, cfg)
					select {
					case results <- result:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, item := range items {
			select {
			case jobs <- item:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	return results
}

func checkProxy(ctx context.Context, task ProxyTask, cfg CheckConfig) CheckResult {
	proxyCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.HardTimeout)*time.Second)
	defer cancel()
	if cfg.Rounds <= 1 {
		return checkProxyOnce(proxyCtx, task, cfg).CheckResult
	}
	results := make([]singleCheckResult, 0, cfg.Rounds)
	for i := 0; i < cfg.Rounds; i++ {
		if proxyCtx.Err() != nil {
			break
		}
		results = append(results, checkProxyOnce(proxyCtx, task, cfg))
	}
	return combineRoundResults(cfg.TargetProfile, results)
}

func checkProxyOnce(ctx context.Context, task ProxyTask, cfg CheckConfig) singleCheckResult {
	profile := targetProfiles[cfg.TargetProfile]
	if profile.ServiceURL == "" {
		profile = targetProfiles["generic"]
	}
	var lastResult singleCheckResult
	for _, candidate := range candidateProxyURLs(task) {
		client, detected, err := proxyHTTPClient(candidate, cfg.RequestTimeout)
		if err != nil {
			lastResult = singleCheckResult{CheckResult: failedResult(task.ID, cfg.TargetProfile, err.Error())}
			continue
		}
		if !probeClient(ctx, client, profile) {
			lastResult = singleCheckResult{CheckResult: failedResult(task.ID, cfg.TargetProfile, detected+" probe failed")}
			continue
		}
		result := checkWithClient(ctx, task.ID, cfg.TargetProfile, profile, client, detected)
		if result.Status == "available" {
			return result
		}
		lastResult = result
	}
	if lastResult.ProxyID != 0 {
		return lastResult
	}
	return singleCheckResult{CheckResult: failedResult(task.ID, cfg.TargetProfile, "all protocol detection failed")}
}

func checkWithClient(ctx context.Context, proxyID int, targetProfile string, profile TargetProfile, client *http.Client, detected string) singleCheckResult {
	started := time.Now()
	probeCtx, cancel := targetContext(ctx, profile)
	defer cancel()
	successes := 0
	total := 1 + len(exitIPTargets())
	serviceReachable := false
	var apiReachable *bool
	var exitIP *string
	var latency *int
	var cloudflareStatus *string
	if ok, cloudflare := serviceOKStatus(probeCtx, client, profile, profile.ServiceURL, serviceAllowedStatus(profile)); ok {
		successes++
		serviceReachable = true
		elapsed := int(time.Since(started).Milliseconds())
		latency = &elapsed
		cloudflareStatus = stringPtr(cloudflare)
	} else if cloudflare != "" {
		cloudflareStatus = &cloudflare
	}
	if profile.APIURL != "" {
		total++
		ok := okStatus(probeCtx, client, profile, profile.APIURL, apiAllowedStatus(profile))
		apiReachable = &ok
		if ok {
			successes++
		}
	}
	for _, target := range exitIPTargets() {
		if value, ok := fetchExitIP(ctx, client, target); ok {
			successes++
			exitIP = &value
			if latency == nil {
				elapsed := int(time.Since(started).Milliseconds())
				latency = &elapsed
			}
		}
	}
	var country, ipType, asnOrg *string
	if exitIP != nil {
		metadata := checkmeta.EnrichIP(ctx, nil, "", *exitIP)
		country = stringPtr(metadata.Country)
		ipType = stringPtr(metadata.IPType)
		asnOrg = stringPtr(metadata.ASNOrg)
	}
	successRate := float64(successes) / float64(total)
	status := "failed"
	if exitIP != nil || serviceReachable || (apiReachable != nil && *apiReachable) {
		status = "available"
	}
	grade := gradeResult(latency, successRate, serviceReachable, apiReachable)
	baseReachable := exitIP != nil
	recommended := recommendUse(targetProfile, baseReachable, serviceReachable, apiReachable, status)
	var lastError *string
	if status == "failed" {
		msg := "proxy check failed"
		lastError = &msg
	}
	return singleCheckResult{CheckResult: CheckResult{
		ProxyID:          proxyID,
		Status:           status,
		Grade:            grade,
		LatencyMS:        latency,
		ExitIP:           exitIP,
		Country:          country,
		IPType:           ipType,
		ASNOrg:           asnOrg,
		SuccessRate:      successRate,
		TargetProfile:    targetProfile,
		DetectedProtocol: &detected,
		ServiceReachable: serviceReachable,
		APIReachable:     apiReachable,
		CloudflareStatus: cloudflareStatus,
		RecommendedUse:   recommended,
		LastError:        lastError,
	}, BaseReachable: baseReachable}
}

func proxyHTTPClient(proxyURL string, requestTimeout int) (*http.Client, string, error) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, "", err
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" && scheme != "socks4" && scheme != "socks5" && scheme != "socks5h" {
		return nil, "", fmt.Errorf("unsupported proxy scheme: %s", scheme)
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyURL(parsed),
		TLSHandshakeTimeout:   time.Duration(requestTimeout) * time.Second,
		ResponseHeaderTimeout: time.Duration(requestTimeout) * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if scheme == "socks5" || scheme == "socks5h" {
		dialer, err := newSocks5Dialer(parsed, time.Duration(requestTimeout)*time.Second)
		if err != nil {
			return nil, "", err
		}
		transport.Proxy = nil
		transport.DialContext = dialer.DialContext
	}
	if scheme == "socks4" {
		dialer, err := newSocks4Dialer(parsed, time.Duration(requestTimeout)*time.Second)
		if err != nil {
			return nil, "", err
		}
		transport.Proxy = nil
		transport.DialContext = dialer.DialContext
	}
	return &http.Client{Transport: transport, Timeout: time.Duration(requestTimeout) * time.Second}, scheme, nil
}

type socks4Dialer struct {
	proxyAddress string
	userID       string
	timeout      time.Duration
}

func newSocks4Dialer(proxy *url.URL, timeout time.Duration) (*socks4Dialer, error) {
	if proxy.Host == "" {
		return nil, errors.New("missing socks4 proxy host")
	}
	host := proxy.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "1080")
	}
	dialer := &socks4Dialer{proxyAddress: host, timeout: timeout}
	if proxy.User != nil {
		dialer.userID = proxy.User.Username()
	}
	return dialer, nil
}

func (d *socks4Dialer) DialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	baseDialer := &net.Dialer{Timeout: d.timeout}
	conn, err := baseDialer.DialContext(ctx, network, d.proxyAddress)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(d.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	if err := d.handshake(conn, address); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (d *socks4Dialer) handshake(conn net.Conn, address string) error {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid target port")
	}
	request := []byte{0x04, 0x01, byte(port >> 8), byte(port)}
	if ip := net.ParseIP(host).To4(); ip != nil {
		request = append(request, ip...)
		request = append(request, []byte(d.userID)...)
		request = append(request, 0x00)
	} else {
		request = append(request, 0x00, 0x00, 0x00, 0x01)
		request = append(request, []byte(d.userID)...)
		request = append(request, 0x00)
		request = append(request, []byte(host)...)
		request = append(request, 0x00)
	}
	if _, err := conn.Write(request); err != nil {
		return err
	}
	reply := make([]byte, 8)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[1] != 0x5a {
		return fmt.Errorf("socks4 connect failed: reply=%d", reply[1])
	}
	return nil
}

type socks5Dialer struct {
	proxyAddress string
	username     string
	password     string
	timeout      time.Duration
}

func newSocks5Dialer(proxy *url.URL, timeout time.Duration) (*socks5Dialer, error) {
	if proxy.Host == "" {
		return nil, errors.New("missing socks5 proxy host")
	}
	host := proxy.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "1080")
	}
	dialer := &socks5Dialer{proxyAddress: host, timeout: timeout}
	if proxy.User != nil {
		dialer.username = proxy.User.Username()
		dialer.password, _ = proxy.User.Password()
	}
	return dialer, nil
}

func (d *socks5Dialer) DialContext(ctx context.Context, network string, address string) (net.Conn, error) {
	baseDialer := &net.Dialer{Timeout: d.timeout}
	conn, err := baseDialer.DialContext(ctx, network, d.proxyAddress)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(d.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	if err := d.handshake(conn, address); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (d *socks5Dialer) handshake(conn net.Conn, address string) error {
	methods := []byte{0x00}
	if d.username != "" {
		methods = append(methods, 0x02)
	}
	greeting := append([]byte{0x05, byte(len(methods))}, methods...)
	if _, err := conn.Write(greeting); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[0] != 0x05 {
		return errors.New("invalid socks5 version")
	}
	switch reply[1] {
	case 0x00:
	case 0x02:
		if err := d.authenticate(conn); err != nil {
			return err
		}
	default:
		return errors.New("socks5 authentication method rejected")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid target port")
	}
	request := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			request = append(request, 0x01)
			request = append(request, ipv4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return errors.New("target host too long")
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, []byte(host)...)
	}
	request = append(request, byte(port>>8), byte(port))
	if _, err := conn.Write(request); err != nil {
		return err
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return err
	}
	if header[0] != 0x05 {
		return errors.New("invalid socks5 response")
	}
	if header[1] != 0x00 {
		return fmt.Errorf("socks5 connect failed: reply=%d", header[1])
	}
	switch header[3] {
	case 0x01:
		_, err = io.CopyN(io.Discard, conn, 4)
	case 0x03:
		length := make([]byte, 1)
		if _, err = io.ReadFull(conn, length); err == nil {
			_, err = io.CopyN(io.Discard, conn, int64(length[0]))
		}
	case 0x04:
		_, err = io.CopyN(io.Discard, conn, 16)
	default:
		err = errors.New("invalid socks5 address type")
	}
	if err != nil {
		return err
	}
	_, err = io.CopyN(io.Discard, conn, 2)
	return err
}

func (d *socks5Dialer) authenticate(conn net.Conn) error {
	if len(d.username) > 255 || len(d.password) > 255 {
		return errors.New("socks5 credentials too long")
	}
	request := []byte{0x01, byte(len(d.username))}
	request = append(request, []byte(d.username)...)
	request = append(request, byte(len(d.password)))
	request = append(request, []byte(d.password)...)
	if _, err := conn.Write(request); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[0] != 0x01 || reply[1] != 0x00 {
		return errors.New("socks5 authentication failed")
	}
	return nil
}

func candidateProxyURLs(task ProxyTask) []string {
	if task.Protocol != "" && task.Protocol != "auto" {
		return []string{buildProxyURLWithProtocol(task, task.Protocol)}
	}
	if raw := strings.TrimSpace(task.Proxy); raw != "" {
		if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" && parsed.Host != "" && parsed.Scheme != "auto" {
			return []string{raw}
		}
	}
	protocols := []string{"http", "https", "socks5", "socks5h", "socks4"}
	values := make([]string, 0, len(protocols))
	for _, protocol := range protocols {
		values = append(values, buildProxyURLWithProtocol(task, protocol))
	}
	return values
}

func buildProxyURLWithProtocol(task ProxyTask, protocol string) string {
	auth := ""
	if task.Username != nil && *task.Username != "" {
		password := ""
		if task.Password != nil {
			password = *task.Password
		}
		auth = url.UserPassword(*task.Username, password).String() + "@"
	}
	return fmt.Sprintf("%s://%s%s:%d", protocol, auth, task.IP, task.Port)
}

func probeClient(ctx context.Context, client *http.Client, profile TargetProfile) bool {
	probeCtx, cancel := targetContext(ctx, profile)
	defer cancel()
	for _, target := range exitIPTargets() {
		req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, target, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return true
			}
		}
	}
	target := profile.APIURL
	allowed := apiAllowedStatus(profile)
	if target == "" {
		target = profile.ServiceURL
		allowed = serviceAllowedStatus(profile)
	}
	return okStatus(probeCtx, client, profile, target, allowed)
}

func okStatus(ctx context.Context, client *http.Client, profile TargetProfile, target string, allowed map[int]bool) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false
	}
	applyProfileHeaders(req, profile)
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	_, _ = io.Copy(io.Discard, resp.Body)
	return allowed[resp.StatusCode] && keywordMatched(profile, string(raw))
}

func serviceOKStatus(ctx context.Context, client *http.Client, profile TargetProfile, target string, allowed map[int]bool) (bool, string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return false, ""
	}
	applyProfileHeaders(req, profile)
	resp, err := client.Do(req)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	_, _ = io.Copy(io.Discard, resp.Body)
	body := string(raw)
	return allowed[resp.StatusCode] && keywordMatched(profile, body), checkmeta.DetectCloudflareStatus(resp.StatusCode, resp.Header, body)
}

func targetContext(ctx context.Context, profile TargetProfile) (context.Context, context.CancelFunc) {
	timeout := clampInt(profile.TimeoutSeconds, 0, 60)
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
}

func serviceAllowedStatus(profile TargetProfile) map[int]bool {
	if len(profile.ExpectedStatuses) > 0 {
		return statusSet(profile.ExpectedStatuses)
	}
	return map[int]bool{200: true, 204: true, 301: true, 302: true, 303: true, 307: true, 308: true}
}

func apiAllowedStatus(profile TargetProfile) map[int]bool {
	if len(profile.ExpectedStatuses) > 0 {
		return statusSet(profile.ExpectedStatuses)
	}
	return map[int]bool{200: true, 401: true, 403: true}
}

func statusSet(values []int) map[int]bool {
	out := map[int]bool{}
	for _, value := range values {
		out[value] = true
	}
	return out
}

func applyProfileHeaders(req *http.Request, profile TargetProfile) {
	for key, value := range profile.Headers {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			req.Header.Set(key, value)
		}
	}
}

func keywordMatched(profile TargetProfile, body string) bool {
	keyword := strings.TrimSpace(profile.Keyword)
	if keyword == "" {
		return true
	}
	return strings.Contains(strings.ToLower(body), strings.ToLower(keyword))
}

func fetchExitIP(ctx context.Context, client *http.Client, target string) (string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", false
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return "", false
	}
	var payload map[string]string
	if err := json.Unmarshal(raw, &payload); err != nil {
		value := strings.TrimSpace(string(raw))
		return value, value != "" && len(value) <= 64
	}
	value := strings.TrimSpace(payload["ip"])
	if value == "" {
		value = strings.Split(strings.TrimSpace(payload["origin"]), ",")[0]
	}
	return value, value != ""
}

func exitIPTargets() []string {
	return []string{"https://api.ipify.org?format=json", "https://httpbin.org/ip"}
}

func failedResult(proxyID int, profile string, message string) CheckResult {
	return CheckResult{
		ProxyID:        proxyID,
		Status:         "failed",
		Grade:          "C",
		SuccessRate:    0,
		TargetProfile:  profile,
		RecommendedUse: "invalid",
		LastError:      stringPtr(message),
	}
}

func combineRoundResults(profile string, results []singleCheckResult) CheckResult {
	if len(results) == 0 {
		return failedResult(0, profile, "no check result")
	}
	available := make([]singleCheckResult, 0, len(results))
	for _, result := range results {
		if result.Status == "available" {
			available = append(available, result)
		}
	}
	status := "failed"
	if len(available)*2 >= len(results) {
		status = "available"
	}
	candidates := available
	if len(candidates) == 0 {
		candidates = results
	}
	best := candidates[0]
	for _, item := range candidates[1:] {
		bestLatency := latencySortValue(best.LatencyMS)
		itemLatency := latencySortValue(item.LatencyMS)
		if item.Status == "available" && best.Status != "available" {
			best = item
			continue
		}
		if itemLatency < bestLatency || (itemLatency == bestLatency && item.SuccessRate > best.SuccessRate) {
			best = item
		}
	}
	successRate := 0.0
	baseReachable := false
	serviceReachable := false
	apiSeen := false
	apiReachableValue := false
	var cloudflareStatus *string
	for _, result := range results {
		successRate += result.SuccessRate
		baseReachable = baseReachable || result.BaseReachable
		serviceReachable = serviceReachable || result.ServiceReachable
		if cloudflareStatus == nil && result.CloudflareStatus != nil {
			cloudflareStatus = result.CloudflareStatus
		}
		if result.APIReachable != nil {
			apiSeen = true
			apiReachableValue = apiReachableValue || *result.APIReachable
		}
	}
	successRate = successRate / float64(len(results))
	availableRatio := float64(len(available)) / float64(len(results))
	var apiReachable *bool
	if apiSeen {
		apiReachable = &apiReachableValue
	}
	grade := gradeResult(best.LatencyMS, successRate, serviceReachable, apiReachable)
	output := best.CheckResult
	output.Status = status
	output.SuccessRate = successRate
	if status == "available" {
		output.SuccessRate = availableRatio
	}
	output.Grade = grade
	output.ServiceReachable = serviceReachable
	output.APIReachable = apiReachable
	output.CloudflareStatus = cloudflareStatus
	output.RecommendedUse = recommendUse(profile, baseReachable, serviceReachable, apiReachable, status)
	if status == "failed" {
		message := "available_rounds=" + strconv.Itoa(len(available)) + "/" + strconv.Itoa(len(results))
		output.LastError = &message
	}
	return output
}

func gradeResult(latency *int, successRate float64, serviceReachable bool, apiReachable *bool) string {
	if successRate >= 0.9 && latency != nil && *latency <= 1500 && serviceReachable && (apiReachable == nil || *apiReachable) {
		return "A"
	}
	if successRate >= 0.65 && latency != nil && *latency <= 4000 {
		return "B"
	}
	if successRate > 0 {
		return "C"
	}
	return "F"
}

func latencySortValue(value *int) int {
	if value == nil {
		return 1 << 30
	}
	return *value
}

func recommendUse(profile string, baseReachable bool, serviceReachable bool, apiReachable *bool, status string) string {
	if status != "available" {
		return "invalid"
	}
	if profile == "generic" {
		if baseReachable && serviceReachable {
			return "generic"
		}
		if baseReachable || serviceReachable {
			return "unstable"
		}
		return "invalid"
	}
	if serviceReachable && apiReachable != nil && *apiReachable {
		return "web_api"
	}
	if apiReachable != nil && *apiReachable {
		return "api"
	}
	if serviceReachable {
		return "web"
	}
	if baseReachable {
		return "base"
	}
	return "invalid"
}

func maxInt(left int, right int) int {
	if left > right {
		return left
	}
	return right
}

func minInt(left int, right int) int {
	if left < right {
		return left
	}
	return right
}
