import '@douyinfe/semi-ui-19/react19-adapter'
import { createRoot } from 'react-dom/client'
import App from './App'
import './styles/index.css'

// Semi 的静态 Modal 在 React 19 StrictMode 开发渲染下会同步卸载临时根，产生误导性的 React 警告。
// 应用状态仍由页面组件和 hooks 管理，生产构建不依赖 StrictMode 才能保持行为一致。
createRoot(document.getElementById('app')!).render(<App />)
