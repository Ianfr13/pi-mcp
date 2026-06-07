# pi-mcp — Design (v2, pós review)

> **Status:** plan-ready (review adversarial aplicado) · **Data:** 2026-06-07 · **Stack:** Go (SDK `github.com/modelcontextprotocol/go-sdk`) · **Projeto:** `/root/pi-mcp`
> Base: estudo `docs/research/2026-06-07-pi-and-dynamic-workflows-study.md` + validação ao vivo (§3). v2 incorpora os fixes do review de completude (§14).

## 1. Objetivo & princípios

Servidor **MCP em Go** que deixa o **Claude Code** (CLI/desktop) delegar uma **TAREFA** ao engine de dynamic-workflow do `pi`. O **`pi` decompõe a tarefa e escolhe os modelos**, roda uma frota de subagentes em **vários modelos diferentes ao mesmo tempo** e devolve um resultado sintetizado — com **resultados intermediários ao vivo**.

**Princípios:**
- **pi é dono da decomposição e da escolha de modelos** (via `~/.pi/workflows/model-tiers.json`, strict). O MCP não fragmenta tarefa, não passa model/tier/provider por chamada, não roda vários `pi` para "espalhar" modelos — um `pi -p` já faz o fan-out heterogêneo.
- **pi-mcp não guarda segredos** e **não loga stdout/stderr/conteúdo de run file** (só metadados) — esses canais carregam resultados que podem conter segredos. O `pi` resolve auth (`auth.json` OAuth + `agent-vault`) sozinho.
- **`mode` = isolamento + formato de entrega, NÃO restrição de capacidade.** As tools do `pi` **nunca** são restringidas (decisão do usuário: "sem limite" vale no read também — e além disso o `--exclude-tools` do pai não chega aos subagentes do fan-out, ver §14/§4.1). `read` roda **in-place no cwd** (pode modificá-lo); `write` roda **isolado num git worktree** e entrega branch+diff.
- **Dependência externa:** o gatilho headless do workflow será garantido pelo **usuário** (mod no fork). O pi-mcp envia o **prompt forçador** (validado, §4) e **assume que o workflow roda** — sem retry.

## 2. Arquitetura

```
+-------------+    MCP / stdio (go-sdk)   +-----------------------+
| Claude Code | <-----------------------> |  pi-mcp (Go)          |
|             |  pi_workflow(mode,cwd)    |  mcpserver | jobs(cap4)|
|             |  pi_status(wait)          |  runner | parser       |
|             |  pi_list | pi_cancel      |  runstore | worktree   |
+-------------+                           |  registry persistido   |
                                          +-----------------------+
                                               | os/exec (1 proc/job, goroutine)
                                               |  stdin=/dev/null  (CRÍTICO)
                                               |  env=os.Environ() (passthrough)
                                               |  cwd = projeto (read) | worktree (write)
                                               v
                                 pi -p --mode json --no-session [--no-context-files]
                                   <forcing prompt + contrato>   (hasUI=false → consent/checkpoint pulados)
                                               | orquestrador AUTORA o script (a "divisão") → tool workflow(background:false)
                                               v
                                 engine (vm realm) — resolveModelRoute() por agente
                                   flash | pro | gpt-5.5 | ...   (frota, limiter 16-wide)
                                               |
   stdout JSONL: orquestrador streama ao vivo | frota é SILENCIOSA aqui (só tool_execution_start/end)
   run file <launch-cwd>/.pi/workflows/runs/<runId>.json: agents[]/journal[] crescem AO VIVO
                                               |
                 pi_status → intermediários (journal, ao vivo) + final (.result) + metadados → Claude
```

## 3. Validação empírica (2026-06-07)

Probe forçando workflow `background:false`:

| Fase | Agente | Modelo (pelo pi) |
|---|---|---|
| Scan | claim A/B/C | `deepseek/deepseek-v4-flash` ×3 |
| Final | final synthesis | `openai-codex/gpt-5.5` |

