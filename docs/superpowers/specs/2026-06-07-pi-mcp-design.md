# pi-mcp — Design

> **Status:** spec revisado (pós-grilling) · **Data:** 2026-06-07 · **Stack:** Go · **Projeto:** `/root/pi-mcp` (greenfield)
> Fundamentado no estudo profundo (`docs/research/2026-06-07-pi-and-dynamic-workflows-study.md`) e **validado ao vivo** (§3). Decisões do grilling consolidadas em §12.

## 1. Objetivo

Servidor **MCP em Go** que deixa o **Claude Code** (CLI/desktop) ser o orquestrador conversacional e **delegar tarefas ao engine de dynamic-workflow do `pi`**. Em vez de eu (Claude) usar meu próprio Workflow tool — só subagentes Claude —, mando uma **TAREFA** ao `pi`; o **`pi` decide a decomposição e escolhe os modelos**, roda uma frota de subagentes em **vários modelos diferentes ao mesmo tempo** (DeepSeek, GPT‑5.5, Claude, Gemini, Kimi… via OpenRouter + codex) e devolve um resultado sintetizado.

**Princípios inegociáveis:**
- O `pi` é dono da **decomposição** e da **escolha de modelos** (via `~/.pi/workflows/model-tiers.json`, roteamento strict). O MCP **não** fragmenta tarefa, **não** passa model/tier/provider por chamada, **não** roda vários `pi` para "espalhar" modelos.
- O **pi-mcp não guarda segredos**. O `pi` resolve auth (`auth.json` OAuth + `agent-vault`) sozinho; o servidor só **repassa o ambiente**.
- **Capacidade do pi é ilimitada** (ler/escrever/editar/bash), mas a **segurança vem do `mode`** (§4.1): `write` roda isolado em git worktree; `read` roda no cwd com tools só-leitura.

**Dependência externa:** o gatilho headless do workflow será garantido pelo **usuário** (modificação no fork). O pi-mcp envia o **prompt forçador** (validado hoje) e **assume que o workflow roda** — sem retry.

## 2. Arquitetura

```
+-------------+    MCP / stdio     +-----------------------+
| Claude Code | <----------------> |  pi-mcp (Go)          |
| (host)      |  pi_workflow       |  - MCP server (stdio, go-sdk oficial)
|             |  pi_status         |  - job registry (cap 4 + fila)
|             |  pi_list           |  - pi runner / parser / runstore
|             |  pi_cancel         |  - worktree manager (write mode)
+-------------+                    +-----------------------+
                                            | os/exec (1 processo/job, goroutine)
                                            |   stdin = /dev/null   (CRÍTICO)
                                            |   env   = os.Environ() (passthrough total)
                                            |   cwd   = projeto (read) | worktree (write)
                                            |   read  → --exclude-tools (só read/grep/glob/web)
                                            v
                              +------------------------------------+
                              | pi -p --mode json --no-session     |
                              |   <prompt forçador + contrato>     |
                              +------------------------------------+
                                            | modelo principal AUTORA o script (a "divisão")
                                            | tool `workflow` (background:false → runSync)
                                            v
                              +------------------------------------+
                              | engine dynamic-workflow (vm realm) |
                              | resolveModelRoute() por agente     |
                              +------------------------------------+
                                |        |        |        |
                          flash       pro      gpt-5.5   ...   (frota heterogênea, limiter 16-wide)
                                \________ síntese final ________/
                                            |
      stdout JSONL: orquestrador streama ao vivo | frota é silenciosa aqui
      run file <cwd>/.pi/workflows/runs/<runId>.json: agents[]/journal[] crescem AO VIVO (intermediários)
                                            |
                pi-mcp → pi_status: intermediários (journal, ao vivo) + final (contrato) + metadados → Claude
```

## 3. Validação empírica (2026-06-07)

Probe real forçando workflow `background:false` (3 afirmações para julgar):

| Fase | Agente | Modelo escolhido **pelo pi** |
|---|---|---|
| Scan | claim A/B/C | `deepseek/deepseek-v4-flash` (×3) |
| Final | final synthesis | `openai-codex/gpt-5.5` |

