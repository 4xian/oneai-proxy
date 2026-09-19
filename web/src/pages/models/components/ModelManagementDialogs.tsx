import { useEffect, useMemo, useState, type FormEvent } from 'react'
import {
  Button,
  Checkbox,
  Input,
  InputNumber,
  Modal,
  Pagination,
  Select,
  Tag,
  TextArea,
} from '@douyinfe/semi-ui-19'
import { changeText, protocolText, type ModelCatalogEntry, type Preview } from './modelCatalog'

type PreviewProps = {
  preview: Preview | null
  visible: boolean
  syncing: boolean
  search: string
  selection: string[]
  onSearchChange: (value: string) => void
  onSelectionChange: (keys: string[]) => void
  onCancel: () => void
  onApply: () => void
}

type SourceProps = {
  visible: boolean
  sourceURL: string
  syncing: boolean
  onSourceChange: (value: string) => void
  onRestoreDefault: () => void
  onCancel: () => void
  onPreview: () => void
}

type ManualProps = {
  visible: boolean
  editingKey: string
  entry: ModelCatalogEntry
  saving: boolean
  onEntryChange: (entry: ModelCatalogEntry) => void
  onCancel: () => void
  onSubmit: (event: FormEvent) => void
}

// ModelSourceModal 收集单一公开模型源地址，并在写库前进入差异预览。
function ModelSourceModal({
  visible,
  sourceURL,
  syncing,
  onSourceChange,
  onRestoreDefault,
  onCancel,
  onPreview,
}: SourceProps) {
  return (
    <Modal
      title="同步在线模型"
      visible={visible}
      width={620}
      onCancel={onCancel}
      footer={
        <div className="modal-footer-actions">
          <Button theme="light" onClick={onCancel}>
            取消
          </Button>
          <Button theme="light" onClick={onRestoreDefault}>
            恢复默认源
          </Button>
          <Button theme="solid" type="primary" loading={syncing} onClick={onPreview}>
            预览差异
          </Button>
        </div>
      }
    >
      <label className="model-source-field" htmlFor="models-source-url">
        <span>在线模型源</span>
        <Input
          id="models-source-url"
          value={sourceURL}
          onChange={onSourceChange}
          placeholder="https://models.dev/catalog.json"
        />
      </label>
    </Modal>
  )
}

