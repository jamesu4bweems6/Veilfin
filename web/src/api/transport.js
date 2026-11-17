// 传输层：前端业务代码只调用 rpc()，不关心当前是本地客户端模式还是网页模式。
//
//   本地客户端模式：浏览器 → 127.0.0.1（明文，不出本机）→ Go 客户端加密 → CDN → 服务端
//   网页模式：浏览器内用 JS 完成握手和加解密，视频 / 图片由 Service Worker 解密
//
// 响应里的对象引用 {"$b","k","s","t"} 会被替换成 /b/{id} 形式的 URL，
// 本地模式由 Go 客户端替换，网页模式在这里替换并把密钥交给 Service Worker。

export class RpcError extends Error {
  constructor(code, message) {
    super(message)
    this.code = code
  }
}

// ---------------- 本地客户端模式 ----------------

export class LocalTransport {
  mode = 'local'

  async init() {}

  async call(m, p) {
    let r
    try {
      r = await fetch('/api/rpc', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-VF-Local': '1' },
        body: JSON.stringify({ m, p: p ?? {} }),
      })
    } catch {
      throw new RpcError('network', '无法连接本地客户端，请确认 veilfin client 仍在运行')
    }
    if (!r.ok) throw new RpcError('network', '本地客户端返回 HTTP ' + r.status)
    const j = await r.json()
    if (!j.ok) throw new RpcError(j.code || 'error', j.err || '未知错误')
    return j.data
  }

  // 页面关闭时也能发出的请求（上报播放进度）
  beacon(m, p) {
    try {
      fetch('/api/rpc', {
        method: 'POST',
        keepalive: true,
        headers: { 'Content-Type': 'application/json', 'X-VF-Local': '1' },
        body: JSON.stringify({ m, p }),
      })
    } catch {}
  }

  async status() {
    const r = await fetch('/api/status', { headers: { 'X-VF-Local': '1' } })
    return (await r.json()).data
  }
}

