import { useLayoutEffect, useRef, useState } from 'react'

// 跟随表格容器尺寸计算 Semi Table 所需的确定纵向滚动高度。
export function useTableScrollY(): {
  tableWrapRef: React.RefObject<HTMLDivElement | null>
  tableScrollY: number | undefined
} {
  const tableWrapRef = useRef<HTMLDivElement>(null)
  const [tableScrollY, setTableScrollY] = useState<number>()

  useLayoutEffect(() => {
    const element = tableWrapRef.current
    if (!element) return
    const measure = () => {
      const headerHeight =
        element.querySelector<HTMLElement>('.semi-table-thead')?.getBoundingClientRect().height ?? 0
      setTableScrollY(Math.max(0, Math.floor(element.clientHeight - headerHeight)))
    }
    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

  return { tableWrapRef, tableScrollY }
}
