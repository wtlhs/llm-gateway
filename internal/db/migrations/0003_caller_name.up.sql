-- +goose Up
-- +goose StatementBegin
-- 新增 caller_name 列: 真实用户名(JOIN users 表获取), 与 caller_tag(令牌名) 区分
ALTER TABLE llm_conversation ADD COLUMN IF NOT EXISTS caller_name VARCHAR(128);

-- caller_name 索引(按真实用户查询)
CREATE INDEX IF NOT EXISTS idx_llm_conv_callername ON llm_conversation (caller_name);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_llm_conv_callername;
ALTER TABLE llm_conversation DROP COLUMN IF EXISTS caller_name;
-- +goose StatementEnd
