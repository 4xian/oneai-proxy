import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import {
  Button,
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
  IconColumnsStroked,
  IconDelete,
  IconEdit,
  IconEyeOpened,
  IconPlus,
  IconRefresh,
} from '@douyinfe/semi-icons'
import type { AppTheme, ThemePreference } from '../../components/PageHeader'
import ModelCatalogDetail from './components/ModelCatalogDetail'
import {
  ManualModelModal,
  ModelSourceModal,
  ModelSyncPreviewModal,
} from './components/ModelManagementDialogs'
import {
  dateText,
  defaultModelSource,
  emptyModelEntry,
  formatCount,
  priceText,
  protocolText,
  statusText,
  type CatalogFacets,
  type ModelCatalogEntry,
  type Preview,
} from './components/modelCatalog'
import { adminError, adminFetch } from '../../api'
import { useTableScrollY } from '../../hooks/useTableScrollY'
import { showErrorToast, showSuccessToast, showWarningToast } from '../../notifications'

type Props = {
  theme: AppTheme
  themePreference: ThemePreference
  onChangeTheme: () => void
  active?: boolean
}
type ModelColumnKey =
  | 'model'
  | 'protocol'
  | 'vendor-provider'
  | 'capabilities'
  | 'limits'
  | 'pricing'
  | 'source'
  | 'updatedAt'
  | 'actions'
type ModelColumnWidthMap = Partial<Record<ModelColumnKey, number>>

const modelColumnDefaults: Record<ModelColumnKey, number> = {
  model: 240,
  protocol: 160,
  'vendor-provider': 170,
  capabilities: 220,
  limits: 160,
  pricing: 190,
  source: 150,
  updatedAt: 180,
  actions: 112,
}

const modelColumnOptions: Array<{ key: ModelColumnKey; label: string }> = [
  { key: 'model', label: '模型' },
  { key: 'protocol', label: '协议' },
  { key: 'vendor-provider', label: '厂商 / 供应商' },
  { key: 'capabilities', label: '类型与能力' },
  { key: 'limits', label: '上下文 / 最大输出' },
  { key: 'pricing', label: '输入 / 输出费用' },
  { key: 'source', label: '来源' },
  { key: 'updatedAt', label: '更新时间' },
  { key: 'actions', label: '操作' },
]

// 从浏览器本地恢复模型表格列设置，异常值回退为全部默认列。
function getInitialModelColumns(): ModelColumnKey[] {
  try {
    const saved = JSON.parse(window.localStorage.getItem('oneai-proxy-model-columns') ?? '[]')
    const allowed = new Set(modelColumnOptions.map((item) => item.key))
    const columns = Array.isArray(saved)
      ? saved.filter(
          (item): item is ModelColumnKey =>
            typeof item === 'string' && allowed.has(item as ModelColumnKey),
        )
      : []
    return columns.length > 0 ? columns : modelColumnOptions.map((item) => item.key)
  } catch {
    return modelColumnOptions.map((item) => item.key)
  }
}

// 从浏览器本地恢复模型表格列宽，只接受合理范围内的数值。
function getInitialModelColumnWidths(): ModelColumnWidthMap {
  try {
    const saved = JSON.parse(
      window.localStorage.getItem('oneai-proxy-model-column-widths') ?? '{}',
    ) as unknown
    if (!saved || typeof saved !== 'object' || Array.isArray(saved)) return {}
    const record = saved as Record<string, unknown>
    const widths: ModelColumnWidthMap = {}
    Object.keys(modelColumnDefaults).forEach((key) => {
      const width = record[key]
      if (typeof width === 'number' && Number.isFinite(width) && width >= 64 && width <= 800) {
        widths[key as ModelColumnKey] = Math.round(width)
      }
    })
    return widths
  } catch {
    return {}
  }
}

