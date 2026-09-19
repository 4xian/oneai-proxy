import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Button,
  Card,
  Checkbox,
  Empty,
  Input,
  Modal,
  Pagination,
  Popover,
  Select,
  Spin,
  Table,
  Tag,
  Tooltip,
} from '@douyinfe/semi-ui-19'
import {
  IconCopy,
  IconColumnsStroked,
  IconDelete,
  IconEdit,
  IconPlus,
  IconPulse,
  IconRefresh,
  IconSearch,
} from '@douyinfe/semi-icons'
import type { AppTheme, ThemePreference } from '../../components/PageHeader'
import { adminError, adminFetch } from '../../api'
import { useTableScrollY } from '../../hooks/useTableScrollY'
import { protocolLabel } from '../../formatters'
import { showErrorToast, showSuccessToast, showWarningToast } from '../../notifications'

type ProbePolicy = {
  channelId: string
  enabled: boolean
  mode: string
  intervalSeconds: number
  model?: string
  failureThreshold: number
  requestTimeoutMs: number
  autoRecover: boolean
  recoverySuccessThreshold: number
}

type ProbeRun = {
  id: string
  channelId: string
  mode: string
  status: string
  latencyMs: number
  errorClass?: string
  errorMessage?: string
  occurredAt: string
}

export type Channel = {
  id: string
  name: string
  note?: string
  group?: string
  protocol: string
  baseUrl: string
  capabilities: string[]
  adminState: string
  healthState: string
  credentialConfigured: boolean
  failureAction: string
  failureThreshold: number
  customHeaders?: Record<string, string>
  concurrencyLimit: number
  requestTimeoutMs: number
  streamIdleTimeoutMs: number
  cooldownSeconds: number
  priority: number
  fallbackModel?: string
  reasoningEffort: 'passthrough' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'
  serviceTierPassthrough: boolean
}

type ChannelDirectoryEntry = Omit<Channel, 'customHeaders'> & {
  inFlight: number
  modelCount: number
  hasRoutableModel: boolean
  logicalModels: string[]
  testModel?: string
  probePolicy: ProbePolicy
  latestProbe?: ProbeRun
  failureCount: number
  cooldownUntil?: string
}

type ChannelDirectorySummary = {
  total: number
  available: number
  attention: number
  modelMappings: number
  protocols: number
  mappedChannels: number
  cooldown: number
  degraded: number
  halfOpen: number
  disabled: number
  adminDisabled: number
  autoDisabled: number
  missingCredentials: number
  missingModels: number
}

type ChannelDirectory = {
  summary: ChannelDirectorySummary
  channels: ChannelDirectoryEntry[]
}

type ChannelsPageProps = {
  active: boolean
  theme: AppTheme
  themePreference: ThemePreference
  onChangeTheme: () => void
}

type ChannelStatCardProps = {
  label: string
  value: string
  note: string
}

type ChannelActionsProps = {
  channel: ChannelDirectoryEntry
  busyActions: ReadonlySet<string>
  onTest: (channel: ChannelDirectoryEntry) => void
  onReset: (channel: ChannelDirectoryEntry) => void
  onCopy: (channel: ChannelDirectoryEntry) => void
  onEdit: (channel: ChannelDirectoryEntry) => void
  onDelete: (channel: ChannelDirectoryEntry) => void
  onToggle: (channel: ChannelDirectoryEntry) => void
}

type HealthPresentation = {
  filterValue: string
  label: string
  color: 'green' | 'orange' | 'blue' | 'red' | 'grey'
  tone: 'healthy' | 'warning' | 'danger' | 'disabled'
}

type PriorityOrder = 'ascend' | 'descend'

type ChannelColumnKey =
  | 'name'
  | 'group'
  | 'protocol'
  | 'models'
  | 'priority'
  | 'baseUrl'
  | 'health'
  | 'traffic'
  | 'probe'
  | 'actions'
type ChannelColumnWidthMap = Partial<Record<ChannelColumnKey, number>>

const channelColumnOptions: Array<{ key: ChannelColumnKey; label: string }> = [
  { key: 'name', label: '渠道' },
  { key: 'group', label: '分组' },
  { key: 'protocol', label: '协议' },
  { key: 'models', label: '模型' },
  { key: 'priority', label: '优先级' },
  { key: 'baseUrl', label: '上游地址' },
  { key: 'health', label: '健康' },
  { key: 'traffic', label: '流量策略' },
  { key: 'probe', label: '最近测试' },
  { key: 'actions', label: '操作' },
]

const defaultChannelColumnWidths: Record<ChannelColumnKey, number> = {
  name: 220,
  group: 126,
  protocol: 158,
  models: 210,
  priority: 104,
  baseUrl: 240,
  health: 126,
  traffic: 164,
  probe: 180,
  actions: 286,
}

const minChannelActionsColumnWidth = 286

