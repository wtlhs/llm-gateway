package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TokenRow 对应 new-api 库 tokens 表的字段(只读所需列)。
type TokenRow struct {
	Key    string // sk-xxx(将被调用方立即 sha256, 不长期持有)
	Name   string
	UserID int32
	Group  string
}

// TokenReader 只读访问 new-api 库, 用于反查 caller 映射。
// 依据 DESIGN.md §4.4 / §5.4。
type TokenReader struct {
	pool *pgxpool.Pool
}

// NewTokenReader 基于 new-api 库连接池构造(只读账号)。
func NewTokenReader(ctx context.Context, dbURL string) (*TokenReader, error) {
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		return nil, err
	}
	// 只读场景保守连接数
	cfg.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, err
	}
	return &TokenReader{pool: pool}, nil
}

// Close 释放连接池。
func (r *TokenReader) Close() {
	if r.pool != nil {
		r.pool.Close()
	}
}

// LoadAll 拉取所有启用中的 token 映射。
// 对应 queries/token_map.sql::LoadTokenMap。
func (r *TokenReader) LoadAll(ctx context.Context) ([]TokenRow, error) {
	const sql = `
SELECT key, COALESCE(name,''), user_id, COALESCE("group",'')
FROM tokens
WHERE deleted_at IS NULL AND status = 1`
	rows, err := r.pool.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("token reader: %w", err)
	}
	defer rows.Close()

	var out []TokenRow
	for rows.Next() {
		var t TokenRow
		if err := rows.Scan(&t.Key, &t.Name, &t.UserID, &t.Group); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
