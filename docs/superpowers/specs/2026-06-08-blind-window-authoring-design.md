# Blind-Window Authoring Transparency — Design Spec

**Date:** 2026-06-08
**Status:** Approved (brainstorming) — pending implementation plan
**Scope:** Make the pi-mcp "blind window" (the ~20s before a run file exists, while the orchestrator authors the workflow script) **transparent** by surfacing live authoring progress to `pi_status` and the dashboard. MCP-side only — **no change to the pi engine fork**.

---

## 1. Problem

When a job is submitted, `pi` first has the pinned orchestrator (`config.OrchestratorModel` = `openai-codex/gpt-5.5`) **author the workflow script** (decide the decomposition + models). Only when that script runs does `pi` write the run file `<RunsDir>/<runId>.json`. For those ~20s there is no run file, so `pi_status` returns `blind_window:true` and the dashboard shows an opaque "✍ authoring…" with nothing else — no runId, no progress.

But during that window `pi`'s stdout **already streams** the orchestrator's activity (`session` → `agent_start` → assistant `message` events carrying the reasoning/script → `tool_execution_start(workflow)`), and pi-mcp **already reads that stream** — it just mines the `sessionId` and discards the rest (`launcher.peekSessionID`). The data exists; we only need to surface it.

## 2. Decision (from brainstorming)

- **Approach A** — surface authoring live, MCP-side, no engine change. (Not B: patching the engine to write the run file at t=0.)
- **Richness** — live plan preview + elapsed + model (not just "authoring…").
- **Reach** — both the dashboard AND `pi_status` (same file read; makes the window transparent for the polling Claude Code too).

## 3. Key constraint: the dashboard is a separate process

The dashboard reads only files (registry + run files); the authoring stream lives in the pi-mcp **server's** memory. So authoring progress must be **persisted to a file** the dashboard can read. We do NOT persist into `registry.json` (it would rewrite the whole registry on every authoring chunk).

## 4. Where the data lives

One small file per job, beside the run file:

**`<RunsDir>/<jobID>.authoring`** — JSON content, but the **`.authoring` extension is deliberate** so `runstore.ListRuns` / `pi_list` (which match `*.json` only) **ignore it** (no run-list pollution). `runstore.ReadRun` only opens explicit `<runId>.json` paths, so it never touches it either.

Shape (`model.AuthoringInfo`):
```
{
  "jobId":     "string",
  "model":     "string",      // config.OrchestratorModel (e.g. openai-codex/gpt-5.5)
  "startedAt":  RFC3339,       // first authoring observation
  "updatedAt":  RFC3339,
  "chars":      int,           // total assistant chars seen (progress hint)
  "preview":   "string",       // accumulated assistant text/thinking, tail-truncated ~6KB UTF-8-safe
  "done":       bool           // true once the workflow tool starts (authoring finished)
}
```

