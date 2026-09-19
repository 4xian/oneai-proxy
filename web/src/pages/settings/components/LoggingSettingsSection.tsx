import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { Button, InputNumber } from '@douyinfe/semi-ui-19'
import { adminError, adminFetch } from '../../../api'
import { showErrorToast, showSuccessToast, showWarningToast } from '../../../notifications'
import {
  defaultLoggingSettings,
  hasFiniteLoggingNumbers,
  normalizeLoggingSettings,
  parseFiniteNumber,
  type CleanupLogsResponse,
  type LoggingSettings,
  type LoggingSettingsResponse,
  type SettingsSectionProps,
} from '../settings-types'

type LoggingDraft = {
  value: LoggingSettings
  baseline: LoggingSettings
  revision: number
  timezone: string
}

const emptyDraft: LoggingDraft = {
  value: defaultLoggingSettings,
  baseline: defaultLoggingSettings,
  revision: 0,
  timezone: 'Asia/Shanghai',
}

// 渲染请求日志保留期、正文上限和立即清理。
function LoggingSettingsSection({ active, reloadToken = 0 }: SettingsSectionProps) {
  const [draft, setDraft] = useState<LoggingDraft>(emptyDraft)
  const [isLoading, setIsLoading] = useState(true)
  const [loadFailed, setLoadFailed] = useState(false)
  const [isSaving, setIsSaving] = useState(false)
  const [syncNotice, setSyncNotice] = useState('')
  const wasActiveRef = useRef(false)
  const draftRef = useRef(emptyDraft)
  const loadSeqRef = useRef(0)

  // 同步当前草稿快照。
  function updateDraft(next: LoggingDraft): void {
    draftRef.current = next
    setDraft(next)
  }

  // 读取请求日志正文策略、保留期和磁盘限制。
  const loadLoggingSettings = useCallback(async (): Promise<boolean> => {
    const seq = ++loadSeqRef.current
    setIsLoading(true)
    setLoadFailed(false)
    try {
      const response = await adminFetch('/api/admin/v1/logs/settings')
      if (!response.ok) throw await adminError(response, '读取请求日志设置失败')
      const result = (await response.json()) as LoggingSettingsResponse
      if (seq !== loadSeqRef.current) return false
      const logging = normalizeLoggingSettings(result.logging ?? {})
      const timezone = result.timezone || draftRef.current.timezone
      const current = draftRef.current
      if (current.revision > 0) {
        updateDraft({ ...current, baseline: logging, timezone })
        setSyncNotice('服务器数据可能已变化，已保留当前未保存输入。')
      } else {
        updateDraft({ value: logging, baseline: logging, revision: 0, timezone })
        setSyncNotice('')
      }
      return true
    } catch (error) {
      if (seq !== loadSeqRef.current) return false
      setLoadFailed(true)
      showErrorToast(error instanceof Error ? error.message : '读取请求日志设置失败')
      return false
    } finally {
      if (seq === loadSeqRef.current) setIsLoading(false)
    }
  }, [])

  useEffect(() => {
    if (active && !wasActiveRef.current) void loadLoggingSettings()
    wasActiveRef.current = active
  }, [active, loadLoggingSettings])

  useEffect(() => {
    if (reloadToken > 0) void loadLoggingSettings()
  }, [loadLoggingSettings, reloadToken])

  // 更新单个日志数字字段。
  function updateLoggingNumber(field: keyof LoggingSettings, value: number): void {
    const current = draftRef.current
    updateDraft({
      ...current,
      value: { ...current.value, [field]: value },
      revision: current.revision + 1,
    })
  }

  // 保存请求日志设置，后端会校验策略、保留期和配额。
  async function saveLoggingSettings(event: FormEvent<HTMLFormElement>): Promise<void> {
    event.preventDefault()
    if (isLoading || loadFailed) {
      showWarningToast('当前日志设置尚未成功读取，请刷新页面后再保存。')
      return
    }
    if (!hasFiniteLoggingNumbers(draft.value)) {
      showWarningToast('请填写有效的数字配置。')
      return
    }
    const snapshot = draftRef.current
    setIsSaving(true)
    try {
      const response = await adminFetch('/api/admin/v1/logs/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ logging: snapshot.value, timezone: snapshot.timezone }),
      })
      if (!response.ok) throw await adminError(response, '保存请求日志设置失败')
      const result = (await response.json()) as LoggingSettingsResponse
      const logging = normalizeLoggingSettings(result.logging ?? snapshot.value)
      const timezone = result.timezone || snapshot.timezone
      const current = draftRef.current
      if (current.revision === snapshot.revision) {
        updateDraft({ value: logging, baseline: logging, revision: 0, timezone })
        setSyncNotice('')
      } else {
        updateDraft({ ...current, baseline: logging, timezone })
        showWarningToast('已保存较早快照，当前输入仍有未保存修改。')
      }
      showSuccessToast('请求日志设置已保存。')
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '保存请求日志设置失败')
    } finally {
      setIsSaving(false)
    }
  }

  // 在确认后立即清理过期请求、正文和运行日志。
  async function cleanupLogs(): Promise<void> {
    if (!window.confirm('将按当前保留期删除过期请求、正文和运行日志，是否继续？')) return
    const response = await adminFetch('/api/admin/v1/logs/cleanup', { method: 'POST' })
    if (!response.ok && response.status !== 207) {
      showErrorToast((await adminError(response, '清理日志失败')).message)
      return
    }
    const result = (await response.json()) as CleanupLogsResponse
    const summary = `请求 ${result.requests ?? 0} 条，正文 ${result.contentBlobs ?? 0} 份`
    if (response.status === 207 || result.runtimeError) {
      showWarningToast(
        result.message
          ? `${result.message}（${summary}）。`
          : `请求日志已清理（${summary}），运行日志清理失败。`,
      )
      return
    }
    showSuccessToast(`清理完成：${summary}。`)
  }

  const logging = draft.value
  return (
    <section
      className="settings-panel settings-section logging-settings-section"
      aria-labelledby="logging-title"
    >
      <div className="section-intro">
        <div>
          <p className="eyebrow">REQUEST LEDGER</p>
          <h2 id="logging-title">请求日志</h2>
        </div>
        <p className="section-note">
          V1 固定保存四类请求快照，正文以加密文件保存，读取时仅遮罩 API 凭证。
        </p>
      </div>
      {syncNotice ? <p className="form-message">{syncNotice}</p> : null}
      <form className="settings-form logging-form" onSubmit={saveLoggingSettings}>
        <label className="field">
          <span>请求保留天数</span>
          <InputNumber
            value={logging.requestRetentionDays}
            min={1}
            max={3650}
            hideButtons
            onChange={(value) =>
              updateLoggingNumber(
                'requestRetentionDays',
                parseFiniteNumber(value, logging.requestRetentionDays, 30),
              )
            }
          />
        </label>
        <label className="field">
          <span>操作日志保留天数</span>
          <InputNumber
            value={logging.auditRetentionDays}
            min={1}
            max={3650}
            hideButtons
            onChange={(value) =>
              updateLoggingNumber(
                'auditRetentionDays',
                parseFiniteNumber(value, logging.auditRetentionDays, 30),
              )
            }
          />
        </label>
        <label className="field">
          <span>运行日志保留天数</span>
          <InputNumber
            value={logging.runtimeRetentionDays}
            min={1}
            max={3650}
            hideButtons
            onChange={(value) =>
              updateLoggingNumber(
                'runtimeRetentionDays',
                parseFiniteNumber(value, logging.runtimeRetentionDays, 7),
              )
            }
          />
        </label>
        <label className="field">
          <span>请求正文上限（字节）</span>
          <InputNumber
            value={logging.maxRequestContentBytes}
            min={1}
            max={64 << 20}
            hideButtons
            onChange={(value) =>
              updateLoggingNumber(
                'maxRequestContentBytes',
                parseFiniteNumber(value, logging.maxRequestContentBytes, 1048576),
              )
            }
          />
        </label>
        <label className="field">
          <span>响应正文上限（字节）</span>
          <InputNumber
            value={logging.maxResponseContentBytes}
            min={1}
            max={64 << 20}
            hideButtons
            onChange={(value) =>
              updateLoggingNumber(
                'maxResponseContentBytes',
                parseFiniteNumber(value, logging.maxResponseContentBytes, 1048576),
              )
            }
          />
        </label>
        <label className="field">
          <span>正文磁盘配额（字节）</span>
          <InputNumber
            value={logging.diskQuotaBytes}
            min={1}
            max={2 ** 40}
            hideButtons
            onChange={(value) =>
              updateLoggingNumber(
                'diskQuotaBytes',
                parseFiniteNumber(value, logging.diskQuotaBytes, 1073741824),
              )
            }
          />
        </label>
        <Button
          className="save-button"
          htmlType="submit"
          theme="solid"
          type="primary"
          loading={isSaving}
          disabled={isLoading || loadFailed}
        >
          保存日志设置
        </Button>
        <Button type="danger" onClick={() => void cleanupLogs()}>
          立即清理过期日志
        </Button>
      </form>
      {isLoading && <p className="form-message">正在读取请求日志设置…</p>}
    </section>
  )
}

export default LoggingSettingsSection