// ModelSyncPreviewModal 展示同步差异，并让用户在写库前选择记录。
function ModelSyncPreviewModal({
  preview,
  visible,
  syncing,
  search,
  selection,
  onSearchChange,
  onSelectionChange,
  onCancel,
  onApply,
}: PreviewProps) {
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(20)
  const filteredItems = useMemo(() => {
    const value = search.trim().toLowerCase()
    if (!value) return preview?.items ?? []
    return (preview?.items ?? []).filter((item) => {
      const entry = item.entry
      return [entry.modelId, entry.displayName, entry.vendor, entry.provider]
        .filter(Boolean)
        .some((field) => String(field).toLowerCase().includes(value))
    })
  }, [preview, search])
  const pageItems = useMemo(
    () => filteredItems.slice((page - 1) * pageSize, page * pageSize),
    [filteredItems, page, pageSize],
  )
  const selectableItems = pageItems.filter(
    (item) => item.changeType !== 'unchanged' && item.changeType !== 'conflict',
  )

  // 更新单条预览记录的选择状态，并保持跨页选择结果。
  function changeItemSelection(stableKey: string, checked: boolean): void {
    onSelectionChange(
      checked
        ? Array.from(new Set([...selection, stableKey]))
        : selection.filter((key) => key !== stableKey),
    )
  }

  // 搜索或切换预览内容后从第一页开始，避免当前页超出筛选结果。
  useEffect(() => {
    setPage(1)
  }, [preview, search, visible])

  return (
    <Modal
      title="同步预览"
      visible={visible}
      width={900}
      onCancel={onCancel}
      footer={
        <div className="modal-footer-actions">
          <Button theme="light" onClick={onCancel}>
            取消
          </Button>
          <Button
            theme="solid"
            type="primary"
            loading={syncing}
            disabled={selection.length === 0}
            onClick={onApply}
          >
            应用已选 {selection.length} 条
          </Button>
        </div>
      }
    >
      {preview ? (
        <div className="preview-content">
          <div className="preview-summary">
            <Tag color="blue">{preview.adapter}</Tag>
            <span>可识别 {preview.parsed} 条</span>
            <span>跳过 {preview.skipped} 条</span>
            <span>差异 {preview.items.length} 条</span>
          </div>
          <Input
            value={search}
            onChange={onSearchChange}
            placeholder="搜索预览中的模型"
            showClear
          />
          <div className="preview-select-actions">
            <Checkbox
              checked={
                selectableItems.length > 0 &&
                selectableItems.every((item) => selection.includes(item.entry.stableKey))
              }
              onChange={(event) =>
                onSelectionChange(
                  event.target.checked
                    ? Array.from(
                        new Set([
                          ...selection,
                          ...selectableItems.map((item) => item.entry.stableKey),
                        ]),
                      )
                    : selection.filter(
                        (key) => !selectableItems.some((item) => item.entry.stableKey === key),
                      ),
                )
              }
            >
              选择当前页
            </Checkbox>
            <span className="preview-selection-count">已选 {selection.length} 条</span>
          </div>
          {preview.errors.length ? (
            <div className="preview-errors">{preview.errors.slice(0, 5).join('；')}</div>
          ) : null}
          <div className="preview-list-wrap">
            <div className="preview-list">
              {pageItems.map((item) => {
                const disabled = item.changeType === 'unchanged' || item.changeType === 'conflict'
                const checked = selection.includes(item.entry.stableKey)
                const label = item.entry.displayName || item.entry.modelId
                return (
                  <div key={item.entry.stableKey} className="preview-row">
                    <Checkbox
                      className="preview-row-checkbox"
                      aria-label={`选择 ${label}`}
                      checked={checked}
                      disabled={disabled}
                      onChange={(event) =>
                        changeItemSelection(item.entry.stableKey, Boolean(event.target.checked))
                      }
                    >
                      <span className="preview-row-details">
                        <strong>{label}</strong>
                        <span>
                          {item.entry.modelId} · {protocolText(item.entry.protocol)}
                        </span>
                      </span>
                    </Checkbox>
                    <Tag
                      color={
                        item.changeType === 'new'
                          ? 'green'
                          : item.changeType === 'changed'
                            ? 'blue'
                            : item.changeType === 'conflict'
                              ? 'orange'
                              : item.changeType === 'retired'
                                ? 'red'
                                : 'grey'
                      }
                    >
                      {changeText(item.changeType)}
                    </Tag>
                  </div>
                )
              })}
            </div>
          </div>
          {filteredItems.length > 0 ? (
            <div className="preview-pagination">
              <span>
                显示 {(page - 1) * pageSize + 1}-{Math.min(page * pageSize, filteredItems.length)} /{' '}
                {filteredItems.length} 条
              </span>
              <Pagination
                currentPage={page}
                pageSize={pageSize}
                total={filteredItems.length}
                showSizeChanger
                pageSizeOpts={[20, 50, 100]}
                onChange={(nextPage, nextPageSize) => {
                  setPage(nextPage)
                  setPageSize(nextPageSize)
                }}
              />
            </div>
          ) : null}
        </div>
      ) : null}
    </Modal>
  )
}

