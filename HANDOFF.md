# Handoff — pi-mcp

**Date:** 2026-06-07 · **Repo:** private `github.com/Ianfr13/pi-mcp` · **Branches:** `master` (integrated via PR #1, #2) and `build/impl` (working). `gh` authed as `Ianfr13`.

## What this is
A **Go MCP server** that lets **Claude Code** (CLI/desktop) be the orchestrator and delegate a **TASK** to the `pi` CLI's dynamic-workflow engine. `pi` decomposes the task, **picks the models**, and runs a fleet of subagents across **many different models at once**, returning live intermediate results + a final synthesized result. Claude orchestrates; `pi` is the multi-model arm. `pi` owns decomposition + model selection (via `~/.pi/workflows/model-tiers.json`); pi-mcp holds **no secrets**.

## Status: DONE, tested, installed, proven in real use
- **Built + 100% green:** 12 packages, **175 tests**, all pass under `go test -race ./...` (incl. `-count=3` on the concurrency pkgs). `go build`/`go vet`/`gofmt` clean. **All 7 HIGH findings below are now FIXED** (TDD + adversarial review; see the ✅ section).
- **Real e2e proven** (`PI_MCP_E2E=1 go test ./test/e2e/`): Claude→MCP→`pi`→multi-model workflow→result; `pi_status{jobId}` reaches `completed`; fan-out across 2+ distinct models (e.g. `deepseek-v4-flash`×3 + `gpt-5.5`×1).
- **Installed:** binary at `/usr/local/bin/pi-mcp`, registered user-scope in `~/.claude.json` (`pi-mcp`, stdio). Verified live with `claude mcp list` → `pi-mcp ✓ Connected`. Available in any **new** Claude Code session (MCP servers load at session start).
- **Used for real:** delegated a multi-model code review of pi-mcp itself — 5 specialized reviewers fanned out (gpt-5.5, the "judge" tier for reviews) and streamed live findings. (Those findings are the open work below.)

## Where everything is
- Source: `cmd/pi-mcp/main.go` + `internal/{model,config,parser,runstore,worktree,runner,jobs,mcpserver,app}` (+ `internal/deps` dep-pin anchor).
- Tests: `*_test.go` per package + `test/e2e/e2e_smoke_test.go` (gated by `PI_MCP_E2E=1`; skips otherwise).
- Docs: `docs/superpowers/specs/2026-06-07-pi-mcp-design.md` (the **authoritative spec v2**, adversarially reviewed), `docs/superpowers/plans/2026-06-07-pi-mcp.md` (impl plan: header has a **"Package API Contract (AUTHORITATIVE)"** + **"Plan Errata"** that supersede the per-package task bodies), `docs/research/2026-06-07-pi-and-dynamic-workflows-study.md` (deep study of `pi`), `docs/research/fixtures/` (real `pi --mode json` stream + run file with `journal[]`).
- Auto-memory: `/root/.claude/projects/-root-pi-mcp/memory/pi-mcp-project.md` + `feedback-pi-owns-decomposition.md`.

## Build / test / run / install
```bash
# toolchain: go is at /usr/local/go/bin/go (symlinked to /usr/local/bin/go); git user is set globally
go build ./... && go vet ./... && gofmt -l .          # clean
go test -race -count=1 ./...                            # all green (e2e skips)
PI_MCP_E2E=1 go test ./test/e2e/ -run TestE2ESmoke -v -timeout 6m   # real pi, ~60s, costs ~$0.13
go build -o /usr/local/bin/pi-mcp ./cmd/pi-mcp         # (re)build the installed binary
claude mcp add -s user pi-mcp -- /usr/local/bin/pi-mcp # register (already done)
claude mcp list                                        # verify: pi-mcp ✓ Connected
```
The 4 tools: `pi_workflow(task, mode read|write, cwd)`, `pi_status(jobId|runId+cwd, wait?)`, `pi_list(cwd, limit)`, `pi_cancel(jobId)`.

## Load-bearing gotchas (do NOT regress these)
- **`pi -p` stdin MUST be `/dev/null`** — else it hangs forever waiting on stdin. (`internal/runner`)
- **Env passthrough = `os.Environ()`** to the `pi` child (HOME/PATH/AGENT_VAULT_*/proxy+CA). Required for credential resolution; pi-mcp logs no stdout/stderr/run-file. (Documented as an intentional broad trust — see open issue.)
- **`pi -p --mode json` streams the orchestrator authoring but the subagent fleet is silent on stdout** — intermediate/fleet progress comes from the **run file** (`<cwd>/.pi/workflows/runs/<runId>.json`), which grows live: `journal[].result` = full per-agent result (join `journal.index == agents.callIndex`), `agents[].resultPreview` = truncated. ~20s blind window before the run file exists. Disk `status` flaps `running↔paused` (NOT terminal — use PID + `completed/failed/aborted`). `cost/tokenUsage` only at end.
- **`pi_status{jobId}` correlation needs RETRY** (bug #1, fixed): the run file appears ~20s after the `session` event, so `correlate()` polls `RunIDForSession` until resolved.
- **Tool OUTPUT structs: a Go `any` field reflects to JSON Schema `true`, which Claude Code's MCP client REJECTS** (bug #3, fixed). Give every `any` output field a `jsonschema:"..."` tag so it becomes an object schema `{"description":...}`. Regression guard: `mcpserver.TestToolOutputSchemasHaveNoBooleanPropertySchemas`. Internal/persisted structs keep `json.RawMessage`; only OUTPUT structs use `any`+tag.
- **Modes:** `read` runs in-cwd (full tools — MAY mutate; user opted out of read-only), `write` runs in an isolated git worktree (branch `pi-mcp/job-<id>`, returns branch/diff). `mode` + `cwd` are REQUIRED; cwd is never the server cwd.
- **The headless workflow trigger is the forcing prompt** (`config.ForcingPromptTemplate`, rendered by `runner.RenderPrompt`) — it works with NO fork change (proven). The keyword/`/effort` triggers are dead headlessly; a fork mod for them is OPTIONAL.

## Open work (real findings from the multi-model self-review — prioritized)

### ✅ HIGH — correctness / concurrency (ALL FIXED, TDD + adversarial review)
Each fixed on `build/impl` with a regression test; full `-race` gate green.
1. **Cancel vs launch races (`registry.go`, `cancel.go`).** ✅ ctx+cancel now installed atomically with the `running` mark under `r.mu` (`startingUnlocked`), so `running` never coexists with `nil` cancel; `finish()` is compare-and-set (never overwrites a terminal `aborted`). Tests: `TestFinishDoesNotOverwriteAbortedOnCleanExit`, `TestAdmittedRunningJobHasNonNilCancel`.
2. **Cancel pruned the write worktree before the process exited (`cancel.go`).** ✅ For jobs we launched, prune moved to `finish()` (post-exit, before `close(done)`). Caveat the review caught: a **reconcile-recovered** running write job has `cancel==nil`/no `finish()`, so its `Cancel` still prunes synchronously (`TestCancelRecoveredRunningWriteJobPrunes`).
3. **`pi_status` mutated running write worktrees (`handler_status.go`).** ✅ `WriteInfoFor` (`git add -A`) now runs ONLY when the **disk** status is terminal & not aborted; running jobs surface branch/worktree with no staging (`writeBlock` helper). Test: `TestStatus_WriteRunningDoesNotStage`.
4. **Oversized stream line (`parser.go` + `app/launcher.go`).** ✅ *Reframed by review: there was no hang* (both readers shared the 1 MiB scanner). The real bug: a >1 MiB JSONL line (e.g. a big inline result) made `ParseStream` fail the job spuriously. Both readers switched to an unbounded `ReadBytes` loop, kept consistent (equal-consumption invariant) and always drain to EOF. Tests: `TestParseStream_OversizedLineDoesNotAbort`, `TestRealLauncher_OversizedLineBeforeSessionStillSurfaces`.
5. **Status hid terminal states in the blind window (`handler_status.go`).** ✅ Terminal JobRecord status now surfaces when the run file is absent; `failureMessage` falls back to `errorCode` before `UNKNOWN` (and tolerates a nil run). Tests: `TestStatus_TerminalJobInBlindWindowReportsTerminal`, `…CompletedJobInBlindWindowHasNoError`, `…FailureMessageFallsBackToErrorCode`.
6. **Reconcile didn't resume correlation / recovered queued stuck (`reconcile.go`).** ✅ Recovered queued → `failed`+`SERVER_RESTARTED` (new `config.ErrServerRestarted`); stale running gets a recovery code (without clobbering a real engine code); fresh running with sessionId & no runId gets a one-shot `RunIDForSession` resume. Tests: `TestReconcileResumesCorrelationAndTerminalizesQueued`, `TestReconcileCorrelationGuards`.
7. **Ignored initial persistence error (`registry.go`).** ✅ The first Submit flush is now mandatory: on failure it rolls back admission (slot/queue/ctx) under the same lock and returns `PERSISTENCE_ERROR`; the job is never silently accepted. Test: `TestSubmitPersistFailureRollsBack`. (Later flushes in start/correlate/finish stay best-effort — noted in code.)

### ✅ Observability / false-failure (the `d129db4c` incident — FIXED)
A live write-mode job looked "stuck for 15 min / no response". Root cause (a *new* issue, not one of the 7): pi authors the workflow for minutes (blind window, no run file), then does write-mode work by **editing the worktree directly** — so the run file's `updatedAt` freezes while the job is alive. The liveness heuristic (`liveStatus`) then flips it to a **false `failed`** at the 300s staleness threshold (prod `pidAlive` is a `return true` stub, so staleness is the only death signal). Fixes:
- `liveStatus` takes a `worktreeActive` signal: a write job whose worktree was modified within `StaleThreshold` is NOT failed by run-file staleness (a confirmed-dead PID still wins). A genuinely wedged write job (worktree quiet >300s) still surfaces `failed`.
- New non-mutating `JobsService.WorktreeActivity` (file count + newest mtime; skips `.git`/`.pi`; works for recovered jobs via the registry record) — distinct from `WriteInfoFor`, no `git add -A`.
- `pi_status` now returns a `progress` heartbeat (`elapsed_seconds`, plus `worktree_files`/`last_activity_seconds` for write jobs) on running/blind jobs, so a slow-but-working job is never an opaque silence and a real wedge is distinguishable. Tests: `TestStatus_WriteActiveWorktreeNotFalselyFailed`, `…WriteStaleWorktreeStillFails`, `…BlindWindowHasElapsedHeartbeat`, `TestLiveStatus_WorktreeActivityOverridesStaleness`, `TestScanWorktreeActivity`.

Still open (offered, not done): wiring a real `pidAlive` (currently a stub) and logging the ~11 silenced `flush`/`Prune` errors in `internal/jobs`.

### MED (still open)
- Registry holds `r.mu` during fsync persistence and during run-file correlation scan → stalls Submit/Cancel/Lookup under a slow FS (`registry.go`, `runstore/lookup.go`).
- `persist.go`: no parent-dir fsync after rename; no `.bak`/WAL; corrupt registry has no recovery path.
- `runstore/list.go`: `ListRuns` decodes every run before applying `limit` (O(n·size)).
- `Close()`/Submit shutdown race: Submit never checks `r.closed`; queued jobs aren't drained (`registry.go`).
- **Security:** `runId` path traversal (`runstore/lookup.go`); `cwd` has no allowlist (`mcpserver/validate.go`); **full `os.Environ()` exfil surface** to a bash-capable fleet (`runner/runner.go`); prompt on argv is visible via `/proc/<pid>/cmdline` (`runner/argv.go`); state/worktree dirs are 0755; verbatim error text may leak secrets; `jobId` is bearer-only auth. *(Good: exec uses separate argv — no shell injection; no regex — no ReDoS.)*
- No persisted-schema version/migration; no input/output size caps (task/context/list-limit/run-file).

### Notes
- The `pi` engine's own **synthesis agent** failed in the demo run (engine-side) → surfaced honestly as `failed`/`UNKNOWN`. The orchestrator (Claude) had the 5 intermediate reviews and synthesized anyway — the intended division of labor.
- Optional: wire the headless keyword/`/effort` trigger in the `pi-dynamic-workflows-custom` fork (the forcing prompt already covers delegation, so this is a nice-to-have).

## Suggested next steps
1. Fix the HIGH concurrency/correctness items (1–7) — start with #5 (status accuracy, small) and #4 (deadlock, real hang), then the cancel/finish races (#1–3), each TDD with a regression test.
2. Then the MED security hardening (runId validation, cwd allowlist, dir perms 0700, size caps) — these matter if pi-mcp is ever exposed to an untrusted MCP client.
3. Each fix on `build/impl` → PR → `master` (the established flow). `claude mcp list` after a rebuild to confirm the live server stays healthy.
