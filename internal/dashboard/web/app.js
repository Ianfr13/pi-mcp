"use strict";
let STATE = { jobs: [], counts: {}, stateDir: "" };
let SELECTED = null;
let DETAIL = null;          // cached detail of SELECTED
let HIST = "24h";

const $ = (s) => document.querySelector(s);
const TERMINAL = new Set(["completed", "failed", "aborted"]);
const esc = (s) => String(s == null ? "" : s).replace(/[&<>"']/g, (c) =>
  ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const shortId = (id) => (id || "").replace(/^job-/, "").slice(0, 8);

function fmtTokens(n) {
  n = n || 0;
  if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 0 : 2).replace(/\.0+$/, "") + "M";
  if (n >= 1e3) return Math.round(n / 1e3) + "k";
  return String(n);
}
function fmtCost(c) { return c == null ? "" : "$" + (c < 1 ? c.toFixed(3) : c.toFixed(2)); }
function fmtDur(ms) {
  if (ms == null) return "";
  const s = Math.round(ms / 1000);
  if (s < 60) return s + "s";
  const m = Math.floor(s / 60); return m + "m" + String(s % 60).padStart(2, "0") + "s";
}
function secsSince(iso) { return Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000)); }
function elapsed(iso) {
  const s = secsSince(iso), m = Math.floor(s / 60);
  return (m >= 60 ? Math.floor(m / 60) + "h" + String(m % 60).padStart(2, "0") : m + ":" + String(s % 60).padStart(2, "0"));
}
function rel(iso) {
  if (!iso) return "";
  const s = secsSince(iso);
  if (s < 45) return "just now";
  if (s < 3600) return Math.round(s / 60) + "m ago";
  if (s < 86400) return Math.round(s / 3600) + "h ago";
  return Math.round(s / 86400) + "d ago";
}
function within(iso, w) {
  if (w === "all") return true;
  return (Date.now() - new Date(iso).getTime()) <= (w === "7d" ? 7 * 864e5 : 864e5);
}
function provClass(model) {
  const m = (model || "").toLowerCase();
  for (const p of ["deepseek", "openai", "minimax", "anthropic"]) if (m.includes(p)) return "prov-" + p;
  return "";
}
const modelShort = (m) => (m || "?").split("/").pop();

/* ---------- top bar ---------- */
function renderStats() {
  const c = STATE.counts || {};
  $("#stats").innerHTML = [
    ["s-run", "live", c.running || 0],
    ["s-queue", "queued", c.queued || 0],
    ["s-done", "done", c.completed || 0],
    ["s-fail", "failed", c.failed || 0],
  ].map(([cls, lbl, n]) => `<span class="stat ${cls}"><span class="dot"></span><span class="lbl">${lbl}</span><b>${n}</b></span>`).join("");
  $("#statedir").textContent = STATE.stateDir || "";
}

/* ---------- rail cards ---------- */
function jobCard(j) {
  const live = j.status === "running" || j.status === "queued";
  const title = j.workflowName ? esc(j.workflowName) : `<span class="sub">${shortId(j.jobId)}</span>`;
  let body = "";
  if (j.status === "running") {
    if (j.blindWindow) {
      body = `<div class="r2"><span class="chip">✍ authoring…</span></div>
              <div class="meta"><span data-elapsed="${j.startedAt}">${elapsed(j.startedAt)}</span></div>`;
    } else {
      const pct = j.agentsTotal ? Math.round(100 * j.agentsDone / j.agentsTotal) : 8;
      body = `<div class="r2"><span class="chip mode">${esc(j.mode)}</span>${j.phase ? `<span class="chip">${esc(j.phase)}</span>` : ""}</div>
        <div class="meta"><div class="bar"><i style="width:${pct}%"></i></div></div>
        <div class="meta"><b>${j.agentsDone}/${j.agentsTotal || "?"}</b> agents · Σ <b>${fmtTokens(j.liveTokens)}</b> tok · <span data-elapsed="${j.startedAt}">${elapsed(j.startedAt)}</span></div>`;
    }
  } else if (j.status === "queued") {
    body = `<div class="r2"><span class="chip mode">${esc(j.mode)}</span><span class="chip">queued</span></div>`;
  } else {
    const chips = Object.entries(j.fleetByModel || {}).slice(0, 3)
      .map(([m, n]) => `<span class="chip">${esc(modelShort(m))}·${n}</span>`).join("");
    const err = j.status === "failed" && j.errorCode ? `<span class="chip err">${esc(j.errorCode)}</span>` : "";
    const bits = [];
    if (j.liveTokens) bits.push(`<b>${fmtTokens(j.liveTokens)}</b> tok`);
    if (j.cost != null) bits.push(`<b>${fmtCost(j.cost)}</b>`);
    if (j.durationMs != null) bits.push(fmtDur(j.durationMs));
    bits.push(rel(j.completedAt || j.startedAt));
    body = `<div class="r2"><span class="chip mode">${esc(j.mode)}</span>${chips}${err}</div>
            <div class="meta">${bits.join(" · ")}</div>`;
  }
  const selCls = j.jobId === SELECTED ? " sel" : "";
  return `<div class="card s-${j.status}${selCls}" data-id="${esc(j.jobId)}">
    <div class="r1"><span class="name">${title}</span><span class="pill ${j.status}">${j.status}</span></div>
    ${body}</div>`;
}

