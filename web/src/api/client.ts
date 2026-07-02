// API 客户端: 封装所有后端请求。
// Token 从 localStorage 读取(首次访问时让用户输入, 或 URL ?token=xxx 带入)。

const TOKEN_KEY = 'platform_token'

export function getToken(): string {
  return localStorage.getItem(TOKEN_KEY) || ''
}

export function setToken(token: string) {
  localStorage.setItem(TOKEN_KEY, token)
}

async function request<T>(path: string, params?: Record<string, any>): Promise<T> {
  const url = new URL(path, window.location.origin)
  if (params) {
    Object.entries(params).forEach(([k, v]) => {
      if (v !== undefined && v !== null && v !== '') {
        url.searchParams.set(k, String(v))
      }
    })
  }
  const headers: Record<string, string> = {}
  const token = getToken()
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }
  const resp = await fetch(url.toString(), { headers })
  if (!resp.ok) {
    const text = await resp.text()
    throw new Error(`${resp.status}: ${text}`)
  }
  const json = await resp.json()
  return json
}

// --- 类型定义 ---

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

export interface DimensionCount {
  key: string
  count: number
  tokens: number
}

export interface ConversationSummary {
  id: number
  model: string
  endpoint: string
  caller_tag: string
  is_stream: boolean
  http_status: number
  prompt_tokens: number
  completion_tokens: number
  upstream_latency_ms: number
  system_prompt_hash?: string
  truncated: boolean
  created_at: string
}

export interface ConversationDetail {
  id: number
  model: string
  endpoint: string
  caller_tag: string
  is_stream: boolean
  http_status: number
  prompt_text: any
  completion_text: any
  tool_calls?: any
  prompt_tokens: number
  completion_tokens: number
  error_message?: string
  upstream_latency_ms: number
  system_prompt_hash?: string
  client_ip?: string
  created_at: string
}

export interface ListResp<T> {
  code: number
  total: number
  page: number
  size: number
  list: T[]
}

export interface SystemPromptSummary {
  hash: string
  agent_name: string
  use_count: number
  content_size: number
  first_seen: string
  last_seen: string
}

// --- API 方法 ---

export const api = {
  overview: () => request<{ data: Overview }>('/api/v1/dashboard/overview'),
  trend: (days: number) => request<{ data: TrendPoint[] }>('/api/v1/dashboard/trend', { days }),
  topModels: (limit: number) => request<{ data: DimensionCount[] }>('/api/v1/dashboard/top-models', { limit }),
  topCallers: (limit: number) => request<{ data: DimensionCount[] }>('/api/v1/dashboard/top-callers', { limit }),

  conversations: (params: { page?: number; size?: number; model?: string; caller?: string }) =>
    request<ListResp<ConversationSummary>>('/api/v1/conversations', params),
  conversation: (id: number) => request<{ data: ConversationDetail }>(`/api/v1/conversations/${id}`),

  configs: (params: { page?: number; size?: number }) =>
    request<ListResp<SystemPromptSummary>>('/api/v1/knowledge/configs', params),
  config: (hash: string) => request<{ data: any }>(`/api/v1/knowledge/configs/${hash}`),
}
