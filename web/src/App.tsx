import { Layout, Menu, theme } from 'antd'
import {
  DashboardOutlined,
  MessageOutlined,
  BookOutlined,
  SettingOutlined,
} from '@ant-design/icons'
import { Routes, Route, useNavigate, useLocation } from 'react-router-dom'
import Dashboard from './pages/Dashboard'
import Conversations from './pages/Conversations'
import Knowledge from './pages/Knowledge'
import Settings from './pages/Settings'

const { Header, Sider, Content } = Layout

const menuItems = [
  { key: '/', icon: <DashboardOutlined />, label: '总览看板' },
  { key: '/conversations', icon: <MessageOutlined />, label: '对话检索' },
  { key: '/knowledge', icon: <BookOutlined />, label: '知识库' },
  { key: '/settings', icon: <SettingOutlined />, label: '配置' },
]

export default function App() {
  const navigate = useNavigate()
  const location = useLocation()
  const { token: { colorBgContainer } } = theme.useToken()

  const selectedKey = menuItems.find(m => m.key !== '/' && location.pathname.startsWith(m.key))?.key
    || (location.pathname === '/' ? '/' : '/')

  return (
    <Layout style={{ minHeight: '100vh' }}>
      <Sider breakpoint="lg" collapsedWidth="0" theme="dark">
        <div style={{ height: 56, margin: 8, color: '#fff', fontSize: 16, fontWeight: 600,
          display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
          上下文仓库
        </div>
        <Menu
          theme="dark"
          mode="inline"
          selectedKeys={[selectedKey]}
          items={menuItems}
          onClick={({ key }) => navigate(key)}
        />
      </Sider>
      <Layout>
        <Header style={{ padding: '0 24px', background: colorBgContainer, display: 'flex', alignItems: 'center' }}>
          <span style={{ fontSize: 16, fontWeight: 500 }}>
            {menuItems.find(m => m.key === selectedKey)?.label}
          </span>
        </Header>
        <Content style={{ margin: 16 }}>
          <div style={{ padding: 24, background: colorBgContainer, minHeight: 360, borderRadius: 8 }}>
            <Routes>
              <Route path="/" element={<Dashboard />} />
              <Route path="/conversations" element={<Conversations />} />
              <Route path="/knowledge" element={<Knowledge />} />
              <Route path="/settings" element={<Settings />} />
            </Routes>
          </div>
        </Content>
      </Layout>
    </Layout>
  )
}
