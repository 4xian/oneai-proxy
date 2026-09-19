type PageFrameProps = {
  active: boolean
  unmountWhenInactive?: boolean
  children: React.ReactNode
}

// 按当前路由控制页面显示，并按需卸载包含敏感状态的页面。
function PageFrame({ active, unmountWhenInactive = false, children }: PageFrameProps) {
  return (
    <div
      className={`route-view ${active ? 'route-view-active' : ''}`}
      hidden={!active}
      aria-hidden={!active}
    >
      {active || !unmountWhenInactive ? children : null}
    </div>
  )
}

export default PageFrame
