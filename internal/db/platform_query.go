package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// 本文件提供平台后端(cmd/platform)的只读查询方法。
// 设计要点:
//   - 全部只读, 不影响网关热路径的写入
//   - 时间聚合按 Asia/Shanghai 切分(遵守 docs/TIMEZONE.md)
//   - 查询自带 LIMIT, 防全表扫
//   - 调用方负责 context 超时(防慢查询拖垮 PG)

// ConversationFilter 对话列表筛选条件。
type ConversationFilter struct {
	Page    int    // 1-based
	Size    int    // 每页条数
	Model   string // 精确匹配, 空则不限
	Caller  string // caller_tag 精确匹配
	Endpoint string
	IsStream *bool // nil=不限, true/false=精确
	Status   int   // 0=不限, 否则精确匹配 http_status 的百位(2/4/5)
	TimeFrom time.Time
	TimeTo   time.Time
}

// ConversationSummary 对话列表项(轻量, 不含完整 prompt/completion)。
type ConversationSummary struct {
	ID               int64          `json:"id"`
	Model            string         `json:"model"`
	Endpoint         string         `json:"endpoint"`
	CallerTag        string         `json:"caller_tag"`
	IsStream         bool           `json:"is_stream"`
	HTTPStatus       int            `json:"http_status"`
	PromptTokens     int32          `json:"prompt_tokens"`
	CompletionTokens int32          `json:"completion_tokens"`
	UpstreamLatency  int32          `json:"upstream_latency_ms"`
	SystemPromptHash string         `json:"system_prompt_hash,omitempty"`
	Truncated        bool           `json:"truncated"`
	CreatedAt        time.Time      `json:"created_at"`
}