- ✅ **Multi-modelo ao vivo:** 1 tarefa → 4 agentes em 2 modelos (regras de tier). Fixture canônico `sample-run.json`: `by_model {flash:3, gpt-5.5:1}`, `agentCount 4`, `tokenUsage.total 120469`, `cost 0.1463847`, `22,4s`.
- ✅ `background:false` retorna resultado inline. Runs em **`<launch-cwd>/.pi/workflows/runs/`** (cwd-relativo). gpt-5.5 autenticado.
- ✅ **Streaming:** o stdout streama a autoria do orquestrador ao vivo; a **frota é silenciosa no stdout** (só `tool_execution_start`→`tool_execution_end`; 1 `agent_start/end` top-level). Progresso/intermediários vêm do **run file**.
- ✅ **Run file ao vivo:** `agents[]` e `journal[]` crescem incrementalmente (amostra 10 agentes: done 0→1→7→9→11). `journal[].result` = resultado **completo** por-agente (join por `index`==`callIndex`); `agents[].resultPreview` = preview truncado.
- 🔴 **stdin DEVE ser `/dev/null`** (senão `pi -p` trava no stdin). Janela cega ~20s antes do run file existir. `status` no disco oscila `running`↔`paused` (não-terminal). `cost`/`tokenUsage` só no fim. Resultado final do `.result`/`tool_execution_end`; mensagem final do assistente é **vazia**.

Fixtures: `docs/research/fixtures/{sample-pi-mode-json-events.jsonl, sample-run.json (com journal[]), sample-forced-workflow-prompt.txt}`.

## 4. Contrato de invocação do `pi`

```
pi -p --mode json --no-session [--no-context-files]  <PROMPT_POSICIONAL_ÚNICO>
stdin : /dev/null                      # CRÍTICO (senão trava)
env   : os.Environ() inteiro           # HOME, PATH, AGENT_VAULT_{ADDR,TOKEN,VAULT}, proxy/CA — passthrough INTENCIONAL
cwd   : projeto (read) | worktree do job (write)
```
- **PROMPT** é UM único elemento de argv (sem shell, sem escaping; newlines preservados). TASK/CONTEXT entram como **dados delimitados**, nunca interpolados em script executável (o vm do engine NÃO é sandbox de segurança — §14).
- `--no-context-files`: **opcional/configurável.** Default = **ligado** para tarefas de código (deixa o pi enxergar AGENTS.md/CLAUDE.md do projeto). Pode desligar para delegação hermética.
- **Sucesso** = exit 0 **e** existe `tool_execution_end(toolName="workflow", isError:false)` no stream **ou** run file `status=completed`. **Falha** = exit≠0, `isError:true`, ou `status=failed/aborted`. `stderr` é diagnóstico (logado como metadado, não é o gate).

### 4.1 `mode` (isolamento + entrega; NÃO restringe tools)
| | **read** | **write** |
|---|---|---|
| cwd do pi | o **projeto** (in-place) | **git worktree** novo, fora do working tree do usuário |
| tools do pi | completas (pode mutar o cwd — usuário aceitou) | completas |
| git | não exige | **exige** (senão `NOT_A_GIT_REPO`, falha rápido) |
| entrega | `result` (contrato) | `result` + `{branch, worktree_path, diff_stat, files_changed[]}` |

- `mode` é **obrigatório** (sem default). `cwd` é **obrigatório** (nunca cai no cwd do servidor).
- **write worktree:** criado **fora** do working tree (sob `$XDG_STATE_HOME/pi-mcp/worktrees/` ou tempdir), branch fresca `pi-mcp/job-<jobId>` a partir do HEAD; checa colisão de nome. **Dirty/untracked HEAD:** documentar — default **warn + prossegue** (branca do HEAD; mudanças não-commitadas do usuário não entram no worktree). Os agentes de write **editam o worktree compartilhado do job** (NÃO usar o `isolation:'worktree'` por-agente do engine — ele criaria sub-worktrees por agente, deixando `files_changed`/`diff` vazios; ver §14). Em cancel/crash: `git worktree prune` + remover worktrees/branches `pi-mcp/job-*` órfãos.
- **read:** roda no projeto; o run file cai em `<projeto>/.pi/workflows/runs/`. **Sem garantia de não-mutação** (o `--exclude-tools` do pai não atinge os subagentes; ver §14). Quem quer isolamento usa `write`.

### 4.2 Forcing prompt (template exato)
Fonte-de-verdade: `docs/research/fixtures/sample-forced-workflow-prompt.txt`. Constante em `internal/config`:
```
You MUST make exactly ONE call to the `workflow` tool with background:false. Do not answer
directly. Do not use background:true. Return the final synthesized result INLINE this turn.
Decompose the task as you see fit and fan out subagents in parallel.
The workflow MUST return an object matching exactly this JSON shape:
<CONTRACT_JSON_SKELETON: read|write §5.4>

TASK:
<TASK>

[CONTEXT:
<CONTEXT>]
```
- Único mecanismo de disparo (effort/ultracode estão mortos headless). O formato do retorno é **pedido, não imposto** (coerção best-effort, §5.4).

