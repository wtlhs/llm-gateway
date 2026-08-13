// API 客户端: 封装所有后端请求。
// 鉴权: 后端下发 httpOnly cookie, 前端无需手动管理 token;
// 只需带 credentials: 'same-origin' 让浏览器自动携带 cookie。
// 401 时跳转登录页。

async function request<T>(path: string, params?: Record<string, any>): Promise<T> {
  const url = new URL(path, window.location.origin)
  if (params) {
    Object.entries(params).forEach(([k, v]) => {
      if (v !== undefined && v !== null && v !== '') {
        url.searchParams.set(k, String(v))
      }
    })
  }
  const resp = await fetch(url.toString(), { credentials: 'same-origin' })
  if (resp.status === 401) {
    // 会话失效/未登录 → 跳登录页
    if (window.location.pathname !== '/login') {
      window.location.href = '/login'
    }
    throw new Error('401: 未登录或会话已过期')
  }
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`${resp.status}: ${text}`)
  }
  const json = await resp.json()
  return json
}

// --- 认证 ---

export async function login(username: string, password: string): Promise<void> {
  const resp = await fetch('/api/v1/auth/login', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ username, password }),
  })
  if (!resp.ok) {
    const j = await resp.json().catch(() => ({}))
    throw new Error(j.error || `${resp.status}: 登录失败`)
  }
}

export async function logout(): Promise<void> {
  await fetch('/api/v1/auth/logout', { method: 'POST', credentials: 'same-origin' }).catch(() => {})
  window.location.href = '/login'
}

// 检查当前登录态(供路由守卫判断)
export async function checkAuth(): Promise<boolean> {
  try {
    const resp = await fetch('/api/v1/auth/me', { credentials: 'same-origin' })
    return resp.ok
  } catch {
    return false
  }
}

// --- 通用类型 ---

interface ApiResp<T> { code: number; data: T }

export interface ListResp<T> {
  code: number; total: number; page: number; size: number; list: T[]
}

// --- Dashboard ---

export interface Overview {
  total: number
  today_count: number
  prompt_tokens_sum: number
  completion_tokens_sum: number
  avg_latency_ms: number
  error_count: number
}

export interface TrendPoint {
  date: string
  count: number
  prompt_tokens: number
  completion_tokens: number
}

export interface DimensionCount { key: string; count: number; tokens: number }

export interface HourlyPoint { hour: number; count: number }

export interface EndpointStat { endpoint: string; count: number; stream_pct: number }

export interface ModelStat {
  model: string; count: number
  avg_prompt: number; avg_completion: number; avg_latency: number
}

// --- Conversations ---

export interface ConversationSummary {
  id: number; model: string; endpoint: string
  caller_tag: string; caller_name: string
  is_stream: boolean; http_status: number
  prompt_tokens: number; completion_tokens: number
  upstream_latency_ms: number; system_prompt_hash?: string
  truncated: boolean; created_at: string
}

export interface ConversationDetail {
  id: number; model: string; endpoint: string; caller_tag: string
  is_stream: boolean; http_status: number
  prompt_text: any; completion_text: any; tool_calls?: any
  prompt_tokens: number; completion_tokens: number
  error_message?: string; upstream_latency_ms: number
  system_prompt_hash?: string; client_ip?: string; created_at: string
}

// --- Knowledge ---

export interface SystemPromptSummary {
  hash: string; agent_name: string; use_count: number
  content_size: number; first_seen: string; last_seen: string
}

export interface KnowledgeStats {
  total_configs: number; total_usage: number; unique_agents: number
  top_agent: string; top_agent_uses: number; avg_config_size: number
}

// --- Ops ---

export interface DBStats {
  conv_table_size: string; conv_index_size: string
  sys_prompt_count: number; sys_prompt_size: string
  live_tuples: number; dead_tuples: number
  last_vacuum: string; last_analyze: string; total_db_size: string
}

export interface LatencyBucket { bucket: string; count: number }

export interface DataQuality {
  total: number; with_usage: number; with_caller: number
  with_sys_prompt: number; truncated: number; errors: number; stream_pct: number
}

// --- API 方法 ---

export const api = {
  // Dashboard
  overview: () => request<ApiResp<Overview>>('/api/v1/dashboard/overview'),
  trend: (days: number) => request<ApiResp<TrendPoint[]>>('/api/v1/dashboard/trend', { days }),
  topModels: (limit: number) => request<ApiResp<DimensionCount[]>>('/api/v1/dashboard/top-models', { limit }),
  topCallers: (limit: number) => request<ApiResp<DimensionCount[]>>('/api/v1/dashboard/top-callers', { limit }),
  hourly: () => request<ApiResp<HourlyPoint[]>>('/api/v1/dashboard/hourly'),
  endpoints: () => request<ApiResp<EndpointStat[]>>('/api/v1/dashboard/endpoints'),
  modelStats: () => request<ApiResp<ModelStat[]>>('/api/v1/dashboard/model-stats'),

  // Conversations
  conversations: (params: { page?: number; size?: number; model?: string; caller?: string; stream?: string }) =>
    request<ListResp<ConversationSummary>>('/api/v1/conversations', params),
  conversation: (id: number) => request<ApiResp<ConversationDetail>>(`/api/v1/conversations/${id}`),
  exportUrl: (params: { model?: string; caller?: string }) => {
    const url = new URL('/api/v1/conversations/export', window.location.origin)
    Object.entries(params).forEach(([k, v]) => { if (v) url.searchParams.set(k, v) })
    return url.toString()
  },

  // Knowledge
  configs: (params: { page?: number; size?: number }) =>
    request<ListResp<SystemPromptSummary>>('/api/v1/knowledge/configs', params),
  config: (hash: string) => request<ApiResp<any>>(`/api/v1/knowledge/configs/${hash}`),
  knowledgeStats: () => request<ApiResp<KnowledgeStats>>('/api/v1/knowledge/stats'),

  // Ops
  dbStats: () => request<ApiResp<DBStats>>('/api/v1/ops/db-stats'),
  dataQuality: () => request<ApiResp<DataQuality>>('/api/v1/ops/data-quality'),
  latency: () => request<ApiResp<LatencyBucket[]>>('/api/v1/ops/latency'),
}
