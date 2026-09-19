import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Button,
  Checkbox,
  Empty,
  Input,
  Pagination,
  Popover,
  RadioGroup,
  Select,
  Spin,
  Table,
  Tag,
  Tooltip,
} from '@douyinfe/semi-ui-19'
import {
  IconCalendar,
  IconColumnsStroked,
  IconDownload,
  IconExternalOpen,
  IconRefresh,
  IconSearch,
} from '@douyinfe/semi-icons'
import { adminError, adminFetch } from '../../api'
import { useTableScrollY } from '../../hooks/useTableScrollY'
import { protocolLabel } from '../../formatters'
import { showErrorToast, showSuccessToast } from '../../notifications'
import { RequestDetailPanel } from './request-detail-panel'
import {
  RequestMetricCard,
  columnOptions,
  defaultColumnWidths,
  defaultExpandedAttempt,
  defaultFilter,
  errorOptions,
  formatCompactNumber,
  formatDuration,
  formatListDate,
  formatNumber,
  formatPercentileDuration,
  formatPercent,
  getInitialColumnWidths,
  getInitialColumns,
  requestStatusValues,
  statusColor,
  statusText,
  type Blob,
  type Channel,
  type ColumnKey,
  type ColumnWidthMap,
  type OverviewData,
  type PageData,
  type Props,
  type Request,
  type RequestColumn,
  type RequestFilter,
  type Stats,
} from './request-log-shared'

