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
