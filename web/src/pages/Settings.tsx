import { useEffect, useState } from 'react'
import { Card, Descriptions, Typography, Alert, Input, Button, Tag, Space } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { getToken, setToken, api } from '../api/client'

const { Paragraph, Text } = Typography

export default function Settings() {
  const [token] = useState(getToken())
  const [health, setHealth] = useState<string>('检测中...')

  const checkHealth = () => {
    setHealth('检测中...')
    api.dbStats().then(() => setHealth('正常')).catch(() => setHealth('异常'))
  }

  useEffect(() => { checkHealth() }, [])

  return (
    <div>
      {/* 系统健康 */}
      <div className="solid-card" style={{ padding: '20px 24px', marginBottom: 16, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div>
          <Text style={{ fontSize: 13, fontWeight: 600, display: 'block', marginBottom: 4 }}>系统状态</Text>
          <Text style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>后端连接 + 数据库连通性</Text>
        </div>
        <Space size="large" align="center">
          <span style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <Text style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>状态</Text>
            <Tag color={health === '正常' ? 'success' : health === '异常' ? 'error' : 'processing'}>{health}</Tag>
          </span>
          <Button size="small" type="text" icon={<ReloadOutlined />} onClick={checkHealth} style={{ color: 'var(--text-tertiary)' }}>刷新</Button>
        </Space>
      </div>

      <Alert message="配置说明" description="网关参数修改需重启网关进程。平台配置管理(白名单/排除模型编辑)在后续版本支持。"
        type="info" showIcon style={{ marginBottom: 16, borderRadius: 6 }} />

      <div style={{ display: 'flex', gap: 16, flexWrap: 'wrap' }}>
        <Card title="访问凭证" style={{ marginBottom: 16, flex: 1, minWidth: 400, background: 'var(--bg-card)', border: '1px solid var(--border-color)' }}>
          <Paragraph type="secondary" style={{ fontSize: 12 }}>
            Bearer Token, 调用后端 API 时用。留空则不鉴权(内网/开发场景)。
          </Paragraph>
          <Input.Search
            defaultValue={token}
            placeholder="输入 Auth Token"
            enterButton="保存"
            onSearch={v => { setToken(v); window.location.reload() }}
            style={{ maxWidth: 400 }}
          />
        </Card>

        <Card title="系统信息" style={{ flex: 1, minWidth: 400, background: 'var(--bg-card)', border: '1px solid var(--border-color)' }}>
          <Descriptions column={1} size="small">
            <Descriptions.Item label="版本">Platform v0.2</Descriptions.Item>
            <Descriptions.Item label="后端">Go cmd/platform(独立进程)</Descriptions.Item>
            <Descriptions.Item label="前端">React + Ant Design + ECharts</Descriptions.Item>
            <Descriptions.Item label="数据源">PostgreSQL llm_gateway(只读)</Descriptions.Item>
            <Descriptions.Item label="时区">Asia/Shanghai</Descriptions.Item>
            <Descriptions.Item label="自动刷新">看板每 60s</Descriptions.Item>
          </Descriptions>
        </Card>
      </div>
    </div>
  )
}
