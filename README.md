# pi-mcp

`pi-mcp` is a Go MCP server that lets Claude Code delegate work to the `pi` CLI dynamic-workflow runtime.

Claude Code stays the orchestrator. `pi` decomposes the task, chooses models through its workflow routing config, fans work out across subagents, and writes live run state that `pi-mcp` exposes back through MCP tools.

## What this repo contains

- **MCP server:** `cmd/pi-mcp`
- **Dashboard:** `cmd/pi-dashboard`
- **Team runtime installer:** `installer/npm` (`@ianfr13/pi-mcp-runtime`)
- **Runtime docs/plans:** `docs/`
- **Core packages:** `internal/{app,config,jobs,mcpserver,runner,runstore,dashboard,...}`

## Quick start for the team

After the npm package is published:

```bash
npx @ianfr13/pi-mcp-runtime install
```

This installs/configures:

- `@earendil-works/pi-coding-agent`
- Ian's custom dynamic-workflow runtime: `Ianfr13/pi-dynamic-workflows-custom`
- `pi-mcp`
- Pi workflow model-tier config
- Optional Claude Code MCP registration, if `claude` is installed

It **does not** install secrets, copy auth files, or require Agent Vault. Each teammate authenticates however they prefer:

```bash
pi
# then run /login
```

Verify the install:

```bash
npx @ianfr13/pi-mcp-runtime doctor
```

## MCP tools

Once registered in Claude Code, this server exposes:

| Tool | Purpose |
| --- | --- |
| `pi_workflow` | Start a delegated Pi workflow for a task. |
| `pi_status` | Check job/run status, live progress, intermediate results, and final result. |
| `pi_list` | List recent Pi workflow runs for a cwd. |
| `pi_cancel` | Cancel a running delegated job. |

`pi_workflow` supports:

- `mode: "read"` — run in the requested cwd.
- `mode: "write"` — run in an isolated git worktree and return branch/diff information.

## Manual development install

If you are developing this repo directly:

```bash
go build -o /usr/local/bin/pi-mcp ./cmd/pi-mcp
claude mcp add -s user pi-mcp -- /usr/local/bin/pi-mcp
claude mcp list
```

For a user-local binary instead:

```bash
mkdir -p ~/.local/bin
go build -o ~/.local/bin/pi-mcp ./cmd/pi-mcp
claude mcp add -s user pi-mcp -- ~/.local/bin/pi-mcp
```

## Runtime installer development

The npm bootstrapper lives in `installer/npm`.

```bash
cd installer/npm
npm test
node bin/pi-mcp-runtime.js install --dry-run
node bin/pi-mcp-runtime.js doctor
npm pack --dry-run
```

The current installer pins the custom workflow runtime in `installer/npm/src/constants.js`.

## Dashboard

The dashboard is a read-only web UI for pi-mcp jobs and workflow runs.

```bash
go build -o /usr/local/bin/pi-dashboard ./cmd/pi-dashboard
```

See [`deploy/README.md`](deploy/README.md) for the systemd user service.

## Testing

Run Go checks:

```bash
go test ./...
go build ./...
```

Run installer checks:

```bash
cd installer/npm
npm test
```

Real end-to-end smoke test, using the actual Pi runtime:

```bash
PI_MCP_E2E=1 go test ./test/e2e/ -run TestE2ESmoke -v -timeout 6m
```

## Important runtime notes

- `pi-mcp` launches `pi -p --mode json --no-session`.
- `stdin` for the `pi` subprocess must be `/dev/null`; otherwise print mode can hang.
- The `pi` child receives the full environment (`os.Environ()`), because Pi/model/MCP credential resolution may depend on `HOME`, `PATH`, provider env vars, Agent Vault env vars, and proxy/CA env vars.
- `pi-mcp` itself should not read or store model/API secrets.
- Workflow run files are written under `<cwd>/.pi/workflows/runs/` and are the authoritative source for subagent progress.

## Repository status

The repo is public, but some runtime defaults are tailored to Ian's team setup, especially:

- custom workflow repo: `Ianfr13/pi-dynamic-workflows-custom`
- default workflow model tiers
- Claude Code MCP registration assumptions

If you are outside the team, review the installer config before running it.
