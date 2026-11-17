<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { state, rpc, toast, toastError } from '../store.js'
import { fmtDuration, fmtSize, fmtTime, resolutionLabel, placeholderStyle } from '../util.js'
import Icon from '../components/Icon.vue'
import Modal from '../components/Modal.vue'
import ItemEditor from '../components/ItemEditor.vue'

const props = defineProps({ id: String })
const router = useRouter()
const it = ref(null)
const err = ref('')
const menu = ref(false)
const editing = ref(false)
const confirmDelete = ref(false)
const deleteFile = ref(false)
const rescrape = ref(false)
const rescrapeQuery = ref('')
const replaceImages = ref(true)
const busy = ref(false)
const bdLoaded = ref(false)
const posterLoaded = ref(false)

async function load() {
  try {
    it.value = await rpc('items.get', { id: Number(props.id) })
  } catch (e) {
    err.value = e.message
  }
}
onMounted(load)

// 返回：有站内历史就后退（保留上一页的滚动位置和筛选），直接打开的链接则回到所在媒体库
function goBack() {
  if (window.history.state?.back) router.back()
  else router.push(it.value ? `/library/${it.value.libraryId}` : '/')
}

// “更多”菜单：点击菜单以外的地方或按 Esc 时收起（pointerdown 同时覆盖鼠标和触屏）
const moreEl = ref(null)
function onPointerDown(e) {
  if (menu.value && !moreEl.value?.contains(e.target)) menu.value = false
}
function onKey(e) {
  if (e.key === 'Escape' && menu.value) menu.value = false
}
onMounted(() => {
  document.addEventListener('pointerdown', onPointerDown)
  document.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onPointerDown)
  document.removeEventListener('keydown', onKey)
})

const backdrop = computed(() => it.value?.backdrop || it.value?.thumb)
const facts = computed(() => {
  const x = it.value
  if (!x) return []
  return [
    x.year && String(x.year),
    x.duration && fmtDuration(x.duration),
    resolutionLabel(x.width, x.height),
    x.rating ? `★ ${x.rating.toFixed(1)}` : '',
  ].filter(Boolean)
})
// ffprobe 对 mp4 / mov 报告同一组格式名，按 MIME 显示更直观
function containerName(x) {
  if (x.mime === 'video/webm') return 'webm'
  if (x.container?.includes('mp4')) return 'mp4'
  return x.container?.split(',')[0] || ''
}
const resumable = computed(() => it.value && it.value.position > 0 && !it.value.played)

function play(resume) {
  router.push({ path: `/play/${it.value.id}`, query: resume ? { t: Math.floor(it.value.position) } : {} })
}

async function togglePlayed() {
  try {
    await rpc('items.setPlayed', { id: it.value.id, played: !it.value.played })
    it.value.played = !it.value.played
    if (it.value.played) it.value.position = 0
  } catch (e) {
    toastError(e)
  }
}

function tagLink(kind, name) {
  return { path: `/library/${it.value.libraryId}`, query: { kind, tag: name } }
}

async function doRefresh() {
  menu.value = false
  try {
    await rpc('items.refresh', { id: it.value.id, trickplay: true })
    toast('已加入刷新队列，稍后刷新页面查看', 'ok')
  } catch (e) {
    toastError(e)
  }
}

async function doRescrape() {
  busy.value = true
  try {
    await rpc('items.rescrape', { id: it.value.id, query: rescrapeQuery.value, replaceImages: replaceImages.value })
    rescrape.value = false
    toast('刮削完成', 'ok')
    await load()
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = false
  }
}

async function doDelete() {
  busy.value = true
  try {
    await rpc('items.delete', { id: it.value.id, deleteFile: deleteFile.value })
    toast(deleteFile.value ? '已删除文件和条目' : '已从媒体库移除（下次扫描会重新加入）', 'ok')
    router.replace(`/library/${it.value.libraryId}`)
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = false
  }
}

function openRescrape() {
  menu.value = false
  rescrapeQuery.value = it.value.library.kind === 'jav' ? it.value.code : ''
  rescrape.value = true
}
</script>

