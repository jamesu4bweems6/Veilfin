// Package server 是部署在源站、位于 CDN 之后的服务端。对外只有以下入口，CDN 只能看到不透明的二进制数据：
//
//	POST /h          握手
//	POST /x          加密 RPC（所有业务接口都走这一个入口）
//	GET  /m/{id}     加密对象（视频 / 图片 / 字幕 / 预览图），支持 Range，内容固定可被 CDN 缓存
//	GET  /k          服务端身份公钥（只用于首次信任）
//	GET  /*          网页模式的前端页面（可选，-web 开启）
package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"veilfin/internal/api"
	"veilfin/internal/db"
	"veilfin/internal/library"
	"veilfin/internal/media"
	"veilfin/internal/mediacrypt"
	"veilfin/internal/proto"
	"veilfin/internal/webui"
)

const (
	rpcSkew        = 60 * time.Second
	ridTTL         = 2 * rpcSkew // 超过时间窗的请求本来就会因时间戳被拒，不必再记 rid
	blobTokenTTL   = 6 * time.Hour
	loginTokenTTL  = 30 * 24 * time.Hour
	unlockDuration = 2 * time.Hour
	userCacheTTL   = 30 * time.Second
	maxLoginFails  = 5
	lockoutTime    = 15 * time.Minute
)

type Config struct {
	DataDir string
	// InitialMedia 不为空时，首次初始化会自动创建一个指向该目录的媒体库。
	InitialMedia string
	// Web 为 true 时提供网页模式的前端页面。
	Web bool
	// APIBase 是网页模式下前端访问 API 的地址（前端与 API 分开部署时使用），为空表示同源。
	APIBase string
	// WebOrigin 是允许跨域访问 API 的前端源（前端与 API 分开部署时使用）。
	WebOrigin string
	// FFmpegDir 指定 ffmpeg 所在目录，为空则在 PATH 中查找。
	FFmpegDir string
	Version   string
}

type Server struct {
	cfg       Config
	id        ed25519.PrivateKey
	keys      *mediacrypt.Keys
	DB        *db.DB
	Lib       *library.Service
	sessions  *sessionStore
	setupMu   sync.Mutex
	setupCode string
	hsMu      sync.Mutex
	hsTokens  float64
	hsLast    time.Time
	ghostMu   sync.Mutex
	ghosts    map[string]*ghost // 不存在的用户名的失败计数

	ucMu      sync.Mutex
	userCache map[int64]*userAccess

	now func() time.Time
}

// New 打开数据目录、数据库并初始化各个组件。ctx 结束时后台任务随之停止。
func New(ctx context.Context, cfg Config) (*Server, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, err
	}
	id, master, err := LoadKeys(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	keys, err := mediacrypt.NewKeys(master)
	if err != nil {
		return nil, err
	}
	d, err := db.Open(db.PathIn(cfg.DataDir))
	if err != nil {
		return nil, err
	}
	ff, err := media.Find(cfg.FFmpegDir)
	if err != nil {
		log.Printf("警告：%v。视频仍可播放，但不会生成截图、字幕和预览图。", err)
		ff = nil
	}
	s := &Server{
		cfg:       cfg,
		id:        id,
		keys:      keys,
		DB:        d,
		Lib:       library.New(ctx, d, ff, filepath.Join(cfg.DataDir, "assets")),
		sessions:  newSessionStore(),
		userCache: map[int64]*userAccess{},
		ghosts:    map[string]*ghost{},
		hsTokens:  100,
		now:       time.Now,
	}
	n, err := d.CountUsers(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		b := make([]byte, 4)
		rand.Read(b)
		s.setupCode = strings.ToUpper(hex.EncodeToString(b))
	}
	return s, nil
}

// SetupCode 返回首次初始化所需的一次性代码（已初始化则为空）。
func (s *Server) SetupCode() string {
	s.setupMu.Lock()
	defer s.setupMu.Unlock()
	return s.setupCode
}

// PublicKey 返回服务端身份公钥。
func (s *Server) PublicKey() ed25519.PublicKey { return s.id.Public().(ed25519.PublicKey) }

// DropSessions 清空所有会话（测试用，也可用于强制所有客户端重新握手）。
func (s *Server) DropSessions() { s.sessions.clear() }

