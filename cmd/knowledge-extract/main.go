// Command knowledge-extract 从 llm_conversation 提取知识问答对到 knowledge_pairs 表。
//
// 用法:
//   knowledge-extract --db-url <PG_URL> [--batch 500] [--truncate]
//
// 说明:
//   - 默认增量模式(ON CONFLICT conv_id 幂等 upsert), 可重复运行
//   - --truncate 先清空再全量提取(首次/重建时用)
//   - 只读有效对话(2xx + 有 completion + 无 error_message)
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/company/llm-gateway/internal/knowledge"
)

func main() {
	var (
		dbURL    = flag.String("db-url", os.Getenv("CONTEXT_DB_URL"), "PostgreSQL URL (或 CONTEXT_DB_URL)")
		batch    = flag.Int("batch", 500, "每批提取行数")
		truncate = flag.Bool("truncate", false, "先清空 knowledge_pairs 再全量提取")
		verbose  = flag.Bool("verbose", false, "输出每条提取明细(调试)")
	)
	flag.Parse()

	if *dbURL == "" {
		slog.Error("--db-url 或 CONTEXT_DB_URL 必填")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		slog.Error("connect db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	st := knowledge.NewStore(pool)

	if *truncate {
		if err := st.Truncate(ctx); err != nil {
			slog.Error("truncate", "err", err)
			os.Exit(1)
		}
		slog.Info("truncated knowledge_pairs")
	}

	var (
		scanned, extracted, skipped int
		started                     = false
	)
	err = st.StreamSources(ctx, *batch, func(rows []knowledge.SourceRow) error {
		if !started {
			slog.Info("extraction started", "batch", *batch)
			started = true
		}
		var pairs []knowledge.Pair
		for _, r := range rows {
			scanned++
			caller := ""
			if r.CallerName != nil {
				caller = *r.CallerName
			}
			p := knowledge.Extract(r.PromptText, r.CompletionText, r.Model, caller, r.Endpoint)
			if p == nil {
				skipped++
				continue
			}
			p.ConvID = r.ID
			pairs = append(pairs, *p)
			if *verbose {
				slog.Info("extracted", "conv", r.ID, "q", truncStr(p.Question, 60), "code", p.CodeBlocks)
			}
		}
		n, err := st.UpsertPairs(ctx, pairs)
		if err != nil {
			return err
		}
		extracted += n
		slog.Info("batch done", "scanned", scanned, "extracted", extracted, "skipped", skipped)
		return nil
	})
	if err != nil {
		slog.Error("extract failed", "err", err)
		os.Exit(1)
	}

	// 去重: 同一 question 保留回答最长的一条(质量优化)
	dupDeleted, derr := st.Deduplicate(ctx)
	if derr != nil {
		slog.Error("deduplicate failed", "err", derr)
		os.Exit(1)
	}

	total, _ := st.Count(ctx)
	slog.Info("extraction complete",
		"scanned", scanned,
		"extracted", extracted,
		"skipped", skipped,
		"dedup_deleted", dupDeleted,
		"total_in_table", total,
	)
}

func truncStr(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "..."
}
