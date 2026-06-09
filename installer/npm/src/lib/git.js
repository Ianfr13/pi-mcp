import { rename } from 'node:fs/promises';
import { exists, defaultTimestamp } from './fs.js';
import { run } from './exec.js';

export async function ensureRepo({ repo, dir, ref, env, dryRun, actions, force }) {
  if (!(await exists(`${dir}/.git`))) {
    if (await exists(dir)) {
      if (!force) throw new Error(`${dir} exists but is not a git checkout; pass --force or choose another --runtime-dir`);
      const backup = `${dir}.bak-${defaultTimestamp()}`;
      if (actions) actions.push({ name: 'backup-dir', path: `${dir} -> ${backup}` });
      if (!dryRun) await rename(dir, backup);
    }
    await run('git', ['clone', repo, dir], { env, dryRun, actions });
  } else {
    await run('git', ['fetch', '--all', '--tags'], { cwd: dir, env, dryRun, actions });
    if (!ref) await run('git', ['pull', '--ff-only'], { cwd: dir, env, dryRun, actions });
  }
  if (ref) await run('git', ['checkout', ref], { cwd: dir, env, dryRun, actions });
}
