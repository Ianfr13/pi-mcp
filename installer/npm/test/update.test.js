import test from 'node:test';
import assert from 'node:assert/strict';
import { Writable } from 'node:stream';
import { update } from '../src/update.js';
import { tempHome, fakeExecutable } from './helpers.js';

const silent = new Writable({ write(_chunk, _enc, cb) { cb(); } });

test('update reuses install with force semantics', async () => {
  const { home, pathDir } = await tempHome();
  await fakeExecutable(pathDir, 'npm');
  await fakeExecutable(pathDir, 'git');
  await fakeExecutable(pathDir, 'go');
  const actions = [];
  const result = await update({ dryRun: true }, { env: { HOME: home, PATH: pathDir }, actions, stdout: silent });
  assert.equal(result.ok, true);
  assert.equal(result.mode, 'update');
  assert.equal(actions.some((a) => a.command?.includes('git clone')), true);
  assert.equal(actions.some((a) => a.command?.includes('npm run build')), true);
});
