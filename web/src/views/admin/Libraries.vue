<script setup>
import { ref, onMounted } from 'vue'
import { rpc, toast, toastError } from '../../store.js'
import { kindNames, scraperNames, fmtDateTime } from '../../util.js'
import Modal from '../../components/Modal.vue'
import Icon from '../../components/Icon.vue'

const libs = ref([])
const edit = ref(null)
const del = ref(null)
const busy = ref(false)
const browser = ref(null) // 目录浏览器 { path, parent, dirs }

async function load() {
  try {
    libs.value = await rpc('admin.libs.list')
  } catch (e) {
    toastError(e)
  }
}
onMounted(load)

const defaultScraper = { home: 'none', movie: 'tmdb', jav: 'metatube', other: 'none' }

function openNew() {
  edit.value = { id: 0, name: '', kind: 'home', restricted: false, scraper: 'none', paths: [''], sortOrder: libs.value.length }
}
function openEdit(l) {
  edit.value = { ...l, paths: [...l.paths] }
}
function onKind() {
  edit.value.scraper = defaultScraper[edit.value.kind]
  if (edit.value.kind === 'jav') edit.value.restricted = true
}

async function save() {
  busy.value = true
  try {
    const l = { ...edit.value, paths: edit.value.paths.filter((p) => p.trim()) }
    await rpc(l.id ? 'admin.libs.update' : 'admin.libs.create', l)
    toast('已保存，开始扫描', 'ok')
    edit.value = null
    load()
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = false
  }
}

async function scan(l, refresh) {
  try {
    await rpc('admin.libs.scan', { id: l.id, refresh })
    toast(refresh ? '已开始刷新元数据' : '已开始扫描', 'ok')
  } catch (e) {
    toastError(e)
  }
}

async function doDelete() {
  try {
    await rpc('admin.libs.delete', { id: del.value.id })
    del.value = null
    load()
  } catch (e) {
    toastError(e)
  }
}

// ---- 目录浏览 ----
let browseIndex = 0
async function browse(i, path) {
  browseIndex = i
  try {
    browser.value = await rpc('admin.fs.list', { path: path ?? edit.value.paths[i] ?? '' })
  } catch (e) {
    browser.value = await rpc('admin.fs.list', { path: '' }).catch(() => null)
    toastError(e)
  }
}
async function cd(path) {
  try {
    browser.value = await rpc('admin.fs.list', { path })
  } catch (e) {
    toastError(e)
  }
}
function pick() {
  edit.value.paths[browseIndex] = browser.value.path
  browser.value = null
}
</script>

