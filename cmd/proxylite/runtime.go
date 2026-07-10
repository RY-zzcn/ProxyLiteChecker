package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

const runtimeLogLimit = 30

type runtimeLogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type runtimeMonitor struct {
	mu        sync.Mutex
	startedAt time.Time
	logs      []runtimeLogEntry
	attach    sync.Once
}

var applicationRuntime = newRuntimeMonitor()

func newRuntimeMonitor() *runtimeMonitor {
	return &runtimeMonitor{startedAt: time.Now()}
}

func (m *runtimeMonitor) AttachLogger() {
	if m == nil {
		return
	}
	m.attach.Do(func() {
		log.SetOutput(io.MultiWriter(os.Stderr, m))
	})
}

func (m *runtimeMonitor) Write(payload []byte) (int, error) {
	if m == nil {
		return len(payload), nil
	}
	for _, line := range strings.Split(string(payload), "\n") {
		line = stripRuntimeLogPrefix(strings.TrimSpace(line))
		if line != "" {
			m.Add(inferRuntimeLogLevel(line), line)
		}
	}
	return len(payload), nil
}

func stripRuntimeLogPrefix(message string) string {
	if len(message) > 20 && message[4] == '/' && message[7] == '/' && message[10] == ' ' && message[13] == ':' && message[16] == ':' && message[19] == ' ' {
		return strings.TrimSpace(message[20:])
	}
	return message
}

func runtimeJobStatusLabel(status string) string {
	switch status {
	case jobStatusCompleted:
		return "已完成"
	case jobStatusPartial:
		return "部分完成"
	case jobStatusFailed:
		return "失败"
	case jobStatusCancelled:
		return "已取消"
	case jobStatusInterrupted:
		return "重启中断"
	default:
		return status
	}
}

func (m *runtimeMonitor) Add(level string, message string) {
	if m == nil {
		return
	}
	message = truncateText(localizeRuntimeLogMessage(strings.TrimSpace(message)), 1024)
	if message == "" {
		return
	}
	entry := runtimeLogEntry{Time: nowString(), Level: normalizeRuntimeLogLevel(level), Message: message}
	m.mu.Lock()
	m.logs = append(m.logs, entry)
	if len(m.logs) > runtimeLogLimit {
		m.logs = append([]runtimeLogEntry(nil), m.logs[len(m.logs)-runtimeLogLimit:]...)
	}
	m.mu.Unlock()
}

func localizeRuntimeLogMessage(message string) string {
	switch message {
	case "SECURITY WARNING: using generated per-process SECRET_KEY; set SECRET_KEY for persistent sessions":
		return "安全警告：正在使用进程临时生成的 SECRET_KEY；请配置 SECRET_KEY 以保持登录会话稳定"
	case "SECURITY WARNING: default admin password is enabled; change ADMIN_PASSWORD before exposing the service":
		return "安全警告：当前仍使用默认管理员密码；对外开放服务前请修改 ADMIN_PASSWORD"
	}
	translations := []struct {
		prefix string
		text   string
	}{
		{"database schema version: ", "数据库结构版本："},
		{"starting proxylite on ", "ProxyLiteChecker 开始监听："},
		{"GeoIP initial load failed: ", "GeoIP 初始加载失败："},
		{"load gateway settings failed: ", "加载网关设置失败："},
		{"local gateway stopped: ", "本机网关已停止："},
	}
	for _, item := range translations {
		if strings.HasPrefix(message, item.prefix) {
			return item.text + strings.TrimSpace(strings.TrimPrefix(message, item.prefix))
		}
	}
	for _, gatewayType := range []string{"HTTP", "SOCKS5"} {
		prefix := "starting " + gatewayType + " gateway target="
		if !strings.HasPrefix(message, prefix) {
			continue
		}
		rest := strings.TrimPrefix(message, prefix)
		parts := strings.SplitN(rest, " on ", 2)
		profile := parts[0]
		bind := ""
		if len(parts) == 2 {
			bind = parts[1]
		}
		return fmt.Sprintf("启动 %s 网关：目标 %s，监听 %s", gatewayType, targetProfileLabel(profile), bind)
	}
	return message
}

func (m *runtimeMonitor) Payload(concurrency map[string]any) map[string]any {
	now := time.Now()
	startedAt := now
	logs := []runtimeLogEntry{}
	if m != nil {
		m.mu.Lock()
		startedAt = m.startedAt
		logs = append(logs, m.logs...)
		m.mu.Unlock()
	}
	return map[string]any{
		"started_at":        formatBeijingTime(startedAt),
		"uptime_seconds":    int64(now.Sub(startedAt).Seconds()),
		"logs":              logs,
		"log_limit":         runtimeLogLimit,
		"check_concurrency": concurrency,
	}
}

func inferRuntimeLogLevel(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "fatal"), strings.Contains(lower, "error"), strings.Contains(lower, "failed"):
		return "error"
	case strings.Contains(lower, "warning"), strings.Contains(lower, "warn"):
		return "warning"
	default:
		return "info"
	}
}

func normalizeRuntimeLogLevel(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "error":
		return "error"
	case "warning", "warn":
		return "warning"
	default:
		return "info"
	}
}
