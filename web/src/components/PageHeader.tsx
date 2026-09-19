import { Button } from '@douyinfe/semi-ui-19'
import { IconDesktop, IconMoon, IconSun } from '@douyinfe/semi-icons'

export type AppTheme = 'light' | 'dark'
export type ThemePreference = AppTheme | 'system'
export type PageKey =
  | 'overview'
  | 'channels'
  | 'models'
  | 'requests'
  | 'logs'
  | 'settings'

type PageHeaderProps = {
  active: PageKey
  theme: AppTheme
  themePreference: ThemePreference
  onChangeTheme: () => void
}

// 渲染所有管理页面共享的导航、状态和主题控制。
function PageHeader({ active, themePreference, onChangeTheme }: PageHeaderProps) {
  const themeIcon =
    themePreference === 'light' ? (
      <IconMoon />
    ) : themePreference === 'dark' ? (
      <IconSun />
    ) : (
      <IconDesktop />
    )
  const themeLabel =
    themePreference === 'light'
      ? '切换暗色主题'
      : themePreference === 'dark'
        ? '切换为跟随系统主题'
        : '切换浅色主题'
  return (
    <header className="topbar glass-surface apple-glass">
      <a className="brand" href="#overview" aria-label="OneAI Proxy 首页">
        <span className="brand-mark" aria-hidden="true">
          O
        </span>
        <span>OneAI Proxy</span>
      </a>
      <nav className="topbar-nav" aria-label="主导航">
        <a
          className={`nav-link ${active === 'overview' ? 'active' : ''}`}
          href="#overview"
          aria-current={active === 'overview' ? 'page' : undefined}
        >
          概览
        </a>
        <a
          className={`nav-link ${active === 'channels' ? 'active' : ''}`}
          href="#channels"
          aria-current={active === 'channels' ? 'page' : undefined}
        >
          渠道
        </a>
        <a
          className={`nav-link ${active === 'models' ? 'active' : ''}`}
          href="#models"
          aria-current={active === 'models' ? 'page' : undefined}
        >
          模型
        </a>
        <a
          className={`nav-link ${active === 'requests' ? 'active' : ''}`}
          href="#requests"
          aria-current={active === 'requests' ? 'page' : undefined}
        >
          请求
        </a>
        <a
          className={`nav-link ${active === 'logs' ? 'active' : ''}`}
          href="#logs"
          aria-current={active === 'logs' ? 'page' : undefined}
        >
          日志
        </a>
        <a
          className={`nav-link ${active === 'settings' ? 'active' : ''}`}
          href="#settings"
          aria-current={active === 'settings' ? 'page' : undefined}
        >
          设置
        </a>
      </nav>
      <div className="topbar-tools">
        <Button
          className="theme-button"
          icon={themeIcon}
          theme="borderless"
          aria-label={themeLabel}
          title={themeLabel}
          onClick={onChangeTheme}
        />
      </div>
    </header>
  )
}

export default PageHeader
