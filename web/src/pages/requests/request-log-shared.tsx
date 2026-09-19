import { Card, CodeHighlight, JsonViewer } from '@douyinfe/semi-ui-19'
import type { ColumnProps } from '@douyinfe/semi-ui-19/lib/es/table/interface'
import type React from 'react'
import type { AppTheme, ThemePreference } from '../../components/PageHeader'

export type Stats = {
  from: string
  to: string
  requestCount: number
  requestFailureCount: number
  fallbackRequestCount: number
  attemptCount: number
  averageLatencyMs: number
  averageTTFTMs?: number | null
  p95TTFTMs?: number | null
  p95TTFTOverflow?: boolean
  totalTokens?: number | null
  inputTokens?: number | null
  outputTokens?: number | null
  cacheReadInputTokens?: number | null
  successRate: number
  fallbackRate: number
}
export type OverviewTrend = {
  requestCount: number
  successRate: number
  p95LatencyMs: number
}
export type OverviewData = {
  metrics: { requestChange: number | null }
  trends: OverviewTrend[]
}
export type Channel = { id: string; name: string; group?: string }
export type Blob = {
  id: string
  requestId: string
  attemptId?: string
  contentType: string
  size: number
  originalSize?: number
  savedSize?: number
  truncated: boolean
}
export type Attempt = {
  id: string
  channelId: string
  channelName?: string
  groupName?: string
  protocol: string
  clientModel?: string
  upstreamModel?: string
  logicalModel?: string
  sequence: number
  status: string
  errorClass?: string
  errorMessage?: string
  retryReason?: string
  fallbackTriggered: boolean
  startedAt?: string
  firstByteAt?: string
  firstEventAt?: string
  completedAt?: string
  httpStatus?: number | null
  latencyMs: number
  inputTokens: number | null
  outputTokens: number | null
  cacheReadInputTokens: number | null
  cacheWriteInputTokens: number | null
  reasoningTokens: number | null
  totalTokens: number | null
  streamEnded?: boolean | null
}
export type RouteEvent = {
  requestId: string
  sequence: number
  eventType: 'candidate_skipped' | 'channel_switched' | 'health_changed' | 'routing_exhausted' | 'custom_headers_ignored'
  channelId?: string
  relatedChannelId?: string
  reason?: string
  details: Record<string, unknown>
  occurredAt: string
}
export type Request = {
  id: string
  protocol: string
  clientModel: string
  logicalModel: string
  groupName?: string
  initialChannelName?: string
  finalChannelName?: string
  channelChain?: string[]
  channelSwitchCount?: number
  finalStatus: string
  startedAt: string
  completedAt?: string
  latencyMs: number
  attemptCount: number
  fallbackTriggered: boolean
  inputTokens: number | null
  outputTokens: number | null
  cacheReadInputTokens: number | null
  cacheWriteInputTokens: number | null
  reasoningTokens: number | null
  totalTokens: number | null
  cacheRate: number | null
  ttftMs: number | null
  tps: number | null
  streamEnded: boolean | null
  finalHttpStatus: number | null
  errorClass?: string
  errorMessage?: string
  attempts: Attempt[]
  routeEvents?: RouteEvent[]
  contentBlobs?: Blob[]
}
export type PageData = {
  items: Request[]
  page: number
  pageSize: number
  total: number
  hasMore: boolean
}
export type RequestFilter = {
  period: number
  model: string
  protocol: string
  status: string
  channel: string
  group: string
  errorClass: string
  fallback: string
  keyword: string
}
export type Props = {
  theme: AppTheme
  themePreference: ThemePreference
  onChangeTheme: () => void
  requestId?: string
  active?: boolean
}
export type ColumnKey =
  | 'startedAt'
  | 'channel'
  | 'model'
  | 'status'
  | 'protocol'
  | 'group'
  | 'input'
  | 'output'
  | 'cacheRead'
  | 'cacheWrite'
  | 'reasoning'
  | 'total'
  | 'cacheRate'
  | 'ttft'
  | 'tps'
  | 'latency'
  | 'detail'
