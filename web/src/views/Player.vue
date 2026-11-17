<script setup>
import { ref, computed, onMounted, onBeforeUnmount, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { rpc, beacon, toastError } from '../store.js'
import { fmtTime, randomId } from '../util.js'
import Icon from '../components/Icon.vue'

const props = defineProps({ id: String })
const router = useRouter()
const route = useRoute()

const root = ref(null)
const video = ref(null)
const bar = ref(null)
const data = ref(null)
const err = ref('')

const playing = ref(false)
const waiting = ref(true)
const cur = ref(0)
const dur = ref(0)
const buffered = ref([])
const volume = ref(Number(localStorage.getItem('vf.volume') ?? 1))
const muted = ref(localStorage.getItem('vf.muted') === '1')
const rate = ref(1)
const fullscreen = ref(false)
const showUI = ref(true)
const menu = ref('') // '' | 'subs' | 'speed'
const subIndex = ref(-1)
const hover = ref(null) // { x, t }
const ended = ref(false)
const countdown = ref(0)
const flash = ref('') // 快捷键提示
const bubble = ref(null) // 中央反馈气泡 { icon, key }
let bubbleSeq = 0
function showBubble(icon, text) {
  bubble.value = { icon, text, key: ++bubbleSeq }
  clearTimeout(showBubble.t)
  showBubble.t = setTimeout(() => (bubble.value = null), 600)
}

const psid = randomId()
let hideTimer = null
let reportTimer = null
let countdownTimer = null
let lastReport = 0

const pct = computed(() => (dur.value ? (cur.value / dur.value) * 100 : 0))
const trick = computed(() => data.value?.trickplay)

async function load() {
  try {
    data.value = await rpc('play.open', { id: Number(props.id) })
    document.title = data.value.item.title
    const d = data.value.item.duration
    if (d) dur.value = d
  } catch (e) {
    err.value = e.message
  }
}

function onLoaded() {
  const v = video.value
  if (!v) return
  v.volume = volume.value
  v.muted = muted.value
  if (isFinite(v.duration)) dur.value = v.duration
  const t = Number(route.query.t)
  if (t > 0 && t < v.duration - 5) v.currentTime = t
  v.play().catch(() => {
    // 浏览器禁止有声自动播放时，静音后再试
    v.muted = true
    muted.value = true
    v.play().catch(() => {})
  })
}

function report(final) {
  const v = video.value
  if (!v || !data.value || !(v.currentTime > 0)) return
  const p = { id: data.value.item.id, psid, position: v.currentTime, duration: v.duration || dur.value }
  if (final) beacon('play.report', p)
  else rpc('play.report', p).catch(() => {})
  lastReport = Date.now()
}

function onTime() {
  const v = video.value
  if (!v) return // 卸载过程中仍可能触发事件
  cur.value = v.currentTime
  if (playing.value && Date.now() - lastReport > 10000) report()
}
function onProgress() {
  const v = video.value
  if (!v) return
  const out = []
  for (let i = 0; i < v.buffered.length; i++) out.push([v.buffered.start(i), v.buffered.end(i)])
  buffered.value = out
}
function onPlay() {
  playing.value = true
  ended.value = false
  poke()
}
function onPause() {
  playing.value = false
  showUI.value = true
  report()
}
function onEnded() {
  playing.value = false
  ended.value = true
  showUI.value = true
  report()
  if (data.value?.next) {
    countdown.value = 10
    countdownTimer = setInterval(() => {
      countdown.value--
      if (countdown.value <= 0) playNext()
    }, 1000)
  }
}
function onError() {
  const code = video.value?.error?.code
  err.value = code === 4 ? '浏览器不支持这个视频的格式或编码（例如 HEVC），无法播放' : '播放出错：视频数据加载失败或校验未通过'
}

function toggle() {
  const v = video.value
  if (!v) return
  if (v.paused || v.ended) {
    v.play()
    showBubble('play')
  } else {
    v.pause()
    showBubble('pause')
  }
}
function seek(t) {
  const v = video.value
  if (!v) return
  v.currentTime = Math.max(0, Math.min(t, (v.duration || dur.value) - 0.5))
  cur.value = v.currentTime
}
function skip(d) {
  if (!video.value) return
  seek(video.value.currentTime + d)
  showBubble(d > 0 ? 'forward' : 'rewind', (d > 0 ? '+' : '') + d + ' 秒')
}
function setVolume(v) {
  volume.value = Math.max(0, Math.min(1, v))
  if (video.value) video.value.volume = volume.value
  if (volume.value > 0 && muted.value) toggleMute(false)
  localStorage.setItem('vf.volume', volume.value)
}
function toggleMute(force) {
  muted.value = typeof force === 'boolean' ? force : !muted.value
  if (video.value) video.value.muted = muted.value
  localStorage.setItem('vf.muted', muted.value ? '1' : '0')
}
function setRate(r) {
  rate.value = r
  video.value.playbackRate = r
  menu.value = ''
  showFlash(r + '×')
}
function setSub(i) {
  subIndex.value = i
  const tracks = video.value.textTracks
  for (let j = 0; j < tracks.length; j++) tracks[j].mode = j === i ? 'showing' : 'disabled'
  menu.value = ''
}
async function toggleFullscreen() {
  try {
    if (document.fullscreenElement) await document.exitFullscreen()
    else await root.value.requestFullscreen()
  } catch {}
}
function onFsChange() {
  fullscreen.value = !!document.fullscreenElement
}
function showFlash(s) {
  flash.value = s
  clearTimeout(showFlash.t)
  showFlash.t = setTimeout(() => (flash.value = ''), 700)
}

// 控制栏自动隐藏
function poke() {
  showUI.value = true
  clearTimeout(hideTimer)
  hideTimer = setTimeout(() => {
    if (playing.value && !menu.value) showUI.value = false
  }, 3000)
}

// 进度条
function barTime(e) {
  const r = bar.value.getBoundingClientRect()
  const x = Math.max(0, Math.min(e.clientX - r.left, r.width))
  return { x, t: (x / r.width) * dur.value, w: r.width }
}
let dragging = false
function onBarDown(e) {
  dragging = true
  seek(barTime(e).t)
  window.addEventListener('pointermove', onBarDrag)
  window.addEventListener('pointerup', onBarUp, { once: true })
}
function onBarDrag(e) {
  if (dragging) {
    const b = barTime(e)
    cur.value = b.t
    hover.value = b
  }
}
function onBarUp(e) {
  dragging = false
  window.removeEventListener('pointermove', onBarDrag)
  if (bar.value) seek(barTime(e).t)
}
function onBarMove(e) {
  hover.value = barTime(e)
}

// 进度条预览图：雪碧图中第 n 张
const preview = computed(() => {
  const tp = trick.value
  const h = hover.value
  if (!tp || !h || !tp.sprites?.length) return null
  const per = tp.cols * tp.rows
  const n = Math.min(Math.floor(h.t / tp.interval), tp.count - 1)
  const sheet = Math.floor(n / per)
  const k = n % per
  if (!tp.sprites[sheet]) return null
  return {
    backgroundImage: `url("${tp.sprites[sheet]}")`,
    backgroundPosition: `-${(k % tp.cols) * tp.width}px -${Math.floor(k / tp.cols) * tp.height}px`,
    width: tp.width + 'px',
    height: tp.height + 'px',
  }
})
const hoverLeft = computed(() => {
  const h = hover.value
  if (!h) return 0
  const half = (trick.value?.width || 60) / 2
  return Math.max(half, Math.min(h.x, h.w - half))
})

function onKey(e) {
  if (e.target.tagName === 'INPUT' || e.ctrlKey || e.metaKey || e.altKey) return
  const v = video.value
  if (!v) return
  switch (e.key) {
    case ' ':
    case 'k':
      toggle()
      break
    case 'ArrowLeft':
    case 'j':
      skip(e.shiftKey ? -60 : -10)
      break
    case 'ArrowRight':
    case 'l':
      skip(e.shiftKey ? 60 : 10)
      break
    case 'ArrowUp':
      setVolume(volume.value + 0.1)
      showFlash('音量 ' + Math.round(volume.value * 100) + '%')
      break
    case 'ArrowDown':
      setVolume(volume.value - 0.1)
      showFlash('音量 ' + Math.round(volume.value * 100) + '%')
      break
    case 'm':
      toggleMute()
      showFlash(muted.value ? '静音' : '取消静音')
      break
    case 'f':
      toggleFullscreen()
      break
    case 'c':
      if (data.value?.subtitles.length) setSub(subIndex.value + 1 >= data.value.subtitles.length ? -1 : subIndex.value + 1)
      break
    case 'Escape':
      if (menu.value) menu.value = ''
      else if (!document.fullscreenElement) back()
      break
    default:
      if (/^[0-9]$/.test(e.key)) seek((Number(e.key) / 10) * dur.value)
      else return
  }
  e.preventDefault()
  poke()
}

function back() {
  report()
  if (window.history.state?.back) router.back()
  else router.replace(`/item/${props.id}`)
}
function playNext() {
  clearInterval(countdownTimer)
  if (data.value?.next) router.replace(`/play/${data.value.next.id}`)
}
function cancelNext() {
  clearInterval(countdownTimer)
  countdown.value = 0
}
function onUnload() {
  report(true)
}

onMounted(() => {
  load()
  document.addEventListener('keydown', onKey)
  document.addEventListener('fullscreenchange', onFsChange)
  window.addEventListener('pagehide', onUnload)
  reportTimer = setInterval(() => playing.value && report(), 15000)
})
onBeforeUnmount(() => {
  report(true)
  document.removeEventListener('keydown', onKey)
  document.removeEventListener('fullscreenchange', onFsChange)
  window.removeEventListener('pagehide', onUnload)
  clearInterval(reportTimer)
  clearInterval(countdownTimer)
  clearTimeout(hideTimer)
  if (document.fullscreenElement) document.exitFullscreen().catch(() => {})
  document.title = 'Veilfin'
})
watch(menu, (m) => m && (showUI.value = true))
</script>

<template>
  <div ref="root" class="player" :class="{ 'hide-ui': !showUI }" @mousemove="poke" @touchstart.passive="poke">
    <video
      v-if="data"
      ref="video"
      :src="data.video"
      playsinline
      preload="auto"
      @loadedmetadata="onLoaded"
      @timeupdate="onTime"
      @progress="onProgress"
      @play="onPlay"
      @pause="onPause"
      @ended="onEnded"
      @waiting="waiting = true"
      @playing="waiting = false"
      @canplay="waiting = false"
      @error="onError"
      @click="toggle"
      @dblclick="toggleFullscreen"
    >
      <track v-for="(s, i) in data.subtitles" :key="i" kind="subtitles" :src="s.src" :srclang="s.lang || 'und'" :label="s.label" />
    </video>

    <Transition name="fade">
    <div v-if="err" class="center-msg err">
      <p>{{ err }}</p>
      <button class="btn" @click="back">返回</button>
    </div>
    <div v-else-if="waiting && !ended" class="center-msg"><div class="spinner" /></div>
    </Transition>
    <Transition name="flash">
      <div v-if="flash" :key="flash" class="flash">{{ flash }}</div>
    </Transition>
    <Transition name="bubble">
      <div v-if="bubble" :key="bubble.key" class="bubble">
        <Icon :name="bubble.icon" :size="40" />
        <span v-if="bubble.text">{{ bubble.text }}</span>
      </div>
    </Transition>

    <Transition name="slide-up">
    <div v-if="ended && data?.next && countdown > 0" class="next-card">
      <div class="muted">即将播放下一个（{{ countdown }}）</div>
      <div class="next-title">{{ data.next.title }}</div>
      <div class="row">
        <button class="btn primary" @click="playNext"><Icon name="play" /> 立即播放</button>
        <button class="btn" @click="cancelNext">取消</button>
      </div>
      <div class="count-bar"><div :style="{ animationDuration: '10s' }" /></div>
    </div>
    </Transition>

    <div class="top ui">
      <button class="icon-btn" title="返回 (Esc)" @click="back"><Icon name="back" /></button>
      <div class="ptitle">
        <div>{{ data?.item.title }}</div>
        <div v-if="data?.item.code && data.item.code !== data.item.title" class="muted small">{{ data.item.code }}</div>
      </div>
    </div>

    <div class="bottom ui" @click.stop>
      <div ref="bar" class="bar" @pointerdown="onBarDown" @pointermove="onBarMove" @pointerleave="hover = null">
        <div class="track">
          <div v-for="(b, i) in buffered" :key="i" class="buf" :style="{ left: (b[0] / dur) * 100 + '%', width: ((b[1] - b[0]) / dur) * 100 + '%' }" />
          <div class="fill" :style="{ width: pct + '%' }" />
          <div class="knob" :style="{ left: pct + '%' }" />
        </div>
        <Transition name="tip">
        <div v-if="hover" class="tip" :style="{ left: hoverLeft + 'px' }">
          <div v-if="preview" class="thumb" :style="preview" />
          <div class="tip-time">{{ fmtTime(hover.t) }}</div>
        </div>
        </Transition>
      </div>

      <div class="controls">
        <button class="icon-btn" title="后退 10 秒 (←)" @click="skip(-10)"><Icon name="rewind" /></button>
        <button class="icon-btn big" :title="playing ? '暂停 (空格)' : '播放 (空格)'" @click="toggle">
          <Transition name="swap" mode="out-in"><Icon :key="playing ? 'p' : 's'" :name="playing ? 'pause' : 'play'" :size="34" /></Transition>
        </button>
        <button class="icon-btn" title="前进 10 秒 (→)" @click="skip(10)"><Icon name="forward" /></button>
        <button v-if="data?.next" class="icon-btn" :title="'下一个：' + data.next.title" @click="playNext"><Icon name="next" /></button>
        <div class="time">{{ fmtTime(cur) }} / {{ fmtTime(dur) }}</div>
        <div class="spacer" />
        <div class="vol">
          <button class="icon-btn" title="静音 (M)" @click="toggleMute()"><Icon :name="muted || volume === 0 ? 'mute' : 'volume'" /></button>
          <input type="range" min="0" max="1" step="0.05" :value="muted ? 0 : volume" @input="setVolume(Number($event.target.value))" />
        </div>
        <div v-if="data?.subtitles.length" class="pop">
          <button class="icon-btn" :class="{ on: subIndex >= 0 }" title="字幕 (C)" @click="menu = menu === 'subs' ? '' : 'subs'"><Icon name="subtitles" /></button>
          <Transition name="pop"><div v-if="menu === 'subs'" class="menu up">
            <button :class="{ sel: subIndex < 0 }" @click="setSub(-1)">关闭字幕</button>
            <button v-for="(s, i) in data.subtitles" :key="i" :class="{ sel: subIndex === i }" @click="setSub(i)">{{ s.label }}</button>
          </div></Transition>
        </div>
        <div class="pop">
          <button class="icon-btn" title="播放速度" @click="menu = menu === 'speed' ? '' : 'speed'"><Icon name="speed" /></button>
          <Transition name="pop"><div v-if="menu === 'speed'" class="menu up">
            <button v-for="r in [0.5, 0.75, 1, 1.25, 1.5, 2]" :key="r" :class="{ sel: rate === r }" @click="setRate(r)">{{ r }}×</button>
          </div></Transition>
        </div>
        <button class="icon-btn" title="全屏 (F)" @click="toggleFullscreen"><Icon :name="fullscreen ? 'exitFullscreen' : 'fullscreen'" /></button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.player { position: fixed; inset: 0; background: #000; overflow: hidden; user-select: none; }
.player.hide-ui { cursor: none; }
video { width: 100%; height: 100%; display: block; background: #000; }
video::cue { background: rgba(0,0,0,.55); font-size: 1.15em; }
.ui { position: absolute; left: 0; right: 0; transition: opacity .45s var(--ease-out), transform .6s var(--spring); z-index: 2; }
.hide-ui .ui { opacity: 0; pointer-events: none; }
.hide-ui .top { transform: translate3d(0, -12px, 0); }
.hide-ui .bottom { transform: translate3d(0, 12px, 0); }
.top { top: 0; display: flex; align-items: center; gap: 10px; padding: 14px 18px 40px; background: linear-gradient(rgba(0,0,0,.75), transparent); }
.ptitle { font-size: 18px; }
.small { font-size: 12px; }
.bottom { bottom: 0; padding: 40px 22px 14px; background: linear-gradient(transparent, rgba(0,0,0,.85)); }
.bar { position: relative; height: 22px; display: flex; align-items: center; cursor: pointer; touch-action: none; }
.track { position: relative; width: 100%; height: 4px; background: rgba(255,255,255,.2); border-radius: 3px; transition: height .3s var(--spring); }
.bar:hover .track { height: 6px; }
.buf { position: absolute; top: 0; bottom: 0; background: rgba(255,255,255,.35); }
.fill { position: absolute; left: 0; top: 0; bottom: 0; background: var(--accent); border-radius: 2px; }
.knob { position: absolute; top: 50%; width: 14px; height: 14px; margin: -7px 0 0 -7px; border-radius: 50%; background: #fff; transform: scale(0);
  transition: transform .4s var(--spring-bouncy); box-shadow: 0 1px 6px rgba(0,0,0,.4); }
.bar:active .knob { transform: scale(1.25); }
.buf { transition: width .4s var(--ease-out); }
.bar:hover .knob { transform: scale(1); }
.tip { position: absolute; bottom: 26px; transform: translateX(-50%); display: grid; justify-items: center; gap: 4px; pointer-events: none; }
.thumb { border: 2px solid rgba(255,255,255,.9); border-radius: 8px; background-color: #000; box-shadow: 0 8px 24px rgba(0,0,0,.6); }
.tip-enter-active { transition: opacity .2s var(--ease-out), transform .4s var(--spring-bouncy); }
.tip-leave-active { transition: opacity .12s, transform .12s; }
.tip-enter-from, .tip-leave-to { opacity: 0; transform: translateX(-50%) translateY(8px) scale(.92); }
.tip-time { padding: 2px 10px; background: rgba(20,20,20,.75); border-radius: 999px; font-size: 13px; backdrop-filter: blur(10px); }
.controls { display: flex; align-items: center; gap: 4px; margin-top: 4px; }
.big { width: 52px; height: 52px; }
.time { margin-left: 10px; font-variant-numeric: tabular-nums; font-size: 14px; }
.vol { display: flex; align-items: center; }
.vol input { width: 90px; accent-color: var(--accent); }
.pop { position: relative; }
.menu.up { bottom: 48px; right: 0; top: auto; min-width: 150px; }
.menu .sel { color: var(--accent); }
.on { color: var(--accent); }
.center-msg { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 16px; pointer-events: none; z-index: 1; }
.center-msg.err { pointer-events: auto; background: rgba(0,0,0,.7); text-align: center; padding: 20px; }
.flash { position: absolute; top: 18%; left: 50%; transform: translate(-50%, -50%); padding: 10px 22px; border-radius: 999px; background: rgba(30,30,30,.6);
  font-size: 17px; z-index: 3; pointer-events: none; backdrop-filter: saturate(180%) blur(20px); }
.flash-enter-active { transition: opacity .2s, transform .45s var(--spring-bouncy); }
.flash-leave-active { transition: opacity .3s, transform .3s; }
.flash-enter-from { opacity: 0; transform: translate(-50%, -50%) scale(.8); }
.flash-leave-to { opacity: 0; transform: translate(-50%, -60%); }
/* 中央反馈气泡：弹出后放大淡出 */
.bubble { position: absolute; top: 50%; left: 50%; width: 92px; height: 92px; margin: -46px 0 0 -46px; border-radius: 50%; z-index: 3; pointer-events: none;
  display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 2px; font-size: 12px;
  background: rgba(30,30,30,.5); backdrop-filter: saturate(180%) blur(16px); -webkit-backdrop-filter: saturate(180%) blur(16px); }
.bubble-enter-active { transition: opacity .15s, transform .4s var(--spring-bouncy); }
.bubble-leave-active { transition: opacity .45s var(--ease-out), transform .55s var(--ease-out); }
.bubble-enter-from { opacity: 0; transform: scale(.6); }
.bubble-leave-to { opacity: 0; transform: scale(1.35); }
.swap-enter-active { transition: opacity .15s, transform .35s var(--spring-bouncy); }
.swap-leave-active { transition: opacity .1s, transform .1s; }
.swap-enter-from { opacity: 0; transform: scale(.5); }
.swap-leave-to { opacity: 0; transform: scale(.5); }
.count-bar { height: 3px; border-radius: 2px; background: rgba(255,255,255,.15); overflow: hidden; }
.count-bar div { height: 100%; background: var(--accent); transform-origin: left; animation: count-down linear forwards; }
@keyframes count-down { from { transform: scaleX(1); } to { transform: scaleX(0); } }
.next-card { position: absolute; right: 30px; bottom: 120px; z-index: 3; background: rgba(30,30,30,.72); padding: 18px 20px; border-radius: 16px; display: grid; gap: 10px;
  max-width: 360px; backdrop-filter: saturate(180%) blur(24px); -webkit-backdrop-filter: saturate(180%) blur(24px); box-shadow: 0 20px 50px rgba(0,0,0,.5); }
.next-title { font-size: 17px; }
@media (max-width: 600px) { .vol input { display: none; } .bottom { padding: 30px 10px 8px; } }
</style>