// ManualModelModal 负责手工模型的新增与编辑表单。
function ManualModelModal({
  visible,
  editingKey,
  entry,
  saving,
  onEntryChange,
  onCancel,
  onSubmit,
}: ManualProps) {
  const onlineEditing = Boolean(editingKey && entry.sourceType === 'online')
  return (
    <Modal
      title={editingKey ? (onlineEditing ? '编辑在线模型' : '编辑手工模型') : '新增手工模型'}
      visible={visible}
      width={760}
      onCancel={onCancel}
      footer={null}
    >
      <form onSubmit={onSubmit} className="manual-model-form">
        <div className="manual-form-row">
          <label htmlFor="manual-model-id">
            <span>
              模型 ID <em>*</em>
            </span>
            <Input
              id="manual-model-id"
              value={entry.modelId}
              onChange={(value) => onEntryChange({ ...entry, modelId: value })}
            />
          </label>
          <div className="manual-field">
            <span id="manual-model-protocol-label">
              协议 <em>*</em>
            </span>
            <Select
              id="manual-model-protocol"
              aria-labelledby="manual-model-protocol-label"
              value={entry.protocol}
              onChange={(value) => onEntryChange({ ...entry, protocol: String(value) })}
            >
              {entry.protocol === 'protocol_unconfirmed' ? (
                <Select.Option value="protocol_unconfirmed">待确认</Select.Option>
              ) : null}
              <Select.Option value="openai_chat">OpenAI Chat</Select.Option>
              <Select.Option value="openai_responses">OpenAI Responses</Select.Option>
              <Select.Option value="anthropic_messages">Anthropic Messages</Select.Option>
            </Select>
          </div>
        </div>
        <div className="manual-form-row">
          <label htmlFor="manual-model-display-name">
            <span>显示名称</span>
            <Input
              id="manual-model-display-name"
              value={entry.displayName}
              onChange={(value) => onEntryChange({ ...entry, displayName: value })}
            />
          </label>
          <label htmlFor="manual-model-base-id">
            <span>基础模型</span>
            <Input
              id="manual-model-base-id"
              value={entry.baseModelId}
              onChange={(value) => onEntryChange({ ...entry, baseModelId: value })}
            />
          </label>
        </div>
        <div className="manual-form-row manual-form-row-three">
          <label htmlFor="manual-model-vendor">
            <span>厂商</span>
            <Input
              id="manual-model-vendor"
              value={entry.vendor}
              onChange={(value) => onEntryChange({ ...entry, vendor: value })}
            />
          </label>
          <label htmlFor="manual-model-provider">
            <span>服务供应商</span>
            <Input
              id="manual-model-provider"
              value={entry.provider}
              disabled={onlineEditing}
              onChange={(value) => onEntryChange({ ...entry, provider: value })}
            />
          </label>
          <label htmlFor="manual-model-type">
            <span>模型类型</span>
            <Input
              id="manual-model-type"
              value={entry.modelType}
              onChange={(value) => onEntryChange({ ...entry, modelType: value })}
            />
          </label>
        </div>
        <div className="manual-form-row manual-form-row-three">
          <label htmlFor="manual-model-input-modalities">
            <span>输入模态</span>
            <Input
              id="manual-model-input-modalities"
              value={entry.modalities.input.join(', ')}
              onChange={(value) =>
                onEntryChange({
                  ...entry,
                  modalities: {
                    ...entry.modalities,
                    input: value
                      .split(',')
                      .map((item) => item.trim())
                      .filter(Boolean),
                  },
                })
              }
            />
          </label>
          <label htmlFor="manual-model-output-modalities">
            <span>输出模态</span>
            <Input
              id="manual-model-output-modalities"
              value={entry.modalities.output.join(', ')}
              onChange={(value) =>
                onEntryChange({
                  ...entry,
                  modalities: {
                    ...entry.modalities,
                    output: value
                      .split(',')
                      .map((item) => item.trim())
                      .filter(Boolean),
                  },
                })
              }
            />
          </label>
          <label htmlFor="manual-model-capabilities">
            <span>能力（逗号分隔）</span>
            <Input
              id="manual-model-capabilities"
              value={entry.capabilities.join(', ')}
              onChange={(value) =>
                onEntryChange({
                  ...entry,
                  capabilities: value
                    .split(',')
                    .map((item) => item.trim())
                    .filter(Boolean),
                })
              }
            />
          </label>
        </div>
        <div className="manual-form-row manual-form-row-three">
          <label htmlFor="manual-model-context-window">
            <span>上下文长度</span>
            <InputNumber
              id="manual-model-context-window"
              value={entry.contextWindow}
              min={0}
              onChange={(value) => onEntryChange({ ...entry, contextWindow: Number(value) || 0 })}
            />
          </label>
          <label htmlFor="manual-model-max-input">
            <span>最大输入</span>
            <InputNumber
              id="manual-model-max-input"
              value={entry.maxInputTokens}
              min={0}
              onChange={(value) => onEntryChange({ ...entry, maxInputTokens: Number(value) || 0 })}
            />
          </label>
          <label htmlFor="manual-model-max-output">
            <span>最大输出</span>
            <InputNumber
              id="manual-model-max-output"
              value={entry.maxOutputTokens}
              min={0}
              onChange={(value) => onEntryChange({ ...entry, maxOutputTokens: Number(value) || 0 })}
            />
          </label>
        </div>
        <div className="manual-form-row manual-form-row-three">
          <label htmlFor="manual-model-published-at">
            <span>发布时间</span>
            <Input
              id="manual-model-published-at"
              value={entry.publishedAt}
              placeholder="YYYY-MM-DD"
              onChange={(value) => onEntryChange({ ...entry, publishedAt: value })}
            />
          </label>
          <label htmlFor="manual-model-currency">
            <span>费用币种</span>
            <Input
              id="manual-model-currency"
              value={entry.currency}
              onChange={(value) => onEntryChange({ ...entry, currency: value })}
            />
          </label>
          <label htmlFor="manual-model-billing-unit">
            <span>计费单位</span>
            <Input
              id="manual-model-billing-unit"
              value={entry.billingUnit}
              onChange={(value) => onEntryChange({ ...entry, billingUnit: value })}
            />
          </label>
        </div>
        <div className="manual-form-row manual-form-row-four">
          <label htmlFor="manual-model-input-price">
            <span>输入费用</span>
            <InputNumber
              id="manual-model-input-price"
              value={entry.pricing.input ?? undefined}
              min={0}
              step={0.000001}
              onChange={(value) =>
                onEntryChange({
                  ...entry,
                  pricing: { ...entry.pricing, input: Number(value) || 0 },
                })
              }
            />
          </label>
          <label htmlFor="manual-model-output-price">
            <span>输出费用</span>
            <InputNumber
              id="manual-model-output-price"
              value={entry.pricing.output ?? undefined}
              min={0}
              step={0.000001}
              onChange={(value) =>
                onEntryChange({
                  ...entry,
                  pricing: { ...entry.pricing, output: Number(value) || 0 },
                })
              }
            />
          </label>
          <label htmlFor="manual-model-cache-read-price">
            <span>缓存读费用</span>
            <InputNumber
              id="manual-model-cache-read-price"
              value={entry.pricing.cacheRead ?? undefined}
              min={0}
              step={0.000001}
              onChange={(value) =>
                onEntryChange({
                  ...entry,
                  pricing: { ...entry.pricing, cacheRead: Number(value) || 0 },
                })
              }
            />
          </label>
          <label htmlFor="manual-model-cache-write-price">
            <span>缓存写费用</span>
            <InputNumber
              id="manual-model-cache-write-price"
              value={entry.pricing.cacheWrite ?? undefined}
              min={0}
              step={0.000001}
              onChange={(value) =>
                onEntryChange({
                  ...entry,
                  pricing: { ...entry.pricing, cacheWrite: Number(value) || 0 },
                })
              }
            />
          </label>
        </div>
        <label className="manual-form-description" htmlFor="manual-model-description">
          <span>描述 / 来源备注</span>
          <TextArea
            id="manual-model-description"
            value={entry.description}
            onChange={(value) => onEntryChange({ ...entry, description: value })}
            rows={3}
          />
        </label>
        <div className="manual-form-actions">
          <Button theme="light" htmlType="button" onClick={onCancel}>
            取消
          </Button>
          <Button theme="solid" type="primary" htmlType="submit" loading={saving} disabled={saving}>
            保存模型
          </Button>
        </div>
      </form>
    </Modal>
  )
}

export { ManualModelModal, ModelSourceModal, ModelSyncPreviewModal }
