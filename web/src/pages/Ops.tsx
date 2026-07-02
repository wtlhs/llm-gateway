import { useEffect, useState } from 'react'
import { message, Spin, Typography, Tag } from 'antd'
import ReactECharts from 'echarts-for-react'
import { useTheme } from '../theme'
import { api, type DBStats, type LatencyBucket, type DataQuality } from '../api/client'
import { Panel, SectionHeader, PanelSkeleton, MiniStat } from '../components/Common'

const { Text } = Typography

export default function Ops() {
  const { mode } = useTheme()
  const [loading, setLoading] = useState(true)
  const [db, setDB] = useState<DBStats | null>(null)
  const [latency, setLatency] = useState<LatencyBucket[]>([])
  const [quality, setQuality] = useState<DataQuality | null>(null)

  const isDark = mode === 'dark'
  const cText = isDark ? '#8B8B90' : '#6E6E73'
  const cGrid = isDark ? '#1E1E22' : '#EFEFF1'
  const cBar = isDark ? '#3D3D44' : '#E4E4E7'
  const cBarPeak = isDark ? '#7B8CFF' : '#5B7CFA'
  const cTooltip = isDark ? '#18181B' : '#FFF'

  useEffect(() => {
    Promise.all([api.dbStats(), api.latency(), api.dataQuality()])
      .then(([d, l, q]) => {
        setDB(d.data); setLatency(l.data || []); setQuality(q.data)
      }).catch(e => message.error('加载失败: ' + e.message))
      .finally(() => setLoading(false))
  }, [])

  if (loading) return <div><PanelSkeleton lines={3} /><div style={{height:16}}/><PanelSkeleton lines={3} /></div>

  const tooltipBase = {
    backgroundColor: cTooltip, borderColor: cGrid, borderWidth: 1,
    textStyle: { color: isDark ? '#F4F4F5' : '#0D0D0D', fontSize: 12 },
    extraCssText: 'border-radius:6px;box-shadow:0 4px 16px rgba(0,0,0,0.08);',
  }

  // 延迟分布柱状图
  const latMax = Math.max(...latency.map(l => l.count), 1)
  const latencyOption = {
    tooltip: { trigger: 'axis', ...tooltipBase, axisPointer: { type: 'shadow' } },
    grid: { left: 36, right: 24, top: 16, bottom: 28, containLabel: true },
    xAxis: { type: 'category', data: latency.map(l => l.bucket),
      axisLine: { lineStyle: { color: cGrid } }, axisTick: { show: false },
      axisLabel: { color: cText, fontSize: 10 } },
    yAxis: { type: 'value', splitLine: { lineStyle: { color: cGrid } },
      axisLabel: { color: cText, fontSize: 11 }, axisLine: { show: false }, axisTick: { show: false } },
    series: [{
      type: 'bar', barWidth: 18,
      data: latency.map(l => ({
        value: l.count,
        itemStyle: { color: l.count === latMax ? cBarPeak : cBar, borderRadius: [3,3,0,0] },
      })),
      label: { show: true, position: 'top', color: cText, fontSize: 11 },
    }],
  }

  // 数据质量环形进度
  const pct = (v: number, total: number) => total > 0 ? Math.round(v / total * 100) : 0
  const qualityBars = [
    { label: 'Token 计量', val: pct(quality?.with_usage || 0, quality?.total || 1), good: 80 },
    { label: '调用方标识', val: pct(quality?.with_caller || 0, quality?.total || 1), good: 80 },
    { label: 'System 分流', val: pct(quality?.with_sys_prompt || 0, quality?.total || 1), good: 0 },
    { label: '完整未截断', val: 100 - pct(quality?.truncated || 0, quality?.total || 1), good: 80 },
  ]

  return (
    <div>
      {/* 数据库健康 */}
      <Panel style={{ marginBottom: 16 }}>
        <SectionHeader title="数据库健康" subtitle="PostgreSQL · llm_gateway" />
        <div style={{ display: 'flex', gap: 32, flexWrap: 'wrap' }}>
          <MiniStat label="对话表大小" value={db?.conv_table_size || '-'} />
          <MiniStat label="索引大小" value={db?.conv_index_size || '-'} />
          <MiniStat label="活元组" value={db?.live_tuples ?? 0} />
          <MiniStat label="死元组" value={db?.dead_tuples ?? 0}
            color={(db?.dead_tuples ?? 0) > 100 ? 'var(--amber)' : undefined} />
          <MiniStat label="System Prompts" value={db?.sys_prompt_count ?? 0} />
          <MiniStat label="知识层大小" value={db?.sys_prompt_size || '-'} />
          <MiniStat label="总库大小" value={db?.total_db_size || '-'} />
        </div>
        <div style={{ marginTop: 16, paddingTop: 16, borderTop: '1px solid var(--border-color)' }}>
          <div style={{ display: 'flex', gap: 24, flexWrap: 'wrap' }}>
            <Text style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>
              VACUUM: <Tag style={{fontSize:10}}>{db?.last_vacuum}</Tag>
            </Text>
            <Text style={{ fontSize: 11, color: 'var(--text-tertiary)' }}>
              ANALYZE: <Tag style={{fontSize:10}}>{db?.last_analyze}</Tag>
            </Text>
            {(db?.dead_tuples ?? 0) > 100 && (
              <Text style={{ fontSize: 11, color: 'var(--amber)' }}>⚠ 建议执行 VACUUM</Text>
            )}
          </div>
        </div>
      </Panel>

      {/* 延迟分布 */}
      <Panel style={{ marginBottom: 16 }}>
        <SectionHeader title="上游延迟分布" subtitle="LLM 响应时间区间统计" />
        <ReactECharts option={latencyOption} style={{ height: 220 }} />
      </Panel>

      {/* 数据质量 */}
      <Panel>
        <SectionHeader title="数据采集质量" subtitle="检测各维度填充率" />
        <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
          {qualityBars.map(q => {
            const color = q.val >= q.good ? 'var(--green)' : q.val >= 40 ? 'var(--amber)' : 'var(--red)'
            return (
              <div key={q.label}>
                <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 6 }}>
                  <Text style={{ fontSize: 12, color: 'var(--text-secondary)' }}>{q.label}</Text>
                  <Text style={{ fontSize: 12, fontWeight: 600, color, fontVariantNumeric: 'tabular-nums' }}>{q.val}%</Text>
                </div>
                <div style={{ height: 6, background: 'var(--bg-subtle)', borderRadius: 3, overflow: 'hidden' }}>
                  <div style={{
                    height: '100%', width: `${q.val}%`, background: color,
                    borderRadius: 3, transition: 'width 0.6s cubic-bezier(0.4,0,0.2,1)',
                  }} />
                </div>
              </div>
            )
          })}
        </div>
      </Panel>
    </div>
  )
}
