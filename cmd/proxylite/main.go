package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	webassets "github.com/RY-zzcn/ProxyLiteChecker/app/web"
)

const (
	appVersion           = "0.1.9"
	defaultSecretKey     = "change-this-secret"
	defaultAdminPassword = "admin123"
	authCookieName       = "plc_access"
)

type config struct {
	AppName              string
	AppVersion           string
	Host                 string
	Port                 int
	DatabaseURL          string
	AdminUsername        string
	AdminPassword        string
	SecretKey            string
	AccessTokenMinutes   int
	WebDir               string
	ExportToken          string
	GatewayEnabled       bool
	GatewayHost          string
	GatewayPort          int
	Socks5GatewayEnabled bool
	Socks5GatewayHost    string
	Socks5GatewayPort    int
	GatewayUpstreamLimit int
}

type server struct {
	cfg       config
	mux       *http.ServeMux
	store     *store
	jobs      *jobManager
	gateway   *gatewayServer
	scheduler *scheduler
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func main() {
	cfg := loadConfig()
	srv := newServer(cfg)
	if srv.cfg.GatewayEnabled {
		srv.gateway = newGatewayServer(srv.store, gatewayConfig{
			Host:            srv.cfg.GatewayHost,
			Port:            srv.cfg.GatewayPort,
			Socks5Enabled:   srv.cfg.Socks5GatewayEnabled,
			Socks5Host:      srv.cfg.Socks5GatewayHost,
			Socks5Port:      srv.cfg.Socks5GatewayPort,
			UpstreamLimit:   srv.cfg.GatewayUpstreamLimit,
			RequestTimeoutS: 20,
		})
		go func() {
			if err := srv.gateway.Start(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("local gateway stopped: %v", err)
			}
		}()
	}
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	log.Printf("starting proxylite on %s", addr)
	if err := http.ListenAndServe(addr, srv); err != nil {
		log.Fatal(err)
	}
}

func loadConfig() config {
	webDir := envString("PLC_WEB_DIR", "")
	if webDir == "" {
		webDir = filepath.Join("app", "web")
	}
	port := clampInt(envInt("PORT", 8899), 1, 65535)
	return config{
		AppName:              envString("APP_NAME", "ProxyLiteChecker"),
		AppVersion:           envString("APP_VERSION", appVersion),
		Host:                 envString("HOST", "0.0.0.0"),
		Port:                 port,
		DatabaseURL:          envString("DATABASE_URL", "sqlite:///./data/proxylite.db"),
		AdminUsername:        envString("ADMIN_USERNAME", "admin"),
		AdminPassword:        envString("ADMIN_PASSWORD", defaultAdminPassword),
		SecretKey:            envString("SECRET_KEY", defaultSecretKey),
		AccessTokenMinutes:   clampInt(envInt("ACCESS_TOKEN_MINUTES", 1440), 5, 525600),
		WebDir:               webDir,
		ExportToken:          envString("PLC_EXPORT_TOKEN", ""),
		GatewayEnabled:       envBool("PLC_GATEWAY_ENABLED", true),
		GatewayHost:          envString("PLC_GATEWAY_HOST", "0.0.0.0"),
		GatewayPort:          clampInt(envInt("PLC_GATEWAY_PORT", 18080), 1, 65535),
		Socks5GatewayEnabled: envBool("PLC_SOCKS5_GATEWAY_ENABLED", true),
		Socks5GatewayHost:    envString("PLC_SOCKS5_GATEWAY_HOST", envString("PLC_GATEWAY_HOST", "0.0.0.0")),
		Socks5GatewayPort:    clampInt(envInt("PLC_SOCKS5_GATEWAY_PORT", 18081), 1, 65535),
		GatewayUpstreamLimit: clampInt(envInt("PLC_GATEWAY_UPSTREAM_LIMIT", 200), 1, 2000),
	}
}

func applySecurityBaseline(cfg config) config {
	requireSecure := envBool("PLC_REQUIRE_SECURE", false)
	if cfg.SecretKey == "" || cfg.SecretKey == defaultSecretKey {
		if requireSecure {
			log.Fatal("SECURITY: SECRET_KEY is unset or default while PLC_REQUIRE_SECURE=1")
		}
		random, err := randomTokenURLSafe(32)
		if err != nil {
			log.Fatalf("generate SECRET_KEY failed: %v", err)
		}
		cfg.SecretKey = random
		log.Printf("SECURITY WARNING: using generated per-process SECRET_KEY; set SECRET_KEY for persistent sessions")
	}
	if cfg.AdminPassword == defaultAdminPassword {
		if requireSecure {
			log.Fatal("SECURITY: ADMIN_PASSWORD is default while PLC_REQUIRE_SECURE=1")
		}
		log.Printf("SECURITY WARNING: default admin password is enabled; change ADMIN_PASSWORD before exposing the service")
	}
	return cfg
}

func newServer(cfg config) *server {
	cfg = applySecurityBaseline(cfg)
	st, err := openStore(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("open database failed: %v", err)
	}
	if err := st.EnsureSchema(cfg.AdminUsername, cfg.AdminPassword); err != nil {
		log.Fatalf("ensure schema failed: %v", err)
	}
	if err := st.EnsureSettingsSchema(); err != nil {
		log.Fatalf("ensure settings schema failed: %v", err)
	}
	srv := &server{
		cfg:   cfg,
		mux:   http.NewServeMux(),
		store: st,
		jobs:  newJobManager(),
	}
	srv.scheduler = newScheduler(srv)
	srv.routes()
	srv.scheduler.Start()
	return srv
}

func (s *server) routes() {
	s.mux.HandleFunc("GET /health", s.handleHealth)
	s.mux.HandleFunc("POST /api/auth/login", s.handleLogin)
	s.mux.HandleFunc("GET /api/bootstrap", s.withAuth(s.handleBootstrap))
	s.mux.HandleFunc("GET /api/settings", s.withAuth(s.handleGetSettings))
	s.mux.HandleFunc("POST /api/settings", s.withAuth(s.handleSaveSettings))
	s.mux.HandleFunc("GET /api/stats", s.withAuth(s.handleStats))
	s.mux.HandleFunc("GET /api/sources", s.withAuth(s.handleSources))
	s.mux.HandleFunc("POST /api/sources/fetch-job", s.withAuth(s.handleFetchSourcesJob))
	s.mux.HandleFunc("GET /api/proxies", s.withAuth(s.handleProxies))
	s.mux.HandleFunc("POST /api/proxies/import", s.withAuth(s.handleImportProxies))
	s.mux.HandleFunc("DELETE /api/proxies/by-status", s.withAuth(s.handleDeleteProxiesByStatus))
	s.mux.HandleFunc("POST /api/checks/run-job", s.withAuth(s.handleRunCheckJob))
	s.mux.HandleFunc("GET /api/jobs/active", s.withAuth(s.handleActiveJobs))
	s.mux.HandleFunc("GET /api/jobs/{job_id}", s.withAuth(s.handleJobStatus))
	s.mux.HandleFunc("POST /api/jobs/{job_id}/cancel", s.withAuth(s.handleCancelJob))
	s.mux.HandleFunc("GET /api/gateway/status", s.withAuth(s.handleGatewayStatus))
	s.mux.HandleFunc("GET /api/export/proxies.txt", s.withExportAuth(s.handleExportTXT))
	s.mux.HandleFunc("GET /api/export/proxies.json", s.withExportAuth(s.handleExportJSON))
	s.mux.HandleFunc("GET /static/", s.handleStatic)
	s.mux.HandleFunc("GET /", s.handleIndex)
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]any{
		"app":     s.cfg.AppName,
		"version": s.cfg.AppVersion,
		"status":  "ok",
	})
}

