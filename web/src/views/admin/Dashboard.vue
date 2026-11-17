<script setup>
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
import { rpc, toast, toastError } from '../../store.js'
import { fmtSize, fmtDateTime, fmtDate } from '../../util.js'

const stats = ref(null)
const tasks = ref([])
const days = ref(30)
const hover = ref(-1)
const showTable = ref(false)
let timer = null

async function loadStats() {
  try {
    stats.value = await rpc('admin.stats', { days: days.value })
  } catch (e) {
    toastError(e)
  }
}
async function loadTasks() {
  try {
    tasks.value = await rpc('admin.tasks')
  } catch {}
}
onMounted(() => {
  loadStats()
  loadTasks()
  timer = setInterval(loadTasks, 3000)
})
onBeforeUnmount(() => clearInterval(timer))

async function scanAll(refresh) {
  try {
    await rpc('admin.libs.scan', { id: 0, refresh })
    toast(refresh ? '已开始刷新全部元数据' : '已开始扫描全部媒体库', 'ok')
    loadTasks()
  } catch (e) {
    toastError(e)
  }
}

// 时长：不到 1 小时显示分钟
function fmtWatch(sec) {
  if (sec < 3600) return Math.round(sec / 60) + ' 分钟'
  return (sec / 3600).toFixed(1) + ' 小时'
}
function compact(n) {
  if (n >= 10000) return (n / 1000).toFixed(n >= 100000 ? 0 : 1) + 'K'
  return n.toLocaleString('zh-CN', { maximumFractionDigits: 1 })
}

// 补齐没有观看记录的日期，柱子按天连续排列
const series = computed(() => {
  const map = new Map((stats.value?.days || []).map((d) => [d.day, d]))
  const out = []
  const now = Date.now() / 1000
  for (let i = days.value - 1; i >= 0; i--) {
    const day = fmtDate(now - i * 86400)
    const d = map.get(day)
    out.push({ day, sec: d ? d.watched : 0, plays: d ? d.plays : 0 })
  }
  return out
})

// 峰值不到 1 小时就用分钟作单位，避免刻度全是 0
const unit = computed(() => (Math.max(...series.value.map((d) => d.sec), 0) >= 3600 ? { name: '小时', div: 3600 } : { name: '分钟', div: 60 }))
const values = computed(() => series.value.map((d) => d.sec / unit.value.div))

// 纵轴：取整到“好看”的刻度
const axis = computed(() => {
  const max = Math.max(...values.value, 0)
  if (max === 0) return { top: 1, ticks: [0, 0.5, 1] }
  const raw = max / 4
  const mag = Math.pow(10, Math.floor(Math.log10(raw)))
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw)
  const top = Math.ceil(max / step) * step
  const ticks = []
  for (let v = 0; v <= top + 1e-9; v += step) ticks.push(+v.toFixed(4))
  return { top, ticks }
})
const totalSec = computed(() => series.value.reduce((a, d) => a + d.sec, 0))
const totalPlays = computed(() => series.value.reduce((a, d) => a + d.plays, 0))
const peak = computed(() => values.value.reduce((best, v, i) => (v > values.value[best] ? i : best), 0))
</script>

