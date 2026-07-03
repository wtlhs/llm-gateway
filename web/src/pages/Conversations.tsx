import { useEffect, useState, useCallback } from 'react'
import { Table, Input, Space, Tag, Drawer, Descriptions, Spin, message, Button, Select, Tooltip, Typography } from 'antd'
import { ExportOutlined, ReloadOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import dayjs from 'dayjs'
import { api, type ConversationSummary, type ConversationDetail } from '../api/client'
import { SectionHeader } from '../components/Common'

const { Text } = Typography

export default function Conversations() {
  const [loading, setLoading] = useState(false)
  const [data, setData] = useState<ConversationSummary[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [size, setSize] = useState(20)
  const [model, setModel] = useState('')
  const [caller, setCaller] = useState('')
  const [streamFilter, setStreamFilter] = useState<string>('')
  const [statusFilter, setStatusFilter] = useState<string>('')
  const [detail, setDetail] = useState<ConversationDetail | null>(null)
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [detailLoading, setDetailLoading] = useState(false)

  const load = useCallback(() => {
    setLoading(true)
    api.conversations({ page, size, model, caller, stream: streamFilter })
      .then(resp => { setData(resp.list || []); setTotal(resp.total) })
      .catch(e => message.error('加载失败: ' + e.message))
      .finally(() => setLoading(false))
  }, [page, size, model, caller, streamFilter])

  useEffect(() => { load() }, [load])

  const showDetail = (id: number) => {
    setDrawerOpen(true); setDetailLoading(true)
    api.conversation(id)
      .then(resp => setDetail(resp.data))
      .catch(e => message.error('加载详情失败: ' + e.message))
      .finally(() => setDetailLoading(false))
  }

  const handleExport = () => {
    const url = api.exportUrl({ model, caller })
    window.open(url, '_blank')
    message.success('正在导出...')
  }

  const columns: ColumnsType<ConversationSummary> = [
    { title: 'ID', dataIndex: 'id', width: 75, fixed: 'left',
      render: (v: number) => <Text style={{ fontSize: 13, color: 'var(--text-tertiary)', fontVariantNumeric: 'tabular-nums' }}>{v}</Text> },
    { title: '时间', dataIndex: 'created_at', width: 145, fixed: 'left',
      render: (v: string) => <Text style={{ fontSize: 13 }}>{dayjs(v).format('MM-DD HH:mm:ss')}</Text> },
    { title: '模型', dataIndex: 'model', width: 110,
      render: (v: string) => <Text style={{ fontSize: 13 }}>{v || '-'}</Text> },
    { title: '端点', dataIndex: 'endpoint', width: 130,
      render: (v: string) => <Tag style={{ fontSize: 12 }}>{v}</Tag> },
    { title: '调用方', width: 150, ellipsis: true,
      render: (_, r) => {
        const name = r.caller_name || r.caller_tag || '-'
        const tip = r.caller_name && r.caller_tag ? `${r.caller_name}（令牌: ${r.caller_tag}）` : name
        return <Tooltip title={tip}><Text style={{ fontSize: 13 }}>{name}</Text></Tooltip>
      } },
    { title: '流', dataIndex: 'is_stream', width: 50,
      render: (v: boolean) => <Tag color={v ? 'blue' : 'default'} style={{ fontSize: 12 }}>{v ? '流' : '非'}</Tag> },
    { title: '状态', dataIndex: 'http_status', width: 65,
      render: (v: number) => <Tag color={v < 400 ? 'success' : 'error'} style={{ fontSize: 12 }}>{v}</Tag> },
    { title: 'P.Tok', dataIndex: 'prompt_tokens', width: 75,
      render: (v: number) => <Text style={{ fontSize: 13, color: v ? 'var(--text-primary)' : 'var(--text-tertiary)' }}>{v || '-'}</Text> },
    { title: 'C.Tok', dataIndex: 'completion_tokens', width: 75,
      render: (v: number) => <Text style={{ fontSize: 13, color: v ? 'var(--text-primary)' : 'var(--text-tertiary)' }}>{v || '-'}</Text> },
    { title: '延迟', dataIndex: 'upstream_latency_ms', width: 75,
      render: (v: number) => {
        const color = v > 20000 ? 'var(--red)' : v > 10000 ? 'var(--amber)' : undefined
        return <Text style={{ fontSize: 13, color, fontVariantNumeric: 'tabular-nums' }}>{v ? `${(v/1000).toFixed(1)}s` : '-'}</Text>
      } },
    { title: '截', dataIndex: 'truncated', width: 45,
      render: (v: boolean) => v ? <Tag color="warning" style={{ fontSize: 12 }}>截</Tag> : '' },
    { title: '', width: 60, fixed: 'right',
      render: (_, r) => <a style={{ fontSize: 13 }} onClick={() => showDetail(r.id)}>详情</a> },
  ]

  const codeBlockStyle: React.CSSProperties = {
    background: 'var(--bg-hover)', padding: 14, borderRadius: 6,
    maxHeight: 240, overflow: 'auto', fontSize: 14,
    border: '1px solid var(--border-color)', fontFamily: 'Consolas, Monaco, monospace',
    whiteSpace: 'pre-wrap', wordBreak: 'break-all',
  }

  return (
    <div>
      {/* 工具栏 */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 16 }}>
        <Space size={8}>
          <Input placeholder="模型" allowClear size="small" style={{ width: 130 }}
            onPressEnter={(e) => { setModel((e.target as HTMLInputElement).value); setPage(1) }} />
          <Input placeholder="调用方" allowClear size="small" style={{ width: 160 }}
            onPressEnter={(e) => { setCaller((e.target as HTMLInputElement).value); setPage(1) }} />
          <Select placeholder="流式" allowClear size="small" style={{ width: 90 }}
            value={streamFilter || undefined}
            onChange={v => { setStreamFilter(v || ''); setPage(1) }}
            options={[{ value: 'true', label: '流式' }, { value: 'false', label: '非流式' }]} />
        </Space>
        <Space size={8}>
          <Button size="small" icon={<ReloadOutlined />} onClick={load}>刷新</Button>
          <Button size="small" type="primary" ghost icon={<ExportOutlined />} onClick={handleExport}>导出</Button>
        </Space>
      </div>

      <Table
        rowKey="id" loading={loading} dataSource={data} columns={columns}
        scroll={{ x: 1100 }} size="small"
        pagination={{
          current: page, pageSize: size, total, showSizeChanger: true,
          size: 'small', showTotal: t => `${t} 条`,
          onChange: (p, s) => { setPage(p); setSize(s) },
        }}
      />

      <Drawer title={<Text strong style={{ fontSize: 14 }}>对话详情 {detail && `#${detail.id}`}</Text>}
        open={drawerOpen} onClose={() => { setDrawerOpen(false); setDetail(null) }} width={760}>
        {detailLoading ? <div style={{ textAlign: 'center', padding: 60 }}><Spin /></div> : detail && (
          <>
            <Descriptions size="small" column={2} bordered style={{ marginBottom: 16 }}>
              <Descriptions.Item label="模型">{detail.model || '-'}</Descriptions.Item>
              <Descriptions.Item label="端点">{detail.endpoint}</Descriptions.Item>
              <Descriptions.Item label="调用方">{detail.caller_tag}</Descriptions.Item>
              <Descriptions.Item label="状态">
                <Tag color={detail.http_status < 400 ? 'success' : 'error'}>{detail.http_status}</Tag>
              </Descriptions.Item>
              <Descriptions.Item label="Prompt Tok">{detail.prompt_tokens}</Descriptions.Item>
              <Descriptions.Item label="Completion Tok">{detail.completion_tokens}</Descriptions.Item>
              <Descriptions.Item label="延迟">{detail.upstream_latency_ms}ms</Descriptions.Item>
              <Descriptions.Item label="时间">{dayjs(detail.created_at).format('YYYY-MM-DD HH:mm:ss')}</Descriptions.Item>
              {detail.system_prompt_hash && (
                <Descriptions.Item label="System" span={2}>
                  <Text code style={{ fontSize: 12 }}>{detail.system_prompt_hash.slice(0, 24)}...</Text>
                </Descriptions.Item>
              )}
              {detail.error_message && (
                <Descriptions.Item label="错误" span={2}>
                  <Text type="danger" style={{ fontSize: 13 }}>{detail.error_message}</Text>
                </Descriptions.Item>
              )}
            </Descriptions>

            <Text strong style={{ fontSize: 14, display: 'block', margin: '16px 0 8px' }}>Prompt</Text>
            <pre style={codeBlockStyle}>{JSON.stringify(detail.prompt_text, null, 2)}</pre>

            <Text strong style={{ fontSize: 14, display: 'block', margin: '16px 0 8px' }}>Completion</Text>
            <pre style={codeBlockStyle}>{JSON.stringify(detail.completion_text, null, 2)}</pre>

            {detail.tool_calls && (
              <>
                <Text strong style={{ fontSize: 14, display: 'block', margin: '16px 0 8px' }}>Tool Calls</Text>
                <pre style={codeBlockStyle}>{JSON.stringify(detail.tool_calls, null, 2)}</pre>
              </>
            )}
          </>
        )}
      </Drawer>
    </div>
  )
}
