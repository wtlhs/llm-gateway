import { useEffect, useState } from 'react'
import { Spin, message, Typography } from 'antd'
import ReactECharts from 'echarts-for-react'
import { useTheme } from '../theme'
import { api, type Overview, type TrendPoint, type DimensionCount } from '../api/client'

const { Text } = Typography

// 现代风格指标卡：大数字 + 标签 + 强调色
function MetricCard({ label, value, suffix, accent, danger }: {
  label: string; value: number | string; suffix?: string; accent?: boolean; danger?: boolean
}) {
  return (
    <div className="solid-card" style={{ padding: '20px 24px', flex: 1, minWidth: 160 }}>
      <Text style={{ color: 'var(--text-secondary)', fontSize: 13, display: 'block', marginBottom: 8 }}>
        {label}
      </Text>
      <div className="metric-value" style={{
        color: danger ? 'var(--danger)' : accent ? 'transparent' : 'var(--text-primary)',
        backgroundImage: accent ? 'var(--accent-grad)' : undefined,
        WebkitBackgroundClip: accent ? 'text' : undefined,
        WebkitTextFillColor: accent ? 'transparent' : undefined,
      }}>
        {typeof value === 'number' ? value.toLocaleString() : value}
        {suffix && <span style={{ fontSize: 14, fontWeight: 400, marginLeft: 4, color: 'var(--text-tertiary)' }}>{suffix}</span>}
      </div>
    </div>
  )
}

export default function Dashboard() {
  const { mode } = useTheme()
  const [loading, setLoading] = useState(true)
  const [overview, setOverview] = useState<Overview | null>(null)
  const [trend, setTrend] = useState<TrendPoint[]>([])
  const [topModels, setTopModels] = useState<DimensionCount[]>([])
  const [topCallers, setTopCallers] = useState<DimensionCount[]>([])

  // 主题相关的图表配色
  const isDark = mode === 'dark'
  const chartText = isDark ? '#999' : '#6B6B6B'
  const chartGrid = isDark ? 'rgba(255,255,255,0.06)' : 'rgba(0,0,0,0.04)'
  const gradFrom = isDark ? 'rgba(123,115,255,0.35)' : 'rgba(99,91,255,0.25)'
  const accentColor = isDark ? '#7B73FF' : '#635BFF'

  useEffect(() => {
    Promise.all([
      api.overview(), api.trend(7), api.topModels(5), api.topCallers(5),
    ]).then(([ov, tr, tm, tc]) => {
      setOverview(ov.data); setTrend(tr.data || [])
      setTopModels(tm.data || []); setTopCallers(tc.data || [])
    }).catch(e => message.error('加载失败: ' + e.message))
      .finally(() => setLoading(false))
  }, [])

  if (loading) return <div style={{ textAlign: 'center', padding: 120 }}><Spin size="large" /></div>

  const trendOption = {
    tooltip: { trigger: 'axis', backgroundColor: isDark ? '#1A1A1D' : '#fff',
      borderColor: chartGrid, textStyle: { color: isDark ? '#EDEDED' : '#1A1A1A' } },
    legend: { data: ['对话量', 'Prompt', 'Completion'], textStyle: { color: chartText },
      bottom: 0, icon: 'circle', itemWidth: 8, itemHeight: 8 },
    grid: { left: 50, right: 50, top: 20, bottom: 40 },
    xAxis: { type: 'category', data: trend.map(t => t.date.slice(5)),
      axisLine: { lineStyle: { color: chartGrid } },
      axisLabel: { color: chartText, fontSize: 11 } },
    yAxis: [
      { type: 'value', name: '', splitLine: { lineStyle: { color: chartGrid } },
        axisLabel: { color: chartText, fontSize: 11 } },
      { type: 'value', splitLine: { show: false }, axisLabel: { color: chartText, fontSize: 11 } },
    ],
    series: [
      { name: '对话量', type: 'bar', barWidth: 18, itemStyle: { borderRadius: [4,4,0,0], color: accentColor },
        data: trend.map(t => t.count) },
      { name: 'Prompt', type: 'line', yAxisIndex: 1, smooth: true, symbol: 'none',
        lineStyle: { width: 2, color: isDark ? '#5B8DEF' : '#3B82F6' },
        areaStyle: { color: { type: 'linear', x:0,y:0,x2:0,y2:1,
          colorStops:[{offset:0,color:gradFrom},{offset:1,color:'transparent'}]}},
        data: trend.map(t => t.prompt_tokens) },
      { name: 'Completion', type: 'line', yAxisIndex: 1, smooth: true, symbol: 'none',
        lineStyle: { width: 2, color: isDark ? '#00D68F' : '#00B884' },
        data: trend.map(t => t.completion_tokens) },
    ],
  }

  const pieOption = (data: DimensionCount[]) => ({
    tooltip: { trigger: 'item', backgroundColor: isDark ? '#1A1A1D' : '#fff',
      borderColor: chartGrid, textStyle: { color: isDark ? '#EDEDED' : '#1A1A1A' } },
    legend: { type: 'scroll', bottom: 0, textStyle: { color: chartText, fontSize: 11 },
      icon: 'circle', itemWidth: 7, itemHeight: 7 },
    series: [{
      type: 'pie', radius: ['42%', '68%'], center: ['50%', '42%'],
      avoidLabelOverlap: true, itemStyle: { borderColor: isDark ? '#0A0A0B' : '#fff', borderWidth: 2 },
      label: { show: false },
      color: [accentColor, isDark?'#5B8DEF':'#3B82F6', isDark?'#00D68F':'#00B884',
        isDark?'#FFAB00':'#FF8C00', isDark?'#FF6B6B':'#FF4D4F'],
      data: data.map(d => ({ name: d.key || '(空)', value: d.count })),
    }],
  })

  return (
    <div>
      {/* 指标卡 row */}
      <div style={{ display: 'flex', gap: 16, marginBottom: 20, flexWrap: 'wrap' }}>
        <MetricCard label="对话总量" value={overview?.total || 0} accent />
        <MetricCard label="今日对话" value={overview?.today_count || 0} />
        <MetricCard label="Prompt Tokens" value={overview?.prompt_tokens_sum || 0} />
        <MetricCard label="Completion Tokens" value={overview?.completion_tokens_sum || 0} />
        <MetricCard label="平均延迟" value={Math.round(overview?.avg_latency_ms || 0)} suffix="ms" />
        <MetricCard label="错误数" value={overview?.error_count || 0} danger={(overview?.error_count || 0) > 0} />
      </div>

      {/* 趋势图 */}
      <div className="solid-card" style={{ padding: 24, marginBottom: 20 }}>
        <Text strong style={{ fontSize: 15, display: 'block', marginBottom: 16, color: 'var(--text-primary)' }}>
          7 天趋势
        </Text>
        <ReactECharts option={trendOption} style={{ height: 280 }} />
      </div>

      {/* 双饼图 */}
      <div style={{ display: 'flex', gap: 16 }}>
        <div className="solid-card" style={{ padding: 24, flex: 1 }}>
          <Text strong style={{ fontSize: 14, display: 'block', marginBottom: 8, color: 'var(--text-primary)' }}>Top 模型</Text>
          <ReactECharts option={pieOption(topModels)} style={{ height: 240 }} />
        </div>
        <div className="solid-card" style={{ padding: 24, flex: 1 }}>
          <Text strong style={{ fontSize: 14, display: 'block', marginBottom: 8, color: 'var(--text-primary)' }}>Top 调用方</Text>
          <ReactECharts option={pieOption(topCallers)} style={{ height: 240 }} />
        </div>
      </div>
    </div>
  )
}
