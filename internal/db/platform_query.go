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

// --- 运维监控查询 ---

// DBStats 数据库表统计(运维监控用)。
type DBStats struct {
	ConvTableSize   string `json:"conv_table_size"`   // 人类可读
	ConvIndexSize   string `json:"conv_index_size"`
	SysPromptCount  int64  `json:"sys_prompt_count"`
	SysPromptSize   string `json:"sys_prompt_size"`
	LiveTuples      int64  `json:"live_tuples"`
	DeadTuples      int64  `json:"dead_tuples"`
	LastVacuum      string `json:"last_vacuum"`
	LastAnalyze     string `json:"last_analyze"`
	TotalDBSize     string `json:"total_db_size"`
}

// GetDBStats 返回数据库表大小/元组/维护状态。
func (s *Store) GetDBStats(ctx context.Context) (*DBStats, error) {
	const sql = `
SELECT
  pg_size_pretty(pg_total_relation_size('llm_conversation')),
  pg_size_pretty(pg_indexes_size('llm_conversation')),
  coalesce((SELECT count(*) FROM system_prompts), 0),
  pg_size_pretty(coalesce((SELECT pg_total_relation_size('system_prompts')), 0)),
  coalesce(s.n_live_tup, 0),
  coalesce(s.n_dead_tup, 0),
  coalesce(to_char(s.last_vacuum, 'YYYY-MM-DD HH24:MI'), 'never'),
  coalesce(to_char(s.last_analyze, 'YYYY-MM-DD HH24:MI'), 'never'),
  pg_size_pretty(pg_database_size(current_database()))
FROM pg_stat_user_tables s WHERE s.relname = 'llm_conversation'`
	var d DBStats
	err := s.pool.QueryRow(ctx, sql).Scan(
		&d.ConvTableSize, &d.ConvIndexSize, &d.SysPromptCount, &d.SysPromptSize,
		&d.LiveTuples, &d.DeadTuples, &d.LastVacuum, &d.LastAnalyze, &d.TotalDBSize)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// LatencyBucket 延迟分布区间。
type LatencyBucket struct {
	Bucket string `json:"bucket"`
	Count  int64  `json:"count"`
}

// LatencyDistribution 延迟分布(按区间统计)。
func (s *Store) LatencyDistribution(ctx context.Context) ([]LatencyBucket, error) {
	const sql = `
SELECT width_bucket(upstream_latency_ms, 0, 30000, 6) AS b, count(*) AS c
FROM llm_conversation WHERE upstream_latency_ms IS NOT NULL
GROUP BY b ORDER BY b`
	rows, err := s.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := []string{"<5s", "5-10s", "10-15s", "15-20s", "20-25s", "25-30s"}
	out := make([]LatencyBucket, 0, 6)
	for rows.Next() {
		var b, c int
		if err := rows.Scan(&b, &c); err != nil {
			return nil, err
		}
		label := "30s+"
		if b >= 1 && b <= 6 {
			label = labels[b-1]
		}
		out = append(out, LatencyBucket{Bucket: label, Count: int64(c)})
	}
	return out, rows.Err()
}

// DataQuality 数据质量指标。
type DataQuality struct {
	Total         int64 `json:"total"`
	WithUsage     int64 `json:"with_usage"`     // prompt_tokens>0
	WithCaller    int64 `json:"with_caller"`    // caller_tag非空
	WithSysPrompt int64 `json:"with_sys_prompt"`
	Truncated     int64 `json:"truncated"`
	Errors        int64 `json:"errors"`
	StreamPct     int   `json:"stream_pct"`     // 流式占比
}

// GetDataQuality 数据完整性统计(检测采集质量)。
func (s *Store) GetDataQuality(ctx context.Context) (*DataQuality, error) {
	const sql = `
SELECT count(*),
  count(*) FILTER (WHERE prompt_tokens > 0),
  count(*) FILTER (WHERE caller_tag IS NOT NULL),
  count(*) FILTER (WHERE system_prompt_hash IS NOT NULL),
  count(*) FILTER (WHERE truncated = true),
  count(*) FILTER (WHERE http_status >= 400),
  coalesce(avg(CASE WHEN is_stream THEN 100 ELSE 0 END), 0)::int
FROM llm_conversation`
	var d DataQuality
	err := s.pool.QueryRow(ctx, sql).Scan(
		&d.Total, &d.WithUsage, &d.WithCaller, &d.WithSysPrompt,
		&d.Truncated, &d.Errors, &d.StreamPct)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// KnowledgeStats 知识库统计。
type KnowledgeStats struct {
	TotalConfigs   int64 `json:"total_configs"`
	TotalUsage     int64 `json:"total_usage"`
	UniqueAgents   int64 `json:"unique_agents"`
	TopAgent       string `json:"top_agent"`
	TopAgentUses   int64 `json:"top_agent_uses"`
	AvgConfigSize  int   `json:"avg_config_size"`
}

// GetKnowledgeStats 知识资产层汇总。
func (s *Store) GetKnowledgeStats(ctx context.Context) (*KnowledgeStats, error) {
	const sql = `
SELECT count(*),
  coalesce(sum(use_count), 0),
  count(DISTINCT agent_name),
  coalesce((SELECT agent_name FROM system_prompts ORDER BY use_count DESC LIMIT 1), ''),
  coalesce((SELECT use_count FROM system_prompts ORDER BY use_count DESC LIMIT 1), 0),
  coalesce(avg(content_size), 0)::int
FROM system_prompts`
	var k KnowledgeStats
	err := s.pool.QueryRow(ctx, sql).Scan(
		&k.TotalConfigs, &k.TotalUsage, &k.UniqueAgents,
		&k.TopAgent, &k.TopAgentUses, &k.AvgConfigSize)
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// EndpointStats 端点分布。
type EndpointStat struct {
	Endpoint string `json:"endpoint"`
	Count    int64  `json:"count"`
	StreamPct int   `json:"stream_pct"`
}

// EndpointDistribution 按端点统计。
func (s *Store) EndpointDistribution(ctx context.Context) ([]EndpointStat, error) {
	const sql = `
SELECT endpoint, count(*),
  coalesce(avg(CASE WHEN is_stream THEN 100 ELSE 0 END), 0)::int
FROM llm_conversation GROUP BY endpoint ORDER BY count(*) DESC`
	rows, err := s.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EndpointStat
	for rows.Next() {
		var e EndpointStat
		if err := rows.Scan(&e.Endpoint, &e.Count, &e.StreamPct); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// HourlyDistribution 按小时分布(发现高峰时段)。
type HourlyPoint struct {
	Hour  int   `json:"hour"`
	Count int64 `json:"count"`
}

// HourlyTrend 24 小时分布(按 timezone)。
func (s *Store) HourlyTrend(ctx context.Context, timezone string) ([]HourlyPoint, error) {
	const sql = `
SELECT extract(hour FROM created_at AT TIME ZONE $1)::int AS h, count(*) AS c
FROM llm_conversation
WHERE created_at > now() - interval '24 hours'
GROUP BY h ORDER BY h`
	rows, err := s.pool.Query(ctx, sql, timezone)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 初始化 24 小时
	hourMap := make(map[int]int64)
	for i := 0; i < 24; i++ {
		hourMap[i] = 0
	}
	for rows.Next() {
		var h int
		var c int64
		if err := rows.Scan(&h, &c); err != nil {
			return nil, err
		}
		hourMap[h] = c
	}
	out := make([]HourlyPoint, 0, 24)
	for i := 0; i < 24; i++ {
		out = append(out, HourlyPoint{Hour: i, Count: hourMap[i]})
	}
	return out, rows.Err()
}

// TokenEfficiency token 效率(按 model)。
type TokenEfficiency struct {
	Model           string  `json:"model"`
	Count           int64   `json:"count"`
	AvgPrompt       float64 `json:"avg_prompt"`
	AvgCompletion   float64 `json:"avg_completion"`
	AvgLatency      float64 `json:"avg_latency"`
}

// ModelTokenStats 按 model 统计 token 效率 + 延迟。
func (s *Store) ModelTokenStats(ctx context.Context) ([]TokenEfficiency, error) {
	const sql = `
SELECT coalesce(model, '(空)'),
  count(*),
  coalesce(avg(prompt_tokens), 0),
  coalesce(avg(completion_tokens), 0),
  coalesce(avg(upstream_latency_ms), 0)
FROM llm_conversation WHERE model != ''
GROUP BY model ORDER BY count(*) DESC`
	rows, err := s.pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TokenEfficiency
	for rows.Next() {
		var t TokenEfficiency
		if err := rows.Scan(&t.Model, &t.Count, &t.AvgPrompt, &t.AvgCompletion, &t.AvgLatency); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// itoa 简单整数转字符串。
func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

// ExportConversations 导出对话(最多 limit 条, 用于 CSV/JSON 导出)。
func (s *Store) ExportConversations(ctx context.Context, f ConversationFilter, limit int) ([]ConversationSummary, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	f.Page = 1
	f.Size = limit
	list, _, err := s.ListConversations(ctx, f)
	return list, err
}
