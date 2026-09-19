import { useLayoutEffect, useRef, useState } from 'react'
import {
  Button,
  Card,
  Checkbox,
  Collapse,
  Input,
  InputNumber,
  Select,
  Switch,
  Tag,
  TextArea,
  Tooltip,
} from '@douyinfe/semi-ui-19'
import { IconCopy, IconDelete, IconPlay, IconPlus, IconRefresh } from '@douyinfe/semi-icons'
import type { Channel } from './channels'

export type ChannelMapping = {
  channelId: string
  protocol: string
  logicalModel: string
  upstreamModel: string
}

export type ProbePolicy = {
  channelId: string
  enabled: boolean
  mode: string
  intervalSeconds: number
  model: string
  path: string
  failureThreshold: number
  requestTimeoutMs: number
  autoRecover: boolean
  recoverySuccessThreshold: number
}

export type Credential = { type: string; secret: string; headerName: string; prefix: string }

type ReasoningEffort = 'passthrough' | 'low' | 'medium' | 'high' | 'xhigh' | 'max'
type ChannelCapability = 'streaming' | 'tools' | 'vision' | 'audio' | 'reasoning'

type EditorCardProps = {
  title: string
  detail: string
  headerAction?: React.ReactNode
  children: React.ReactNode
}

// 将数字输入转为有限值；清空或非法时回退上次合法值，再回退字段默认值。
function parseFiniteNumber(value: unknown, current: number, fallback: number): number {
  if (value === null || value === undefined || value === '') {
    return Number.isFinite(current) ? current : fallback
  }
  const parsed = typeof value === 'number' ? value : Number(value)
  if (Number.isFinite(parsed)) return parsed
  return Number.isFinite(current) ? current : fallback
}

const channelCapabilityOptions: Array<{ value: ChannelCapability; label: string }> = [
  { value: 'streaming', label: '流式' },
  { value: 'tools', label: '工具调用' },
  { value: 'vision', label: '视觉' },
  { value: 'audio', label: '音频' },
  { value: 'reasoning', label: '推理' },
]

// 渲染编辑页统一的整行玻璃卡片。
function EditorCard({ title, detail, headerAction, children }: EditorCardProps) {
  return (
    <Card
      className="editor-card apple-card"
      title={
        <span className="editor-card-title-line">
          <span>{title}</span>
          <span className="editor-card-meta">{detail}</span>
        </span>
      }
      headerExtraContent={
        headerAction ? <div className="editor-card-header-action">{headerAction}</div> : undefined
      }
      bordered={false}
    >
      {children}
    </Card>
  )
}

// 渲染必填项标记。
function RequiredMark(): React.ReactNode {
  return (
    <em className="required-mark" aria-hidden="true">
      *
    </em>
  )
}

type BasicInfoProps = {
  form: Channel
  credential: Credential
  configured: boolean
  update: <K extends keyof Channel>(field: K, value: Channel[K]) => void
  updateCredential: (field: keyof Credential, value: string) => void
  onCopySecret: () => void
}

// 渲染渠道名称、协议、Base URL、访问凭证和备注。
export function BasicInfoSection({
  form,
  credential,
  configured,
  update,
  updateCredential,
  onCopySecret,
}: BasicInfoProps) {
  return (
    <EditorCard
      title="基本信息"
      detail="渠道身份与连接地址"
      headerAction={
        <div className="editor-switch-row editor-header-switch">
          <Switch
            checked={form.adminState === 'enabled'}
            onChange={(checked) => update('adminState', checked ? 'enabled' : 'disabled')}
          />
          <span>{form.adminState === 'enabled' ? '启用渠道' : '人工禁用'}</span>
        </div>
      }
    >
      <div className="editor-form-grid basic-info-grid">
        <label className="field field-name">
          <span>
            渠道名称 <RequiredMark />
          </span>
          <Input
            value={form.name}
            onChange={(value) => update('name', value)}
            placeholder="例如：OpenAI 主渠道"
          />
        </label>
        <label className="field field-protocol">
          <span>
            协议 <RequiredMark />
          </span>
          <Select
            className="apple-select"
            value={form.protocol}
            onChange={(value) => update('protocol', String(value))}
          >
            <Select.Option value="openai_responses">OpenAI Responses</Select.Option>
            <Select.Option value="openai_chat">OpenAI Chat Completions</Select.Option>
            <Select.Option value="anthropic_messages">Anthropic Messages</Select.Option>
          </Select>
        </label>
        <label className="field field-base-url">
          <span>
            上游 Base URL <RequiredMark />
          </span>
          <Input
            value={form.baseUrl}
            onChange={(value) => update('baseUrl', value)}
            placeholder="https://api.example.com"
          />
        </label>
        <label className="field field-group">
          <span>分组</span>
          <Input
            value={form.group ?? ''}
            onChange={(value) => update('group', value)}
            placeholder="例如：tagA（可选）"
          />
        </label>
        <label className="field field-secret">
          <span>访问凭证 {!configured || !credential.secret ? <RequiredMark /> : null}</span>
          <div className="secret-input">
            <Input
              mode="password"
              value={credential.secret}
              onChange={(value) => updateCredential('secret', value)}
              placeholder={configured ? '已配置，输入新值可替换' : '输入渠道密钥'}
            />
            <Button
              theme="borderless"
              icon={<IconCopy />}
              aria-label="复制密钥"
              disabled={!credential.secret}
              onClick={onCopySecret}
            />
          </div>
        </label>
        <label className="field field-note">
          <span>备注</span>
          <TextArea
            value={form.note ?? ''}
            onChange={(value) => update('note', value)}
            placeholder="记录供应商、用途或维护信息（可选）"
            rows={2}
          />
        </label>
      </div>
    </EditorCard>
  )
}

