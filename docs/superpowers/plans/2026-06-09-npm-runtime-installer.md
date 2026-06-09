# npm Runtime Installer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `@ianfr13/pi-mcp-runtime`, an npm CLI that installs Ian's Pi runtime, custom dynamic-workflows fork, pi-mcp binary, and team-safe configs without secrets.

**Architecture:** Add a self-contained Node.js ESM package under `installer/npm`. The CLI delegates to focused modules: paths, exec/command runner, file/config helpers, git/build actions, installer orchestration, doctor checks, and update orchestration. Tests use Node's built-in test runner with temporary HOME/PATH fixtures and fake executables.

**Tech Stack:** Node.js 20+ ESM, npm package bin, built-in `node:test`, `assert`, `fs/promises`, `child_process`, existing Go build for `pi-mcp`.

---

## File Structure

- Create `installer/npm/package.json` — npm package metadata, bin mapping, test script.
- Create `installer/npm/README.md` — team-facing usage and publish/local-pack instructions.
- Create `installer/npm/bin/pi-mcp-runtime.js` — executable bin shim that calls the CLI.
- Create `installer/npm/src/cli.js` — command/option parsing and dispatch.
- Create `installer/npm/src/constants.js` — pinned URLs, refs, default config payloads.
- Create `installer/npm/src/lib/paths.js` — HOME expansion and runtime/config/bin path derivation.
- Create `installer/npm/src/lib/exec.js` — command existence checks and spawned command wrapper with dry-run support.
- Create `installer/npm/src/lib/fs.js` — JSON read/write, backup, atomic write, mkdir helpers.
- Create `installer/npm/src/lib/config.js` — settings merge and model tier generation.
- Create `installer/npm/src/lib/git.js` — clone/update/checkout helpers.
- Create `installer/npm/src/install.js` — installer orchestration.
- Create `installer/npm/src/doctor.js` — runtime health checks.
- Create `installer/npm/src/update.js` — managed runtime updater.
- Create `installer/npm/test/helpers.js` — temp HOME/PATH and fake executable helpers.
- Create `installer/npm/test/*.test.js` — unit tests per module.

---

### Task 1: Scaffold npm package and CLI dispatch

**Files:**
- Create: `installer/npm/package.json`
- Create: `installer/npm/bin/pi-mcp-runtime.js`
- Create: `installer/npm/src/cli.js`
- Create: `installer/npm/src/constants.js`
- Create: `installer/npm/test/cli.test.js`

- [ ] **Step 1: Write the failing CLI tests**

Create `installer/npm/test/cli.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { parseArgs } from '../src/cli.js';

const names = (actions) => actions.map((a) => a.name);

test('parseArgs defaults to help when no command is provided', () => {
  const parsed = parseArgs([]);
  assert.equal(parsed.command, 'help');
  assert.deepEqual(parsed.options, {});
});

test('parseArgs parses install flags', () => {
  const parsed = parseArgs([
    'install',
    '--workflow-ref', 'abc123',
    '--runtime-dir', '/tmp/runtime',
    '--bin-dir', '/tmp/bin',
    '--pi-mcp-source', '/repo/pi-mcp',
    '--skip-claude-register',
    '--force',
    '--dry-run',
  ]);
  assert.equal(parsed.command, 'install');
  assert.equal(parsed.options.workflowRef, 'abc123');
  assert.equal(parsed.options.runtimeDir, '/tmp/runtime');
  assert.equal(parsed.options.binDir, '/tmp/bin');
  assert.equal(parsed.options.piMcpSource, '/repo/pi-mcp');
  assert.equal(parsed.options.skipClaudeRegister, true);
  assert.equal(parsed.options.force, true);
  assert.equal(parsed.options.dryRun, true);
});

test('parseArgs rejects unknown flags', () => {
  assert.throws(() => parseArgs(['install', '--bogus']), /Unknown option: --bogus/);
});

test('parseArgs exposes command names used by docs', () => {
  assert.deepEqual(names([
    parseArgs(['install']),
    parseArgs(['doctor']),
    parseArgs(['update']),
  ]), ['install', 'doctor', 'update']);
});
```

- [ ] **Step 2: Add package scaffold**

Create `installer/npm/package.json`:

```json
{
  "name": "@ianfr13/pi-mcp-runtime",
  "version": "0.1.0",
  "description": "Team bootstrapper for Pi runtime, custom dynamic workflows, and pi-mcp.",
  "type": "module",
  "bin": {
    "pi-mcp-runtime": "./bin/pi-mcp-runtime.js"
  },
  "files": [
    "bin/",
    "src/",
    "README.md"
  ],
  "scripts": {
    "test": "node --test test/*.test.js"
  },
  "engines": {
    "node": ">=20"
  },
  "license": "UNLICENSED",
  "private": true
}
```

