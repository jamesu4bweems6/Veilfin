// 网页模式传输层：按需加载（本地客户端模式不需要这些加密库）。
import { ClientHandshake, b64decode, fingerprint } from '../crypto/proto.js'
import * as idb from './idb.js'
import { RpcError } from './transport.js'

// ---------------- 网页模式 ----------------

const TOKEN_KEY = 'vf.token'
const PIN_KEY = 'vf.serverKey'

export class WebTransport {
  mode = 'web'

  constructor(cfg) {
    this.apiBase = (cfg.apiBase || '').replace(/\/$/, '')
    this.pub = b64decode(cfg.pubkey)
    this.fp = fingerprint(this.pub)
    this.sess = null
    this.offset = 0
    this.hsPromise = null
    this.authed = false
    this.blobTok = null
    this.registered = new Set()
    this.warning = ''
    this.seq = 0
  }

  async init() {
    // 首次访问时记住服务端公钥，之后如果变化就拒绝连接（防止被替换）
    const pinned = localStorage.getItem(PIN_KEY)
    const cur = btoa(String.fromCharCode(...this.pub))
    if (pinned && pinned !== cur) {
      throw new RpcError(
        'key_changed',
        '服务端公钥与上次不同！可能遭到中间人攻击，或服务端重装后密钥变了。确认安全后，可在浏览器中清除本站数据再访问。',
      )
    }
    if (!pinned) {
      localStorage.setItem(PIN_KEY, cur)
      this.warning = '首次访问，已记住服务端公钥指纹 ' + this.fp + '，请与服务端启动日志核对。'
    }
    if (!('serviceWorker' in navigator) || !crypto?.subtle) {
      throw new RpcError('unsupported', '当前浏览器不支持 Service Worker 或 WebCrypto（需要 HTTPS，且不能是无痕模式的 Firefox），无法解密播放。')
    }
    await idb.put('meta', 'cfg', { apiBase: this.apiBase })
    navigator.serviceWorker.addEventListener('message', async (ev) => {
      if (ev.data?.type !== 'need-token') return
      try {
        const token = await this.blobToken(ev.data.force)
        ev.ports[0]?.postMessage({ token })
      } catch {
        ev.ports[0]?.postMessage({ token: null })
      }
    })
    await navigator.serviceWorker.register('/sw.js', { scope: '/' })
    await navigator.serviceWorker.ready
    if (!navigator.serviceWorker.controller) {
      // 第一次安装：等待 Service Worker 接管当前页面
      await new Promise((resolve) => {
        navigator.serviceWorker.addEventListener('controllerchange', resolve, { once: true })
        setTimeout(resolve, 4000)
      })
    }
  }

  async handshake() {
    const hs = new ClientHandshake()
    const res = await fetch(this.apiBase + '/h', { method: 'POST', body: hs.hello })
    if (!res.ok) throw new RpcError('network', '握手失败：HTTP ' + res.status)
    const sess = hs.finish(new Uint8Array(await res.arrayBuffer()), this.pub)
    this.sess = sess
    this.offset = sess.serverTime - Date.now()
    this.authed = false
    this.blobTok = null
    const tok = localStorage.getItem(TOKEN_KEY)
    if (tok) {
      try {
        await this.send(sess, 'auth.resume', { token: tok })
        this.authed = true
      } catch (e) {
        if (e instanceof RpcError && e.code !== 'network') localStorage.removeItem(TOKEN_KEY)
        else throw e
      }
    }
    return sess
  }

  async session(force) {
    if (!force && this.sess && Date.now() - this.sess.created < 23 * 3600 * 1000) return this.sess
    if (!this.hsPromise) {
      this.hsPromise = this.handshake().finally(() => (this.hsPromise = null))
    }
    return this.hsPromise
  }

  envelope(sess, m, p) {
    const rid = `${Date.now().toString(36)}-${++this.seq}-${Math.random().toString(36).slice(2)}`
    const req = new TextEncoder().encode(JSON.stringify({ ts: Date.now() + this.offset, rid, m, p: p ?? {} }))
    return { rid, body: sess.seal(req) }
  }

