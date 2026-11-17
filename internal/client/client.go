// Package client 是本地客户端：前端页面编译在程序里，不经过 CDN 下发；
// 所有加解密都在本程序里用 Go 完成，CDN 只能看到密文。
// 每个浏览器有独立的会话和登录状态（见 browser.go），可以被多台设备或反向代理访问。
package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"veilfin/internal/api"
	"veilfin/internal/proto"
	"veilfin/internal/webui"
)

const (
	knownServersFile = "known_servers.json"
	localHeader      = "X-VF-Local"
)

type Config struct {
	Server    string // 服务端地址（通常是 CDN 域名）
	DataDir   string
	ServerKey string // 服务端身份公钥（base64），留空则首次连接时信任并记住
}

type Client struct {
	cfg  Config
	base string
	hc   *http.Client
	pub  ed25519.PublicKey

	bmu      sync.Mutex
	browsers map[string]*browser

	cache *chunkCache // 所有浏览器共用，键里带浏览器会话 ID，彼此读不到对方解密的数据

	pfMu        sync.Mutex
	prefetching map[string]bool

	// Warning 是需要提示用户的安全信息（例如首次信任了服务端公钥）。
	Warning string
}

// New 创建客户端，并确定要信任的服务端公钥。
func New(ctx context.Context, cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.Server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("服务端地址无效: %q", cfg.Server)
	}
	base := strings.TrimRight(u.String(), "/")
	hc := &http.Client{
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			ForceAttemptHTTP2:   true,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second,
		},
	}
	c := &Client{
		cfg:         cfg,
		base:        base,
		hc:          hc,
		browsers:    map[string]*browser{},
		cache:       newChunkCache(cacheBytes),
		prefetching: map[string]bool{},
	}
	pub, warn, err := resolveServerKey(ctx, hc, base, u.Host, cfg)
	if err != nil {
		return nil, err
	}
	c.Warning = warn
	c.pub = pub
	return c, nil
}

// ServerFingerprint 返回当前信任的服务端公钥指纹。
func (c *Client) ServerFingerprint() string { return proto.Fingerprint(c.pub) }

// resolveServerKey 的优先级：命令行参数 > 本地已记住的公钥 > 首次连接时获取（TOFU）。
func resolveServerKey(ctx context.Context, hc *http.Client, base, host string, cfg Config) (ed25519.PublicKey, string, error) {
	path := filepath.Join(cfg.DataDir, knownServersFile)
	known := map[string]string{}
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &known); err != nil {
			return nil, "", fmt.Errorf("读取 %s 失败: %w", path, err)
		}
	}
	if cfg.ServerKey != "" {
		pub, err := decodeKey(cfg.ServerKey)
		if err != nil {
			return nil, "", err
		}
		known[host] = base64.StdEncoding.EncodeToString(pub)
		return pub, "", saveKnown(path, known)
	}
	if s, ok := known[host]; ok {
		pub, err := decodeKey(s)
		return pub, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/k", nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("连接服务端失败: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil || resp.StatusCode != http.StatusOK || len(b) != ed25519.PublicKeySize {
		return nil, "", errors.New("获取服务端公钥失败")
	}
	pub := ed25519.PublicKey(b)
	known[host] = base64.StdEncoding.EncodeToString(pub)
	if err := saveKnown(path, known); err != nil {
		return nil, "", err
	}
	warn := "首次连接，已记住服务端公钥，指纹 " + proto.Fingerprint(pub) +
		"。请与服务端启动日志中的指纹核对，一致才说明没有被中间人替换。"
	return pub, warn, nil
}

func decodeKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("服务端公钥格式错误（应为 base64 编码的 32 字节）")
	}
	return ed25519.PublicKey(b), nil
}

func saveKnown(path string, known map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(known, "", "  ")
	return os.WriteFile(path, b, 0o600)
}

// Handler 返回 HTTP 处理器。每个浏览器需要自己登录。
func (c *Client) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", webui.Handler(func(*http.Request) []byte { return []byte(`{"mode":"local"}`) }))
	mux.HandleFunc("POST /api/rpc", c.handleRPC)
	mux.HandleFunc("GET /api/status", c.handleStatus)
	mux.HandleFunc("GET /b/{id}", c.handleBlob)
	return c.guard(mux)
}

// guard 防止跨站滥用：
//   - 拒绝跨站请求（Sec-Fetch-Site）；
//   - /api/ 必须带自定义请求头，跨域页面带它会触发 CORS 预检，而本程序不响应预检；
//   - 登录状态绑定在 SameSite=Strict 的 HttpOnly Cookie 上，别的网站（包括 DNS 重绑定）拿不到。
func (c *Client) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 只接受本页面自己发起的请求（same-origin）或用户直接打开（none）；
		// localhost 其他端口上的页面属于 same-site，同样拒绝
		if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get(localHeader) != "1" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data: blob:; "+
			"media-src 'self' blob:; connect-src 'self'; worker-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		next.ServeHTTP(w, r)
	})
}

