# Veilfin（帷影）

类似 Emby 的个人视频服务。**所有请求、响应、视频、图片和字幕都在应用层做了端到端加密**，即使服务端部署在 CDN 之后，CDN 也只能看到密文，没法审查内容。

- 单个可执行文件（约 13 MB），只依赖 ffmpeg；数据库是内置的 SQLite
- 多用户、媒体库权限、受限媒体库加 PIN 锁
- 观看进度、继续观看、观看记录、播放统计
- 元数据：NFO / JSON 附加文件 / 同目录图片 → MetaTube（番号）/ TMDB（电影）→ 手动编辑（编辑过的字段会被锁定）
- 自动截图做封面，可以在视频里挑一帧做海报；外挂和内嵌字幕（自动识别 GBK 编码）；进度条预览图
- 目录监听、定时扫描、文件夹浏览、搜索、筛选
- 加密备份 / 恢复

## 架构

```
                    ┌───────────── 你的电脑 ─────────────┐
 本地客户端模式：   浏览器 ──明文(不出本机)──► veilfin client ═══╗
                    └────────────────────────────────────┘          ║
                                                                     ║ 密文
 网页模式（手机）：  浏览器（JS 加解密 + Service Worker 解密视频）═══╣
                                                                     ▼
                                                   CDN（只看到密文）═══► veilfin server ──► 媒体目录
```

| 模式 | 适用 | 前端代码从哪来 | CDN 能否篡改代码 |
|---|---|---|---|
| **本地客户端**（推荐） | Windows / Mac / Linux 电脑 | 编译在客户端程序里 | 不能 |
| 网页模式 | 手机、平板、临时设备 | 服务端下发 | 前端走 CDN 时理论上可以；用 `-api-base` 让前端不走 CDN 就不能 |

## 加密设计

| 环节 | 算法 |
|---|---|
| 握手 | **X25519 + ML-KEM-768** 混合密钥交换（后量子，防“先存后解”），Ed25519 服务端身份签名，HKDF-SHA256 |
| API | **XChaCha20-Poly1305 内层 + AES-256-GCM 外层**，所有接口都走同一个入口 `POST /x`，按 1 KiB 填充长度 |
| 前向安全 | 两个方向各有一个哈希棘轮，每 500 条消息或 5 分钟换一代密钥，旧密钥用完即销毁；每 24 小时重新握手 |
| 防重放 | 请求带时间戳（±60 秒）和随机 ID，重复的请求会被拒绝 |
| 媒体 | 每个对象一把独立密钥，1 MiB 分块 AES-256-GCM；nonce 由“块序号 + 内容”经 HMAC 派生（SIV 思路，原地改文件也不会重用 nonce）；AAD 绑定对象 ID、块序号和“是否最后一块”，可防篡改、防调换、防截断 |
| 媒体访问 | 对象 ID 是随机数；必须带短期令牌（绑定用户和解锁状态）才能取到密文；密文是确定的，CDN 可以缓存 |
| 密码 | Argon2id（64 MiB，2 轮） |

CDN 能看到的只有：访问 IP、时间、请求大小，以及 `/h`、`/x`、`/m/{随机ID}` 这几个不透明路径。

## 快速开始

### 1. 服务端（源站）

用 Docker：

```bash
docker compose up -d
docker compose logs veilfin     # 记下“初始化代码”和“服务端公钥”
```

或者直接运行（需要先安装 ffmpeg）：

```bash
./veilfin server -media /path/to/videos -web
```

启动日志会打印三样东西：

- **服务端公钥**：客户端用它来固定信任
- **公钥指纹**：给人工核对用
- **初始化代码**：首次创建管理员时需要输入，防止别人抢先初始化

`data/keys/` 下的两个密钥文件**丢了就没法恢复**，请用 `veilfin backup` 定期备份。

CDN 回源到 8097 端口即可。如果 CDN 到源站也想走 HTTPS，可以用 `deploy/Caddyfile`，或者给服务端加 `-tls-cert` / `-tls-key` 参数。

### 2. 本地客户端（电脑上看，推荐）

```bash
veilfin client -server https://你的CDN域名 -server-key '服务端公钥'
```

启动后会自动打开浏览器，地址是 `http://127.0.0.1:8096`。第一次访问时，用初始化代码创建管理员。

客户端默认监听 `0.0.0.0:8096`。局域网里的其他设备可以直接用 `http://电脑的IP:8096` 访问，也可以放在反向代理后面。**每个浏览器都要用自己的账号登录**，登录状态通过 HttpOnly Cookie 和浏览器绑定，一个人登录不会让别的设备也处于登录状态。

