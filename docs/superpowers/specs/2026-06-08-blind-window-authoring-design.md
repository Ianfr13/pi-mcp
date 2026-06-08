# Blind-Window Authoring Transparency — Design Spec

**Date:** 2026-06-08
**Status:** Approved (brainstorming) — pending implementation plan
**Scope:** Make the pi-mcp "blind window" (the ~20s before a run file exists, while the orchestrator authors the workflow script) **transparent** by surfacing live authoring progress to `pi_status` and the dashboard. MCP-side only — **no change to the pi engine fork**.

---

## 1. Problem

When a job is submitted, `pi` first has the pinned orchestrator (`config.OrchestratorModel` = `openai-codex/gpt-5.5`) **author the workflow script** (decide the decomposition + models). Only when that script runs does `pi` write the run file `<RunsDir>/<runId>.json`. For those ~20s there is no run file, so `pi_status` returns `blind_window:true` and the dashboard shows an opaque "✍ authoring…" with nothing else — no runId, no progress.

But during that window `pi`'s stdout **already streams** the orchestrator's activity (`session` → `agent_start` → assistant `message` events carrying the reasoning/script → `tool_execution_start(workflow)`), and pi-mcp **already reads that stream** — it just mines the `sessionId` and discards the rest (`launcher.peekSessionID`). The data exists; we only need to surface it.

## 2. Decision (from brainstorming + correctness review)

- **Approach A** — surface authoring live, MCP-side, no engine change. (Not B: patching the engine to write the run file at t=0.)
- **Richness** — **`✍ writing the workflow plan…` + a live elapsed timer + the model (gpt-5.5), shown immediately and client-side**; the **plan text is revealed when it arrives** (see §2.1), not a progressive fill. (This corrects the original "progressive live preview" goal after the review below.)
- **Reach** — both the dashboard AND `pi_status` (same file read; makes the window transparent for the polling Claude Code too).

### 2.1 Stream-timing reality (verified by adversarial review)

The pinned orchestrator is `openai-codex/gpt-5.5`, and **codex does NOT stream its authoring**. Evidence (the genuine headless `--mode json` capture `docs/research/fixtures/sample-pi-mode-json-events.jsonl`): the authoring turn is **three consecutive lines** — `message_start`(assistant, 0 chars) → `message_end`(assistant, full content: a `thinking` block + the `workflow` `toolCall` with the script) → `tool_execution_start(workflow)` — with **zero `message_update`(delta) events** (codex returns reasoning as an encrypted summary, not streamed tokens). The engine *can* stream deltas for other providers (e.g. deepseek emits ~21 `message_update`s), but not for the pinned author.

**Consequence:** the assistant content lands in **one block right before** `tool_execution_start(workflow)` — i.e. ~1s before the run file appears and the blind window ends. So a "progressively filling" preview is not achievable with this model. What we surface:
- **Immediately** (from `session`/`agent_start`, client-side): the `✍ writing the plan…` state, the elapsed timer, and the model. This is what kills the opaque blank.
- **When `message_end` lands** (near the end of the window): the plan text (`preview`) and `chars`. A late reveal, not a fill. Then the run file takes over.

