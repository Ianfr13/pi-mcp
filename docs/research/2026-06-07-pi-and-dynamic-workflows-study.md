# Estudo profundo — pi & dynamic workflows (base para o pi-mcp)

> Gerado por workflow multi-agente em 2026-06-07. 11/11 frentes cobertas.

## Sumário executivo

## What `pi` Is

`pi` is a Node.js coding agent CLI (`@earendil-works/pi-coding-agent` v0.78.1; `/usr/bin/pi` → `dist/cli.js`). It runs an LLM agent loop with tools, sessions, and MCP servers. Three output modes via `--mode`:

- **text** (default): human TUI / plain output.
- **json**: one JSON event per line on **stdout** (the `AgentSessionEvent` stream), diagnostics on **stderr**. This is the mode pi-mcp drives.
- **rpc**: bidirectional JSONL command protocol — avoid for headless delegation (strict LF framing, can block on `extension_ui_request` dialogs awaiting stdin).

Headless one-shot is `pi -p --mode json`. On **this box** the effective default is `openai-codex/gpt-5.5` @ thinking `xhigh` (from `/root/.pi/agent/settings.json`), **not** the help text's `google` default — confirmed by live probe (`agent_end` reports `provider=openai-codex, model=gpt-5.5, api=openai-codex-responses`). Config home is `/root/.pi/agent` (override `PI_CODING_AGENT_DIR`). Credentials live in `auth.json` (openai-codex OAuth, deepseek api_key) and `models.json` (deepseek is a custom provider whose `apiKey` is a `!`-prefixed `agent-vault` shell command). pi resolves all of this itself — **pi-mcp passes no `--api-key`** and reads no secrets.

## What the Dynamic-Workflow Engine Does

A local fork (`/root/projects/pi-dynamic-workflows-custom`, forked from `@quintinshaw/pi-dynamic-workflows` v2.0.1) is loaded as a pi extension via `settings.json` `packages` and runs the TypeScript **source** directly (`extensions/workflow.ts` → `../src/index.js` → `src/*.ts`); editing `src/` + `/reload` (or a fresh `pi -p` process) is the whole loop — no `dist/` build needed for the running extension. The suite is green (biome clean, tsc 0, 83 tests).

The engine registers a single tool named exactly **`workflow`** with input `{script, args?, background? (default TRUE), maxAgents? (1000), agentTimeoutMs? (300000), tokenBudget? (unlimited)}`. The `script` is JS module source whose **first statement must be `export const meta = {...}`** (object-literal only — AST-evaluated, not executed). It runs in a Node `vm` realm (NOT a security sandbox) with a determinism prelude: `Date.now()`, `Math.random()`, and `new Date()` (no-arg) are rejected at parse time and throw at runtime, because nondeterminism would break the journaled resume. Injected globals include `agent`, `parallel`, `pipeline`, `workflow`, `verify`, `judgePanel`, `checkpoint`, `phase`, `args`, `budget`, `log`.

`agent(prompt, opts)` is the fan-out primitive. Each call reserves a slot atomically (against `maxAgents`/budget), gets a monotonic `callIndex`, hashes its identity (prompt+model+tier+phase+agentType+agentDef+schema), checks the resume journal, then runs as a **real pi SDK session** (`createAgentSession` with in-memory `SessionManager` but **real `SettingsManager.create`** so it inherits the user's default provider/model). Caps: `MAX_CONCURRENCY=16` (the real parallelism ceiling), `MAX_AGENTS_PER_RUN=1000`, default per-agent timeout 5 min. Nested `workflow()` is one level deep and shares the parent `SharedRuntime` (limiter/agentCount/spent/tokenUsage). Token budget is a **soft, post-hoc** gate (in-flight waves can overshoot; later `agent()` calls throw `TOKEN_BUDGET_EXHAUSTED`).

## How Multi-Model Fan-Out Works

pi — not Claude — chooses the models. Each `agent()` resolves its model independently via `resolveModelRoute()` with this precedence: **explicit-model > agent-type-model > rules (field priority agentType > phase > label) > explicit-tier > phase-model > default-tier (`medium`) > non-strict fallback-to-main > (strict: throw `MODEL_ROUTING_ERROR`)**. Tiers map named slots → one model spec each in `~/.pi/workflows/model-tiers.json` (`{tiers, rules, fallback}`). The live config is **strict** (`fallback.mode: 'error'`) with tiers small/cheap=`deepseek/deepseek-v4-flash`, medium/research=`deepseek/deepseek-v4-pro`, big/coder/judge=`openai-codex/gpt-5.5`, and rules (coder agentType→coder, reviewer→judge, phase `Scan`→cheap, label `/judge|critic|review|synthesis|final/i`→judge).

**Ground-truth proof** from real run journals: run `mq314rcc-nlfu7s` ran 3 agents on 3 different models in one run (Scan→deepseek-v4-flash, label "final critic"→gpt-5.5, Verify→deepseek-v4-pro), exactly matching the tier rules; run `mq3sfgzk-asvqo8` fanned out 20 agents (19 flash + 1 pro synthesis) with journal indices appended in **completion order** ([0,6,5,4,1,7,...]), proving concurrent execution. The MCP therefore must **not** shard tasks across models or run multiple pi processes to "spread" models — a single `pi -p` workflow run already fans out across the heterogeneous fleet. To change the fleet, the **user** edits `model-tiers.json` and `.pi/agents/*.md` (agentType defs binding tools/model/prompt; currently none exist).

**Headless safety contract (load-bearing):** in print/json mode `hasUI === false` (the runner keeps `noOpUIContext`), so the workflow tool's preflight consent card and `checkpoint()` human gates are auto-skipped — checkpoints take their declared default and a background run never hangs. The `session_start` hook still fires via `bindExtensions`, so the `workflow` tool stays registered and active headlessly. Consent is already accepted on this box (`~/.pi/workflows/consent.json {multiAgentWarningAccepted:true}`). The `/effort`/`/ultracode` auto-arming hooks are **dead headlessly** (they bail on `event.source !== 'interactive'`), so the MCP must force a workflow by **replicating `buildForcedWorkflowPrompt`** in its prompt text, not by relying on standing modes.

## Arquitetura-alvo do pi-mcp

## pi-mcp Target Architecture (Go MCP server)

`pi-mcp` is a thin, stateless-credential Go MCP server registered into Claude Code over stdio. It does **not** decompose tasks or pick models — it hands pi a TASK plus a forced-workflow directive, drives `pi -p --mode json` as a child process, and exposes the persisted run files as read-only visibility. pi owns decomposition, model fan-out, and credentials.

### Data-flow diagram

```
+-------------+     MCP/stdio      +------------------+
| Claude Code | <----------------> |  pi-mcp (Go)     |
| (host)      |  pi_run_workflow   |  MCP server      |
+-------------+  pi_run_status     +------------------+
                pi_list_runs                |
                                            | exec.Cmd (env INHERITED:
                                            |   HOME, PATH, AGENT_VAULT_*,
                                            |   proxy/CA vars; Env=nil)
                                            |  + cwd set deliberately
                                            v
                              +-------------------------------+
                              | pi -p --mode json             |
                              |  (headless, hasUI=false)      |
                              |  prompt = TASK +              |
                              |  buildForcedWorkflowPrompt(   |
                              |    background:false)          |
                              +-------------------------------+
                                            |
                                  emits `workflow` tool call
                                            v
                              +-------------------------------+
                              | dynamic-workflow engine       |
                              | runWorkflow() in vm realm     |
                              |  meta + agent()/parallel()    |
                              +-------------------------------+
                                  |        |        |        |
                          resolveModelRoute (tiers/rules, strict)
                                  v        v        v        v
        +-----------+   +-----------+   +-----------+   +-----------+
        | agent #1  |   | agent #2  |   | agent #3  |   |  ...#N    |
        | deepseek- |   | gpt-5.5   |   | deepseek- |   | (<=16     |
        | v4-flash  |   | (judge)   |   | v4-pro    |   | concurrent)|
        +-----------+   +-----------+   +-----------+   +-----------+
              \_______________ synthesis agent _______________/
                                            |
                  background:false => synthesized result returned INLINE
                                            v
        STDOUT JSONL events  ............. + .............  run files
        (session->agent_start->...->        /root/.pi/workflows/runs/
         tool_execution_end[workflow]->     <runId>.json (+.bak, run-*.log)
         agent_end with messages[])
                                            |
                          pi-mcp parses agent_end.messages[]
                          toolResult(workflow).details.result
                                            v
                              structured result -> Claude Code
```

### Tool surface

**`pi_run_workflow`** — primary delegation entrypoint. Background-first design with two paths:

- Default sync path: launch `pi -p --mode json` with a prompt that **(a)** explicitly asks for a workflow/fan-out and **(b)** instructs `set background:false / return the result inline this turn` (optionally hardened with `--append-system-prompt`, replicating `buildForcedWorkflowPrompt`). Parse stdout JSONL; the authoritative result is the terminal `agent_end.messages[]` → the `role:"toolResult"` message with `toolName=="workflow"`. Prefer `details.result` (raw structured object) over parsing the fenced ```json``` out of `content[0].text` (header `✓ Workflow "<name>" finished (N agents · T tokens · $C · Ds)`). Assert at least one successful `toolName=="workflow"` toolResult; otherwise report "no workflow ran" (pi answered directly or the script errored — `SCRIPT_VALIDATION_ERROR`, `Workflow scripts must be deterministic...`, etc.). Accepts `maxAgents`, `agentTimeoutMs`, `tokenBudget` passthrough; recommend setting `tokenBudget` for cost control (soft cap — surface that it can overshoot).
- Background path (`background:true` for long fleets): launch detached, capture `runId` (regex `Run ID: (\S+)` from started text, else `details.runId`, else newest `runs/*.json`), return jobId immediately. **Caveat:** under single-shot `-p`, a background workflow's result is delivered via a *later* injected `workflow-result` turn that print-mode will not wait for — so background mode MUST poll the run file to completion rather than expecting an inline result.

**`pi_run_status` (alias of status/result)** — read a single `<runId>.json` with `.bak` fallback on parse failure (mirrors `RunPersistence.load`); ignore `.tmp`. Returns run-level `status` (pending|running|paused|completed|failed|aborted), `currentPhase` + `phases[]` progress, `startedAt/updatedAt/completedAt`, `durationMs`, `sessionId`, `tokenUsage{input,output,total,cost,cacheRead,cacheWrite}` (read `cost` verbatim — never recompute; `total` includes `cacheRead`). Per-agent: map `agents[] -> {id,label,phase,model,status,tokens,startedAt,endedAt}` and build a **model histogram** (group by `model`) as the proof of multi-model fan-out. Treat `result/tokenUsage/durationMs/completedAt` as **optional pointer fields** — they are entirely absent on non-completed runs. For the synthesized answer return top-level `.result` raw (arbitrary, workflow-defined; can be a 21KB markdown string — support truncation), with `not ready` when absent. On `failed`/agent `error|interrupted`, read the sidecar log via the path embedded in `logs[0]` (filename uses a mangled prefix, e.g. `mq3v6uyo-pmtqtv` → `run-mq3v6uyv.log` — never reconstruct it). Optional join `agents[id=N] -> journal[index=N-1].result` for full per-agent output.

**`pi_list_runs`** — glob `<cwd>/.pi/workflows/runs/*.json` excluding `*.json.bak` and `*.json.tmp` (mirrors `RunPersistence.list()`); JSON-parse each, skip failures, sort by `updatedAt` desc (file mtime as secondary). Return only lightweight fields (runId, workflowName, status, currentPhase, phases.length, agents.length, timestamps, durationMs, tokenUsage.cost/total) — keep heavy `prompt`/`journal`/`result`/`script` behind explicit opt-in. Directory glob is the only enumeration mechanism (no manifest). Critically, the runs dir is **cwd-relative** — pi-mcp must control/know the cwd it launches pi with (force a known dir, e.g. `$HOME`, so this resolves to `/root/.pi/workflows/runs`).

Optional adjuncts: `pi_get_routing_config` (expose `model-tiers.json` so Claude sees which models pi may pick and which rules fire), `pi_run_logs(runId)` (tail the sidecar), `pi_run_agent_result(runId, agentId)`. Control verbs (stop/pause/resume/restart) require a **live** WorkflowManager in the pi process — they cannot be done by editing JSON, so defer them or drive them through a pi command invocation.

## Índice das frentes

- pi 0.78.1 runtime: CLI flags, output modes (text/json/rpc), provider/model pool, config & session storage layout
- pi HEADLESS I/O CONTRACT — JSONL streaming envelope, tool events, on-disk session/run schemas, and the workflow tool result format
- runWorkflow() engine: VM realm, injected globals, model routing, resume/checkpoint, budgets, structured output
- Orchestration & Persistence: WorkflowManager, run-persistence, errors
- The `workflow` tool, subagent model resolution + provider calls, and the headless extension wiring
- Model routing: tiers, rules, agentTypes, and how one workflow fans out across many models
- Built-in Workflow Patterns, Effort/Ultracode Standing Modes, Saved Workflows, Worktrees, Web Tools, Config & Logging
- The /workflows command tree, navigator, task panel, editor, and render/builder functions
- pi-dynamic-workflows-custom: origin, build/load/reload loop, model-routing design, and stability for pi-mcp
- pi Workflow Run Persistence: Schema, Multi-Model Evidence, and Lifecycle States
- How pi wires MCP servers + resolves `!`-command / `${ENV}` credentials, and how pi-mcp will be registered in Claude Code


---

# pi 0.78.1 runtime: CLI flags, output modes (text/json/rpc), provider/model pool, config & session storage layout

**Arquivos examinados:** /usr/bin/pi, /usr/lib/node_modules/@earendil-works/pi-coding-agent/package.json, /usr/lib/node_modules/@earendil-works/pi-coding-agent/docs/json.md, /usr/lib/node_modules/@earendil-works/pi-coding-agent/docs/rpc.md, /usr/lib/node_modules/@earendil-works/pi-coding-agent/docs/providers.md, /usr/lib/node_modules/@earendil-works/pi-coding-agent/docs/models.md, /usr/lib/node_modules/@earendil-works/pi-coding-agent/docs/settings.md, /usr/lib/node_modules/@earendil-works/pi-coding-agent/docs/session-format.md, /usr/lib/node_modules/@earendil-works/pi-coding-agent/docs/packages.md, /usr/lib/node_modules/@earendil-works/pi-coding-agent/docs/skills.md, /root/.pi/agent/settings.json, /root/.pi/agent/models.json, /root/.pi/agent/mcp.json, /root/.pi/agent/mcp-cache.json, /root/.pi/agent/auth.json, /root/.pi/settings.json, /root/.pi/agent/state/pi-hud.json, /root/.pi/agent/bin/railway-mcp-vault, /root/.pi/agent/sessions/--root-projects-sales-brain--/2026-06-06T16-50-51-106Z_019e9dd8-2e61-744a-b34e-00bb22e594bd.jsonl, /root/.pi/workflows/model-tiers.json, /root/.pi/workflows/consent.json, /root/.pi/workflows/runs/mq3v6uyo-pmtqtv.json

**Fatos-chave:**
- pi binary at /usr/bin/pi is a symlink (681 bytes) to ../lib/node_modules/@earendil-works/pi-coding-agent/dist/cli.js — a Node.js shebang script (#!/usr/bin/env node). Package name @earendil-works/pi-coding-agent, version 0.78.1, bin.pi = dist/cli.js.
- Three output modes via --mode: text (default, human TUI/plain), json (one JSON event per line to stdout; AgentSessionEvent stream), rpc (bidirectional JSONL command/event protocol over stdin/stdout). Headless one-shot uses -p/--print.
- Effective default provider/model on THIS box = openai-codex / gpt-5.5 (from /root/.pi/agent/settings.json defaultProvider+defaultModel), NOT the help text's --provider default of 'google'. Confirmed by live probe: agent_end assistant message reports provider=openai-codex, model=gpt-5.5, api=openai-codex-responses. defaultThinkingLevel=xhigh.
- 264 models from `pi --list-models`: 258 openrouter + 4 openai-codex (gpt-5.3-codex-spark, gpt-5.4, gpt-5.4-mini, gpt-5.5) + 2 deepseek (deepseek-v4-flash, deepseek-v4-pro). Only 3 are in settings.enabledModels (Ctrl+P cycling): deepseek/deepseek-v4-pro, deepseek/deepseek-v4-flash, openai-codex/gpt-5.5. The deepseek provider is a CUSTOM provider defined in models.json (not a built-in).
- auth.json (0600) holds two providers: 'deepseek' (keys: type, key — api_key) and 'openai-codex' (keys: type, access, refresh, expires, accountId — OAuth). Tokens auto-refresh. auth.json takes priority over env vars. Credential `key` values support `!command` shell execution, `$ENV`/`${ENV}` interpolation, and literals.
- Config home is /root/.pi/agent (override via PI_CODING_AGENT_DIR). Sessions live under <home>/sessions/--<cwd-with-slashes-as-dashes>--/<ISO-timestamp>_<uuid>.jsonl (JSONL, version 3, tree-structured via id/parentId). Session dir precedence: --session-dir > PI_CODING_AGENT_SESSION_DIR > settings.sessionDir.
- MCP servers configured in /root/.pi/agent/mcp.json (mcpServers map): context7 (http) and railway (stdio via /root/.pi/agent/bin/railway-mcp-vault, lazy lifecycle). Tool schemas cached in mcp-cache.json (version 1; servers.<name> = {configHash, tools[], resources[], cachedAt}). railway exposes 36 tools, context7 exposes 2.
- 9 packages loaded via settings.packages (npm: and a local path /root/projects/pi-dynamic-workflows-custom). 92 skills (symlinks) under <home>/skills. The pi-dynamic-workflows-custom fork persists workflow state under /root/.pi/workflows/ (model-tiers.json, consent.json, runs/<runId>.json + runs/run-<id>.log).

## pi Runtime Overview\n\n| Item | Value |\n|------|-------|\n| Binary | `/usr/bin/pi` -> `../lib/node_modules/@earendil-works/pi-coding-agent/dist/cli.js` (symlink, 681 bytes) |\n| Type | Node.js script, `#!/usr/bin/env node` |\n| Package | `@earendil-works/pi-coding-agent` |\n| Version | `0.78.1` (`pi --version` => `0.78.1`) |\n| Install root | `/usr/lib/node_modules/@earendil-works/pi-coding-agent/` (dist/, docs/, examples/, node_modules/) |\n\nThe Go MCP server invokes the `pi` binary as a normal CLI subprocess; it is a Node program, so Node must be on PATH.\n\n---\n\n## Every CLI Flag (`pi --help`, verbatim meaning)\n\n### Subcommands\n`pi install <source> [-l]`, `pi remove/uninstall <source> [-l]`, `pi update [source|self|pi]`, `pi list`, `pi config` (TUI to enable/disable package resources). `-l` writes to project `.pi/settings.json` instead of user settings.\n\n### Options\n| Flag | Meaning |\n|------|---------|\n| `--provider <name>` | Provider name. **Help default: `google`**, but overridden by `settings.defaultProvider`. |\n| `--model <pattern>` | Model pattern or ID. Supports `provider/id` and optional `:<thinking>` (e.g. `sonnet:high`). |\n| `--api-key <key>` | API key (else env vars / auth.json). **Highest precedence** in credential resolution. |\n| `--system-prompt <text>` | Replace system prompt. |\n| `--append-system-prompt <text>` | Append text OR file contents to system prompt (repeatable). |\n| `--mode <mode>` | Output mode: `text` (default), `json`, or `rpc`. |\n| `--print, -p` | Non-interactive: process prompt and exit. **Key flag for headless MCP use.** |\n| `--continue, -c` | Continue previous session. |\n| `--resume, -r` | Select a session to resume (interactive). |\n| `--session <path\\|id>` | Use specific session file or partial UUID. |\n| `--session-id <id>` | Use exact project session ID, creating it if missing. |\n| `--fork <path\\|id>` | Fork a session into a new session. |\n| `--session-dir <dir>` | Directory for session storage and lookup. |\n| `--no-session` | Ephemeral; don't save session. |\n| `--name, -n <name>` | Set session display name. |\n| `--models <patterns>` | Comma-separated model patterns for Ctrl+P cycling (globs `anthropic/*`, `*sonnet*`, fuzzy, `:thinking`). |\n| `--no-tools, -nt` | Disable ALL tools. |\n| `--no-builtin-tools, -nbt` | Disable built-in tools but keep extension/custom tools. |\n| `--tools, -t <tools>` | Comma-separated **allowlist** of tool names. |\n| `--exclude-tools, -xt <tools>` | Comma-separated **denylist** of tool names. |\n| `--thinking <level>` | `off, minimal, low, medium, high, xhigh`. |\n| `--extension, -e <path>` | Load extension file (repeatable). Also accepts `npm:`/`git:` for one-run temp install. |\n| `--no-extensions, -ne` | Disable extension discovery (explicit `-e` still load). |\n| `--skill <path>` | Load skill file/dir (repeatable). |\n| `--no-skills, -ns` | Disable skills discovery. |\n| `--prompt-template <path>` | Load prompt template file/dir (repeatable). |\n| `--no-prompt-templates, -np` | Disable prompt-template discovery. |\n| `--theme <path>` | Load theme file/dir (repeatable). |\n| `--no-themes` | Disable theme discovery. |\n| `--no-context-files, -nc` | Disable `AGENTS.md` and `CLAUDE.md` discovery. |\n| `--export <file>` | Export a session `.jsonl` to HTML and exit. |\n| `--list-models [search]` | List available models (optional fuzzy search). |\n| `--verbose` | Force verbose startup. |\n| `--offline` | Disable startup network ops (= `PI_OFFLINE=1`). |\n| `--help, -h` / `--version, -v` | Help / version. |\n\n**Extension-registered flags:** Extensions can add flags. On this box `pi-mcp-adapter` registers `--mcp-config <value>` (Path to MCP config file). The plan-mode extension would register `--plan`, etc.\n\n### Positional args\n`pi [options] [@files...] [messages...]` — `@file` includes file contents/images in the first message; bare strings are messages.\n\n---\n\n## Output Modes\n\n### text (default)\nHuman-oriented. With `-p`, prints assistant text to stdout and exits.\n\n### json (`--mode json`) — primary for headless fan-out\nEmits **one JSON object per line** to stdout. First line is the session header, then a stream of events. From `docs/json.md`:\n\n```typescript\ntype AgentSessionEvent =\n  | AgentEvent\n  | { type: \"queue_update\"; steering: readonly string[]; followUp: readonly string[] }\n  | { type: \"compaction_start\"; reason: \"manual\" | \"threshold\" | \"overflow\" }\n  | { type: \"compaction_end\"; reason; result: CompactionResult | undefined; aborted: boolean; willRetry: boolean; errorMessage?: string }\n  | { type: \"auto_retry_start\"; attempt; maxAttempts; delayMs; errorMessage }\n  | { type: \"auto_retry_end\"; success; attempt; finalError? };\n\ntype AgentEvent =\n  | { type: \"agent_start\" }\n  | { type: \"agent_end\"; messages: AgentMessage[] }\n  | { type: \"turn_start\" }\n  | { type: \"turn_end\"; message: AgentMessage; toolResults: ToolResultMessage[] }\n  | { type: \"message_start\"; message: AgentMessage }\n  | { type: \"message_update\"; message: AgentMessage; assistantMessageEvent: AssistantMessageEvent }\n  | { type: \"message_end\"; message: AgentMessage }\n  | { type: \"tool_execution_start\"; toolCallId; toolName; args }\n  | { type: \"tool_execution_update\"; toolCallId; toolName; args; partialResult }\n  | { type: \"tool_execution_end\"; toolCallId; toolName; result; isError };\n```\n\n**Live-probed sequence** (`pi -p --mode json --no-session --offline --tools read --model deepseek/deepseek-v4-flash \"...\"`), counts of event `type`:\n```\n1 session, 1 agent_start, 1 turn_start, 2 message_start, 21 message_update,\n2 message_end, 1 turn_end, 1 agent_end\n```\nFirst line header: `{\"type\":\"session\",\"version\":3,\"id\":\"<uuid>\",\"timestamp\":\"...\",\"cwd\":\"...\"}`. `agent_end.messages[]` includes the final assistant message with full metadata — example captured:\n```json\n{\"role\":\"assistant\",\"provider\":\"deepseek\",\"model\":\"deepseek-v4-flash\",\"api\":\"openai-completions\",\"stopReason\":\"stop\",\"usage\":{\"input\":12635,\"output\":18,\"cacheRead\":0,\"cacheWrite\":0,\"totalTokens\":12653,\"cost\":{\"input\":0.0012635,\"output\":7.2e-06,\"cacheRead\":0,\"cacheWrite\":0,\"total\":0.0012707}}}\n```\nFinal text extracted from `message_end` assistant content `{type:text}`. Recommended consumption: `... 2>/dev/null | jq -c 'select(.type==\"agent_end\")'` (or `message_end`).\n\n### rpc (`--mode rpc`) — bidirectional, persistent process\nStrict JSONL over stdin/stdout, **LF-only** record delimiter (Node `readline` is NON-compliant because it also splits on U+2028/U+2029 — must split on `\\n` only, strip trailing `\\r`). Commands in (one JSON/line), responses (`type:\"response\"` with optional correlating `id`) + events out. Command set includes: `prompt` (with `streamingBehavior: \"steer\"|\"followUp\"`), `steer`, `follow_up`, `abort`, `new_session`, `get_state`, `get_messages`, `set_model`, `cycle_model`, `get_available_models`, `set_thinking_level`, `cycle_thinking_level`, `set_steering_mode`, `set_follow_up_mode`, `compact`, `set_auto_compaction`, `set_auto_retry`, `abort_retry`, `bash`, `abort_bash`, `get_session_stats`, `export_html`, `switch_session`, `fork`, `clone`, `get_fork_messages`, `get_last_assistant_text`, `set_session_name`, `get_commands`. There is also an **extension UI sub-protocol** (`extension_ui_request`/`extension_ui_response` for select/confirm/input/editor/notify/setStatus/etc.) — relevant because in RPC mode extension dialogs would block waiting for stdin responses; a headless driver must answer or set timeouts.\n\n> For pi-mcp the simplest, most robust pattern is **`pi -p --mode json`** per fan-out subagent (one-shot, parse the JSONL stream). RPC mode is only needed for long-lived interactive steering.\n\n---\n\n## Provider / Model Pool\n\n`pi --list-models` returns **264** models. Columns: `provider | model | context | max-out | thinking | images`.\n\n| Provider | Count | Notes |\n|----------|-------|-------|\n| `openrouter` | 258 | Built-in catalog (anthropic/*, openai/*, google/*, qwen/*, deepseek/*, z-ai/glm-*, x-ai/grok-*, minimax, moonshotai/kimi, meta-llama, mistralai, nvidia, etc.). `~`-prefixed aliases like `~anthropic/claude-opus-latest`. |\n| `openai-codex` | 4 | `gpt-5.3-codex-spark`, `gpt-5.4`, `gpt-5.4-mini`, `gpt-5.5` — via ChatGPT OAuth (auth.json `openai-codex`). |\n| `deepseek` | 2 | `deepseek-v4-pro`, `deepseek-v4-flash` — **custom provider** from `models.json`. |\n\nThe full built-in provider catalog pi knows (from `docs/providers.md`, selectable via env var or auth.json): anthropic, ant-ling, azure-openai-responses, openai, deepseek, nvidia, google, mistral, groq, cerebras, cloudflare-ai-gateway, cloudflare-workers-ai, xai, openrouter, vercel-ai-gateway, zai, zai-coding-cn, opencode, opencode-go, huggingface, fireworks, together, kimi-coding, minimax(+cn), xiaomi (+token-plan cn/ams/sgp), plus cloud providers amazon-bedrock and google-vertex. Each provider's full model list is baked into the pi release.\n\n**Thinking levels:** `off, minimal, low, medium, high, xhigh` (`xhigh` only on OpenAI codex-max models). Per-model mapping via `thinkingLevelMap` (string=value sent, `null`=unsupported/hidden, omitted=default).\n\n---\n\n## Configuration Files (on this box)\n\n### `/root/.pi/agent/settings.json` (user/global)\n```json\n{\n  \"lastChangelogVersion\": \"0.78.1\",\n  \"packages\": [\n    \"npm:pi-web-access\", \"npm:pi-hud\",\n    \"/root/projects/pi-dynamic-workflows-custom\",\n    \"npm:@juicesharp/rpiv-ask-user-question\", \"npm:@juicesharp/rpiv-todo\",\n    \"npm:pi-simplify\", \"npm:pi-btw\", \"npm:pi-mcp-adapter\", \"npm:@vndv/pi-codegraph\"\n  ],\n  \"defaultModel\": \"gpt-5.5\",\n  \"defaultThinkingLevel\": \"xhigh\",\n  \"enabledModels\": [\"deepseek/deepseek-v4-pro\",\"deepseek/deepseek-v4-flash\",\"openai-codex/gpt-5.5\"],\n  \"hud\": { ... },\n  \"defaultProvider\": \"openai-codex\"\n}\n```\nThe local pi-dynamic-workflows fork is loaded as a **local-path package** (entry `/root/projects/pi-dynamic-workflows-custom`).\n\n### `/root/.pi/settings.json` (a second, partial settings file at `~/.pi/`)\nContains only a `hud` block (`mode: footer`). Note: the documented project-settings path is `.pi/settings.json` relative to cwd; the canonical user settings is `~/.pi/agent/settings.json`. Settings precedence: project overrides global, nested objects merged.\n\n### `/root/.pi/agent/models.json` (custom providers)\nDefines provider `deepseek`:\n```json\n{ \"providers\": { \"deepseek\": {\n  \"baseUrl\": \"https://api.deepseek.com\",\n  \"api\": \"openai-completions\",\n  \"apiKey\": \"!AGENT_VAULT_VAULT=sales-brain agent-vault vault credential get DEEPSEEK_API_KEY\",\n  \"compat\": { \"supportsDeveloperRole\": false, \"supportsReasoningEffort\": true, \"thinkingFormat\": \"deepseek\" },\n  \"models\": [ {id:\"deepseek-v4-pro\",...}, {id:\"deepseek-v4-flash\",...} ]\n}}}\n```\nNote the `apiKey` uses the `!command` form to fetch the key from **agent-vault** at request time (no secret stored in file). Each model has reasoning=true, contextWindow=1_000_000, maxTokens=131072, cost, and a `thinkingLevelMap`. `models.json` reloads on each `/model` open.\n\n### `/root/.pi/agent/mcp.json`\n```json\n{ \"mcpServers\": {\n  \"context7\": { \"type\": \"http\", \"url\": \"https://mcp.context7.com/mcp\", \"directTools\": true },\n  \"railway\": {\n    \"command\": \"/root/.pi/agent/bin/railway-mcp-vault\", \"args\": [],\n    \"lifecycle\": \"lazy\",\n    \"env\": { \"AGENT_VAULT_ADDR\": \"${AGENT_VAULT_ADDR}\", \"AGENT_VAULT_TOKEN\": \"${AGENT_VAULT_TOKEN}\",\n             \"RAILWAY_MCP_VAULT\": \"sales-brain\", \"RAILWAY_MCP_TOKEN_KEY\": \"RAILWAY_API_TOKEN\" },\n    \"directTools\": true\n  }\n}}\n```\nServer config fields observed: `type` (`http`/stdio-implied-by-`command`), `url`, `command`, `args`, `env` (supports `${VAR}` interpolation), `lifecycle` (`lazy`), `directTools` (bool — surface tools without namespacing). The railway server is a stdio wrapper script that pulls a token from agent-vault then `exec railway mcp`.\n\n### `/root/.pi/agent/mcp-cache.json` (structure only)\n`{ version: 1, servers: { <name>: { configHash: string, tools: array, resources: array, cachedAt: number } } }`. Each tool object = `{ name, description, inputSchema }`. context7=2 tools, railway=36 tools. A `.bak.<epoch>` backup also exists. pi-mcp's visibility tools can read this to know what MCP tools a run had access to.\n\n### `/root/.pi/agent/auth.json` (structure/keys ONLY — no values)\n0600 perms. Top-level keys = provider names:\n- `deepseek`: `{ type, key }` (api_key form).\n- `openai-codex`: `{ type, access, refresh, expires, accountId }` (OAuth; auto-refreshed when expired).\n\nResolution order for credentials: (1) `--api-key`, (2) `auth.json`, (3) env var, (4) custom-provider key from models.json. `key` values support `!command`, `$ENV`/`${ENV}`, escapes `$$`/`$!`, and literals.\n\n### Other dirs/files under `/root/.pi/agent/`\n| Path | Contents |\n|------|----------|\n| `bin/` | `fd`, `rg` (ELF x86-64 binaries pi bundles for find/grep) + `railway-mcp-vault` (0700 bash wrapper). |\n| `extensions/` | 1 local extension dir: `double-esc-clear/` (index.ts + helper js/test). |\n| `skills/` | 92 symlinks into `~/.claude/skills`, plugin caches, `.agents/skills`, project skills. |\n| `state/` | `pi-hud.json` (`{lastReleaseNotesShown, footerModeTipShownVersion}`). |\n| `npm/node_modules/` | User-scoped installed npm packages (pi-simplify, pi-mcp-adapter, @mistralai, jose, eventsource, express bits, etc.). |\n| `mcp-oauth/` | OAuth state for MCP servers (0700). |\n| `pi-crash.log` | Crash log (24KB). |\n\n---\n\n## Session Storage Layout\n\nLayout: `~/.pi/agent/sessions/--<cwd>--/<ISO-timestamp>_<uuid>.jsonl` where cwd slashes -> `-`. Observed project buckets: `--root--`, `--root-projects-sales-brain--`, `--root-projects-pi-dynamic-workflows-custom--`, `--root-marketing--`, `--root-projects-marketing--` (30 session files total).\n\nFiles are **JSONL, version 3**, tree-structured. First line is the header `{type:\"session\",version:3,id:<uuid>,timestamp,cwd[,parentSession]}`. Subsequent lines extend `SessionEntryBase {type,id(8-hex),parentId,timestamp(ISO)}`. Entry `type`s: `message` (wraps an `AgentMessage` in `.message`), `model_change` (`{provider, modelId}`), `thinking_level_change`, `compaction`, `branch_summary`, `custom`, `custom_message`, `label`, `session_info`. Live-confirmed `message` entry keys: `id, message, parentId, timestamp, type`; assistant `.message` keys: `api, content, errorMessage, model, provider, role, stopReason, timestamp, usage`.\n\n`AgentMessage` union (from `docs/session-format.md`): `UserMessage | AssistantMessage | ToolResultMessage | BashExecutionMessage | CustomMessage | BranchSummaryMessage | CompactionSummaryMessage`. `Usage = {input, output, cacheRead, cacheWrite, totalTokens, cost:{input,output,cacheRead,cacheWrite,total}}`. Stop reasons: `stop|length|toolUse|error|aborted`.\n\nSession-dir precedence: `--session-dir` > `PI_CODING_AGENT_SESSION_DIR` > `settings.sessionDir`. `--no-session` = ephemeral (no file). `--export <file.jsonl> [out.html]` renders a session to HTML.\n\n---\n\n## Resource Discovery & Loading\n\n- **packages** (`settings.packages[]`): npm (`npm:@scope/pkg@ver`), git (`git:host/repo@ref`), raw URL, or absolute/relative local path. User installs -> `~/.pi/agent/npm/`; project -> `.pi/npm/`. String form loads all resources; object form `{source, skills:[...], extensions:[...]}` filters. A package declares resources via `package.json` `pi` key or conventional `skills/`, `extensions/`, etc. dirs.\n- **skills**: discovered from `~/.pi/agent/skills/`, `~/.agents/skills/`, project `.pi/skills/` and `.agents/skills/` (cwd + ancestors up to git root), package `skills/`, and `settings.skills[]`. Dirs containing `SKILL.md` discovered recursively. `--no-skills` disables (explicit `--skill` still loads). `enableSkillCommands` registers `/skill:name`.\n- **extensions**: `settings.extensions[]` + package extensions + `-e/--extension` (repeatable, also one-run `npm:`/`git:`). `--no-extensions` disables discovery.\n- **prompt-templates** & **themes**: `settings.prompts[]`/`settings.themes[]`, package dirs, `--prompt-template`/`--theme` flags; `--no-prompt-templates`/`--no-themes` disable.\n- **context files**: `AGENTS.md` + `CLAUDE.md` auto-discovered unless `--no-context-files`.\n- Resource arrays support glob + `!exclude`, `+force-include`, `-force-exclude`.\n\n---\n\n## Relevant Environment Variables\n\nProvider keys (only needed if not in auth.json): `ANTHROPIC_API_KEY`, `ANTHROPIC_OAUTH_TOKEN`, `OPENAI_API_KEY`, `DEEPSEEK_API_KEY`, `GEMINI_API_KEY`, `OPENROUTER_API_KEY`, `XAI_API_KEY`, `GROQ_API_KEY`, ... (full list in providers table). Cloud: `AWS_*`, `AZURE_OPENAI_*`, `GOOGLE_*`, `CLOUDFLARE_*`.\n\nRuntime/home controls:\n| Env | Effect |\n|-----|--------|\n| `PI_CODING_AGENT_DIR` | Config directory (default `~/.pi/agent`). |\n| `PI_CODING_AGENT_SESSION_DIR` | Session storage dir (below `--session-dir`). |\n| `PI_PACKAGE_DIR` | Override package directory (Nix/Guix). |\n| `PI_OFFLINE` | `1/true/yes` disables startup network ops (same as `--offline`). |\n| `PI_SKIP_VERSION_CHECK` | `1` disables version update check. |\n| `PI_TELEMETRY` | Override install telemetry. |\n| `PI_SHARE_VIEWER_URL` | Base URL for `/share`. |\n\nThis box also relies on `AGENT_VAULT_ADDR` / `AGENT_VAULT_TOKEN` (consumed by `models.json` `!command` and the railway MCP wrapper) — must be present in the subprocess env for deepseek + railway to authenticate.\n\n---\n\n## pi-dynamic-workflows persistence (for visibility tools)\n\nThe fork stores state at `/root/.pi/workflows/`:\n- `model-tiers.json`: `{ tiers: {small,medium,big,cheap,coder,judge,research -> provider/model}, rules:[{name,match,tier}], fallback:{mode:\"error\"} }`. On this box tiers map to deepseek-v4-flash/pro and openai-codex/gpt-5.5.\n- `consent.json`: `{ \"multiAgentWarningAccepted\": true }`.\n- `runs/<runId>.json`: per-run record. Keys observed: `agents, args, completedAt, currentPhase, durationMs, journal, logs, phases, result, runId, script, sessionId, startedAt, status, tokenUsage, updatedAt, workflowName`. Plus `runs/run-<id>.log` text logs and `.bak` snapshots. **These are the files pi-mcp's visibility tools should read.**

**Implicações para pi-mcp:**
- Drive each fan-out subagent with: `pi -p --mode json` plus `--model <provider/id>` (and optional `:thinking` or `--thinking <level>`). Use `--no-session` for ephemeral runs OR `--session-dir <runDir>` / `--session-id <id>` so the visibility tools can later read the persisted JSONL. Add `--no-context-files` to avoid pulling AGENTS.md/CLAUDE.md unless desired, and `--tools <allowlist>` / `--no-tools` to scope tool access per subagent.
- Parse stdout as JSONL: the authoritative final result is the `agent_end` event's `messages[]` (last assistant message gives text content + `provider`, `model`, `api`, `stopReason`, and `usage` with token+cost breakdown). For streaming progress, consume `message_update.assistantMessageEvent` (text_delta/thinking_delta/toolcall_*) and `tool_execution_*`. Always read stderr separately (it stays clean: pi puts the JSON stream on stdout, diagnostics on stderr).
- The subprocess env MUST carry: HOME (so `~/.pi/agent` resolves) or set `PI_CODING_AGENT_DIR=/root/.pi/agent`; PATH including Node and the bundled `~/.pi/agent/bin`; `AGENT_VAULT_ADDR` + `AGENT_VAULT_TOKEN` (required for the deepseek `!command` apiKey in models.json and the railway MCP wrapper); plus any provider API-key env vars not already in auth.json. Without AGENT_VAULT_* the deepseek tier and railway MCP will fail.
- Provider credentials are already configured via auth.json (openai-codex OAuth, deepseek api_key) and models.json — the Go server should NOT pass `--api-key`; let pi resolve credentials itself. Never read/forward auth.json or agent-vault values.
- To let PI decide decomposition + pick MANY models, expose the full pool from `pi --list-models` (264 models; openrouter/openai-codex/deepseek) and respect the existing tier map in /root/.pi/workflows/model-tiers.json. Validate model strings against `--list-models` output before spawning.
- Default model/provider on this box is openai-codex/gpt-5.5 @ xhigh (from settings.json), confirmed by live probe — NOT the help text's 'google'. The MCP should pass an explicit `--model` per subagent rather than relying on the box default, so fan-out is deterministic.
- Session files for visibility tools live at `~/.pi/agent/sessions/--<cwd-with-dashes>--/<ts>_<uuid>.jsonl` (version 3, JSONL, id/parentId tree). To make runs discoverable, set a known `--session-dir` per delegated TASK; then read header + `message` entries (assistant `.message.usage` gives per-call cost/tokens). The pi-dynamic-workflows fork additionally writes run records under `/root/.pi/workflows/runs/<runId>.json` (keys: agents, phases, journal, logs, result, status, tokenUsage, sessionId, ...) — the richest source for a synthesized multi-agent run.
- Prefer one-shot `pi -p --mode json` per subagent over `--mode rpc`: RPC requires strict LF-only JSONL framing (Node readline is non-compliant) and can block on extension UI dialogs (`extension_ui_request`) awaiting stdin. If RPC is used, the Go client must answer or time out those dialogs and disable extensions (`--no-extensions`) to avoid hangs.
- MCP tool availability per run is cached in `~/.pi/agent/mcp-cache.json` (servers.<name>.tools = [{name,description,inputSchema}]); a visibility tool can surface which MCP tools (context7=2, railway=36) a subagent could call. Note railway/context7 may require network + AGENT_VAULT, so consider `--offline` or disabling MCP for hermetic fan-out.

**Questões em aberto:**
- How exactly does the pi-dynamic-workflows-custom fork expose a 'decompose + fan-out + synthesize' entrypoint (a `/workflow` slash command, an extension-registered CLI flag, or a separate binary)? Area A only confirms its persistence at /root/.pi/workflows/; the command surface lives in the fork at /root/projects/pi-dynamic-workflows-custom (other areas).
- Whether `--mode json` ever emits MCP tool errors or extension_error events to stdout vs stderr under failure — the probe was a trivial no-tool run, so failure-path framing (e.g. auth_retry, compaction_end errorMessage) was not exercised live.
- Does setting `--session-id <id>` in a non-interactive `-p` run reliably create a discoverable file under the default sessions dir, and what is the exact bucket name when cwd differs from /root (needed so pi-mcp can locate the file deterministically)?
- Exact semantics of `directTools: true` and `lifecycle: lazy` in mcp.json (tool namespacing/startup behavior) were inferred from config + cache, not from source — confirm in the bundle/fork if pi-mcp will register its own MCP-driven subprocesses.


---

# pi HEADLESS I/O CONTRACT — JSONL streaming envelope, tool events, on-disk session/run schemas, and the workflow tool result format

**Arquivos examinados:** /tmp/pi_json_probe.txt, /tmp/pi_stderr.txt (probe capture, empty), /tmp/tool_stream.jsonl (probe capture), /root/.pi/agent/sessions/--root-projects-pi-dynamic-workflows-custom--/2026-06-07T00-34-48-866Z_019e9f80-f3a1-7b01-a22b-a7a00a08b650.jsonl, /root/.pi/workflows/runs/mq2s4e2i-wzezmw.json, /root/.pi/workflows/runs/mq2u62bu-s1vast.json, /root/.pi/workflows/runs/mq2tzf50-o9tzb5.json, /root/.pi/workflows/runs/mq2xe0wb-lt24zd.json, /root/projects/pi-dynamic-workflows-custom/src/display.ts, /root/projects/pi-dynamic-workflows-custom/src/workflow-tool.ts, /root/projects/pi-dynamic-workflows-custom/src/task-panel.ts, /root/projects/pi-dynamic-workflows-custom/src/builtin-commands.ts

**Fatos-chave:**
- `pi -p --mode json` streams newline-delimited JSON (JSONL) to STDOUT. STDERR is empty on success. Exit code is 0 on success (verified). There is NO top-level wrapper or final summary object — the LAST `turn_end`/`agent_end` lines carry the cumulative usage and the full final assistant message.
- Streaming envelope order for a no-tool turn: `session` -> `agent_start` -> `turn_start` -> user `message_start`/`message_end` -> assistant `message_start` -> many `message_update` -> assistant `message_end` -> `turn_end` -> `agent_end`. Each line has a top-level `type`.
- The FINAL synthesized text is the assistant `message_end`'s `message.content[]` block of `{type:'text', text:..., textSignature:...}`. The `textSignature` JSON contains `"phase":"final_answer"` marking the final answer text block.
- Token/cost totals live in `message.usage` on assistant `message_start`(zeros)/`message_end`/`turn_end`/`agent_end`. Shape: `usage:{input,output,cacheRead,cacheWrite,totalTokens,cost:{input,output,cacheRead,cacheWrite,total}}`. Use the LAST `turn_end` or `agent_end` for final totals. In probe: input=25273, output=41, totalTokens=25314, cost.total=0.127595.
- Assistant message metadata fields: `role`, `content[]`, `api` (e.g. 'openai-codex-responses'), `provider` (e.g. 'openai-codex'), `model` (e.g. 'gpt-5.5'), `usage{...}`, `stopReason` (e.g. 'stop'), `timestamp` (epoch ms), `responseId`. Error turns add `errorMessage` and `diagnostics`.
- `message_update` events wrap an `assistantMessageEvent` whose own `.type` is one of: `thinking_start`, `thinking_end`, `text_start`, `text_delta` (has `.delta`), `text_end`, `toolcall_start`, `toolcall_delta`, `toolcall_end`. Each also carries `.partial` (the cumulative message) and outer `.message` (full snapshot).
- Tool calls in the stream appear TWICE: (1) as `message_update` assistantMessageEvents `toolcall_start`/`toolcall_delta`/`toolcall_end` building a `{type:'toolCall', id, name, arguments}` block inside the assistant message; (2) as standalone top-level `tool_execution_start` {type,toolName,toolCallId,args} and `tool_execution_end` {type,toolName,toolCallId,isError,result:{content:[{type:'text',text:...}]}} lines. The tool's OUTPUT text is in `tool_execution_end.result.content[0].text`.
- The `workflow` tool is invoked by name `workflow` with arguments `{script: <raw JS string>, background?: boolean (default true), args?, maxAgents?, agentTimeoutMs?, tokenBudget?}`. By DEFAULT background:true returns immediately with a run ID and does NOT contain the result; must pass `background:false` to get the synthesized result inline as the tool result.
- Foreground (background:false) workflow tool result text = `✓ Workflow "<name>" finished (<N> agents · <T> tokens · $<C> · <D>s).` followed by `\n```json\n<JSON.stringify(result,null,2)>\n````. The `tool_execution_end`/persisted-toolResult `details` object carries `{runId, tokenUsage, result, durationMs, meta, phases, logs, ...snapshot}` — runId and structured metadata are HERE, not parsed from text. Source: src/workflow-tool.ts:279-302, src/display.ts:332-345.
- Background workflow result is delivered later via a separate injected message (customType 'workflow-result') = `workflowFinishedText(...) + '\n\n' + summarizeResult(result)`; summarizeResult prefers a `verdict`/`report`/`summary` string field else truncated JSON. Source: src/task-panel.ts:27-51, 65-101.
- On-disk session files (/root/.pi/agent/sessions/<--project-->/<TS>_<uuid>.jsonl) use a DIFFERENT envelope than the stream. Line 1: `{type:'session', version:3, id, timestamp, cwd}`. Subsequent lines: `{type, id, parentId, timestamp, message:{...}}` where type is `message` (most), plus session-control types `custom`, `custom_message`, `model_change` {provider,modelId}, `thinking_level_change` {thinkingLevel}.
- In persisted sessions a tool RESULT is its own `message` with `message.role:'toolResult'` and fields `{toolCallId, toolName, content:[{type:'text',text}], isError, timestamp, details?}` — it is NOT nested inside the assistant message. Tool CALLS are `{type:'toolCall', id, name, arguments}` blocks inside an assistant message's `content[]`.
- Workflow run state is persisted at /root/.pi/workflows/runs/<runId>.json (+ .bak). Top-level keys: `runId, workflowName, script, sessionId, status, phases[], agents[], logs[], startedAt, updatedAt`. status ∈ {running, paused, completed, failed, ...}. Each agent: `{id:number, label, phase, prompt, status, model, resultPreview (≤80 chars, ellipsis-truncated), tokens:number, startedAt, endedAt}`. The FULL result is NOT in the run file (only an 80-char resultPreview).
- Per-run models are observable in the run file's `agents[].model` (e.g. 'deepseek/deepseek-v4-pro', 'openai-codex/gpt-5.5') and per-agent `tokens`. In the stream, models appear on assistant `message.model`/`provider`/`api` (the synthesizing/top-level pi model, not necessarily subagent models).
- `--mode rpc` is a JSON-RPC command protocol for embedding (emits `{type:'extension_ui_request',...}` and `{type:'response',command,success,error}`), reading commands from STDIN — NOT a streaming output mode for headless delegation. Use `--mode json` for the MCP.

## AREA B — pi HEADLESS I/O CONTRACT

This documents the exact wire format pi emits under `pi -p --mode json`, the on-disk session/run schemas, and precisely how the `workflow` tool result appears so the Go MCP can extract: final synthesized text, per-run models, token/cost totals, pi runId, and detect "no workflow ran".

All findings are from the fixture `/tmp/pi_json_probe.txt`, a fresh probe (`pi -p --mode json --no-session --thinking off "reply with the single word ok"`), a real 387-line session file, the workflow run JSONs under `/root/.pi/workflows/runs/`, and the fork source in `/root/projects/pi-dynamic-workflows-custom/src/`.

---

### 1. Invocation & process contract

```
pi -p --mode json [--no-session] [--thinking off|...] [--provider P] [--model M] [-t toolA,toolB] "PROMPT"
```

- `--print, -p` — non-interactive: process prompt and exit.
- `--mode <mode>` — `text` (default), `json`, or `rpc`. **Use `json`.**
- `--no-session` — ephemeral, do not persist a session file.
- Output is **JSONL** (one JSON object per line) on **STDOUT**.
- **STDERR is empty on success.** **Exit code = 0** on success (verified: `===EXIT:0===`, empty stderr).
- There is **no envelope wrapper and no single final summary object**. You must scan lines; cumulative usage and the final assistant text are on the **last** `message_end`/`turn_end`/`agent_end` lines.

**`--mode rpc` is NOT a headless output mode.** Probing it (`echo "" | pi --mode rpc -p ...`) produced JSON-RPC framing:
```json
{"type":"extension_ui_request","id":"...","method":"setWidget","widgetKey":"btw"}
{"type":"response","command":"parse","success":false,"error":"Failed to parse command: Unexpected end of JSON input"}
```
It reads commands from STDIN and is meant for embedding pi in another UI. The MCP should use `--mode json`.

---

### 2. Streaming JSONL envelope — top-level `type` values

Observed top-level `type`s (no-tool turn and tool turn):

| top-level `type` | When | Key fields |
|---|---|---|
| `session` | first line | `{type, version:3, id, timestamp, cwd}` *(note: only emitted in stream when a session is created; with `--no-session` the fixture still shows it)* |
| `agent_start` | once | `{type}` only |
| `turn_start` | per turn | `{type}` only |
| `message_start` | start of each user/assistant message | `{type, message:{...}}` |
| `message_update` | streaming deltas (assistant only) | `{type, assistantMessageEvent:{...}, message:{...}}` |
| `message_end` | end of each message | `{type, message:{...}}` (final, with full `usage`) |
| `turn_end` | per turn | `{type, message:{...}, toolResults:[...]}` |
| `agent_end` | once | `{type, messages:[...all...], willRetry:boolean}` |
| `tool_execution_start` | a tool begins | `{type, toolName, toolCallId, args}` |
| `tool_execution_end` | a tool finishes | `{type, toolName, toolCallId, isError, result}` |

Ordering for a **no-tool** turn (from fixture):
```
session → agent_start → turn_start
→ message_start(user) → message_end(user)
→ message_start(assistant, content:[]) → message_update×N → message_end(assistant)
→ turn_end → agent_end
```

Ordering for a **tool-using** turn (observed counts from a `read` probe): multiple turns appear (assistant calls tool → tool executes → assistant continues), so `turn_start`/`turn_end` repeat (3×), `message_start`/`message_end` repeat (6×), interleaved with `tool_execution_start`/`tool_execution_end` (2× each).

---

### 3. The `message` object

#### User message
```json
{"role":"user","content":[{"type":"text","text":"reply with the single word ok"}],"timestamp":1780848921767}
```

#### Assistant message (streaming snapshot fields)
```json
{
  "role":"assistant",
  "content":[ ...blocks... ],
  "api":"openai-codex-responses",
  "provider":"openai-codex",
  "model":"gpt-5.5",
  "usage":{"input":25273,"output":41,"cacheRead":0,"cacheWrite":0,"totalTokens":25314,
           "cost":{"input":0.126365,"output":0.00123,"cacheRead":0,"cacheWrite":0,"total":0.127595}},
  "stopReason":"stop",
  "timestamp":1780848921777,
  "responseId":"resp_..."
}
```
- On assistant `message_start`, `content:[]` and all `usage` numbers are **0** — do NOT read totals here.
- On assistant `message_end` / `turn_end` / `agent_end`, `usage` is the **final cumulative** value for that message.
- Error turns add `errorMessage` (string) and sometimes `diagnostics`.

#### Assistant `content[]` block shapes (streaming)
- **thinking**: `{"type":"thinking","thinking":"...","thinkingSignature":"<JSON string, includes encrypted_content>"}`
- **text**: `{"type":"text","text":"ok","textSignature":"{\"v\":1,\"id\":\"msg_...\",\"phase\":\"final_answer\"}"}`
- **toolCall**: `{"type":"toolCall","id":"call_...","name":"read","arguments":{...}}`

**The final answer text** = the `text` block whose `textSignature` decodes to `"phase":"final_answer"`. In practice it is the last `text` block of the last assistant `message_end`.

---

### 4. `message_update` → `assistantMessageEvent` variants

Each `message_update` carries `assistantMessageEvent` with its own `.type`. Observed variants:

| `assistantMessageEvent.type` | Fields | Meaning |
|---|---|---|
| `thinking_start` | `contentIndex, partial, message` | reasoning block opened |
| `thinking_end` | `contentIndex, content, partial, message` | reasoning block closed (signature populated) |
| `text_start` | `contentIndex, partial, message` | answer text block opened |
| `text_delta` | `contentIndex, delta:"ok", partial, message` | **incremental answer text** (concatenate `.delta`) |
| `text_end` | `contentIndex, content:"ok", partial, message` | answer text block closed |
| `toolcall_start` | `contentIndex, partial, toolCall, message` | tool call block opened |
| `toolcall_delta` | `contentIndex, partial, toolCall?, message` | streaming tool-args JSON |
| `toolcall_end` | `contentIndex, toolCall:{type,id,name,arguments}, partial, type` | tool call finalized |

`.partial` and `.message` both hold the cumulative assistant message; for final text you can ignore deltas and just read the last `message_end`.

---

### 5. Tool calls & tool results in the stream (CRITICAL for extracting the `workflow` result)

A tool round-trip appears as **two independent representations**:

**(a) Inside the assistant message** — a `toolCall` block (built via `toolcall_start/delta/end`):
```json
{"type":"toolCall","id":"call_fgKAnrY1...|fc_...","name":"read","arguments":{"path":"...","limit":2000}}
```

**(b) Standalone top-level lines** — the actual execution + output:
```json
{"type":"tool_execution_start","toolCallId":"call_...|fc_...","toolName":"read","args":{"path":"...","limit":2000}}
{"type":"tool_execution_end","toolCallId":"call_...|fc_...","toolName":"read","isError":false,
 "result":{"content":[{"type":"text","text":"test content 123\n"}]}}
```

**To extract the `workflow` tool's result text from the stream:** find the `tool_execution_end` line where `toolName == "workflow"`, then read `result.content[0].text`. Structured metadata (runId, tokenUsage, full result) is NOT in `result` here in the same way as the persisted `details` — the text is the formatted block (see §7).

> Note: top-level `tool_execution_end.result` carries only `{content:[{type,text}]}` in the stream (observed for `read`). The rich `details{runId,tokenUsage,...}` object is attached to the **persisted toolResult message** and the tool's return value (§7/§8) — for robust runId/cost extraction the MCP should read the **run file** (§9), not only the stream text.

---

### 6. Final totals — where to read them

Use the **last** of these lines (scan to EOF):
- `agent_end` → `.messages[-1].usage` (final assistant message usage) and `.willRetry`.
- `turn_end` → `.message.usage` and `.toolResults`.
- assistant `message_end` → `.message.usage`.

`usage` shape (exact field names):
```
usage.input, usage.output, usage.cacheRead, usage.cacheWrite, usage.totalTokens,
usage.cost.input, usage.cost.output, usage.cost.cacheRead, usage.cost.cacheWrite, usage.cost.total
```
This is the **top-level pi agent's** usage (the orchestrator turn). Per-subagent token usage lives in the workflow run file (`agents[].tokens`) and in the workflow tool's `details.tokenUsage` (§9).

---

### 7. The `workflow` tool result format (the synthesized result)

Source: `src/workflow-tool.ts` and `src/display.ts`.

**Tool input schema** (`workflow-tool.ts`): required `script` (raw JS string, *no markdown fences*); optional `background` (default **true**), `args`, `maxAgents`, `agentTimeoutMs`, `tokenBudget`.

#### 7a. background: true (DEFAULT) — returns immediately, NO result
`workflow-tool.ts:201-210` returns `backgroundStartedText(name, runId)`:
```
Workflow "<name>" started in the background.
Run ID: <runId>
It keeps running on its own. When it finishes, the result is delivered back
here ...
They can also track or cancel it with /workflows status <runId> or /workflows stop <runId>.
```
`details: { runId, background: true }`. **The result is delivered LATER** as an injected message (§7c). For the MCP to get a result inline, it must either (i) drive pi with `background:false`, or (ii) poll the run file (§9).

#### 7b. background: false — result inline (`workflow-tool.ts:279-302`)
```
const header = workflowFinishedText(finalSnapshot);
const formattedResult = result.result !== undefined
  ? `\n```json\n${JSON.stringify(result.result, null, 2)}\n```` : "";
return { content:[{type:"text", text:`${header}${formattedResult}`}],
         details:{...snapshot, meta, phases, logs, result, durationMs, tokenUsage, runId} };
```
`header` (`display.ts:332-345`):
```
✓ Workflow "<name>" finished (<N> agents · <T> tokens · $<C(4dp)> · <D(1dp)>s).
```
So the full text is, e.g.:
```
✓ Workflow "live_widget_demo" finished (5 agents · 168,481 tokens · $0.1525 · 35.0s).
```json
{ "ok": true, "facts": {...}, "verdict": "..." }
```
```
**`details` carries the gold:** `runId`, `tokenUsage{...}`, `result` (the synthesized object/string), `durationMs`, `meta`, `phases`, `logs`, plus snapshot agent list.

#### 7c. background delivery text (`task-panel.ts:41-51, 65-101`)
When a background run completes, `installResultDelivery` injects a message (`customType:"workflow-result"`, `triggerTurn:true, deliverAs:"followUp"`):
```
deliverText(run) = workflowFinishedText(snapshot) + "\n\n" + summarizeResult(run.result.result)
```
`summarizeResult` (`task-panel.ts:27-39`) prefers a string `verdict`/`report`/`summary` field, else a bare string, else JSON truncated to 400 chars + `…(truncated)`. On error it injects `✗ Workflow <runId> failed: <message>`.

#### 7d. Error / abort variants (from real session, `isError:true`)
Short plain strings, e.g. `Workflow scripts must be deterministic: ...`, `Unexpected token (77:78)`, `Workflow was aborted`. Also `workflow-tool.ts:260-263`: if `agentCount === 0` it throws `"workflow scripts must call agent() at least once; this workflow declared phases but did not run any subagents"` — **this is the canonical "no subagents ran" signal.**

> Historical note: a real session also showed an older delivery wrapper (`Workflow **<name>** completed with **11** agent(s).\n\nToken usage: 4,895,841 tokens ($2.3868)\n\n## Result\n...`). This came from the CC-reconstructed build, not the current fork. The MCP parser should tolerate **both** the `✓ Workflow "..." finished (...)` header and a `## Result` / `Token usage:` / `completed with N agent(s)` wrapper.

---

### 8. On-disk SESSION schema (`/root/.pi/agent/sessions/<--project-dir-->/<TS>_<uuid>.jsonl`)

Project dir is the cwd path with `/` → `-` and wrapped in `--...--`. **Different envelope from the stream.**

Line 1: `{"type":"session","version":3,"id":"<uuid>","timestamp":"ISO","cwd":"/..."}`

Other lines (top-level): `{type, id, parentId, timestamp, message:{...}}` for `type:"message"`, plus control events:

| persisted `type` | fields |
|---|---|
| `message` | `id, parentId, timestamp, message{...}` |
| `model_change` | `id, parentId, timestamp, provider, modelId` |
| `thinking_level_change` | `id, parentId, timestamp, thinkingLevel` |
| `custom` | `customType, data, id, parentId, timestamp` |
| `custom_message` | `content, customType, display, id, parentId, timestamp` (this is how `workflow-result`, `effort`, `/workflows` output get persisted) |

**`message.role` values:** `user`, `assistant`, `toolResult`.
- assistant: `{role, content[], api, provider, model, usage{...}, stopReason, timestamp, responseId, errorMessage?, diagnostics?}`
- **toolResult: `{role:"toolResult", toolCallId, toolName, content:[{type:"text",text}], isError, timestamp, details?}`** — a standalone message, NOT nested in assistant.
- assistant `content[]` block types observed: `text`, `thinking`, `toolCall`, `image`. **Tool calls = `toolCall` blocks** `{type:"toolCall", id, name, arguments}` (NOT `tool_use`).

Workflow tool calls in the session: `name:"workflow"`, `arguments:{background, script}` (e.g. `background:false`, `script:"export const meta = {...}"`). Their results are the matching `role:"toolResult"`, `toolName:"workflow"` messages (link via `toolCallId`).

---

### 9. On-disk WORKFLOW RUN schema (`/root/.pi/workflows/runs/<runId>.json` + `.bak`)

runId format: `<base36-ish>-<6char>` e.g. `mq2u62bu-s1vast`. Top-level:
```json
{
  "runId": "mq2u62bu-s1vast",
  "workflowName": "adversarial_model_routing_plan_review_retry",
  "script": "<full JS workflow source>",
  "sessionId": "019e9e8d-0570-7c79-840c-e6b98f2ffa1b",
  "status": "completed",            // also: running | paused | failed
  "phases": ["Review","Synthesis"],
  "agents": [ ... ],
  "logs": ["Logs persisted to /root/.pi/workflows/runs/run-<id>.log"],
  "startedAt": "2026-06-06T20:59:14.634Z",
  "updatedAt": "2026-06-06T21:03:33.267Z"
}
```
Each `agents[]` entry (NOTE: no `tier`/`provider`/`result`/`usage` keys):
```json
{
  "id": 1, "label": "routing correctness", "phase": "Review",
  "prompt": "<full prompt>", "status": "done",      // done | running | error | interrupted
  "model": "deepseek/deepseek-v4-pro",               // <provider>/<modelId>
  "resultPreview": "{\"lens\":\"...\"}",             // ≤80 chars, ellipsis-truncated via preview()
  "tokens": 242006,
  "startedAt": "ISO", "endedAt": "ISO"
}
```
**This is where the MCP reads per-run MODELS** (`agents[].model`, distinct values = the fleet of models pi chose), per-agent `tokens`, and status. Example real fleet: 4× `deepseek/deepseek-v4-pro` (Review phase) + 1× `openai-codex/gpt-5.5` (Synthesis). **The full synthesized result is NOT here** (only an 80-char `resultPreview`); get it from `details.result` / the tool-result text.

A `paused`/early run can have `agents:[]` and `logs:[]`.

---

### 10. Files examined / line refs
- Result header format: `src/display.ts:332-345` (`workflowFinishedText`), `:360-364` (`preview` → 80-char truncation).
- Inline tool result build + `details{runId,tokenUsage,result,...}`: `src/workflow-tool.ts:201-302`; background-started text `:347-358`; tool input schema `:60-110`; "must call agent() at least once" `:260-263`; fence stripping `:429-430`.
- Background delivery: `src/task-panel.ts:27-51` (`summarizeResult`/`deliverText`), `:65-101` (`installResultDelivery`, `complete`/`error` events, `customType:"workflow-result"`).
- `reportText` (built-in/saved commands, `report` field preference): `src/builtin-commands.ts:26-30`, `src/saved-commands.ts:24`.

**Implicações para pi-mcp:**
- PARSE STRATEGY (Go): read STDOUT line-by-line; `json.Unmarshal` each into a struct with `Type string`. Switch on `Type`. Buffer nothing except: (a) the last assistant `message_end`/`turn_end`/`agent_end` for final text+usage, (b) any `tool_execution_end` with `toolName=="workflow"`. Ignore unknown types forward-compatibly. Tolerate the leading `session` line and `--no-session` still emitting it.
- FINAL SYNTHESIZED RESULT: prefer the `workflow` tool result. With `background:false` the text is in the `tool_execution_end.result.content[0].text` (and the assistant `toolCall` arguments confirm `name=="workflow"`). Strip the header line `✓ Workflow "..." finished (...)` and extract the fenced ```json block; if no fence, treat remaining text as the result. ALSO read the run file's `details`-equivalent for the structured `result`. If you only have the top-level agent text (no workflow tool), the final answer is the last assistant `message_end` `content[]` text block whose `textSignature` has `"phase":"final_answer"`.
- PER-RUN MODELS: do NOT rely on the stream's `message.model` (that is the orchestrator model). Instead read `/root/.pi/workflows/runs/<runId>.json` `agents[].model` and take the distinct set — that is the fleet pi actually chose. Also expose `agents[].tokens`, `.status`, `.label`, `.phase` for visibility tools.
- TOKEN/COST TOTALS: two sources. (1) Orchestrator turn totals = last `turn_end`/`agent_end` `message.usage{input,output,cacheRead,cacheWrite,totalTokens,cost{...total}}`. (2) Workflow-internal totals = the header line `(N agents · T tokens · $C · Ds)` AND `details.tokenUsage` / sum of run-file `agents[].tokens`. Prefer `details.tokenUsage.total`/`.cost` when available; the header is a formatted fallback (tokens use thousands separators, cost is 4dp, duration 1dp).
- pi runId: capture from background-started text `Run ID: <runId>` (regex `Run ID: (\S+)`) when background:true, OR from `details.runId` / the tool-result `details` when background:false, OR by listing newest file in `/root/.pi/workflows/runs/*.json`. runId is the key to poll the run file for progress (status running→completed/failed/paused) and to map to a `/workflows status <runId>` style visibility tool.
- DETECT NO WORKFLOW RAN: (a) no `tool_execution_end`/persisted toolResult with `toolName=="workflow"` in the run → pi answered directly without delegating; (b) workflow tool returned `isError:true` with messages like `Workflow scripts must be deterministic...`, `Unexpected token (...)`, `Workflow was aborted`, or `workflow scripts must call agent() at least once...` → a workflow was attempted but produced no fleet; (c) run file `agents:[]` with status `paused`/`failed`. The MCP must FORCE delegation (system prompt instructing pi to always call `workflow` with `background:false`) and then assert at least one `toolName=="workflow"` success before returning.
- BACKGROUND vs FOREGROUND: background is pi's DEFAULT and returns only a runId + 'started' text, with the real result injected into a LATER turn (which a headless `-p` single-shot will NOT wait for). For synchronous MCP delegation, drive pi with an instruction/argument that sets `background:false` so the synthesized result is inline in the same turn; otherwise poll `/root/.pi/workflows/runs/<runId>.json` until `status==completed` and read results from there.
- ROBUSTNESS: exit code 0 + empty stderr is the success signal; treat non-zero exit or non-empty stderr as transport failure. Lines can be very large (encrypted thinking signatures, full scripts) — use a bufio.Scanner with a large/grown buffer (>1MB) or a streaming json.Decoder over the pipe. Cost fields are floats with full precision in JSON (e.g. 0.127595) but only 4dp in the header string — always prefer the numeric `usage.cost`/`details.tokenUsage` over parsing the header.

**Questões em aberto:**
- With `--no-session` the stream still emits a `session` line (version 3, with id/cwd); unclear whether a session file is actually written to disk in that mode — the MCP's run-file polling for runId works regardless since workflow runs persist separately under /root/.pi/workflows/runs/.
- In the streaming `tool_execution_end`, only `result.content[0].text` was confirmed for the `read` tool; I did not capture a live `background:false` workflow run in `--mode json` to confirm whether the rich `details{runId,tokenUsage,...}` object also rides on the stream's `tool_execution_end` (it is definitely on the persisted toolResult and the tool return). The MCP should treat the run file as the authoritative source for runId/tokenUsage.
- The exact pi/orchestrator behavior when a background workflow is launched under single-shot `-p` (does pi exit before delivery, leaving only the 'started' text?) was inferred from code, not directly timed; should be validated by an end-to-end probe that launches a real background workflow and watches both stdout and the run file.
- Whether `status` in the run file has additional terminal values beyond {running, paused, completed, failed} (e.g. 'aborted'/'stopped') — task-panel.ts references 'stopped'/'paused'/'resumed' events; the run-file status enum should be confirmed against workflow-manager.ts.


---

# runWorkflow() engine: VM realm, injected globals, model routing, resume/checkpoint, budgets, structured output

**Arquivos examinados:** /root/projects/pi-dynamic-workflows-custom/src/workflow.ts, /root/projects/pi-dynamic-workflows-custom/src/structured-output.ts, /root/projects/pi-dynamic-workflows-custom/src/index.ts, /root/projects/pi-dynamic-workflows-custom/src/agent.ts, /root/projects/pi-dynamic-workflows-custom/src/model-routing.ts, /root/projects/pi-dynamic-workflows-custom/src/model-routing-policy.ts, /root/projects/pi-dynamic-workflows-custom/src/model-tier-config.ts, /root/projects/pi-dynamic-workflows-custom/src/agent-registry.ts, /root/projects/pi-dynamic-workflows-custom/src/config.ts, /root/projects/pi-dynamic-workflows-custom/src/errors.ts

**Fatos-chave:**
- Workflow scripts are JS module source. parseWorkflowScript() requires the FIRST statement to be `export const meta = {...}` with object-LITERAL-only values (no interpolation, no spread, no computed keys); meta is AST-evaluated (acorn), not executed. The rest of the script is sliced out and run as the body.
- Scripts run in a Node `vm` realm (vm.createContext + new vm.Script(...).runInContext). The body is wrapped as `${DETERMINISM_PRELUDE}\n(async () => {\n${body}\n})()` so top-level await works and the whole script awaits to completion. vm is explicitly NOT a security sandbox (comments say an injected bridge fn's `.constructor` is still the host Function).
- Determinism neutering: at PARSE time a regex DETERMINISM_BLOCKLIST rejects scripts literally containing Date.now()/Math.random()/new Date(). At RUNTIME the DETERMINISM_PRELUDE (run before the user body, inside the realm) replaces Math.random (throws), and wraps Date so Date(), new Date() with no args, and Date.now() all throw, while new Date(arg)/Date.UTC/Date.parse still work. Reason: nondeterminism would break the resume journal (a re-run would produce values differing from the cached entries).
- Globals injected into every script: agent, parallel, pipeline, workflow, verify, judgePanel, loopUntilDry, completenessCheck, retry, gate, checkpoint, log, phase, args, cwd, process (frozen, only .cwd()), budget, console (mapped to log). Object/Array/JSON/Math/Date/Promise/Set/Map come from the realm itself; host built-ins are deliberately NOT injected.
- agent(prompt, AgentOptions) is the core primitive. AgentOptions: label?, phase?, schema? (typebox TSchema), model?, tier?, isolation?: 'worktree', agentType?, timeoutMs?. It reserves a slot synchronously (atomic agentCount++ gate against maxAgents/budget), assigns a monotonic callIndex (state.callSeq++), hashes the call identity, checks the resume journal, then runs inside a concurrency limiter.
- Concurrency cap: limiter defaults to min(navigator.hardwareConcurrency-2 || 8-2, MAX_CONCURRENCY=16), floored at 1; overridable via options.concurrency. Agent count cap: MAX_AGENTS_PER_RUN=1000 (override maxAgents). Per-agent timeout default DEFAULT_AGENT_TIMEOUT_MS = 5*60*1000 (override agentTimeoutMs or per-call AgentOptions.timeoutMs).
- Model resolution order (resolveModelRoute in model-routing-policy.ts): explicit-model > agent-type-model > rule-model > rule-tier > explicit-tier > phase-model > default-tier ('medium') > fallback-main-model. Strict mode is enabled when model-tiers.json has fallback.mode === 'error'; in strict mode an unresolved route throws MODEL_ROUTING_ERROR (non-recoverable).
- Per-phase model routing: parseModelRoutingFromMeta(meta.phases, meta.model) builds a ModelRoutingConfig (routes from phases[].model with exact case-sensitive title match, defaultModel from meta.model). resolveModelForPhase(phase, config) returns the phase's model (regex match supported per-route via useRegex), used as the 'phaseModel' input to the policy.
- Resume uses longest-unchanged-prefix replay keyed by callIndex. hashAgentCall sha256s {prompt, model, tier, phase, agentType, agentDef(tools/model/prompt JSON), schema}. A cached entry replays ONLY while callIndex < state.firstMiss AND hash matches; the first miss sets firstMiss = min(firstMiss, callIndex), so that call and ALL later calls run live (an edited upstream call invalidates downstream results).
- checkpoint(promptText, CheckpointOptions) is a deterministic, journaled human gate that spends no tokens (gated on agent counter + abort, not budget). It hashes {promptText, kind, choices}, replays the journaled human reply on resume. Headless (no options.confirm threaded in): takes CheckpointOptions.default (or true) unless headless==='abort' (then throws WORKFLOW_ABORTED) — so a detached/background run never hangs.
- Token budget is a SOFT gate: spent accrues AFTER each agent (recordTokens), so an in-flight wave may overshoot; subsequent agent() calls then throw TOKEN_BUDGET_EXHAUSTED. budget global exposes total/spent()/remaining(). Per-phase soft sub-budgets via phase(title, {budget}) warn once at 80%, throw at 100%, without touching the run total.
- structured-output.ts createStructuredOutputTool returns a terminating tool (terminate:true) whose params Pi validates against the typebox schema before execute() runs; the captured value becomes the subagent result. agent.ts resolveStructuredOutput re-prompts up to maxSchemaRetries (default 2, tools restricted to structured_output), then strict prose extraction via extractValidated, else throws SCHEMA_NONCOMPLIANCE (non-recoverable).
- Nested workflow(nameOrScript, args) runs a saved workflow or raw script inline, sharing the parent's SharedRuntime (limiter/agentCount/spent/tokenUsage), ONE level deep only (shared.depth>=1 throws). Nested runs never inherit the parent's resume journal and don't persist logs.
- SharedRuntime is the cross-nesting state: limiter, agentCount, spent, tokenUsage {input,output,total,cost,cacheRead,cacheWrite}, depth. It is created once at the top run and passed down via options.sharedRuntime so caps/budget hold across nesting.
- WorkflowAgent.run() creates a real pi SDK agent session (createAgentSession with SessionManager.inMemory() but a REAL SettingsManager.create so the user's default provider/model is inherited — using inMemory settings would fall back to a possibly-unauthed first model). Real token/cost usage is read via session.getSessionStats() before dispose; tool calls captured via extractToolCalls. Abort wired through session.abort().

## Area C — Workflow Script Runtime

Files: `/root/projects/pi-dynamic-workflows-custom/src/workflow.ts` (1077 lines), `src/structured-output.ts` (47), `src/index.ts` (113). Cross-referenced: `src/agent.ts`, `src/model-routing.ts`, `src/model-routing-policy.ts`, `src/model-tier-config.ts`, `src/agent-registry.ts`, `src/config.ts`, `src/errors.ts`.

This subsystem is the deterministic, resumable VM that *executes a workflow script* (the thing pi/an LLM authors) and fans it out into many subagent sessions. It is exactly what pi-mcp must drive.

---

### 1. `runWorkflow()` — top-level signature

```ts
export async function runWorkflow<T = unknown>(
  script: string,
  options: WorkflowRunOptions = {},
): Promise<WorkflowRunResult<T>>
```

Flow: `parseWorkflowScript(script)` → `parseModelRoutingFromMeta` + `loadModelTierConfig` → init `RuntimeState` → build the `WorkflowAgent` runner → compute concurrency limiter + `SharedRuntime` → define all injected globals → `vm.createContext(...)` → run `DETERMINISM_PRELUDE + (async()=>{ body })()` in-realm → persist logs → emit final token usage → return `WorkflowRunResult`.

```ts
const wrapped = `${DETERMINISM_PRELUDE}\n(async () => {\n${body}\n})()`;
const result = await new vm.Script(wrapped, { filename: `${meta.name || "workflow"}.js` }).runInContext(context);
```

The script's *return value* (the resolved value of the async IIFE) becomes `result`.

#### `WorkflowRunOptions` (extends `WorkflowAgentOptions`)

```ts
export interface WorkflowRunOptions extends WorkflowAgentOptions {
  args?: unknown;                       // -> global `args`
  agent?: Pick<WorkflowAgent, "run">;   // inject a runner (tests)
  mainModel?: string;                   // session's main model (provider/id)
  agentRegistry?: AgentRegistry;        // snapshotted once per run; default scans .pi/agents
  concurrency?: number;
  tokenBudget?: number | null;
  signal?: AbortSignal;
  maxAgents?: number;                   // default MAX_AGENTS_PER_RUN = 1000
  agentTimeoutMs?: number;              // default 5 min
  persistLogs?: boolean;                // default true
  runId?: string;                       // default `run-${started.toString(36)}`
  resumeJournal?: Map<number, JournalEntry>;
  resumeFromRunId?: string;
  onAgentJournal?: (entry: JournalEntry) => void;   // persist each live result
  sharedRuntime?: SharedRuntime;        // internal: inherited by nested workflow()
  loadSavedWorkflow?: (name: string) => string | undefined;  // enables workflow('name')
  confirm?: (promptText: string, options: CheckpointOptions) => Promise<unknown>; // checkpoint UI
  onLog?: (message: string) => void;
  onPhase?: (title: string) => void;
  onAgentStart?: (event: { label; phase?; prompt; model?; callIndex }) => void;
  onAgentEnd?: (event: { label; phase?; result; tokens?; worktree?; model?; toolCalls? }) => void;
  onTokenUsage?: (usage: { input; output; total; cost; cacheRead?; cacheWrite? }) => void;
}
```

`WorkflowAgentOptions` (inherited, from `agent.ts`):

```ts
export interface WorkflowAgentOptions {
  cwd?: string;
  tools?: ToolDefinition[];                  // extra tools beyond structured_output
  session?: Partial<CreateAgentSessionOptions>;  // override createAgentSession (model, authStorage, ...)
  instructions?: string;                     // system guidance prepended to every subagent
  mainModel?: string;                        // fallback for tier resolution
}
```

#### `WorkflowRunResult<T>`

```ts
export interface WorkflowRunResult<T = unknown> {
  meta: WorkflowMeta;
  result: T;
  logs: string[];
  phases: string[];
  agentCount: number;
  durationMs: number;
  runId?: string;
  tokenUsage?: { input; output; total; cost; cacheRead?; cacheWrite? };
}
```

#### `SharedRuntime` (cross-nesting global resources)

```ts
export interface SharedRuntime {
  limiter: <T>(fn: () => Promise<T>) => Promise<T>;
  agentCount: number;
  spent: number;
  tokenUsage: { input; output; total; cost; cacheRead; cacheWrite };
  depth: number;
}
```
Created once at the top run; passed to nested `workflow()` so the 16-concurrent / 1000-total caps and the token budget hold across nesting.

#### `JournalEntry` (resume cache)

```ts
export interface JournalEntry { index: number; hash: string; result: unknown; }
```

---

### 2. The sandbox / VM realm

- Uses Node `vm` (`import vm from "node:vm"`). `vm.createContext({...globals})` then `new vm.Script(wrapped).runInContext(context)`.
- **Not a security boundary.** Verbatim comment: *"vm is not a security sandbox — an injected bridge function's `.constructor` is still the host Function, so a determined script could bypass this."* The guard is best-effort against ACCIDENTAL nondeterminism in *trusted* (user / guided-LLM) scripts.
- Host built-ins are **deliberately not injected**. `Object/Array/JSON/Math/Date/Promise/Set/Map` come from the realm itself precisely because their `.constructor` would otherwise be the host `Function` (a determinism-guard bypass).

#### Determinism neutering (two layers)

1. **Parse-time author hint** — `parseWorkflowScript` rejects with `SCRIPT_VALIDATION_ERROR` if the source text matches:
   ```ts
   const DETERMINISM_BLOCKLIST = /\bDate\s*\.\s*now\b|\bMath\s*\.\s*random\b|\bnew\s+Date\s*\(\s*\)/;
   ```
2. **Runtime enforcement** — `DETERMINISM_PRELUDE` runs in-realm before the body:
   - `Math.random` → throws.
   - `Date` is replaced by `SafeDate`: `Date()` (no `new.target`) throws; `new Date()` with zero args throws; `Date.now()` throws. `new Date(arg)` works via `Reflect.construct(RealDate, a, SafeDate)`. `SafeDate.UTC`/`.parse`/`.prototype` preserved.
   - Uses the realm's own `Reflect`/`Date` (not host objects), so it adds no host-`Function` escape.

**Why:** the resume journal caches `agent()` results by a deterministic `callIndex`. If `Math.random()`/`Date.now()` were allowed, a re-run would produce different values than the journal → broken replay. Authors are told to pass randomness/timestamps via `args` or vary by index.

---

### 3. Every injected global (exact semantics)

| Global | Signature | Semantics |
|---|---|---|
| `agent` | `agent(prompt: string, opts?: AgentOptions): Promise<result>` | Core primitive (see §4). Reserves a slot, journals, runs a subagent session. |
| `parallel` | `parallel(thunks: Array<() => Promise<unknown>>): Promise<unknown[]>` | `Promise.all` over thunks. **Must be thunks, not promises** (TypeError otherwise). Recoverable failures → `null` in the array + log; **non-recoverable (budget/agent-limit) rethrow** to halt the run. |
| `pipeline` | `pipeline(items: unknown[], ...stages: Array<(prev, original, index) => unknown>): Promise<unknown[]>` | Per-item sequential stage chain, all items in parallel. Same recoverable→null / non-recoverable→throw policy; aborts checked between stages. |
| `workflow` | `workflow(nameOrScript: string, childArgs?: unknown): Promise<childResult>` | Nested run. Resolves via `loadSavedWorkflow` else treats arg as raw script. **One level deep** (`shared.depth>=1` throws `SCRIPT_VALIDATION_ERROR`). Shares `SharedRuntime`; no resume journal inheritance; `persistLogs:false`; `runId` = `${runId}-nested${depth}`. |
| `phase` | `phase(title: string, opts?: { budget?: number }): void` | Sets `currentPhase`, appends to phases list, optionally carves a soft per-phase sub-budget (re-bases on re-declare). Calls `onPhase`. |
| `log` | `log(message): void` | Pushes to `state.logs` + logger. `console.log/info` map to `log`; `console.warn/error` prefix `[warn]`/`[error]`. |
| `budget` | frozen `{ total: number|null, spent(): number, remaining(): number }` | Reads `shared.spent`. `remaining()` is `Infinity` when no `tokenBudget`. |
| `args` | `options.args` (any) | The caller-supplied task input. |
| `cwd` | `string` | `options.cwd ?? process.cwd()`. |
| `process` | frozen `{ cwd: () => string }` | Only `.cwd()`; no real `process`. |
| `console` | `{ log, info, warn, error }` | All routed to `log`. |
| `checkpoint` | `checkpoint(promptText, opts?: CheckpointOptions): Promise<reply>` | Deterministic journaled human gate (see §7). |
| `verify` | `verify(item, { reviewers?=2, threshold?=0.5, lens?: string|string[] }): Promise<{ real, realCount, total, votes }>` | Adversarial multi-reviewer vote via `parallel`+`agent` with `VERIFY_SCHEMA` `{real:boolean, reason?}`. `real` if `realCount/total >= threshold`. |
| `judgePanel` | `judgePanel(attempts[], { judges?=3, rubric? }): Promise<best>` | Each attempt scored by N judges (`JUDGE_SCHEMA` `{score:number, reason?}`); returns highest mean score, stable tie-break by input index. |
| `loopUntilDry` | `loopUntilDry({ round:(i)=>items[]|Promise, key?, consecutiveEmpty?=2, maxRounds?=50 }): Promise<all[]>` | Repeats `round` until `consecutiveEmpty` rounds yield no fresh items (dedup by `key`). Budget/agent-limit exhaustion breaks and returns partial. |
| `completenessCheck` | `completenessCheck(taskArgs, results): Promise<{ complete, missing? }>` | Single agent w/ `COMPLETENESS_SCHEMA`; lists gaps (results truncated to 4000 chars). |
| `retry` | `retry(thunk:(attempt)=>..., { attempts?=3, until?:(r)=>boolean }): Promise<last>` | Bounded retry; each attempt is a real `agent()` so it auto-journals. No backoff (no timers in vm). |
| `gate` | `gate(thunk:(feedback,attempt)=>..., validator:(r)=>{ok,feedback?}, { attempts?=3 }): Promise<{ok,value,attempts}>` | Validation gate; validator feedback fed into the next attempt. |

**Concurrency / barrier behavior**: there is exactly ONE limiter (per `SharedRuntime`). `agent()` reserves its slot (`agentCount++`) and assigns `callIndex` synchronously *before* entering the limiter, so a `parallel()` fan-out gets stable, reproducible call indices and cannot collectively overshoot `maxAgents`. The limiter (`createLimiter(limit)`) is a simple FIFO queue: when `active >= limit`, callers await a queued resolver; `next()` decrements and shifts the queue. `parallel`/`pipeline` are pure `Promise.all` — the *barrier* is the limiter, not the combinator. Token budget is a **soft** post-hoc gate (in-flight wave can overshoot, later calls throw).

---

### 4. `AgentOptions` and how each field flows

```ts
export interface AgentOptions<TSchemaDef extends TSchema | undefined = ...> {
  label?: string;       // display label; default `${phase} agent ${n}` or `agent ${n}`
  phase?: string;       // overrides currentPhase for this call
  schema?: TSchemaDef;  // typebox schema -> structured_output tool
  model?: string;       // explicit provider/modelId or bare id (highest precedence)
  tier?: string;        // coarse tier from model-tiers.json
  isolation?: "worktree";  // run in an isolated git worktree
  agentType?: string;   // named .pi/agents/<name>.md definition (tools+model+prompt)
  timeoutMs?: number;   // per-call timeout override
}
```

Flow into a single `agent()` call:
1. `assignedPhase = opts.phase ?? state.currentPhase`.
2. `agentDef = resolveAgentType(opts.agentType, registry)`; unknown name → warn + fall back.
3. `phaseModel = resolveModelForPhase(assignedPhase, routingConfig)`.
4. `routeDecision = resolveModelRoute({ explicitModel: opts.model, explicitTier: opts.tier, agentType, agentTypeModel: agentDef?.model, phase, label, phaseModel, mainModel }, tierConfig)`.
5. `callIndex = state.callSeq++`; `callHash = hashAgentCall(prompt, modelSpec, phase, opts, agentDefinitionKey(agentDef))`.
6. `shared.agentCount++` (atomic with the limit/budget gate); label computed.
7. Resume check (journal replay if hash matches & `callIndex < firstMiss`).
8. Enter limiter → optional worktree → `agentRunner.run(prompt, AgentRunOptions{...})` with timeout → record tokens → `onAgentJournal` + `onAgentEnd`.

The `AgentRunOptions` passed to `WorkflowAgent.run`:
```ts
{ label, schema, signal, instructions: buildAgentInstructions(phase, opts, agentDef),
  model: modelSpec, tier: undefined, strictModelResolution: routeDecision?.strict ?? false,
  routeSource: [source, tier, ruleName].filter(Boolean).join(":"),
  toolNames: agentDef?.tools, disallowedToolNames: agentDef?.disallowedTools,
  cwd: runCwd, onModelResolved, onModelFallback, onUsage, onToolCalls }
```
Note `tier` is forced to `undefined` here — tier→model is already collapsed into `modelSpec` by the route policy, so the runner only sees a concrete model.

`buildAgentInstructions`: if `agentDef.prompt` exists, prepend it; else if `agentType` set, prepend prose hint `Act as workflow subagent type: X`. Then `Workflow phase: X` and `Requested isolation: X`. `model` is applied for real via the session, never as prose.

`isolation: "worktree"` → `createWorktree(baseCwd, `${runId}-${callIndex}-${label}`)` (deterministic name → stable resume keys); if not isolated, logs the reason and runs in-place; always torn down in `finally`.

---

### 5. Model resolution — the full chain

Two layers compose:

**(a) `resolveModelRoute(context, config)` (model-routing-policy.ts)** — `ModelRouteSource` precedence:

```
explicit-model        context.explicitModel (AgentOptions.model)
agent-type-model      agentDef.model
rule-model            config.rules[].model (matched by agentType > phase > label)
rule-tier             config.rules[].tier -> tiers[tier]
explicit-tier         AgentOptions.tier -> tiers[tier]
phase-model           meta.phases[].model (or meta.model default) via resolveModelForPhase
default-tier          tiers["medium"]
fallback-main-model   options.mainModel (only when NOT strict)
```
Returns `ModelRouteDecision { modelSpec, source, tier?, ruleName?, strict }`. `strict = config.fallback.mode === "error"`. In strict mode: an unresolved route, an unknown referenced tier, or a rule defining both `model`+`tier` → `WorkflowError(MODEL_ROUTING_ERROR, recoverable:false)`. Rule matching: `match.{agentType|phase|label}` compared by exact string OR a `/regex/flags` literal.

**(b) `WorkflowAgent.run` → `resolveAgentModelSpec` + `resolveWorkflowAgentModel` (agent.ts)** — the runner re-resolves `{model, tier}` (here `model` already = `modelSpec`, `tier` undefined). `resolveModel(spec)`: `provider/modelId` → `registry.find`; bare id → prefer auth-configured (`getAvailable()`) else any (`getAll()`). Unresolvable + `strictModelResolution` → throw; else warn + `onModelFallback` + use session default. The `ModelRegistry`/`AuthStorage` are built from `getAgentDir()` so resolved models carry valid credentials.

**Per-phase routing helpers** (model-routing.ts):
- `parseModelRoutingFromMeta(phases?, defaultModel?) -> { defaultModel, routes: [{phasePattern: title, model}] }` (one route per phase that sets a `model`).
- `resolveModelForPhase(phase, config)`: exact case-sensitive title match (or per-route `useRegex` case-insensitive); falls back to `config.defaultModel` (= `meta.model`).

`ModelTierConfig` (model-tier-config.ts): `{ tiers: Record<string,string>, rules?: ModelTierRule[], fallback?: { mode?: "session"|"error" } }`. Loaded from `~/.pi/workflows/model-tiers.json` (`MODEL_TIERS_FILE`). `null` if absent; **invalid configs throw** (so strict routing can't be silently disabled by a typo). Default config points small/medium/big all at the current model.

---

### 6. meta parsing + per-phase routing

`parseWorkflowScript(script): { meta, body }`:
- Rejects nondeterministic source (blocklist).
- Parses with acorn (`ecmaVersion:"latest"`, `sourceType:"module"`, `allowAwaitOutsideFunction`, `allowReturnOutsideFunction`).
- Requires `ast.body[0]` to be `ExportNamedDeclaration` whose declaration is `const meta = <literal>` (exactly one declarator named `meta`).
- `evaluateLiteral` walks the AST and only allows: `ObjectExpression` (no spread/computed/methods/accessors; rejects `__proto__`/`constructor`/`prototype` keys), `ArrayExpression` (no sparse/spread), `Literal`, `TemplateLiteral` (NO interpolation), and negative-number `UnaryExpression`. Anything else throws → `SCRIPT_VALIDATION_ERROR`. **meta is statically evaluated, never executed.**
- `validateMeta`: `name` and `description` must be non-empty strings; `model?` string; `phases?` array of objects each with a `title` string.
- `body = script.slice(0, first.start) + script.slice(first.end)` — the meta export is excised; the remainder runs.

```ts
export interface WorkflowMeta { name; description; phases?: WorkflowMetaPhase[]; model?; }
export interface WorkflowMetaPhase { title; detail?; model?; }
```

When `meta.phases[0].title` exists, `state.currentPhase` defaults to it (so pre-`phase()` agents group under a declared phase, not `(no phase)`).

---

### 7. Checkpoint / resume + callIndex hashing

**callIndex**: `state.callSeq` is a monotonic counter incremented at *lexical* `agent()`/`checkpoint()` call time, **before** the limiter — so `parallel`/`pipeline` fan-out gets reproducible indices.

**Resume (longest-unchanged-prefix)**:
- `hashAgentCall` sha256 of `{ prompt, model, tier, phase, agentType, agentDef(=agentDefinitionKey JSON of tools/model/prompt), schema }`.
- For each call: `cached = resumeJournal.get(callIndex)`; if `cached.hash === callHash && callIndex < state.firstMiss` → replay `cached.result` (fires `onAgentStart`/`onAgentEnd` with `tokens:0`, no live run).
- On any miss (no entry or hash changed): `firstMiss = min(firstMiss, callIndex)` — that call AND every later call run **live**. So editing an upstream call cache-miss-cascades all downstream calls (matching Claude Code's contract; no stale results).
- Live results are emitted via `onAgentJournal({ index, hash, result })` so the caller persists them.

**`checkpoint()`** (deterministic human gate; spends no tokens, gated on agent counter + abort, not budget):
```ts
checkpoint(promptText: string, opts?: CheckpointOptions): Promise<reply>
export interface CheckpointOptions {
  default?: unknown;                         // headless reply
  headless?: "default" | "abort";            // default "default"
  kind?: "confirm" | "input" | "select";     // affects hash + UI
  choices?: string[];                        // for kind "select"
  timeoutMs?: number;
}
```
- `hashCheckpoint` = sha256 of `{ promptText, kind, choices }`.
- Resume: replays the journaled human reply by callIndex exactly like a cached agent.
- Live: if `options.confirm` is threaded in → `await confirm(promptText, opts)`; else if `headless === "abort"` → throw `WORKFLOW_ABORTED`; else `reply = opts.default ?? true` (and journal THAT). **This is the headless-safety mechanism: a background/detached run never hangs on a checkpoint.** Critical for pi-mcp's headless driving.

---

### 8. tokenBudget, phase sub-budgets, concurrency cap

- **Global budget** (`options.tokenBudget`): SOFT. `budget.remaining()` checked before each agent; `<=0` → throw `TOKEN_BUDGET_EXHAUSTED` (non-recoverable). `recordTokens` accrues `shared.spent` *after* the agent finishes (success or error). Real usage from `session.getSessionStats()`; falls back to `estimateTokens = ceil(JSON.stringify(value).length/4)` when provider reports `total === 0`.
- **Per-phase sub-budget** (`phase(title, {budget})`): carves `{budget, startSpent: shared.spent, warned:false}`. Each agent in that phase computes `phaseSpent = shared.spent - startSpent`; `>= budget` → throw `TOKEN_BUDGET_EXHAUSTED`; warns once at ≥80%. Re-declaring re-bases from current spent (idempotent across resume). Soft, so a phase can overshoot slightly.
- **Concurrency**: `concurrency = max(1, min(options.concurrency ?? max(1, (navigator.hardwareConcurrency ?? 8) - 2), MAX_CONCURRENCY))`, `MAX_CONCURRENCY = 16`.
- **Agent count**: `maxAgents ?? MAX_AGENTS_PER_RUN (1000)`. `agent()` and `checkpoint()` both throw `AGENT_LIMIT_EXCEEDED` (non-recoverable) when `shared.agentCount >= maxAgents`.

Constants (config.ts): `MAX_AGENTS_PER_RUN=1000`, `DEFAULT_AGENT_TIMEOUT_MS=300000`, `MAX_CONCURRENCY=16`, `DEFAULT_TOKEN_BUDGET=null`, `WORKFLOW_RUNS_DIR=".pi/workflows/runs"`, `MODEL_TIERS_FILE=".pi/workflows/model-tiers.json"`, `AGENTS_DIR=".pi/agents"`.

---

### 9. Structured-output validation (`structured-output.ts` + agent.ts)

```ts
export function createStructuredOutputTool<TSchemaDef extends TSchema>({
  schema, capture, name = "structured_output",
}: StructuredOutputToolOptions<TSchemaDef>): ToolDefinition<TSchemaDef, Static<TSchemaDef>>
```
Defines a tool whose `parameters = schema`. **Pi validates `params` against the typebox `schema` before `execute()` runs.** `execute` sets `capture.value = params; capture.called = true;` and returns `{ content:[{type:"text",text:"Structured output received."}], details: params, terminate: true }` — `terminate:true` lets the subagent finish without an extra assistant turn.

```ts
export interface StructuredOutputCapture<T> { value: T | undefined; called: boolean; }
export interface StructuredOutputToolOptions<TSchemaDef extends TSchema> {
  schema: TSchemaDef; capture: StructuredOutputCapture<Static<TSchemaDef>>; name?: string;
}
```

Resolution path (`resolveStructuredOutput`, agent.ts): if `capture.called` → return value. Else restrict tools to `["structured_output"]` and re-prompt up to `maxSchemaRetries` (default 2). If still not called → `extractValidated(lastAssistantText, schema)`: find a fenced ```json``` block or first balanced `{}`/`[]`, `JSON.parse`, `Convert(schema, parsed)`, accept only if `Check(schema, converted)` passes (logs a warning). Else throw `WorkflowError(SCHEMA_NONCOMPLIANCE, recoverable:false)` — never a silent null. The schema tool is always appended **after** the agentType allowlist (`applyToolPolicy`), so a restrictive allowlist can't strip it.

---

### 10. Error semantics (errors.ts)

`WorkflowErrorCode`: `AGENT_TIMEOUT`, `WORKFLOW_ABORTED`, `AGENT_LIMIT_EXCEEDED`, `TOKEN_BUDGET_EXHAUSTED`, `SCRIPT_VALIDATION_ERROR`, `SCHEMA_NONCOMPLIANCE`, `MODEL_ROUTING_ERROR`, `AGENT_EXECUTION_ERROR`, `PERSISTENCE_ERROR`, `UNKNOWN`. `WorkflowError` carries `{ code, recoverable, agentLabel?, details? }`. In `agent()`, a **recoverable** error (timeout, generic execution, abort) returns `null` for that agent; **non-recoverable** (budget/agent-limit/schema/model-routing/validation) rethrows and halts. `wrapError` classifies unknown errors: abort→`WORKFLOW_ABORTED(recoverable)`, timeout→`AGENT_TIMEOUT(recoverable)`, else `AGENT_EXECUTION_ERROR(recoverable)`.

`withTimeout(promise, ms, message)` races against a `setTimeout` that rejects with `AGENT_TIMEOUT(recoverable)`.

---

### 11. `index.ts` public exports (what pi-mcp can import)

Re-exports the whole library surface. Workflow-runtime-relevant ones:
- `runWorkflow`, `parseWorkflowScript` (from `workflow.js`).
- Types: `AgentOptions`, `JournalEntry`, `SharedRuntime`, `WorkflowMeta`, `WorkflowMetaPhase`, `WorkflowRunOptions`, `WorkflowRunResult`.
- `WorkflowAgent`, `listAvailableModelSpecs`, `resolveWorkflowAgentModel`; types `AgentRunOptions`, `AgentRunResult`, `WorkflowAgentModelResolutionOptions`, `WorkflowAgentOptions`.
- Agent registry: `applyToolPolicy`, `listAgentTypes`, `loadAgentRegistry`, `resolveAgentType`; types `AgentDefinition`, `AgentRegistry`.
- Model routing: `parseModelRoutingFromMeta`, `resolveModelForPhase` (+ types `ModelRoute`, `ModelRoutingConfig`); `isStrictFallback`, `matchRouteRule`, `resolveModelRoute` (+ `ModelRouteContext`, `ModelRouteDecision`, `ModelRouteSource`).
- Model tiers: `buildDefaultTierConfig`, `getModelTierConfigPath`, `loadModelTierConfig`, `resolveTierModel`, `saveModelTierConfig`, `sortedTierNames` (+ config types).
- Errors: `WorkflowError`, `WorkflowErrorCode`, `wrapError`, `isWorkflowError`, `isAbortError`, `isTimeoutError`.
- Structured output: `createStructuredOutputTool` (+ `StructuredOutputCapture`, `StructuredOutputToolOptions`).
- Persistence: `createRunPersistence`, `generateRunId` (+ `PersistedRunState`, `RunPersistence`, `RunStatus`) — relevant for pi-mcp visibility tools.
- Higher-level: `WorkflowManager`/`ManagedRun`, `createWorkflowTool`/`WorkflowToolInput`/`WorkflowToolOptions`/`backgroundStartedText`, saved-workflow/registration helpers, display/snapshot helpers, web tools, task-panel, generators (`generateDeepResearchWorkflow`, etc.). These belong to adjacent areas but are part of the same package.

**Implicações para pi-mcp:**
- pi-mcp's headless driving is directly supported: omit `confirm` so checkpoint() takes its declared default and journals it — a background run NEVER hangs on human input (unless a checkpoint sets headless:'abort'). This is the single most important headless-safety contract to rely on.
- To run a fleet, pi-mcp does NOT decompose the task itself — it hands a workflow SCRIPT to runWorkflow() and pi's script (authored by an LLM) calls agent()/parallel()/pipeline() to fan out across many models. The MANY-models behavior is achieved per-agent via AgentOptions.model / tier / agentType, resolved through resolveModelRoute. pi-mcp must let pi pick models, which it does via the script + model-tiers.json + meta.phases[].model.
- Visibility tools: subscribe to onAgentStart/onAgentEnd/onPhase/onLog/onTokenUsage/onAgentJournal callbacks to stream/persist progress. Each onAgentStart carries {label, phase, prompt, model, callIndex}; onAgentEnd carries {result, tokens, worktree, model, toolCalls}. These are the live feed for a 'read pi's run state' MCP tool. Persisted run files come from createRunPersistence/PersistedRunState (run-persistence.js) — examine that module for the on-disk schema the MCP must read.
- Resume is built-in and deterministic: persist each onAgentJournal entry ({index, hash, result}) and pass them back as resumeJournal + resumeFromRunId to re-run only the changed suffix. pi-mcp can offer a 'resume a delegated run' tool cheaply. Note callIndex is the stable key; hashing covers prompt+model+tier+phase+agentType+agentDef+schema, so editing the script's prompts invalidates exactly the right suffix.
- Token-budget accounting is SOFT and post-hoc — pi-mcp cannot rely on a hard cap; in-flight waves can overshoot tokenBudget and per-phase budgets before later calls throw. Surface the soft nature to callers and set conservative budgets. Real usage requires the provider to report it via session.getSessionStats(); otherwise it's a length/4 estimate.
- Concurrency is capped at MAX_CONCURRENCY=16 and agents at MAX_AGENTS_PER_RUN=1000 (overridable). A nested workflow() shares these via SharedRuntime, so a delegated task that itself nests still obeys one global limiter — pi-mcp should set `concurrency` deliberately and know nesting is capped at ONE level.
- Subagents are full pi SDK sessions (createAgentSession) with REAL SettingsManager (inherits ~/.pi/settings.json default provider/model) but in-memory SessionManager. Models must be auth-configured in ~/.pi/auth.json / models.json or they silently fall back to the session default (non-strict) or throw MODEL_ROUTING_ERROR (strict, when model-tiers.json fallback.mode==='error'). pi-mcp should verify required models are authed before delegating, or set strict mode to fail loudly.
- Structured output is the reliable machine-readable return channel: pass a typebox `schema` in agent() and Pi validates before capture; non-compliance after 2 repair retries + prose extraction is a hard SCHEMA_NONCOMPLIANCE error. For pi-mcp's synthesized final result, drive the top-level workflow to return a schema-validated object so the MCP can hand Claude Code a structured payload.
- The VM is explicitly NOT a security sandbox (host Function reachable via injected fn .constructor). Workflow scripts are trusted/LLM-authored. If pi-mcp ever accepts a script from an untrusted source, it must add its own isolation — do not treat runWorkflow as safe arbitrary-code execution.
- Determinism constraints shape what scripts pi-mcp generates: no Date.now()/Math.random()/new Date() (parse-time reject + runtime throw). Any time/randomness must come through `args`. meta must be the FIRST statement and an object literal only (no interpolation/spread/computed keys) — pi-mcp's script template/generator must conform exactly or runWorkflow throws SCRIPT_VALIDATION_ERROR.
- agentType definitions (.pi/agents/*.md, project>user) bind tools allow/denylist + model + body prompt and are snapshotted once per run. pi-mcp can pre-provision named subagent roles on disk so generated scripts reference them by name; editing a definition invalidates that call's resume cache (folded into the hash via agentDefinitionKey).
- isolation:'worktree' gives per-agent git worktree isolation with deterministic names (runId-callIndex-label); useful if pi-mcp delegates code-editing fleets that must not stomp each other. Worktrees are always torn down in finally, even on timeout/abort.
- Worth importing directly from index.ts in any Go-adjacent TS shim: runWorkflow, parseWorkflowScript, WorkflowAgent, the model-routing/tier helpers, createRunPersistence, and WorkflowManager/createWorkflowTool (higher-level orchestration that may already implement most of what pi-mcp needs to wrap).

**Questões em aberto:**
- The on-disk persisted run schema (PersistedRunState/RunStatus) lives in run-persistence.js — not read here. pi-mcp's visibility tools depend on its exact JSON field names and file layout under .pi/workflows/runs; that file must be examined separately.
- How runWorkflow is actually invoked headless via `pi -p --mode json` (the CLI/tool wiring) is in workflow-tool.js / workflow-manager.js / workflow-commands.js, not in the three target files. The mapping from CLI flags to WorkflowRunOptions (especially confirm/onLog/persistLogs/resumeJournal) needs confirming there.
- Where the top-level workflow SCRIPT comes from in the delegate-a-task flow: is it LLM-generated on the fly (generateDeepResearchWorkflow/adversarial-review/deep-research generators) or a saved workflow? The generator modules (deep-research.js, adversarial-review.js) define the default decomposition strategies pi-mcp would lean on.
- estimateTokens (length/4) is a crude fallback; unclear how often providers report total===0 in practice, which affects how trustworthy budget accounting is for cost-sensitive pi-mcp callers.
- The `session.setActiveToolsByName` call in resolveStructuredOutput is optional (best-effort); whether the pi SDK session reliably implements it for all providers affects schema-retry reliability.


---

# Orchestration & Persistence: WorkflowManager, run-persistence, errors

**Arquivos examinados:** /root/projects/pi-dynamic-workflows-custom/src/workflow-manager.ts, /root/projects/pi-dynamic-workflows-custom/src/run-persistence.ts, /root/projects/pi-dynamic-workflows-custom/src/errors.ts, /root/projects/pi-dynamic-workflows-custom/src/config.ts, /root/projects/pi-dynamic-workflows-custom/src/workflow.ts, /root/projects/pi-dynamic-workflows-custom/src/workflow-tool.ts, /root/.pi/workflows/runs/ (35 live run JSON + .bak files inspected via jq)

**Fatos-chave:**
- WorkflowManager (src/workflow-manager.ts) is an EventEmitter wrapping runWorkflow(); it tracks runs in an in-memory Map<runId, ManagedRun> and mirrors every state change to disk via a RunPersistence layer.
- Run IDs are generated by generateRunId() = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2,8)}` e.g. 'mq3v6uyo-pmtqtv'. NOT a UUID, NOT the same as pi's sessionId (which IS a UUID, stored in the run file's sessionId field).
- Run files persist to join(cwd, '.pi/workflows/runs') as `<runId>.json` plus `<runId>.json.bak`; with pi running from $HOME this resolves to ~/.pi/workflows/runs. Writes are atomic (tmp+rename) with a best-effort .bak mirror; load() falls back to .bak if the primary is corrupt/truncated.
- RunStatus union (run-persistence.ts:9) = 'pending' | 'running' | 'paused' | 'completed' | 'failed' | 'aborted'. NOTE: 'interrupted' is NOT a run status — it is only a PER-AGENT status. On-disk observed run statuses: completed, failed, aborted, paused, running.
- PersistedAgentState.status union = 'queued' | 'running' | 'done' | 'error' | 'skipped' | 'interrupted'. On-disk observed: done, error, interrupted, running.
- Resume is journal-based: agent() calls get a deterministic callIndex (monotonic callSeq assigned at lexical call time) and a sha256 callHash (prompt+model+phase+agentOptions+agentType). Cached result replayed only while callIndex < firstMiss (longest-unchanged-prefix); first hash miss sets firstMiss, and that call + all later ones run live.
- restartAgent(runId, agentId) truncates the journal to entries with index < target.callIndex, so the target call (callIndex >= target) and everything after re-run live in place (same runId). Requires the run to be NOT running and the target agent to have a numeric callIndex (else 'predates per-agent restart').
- recoverStaleRuns() runs in the constructor: any persisted run still 'running' that this fresh manager doesn't have in memory is reconciled to 'paused' (never 'failed') so its journal survives for resume.
- Runtime abort throws WorkflowError('workflow aborted', WORKFLOW_ABORTED) (lowercase) from workflow.ts; the 'Workflow aborted' (capitalized) string in errors.ts wrapError() is only a fallback when the caught value is not an Error instance.
- WorkflowErrorCode enum (errors.ts:5): AGENT_TIMEOUT, WORKFLOW_ABORTED, AGENT_LIMIT_EXCEEDED, TOKEN_BUDGET_EXHAUSTED, SCRIPT_VALIDATION_ERROR, SCHEMA_NONCOMPLIANCE, MODEL_ROUTING_ERROR, AGENT_EXECUTION_ERROR, PERSISTENCE_ERROR, UNKNOWN.

## Files

- `/root/projects/pi-dynamic-workflows-custom/src/workflow-manager.ts` (598 lines) — the orchestrator/lifecycle layer.
- `/root/projects/pi-dynamic-workflows-custom/src/run-persistence.ts` (190 lines) — the filesystem run-file schema + atomic write/.bak.
- `/root/projects/pi-dynamic-workflows-custom/src/errors.ts` (89 lines) — `WorkflowError` + codes + wrap/classify helpers.
- Supporting: `/root/projects/pi-dynamic-workflows-custom/src/config.ts` (paths/limits), `/root/projects/pi-dynamic-workflows-custom/src/workflow.ts` (`runWorkflow`, `JournalEntry`, resume/journal mechanics), `/root/projects/pi-dynamic-workflows-custom/src/workflow-tool.ts` (constructs the manager).

---

## 1. WorkflowManager — full API

### Constructor options (`WorkflowManagerOptions`, lines 67–80)

```ts
export interface WorkflowManagerOptions {
  cwd?: string;            // default process.cwd(); runs dir = join(cwd, ".pi/workflows/runs")
  concurrency?: number;    // default 8 (capped elsewhere by MAX_CONCURRENCY=16)
  loadSavedWorkflow?: (name: string) => string | undefined; // enables nested workflow('name')
  agent?: Pick<WorkflowAgent, "run">;   // inject a custom subagent runner (tests)
  mainModel?: string;      // session main model (provider/id), for auto-tiering explore agents
  sessionId?: string;      // pi session id to tag runs with
  persistence?: RunPersistence; // inject custom persistence (tests); else createRunPersistence(cwd)
}
```

Constructor (lines 94–104) sets fields, builds `this.persistence = options.persistence ?? createRunPersistence(this.cwd)`, then calls `recoverStaleRuns()`. In production it is created in `workflow-tool.ts:121` as:

```ts
new WorkflowManager({
  cwd: options.cwd,
  concurrency: options.concurrency,
  loadSavedWorkflow: (name) => storage.load(name)?.script,
});
```

(Note: `sessionId`/`mainModel` are NOT passed at construction there — they are wired later via `setSessionId()`/`setMainModel()`.)

### `ExecOptions` (per-execution, lines 46–65)

Shared by `runSync`, `startInBackground`, `resume`, `restartAgent` (the last two only ever set `resumeJournal`):

```ts
export interface ExecOptions {
  resumeJournal?: Map<number, JournalEntry>; // replay journaled results for unchanged prefix
  maxAgents?: number;                        // cap on total agents (default 1000 in runWorkflow)
  agentTimeoutMs?: number;                   // per-agent timeout (default 5*60*1000)
  externalSignal?: AbortSignal;              // host abort (tool/Esc) -> aborts this run
  onProgress?: (snapshot: WorkflowSnapshot) => void; // fired on every progress event
  tokenBudget?: number | null;               // hard run-wide token budget; agent() throws when spent
  confirm?: (promptText: string, options: unknown) => Promise<unknown>; // checkpoint() resolver (UI runs)
  tools?: WorkflowRunOptions["tools"];        // tools exposed to every agent (deep-research/adversarial)
}
```

These map almost 1:1 onto `WorkflowRunOptions` when `executeRun` calls `runWorkflow` (lines 245, 253–319). `externalSignal` is bridged to the internal `AbortController` (lines 248–251): if already aborted it aborts immediately, else it adds a one-time `abort` listener.

### Public methods

| Method | Signature | Behavior |
|---|---|---|
| `setSessionId` | `(id: string \| undefined): void` | Binds manager to a pi session; new runs stamped with it; `listRuns()` filters by it. Called on session_start. |
| `setMainModel` | `(spec: string \| undefined): void` | Sets the session main model used to auto-tier explore agents. |
| `startInBackground` | `(script, args?, exec?: ExecOptions = {}) => { runId: string; promise: Promise<WorkflowRunResult> }` | Returns immediately. Generates runId, parses script, builds `ManagedRun` with `background: true, status: 'running'`, persists initial state, then fires `executeRun(...)` (NOT awaited). Adds `promise.catch(()=>{})` to suppress unhandled-rejection on abort; the real promise is still returned. |
| `runSync` | `async (script, args?, exec?: ExecOptions = {}) => Promise<WorkflowRunResult>` | Blocking, but still tracked (visible in navigator/task panel). `createManaged()` builds `ManagedRun` with `background: false`; persists initial state, then awaits `executeRun`. |
| `pause` | `(runId): boolean` | Only if status `running`. Aborts controller, sets status `paused`, emits `paused`, persists. Returns false otherwise. |
| `resume` | `async (runId): Promise<boolean>` | Refuses if in-memory run is `running` or `aborted`. Loads persisted; refuses if no script, or status `completed`/`aborted`. Rebuilds `ManagedRun` (status `running`, `background: true`), builds `resumeJournal` Map from persisted journal, emits `resumed`, fires background `executeRun({resumeJournal})`. Returns true. |
| `stop` | `(runId): boolean` | Only if status `running` or `paused`. Aborts, sets status `aborted`, marks in-flight agents `interrupted`, emits `stopped`, persists. |
| `restartAgent` | `(runId, agentId): { ok: boolean; reason?: string }` | Restart-from-agent (see §4). |
| `getRun` | `(runId): ManagedRun \| undefined` | In-memory only. |
| `listRuns` | `(): PersistedRunState[]` | `persistence.list()`; if `sessionId` set, filters `r.sessionId === this.sessionId`, else returns all. |
| `listAllRuns` | `(): PersistedRunState[]` | All persisted runs regardless of session (used by recovery). |
| `getSnapshot` | `(runId): WorkflowSnapshot \| null` | In-memory snapshot. |
| `deleteRun` | `(runId): boolean` | Removes from memory + `persistence.delete(runId)` (also unlinks `.bak`/`.tmp`). |
| `getPersistence` | `(): RunPersistence` | Exposes persistence layer (used for saving workflows). |

Private: `recoverStaleRuns()`, `createManaged()`, `executeRun()`, `persistRun()`, `markInterruptedAgents()`.

### `ManagedRun` (in-memory, lines 24–44)

```ts
interface ManagedRun {
  runId: string;
  status: RunStatus;
  snapshot: WorkflowSnapshot;       // live agents/logs/phases/tokenUsage
  result?: WorkflowRunResult;
  error?: WorkflowError;
  controller: AbortController;
  startedAt: Date;
  script: string;                   // kept so the run can be resumed
  args?: unknown;
  journal: JournalEntry[];          // accumulated agent results (deterministic callIndex -> result)
  background: boolean;              // bg runs re-deliver result into conversation; sync runs don't
}
```

### `runWorkflow` usage inside `executeRun` (lines 253–319)

Forwards: `cwd, args, agent, mainModel, signal (controller.signal), concurrency, maxAgents, agentTimeoutMs, tokenBudget, confirm, tools, loadSavedWorkflow, resumeJournal, resumeFromRunId` (= runId only when resuming), and a set of callbacks:

- `onAgentJournal(entry)` — dedupes by `entry.index`, appends to `managed.journal`, then `persistRun()`. **This is the crash-safety write — the journal is flushed to disk after every live agent completes.**
- `onLog` → pushes to snapshot.logs, emits `log`, progress().
- `onPhase` → sets `currentPhase`, adds to `phases`, emits `phase`.
- `onAgentStart` → pushes an agent into snapshot (`id = agents.length+1`, status `running`, with `callIndex/label/phase/prompt/model`), emits `agentStart`.
- `onAgentEnd` → finds the last matching `running` agent by label, sets status `done` (or `error` if `result === null`), records `resultPreview` (via `preview()`), `tokens`, `model`, `toolCalls`; emits `agentEnd`.
- `onTokenUsage` → sets `snapshot.tokenUsage`, emits `tokenUsage`.

### Emitted events
`log`, `phase`, `agentStart`, `agentEnd`, `tokenUsage`, `complete` ({runId, result}), `error` ({runId, error}), `paused`, `resumed`, `stopped`. All carry `{ runId, ... }`.

---

## 2. Run lifecycle & status values

`executeRun` (lines 239–358) is the single funnel for sync/background/resume/restart:

1. Bridge `externalSignal` to the internal `AbortController`.
2. `await runWorkflow(...)`.
3. **Success**: `managed.status = 'completed'`, store `result`, emit `complete`, `persistRun()`, return result.
4. **Catch**: wrap non-`WorkflowError` into `WorkflowError(msg, WORKFLOW_ABORTED, {recoverable:true})`.
   - If `controller.signal.aborted` (intentional pause/stop/Esc):
     - If status still `running` (Esc-style, not via pause/stop which already set it): set `aborted` and `markInterruptedAgents()`.
     - Otherwise the status set by `pause()` (`paused`) / `stop()` (`aborted`) is preserved.
   - Else (genuine failure): `managed.status = 'failed'`.
   - Store `error`, emit `error`, `persistRun()`, **re-throw**.

### Run-level status values (`RunStatus`)
`pending` | `running` | `paused` | `completed` | `failed` | `aborted`.

- `pending` — declared in the union but never assigned by the manager (reserved).
- `running` — active, or (after a crash) a stale run that recovery flips to `paused`.
- `paused` — `pause()`, or `recoverStaleRuns()` reconciling a crashed `running` run.
- `completed` — successful finish.
- `failed` — non-abort error (e.g. token budget, agent limit, agent execution error).
- `aborted` — `stop()` or Esc-style abort.

On-disk distribution observed (35 run files): `completed` 22, `failed` 7, `paused` 3, `aborted` 2.

### Agent-level status values (`PersistedAgentState.status`)
`queued` | `running` | `done` | `error` | `skipped` | `interrupted`. Live snapshot only ever sets `running` → `done`/`error`, and `interrupted` via `markInterruptedAgents()`. On-disk observed: `done` 162, `running` 5 (in crashed/paused runs), `error` 4, `interrupted` 3.

> **Important nuance for the prompt's "running/done/error/interrupted":** these are the **agent** statuses. `interrupted` is NOT a member of `RunStatus`. Crashed runs keep their in-flight agents as `running` on disk until recovered.

---

## 3. JOURNAL format & resume

### `JournalEntry` (workflow.ts:38–43)

```ts
export interface JournalEntry {
  index: number;   // deterministic callIndex (monotonic callSeq, assigned at lexical agent() call time)
  hash: string;    // sha256 of call identity: prompt + modelSpec + phase + agentOptions + agentType key
  result: unknown; // the agent's return value (replayed verbatim on resume)
}
```

Persisted form in the run file (`PersistedRunState.journal`, run-persistence.ts:56):
```ts
journal?: Array<{ index: number; hash: string; result: unknown }>
```

### How resume works (workflow.ts:391–418)
- Every `agent()` call increments `state.callSeq` to get `callIndex` (assigned **before** the concurrency limiter, so `parallel()`/`pipeline()` fan-out is reproducible for a fixed script).
- `callHash = hashAgentCall(prompt, modelSpec, assignedPhase, agentOptions, agentDefinitionKey(agentDef))`.
- A cached entry is replayed **only if** `cached.hash === callHash && callIndex < state.firstMiss` (longest-unchanged-prefix). On replay it still fires `onAgentStart`/`onAgentEnd` (tokens: 0) so the UI shows it, then returns `cached.result`.
- On a genuine miss (no entry or hash changed) `state.firstMiss = Math.min(firstMiss, callIndex)` — that call AND every later call run live, so an edited upstream call never serves stale downstream results. This matches Claude Code's resume contract.
- Determinism is enforced by `DETERMINISM_PRELUDE` (Math.random/Date.now/new Date() throw) so re-runs reproduce the same hashes. Best-effort, not a security sandbox.

### Resume entry points
- **`resume(runId)`** — replays the entire persisted journal (`new Map(journal.map(e => [e.index, e]))`); everything past the last journaled index runs live.
- **`recoverStaleRuns()` + resume** — a crashed `running` run becomes `paused`; the preserved journal means resume replays the completed prefix.

---

## 4. Restart-from-agent (`restartAgent`, lines 488–540)

```ts
restartAgent(runId: string, agentId: number): { ok: boolean; reason?: string }
```

Guards & exact reasons:
- Run in memory is `running` → `{ ok:false, reason:"This run is still running — pause or stop it before restarting an agent." }`
- No persisted script → `{ ok:false, reason: \`Cannot restart agent in ${runId} (no script saved).\` }`
- Agent id not found → `{ ok:false, reason: \`Agent ${agentId} not found in ${runId}.\` }`
- Target agent has no numeric `callIndex` (legacy run) → `{ ok:false, reason:"This run predates per-agent restart — use the runs view to restart the whole run." }`

Mechanism: keep journal entries with `e.index < target.callIndex`; build `resumeJournal` from those; **the target call (callIndex >= target) and everything after it miss the cache and re-run live**, the earlier prefix replays. Runs in place (same `runId`), `background: true`, emits `resumed`. This coarse blast radius (>= target) is intentional: downstream agents consumed the target's result as a plain JS value, with no provenance graph to narrow it.

> Most on-disk run files have `journal=true` but NOT all agents carry `callIndex` (mixed legacy data observed), so MCP code must treat `callIndex` as optional.

---

## 5. Persisted RUN FILE schema (`PersistedRunState`, run-persistence.ts:29–57)

Written to `~/.pi/workflows/runs/<runId>.json` (and `.bak`). Every field with type:

```ts
interface PersistedRunState {
  runId: string;
  workflowName: string;        // from meta.name
  script: string;              // full JS source (kept for resume)
  args?: unknown;              // workflow args
  sessionId?: string;          // pi session UUID; undefined = legacy/global
  status: RunStatus;           // pending|running|paused|completed|failed|aborted
  phases: string[];
  currentPhase?: string;
  agents: PersistedAgentState[];
  logs: string[];
  result?: unknown;            // = WorkflowRunResult.result (the final synthesized value)
  startedAt: string;           // ISO
  updatedAt: string;           // ISO, overwritten on every save() (run-persistence.ts:108)
  completedAt?: string;        // ISO, set only when status === "completed"
  durationMs?: number;         // from WorkflowRunResult.durationMs
  tokenUsage?: {
    input: number; output: number; total: number;
    cost?: number; cacheRead?: number; cacheWrite?: number;
  };
  journal?: Array<{ index: number; hash: string; result: unknown }>;
}
```

`PersistedAgentState` (lines 11–27):

```ts
interface PersistedAgentState {
  id: number;                  // 1-based, snapshot order
  callIndex?: number;          // deterministic resume key; ABSENT on legacy runs
  label: string;
  phase?: string;
  prompt: string;              // full agent prompt (can be very large)
  status: "queued"|"running"|"done"|"error"|"skipped"|"interrupted";
  result?: unknown;
  error?: string;
  startedAt?: string;          // ISO (manager stamps managed.startedAt for ALL agents)
  endedAt?: string;            // ISO (manager stamps "now" for ALL agents at persist time)
  model?: string;              // provider/id, e.g. "deepseek/deepseek-v4-pro"
  toolCalls?: Array<{ name: string; summary?: string }>;
}
```

Note (lines 374–378): `persistRun()` writes `startedAt: managed.startedAt.toISOString()` and `endedAt: new Date().toISOString()` for **every** agent at save time — so per-agent timestamps are approximate (the run's start + the persist moment), not the agent's real wall-clock window. The live snapshot also carries `tokens` and `resultPreview` per agent (visible in the on-disk `agents[]`).

On-disk `keys` confirmed: `agents, args, completedAt, currentPhase, durationMs, journal, logs, phases, result, runId, script, sessionId, startedAt, status, tokenUsage, updatedAt, workflowName`.

### Paths & IDs
- Runs dir: `join(cwd, WORKFLOW_RUNS_DIR)` where `WORKFLOW_RUNS_DIR = ".pi/workflows/runs"` (config.ts:18). With pi launched from `$HOME`, this is `~/.pi/workflows/runs`. It is cwd-relative — **a workflow started from a project dir would persist under `<project>/.pi/workflows/runs`**, not home. (Confirmed: `createRunPersistence(this.cwd)` and `runsDir = join(cwd, WORKFLOW_RUNS_DIR)`.)
- File name: `<runId>.json`, sidecars `<runId>.json.bak` and (transient) `<runId>.json.tmp`.
- `generateRunId()` = base36 timestamp + '-' + 6 base36 random chars, e.g. `mq3v6uyo-pmtqtv`. Not globally unique under high concurrency in the same ms, but practically fine.

---

## 6. `.bak` behavior & atomic write (run-persistence.ts:106–135)

`save()`:
1. `ensureDir()` (mkdir recursive).
2. Mutates `state.updatedAt = new Date().toISOString()`.
3. `writeFileSync(path + ".tmp", json)` then `renameSync(tmp, path)` — atomic on same FS; a crash mid-write can't corrupt the live file.
4. `writeFileSync(path + ".bak", json)` in try/catch — best-effort mirror of the last good save.

`load(runId)`: tries `[path, path+".bak"]` in order; returns the first that parses, else `null`. So a corrupt/truncated primary transparently falls back to `.bak`.

`list()`: reads all `*.json` in the dir (this **includes** `.bak`? — no: `.bak` ends in `.bak` not `.json`, but `.json.bak` does NOT match `endsWith(".json")`, so `.bak` files are correctly excluded; `.tmp` also excluded). Skips files that fail to parse. Sorts by `updatedAt` desc.

`delete(runId)`: unlinks `.bak` and `.tmp` sidecars (best-effort) then the primary; returns true iff the primary existed.

`json = JSON.stringify(state, null, 2)` — 2-space pretty-printed (large prompts/results make files big: observed up to ~108 KB).

---

## 7. WorkflowError — codes & exact messages (errors.ts)

```ts
class WorkflowError extends Error {
  readonly code: WorkflowErrorCode;
  readonly recoverable: boolean;      // default false
  readonly agentLabel?: string;
  readonly details?: unknown;
  constructor(message, code, options: { recoverable?; agentLabel?; details? } = {})
}
```

`WorkflowErrorCode` members: `AGENT_TIMEOUT`, `WORKFLOW_ABORTED`, `AGENT_LIMIT_EXCEEDED`, `TOKEN_BUDGET_EXHAUSTED`, `SCRIPT_VALIDATION_ERROR`, `SCHEMA_NONCOMPLIANCE`, `MODEL_ROUTING_ERROR`, `AGENT_EXECUTION_ERROR`, `PERSISTENCE_ERROR`, `UNKNOWN`.

Helpers: `isWorkflowError`, `isAbortError` (regex `/\babort(?:ed)?\b/i` on message), `isTimeoutError` (`/\btimeout\b/i` or `name === "TimeoutError"`), `wrapError(error, {agentLabel?})`:
- WorkflowError → returned as-is.
- abort-like → `WorkflowError(err.message || "Workflow aborted", WORKFLOW_ABORTED, {recoverable:true})`.
- timeout-like → `WorkflowError(err.message || "Agent timed out", AGENT_TIMEOUT, {recoverable:true, agentLabel})`.
- else → `WorkflowError(message, AGENT_EXECUTION_ERROR, {recoverable:true, agentLabel, details:error})`.

### Exact thrown messages (from workflow.ts, the actual emitters)

| Code | Exact message | Site | recoverable |
|---|---|---|---|
| `WORKFLOW_ABORTED` | `"workflow aborted"` (lowercase) | `throwIfAborted()` workflow.ts:319 | true |
| `WORKFLOW_ABORTED` | `"Workflow aborted"` (capitalized) | errors.ts `wrapError` fallback only (non-Error value) | true |
| `WORKFLOW_ABORTED` | (also) message from a pipeline abort, workflow.ts:796–798 | | |
| `AGENT_LIMIT_EXCEEDED` | `` `Agent limit exceeded (${maxAgents}). Use maxAgents option to increase the limit.` `` | workflow.ts:328-330 & 776-778 | false |
| `TOKEN_BUDGET_EXHAUSTED` | `"workflow token budget exhausted"` | workflow.ts:336 | false |
| `TOKEN_BUDGET_EXHAUSTED` | `` `phase "${phase}" token sub-budget exhausted (${budget})` `` | workflow.ts:352-354 | false |
| `AGENT_TIMEOUT` | `` `Agent "${label}" timed out after ${timeout}ms` `` (thrown via withTimeout, wrapped at workflow.ts:1068) | | true |
| `SCRIPT_VALIDATION_ERROR` | `"workflow() can nest only one level deep"`, `"meta export must declare only \`meta\`"`, `"meta export must declare \`meta\`"`, `"meta must have a literal value"` | workflow.ts:589, 900, 907, 912 | |

In `executeRun`'s catch (workflow-manager.ts:330-337), any non-WorkflowError is wrapped as `WorkflowError(msg, WORKFLOW_ABORTED, {recoverable:true})` — so a raw thrown Error surfaces as a recoverable WORKFLOW_ABORTED even if it wasn't really an abort. The run's status, however, is decided by `controller.signal.aborted` (→ aborted/paused) vs not (→ failed), independent of the error code.

---

## 8. Constants (config.ts)
- `MAX_AGENTS_PER_RUN = 1000` (runWorkflow default `maxAgents`).
- `DEFAULT_AGENT_TIMEOUT_MS = 5*60*1000`.
- `MAX_CONCURRENCY = 16` ("matches Claude Code limit"). Manager default `concurrency` is 8.
- `DEFAULT_TOKEN_BUDGET = null` (unbounded by default).
- `WORKFLOW_RUNS_DIR = ".pi/workflows/runs"`, `WORKFLOW_SAVED_DIR = ".pi/workflows/saved"`, `MODEL_TIERS_FILE = ".pi/workflows/model-tiers.json"`, `USER_WORKFLOW_CONSENT_FILE = "~/.pi/workflows/consent.json"`.

**Implicações para pi-mcp:**
- pi_list_runs should read the runs directory directly with the SAME logic as RunPersistence.list(): readdir, filter f.endsWith('.json') (this excludes '.json.bak' and '.json.tmp'), JSON.parse each, skip parse failures, sort by updatedAt desc. Do NOT also read .bak files or you will double-count. Path is join(cwd, '.pi/workflows/runs') — for headless pi launched from $HOME this is ~/.pi/workflows/runs, but it is CWD-RELATIVE: if pi-mcp launches pi with a project cwd, runs land under <cwd>/.pi/workflows/runs. The MCP must know/control the cwd it launches pi with and look there. Consider forcing pi's cwd to a known dir.
- pi_run_status should parse a single <runId>.json with the .bak fallback: try the primary, and if it is missing or fails to JSON.parse, fall back to <runId>.json.bak (this is exactly RunPersistence.load semantics). The .tmp file is transient and must be ignored.
- Map our jobId -> pi runId: pi runIds are generated internally by generateRunId() and are NOT controllable from the CLI invocation. We cannot pass our own jobId in as the runId. To correlate, either (a) read pi -p --mode json output for the runId pi reports, or (b) after launching, diff the runs dir to find the new <runId>.json (race-prone under concurrency), or (c) prefer using sessionId: tag/launch each pi job with a known session id and filter run files by the persisted top-level `sessionId` field. Recommended: maintain a jobId -> {sessionId, runId} table; capture runId from pi's JSON stream when available, else match on sessionId.
- Surface BOTH levels of status. Run-level status is one of pending|running|paused|completed|failed|aborted; agent-level status (in agents[].status) is queued|running|done|error|skipped|interrupted. 'interrupted' and 'running' agents inside a non-running run indicate a crash/abort mid-flight. Our 'is it done?' check = run.status === 'completed' (or failed/aborted as terminal). 'completed' is the only status that sets completedAt and durationMs.
- A run still marked 'running' on disk may be a CRASHED process, not a live one. We cannot distinguish a live background pi from a dead one purely from the file. Heuristics: a fresh WorkflowManager would flip stale 'running' -> 'paused' on its next startup (recoverStaleRuns), but a headless one-shot pi -p may not re-instantiate the manager. The MCP should track the pi child PID per job and treat 'running' + dead PID as crashed; updatedAt staleness (no change for > some timeout) is a secondary signal.
- The final synthesized answer is the top-level `result` field (= WorkflowRunResult.result), an arbitrary JSON value (often an object). Token/cost accounting is `tokenUsage` {input, output, total, cost, cacheRead, cacheWrite}. Wall time is `durationMs`. These are exactly what a pi_run_status summary should return.
- Run files can be large (observed up to ~108 KB; agents[].prompt and journal[].result embed full prompts/results). For pi_list_runs, return only lightweight fields (runId, workflowName, status, sessionId, startedAt/updatedAt/completedAt, durationMs, agent counts, tokenUsage) and avoid shipping full prompts/journal/result; expose the heavy fields only via a detail tool with explicit opt-in.
- Per-agent startedAt/endedAt are NOT reliable per-agent windows — persistRun() stamps the run's startedAt and 'now' for every agent on each save. Do not present them as accurate per-agent latency. Use agents[].tokens for per-agent cost signal instead.
- Resume/restart are in-memory WorkflowManager operations (resume(), restartAgent()) — they are NOT exposed via the run files alone. If pi-mcp wants to resume/restart, it must invoke pi in a mode that drives the manager, not just rewrite the JSON. The journal+script in the run file is what makes resume possible, so never strip/blank `script` or `journal` when copying/relaying run files. callIndex is OPTIONAL on agents (legacy runs lack it) — restart-from-agent requires a numeric callIndex.
- Errors are reported via the WorkflowError code set; map them for the user: WORKFLOW_ABORTED (incl. user stop/Esc and any wrapped unknown error), AGENT_TIMEOUT, AGENT_LIMIT_EXCEEDED (default cap 1000), TOKEN_BUDGET_EXHAUSTED (run-wide or per-phase), SCHEMA_NONCOMPLIANCE, MODEL_ROUTING_ERROR, AGENT_EXECUTION_ERROR, SCRIPT_VALIDATION_ERROR, PERSISTENCE_ERROR, UNKNOWN. Note the run FILE does not persist the WorkflowError object/code — only run.status (failed/aborted) and per-agent agents[].error strings. To get the structured error code, capture it from pi's stdout/JSON stream; the file alone only tells you failed-vs-aborted plus free-text agent errors.

**Questões em aberto:**
- The run file does NOT persist the top-level WorkflowError code/message (only status failed/aborted + per-agent agents[].error strings). Does pi -p --mode json emit the WorkflowErrorCode on stdout so pi-mcp can surface it? Needs verification by probing headless mode (Area covering the CLI/JSON output).
- How does headless `pi -p --mode json` choose its cwd, and therefore where does it write runs? Confirm whether it always uses $HOME, the invocation cwd, or a configurable path — this directly determines where pi_list_runs/pi_run_status must look. (config makes it cwd-relative.)
- Does the headless one-shot pi process instantiate a long-lived WorkflowManager (so recoverStaleRuns runs and stale 'running' -> 'paused' reconciliation happens), or does a crashed background job leave the file stuck at 'running' indefinitely? Affects our liveness detection design.
- Is there any existing mechanism in pi to pass/override the runId or sessionId from the CLI so pi-mcp can deterministically correlate jobId -> runId without diffing the runs directory? (sessionId appears settable via setSessionId on session_start, but CLI surface unverified.)
- generateRunId() uses Date.now()+Math.random() base36 with only 6 random chars — under heavy parallel job launches is collision realistically possible, and should pi-mcp add its own namespacing?


---

# The `workflow` tool, subagent model resolution + provider calls, and the headless extension wiring

**Arquivos examinados:** /root/projects/pi-dynamic-workflows-custom/src/workflow-tool.ts, /root/projects/pi-dynamic-workflows-custom/src/agent.ts, /root/projects/pi-dynamic-workflows-custom/src/workflow-consent.ts, /root/projects/pi-dynamic-workflows-custom/extensions/workflow.ts, /root/projects/pi-dynamic-workflows-custom/src/config.ts, /root/projects/pi-dynamic-workflows-custom/src/structured-output.ts, /root/projects/pi-dynamic-workflows-custom/src/model-tier-config.ts, /root/projects/pi-dynamic-workflows-custom/src/task-panel.ts, /root/projects/pi-dynamic-workflows-custom/src/display.ts (workflowFinishedText), /root/projects/pi-dynamic-workflows-custom/src/workflow-manager.ts (startInBackground/runSync/executeRun/persist), /root/projects/pi-dynamic-workflows-custom/src/workflow.ts (WorkflowRunResult, WorkflowRunOptions, SharedRuntime, token budget), /root/projects/pi-dynamic-workflows-custom/src/index.ts, /root/projects/pi-dynamic-workflows-custom/package.json, /root/projects/pi-dynamic-workflows-custom/node_modules/@earendil-works/pi-coding-agent/dist/core/extensions/types.d.ts, /root/projects/pi-dynamic-workflows-custom/node_modules/@earendil-works/pi-coding-agent/dist/core/extensions/runner.js, /root/projects/pi-dynamic-workflows-custom/node_modules/@earendil-works/pi-coding-agent/dist/modes/print-mode.js, /root/projects/pi-dynamic-workflows-custom/node_modules/@earendil-works/pi-coding-agent/dist/core/agent-session.js (bindExtensions), /root/projects/pi-dynamic-workflows-custom/node_modules/@earendil-works/pi-ai/dist/types.d.ts

**Fatos-chave:**
- The tool is registered with name `"workflow"`, label `"Workflow"`, via `defineTool` in src/workflow-tool.ts (createWorkflowTool, line 117-324). Its input schema (workflowToolSchema, lines 61-95) is a TypeBox `Type.Object` with: `script` (required string), `args` (optional Any), `background` (optional boolean, DEFAULT true), `maxAgents` (optional number, DEFAULT 1000), `agentTimeoutMs` (optional number, DEFAULT 300000 = 5min), `tokenBudget` (optional number, no default = unlimited).
- `background` defaults to TRUE (`params.background ?? true`, line 201). background:true calls `manager.startInBackground(...)` and returns IMMEDIATELY with text from `backgroundStartedText(name, runId)` plus `details: { runId, background: true }`. background:false calls `await manager.runSync(...)`, blocks, and returns `content[0].text = `${header}${formattedResult}`` where header = `workflowFinishedText(...)` and formattedResult = a fenced ```json block of `JSON.stringify(result.result, null, 2)`.
- Preflight consent + checkpoint UI are SKIPPED in headless: the preflight card is gated on `if (ctx.hasUI && !consent.isAccepted())` (line 182) and `confirm` is only wired `uiCtx?.hasUI ? uiCtx.ui?.confirm : undefined` (line 173). In print/json mode `ctx.hasUI === false`, so neither ever fires.
- CRITICAL headless fact: in the pi SDK, `hasUI` = `this.uiContext !== noOpUIContext` (runner.js:215-216). print-mode.js calls `session.bindExtensions({ mode: mode==="json"?"json":"print", ... })` WITHOUT a uiContext, so the runner keeps its default `noOpUIContext` and `hasUI === false`. ExtensionMode is `"tui" | "rpc" | "json" | "print"`; only tui and rpc set a real UI context (hasUI true).
- `bindExtensions` (agent-session.js:1629-1651) DOES `await emit(this._sessionStartEvent)` (a `session_start` event), so the extension's `session_start` hook in extensions/workflow.ts FIRES in headless print/json mode — the workflow tool gets set active, manager.setMainModel/setSessionId run, installResultDelivery + installTaskPanel + the editor get installed (the latter two as harmless no-ops via noOpUIContext.setWidget).
- A subagent resolves its model via `resolveAgentModelSpec(options, mainModel)` (agent.ts:174-199): precedence is explicit `options.model` > `options.tier` (looked up in ~/.pi/workflows/model-tiers.json via `resolveTierModel`) > falls back to the configured `medium` tier for untagged agents when a tiers config exists > else `undefined` (session default). The resolved spec is turned into a real `Model` by `resolveWorkflowAgentModel` + `WorkflowAgent.resolveModel` against a `ModelRegistry.create(auth, models.json)`.
- A subagent ACTUALLY calls a provider via `createAgentSession({ cwd, agentDir: getAgentDir(), sessionManager: SessionManager.inMemory(), settingsManager: SettingsManager.create(this.cwd, agentDir), customTools, ...sessionOptions, ...(resolvedModel ? {model: resolvedModel} : {}) })` then `await session.prompt(...)` (agent.ts:436-460). Using the REAL `SettingsManager.create` (NOT inMemory) is load-bearing: inMemory doesn't load ~/.pi/settings.json and subagents would fall back to the first available model (e.g. openai-codex) with possibly-invalid auth = silent empty responses (comment at lines 440-443).
- Token/cost accounting: per-subagent usage is read from `session.getSessionStats()` -> `{tokens:{input,output,cacheRead,cacheWrite,total}, cost}` in the `finally` of WorkflowAgent.run (agent.ts:472-487) and reported via `onUsage`. These accumulate into the run-wide `shared.tokenUsage` and `shared.spent` in workflow.ts; the tokenBudget hard gate throws `TOKEN_BUDGET_EXHAUSTED` once `budget.remaining() <= 0`. Final usage surfaces as `WorkflowRunResult.tokenUsage` and is rendered into `workflowFinishedText`.
- The MCP must extract the verbatim result from the `role:"toolResult"` message (pi-ai ToolResultMessage: `{role:"toolResult", toolName:"workflow", content:[{type:"text",text}], details, isError}`). For background:false, `content[0].text` = header + fenced JSON, and `details.result` is the raw structured result object (cleanest source). The json-mode stdout is newline-delimited JSON events; the terminal `agent_end` event carries the full `messages[]` array including the toolResult.

## Area E — Workflow Tool + Agent Execution

This documents `src/workflow-tool.ts`, `src/agent.ts`, `src/workflow-consent.ts`, and `extensions/workflow.ts`, plus the SDK plumbing that decides headless behavior.

---

## 1. The `workflow` tool

Defined in `src/workflow-tool.ts` via `createWorkflowTool(options)` (lines 117-324), registered with `defineTool({ name: "workflow", label: "Workflow", ... })`.

### 1.1 Input schema (`workflowToolSchema`, lines 61-95)

```ts
const workflowToolSchema = Type.Object({
  script: Type.String({ description: "Required raw JavaScript workflow script, with no Markdown fences. ..." }),
  args: Type.Optional(Type.Any({ description: "Optional JSON value exposed to the workflow script as global `args`." })),
  background: Type.Optional(Type.Boolean({ description: "... Default: true ..." })),
  maxAgents: Type.Optional(Type.Number({ description: "Maximum number of agents allowed in this run. Default: 1000." })),
  agentTimeoutMs: Type.Optional(Type.Number({ description: "Timeout per agent in milliseconds. Default: 300000 (5 minutes)." })),
  tokenBudget: Type.Optional(Type.Number({ description: "Hard total-token budget ... Omit for no limit. ..." })),
});

export type WorkflowToolInput = {
  script: string; args?: unknown; background?: boolean;
  maxAgents?: number; agentTimeoutMs?: number; tokenBudget?: number;
};
```

| Field | Type | Required | Default | Meaning |
|---|---|---|---|---|
| `script` | string | **yes** | — | Raw JS workflow; must start with `export const meta = {...}` and call `agent()` ≥ once |
| `args` | any | no | — | JSON exposed to script as global `args` |
| `background` | boolean | no | **`true`** | true = fire-and-forget + runId; false = block & return result inline |
| `maxAgents` | number | no | **1000** (`MAX_AGENTS_PER_RUN`, config.ts) | Hard cap on agents this run |
| `agentTimeoutMs` | number | no | **300000** (`DEFAULT_AGENT_TIMEOUT_MS`) | Per-agent timeout |
| `tokenBudget` | number | no | **none / unlimited** (`DEFAULT_TOKEN_BUDGET = null`) | Hard run-wide token cap; throws `TOKEN_BUDGET_EXHAUSTED` when spent ≥ budget |

Note: `MAX_CONCURRENCY = 16` (config.ts) is a separate global concurrency cap (not a tool param); the WorkflowManager default `concurrency` is 8 unless overridden.

### 1.2 `prepareArguments` / normalization

- `prepareArguments(args)` → `normalizeWorkflowToolArgs(args)` (lines 420-425): requires an object, requires `script` to be a string, and runs `normalizeWorkflowScript` on it.
- `normalizeWorkflowScript` (lines 427-432) trims and strips a leading/trailing ```` ```js ```` / ```` ```javascript ```` fence so the model can wrap the script in a fence without breaking parsing.
- `execute` re-normalizes the script then `parseWorkflowScript(script)` to obtain `parsed.meta` (name/description/phases).

### 1.3 Headless guard (preflight + checkpoint), lines 167-195 — QUOTED

```ts
const uiCtx = ctx as { hasUI?: boolean; ui?: { confirm?(title, message): Promise<boolean> } } | undefined;
const uiConfirm = uiCtx?.hasUI ? uiCtx.ui?.confirm : undefined;
const confirm = uiConfirm
  ? (promptText) => uiConfirm.call(uiCtx?.ui, "Workflow checkpoint", promptText)
  : undefined;

// Headless/RPC contexts have no UI to prompt, so they run as before.
if (ctx.hasUI && !consent.isAccepted()) {
  const decision = await runPreflightApproval(ctx.ui, parsed.meta,
    { maxAgents: params.maxAgents, tokenBudget: params.tokenBudget }, consent);
  if (decision === "cancel") {
    return { content: [{ type: "text", text: "Workflow cancelled by the user." }], details: { cancelled: true } };
  }
}
```

So in headless (`ctx.hasUI === false`): preflight is skipped, `confirm` is `undefined` (a `checkpoint()` in a script takes its declared headless default — never hangs).

### 1.4 `background: true` branch (lines 201-211) — the default

```ts
if (params.background ?? true) {
  const { runId } = manager.startInBackground(script, params.args, {
    maxAgents: params.maxAgents, agentTimeoutMs: params.agentTimeoutMs, tokenBudget: params.tokenBudget,
  });
  return {
    content: [{ type: "text", text: backgroundStartedText(parsed.meta.name, runId) }],
    details: { runId, background: true },
  };
}
```

`backgroundStartedText(name, runId)` (lines 347-358) returns multi-line prose:

```
Workflow "<name>" started in the background.
Run ID: <runId>
It keeps running on its own. When it finishes, the result is delivered back
here ... Tell the user they can simply wait here ...
They can also track or cancel it with /workflows status <runId> or /workflows stop <runId>.
```

The result is **NOT** in this return value — it is delivered later via `installResultDelivery` (see §4).

### 1.5 `background: false` branch (lines 213-302) — inline result

1. Registers a live TUI widget `workflow-<name>` (only if `ctx.hasUI`; no-op in headless).
2. `result = await manager.runSync(script, params.args, { maxAgents, agentTimeoutMs, tokenBudget, confirm, externalSignal: signal, onProgress(...) })`.
3. Throws `"workflow scripts must call agent() at least once..."` if `result.agentCount === 0`.
4. Builds the return:

```ts
const header = workflowFinishedText(finalSnapshot);     // ✓ Workflow "<name>" finished (N agents · T tokens · $cost · Ds).
const formattedResult = result.result !== undefined
  ? `\n\`\`\`json\n${JSON.stringify(result.result, null, 2)}\n\`\`\``
  : "";
return {
  content: [{ type: "text", text: `${header}${formattedResult}` }],
  details: { ...snapshot, meta, phases, logs, result: result.result, durationMs, tokenUsage, runId },
};
```

`workflowFinishedText` (display.ts:332-345):

```ts
return `✓ Workflow "${snapshot.name}" finished (${parts.join(" · ")}).`;
// parts: "N agents", optionally "T tokens", "$cost" (4dp), "D.Ds"
```

So the **inline result text** = a one-line `✓ Workflow ... finished (...)` header, then a newline, then a fenced ```` ```json ```` block of the workflow's return value. The structured object is ALSO available cleanly at `details.result`.

### 1.6 Abort handling

On abort/`WORKFLOW_ABORTED`, in-flight agents are marked `interrupted` and the tool throws `new Error("Workflow aborted")` (lines 245-256).

---

## 2. Agent execution (`src/agent.ts`)

### 2.1 Model resolution

**`resolveAgentModelSpec(options, mainModel, loadConfig=loadModelTierConfig)` → `string | undefined`** (lines 174-199). Precedence:
1. `options.model` (explicit per-agent spec; the workflow layer folds agentType/phase model into this).
2. `options.tier` → `resolveTierModel(tier, config)` (model-tier-config.ts:174-176 = `config.tiers[tier]`). If unresolved and `strictModelResolution` → throws `MODEL_ROUTING_ERROR`; else returns `mainModel`.
3. Untagged: if a tiers config exists, default to the `medium` tier model.
4. Else `undefined` → session default.

**`resolveWorkflowAgentModel(modelSpec, options, resolveModel)`** (lines 209-242): trims the spec, calls `resolveModel(spec)`; on success fires `onModelResolved("provider/id")`; on miss + strict → throws `MODEL_ROUTING_ERROR`, else warns + `onModelFallback` + returns `undefined`.

**`WorkflowAgent.resolveModel(spec)`** (lines 386-393): `provider/id` → `registry.find(provider, id)`; bare id → `registry.getAvailable().find(id) ?? registry.getAll().find(id)`.

**`resolveTierModel(tier, config)`** = `config.tiers[tier]`. Tier names are open-ended (small/medium/big/cheap/coder/judge/research). `model-tiers.json` lives at `~/.pi/workflows/model-tiers.json` (`MODEL_TIERS_FILE`), shape `{ tiers: Record<string,string>, rules?, fallback? }`.

### 2.2 The actual provider call (lines 395-497) — the heart of the fleet

```ts
const agentDir = getAgentDir();
const { session } = await createAgentSession({
  cwd: runCwd,
  agentDir,
  sessionManager: SessionManager.inMemory(),
  settingsManager: SettingsManager.create(this.cwd, agentDir),  // REAL, not inMemory
  customTools,
  ...this.sessionOptions,
  ...(resolvedModel ? { model: resolvedModel } : {}),
});
await session.prompt(this.buildPrompt(prompt, options, Boolean(options.schema)));
```

- **`SettingsManager.create(this.cwd, agentDir)` is load-bearing** (comment lines 440-443): `SettingsManager.inMemory()` would not load `~/.pi/settings.json`, causing subagents to fall back to the first available model (e.g. `openai-codex`) which may lack valid auth → silent empty responses.
- The registry used for model resolution is built lazily from the **same** `getAgentDir()` files: `AuthStorage.create(join(dir,"auth.json"))` + `ModelRegistry.create(auth, join(dir,"models.json"))` (lines 369-378) — so resolved models carry valid credentials.
- Each subagent is a fresh in-memory `createAgentSession` with `createCodingTools(cwd)` (default) plus optional `structured_output` tool. Abort is wired: `signal.addEventListener("abort", () => session.abort())`.

### 2.3 `resolveStructuredOutput` (lines 118-156)

If `capture.called` → return `capture.value`. Else restrict tools to `["structured_output"]` via `setActiveToolsByName`, re-prompt up to `maxSchemaRetries` (default 2) with a strict "call structured_output now as your only action" message; if still not called, try `extractValidated` (prose JSON extraction, validated against the schema). If nothing validates → throws `WorkflowError(SCHEMA_NONCOMPLIANCE, { recoverable: false })`.

The terminating tool (`structured-output.ts`): `createStructuredOutputTool({schema, capture})` — pi validates params against schema BEFORE execute, then `execute` sets `capture.value/called` and returns `{ terminate: true }` so the subagent ends without an extra assistant turn.

### 2.4 `extractValidated<T>(text, schema)` (lines 46-62)

Finds a JSON block (fenced ```` ```json ```` first, else first balanced `{}`/`[]` via `findJsonBlock`), `JSON.parse`, then `Convert(schema, parsed)` + `Check(schema, converted)`; returns the value only if it validates, else `undefined`. Never fabricates.

### 2.5 `extractToolCalls(messages)` (lines 89-102)

Walks assistant messages' `content`, collects `{ type:"toolCall", name, arguments }` parts (pi-ai `ToolCall`), excludes the internal `structured_output` tool, summarizes args via `summarizeToolArgs` (prefers `command/query/pattern/url/path/file_path/name`). Best-effort; `[]` on unexpected shape.

### 2.6 `listAvailableModelSpecs()` (lines 266-275)

Builds `ModelRegistry` from `getAgentDir()`'s `auth.json` + `models.json` and returns `registry.getAvailable().map(m => `${m.provider}/${m.id}`)`. Best-effort `[]`. Used to advise the workflow author about routable models (`modelRoutingGuideline`).

### 2.7 Token / cost accounting (lines 472-494)

In the `finally` (so both success and error paths), reads `session.getSessionStats()` → `{ tokens:{input,output,cacheRead,cacheWrite,total}, cost }` and calls `onUsage({input,output,cacheRead,cacheWrite,total,cost})` (type `AgentUsage`). `total === 0` means the provider reported no usage. Then `onToolCalls(extractToolCalls(...))`, then `session.dispose()`. Per-agent usage accumulates into the run-wide `shared.tokenUsage`/`shared.spent` in workflow.ts (the budget gate and `WorkflowRunResult.tokenUsage` both read from there).

### 2.8 `AgentRunOptions` (lines 287-346) — superset of what the script's `agent(prompt, opts)` accepts

Includes: `label`, `schema`, `tools`, `instructions`, `signal`, `onUsage`, `onToolCalls`, `model`, `tier`, `strictModelResolution`, `routeSource`, `onModelResolved`, `onModelFallback`, `cwd`, `toolNames` (allowlist), `disallowedToolNames` (denylist), `maxSchemaRetries`.

---

## 3. `WorkflowRunResult` (workflow.ts:118-134)

```ts
export interface WorkflowRunResult<T = unknown> {
  meta: WorkflowMeta;
  result: T;
  logs: string[];
  phases: string[];
  agentCount: number;
  durationMs: number;
  runId?: string;
  tokenUsage?: { input: number; output: number; total: number; cost: number; cacheRead?: number; cacheWrite?: number };
}
```

`result.result` is the workflow script's return value — the synthesized output the MCP cares about.

---

## 4. Background result delivery (`installResultDelivery`, task-panel.ts:65-101)

Set up once per extension (idempotent via `manager.__deliveryInstalled`). Subscribes to manager events:

```ts
manager.on("complete", ({ runId }) => {
  const run = manager.getRun(runId);
  if (run?.background) deliver(deliverText(run));   // only background runs are delivered
});
manager.on("error", ({ runId, error }) => {
  if (!manager.getRun(runId)?.background) return;
  deliver(`✗ Workflow ${runId} failed: ${error?.message ?? "unknown error"}`);
});
```

`deliver(content)` calls:

```ts
pi.sendMessage(
  { customType: "workflow-result", content, display: true },
  { triggerTurn: true, deliverAs: "followUp" },
);
```

- `triggerTurn: true` → starts a fresh turn when the agent is idle, feeding the result back to the model so the paused conversation continues.
- `deliverAs: "followUp"` → if the user is mid-turn, the result is queued and picked up after that turn (never interrupts).

`deliverText(run)` (lines 41-51) = `workflowFinishedText(completionSnapshot)` + blank line + `summarizeResult(run.result?.result)` (prefers `verdict`/`report`/`summary` string fields, else bare string, else truncated JSON to 400 chars).

**Foreground (`background:false`) runs are NOT delivered** — they already return inline as the tool result; re-delivering would duplicate.

---

## 5. Extension wiring (`extensions/workflow.ts`)

`export default function extension(pi: ExtensionAPI)`:

1. Creates ONE shared `cwd`, `storage = createWorkflowStorage(cwd)`, and `manager = new WorkflowManager({ cwd, loadSavedWorkflow: (name) => storage.load(name)?.script })`. **Single manager/storage** so background runs from the tool are reachable from `/workflows`.
2. `workflowTool = createWorkflowTool({ cwd, manager, storage })`; `pi.registerTool(workflowTool)`.
3. Registers commands: `registerWorkflowCommands`, `registerWorkflowModelsCommand`, `registerBuiltinWorkflows` (deep-research/adversarial-review), `registerAllSavedWorkflows`.
4. `effort = createEffortState()`; `workflowMode = { active:false, enabled:false }` — keyword trigger is OFF by default (the model decides when to call the tool); `registerEffortCommand(pi, effort, workflowMode)`.

### 5.1 `session_start` hook (lines 46-70) — runs in headless too

```ts
pi.on("session_start", (_event, ctx: ExtensionContext) => {
  const active = pi.getActiveTools();
  if (!active.includes(workflowTool.name)) pi.setActiveTools([...active, workflowTool.name]);
  manager.setMainModel(ctx.model ? `${ctx.model.provider}/${ctx.model.id}` : undefined);
  try { manager.setSessionId(ctx.sessionManager?.getSessionId()); } catch {}
  installResultDelivery(pi, manager);
  installTaskPanel(pi, manager, ctx.ui, { storage, cwd });   // ctx.ui is noOp in headless
  if (!editorInstalled) { installWorkflowEditor(pi, ctx.ui, effort, workflowMode); editorInstalled = true; }
});
```

This hook keeps the tool ACTIVE, sets the main model (for tier fallback + auto-tiering explore agents), scopes run history to the session, and installs result delivery. `installTaskPanel`/`installWorkflowEditor` call `ctx.ui.setWidget(...)` which is a **safe no-op** in headless.

---

## 6. Does the engine run correctly outside the TUI? (YES, with caveats)

Evidence from the pi SDK (`node_modules/@earendil-works/pi-coding-agent/dist`):

- `ExtensionMode = "tui" | "rpc" | "json" | "print"` (extensions/types.d.ts:207).
- `ExtensionContext.hasUI` doc: *"Whether dialog-capable UI is available (true in TUI and RPC modes)"* (types.d.ts:213-214).
- `ExtensionRunner.hasUI()` = `this.uiContext !== noOpUIContext` (runner.js:215-216). Default `uiContext = noOpUIContext` (runner.js:122).
- **print-mode.js** (`pi -p` and `pi --mode json`) calls `session.bindExtensions({ mode: mode==="json"?"json":"print", commandContextActions, onError })` (lines 50-78) — **no `uiContext` passed** → runner keeps `noOpUIContext` → **`hasUI === false`**.
- `bindExtensions` (agent-session.js:1629-1651) ends with `await this._extensionRunner.emit(this._sessionStartEvent)` — so **`session_start` fires** in print/json mode (default event `{type:"session_start", reason:"startup"}`).
- `noOpUIContext` (runner.js:59-90) gives safe no-ops: `confirm: async () => false`, `setWidget: () => {}`, `notify/setStatus/...` all no-ops.

Conclusion: in headless `pi -p --mode json`, the extension loads, `session_start` fires, the workflow tool is registered + active, `WorkflowManager.runSync`/`startInBackground` run normally, subagents spawn via `createAgentSession`, and preflight/checkpoint are skipped (no hang). The engine is independent of the TUI.

---

## 7. The json-mode event envelope (probed live)

`pi -p --mode json "<prompt>"` emits newline-delimited JSON events on stdout (confirmed):

```
{"type":"session","version":3,"id":"<uuid>","cwd":"/tmp"}
{"type":"agent_start"}
{"type":"turn_start"}
{"type":"message_start","message":{role:"user",...}}
{"type":"message_end","message":{role:"user",...}}
{"type":"message_start","message":{role:"assistant",content:[],provider,model,usage,...}}
{"type":"message_update","assistantMessageEvent":{type:"text_delta",delta:"...",partial:{...}},"message":{...}}
... (more text_delta / tool calls) ...
{"type":"message_end","message":{role:"assistant",content:[...],usage:{input,output,totalTokens,cost:{...}}}}
{"type":"turn_end","message":{...},"toolResults":[...]}
{"type":"agent_end","messages":[ ...full transcript... ],"willRetry":false}
```

Tool results are pi-ai `ToolResultMessage` (pi-ai types.d.ts:211-219):

```ts
interface ToolResultMessage<TDetails=any> {
  role: "toolResult"; toolCallId: string; toolName: string;
  content: (TextContent|ImageContent)[]; details?: TDetails; isError: boolean; timestamp: number;
}
```

A `workflow` tool call appears as a `{type:"toolCall", name:"workflow", arguments:{...}}` part in an assistant message, and its result as a `role:"toolResult", toolName:"workflow"` message. For **background:false**, `content[0].text` = the header + fenced JSON, and **`details.result`** = the raw structured object (the cleanest extraction point). The terminal `agent_end.messages[]` contains the entire transcript including this toolResult.

Background-run results delivered via `installResultDelivery` arrive as a separate injected user/custom message with `customType:"workflow-result"` in a NEW turn (because `triggerTurn:true`), not as the original tool's return.

**Implicações para pi-mcp:**
- To make pi reliably AUTHOR + CALL the workflow tool with background:false in one headless turn, drive `pi -p --mode json` and craft the user prompt so the model emits a single `workflow` tool call with `background:false`. The tool stays registered+active in headless (session_start fires via bindExtensions). The tool guidelines bias the model toward `background:true` by default and only call it for explicit 'workflow/fan-out/multi-agent' requests — so the MCP prompt MUST (a) explicitly ask for a workflow and (b) explicitly state 'set background:false / return the result inline in this turn'. Consider also `--append-system-prompt` to harden this instruction.
- EXTRACT the verbatim result from the json event stream: parse newline-delimited events, take the terminal `agent_end` event's `messages[]`, find the `role:"toolResult"` with `toolName:"workflow"`. Prefer `details.result` (the raw structured object) over parsing the fenced ```json``` out of `content[0].text`. The text form is `✓ Workflow "<name>" finished (...)` + newline + ```json block; the JSON inside that block equals `details.result`.
- AVOID background:true for the synchronous MCP delegate pattern: its result is NOT in the tool return — it is delivered LATER via pi.sendMessage(customType:"workflow-result", triggerTurn:true) which starts a NEW turn. In a single `pi -p` invocation that triggers another agent turn (extra latency/cost) and the result lands in a later assistant message, not the workflow tool's toolResult. background:false is the right mode for 'delegate a task, get one synthesized result back'.
- Headless safety is confirmed: hasUI=false in print/json (uiContext stays noOpUIContext), so preflight consent + checkpoint() are auto-skipped (no hangs), and the task-panel/editor widgets are no-ops. The MCP does NOT need to pre-seed ~/.pi/workflows/consent.json for headless runs (consent is only checked when hasUI is true). RPC mode (--mode rpc) WOULD set hasUI=true and re-enable the preflight gate — avoid RPC unless you also handle consent.
- Pass run-shaping caps directly as tool args: `maxAgents` (default 1000), `agentTimeoutMs` (default 300000), `tokenBudget` (default unlimited). For cost control the MCP should set `tokenBudget`; once exceeded the run throws TOKEN_BUDGET_EXHAUSTED (non-recoverable). Global concurrency is capped at 16 (MAX_CONCURRENCY) and total agents at 1000 regardless of args.
- Model fleet correctness depends on user config: subagent models come from `~/.pi/workflows/model-tiers.json` (tier->model spec) and resolve against `~/.pi/<agentDir>/auth.json` + `models.json` via getAgentDir(). The MCP/host MUST ensure those files are present and authed in the environment pi runs in; otherwise tier routing falls back to the session main model or session default. The use of REAL SettingsManager.create (not inMemory) means subagents inherit ~/.pi/settings.json default provider/model — keep that file valid or subagents may silently produce empty responses.
- For VISIBILITY tools, read the persisted run files under `.pi/workflows/runs/` (WORKFLOW_RUNS_DIR). startInBackground/runSync persist initial + per-journal + final state including status, phases, agents, logs, result, and tokenUsage (input/output/total/cost/cacheRead/cacheWrite), plus durationMs and sessionId. runId comes from generateRunId(). The MCP's status tool can poll these files; the `details.runId` (background) or `details.runId` (foreground) ties the tool call to its run file.
- If the MCP wants schema-validated synthesized output, instruct the workflow author guideline path: scripts pass `opts.schema` (plain JSON Schema) to agent() and the final synthesis agent returns a validated object; resolveStructuredOutput enforces it (2 repair retries + prose extraction fallback, else SCHEMA_NONCOMPLIANCE). The whole-workflow return value is whatever the script returns (result.result), which need not match any schema — so the MCP should define the contract in the prompt (e.g. 'return {ok, verdict, ...}').

**Questões em aberto:**
- Does the model reliably emit `background:false` when asked? The tool's promptGuidelines strongly default to background:true ('runs are background by default ... Pass background:false only when you must use the result inline'). Empirically need to confirm the model honors an explicit MCP instruction to set background:false; otherwise consider a forced/saved-workflow path or post-processing.
- In `pi --mode json -p`, when a background workflow's result is delivered via sendMessage(triggerTurn:true) AFTER the initial prompt completes, does print-mode keep the process alive long enough to emit that follow-up turn, or does it exit at the first agent_end? print-mode.js only awaits the initial prompt(s) then disposes — strongly implies background results would be LOST in single-shot print mode (another reason to use background:false). Needs a live probe to confirm.
- The MCP plan mentions reading 'pi's persisted run files' for visibility — confirm the exact on-disk JSON shape of PersistedRunState (run-persistence.ts) and whether tokenUsage/result are always present for in-progress vs completed runs (only completed runs set completedAt/durationMs per persistRun).
- Whether `--mode json` emits a distinct event for the `workflow-result` custom message and how `triggerTurn` interacts with `-p` single-shot (does it spawn a second turn in the same process or require an interactive/rpc loop). This matters only if the MCP ever uses background:true.
- Exact behavior when no model-tiers.json exists AND mainModel is undefined in headless (ctx.model could be undefined if no default model is configured) — resolveAgentModelSpec returns undefined and subagents use the session default; need to confirm the session default in a headless subagent is the authed model, not an unauthed first-available.


---

# Model routing: tiers, rules, agentTypes, and how one workflow fans out across many models

**Arquivos examinados:** /root/projects/pi-dynamic-workflows-custom/src/model-routing.ts, /root/projects/pi-dynamic-workflows-custom/src/model-routing-policy.ts, /root/projects/pi-dynamic-workflows-custom/src/model-tier-config.ts, /root/projects/pi-dynamic-workflows-custom/src/agent-registry.ts, /root/projects/pi-dynamic-workflows-custom/src/workflows-models-command.ts, /root/projects/pi-dynamic-workflows-custom/src/workflow.ts (lines ~240-520, the agent() closure + route assembly), /root/projects/pi-dynamic-workflows-custom/src/agent.ts (lines ~160-450: resolveAgentModelSpec, resolveWorkflowAgentModel, listAvailableModelSpecs, WorkflowAgent.resolveModel/getRegistry/run), /root/projects/pi-dynamic-workflows-custom/src/config.ts (constants MODEL_TIERS_FILE, AGENTS_DIR), /root/.pi/workflows/model-tiers.json, /root/.pi/workflows/model-tiers.json.bak-pi-dynamic-workflows-2026-06-06, /root/.pi/agent/models.json

**Fatos-chave:**
- The authoritative resolution function is resolveModelRoute() in src/model-routing-policy.ts. Resolution order is: explicit-model > agent-type-model > rules (by field priority agentType > phase > label) > explicit-tier > phase-model > default-tier ('medium') > non-strict fallback-main-model > (strict: throw).
- There are TWO distinct routing layers: (1) phase-pattern routing in src/model-routing.ts (meta.phases[].model + meta.model default) which produces context.phaseModel; (2) the central tier/rule policy in src/model-routing-policy.ts driven by ~/.pi/workflows/model-tiers.json. The phase layer feeds INTO the central policy as one input, not a parallel decision.
- Tiers are named slots mapping to exactly ONE model spec string each (e.g. 'small' -> 'deepseek/deepseek-v4-flash'). Config file: ~/.pi/workflows/model-tiers.json (constant MODEL_TIERS_FILE = '.pi/workflows/model-tiers.json' in src/config.ts).
- Rule match fields are agentType, phase, label. Each can be a literal string OR a /regex/flags literal (parseRegexLiteral requires a leading '/' and a closing '/'). Priority is by FIELD not by array order: all rules are scanned for agentType matches first, then phase, then label (RULE_PRIORITY = ['agentType','phase','label']).
- Fallback modes are exactly two: 'session' (default/non-strict) and 'error' (strict). isStrictFallback() returns true only when config.fallback.mode === 'error'. The live config at /root/.pi/workflows/model-tiers.json uses mode 'error' (strict).
- agentType is a real binding loaded from .pi/agents/*.md (project, cwd-relative) and ~/.pi/agents/*.md (user). Frontmatter binds name, description, tools (allowlist), disallowedTools (denylist), model; the markdown body becomes the role prompt. Project defs win over user on name collision. Currently NO .md files exist in either location.
- CURRENT live tier->model map (/root/.pi/workflows/model-tiers.json): small=deepseek/deepseek-v4-flash, medium=deepseek/deepseek-v4-pro, big=openai-codex/gpt-5.5, cheap=deepseek/deepseek-v4-flash, coder=openai-codex/gpt-5.5, judge=openai-codex/gpt-5.5, research=deepseek/deepseek-v4-pro. Four rules: coder agentType->coder tier, reviewer agentType->judge tier, phase 'Scan'->cheap tier, label /judge|critic|review|synthesis|final/i->judge tier.
- Provider creds live in ~/.pi/agent/models.json under providers.<name>. The deepseek provider uses api 'openai-completions', baseUrl https://api.deepseek.com, and apiKey is a pi command-substitution: a leading '!' makes pi exec the remainder as a shell command and use stdout as the key — here it runs agent-vault to fetch DEEPSEEK_API_KEY from the 'sales-brain' vault. Note: openai-codex (used by big/coder/judge) is NOT defined in this models.json, so it must come from a built-in/default provider registry plus auth.json.
- A model spec resolves against pi's ModelRegistry (created from ~/.pi/agent/auth.json + ~/.pi/agent/models.json). 'provider/modelId' uses registry.find(provider, id); a bare id prefers registry.getAvailable() (auth-configured) then registry.getAll(). Unresolved spec: strict -> throw MODEL_ROUTING_ERROR, non-strict -> warn + use session default.
- The per-agent resolved model overrides the session default via createAgentSession({ ..., model: resolvedModel }). Each agent() call independently resolves its own model, so a single runWorkflow() spawns subagents on different providers/models concurrently under a shared limiter.

## AREA F — Model Routing (the heart of multi-model execution)

This subsystem decides, **per `agent()` call**, exactly which provider/model a subagent runs on. Because every `agent()` call resolves independently and the workflow fans them out under a concurrency limiter, **one `runWorkflow()` ends up executing across many different models at once**.

There are **two cooperating layers**:

1. **Phase-pattern routing** (`src/model-routing.ts`) — maps workflow *phases* to models from the script's `meta` (`meta.phases[].model`, with `meta.model` as default). Produces a single `phaseModel` candidate.
2. **Central tier/rule policy** (`src/model-routing-policy.ts`) — the authoritative resolver, driven by `~/.pi/workflows/model-tiers.json`. It takes `phaseModel` as **one input among many** and applies the full precedence chain.

---

### 1. The complete resolution order

The single source of truth is `resolveModelRoute()` in `src/model-routing-policy.ts`:

```ts
export type ModelRouteSource =
  | "explicit-model"      // 1
  | "agent-type-model"    // 2
  | "rule-model"          // 3a (a matching rule with .model)
  | "rule-tier"           // 3b (a matching rule with .tier)
  | "explicit-tier"       // 4
  | "phase-model"         // 5
  | "default-tier"        // 6  (always tier "medium")
  | "fallback-main-model";// 7  (non-strict only)

export interface ModelRouteContext {
  explicitModel?: string;   // agentOptions.model
  explicitTier?: string;    // agentOptions.tier
  agentType?: string;       // agentOptions.agentType (name)
  agentTypeModel?: string;  // agentDef?.model from the .md frontmatter
  phase?: string;           // assignedPhase
  label?: string;           // agentOptions.label (trimmed)
  phaseModel?: string;      // from resolveModelForPhase(...)
  mainModel?: string;       // the session's main model
}

export interface ModelRouteDecision {
  modelSpec: string;
  source: ModelRouteSource;
  tier?: string;
  ruleName?: string;
  strict: boolean;
}

export function resolveModelRoute(
  context: ModelRouteContext,
  config: ModelTierConfig | null | undefined,
): ModelRouteDecision | undefined
```

Exact precedence inside `resolveModelRoute` (top wins, first match returns):

| # | Source | Condition | Code |
|---|--------|-----------|------|
| 1 | `explicit-model` | `context.explicitModel?.trim()` | returns immediately |
| 2 | `agent-type-model` | `context.agentTypeModel?.trim()` (model from agentType `.md`) | returns immediately |
| 3 | `rule-model` / `rule-tier` | `resolveRuleDecision(...)` returns a decision | see rules below |
| 4 | `explicit-tier` | `context.explicitTier?.trim()` resolves to a configured tier | `resolveTierDecision(tier, "explicit-tier", …, { allowMissing: !strict })` |
| 5 | `phase-model` | `context.phaseModel?.trim()` | returns immediately |
| 6 | `default-tier` | tier **"medium"** is configured | `resolveTierDecision("medium", "default-tier", …, { allowMissing: true })` |
| 7 | `fallback-main-model` | `!strict && context.mainModel?.trim()` | returns the session model |
| — | strict error | `strict` and nothing resolved | throws `WorkflowError(MODEL_ROUTING_ERROR, recoverable:false)` |
| — | `undefined` | non-strict, nothing resolved | returns `undefined` → session default used downstream |

The **call site** that assembles `ModelRouteContext` is in `src/workflow.ts`, inside the `agent()` closure (lines ~373-388):

```ts
// src/workflow.ts:373
const phaseModel = resolveModelForPhase(assignedPhase, routingConfig);
const routeDecision = resolveModelRoute(
  {
    explicitModel: agentOptions.model,
    explicitTier: agentOptions.tier,
    agentType: agentOptions.agentType,
    agentTypeModel: agentDef?.model,
    phase: assignedPhase,
    label: requestedLabel,
    phaseModel,
    mainModel: options.mainModel,
  },
  tierConfig,
);
const modelSpec = routeDecision?.modelSpec;
```

`routingConfig` comes from `parseModelRoutingFromMeta(meta.phases, meta.model)` (workflow.ts:247) and `tierConfig` from `loadModelTierConfig()` (workflow.ts:248), both computed once per run.

> Note: the comment in workflow.ts:373 ("explicit model > agentType model > rules > explicit tier > phase model > default tier > non-strict session fallback") matches the implementation exactly.

#### Phase routing detail (`src/model-routing.ts`)
`resolveModelForPhase(phase, config)` iterates `config.routes`; each route matches either exactly (case-sensitive `phase === route.phasePattern`) or via case-insensitive regex when `useRegex` is set. `parseModelRoutingFromMeta` builds routes ONLY for phases that declared a `model`, and carries `meta.model` as `defaultModel` (returned when no route matches). So `phaseModel` is non-empty when the matched/declared phase has a model, else the meta default.

---

### 2. Rules: match fields and priority

Rules live in `model-tiers.json` under `rules[]`. Type (`src/model-tier-config.ts`):

```ts
export interface ModelTierRuleMatch { agentType?: string; phase?: string; label?: string; }
export interface ModelTierRule { name?: string; match: ModelTierRuleMatch; tier?: string; model?: string; }
export interface ModelTierFallback { mode?: "session" | "error"; }
export interface ModelTierConfig {
  tiers: Record<string, string>;
  rules?: ModelTierRule[];
  fallback?: ModelTierFallback;
}
```

**Priority is by FIELD, not array order** (`src/model-routing-policy.ts`):

```ts
type RuleField = "agentType" | "phase" | "label";
const RULE_PRIORITY: RuleField[] = ["agentType", "phase", "label"];
```

`resolveRuleDecision` loops `field` in that order; for each field it scans every rule, skips rules whose `match[field]` is undefined, and checks `matchRouteRule`. So an `agentType` rule beats a `phase` rule beats a `label` rule even if the label rule appears earlier in the array.

**Matching a single field** (`matchValue` → `parseRegexLiteral`):
- A pattern starting with `/` and containing a closing `/` is treated as a **JS regex literal** `/body/flags` and tested with `regex.test(actual)`.
- Otherwise it is a **case-sensitive exact string** comparison `actual === pattern`.
- `undefined` actual never matches.

**Rule resolution semantics:**
- A matched rule with `model` → `{ source: "rule-model" }`. If the rule defines **both** `model` and `tier` AND strict, it throws.
- A matched rule with `tier` → `resolveTierDecision(tier, "rule-tier", …)`. In strict mode, a rule referencing an unknown tier throws; non-strict (`allowMissing: !strict`) it is skipped and resolution continues.

(`isModelTierConfig` validation in `model-tier-config.ts` also rejects a config at load time if any rule references a tier not present in `tiers`, and requires each rule to have at least one of `tier`/`model` and at least one match field.)

---

### 3. Fallback modes (strict vs session)

Only two modes exist: `"session"` (default) and `"error"` (strict).

```ts
export function isStrictFallback(config) { return config?.fallback?.mode === "error"; }
```

- **session** (non-strict): unresolved tiers/models degrade gracefully — `resolveModelRoute` may fall through to `fallback-main-model` (the session model) or return `undefined` (session default). `resolveWorkflowAgentModel` (agent.ts) warns `[workflow] model "X" not found; using session default` and calls `onModelFallback`, which `workflow.ts` surfaces as a log line: `<label>: model "<spec>" unavailable — using the session default`.
- **error** (strict): any failure to resolve a concrete, available model throws a non-recoverable `WorkflowError(WorkflowErrorCode.MODEL_ROUTING_ERROR)`. Strictness is threaded into the agent run via `strictModelResolution: routeDecision?.strict` (workflow.ts:463) and `routeSource: [source, tier, ruleName].join(":")` (workflow.ts:464).

The live config uses `"fallback": { "mode": "error" }` → **strict routing is ON**; a typo or an unavailable model in any tier/rule will hard-fail the run rather than silently downgrade.

---

### 4. Tier → model mapping and config plumbing (`src/model-tier-config.ts`)

- Path: `getModelTierConfigPath()` = `~/.pi/workflows/model-tiers.json`.
- `loadModelTierConfig()` returns `null` only when the file is absent; an invalid/unreadable file **throws** (so strict routing cannot be silently disabled by a typo).
- `buildDefaultTierConfig(currentModelSpec)` makes `{ small, medium, big }` all = current Pi model (or first available).
- `resolveTierModel(tier, config)` = `config.tiers[tier]`.
- `sortedTierNames` orders `small < medium < big`, then alphabetical.
- `saveModelTierConfig` writes pretty-printed JSON, creating parent dirs.

The `/workflows-models` command (`src/workflows-models-command.ts`) is the interactive editor: it lists tiers, lets the user pick a model per tier from `listAvailableModelSpecs()` (the same list as Pi's `/model`), add/remove custom tiers (name must match `/^[a-z0-9_-]+$/`), reset to defaults (preserving `rules` and `fallback`), and Save. It blocks removing a tier still referenced by a rule and never persists until "Save and exit".

#### Current live `~/.pi/workflows/model-tiers.json`

```json
{
  "tiers": {
    "small":    "deepseek/deepseek-v4-flash",
    "medium":   "deepseek/deepseek-v4-pro",
    "big":      "openai-codex/gpt-5.5",
    "cheap":    "deepseek/deepseek-v4-flash",
    "coder":    "openai-codex/gpt-5.5",
    "judge":    "openai-codex/gpt-5.5",
    "research": "deepseek/deepseek-v4-pro"
  },
  "rules": [
    { "name": "coder agentType",    "match": { "agentType": "coder" },    "tier": "coder" },
    { "name": "reviewer agentType", "match": { "agentType": "reviewer" }, "tier": "judge" },
    { "name": "scan phase",         "match": { "phase": "Scan" },          "tier": "cheap" },
    { "name": "judgment labels",    "match": { "label": "/judge|critic|review|synthesis|final/i" }, "tier": "judge" }
  ],
  "fallback": { "mode": "error" }
}
```

(A backup `~/.pi/workflows/model-tiers.json.bak-...` shows the prior 3-tier-only shape: small/medium/big with the same deepseek/openai-codex models and no rules/fallback.)

---

### 5. The agentType registry (`src/agent-registry.ts`)

`agentType` is a **real binding of tools + model + system prompt**, loaded from Markdown files:
- `.pi/agents/*.md` (project, cwd-relative) and `~/.pi/agents/*.md` (user). Constant `AGENTS_DIR = ".pi/agents"`.
- Project defs win over user; within a dir, first-by-sorted-filename wins; collisions resolved silently.
- **Currently NO `.md` files exist** in either location (so no named agentTypes are registered right now).

```ts
export interface AgentDefinition {
  name: string;             // = frontmatter.name || filename (sans .md)
  description?: string;
  tools?: string[];         // allowlist of coding-tool names
  disallowedTools?: string[];// denylist, applied after allowlist
  model?: string;           // provider/modelId or bare id -> context.agentTypeModel
  prompt: string;           // markdown body, prepended as role guidance
  source: "project" | "user";
}
```

Key functions: `loadAgentRegistry(cwd, opts?)` (snapshotted **once per run** in workflow.ts:255 for determinism), `resolveAgentType(name, registry)`, `applyToolPolicy(tools, allow, deny)`, `agentDefinitionKey(def)` (folded into the resume call-hash so editing a `.md` invalidates that call's cached result), `listAgentTypes(registry)`.

Frontmatter keys **parsed but currently ignored** (documented): `mcp`, `skills`, `background`, `isolation`.

In `workflow.ts`: `agentDef = resolveAgentType(agentOptions.agentType, agentRegistry)`. Its `.model` becomes `context.agentTypeModel` (precedence #2, above rules/tiers). Its `tools`/`disallowedTools` are passed to the agent run as `toolNames`/`disallowedToolNames`, and its `prompt` is folded into the agent instructions.

---

### 6. From `modelSpec` string to a concrete authenticated model (`src/agent.ts`)

Once `resolveModelRoute` returns a `modelSpec`, `WorkflowAgent.run` resolves it against pi's `ModelRegistry`:

```ts
// agent.ts:386
private resolveModel(spec: string): Model<any> | undefined {
  const registry = this.getRegistry();          // ModelRegistry.create(auth.json, models.json)
  const slash = spec.indexOf("/");
  if (slash > 0) return registry.find(spec.slice(0, slash), spec.slice(slash + 1)); // provider/modelId
  return registry.getAvailable().find(m => m.id === spec)   // bare id: prefer authed
      ?? registry.getAll().find(m => m.id === spec);         // then any known
}
```

`resolveWorkflowAgentModel(modelSpec, options, resolveModel)` (agent.ts:209) wraps this: on success calls `onModelResolved("provider/id")`; on miss, **strict → throw**, **non-strict → warn + `onModelFallback` + return undefined**. The resolved model is then applied per-call, overriding the session default:

```ts
// agent.ts:436
const { session } = await createAgentSession({
  cwd: runCwd, agentDir,
  sessionManager: SessionManager.inMemory(),
  settingsManager: SettingsManager.create(this.cwd, agentDir),
  customTools,
  ...this.sessionOptions,
  ...(resolvedModel ? { model: resolvedModel } : {}), // per-call model wins
});
```

`listAvailableModelSpecs()` (agent.ts:266) builds the same registry and returns `provider/modelId` strings for all auth-configured models — this is what both `/workflows-models` and the workflow tool guideline show authors.

---

### 7. Provider credentials (`~/.pi/agent/models.json`)

Structure (no secret values shown):

```json
{
  "providers": {
    "deepseek": {
      "baseUrl": "https://api.deepseek.com",
      "api": "openai-completions",
      "apiKey": "!AGENT_VAULT_VAULT=sales-brain agent-vault vault credential get DEEPSEEK_API_KEY",
      "compat": { "supportsDeveloperRole": false, "supportsReasoningEffort": true, "thinkingFormat": "deepseek" },
      "models": [
        { "id": "deepseek-v4-pro",   "name": "DeepSeek V4 Pro",   "reasoning": true, "input": ["text"],
          "contextWindow": 1000000, "maxTokens": 131072,
          "cost": { "input": 0.5, "output": 2, "cacheRead": 0, "cacheWrite": 0 },
          "thinkingLevelMap": { "minimal": "low", "low": "low", "medium": "medium", "high": "high", "xhigh": "high" } },
        { "id": "deepseek-v4-flash", "name": "DeepSeek V4 Flash", "reasoning": true, "input": ["text"],
          "contextWindow": 1000000, "maxTokens": 131072,
          "cost": { "input": 0.1, "output": 0.4, "cacheRead": 0, "cacheWrite": 0 },
          "thinkingLevelMap": { … } }
      ]
    }
  }
}
```

**apiKey command-substitution convention**: the leading `!` tells pi to **execute the remainder as a shell command** and use stdout as the API key. Here `AGENT_VAULT_VAULT=sales-brain` selects the vault and `agent-vault vault credential get DEEPSEEK_API_KEY` fetches the named credential (verified: `agent-vault vault credential get` is a real subcommand; binary at `/usr/local/bin/agent-vault`). The plaintext key is therefore **never stored in models.json** — only the fetch command.

**Important gap**: the tiers `big`/`coder`/`judge` point at `openai-codex/gpt-5.5`, but **`openai-codex` is NOT a provider in this models.json**. It must be supplied by pi's built-in/default provider registry (with credentials in `~/.pi/agent/auth.json`). For a model spec to resolve, `ModelRegistry.create(auth.json, models.json)` must yield it via `getAvailable()`/`getAll()`. Because the live config is strict (`fallback.mode: "error"`), if `openai-codex/gpt-5.5` is not actually available/authed, any `big`/`coder`/`judge`/judgment-label route will hard-fail the workflow.

---

### 8. Concretely: how ONE workflow runs across MANY models at once

For a fixed `runWorkflow()`:
1. `routingConfig` (phase→model) and `tierConfig` (tiers/rules/fallback) are loaded **once**; the agentType registry is snapshotted **once**.
2. For **each** `agent(prompt, opts)` call, `assignedPhase = opts.phase ?? state.currentPhase`, then `resolveModelRoute` runs the full precedence chain to pick that one agent's `modelSpec`.
3. Concretely with the live config:
   - `agent(..., { agentType: "coder" })` → rule "coder agentType" → tier `coder` → **openai-codex/gpt-5.5**.
   - `agent(..., { agentType: "reviewer" })` → tier `judge` → **openai-codex/gpt-5.5**.
   - `agent(..., { label: "final-synthesis" })` → label rule matches `/.../i` → tier `judge` → **openai-codex/gpt-5.5**.
   - An agent created while `currentPhase === "Scan"` → phase rule → tier `cheap` → **deepseek/deepseek-v4-flash**.
   - `agent(..., { tier: "research" })` → explicit-tier → **deepseek/deepseek-v4-pro**.
   - `agent(..., { model: "deepseek/deepseek-v4-pro" })` → explicit-model wins over everything.
   - An untagged agent in an unmatched phase → falls to default tier `medium` → **deepseek/deepseek-v4-pro**.
4. All these `agent()` calls are dispatched through the shared concurrency limiter (`createLimiter`, capped by `MAX_CONCURRENCY` and CPU count), so subagents on DeepSeek and OpenAI-Codex run **in parallel**. Each gets its own `createAgentSession` with its own per-call `model`, so a single workflow truly executes a heterogeneous model fleet simultaneously, then the script synthesizes the results.

The `routeDecision` also carries `source`/`tier`/`ruleName` (used for telemetry via `routeSource`), and `displayModel` is updated by `onModelResolved` and surfaced in `onAgentStart`/`onAgentEnd` events — so visibility tooling can show which model each subagent actually ran on.

**Implicações para pi-mcp:**
- pi OWNS model selection. The MCP must NOT pass any model/provider/tier/temperature to pi on a per-task basis, and must NOT inject a model into the headless invocation that would override the workflow's own resolveModelRoute decisions. Claude (via the MCP) decomposes nothing about models; it only hands pi a TASK string.
- The model fleet is configured entirely by the USER editing files on disk, not by the MCP: ~/.pi/workflows/model-tiers.json (tiers, rules, fallback mode) and ~/.pi/agents/*.md or <cwd>/.pi/agents/*.md (named agentTypes binding tools/model/prompt). Provider creds/models live in ~/.pi/agent/models.json (+ ~/.pi/agent/auth.json). To change which models the fleet uses, the user runs /workflows-models or edits these files.
- The MCP MUST preserve cwd semantics: agentType project defs and routing are cwd-relative (.pi/agents, plus model-tiers under ~/.pi). When driving pi headless in a background job, set the working directory deliberately so project-level .pi/agents resolve as the user expects; otherwise only ~/.pi (user-level) defs/tiers apply.
- Strict routing is currently ON (fallback.mode: 'error'). The MCP should surface MODEL_ROUTING_ERROR (WorkflowErrorCode.MODEL_ROUTING_ERROR) cleanly to Claude: an unavailable model (e.g. openai-codex/gpt-5.5 if not authed) or an unknown/mistyped tier will hard-fail the whole run rather than degrade. A pre-flight visibility tool could call listAvailableModelSpecs-equivalent and diff against the tier/rule model specs to warn before launching.
- For visibility tools, the MCP can read per-agent model attribution from pi's persisted run files: each agent emits onAgentStart/onAgentEnd carrying { label, phase, model: displayModel, callIndex } where displayModel reflects the resolved provider/model id. This is how the MCP shows 'which subagent ran on which model'. routeSource (source:tier:ruleName) is also computed and useful for explaining decisions.
- Never read or echo the apiKey values: models.json stores a '!'-prefixed agent-vault fetch command (e.g. for the 'sales-brain' vault), and auth.json holds tokens. The MCP may report provider/model availability and structure, but must treat credential material as opaque.
- Because each agent() call resolves its model independently and runs under pi's own concurrency limiter, the MCP gets true multi-model parallelism for free — it must NOT try to shard the task across models itself or run multiple pi processes per task to 'spread' models; one pi run already fans out across the configured fleet.

**Questões em aberto:**
- openai-codex/gpt-5.5 (backing big/coder/judge tiers) is not defined in ~/.pi/agent/models.json — it must come from pi's built-in default provider registry plus ~/.pi/agent/auth.json. Need to confirm openai-codex is actually authed/available, otherwise strict mode will hard-fail any big/coder/judge route. (Auth.json not read here to avoid touching secrets.)
- The exact pi-side parsing of the '!'-prefixed apiKey command-substitution (shell, env handling, caching of the fetched key) lives in the pi binary/SDK (ModelRegistry/AuthStorage in @earendil-works/pi-coding-agent), which was not grep-able from /usr/bin/pi strings. Worth confirming how often pi re-executes the agent-vault fetch (per process vs per request).
- agentType frontmatter keys mcp/skills/background/isolation are parsed-but-ignored in this fork version per the header comment. If the MCP relies on agentType-level isolation/background, confirm whether a newer build wires these (agentOptions.isolation === 'worktree' IS honored at the workflow.ts level, separate from the agentType field).
- label-rule regex matching uses /pattern/flags literals; need to confirm authors writing model-tiers.json by hand understand a bare string is exact-match (not substring) — a common foot-gun for the 'label'/'phase' fields.


---

# Built-in Workflow Patterns, Effort/Ultracode Standing Modes, Saved Workflows, Worktrees, Web Tools, Config & Logging

**Arquivos examinados:** /root/projects/pi-dynamic-workflows-custom/src/builtin-commands.ts, /root/projects/pi-dynamic-workflows-custom/src/deep-research.ts, /root/projects/pi-dynamic-workflows-custom/src/adversarial-review.ts, /root/projects/pi-dynamic-workflows-custom/src/web-tools.ts, /root/projects/pi-dynamic-workflows-custom/src/worktree.ts, /root/projects/pi-dynamic-workflows-custom/src/saved-commands.ts, /root/projects/pi-dynamic-workflows-custom/src/workflow-saved.ts, /root/projects/pi-dynamic-workflows-custom/src/effort-command.ts, /root/projects/pi-dynamic-workflows-custom/src/config.ts, /root/projects/pi-dynamic-workflows-custom/src/logger.ts, /root/projects/pi-dynamic-workflows-custom/src/workflow-editor.ts, /root/projects/pi-dynamic-workflows-custom/src/workflow-tool.ts, /root/projects/pi-dynamic-workflows-custom/src/workflow-commands.ts, /root/projects/pi-dynamic-workflows-custom/src/workflow-consent.ts, /root/projects/pi-dynamic-workflows-custom/src/index.ts, /root/projects/pi-dynamic-workflows-custom/extensions/workflow.ts, /root/projects/pi-dynamic-workflows-custom/node_modules/@earendil-works/pi-coding-agent/dist/core/extensions/types.d.ts, /root/.pi/workflows/consent.json, /root/.pi/workflows/model-tiers.json, /root/projects/pi-dynamic-workflows-custom/.pi/workflows/runs (listing)

**Fatos-chave:**
- Two built-in workflows are registered as slash commands by registerBuiltinWorkflows(pi, {cwd, manager}): /deep-research (generateDeepResearchWorkflow, 4 phases Queries/Gather/Verify/Report, injects real web_search+web_fetch tools) and /adversarial-review (generateAdversarialReviewWorkflow, 3 phases Investigate/Refute/Consensus). Both prefer manager.startInBackground and fall back to inline runWorkflow() when no WorkflowManager (headless/tests).
- The workflow SCRIPTS are static strings that read inputs from global `args` (question/angles/minSupport or task/reviewers/threshold) — the user's input is NEVER string-interpolated into source, avoiding escaping/injection. The web tools (web_search Bing-scrape, web_fetch HTML->text) are injected at runtime via the agent `tools` option, not baked into the script.
- Effort/ultracode is a standing session toggle: EffortState = { level: 'off' | 'high' | 'ultra' }, created by createEffortState() (default level 'off'). registerEffortCommand(pi, state, modeState?) registers BOTH /effort (off|high|ultra; xhigh and ultracode are aliases for ultra) and /ultracode (on = ultra; off; plus `/ultracode keyword on|off` to toggle the in-editor keyword trigger via modeState.enabled).
- The actual auto-arming happens in installWorkflowEditor() (workflow-editor.ts) via pi.on('input', ...). It bails immediately if event.source !== 'interactive'. byEffort = !triggered && effort.level !== 'off' && isSubstantive(event.text); isSubstantive = text.trim().length >= 16 && !startsWith('/'). When armed it (a) adds the 'workflow' tool to active tools and (b) returns { action:'transform', text: buildForcedWorkflowPrompt(text, effortDirective(level)) }.
- buildForcedWorkflowPrompt() appends a hard directive: 'You MUST handle this request by calling the tool named exactly `workflow`' and forbids answering directly, calling subagent, or using skills/commands — even small tasks must be wrapped in a minimal `workflow` call with >=1 agent(). effortDirective('high'/'ultra') adds tier guidance nudging fan-out breadth and tokenBudget/maxAgents caps.
- CRITICAL for headless: InputSource = 'interactive' | 'rpc' | 'extension' (node_modules .../core/extensions/types.d.ts:570). The input hook only fires for source==='interactive'. In pi -p / --mode json / --mode rpc the prompt is NOT interactive, so effort/ultracode auto-arming and the keyword trigger DO NOT FIRE headlessly. Also installWorkflowEditor (which registers the input/turn_end hooks) is only called inside the session_start handler in extensions/workflow.ts, gated on ctx.ui availability.
- Saved workflows: WorkflowStorage (createWorkflowStorage(cwd)) stores each workflow as a JSON file named <name>.json in project .pi/workflows/saved/ (WORKFLOW_SAVED_DIR) or user ~/.pi/workflows/saved/ (USER_WORKFLOW_SAVED_DIR). SavedWorkflow JSON fields: name, description, script, parameters?, location ('project'|'user'), path, savedAt (ISO). load() resolves project-then-user; list() merges both sorted by name; project wins on name collision.
- Each saved workflow is registered as a /<name> slash command by registerSavedWorkflow / registerAllSavedWorkflows; the handler parses args via parseCommandArgs (key=value tokens, positional -> `_`, raw -> `_raw`, parameter defaults fill missing), then runs via manager.startInBackground(wf.script, args) (background, visible in /workflows) or inline runWorkflow fallback. A workflow tool call can ALSO invoke a saved workflow by name inline: `await workflow('saved-name', argsObject)` (nesting one level deep), wired via WorkflowManager loadSavedWorkflow.
- Worktree isolation (worktree.ts): createWorktree(baseCwd, name) runs `git worktree add -b pi/wf/<slug> <repoRoot>/.pi/worktrees/<slug> HEAD`. Returns {isolated, cwd, branch, repoRoot, reason?}; falls back to no-op (isolated:false, cwd=baseCwd) on non-git or failure. name MUST be deterministic (runId+call index, never wall-clock) for stable resume keys. removeWorktree does `git worktree remove --force` + `git branch -D`. Results are NOT auto-merged.
- Config constants (config.ts): MAX_AGENTS_PER_RUN=1000, DEFAULT_AGENT_TIMEOUT_MS=5*60*1000, MAX_CONCURRENCY=16, DEFAULT_TOKEN_BUDGET=null, WORKFLOW_RUNS_DIR='.pi/workflows/runs', WORKFLOW_SAVED_DIR='.pi/workflows/saved', USER_WORKFLOW_SAVED_DIR='~/.pi/workflows/saved', MODEL_TIERS_FILE='.pi/workflows/model-tiers.json', USER_WORKFLOW_CONSENT_FILE='~/.pi/workflows/consent.json', AGENTS_DIR='.pi/agents'.
- Logger (logger.ts): createWorkflowLogger({runId, cwd, persist, onLog}) writes timestamped `[ISO] [LEVEL] msg` lines to <cwd>/.pi/workflows/runs/<runId>.log (persist defaults true). log/error/warn/getLogs/persist API. Confirmed on disk: runs dir holds run-<id>.log files plus <runId>.json and .json.bak persisted run states.
- Preflight consent gate (workflow-tool.ts + workflow-consent.ts): a workflow tool call only shows the one-time multi-agent warning when ctx.hasUI && !consent.isAccepted(). Consent file ~/.pi/workflows/consent.json = {"multiAgentWarningAccepted": true} (already accepted on this machine). Headless/RPC has no UI so it NEVER prompts and runs straight through.

## Area G — Built-in Workflow Patterns & Helpers

This area covers the bundled workflows (`/deep-research`, `/adversarial-review`), the `/effort` & `/ultracode` standing modes and exactly how they auto-arm a forced workflow, git-worktree isolation, the saved-workflow storage format, the real web tools, config knobs, and the logger.

---

### 1. Built-in workflow commands (`src/builtin-commands.ts`)

`registerBuiltinWorkflows(pi: ExtensionAPI, opts: { cwd: string; manager?: WorkflowManager }): void` registers two slash commands (idempotent — guarded by `alreadyRegistered`):

| Command | Description | Script generator | Extra tools |
|---|---|---|---|
| `/deep-research <question>` | "Research a question across the web with cross-checked sources" | `generateDeepResearchWorkflow()` | `createCodingTools(cwd)` **+** `createWebTools()` |
| `/adversarial-review <task>` | "Investigate a task, then cross-check each finding with skeptical reviewers" | `generateAdversarialReviewWorkflow()` | `createCodingTools(cwd)` only |

**Execution path** (identical shape for both):
- If a `WorkflowManager` is provided → `announceLaunch()` (UI-only banner listing the declared phases) then `manager.startInBackground(script, argsObject, { tools })`; returns a `runId` and tells the user to track via `/workflows`. The report is delivered into the conversation when it finishes.
- If **no** manager (headless / tests) → falls back to **inline foreground** `runWorkflow(script, { cwd, args, tools, onPhase })`, then `pi.sendMessage({ customType, content: reportText(result), display: true })`.

`reportText(result)` returns `result.result.report` if it's a non-empty string, else `JSON.stringify(result.result, null, 2)`.

The `args` object passed is `{ question }` for deep-research and `{ task }` for adversarial-review — the user's free text is passed as **data**, never interpolated into the script string.

---

### 2. Deep-research workflow (`src/deep-research.ts`)

```ts
export interface DeepResearchConfig { angles: number; minSupport: number; }
export function generateDeepResearchWorkflow(): string
```

The returned script is **static** and reads `args.question`, `args.angles` (default 4), `args.minSupport` (default 2). Phases & logic:

1. **Queries** — one `agent()` produces `angles` diverse search queries (schema `{ queries: string[] }`).
2. **Gather** — `parallel(queries.map((q,i) => () => agent(...)))`: each agent calls `web_search` then `web_fetch` the top 2 URLs and extracts claims tagged with source URL (schema: `{ sources: [{ url, claims: string[] }] }`). Explicitly told "Do NOT invent sources or claims."
3. **Verify** — one `agent()` cross-checks: a claim survives only if supported by `>= minSupport` distinct source URLs OR one authoritative source (schema `{ supported: [{ claim, sources[] }], discarded: string[] }`).
4. **Report** — one `agent()` writes a cited report from supported claims only.

Returns `{ question, queries, supported, report }`.

Also in this file (used by the workflow editor/tool surface, not the slash command):
```ts
export function generateCodebaseAuditWorkflow(scope: string, checks: string[]): string
```
This one **does** string-interpolate (`escapedScope = scope.replace(/'/g,"\\'").slice(0,60)`, per-check labels), since it's generated from structured inputs. Phases: Individual Checks (parallel per-check agents) → Cross-Validation → Report.

---

### 3. Adversarial-review workflow (`src/adversarial-review.ts`)

```ts
export interface AdversarialReviewConfig { reviewerCount: number; filterContested: boolean; agreementThreshold: number; }
export function generateAdversarialReviewWorkflow(): string
```

Static script reads `args.task`, `args.reviewers` (default 2), `args.threshold` (default 0.5). Phases:

1. **Investigate** — one agent lists individually-checkable findings (schema `{ findings: string[] }`).
2. **Refute** — **nested parallel**: `parallel(findings.map((f,i) => () => parallel(Array.from({length: reviewers}, (_,r) => () => agent('You are a skeptical reviewer. Try to REFUTE this finding...', { schema: { real: boolean, reason?: string } }))).then(votes => ...)))`. Each finding's reviewers vote `real`; `ratio = realCount/total`; `survives = ratio >= threshold`. Reviewers default to `real=false` when uncertain.
3. **Consensus** — one agent writes a final report including only survivors.

Returns `{ total, survivors, report }`.

Also in this file:
```ts
export function generateMultiPerspectiveWorkflow(topic: string, perspectives: string[]): string
```
Interpolates topic/perspectives; phases: Perspective Analysis (parallel) → Synthesis.

These two patterns (deep-research = fan-out search + cross-check verify + synthesis; adversarial-review = investigate + N skeptical reviewers + consensus filter) are the canonical "decompose → fan out across subagents → adversarially verify → synthesize one result" templates a pi-mcp would want pi to reach for.

---

### 4. Web tools (`src/web-tools.ts`)

Run in the **extension host process** (has network access), not in a sandbox. Built with `defineTool` + TypeBox `Type`.

```ts
export function createWebSearchTool(): ToolDefinition   // name: "web_search"
export function createWebFetchTool(maxChars = 6000): ToolDefinition  // name: "web_fetch"
export function createWebTools(): ToolDefinition[]       // [search, fetch]
export function htmlToText(html: string): string
export function parseBingResults(html: string, limit: number): Array<{ url: string; title: string }>
```

- **web_search** params `{ query: string, count?: number }` (clamped 1..10, default 6). Does a **best-effort Bing HTML scrape** of `https://www.bing.com/search?q=...` with a desktop Chrome UA, parses `<h2><a href>` results (filters out bing/microsoft links). Returns text list + `details.results`. No API key.
- **web_fetch** params `{ url: string }`. `fetch()` with 15s `AbortController` timeout, `redirect:"follow"`, strips HTML to text (`htmlToText`), truncates to `maxChars` (6000). Returns `HTTP <status> <url>\n\n<text>` + `details {status,url}`.
- Failures return a text error rather than throwing.

These are the ONLY web tools; they are injected explicitly (deep-research adds them) and are not part of the default coding toolset.

---

### 5. Effort / Ultracode standing modes — EXACTLY how auto-arming works

This is the heart of Area G. Three files cooperate: `effort-command.ts`, `workflow-editor.ts`, and `extensions/workflow.ts`.

#### 5a. State & commands (`src/effort-command.ts`)

```ts
export type EffortLevel = "off" | "high" | "ultra";
export interface EffortState { level: EffortLevel; }
export function createEffortState(): EffortState        // { level: "off" }
export function effortDirective(level: EffortLevel): string | undefined  // HIGH/ULTRA directive text or undefined
export function isSubstantive(text: string): boolean    // t.length >= 16 && !t.startsWith("/")
export function registerEffortCommand(pi: ExtensionAPI, state: EffortState, modeState?: { enabled?: boolean }): void
```

`registerEffortCommand` registers two commands sharing the same `state`:

- **`/effort off|high|ultra`** — `xhigh` and `ultracode` are aliases for `ultra`. Sets `state.level`. Honest copy: tiers are guidance + real `tokenBudget/maxAgents` caps; they do NOT set a provider thinking-effort level.
- **`/ultracode`** — `/ultracode` (no arg) sets `state.level = "ultra"`; `/ultracode off` sets `"off"`. `/ultracode keyword on|off` flips `modeState.enabled` (the in-editor keyword trigger), independent of the standing effort level.

The two directive strings:
- **HIGH_DIRECTIVE**: "Effort: HIGH. Be thorough — use a few parallel reviewers/perspectives and an adversarial verify pass (see verify()/judgePanel()); set a moderate tokenBudget and maxAgents on the workflow tool call."
- **ULTRA_DIRECTIVE**: "Effort: ULTRA. Be exhaustive — fan out widely (more reviewers/judges, deeper loopUntilDry rounds, a completenessCheck at the end), set a generous tokenBudget and a high maxAgents on the workflow tool call, and prefer the big tier for synthesis."

**Honesty note from the source header**: the runtime cannot enforce "reviewer N / loop K" (those live in the script the model writes). Tiers are guidance + the model setting `tokenBudget`/`maxAgents` which ARE genuine runtime ceilings.

#### 5b. The arming hook (`src/workflow-editor.ts`)

```ts
export interface WorkflowModeState { active: boolean; enabled?: boolean; }
export function hasTrigger(text: string): boolean       // TRIGGER = /(?<!\/)(?:workflows?|ultracode)/i
export function keywordTriggerArmed(text: string, enabled: boolean|undefined): boolean  // enabled !== false && hasTrigger(text)
export function buildForcedWorkflowPrompt(text: string, extraDirective?: string): string
export const WORKFLOW_TOOL_NAME = "workflow";
export function installWorkflowEditor(pi, ui, effort?: EffortState, state?: WorkflowModeState): WorkflowModeState
```

`installWorkflowEditor` does two things: installs the rainbow `WorkflowEditor` component, and registers the submit-time forcing hook:

```ts
pi.on("input", (event: { source?: string; text?: string }) => {
  if (event.source !== "interactive" || !event.text) return { action: "continue" };
  const triggered = keywordTriggerArmed(event.text, state.enabled);
  const byEffort = !triggered && !!effort && effort.level !== "off" && isSubstantive(event.text);
  if (!triggered && !byEffort) return { action: "continue" };
  // ... add WORKFLOW_TOOL_NAME to active tools (saving prior set in savedTools) ...
  const extra = byEffort && effort ? effortDirective(effort.level) : undefined;
  return { action: "transform", text: buildForcedWorkflowPrompt(event.text, extra) };
});
pi.on("turn_end", () => { /* restore savedTools */ });
```

`buildForcedWorkflowPrompt(text, extra)` appends, after a `---`:
```
[workflows mode is ON for this message]
You MUST handle this request by calling the tool named exactly `workflow` ...
Write a workflow script that fans the task out across subagents via agent()/parallel()/pipeline().
The ONLY acceptable action is a `workflow` tool call. Do NOT instead:
- answer directly or in prose,
- call the `subagent` tool yourself,
- use any skill or command (e.g. pi-subagents, /code-review, deep-research),
- or interpret the word "workflow/workflows" loosely as some other parallel/audit approach.
Even for a small task, wrap it in a minimal `workflow` call with at least one agent().
```
followed by the effort directive (if any).

So the FORCE mechanism is purely a **prompt transform + tool-set guarantee**. It does not call the workflow tool itself — it instructs the model to, and ensures the tool is callable.

#### 5c. Wiring (`extensions/workflow.ts`)

```ts
const effort = createEffortState();
const workflowMode: WorkflowModeState = { active: false, enabled: false }; // keyword trigger OFF by default
registerEffortCommand(pi, effort, workflowMode);
pi.on("session_start", (_event, ctx: ExtensionContext) => {
  // ... setActiveTools, setMainModel, installResultDelivery, installTaskPanel ...
  if (!editorInstalled) { installWorkflowEditor(pi, ctx.ui, effort, workflowMode); editorInstalled = true; }
});
```

Two crucial defaults:
1. The **keyword trigger is disabled by default** (`enabled: false`) — typing "workflow"/"ultracode" no longer hijacks the message; re-enable with `/ultracode keyword on`. `/effort` and `/ultracode` standing modes are unaffected.
2. `installWorkflowEditor` (which registers the `input` + `turn_end` hooks) is only invoked **inside `session_start`**, and passes `ctx.ui`. The editor component requires a TUI.

---

### 6. Headless behavior — does effort fire in `pi -p --mode json`?

**No.** Two independent gates block it:

1. **InputSource gate.** `node_modules/@earendil-works/pi-coding-agent/dist/core/extensions/types.d.ts:570`:
   ```ts
   export type InputSource = "interactive" | "rpc" | "extension";
   ```
   The hook's first line `if (event.source !== "interactive") return { action: "continue" }`. Headless prompts arrive as `"rpc"` (or otherwise non-interactive), so the transform never runs.
2. **UI gate.** The hook is only registered from within `session_start` → `installWorkflowEditor(pi, ctx.ui, ...)`, which is part of the interactive editor setup. (`pi --help` confirms `--mode text|json|rpc` and `--print/-p` non-interactive.)

Therefore `/effort` / `/ultracode` are **interactive-only** affordances and cannot be relied on to coerce a workflow in headless mode.

---

### 7. Saved-workflow storage (`src/workflow-saved.ts`, `src/saved-commands.ts`)

#### On-disk format
```ts
export interface SavedWorkflow {
  name: string; description: string; script: string;
  parameters?: Record<string, { type: string; description?: string; required?: boolean; default?: unknown }>;
  location: "project" | "user"; path: string; savedAt: string; // ISO
}
```
Stored as **one JSON file per workflow**: `<name>.json`, pretty-printed (`JSON.stringify(saved, null, 2)`), in:
- Project: `<cwd>/.pi/workflows/saved/` (`WORKFLOW_SAVED_DIR`)
- User: `~/.pi/workflows/saved/` (`USER_WORKFLOW_SAVED_DIR`, `~` expanded to `$HOME`)

#### WorkflowStorage API (`createWorkflowStorage(cwd)`)
| Method | Signature | Behavior |
|---|---|---|
| save | `save(wf: Omit<SavedWorkflow,"path"\|"savedAt">, location="project"): SavedWorkflow` | ensures dir, writes `<name>.json`, stamps `savedAt`/`path` |
| load | `load(name): SavedWorkflow \| null` | **project then user** (project precedence); reads + JSON.parse |
| list | `list(): SavedWorkflow[]` | reads `*.json` in both dirs, sorted by `name.localeCompare` |
| delete | `delete(name, location?): boolean` | unlinks in given location, or both |
| exists | `exists(name, location?): boolean` | checks file existence in given location, or either |

#### Registration as slash commands (`src/saved-commands.ts`)
- `registerAllSavedWorkflows(pi, cwd, storage, manager?)` iterates `storage.list()` and calls `registerSavedWorkflow` for each.
- `registerSavedWorkflow(pi, cwd, wf, manager?, exists?)` registers a `/<name>` command (idempotent via `isRegistered`). Handler: parses args, then either `manager.startInBackground(wf.script, parseCommandArgs(args, wf.parameters))` (background, visible in `/workflows` TUI/task panel) or inline `runWorkflow(...)` fallback. Result delivered via `pi.sendMessage({ customType: 'workflow:<name>', content: reportText(result) })`.
- **Pi has no `unregisterCommand`** — a deleted workflow's command lingers in-session; the `exists` predicate makes the handler warn "reload the session" instead of running a deleted workflow.
- `parseCommandArgs(raw, parameters?)`: splits whitespace tokens; `key=value` → `out[key]=value`; bare tokens → joined into `out._`; also sets `out._raw`; fills missing keys from `parameters[k].default`.
- `promptSaveWorkflow(...)`: Claude-Code-style modal (name → location select `project (.pi)`/`user (~/.pi)` → description → overwrite confirm via `storage.exists`). Used by `/workflows save` (UI path) and the navigator's `s`.

#### Invoking a saved workflow programmatically
The `WorkflowManager` is created with `loadSavedWorkflow: (name) => storage.load(name)?.script` (see `extensions/workflow.ts`). The workflow tool's guidelines expose: `await workflow('saved-name', argsObject)` to run a saved workflow **inline, nesting one level deep**, under the global 16-concurrent / 1000-total caps.

---

### 8. Worktree isolation (`src/worktree.ts`)

```ts
export interface Worktree { isolated: boolean; cwd: string; branch?: string; repoRoot?: string; reason?: string; }
export async function createWorktree(baseCwd: string, name: string): Promise<Worktree>
export async function removeWorktree(wt: Worktree): Promise<void>
```

- `createWorktree`: `git -C baseCwd rev-parse --show-toplevel` → repoRoot; then `git -C repoRoot worktree add -b pi/wf/<slug> <repoRoot>/.pi/worktrees/<slug> HEAD`. `slug(name)` lowercases, hyphenates non-alphanumerics, trims, slices to 32 chars (fallback `"agent"`).
- On non-git or any failure → **no-op Worktree** `{ isolated: false, cwd: baseCwd, reason }` (agent just runs in the shared tree).
- `name` MUST be deterministic (runId + call index, never wall-clock) so resume keys stay stable.
- Results are **NOT auto-merged** — the worktree path is surfaced for the caller to inspect.
- `removeWorktree`: best-effort `git worktree remove --force <cwd>` then `git branch -D <branch>`; safe on a no-op Worktree.

This is requested per-agent via `isolation: "worktree"` (the agent layer, Area covered elsewhere, calls these helpers).

---

### 9. Config knobs (`src/config.ts`)

| Constant | Value | Meaning |
|---|---|---|
| `MAX_AGENTS_PER_RUN` | `1000` | hard cap on agents per run |
| `DEFAULT_AGENT_TIMEOUT_MS` | `5 * 60 * 1000` | per-agent timeout (5 min) |
| `MAX_CONCURRENCY` | `16` | max concurrent agents (matches Claude Code) |
| `DEFAULT_TOKEN_BUDGET` | `null` | no limit by default |
| `WORKFLOW_RUNS_DIR` | `.pi/workflows/runs` | persisted run state + logs |
| `WORKFLOW_SAVED_DIR` | `.pi/workflows/saved` | project saved workflows |
| `USER_WORKFLOW_SAVED_DIR` | `~/.pi/workflows/saved` | user saved workflows |
| `MODEL_TIERS_FILE` | `.pi/workflows/model-tiers.json` | per-tier model config (home-relative) |
| `USER_WORKFLOW_CONSENT_FILE` | `~/.pi/workflows/consent.json` | one-time multi-agent warning flag |
| `AGENTS_DIR` | `.pi/agents` | named subagent `*.md` definitions (project + home) |

Verified on disk: `~/.pi/workflows/model-tiers.json` has top-level keys `tiers`, `rules`, `fallback`; `~/.pi/workflows/consent.json` = `{ "multiAgentWarningAccepted": true }`.

---

### 10. Logger (`src/logger.ts`)

```ts
export interface WorkflowLogger { log(m): void; error(m): void; warn(m): void; getLogs(): string[]; persist(): string | null; }
export interface WorkflowLoggerOptions { runId?: string; cwd?: string; persist?: boolean; onLog?: (m:string)=>void; }
export function createWorkflowLogger(options?: WorkflowLoggerOptions): WorkflowLogger
```

- Each entry: `[<ISO timestamp>] [<LEVEL>] <message>` (LEVEL ∈ INFO/ERROR/WARN). Buffered in-memory (`getLogs()` returns a copy) and, when `persist !== false` (default true), appended to `<cwd>/.pi/workflows/runs/<runId>.log`.
- `runId` defaults to `run-${Date.now()}`. `persist()` rewrites the whole buffer to the file and returns the path (or null when persistence disabled). All disk writes are try/catch silent-fail.
- Confirmed on disk: `.pi/workflows/runs/run-<id>.log` (logs) alongside `<runId>.json` and `.json.bak` (persisted run state, Area F territory).

---

### 11. Preflight consent (`src/workflow-tool.ts` + `src/workflow-consent.ts`)

The `workflow` tool gates a run behind a one-time multi-agent warning ONLY when `ctx.hasUI && !consent.isAccepted()`. `runPreflightApproval` shows a `ui.confirm` listing phases + caps (`buildPreflightLines`), and on approval calls `consent.accept()` (writes `~/.pi/workflows/consent.json = { multiAgentWarningAccepted: true }`). **Headless/RPC has no UI → never prompts → runs straight through.** Already accepted on this machine.

**Implicações para pi-mcp:**
- CANNOT exploit /effort or /ultracode to force a workflow headlessly. The auto-arming input hook bails on `event.source !== "interactive"` and InputSource only has values interactive|rpc|extension; pi -p / --mode json / --mode rpc deliver non-interactive input. The hook is also only installed inside session_start with ctx.ui. So the effort standing modes are dead in headless.
- DO force a workflow headlessly by REPLICATING buildForcedWorkflowPrompt in the MCP prompt text. The actual forcing is just (a) a prompt transform that says 'you MUST call the tool named exactly `workflow` ... ONLY acceptable action is a workflow tool call' plus optional effortDirective, and (b) ensuring the `workflow` tool is active. pi-mcp should prepend/append that exact directive block to the TASK it sends to `pi -p`, and pass --tools including the workflow tool (the extension already adds it on session_start: setActiveTools([...active, 'workflow']), which DOES run headlessly).
- To bias fan-out breadth without interactive effort modes, append effortDirective('high') / effortDirective('ultra') text verbatim (these are plain strings) and set genuine ceilings: the workflow tool input supports `maxAgents` (default 1000), `tokenBudget` (hard cap), `agentTimeoutMs` (default 300000), and `background` (default true). Hard runtime caps are MAX_CONCURRENCY=16 and MAX_AGENTS_PER_RUN=1000 — pi-mcp should treat 16 as the real parallelism ceiling regardless of how many agents the script spawns.
- YES, saved workflows can be invoked by name. Two routes: (1) each saved workflow is a /<name> slash command — but slash commands fire through the interactive command layer, so headless invocation is uncertain; (2) RELIABLE route: the workflow tool itself supports `await workflow('saved-name', argsObject)` inline (one level of nesting), wired via WorkflowManager.loadSavedWorkflow -> storage.load(name).script. pi-mcp can instruct pi to call a saved workflow by name this way, or simply read the SavedWorkflow JSON from .pi/workflows/saved/<name>.json (or ~/.pi/...) and pass its `.script` field directly as the workflow tool's `script` param.
- pi-mcp's visibility tools should read the documented on-disk layout: run state + logs in <cwd>/.pi/workflows/runs/ (<runId>.json, <runId>.json.bak, run-<id>.log), saved workflows in <cwd>/.pi/workflows/saved/*.json and ~/.pi/workflows/saved/*.json (project precedence), model tiers in ~/.pi/workflows/model-tiers.json (keys tiers/rules/fallback), and consent in ~/.pi/workflows/consent.json. SavedWorkflow JSON fields are stable: name, description, script, parameters?, location, path, savedAt.
- Headless runs WON'T be blocked by the preflight consent card (ctx.hasUI false), and on this machine consent is already accepted anyway. So a headless `pi -p` workflow tool call executes immediately with no human gate — exactly what pi-mcp wants. To keep runs visible/stoppable, ensure the same shared WorkflowManager is used (it persists run files), which the bundled extension already sets up.
- The deep-research and adversarial-review SCRIPTS are static, parameterized via `args`, and safe from injection — pi-mcp can reuse generateDeepResearchWorkflow()/generateAdversarialReviewWorkflow() output as ready-made multi-model fan-out templates, passing {question}/{task} as args. Note deep-research needs the web tools injected at runtime (createWebTools, Bing-scrape based, no API key) — they only work when the workflow runs in a context that injects them; a raw `pi -p` workflow tool call would NOT have web_search/web_fetch unless those tools are added to the run, so prefer the /deep-research command path or inject tools.
- Worktree isolation (isolation:'worktree') gives parallel agents conflict-free edits at <repoRoot>/.pi/worktrees/<slug> on branch pi/wf/<slug>, but results are NOT auto-merged and it silently no-ops outside a git repo. If pi-mcp delegates code-editing fan-outs, it must surface/collect the per-worktree paths itself and handle merging; and must use deterministic agent names (runId+index) so resume works.

**Questões em aberto:**
- Do per-saved-workflow /<name> slash commands actually dispatch in headless `pi -p`/--mode json, or only interactive? The workflow-commands.ts /workflows handler has hasUI-gated branches but the saved-command handlers themselves call manager.startInBackground regardless — needs a live probe of `pi -p "/somesavedworkflow ..."`. (Area D/RPC may cover slash-command dispatch headlessly.)
- Confirm that the bundled extension is actually loaded by the system `/usr/bin/pi` binary (vs only the fork's local dist). The fork registers the workflow tool + setActiveTools on session_start; whether the production pi the MCP shells out to has this extension installed needs verification via `pi config` / settings.json extension list.
- When invoking a saved workflow via `await workflow('name', args)` inline, does loadSavedWorkflow honor project-vs-user precedence and the parameters defaults the same way parseCommandArgs does for the slash-command path? The tool path passes args straight through; the command path runs parseCommandArgs — a possible behavioral difference for parameterized saved workflows.
- Exact value of event.source for a `pi -p` prompt vs `--mode rpc` emitInput — code shows emitInput(text, images, source, ...) so the host chooses; need to confirm whether -p uses 'rpc' or some other path, to be 100% sure the effort hook stays inert (current evidence strongly implies it does).


---

# The /workflows command tree, navigator, task panel, editor, and render/builder functions

**Arquivos examinados:** /root/projects/pi-dynamic-workflows-custom/src/workflow-commands.ts, /root/projects/pi-dynamic-workflows-custom/src/workflow-ui.ts, /root/projects/pi-dynamic-workflows-custom/src/task-panel.ts, /root/projects/pi-dynamic-workflows-custom/src/workflow-editor.ts, /root/projects/pi-dynamic-workflows-custom/src/display.ts, /root/projects/pi-dynamic-workflows-custom/src/workflow-saved.ts, /root/projects/pi-dynamic-workflows-custom/src/saved-commands.ts, /root/projects/pi-dynamic-workflows-custom/src/run-persistence.ts, /root/projects/pi-dynamic-workflows-custom/src/config.ts, /root/projects/pi-dynamic-workflows-custom/src/index.ts, /root/projects/pi-dynamic-workflows-custom/src/workflow-manager.ts, /root/projects/pi-dynamic-workflows-custom/extensions/workflow.ts

**Fatos-chave:**
- The `/workflows` slash command is registered by `registerWorkflowCommands(pi, manager, { storage?, cwd? })` in src/workflow-commands.ts. Subcommands: (no args / `ui`) opens the interactive navigator; `list` prints plain text; `status <id>` (alias `watch`); `status <id> --script` prints the raw persisted JS script; `stop|pause|resume|rm <id>`; `save <name> [runId]`.
- All command data comes from the shared `WorkflowManager`: `listRuns(): PersistedRunState[]` (session-filtered), `getRun(runId): ManagedRun | undefined` (live), `getSnapshot(runId): WorkflowSnapshot | null`, plus mutators `stop/pause/resume/deleteRun/startInBackground/restartAgent`. The MCP can mirror EVERYTHING the TUI shows by reading the same persisted JSON files directly — no TUI code is needed.
- Persisted run files live at `.pi/workflows/runs/<runId>.json` (constant WORKFLOW_RUNS_DIR) with atomic tmp+rename writes and a `.bak` sidecar. Saved workflows live at `.pi/workflows/saved/<name>.json` (project) and `~/.pi/workflows/saved/<name>.json` (user). These exact paths/schemas are what an MCP visibility tool must read.
- PersistedRunState schema (run-persistence.ts) is the canonical on-disk record: runId, workflowName, script, args?, sessionId?, status (pending|running|paused|completed|failed|aborted), phases[], currentPhase?, agents[] (PersistedAgentState: id, callIndex?, label, phase?, prompt, status, result?, error?, startedAt?, endedAt?, model?, toolCalls?), logs[], result?, startedAt, updatedAt, completedAt?, durationMs?, tokenUsage?{input,output,total,cost?,cacheRead?,cacheWrite?}, journal?[].
- The navigator (`openWorkflowNavigator` in workflow-ui.ts) is a 5-view drill-down state machine: runs -> phases -> agents -> detail, plus runs -> savedDetail. Rendering (`renderNavigator`) and key mapping (`keyToAction`) are PURE and unit-tested; only the pi-tui Component shell touches live events. Keys: up/down or j/k select, enter/right drill, esc/left back, q close; on runs: p pause, x stop, r restart, s save; on saved: x delete; in agents/detail: r restartAgent.
- The editor (`installWorkflowEditor` in workflow-editor.ts) is a `WorkflowEditor extends CustomEditor` that colorizes the trigger words `workflow`/`workflows`/`ultracode` (not preceded by `/`) with a flowing rainbow; when armed at submit, the `input` hook rewrites the message via `buildForcedWorkflowPrompt()` to force a `workflow` tool call. The keyword trigger is OFF by default (`enabled: false`); the model decides when to call the tool.
- The task panel (`installTaskPanel` / `renderPanel` in task-panel.ts) is an informational below-editor widget listing only RUNNING/PAUSED background runs; foreground runs are excluded (they render their own widget). `installResultDelivery` re-injects a finished BACKGROUND run's result into the conversation via `pi.sendMessage({customType:'workflow-result'}, {triggerTurn:true, deliverAs:'followUp'})`.

## Overview

Area H is the human-facing TUI/command surface of the pi-dynamic-workflows extension. It is built entirely on the `@earendil-works/pi-coding-agent` extension API (`ExtensionAPI`, `ExtensionUIContext`, `ExtensionCommandContext`) and `@earendil-works/pi-tui` (`Editor`/`CustomEditor`, `Component`, `parseKey`, `truncateToWidth`, `visibleWidth`). **None of this matters to the Go MCP server's runtime** — but the data it visualizes (persisted run JSON) is exactly what the MCP's visibility tools should read. Everything below is driven by a single shared `WorkflowManager` instance created in `extensions/workflow.ts`.

### Wiring (extensions/workflow.ts)

```ts
const cwd = process.cwd();
const storage = createWorkflowStorage(cwd);
const manager = new WorkflowManager({ cwd, loadSavedWorkflow: (name) => storage.load(name)?.script });
pi.registerTool(createWorkflowTool({ cwd, manager, storage }));
registerWorkflowCommands(pi, manager, { storage, cwd });   // /workflows
registerWorkflowModelsCommand(pi);                          // /workflows-models (separate file)
registerBuiltinWorkflows(pi, { cwd, manager });
registerAllSavedWorkflows(pi, cwd, storage, manager);       // /<savedName> commands
// session_start:
manager.setSessionId(ctx.sessionManager?.getSessionId());   // scopes listRuns() to the session
installResultDelivery(pi, manager);
installTaskPanel(pi, manager, ctx.ui, { storage, cwd });
installWorkflowEditor(pi, ctx.ui, effort, workflowMode);
```

Note `workflowMode` defaults to `{ active: false, enabled: false }` — the colorized keyword trigger is **off by default**; the model decides when to call the `workflow` tool.

---

## 1. The `/workflows` command tree (src/workflow-commands.ts)

Registered via:

```ts
export function registerWorkflowCommands(
  pi: ExtensionAPI,
  manager: WorkflowManager,
  opts: WorkflowCommandOptions = {},   // { storage?: WorkflowStorage; cwd?: string }
): void
```

Registration is idempotent (checks `pi.getCommands()` for an existing `workflows`). Command description string:

> `"Manage workflow runs — no args (opens navigator) | status/stop/pause/resume <id> | rm <id> | save <name> [runId]"`

The handler parses `args` into `parts`, takes `sub = parts[0] ?? "list"` and `id = parts[1]`. USAGE string:

> `Usage: /workflows [list] | status <id> [--script] | watch <id> | stop <id> | pause <id> | resume <id> | rm <id> | save <name> [runId]`

### Subcommand table

| Subcommand | Behavior | Data source |
|---|---|---|
| (no args) / `ui` | If `ctx.hasUI`, opens the interactive navigator (`openWorkflowNavigator`). | manager + storage |
| `list` | Always plain text. Prints `manager.listRuns()` via `summarizeRun()` (one line each) + USAGE. Empty → "No workflow runs yet…". | `manager.listRuns()` |
| `status <id>` (alias `watch`) | If the run is currently `running`, calls `watchRun()` → streams a one-line progress to the status bar (`ctx.ui.setStatus("wf:<id>", …)`) and prints the final snapshot on completion. Otherwise prints `renderWorkflowText(recomputeWorkflowSnapshot(live), false)` for a live run, or `renderPersistedStatus(run)` for a disk-only run. No id → notify USAGE warning. | `getRun`, `getSnapshot`, `listRuns` |
| `status <id> --script` | Prints the **raw persisted JS script** in a fenced ```js block (Claude Code's "View raw script"). Falls back to `(no script persisted)`. Resolves `targetId` from `id` or `parts[2]`. | `listRuns().find(r=>r.runId===id).script` |
| `stop <id>` | `manager.stop(id)` → notify "Stopped"/"Cannot stop (not running)". | manager.stop |
| `pause <id>` | `manager.pause(id)` → notify "Paused"/"Cannot pause". | manager.pause |
| `resume <id>` | `await manager.resume(id)` → notify "Resumed"/"Resume not available yet". | manager.resume |
| `rm <id>` | `manager.deleteRun(id)` → notify "Removed"/"No run". | manager.deleteRun |
| `save <name> [runId]` | Requires `opts.storage`. Picks the named run or the most recent run that still has a `.script`. With UI → `promptSaveWorkflow()` modal; headless → saves directly to `project` location + `registerSavedWorkflow`. | manager.listRuns + storage |
| default | notify `Unknown subcommand "<sub>". <USAGE>`. | — |

**`watchRun(manager, pi, ctx, id)`** subscribes to manager events `["agentStart","agentEnd","phase","log"]` (progress) and `["complete","error","stopped","paused"]` (final), streaming `oneLineProgress(snapshot)` to a `wf:<id>` status key and printing `renderWorkflowText(...)` once. Cleans up all listeners on settle.

**Output helpers (exported / module-level):**

- `summarizeRun(run: PersistedRunState): string` → `"◆ <runId>  <name> [status] done/total agents · N,NNN tok"`. STATUS_ICON map: `pending:· running:◆ paused:|| completed:✓ failed:✗ aborted:⊘`.
- `oneLineProgress(snapshot): string` → `"◆ <name>: done/total done[, R running][, E err][ · phase]"`.
- `export function renderPersistedStatus(run: PersistedRunState): string` → multi-line status block with per-agent icons (`✓/✗/●/·`), phase, tokens, duration.

All conversation output goes through `pi.sendMessage({ customType: "workflows", content, display: true })`.

---

## 2. The interactive navigator (src/workflow-ui.ts)

### Entry point

```ts
export function openWorkflowNavigator(
  pi: ExtensionAPI,
  manager: WorkflowManager,
  ui: ExtensionUIContext,
  opts: NavigatorOptions = {},   // { storage?: WorkflowStorage; cwd?: string }
): Promise<void>
```

Opens a focused, bottom-anchored full-width overlay via `ui.custom<void>(...)` with `{ overlay: true, overlayOptions: { width: "100%", maxHeight: "100%", anchor: "bottom-center", margin: 0 } }`. Subscribes to manager events `["agentStart","agentEnd","phase","log","complete","error","stopped","paused","resumed"]` for live re-render; disposes listeners on close. Resolves when the user escapes at the top level or presses `q`.

### View state machine

`type ViewKind = "runs" | "phases" | "agents" | "detail" | "savedDetail"`. Drill path:

```
runs ──enter──▶ phases ──enter──▶ agents ──enter──▶ detail
     ◀──esc───        ◀──esc────         ◀──esc────
     (saved items in runs view) ──enter──▶ savedDetail
```

**`class NavigatorState`** — a stack of `StackFrame { kind, cursor, runId?, phase?, agentId?, savedName? }` plus a `scroll` for detail views. Methods: `move(delta,count)`, `drill(model): boolean`, `back(): boolean`, `clamp(count)`, `itemKindAt(model,cursor): "run"|"saved"`, `activeRunId(model)`, getters `kind/cursor/runId/phase/agentId/savedName/depth`. In runs view, cursors `< runs.length` are runs; the rest index into saved workflows.

**`class NavigatorModel`** — adapter over `Pick<WorkflowManager,"listRuns"|"getRun">` plus optional storage `{ list(); delete() }`. Prefers the live `getRun()` snapshot, falling back to `persistedToSnapshot(PersistedRunState)`. Public methods:

```ts
runs(): RunRow[]              // {runId,name,status,done,total,tokens,cost,script?}
saved(): SavedWorkflow[]      // sorted by name; [] if no storage
deleteSaved(name): boolean
runName(runId): string
runStatus(runId): string
runSnapshot(runId): WorkflowSnapshot | undefined
phases(runId): PhaseRow[]     // {title,done,total,tokens}
agents(runId, phase): AgentRow[]  // {id,label,status,phase?,tokens?,model?}
agentDetail(runId, agentId): WorkflowAgentSnapshot | undefined
```

Phase bucketing uses fuzzy title matching (`phaseTitlesMatch` → trim/lowercase + startsWith either way) so agent `phase` strings reconcile to declared phase titles; unmatched → `"(no phase)"`.

### Pure rendering: `renderNavigator`

```ts
export function renderNavigator(
  state: NavigatorState,
  model: NavigatorModel,
  width: number,
  theme: ThemeLike = PLAIN,   // { fg(color,text); bold(text) }
  viewportRows = 24,
): string[]
```

Per-view content:
- **runs**: `◆ <name>  done/total · N done · N,NNN tok · $cost  <dim runId>`; a `── saved ──` separator then saved rows `<name>  <desc>  <loc(~|.)>`.
- **phases**: header `runName` + `<desc> done/total agents · Ns`, a windowed phase list (`windowAroundCursor`), and a `borderedBox` preview of the selected phase's agents (rounded `╭─╮` box, max 44 wide / 8 tall).
- **agents**: `runName › phase` header + rows `<icon> <label>  <model · tok>`.
- **detail** (`WorkflowAgentSnapshot`): status, Model (provider prefix stripped via `shortModel`), Tokens, Error (suppressing the redundant `"interrupted"`), Tool calls list, Prompt (wrapped, char count), and a status-aware "Result/transcript" ladder (`resultPreview`, else state-specific message). Scrollable via `pushScrollable` (fixed viewport, clamps `state.scroll`, shows `[a-b / N]`).
- **savedDetail** (`SavedWorkflow`): Description, Location (`user (~/.pi)` / `project (.pi)`), Saved at, Parameters JSON, and the full Script (wrapped, scrollable).

Footer hints built by `footerHint` + `fitFooterParts(parts: FooterPart[], width)` — parts have a `priority` (4=escape hatch never dropped first, 3=primary action, 2=run controls, 1=nav); lowest-priority/right-most dropped until the footer fits, always keeping ≥1 part.

### Key mapping (pure): `keyToAction`

```ts
export function keyToAction(keyId: string | undefined, kind: ViewKind, itemKind?: "run"|"saved"): NavAction
```

`NavAction` union: `move{delta}` | `drill` | `back` | `close` | `pause` | `stop` | `restart` | `restartAgent` | `save` | `deleteSaved` | `none`. Bindings: `up/k`→-1, `down/j`→+1, `enter/return/right`→drill (none in detail/savedDetail), `escape/esc/left`→back, `q`→close, `p`→pause, `x`→stop OR deleteSaved (saved item / savedDetail), `r`→restart OR restartAgent (in agents/detail), `s`→save (none on saved item).

The `act()` closure inside `openWorkflowNavigator` maps actions to manager calls: `manager.pause/stop`; `restart` → `manager.startInBackground(run.script, run.args)` and notifies the new runId; `restartAgent` → `manager.restartAgent(runId, agentId): { ok, reason? }`; `save` → `promptSaveWorkflow(...)`; `deleteSaved` → `model.deleteSaved(name)`.

---

## 3. Task panel (src/task-panel.ts)

```ts
export function installTaskPanel(_pi, manager, ui, _opts = {}): void   // ui.setWidget("workflow-tasks", …, {placement:"belowEditor"})
export function renderPanel(manager, theme, width = +Infinity): string[]
export function deliverText(run: ManagedRun): string
export function installResultDelivery(pi, manager): void
```

- **`renderPanel`** lists only `running`/actively-`paused` background runs (`manager.getRun(r.runId)?.background !== false`), excluding foreground runs (which render their own widget) and orphaned paused runs (reconciled after `/reload` with no live entry). Output:
  ```
  Workflows (2 running)        ← theme.bold
    ◆ deep-research  5/8 agents · Analysis phase
    ◆ code-review    3/5 agents · Fix phase
    /workflows — open navigator (3 finished)   ← dim; "(N finished)" dropped if too narrow
  ```
  Returns `[]` when nothing is active (panel never lingers). Re-renders on `RUN_EVENTS = ["agentStart","agentEnd","phase","log","complete","error","stopped","paused","resumed"]`. Purely informational — takes **no input**.

- **`installResultDelivery`** is idempotent (`manager.__deliveryInstalled` guard, refreshes `pi` ref on re-call). On `complete` for a **background** run it injects `deliverText(run)` back into the conversation; on `error` injects a failure line. Delivery: `pi.sendMessage({ customType:"workflow-result", content, display:true }, { triggerTurn:true, deliverAs:"followUp" })` — continues the paused turn without interrupting an active one. `deliverText` builds `workflowFinishedText(snapshot)` header + `summarizeResult(run.result?.result)` (prefers `verdict`/`report`/`summary` string fields, else string, else ≤400-char JSON).

---

## 4. Workflows-mode editor (src/workflow-editor.ts)

```ts
export class WorkflowEditor extends CustomEditor   // overrides render() + handleInput() only
export function installWorkflowEditor(pi, ui, effort?, state = {active:false,enabled:true}): WorkflowModeState
export function buildForcedWorkflowPrompt(text: string, extraDirective?: string): string
export const WORKFLOW_TOOL_NAME = "workflow";
export const RAINBOW: number[]    // 256-color ring
// helpers: hasTrigger, endsWithTrigger, keywordTriggerArmed, colorizeWorkflow, tokenizeAnsi
```

- Trigger regex `TRIGGER = /(?<!\/)(?:workflows?|ultracode)/i` (case-insensitive substring, NOT preceded by `/` so slash commands are left alone). `_G` and `_AT_END` variants for colorizing and end-detection.
- `WorkflowModeState { active: boolean; enabled?: boolean }` is the shared armed/enabled flag. `keywordTriggerArmed(text, enabled)` = `enabled !== false && hasTrigger(text)`.
- Editor `render()` colorizes trigger words with a flowing rainbow (animated at 90 ms via an unref'd `setInterval` while focused+armed); only the text lines (not the editor borders) are recolored. `tokenizeAnsi` safely splits CSI/OSC/APC escapes (incl. the zero-width cursor marker) so colorizing never corrupts them.
- `handleInput`: first Backspace right after a trigger word **disarms** (non-destructive); typing a fresh trigger re-arms.
- The `input` hook (`pi.on("input")`) arms when (a) the keyword is present & enabled, or (b) standing `/effort` mode is on and the message `isSubstantive`. When armed it (1) ADDS `"workflow"` to the active tool set (saving the original in `savedTools`, restored on `turn_end`) and (2) returns `{ action: "transform", text: buildForcedWorkflowPrompt(text, extra) }`, forcing a `workflow` tool call. It checks `event.text` directly (not editor state, which is reset synchronously by submit).

---

## 5. Shared render/builder functions (src/display.ts)

Core types: `WorkflowAgentSnapshot`, `WorkflowSnapshot`, `WorkflowDisplay`, `WorkflowDisplayOptions`. Functions:

```ts
createWorkflowSnapshot(meta: WorkflowMeta): WorkflowSnapshot
recomputeWorkflowSnapshot(snapshot): WorkflowSnapshot           // recomputes running/done/error counts
createWidgetWorkflowDisplay(ctx, options): WorkflowDisplay      // setWidget + optional setStatus
createToolUpdateWorkflowDisplay(onUpdate, ctx?, options): WorkflowDisplay  // streams tool result text + widget
renderWorkflowLines(snapshot, options): string[]               // CC-style phase/agent layout
renderWorkflowText(snapshot, completed=false): string          // header + lines, for tool-result streaming
workflowFinishedText(snapshot): string                          // ✓ Workflow "name" finished (N agents · T tokens · $cost · Ys).
preview(value, max=80): string
```

`renderWorkflowText` headers are confirmed-from-binary CC strings: `"running dynamic workflow"` (in-progress) / `"Dynamic workflow completed"` (pi-native completed). Status **glyphs** (`agentStatusIcon`, `phaseMarker`) are documented as deliberate reconstructions — the comment notes the textual status strings are confirmed but the icon glyphs are NOT confirmed from the CC binary. `createToolUpdateWorkflowDisplay` decides `streamToolUpdates = options.streamToolUpdates ?? !ctx?.hasUI` — i.e. **in headless mode it streams the rendered text into tool-result `content`**, which is the path the MCP's `pi -p --mode json` driver will surface.

---

## 6. On-disk schemas the MCP can read directly

These are the persisted records every TUI view ultimately reflects (constants in src/config.ts):

| Constant | Path |
|---|---|
| `WORKFLOW_RUNS_DIR` | `.pi/workflows/runs` (per-run `<runId>.json` + `.bak`/`.tmp`) |
| `WORKFLOW_SAVED_DIR` | `.pi/workflows/saved` (project) |
| `USER_WORKFLOW_SAVED_DIR` | `~/.pi/workflows/saved` (user) |
| `MODEL_TIERS_FILE` | `.pi/workflows/model-tiers.json` |
| `USER_WORKFLOW_CONSENT_FILE` | `~/.pi/workflows/consent.json` |
| `AGENTS_DIR` | `.pi/agents` (project + home; project wins) |

`PersistedRunState` (run-persistence.ts) and `SavedWorkflow` (workflow-saved.ts) are the two JSON shapes to mirror. `generateRunId()` = `<base36 timestamp>-<6 base36 random>`. `list()` sorts runs by `updatedAt` desc; saved by `name` asc.

**Implicações para pi-mcp:**
- The MCP's visibility tools should mirror exactly the data the TUI consumes by reading persisted JSON directly: per-run files at `<cwd>/.pi/workflows/runs/<runId>.json` (schema = `PersistedRunState`), and saved workflows at `<cwd>/.pi/workflows/saved/*.json` + `~/.pi/workflows/saved/*.json` (schema = `SavedWorkflow`). No TUI code or pi-tui dependency is required.
- Map MCP visibility verbs onto manager semantics the TUI uses: list-runs = read+sort runs dir by `updatedAt` desc (note the TUI filters by `sessionId`; the MCP likely wants ALL runs, equivalent to `listAllRuns()`); status/detail = parse one run JSON; view-raw-script = the `script` field (this is the `/workflows status <id> --script` feature); list-saved = read saved dirs. `stop/pause/resume/rm/restart` require a LIVE manager in the pi process — reading files cannot mutate a run, so control actions must be driven through pi itself (headless command invocation), not by editing JSON.
- The headless render path is `createToolUpdateWorkflowDisplay` with `streamToolUpdates = !ctx.hasUI` (true in `pi -p --mode json`): progress is emitted into tool-result `content` as `renderWorkflowText(snapshot)` text (headers `running dynamic workflow` / `Dynamic workflow completed`). The MCP driving pi headless will see these strings, not the navigator overlay; plan parsing/structured reads around the JSON files + tool-result JSON, not terminal glyphs.
- Background-run completion is re-injected into pi's conversation via `pi.sendMessage({customType:'workflow-result'}, {triggerTurn:true, deliverAs:'followUp'})`. For the MCP, the authoritative final result is the run JSON's `result`/`tokenUsage`/`durationMs`/`completedAt` fields and (for the synthesized answer) the `verdict`/`report`/`summary` preference order used by `summarizeResult` — reuse that same field-precedence when extracting one synthesized result.
- Confirm to the design: NOTHING about TUI rendering (rainbow editor, overlays, borderedBox, footer fitting, status glyphs) affects the Go server. The editor's keyword-forcing is OFF by default and is purely an interactive affordance; the MCP forces workflow behavior through the prompt/tool it sends to headless pi, not through the editor.
- Run/saved file writes are atomic (tmp+rename) with `.bak` sidecars and may transiently leave `.tmp`/`.bak` files — the MCP reader should filter to `*.json` (excluding `.bak`/`.tmp`) exactly as `RunPersistence.list()` does, and tolerate a corrupt primary by falling back to `<id>.json.bak`.

**Questões em aberto:**
- `manager.resume()` and `manager.restartAgent()` are referenced by the TUI but their full bodies/edge-cases live in workflow-manager.ts (Area covering the manager) — confirm whether resume reconstructs a `ManagedRun` purely from the persisted journal so the MCP could trigger resume on a run from a prior session.
- The navigator filters runs by `sessionId` (set from `ctx.sessionManager.getSessionId()`); it is unclear how the MCP, driving pi headless per-task, will obtain/stabilize a session id so its background runs are discoverable — or whether the MCP should simply ignore session scoping and read all run files.
- `/workflows-models` is registered separately (registerWorkflowModelsCommand in workflows-models-command.ts) and was out of scope here; its command surface (model-tier listing/editing) was not read and may also be MCP-mirrorable.


---

# pi-dynamic-workflows-custom: origin, build/load/reload loop, model-routing design, and stability for pi-mcp

**Arquivos examinados:** /root/projects/pi-dynamic-workflows-custom/README.md, /root/projects/pi-dynamic-workflows-custom/HANDOFF.md, /root/projects/pi-dynamic-workflows-custom/LOCAL_SETUP.md, /root/projects/pi-dynamic-workflows-custom/CC_AUDIT_REPORT.md, /root/projects/pi-dynamic-workflows-custom/package.json, /root/projects/pi-dynamic-workflows-custom/tsconfig.json, /root/projects/pi-dynamic-workflows-custom/biome.json, /root/projects/pi-dynamic-workflows-custom/extensions/workflow.ts, /root/projects/pi-dynamic-workflows-custom/scripts/run-unit-tests.mjs, /root/projects/pi-dynamic-workflows-custom/src/index.ts, /root/projects/pi-dynamic-workflows-custom/src/effort-command.ts (grep), /root/projects/pi-dynamic-workflows-custom/docs/superpowers/plans/2026-06-07-per-agent-restart.md, /root/projects/pi-dynamic-workflows-custom/docs/superpowers/plans/2026-06-07-width-aware-footers.md, /root/projects/pi-dynamic-workflows-custom/POST_NPM_TEST.log, /root/projects/pi-dynamic-workflows-custom/BASELINE_NPM_TEST.status, /root/specs/pi-dynamic-workflows-model-routing/index.html, /root/specs/dynamic-workflow-reverse-engineering/index.html, /root/specs/model-review-orchestration/index.html, /root/.pi/agent/settings.json (keys only), /root/.pi/workflows/model-tiers.json (keys only), /root/.pi/workflows/consent.json (keys only)

**Fatos-chave:**
- Project is a local durable fork of npm `@quintinshaw/pi-dynamic-workflows` v2.0.1 (package.json still names that pkg/version; original idea from Michael Livs + Anthropic's Claude Code dynamic workflows). Repo at /root/projects/pi-dynamic-workflows-custom, git remote origin = https://github.com/Ianfr13/pi-dynamic-workflows-custom.git.
- Git is on branch `cc-tui-parity-sprint` with a CLEAN working tree (git status -s empty; git diff HEAD empty). The latest commit 953ecbb is 'docs: parity audit report, implementation plans, updated handoff'. HANDOFF.md's claim 'Branch: master, Nothing committed' is STALE — work has since been committed to cc-tui-parity-sprint and matches origin (no unpushed commits).
- Pi loads the fork via /root/.pi/agent/settings.json `packages` array, which contains the literal local path "/root/projects/pi-dynamic-workflows-custom" (alongside npm: packages like pi-web-access, pi-hud, pi-mcp-adapter). Already wired; nothing to deploy.
- package.json `pi.extensions` = ["extensions/workflow.ts"]. Pi runs the TypeScript SOURCE directly: extensions/workflow.ts imports from "../src/index.js" (NodeNext specifier that resolves to src/index.ts at runtime). After editing src/*.ts you run `/reload` in the Pi session (or restart) — no rebuild needed for the running extension. dist/ is built only for npm publish + type-checking.
- Build/test: `npm test` = `npm run check` (biome check .) && `npm run build` (tsc) && `npm run test:unit` (node scripts/run-unit-tests.mjs -> npx tsx --test on 13 listed .ts files) && `npm run test:mjs` (node --test tests/task-panel.test.mjs). VERIFIED GREEN now: biome clean (47 files), tsc exit 0, 76 TS unit tests + 7 mjs tests = 83 passing. (README badge says 622 and HANDOFF says 62+3=65; both are stale — actual is 83.)
- Rollback: restore /root/.pi/agent/settings.json.bak-pi-dynamic-workflows-2026-06-06 (or swap the path entry back to "npm:@quintinshaw/pi-dynamic-workflows"), and restore /root/.pi/workflows/model-tiers.json.bak-pi-dynamic-workflows-2026-06-06 if strict routing breaks workflows, then `/reload`. Documented in LOCAL_SETUP.md and HANDOFF.md.
- Model-routing design (spec at /root/specs/pi-dynamic-workflows-model-routing/index.html, Portuguese): a dedicated resolver src/model-routing-policy.ts with arbitrary user-defined tiers (not just small/medium/big), configurable rules matching on agentType/phase/label, and FAIL-FAST strict fallback (fallback.mode='error') — no silent fallback to the session model. This is fully implemented and live.
- Config file ~/.pi/workflows/model-tiers.json now has keys {tiers, rules, fallback}. Live tiers present: small, medium, big, cheap, coder, judge, research. consent.json at ~/.pi/workflows/consent.json = {"multiAgentWarningAccepted": ...} (one-time global multi-agent warning, Claude-Code style).
- Two written-but-NOT-yet-executed implementation plans exist under docs/superpowers/plans/ (per-agent-restart and width-aware-footers). NOTE: per-agent-restart is ALREADY implemented (restart.test.ts passes, restartAgent in workflow-manager.ts) and footer-fit.test.ts passes — so these plans were executed even though the markdown still has unchecked boxes. HANDOFF's 'Open/nice-to-have' list is partly stale.
- src/index.ts is the barrel exporting the entire public API (WorkflowManager, createWorkflowTool, runWorkflow, resolveModelRoute, createWorkflowStorage, installTaskPanel, installResultDelivery, registerBuiltinWorkflows, etc.) — this is the surface a pi-mcp integration or the extension consumes.

## Area I — Docs, Specs, Build/Test, History

### 1. Project origin & lineage
- **Path:** `/root/projects/pi-dynamic-workflows-custom` (a git repo, as the spec required).
- **Fork of:** npm `@quintinshaw/pi-dynamic-workflows` **v2.0.1**. `package.json` still carries `"name": "@quintinshaw/pi-dynamic-workflows"`, `"version": "2.0.1"`, `"author": "QuintinShaw"`, `contributors: ["michaelliv (original author)"]`, and `repository.url` still points at `github.com/QuintinShaw/pi-dynamic-workflows`.
- **Local remote:** `origin = https://github.com/Ianfr13/pi-dynamic-workflows-custom.git` (Ian's fork, not Quintin's).
- **Lineage chain (per README "Credits"):** Michael Livs' original `pi-dynamic-workflows` → Anthropic's "dynamic workflows in Claude Code" idea → QuintinShaw's productionized npm package → **this local custom fork** (adds model-routing policy + Claude-Code TUI/UX parity).
- **Concept:** "code mode for subagents" — Pi (the model) writes a JS orchestration script that fans a task out to many isolated subagents (`agent()`/`parallel()`/`pipeline()`/`phase()`) in a Node `vm` sandbox, keeps intermediate work in script variables (not chat context), and returns one synthesized answer. Up to **16 concurrent / 1000 total** subagents.

### 2. How it loads into Pi (the load/reload loop)
```
/root/.pi/agent/settings.json
  packages: [ "npm:pi-web-access", "npm:pi-hud",
              "/root/projects/pi-dynamic-workflows-custom",   <-- local path entry
              "npm:@juicesharp/rpiv-ask-user-question", ... "npm:pi-mcp-adapter", ... ]
```
- `package.json` declares the extension entry: `"pi": { "extensions": ["extensions/workflow.ts"] }`.
- **Pi runs TypeScript source directly.** `extensions/workflow.ts` does:
  ```ts
  import { ... WorkflowManager, createWorkflowTool, ... } from "../src/index.js";
  ```
  Under `tsconfig` `module/moduleResolution: NodeNext`, the `.js` specifier resolves to `src/index.ts` at runtime — so the **running extension uses `src/`, not `dist/`**.
- The default export `extension(pi: ExtensionAPI)` registers everything: the `workflow` tool (`pi.registerTool`), `/workflows` commands, `/workflows-models`, built-in workflows (`/deep-research`, `/adversarial-review`), saved workflows, and the `/effort`/`/ultracode` standing opt-in. On `session_start` it forces the tool active, sets the main model (so `explore` agents auto-tier-down), scopes run history to the session, and installs result delivery + the task panel + the workflow editor.
- **Reload loop:** edit `src/*.ts` → run **`/reload` in the Pi session** (or restart Pi). **No `npm run build` is required** for the running extension (it reads `src/`). `dist/` is kept built only for npm packaging and `tsc` type-checking.
- **Keyword trigger is OFF by default** (`workflowMode = { active:false, enabled:false }` in `extensions/workflow.ts`): typing "workflow"/"ultracode" no longer hijacks a message; the model itself decides to call the registered `workflow` tool. Re-enable the colorized affordance with `/ultracode keyword on`.

### 3. Build / test (`npm test`)
`package.json` scripts:
```jsonc
"test":      "npm run check && npm run build && npm run test:unit && npm run test:mjs",
"test:unit": "node scripts/run-unit-tests.mjs",      // -> npx tsx --test <13 files>
"check":     "biome check .",
"build":     "tsc",
"dev":       "tsx src/index.ts",
"test:mjs":  "node --test tests/task-panel.test.mjs"
```
- **tsconfig.json:** `target ES2022`, `module/moduleResolution NodeNext`, `strict`, `declaration` + `sourceMap`, `outDir dist`, `rootDir src`, `include ["src/**/*.ts"]`, `exclude ["dist","node_modules","tests"]`.
- **biome.json:** biome **2.4.16**, formatter disabled, linter recommended rules, scope = `src/**/*.ts`, `extensions/**/*.ts`, `tests/**/*.{ts,mjs}` + config files (excludes dist/node_modules).
- **`scripts/run-unit-tests.mjs`** runs `npx tsx --test` on this list (`.filter(existsSync)`): `model-routing-policy`, `agent-model-resolution`, `workflows-models-command`, `workflow-ui`, `workflow-tool-render`, `parity-cluster`, `preflight-save`, `effort-trigger`, `builtins`, `agent-detail`, `integration`, `restart`, `footer-fit`. Plus `tests/task-panel.test.mjs` via `--test:mjs`.
- **VERIFIED CURRENT COUNTS (I re-ran):** biome `Checked 47 files... No fixes applied`; `tsc` exit **0**; **76 TS unit tests + 7 mjs = 83 passing, 0 fail**. (Stale numbers in docs: README badge = 622 passing; HANDOFF says "62 unit + 3 mjs = 65"; `POST_NPM_TEST.log` shows only 25+1 — all predate the restart/footer test additions.)
- **dist/ staleness:** dist was older than src (src/task-panel.ts 10:35 vs dist 07:06) but `npm run build`/my `tsc` run rebuilt it (dist/index.js now 12:20). dist **is git-tracked** (87 files) alongside src (29 tracked files). Since the extension runs from src, dist staleness does NOT affect the live Pi behavior — only npm publish / type artifacts.

### 4. Git history & current state
`git log --oneline -15` (branch `cc-tui-parity-sprint`):
```
953ecbb docs: parity audit report, implementation plans, updated handoff
d9b38b5 feat: Claude Code TUI/UX parity — consent, restart, width-aware footers, panel dedup
85ea373 chore: ignore local .pi workflow run state
17601fb fix workflow model routing review issues
74ef353 chore: record post-change npm test result
4595e20 chore: rebuild workflow dist
886ed8b chore: satisfy biome import ordering
3cfbb5e docs: add local fork setup and rollback
c07ca7f feat: manage custom workflow model tiers
aac673b docs: describe dynamic workflow model tiers
c351256 feat: route workflow agents through model policy
8d0136a feat: enforce strict workflow model availability
cb6d929 feat: add dynamic model routing policy
a9687d6 test: define dynamic workflow model routing behavior
261c507 chore: create local pi dynamic workflows fork
```
- The history shows two clean phases: **(a) model-routing** (261c..c07c) implementing the routing spec, then **(b) CC TUI/UX parity sprint** (d9b3, 953e).
- **Working tree is CLEAN** (`git status -s` empty, `git diff HEAD` empty). Branches: `cc-tui-parity-sprint` (current), `master`, plus matching `origin/*`. No unpushed commits on `cc-tui-parity-sprint`.
- `.gitignore` = `node_modules/` and `.pi/` (local run state is intentionally not tracked).
- **HANDOFF.md is partly stale:** it says "Branch: master, Nothing committed (all in working tree)" — but the parity work IS committed on `cc-tui-parity-sprint`. Its "re-verify in a live Pi after /reload" warnings (keyword-off, narrow-terminal crash fix, idle-panel-hides, preflight→`ui.confirm`) reflect fixes that are now committed + test-green but, per HANDOFF, **had not yet been re-verified in a live Pi session**.

### 5. Model-routing design rationale (the core spec)
Spec: `/root/specs/pi-dynamic-workflows-model-routing/index.html` (PT-BR, dated 2026-06-06, status "Decisão aprovada"). Design intent:
- **Goal:** route workflow agents by configurable rules + user-defined tiers (arbitrary names like `judge`, `coder`, `research`, `cheap` — not only `small/medium/big`). Behavior must be **fail-fast**: a rule/tier pointing at a missing/unavailable model errors with a clear message instead of silently running the wrong model.
- **New module `src/model-routing-policy.ts`** centralizes rule/fallback types, matcher parsing/eval, tier/model resolution, strict errors, decision metadata.
- **Decision shape** the resolver returns:
  ```ts
  interface ModelRouteDecision {
    modelSpec: string;
    source: "explicit-model" | "agent-type-model" | "rule-model" | "rule-tier"
          | "explicit-tier" | "phase-model" | "default-tier";
    tier?: string;
    ruleName?: string;
  }
  ```
- **Config** `~/.pi/workflows/model-tiers.json` extended to `{ tiers, rules, fallback }` (backward-compatible with old `{tiers}`-only files). Rules match on `agentType` (exact), `phase` (exact or `/regex/flags`), `label` (exact or `/regex/flags`), and point at a `tier` or a direct `model`. Full-prompt-text routing is explicitly out of first scope.
- **Routing priority (9 levels):** 1 `opts.model` → 2 resolved `agentType.model` → 3 rule by `agentType` → 4 rule by `phase` → 5 rule by `label` → 6 `opts.tier` → 7 `meta.phases[].model` → 8 configured `medium` tier → 9 **error if nothing resolves**.
- **Strict mode (`fallback.mode: "error"`)** errors when: tier not in `tiers`; rule points at unknown tier; resolved model not in Pi registry; or no route resolves. **No silent fallback to the session main model in strict mode.**
- **Durable-fork mandate:** the spec itself dictated creating `/root/projects/pi-dynamic-workflows-custom` as a git repo and swapping the settings.json package entry — i.e. this whole fork exists *because of* this spec. Success criteria: unlimited user tiers, custom-tier resolution, rule routing, fail-fast, `/workflows` still shows the real resolved model, existing small/medium/big workflows keep working, and the installed source is a durable fork (not a node_modules patch).
- **Live config confirms implementation:** `~/.pi/workflows/model-tiers.json` has `{tiers, rules, fallback}` with tiers `small, medium, big, cheap, coder, judge, research`.

### 6. CC parity audit (CC_AUDIT_REPORT.md)
Self-assessed **parity score 82/100**. Runtime primitives at/above Claude Code parity; gaps were UX/permission polish. The audit drove a **P0+P1+P2 sprint (11 tasks, all done)**: preflight approval card + global consent (`~/.pi/workflows/consent.json` = `{multiAgentWarningAccepted}`), built-ins through `WorkflowManager`, `/workflows status <id> --script`, save modal, abort string, glyph consistency (◆ run / ● agent), status-aware detail ladder + `interrupted` status, per-agent tool-call capture, ultracode keyword + `/effort xhigh|ultracode` + `/ultracode keyword on|off`. Things the fork claims to do **BETTER** than CC: cross-session journaled resume, richer cross-provider model routing, quality stdlib (`verify`/`judgePanel`/`loopUntilDry`/`completenessCheck`/`retry`/`gate`), replayable human `checkpoint()`, per-agent worktree isolation, real token/cost accounting, nested saved workflows.

### 7. Other pi-related specs in /root/specs/
| Spec dir | What it is |
| --- | --- |
| `pi-dynamic-workflows-model-routing/` | **The core routing spec** (above). Source of this fork. |
| `dynamic-workflow-reverse-engineering/` | Deep reverse-engineering of Claude Code's dynamic-workflow orchestration (47 sources). Covers DAG vs durable-code execution (Temporal-style), deterministic replay/event-sourcing (no `Date.now`/`Math.random`/I/O — matches the fork's `vm` determinism), pipeline vs barrier parallelism, multi-agent frameworks (LangGraph/AutoGen/CrewAI), and QA-by-ensemble patterns. Key thesis directly relevant to pi-mcp: **heterogeneity beats homogeneity in verification** (cross-family review, asymmetric context, minority-veto > majority), and **saturation detection is the missing primitive**. |
| `model-review-orchestration/` | An operational "who-does-what / who-reviews-whom" map across **Claude Opus 4.8 (architect/final judge), GPT-5.5 (operational executor), DeepSeek V4 Flash (cheap triage), DeepSeek V4 Pro (technical engineer), MiniMax M3 (long-context), GLM 5.1 (long-horizon loops)**. Includes a YAML router/planner/execution/review block — a concrete model-routing policy pi-mcp could encode as tiers/rules. |
| `dynamic-workflow-reverse-engineering` + `etapa1-worker-fidelity-spike`, `etapa1-plano-implementacao`, `etapa2-*`, `etapa3-inferd`, `etapa4-*`, `etapa5-*` | A larger multi-stage program ("etapa" = stage) about worker fidelity, fleet safety, durable NATS queues, production scale, and measurement/calibration — adjacent infra research, not strictly the workflows fork. |

(Most other `/root/specs/*` dirs — funis, meteorico, dani-*, confeitaria, etc. — are unrelated business/product specs served by `serve.py`.)

### 8. docs/ tree
```
docs/superpowers/plans/2026-06-07-per-agent-restart.md
docs/superpowers/plans/2026-06-07-width-aware-footers.md
```
Both are detailed TDD implementation plans (file-by-file, with exact line numbers, failing-test-first steps, and commit messages). **Both appear already executed** — `tests/restart.test.ts` and `tests/footer-fit.test.ts` exist and pass, `restartAgent` is implemented in `workflow-manager.ts`, and HANDOFF's "Decisions" section already describes restart-from-agent as implemented. The plans' checkboxes are unchecked but the code/tests are green, so they're historical artifacts, not open work.

### 9. Roadmap / open tasks
- CC_AUDIT_REPORT roadmap: P0/P1/P2 done; **P3 stretch (beyond parity)** still open — lightweight workflow daemon/supervisor, visual DAG graph, live budget governor, marketplace/templates, cross-run analytics. "Surpass CC" ideas: workflow graph view, replay/fork UI, budget cockpit, evidence ledger, quality scoreboard, cross-provider judge panels, templates, richer checkpoints, worktree merge assistant, auto HTML reports.
- HANDOFF open items: real surgical per-agent restart (currently restart-from-agent, re-runs target + all lexically-later); commit/PR when user approves (now done on `cc-tui-parity-sprint`); **TOP PRIORITY: re-verify the last fix round in a live Pi after `/reload`** (test-green but not live-verified).

**Implicações para pi-mcp:**
- BUILD/LOAD/RELOAD LOOP for pi-mcp: the fork is already registered in /root/.pi/agent/settings.json packages as a local path, and Pi executes the TypeScript SOURCE (extensions/workflow.ts -> ../src/index.js resolving to src/*.ts). So pi-mcp does NOT need to build dist/ for the running extension — editing src/ + `/reload` (or restarting the headless pi process) is the full loop. Important for headless mode: `pi -p --mode json` starts a fresh process, so it picks up current src/ automatically; there is no persistent daemon to /reload.
- STABILITY: The fork is stable enough to depend on. Working tree is clean, committed on cc-tui-parity-sprint, biome clean, tsc exit 0, and 83/83 tests green (verified live, not just from stale logs). The public API surface (src/index.ts barrel) is broad and stable: WorkflowManager, createWorkflowTool, runWorkflow, createWorkflowStorage, resolveModelRoute, installTaskPanel/installResultDelivery, registerBuiltinWorkflows. pi-mcp can rely on these names.
- PERSISTED RUN FILES (for pi-mcp visibility tools): runs persist to <cwd>/.pi/workflows/runs/<runId>.json (with .bak siblings) plus run-<id>.log files; saved workflows under .pi/workflows/saved (project) and ~/.pi/workflows/saved (user). Note `.pi/` is gitignored. pi-mcp's read-only visibility tools should read these JSON run files (PersistedRunState / PersistedAgentState shapes, exported from run-persistence.ts) — each agent record carries callIndex, model, status, tokens, and tool calls.
- MODEL ROUTING is exactly the pi-mcp value prop: PI (not Claude) chooses MANY models. The fork already routes per-agent/per-phase via ~/.pi/workflows/model-tiers.json {tiers, rules, fallback} with a 9-level priority and fail-fast strict mode. pi-mcp can pre-seed this file with tiers/rules (the model-review-orchestration spec gives a ready policy: claude-opus-4.8=judge, gpt-5.5=executor, deepseek-flash=cheap, deepseek-pro=coder, minimax-m3=context, glm-5.1=long-loop) so a delegated TASK fans across heterogeneous models automatically.
- HEADLESS CAVEAT: the fork's UX layer (preflight ui.confirm approval, task panel, navigator) assumes an interactive TUI. In `pi -p --mode json` there is no UI, so the preflight approval and consent prompt paths matter — global consent lives at ~/.pi/workflows/consent.json ({multiAgentWarningAccepted}); pi-mcp should ensure that file marks consent accepted so background runs don't block waiting for a confirm that can't be shown. Also background-by-default behavior + result delivery (installResultDelivery) is designed for an interactive session that auto-continues; pi-mcp driving headless must instead poll the persisted run JSON for completion.
- DETERMINISM CONSTRAINT to respect: workflow scripts run in a Node vm with no Date.now/Math.random/require/import/fs/network (enables journaled resume). pi-mcp-generated or pi-generated orchestration scripts must stay within this sandbox; any nondeterminism must go through agent() calls.
- TESTING/DOCS DRIFT to be aware of: README (622), HANDOFF (65), and POST_NPM_TEST.log (26) all under-report the real test count (83). Don't trust the doc numbers; the suite itself is the source of truth and is fully green. HANDOFF also still flags 'not yet re-verified in a live Pi' for the last fix round — pi-mcp integration testing should do that live verification (narrow-terminal no longer crashes, idle panel hides, model decides tool call).

**Questões em aberto:**
- Live Pi verification of the last fix round (keyword-off, narrow-terminal width-crash fix, idle-panel-hide, preflight ui.confirm) is still pending per HANDOFF — confirmed test-green but not confirmed in an actual /reload'd Pi session.
- In fully headless `pi -p --mode json` mode, does the workflow tool's preflight ui.confirm get auto-skipped (because ctx.hasUI is false) or does it block? Needs confirmation in workflow-tool.ts (Area covering the tool) — affects whether pi-mcp must pre-seed consent.json.
- dist/ is git-tracked and was stale relative to src before I rebuilt it; if pi-mcp or CI ever consumes dist (npm import path) rather than src, a forgotten `npm run build` could ship stale behavior. The running extension is safe (uses src), but the packaging path is a latent footgun.
- The model-tiers.json on this machine references models like deepseek/deepseek-v4-*, openai-codex/gpt-5.5 (from the spec examples) — whether those exact provider/modelIds are actually authenticated/available in Pi's registry here (and thus whether strict mode would error) is unverified in this area; needs cross-check against pi's model registry / auth.
- The two docs/superpowers/plans/*.md are unchecked but appear implemented — minor risk that some plan step (e.g. a specific notify-copy or doc edit) was skipped; the HANDOFF text suggests they were applied, but a line-level diff against the plans was not done in this area.


---

# pi Workflow Run Persistence: Schema, Multi-Model Evidence, and Lifecycle States

**Arquivos examinados:** /root/.pi/workflows/runs/mq3v6uyo-pmtqtv.json (telemedicine_research, 6 agents, 2 models, completed — largest run), /root/.pi/workflows/runs/mq31e6fs-5z3jw8.json (agent_count_investigation, 8 agents, 2 models, completed), /root/.pi/workflows/runs/mq2u62bu-s1vast.json (adversarial_model_routing_plan_review_retry, 5 agents, completed), /root/.pi/workflows/runs/mq314rcc-nlfu7s.json (routing_live_smoke_test, 3 agents, 3 DISTINCT models — multi-model proof), /root/.pi/workflows/runs/mq3sfgzk-asvqo8.json (test_twenty_agents, 20 agents — parallel fan-out / journal completion-order proof), /root/.pi/workflows/runs/mq2tzf50-o9tzb5.json (failed run — terminal-field omission proof), /root/.pi/workflows/runs/mq2xgzm3-w537fz.json (agent status:error proof), /root/.pi/workflows/runs/mq2ovqz4-wh8gv8.json (paused run, running agent — in-progress visibility), /root/.pi/workflows/runs/mq2s4e2i-wzezmw.json (paused, 0 agents — empty run shape), /root/.pi/workflows/runs/run-mq2xgzm3.log, run-mq2znvm3.log, run-mq302nkz.log, run-mq314rcd.log (sidecar logs — error detail location), /root/.pi/workflows/model-tiers.json (tier->model map + routing rules), /root/.pi/workflows/consent.json (multiAgentWarningAccepted gate)

**Fatos-chave:**
- Runs are persisted one-JSON-file-per-run at /root/.pi/workflows/runs/<runId>.json, each with a byte-identical write-ahead backup <runId>.json.bak (cmp confirmed IDENTICAL). 90 files total (~45 runs + 45 .bak).
- Top-level run schema (completed): runId, workflowName, sessionId, status, currentPhase, phases[], startedAt, completedAt, updatedAt, durationMs, agents[], journal[], logs[], result, tokenUsage, args, script. There is NO top-level 'meta' object (jq .meta == null) and NO top-level 'id' — meta lives INSIDE the script source string as `export const meta = {...}`.
- Failed/paused/aborted runs OMIT the terminal fields entirely: result, tokenUsage, durationMs, completedAt are ABSENT (not null) when status != completed. No run ever populated a top-level .error field (scanned all runs).
- Agent object schema: {id (1-based int), label, phase, prompt (full prompt text), status, model (the model ACTUALLY used), resultPreview (~80-char truncated JSON-stringified result), tokens (a NUMBER = total tokens for that agent), startedAt, endedAt}. NOTE: there is NO 'agentType', NO 'callIndex', NO per-agent 'error', and NO per-agent 'cost'/'tokenUsage' object in persisted agents (those .agents[].callIndex/.agentType seen earlier were jq returning null for absent keys).
- PROOF of simultaneous multi-model execution: run mq314rcc-nlfu7s (routing_live_smoke_test) ran 3 agents on 3 DIFFERENT models in one run — Scan→deepseek/deepseek-v4-flash, Review(label 'final critic smoke')→openai-codex/gpt-5.5, Verify→deepseek/deepseek-v4-pro — exactly matching /root/.pi/workflows/model-tiers.json rules (scan phase→cheap, label /critic|judge/→judge, tier:medium→pro).
- PROOF of parallel fan-out: 20-agent run mq3sfgzk-asvqo8 has agents ordered by id [1..20] but journal indices appended in COMPLETION order [0,6,5,4,1,7,2,3,9,8,14,11,...] — out of order, proving agents in a phase run concurrently and the journal records completion order. 19 agents on deepseek-v4-flash + 1 synthesis on deepseek-v4-pro.
- Journal entry schema: {index (0-based call ordinal, maps to agents[].id minus 1), hash (sha256 content hash), result (FULL structured object OR free-form string)}. agents[id=1].resultPreview is the leading slice of journal[index=0].result stringified.
- Agent status values observed: done, running, error, interrupted. Run status values observed: completed (22), failed (7), paused (3), aborted (2). A 'running' agent can have tokens=null yet an endedAt set if the run was paused mid-flight.
- Per-agent error DETAIL is NOT in the JSON — an errored agent has status:'error', resultPreview:'null', tokens kept small. The actual error string lives in the sidecar log file run-<prefix>.log, e.g. '[ERROR] agent "patch plan" failed: Agent "patch plan" timed out after 300000ms' (300s == the agent timeout).
- logs[] is an array of human strings, normally just ['Logs persisted to /root/.pi/workflows/runs/run-<prefix>.log']. The log filename uses a DIFFERENT trailing char than runId (runId mq2xgzm3-w537fz → run-mq2xgzm3.log; runId mq3v6uyo-pmtqtv → run-mq3v6uyv.log) — derived from the timestamp prefix, not the full runId.
- tokenUsage (run-level, only on completed) = {input, output, total, cost (USD float), cacheRead, cacheWrite}. 'total' is much larger than input+output because it includes cacheRead (e.g. mq31e6fs: input 226486 + output 20567 but total 2416141 with cacheRead 2169088).
- result (final synthesized) shape is workflow-defined = whatever the script's final `return` produces. Examples: telemedicine_research returned {ok, report (21KB markdown string), topDataPoints, caveats}; smoke test returned {ok, scan, review, verdict}. There is no fixed result schema.

## AREA J — Real Run Journals (ground-truth evidence)

All evidence below is from real persisted files under `/root/.pi/workflows/runs/`. There are 90 files (≈45 runs, each shadowed by a byte-identical `*.json.bak` write-ahead backup; `cmp` confirms IDENTICAL).

Sibling config files in `/root/.pi/workflows/`:
- `model-tiers.json` — tier→model map + routing rules (drives which model each agent gets).
- `consent.json` — `{"multiAgentWarningAccepted": true}` (the one-time multi-agent consent gate).

### 1. Top-level persisted run schema

Keys on a **completed** run (`mq314rcc-nlfu7s.json`):

```jsonc
{
  "runId":        "mq3v6uyo-pmtqtv",          // <timestamp36>-<rand6>; also the filename
  "workflowName": "telemedicine_research",    // from script meta.name
  "sessionId":    "019ea26d-6d97-7eb2-...",   // UUIDv7 of the pi session that launched the run (present on ~32/34 runs)
  "status":       "completed",                // completed | failed | paused | aborted
  "currentPhase": "Síntese final",            // last phase reached
  "phases":       ["Mapeamento","Síntese final"],  // declared phase titles, in order
  "startedAt":    "2026-06-07T14:15:37.541Z", // ISO 8601
  "completedAt":  "2026-06-07T14:20:14.783Z", // present ONLY when completed
  "updatedAt":    "2026-06-07T14:20:14.783Z", // last persistence write
  "durationMs":   277239,                     // present ONLY when completed
  "agents":       [ ... ],                    // see §3
  "journal":      [ ... ],                    // see §4
  "logs":         ["Logs persisted to /root/.pi/workflows/runs/run-mq3v6uyv.log"],
  "result":       { ... },                    // final synthesized return value (workflow-defined); null/absent unless completed
  "tokenUsage":   { ... },                    // see §5; absent unless completed
  "args":         { ... } | null,             // initial input passed to the workflow (e.g. a sourceDigest object)
  "script":       "export const meta = {...} ... return {...}"  // the FULL workflow source code as a string
}
```

**There is NO top-level `meta` object** — `jq .meta` returns null. The `name`/`description`/`phases` that a caller might expect under `meta` are embedded inside the `script` source string:

```js
// first line of .script
export const meta = { name: 'routing_live_smoke_test', description: 'Live smoke test ...', phases: [{ title: 'Scan' }, { title: 'Review' }, { title: 'Verify' }] }
```

The hoisted `.phases[]` top-level array is the runtime's flattened copy of `meta.phases[].title`.

### 2. Lifecycle states & how non-completed runs degrade

Across all runs:

| Run status | Count | result | tokenUsage | durationMs | completedAt |
|---|---|---|---|---|---|
| `completed` | 22 | object | present | present | present |
| `failed`    | 7  | **absent** | **absent** | **absent** | **absent** |
| `paused`    | 3  | null/absent | absent | absent | absent |
| `aborted`   | 2  | absent | absent | absent | absent |

Critically, a non-terminal run does not write `null` for the terminal fields — it **omits the keys entirely**. Compare top-level `keys`:
- completed `mq314rcc`: includes `completedAt, durationMs, result, tokenUsage`.
- failed `mq2tzf50`: those four keys are simply **not present**.

No run ever set a top-level `.error` field (scanned all). Run-level failure context is only in the sidecar `.log`.

Agent-level statuses observed: `done`, `running`, `error`, `interrupted`. A `paused` run (`mq2ovqz4`) shows an agent with `status:"running"`, `tokens:null`, yet an `endedAt` already set (model finished but the workflow was paused), and its journal index 0 already holds the agent's full result. This shows: **journal entries are appended as each agent's result lands, independent of the agent's workflow-lifecycle `status`.**

### 3. Agent object schema (`agents[]`)

Exact keys (union across runs) = `id, label, phase, prompt, status, model, resultPreview, tokens, startedAt, endedAt`.

```jsonc
{
  "id":            1,                          // 1-based submission ordinal
  "label":         "scan inventory smoke",     // agent label from agent(prompt,{label})
  "phase":         "Scan",                      // the phase() active when spawned
  "prompt":        "Smoke test only. Return marker SCAN_OK with ok=true.", // FULL prompt text
  "status":        "done",                      // done | running | error | interrupted
  "model":         "deepseek/deepseek-v4-flash",// the model ACTUALLY used (post-routing)
  "resultPreview": "{\"ok\":true,\"marker\":\"SCAN_OK\"}", // ~80-char JSON-stringified prefix of result
  "tokens":        29716,                       // a NUMBER (total tokens for this agent), NOT an object
  "startedAt":     "2026-06-07T00:14:11.052Z",
  "endedAt":       "2026-06-07T00:14:32.317Z"
}
```

Pitfalls to record (these are what an integrator gets WRONG):
- There is **no** `agentType`, `callIndex`, per-agent `error`, per-agent `cost`, or per-agent `tokenUsage` object. (Earlier probes that showed `agentType:null`/`callIndex:null` were jq emitting null for ABSENT keys.)
- `tokens` is a single integer, not `{input,output}`.
- `resultPreview` truncates at ~80 chars with an ellipsis; the FULL result is only in `journal[].result`.
- In completed parallel runs all agents often share the SAME `startedAt`/`endedAt` equal to run-level timestamps (e.g. all 6 telemedicine agents have identical timestamps) — i.e. per-agent timing is sometimes coarse/run-level, not reliable for per-agent latency. The smoke-test and paused runs DO have distinct per-agent timestamps. Treat per-agent timing as best-effort.

### 4. Journal schema (`journal[]`)

```jsonc
{
  "index":  0,                                  // 0-based call ordinal == agents[].id - 1
  "hash":   "5b15e4...18be",                     // sha256 content hash of the result
  "result": { "ok": true, "marker": "SCAN_OK" }  // FULL result: object if schema given, else a free-form string
}
```

- `journal[index=0].result` == the FULL value whose 80-char prefix is `agents[id=1].resultPreview` (verified by cross-reference).
- `result` is a **structured object** when the `agent()` call passed a `schema`, otherwise a **free-form string** (the paused explorer run shows a multi-paragraph string result).
- Rich structured results in these runs (research workflows) look like: `{angle, keyFindings[], dataPoints[{metric,value,geography,period,source,caveat}], opportunities[], risks[], sources[]}`.

**Parallelism proof:** in the 20-agent run `mq3sfgzk-asvqo8`, `agents[]` is ordered by id `[1..20]` but `journalIndices` are `[0,6,5,4,1,7,2,3,9,8,14,11,10,12,15,13,16,17,18,19]` — out of submission order. The journal records **completion order**, proving agents in a phase execute concurrently.

### 5. tokenUsage (run-level, only on completed)

```jsonc
"tokenUsage": {
  "input":      270788,
  "output":      27881,
  "total":      528941,   // includes cacheRead; NOT input+output
  "cost":         0.820729, // USD, float
  "cacheRead":  230272,
  "cacheWrite":      0
}
```

Real examples:

| runId | workflow | total | input | output | cacheRead | cost (USD) |
|---|---|---|---|---|---|---|
| mq31e6fs | agent_count_investigation | 2,416,141 | 226,486 | 20,567 | 2,169,088 | 1.9683 |
| mq2u62bu | adversarial_model_routing_plan_review_retry | 2,484,496 | 182,313 | 44,519 | 2,257,664 | 0.4112 |
| mq3v6uyo | telemedicine_research | 528,941 | 270,788 | 27,881 | 230,272 | 0.8207 |
| mq314rcc | routing_live_smoke_test | 109,535 | 56,011 | 532 | 52,992 | 0.1528 |

Note `total` ≈ input + output + cacheRead. `cost` does not track linearly with `total` (cache reads are cheap; gpt-5.5 output is pricey) — so cost must be read from the field, never recomputed.

### 6. Final synthesized `result` shape

There is **no fixed schema** — `result` is exactly whatever the workflow script's terminal `return` produced.

- `routing_live_smoke_test` → `{ ok, scan:{ok,marker}, review:{ok,marker}, verdict:{ok,verdict,scanMarker,reviewMarker} }`
- `telemedicine_research` → `{ ok, report (21,796-char markdown string), topDataPoints, caveats }`

### 7. Sidecar log files

`logs[]` is an array of strings, typically a single `"Logs persisted to /root/.pi/workflows/runs/run-<prefix>.log"`. The actual `run-<prefix>.log` is a separate plain-text file in the same directory. Its name uses the timestamp prefix of the runId with a different final char, NOT the full runId:
- runId `mq2xgzm3-w537fz` → `run-mq2xgzm3.log`
- runId `mq3v6uyo-pmtqtv` → `run-mq3v6uyv.log`
- runId `mq314rcc-nlfu7s` → `run-mq314rcd.log`

Log lines: `[ISO ts] [LEVEL] message`. This is the ONLY place per-agent error detail is captured:

```
[2026-06-06T22:36:43.196Z] [ERROR] agent patch plan failed: Agent "patch plan" timed out after 300000ms
[2026-06-06T22:37:21.139Z] [INFO] Logs persisted to /root/.pi/workflows/runs/run-mq2xgzm3.log
```

(300000ms = the per-agent timeout that produced most `failed`/`error` runs in this corpus.)

### 8. Model routing config (`model-tiers.json`)

```jsonc
{
  "tiers": {
    "small":   "deepseek/deepseek-v4-flash",
    "medium":  "deepseek/deepseek-v4-pro",
    "big":     "openai-codex/gpt-5.5",
    "cheap":   "deepseek/deepseek-v4-flash",
    "coder":   "openai-codex/gpt-5.5",
    "judge":   "openai-codex/gpt-5.5",
    "research":"deepseek/deepseek-v4-pro"
  },
  "rules": [
    { "name": "coder agentType",   "match": { "agentType": "coder" },     "tier": "coder" },
    { "name": "reviewer agentType","match": { "agentType": "reviewer" },  "tier": "judge" },
    { "name": "scan phase",        "match": { "phase": "Scan" },          "tier": "cheap" },
    { "name": "judgment labels",   "match": { "label": "/judge|critic|review|synthesis|final/i" }, "tier": "judge" }
  ],
  "fallback": { "mode": "error" }
}
```

The 3-model run `mq314rcc` is a direct demonstration: `Scan` agent matched the scan-phase rule → flash; `final critic smoke` matched the judgment-label regex → gpt-5.5; `Verify` agent (no rule) used its explicit `tier:'medium'` → deepseek-v4-pro.

### Per-run multi-model table (one real run = `mq3v6uyo-pmtqtv`, telemedicine_research)

| agent id | label | phase | model actually used | tokens | status |
|---|---|---|---|---|---|
| 1 | global market    | Mapeamento    | deepseek/deepseek-v4-pro | 38,662  | done |
| 2 | brazil market    | Mapeamento    | deepseek/deepseek-v4-pro | 37,465  | done |
| 3 | specialty map    | Mapeamento    | deepseek/deepseek-v4-pro | 146,691 | done |
| 4 | regulatory scan  | Mapeamento    | deepseek/deepseek-v4-pro | 176,476 | done |
| 5 | business models  | Mapeamento    | deepseek/deepseek-v4-pro | 36,956  | done |
| 6 | final synthesis  | Síntese final | openai-codex/gpt-5.5     | 92,691  | done |

And the **maximally diverse** run (`mq314rcc-nlfu7s`, routing_live_smoke_test) — 3 distinct models in one run:

| agent id | label | phase | model actually used | tokens |
|---|---|---|---|---|
| 1 | scan inventory smoke | Scan   | deepseek/deepseek-v4-flash | 29,716 |
| 2 | final critic smoke   | Review | openai-codex/gpt-5.5       | 49,923 |
| 3 | smoke verifier       | Verify | deepseek/deepseek-v4-pro   | 29,896 |

This is the ground-truth proof that pi (not the caller) routes different agents to different models within a single run.

**Implicações para pi-mcp:**
- pi_list_runs: glob /root/.pi/workflows/runs/*.json EXCLUDING *.json.bak (the .bak is a byte-identical shadow; including it doubles every run). For each, surface: runId, workflowName, status, currentPhase, phases.length, agents.length, startedAt, updatedAt, completedAt (may be absent), durationMs (absent unless completed), and tokenUsage.cost+total (absent unless completed). Sort by updatedAt/startedAt desc. File mtime is a reliable secondary sort.
- pi_list_runs must DEFENSIVELY treat result/tokenUsage/durationMs/completedAt as OPTIONAL — they are entirely absent (not null) on failed/paused/aborted runs. Use jq-style `// null` / optional-field decoding in Go (pointer fields or omitempty) to avoid unmarshal surprises.
- pi_run_status (single run): return runId, workflowName, status, currentPhase + phases[] (compute phase progress = index(currentPhase)+1 of phases.length), startedAt, completedAt, durationMs, sessionId, tokenUsage{input,output,total,cost,cacheRead,cacheWrite}. Read cost from the field; NEVER recompute (cost is non-linear vs total because total includes cacheRead).
- Per-agent surface for pi_run_status: map agents[] -> {id, label, phase, model, status, tokens, startedAt, endedAt}. Emphasize 'model' as the proof of multi-model fan-out (build a model-histogram: group agents by model with counts — e.g. {deepseek-v4-flash:19, deepseek-v4-pro:1}). Do NOT expect agentType/callIndex/per-agent error — they do not exist in persisted agents.
- To show the full result of an agent (not the 80-char resultPreview), JOIN agents[id=N] to journal[index=N-1].result. resultPreview is only a teaser. Expose a pi_run_agent_result(runId, agentId) that returns journal entry by index = agentId-1, including the content hash.
- Distinguish 'final synthesized result' (top-level .result, workflow-defined arbitrary shape) from per-agent journal results. pi_run_result(runId) should return .result raw (often {ok, report, ...}); it may be a large markdown string (21KB seen) so support truncation/streaming. Return a clear 'not ready' if .result is absent (run not completed).
- Error/diagnostics: when status==failed or an agent has status in {error,interrupted}, the JSON has NO error text. pi_run_status MUST read the sidecar log. Resolve the log path from .logs[0] (it embeds the absolute path) rather than reconstructing it — the filename uses a mangled prefix (run-<prefix>.log where <prefix> != the runId suffix; e.g. runId mq3v6uyo-pmtqtv -> run-mq3v6uyv.log). Provide a pi_run_logs(runId) tool that tails this file; lines are `[ISO ts] [LEVEL] msg` (LEVEL in INFO/ERROR).
- For background/headless visibility while a run is in flight: poll the run JSON — currentPhase, agents[].status (running/done), and journal.length advance as agents complete. Agents complete OUT OF ORDER (journal indices are completion-ordered), so progress = count(agents.status==done) and journal.length, NOT agent id ordering. updatedAt advances on every persistence write — use it to detect liveness.
- Treat per-agent startedAt/endedAt as best-effort: in some completed parallel runs all agents share the run-level timestamps (coarse), so don't compute per-agent latency from them; use the sidecar log timeline for real per-agent timing if needed.
- Resilience: if reading a half-written run JSON fails to parse, fall back to the <runId>.json.bak (byte-identical write-ahead copy) — useful for reads racing a concurrent write. pi_list_runs should prefer .json and only consult .bak on parse error.
- Model routing config is at /root/.pi/workflows/model-tiers.json (tiers map + rules + fallback.mode). A pi_get_routing_config tool can expose this so Claude can see WHICH models pi may pick and which tier rules fire (scan->cheap, label /judge|critic|review|synthesis|final/i ->judge, agentType coder/reviewer). The consent gate /root/.pi/workflows/consent.json {multiAgentWarningAccepted:true} must be true or multi-agent runs may prompt/abort — pi-mcp should verify/seed it before launching headless runs.

**Questões em aberto:**
- The log filename suffix derivation (runId mq3v6uyo-pmtqtv -> run-mq3v6uyv.log) appears to be the timestamp portion of the id re-encoded with a +1 increment on the last base36 char (pmtqtv vs the 'v' suffix). Worth confirming the exact algorithm in the workflow engine source so pi-mcp can predict log paths without relying on .logs[0]; until then, ALWAYS read the path from .logs[0].
- Whether a 'running' run (vs paused) is ever persisted with a distinct status string while genuinely live — the corpus only contains terminal/paused snapshots; confirm by launching a headless run and reading mid-flight (does status ever == 'running'/'active'?).
- Whether tokenUsage/durationMs are written incrementally during a run or only at completion — corpus shows them only on completed runs; need a live probe to know if partial token accounting is available mid-run for progress UIs.
- The exact 'hash' algorithm for journal[].hash (assumed sha256 of stringified result) — confirm against engine source so pi-mcp can verify integrity / dedupe.
- How args is captured for runs launched headless via `pi -p --mode json` vs from the TUI (some runs have args:null, one has a rich sourceDigest) — need to confirm where the MCP's TASK payload lands (args vs prompt).
- Whether there is any index/manifest file enumerating runs, or if directory glob is the only enumeration mechanism (only consent.json + model-tiers.json found alongside runs/ — appears glob-only).


---

# How pi wires MCP servers + resolves `!`-command / `${ENV}` credentials, and how pi-mcp will be registered in Claude Code

**Arquivos examinados:** /root/.pi/agent/mcp.json, /root/.pi/agent/bin/railway-mcp-vault, /root/.pi/agent/models.json, /root/.pi/agent/settings.json, /root/.pi/settings.json, /root/.pi/agent/auth.json (keys only, no values), /usr/lib/node_modules/@earendil-works/pi-coding-agent/dist/core/resolve-config-value.js, /usr/lib/node_modules/@earendil-works/pi-coding-agent/dist/core/model-registry.js, /root/.pi/agent/npm/node_modules/pi-mcp-adapter/package.json, /root/.pi/agent/npm/node_modules/pi-mcp-adapter/config.ts, /root/.pi/agent/npm/node_modules/pi-mcp-adapter/types.ts, /root/.pi/agent/npm/node_modules/pi-mcp-adapter/utils.ts, /root/.pi/agent/npm/node_modules/pi-mcp-adapter/server-manager.ts, /root/.pi/agent/npm/node_modules/pi-mcp-adapter/index.ts, /root/.claude.json (structure only), pi --help output, claude mcp add --help output, agent-vault --help / vault credential get --help / run --help output

**Fatos-chave:**
- pi's MCP support is NOT built into the core binary — it ships as an installed extension `pi-mcp-adapter` v2.9.0 (author Nico Bailon), located at /root/.pi/agent/npm/node_modules/pi-mcp-adapter. It is listed in /root/.pi/agent/settings.json `packages` as `npm:pi-mcp-adapter`.
- The `--mcp-config <path>` flag is registered by that extension (index.ts:84 `pi.registerFlag("mcp-config", {description, type:"string"})`), read via `pi.getFlag("mcp-config")` and as a raw argv scan `process.argv.indexOf("--mcp-config")` (utils.ts:54). It overrides the pi-global config path only; project/shared configs still merge.
- MCP config is loaded from a layered set of files (config.ts `getConfigSources`): user-global `~/.config/mcp/mcp.json` (shared), pi-global `~/.pi/agent/mcp.json` (via getAgentPath), project `./.mcp.json` (shared), and project `./.pi/mcp.json` (pi). Later sources override earlier by server name (`mergeConfigs` = `{...base.mcpServers, ...next.mcpServers}`). `imports` can pull servers from cursor/claude-code/claude-desktop/codex/windsurf/vscode configs.
- /root/.pi/agent/mcp.json defines two servers: `context7` (type:http, url https://mcp.context7.com/mcp, directTools:true) and `railway` (stdio: command /root/.pi/agent/bin/railway-mcp-vault, lifecycle:lazy, directTools:true, env block with ${AGENT_VAULT_ADDR}, ${AGENT_VAULT_TOKEN}, RAILWAY_MCP_VAULT=sales-brain, RAILWAY_MCP_TOKEN_KEY=RAILWAY_API_TOKEN).
- /root/.pi/agent/bin/railway-mcp-vault is a bash script (1198 bytes, mode 0700). It requires AGENT_VAULT_TOKEN to be set, calls `agent-vault vault credential get --vault "$VAULT_NAME" "$TOKEN_KEY"`, exports the result as RAILWAY_API_TOKEN, explicitly unsets RAILWAY_TOKEN, then `exec`s `$RAILWAY_BIN mcp` (default /root/.railway/bin/railway). The vaulted secret is fetched at spawn time and never written to mcp.json.
- Two DISTINCT credential-substitution mechanisms exist. (1) MCP server `env`/`headers`/`cwd`/`bearerToken` values use the adapter's `interpolateEnvVars` (utils.ts:62): ONLY `${VAR}` and `$env:VAR` are expanded from process.env — there is NO `!`-command execution here. (2) models.json `apiKey`/`headers`/`authHeader` use the pi-core `resolveConfigValue` (dist/core/resolve-config-value.js) which DOES support the `!`-command pattern.
- The `!`-prefix command substitution (dist/core/resolve-config-value.js): if a config value starts with `!`, the remainder (after stripping the `!`) is executed as a shell command via `execSync(command, {encoding:'utf-8', timeout:10000, stdio:['ignore','pipe','ignore']})` on non-Windows; stdout is `.trim()`ed and used as the value. Results are cached for the process lifetime in `commandResultCache` (Map keyed by the full command string).
- models.json apiKey example: `"!AGENT_VAULT_VAULT=sales-brain agent-vault vault credential get DEEPSEEK_API_KEY"`. The leading `!` triggers shell exec; `AGENT_VAULT_VAULT=sales-brain` is a normal shell inline env-var assignment scoped to that single command invocation, so agent-vault runs in agent-mode against the `sales-brain` vault. The resolved key becomes `Authorization: Bearer <key>` (model-registry.js:586) or the provider apiKey.
- Non-command config values support env templating: `${ENV}` / `$ENV` interpolation, `$$` escapes a literal `$`, `$!` escapes a literal `!`. If any referenced env var is missing, resolution returns undefined and `resolveConfigValueOrThrow` throws a descriptive error.
- agent-vault is a statically-linked Go ELF binary at /usr/local/bin/agent-vault. `agent-vault vault credential get <key>` in agent mode REQUIRES AGENT_VAULT_TOKEN set plus AGENT_VAULT_VAULT (or --vault); there is no interactive fallback. The process is normally launched via `agent-vault run -- <cmd>` which injects AGENT_VAULT_TOKEN/ADDR/VAULT and proxy/CA env (HTTPS_PROXY, HTTP_PROXY, SSL_CERT_FILE, NODE_EXTRA_CA_CERTS, REQUESTS_CA_BUNDLE, CURL_CA_BUNDLE, GIT_SSL_CAINFO, DENO_CERT, NODE_USE_ENV_PROXY=1) into the child.
- CONFIRMED in this environment: AGENT_VAULT_ADDR, AGENT_VAULT_TOKEN, AGENT_VAULT_VAULT are all currently SET in the process env (HTTPS_PROXY/SSL_CERT_FILE were unset in this particular subagent shell). This proves pi runs under an agent-vault session and inherits those vars — they are what make both the railway-mcp-vault script and the models.json `!`-command resolve credentials.
- Stdio MCP children inherit the FULL parent environment: server-manager.ts `resolveEnv` (line 373) copies all of process.env into the child, then overlays the interpolated per-server `env` block (`{...processEnv, ...interpolatedOverrides}`). So an MCP server spawned by pi automatically sees AGENT_VAULT_TOKEN/ADDR/VAULT and proxy/CA vars without them being listed in mcp.json.
- Claude Code registers MCP servers via `claude mcp add <name> <commandOrUrl> [args...]` with flags: `-s/--scope <local|user|project>` (default local), `-t/--transport <stdio|sse|http>` (default stdio), `-e/--env KEY=value` (repeatable), `-H/--header`, plus `claude mcp add-json <name> <json>`. Stdio example: `claude mcp add my-server -e API_KEY=xxx -- npx my-mcp-server`. Config is persisted in ~/.claude.json under top-level `mcpServers` (user/local scope) or in projects[<dir>].mcpServers; project scope writes ./.mcp.json.

## AREA K — MCP Integration & Credential Injection

### 1. Where MCP support lives in `pi`

`pi` core (`/usr/bin/pi` → `/usr/lib/node_modules/@earendil-works/pi-coding-agent/dist/cli.js`) does **not** implement MCP. MCP is provided by an installed extension:

- Package: **`pi-mcp-adapter` v2.9.0** (author *Nico Bailon*, repo `github.com/nicobailon/pi-mcp-adapter`)
- On disk: `/root/.pi/agent/npm/node_modules/pi-mcp-adapter`
- Enabled via `/root/.pi/agent/settings.json` → `"packages": [ ... "npm:pi-mcp-adapter" ... ]`
- Ships TypeScript source directly (`index.ts`, `config.ts`, `server-manager.ts`, `utils.ts`, `types.ts`, …) loaded through pi's extension runner.

The `--mcp-config` flag visible in `pi --help` ("Extension CLI Flags: --mcp-config <value>") is registered by this extension, not by core.

```ts
// index.ts:84
pi.registerFlag("mcp-config", {
  description: "Path to MCP config file",
  type: "string",
});
```

It is read both via `pi.getFlag("mcp-config")` (commands.ts:350/399, init.ts:32) and via a raw argv scan:

```ts
// utils.ts:54
export function getConfigPathFromArgv(): string | undefined {
  const idx = process.argv.indexOf("--mcp-config");
  if (idx >= 0 && idx + 1 < process.argv.length) return process.argv[idx + 1];
  return undefined;
}
```

### 2. MCP config file discovery & precedence

`config.ts` `getConfigSources(overridePath, cwd)` builds a layered list; `loadMcpConfig` merges them in order (later wins by server name):

| id | label | read path |
|----|-------|-----------|
| `shared-global` | user-global standard MCP | `~/.config/mcp/mcp.json` |
| `pi-global` | Pi global override | `~/.pi/agent/mcp.json` (or `--mcp-config` path via `getPiGlobalConfigPath(overridePath)` → `resolve(overridePath)`) |
| `shared-project` | project standard MCP | `./.mcp.json` |
| `pi-project` | project Pi override | `./.pi/mcp.json` |

`--mcp-config <path>` overrides **only** the `pi-global` read+write path (`getPiGlobalConfigPath`); the other layers still merge. `imports` can pull servers from other tools' configs (cursor / claude-code / claude-desktop / codex / windsurf / vscode — `IMPORT_PATHS` in config.ts). Merge rule:

```ts
mcpServers: { ...base.mcpServers, ...next.mcpServers }   // config.ts:253 (last source wins)
```

### 3. The actual `/root/.pi/agent/mcp.json`

```json
{
  "mcpServers": {
    "context7": { "type": "http", "url": "https://mcp.context7.com/mcp", "directTools": true },
    "railway": {
      "command": "/root/.pi/agent/bin/railway-mcp-vault",
      "args": [],
      "lifecycle": "lazy",
      "env": {
        "AGENT_VAULT_ADDR":  "${AGENT_VAULT_ADDR}",
        "AGENT_VAULT_TOKEN": "${AGENT_VAULT_TOKEN}",
        "RAILWAY_MCP_VAULT": "sales-brain",
        "RAILWAY_MCP_TOKEN_KEY": "RAILWAY_API_TOKEN"
      },
      "directTools": true
    }
  }
}
```

`ServerEntry` shape (types.ts:284) — relevant fields: `command`, `args`, `env`, `cwd`, `url`, `headers`, `auth: "oauth"|"bearer"|false`, `bearerToken`, `bearerTokenEnv`, `oauth`, `lifecycle: "keep-alive"|"lazy"|"eager"`, `idleTimeout`, `exposeResources`, `directTools: boolean|string[]`, `excludeTools`, `debug`.

### 4. The `railway-mcp-vault` credential-injection wrapper

`/root/.pi/agent/bin/railway-mcp-vault` — Bourne-Again **shell script**, mode `0700`, 1198 bytes. Logic:

```bash
#!/usr/bin/env bash
set -euo pipefail
VAULT_NAME="${RAILWAY_MCP_VAULT:-sales-brain}"
TOKEN_KEY="${RAILWAY_MCP_TOKEN_KEY:-RAILWAY_API_TOKEN}"
command -v agent-vault    # else exit 127
RAILWAY_BIN="${RAILWAY_BIN:-/root/.railway/bin/railway}"   # falls back to `command -v railway`
[ -n "${AGENT_VAULT_TOKEN:-}" ]   # else: "start Pi with agent-vault vault run"; exit 1
RAILWAY_TOKEN_VALUE="$(agent-vault vault credential get --vault "$VAULT_NAME" "$TOKEN_KEY")"
export RAILWAY_API_TOKEN="$RAILWAY_TOKEN_VALUE"
unset RAILWAY_TOKEN_VALUE; unset RAILWAY_TOKEN      # avoid project-scoped token confusion
exec "$RAILWAY_BIN" mcp
```

Pattern: pi spawns the wrapper as a stdio MCP server; the wrapper pulls the real Railway token from the vault **at spawn time**, exports it, and `exec`s the real `railway mcp`. The plaintext secret is never persisted in any JSON.

### 5. Credential resolution — two SEPARATE mechanisms

There are **two different** value-resolution code paths. Knowing which applies where is critical.

#### (a) MCP server `env` / `headers` / `cwd` / `bearerToken` → adapter `interpolateEnvVars` (utils.ts)
ONLY `${VAR}` and `$env:VAR` are expanded; **no `!`-command execution**:

```ts
// utils.ts:62
export function interpolateEnvVars(value: string): string {
  return value
    .replace(/\$\{(\w+)\}/g, (_, name) => process.env[name] ?? "")
    .replace(/\$env:(\w+)/g, (_, name) => process.env[name] ?? "");
}
```

Stdio child env (server-manager.ts:373) = full `process.env` + interpolated per-server overrides:

```ts
function resolveEnv(env?) {
  const resolved = { ...process.env (undefined filtered) };
  if (!env) return resolved;
  return { ...resolved, ...interpolateEnvRecord(env) };   // overrides layered on top
}
// used at: env: resolveEnv(definition.env)  (server-manager.ts:100, StdioClientTransport)
```

So `${AGENT_VAULT_ADDR}` / `${AGENT_VAULT_TOKEN}` in mcp.json are filled from pi's own env, and **the child inherits all of pi's env** regardless.

#### (b) models.json `apiKey` / `headers` / `authHeader` → pi-core `resolveConfigValue` (dist/core/resolve-config-value.js)
This path supports the `!`-command pattern. Resolution rules (verbatim from the file's docstring):

> - If starts with `!`, executes the rest as a shell command and uses stdout (cached)
> - Interpolates `$ENV_VAR` or `${ENV_VAR}` references with the named environment variable
> - In non-command values, `$$` escapes a literal `$` and `$!` escapes a literal `!`
> - Otherwise treats the value as a literal

```js
// resolve-config-value.js
function parseConfigValueReference(config) {
  if (config.startsWith("!")) return { type: "command", config };
  return { type: "template", parts: parseConfigValueTemplate(config) };
}
function executeCommandUncached(commandConfig) {
  const command = commandConfig.slice(1);          // strip leading "!"
  return executeWithDefaultShell(command);         // (non-win32)
}
function executeWithDefaultShell(command) {
  const output = execSync(command, {
    encoding: "utf-8", timeout: 10000,
    stdio: ["ignore", "pipe", "ignore"],
  });
  return output.trim() || undefined;
}
// results cached process-wide in commandResultCache (Map keyed by full command string)
```

Consumed by the model registry:

```js
// model-registry.js:574-590
const apiKey = apiKeyFromAuthStorage ??
  (providerConfig?.apiKey
     ? resolveConfigValueOrThrow(providerConfig.apiKey, `API key for provider "${model.provider}"`)
     : ...);
headers = { ...headers, Authorization: `Bearer ${apiKey}` };
```

#### The models.json example, decoded
```json
"apiKey": "!AGENT_VAULT_VAULT=sales-brain agent-vault vault credential get DEEPSEEK_API_KEY"
```
- Leading `!` → run as shell command via `execSync`.
- `AGENT_VAULT_VAULT=sales-brain` → a **shell inline env-var assignment** scoped to just this one command, so `agent-vault` runs in agent-mode against the `sales-brain` vault (agent mode requires `AGENT_VAULT_VAULT` or `--vault`).
- `agent-vault vault credential get DEEPSEEK_API_KEY` → prints the decrypted key on stdout.
- pi trims stdout, caches it, and uses it as the DeepSeek provider key. `AGENT_VAULT_TOKEN`/`AGENT_VAULT_ADDR` must already be in pi's env (they are — see below) for the vault call to succeed.

### 6. agent-vault session (how the env vars get there)

`/usr/local/bin/agent-vault` is a statically-linked Go ELF binary. Normal launch is `agent-vault run -- <agent-cmd>` which:
- Validates a token, then `exec`s the child with `AGENT_VAULT_TOKEN`, `AGENT_VAULT_ADDR`, `AGENT_VAULT_VAULT` set.
- Also injects proxy/CA env into the child: `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`, `NODE_USE_ENV_PROXY=1`, `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`, `DENO_CERT` (root CA at `~/.agent-vault/mitm-ca.pem`).

`agent-vault vault credential get <key>` in **agent mode** (`AGENT_VAULT_TOKEN` set) **requires** `AGENT_VAULT_VAULT` or `--vault` — no interactive/project-file fallback.

**Verified live in this environment:** `AGENT_VAULT_ADDR`, `AGENT_VAULT_TOKEN`, `AGENT_VAULT_VAULT` are all SET in the process env. This confirms pi (and its descendants, including the railway MCP child and the `!`-command shell) run inside an agent-vault session and inherit these vars. (`HTTPS_PROXY`/`SSL_CERT_FILE` happened to be unset in this particular subagent shell, but the vault vars — the ones that matter for credential resolution — were present.)

### 7. auth.json structure (keys only — NO values printed)

`/root/.pi/agent/auth.json` (mode 0600) top-level providers `["deepseek","openai-codex"]`:
- `deepseek`: `{ type, key }`
- `openai-codex`: `{ type, access, refresh, expires, accountId }` (OAuth-style)

This is pi's own auth store (separate from the vault). `model-registry` prefers `authStorage.getApiKey()` and falls back to the `!`-command/`apiKey` resolution.

### 8. How Claude Code registers an MCP server (for wiring pi-mcp)

`claude mcp add <name> <commandOrUrl> [args...]` options:

| flag | meaning |
|------|---------|
| `-s, --scope <local\|user\|project>` | where to write (default `local`) |
| `-t, --transport <stdio\|sse\|http>` | default `stdio` |
| `-e, --env KEY=value` | repeatable env vars for the child |
| `-H, --header "K: V"` | headers (http/sse) |

Also `claude mcp add-json <name> '<json>'`, `claude mcp list`, `claude mcp get <name>`, `claude mcp remove <name>`.

Persistence: user/local scope → `~/.claude.json` top-level `mcpServers` (and per-project under `projects["<dir>"].mcpServers`); project scope → `./.mcp.json`. A stdio entry's JSON shape is `{ "type":"stdio"|null, "command": "...", "args": [...], "env": { ... } }` (the existing `railway` entry in `~/.claude.json` is `type:"http"` so its command/args are null). Confirmed `~/.claude.json` already has many projects with `mcpServers` blocks.

Example stdio registration (the pattern pi-mcp will use):
```bash
claude mcp add my-server -e API_KEY=xxx -- npx my-mcp-server
```

### 9. Implications for pi-mcp (Go MCP server)

- pi-mcp will be a **stdio** MCP server registered in Claude Code with `claude mcp add`. When Claude Code spawns it, it inherits Claude Code's own environment (which itself runs under the same agent-vault session → so AGENT_VAULT_* and HOME flow down naturally).
- pi-mcp's job is to spawn `pi -p --mode json` as a subprocess. Because Node/`pi` and its MCP children inherit `process.env` wholesale, pi-mcp **must pass its own environment through unchanged** to the `pi` child (in Go: do **not** set `cmd.Env` to a trimmed list — leave it nil to inherit, or copy `os.Environ()` and only ADD vars). Critical pass-through vars: `HOME` (so pi finds `~/.pi`), `AGENT_VAULT_TOKEN`, `AGENT_VAULT_ADDR`, `AGENT_VAULT_VAULT`, plus the proxy/CA set (`HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`, `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`, `DENO_CERT`, `NODE_USE_ENV_PROXY`), and `PATH` (so `pi`, `agent-vault`, `railway` resolve).
- pi-mcp itself needs **no secrets**: pi already holds/resolves them (auth.json + the `!`-command vault pattern). pi-mcp should never read auth.json or call agent-vault directly.

**Implicações para pi-mcp:**
- The Go pi-mcp server MUST inherit and pass through its full environment to the `pi` subprocess — in Go, leave exec.Cmd.Env nil (inherit) or start from os.Environ() and only append. Trimming the env will break credential resolution.
- Required pass-through vars for `pi`: HOME (locates ~/.pi/agent), PATH (locates pi, agent-vault, railway, node), and the agent-vault session vars AGENT_VAULT_TOKEN, AGENT_VAULT_ADDR, AGENT_VAULT_VAULT. Without these the models.json `!AGENT_VAULT_VAULT=... agent-vault vault credential get ...` apiKey and the railway-mcp-vault wrapper both fail (the script exits 1 if AGENT_VAULT_TOKEN is unset).
- Also pass through the proxy/CA bundle vars that agent-vault injects (HTTPS_PROXY, HTTP_PROXY, NO_PROXY, NODE_USE_ENV_PROXY, SSL_CERT_FILE, NODE_EXTRA_CA_CERTS, REQUESTS_CA_BUNDLE, CURL_CA_BUNDLE, GIT_SSL_CAINFO, DENO_CERT) so pi's own outbound LLM/MCP traffic stays routed through the broker and trusts its MITM CA.
- pi-mcp itself needs NO secrets and should hold none: pi already owns auth.json and resolves vault-backed keys via execSync of the `!`-command. pi-mcp must never read auth.json/agent-vault directly or log resolved key values.
- Because Claude Code already runs inside the same agent-vault session, a pi-mcp stdio server spawned by `claude mcp add` inherits AGENT_VAULT_* / HOME automatically — no `-e` env flags are strictly required for credentials, but pi-mcp must still forward them to the `pi` child.
- Exact registration command (stdio, user scope so it's available everywhere): `claude mcp add -s user pi-mcp -- /usr/local/bin/pi-mcp` (replace path with the built Go binary). Alternatively JSON form: `claude mcp add-json -s user pi-mcp '{"type":"stdio","command":"/usr/local/bin/pi-mcp","args":[],"env":{}}'`. Verify with `claude mcp get pi-mcp`; this writes to ~/.claude.json under top-level mcpServers (user) / projects[dir].mcpServers (local).
- When pi-mcp drives pi headless it should invoke `pi -p --mode json` (confirmed flags from `pi --help`: -p/--print non-interactive, --mode json). Model/provider selection by pi (not Claude) can use --provider/--model or rely on settings.json defaultModel/defaultProvider; the enabled models in this env are deepseek/* and openai-codex/gpt-5.5.
- If pi-mcp ever needs to point pi at a custom MCP config it can forward `--mcp-config <path>` (extension flag), but that only overrides the pi-global layer; project ./.mcp.json and ./.pi/mcp.json still merge based on pi's cwd, so pi-mcp should control the `pi` subprocess working directory deliberately.

**Questões em aberto:**
- Does the `pi-dynamic-workflows-custom` fork (listed in settings.json packages and the focus of the broader study) add its own subagent/fleet-spawn env handling that could strip or rewrite env before launching child `pi` model calls? Not examined in this area — needs the fork's source reviewed.
- When pi runs headless (`pi -p --mode json`) and spawns parallel subagents across many models, is each subagent a separate process inheriting env, or in-process model calls reusing the cached `!`-command result? resolve-config-value caches per-process, so in-process reuse is fine, but multi-process fan-out would re-run the vault command per child (extra agent-vault calls).
- context7 MCP server (type:http) has no auth block in mcp.json — confirm whether it relies on OAuth auto-detection (the adapter auto-detects OAuth when url present and no auth specified) or is unauthenticated; not security-critical for pi-mcp but affects whether headless pi stalls on an OAuth prompt.
- Exact final install path/name for the pi-mcp Go binary is a build decision (assumed /usr/local/bin/pi-mcp here) — the registration snippet must use the real path.


---

# Implicações transversais (consolidado)

- ENVIRONMENT PASSTHROUGH IS THE #1 CORRECTNESS REQUIREMENT: the Go server must inherit and forward its full environment to the pi child (exec.Cmd.Env = nil, or os.Environ() + appends only). Required vars: HOME (locates ~/.pi/agent), PATH (pi/node/agent-vault/railway), and the agent-vault session vars AGENT_VAULT_TOKEN / AGENT_VAULT_ADDR / AGENT_VAULT_VAULT, plus proxy/CA vars (HTTPS_PROXY, HTTP_PROXY, NO_PROXY, NODE_USE_ENV_PROXY, SSL_CERT_FILE, NODE_EXTRA_CA_CERTS, REQUESTS_CA_BUNDLE, CURL_CA_BUNDLE, GIT_SSL_CAINFO, DENO_CERT). Without AGENT_VAULT_* the deepseek tier (!-command apiKey) and railway MCP wrapper fail; trimming env breaks all credential resolution. pi-mcp itself holds NO secrets and must never read/echo auth.json or vault values.
- FORCE DELEGATION VIA PROMPT, NOT STANDING MODES: /effort and /ultracode auto-arming are dead headlessly (input hook bails on source!='interactive'). The MCP must replicate buildForcedWorkflowPrompt in the prompt it sends pi: explicitly demand a single `workflow` tool call AND explicitly state background:false / return result inline this turn (the tool guideline biases toward background:true). Optionally harden with --append-system-prompt. The workflow tool stays registered/active headlessly (session_start fires via bindExtensions), so no consent pre-seeding is needed (and hasUI=false skips the preflight card anyway).
- BACKGROUND:FALSE IS THE SYNC CONTRACT: background:true returns only a runId + 'started' text and delivers the real result in a LATER injected workflow-result turn that single-shot `pi -p` will not wait for (process likely exits at first agent_end). Use background:false for 'delegate and get one synthesized result inline'. Reserve background:true strictly for long fleets, and in that case poll <runId>.json until status==completed and read .result from the file.
- PARSE STDOUT AS JSONL, RESULT FROM agent_end.toolResult: read stdout line-by-line into a {Type string} struct, switch on Type, ignore unknown types forward-compatibly, tolerate the leading `session` line (emitted even with --no-session). The synthesized result is the terminal agent_end.messages[] role:'toolResult' with toolName=='workflow'; prefer details.result (raw object) over stripping the header + fenced ```json``` from content[0].text. Use a bufio.Scanner with a >1MB buffer (or streaming json.Decoder) — lines carry full scripts and encrypted thinking signatures. Success signal = exit 0 + empty stderr; non-zero exit or non-empty stderr = transport failure.
- RUN FILE IS AUTHORITATIVE FOR FLEET/PROGRESS, NOT THE STREAM: the stream's message.model is the orchestrator model, not subagents. Read /root/.pi/workflows/runs/<runId>.json agents[].model for the distinct fleet pi actually chose (build a model histogram). Glob *.json EXCLUDING *.json.bak and *.tmp (mirror RunPersistence.list), parse-fall-back to <runId>.json.bak on a corrupt primary, sort by updatedAt desc. Treat result/tokenUsage/durationMs/completedAt as OPTIONAL pointer fields (absent, not null, on non-completed runs).
- CWD CONTROLS WHERE RUNS LAND AND WHICH CONFIG APPLIES: WORKFLOW_RUNS_DIR is cwd-relative (.pi/workflows/runs), and project-level .pi/agents + ./.pi/mcp.json/.mcp.json merge based on pi's cwd; only ~/.pi (user-level) tiers/agentTypes apply otherwise. pi-mcp must launch pi with a deliberate, known cwd (e.g. $HOME so runs resolve to ~/.pi/workflows/runs) and look there for visibility tools.
- STRICT MODEL ROUTING IS ON (fallback.mode='error'): an unauthed/mistyped model or unknown tier hard-fails the whole run with MODEL_ROUTING_ERROR rather than degrading. The run FILE does not persist the WorkflowError code (only status failed/aborted + free-text agents[].error and the sidecar log) — capture the structured code from stdout JSON if needed. A preflight tool diffing tier/rule model specs against `pi --list-models`/authed availability can warn before launching. Verify openai-codex/gpt-5.5 (big/coder/judge) is authed since it is not in models.json (comes from built-in registry + auth.json OAuth).
- DO NOT TOUCH MODELS OR DECOMPOSITION: pi owns model selection (resolveModelRoute) and decomposition (the LLM-authored script's agent()/parallel()/pipeline()). The MCP must NOT pass per-task model/provider/tier/temperature and must NOT shard or spawn multiple pi processes to spread models — one pi run already fans out across the configured heterogeneous fleet under a single 16-wide limiter. Fleet changes are a USER concern (edit model-tiers.json / .pi/agents/*.md).
- JOBID -> RUNID CORRELATION IS RACE-PRONE: pi generateRunId() = Date.now().toString(36)+'-'+Math.random().toString(36).slice(2,8) is internal, not a UUID, and not CLI-settable; it differs from sessionId (a UUID in the run file). Correlate by capturing runId from pi's JSON stream when present, else match on the persisted sessionId field, else diff the runs dir for the new file (only safe under serialized launches). Maintain a jobId -> {sessionId, runId} table.
- LIVENESS DETECTION IS NOT FILE-DERIVABLE: a 'running' status on disk may be a CRASHED process; a single-shot `pi -p` may not re-instantiate the WorkflowManager whose recoverStaleRuns() would flip stale 'running'->'paused'. Track the pi child PID per job; treat status=='running' + dead PID as crashed, with updatedAt staleness as a secondary signal. Terminal check = status=='completed' (only state setting completedAt/durationMs); failed/aborted are also terminal.
- STRUCTURED OUTPUT IS THE RELIABLE MACHINE-READABLE CHANNEL, BUT THE WHOLE-WORKFLOW RETURN IS UNTYPED: per-agent typebox schema is validated (2 repair retries + prose extraction, else SCHEMA_NONCOMPLIANCE), but result.result is whatever the script returns. Define the output contract in the prompt (e.g. return {ok, verdict, report}) and reuse summarizeResult's verdict>report>summary field precedence when extracting one synthesized answer. Token budget is SOFT/post-hoc — surface this and set conservative budgets; real usage depends on providers reporting it (else length/4 estimate).
- NO BUILD/DAEMON COUPLING: the fork runs TypeScript source via the settings.json packages local path, so each `pi -p` is a fresh process picking up current src/ automatically — no dist build, no /reload, no persistent daemon to manage. Determinism constraints (no Date.now/Math.random/new Date; meta must be first statement, object-literal only) shape any script the MCP or pi generates; route time/randomness through args. The vm is NOT a security sandbox — treat workflow scripts as trusted/LLM-authored only.
- REGISTER pi-mcp OVER STDIO AT USER SCOPE: `claude mcp add -s user pi-mcp -- /usr/local/bin/pi-mcp` (or add-json equivalent), written to ~/.claude.json mcpServers. Since Claude Code already runs in the same agent-vault session, the stdio child inherits AGENT_VAULT_*/HOME automatically — but pi-mcp must still forward them to the pi grandchild. Use the real installed binary path.

# Questões em aberto priorizadas

- Does the model reliably emit background:false when the MCP prompt explicitly demands it? The workflow tool's promptGuidelines strongly default to background:true ('Pass background:false only when you must use the result inline'). If the model ignores the instruction, the sync delegate pattern breaks — needs a live end-to-end probe; fallback is a forced/saved-workflow path or driving a known saved script's `.script` directly as the tool's `script` param.
- Under single-shot `pi -p --mode json`, does the process stay alive long enough to emit a background workflow's follow-up workflow-result turn, or does it exit at the first agent_end (losing the result)? Code strongly implies print-mode disposes after the initial prompt(s), which is the core reason to use background:false. Must be confirmed by launching a real background workflow and watching both stdout and the run file — this determines whether background mode is viable at all under `-p`.
- Where exactly does headless `pi -p --mode json` write runs (always $HOME, the invocation cwd, or configurable)? WORKFLOW_RUNS_DIR is cwd-relative per config, so pi-mcp must control and know the cwd to locate run files deterministically. Confirm by probing, and decide whether to force pi's cwd to a fixed known dir.
- Is openai-codex/gpt-5.5 (backing the big/coder/judge tiers) actually authed/available in pi's registry on this box? It is NOT defined in models.json (must come from the built-in provider registry + auth.json OAuth). Under the live strict config (fallback.mode='error') an unavailable model hard-fails any big/coder/judge route — verify availability via `pi --list-models`/registry without touching secrets before relying on those tiers.
- Does a genuinely live run ever persist status=='running' (vs only terminal/paused snapshots), and are tokenUsage/durationMs written incrementally during a run or only at completion? The observed corpus contains only terminal/paused snapshots. This determines whether mid-flight progress UIs can show partial token accounting and whether 'running' is a real observable state. Probe with a long headless run read mid-flight.
- Can a crashed background `pi -p` leave a run file stuck at 'running' indefinitely (because the one-shot process never re-instantiates WorkflowManager.recoverStaleRuns to flip stale 'running'->'paused')? This dictates whether pi-mcp must track child PIDs for liveness — confirm whether the headless one-shot instantiates a long-lived manager.
- Is there any CLI surface to pass/override runId or sessionId so pi-mcp can deterministically correlate jobId -> runId without diffing the runs directory under concurrency? sessionId appears settable via setSessionId on session_start but the CLI surface is unverified; without it, concurrent launches risk mis-attribution.
- On the FAILURE path of `--mode json`, are WorkflowErrorCode/extension_error/auth_retry/compaction_end errorMessage events emitted to stdout (so pi-mcp can surface the structured error code that the run file omits), or only to stderr? The probe was a trivial no-tool success run; failure-path framing is unconfirmed and the run file only records failed-vs-aborted + free-text agent errors.
- Does the fork add its own subagent/fleet-spawn env handling that could strip or rewrite env before launching child model calls, and does multi-model fan-out re-run the `!`-command vault fetch per child process (extra agent-vault calls) or reuse the per-process commandResultCache? resolve-config-value caches per process, so in-process reuse is fine, but out-of-process fan-out would re-hit the vault — affects latency and vault load.
- Exact sidecar log-filename derivation (runId mq3v6uyo-pmtqtv -> run-mq3v6uyv.log; prefix re-encoded, not the runId suffix) is unconfirmed against engine source — until verified, ALWAYS read the log path from logs[0] rather than reconstructing it.
- Confirm the bundled fork extension is actually loaded by the production /usr/bin/pi the MCP shells out to (not only the fork's local dist), and that setActiveTools adds the `workflow` tool on session_start headlessly — verify via pi config / settings.json extension list and a live `pi -p` probe asserting a successful toolName=='workflow' toolResult.
- Where does the MCP's TASK payload actually land in the persisted run (top-level args vs the script's prompt), given some runs show args:null? Needed so visibility tools can attribute a run to its originating task and so resume (which keys on script+journal) behaves as expected.

