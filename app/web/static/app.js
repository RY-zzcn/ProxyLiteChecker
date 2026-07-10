const state = {
  token: localStorage.getItem("plc_token") || "",
  sources: [],
  settings: null,
  scheduler: null,
  stats: null,
  geoip: null,
  activeJob: null,
  watchedJobId: null,
  pollTimer: null,
  gatewayTimer: null,
  liveCycle: 0,
  runtime: null,
  inFlightRequests: new Map(),
  sourceSelected: 0,
  proxies: {
    offset: 0,
    limit: 50,
    total: 0,
  },
};

const TARGET_OPTIONS = ["generic", "openai", "grok", "gemini", "claude"];
const TARGET_LABELS = {
  generic: "常规",
  openai: "OpenAI",
  grok: "Grok",
  gemini: "Gemini",
  claude: "Claude",
  all: "全部目标",
};

const BEIJING_TIME_FORMATTER = new Intl.DateTimeFormat("zh-CN", {
  timeZone: "Asia/Shanghai",
  year: "numeric",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
  hourCycle: "h23",
});
const NUMBER_FORMATTER = new Intl.NumberFormat("zh-CN");
const THEME_STORAGE_KEY = "plc_theme";

const el = (id) => document.getElementById(id);

function formatBeijingDate(date) {
  const parts = Object.fromEntries(BEIJING_TIME_FORMATTER.formatToParts(date).map((part) => [part.type, part.value]));
  return `${parts.year}-${parts.month}-${parts.day} ${parts.hour}:${parts.minute}:${parts.second}`;
}

function displayTimestamp(value, fallback = "-") {
  if (!value) return fallback;
  const text = String(value).trim();
  if (!text) return fallback;
  const normalized = text.includes("T") ? text : text.replace(" ", "T");
  const hasTimezone = /(?:Z|[+-]\d{2}:?\d{2})$/i.test(normalized);
  const date = new Date(hasTimezone ? normalized : `${normalized}+08:00`);
  return Number.isNaN(date.getTime()) ? text : formatBeijingDate(date);
}

function freshnessText(payload, fallback = "") {
  if (!payload) return fallback;
  const cacheAge = Math.max(0, Number(payload.cache_age_ms || 0));
  const generated = payload.generated_at ? displayTimestamp(payload.generated_at, "") : "";
  const ageText = cacheAge < 1000 ? "刚更新" : cacheAge < 60000 ? `${Math.round(cacheAge / 1000)} 秒前` : `${Math.round(cacheAge / 60000)} 分钟前`;
  return generated ? `${ageText} · ${generated}` : ageText;
}

function durationText(milliseconds) {
  const value = Math.max(0, Number(milliseconds || 0));
  if (value < 1000) return `${value} ms`;
  if (value < 60000) return `${Math.round(value / 1000)} 秒`;
  return `${Math.round(value / 60000)} 分钟`;
}

function uptimeText(seconds) {
  let remaining = Math.max(0, Math.floor(Number(seconds || 0)));
  const days = Math.floor(remaining / 86400);
  remaining %= 86400;
  const hours = Math.floor(remaining / 3600);
  remaining %= 3600;
  const minutes = Math.floor(remaining / 60);
  const secs = remaining % 60;
  if (days) return `${days} 天 ${hours} 小时 ${minutes} 分`;
  if (hours) return `${hours} 小时 ${minutes} 分 ${secs} 秒`;
  if (minutes) return `${minutes} 分 ${secs} 秒`;
  return `${secs} 秒`;
}

function formatNumber(value) {
  return NUMBER_FORMATTER.format(Number(value || 0));
}

function applyTheme(theme, persist = false) {
  const next = theme === "dark" ? "dark" : "light";
  document.documentElement.dataset.theme = next;
  if (persist) localStorage.setItem(THEME_STORAGE_KEY, next);
  const button = el("themeToggle");
  if (button) {
    const dark = next === "dark";
    button.textContent = dark ? "浅色" : "深色";
    button.setAttribute("aria-label", dark ? "切换浅色主题" : "切换深色主题");
    button.setAttribute("aria-pressed", String(dark));
  }
  const themeColor = document.querySelector('meta[name="theme-color"]');
  if (themeColor) themeColor.content = next === "dark" ? "#0b1220" : "#f4f7fb";
}

function initializeInterface() {
  const stored = localStorage.getItem(THEME_STORAGE_KEY);
  const current = stored || document.documentElement.dataset.theme || "light";
  applyTheme(current);
  if (window.matchMedia("(max-width: 900px)").matches) {
    document.querySelectorAll(".settings-group").forEach((group) => group.removeAttribute("open"));
  }
  setupSectionNavigation();
}

function setupSectionNavigation() {
  const links = [...document.querySelectorAll("[data-section-link]")];
  const sections = links.map((link) => document.getElementById(link.dataset.sectionLink)).filter(Boolean);
  if (!links.length || !sections.length) return;
  const activate = (id) => {
    links.forEach((link) => {
      const active = link.dataset.sectionLink === id;
      link.classList.toggle("active", active);
      if (active) link.setAttribute("aria-current", "location");
      else link.removeAttribute("aria-current");
    });
  };
  let scheduled = false;
  let navigationLock = "";
  let navigationUnlockTimer = null;
  const stickyHeaderOffset = () => {
    const header = document.querySelector(".workspace-header");
    if (!header || getComputedStyle(header).position !== "sticky") return 12;
    return Math.ceil(header.getBoundingClientRect().bottom);
  };
  const updateFromScroll = () => {
    scheduled = false;
    if (navigationLock) {
      activate(navigationLock);
      return;
    }
    const threshold = stickyHeaderOffset() + 2;
    let current = sections[0];
    for (const section of sections) {
      if (section.getBoundingClientRect().top <= threshold) current = section;
      else break;
    }
    if (window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 8) {
      current = sections[sections.length - 1];
    }
    activate(current.id);
  };
  const scheduleUpdate = () => {
    if (scheduled) return;
    scheduled = true;
    requestAnimationFrame(updateFromScroll);
  };
  links.forEach((link) =>
    link.addEventListener("click", (event) => {
      event.preventDefault();
      const section = document.getElementById(link.dataset.sectionLink);
      if (!section) return;
      const spacer = el("sectionScrollSpacer");
      navigationLock = section.id;
      clearTimeout(navigationUnlockTimer);
      activate(section.id);
      if (spacer) spacer.style.height = "0px";
      const offset = stickyHeaderOffset();
      const targetTop = Math.max(0, window.scrollY + section.getBoundingClientRect().top - offset);
      const maximumScroll = Math.max(0, document.documentElement.scrollHeight - window.innerHeight);
      if (spacer && targetTop > maximumScroll) {
        spacer.style.height = `${Math.ceil(targetTop - maximumScroll + 12)}px`;
        void spacer.offsetHeight;
      }
      const root = document.documentElement;
      const previousScrollBehavior = root.style.scrollBehavior;
      root.style.scrollBehavior = "auto";
      window.scrollTo(0, targetTop);
      root.style.scrollBehavior = previousScrollBehavior;
      history.replaceState(null, "", `#${section.id}`);
      navigationUnlockTimer = setTimeout(() => {
        navigationLock = "";
        scheduleUpdate();
      }, 120);
    }),
  );
  window.addEventListener("scroll", scheduleUpdate, { passive: true });
  window.addEventListener("resize", scheduleUpdate);
  activate(sections[0].id);
  scheduleUpdate();
}

async function api(path, options = {}) {
  const method = String(options.method || "GET").toUpperCase();
  const requestKey = method === "GET" ? `${method}:${path}` : "";
  if (requestKey && state.inFlightRequests.has(requestKey)) {
    return state.inFlightRequests.get(requestKey);
  }
  const request = performAPIRequest(path, options);
  if (!requestKey) return request;
  state.inFlightRequests.set(requestKey, request);
  try {
    return await request;
  } finally {
    state.inFlightRequests.delete(requestKey);
  }
}

