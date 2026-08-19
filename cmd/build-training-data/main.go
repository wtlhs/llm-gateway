// Command build-training-data 从 llm_conversation 构建大模型后训练语料(JSONL)。
//
// 每条记录 = 一个请求轮次(prompt_text.messages 历史 + completion/tool_calls 回复),
// 组装为 OpenAI messages 格式训练样本。清洗:
//   - 脱敏残留(****4321 / a***@example.com) → <REDACTED>
//   - system-reminder 注入剥离
//   - 丢弃: 非 200 / 截断 / 无有效回复 / 回答过短 / 重复请求(request_body_hash)
//
// 用法:
//   build-training-data --db-url <PG_URL> --out /data/train.jsonl [--days 30] [--dry-run]
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
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/company/llm-gateway/internal/training"
)

type stats struct {
	scanned, ok int
	dropErr, dropTrunc, dropNoComp, dropShort, dropDup, dropNoMsg int
	redactedInPrompt, redactedInComp int
}

func main() {
	var (
		dbURL = flag.String("db-url", os.Getenv("CONTEXT_DB_URL"), "PostgreSQL URL (或 CONTEXT_DB_URL)")
		out   = flag.String("out", "", "输出 JSONL 文件(必填, --dry-run 除外)")
		days  = flag.Int("days", 0, "仅处理最近 N 天(0=全量)")
		dry   = flag.Bool("dry-run", false, "只统计不写文件")
		batch = flag.Int("batch", 500, "每批行数")
		minComp = flag.Int("min-completion", 20, "回复内容少于该字符且非工具轮则丢弃")
	)
	flag.Parse()
	if *out == "" && !*dry {
		log.Fatal("--out 必填(或使用 --dry-run)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		log.Fatal("connect:", err)
	}
	defer pool.Close()

	// 全量加载 system_prompts(hash → content), 数量小
	sysMap := map[string]string{}
	{
		rows, err := pool.Query(ctx, `SELECT hash, content FROM system_prompts`)
		if err != nil {
			log.Fatal("load system_prompts:", err)
		}
		for rows.Next() {
			var h, c string
			if err := rows.Scan(&h, &c); err != nil {
				rows.Close()
				log.Fatal("scan system:", err)
			}
			sysMap[h] = c
		}
		rows.Close()
	}

	var f *os.File
	if !*dry {
		f, err = os.Create(*out)
		if err != nil {
			log.Fatal("create out:", err)
		}
		defer f.Close()
	}

	var st stats
	seen := map[string]bool{}
	enc := json.NewEncoder(f)
	if f == nil {
		enc = nil
	}

	lastID := int64(0)
	for {
		q := `
			SELECT id, prompt_text::text, coalesce(completion_text::text,''), coalesce(tool_calls::text,'[]'),
			       http_status, truncated, COALESCE(system_prompt_hash,''), COALESCE(request_body_hash,'')
			FROM llm_conversation
			WHERE id > $1
			  AND http_status >= 200 AND http_status < 300
			  AND truncated = false
			  AND completion_text IS NOT NULL AND completion_text != '{}'::jsonb`
		args := []any{lastID, *batch}
		if *days > 0 {
			q += ` AND created_at > now() - make_interval(days => $3)`
			args = append(args, *days)
		}
		q += ` ORDER BY id LIMIT $2`
		rows, err := pool.Query(ctx, q, args...)
		if err != nil {
			log.Fatal("query:", err)
		}
		type row struct {
			id      int64
			prompt, comp, tools string
			status  int
			trunc   bool
			sysHash string
			bodyHash string
		}
		var items []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.prompt, &r.comp, &r.tools, &r.status, &r.trunc, &r.sysHash, &r.bodyHash); err != nil {
				rows.Close()
				log.Fatal("scan:", err)
			}
			items = append(items, r)
		}
		rowsErr := rows.Err()
		rows.Close()
		if rowsErr != nil {
			log.Fatal("rows:", rowsErr)
		}
		if len(items) == 0 {
			break
		}
		for _, r := range items {
			st.scanned++
			if r.status < 200 || r.status >= 300 {
				st.dropErr++
				continue
			}
			if r.trunc {
				st.dropTrunc++
				continue
			}
			if strings.TrimSpace(r.comp) == "" || strings.TrimSpace(r.comp) == "{}" {
				st.dropNoComp++
				continue
			}
			// 去重: 相同请求体(request_body_hash 全量哈希)只保留第一条
			if r.bodyHash != "" {
				if seen[r.bodyHash] {
					st.dropDup++
					continue
				}
				seen[r.bodyHash] = true
			}

			sample, ok := training.BuildSample(r.prompt, r.comp, r.tools, sysMap[r.sysHash])
			if !ok {
				st.dropNoMsg++
				continue
			}
			// 回答过短且非工具轮丢弃
			last := sample.Messages[len(sample.Messages)-1]
			if len([]rune(last.Content)) < *minComp && len(last.ToolCalls) == 0 {
				st.dropShort++
				continue
			}
			if training.HasRedaction(joinAll(sample)) {
				st.redactedInPrompt++
			}
			st.ok++
			if enc != nil {
				if err := enc.Encode(sample); err != nil {
					log.Fatal("encode:", err)
				}
			}
		}
		lastID = items[len(items)-1].id
		if len(items) < *batch {
			break
		}
	}

	fmt.Printf("RESULT scanned=%d ok=%d\n", st.scanned, st.ok)
	fmt.Printf("  drop: err=%d trunc=%d no_comp=%d short=%d dup=%d no_msg=%d\n",
		st.dropErr, st.dropTrunc, st.dropNoComp, st.dropShort, st.dropDup, st.dropNoMsg)
	fmt.Printf("  redacted_removed=%d\n", st.redactedInPrompt)
	if f != nil {
		fi, _ := f.Stat()
		fmt.Printf("  out_file=%s size=%d\n", *out, fi.Size())
	}
}

func hashRequest(prompt string) string {
	// 粗去重: 前 2048 字符的散列(避免大字符串 map key)
	s := prompt
	if len(s) > 2048 {
		s = s[:2048]
	}
	return s
}

func joinAll(s *training.Sample) string {
	var sb strings.Builder
	for _, m := range s.Messages {
		sb.WriteString(m.Content)
	}
	return sb.String()
}