## 5. Superfície MCP (4 tools)

### 5.1 `pi_workflow`
```jsonc
{
  "task": "string (obrigatório)",
  "mode": "read | write (OBRIGATÓRIO)",
  "cwd":  "string (OBRIGATÓRIO) — caminho absoluto existente; rejeita '..'; resolve symlinks; nunca o cwd do servidor",
  "context": "string (opcional)"
}
```
**Retorno (imediato, não bloqueia):**
```jsonc
{ "jobId":"<uuidv4>", "status":"queued|running", "mode":"read|write",
  "cwd":"...", "worktree_path":"... (iff write)", "started_at":"<RFC3339 UTC>" }
```
`jobId` = UUIDv4 do servidor (chave do registry + nome da branch no write).

### 5.2 `pi_status` — intermediários (ao vivo) + final
```jsonc
{ "jobId":"string" }  // ou { "runId":"string", "cwd":"string" } p/ runs externos / pós-restart
{ "wait": false }     // opcional: long-poll
```
**Retorno:**
```jsonc
{
  "jobId":"...", "runId":"mq40rdpt-yij9hj | null", "status":"queued|running|completed|failed|aborted",
  "phase":"Scan | null",                 // = run-file .currentPhase
  "blind_window": false,                  // true enquanto o run file ainda não existe (~20s de autoria)
  "intermediate": [                       // agentes concluídos, AO VIVO; cresce a cada poll
    { "label":"claim A", "model":"deepseek/deepseek-v4-flash", "phase":"Scan",
      "result": <journal[].result COMPLETO, join por index==callIndex>,   // truncado p/ preview+flag se > N KB
      "truncated": false }
  ],
  "result": <contrato §5.4 — autoritativo do run-file .result quando status=completed>,
  "metadata": { "by_model":{"deepseek/deepseek-v4-flash":3,"openai-codex/gpt-5.5":1},
                "agentCount":4, "tokenUsage":{"total":120469,"cost":0.1463847}, "durationMs":22391 }, // cost/tokens só no fim
  "write": { "branch":"pi-mcp/job-...", "worktree_path":"...", "diff_stat":"...", "files_changed":[...] }, // iff write
  "error": "string (failed/aborted)"
}
```
**Mapping disco→MCP:** `running|paused`→`running` (paused é não-terminal); `completed`→`completed`; `failed`→`failed`; `aborted`→`aborted`. **Override de liveness:** `running` no disco com `updatedAt` mais velho que `STALE_THRESHOLD` (default 300s = `DEFAULT_AGENT_TIMEOUT_MS`, configurável) **ou** (mesma sessão) PID morto → `failed`.
**Blind window:** antes do run file existir → `{status:running, blind_window:true, runId:null, intermediate:[]}`. `pi_status{runId}` para run inexistente → `queued/pending`, **não** `NO_WORKFLOW_RUN`.
**`wait` (long-poll):** cap 60s (injetável p/ teste); acorda quando: status terminal **OU** `len(journal)`/`len(agents)` cresceu **OU** mudou `phase`. **Exclui** flapping `running↔paused`.
**Intermediários:** join `journal[].index == agents[].callIndex` (NUNCA por posição do array — journal é em ordem de conclusão; NUNCA por `agents[].id` que = callIndex+1). Resultado completo do `journal[].result`; se > limite, devolve `resultPreview` + `truncated:true` (+ fetch on-demand via `pi_status` com filtro de agente — Fase 2).

### 5.3 `pi_list`
`{ "cwd":"string (obrigatório — resolve o runs dir)", "limit":20 }` → lista `{ runId, workflowName, status, agentCount, by_model, cost, durationMs, completedAt }` de `<cwd>/.pi/workflows/runs/*.json` (exclui `*.bak`/`*.tmp`; fallback `.bak` se corrompido), por `updatedAt` desc. Para write jobs, o `cwd` é o `worktree_path`.