async function performAPIRequest(path, options = {}) {
  const headers = new Headers(options.headers || {});
  if (!headers.has("Content-Type") && options.body) {
    headers.set("Content-Type", "application/json");
  }
  if (state.token) {
    headers.set("Authorization", `Bearer ${state.token}`);
  }
  const response = await fetch(path, { ...options, headers });
  if (response.status === 401) {
    showLogin();
    throw new Error("login required");
  }
  const text = await response.text();
  const payload = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new Error(payload.message || payload.error || `HTTP ${response.status}`);
  }
  return payload;
}

function showLogin() {
  stopJobPolling();
  stopGatewayPolling();
  el("loginView").hidden = false;
  el("appView").hidden = true;
}

function showApp() {
  el("loginView").hidden = true;
  el("appView").hidden = false;
}

function pct(done, total) {
  if (!total) return 0;
  return Math.max(0, Math.min(100, Math.round((done / total) * 100)));
}

function escapeHtml(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

async function copyText(value) {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return;
    } catch {
      // Fall through to the textarea copy path for non-secure public HTTP access.
    }
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.left = "-9999px";
  textarea.style.top = "0";
  document.body.appendChild(textarea);
  textarea.select();
  document.execCommand("copy");
  textarea.remove();
}

function statusTag(status) {
  const text = { available: "可用", failed: "失败", untested: "待检", checking: "检测中" }[status] || status;
  return `<span class="tag ${escapeHtml(status)}">${escapeHtml(text)}</span>`;
}

function normalizeTargetValues(values, allowAll = false) {
  const list = Array.isArray(values) ? values : String(values || "").split(",");
  const out = [];
  for (const value of list) {
    const target = String(value || "").trim().toLowerCase();
    if (allowAll && target === "all") {
      return ["all"];
    }
    if (TARGET_OPTIONS.includes(target) && !out.includes(target)) {
      out.push(target);
    }
  }
  return out.length ? out : ["generic"];
}

function getTargetSelections(containerId) {
  const selected = [...document.querySelectorAll(`#${containerId} input:checked`)].map((input) => input.value);
  return normalizeTargetValues(selected);
}

function setTargetSelections(containerId, values) {
  const selected = new Set(normalizeTargetValues(values));
  document.querySelectorAll(`#${containerId} input`).forEach((input) => {
    input.checked = selected.has(input.value);
  });
}

function ensureTargetSelection(containerId) {
  const hasChecked = [...document.querySelectorAll(`#${containerId} input`)].some((input) => input.checked);
  if (!hasChecked) {
    const first = document.querySelector(`#${containerId} input[value="generic"]`);
    if (first) first.checked = true;
  }
}

function targetLabel(value) {
  return TARGET_LABELS[value] || value || "常规";
}

function normalizeCountryCodesInput(value) {
  const list = Array.isArray(value) ? value : String(value || "").replaceAll("，", ",").split(",");
  const out = [];
  for (const item of list) {
    const code = String(item || "").trim().toUpperCase();
    if (code && code !== "ALL" && /^[A-Z]{2}$/.test(code) && !out.includes(code)) {
      out.push(code);
    }
  }
  return out.sort();
}

function countrySummary(countries, policy) {
  const list = normalizeCountryCodesInput(countries);
  if (!list.length) return "全部地区";
  const suffix = String(policy || "strict") === "fallback_any" ? " · 可回退" : "";
  return `${list.join(",")}${suffix}`;
}

function defaultSettings() {
  return {
    proxy_page_size: 50,
    fetch_limit_per_source: 0,
    auto_fetch_low_stock_enabled: false,
    auto_fetch_untested_minimum: 5000,
    auto_fetch_cooldown_minutes: 30,
    check_status: "untested",
    check_target_profile: "generic",
    check_target_profiles: ["generic"],
    check_limit: 500,
    check_concurrent: 100,
    check_rounds: 1,
    check_request_timeout: 6,
    check_hard_timeout: 60,
    delete_failed_on_check: false,
    recheck_expired_enabled: false,
    available_ttl_hours: 24,
    delete_expired_untested: false,
    untested_ttl_hours: 72,
    auto_fetch_enabled: false,
    auto_fetch_interval_minutes: 360,
    auto_fetch_source_ids: [],
    auto_check_enabled: false,
    auto_check_interval_minutes: 120,
    target_low_stock_enabled: false,
    target_low_stock_profiles: ["generic"],
    target_low_stock_minimum: 50,
    target_candidate_minimum: 200,
    check_after_fetch_enabled: true,
    gateway_upstream_limit: 200,
    gateway_upstream_strategy: "round_robin",
    gateway_countries: [],
    gateway_country_policy: "strict",
    gateway_retry_attempts: 2,
    gateway_failure_threshold: 3,
    gateway_failure_cooldown_seconds: 300,
    gateway_request_timeout_seconds: 20,
    export_target_profile: "generic",
  };
}

function proxyUrl(item) {
  const protocol = item.detected_protocol || item.protocol || "http";
  const auth = item.username ? `${item.username}:***@` : "";
  return `${protocol}://${auth}${item.ip}:${item.port}`;
}

function proxyGeoLabel(item) {
  const code = String(item?.country || "").trim().toUpperCase();
  const name = String(item?.country_name || "").trim();
  if (code && name) return `${code} ${name}`;
  return code || name || "";
}

function toast(message, type = "info") {
  const host = el("toastHost");
  const item = document.createElement("div");
  item.className = `toast ${type}`;
  item.setAttribute("role", type === "error" ? "alert" : "status");
  item.textContent = message;
  host.appendChild(item);
  setTimeout(() => item.remove(), 3200);
}

function setBusy(button, busy, label) {
  if (!button) return;
  if (!button.dataset.originalHtml) {
    button.dataset.originalHtml = button.innerHTML;
  }
  button.classList.toggle("is-loading", busy);
  button.disabled = busy;
  button.setAttribute("aria-busy", String(busy));
  if (busy) {
    button.textContent = label || "处理中";
  } else {
    button.innerHTML = button.dataset.originalHtml;
  }
}

async function withButton(button, label, task) {
  setBusy(button, true, label);
  try {
    return await task();
  } finally {
    setBusy(button, false);
  }
}

async function login(event) {
  event.preventDefault();
  el("loginError").textContent = "";
  await withButton(event.submitter, "登录中", async () => {
    try {
      const payload = await api("/api/auth/login", {
        method: "POST",
        body: JSON.stringify({
          username: el("username").value.trim(),
          password: el("password").value,
        }),
      });
      state.token = payload.access_token;
      localStorage.setItem("plc_token", state.token);
      await bootstrap();
      toast("登录成功", "success");
    } catch (error) {
      el("loginError").textContent = error.message;
    }
  });
}

async function bootstrap() {
  const payload = await api("/api/bootstrap");
  showApp();
  el("versionText").textContent = `v${payload.app.version}`;
  state.sources = payload.sources || [];
  state.settings = { ...defaultSettings(), ...(payload.settings || {}) };
  state.scheduler = payload.scheduler || null;
  state.proxies.limit = Number(state.settings.proxy_page_size || 50);
  renderStats(payload.stats || {});
  renderSettings(state.settings, state.scheduler);
  renderSources();
  renderGateway(payload.gateway || {});
  const activeJobs = payload.active_jobs || [];
  renderJobs(activeJobs.length ? activeJobs : payload.recent_jobs || []);
  if (activeJobs.length) {
    state.watchedJobId = activeJobs[0].id;
    startPolling();
  }
  loadGeoIP().catch(() => {});
  await Promise.all([loadProxies(), loadRuntime()]);
  if (!state.pollTimer) startGatewayPolling();
}