type HeadersProps = {
  credential: Credential
  headersText: string
  onCredentialChange: (field: keyof Credential, value: string) => void
  onHeadersChange: (value: string) => void
}

// 渲染凭证元数据和整行 JSON 自定义请求头编辑器。
export function HeadersSection({
  credential,
  headersText,
  onCredentialChange,
  onHeadersChange,
}: HeadersProps) {
  return (
    <Collapse className="editor-card apple-card editor-collapse" keepDOM>
      <Collapse.Panel
        itemKey="headers"
        header={
          <span className="editor-card-title-line">
            <span>自定义请求头</span>
            <span className="editor-card-meta">点击展开 JSON 与认证 Header 配置</span>
          </span>
        }
      >
        <div className="credential-options-row">
          <label className="field">
            <span>
              凭证类型 <RequiredMark />
            </span>
            <Select
              className="apple-select"
              value={credential.type}
              onChange={(value) => onCredentialChange('type', String(value))}
            >
              <Select.Option value="bearer">Bearer Token</Select.Option>
              <Select.Option value="api_key">API Key</Select.Option>
              <Select.Option value="custom">自定义 Header</Select.Option>
            </Select>
          </label>
          <label className="field">
            <span>认证 Header</span>
            <Input
              value={credential.headerName}
              onChange={(value) => onCredentialChange('headerName', value)}
              placeholder="Authorization"
            />
          </label>
          <label className="field">
            <span>Header 前缀</span>
            <Input
              value={credential.prefix}
              onChange={(value) => onCredentialChange('prefix', value)}
              placeholder="Bearer "
            />
          </label>
        </div>
        <label className="field code-input headers-json-field">
          <span>
            请求头 JSON <RequiredMark />
          </span>
          <TextArea
            value={headersText}
            onChange={onHeadersChange}
            placeholder={'{\n  "X-Organization": "example"\n}'}
            rows={7}
          />
        </label>
      </Collapse.Panel>
    </Collapse>
  )
}

type ModelsProps = {
  mappings: ChannelMapping[]
  models: string[]
  testModel: string
  fallbackModel: string
  discovering: boolean
  canDiscover: boolean
  canTest: boolean
  testing: boolean
  modelInput: string
  onDiscover: () => void
  onClearModels: () => void
  onModelInput: (value: string) => void
  modelSuggestions: Array<{ modelId: string; displayName?: string }>
  onAddModel: () => void
  onRemoveModel: (model: string) => void
  onCopyModel: (model: string) => void
  onTestModel: (value: string) => void
  onTest: () => void
  onFallbackModel: (value: string) => void
  onAddMapping: () => void
  onRemoveMapping: (index: number) => void
  onChangeMapping: (index: number, field: 'logicalModel' | 'upstreamModel', value: string) => void
}

