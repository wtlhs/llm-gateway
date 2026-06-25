-- queries/conversation.sql  (sqlc 输入; 运行 `make sqlc` 生成 gen/)
-- 同时被 internal/db/store.go 手写实现镜像(因 sqlc 未内置于工具链, Phase1 用手写薄封装直跑)。

-- name: InsertConversation :one
INSERT INTO llm_conversation (
    request_id, upstream_request_id, caller_tag, caller_user_id, caller_group,
    token_key_hash, model, endpoint, is_stream, prompt_text, completion_text,
    tool_calls, request_body_hash, http_status, prompt_tokens, completion_tokens,
    error_message, client_ip, redacted, truncated, upstream_latency_ms,
    total_latency_ms, version
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,1)
ON CONFLICT (request_id) DO NOTHING
RETURNING *;

-- name: DeleteOlderThan :execrows
DELETE FROM llm_conversation WHERE created_at < $1;

-- name: BackfillCallerByTokenHash :execrows
UPDATE llm_conversation
SET caller_tag = $2, caller_user_id = $3, caller_group = $4
WHERE token_key_hash = $1 AND caller_tag IS NULL;

-- TTL 多实例安全: advisory lock 防重复扫表
-- name: TryAcquireTtlLock :one
SELECT pg_try_advisory_lock($1) AS ok;

-- name: ReleaseTtlLock :exec
SELECT pg_advisory_unlock($1);

-- name: CountConversations :one
SELECT count(*) FROM llm_conversation;
