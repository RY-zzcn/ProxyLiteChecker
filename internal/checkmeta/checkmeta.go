package checkmeta

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const defaultIPMetadataEndpoint = "http://ip-api.com/json"

type Metadata struct {
	Country string
	IPType  string
	ASNOrg  string
}

func DetectCloudflareStatus(statusCode int, headers http.Header, body string) string {
	server := strings.ToLower(headers.Get("Server"))
	hasCFHeader := headers.Get("CF-Ray") != "" || strings.Contains(server, "cloudflare")
	text := strings.ToLower(body)
	if len(text) > 2000 {
		text = text[:2000]
	}
	challengeMarkers := []string{
		"cf-browser-verification",
		"cf-challenge",
		"checking your browser",
		"turnstile",
		"challenge-platform",
	}
	for _, marker := range challengeMarkers {
		if strings.Contains(text, marker) {
			return "challenge"
		}
	}
	if hasCFHeader && (statusCode == http.StatusForbidden || statusCode == http.StatusTooManyRequests || statusCode == http.StatusServiceUnavailable) {
		return "blocked"
	}
	if hasCFHeader {
		return "behind_cf"
	}
	return "not_cf"
}

func EnrichIP(ctx context.Context, client *http.Client, endpoint string, ip string) Metadata {
	ip = strings.TrimSpace(ip)
	metadata := Metadata{IPType: ClassifyIPType(ip)}
	if ip == "" {
		return metadata
	}
	remote, ok := LookupIPMetadata(ctx, client, endpoint, ip)
	if !ok {
		return metadata
	}
	if remote.Country != "" {
		metadata.Country = remote.Country
	}
	if remote.IPType != "" {
		metadata.IPType = remote.IPType
	}
	if remote.ASNOrg != "" {
		metadata.ASNOrg = remote.ASNOrg
	}
	return metadata
}

func ClassifyIPType(ip string) string {
	value := net.ParseIP(strings.TrimSpace(ip))
	if value == nil {
		if strings.TrimSpace(ip) == "" {
			return ""
		}
		return "unknown"
	}
	if value.IsPrivate() {
		return "private"
	}
	if value.IsLoopback() {
		return "loopback"
	}
	if value.IsMulticast() {
		return "multicast"
	}
	if value.IsUnspecified() || value.IsLinkLocalUnicast() || !value.IsGlobalUnicast() {
		return "reserved"
	}
	return "public"
}

func LookupIPMetadata(ctx context.Context, client *http.Client, endpoint string, ip string) (Metadata, bool) {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return Metadata{}, false
	}
	if client == nil {
		client = &http.Client{Timeout: 3 * time.Second}
	}
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = strings.TrimSpace(os.Getenv("PLC_IP_METADATA_ENDPOINT"))
	}
	if endpoint == "" {
		endpoint = defaultIPMetadataEndpoint
	}
	requestURL, err := buildMetadataURL(endpoint, ip)
	if err != nil {
		return Metadata{}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return Metadata{}, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return Metadata{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Metadata{}, false
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return Metadata{}, false
	}
	if strings.ToLower(strings.TrimSpace(stringValue(payload["status"]))) != "success" {
		return Metadata{}, false
	}
	return Metadata{
		Country: truncateString(stringValue(payload["countryCode"]), 64),
		IPType:  ClassifyIPMetadataType(payload),
		ASNOrg:  truncateString(firstNonEmpty(stringValue(payload["org"]), stringValue(payload["isp"]), stringValue(payload["as"])), 128),
	}, true
}

func buildMetadataURL(endpoint string, ip string) (string, error) {
	if strings.Contains(endpoint, "{ip}") {
		endpoint = strings.ReplaceAll(endpoint, "{ip}", url.PathEscape(ip))
	} else {
		endpoint = strings.TrimRight(endpoint, "/") + "/" + url.PathEscape(ip)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	query := parsed.Query()
	if query.Get("fields") == "" {
		query.Set("fields", "status,countryCode,isp,org,as,proxy,hosting,mobile,query")
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func ClassifyIPMetadataType(data map[string]any) string {
	if boolValue(data["mobile"]) {
		return "mobile"
	}
	if boolValue(data["proxy"]) {
		return "proxy"
	}
	if boolValue(data["hosting"]) {
		return "datacenter"
	}
	org := strings.ToLower(strings.Join([]string{
		stringValue(data["org"]),
		stringValue(data["isp"]),
		stringValue(data["as"]),
	}, " "))
	datacenterKeywords := []string{
		"amazon", "aws", "google", "cloudflare", "azure", "microsoft",
		"digitalocean", "linode", "vultr", "hetzner", "ovh", "oracle",
		"alibaba", "tencent", "datacenter", "hosting", "server", "cloud",
	}
	for _, keyword := range datacenterKeywords {
		if strings.Contains(org, keyword) {
			return "datacenter"
		}
	}
	residentialKeywords := []string{
		"broadband", "cable", "communications", "fiber", "fibre", "ftth",
		"isp", "telecom", "telecommunications", "wireless", "internet service",
	}
	for _, keyword := range residentialKeywords {
		if strings.Contains(org, keyword) {
			return "residential"
		}
	}
	return "public"
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed == "1" || strings.EqualFold(typed, "true") || strings.EqualFold(typed, "yes")
	case float64:
		return typed != 0
	default:
		return false
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func truncateString(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit > 0 && len(value) > limit {
		return value[:limit]
	}
	return value
}
