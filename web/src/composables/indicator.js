import { ref, onMounted, onBeforeUnmount, nextTick, watch } from 'vue'

// useIndicator 让一个高亮块（胶囊 / 下划线）在一组选项之间滑动，类似 iOS 的分段控件。
// container：选项所在的容器（需要 position: relative）；selector：当前选中项的选择器；
// deps：变化时重新定位的依赖（例如路由）。
export function useIndicator(container, selector, deps) {
  const style = ref({ opacity: 0 })
  let first = true
  let ro = null

  function update() {
    const c = container.value
    if (!c) return
    const el = c.querySelector(selector)
    if (!el) {
      style.value = { ...style.value, opacity: 0 }
      first = true
      return
    }
    const cr = c.getBoundingClientRect()
    const er = el.getBoundingClientRect()
    style.value = {
      opacity: 1,
      width: er.width + 'px',
      height: er.height + 'px',
      transform: `translate3d(${er.left - cr.left + c.scrollLeft}px, ${er.top - cr.top}px, 0)`,
      // 第一次出现时直接就位，不从左边滑过来
      transition: first ? 'none' : undefined,
    }
    first = false
  }
  const later = () => nextTick(() => requestAnimationFrame(update))

  onMounted(() => {
    later()
    ro = new ResizeObserver(later)
    if (container.value) ro.observe(container.value)
    window.addEventListener('resize', later)
    document.fonts?.ready.then(later)
  })
  onBeforeUnmount(() => {
    ro?.disconnect()
    window.removeEventListener('resize', later)
  })
  if (deps) watch(deps, later, { deep: true })
  return { style, update: later }
}