// 从浏览器本地恢复渠道表格列设置，异常值回退为全部默认列。
function getInitialChannelColumns(): ChannelColumnKey[] {
  try {
    const saved = JSON.parse(window.localStorage.getItem('oneai-proxy-channel-columns') ?? '[]')
    const allowed = new Set(channelColumnOptions.map((item) => item.key))
    const columns = Array.isArray(saved)
      ? saved.filter(
          (item): item is ChannelColumnKey =>
            typeof item === 'string' && allowed.has(item as ChannelColumnKey),
        )
      : []
    return columns.length > 0 ? columns : channelColumnOptions.map((item) => item.key)
  } catch {
    return channelColumnOptions.map((item) => item.key)
  }
}

// 从浏览器本地恢复渠道表格列宽，只接受合理范围内的数值。
function getInitialChannelColumnWidths(): ChannelColumnWidthMap {
  try {
    const saved = JSON.parse(
      window.localStorage.getItem('oneai-proxy-channel-column-widths') ?? '{}',
    ) as unknown
    if (!saved || typeof saved !== 'object' || Array.isArray(saved)) return {}
    const record = saved as Record<string, unknown>
    const widths: ChannelColumnWidthMap = {}
    Object.keys(defaultChannelColumnWidths).forEach((key) => {
      const width = record[key]
      if (typeof width === 'number' && Number.isFinite(width) && width >= 64 && width <= 800) {
        widths[key as ChannelColumnKey] =
          key === 'actions'
            ? Math.max(minChannelActionsColumnWidth, Math.round(width))
            : Math.round(width)
      }
    })
    return widths
  } catch {
    return {}
  }
}

const emptyDirectory: ChannelDirectory = {
  summary: {
    total: 0,
    available: 0,
    attention: 0,
    modelMappings: 0,
    protocols: 0,
    mappedChannels: 0,
    cooldown: 0,
    degraded: 0,
    halfOpen: 0,
    disabled: 0,
    adminDisabled: 0,
    autoDisabled: 0,
    missingCredentials: 0,
    missingModels: 0,
  },
  channels: [],
}

const allGroupsFilter = 'filter:all'
const ungroupedFilter = 'filter:ungrouped'
const groupFilterPrefix = 'group:'

// 将 hash 路径写入浏览器地址并触发应用路由切换。
function navigate(path: string): void {
  window.location.hash = path
}

// 按探针结果选择成功、警告或错误 Toast，避免 HTTP 200 把失败显示成成功。
function notifyProbeResult(actionLabel: string, result: ProbeRun): void {
  const detail = [result.errorMessage, result.errorClass].filter(Boolean).join(' · ')
  if (result.status === 'success') {
    showSuccessToast(`${actionLabel}成功${result.latencyMs ? `，延迟 ${result.latencyMs} ms` : ''}`)
    return
  }
  if (result.status === 'skipped_busy') {
    showWarningToast(detail || `${actionLabel}因并发已满已跳过`)
    return
  }
  showErrorToast(detail || `${actionLabel}失败`)
}

// 格式化探针时间，无法解析时保留服务端原值。
function formatProbeTime(value: string | undefined): string {
  if (!value) return '尚未测试'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(date)
}

// 根据渠道完整运行条件生成列表中的可操作状态。
function healthPresentation(channel: ChannelDirectoryEntry): HealthPresentation {
  if (channel.adminState === 'disabled')
    return { filterValue: 'disabled', label: '人工禁用', color: 'grey', tone: 'disabled' }
  if (!channel.credentialConfigured)
    return { filterValue: 'missing_credential', label: '缺少凭证', color: 'red', tone: 'danger' }
  if (!channel.hasRoutableModel)
    return { filterValue: 'missing_model', label: '无模型', color: 'orange', tone: 'warning' }
  switch (channel.healthState) {
    case 'healthy':
      return { filterValue: 'healthy', label: '健康', color: 'green', tone: 'healthy' }
    case 'degraded':
      return { filterValue: 'degraded', label: '降级', color: 'orange', tone: 'warning' }
    case 'cooldown':
      return { filterValue: 'cooldown', label: '冷却中', color: 'orange', tone: 'warning' }
    case 'half_open':
      return { filterValue: 'half_open', label: '半开放', color: 'blue', tone: 'warning' }
    case 'auto_disabled':
      return { filterValue: 'auto_disabled', label: '自动禁用', color: 'red', tone: 'danger' }
    default:
      return {
        filterValue: channel.healthState || 'unknown',
        label: channel.healthState || '未知',
        color: 'grey',
        tone: 'disabled',
      }
  }
}

// 渲染渠道页顶部的普通统计卡，只展示当前数字和说明。
function ChannelStatCard({ label, value, note }: ChannelStatCardProps) {
  return (
    <Card className="overview-metric-card apple-card" bordered={false}>
      <div className="metric-card-copy">
        <span className="metric-label">{label}</span>
        <strong className="metric-value">{value}</strong>
        <small className="metric-note">{note}</small>
      </div>
    </Card>
  )
}