func (s *server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var payload loginRequest
	if err := readJSON(r, &payload); err != nil {
		errorResponse(w, http.StatusBadRequest, "invalid json")
		return
	}
	ok, err := s.authenticate(payload.Username, payload.Password)
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		errorResponse(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	token, expiresAt, err := s.signToken(payload.Username)
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	jsonResponse(w, http.StatusOK, map[string]any{
		"access_token": token,
		"token_type":   "bearer",
		"expires_at":   expiresAt.UTC().Format(time.RFC3339),
	})
}

func (s *server) authenticate(username string, password string) (bool, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return false, nil
	}
	hash, found, err := s.store.UserPasswordHash(username)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	return hmac.Equal([]byte(hash), []byte(hashPassword(password))), nil
}

func (s *server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.Stats()
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	settings, err := s.store.AppSettings()
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"app": map[string]any{
			"name":    s.cfg.AppName,
			"version": s.cfg.AppVersion,
		},
		"stats":     stats,
		"sources":   builtinSources(),
		"settings":  settings,
		"scheduler": s.scheduler.Status(settings),
		"gateway":   s.gatewayPayload(),
		"exports": map[string]any{
			"txt":  "/api/export/proxies.txt",
			"json": "/api/export/proxies.json",
		},
		"active_jobs": s.jobs.Active(),
		"user": map[string]any{
			"username": s.usernameFromRequest(r),
		},
	})
}

func (s *server) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	settings, err := s.store.AppSettings()
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{
		"settings":  settings,
		"scheduler": s.scheduler.Status(settings),
	})
}

