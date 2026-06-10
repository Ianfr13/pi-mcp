import test from 'node:test';
import assert from 'node:assert/strict';
import { mergeSettings, modelTiersConfig } from '../src/lib/config.js';
import { DEFAULT_MODEL_TIERS, WORKFLOW_REF } from '../src/constants.js';

const workflowPath = '/home/user/.pi-mcp/runtime/pi-dynamic-workflows-custom';
const expectedWorkflowRef = 'cd87fbe435cd133f647b9e2189a685d0eb61d92c';

test('mergeSettings preserves unrelated fields and appends workflow path once', () => {
  const existing = {
    theme: 'dark',
    packages: ['npm:pi-web-access', 'npm:@quintinshaw/pi-dynamic-workflows'],
  };
  const next = mergeSettings(existing, workflowPath);
  assert.equal(next.theme, 'dark');
  assert.deepEqual(next.packages, ['npm:pi-web-access', workflowPath]);
  assert.equal(next.defaultModel, 'gpt-5.5');
  assert.equal(next.defaultThinkingLevel, 'high');
  assert.equal(next.defaultProvider, 'openai-codex');
  assert.deepEqual(next.enabledModels, [
    'deepseek/deepseek-v4-pro',
    'deepseek/deepseek-v4-flash',
    'openai-codex/gpt-5.5',
    'minimax/MiniMax-M3',
    'kimi-coding/kimi-for-coding',
  ]);
});

test('mergeSettings is idempotent', () => {
  const once = mergeSettings({ packages: [workflowPath] }, workflowPath);
  const twice = mergeSettings(once, workflowPath);
  assert.deepEqual(twice.packages.filter((p) => p === workflowPath), [workflowPath]);
  assert.deepEqual(twice, once);
});

test('modelTiersConfig returns exact Ian routing profile clone', () => {
  assert.deepEqual(modelTiersConfig(), DEFAULT_MODEL_TIERS);
  const one = modelTiersConfig();
  one.tiers.small = 'changed';
  assert.equal(modelTiersConfig().tiers.small, 'deepseek/deepseek-v4-flash');
});

test('installer pins the current team workflow custom commit', () => {
  assert.equal(WORKFLOW_REF, expectedWorkflowRef);
});
