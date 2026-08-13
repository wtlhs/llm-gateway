package knowledge

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store 提供 knowledge_pairs 的读取与批量写入。
type Store struct {
	pool *pgxpool.Pool
}

// NewStore 基于已有 pgxpool 构造(与 db.Store 共用连接池语义)。
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// SourceRow 是提取器的输入行(从 llm_conversation 读取)。
type SourceRow struct {
	ID               int64
	PromptText       string
	CompletionText   string
	Model            string
	CallerName       *string
	Endpoint         string
}

// StreamSources 分批流式读取有效对话(错误请求/空 completion 跳过)。
// batchSize 控制每批行数; 返回的迭代器逐批回调。
//
// 修复(2026-08-12): 原实现一次 Query 全量(ORDER BY id 触发全表排序, 2GB TEXT
// cast 导致容器 OOM 被杀)。改为按 id 游标分页(WHERE id > last), 每批只加载
// batchSize 行, 内存恒定, 支持超大表。
func (s *Store) StreamSources(ctx context.Context, batchSize int, fn func([]SourceRow) error) error {
	lastID := int64(0)
	for {
		const q = `
			SELECT id, prompt_text::text, coalesce(completion_text::text, ''),
			       model, caller_name, endpoint
			FROM llm_conversation
			WHERE id > $1
			  AND error_message IS NULL
			  AND completion_text IS NOT NULL
			  AND completion_text != '{}'::jsonb
			  AND http_status >= 200 AND http_status < 300
			ORDER BY id
			LIMIT $2`
		rows, err := s.pool.Query(ctx, q, lastID, batchSize)
		if err != nil {
			return fmt.Errorf("query sources: %w", err)
		}
		batch := make([]SourceRow, 0, batchSize)
		for rows.Next() {
			var r SourceRow
			if err := rows.Scan(&r.ID, &r.PromptText, &r.CompletionText, &r.Model, &r.CallerName, &r.Endpoint); err != nil {
				rows.Close()
				return fmt.Errorf("scan row: %w", err)
			}
			batch = append(batch, r)
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			return fmt.Errorf("iterate rows: %w", rowsErr)
		}
		if len(batch) == 0 {
			return nil // 全部读完
		}
		if err := fn(batch); err != nil {
			return err
		}
		lastID = batch[len(batch)-1].ID
		if len(batch) < batchSize {
			return nil // 最后一页
		}
	}
}

// UpsertPairs 批量写入问答对(ON CONFLICT conv_id 更新, 幂等可重跑)。
// 返回写入条数。
func (s *Store) UpsertPairs(ctx context.Context, pairs []Pair) (int, error) {
	if len(pairs) == 0 {
		return 0, nil
	}
	// 分批事务写入(单批不宜过大)
	const batchMax = 200
	written := 0
	for start := 0; start < len(pairs); start += batchMax {
		end := min(start+batchMax, len(pairs))
		b := pairs[start:end]
		n, err := s.upsertBatch(ctx, b)
		if err != nil {
			return written, err
		}
		written += n
	}
	return written, nil
}

func (s *Store) upsertBatch(ctx context.Context, pairs []Pair) (int, error) {
	const q = `
		INSERT INTO knowledge_pairs
			(conv_id, question, answer, code_blocks, file_paths, keywords, model, caller_name, endpoint, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9, now())
		ON CONFLICT (conv_id) DO UPDATE SET
			question = EXCLUDED.question,
			answer   = EXCLUDED.answer,
			code_blocks = EXCLUDED.code_blocks,
			file_paths  = EXCLUDED.file_paths,
			keywords    = EXCLUDED.keywords,
			model       = EXCLUDED.model,
			caller_name = EXCLUDED.caller_name,
			endpoint    = EXCLUDED.endpoint,
			created_at  = now()`

	batch := &pgx.Batch{}
	for _, p := range pairs {
		batch.Queue(q,
			p.ConvID, p.Question, p.Answer, p.CodeBlocks,
			p.FilePaths, p.Keywords, p.Model, nullableStr(p.CallerName), p.Endpoint,
		)
	}
	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()
	n := 0
	for range pairs {
		if _, err := br.Exec(); err != nil {
			return n, fmt.Errorf("upsert pair: %w", err)
		}
		n++
	}
	return n, nil
}

// Count 返回当前 knowledge_pairs 总条数。
func (s *Store) Count(ctx context.Context) (int64, error) {
	var c int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM knowledge_pairs`).Scan(&c)
	return c, err
}

// Deduplicate 删除重复问答对: 同一 question 保留回答最长(最完整)的一条。
// 删除条件: 存在同 question 且 answer 更长的另一条记录。
// 返回删除条数。
func (s *Store) Deduplicate(ctx context.Context) (int64, error) {
	const q = `
		DELETE FROM knowledge_pairs a
		USING knowledge_pairs b
		WHERE a.question = b.question
		  AND length(a.answer) < length(b.answer)`
	tag, err := s.pool.Exec(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("deduplicate: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Truncate 清空表(用于完全重跑)。
func (s *Store) Truncate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `TRUNCATE knowledge_pairs RESTART IDENTITY CASCADE`)
	return err
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

var _ = strings.TrimSpace // keep import if unused in future refactors
