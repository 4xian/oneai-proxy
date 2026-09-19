import { Toast } from '@douyinfe/semi-ui-19'

const toastOptions = {
  className: 'apple-toast',
  duration: 3,
  showClose: false,
  textMaxWidth: 360,
  top: 84,
} as const

type ToastKind = 'error' | 'success' | 'warning'

const recentToasts = new Map<ToastKind, { message: string; shownAt: number }>()

// 过滤 StrictMode 或连续回调在短时间内产生的完全重复提示。
function shouldShowToast(kind: ToastKind, message: string): boolean {
  const normalized = message.trim()
  if (!normalized) return false
  const now = Date.now()
  const recent = recentToasts.get(kind)
  if (recent?.message === normalized && now - recent.shownAt < 1000) return false
  recentToasts.set(kind, { message: normalized, shownAt: now })
  return true
}

// 显示统一的错误 Toast。
export function showErrorToast(message: string): void {
  if (shouldShowToast('error', message)) {
    Toast.error({ ...toastOptions, id: 'oneai-error-toast', content: message })
  }
}

// 显示统一的成功 Toast。
export function showSuccessToast(message: string): void {
  if (shouldShowToast('success', message)) {
    Toast.success({ ...toastOptions, id: 'oneai-success-toast', content: message })
  }
}

// 显示统一的前置条件或注意事项 Toast。
export function showWarningToast(message: string): void {
  if (shouldShowToast('warning', message)) {
    Toast.warning({ ...toastOptions, id: 'oneai-warning-toast', content: message })
  }
}
