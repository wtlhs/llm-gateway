-- +goose Up
-- +goose StatementBegin

-- 知识资产层: system prompt 去重存储(见 docs/KNOWLEDGE_LAYER.md)
CREATE TABLE IF NOT EXISTS system_prompts (
    hash            VARCHAR(64)  PRIMARY KEY,              -- sha256(content), 内容指纹 + 主键
    agent_name      VARCHAR(128),                           -- 识别出的 agent 名(启发式提取)
    content         TEXT         NOT NULL,                  -- 完整 system prompt 原文
    content_size    INTEGER      NOT NULL,                  -- 字节数(便于统计)
    tokens          INTEGER,                                -- 该 system 的 token 数(首次观测值)

    -- 版本与使用追踪
    first_seen      TIMESTAMPTZ  NOT NULL DEFAULT now(),    -- 首次出现时间
    last_seen       TIMESTAMPTZ  NOT NULL DEFAULT now(),    -- 最近出现时间(每次刷新)
    use_count       BIGINT       NOT NULL DEFAULT 0,        -- 累计使用次数
    caller_tags     TEXT[],                                -- 使用过该 system 的 caller 数组

    -- 演进链(同 agent 的不同版本)
    prev_hash       VARCHAR(64),                            -- 上一版本 hash(若能识别同 agent 演进)

    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- agent_name 索引:按 agent 查配置
CREATE INDEX IF NOT EXISTS idx_sysprompt_agent    ON system_prompts (agent_name);
-- 最近使用索引:找活跃 agent
CREATE INDEX IF NOT EXISTS idx_sysprompt_lastseen ON system_prompts (last_seen DESC);
-- 使用次数:找高频 agent(知识库价值排序)
CREATE INDEX IF NOT EXISTS idx_sysprompt_usecount ON system_prompts (use_count DESC);

-- 流水层改造: 引用知识资产层(不破坏现有结构, 老数据 system_prompt_hash=NULL)
ALTER TABLE llm_conversation ADD COLUMN IF NOT EXISTS system_prompt_hash VARCHAR(64);
ALTER TABLE llm_conversation ADD COLUMN IF NOT EXISTS system_prompt_size  INTEGER;

CREATE INDEX IF NOT EXISTS idx_llm_conv_sysprompthash ON llm_conversation (system_prompt_hash);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_llm_conv_sysprompthash;
ALTER TABLE llm_conversation DROP COLUMN IF EXISTS system_prompt_size;
ALTER TABLE llm_conversation DROP COLUMN IF EXISTS system_prompt_hash;
DROP INDEX IF EXISTS idx_sysprompt_usecount;
DROP INDEX IF EXISTS idx_sysprompt_lastseen;
DROP INDEX IF EXISTS idx_sysprompt_agent;
DROP TABLE IF EXISTS system_prompts;
-- +goose StatementEnd