`RunsDir` is on the `JobRecord` (set at submit), so every party agrees on the path without env derivation: the **launcher** writes it (has `spec.RunsDir` + new `spec.JobID`), and both readers use `rec.RunsDir` + `jobID`. For **read** jobs `RunsDir` may not exist yet at authoring start → the writer `MkdirAll`s it first (consistent with read-mode's "may mutate cwd").

## 5. Data flow

```
pi stdout (authoring):  session → agent_start → message(assistant: thinking/text)… → tool_execution_start(workflow)
        │  tee (already exists in launcher.Launch)
        ▼
  observeAuthoring(r, sessionCh, spec)          [replaces peekSessionID]
     • pushes sessionId onto sessionCh           (correlation unchanged)
     • accumulates assistant message text/thinking → preview
     • writes/updates <RunsDir>/<jobID>.authoring (coalesced ≤300ms)
     • on tool_execution_start(workflow): done=true, final write (keep file)
     • drains to EOF (TeeReader-safe, same as today)
     • DELETE the file once, via defer when the observer goroutine returns
       (fires on clean EOF AND on cancel — the child dies, stdout EOFs, defer runs)
        │
        ▼  (run file appears ~here → blindWindow ends)
  readers (while blindWindow):
     pi_status  → reads <rec.RunsDir>/<jobID>.authoring → StatusOutput.Authoring
     dashboard  → BuildDetail reads it → JobDetail.authoring → SPA renders live preview
```

### Preview extraction (from the stream `message` payload)
Each event line is parsed minimally for `{type, message, toolName}`. The `message` payload is `{role, content:[{type,text|thinking,...}], model, provider, ...}`:
- `role == "user"` → **skip** (that is pi-mcp's own forcing prompt echoed back).
- `role == "assistant"` → append each content block's `.text` (type `text`) or `.thinking` (type `thinking`). This is the orchestrator reasoning about / writing the decomposition.
- `role == "toolResult"` → skip (that is the finished workflow result, after authoring).
- `type == "tool_execution_start"` && `toolName == "workflow"` → authoring is done.

Preview accumulates assistant text and is **tail-truncated to a dedicated `MaxAuthoringPreviewBytes` (~6KB)** const (keep the most recent, UTF-8-safe — reuse the existing `runstore.truncatePreview` helper) so the file/payload stays bounded. This is its own const, NOT the 16KB `MaxInlineResultBytes` (that one is for finished results). `chars` counts total assistant chars seen (a coarse progress signal even when the preview is truncated).

Note: this stream pairs `message_start`/`message_end` (content filled at `message_end`); no token-delta events were observed, so the preview updates a few times across the window (per assistant message), not continuously. That still removes the blank screen; the elapsed timer ticks client-side every 1s. If delta events do appear, they are captured the same way (best-effort).

## 6. Components touched (small, surgical)

- **`internal/model`** — add `AuthoringInfo` (§4 shape). Add optional `Authoring *AuthoringInfo` to `StatusOutput`.
- **`internal/jobs`** — `Spec` gains `JobID string`; the registry sets it in `start()` (it already holds `j.Record.JobID`). The authoring-file **lifecycle stays in the launcher** (create→update→delete); the registry is otherwise untouched.
- **`internal/app/launcher.go`** — replace `peekSessionID` with `observeAuthoring`: same tee/EOF-drain + sessionId push, plus preview accumulation and `.authoring` writes + defer-delete. Uses `config.OrchestratorModel` for the model and `config.MaxAuthoringPreviewBytes` (new ~6KB const) for truncation.
- **`internal/mcpserver`** — `buildStatus` blind-window branch reads `<rec.RunsDir>/<jobID>.authoring` and sets `out.Authoring`. A tiny reader (`readAuthoring(runsDir, jobID) (*AuthoringInfo, bool)`) lives in `runstore` (shared) or mcpserver; **runstore** is the natural home (it already reads run files) — add `runstore.ReadAuthoring`.
- **`internal/dashboard`** — `BuildDetail`, when `blindWindow`, calls `runstore.ReadAuthoring(rec.RunsDir, jobID)` and attaches `authoring` to `JobDetail`. The **light list (SSE) does NOT read the file** — the live card stays "✍ authoring… {elapsed}"; the rich preview is detail-only.
- **`internal/dashboard/web`** — the blind-window render in the detail panel shows `✍ writing the workflow plan · {elapsed} · {model}` + a live `<pre>` preview of `authoring.preview` (updates each 1s poll; elapsed ticks client-side).

## 7. Shared reader

`runstore.ReadAuthoring(runsDir, jobID string) (*model.AuthoringInfo, bool)` — reads + decodes `<runsDir>/<jobID>.authoring`; returns `(nil,false)` on missing/corrupt (never errors the caller). Used by both `mcpserver` and `dashboard` so the two cannot drift (same pattern as the shared `livestatus`).

## 8. Error handling

| Condition | Behavior |
|-----------|----------|
| `.authoring` missing | readers return `(nil,false)` → generic "authoring…" (today's fallback). No regression. |
| Write error (mkdir/rename) | log to stderr, continue — **never blocks the launch** |
| Run file appears (authoring done) | `.authoring` lingers (defer-deleted at process exit) but is only **read** while `blindWindow` (run file absent); once the run file exists readers ignore it. No gap, no race. |
| pi-mcp server crashes mid-authoring | the observer's `defer` runs on child-process death/EOF, not on a server crash; a rare orphan `.authoring` (unique jobID, tiny) is harmless. Optional best-effort GC noted below. |
| Corrupt `.authoring` (partial write) | atomic tmp+rename write avoids partials; a decode failure → `(nil,false)` fallback |

## 9. Testing

- **observer** (`internal/app`, where `launcher.go` lives): feed the fixture stream (`docs/research/fixtures/sample-pi-mode-json-events.jsonl`) → asserts sessionId still pushed, `.authoring` written with `model` set and `preview` containing the assistant thinking/text (and NOT the `role:user` forcing prompt), `done` flips at `tool_execution_start(workflow)`, file deleted at end.
- **runstore**: `ReadAuthoring` round-trips a written file; missing/corrupt → `(nil,false)`; a `*.authoring` file is **excluded** by `ListRuns`.
- **mcpserver**: `buildStatus` blind-window + `.authoring` present → `StatusOutput.Authoring` populated; absent → nil.
- **dashboard**: `BuildDetail` blind-window + `.authoring` → `JobDetail.authoring`; absent → fallback.
- **visual**: fabricate a `<RunsDir>/<jobID>.authoring` for a synthetic "running, no run file" job and screenshot the authoring detail state via the Playwright harness.

## 10. Scope / YAGNI

**In:** observer preview + `.authoring` file + shared reader + `pi_status` field + dashboard detail render + SPA authoring view.

**Out:** engine-fork change (Approach B); token-level streaming if pi emits none; persisting authoring into `registry.json`; authoring preview in the light SSE list (detail-only); a dedicated reconcile GC pass for orphan `.authoring` files (launcher `defer`-delete suffices; revisit only if orphans accumulate).

## 11. File-touch summary

```
internal/config/config.go                # +MaxAuthoringPreviewBytes (~6KB)
internal/model/model.go                  # +AuthoringInfo, +StatusOutput.Authoring
internal/jobs/job.go (Spec)              # +Spec.JobID
internal/jobs/registry.go                # set spec.JobID in start()
internal/app/launcher.go                 # peekSessionID -> observeAuthoring (write/delete .authoring)
internal/runstore/authoring.go (new)     # ReadAuthoring (+ test); ListRuns already ignores non-.json
internal/mcpserver/handler_status.go     # blind-window: read authoring -> out.Authoring
internal/dashboard/state.go              # BuildDetail blind-window: attach authoring
internal/dashboard/web/app.js,app.css    # authoring detail render (preview + elapsed + model)
```
