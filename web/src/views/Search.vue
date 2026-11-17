<script setup>
import { ref, watch, onMounted } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { rpc, toastError } from '../store.js'
import { debounce } from '../util.js'
import MediaCard from '../components/MediaCard.vue'

const route = useRoute()
const router = useRouter()
const q = ref(route.query.q || '')
const items = ref([])
const total = ref(0)
const loading = ref(false)
const input = ref(null)

async function search() {
  const text = q.value.trim()
  router.replace({ query: text ? { q: text } : {} })
  if (!text) {
    items.value = []
    total.value = 0
    return
  }
  loading.value = true
  try {
    const r = await rpc('items.list', { libraryId: 0, q: text, sort: 'title', limit: 120 })
    items.value = r.items
    total.value = r.total
  } catch (e) {
    toastError(e)
  } finally {
    loading.value = false
  }
}
watch(q, debounce(search, 300))
onMounted(() => {
  input.value?.focus()
  if (q.value) search()
})
</script>

<template>
  <div class="page">
    <input ref="input" v-model="q" class="input big-search" placeholder="搜索标题、番号、演员、标签…" />
    <p v-if="q && !loading" class="muted">共 {{ total }} 个结果<span v-if="total > items.length">，显示前 {{ items.length }} 个</span></p>
    <div class="grid poster">
      <MediaCard v-for="(it, i) in items" :key="it.id" :item="it" shape="poster" :index="i" />
    </div>
    <div v-if="loading" class="loading"><div class="spinner" /></div>
  </div>
</template>

<style scoped>
.big-search { font-size: 20px; padding: 14px 20px; margin-bottom: 16px; border-radius: 14px; animation: rise-in .5s var(--spring) backwards; }
</style>
