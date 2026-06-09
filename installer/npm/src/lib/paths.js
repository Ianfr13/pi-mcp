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
