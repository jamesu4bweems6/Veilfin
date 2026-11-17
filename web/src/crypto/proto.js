// 浏览器端（网页模式）的加密协议实现，与 Go 端 internal/proto 完全对应：
//   握手：X25519 + ML-KEM-768 混合密钥交换，Ed25519 校验服务端签名
//   消息：XChaCha20-Poly1305（内层）⊂ AES-256-GCM（外层），按 1 KiB 填充
//   棘轮：每个方向独立推进，500 条消息或 5 分钟换一代
import { x25519, ed25519 } from '@noble/curves/ed25519.js'
import { ml_kem768 } from '@noble/post-quantum/ml-kem.js'
import { extract, expand } from '@noble/hashes/hkdf.js'
import { sha256 } from '@noble/hashes/sha2.js'
import { xchacha20poly1305 } from '@noble/ciphers/chacha.js'
import { gcm } from '@noble/ciphers/aes.js'

const VERSION = 1
const SID_SIZE = 16
const HEADER_SIZE = 1 + SID_SIZE + 4 + 12
const XNONCE = 24
const TAG = 16
const PAD_BLOCK = 1024
const KEM_CT = 1088
const SERVER_HELLO_SIZE = 32 + KEM_CT + SID_SIZE + 8 + 64
const HS_LABEL = new TextEncoder().encode('Veilfin/handshake/v1')
const REKEY_MESSAGES = 500
const REKEY_INTERVAL = 5 * 60 * 1000
const MAX_EPOCH_SKIP = 8

const enc = new TextEncoder()

export function randomBytes(n) {
  const b = new Uint8Array(n)
  crypto.getRandomValues(b)
  return b
}

function concat(...parts) {
  const len = parts.reduce((a, p) => a + p.length, 0)
  const out = new Uint8Array(len)
  let o = 0
  for (const p of parts) {
    out.set(p, o)
    o += p.length
  }
  return out
}

function hkdfExpand(prk, info, len = 32) {
  return expand(sha256, prk, enc.encode(info), len)
}

function deriveEpoch(chain) {
  return {
    keys: { outer: hkdfExpand(chain, 'msg/outer'), inner: hkdfExpand(chain, 'msg/inner') },
    next: hkdfExpand(chain, 'ratchet'),
  }
}

export class ClientHandshake {
  constructor() {
    this.xSecret = x25519.utils.randomSecretKey()
    const xPub = x25519.getPublicKey(this.xSecret)
    const kem = ml_kem768.keygen()
    this.kemSecret = kem.secretKey
    this.hello = concat(new Uint8Array([VERSION]), xPub, kem.publicKey)
  }

  // finish 校验服务端签名并派生会话。serverPub 是事先固定的服务端 Ed25519 公钥。
  finish(serverHello, serverPub) {
    if (serverHello.length !== SERVER_HELLO_SIZE) throw new Error('握手响应长度错误')
    let o = 0
    const sx = serverHello.subarray(o, (o += 32))
    const ct = serverHello.subarray(o, (o += KEM_CT))
    const sid = serverHello.subarray(o, (o += SID_SIZE))
    const ts = serverHello.subarray(o, (o += 8))
    const sig = serverHello.subarray(o)
    const tr = concat(HS_LABEL, this.hello, sx, ct, sid, ts)
    if (!ed25519.verify(sig, tr, serverPub)) {
      throw new Error('服务端签名校验失败：可能存在中间人，或服务端密钥已更换')
    }
    const ssX = x25519.getSharedSecret(this.xSecret, sx)
    const ssPQ = ml_kem768.decapsulate(ct, this.kemSecret)
    const th = sha256(tr)
    const root = extract(sha256, concat(ssPQ, ssX), th)
    const c2s = hkdfExpand(root, 'chain/c2s')
    const s2c = hkdfExpand(root, 'chain/s2c')
    root.fill(0)
    ssX.fill(0)
    ssPQ.fill(0)
    this.xSecret.fill(0)
    const serverTime = Number(new DataView(ts.buffer, ts.byteOffset, 8).getBigUint64(0))
    return new Session(sid.slice(), c2s, s2c, serverTime)
  }
}

export class Session {
  constructor(sid, sendChain, recvChain, serverTime) {
    this.sid = sid
    this.created = Date.now()
    this.serverTime = serverTime
    const s = deriveEpoch(sendChain)
    this.send = { epoch: 0, keys: s.keys, chain: s.next, count: 0, since: Date.now() }
    const r = deriveEpoch(recvChain)
    this.recv = { latest: 0, cur: r.keys, prev: null, chain: r.next }
    sendChain.fill(0)
    recvChain.fill(0)
  }

  nextSendKeys() {
    const s = this.send
    if (s.count >= REKEY_MESSAGES || Date.now() - s.since >= REKEY_INTERVAL) {
      const d = deriveEpoch(s.chain)
      s.chain.fill(0)
      s.chain = d.next
      s.keys = d.keys
      s.epoch++
      s.count = 0
      s.since = Date.now()
    }
    s.count++
    return { epoch: s.epoch, keys: s.keys }
  }