<template>
  <div class="admin-body">
    <div class="row head">
      <span class="muted">媒体库目录在服务器上，支持 mp4 / m4v / mov / webm。</span>
      <div class="spacer" />
      <button class="btn primary" @click="openNew">添加媒体库</button>
    </div>
    <div v-if="!libs.length" class="card-panel empty">还没有媒体库，点击右上角添加。</div>
    <div class="libs">
      <div v-for="(l, i) in libs" :key="l.id" class="card-panel lib" :style="{ '--i': i }">
        <div class="lhead">
          <h2>{{ l.name }} <Icon v-if="l.restricted" name="lock" :size="16" class="muted" /></h2>
          <span class="muted">{{ kindNames[l.kind] }} · {{ l.count }} 个视频 · {{ scraperNames[l.scraper] }}</span>
        </div>
        <div v-for="p in l.paths" :key="p" class="path">{{ p }}</div>
        <div class="muted small">上次扫描：{{ l.lastScanAt ? fmtDateTime(l.lastScanAt) : '从未' }}</div>
        <div class="row ops">
          <button class="btn" @click="scan(l, false)"><Icon name="refresh" :size="18" /> 扫描</button>
          <button class="btn" @click="scan(l, true)">刷新元数据</button>
          <button class="btn" @click="openEdit(l)"><Icon name="edit" :size="18" /> 编辑</button>
          <div class="spacer" />
          <button class="btn ghost err" @click="del = l">删除</button>
        </div>
      </div>
    </div>

    <Modal v-if="edit" :title="edit.id ? '编辑媒体库' : '添加媒体库'" width="600px" @close="edit = null">
      <form @submit.prevent="save">
        <div class="field"><label>名称</label><input v-model="edit.name" class="input" required /></div>
        <div class="field">
          <label>类型</label>
          <select v-model="edit.kind" class="input" @change="onKind">
            <option v-for="(n, k) in kindNames" :key="k" :value="k">{{ n }}</option>
          </select>
          <span class="hint">自制视频以横版缩略图展示，并优先读取同名 .json / .nfo；日本影片会从文件名识别番号。</span>
        </div>
        <div class="field">
          <label>刮削源</label>
          <select v-model="edit.scraper" class="input">
            <option v-for="(n, k) in scraperNames" :key="k" :value="k">{{ n }}</option>
          </select>
          <span class="hint">有 NFO 的视频优先使用 NFO，不会在线刮削。在线刮削需要先在“服务器设置”里填写 TMDB Key 或 MetaTube 地址。</span>
        </div>
        <div class="field">
          <label>目录</label>
          <div v-for="(p, i) in edit.paths" :key="i" class="pathrow">
            <input v-model="edit.paths[i]" class="input" placeholder="服务器上的绝对路径" />
            <button type="button" class="btn" @click="browse(i)">浏览…</button>
            <button v-if="edit.paths.length > 1" type="button" class="icon-btn" @click="edit.paths.splice(i, 1)"><Icon name="close" :size="18" /></button>
          </div>
          <button type="button" class="btn ghost addp" @click="edit.paths.push('')">+ 添加目录</button>
        </div>
        <label class="check"><input v-model="edit.restricted" type="checkbox" /> 受限媒体库</label>
        <p class="hint">受限媒体库只对被授权的用户可见；用户设置 PIN 后，还需要输入 PIN 才能看到。</p>
        <button type="submit" class="hidden" />
      </form>
      <template #footer>
        <button class="btn ghost" @click="edit = null">取消</button>
        <button class="btn primary" :disabled="busy" @click="save">保存并扫描</button>
      </template>
    </Modal>

    <Modal v-if="browser" title="选择目录" width="560px" @close="browser = null">
      <div class="cur-path">{{ browser.path || '（选择磁盘）' }}</div>
      <div class="dirs">
        <button v-if="browser.path" class="dir" @click="cd(browser.parent)">⬑ 上一级</button>
        <button v-for="d in browser.dirs" :key="d.path" class="dir" @click="cd(d.path)"><Icon name="folder" :size="18" class="fi" /> {{ d.name }}</button>
        <div v-if="!browser.dirs?.length" class="muted pad">没有子目录</div>
      </div>
      <template #footer>
        <button class="btn ghost" @click="browser = null">取消</button>
        <button class="btn primary" :disabled="!browser.path" @click="pick">选择此目录</button>
      </template>
    </Modal>

    <Modal v-if="del" title="删除媒体库" @close="del = null">
      <p>确定删除媒体库「{{ del.name }}」？只会删除数据库中的条目、观看记录和缓存的图片，<b>不会删除硬盘上的视频文件</b>。</p>
      <template #footer>
        <button class="btn ghost" @click="del = null">取消</button>
        <button class="btn danger" @click="doDelete">删除</button>
      </template>
    </Modal>
  </div>
</template>

<style scoped>
.head { margin-bottom: 14px; }
.libs { display: grid; grid-template-columns: repeat(auto-fill, minmax(420px, 1fr)); gap: 14px; }
@media (max-width: 500px) { .libs { grid-template-columns: 1fr; } }
.lhead { margin-bottom: 8px; }
.lhead h2 { font-size: 18px; display: flex; align-items: center; gap: 6px; }
.path { font-family: Consolas, monospace; font-size: 12.5px; color: #bbb; word-break: break-all; padding: 2px 0; }
.small { font-size: 12.5px; margin-top: 6px; }
.ops { margin-top: 14px; }
.pathrow { display: flex; gap: 8px; margin-bottom: 6px; align-items: center; }
.addp { justify-self: start; }
.cur-path { font-family: Consolas, monospace; font-size: 13px; padding: 8px 10px; background: var(--bg2); border-radius: 4px; margin-bottom: 8px; word-break: break-all; }
.dirs { max-height: 50vh; overflow-y: auto; display: grid; }
.dir { text-align: left; padding: 8px 10px; border-radius: 8px; display: flex; align-items: center; gap: 8px; animation: fade-in .3s var(--ease-out) backwards; }
.dir:hover { background: rgba(255,255,255,.06); }
.fi { color: #d9b44a; }
.pad { padding: 10px; }
</style>