// ModelsPage 负责模型目录查询、同步、手工维护与详情查看的页面编排。
function ModelsPage({ active = true }: Props) {
  const [items, setItems] = useState<ModelCatalogEntry[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const [search, setSearch] = useState('')
  const [protocol, setProtocol] = useState('')
  const [vendor, setVendor] = useState('')
  const [modelType, setModelType] = useState('')
  const [capability, setCapability] = useState('')
  const [sourceStatus, setSourceStatus] = useState('')
  const [sourceURL, setSourceURL] = useState(defaultModelSource)
  const [facets, setFacets] = useState<CatalogFacets>({
    vendors: [],
    modelTypes: [],
    capabilities: [],
  })
  const [loading, setLoading] = useState(false)
  const [loadError, setLoadError] = useState(false)
  const [preview, setPreview] = useState<Preview | null>(null)
  const [sourceModalOpen, setSourceModalOpen] = useState(false)
  const [previewOpen, setPreviewOpen] = useState(false)
  const [previewSelection, setPreviewSelection] = useState<string[]>([])
  const [previewSearch, setPreviewSearch] = useState('')
  const [syncing, setSyncing] = useState(false)
  const [manualOpen, setManualOpen] = useState(false)
  const [manualEntry, setManualEntry] = useState<ModelCatalogEntry>(emptyModelEntry)
  const [editingKey, setEditingKey] = useState('')
  const [saving, setSaving] = useState(false)
  const catalogRequestIdRef = useRef(0)
  const savingLockRef = useRef(false)
  const wasActiveRef = useRef(active)
  const [selectedKeys, setSelectedKeys] = useState<string[]>([])
  const [deleteConfirmOpen, setDeleteConfirmOpen] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [detailEntry, setDetailEntry] = useState<ModelCatalogEntry | null>(null)
  const [visibleColumns, setVisibleColumns] = useState<ModelColumnKey[]>(getInitialModelColumns)
  const [columnWidths, setColumnWidths] = useState<ModelColumnWidthMap>(getInitialModelColumnWidths)
  const { tableScrollY, tableWrapRef } = useTableScrollY()

  // 读取模型目录分页数据；仅最新请求会写入列表，失败保留上次成功数据。
  const loadCatalog = useCallback(async (): Promise<void> => {
    const requestId = catalogRequestIdRef.current + 1
    catalogRequestIdRef.current = requestId
    setLoading(true)
    try {
      const params = new URLSearchParams({
        page: String(page),
        pageSize: String(pageSize),
        grouped: 'false',
      })
      if (search.trim()) params.set('search', search.trim())
      if (protocol) params.set('protocol', protocol)
      if (vendor) params.set('vendor', vendor)
      if (modelType) params.set('modelType', modelType)
      if (capability) params.set('capability', capability)
      if (sourceStatus) params.set('sourceStatus', sourceStatus)
      const response = await adminFetch(`/api/admin/v1/models/catalog?${params.toString()}`)
      if (!response.ok) throw await adminError(response, '读取模型目录失败')
      const result = (await response.json()) as {
        items: ModelCatalogEntry[]
        total: number
        sourceUrl?: string
        facets?: CatalogFacets
      }
      if (requestId !== catalogRequestIdRef.current) return
      setItems(result.items ?? [])
      setTotal(result.total ?? 0)
      setFacets(result.facets ?? { vendors: [], modelTypes: [], capabilities: [] })
      if (result.sourceUrl) setSourceURL(result.sourceUrl)
      setLoadError(false)
    } catch (reason) {
      if (requestId !== catalogRequestIdRef.current) return
      setLoadError(true)
      showErrorToast(reason instanceof Error ? reason.message : '读取模型目录失败')
    } finally {
      if (requestId === catalogRequestIdRef.current) setLoading(false)
    }
  }, [capability, modelType, page, pageSize, protocol, search, sourceStatus, vendor])

  // 筛选或分页变化时刷新目录；未激活时跳过，避免与进入页补拉重复请求。
  useEffect(() => {
    if (!wasActiveRef.current) return
    void loadCatalog()
  }, [loadCatalog])

  // 仅在 active 从 false 变为 true 时补拉；首屏由 loadCatalog 依赖 effect 负责。
  useEffect(() => {
    if (!active) {
      wasActiveRef.current = false
      return
    }
    if (wasActiveRef.current) return
    wasActiveRef.current = true
    void loadCatalog()
  }, [active, loadCatalog])

  useEffect(() => {
    window.localStorage.setItem('oneai-proxy-model-columns', JSON.stringify(visibleColumns))
  }, [visibleColumns])

  useEffect(() => {
    window.localStorage.setItem('oneai-proxy-model-column-widths', JSON.stringify(columnWidths))
  }, [columnWidths])

  // 切换模型表格列并将结果持久化到浏览器本地。
  function toggleColumn(key: ModelColumnKey, checked: boolean): void {
    setVisibleColumns((current) => {
      const next = checked
        ? Array.from(new Set([...current, key]))
        : current.filter((item) => item !== key)
      const ordered = modelColumnOptions
        .map((item) => item.key)
        .filter((item) => next.includes(item))
      return ordered.length > 0 ? ordered : current
    })
  }
  // 请求在线源并打开差异预览弹窗，不会写入本地目录。
  async function previewSource(): Promise<void> {
    setSyncing(true)
    try {
      const response = await adminFetch('/api/admin/v1/models/preview', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ sourceUrl: sourceURL.trim() || defaultModelSource }),
      })
      if (!response.ok) throw await adminError(response, '同步预览失败')
      const result = (await response.json()) as Preview
      setPreview(result)
      setPreviewSearch('')
      setPreviewSelection([])
      setSourceModalOpen(false)
      setPreviewOpen(true)
    } catch (reason) {
      showErrorToast(reason instanceof Error ? reason.message : '同步预览失败')
    } finally {
      setSyncing(false)
    }
  }

  // 提交预览中的选中差异；无选中项不发请求，并按写入结果提示。
  async function applyPreview(): Promise<void> {
    if (!preview) return
    if (previewSelection.length === 0) {
      showWarningToast('请先选择要应用的模型。')
      return
    }
    setSyncing(true)
    try {
      const response = await adminFetch('/api/admin/v1/models/apply', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          sourceUrl: preview.sourceUrl,
          sourceDigest: preview.sourceDigest,
          selectedStableKeys: previewSelection,
        }),
      })
      if (!response.ok) throw await adminError(response, '应用模型同步失败')
      const result = (await response.json()) as {
        applied?: number
        skipped?: number
        conflicts?: number
      }
      const applied = result.applied ?? 0
      const skipped = result.skipped ?? 0
      const conflicts = result.conflicts ?? 0
      setPreviewOpen(false)
      if (applied === 0) {
        showWarningToast('未写入目录：所选记录存在冲突或无变化。')
      } else {
        const extras = [
          skipped > 0 ? `跳过 ${skipped} 条` : '',
          conflicts > 0 ? `冲突 ${conflicts} 条` : '',
        ].filter(Boolean)
        showSuccessToast(
          extras.length > 0
            ? `已应用 ${applied} 条模型目录变更，${extras.join('，')}。`
            : `已应用 ${applied} 条模型目录变更。`,
        )
      }
      await loadCatalog()
    } catch (reason) {
      showErrorToast(reason instanceof Error ? reason.message : '应用模型同步失败')
    } finally {
      setSyncing(false)
    }
  }

  // 删除用户勾选的模型目录记录。
  async function performDeleteSelected(keys: string[]): Promise<boolean> {
    if (keys.length === 0) {
      showWarningToast('请先勾选可删除的模型。')
      return false
    }
    setDeleting(true)
    try {
      const response = await adminFetch('/api/admin/v1/models/batch', {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ stableKeys: keys }),
      })
      if (!response.ok) throw await adminError(response, '删除模型失败')
      showSuccessToast(`已删除 ${keys.length} 条模型。`)
      setSelectedKeys([])
      if (page === 1) {
        await loadCatalog()
      } else {
        setPage(1)
      }
      return true
    } catch (reason) {
      showErrorToast(reason instanceof Error ? reason.message : '删除模型失败')
      return false
    } finally {
      setDeleting(false)
    }
  }

  // 打开受控的 Semi 确认弹窗，避免误删已选模型。
  function deleteSelected(): void {
    if (selectedKeys.length === 0) {
      showWarningToast('请先勾选可删除的模型。')
      return
    }
    setDeleteConfirmOpen(true)
  }

  // 确认删除并仅在请求成功后关闭弹窗。
  async function confirmDeleteSelected(): Promise<void> {
    if (await performDeleteSelected(selectedKeys)) {
      setDeleteConfirmOpen(false)
    }
  }

  // 打开模型新增或编辑弹窗，并复制嵌套表单状态。
  const openManual = useCallback((entry?: ModelCatalogEntry): void => {
    setEditingKey(entry?.stableKey ?? '')
    setManualEntry(
      entry
        ? {
            ...entry,
            modalities: { ...entry.modalities },
            pricing: { ...entry.pricing },
            capabilities: [...entry.capabilities],
          }
        : {
            ...emptyModelEntry,
            modalities: { input: ['text'], output: ['text'] },
            pricing: {},
            capabilities: [],
          },
    )
    setManualOpen(true)
  }, [])

  // 新增或保存手工模型，保存中忽略重复提交。
  async function saveManual(event: FormEvent): Promise<void> {
    event.preventDefault()
    if (savingLockRef.current || saving) return
    if (!manualEntry.modelId.trim()) {
      showWarningToast('模型 ID 不能为空。')
      return
    }
    const validProtocol = ['openai_chat', 'openai_responses', 'anthropic_messages'].includes(
      manualEntry.protocol,
    )
    if (!validProtocol) {
      showWarningToast('请选择有效协议。')
      return
    }
    const payload = {
      ...manualEntry,
      modelId: manualEntry.modelId.trim(),
      capabilities: manualEntry.capabilities ?? [],
      sourceType: editingKey ? manualEntry.sourceType : 'manual',
      sourceStatus: editingKey ? manualEntry.sourceStatus : 'valid',
      stableKey: undefined,
    }
    savingLockRef.current = true
    setSaving(true)
    try {
      const endpoint = editingKey
        ? `/api/admin/v1/models/${encodeURIComponent(editingKey)}`
        : '/api/admin/v1/models'
      const response = await adminFetch(endpoint, {
        method: editingKey ? 'PUT' : 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      })
      if (!response.ok) throw await adminError(response, '保存模型失败')
      setManualOpen(false)
      showSuccessToast(editingKey ? '模型已更新。' : '手工模型已新增。')
      await loadCatalog()
    } catch (reason) {
      showErrorToast(reason instanceof Error ? reason.message : '保存模型失败')
    } finally {
      savingLockRef.current = false
      setSaving(false)
    }
  }

  // 在列宽拖拽结束后记录最新宽度，供刷新页面时恢复。
  const cacheModelColumnWidth = useCallback((column: ModelCatalogEntry): ModelCatalogEntry => {
    const resized = column as unknown as { key?: string | number; width?: string | number }
    const key = resized.key as ModelColumnKey
    const width = resized.width
    if (
      Object.prototype.hasOwnProperty.call(modelColumnDefaults, key) &&
      typeof width === 'number' &&
      Number.isFinite(width)
    ) {
      setColumnWidths((current) => ({ ...current, [key]: Math.round(width) }))
    }
    return column
  }, [])

  const rowSelection = {
    selectedRowKeys: selectedKeys,
    getCheckboxProps: (_entry: ModelCatalogEntry) => ({ disabled: false }),
    onChange: (keys?: (string | number)[]) => {
      const currentPageKeys = new Set(items.map((entry) => entry.stableKey))
      const nextKeys = selectedKeys.filter((key) => !currentPageKeys.has(key))
      ;(keys ?? []).forEach((key) => nextKeys.push(String(key)))
      setSelectedKeys(Array.from(new Set(nextKeys)))
    },
  }
  const columns = useMemo(
    () => [
      {
        title: '模型',
        dataIndex: 'modelId',
        key: 'model',
        width: columnWidths.model ?? modelColumnDefaults.model,
        align: 'center' as const,
        ellipsis: { showTitle: false },
        render: (value: string, entry: ModelCatalogEntry) => (
          <Tooltip content={entry.displayName || value} position="top">
            <div className="model-primary-cell">
              <strong>{entry.displayName || value}</strong>
            </div>
          </Tooltip>
        ),
      },
      {
        title: '协议',
        dataIndex: 'protocol',
        key: 'protocol',
        width: columnWidths.protocol ?? modelColumnDefaults.protocol,
        align: 'center' as const,
        ellipsis: { showTitle: false },
        render: (value: string) => {
          const text = value ? protocolText(value) : '—'
          return (
            <Tooltip content={text} position="top">
              <span>{text}</span>
            </Tooltip>
          )
        },
      },
      {
        title: '厂商 / 供应商',
        key: 'vendor-provider',
        width: columnWidths['vendor-provider'] ?? modelColumnDefaults['vendor-provider'],
        align: 'center' as const,
        ellipsis: { showTitle: false },
        render: (_value: unknown, entry: ModelCatalogEntry) => {
          const text = [entry.vendor, entry.provider].filter(Boolean).join(' / ') || '—'
          return (
            <Tooltip content={text} position="top">
              <span>{text}</span>
            </Tooltip>
          )
        },
      },
      {
        title: '类型与能力',
        key: 'capabilities',
        width: columnWidths.capabilities ?? modelColumnDefaults.capabilities,
        align: 'center' as const,
        ellipsis: { showTitle: false },
        render: (_value: unknown, entry: ModelCatalogEntry) => {
          const values = [entry.modelType, ...entry.capabilities].filter(Boolean)
          const text = values.join('、') || '—'
          return (
            <Tooltip content={text} position="top">
              <div className="model-tags">
                {entry.modelType && <Tag type="light">{entry.modelType}</Tag>}
                <span className="model-tags-summary">{entry.capabilities.join('、') || '—'}</span>
              </div>
            </Tooltip>
          )
        },
      },
      {
        title: '上下文 / 最大输出',
        key: 'limits',
        width: columnWidths.limits ?? modelColumnDefaults.limits,
        align: 'center' as const,
        ellipsis: { showTitle: false },
        render: (_value: unknown, entry: ModelCatalogEntry) => {
          const text = `${formatCount(entry.contextWindow)} / ${formatCount(entry.maxOutputTokens)}`
          return (
            <Tooltip content={text} position="top">
              <span>{text}</span>
            </Tooltip>
          )
        },
      },
      {
        title: '输入 / 输出费用',
        key: 'pricing',
        width: columnWidths.pricing ?? modelColumnDefaults.pricing,
        align: 'center' as const,
        ellipsis: { showTitle: false },
        render: (_value: unknown, entry: ModelCatalogEntry) => {
          const text = `${priceText(entry.pricing.input, entry.currency)} / ${priceText(entry.pricing.output, entry.currency)}${entry.billingUnit ? ` · ${entry.billingUnit}` : ''}`
          return (
            <Tooltip content={text} position="top">
              <span className="model-price-range">{text}</span>
            </Tooltip>
          )
        },
      },
      {
        title: '来源',
        key: 'source',
        width: columnWidths.source ?? modelColumnDefaults.source,
        align: 'center' as const,
        ellipsis: { showTitle: false },
        render: (_value: unknown, entry: ModelCatalogEntry) => {
          const source =
            entry.sourceType === 'manual' ? '手工新增' : entry.sourceAdapter || '在线同步'
          const text = `${statusText(entry.sourceStatus)} ${source}`
          return (
            <Tooltip content={text} position="top">
              <div className="model-tags">
                <Tag
                  color={
                    entry.sourceStatus === 'valid'
                      ? 'green'
                      : entry.sourceStatus === 'retired'
                        ? 'red'
                        : 'orange'
                  }
                >
                  {statusText(entry.sourceStatus)}
                </Tag>
                <span className="model-tags-summary">{source}</span>
              </div>
            </Tooltip>
          )
        },
      },
      {
        title: '更新时间',
        dataIndex: 'updatedAt',
        key: 'updatedAt',
        width: columnWidths.updatedAt ?? modelColumnDefaults.updatedAt,
        align: 'center' as const,
        ellipsis: { showTitle: false },
        render: (value: string) => {
          const text = dateText(value)
          return (
            <Tooltip content={text} position="top">
              <span>{text}</span>
            </Tooltip>
          )
        },
      },
      {
        title: '操作',
        key: 'actions',
        fixed: 'right' as const,
        align: 'center' as const,
        width: columnWidths.actions ?? modelColumnDefaults.actions,
        onHeaderCell: () => ({ resize: false }),
        render: (_value: unknown, entry: ModelCatalogEntry) => (
          <div className="model-row-actions">
            <Tooltip content="查看模型详情">
              <Button
                theme="borderless"
                icon={<IconEyeOpened />}
                aria-label="查看模型详情"
                onClick={() => setDetailEntry(entry)}
              />
            </Tooltip>
            <Tooltip content="编辑模型">
              <Button
                theme="borderless"
                icon={<IconEdit />}
                aria-label="编辑模型"
                onClick={() => openManual(entry)}
              />
            </Tooltip>
          </div>
        ),
      },
    ],
    [columnWidths, openManual],
  ).filter((column) => visibleColumns.includes(column.key as ModelColumnKey))

  return (
    <section className="models-page page-content">
      <div className="models-workspace">
        <section className="models-catalog-panel apple-card">
          <div className="models-panel-header">
            <div className="request-ledger-title">
              <h1>
                模型目录 <span>共 {total.toLocaleString('zh-CN')} 个模型</span>
              </h1>
              <p>跨渠道的模型资料、能力与费用参考</p>
            </div>
            <div className="request-ledger-actions models-header-actions">
              <Button theme="light" icon={<IconRefresh />} onClick={() => void loadCatalog()}>
                刷新
              </Button>
              <Popover
                trigger="click"
                position="bottomRight"
                contentClassName="request-columns-popover"
                content={
                  <div className="request-columns-menu">
                    <strong>显示列</strong>
                    {modelColumnOptions.map((item) => (
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
                theme="light"
                icon={<IconDelete />}
                disabled={selectedKeys.length === 0}
                onClick={deleteSelected}
              >
                删除{selectedKeys.length ? `（${selectedKeys.length}）` : ''}
              </Button>
              <Button theme="light" icon={<IconPlus />} onClick={() => openManual()}>
                新增模型
              </Button>
              <Button
                theme="solid"
                type="primary"
                onClick={() => {
                  setSourceURL(defaultModelSource)
                  setSourceModalOpen(true)
                }}
              >
                同步模型
              </Button>
            </div>
          </div>
          <div className="models-toolbar">
            <Input
              id="models-search"
              value={search}
              onChange={(value) => {
                setPage(1)
                setSearch(value)
              }}
              placeholder="搜索模型 ID 或显示名称"
              showClear
            />
            <Select
              value={protocol}
              onChange={(value) => {
                setPage(1)
                setProtocol(String(value ?? ''))
              }}
              placeholder="全部协议"
              showClear
            >
              <Select.Option value="openai_chat">OpenAI Chat</Select.Option>
              <Select.Option value="openai_responses">OpenAI Responses</Select.Option>
              <Select.Option value="anthropic_messages">Anthropic Messages</Select.Option>
              <Select.Option value="protocol_unconfirmed">协议未确认</Select.Option>
            </Select>
            <Select
              value={sourceStatus}
              onChange={(value) => {
                setPage(1)
                setSourceStatus(String(value ?? ''))
              }}
              placeholder="全部来源状态"
              showClear
            >
              <Select.Option value="valid">有效</Select.Option>
              <Select.Option value="retired">来源已移除</Select.Option>
              <Select.Option value="protocol_unconfirmed">协议未确认</Select.Option>
            </Select>
            <Select
              value={vendor}
              onChange={(value) => {
                setPage(1)
                setVendor(String(value ?? ''))
              }}
              placeholder="全部厂商"
              showClear
            >
              {facets.vendors.map((value) => (
                <Select.Option key={value} value={value}>
                  {value}
                </Select.Option>
              ))}
            </Select>
            <Select
              value={modelType}
              onChange={(value) => {
                setPage(1)
                setModelType(String(value ?? ''))
              }}
              placeholder="全部模型类型"
              showClear
            >
              {facets.modelTypes.map((value) => (
                <Select.Option key={value} value={value}>
                  {value}
                </Select.Option>
              ))}
            </Select>
            <Select
              value={capability}
              onChange={(value) => {
                setPage(1)
                setCapability(String(value ?? ''))
              }}
              placeholder="全部能力"
              showClear
            >
              {facets.capabilities.map((value) => (
                <Select.Option key={value} value={value}>
                  {value}
                </Select.Option>
              ))}
            </Select>
          </div>
          <div ref={tableWrapRef} className="models-table-wrap">
            <Table<ModelCatalogEntry>
              rowKey="stableKey"
              size="small"
              dataSource={items}
              columns={columns}
              rowSelection={rowSelection}
              pagination={false}
              resizable={{ onResizeStop: cacheModelColumnWidth }}
              scroll={{ x: 1540, y: tableScrollY }}
              empty={<span aria-hidden="true" />}
            />
            {(loading || loadError || items.length === 0) && (
              <div className="table-state-overlay">
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
                          onClick={() => void loadCatalog()}
                        >
                          重试
                        </Button>
                      </div>
                    }
                  />
                ) : (
                  <span>暂无模型资料</span>
                )}
              </div>
            )}
          </div>
          <div className="models-pagination">
            <span>共 {total} 个模型</span>
            <Pagination
              currentPage={page}
              pageSize={pageSize}
              total={total}
              showSizeChanger
              pageSizeOpts={[10, 20, 50, 100]}
              onChange={(nextPage, nextPageSize) => {
                setPage(nextPage)
                setPageSize(nextPageSize)
              }}
            />
          </div>
        </section>
      </div>
      <ModelSourceModal
        visible={sourceModalOpen}
        sourceURL={sourceURL}
        syncing={syncing}
        onSourceChange={setSourceURL}
        onRestoreDefault={() => setSourceURL(defaultModelSource)}
        onCancel={() => setSourceModalOpen(false)}
        onPreview={() => void previewSource()}
      />
      <ModelSyncPreviewModal
        preview={preview}
        visible={previewOpen}
        syncing={syncing}
        search={previewSearch}
        selection={previewSelection}
        onSearchChange={setPreviewSearch}
        onSelectionChange={setPreviewSelection}
        onCancel={() => setPreviewOpen(false)}
        onApply={() => void applyPreview()}
      />
      <ManualModelModal
        visible={manualOpen}
        editingKey={editingKey}
        entry={manualEntry}
        saving={saving}
        onEntryChange={setManualEntry}
        onCancel={() => setManualOpen(false)}
        onSubmit={saveManual}
      />
      <Modal
        title="模型详情"
        visible={detailEntry !== null}
        width={760}
        onCancel={() => setDetailEntry(null)}
        footer={
          <Button theme="light" onClick={() => setDetailEntry(null)}>
            关闭
          </Button>
        }
      >
        {detailEntry ? <ModelCatalogDetail entry={detailEntry} /> : null}
      </Modal>
      <Modal
        title="删除模型"
        visible={deleteConfirmOpen}
        okText="删除"
        cancelText="取消"
        confirmLoading={deleting}
        okButtonProps={{ type: 'danger' }}
        onCancel={() => setDeleteConfirmOpen(false)}
        onOk={() => void confirmDeleteSelected()}
      >
        确认删除已勾选的 {selectedKeys.length} 条模型吗？此操作不可撤销。
      </Modal>
    </section>
  )
}

export default ModelsPage