<template>
  <div class="item-view">
  <button class="back-btn rise" title="返回" @click="goBack"><Icon name="back" :size="20" /> 返回</button>
  <div v-if="err" class="page empty">{{ err }}</div>
  <div v-else-if="!it" class="detail skeleton-detail">
    <div class="content">
      <div class="skel sk-poster" />
      <div class="info">
        <div class="skel skel-line w-90" />
        <div class="skel sk-title" />
        <div class="skel skel-line w-220" />
        <div class="row mt-26"><div class="skel sk-btn" /><div class="skel sk-btn" /></div>
        <div class="skel skel-line w-80p mt-28" />
        <div class="skel skel-line w-64p" />
      </div>
    </div>
  </div>
  <div v-else class="detail">
    <div class="backdrop" :style="backdrop ? null : placeholderStyle(it.id)">
      <img v-if="backdrop" :src="backdrop" alt="" class="bd" :class="{ blur: it.restricted && state.me?.blurRestricted, loaded: bdLoaded }" @load="bdLoaded = true" />
      <div class="shade" />
    </div>

    <div class="content">
      <div class="poster-col rise">
        <div class="poster" :class="{ wide: !it.poster }" :style="it.poster || it.thumb ? null : placeholderStyle(it.id)">
          <img v-if="it.poster || it.thumb" :src="it.poster || it.thumb" alt="" class="lazy-img" :class="{ loaded: posterLoaded }" @load="posterLoaded = true" />
        </div>
      </div>

      <div class="info">
        <div class="lib muted rise d1">
          <router-link :to="`/library/${it.libraryId}`">{{ it.library.name }}</router-link>
          <span v-if="it.folder"> / {{ it.folder }}</span>
        </div>
        <h1 class="title rise d2">{{ it.title }}</h1>
        <div v-if="it.originalTitle" class="orig muted rise d3">{{ it.originalTitle }}</div>
        <div class="facts rise d3">
          <span v-if="it.code" class="badge code">{{ it.code }}</span>
          <span v-for="f in facts" :key="f">{{ f }}</span>
          <Transition name="swap-badge"><span v-if="it.played" class="played"><Icon name="check" :size="16" /> 已看过</span></Transition>
        </div>

        <div class="actions rise d4">
          <button class="btn primary big" @click="play(resumable)">
            <Icon name="play" /> {{ resumable ? `继续播放 ${fmtTime(it.position)}` : '播放' }}
          </button>
          <button v-if="resumable" class="btn big" @click="play(false)">从头播放</button>
          <button class="btn big" :title="it.played ? '标记为未看' : '标记为已看'" @click="togglePlayed">
            <Icon name="check" :class="{ on: it.played }" /> {{ it.played ? '已看' : '标记已看' }}
          </button>
          <template v-if="state.me?.isAdmin">
            <button class="btn big" @click="editing = true"><Icon name="edit" /> 编辑</button>
            <div ref="moreEl" class="more">
              <button class="btn big" @click.stop="menu = !menu"><Icon name="more" /></button>
              <Transition name="pop">
              <div v-if="menu" class="menu left" @mouseleave="menu = false">
                <button @click="openRescrape"><Icon name="search" :size="18" /> 重新刮削…</button>
                <button @click="doRefresh"><Icon name="refresh" :size="18" /> 刷新元数据和预览图</button>
                <hr />
                <button class="danger-item" @click="menu = false; confirmDelete = true"><Icon name="delete" :size="18" /> 删除…</button>
              </div>
              </Transition>
            </div>
          </template>
        </div>

        <p v-if="it.overview" class="overview rise d5">{{ it.overview }}</p>

        <div v-if="it.tags.actor?.length" class="tagrow rise d6">
          <span class="lbl">演员</span>
          <div class="chips"><router-link v-for="a in it.tags.actor" :key="a" class="chip" :to="tagLink('actor', a)">{{ a }}</router-link></div>
        </div>
        <div v-if="it.tags.genre?.length" class="tagrow rise d6">
          <span class="lbl">类型</span>
          <div class="chips"><router-link v-for="g in it.tags.genre" :key="g" class="chip" :to="tagLink('genre', g)">{{ g }}</router-link></div>
        </div>
        <div v-if="it.tags.tag?.length" class="tagrow rise d7">
          <span class="lbl">标签</span>
          <div class="chips"><router-link v-for="g in it.tags.tag" :key="g" class="chip" :to="tagLink('tag', g)">{{ g }}</router-link></div>
        </div>

        <dl class="meta rise d8">
          <template v-if="it.studio"><dt>制作</dt><dd>{{ it.studio }}</dd></template>
          <template v-if="it.director"><dt>导演</dt><dd>{{ it.director }}</dd></template>
          <template v-if="it.series"><dt>系列</dt><dd>{{ it.series }}</dd></template>
          <template v-if="it.releaseDate"><dt>发行日期</dt><dd>{{ it.releaseDate }}</dd></template>
          <dt>文件</dt>
          <dd>
            {{ [containerName(it), it.vcodec, it.acodec].filter(Boolean).join(' / ').toUpperCase() }}
            · {{ fmtSize(it.size) }}<span v-if="it.bitrate"> · {{ (it.bitrate / 1e6).toFixed(1) }} Mbps</span>
            <span v-if="it.width"> · {{ it.width }}×{{ it.height }}</span>
          </dd>
          <template v-if="it.subtitles?.length"><dt>字幕</dt><dd>{{ it.subtitles.map((s) => s.label).join('、') }}</dd></template>
          <template v-if="it.path"><dt>路径</dt><dd class="path">{{ it.path }}</dd></template>
          <template v-if="state.me?.isAdmin"><dt>元数据来源</dt><dd>{{ it.metaSource || '文件名' }}<span v-if="it.locked?.length" class="muted">（已锁定 {{ it.locked.length }} 个字段）</span></dd></template>
        </dl>
      </div>
    </div>

    <ItemEditor v-if="editing" :item="it" @close="editing = false" @saved="load" />

    <Modal v-if="rescrape" title="重新刮削" @close="rescrape = false">
      <div class="field">
        <label>{{ it.library.kind === 'jav' ? '番号' : '搜索标题' }}（留空则从文件名自动识别）</label>
        <input v-model="rescrapeQuery" class="input" />
      </div>
      <label class="check"><input v-model="replaceImages" type="checkbox" /> 同时替换封面和背景图</label>
      <p class="hint">手动编辑过的字段已被锁定，不会被覆盖。</p>
      <template #footer>
        <button class="btn ghost" @click="rescrape = false">取消</button>
        <button class="btn primary" :disabled="busy" @click="doRescrape">{{ busy ? '刮削中…' : '开始刮削' }}</button>
      </template>
    </Modal>

    <Modal v-if="confirmDelete" title="删除" @close="confirmDelete = false">
      <p>确定要删除「{{ it.title }}」吗？</p>
      <label class="check"><input v-model="deleteFile" type="checkbox" /> 同时从硬盘删除视频文件（不可恢复）</label>
      <p class="hint">不勾选时只是从媒体库中移除，下次扫描时会重新加入。</p>
      <template #footer>
        <button class="btn ghost" @click="confirmDelete = false">取消</button>
        <button class="btn danger" :disabled="busy" @click="doDelete">{{ deleteFile ? '删除文件' : '移除' }}</button>
      </template>
    </Modal>
  </div>
  </div>
