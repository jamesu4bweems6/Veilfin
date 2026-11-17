<script setup>
import { ref, reactive, onMounted } from 'vue'
import { rpc, toast, toastError } from '../store.js'
import { fmtTime, readFileBase64 } from '../util.js'
import Modal from './Modal.vue'
import { useIndicator } from '../composables/indicator.js'

const props = defineProps({ item: { type: Object, required: true } })
const emit = defineEmits(['close', 'saved'])

const it = props.item
const tab = ref('meta')
const tabsEl = ref(null)
const { style: tabLine } = useIndicator(tabsEl, 'button.on', tab)
const busy = ref(false)
const form = reactive({
  title: it.title,
  sortTitle: it.sortTitle,
  originalTitle: it.originalTitle,
  overview: it.overview,
  year: it.year || '',
  releaseDate: it.releaseDate,
  code: it.code || '',
  studio: it.studio,
  director: it.director,
  series: it.series,
  rating: it.rating || '',
  genres: (it.tags.genre || []).join(', '),
  tags: (it.tags.tag || []).join(', '),
  actors: (it.tags.actor || []).join(', '),
})
const initial = JSON.stringify(form)
const original = JSON.parse(initial)

const split = (s) => String(s).split(/[,，、]/).map((x) => x.trim()).filter(Boolean)

async function save() {
  // 只提交修改过的字段：它们会被锁定，之后重新刮削不会覆盖
  const fields = Object.keys(form).filter((k) => String(form[k]) !== String(original[k]))
  if (!fields.length) {
    emit('close')
    return
  }
  busy.value = true
  try {
    await rpc('items.update', {
      id: it.id,
      fields,
      meta: {
        ...form,
        year: Number(form.year) || 0,
        rating: Number(form.rating) || 0,
        genres: split(form.genres),
        tags: split(form.tags),
        actors: split(form.actors),
      },
    })
    toast('已保存', 'ok')
    emit('saved')
    emit('close')
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = false
  }
}

async function unlockAll() {
  try {
    await rpc('items.unlock', { id: it.id })
    toast('已解除所有字段锁定，下次刮削会更新它们', 'ok')
    emit('saved')
  } catch (e) {
    toastError(e)
  }
}

// ---- 图片 ----
const preview = ref(null)
const videoSrc = ref('')
const frameAt = ref(0)
onMounted(async () => {
  try {
    const p = await rpc('play.open', { id: it.id })
    videoSrc.value = p.video
  } catch {}
})

async function setFromFrame(kind) {
  busy.value = true
  try {
    await rpc('items.image', { id: it.id, kind, frameAt: preview.value?.currentTime ?? frameAt.value })
    toast('已更新图片', 'ok')
    emit('saved')
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = false
  }
}

async function upload(kind, ev) {
  const file = ev.target.files?.[0]
  ev.target.value = ''
  if (!file) return
  if (file.size > 6 * 1024 * 1024) {
    toastError(new Error('图片不能超过 6 MB'))
    return
  }
  busy.value = true
  try {
    await rpc('items.image', { id: it.id, kind, data: await readFileBase64(file) })
    toast('已上传', 'ok')
    emit('saved')
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = false
  }
}
const kinds = [
  { k: 'poster', name: '海报（竖版）' },
  { k: 'thumb', name: '缩略图（横版）' },
  { k: 'backdrop', name: '背景图' },
]
</script>