// 渲染渠道行右侧固定的高频操作区。
function ChannelActions({
  channel,
  busyActions,
  onTest,
  onReset,
  onCopy,
  onEdit,
  onDelete,
  onToggle,
}: ChannelActionsProps) {
  return (
    <div className="table-actions channel-actions">
      <Tooltip content="测试渠道">
        <Button
          theme="borderless"
          icon={<IconPulse />}
          loading={busyActions.has(`${channel.id}:test`)}
          aria-label={`测试 ${channel.name}`}
          onClick={() => onTest(channel)}
        />
      </Tooltip>
      <Tooltip content="人工恢复健康状态">
        <Button
          theme="borderless"
          icon={<IconRefresh />}
          loading={busyActions.has(`${channel.id}:health`)}
          aria-label={`人工恢复 ${channel.name} 健康状态`}
          onClick={() => onReset(channel)}
        />
      </Tooltip>
      <Tooltip content="复制渠道">
        <Button
          theme="borderless"
          icon={<IconCopy />}
          loading={busyActions.has(`${channel.id}:copy`)}
          aria-label={`复制 ${channel.name}`}
          onClick={() => onCopy(channel)}
        />
      </Tooltip>
      <Tooltip content="编辑渠道">
        <Button
          theme="borderless"
          icon={<IconEdit />}
          aria-label={`编辑 ${channel.name}`}
          onClick={() => onEdit(channel)}
        />
      </Tooltip>
      <Tooltip content="删除渠道">
        <Button
          theme="borderless"
          type="danger"
          icon={<IconDelete />}
          loading={busyActions.has(`${channel.id}:delete`)}
          aria-label={`删除 ${channel.name}`}
          onClick={() => onDelete(channel)}
        />
      </Tooltip>
      <Button
        className="channel-toggle-button"
        theme="borderless"
        loading={busyActions.has(`${channel.id}:toggle`)}
        onClick={() => onToggle(channel)}
      >
        {channel.adminState === 'enabled' ? '禁用' : '启用'}
      </Button>
    </div>
  )
}