// 渲染模型发现、模型标签、测试、兜底模型和独立的模型映射模块。
export function ModelsSection({
  mappings,
  models,
  testModel,
  fallbackModel,
  discovering,
  canDiscover,
  canTest,
  testing,
  modelInput,
  onDiscover,
  onClearModels,
  onModelInput,
  modelSuggestions,
  onAddModel,
  onRemoveModel,
  onCopyModel,
  onTestModel,
  onTest,
  onFallbackModel,
  onAddMapping,
  onRemoveMapping,
  onChangeMapping,
}: ModelsProps) {
  const testOptions = Array.from(
    new Set(
      [...models, ...mappings.map((item) => item.upstreamModel.trim()), testModel].filter(Boolean),
    ),
  )
  const fallbackOptions = Array.from(new Set([...models, fallbackModel].filter(Boolean)))
  const modelsViewportRef = useRef<HTMLDivElement>(null)
  const [modelsOverflowing, setModelsOverflowing] = useState(false)

  useLayoutEffect(() => {
    const viewport = modelsViewportRef.current
    if (!viewport) return
    // 根据真实内容高度决定是否展示模型列表的省略提示。
    const updateOverflow = (): void => {
      setModelsOverflowing(viewport.scrollHeight > viewport.clientHeight + 1)
    }
    updateOverflow()
    const observer = new ResizeObserver(updateOverflow)
    observer.observe(viewport)
    return () => observer.disconnect()
  }, [models])

  return (
    <EditorCard
      title="模型配置"
      detail={`${models.length} 个可用模型`}
      headerAction={
        <div className="editor-card-header-actions">
          <Button
            theme="light"
            type="danger"
            icon={<IconDelete />}
            disabled={models.length === 0}
            onClick={onClearModels}
          >
            清除模型
          </Button>
          <Button
            theme="light"
            icon={<IconRefresh />}
            loading={discovering}
            disabled={!canDiscover}
            onClick={onDiscover}
          >
            从上游获取
          </Button>
        </div>
      }
    >
      <section className="model-module model-library-module" aria-label="模型列表">
        <div className="model-control-row">
          <div className="model-control-item">
            <span>测试模型</span>
            <Select
              className="apple-select"
              value={testModel}
              onChange={(value) => onTestModel(String(value))}
              placeholder="选择模型"
              disabled={testOptions.length === 0}
            >
              {testOptions.map((model) => (
                <Select.Option key={model} value={model}>
                  {model}
                </Select.Option>
              ))}
            </Select>
            <Button
              theme="light"
              icon={<IconPlay />}
              loading={testing}
              disabled={!canTest}
              onClick={onTest}
            >
              测试
            </Button>
          </div>
          <div className="model-control-item">
            <span>添加模型</span>
            <div className="model-input-with-suggestions">
              <Input value={modelInput} onChange={onModelInput} placeholder="输入模型名称" />
              {modelInput.trim() && modelSuggestions.length > 0 ? (
                <div className="model-suggestion-list">
                  {modelSuggestions.map((suggestion) => (
                    <button
                      key={suggestion.modelId}
                      type="button"
                      onClick={() => onModelInput(suggestion.modelId)}
                    >
                      <strong>{suggestion.displayName || suggestion.modelId}</strong>
                      <span>{suggestion.modelId}</span>
                    </button>
                  ))}
                </div>
              ) : null}
            </div>
            <Button
              theme="light"
              icon={<IconPlus />}
              disabled={!modelInput.trim()}
              onClick={onAddModel}
            >
              添加
            </Button>
          </div>
          <div className="model-control-item model-fallback-item">
            <span>兜底模型</span>
            <Select
              className="apple-select"
              value={fallbackModel || undefined}
              onChange={(value) => onFallbackModel(String(value ?? ''))}
              placeholder="未匹配时不转发"
              showClear
              disabled={fallbackOptions.length === 0}
            >
              {fallbackOptions.map((model) => (
                <Select.Option key={model} value={model}>
                  {model}
                </Select.Option>
              ))}
            </Select>
          </div>
        </div>
        <Tooltip content={models.length > 0 ? models.join('、') : undefined} position="top">
          <div
            ref={modelsViewportRef}
            className={`model-tags-viewport${models.length === 0 ? ' is-empty' : ''}${modelsOverflowing ? ' is-overflowing' : ''}`}
          >
            {models.length === 0 ? (
              <div className="inline-empty">暂无模型，请从上游获取或手动添加。</div>
            ) : (
              <div className="model-tags-list">
                {models.map((model) => (
                  <Tag
                    key={model}
                    className="model-tag"
                    color="blue"
                    type="light"
                    closable
                    aria-label={`复制模型 ${model}`}
                    onClose={(_value, event) => {
                      event.stopPropagation()
                      onRemoveModel(model)
                    }}
                    onClick={() => onCopyModel(model)}
                  >
                    {model}
                  </Tag>
                ))}
              </div>
            )}
          </div>
        </Tooltip>
      </section>
      <section className="model-module model-mapping-module" aria-label="模型映射">
        <div className="model-module-heading">
          <div>
            <strong>模型映射</strong>
            <span>仅展示手动配置的映射，渠道映射优先于全局映射</span>
          </div>
          <Button
            className="model-mapping-action"
            theme="solid"
            type="primary"
            icon={<IconPlus />}
            onClick={onAddMapping}
          >
            新增映射
          </Button>
        </div>
        <div className={`mapping-list${mappings.length === 0 ? ' is-empty' : ''}`}>
          {mappings.length === 0 && <div className="inline-empty">暂无手动模型映射</div>}
          {mappings.map((mapping, index) => (
            <div className="mapping-row" key={`${mapping.channelId || 'new'}-${index}`}>
              <Input
                value={mapping.logicalModel}
                onChange={(value) => onChangeMapping(index, 'logicalModel', value)}
                placeholder="请输入模型"
                aria-label="请输入模型"
              />
              <span className="mapping-arrow">→</span>
              <Input
                value={mapping.upstreamModel}
                onChange={(value) => onChangeMapping(index, 'upstreamModel', value)}
                placeholder="请输入实际模型"
                aria-label="请输入实际模型"
              />
              <Button
                theme="borderless"
                type="danger"
                icon={<IconDelete />}
                aria-label="删除模型映射"
                onClick={() => onRemoveMapping(index)}
              />
            </div>
          ))}
        </div>
      </section>
    </EditorCard>
  )
}