function renderRail() {
  const jobs = STATE.jobs || [];
  const live = jobs.filter((j) => j.status === "running" || j.status === "queued");
  const hist = jobs.filter((j) => TERMINAL.has(j.status) && within(j.completedAt || j.startedAt, HIST));
  $("#livecount").textContent = live.length;
  $("#live").innerHTML = live.length ? live.map(jobCard).join("") : `<div class="empty">No active jobs.</div>`;
  $("#history").innerHTML = hist.length ? hist.map(jobCard).join("") : `<div class="empty">No jobs in ${HIST}.</div>`;
  for (const el of document.querySelectorAll(".card")) {
    el.onclick = () => select(el.dataset.id);
  }
}

/* ---------- selection ---------- */
function select(id) {
  SELECTED = id; DETAIL = null;
  renderRail();
  loadDetail(id);
}
async function loadDetail(id) {
  try {
    const r = await fetch("/api/job/" + encodeURIComponent(id));
    if (!r.ok) { DETAIL = { _missing: true }; renderPanel(); return; }
    DETAIL = await r.json(); renderPanel();
  } catch (e) { /* keep */ }
}

/* ---------- panel ---------- */
function renderPanel() {
  if (!SELECTED) return renderOverview();
  if (!DETAIL) { $("#panel").innerHTML = `<div class="hint">loading…</div>`; return; }
  renderDetail(DETAIL);
}

function renderOverview() {
  const c = STATE.counts || {}, jobs = STATE.jobs || [];
  const cards = [
    ["s-run", "live", c.running || 0], ["s-queue", "queued", c.queued || 0],
    ["s-done", "completed", c.completed || 0], ["s-fail", "failed", c.failed || 0],
    ["s-tot", "total", c.total || 0],
  ].map(([cls, k, v]) => `<div class="ov ${cls}"><div class="k"><span class="dot"></span>${k}</div><div class="v">${v}</div></div>`).join("");

  // aggregate fleet + totals
  const fleet = {}; let totCost = 0, totTok = 0;
  for (const j of jobs) {
    for (const [m, n] of Object.entries(j.fleetByModel || {})) fleet[m] = (fleet[m] || 0) + n;
    if (j.cost) totCost += j.cost;
    totTok += j.liveTokens || 0;
  }
  const entries = Object.entries(fleet).sort((a, b) => b[1] - a[1]);
  const max = entries.length ? entries[0][1] : 1;
  const bars = entries.length ? entries.map(([m, n]) =>
    `<div class="fb"><span class="m">${esc(m)}</span><span class="track"><i style="width:${Math.max(4, 100 * n / max)}%"></i></span><span class="n">${n}</span></div>`).join("")
    : `<div class="hint">No model usage recorded yet.</div>`;

  $("#panel").innerHTML = `
    <h1 class="ph1">Overview</h1>
    <div class="pmeta"><span>state dir <b>${esc(STATE.stateDir)}</b></span></div>
    <div class="ov-grid">${cards}</div>
    <div class="sec-title">Fleet — agents by model (all jobs)</div>
    <div class="fleetbars">${bars}</div>
    <div class="sec-title">Totals</div>
    <div class="strip">
      <div class="kpi"><div class="k">agent tokens</div><div class="v">${fmtTokens(totTok)}</div></div>
      <div class="kpi"><div class="k">cost</div><div class="v">${totCost ? fmtCost(totCost) : "—"}</div></div>
      <div class="kpi"><div class="k">workflows</div><div class="v">${jobs.length}</div></div>
    </div>
    <p class="hint" style="margin-top:22px">Select a job on the left to drill into its agent fleet.</p>`;
}

