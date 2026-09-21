/* Robyou GUI — talks to the Go backend through the Wails bindings on
   window.go.main.App and the event bus on window.runtime. */

const PHASE_LABELS = {
  idle: "未开始",
  logging_in: "登录中",
  ready: "就绪",
  waiting: "等待选课开放",
  running: "运行中",
  stopping: "停止中",
  finished: "已结束",
  error: "出错",
};

const STATE_LABELS = {
  pending: "等待",
  waiting: "等待开放",
  running: "运行中",
  succeeded: "已完成",
  completed: "已结束",
  stopped: "已停止",
  failed: "失败",
  skipped: "已跳过",
};

const MAX_LOG_NODES = 3000;

const state = {
  config: null,
  paths: null,
  status: null,
  dirty: false,
  lastSeq: 0,
  levels: new Set(["debug", "info", "success", "warn", "error"]),
  search: "",
  catalog: { stats: null, results: null },
  // publicCategories is the 素质教育类别 dropdown, fetched from the backend so
  // the numbers the school system uses only have to be written down once.
  publicCategories: [],
  scale: 1,
};

// The catalog panel keeps two searches apart on purpose: a local one over the
// cache, which is free and instant, and a server one, which spends requests
// under the rate limit and is the only way new courses enter the cache.
const CATALOG_ROW_LIMIT = 500;

const $ = (id) => document.getElementById(id);

// PUBLIC_CATEGORY_ALL is the dropdown's 「--所有课程--」 entry: category 0 is
// not a category, it is the absence of a filter.
const PUBLIC_CATEGORY_ALL = 0;

function categoryName(value) {
  if (value === null || value === undefined) return "";
  const found = state.publicCategories.find((category) => category.value === value);
  return found ? found.name : `类别 ${value}`;
}

// categoryOptions renders the dropdown, with the "no filter" entry relabelled
// for wherever it is being used.
function categoryOptions(selected, allLabel) {
  const chosen = selected === null || selected === undefined ? PUBLIC_CATEGORY_ALL : selected;
  return state.publicCategories
    .map((category) => {
      const label = category.value === PUBLIC_CATEGORY_ALL ? allLabel : category.name;
      return `<option value="${category.value}" ${category.value === chosen ? "selected" : ""}>${escapeHTML(
        label
      )}</option>`;
    })
    .join("");
}

/* ---------------------------------------------------------------- helpers */

function api() {
  return window.go && window.go.main && window.go.main.App;
}

function waitForBackend() {
  return new Promise((resolve) => {
    const tick = () => (api() && window.runtime ? resolve() : setTimeout(tick, 40));
    tick();
  });
}

function toast(message, kind = "info") {
  const node = document.createElement("div");
  node.className = "toast";
  node.dataset.kind = kind;
  node.textContent = message;
  $("toast-stack").appendChild(node);
  setTimeout(() => node.remove(), kind === "error" ? 6000 : 3200);
}

function errorText(err) {
  if (!err) return "未知错误";
  if (typeof err === "string") return err;
  return err.message || String(err);
}

function toNumber(value, fallback = 0) {
  const parsed = Number.parseFloat(value);
  return Number.isFinite(parsed) ? parsed : fallback;
}