</template>

<style scoped>
.item-view { position: relative; }
/* 左上角返回按钮：毛玻璃胶囊，位于顶栏下方 */
.back-btn { position: absolute; z-index: 5; top: calc(var(--topbar) + 14px); left: 4vw; display: inline-flex; align-items: center; gap: 2px;
  height: 36px; padding: 0 14px 0 8px; border-radius: 999px; color: #eee; background: rgba(255,255,255,.1);
  border: 0.5px solid rgba(255,255,255,.12); backdrop-filter: saturate(180%) blur(20px); -webkit-backdrop-filter: saturate(180%) blur(20px);
  transition: background-color .2s, transform .35s var(--spring-bouncy); }
.back-btn:hover { background: rgba(255,255,255,.18); }
.back-btn:active { transform: scale(.94); transition-duration: .08s; }
.detail { position: relative; min-height: 100vh; }
.backdrop { position: absolute; inset: 0 0 auto 0; height: 78vh; overflow: hidden; }
/* 背景图：加载后淡入并缓慢推近，类似 iOS 的视差背景 */
.backdrop img { width: 100%; height: 100%; object-fit: cover; opacity: 0; transform: scale(1.08); transition: opacity 1s var(--ease-out), transform 2.4s var(--spring); }
.backdrop img.loaded { opacity: .55; transform: scale(1); }
.backdrop img.blur { filter: blur(24px); }
.shade { position: absolute; inset: 0; background: linear-gradient(90deg, rgba(16,16,16,.95) 0%, rgba(16,16,16,.55) 55%, rgba(16,16,16,.2) 100%),
  linear-gradient(0deg, var(--bg) 0%, rgba(16,16,16,0) 45%); }