function timeline(d) {
  if (!d.phases || !d.phases.length) return "";
  const cur = d.phases.indexOf(d.phase);
  const steps = d.phases.map((p, i) => {
    const cls = i < cur || d.status === "completed" ? "done" : (p === d.phase ? "cur" : "future");
    const sep = i < d.phases.length - 1 ? `<span class="tsep ${i < cur ? "done" : ""}"></span>` : "";
    return `<span class="tstep ${cls}"><span class="node"></span><span class="lbl">${esc(p)}</span></span>${sep}`;
  }).join("");
  return `<div class="timeline">${steps}</div>`;
}

function agentCard(a) {
  const stcls = a.status === "done" ? "done" : a.status === "error" ? "error" : "running";
  const ic = a.status === "done" ? "✓" : a.status === "error" ? "✕" : "•";
  const prompt = a.prompt ? `<details><summary>prompt</summary><pre>${esc(a.prompt)}</pre></details>` : "";
  const res = a.resultPreview ? `<details><summary>result</summary><pre>${esc(a.resultPreview)}</pre></details>` : "";
  const err = a.error ? `<div class="errbox"><pre>${esc(a.error)}</pre></div>` : "";
  return `<div class="agent">
    <div class="ar1"><span class="mbadge ${provClass(a.model)}">${esc(modelShort(a.model))}</span>
      <span class="st ${stcls}"><span class="ic">${ic}</span>${esc(a.status || "")}</span></div>
    <div class="alabel">${esc(a.label || "agent")}</div>
    <div class="ameta">${a.phase ? `<span>${esc(a.phase)}</span>` : ""}<span>${fmtTokens(a.tokens)} tok</span>${a.startedAt && a.endedAt ? `<span>${fmtDur(new Date(a.endedAt) - new Date(a.startedAt))}</span>` : ""}</div>
    ${err}${prompt}${res}</div>`;
}

function renderResult(res) {
  if (res == null) return "";
  const raw = `<details class="raw"><summary>raw JSON</summary><pre>${esc(JSON.stringify(res, null, 2))}</pre></details>`;
  if (typeof res !== "object" || Array.isArray(res)) {
    return `<div class="sec-title">Result</div><div class="result"><pre style="margin:0;white-space:pre-wrap">${esc(JSON.stringify(res, null, 2))}</pre></div>`;
  }
  let h = "";
  if (res.summary) h += `<p class="summary">${esc(res.summary)}</p>`;
  if (Array.isArray(res.findings)) h += res.findings.map((f) => {
    const sev = (f.severity || "").toLowerCase();
    return `<div class="finding ${sev}"><div class="ft">${esc(f.title || "")}${f.severity ? `<span class="sev ${sev}">${esc(f.severity)}</span>` : ""}</div>${f.detail ? `<div class="fd">${esc(f.detail)}</div>` : ""}</div>`;
  }).join("");
  const kv = [];
  if (Array.isArray(res.files_changed) && res.files_changed.length) kv.push(`files: ${res.files_changed.map((f) => `<code>${esc(f)}</code>`).join(" ")}`);
  if (res.diff_summary) kv.push(`diff: ${esc(res.diff_summary)}`);
  if (res.tests_run) kv.push(`tests: ${esc(res.tests_run)}`);
  if (res.confidence) kv.push(`confidence: <b>${esc(res.confidence)}</b>`);
  if (res.notes) kv.push(esc(res.notes));
  if (Array.isArray(res.open_questions) && res.open_questions.length)
    kv.push("open questions:<br>" + res.open_questions.map((q) => "• " + esc(q)).join("<br>"));
  const kvh = kv.length ? `<div class="kvlist">${kv.map((x) => `<div>${x}</div>`).join("")}</div>` : "";
  if (!h && !kvh) h = `<pre style="margin:0;white-space:pre-wrap">${esc(JSON.stringify(res, null, 2))}</pre>`;
  return `<div class="sec-title">Result</div><div class="result">${h}${kvh}${raw}</div>`;
}

