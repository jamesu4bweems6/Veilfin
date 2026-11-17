// Service Worker（网页模式）：拦截 /b/{id}，从服务端（或 CDN）拉取密文块，用 WebCrypto 解密后交给 <video>/<img>。
// 支持 Range 请求和流式输出，拖动进度条时只拉取需要的块。
// 密钥和访问令牌由页面写入 IndexedDB（结构与 src/api/idb.js 一致）。
'use strict'

const CHUNK = 1 << 20
const OVERHEAD = 28
const CIPHER_CHUNK = CHUNK + OVERHEAD
const GROUP = 4 // 每次向上游拉取的块数
const CACHE_LIMIT = 96 // 解密块缓存（约 96 MiB）

self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()))

// 页面锁定受限内容或退出登录时清空解密缓存
self.addEventListener('message', (e) => {
  if (e.data?.type === 'clear-cache') cache.clear()
})

self.addEventListener('fetch', (e) => {
  const url = new URL(e.request.url)
  if (url.origin === self.location.origin && url.pathname.startsWith('/b/')) {
    e.respondWith(serve(e.request, decodeURIComponent(url.pathname.slice(3))))
  }
})

// ---- IndexedDB ----
let dbp = null
function openDB() {
  if (!dbp) {
    dbp = new Promise((resolve, reject) => {
      const req = indexedDB.open('veilfin', 1) // 与 src/api/idb.js 一致
      req.onupgradeneeded = () => {
        const db = req.result
        if (!db.objectStoreNames.contains('blobs')) db.createObjectStore('blobs')
        if (!db.objectStoreNames.contains('meta')) db.createObjectStore('meta')
      }
      req.onsuccess = () => resolve(req.result)
      req.onerror = () => reject(req.error)
    })
  }
  return dbp
}
async function idbGet(store, key) {
  const db = await openDB()
  return new Promise((resolve, reject) => {
    const req = db.transaction(store).objectStore(store).get(key)
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
}

// ---- 解密块缓存（LRU） ----
const cache = new Map()
function cacheGet(k) {
  const v = cache.get(k)
  if (v) {
    cache.delete(k)
    cache.set(k, v)
  }
  return v
}
function cachePut(k, v) {
  cache.set(k, v)
  while (cache.size > CACHE_LIMIT) cache.delete(cache.keys().next().value)
}

// ---- 访问令牌 ----
async function askClientForToken(force) {
  const clients = await self.clients.matchAll({ type: 'window' })
  for (const c of clients) {
    const ch = new MessageChannel()
    const reply = new Promise((resolve) => {
      ch.port1.onmessage = (ev) => resolve(ev.data)
      setTimeout(() => resolve(null), 15000)
    })
    c.postMessage({ type: 'need-token', force }, [ch.port2])
    const r = await reply
    if (r && r.token) return r.token
  }
  return null
}

async function getToken(force) {
  if (!force) {
    const t = await idbGet('meta', 'token')
    if (t && t.exp * 1000 - Date.now() > 60 * 1000) return t.token
  }
  return askClientForToken(force)
}

// ---- 拉取与解密 ----
function aadFor(id, idx, n) {
  const idb = new TextEncoder().encode(id)
  const aad = new Uint8Array(4 + idb.length + 9)
  aad.set([0x56, 0x46, 0x4d, 0x32], 0) // "VFM2"
  aad.set(idb, 4)
  new DataView(aad.buffer).setBigUint64(4 + idb.length, BigInt(idx))
  aad[aad.length - 1] = idx === n - 1 ? 1 : 0
  return aad
}

const inflight = new Map()

async function fetchGroup(id, info, first, last, base) {
  const key = id + ':' + first + ':' + last
  if (inflight.has(key)) return inflight.get(key)
  const p = (async () => {
    const n = Math.ceil(info.size / CHUNK)
    const start = first * CIPHER_CHUNK
    const end = last * CIPHER_CHUNK + Math.min(CHUNK, info.size - last * CHUNK) + OVERHEAD - 1
    let force = false
    for (let attempt = 0; attempt < 2; attempt++) {
      const token = await getToken(force)
      if (!token) throw new Error('无法获取访问令牌，请重新登录')
      const res = await fetch(base + '/m/' + encodeURIComponent(id), {
        headers: { Range: `bytes=${start}-${end}`, 'X-VF-T': token },
        cache: 'no-store',
      })
      if (res.status === 403) {
        force = true
        continue
      }
      if (res.status !== 206 && res.status !== 200) throw new Error('上游返回 HTTP ' + res.status)
      let buf = new Uint8Array(await res.arrayBuffer())
      if (res.status === 200) buf = buf.subarray(start, end + 1)
      if (buf.length !== end - start + 1) throw new Error('上游数据长度不符')
      let pos = 0
      for (let i = first; i <= last; i++) {
        const clen = Math.min(CHUNK, info.size - i * CHUNK) + OVERHEAD
        const ct = buf.subarray(pos, pos + clen)
        const pt = await crypto.subtle.decrypt(
          { name: 'AES-GCM', iv: ct.subarray(0, 12), additionalData: aadFor(id, i, n), tagLength: 128 },
          info.key,
          ct.subarray(12),
        )
        cachePut(id + ':' + i, new Uint8Array(pt))
        pos += clen
      }
      return
    }
    throw new Error('无权访问（受限内容可能需要先输入 PIN）')
  })()
  inflight.set(key, p)
  try {
    return await p
  } finally {
    inflight.delete(key)
  }
}

async function getChunk(id, info, idx, lastNeeded, base) {
  const k = id + ':' + idx
  let c = cacheGet(k)
  if (c) return c
  const n = Math.ceil(info.size / CHUNK)
  const last = Math.min(idx + GROUP - 1, lastNeeded, n - 1)
  await fetchGroup(id, info, idx, last, base)
  c = cacheGet(k)
  if (!c) throw new Error('解密失败')
  return c
}

function parseRange(h, size) {
  if (!h) return null
  const m = /^bytes=(\d*)-(\d*)$/.exec(h.trim())
  if (!m) return 'invalid'
  let start, end
  if (m[1] === '') {
    const suffix = parseInt(m[2], 10)
    if (!suffix) return 'invalid'
    start = Math.max(size - suffix, 0)
    end = size - 1
  } else {
    start = parseInt(m[1], 10)
    end = m[2] === '' ? size - 1 : Math.min(parseInt(m[2], 10), size - 1)
  }
  if (start > end || start >= size) return 'invalid'
  return [start, end]
}

async function serve(req, id) {
  const info = await idbGet('blobs', id)
  if (!info) return new Response('未知对象，请刷新页面', { status: 404 })
  const cfg = (await idbGet('meta', 'cfg')) || {}
  const base = cfg.apiBase || ''
  const size = info.size
  const range = parseRange(req.headers.get('Range'), size)
  if (range === 'invalid') return new Response(null, { status: 416, headers: { 'Content-Range': `bytes */${size}` } })
  const [start, end] = range || [0, size - 1]

  // 先取第一块：出错时可以直接返回错误状态，而不是一个中途断掉的流
  let firstChunk
  try {
    firstChunk = await getChunk(id, info, Math.floor(start / CHUNK), Math.floor(end / CHUNK), base)
  } catch (e) {
    return new Response(String(e.message || e), { status: 502 })
  }

  let pos = start
  const body = new ReadableStream({
    async pull(ctrl) {
      if (pos > end) {
        ctrl.close()
        return
      }
      try {
        const idx = Math.floor(pos / CHUNK)
        const chunk = firstChunk && idx === Math.floor(start / CHUNK) ? firstChunk : await getChunk(id, info, idx, Math.floor(end / CHUNK), base)
        firstChunk = null
        const from = pos - idx * CHUNK
        const to = Math.min(chunk.length, end - idx * CHUNK + 1)
        ctrl.enqueue(chunk.slice(from, to))
        pos = idx * CHUNK + to
        if (pos > end) ctrl.close()
      } catch (e) {
        ctrl.error(e)
      }
    },
  })
  const headers = {
    'Content-Type': info.mime,
    'Content-Length': String(end - start + 1),
    'Accept-Ranges': 'bytes',
    'Cache-Control': 'no-store',
  }
  if (range) {
    headers['Content-Range'] = `bytes ${start}-${end}/${size}`
    return new Response(body, { status: 206, headers })
  }
  return new Response(body, { status: 200, headers })
}