  seal(plaintext) {
    const { epoch, keys } = this.nextSendKeys()
    const padded = pad(plaintext)
    const hdr = new Uint8Array(HEADER_SIZE)
    hdr[0] = VERSION
    hdr.set(this.sid, 1)
    new DataView(hdr.buffer).setUint32(1 + SID_SIZE, epoch)
    const nonce = randomBytes(12)
    hdr.set(nonce, 1 + SID_SIZE + 4)
    const xn = randomBytes(XNONCE)
    const inner = concat(xn, xchacha20poly1305(keys.inner, xn, hdr).encrypt(padded))
    return concat(hdr, gcm(keys.outer, nonce, hdr).encrypt(inner))
  }

  open(msg) {
    if (msg.length < HEADER_SIZE + TAG + XNONCE + TAG + PAD_BLOCK || msg[0] !== VERSION) throw new Error('解密失败')
    for (let i = 0; i < SID_SIZE; i++) if (msg[1 + i] !== this.sid[i]) throw new Error('解密失败')
    const hdr = msg.subarray(0, HEADER_SIZE)
    const epoch = new DataView(msg.buffer, msg.byteOffset).getUint32(1 + SID_SIZE)
    const nonce = msg.subarray(1 + SID_SIZE + 4, HEADER_SIZE)
    const r = this.recv
    const tryOpen = (keys) => {
      const inner = gcm(keys.outer, nonce, hdr).decrypt(msg.subarray(HEADER_SIZE))
      return unpad(xchacha20poly1305(keys.inner, inner.subarray(0, XNONCE), hdr).decrypt(inner.subarray(XNONCE)))
    }
    if (epoch === r.latest) return tryOpen(r.cur)
    if (epoch + 1 === r.latest && r.prev) return tryOpen(r.prev)
    if (epoch > r.latest && epoch - r.latest <= MAX_EPOCH_SKIP) {
      // epoch 未经认证：先试算，解密成功后才提交新状态
      let chain = r.chain.slice()
      let prev = r.cur
      let cur = r.cur
      for (let i = r.latest; i < epoch; i++) {
        const d = deriveEpoch(chain)
        prev = cur
        cur = d.keys
        chain = d.next
      }
      const out = tryOpen(cur)
      r.chain.fill(0)
      Object.assign(r, { latest: epoch, cur, prev, chain })
      return out
    }
    throw new Error('密钥已过期')
  }
}

function pad(p) {
  const n = 4 + p.length
  const size = Math.ceil(n / PAD_BLOCK) * PAD_BLOCK
  const buf = new Uint8Array(size)
  new DataView(buf.buffer).setUint32(0, p.length)
  buf.set(p, 4)
  return buf
}

function unpad(b) {
  if (b.length < 4 || b.length % PAD_BLOCK !== 0) throw new Error('解密失败')
  const n = new DataView(b.buffer, b.byteOffset).getUint32(0)
  if (n > b.length - 4) throw new Error('解密失败')
  return b.subarray(4, 4 + n)
}

export function b64decode(s) {
  const std = s.replace(/-/g, '+').replace(/_/g, '/')
  const bin = atob(std + '==='.slice((std.length + 3) % 4))
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

export function fingerprint(pub) {
  const h = Array.from(sha256(pub).subarray(0, 16), (b) => b.toString(16).padStart(2, '0')).join('')
  return h.match(/.{4}/g).join('-').toUpperCase()
}

// ---- 媒体分块（与 internal/mediacrypt 对应） ----

export const CHUNK = 1 << 20
export const OVERHEAD = 12 + 16
export const CIPHER_CHUNK = CHUNK + OVERHEAD

export function numChunks(size) {
  return Math.ceil(size / CHUNK)
}

export function chunkAAD(id, idx, n) {
  const idb = enc.encode(id)
  const aad = new Uint8Array(4 + idb.length + 9)
  aad.set(enc.encode('VFM2'), 0)
  aad.set(idb, 4)
  new DataView(aad.buffer).setBigUint64(4 + idb.length, BigInt(idx))
  aad[aad.length - 1] = idx === n - 1 ? 1 : 0
  return aad
}

export function plainChunkLen(size, idx) {
  return Math.min(CHUNK, size - idx * CHUNK)
}

// decryptChunk 用 WebCrypto 解密一个媒体块（key 为 AES-GCM CryptoKey）。
export async function decryptChunk(key, id, idx, size, ct) {
  const n = numChunks(size)
  const pt = await crypto.subtle.decrypt(
    { name: 'AES-GCM', iv: ct.subarray(0, 12), additionalData: chunkAAD(id, idx, n), tagLength: 128 },
    key,
    ct.subarray(12),
  )
  return new Uint8Array(pt)
}