<template>
  <Modal title="编辑元数据" width="760px" @close="emit('close')">
    <div ref="tabsEl" class="tabs">
      <span class="indicator tab-line" :style="tabLine" />
      <button :class="{ on: tab === 'meta' }" @click="tab = 'meta'">基本信息</button>
      <button :class="{ on: tab === 'images' }" @click="tab = 'images'">图片</button>
    </div>

    <Transition name="page-tab" type="transition" mode="out-in">
    <form v-if="tab === 'meta'" class="grid2" @submit.prevent="save">
      <div class="field span2"><label>标题</label><input v-model="form.title" class="input" required /></div>
      <div class="field"><label>原标题</label><input v-model="form.originalTitle" class="input" /></div>
      <div class="field"><label>排序标题</label><input v-model="form.sortTitle" class="input" /></div>
      <div class="field"><label>番号</label><input v-model="form.code" class="input" /></div>
      <div class="field"><label>系列 / 合集</label><input v-model="form.series" class="input" /></div>
      <div class="field"><label>年份</label><input v-model="form.year" class="input" inputmode="numeric" /></div>
      <div class="field"><label>发行日期</label><input v-model="form.releaseDate" class="input" placeholder="YYYY-MM-DD" /></div>
      <div class="field"><label>制作</label><input v-model="form.studio" class="input" /></div>
      <div class="field"><label>导演</label><input v-model="form.director" class="input" /></div>
      <div class="field"><label>评分</label><input v-model="form.rating" class="input" inputmode="decimal" /></div>
      <div class="field span2"><label>简介</label><textarea v-model="form.overview" class="input" rows="4" /></div>
      <div class="field span2"><label>类型（逗号分隔）</label><input v-model="form.genres" class="input" /></div>
      <div class="field span2"><label>标签（逗号分隔）</label><input v-model="form.tags" class="input" /></div>
      <div class="field span2"><label>演员 / 人物（逗号分隔）</label><input v-model="form.actors" class="input" /></div>
      <p class="hint span2">
        修改过的字段会被锁定，重新刮削时不会覆盖。
        <template v-if="it.locked?.length">当前已锁定：{{ it.locked.join('、') }}。<a class="link" @click="unlockAll">解除全部锁定</a></template>
      </p>
      <button type="submit" class="hidden" />
    </form>

    <div v-else class="images">
      <div class="frame-picker">
        <video v-if="videoSrc" ref="preview" :src="videoSrc" controls preload="metadata" muted @timeupdate="frameAt = $event.target.currentTime" />
        <div v-else class="muted">视频加载中…</div>
        <div class="row">
          <span class="muted">拖到想要的画面，然后：</span>
          <button v-for="k in kinds" :key="k.k" class="btn" :disabled="busy" @click="setFromFrame(k.k)">设为{{ k.name }}</button>
        </div>
        <p class="hint">当前位置 {{ fmtTime(frameAt) }}。设为海报时会从画面中间裁出竖版。</p>
      </div>
      <div class="uploads">
        <div v-for="k in kinds" :key="k.k" class="up">
          <div class="muted">{{ k.name }}</div>
          <div class="cur" :class="k.k">
            <img v-if="it[k.k]" :src="it[k.k]" alt="" />
            <span v-else class="muted">无</span>
          </div>
          <label class="btn small">上传图片<input type="file" accept="image/*" hidden @change="upload(k.k, $event)" /></label>
        </div>
      </div>
      <p class="hint">图片修改后关闭编辑窗口即可在详情页看到。</p>
    </div>
    </Transition>

    <template #footer>
      <button class="btn ghost" @click="emit('close')">{{ tab === 'meta' ? '取消' : '关闭' }}</button>
      <button v-if="tab === 'meta'" class="btn primary" :disabled="busy" @click="save">保存</button>
    </template>
  </Modal>
</template>

<style scoped>
.tabs { position: relative; display: flex; gap: 4px; margin-bottom: 16px; border-bottom: 1px solid var(--line); }
.tabs button { position: relative; z-index: 1; padding: 8px 16px; color: var(--muted); transition: color .25s; }
.tabs button.on { color: var(--text); }
.tab-line { left: 0; top: 0; }
.tab-line::after { content: ''; position: absolute; left: 10px; right: 10px; bottom: 0; height: 2px; border-radius: 2px; background: var(--accent); }
.grid2 { display: grid; grid-template-columns: 1fr 1fr; gap: 0 16px; }
.span2 { grid-column: span 2; }
.link { color: var(--accent); cursor: pointer; }
.frame-picker video { width: 100%; max-height: 300px; background: #000; border-radius: 6px; margin-bottom: 10px; }
.uploads { display: grid; grid-template-columns: repeat(3, 1fr); gap: 14px; margin-top: 18px; }
.up { display: grid; gap: 8px; justify-items: start; }
.cur { width: 100%; background: var(--bg2); border-radius: 4px; overflow: hidden; display: flex; align-items: center; justify-content: center; }
.cur.poster { aspect-ratio: 2 / 3; max-width: 140px; }
.cur.thumb, .cur.backdrop { aspect-ratio: 16 / 9; }
.cur img { width: 100%; height: 100%; object-fit: cover; }
.small { height: 30px; padding: 0 12px; font-size: 13px; }
@media (max-width: 600px) { .grid2 { grid-template-columns: 1fr; } .span2 { grid-column: auto; } .uploads { grid-template-columns: 1fr; } }
</style>
