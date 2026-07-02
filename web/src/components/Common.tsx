import { Skeleton, Typography, Empty } from 'antd'
import type { ReactNode } from 'react'

const { Text } = Typography

// 统一的卡片容器(细边框, 无阴影)
export function Panel({ children, style }: { children: ReactNode; style?: React.CSSProperties }) {
  return (
    <div className="solid-card" style={{ padding: '22px 24px', ...style }}>
      {children}
    </div>
  )
}

// 区域标题 + 副标题
export function SectionHeader({ title, subtitle, extra }: {
  title: string; subtitle?: string; extra?: ReactNode
}) {
  return (
    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: 16 }}>
      <div>
        <Text style={{ fontSize: 13, fontWeight: 600, display: 'block', color: 'var(--text-primary)' }}>{title}</Text>
        {subtitle && <Text style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>{subtitle}</Text>}
      </div>
      {extra}
    </div>
  )
}

// 加载骨架屏(精致的数据加载状态)
export function PanelSkeleton({ lines = 4 }: { lines?: number }) {
  return (
    <div style={{ padding: '22px 24px' }}>
      <Skeleton active paragraph={{ rows: lines }} title={{ width: 120 }} />
    </div>
  )
}

// 空状态
export function EmptyPanel({ text = '暂无数据' }: { text?: string }) {
  return (
    <div style={{ padding: '48px 24px', textAlign: 'center' }}>
      <Empty description={<Text style={{ color: 'var(--text-tertiary)', fontSize: 12 }}>{text}</Text>} image={Empty.PRESENTED_IMAGE_SIMPLE} />
    </div>
  )
}

// 指标小标签
export function MiniStat({ label, value, color }: { label: string; value: string | number; color?: string }) {
  return (
    <div style={{ flex: 1 }}>
      <Text style={{ fontSize: 11, color: 'var(--text-tertiary)', display: 'block', marginBottom: 4 }}>{label}</Text>
      <Text style={{ fontSize: 16, fontWeight: 600, color: color || 'var(--text-primary)', fontVariantNumeric: 'tabular-nums' }}>
        {typeof value === 'number' ? value.toLocaleString() : value}
      </Text>
    </div>
  )
}
