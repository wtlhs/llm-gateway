-- 0005_knowledge_pairs.up.sql
-- 知识库提取层: 从 llm_conversation 提取(问题, 回答)问答对, 供检索/知识复用。
--
-- 背景:
--   llm_conversation 是对话流水账(25k+ 条), 直接查询无法回答
--   "某业务问题之前怎么解决的"。本表把每条有效对话提炼为最小知识单元:
--   用户问题(最后 user 消息) + agent 回答(completion 全文)。
--
-- 设计要点:
--   - 一对话一知识单元(UNIQUE conv_id), 与源表 1:1, 可回源
--   - question/answer 均 TEXT(不存 JSONB, 提取器已解析为纯文本)
--   - code_blocks/file_paths/keywords 为知识权重字段, 供排序加权
--   - 检索用 pg_trgm GIN 索引(零依赖关键词检索, Phase 3 预留向量列)
--   - 幂等: 提取器可重复运行(ON CONFLICT DO UPDATE / 先清后插)
--
-- 检索示例:
--   SELECT conv_id, question, left(answer,300),
--          similarity(question, $q) AS q_sim
--   FROM knowledge_pairs
--   WHERE question % $q OR answer % $q
--   ORDER BY (q_sim*1.5 + similarity(answer,$q)) DESC LIMIT 10;

-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS knowledge_pairs (
    id            BIGSERIAL PRIMARY KEY,
    conv_id       BIGINT       NOT NULL UNIQUE REFERENCES llm_conversation(id) ON DELETE CASCADE,

    question      TEXT         NOT NULL,   -- 最后一条 user 消息(问题)
    answer        TEXT         NOT NULL,   -- completion 全文(回答)

    -- 知识权重特征(提取器填充, 供排序/过滤)
    code_blocks   INTEGER      NOT NULL DEFAULT 0,   -- 回答中 ``` 代码块数
    file_paths    TEXT[]       DEFAULT '{}',         -- 回答中出现的文件路径
    keywords      TEXT[]       DEFAULT '{}',         -- 从 question 抽取的业务关键词

    -- 归属与时间
    model         VARCHAR(128),
    caller_name   VARCHAR(128),
    endpoint      VARCHAR(64),
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- 检索索引(零依赖关键词检索)
CREATE INDEX IF NOT EXISTS idx_kp_question_trgm ON knowledge_pairs USING gin (question gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_kp_answer_trgm   ON knowledge_pairs USING gin (answer gin_trgm_ops);
CREATE INDEX IF NOT EXISTS idx_kp_keywords      ON knowledge_pairs USING gin (keywords);
CREATE INDEX IF NOT EXISTS idx_kp_caller        ON knowledge_pairs (caller_name);
CREATE INDEX IF NOT EXISTS idx_kp_created       ON knowledge_pairs (created_at);

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS knowledge_pairs;
-- +goose StatementEnd