type TrafficProps = {
  form: Channel
  update: <K extends keyof Channel>(field: K, value: Channel[K]) => void
}

// 渲染渠道优先级、并发、超时和失败动作设置。
export function TrafficPolicySection({ form, update }: TrafficProps) {
  return (
    <EditorCard title="请求设置" detail="请求级运行策略">
      <div className="editor-form-grid traffic-grid">
        <label className="field">
          <span>渠道优先级</span>
          <InputNumber
            min={0}
            max={1000000}
            value={form.priority}
            onChange={(value) => update('priority', parseFiniteNumber(value, form.priority, 100))}
          />
        </label>
        <label className="field">
          <span>并发上限</span>
          <InputNumber
            min={1}
            max={1000}
            value={form.concurrencyLimit}
            onChange={(value) =>
              update('concurrencyLimit', parseFiniteNumber(value, form.concurrencyLimit, 8))
            }
            suffix="路"
          />
        </label>
        <label className="field">
          <span>请求超时</span>
          <InputNumber
            min={1000}
            value={form.requestTimeoutMs}
            onChange={(value) =>
              update('requestTimeoutMs', parseFiniteNumber(value, form.requestTimeoutMs, 120000))
            }
            suffix="毫秒"
          />
          <small className="field-hint">
            本次上游调用的总时长，含连接、等待响应和整段流式传输。超过后取消当前渠道调用。
          </small>
        </label>
        <label className="field">
          <span>流式空闲超时</span>
          <InputNumber
            min={1000}
            value={form.streamIdleTimeoutMs}
            onChange={(value) =>
              update(
                'streamIdleTimeoutMs',
                parseFiniteNumber(value, form.streamIdleTimeoutMs, 300000),
              )
            }
            suffix="毫秒"
          />
          <small className="field-hint">
            提交后相邻数据的最长间隔，有数据会重置。不能让流超过上面的请求超时。
          </small>
        </label>
        <label className="field">
          <span>连续失败阈值</span>
          <InputNumber
            min={1}
            max={20}
            value={form.failureThreshold}
            onChange={(value) =>
              update('failureThreshold', parseFiniteNumber(value, form.failureThreshold, 3))
            }
            suffix="次"
          />
        </label>
        <label className="field">
          <span>失败后动作</span>
          <Select
            className="apple-select"
            value={form.failureAction}
            onChange={(value) => update('failureAction', String(value))}
          >
            <Select.Option value="cooldown">进入冷却</Select.Option>
            <Select.Option value="auto_disable">自动禁用渠道</Select.Option>
          </Select>
        </label>
        <label className="field">
          <span>冷却基数</span>
          <InputNumber
            min={1}
            value={form.cooldownSeconds}
            onChange={(value) =>
              update('cooldownSeconds', parseFiniteNumber(value, form.cooldownSeconds, 30))
            }
            disabled={form.failureAction === 'auto_disable'}
            suffix="秒"
          />
          <small className="field-hint">
            {form.failureAction === 'auto_disable'
              ? '自动禁用不会使用冷却时间。'
              : '连续失败达到阈值后，以此为指数冷却的起始时长。'}
          </small>
        </label>
      </div>
    </EditorCard>
  )
}

