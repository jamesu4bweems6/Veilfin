// 真实浏览器端到端测试（Edge / Chrome，无头模式）。
// 用法：npm run build && node tests/browser.mjs
// 覆盖：网页模式（浏览器内加密 + Service Worker 解密）与本地客户端模式下的初始化、登录、浏览、
// 播放、拖动、字幕、进度条预览图、继续观看、管理后台页面。截图保存在 tests/screenshots/。
import { spawn, execFileSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, writeFileSync, existsSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import puppeteer from 'puppeteer-core'

const here = dirname(fileURLToPath(import.meta.url))
const root = join(here, '..', '..')
const shots = join(here, 'screenshots')
mkdirSync(shots, { recursive: true })
const work = mkdtempSync(join(tmpdir(), 'vf-browser-'))
const exe = join(work, process.platform === 'win32' ? 'veilfin.exe' : 'veilfin')

const browsers = [
  process.env.BROWSER,
  'C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Microsoft/Edge/Application/msedge.exe',
  'C:/Program Files/Google/Chrome/Application/chrome.exe',
  '/usr/bin/google-chrome',
  '/usr/bin/chromium',
].filter(Boolean)
const browserPath = browsers.find((p) => existsSync(p))
if (!browserPath) {
  console.error('找不到 Edge / Chrome，可用 BROWSER 环境变量指定')
  process.exit(2)
}

let failed = 0
const ok = (cond, msg) => {
  console.log(cond ? '  ✓' : '  ✗', msg)
  if (!cond) failed++
  return cond
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

function ffmpeg(args) {
  execFileSync('ffmpeg', ['-v', 'error', '-y', ...args], { stdio: 'inherit' })
}

console.log('准备：编译、生成测试视频…')
execFileSync('go', ['build', '-o', exe, './cmd/veilfin'], { cwd: root, stdio: 'inherit' })
const media = join(work, 'media')
mkdirSync(join(media, '旅行'), { recursive: true })
// 1080p 测试视频，带关键帧间隔，模拟真实片源
ffmpeg(['-f', 'lavfi', '-i', 'testsrc2=size=1280x720:rate=30:duration=120', '-f', 'lavfi', '-i', 'sine=frequency=440:duration=120',
  '-c:v', 'libx264', '-preset', 'veryfast', '-g', '60', '-b:v', '2M', '-c:a', 'aac', '-movflags', '+faststart', join(media, '家庭聚会.mp4')])
writeFileSync(join(media, '家庭聚会.srt'), '1\n00:00:00,500 --> 00:01:59,000\n字幕测试文本\n')
writeFileSync(join(media, '家庭聚会.nfo'), '<movie><title>二零二三年家庭聚会</title><plot>一家人的聚会。</plot><year>2023</year><genre>家庭</genre><actor><name>爷爷</name></actor></movie>')
// moov 在文件尾的视频（拖动更依赖 Range）
ffmpeg(['-f', 'lavfi', '-i', 'testsrc=size=640x360:rate=25:duration=60', '-f', 'lavfi', '-i', 'sine=frequency=660:duration=60',
  '-c:v', 'libx264', '-preset', 'veryfast', '-g', '50', '-c:a', 'aac', join(media, '旅行', '海边.mp4')])
// 第二个目录：通过管理后台界面添加成受限媒体库
const media2 = join(work, 'media2')
mkdirSync(media2, { recursive: true })
ffmpeg(['-f', 'lavfi', '-i', 'testsrc=size=640x360:rate=25:duration=20', '-c:v', 'libx264', '-preset', 'ultrafast', '-movflags', '+faststart', join(media2, 'ABC-123.mp4')])

const port = 19000 + Math.floor(Math.random() * 500)
const cport = port + 600
const data = join(work, 'data')
const procs = []
function start(args, waitFor) {
  return new Promise((resolve, reject) => {
    const p = spawn(exe, args)
    procs.push(p)
    let log = ''
    const t = setTimeout(() => reject(new Error('启动超时：\n' + log)), 30000)
    const on = (d) => {
      log += d
      const m = waitFor(log)
      if (m) {
        clearTimeout(t)
        resolve({ p, log, m })
      }
    }
    p.stdout.on('data', on)
    p.stderr.on('data', on)
  })
}

const srv = await start(['server', '-listen', `127.0.0.1:${port}`, '-data', data, '-media', media, '-web'], (log) =>
  log.includes('服务端监听') && log.includes('初始化代码'),
)
const setupCode = srv.log.match(/初始化代码：([0-9A-F]+)/)[1]
const pubkey = srv.log.match(/服务端公钥: ([A-Za-z0-9+/=]+)/)[1]

const browser = await puppeteer.launch({
  executablePath: browserPath,
  headless: true,
  args: ['--autoplay-policy=no-user-gesture-required', '--mute-audio', '--window-size=1440,900', '--no-first-run'],
  defaultViewport: { width: 1440, height: 900 },
})

async function newPage(ctx, label) {
  const page = await ctx.newPage()
  page.errors = []
  page.on('console', (m) => {
    if (m.type() === 'error') page.errors.push(`[${label}] console: ${m.text()}`)
  })
  page.on('pageerror', (e) => page.errors.push(`[${label}] pageerror: ${e.message}`))
  return page
}

async function shot(page, name) {
  await page.screenshot({ path: join(shots, name + '.png') })
}

// 等待首页出现视频卡片（媒体库扫描是异步的）
async function waitForCards(page, base) {
  for (let i = 0; i < 60; i++) {
    if (await page.$('.card')) return true
    await sleep(1000)
    if (i % 5 === 4) await page.goto(base + '/#/', { waitUntil: 'networkidle2' })
  }
  return false
}

// 播放测试：打开详情页 → 播放 → 检查进度推进 → 拖动 → 字幕 → 进度条预览图
async function playbackTest(page, base, label) {
  await page.goto(base + '/#/library/1?view=', { waitUntil: 'networkidle2' })
  // 只改 hash 的跳转不会重新加载页面：等媒体库页面自己的工具栏和卡片出现，避免读到上一页的内容
  await page.waitForSelector('.toolbar')
  await page.waitForSelector('.grid .card')
  const href = await page.$$eval('.grid .card', (cs) => cs.map((c) => [c.querySelector('.title').textContent, c.getAttribute('href')]))
  const party = href.find(([t]) => t.includes('家庭聚会'))
  if (!ok(!!party, `${label}：媒体库中有 NFO 标题的视频`)) {
    console.log('      页面上的卡片：', JSON.stringify(href), page.url())
    await shot(page, `${label}-library-debug`)
    throw new Error('找不到测试视频')
  }
  await page.goto(base + '/' + party[1], { waitUntil: 'networkidle2' })
  await page.waitForSelector('.detail .title')
  await sleep(800)
  await shot(page, `${label}-detail`)
  const overview = await page.$eval('.overview', (e) => e.textContent).catch(() => '')
  ok(overview.includes('一家人的聚会'), `${label}：详情页显示 NFO 简介`)
  const posterOk = await page.$eval('.poster img', (img) => img.complete && img.naturalWidth > 0).catch(() => false)
  ok(posterOk, `${label}：封面图片经解密后正常显示`)

  await page.click('.actions .btn.primary')
  await page.waitForSelector('video')
  const started = await page
    .waitForFunction(() => {
      const v = document.querySelector('video')
      return v && v.currentTime > 2 && !v.paused
    }, { timeout: 30000 })
    .then(() => true)
    .catch(() => false)
  ok(started, `${label}：视频开始播放并持续推进`)
  const info = await page.$eval('video', (v) => ({ w: v.videoWidth, h: v.videoHeight, d: v.duration }))
  ok(info.w === 1280 && info.h === 720 && Math.abs(info.d - 120) < 1, `${label}：解码出正确的画面尺寸和时长 ${info.w}×${info.h} ${info.d?.toFixed(1)}s`)

  // 拖动到 80 秒：只拉取需要的密文块
  await page.$eval('video', (v) => (v.currentTime = 80))
  const seeked = await page
    .waitForFunction(() => {
      const v = document.querySelector('video')
      return v.currentTime > 81 && !v.paused && v.readyState >= 3
    }, { timeout: 30000 })
    .then(() => true)
    .catch(() => false)
  ok(seeked, `${label}：拖动到 80 秒后继续播放`)

  // 字幕：启用后应加载出字幕内容
  const cue = await page.evaluate(async () => {
    const v = document.querySelector('video')
    const tr = v.textTracks[0]
    if (!tr) return ''
    tr.mode = 'showing'
    for (let i = 0; i < 50 && !(tr.cues && tr.cues.length); i++) await new Promise((r) => setTimeout(r, 100))
    return tr.cues?.[0]?.text || ''
  })
  ok(cue === '字幕测试文本', `${label}：字幕经解密加载（“${cue}”）`)

  // 等待预览图生成后，悬停进度条
  let tp = false
  for (let i = 0; i < 20 && !tp; i++) {
    await page.reload({ waitUntil: 'networkidle2' })
    await page.waitForSelector('video')
    await sleep(500)
    const bar = await page.$('.bar')
    const box = await bar.boundingBox()
    await page.mouse.move(box.x + box.width * 0.4, box.y + box.height / 2)
    await sleep(600)
    tp = await page.evaluate(() => {
      const t = document.querySelector('.tip .thumb')
      return !!t && getComputedStyle(t).backgroundImage.includes('/b/')
    })
    if (!tp) await sleep(1500)
  }
  ok(tp, `${label}：进度条悬停显示预览图`)
  if (tp) {
    const loaded = await page.evaluate(
      () =>
        new Promise((resolve) => {
          const url = getComputedStyle(document.querySelector('.tip .thumb')).backgroundImage.slice(5, -2)
          const img = new Image()
          img.onload = () => resolve(img.naturalWidth > 0)
          img.onerror = () => resolve(false)
          img.src = url
        }),
    )
    ok(loaded, `${label}：预览图雪碧图解密成功`)
  }
  await shot(page, `${label}-player`)

  // 播放一会儿后返回，首页应出现“继续观看”
  await page.$eval('video', (v) => (v.currentTime = 40))
  await sleep(3000)
  await page.click('.top .icon-btn')
  await sleep(1500)
  await page.goto(base + '/#/', { waitUntil: 'networkidle2' })
  await page.waitForSelector('.section-title')
  const rows = await page.$$eval('.section-title', (h) => h.map((x) => x.textContent))
  ok(rows.some((r) => r.includes('继续观看')), `${label}：首页出现“继续观看”`)
  await shot(page, `${label}-home`)
}

try {
  // ================= 网页模式 =================
  console.log('\n[网页模式] 浏览器内加密 + Service Worker 解密')
  const webBase = `http://localhost:${port}`
  const ctx1 = await browser.createBrowserContext()
  const page = await newPage(ctx1, 'web')
  await page.goto(webBase + '/', { waitUntil: 'networkidle2' })
  await page.waitForSelector('#c', { timeout: 20000 })
  await shot(page, 'web-setup')
  await page.type('#c', setupCode)
  await page.$eval('#u', (e) => (e.value = ''))
  await page.type('#u', 'admin')
  await page.type('#p', 'admin-password-1')
  await page.type('#p2', 'admin-password-1')
  await page.click('button[type=submit]')
  await page.waitForSelector('.topbar', { timeout: 20000 })
  ok(true, '网页模式：完成初始化并登录')
  ok(await page.evaluate(() => !!navigator.serviceWorker.controller), '网页模式：Service Worker 已接管页面')
  ok(await waitForCards(page, webBase), '网页模式：首页出现扫描到的视频')
  await sleep(1000)
  await shot(page, 'web-home-first')
  await playbackTest(page, webBase, 'web')

  for (const [path, name] of [['/#/admin', 'admin-dashboard'], ['/#/admin/libraries', 'admin-libraries'], ['/#/admin/users', 'admin-users'], ['/#/settings', 'settings'], ['/#/history', 'history'], ['/#/library/1?view=folders', 'library-folders']]) {
    await page.goto(webBase + path, { waitUntil: 'networkidle2' })
    await sleep(1200)
    await shot(page, `web-${name}`)
    if (name === 'settings') {
      const conn = await page.$eval('.kv', (e) => e.textContent).catch(() => '')
      ok(conn.includes('ML-KEM-768'), '网页模式：设置页显示后量子加密套件')
    }
  }
  const webErrors = page.errors.filter((e) => !e.includes('favicon'))
  ok(webErrors.length === 0, '网页模式：没有控制台错误 / CSP 违规' + (webErrors.length ? '\n      ' + webErrors.join('\n      ') : ''))

  // ---- 管理后台界面：添加受限媒体库 ----
  await page.goto(webBase + '/#/admin/libraries', { waitUntil: 'networkidle2' })
  await page.waitForSelector('.head .btn.primary')
  await page.click('.head .btn.primary')
  await page.waitForSelector('.dialog input.input')
  const dlg = await page.$('.dialog')
  const inputs = await dlg.$$('input.input')
  await inputs[0].type('私人收藏')
  await dlg.$eval('select', (s) => {
    s.value = 'jav'
    s.dispatchEvent(new Event('change'))
  })
  await page.evaluate(() => {
    const sel = document.querySelectorAll('.dialog select')[1]
    sel.value = 'none' // 测试环境没有 MetaTube，不刮削
    sel.dispatchEvent(new Event('change'))
  })
  const pathInput = (await dlg.$$('.pathrow input.input'))[0]
  await pathInput.type(media2)
  const restrictedChecked = await dlg.$eval('input[type=checkbox]', (c) => c.checked)
  ok(restrictedChecked, '管理后台：选择“日本影片”类型时自动勾选“受限”')
  await shot(page, 'web-admin-add-library')
  await page.click('.dialog footer .btn.primary')
  await page.waitForFunction(() => !document.querySelector('.dialog'), { timeout: 10000 })
  await sleep(2500)
  await page.goto(webBase + '/#/admin/libraries', { waitUntil: 'networkidle2' })
  await sleep(800)
  const libNames = await page.$$eval('.lib h2', (h) => h.map((x) => x.textContent.trim()))
  ok(libNames.some((n) => n.includes('私人收藏')), '管理后台：通过界面创建了受限媒体库')

  // ---- 设置 PIN ----
  await page.goto(webBase + '/#/settings', { waitUntil: 'networkidle2' })
  await page.waitForSelector('input[placeholder="当前登录密码"]')
  await page.type('input[placeholder="当前登录密码"]', 'admin-password-1')
  await page.type('input[placeholder^="新 PIN"]', '1357')
  await page.click('form.row .btn.primary')
  await sleep(1500)
  const pinState = await page.$eval('section.card-panel:nth-of-type(2) b', (b) => b.textContent)
  ok(pinState.includes('已锁定'), `设置 PIN 后受限内容处于锁定状态（${pinState}）`)

  // ---- 首页：受限库显示为锁定，点击后弹出 PIN 输入框 ----
  await page.goto(webBase + '/#/', { waitUntil: 'networkidle2' })
  await page.waitForSelector('.tile')
  const tiles = await page.$$('.tile')
  let lockedTile = null
  for (const t of tiles) if ((await t.evaluate((e) => e.textContent)).includes('私人收藏')) lockedTile = t
  ok(!!lockedTile, '首页显示受限库的入口')
  await lockedTile.click()
  await page.waitForSelector('.pin', { timeout: 5000 })
  await shot(page, 'web-pin-dialog')
  await page.type('.pin', '0000')
  await page.keyboard.press('Enter')
  await page.waitForFunction(() => document.querySelector('.dialog .err')?.textContent.includes('PIN'), { timeout: 5000 })
  ok(true, '输入错误的 PIN 被拒绝')
  await page.$eval('.pin', (e) => (e.value = ''))
  await page.type('.pin', '1357')
  await page.keyboard.press('Enter')
  const unlocked = await page
    .waitForFunction(() => location.hash.startsWith('#/library/') && document.querySelector('.grid .card'), { timeout: 20000 })
    .then(() => true)
    .catch(() => false)
  ok(unlocked, '输入正确的 PIN 后进入受限库并看到内容')
  await shot(page, 'web-restricted-library')

  // ---- 顶栏一键锁定 ----
  await page.click('.topbar button[title="锁定受限内容"]')
  await sleep(1200)
  await page.goto(webBase + '/#/', { waitUntil: 'networkidle2' })
  await page.waitForSelector('.tile')
  const relocked = await page.$$eval('.tile', (ts) => ts.some((t) => t.textContent.includes('私人收藏') && t.querySelector('svg')))
  const latestRows = await page.$$eval('.section-title', (h) => h.map((x) => x.textContent))
  ok(relocked && !latestRows.some((r) => r.includes('私人收藏')), '锁定后首页不再展示受限库的内容')

  // 刷新后仍保持登录（令牌恢复 + 新握手）
  await page.reload({ waitUntil: 'networkidle2' })
  ok(!!(await page.$('.topbar')), '网页模式：刷新页面后保持登录')

  // ================= 本地客户端模式 =================
  console.log('\n[本地客户端模式] 浏览器 → 127.0.0.1 → Go 客户端加密 → 服务端')
  await start(['client', '-server', `http://127.0.0.1:${port}`, '-server-key', pubkey, '-listen', `127.0.0.1:${cport}`, '-data', join(work, 'client'), '-open=false'], (log) =>
    log.includes('本地客户端已启动'),
  )
  const localBase = `http://127.0.0.1:${cport}`
  const ctx2 = await browser.createBrowserContext()
  const lp = await newPage(ctx2, 'local')
  await lp.goto(localBase + '/', { waitUntil: 'networkidle2' })
  await lp.waitForSelector('#u', { timeout: 20000 })
  await shot(lp, 'local-login')
  await lp.type('#u', 'admin')
  await lp.type('#p', 'admin-password-1')
  await lp.click('button[type=submit]')
  await lp.waitForSelector('.topbar', { timeout: 20000 })
  ok(true, '本地模式：登录成功')
  ok(await lp.evaluate(() => !navigator.serviceWorker.controller), '本地模式：不使用 Service Worker（由 Go 客户端解密）')
  await playbackTest(lp, localBase, 'local')
  const localErrors = lp.errors.filter((e) => !e.includes('favicon'))
  ok(localErrors.length === 0, '本地模式：没有控制台错误 / CSP 违规' + (localErrors.length ? '\n      ' + localErrors.join('\n      ') : ''))
} catch (e) {
  failed++
  console.error('测试异常：', e)
} finally {
  await browser.close().catch(() => {})
  for (const p of procs) p.kill()
  await sleep(800)
  try {
    rmSync(work, { recursive: true, force: true })
  } catch {}
}
console.log(failed ? `\n失败 ${failed} 项` : '\n全部通过')
console.log('截图目录：' + shots)
process.exit(failed ? 1 : 0)
