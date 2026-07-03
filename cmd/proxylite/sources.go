package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type sourceOption struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	DefaultProtocol string `json:"default_protocol"`
	Parser          string `json:"parser"`
	Enabled         bool   `json:"enabled"`
}

func builtinSources() []sourceOption {
	items := []sourceOption{
		{ID: "proxifly_all", Name: "Proxifly All", URL: "https://cdn.jsdelivr.net/gh/proxifly/free-proxy-list@main/proxies/all/data.json", DefaultProtocol: "auto", Parser: "json", Enabled: true},
		{ID: "databay_http", Name: "Databay HTTP", URL: "https://raw.githubusercontent.com/databay-labs/free-proxy-list/master/http.txt", DefaultProtocol: "http", Parser: "plain", Enabled: true},
		{ID: "databay_socks4", Name: "Databay SOCKS4", URL: "https://raw.githubusercontent.com/databay-labs/free-proxy-list/master/socks4.txt", DefaultProtocol: "socks4", Parser: "plain", Enabled: true},
		{ID: "databay_socks5", Name: "Databay SOCKS5", URL: "https://raw.githubusercontent.com/databay-labs/free-proxy-list/master/socks5.txt", DefaultProtocol: "socks5", Parser: "plain", Enabled: true},
		{ID: "iplocate_all", Name: "IPLocate All", URL: "https://raw.githubusercontent.com/iplocate/free-proxy-list/main/all-proxies.txt", DefaultProtocol: "auto", Parser: "plain", Enabled: true},
		{ID: "iplocate_http", Name: "IPLocate HTTP", URL: "https://raw.githubusercontent.com/iplocate/free-proxy-list/main/protocols/http.txt", DefaultProtocol: "http", Parser: "plain", Enabled: true},
		{ID: "iplocate_socks4", Name: "IPLocate SOCKS4", URL: "https://raw.githubusercontent.com/iplocate/free-proxy-list/main/protocols/socks4.txt", DefaultProtocol: "socks4", Parser: "plain", Enabled: true},
		{ID: "iplocate_socks5", Name: "IPLocate SOCKS5", URL: "https://raw.githubusercontent.com/iplocate/free-proxy-list/main/protocols/socks5.txt", DefaultProtocol: "socks5", Parser: "plain", Enabled: true},
		{ID: "roosterkid_http", Name: "OpenProxyList HTTP", URL: "https://raw.githubusercontent.com/roosterkid/openproxylist/main/HTTPS.txt", DefaultProtocol: "http", Parser: "plain", Enabled: true},
		{ID: "roosterkid_socks4", Name: "OpenProxyList SOCKS4", URL: "https://raw.githubusercontent.com/roosterkid/openproxylist/main/SOCKS4.txt", DefaultProtocol: "socks4", Parser: "plain", Enabled: true},
		{ID: "roosterkid_socks5", Name: "OpenProxyList SOCKS5", URL: "https://raw.githubusercontent.com/roosterkid/openproxylist/main/SOCKS5.txt", DefaultProtocol: "socks5", Parser: "plain", Enabled: true},
		{ID: "thespeedx_http", Name: "TheSpeedX HTTP", URL: "https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/http.txt", DefaultProtocol: "http", Parser: "plain", Enabled: true},
		{ID: "thespeedx_socks4", Name: "TheSpeedX SOCKS4", URL: "https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/socks4.txt", DefaultProtocol: "socks4", Parser: "plain", Enabled: true},
		{ID: "thespeedx_socks5", Name: "TheSpeedX SOCKS5", URL: "https://raw.githubusercontent.com/TheSpeedX/SOCKS-List/master/socks5.txt", DefaultProtocol: "socks5", Parser: "plain", Enabled: true},
		{ID: "proxyscrape_http", Name: "ProxyScrape HTTP", URL: "https://api.proxyscrape.com/?request=getproxies&proxytype=http&timeout=1000&country=all", DefaultProtocol: "http", Parser: "plain", Enabled: true},
		{ID: "proxyscrape_socks5", Name: "ProxyScrape SOCKS5", URL: "https://api.proxyscrape.com/?request=getproxies&proxytype=socks5&timeout=1000&country=all", DefaultProtocol: "socks5", Parser: "plain", Enabled: true},
		{ID: "geonode", Name: "GeoNode Recently Checked", URL: "https://proxylist.geonode.com/api/proxy-list?limit=500&page=1&sort_by=lastChecked&sort_type=desc", DefaultProtocol: "auto", Parser: "json", Enabled: true},
		{ID: "vpslab_all", Name: "VPSLab All", URL: "https://raw.githubusercontent.com/VPSLabCloud/VPSLab-Free-Proxy-List/main/all_proxies.txt", DefaultProtocol: "auto", Parser: "plain", Enabled: true},
		{ID: "vpslab_elite", Name: "VPSLab Elite", URL: "https://raw.githubusercontent.com/VPSLabCloud/VPSLab-Free-Proxy-List/main/all_elite.txt", DefaultProtocol: "auto", Parser: "plain", Enabled: true},
		{ID: "vpslab_http_all", Name: "VPSLab HTTP All", URL: "https://raw.githubusercontent.com/VPSLabCloud/VPSLab-Free-Proxy-List/main/http_all.txt", DefaultProtocol: "http", Parser: "plain", Enabled: true},
		{ID: "vpslab_http_ssl", Name: "VPSLab HTTP SSL", URL: "https://raw.githubusercontent.com/VPSLabCloud/VPSLab-Free-Proxy-List/main/http_ssl.txt", DefaultProtocol: "http", Parser: "plain", Enabled: true},
		{ID: "vpslab_http_elite", Name: "VPSLab HTTP Elite", URL: "https://raw.githubusercontent.com/VPSLabCloud/VPSLab-Free-Proxy-List/main/http_elite.txt", DefaultProtocol: "http", Parser: "plain", Enabled: true},
		{ID: "vpslab_http_anonymous", Name: "VPSLab HTTP Anonymous", URL: "https://raw.githubusercontent.com/VPSLabCloud/VPSLab-Free-Proxy-List/main/http_anonymous.txt", DefaultProtocol: "http", Parser: "plain", Enabled: true},
		{ID: "vpslab_socks4", Name: "VPSLab SOCKS4", URL: "https://raw.githubusercontent.com/VPSLabCloud/VPSLab-Free-Proxy-List/main/socks4_all.txt", DefaultProtocol: "socks4", Parser: "plain", Enabled: true},
		{ID: "vpslab_socks5", Name: "VPSLab SOCKS5", URL: "https://raw.githubusercontent.com/VPSLabCloud/VPSLab-Free-Proxy-List/main/socks5_all.txt", DefaultProtocol: "socks5", Parser: "plain", Enabled: true},
		{ID: "hookzof_socks5", Name: "Hookzof SOCKS5", URL: "https://raw.githubusercontent.com/hookzof/socks5_list/master/proxy.txt", DefaultProtocol: "socks5", Parser: "plain", Enabled: true},
		{ID: "spysme_http", Name: "Spys.me HTTP", URL: "https://spys.me/proxy.txt", DefaultProtocol: "http", Parser: "plain", Enabled: true},
		{ID: "spysme_socks", Name: "Spys.me SOCKS", URL: "https://spys.me/socks.txt", DefaultProtocol: "auto", Parser: "plain", Enabled: true},
		{ID: "my_proxy", Name: "My-Proxy Hourly HTTP", URL: "https://www.my-proxy.com/free-proxy-list.html", DefaultProtocol: "http", Parser: "html", Enabled: true},
		{ID: "proxynova", Name: "ProxyNova", URL: "https://www.proxynova.com/proxy-server-list/", DefaultProtocol: "http", Parser: "html", Enabled: true},
		{ID: "hidemn", Name: "hide.mn", URL: "https://hide.mn/en/proxy-list/", DefaultProtocol: "auto", Parser: "html", Enabled: true},
		{ID: "freeproxy_socks", Name: "Free-Proxy-List SOCKS", URL: "https://free-proxy-list.net/zh-cn/socks-proxy.html", DefaultProtocol: "socks5", Parser: "html", Enabled: true},
		{ID: "checkerproxy_archive", Name: "CheckerProxy Archive", URL: "https://checkerproxy.net/api/archive/2026-07-03", DefaultProtocol: "auto", Parser: "plain", Enabled: true},
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items
}

func sourceMap() map[string]sourceOption {
	items := map[string]sourceOption{}
	for _, source := range builtinSources() {
		items[source.ID] = source
	}
	return items
}

func (s *server) StartFetchSourcesJob(payload map[string]any) (map[string]any, error) {
	if s.jobs.TypeRunning("fetch") {
		return nil, jobConflict("fetch")
	}
	job, ctx := s.jobs.Create("fetch", "准备拉取代理源")
	sourceIDs := anyToStringSlice(payload["source_ids"])
	limitPerSource := anyToInt(payload["limit_per_source"])
	if limitPerSource < 0 {
		limitPerSource = 0
	}
	if limitPerSource > 50000 {
		limitPerSource = 50000
	}
	go s.runFetchSourcesJob(ctx, job.ID, sourceIDs, limitPerSource)
	return map[string]any{"job_id": job.ID}, nil
}

func (s *server) runFetchSourcesJob(ctx context.Context, jobID string, sourceIDs []string, limitPerSource int) {
	sources := selectedSources(sourceIDs)
	total := len(sources)
	s.jobs.Update(jobID, map[string]any{"total": total, "message": fmt.Sprintf("开始拉取 %d 个代理源", total)})
	added := 0
	updated := 0
	failed := 0
	sourceResults := []map[string]any{}
	for i, source := range sources {
		if ctx.Err() != nil {
			s.jobs.finishCancelled(jobID, "拉取代理源已停止")
			return
		}
		s.jobs.Update(jobID, map[string]any{"done": i, "message": fmt.Sprintf("正在拉取：%s", source.Name)})
		text, count, err := fetchSource(ctx, source, limitPerSource)
		if err != nil {
			failed++
			sourceResults = append(sourceResults, map[string]any{"id": source.ID, "name": source.Name, "error": err.Error()})
			s.jobs.Update(jobID, map[string]any{"done": i + 1, "failed": failed, "message": fmt.Sprintf("代理源失败：%s", source.Name)})
			continue
		}
		result, err := s.store.ImportProxies(text, source.ID, source.DefaultProtocol)
		if err != nil {
			failed++
			sourceResults = append(sourceResults, map[string]any{"id": source.ID, "name": source.Name, "count": count, "error": err.Error()})
			s.jobs.Update(jobID, map[string]any{"done": i + 1, "failed": failed, "message": fmt.Sprintf("代理源导入失败：%s", source.Name)})
			continue
		}
		added += anyToInt(result["added"])
		updated += anyToInt(result["updated"])
		sourceResults = append(sourceResults, map[string]any{
			"id":      source.ID,
			"name":    source.Name,
			"count":   count,
			"added":   anyToInt(result["added"]),
			"updated": anyToInt(result["updated"]),
		})
		s.jobs.Update(jobID, map[string]any{
			"done":    i + 1,
			"success": added + updated,
			"failed":  failed,
			"message": fmt.Sprintf("已导入：%s", source.Name),
		})
	}
	result := map[string]any{
		"added":          added,
		"updated":        updated,
		"failed_sources": failed,
		"sources":        sourceResults,
	}
	s.jobs.Update(jobID, map[string]any{"done": total, "total": total, "success": added + updated, "failed": failed})
	s.jobs.complete(jobID, fmt.Sprintf("拉取完成：新增 %d，更新 %d，失败源 %d", added, updated, failed), result)
}

func selectedSources(sourceIDs []string) []sourceOption {
	all := sourceMap()
	if len(sourceIDs) == 0 {
		out := builtinSources()
		return out
	}
	out := []sourceOption{}
	seen := map[string]bool{}
	for _, id := range sourceIDs {
		id = strings.TrimSpace(id)
		if id == "all" {
			return builtinSources()
		}
		if source, ok := all[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, source)
		}
	}
	if len(out) == 0 {
		return builtinSources()
	}
	return out
}

