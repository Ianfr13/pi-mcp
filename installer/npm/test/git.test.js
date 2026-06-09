import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdir, writeFile, readdir } from 'node:fs/promises';
import { join } from 'node:path';
import { ensureRepo } from '../src/lib/git.js';
import { tempHome, fakeExecutable } from './helpers.js';

test('ensureRepo pulls existing checkout when no ref is pinned', async () => {
  const { home, pathDir } = await tempHome();
  const repoDir = join(home, 'repo');
  await mkdir(join(repoDir, '.git'), { recursive: true });
  await fakeExecutable(pathDir, 'git');
  const actions = [];

  await ensureRepo({ repo: 'https://example.test/repo.git', dir: repoDir, env: { PATH: pathDir }, dryRun: true, actions });

  assert.equal(actions.some((a) => a.command === 'git fetch --all --tags'), true);
  assert.equal(actions.some((a) => a.command === 'git pull --ff-only'), true);
});

test('ensureRepo backs up non-git directory when force is set', async () => {
  const { home, pathDir } = await tempHome();
  const repoDir = join(home, 'repo');
  await mkdir(repoDir, { recursive: true });
  await writeFile(join(repoDir, 'file.txt'), 'keep me');
  await fakeExecutable(pathDir, 'git', 'cmd="$1"; shift || true\nif [ "$cmd" = "clone" ]; then /bin/mkdir -p "$2/.git"; exit 0; fi\nexit 0');

  await ensureRepo({ repo: 'https://example.test/repo.git', dir: repoDir, env: { PATH: pathDir }, force: true });

  const entries = await readdir(home);
  assert.equal(entries.some((name) => name.startsWith('repo.bak-')), true);
  assert.equal(entries.includes('repo'), true);
});
