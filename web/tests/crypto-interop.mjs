// JS（网页模式）加密实现与 Go 服务端的互通测试。
// 用法：node tests/crypto-interop.mjs
// 会自动编译 Go 服务端、生成测试视频、启动服务端，然后用 JS 实现完成全部流程。
import { spawn, execFileSync } from 'node:child_process'
import { mkdtempSync, writeFileSync, readFileSync, rmSync, mkdirSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import { ClientHandshake, b64decode, CIPHER_CHUNK, OVERHEAD, plainChunkLen, decryptChunk, numChunks } from '../src/crypto/proto.js'

const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..')
const work = mkdtempSync(join(tmpdir(), 'vf-interop-'))
const exe = join(work, process.platform === 'win32' ? 'veilfin.exe' : 'veilfin')
const port = 18700 + Math.floor(Math.random() * 200)
const base = `http://127.0.0.1:${port}`
let failed = 0
const ok = (cond, msg) => {
  if (cond) console.log('  ✓', msg)
  else {
    failed++
    console.log('  ✗', msg)
  }
}

console.log('编译服务端…')
execFileSync('go', ['build', '-o', exe, './cmd/veilfin'], { cwd: root, stdio: 'inherit' })
const media = join(work, 'media')
mkdirSync(media)
// 2.5 MiB 的伪视频：内容随机，测试的是加解密，不需要能播放
const video = new Uint8Array(2.5 * 1024 * 1024)
for (let i = 0; i < video.length; i += 65536) crypto.getRandomValues(video.subarray(i, Math.min(i + 65536, video.length)))
writeFileSync(join(media, 'interop.mp4'), video)

const srv = spawn(exe, ['server', '-listen', `127.0.0.1:${port}`, '-data', join(work, 'data'), '-media', media], {
  env: { ...process.env, PATH: '' }, // 不提供 ffmpeg：跳过探测，测试只关心加密
})
let log = ''
const setupCode = await new Promise((resolve, reject) => {
  const t = setTimeout(() => reject(new Error('服务端启动超时：\n' + log)), 20000)
  const onData = (d) => {
    log += d
    const m = log.match(/初始化代码：([0-9A-F]+)/)
    if (m && log.includes('服务端监听')) {
      clearTimeout(t)
      resolve(m[1])
    }
  }
  srv.stdout.on('data', onData)
  srv.stderr.on('data', onData)
})

try {
  const pub = new Uint8Array(await (await fetch(base + '/k')).arrayBuffer())
  ok(pub.length === 32, '获取服务端公钥')

  const hs = new ClientHandshake()
  const sh = new Uint8Array(await (await fetch(base + '/h', { method: 'POST', body: hs.hello })).arrayBuffer())
  const sess = hs.finish(sh, pub)
  ok(sess.sid.length === 16, 'JS 完成混合后量子握手（X25519 + ML-KEM-768），Ed25519 签名校验通过')

  let wrongKeyRejected = false
  try {
    const hs2 = new ClientHandshake()
    const sh2 = new Uint8Array(await (await fetch(base + '/h', { method: 'POST', body: hs2.hello })).arrayBuffer())
    const fake = new Uint8Array(32)
    fake[0] = 1
    hs2.finish(sh2, fake)
  } catch {
    wrongKeyRejected = true
  }
  ok(wrongKeyRejected, '使用错误的服务端公钥时握手被拒绝')

  const offset = sess.serverTime - Date.now()
  let n = 0
  const call = async (m, p, s = sess) => {
    const req = new TextEncoder().encode(JSON.stringify({ ts: Date.now() + offset, rid: `r${++n}-${Math.random()}`, m, p }))
    const res = await fetch(base + '/x', { method: 'POST', body: s.seal(req) })
    if (res.status !== 200) throw new Error('HTTP ' + res.status)
    const body = new Uint8Array(await res.arrayBuffer())
    return JSON.parse(new TextDecoder().decode(s.open(body)))
  }

  const info = await call('sys.info')
  ok(info.ok && info.data.needsSetup === true, 'sys.info：服务端处于待初始化状态')
  const bad = await call('setup.create', { code: 'WRONG', username: 'admin', password: 'password123' })
  ok(!bad.ok, '错误的初始化代码被拒绝')
  const setup = await call('setup.create', { code: setupCode, username: 'admin', password: 'password123' })
  ok(setup.ok && setup.data.token && setup.data.me.isAdmin, '初始化并登录为管理员')

  // 跨越多代密钥：客户端每 500 条换代，服务端回复同样会换代
  let allOk = true
  for (let i = 0; i < 1200; i++) {
    const r = await call('sys.ping')
    if (!r.ok) allOk = false
  }
  ok(allOk && sess.send.epoch === 2 && sess.recv.latest === 2, `1200 次 RPC 跨越密钥棘轮（发送第 ${sess.send.epoch} 代，接收第 ${sess.recv.latest} 代）`)

  // 等待扫描完成
  let items = []
  for (let i = 0; i < 50 && items.length === 0; i++) {
    const r = await call('items.list', { libraryId: 0 })
    items = r.data?.items || []
    if (!items.length) await new Promise((r) => setTimeout(r, 200))
  }
  ok(items.length === 1, '媒体库扫描到测试视频')

  const play = await call('play.open', { id: items[0].id })
  ok(play.ok && play.data.video.$b && play.data.video.k, 'play.open 返回视频对象引用和解密密钥')
  const ref = play.data.video
  const tok = (await call('auth.blobToken')).data.token

  const key = await crypto.subtle.importKey('raw', b64decode(ref.k), 'AES-GCM', false, ['decrypt'])
  const n2 = numChunks(ref.s)
  const res = await fetch(base + '/m/' + ref.$b, { headers: { 'X-VF-T': tok, Range: `bytes=${CIPHER_CHUNK}-${CIPHER_CHUNK * 3 - 1}` } })
  ok(res.status === 206, '按密文偏移的 Range 请求返回 206')
  const ct = new Uint8Array(await res.arrayBuffer())
  const c1 = await decryptChunk(key, ref.$b, 1, ref.s, ct.subarray(0, CIPHER_CHUNK))
  const c2len = plainChunkLen(ref.s, 2) + OVERHEAD
  const c2 = await decryptChunk(key, ref.$b, 2, ref.s, ct.subarray(CIPHER_CHUNK, CIPHER_CHUNK + c2len))
  const same = (a, b) => a.length === b.length && a.every((v, i) => v === b[i])
  ok(n2 === 3 && same(c1, video.subarray(1 << 20, 2 << 20)) && same(c2, video.subarray(2 << 20)), 'WebCrypto 解密视频块，与原文件逐字节一致（含最后一块）')

  let tamper = false
  try {
    const t = ct.slice(0, CIPHER_CHUNK)
    t[500] ^= 1
    await decryptChunk(key, ref.$b, 1, ref.s, t)
  } catch {
    tamper = true
  }
  ok(tamper, '篡改的视频块被拒绝')

  const noTok = await fetch(base + '/m/' + ref.$b)
  ok(noTok.status === 403, '没有访问令牌时拒绝提供密文')

  let replay = false
  {
    const req = new TextEncoder().encode(JSON.stringify({ ts: Date.now() + offset, rid: 'replay-test', m: 'sys.ping' }))
    const msg = sess.seal(req)
    await fetch(base + '/x', { method: 'POST', body: msg })
    const again = await fetch(base + '/x', { method: 'POST', body: msg })
    const r = JSON.parse(new TextDecoder().decode(sess.open(new Uint8Array(await again.arrayBuffer()))))
    replay = !r.ok && r.code === 'replay'
  }
  ok(replay, '重放的请求被服务端拒绝')
} catch (e) {
  failed++
  console.error('测试异常：', e)
} finally {
  srv.kill()
  await new Promise((r) => setTimeout(r, 500))
  try {
    rmSync(work, { recursive: true, force: true })
  } catch {}
}
console.log(failed ? `\n失败 ${failed} 项` : '\n全部通过')
process.exit(failed ? 1 : 0)