Create `installer/npm/bin/pi-mcp-runtime.js`:

```js
#!/usr/bin/env node
import { main } from '../src/cli.js';

main(process.argv.slice(2)).catch((err) => {
  console.error(err?.message || String(err));
  process.exitCode = 1;
});
```

Create `installer/npm/src/constants.js`:

```js
export const PI_CORE_PACKAGE = '@earendil-works/pi-coding-agent';
export const WORKFLOW_REPO = 'https://github.com/Ianfr13/pi-dynamic-workflows-custom.git';
export const WORKFLOW_REF = '953ecbbc71646bda36f326ad5b5aed9457c9b812';
export const PI_MCP_REPO = 'https://github.com/Ianfr13/pi-mcp.git';
export const WORKFLOW_DIR_NAME = 'pi-dynamic-workflows-custom';
export const PI_MCP_DIR_NAME = 'pi-mcp';

export const DEFAULT_ENABLED_MODELS = [
  'deepseek/deepseek-v4-pro',
  'deepseek/deepseek-v4-flash',
  'openai-codex/gpt-5.5',
  'minimax/MiniMax-M3',
  'kimi-coding/kimi-for-coding',
];

export const DEFAULT_MODEL_TIERS = {
  tiers: {
    small: 'deepseek/deepseek-v4-flash',
    medium: 'minimax/MiniMax-M3',
    big: 'openai-codex/gpt-5.5',
    cheap: 'deepseek/deepseek-v4-flash',
    coder: 'kimi-coding/kimi-for-coding',
    judge: 'openai-codex/gpt-5.5',
    research: 'minimax/MiniMax-M3',
  },
  rules: [
    { name: 'coder agentType', match: { agentType: 'coder' }, tier: 'coder' },
    { name: 'reviewer agentType', match: { agentType: 'reviewer' }, tier: 'judge' },
    { name: 'scan phase', match: { phase: 'Scan' }, tier: 'cheap' },
    { name: 'judgment labels', match: { label: '/judge|critic|review|synthesis|final/i' }, tier: 'judge' },
  ],
  fallback: { mode: 'error' },
};
```

Create `installer/npm/src/cli.js`:

```js
import { install } from './install.js';
import { doctor } from './doctor.js';
import { update } from './update.js';

const booleanFlags = new Map([
  ['--skip-claude-register', 'skipClaudeRegister'],
  ['--force', 'force'],
  ['--dry-run', 'dryRun'],
]);

const valueFlags = new Map([
  ['--workflow-ref', 'workflowRef'],
  ['--runtime-dir', 'runtimeDir'],
  ['--bin-dir', 'binDir'],
  ['--pi-mcp-source', 'piMcpSource'],
]);

export function parseArgs(argv) {
  const [command = 'help', ...rest] = argv;
  const options = {};
  for (let i = 0; i < rest.length; i += 1) {
    const arg = rest[i];
    if (booleanFlags.has(arg)) {
      options[booleanFlags.get(arg)] = true;
      continue;
    }
    if (valueFlags.has(arg)) {
      const value = rest[i + 1];
      if (!value || value.startsWith('--')) throw new Error(`Missing value for ${arg}`);
      options[valueFlags.get(arg)] = value;
      i += 1;
      continue;
    }
    throw new Error(`Unknown option: ${arg}`);
  }
  return { command, options };
}

export async function main(argv, deps = {}) {
  const { command, options } = parseArgs(argv);
  if (command === 'install') return install(options, deps);
  if (command === 'doctor') return doctor(options, deps);
  if (command === 'update') return update(options, deps);
  if (command === 'help') {
    const out = deps.stdout || process.stdout;
    out.write(`Usage: pi-mcp-runtime <install|doctor|update> [options]\n`);
    return { ok: true };
  }
  throw new Error(`Unknown command: ${command}`);
}
```

Create minimal stub modules so tests can import; later tasks replace behavior:

```js
// installer/npm/src/install.js
export async function install() { return { ok: true, command: 'install' }; }
```

```js
// installer/npm/src/doctor.js
export async function doctor() { return { ok: true, command: 'doctor' }; }
```

```js
// installer/npm/src/update.js
export async function update() { return { ok: true, command: 'update' }; }
```

- [ ] **Step 3: Run scaffold tests**

Run:

```bash
cd installer/npm && npm test
```

Expected: PASS for `cli.test.js`.

- [ ] **Step 4: Commit**

```bash
git add installer/npm/package.json installer/npm/bin/pi-mcp-runtime.js installer/npm/src installer/npm/test/cli.test.js
git commit -m "feat(installer): scaffold npm runtime CLI"
```

---

### Task 2: Add paths, exec, and filesystem primitives