// Run 启动后台任务：会话清理、定时扫描、目录监听。
func (s *Server) Run(ctx context.Context) {
	go s.Lib.RunScheduler(ctx)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sessions.gc(s.now())
			s.DB.PurgeExpiredTokens(ctx)
		}
	}
}

func (s *Server) Close() error { return s.DB.Close() }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /h", s.handleHandshake)
	mux.HandleFunc("POST /x", s.handleRPC)
	mux.HandleFunc("GET /m/{id}", s.handleBlob)
	mux.HandleFunc("GET /k", s.handlePubKey)
	if s.cfg.Web {
		mux.Handle("GET /", webui.Handler(s.webConfig))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if s.cfg.WebOrigin != "" && r.Header.Get("Origin") == s.cfg.WebOrigin {
			h.Set("Access-Control-Allow-Origin", s.cfg.WebOrigin)
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Expose-Headers", "Content-Range, Content-Length, Accept-Ranges")
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET, POST")
				h.Set("Access-Control-Allow-Headers", "Content-Type, Range, "+api.TokenHeader)
				h.Set("Access-Control-Max-Age", "86400")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if s.cfg.Web && !isAPIPath(r.URL.Path) {
			h.Set("Content-Security-Policy", s.webCSP())
			h.Set("X-Frame-Options", "DENY")
		}
		mux.ServeHTTP(w, r)
	})
}

func isAPIPath(p string) bool {
	return p == "/h" || p == "/x" || p == "/k" || strings.HasPrefix(p, "/m/")
}

func (s *Server) webCSP() string {
	connect := "'self'"
	if s.cfg.APIBase != "" {
		connect += " " + s.cfg.APIBase
	}
	return "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; " +
		"connect-src " + connect + "; worker-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"
}

func (s *Server) webConfig(r *http.Request) []byte {
	b, _ := json.Marshal(map[string]any{
		"mode":    "web",
		"pubkey":  base64.StdEncoding.EncodeToString(s.PublicKey()),
		"apiBase": s.cfg.APIBase,
	})
	return b
}

func (s *Server) handlePubKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(s.PublicKey())
}

// allowHandshake 是全局的握手限速（令牌桶）：防止握手洪水把真实会话挤出会话表。
// 个人使用场景下每秒 20 次、突发 100 次绰绰有余。
func (s *Server) allowHandshake() bool {
	s.hsMu.Lock()
	defer s.hsMu.Unlock()
	now := s.now()
	if !s.hsLast.IsZero() {
		s.hsTokens = min(100, s.hsTokens+now.Sub(s.hsLast).Seconds()*20)
	}
	s.hsLast = now
	if s.hsTokens < 1 {
		return false
	}
	s.hsTokens--
	return true
}

func (s *Server) handleHandshake(w http.ResponseWriter, r *http.Request) {
	if !s.allowHandshake() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, proto.ClientHelloSize+1))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	now := s.now()
	resp, sess, err := proto.ServerRespond(body, s.id, now)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s.sessions.add(sess, now)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(resp)
}

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	// 先只读消息头找会话，不存在的会话不读后面的大消息体
	head := make([]byte, proto.HeaderSize)
	if _, err := io.ReadFull(r.Body, head); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sid, ok := proto.PeekSID(head)
	if !ok {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ss := s.sessions.get(sid, s.now())
	if ss == nil {
		// 会话不存在或已过期，客户端收到 401 后会重新握手
		http.Error(w, "session", http.StatusUnauthorized)
		return
	}
	rest, err := io.ReadAll(io.LimitReader(r.Body, proto.MaxMessageSize-proto.HeaderSize+1))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pt, err := ss.Open(append(head, rest...))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ss.touch(s.now()) // 只有解密成功的消息才算活跃，防止知道 SID 的人无限续命会话
	var req api.Request
	var resp api.Response
	if err := json.Unmarshal(pt, &req); err != nil {
		resp = failResp("", &rpcError{api.CodeBadRequest, "请求格式错误"})
	} else {
		resp = s.dispatch(r.Context(), ss, &req)
	}
	out, err := json.Marshal(resp)
	if err == nil && len(out) > proto.MaxPlaintext {
		out, err = json.Marshal(failResp(req.RID, &rpcError{api.CodeInternal, "响应过大"}))
	}
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	enc, err := ss.Seal(out)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(enc)
}

