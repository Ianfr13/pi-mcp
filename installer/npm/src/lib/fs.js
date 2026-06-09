import { constants as fsConstants } from 'node:fs';
import { access, chmod, copyFile, mkdir, open, readFile, rename, writeFile } from 'node:fs/promises';
import { dirname } from 'node:path';

export async function exists(path) {
  try {
    await access(path, fsConstants.F_OK);
    return true;
  } catch {
    return false;
  }
}

export async function ensureDir(path, mode = 0o700) {
  await mkdir(path, { recursive: true, mode });
  try {
    await chmod(path, mode);
  } catch {
    // Best effort; parent directories may be owned/managed by the platform.
  }
}

export async function readJson(path, fallback = undefined) {
  if (!(await exists(path))) return fallback;
  return JSON.parse(await readFile(path, 'utf8'));
}

function defaultTimestamp() {
  return new Date().toISOString().replace(/[-:.]/g, '').slice(0, 15) + 'Z';
}

export async function writeJsonAtomic(path, value, options = {}) {
  await ensureDir(dirname(path));
  const tmp = `${path}.tmp-${process.pid}`;
  const data = `${JSON.stringify(value, null, 2)}\n`;
  await writeFile(tmp, data, { mode: options.mode || 0o600 });
  const handle = await open(tmp, 'r');
  await handle.sync();
  await handle.close();
  await rename(tmp, path);
  await chmod(path, options.mode || 0o600);
}

export async function writeJsonWithBackup(path, value, options = {}) {
  await ensureDir(dirname(path));
  let backup;
  if (await exists(path)) {
    const timestamp = options.timestamp || defaultTimestamp();
    backup = `${path}.bak-${timestamp}`;
    await copyFile(path, backup);
  }
  await writeJsonAtomic(path, value, options);
  return backup;
}