- ✅ **Multi-modelo ao vivo:** 1 tarefa → 4 agentes em 2 modelos, batendo as regras de tier. Run: `$0.146`, `22,4s`, `tokenUsage.total=120469`.
- ✅ **`background:false` retorna o resultado inline** (`{claims, overall}`).
- ✅ Runs caem em **`<cwd>/.pi/workflows/runs/`** (cwd-relativo). gpt‑5.5 autenticado.
- 🔴 **Gotcha crítico:** `pi -p` **trava para sempre** se o stdin não der EOF. O filho **DEVE** ter `stdin = /dev/null`.
- ✅ **Streaming real-time (provado por amostragem 1s):** o stdout streama incrementalmente o **raciocínio/autoria do orquestrador** (t≈5–24s, linhas crescendo token a token). **Mas a frota de subagentes é silenciosa no stdout pai** (só `tool_execution_start` → `tool_execution_end`; existe só 1 `agent_start/end`, o do orquestrador).
- ✅ **Run file atualiza AO VIVO durante o fan-out:** `agents[]` e `journal[]` crescem incrementalmente (done 0→1→7→9→11 numa amostra de 10 agentes). `journal[].result` = **resultado COMPLETO** por-agente (não truncado), casado por `index`=`callIndex`; `agents[].resultPreview` = preview truncado. → **resultados intermediários no meio do run são viáveis** (via run file, não stdout).
- 🔴 **Ressalvas:** janela cega ~20s antes do run file existir (autoria do script); `status` no disco oscila `running`↔`paused` (não-terminal); `cost`/`tokenUsage` só preenche no fim. Resultado final vem do `tool_execution_end(workflow)` / `.result`; **a mensagem final do assistente vem vazia**.

Fixtures: `docs/research/fixtures/{sample-pi-mode-json-events.jsonl, sample-run.json, sample-forced-workflow-prompt.txt}` (`sample-run.json` contém `journal[]` com resultados completos).

## 4. Contrato de invocação do `pi`

```
comando : pi -p --mode json --no-session [--exclude-tools <write,edit,...>] <PROMPT>
stdin   : /dev/null            # CRÍTICO
env     : os.Environ() inteiro # HOME, PATH, AGENT_VAULT_{ADDR,TOKEN,VAULT}, proxy/CA
cwd     : projeto (read) | worktree do job (write)
```

`<PROMPT>` = **wrapper forçador** (não prescreve fases — o pi decide a divisão) + **contrato de saída** (§5.4):
> "You MUST make exactly ONE call to the `workflow` tool with `background:false`. Do not answer directly; do not use `background:true`. Return the final synthesized result INLINE this turn. Decompose as you see fit and fan out subagents in parallel. Return an object matching: <contrato read|write>.
> \n\nTASK:\n<task>\n[CONTEXT:\n<context>]"

### 4.1 Modes & isolamento

| | **read** | **write** |
|---|---|---|
| cwd do pi | o projeto (lê arquivos) | **git worktree** novo do job |
| tools do pi | `--exclude-tools` → só read/grep/glob/web (zero mutação) | completas (sem limite) |
| git | não exige | **exige** (senão falha rápido) |
| pós-job | só resultado | branch+worktree mantidos; retorna branch/path/diff stat |

- `mode` é **obrigatório** em `pi_workflow` (sem default — sem ambiguidade).
- **write:** o servidor cria worktree `pi-mcp/job-<jobId>` a partir do HEAD do projeto, lança o pi com `cwd=worktree`. Ao terminar, **deixa** worktree+branch e retorna `{branch, worktree_path, diff_stat, files_changed}`; o diff completo eu leio via git sob demanda. Se o cwd não for repo git → erro `NOT_A_GIT_REPO`.
- **read:** runs em `<projeto>/.pi/workflows/runs`; nada muta o tree (tools só-leitura).

## 5. Superfície MCP (4 tools)