The preview is still worth capturing (you see *what* pi decided right as it commits), but the UX value during the window is the timer+model+spinner, not a streaming plan.

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
   READ-LOOP goroutine (in the parser's critical path — must NOT block on I/O):
     • MkdirAll(spec.RunsDir) ONCE, before the loop starts
     • ReadBytes('\n') to EOF, ALWAYS — never early-returns (ParseStream still
       needs the later tool_execution_end(workflow) line; an early return would
       deadlock the io.Pipe writer / wait())
     • per line, in memory only: push sessionId; accumulate assistant
       text/thinking/toolCall → preview; on tool_execution_start(workflow) mark done
     • after each meaningful change, send a latest-wins SNAPSHOT to the writer
       (non-blocking; never touches disk itself)
     • defer: signal the writer to stop + DELETE <RunsDir>/<jobID>.authoring once
       (fires on clean EOF AND on cancel — child dies → stdout EOFs → loop returns)
   WRITER goroutine (decoupled from the tee/pipe):
     • coalesces snapshots (single-slot/ticker) and does the atomic tmp+rename
       writes; slow FS here can NEVER back-pressure ParseStream
        │
        ▼  (run file appears ~here → blindWindow ends)
  readers (while blindWindow):
     pi_status  → reads <rec.RunsDir>/<jobID>.authoring → StatusOutput.Authoring
     dashboard  → BuildDetail reads it → JobDetail.authoring → SPA renders live preview
```

### Preview extraction (from the stream `message` payload)
Each event line is parsed minimally for `{type, message, toolName}`. The `message` payload is `{role, content:[{type,text|thinking,...}], model, provider, ...}`:
- `role == "user"` → **skip** (that is pi-mcp's own forcing prompt echoed back).
- `role == "assistant"` → append each content block's text: `.text` (type `text`), `.thinking` (type `thinking`), and the **`workflow` `toolCall` args** (type `toolCall` — this block carries the authored **script**, which is the actual plan and the most useful thing to show). This is the orchestrator's reasoning + the decomposition it commits.
- `role == "toolResult"` → skip (that is the finished workflow result, after authoring).
- `type == "tool_execution_start"` && `toolName == "workflow"` → authoring is done.

Preview accumulates assistant text and is **tail-truncated to a dedicated `MaxAuthoringPreviewBytes` (~6KB)** const (keep the most recent, UTF-8-safe — reuse the existing `runstore.truncatePreview` helper) so the file/payload stays bounded. This is its own const, NOT the 16KB `MaxInlineResultBytes` (that one is for finished results). `chars` counts total assistant chars seen (a coarse progress signal even when the preview is truncated).

Note (see §2.1): for `gpt-5.5`/codex there are **no `message_update` deltas** — `message_end` fires once with the full content, ~immediately before `tool_execution_start(workflow)`. So `preview` populates in **one late shot**, not progressively. The blank screen is removed by the **immediate** client-side timer+model+spinner, not by the preview. The extraction still handles deltas (`message_update`) the same way, so if a future/streaming author is used the preview fills progressively for free.

## 6. Components touched (small, surgical)

- **`internal/model`** — add `AuthoringInfo` (§4 shape). Add optional `Authoring *AuthoringInfo` to `StatusOutput`.
- **`internal/jobs`** — `Spec` gains `JobID string`; the registry sets it in `start()` (it already holds `j.Record.JobID`). The authoring-file **lifecycle stays in the launcher** (create→update→delete); the registry is otherwise untouched.
- **`internal/app/launcher.go`** — replace `peekSessionID` with `observeAuthoring`. **Two goroutines** (per the review, to keep the deadlock invariant): a **read loop** that drains the tee to EOF unconditionally (never early-returns), pushes the sessionId, and parses/accumulates the preview **in memory only**; and a **writer** goroutine that the read loop feeds latest-wins snapshots and which performs the atomic `.authoring` writes, so disk I/O can never back-pressure `ParseStream`. `MkdirAll(RunsDir)` runs once before the loop. `defer` (on the read loop) stops the writer and deletes the file once. Uses `config.OrchestratorModel` for the model and `config.MaxAuthoringPreviewBytes` (new ~6KB const) for tail-truncation.
- **`internal/mcpserver`** — `buildStatus` blind-window branch reads `<rec.RunsDir>/<jobID>.authoring` and sets `out.Authoring`. A tiny reader (`readAuthoring(runsDir, jobID) (*AuthoringInfo, bool)`) lives in `runstore` (shared) or mcpserver; **runstore** is the natural home (it already reads run files) — add `runstore.ReadAuthoring`.
- **`internal/dashboard`** — `BuildDetail`, when `blindWindow`, calls `runstore.ReadAuthoring(rec.RunsDir, jobID)` and attaches `authoring` to `JobDetail`. The **light list (SSE) does NOT read the file** — the live card stays "✍ authoring… {elapsed}"; the rich preview is detail-only.
- **`internal/dashboard/web`** — the blind-window render in the detail panel shows `✍ writing the workflow plan · {elapsed} · {model}` **immediately** (elapsed ticks client-side every 1s). When `authoring.preview` is present (it arrives near the end of the window), it is shown in a `<pre>` below; until then a subtle spinner. No claim of progressive fill.

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

- **observer** (`internal/app`, where `launcher.go` lives): feed the fixture stream (`docs/research/fixtures/sample-pi-mode-json-events.jsonl`) → asserts sessionId still pushed, `.authoring` written with `model` set and `preview` containing the assistant thinking/script (and NOT the `role:user` forcing prompt), `done` flips at `tool_execution_start(workflow)`, file deleted at end.
- **no-early-return deadlock** (review fix): a stream with `tool_execution_start(workflow)` followed by a **large** `tool_execution_end(workflow)` line → assert `Launch`'s `wait()` returns (catches an observer that wrongly returns at `done` and deadlocks the `io.Pipe` writer).
- **write back-pressure decoupling** (review fix): inject a slow/blocking `.authoring` writer → assert `ParseStream` throughput / `wait()` is unaffected (catches disk I/O coupled into the tee drain).
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