type ProbeProps = {
  probe: ProbePolicy
  failureAction: string
  modelOptions: string[]
  inferredModel: string
  inferredSource: string
  update: (field: keyof ProbePolicy, value: string | number | boolean) => void
  onProbe: () => void
  onReset: () => void
  probing: boolean
  resetting: boolean
  canProbe: boolean
}

// 渲染自动探针、恢复策略和手动健康操作。
export function ProbeSection({
  probe,
  failureAction,
  modelOptions,
  inferredModel,
  inferredSource,
  update,
  onProbe,
  onReset,
  probing,
  resetting,
  canProbe,
}: ProbeProps) {
  return (
    <EditorCard
      title="探针设置"
      detail={probe.enabled ? '已启用' : '未启用'}
      headerAction={
        <div className="probe-header-actions">
          <div className="editor-switch-row probe-header-switch">
            <Switch
              checked={probe.enabled}
              onChange={(checked) => update('enabled', checked)}
              aria-label="启用探针"
            />
            <span>启用探针</span>
          </div>
          <div className="editor-switch-row probe-header-switch">
            <Switch
              checked={probe.autoRecover}
              onChange={(checked) => update('autoRecover', checked)}
              aria-label="探针自动恢复"
            />
            <span>探针自动恢复</span>
          </div>
          <Button
            theme="light"
            icon={<IconPlay />}
            loading={probing}
            disabled={!canProbe}
            onClick={onProbe}
          >
            立即探测
          </Button>
          <Button
            theme="light"
            icon={<IconRefresh />}
            loading={resetting}
            disabled={!canProbe}
            onClick={onReset}
          >
            人工恢复
          </Button>
        </div>
      }
    >
      <div className="editor-form-grid editor-form-grid-compact probe-grid">
        <label className="field">
          <span>探针模式</span>
          <Select
            className="apple-select"
            disabled={!probe.enabled}
            value={probe.mode}
            onChange={(value) => update('mode', String(value))}
          >
            <Select.Option value="connectivity">连通性检查</Select.Option>
            <Select.Option value="minimal_inference">最小推理</Select.Option>
          </Select>
        </label>
        <label className="field">
          <span>探针间隔</span>
          <InputNumber
            min={10}
            value={probe.intervalSeconds}
            disabled={!probe.enabled}
            onChange={(value) =>
              update('intervalSeconds', parseFiniteNumber(value, probe.intervalSeconds, 60))
            }
            suffix="秒"
          />
        </label>
        <label className="field">
          <span>自动探针模型</span>
          <Select
            className="apple-select"
            disabled={!probe.enabled || probe.mode !== 'minimal_inference'}
            value={probe.model || ''}
            onChange={(value) => update('model', String(value))}
            placeholder="自动选择"
          >
            <Select.Option value="">自动选择</Select.Option>
            {Array.from(new Set([probe.model, ...modelOptions].filter(Boolean))).map((model) => (
              <Select.Option key={model} value={model}>
                {model}
              </Select.Option>
            ))}
          </Select>
          <small className="field-hint">
            {probe.mode === 'connectivity'
              ? '连通性模式不使用推理模型，只验证上游可达。立即探测使用已保存策略。'
              : probe.model
                ? `定时探针将使用显式模型 ${probe.model}。立即探测使用已保存策略，未保存修改不会生效。`
                : inferredModel
                  ? `当前为空，表示自动选择；预计使用 ${inferredModel}（${inferredSource}）。立即探测使用已保存策略。`
                  : '当前为空，表示自动选择。最小推理模式保存前需要可推导模型。立即探测使用已保存策略。'}
          </small>
        </label>
        <label className="field">
          <span>失败阈值</span>
          <InputNumber
            min={1}
            value={probe.failureThreshold}
            disabled={!probe.enabled}
            onChange={(value) =>
              update('failureThreshold', parseFiniteNumber(value, probe.failureThreshold, 3))
            }
            suffix="次"
          />
        </label>
        <label className="field">
          <span>请求超时</span>
          <InputNumber
            min={1000}
            value={probe.requestTimeoutMs}
            disabled={!probe.enabled}
            onChange={(value) =>
              update('requestTimeoutMs', parseFiniteNumber(value, probe.requestTimeoutMs, 15000))
            }
            suffix="毫秒"
          />
          <small className="field-hint">单次探针调用的总时长，不影响业务请求。</small>
        </label>
        <label className="field">
          <span>恢复成功阈值</span>
          <InputNumber
            min={1}
            value={probe.recoverySuccessThreshold}
            disabled={!probe.enabled || !probe.autoRecover}
            onChange={(value) =>
              update(
                'recoverySuccessThreshold',
                parseFiniteNumber(value, probe.recoverySuccessThreshold, 1),
              )
            }
            suffix="次"
          />
        </label>
      </div>
      {failureAction === 'auto_disable' && !probe.autoRecover && (
        <p className="probe-recovery-hint">自动禁用后不会自动到期，需要人工恢复。</p>
      )}
    </EditorCard>
  )
}