### 5.4 Contrato de saída default (pedido no prompt; coerção best-effort no pi-mcp)
- **read:** `{ summary, findings:[{title, detail, severity?}], confidence, open_questions }` (`severity` ∈ low|med|high).
- **write:** `{ summary, files_changed:[string], diff_summary, tests_run?, notes }`.
- **Coerção** (engine NÃO valida o retorno do workflow): (a) objeto no formato → passthrough; (b) string/escalar → `{summary:<raw>}`; (c) objeto com chaves diferentes (ex.: fixture `{claims,overall}`) → `{summary:<sinopse>, ...preserva campos originais}` (nunca descarta dados); (d) ausente em `completed` → `{summary:""}` + warn.

### 5.5 `pi_cancel`
`{ "jobId":"string" }` → mata o processo `pi`, marca `aborted`, lê parcial do run file; no write faz `git worktree prune` + remove a branch/worktree órfã. Essencial (sem timeout no nível do MCP).

## 6. Componentes (pacotes Go)
```
cmd/pi-mcp/main.go     # MCP server (go-sdk, stdio); registra as 4 tools
internal/mcpserver/    # handlers + validação (mode/cwd obrigatórios; cwd absoluto/existe/sem '..')
internal/runner/       # subprocess pi: monta prompt+contrato, spawn (stdin=/dev/null, env=os.Environ(), cwd), kill via context
internal/parser/       # stream JSONL: linha-a-linha, switch .type, ignora tipos desconhecidos; extrai sessionId;
                       #   resultado = strip do header "✓ Workflow ... finished" + parse do bloco ```json``` em content[0].text
internal/runstore/     # lê <cwd>/.pi/workflows/runs/*.json → Run/Agent; .result autoritativo; histograma de modelos; join journal↔agents
internal/worktree/     # write: cria worktree fora do tree (XDG_STATE/temp), branch pi-mcp/job-<id> do HEAD; diff_stat; prune no cancel
internal/jobs/         # registry map[jobID]*Job + mutex; cap 4 + fila; correlação por sessionId; liveness PID+staleness;
                       #   PERSISTE jobId→{runId,cwd,worktree_path,runsDir,pid,status} em disco (recuperável pós-restart); reconciliação no startup
internal/config/       # paths (pi bin), cap, STALE_THRESHOLD, flags (--no-context-files etc.), template do forcing prompt
internal/model/        # structs (ver §7) + erros
*_test.go              # ver §10
```

## 7. Estruturas de dados (validadas) + tipos Go

**Run file** `<launch-cwd>/.pi/workflows/runs/<runId>.json` — chaves: `runId, sessionId, workflowName, status, currentPhase, phases, agents[], journal[], logs[], result, script, startedAt, completedAt, updatedAt, durationMs, tokenUsage, args(opcional, null em headless)`.
- `agents[]`: `{ callIndex, id(=callIndex+1), label, model, phase, prompt, resultPreview, startedAt, endedAt, status, tokens }`.
- `journal[]`: `{ index(=callIndex), hash, result }` — `result` = **resultado COMPLETO** por-agente; **ordem de conclusão** (ex.: index 0,2,1,3).
- `tokenUsage`: `{ input, output, total, cost(float64 VERBATIM, ex. 0.1463847), cacheRead, cacheWrite }` (só no fim).

**Tipos Go (não adivinhar):** opcionais como ponteiros (`CompletedAt *time.Time`, `DurationMs *int64`, `Result json.RawMessage`, `TokenUsage *TokenUsage`); `journal[].result` e `result` como `json.RawMessage` (shape arbitrário do workflow); `Cost float64` lido verbatim (nunca recomputar). **runstore é dono do `.result` canônico** quando `completed`; o parser stream é fallback ao vivo/cedo.

**Stream JSONL** (`pi -p --mode json`): tipos reais — `session, agent_start, agent_end, turn_start, turn_end, message_start, message_end` e dentro de mensagens blocos `text, thinking, toolCall, tool_execution_start, tool_execution_end` (NÃO há `message_update` no `-p`). `session.id` == run-file `sessionId` (capturado da 1ª linha, antes do run file existir).
- **Resultado do stream:** `tool_execution_end(toolName="workflow").result = { content:[{type,text}] }` — **NÃO** traz `details{result,runId}`. Extrair = tirar a linha de header e parsear o bloco ```json``` de `content[0].text` (fallback: texto cru). **Nunca** da mensagem final (vazia).
- Parser: `bufio.Scanner` com buffer adequado (linha máx ~13,5KB no fixture; o script vive no run file, não numa linha do stdout — buffer ≥1MB só se necessário). Teste com `{type:"__unknown__"}` → ignora sem erro.

