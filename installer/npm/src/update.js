import { install } from './install.js';

export async function update(options = {}, deps = {}) {
  const result = await install({ ...options, force: true }, deps);
  return { ...result, mode: 'update' };
}