// ListConversations 分页查询对话列表。
func (s *Store) ListConversations(ctx context.Context, f ConversationFilter) ([]ConversationSummary, int64, error) {
	if f.Page < 1 {
		f.Page = 1
	}
	if f.Size <= 0 || f.Size > 100 {
		f.Size = 20
	}

	// 构造 WHERE
	where := "WHERE 1=1"
	args := []any{}
	argIdx := 1
	addWhere := func(cond string, val any) {
		where += " AND " + cond
		args = append(args, val)
		argIdx++
	}
	if f.Model != "" {
		addWhere("model = $"+itoa(argIdx), f.Model)
	}
	if f.Caller != "" {
		addWhere("caller_tag = $"+itoa(argIdx), f.Caller)
	}
	if f.Endpoint != "" {
		addWhere("endpoint = $"+itoa(argIdx), f.Endpoint)
	}
	if f.IsStream != nil {
		addWhere("is_stream = $"+itoa(argIdx), *f.IsStream)
	}
	if f.Status > 0 {
		// status=2 → 200-299, 4 → 400-499, 5 → 500-599
		lo := f.Status * 100
		hi := lo + 99
		where += " AND http_status BETWEEN $" + itoa(argIdx) + " AND $" + itoa(argIdx+1)
		args = append(args, lo, hi)
		argIdx += 2
	}
	if !f.TimeFrom.IsZero() {
		addWhere("created_at >= $"+itoa(argIdx), f.TimeFrom)
	}
	if !f.TimeTo.IsZero() {
		addWhere("created_at <= $"+itoa(argIdx), f.TimeTo)
	}

	// 先查总数
	countSQL := "SELECT count(*) FROM llm_conversation " + where
	var total int64
	if err := s.pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// 查列表
	listSQL := fmt.Sprintf(`
SELECT id, coalesce(model,''), endpoint, coalesce(caller_tag,''), is_stream, http_status,
       coalesce(prompt_tokens,0), coalesce(completion_tokens,0), coalesce(upstream_latency_ms,0),
       coalesce(system_prompt_hash,''), truncated, created_at
FROM llm_conversation %s
ORDER BY id DESC
LIMIT %d OFFSET %d`, where, f.Size, (f.Page-1)*f.Size)

	rows, err := s.pool.Query(ctx, listSQL, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []ConversationSummary
	for rows.Next() {
		var c ConversationSummary
		if err := rows.Scan(&c.ID, &c.Model, &c.Endpoint, &c.CallerTag, &c.IsStream,
			&c.HTTPStatus, &c.PromptTokens, &c.CompletionTokens, &c.UpstreamLatency,
			&c.SystemPromptHash, &c.Truncated, &c.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// GetConversation 查询单条对话完整内容(含 prompt/completion)。
func (s *Store) GetConversation(ctx context.Context, id int64) (*Conversation, error) {
	const sql = `
SELECT id, request_id, coalesce(upstream_request_id,''), coalesce(caller_tag,''),
       caller_user_id, coalesce(caller_group,''), coalesce(token_key_hash,''),
       coalesce(model,''), endpoint, is_stream, prompt_text,
       coalesce(completion_text,'{}'::jsonb), tool_calls, coalesce(request_body_hash,''),
       http_status, prompt_tokens, completion_tokens, coalesce(error_message,''),
       coalesce(client_ip,''), redacted, truncated, upstream_latency_ms, total_latency_ms,
       coalesce(system_prompt_hash,''), system_prompt_size, created_at
FROM llm_conversation WHERE id = $1`
	var c Conversation
	var prompt, completion json.RawMessage
	var tools json.RawMessage
	err := s.pool.QueryRow(ctx, sql, id).Scan(
		&c.ID, &c.RequestID, &c.UpstreamRequestID, &c.CallerTag,
		&c.CallerUserID, &c.CallerGroup, &c.TokenKeyHash,
		&c.Model, &c.Endpoint, &c.IsStream, &prompt,
		&completion, &tools, &c.RequestBodyHash,
		&c.HTTPStatus, &c.PromptTokens, &c.CompletionTokens, &c.ErrorMessage,
		&c.ClientIP, &c.Redacted, &c.Truncated, &c.UpstreamLatencyMs, &c.TotalLatencyMs,
		&c.SystemPromptHash, &c.SystemPromptSize, &c.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	c.PromptText = prompt
	c.CompletionText = completion
	c.ToolCalls = tools
	return &c, nil
}

// Overview 总览看板聚合数据。
type Overview struct {
	Total            int64   `json:"total"`
	TodayCount       int64   `json:"today_count"`
	PromptTokensSum  int64   `json:"prompt_tokens_sum"`
	CompletionTokens int64   `json:"completion_tokens_sum"`
	AvgLatencyMs     float64 `json:"avg_latency_ms"`
	ErrorCount       int64   `json:"error_count"`
}

// DashboardOverview 总览聚合(今日 + 全量)。
// timezone 用于"今日"切分(遵守 docs/TIMEZONE.md)。
func (s *Store) DashboardOverview(ctx context.Context, timezone string) (*Overview, error) {
	const sql = `
SELECT
  count(*) AS total,
  count(*) FILTER (WHERE created_at AT TIME ZONE $1 >= date_trunc('day', now() AT TIME ZONE $1)) AS today_count,
  coalesce(sum(prompt_tokens), 0) AS p_sum,
  coalesce(sum(completion_tokens), 0) AS c_sum,
  coalesce(avg(upstream_latency_ms), 0) AS avg_lat,
  count(*) FILTER (WHERE http_status >= 400) AS err_cnt
FROM llm_conversation`
	var o Overview
	err := s.pool.QueryRow(ctx, sql, timezone).Scan(
		&o.Total, &o.TodayCount, &o.PromptTokensSum, &o.CompletionTokens,
		&o.AvgLatencyMs, &o.ErrorCount)
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// TrendPoint 趋势数据点。
type TrendPoint struct {
	Date             string `json:"date"`
	Count            int64  `json:"count"`
	PromptTokens     int64  `json:"prompt_tokens"`
	CompletionTokens int64  `json:"completion_tokens"`
}

// DashboardTrend 按天趋势(最近 N 天, 按 timezone 切天)。
func (s *Store) DashboardTrend(ctx context.Context, timezone string, days int) ([]TrendPoint, error) {
	if days <= 0 || days > 90 {
		days = 7
	}
	const sql = `
SELECT
  to_char(d.day, 'YYYY-MM-DD') AS date,
  coalesce(count(c.id), 0) AS cnt,
  coalesce(sum(c.prompt_tokens), 0) AS p_sum,
  coalesce(sum(c.completion_tokens), 0) AS c_sum
FROM generate_series(
       date_trunc('day', now() AT TIME ZONE $1) - make_interval(days => $2)::interval,
       date_trunc('day', now() AT TIME ZONE $1),
       '1 day'::interval
     ) AS d(day)
LEFT JOIN llm_conversation c
  ON (c.created_at AT TIME ZONE $1)::date = d.day::date
GROUP BY d.day ORDER BY d.day`
	rows, err := s.pool.Query(ctx, sql, timezone, days-1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrendPoint
	for rows.Next() {
		var p TrendPoint
		if err := rows.Scan(&p.Date, &p.Count, &p.PromptTokens, &p.CompletionTokens); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DimensionCount 维度分组统计(model/caller 等)。
type DimensionCount struct {
	Key    string `json:"key"`
	Count  int64  `json:"count"`
	Tokens int64  `json:"tokens"`
}

// TopByDimension 按 model 或 caller_tag 分组 Top N。
func (s *Store) TopByDimension(ctx context.Context, dimension string, limit int) ([]DimensionCount, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	// dimension 白名单(防注入)
	col := "model"
	switch dimension {
	case "caller":
		col = "caller_tag"
	case "group":
		col = "caller_group"
	case "endpoint":
		col = "endpoint"
	default:
		col = "model"
	}
	sql := fmt.Sprintf(`
SELECT coalesce(%s,'unknown') AS k, count(*) AS c, coalesce(sum(prompt_tokens + completion_tokens), 0) AS t
FROM llm_conversation GROUP BY %s ORDER BY c DESC LIMIT %d`, col, col, limit)
	rows, err := s.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DimensionCount
	for rows.Next() {
		var d DimensionCount
		if err := rows.Scan(&d.Key, &d.Count, &d.Tokens); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SystemPromptSummary 知识资产层列表项。
type SystemPromptSummary struct {
	Hash      string    `json:"hash"`
	AgentName string    `json:"agent_name"`
	UseCount  int64     `json:"use_count"`
	Size      int       `json:"content_size"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

// ListSystemPrompts 知识资产层列表(分页, 按 use_count 排序)。
func (s *Store) ListSystemPrompts(ctx context.Context, page, size int) ([]SystemPromptSummary, int64, error) {
	if page < 1 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 20
	}
	var total int64
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM system_prompts").Scan(&total); err != nil {
		return nil, 0, err
	}
	sql := fmt.Sprintf(`
SELECT hash, coalesce(agent_name,'unknown'), use_count, content_size, first_seen, last_seen
FROM system_prompts ORDER BY use_count DESC LIMIT %d OFFSET %d`, size, (page-1)*size)
	rows, err := s.pool.Query(ctx, sql)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []SystemPromptSummary
	for rows.Next() {
		var sp SystemPromptSummary
		if err := rows.Scan(&sp.Hash, &sp.AgentName, &sp.UseCount, &sp.Size, &sp.FirstSeen, &sp.LastSeen); err != nil {
			return nil, 0, err
		}
		out = append(out, sp)
	}
	return out, total, rows.Err()
}

// GetSystemPrompt 查询单条 system prompt 完整内容。
func (s *Store) GetSystemPrompt(ctx context.Context, hash string) (content, agentName string, useCount int64, err error) {
	const sql = `SELECT content, coalesce(agent_name,'unknown'), use_count FROM system_prompts WHERE hash = $1`
	err = s.pool.QueryRow(ctx, sql, hash).Scan(&content, &agentName, &useCount)
	return
}

// itoa 简单整数转字符串(避免引入 strconv 到查询路径的歧义, 实际可直接用 fmt 或 strconv)。
func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}
