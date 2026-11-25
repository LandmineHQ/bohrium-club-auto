(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const TAB_TITLES = {
    overview: "任务与总览",
    assets: "积分与容量",
    gateway: "OpenAI 网关",
    logs: "执行日志",
    recon: "前端 Recon",
    settings: "定时与设置",
  };

  let qrPollTimer = null;
  let currentSceneId = "";
  let selectedOAuthProvider = "google";
  let gatewayCache = null;
  let playgroundPrompts = [];
  let pgPromptCustomized = false;
  let pgLastRandomIdx = -1;
  const revealedKeyIDs = new Set();

  function escapeHTML(str) {
    return String(str ?? "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;")
      .replace(/'/g, "&#39;");
  }

  function toast(msg, type = "info") {
    const c = $("toast-container");
    const el = document.createElement("div");
    el.className = `toast ${type}`;
    el.textContent = msg;
    c.appendChild(el);
    setTimeout(() => el.remove(), 4800);
  }

  async function api(url, opts = {}) {
    const res = await fetch(url, {
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", ...(opts.headers || {}) },
      ...opts,
    });
    const data = await res.json().catch(() => ({ ok: false, error: `HTTP ${res.status}` }));
    if (res.status === 401 && !url.startsWith("/api/auth/")) {
      showLoginGate();
      throw new Error(data.error || "会话已过期，请重新登录控制台");
    }
    if (!res.ok || data.ok === false) {
      throw new Error(data.error || data.message || data.data?.message || `HTTP ${res.status}`);
    }
    return data;
  }

  function formatNum(v) {
    if (v === null || v === undefined || Number.isNaN(Number(v))) return "--";
    return Number(v).toLocaleString("zh-CN", { maximumFractionDigits: 2 });
  }

  function formatQuota(q) {
    if (!q || (q.remaining === null && q.total === null)) return "--";
    if (q.total !== null && q.total !== undefined) return `${q.remaining ?? 0}/${q.total}`;
    return `${q.remaining ?? "--"}`;
  }

  function formatTime(iso) {
    if (!iso) return "--";
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return String(iso);
    return d.toLocaleString("zh-CN", { hour12: false });
  }

  function applyTheme(theme) {
    const root = document.documentElement;
    if (theme === "dark") {
      root.setAttribute("data-theme", "dark");
      if ($("btn-theme-toggle")) $("btn-theme-toggle").textContent = "浅色";
    } else {
      root.removeAttribute("data-theme");
      if ($("btn-theme-toggle")) $("btn-theme-toggle").textContent = "暗色";
    }
    try { localStorage.setItem("brm_console_theme", theme); } catch {}
  }

  function initTheme() {
    let saved = "light";
    try { saved = localStorage.getItem("brm_console_theme") || "light"; } catch {}
    applyTheme(saved);
  }

  function showLoginGate() {
    $("app-shell").classList.add("hidden");
    $("login-gate").classList.remove("hidden");
    setTimeout(() => $("admin-password-input")?.focus(), 50);
  }

  function showAppShell() {
    $("login-gate").classList.add("hidden");
    $("app-shell").classList.remove("hidden");
  }

  async function checkAuthAndBoot() {
    try {
      const st = await api("/api/auth/status");
      if (st.version) $("app-version").textContent = st.version;
      if (st.authenticated) {
        showAppShell();
        await Promise.all([loadOverview(), loadSettings(), loadReconStatus(), loadGatewayStatus()]);
      } else {
        showLoginGate();
      }
    } catch {
      showLoginGate();
    }
  }

  async function loadOverview() {
    try {
      const data = await api("/api/overview");
      renderOverview(data);
      renderAssetsDetail(data.assets);
      renderLogs(data.logs || []);
    } catch (e) {
      toast("加载总览失败: " + e.message, "err");
    }
  }

  function renderOverview(data) {
    if (data.version) $("app-version").textContent = data.version;

    const sess = data.session || {};
    const auth = sess.auth || {};
    const pill = $("bohrium-account-pill");
    const pillText = $("bohrium-account-text");
    const pillMeta = $("bohrium-account-meta");
    const banner = $("not-logged-in-banner");
    const refreshBtn = $("btn-refresh-token");

    if (sess.hasToken && sess.valid) {
      pill.className = "account-box online";
      pillText.textContent = auth.nickname || auth.email || `UID ${auth.userId}`;
      const days = sess.expiresInSec > 0 ? `剩余 ${Math.floor(sess.expiresInSec / 86400)} 天` : "有效";
      pillMeta.textContent = `UID ${auth.userId} · ${days}`;
      banner.classList.add("hidden");
      refreshBtn.classList.remove("hidden");
    } else if (sess.hasToken) {
      pill.className = "account-box offline";
      pillText.textContent = "登录已失效";
      pillMeta.textContent = "请重新扫码或导入 Token";
      banner.classList.remove("hidden");
      refreshBtn.classList.remove("hidden");
    } else {
      pill.className = "account-box offline";
      pillText.textContent = "未绑定玻尔账号";
      pillMeta.textContent = "需远程扫码或导入 brmToken";
      banner.classList.remove("hidden");
      refreshBtn.classList.add("hidden");
    }

    // KPIs
    const assets = data.assets;
    if (assets) {
      $("kpi-photons").textContent = `${formatNum(assets.photonBalance)}`;
      const exp7 = assets.expiring7DayPhotons > 0 ? ` · 7日过期 ${formatNum(assets.expiring7DayPhotons)}` : "";
      $("kpi-photons-sub").textContent = `组织 ${formatNum(assets.orgBalance)} · 钱包 ¥${formatNum(assets.walletBalance)}${exp7}`;

      const st = assets.storage || {};
      $("kpi-storage").textContent = st.remainingHuman || "--";
      const pct = Math.min(100, Math.max(0, Number(st.usedPercent || 0)));
      $("kpi-storage-bar").style.width = `${pct.toFixed(1)}%`;
      $("kpi-storage-sub").textContent = `已用 ${st.usedHuman || "--"} / ${st.totalHuman || "--"} (${pct.toFixed(1)}%)`;

      const lvl = assets.membershipLevel !== null && assets.membershipLevel !== undefined ? `Lv.${assets.membershipLevel}` : "标准";
      $("kpi-membership").textContent = lvl;
      $("kpi-membership-sub").textContent = `文献 ${formatQuota(assets.paperQuota)} · 对话 ${formatQuota(assets.knowledgeChat)} · 深度研究 ${formatQuota(assets.deepResearch)}`;
    } else {
      $("kpi-photons").textContent = "--";
      $("kpi-photons-sub").textContent = data.assetsError || "登录后显示";
      $("kpi-storage").textContent = "--";
      $("kpi-storage-bar").style.width = "0%";
      $("kpi-storage-sub").textContent = "登录后显示";
      $("kpi-membership").textContent = "--";
      $("kpi-membership-sub").textContent = "登录后显示";
    }

    const tasks = data.tasks;
    const sched = data.scheduler || {};
    if (tasks) {
      const webTotal = Math.max(0, tasks.totalCount - tasks.skippedCount);
      $("kpi-tasks").textContent = `${tasks.completedCount} / ${webTotal}`;
      $("kpi-tasks-sub").textContent = `今日 +${formatNum(tasks.earnedPhotons)} 光子 · 下次 ${sched.nextRunAt || "已暂停"}`;
      $("task-date-badge").textContent = tasks.taskDate || "今日";
      $("tasks-summary-pill").textContent = `${tasks.completedCount}/${webTotal} 已完成 · 跳过 ${tasks.skippedCount}`;
    } else {
      $("kpi-tasks").textContent = "--";
      $("kpi-tasks-sub").textContent = `下次定时：${sched.nextRunAt || "已暂停"}`;
      $("tasks-summary-pill").textContent = "未连接";
    }

    const errEl = $("tasks-error");
    if (data.tasksError) {
      errEl.textContent = data.tasksError;
      errEl.classList.remove("hidden");
    } else {
      errEl.classList.add("hidden");
    }
    renderTasksList(tasks);

    // Corpus & Runtime
    const ps = data.promptStats || {};
    const pdfs = data.pdfStats || {};
    $("stat-scispace").textContent = `${ps.scienceSpacePrompts || 0} 条`;
    $("stat-writing").textContent = `${ps.academicWritingPrompts || 0} 条`;
    $("stat-reading").textContent = `${ps.literatureReadingPrompts || 0} 条`;
    $("stat-papers").textContent = `${ps.paperCatalogCount || 0} 篇`;
    $("stat-pdf-cache").textContent = `${pdfs.cached || 0} 缓存 / ${pdfs.custom || 0} 自定义`;
    $("stat-sched-state").textContent = sched.running
      ? `执行中 (${sched.currentJob})`
      : sched.enabled
        ? `已启用 · 每日 ${sched.timeHHMM} (+0~${sched.jitterMinutes}m)`
        : "已暂停";
    if ($("header-sched-pill")) {
      $("header-sched-pill").textContent = sched.running
        ? `RUN: ${sched.currentJob}`
        : sched.enabled
          ? `NEXT: ${sched.timeHHMM} ${sched.timezone}`
          : "SCHED: OFF";
    }
    $("stat-last-day").textContent = sched.lastScheduledDay
      ? `${sched.lastScheduledDay} · ${sched.lastRunSummary || "完成"}`
      : "尚无记录";
    $("stat-ua").textContent = data.userAgent || "--";
  }

  function renderTasksList(tasksResp) {
    const list = $("tasks-list");
    if (!tasksResp || !Array.isArray(tasksResp.tasks) || tasksResp.tasks.length === 0) {
      list.innerHTML = `<div class="empty-row">请先远程登录玻尔账号，登录后此处将列出全部每日任务并支持一键或单项触发。</div>`;
      return;
    }
    list.innerHTML = tasksResp.tasks.map((t, idx) => {
      const num = String(idx + 1).padStart(2, "0");
      let statusHTML = `<span class="status-badge status-muted">${escapeHTML(t.statusLabel)}</span>`;
      if (t.skipped) statusHTML = `<span class="status-badge status-muted">已跳过</span>`;
      else if (t.status === 1) statusHTML = `<span class="status-badge status-ok">已完成</span>`;
      else if (t.status === 0) statusHTML = `<span class="status-badge status-info">待完成</span>`;
      else if (t.status === -1) statusHTML = `<span class="status-badge status-warn">未领取</span>`;

      let opsHTML = "";
      if (t.skipped) {
        opsHTML = `<span class="skip-label">客户端跳过</span>`;
      } else {
        if (t.status === -1) {
          opsHTML += `<button class="btn btn-outline btn-xs" data-claim-one="${escapeHTML(t.taskKey)}">领取</button>`;
        }
        const runCls = t.status === 1 ? "btn btn-outline btn-xs" : "btn btn-primary btn-xs";
        const runLabel = t.status === 1 ? "重新执行" : "手动执行";
        opsHTML += `<button class="${runCls}" data-run-one="${escapeHTML(t.taskKey)}">${runLabel}</button>`;
      }

      return `
        <div class="task-row ${t.skipped ? "is-skipped" : ""}">
          <div class="task-main">
            <span class="task-num">${num}</span>
            <div class="task-info">
              <div class="task-name-line">
                <span class="task-name">${escapeHTML(t.titleZh || t.taskKey)}</span>
                <span class="task-key">${escapeHTML(t.actionKey || t.taskKey)}</span>
              </div>
              <div class="task-sub">${escapeHTML(t.descriptionZh || (t.skipped ? t.skipReason : ""))}</div>
            </div>
          </div>
          <div class="task-reward">+${formatNum(t.rewardAmount)} 光子</div>
          <div>${statusHTML}</div>
          <div class="task-ops">${opsHTML}</div>
        </div>
      `;
    }).join("");

    list.querySelectorAll("[data-claim-one]").forEach((btn) => {
      btn.addEventListener("click", () => handleClaimOne(btn.getAttribute("data-claim-one"), btn));
    });
    list.querySelectorAll("[data-run-one]").forEach((btn) => {
      btn.addEventListener("click", () => handleRunOne(btn.getAttribute("data-run-one"), btn));
    });
  }

  function renderAssetsDetail(assets) {
    const warnEl = $("assets-warnings");
    const grid = $("quota-detail-grid");
    const lotsBody = $("expiring-lots-body");
    const histBody = $("integral-history-body");

    if (!assets) {
      warnEl.classList.add("hidden");
      grid.innerHTML = `<div class="empty-row" style="grid-column:1/-1;">请先登录玻尔账号以查看实时积分与容量明细。</div>`;
      lotsBody.innerHTML = `<tr><td colspan="4" class="empty-cell">暂无数据</td></tr>`;
      histBody.innerHTML = `<tr><td colspan="3" class="empty-cell">暂无数据</td></tr>`;
      return;
    }

    if (assets.warnings && assets.warnings.length > 0) {
      warnEl.textContent = "部分子接口提示：" + assets.warnings.join("；");
      warnEl.classList.remove("hidden");
    } else {
      warnEl.classList.add("hidden");
    }

    const st = assets.storage || {};
    const items = [
      ["个人可用光子", `${formatNum(assets.photonBalance)}`],
      ["组织余额 / 上限", `${formatNum(assets.orgBalance)} / ${formatNum(assets.orgBalanceLimit)}`],
      ["钱包现金余额", `¥ ${formatNum(assets.walletBalance)}`],
      ["7 日内过期光子", `${formatNum(assets.expiring7DayPhotons)}`],
      ["知识库剩余存储", `${st.remainingHuman || "--"} / ${st.totalHuman || "--"}`],
      ["文献检索配额", formatQuota(assets.paperQuota)],
      ["知识库对话配额", formatQuota(assets.knowledgeChat)],
      ["深度研究配额", formatQuota(assets.deepResearch)],
    ];
    grid.innerHTML = items.map(([label, val]) => `
      <div class="quota-cell"><div class="q-label">${escapeHTML(label)}</div><div class="q-val">${escapeHTML(val)}</div></div>
    `).join("");

    const lots = assets.expiringLots || [];
    lotsBody.innerHTML = lots.length === 0
      ? `<tr><td colspan="4" class="empty-cell">暂无即将过期光子批次</td></tr>`
      : lots.map((l) => `
          <tr>
            <td class="tabular"><strong>${formatNum(l.remaining)}</strong></td>
            <td>${escapeHTML(l.description || l.sourceType || "--")}</td>
            <td class="tabular">${escapeHTML(l.createdTime || "--")}</td>
            <td class="tabular">${escapeHTML(l.expireTime || "--")}</td>
          </tr>
        `).join("");

    const hist = assets.recentIntegralHistory || [];
    histBody.innerHTML = hist.length === 0
      ? `<tr><td colspan="3" class="empty-cell">暂无近期光子流水</td></tr>`
      : hist.map((r) => {
          const sign = r.amount > 0 ? `+${formatNum(r.amount)}` : formatNum(r.amount);
          const cls = r.amount >= 0 ? "ink-ok" : "ink-err";
          const expNote = r.expireAt ? `<div class="log-sub">有效期至 ${escapeHTML(r.expireAt)}</div>` : "";
          return `
            <tr>
              <td class="tabular ${cls}"><strong>${escapeHTML(sign)}</strong></td>
              <td>${escapeHTML(r.description || r.type || "--")}${expNote}</td>
              <td class="tabular" style="white-space:nowrap;">${escapeHTML(formatTime(r.createdAt))}</td>
            </tr>
          `;
        }).join("");
  }

  function renderLogs(logs) {
    const body = $("logs-body");
    if (!logs || logs.length === 0) {
      body.innerHTML = `<tr><td colspan="6" class="empty-cell">暂无执行日志</td></tr>`;
      return;
    }
    body.innerHTML = logs.map((l) => {
      const badge = l.skipped
        ? `<span class="status-badge status-muted">跳过</span>`
        : l.success
          ? `<span class="status-badge status-ok">成功</span>`
          : `<span class="status-badge status-err">失败</span>`;
      const extra = l.meta && l.meta.promptPreview
        ? `<div class="log-sub">提问：${escapeHTML(l.meta.promptPreview)}</div>`
        : "";
      return `
        <tr>
          <td class="tabular" style="white-space:nowrap;">${escapeHTML(formatTime(l.timestamp))}</td>
          <td><code>${escapeHTML(l.trigger || "manual")}</code></td>
          <td>${escapeHTML(l.taskTitle || l.action || "--")}</td>
          <td>${badge}</td>
          <td class="tabular" style="white-space:nowrap;">${l.durationMs ? `${l.durationMs} ms` : "--"}</td>
          <td>${escapeHTML(l.message || "")}${extra}</td>
        </tr>
      `;
    }).join("");
  }

  async function handleClaimAll() {
    const btn = $("btn-claim-all");
    btn.disabled = true;
    const orig = btn.textContent;
    btn.textContent = "领取中...";
    try {
      const res = await api("/api/tasks/claim-all", { method: "POST" });
      toast(res.data?.message || "已一键领取全部每日任务", "ok");
      await loadOverview();
    } catch (e) {
      toast("一键领取失败: " + e.message, "err");
    } finally {
      btn.disabled = false;
      btn.textContent = orig;
    }
  }

  async function handleClaimOne(taskKey, btn) {
    btn.disabled = true;
    try {
      const res = await api("/api/tasks/claim", { method: "POST", body: JSON.stringify({ taskKey }) });
      toast(res.data?.message || "领取成功", "ok");
      await loadOverview();
    } catch (e) {
      toast("领取失败: " + e.message, "err");
    } finally {
      btn.disabled = false;
    }
  }

  async function handleRunOne(taskKey, btn) {
    btn.disabled = true;
    const orig = btn.textContent;
    btn.textContent = "执行中...";
    try {
      const res = await api("/api/tasks/run", { method: "POST", body: JSON.stringify({ taskKey }) });
      toast(res.data?.message || "任务已执行", res.data?.success ? "ok" : "warn");
      await loadOverview();
    } catch (e) {
      toast("执行失败: " + e.message, "err");
      await loadOverview();
    } finally {
      btn.disabled = false;
      btn.textContent = orig;
    }
  }

  async function handleRunAll() {
    const btn = $("btn-run-all");
    btn.disabled = true;
    const orig = btn.textContent;
    btn.textContent = "执行中...";
    try {
      const res = await api("/api/tasks/run-all", { method: "POST" });
      toast(res.data?.summary || "全部 Web 任务已执行", "ok");
      await loadOverview();
    } catch (e) {
      toast("批量执行失败: " + e.message, "err");
    } finally {
      btn.disabled = false;
      btn.textContent = orig;
    }
  }

  async function loadSettings() {
    try {
      const res = await api("/api/settings");
      const d = res.data || {};
      const sc = d.schedule || {};
      $("set-sched-enabled").checked = !!sc.enabled;
      $("set-auto-claim").checked = !!sc.autoClaim;
      $("set-auto-refresh").checked = !!sc.autoRefresh;
      $("set-time").value = sc.timeHHMM || "08:15";
      $("set-tz").value = sc.timezone || "Asia/Shanghai";
      $("set-jitter").value = sc.jitterMinutes ?? 15;
      $("set-task-delay").value = d.taskDelaySec ?? 3;
      $("set-cancel-ms").value = d.cancelAfterMs ?? 450;
      $("set-ua").value = d.userAgent || "";
      $("set-proxy").value = d.proxyUrl || "";
      $("set-new-pw").value = "";
    } catch (e) {
      toast("读取配置失败: " + e.message, "err");
    }
  }

  async function saveSettings(e) {
    e.preventDefault();
    const btn = $("btn-save-settings");
    btn.disabled = true;
    try {
      const payload = {
        userAgent: $("set-ua").value.trim(),
        proxyUrl: $("set-proxy").value.trim(),
        taskDelaySec: Number($("set-task-delay").value),
        cancelAfterMs: Number($("set-cancel-ms").value),
        newAdminPassword: $("set-new-pw").value,
        schedule: {
          enabled: $("set-sched-enabled").checked,
          timeHHMM: $("set-time").value.trim(),
          timezone: $("set-tz").value.trim(),
          jitterMinutes: Number($("set-jitter").value),
          autoClaim: $("set-auto-claim").checked,
          autoRefresh: $("set-auto-refresh").checked,
        },
      };
      const res = await api("/api/settings", { method: "PUT", body: JSON.stringify(payload) });
      toast(res.message || "配置已保存", "ok");
      $("set-new-pw").value = "";
      await loadOverview();
    } catch (e2) {
      toast("保存配置失败: " + e2.message, "err");
    } finally {
      btn.disabled = false;
    }
  }

  async function loadReconStatus() {
    try {
      const res = await api("/api/recon/status");
      const box = $("recon-status-box");
      if (!res.baselineExists) {
        box.innerHTML = `<strong>尚无本地基线快照</strong>（路径：<code>${escapeHTML(res.manifestPath)}</code>）。请点击「更新基线 (Pull)」建立首次快照。`;
        return;
      }
      const m = res.markers || {};
      const badge = (ok, label) => `<span class="status-badge ${ok ? "status-ok" : "status-warn"}">${escapeHTML(label)}</span>`;
      box.innerHTML = `
        <div class="tabular"><strong>本地基线时间：</strong>${escapeHTML(formatTime(res.fetchedAt))} · 资源数：<strong>${res.assetCount}</strong> · ${(Number(res.totalBytes || 0) / 1024).toFixed(1)} KB</div>
        <div class="panel-desc">基线路径：<code>${escapeHTML(res.manifestPath)}</code></div>
        <div class="marker-strip">
          ${badge(m.plazaTasksEndpoint, "/api/plaza/tasks")}
          ${badge(m.plazaClaimEndpoint, "/api/plaza/tasks/claim")}
          ${badge(m.gatewayHeader, "X-Plaza-Daily-Task-Gateway")}
          ${badge(m.wechatQrEndpoint, "WeChat QR API")}
        </div>
      `;
    } catch (e) {
      $("recon-status-box").textContent = "读取 Recon 状态失败: " + e.message;
    }
  }

  async function handleReconPull() {
    const btn = $("btn-recon-pull");
    btn.disabled = true;
    const orig = btn.textContent;
    btn.textContent = "拉取中...";
    try {
      const res = await api("/api/recon/pull", { method: "POST" });
      toast(`基线已更新（共 ${res.data?.assets?.length || 0} 个静态资源）`, "ok");
      await loadReconStatus();
    } catch (e) {
      toast("Recon Pull 失败: " + e.message, "err");
    } finally {
      btn.disabled = false;
      btn.textContent = orig;
    }
  }

  async function handleReconDiff() {
    const btn = $("btn-recon-diff");
    btn.disabled = true;
    const orig = btn.textContent;
    btn.textContent = "比对中...";
    try {
      const res = await api("/api/recon/diff", { method: "POST" });
      const d = res.data || {};
      const box = $("recon-diff-result");
      box.classList.remove("hidden");
      const statusCls = d.same ? "status-ok" : "status-warn";
      const rows = [
        ...(d.markerChanges || []).map((x) => `<tr><td><span class="status-badge status-err">契约变更</span></td><td>${escapeHTML(x)}</td></tr>`),
        ...(d.added || []).map((x) => `<tr><td><span class="status-badge status-info">新增</span></td><td><code>${escapeHTML(x.url)}</code></td></tr>`),
        ...(d.changed || []).map((x) => `<tr><td><span class="status-badge status-warn">变更</span></td><td><code>${escapeHTML(x.url)}</code></td></tr>`),
        ...(d.removed || []).map((x) => `<tr><td><span class="status-badge status-muted">移除</span></td><td><code>${escapeHTML(x.url)}</code></td></tr>`),
      ];
      box.innerHTML = `
        <div class="recon-summary">
          <div style="display:flex;align-items:center;gap:10px;margin-bottom:${rows.length ? "12px" : "0"};">
            <span class="status-badge ${statusCls}">${d.same ? "无变化" : "发现上游更新"}</span>
            <strong>${escapeHTML(d.summary || "")}</strong>
          </div>
          ${rows.length ? `<div class="table-scroll"><table class="data-table"><thead><tr><th>类型</th><th>路径 / 详情</th></tr></thead><tbody>${rows.join("")}</tbody></table></div>` : ""}
        </div>
      `;
      toast(d.summary || "比对完成", d.same ? "ok" : "warn");
    } catch (e) {
      toast("Recon Diff 失败: " + e.message, "err");
    } finally {
      btn.disabled = false;
      btn.textContent = orig;
    }
  }

  // Modal & WeChat QR
  function stopQRPoll() {
    if (qrPollTimer) {
      clearInterval(qrPollTimer);
      qrPollTimer = null;
    }
    currentSceneId = "";
  }
  function openLoginModal() { $("login-modal").classList.remove("hidden"); }
  function closeLoginModal() { stopQRPoll(); $("login-modal").classList.add("hidden"); }

  async function startWeChatQR() {
    stopQRPoll();
    const btn = $("btn-gen-qr");
    btn.disabled = true;
    $("wechat-qr-status").textContent = "正在申请微信登录二维码...";
    try {
      const res = await api("/api/bohrium/wechat/qr", { method: "POST" });
      const { sceneId, qrCodeUrl } = res.data;
      currentSceneId = sceneId;
      const img = $("wechat-qr-img");
      img.src = qrCodeUrl;
      img.classList.remove("hidden");
      $("wechat-qr-placeholder").classList.add("hidden");
      $("wechat-qr-status").textContent = "请使用微信扫一扫并确认登录（轮询中...）";

      qrPollTimer = setInterval(async () => {
        if (!currentSceneId) return;
        try {
          const poll = await api(`/api/bohrium/wechat/poll?sceneId=${encodeURIComponent(currentSceneId)}`);
          const p = poll.data || {};
          if (p.loggedIn) {
            stopQRPoll();
            $("wechat-qr-status").textContent = "扫码登录成功";
            toast("微信远程登录成功", "ok");
            closeLoginModal();
            await loadOverview();
          } else if (p.status === "expired") {
            stopQRPoll();
            $("wechat-qr-status").textContent = "二维码已过期，请重新获取";
          } else if (p.status === "scanned") {
            $("wechat-qr-status").textContent = "已扫码，请在手机微信上点击确认...";
          }
        } catch {}
      }, 2200);
    } catch (e) {
      $("wechat-qr-status").textContent = "获取二维码失败: " + e.message;
      toast("获取二维码失败: " + e.message, "err");
    } finally {
      btn.disabled = false;
    }
  }

  async function handleImportToken() {
    const raw = $("import-token-input").value.trim();
    if (!raw) return toast("请先粘贴 brmToken", "warn");
    const btn = $("btn-import-token");
    btn.disabled = true;
    try {
      await api("/api/bohrium/login/token", { method: "POST", body: JSON.stringify({ token: raw }) });
      toast("brmToken 验证并保存成功", "ok");
      $("import-token-input").value = "";
      closeLoginModal();
      await loadOverview();
    } catch (e) {
      toast("导入失败: " + e.message, "err");
    } finally {
      btn.disabled = false;
    }
  }

  async function handleOAuthStart(provider, btn) {
    selectedOAuthProvider = provider;
    document.querySelectorAll("[data-oauth-provider]").forEach((b) => b.classList.remove("active-provider"));
    btn.classList.add("active-provider");
    btn.disabled = true;
    try {
      const res = await api("/api/bohrium/oauth/url", { method: "POST", body: JSON.stringify({ provider }) });
      const authUrl = res.data?.authUrl;
      if (!authUrl) throw new Error("未获取到授权链接");
      $("oauth-active-label").textContent = `${provider.toUpperCase()} 授权链接（已尝试在新标签打开）`;
      const link = $("oauth-open-link");
      link.href = authUrl;
      link.textContent = authUrl;
      $("oauth-url-box").classList.remove("hidden");
      window.open(authUrl, "_blank", "noopener,noreferrer");
      toast(`已获取 ${provider.toUpperCase()} 官方授权链接，请在新页面完成后粘贴回调 URL`, "ok");
    } catch (e) {
      toast("获取 OAuth 链接失败: " + e.message, "err");
    } finally {
      btn.disabled = false;
    }
  }

  async function handleOAuthVerify() {
    const input = $("oauth-callback-input").value.trim();
    if (!input) return toast("请先粘贴授权跳转后的完整 URL 或 code", "warn");
    const btn = $("btn-oauth-verify");
    btn.disabled = true;
    try {
      await api("/api/bohrium/oauth/verify", {
        method: "POST",
        body: JSON.stringify({ provider: selectedOAuthProvider, callbackInput: input }),
      });
      toast(`${selectedOAuthProvider.toUpperCase()} OAuth 登录成功`, "ok");
      $("oauth-callback-input").value = "";
      closeLoginModal();
      await loadOverview();
    } catch (e) {
      toast(e.message, "err");
    } finally {
      btn.disabled = false;
    }
  }

  function currentOTPKind() {
    const checked = document.querySelector('input[name="otp-kind"]:checked');
    return checked ? checked.value : "phone";
  }

  async function handleSendOTP() {
    const kind = currentOTPKind();
    const target = $("otp-target").value.trim();
    const countryCode = $("otp-cc").value.trim() || "86";
    if (!target) return toast(kind === "phone" ? "请输入手机号" : "请输入邮箱", "warn");
    const btn = $("btn-send-otp");
    btn.disabled = true;
    const orig = btn.textContent;
    btn.textContent = "发送中...";
    try {
      const res = await api("/api/bohrium/code/send", {
        method: "POST",
        body: JSON.stringify({ kind, target, countryCode }),
      });
      toast(res.message || "验证码已发送", "ok");
    } catch (e) {
      toast(e.message, "warn");
    } finally {
      btn.disabled = false;
      btn.textContent = orig;
    }
  }

  async function handleOTPLogin() {
    const kind = currentOTPKind();
    const target = $("otp-target").value.trim();
    const countryCode = $("otp-cc").value.trim() || "86";
    const code = $("otp-code").value.trim();
    if (!target || !code) return toast("请填写账号和验证码", "warn");
    const btn = $("btn-otp-login");
    btn.disabled = true;
    try {
      await api("/api/bohrium/login/code", {
        method: "POST",
        body: JSON.stringify({ kind, target, countryCode, code }),
      });
      toast("验证码登录成功", "ok");
      $("otp-code").value = "";
      closeLoginModal();
      await loadOverview();
    } catch (e) {
      toast(e.message, "err");
    } finally {
      btn.disabled = false;
    }
  }

  async function handlePasswordLogin() {
    const account = $("pw-account").value.trim();
    const password = $("pw-secret").value;
    if (!account || !password) return toast("请输入账号与密码", "warn");
    const btn = $("btn-pw-login");
    btn.disabled = true;
    try {
      await api("/api/bohrium/login/password", { method: "POST", body: JSON.stringify({ account, password }) });
      toast("密码登录成功", "ok");
      $("pw-secret").value = "";
      closeLoginModal();
      await loadOverview();
    } catch (e) {
      toast(e.message, "warn");
    } finally {
      btn.disabled = false;
    }
  }

  // --- OpenAI Gateway Console ---
  function gatewayBaseURL() {
    return `${window.location.origin}/v1`;
  }

  function maskKey(k) {
    if (!k || k.length < 16) return k || "--";
    return `${k.slice(0, 11)}••••••••${k.slice(-4)}`;
  }

  async function copyText(text, label) {
    try {
      await navigator.clipboard.writeText(text);
      toast(`已复制${label}`, "ok");
    } catch {
      const ta = document.createElement("textarea");
      ta.value = text;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      ta.remove();
      toast(`已复制${label}`, "ok");
    }
  }

  async function loadGatewayStatus() {
    try {
      const data = await api("/api/gateway/status");
      gatewayCache = data;
      renderGateway(data);
    } catch (e) {
      toast("加载 OpenAI 网关状态失败: " + e.message, "err");
    }
  }

  function renderGateway(data) {
    const cfg = data.config || {};
    const stats = data.stats || {};
    const cat = data.catalog || null;
    const models = cat?.data || [];

    if ($("gw-base-url")) $("gw-base-url").value = gatewayBaseURL();

    $("gw-kpi-state").textContent = cfg.enabled ? "运行中" : "已停用";
    $("gw-kpi-state-sub").textContent = `默认 ${cfg.defaultModel || "dspro-high"} · ${cfg.allowNoAuth ? "免鉴权" : "Key 鉴权"}`;

    $("gw-kpi-reqs").textContent = formatNum(stats.totalRequests || 0);
    $("gw-kpi-reqs-sub").textContent = `Chat ${formatNum(stats.chatRequests || 0)} · Resp ${formatNum(stats.responseRequests || 0)} · 流式 ${formatNum(stats.streamRequests || 0)}`;

    const totalTok = (stats.promptTokens || 0) + (stats.completionTokens || 0);
    $("gw-kpi-tokens").textContent = formatNum(totalTok);
    $("gw-kpi-tokens-sub").textContent = `入 ${formatNum(stats.promptTokens || 0)} · 出 ${formatNum(stats.completionTokens || 0)} · 思 ${formatNum(stats.reasoningTokens || 0)}`;

    const visibleCount = models.filter((m) => !m.hidden).length;
    $("gw-kpi-models").textContent = models.length ? `${visibleCount} / ${models.length}` : "--";
    $("gw-kpi-models-sub").textContent = `本地会话链 ${formatNum(stats.storedResponses || 0)} 条`;

    $("gw-set-enabled").checked = !!cfg.enabled;
    $("gw-set-noauth").checked = !!cfg.allowNoAuth;
    $("gw-set-preflight").checked = !!cfg.preflightMode;
    if ($("gw-set-scimaster")) $("gw-set-scimaster").checked = cfg.sciMasterPrompt !== false;
    $("gw-set-default-model").value = cfg.defaultModel || "dspro-high";
    $("gw-set-cors").value = cfg.corsOrigins || "*";

    renderGatewayKeys(cfg.apiKeys || []);
    renderGatewayModels(models, data.catalogError, data.bohriumLoggedIn);
    renderGatewayRecent(stats.recentRequests || []);

    if (Array.isArray(data.playgroundPrompts) && data.playgroundPrompts.length > 0) {
      playgroundPrompts = data.playgroundPrompts;
      if (!pgPromptCustomized && !$("pg-prompt").value.trim()) {
        rollRandomPlaygroundPrompt();
      } else if (pgLastRandomIdx < 0 && $("pg-pool-badge")) {
        $("pg-pool-badge").textContent = `随机池 ${playgroundPrompts.length} 条`;
      }
    }
  }

  function rollRandomPlaygroundPrompt() {
    if (!playgroundPrompts.length) return "";
    let idx = Math.floor(Math.random() * playgroundPrompts.length);
    if (playgroundPrompts.length > 1 && idx === pgLastRandomIdx) {
      idx = (idx + 1 + Math.floor(Math.random() * (playgroundPrompts.length - 1))) % playgroundPrompts.length;
    }
    pgLastRandomIdx = idx;
    pgPromptCustomized = false;
    const picked = playgroundPrompts[idx];
    if ($("pg-prompt")) $("pg-prompt").value = picked;
    if ($("pg-pool-badge")) $("pg-pool-badge").textContent = `随机 #${idx + 1} / ${playgroundPrompts.length}`;
    return picked;
  }

  function renderGatewayKeys(keys) {
    const tbody = $("gw-keys-body");
    if (!keys.length) {
      tbody.innerHTML = '<tr><td colspan="4" class="empty-cell">暂无 API Key，请点击上方「新建 Key」</td></tr>';
      return;
    }
    tbody.innerHTML = keys
      .map((k) => {
        const shown = revealedKeyIDs.has(k.id) ? k.key : maskKey(k.key);
        const pill = k.enabled
          ? '<span class="status-badge done">启用</span>'
          : '<span class="status-badge skipped">停用</span>';
        return `<tr>
          <td><strong>${escapeHTML(k.name || k.id)}</strong></td>
          <td class="key-cell">${escapeHTML(shown)}</td>
          <td>${pill}</td>
          <td class="align-right">
            <div class="btn-row" style="justify-content:flex-end;">
              <button type="button" class="btn btn-ghost btn-xs" data-key-act="reveal" data-key-id="${escapeHTML(k.id)}">${revealedKeyIDs.has(k.id) ? "隐藏" : "显示"}</button>
              <button type="button" class="btn btn-outline btn-xs" data-key-act="copy" data-key-val="${escapeHTML(k.key)}">复制</button>
              <button type="button" class="btn btn-ghost btn-xs" data-key-act="toggle" data-key-id="${escapeHTML(k.id)}" data-key-enabled="${k.enabled ? "1" : "0"}">${k.enabled ? "停用" : "启用"}</button>
              <button type="button" class="btn btn-ghost btn-xs ink-err" data-key-act="delete" data-key-id="${escapeHTML(k.id)}">删除</button>
            </div>
          </td>
        </tr>`;
      })
      .join("");

    tbody.querySelectorAll("[data-key-act]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        const act = btn.getAttribute("data-key-act");
        const id = btn.getAttribute("data-key-id");
        if (act === "reveal") {
          if (revealedKeyIDs.has(id)) revealedKeyIDs.delete(id);
          else revealedKeyIDs.add(id);
          renderGatewayKeys(gatewayCache?.config?.apiKeys || []);
          return;
        }
        if (act === "copy") {
          await copyText(btn.getAttribute("data-key-val") || "", " API Key");
          return;
        }
        if (act === "toggle") {
          const cur = btn.getAttribute("data-key-enabled") === "1";
          try {
            const res = await api(`/api/gateway/keys/${encodeURIComponent(id)}`, {
              method: "PATCH",
              body: JSON.stringify({ enabled: !cur }),
            });
            if (gatewayCache?.config) gatewayCache.config.apiKeys = res.apiKeys || [];
            renderGatewayKeys(res.apiKeys || []);
            toast(cur ? "API Key 已停用" : "API Key 已启用", "ok");
          } catch (e) {
            toast(e.message, "err");
          }
          return;
        }
        if (act === "delete") {
          if (!confirm("确定删除此 API Key 吗？使用该 Key 的客户端将立即失去访问权限。")) return;
          try {
            const res = await api(`/api/gateway/keys/${encodeURIComponent(id)}`, { method: "DELETE" });
            if (gatewayCache?.config) gatewayCache.config.apiKeys = res.apiKeys || [];
            renderGatewayKeys(res.apiKeys || []);
            toast("API Key 已删除", "warn");
          } catch (e) {
            toast(e.message, "err");
          }
        }
      });
    });
  }

  function renderGatewayModels(models, catalogError, loggedIn) {
    const errEl = $("gw-catalog-error");
    if (catalogError) {
      errEl.textContent = catalogError;
      errEl.classList.remove("hidden");
    } else {
      errEl.classList.add("hidden");
    }

    const tbody = $("gw-models-body");
    if (!models.length) {
      tbody.innerHTML = `<tr><td colspan="6" class="empty-cell">${loggedIn ? "暂无模型数据，请点击「刷新模型目录」" : "请先绑定并登录玻尔账号，即可自动拉取 SciX 模型目录"}</td></tr>`;
      return;
    }

    const sorted = [...models].sort((a, b) => {
      const av = a.presentation?.visible ? 0 : 1;
      const bv = b.presentation?.visible ? 0 : 1;
      if (av !== bv) return av - bv;
      return (a.presentation?.sortOrder || 999) - (b.presentation?.sortOrder || 999);
    });

    // Update Playground model dropdown
    const pgSel = $("pg-model");
    const prevSel = pgSel.value;
    pgSel.innerHTML = sorted
      .map((m) => {
        const label = m.presentation?.name || m.name || m.id;
        const tag = m.hidden ? " [hidden]" : "";
        return `<option value="${escapeHTML(m.id)}">${escapeHTML(m.id)} (${escapeHTML(label)})${tag}</option>`;
      })
      .join("");
    if (prevSel && sorted.some((m) => m.id === prevSel)) {
      pgSel.value = prevSel;
    } else {
      const defModel = sorted.find((m) => m.id === "dsflash-high") || sorted.find((m) => m.presentation?.default) || sorted[0];
      if (defModel) pgSel.value = defModel.id;
    }

    tbody.innerHTML = sorted
      .map((m) => {
        const nativeApi = m.api === "chat_completions" ? "chat_completions" : "responses";
        const displayName = m.presentation?.name || m.name || m.id;
        const desc = m.presentation?.description || "";
        const ctxK = m.context_window ? `${Math.round(m.context_window / 1000)}K` : "--";
        const outK = m.max_output_tokens ? `${Math.round(m.max_output_tokens / 1000)}K` : "--";
        const mods = (m.input || ["text"]).join(", ");
        const effs = (m.reasoning_efforts || []).join("/");
        const mult = m.presentation?.usageMultiplier ? `${m.presentation.usageMultiplier}x` : "--";
        const badges = [
          m.presentation?.default ? '<span class="mono-tag">default</span>' : "",
          m.hidden ? '<span class="mono-tag">internal</span>' : "",
        ].filter(Boolean).join(" ");
        return `<tr>
          <td>
            <div><code>${escapeHTML(m.id)}</code> <strong>${escapeHTML(displayName)}</strong> ${badges}</div>
            ${desc ? `<div class="model-desc-sub">${escapeHTML(desc)}</div>` : ""}
          </td>
          <td><span class="mono-tag">${escapeHTML(nativeApi)}</span></td>
          <td class="tabular">${escapeHTML(ctxK)} / ${escapeHTML(outK)}</td>
          <td><div>${escapeHTML(mods)}</div>${effs ? `<div class="model-desc-sub">effort: ${escapeHTML(effs)}</div>` : ""}</td>
          <td class="tabular">${escapeHTML(mult)}</td>
          <td class="align-right">
            <div class="btn-row" style="justify-content:flex-end;">
              <button type="button" class="btn btn-ghost btn-xs" data-model-preflight="${escapeHTML(m.id)}">预检</button>
              <button type="button" class="btn btn-outline btn-xs" data-model-try="${escapeHTML(m.id)}">调试</button>
            </div>
          </td>
        </tr>`;
      })
      .join("");

    tbody.querySelectorAll("[data-model-preflight]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        const mid = btn.getAttribute("data-model-preflight");
        btn.disabled = true;
        try {
          const res = await api("/api/gateway/preflight", { method: "POST", body: JSON.stringify({ model: mid }) });
          toast(res.allowed ? `模型 ${mid} 额度预检通过 (allowed=true)` : `模型 ${mid} 预检拒绝: ${res.reason || "额度不足"}`, res.allowed ? "ok" : "warn");
        } catch (e) {
          toast("预检失败: " + e.message, "err");
        } finally {
          btn.disabled = false;
        }
      });
    });

    tbody.querySelectorAll("[data-model-try]").forEach((btn) => {
      btn.addEventListener("click", () => {
        const mid = btn.getAttribute("data-model-try");
        $("pg-model").value = mid;
        $("pg-prompt").focus();
        toast(`已在调试区选中模型 ${mid}`, "info");
      });
    });
  }

  function renderGatewayRecent(rows) {
    const tbody = $("gw-recent-body");
    if (!rows.length) {
      tbody.innerHTML = '<tr><td colspan="7" class="empty-cell">暂无网关调用记录</td></tr>';
      return;
    }
    tbody.innerHTML = rows
      .map((r) => {
        const ok = r.status >= 200 && r.status < 300 && !r.error;
        const stBadge = ok
          ? `<span class="status-badge done">${r.status}</span>`
          : `<span class="status-badge failed">${r.status || "ERR"}</span>`;
        const tokStr = `${formatNum(r.promptTokens)} / ${formatNum(r.completionTokens)} / ${formatNum(r.reasoningTokens)}`;
        return `<tr>
          <td class="tabular">${escapeHTML(formatTime(r.timestamp))}</td>
          <td><code>${escapeHTML(r.endpoint)}</code>${r.stream ? ' <span class="mono-tag">SSE</span>' : ""}</td>
          <td><code>${escapeHTML(r.model || "--")}</code></td>
          <td><span class="mono-tag">${escapeHTML(r.upstreamApi || "--")}</span></td>
          <td>${stBadge}</td>
          <td class="tabular">${formatNum(r.durationMs)} ms</td>
          <td class="tabular">${escapeHTML(tokStr)}</td>
        </tr>`;
      })
      .join("");
  }

  function firstEnabledGatewayKey() {
    const keys = gatewayCache?.config?.apiKeys || [];
    const active = keys.find((k) => k.enabled && k.key);
    return active ? active.key : "sk-brm-YOUR_KEY";
  }

  async function runGatewayPlayground() {
    const endpoint = $("pg-endpoint").value;
    const model = $("pg-model").value;
    const effort = $("pg-effort").value;
    const stream = $("pg-stream").checked;
    const autoRandom = $("pg-auto-random")?.checked;
    let prompt = $("pg-prompt").value.trim();
    if (!prompt && playgroundPrompts.length > 0) {
      prompt = rollRandomPlaygroundPrompt();
    }
    if (!prompt) return toast("请输入测试提示词", "warn");

    const shouldRollNext = !!autoRandom && !pgPromptCustomized && playgroundPrompts.length > 1;
    const btn = $("btn-pg-run");
    const wrap = $("pg-result-wrap");
    const badge = $("pg-status-badge");
    const usageEl = $("pg-usage-meta");
    const sentPromptEl = $("pg-sent-prompt");
    const reasonBox = $("pg-reasoning-box");
    const ansBox = $("pg-answer-box");

    btn.disabled = true;
    wrap.classList.remove("hidden");
    if (sentPromptEl) {
      sentPromptEl.textContent = `本次提问：${prompt}`;
      sentPromptEl.classList.remove("hidden");
    }
    reasonBox.classList.add("hidden");
    reasonBox.textContent = "";
    ansBox.textContent = "";
    usageEl.textContent = "";
    badge.textContent = `请求中 (${endpoint} / ${model})...`;
    if (shouldRollNext) {
      rollRandomPlaygroundPrompt();
    }

    const t0 = performance.now();
    try {
      const res = await fetch("/api/gateway/playground", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          endpoint,
          model,
          prompt,
          reasoningEffort: effort,
          stream,
          maxTokens: 1024,
        }),
      });
      if (!res.ok) {
        const errData = await res.json().catch(() => ({}));
        const msg = errData.error?.message || errData.error || `HTTP ${res.status}`;
        badge.textContent = `失败 (HTTP ${res.status})`;
        ansBox.textContent = msg;
        toast("调试请求失败: " + msg, "err");
        return;
      }

      if (!stream) {
        const data = await res.json();
        const ms = Math.round(performance.now() - t0);
        badge.textContent = `完成 (${ms} ms)`;
        if (endpoint === "responses") {
          const outputs = data.output || [];
          const rTexts = outputs
            .filter((o) => o.type === "reasoning")
            .flatMap((o) => (o.content || []).map((p) => p.text || ""));
          const aTexts = outputs
            .filter((o) => o.type === "message")
            .flatMap((o) => (o.content || []).map((p) => p.text || ""));
          if (rTexts.join("")) {
            reasonBox.textContent = rTexts.join("\n");
            reasonBox.classList.remove("hidden");
          }
          ansBox.textContent = aTexts.join("") || JSON.stringify(data, null, 2);
          if (data.usage) {
            usageEl.textContent = `in=${data.usage.input_tokens || 0} out=${data.usage.output_tokens || 0} reasoning=${data.usage.output_tokens_details?.reasoning_tokens || 0}`;
          }
        } else {
          const msg = data.choices?.[0]?.message || {};
          if (msg.reasoning_content) {
            reasonBox.textContent = msg.reasoning_content;
            reasonBox.classList.remove("hidden");
          }
          ansBox.textContent = msg.content || JSON.stringify(msg.tool_calls || data, null, 2);
          if (data.usage) {
            usageEl.textContent = `in=${data.usage.prompt_tokens || 0} out=${data.usage.completion_tokens || 0} reasoning=${data.usage.completion_tokens_details?.reasoning_tokens || 0}`;
          }
        }
        loadGatewayStatus();
        return;
      }

      // Streaming SSE parser (supports both Chat Completions and Responses API)
      const reader = res.body.getReader();
      const decoder = new TextDecoder();
      let buf = "";
      let reasoningText = "";
      let answerText = "";
      let usageText = "";

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += decoder.decode(value, { stream: true });
        const lines = buf.split("\n");
        buf = lines.pop() || "";
        for (const rawLine of lines) {
          const line = rawLine.trim();
          if (!line.startsWith("data:")) continue;
          const payload = line.slice(5).trim();
          if (!payload || payload === "[DONE]") continue;
          let obj;
          try { obj = JSON.parse(payload); } catch { continue; }

          if (endpoint === "responses") {
            if (obj.type === "response.reasoning_text.delta" || obj.type === "response.reasoning_summary_text.delta") {
              reasoningText += obj.delta || "";
              reasonBox.textContent = reasoningText;
              reasonBox.classList.remove("hidden");
            } else if (obj.type === "response.output_text.delta") {
              answerText += obj.delta || "";
              ansBox.textContent = answerText;
            } else if ((obj.type === "response.completed" || obj.type === "response.incomplete") && obj.response?.usage) {
              const u = obj.response.usage;
              usageText = `in=${u.input_tokens || 0} out=${u.output_tokens || 0} reasoning=${u.output_tokens_details?.reasoning_tokens || 0}`;
              usageEl.textContent = usageText;
            }
          } else {
            const delta = obj.choices?.[0]?.delta;
            if (delta?.reasoning_content) {
              reasoningText += delta.reasoning_content;
              reasonBox.textContent = reasoningText;
              reasonBox.classList.remove("hidden");
            }
            if (delta?.content) {
              answerText += delta.content;
              ansBox.textContent = answerText;
            }
            if (obj.usage) {
              usageText = `in=${obj.usage.prompt_tokens || 0} out=${obj.usage.completion_tokens || 0} reasoning=${obj.usage.completion_tokens_details?.reasoning_tokens || 0}`;
              usageEl.textContent = usageText;
            }
          }
        }
      }
      const ms = Math.round(performance.now() - t0);
      badge.textContent = `流式完成 (${ms} ms)`;
      loadGatewayStatus();
    } catch (e) {
      badge.textContent = "异常";
      ansBox.textContent = e.message;
      toast("调试异常: " + e.message, "err");
    } finally {
      btn.disabled = false;
    }
  }

  document.addEventListener("DOMContentLoaded", () => {
    initTheme();

    $("btn-theme-toggle").addEventListener("click", () => {
      const isDark = document.documentElement.getAttribute("data-theme") === "dark";
      applyTheme(isDark ? "light" : "dark");
    });

    $("admin-login-form").addEventListener("submit", async (e) => {
      e.preventDefault();
      const errBox = $("admin-login-error");
      errBox.classList.add("hidden");
      const btn = $("admin-login-btn");
      btn.disabled = true;
      try {
        await api("/api/auth/login", {
          method: "POST",
          body: JSON.stringify({ password: $("admin-password-input").value }),
        });
        $("admin-password-input").value = "";
        showAppShell();
        await Promise.all([loadOverview(), loadSettings(), loadReconStatus(), loadGatewayStatus()]);
      } catch (err) {
        errBox.textContent = err.message;
        errBox.classList.remove("hidden");
      } finally {
        btn.disabled = false;
      }
    });

    document.querySelectorAll(".nav-item").forEach((item) => {
      item.addEventListener("click", () => {
        document.querySelectorAll(".nav-item").forEach((n) => n.classList.remove("active"));
        document.querySelectorAll(".tab-panel").forEach((p) => p.classList.remove("active"));
        item.classList.add("active");
        const target = item.getAttribute("data-tab");
        $("tab-" + target)?.classList.add("active");
        $("page-title").textContent = TAB_TITLES[target] || target;
        if (target === "logs") {
          api("/api/logs?limit=100").then((r) => renderLogs(r.data || [])).catch(() => {});
        } else if (target === "gateway") {
          loadGatewayStatus();
        }
      });
    });

    document.querySelectorAll(".modal-tab").forEach((mt) => {
      mt.addEventListener("click", () => {
        document.querySelectorAll(".modal-tab").forEach((x) => x.classList.remove("active"));
        document.querySelectorAll(".mtab-panel").forEach((x) => x.classList.remove("active"));
        mt.classList.add("active");
        $("mtab-" + mt.getAttribute("data-mtab"))?.classList.add("active");
      });
    });
    document.querySelectorAll("[data-close-modal]").forEach((el) => {
      el.addEventListener("click", closeLoginModal);
    });

    $("btn-open-login").addEventListener("click", openLoginModal);
    $("btn-banner-login").addEventListener("click", openLoginModal);
    $("btn-gen-qr").addEventListener("click", startWeChatQR);
    document.querySelectorAll("[data-oauth-provider]").forEach((btn) => {
      btn.addEventListener("click", () => handleOAuthStart(btn.getAttribute("data-oauth-provider"), btn));
    });
    $("btn-oauth-verify").addEventListener("click", handleOAuthVerify);
    document.querySelectorAll('input[name="otp-kind"]').forEach((radio) => {
      radio.addEventListener("change", () => {
        const isPhone = currentOTPKind() === "phone";
        $("otp-cc-wrap").classList.toggle("hidden", !isPhone);
        $("otp-target-label").textContent = isPhone ? "手机号" : "邮箱地址";
        $("otp-target").placeholder = isPhone ? "13800138000" : "name@example.com";
      });
    });
    $("btn-send-otp").addEventListener("click", handleSendOTP);
    $("btn-otp-login").addEventListener("click", handleOTPLogin);
    $("btn-import-token").addEventListener("click", handleImportToken);
    $("btn-pw-login").addEventListener("click", handlePasswordLogin);
    $("btn-claim-all").addEventListener("click", handleClaimAll);
    $("btn-run-all").addEventListener("click", handleRunAll);
    $("btn-refresh-all").addEventListener("click", () => loadOverview().then(() => toast("已刷新最新数据", "ok")));
    $("btn-refresh-assets").addEventListener("click", () => loadOverview().then(() => toast("资产数据已刷新", "ok")));
    $("btn-refresh-logs").addEventListener("click", () => api("/api/logs?limit=100").then((r) => { renderLogs(r.data || []); toast("日志已刷新", "ok"); }));
    $("btn-refresh-token").addEventListener("click", async () => {
      try {
        await api("/api/bohrium/refresh-token", { method: "POST" });
        toast("Token 续期成功", "ok");
        await loadOverview();
      } catch (e) {
        toast("Token 续期失败: " + e.message, "err");
      }
    });
    $("btn-admin-logout").addEventListener("click", async () => {
      await api("/api/auth/logout", { method: "POST" }).catch(() => {});
      showLoginGate();
    });
    $("btn-bohrium-logout").addEventListener("click", async () => {
      if (!confirm("确定清除已保存的玻尔登录凭据吗？")) return;
      await api("/api/bohrium/logout", { method: "POST" });
      toast("已清除玻尔登录凭据", "warn");
      await loadOverview();
    });
    $("settings-form").addEventListener("submit", saveSettings);
    $("btn-recon-pull").addEventListener("click", handleReconPull);
    $("btn-recon-diff").addEventListener("click", handleReconDiff);

    // OpenAI Gateway controls
    $("btn-copy-gw-url").addEventListener("click", () => copyText(gatewayBaseURL(), " Base URL"));
    $("btn-copy-curl-chat").addEventListener("click", () => {
      const m = $("gw-set-default-model").value.trim() || "dspro-high";
      const snippet = `curl ${gatewayBaseURL()}/chat/completions \\\n  -H "Authorization: Bearer ${firstEnabledGatewayKey()}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"${m}","stream":true,"messages":[{"role":"user","content":"Hello!"}]}'`;
      copyText(snippet, " Chat Completions cURL");
    });
    $("btn-copy-curl-resp").addEventListener("click", () => {
      const m = $("gw-set-default-model").value.trim() || "dspro-high";
      const snippet = `curl ${gatewayBaseURL()}/responses \\\n  -H "Authorization: Bearer ${firstEnabledGatewayKey()}" \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"${m}","stream":true,"input":"Hello!"}'`;
      copyText(snippet, " Responses API cURL");
    });
    $("btn-copy-curl-balance").addEventListener("click", () => {
      const snippet = `curl ${gatewayBaseURL()}/balance \\\n  -H "Authorization: Bearer ${firstEnabledGatewayKey()}"`;
      copyText(snippet, " Balance cURL");
    });
    $("btn-pg-random").addEventListener("click", () => {
      if (rollRandomPlaygroundPrompt()) toast("已随机切换测试提示词", "info");
    });
    $("pg-prompt").addEventListener("input", () => {
      pgPromptCustomized = $("pg-prompt").value.trim().length > 0;
      if (pgPromptCustomized && $("pg-pool-badge")) {
        $("pg-pool-badge").textContent = "自定义输入";
      }
    });
    $("btn-save-gw-settings").addEventListener("click", async () => {
      try {
        await api("/api/gateway/settings", {
          method: "PUT",
          body: JSON.stringify({
            enabled: $("gw-set-enabled").checked,
            allowNoAuth: $("gw-set-noauth").checked,
            preflightMode: $("gw-set-preflight").checked,
            sciMasterPrompt: $("gw-set-scimaster") ? $("gw-set-scimaster").checked : true,
            defaultModel: $("gw-set-default-model").value.trim() || "dspro-high",
            corsOrigins: $("gw-set-cors").value.trim() || "*",
          }),
        });
        toast("OpenAI 网关配置已保存", "ok");
        await loadGatewayStatus();
      } catch (e) {
        toast("保存失败: " + e.message, "err");
      }
    });
    $("btn-gw-create-key").addEventListener("click", async () => {
      const name = $("gw-new-key-name").value.trim();
      try {
        const res = await api("/api/gateway/keys", { method: "POST", body: JSON.stringify({ name }) });
        $("gw-new-key-name").value = "";
        if (res.key?.id) revealedKeyIDs.add(res.key.id);
        toast("已新建 API Key", "ok");
        await loadGatewayStatus();
      } catch (e) {
        toast("创建失败: " + e.message, "err");
      }
    });
    $("btn-gw-refresh-models").addEventListener("click", async () => {
      const btn = $("btn-gw-refresh-models");
      btn.disabled = true;
      try {
        await api("/api/gateway/models/refresh", { method: "POST" });
        toast("已刷新 Bohrium SciX 模型目录", "ok");
        await loadGatewayStatus();
      } catch (e) {
        toast("刷新失败: " + e.message, "err");
      } finally {
        btn.disabled = false;
      }
    });
    $("btn-gw-refresh-stats").addEventListener("click", () => loadGatewayStatus().then(() => toast("已刷新网关调用统计", "ok")));
    $("btn-pg-run").addEventListener("click", runGatewayPlayground);

    checkAuthAndBoot();
  });
})();