// 渲染独立的阶段 6R 请求日志页面。
function RequestsPage({ requestId, active = true }: Props) {
  const [stats, setStats] = useState<Stats | null>(null)
  const [overview, setOverview] = useState<OverviewData | null>(null)
  const [channels, setChannels] = useState<Channel[]>([])
  const [requests, setRequests] = useState<Request[]>([])
  const [selected, setSelected] = useState<Request | null>(null)
  const [snapshot, setSnapshot] = useState<Record<string, string>>({})
  const [expandedAttempts, setExpandedAttempts] = useState<string[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState(false)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(50)
  const [total, setTotal] = useState(0)
  const [draftFilter, setDraftFilter] = useState<RequestFilter>(defaultFilter)
  const [filter, setFilter] = useState<RequestFilter>(defaultFilter)
  const [visibleColumns, setVisibleColumns] = useState<ColumnKey[]>(getInitialColumns)
  const [columnWidths, setColumnWidths] = useState<ColumnWidthMap>(getInitialColumnWidths)
  const { tableScrollY, tableWrapRef } = useTableScrollY()
  const wasActiveRef = useRef(active)
  const loadRequestsSeqRef = useRef(0)
  const loadReferenceSeqRef = useRef(0)
  const openRequestSeqRef = useRef(0)
  const openRequestTargetRef = useRef<string | null>(null)

  const query = useMemo(() => {
    const params = new URLSearchParams({
      page: String(page),
      pageSize: String(pageSize),
      days: String(filter.period),
    })
    Object.entries(filter).forEach(([key, value]) => {
      if (key !== 'period' && value) params.set(key, String(value))
    })
    return params.toString()
  }, [filter, page, pageSize])

  const channelGroups = useMemo(
    () =>
      Array.from(new Set(channels.map((item) => item.group).filter(Boolean) as string[])).sort(),
    [channels],
  )

  // 读取筛选后的请求分页和同时间窗口统计；仅最新请求会写入列表。
  const loadRequests = useCallback(
    async (notify = false): Promise<void> => {
      const seq = loadRequestsSeqRef.current + 1
      loadRequestsSeqRef.current = seq
      setLoading(true)
      setLoadError(false)
      try {
        const statsResponse = await adminFetch(`/api/admin/v1/logs/stats?days=${filter.period}`)
        if (!statsResponse.ok) throw await adminError(statsResponse, '读取请求统计失败')
        const statsResult = (await statsResponse.json()) as Stats
        const listParams = new URLSearchParams(query)
        listParams.set('from', statsResult.from)
        listParams.set('to', statsResult.to)
        const requestsResponse = await adminFetch(
          `/api/admin/v1/logs/requests?${listParams.toString()}`,
        )
        if (!requestsResponse.ok) throw await adminError(requestsResponse, '读取请求记录失败')
        const result = (await requestsResponse.json()) as PageData
        if (seq !== loadRequestsSeqRef.current) return
        setStats(statsResult)
        setRequests(result.items ?? [])
        setTotal(result.total ?? 0)
        setLoadError(false)
        if (notify) showSuccessToast('请求记录已刷新。')
      } catch (reason) {
        if (seq !== loadRequestsSeqRef.current) return
        setLoadError(true)
        showErrorToast(reason instanceof Error ? reason.message : '读取请求记录失败')
      } finally {
        if (seq === loadRequestsSeqRef.current) setLoading(false)
      }
    },
    [filter.period, query],
  )

  // 读取趋势数据和筛选目录；两份响应都解析后再按最新序号一次写入。
  const loadReferenceData = useCallback(async (): Promise<void> => {
    const seq = loadReferenceSeqRef.current + 1
    loadReferenceSeqRef.current = seq
    try {
      const [overviewResponse, channelsResponse] = await Promise.all([
        adminFetch('/api/admin/v1/overview'),
        adminFetch('/api/admin/v1/channels'),
      ])
      const nextOverview = overviewResponse.ok
        ? ((await overviewResponse.json()) as OverviewData)
        : null
      const nextChannels = channelsResponse.ok
        ? ((await channelsResponse.json()) as Channel[])
        : null
      if (seq !== loadReferenceSeqRef.current) return
      if (nextOverview) setOverview(nextOverview)
      if (nextChannels) setChannels(nextChannels)
    } catch {
      // 请求列表本身可用时，辅助趋势或筛选目录失败不阻断主页面。
    }
  }, [])

  useEffect(() => {
    void loadRequests()
  }, [loadRequests])

  // 页面从缓存切回可见时刷新列表和辅助数据；失活时作废在途响应。
  useEffect(() => {
    if (active && !wasActiveRef.current) {
      void loadRequests()
      void loadReferenceData()
    }
    if (!active && wasActiveRef.current) {
      loadRequestsSeqRef.current += 1
      loadReferenceSeqRef.current += 1
    }
    wasActiveRef.current = active
  }, [active, loadReferenceData, loadRequests])

  useEffect(() => {
    void loadReferenceData()
  }, [loadReferenceData])

  useEffect(() => {
    window.localStorage.setItem('oneai-proxy-request-columns', JSON.stringify(visibleColumns))
  }, [visibleColumns])

  useEffect(() => {
    window.localStorage.setItem('oneai-proxy-request-column-widths', JSON.stringify(columnWidths))
  }, [columnWidths])

  // 根据路由参数打开指定请求，支持概览页直达详情；过期响应丢弃。
  const openRequest = useCallback(async (item: Request | string): Promise<void> => {
    const id = typeof item === 'string' ? item : item.id
    const seq = openRequestSeqRef.current + 1
    openRequestSeqRef.current = seq
    openRequestTargetRef.current = id
    if (typeof item !== 'string') setSelected(item)
    setSnapshot({})
    try {
      const response = await adminFetch(`/api/admin/v1/logs/requests/${encodeURIComponent(id)}`)
      if (!response.ok) throw await adminError(response, '读取请求详情失败')
      const payload = (await response.json()) as { request: Request; contentBlobs?: Blob[] }
      if (seq !== openRequestSeqRef.current || openRequestTargetRef.current !== id) return
      const detail = {
        ...payload.request,
        contentBlobs: payload.request.contentBlobs ?? payload.contentBlobs ?? [],
      }
      setSelected(detail)
      setExpandedAttempts(defaultExpandedAttempt(detail.attempts ?? []))
    } catch (reason) {
      if (seq !== openRequestSeqRef.current || openRequestTargetRef.current !== id) return
      showErrorToast(reason instanceof Error ? reason.message : '读取请求详情失败')
      setSelected(null)
    }
  }, [])

  useEffect(() => {
    if (!active) return
    if (requestId) {
      void openRequest(requestId)
      return
    }
    openRequestSeqRef.current += 1
    openRequestTargetRef.current = null
    setSelected(null)
    setSnapshot({})
    setExpandedAttempts([])
  }, [active, openRequest, requestId])

  // 按需解密读取一份正文快照，已读取内容保留在当前详情会话中。
  async function loadSnapshot(blob: Blob): Promise<void> {
    if (snapshot[blob.id] !== undefined) return
    try {
      const response = await adminFetch(
        `/api/admin/v1/logs/requests/${encodeURIComponent(blob.requestId)}/content/${encodeURIComponent(blob.id)}`,
      )
      if (!response.ok) throw await adminError(response, '读取正文快照失败')
      const payload = (await response.json()) as { content: string }
      setSnapshot((current) => ({ ...current, [blob.id]: payload.content }))
    } catch (reason) {
      showErrorToast(reason instanceof Error ? reason.message : '读取正文快照失败')
    }
  }

  // 更新查询表单但不立即发送请求，避免输入过程中反复刷新表格。
  function updateDraftFilter(name: keyof RequestFilter, value: string | number): void {
    setDraftFilter((current) => ({ ...current, [name]: value }))
  }

  // 应用当前筛选条件并回到第一页。
  function applyFilter(): void {
    setPage(1)
    setFilter(draftFilter)
  }

  // 恢复默认筛选并立即重新查询。
  function resetFilter(): void {
    setDraftFilter(defaultFilter)
    setFilter(defaultFilter)
    setPage(1)
  }

  // 切换请求表格列并将结果持久化到浏览器本地。
  function toggleColumn(key: ColumnKey, checked: boolean): void {
    setVisibleColumns((current) => {
      const next = checked
        ? Array.from(new Set([...current, key]))
        : current.filter((item) => item !== key)
      const ordered = columnOptions.map((item) => item.key).filter((item) => next.includes(item))
      return ordered.length > 0 ? ordered : current
    })
  }

  // 在列宽拖拽结束后记录最新宽度，供刷新页面时恢复。
  const cacheColumnWidth = useCallback((column: Request): Request => {
    const resized = column as unknown as { key?: string | number; width?: string | number }
    const key = resized.key as ColumnKey
    const width = resized.width
    if (
      columnOptions.some((item) => item.key === key) &&
      typeof width === 'number' &&
      Number.isFinite(width)
    ) {
      setColumnWidths((current) => ({ ...current, [key]: Math.round(width) }))
    }
    return column
  }, [])

  // 导出当前筛选页中的真实请求记录为 CSV 文件。
  function exportCurrentResults(): void {
    const headers = [
      'request_id',
      'started_at',
      'channel',
      'client_model',
      'logical_model',
      'status',
      'protocol',
      'group',
      'input_tokens',
      'output_tokens',
      'total_tokens',
      'ttft_ms',
      'tps',
      'latency_ms',
    ]
    const escape = (value: unknown) => `"${String(value ?? '').replace(/"/g, '""')}"`
    const rows = requests.map((item) =>
      [
        item.id,
        item.startedAt,
        item.initialChannelName ?? item.finalChannelName,
        item.clientModel,
        item.logicalModel,
        item.finalStatus,
        item.protocol,
        item.groupName,
        item.inputTokens,
        item.outputTokens,
        item.totalTokens,
        item.ttftMs,
        item.tps,
        item.latencyMs,
      ]
        .map(escape)
        .join(','),
    )
    const blob = new window.Blob([[headers.join(','), ...rows].join('\n')], {
      type: 'text/csv;charset=utf-8',
    })
    const url = window.URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = `oneai-proxy-requests-${Date.now()}.csv`
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
    window.URL.revokeObjectURL(url)
  }

  // 打开请求详情并将请求 ID 写入 hash，支持刷新后恢复。
  function showRequest(item: Request): void {
    window.location.assign(`#requests?request=${encodeURIComponent(item.id)}`)
  }

  // 关闭详情抽屉并清理 hash 中的请求 ID。
  function closeRequest(): void {
    openRequestSeqRef.current += 1
    openRequestTargetRef.current = null
    setSelected(null)
    setSnapshot({})
    setExpandedAttempts([])
    if (window.location.hash.startsWith('#requests?')) window.location.assign('#requests')
  }

  // 复制当前请求 ID，并给出明确反馈。
  async function copyRequestID(): Promise<void> {
    if (!selected) return
    try {
      await navigator.clipboard.writeText(selected.id)
      showSuccessToast('请求 ID 已复制。')
    } catch {
      showErrorToast('复制请求 ID 失败。')
    }
  }

  const pageCount = Math.max(1, Math.ceil(total / pageSize))
  const totalTokens =
    stats?.totalTokens ??
    (stats?.inputTokens != null && stats?.outputTokens != null
      ? stats.inputTokens + stats.outputTokens
      : null)
  const isDayWindow = filter.period === 1
  const requestValues = isDayWindow ? (overview?.trends.map((item) => item.requestCount) ?? []) : []
  const successValues = isDayWindow ? (overview?.trends.map((item) => item.successRate) ?? []) : []
  const trendLabel = isDayWindow ? '24 小时趋势' : ''
  const requestChange = isDayWindow ? (overview?.metrics.requestChange ?? null) : null
  const rootBlobs = (selected?.contentBlobs ?? []).filter((blob) => !blob.attemptId)
  const clientRequestBlobs = rootBlobs.filter(
    (blob) => blob.contentType === 'request' || blob.contentType === 'request_headers',
  )
  const finalResponseBlobs = rootBlobs.filter(
    (blob) => blob.contentType === 'response' || blob.contentType === 'response_headers',
  )
  const tableColumns = useMemo(() => {
    const columns: RequestColumn[] = [
      {
        key: 'startedAt',
        dataIndex: 'startedAt',
        title: '请求时间',
        width: columnWidths.startedAt ?? defaultColumnWidths.startedAt,
        render: (value: string) => formatListDate(value),
      },
      {
        key: 'channel',
        title: '渠道',
        width: columnWidths.channel ?? defaultColumnWidths.channel,
        render: (_value: unknown, item: Request) => {
          const channelChain = item.channelChain?.filter(Boolean) ?? []
          const channelContent = channelChain.length > 0 ? channelChain.join(' → ') : '无渠道链路'
          return (
            <div className="request-channel-content">
              <Tooltip content={channelContent} position="topLeft">
                <span className="request-channel-cell">
                  {item.finalChannelName || item.initialChannelName || '—'}
                </span>
              </Tooltip>
              <Tag color="grey">尝试 {item.attemptCount}</Tag>
              {(item.channelSwitchCount ?? 0) > 0 && (
                <Tag color="orange">切换 {item.channelSwitchCount}</Tag>
              )}
            </div>
          )
        },
      },
      {
        key: 'model',
        title: '模型',
        width: columnWidths.model ?? defaultColumnWidths.model,
        render: (_value: unknown, item: Request) => {
          const text = `${item.clientModel}${item.logicalModel && item.logicalModel !== item.clientModel ? ` → ${item.logicalModel}` : ''}`
          return (
            <Tooltip content={text} position="top">
              <span className="request-model-cell">{text}</span>
            </Tooltip>
          )
        },
      },
      {
        key: 'status',
        title: '状态',
        width: columnWidths.status ?? defaultColumnWidths.status,
        render: (_value: unknown, item: Request) => {
          const status = statusText[item.finalStatus] ?? item.finalStatus
          const text = [status, item.errorMessage].filter(Boolean).join(' · ')
          return (
            <Tooltip content={text} position="top">
              <div className="request-status-content">
                <Tag color={statusColor(item.finalStatus)}>{status}</Tag>
                {item.errorMessage && (
                  <small className="request-status-error">{item.errorMessage}</small>
                )}
              </div>
            </Tooltip>
          )
        },
      },
      {
        key: 'protocol',
        dataIndex: 'protocol',
        title: '协议',
        width: columnWidths.protocol ?? defaultColumnWidths.protocol,
        render: (value: string) => protocolLabel(value),
      },
      {
        key: 'group',
        dataIndex: 'groupName',
        title: '分组',
        width: columnWidths.group ?? defaultColumnWidths.group,
        render: (value: string | undefined) => value || '—',
      },
      {
        key: 'input',
        dataIndex: 'inputTokens',
        title: '输入',
        width: columnWidths.input ?? defaultColumnWidths.input,
        render: (value: number | null) => formatNumber(value),
      },
      {
        key: 'output',
        dataIndex: 'outputTokens',
        title: '输出',
        width: columnWidths.output ?? defaultColumnWidths.output,
        render: (value: number | null) => formatNumber(value),
      },
      {
        key: 'cacheRead',
        dataIndex: 'cacheReadInputTokens',
        title: '缓存读取',
        width: columnWidths.cacheRead ?? defaultColumnWidths.cacheRead,
        render: (value: number | null) => formatNumber(value),
      },
      {
        key: 'cacheWrite',
        dataIndex: 'cacheWriteInputTokens',
        title: '缓存写入',
        width: columnWidths.cacheWrite ?? defaultColumnWidths.cacheWrite,
        render: (value: number | null) => formatNumber(value),
      },
      {
        key: 'reasoning',
        dataIndex: 'reasoningTokens',
        title: '思考',
        width: columnWidths.reasoning ?? defaultColumnWidths.reasoning,
        render: (value: number | null) => formatNumber(value),
      },
      {
        key: 'total',
        dataIndex: 'totalTokens',
        title: '总 Tokens',
        width: columnWidths.total ?? defaultColumnWidths.total,
        render: (value: number | null) => formatNumber(value),
      },
      {
        key: 'cacheRate',
        dataIndex: 'cacheRate',
        title: '缓存率',
        width: columnWidths.cacheRate ?? defaultColumnWidths.cacheRate,
        render: (value: number | null) => formatPercent(value),
      },
      {
        key: 'ttft',
        dataIndex: 'ttftMs',
        title: 'TTFT',
        width: columnWidths.ttft ?? defaultColumnWidths.ttft,
        render: (value: number | null) => formatDuration(value),
      },
      {
        key: 'tps',
        dataIndex: 'tps',
        title: 'TPS',
        width: columnWidths.tps ?? defaultColumnWidths.tps,
        render: (value: number | null) => (value == null ? '—' : value.toFixed(1)),
      },
      {
        key: 'latency',
        dataIndex: 'latencyMs',
        title: '耗时',
        width: columnWidths.latency ?? defaultColumnWidths.latency,
        render: (value: number | null) => formatDuration(value),
      },
      {
        key: 'detail',
        title: '详情',
        width: columnWidths.detail ?? defaultColumnWidths.detail,
        fixed: 'right' as const,
        render: (_value: unknown, item: Request) => (
          <Button
            className="request-detail-button"
            icon={<IconExternalOpen />}
            iconPosition="right"
            theme="borderless"
            type="primary"
            onClick={() => showRequest(item)}
          >
            查看
          </Button>
        ),
      },
    ]
    return columns
      .filter((column) => visibleColumns.includes(column.key))
      .map((column) =>
        column.key === 'detail'
          ? { ...column, align: 'center' as const }
          : { ...column, align: 'center' as const, ellipsis: { showTitle: false } },
      )
  }, [columnWidths, visibleColumns])
  const tableScrollWidth = tableColumns.reduce((sum, column) => sum + column.width, 0)

  return (
    <section className="page-content requests-page">
      <div className="overview-metric-row">
        <RequestMetricCard
          label={`请求总数 (${filter.period === 1 ? '24H' : `${filter.period}D`})`}
          value={loading ? '同步中' : formatNumber(stats?.requestCount)}
          change={requestChange}
          note={`${formatNumber(stats?.attemptCount)} 次渠道尝试`}
          values={requestValues}
          trendLabel={trendLabel}
        />
        <RequestMetricCard
          label={`成功率 (${filter.period === 1 ? '24H' : `${filter.period}D`})`}
          value={loading ? '同步中' : formatPercent(stats?.successRate, 2)}
          change={null}
          note={`${formatNumber(stats?.requestFailureCount)} 次失败`}
          values={successValues}
          trendLabel={trendLabel}
          tone="green"
          favorableDirection="up"
        />
        <RequestMetricCard
          label="平均 TTFT"
          value={loading ? '同步中' : formatDuration(stats?.averageTTFTMs)}
          change={null}
          note={`P95 ${formatPercentileDuration(stats?.p95TTFTMs, stats?.p95TTFTOverflow)}`}
          values={[]}
          trendLabel=""
          favorableDirection="down"
        />
        <RequestMetricCard
          label="总 Tokens"
          value={loading ? '同步中' : formatCompactNumber(totalTokens)}
          change={null}
          note={`缓存读取 ${formatCompactNumber(stats?.cacheReadInputTokens)}`}
          values={[]}
          trendLabel=""
        />
      </div>

      <section className="request-ledger apple-card">
        <header className="request-ledger-header">
          <div className="request-ledger-title">
            <div>
              <h1>
                请求记录 <span>共 {total.toLocaleString('zh-CN')} 条</span>
              </h1>
              <p>一条客户端请求一行，渠道尝试、跳过与切换在详情中查看</p>
            </div>
          </div>
          <div className="request-ledger-actions">
            <Tooltip content="刷新请求记录">
              <Button
                className="request-icon-button"
                icon={<IconRefresh />}
                theme="borderless"
                aria-label="刷新请求记录"
                onClick={() => void loadRequests(true)}
              />
            </Tooltip>
            <Popover
              trigger="click"
              position="bottomRight"
              contentClassName="request-columns-popover"
              content={
                <div className="request-columns-menu">
                  <strong>显示列</strong>
                  {columnOptions.map((item) => (
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
              type="primary"
              icon={<IconDownload />}
              disabled={requests.length === 0}
              onClick={exportCurrentResults}
            >
              导出当前结果
            </Button>
          </div>
        </header>

        <div className="request-filter-panel">
          <div className="request-filter-row request-filter-row-primary">
            <Select
              className="request-filter-period"
              prefix={<IconCalendar />}
              value={draftFilter.period}
              onChange={(value) => updateDraftFilter('period', Number(value))}
            >
              <Select.Option value={1}>最近 24 小时</Select.Option>
              <Select.Option value={7}>最近 7 天</Select.Option>
              <Select.Option value={30}>最近 30 天</Select.Option>
            </Select>
            <Input
              className="request-filter-model"
              prefix={<IconSearch />}
              value={draftFilter.model}
              placeholder="搜索原始模型或映射模型"
              onChange={(value) => updateDraftFilter('model', value)}
            />
            <Select
              value={draftFilter.protocol || undefined}
              placeholder="全部协议"
              showClear
              onChange={(value) => updateDraftFilter('protocol', String(value ?? ''))}
            >
              <Select.Option value="openai_chat">OpenAI Chat</Select.Option>
              <Select.Option value="openai_responses">OpenAI Responses</Select.Option>
              <Select.Option value="anthropic_messages">Anthropic Messages</Select.Option>
            </Select>
            <Select
              value={draftFilter.status || undefined}
              placeholder="全部状态"
              showClear
              onChange={(value) => updateDraftFilter('status', String(value ?? ''))}
            >
              {requestStatusValues.map((value) => (
                <Select.Option key={value} value={value}>
                  {statusText[value]}
                </Select.Option>
              ))}
            </Select>
            <Select
              value={draftFilter.channel || undefined}
              placeholder="全部渠道"
              showClear
              filter
              onChange={(value) => updateDraftFilter('channel', String(value ?? ''))}
            >
              {channels.map((item) => (
                <Select.Option key={item.id} value={item.id}>
                  {item.name}
                </Select.Option>
              ))}
            </Select>
            <Select
              value={draftFilter.group || undefined}
              placeholder="全部分组"
              showClear
              onChange={(value) => updateDraftFilter('group', String(value ?? ''))}
            >
              {channelGroups.map((item) => (
                <Select.Option key={item} value={item}>
                  {item}
                </Select.Option>
              ))}
            </Select>
          </div>
          <div className="request-filter-row request-filter-row-secondary">
            <Select
              className="request-filter-error"
              value={draftFilter.errorClass || undefined}
              placeholder="全部错误"
              showClear
              onChange={(value) => updateDraftFilter('errorClass', String(value ?? ''))}
            >
              {errorOptions.map((item) => (
                <Select.Option key={item} value={item}>
                  {item}
                </Select.Option>
              ))}
            </Select>
            <div className="request-fallback-filter">
              <span>跨渠道切换：</span>
              <RadioGroup
                type="button"
                value={draftFilter.fallback}
                options={[
                  { label: '全部', value: '' },
                  { label: '有', value: 'yes' },
                  { label: '无', value: 'no' },
                ]}
                onChange={(event) => updateDraftFilter('fallback', String(event.target.value))}
              />
            </div>
            <Input
              className="request-filter-keyword"
              prefix={<IconSearch />}
              value={draftFilter.keyword}
              placeholder="请求 ID、错误摘要"
              onChange={(value) => updateDraftFilter('keyword', value)}
              onEnterPress={applyFilter}
            />
            <Button type="primary" onClick={applyFilter}>
              查询
            </Button>
            <Button theme="borderless" onClick={resetFilter}>
              重置
            </Button>
          </div>
        </div>

        <div ref={tableWrapRef} className="request-table-wrap">
          <Table<Request>
            rowKey="id"
            dataSource={requests}
            columns={tableColumns}
            empty={<span aria-hidden="true" />}
            pagination={false}
            resizable={{ onResizeStop: cacheColumnWidth }}
            scroll={{ x: tableScrollWidth, y: tableScrollY }}
            onRow={(record) => ({
              onDoubleClick: () => {
                if (record) showRequest(record)
              },
            })}
          />
          {(loading || loadError || requests.length === 0) && (
            <div className="table-state-overlay">
              {loading ? (
                <Spin size="large" />
              ) : loadError ? (
                <Empty description="加载失败，请重试">
                  <Button type="primary" onClick={() => void loadRequests()}>
                    重试
                  </Button>
                </Empty>
              ) : (
                <span>暂无符合条件的请求记录。</span>
              )}
            </div>
          )}
        </div>

        <footer className="request-pagination">
          <span>
            显示第 {total === 0 ? 0 : (page - 1) * pageSize + 1}–{Math.min(page * pageSize, total)}{' '}
            条， 共 {total.toLocaleString('zh-CN')} 条
          </span>
          <div className="request-pagination-controls">
            <Select
              value={pageSize}
              onChange={(value) => {
                setPageSize(Number(value))
                setPage(1)
              }}
            >
              <Select.Option value={20}>20 / 页</Select.Option>
              <Select.Option value={50}>50 / 页</Select.Option>
              <Select.Option value={100}>100 / 页</Select.Option>
            </Select>
            <Pagination
              currentPage={page}
              pageSize={pageSize}
              total={total}
              showSizeChanger={false}
              onChange={(nextPage) => setPage(Math.min(Math.max(1, nextPage), pageCount))}
            />
          </div>
        </footer>
      </section>

      {active && selected && (
        <RequestDetailPanel
          selected={selected}
          expandedAttempts={expandedAttempts}
          onExpandedAttemptsChange={setExpandedAttempts}
          clientRequestBlobs={clientRequestBlobs}
          finalResponseBlobs={finalResponseBlobs}
          snapshot={snapshot}
          onLoadSnapshot={(blob) => void loadSnapshot(blob)}
          onClose={closeRequest}
          onCopyRequestID={copyRequestID}
        />
      )}
    </section>
  )
}

export default RequestsPage