function renderStats(stats) {
  state.stats = stats || {};
  el("statTotal").textContent = formatNumber(stats.total);
  el("statAvailable").textContent = formatNumber(stats.available);
  el("statUntested").textContent = formatNumber(stats.untested);
  el("statFailed").textContent = formatNumber(stats.failed);
  const targetText = (stats.by_target || [])
    .filter((item) => Number(item.available || 0) > 0)
    .map((item) => `${targetLabel(item.target_profile)} ${Number(item.available || 0)}`)
    .join(" / ");
  const availableMetric = el("statAvailable").closest(".metric");
  if (availableMetric) {
    const recordText = Number(stats.available_records || 0) > Number(stats.available || 0) ? `；记录 ${Number(stats.available_records || 0)}` : "";
    const transportText = `；基础链路可用 ${Number(stats.transport_available || 0)}`;
    const freshText = freshnessText(stats);
    availableMetric.title = targetText
      ? `目标能力摘要：${targetText}${recordText}${transportText}；${freshText}`
      : `暂无目标可用上游${transportText}；${freshText}`;
  }
}

function renderSources() {
  const selectedIDs = new Set((state.settings?.auto_fetch_source_ids || []).map(String));
  const useAll = selectedIDs.size === 0;
  el("sourceCountText").textContent = `${state.sources.length} 个代理源`;
  el("sourcesList").innerHTML = state.sources
    .map(
      (source) => {
        const health = source.health || source;
        const status = sourceHealthLabel(health);
        return `
        <label class="source-item" title="${escapeHtml(source.url)}">
          <input type="checkbox" class="source-check" value="${escapeHtml(source.id)}" ${useAll || selectedIDs.has(source.id) ? "checked" : ""} />
          <span>${escapeHtml(source.name)}</span>
          <small>${escapeHtml(status)}</small>
        </label>
      `;
      },
    )
    .join("");
  document.querySelectorAll(".source-check").forEach((input) => {
    input.addEventListener("change", updateSelectedSources);
  });
  updateSelectedSources();
}

function sourceHealthLabel(health) {
  if (!health || !health.last_fetch_status) return "未拉取";
  if (health.last_fetch_status === "success") {
    return `成功 新增 ${Number(health.last_new || 0)} / 更新 ${Number(health.last_updated || 0)}`;
  }
  const disabled = health.disabled_until ? ` 冷却至 ${displayTimestamp(health.disabled_until, "")}` : "";
  return `失败 ${Number(health.failure_streak || 0)}${disabled}`;
}

function renderSettings(settings, scheduler) {
  state.settings = { ...defaultSettings(), ...(settings || {}) };
  state.scheduler = scheduler || state.scheduler;
  el("autoFetchEnabled").checked = Boolean(state.settings.auto_fetch_enabled);
  el("autoFetchInterval").value = state.settings.auto_fetch_interval_minutes;
  el("sourceLimit").value = state.settings.fetch_limit_per_source;
  el("settingsFetchLimit").value = state.settings.fetch_limit_per_source;
  el("autoFetchLowStockEnabled").checked = Boolean(state.settings.auto_fetch_low_stock_enabled);
  el("autoFetchUntestedMinimum").value = state.settings.auto_fetch_untested_minimum;
  el("autoFetchCooldown").value = state.settings.auto_fetch_cooldown_minutes;
  el("autoCheckEnabled").checked = Boolean(state.settings.auto_check_enabled);
  el("autoCheckInterval").value = state.settings.auto_check_interval_minutes;
  el("targetLowStockEnabled").checked = Boolean(state.settings.target_low_stock_enabled);
  el("targetLowStockMinimum").value = state.settings.target_low_stock_minimum;
  el("targetCandidateMinimum").value = state.settings.target_candidate_minimum;
  el("checkAfterFetchEnabled").checked = state.settings.check_after_fetch_enabled !== false;
  el("deleteFailedOnCheck").checked = Boolean(state.settings.delete_failed_on_check);
  el("recheckExpiredEnabled").checked = Boolean(state.settings.recheck_expired_enabled);
  el("availableTtlHours").value = state.settings.available_ttl_hours;
  el("deleteExpiredUntestedEnabled").checked = Boolean(state.settings.delete_expired_untested);
  el("untestedTtlHours").value = state.settings.untested_ttl_hours;
  el("proxyPageSize").value = String(state.settings.proxy_page_size);
  el("settingsCheckStatus").value = state.settings.check_status;
  const checkTargets = state.settings.check_target_profiles || [state.settings.check_target_profile || "generic"];
  setTargetSelections("settingsCheckTargets", checkTargets);
  el("settingsCheckLimit").value = state.settings.check_limit;
  el("quickCheckStatus").value = state.settings.check_status;
  setTargetSelections("quickCheckTargets", checkTargets);
  el("quickCheckLimit").value = state.settings.check_limit;
  el("settingsCheckConcurrent").value = state.settings.check_concurrent;
  el("settingsCheckRounds").value = state.settings.check_rounds;
  el("settingsCheckTimeout").value = state.settings.check_request_timeout;
  el("settingsCheckHardTimeout").value = state.settings.check_hard_timeout;
  el("gatewayUpstreamLimit").value = state.settings.gateway_upstream_limit;
  el("gatewayUpstreamStrategy").value = state.settings.gateway_upstream_strategy;
  el("gatewayCountries").value = normalizeCountryCodesInput(state.settings.gateway_countries).join(",");
  el("gatewayCountryPolicy").value = state.settings.gateway_country_policy || "strict";
  el("gatewayRetryAttempts").value = state.settings.gateway_retry_attempts;
  el("gatewayFailureThreshold").value = state.settings.gateway_failure_threshold;
  el("gatewayFailureCooldown").value = state.settings.gateway_failure_cooldown_seconds;
  el("gatewayRequestTimeout").value = state.settings.gateway_request_timeout_seconds;
  el("exportTarget").value = normalizeTargetValues(state.settings.export_target_profile, true)[0];
  updateExportLinks();
  renderSchedulerText(state.scheduler);
}

function renderSchedulerText(scheduler) {
  const parts = [];
  if (scheduler?.fetch?.enabled) {
    parts.push(`拉取 ${displayNextRun(scheduler.fetch.next_run_at)}`);
  }
  if (scheduler?.fetch?.low_stock_enabled) {
    parts.push(`待检 ${scheduler.fetch.last_untested_count ?? 0}/${scheduler.fetch.untested_minimum}`);
  }
  if (scheduler?.check?.enabled) {
    parts.push(`检测 ${displayNextRun(scheduler.check.next_run_at)}`);
  }
  if (scheduler?.maintenance?.expired_requeued || scheduler?.maintenance?.failed_deleted || scheduler?.maintenance?.untested_deleted) {
    parts.push(
      `维护 失败 ${scheduler.maintenance.failed_deleted || 0} / 回检 ${scheduler.maintenance.expired_requeued || 0} / 待检 ${scheduler.maintenance.untested_deleted || 0}`,
    );
  }
  if (scheduler?.blocking_job) {
    parts.push(`阻塞 ${scheduler.blocking_job.type} #${scheduler.blocking_job.id}`);
  }
  if ((scheduler?.pending || []).length) {
    parts.push(`延后 ${(scheduler.pending || []).map((item) => item.reason).join("/")}`);
  }
  const backoff = (scheduler?.states || []).find((item) => item.backoff_until);
  if (backoff) parts.push(`退避至 ${displayTimestamp(backoff.backoff_until, backoff.backoff_until)}`);
  el("schedulerText").textContent = parts.length ? parts.join(" · ") : scheduler?.message || "自动任务未启用";
}

function displayNextRun(value) {
  if (!value) return "等待排程";
  const text = displayTimestamp(value, "");
  return text ? `下次 ${text}` : "等待排程";
}

function updateSelectedSources() {
  state.sourceSelected = document.querySelectorAll(".source-check:checked").length;
  el("selectedSourcesText").textContent = `已选 ${state.sourceSelected} 个`;
}