export type ColumnWidthMap = Partial<Record<ColumnKey, number>>
export type RequestColumn = ColumnProps<Request> & { key: ColumnKey; width: number }

export const defaultFilter: RequestFilter = {
  period: 1,
  model: '',
  protocol: '',
  status: '',
  channel: '',
  group: '',
  errorClass: '',
  fallback: '',
  keyword: '',
}
export const columnOptions: Array<{ key: ColumnKey; label: string }> = [
  { key: 'startedAt', label: '请求时间' },
  { key: 'channel', label: '渠道' },
  { key: 'model', label: '模型' },
  { key: 'status', label: '状态' },
  { key: 'protocol', label: '协议' },
  { key: 'group', label: '分组' },
  { key: 'input', label: '输入' },
  { key: 'output', label: '输出' },
  { key: 'cacheRead', label: '缓存读取' },
  { key: 'cacheWrite', label: '缓存写入' },
  { key: 'reasoning', label: '思考' },
  { key: 'total', label: '总 Tokens' },
  { key: 'cacheRate', label: '缓存率' },
  { key: 'ttft', label: 'TTFT' },
  { key: 'tps', label: 'TPS' },
  { key: 'latency', label: '耗时' },
  { key: 'detail', label: '详情' },
]
export const defaultColumnWidths: Record<ColumnKey, number> = {
  startedAt: 190,
  channel: 220,
  model: 260,
  status: 150,
  protocol: 180,
  group: 120,
  input: 100,
  output: 100,
  cacheRead: 110,
  cacheWrite: 110,
  reasoning: 100,
  total: 120,
  cacheRate: 100,
  ttft: 100,
  tps: 90,
  latency: 100,
  detail: 90,
}
export const statusText: Record<string, string> = {
  processing: '处理中',
  success: '成功',
  failed: '失败',
  error: '失败',
  timeout: '超时',
  cancelled: '已取消',
  partial: '部分中断',
  stream_interrupted: '流中断',
}
export const requestStatusValues = [
  'processing',
  'success',
  'error',
  'timeout',
  'cancelled',
  'partial',
]
export const errorOptions = [
  'http_429',
  'upstream_auth_401',
  'upstream_403',
  'http_408',
  'http_409',
  'http_5xx',
  'first_byte_timeout',
  'timeout',
  'stream_postcommit_disconnect',
  'stream_idle_timeout',
  'network_interrupted',
  'cancelled',
  'upstream_http',
  'upstream_protocol',
  'ledger_error',
]

// 从浏览器本地恢复请求表格列设置，异常值回退为全部默认列。
export function getInitialColumns(): ColumnKey[] {
  try {
    const saved = JSON.parse(window.localStorage.getItem('oneai-proxy-request-columns') ?? '[]')
    const allowed = new Set(columnOptions.map((item) => item.key))
    const columns = Array.isArray(saved)
      ? saved.filter(
          (item): item is ColumnKey => typeof item === 'string' && allowed.has(item as ColumnKey),
        )
      : []
    return columns.length > 0 ? columns : columnOptions.map((item) => item.key)
  } catch {
    return columnOptions.map((item) => item.key)
  }
}

// 从浏览器本地恢复请求表格列宽，只接受合理范围内的数值。
export function getInitialColumnWidths(): ColumnWidthMap {
  try {
    const saved = JSON.parse(
      window.localStorage.getItem('oneai-proxy-request-column-widths') ?? '{}',
    ) as unknown
    if (!saved || typeof saved !== 'object' || Array.isArray(saved)) return {}
    const record = saved as Record<string, unknown>
    const widths: ColumnWidthMap = {}
    columnOptions.forEach(({ key }) => {
      const width = record[key]
      if (typeof width === 'number' && Number.isFinite(width) && width >= 64 && width <= 800) {
        widths[key] = Math.round(width)
      }
    })
    return widths
  } catch {
    return {}
  }
}