### 5.1 `pi_workflow` — delega uma tarefa (background no nível do MCP)
```jsonc
{
  "task": "string (obrigatório)",
  "mode": "read | write (OBRIGATÓRIO)",
  "cwd":  "string (recomendado: caminho do projeto; default = cwd do servidor)",
  "context": "string (opcional) — paths/contexto extra embutidos no prompt"
}
```
**Retorno (imediato):** `{ jobId, status:"queued"|"running", mode, cwd, worktree_path?, started_at }`. Não bloqueia.

### 5.2 `pi_status` — resultados intermediários (ao vivo) + final
```jsonc
{ "jobId": "string" }  // ou { "runId": "string", "cwd": "string" } p/ runs externos / pós-restart
{ "wait": "bool (opcional) — long-poll até ~60s ou mudança de estado/novo agente concluído" }
```
**Retorno:**
```jsonc
{
  "jobId":"...", "runId":"mq40rdpt-yij9hj", "status":"queued|running|completed|failed|aborted",
  "phase":"Scan",
  "intermediate": [   // agentes JÁ concluídos, gravados AO VIVO (cresce a cada poll durante o run)
    { "label":"claim A", "model":"deepseek/deepseek-v4-flash", "phase":"Scan",
      "result": { /* journal[].result COMPLETO, casado por index/callIndex */ },
      "preview": "{...}" /* resultPreview truncado (fallback p/ saídas grandes) */ }
  ],
  "result": { /* contrato §5.4 — o .result final, só quando completed */ },
  "metadata": { "by_model": {"deepseek/deepseek-v4-flash":10,"openai-codex/gpt-5.5":1},
                "agentCount":11, "tokenUsage":{"total":...,"cost":0.13}, "durationMs":... }, // cost/tokens só no fim
  "write": { "branch":"pi-mcp/job-...", "worktree_path":"...", "diff_stat":"...", "files_changed":[...] }, // só write
  "error": "string (failed/aborted; inclui MODEL_ROUTING_ERROR, NOT_A_GIT_REPO, NO_WORKFLOW_RUN)"
}
```
**Resultados intermediários + final** (capacidade que o usuário pediu): enquanto `running`, `intermediate[]` traz os agentes já concluídos com seu **resultado completo** (de `journal[].result`, casado por `index`=`callIndex` com `agents[]` p/ pegar label/model/phase; `preview` = `resultPreview` como fallback). Eu posso ler achados PARCIAIS no meio do run e agir antes do fim; o `result` final chega quando `completed`. Correlação `jobId→runId` por **`sessionId`** (`session.id` no stream = `run.sessionId`).

**Ressalvas (provadas ao vivo, baked no design):**
- **Janela cega ~20s:** o run file só existe depois que o orquestrador termina de escrever o script. Antes disso `status=queued/running` sem `intermediate` (o stdout do orquestrador streama, mas não expomos isso no MVP).
- **`status` no disco oscila `running`↔`paused`** durante o run → NÃO é sinal terminal. Terminal real = `completed`/`failed`/`aborted` + PID. Liveness = PID vivo + estado terminal.
- **`cost`/`tokenUsage` só no fim** (0 durante) → metadata de custo só quando `completed`.

### 5.3 `pi_list` — runs recentes
`{ "cwd":"string (opcional)", "limit":"number (default 20)" }` → lista `{ runId, workflowName, status, agentCount, by_model, cost, durationMs, completedAt }` ordenada por `updatedAt` desc; lê `<cwd>/.pi/workflows/runs/*.json` (exclui `*.bak`/`*.tmp`; fallback `.bak` se corrompido).

### 5.4 Contrato de saída default (instruído via prompt + coerção best-effort)
- **read:** `{ summary, findings: [{title, detail, severity?}], confidence, open_questions }`
- **write:** `{ summary, files_changed: [], diff_summary, tests_run?, notes }`
O retorno do workflow inteiro não é validado pelo engine; o pi-mcp coage (se faltar, embrulha em `{summary: <texto cru>}`).

### 5.5 `pi_cancel` — mata um job
`{ "jobId":"string" }` → mata o processo pi, marca `status=aborted`, lê o parcial do run file. Essencial (não há timeout).

## 6. Componentes (pacotes Go)