function renderGateway(gateway) {
  const profiles = (gateway.profiles || []).length ? gateway.profiles : [gateway];
  const enabledProfiles = profiles.filter((item) => item && (item.http_enabled || item.socks5_enabled || gateway.enabled));
  const loadedSlots = Number(gateway.loaded_upstreams ?? profiles.reduce((total, item) => total + gatewayLoadedUpstreams(item), 0));
  const activeSlots = Number(gateway.active_upstreams ?? profiles.reduce((total, item) => total + gatewayActiveUpstreams(item), 0));
  const skippedSlots = Number(gateway.skipped_upstreams ?? profiles.reduce((total, item) => total + gatewaySkippedUpstreams(item), 0));
  const uniqueAvailable = Number(
    gateway.unique_available_upstreams ?? gateway.available_upstreams ?? profiles.reduce((total, item) => total + gatewayAvailableUpstreams(item), 0),
  );
  const upstreamLimit = gatewayUpstreamLimit(gateway, profiles);
  const limitText = upstreamLimit > 0 ? `单目标上限 ${upstreamLimit}` : "单目标上限未设置";
  const countryText = countrySummary(gateway.countries || state.settings?.gateway_countries || [], gateway.country_policy || state.settings?.gateway_country_policy);
  const isolationText = skippedSlots > 0 ? ` · 活跃 ${activeSlots} / 隔离 ${skippedSlots}` : "";
  const closed = Number(gateway.closed_upstreams ?? profiles.reduce((total, item) => total + Number(item.closed_upstreams || 0), 0));
  const open = Number(gateway.open_upstreams ?? profiles.reduce((total, item) => total + Number(item.open_upstreams || 0), 0));
  const halfOpen = Number(gateway.half_open_upstreams ?? profiles.reduce((total, item) => total + Number(item.half_open_upstreams || 0), 0));
  const circuitText = ` · 电路 ${closed}/${open}/${halfOpen}`;
  const degradedText = gateway.degraded ? " · 降级选路" : "";
  const poolAgeText = gateway.pool_age_ms != null ? ` · 池龄 ${durationText(gateway.pool_age_ms)}` : "";
  const freshText = freshnessText(gateway);
  const totalRequests = Number(gateway.valid_requests ?? gateway.total_requests ?? profiles.reduce((total, item) => total + Number(item.valid_requests ?? item.total_requests ?? 0), 0));
  const totalConnections = Number(gateway.total_connections ?? profiles.reduce((total, item) => total + Number(item.total_connections || 0), 0));
  el("gatewaySummary").textContent = gateway.enabled
    ? `${enabledProfiles.length} 个目标入口 · ${limitText} · ${countryText} · 已装载 ${loadedSlots} 个目标槽位${isolationText}${circuitText}${degradedText}${poolAgeText} · 唯一可用 ${uniqueAvailable} 个上游 · 有效请求 ${totalRequests} / 连接 ${totalConnections} · ${freshText}`
    : "网关未启用";
  el("gatewayCards").innerHTML =
    profiles
      .map((item) => gatewayCardHTML(item, gateway.enabled))
      .join("") || `<article class="gateway-card empty-row">网关未启用</article>`;
  renderGatewayEvents(gateway.events || []);
  const pill = el("gatewayPill");
  pill.textContent = gateway.enabled ? `网关 ${enabledProfiles.length} 目标 · ${loadedSlots} 槽位 · ${uniqueAvailable} 上游` : "网关未启用";
  pill.title = gateway.enabled
    ? `${limitText}；地区 ${countryText}；策略 ${gatewayStrategyLabel(gateway.upstream_strategy)}；重试 ${Number(gateway.retry_attempts || 1)} 次；保存设置后热生效`
    : "";
  pill.classList.toggle("offline", !gateway.enabled);
}

function renderGatewayEvents(events) {
  const container = el("gatewayEvents");
  const rows = (events || []).slice(0, 8);
  if (!rows.length) {
    container.innerHTML = "";
    return;
  }
  container.innerHTML = `
    <div class="gateway-events-head">
      <strong>网关诊断</strong>
      <span>最近 ${rows.length} 条异常事件</span>
    </div>
    <div class="gateway-event-list">
      ${rows
        .map(
          (event) => `
            <div class="gateway-event-row" title="${escapeHtml(event.message || "")}">
              <span>${escapeHtml(displayTimestamp(event.time, "-"))}</span>
              <span>${escapeHtml(targetLabel(event.target_profile))}</span>
              <span>${escapeHtml(event.gateway_type || "-")}</span>
              <code>${escapeHtml(event.upstream || event.client_ip || "-")}</code>
              <strong>${escapeHtml(gatewayEventLabel(event.event_type))}</strong>
            </div>
          `,
        )
        .join("")}
    </div>
  `;
}

function gatewayEventLabel(value) {
  return {
    rejected: "已拒绝",
    upstream_failure: "上游失败",
    request_failed: "请求失败",
  }[value] || value || "事件";
}

function gatewayUpstreamLimit(gateway, profiles) {
  const direct = Number(gateway?.upstream_limit || 0);
  if (direct > 0) return direct;
  for (const item of profiles || []) {
    const value = Number(item?.upstream_limit || 0);
    if (value > 0) return value;
  }
  return 0;
}

function gatewayLoadedUpstreams(item) {
  return Number(item?.loaded_upstreams ?? item?.upstreams ?? 0);
}

function gatewayActiveUpstreams(item) {
  return Number(item?.active_upstreams ?? gatewayLoadedUpstreams(item));
}

function gatewaySkippedUpstreams(item) {
  return Number(item?.skipped_upstreams ?? 0);
}

function gatewayAvailableUpstreams(item) {
  const loaded = gatewayLoadedUpstreams(item);
  return Number(item?.available_upstreams ?? loaded);
}

function gatewayUpstreamSummary(loaded, available) {
  if (available > loaded) {
    return `已装载 ${loaded}/${available} 个上游`;
  }
  return `${loaded} 个上游`;
}

function gatewayUpstreamPill(loaded, available) {
  if (available > loaded) {
    return `${loaded}/${available} 上游`;
  }
  return `${loaded} 上游`;
}

