<script setup>
import { ref, onMounted } from 'vue'
import { state, rpc, toast, toastError, connectionStatus } from '../store.js'
import { fmtDateTime } from '../util.js'
import Icon from '../components/Icon.vue'

const conn = ref(null)
const pw = ref({ old: '', new1: '', new2: '' })
const pin = ref({ password: '', pin: '' })
const busy = ref(false)

onMounted(async () => {
  try {
    conn.value = await connectionStatus()
  } catch {}
})

async function changePassword() {
  if (pw.value.new1 !== pw.value.new2) {
    toastError(new Error('两次输入的新密码不一致'))
    return
  }
  busy.value = true
  try {
    state.me = await rpc('auth.changePassword', { old: pw.value.old, new: pw.value.new1 })
    pw.value = { old: '', new1: '', new2: '' }
    toast('密码已修改，其他设备上的登录已失效', 'ok')
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = false
  }
}

async function savePin(remove) {
  busy.value = true
  try {
    state.me = await rpc('auth.setPin', { password: pin.value.password, pin: remove ? '' : pin.value.pin })
    pin.value = { password: '', pin: '' }
    toast(remove ? '已移除 PIN，受限内容不再需要解锁' : 'PIN 已设置，受限内容需要解锁后才能看到', 'ok')
  } catch (e) {
    toastError(e)
  } finally {
    busy.value = false
  }
}

async function setBlur(v) {
  try {
    state.me = await rpc('auth.setPrefs', { blurRestricted: v })
  } catch (e) {
    toastError(e)
  }
}
</script>

<template>
  <div class="page settings">
    <h1 class="page-title">设置</h1>

    <section class="card-panel d1">
      <h2>连接安全</h2>
      <div v-if="conn" class="kv">
        <span>模式</span><span>{{ conn.mode === 'local' ? '本地客户端（前端代码不经过 CDN）' : '网页模式（浏览器内加密，Service Worker 解密）' }}</span>
        <span>服务端</span><span>{{ conn.server }}</span>
        <span>公钥指纹</span><code>{{ conn.fingerprint }}</code>
        <span>加密套件</span><span>{{ conn.suite }}</span>
        <span>会话</span><span>{{ conn.connected ? `已建立 ${conn.sessionAge} 秒，发送密钥第 ${conn.sendEpoch} 代` : '未连接：' + (conn.error || '') }}</span>
      </div>
      <p v-if="conn?.warning" class="warn">{{ conn.warning }}</p>
      <p class="hint">请把公钥指纹和服务端启动日志里打印的指纹对比，一致才说明连接没有被中间人替换。</p>
    </section>

    <section class="card-panel d2">
      <h2>受限内容</h2>
      <p class="muted">设置 PIN 后，受限媒体库默认隐藏，需要输入 PIN 解锁（2 小时内有效）。当前：
        <b>{{ state.me.hasPin ? (state.me.unlocked ? `已解锁，到 ${fmtDateTime(state.me.unlockedUntil)}` : '已锁定') : '未设置 PIN' }}</b>
      </p>
      <form class="row" @submit.prevent="savePin(false)">
        <input v-model="pin.password" class="input w" type="password" placeholder="当前登录密码" autocomplete="current-password" required />
        <input v-model="pin.pin" class="input w" type="password" inputmode="numeric" placeholder="新 PIN（4-8 位数字）" autocomplete="off" />
        <button class="btn primary" :disabled="busy || !pin.pin">{{ state.me.hasPin ? '修改 PIN' : '设置 PIN' }}</button>
        <button v-if="state.me.hasPin" type="button" class="btn" :disabled="busy || !pin.password" @click="savePin(true)">移除 PIN</button>
      </form>
      <label class="check top"><input type="checkbox" :checked="state.me.blurRestricted" @change="setBlur($event.target.checked)" /> 模糊显示受限内容的封面（鼠标悬停时显示）</label>
    </section>

    <section class="card-panel d3">
      <h2>修改密码</h2>
      <form class="pwform" @submit.prevent="changePassword">
        <input v-model="pw.old" class="input" type="password" placeholder="当前密码" autocomplete="current-password" required />
        <input v-model="pw.new1" class="input" type="password" placeholder="新密码（至少 8 位）" autocomplete="new-password" minlength="8" required />
        <input v-model="pw.new2" class="input" type="password" placeholder="确认新密码" autocomplete="new-password" required />
        <button class="btn primary" :disabled="busy">修改密码</button>
      </form>
    </section>

    <section class="card-panel d4">
      <h2>快捷键</h2>
      <div class="keys">
        <span><kbd>空格</kbd>/<kbd>K</kbd> 播放 / 暂停</span>
        <span><kbd>←</kbd><kbd>→</kbd> 后退 / 前进 10 秒（按住 Shift 为 60 秒）</span>
        <span><kbd>↑</kbd><kbd>↓</kbd> 音量</span>
        <span><kbd>F</kbd> 全屏</span>
        <span><kbd>M</kbd> 静音</span>
        <span><kbd>C</kbd> 切换字幕</span>
        <span><kbd>0</kbd>–<kbd>9</kbd> 跳到 0%–90%</span>
        <span><kbd>Esc</kbd> 退出播放</span>
      </div>
    </section>
    <p class="muted foot"><Icon name="shield" :size="14" /> 所有请求、响应、视频和图片都经过端到端加密。</p>
  </div>
</template>

<style scoped>
.settings { max-width: 900px; }
section { margin-bottom: 18px; }
h2 { font-size: 17px; margin-bottom: 14px; }
.kv { display: grid; grid-template-columns: 90px 1fr; gap: 8px 14px; font-size: 13.5px; margin-bottom: 10px; }
.kv > span:nth-child(odd) { color: var(--muted); }
.kv code { font-size: 13px; color: var(--accent); }
.warn { color: var(--warn); }
.w { width: 220px; }
.top { margin-top: 14px; }
.pwform { display: grid; gap: 10px; max-width: 360px; }
.keys { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 8px; color: #ccc; }
kbd { display: inline-block; min-width: 22px; padding: 1px 6px; margin: 0 2px; border: 1px solid #555; border-bottom-width: 2px; border-radius: 4px; font-size: 12px; text-align: center; background: var(--bg2); }
.foot { display: flex; gap: 6px; align-items: center; }
</style>
