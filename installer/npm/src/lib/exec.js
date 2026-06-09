import { spawn } from 'node:child_process';

export async function commandExists(command, options = {}) {
  const checker = process.platform === 'win32' ? 'where' : 'command';
  const args = process.platform === 'win32' ? [command] : ['-v', command];
  const result = await run(checker, args, { ...options, shell: process.platform !== 'win32', quiet: true, allowFailure: true });
  return result.status === 0;
}

export async function run(command, args = [], options = {}) {
  const commandText = [command, ...args].join(' ');
  if (options.actions) options.actions.push({ name: 'run', command: commandText });
  if (options.dryRun) return { status: 0, stdout: '', stderr: '' };

  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: options.cwd,
      env: options.env || process.env,
      stdio: options.quiet ? ['ignore', 'pipe', 'pipe'] : ['ignore', 'inherit', 'pipe'],
      shell: options.shell || false,
    });
    let stdout = '';
    let stderr = '';
    if (child.stdout) child.stdout.on('data', (d) => { stdout += d.toString(); });
    if (child.stderr) child.stderr.on('data', (d) => { stderr += d.toString(); });
    child.on('error', (err) => {
      if (options.allowFailure) resolve({ status: 127, stdout, stderr: err.message });
      else reject(err);
    });
    child.on('close', (status) => {
      const code = status ?? 0;
      if (code === 0 || options.allowFailure) resolve({ status: code, stdout, stderr });
      else reject(new Error(`${commandText} failed with exit ${code}${stderr ? `: ${stderr.trim()}` : ''}`));
    });
  });
}
