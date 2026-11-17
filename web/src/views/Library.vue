<script setup>
import { ref, computed, watch, onMounted, onBeforeUnmount, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { rpc, toastError } from '../store.js'
import { debounce, kindNames } from '../util.js'
import MediaCard from '../components/MediaCard.vue'
import Icon from '../components/Icon.vue'
import { useIndicator } from '../composables/indicator.js'

const props = defineProps({ id: String })
const route = useRoute()
const router = useRouter()

const lib = ref(null)
const items = ref([])
const folders = ref([])
const total = ref(0)
const loading = ref(false)
const done = ref(false)
const filters = ref({ genre: [], tag: [], actor: [] })
const sentinel = ref(null)
const PAGE = 60

// 筛选条件保存在 URL 里，返回时可以恢复
const q = computed(() => route.query)
const browse = computed(() => q.value.view === 'folders')
const folder = computed(() => q.value.folder || '')
const searchText = ref(q.value.q || '')
const seg = ref(null)
const { style: thumbStyle } = useIndicator(seg, 'button.on', () => browse.value)
const firstLoad = ref(true)

const shape = computed(() => (lib.value?.kind === 'home' ? 'thumb' : 'poster'))
const crumbs = computed(() => {
  const parts = folder.value ? folder.value.split('/') : []
  return parts.map((name, i) => ({ name, path: parts.slice(0, i + 1).join('/') }))
})

function setQuery(patch) {
  const next = { ...route.query, ...patch }
  for (const k of Object.keys(next)) if (next[k] === '' || next[k] == null) delete next[k]
  router.replace({ query: next })
}
const applySearch = debounce((v) => setQuery({ q: v }), 350)
watch(searchText, applySearch)

async function load(reset) {
  if (loading.value) return
  if (reset) {
    items.value = []
    folders.value = []
    done.value = false
  }
  if (done.value) return
  loading.value = true
  try {
    const played = q.value.played === 'yes' ? true : q.value.played === 'no' ? false : undefined
    const r = await rpc('items.list', {
      libraryId: Number(props.id),
      browse: browse.value,
      folder: folder.value,
      q: q.value.q || '',
      kind: q.value.kind || '',
      tag: q.value.tag || '',
      played,
      sort: q.value.sort || 'added',
      desc: q.value.order !== 'asc',
      offset: items.value.length,
      limit: PAGE,
    })
    if (reset) folders.value = r.folders || []
    items.value.push(...r.items)
    total.value = r.total
    done.value = items.value.length >= r.total || r.items.length === 0
    firstLoad.value = false
  } catch (e) {
    toastError(e)
    done.value = true
  } finally {
    loading.value = false
  }
  await nextTick()
  checkSentinel()
}

let io = null
function checkSentinel() {
  if (!sentinel.value || done.value) return
  const r = sentinel.value.getBoundingClientRect()
  if (r.top < window.innerHeight + 600) load(false)
}

onMounted(async () => {
  try {
    const libs = await rpc('lib.list')
    lib.value = libs.find((l) => String(l.id) === props.id) || null
    filters.value = await rpc('items.filters', { libraryId: Number(props.id) })
  } catch (e) {
    toastError(e)
  }
  io = new IntersectionObserver((es) => es.some((e) => e.isIntersecting) && load(false), { rootMargin: '600px' })
  if (sentinel.value) io.observe(sentinel.value)
  load(true)
})
onBeforeUnmount(() => io?.disconnect())
watch(() => route.query, () => load(true), { deep: true })

const tagOptions = computed(() => filters.value[q.value.kind] || [])
</script>

<template>
  <div class="page">
    <h1 class="page-title">
      {{ lib?.name || '媒体库' }}
      <span class="muted count">{{ total }} 个{{ browse ? '（当前文件夹）' : '' }}</span>
    </h1>

    <div class="toolbar">
      <div ref="seg" class="seg">
        <span class="indicator seg-thumb" :style="thumbStyle" />
        <button :class="{ on: !browse }" @click="setQuery({ view: '', folder: '' })">全部</button>
        <button :class="{ on: browse }" @click="setQuery({ view: 'folders' })"><Icon name="folder" :size="16" /> 文件夹</button>
      </div>
      <input v-model="searchText" class="input search" placeholder="在此媒体库中搜索标题、番号、演员…" />
      <select class="input sel" :value="q.sort || 'added'" @change="setQuery({ sort: $event.target.value })">
        <option value="added">添加时间</option>
        <option value="title">标题</option>
        <option value="release">发行日期</option>
        <option value="duration">时长</option>
        <option value="lastPlayed">最近播放</option>
        <option value="random">随机</option>
      </select>
      <button class="btn" :title="q.order === 'asc' ? '升序' : '降序'" @click="setQuery({ order: q.order === 'asc' ? '' : 'asc' })">
        {{ q.order === 'asc' ? '↑ 升序' : '↓ 降序' }}
      </button>
      <select class="input sel" :value="q.played || ''" @change="setQuery({ played: $event.target.value })">
        <option value="">全部</option>
        <option value="no">未看</option>
        <option value="yes">已看</option>
      </select>
      <select class="input sel" :value="q.kind || ''" @change="setQuery({ kind: $event.target.value, tag: '' })">
        <option value="">按标签筛选</option>
        <option value="genre" :disabled="!filters.genre?.length">类型</option>
        <option value="tag" :disabled="!filters.tag?.length">标签</option>
        <option value="actor" :disabled="!filters.actor?.length">演员</option>
      </select>
      <select v-if="q.kind" class="input sel" :value="q.tag || ''" @change="setQuery({ tag: $event.target.value })">
        <option value="">（全部）</option>
        <option v-for="t in tagOptions" :key="t" :value="t">{{ t }}</option>
      </select>
    </div>

    <Transition name="slide-up">
      <TransitionGroup v-if="browse" tag="div" name="crumb" class="crumbs">
        <a key="root" @click="setQuery({ folder: '' })">{{ lib?.name }}</a>
        <span v-for="c in crumbs" :key="c.path" class="crumb">
          <Icon name="chevronRight" :size="16" class="muted" />
          <a @click="setQuery({ folder: c.path })">{{ c.name }}</a>
        </span>
      </TransitionGroup>
    </Transition>

    <div v-if="browse && folders.length" class="folders">
      <button v-for="(f, i) in folders" :key="f.path" class="folder rise" :style="{ '--i': i }" @click="setQuery({ folder: f.path })">
        <Icon name="folder" :size="34" class="fi" />
        <div class="fname" :title="f.name">{{ f.name }}</div>
        <div class="muted small">{{ f.count }} 个视频</div>
      </button>
    </div>

    <div v-if="firstLoad && loading" class="grid" :class="shape">
      <div v-for="n in 12" :key="n" class="skel skel-card" :class="shape" :style="{ animationDelay: n * 60 + 'ms' }" />
    </div>
    <div class="grid" :class="shape">
      <MediaCard v-for="(it, i) in items" :key="it.id" :item="it" :shape="shape" :index="i" />
    </div>
    <div v-if="!loading && !items.length && !folders.length" class="empty rise">
      {{ q.q || q.tag || q.played ? '没有符合条件的视频' : lib ? `这个${kindNames[lib.kind] || ''}媒体库还是空的` : '' }}
    </div>
    <div ref="sentinel" class="sentinel" />
    <Transition name="fade">
      <div v-if="loading && !firstLoad" class="loading"><div class="spinner" /></div>
    </Transition>
  </div>
</template>

<style scoped>
.count { font-size: 15px; }
.toolbar { display: flex; flex-wrap: wrap; gap: 10px; margin-bottom: 18px; align-items: center; }
.search { width: 280px; max-width: 100%; }
.sel { width: auto; padding-right: 28px; }
/* iOS 分段控件：选中块在两个选项之间滑动 */
.seg { position: relative; display: inline-flex; background: var(--bg2); border: 1px solid var(--line); border-radius: 10px; padding: 3px; }
.seg button { position: relative; z-index: 1; padding: 6px 14px; display: inline-flex; align-items: center; gap: 4px; border-radius: 8px; color: var(--muted); transition: color .3s var(--ease-out), transform .35s var(--spring-bouncy); }
.seg button.on { color: #fff; }
.seg-thumb { left: 0; top: 0; border-radius: 8px; background: var(--accent); box-shadow: 0 2px 8px rgba(0,0,0,.35); }
.skel-card.poster { aspect-ratio: 2 / 3; }
.skel-card.thumb { aspect-ratio: 16 / 9; }
.crumb { display: inline-flex; align-items: center; gap: 6px; }
.crumb-enter-active { transition: opacity .3s var(--ease-out), transform .45s var(--spring); }
.crumb-leave-active { transition: opacity .15s; position: absolute; }
.crumb-enter-from { opacity: 0; transform: translateX(-10px); }
.crumb-leave-to { opacity: 0; }
.crumbs { display: flex; align-items: center; gap: 6px; margin-bottom: 14px; flex-wrap: wrap; }
.crumbs a { cursor: pointer; }
.crumbs a:hover { color: var(--accent); }
.folders { display: grid; grid-template-columns: repeat(auto-fill, minmax(170px, 1fr)); gap: 12px; margin-bottom: 24px; }
.folder { background: var(--panel); border-radius: 12px; padding: 14px; text-align: left; }
.folder:hover { box-shadow: 0 0 0 2px var(--accent), 0 12px 24px rgba(0,0,0,.35); transform: translateY(-2px); }
.folder .fi { transition: transform .45s var(--spring-bouncy); }
.folder:hover .fi { transform: scale(1.12) rotate(-6deg); }
.fi { color: #d9b44a; }
.fname { margin-top: 6px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.small { font-size: 12px; }
.sentinel { height: 1px; }
</style>
