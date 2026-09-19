import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button, Card, Table, Tag, Tooltip } from '@douyinfe/semi-ui-19'
import { IconCopy, IconEdit, IconExternalOpen } from '@douyinfe/semi-icons'
import type { AppTheme, ThemePreference } from '../../components/PageHeader'
import { adminError, adminFetch } from '../../api'
import { protocolLabel } from '../../formatters'
import { showErrorToast, showSuccessToast } from '../../notifications'
import { statusColor, statusText } from '../requests/request-log-shared'

type OverviewMetrics = {
  requestCount: number
  requestSuccessCount: number
  requestFailureCount: number
  fallbackRequestCount: number
  successRate: number
  fallbackRate: number
  p95LatencyMs: number
  averageLatencyMs: number
  requestChange: number | null
  successRateChange: number | null
  p95LatencyChange: number | null
  fallbackChange: number | null
}
type OverviewTrend = {
  bucket: string
  requestCount: number
  requestSuccessCount: number
  fallbackRequestCount: number
  successRate: number
  p95LatencyMs: number
}
type OverviewChannel = {
  id: string
  name: string
  protocol: string
  baseUrl: string
  adminState: string
  healthState: string
  credentialConfigured: boolean
  requests24h: number
  successRate: number
  p95LatencyMs: number
  modelCount: number
  hasRoutableModel?: boolean
}
type OverviewRequest = {
  id: string
  protocol: string
  clientModel: string
  finalStatus: string
  startedAt: string
  latencyMs: number
  attemptCount: number
  finalChannelName?: string
  tokens: number | null
}
type OverviewData = {
  metrics: OverviewMetrics
  trends: OverviewTrend[]
  proxyAccess: { baseUrl: string; webUrl: string }
  channels: OverviewChannel[]
  recentRequests: OverviewRequest[]
  systemInfo: {
    version: string
    os: string
    goVersion: string
    memoryBytes: number
    memoryAlloc: number
    goroutines: number
    startedAt: string
    uptimeSeconds: number
    proxyListen: string
    adminListen: string
  }
}
type OverviewPageProps = {
  theme: AppTheme
  themePreference: ThemePreference
  onChangeTheme: () => void
  active: boolean
}

const emptyData: OverviewData = {
  metrics: {
    requestCount: 0,
    requestSuccessCount: 0,
    requestFailureCount: 0,
    fallbackRequestCount: 0,
    successRate: 0,
    fallbackRate: 0,
    p95LatencyMs: 0,
    averageLatencyMs: 0,
    requestChange: null,
    successRateChange: null,
    p95LatencyChange: null,
    fallbackChange: null,
  },
  trends: [],
  proxyAccess: { baseUrl: '', webUrl: '' },
  channels: [],
  recentRequests: [],
  systemInfo: {
    version: '',
    os: '',
    goVersion: '',
    memoryBytes: 0,
    memoryAlloc: 0,
    goroutines: 0,
    startedAt: '',
    uptimeSeconds: 0,
    proxyListen: '',
    adminListen: '',
  },
}

// 读取总览聚合接口，并将服务端错误转换为页面提示。
async function readOverview(): Promise<OverviewData> {
  const response = await adminFetch('/api/admin/v1/overview')
  if (!response.ok) throw await adminError(response, '读取总览失败')
  return response.json() as Promise<OverviewData>
}

// 格式化数量，避免大数字在表格和指标卡中挤压布局。
function formatNumber(value: number): string {
  return new Intl.NumberFormat('zh-CN').format(value)
}

// 格式化百分比，统一保留两位小数以便比较渠道状态。
function formatPercent(value: number): string {
  return `${(value * 100).toFixed(2)}%`
}

// 格式化延迟；仅未知值显示破折号，0ms 原样展示。
function formatLatency(value: number | null | undefined): string {
  if (value == null) return '—'
  return value >= 1000 ? `${(value / 1000).toFixed(2)} s` : `${Math.round(value)} ms`
}

// 格式化相对时间，无法解析时保留后端原始时间。
function formatRelativeTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  const seconds = Math.max(0, (Date.now() - date.getTime()) / 1000)
  if (seconds < 60) return `${Math.max(1, Math.round(seconds))} 秒前`
  if (seconds < 3600) return `${Math.round(seconds / 60)} 分钟前`
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(date)
}

// 将渠道健康枚举转换为总览页使用的中文状态。
function formatHealthState(value: string): string {
  const labels: Record<string, string> = {
    healthy: '健康',
    degraded: '降级',
    cooldown: '冷却中',
    half_open: '半开放',
    auto_disabled: '自动禁用',
  }
  return (labels[value] ?? value) || '未知'
}

