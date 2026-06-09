# npm Runtime Installer Design

**Date:** 2026-06-09  
**Package name:** `@ianfr13/pi-mcp-runtime`  
**Goal:** Give the team a repeatable npm-based bootstrap that recreates Ian's local `pi` + custom dynamic-workflows + `pi-mcp` setup, without distributing secrets or forcing a credential strategy.

## Scope

Build an npm package inside this repository that exposes a CLI:

```bash
npx @ianfr13/pi-mcp-runtime install
npx @ianfr13/pi-mcp-runtime doctor
npx @ianfr13/pi-mcp-runtime update
```

The installer prepares the runtime and configuration only. Each teammate is responsible for authenticating Pi afterward through their preferred method (`pi /login`, API keys, Agent Vault, or another local setup).

## Non-goals

- Do not copy or generate `~/.pi/agent/auth.json`.
- Do not require Agent Vault.
- Do not install model/provider secrets.
- Do not use `postinstall` for invasive setup.
- Do not overwrite a user's existing Pi config without backup and merge.
- Do not change `pi-mcp` runtime behavior in this feature; this is an install/bootstrap layer.

## Team-facing install flow

Recommended command:

```bash
npx @ianfr13/pi-mcp-runtime install
```

The command should:

1. Check prerequisites: Node/npm, git, Go, and optionally Claude Code CLI.
2. Install or verify the Pi core runtime:
   ```bash
   npm install -g --ignore-scripts @earendil-works/pi-coding-agent
   ```
3. Clone or update the custom workflow fork:
   - Source: `https://github.com/Ianfr13/pi-dynamic-workflows-custom.git`
   - Default ref: current known-good ref from Ian's environment, `953ecbbc71646bda36f326ad5b5aed9457c9b812`, unless overridden.
   - Destination: `~/.pi-mcp/runtime/pi-dynamic-workflows-custom` by default.
4. Install/build the workflow fork:
   ```bash
   npm install
   npm run build
   ```
5. Merge Pi configuration files:
   - `~/.pi/agent/settings.json`
   - `~/.pi/workflows/model-tiers.json`
6. Build/install `pi-mcp`:
   - Default install path: `~/.local/bin/pi-mcp`.
   - Avoid `sudo` by default.
   - Allow an explicit option/env override for a system path such as `/usr/local/bin/pi-mcp`.
7. Register `pi-mcp` in Claude Code if the `claude` CLI is present:
   ```bash
   claude mcp add -s user pi-mcp -- <installed-pi-mcp-path>
   ```
   If Claude Code is missing, print the command instead of failing.
8. Print next steps:
   - Ensure `~/.local/bin` is on `PATH` if needed.
   - Run `pi` and authenticate with `/login` or preferred credentials.
   - Run `npx @ianfr13/pi-mcp-runtime doctor`.

## Configuration behavior

### `settings.json`

The installer must merge with existing settings and ensure the custom workflow package path is present:

```json
{
  "packages": [
    "<home>/.pi-mcp/runtime/pi-dynamic-workflows-custom"
  ],
  "defaultModel": "gpt-5.5",
  "defaultThinkingLevel": "high",
  "enabledModels": [
    "deepseek/deepseek-v4-pro",
    "deepseek/deepseek-v4-flash",
    "openai-codex/gpt-5.5",
    "minimax/MiniMax-M3",
    "kimi-coding/kimi-for-coding"
  ],
  "defaultProvider": "openai-codex"
}
```

Rules:

- Preserve existing unrelated fields.
- Preserve existing packages and append the custom workflow path if absent.
- Remove the public `npm:@quintinshaw/pi-dynamic-workflows` entry only if doing so avoids duplicate workflow extension activation.
- Do not write secrets.
- Do not write `auth.json`.

### `model-tiers.json`

Write Ian's default dynamic-workflow routing profile:

