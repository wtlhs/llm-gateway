-- queries/token_map.sql  (连 new-api 库执行, 只读)
-- 注意 "group" 是保留字, 必须加双引号。status=1 表示启用中。

-- name: LoadTokenMap :many
SELECT key, name, user_id, "group"
FROM tokens
WHERE deleted_at IS NULL
  AND status = 1;
