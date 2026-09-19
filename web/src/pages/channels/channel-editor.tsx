import { useCallback, useEffect, useLayoutEffect, useRef, useState, type FormEvent } from 'react'
import { Button } from '@douyinfe/semi-ui-19'
import { IconArrowLeft } from '@douyinfe/semi-icons'
import type { AppTheme, ThemePreference } from '../../components/PageHeader'
import { adminError, adminFetch, getAdminToken, unwrapChannelCredentialSecret } from '../../api'
import type { Channel } from './channels'
import { showErrorToast, showSuccessToast, showWarningToast } from '../../notifications'
import {
  AdvancedSettingsSection,
  BasicInfoSection,
  EditorActionBar,
  HeadersSection,
  ModelsSection,
  ProbeSection,
  TrafficPolicySection,
  type ChannelMapping,
  type Credential,
  type ProbePolicy,
} from './channel-editor-sections'

type Detail = {
  channel: Channel
  models: string[]
  modelMappings: ChannelMapping[]
  probePolicy: ProbePolicy
}

type Props = {
  channelId?: string
  active: boolean
  theme: AppTheme
  themePreference: ThemePreference
  onChangeTheme: () => void
}

type EditorRequestToken = {
  session: number
  revision: number
}

type ProbeOutcome = {
  status?: string
  latencyMs?: number
  errorClass?: string
  errorMessage?: string
}

const defaultCredential: Credential = {
  type: 'bearer',
  secret: '',
  headerName: 'Authorization',
  prefix: 'Bearer ',
}

// 创建渠道默认值，确保新增页不会共享可变状态。
function createDefaultChannel(): Channel {
  return {
    id: '',
    name: '',
    note: '',
    group: '',
    protocol: 'openai_responses',
    baseUrl: '',
    capabilities: [],
    adminState: 'enabled',
    healthState: 'healthy',
    credentialConfigured: false,
    failureAction: 'cooldown',
    failureThreshold: 3,
    customHeaders: {},
    fallbackModel: '',
    reasoningEffort: 'passthrough',
    serviceTierPassthrough: true,
    concurrencyLimit: 8,
    requestTimeoutMs: 120000,
    streamIdleTimeoutMs: 300000,
    cooldownSeconds: 30,
    priority: 100,
  }
}

// 创建渠道探针默认策略。
function createDefaultProbe(channelId = ''): ProbePolicy {
  return {
    channelId,
    enabled: false,
    mode: 'connectivity',
    intervalSeconds: 60,
    model: '',
    path: '',
    failureThreshold: 3,
    requestTimeoutMs: 15000,
    autoRecover: false,
    recoverySuccessThreshold: 1,
  }
}

// 返回渠道列表页。
function goBack(): void {
  window.location.hash = '#channels'
}

// 按测试/探针结果选择 Toast，HTTP 200 且非 success 时不显示成功提示。
function notifyProbeOutcome(actionLabel: string, result: ProbeOutcome): void {
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

// 解析并校验 JSON 请求头对象。
function parseHeaders(value: string): Record<string, string> | null {
  try {
    const parsed: unknown = JSON.parse(value)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) return null
    const headers: Record<string, string> = {}
    for (const [name, item] of Object.entries(parsed)) {
      if (typeof item !== 'string' || !name.trim()) return null
      headers[name.trim()] = item
    }
    return headers
  } catch {
    return null
  }
}

