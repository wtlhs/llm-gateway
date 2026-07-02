import { Layout, Menu, Button, Space } from 'antd'
import {
  DashboardOutlined,
  MessageOutlined,
  BookOutlined,
  SettingOutlined,
  MonitorOutlined,
} from '@ant-design/icons'
import { Routes, Route, useNavigate, useLocation } from 'react-router-dom'
import { useTheme } from './theme'
import Dashboard from './pages/Dashboard'
import Conversations from './pages/Conversations'
import Knowledge from './pages/Knowledge'
import Settings from './pages/Settings'
import Ops from './pages/Ops'

const { Header, Content } = Layout

export default function App() {
  const navigate = useNavigate()
  const location = useLocation()
  const { mode, toggle } = useTheme()

  const items = [
    { key: '/', icon: <DashboardOutlined />, label: '总览' },
    { key: '/conversations', icon: <MessageOutlined />, label: '对话' },
    { key: '/knowledge', icon: <BookOutlined />, label: '知识库' },
    { key: '/ops', icon: <MonitorOutlined />, label: '运维' },
    { key: '/settings', icon: <SettingOutlined />, label: '配置' },
  ]

  const selectedKey = items.find(i => i.key !== '/' && location.pathname.startsWith(i.key))?.key
    || (location.pathname === '/' ? '/' : '/')

  return (
    <Layout style={{ minHeight: '100vh', background: 'var(--bg-page)' }}>
      {/* 顶栏：实色细边框, 无毛玻璃 */}
      <Header style={{
        position: 'sticky', top: 0, zIndex: 100,
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
        padding: '0 32px', height: 56,
        background: 'var(--bg-elevated)',
        borderBottom: '1px solid var(--border-color)',
      }}>
        <Space size="large" align="center">
          <span
            onClick={() => navigate('/')}
            style={{ cursor: 'pointer', fontWeight: 600, fontSize: 14, color: 'var(--text-primary)', letterSpacing: '-0.01em' }}
          >
            Context
          </span>
          <Menu
            mode="horizontal"
            selectedKeys={[selectedKey]}
            items={items}
            onClick={({ key }) => navigate(key)}
            style={{
              background: 'transparent', borderBottom: 'none', minWidth: 320, flex: 1,
              fontSize: 13,
            }}
          />
        </Space>

        <Button
          type="text"
          size="small"
          onClick={toggle}
          style={{ color: 'var(--text-secondary)', fontSize: 12 }}
        >
          {mode === 'dark' ? '亮色' : '暗色'}
        </Button>
      </Header>

      <Content style={{ padding: '32px 32px', maxWidth: 1280, width: '100%', margin: '0 auto' }}>
          <Routes>
            <Route path="/" element={<Dashboard />} />
            <Route path="/conversations" element={<Conversations />} />
            <Route path="/knowledge" element={<Knowledge />} />
            <Route path="/ops" element={<Ops />} />
            <Route path="/settings" element={<Settings />} />
          </Routes>
      </Content>
    </Layout>
  )
}