function gatewayCardHTML(item, gatewayEnabled) {
  const profile = item.target_profile || "generic";
  const httpBind = gatewayEnabled && item.http_enabled !== false ? displayGatewayBind(item, "http") : "未启用";
  const socks5Bind = gatewayEnabled && item.socks5_enabled ? displayGatewayBind(item, "socks5") : "未启用";
  const loadedUpstreams = gatewayLoadedUpstreams(item);
  const availableUpstreams = gatewayAvailableUpstreams(item);
  const activeUpstreams = gatewayActiveUpstreams(item);
  const skippedUpstreams = gatewaySkippedUpstreams(item);
  const regionText = countrySummary(item.countries || state.settings?.gateway_countries || [], item.country_policy || state.settings?.gateway_country_policy);
  const validRequests = Number(item.valid_requests ?? item.total_requests ?? 0);
  const totalConnections = Number(item.total_connections ?? validRequests);
  const rejectedRequests = Number(item.rejected_requests || 0);
  const upstreamAttempts = Number(item.upstream_attempts || 0);
  const closedUpstreams = Number(item.closed_upstreams || 0);
  const openUpstreams = Number(item.open_upstreams || 0);
  const halfOpenUpstreams = Number(item.half_open_upstreams || 0);
  const poolAge = durationText(item.pool_age_ms || 0);
  const refreshError = String(item.last_refresh_error || "").trim();
  const recent = gatewayRecentRows(item)
    .map(
      (entry, index) => `
        <div class="recent-row${entry.value ? "" : " empty"}">
          <span>${entry.value ? (index === 0 ? "当前" : "最近") : index === 0 ? "等待" : ""}</span>
          <code title="${escapeHtml(entry.value || "")}">${escapeHtml(entry.value || (index === 0 ? "等待请求" : "-"))}</code>
        </div>
      `,
    )
    .join("");
  return `
    <article class="gateway-card">
      <div class="gateway-card-head">
        <strong>${escapeHtml(targetLabel(profile))}</strong>
        <span>${gatewayUpstreamPill(loadedUpstreams, availableUpstreams)}</span>
      </div>
      <div class="endpoint-pair">
        <div class="endpoint-row">
          <span>HTTP</span>
          <div class="endpoint-copy-row">
            <code>${escapeHtml(httpBind)}</code>
            <button type="button" class="copy-button" data-copy-gateway="${httpBind === "未启用" ? "" : escapeHtml(httpBind)}" ${httpBind === "未启用" ? "disabled" : ""}>复制</button>
          </div>
        </div>
        <div class="endpoint-row">
          <span>SOCKS5</span>
          <div class="endpoint-copy-row">
            <code>${escapeHtml(socks5Bind)}</code>
            <button type="button" class="copy-button" data-copy-gateway="${socks5Bind === "未启用" ? "" : escapeHtml(socks5Bind)}" ${socks5Bind === "未启用" ? "disabled" : ""}>复制</button>
          </div>
        </div>
      </div>
      <div class="gateway-card-meta">
        <span>有效 ${validRequests}</span>
        <span>连接 ${totalConnections}</span>
        <span>拒绝 ${rejectedRequests}</span>
        <span>上游尝试 ${upstreamAttempts}</span>
        <span>成功率 ${Math.round(Number(item.success_rate || 0) * 100)}%</span>
        <span>活跃 ${activeUpstreams}</span>
        <span>隔离 ${skippedUpstreams}</span>
        <span>电路 ${closedUpstreams}/${openUpstreams}/${halfOpenUpstreams}</span>
        <span>${item.degraded ? "降级" : "正常"}</span>
        <span title="${escapeHtml(refreshError)}">池龄 ${escapeHtml(poolAge)}${refreshError ? " · 刷新异常" : ""}</span>
        <span>${escapeHtml(regionText)}</span>
        <span>${escapeHtml(gatewayStrategyLabel(item.upstream_strategy))}</span>
      </div>
      <div class="recent-list">${recent}</div>
    </article>
  `;
}

function gatewayStrategyLabel(value) {
  const key = String(value || "round_robin").trim();
  return {
    round_robin: "轮询",
    lowest_latency: "低延迟",
    stability_first: "稳定优先",
  }[key] || key;
}

function gatewayRecentRows(item) {
  const values = [];
  const seen = new Set();
  const push = (value) => {
    const text = String(value || "").trim();
    if (!text || seen.has(text)) return;
    seen.add(text);
    values.push(text);
  };
  push(item.last_upstream);
  (item.recent_upstreams || []).forEach(push);
  return Array.from({ length: 5 }, (_, index) => ({ value: values[index] || "" }));
}

function displayGatewayBind(gateway, type) {
  const scheme = type === "socks5" ? "socks5" : "http";
  const hostKey = type === "socks5" ? "socks5_host" : "http_host";
  const portKey = type === "socks5" ? "socks5_port" : "http_port";
  const bindKey = type === "socks5" ? "socks5_bind" : "http_bind";
  const fallback = gateway[bindKey] || gateway.bind || `${gateway[hostKey] || gateway.host}:${gateway[portKey] || gateway.port}`;
  if (hasGatewayScheme(fallback)) {
    return fallback;
  }
  const bindText = String(fallback || "").trim();
  const host = gateway[hostKey] || gateway.host || gatewayHostFromBind(bindText);
  const port = gateway[portKey] || gatewayPortFromBind(bindText);
  if (host === "0.0.0.0" || host === "::" || bindText.startsWith("0.0.0.0:") || bindText.startsWith("[::]:")) {
    return gatewayURL(scheme, location.hostname || "服务器IP", port);
  }
  if (host && port) {
    return gatewayURL(scheme, host, port);
  }
  return gatewayURL(scheme, bindText, "");
}

function hasGatewayScheme(value) {
  return /^[a-z][a-z0-9+.-]*:\/\//i.test(String(value || "").trim());
}

function gatewayURL(scheme, host, port) {
  const cleanHost = gatewayURLHost(host);
  if (!cleanHost) return `${scheme}://`;
  return `${scheme}://${cleanHost}${port ? `:${port}` : ""}`;
}

function gatewayURLHost(host) {
  const text = String(host || "").trim();
  if (!text) return "";
  if (text.startsWith("[") && text.endsWith("]")) return text;
  if (text.includes(":")) return `[${text}]`;
  return text;
}

function gatewayPortFromBind(value) {
  const match = String(value || "").trim().match(/:(\d+)$/);
  return match ? match[1] : "";
}

function gatewayHostFromBind(value) {
  const text = String(value || "").trim();
  if (!text) return "";
  if (text.startsWith("[")) {
    const end = text.indexOf("]");
    return end > 0 ? text.slice(1, end) : "";
  }
  const port = gatewayPortFromBind(text);
  return port ? text.slice(0, -port.length - 1) : text;
}

function renderJobs(jobs) {
  const job = jobs.find((item) => ["queued", "running", "cancel_requested", "cancelling"].includes(item.status)) || jobs[0] || null;
  state.activeJob = job;
  if (!job) {
    el("taskSubtitle").textContent = "空闲";
    el("jobMessage").textContent = "等待操作";
    el("progressBar").style.width = "0%";
    el("jobProgress").setAttribute("aria-valuenow", "0");
    el("jobProgress").setAttribute("aria-valuetext", "空闲");
    el("cancelJobBtn").disabled = true;
    el("jobMeta").textContent = "当前无活动任务";
    el("jobTargets").innerHTML = "";
    return;
  }
  const percent = pct(job.done, job.total);
  const typeText = { fetch: "拉取", check: "检测" }[job.type] || job.type;
  const statusText = {
    running: "运行中",
    queued: "排队中",
    cancel_requested: "正在停止",
    cancelling: "正在收尾",
    completed: "已完成",
    partial: "部分完成",
    failed: "失败",
    cancelled: "已取消",
    interrupted: "重启中断",
  }[job.status] || job.status;
  const result = job.result || {};
  const proxyDone = Number(result.proxy_done || 0);
  const proxyTotal = Number(result.proxy_total || 0);
  const itemDone = Number(result.target_item_done ?? job.done ?? 0);
  const itemTotal = Number(result.target_item_total ?? job.total ?? 0);
  const progressText = job.type === "check" && proxyTotal > 0
    ? `代理 ${formatNumber(proxyDone)}/${formatNumber(proxyTotal)} · 目标项 ${formatNumber(itemDone)}/${formatNumber(itemTotal)}`
    : `${formatNumber(job.done)}/${formatNumber(job.total)}`;
  el("taskSubtitle").textContent = `${typeText} · ${statusText} · ${progressText}`;
  el("jobMessage").textContent = job.message || `${typeText}${statusText}`;
  el("progressBar").style.width = `${percent}%`;
  el("jobProgress").setAttribute("aria-valuenow", String(percent));
  el("jobProgress").setAttribute("aria-valuetext", `${typeText}${statusText}，${percent}%`);
  el("cancelJobBtn").disabled = job.status !== "running";
  const concurrency = result.check_concurrency || {};
  const targetProfiles = result.target_profiles || job.params?.target_profiles || [];
  const modeText = result.execution_mode === "proxy_parallel_target_ordered"
    ? "代理并行；单代理内按所选目标顺序检测"
    : "任务按当前配置执行";
  const concurrencyText = job.type === "check"
    ? `并发 ${formatNumber(concurrency.active || 0)}/${formatNumber(concurrency.limit || job.params?.concurrent || 100)}（可热更新）`
    : "";
  el("jobMeta").textContent = [modeText, concurrencyText].filter(Boolean).join(" · ");
  el("jobTargets").innerHTML = renderJobTargetProgress(targetProfiles, result.target_progress || {});
}

