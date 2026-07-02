import { useEffect, useState, useCallback } from 'react'
import { Table, Input, Space, Tag, Drawer, Descriptions, Spin, message, Button, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import dayjs from 'dayjs'
import { useTheme } from '../theme'
import { api, type ConversationSummary, type ConversationDetail } from '../api/client'

const { Text } = Typography

export default function Conversations() {
  const [loading, setLoading] = useState(false)
  const [data, setData] = useState<ConversationSummary[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [size, setSize] = useState(20)
  const [model, setModel] = useState('')
  const [caller, setCaller] = useState('')
  const [detail, setDetail] = useState<ConversationDetail | null>(null)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [detailLoading, setDetailLoading] = useState(false)

  const load = useCallback(() => {
    setLoading(true)
    api.conversations({ page, size, model, caller })
      .then(resp => {
        setData(resp.list || [])
        setTotal(resp.total)
      })
      .catch(e => message.error('加载失败: ' + e.message))
      .finally(() => setLoading(false))
  }, [page, size, model, caller])

  useEffect(() => { load() }, [load])

  const showDetail = (id: number) => {
    setDrawerOpen(true)
    setDetailLoading(true)
    api.conversation(id)
      .then(resp => setDetail(resp.data))
      .catch(e => message.error('加载详情失败: ' + e.message))
      .finally(() => setDetailLoading(false))
  }

  const columns: ColumnsType<ConversationSummary> = [
    { title: 'ID', dataIndex: 'id', width: 80 },
    {
      title: '时间', dataIndex: 'created_at', width: 160,
      render: (v: string) => dayjs(v).format('MM-DD HH:mm:ss'),
    },
    { title: '模型', dataIndex: 'model', width: 120, render: (v: string) => v || '-' },
    { title: '端点', dataIndex: 'endpoint', width: 140 },
    { title: '调用方', dataIndex: 'caller_tag', ellipsis: true },
    {
      title: '流式', dataIndex: 'is_stream', width: 60,
      render: (v: boolean) => v ? <Tag color="blue">流</Tag> : <Tag>非</Tag>,
    },
    {
      title: '状态', dataIndex: 'http_status', width: 70,
      render: (v: number) => <Tag color={v < 400 ? 'green' : 'red'}>{v}</Tag>,
    },
    {
      title: 'P.Tok', dataIndex: 'prompt_tokens', width: 80,
      render: (v: number) => v || '-',
    },
    {
      title: 'C.Tok', dataIndex: 'completion_tokens', width: 80,
      render: (v: number) => v || '-',
    },
    {
      title: '延迟', dataIndex: 'upstream_latency_ms', width: 80,
      render: (v: number) => v ? `${v}ms` : '-',
    },
    {
      title: '截断', dataIndex: 'truncated', width: 60,
      render: (v: boolean) => v ? <Tag color="orange">截</Tag> : '',
    },
    {
      title: '操作', width: 70, fixed: 'right',
      render: (_, r) => <a onClick={() => showDetail(r.id)}>详情</a>,
    },
  ]

  return (
    <div>
      <Space style={{ marginBottom: 16 }}>
        <Input.Search
          placeholder="模型筛选"
          allowClear
          style={{ width: 160 }}
          onSearch={v => { setModel(v); setPage(1) }}
        />
        <Input.Search
          placeholder="调用方筛选"
          allowClear
          style={{ width: 200 }}
          onSearch={v => { setCaller(v); setPage(1) }}
        />
        <Button onClick={load}>刷新</Button>
      </Space>

      <Table
        rowKey="id"
        loading={loading}
        dataSource={data}
        columns={columns}
        scroll={{ x: 1200 }}
        size="small"
        pagination={{
          current: page,
          pageSize: size,
          total,
          showSizeChanger: true,
          showTotal: t => `共 ${t} 条`,
          onChange: (p, s) => { setPage(p); setSize(s) },
        }}
      />

      <Drawer
        title={`对话详情 #${detail?.id || ''}`}
        open={drawerOpen}
        onClose={() => { setDrawerOpen(false); setDetail(null) }}
        width={720}
      >
        {detailLoading ? <Spin /> : detail && (
          <>
            <Descriptions size="small" column={2} bordered style={{ marginBottom: 16 }}>
              <Descriptions.Item label="模型">{detail.model || '-'}</Descriptions.Item>
              <Descriptions.Item label="端点">{detail.endpoint}</Descriptions.Item>
              <Descriptions.Item label="调用方">{detail.caller_tag}</Descriptions.Item>
              <Descriptions.Item label="状态">
                <Tag color={detail.http_status < 400 ? 'green' : 'red'}>{detail.http_status}</Tag>
              </Descriptions.Item>
              <Descriptions.Item label="Prompt Tokens">{detail.prompt_tokens}</Descriptions.Item>
              <Descriptions.Item label="Completion Tokens">{detail.completion_tokens}</Descriptions.Item>
              <Descriptions.Item label="延迟">{detail.upstream_latency_ms}ms</Descriptions.Item>
              <Descriptions.Item label="时间">{dayjs(detail.created_at).format('YYYY-MM-DD HH:mm:ss')}</Descriptions.Item>
              {detail.system_prompt_hash && (
                <Descriptions.Item label="System Hash" span={2}>
                  {detail.system_prompt_hash.substring(0, 16)}...
                </Descriptions.Item>
              )}
              {detail.error_message && (
                <Descriptions.Item label="错误" span={2}>{detail.error_message}</Descriptions.Item>
              )}
            </Descriptions>

            <h4>Prompt</h4>
            <pre style={{ background: 'var(--bg-hover)', padding: 14, borderRadius: 'var(--radius-sm)', maxHeight: 240, overflow: 'auto', fontSize: 12, border: '1px solid var(--border-color)' }}>
              {JSON.stringify(detail.prompt_text, null, 2)}
            </pre>

            <h4>Completion</h4>
            <pre style={{ background: 'var(--bg-hover)', padding: 14, borderRadius: 'var(--radius-sm)', maxHeight: 240, overflow: 'auto', fontSize: 12, border: '1px solid var(--border-color)' }}>
              {JSON.stringify(detail.completion_text, null, 2)}
            </pre>

            {detail.tool_calls && (
              <>
                <h4>Tool Calls</h4>
                <pre style={{ background: 'var(--bg-hover)', padding: 14, borderRadius: 'var(--radius-sm)', maxHeight: 200, overflow: 'auto', fontSize: 12, border: '1px solid var(--border-color)' }}>
                  {JSON.stringify(detail.tool_calls, null, 2)}
                </pre>
              </>
            )}
          </>
        )}
      </Drawer>
    </div>
  )
}
