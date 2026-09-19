import { useCallback, useEffect, useMemo, useRef, useState, type UIEvent } from 'react'
import {
  Button,
  Checkbox,
  Empty,
  Input,
  Pagination,
  Popover,
  Select,
  Spin,
  Table,
  Tag,
  Tooltip,
} from '@douyinfe/semi-ui-19'
import { IconArrowDown, IconColumnsStroked, IconRefresh, IconSearch } from '@douyinfe/semi-icons'
import type { ColumnProps } from '@douyinfe/semi-ui-19/lib/es/table/interface'
import { adminError, adminFetch } from '../../api'
import { showErrorToast, showSuccessToast } from '../../notifications'
import type { AppTheme, ThemePreference } from '../../components/PageHeader'
import { useTableScrollY } from '../../hooks/useTableScrollY'

type LogsPageProps = {
  theme: AppTheme
  themePreference: ThemePreference
  onChangeTheme: () => void
  active?: boolean
}

type RuntimeLog = {
  id: string
  occurredAt: string
  level: string
  event: string
  message: string
  requestId?: string
  attemptId?: string
  channelId?: string
  context?: Record<string, unknown>
}

type AuditLog = {
  id: string
  actorType: string
  action: string
  targetId: string
  category: string
  result: string
  operatorIp: string
  details?: Record<string, unknown>
  occurredAt: string
}

type AuditPageData = {
  items: AuditLog[]
  page: number
  pageSize: number
  total: number
  hasMore: boolean
}

type AuditFilter = {
  period: number
  category: string
  result: string
  target: string
  operatorIp: string
}

type LogColumnKey = 'time' | 'category' | 'result' | 'target' | 'ip' | 'operator' | 'action'
type RuntimeLevelFilter = 'debug' | 'info' | 'warn' | 'error'

const auditColumnOptions: Array<{ key: LogColumnKey; label: string }> = [
  { key: 'time', label: '时间' },
  { key: 'category', label: '分类' },
  { key: 'action', label: '操作' },
  { key: 'target', label: '目标对象' },
  { key: 'result', label: '结果' },
  { key: 'ip', label: '操作 IP' },
  { key: 'operator', label: '操作者' },
]

const defaultWidths: Record<string, number> = {
  time: 150,
  category: 100,
  action: 180,
  target: 180,
  result: 92,
  ip: 130,
  operator: 110,
}

const defaultAuditFilter: AuditFilter = {
  period: 30,
  category: '',
  result: '',
  target: '',
  operatorIp: '',
}

const runtimeLevelRank: Record<RuntimeLevelFilter, number> = {
  debug: 0,
  info: 1,
  warn: 2,
  error: 3,
}

const adminTokenChangedEvent = 'oneai-proxy-admin-token-changed'
const adminTokenStorageKey = 'oneai-proxy-admin-token'
const runtimeReconnectMs = 3000

type RuntimeSseEvent = {
  event: string
  data: string
  id: string
}

// 将 UTC 日志时间转换为当前浏览器时区的完整时间。
function formatTime(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value || '—'
  return date.toLocaleString('zh-CN', { hour12: false })
}

// 将操作结果映射为统一的状态色。
function resultColor(result: string): 'green' | 'orange' | 'red' | 'grey' {
  if (result === 'success') return 'green'
  if (result === 'rejected') return 'orange'
  if (result === 'failed' || result === 'error') return 'red'
  return 'grey'
}

// 将操作结果转换为中文展示文案。
function resultLabel(result: string): string {
  return (
    ({ success: '成功', failed: '失败', error: '失败', rejected: '拒绝' }[result] ?? result) ||
    '未知'
  )
}

// 从运行事件结构化上下文读取可展示文本。
function runtimeContextValue(item: RuntimeLog, key: string): string {
  const value = item.context?.[key]
  if (value == null || value === '') return ''
  return typeof value === 'string' ? value : JSON.stringify(value)
}