**Files:**
- Create: `installer/npm/src/lib/paths.js`
- Create: `installer/npm/src/lib/exec.js`
- Create: `installer/npm/src/lib/fs.js`
- Create: `installer/npm/test/helpers.js`
- Create: `installer/npm/test/lib.test.js`

- [ ] **Step 1: Write failing helper tests**

Create `installer/npm/test/helpers.js`:

```js
import { mkdtemp, mkdir, writeFile, chmod } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

export async function tempHome() {
  const root = await mkdtemp(join(tmpdir(), 'pi-mcp-runtime-'));
  return { root, home: join(root, 'home'), pathDir: join(root, 'bin') };
}

export async function fakeExecutable(dir, name, body = 'exit 0') {
  await mkdir(dir, { recursive: true });
  const file = join(dir, name);
  await writeFile(file, `#!/usr/bin/env bash\nset -euo pipefail\n${body}\n`);
  await chmod(file, 0o755);
  return file;
}
```

Create `installer/npm/test/lib.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, readFile, stat, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { tempHome, fakeExecutable } from './helpers.js';
import { resolvePaths } from '../src/lib/paths.js';
import { commandExists, run } from '../src/lib/exec.js';
import { readJson, writeJsonWithBackup } from '../src/lib/fs.js';

test('resolvePaths derives team runtime paths from HOME', async () => {
  const { home } = await tempHome();
  const paths = resolvePaths({ env: { HOME: home } });
  assert.equal(paths.runtimeDir, join(home, '.pi-mcp', 'runtime'));
  assert.equal(paths.workflowDir, join(home, '.pi-mcp', 'runtime', 'pi-dynamic-workflows-custom'));
  assert.equal(paths.piMcpRepoDir, join(home, '.pi-mcp', 'runtime', 'pi-mcp'));
  assert.equal(paths.binDir, join(home, '.local', 'bin'));
  assert.equal(paths.piMcpBin, join(home, '.local', 'bin', 'pi-mcp'));
  assert.equal(paths.settingsPath, join(home, '.pi', 'agent', 'settings.json'));
  assert.equal(paths.modelTiersPath, join(home, '.pi', 'workflows', 'model-tiers.json'));
});

test('commandExists finds fake executable on PATH', async () => {
  const { pathDir } = await tempHome();
  await fakeExecutable(pathDir, 'pi');
  assert.equal(await commandExists('pi', { env: { PATH: pathDir } }), true);
  assert.equal(await commandExists('missing', { env: { PATH: pathDir } }), false);
});

test('run dry-run records command without spawning', async () => {
  const actions = [];
  const result = await run('missing-command', ['--flag'], { dryRun: true, actions });
  assert.equal(result.status, 0);
  assert.deepEqual(actions, [{ name: 'run', command: 'missing-command --flag' }]);
});

test('writeJsonWithBackup creates backup and atomic JSON output', async () => {
  const { root } = await tempHome();
  const file = join(root, 'settings.json');
  await mkdir(root, { recursive: true });
  await writeFile(file, JSON.stringify({ old: true }, null, 2));
  const backup = await writeJsonWithBackup(file, { next: true }, { timestamp: '20260609T000000Z' });
  assert.equal(backup, `${file}.bak-20260609T000000Z`);
  assert.deepEqual(JSON.parse(await readFile(backup, 'utf8')), { old: true });
  assert.deepEqual(await readJson(file, {}), { next: true });
  assert.equal((await stat(file)).mode & 0o777, 0o600);
});
```

- [ ] **Step 2: Implement paths helper**

Create `installer/npm/src/lib/paths.js`:

```js
import { join, resolve } from 'node:path';
import { homedir } from 'node:os';
import { PI_MCP_DIR_NAME, WORKFLOW_DIR_NAME } from '../constants.js';

function homeFrom(env = process.env) {
  return env.HOME || homedir();
}

export function expandHome(value, env = process.env) {
  if (!value) return value;
  if (value === '~') return homeFrom(env);
  if (value.startsWith('~/')) return join(homeFrom(env), value.slice(2));
  return resolve(value);
}

export function resolvePaths(options = {}) {
  const env = options.env || process.env;
  const home = homeFrom(env);
  const runtimeDir = expandHome(options.runtimeDir || '~/.pi-mcp/runtime', env);
  const binDir = expandHome(options.binDir || '~/.local/bin', env);
  return {
    home,
    runtimeDir,
    workflowDir: join(runtimeDir, WORKFLOW_DIR_NAME),
    piMcpRepoDir: join(runtimeDir, PI_MCP_DIR_NAME),
    binDir,
    piMcpBin: join(binDir, 'pi-mcp'),
    settingsPath: join(home, '.pi', 'agent', 'settings.json'),
    modelTiersPath: join(home, '.pi', 'workflows', 'model-tiers.json'),
  };
}
```

- [ ] **Step 3: Implement exec helper**

Create `installer/npm/src/lib/exec.js`:

```js
import { spawn } from 'node:child_process';

