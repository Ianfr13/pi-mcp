# @ianfr13/pi-mcp-runtime

Team bootstrapper for Ian's Pi + custom dynamic-workflows + pi-mcp runtime.

## Install

```bash
npx @ianfr13/pi-mcp-runtime install
```

The installer configures runtime files only. It does not install secrets and does not write `~/.pi/agent/auth.json`.

After install:

```bash
pi
# then run /login and choose your preferred provider/model auth
```

Then verify:

```bash
npx @ianfr13/pi-mcp-runtime doctor
```

## What it installs

- `@earendil-works/pi-coding-agent`
- `https://github.com/Ianfr13/pi-dynamic-workflows-custom.git`
- `https://github.com/Ianfr13/pi-mcp.git`
- Pi settings and workflow model tiers matching Ian's team setup
- Optional Claude Code MCP registration if `claude` is installed

## Options

```bash
pi-mcp-runtime install --workflow-ref <ref>
pi-mcp-runtime install --runtime-dir ~/.pi-mcp/runtime
pi-mcp-runtime install --bin-dir ~/.local/bin
pi-mcp-runtime install --pi-mcp-source /path/to/pi-mcp
pi-mcp-runtime install --skip-claude-register
pi-mcp-runtime install --dry-run
```

## Update

```bash
npx @ianfr13/pi-mcp-runtime update
```

## Safety

The installer backs up changed config files using `.bak-<timestamp>` suffixes. Each teammate must configure authentication independently with `/login`, environment variables, Agent Vault, or another local method.
