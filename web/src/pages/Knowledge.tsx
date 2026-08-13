import { useEffect, useState } from 'react'
import { Tabs, Table, Tag, Drawer, Spin, message, Typography, Button, Input, Space } from 'antd'
import { ReloadOutlined, SearchOutlined, CodeOutlined, FileTextOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import dayjs from 'dayjs'
import { useTheme } from '../theme'
import { api, type SystemPromptSummary, type KnowledgeStats, type KnowledgePair, type KnowledgePairStats } from '../api/client'
import { Panel, SectionHeader, MiniStat, EmptyPanel } from '../components/Common'

const { Text, Paragraph } = Typography

// ============ Tab 1: 问答知识(知识库核心) ============

function QAPairs() {
  const [loading, setLoading] = useState(false)
  const [data, setData] = useState<KnowledgePair[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [size, setSize] = useState(10)
  const [q, setQ] = useState('')
  const [searchInput, setSearchInput] = useState('')
  const [stats, setStats] = useState<KnowledgePairStats | null>(null)
  const [detail, setDetail] = useState<KnowledgePair | null>(null)
  const [drawerOpen, setDrawerOpen] = useState(false)

  const load = (query = q, p = page, s = size) => {
    setLoading(true)
    Promise.all([api.searchKnowledge({ q: query, page: p, size: s }), api.knowledgePairStats()])
      .then(([resp, st]) => {
        setData(resp.list || []); setTotal(resp.total)
        setStats(st.data)
      }).catch(e => message.error('加载失败: ' + e.message))
      .finally(() => setLoading(false))
  }

  useEffect(() => { load() }, []) // 首次加载

  const onSearch = () => {
    setQ(searchInput)
    setPage(1)
    load(searchInput, 1, size)
  }

  const showDetail = (r: KnowledgePair) => { setDetail(r); setDrawerOpen(true) }

  const columns: ColumnsType<KnowledgePair> = [
    { title: '问题', dataIndex: 'question', width: '38%',
      render: (v: string, r) => (
        <a style={{ fontSize: 14, fontWeight: 500 }} onClick={() => showDetail(r)}>
          {v.length > 80 ? v.slice(0, 80) + '…' : v}
        </a>
      ) },
    { title: '知识特征', width: 130,
      render: (_, r) => (
        <Space size={4}>
          {r.code_blocks > 0 && <Tag icon={<CodeOutlined />} color="geekblue" style={{ fontSize: 12 }}>{r.code_blocks} 代码块</Tag>}
          {r.file_paths?.length > 0 && <Tag icon={<FileTextOutlined />} color="green" style={{ fontSize: 12 }}>{r.file_paths.length} 文件</Tag>}
        </Space>
      ) },
    { title: '回答长度', dataIndex: 'answer', width: 90,
      render: (v: string) => <Text style={{ fontSize: 13, color: 'var(--text-tertiary)' }}>{v.length} 字</Text> },
    { title: '模型', dataIndex: 'model', width: 100, ellipsis: true,
      render: (v: string) => <Text style={{ fontSize: 13 }}>{v || '-'}</Text> },
    { title: '时间', dataIndex: 'created_at', width: 110,
      render: (v: string) => <Text style={{ fontSize: 13 }}>{dayjs(v).format('MM-DD HH:mm')}</Text> },
  ]

  return (
    <div>
      {stats && (
        <Panel style={{ marginBottom: 16 }}>
          <SectionHeader title="知识问答库" subtitle="从真实对话自动提炼的问题→解法 问答对"
            extra={<Button size="small" type="text" icon={<ReloadOutlined />} onClick={() => load()} style={{ color: 'var(--text-tertiary)' }} />} />
          <div style={{ display: 'flex', gap: 32, flexWrap: 'wrap', marginBottom: 16 }}>
            <MiniStat label="问答对" value={stats.total} />
            <MiniStat label="含代码解法" value={stats.with_code} />
            <MiniStat label="涉及文件" value={stats.with_files} />
            <MiniStat label="平均回答长度" value={`${stats.avg_answer_len}字`} />
            <MiniStat label="最早沉淀" value={stats.oldest ? dayjs(stats.oldest).format('MM-DD') : '-'} color="var(--green)" />
          </div>

          {/* 搜索框 */}
          <Input.Search
            placeholder="搜索历史问题/解法, 如: 物流匹配、取消流程、goroutine..."
            enterButton={<span><SearchOutlined /> 搜索</span>}
            size="large"
            value={searchInput}
            onChange={e => setSearchInput(e.target.value)}
            onSearch={onSearch}
            allowClear
            loading={loading}
            style={{ maxWidth: 560, marginBottom: 16 }}
          />
          {q && (
            <Paragraph type="secondary" style={{ fontSize: 13, margin: 0 }}>
              搜索 “<Text strong>{q}</Text>” · 共 {total} 条结果
            </Paragraph>
          )}
        </Panel>
      )}

      <div className="solid-card">
        {total === 0 && !loading ? (
          <EmptyPanel text={q ? '没有匹配的知识 — 换个关键词试试' : '知识库暂无问答对 — 新对话会在每日 02:30 自动提炼'} />
        ) : (
          <Table rowKey="id" loading={loading} dataSource={data} columns={columns} size="small"
            style={{ padding: '8px 0' }}
            pagination={{
              current: page, pageSize: size, total, size: 'small', showTotal: t => `${t} 条`,
              onChange: (p, s) => { setPage(p); setSize(s); load(q, p, s) },
            }}
          />
        )}
      </div>

      {/* 详情抽屉: 问题 + 完整回答 */}
      <Drawer title={<Text strong style={{ fontSize: 15 }}>{detail?.question}</Text>}
        open={drawerOpen} onClose={() => { setDrawerOpen(false); setDetail(null) }} width={720}>
        {detail && (
          <div>
            <Space size={8} style={{ marginBottom: 16 }} wrap>
              {detail.code_blocks > 0 && <Tag icon={<CodeOutlined />} color="geekblue">{detail.code_blocks} 个代码块</Tag>}
              {detail.file_paths?.map((f, i) => <Tag key={i} color="green">{f}</Tag>)}
              <Tag>{detail.model}</Tag>
              <Tag color="default">{dayjs(detail.created_at).format('YYYY-MM-DD HH:mm')}</Tag>
            </Space>
            <div style={{
              background: 'var(--bg-page)', border: '1px solid var(--border-color)',
              borderRadius: 8, padding: 20, fontSize: 14, lineHeight: 1.8,
              color: 'var(--text-primary)', whiteSpace: 'pre-wrap', maxHeight: '72vh', overflow: 'auto',
            }}>
              {detail.answer}
            </div>
          </div>
        )}
      </Drawer>
    </div>
  )
}

// ============ Tab 2: Agent 配置库(保留原功能) ============

function AgentConfigs() {
  const { mode } = useTheme()
  const [loading, setLoading] = useState(false)
  const [data, setData] = useState<SystemPromptSummary[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [size, setSize] = useState(20)
  const [stats, setStats] = useState<KnowledgeStats | null>(null)
  const [content, setContent] = useState<string>('')
  const [agentName, setAgentName] = useState('')
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [detailLoading, setDetailLoading] = useState(false)

  const load = () => {
    setLoading(true)
    Promise.all([api.configs({ page, size }), api.knowledgeStats()])
      .then(([resp, st]) => {
        setData(resp.list || []); setTotal(resp.total)
        setStats(st.data)
      }).catch(e => message.error('加载失败: ' + e.message))
      .finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [page, size])

  const showContent = (hash: string) => {
    setDrawerOpen(true); setDetailLoading(true)
    api.config(hash)
      .then(resp => { setContent(resp.data.content); setAgentName(resp.data.agent_name) })
      .catch(e => message.error('加载失败: ' + e.message))
      .finally(() => setDetailLoading(false))
  }

  const columns: ColumnsType<SystemPromptSummary> = [
    { title: 'Agent', dataIndex: 'agent_name', ellipsis: true,
      render: (v: string) => <Text style={{ fontSize: 14, fontWeight: 500 }}>{v || <Text type="secondary">未识别</Text>}</Text> },
    { title: 'Hash', dataIndex: 'hash', width: 130, ellipsis: true,
      render: (v: string) => <Text code style={{ fontSize: 12 }}>{v.slice(0, 16)}…</Text> },
    { title: '使用', dataIndex: 'use_count', width: 80,
      render: (v: number) => <Tag color={v > 10 ? 'success' : v > 1 ? 'processing' : 'default'} style={{ fontSize: 12 }}>{v}</Tag> },
    { title: '大小', dataIndex: 'content_size', width: 80,
      render: (v: number) => <Text style={{ fontSize: 13, color: 'var(--text-tertiary)' }}>{v > 1024 ? `${(v/1024).toFixed(1)}KB` : `${v}B`}</Text> },
    { title: '首次出现', dataIndex: 'first_seen', width: 130,
      render: (v: string) => <Text style={{ fontSize: 13 }}>{dayjs(v).format('MM-DD HH:mm')}</Text> },
    { title: '最近使用', dataIndex: 'last_seen', width: 130,
      render: (v: string) => <Text style={{ fontSize: 13 }}>{dayjs(v).format('MM-DD HH:mm')}</Text> },
    { title: '', width: 60,
      render: (_, r) => <a style={{ fontSize: 13 }} onClick={() => showContent(r.hash)}>查看</a> },
  ]

  return (
    <div>
      {stats && (
        <Panel style={{ marginBottom: 16 }}>
          <SectionHeader title="Agent 配置资产" subtitle="System Prompt 配置库, 按内容指纹去重"
            extra={<Button size="small" type="text" icon={<ReloadOutlined />} onClick={load} style={{ color: 'var(--text-tertiary)' }} />} />
          <div style={{ display: 'flex', gap: 32, flexWrap: 'wrap' }}>
            <MiniStat label="配置总数" value={stats.total_configs} />
            <MiniStat label="累计使用" value={stats.total_usage} />
            <MiniStat label="独立 Agent" value={stats.unique_agents} />
            <MiniStat label="平均大小" value={stats.avg_config_size > 1024 ? `${(stats.avg_config_size/1024).toFixed(1)}KB` : `${stats.avg_config_size}B`} />
            <MiniStat label="最活跃 Agent" value={stats.top_agent?.slice(0, 20) || '-'} color="var(--blue)" />
          </div>
        </Panel>
      )}

      <div className="solid-card">
        {total === 0 && !loading ? (
          <EmptyPanel text="暂无 System Prompt 配置 — 含 system 的对话会自动沉淀到这里" />
        ) : (
          <Table rowKey="hash" loading={loading} dataSource={data} columns={columns} size="small"
            style={{ padding: '8px 0' }}
            pagination={{
              current: page, pageSize: size, total, size: 'small', showTotal: t => `${t} 条`,
              onChange: (p, s) => { setPage(p); setSize(s) },
            }}
          />
        )}
      </div>

      <Drawer title={<Text strong style={{ fontSize: 14 }}>{agentName}</Text>}
        open={drawerOpen} onClose={() => { setDrawerOpen(false); setContent('') }} width={660}>
        {detailLoading ? <div style={{ textAlign: 'center', padding: 60 }}><Spin /></div> : (
          <pre style={{
            background: '#0F0F11', color: '#d4d4d4', padding: 16,
            borderRadius: 6, overflow: 'auto', fontSize: 15,
            fontFamily: 'Consolas, Monaco, monospace', whiteSpace: 'pre-wrap',
            maxHeight: '78vh', border: '1px solid var(--border-color)',
          }}>
            {content}
          </pre>
        )}
      </Drawer>
    </div>
  )
}

// ============ 主页面: 双 Tab ============

export default function Knowledge() {
  return (
    <Tabs
      defaultActiveKey="qa"
      items={[
        {
          key: 'qa',
          label: (
            <span style={{ fontSize: 15 }}>
              <SearchOutlined style={{ marginRight: 6 }} />
              问答知识
            </span>
          ),
          children: <QAPairs />,
        },
        {
          key: 'configs',
          label: (
            <span style={{ fontSize: 15 }}>
              <FileTextOutlined style={{ marginRight: 6 }} />
              Agent 配置
            </span>
          ),
          children: <AgentConfigs />,
        },
      ]}
      style={{ fontSize: 14 }}
    />
  )
}
