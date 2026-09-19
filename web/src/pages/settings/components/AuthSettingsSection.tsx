import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, Input } from '@douyinfe/semi-ui-19'
import {
  adminError,
  adminFetch,
  getAdminToken,
  getProxyToken,
  setAdminToken,
  setProxyToken,
} from '../../../api'
import { showErrorToast, showSuccessToast, showWarningToast } from '../../../notifications'
import type { AuthStatus, SettingsSectionProps } from '../settings-types'

// 渲染管理/代理令牌保存和轮换。
function AuthSettingsSection({ active, reloadToken = 0 }: SettingsSectionProps) {
  const [adminToken, setAdminTokenState] = useState(getAdminToken)
  const [proxyToken, setProxyTokenState] = useState(getProxyToken)
  const [authStatus, setAuthStatus] = useState<AuthStatus | null>(null)
  const [isRotating, setIsRotating] = useState(false)
  const wasActiveRef = useRef(false)

  // 读取令牌是否仍为出厂默认值；导入后才同步本地输入框。
  const loadAuthStatus = useCallback(async (syncLocal = false): Promise<void> => {
    if (syncLocal) {
      setAdminTokenState(getAdminToken())
      setProxyTokenState(getProxyToken())
    }
    try {
      const response = await adminFetch('/api/admin/v1/auth')
      if (!response.ok) return
      const result = (await response.json()) as AuthStatus
      setAuthStatus(result)
    } catch {
      // 令牌状态只用于提示，失败不阻断设置页。
    }
  }, [])

  useEffect(() => {
    if (active && !wasActiveRef.current) void loadAuthStatus()
    wasActiveRef.current = active
  }, [active, loadAuthStatus])

  useEffect(() => {
    if (reloadToken > 0) void loadAuthStatus(true)
  }, [loadAuthStatus, reloadToken])

  // 保存令牌并立即用于后续管理 API 请求。
  async function saveAdminToken(): Promise<void> {
    const normalized = adminToken.trim()
    if (!normalized) {
      showWarningToast('管理令牌不能为空。')
      return
    }
    try {
      const response = await adminFetch('/api/admin/v1/auth', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tokenType: 'admin', token: normalized }),
      })
      if (!response.ok) throw await adminError(response, '保存管理令牌失败')
      setAdminToken(normalized)
      setAdminTokenState(normalized)
      window.dispatchEvent(new Event('oneai-proxy-admin-token-changed'))
      showSuccessToast('管理令牌已保存。')
      await loadAuthStatus()
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '保存管理令牌失败')
    }
  }

  // 保存用户指定的代理令牌并立即更新本地使用值。
  async function saveProxyToken(): Promise<void> {
    const normalized = proxyToken.trim()
    if (!normalized) {
      showWarningToast('代理令牌不能为空。')
      return
    }
    try {
      const response = await adminFetch('/api/admin/v1/auth', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tokenType: 'proxy', token: normalized }),
      })
      if (!response.ok) throw await adminError(response, '保存代理令牌失败')
      setProxyToken(normalized)
      setProxyTokenState(normalized)
      showSuccessToast('代理令牌已保存。')
      await loadAuthStatus()
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '保存代理令牌失败')
    }
  }

  // 轮换管理或代理令牌；明文只在本次响应中展示。
  async function rotateToken(tokenType: 'admin' | 'proxy'): Promise<void> {
    const label = tokenType === 'admin' ? '管理' : '代理'
    if (
      !window.confirm(`轮换${label}令牌后旧令牌立即失效，已打开的页面需要使用新令牌。是否继续？`)
    ) {
      return
    }
    setIsRotating(true)
    try {
      const response = await adminFetch('/api/admin/v1/auth', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ tokenType }),
      })
      if (!response.ok) throw await adminError(response, '轮换令牌失败')
      const result = (await response.json()) as { token: string }
      if (tokenType === 'admin') {
        setAdminTokenState(result.token)
        setAdminToken(result.token)
        window.dispatchEvent(new Event('oneai-proxy-admin-token-changed'))
      } else {
        setProxyTokenState(result.token)
        setProxyToken(result.token)
      }
      await loadAuthStatus()
      showSuccessToast(`${label}令牌已轮换，并已填入对应输入框。`)
    } catch (error) {
      showErrorToast(error instanceof Error ? error.message : '轮换令牌失败')
    } finally {
      setIsRotating(false)
    }
  }

  return (
    <section
      className="settings-panel settings-section auth-settings-section"
      aria-labelledby="auth-title"
    >
      <div className="section-intro">
        <div>
          <p className="eyebrow">LOCAL AUTHENTICATION</p>
          <h2 id="auth-title">访问令牌</h2>
        </div>
      </div>
      {authStatus && (!authStatus.adminCustom || !authStatus.proxyCustom) ? (
        <p className="section-note">
          当前仍在使用出厂默认令牌
          {!authStatus.adminCustom && !authStatus.proxyCustom
            ? '（管理和代理）'
            : !authStatus.adminCustom
              ? '（管理）'
              : '（代理）'}
          ，建议立即修改后再对外使用。
        </p>
      ) : null}
      <form
        className="settings-form auth-form"
        onSubmit={(event) => {
          event.preventDefault()
          void saveAdminToken()
        }}
      >
        <label className="field auth-token-field">
          <span>管理令牌</span>
          <Input
            type="password"
            value={adminToken}
            onChange={setAdminTokenState}
            placeholder="本地默认：oneai-local-admin"
            autoComplete="off"
            required
          />
        </label>
        <Button className="save-button" htmlType="submit" theme="solid" type="primary">
          保存令牌
        </Button>
      </form>
      <form
        className="settings-form auth-form"
        onSubmit={(event) => {
          event.preventDefault()
          void saveProxyToken()
        }}
      >
        <label className="field auth-token-field">
          <span>代理令牌</span>
          <Input
            type="password"
            value={proxyToken}
            onChange={setProxyTokenState}
            placeholder="本地默认：oneai-local-proxy"
            autoComplete="off"
            required
          />
        </label>
        <Button className="save-button" htmlType="submit" theme="solid">
          保存代理令牌
        </Button>
      </form>
      <div className="token-actions">
        <Button onClick={() => void rotateToken('admin')} loading={isRotating}>
          轮换管理令牌
        </Button>
        <Button onClick={() => void rotateToken('proxy')} loading={isRotating}>
          轮换代理令牌
        </Button>
      </div>
    </section>
  )
}

export default AuthSettingsSection
