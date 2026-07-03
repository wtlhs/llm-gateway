package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/go-sql-driver/mysql" // MySQL 驱动注册
)

// TokenRow 对应 new-api 库 tokens 表的字段(只读所需列)。
type TokenRow struct {
	Key      string // sk-xxx(将被调用方立即 sha256, 不长期持有)
	Name     string
	UserID   int32
	Group    string
	UserName string // 真实用户名(JOIN users 表, 可空)
}

// TokenReader 只读访问 new-api MySQL 库, 用于反查 caller 映射。
// 依据 DESIGN.md §4.4 / §5.4。
type TokenReader struct {
	db *sql.DB
}

// NewTokenReader 基于 new-api MySQL 库连接池构造(只读账号)。
// dbURL 格式: user:pass@tcp(host:3306)/new_api?charset=utf8mb4&parseTime=true
func NewTokenReader(ctx context.Context, dbURL string) (*TokenReader, error) {
	db, err := sql.Open("mysql", dbURL)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	// 只读场景保守连接数
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	// 防止 MySQL wait_timeout 主动断开空闲连接后, 下次刷新报 bad connection
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return &TokenReader{db: db}, nil
}

// Close 释放连接池。
func (r *TokenReader) Close() {
	if r.db != nil {
		r.db.Close()
	}
}

// LoadAll 拉取所有启用中的 token 映射(含真实用户名)。
// MySQL 注意: key/group 均用反引号(MySQL 保留字/关键字)。
// LEFT JOIN users 表获取真实用户名(user_id → users.username)。
func (r *TokenReader) LoadAll(ctx context.Context) ([]TokenRow, error) {
	const sqlQuery = `
SELECT t.` + "`key`" + `, COALESCE(t.` + "`name`" + `,''), t.user_id,
       COALESCE(t.` + "`group`" + `,''), COALESCE(u.username,'')
FROM tokens t
LEFT JOIN users u ON t.user_id = u.id
WHERE t.deleted_at IS NULL AND t.status = 1`

	rows, err := r.db.QueryContext(ctx, sqlQuery)
	if err != nil {
		return nil, fmt.Errorf("token reader: %w", err)
	}
	defer rows.Close()

	var out []TokenRow
	for rows.Next() {
		var t TokenRow
		if err := rows.Scan(&t.Key, &t.Name, &t.UserID, &t.Group, &t.UserName); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