func fetchSource(ctx context.Context, source sourceOption, limit int) (string, int, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, source.URL, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("User-Agent", "ProxyLiteChecker/0.1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		return "", 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20*1024*1024))
	if err != nil {
		return "", 0, err
	}
	lines := extractSourceProxyLines(raw, source)
	if limit > 0 && len(lines) > limit {
		lines = lines[:limit]
	}
	return strings.Join(lines, "\n"), len(lines), nil
}

func extractSourceProxyLines(raw []byte, source sourceOption) []string {
	defaultProtocol := normalizeProtocol(source.DefaultProtocol)
	if defaultProtocol == "" {
		defaultProtocol = "auto"
	}
	lines := []string{}
	if strings.EqualFold(source.Parser, "json") {
		var payload any
		if json.Unmarshal(raw, &payload) == nil {
			walkJSONProxyPayload(payload, defaultProtocol, &lines)
		}
	}
	lines = append(lines, strings.Split(string(raw), "\n")...)
	seen := map[string]bool{}
	out := []string{}
	for _, proxy := range parseProxyText(strings.Join(lines, "\n"), defaultProtocol) {
		key := proxyKey(proxy)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}

func walkJSONProxyPayload(value any, defaultProtocol string, out *[]string) {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			walkJSONProxyPayload(item, defaultProtocol, out)
		}
	case map[string]any:
		host := firstNonEmpty(
			jsonFieldString(typed, "ip"),
			jsonFieldString(typed, "host"),
			jsonFieldString(typed, "addr"),
			jsonFieldString(typed, "address"),
		)
		port := anyToInt(firstNonEmpty(
			jsonFieldString(typed, "port"),
			jsonFieldString(typed, "proxyPort"),
		))
		protocol := normalizeProtocol(firstNonEmpty(
			jsonFieldString(typed, "protocol"),
			jsonFieldString(typed, "type"),
			defaultProtocol,
		))
		if host != "" && host != "<nil>" && port > 0 {
			if protocol == "" {
				protocol = defaultProtocol
			}
			*out = append(*out, fmt.Sprintf("%s://%s:%d", protocol, strings.TrimSpace(host), port))
		}
		for _, item := range typed {
			walkJSONProxyPayload(item, defaultProtocol, out)
		}
	case string:
		if strings.Contains(typed, ":") {
			*out = append(*out, typed)
		}
	}
}

func jsonFieldString(data map[string]any, key string) string {
	value, ok := data[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}
