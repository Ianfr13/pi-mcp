# Handoff — pi-mcp

**Date:** 2026-06-07 · **Repo:** private `github.com/Ianfr13/pi-mcp` · **Branches:** `master` (integrated via PR #1, #2) and `build/impl` (working). `gh` authed as `Ianfr13`.

## What this is
A **Go MCP server** that lets **Claude Code** (CLI/desktop) be the orchestrator and delegate a **TASK** to the `pi` CLI's dynamic-workflow engine. `pi` decomposes the task, **picks the models**, and runs a fleet of subagents across **many different models at once**, returning live intermediate results + a final synthesized result. Claude orchestrates; `pi` is the multi-model arm. `pi` owns decomposition + model selection (via `~/.pi/workflows/model-tiers.json`); pi-mcp holds **no secrets**.

## Status: DONE, tested, installed, proven in real use
- **Built + 100% green:** 12 packages, ~3,330 LOC (non-test), **162 tests**, all pass under `go test -race ./...`. `go build`/`go vet`/`gofmt` clean.
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
These are genuine issues the pi-mcp review surfaced in our own code. Not yet fixed.

### HIGH — correctness / concurrency
1. **Cancel vs launch races (`internal/jobs/registry.go`, `cancel.go`).** `finish()` marks the next queued job `running`, unlocks, then `start()` — a concurrent `Cancel()` in that window sees `running` with `j.cancel==nil`. And the wait goroutine's `finish(JobCompleted)` can overwrite an `aborted` set by Cancel. **Fix:** install the cancel handle under `r.mu` before exposing `running` (or a `JobStarting` state); make terminal transitions compare-and-set (don't overwrite `aborted`).
2. **Cancel prunes the write worktree before the process exits (`internal/jobs/cancel.go`).** Context cancel is async; pruning while `pi` may still write the worktree corrupts cleanup. **Fix:** prune in the post-exit path (after `wait()`/`done`).
3. **`pi_status` mutates write worktrees (`internal/app/jobs_adapter.go`, `internal/mcpserver/handler_status.go`).** `WriteInfoFor` runs `git add -A` on every status call, even while running. **Fix:** compute write info only at terminal status, or use a non-mutating diff.
4. **Launcher session-peek can deadlock the parser (`internal/app/launcher.go`).** `peekSessionID` uses `bufio.Scanner` (1 MiB token limit) and ignores `Err()`; a long JSONL line stops the scanner and the TeeReader→io.Pipe blocks `ParseStream` forever → the job hangs and leaks a slot. **Fix:** robust reader (ReadBytes loop), close the pipe with error on unrecoverable read.
5. **Status hides terminal job states in the blind window (`internal/mcpserver/handler_status.go`).** A `failed`/`aborted` job with no run file is reported `running`/`blind_window`. And aborts surface as `UNKNOWN` instead of `WORKFLOW_ABORTED` (`failureMessage` ignores `resolved.errorCode`). **Fix:** return the JobRecord terminal status when the run file is absent; fall back to `errorCode` before `ErrUnknown`. (Seen live in the demo.)
6. **Reconcile doesn't resume correlation / recovered queued jobs never terminal (`internal/jobs/reconcile.go`).** After restart, a job with SessionID but no RunID stays blind forever; queued records are restored non-terminal but never re-enqueued (task/context aren't persisted). **Fix:** mark recovered queued jobs failed/aborted with a recovery code; resume `RunIDForSession` for running ones.
7. **Ignored persistence/cleanup errors (`registry.go`, `cancel.go`, `persist.go`).** `_ = r.flushUnlocked()`, ignored `pruner.Prune()`. A crash after a failed flush can lose an accepted job. **Fix:** make the initial Submit flush mandatory; log/join later errors; expose `PERSISTENCE_ERROR`.

### MED
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
