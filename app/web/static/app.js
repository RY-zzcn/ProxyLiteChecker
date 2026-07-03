const state = {
  token: localStorage.getItem("plc_token") || "",
  sources: [],
  settings: null,
  scheduler: null,
  activeJob: null,
  pollTimer: null,
  sourceSelected: 0,
  proxies: {
    offset: 0,
    limit: 50,
    total: 0,
  },
};

const el = (id) => document.getElementById(id);

async function api(path, options = {}) {
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

function statusTag(status) {
  const text = { available: "可用", failed: "失败", untested: "待检", checking: "检测中" }[status] || status;
  return `<span class="tag ${escapeHtml(status)}">${escapeHtml(text)}</span>`;
}

function defaultSettings() {
  return {
    proxy_page_size: 50,
    fetch_limit_per_source: 0,
    check_status: "untested",
    check_target_profile: "generic",
    check_limit: 500,
    check_concurrent: 50,
    check_rounds: 1,
    check_request_timeout: 6,
    check_hard_timeout: 60,
    auto_fetch_enabled: false,
    auto_fetch_interval_minutes: 360,
    auto_fetch_source_ids: [],
    auto_check_enabled: false,
    auto_check_interval_minutes: 120,
  };
}

function proxyUrl(item) {
  const protocol = item.detected_protocol || item.protocol || "http";
  const auth = item.username ? `${item.username}:***@` : "";
  return `${protocol}://${auth}${item.ip}:${item.port}`;
}

function toast(message, type = "info") {
  const host = el("toastHost");
  const item = document.createElement("div");
  item.className = `toast ${type}`;
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
  renderJobs(payload.active_jobs || []);
  await loadProxies();
}

function renderStats(stats) {
  el("statTotal").textContent = stats.total || 0;
  el("statAvailable").textContent = stats.available || 0;
  el("statUntested").textContent = stats.untested || 0;
  el("statFailed").textContent = stats.failed || 0;
}

function renderSources() {
  const selectedIDs = new Set((state.settings?.auto_fetch_source_ids || []).map(String));
  const useAll = selectedIDs.size === 0;
  el("sourceCountText").textContent = `${state.sources.length} 个代理源`;
  el("sourcesList").innerHTML = state.sources
    .map(
      (source) => `
        <label class="source-item" title="${escapeHtml(source.url)}">
          <input type="checkbox" class="source-check" value="${escapeHtml(source.id)}" ${useAll || selectedIDs.has(source.id) ? "checked" : ""} />
          <span>${escapeHtml(source.name)}</span>
        </label>
      `,
    )
    .join("");
  document.querySelectorAll(".source-check").forEach((input) => {
    input.addEventListener("change", updateSelectedSources);
  });
  updateSelectedSources();
}

function renderSettings(settings, scheduler) {
  state.settings = { ...defaultSettings(), ...(settings || {}) };
  state.scheduler = scheduler || state.scheduler;
  el("autoFetchEnabled").checked = Boolean(state.settings.auto_fetch_enabled);
  el("autoFetchInterval").value = state.settings.auto_fetch_interval_minutes;
  el("sourceLimit").value = state.settings.fetch_limit_per_source;
  el("settingsFetchLimit").value = state.settings.fetch_limit_per_source;
  el("autoCheckEnabled").checked = Boolean(state.settings.auto_check_enabled);
  el("autoCheckInterval").value = state.settings.auto_check_interval_minutes;
  el("proxyPageSize").value = String(state.settings.proxy_page_size);
  el("settingsCheckStatus").value = state.settings.check_status;
  el("settingsCheckTarget").value = state.settings.check_target_profile;
  el("settingsCheckLimit").value = state.settings.check_limit;
  el("settingsCheckConcurrent").value = state.settings.check_concurrent;
  el("settingsCheckRounds").value = state.settings.check_rounds;
  el("settingsCheckTimeout").value = state.settings.check_request_timeout;
  el("settingsCheckHardTimeout").value = state.settings.check_hard_timeout;
  renderSchedulerText(state.scheduler);
}

function renderSchedulerText(scheduler) {
  const parts = [];
  if (scheduler?.fetch?.enabled) {
    parts.push(`拉取 ${displayNextRun(scheduler.fetch.next_run_at)}`);
  }
  if (scheduler?.check?.enabled) {
    parts.push(`检测 ${displayNextRun(scheduler.check.next_run_at)}`);
  }
  el("schedulerText").textContent = parts.length ? parts.join(" · ") : scheduler?.message || "自动任务未启用";
}

function displayNextRun(value) {
  if (!value) return "等待排程";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "等待排程";
  return `下次 ${date.toLocaleString()}`;
}

function updateSelectedSources() {
  state.sourceSelected = document.querySelectorAll(".source-check:checked").length;
  el("selectedSourcesText").textContent = `已选 ${state.sourceSelected} 个`;
}

function renderGateway(gateway) {
  const httpBind = gateway.enabled ? displayGatewayBind(gateway, "http") : "未启用";
  const socks5Bind = gateway.enabled && gateway.socks5_enabled ? displayGatewayBind(gateway, "socks5") : "未启用";
  const upstreams = gateway.upstreams || 0;
  el("gatewayHttpBind").textContent = httpBind;
  el("gatewaySocks5Bind").textContent = socks5Bind;
  el("gatewayUpstreams").textContent = upstreams;
  el("gatewayTotal").textContent = gateway.total_requests || 0;
  el("gatewayRate").textContent = `${Math.round((gateway.success_rate || 0) * 100)}%`;
  const pill = el("gatewayPill");
  pill.textContent = gateway.enabled ? `HTTP ${httpBind} · SOCKS5 ${socks5Bind} · ${upstreams} 上游` : "网关未启用";
  pill.classList.toggle("offline", !gateway.enabled);
}

function displayGatewayBind(gateway, type) {
  const hostKey = type === "socks5" ? "socks5_host" : "http_host";
  const portKey = type === "socks5" ? "socks5_port" : "http_port";
  const bindKey = type === "socks5" ? "socks5_bind" : "http_bind";
  const fallback = gateway[bindKey] || gateway.bind || `${gateway[hostKey] || gateway.host}:${gateway[portKey] || gateway.port}`;
  const host = gateway[hostKey] || gateway.host;
  const port = gateway[portKey] || String(fallback).split(":").pop();
  if (host === "0.0.0.0" || String(fallback).startsWith("0.0.0.0:")) {
    return `${location.hostname || "服务器IP"}:${port}`;
  }
  if (host === "::" || String(fallback).startsWith("[::]:")) {
    return `${location.hostname || "服务器IP"}:${port}`;
  }
  return fallback;
}

function renderJobs(jobs) {
  const job = jobs.find((item) => item.status === "running") || jobs[0] || null;
  state.activeJob = job;
  if (!job) {
    el("taskSubtitle").textContent = "空闲";
    el("jobMessage").textContent = "等待操作";
    el("progressBar").style.width = "0%";
    el("cancelJobBtn").disabled = true;
    return;
  }
  const percent = pct(job.done, job.total);
  const typeText = { fetch: "拉取", check: "检测" }[job.type] || job.type;
  el("taskSubtitle").textContent = `${typeText} · ${job.status} · ${job.done}/${job.total}`;
  el("jobMessage").textContent = `${job.message || ""} ${percent}%`;
  el("progressBar").style.width = `${percent}%`;
  el("cancelJobBtn").disabled = job.status !== "running";
}

async function loadStats() {
  const stats = await api("/api/stats");
  renderStats(stats);
}

async function loadGateway() {
  const gateway = await api("/api/gateway/status");
  renderGateway(gateway);
}

async function loadActiveJobs() {
  const payload = await api("/api/jobs/active");
  renderJobs(payload.items || []);
}

async function loadSettings() {
  const payload = await api("/api/settings");
  renderSettings(payload.settings, payload.scheduler);
}

async function loadProxies() {
  const params = new URLSearchParams({
    status: el("proxyStatus").value,
    target_profile: el("targetProfile").value,
    q: el("proxySearch").value.trim(),
    limit: String(state.proxies.limit),
    offset: String(state.proxies.offset),
  });
  const payload = await api(`/api/proxies?${params}`);
  const rows = payload.items || [];
  state.proxies.total = payload.total || rows.length;
  state.proxies.limit = payload.limit || state.proxies.limit;
  state.proxies.offset = payload.offset || 0;
  el("proxyTableSummary").textContent = `${state.proxies.total} 条记录`;
  el("proxyRows").innerHTML =
    rows
      .map(
        (item) => `
          <tr>
            <td class="proxy-cell" title="${escapeHtml(proxyUrl(item))}">${escapeHtml(proxyUrl(item))}</td>
            <td>${statusTag(item.status)}</td>
            <td>${escapeHtml(item.grade || "-")}</td>
            <td>${item.latency_ms == null ? "-" : `${item.latency_ms} ms`}</td>
            <td>${escapeHtml(item.exit_ip || "-")} ${item.country ? `<span class="muted">${escapeHtml(item.country)}</span>` : ""}</td>
            <td>${escapeHtml(item.recommended_use || "-")}</td>
            <td>${escapeHtml(item.source || "-")}</td>
            <td>${escapeHtml(item.last_checked_at || item.updated_at || "-")}</td>
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
  el("proxyPageText").textContent = `${start}-${end} / ${total} · 第 ${page}/${pages} 页`;
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
  await withButton(event.currentTarget, "检测中", async () => {
    const payload = await api("/api/checks/run-job", {
      method: "POST",
      body: JSON.stringify({
        status: el("settingsCheckStatus").value,
        target_profile: el("settingsCheckTarget").value,
        limit: Number(el("settingsCheckLimit").value || 500),
        concurrent: Number(el("settingsCheckConcurrent").value || 50),
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
  const job = await api(`/api/jobs/${jobId}`);
  renderJobs([job]);
  startPolling();
}

function startPolling() {
  if (state.pollTimer) return;
  state.pollTimer = setInterval(async () => {
    try {
      await loadActiveJobs();
      await loadStats();
      await loadGateway();
      await loadSettings();
      if (!state.activeJob) {
        await loadProxies();
        clearInterval(state.pollTimer);
        state.pollTimer = null;
        toast("任务已完成", "success");
      }
    } catch (error) {
      toast(error.message, "error");
    }
  }, 1200);
}

async function saveSettings(event) {
  const selectedSourceIDs = [...document.querySelectorAll(".source-check:checked")].map((input) => input.value);
  const allSourcesSelected = selectedSourceIDs.length === state.sources.length;
  const checkTimeout = Number(el("settingsCheckTimeout").value || 6);
  const hardTimeout = Number(el("settingsCheckHardTimeout").value || checkTimeout * 10);
  await withButton(event.currentTarget, "保存中", async () => {
    const payload = await api("/api/settings", {
      method: "POST",
      body: JSON.stringify({
        proxy_page_size: Number(el("proxyPageSize").value || 50),
        fetch_limit_per_source: Number(el("settingsFetchLimit").value || 0),
        check_status: el("settingsCheckStatus").value,
        check_target_profile: el("settingsCheckTarget").value,
        check_limit: Number(el("settingsCheckLimit").value || 500),
        check_concurrent: Number(el("settingsCheckConcurrent").value || 50),
        check_rounds: Number(el("settingsCheckRounds").value || 1),
        check_request_timeout: checkTimeout,
        check_hard_timeout: Math.max(checkTimeout, hardTimeout),
        auto_fetch_enabled: el("autoFetchEnabled").checked,
        auto_fetch_interval_minutes: Number(el("autoFetchInterval").value || 360),
        auto_fetch_source_ids: allSourcesSelected ? [] : selectedSourceIDs,
        auto_check_enabled: el("autoCheckEnabled").checked,
        auto_check_interval_minutes: Number(el("autoCheckInterval").value || 120),
      }),
    });
    renderSettings(payload.settings, payload.scheduler);
    state.proxies.limit = Number(payload.settings.proxy_page_size || 50);
    state.proxies.offset = 0;
    toast("设置已保存", "success");
    await loadProxies();
  });
}

async function cancelJob(event) {
  if (!state.activeJob) return;
  await withButton(event.currentTarget, "停止中", async () => {
    await api(`/api/jobs/${state.activeJob.id}/cancel`, { method: "POST" });
    toast("已请求停止任务", "success");
    await refreshAll();
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
  await loadStats();
  await loadGateway();
  await loadActiveJobs();
  await loadSettings();
  await loadProxies();
}

function bindEvents() {
  el("loginForm").addEventListener("submit", login);
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
  el("settingsFetchLimit").addEventListener("input", () => {
    el("sourceLimit").value = el("settingsFetchLimit").value;
  });
  el("sourceLimit").addEventListener("input", () => {
    el("settingsFetchLimit").value = el("sourceLimit").value;
  });
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
  el("proxySearch").addEventListener("input", debounce(resetProxyPage, 250));
}

function debounce(fn, wait) {
  let timer = null;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), wait);
  };
}

bindEvents();
if (state.token) {
  bootstrap().catch(() => showLogin());
} else {
  showLogin();
}