export async function commandExists(command, options = {}) {
  const checker = process.platform === 'win32' ? 'where' : 'command';
  const args = process.platform === 'win32' ? [command] : ['-v', command];
  const result = await run(checker, args, { ...options, shell: process.platform !== 'win32', quiet: true });
  return result.status === 0;
}

export async function run(command, args = [], options = {}) {
  const commandText = [command, ...args].join(' ');
  if (options.actions) options.actions.push({ name: 'run', command: commandText });
  if (options.dryRun) return { status: 0, stdout: '', stderr: '' };

  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: options.cwd,
      env: options.env || process.env,
      stdio: options.quiet ? ['ignore', 'pipe', 'pipe'] : ['ignore', 'inherit', 'pipe'],
      shell: options.shell || false,
    });
    let stdout = '';
    let stderr = '';
    if (child.stdout) child.stdout.on('data', (d) => { stdout += d.toString(); });
    if (child.stderr) child.stderr.on('data', (d) => { stderr += d.toString(); });
    child.on('error', reject);
    child.on('close', (status) => {
      if (status === 0) resolve({ status, stdout, stderr });
      else reject(new Error(`${commandText} failed with exit ${status}${stderr ? `: ${stderr.trim()}` : ''}`));
    });
  });
}
```

- [ ] **Step 4: Implement filesystem helper**

Create `installer/npm/src/lib/fs.js`:

```js
import { constants as fsConstants } from 'node:fs';
import { access, copyFile, mkdir, open, readFile, rename, writeFile } from 'node:fs/promises';
import { dirname } from 'node:path';

export async function exists(path) {
  try {
    await access(path, fsConstants.F_OK);
    return true;
  } catch {
    return false;
  }
}

export async function ensureDir(path, mode = 0o700) {
  await mkdir(path, { recursive: true, mode });
}

export async function readJson(path, fallback = undefined) {
  if (!(await exists(path))) return fallback;
  return JSON.parse(await readFile(path, 'utf8'));
}

function defaultTimestamp() {
  return new Date().toISOString().replace(/[-:.]/g, '').replace('T', 'T').slice(0, 15) + 'Z';
}

export async function writeJsonAtomic(path, value, options = {}) {
  await ensureDir(dirname(path));
  const tmp = `${path}.tmp-${process.pid}`;
  const data = `${JSON.stringify(value, null, 2)}\n`;
  await writeFile(tmp, data, { mode: options.mode || 0o600 });
  const handle = await open(tmp, 'r');
  await handle.sync();
  await handle.close();
  await rename(tmp, path);
}

export async function writeJsonWithBackup(path, value, options = {}) {
  await ensureDir(dirname(path));
  let backup;
  if (await exists(path)) {
    const timestamp = options.timestamp || defaultTimestamp();
    backup = `${path}.bak-${timestamp}`;
    await copyFile(path, backup);
  }
  await writeJsonAtomic(path, value, options);
  return backup;
}
```

- [ ] **Step 5: Run tests**

Run:

```bash
cd installer/npm && npm test
```

Expected: PASS for CLI and lib tests.

- [ ] **Step 6: Commit**

```bash
git add installer/npm/src/lib installer/npm/test/helpers.js installer/npm/test/lib.test.js
git commit -m "feat(installer): add runtime path and IO helpers"
```

---

### Task 3: Implement config merge and model tier writing

**Files:**
- Create: `installer/npm/src/lib/config.js`
- Create: `installer/npm/test/config.test.js`

- [ ] **Step 1: Write failing config tests**

Create `installer/npm/test/config.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { mergeSettings, modelTiersConfig } from '../src/lib/config.js';
import { DEFAULT_MODEL_TIERS } from '../src/constants.js';

const workflowPath = '/home/user/.pi-mcp/runtime/pi-dynamic-workflows-custom';

test('mergeSettings preserves unrelated fields and appends workflow path once', () => {
  const existing = {
    theme: 'dark',
    packages: ['npm:pi-web-access', 'npm:@quintinshaw/pi-dynamic-workflows'],
  };
  const next = mergeSettings(existing, workflowPath);
  assert.equal(next.theme, 'dark');
  assert.deepEqual(next.packages, ['npm:pi-web-access', workflowPath]);
  assert.equal(next.defaultModel, 'gpt-5.5');
  assert.equal(next.defaultThinkingLevel, 'high');
  assert.equal(next.defaultProvider, 'openai-codex');
  assert.deepEqual(next.enabledModels, [
    'deepseek/deepseek-v4-pro',
    'deepseek/deepseek-v4-flash',
    'openai-codex/gpt-5.5',
    'minimax/MiniMax-M3',
    'kimi-coding/kimi-for-coding',
  ]);
});

