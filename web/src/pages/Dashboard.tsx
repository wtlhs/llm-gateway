import { useEffect, useState } from 'react'
import { Spin, message, Typography } from 'antd'
import ReactECharts from 'echarts-for-react'
import { useTheme } from '../theme'
import { api, type Overview, type TrendPoint, type DimensionCount } from '../api/client'

const { Text } = Typography

function MetricCard({ label, value, suffix, danger }: {
  label: string; value: number | string; suffix?: string; danger?: boolean
}) {
  return (
    <div style={{ padding: '18px 20px', flex: 1, minWidth: 150 }}>
      <Text style={{ color: 'var(--text-tertiary)', fontSize: 12, display: 'block', marginBottom: 10, fontWeight: 500 }}>
        {label}
      </Text>
      <div className="metric-value" style={{
        color: danger ? 'var(--red)' : 'var(--text-primary)',
      }}>
        {typeof value === 'number' ? value.toLocaleString() : value}
        {suffix && <span style={{ fontSize: 13, fontWeight: 400, marginLeft: 3, color: 'var(--text-tertiary)' }}>{suffix}</span>}
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

  const isDark = mode === 'dark'
  const cText = isDark ? '#8B8B90' : '#6E6E73'
  const cGrid = isDark ? '#1E1E22' : '#ECECEE'
  const cAxis = isDark ? '#26262A' : '#F2F2F3'
  const c1 = isDark ? '#E1E1E3' : '#0D0D0D'   // 对话量: 主色(近黑/近白)
  const c2 = isDark ? '#5B5BFF' : '#2563EB'   // prompt: 低饱和蓝
  const c3 = isDark ? '#3FAE6A' : '#16A34A'   // completion: 低饱和绿

  useEffect(() => {
    Promise.all([api.overview(), api.trend(7), api.topModels(5), api.topCallers(5)])
      .then(([ov, tr, tm, tc]) => {
        setOverview(ov.data); setTrend(tr.data || [])
        setTopModels(tm.data || []); setTopCallers(tc.data || [])
      }).catch(e => message.error('加载失败: ' + e.message))
      .finally(() => setLoading(false))
  }, [])

  if (loading) return <div style={{ textAlign: 'center', padding: 120 }}><Spin /></div>

  const tooltipStyle = {
    backgroundColor: isDark ? '#18181B' : '#FFF',
    borderColor: cGrid, borderWidth: 1,
    textStyle: { color: isDark ? '#F4F4F5' : '#0D0D0D', fontSize: 12 },
    extraCssText: 'border-radius: 6px; box-shadow: 0 2px 8px rgba(0,0,0,0.08);',
  }

  const trendOption = {
    tooltip: { trigger: 'axis', ...tooltipStyle, axisPointer: { type: 'line', lineStyle: { color: cAxis } } },
    legend: { data: ['对话量', 'Prompt', 'Completion'], textStyle: { color: cText, fontSize: 11 },
      bottom: 0, icon: 'roundRect', itemWidth: 10, itemHeight: 2, itemGap: 20 },
    grid: { left: 44, right: 44, top: 16, bottom: 36 },
    xAxis: { type: 'category', data: trend.map(t => t.date.slice(5)),
      axisLine: { lineStyle: { color: cGrid } }, axisTick: { show: false },
      axisLabel: { color: cText, fontSize: 11 } },
    yAxis: [
      { type: 'value', splitLine: { lineStyle: { color: cGrid, type: 'solid' } },
        axisLabel: { color: cText, fontSize: 11 } },
      { type: 'value', splitLine: { show: false }, axisLabel: { color: cText, fontSize: 11 } },
    ],
    series: [
      { name: '对话量', type: 'bar', barWidth: 14, itemStyle: { color: c1 }, data: trend.map(t => t.count) },
      { name: 'Prompt', type: 'line', yAxisIndex: 1, smooth: false, symbol: 'circle', symbolSize: 4,
        lineStyle: { width: 1.5, color: c2 }, itemStyle: { color: c2 },
        data: trend.map(t => t.prompt_tokens) },
      { name: 'Completion', type: 'line', yAxisIndex: 1, smooth: false, symbol: 'circle', symbolSize: 4,
        lineStyle: { width: 1.5, color: c3 }, itemStyle: { color: c3 },
        data: trend.map(t => t.completion_tokens) },
    ],
  }

  const barOption = (data: DimensionCount[], title: string) => {
    const max = Math.max(...data.map(d => d.count), 1)
    return {
      tooltip: { trigger: 'axis', ...tooltipStyle, axisPointer: { type: 'shadow' } },
      grid: { left: 8, right: 16, top: 24, bottom: 8, containLabel: true },
      xAxis: { type: 'value', splitLine: { lineStyle: { color: cGrid } },
        axisLabel: { color: cText, fontSize: 11 }, axisLine: { show: false }, axisTick: { show: false } },
      yAxis: { type: 'category', data: data.map(d => (d.key || '(空)').slice(0, 16)).reverse(),
        axisLine: { lineStyle: { color: cGrid } }, axisTick: { show: false },
        axisLabel: { color: cText, fontSize: 11, width: 120, overflow: 'truncate' } },
      series: [{
        type: 'bar', barWidth: 14,
        data: data.map(d => d.count).reverse(),
        itemStyle: { color: isDark ? '#3A3A40' : '#E1E1E3', borderRadius: [0, 3, 3, 0] },
        label: { show: true, position: 'right', color: cText, fontSize: 11 },
      }],
    }
  }

  return (
    <div>
      {/* 指标卡：无边框, 靠间距分隔 */}
      <div className="solid-card" style={{ display: 'flex', marginBottom: 16, overflow: 'hidden' }}>
        <MetricCard label="对话总量" value={overview?.total || 0} />
        <div style={{ width: 1, background: 'var(--border-color)', margin: '12px 0' }} />
        <MetricCard label="今日对话" value={overview?.today_count || 0} />
        <div style={{ width: 1, background: 'var(--border-color)', margin: '12px 0' }} />
        <MetricCard label="Prompt Tokens" value={overview?.prompt_tokens_sum || 0} />
        <div style={{ width: 1, background: 'var(--border-color)', margin: '12px 0' }} />
        <MetricCard label="Completion" value={overview?.completion_tokens_sum || 0} />
        <div style={{ width: 1, background: 'var(--border-color)', margin: '12px 0' }} />
        <MetricCard label="平均延迟" value={Math.round(overview?.avg_latency_ms || 0)} suffix="ms" />
        <div style={{ width: 1, background: 'var(--border-color)', margin: '12px 0' }} />
        <MetricCard label="错误" value={overview?.error_count || 0} danger={(overview?.error_count || 0) > 0} />
      </div>

      {/* 趋势图 */}
      <div className="solid-card" style={{ padding: '20px 24px', marginBottom: 16 }}>
        <Text style={{ fontSize: 13, fontWeight: 600, display: 'block', marginBottom: 16, color: 'var(--text-primary)' }}>
          7 天趋势
        </Text>
        <ReactECharts option={trendOption} style={{ height: 240 }} />
      </div>

      {/* 横向条形图(替代花哨饼图) */}
      <div style={{ display: 'flex', gap: 16 }}>
        <div className="solid-card" style={{ padding: '20px 24px', flex: 1 }}>
          <Text style={{ fontSize: 13, fontWeight: 600, display: 'block', marginBottom: 12, color: 'var(--text-primary)' }}>Top 模型</Text>
          <ReactECharts option={barOption(topModels, '模型')} style={{ height: 180 }} />
        </div>
        <div className="solid-card" style={{ padding: '20px 24px', flex: 1 }}>
          <Text style={{ fontSize: 13, fontWeight: 600, display: 'block', marginBottom: 12, color: 'var(--text-primary)' }}>Top 调用方</Text>
          <ReactECharts option={barOption(topCallers, '调用方')} style={{ height: 180 }} />
        </div>
      </div>
    </div>
  )
}
