import { useEffect, useState } from 'react'
import { Card, Col, Row, Statistic, Spin, message } from 'antd'
import ReactECharts from 'echarts-for-react'
import { api, type Overview, type TrendPoint, type DimensionCount } from '../api/client'

export default function Dashboard() {
  const [loading, setLoading] = useState(true)
  const [overview, setOverview] = useState<Overview | null>(null)
  const [trend, setTrend] = useState<TrendPoint[]>([])
  const [topModels, setTopModels] = useState<DimensionCount[]>([])
  const [topCallers, setTopCallers] = useState<DimensionCount[]>([])

  useEffect(() => {
    Promise.all([
      api.overview(),
      api.trend(7),
      api.topModels(5),
      api.topCallers(5),
    ]).then(([ov, tr, tm, tc]) => {
      setOverview(ov.data)
      setTrend(tr.data || [])
      setTopModels(tm.data || [])
      setTopCallers(tc.data || [])
    }).catch(e => message.error('加载失败: ' + e.message))
      .finally(() => setLoading(false))
  }, [])

  if (loading) return <Spin size="large" />

  const trendOption = {
    tooltip: { trigger: 'axis' },
    legend: { data: ['对话量', 'Prompt Tokens', 'Completion Tokens'] },
    xAxis: { type: 'category', data: trend.map(t => t.date) },
    yAxis: [
      { type: 'value', name: '对话量' },
      { type: 'value', name: 'Tokens' },
    ],
    series: [
      { name: '对话量', type: 'bar', data: trend.map(t => t.count) },
      { name: 'Prompt Tokens', type: 'line', yAxisIndex: 1, data: trend.map(t => t.prompt_tokens) },
      { name: 'Completion Tokens', type: 'line', yAxisIndex: 1, data: trend.map(t => t.completion_tokens) },
    ],
  }

  const pieOption = (title: string, data: DimensionCount[]) => ({
    title: { text: title, left: 'center' },
    tooltip: { trigger: 'item' },
    series: [{
      type: 'pie', radius: '60%',
      data: data.map(d => ({ name: d.key || '(空)', value: d.count })),
    }],
  })

  return (
    <div>
      <Row gutter={16} style={{ marginBottom: 16 }}>
        <Col span={4}><Card><Statistic title="对话总量" value={overview?.total || 0} /></Card></Col>
        <Col span={4}><Card><Statistic title="今日对话" value={overview?.today_count || 0} /></Card></Col>
        <Col span={4}><Card><Statistic title="Prompt Tokens" value={overview?.prompt_tokens_sum || 0} /></Card></Col>
        <Col span={4}><Card><Statistic title="Completion Tokens" value={overview?.completion_tokens_sum || 0} /></Card></Col>
        <Col span={4}><Card><Statistic title="平均延迟(ms)" value={Math.round(overview?.avg_latency_ms || 0)} /></Card></Col>
        <Col span={4}><Card><Statistic title="错误数" value={overview?.error_count || 0} valueStyle={{ color: (overview?.error_count || 0) > 0 ? '#cf1322' : undefined }} /></Card></Col>
      </Row>

      <Card title="7 天趋势" style={{ marginBottom: 16 }}>
        <ReactECharts option={trendOption} style={{ height: 300 }} />
      </Card>

      <Row gutter={16}>
        <Col span={12}><Card><ReactECharts option={pieOption('Top 5 模型', topModels)} style={{ height: 280 }} /></Card></Col>
        <Col span={12}><Card><ReactECharts option={pieOption('Top 5 调用方', topCallers)} style={{ height: 280 }} /></Card></Col>
      </Row>
    </div>
  )
}
