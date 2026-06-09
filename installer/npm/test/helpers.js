import { mkdtemp, mkdir, writeFile, chmod } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

export async function tempHome() {
  const root = await mkdtemp(join(tmpdir(), 'pi-mcp-runtime-'));
  return { root, home: join(root, 'home'), pathDir: join(root, 'bin') };
}

export async function fakeExecutable(dir, name, body = 'exit 0') {
  await mkdir(dir, { recursive: true });
  const file = join(dir, name);
  await writeFile(file, `#!/bin/sh\nset -eu\n${body}\n`);
  await chmod(file, 0o755);
  return file;
}
