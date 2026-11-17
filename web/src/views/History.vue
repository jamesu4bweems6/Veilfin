<script setup>
import { ref, computed, onMounted } from 'vue'
import { rpc, toast, toastError } from '../store.js'
import { fmtDate, fmtDateTime, fmtDuration } from '../util.js'
import MediaCard from '../components/MediaCard.vue'
import Modal from '../components/Modal.vue'

const items = ref([])
const total = ref(0)
const loading = ref(false)
const confirm = ref(false)

async function load(more) {
  loading.value = true
  try {
    const r = await rpc('history.list', { offset: more ? items.value.length : 0, limit: 60 })
    items.value = more ? items.value.concat(r.items) : r.items
    total.value = r.total
  } catch (e) {
    toastError(e)
  } finally {
    loading.value = false
  }
}
onMounted(() => load(false))

// 按日期分组
const groups = computed(() => {
  const out = []
  let cur = null
  const today = fmtDate(Date.now() / 1000)
  const yesterday = fmtDate(Date.now() / 1000 - 86400)
  for (const it of items.value) {
    const d = fmtDate(it.startedAt)
    if (!cur || cur.day !== d) {
      cur = { day: d, label: d === today ? '今天' : d === yesterday ? '昨天' : d, items: [] }
      out.push(cur)
    }
    cur.items.push(it)
  }
  return out
})

async function clearAll() {
  try {
    await rpc('history.clear')
    items.value = []
    total.value = 0
    confirm.value = false
    toast('已清空观看记录', 'ok')
  } catch (e) {
    toastError(e)
  }
}
</script>

<template>
  <div class="page">
    <h1 class="page-title">
      观看记录 <span class="muted count">{{ total }} 次</span>
      <div class="spacer" />
      <button v-if="total" class="btn" @click="confirm = true">清空</button>
    </h1>
    <div v-if="!loading && !items.length" class="empty">还没有观看记录</div>
    <section v-for="g in groups" :key="g.day">
      <h2 class="section-title">{{ g.label }}</h2>
      <div class="grid thumb">
        <MediaCard v-for="(it, i) in g.items" :key="it.id + '-' + it.startedAt + '-' + i" :item="it" shape="thumb" :index="i"
          :subtitle="`${fmtDateTime(it.startedAt).slice(11)} · 观看 ${fmtDuration(it.watched) || '不到 1 分钟'}`" />
      </div>
    </section>
    <div v-if="loading" class="loading"><div class="spinner" /></div>
    <div v-else-if="items.length < total" class="more"><button class="btn" @click="load(true)">加载更多</button></div>

    <Modal v-if="confirm" title="清空观看记录" @close="confirm = false">
      <p>清空后，“观看记录”列表和统计里的数据会被删除。播放进度和“已看”标记不受影响。</p>
      <template #footer>
        <button class="btn ghost" @click="confirm = false">取消</button>
        <button class="btn danger" @click="clearAll">清空</button>
      </template>
    </Modal>
  </div>
</template>

<style scoped>
.count { font-size: 15px; }
.more { text-align: center; margin-top: 24px; }
</style>