// ---- 加密对象 ----

// userAccess 缓存某个用户的访问权限，避免每个 Range 请求都查库。
type userAccess struct {
	user       *db.User
	allowed    map[int64]bool
	restricted map[int64]bool
	exp        time.Time
}

func (s *Server) access(ctx context.Context, uid int64) (*userAccess, error) {
	now := s.now()
	s.ucMu.Lock()
	if a, ok := s.userCache[uid]; ok && now.Before(a.exp) {
		s.ucMu.Unlock()
		return a, nil
	}
	s.ucMu.Unlock()

	u, err := s.DB.UserByID(ctx, uid)
	if err != nil {
		return nil, err
	}
	libs, err := s.DB.ListLibraries(ctx)
	if err != nil {
		return nil, err
	}
	a := &userAccess{user: u, allowed: map[int64]bool{}, restricted: map[int64]bool{}, exp: now.Add(userCacheTTL)}
	var grant map[int64]bool
	if !u.IsAdmin {
		ids, err := s.DB.LibraryAccess(ctx, uid)
		if err != nil {
			return nil, err
		}
		grant = map[int64]bool{}
		for _, id := range ids {
			grant[id] = true
		}
	}
	for _, l := range libs {
		if u.IsAdmin || grant[l.ID] {
			a.allowed[l.ID] = true
			if l.Restricted {
				a.restricted[l.ID] = true
			}
		}
	}
	s.ucMu.Lock()
	s.userCache[uid] = a
	s.ucMu.Unlock()
	return a, nil
}

// invalidateAccess 在用户或媒体库发生变化后清空权限缓存。
func (s *Server) invalidateAccess() {
	s.ucMu.Lock()
	s.userCache = map[int64]*userAccess{}
	s.ucMu.Unlock()
}

// blobError 返回错误。错误响应禁止缓存，避免 CDN 把一次 403 / 404 放大成所有人都无法播放。
func blobError(w http.ResponseWriter, code int) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, http.StatusText(code), code)
}

func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	b, err := s.DB.BlobByID(ctx, id)
	if err != nil {
		blobError(w, http.StatusNotFound)
		return
	}
	uid, gen, flags, ok := s.keys.ParseToken(r.Header.Get(api.TokenHeader), s.now())
	if !ok {
		blobError(w, http.StatusForbidden)
		return
	}
	a, err := s.access(ctx, uid)
	if err != nil || a.user.Disabled || gen != a.user.TokenGen || !a.allowed[b.LibraryID] {
		blobError(w, http.StatusForbidden)
		return
	}
	if a.restricted[b.LibraryID] && a.user.PinHash != "" && flags&mediacrypt.TokenUnlocked == 0 {
		blobError(w, http.StatusForbidden)
		return
	}
	f, err := os.Open(b.Path)
	if err != nil {
		blobError(w, http.StatusNotFound)
		return
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.Size() != b.Size {
		// 视频在扫描之后被修改过：拒绝服务旧对象，触发重新扫描
		if b.Kind == "video" {
			if it, err := s.DB.ItemByID(ctx, b.ItemID); err == nil {
				s.Lib.EnqueueScan(it.LibraryID, false)
			}
		}
		blobError(w, http.StatusNotFound)
		return
	}
	enc, nonce := s.keys.BlobKeys(b.ID)
	c, err := mediacrypt.NewCipher(enc, nonce, b.ID, b.Size)
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("ETag", `"`+b.ID+`"`)
	// 对象 ID 随内容变化，所以密文可以被永久缓存。CDN 缓存到的也只是密文。
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	// 传零值时间，不暴露文件修改时间（Last-Modified）
	http.ServeContent(w, r, "", time.Time{}, io.NewSectionReader(mediacrypt.NewEncryptor(c, f), 0, c.CipherSize()))
}

// ref 构造对象引用：把对象 ID 和解密密钥一起交给已授权的用户。
func (s *Server) ref(b *db.Blob) *api.BlobRef {
	if b == nil {
		return nil
	}
	enc, _ := s.keys.BlobKeys(b.ID)
	return &api.BlobRef{ID: b.ID, Key: base64.RawURLEncoding.EncodeToString(enc), Size: b.Size, Mime: b.Mime}
}