function renderJobTargetProgress(profiles, progress) {
  return (profiles || [])
    .map((profile) => {
      const item = progress[profile] || {};
      const done = Number(item.done || 0);
      const total = Number(item.total || 0);
      const percent = pct(done, total);
      return `
        <div class="job-target-chip">
          <span><strong>${escapeHtml(targetLabel(profile))}</strong><small>${formatNumber(done)}/${formatNumber(total)}</small></span>
          <i aria-hidden="true"><b style="width:${percent}%"></b></i>
        </div>
      `;
    })
    .join("");
}

async function loadStats() {
  const stats = await api("/api/stats");
  renderStats(stats);
}

async function loadRuntime() {
  const runtime = await api("/api/runtime");
  renderRuntime(runtime);
}

function renderRuntime(runtime) {
  state.runtime = runtime || {};
  el("runtimeUptime").textContent = uptimeText(state.runtime.uptime_seconds);
  el("runtimeStartedAt").textContent = displayTimestamp(state.runtime.started_at);
  const concurrency = state.runtime.check_concurrency || {};
  el("runtimeConcurrency").textContent = `${formatNumber(concurrency.active || 0)} / ${formatNumber(concurrency.limit || 100)}`;
  const logs = (state.runtime.logs || []).slice(-30).reverse();
  el("runtimeLogs").innerHTML = logs.length
    ? logs
        .map(
          (entry) => `
            <div class="runtime-log-row ${escapeHtml(entry.level || "info")}">
              <time>${escapeHtml(displayTimestamp(entry.time))}</time>
              <span>${escapeHtml(runtimeLevelLabel(entry.level))}</span>
              <code>${escapeHtml(entry.message || "")}</code>
            </div>
          `,
        )
        .join("")
    : `<div class="runtime-log-empty">暂无运行日志</div>`;
}

function runtimeLevelLabel(level) {
  return { info: "信息", warning: "警告", error: "错误" }[String(level || "").toLowerCase()] || "信息";
}

async function loadGateway() {
  const gateway = await api("/api/gateway/status");
  renderGateway(gateway);
}

async function loadGeoIP() {
  const status = await api("/api/geoip/status");
  renderGeoIP(status);
}

function renderGeoIP(status) {
  state.geoip = status || {};
  const loaded = Boolean(state.geoip.country_loaded);
  const updated = state.geoip.last_update_at || state.geoip.last_load_at || "";
  const updatedText = updated ? ` · ${displayTimestamp(updated)}` : "";
  const source = state.geoip.country_source_hint || (loaded ? "mmdb" : "endpoint");
  const errorText = state.geoip.last_load_error ? ` · ${state.geoip.last_load_error}` : "";
  el("geoipStatusText").textContent = loaded ? `GeoIP 已加载 · ${source}${updatedText}${errorText}` : `GeoIP 未加载 · ${source}${errorText}`;
  el("geoipStatusText").title = state.geoip.country_path || state.geoip.last_load_error || "";
}

async function loadActiveJobs() {
  const payload = await api("/api/jobs/active");
  const jobs = payload.items || [];
  renderJobs(jobs);
  return jobs;
}

async function loadSettings() {
  const payload = await api("/api/settings");
  renderSettings(payload.settings, payload.scheduler);
}

async function loadScheduler() {
  const scheduler = await api("/api/scheduler/status");
  state.scheduler = scheduler;
  renderSchedulerText(scheduler);
}

async function loadSources() {
  const existingControls = [...document.querySelectorAll(".source-check")];
  const selected = new Set(existingControls.filter((input) => input.checked).map((input) => input.value));
  const payload = await api("/api/sources");
  state.sources = payload.items || [];
  renderSources();
  if (existingControls.length) {
    document.querySelectorAll(".source-check").forEach((input) => {
      input.checked = selected.has(input.value);
    });
    updateSelectedSources();
  }
}

async function loadProxies() {
  const params = new URLSearchParams({
    status: el("proxyStatus").value,
    target_profile: el("targetProfile").value,
    q: el("proxySearch").value.trim(),
    countries: normalizeCountryCodesInput(el("proxyCountry").value).join(","),
    limit: String(state.proxies.limit),
    offset: String(state.proxies.offset),
  });
  const payload = await api(`/api/proxies?${params}`);
  const rows = payload.items || [];
  state.proxies.total = payload.total || rows.length;
  state.proxies.limit = payload.limit || state.proxies.limit;
  state.proxies.offset = payload.offset || 0;
  el("proxyTableSummary").textContent = `${formatNumber(state.proxies.total)} 条记录`;
  el("proxyRows").innerHTML =
    rows
      .map(
        (item) => `
          <tr>
            <td data-label="代理" class="proxy-cell" title="${escapeHtml(proxyUrl(item))}">${escapeHtml(proxyUrl(item))}</td>
            <td data-label="状态">${statusTag(item.status)}${item.failure_reason ? `<span class="reason-tag">${escapeHtml(item.failure_reason)}</span>` : ""}</td>
            <td data-label="等级">${escapeHtml(item.grade || "-")}</td>
            <td data-label="延迟">${item.latency_ms == null ? "-" : `${item.latency_ms} ms`}</td>
            <td data-label="出口">${escapeHtml(item.exit_ip || "-")} ${proxyGeoLabel(item) ? `<span class="muted">${escapeHtml(proxyGeoLabel(item))}</span>` : ""}</td>
            <td data-label="用途">${escapeHtml(item.recommended_use || "-")}</td>
            <td data-label="来源">${escapeHtml(item.source || "-")}</td>
            <td data-label="更新时间">${escapeHtml(displayTimestamp(item.last_checked_at || item.updated_at))}</td>
          </tr>
        `,
      )
      .join("") || `<tr><td colspan="8" class="empty-row">暂无代理</td></tr>`;
  renderProxyPager(rows.length);
}

function renderProxyPager(rowCount) {
  const total = state.proxies.total;
  const limit = state.proxies.limit;
  const offset = state.proxies.offset;
  const page = total === 0 ? 1 : Math.floor(offset / limit) + 1;
  const pages = Math.max(1, Math.ceil(total / limit));
  const start = total === 0 ? 0 : offset + 1;
  const end = Math.min(offset + rowCount, total);
  el("proxyPageText").textContent = `${formatNumber(start)}-${formatNumber(end)} / ${formatNumber(total)} · 第 ${formatNumber(page)}/${formatNumber(pages)} 页`;
  el("prevProxyPage").disabled = offset <= 0;
  el("nextProxyPage").disabled = offset + limit >= total;
}

function resetProxyPage() {
  state.proxies.offset = 0;
  return loadProxies();
}

async function fetchSources(event) {
  if (state.sourceSelected === 0) {
    toast("请至少选择一个代理源", "error");
    return;
  }
  await withButton(event.currentTarget, "拉取中", async () => {
    const ids = [...document.querySelectorAll(".source-check:checked")].map((input) => input.value);
    const limit = Number(el("sourceLimit").value || 0);
    const payload = await api("/api/sources/fetch-job", {
      method: "POST",
      body: JSON.stringify({ source_ids: ids, limit_per_source: limit }),
    });
    toast("拉取任务已开始", "success");
    await watchJob(payload.job_id);
  });
}

async function importProxies(event) {
  const text = el("importText").value.trim();
  if (!text) {
    toast("没有可导入内容", "error");
    return;
  }
  await withButton(event.currentTarget, "导入中", async () => {
    const result = await api("/api/proxies/import", {
      method: "POST",
      body: JSON.stringify({ text, source: "manual", default_protocol: "auto" }),
    });
    el("importResult").textContent = `新增 ${result.added}，更新 ${result.updated}`;
    el("importText").value = "";
    toast("导入完成", "success");
    await refreshAll();
  });
}

