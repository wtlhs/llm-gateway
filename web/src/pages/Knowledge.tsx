import { useEffect, useState } from 'react'
import { Table, Tag, Drawer, Spin, message, Typography, Button } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import dayjs from 'dayjs'
import { useTheme } from '../theme'
import { api, type SystemPromptSummary, type KnowledgeStats } from '../api/client'
import { Panel, SectionHeader, MiniStat, EmptyPanel } from '../components/Common'

const { Text, Paragraph } = Typography

export default function Knowledge() {
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
      {/* 统计卡片 */}
      {stats && (
        <Panel style={{ marginBottom: 16 }}>
          <SectionHeader title="知识资产总览" subtitle="System Prompt 配置库"
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

      <Paragraph type="secondary" style={{ fontSize: 13, marginBottom: 12 }}>
        从对话流量自动沉淀的 Agent 配置, 按 hash 去重存储。同一 Agent 的多次请求只存一份配置。
      </Paragraph>

      {/* 列表 */}
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
