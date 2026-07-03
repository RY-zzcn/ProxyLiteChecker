const state = {
  token: localStorage.getItem("plc_token") || "",
  sources: [],
  activeJob: null,
  pollTimer: null,
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

function statusTag(status) {
  const text = { available: "可用", failed: "失败", untested: "待检", checking: "检测中" }[status] || status;
  return `<span class="tag ${escapeHtml(status)}">${escapeHtml(text)}</span>`;
}

function escapeHtml(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function proxyUrl(item) {
  const protocol = item.detected_protocol || item.protocol || "http";
  const auth = item.username ? `${item.username}:***@` : "";
  return `${protocol}://${auth}${item.ip}:${item.port}`;
}

async function login(event) {
  event.preventDefault();
  el("loginError").textContent = "";
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
  } catch (error) {
    el("loginError").textContent = error.message;
  }
}

async function bootstrap() {
  const payload = await api("/api/bootstrap");
  showApp();
  el("versionText").textContent = `v${payload.app.version}`;
  state.sources = payload.sources || [];
  renderStats(payload.stats || {});
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
  el("sourcesList").innerHTML = state.sources
    .map(
      (source) => `
        <label class="source-item" title="${escapeHtml(source.url)}">
          <input type="checkbox" class="source-check" value="${escapeHtml(source.id)}" checked />
          <span>${escapeHtml(source.name)}</span>
        </label>
      `,
    )
    .join("");
}

function renderGateway(gateway) {
  el("gatewayBind").textContent = gateway.enabled ? gateway.bind || `${gateway.host}:${gateway.port}` : "未启用";
  el("gatewayUpstreams").textContent = gateway.upstreams || 0;
  el("gatewayTotal").textContent = gateway.total_requests || 0;
  el("gatewayRate").textContent = `${Math.round((gateway.success_rate || 0) * 100)}%`;
}

function renderJobs(jobs) {
  const job = jobs.find((item) => item.status === "running") || jobs[0] || null;
  state.activeJob = job;
  if (!job) {
    el("taskSubtitle").textContent = "暂无运行任务";
    el("jobMessage").textContent = "等待操作";
    el("progressBar").style.width = "0%";
    el("cancelJobBtn").disabled = true;
    return;
  }
  const percent = pct(job.done, job.total);
  el("taskSubtitle").textContent = `${job.type} ${job.status}：${job.done}/${job.total}`;
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

async function loadProxies() {
  const params = new URLSearchParams({
    status: el("proxyStatus").value,
    target_profile: el("targetProfile").value,
    q: el("proxySearch").value.trim(),
    limit: "200",
  });
  const payload = await api(`/api/proxies?${params}`);
  const rows = payload.items || [];
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
      .join("") || `<tr><td colspan="8" class="muted">暂无代理</td></tr>`;
}

async function fetchSources() {
  const ids = [...document.querySelectorAll(".source-check:checked")].map((input) => input.value);
  const limit = Number(el("sourceLimit").value || 0);
  const payload = await api("/api/sources/fetch-job", {
    method: "POST",
    body: JSON.stringify({ source_ids: ids, limit_per_source: limit }),
  });
  await watchJob(payload.job_id);
}

async function importProxies() {
  const text = el("importText").value;
  const result = await api("/api/proxies/import", {
    method: "POST",
    body: JSON.stringify({ text, source: "manual", default_protocol: "auto" }),
  });
  el("importResult").textContent = `新增 ${result.added}，更新 ${result.updated}`;
  el("importText").value = "";
  await refreshAll();
}

async function runCheck() {
  const payload = await api("/api/checks/run-job", {
    method: "POST",
    body: JSON.stringify({
      status: el("proxyStatus").value === "all" ? "untested" : el("proxyStatus").value,
      target_profile: el("targetProfile").value,
      limit: 500,
      concurrent: 30,
      rounds: 1,
    }),
  });
  await watchJob(payload.job_id);
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
      if (!state.activeJob) {
        await loadProxies();
        clearInterval(state.pollTimer);
        state.pollTimer = null;
      }
    } catch (error) {
      console.warn(error);
    }
  }, 1200);
}

async function cancelJob() {
  if (!state.activeJob) return;
  await api(`/api/jobs/${state.activeJob.id}/cancel`, { method: "POST" });
  await refreshAll();
}

async function deleteFailed() {
  if (!confirm("确认删除所有失败代理？")) return;
  await api("/api/proxies/by-status?status=failed", { method: "DELETE" });
  await refreshAll();
}

async function refreshAll() {
  await loadStats();
  await loadGateway();
  await loadActiveJobs();
  await loadProxies();
}

function bindEvents() {
  el("loginForm").addEventListener("submit", login);
  el("logoutBtn").addEventListener("click", () => {
    localStorage.removeItem("plc_token");
    state.token = "";
    showLogin();
  });
  el("refreshBtn").addEventListener("click", refreshAll);
  el("fetchSourcesBtn").addEventListener("click", fetchSources);
  el("selectAllSources").addEventListener("click", () => {
    document.querySelectorAll(".source-check").forEach((item) => {
      item.checked = true;
    });
  });
  el("clearSources").addEventListener("click", () => {
    document.querySelectorAll(".source-check").forEach((item) => {
      item.checked = false;
    });
  });
  el("importBtn").addEventListener("click", importProxies);
  el("runCheckBtn").addEventListener("click", runCheck);
  el("deleteFailedBtn").addEventListener("click", deleteFailed);
  el("cancelJobBtn").addEventListener("click", cancelJob);
  el("proxyStatus").addEventListener("change", loadProxies);
  el("targetProfile").addEventListener("change", loadProxies);
  el("proxySearch").addEventListener("input", debounce(loadProxies, 250));
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