// 渲染低频使用的上游协议兼容设置。
export function AdvancedSettingsSection({ form, update }: TrafficProps) {
  // 切换单项能力声明，空集合继续表示渠道不限制请求能力。
  function changeCapability(capability: ChannelCapability, checked: boolean): void {
    const current = form.capabilities ?? []
    update(
      'capabilities',
      checked
        ? Array.from(new Set([...current, capability]))
        : current.filter((item) => item !== capability),
    )
  }

  const supportsReasoningEffort =
    form.protocol === 'openai_responses' || form.protocol === 'openai_chat'
  return (
    <EditorCard title="高级设置" detail="协议兼容选项">
      <div className="advanced-settings-list">
        <div className="advanced-setting-item advanced-capability-item">
          <div className="advanced-setting-copy">
            <strong>能力声明</strong>
            <span>为空表示不限制；声明后只承接请求能力均在所选范围内的请求。</span>
          </div>
          <div className="advanced-capability-options" aria-label="能力声明">
            {channelCapabilityOptions.map((option) => (
              <Checkbox
                key={option.value}
                checked={(form.capabilities ?? []).includes(option.value)}
                onChange={(event) => changeCapability(option.value, Boolean(event.target.checked))}
                aria-label={`能力 ${option.label}`}
              >
                {option.label}
              </Checkbox>
            ))}
          </div>
        </div>
        {supportsReasoningEffort && (
          <div className="advanced-setting-item">
            <div className="advanced-setting-copy">
              <strong>思考等级</strong>
              <span>透传时沿用全局设置；全局也透传时保留请求原始值。</span>
            </div>
            <Select
              className="apple-select advanced-effort-select"
              value={form.reasoningEffort ?? 'passthrough'}
              onChange={(value) => update('reasoningEffort', String(value) as ReasoningEffort)}
              aria-label="思考等级"
            >
              <Select.Option value="passthrough">透传</Select.Option>
              <Select.Option value="low">low</Select.Option>
              <Select.Option value="medium">medium</Select.Option>
              <Select.Option value="high">high</Select.Option>
              <Select.Option value="xhigh">xhigh</Select.Option>
              <Select.Option value="max">max</Select.Option>
            </Select>
          </div>
        )}
        <div className="advanced-setting-item">
          <div className="advanced-setting-copy">
            <strong>透传 service_tier</strong>
            <span>
              全局与当前渠道均开启时透传客户端设置；关闭时 OpenAI 使用 default，Anthropic 使用
              auto。
            </span>
          </div>
          <Switch
            checked={form.serviceTierPassthrough}
            onChange={(checked) => update('serviceTierPassthrough', checked)}
            aria-label="透传 service_tier"
          />
        </div>
      </div>
    </EditorCard>
  )
}

// 渲染表单底部操作栏。
export function EditorActionBar({
  editing,
  saving,
  copying,
  onCancel,
  onCopy,
}: {
  editing: boolean
  saving: boolean
  copying: boolean
  onCancel: () => void
  onCopy: () => void
}) {
  return (
    <div className="editor-submit-bar apple-glass">
      <Button htmlType="button" theme="borderless" onClick={onCancel}>
        取消
      </Button>
      {editing && (
        <Button
          htmlType="button"
          theme="light"
          icon={<IconCopy />}
          loading={copying}
          onClick={onCopy}
        >
          复制新建
        </Button>
      )}
      <Button htmlType="submit" theme="solid" type="primary" loading={saving} disabled={saving}>
        保存
      </Button>
    </div>
  )
}
