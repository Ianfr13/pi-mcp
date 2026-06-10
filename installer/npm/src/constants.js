export const PI_CORE_PACKAGE = '@earendil-works/pi-coding-agent';
export const WORKFLOW_REPO = 'https://github.com/Ianfr13/pi-dynamic-workflows-custom.git';
export const WORKFLOW_REF = 'cd87fbe435cd133f647b9e2189a685d0eb61d92c';
export const PI_MCP_REPO = 'https://github.com/Ianfr13/pi-mcp.git';
export const WORKFLOW_DIR_NAME = 'pi-dynamic-workflows-custom';
export const PI_MCP_DIR_NAME = 'pi-mcp';

export const DEFAULT_ENABLED_MODELS = [
  'deepseek/deepseek-v4-pro',
  'deepseek/deepseek-v4-flash',
  'openai-codex/gpt-5.5',
  'minimax/MiniMax-M3',
  'kimi-coding/kimi-for-coding',
];

export const DEFAULT_MODEL_TIERS = {
  tiers: {
    small: 'deepseek/deepseek-v4-flash',
    medium: 'minimax/MiniMax-M3',
    big: 'openai-codex/gpt-5.5',
    cheap: 'deepseek/deepseek-v4-flash',
    coder: 'kimi-coding/kimi-for-coding',
    judge: 'openai-codex/gpt-5.5',
    research: 'minimax/MiniMax-M3',
  },
  rules: [
    { name: 'coder agentType', match: { agentType: 'coder' }, tier: 'coder' },
    { name: 'reviewer agentType', match: { agentType: 'reviewer' }, tier: 'judge' },
    { name: 'scan phase', match: { phase: 'Scan' }, tier: 'cheap' },
    { name: 'judgment labels', match: { label: '/judge|critic|review|synthesis|final/i' }, tier: 'judge' },
  ],
  fallback: { mode: 'error' },
};
