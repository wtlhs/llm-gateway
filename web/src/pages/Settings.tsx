import { Card, Descriptions, Typography, Alert, Input } from 'antd'
import { getToken, setToken } from '../api/client'

const { Paragraph, Text } = Typography

export default function Settings() {
  const token = getToken()
  return (
    <div>
      <Alert
        message="一期 MVP: 配置只读展示"
        description="网关参数修改需重启网关进程。平台配置管理(白名单/排除模型编辑)在后续版本支持。"
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
      />

      <Card title="访问凭证" style={{ marginBottom: 16, background: 'var(--bg-card)', border: '1px solid var(--border-color)' }}>
        <Paragraph type="secondary">
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

      <Card title="系统信息" style={{ background: 'var(--bg-card)', border: '1px solid var(--border-color)' }}>
        <Descriptions column={1} size="small">
          <Descriptions.Item label="版本">Platform MVP v0.1</Descriptions.Item>
          <Descriptions.Item label="后端">Go cmd/platform (独立进程)</Descriptions.Item>
          <Descriptions.Item label="前端">React + Ant Design + ECharts</Descriptions.Item>
          <Descriptions.Item label="数据源">PostgreSQL llm_gateway 库(只读查询)</Descriptions.Item>
          <Descriptions.Item label="时区">Asia/Shanghai(遵守 docs/TIMEZONE.md)</Descriptions.Item>
        </Descriptions>
      </Card>
    </div>
  )
}
