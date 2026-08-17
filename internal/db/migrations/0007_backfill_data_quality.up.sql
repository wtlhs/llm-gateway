-- 0007_backfill_data_quality.up.sql
-- 存量数据质量回填(2026-08-17 agent 名单 + usage 估算兜底上线前的存量数据):
--
-- 1. system_prompts.agent_name 回填
--    背景: 08-17 前的网关镜像没有已知 agent 名单, "You are opencode,/"You are ZCode,"
--    等无冠词写法(及 claude-vscode 前缀)全部提取为 unknown 或冗长描述。
--    system_prompts 按 hash upsert, 存量记录不会随新代码重算, 需一次性回填。
--
-- 2. llm_conversation.prompt_tokens 回填
--    背景: GLM 等上游 message_start.usage 恒为 0, 08-17 前记录的 prompt_tokens
--    大量为 0。用与网关 estimatePromptTokens 相同的规则回填估算值并标记
--    usage_estimated=true(与 Go 端剥离字符集一致: {}[]",: \n\t\r\ /)。
--
-- 安全性:
--   - 幂等: 只更新 agent_name='unknown'(及已知旧错误名) / prompt_tokens=0 的行
--   - 事务包裹, 可整体回滚
--   - 不动 content / prompt_text 等其他字段

BEGIN;

-- ============ 1. system_prompts.agent_name 回填 ============
-- 已知 agent 名单(注意顺序: 长短语优先, 如 ZCode Explore 先于 ZCode)
UPDATE system_prompts SET agent_name = 'Claude'
WHERE agent_name IN ('unknown', 'Claude agent', 'interactive agent that helps users with software engineering tasks', 'interactive coding agent')
  AND (content LIKE '%cc_entrypoint=claude%' OR content ILIKE '%You are Claude%');

UPDATE system_prompts SET agent_name = 'opencode'
WHERE agent_name = 'unknown' AND content ILIKE '%You are opencode,%';

UPDATE system_prompts SET agent_name = 'ZCode Explore'
WHERE agent_name = 'unknown' AND content ILIKE '%You are ZCode Explore%';

UPDATE system_prompts SET agent_name = 'ZCode'
WHERE agent_name = 'unknown' AND content ILIKE '%You are ZCode,%';

UPDATE system_prompts SET agent_name = 'TraeCode'
WHERE agent_name = 'unknown' AND (content ILIKE '%You are TraeCode%' OR content ILIKE '%in TraeCode%');

UPDATE system_prompts SET agent_name = 'Gemini CLI'
WHERE agent_name = 'unknown' AND content ILIKE '%You are Gemini CLI%';

UPDATE system_prompts SET agent_name = 'Qwen Code'
WHERE agent_name = 'unknown' AND content ILIKE '%You are Qwen Code%';

UPDATE system_prompts SET agent_name = 'Roo Code'
WHERE agent_name = 'unknown' AND content ILIKE '%You are Roo Code%';

UPDATE system_prompts SET agent_name = 'Amazon Q'
WHERE agent_name = 'unknown' AND content ILIKE '%You are Amazon Q%';

UPDATE system_prompts SET agent_name = 'Windsurf'
WHERE agent_name = 'unknown' AND content ILIKE '%You are Windsurf%';

UPDATE system_prompts SET agent_name = 'Cursor'
WHERE agent_name = 'unknown' AND content ILIKE '%You are Cursor%';

UPDATE system_prompts SET agent_name = 'Copilot'
WHERE agent_name = 'unknown' AND content ILIKE '%You are Copilot%';

UPDATE system_prompts SET agent_name = 'Cline'
WHERE agent_name = 'unknown' AND content ILIKE '%You are Cline%';

UPDATE system_prompts SET agent_name = 'Aider'
WHERE agent_name = 'unknown' AND content ILIKE '%You are Aider%';

UPDATE system_prompts SET agent_name = 'Codex'
WHERE agent_name = 'unknown' AND content ILIKE '%You are Codex%';

UPDATE system_prompts SET agent_name = 'Cody'
WHERE agent_name = 'unknown' AND content ILIKE '%You are Cody%';

UPDATE system_prompts SET agent_name = 'Continue'
WHERE agent_name = 'unknown' AND content ILIKE '%You are Continue%';

UPDATE system_prompts SET agent_name = 'Kilo'
WHERE agent_name = 'unknown' AND content ILIKE '%You are Kilo%';

UPDATE system_prompts SET agent_name = 'Mentat'
WHERE agent_name = 'unknown' AND content ILIKE '%You are Mentat%';

UPDATE system_prompts SET agent_name = 'OpenHands'
WHERE agent_name = 'unknown' AND content ILIKE '%You are OpenHands%';

UPDATE system_prompts SET agent_name = 'SWE-agent'
WHERE agent_name = 'unknown' AND content ILIKE '%You are SWE-agent%';

-- ============ 2. llm_conversation.prompt_tokens 回填 ============
-- 与网关 estimatePromptTokens 一致: 剥离 JSON 结构字符 {}[]",: \n\t\r\ / 后 ~2字符/token
-- 仅回填成功响应(200)且 usage 缺失(0)的存量记录
UPDATE llm_conversation SET
    prompt_tokens = GREATEST(1, length(
        translate(prompt_text::text, '{}[]",: ' || chr(10) || chr(9) || chr(13) || '\' || '/', '')
    ) / 2),
    usage_estimated = true
WHERE http_status = 200
  AND prompt_tokens = 0
  AND usage_estimated = false
  AND prompt_text IS NOT NULL
  AND jsonb_typeof(prompt_text) = 'object';

COMMIT;
