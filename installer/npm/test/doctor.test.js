import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, writeFile, chmod } from 'node:fs/promises';
import { join } from 'node:path';
import { Writable } from 'node:stream';
import { doctor } from '../src/doctor.js';
import { mergeSettings, modelTiersConfig } from '../src/lib/config.js';
import { writeJsonAtomic } from '../src/lib/fs.js';
import { tempHome, fakeExecutable } from './helpers.js';

const silent = new Writable({ write(_chunk, _enc, cb) { cb(); } });

test('doctor reports missing components without throwing', async () => {
  const { home, pathDir } = await tempHome();
  const result = await doctor({}, { env: { HOME: home, PATH: pathDir }, stdout: silent });
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

  const result = await doctor({}, { env: { HOME: home, PATH: pathDir }, stdout: silent });
  assert.equal(result.ok, true);
  assert.equal(result.checks.every((c) => c.ok), true);
});