  async send(sess, m, p) {
    const { rid, body } = this.envelope(sess, m, p)
    let res
    try {
      res = await fetch(this.apiBase + '/x', { method: 'POST', body })
    } catch {
      throw new RpcError('network', '无法连接服务端')
    }
    if (res.status === 401) throw new RpcError('session_gone', '会话已失效')
    if (!res.ok) throw new RpcError('network', '服务端返回 HTTP ' + res.status)
    let out
    try {
      out = JSON.parse(new TextDecoder().decode(sess.open(new Uint8Array(await res.arrayBuffer()))))
    } catch {
      throw new RpcError('tampered', '响应解密失败，数据可能被篡改')
    }
    if (out.rid !== rid) throw new RpcError('tampered', '响应与请求不匹配')
    if (!out.ok) throw new RpcError(out.code || 'error', out.err || '未知错误')
    return out.data
  }

  async call(m, p) {
    if (m === 'auth.logout') p = { token: localStorage.getItem(TOKEN_KEY) || '' }
    let sess = await this.session()
    let data
    try {
      data = await this.send(sess, m, p)
    } catch (e) {
      if (e.code !== 'session_gone') throw this.after(m, e)
      sess = await this.session(true)
      data = await this.send(sess, m, p)
    }
    if (m === 'auth.login' || m === 'setup.create' || m === 'auth.changePassword') {
      localStorage.setItem(TOKEN_KEY, data.token)
      this.authed = true
      data = data.me
    }
    this.after(m)
    return this.rewrite(data)
  }

  after(m, err) {
    if (m === 'auth.logout') {
      localStorage.removeItem(TOKEN_KEY)
      this.authed = false
    }
    if (m === 'auth.logout' || m === 'auth.lock' || m === 'auth.setPin' || m === 'auth.changePassword') {
      // 锁定 / 退出：丢弃所有对象密钥，并让 Service Worker 清空已解密的数据块
      this.registered.clear()
      idb.clear('blobs')
      navigator.serviceWorker.controller?.postMessage({ type: 'clear-cache' })
    }
    if (m === 'auth.unlock' || m === 'auth.lock' || m === 'auth.setPin' || m === 'auth.logout' || m === 'auth.changePassword') {
      this.blobTok = null
      idb.put('meta', 'token', null)
    }
    return err
  }

  beacon(m, p) {
    if (!this.sess) return
    try {
      const { body } = this.envelope(this.sess, m, p)
      fetch(this.apiBase + '/x', { method: 'POST', body, keepalive: true })
    } catch {}
  }

  async blobToken(force) {
    if (!force && this.blobTok && this.blobTok.exp * 1000 - Date.now() > 5 * 60 * 1000) return this.blobTok.token
    const sess = await this.session()
    const t = await this.send(sess, 'auth.blobToken')
    this.blobTok = t
    await idb.put('meta', 'token', t)
    return t.token
  }

  // rewrite 把对象引用替换成 /b/{id}，并把解密密钥（不可导出的 CryptoKey）存入 IndexedDB 供 Service Worker 使用。
  async rewrite(data) {
    const refs = []
    const walk = (x) => {
      if (Array.isArray(x)) {
        for (let i = 0; i < x.length; i++) x[i] = walk(x[i])
        return x
      }
      if (x && typeof x === 'object') {
        if (typeof x.$b === 'string') {
          if (!this.registered.has(x.$b)) refs.push(x)
          return '/b/' + encodeURIComponent(x.$b)
        }
        for (const k of Object.keys(x)) x[k] = walk(x[k])
      }
      return x
    }
    data = walk(data)
    if (refs.length) {
      const entries = await Promise.all(
        refs.map(async (r) => [r.$b, { key: await crypto.subtle.importKey('raw', b64decode(r.k), 'AES-GCM', false, ['decrypt']), size: r.s, mime: r.t }]),
      )
      await idb.putMany('blobs', entries)
      for (const [id] of entries) this.registered.add(id)
      if (!this.blobTok) await this.blobToken().catch(() => {})
    }
    return data
  }

  async status() {
    const st = {
      mode: 'web',
      server: location.origin + (this.apiBase ? ` → ${this.apiBase}` : ''),
      fingerprint: this.fp,
      suite: 'X25519+ML-KEM-768 / Ed25519 / XChaCha20-Poly1305 ⊂ AES-256-GCM',
      warning: this.warning,
      connected: false,
      authed: this.authed,
    }
    try {
      const s = await this.session()
      st.connected = true
      st.authed = this.authed
      st.sessionAge = Math.round((Date.now() - s.created) / 1000)
      st.sendEpoch = s.send.epoch
    } catch (e) {
      st.error = e.message
    }
    return st
  }
}
