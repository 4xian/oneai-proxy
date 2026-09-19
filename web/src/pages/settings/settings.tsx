import { useCallback, useLayoutEffect, useRef, useState } from 'react'
import { IconArchive, IconBranch, IconKeyStroked, IconServerStroked } from '@douyinfe/semi-icons'
import AuthSettingsSection from './components/AuthSettingsSection'
import ChannelSettingsSection from './components/ChannelSettingsSection'
import DataSettingsSection from './components/DataSettingsSection'
import GlobalModelMapping from './components/GlobalModelMapping'
import LoggingSettingsSection from './components/LoggingSettingsSection'
import RuntimeSettingsSection from './components/RuntimeSettingsSection'
import type { SettingsPageProps, SettingsSection } from './settings-types'

const sectionOrder: SettingsSection[] = [
  'runtime',
  'channels',
  'auth',
  'mapping',
  'logging',
  'data',
]

// 渲染设置页内容，侧栏子路由由上层应用壳负责切换。
function SettingsPage({ settingsSection = 'runtime', active = true }: SettingsPageProps) {
  const [reloadToken, setReloadToken] = useState(0)
  const sectionIndex = sectionOrder.indexOf(settingsSection)
  const previousSectionIndex = useRef(sectionIndex)
  const [animationDirection, setAnimationDirection] = useState<'up' | 'down'>('up')

  useLayoutEffect(() => {
    setAnimationDirection(sectionIndex >= previousSectionIndex.current ? 'up' : 'down')
    previousSectionIndex.current = sectionIndex
  }, [sectionIndex])

  // 导入或迁移成功后通知各分区重新读取，未保存草稿由分区自己保留。
  const refreshSettingsForms = useCallback(async (): Promise<boolean> => {
    setReloadToken((current) => current + 1)
    return true
  }, [])

  return (
    <section className="settings-page page-content">
      <div className="settings-workspace">
        <aside className="settings-sidebar" aria-label="设置菜单">
          <p className="settings-sidebar-title">系统设置</p>
          <nav className="settings-side-nav">
            <a
              className={settingsSection === 'runtime' ? 'active' : ''}
              href="#settings/runtime"
              aria-current={settingsSection === 'runtime' ? 'page' : undefined}
            >
              <IconServerStroked />
              <span>监听设置</span>
            </a>
            <a
              className={settingsSection === 'channels' ? 'active' : ''}
              href="#settings/channels"
              aria-current={settingsSection === 'channels' ? 'page' : undefined}
            >
              <IconServerStroked />
              <span>全局渠道设置</span>
            </a>
            <a
              className={settingsSection === 'auth' ? 'active' : ''}
              href="#settings/auth"
              aria-current={settingsSection === 'auth' ? 'page' : undefined}
            >
              <IconKeyStroked />
              <span>访问令牌</span>
            </a>
            <a
              className={settingsSection === 'mapping' ? 'active' : ''}
              href="#settings/mapping"
              aria-current={settingsSection === 'mapping' ? 'page' : undefined}
            >
              <IconBranch />
              <span>全局模型映射</span>
            </a>
            <a
              className={settingsSection === 'logging' ? 'active' : ''}
              href="#settings/logging"
              aria-current={settingsSection === 'logging' ? 'page' : undefined}
            >
              <IconArchive />
              <span>请求日志</span>
            </a>
            <a
              className={settingsSection === 'data' ? 'active' : ''}
              href="#settings/data"
              aria-current={settingsSection === 'data' ? 'page' : undefined}
            >
              <IconArchive />
              <span>配置与恢复</span>
            </a>
          </nav>
        </aside>
        <div className={`settings-route-content settings-route-${animationDirection}`}>
          <div hidden={settingsSection !== 'runtime'}>
            <RuntimeSettingsSection
              active={active && settingsSection === 'runtime'}
              reloadToken={reloadToken}
            />
          </div>
          <div hidden={settingsSection !== 'channels'}>
            <ChannelSettingsSection
              active={active && settingsSection === 'channels'}
              reloadToken={reloadToken}
            />
          </div>
          <div hidden={settingsSection !== 'auth'}>
            <AuthSettingsSection
              active={active && settingsSection === 'auth'}
              reloadToken={reloadToken}
            />
          </div>
          <div hidden={settingsSection !== 'mapping'}>
            <GlobalModelMapping
              active={active && settingsSection === 'mapping'}
              reloadToken={reloadToken}
            />
          </div>
          <div hidden={settingsSection !== 'logging'}>
            <LoggingSettingsSection
              active={active && settingsSection === 'logging'}
              reloadToken={reloadToken}
            />
          </div>
          <div hidden={settingsSection !== 'data'}>
            <DataSettingsSection onImported={refreshSettingsForms} />
          </div>
        </div>
      </div>
    </section>
  )
}

export default SettingsPage