// 将请求状态映射为统一的 Semi Tag 颜色。
export function statusColor(value: string): 'green' | 'red' | 'orange' | 'blue' | 'grey' {
  if (value === 'success') return 'green'
  if (value === 'processing') return 'blue'
  if (value === 'timeout') return 'orange'
  if (value === 'cancelled') return 'grey'
  return 'red'
}

// 格式化整数；未知值严格显示破折号，不伪造为零。
export function formatNumber(value: number | null | undefined): string {
  return value == null ? '—' : value.toLocaleString('zh-CN')
}

// 将大 Token 数压缩成适合指标卡的 K/M 展示。
export function formatCompactNumber(value: number | null | undefined): string {
  if (value == null) return '—'
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(2)}M`
  if (value >= 10_000) return `${(value / 1_000).toFixed(1)}K`
  return formatNumber(value)
}

// 格式化毫秒耗时并保持表格数字宽度稳定。
export function formatDuration(value: number | null | undefined): string {
  if (value == null) return '—'
  return value >= 1000 ? `${(value / 1000).toFixed(2)} s` : `${Math.round(value)} ms`
}

// 格式化带固定延迟桶的分位耗时，命中无穷桶时明确提示超过上限。
export function formatPercentileDuration(
  value: number | null | undefined,
  overflow = false,
): string {
  return overflow ? '超过 60s' : formatDuration(value)
}

// 格式化请求列表时间为完整的本地日期和时间。
export function formatListDate(value: string): string {
  const parsed = new Date(value)
  if (Number.isNaN(parsed.valueOf())) return value
  const pad = (part: number) => String(part).padStart(2, '0')
  return `${parsed.getFullYear()}-${pad(parsed.getMonth() + 1)}-${pad(parsed.getDate())} ${pad(parsed.getHours())}:${pad(parsed.getMinutes())}:${pad(parsed.getSeconds())}`
}

// 格式化详情页完整时间，无法解析时保留服务端原值。
export function formatDetailDate(value: string | undefined): string {
  if (!value) return '—'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.valueOf())) return value
  return parsed.toLocaleString('zh-CN', { hour12: false })
}

// 格式化百分比，未知值显示破折号。
export function formatPercent(value: number | null | undefined, digits = 1): string {
  return value == null ? '—' : `${(value * 100).toFixed(digits)}%`
}

// 将正文快照类型转换为详情页中的业务名称。
export function snapshotLabel(value: string): string {
  const labels: Record<string, string> = {
    request_headers: '请求头',
    request: '请求体',
    response_headers: '最终响应头',
    response: '最终响应体',
    upstream_request_headers: '上游请求头',
    upstream_request_body: '上游请求体',
    upstream_response_headers: '上游响应头',
    upstream_response_body: '上游响应体',
  }
  return labels[value] ?? value
}

// 保留正文快照的原始字符内容；管理 API 已在返回前完成凭证字段脱敏。
export function formatSnapshotContent(value: string): string {
  return value
}

// 使用 Semi 内置查看器美化 JSON，否则保留原始文本并应用代码高亮容器。
export function renderSnapshotContent(value: string): React.ReactNode {
  try {
    const parsed = JSON.parse(value) as unknown
    const formatted = JSON.stringify(parsed, null, 2) ?? value
    return (
      <JsonViewer
        className="snapshot-json-viewer"
        value={formatted}
        width="100%"
        height={480}
        showSearch
        options={{ readOnly: true, autoWrap: true }}
      />
    )
  } catch {
    return (
      <CodeHighlight
        className="snapshot-code-highlight"
        code={value}
        language="text"
        lineNumber
        defaultTheme
      />
    )
  }
}

// 计算单次 Attempt 从开始到首个有效事件的高精度毫秒数。
export function attemptTTFT(attempt: Attempt): number | null {
  if (!attempt.firstEventAt || !attempt.startedAt) return null
  const value = new Date(attempt.firstEventAt).valueOf() - new Date(attempt.startedAt).valueOf()
  return Number.isFinite(value) && value >= 0 ? value : null
}

// 计算当前详情默认展开的 Attempt：优先成功 Attempt，否则最后一次。
export function defaultExpandedAttempt(attempts: Attempt[]): string[] {
  for (let index = attempts.length - 1; index >= 0; index -= 1) {
    if (attempts[index].status === 'success') return [attempts[index].id]
  }
  return attempts.length > 0 ? [attempts[attempts.length - 1].id] : []
}

// 绘制与总览指标卡一致的轻量趋势折线。
export function RequestSparkline({
  values,
  tone,
  label,
}: {
  values: number[]
  tone: 'blue' | 'green'
  label: string
}) {
  const normalized = values.length > 1 ? values : [0, 0]
  const max = Math.max(...normalized, 1)
  const points = normalized
    .map(
      (value, index) => `${(index / (normalized.length - 1)) * 118 + 1},${54 - (value / max) * 48}`,
    )
    .join(' ')
  return (
    <div className={`metric-sparkline metric-sparkline-${tone}`} aria-label={label}>
      <svg viewBox="0 0 120 56" role="img" aria-hidden="true">
        <polyline points={points} />
      </svg>
    </div>
  )
}

// 渲染请求页顶部的一张核心指标卡。
export function RequestMetricCard({
  label,
  value,
  change,
  note,
  values,
  trendLabel,
  tone = 'blue',
  favorableDirection = 'none',
}: {
  label: string
  value: string
  change: number | null
  note: string
  values: number[]
  trendLabel: string
  tone?: 'blue' | 'green'
  favorableDirection?: 'up' | 'down' | 'none'
}) {
  const changeLabel =
    change === null
      ? trendLabel
        ? '无对比基线'
        : ''
      : `${change >= 0 ? '↗' : '↘'} ${Math.abs(change * 100).toFixed(1)}% 较前 24 小时`
  const favorable =
    change !== null &&
    ((favorableDirection === 'up' && change > 0) || (favorableDirection === 'down' && change < 0))
  return (
    <Card className="overview-metric-card apple-card" bordered={false}>
      <div className="metric-card-copy">
        <span className="metric-label">{label}</span>
        <strong className="metric-value">{value}</strong>
        {changeLabel ? (
          <small className={favorable ? 'metric-change metric-change-positive' : 'metric-change'}>
            {changeLabel}
          </small>
        ) : null}
        <small className="metric-note">{note}</small>
      </div>
      {trendLabel ? <RequestSparkline values={values} tone={tone} label={trendLabel} /> : null}
    </Card>
  )
}

// 渲染一组可按需读取的加密正文快照。
export function SnapshotGroup({
  title,
  blobs,
  snapshot,
  onLoad,
}: {
  title: string
  blobs: Blob[]
  snapshot: Record<string, string>
  onLoad: (blob: Blob) => void
}) {
  if (blobs.length === 0) return null
  return (
    <section className="snapshot-group">
      <h4>{title}</h4>
      <div className="snapshot-items">
        {blobs.map((blob) => (
          <details
            key={blob.id}
            onToggle={(event) => {
              if (event.currentTarget.open) onLoad(blob)
            }}
          >
            <summary>
              <span>{snapshotLabel(blob.contentType)}</span>
              <small>
                保存{' '}
                {(blob.savedSize ?? blob.size) >= 1024
                  ? `${((blob.savedSize ?? blob.size) / 1024).toFixed(1)} KB`
                  : `${blob.savedSize ?? blob.size} B`}
                {blob.originalSize != null && blob.originalSize !== (blob.savedSize ?? blob.size)
                  ? ` · 原始 ${(blob.originalSize / 1024).toFixed(1)} KB`
                  : ''}
                {blob.truncated ? ' · 已截断' : ''}
              </small>
            </summary>
            {snapshot[blob.id] === undefined ? (
              <pre className="snapshot-loading">正在读取加密快照…</pre>
            ) : (
              renderSnapshotContent(formatSnapshotContent(snapshot[blob.id]))
            )}
          </details>
        ))}
      </div>
    </section>
  )
}
