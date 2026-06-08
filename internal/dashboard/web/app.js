"use strict";
let STATE = { jobs: [], counts: {}, stateDir: "" };
let SELECTED = null;       // jobId
let HIST_WINDOW = "24h";

const $ = (s) => document.querySelector(s);
const TERMINAL = new Set(["completed", "failed", "aborted"]);
const shortId = (id) => (id || "").replace(/^job-/, "").slice(0, 8);

function fmtTokens(n) { return n >= 1000 ? (n / 1000).toFixed(0) + "k" : String(n); }
function fmtDur(ms) {
  if (ms == null) return "";
  const s = Math.floor(ms / 1000);
  return (s >= 60 ? Math.floor(s / 60) + "m" + (s % 60) + "s" : s + "s");
}
function elapsed(startedAt) {
  const s = Math.max(0, Math.floor((Date.now() - new Date(startedAt).getTime()) / 1000));
  const m = Math.floor(s / 60), sec = s % 60;
  return (m > 0 ? m + ":" + String(sec).padStart(2, "0") : "0:" + String(sec).padStart(2, "0"));
}
function within(startedAt, win) {
  if (win === "all") return true;
  const ageMs = Date.now() - new Date(startedAt).getTime();
  return ageMs <= (win === "7d" ? 7 * 864e5 : 864e5);
}

function jobCard(j, live) {
  const sel = j.jobId === SELECTED ? " sel" : "";
  const title = j.workflowName || shortId(j.jobId);
  let body = "";
  if (live && j.status === "running") {
    const frac = j.agentsTotal ? Math.round(100 * j.agentsDone / j.agentsTotal) : 0;
    const meta = j.blindWindow ? "authoring…" :
      `${j.phase || ""} · Σ ${fmtTokens(j.liveTokens)} tok · ${j.agentsDone}/${j.agentsTotal}`;
    body = `<div class="bar"><i style="width:${frac}%"></i></div>
            <div class="sub" data-elapsed="${j.startedAt}">${meta} · ${elapsed(j.startedAt)}</div>`;
  } else if (j.status === "queued") {
    body = `<div class="sub">queued</div>`;
  } else {
    const chips = Object.entries(j.fleetByModel || {}).map(([m, c]) =>
      `<span class="chip">${m.split("/").pop()}×${c}</span>`).join("");
    const cost = j.cost != null ? "$" + j.cost.toFixed(2) : "";
    body = `<div class="sub">${chips} ${fmtTokens(j.liveTokens)} tok ${cost} ${fmtDur(j.durationMs)}</div>`;
  }
  return `<div class="card${sel}" data-id="${j.jobId}">
    <div class="row"><span class="jid">${title}</span>
      <span class="pill ${j.status}">${j.status}</span></div>${body}</div>`;
}

function render() {
  const c = STATE.counts || {};
  $("#counts").innerHTML =
    `<span>LIVE <b>${c.running || 0}</b></span><span>QUEUED <b>${c.queued || 0}</b></span>` +
    `<span>DONE <b>${c.completed || 0}</b></span><span>FAIL <b>${c.failed || 0}</b></span>`;

  const live = STATE.jobs.filter((j) => j.status === "running" || j.status === "queued");
  const hist = STATE.jobs.filter((j) => TERMINAL.has(j.status) && within(j.startedAt, HIST_WINDOW));
  $("#live").innerHTML = live.length ? live.map((j) => jobCard(j, true)).join("") : `<div class="muted">no active jobs</div>`;
  $("#history").innerHTML = hist.length ? hist.map((j) => jobCard(j, false)).join("") : `<div class="muted">none in ${HIST_WINDOW}</div>`;

  for (const el of document.querySelectorAll(".card")) {
    el.onclick = () => { SELECTED = el.dataset.id; loadDetail(SELECTED); render(); };
  }
  if (!SELECTED) renderOverview();
}

function renderOverview() {
  const c = STATE.counts || {};
  $("#panel").innerHTML = `<h1>Overview</h1>
    <div class="muted">${STATE.stateDir || ""}</div>
    <div class="overview">
      <div class="ov"><span class="muted">live</span><b>${c.running || 0}</b></div>
      <div class="ov"><span class="muted">queued</span><b>${c.queued || 0}</b></div>
      <div class="ov"><span class="muted">done</span><b>${c.completed || 0}</b></div>
      <div class="ov"><span class="muted">failed</span><b>${c.failed || 0}</b></div>
      <div class="ov"><span class="muted">aborted</span><b>${c.aborted || 0}</b></div>
    </div>
    <p class="muted">Select a job to drill into its fleet.</p>`;
}

async function loadDetail(id) {
  try {
    const r = await fetch("/api/job/" + encodeURIComponent(id));
    if (!r.ok) { $("#panel").innerHTML = `<div class="muted">job not found</div>`; return; }
    renderDetail(await r.json());
  } catch (e) { /* keep last panel */ }
}