// 将运行事件的结构化字段压缩为 Docker 风格的行内元信息。
function runtimeMeta(item: RuntimeLog): string {
  const values = [
    item.requestId ? `request=${item.requestId}` : '',
    item.attemptId ? `attempt=${item.attemptId}` : '',
    item.channelId ? `channel=${item.channelId}` : '',
    ...(
      ['method', 'path', 'status', 'durationMs', 'remoteIP', 'model', 'protocol', 'error'] as const
    ).map((key) => {
      const value = runtimeContextValue(item, key)
      return value ? `${key}=${value}` : ''
    }),
  ].filter(Boolean)
  return values.join('  ')
}

// 递归隐藏操作详情中的令牌、Header、正文和 Cookie。
function redactAuditDetails(value: unknown): unknown {
  if (Array.isArray(value)) return value.map((item) => redactAuditDetails(item))
  if (!value || typeof value !== 'object') return value
  return Object.fromEntries(
    Object.entries(value).map(([key, child]) => {
      const normalized = key.toLowerCase().replaceAll('_', '-')
      const sensitive =
        normalized.includes('authorization') ||
        normalized.includes('api-key') ||
        normalized.includes('token') ||
        normalized.includes('secret') ||
        normalized.includes('password') ||
        normalized.includes('cookie') ||
        normalized.includes('header') ||
        normalized.includes('body')
      return [key, sensitive ? '***' : redactAuditDetails(child)]
    }),
  )
}

// 将操作日志详情转换为脱敏后的可读 JSON。
function formatAuditDetails(value?: Record<string, unknown>): string {
  try {
    return JSON.stringify(redactAuditDetails(value ?? {}), null, 2)
  } catch {
    return '{}'
  }
}

// 读取并校验浏览器保存的日志列显隐设置。
function readColumns(
  storageKey: string,
  options: Array<{ key: LogColumnKey; label: string }>,
): LogColumnKey[] {
  try {
    const saved = JSON.parse(window.localStorage.getItem(storageKey) ?? 'null')
    if (Array.isArray(saved)) {
      const allowed = new Set(options.map((item) => item.key))
      const columns = saved.filter(
        (item): item is LogColumnKey =>
          typeof item === 'string' && allowed.has(item as LogColumnKey),
      )
      if (columns.length > 0) return columns
    }
  } catch {
    // 忽略损坏的本地列设置，回退为默认列。
  }
  return options.map((item) => item.key)
}

// 读取浏览器保存的日志列宽设置。
function readWidths(storageKey: string): Record<string, number> {
  try {
    const saved = JSON.parse(window.localStorage.getItem(storageKey) ?? 'null')
    if (saved && typeof saved === 'object') return { ...defaultWidths, ...saved }
  } catch {
    // 忽略损坏的本地列宽设置。
  }
  return { ...defaultWidths }
}

// 从 SSE 文本缓冲中拆出完整事件，剩余半包留待下次拼接。
function takeSseEvents(buffer: string): { events: RuntimeSseEvent[]; rest: string } {
  const events: RuntimeSseEvent[] = []
  const chunks = buffer.replaceAll('\r\n', '\n').split('\n\n')
  const rest = chunks.pop() ?? ''
  for (const chunk of chunks) {
    if (!chunk.trim()) continue
    let event = 'message'
    let id = ''
    const dataLines: string[] = []
    for (const rawLine of chunk.split('\n')) {
      const line = rawLine.replace(/\r$/, '')
      if (!line || line.startsWith(':')) continue
      const colon = line.indexOf(':')
      const field = colon < 0 ? line : line.slice(0, colon)
      let value = colon < 0 ? '' : line.slice(colon + 1)
      if (value.startsWith(' ')) value = value.slice(1)
      if (field === 'event') event = value
      else if (field === 'id') id = value
      else if (field === 'data') dataLines.push(value)
    }
    events.push({ event, id, data: dataLines.join('\n') })
  }
  return { events, rest }
}

