<script setup>
import { ref, onMounted, onBeforeUnmount } from 'vue'
import Icon from './Icon.vue'
import MediaCard from './MediaCard.vue'

defineProps({
  title: String,
  to: String,
  items: { type: Array, default: () => [] },
  shape: { type: String, default: 'poster' },
  index: { type: Number, default: 0 }, // 第几行：整行依次出现
})
const el = ref(null)
const atStart = ref(true)
const atEnd = ref(false)

function onScroll() {
  const s = el.value
  if (!s) return
  atStart.value = s.scrollLeft < 4
  atEnd.value = s.scrollLeft + s.clientWidth >= s.scrollWidth - 4
}
function scroll(dir) {
  const s = el.value
  s.scrollBy({ left: dir * s.clientWidth * 0.85, behavior: 'smooth' })
}
let ro
onMounted(() => {
  onScroll()
  ro = new ResizeObserver(onScroll)
  ro.observe(el.value)
})
onBeforeUnmount(() => ro?.disconnect())
</script>

<template>
  <section class="row-sec fade" :style="{ '--i': index }">
    <h2 class="section-title">
      <router-link v-if="to" :to="to" class="title-link">{{ title }} <Icon name="chevronRight" :size="20" class="chev" /></router-link>
      <span v-else>{{ title }}</span>
      <div class="spacer" />
      <button class="icon-btn arrow" :class="{ dim: atStart }" @click="scroll(-1)"><Icon name="chevronLeft" /></button>
      <button class="icon-btn arrow" :class="{ dim: atEnd }" @click="scroll(1)"><Icon name="chevronRight" /></button>
    </h2>
    <div ref="el" class="scroller" :class="shape" @scroll.passive="onScroll">
      <slot>
        <MediaCard v-for="(it, i) in items" :key="it.id" :item="it" :shape="shape" :index="i" />
      </slot>
    </div>
  </section>
</template>

<style scoped>
/* 给悬停上浮和描边留出空间（横向滚动容器会把纵向溢出也裁掉） */
.scroller { padding: 10px 6px 14px; margin: -10px -6px -6px; scroll-padding-left: 6px; }
.scroller.poster { grid-auto-columns: clamp(120px, 13vw, 180px); }
.scroller.thumb { grid-auto-columns: clamp(220px, 24vw, 340px); }
.scroller.tile { grid-auto-columns: clamp(200px, 20vw, 300px); }
.arrow { width: 32px; height: 32px; transition: opacity .3s var(--ease-out), background-color .2s, transform .35s var(--spring-bouncy); }
.arrow.dim { opacity: .25; pointer-events: none; }
.title-link { display: inline-flex; align-items: center; }
.chev { transition: transform .4s var(--spring-bouncy); }
.title-link:hover .chev { transform: translateX(4px); }
@media (max-width: 700px) { .arrow { display: none; } }
</style>