<template>
  <div class="admin-body">

    <div v-if="stats" class="tiles">
      <div class="tile rise d0">
        <div class="t-label">媒体数量</div>
        <div class="t-value">{{ compact(stats.itemCount) }}</div>
      </div>
      <div class="tile rise d1">
        <div class="t-label">视频总时长</div>
        <div class="t-value">{{ fmtWatch(stats.totalDuration).split(' ')[0] }} <small>{{ fmtWatch(stats.totalDuration).split(' ')[1] }}</small></div>
      </div>
      <div class="tile rise d2">
        <div class="t-label">占用空间</div>
        <div class="t-value">{{ fmtSize(stats.totalSize) }}</div>
      </div>
      <div class="tile rise d3">
        <div class="t-label">近 {{ days }} 天观看</div>
        <div class="t-value">{{ fmtWatch(totalSec).split(' ')[0] }} <small>{{ fmtWatch(totalSec).split(' ')[1] }} · {{ totalPlays }} 次</small></div>
      </div>
    </div>

    <section v-if="stats" class="card-panel">
      <div class="chart-head">
        <h2>每日观看时长（{{ unit.name }}）</h2>
        <div class="spacer" />
        <select v-model.number="days" class="input sel" @change="loadStats">
          <option :value="7">最近 7 天</option>
          <option :value="30">最近 30 天</option>
          <option :value="90">最近 90 天</option>
        </select>
        <button class="btn" @click="showTable = !showTable">{{ showTable ? '图表' : '表格' }}</button>
      </div>

      <div v-if="!showTable" class="chart" role="img" :aria-label="`最近 ${days} 天每日观看时长柱状图，共 ${fmtWatch(totalSec)}`">
        <div class="yaxis">
          <span v-for="t in axis.ticks" :key="t" :style="{ bottom: (t / axis.top) * 100 + '%' }">{{ compact(t) }}</span>
        </div>
        <div class="plot">
          <div v-for="t in axis.ticks" :key="t" class="grid-line" :style="{ bottom: (t / axis.top) * 100 + '%' }" />
          <div class="cols">
            <div v-for="(d, i) in series" :key="d.day" class="col" @mouseenter="hover = i" @mouseleave="hover = -1">
              <div class="bar" :class="{ dim: hover >= 0 && hover !== i }" :style="{ height: (values[i] / axis.top) * 100 + '%', animationDelay: i * 14 + 'ms' }" />
              <div v-if="i === peak && values[i] > 0 && hover < 0" class="peak-label" :style="{ bottom: (values[i] / axis.top) * 100 + '%' }">{{ compact(values[i]) }}</div>
              <div v-if="hover === i" class="tooltip" :class="{ left: i > series.length * 0.7 }">
                <div class="tt-day">{{ d.day }}</div>
                <div><b>{{ fmtWatch(d.sec) }}</b> · {{ d.plays }} 次播放</div>
              </div>
            </div>
          </div>
          <div class="xaxis">
            <span>{{ series[0]?.day.slice(5) }}</span>
            <span>{{ series[Math.floor(series.length / 2)]?.day.slice(5) }}</span>
            <span>{{ series[series.length - 1]?.day.slice(5) }}</span>
          </div>
        </div>
      </div>
      <div v-else class="table-wrap">
        <table class="list">
          <thead><tr><th>日期</th><th>观看时长</th><th>播放次数</th></tr></thead>
          <tbody>
            <tr v-for="d in [...series].reverse()" :key="d.day"><td>{{ d.day }}</td><td>{{ fmtWatch(d.sec) }}</td><td>{{ d.plays }}</td></tr>
          </tbody>
        </table>
      </div>
    </section>

    <div class="two">
      <section v-if="stats" class="card-panel">
        <h2>用户观看排行（近 {{ days }} 天）</h2>
        <table class="list">
          <thead><tr><th>用户</th><th>观看时长</th><th>次数</th><th>最近播放</th></tr></thead>
          <tbody>
            <tr v-for="u in stats.users" :key="u.userId">
              <td>{{ u.username }}</td><td>{{ fmtWatch(u.watched) }}</td><td>{{ u.plays }}</td>
              <td class="muted">{{ u.lastPlay ? fmtDateTime(u.lastPlay) : '—' }}</td>
            </tr>
          </tbody>
        </table>
        <h2 class="mt">最常播放</h2>
        <table class="list">
          <thead><tr><th>标题</th><th>次数</th><th>观看时长</th></tr></thead>
          <tbody>
            <tr v-for="t in stats.top" :key="t.itemId">
              <td><router-link :to="`/item/${t.itemId}`" class="lnk">{{ t.title }}</router-link></td><td>{{ t.plays }}</td><td>{{ fmtWatch(t.watched) }}</td>
            </tr>
            <tr v-if="!stats.top?.length"><td colspan="3" class="muted">暂无数据</td></tr>
          </tbody>
        </table>
      </section>

      <section class="card-panel">
        <div class="chart-head">
          <h2>后台任务</h2>
          <div class="spacer" />
          <button class="btn" @click="scanAll(false)">扫描全部</button>
          <button class="btn" @click="scanAll(true)">刷新全部元数据</button>
        </div>
        <div v-for="q in tasks" :key="q.name" class="queue">
          <div class="qhead">
            <b>{{ q.name }}</b>
            <span class="muted">排队 {{ q.pending }} · 已完成 {{ q.done }}<span v-if="q.failed"> · 失败 {{ q.failed }}</span></span>
          </div>
          <div v-for="r in q.running || []" :key="r" class="running"><span class="dot" /> {{ r }}</div>
          <div v-if="!q.running?.length && !q.pending" class="muted small">空闲</div>
          <details v-if="q.errors?.length" class="errs">
            <summary class="muted small">最近的错误（{{ q.errors.length }}）</summary>
            <div v-for="(e, i) in [...q.errors].reverse()" :key="i" class="small"><span class="muted">{{ fmtDateTime(e.time) }}</span> {{ e.label }}：{{ e.err }}</div>
          </details>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.tiles { display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 14px; margin-bottom: 16px; }