function splitList(value) {
  return String(value || "")
    .split(/[\n,，]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function joinList(items) {
  return (items || []).join(", ");
}

function parseFilters(value) {
  const filters = {};
  for (const line of String(value || "").split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const index = trimmed.indexOf("=");
    if (index <= 0) continue;
    filters[trimmed.slice(0, index).trim()] = trimmed.slice(index + 1).trim();
  }
  return Object.keys(filters).length ? filters : null;
}

function formatFilters(filters) {
  if (!filters) return "";
  return Object.entries(filters)
    .map(([key, value]) => `${key}=${value}`)
    .join("\n");
}

function escapeHTML(value) {
  return String(value == null ? "" : value).replace(
    /[&<>"']/g,
    (char) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[char])
  );
}

function markDirty(dirty = true) {
  state.dirty = dirty;
  $("dirty-flag").hidden = !dirty;
}

/* ------------------------------------------------------------------- jobs */

function newTarget() {
  return {
    name: "新课程目标",
    type: "inplan",
    keyword: "",
    enabled: true,
    request_delay_seconds: 0.5,
    continue_after_successful: false,
    fuzzy_filter_keywords: [],
    exact_filter_keywords: [],
  };
}

function newJob(index) {
  return {
    id: `job-${index + 1}-${Date.now().toString(36)}`,
    name: `任务 ${index + 1}`,
    enabled: true,
    interval_seconds: 3,
    max_rounds: 0,
    stop_on_first_success: false,
    targets: [newTarget()],
  };
}

function renderJobs() {
  const container = $("jobs-container");
  const jobs = state.config.jobs || [];

  if (!jobs.length) {
    container.innerHTML = `<p class="empty">还没有任务。点击右上角「新建任务」开始。</p>`;
    return;
  }

  container.innerHTML = jobs
    .map((job, jobIndex) => renderJob(job, jobIndex, jobs.length))
    .join("");
}

function renderJob(job, jobIndex, total) {
  const targets = job.targets || [];
  return `
    <div class="job ${job.enabled ? "" : "disabled"}" data-job="${jobIndex}">
      <div class="job-head">
        <span class="job-order">${jobIndex + 1}</span>
        <label class="check" title="启用该任务">
          <input type="checkbox" data-job="${jobIndex}" data-field="enabled" ${job.enabled ? "checked" : ""} />
        </label>
        <input type="text" data-job="${jobIndex}" data-field="name" value="${escapeHTML(job.name)}" placeholder="任务名称" />
        <button class="icon-btn" data-action="move-job-up" data-job="${jobIndex}" ${jobIndex === 0 ? "disabled" : ""} title="上移">↑</button>
        <button class="icon-btn" data-action="move-job-down" data-job="${jobIndex}" ${jobIndex === total - 1 ? "disabled" : ""} title="下移">↓</button>
        <button class="icon-btn" data-action="dup-job" data-job="${jobIndex}" title="复制">⧉</button>
        <button class="icon-btn danger" data-action="del-job" data-job="${jobIndex}" title="删除">✕</button>
      </div>

      <div class="job-body">
        <div class="grid-3">
          <label class="field">
            <span>轮询间隔 (秒)</span>
            <input type="number" min="0" step="0.5" data-job="${jobIndex}" data-field="interval_seconds" value="${job.interval_seconds ?? 3}" />
          </label>
          <label class="field">
            <span>最大轮次 (0=不限)</span>
            <input type="number" min="0" step="1" data-job="${jobIndex}" data-field="max_rounds" value="${job.max_rounds ?? 0}" />
          </label>
          <label class="check" style="align-self: end; padding-bottom: 7px;">
            <input type="checkbox" data-job="${jobIndex}" data-field="stop_on_first_success" ${job.stop_on_first_success ? "checked" : ""} />
            <span>选中一门即结束</span>
          </label>
        </div>

        <div class="section-label">课程目标 (${targets.length})</div>
        <div class="targets">
          ${targets.map((target, targetIndex) => renderTarget(target, jobIndex, targetIndex, targets.length)).join("")}
        </div>
        <button class="btn btn-small" data-action="add-target" data-job="${jobIndex}">+ 添加课程目标</button>
      </div>
    </div>`;
}

function renderTarget(target, jobIndex, targetIndex, total) {
  const isPublic = target.type === "public";
  const attrs = `data-job="${jobIndex}" data-target="${targetIndex}"`;

  return `
    <div class="target ${target.enabled ? "" : "disabled"}">
      <div class="target-head">
        <label class="check" title="启用该课程目标">
          <input type="checkbox" ${attrs} data-field="enabled" ${target.enabled ? "checked" : ""} />
        </label>
        <input type="text" ${attrs} data-field="name" value="${escapeHTML(target.name)}" placeholder="备注名称" />
        <button class="icon-btn" data-action="move-target-up" ${attrs} ${targetIndex === 0 ? "disabled" : ""} title="上移">↑</button>
        <button class="icon-btn" data-action="move-target-down" ${attrs} ${targetIndex === total - 1 ? "disabled" : ""} title="下移">↓</button>
        <button class="icon-btn danger" data-action="del-target" ${attrs} title="删除">✕</button>
      </div>

      <div class="grid-2">
        <label class="field">
          <span>课程类型</span>
          <select ${attrs} data-field="type">
            <option value="inplan" ${isPublic ? "" : "selected"}>计划内课程 (inplan)</option>
            <option value="public" ${isPublic ? "selected" : ""}>公选课 (public)</option>
          </select>
        </label>
        <label class="field">
          <span>搜索关键词</span>
          <input type="text" ${attrs} data-field="keyword" value="${escapeHTML(target.keyword)}" placeholder="课程名或课程号" />
        </label>
      </div>

      <div class="grid-2">
        <label class="field">
          <span>选课请求间隔 (秒)</span>
          <input type="number" min="0" step="0.1" ${attrs} data-field="request_delay_seconds" value="${target.request_delay_seconds ?? 0.5}" />
        </label>
        ${
          isPublic
            ? `<label class="field">
                 <span>公选课类别 (素质教育类别)</span>
                 <select ${attrs} data-field="public_category">
                   ${categoryOptions(target.public_category, "全部课程 (不限类别)")}
                 </select>
               </label>`
            : `<label class="check" style="align-self: end; padding-bottom: 7px;">
                 <input type="checkbox" ${attrs} data-field="continue_after_successful" ${
                   target.continue_after_successful ? "checked" : ""
                 } />
                 <span>选中后继续搜索</span>
               </label>`
        }
      </div>

      ${
        isPublic
          ? `<label class="check">
               <input type="checkbox" ${attrs} data-field="continue_after_successful" ${
                 target.continue_after_successful ? "checked" : ""
               } />
               <span>选中后继续搜索</span>
             </label>`
          : ""
      }

      <div class="grid-2">
        <label class="field">
          <span>模糊排除 (逗号分隔)</span>
          <input type="text" ${attrs} data-field="fuzzy_filter_keywords" value="${escapeHTML(
            joinList(target.fuzzy_filter_keywords)
          )}" placeholder="教师或课程名片段" />
        </label>
        <label class="field">
          <span>精确排除 (逗号分隔)</span>
          <input type="text" ${attrs} data-field="exact_filter_keywords" value="${escapeHTML(
            joinList(target.exact_filter_keywords)
          )}" placeholder="完整教师名或课程名" />
        </label>
      </div>

      <label class="field">
        <span>高级搜索参数 <em class="inline-hint">每行 key=value，覆盖默认过滤项</em></span>
        <textarea rows="2" ${attrs} data-field="filters" placeholder="sfym=false">${escapeHTML(
          formatFilters(target.filters)
        )}</textarea>
      </label>
    </div>`;
}

/* --------------------------------------------------------- config binding */

function applyFieldEdit(element) {
  const jobIndex = Number.parseInt(element.dataset.job, 10);
  const targetIndex = element.dataset.target === undefined ? null : Number.parseInt(element.dataset.target, 10);
  const field = element.dataset.field;
  if (!field || Number.isNaN(jobIndex)) return;

  const job = state.config.jobs[jobIndex];
  if (!job) return;
  const node = targetIndex === null ? job : job.targets[targetIndex];
  if (!node) return;

  switch (field) {
    case "enabled":
    case "stop_on_first_success":
    case "continue_after_successful":
      node[field] = element.checked;
      break;
    case "interval_seconds":
    case "request_delay_seconds":
      node[field] = toNumber(element.value, 0);
      break;
    case "max_rounds":
      node[field] = Math.max(0, Math.trunc(toNumber(element.value, 0)));
      break;
    case "public_category": {
      // 全部课程 is the school UI's "no filter" entry, so it is stored as an
      // absent category rather than as the number 0.
      const value = Math.trunc(toNumber(element.value, PUBLIC_CATEGORY_ALL));
      node[field] = value === PUBLIC_CATEGORY_ALL ? null : value;
      break;
    }
    case "fuzzy_filter_keywords":
    case "exact_filter_keywords":
      node[field] = splitList(element.value);
      break;
    case "filters":
      node[field] = parseFilters(element.value);
      break;
    case "type":
      node[field] = element.value;
      renderJobs();
      break;
    default:
      node[field] = element.value;
  }

  markDirty();
}

function handleStructuralAction(action, jobIndex, targetIndex) {
  const jobs = state.config.jobs;

  switch (action) {
    case "add-job":
      jobs.push(newJob(jobs.length));
      break;
    case "del-job":
      jobs.splice(jobIndex, 1);
      break;
    case "dup-job": {
      const copy = JSON.parse(JSON.stringify(jobs[jobIndex]));
      copy.id = `${copy.id}-copy-${Date.now().toString(36)}`;
      copy.name = `${copy.name} 副本`;
      jobs.splice(jobIndex + 1, 0, copy);
      break;
    }
    case "move-job-up":
      if (jobIndex > 0) [jobs[jobIndex - 1], jobs[jobIndex]] = [jobs[jobIndex], jobs[jobIndex - 1]];
      break;
    case "move-job-down":
      if (jobIndex < jobs.length - 1) [jobs[jobIndex + 1], jobs[jobIndex]] = [jobs[jobIndex], jobs[jobIndex + 1]];
      break;
    case "add-target":
      jobs[jobIndex].targets.push(newTarget());
      break;
    case "del-target":
      jobs[jobIndex].targets.splice(targetIndex, 1);
      break;
    case "move-target-up": {
      const targets = jobs[jobIndex].targets;
      if (targetIndex > 0)
        [targets[targetIndex - 1], targets[targetIndex]] = [targets[targetIndex], targets[targetIndex - 1]];
      break;
    }
    case "move-target-down": {
      const targets = jobs[jobIndex].targets;
      if (targetIndex < targets.length - 1)
        [targets[targetIndex + 1], targets[targetIndex]] = [targets[targetIndex], targets[targetIndex + 1]];
      break;
    }
    default:
      return;
  }

  markDirty();
  renderJobs();
}

function renderGlobalSettings() {
  const config = state.config;
  $("input-mode").value = config.mode || "sequence";
  $("input-rps").value = config.requests_per_second ?? 1;
  $("input-login-check").value = config.login_check_seconds ?? 180;
  $("input-round-retry").value = config.round_retry_seconds ?? 30;
  renderRateReadout(config.requests_per_second ?? 1);
}

function renderRateReadout(rps) {
  const readout = $("rate-readout");
  const warning = $("rate-warning");

  if (!rps || rps <= 0) {
    readout.textContent = "不限速";
    warning.hidden = false;
    return;
  }

  const intervalMS = Math.round(1000 / rps);
  readout.textContent = `每 ${intervalMS} ms 一个请求`;
  warning.hidden = rps <= 1;
}

/* ----------------------------------------------------------------- status */

function renderStatus(status) {
  state.status = status;

  const pill = $("phase-pill");
  pill.dataset.phase = status.phase;
  pill.textContent = PHASE_LABELS[status.phase] || status.phase;

  const parts = [];
  parts.push(status.logged_in ? `已登录 ${status.username || ""}`.trim() : "未登录");
  if (status.waiting_for_round) parts.push(`等待选课开放 · 第 ${status.round_attempts} 次检查`);
  else if (status.xkid) parts.push(`xkid ${status.xkid.slice(0, 8)}…`);
  if (status.requests_per_second > 0) parts.push(`${status.requests_per_second.toFixed(2)} rps`);
  else parts.push("不限速");
  $("session-meta").textContent = parts.join(" · ");

  const running = status.phase === "running" || status.phase === "stopping" || status.waiting_for_round;
  $("btn-start").disabled = running;
  $("btn-stop").disabled = !running;
  $("progress-hint").textContent = status.message || "";

  renderProgress(status);
}

function renderWaitingBanner(status) {
  if (!status.waiting_for_round) return "";

  const next = status.next_round_check_at
    ? new Date(status.next_round_check_at).toLocaleTimeString("zh-CN", { hour12: false })
    : "—";

  return `<div class="waiting-banner">
      <h3><span class="waiting-dot"></span>选课尚未开放，正在等待</h3>
      <span class="detail">已检查 ${status.round_attempts} 次 · 下次检查 ${next}</span>
    </div>`;
}

function renderProgress(status) {
  const container = $("progress-container");
  const jobs = status.jobs || [];
  const waiting = renderWaitingBanner(status);

  if (!jobs.length) {
    container.innerHTML = waiting || `<p class="empty">尚未开始运行。</p>`;
    return;
  }

  const enrolled = status.enrolled || [];
  const enrolledBlock = enrolled.length
    ? `<div class="enrolled">
         <h3>已选中 ${enrolled.length} 门</h3>
         <ul>${enrolled
           .map(
             (course) =>
               `<li>${escapeHTML(course.name)} — ${escapeHTML(course.teacher)} <span class="counters">${escapeHTML(
                 course.time
               )}</span></li>`
           )
           .join("")}</ul>
       </div>`
    : "";

  container.innerHTML =
    waiting +
    enrolledBlock +
    jobs
      .map(
        (job) => `
      <div class="progress-job">
        <div class="progress-job-head">
          <span class="state" data-state="${job.state}">${STATE_LABELS[job.state] || job.state}</span>
          <span class="name">${escapeHTML(job.name)}</span>
          <span class="round">第 ${job.round} 轮${job.max_rounds ? ` / ${job.max_rounds}` : ""}</span>
        </div>
        ${(job.targets || [])
          .map(
            (target) => `
          <div class="progress-target">
            <span class="state" data-state="${target.state}">${STATE_LABELS[target.state] || target.state}</span>
            <span class="label">${escapeHTML(target.name)}</span>
            <span class="counters">搜索 ${target.searches} · 尝试 ${target.attempts} · 命中 ${target.found} · 成功 ${
              target.successes
            }</span>
            <span class="message" title="${escapeHTML(target.message)}">${escapeHTML(target.message)}</span>
          </div>`
          )
          .join("")}
        ${job.message ? `<div class="progress-target"><span class="message">${escapeHTML(job.message)}</span></div>` : ""}
      </div>`
      )
      .join("");
}

/* -------------------------------------------------------------------- log */

function logMatchesFilter(level, text) {
  if (!state.levels.has(level)) return false;
  if (!state.search) return true;
  return text.toLowerCase().includes(state.search);
}

function appendLog(entry) {
  if (entry.seq <= state.lastSeq) return;
  state.lastSeq = entry.seq;

  const view = $("log-view");
  const atBottom = view.scrollHeight - view.scrollTop - view.clientHeight < 60;

  const scope = entry.job && entry.target ? `${entry.job}/${entry.target}` : entry.job || "";
  const line = document.createElement("div");
  line.className = "log-line";
  line.dataset.level = entry.level;
  line.dataset.text = `${scope} ${entry.message}`.toLowerCase();
  line.innerHTML =
    `<span class="time">${new Date(entry.time).toLocaleTimeString("zh-CN", { hour12: false })}</span>` +
    (scope ? `<span class="scope">[${escapeHTML(scope)}]</span>` : "") +
    `<span class="text">${escapeHTML(entry.message)}</span>`;
  line.hidden = !logMatchesFilter(entry.level, line.dataset.text);

  view.appendChild(line);
  while (view.childElementCount > MAX_LOG_NODES) view.removeChild(view.firstElementChild);

  if ($("log-autoscroll").checked && atBottom) view.scrollTop = view.scrollHeight;
}

function applyLogFilter() {
  for (const line of $("log-view").children) {
    line.hidden = !logMatchesFilter(line.dataset.level, line.dataset.text);
  }
}

/* ---------------------------------------------------------------- catalog */

function renderCatalogStats(stats) {
  state.catalog.stats = stats;
  const label = $("catalog-stats");

  if (!stats || !stats.total) {
    label.textContent = stats && stats.xkid ? `课程库为空 (轮次 ${stats.xkid.slice(0, 8)}…)` : "课程库为空";
    return;
  }

  const parts = [`共 ${stats.total} 门`, `计划内 ${stats.inplan}`, `公选 ${stats.public}`];
  if (stats.public_fetched_at) {
    parts.push(`公选全量 ${new Date(stats.public_fetched_at).toLocaleString("zh-CN", { hour12: false })}`);
  }
  // Categories are only known for courses fetched one category at a time, so
  // the stats say how much of the public list still has no category.
  const categories = stats.categories || [];
  if (categories.length) {
    parts.push(`已知类别 ${stats.public - stats.uncategorized}`);
    label.title = categories.map((category) => `${category.name} ${category.count}`).join("、");
  } else {
    label.title = "";
  }
  label.textContent = parts.join(" · ");

  $("catalog-fetch-hint").textContent = stats.keywords && stats.keywords.length
    ? `已搜索过的计划内关键词: ${stats.keywords.slice(0, 6).join("、")}${stats.keywords.length > 6 ? "…" : ""}`
    : "";
}

function catalogQuery() {
  // "none" is the panel's own option for courses no category-restricted
  // search has reached yet; the backend takes it as a separate flag.
  const category = $("catalog-category-filter").value;

  return {
    text: $("catalog-query").value.trim(),
    type: $("catalog-type-filter").value,
    only_available: $("catalog-only-available").checked,
    sort: $("catalog-sort").value,
    limit: CATALOG_ROW_LIMIT,
    public_category: category === "" || category === "none" ? null : Number.parseInt(category, 10),
    only_uncategorized: category === "none",
  };
}

// renderCategoryPickers fills the three category dropdowns from the list the
// backend owns: the panel's local filter, the fetch picker and, through
// renderJobs, the per-target picker.
function renderCategoryPickers() {
  const named = state.publicCategories.filter((category) => category.value !== PUBLIC_CATEGORY_ALL);

  $("catalog-category-filter").innerHTML = `<option value="">全部类别</option>
    ${named.map((category) => `<option value="${category.value}">${escapeHTML(category.name)}</option>`).join("")}
    <option value="none">未记录类别</option>`;

  $("catalog-fetch-category").innerHTML = categoryOptions(PUBLIC_CATEGORY_ALL, "全部课程 (不限类别)");
}

async function runLocalSearch() {
  // Local only: this never issues a request, so it stays responsive while a
  // run is polling and works even when logged out.
  state.catalog.results = await api().SearchCatalog(catalogQuery());
  renderCatalogResults();
}

function renderCatalogResults() {
  const container = $("catalog-results");
  const results = state.catalog.results;

  if (!results || !results.total) {
    container.innerHTML = `<p class="empty">课程库还是空的。用上面的「服务器搜索」获取公选课全部课程，计划内课程会在每次搜索时自动累积。</p>`;
    return;
  }
  if (!results.entries.length) {
    container.innerHTML = `<div class="catalog-summary">缓存 ${results.total} 门课程，没有匹配当前条件的结果</div>`;
    return;
  }

  const shown = results.entries.length;
  const summary = `<div class="catalog-summary">匹配 ${results.matched} 门${
    shown < results.matched ? `，显示前 ${shown} 门` : ""
  } · 缓存共 ${results.total} 门</div>`;

  const rows = results.entries
    .map((entry) => {
      const remaining = Number.parseInt(entry.remaining, 10);
      const seatClass = Number.isFinite(remaining) && remaining > 0 ? "seats-some" : "seats-none";
      return `<tr>
        <td class="name">${escapeHTML(entry.name)}</td>
        <td><span class="type-badge" data-type="${escapeHTML(entry.type)}">${
          entry.type === "public" ? "公选" : "计划内"
        }</span></td>
        <td>${escapeHTML(categoryName(entry.public_category)) || "—"}</td>
        <td>${escapeHTML(entry.teacher)}</td>
        <td>${escapeHTML(entry.time)}</td>
        <td>${escapeHTML(entry.location)}</td>
        <td>${escapeHTML(entry.campus)}</td>
        <td class="num">${escapeHTML(entry.credit)}</td>
        <td class="num">${escapeHTML(entry.enrolled)}</td>
        <td class="num ${seatClass}">${escapeHTML(entry.remaining)}</td>
        <td>${escapeHTML(entry.code)}</td>
      </tr>`;
    })
    .join("");

  container.innerHTML = `${summary}
    <table class="catalog">
      <thead><tr>
        <th>课程名</th><th>类型</th><th>类别</th><th>教师</th><th>上课时间</th><th>地点</th>
        <th>校区</th><th>学分</th><th>已选</th><th>剩余</th><th>课程号</th>
      </tr></thead>
      <tbody>${rows}</tbody>
    </table>`;
}

// refreshCatalogView re-reads the cache, which a run may have grown since the
// panel was last looked at.
async function refreshCatalogView() {
  renderCatalogStats(await api().CatalogStats());
  await runLocalSearch();
}

async function fetchCatalogFromServer() {
  const button = $("btn-catalog-fetch");
  const type = $("catalog-fetch-type").value;
  const keyword = $("catalog-fetch-keyword").value.trim();

  if (type === "inplan" && !keyword) {
    toast("计划内课程必须填写关键词：服务器不会返回完整的计划内课程列表", "error");
    return;
  }

  const category = Number.parseInt($("catalog-fetch-category").value, 10) || PUBLIC_CATEGORY_ALL;
  const eachCategory = type === "public" && $("catalog-fetch-each-category").checked;

  button.disabled = true;
  button.textContent = "获取中…";

  try {
    const result = await api().RefreshCatalog({
      type,
      keyword,
      public_category: type === "public" && category !== PUBLIC_CATEGORY_ALL ? category : null,
      each_category: eachCategory,
      include_filtered: $("catalog-include-filtered").checked,
      all: $("catalog-fetch-all").checked,
    });
    toast(result.message || "课程库已更新", "success");
  } catch (err) {
    toast(errorText(err), "error");
  } finally {
    button.disabled = false;
    button.textContent = "获取";
    await refreshCatalogView();
  }
}

/* ---------------------------------------------------------------- actions */

async function loadConfig() {
  state.config = await api().GetConfig();
  renderGlobalSettings();
  renderJobs();
  markDirty(false);
}

async function saveConfig() {
  try {
    state.config = await api().SaveConfig(state.config);
    renderGlobalSettings();
    renderJobs();
    markDirty(false);
    toast("配置已保存", "success");
    return true;
  } catch (err) {
    toast(`保存失败: ${errorText(err)}`, "error");
    return false;
  }
}

async function doLogin() {
  const button = $("btn-login");
  button.disabled = true;
  button.textContent = "登录中…";

  try {
    const result = await api().Login({
      username: $("input-username").value.trim(),
      password: $("input-password").value,
      remember: $("input-remember").checked,
    });
    // Logging in while enrollment is closed is a normal outcome: the run
    // waits for the round to open instead of failing.
    if (result && result.round_open) {
      toast("登录成功，已进入选课界面", "success");
    } else {
      toast(result?.message || "登录成功，但选课尚未开放；点击开始后会自动等待", "info");
    }
    $("input-password").value = "";
  } catch (err) {
    toast(errorText(err), "error");
  } finally {
    button.disabled = false;
    button.textContent = "登录并进入选课";
    renderStatus(await api().GetStatus());
    await refreshAccountHint();
  }
}

async function refreshAccountHint() {
  const info = await api().GetCredentials();
  $("account-hint").textContent = info.saved ? `已保存 ${info.username}` : "未保存账号";
  if (info.username && !$("input-username").value) {
    $("input-username").value = info.username;
    $("input-remember").checked = info.saved;
  }
}

async function startRun() {
  if (state.dirty && !(await saveConfig())) return;

  try {
    await api().Start();
    toast("已开始运行", "success");
  } catch (err) {
    toast(errorText(err), "error");
  } finally {
    renderStatus(await api().GetStatus());
  }
}

/* --------------------------------------------------------------- bindings */

function bindEvents() {
  $("btn-login").addEventListener("click", doLogin);
  $("btn-forget").addEventListener("click", async () => {
    try {
      await api().ForgetCredentials();
      await refreshAccountHint();
      toast("已删除本地账号");
    } catch (err) {
      toast(errorText(err), "error");
    }
  });

  $("btn-start").addEventListener("click", startRun);
  $("btn-stop").addEventListener("click", async () => {
    await api().Stop();
    renderStatus(await api().GetStatus());
  });

  $("btn-save").addEventListener("click", saveConfig);
  $("btn-reload").addEventListener("click", async () => {
    await loadConfig();
    toast("已重新载入配置");
  });
  $("btn-reset").addEventListener("click", async () => {
    state.config = await api().ResetConfig();
    renderGlobalSettings();
    renderJobs();
    markDirty(false);
    toast("已恢复示例配置");
  });
  $("btn-add-job").addEventListener("click", () => handleStructuralAction("add-job"));

  $("input-mode").addEventListener("change", (event) => {
    state.config.mode = event.target.value;
    markDirty();
  });
  $("input-login-check").addEventListener("input", (event) => {
    state.config.login_check_seconds = Math.max(0, toNumber(event.target.value, 0));
    markDirty();
  });
  $("input-round-retry").addEventListener("input", (event) => {
    state.config.round_retry_seconds = Math.max(1, toNumber(event.target.value, 30));
    markDirty();
  });
  $("input-rps").addEventListener("change", async (event) => {
    const rps = Math.max(0, toNumber(event.target.value, 1));
    state.config.requests_per_second = rps;
    markDirty();
    renderRateReadout(rps);
    await api().SetRateLimit(rps);
  });
  document.querySelectorAll(".rate-presets .chip").forEach((chip) => {
    chip.addEventListener("click", async () => {
      const rps = toNumber(chip.dataset.rps, 1);
      state.config.requests_per_second = rps;
      $("input-rps").value = rps;
      markDirty();
      renderRateReadout(rps);
      await api().SetRateLimit(rps);
    });
  });

  const container = $("jobs-container");
  container.addEventListener("input", (event) => {
    const element = event.target;
    if (element.dataset.field && element.type !== "checkbox") applyFieldEdit(element);
  });
  container.addEventListener("change", (event) => {
    const element = event.target;
    if (element.dataset.field && (element.type === "checkbox" || element.tagName === "SELECT")) {
      applyFieldEdit(element);
    }
  });
  container.addEventListener("click", (event) => {
    const button = event.target.closest("[data-action]");
    if (!button || button.disabled) return;
    handleStructuralAction(
      button.dataset.action,
      Number.parseInt(button.dataset.job, 10),
      button.dataset.target === undefined ? null : Number.parseInt(button.dataset.target, 10)
    );
  });

  $("log-levels").addEventListener("click", (event) => {
    const chip = event.target.closest(".chip");
    if (!chip) return;
    const level = chip.dataset.level;
    if (state.levels.has(level)) {
      state.levels.delete(level);
      chip.classList.remove("active");
    } else {
      state.levels.add(level);
      chip.classList.add("active");
    }
    applyLogFilter();
  });
  $("log-search").addEventListener("input", (event) => {
    state.search = event.target.value.trim().toLowerCase();
    applyLogFilter();
  });
  $("btn-clear-log").addEventListener("click", async () => {
    await api().ClearLogs();
    $("log-view").innerHTML = "";
  });
  $("btn-export-log").addEventListener("click", async () => {
    try {
      const path = await api().ExportLogs();
      if (path) toast(`已导出到 ${path}`, "success");
    } catch (err) {
      toast(errorText(err), "error");
    }
  });

  $("workspace-tabs").addEventListener("click", (event) => {
    const tab = event.target.closest(".tab");
    if (!tab) return;

    for (const button of document.querySelectorAll("#workspace-tabs .tab")) {
      button.classList.toggle("active", button === tab);
    }
    for (const panel of document.querySelectorAll(".tab-panel")) {
      panel.hidden = panel.dataset.panel !== tab.dataset.tab;
    }
    for (const tools of document.querySelectorAll("[data-tools]")) {
      tools.hidden = tools.dataset.tools !== tab.dataset.tab;
    }
    if (tab.dataset.tab === "catalog") refreshCatalogView();
  });

  let localSearchTimer = null;
  const scheduleLocalSearch = () => {
    clearTimeout(localSearchTimer);
    localSearchTimer = setTimeout(runLocalSearch, 120);
  };
  $("catalog-query").addEventListener("input", scheduleLocalSearch);
  $("catalog-type-filter").addEventListener("change", runLocalSearch);
  $("catalog-category-filter").addEventListener("change", runLocalSearch);
  $("catalog-sort").addEventListener("change", runLocalSearch);
  $("catalog-only-available").addEventListener("change", runLocalSearch);

  $("btn-catalog-fetch").addEventListener("click", fetchCatalogFromServer);
  $("catalog-fetch-each-category").addEventListener("change", (event) => {
    // Walking every category and picking one are the same control, used two
    // ways: the picker is meaningless once every category gets its own pass.
    $("catalog-fetch-category").disabled = event.target.checked;
  });
  $("catalog-fetch-keyword").addEventListener("keydown", (event) => {
    if (event.key === "Enter") fetchCatalogFromServer();
  });
  $("catalog-fetch-type").addEventListener("change", (event) => {
    // The whole-list fetch, and the categories, only exist for public electives.
    const isInPlan = event.target.value === "inplan";
    $("catalog-fetch-keyword").placeholder = isInPlan
      ? "关键词（计划内课程必填）"
      : "关键词（公选课可留空以获取全部）";
    $("catalog-fetch-category").hidden = isInPlan;
    $("catalog-each-category-field").hidden = isInPlan;
  });

  $("btn-clear-catalog").addEventListener("click", async () => {
    try {
      await api().ClearCatalog();
      await refreshCatalogView();
      toast("已清空课程库缓存");
    } catch (err) {
      toast(errorText(err), "error");
    }
  });

  window.addEventListener("keydown", (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key === "s") {
      event.preventDefault();
      saveConfig();
    }
  });
}

/* -------------------------------------------------------------- ui scale */

/* The stylesheet is written in px, so the whole UI is scaled with CSS zoom on
   the root element (see --ui-scale in app.css). Zoom reflows the layout rather
   than stretching a picture of it, so a larger scale still uses the whole
   window. The scale is a per-machine display preference, not part of the job
   configuration, so it lives in localStorage instead of enroll_config.json. */

const SCALE_KEY = "robyou.ui-scale";
// Stepping through presets keeps repeated clicks on round numbers.
const SCALE_STEPS = [0.75, 0.85, 1, 1.15, 1.3, 1.5, 1.75, 2];
const DEFAULT_SCALE = 1;
// The two columns stop fitting below roughly this much CSS width.
const NARROW_WIDTH = 900;

function clampScale(value) {
  const min = SCALE_STEPS[0];
  const max = SCALE_STEPS[SCALE_STEPS.length - 1];
  return Math.min(max, Math.max(min, value));
}

function storedScale() {
  try {
    const value = Number(window.localStorage.getItem(SCALE_KEY));
    if (Number.isFinite(value) && value > 0) return clampScale(value);
  } catch (err) {
    // Storage can be unavailable; the default scale is a fine fallback.
  }
  return DEFAULT_SCALE;
}

function applyScale(scale, persist = true) {
  state.scale = clampScale(scale);
  document.documentElement.style.setProperty("--ui-scale", String(state.scale));

  if (persist) {
    try {
      window.localStorage.setItem(SCALE_KEY, String(state.scale));
    } catch (err) {
      // Not remembering it is harmless — the scale still applies to this run.
    }
  }

  const readout = $("btn-scale-reset");
  if (readout) readout.textContent = `${Math.round(state.scale * 100)}%`;
  updateNarrowLayout();
}

function stepScale(direction) {
  const steps = direction > 0 ? SCALE_STEPS : [...SCALE_STEPS].reverse();
  const next = steps.find((step) =>
    direction > 0 ? step > state.scale + 0.001 : step < state.scale - 0.001
  );
  if (next !== undefined) applyScale(next);
}

// Zoom divides the usable CSS width, so the layout has to be told when the
// columns no longer fit. A media query would keep measuring the window, which
// has not changed, so the check is done here instead.
function updateNarrowLayout() {
  const narrow = window.innerWidth / state.scale < NARROW_WIDTH;
  document.documentElement.toggleAttribute("data-narrow", narrow);
}

// initScale runs before the backend is up, so the window never flashes at the
// wrong size while the Go side is still starting.
function initScale() {
  applyScale(storedScale(), false);

  $("btn-scale-down").addEventListener("click", () => stepScale(-1));
  $("btn-scale-up").addEventListener("click", () => stepScale(1));
  $("btn-scale-reset").addEventListener("click", () => applyScale(DEFAULT_SCALE));

  window.addEventListener("resize", updateNarrowLayout);

  window.addEventListener("keydown", (event) => {
    if (!event.ctrlKey && !event.metaKey) return;
    if (event.key === "=" || event.key === "+") stepScale(1);
    else if (event.key === "-" || event.key === "_") stepScale(-1);
    else if (event.key === "0") applyScale(DEFAULT_SCALE);
    else return;
    event.preventDefault();
  });

  // Ctrl + wheel is the habit everywhere else; claiming the event also keeps
  // the webview from applying its own page zoom on top of ours.
  window.addEventListener(
    "wheel",
    (event) => {
      if (!event.ctrlKey && !event.metaKey) return;
      event.preventDefault();
      stepScale(event.deltaY < 0 ? 1 : -1);
    },
    { passive: false }
  );
}

/* ------------------------------------------------------------------- boot */

async function main() {
  await waitForBackend();

  state.paths = await api().ConfigPaths();
  $("config-path").textContent = state.paths.jobs;

  // The category list has to be in hand before anything renders a picker.
  state.publicCategories = (await api().PublicCategories()) || [];
  renderCategoryPickers();

  await loadConfig();
  await refreshAccountHint();
  bindEvents();

  for (const entry of await api().GetLogs(0)) appendLog(entry);
  renderStatus(await api().GetStatus());
  renderCatalogStats(await api().CatalogStats());
  await runLocalSearch();

  window.runtime.EventsOn("robyou:log", appendLog);
  window.runtime.EventsOn("robyou:status", renderStatus);
  window.runtime.EventsOn("robyou:catalog", async (stats) => {
    renderCatalogStats(stats);
    await runLocalSearch();
  });
}

initScale();
document.addEventListener("DOMContentLoaded", main);
