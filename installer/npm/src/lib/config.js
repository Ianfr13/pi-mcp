import { DEFAULT_ENABLED_MODELS, DEFAULT_MODEL_TIERS } from '../constants.js';

export function mergeSettings(existing = {}, workflowPath) {
  const packages = Array.isArray(existing.packages) ? [...existing.packages] : [];
  const filtered = packages.filter((p) => p !== 'npm:@quintinshaw/pi-dynamic-workflows');
  if (!filtered.includes(workflowPath)) filtered.push(workflowPath);

  return {
    ...existing,
    packages: filtered,
    defaultModel: 'gpt-5.5',
    defaultThinkingLevel: 'high',
    enabledModels: [...DEFAULT_ENABLED_MODELS],
    defaultProvider: 'openai-codex',
  };
}

export function modelTiersConfig() {
  return JSON.parse(JSON.stringify(DEFAULT_MODEL_TIERS));
}
