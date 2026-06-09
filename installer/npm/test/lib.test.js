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