// 渲染完整的渠道新增/编辑页面。
function ChannelEditorPage({ channelId, active }: Props) {
  const [form, setForm] = useState<Channel>(createDefaultChannel)
  const [credential, setCredential] = useState<Credential>(defaultCredential)
  const [credentialDirty, setCredentialDirty] = useState(false)
  const [headersText, setHeadersText] = useState('{}')
  const [mappings, setMappings] = useState<ChannelMapping[]>([])
  const [probe, setProbe] = useState<ProbePolicy>(createDefaultProbe)
  const [models, setModels] = useState<string[]>([])
  const [testModel, setTestModel] = useState('')
  const [modelInput, setModelInput] = useState('')
  const [modelSuggestions, setModelSuggestions] = useState<
    Array<{ modelId: string; displayName?: string }>
  >([])
  const [persistedId, setPersistedId] = useState(channelId ?? '')
  const [loading, setLoading] = useState(Boolean(channelId))
  const [loadFailed, setLoadFailed] = useState(false)
  const [saving, setSaving] = useState(false)
  const [copying, setCopying] = useState(false)
  const [probing, setProbing] = useState(false)
  const [resetting, setResetting] = useState(false)
  const [discovering, setDiscovering] = useState(false)
  const [testing, setTesting] = useState(false)
  const editorSessionRef = useRef(0)
  const editorRevisionRef = useRef(0)
  const savingLockRef = useRef(false)
  const persistedIdRef = useRef(channelId ?? '')
  const activeId = channelId || persistedId
  const editing = Boolean(activeId)

  // 记录一次用户输入变化，使在途异步结果不能覆盖更新后的表单。
  function markEditorChanged(): void {
    editorRevisionRef.current += 1
  }

  // 捕获请求发起时的编辑会话和输入版本。
  function captureEditorRequest(): EditorRequestToken {
    return { session: editorSessionRef.current, revision: editorRevisionRef.current }
  }

  // 判断异步结果是否仍属于当前编辑会话和当前输入。
  function isEditorRequestCurrent(request: EditorRequestToken): boolean {
    return (
      editorSessionRef.current === request.session && editorRevisionRef.current === request.revision
    )
  }

  // 判断异步操作是否仍属于当前页面会话，用于安全结束加载状态。
  function isEditorSessionCurrent(request: EditorRequestToken): boolean {
    return editorSessionRef.current === request.session
  }

  // 立即使当前会话失效后返回列表，阻止延迟导航重新打开旧渠道。
  function leaveEditor(): void {
    editorSessionRef.current += 1
    goBack()
  }

  // 按当前协议和输入内容查询模型目录联想，目录只辅助填写，不限制实际代理请求。
  useEffect(() => {
    const search = modelInput.trim()
    if (!search) {
      setModelSuggestions([])
      return
    }
    let cancelled = false
    const timer = window.setTimeout(() => {
      void (async () => {
        try {
          const params = new URLSearchParams({
            protocol: form.protocol,
            search,
            page: '1',
            pageSize: '8',
          })
          const response = await adminFetch(`/api/admin/v1/models/catalog?${params.toString()}`)
          if (!response.ok) return
          const result = (await response.json()) as {
            items?: Array<{ modelId: string; displayName?: string }>
          }
          if (!cancelled) setModelSuggestions((result.items ?? []).slice(0, 8))
        } catch {
          if (!cancelled) setModelSuggestions([])
        }
      })()
    }, 180)
    return () => {
      cancelled = true
      window.clearTimeout(timer)
    }
  }, [form.protocol, modelInput])

  // 修改渠道字段并保持表单状态不可变。
  const updateChannel = useCallback(
    <K extends keyof Channel>(field: K, value: Channel[K]): void => {
      editorRevisionRef.current += 1
      setForm((current) => ({ ...current, [field]: value }))
      if (field !== 'protocol') return
      const protocol = String(value)
      const usesOpenAIDefault =
        credential.type === 'bearer' &&
        credential.headerName === 'Authorization' &&
        credential.prefix === 'Bearer '
      const usesAnthropicDefault =
        credential.type === 'api_key' &&
        credential.headerName.toLowerCase() === 'x-api-key' &&
        credential.prefix === ''
      if (protocol === 'anthropic_messages' && usesOpenAIDefault) {
        setCredential((current) => ({
          ...current,
          type: 'api_key',
          headerName: 'x-api-key',
          prefix: '',
        }))
        setCredentialDirty(true)
      } else if (protocol !== 'anthropic_messages' && usesAnthropicDefault) {
        setCredential((current) => ({
          ...current,
          type: 'bearer',
          headerName: 'Authorization',
          prefix: 'Bearer ',
        }))
        setCredentialDirty(true)
      }
    },
    [credential.headerName, credential.prefix, credential.type],
  )

  // 修改凭证字段并标记秘密需要重新保存。
  const updateCredential = useCallback((field: keyof Credential, value: string): void => {
    editorRevisionRef.current += 1
    setCredential((current) => {
      const next = { ...current, [field]: value }
      if (field === 'type') {
        next.headerName =
          value === 'bearer'
            ? 'Authorization'
            : value === 'api_key'
              ? 'x-api-key'
              : current.headerName
        next.prefix = value === 'bearer' ? 'Bearer ' : value === 'api_key' ? '' : current.prefix
      }
      return next
    })
    setCredentialDirty(true)
  }, [])

  // 修改探针策略的单个字段。
  function updateProbe(field: keyof ProbePolicy, value: string | number | boolean): void {
    markEditorChanged()
    setProbe((current) => {
      const next = { ...current, [field]: value }
      if (field === 'enabled' && value === false) next.autoRecover = false
      if (field === 'autoRecover' && value === true) next.enabled = true
      return next
    })
  }

  // 更新自定义请求头文本并使旧异步结果失效。
  function updateHeadersText(value: string): void {
    markEditorChanged()
    setHeadersText(value)
  }

  // 更新模型输入并使旧异步结果失效。
  function updateModelInput(value: string): void {
    markEditorChanged()
    setModelInput(value)
  }

  // 更新当前测试模型并使旧异步结果失效。
  function updateTestModel(value: string): void {
    markEditorChanged()
    setTestModel(value)
  }

  // 页面重新激活时重置新增表单，或刷新编辑页聚合详情和可回显凭证。
  useLayoutEffect(() => {
    if (!active) return
    const session = ++editorSessionRef.current
    editorRevisionRef.current = 0
    let cancelled = false
    const cleanup = (): void => {
      cancelled = true
      if (editorSessionRef.current === session) editorSessionRef.current += 1
    }
    if (!channelId) {
      setForm(createDefaultChannel())
      setCredential({ ...defaultCredential })
      setCredentialDirty(false)
      setHeadersText('{}')
      setMappings([])
      setProbe(createDefaultProbe())
      setModels([])
      setTestModel('')
      setModelInput('')
      setModelSuggestions([])
      setPersistedId('')
      persistedIdRef.current = ''
      setLoading(false)
      setLoadFailed(false)
      setSaving(false)
      setCopying(false)
      setProbing(false)
      setResetting(false)
      setDiscovering(false)
      setTesting(false)
      return cleanup
    }
    const loadDetail = async (): Promise<void> => {
      setLoading(true)
      setLoadFailed(false)
      try {
        const response = await adminFetch(`/api/admin/v1/channels/${encodeURIComponent(channelId)}`)
        if (!response.ok) throw await adminError(response, '读取渠道详情失败')
        const detail = (await response.json()) as Detail
        if (cancelled) return
        setForm({ ...createDefaultChannel(), ...detail.channel })
        setHeadersText(JSON.stringify(detail.channel.customHeaders ?? {}, null, 2))
        setMappings(detail.modelMappings ?? [])
        setModels(detail.models ?? [])
        setProbe({ ...createDefaultProbe(channelId), ...detail.probePolicy })
        if (detail.channel.credentialConfigured) {
          const credentialResponse = await adminFetch(
            `/api/admin/v1/channels/${encodeURIComponent(channelId)}/credential`,
          )
          if (!cancelled && credentialResponse.ok) {
            const payload = (await credentialResponse.json()) as {
              type?: string
              headerName?: string
              prefix?: string
              secret?: string
              ciphertext?: string
              nonce?: string
            }
            const next: Credential = {
              type: payload.type || defaultCredential.type,
              headerName: payload.headerName || defaultCredential.headerName,
              prefix: payload.prefix ?? defaultCredential.prefix,
              secret: '',
            }
            if (payload.ciphertext && payload.nonce) {
              try {
                next.secret = await unwrapChannelCredentialSecret(
                  payload.ciphertext,
                  payload.nonce,
                  getAdminToken(),
                )
              } catch {
                if (!cancelled) showErrorToast('凭证无法解密，请重新填写')
              }
            } else if (typeof payload.secret === 'string' && payload.secret) {
              if (!cancelled) showErrorToast('凭证无法解密，请重新填写')
            }
            if (!cancelled) {
              setCredential(next)
              setCredentialDirty(false)
            }
          } else if (!cancelled) throw await adminError(credentialResponse, '读取渠道凭证失败')
        }
        if (!cancelled)
          setTestModel(
            detail.models?.[0] ||
              detail.modelMappings?.[0]?.upstreamModel ||
              detail.channel.fallbackModel ||
              '',
          )
      } catch (reason) {
        if (!cancelled) {
          setLoadFailed(true)
          showErrorToast(reason instanceof Error ? reason.message : '读取渠道详情失败')
        }
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void loadDetail()
    return cleanup
  }, [active, channelId])

  // 将手动输入的模型加入独立模型目录。
  function addModel(): void {
    const model = modelInput.trim()
    if (!model) return
    if (models.includes(model)) {
      showWarningToast('该模型已经存在。')
      return
    }
    markEditorChanged()
    setModels((current) => [...current, model])
    setTestModel((current) => current || model)
    setModelInput('')
  }

  // 从模型目录删除模型，不改动用户手工配置的映射。
  function removeModel(model: string): void {
    markEditorChanged()
    const remaining = models.filter((item) => item !== model)
    setModels(remaining)
    if (testModel === model) setTestModel(remaining[0] || '')
    if (form.fallbackModel === model) updateChannel('fallbackModel', '')
  }

  // 一次清空模型目录和当前测试模型，不改动独立的兜底模型配置。
  function clearModels(): void {
    markEditorChanged()
    setModels([])
    setTestModel('')
  }

  // 复制模型名称到系统剪贴板。
  async function copyModel(model: string): Promise<void> {
    try {
      await window.navigator.clipboard.writeText(model)
      showSuccessToast('模型名称已复制。')
    } catch {
      showErrorToast('复制模型名称失败。')
    }
  }

  // 添加一条空模型映射。
  function addMapping(): void {
    markEditorChanged()
    setMappings((current) => [
      ...current,
      { channelId: activeId, protocol: form.protocol, logicalModel: '', upstreamModel: '' },
    ])
  }

  // 删除一条模型映射。
  function removeMapping(index: number): void {
    markEditorChanged()
    setMappings((current) => current.filter((_, itemIndex) => itemIndex !== index))
  }

  // 更新模型映射字段。
  function updateMapping(
    index: number,
    field: 'logicalModel' | 'upstreamModel',
    value: string,
  ): void {
    markEditorChanged()
    setMappings((current) =>
      current.map((item, itemIndex) => (itemIndex === index ? { ...item, [field]: value } : item)),
    )
  }

  // 组装未保存渠道的模型发现请求。
  function buildDiscoveryPayload(): Record<string, unknown> {
    return {
      channel: {
        name: form.name.trim(),
        note: (form.note ?? '').trim(),
        protocol: form.protocol,
        baseUrl: form.baseUrl.trim(),
        customHeaders: parseHeaders(headersText) ?? {},
        requestTimeoutMs: form.requestTimeoutMs,
      },
      credential,
    }
  }

  // 校验保存前的必填项和 JSON 请求头。
  function validateForm(): boolean {
    if (!form.name.trim()) {
      showWarningToast('请填写渠道名称。')
      return false
    }
    if (!form.protocol) {
      showWarningToast('请选择协议。')
      return false
    }
    if (!form.baseUrl.trim()) {
      showWarningToast('请填写上游 Base URL。')
      return false
    }
    if (!editing && !credential.secret.trim()) {
      showWarningToast('创建渠道时必须填写访问凭证。')
      return false
    }
    if (form.adminState === 'enabled' && !form.credentialConfigured && !credential.secret.trim()) {
      showWarningToast('启用渠道前必须配置访问凭证。')
      return false
    }
    if (credential.type === 'custom' && !credential.headerName.trim()) {
      showWarningToast('自定义凭证必须填写认证 Header。')
      return false
    }
    if (
      mappings.some(
        (mapping) => Boolean(mapping.logicalModel.trim()) !== Boolean(mapping.upstreamModel.trim()),
      )
    ) {
      showWarningToast('模型映射必须同时填写逻辑模型和上游实际模型。')
      return false
    }
    if (!parseHeaders(headersText)) {
      showWarningToast('自定义请求头必须是值为字符串的 JSON 对象。')
      return false
    }
    const numericFields = [
      form.priority,
      form.concurrencyLimit,
      form.requestTimeoutMs,
      form.streamIdleTimeoutMs,
      form.failureThreshold,
      form.cooldownSeconds,
      probe.intervalSeconds,
      probe.failureThreshold,
      probe.requestTimeoutMs,
      probe.recoverySuccessThreshold,
    ]
    if (numericFields.some((value) => !Number.isFinite(value))) {
      showWarningToast('请填写有效的数字配置。')
      return false
    }
    if (probe.enabled && probe.mode === 'minimal_inference' && !probe.model.trim()) {
      const inferred =
        models[0] ||
        mappings.find((item) => item.upstreamModel.trim())?.upstreamModel ||
        form.fallbackModel
      if (!String(inferred ?? '').trim()) {
        showWarningToast('最小推理探针需要选择自动探针模型，或先配置渠道模型、映射或兜底模型。')
        return false
      }
    }
    return true
  }

  // 从上游发现模型并合并到独立模型目录。
  async function discoverModels(): Promise<void> {
    if (!form.baseUrl.trim()) {
      showWarningToast('请先填写上游 Base URL。')
      return
    }
    if (!credential.secret.trim()) {
      showWarningToast('请先填写访问凭证，再从上游获取模型。')
      return
    }
    if (!parseHeaders(headersText)) {
      showWarningToast('自定义请求头必须是值为字符串的 JSON 对象。')
      return
    }
    const request = captureEditorRequest()
    setDiscovering(true)
    try {
      const response = await adminFetch('/api/admin/v1/channels/models', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(buildDiscoveryPayload()),
      })
      if (!response.ok) throw await adminError(response, '获取上游模型失败')
      const result = (await response.json()) as { models?: string[] }
      if (!isEditorRequestCurrent(request)) return
      const discoveredModels = result.models ?? []
      setModels((current) => Array.from(new Set([...current, ...discoveredModels])))
      setTestModel((current) => current || discoveredModels[0] || '')
      showSuccessToast(`已获取 ${discoveredModels.length} 个模型。`)
    } catch (reason) {
      if (isEditorRequestCurrent(request))
        showErrorToast(reason instanceof Error ? reason.message : '获取上游模型失败')
    } finally {
      if (isEditorSessionCurrent(request)) setDiscovering(false)
    }
  }

  // 复制当前渠道及其完整聚合配置。
  async function copyChannel(): Promise<void> {
    if (!activeId) return
    const request = captureEditorRequest()
    setCopying(true)
    try {
      const response = await adminFetch(
        `/api/admin/v1/channels/${encodeURIComponent(activeId)}/copy`,
        { method: 'POST' },
      )
      if (!response.ok) throw await adminError(response, '复制渠道失败')
      const copied = (await response.json()) as Channel
      if (!isEditorSessionCurrent(request)) return
      showSuccessToast(`已复制为“${copied.name}”。`)
      window.location.hash = `#channels/${encodeURIComponent(copied.id)}/edit`
    } catch (reason) {
      if (isEditorSessionCurrent(request))
        showErrorToast(reason instanceof Error ? reason.message : '复制渠道失败')
    } finally {
      if (isEditorSessionCurrent(request)) setCopying(false)
    }
  }

  // 执行手动探针并反馈上游结果。
  async function runProbe(): Promise<void> {
    if (!activeId) {
      showWarningToast('请先保存渠道，再执行手动探针。')
      return
    }
    setProbing(true)
    try {
      const response = await adminFetch(
        `/api/admin/v1/channels/${encodeURIComponent(activeId)}/probe`,
        { method: 'POST' },
      )
      if (!response.ok) throw await adminError(response, '探针执行失败')
      const result = (await response.json()) as ProbeOutcome
      notifyProbeOutcome('探针', result)
    } catch (reason) {
      showErrorToast(reason instanceof Error ? reason.message : '探针执行失败')
    } finally {
      setProbing(false)
    }
  }

  // 经二次确认后人工恢复渠道健康状态，不改变人工启停状态。
  async function resetHealth(): Promise<void> {
    if (!activeId) return
    if (!window.confirm('人工恢复会清除失败计数和冷却状态，但不会启用人工禁用渠道，是否继续？')) {
      return
    }
    setResetting(true)
    try {
      const response = await adminFetch(
        `/api/admin/v1/channels/${encodeURIComponent(activeId)}/health`,
        { method: 'POST' },
      )
      if (!response.ok) throw await adminError(response, '人工恢复失败')
      showSuccessToast('渠道健康状态已人工恢复。')
    } catch (reason) {
      showErrorToast(reason instanceof Error ? reason.message : '人工恢复失败')
    } finally {
      setResetting(false)
    }
  }

  // 组装当前编辑值的即时测试请求。
  function buildTestPayload(): Record<string, unknown> {
    return {
      channel: {
        protocol: form.protocol,
        baseUrl: form.baseUrl.trim(),
        customHeaders: parseHeaders(headersText) ?? {},
        requestTimeoutMs: form.requestTimeoutMs,
      },
      credential,
      model: testModel.trim(),
    }
  }

  // 使用当前协议、地址、凭证和测试模型即时请求上游。
  async function testCurrentChannel(): Promise<void> {
    if (!form.protocol) {
      showWarningToast('请先选择协议。')
      return
    }
    if (!form.baseUrl.trim()) {
      showWarningToast('请先填写上游 Base URL。')
      return
    }
    if (!credential.secret.trim()) {
      showWarningToast('请先填写访问凭证。')
      return
    }
    if (!testModel.trim()) {
      showWarningToast('请先选择测试模型。')
      return
    }
    if (!parseHeaders(headersText)) {
      showWarningToast('自定义请求头必须是值为字符串的 JSON 对象。')
      return
    }
    setTesting(true)
    try {
      const response = await adminFetch('/api/admin/v1/channels/test', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(buildTestPayload()),
      })
      if (!response.ok) throw await adminError(response, '渠道测试失败')
      const result = (await response.json()) as ProbeOutcome
      notifyProbeOutcome('渠道测试', result)
    } catch (reason) {
      showErrorToast(reason instanceof Error ? reason.message : '渠道测试失败')
    } finally {
      setTesting(false)
    }
  }

  // 保存渠道、模型目录、模型映射和探针策略。
  async function persist(request: EditorRequestToken): Promise<Channel | null> {
    const targetId = channelId || persistedIdRef.current
    const creating = !targetId
    const cleanedModels = Array.from(new Set(models.map((model) => model.trim()).filter(Boolean)))
    const cleanedMappings = mappings
      .filter((mapping) => mapping.logicalModel.trim() && mapping.upstreamModel.trim())
      .map((mapping) => ({
        ...mapping,
        logicalModel: mapping.logicalModel.trim(),
        upstreamModel: mapping.upstreamModel.trim(),
        channelId: creating ? '' : targetId,
        protocol: form.protocol,
      }))
    const payload: Record<string, unknown> = {
      channel: {
        id: creating ? '' : form.id || targetId,
        name: form.name.trim(),
        note: (form.note ?? '').trim(),
        group: (form.group ?? '').trim(),
        protocol: form.protocol,
        baseUrl: form.baseUrl.trim(),
        capabilities: form.capabilities ?? [],
        adminState: form.adminState,
        failureAction: form.failureAction,
        failureThreshold: form.failureThreshold,
        customHeaders: parseHeaders(headersText) ?? {},
        fallbackModel: form.fallbackModel ?? '',
        reasoningEffort: form.reasoningEffort ?? 'passthrough',
        serviceTierPassthrough: form.serviceTierPassthrough,
        concurrencyLimit: form.concurrencyLimit,
        requestTimeoutMs: form.requestTimeoutMs,
        streamIdleTimeoutMs: form.streamIdleTimeoutMs,
        cooldownSeconds: form.cooldownSeconds,
        priority: form.priority,
        ...(credentialDirty || creating ? { credential } : {}),
      },
      models: cleanedModels,
      modelMappings: cleanedMappings,
      probePolicy: {
        channelId: creating ? '' : targetId,
        enabled: probe.enabled,
        mode: probe.mode,
        intervalSeconds: probe.intervalSeconds,
        model: probe.model,
        path: probe.path,
        failureThreshold: probe.failureThreshold,
        requestTimeoutMs: probe.requestTimeoutMs,
        autoRecover: probe.autoRecover,
        recoverySuccessThreshold: probe.recoverySuccessThreshold,
      },
    }
    const response = await adminFetch(
      creating
        ? '/api/admin/v1/channels/bundle'
        : `/api/admin/v1/channels/${encodeURIComponent(targetId)}/bundle`,
      {
        method: creating ? 'POST' : 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      },
    )
    if (!response.ok) throw await adminError(response, '保存渠道配置失败')
    const saved = (await response.json()) as Channel
    if (!isEditorSessionCurrent(request)) return null
    if (creating) {
      persistedIdRef.current = saved.id
      setPersistedId(saved.id)
      setForm((current) => ({
        ...current,
        id: saved.id,
        credentialConfigured: saved.credentialConfigured,
      }))
    }
    if (!isEditorRequestCurrent(request)) return null
    setForm((current) => ({ ...current, ...saved }))
    setPersistedId(saved.id)
    setModels(cleanedModels)
    setMappings(cleanedMappings.map((mapping) => ({ ...mapping, channelId: saved.id })))
    setProbe((current) => ({ ...current, channelId: saved.id }))
    setCredentialDirty(false)
    return saved
  }

  // 响应表单提交并通过 Toast 显示结果。
  async function save(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    if (savingLockRef.current) return
    if (!validateForm()) return
    const request = captureEditorRequest()
    savingLockRef.current = true
    setSaving(true)
    const wasCreate = !channelId && !persistedIdRef.current
    try {
      const saved = await persist(request)
      if (!saved) return
      if (isEditorRequestCurrent(request)) {
        showSuccessToast(wasCreate ? '渠道已创建，配置已保存。' : '渠道配置已保存。')
      }
      if (wasCreate)
        window.setTimeout(() => {
          if (isEditorRequestCurrent(request) && persistedIdRef.current)
            window.location.hash = `#channels/${encodeURIComponent(persistedIdRef.current)}/edit`
        }, 300)
    } catch (reason) {
      if (isEditorRequestCurrent(request))
        showErrorToast(reason instanceof Error ? reason.message : '保存渠道配置失败')
    } finally {
      if (isEditorSessionCurrent(request)) {
        savingLockRef.current = false
        setSaving(false)
      }
    }
  }

  // 复制当前编辑会话中的渠道密钥。
  async function copySecret(): Promise<void> {
    if (!credential.secret) {
      showWarningToast('当前渠道没有可复制的密钥。')
      return
    }
    try {
      await window.navigator.clipboard.writeText(credential.secret)
      showSuccessToast('密钥已复制到剪贴板。')
    } catch {
      showErrorToast('复制密钥失败，请手动复制。')
    }
  }

  const canDiscover = Boolean(form.baseUrl.trim() && credential.secret.trim())
  const canTest = Boolean(
    form.protocol && form.baseUrl.trim() && credential.secret.trim() && testModel.trim(),
  )
  const canProbe = Boolean(activeId)
  if (loading)
    return (
      <section className="channel-editor-shell page-content">
        <div className="loading-state">正在读取渠道配置…</div>
      </section>
    )
  if (loadFailed)
    return (
      <section className="channel-editor-shell page-content">
        <div className="loading-state">
          渠道配置读取失败，请返回渠道列表后重试。
          <Button theme="light" onClick={leaveEditor}>
            返回渠道
          </Button>
        </div>
      </section>
    )
  return (
    <section className="channel-editor-shell page-content">
      <section className="channel-editor-page" aria-label={editing ? '编辑渠道' : '新增渠道'}>
        <div className="editor-page-heading">
          <Button theme="borderless" icon={<IconArrowLeft />} onClick={leaveEditor}>
            返回渠道
          </Button>
          <span className="editor-breadcrumb">
            渠道 <span>/</span> {editing ? form.name || '编辑渠道' : '新增渠道'}
          </span>
        </div>
        <form className="channel-editor-stack" onSubmit={(event) => void save(event)}>
          <BasicInfoSection
            form={form}
            credential={credential}
            configured={form.credentialConfigured}
            update={updateChannel}
            updateCredential={updateCredential}
            onCopySecret={() => void copySecret()}
          />
          <HeadersSection
            credential={credential}
            headersText={headersText}
            onCredentialChange={updateCredential}
            onHeadersChange={updateHeadersText}
          />
          <ModelsSection
            mappings={mappings}
            models={models}
            testModel={testModel}
            fallbackModel={form.fallbackModel ?? ''}
            discovering={discovering}
            canDiscover={canDiscover}
            canTest={canTest}
            testing={testing}
            modelInput={modelInput}
            modelSuggestions={modelSuggestions}
            onDiscover={() => void discoverModels()}
            onClearModels={clearModels}
            onModelInput={updateModelInput}
            onAddModel={addModel}
            onRemoveModel={removeModel}
            onCopyModel={(model) => void copyModel(model)}
            onTestModel={updateTestModel}
            onTest={() => void testCurrentChannel()}
            onFallbackModel={(value) => updateChannel('fallbackModel', value)}
            onAddMapping={addMapping}
            onRemoveMapping={removeMapping}
            onChangeMapping={updateMapping}
          />
          <TrafficPolicySection form={form} update={updateChannel} />
          <ProbeSection
            probe={probe}
            failureAction={form.failureAction}
            modelOptions={[
              ...models,
              ...mappings.map((item) => item.upstreamModel.trim()),
              form.fallbackModel ?? '',
            ]}
            inferredModel={
              models[0] ||
              mappings.find((item) => item.upstreamModel.trim())?.upstreamModel ||
              form.fallbackModel ||
              ''
            }
            inferredSource={
              models[0]
                ? '渠道模型目录第一项'
                : mappings.find((item) => item.upstreamModel.trim())
                  ? '渠道映射模型'
                  : form.fallbackModel
                    ? '兜底模型'
                    : '无可推导模型'
            }
            update={updateProbe}
            onProbe={() => void runProbe()}
            onReset={() => void resetHealth()}
            probing={probing}
            resetting={resetting}
            canProbe={canProbe}
          />
          <AdvancedSettingsSection form={form} update={updateChannel} />
          <EditorActionBar
            editing={editing}
            saving={saving}
            copying={copying}
            onCancel={leaveEditor}
            onCopy={() => void copyChannel()}
          />
        </form>
      </section>
    </section>
  )
}

export default ChannelEditorPage
