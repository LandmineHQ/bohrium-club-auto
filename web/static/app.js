(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  let qrPollTimer = null;
  let currentSceneId = "";
  let cachedOverview = null;

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
      throw new Error(data.error || data.message || `HTTP ${res.status}`);
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
        await Promise.all([loadOverview(), loadSettings(), loadReconStatus()]);
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
      cachedOverview = data;
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
    const banner = $("not-logged-in-banner");
    const refreshBtn = $("btn-refresh-token");

    if (sess.hasToken && sess.valid) {
      pill.className = "account-pill online";
      const days = sess.expiresInSec > 0 ? ` · 剩余 ${Math.floor(sess.expiresInSec / 86400)} 天` : "";
      pillText.textContent = `${auth.nickname || "UID " + auth.userId}${days}`;
      banner.classList.add("hidden");
      refreshBtn.classList.remove("hidden");
    } else if (sess.hasToken) {
      pill.className = "account-pill offline";
      pillText.textContent = `Token 已失效（请重新扫码）`;
      banner.classList.remove("hidden");
      refreshBtn.classList.remove("hidden");
    } else {
      pill.className = "account-pill offline";
      pillText.textContent = "未登录玻尔账号";
      banner.classList.remove("hidden");
      refreshBtn.classList.add("hidden");
    }

    // KPI 1: Photons
    const assets = data.assets;
    if (assets) {
      $("kpi-photons").textContent = `${formatNum(assets.photonBalance)} 光子`;
      const exp7 = assets.expiring7DayPhotons > 0 ? ` · 7日过期 ${formatNum(assets.expiring7DayPhotons)}` : "";
      $("kpi-photons-sub").textContent = `组织 ${formatNum(assets.orgBalance)} · 钱包 ¥${formatNum(assets.walletBalance)}${exp7}`;

      // KPI 2: Storage
      const st = assets.storage || {};
      $("kpi-storage").textContent = st.remainingHuman || "--";
      const pct = Math.min(100, Math.max(0, Number(st.usedPercent || 0)));
      $("kpi-storage-bar").style.width = `${pct.toFixed(1)}%`;
      $("kpi-storage-sub").textContent = `已用 ${st.usedHuman || "--"} / 总计 ${st.totalHuman || "--"} (${pct.toFixed(1)}%)`;

      // KPI 3: Membership & Quotas
      const lvl = assets.membershipLevel !== null && assets.membershipLevel !== undefined ? `Lv.${assets.membershipLevel}` : "普通用户";
      $("kpi-membership").textContent = lvl;
      $("kpi-membership-sub").textContent = `文献 ${formatQuota(assets.paperQuota)} · 知识库对话 ${formatQuota(assets.knowledgeChat)} · 深度研究 ${formatQuota(assets.deepResearch)}`;
    } else {
      $("kpi-photons").textContent = "--";
      $("kpi-photons-sub").textContent = data.assetsError || "登录后自动显示";
      $("kpi-storage").textContent = "--";
      $("kpi-storage-bar").style.width = "0%";
      $("kpi-storage-sub").textContent = "登录后自动显示";
      $("kpi-membership").textContent = "--";
      $("kpi-membership-sub").textContent = "登录后自动显示";
    }

    // KPI 4: Tasks & Scheduler
    const tasks = data.tasks;
    const sched = data.scheduler || {};
    if (tasks) {
      const webTotal = Math.max(0, tasks.totalCount - tasks.skippedCount);
      $("kpi-tasks").textContent = `${tasks.completedCount} / ${webTotal} Web任务`;
      $("kpi-tasks-sub").textContent = `今日已获 +${formatNum(tasks.earnedPhotons)} 光子 · 下次定时 ${sched.nextRunAt || "已关闭"}`;
      $("task-date-badge").textContent = tasks.taskDate || "今日";
    } else {
      $("kpi-tasks").textContent = "--";
      $("kpi-tasks-sub").textContent = `下次定时：${sched.nextRunAt || "已关闭"}`;
    }

    // Render Tasks Grid
    const errEl = $("tasks-error");
    if (data.tasksError) {
      errEl.textContent = data.tasksError;
      errEl.classList.remove("hidden");
    } else {
      errEl.classList.add("hidden");
    }
    renderTasksGrid(tasks);

    // Corpus & Runtime Stats
    const ps = data.promptStats || {};
    const pdfs = data.pdfStats || {};
    $("stat-scispace").textContent = `${ps.scienceSpacePrompts || 0} 条`;
    $("stat-writing").textContent = `${ps.academicWritingPrompts || 0} 条`;
    $("stat-reading").textContent = `${ps.literatureReadingPrompts || 0} 条`;
    $("stat-papers").textContent = `${ps.paperCatalogCount || 0} 篇`;
    $("stat-pdf-cache").textContent = `${pdfs.cached || 0} 篇缓存 / ${pdfs.custom || 0} 篇自定义`;
    $("stat-sched-state").textContent = sched.running
      ? `执行中 (${sched.currentJob})`
      : sched.enabled
        ? `已开启（每日 ${sched.timeHHMM} ${sched.timezone} +0~${sched.jitterMinutes}m）`
        : "已暂停";
    $("stat-last-day").textContent = sched.lastScheduledDay
      ? `${sched.lastScheduledDay} (${sched.lastRunSummary || "完成"})`
      : "尚无记录";
    $("stat-ua").textContent = data.userAgent || "--";
  }

  function renderTasksGrid(tasksResp) {
    const grid = $("tasks-grid");
    if (!tasksResp || !Array.isArray(tasksResp.tasks) || tasksResp.tasks.length === 0) {
      grid.innerHTML = `<div class="empty-state">请先远程登录玻尔账号，登录后即可查看并触发每日任务。</div>`;
      return;
    }
    grid.innerHTML = tasksResp.tasks.map((t) => {
      let badge = `<span class="pill pill-muted">${escapeHTML(t.statusLabel)}</span>`;
      if (t.skipped) badge = `<span class="pill pill-muted">已跳过</span>`;
      else if (t.status === 1) badge = `<span class="pill pill-ok">已完成</span>`;
      else if (t.status === 0) badge = `<span class="pill pill-info">待完成</span>`;
      else if (t.status === -1) badge = `<span class="pill pill-warn">未领取</span>`;

      const cardCls = t.skipped ? "task-card skipped" : t.status === 1 ? "task-card done" : "task-card";
      let buttons = "";
      if (t.skipped) {
        buttons = `<button class="btn btn-ghost btn-sm" disabled>${escapeHTML(t.skipReason || "客户端任务自动跳过")}</button>`;
      } else {
        if (t.status === -1) {
          buttons += `<button class="btn btn-secondary btn-sm" data-claim-one="${escapeHTML(t.taskKey)}">领取任务</button>`;
        }
        const runLabel = t.status === 1 ? "再次手动触发" : "手动完成任务";
        buttons += `<button class="btn btn-primary btn-sm" data-run-one="${escapeHTML(t.taskKey)}">${runLabel}</button>`;
      }

      return `
        <div class="${cardCls}">
          <div>
            <div class="task-top">
              <div class="task-title">${escapeHTML(t.titleZh || t.taskKey)}</div>
              ${badge}
            </div>
            <div class="task-desc">${escapeHTML(t.descriptionZh || "")}</div>
          </div>
          <div>
            <div class="task-meta">
              <span class="reward-badge">+${formatNum(t.rewardAmount)} 光子</span>
              <code>${escapeHTML(t.actionKey || t.taskKey)}</code>
            </div>
            <div class="task-actions">${buttons}</div>
          </div>
        </div>
      `;
    }).join("");

    grid.querySelectorAll("[data-claim-one]").forEach((btn) => {
      btn.addEventListener("click", () => handleClaimOne(btn.getAttribute("data-claim-one"), btn));
    });
    grid.querySelectorAll("[data-run-one]").forEach((btn) => {
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
      grid.innerHTML = `<div class="empty-state">请先登录玻尔账号以获取实时积分与容量数据。</div>`;
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
      ["个人可用光子", `${formatNum(assets.photonBalance)} 光子`],
      ["组织光子余额 / 上限", `${formatNum(assets.orgBalance)} / ${formatNum(assets.orgBalanceLimit)}`],
      ["钱包现金余额", `¥ ${formatNum(assets.walletBalance)}`],
      ["7 日内过期光子", `${formatNum(assets.expiring7DayPhotons)} 光子`],
      ["知识库剩余存储", `${st.remainingHuman || "--"}（总 ${st.totalHuman || "--"}）`],
      ["文献检索剩余配额", formatQuota(assets.paperQuota)],
      ["知识库对话剩余配额", formatQuota(assets.knowledgeChat)],
      ["深度研究剩余配额", formatQuota(assets.deepResearch)],
    ];
    grid.innerHTML = items.map(([label, val]) => `
      <div class="quota-item"><div class="label">${escapeHTML(label)}</div><div class="val">${escapeHTML(val)}</div></div>
    `).join("");

    const lots = assets.expiringLots || [];
    lotsBody.innerHTML = lots.length === 0
      ? `<tr><td colspan="4" class="empty-cell">暂无即将过期光子批次</td></tr>`
      : lots.map((l) => `
          <tr>
            <td><strong>${formatNum(l.remaining)}</strong></td>
            <td>${escapeHTML(l.description || l.sourceType || "--")}</td>
            <td>${escapeHTML(l.createdTime || "--")}</td>
            <td>${escapeHTML(l.expireTime || "--")}</td>
          </tr>
        `).join("");

    const hist = assets.recentIntegralHistory || [];
    histBody.innerHTML = hist.length === 0
      ? `<tr><td colspan="3" class="empty-cell">暂无近期光子流水</td></tr>`
      : hist.map((r) => {
          const sign = r.amount > 0 ? `+${formatNum(r.amount)}` : formatNum(r.amount);
          const cls = r.amount >= 0 ? "text-ok" : "text-danger";
          return `
            <tr>
              <td class="${cls}"><strong>${escapeHTML(sign)}</strong></td>
              <td>${escapeHTML(r.description || r.type || "--")}</td>
              <td>${escapeHTML(r.createdAt || "--")}</td>
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
        ? `<span class="pill pill-muted">跳过</span>`
        : l.success
          ? `<span class="pill pill-ok">成功</span>`
          : `<span class="pill pill-danger">失败</span>`;
      const extra = l.meta && l.meta.promptPreview
        ? `<div class="muted small">提问：${escapeHTML(l.meta.promptPreview)}</div>`
        : "";
      return `
        <tr>
          <td>${escapeHTML(formatTime(l.timestamp))}</td>
          <td><code>${escapeHTML(l.trigger || "manual")}</code></td>
          <td>${escapeHTML(l.taskTitle || l.action || "--")}</td>
          <td>${badge}</td>
          <td>${l.durationMs ? `${l.durationMs} ms` : "--"}</td>
          <td>${escapeHTML(l.message || "")}${extra}</td>
        </tr>
      `;
    }).join("");
  }

  // Task actions
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
    } finally {
      btn.disabled = false;
      btn.textContent = orig;
    }
  }

  async function handleRunAll() {
    const btn = $("btn-run-all");
    btn.disabled = true;
    const orig = btn.textContent;
    btn.textContent = "正在依次执行 3 项 Web 任务...";
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

  // Settings
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

  // Recon
  async function loadReconStatus() {
    try {
      const res = await api("/api/recon/status");
      const box = $("recon-status-box");
      if (!res.baselineExists) {
        box.innerHTML = `<strong>尚无本地基线快照</strong>（路径：<code>${escapeHTML(res.manifestPath)}</code>）。请点击右上角「拉取 / 更新基线」建立首次快照。`;
        return;
      }
      const m = res.markers || {};
      const badge = (ok, label) => `<span class="pill ${ok ? "pill-ok" : "pill-warn"}">${ok ? "✓" : "?"} ${label}</span>`;
      box.innerHTML = `
        <div><strong>本地基线时间：</strong>${escapeHTML(formatTime(res.fetchedAt))} · 资源数：<strong>${res.assetCount}</strong> · 总计 ${(Number(res.totalBytes || 0) / 1024).toFixed(1)} KB</div>
        <div class="muted small">基线文件：<code>${escapeHTML(res.manifestPath)}</code></div>
        <div class="marker-row">
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
    btn.textContent = "抓取线上快照中...";
    try {
      const res = await api("/api/recon/pull", { method: "POST" });
      toast(`已更新前端基线（共 ${res.data?.assets?.length || 0} 个静态资源）`, "ok");
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
    btn.textContent = "比对线上源码中...";
    try {
      const res = await api("/api/recon/diff", { method: "POST" });
      const d = res.data || {};
      const box = $("recon-diff-result");
      box.classList.remove("hidden");
      const statusCls = d.same ? "pill-ok" : "pill-warn";
      const rows = [
        ...(d.markerChanges || []).map((x) => `<tr><td><span class="pill pill-danger">契约变更</span></td><td>${escapeHTML(x)}</td></tr>`),
        ...(d.added || []).map((x) => `<tr><td><span class="pill pill-info">新增</span></td><td><code>${escapeHTML(x.url)}</code></td></tr>`),
        ...(d.changed || []).map((x) => `<tr><td><span class="pill pill-warn">变更</span></td><td><code>${escapeHTML(x.url)}</code></td></tr>`),
        ...(d.removed || []).map((x) => `<tr><td><span class="pill pill-muted">移除</span></td><td><code>${escapeHTML(x.url)}</code></td></tr>`),
      ];
      box.innerHTML = `
        <div class="recon-box">
          <div style="display:flex;align-items:center;gap:10px;margin-bottom:8px;">
            <span class="pill ${statusCls}">${d.same ? "无变化" : "发现上游更新"}</span>
            <strong>${escapeHTML(d.summary || "")}</strong>
          </div>
          ${rows.length ? `<div class="table-wrap"><table><thead><tr><th>类型</th><th>详情</th></tr></thead><tbody>${rows.join("")}</tbody></table></div>` : ""}
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

  // Remote Login Modal & WeChat QR Polling
  function stopQRPoll() {
    if (qrPollTimer) {
      clearInterval(qrPollTimer);
      qrPollTimer = null;
    }
    currentSceneId = "";
  }

  function openLoginModal() {
    $("login-modal").classList.remove("hidden");
  }

  function closeLoginModal() {
    stopQRPoll();
    $("login-modal").classList.add("hidden");
  }

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
      $("wechat-qr-status").textContent = "请使用微信扫一扫关注/确认登录（自动轮询中...）";

      qrPollTimer = setInterval(async () => {
        if (!currentSceneId) return;
        try {
          const poll = await api(`/api/bohrium/wechat/poll?sceneId=${encodeURIComponent(currentSceneId)}`);
          const p = poll.data || {};
          if (p.loggedIn) {
            stopQRPoll();
            $("wechat-qr-status").textContent = "扫码登录成功！";
            toast("微信远程登录成功！", "ok");
            closeLoginModal();
            await loadOverview();
          } else if (p.status === "expired") {
            stopQRPoll();
            $("wechat-qr-status").textContent = "二维码已过期，请点击刷新重新获取";
          } else if (p.status === "scanned") {
            $("wechat-qr-status").textContent = "已扫码，请在手机微信上点击确认登录...";
          }
        } catch (err) {
          // Transient poll errors are tolerated while polling
        }
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
      toast("brmToken 验证并保存成功！", "ok");
      $("import-token-input").value = "";
      closeLoginModal();
      await loadOverview();
    } catch (e) {
      toast("导入失败: " + e.message, "err");
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
      toast("密码登录成功！", "ok");
      $("pw-secret").value = "";
      closeLoginModal();
      await loadOverview();
    } catch (e) {
      toast(e.message, "warn");
    } finally {
      btn.disabled = false;
    }
  }

  // Event wiring
  document.addEventListener("DOMContentLoaded", () => {
    // Admin login form
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
        await Promise.all([loadOverview(), loadSettings(), loadReconStatus()]);
      } catch (err) {
        errBox.textContent = err.message;
        errBox.classList.remove("hidden");
      } finally {
        btn.disabled = false;
      }
    });

    // Main tabs
    document.querySelectorAll(".tab").forEach((tab) => {
      tab.addEventListener("click", () => {
        document.querySelectorAll(".tab").forEach((t) => t.classList.remove("active"));
        document.querySelectorAll(".tab-panel").forEach((p) => p.classList.remove("active"));
        tab.classList.add("active");
        const target = tab.getAttribute("data-tab");
        $("tab-" + target)?.classList.add("active");
        if (target === "logs") {
          api("/api/logs?limit=100").then((r) => renderLogs(r.data || [])).catch(() => {});
        }
      });
    });

    // Modal tabs & close
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

    // Buttons
    $("btn-open-login").addEventListener("click", openLoginModal);
    $("btn-banner-login").addEventListener("click", openLoginModal);
    $("btn-gen-qr").addEventListener("click", startWeChatQR);
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
        toast("Token 续期成功！", "ok");
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

    checkAuthAndBoot();
  });
})();