test('mergeSettings is idempotent', () => {
  const once = mergeSettings({ packages: [workflowPath] }, workflowPath);
  const twice = mergeSettings(once, workflowPath);
  assert.deepEqual(twice.packages.filter((p) => p === workflowPath), [workflowPath]);
  assert.deepEqual(twice, once);
});

test('modelTiersConfig returns exact Ian routing profile clone', () => {
  assert.deepEqual(modelTiersConfig(), DEFAULT_MODEL_TIERS);
  const one = modelTiersConfig();
  one.tiers.small = 'changed';
  assert.equal(modelTiersConfig().tiers.small, 'deepseek/deepseek-v4-flash');
});
```

- [ ] **Step 2: Implement config helper**

Create `installer/npm/src/lib/config.js`:

```js
import { DEFAULT_ENABLED_MODELS, DEFAULT_MODEL_TIERS } from '../constants.js';

export function mergeSettings(existing = {}, workflowPath) {
  const packages = Array.isArray(existing.packages) ? [...existing.packages] : [];
  const filtered = packages.filter((p) => p !== 'npm:@quintinshaw/pi-dynamic-workflows');
  if (!filtered.includes(workflowPath)) filtered.push(workflowPath);

  return {
    ...existing,
    packages: filtered,
    defaultModel: 'gpt-5.5',
    defaultThinkingLevel: 'high',
    enabledModels: [...DEFAULT_ENABLED_MODELS],
    defaultProvider: 'openai-codex',
  };
}

export function modelTiersConfig() {
  return JSON.parse(JSON.stringify(DEFAULT_MODEL_TIERS));
}
```

- [ ] **Step 3: Run config tests**

Run:

```bash
cd installer/npm && npm test
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add installer/npm/src/lib/config.js installer/npm/test/config.test.js
git commit -m "feat(installer): merge Pi workflow configuration"
```

---

### Task 4: Implement doctor command

**Files:**
- Modify: `installer/npm/src/doctor.js`
- Create: `installer/npm/test/doctor.test.js`

- [ ] **Step 1: Write failing doctor tests**

Create `installer/npm/test/doctor.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, writeFile, chmod } from 'node:fs/promises';
import { join } from 'node:path';
import { doctor } from '../src/doctor.js';
import { mergeSettings, modelTiersConfig } from '../src/lib/config.js';
import { writeJsonAtomic } from '../src/lib/fs.js';
import { tempHome, fakeExecutable } from './helpers.js';

test('doctor reports missing components without throwing', async () => {
  const { home, pathDir } = await tempHome();
  const result = await doctor({}, { env: { HOME: home, PATH: pathDir } });
  assert.equal(result.ok, false);
  assert.equal(result.checks.find((c) => c.name === 'pi').ok, false);
  assert.equal(result.checks.find((c) => c.name === 'workflow-build').ok, false);
  assert.equal(result.checks.find((c) => c.name === 'pi-mcp-binary').ok, false);
});

test('doctor reports healthy configured runtime', async () => {
  const { home, pathDir } = await tempHome();
  await fakeExecutable(pathDir, 'pi', 'echo pi 0.79.0');
  await fakeExecutable(pathDir, 'claude', 'if [ "$1" = "mcp" ] && [ "$2" = "get" ]; then exit 0; fi; exit 1');

  const workflowDir = join(home, '.pi-mcp', 'runtime', 'pi-dynamic-workflows-custom');
  await mkdir(join(workflowDir, 'dist'), { recursive: true });
  const piMcpBin = join(home, '.local', 'bin', 'pi-mcp');
  await mkdir(join(home, '.local', 'bin'), { recursive: true });
  await writeFile(piMcpBin, '#!/usr/bin/env bash\nexit 0\n');
  await chmod(piMcpBin, 0o755);

  await writeJsonAtomic(join(home, '.pi', 'agent', 'settings.json'), mergeSettings({}, workflowDir));
  await writeJsonAtomic(join(home, '.pi', 'workflows', 'model-tiers.json'), modelTiersConfig());

  const result = await doctor({}, { env: { HOME: home, PATH: pathDir } });
  assert.equal(result.ok, true);
  assert.equal(result.checks.every((c) => c.ok), true);
});
```

- [ ] **Step 2: Implement doctor**

Replace `installer/npm/src/doctor.js` with:

```js
import { access } from 'node:fs/promises';
import { constants as fsConstants } from 'node:fs';
import { resolvePaths } from './lib/paths.js';
import { commandExists, run } from './lib/exec.js';
import { readJson, exists } from './lib/fs.js';

async function executable(path) {
  try {
    await access(path, fsConstants.X_OK);
    return true;
  } catch {
    return false;
  }
}