// 绘制不依赖第三方图表的迷你趋势条，保持管理台信息密度和响应式稳定性。
function TrendSparkline({ values, tone }: { values: number[]; tone: 'blue' | 'green' }) {
  const max = Math.max(...values, 1)
  const normalized = values.length > 1 ? values : [0, 0]
  const points = normalized
    .map(
      (value, index) => `${(index / (normalized.length - 1)) * 118 + 1},${54 - (value / max) * 48}`,
    )
    .join(' ')
  return (
    <div className={`metric-sparkline metric-sparkline-${tone}`} aria-label="24 小时趋势">
      <svg viewBox="0 0 120 56" role="img" aria-hidden="true">
        <polyline points={points} />
      </svg>
    </div>
  )
}

// 渲染一张带趋势图和变化说明的核心指标卡。
function MetricCard({
  label,
  value,
  change,
  values,
  tone,
  note,
  favorableDirection = 'none',
}: {
  label: string
  value: string
  change: number | null
  values: number[]
  tone: 'blue' | 'green'
  note: string
  favorableDirection?: 'up' | 'down' | 'none'
}) {
  const changeLabel =
    change === null
      ? '无对比基线'
      : `${change >= 0 ? '↗' : '↘'} ${Math.abs(change * 100).toFixed(1)}% 较前 24 小时`
  const favorable =
    change !== null &&
    ((favorableDirection === 'up' && change > 0) || (favorableDirection === 'down' && change < 0))
  return (
    <Card className="overview-metric-card apple-card" bordered={false}>
      <div className="metric-card-copy">
        <span className="metric-label">{label}</span>
        <strong className="metric-value">{value}</strong>
        <small className={favorable ? 'metric-change metric-change-positive' : 'metric-change'}>
          {changeLabel}
        </small>
        <small className="metric-note">{note}</small>
      </div>
      <TrendSparkline values={values} tone={tone} />
    </Card>
  )
}

// 格式化系统运行时长；不足 1 分钟单独提示，避免显示 0 小时 0 分钟。
function formatUptime(seconds: number): string {
  if (!seconds) return '—'
  if (seconds < 60) return '不足 1 分钟'
  const days = Math.floor(seconds / 86400)
  const hours = Math.floor((seconds % 86400) / 3600)
  const minutes = Math.floor((seconds % 3600) / 60)
  return days > 0 ? `${days} 天 ${hours} 小时 ${minutes} 分钟` : `${hours} 小时 ${minutes} 分钟`
}

// 格式化内存占用，保持单位和参考图一致。
function formatMemory(bytes: number): string {
  if (!bytes) return '—'
  return bytes >= 1024 * 1024 * 1024
    ? `${(bytes / 1024 / 1024 / 1024).toFixed(2)} GB`
    : `${Math.round(bytes / 1024 / 1024)} MB`
}

// 格式化服务启动时间，无法解析时保留后端原始值。
function formatDateTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value || '—'
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(date)
}