function renderDetail(d) {
  const where = d.mode === "write"
    ? `branch <b>${esc(d.branch || "")}</b>` + (d.worktreePath ? ` · <b>${esc(d.worktreePath)}</b>` : "")
    : `cwd <b>${esc(d.cwd || "")}</b>`;
  const timing = d.status === "running"
    ? `elapsed <b><span data-elapsed="${d.startedAt}">${elapsed(d.startedAt)}</span></b>`
    : (d.durationMs != null ? `took <b>${fmtDur(d.durationMs)}</b>` : "") + (d.completedAt ? ` · ${rel(d.completedAt)}` : "");
  let h = `<h1 class="ph1">${esc(d.workflowName || shortId(d.jobId))} <span class="pill ${d.status}" style="font-size:12px;vertical-align:3px">${d.status}</span></h1>
    <div class="pmeta"><span>${esc(d.mode)}</span><span>${where}</span>${d.runId ? `<span>run <b>${esc(d.runId)}</b></span>` : ""}<span>${timing}</span></div>`;

  if (d.error) h += `<div class="errbox"><span class="code">${esc(d.errorCode || "error")}</span><pre>${esc(d.error)}</pre></div>`;

  const noRun = (!d.agents || d.agents.length === 0) && !d.tokenUsage && !d.blindWindow;
  if (d.blindWindow) {
    const a = d.authoring || {};
    const model = a.model || "orchestrator";
    const plan = a.preview
      ? `<pre class="authpre">${esc(a.preview)}</pre>`
      : `<div class="authwait"><span class="spin"></span> waiting for the plan… (the author returns it in one shot near the end)</div>`;
    h += `<div class="authoring">
      <div class="authhead">✍ writing the workflow plan…
        <span class="authmeta">· <span data-elapsed="${d.startedAt}">${elapsed(d.startedAt)}</span> · ${esc(model)}</span></div>
      ${plan}</div>`;
    $("#panel").innerHTML = h; return;
  }
  if (noRun) {
    h += `<div class="notice"><div class="big">📄</div><b>No run data for this job.</b><br>The run file isn't available — its worktree may have been pruned, or the job produced no fleet.<br><span class="dim">Registry shows: ${esc(d.mode)} · ${esc(d.status)}${d.branch ? " · " + esc(d.branch) : ""}</span></div>`;
    $("#panel").innerHTML = h; return;
  }

  // stat strip
  const tu = d.tokenUsage;
  const strip = [
    ["agents", `${d.agentsDone}/${d.agentsTotal}`],
    ["tokens", fmtTokens(d.liveTokens || (tu && tu.total) || 0)],
    d.cost != null ? ["cost", fmtCost(d.cost)] : null,
    d.durationMs != null ? ["duration", fmtDur(d.durationMs)] : null,
    d.phase ? ["phase", esc(d.phase)] : null,
  ].filter(Boolean).map(([k, v]) => `<div class="kpi"><div class="k">${k}</div><div class="v">${v}</div></div>`).join("");
  h += `<div class="strip">${strip}</div>`;

  h += timeline(d);

  if (d.agents && d.agents.length) {
    h += `<div class="sec-title">Fleet · ${d.agents.length} agents</div><div class="fleet">${d.agents.map(agentCard).join("")}</div>`;
  }

  if (tu) {
    h += `<div class="sec-title">Tokens</div><div class="strip">
      <div class="kpi"><div class="k">input</div><div class="v">${fmtTokens(tu.input)}</div></div>
      <div class="kpi"><div class="k">output</div><div class="v">${fmtTokens(tu.output)}</div></div>
      <div class="kpi"><div class="k">cache read</div><div class="v">${fmtTokens(tu.cacheRead)}</div></div>
      <div class="kpi"><div class="k">total</div><div class="v">${fmtTokens(tu.total)}</div></div>
      ${tu.cost != null ? `<div class="kpi"><div class="k">cost</div><div class="v">${fmtCost(tu.cost)}</div></div>` : ""}
    </div>`;
  }

  h += renderResult(d.result);
  $("#panel").innerHTML = h;
}

/* ---------- live data ---------- */
function applyState(st) {
  STATE = st;
  renderStats(); renderRail();
  if (!SELECTED) { renderPanel(); return; }
  const j = STATE.jobs.find((x) => x.jobId === SELECTED);
  if (j && !TERMINAL.has(j.status)) loadDetail(SELECTED);   // live job → refresh detail
  else if (!DETAIL) renderPanel();
}

function setConn(ok) {
  const c = $("#conn");
  c.classList.toggle("down", !ok);
  $("#conntxt").textContent = ok ? "live" : "reconnecting…";
}

function connect() {
  fetch("/api/state").then((r) => r.json()).then(applyState).catch(() => {});
  const es = new EventSource("/events");
  es.onmessage = (e) => { try { applyState(JSON.parse(e.data)); setConn(true); } catch (_) {} };
  es.onopen = () => setConn(true);
  es.onerror = () => setConn(false);
}

document.addEventListener("click", (e) => {
  const b = e.target.closest("#filter button");
  if (!b) return;
  HIST = b.dataset.w;
  for (const x of document.querySelectorAll("#filter button")) x.classList.toggle("on", x === b);
  renderRail();
});

setInterval(() => {
  for (const el of document.querySelectorAll("[data-elapsed]")) el.textContent = elapsed(el.dataset.elapsed);
}, 1000);

connect();
