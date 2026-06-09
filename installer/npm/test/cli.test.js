import test from 'node:test';
import assert from 'node:assert/strict';
import { parseArgs } from '../src/cli.js';

const names = (actions) => actions.map((a) => a.command);

test('parseArgs defaults to help when no command is provided', () => {
  const parsed = parseArgs([]);
  assert.equal(parsed.command, 'help');
  assert.deepEqual(parsed.options, {});
});

test('parseArgs parses install flags', () => {
  const parsed = parseArgs([
    'install',
    '--workflow-ref', 'abc123',
    '--runtime-dir', '/tmp/runtime',
    '--bin-dir', '/tmp/bin',
    '--pi-mcp-source', '/repo/pi-mcp',
    '--skip-claude-register',
    '--force',
    '--dry-run',
  ]);
  assert.equal(parsed.command, 'install');
  assert.equal(parsed.options.workflowRef, 'abc123');
  assert.equal(parsed.options.runtimeDir, '/tmp/runtime');
  assert.equal(parsed.options.binDir, '/tmp/bin');
  assert.equal(parsed.options.piMcpSource, '/repo/pi-mcp');
  assert.equal(parsed.options.skipClaudeRegister, true);
  assert.equal(parsed.options.force, true);
  assert.equal(parsed.options.dryRun, true);
});

test('parseArgs rejects unknown flags', () => {
  assert.throws(() => parseArgs(['install', '--bogus']), /Unknown option: --bogus/);
});

test('parseArgs exposes command names used by docs', () => {
  assert.deepEqual(names([
    parseArgs(['install']),
    parseArgs(['doctor']),
    parseArgs(['update']),
  ]), ['install', 'doctor', 'update']);
});
