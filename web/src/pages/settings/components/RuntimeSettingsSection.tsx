import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { Button, Input, InputNumber } from '@douyinfe/semi-ui-19'
import { IconRefresh } from '@douyinfe/semi-icons'
import { adminError, adminFetch } from '../../../api'
import { showErrorToast, showSuccessToast, showWarningToast } from '../../../notifications'
import {
  defaultRuntimeSettings,
  hasFiniteRuntimeNumbers,
  normalizeRuntimeSettings,
  parseFiniteNumber,
  type Listener,
  type RequestPolicy,
  type RuntimeSettings,
  type SettingsSectionProps,
} from '../settings-types'

type RuntimeDraft = {
  value: RuntimeSettings
  baseline: RuntimeSettings
  revision: number
  submittedRevision: number
}

const emptyDraft: RuntimeDraft = {
  value: defaultRuntimeSettings,
  baseline: defaultRuntimeSettings,
  revision: 0,
  submittedRevision: 0,
}

// 渲染监听地址和请求策略，并在有草稿时避免回访覆盖输入。
function RuntimeSettingsSection({ active, reloadToken = 0 }: SettingsSectionProps) {
  const [draft, setDraft] = useState<RuntimeDraft>(emptyDraft)
  const [isLoading, setIsLoading] = useState(true)
  const [loadFailed, setLoadFailed] = useState(false)
  const [isSaving, setIsSaving] = useState(false)
  const [syncNotice, setSyncNotice] = useState('')
  const wasActiveRef = useRef(false)
  const draftRef = useRef(emptyDraft)
  const loadSeqRef = useRef(0)

  // 同步当前草稿快照，供异步加载判断是否覆盖表单。
  function updateDraft(next: RuntimeDraft): void {
    draftRef.current = next
    setDraft(next)
  }

  // 读取已持久化的运行设置；有未保存草稿时只更新基线。
  const loadSettings = useCallback(async (notifySuccess = false): Promise<boolean> => {
    const seq = ++loadSeqRef.current
    setIsLoading(true)
    setLoadFailed(false)
    try {
      const response = await adminFetch('/api/admin/v1/settings')
      if (!response.ok) throw await adminError(response, '读取监听配置失败')
      const result = normalizeRuntimeSettings((await response.json()) as Partial<RuntimeSettings>)
      if (seq !== loadSeqRef.current) return false
      const current = draftRef.current
      if (current.revision > 0) {
        updateDraft({ ...current, baseline: result })
        setSyncNotice('服务器数据可能已变化，已保留当前未保存输入。')
      } else {
        updateDraft({ value: result, baseline: result, revision: 0, submittedRevision: 0 })
        setSyncNotice('')
        if (notifySuccess) showSuccessToast('监听设置已刷新。')
      }
      return true
    } catch (error) {
      if (seq !== loadSeqRef.current) return false
      setLoadFailed(true)
      showErrorToast(error instanceof Error ? error.message : '读取监听配置失败')
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

  // 更新单个监听器字段。
  function updateListener(
    listener: 'proxyListener' | 'adminListener',
    field: keyof Listener,
    value: string | number,
  ): void {
    const current = draftRef.current
    updateDraft({
      ...current,
      value: { ...current.value, [listener]: { ...current.value[listener], [field]: value } },
      revision: current.revision + 1,
    })
  }

  // 更新单个请求策略字段。
  function updateRequestPolicy(field: keyof RequestPolicy, value: number): void {
    const current = draftRef.current
    updateDraft({
      ...current,
      value: {
        ...current.value,
        requestPolicy: { ...current.value.requestPolicy, [field]: value },
      },
      revision: current.revision + 1,
    })
  }

  // 保存监听配置；回包只推进已提交快照的基线。
  async function saveSettings(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    if (isLoading || loadFailed) {
      showWarningToast('当前设置尚未成功读取，请刷新后再保存。')
      return
    }
    if (!hasFiniteRuntimeNumbers(draft.value)) {
      showWarningToast('请填写有效的数字配置。')
      return
    }
    const snapshot = draftRef.current
    setIsSaving(true)
    try {
      const latestResponse = await adminFetch('/api/admin/v1/settings')
      if (!latestResponse.ok) throw await adminError(latestResponse, '读取监听配置失败')
      const latest = normalizeRuntimeSettings(
        (await latestResponse.json()) as Partial<RuntimeSettings>,
      )
      const payload: RuntimeSettings = {
        ...latest,
        proxyListener: snapshot.value.proxyListener,
        adminListener: snapshot.value.adminListener,
        requestPolicy: snapshot.value.requestPolicy,
      }
      const response = await adminFetch('/api/admin/v1/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      })
      if (!response.ok) throw await adminError(response, '保存监听配置失败')
      const result = normalizeRuntimeSettings((await response.json()) as Partial<RuntimeSettings>)
      const current = draftRef.current
      if (current.revision === snapshot.revision) {
        updateDraft({ value: result, baseline: result, revision: 0, submittedRevision: 0 })
        setSyncNotice('')
      } else {
        updateDraft({ ...current, baseline: result, submittedRevision: snapshot.revision })
        showWarningToast('已保存较早快照，当前输入仍有未保存修改。')
      }
      showSuccessToast(
        result.restartRequired
          ? '请求策略已立即生效；监听地址将在重启后生效。'
          : '请求策略和监听设置已保存。',
      )
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '保存监听配置失败')
    } finally {
      setIsSaving(false)
    }
  }

  const listeners = draft.value
  return (
    <section
      className="settings-panel settings-section listener-settings-section"
      id="settings"
      aria-labelledby="settings-title"
    >
      <div className="section-intro">
        <div>
          <p className="eyebrow">RUNTIME CONFIGURATION</p>
          <h2 id="settings-title">监听设置</h2>
        </div>
        <Button
          className="refresh-button"
          icon={<IconRefresh />}
          theme="borderless"
          aria-label="重新读取监听设置"
          onClick={() => void loadSettings(true)}
        />
      </div>
      {syncNotice ? <p className="form-message">{syncNotice}</p> : null}
      <form className="settings-form" onSubmit={saveSettings}>
        <div className="runtime-settings-group">
          <div className="runtime-settings-heading">
            <h3>监听地址</h3>
            <p>保存后需要重启应用，现有监听不会在运行中切换。</p>
          </div>
          <div className="runtime-settings-fields">
            <label className="field">
              <span>代理 IP</span>
              <Input
                value={listeners.proxyListener.host}
                onChange={(value) => updateListener('proxyListener', 'host', value)}
                placeholder="127.0.0.1"
                aria-label="代理 IP"
                required
              />
            </label>
            <label className="field field-number">
              <span>代理端口</span>
              <InputNumber
                value={listeners.proxyListener.port}
                min={1}
                max={65535}
                hideButtons
                onChange={(value) =>
                  updateListener(
                    'proxyListener',
                    'port',
                    parseFiniteNumber(value, listeners.proxyListener.port, 9988),
                  )
                }
                aria-label="代理端口"
                required
              />
            </label>
            <label className="field">
              <span>管理 IP</span>
              <Input
                value={listeners.adminListener.host}
                onChange={(value) => updateListener('adminListener', 'host', value)}
                placeholder="127.0.0.1"
                aria-label="管理 IP"
                required
              />
            </label>
            <label className="field field-number">
              <span>管理端口</span>
              <InputNumber
                value={listeners.adminListener.port}
                min={1}
                max={65535}
                hideButtons
                onChange={(value) =>
                  updateListener(
                    'adminListener',
                    'port',
                    parseFiniteNumber(value, listeners.adminListener.port, 9989),
                  )
                }
                aria-label="管理端口"
                required
              />
            </label>
          </div>
        </div>
        <div className="runtime-settings-group runtime-policy-group">
          <div className="runtime-settings-heading">
            <h3>请求策略</h3>
            <p>
              保存后立即用于新请求，已经开始的请求继续使用原策略。连接和首字节只约束建连与等响应；流式空闲只约束相邻数据间隔；逻辑请求总超时约束整次请求，含切渠道和已提交的流。
            </p>
          </div>
          <div className="runtime-settings-fields">
            <label className="field field-number runtime-timeout-field">
              <span>连接超时</span>
              <InputNumber
                value={listeners.requestPolicy.connectTimeoutMs}
                min={1}
                hideButtons
                suffix="毫秒"
                onChange={(value) =>
                  updateRequestPolicy(
                    'connectTimeoutMs',
                    parseFiniteNumber(value, listeners.requestPolicy.connectTimeoutMs, 5000),
                  )
                }
              />
              <small>建立到上游的 TCP/TLS 连接。</small>
            </label>
            <label className="field field-number runtime-timeout-field">
              <span>首字节超时</span>
              <InputNumber
                value={listeners.requestPolicy.firstByteTimeoutMs}
                min={1}
                hideButtons
                suffix="毫秒"
                onChange={(value) =>
                  updateRequestPolicy(
                    'firstByteTimeoutMs',
                    parseFiniteNumber(value, listeners.requestPolicy.firstByteTimeoutMs, 30000),
                  )
                }
              />
              <small>发出上游请求后等到首个响应字节。</small>
            </label>
            <label className="field field-number runtime-timeout-field">
              <span>流式空闲超时</span>
              <InputNumber
                value={listeners.requestPolicy.streamIdleTimeoutMs}
                min={1}
                hideButtons
                suffix="毫秒"
                onChange={(value) =>
                  updateRequestPolicy(
                    'streamIdleTimeoutMs',
                    parseFiniteNumber(value, listeners.requestPolicy.streamIdleTimeoutMs, 60000),
                  )
                }
              />
              <small>渠道未单独设置时的默认空闲间隔，有数据会重置。不限制流的总时长。</small>
            </label>
            <label className="field field-number runtime-timeout-field">
              <span>逻辑请求总超时</span>
              <InputNumber
                value={listeners.requestPolicy.totalTimeoutMs}
                min={1}
                hideButtons
                suffix="毫秒"
                onChange={(value) =>
                  updateRequestPolicy(
                    'totalTimeoutMs',
                    parseFiniteNumber(value, listeners.requestPolicy.totalTimeoutMs, 120000),
                  )
                }
              />
              <small>整次逻辑请求的总时长，含切渠道和流式传输。</small>
            </label>
            <label className="field field-number max-attempts-field">
              <span>最大渠道尝试数</span>
              <InputNumber
                value={listeners.requestPolicy.maxChannelAttempts}
                min={0}
                max={1000}
                hideButtons
                suffix="个渠道"
                onChange={(value) =>
                  updateRequestPolicy(
                    'maxChannelAttempts',
                    parseFiniteNumber(value, listeners.requestPolicy.maxChannelAttempts, 0),
                  )
                }
              />
              <small>
                0：全部合格渠道；1：只调用首个可准入渠道；2：首个失败后最多再调用一个不同渠道。
              </small>
            </label>
          </div>
        </div>
        <Button
          className="save-button"
          htmlType="submit"
          theme="solid"
          type="primary"
          loading={isSaving}
          disabled={isLoading || loadFailed}
        >
          保存设置
        </Button>
      </form>
      {isLoading && <p className="form-message">正在读取当前设置…</p>}
    </section>
  )
}

export default RuntimeSettingsSection
