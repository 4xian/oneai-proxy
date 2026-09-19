import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { Button, Select, Switch } from '@douyinfe/semi-ui-19'
import { adminError, adminFetch } from '../../../api'
import { showErrorToast, showSuccessToast, showWarningToast } from '../../../notifications'
import {
  defaultRuntimeSettings,
  normalizeRuntimeSettings,
  type ChannelSettings,
  type ReasoningEffort,
  type RuntimeSettings,
  type SettingsSectionProps,
} from '../settings-types'

type ChannelDraft = {
  value: RuntimeSettings
  baseline: RuntimeSettings
  revision: number
}

const emptyDraft: ChannelDraft = {
  value: defaultRuntimeSettings,
  baseline: defaultRuntimeSettings,
  revision: 0,
}

// 渲染全局渠道请求体策略，并在有草稿时避免回访覆盖输入。
function ChannelSettingsSection({ active, reloadToken = 0 }: SettingsSectionProps) {
  const [draft, setDraft] = useState<ChannelDraft>(emptyDraft)
  const [isLoading, setIsLoading] = useState(true)
  const [loadFailed, setLoadFailed] = useState(false)
  const [isSaving, setIsSaving] = useState(false)
  const [syncNotice, setSyncNotice] = useState('')
  const wasActiveRef = useRef(false)
  const draftRef = useRef(emptyDraft)
  const loadSeqRef = useRef(0)

  // 同步当前草稿快照。
  function updateDraft(next: ChannelDraft): void {
    draftRef.current = next
    setDraft(next)
  }

  // 读取已持久化的渠道设置；有未保存草稿时只更新基线。
  const loadSettings = useCallback(async (): Promise<boolean> => {
    const seq = ++loadSeqRef.current
    setIsLoading(true)
    setLoadFailed(false)
    try {
      const response = await adminFetch('/api/admin/v1/settings')
      if (!response.ok) throw await adminError(response, '读取全局渠道设置失败')
      const result = normalizeRuntimeSettings((await response.json()) as Partial<RuntimeSettings>)
      if (seq !== loadSeqRef.current) return false
      const current = draftRef.current
      if (current.revision > 0) {
        updateDraft({ ...current, baseline: result })
        setSyncNotice('服务器数据可能已变化，已保留当前未保存输入。')
      } else {
        updateDraft({ value: result, baseline: result, revision: 0 })
        setSyncNotice('')
      }
      return true
    } catch (error) {
      if (seq !== loadSeqRef.current) return false
      setLoadFailed(true)
      showErrorToast(error instanceof Error ? error.message : '读取全局渠道设置失败')
      return false
    } finally {
      if (seq === loadSeqRef.current) setIsLoading(false)
    }
  }, [])

  useEffect(() => {
    if (active && !wasActiveRef.current) void loadSettings()
    wasActiveRef.current = active
  }, [active, loadSettings])

  useEffect(() => {
    if (reloadToken > 0) void loadSettings()
  }, [loadSettings, reloadToken])

  // 更新全局渠道请求体策略。
  function updateChannelSettings(field: keyof ChannelSettings, value: string | boolean): void {
    const current = draftRef.current
    updateDraft({
      ...current,
      value: {
        ...current.value,
        channelSettings: { ...current.value.channelSettings, [field]: value },
      },
      revision: current.revision + 1,
    })
  }

  // 保存渠道设置；回包只推进已提交快照的基线。
  async function saveSettings(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    if (isLoading || loadFailed) {
      showWarningToast('当前设置尚未成功读取，请刷新后再保存。')
      return
    }
    const snapshot = draftRef.current
    setIsSaving(true)
    try {
      const latestResponse = await adminFetch('/api/admin/v1/settings')
      if (!latestResponse.ok) throw await adminError(latestResponse, '读取全局渠道设置失败')
      const latest = normalizeRuntimeSettings(
        (await latestResponse.json()) as Partial<RuntimeSettings>,
      )
      const payload: RuntimeSettings = {
        ...latest,
        channelSettings: snapshot.value.channelSettings,
      }
      const response = await adminFetch('/api/admin/v1/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      })
      if (!response.ok) throw await adminError(response, '保存全局渠道设置失败')
      const result = normalizeRuntimeSettings((await response.json()) as Partial<RuntimeSettings>)
      const current = draftRef.current
      if (current.revision === snapshot.revision) {
        updateDraft({ value: result, baseline: result, revision: 0 })
        setSyncNotice('')
      } else {
        updateDraft({ ...current, baseline: result })
        showWarningToast('已保存较早快照，当前输入仍有未保存修改。')
      }
      showSuccessToast('全局渠道设置已立即生效。')
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '保存全局渠道设置失败')
    } finally {
      setIsSaving(false)
    }
  }

  const listeners = draft.value
  return (
    <section
      className="settings-panel settings-section channel-settings-section"
      aria-labelledby="channel-settings-title"
    >
      <div className="section-intro">
        <div>
          <p className="eyebrow">CHANNEL REQUEST OVERRIDES</p>
          <h2 id="channel-settings-title">全局渠道设置</h2>
        </div>
        <p className="section-note">
          全局策略作用于每个后续代理请求；渠道编辑页指定的非透传思考等级优先于全局等级。
        </p>
      </div>
      {syncNotice ? <p className="form-message">{syncNotice}</p> : null}
      <form className="settings-form channel-settings-form" onSubmit={saveSettings}>
        <label className="field channel-effort-field">
          <span>全局思考等级</span>
          <Select
            className="apple-select"
            value={listeners.channelSettings.reasoningEffort}
            onChange={(value) =>
              updateChannelSettings('reasoningEffort', String(value) as ReasoningEffort)
            }
            aria-label="全局思考等级"
          >
            <Select.Option value="passthrough">透传</Select.Option>
            <Select.Option value="low">low</Select.Option>
            <Select.Option value="medium">medium</Select.Option>
            <Select.Option value="high">high</Select.Option>
            <Select.Option value="xhigh">xhigh</Select.Option>
            <Select.Option value="max">max</Select.Option>
          </Select>
          <small>
            透传保留请求原始值；其他选项会替换 Responses 的 reasoning.effort 或 Chat Completions 的
            reasoning_effort。
          </small>
        </label>
        <div className="channel-setting-switch">
          <div>
            <strong>透传 service_tier</strong>
            <small>
              需与渠道开关同时开启才保留请求值；关闭时 OpenAI 使用 default，Anthropic 使用 auto。
            </small>
          </div>
          <Switch
            checked={listeners.channelSettings.serviceTierPassthrough}
            onChange={(checked) => updateChannelSettings('serviceTierPassthrough', checked)}
            aria-label="全局透传 service_tier"
          />
        </div>
        <Button
          className="save-button"
          htmlType="submit"
          theme="solid"
          type="primary"
          loading={isSaving}
          disabled={isLoading || loadFailed}
        >
          保存全局渠道设置
        </Button>
      </form>
      {isLoading && <p className="form-message">正在读取当前设置…</p>}
    </section>
  )
}

export default ChannelSettingsSection
