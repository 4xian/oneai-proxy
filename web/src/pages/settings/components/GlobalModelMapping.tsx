import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, Card, TextArea } from '@douyinfe/semi-ui-19'
import { adminError, adminFetch } from '../../../api'
import { showErrorToast, showSuccessToast, showWarningToast } from '../../../notifications'
import type { SettingsSectionProps } from '../settings-types'

type MappingFamily = 'openai' | 'anthropic'
type LoadStatus = 'loading' | 'ready' | 'error'

type FamilyDraft = {
  text: string
  revision: number
  baseline: string
  saving: boolean
}

const emptyDraft: FamilyDraft = { text: '{}', revision: 0, baseline: '{}', saving: false }

// 将映射对象格式化为可编辑 JSON 文本。
function formatMappingJSON(value: Record<string, string> | undefined): string {
  return JSON.stringify(value ?? {}, null, 2)
}

// 渲染设置中的全局模型映射编辑器。
function GlobalModelMapping({ active = true, reloadToken = 0 }: SettingsSectionProps) {
  const [loadStatus, setLoadStatus] = useState<LoadStatus>('loading')
  const [syncNotice, setSyncNotice] = useState('')
  const [openai, setOpenai] = useState<FamilyDraft>(emptyDraft)
  const [anthropic, setAnthropic] = useState<FamilyDraft>(emptyDraft)
  const sessionRef = useRef(0)
  const loadSeqRef = useRef(0)
  const wasActiveRef = useRef(false)
  const savingRef = useRef<Record<MappingFamily, boolean>>({ openai: false, anthropic: false })
  const draftsRef = useRef<{ openai: FamilyDraft; anthropic: FamilyDraft }>({
    openai: emptyDraft,
    anthropic: emptyDraft,
  })

  // 按协议族读取当前草稿。
  function familyDraft(family: MappingFamily): FamilyDraft {
    return family === 'openai' ? openai : anthropic
  }

  // 更新指定协议族草稿，并同步保存回包要用的会话快照。
  function setFamilyDraft(family: MappingFamily, next: FamilyDraft): void {
    draftsRef.current = { ...draftsRef.current, [family]: next }
    if (family === 'openai') setOpenai(next)
    else setAnthropic(next)
  }

  // 从服务端读取两个协议族的全局模型映射。
  const loadMappings = useCallback(async (notifyError = true): Promise<boolean> => {
    const session = sessionRef.current
    const seq = ++loadSeqRef.current
    setLoadStatus((current) => (current === 'ready' ? current : 'loading'))
    try {
      const response = await adminFetch('/api/admin/v1/models/global-mappings')
      if (!response.ok) throw await adminError(response, '读取全局模型映射失败')
      const result = (await response.json()) as {
        globalModelMappings: { openai: Record<string, string>; anthropic: Record<string, string> }
      }
      if (sessionRef.current !== session || loadSeqRef.current !== seq) return false
      const nextOpenAI = formatMappingJSON(result.globalModelMappings.openai)
      const nextAnthropic = formatMappingJSON(result.globalModelMappings.anthropic)
      setFamilyDraft(
        'openai',
        draftsRef.current.openai.revision === 0
          ? {
              text: nextOpenAI,
              revision: 0,
              baseline: nextOpenAI,
              saving: draftsRef.current.openai.saving,
            }
          : { ...draftsRef.current.openai, baseline: nextOpenAI },
      )
      setFamilyDraft(
        'anthropic',
        draftsRef.current.anthropic.revision === 0
          ? {
              text: nextAnthropic,
              revision: 0,
              baseline: nextAnthropic,
              saving: draftsRef.current.anthropic.saving,
            }
          : { ...draftsRef.current.anthropic, baseline: nextAnthropic },
      )
      setLoadStatus('ready')
      if (draftsRef.current.openai.revision > 0 || draftsRef.current.anthropic.revision > 0) {
        setSyncNotice('服务器数据可能已变化，已保留当前未保存输入。')
      } else {
        setSyncNotice('')
      }
      return true
    } catch (reason) {
      if (sessionRef.current !== session || loadSeqRef.current !== seq) return false
      setLoadStatus((current) => {
        if (current === 'ready') return current
        return 'error'
      })
      setSyncNotice((currentNotice) => {
        if (
          draftsRef.current.openai.revision > 0 ||
          draftsRef.current.anthropic.revision > 0 ||
          currentNotice
        )
          return '刷新未成功，已保留当前输入。'
        return currentNotice
      })
      if (notifyError)
        showErrorToast(reason instanceof Error ? reason.message : '读取全局模型映射失败')
      return false
    }
  }, [])

  useEffect(() => {
    if (active && !wasActiveRef.current) void loadMappings()
    wasActiveRef.current = active
  }, [active, loadMappings])

  useEffect(() => {
    if (reloadToken > 0) void loadMappings()
  }, [loadMappings, reloadToken])

  // 记录输入并递增对应协议族的编辑版本。
  function updateDraft(family: MappingFamily, value: string): void {
    setFamilyDraft(family, {
      ...familyDraft(family),
      text: value,
      revision: familyDraft(family).revision + 1,
    })
  }

  // 校验并保存一个协议族的扁平 JSON 映射。
  async function saveMapping(family: MappingFamily): Promise<void> {
    if (loadStatus !== 'ready') {
      showWarningToast('全局映射尚未加载完成，暂不能保存。')
      return
    }
    if (savingRef.current[family]) return
    const snapshot = familyDraft(family)
    const mappingJSON = snapshot.text.trim() || '{}'
    try {
      const parsed = JSON.parse(mappingJSON) as unknown
      if (!parsed || Array.isArray(parsed) || typeof parsed !== 'object') throw new Error('invalid')
    } catch {
      showWarningToast('映射必须是有效 JSON 对象，例如 {"modelA":"modelB"}。')
      return
    }
    const session = sessionRef.current
    savingRef.current[family] = true
    setFamilyDraft(family, { ...snapshot, saving: true })
    try {
      const response = await adminFetch('/api/admin/v1/models/global-mappings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: `{"protocolFamily":${JSON.stringify(family)},"mapping":${mappingJSON}}`,
      })
      if (!response.ok) throw await adminError(response, '保存全局映射失败')
      if (sessionRef.current !== session) return
      const formatted = formatMappingJSON(JSON.parse(mappingJSON) as Record<string, string>)
      const current = draftsRef.current[family]
      if (current.revision === snapshot.revision) {
        setFamilyDraft(family, { text: formatted, revision: 0, baseline: formatted, saving: false })
      } else {
        setFamilyDraft(family, { ...current, baseline: formatted, saving: false })
        showWarningToast('已保存较早快照，当前输入仍有未保存修改。')
      }
      showSuccessToast(`${family === 'openai' ? 'OpenAI' : 'Anthropic'} 全局映射已保存。`)
    } catch (reason) {
      if (sessionRef.current !== session) return
      setFamilyDraft(family, { ...draftsRef.current[family], saving: false })
      showErrorToast(reason instanceof Error ? reason.message : '保存全局映射失败')
    } finally {
      savingRef.current[family] = false
    }
  }

  const ready = loadStatus === 'ready'
  const loadFailed = loadStatus === 'error'
  const loading = loadStatus === 'loading'

  return (
    <section className="settings-panel settings-section mapping-settings-section">
      <div className="section-intro">
        <div>
          <p className="eyebrow">GLOBAL MODEL MAPPING</p>
          <h2>全局模型映射</h2>
        </div>
        <p className="section-note">按协议族将客户端模型解析为逻辑模型，只执行一次映射。</p>
      </div>
      {loading && <p className="form-message">正在读取全局模型映射…</p>}
      {loadFailed && (
        <div className="form-message">
          全局映射读取失败，当前不能保存空配置。
          <Button theme="light" onClick={() => void loadMappings()}>
            重新读取
          </Button>
        </div>
      )}
      {syncNotice && <p className="form-message">{syncNotice}</p>}
      <div className="mapping-editors">
        <Card className="mapping-editor-card" bordered={false}>
          <div className="mapping-editor-heading">
            <div>
              <strong>OpenAI</strong>
              <span>Chat Completions 与 Responses 共用</span>
            </div>
            <Button
              theme="solid"
              type="primary"
              loading={openai.saving}
              disabled={!ready}
              onClick={() => void saveMapping('openai')}
            >
              保存映射
            </Button>
          </div>
          <TextArea
            id="settings-openai-mapping"
            className="mapping-textarea"
            value={openai.text}
            onChange={(value) => updateDraft('openai', value)}
            rows={12}
            disabled={!ready}
          />
        </Card>
        <Card className="mapping-editor-card" bordered={false}>
          <div className="mapping-editor-heading">
            <div>
              <strong>Anthropic</strong>
              <span>Messages 协议</span>
            </div>
            <Button
              theme="solid"
              type="primary"
              loading={anthropic.saving}
              disabled={!ready}
              onClick={() => void saveMapping('anthropic')}
            >
              保存映射
            </Button>
          </div>
          <TextArea
            id="settings-anthropic-mapping"
            className="mapping-textarea"
            value={anthropic.text}
            onChange={(value) => updateDraft('anthropic', value)}
            rows={12}
            disabled={!ready}
          />
        </Card>
      </div>
    </section>
  )
}

export default GlobalModelMapping