function check(name, ok, detail) {
  return { name, ok, detail };
}

export async function doctor(options = {}, deps = {}) {
  const env = deps.env || process.env;
  const paths = resolvePaths({ ...options, env });
  const checks = [];

  const hasPi = await commandExists('pi', { env });
  checks.push(check('pi', hasPi, hasPi ? 'pi found on PATH' : 'pi missing; run install'));

  const workflowDist = `${paths.workflowDir}/dist`;
  checks.push(check('workflow-build', await exists(workflowDist), `${workflowDist}`));

  const settings = await readJson(paths.settingsPath, {});
  const packages = Array.isArray(settings.packages) ? settings.packages : [];
  checks.push(check('settings-workflow-package', packages.includes(paths.workflowDir), paths.settingsPath));

  const tiers = await readJson(paths.modelTiersPath, null);
  checks.push(check('model-tiers', Boolean(tiers?.tiers?.small && tiers?.fallback?.mode), paths.modelTiersPath));

  checks.push(check('pi-mcp-binary', await executable(paths.piMcpBin), paths.piMcpBin));

  const hasClaude = await commandExists('claude', { env });
  if (hasClaude) {
    try {
      await run('claude', ['mcp', 'get', 'pi-mcp'], { env, quiet: true });
      checks.push(check('claude-registration', true, 'pi-mcp registered'));
    } catch {
      checks.push(check('claude-registration', false, 'run: claude mcp add -s user pi-mcp -- ' + paths.piMcpBin));
    }
  }

  const ok = checks.every((c) => c.ok);
  const out = deps.stdout || process.stdout;
  for (const c of checks) out.write(`${c.ok ? '✓' : '✗'} ${c.name}: ${c.detail}\n`);
  out.write('Auth is not checked. Run `pi` then `/login` if model calls fail.\n');
  return { ok, checks };
}
```

- [ ] **Step 3: Run doctor tests**

Run:

```bash
cd installer/npm && npm test
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add installer/npm/src/doctor.js installer/npm/test/doctor.test.js
git commit -m "feat(installer): add runtime doctor checks"
```

---

### Task 5: Implement git/build helpers and install orchestration

**Files:**
- Create: `installer/npm/src/lib/git.js`
- Modify: `installer/npm/src/install.js`
- Create: `installer/npm/test/install.test.js`

- [ ] **Step 1: Write failing install orchestration tests**

Create `installer/npm/test/install.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { install } from '../src/install.js';
import { tempHome, fakeExecutable } from './helpers.js';

test('install dry-run records actions and writes nothing', async () => {
  const { home, pathDir } = await tempHome();
  const actions = [];
  const result = await install({ dryRun: true }, { env: { HOME: home, PATH: pathDir }, actions });
  assert.equal(result.ok, true);
  assert.equal(actions.some((a) => a.command?.includes('npm install -g --ignore-scripts @earendil-works/pi-coding-agent')), true);
  assert.rejects(() => readFile(join(home, '.pi', 'agent', 'settings.json'), 'utf8'));
});

test('install merges configs and skips Claude registration when missing', async () => {
  const { home, pathDir } = await tempHome();
  await fakeExecutable(pathDir, 'npm');
  await fakeExecutable(pathDir, 'git');
  await fakeExecutable(pathDir, 'go', 'mkdir -p "$4" 2>/dev/null || true\n# go build -o path ./cmd/pi-mcp\nwhile [ "$#" -gt 0 ]; do if [ "$1" = "-o" ]; then shift; echo bin > "$1"; chmod +x "$1"; exit 0; fi; shift; done');
  await fakeExecutable(pathDir, 'pi');

  const piMcpSource = join(home, 'source', 'pi-mcp');
  await mkdir(join(piMcpSource, 'cmd', 'pi-mcp'), { recursive: true });
  await writeFile(join(piMcpSource, 'go.mod'), 'module pi-mcp\n');

  const result = await install({ piMcpSource, skipClaudeRegister: true }, { env: { HOME: home, PATH: pathDir } });
  assert.equal(result.ok, true);

  const settings = JSON.parse(await readFile(join(home, '.pi', 'agent', 'settings.json'), 'utf8'));
  assert.equal(settings.packages.includes(join(home, '.pi-mcp', 'runtime', 'pi-dynamic-workflows-custom')), true);

  const tiers = JSON.parse(await readFile(join(home, '.pi', 'workflows', 'model-tiers.json'), 'utf8'));
  assert.equal(tiers.tiers.coder, 'kimi-coding/kimi-for-coding');
});
```

- [ ] **Step 2: Implement git helper**

Create `installer/npm/src/lib/git.js`:

```js
import { exists } from './fs.js';
import { run } from './exec.js';

