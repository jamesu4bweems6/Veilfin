// 全局状态与通用操作。
import { reactive } from 'vue'
import { LocalTransport, RpcError } from './api/transport.js'

export { RpcError }

export const state = reactive({
  ready: false,
  fatal: '',
  mode: '',
  me: null, // 当前用户
  serverName: 'Veilfin',
  needsSetup: false,
  toasts: [],
  pin: null, // { resolve } 正在请求 PIN
})

let transport = null

export async function init() {
  const cfg = await (await fetch('/config.json', { cache: 'no-store' })).json()
  if (cfg.mode === 'web') {
    const { WebTransport } = await import('./api/web-transport.js')
    transport = new WebTransport(cfg)
  } else {
    transport = new LocalTransport()
  }
  state.mode = transport.mode
  await transport.init()
  const info = await transport.call('sys.info')
  state.needsSetup = info.needsSetup
  state.serverName = info.serverName
  if (!info.needsSetup) {
    try {
      state.me = await transport.call('auth.me')
      state.serverName = state.me.serverName
    } catch (e) {
      if (!(e instanceof RpcError) || (e.code !== 'unauthorized' && e.code !== 'needs_setup')) throw e
    }
  }
  state.ready = true
}

// rpc 调用服务端方法。遇到受限内容会先弹出 PIN 输入框，解锁后自动重试。
export async function rpc(m, p) {
  try {
    return await transport.call(m, p)
  } catch (e) {
    if (e.code === 'locked' && (await requestPin())) return transport.call(m, p)
    if (e.code === 'unauthorized' && state.me && m !== 'auth.unlock' && m !== 'auth.changePassword' && m !== 'auth.setPin') {
      state.me = null
      location.hash = '#/login'
    }
    throw e
  }
}

export function beacon(m, p) {
  transport?.beacon(m, p)
}

export function connectionStatus() {
  return transport.status()
}

export function toast(msg, kind = 'info') {
  const id = Math.random()
  state.toasts.push({ id, msg, kind })
  setTimeout(() => {
    const i = state.toasts.findIndex((t) => t.id === id)
    if (i >= 0) state.toasts.splice(i, 1)
  }, kind === 'error' ? 6000 : 3000)
}

export function toastError(e) {
  toast(e?.message || String(e), 'error')
}

// requestPin 弹出 PIN 输入框，成功解锁返回 true。
export function requestPin() {
  if (state.pin) return state.pin.promise
  let resolve
  const promise = new Promise((r) => (resolve = r))
  state.pin = { resolve, promise }
  return promise
}

export async function submitPin(pin) {
  const me = await transport.call('auth.unlock', { pin })
  state.me = me
  const p = state.pin
  state.pin = null
  p?.resolve(true)
}

export function cancelPin() {
  const p = state.pin
  state.pin = null
  p?.resolve(false)
}

export async function lockNow() {
  state.me = await transport.call('auth.lock')
}
