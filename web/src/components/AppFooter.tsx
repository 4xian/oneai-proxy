import { useEffect, useState } from 'react'

// 单独维护页脚时钟，避免时间变化触发整个应用路由树更新。
function AppFooter() {
  const [now, setNow] = useState(() => new Date())

  useEffect(() => {
    const timer = window.setInterval(() => setNow(new Date()), 1000)
    return () => window.clearInterval(timer)
  }, [])

  return (
    <footer className="footer-note app-footer">
      OneAI Proxy <span>·</span> {now.toLocaleString('zh-CN')} <span>·</span> Local by design
    </footer>
  )
}

export default AppFooter