async function runCheck(event) {
  const checkTimeout = Number(el("settingsCheckTimeout").value || state.settings?.check_request_timeout || 6);
  const hardTimeout = Number(el("settingsCheckHardTimeout").value || state.settings?.check_hard_timeout || checkTimeout * 10);
  const targetProfiles = getTargetSelections("quickCheckTargets");
  await withButton(event.currentTarget, "检测中", async () => {
    const payload = await api("/api/checks/run-job", {
      method: "POST",
      body: JSON.stringify({
        status: el("quickCheckStatus").value,
        target_profiles: targetProfiles,
        target_profile: targetProfiles[0],
        limit: Number(el("quickCheckLimit").value || 500),
        concurrent: Number(el("settingsCheckConcurrent").value || 100),
        rounds: Number(el("settingsCheckRounds").value || 1),
        request_timeout: checkTimeout,
        hard_timeout: Math.max(checkTimeout, hardTimeout),
      }),
    });
    toast(`检测任务已开始：${payload.count || 0} 条`, "success");
    await watchJob(payload.job_id);
  });
}

async function watchJob(jobId) {
  state.watchedJobId = String(jobId);
  const job = await api(`/api/jobs/${jobId}`);
  renderJobs([job]);
  startPolling();
}

function startPolling() {
  if (state.pollTimer) return;
  if (document.hidden || el("appView").hidden) return;
  stopGatewayPolling();
  const poll = async () => {
    state.pollTimer = null;
    if (document.hidden || el("appView").hidden) return;
    try {
      const cycle = ++state.liveCycle;
      let job = null;
      if (state.watchedJobId) {
        const requests = [api(`/api/jobs/${state.watchedJobId}`), loadStats(), loadRuntime()];
        if (cycle % 2 === 0) requests.push(loadGateway(), loadProxies());
        if (cycle % 2 === 0 && state.activeJob?.type === "fetch") requests.push(loadSources());
        if (cycle % 5 === 0) requests.push(loadScheduler());
        [job] = await Promise.all(requests);
        renderJobs([job]);
      } else {
        const requests = [loadActiveJobs(), loadStats(), loadRuntime()];
        if (cycle % 2 === 0) requests.push(loadGateway(), loadProxies());
        if (cycle % 2 === 0 && state.activeJob?.type === "fetch") requests.push(loadSources());
        if (cycle % 5 === 0) requests.push(loadScheduler());
        const [jobs] = await Promise.all(requests);
        job = jobs[0] || null;
        if (job) state.watchedJobId = String(job.id);
      }
      if (job && ["completed", "partial", "failed", "cancelled", "interrupted"].includes(job.status)) {
        state.watchedJobId = null;
        await Promise.all([loadStats(), loadGateway(), loadRuntime(), loadSettings(), loadSources(), loadProxies(), loadGeoIP()]);
        renderJobs([job]);
        if (job.status === "completed") {
          toast(job.message || "任务已完成", "success");
        } else if (job.status === "partial") {
          toast(job.message || "任务部分完成", "info");
        } else if (job.status === "cancelled") {
          toast(job.message || "任务已取消", "info");
        } else if (job.status === "interrupted") {
          toast(job.error || "任务因服务重启中断", "warning");
        } else {
          toast(job.error || job.message || "任务失败", "error");
        }
        startGatewayPolling();
        return;
      }
    } catch (error) {
      toast(error.message, "error");
    }
    state.pollTimer = setTimeout(poll, 1000);
  };
  state.pollTimer = setTimeout(poll, 250);
}

function stopJobPolling() {
  if (state.pollTimer) clearTimeout(state.pollTimer);
  state.pollTimer = null;
  state.watchedJobId = null;
}

function startGatewayPolling() {
  if (state.gatewayTimer) return;
  if (document.hidden || el("appView").hidden || state.pollTimer) return;
  const poll = async () => {
    state.gatewayTimer = null;
    if (document.hidden || el("appView").hidden || state.pollTimer) return;
    try {
      const cycle = ++state.liveCycle;
      const requests = [loadRuntime(), loadStats(), loadActiveJobs()];
      if (cycle % 2 === 0) requests.push(loadGateway());
      if (cycle % 5 === 0) requests.push(loadProxies());
      if (cycle % 5 === 0) requests.push(loadScheduler());
      if (cycle % 10 === 0) requests.push(loadSources());
      const results = await Promise.all(requests);
      const jobs = results[2] || [];
      if (!state.pollTimer) {
        if (jobs.length) {
          state.watchedJobId = String(jobs[0].id);
          startPolling();
        }
      }
    } catch {
      // Avoid repeating toast noise when the session expires or the service restarts.
    }
    if (!state.pollTimer && !document.hidden && !el("appView").hidden) {
      state.gatewayTimer = setTimeout(poll, 1000);
    }
  };
  state.gatewayTimer = setTimeout(poll, 1000);
}

function stopGatewayPolling() {
  if (!state.gatewayTimer) return;
  clearTimeout(state.gatewayTimer);
  state.gatewayTimer = null;
}

function pausePollingForHiddenPage() {
  if (state.pollTimer) clearTimeout(state.pollTimer);
  state.pollTimer = null;
  stopGatewayPolling();
}

async function resumeVisiblePolling() {
  if (el("appView").hidden) return;
  try {
    const [jobs] = await Promise.all([loadActiveJobs(), loadStats(), loadGateway(), loadRuntime(), loadProxies()]);
    const active = jobs[0] || null;
    if (active) {
      state.watchedJobId = String(active.id);
      startPolling();
      return;
    }
  } catch {
    // A later explicit refresh or authentication flow will surface the error.
  }
  startGatewayPolling();
}

async function saveSettings(event) {
  const selectedSourceIDs = [...document.querySelectorAll(".source-check:checked")].map((input) => input.value);
  const allSourcesSelected = selectedSourceIDs.length === state.sources.length;
  const checkTimeout = Number(el("settingsCheckTimeout").value || 6);
  const hardTimeout = Number(el("settingsCheckHardTimeout").value || checkTimeout * 10);
  const checkTargets = getTargetSelections("settingsCheckTargets");
  await withButton(event.currentTarget, "保存中", async () => {
    const payload = await api("/api/settings", {
      method: "POST",
      body: JSON.stringify({
        proxy_page_size: Number(el("proxyPageSize").value || 50),
        fetch_limit_per_source: Number(el("settingsFetchLimit").value || 0),
        auto_fetch_low_stock_enabled: el("autoFetchLowStockEnabled").checked,
        auto_fetch_untested_minimum: Number(el("autoFetchUntestedMinimum").value || 5000),
        auto_fetch_cooldown_minutes: Number(el("autoFetchCooldown").value || 30),
        check_status: el("settingsCheckStatus").value,
        check_target_profile: checkTargets[0],
        check_target_profiles: checkTargets,
        check_limit: Number(el("settingsCheckLimit").value || 500),
        check_concurrent: Number(el("settingsCheckConcurrent").value || 100),
        check_rounds: Number(el("settingsCheckRounds").value || 1),
        check_request_timeout: checkTimeout,
        check_hard_timeout: Math.max(checkTimeout, hardTimeout),
        delete_failed_on_check: el("deleteFailedOnCheck").checked,
        recheck_expired_enabled: el("recheckExpiredEnabled").checked,
        available_ttl_hours: Number(el("availableTtlHours").value || 24),
        delete_expired_untested: el("deleteExpiredUntestedEnabled").checked,
        untested_ttl_hours: Number(el("untestedTtlHours").value || 72),
        auto_fetch_enabled: el("autoFetchEnabled").checked,
        auto_fetch_interval_minutes: Number(el("autoFetchInterval").value || 360),
        auto_fetch_source_ids: allSourcesSelected ? [] : selectedSourceIDs,
        auto_check_enabled: el("autoCheckEnabled").checked,
        auto_check_interval_minutes: Number(el("autoCheckInterval").value || 120),
        target_low_stock_enabled: el("targetLowStockEnabled").checked,
        target_low_stock_profiles: checkTargets,
        target_low_stock_minimum: Number(el("targetLowStockMinimum").value || 50),
        target_candidate_minimum: Number(el("targetCandidateMinimum").value || 200),
        check_after_fetch_enabled: el("checkAfterFetchEnabled").checked,
        gateway_upstream_limit: Number(el("gatewayUpstreamLimit").value || 200),
        gateway_upstream_strategy: el("gatewayUpstreamStrategy").value,
        gateway_countries: normalizeCountryCodesInput(el("gatewayCountries").value),
        gateway_country_policy: el("gatewayCountryPolicy").value,
        gateway_retry_attempts: Number(el("gatewayRetryAttempts").value || 2),
        gateway_failure_threshold: Number(el("gatewayFailureThreshold").value || 3),
        gateway_failure_cooldown_seconds: Number(el("gatewayFailureCooldown").value || 300),
        gateway_request_timeout_seconds: Number(el("gatewayRequestTimeout").value || 20),
        export_target_profile: el("exportTarget").value,
      }),
    });
    renderSettings(payload.settings, payload.scheduler);
    state.proxies.limit = Number(payload.settings.proxy_page_size || 50);
    state.proxies.offset = 0;
    const hotUpdated = state.activeJob?.type === "check" && ["queued", "running", "cancel_requested", "cancelling"].includes(state.activeJob.status);
    toast(hotUpdated ? "设置已保存，检测并发已热更新" : "设置已保存", "success");
    await loadRuntime();
    await loadGateway();
    await loadProxies();
  });
}

