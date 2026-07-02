import { useEffect, useState } from 'react'
import { Table, Tag, Drawer, Spin, message, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import dayjs from 'dayjs'
import { useTheme } from '../theme'
import { api, type SystemPromptSummary } from '../api/client'

const { Paragraph, Text } = Typography

export default function Knowledge() {
  const { mode } = useTheme()
  const [loading, setLoading] = useState(false)
  const [data, setData] = useState<SystemPromptSummary[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [size, setSize] = useState(20)
  const [content, setContent] = useState<string>('')
  const [agentName, setAgentName] = useState('')
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [detailLoading, setDetailLoading] = useState(false)

  useEffect(() => {
    setLoading(true)
    api.configs({ page, size })
      .then(resp => {
        setData(resp.list || [])
        setTotal(resp.total)
      })
      .catch(e => message.error('加载失败: ' + e.message))
      .finally(() => setLoading(false))
  }, [page, size])

  const showContent = (hash: string) => {
    setDrawerOpen(true)
    setDetailLoading(true)
    api.config(hash)
      .then(resp => {
        setContent(resp.data.content)
        setAgentName(resp.data.agent_name)
      })
      .catch(e => message.error('加载失败: ' + e.message))
      .finally(() => setDetailLoading(false))
  }

  const columns: ColumnsType<SystemPromptSummary> = [
    {
      title: 'Agent 名称', dataIndex: 'agent_name', ellipsis: true,
      render: (v: string) => v || <Text type="secondary">(未识别)</Text>,
    },
    {
      title: 'Hash', dataIndex: 'hash', width: 140, ellipsis: true,
      render: (v: string) => <Text code style={{ fontSize: 11 }}>{v.substring(0, 16)}...</Text>,
    },
    {
      title: '使用次数', dataIndex: 'use_count', width: 90,
      render: (v: number) => <Tag color={v > 10 ? 'green' : v > 1 ? 'blue' : 'default'}>{v}</Tag>,
    },
    {
      title: '大小', dataIndex: 'content_size', width: 80,
      render: (v: number) => v > 1024 ? `${(v / 1024).toFixed(1)} KB` : `${v} B`,
    },
    {
      title: '首次出现', dataIndex: 'first_seen', width: 150,
      render: (v: string) => dayjs(v).format('MM-DD HH:mm'),
    },
    {
      title: '最近使用', dataIndex: 'last_seen', width: 150,
      render: (v: string) => dayjs(v).format('MM-DD HH:mm'),
    },
    {
      title: '操作', width: 70,
      render: (_, r) => <a onClick={() => showContent(r.hash)}>查看</a>,
    },
  ]

  return (
    <div>
      <Paragraph type="secondary" style={{ marginBottom: 16 }}>
        Agent 配置资产库 — 从对话流量自动沉淀的 system prompt, 按 hash 去重存储。
      </Paragraph>

      <Table
        rowKey="hash"
        loading={loading}
        dataSource={data}
        columns={columns}
        size="small"
        pagination={{
          current: page,
          pageSize: size,
          total,
          showTotal: t => `共 ${t} 条`,
          onChange: (p, s) => { setPage(p); setSize(s) },
        }}
      />

      <Drawer
        title={`System Prompt: ${agentName}`}
        open={drawerOpen}
        onClose={() => { setDrawerOpen(false); setContent('') }}
        width={640}
      >
        {detailLoading ? <Spin /> : (
          <pre style={{
            background: mode === 'dark' ? '#0F0F11' : '#1E1E1E',
            color: '#d4d4d4', padding: 16,
            borderRadius: 'var(--radius-sm)', overflow: 'auto', fontSize: 13,
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
