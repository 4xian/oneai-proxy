import { useState, type ChangeEvent } from 'react'
import { Button, Input } from '@douyinfe/semi-ui-19'
import { IconDownload, IconEyeOpened, IconTick, IconUpload } from '@douyinfe/semi-icons'
import {
  adminError,
  adminFetch,
  setAdminToken,
  setPendingAdminToken,
  setProxyToken,
} from '../../../api'
import { showErrorToast, showSuccessToast, showWarningToast } from '../../../notifications'
import type { MigrationPreview } from '../settings-types'

type Props = {
  onImported?: () => Promise<boolean> | boolean | void
}

// 渲染配置导出、导入、迁移包和备份恢复。
function DataSettingsSection({ onImported }: Props) {
  const [exportPassword, setExportPassword] = useState('')
  const [importFile, setImportFile] = useState<File | null>(null)
  const [importPreview, setImportPreview] = useState<{
    current: Record<string, number>
    incoming: Record<string, number>
  } | null>(null)
  const [migrationPassword, setMigrationPassword] = useState('')
  const [migrationFile, setMigrationFile] = useState<File | null>(null)
  const [migrationPreview, setMigrationPreview] = useState<MigrationPreview | null>(null)
  const [migrationAction, setMigrationAction] = useState<'export' | 'preview' | 'import' | null>(
    null,
  )
  const [migrationRestartRequired, setMigrationRestartRequired] = useState(false)
  const [manifestPath, setManifestPath] = useState('')
  const [manifestVerified, setManifestVerified] = useState(false)

  // 导入或迁移成功后刷新其他设置分区。
  async function refreshAfterImport(): Promise<boolean> {
    if (!onImported) return true
    const result = await onImported()
    return result !== false
  }

  // 导出安全配置或口令保护的完整配置文件。
  async function exportConfig(mode: 'safe' | 'complete_encrypted'): Promise<void> {
    if (mode === 'complete_encrypted' && !exportPassword.trim()) {
      showWarningToast('完整导出需要输入口令。')
      return
    }
    const response = await adminFetch('/api/admin/v1/config/export', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ mode, password: exportPassword }),
    })
    if (!response.ok) {
      showErrorToast((await adminError(response, '导出配置失败')).message)
      return
    }
    const blob = await response.blob()
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = mode === 'safe' ? 'oneai-proxy-safe.json' : 'oneai-proxy-complete.json'
    anchor.click()
    URL.revokeObjectURL(url)
    showSuccessToast('配置文件已生成。')
  }

  // 读取用户选择的导入文件并清除旧预览。
  function handleImportFile(event: ChangeEvent<HTMLInputElement>): void {
    setImportFile(event.target.files?.[0] ?? null)
    setImportPreview(null)
    event.target.value = ''
  }

  // 预览导入配置数量差异，不会写入当前配置。
  async function previewImport(): Promise<void> {
    if (!importFile) {
      showWarningToast('请先选择配置文件。')
      return
    }
    const body = await importFile.text()
    const headers = new Headers({ 'Content-Type': 'application/json' })
    if (exportPassword.trim()) headers.set('X-OneAI-Export-Password', exportPassword)
    const response = await adminFetch('/api/admin/v1/config/preview', {
      method: 'POST',
      headers,
      body,
    })
    const result = (await response.json()) as {
      error?: string
      current?: Record<string, number>
      incoming?: Record<string, number>
    }
    if (!response.ok) {
      showErrorToast(result.error ?? '预览导入失败')
      return
    }
    setImportPreview({
      current: result.current ?? {},
      incoming: result.incoming ?? {},
    })
    showSuccessToast('预览完成，确认差异后再导入。')
  }

  // 在预览后导入配置；服务端会先备份并以事务替换。
  async function importConfig(): Promise<void> {
    if (!importFile) {
      showWarningToast('请先选择配置文件。')
      return
    }
    if (!importPreview) {
      showWarningToast('请先预览差异后再导入。')
      return
    }
    if (!window.confirm('导入将覆盖当前渠道、路由和模型映射，是否继续？')) return
    const body = await importFile.text()
    const headers = new Headers({ 'Content-Type': 'application/json' })
    if (exportPassword.trim()) headers.set('X-OneAI-Export-Password', exportPassword)
    headers.set('X-OneAI-Import-Confirm', 'true')
    const response = await adminFetch('/api/admin/v1/config/import', {
      method: 'POST',
      headers,
      body,
    })
    if (!response.ok) {
      showErrorToast((await adminError(response, '导入配置失败')).message)
      return
    }
    const result = (await response.json()) as {
      adminToken?: string
      proxyToken?: string
    }
    if (result.adminToken) {
      setAdminToken(result.adminToken)
      window.dispatchEvent(new Event('oneai-proxy-admin-token-changed'))
    }
    if (result.proxyToken) setProxyToken(result.proxyToken)
    const refreshed = await refreshAfterImport()
    setImportPreview(null)
    if (refreshed) showSuccessToast('配置已导入，重启应用后加载新的监听设置。')
    else showWarningToast('配置已导入，但设置表单刷新失败；刷新页面后再修改设置。')
  }

  // 使用一次性口令生成跨平台单文件迁移包。
  async function exportMigrationPackage(): Promise<void> {
    if (!migrationPassword.trim()) {
      showWarningToast('导出迁移包需要输入口令。')
      return
    }
    setMigrationAction('export')
    try {
      const response = await adminFetch('/api/admin/v1/migration/export', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: migrationPassword }),
      })
      if (!response.ok) throw await adminError(response, '导出迁移包失败')
      const blob = await response.blob()
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = 'oneai-proxy.oneai-migrate'
      anchor.click()
      URL.revokeObjectURL(url)
      showSuccessToast('迁移包已生成。')
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '导出迁移包失败')
    } finally {
      setMigrationAction(null)
    }
  }

  // 读取迁移包文件并清除上一次预览和重启状态。
  function handleMigrationFile(event: ChangeEvent<HTMLInputElement>): void {
    setMigrationFile(event.target.files?.[0] ?? null)
    setMigrationPreview(null)
    setMigrationRestartRequired(false)
    event.target.value = ''
  }

  // 解密并校验迁移包，只展示不含秘密的替换预览。
  async function previewMigrationPackage(): Promise<void> {
    if (!migrationFile || !migrationPassword.trim()) {
      showWarningToast('请选择迁移包并输入口令。')
      return
    }
    setMigrationAction('preview')
    try {
      const response = await adminFetch('/api/admin/v1/migration/preview', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/vnd.oneai-proxy.migration+json',
          'X-OneAI-Migration-Password': migrationPassword,
        },
        body: await migrationFile.text(),
      })
      if (!response.ok) throw await adminError(response, '预览迁移包失败')
      setMigrationPreview((await response.json()) as MigrationPreview)
      showSuccessToast('迁移包校验通过，请确认替换范围。')
    } catch (error) {
      setMigrationPreview(null)
      showErrorToast(error instanceof Error ? error.message : '预览迁移包失败')
    } finally {
      setMigrationAction(null)
    }
  }

  // 在用户显式确认后导入迁移包，并保存重启后使用的新令牌。
  async function importMigrationPackage(): Promise<void> {
    if (!migrationFile || !migrationPreview || !migrationPassword.trim()) {
      showWarningToast('请先完成迁移包预览。')
      return
    }
    if (!window.confirm('迁移将替换目标机的可迁移配置，但保留日志和正文，是否继续？')) return
    setMigrationAction('import')
    try {
      const response = await adminFetch('/api/admin/v1/migration/import', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/vnd.oneai-proxy.migration+json',
          'X-OneAI-Migration-Password': migrationPassword,
          'X-OneAI-Migration-Confirm': 'true',
        },
        body: await migrationFile.text(),
      })
      if (!response.ok) throw await adminError(response, '导入迁移包失败')
      const result = (await response.json()) as {
        adminToken?: string
        proxyToken?: string
        cleanupPendingCount?: number
      }
      if (result.adminToken) setPendingAdminToken(result.adminToken)
      if (result.proxyToken) setProxyToken(result.proxyToken)
      const refreshed = await refreshAfterImport()
      setMigrationRestartRequired(true)
      setMigrationPreview(null)
      if (refreshed)
        showSuccessToast(
          result.cleanupPendingCount
            ? `迁移完成，${result.cleanupPendingCount} 个旧秘密待系统后续清理；请重启应用。`
            : '迁移完成，请重启应用加载新监听和令牌。',
        )
      else showWarningToast('迁移已完成，但设置表单刷新失败；重启并刷新页面后再修改设置。')
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '导入迁移包失败')
    } finally {
      setMigrationAction(null)
    }
  }

  // 创建包含 SQLite 快照和加密正文清单的本地备份。
  async function backupConfig(): Promise<void> {
    const response = await adminFetch('/api/admin/v1/config/backup', {
      method: 'POST',
    })
    if (!response.ok) {
      showErrorToast((await adminError(response, '创建备份失败')).message)
      return
    }
    const result = (await response.json()) as { manifestPath?: string }
    showSuccessToast(`备份已创建：${result.manifestPath ?? '已保存到数据目录'}`)
  }

  // 下载不含秘密和正文的诊断摘要。
  async function exportDiagnostics(): Promise<void> {
    const response = await adminFetch('/api/admin/v1/config/diagnostics/export')
    if (!response.ok) {
      showErrorToast((await adminError(response, '导出诊断摘要失败')).message)
      return
    }
    const blob = await response.blob()
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = 'oneai-proxy-diagnostics.zip'
    anchor.click()
    URL.revokeObjectURL(url)
    showSuccessToast('诊断包已下载。')
  }

  // 校验完整备份清单，不会修改当前配置。
  async function verifyBackup(): Promise<void> {
    setManifestVerified(false)
    if (!manifestPath.trim()) {
      showWarningToast('请先填写备份清单路径。')
      return
    }
    const response = await adminFetch('/api/admin/v1/config/backup/verify', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ manifestPath: manifestPath.trim() }),
    })
    if (!response.ok) {
      showErrorToast((await adminError(response, '校验备份失败')).message)
      return
    }
    const result = (await response.json()) as { contentBlobCount?: number }
    setManifestVerified(true)
    showSuccessToast(`备份校验通过，正文文件 ${result.contentBlobCount ?? 0} 个。`)
  }

  // 在校验通过且用户显式确认后恢复完整备份。
  async function restoreBackup(): Promise<void> {
    if (!manifestVerified || !manifestPath.trim()) {
      showWarningToast('请先校验备份清单。')
      return
    }
    if (!window.confirm('恢复将替换当前配置和加密正文，是否继续？')) return
    const response = await adminFetch('/api/admin/v1/config/backup/restore', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        'X-OneAI-Restore-Confirm': 'true',
      },
      body: JSON.stringify({ manifestPath: manifestPath.trim() }),
    })
    if (!response.ok) {
      showErrorToast((await adminError(response, '恢复备份失败')).message)
      return
    }
    setManifestVerified(false)
    showSuccessToast('备份已恢复，重启应用后加载恢复的监听设置。')
  }

  return (
    <section
      className="settings-panel settings-section data-settings-section"
      aria-labelledby="data-title"
    >
      <div className="section-intro">
        <div>
          <p className="eyebrow">DATA MANAGEMENT</p>
          <h2 id="data-title">配置与恢复</h2>
        </div>
        <p className="section-note">
          safe 导出不含密钥；完整导出使用口令加密，导入前会先显示数量差异。
        </p>
      </div>
      <div className="migration-workflow">
        <div className="migration-heading">
          <div>
            <h3>跨平台迁移包</h3>
            <p>完整替换可迁移配置，目标机已有日志、统计、审计和正文保持不变。</p>
          </div>
        </div>
        <div className="migration-controls">
          <label className="data-password">
            <span>迁移包口令</span>
            <Input
              type="password"
              value={migrationPassword}
              onChange={(value) => {
                setMigrationPassword(value)
                setMigrationPreview(null)
              }}
              autoComplete="new-password"
              placeholder="仅用于本次迁移包"
            />
          </label>
          <div className="data-row-actions">
            <Button
              icon={<IconDownload />}
              loading={migrationAction === 'export'}
              disabled={migrationAction !== null && migrationAction !== 'export'}
              onClick={() => void exportMigrationPackage()}
            >
              导出迁移包
            </Button>
          </div>
        </div>
        <div className="migration-controls migration-file-row">
          <div className="data-field">
            <span>目标机迁移包</span>
            <div className="file-picker-control">
              <Input readOnly value={migrationFile?.name ?? ''} placeholder="未选择任何文件" />
              <span className="file-picker-button">
                <input
                  className="file-picker-input"
                  type="file"
                  accept=".oneai-migrate,application/vnd.oneai-proxy.migration+json,application/json"
                  disabled={migrationAction !== null}
                  onChange={handleMigrationFile}
                />
                选择文件
              </span>
            </div>
          </div>
          <div className="data-row-actions">
            <Button
              icon={<IconEyeOpened />}
              loading={migrationAction === 'preview'}
              disabled={!migrationFile || !migrationPassword.trim() || migrationAction !== null}
              onClick={() => void previewMigrationPackage()}
            >
              预览替换
            </Button>
            <Button
              icon={<IconUpload />}
              type="primary"
              loading={migrationAction === 'import'}
              disabled={!migrationPreview || migrationAction !== null}
              onClick={() => void importMigrationPackage()}
            >
              确认迁移
            </Button>
          </div>
        </div>
        {migrationPreview && (
          <div className="migration-preview" aria-live="polite">
            <div className="migration-counts">
              <span>
                渠道 {migrationPreview.current.channels ?? 0} →{' '}
                {migrationPreview.incoming.channels ?? 0}
              </span>
              <span>
                模型 {migrationPreview.current.modelCatalog ?? 0} →{' '}
                {migrationPreview.incoming.modelCatalog ?? 0}
              </span>
              <span>
                渠道模型 {migrationPreview.current.channelModels ?? 0} →{' '}
                {migrationPreview.incoming.channelModels ?? 0}
              </span>
              <span>
                映射 {migrationPreview.current.globalModelMappings ?? 0} →{' '}
                {migrationPreview.incoming.globalModelMappings ?? 0}
              </span>
              <span>
                渠道映射 {migrationPreview.current.channelModelMappings ?? 0} →{' '}
                {migrationPreview.incoming.channelModelMappings ?? 0}
              </span>
              <span>
                探针 {migrationPreview.current.probePolicies ?? 0} →{' '}
                {migrationPreview.incoming.probePolicies ?? 0}
              </span>
            </div>
            <p>
              新监听：{migrationPreview.settings.proxyListener.host}:
              {migrationPreview.settings.proxyListener.port} /{' '}
              {migrationPreview.settings.adminListener.host}:
              {migrationPreview.settings.adminListener.port}；时区{' '}
              {migrationPreview.settings.timezone}；管理和代理令牌均已包含。
            </p>
            <div className="migration-channel-list">
              {migrationPreview.channels.slice(0, 12).map((channel) => (
                <span key={channel.id}>
                  {channel.name} · {channel.protocol} ·{' '}
                  {channel.credentialPresent ? '含凭证' : '无凭证'}
                </span>
              ))}
              {migrationPreview.channels.length > 12 && (
                <span>另有 {migrationPreview.channels.length - 12} 个渠道</span>
              )}
            </div>
          </div>
        )}
        {migrationAction && (
          <p className="form-message" aria-live="polite">
            {migrationAction === 'export'
              ? '正在生成迁移包…'
              : migrationAction === 'preview'
                ? '正在解密并校验迁移包…'
                : '正在创建快照并替换配置…'}
          </p>
        )}
        {migrationRestartRequired && (
          <p className="migration-restart-state" role="status">
            <IconTick />
            迁移已完成。请重启应用后继续使用，新监听和令牌将在重启后同时生效。
          </p>
        )}
      </div>
      <form className="data-actions" onSubmit={(event) => event.preventDefault()}>
        <label className="data-password">
          <span>完整导出口令</span>
          <Input
            type="password"
            value={exportPassword}
            onChange={setExportPassword}
            autoComplete="new-password"
            placeholder="仅用于本次加密"
          />
        </label>
        <div className="data-row-actions">
          <Button htmlType="button" onClick={() => void exportConfig('safe')}>
            导出安全配置
          </Button>
          <Button htmlType="button" onClick={() => void exportConfig('complete_encrypted')}>
            导出完整配置
          </Button>
          <Button htmlType="button" onClick={() => void backupConfig()}>
            创建完整备份
          </Button>
          <Button htmlType="button" onClick={() => void exportDiagnostics()}>
            导出诊断摘要
          </Button>
        </div>
      </form>
      <div className="data-import">
        <div className="data-field">
          <span>导入配置文件</span>
          <div className="file-picker-control">
            <Input readOnly value={importFile?.name ?? ''} placeholder="未选择任何文件" />
            <span className="file-picker-button">
              <input
                className="file-picker-input"
                type="file"
                accept="application/json,.json"
                onChange={handleImportFile}
              />
              选择文件
            </span>
          </div>
        </div>
        <div className="data-row-actions">
          <Button onClick={() => void previewImport()} disabled={!importFile}>
            预览差异
          </Button>
          <Button
            type="primary"
            onClick={() => void importConfig()}
            disabled={!importFile || !importPreview}
          >
            确认导入
          </Button>
        </div>
      </div>
      <div className="data-import backup-restore">
        <label className="data-password">
          <span>完整备份清单路径</span>
          <Input
            value={manifestPath}
            onChange={(value) => {
              setManifestPath(value)
              setManifestVerified(false)
            }}
            placeholder="例如：数据目录/backups/manifest-....json"
          />
        </label>
        <div className="data-row-actions">
          <Button onClick={() => void verifyBackup()} disabled={!manifestPath.trim()}>
            校验备份
          </Button>
          <Button type="danger" onClick={() => void restoreBackup()} disabled={!manifestVerified}>
            确认恢复
          </Button>
        </div>
      </div>
      {importPreview && (
        <p className="form-message">
          当前渠道 {importPreview.current.channels ?? 0} → 导入{' '}
          {importPreview.incoming.channels ?? 0}。
        </p>
      )}
    </section>
  )
}

export default DataSettingsSection