type localResp struct {
	OK   bool   `json:"ok"`
	Data any    `json:"data,omitempty"`
	Code string `json:"code,omitempty"`
	Err  string `json:"err,omitempty"`
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	var re *RPCError
	if errors.As(err, &re) {
		writeJSON(w, localResp{Code: re.Code, Err: re.Msg})
		return
	}
	log.Printf("请求失败: %v", err)
	writeJSON(w, localResp{Code: "network", Err: "无法连接服务端：" + err.Error()})
}

// 浏览器不能直接调用的内部方法
var internalMethods = map[string]bool{"auth.resume": true, "auth.blobToken": true}

// handleRPC 把浏览器的明文请求转发到加密通道。
// 登录令牌和对象解密密钥只留在本程序内存里，不交给浏览器。
func (c *Client) handleRPC(w http.ResponseWriter, r *http.Request) {
	var in struct {
		M string          `json:"m"`
		P json.RawMessage `json:"p"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&in); err != nil {
		writeJSON(w, localResp{Code: api.CodeBadRequest, Err: "请求格式错误"})
		return
	}
	if internalMethods[in.M] {
		writeJSON(w, localResp{Code: api.CodeForbidden, Err: "不允许调用内部方法"})
		return
	}
	ctx := r.Context()
	b := c.browserFor(w, r)
	params := any(in.P)
	if in.M == "auth.logout" {
		params = api.TokenParams{Token: b.tr.token()}
	}
	raw, err := b.tr.Call(ctx, in.M, params)
	switch in.M {
	case "auth.logout", "auth.lock", "auth.setPin", "auth.changePassword":
		// 锁定 / 退出后，丢弃这个浏览器的对象密钥和已解密的数据块（受限内容不能再从缓存里读出来）
		c.forgetBlobs(b)
		if in.M == "auth.logout" {
			b.tr.setAuth("", false)
		}
		b.tr.resetBlobToken()
	case "auth.unlock":
		b.tr.resetBlobToken() // 解锁状态变了，访问令牌要重新获取
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	switch in.M {
	case "auth.login", "setup.create", "auth.changePassword":
		var lr api.LoginResult
		if err := json.Unmarshal(raw, &lr); err != nil {
			writeErr(w, err)
			return
		}
		b.tr.setAuth(lr.Token, true)
		raw = lr.Me
	}
	data, err := c.rewriteRefs(b, raw)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, localResp{OK: true, Data: data})
}

// forgetBlobs 清空某个浏览器的对象密钥登记表和它的解密缓存。
func (c *Client) forgetBlobs(b *browser) {
	b.blobMu.Lock()
	b.blobs = map[string]*blobInfo{}
	b.blobMu.Unlock()
	c.cache.clearPrefix(b.id + "/")
}

// rewriteRefs 把响应里的所有对象引用替换成本地 URL（/b/{id}），并记住解密密钥。
func (c *Client) rewriteRefs(b *browser, raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || !bytes.Contains(raw, []byte(`"$b"`)) {
		return raw, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	var walk func(any) (any, error)
	walk = func(x any) (any, error) {
		switch t := x.(type) {
		case map[string]any:
			if id, ok := t["$b"].(string); ok {
				ref := api.BlobRef{ID: id}
				ref.Key, _ = t["k"].(string)
				ref.Mime, _ = t["t"].(string)
				if n, ok := t["s"].(json.Number); ok {
					ref.Size, _ = n.Int64()
				}
				return c.registerRef(b, &ref)
			}
			for k, val := range t {
				nv, err := walk(val)
				if err != nil {
					return nil, err
				}
				t[k] = nv
			}
			return t, nil
		case []any:
			for i, val := range t {
				nv, err := walk(val)
				if err != nil {
					return nil, err
				}
				t[i] = nv
			}
			return t, nil
		}
		return x, nil
	}
	nv, err := walk(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(nv)
}

func (c *Client) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	b := c.browserFor(w, r)
	s, err := b.tr.session(ctx)
	st := map[string]any{
		"mode":        "local",
		"server":      c.cfg.Server,
		"fingerprint": c.ServerFingerprint(),
		"suite":       "X25519+ML-KEM-768 / Ed25519 / XChaCha20-Poly1305 ⊂ AES-256-GCM",
		"warning":     c.Warning,
		"connected":   err == nil,
		"authed":      b.tr.isAuthed(),
	}
	if err != nil {
		st["error"] = err.Error()
	} else {
		st["sessionAge"] = int(time.Since(s.Created).Seconds())
		st["sendEpoch"] = s.SendEpoch()
	}
	writeJSON(w, localResp{OK: true, Data: st})
}