func (s *server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := readJSON(r, &payload); err != nil {
		errorResponse(w, http.StatusBadRequest, "invalid json")
		return
	}
	current, err := s.store.AppSettings()
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	settings, err := s.store.SaveAppSettings(settingsFromPayload(current, payload))
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.scheduler.Reset(settings)
	jsonResponse(w, http.StatusOK, map[string]any{
		"settings":  settings,
		"scheduler": s.scheduler.Status(settings),
	})
}

func (s *server) handleStats(w http.ResponseWriter, _ *http.Request) {
	stats, err := s.store.Stats()
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, stats)
}

func (s *server) handleSources(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]any{"items": builtinSources()})
}

func (s *server) handleFetchSourcesJob(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	_ = readJSON(r, &payload)
	result, err := s.StartFetchSourcesJob(payload)
	if err != nil {
		errorResponse(w, http.StatusConflict, err.Error())
		return
	}
	jsonResponse(w, http.StatusAccepted, result)
}

func (s *server) handleProxies(w http.ResponseWriter, r *http.Request) {
	limit := parseIntQuery(r.URL.Query().Get("limit"), 200, 1, 1000)
	offset := clampInt(anyToInt(r.URL.Query().Get("offset")), 0, 10000000)
	filter := proxyFilter{
		Status:        r.URL.Query().Get("status"),
		TargetProfile: r.URL.Query().Get("target_profile"),
		Query:         r.URL.Query().Get("q"),
		Limit:         limit,
		Offset:        offset,
	}
	items, total, err := s.store.ListProxies(filter)
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *server) handleImportProxies(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := readJSON(r, &payload); err != nil {
		errorResponse(w, http.StatusBadRequest, "invalid json")
		return
	}
	result, err := s.store.ImportProxies(fmt.Sprint(payload["text"]), optionalString(payload["source"], "manual"), optionalString(payload["default_protocol"], "auto"))
	if err != nil {
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, result)
}

func (s *server) handleDeleteProxiesByStatus(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = r.URL.Query().Get("target")
	}
	result, err := s.store.DeleteProxiesByStatus(status)
	if err != nil {
		errorResponse(w, http.StatusBadRequest, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, result)
}

func (s *server) handleRunCheckJob(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	_ = readJSON(r, &payload)
	result, err := s.StartCheckJob(payload)
	if err != nil {
		errorResponse(w, http.StatusConflict, err.Error())
		return
	}
	jsonResponse(w, http.StatusAccepted, result)
}

func (s *server) handleActiveJobs(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, map[string]any{"items": s.jobs.Active()})
}

func (s *server) handleJobStatus(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobs.Get(r.PathValue("job_id"))
	if !ok {
		errorResponse(w, http.StatusNotFound, "job not found")
		return
	}
	jsonResponse(w, http.StatusOK, job)
}

func (s *server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobs.Cancel(r.PathValue("job_id"))
	if !ok {
		errorResponse(w, http.StatusNotFound, "job not found")
		return
	}
	jsonResponse(w, http.StatusOK, job)
}

func (s *server) handleGatewayStatus(w http.ResponseWriter, _ *http.Request) {
	jsonResponse(w, http.StatusOK, s.gatewayPayload())
}

func (s *server) gatewayPayload() map[string]any {
	payload := map[string]any{
		"enabled": s.cfg.GatewayEnabled,
		"host":    s.cfg.GatewayHost,
		"port":    s.cfg.GatewayPort,
	}
	if s.gateway != nil {
		for key, value := range s.gateway.Status() {
			payload[key] = value
		}
	}
	return payload
}

func (s *server) handleExportTXT(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ExportAvailable(exportTargetProfileQuery(r), clampInt(anyToInt(r.URL.Query().Get("limit")), 0, 100000))
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	lines := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		proxyURL := item.ProxyURL()
		if !seen[proxyURL] {
			seen[proxyURL] = true
			lines = append(lines, proxyURL)
		}
	}
	writeText(w, "text/plain; charset=utf-8", strings.Join(lines, "\n"))
}

func (s *server) handleExportJSON(w http.ResponseWriter, r *http.Request) {
	targetProfile := exportTargetProfileQuery(r)
	items, err := s.store.ExportAvailable(targetProfile, clampInt(anyToInt(r.URL.Query().Get("limit")), 0, 100000))
	if err != nil {
		errorResponse(w, http.StatusInternalServerError, err.Error())
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"items": items, "total": len(items), "target_profile": targetProfile})
}

func exportTargetProfileQuery(r *http.Request) string {
	values := []string{}
	for _, key := range []string{"target_profile", "target_profiles"} {
		for _, value := range r.URL.Query()[key] {
			values = append(values, anyToStringSlice(value)...)
		}
	}
	return strings.Join(values, ",")
}

