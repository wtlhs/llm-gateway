-- 0006_usage_estimated.up.sql
-- 新增 usage_estimated 列: 标记 prompt_tokens 为本地估算值(上游未返回 usage 时兜底)。
--
-- 背景:
--   GLM 等上游的 Anthropic 兼容层 message_start.usage.input_tokens 恒为 0(实测),
--   Kimi k3 等则正常返回。导致 glm-5.2 的 prompt_tokens 大量为 0(部署后 14683 条中
--   仅 33.8% 有真实值), 影响 token 计量统计。
--   网关侧 Finalize 兜底: 上游缺失时用请求体粗估(剥离 JSON 结构字符后 ~2字符/token),
--   并置 usage_estimated=true, 平台可按此列区分真实/估算值。

-- +goose Up
-- +goose StatementBegin
ALTER TABLE llm_conversation ADD COLUMN IF NOT EXISTS usage_estimated BOOLEAN NOT NULL DEFAULT FALSE;

-- 估算值占比统计索引
CREATE INDEX IF NOT EXISTS idx_llm_conv_usage_est ON llm_conversation (usage_estimated);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_llm_conv_usage_est;
ALTER TABLE llm_conversation DROP COLUMN IF EXISTS usage_estimated;
-- +goose StatementEnd
