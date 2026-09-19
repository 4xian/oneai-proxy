export type Pricing = {
  input?: number | null
  output?: number | null
  cacheRead?: number | null
  cacheWrite?: number | null
}

export type ModelCatalogEntry = {
  stableKey: string
  baseModelId?: string
  modelId: string
  displayName?: string
  vendor?: string
  provider?: string
  protocol: string
  modelType?: string
  description?: string
  modalities: { input: string[]; output: string[] }
  capabilities: string[]
  contextWindow?: number
  maxInputTokens?: number
  maxOutputTokens?: number
  pricing: Pricing
  currency?: string
  billingUnit?: string
  sourceType: string
  sourceUrl?: string
  sourceAdapter?: string
  sourceVersion?: string
  sourceStatus: string
  syncedAt?: string
  publishedAt?: string
  createdAt?: string
  updatedAt?: string
}

export type ModelCatalogGroup = {
  groupKey: string
  baseModelId: string
  displayName: string
  vendors: string[]
  modelTypes: string[]
  capabilities: string[]
  contextWindow: number
  maxOutputTokens: number
  providerCount: number
  sourceStatuses: string[]
  updatedAt?: string
  versions: ModelCatalogEntry[]
}

export type PreviewItem = {
  changeType: string
  entry: ModelCatalogEntry
  existing?: ModelCatalogEntry
}

export type Preview = {
  sourceUrl: string
  sourceDigest: string
  adapter: string
  items: PreviewItem[]
  parsed: number
  skipped: number
  errors: string[]
}

export type CatalogFacets = {
  vendors: string[]
  modelTypes: string[]
  capabilities: string[]
}

export const defaultModelSource = 'https://models.dev/catalog.json'

export const emptyModelEntry: ModelCatalogEntry = {
  stableKey: '',
  modelId: '',
  displayName: '',
  baseModelId: '',
  vendor: '',
  provider: '',
  protocol: 'openai_responses',
  modelType: 'text_generation',
  description: '',
  modalities: { input: ['text'], output: ['text'] },
  capabilities: [],
  contextWindow: 0,
  maxInputTokens: 0,
  maxOutputTokens: 0,
  pricing: {},
  currency: 'USD',
  billingUnit: 'per_1m_tokens',
  sourceType: 'manual',
  sourceStatus: 'valid',
}

// protocolText 返回模型协议的中文展示名称。
export function protocolText(value: string): string {
  return (
    (
      {
        openai_chat: 'OpenAI Chat',
        openai_responses: 'OpenAI Responses',
        anthropic_messages: 'Anthropic Messages',
        protocol_unconfirmed: '待确认',
      } as Record<string, string>
    )[value] ?? value
  )
}

// statusText 返回模型来源状态的中文展示名称。
export function statusText(value: string): string {
  return (
    (
      { valid: '有效', retired: '来源已移除', protocol_unconfirmed: '协议未确认' } as Record<
        string,
        string
      >
    )[value] ?? value
  )
}

// changeText 返回同步差异类型的中文展示名称。
export function changeText(value: string): string {
  return (
    (
      {
        new: '新增',
        changed: '变更',
        unchanged: '无变化',
        conflict: '冲突',
        retired: '来源已移除',
        protocol_unconfirmed: '协议未确认',
      } as Record<string, string>
    )[value] ?? value
  )
}

// priceText 保留费用原始数值并补充币种。
export function priceText(value: number | null | undefined, currency = 'USD'): string {
  return value === null || value === undefined ? '—' : `${currency} ${value}`
}

// priceRangeText 按币种和计费单位显示一组供应商版本的费用区间。
export function priceRangeText(versions: ModelCatalogEntry[], field: keyof Pricing): string {
  const groups = new Map<string, number[]>()
  versions.forEach((entry) => {
    const value = entry.pricing[field]
    if (value === null || value === undefined) return
    const label = [entry.currency || 'USD', entry.billingUnit].filter(Boolean).join(' ')
    groups.set(label, [...(groups.get(label) ?? []), value])
  })
  if (groups.size === 0) return '—'
  return [...groups.entries()]
    .map(([label, values]) => {
      const minimum = Math.min(...values)
      const maximum = Math.max(...values)
      return `${label} ${minimum === maximum ? minimum : `${minimum} - ${maximum}`}`
    })
    .join('；')
}

// formatCount 格式化模型上下文和 Token 数值，仅空值显示为 —。
export function formatCount(value: number | null | undefined): string {
  return value == null ? '—' : new Intl.NumberFormat('zh-CN').format(value)
}

// dateText 将 ISO 时间转换为本地中文时间。
export function dateText(value: string | undefined): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString('zh-CN', { hour12: false })
}

// listText 将模型枚举值合并为紧凑文本。
export function listText(values: string[] | undefined): string {
  return values?.length ? values.join('、') : '—'
}