## 8. Concorrência / jobs / liveness
- 1 job = 1 goroutine + 1 processo `pi`. **Cap 4** (configurável) + fila. **Teto efetivo:** 4 jobs × `MAX_CONCURRENCY=16` = até **64 sessões de modelo vivas** (e até 4×1000 agentes), **sem tokenBudget** (decisão #12) — cap 4 justificado por rate limit dos providers. (Opcional Fase 2: expor `maxAgents`/`tokenBudget` como trilhos.)
- **Sem timeout no nível do MCP** (controle via `pi_cancel`). O **engine** ainda aplica `DEFAULT_AGENT_TIMEOUT_MS` (~5min) por-agente (surge como `agents[].status=error`/result null) — o pi-mcp não interfere.
- **Correlação jobId→runId:** `sessionId` do 1º evento `session`; casa com `run.sessionId`. **Registry persistido em disco** (`jobId→{runId, sessionId, cwd, worktree_path, runsDir, pid, status}`) — necessário p/ achar o run de um **write job** (runs no worktree) e p/ recuperar pós-restart.
- **Liveness:** mesma sessão → PID vivo + estado terminal. Pós-restart (sem PID; `pi -p` one-shot não roda `recoverStaleRuns`) → heurística de **staleness** (`running` + `updatedAt` > `STALE_THRESHOLD` = crashed). **Reconciliação no startup:** varre `pi-mcp/job-*` worktrees + o registry persistido, marca terminais/stale e faz GC de órfãos.
- **TASK não está em `args`** do run file (null em headless) — vai no prompt do script. Atribuição de run→task só via o map do pi-mcp; run recuperado do disco não re-atribui à TASK sozinho.

## 9. Erros — catálogo

| Engine `WorkflowErrorCode` / situação | Superfície pi-mcp |
|---|---|
| `MODEL_ROUTING_ERROR` (tier/model não-autenticado; config strict) | `failed` + "conferir model-tiers.json" |
| `AGENT_TIMEOUT` / `AGENT_EXECUTION_ERROR` | `failed` ou agente parcial em `intermediate` |
| `WORKFLOW_ABORTED` | `aborted` |
| `AGENT_LIMIT_EXCEEDED`, `TOKEN_BUDGET_EXHAUSTED`, `SCRIPT_VALIDATION_ERROR`, `SCHEMA_NONCOMPLIANCE`, `PERSISTENCE_ERROR`, `UNKNOWN` | `failed` + código |
| cwd write não-git | `NOT_A_GIT_REPO` (falha antes de spawnar) |
| sem `tool_execution_end(workflow)` | `NO_WORKFLOW_RUN` (sem retry — trigger assumido; erro claro se ainda não wired) |
| stdin não-/dev/null | prevenido na origem |

**Ordem de extração da mensagem de falha:** (1) `tool_execution_end(workflow) isError:true` text; (2) `agents[].error`/`status=error`; (3) log sidecar via **`logs[0]` verbatim** (NUNCA reconstruir o nome — prefixo é mangled: runId `mq40rdpt` vs log `run-mq40rdpu`). Run `.error` top-level ausente na prática; runs failed omitem `result/tokenUsage/completedAt/durationMs`.

## 10. Testes (barra: 100% e2e)
- **parser:** contra `sample-pi-mode-json-events.jsonl` — sessionId; resultado via header-strip + bloco ```json```; tipo desconhecido ignorado; **fixture negativo** `sample-run-no-workflow.jsonl` (resposta direta sem `tool_execution_end(workflow)`) → `NO_WORKFLOW_RUN`.
- **runstore:** contra `sample-run.json` — schema, `.result` autoritativo, histograma `{flash:3,gpt-5.5:1}`, join `journal.index↔agents.callIndex` correto na **ordem fora-de-sequência**, opcionais ausentes.
- **fixture parcial** `sample-run-partial.json` (a criar na Fase 0): `status=running` + variante `paused`; journal subset fora de ordem (ex.: index 0,2); agents mistos done/running; `result/tokenUsage/completedAt/durationMs` OMITIDOS → testa intermediários ao vivo, long-poll wake-on-new-agent, supressão de custo durante o run, handling running↔paused.
- **worktree:** cria/lista/diff num repo de teste; fora do tree; `NOT_A_GIT_REPO`; prune no cancel.
- **runner:** `pi` fake (emite fixture, respeita stdin=/dev/null, cwd, env passthrough, kill).
- **jobs:** lifecycle, cap 4 + fila, correlação por sessionId, cancel, liveness (PID + staleness), persistência + reconciliação pós-restart.
- **e2e real (gate `PI_MCP_E2E=1`):** cliente MCP go-sdk via stdio → `pi_workflow{mode:read}` → poll `pi_status{wait}` até `completed`; assere fan-out multi-modelo. Distinto do probe cru do pi; requer o trigger do fork (senão `NO_WORKFLOW_RUN` esperado).

## 11. Fases
- **Fase 0 (gate):** ✅ contrato de invocação validado nesta sessão (stdin, multi-modelo, run file ao vivo, journal completo, runs em `<cwd>/.pi/...`). A fazer: gerar `sample-run-partial.json` e `sample-run-no-workflow.jsonl` (capturar de runs reais).
- **Fase 1 (MVP):** 4 tools; parser/runstore/runner/worktree/jobs robustos; intermediários+final ao vivo; stdin=/dev/null + env passthrough + cwd obrigatório; mode read(in-cwd)/write(worktree); registry persistido + reconciliação; liveness PID+staleness; catálogo de erros; cap 4; coerção do contrato; sem timeout (cancel).
- **Fase 2:** fetch on-demand de resultado grande por-agente; health-check de tiers (`pi --list-models` vs config); `maxAgents`/`tokenBudget` opcionais; invocar workflows salvos por nome.

## 12. Decisões (consolidado)
Capacidade ilimitada (read **e** write); `mode` = isolamento+entrega (read in-cwd pode mutar / write worktree+diff), **sem** restrição de tools. `mode` e `cwd` obrigatórios; cwd nunca = servidor. Contrato default rico. `pi_status` = intermediários (journal, ao vivo) + final. Cap 4 + fila. Sem timeout (engine ainda tem ~5min/agente). `pi_cancel` no MVP. Sem orçamento. Trigger headless garantido pelo usuário (fork) → forcing prompt + assume roda, sem retry. Registry **persistido em disco** + fallback via run files. Long-poll opt-in. Nomes `pi_workflow/pi_status/pi_list/pi_cancel`, bin `pi-mcp`. go-sdk oficial. Resultado: run-file `.result` autoritativo + stream (bloco json) como fallback ao vivo.

## 13. Instalação
```
go build -o /usr/local/bin/pi-mcp ./cmd/pi-mcp
claude mcp add -s user pi-mcp -- /usr/local/bin/pi-mcp
```
Claude Code herda `AGENT_VAULT_*`/`HOME`; o pi-mcp repassa ao neto `pi` (`cmd.Env = os.Environ()`). pi-mcp não recebe nem loga segredos.

## 14. Notas de segurança (verificadas na fonte)
- **`--exclude-tools` NÃO atinge os subagentes do fan-out.** Fonte: `src/agent.ts:363` `this.baseTools = options.tools ?? createCodingTools(this.cwd)` e `:404` (cwd por-call → `createCodingTools(runCwd)`). Restrição de tools de subagente só via agentType em `.pi/agents/*.md` (allowlist/`disallowedTools`, `agent-registry.ts`) — **nenhum existe**. Logo o `mode=read` **não** garante não-mutação; isolamento real só via `write` (worktree). Decisão do usuário: aceitar mutação no read.
- **Env passthrough é intencional e amplo:** repassa `AGENT_VAULT_TOKEN` (bearer do vault) a uma frota com `bash` → a frota pode puxar credenciais arbitrárias do vault. É o **mesmo limite de confiança de rodar o `pi` interativamente**. pi-mcp **não loga** stdout/stderr/run-file (evita persistir segredos em trânsito).
- **vm do engine NÃO é sandbox de segurança** (host `Function` alcançável via `.constructor`). O único limite é OS-level (cwd/worktree). TASK/CONTEXT entram como **dados delimitados**, nunca interpolados no script.
- **engine `isolation:'worktree'` por-agente FALHA ABERTO** (no-op no cwd compartilhado em não-git) — não confiar para containment. Containment vem só do cwd/worktree controlado pelo MCP.
