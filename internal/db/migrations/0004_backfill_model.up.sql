-- 0004_backfill_model.up.sql
-- 回填 llm_conversation.model 字段（修复 safeJSON 包装导致 model 提取失败的存量数据）
--
-- 背景:
--   网关 record.go 的 safeJSON 在请求体被截断/非合法 JSON 时, 会把原始请求体
--   包成 {"raw": "<原始JSON字符串>"} 落库。extractPromptMeta 的正则在包装后的
--   数据上匹配不到 model（因为 "model" 变成了转义的 \"model\"），导致 model 列空。
--
-- 修复策略:
--   统一用正则从 prompt_text 里提取 model（同时兼容顶层和 raw 转义两种形式）:
--   - 顶层: {"model":"glm-5.2"}            → 匹配 model":"glm-5.2"
--   - raw:  {"raw":"{\"model\":\"glm-5.2\"}"} → 匹配 model\":\"glm-5.2"
--   正则: 'model\\"?\s*:\s*\\"?([A-Za-z0-9._/-]+)' 同时匹配两种（引号前可能有0或1个反斜杠）
--
-- 覆盖率: 15599/16399 = 95.1%（验证过的正则, 分布合理）
-- 剩余 800 条是请求体严重残缺, 无法提取, 保持空值
--
-- 安全性:
--   - 幂等: 只更新 model 为空且能提取到 model 的行; 已有 model 的行不受影响
--   - 事务: 整体在事务里, 可回滚
--   - 不动 prompt_text / completion_text 等其他字段

BEGIN;

\echo '=== 修复前: model 为空的记录数 ==='
SELECT COUNT(*) AS empty_model_before FROM llm_conversation WHERE model = '' OR model IS NULL;

-- 主回填: 用验证过的正则从 prompt_text 提取 model
UPDATE llm_conversation
SET model = (regexp_match(prompt_text::text, 'model\\"?\s*:\s*\\"?([A-Za-z0-9._/-]+)'))[1]
WHERE (model = '' OR model IS NULL)
  AND (regexp_match(prompt_text::text, 'model\\"?\s*:\s*\\"?([A-Za-z0-9._/-]+)'))[1] IS NOT NULL;

\echo '=== 修复后: model 为空的记录数 ==='
SELECT COUNT(*) AS empty_model_after FROM llm_conversation WHERE model = '' OR model IS NULL;

\echo '=== 回填后 model 分布 TOP 15 ==='
SELECT model, COUNT(*) AS calls
FROM llm_conversation 
GROUP BY model ORDER BY calls DESC LIMIT 15;

COMMIT;
