import { Layout, Menu, Button, Avatar, Space } from 'antd'
import {
  DashboardOutlined,
  MessageOutlined,
  BookOutlined,
  SettingOutlined,
  BulbOutlined,
  BulbFilled,
} from '@ant-design/icons'
import { Routes, Route, useNavigate, useLocation } from 'react-router-dom'
import { useTheme } from './theme'
import Dashboard from './pages/Dashboard'
import Conversations from './pages/Conversations'
import Knowledge from './pages/Knowledge'
import Settings from './pages/Settings'

const { Header, Content } = Layout

export default function App() {
  const navigate = useNavigate()
  const location = useLocation()
  const { mode, toggle } = useTheme()

  const items = [
    { key: '/', icon: <DashboardOutlined />, label: '总览' },
    { key: '/conversations', icon: <MessageOutlined />, label: '对话' },
    { key: '/knowledge', icon: <BookOutlined />, label: '知识库' },
    { key: '/settings', icon: <SettingOutlined />, label: '配置' },
  ]

  const selectedKey = items.find(i => i.key !== '/' && location.pathname.startsWith(i.key))?.key
    || (location.pathname === '/' ? '/' : '/')

  return (
    <Layout style={{ minHeight: '100vh', background: 'var(--bg-page)' }}>
      {/* 顶部导航栏：毛玻璃 */}
      <Header style={{
        position: 'sticky', top: 0, zIndex: 100,
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
        padding: '0 32px', height: 60,
        background: 'var(--bg-glass)',
        backdropFilter: 'blur(20px) saturate(180%)',
        WebkitBackdropFilter: 'blur(20px) saturate(180%)',
        borderBottom: '1px solid var(--border-color)',
      }}>
        {/* 左：Logo + 导航 */}
        <Space size="large">
          <Space size={8} style={{ cursor: 'pointer' }} onClick={() => navigate('/')}>
            <Avatar size={28} style={{ background: 'var(--accent-grad)', fontSize: 14 }}>LC</Avatar>
            <span style={{ fontWeight: 700, fontSize: 15, letterSpacing: '-0.01em', color: 'var(--text-primary)' }}>
              上下文仓库
            </span>
          </Space>
          <Menu
            mode="horizontal"
            selectedKeys={[selectedKey]}
            items={items}
            onClick={({ key }) => navigate(key)}
            style={{
              background: 'transparent',
              borderBottom: 'none',
              minWidth: 360,
              flex: 1,
            }}
          />
        </Space>

        {/* 右：主题切换 */}
        <Button
          type="text"
          size="large"
          onClick={toggle}
          icon={mode === 'dark' ? <BulbFilled /> : <BulbOutlined />}
          style={{ color: 'var(--text-secondary)' }}
        />
      </Header>

      {/* 内容区 */}
      <Content style={{ padding: '28px 32px', maxWidth: 1400, width: '100%', margin: '0 auto' }}>
        <div className="fade-in">
          <Routes>
            <Route path="/" element={<Dashboard />} />
            <Route path="/conversations" element={<Conversations />} />
            <Route path="/knowledge" element={<Knowledge />} />
            <Route path="/settings" element={<Settings />} />
          </Routes>
        </div>
      </Content>
    </Layout>
  )
}