// refs 批量构造对象引用。
func (s *Server) refs(ctx context.Context, ids []string) (map[string]*api.BlobRef, error) {
	var want []string
	for _, id := range ids {
		if id != "" {
			want = append(want, id)
		}
	}
	m, err := s.DB.BlobSizes(ctx, want)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*api.BlobRef, len(m))
	for id, b := range m {
		out[id] = s.ref(b)
	}
	return out, nil
}

// ---- RPC 分发 ----

type rpcError struct {
	code string
	msg  string
}

func (e *rpcError) Error() string { return e.msg }

func errf(code, format string, a ...any) error { return &rpcError{code, fmt.Sprintf(format, a...)} }

func failResp(rid string, err error) api.Response {
	var re *rpcError
	if errors.As(err, &re) {
		return api.Response{RID: rid, Code: re.code, Err: re.msg}
	}
	if errors.Is(err, db.ErrNotFound) {
		return api.Response{RID: rid, Code: api.CodeNotFound, Err: "记录不存在"}
	}
	log.Printf("RPC 内部错误: %v", err)
	return api.Response{RID: rid, Code: api.CodeInternal, Err: "服务器内部错误"}
}

// call 是一次 RPC 调用的上下文。
type call struct {
	ctx context.Context
	ss  *serverSession
	req *api.Request
	u   *db.User // 已登录用户（公开方法中可能为 nil）
}

func (c *call) bind(v any) error {
	if len(c.req.P) == 0 || string(c.req.P) == "null" {
		return nil
	}
	if err := json.Unmarshal(c.req.P, v); err != nil {
		return errf(api.CodeBadRequest, "参数错误: %v", err)
	}
	return nil
}

type level int

const (
	public level = iota
	user
	admin
)

type method struct {
	lvl level
	fn  func(s *Server, c *call) (any, error)
}

var methods = map[string]method{}

func register(name string, lvl level, fn func(s *Server, c *call) (any, error)) {
	methods[name] = method{lvl, fn}
}

func (s *Server) dispatch(ctx context.Context, ss *serverSession, req *api.Request) api.Response {
	now := s.now()
	rid := req.RID
	if d := now.Sub(time.UnixMilli(req.TS)); d > rpcSkew || d < -rpcSkew {
		return failResp(rid, errf(api.CodeClockSkew, "请求时间戳超出允许范围"))
	}
	if rid == "" || len(rid) > 64 {
		return failResp(rid, errf(api.CodeBadRequest, "缺少请求 ID"))
	}
	ss.mu.Lock()
	_, dup := ss.rids[rid]
	if !dup {
		ss.rids[rid] = now
	}
	ss.mu.Unlock()
	if dup {
		return failResp(rid, errf(api.CodeReplay, "重复的请求"))
	}

	m, ok := methods[req.M]
	if !ok {
		return failResp(rid, errf(api.CodeNoMethod, "未知方法: %s", req.M))
	}
	c := &call{ctx: ctx, ss: ss, req: req}
	if uid := ss.uid(); uid != 0 {
		u, err := s.DB.UserByID(ctx, uid)
		if err == nil && !u.Disabled {
			c.u = u
		} else {
			ss.mu.Lock()
			ss.userID = 0
			ss.mu.Unlock()
		}
	}
	if m.lvl >= user && c.u == nil {
		if s.SetupCode() != "" {
			return failResp(rid, errf(api.CodeSetup, "服务端尚未初始化"))
		}
		return failResp(rid, errf(api.CodeUnauthorized, "未登录"))
	}
	if m.lvl == admin && !c.u.IsAdmin {
		return failResp(rid, errf(api.CodeForbidden, "需要管理员权限"))
	}
	data, err := m.fn(s, c)
	if err != nil {
		return failResp(rid, err)
	}
	b, err := json.Marshal(data)
	if err != nil {
		return failResp(rid, err)
	}
	return api.Response{RID: rid, OK: true, Data: b}
}

type ghost struct {
	fails int
	until time.Time
}

// slowFail 在认证失败时等待一会儿，拖慢暴力破解（真正的限制靠按账号持久化的锁定）。
func (s *Server) slowFail() {
	time.Sleep(700 * time.Millisecond)
}