function agentCard(a) {
  const icon = a.status === "done" ? "✓" : a.status === "error" ? "✗" : "⏳";
  const prompt = (a.prompt || "").slice(0, 160);
  return `<div class="agent">
    <div class="row"><span class="m">${a.model || "?"}</span><span class="st">${icon} ${a.status}</span></div>
    <div class="kv">${a.label || ""} · ${a.phase || ""} · ${fmtTokens(a.tokens || 0)} tok</div>
    <details><summary>prompt / result</summary>
      <pre>${escapeHTML(prompt)}</pre>
      <pre>${escapeHTML(a.resultPreview || "")}</pre></details>
  </div>`;
}

function renderResult(res) {
  if (res == null) return "";
  if (typeof res !== "object" || Array.isArray(res)) {
    return `<h3>Result (fora do contrato)</h3><pre>${escapeHTML(JSON.stringify(res, null, 2))}</pre>`;
  }
  let h = `<h3>Result</h3>`;
  if (res.summary) h += `<p>${escapeHTML(res.summary)}</p>`;
  if (Array.isArray(res.findings)) {
    h += res.findings.map((f) =>
      `<div class="finding ${f.severity || ""}"><b>${escapeHTML(f.title || "")}</b><br>${escapeHTML(f.detail || "")}</div>`).join("");
  }
  if (Array.isArray(res.files_changed) && res.files_changed.length) {
    h += `<div class="kv">files: ${res.files_changed.map(escapeHTML).join(", ")}</div>`;
  }
  if (res.diff_summary) h += `<p class="kv">${escapeHTML(res.diff_summary)}</p>`;
  if (Array.isArray(res.open_questions) && res.open_questions.length) {
    h += `<h4>Open questions</h4><ul>` + res.open_questions.map((q) => `<li>${escapeHTML(q)}</li>`).join("") + `</ul>`;
  }
  if (res.confidence) h += `<div class="kv">confidence: ${escapeHTML(res.confidence)}</div>`;
  // Nothing recognized -> raw fallback (never a blank panel).
  if (h === `<h3>Result</h3>`) h += `<pre>${escapeHTML(JSON.stringify(res, null, 2))}</pre>`;
  return h;
}

function renderDetail(d) {
  const where = d.mode === "write" ? `${d.branch || ""} · ${d.worktreePath || ""}` : d.cwd;
  let h = `<h1>${escapeHTML(d.workflowName || shortId(d.jobId))} <span class="pill ${d.status}">${d.status}</span></h1>
    <div class="muted">${d.mode} · ${escapeHTML(where || "")} · run ${d.runId || "—"}</div>`;
  if (d.blindWindow) h += `<p>✍ orchestrator authoring workflow… (no run file yet)</p>`;
  if (d.phases && d.phases.length) {
    h += `<div class="sub">phases: ${d.phases.map((p) => p === d.phase ? `<b>${escapeHTML(p)}</b>` : escapeHTML(p)).join(" → ")}</div>`;
  }
  if (d.agents && d.agents.length) h += `<div class="fleet">${d.agents.map(agentCard).join("")}</div>`;
  if (d.tokenUsage) {
    const t = d.tokenUsage;
    h += `<div class="kv">tokens in ${t.input} / out ${t.output} / cache ${t.cacheRead} · total ${t.total}${d.cost != null ? " · $" + d.cost.toFixed(4) : ""}${d.durationMs ? " · " + fmtDur(d.durationMs) : ""}</div>`;
  }
  if (d.error) h += `<p class="pill failed">${escapeHTML(d.errorCode || "error")}</p><pre>${escapeHTML(d.error)}</pre>`;
  h += renderResult(d.result);
  $("#panel").innerHTML = h;
}

function escapeHTML(s) {
  return String(s).replace(/[&<>"']/g, (c) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

function applyState(st) {
  STATE = st;
  render();
  // If a live job is selected, refresh its detail each push; terminal stays frozen.
  if (SELECTED) {
    const j = STATE.jobs.find((x) => x.jobId === SELECTED);
    if (j && !TERMINAL.has(j.status)) loadDetail(SELECTED);
  }
}

function connect() {
  const es = new EventSource("/events");
  es.onmessage = (e) => { try { applyState(JSON.parse(e.data)); } catch (_) {} $("#conn").classList.remove("down"); };
  es.onerror = () => { $("#conn").classList.add("down"); };
}

// History window buttons.
document.addEventListener("click", (e) => {
  const b = e.target.closest("#filter button");
  if (!b) return;
  HIST_WINDOW = b.dataset.w;
  for (const x of document.querySelectorAll("#filter button")) x.classList.toggle("on", x === b);
  render();
});

// Tick the live elapsed labels locally (no server push needed).
setInterval(() => {
  for (const el of document.querySelectorAll("[data-elapsed]")) {
    const base = el.textContent.replace(/ · \d+:\d\d$/, "");
    el.textContent = base + " · " + elapsed(el.dataset.elapsed);
  }
}, 1000);

connect();