func (s *server) handleStatic(w http.ResponseWriter, r *http.Request) {
	localPath := filepath.Join(s.cfg.WebDir, strings.TrimPrefix(r.URL.Path, "/"))
	if info, err := os.Stat(localPath); err == nil && !info.IsDir() {
		http.ServeFile(w, r, localPath)
		return
	}
	if serveEmbeddedWebFile(w, r, strings.TrimPrefix(r.URL.Path, "/")) {
		return
	}
	errorResponse(w, http.StatusNotFound, "static asset not found")
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		errorResponse(w, http.StatusNotFound, "api endpoint not found")
		return
	}
	path := filepath.Join(s.cfg.WebDir, strings.TrimPrefix(r.URL.Path, "/"))
	if r.URL.Path == "/" {
		path = filepath.Join(s.cfg.WebDir, "index.html")
	}
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		http.ServeFile(w, r, path)
		return
	}
	requested := strings.TrimPrefix(r.URL.Path, "/")
	if requested != "" && serveEmbeddedWebFile(w, r, requested) {
		return
	}
	if serveEmbeddedWebFile(w, r, "index.html") {
		return
	}
	http.ServeFile(w, r, filepath.Join(s.cfg.WebDir, "index.html"))
}

func serveEmbeddedWebFile(w http.ResponseWriter, r *http.Request, name string) bool {
	name = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(name)), "/")
	if name == "" {
		name = "index.html"
	}
	raw, err := webassets.FS.ReadFile(name)
	if err != nil {
		return false
	}
	http.ServeContent(w, r, filepath.Base(name), time.Time{}, bytes.NewReader(raw))
	return true
}

func (s *server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.verifyRequestToken(r); err != nil {
			errorResponse(w, http.StatusUnauthorized, "login required")
			return
		}
		next(w, r)
	}
}

func (s *server) withExportAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.ExportToken != "" && hmac.Equal([]byte(r.URL.Query().Get("token")), []byte(s.cfg.ExportToken)) {
			next(w, r)
			return
		}
		if _, err := s.verifyRequestToken(r); err != nil {
			errorResponse(w, http.StatusUnauthorized, "export token or login required")
			return
		}
		next(w, r)
	}
}

func (s *server) usernameFromRequest(r *http.Request) string {
	username, _ := s.verifyRequestToken(r)
	return username
}

func (s *server) signToken(username string) (string, time.Time, error) {
	expiresAt := time.Now().Add(time.Duration(s.cfg.AccessTokenMinutes) * time.Minute)
	payload := map[string]any{
		"sub": strings.TrimSpace(username),
		"exp": expiresAt.Unix(),
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return "", time.Time{}, err
	}
	body := base64.RawURLEncoding.EncodeToString(rawPayload)
	sig := hmac.New(sha256.New, []byte(s.cfg.SecretKey))
	_, _ = sig.Write([]byte(body))
	token := body + "." + base64.RawURLEncoding.EncodeToString(sig.Sum(nil))
	return token, expiresAt, nil
}

func (s *server) verifyRequestToken(r *http.Request) (string, error) {
	token := ""
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		token = strings.TrimSpace(auth[7:])
	}
	if token == "" {
		if cookie, err := r.Cookie(authCookieName); err == nil {
			token = cookie.Value
		}
	}
	if token == "" {
		return "", errors.New("missing token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return "", errors.New("invalid token")
	}
	sig := hmac.New(sha256.New, []byte(s.cfg.SecretKey))
	_, _ = sig.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(sig.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[1])) {
		return "", errors.New("bad signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", err
	}
	exp := int64(anyToInt(payload["exp"]))
	if exp <= time.Now().Unix() {
		return "", errors.New("expired token")
	}
	username := strings.TrimSpace(fmt.Sprint(payload["sub"]))
	if username == "" {
		return "", errors.New("missing subject")
	}
	return username, nil
}

func optionalLimit(value any, fallback int, maxValue int) int {
	limit := anyToInt(value)
	if limit <= 0 {
		limit = fallback
	}
	return clampInt(limit, 1, maxValue)
}

func optionalString(value any, fallback string) string {
	text := strings.TrimSpace(fmt.Sprint(value))
	if text == "" || text == "<nil>" {
		return fallback
	}
	return text
}

func parseBool(value any, fallback bool) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "1", "true", "yes", "y", "on":
			return true
		case "0", "false", "no", "n", "off":
			return false
		}
	}
	return fallback
}

func parseIntQuery(value string, fallback int, minValue int, maxValue int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return clampInt(parsed, minValue, maxValue)
}