// 渲染阶段 6R 的操作日志和运行日志双栏管理页。
function LogsPage({ active = true }: LogsPageProps) {
  const [runtimeItems, setRuntimeItems] = useState<RuntimeLog[]>([])
  const [runtimeLoading, setRuntimeLoading] = useState(true)
  const [runtimeFollow, setRuntimeFollow] = useState(true)
  const [runtimeNotice, setRuntimeNotice] = useState('')
  const [runtimeReloadKey, setRuntimeReloadKey] = useState(0)
  const [runtimeLevelFilter, setRuntimeLevelFilter] = useState<RuntimeLevelFilter>('info')
  const [runtimeSelected, setRuntimeSelected] = useState<RuntimeLog | null>(null)
  const runtimeRef = useRef<HTMLDivElement>(null)
  const runtimeFollowRef = useRef(true)
  const streamAbortRef = useRef<AbortController | null>(null)
  const reconnectTimerRef = useRef(0)
  const lastRuntimeIdRef = useRef('')
  const activeRef = useRef(active)
  const loadAuditSeqRef = useRef(0)
  const [auditItems, setAuditItems] = useState<AuditLog[]>([])
  const [auditLoading, setAuditLoading] = useState(true)
  const [auditLoadError, setAuditLoadError] = useState('')
  const [auditFilter, setAuditFilter] = useState<AuditFilter>(defaultAuditFilter)
  const [auditDraft, setAuditDraft] = useState<AuditFilter>(defaultAuditFilter)
  const [auditPage, setAuditPage] = useState(1)
  const [auditPageSize, setAuditPageSize] = useState(50)
  const [auditTotal, setAuditTotal] = useState(0)
  const [auditSelected, setAuditSelected] = useState<AuditLog | null>(null)
  const [auditColumns, setAuditColumns] = useState<LogColumnKey[]>(() =>
    readColumns('oneai-proxy-audit-columns', auditColumnOptions),
  )
  const [auditWidths, setAuditWidths] = useState<Record<string, number>>(() =>
    readWidths('oneai-proxy-audit-widths'),
  )
  const { tableScrollY, tableWrapRef } = useTableScrollY()

  // 将运行日志流滚动到最新一条记录。
  const scrollRuntimeToBottom = useCallback(() => {
    const element = runtimeRef.current
    if (element) element.scrollTop = element.scrollHeight
  }, [])

  // 中止当前运行日志流并取消待执行的重连。
  const closeRuntimeStream = useCallback((): void => {
    window.clearTimeout(reconnectTimerRef.current)
    reconnectTimerRef.current = 0
    streamAbortRef.current?.abort()
    streamAbortRef.current = null
  }, [])

  // 重连定时器回调始终调用最新的流读取函数，避免闭包过期。
  const consumeRuntimeStreamRef = useRef<(controller: AbortController) => Promise<void>>(
    async () => undefined,
  )

  // 用 adminFetch 读取运行日志 SSE，解析 runtime / reset，断开后仅重连流。
  const consumeRuntimeStream = useCallback(
    async (controller: AbortController): Promise<void> => {
      const query = new URLSearchParams()
      if (lastRuntimeIdRef.current) query.set('after', lastRuntimeIdRef.current)
      const streamPath = query.toString()
        ? `/api/admin/v1/logs/runtime/stream?${query.toString()}`
        : '/api/admin/v1/logs/runtime/stream'
      // 页面仍前台时按原间隔重连流，不重新拉取最近 100 条。
      const scheduleReconnect = () => {
        if (!activeRef.current) return
        setRuntimeNotice('运行日志连接已断开，浏览器将自动重连；期间记录可能缺失。')
        reconnectTimerRef.current = window.setTimeout(() => {
          if (!activeRef.current) return
          const next = new AbortController()
          streamAbortRef.current = next
          void consumeRuntimeStreamRef.current(next)
        }, runtimeReconnectMs)
      }
      try {
        const stream = await adminFetch(streamPath, { signal: controller.signal })
        if (!stream.ok) throw await adminError(stream, '连接运行日志失败')
        if (!stream.body) throw new Error('运行日志流不可用')
        setRuntimeNotice('')
        const reader = stream.body.getReader()
        const decoder = new TextDecoder()
        let buffer = ''
        while (!controller.signal.aborted) {
          const { done, value } = await reader.read()
          if (done) break
          buffer += decoder.decode(value, { stream: true })
          const parsed = takeSseEvents(buffer)
          buffer = parsed.rest
          for (const event of parsed.events) {
            if (event.id) lastRuntimeIdRef.current = event.id
            if (event.event === 'reset') {
              lastRuntimeIdRef.current = ''
              setRuntimeItems([])
              setRuntimeNotice('运行日志补发游标已过期，已清空本地列表并继续接收。')
              continue
            }
            if (event.event !== 'runtime' || !event.data) continue
            try {
              const item = JSON.parse(event.data) as RuntimeLog
              if (item.id) lastRuntimeIdRef.current = item.id
              setRuntimeItems((current) => {
                if (item.id && current.some((existing) => existing.id === item.id)) return current
                return [...current, item].slice(-2000)
              })
              if (runtimeFollowRef.current) requestAnimationFrame(scrollRuntimeToBottom)
            } catch {
              setRuntimeNotice('运行日志事件解析失败。')
            }
          }
        }
        if (!controller.signal.aborted) scheduleReconnect()
      } catch (reason) {
        if (controller.signal.aborted) return
        setRuntimeNotice(
          reason instanceof Error
            ? reason.message
            : '运行日志连接已断开，浏览器将自动重连；期间记录可能缺失。',
        )
        scheduleReconnect()
      }
    },
    [closeRuntimeStream, scrollRuntimeToBottom],
  )

  // 首次读取最近 100 条运行日志，再用当前 Bearer 建立实时 SSE 连接。
  const loadRuntime = useCallback(
    async (notify = false): Promise<void> => {
      closeRuntimeStream()
      const controller = new AbortController()
      streamAbortRef.current = controller
      setRuntimeLoading(true)
      try {
        const response = await adminFetch('/api/admin/v1/logs/runtime?limit=100', {
          signal: controller.signal,
        })
        if (!response.ok) throw await adminError(response, '读取运行日志失败')
        const payload = (await response.json()) as { items?: RuntimeLog[] }
        const items = [...(payload.items ?? [])].reverse()
        lastRuntimeIdRef.current = items.at(-1)?.id ?? ''
        setRuntimeItems(items)
        setRuntimeNotice('')
        if (notify) showSuccessToast('运行日志已刷新。')
        requestAnimationFrame(scrollRuntimeToBottom)
      } catch (reason) {
        if (controller.signal.aborted) return
        setRuntimeNotice(reason instanceof Error ? reason.message : '读取运行日志失败')
        showErrorToast(reason instanceof Error ? reason.message : '读取运行日志失败')
        return
      } finally {
        if (!controller.signal.aborted) setRuntimeLoading(false)
      }
      await consumeRuntimeStream(controller)
    },
    [closeRuntimeStream, consumeRuntimeStream, scrollRuntimeToBottom],
  )

  // 读取操作日志分页数据。
  const loadAudit = useCallback(
    async (notify = false): Promise<void> => {
      const seq = ++loadAuditSeqRef.current
      setAuditLoading(true)
      try {
        const params = new URLSearchParams({
          page: String(auditPage),
          pageSize: String(auditPageSize),
        })
        params.set('days', String(auditFilter.period))
        if (auditFilter.category) params.set('category', auditFilter.category)
        if (auditFilter.result) params.set('result', auditFilter.result)
        if (auditFilter.target) params.set('target', auditFilter.target)
        if (auditFilter.operatorIp) params.set('operatorIp', auditFilter.operatorIp)
        const response = await adminFetch(`/api/admin/v1/logs/audit?${params.toString()}`)
        if (!response.ok) throw await adminError(response, '读取操作日志失败')
        const payload = (await response.json()) as AuditPageData
        if (seq !== loadAuditSeqRef.current) return
        setAuditItems(payload.items ?? [])
        setAuditTotal(payload.total ?? 0)
        setAuditLoadError('')
        if (notify) showSuccessToast('操作日志已刷新。')
      } catch (reason) {
        if (seq !== loadAuditSeqRef.current) return
        const message = reason instanceof Error ? reason.message : '读取操作日志失败'
        setAuditLoadError(message)
        showErrorToast(message)
      } finally {
        if (seq === loadAuditSeqRef.current) setAuditLoading(false)
      }
    },
    [auditFilter, auditPage, auditPageSize],
  )

  useEffect(() => {
    activeRef.current = active
  }, [active])

  useEffect(() => {
    consumeRuntimeStreamRef.current = consumeRuntimeStream
  }, [consumeRuntimeStream])

  useEffect(() => {
    if (!active) {
      closeRuntimeStream()
      return
    }
    void loadRuntime()
    return () => closeRuntimeStream()
  }, [active, closeRuntimeStream, loadRuntime, runtimeReloadKey])

  useEffect(() => {
    if (active) void loadAudit()
  }, [active, loadAudit])

  // 令牌轮换或跨标签页更新后，用当前 Bearer 重建运行日志流。
  useEffect(() => {
    const reloadRuntime = () => {
      if (activeRef.current) setRuntimeReloadKey((current) => current + 1)
    }
    const onStorage = (event: StorageEvent) => {
      if (event.key === adminTokenStorageKey) reloadRuntime()
    }
    window.addEventListener(adminTokenChangedEvent, reloadRuntime)
    window.addEventListener('storage', onStorage)
    return () => {
      window.removeEventListener(adminTokenChangedEvent, reloadRuntime)
      window.removeEventListener('storage', onStorage)
    }
  }, [])
  useEffect(() => {
    window.localStorage.setItem('oneai-proxy-audit-columns', JSON.stringify(auditColumns))
  }, [auditColumns])
  useEffect(() => {
    window.localStorage.setItem('oneai-proxy-audit-widths', JSON.stringify(auditWidths))
  }, [auditWidths])

  // 根据滚动位置决定是否继续自动跟随最新运行事件。
  function handleRuntimeScroll(event: UIEvent<HTMLDivElement>): void {
    const element = event.target as HTMLElement
    const atBottom = element.scrollHeight - element.scrollTop - element.clientHeight < 24
    runtimeFollowRef.current = atBottom
    setRuntimeFollow(atBottom)
  }

  // 应用操作日志筛选并回到第一页。
  function applyAuditFilter(): void {
    setAuditPage(1)
    setAuditFilter(auditDraft)
  }

  // 切换日志列显隐，至少保留一列可见。
  function toggleColumn(key: LogColumnKey, checked: boolean): void {
    setAuditColumns((current) => {
      const next = checked
        ? [...new Set([...current, key])]
        : current.filter((item) => item !== key)
      return next.length > 0
        ? auditColumnOptions.map((item) => item.key).filter((item) => next.includes(item))
        : current
    })
  }

  // 保存 Semi 表格拖拽后的列宽。
  function cacheWidth<T>(column: T): T {
    const resized = column as T & { key?: string | number; width?: string | number }
    const value = resized.width
    const key = String(resized.key ?? '')
    if (typeof value === 'number' && Number.isFinite(value) && key) {
      setAuditWidths((current) => ({ ...current, [key]: Math.round(value) }))
    }
    return column
  }

  const visibleRuntimeItems = useMemo(
    () =>
      runtimeItems.filter(
        (item) =>
          (runtimeLevelRank[item.level as RuntimeLevelFilter] ?? 1) >=
          runtimeLevelRank[runtimeLevelFilter],
      ),
    [runtimeItems, runtimeLevelFilter],
  )

  const auditTableColumns = useMemo(() => {
    const columns: Array<ColumnProps<AuditLog> & { key: LogColumnKey }> = [
      {
        key: 'time',
        dataIndex: 'occurredAt',
        title: '时间',
        width: auditWidths.time,
        render: (value: string) => formatTime(value),
      },
      {
        key: 'category',
        dataIndex: 'category',
        title: '分类',
        width: auditWidths.category,
        render: (value: string) => value || '—',
      },
      {
        key: 'action',
        dataIndex: 'action',
        title: '操作',
        width: auditWidths.action,
        render: (value: string) => (
          <Tooltip content={value}>
            <span className="logs-ellipsis">{value}</span>
          </Tooltip>
        ),
      },
      {
        key: 'target',
        dataIndex: 'targetId',
        title: '目标对象',
        width: auditWidths.target,
        render: (value: string) => (
          <Tooltip content={value || '无目标'}>
            <span className="logs-ellipsis">{value || '—'}</span>
          </Tooltip>
        ),
      },
      {
        key: 'result',
        dataIndex: 'result',
        title: '结果',
        width: auditWidths.result,
        render: (value: string) => <Tag color={resultColor(value)}>{resultLabel(value)}</Tag>,
      },
      {
        key: 'ip',
        dataIndex: 'operatorIp',
        title: '操作 IP',
        width: auditWidths.ip,
        render: (value: string) => value || '—',
      },
      {
        key: 'operator',
        dataIndex: 'actorType',
        title: '操作者',
        width: auditWidths.operator,
        render: (value: string) => value || '—',
      },
    ]
    return columns
      .filter((column) => auditColumns.includes(column.key))
      .map((column) => ({ ...column, align: 'center' as const, ellipsis: { showTitle: false } }))
  }, [auditColumns, auditWidths])

  return (
    <section className="page-content logs-page" aria-label="日志">
      <div className="logs-columns">
        <section className="logs-panel apple-card" aria-labelledby="audit-log-heading">
          <header className="logs-panel-header">
            <div>
              <h2 id="audit-log-heading">
                操作日志 <span>共 {auditTotal.toLocaleString('zh-CN')} 条</span>
              </h2>
              <p>记录关键管理动作、结果和操作来源。</p>
            </div>
            <div className="logs-panel-actions">
              <Tooltip content="刷新操作日志">
                <Button
                  className="request-icon-button"
                  icon={<IconRefresh />}
                  theme="borderless"
                  aria-label="刷新操作日志"
                  onClick={() => void loadAudit(true)}
                />
              </Tooltip>
              <PopoverColumns
                options={auditColumnOptions}
                columns={auditColumns}
                onToggle={toggleColumn}
              />
            </div>
          </header>
          <div className="logs-filter-row">
            <Select
              value={auditDraft.period}
              onChange={(value) =>
                setAuditDraft((current) => ({ ...current, period: Number(value) }))
              }
            >
              <Select.Option value={1}>最近 24 小时</Select.Option>
              <Select.Option value={7}>最近 7 天</Select.Option>
              <Select.Option value={30}>最近 30 天</Select.Option>
            </Select>
            <Select
              value={auditDraft.category || undefined}
              placeholder="全部分类"
              showClear
              onChange={(value) =>
                setAuditDraft((current) => ({ ...current, category: String(value ?? '') }))
              }
            >
              <Select.Option value="channels">渠道</Select.Option>
              <Select.Option value="models">模型</Select.Option>
              <Select.Option value="probes">探针</Select.Option>
              <Select.Option value="config">导入导出与备份</Select.Option>
              <Select.Option value="auth">安全</Select.Option>
              <Select.Option value="logs">日志与设置</Select.Option>
            </Select>
            <Select
              value={auditDraft.result || undefined}
              placeholder="全部结果"
              showClear
              onChange={(value) =>
                setAuditDraft((current) => ({ ...current, result: String(value ?? '') }))
              }
            >
              <Select.Option value="success">成功</Select.Option>
              <Select.Option value="failed">失败</Select.Option>
              <Select.Option value="rejected">拒绝</Select.Option>
            </Select>
            <Input
              prefix={<IconSearch />}
              placeholder="目标关键词"
              value={auditDraft.target}
              onChange={(value) => setAuditDraft((current) => ({ ...current, target: value }))}
              onEnterPress={applyAuditFilter}
            />
            <Input
              prefix={<IconSearch />}
              placeholder="操作 IP"
              value={auditDraft.operatorIp}
              onChange={(value) => setAuditDraft((current) => ({ ...current, operatorIp: value }))}
              onEnterPress={applyAuditFilter}
            />
            <Button type="primary" onClick={applyAuditFilter}>
              查询
            </Button>
            <Button
              theme="borderless"
              onClick={() => {
                setAuditDraft(defaultAuditFilter)
                setAuditFilter(defaultAuditFilter)
                setAuditPage(1)
              }}
            >
              重置
            </Button>
          </div>
          <div ref={tableWrapRef} className="logs-table-wrap">
            <Table<AuditLog>
              rowKey="id"
              dataSource={auditItems}
              columns={auditTableColumns}
              pagination={false}
              empty={<span aria-hidden="true" />}
              resizable={{ onResizeStop: cacheWidth }}
              scroll={{
                x: auditTableColumns.reduce((sum, column) => sum + Number(column.width ?? 0), 0),
                y: tableScrollY,
              }}
              onRow={(record) => ({
                onClick: () => {
                  if (record) setAuditSelected(record)
                },
              })}
            />
            {(auditLoading || auditLoadError || auditItems.length === 0) && (
              <div className="table-state-overlay">
                {auditLoading ? (
                  <Spin size="large" />
                ) : auditLoadError ? (
                  <Empty description="加载失败，请重试">
                    <Button type="primary" onClick={() => void loadAudit()}>
                      重试
                    </Button>
                  </Empty>
                ) : (
                  <span>暂无符合条件的操作日志。</span>
                )}
              </div>
            )}
          </div>
          <footer className="logs-pagination">
            <span>
              显示第 {auditTotal === 0 ? 0 : (auditPage - 1) * auditPageSize + 1}–
              {Math.min(auditPage * auditPageSize, auditTotal)} 条
            </span>
            <div>
              <Select
                value={auditPageSize}
                onChange={(value) => {
                  setAuditPageSize(Number(value))
                  setAuditPage(1)
                }}
              >
                <Select.Option value={20}>20 / 页</Select.Option>
                <Select.Option value={50}>50 / 页</Select.Option>
                <Select.Option value={100}>100 / 页</Select.Option>
              </Select>
              <Pagination
                currentPage={auditPage}
                pageSize={auditPageSize}
                total={auditTotal}
                showSizeChanger={false}
                onChange={setAuditPage}
              />
            </div>
          </footer>
          {auditSelected && (
            <div className="logs-detail">
              <div>
                <strong>{auditSelected.action}</strong>
                <Button
                  theme="borderless"
                  onClick={() => setAuditSelected(null)}
                  aria-label="关闭操作日志详情"
                >
                  ×
                </Button>
              </div>
              <p>
                {formatTime(auditSelected.occurredAt)} · {auditSelected.operatorIp || '本机'} ·{' '}
                {resultLabel(auditSelected.result)}
              </p>
              <pre>{formatAuditDetails(auditSelected.details)}</pre>
            </div>
          )}
        </section>
        <section className="logs-panel apple-card" aria-labelledby="runtime-log-heading">
          <header className="logs-panel-header">
            <div>
              <h2 id="runtime-log-heading">
                运行日志 <span>实时</span>
              </h2>
              <p>后端事件流，默认显示 info、warn 和 error。</p>
            </div>
            <div className="logs-panel-actions">
              <Select
                className="logs-level-select"
                aria-label="运行日志等级筛选"
                value={runtimeLevelFilter}
                onChange={(value) => setRuntimeLevelFilter(String(value) as RuntimeLevelFilter)}
              >
                <Select.Option value="debug">DEBUG（全部）</Select.Option>
                <Select.Option value="info">INFO 及以上</Select.Option>
                <Select.Option value="warn">WARN 及以上</Select.Option>
                <Select.Option value="error">ERROR</Select.Option>
              </Select>
              <Tooltip content="刷新并重新连接运行日志">
                <Button
                  className="request-icon-button"
                  icon={<IconRefresh />}
                  theme="borderless"
                  aria-label="刷新并重新连接运行日志"
                  onClick={() => void loadRuntime(true)}
                />
              </Tooltip>
            </div>
          </header>
          {runtimeNotice && (
            <div className="logs-notice" role="status">
              {runtimeNotice}
            </div>
          )}
          <div className="logs-runtime-area">
            <div
              ref={runtimeRef}
              className="logs-runtime-stream"
              onScroll={handleRuntimeScroll}
              role="log"
              aria-label="运行日志事件流"
            >
              {visibleRuntimeItems.map((item) => {
                const meta = runtimeMeta(item)
                return (
                  <button
                    className="logs-runtime-line"
                    data-level={item.level}
                    key={item.id}
                    type="button"
                    onClick={() => setRuntimeSelected(item)}
                  >
                    <span className="logs-runtime-time">{formatTime(item.occurredAt)}</span>
                    <span className="logs-runtime-level">{item.level.toUpperCase()}</span>
                    <span className="logs-runtime-event">{item.event}</span>
                    <span className="logs-runtime-message">{item.message || '—'}</span>
                    {meta && <span className="logs-runtime-meta"> · {meta}</span>}
                  </button>
                )
              })}
              {(runtimeLoading || visibleRuntimeItems.length === 0) && (
                <div className="logs-runtime-state">
                  {runtimeLoading ? <Spin size="large" /> : <span>暂无符合条件的运行日志。</span>}
                </div>
              )}
            </div>
            {!runtimeFollow && (
              <Tooltip content="回到底部">
                <Button
                  className="logs-follow-button"
                  type="primary"
                  icon={<IconArrowDown />}
                  aria-label="回到底部"
                  onClick={() => {
                    runtimeFollowRef.current = true
                    setRuntimeFollow(true)
                    requestAnimationFrame(scrollRuntimeToBottom)
                  }}
                />
              </Tooltip>
            )}
          </div>
          {runtimeSelected && (
            <div className="logs-detail">
              <div>
                <strong>{runtimeSelected.event}</strong>
                <Button
                  theme="borderless"
                  onClick={() => setRuntimeSelected(null)}
                  aria-label="关闭运行日志详情"
                >
                  ×
                </Button>
              </div>
              <p>
                {formatTime(runtimeSelected.occurredAt)} · {runtimeSelected.level}
              </p>
              <pre>
                {JSON.stringify(
                  {
                    message: runtimeSelected.message,
                    requestId: runtimeSelected.requestId,
                    attemptId: runtimeSelected.attemptId,
                    channelId: runtimeSelected.channelId,
                    context: runtimeSelected.context ?? {},
                  },
                  null,
                  2,
                )}
              </pre>
            </div>
          )}
        </section>
      </div>
    </section>
  )
}

// 渲染操作日志和运行日志共用的列设置弹层。
function PopoverColumns({
  options,
  columns,
  onToggle,
}: {
  options: Array<{ key: LogColumnKey; label: string }>
  columns: LogColumnKey[]
  onToggle: (key: LogColumnKey, checked: boolean) => void
}) {
  return (
    <Popover
      trigger="click"
      position="bottomRight"
      content={
        <div className="logs-columns-menu">
          <strong>显示列</strong>
          {options.map((item) => (
            <Checkbox
              key={item.key}
              checked={columns.includes(item.key)}
              onChange={(event) => onToggle(item.key, Boolean(event.target.checked))}
            >
              {item.label}
            </Checkbox>
          ))}
        </div>
      }
    >
      <Button icon={<IconColumnsStroked />}>列设置</Button>
    </Popover>
  )
}

export default LogsPage