export async function ensureRepo({ repo, dir, ref, env, dryRun, actions, force }) {
  if (!(await exists(`${dir}/.git`))) {
    if (await exists(dir) && !force) throw new Error(`${dir} exists but is not a git checkout; pass --force or choose another --runtime-dir`);
    await run('git', ['clone', repo, dir], { env, dryRun, actions });
  } else {
    await run('git', ['fetch', '--all', '--tags'], { cwd: dir, env, dryRun, actions });
  }
  if (ref) await run('git', ['checkout', ref], { cwd: dir, env, dryRun, actions });
}
```

- [ ] **Step 3: Implement installer**

Replace `installer/npm/src/install.js` with:

```js
import { chmod } from 'node:fs/promises';
import { resolvePaths } from './lib/paths.js';
import { commandExists, run } from './lib/exec.js';
import { ensureDir, readJson, writeJsonWithBackup, exists } from './lib/fs.js';
import { mergeSettings, modelTiersConfig } from './lib/config.js';
import { ensureRepo } from './lib/git.js';
import { PI_CORE_PACKAGE, PI_MCP_REPO, WORKFLOW_REF, WORKFLOW_REPO } from './constants.js';

async function requireCommand(name, env) {
  if (!(await commandExists(name, { env }))) throw new Error(`Missing required command: ${name}`);
}

async function installPiCore(options, env, actions) {
  await run('npm', ['install', '-g', '--ignore-scripts', PI_CORE_PACKAGE], { env, dryRun: options.dryRun, actions });
}

async function buildWorkflow(paths, options, env, actions) {
  await ensureRepo({ repo: WORKFLOW_REPO, dir: paths.workflowDir, ref: options.workflowRef || WORKFLOW_REF, env, dryRun: options.dryRun, actions, force: options.force });
  await run('npm', ['install'], { cwd: paths.workflowDir, env, dryRun: options.dryRun, actions });
  await run('npm', ['run', 'build'], { cwd: paths.workflowDir, env, dryRun: options.dryRun, actions });
}

async function buildPiMcp(paths, options, env, actions) {
  let source = options.piMcpSource;
  if (!source) {
    source = paths.piMcpRepoDir;
    await ensureRepo({ repo: PI_MCP_REPO, dir: source, env, dryRun: options.dryRun, actions, force: options.force });
  }
  await ensureDir(paths.binDir);
  await run('go', ['build', '-o', paths.piMcpBin, './cmd/pi-mcp'], { cwd: source, env, dryRun: options.dryRun, actions });
  if (!options.dryRun && await exists(paths.piMcpBin)) await chmod(paths.piMcpBin, 0o755);
}

async function writeConfigs(paths, options) {
  if (options.dryRun) return;
  const settings = await readJson(paths.settingsPath, {});
  await writeJsonWithBackup(paths.settingsPath, mergeSettings(settings, paths.workflowDir));
  await writeJsonWithBackup(paths.modelTiersPath, modelTiersConfig());
}

async function registerClaude(paths, options, env, actions) {
  if (options.skipClaudeRegister) return;
  if (!(await commandExists('claude', { env }))) {
    const out = options.stdout || process.stdout;
    out.write(`Claude CLI not found. To register later: claude mcp add -s user pi-mcp -- ${paths.piMcpBin}\n`);
    return;
  }
  await run('claude', ['mcp', 'add', '-s', 'user', 'pi-mcp', '--', paths.piMcpBin], { env, dryRun: options.dryRun, actions });
}

export async function install(options = {}, deps = {}) {
  const env = deps.env || process.env;
  const actions = deps.actions || [];
  const paths = resolvePaths({ ...options, env });
  options.stdout = deps.stdout;

  await requireCommand('npm', env);
  await requireCommand('git', env);
  await requireCommand('go', env);

  await ensureDir(paths.runtimeDir);
  await installPiCore(options, env, actions);
  await buildWorkflow(paths, options, env, actions);
  await writeConfigs(paths, options);
  await buildPiMcp(paths, options, env, actions);
  await registerClaude(paths, options, env, actions);

  const out = deps.stdout || process.stdout;
  out.write(`Installed pi-mcp runtime. Next: run \`pi\`, then \`/login\`, then \`npx @ianfr13/pi-mcp-runtime doctor\`.\n`);
  return { ok: true, paths, actions };
}
```

- [ ] **Step 4: Run installer tests**

Run:

```bash
cd installer/npm && npm test
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add installer/npm/src/lib/git.js installer/npm/src/install.js installer/npm/test/install.test.js
git commit -m "feat(installer): install Pi custom runtime"
```

---

### Task 6: Implement update command

**Files:**
- Modify: `installer/npm/src/update.js`
- Create: `installer/npm/test/update.test.js`

- [ ] **Step 1: Write failing update test**

Create `installer/npm/test/update.test.js`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { update } from '../src/update.js';
import { tempHome, fakeExecutable } from './helpers.js';

test('update reuses install with force semantics and no Claude registration by default override', async () => {
  const { home, pathDir } = await tempHome();
  await fakeExecutable(pathDir, 'npm');
  await fakeExecutable(pathDir, 'git');
  await fakeExecutable(pathDir, 'go');
  const actions = [];
  const result = await update({ dryRun: true }, { env: { HOME: home, PATH: pathDir }, actions });
  assert.equal(result.ok, true);
  assert.equal(result.mode, 'update');
  assert.equal(actions.some((a) => a.command?.includes('git clone')), true);
  assert.equal(actions.some((a) => a.command?.includes('npm run build')), true);
});
```