```
cmd/pi-mcp/main.go        # entrypoint; MCP server (go-sdk oficial, stdio); registra as 4 tools
internal/mcpserver/       # handlers + validação de input (mode obrigatório etc.)
internal/runner/          # dono do subprocess pi: args+prompt+exclude-tools, spawn (stdin=/dev/null,
                          #   env=os.Environ(), cwd), captura stdout, kill via context
internal/parser/          # stream JSONL: linha-a-linha, switch .type, extrai sessionId,
                          #   tool_execution_end(workflow).result, detecta NO_WORKFLOW_RUN
internal/runstore/        # lê <cwd>/.pi/workflows/runs/*.json → Run/Agent; histograma de modelos
internal/worktree/        # cria/abre git worktree p/ write; branch pi-mcp/job-<id>; diff stat
internal/jobs/            # registry map[jobID]*Job + mutex; cap 4 + fila; correlação por sessionId;
                          #   liveness por PID; sem timeout; cancel; fallback via runstore pós-restart
internal/config/          # paths (pi bin, cwd default), cap, exclude-tools list, wrapper forçador
internal/model/           # structs (Run, Agent, TokenUsage, JobStatus, eventos JSONL, contratos)
*_test.go                 # parser/runstore contra fixtures; runner com pi fake; jobs lifecycle
```

## 7. Estruturas de dados (validadas)

**Run file** `<cwd>/.pi/workflows/runs/<runId>.json`: `runId, sessionId, workflowName, status, currentPhase, phases, agents[], journal, logs, result, script, startedAt, completedAt, updatedAt, durationMs, tokenUsage`.
- `agents[]`: `{ callIndex, id, label, model, phase, prompt, resultPreview, startedAt, endedAt, status, tokens }` — `model` = prova multi-modelo; `resultPreview` = preview truncado.
- `journal[]`: `{ index, hash, result }` — `result` = **resultado COMPLETO** por-agente (fonte do conteúdo intermediário), `index`=`callIndex`, gravado ao vivo conforme cada agente conclui (ordem de conclusão).
- `tokenUsage`: `{ input, output, total, cost, cacheRead, cacheWrite }` (preenchido só no fim).
- `status`: `running|completed|failed|aborted|paused` (só `completed` grava `completedAt`/`durationMs` → ponteiros opcionais).

**Stream JSONL:** tipos `session, agent_start/end, turn_start/end, message_start/update/end, tool_execution_start/end`.
- `session.id` = `run.sessionId`.
- Resultado: `tool_execution_end` com `toolName=="workflow"` → `.result = { content:[{type:"text",text:<header+```json```>}], details }`. Preferir `details` / parsear o bloco ```json``` do `content[0].text`. **Nunca** extrair da mensagem final do assistente (vem vazia).
- Sem esse evento ⇒ `NO_WORKFLOW_RUN` (erro; não esperado, pois o trigger estará ativo — sem retry).
Parser: `bufio.Scanner` buffer ≥1MB (linhas carregam o script inteiro). Sucesso = exit 0 + stderr vazio.

## 8. Concorrência / jobs

- 1 job = 1 goroutine + 1 processo `pi`. Registry `map[jobID]*Job` sob mutex.
- **Cap 4** (configurável via env/flag) + fila; excedentes ficam `queued`.
- **Sem timeout**: jobs rodam até terminar. Controle via `pi_cancel`.
- **Liveness:** guarda PID; `running` no disco + PID morto = `failed/crashed` (o `pi -p` one-shot não re-instancia `recoverStaleRuns`).
- **jobId→runId:** captura `sessionId` do 1º evento `session`; casa com `run.sessionId` (concorrência-safe).
- **Persistência:** registry em memória; pós-restart, `pi_status(runId,cwd)`/`pi_list` recuperam do disco.
- **cwd:** eu passo o projeto; default = cwd do servidor. write → cwd = worktree do job.

## 9. Tratamento de erros

