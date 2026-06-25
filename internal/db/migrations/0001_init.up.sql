-- +goose Up
-- +goose StatementBegin
CREATE TABLE IF NOT EXISTS llm_conversation (
    id                  BIGSERIAL PRIMARY KEY,
    -- id 语义见 DESIGN.md §5.1.1
    --   request_id           = 网关自生成 gateway_id (幂等键)
    --   upstream_request_id  = New API 的 X-Oneapi-Request-Id (关联 New API logs)
    request_id          VARCHAR(64)  NOT NULL UNIQUE,
    upstream_request_id VARCHAR(128),

    -- caller 标识(反查 token 表填充; miss 时用 token_key_hash 兜底)
    caller_tag          VARCHAR(128),
    caller_user_id      INTEGER,
    caller_group        VARCHAR(64),
    token_key_hash      VARCHAR(64),

    -- 调用上下文
    model               VARCHAR(128) NOT NULL,
    endpoint            VARCHAR(64)  NOT NULL,
    is_stream           BOOLEAN      NOT NULL DEFAULT FALSE,

    -- 内容(审计核心)
    prompt_text         JSONB        NOT NULL,
    completion_text     JSONB        DEFAULT '{}'::jsonb,
    tool_calls          JSONB,
    request_body_hash   VARCHAR(64),

    -- 计量与状态(冗余; 权威在 New API logs)
    http_status         INTEGER      NOT NULL,
    prompt_tokens       INTEGER,
    completion_tokens   INTEGER,
    error_message       TEXT,
    client_ip           VARCHAR(64),
    redacted            BOOLEAN      NOT NULL DEFAULT FALSE,
    truncated           BOOLEAN      NOT NULL DEFAULT FALSE,

    -- 性能观测
    upstream_latency_ms INTEGER,
    total_latency_ms    INTEGER,

    -- 演进兼容
    version             SMALLINT     NOT NULL DEFAULT 1,
    created_at          TIMESTAMPTZ  NOT NULL DEFAULT now()
);
-- +goose StatementEnd

CREATE INDEX IF NOT EXISTS idx_llm_conv_created   ON llm_conversation (created_at);
CREATE INDEX IF NOT EXISTS idx_llm_conv_model_ct  ON llm_conversation (model, created_at);
CREATE INDEX IF NOT EXISTS idx_llm_conv_caller_ct ON llm_conversation (caller_user_id, created_at);
CREATE INDEX IF NOT EXISTS idx_llm_conv_hash      ON llm_conversation (request_body_hash);
CREATE INDEX IF NOT EXISTS idx_llm_conv_tokenhash ON llm_conversation (token_key_hash);

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS llm_conversation;
-- +goose StatementEnd