async function savePartialSettings(partial) {
  const payload = await api("/api/settings", {
    method: "POST",
    body: JSON.stringify(partial),
  });
  renderSettings(payload.settings, payload.scheduler);
  state.proxies.limit = Number(payload.settings.proxy_page_size || 50);
  return payload;
}

async function saveExportTarget() {
  updateExportLinks();
  try {
    await savePartialSettings({ export_target_profile: el("exportTarget").value });
  } catch (error) {
    toast(error.message, "error");
  }
}

async function updateGeoIP(event) {
  await withButton(event.currentTarget, "更新中", async () => {
    const status = await api("/api/geoip/update", { method: "POST" });
    renderGeoIP(status);
    toast("GeoIP 已更新", "success");
  });
}

async function copyGatewayAddress(event) {
  const button = event.target.closest("[data-copy-gateway]");
  if (!button) return;
  const value = button.dataset.copyGateway;
  if (!value) return;
  try {
    await copyText(value);
    const previous = button.textContent;
    button.textContent = "已复制";
    window.setTimeout(() => {
      button.textContent = previous;
    }, 1200);
    toast("网关地址已复制", "success");
  } catch (error) {
    toast(error.message || "复制失败", "error");
  }
}

async function cancelJob(event) {
  if (!state.activeJob) return;
  await withButton(event.currentTarget, "停止中", async () => {
    const job = await api(`/api/jobs/${state.activeJob.id}/cancel`, { method: "POST" });
    state.watchedJobId = String(job.id);
    renderJobs([job]);
    startPolling();
    toast("已请求停止任务", "success");
  });
}

async function deleteFailed(event) {
  if (!confirm("确认删除所有失败代理？")) return;
  await withButton(event.currentTarget, "清理中", async () => {
    const result = await api("/api/proxies/by-status?status=failed", { method: "DELETE" });
    toast(`已清理 ${result.deleted || 0} 条失败代理`, "success");
    await refreshAll();
  });
}

async function refreshAll() {
  await Promise.all([loadStats(), loadGateway(), loadRuntime(), loadGeoIP(), loadActiveJobs(), loadSettings(), loadSources(), loadProxies()]);
}

function updateExportLinks() {
  const target = normalizeTargetValues(el("exportTarget")?.value || "generic", true)[0];
  const params = new URLSearchParams({ target_profile: target });
  const countries = normalizeCountryCodesInput(el("proxyCountry")?.value || "");
  if (countries.length) {
    params.set("countries", countries.join(","));
  }
  el("exportTxtLink").href = `/api/export/proxies.txt?${params}`;
  el("exportJsonLink").href = `/api/export/proxies.json?${params}`;
}

function bindTargetSync(sourceId, targetId) {
  document.querySelectorAll(`#${sourceId} input`).forEach((input) => {
    input.addEventListener("change", () => {
      ensureTargetSelection(sourceId);
      setTargetSelections(targetId, getTargetSelections(sourceId));
    });
  });
}

function bindEvents() {
	// Browser visibility controls polling globally so background tabs do not keep high-frequency API traffic alive.
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) {
      pausePollingForHiddenPage();
      return;
    }
    resumeVisiblePolling();
  });
  el("loginForm").addEventListener("submit", login);
  el("themeToggle").addEventListener("click", () => {
    applyTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark", true);
  });
  el("logoutBtn").addEventListener("click", () => {
    localStorage.removeItem("plc_token");
    state.token = "";
    showLogin();
  });
  el("refreshBtn").addEventListener("click", (event) => withButton(event.currentTarget, "刷新中", refreshAll));
  el("fetchSourcesBtn").addEventListener("click", fetchSources);
  el("selectAllSources").addEventListener("click", () => {
    document.querySelectorAll(".source-check").forEach((item) => {
      item.checked = true;
    });
    updateSelectedSources();
  });
  el("clearSources").addEventListener("click", () => {
    document.querySelectorAll(".source-check").forEach((item) => {
      item.checked = false;
    });
    updateSelectedSources();
  });
  el("importBtn").addEventListener("click", importProxies);
  el("runCheckBtn").addEventListener("click", runCheck);
  el("deleteFailedBtn").addEventListener("click", deleteFailed);
  el("cancelJobBtn").addEventListener("click", cancelJob);
  el("saveSettingsBtn").addEventListener("click", saveSettings);
  el("geoipUpdateBtn").addEventListener("click", updateGeoIP);
  el("settingsFetchLimit").addEventListener("input", () => {
    el("sourceLimit").value = el("settingsFetchLimit").value;
  });
  el("sourceLimit").addEventListener("input", () => {
    el("settingsFetchLimit").value = el("sourceLimit").value;
  });
  el("quickCheckStatus").addEventListener("change", () => {
    el("settingsCheckStatus").value = el("quickCheckStatus").value;
  });
  bindTargetSync("quickCheckTargets", "settingsCheckTargets");
  el("quickCheckLimit").addEventListener("input", () => {
    el("settingsCheckLimit").value = el("quickCheckLimit").value;
  });
  el("settingsCheckStatus").addEventListener("change", () => {
    el("quickCheckStatus").value = el("settingsCheckStatus").value;
  });
  bindTargetSync("settingsCheckTargets", "quickCheckTargets");
  el("settingsCheckLimit").addEventListener("input", () => {
    el("quickCheckLimit").value = el("settingsCheckLimit").value;
  });
  el("exportTarget").addEventListener("change", saveExportTarget);
  document.addEventListener("click", copyGatewayAddress);
  el("proxyPageSize").addEventListener("change", () => {
    state.proxies.limit = Number(el("proxyPageSize").value || 50);
    resetProxyPage();
  });
  el("prevProxyPage").addEventListener("click", () => {
    state.proxies.offset = Math.max(0, state.proxies.offset - state.proxies.limit);
    loadProxies();
  });
  el("nextProxyPage").addEventListener("click", () => {
    state.proxies.offset += state.proxies.limit;
    loadProxies();
  });
  el("proxyStatus").addEventListener("change", resetProxyPage);
  el("targetProfile").addEventListener("change", resetProxyPage);
  el("proxyCountry").addEventListener(
    "input",
    debounce(() => {
      updateExportLinks();
      resetProxyPage();
    }, 250),
  );
  el("proxySearch").addEventListener("input", debounce(resetProxyPage, 250));
}

function debounce(fn, wait) {
  let timer = null;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), wait);
  };
}

initializeInterface();
bindEvents();
if (state.token) {
  bootstrap().catch(() => showLogin());
} else {
  showLogin();
}
