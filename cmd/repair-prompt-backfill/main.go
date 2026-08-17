// Command repair-prompt-backfill 修复存量 prompt_text 的 raw 包装:
// 对 {"raw":"..."} 包装且内容可修复的记录, 用 audit.RepairJSON 转回结构化 JSON。
//
// 背景(2026-08-17): safeJSON 宽容化上线前, Claude Code 工具 schema 非法值
// ("maximum":* 等)导致 ~92% 的 prompt_text 是 raw 包装(解包后 JSON 仍非法),
// 知识提取无法解析 → 召回率仅 ~4%。本工具对存量数据重跑修复逻辑。
//
// 用法:
//   repair-prompt-backfill --db-url <PG_URL> [--batch 500] [--dry-run] [--hours 0]
//
// 说明:
//   - 默认扫描全部 raw 记录; --hours 限定窗口(如 168 最近一周)
//   - --dry-run 只统计不更新
//   - 幂等: 仅更新仍为 raw 包装且修复后可解析的记录
//   - request_body_hash 等字段不动(修复仅影响 prompt_text 展示结构)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/company/llm-gateway/internal/audit"
)

func main() {
	var (
		dbURL = flag.String("db-url", os.Getenv("CONTEXT_DB_URL"), "PostgreSQL URL (或 CONTEXT_DB_URL)")
		batch = flag.Int("batch", 500, "每批处理行数")
		dry   = flag.Bool("dry-run", false, "只统计不更新")
		hours = flag.Int("hours", 0, "仅处理最近 N 小时(0=全部)")
		work  = flag.Int("workers", 8, "并行修复 worker 数")
	)
	flag.Parse()
	if *dbURL == "" {
		log.Fatal("--db-url 或 CONTEXT_DB_URL 必填")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		log.Fatal("connect:", err)
	}
	defer pool.Close()

	// 游标分页扫描 raw 包装记录
	lastID := int64(0)
	var scanned, fixed, unfixable int
	t0 := time.Now()
	for {
		var q string
		args := []any{lastID, *batch}
		q = `
			SELECT id, prompt_text::text
			FROM llm_conversation
			WHERE id > $1 AND prompt_text::text LIKE '{"raw":%'`
		if *hours > 0 {
			q += ` AND created_at > now() - make_interval(hours => $3)`
			args = append(args, *hours)
		}
		// 截断记录(truncated=true)是 postMax 截断的残缺 JSON, 修复后仍不完整,
		// 无法转结构化, 直接跳过(存量 1.8 万条, 省 ~60% 扫描)
		q += ` AND truncated = false ORDER BY id LIMIT $2`

		rows, err := pool.Query(ctx, q, args...)
		if err != nil {
			log.Fatal("query:", err)
		}
		type item struct {
			id   int64
			text string
		}
		var items []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.id, &it.text); err != nil {
				rows.Close()
				log.Fatal("scan:", err)
			}
			items = append(items, it)
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			log.Fatal("rows:", rowsErr)
		}
		if len(items) == 0 {
			break
		}

		// 并行修复(repair 是纯 CPU 密集, 大文本 JSON 校验; UPDATE 串行避免连接争用)
		type fixedItem struct {
			id   int64
			text string
			ok   bool
		}
		fixedBatch := make([]fixedItem, len(items))
		var wg sync.WaitGroup
		sem := make(chan struct{}, *work)
		for idx := range items {
			wg.Add(1)
			sem <- struct{}{}
			go func(idx int) {
				defer wg.Done()
				defer func() { <-sem }()
				it := items[idx]
				ft, ok := repairPrompt(it.text)
				fixedBatch[idx] = fixedItem{id: it.id, text: ft, ok: ok}
			}(idx)
		}
		wg.Wait()

		for _, fi := range fixedBatch {
			scanned++
			if !fi.ok {
				unfixable++
				continue
			}
			fixed++
			if *dry {
				continue
			}
			if _, err := pool.Exec(ctx,
				`UPDATE llm_conversation SET prompt_text = $2 WHERE id = $1 AND prompt_text::text LIKE '{"raw":%'`,
				fi.id, fi.text); err != nil {
				log.Printf("update id=%d: %v", fi.id, err)
			}
		}
		if len(items) < *batch {
			break
		}
		lastID = items[len(items)-1].id
	}

	mode := "dry-run"
	if !*dry {
		mode = "updated"
	}
	fmt.Printf("RESULT mode=%s scanned=%d fixed=%d unfixable=%d elapsed=%s\n",
		mode, scanned, fixed, unfixable, time.Since(t0).Round(time.Second))
}

// repairPrompt 解 raw 包装后用 RepairJSON 修复, 返回可落库的结构化文本。
// 成功条件: 修复后为合法 JSON 且不是 raw 包装。
func repairPrompt(raw string) (string, bool) {
	var wrap struct {
		Raw string `json:"raw"`
	}
	if err := json.Unmarshal([]byte(raw), &wrap); err != nil || wrap.Raw == "" {
		return "", false
	}
	inner := []byte(wrap.Raw)
	fixed := audit.RepairJSONScan(inner) // 已知非法, 跳过预检省一次 O(n)
	if len(fixed) == 0 || !json.Valid(fixed) || strings.HasPrefix(strings.TrimSpace(string(fixed)), `{"raw":`) {
		return "", false
	}
	return string(fixed), true
}
