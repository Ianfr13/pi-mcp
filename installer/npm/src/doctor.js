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
  checks.push(check('workflow-build', await exists(workflowDist), workflowDist));

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
      checks.push(check('claude-registration', false, `run: claude mcp add -s user pi-mcp -- ${paths.piMcpBin}`));
    }
  }

  const ok = checks.every((c) => c.ok);
  const out = deps.stdout || process.stdout;
  for (const c of checks) out.write(`${c.ok ? '✓' : '✗'} ${c.name}: ${c.detail}\n`);
  out.write('Auth is not checked. Run `pi` then `/login` if model calls fail.\n');
  return { ok, checks };
}