> 注意：本地客户端输出的是**解密后的明文**。从客户端到浏览器这一段，如果要经过公网或 CDN，请在反向代理上启用 HTTPS，并且这一段不要套 CDN，否则前面所有的端到端加密就白费了。需要从外网访问时，更推荐用服务端的网页模式：加密一直延续到浏览器里。

### 3. 网页模式（手机上看）

服务端加了 `-web` 后，直接在手机浏览器打开 `https://你的域名`。要求：

- 必须是 HTTPS。Service Worker 和 WebCrypto 只能在安全上下文中使用
- Firefox 的隐私模式用不了，因为它禁用了 Service Worker

**强隔离部署**：前端页面走一个不经过 CDN 的子域名（DNS 直接指向源站），API 和视频走 CDN。这样 CDN 就没有机会篡改前端代码：

```bash
veilfin server -web -api-base https://cdn.example.com -web-origin https://app.example.com
```

### 4. 添加媒体库

进入「管理后台 → 媒体库」，选择类型和目录：

| 类型 | 展示 | 元数据 |
|---|---|---|
| 自制视频 | 横版缩略图 | 读取 `<文件名>.json` / `.nfo` / 同名图片；没有封面时自动截图 |
| 电影 | 竖版海报 | NFO 优先，其次 TMDB（在“服务器设置”填 API Key） |
| 日本影片 | 竖版海报 | 从文件名识别番号（`ABC-123`、`FC2-PPV-xxx`、`-C` 中文字幕、`CD1` 分段等），通过 MetaTube 刮削 |

勾选「受限」后，这个库只对授权用户可见；用户设置了 PIN 的话，还要先解锁才能看到，包括首页、搜索和视频数据本身。

自制视频可以在旁边放一个同名 `.json` 文件来提供元数据：

```json
{ "title": "2023 年家庭聚会", "overview": "…", "date": "2023-10-01", "tags": ["家庭"], "actors": ["爷爷", "奶奶"] }
```

## 命令

```
veilfin server   [-listen 0.0.0.0:8097] [-data ./data] [-media 目录] [-web] [-api-base URL] [-web-origin URL] [-ffmpeg 目录] [-tls-cert/-tls-key]
veilfin client   -server URL [-server-key 公钥] [-listen 0.0.0.0:8096] [-open=false]
veilfin keyinfo  [-data ./data]                      显示服务端公钥和指纹
veilfin backup   [-data ./data] [-out 文件] [-no-assets]   加密备份（口令用 Argon2id 派生，XChaCha20-Poly1305 加密）
veilfin restore  -in 文件 [-data ./data] [-force]     恢复（先停止服务端）
veilfin user reset-password -name 用户名 [-data ./data]   忘记密码时重置
```

服务端的选项都可以用环境变量设置：`VF_LISTEN`、`VF_DATA`、`VF_MEDIA`、`VF_WEB=1`、`VF_API_BASE`、`VF_WEB_ORIGIN`、`VF_FFMPEG`。

## 构建

```bash
sh build.sh          # 先构建前端，再交叉编译 Windows / Linux / macOS 到 dist/
```

Node 只在构建前端时需要，运行时不需要。仓库里已经带有构建好的前端（`internal/webui/dist`），所以只改 Go 代码时直接 `go build ./cmd/veilfin` 就行。

## 测试

```bash
go test ./...                              # 单元测试和端到端测试（媒体相关的测试需要 ffmpeg）
cd web && npm run build
node tests/crypto-interop.mjs              # 浏览器端 JS 加密实现与 Go 服务端的互通测试
node tests/browser.mjs                     # 真实浏览器（Edge/Chrome 无头）测试两种模式的完整流程，截图存在 tests/screenshots/
```

端到端测试中有一个模拟的 CDN 中间层，会记录所有经过的 URL、请求头、请求体和响应体。测试会断言其中找不到任何片名、简介、演员、字幕文本、密码、PIN、接口名，以及视频和图片的原始字节。

## 已知限制

- 只支持浏览器能直接播放的格式（MP4/H.264、WebM），没有转码。HEVC 视频要看浏览器是否支持硬解。
- 没有电视剧的“季 / 集”层级。同一文件夹里的视频可以用“下一个”连续播放，也可以按文件夹浏览。
- MetaTube 的接口是按它公开的 HTTP API 实现的，只用模拟服务测试过，还没有对接过真实的 MetaTube 服务。
- 网页模式下，CDN 能看到访问时间、IP 和流量大小。如果有人知道某部公开影片的确切文件大小，理论上可以据此猜出你看的是哪部。