| Situação | Detecção | Resposta |
|---|---|---|
| pi trava (stdin) | prevenido: `stdin=/dev/null` | n/a |
| cwd não é git (write) | check no spawn | erro `NOT_A_GIT_REPO` (falha rápido) |
| Não rodou workflow | sem `tool_execution_end(workflow)` | `NO_WORKFLOW_RUN` (sem retry; trigger é assumido ativo) |
| Roteamento strict falha | `MODEL_ROUTING_ERROR` / `status=failed` | propaga; sugere conferir `model-tiers.json` |
| Crash do pi | exit≠0 / PID morto com `running` | `status=failed` + stderr |
| Run file corrompido | parse falha | fallback `.bak`; senão erro |
| Cancelado | `pi_cancel` | mata pi, `status=aborted`, parcial do run file |

## 10. Testes
- **parser:** unit contra `fixtures/sample-pi-mode-json-events.jsonl` (sessionId, result, NO_WORKFLOW_RUN, tipos desconhecidos).
- **runstore:** unit contra `fixtures/sample-run.json` (schema, histograma {flash:3, gpt-5.5:1}, campos opcionais).
- **worktree:** cria/lista/diff num repo de teste; erro em não-repo.
- **runner:** `pi` fake (emite fixture, respeita stdin=/dev/null, exclude-tools, cwd, kill).
- **jobs:** lifecycle, cap 4 + fila, correlação por sessionId, cancel, liveness, fallback pós-restart.
- **smoke (flag):** roda o `pi` real reproduzindo o probe; assere fan-out multi-modelo.

## 11. Fases (YAGNI)
- **Fase 0 (gate):** automatizar o smoke do probe validado (contrato de invocação ponta a ponta). ✅ Já confirmado nesta sessão: stdin=/dev/null, fan-out multi-modelo, run file ao vivo, `journal[].result` completo, runs em `<cwd>/.pi/workflows/runs`.
- **Fase 1 (MVP):** as 4 tools (`pi_workflow` mode-gated, `pi_status` com **intermediário+final** e long-poll lendo `journal[]`/`agents[]` ao vivo, `pi_list`, `pi_cancel`); parser/runstore/runner/worktree/jobs robustos; contrato default rico; stdin=/dev/null + env passthrough + cwd; read=read-only tools / write=worktree; erros; cap 4; liveness (PID+terminal); sem timeout.
- **Fase 2:** health-check de tiers (`pi --list-models` vs config); progresso rico (se o run file permitir); invocar workflows salvos por nome; persistência do registry em disco.

## 12. Decisões (grilling 2026-06-07)
1. Capacidade ilimitada; segurança via `mode`. 2. write → git worktree por job. 3. pós-job: deixa worktree+branch, retorna branch/path/diff. 4. não-git no write → falha rápido. 5. `mode` obrigatório (sem default). 6. read → tools só-leitura (read/grep/glob/web), roda no cwd. 7. contrato default rico (read/write). 8. `pi_status` expõe **resultados intermediários (journal, ao vivo) + final** (não só contadores), com ressalvas (janela cega ~20s, status flapa paused, custo só no fim). 9. cap 4 + fila. 10. sem timeout. 11. `pi_cancel` no MVP. 12. sem orçamento. 13. trigger headless garantido pelo usuário (fork) → prompt forçador + assume que roda, sem retry. 14. registry em memória + fallback via run files. 15. long-poll opt-in (`wait`). 16. nomes `pi_workflow/pi_status/pi_list/pi_cancel`, bin `pi-mcp`. 17. go-sdk oficial; cwd = projeto (eu passo).

**Riscos / dependências:**
- Trigger headless depende de mudança do usuário no fork (até lá, o prompt forçador validado cobre).
- Engine roda do **source TS** (fork WIP sem commit) — formato de run/stream pode mudar; observar.
- `running` observável / atualização incremental do run file: confirmar na Fase 0 (progresso é mínimo, então baixo impacto).

## 13. Instalação / registro no Claude Code
```
go build -o /usr/local/bin/pi-mcp ./cmd/pi-mcp
claude mcp add -s user pi-mcp -- /usr/local/bin/pi-mcp
```
O Claude Code herda `AGENT_VAULT_*`/`HOME` na sessão; o pi-mcp **repassa ao neto `pi`** (`cmd.Env = os.Environ()`). O pi-mcp não recebe nem guarda segredos.
