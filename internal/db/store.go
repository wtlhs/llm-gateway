// Package db 提供沉淀数据的持久化与查询。
//
// 实现说明:DESIGN.md §4 选型 sqlc,但 sqlc 工具未内置工具链。
// Phase 1 这里用手写薄封装(基于 pgx/v5)镜像 queries/*.sql 的语义,
// 保证可立即运行;后续 `make sqlc` 生成 gen/ 后可平滑替换本文件。
package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Conversation 对应 llm_conversation 表的一行。
// 与 DESIGN.md §4.2 schema 一一对应。
type Conversation struct {
	ID                int64
	RequestID         string // gateway_id(网关自生成), 幂等键
	UpstreamRequestID string // New API 的 X-Oneapi-Request-Id
	CallerTag         string
	CallerUserID      int32
	CallerGroup       string
	TokenKeyHash      string
	Model             string
	Endpoint          string
	IsStream          bool
	PromptText        json.RawMessage
	CompletionText    json.RawMessage
	ToolCalls         json.RawMessage // 可空
	RequestBodyHash   string          // 可空
	HTTPStatus        int
	PromptTokens      int32
	CompletionTokens  int32
	ErrorMessage      string // 可空
	ClientIP          string
	Redacted          bool
	Truncated         bool
	UpstreamLatencyMs int32
	TotalLatencyMs    int32
	Version           int16
	CreatedAt         time.Time
}

// Store 沉淀库的读写门面。
type Store struct {
	pool *pgxpool.Pool
}

// NewStore 基于 context_repo 库的连接池构造 Store。
func NewStore(ctx context.Context, dbURL string, maxOpen int) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = int32(maxOpen)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Close 释放连接池。
func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// ErrDuplicate 来自 ON CONFLICT DO NOTHING 的语义化表示。
// 注意:本实现用 `RETURNING *`,冲突时 RowsAffected=0 返回 pgx.ErrNoRows。
var ErrDuplicate = errors.New("duplicate request_id, no row inserted")

// Insert 插入一条对话记录;幂等(request_id 冲突时 ErrDuplicate)。
// 对应 queries/conversation.sql::InsertConversation。
func (s *Store) Insert(ctx context.Context, c *Conversation) error {
	const sql = `
INSERT INTO llm_conversation (
    request_id, upstream_request_id, caller_tag, caller_user_id, caller_group,
    token_key_hash, model, endpoint, is_stream, prompt_text, completion_text,
    tool_calls, request_body_hash, http_status, prompt_tokens, completion_tokens,
    error_message, client_ip, redacted, truncated, upstream_latency_ms,
    total_latency_ms, version
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,1)
ON CONFLICT (request_id) DO NOTHING
RETURNING id;`
	tag, err := s.pool.Exec(ctx, sql,
		c.RequestID, nullable(c.UpstreamRequestID), nullable(c.CallerTag),
		c.CallerUserID, nullable(c.CallerGroup), nullable(c.TokenKeyHash),
		c.Model, c.Endpoint, c.IsStream, c.PromptText,
		nullableJSON(c.CompletionText), nullableJSON(c.ToolCalls),
		nullable(c.RequestBodyHash), c.HTTPStatus, c.PromptTokens, c.CompletionTokens,
		nullable(c.ErrorMessage), nullable(c.ClientIP), c.Redacted, c.Truncated,
		c.UpstreamLatencyMs, c.TotalLatencyMs,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrDuplicate
	}
	return nil
}

// DeleteOlderThan 删除早于 cutoff 的记录,返回删除行数。
// 对应 queries/conversation.sql::DeleteOlderThan。
func (s *Store) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	const sql = `DELETE FROM llm_conversation WHERE created_at < $1`
	tag, err := s.pool.Exec(ctx, sql, cutoff)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// BackfillCallerByTokenHash 按 token 哈希回填 caller 信息(cache 刷新后调用)。
// 对应 queries/conversation.sql::BackfillCallerByTokenHash。
func (s *Store) BackfillCallerByTokenHash(ctx context.Context, hash, tag string, userID int32, group string) (int64, error) {
	const sql = `
UPDATE llm_conversation
SET caller_tag = $2, caller_user_id = $3, caller_group = $4
WHERE token_key_hash = $1 AND caller_tag IS NULL`
	tag2, err := s.pool.Exec(ctx, sql, hash, tag, userID, group)
	if err != nil {
		return 0, err
	}
	return tag2.RowsAffected(), nil
}

// TryAcquireTTLLock 尝试获取 TTL advisory lock。返回 true 表示获得锁。
func (s *Store) TryAcquireTTLLock(ctx context.Context, lockID int64) (bool, error) {
	const sql = `SELECT pg_try_advisory_lock($1)`
	var ok bool
	err := s.pool.QueryRow(ctx, sql, lockID).Scan(&ok)
	return ok, err
}

// ReleaseTTLLock 释放 advisory lock。
func (s *Store) ReleaseTTLLock(ctx context.Context, lockID int64) error {
	const sql = `SELECT pg_advisory_unlock($1)`
	_, err := s.pool.Exec(ctx, sql, lockID)
	return err
}

// Count 返回总行数(供 /ctx/stats)。
func (s *Store) Count(ctx context.Context) (int64, error) {
	const sql = `SELECT count(*) FROM llm_conversation`
	var n int64
	err := s.pool.QueryRow(ctx, sql).Scan(&n)
	return n, err
}

// --- 辅助:把空串转为 NULL ---

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

// 防止未使用 import 报错(pgx 用于未来 RowScan 场景保留)。
var _ = pgx.ErrNoRows
