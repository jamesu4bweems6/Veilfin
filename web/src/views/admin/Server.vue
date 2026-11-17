<script setup>
import { ref, onMounted } from 'vue'
import { state, rpc, toast, toastError } from '../../store.js'

const s = ref(null)
const ffmpeg = ref(true)
const busy = ref(false)

onMounted(async () => {
  try {
    const r = await rpc('admin.settings.get')
    s.value = r.settings
    ffmpeg.value = r.ffmpeg
  } catch (e) {
    toastError(e)
  }
})

async function save() {
  busy.value = true
  try {
    s.value = await rpc('admin.settings.set', { ...s.value, scanIntervalHours: Number(s.value.scanIntervalHours) || 0 })
    state.serverName = s.value.serverName
    toast('设置已保存', 'ok')
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="admin-body">
    <p v-if="!ffmpeg" class="warn card-panel">服务端没有找到 ffmpeg / ffprobe：视频可以播放，但不会生成截图、字幕和进度条预览图。请安装 ffmpeg 或启动时用 -ffmpeg 指定目录。</p>
    <form v-if="s" class="settings" @submit.prevent="save">
      <section class="card-panel d1">
        <h2>常规</h2>
        <div class="field"><label>服务器名称</label><input v-model="s.serverName" class="input" /></div>
        <div class="field">
          <label>定时扫描间隔（小时，0 表示关闭）</label>
          <input v-model="s.scanIntervalHours" class="input short" inputmode="numeric" />
        </div>
        <label class="check"><input v-model="s.watchEnabled" type="checkbox" /> 监听目录变化，自动扫描新增 / 删除的文件</label>
        <label class="check"><input v-model="s.trickplayEnabled" type="checkbox" /> 生成进度条预览图（只解码关键帧，3 小时视频约需几十秒）</label>
      </section>

      <section class="card-panel d2">
        <h2>TMDB（普通电影）</h2>
        <div class="field">
          <label>API Key 或读访问令牌</label>
          <input v-model="s.tmdbKey" class="input" type="password" autocomplete="off" placeholder="在 themoviedb.org 的账户设置中申请" />
        </div>
        <div class="field"><label>语言</label><input v-model="s.tmdbLang" class="input short" placeholder="zh-CN" /></div>
      </section>

      <section class="card-panel d3">
        <h2>MetaTube（番号刮削）</h2>
        <div class="field">
          <label>服务地址</label>
          <input v-model="s.metatubeUrl" class="input" placeholder="http://127.0.0.1:8080" />
          <span class="hint">需要自行部署 MetaTube 服务端（metatube-community/metatube-sdk-go），本程序通过它的 HTTP API 查询。</span>
        </div>
        <div class="field"><label>访问令牌（可选）</label><input v-model="s.metatubeToken" class="input" type="password" autocomplete="off" /></div>
      </section>

      <section class="card-panel d4">
        <h2>网络</h2>
        <div class="field">
          <label>刮削代理（可选）</label>
          <input v-model="s.scraperProxy" class="input" placeholder="http://127.0.0.1:7890 或 socks5://127.0.0.1:1080" />
          <span class="hint">只用于在线刮削和下载封面，不影响视频播放。</span>
        </div>
      </section>

      <div class="actions"><button class="btn primary big" :disabled="busy">保存设置</button></div>
    </form>
  </div>
</template>

<style scoped>
.settings { max-width: 760px; display: grid; gap: 14px; }
h2 { font-size: 16px; margin-bottom: 14px; }
.short { max-width: 200px; }
.check { display: flex; margin-bottom: 8px; }
.warn { color: var(--warn); margin-bottom: 14px; }
</style>
