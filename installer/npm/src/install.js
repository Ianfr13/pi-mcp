import { chmod, cp } from 'node:fs/promises';
import { resolvePaths } from './lib/paths.js';
import { commandExists, run } from './lib/exec.js';
import { ensureDir, readJson, writeJsonWithBackup, exists } from './lib/fs.js';
import { mergeSettings, modelTiersConfig } from './lib/config.js';
import { ensureRepo } from './lib/git.js';
import { PI_CORE_PACKAGE, PI_MCP_REPO, WORKFLOW_REF, WORKFLOW_REPO } from './constants.js';

async function requireCommand(name, env, dryRun = false) {
  if (dryRun) return;
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

async function preparePiMcpSource(paths, options, env, actions) {
  if (!options.piMcpSource) {
    await ensureRepo({ repo: PI_MCP_REPO, dir: paths.piMcpRepoDir, env, dryRun: options.dryRun, actions, force: options.force });
    return paths.piMcpRepoDir;
  }
  if (options.piMcpSource.startsWith('http://') || options.piMcpSource.startsWith('https://') || options.piMcpSource.startsWith('git@')) {
    await ensureRepo({ repo: options.piMcpSource, dir: paths.piMcpRepoDir, env, dryRun: options.dryRun, actions, force: options.force });
    return paths.piMcpRepoDir;
  }
  return options.piMcpSource;
}

async function buildPiMcp(paths, options, env, actions) {
  const source = await preparePiMcpSource(paths, options, env, actions);
  if (options.dryRun) actions.push({ name: 'mkdir', path: paths.binDir });
  else await ensureDir(paths.binDir);
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
  const effectiveOptions = { ...options, stdout: deps.stdout };

  await requireCommand('npm', env, effectiveOptions.dryRun);
  await requireCommand('git', env, effectiveOptions.dryRun);
  await requireCommand('go', env, effectiveOptions.dryRun);

  if (!effectiveOptions.dryRun) await ensureDir(paths.runtimeDir);
  else actions.push({ name: 'mkdir', path: paths.runtimeDir });

  await installPiCore(effectiveOptions, env, actions);
  await buildWorkflow(paths, effectiveOptions, env, actions);
  await writeConfigs(paths, effectiveOptions);
  await buildPiMcp(paths, effectiveOptions, env, actions);
  await registerClaude(paths, effectiveOptions, env, actions);

  const out = deps.stdout || process.stdout;
  if (effectiveOptions.dryRun) {
    out.write('Dry run actions:\n');
    for (const action of actions) out.write(`- ${action.command || `${action.name} ${action.path || ''}`.trim()}\n`);
  }
  out.write('Installed pi-mcp runtime. Next: run `pi`, then `/login`, then `npx @ianfr13/pi-mcp-runtime doctor`.\n');
  return { ok: true, paths, actions };
}

export async function copyLocalPiMcpForPackageRuntime(source, dest) {
  await cp(source, dest, { recursive: true, force: true, verbatimSymlinks: true });
}