.content { position: relative; display: flex; gap: 36px; padding: calc(var(--topbar) + 9vh) 4vw 60px; }
.poster-col { flex-shrink: 0; }
.poster { width: 270px; aspect-ratio: 2 / 3; border-radius: 12px; overflow: hidden; box-shadow: 0 18px 50px rgba(0,0,0,.6); background: #222;
  transition: transform .6s var(--spring); }
.poster:hover { transform: scale(1.02) rotate(-0.4deg); }
.poster.wide { aspect-ratio: 16 / 9; width: 360px; }
.poster img { width: 100%; height: 100%; object-fit: cover; }
.info { flex: 1; min-width: 0; max-width: 980px; }
.lib a:hover { color: var(--accent); }
.title { font-size: clamp(26px, 3.4vw, 42px); font-weight: 600; margin: 6px 0 4px; line-height: 1.2; }
.orig { font-size: 16px; margin-bottom: 6px; }
.facts { display: flex; flex-wrap: wrap; align-items: center; gap: 14px; margin: 12px 0 22px; color: #ddd; }
.code { font-weight: 600; letter-spacing: .5px; }
.played { color: var(--accent); display: inline-flex; align-items: center; gap: 4px; }
.actions { display: flex; flex-wrap: wrap; gap: 10px; margin-bottom: 24px; }
.on { color: var(--accent); }
.more { position: relative; }
.more .menu { top: 52px; left: 0; transform-origin: top left; }
.swap-badge-enter-active { transition: opacity .25s, transform .5s var(--spring-bouncy); }
.swap-badge-leave-active { transition: opacity .15s, transform .15s; }
.swap-badge-enter-from, .swap-badge-leave-to { opacity: 0; transform: scale(.6); }
.sk-poster { width: 270px; aspect-ratio: 2 / 3; border-radius: 12px; flex-shrink: 0; }
.sk-title { width: 60%; height: 40px; margin: 14px 0 10px; border-radius: 8px; }
.sk-btn { width: 130px; height: 44px; border-radius: 10px; }
.danger-item { color: var(--err); }
.overview { font-size: 15px; line-height: 1.8; color: #ddd; white-space: pre-wrap; margin: 0 0 22px; max-width: 900px; }
.tagrow { display: flex; gap: 14px; margin-bottom: 12px; align-items: flex-start; }
.lbl { color: var(--muted); width: 48px; flex-shrink: 0; padding-top: 4px; }
.meta { display: grid; grid-template-columns: 90px 1fr; gap: 8px 14px; margin-top: 26px; font-size: 13.5px; }
.meta dt { color: var(--muted); }
.meta dd { margin: 0; }
.path { word-break: break-all; font-family: Consolas, monospace; font-size: 12.5px; color: #bbb; }
@media (max-width: 760px) {
  .content { flex-direction: column; gap: 18px; padding-top: calc(var(--topbar) + 64px); }
  .poster { width: 160px; }
  .poster.wide { width: 100%; }
}
</style>
