<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue'
import Icon from './Icon.vue'

const props = defineProps({ title: String, width: { type: String, default: '480px' } })
const emit = defineEmits(['close'])
const root = ref(null)

function onKey(e) {
  if (e.key === 'Escape') emit('close')
}
onMounted(() => document.addEventListener('keydown', onKey))

// 父组件用 v-if 直接移除弹窗时，Vue 来不及播放离场动画。
// 这里在卸载前复制一份静态快照，让快照播放“下沉淡出”后再移除，所有弹窗自动获得离场动画。
onBeforeUnmount(() => {
  document.removeEventListener('keydown', onKey)
  const el = root.value
  if (!el || matchMedia('(prefers-reduced-motion: reduce)').matches) return
  const ghost = el.cloneNode(true)
  ghost.classList.add('ghost')
  // 快照里不能留会加载资源的元素（例如视频）
  const originals = el.querySelectorAll('video, audio, iframe')
  ghost.querySelectorAll('video, audio, iframe').forEach((m, i) => {
    const ph = document.createElement('div')
    ph.style.cssText = `width:${originals[i]?.offsetWidth || 0}px;height:${originals[i]?.offsetHeight || 0}px;background:#000;border-radius:6px`
    m.replaceWith(ph)
  })
  document.body.appendChild(ghost)
  setTimeout(() => ghost.remove(), 320)
})
</script>

<template>
  <Teleport to="body">
    <div ref="root" class="overlay" @mousedown.self="emit('close')">
      <div class="dialog" :style="{ width: props.width }" role="dialog">
        <header>
          <h3>{{ title }}</h3>
          <button class="icon-btn" @click="emit('close')"><Icon name="close" /></button>
        </header>
        <div class="body"><slot /></div>
        <footer v-if="$slots.footer"><slot name="footer" /></footer>
      </div>
    </div>
  </Teleport>
</template>

<style>
/* 不加 scoped：弹窗被 Teleport 到 body，离场快照也需要这些样式 */
.overlay { position: fixed; inset: 0; z-index: 100; background: rgba(0,0,0,.5); display: flex; align-items: center; justify-content: center; padding: 16px;
  backdrop-filter: blur(6px); -webkit-backdrop-filter: blur(6px); animation: overlay-in .3s var(--ease-out) both; }
.overlay > .dialog { max-width: 100%; max-height: calc(100vh - 32px); display: flex; flex-direction: column; background: rgba(34,34,34,.92);
  border: 0.5px solid rgba(255,255,255,.1); border-radius: 16px; box-shadow: 0 30px 80px rgba(0,0,0,.6);
  backdrop-filter: saturate(180%) blur(30px); -webkit-backdrop-filter: saturate(180%) blur(30px);
  animation: sheet-in .55s var(--spring-bouncy) both; }
.overlay > .dialog > header { display: flex; align-items: center; justify-content: space-between; padding: 14px 14px 6px 22px; }
.overlay > .dialog h3 { font-size: 18px; }
.overlay > .dialog > .body { padding: 10px 22px 18px; overflow-y: auto; }
.overlay > .dialog > footer { display: flex; justify-content: flex-end; gap: 10px; padding: 12px 22px 18px; border-top: 0.5px solid rgba(255,255,255,.08); }
/* 离场快照 */
.overlay.ghost { pointer-events: none; animation: overlay-out .28s ease-in both; }
.overlay.ghost > .dialog { animation: sheet-out .28s ease-in both; }
@keyframes overlay-in { from { background-color: rgba(0,0,0,0); backdrop-filter: blur(0); -webkit-backdrop-filter: blur(0); } }
@keyframes overlay-out { to { background-color: rgba(0,0,0,0); backdrop-filter: blur(0); -webkit-backdrop-filter: blur(0); } }
@keyframes sheet-in { from { opacity: 0; transform: translate3d(0, 28px, 0) scale(.94); } }
@keyframes sheet-out { to { opacity: 0; transform: translate3d(0, 18px, 0) scale(.96); } }
</style>