.tile { background: var(--panel); border-radius: 12px; padding: 16px 18px; }
.t-label { color: var(--muted); font-size: 13px; }
.t-value { font-size: 26px; font-weight: 600; margin-top: 4px; font-variant-numeric: tabular-nums; }
.t-value small { font-size: 13px; font-weight: normal; color: var(--muted); }
h2 { font-size: 16px; }
.mt { margin-top: 22px; margin-bottom: 8px; }
.chart-head { display: flex; align-items: center; gap: 10px; margin-bottom: 14px; flex-wrap: wrap; }
.sel { width: auto; }
.chart { display: flex; gap: 8px; height: 220px; }
.yaxis { position: relative; width: 36px; flex-shrink: 0; margin-bottom: 22px; }
.yaxis span { position: absolute; right: 0; transform: translateY(50%); font-size: 11px; color: var(--muted); font-variant-numeric: tabular-nums; }
.plot { position: relative; flex: 1; display: flex; flex-direction: column; min-width: 0; }
.grid-line { position: absolute; left: 0; right: 0; height: 1px; background: #2c2c2c; margin-bottom: 22px; pointer-events: none; }
.cols { position: relative; flex: 1; display: flex; align-items: flex-end; gap: 2px; }
.col { position: relative; flex: 1; height: 100%; display: flex; align-items: flex-end; justify-content: center; cursor: default; }
.bar { width: 100%; max-width: 24px; background: var(--accent); border-radius: 4px 4px 0 0; min-height: 0; transform-origin: bottom;
  transition: opacity .2s, height .6s var(--spring); animation: bar-grow .8s var(--spring) backwards; }
@keyframes bar-grow { from { transform: scaleY(0); } }
.bar.dim { opacity: .45; }
.peak-label { position: absolute; transform: translateY(-4px); font-size: 11px; color: var(--text); white-space: nowrap; pointer-events: none; }
.tooltip { position: absolute; bottom: calc(100% - 40px); left: 50%; transform: translateX(-10%); z-index: 5; white-space: nowrap; pointer-events: none;
  background: rgba(43,43,43,.85); border: 0.5px solid rgba(255,255,255,.1); border-radius: 10px; padding: 8px 10px; font-size: 12.5px; box-shadow: 0 10px 24px rgba(0,0,0,.5);
  backdrop-filter: blur(16px); animation: tip-in .3s var(--spring-bouncy) backwards; }
@keyframes tip-in { from { opacity: 0; translate: 0 6px; scale: .94; } }
.tooltip.left { transform: translateX(-90%); }
.tt-day { color: var(--muted); margin-bottom: 2px; }
.xaxis { height: 22px; display: flex; justify-content: space-between; align-items: flex-end; font-size: 11px; color: var(--muted); }
.table-wrap { max-height: 320px; overflow-y: auto; }
.two { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; margin-top: 16px; }
@media (max-width: 900px) { .two { grid-template-columns: 1fr; } }
.lnk:hover { color: var(--accent); }
.queue { padding: 12px 0; border-top: 1px solid var(--line); }
.qhead { display: flex; justify-content: space-between; margin-bottom: 6px; }
.running { display: flex; align-items: center; gap: 8px; font-size: 13px; padding: 2px 0; }
.dot { width: 8px; height: 8px; border-radius: 50%; background: var(--accent); animation: pulse 1s infinite alternate; }
@keyframes pulse { to { opacity: .3; } }
.small { font-size: 12.5px; }
.errs { margin-top: 6px; }
.errs div { padding: 3px 0; word-break: break-all; }
</style>