// 渲染运行总览看板，集中展示服务入口、渠道健康、最近请求和系统信息。
function OverviewPage({ active }: OverviewPageProps) {
  const [data, setData] = useState<OverviewData>(emptyData)
  const [loading, setLoading] = useState(true)
  const loadOverviewSeqRef = useRef(0)

  // 拉取最新总览数据并保留上一份数据；仅最新序号会写入状态。
  const loadOverview = useCallback(async (): Promise<void> => {
    const seq = loadOverviewSeqRef.current + 1
    loadOverviewSeqRef.current = seq
    setLoading(true)
    try {
      const next = await readOverview()
      if (seq !== loadOverviewSeqRef.current) return
      setData(next)
    } catch (reason) {
      if (seq !== loadOverviewSeqRef.current) return
      showErrorToast(reason instanceof Error ? reason.message : '读取总览失败')
    } finally {
      if (seq === loadOverviewSeqRef.current) setLoading(false)
    }
  }, [])

  // 页面失活时作废在途总览响应，避免回访后被旧数据覆盖。
  useEffect(() => {
    if (active) return
    loadOverviewSeqRef.current += 1
  }, [active])

  // 复制代理入口地址，并通过 Toast 提供完成反馈。
  async function copyAddress(address: string): Promise<void> {
    if (!address || !window.navigator.clipboard) {
      showErrorToast('当前浏览器不支持复制，请手动复制地址')
      return
    }
    try {
      await window.navigator.clipboard.writeText(address)
      showSuccessToast('代理地址已复制。')
    } catch {
      showErrorToast('复制失败，请手动复制地址')
    }
  }

  // 仅在前台激活时拉取总览，避免缓存页在后台刷新。
  useEffect(() => {
    if (!active) return
    void loadOverview()
  }, [active, loadOverview])

  const healthyChannels = data.channels.filter(
    (channel) =>
      channel.adminState === 'enabled' &&
      channel.credentialConfigured &&
      channel.healthState === 'healthy' &&
      (channel.hasRoutableModel ?? channel.modelCount > 0),
  ).length
  const disabledChannels = data.channels.filter(
    (channel) => channel.adminState !== 'enabled',
  ).length
  const missingCredentialChannels = data.channels.filter(
    (channel) => !channel.credentialConfigured,
  ).length
  const missingModelChannels = data.channels.filter(
    (channel) => !(channel.hasRoutableModel ?? channel.modelCount > 0),
  ).length
  const unhealthyChannels = data.channels.filter(
    (channel) => channel.healthState !== 'healthy',
  ).length
  const channelIssueSummary = [
    `${healthyChannels} / ${data.channels.length} 个渠道可用`,
    disabledChannels > 0 ? `${disabledChannels} 个已禁用` : '',
    missingCredentialChannels > 0 ? `${missingCredentialChannels} 个缺少凭证` : '',
    missingModelChannels > 0 ? `${missingModelChannels} 个无模型` : '',
    unhealthyChannels > 0 ? `${unhealthyChannels} 个健康异常` : '',
  ]
    .filter(Boolean)
    .join(' · ')
  const requestValues = data.trends.map((trend) => trend.requestCount)
  const successValues = data.trends.map((trend) => trend.successRate)
  const latencyValues = data.trends.map((trend) => trend.p95LatencyMs)
  const fallbackValues = data.trends.map((trend) => trend.fallbackRequestCount)
  const displayMetrics = data.metrics

  const channelColumns = useMemo(
    () => [
      {
        title: '渠道',
        dataIndex: 'name',
        key: 'name',
        width: 190,
        render: (_value: unknown, channel: OverviewChannel) => (
          <div className="table-primary-cell">
            <strong>{channel.name}</strong>
            <span>
              {protocolLabel(channel.protocol)} · {channel.modelCount} 个模型
            </span>
          </div>
        ),
      },
      {
        title: '状态',
        dataIndex: 'healthState',
        key: 'healthState',
        width: 126,
        render: (value: string, channel: OverviewChannel) => {
          const hasModel = channel.hasRoutableModel ?? channel.modelCount > 0
          const available =
            channel.adminState === 'enabled' &&
            channel.credentialConfigured &&
            value === 'healthy' &&
            hasModel
          const label =
            channel.adminState !== 'enabled'
              ? '已禁用'
              : !channel.credentialConfigured
                ? '缺少凭证'
                : !hasModel
                  ? '无模型'
                  : formatHealthState(value)
          return (
            <span className={available ? 'health-cell' : 'health-cell is-unavailable'}>
              <i className={`health-dot ${available ? 'is-healthy' : 'is-warning'}`} />
              <span>{label}</span>
            </span>
          )
        },
      },
      {
        title: '上游',
        dataIndex: 'baseUrl',
        key: 'baseUrl',
        width: 230,
        render: (value: string) => (
          <Tooltip content={value || '—'} position="top">
            <span className="table-url">{value || '—'}</span>
          </Tooltip>
        ),
      },
      {
        title: '成功率',
        dataIndex: 'successRate',
        key: 'successRate',
        width: 150,
        render: (value: number) => (
          <span className="rate-cell">
            <strong>{formatPercent(value)}</strong>
            <i>
              <b style={{ width: `${Math.min(100, Math.max(0, value * 100))}%` }} />
            </i>
          </span>
        ),
      },
      {
        title: '延迟 (P95)',
        dataIndex: 'p95LatencyMs',
        key: 'p95LatencyMs',
        width: 130,
        align: 'right' as const,
        render: (value: number) => <span>{formatLatency(value)}</span>,
      },
      {
        title: '请求 (24H)',
        dataIndex: 'requests24h',
        key: 'requests24h',
        width: 130,
        align: 'right' as const,
        render: (value: number) => <span>{formatNumber(value)}</span>,
      },
      {
        title: '',
        key: 'actions',
        width: 62,
        align: 'right' as const,
        fixed: 'right' as const,
        onHeaderCell: () => ({ resize: false }),
        render: (_value: unknown, channel: OverviewChannel) => (
          <Tooltip content="编辑渠道">
            <Button
              theme="borderless"
              icon={<IconEdit />}
              aria-label={`编辑 ${channel.name}`}
              onClick={() => {
                window.location.hash = `#channels/${encodeURIComponent(channel.id)}/edit`
              }}
            />
          </Tooltip>
        ),
      },
    ],
    [],
  )

  const requestColumns = useMemo(
    () => [
      {
        title: '时间',
        dataIndex: 'startedAt',
        key: 'startedAt',
        width: 126,
        render: (value: string) => <span>{formatRelativeTime(value)}</span>,
      },
      {
        title: '渠道',
        dataIndex: 'finalChannelName',
        key: 'finalChannelName',
        width: 150,
        render: (value: string) => <span>{value || '未完成'}</span>,
      },
      {
        title: '模型',
        dataIndex: 'clientModel',
        key: 'clientModel',
        width: 190,
        render: (value: string, request: OverviewRequest) => (
          <div className="table-primary-cell">
            <strong>{value}</strong>
            <span>{protocolLabel(request.protocol)}</span>
          </div>
        ),
      },
      {
        title: '状态',
        dataIndex: 'finalStatus',
        key: 'finalStatus',
        width: 110,
        render: (value: string) => (
          <Tag color={statusColor(value)}>{statusText[value] ?? value}</Tag>
        ),
      },
      {
        title: '延迟',
        dataIndex: 'latencyMs',
        key: 'latencyMs',
        width: 108,
        align: 'right' as const,
        render: (value: number) => <span>{formatLatency(value)}</span>,
      },
      {
        title: 'Tokens',
        dataIndex: 'tokens',
        key: 'tokens',
        width: 100,
        align: 'right' as const,
        render: (value: number | null) => <span>{value == null ? '—' : formatNumber(value)}</span>,
      },
      {
        title: '尝试',
        dataIndex: 'attemptCount',
        key: 'attemptCount',
        width: 74,
        align: 'right' as const,
        render: (value: number) => <span>{value || '—'}</span>,
      },
      {
        title: '',
        key: 'actions',
        width: 58,
        align: 'right' as const,
        fixed: 'right' as const,
        onHeaderCell: () => ({ resize: false }),
        render: (_value: unknown, request: OverviewRequest) => (
          <Tooltip content="查看请求详情">
            <Button
              theme="borderless"
              icon={<IconExternalOpen />}
              aria-label={`查看请求 ${request.id}`}
              onClick={() => {
                window.location.hash = `#requests?request=${encodeURIComponent(request.id)}`
              }}
            />
          </Tooltip>
        ),
      },
    ],
    [],
  )

  return (
    <section className="overview-shell page-content">
      <section className="overview-page" aria-label="总览">
        <div className="overview-metric-row">
          <MetricCard
            label="请求数 (24H)"
            value={loading ? '同步中' : formatNumber(displayMetrics.requestCount)}
            change={displayMetrics.requestChange}
            values={requestValues}
            tone="blue"
            note={`${formatNumber(displayMetrics.requestSuccessCount)} 次成功请求`}
          />
          <MetricCard
            label="成功率 (24H)"
            value={loading ? '同步中' : formatPercent(displayMetrics.successRate)}
            change={displayMetrics.successRateChange}
            values={successValues}
            tone="green"
            note={`${formatNumber(displayMetrics.requestFailureCount)} 次失败请求`}
            favorableDirection="up"
          />
          <MetricCard
            label="延迟 (P95)"
            value={loading ? '同步中' : formatLatency(displayMetrics.p95LatencyMs)}
            change={displayMetrics.p95LatencyChange}
            values={latencyValues}
            tone="blue"
            note={`平均延迟 ${formatLatency(displayMetrics.averageLatencyMs)}`}
            favorableDirection="down"
          />
          <MetricCard
            label="Fallback (24H)"
            value={loading ? '同步中' : formatNumber(displayMetrics.fallbackRequestCount)}
            change={displayMetrics.fallbackChange}
            values={fallbackValues}
            tone="blue"
            note={`占请求 ${formatPercent(displayMetrics.fallbackRate)}`}
            favorableDirection="down"
          />
        </div>
        <section
          className="data-panel channel-health-panel apple-card"
          aria-labelledby="overview-channels-title"
        >
          <div className="data-panel-heading">
            <div>
              <h2 id="overview-channels-title">渠道健康</h2>
              <span className="panel-subtitle">{channelIssueSummary}</span>
            </div>
            <a className="panel-link" href="#channels">
              查看全部
            </a>
          </div>
          <div className="overview-table-wrap">
            <Table<OverviewChannel>
              className="overview-table channel-health-table"
              rowKey="id"
              loading={loading}
              columns={channelColumns}
              dataSource={data.channels}
              empty={
                <div className="overview-channel-empty">
                  <span>暂无渠道配置</span>
                  <Button
                    theme="solid"
                    type="primary"
                    onClick={() => {
                      window.location.hash = '#channels/new'
                    }}
                  >
                    新增渠道
                  </Button>
                </div>
              }
              pagination={false}
              resizable
              scroll={{ x: 1018 }}
            />
          </div>
        </section>
        <section
          className="data-panel recent-requests-panel apple-card"
          aria-labelledby="overview-requests-title"
        >
          <div className="data-panel-heading">
            <div>
              <h2 id="overview-requests-title">最近请求</h2>
              <span className="panel-subtitle">最近 24 小时的请求记录</span>
            </div>
            <a className="panel-link" href="#requests">
              查看全部日志
            </a>
          </div>
          <div className="overview-table-wrap">
            <Table<OverviewRequest>
              className="overview-table recent-requests-table"
              rowKey="id"
              loading={loading}
              columns={requestColumns}
              dataSource={data.recentRequests}
              empty="暂无请求记录"
              pagination={false}
              resizable
              scroll={{ x: 1016 }}
            />
          </div>
        </section>
        <div className="overview-utility-row">
          <Card className="proxy-access-card apple-card" bordered={false}>
            <div className="panel-heading">
              <div>
                <span className="panel-icon">◎</span>
                <h2>Proxy Access</h2>
              </div>
              <Tag color="green">本机运行</Tag>
            </div>
            <p className="panel-description">
              将此地址配置到 Codex、Claude Code 或其他兼容客户端。
            </p>
            <div className="access-field">
              <span>Base URL</span>
              <strong>{data.proxyAccess.baseUrl || '—'}</strong>
              <div className="access-actions">
                <Button
                  theme="light"
                  icon={<IconCopy />}
                  aria-label="复制代理地址"
                  title="复制代理地址"
                  disabled={!data.proxyAccess.baseUrl}
                  onClick={() => void copyAddress(data.proxyAccess.baseUrl)}
                />
              </div>
            </div>
            <div className="access-field access-field-secondary">
              <span>Web UI</span>
              <strong className="access-link">{data.proxyAccess.webUrl || '—'}</strong>
              <Button
                theme="borderless"
                icon={<IconExternalOpen />}
                aria-label="打开管理页面"
                title="打开管理页面"
                disabled={!data.proxyAccess.webUrl}
                onClick={() => {
                  if (!data.proxyAccess.webUrl) return
                  window.open(data.proxyAccess.webUrl, '_blank', 'noopener,noreferrer')
                }}
              />
            </div>
            <a className="quick-link" href="#settings">
              查看快速接入设置 <span>›</span>
            </a>
          </Card>
          <Card className="system-info-card apple-card" bordered={false}>
            <div className="data-panel-heading">
              <div>
                <h2>系统信息</h2>
                <span className="panel-subtitle">运行时状态</span>
              </div>
            </div>
            <dl className="system-info-list">
              <div>
                <dt>版本</dt>
                <dd>{data.systemInfo.version || '—'}</dd>
              </div>
              <div>
                <dt>运行时间</dt>
                <dd>{formatUptime(data.systemInfo.uptimeSeconds)}</dd>
              </div>
              <div>
                <dt>操作系统</dt>
                <dd>{data.systemInfo.os || '—'}</dd>
              </div>
              <div>
                <dt>Go 版本</dt>
                <dd>{data.systemInfo.goVersion || '—'}</dd>
              </div>
              <div>
                <dt>内存</dt>
                <dd>
                  {formatMemory(data.systemInfo.memoryAlloc)} /{' '}
                  {formatMemory(data.systemInfo.memoryBytes)}
                </dd>
              </div>
              <div>
                <dt>Goroutines</dt>
                <dd>{data.systemInfo.goroutines || '—'}</dd>
              </div>
              <div>
                <dt>启动时间</dt>
                <dd title={formatDateTime(data.systemInfo.startedAt)}>
                  {formatDateTime(data.systemInfo.startedAt)}
                </dd>
              </div>
            </dl>
            <a className="diagnostics-link" href="#logs">
              <span>⌁</span> 查看运行日志 <IconExternalOpen />
            </a>
          </Card>
        </div>
      </section>
    </section>
  )
}

export default OverviewPage
