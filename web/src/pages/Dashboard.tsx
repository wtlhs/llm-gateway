import { useEffect, useState } from 'react'
import { Spin, message, Typography } from 'antd'
import ReactECharts from 'echarts-for-react'
import { useTheme } from '../theme'
import { api, type Overview, type TrendPoint, type DimensionCount } from '../api/client'

const { Text } = Typography

// 精致指标卡：tabular-nums + 蓝色强调 + 增长指示
function MetricCard({ label, value, suffix, danger, highlight, delta }: {
  label: string; value: number | string; suffix?: string
  danger?: boolean; highlight?: boolean; delta?: string
}) {
  return (
    <div style={{ padding: '20px 22px', flex: 1, minWidth: 150, position: 'relative' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 12 }}>
        <Text style={{ color: 'var(--text-tertiary)', fontSize: 12, fontWeight: 500, letterSpacing: '0.01em' }}>
          {label}
        </Text>
        {delta && (
          <span style={{
            fontSize: 11, fontWeight: 500, padding: '1px 6px', borderRadius: 4,
            background: 'var(--green-bg)', color: 'var(--green)',
          }}>{delta}</span>
        )}
      </div>
      <div className="metric-value" style={{
        color: danger ? 'var(--red)' : highlight ? 'var(--blue)' : 'var(--text-primary)',
        transition: 'color 0.2s',
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
  const cGrid = isDark ? '#1E1E22' : '#EFEFF1'
  const cAxis = isDark ? '#26262A' : '#F2F2F3'
  // Linear/Stripe 风格: 1 个蓝色强调, 其余低饱和
  const cBar = isDark ? '#3D3D44' : '#E4E4E7'      // 对话量柱: 中性灰
  const cBarActive = isDark ? '#7B8CFF' : '#5B7CFA' // 最新一天高亮蓝
  const cLine1 = isDark ? '#7B8CFF' : '#5B7CFA'     // prompt: 蓝
  const cLine2 = isDark ? '#4ADE80' : '#16A34A'     // completion: 绿

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
    borderColor: isDark ? '#33333A' : '#E4E4E7', borderWidth: 1,
    textStyle: { color: isDark ? '#F4F4F5' : '#0D0D0D', fontSize: 12 },
    extraCssText: 'border-radius: 6px; box-shadow: 0 4px 16px rgba(0,0,0,0.08); padding: 8px 12px;',
  }

  // 趋势图: 最新数据点高亮蓝, 渐进式精致
  const trendDates = trend.map(t => t.date.slice(5))
  const trendOption = {
    tooltip: { trigger: 'axis', ...tooltipStyle, axisPointer: { type: 'line', lineStyle: { color: cAxis, width: 1 } } },
    legend: { data: ['对话量', 'Prompt', 'Completion'], textStyle: { color: cText, fontSize: 11 },
      bottom: 0, icon: 'roundRect', itemWidth: 12, itemHeight: 2, itemGap: 24 },
    grid: { left: 44, right: 44, top: 16, bottom: 36 },
    xAxis: { type: 'category', data: trendDates, boundaryGap: true,
      axisLine: { lineStyle: { color: cGrid } }, axisTick: { show: false },
      axisLabel: { color: cText, fontSize: 11, margin: 12 } },
    yAxis: [
      { type: 'value', splitLine: { lineStyle: { color: cGrid } },
        axisLabel: { color: cText, fontSize: 11 }, axisLine: { show: false }, axisTick: { show: false } },
      { type: 'value', splitLine: { show: false },
        axisLabel: { color: cText, fontSize: 11 }, axisLine: { show: false }, axisTick: { show: false } },
    ],
    series: [
      // 对话量: 柱状, 最新一根高亮蓝
      { name: '对话量', type: 'bar', barWidth: 12, barGap: '20%',
        itemStyle: { color: (p: any) => p.dataIndex === trendDates.length - 1 ? cBarActive : cBar, borderRadius: [3,3,0,0] },
        data: trend.map(t => t.count) },
      // Prompt: 平滑线 + 端点圆点 + 极淡填充
      { name: 'Prompt', type: 'line', yAxisIndex: 1, smooth: true, symbol: 'circle', symbolSize: 5,
        showSymbol: false,
        lineStyle: { width: 2, color: cLine1 },
        itemStyle: { color: cLine1, borderColor: isDark ? '#18181B' : '#FFF', borderWidth: 2 },
        emphasis: { focus: 'series', scale: 1.5 },
        areaStyle: { color: { type: 'linear', x:0,y:0,x2:0,y2:1, colorStops:[
          {offset:0, color: isDark ? 'rgba(123,140,255,0.12)' : 'rgba(91,124,250,0.08)'},
          {offset:1, color: 'transparent'}] } },
        data: trend.map(t => t.prompt_tokens) },
      { name: 'Completion', type: 'line', yAxisIndex: 1, smooth: true, symbol: 'circle', symbolSize: 5,
        showSymbol: false,
        lineStyle: { width: 2, color: cLine2 },
        itemStyle: { color: cLine2, borderColor: isDark ? '#18181B' : '#FFF', borderWidth: 2 },
        emphasis: { focus: 'series', scale: 1.5 },
        data: trend.map(t => t.completion_tokens) },
    ],
  }

  // 横向条形: 渐进色 + 数值标签
  const barOption = (data: DimensionCount[]) => {
    const sorted = [...data].sort((a, b) => a.count - b.count)
    return {
      tooltip: { trigger: 'axis', ...tooltipStyle, axisPointer: { type: 'shadow', shadowStyle: { color: isDark ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.02)' } } },
      grid: { left: 8, right: 40, top: 8, bottom: 8, containLabel: true },
      xAxis: { type: 'value', splitLine: { lineStyle: { color: cGrid } },
        axisLabel: { color: cText, fontSize: 11 }, axisLine: { show: false }, axisTick: { show: false } },
      yAxis: { type: 'category', data: sorted.map(d => (d.key || '(空)').slice(0, 14)),
        axisLine: { show: false }, axisTick: { show: false },
        axisLabel: { color: cText, fontSize: 11, width: 110, overflow: 'truncate' } },
      series: [{
        type: 'bar', barWidth: 10, barCategoryGap: '40%',
        data: sorted.map((d, i) => ({
          value: d.count,
          itemStyle: { color: i === sorted.length - 1 ? cLine1 : cBar, borderRadius: [0,3,3,0] },
        })),
        label: { show: true, position: 'right', color: cText, fontSize: 11, fontWeight: 500,
          formatter: '{c}' },
      }],
      animationDuration: 600, animationEasing: 'cubicOut',
    }
  }

  return (
    <div>
      {/* 指标卡: 竖线分隔 + 今日高亮蓝 */}
      <div className="solid-card" style={{ display: 'flex', marginBottom: 16, overflow: 'hidden' }}>
        <MetricCard label="对话总量" value={overview?.total || 0} />
        <div style={{ width: 1, background: 'var(--border-color)', margin: '14px 0' }} />
        <MetricCard label="今日对话" value={overview?.today_count || 0} highlight />
        <div style={{ width: 1, background: 'var(--border-color)', margin: '14px 0' }} />
        <MetricCard label="Prompt Tokens" value={overview?.prompt_tokens_sum || 0} />
        <div style={{ width: 1, background: 'var(--border-color)', margin: '14px 0' }} />
        <MetricCard label="Completion" value={overview?.completion_tokens_sum || 0} />
        <div style={{ width: 1, background: 'var(--border-color)', margin: '14px 0' }} />
        <MetricCard label="平均延迟" value={Math.round(overview?.avg_latency_ms || 0)} suffix="ms" />
        <div style={{ width: 1, background: 'var(--border-color)', margin: '14px 0' }} />
        <MetricCard label="错误" value={overview?.error_count || 0} danger={(overview?.error_count || 0) > 0} />
      </div>

      {/* 趋势图 */}
      <div className="solid-card" style={{ padding: '22px 24px 12px', marginBottom: 16 }}>
        <Text style={{ fontSize: 13, fontWeight: 600, display: 'block', marginBottom: 4, color: 'var(--text-primary)' }}>
          7 天趋势
        </Text>
        <Text style={{ fontSize: 11, color: 'var(--text-tertiary)', display: 'block', marginBottom: 12 }}>
          对话量 · Prompt Tokens · Completion Tokens
        </Text>
        <ReactECharts option={trendOption} style={{ height: 232 }} />
      </div>

      {/* 横向条形图 */}
      <div style={{ display: 'flex', gap: 16 }}>
        <div className="solid-card" style={{ padding: '22px 24px', flex: 1 }}>
          <Text style={{ fontSize: 13, fontWeight: 600, display: 'block', marginBottom: 4, color: 'var(--text-primary)' }}>Top 模型</Text>
          <Text style={{ fontSize: 11, color: 'var(--text-tertiary)', display: 'block', marginBottom: 16 }}>按对话量排序</Text>
          <ReactECharts option={barOption(topModels)} style={{ height: 168 }} />
        </div>
        <div className="solid-card" style={{ padding: '22px 24px', flex: 1 }}>
          <Text style={{ fontSize: 13, fontWeight: 600, display: 'block', marginBottom: 4, color: 'var(--text-primary)' }}>Top 调用方</Text>
          <Text style={{ fontSize: 11, color: 'var(--text-tertiary)', display: 'block', marginBottom: 16 }}>按对话量排序</Text>
          <ReactECharts option={barOption(topCallers)} style={{ height: 168 }} />
        </div>
      </div>
    </div>
  )
}
