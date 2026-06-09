import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { Writable } from 'node:stream';
import { install } from '../src/install.js';
import { tempHome, fakeExecutable } from './helpers.js';

const silent = new Writable({ write(_chunk, _enc, cb) { cb(); } });

test('install dry-run records actions and writes nothing', async () => {
  const { home, pathDir } = await tempHome();
  const actions = [];
  const result = await install({ dryRun: true }, { env: { HOME: home, PATH: pathDir }, actions, stdout: silent });
  assert.equal(result.ok, true);
  assert.equal(actions.some((a) => a.command?.includes('npm install -g --ignore-scripts @earendil-works/pi-coding-agent')), true);
  await assert.rejects(() => readFile(join(home, '.pi', 'agent', 'settings.json'), 'utf8'));
});

test('install merges configs and skips Claude registration when missing', async () => {
  const { home, pathDir } = await tempHome();
  await fakeExecutable(pathDir, 'npm');
  await fakeExecutable(pathDir, 'git', 'cmd="$1"; shift || true\nif [ "$cmd" = "clone" ]; then /bin/mkdir -p "$2/.git" "$2/dist"; exit 0; fi\nexit 0');
  await fakeExecutable(pathDir, 'go', 'while [ "$#" -gt 0 ]; do if [ "$1" = "-o" ]; then shift; dir=${1%/*}; /bin/mkdir -p "$dir"; echo bin > "$1"; /bin/chmod +x "$1"; exit 0; fi; shift; done');
  await fakeExecutable(pathDir, 'pi');

  const piMcpSource = join(home, 'source', 'pi-mcp');
  await mkdir(join(piMcpSource, 'cmd', 'pi-mcp'), { recursive: true });
  await writeFile(join(piMcpSource, 'go.mod'), 'module pi-mcp\n');

  const result = await install({ piMcpSource, skipClaudeRegister: true }, { env: { HOME: home, PATH: pathDir }, stdout: silent });
  assert.equal(result.ok, true);

  const settings = JSON.parse(await readFile(join(home, '.pi', 'agent', 'settings.json'), 'utf8'));
  assert.equal(settings.packages.includes(join(home, '.pi-mcp', 'runtime', 'pi-dynamic-workflows-custom')), true);

  const tiers = JSON.parse(await readFile(join(home, '.pi', 'workflows', 'model-tiers.json'), 'utf8'));
  assert.equal(tiers.tiers.coder, 'kimi-coding/kimi-for-coding');
});
