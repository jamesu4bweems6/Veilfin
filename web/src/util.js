export function fmtTime(sec) {
  if (!isFinite(sec) || sec < 0) sec = 0
  sec = Math.floor(sec)
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  const mm = String(m).padStart(h ? 2 : 1, '0')
  return (h ? h + ':' : '') + mm + ':' + String(s).padStart(2, '0')
}

export function fmtDuration(sec) {
  if (!sec) return ''
  const h = Math.floor(sec / 3600)
  const m = Math.round((sec % 3600) / 60)
  if (h && m) return `${h} 小时 ${m} 分钟`
  if (h) return `${h} 小时`
  return `${Math.max(m, 1)} 分钟`
}

export function fmtSize(n) {
  if (!n) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  while (n >= 1024 && i < u.length - 1) {
    n /= 1024
    i++
  }
  return n.toFixed(i ? 1 : 0) + ' ' + u[i]
}

export function fmtDate(ts) {
  if (!ts) return ''
  const d = new Date(ts * 1000)
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
}

export function fmtDateTime(ts) {
  if (!ts) return ''
  const d = new Date(ts * 1000)
  return `${fmtDate(ts)} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

export function resolutionLabel(w, h) {
  if (!w || !h) return ''
  const p = Math.min(w, h)
  if (w >= 3800 || p >= 2000) return '4K'
  if (w >= 2500 || p >= 1400) return '1440p'
  if (w >= 1900 || p >= 1000) return '1080p'
  if (w >= 1200 || p >= 700) return '720p'
  if (p >= 480) return '480p'
  return 'SD'
}

export function debounce(fn, ms) {
  let t
  return (...a) => {
    clearTimeout(t)
    t = setTimeout(() => fn(...a), ms)
  }
}

export const kindNames = { home: '自制视频', movie: '电影', jav: '日本影片', other: '其他' }
export const scraperNames = { none: '不刮削', tmdb: 'TMDB（电影）', metatube: 'MetaTube（番号）' }

// 生成稳定的占位渐变色（没有封面时使用）
export function placeholderStyle(seed) {
  let h = 0
  for (const ch of String(seed)) h = (h * 31 + ch.charCodeAt(0)) >>> 0
  const a = h % 360
  return { background: `linear-gradient(160deg, hsl(${a} 30% 26%), hsl(${(a + 40) % 360} 35% 13%))` }
}

export function randomId() {
  const b = new Uint8Array(12)
  crypto.getRandomValues(b)
  return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('')
}

export function readFileBase64(file) {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => resolve(String(r.result).split(',')[1])
    r.onerror = () => reject(r.error)
    r.readAsDataURL(file)
  })
}