- [ ] **Step 2: Implement update**

Replace `installer/npm/src/update.js` with:

```js
import { install } from './install.js';

export async function update(options = {}, deps = {}) {
  const result = await install({ ...options, force: true }, deps);
  return { ...result, mode: 'update' };
}
```

- [ ] **Step 3: Run tests**

Run:

```bash
cd installer/npm && npm test
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add installer/npm/src/update.js installer/npm/test/update.test.js
git commit -m "feat(installer): add managed runtime update command"
```

---

### Task 7: Add README and package polish

**Files:**
- Create: `installer/npm/README.md`
- Modify: `installer/npm/package.json`

- [ ] **Step 1: Write package README**

Create `installer/npm/README.md`:

```md
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
```

- [ ] **Step 2: Add publish metadata but keep private until ready**

Modify `installer/npm/package.json` to include repository metadata while keeping `private: true` until Ian chooses to publish:

```json
{
  "repository": {
    "type": "git",
    "url": "git+https://github.com/Ianfr13/pi-mcp.git",
    "directory": "installer/npm"
  },
  "keywords": [
    "pi",
    "pi-mcp",
    "dynamic-workflows",
    "bootstrap"
  ]
}
```

Merge these fields with the existing JSON; do not remove `bin`, `files`, `scripts`, or `engines`.

- [ ] **Step 3: Run npm tests and package dry pack**

Run:

```bash
cd installer/npm && npm test && npm pack --dry-run
```

Expected: tests PASS; dry-run lists `bin/`, `src/`, `README.md`, `package.json`.

- [ ] **Step 4: Commit**

```bash
git add installer/npm/README.md installer/npm/package.json
git commit -m "docs(installer): document npm runtime bootstrapper"
```

---

### Task 8: Final verification and integration docs

**Files:**
- Modify: `HANDOFF.md`
- Modify: `deploy/README.md` if the installer changes dashboard install guidance
- Existing tests only

- [ ] **Step 1: Update handoff with installer commands**

Add this section near the build/install section in `HANDOFF.md`:

```md
## Team runtime npm installer

The repo now includes an npm bootstrapper at `installer/npm`:

```bash
cd installer/npm
npm test
node bin/pi-mcp-runtime.js install --dry-run
node bin/pi-mcp-runtime.js doctor
```

Target package name: `@ianfr13/pi-mcp-runtime`.
Team install command after publishing:

```bash
npx @ianfr13/pi-mcp-runtime install
```

The installer configures Pi + the custom dynamic-workflows fork + pi-mcp, but intentionally does not configure secrets. Each teammate runs `pi` and `/login` or uses their preferred credential setup.
```

- [ ] **Step 2: Run all verification commands**

Run:

```bash
cd installer/npm && npm test
cd ../..
go test ./...
go build ./...
```

Expected: all PASS.

- [ ] **Step 3: Run local dry-run smoke**

Run:

```bash
cd installer/npm
node bin/pi-mcp-runtime.js install --dry-run
```

Expected: prints planned command actions and next steps; does not change `~/.pi/agent/settings.json`.

- [ ] **Step 4: Check worktree state**

Run:

```bash
git status --short
```

Expected: only intended docs/package files modified.

- [ ] **Step 5: Commit final docs**

```bash
git add HANDOFF.md deploy/README.md installer/npm
git commit -m "docs(installer): add team runtime bootstrap instructions"
```

---

## Self-Review

**Spec coverage:** This plan covers the npm package, CLI commands, Pi runtime install, custom workflow clone/build, config merge, model tiers, pi-mcp build/install, optional Claude Code registration, doctor/update commands, backups, idempotency, dry-run behavior, and tests. Secrets/Auth/Vault are explicitly excluded.

**Red-flag scan:** No incomplete instructions remain; all tasks include exact files, commands, and code snippets.

**Type consistency:** The main options object uses `workflowRef`, `runtimeDir`, `binDir`, `piMcpSource`, `skipClaudeRegister`, `force`, and `dryRun` consistently across parser, paths, install, doctor, and update.
