import { useEffect, useMemo, useState } from 'react'
import PageFrame from './components/PageFrame'
import PageHeader, {
  type AppTheme,
  type PageKey,
  type ThemePreference,
} from './components/PageHeader'
import OverviewPage from './pages/overview/overview'
import ChannelsPage from './pages/channels/channels'
import ChannelEditorPage from './pages/channels/channel-editor'
import LogsPage from './pages/logs/logs'
import RequestsPage from './pages/requests/requests'
import ModelsPage from './pages/models/models'
import SettingsPage from './pages/settings/settings'

type Route = {
  key: string
  page:
    | 'overview'
    | 'channels'
    | 'channel-editor'
    | 'logs'
    | 'requests'
    | 'models'
    | 'settings'
  channelId?: string
  requestId?: string
  settingsSection?: 'runtime' | 'channels' | 'auth' | 'data' | 'mapping' | 'logging'
}

// 根据用户偏好或系统设置确定首次渲染的主题。
function getInitialThemePreference(): ThemePreference {
  const savedTheme = window.localStorage.getItem('oneai-proxy-theme')
  if (savedTheme === 'light' || savedTheme === 'dark' || savedTheme === 'system') return savedTheme
  return 'system'
}

// 将主题偏好解析为页面实际使用的浅色或暗色主题。
function resolveTheme(preference: ThemePreference): AppTheme {
  if (preference !== 'system') return preference
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

// 监听 hash 变化，让管理页面保持无需额外路由依赖的轻量切换。
function useHashPath(): string {
  const [path, setPath] = useState(() => window.location.hash)
  useEffect(() => {
    const handleHashChange = () => setPath(window.location.hash)
    window.addEventListener('hashchange', handleHashChange)
    return () => window.removeEventListener('hashchange', handleHashChange)
  }, [])
  return path
}

// 将 hash 路径转换成页面类型；编辑页保留渠道 ID 作为独立缓存实例。
function parseRoute(path: string): Route {
  if (path === '#channels/new') return { key: 'channels-new', page: 'channel-editor' }
  const editMatch = path.match(/^#channels\/([^/?]+)\/edit(?:\?.*)?$/)
  if (editMatch) {
    const channelId = decodeURIComponent(editMatch[1])
    return { key: `channels-edit-${channelId}`, page: 'channel-editor', channelId }
  }
  const pathname = path.split('?')[0]
  if (pathname === '#channels' || pathname === '#channels/')
    return { key: 'channels', page: 'channels' }
  if (pathname.startsWith('#requests')) {
    const query = path.includes('?') ? path.slice(path.indexOf('?') + 1) : ''
    const requestId = new URLSearchParams(query).get('request') ?? undefined
    return { key: 'requests', page: 'requests', requestId }
  }
  if (pathname.startsWith('#logs')) return { key: 'logs', page: 'logs' }
  if (pathname === '#models' || pathname === '#models/') return { key: 'models', page: 'models' }
  if (pathname.startsWith('#settings')) {
    const section = pathname.split('/')[1]
    const settingsSection =
      section === 'channels' ||
      section === 'auth' ||
      section === 'data' ||
      section === 'mapping' ||
      section === 'logging'
        ? section
        : 'runtime'
    return { key: 'settings', page: 'settings', settingsSection }
  }
  return { key: 'overview', page: 'overview' }
}

// 将内部路由类型映射为顶部菜单的当前项。
function activePageKey(route: Route): PageKey {
  if (route.page === 'channel-editor') return 'channels'
  return route.page
}

// 应用公共壳只挂载一次，页面内容通过缓存路由切换。
function App() {
  const path = useHashPath()
  const route = useMemo(() => parseRoute(path), [path])
  const [themePreference, setThemePreference] = useState<ThemePreference>(getInitialThemePreference)
  const [theme, setTheme] = useState<AppTheme>(() => resolveTheme(getInitialThemePreference()))
  const [visitedRoutes, setVisitedRoutes] = useState<Route[]>(() => [route])

  useEffect(() => {
    setVisitedRoutes((current) => {
      const index = current.findIndex((item) => item.key === route.key)
      if (index === -1) return [...current, route]
      if (
        current[index].channelId === route.channelId &&
        current[index].requestId === route.requestId &&
        current[index].settingsSection === route.settingsSection
      )
        return current
      return current.map((item, itemIndex) => (itemIndex === index ? route : item))
    })
  }, [route])

  // 在浅色、暗色和跟随系统之间循环主题偏好。
  function changeTheme(): void {
    setThemePreference((current) =>
      current === 'light' ? 'dark' : current === 'dark' ? 'system' : 'light',
    )
  }

  useEffect(() => {
    window.localStorage.setItem('oneai-proxy-theme', themePreference)
    const media = window.matchMedia('(prefers-color-scheme: dark)')
    const applyTheme = () => setTheme(resolveTheme(themePreference))
    applyTheme()
    if (themePreference === 'system') {
      media.addEventListener('change', applyTheme)
      return () => media.removeEventListener('change', applyTheme)
    }
  }, [themePreference])

  useEffect(() => {
    document.documentElement.dataset.theme = theme
    if (theme === 'dark') document.body.setAttribute('theme-mode', 'dark')
    else document.body.removeAttribute('theme-mode')
  }, [theme])

  // 根据缓存路由实例渲染中间内容，顶部和底部不参与切换。
  function renderPage(cachedRoute: Route): React.ReactNode {
    const props = { theme, themePreference, onChangeTheme: changeTheme }
    if (cachedRoute.page === 'overview')
      return <OverviewPage {...props} active={cachedRoute.key === route.key} />
    if (cachedRoute.page === 'channels') {
      return <ChannelsPage {...props} active={cachedRoute.key === route.key} />
    }
    if (cachedRoute.page === 'channel-editor') {
      return (
        <ChannelEditorPage
          {...props}
          channelId={cachedRoute.channelId}
          active={cachedRoute.key === route.key}
        />
      )
    }
    if (cachedRoute.page === 'requests') {
      return (
        <RequestsPage
          {...props}
          requestId={cachedRoute.requestId}
          active={cachedRoute.key === route.key}
        />
      )
    }
    if (cachedRoute.page === 'logs') {
      return <LogsPage {...props} active={cachedRoute.key === route.key} />
    }
    if (cachedRoute.page === 'models')
      return <ModelsPage {...props} active={cachedRoute.key === route.key} />
    return (
      <SettingsPage
        {...props}
        settingsSection={cachedRoute.settingsSection}
        active={cachedRoute.key === route.key}
      />
    )
  }

  return (
    <main
      className="page-shell apple-page app-shell"
      data-theme={theme}
      data-page={activePageKey(route)}
    >
      <PageHeader
        active={activePageKey(route)}
        theme={theme}
        themePreference={themePreference}
        onChangeTheme={changeTheme}
      />
      <div className="route-container">
        {visitedRoutes.map((cachedRoute) => (
          <PageFrame
            key={cachedRoute.key}
            active={cachedRoute.key === route.key}
            unmountWhenInactive={cachedRoute.page === 'channel-editor'}
          >
            {renderPage(cachedRoute)}
          </PageFrame>
        ))}
      </div>
      {/* <AppFooter /> */}
    </main>
  )
}

export default App
