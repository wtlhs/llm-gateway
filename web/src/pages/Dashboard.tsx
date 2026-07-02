import { useEffect, useState, useCallback } from 'react'
import { Spin, message, Typography, Table, Tag, Button, Tooltip } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import type { ColumnsType } from 'antd/es/table'
import ReactECharts from 'echarts-for-react'
import { useTheme } from '../theme'
import { api, type Overview, type TrendPoint, type DimensionCount, type HourlyPoint, type ModelStat } from '../api/client'
import { Panel, SectionHeader } from '../components/Common'

const { Text } = Typography

function MetricCard({ label, value, suffix, danger, highlight }: {
  label: string; value: number | string; suffix?: string; danger?: boolean; highlight?: boolean
}) {
  return (
    <div style={{ padding: '20px 22px', flex: 1, minWidth: 150 }}>
      <Text style={{ color: 'var(--text-tertiary)', fontSize: 12, fontWeight: 500, display: 'block', marginBottom: 10 }}>
        {label}
      </Text>
      <div className="metric-value" style={{
        color: danger ? 'var(--red)' : highlight ? 'var(--blue)' : 'var(--text-primary)',
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
  const [refreshing, setRefreshing] = useState(false)
  const [overview, setOverview] = useState<Overview | null>(null)
  const [trend, setTrend] = useState<TrendPoint[]>([])
  const [topModels, setTopModels] = useState<DimensionCount[]>([])
  const [topCallers, setTopCallers] = useState<DimensionCount[]>([])
  const [hourly, setHourly] = useState<HourlyPoint[]>([])
  const [modelStats, setModelStats] = useState<ModelStat[]>([])
  const [lastUpdate, setLastUpdate] = useState<Date>(new Date())

  const isDark = mode === 'dark'
  const cText = isDark ? '#8B8B90' : '#6E6E73'
  const cGrid = isDark ? '#1E1E22' : '#EFEFF1'
  const cBar = isDark ? '#3D3D44' : '#E4E4E7'
  const cBarActive = isDark ? '#7B8CFF' : '#5B7CFA'
  const cLine1 = isDark ? '#7B8CFF' : '#5B7CFA'
  const cLine2 = isDark ? '#4ADE80' : '#16A34A'
  const cTooltip = isDark ? '#18181B' : '#FFF'

  const loadAll = useCallback(async (silent = false) => {
    if (!silent) setLoading(true)
    else setRefreshing(true)
    try {
      const [ov, tr, tm, tc, hr, ms] = await Promise.all([
        api.overview(), api.trend(7), api.topModels(5), api.topCallers(5),
        api.hourly(), api.modelStats(),
      ])
      setOverview(ov.data); setTrend(tr.data || [])
      setTopModels(tm.data || []); setTopCallers(tc.data || [])
      setHourly(hr.data || []); setModelStats(ms.data || [])
      setLastUpdate(new Date())
    } catch (e: any) {
      message.error('加载失败: ' + e.message)
    } finally {
      setLoading(false); setRefreshing(false)
    }
  }, [])

  useEffect(() => {
    loadAll()
    // 自动刷新: 每 60s
    const timer = setInterval(() => loadAll(true), 60000)
    return () => clearInterval(timer)
  }, [loadAll])

  if (loading) return <div style={{ textAlign: 'center', padding: 120 }}><Spin /></div>

  const tooltipBase = {
    backgroundColor: cTooltip, borderColor: cGrid, borderWidth: 1,
    textStyle: { color: isDark ? '#F4F4F5' : '#0D0D0D', fontSize: 12 },
    extraCssText: 'border-radius:6px;box-shadow:0 4px 16px rgba(0,0,0,0.08);padding:8px 12px;',
  }

  const trendOption = {
    tooltip: { trigger: 'axis', ...tooltipBase, axisPointer: { type: 'line', lineStyle: { color: cGrid } } },
    legend: { data: ['对话量', 'Prompt', 'Completion'], textStyle: { color: cText, fontSize: 11 },
      bottom: 0, icon: 'roundRect', itemWidth: 12, itemHeight: 2, itemGap: 24 },
    grid: { left: 44, right: 44, top: 16, bottom: 36 },
    xAxis: { type: 'category', data: trend.map(t => t.date.slice(5)),
      axisLine: { lineStyle: { color: cGrid } }, axisTick: { show: false },
      axisLabel: { color: cText, fontSize: 11, margin: 12 } },
    yAxis: [
      { type: 'value', splitLine: { lineStyle: { color: cGrid } }, axisLabel: { color: cText, fontSize: 11 }, axisLine: { show: false }, axisTick: { show: false } },
      { type: 'value', splitLine: { show: false }, axisLabel: { color: cText, fontSize: 11 }, axisLine: { show: false }, axisTick: { show: false } },
    ],
    series: [
      { name: '对话量', type: 'bar', barWidth: 12,
        itemStyle: { color: (p: any) => p.dataIndex === trend.length - 1 ? cBarActive : cBar, borderRadius: [3,3,0,0] },
        data: trend.map(t => t.count) },
      { name: 'Prompt', type: 'line', yAxisIndex: 1, smooth: true, showSymbol: false,
        lineStyle: { width: 2, color: cLine1 }, itemStyle: { color: cLine1 },
        areaStyle: { color: { type: 'linear', x:0,y:0,x2:0,y2:1, colorStops:[
          {offset:0, color: isDark ? 'rgba(123,140,255,0.12)' : 'rgba(91,124,250,0.08)'},
          {offset:1, color: 'transparent'}] } },
        data: trend.map(t => t.prompt_tokens) },
      { name: 'Completion', type: 'line', yAxisIndex: 1, smooth: true, showSymbol: false,
        lineStyle: { width: 2, color: cLine2 }, itemStyle: { color: cLine2 },
        data: trend.map(t => t.completion_tokens) },
    ],
  }

  const barOption = (data: DimensionCount[]) => {
    const sorted = [...data].sort((a, b) => a.count - b.count)
    return {
      tooltip: { trigger: 'axis', ...tooltipBase, axisPointer: { type: 'shadow', shadowStyle: { color: isDark ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.02)' } } },
      grid: { left: 8, right: 44, top: 8, bottom: 8, containLabel: true },
      xAxis: { type: 'value', splitLine: { lineStyle: { color: cGrid } }, axisLabel: { color: cText, fontSize: 11 }, axisLine: { show: false }, axisTick: { show: false } },
      yAxis: { type: 'category', data: sorted.map(d => (d.key || '(空)').slice(0, 14)),
        axisLine: { show: false }, axisTick: { show: false }, axisLabel: { color: cText, fontSize: 11, width: 110, overflow: 'truncate' } },
      series: [{
        type: 'bar', barWidth: 10,
        data: sorted.map((d, i) => ({ value: d.count,
          itemStyle: { color: i === sorted.length - 1 ? cLine1 : cBar, borderRadius: [0,3,3,0] } })),
        label: { show: true, position: 'right', color: cText, fontSize: 11, fontWeight: 500, formatter: '{c}' },
      }],
    }
  }

  // 24h 活跃热力(柱状)
  const hourMax = Math.max(...hourly.map(h => h.count), 1)
  const hourlyOption = {
    tooltip: { trigger: 'axis', ...tooltipBase, axisPointer: { type: 'shadow' },
      formatter: (p: any) => `${p[0].name}:00 - ${p[0].value} 次` },
    grid: { left: 8, right: 8, top: 8, bottom: 24, containLabel: false },
    xAxis: { type: 'category', data: hourly.map(h => String(h.hour)),
      axisLine: { show: false }, axisTick: { show: false },
      axisLabel: { color: cText, fontSize: 9, interval: 2 } },
    yAxis: { show: false },
    series: [{
      type: 'bar', barWidth: '60%',
      data: hourly.map(h => ({ value: h.count,
        itemStyle: { color: h.count === hourMax && h.count > 0 ? cBarActive : cBar, borderRadius: [2,2,0,0] } })),
    }],
  }

  // 模型效率表
  const modelColumns: ColumnsType<ModelStat> = [
    { title: '模型', dataIndex: 'model', ellipsis: true,
      render: (v: string) => <Text style={{ fontSize: 12, fontWeight: 500 }}>{v}</Text> },
    { title: '对话', dataIndex: 'count', width: 70,
      render: (v: number) => <Text style={{ fontSize: 12 }}>{v}</Text> },
    { title: '均Prompt', dataIndex: 'avg_prompt', width: 90,
      render: (v: number) => <Text style={{ fontSize: 12 }}>{Math.round(v)}</Text> },
    { title: '均Completion', dataIndex: 'avg_completion', width: 100,
      render: (v: number) => <Text style={{ fontSize: 12 }}>{Math.round(v)}</Text> },
    { title: '均延迟', dataIndex: 'avg_latency', width: 80,
      render: (v: number) => <Tag style={{ fontSize: 10 }}>{Math.round(v)}ms</Tag> },
  ]

  return (
    <div>
      {/* 指标卡 */}
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

      {/* 趋势图 + 刷新 */}
      <Panel style={{ marginBottom: 16, paddingBottom: 12 }}>
        <SectionHeader title="7 天趋势" subtitle="对话量 · Prompt Tokens · Completion Tokens"
          extra={
            <Tooltip title={`更新于 ${lastUpdate.toLocaleTimeString()}`}>
              <Button type="text" size="small" icon={<ReloadOutlined spin={refreshing} />}
                onClick={() => loadAll(true)} style={{ color: 'var(--text-tertiary)', fontSize: 11 }} />
            </Tooltip>
          }
        />
        <ReactECharts option={trendOption} style={{ height: 232 }} />
      </Panel>

      {/* 24h 活跃 */}
      <Panel style={{ marginBottom: 16 }}>
        <SectionHeader title="24 小时活跃分布" subtitle="最近 24 小时对话量(按北京时间)" />
        <ReactECharts option={hourlyOption} style={{ height: 100 }} />
      </Panel>

      {/* Top 模型 + 调用方 */}
      <div style={{ display: 'flex', gap: 16, marginBottom: 16 }}>
        <Panel style={{ flex: 1 }}>
          <SectionHeader title="Top 模型" subtitle="按对话量排序" />
          <ReactECharts option={barOption(topModels)} style={{ height: 168 }} />
        </Panel>
        <Panel style={{ flex: 1 }}>
          <SectionHeader title="Top 调用方" subtitle="按对话量排序" />
          <ReactECharts option={barOption(topCallers)} style={{ height: 168 }} />
        </Panel>
      </div>

      {/* 模型效率表 */}
      <Panel>
        <SectionHeader title="模型效率分析" subtitle="Token 用量 + 延迟对比" />
        <Table rowKey="model" dataSource={modelStats} columns={modelColumns} size="small"
          pagination={false} scroll={{ x: 400 }} />
      </Panel>
    </div>
  )
}