```json
{
  "tiers": {
    "small": "deepseek/deepseek-v4-flash",
    "medium": "minimax/MiniMax-M3",
    "big": "openai-codex/gpt-5.5",
    "cheap": "deepseek/deepseek-v4-flash",
    "coder": "kimi-coding/kimi-for-coding",
    "judge": "openai-codex/gpt-5.5",
    "research": "minimax/MiniMax-M3"
  },
  "rules": [
    { "name": "coder agentType", "match": { "agentType": "coder" }, "tier": "coder" },
    { "name": "reviewer agentType", "match": { "agentType": "reviewer" }, "tier": "judge" },
    { "name": "scan phase", "match": { "phase": "Scan" }, "tier": "cheap" },
    { "name": "judgment labels", "match": { "label": "/judge|critic|review|synthesis|final/i" }, "tier": "judge" }
  ],
  "fallback": { "mode": "error" }
}
```

This file can be overwritten after backing up because it is the routing profile the installer exists to replicate. Future versions may add a `--preserve-model-tiers` option, but the first version should keep the behavior simple and explicit.

## CLI design

### `install`

Options:

- `--workflow-ref <ref>`: override the pinned workflow ref.
- `--runtime-dir <dir>`: override `~/.pi-mcp/runtime`.
- `--bin-dir <dir>`: override `~/.local/bin`.
- `--pi-mcp-source <dir|url>`: use a local checkout or alternate repo source for `pi-mcp`.
- `--skip-claude-register`: do not run `claude mcp add`.
- `--force`: allow replacing existing runtime checkout state after backup/confirmation.
- `--dry-run`: print planned actions without changing disk.

### `doctor`

Checks and reports:

- `pi` exists and `pi --version` or `pi --help` works.
- Custom workflow directory exists and has a built `dist/`.
- `~/.pi/agent/settings.json` contains the custom workflow package path.
- `~/.pi/workflows/model-tiers.json` parses and has required tiers.
- `pi-mcp` binary exists and is executable.
- Claude Code MCP registration exists if `claude` is available.
- Authentication is intentionally not validated, but the output should remind the user to run `pi /login` if models fail.

### `update`

Updates only the managed runtime pieces:

- Fetch/checkout the configured workflow ref.
- Rebuild the workflow package.
- Rebuild/reinstall `pi-mcp`.
- Re-merge configs idempotently.

`update` should not remove user auth or provider config.

## Safety and idempotency

Before changing any user config, create timestamped backups:

- `~/.pi/agent/settings.json.bak-<timestamp>`
- `~/.pi/workflows/model-tiers.json.bak-<timestamp>`

Other rules:

- Create directories with owner-only permissions where practical (`0700` for runtime/config dirs).
- Use atomic writes for JSON configs: write temp file, fsync where feasible, rename.
- Running `install` twice must not duplicate package entries or Claude MCP entries.
- Print every important path being modified.
- If a prerequisite is missing, fail with an actionable command.

## Repository layout

Add:

```txt
installer/npm/
  package.json
  README.md
  bin/pi-mcp-runtime.js
  src/cli.js
  src/install.js
  src/doctor.js
  src/update.js
  src/lib/config.js
  src/lib/exec.js
  src/lib/fs.js
  src/lib/git.js
  src/lib/paths.js
  test/*.test.js
```

The npm package should use plain Node.js ESM and avoid heavy dependencies unless needed.

## Testing plan

Unit tests should use temporary HOME/XDG directories and fake executables on PATH.

Coverage:

- JSON merge preserves existing settings and appends workflow path once.
- `model-tiers.json` output matches the expected routing profile.
- Backups are created before config mutation.
- `doctor` reports missing and healthy components correctly.
- `install --dry-run` performs no writes.
- Claude registration is skipped when `claude` is missing and does not fail install.
- Idempotency: running config merge twice produces the same file.

Manual smoke test on a clean-ish environment:

```bash
cd installer/npm
npm test
node bin/pi-mcp-runtime.js install --dry-run
node bin/pi-mcp-runtime.js doctor
```

After publishing or packing locally:

```bash
npm pack
npx ./ianfr13-pi-mcp-runtime-*.tgz install --dry-run
```

## Implementation decisions

- `pi-mcp` source defaults to `https://github.com/Ianfr13/pi-mcp.git`, cloned under the runtime directory and built from there. For local development, `--pi-mcp-source <dir>` can point at the current checkout instead.
- The workflow fork defaults to the known-good commit `953ecbbc71646bda36f326ad5b5aed9457c9b812`. A future release can change the default to a named tag, but the first team rollout should prioritize exact replication of Ian's current environment.