// 渲染渠道管理工作台，集中处理目录筛选和运维动作。
function ChannelsPage({ active }: ChannelsPageProps) {
  const [directory, setDirectory] = useState<ChannelDirectory>(emptyDirectory)
  const [search, setSearch] = useState('')
  const [protocolFilter, setProtocolFilter] = useState('all')
  const [groupFilter, setGroupFilter] = useState(allGroupsFilter)
  const [stateFilter, setStateFilter] = useState('all')
  const [healthFilter, setHealthFilter] = useState('all')
  const [currentPage, setCurrentPage] = useState(1)
  const [pageSize, setPageSize] = useState(10)
  const [priorityOrder, setPriorityOrder] = useState<PriorityOrder>('descend')
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState(false)
  const [busyActions, setBusyActions] = useState<Set<string>>(() => new Set())
  const directoryRequestRef = useRef(0)
  const [visibleColumns, setVisibleColumns] = useState<ChannelColumnKey[]>(getInitialChannelColumns)
  const [columnWidths, setColumnWidths] = useState<ChannelColumnWidthMap>(
    getInitialChannelColumnWidths,
  )
  const { tableScrollY, tableWrapRef } = useTableScrollY()

  // 标记单个渠道动作正在执行，允许不同行和不同动作并行展示状态。
  const startAction = useCallback((action: string): void => {
    setBusyActions((current) => new Set(current).add(action))
  }, [])

  // 只结束当前动作，避免并发请求互相解除加载状态。
  const finishAction = useCallback((action: string): void => {
    setBusyActions((current) => {
      const next = new Set(current)
      next.delete(action)
      return next
    })
  }, [])

  // 一次读取渠道主页需要的全部非敏感目录数据；失败保留上次成功列表。
  const loadChannels = useCallback(async (notifySuccess = false): Promise<void> => {
    const requestID = ++directoryRequestRef.current
    setLoading(true)
    try {
      const response = await adminFetch('/api/admin/v1/channels/directory')
      if (!response.ok) throw await adminError(response, '读取渠道目录失败')
      const payload = (await response.json()) as Partial<ChannelDirectory>
      if (requestID !== directoryRequestRef.current) return
      const channels = Array.isArray(payload.channels) ? payload.channels : []
      setDirectory({
        summary: { ...emptyDirectory.summary, ...(payload.summary ?? {}) },
        channels: channels.map((channel) => ({
          ...channel,
          capabilities: channel.capabilities ?? [],
          logicalModels: channel.logicalModels ?? [],
        })),
      })
      setLoadError(false)
      if (notifySuccess) showSuccessToast('渠道数据已刷新。')
    } catch (reason) {
      if (requestID === directoryRequestRef.current) {
        setLoadError(true)
        showErrorToast(reason instanceof Error ? reason.message : '读取渠道目录失败')
      }
    } finally {
      if (requestID === directoryRequestRef.current) setLoading(false)
    }
  }, [])

  // 运行一次真实渠道测试，并刷新最近测试和健康摘要。
  async function testChannel(channel: ChannelDirectoryEntry): Promise<void> {
    const action = `${channel.id}:test`
    startAction(action)
    try {
      const response = await adminFetch(
        `/api/admin/v1/channels/${encodeURIComponent(channel.id)}/test`,
        {
          method: 'POST',
          ...(channel.testModel
            ? {
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ model: channel.testModel }),
              }
            : {}),
        },
      )
      if (!response.ok) throw await adminError(response, '渠道测试失败')
      const result = (await response.json()) as ProbeRun
      notifyProbeResult(`${channel.name} 测试`, result)
      await loadChannels()
    } catch (reason) {
      showErrorToast(reason instanceof Error ? reason.message : '渠道测试失败')
    } finally {
      finishAction(action)
    }
  }

  // 通过专用接口切换人工状态，避免覆盖渠道其他配置。
  async function toggleChannel(channel: ChannelDirectoryEntry): Promise<void> {
    const nextState = channel.adminState === 'enabled' ? 'disabled' : 'enabled'
    const action = `${channel.id}:toggle`
    startAction(action)
    try {
      const response = await adminFetch(
        `/api/admin/v1/channels/${encodeURIComponent(channel.id)}/state`,
        {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ adminState: nextState }),
        },
      )
      if (!response.ok) throw await adminError(response, '更新渠道状态失败')
      showSuccessToast(
        nextState === 'enabled' ? `${channel.name} 已启用。` : `${channel.name} 已人工禁用。`,
      )
      await loadChannels()
    } catch (reason) {
      showErrorToast(reason instanceof Error ? reason.message : '更新渠道状态失败')
    } finally {
      finishAction(action)
    }
  }

  // 经二次确认后人工恢复系统健康状态，不改变渠道的人工启停状态。
  async function resetHealth(channel: ChannelDirectoryEntry): Promise<void> {
    if (
      !window.confirm(
        `人工恢复“${channel.name}”会清除失败计数和冷却状态，但不会启用人工禁用渠道，是否继续？`,
      )
    ) {
      return
    }
    const action = `${channel.id}:health`
    startAction(action)
    try {
      const response = await adminFetch(
        `/api/admin/v1/channels/${encodeURIComponent(channel.id)}/health`,
        { method: 'POST' },
      )
      if (!response.ok) throw await adminError(response, '人工恢复失败')
      showSuccessToast(`${channel.name} 的健康状态已人工恢复。`)
      await loadChannels()
    } catch (reason) {
      showErrorToast(reason instanceof Error ? reason.message : '人工恢复失败')
    } finally {
      finishAction(action)
    }
  }

  // 复制渠道的完整配置和凭证，并用后端生成的新 ID 刷新目录。
  async function copyChannel(channel: ChannelDirectoryEntry): Promise<void> {
    const action = `${channel.id}:copy`
    startAction(action)
    try {
      const response = await adminFetch(
        `/api/admin/v1/channels/${encodeURIComponent(channel.id)}/copy`,
        { method: 'POST' },
      )
      if (!response.ok) throw await adminError(response, '复制渠道失败')
      const copied = (await response.json()) as ChannelDirectoryEntry
      showSuccessToast(`已复制为“${copied.name}”。`)
      await loadChannels()
    } catch (reason) {
      showErrorToast(reason instanceof Error ? reason.message : '复制渠道失败')
    } finally {
      finishAction(action)
    }
  }

  // 删除渠道并刷新目录，错误通过统一 Toast 返回。
  async function deleteChannel(channel: ChannelDirectoryEntry): Promise<void> {
    const action = `${channel.id}:delete`
    startAction(action)
    try {
      const response = await adminFetch(
        `/api/admin/v1/channels/${encodeURIComponent(channel.id)}`,
        { method: 'DELETE' },
      )
      if (!response.ok) throw await adminError(response, '删除渠道失败')
      showSuccessToast(`${channel.name} 已删除。`)
      await loadChannels()
    } catch (reason) {
      showErrorToast(reason instanceof Error ? reason.message : '删除渠道失败')
      throw reason
    } finally {
      finishAction(action)
    }
  }

  // 禁用渠道前二次确认；启用无需确认，取消时不发状态请求。
  function confirmToggle(channel: ChannelDirectoryEntry): void {
    if (channel.adminState !== 'enabled') {
      void toggleChannel(channel)
      return
    }
    Modal.confirm({
      className: 'apple-confirm-modal',
      title: '禁用渠道',
      content: <span>确认禁用“{channel.name}”？禁用后将停止该渠道的新请求。</span>,
      okText: '禁用',
      cancelText: '取消',
      okButtonProps: { type: 'danger' },
      onOk: () => toggleChannel(channel),
    })
  }

  // 使用 Semi 确认框阻止误删渠道。
  function confirmDelete(channel: ChannelDirectoryEntry): void {
    Modal.confirm({
      className: 'apple-confirm-modal',
      title: '删除渠道',
      content: <span>确认删除“{channel.name}”？渠道配置、模型映射和探针记录将一并删除。</span>,
      okText: '删除',
      cancelText: '取消',
      okButtonProps: { type: 'danger' },
      onOk: () => deleteChannel(channel),
    })
  }

  // 清空搜索和全部筛选条件。
  function clearFilters(): void {
    setSearch('')
    setProtocolFilter('all')
    setGroupFilter(allGroupsFilter)
    setStateFilter('all')
    setHealthFilter('all')
  }

  // 切换全量渠道的优先级顺序，并从第一页重新展示。
  function changePrioritySort(sortOrder: boolean | string | undefined): void {
    setPriorityOrder((current) =>
      sortOrder === 'ascend' || sortOrder === 'descend'
        ? sortOrder
        : current === 'descend'
          ? 'ascend'
          : 'descend',
    )
    setCurrentPage(1)
  }

  // 切换渠道表格列并将结果持久化到浏览器本地。
  function toggleColumn(key: ChannelColumnKey, checked: boolean): void {
    setVisibleColumns((current) => {
      const next = checked
        ? Array.from(new Set([...current, key]))
        : current.filter((item) => item !== key)
      const ordered = channelColumnOptions
        .map((item) => item.key)
        .filter((item) => next.includes(item))
      return ordered.length > 0 ? ordered : current
    })
  }

  // 在列宽拖拽结束后记录最新宽度，供刷新页面时恢复。
  const cacheColumnWidth = useCallback((column: ChannelDirectoryEntry): ChannelDirectoryEntry => {
    const resized = column as unknown as { key?: string | number; width?: string | number }
    const key = resized.key as ChannelColumnKey
    const width = resized.width
    if (
      Object.prototype.hasOwnProperty.call(defaultChannelColumnWidths, key) &&
      typeof width === 'number' &&
      Number.isFinite(width)
    ) {
      setColumnWidths((current) => ({
        ...current,
        [key]:
          key === 'actions'
            ? Math.max(minChannelActionsColumnWidth, Math.round(width))
            : Math.round(width),
      }))
    }
    return column
  }, [])

  useEffect(() => {
    if (!active) return
    void loadChannels()
  }, [active, loadChannels])

  useEffect(() => {
    window.localStorage.setItem('oneai-proxy-channel-columns', JSON.stringify(visibleColumns))
  }, [visibleColumns])

  useEffect(() => {
    window.localStorage.setItem('oneai-proxy-channel-column-widths', JSON.stringify(columnWidths))
  }, [columnWidths])

  const filteredChannels = useMemo(() => {
    const needle = search.trim().toLowerCase()
    return directory.channels.filter((channel) => {
      const presentation = healthPresentation(channel)
      const matchesSearch =
        !needle ||
        [
          channel.name,
          channel.id,
          channel.baseUrl,
          channel.note ?? '',
          channel.group ?? '',
          channel.fallbackModel ?? '',
          ...channel.logicalModels,
        ].some((value) => value.toLowerCase().includes(needle))
      const matchesProtocol = protocolFilter === 'all' || channel.protocol === protocolFilter
      const normalizedGroup = channel.group?.trim() ?? ''
      const matchesGroup =
        groupFilter === allGroupsFilter ||
        (groupFilter === ungroupedFilter && !normalizedGroup) ||
        groupFilter === `${groupFilterPrefix}${normalizedGroup}`
      const matchesState = stateFilter === 'all' || channel.adminState === stateFilter
      const matchesHealth =
        healthFilter === 'all' ||
        presentation.filterValue === healthFilter ||
        (healthFilter === 'attention' && presentation.tone !== 'healthy')
      return matchesSearch && matchesProtocol && matchesGroup && matchesState && matchesHealth
    })
  }, [directory.channels, groupFilter, healthFilter, protocolFilter, search, stateFilter])

  const groupOptions = useMemo(
    () =>
      Array.from(
        new Set(
          directory.channels
            .map((channel) => channel.group?.trim())
            .filter((value): value is string => Boolean(value)),
        ),
      ).sort((left, right) => left.localeCompare(right, 'zh-CN')),
    [directory.channels],
  )
  const sortedChannels = useMemo(
    () =>
      [...filteredChannels].sort((left, right) =>
        priorityOrder === 'descend'
          ? right.priority - left.priority
          : left.priority - right.priority,
      ),
    [filteredChannels, priorityOrder],
  )
  const maxPage = Math.max(1, Math.ceil(sortedChannels.length / pageSize))
  const pagedChannels = useMemo(
    () => sortedChannels.slice((currentPage - 1) * pageSize, currentPage * pageSize),
    [currentPage, pageSize, sortedChannels],
  )
  useEffect(() => {
    setCurrentPage(1)
  }, [groupFilter, healthFilter, protocolFilter, search, stateFilter])
  useEffect(() => {
    setCurrentPage((page) => Math.min(page, maxPage))
  }, [maxPage])

  const hasFilters = Boolean(
    search ||
    protocolFilter !== 'all' ||
    groupFilter !== allGroupsFilter ||
    stateFilter !== 'all' ||
    healthFilter !== 'all',
  )
  const summary = directory.summary
  const attentionNote =
    summary.attention === 0
      ? '所有渠道均可正常路由'
      : [
          summary.cooldown > 0 ? `${summary.cooldown} 个冷却` : '',
          summary.degraded > 0 ? `${summary.degraded} 个降级` : '',
          summary.halfOpen > 0 ? `${summary.halfOpen} 个半开` : '',
          summary.adminDisabled > 0 ? `${summary.adminDisabled} 个人工禁用` : '',
          summary.autoDisabled > 0 ? `${summary.autoDisabled} 个自动禁用` : '',
          summary.missingCredentials > 0 ? `${summary.missingCredentials} 个缺凭证` : '',
          summary.missingModels > 0 ? `${summary.missingModels} 个无模型` : '',
        ]
          .filter(Boolean)
          .join(' · ')
  const columns = [
    {
      title: '渠道',
      dataIndex: 'name',
      key: 'name',
      width: columnWidths.name ?? defaultChannelColumnWidths.name,
      render: (_value: unknown, channel: ChannelDirectoryEntry) => {
        const presentation = healthPresentation(channel)
        return (
          <div className="table-primary-cell channel-name-cell">
            <span className={`channel-health-dot is-${presentation.tone}`} />
            <Tooltip content={channel.name} position="topLeft">
              <strong>{channel.name}</strong>
            </Tooltip>
          </div>
        )
      },
    },
    {
      title: '分组',
      dataIndex: 'group',
      key: 'group',
      width: columnWidths.group ?? defaultChannelColumnWidths.group,
      render: (value: string | undefined) =>
        value ? (
          <Tooltip content={value} position="top">
            <span className="channel-tag-cell">
              <Tag className="channel-group-tag" color="light-blue" type="light">
                {value}
              </Tag>
            </span>
          </Tooltip>
        ) : (
          <span className="channel-group-empty">未分组</span>
        ),
    },
    {
      title: '协议',
      dataIndex: 'protocol',
      key: 'protocol',
      width: columnWidths.protocol ?? defaultChannelColumnWidths.protocol,
      render: (value: string) => {
        const text = protocolLabel(value)
        return (
          <Tooltip content={text} position="top">
            <span className="channel-tag-cell">
              <Tag className="channel-protocol-tag" color="blue">
                {text}
              </Tag>
            </span>
          </Tooltip>
        )
      },
    },
    {
      title: '模型',
      dataIndex: 'logicalModels',
      key: 'models',
      width: columnWidths.models ?? defaultChannelColumnWidths.models,
      render: (items: string[], channel: ChannelDirectoryEntry) => {
        const names = items.join('、')
        const text =
          channel.modelCount > 0
            ? `${channel.modelCount} 个模型${names ? ` · ${names}` : ''}`
            : '尚未配置模型'
        return (
          <Tooltip content={text} position="top">
            <span className="channel-inline-cell">{text}</span>
          </Tooltip>
        )
      },
    },
    {
      title: '优先级',
      dataIndex: 'priority',
      key: 'priority',
      width: columnWidths.priority ?? defaultChannelColumnWidths.priority,
      sorter: true,
      sortOrder: priorityOrder,
      render: (value: number) => (
        <Tooltip content={String(value)} position="top">
          <strong>{value}</strong>
        </Tooltip>
      ),
    },
    {
      title: '上游地址',
      dataIndex: 'baseUrl',
      key: 'baseUrl',
      width: columnWidths.baseUrl ?? defaultChannelColumnWidths.baseUrl,
      render: (value: string) => (
        <Tooltip content={value} position="top">
          <span className="table-url">{value}</span>
        </Tooltip>
      ),
    },
    {
      title: '健康',
      key: 'health',
      width: columnWidths.health ?? defaultChannelColumnWidths.health,
      render: (_value: unknown, channel: ChannelDirectoryEntry) => {
        const presentation = healthPresentation(channel)
        return (
          <Tooltip content={presentation.label} position="top">
            <span className="channel-tag-cell">
              <Tag className="channel-health-tag" color={presentation.color}>
                {presentation.label}
              </Tag>
            </span>
          </Tooltip>
        )
      },
    },
    {
      title: '流量策略',
      dataIndex: 'concurrencyLimit',
      key: 'traffic',
      width: columnWidths.traffic ?? defaultChannelColumnWidths.traffic,
      render: (_value: number, channel: ChannelDirectoryEntry) => {
        const text = `${channel.inFlight} / ${channel.concurrencyLimit} 在途 · 探针${channel.probePolicy.enabled ? '开启' : '关闭'}`
        return (
          <Tooltip content={text} position="top">
            <span className="channel-inline-cell">{text}</span>
          </Tooltip>
        )
      },
    },
    {
      title: '最近测试',
      dataIndex: 'latestProbe',
      key: 'probe',
      width: columnWidths.probe ?? defaultChannelColumnWidths.probe,
      render: (run: ProbeRun | undefined) => {
        const status = run
          ? run.status === 'success'
            ? '测试成功'
            : run.status === 'skipped_busy'
              ? '并发已满，已跳过'
              : '测试失败'
          : '尚未测试'
        const detail = run
          ? `${formatProbeTime(run.occurredAt)}${run.latencyMs ? ` · ${run.latencyMs} ms` : ''}`
          : '点击右侧按钮测试'
        const text = `${status} · ${detail}`
        return (
          <Tooltip content={text} position="top">
            <span
              className={`channel-inline-cell ${run?.status === 'success' ? 'table-credential-ready' : run ? 'table-credential-missing' : ''}`}
            >
              {text}
            </span>
          </Tooltip>
        )
      },
    },
    {
      title: '操作',
      key: 'actions',
      align: 'right' as const,
      fixed: 'right' as const,
      width: columnWidths.actions ?? defaultChannelColumnWidths.actions,
      onHeaderCell: () => ({ resize: false }),
      render: (_value: unknown, channel: ChannelDirectoryEntry) => (
        <ChannelActions
          channel={channel}
          busyActions={busyActions}
          onTest={(item) => void testChannel(item)}
          onReset={(item) => void resetHealth(item)}
          onCopy={(item) => void copyChannel(item)}
          onEdit={(item) => navigate(`#channels/${encodeURIComponent(item.id)}/edit`)}
          onDelete={confirmDelete}
          onToggle={confirmToggle}
        />
      ),
    },
  ]
    .filter((column) => visibleColumns.includes(column.key as ChannelColumnKey))
    .map((column) =>
      column.key === 'actions'
        ? { ...column, align: 'center' as const }
        : {
            ...column,
            align: column.key === 'name' ? ('left' as const) : ('center' as const),
            ellipsis: { showTitle: false },
          },
    )

  return (
    <section className="channels-shell page-content">
      <section className="channels-workspace" aria-label="渠道管理">
        <div className="overview-metric-row">
          <ChannelStatCard
            label="渠道总数"
            value={loading ? '—' : String(summary.total)}
            note={`${summary.protocols} 种上游协议`}
          />
          <ChannelStatCard
            label="可用渠道"
            value={loading ? '—' : String(summary.available)}
            note="凭证、模型与运行状态正常"
          />
          <ChannelStatCard
            label="需要关注"
            value={loading ? '—' : String(summary.attention)}
            note={attentionNote}
          />
          <ChannelStatCard
            label="模型映射"
            value={loading ? '—' : String(summary.modelMappings)}
            note={`已关联 ${summary.mappedChannels} 个渠道`}
          />
        </div>

        <section
          className="channel-directory-panel apple-card"
          aria-labelledby="channel-directory-title"
        >
          <div className="channel-directory-heading">
            <div className="request-ledger-title">
              <h1 id="channel-directory-title">
                渠道列表 <span>共 {filteredChannels.length.toLocaleString('zh-CN')} 个渠道</span>
              </h1>
              <p>管理凭证、模型、并发和故障策略</p>
            </div>
            <div className="request-ledger-actions channel-directory-actions">
              <Tooltip content="刷新渠道">
                <Button
                  className="request-icon-button"
                  icon={<IconRefresh />}
                  theme="borderless"
                  aria-label="刷新渠道"
                  loading={loading}
                  onClick={() => void loadChannels(true)}
                />
              </Tooltip>
              <Popover
                trigger="click"
                position="bottomRight"
                contentClassName="request-columns-popover"
                content={
                  <div className="request-columns-menu">
                    <strong>显示列</strong>
                    {channelColumnOptions.map((item) => (
                      <Checkbox
                        key={item.key}
                        checked={visibleColumns.includes(item.key)}
                        onChange={(event) => toggleColumn(item.key, Boolean(event.target.checked))}
                      >
                        {item.label}
                      </Checkbox>
                    ))}
                  </div>
                }
              >
                <Button className="request-columns-button" icon={<IconColumnsStroked />}>
                  列设置
                </Button>
              </Popover>
              <Button
                theme="solid"
                type="primary"
                icon={<IconPlus />}
                onClick={() => navigate('#channels/new')}
              >
                新增渠道
              </Button>
            </div>
          </div>
          <div className="channel-toolbar">
            <Input
              className="channel-search"
              prefix={<IconSearch />}
              value={search}
              onChange={setSearch}
              placeholder="搜索名称、分组、地址、ID 或模型"
              aria-label="搜索渠道"
              showClear
            />
            <Select
              className="apple-select channel-filter"
              dropdownClassName="apple-select-dropdown"
              value={protocolFilter}
              onChange={(value) => setProtocolFilter(String(value))}
            >
              <Select.Option value="all">全部协议</Select.Option>
              <Select.Option value="openai_responses">OpenAI Responses</Select.Option>
              <Select.Option value="openai_chat">OpenAI Chat</Select.Option>
              <Select.Option value="anthropic_messages">Anthropic Messages</Select.Option>
            </Select>
            <Select
              className="apple-select channel-filter"
              dropdownClassName="apple-select-dropdown"
              value={groupFilter}
              onChange={(value) => setGroupFilter(String(value))}
            >
              <Select.Option value={allGroupsFilter}>全部分组</Select.Option>
              <Select.Option value={ungroupedFilter}>未分组</Select.Option>
              {groupOptions.map((group) => (
                <Select.Option key={group} value={`${groupFilterPrefix}${group}`}>
                  {group}
                </Select.Option>
              ))}
            </Select>
            <Select
              className="apple-select channel-filter"
              dropdownClassName="apple-select-dropdown"
              value={stateFilter}
              onChange={(value) => setStateFilter(String(value))}
            >
              <Select.Option value="all">全部人工状态</Select.Option>
              <Select.Option value="enabled">已启用</Select.Option>
              <Select.Option value="disabled">人工禁用</Select.Option>
            </Select>
            <Select
              className="apple-select channel-filter"
              dropdownClassName="apple-select-dropdown"
              value={healthFilter}
              onChange={(value) => setHealthFilter(String(value))}
            >
              <Select.Option value="all">全部健康状态</Select.Option>
              <Select.Option value="healthy">健康</Select.Option>
              <Select.Option value="degraded">降级</Select.Option>
              <Select.Option value="cooldown">冷却中</Select.Option>
              <Select.Option value="half_open">半开放</Select.Option>
              <Select.Option value="auto_disabled">自动禁用</Select.Option>
              <Select.Option value="attention">全部需关注</Select.Option>
            </Select>
            <div className="channel-filter-result">
              {hasFilters && (
                <Button theme="borderless" onClick={clearFilters}>
                  清空筛选
                </Button>
              )}
              <span>{filteredChannels.length} 条结果</span>
            </div>
          </div>
          <div className="channel-table-wrap">
            <div ref={tableWrapRef} className="channel-table-scroll">
              <Table<ChannelDirectoryEntry>
                className="channel-table"
                rowKey="id"
                dataSource={pagedChannels}
                empty={<span aria-hidden="true" />}
                columns={columns}
                pagination={false}
                resizable={{ onResizeStop: cacheColumnWidth }}
                scroll={{
                  x: visibleColumns.reduce(
                    (sum, key) => sum + (columnWidths[key] ?? defaultChannelColumnWidths[key]),
                    0,
                  ),
                  y: tableScrollY,
                }}
                onChange={({ sorter }) => changePrioritySort(sorter?.sortOrder)}
              />
            </div>
            <footer className="channel-pagination">
              <span>
                显示第 {filteredChannels.length === 0 ? 0 : (currentPage - 1) * pageSize + 1}–
                {Math.min(currentPage * pageSize, filteredChannels.length)} 条，共{' '}
                {filteredChannels.length.toLocaleString('zh-CN')} 条
              </span>
              <Pagination
                currentPage={currentPage}
                pageSize={pageSize}
                total={filteredChannels.length}
                pageSizeOpts={[10, 20, 50, 100]}
                showSizeChanger
                onChange={(nextPage, nextPageSize) => {
                  setCurrentPage(nextPage)
                  setPageSize(nextPageSize)
                }}
              />
            </footer>
            {(loading || loadError || pagedChannels.length === 0) && (
              <div className="table-state-overlay channel-table-state">
                {loading ? (
                  <Spin size="large" />
                ) : loadError ? (
                  <Empty
                    description={
                      <div
                        style={{ display: 'flex', flexDirection: 'column', alignItems: 'center' }}
                      >
                        <span>加载失败，请重试</span>
                        <Button
                          theme="light"
                          style={{ marginTop: 12 }}
                          onClick={() => void loadChannels()}
                        >
                          重试
                        </Button>
                      </div>
                    }
                  />
                ) : (
                  <Empty
                    description={
                      hasFilters ? '暂无符合条件的渠道' : '还没有渠道，先新增一个上游连接'
                    }
                  />
                )}
              </div>
            )}
          </div>
        </section>
      </section>
    </section>
  )
}

export default ChannelsPage
