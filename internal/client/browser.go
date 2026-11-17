package client

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

// 每个浏览器一个独立会话。
//
// 本地客户端可能被多台设备访问（局域网、反向代理），所以登录状态不能放在客户端全局：
// 每个浏览器通过 HttpOnly Cookie 对应自己的加密通道、登录令牌、对象密钥表和缓存命名空间。
// 谁没登录，谁就什么都看不到；一个人登录不会让别人也处于登录状态。

const (
	sidCookie      = "vf_sid"
	browserIdleTTL = 30 * 24 * time.Hour
	maxBrowsers    = 2000
)

type browser struct {
	id string
	tr *transport

	blobMu sync.Mutex
	blobs  map[string]*blobInfo

	mu       sync.Mutex
	lastSeen time.Time
}

func (b *browser) touch() {
	b.mu.Lock()
	b.lastSeen = time.Now()
	b.mu.Unlock()
}

func (b *browser) seen() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastSeen
}

// lookupBrowser 根据 Cookie 找到浏览器会话，没有则返回 nil。
func (c *Client) lookupBrowser(r *http.Request) *browser {
	ck, err := r.Cookie(sidCookie)
	if err != nil || len(ck.Value) != 64 {
		return nil
	}
	c.bmu.Lock()
	b := c.browsers[ck.Value]
	c.bmu.Unlock()
	if b == nil || time.Since(b.seen()) > browserIdleTTL {
		return nil
	}
	b.touch()
	return b
}

// browserFor 返回请求对应的浏览器会话，没有就新建一个并种下 Cookie。
func (c *Client) browserFor(w http.ResponseWriter, r *http.Request) *browser {
	if b := c.lookupBrowser(r); b != nil {
		return b
	}
	raw := make([]byte, 32)
	rand.Read(raw)
	b := &browser{
		id:       hex.EncodeToString(raw),
		tr:       &transport{base: c.base, hc: c.hc, pub: c.pub},
		blobs:    map[string]*blobInfo{},
		lastSeen: time.Now(),
	}
	c.bmu.Lock()
	if len(c.browsers) >= maxBrowsers {
		c.evictLocked()
	}
	c.browsers[b.id] = b
	c.bmu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name: sidCookie, Value: b.id, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
		// 经 HTTPS 反向代理访问时加上 Secure
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		MaxAge: int(browserIdleTTL.Seconds()),
	})
	return b
}

// evictLocked 淘汰最久未活动的会话（优先淘汰未登录的）。调用方持有 c.bmu。
func (c *Client) evictLocked() {
	var victim string
	var oldest time.Time
	victimAuthed := true
	for id, b := range c.browsers {
		ls, authed := b.seen(), b.tr.isAuthed()
		if time.Since(ls) > browserIdleTTL {
			delete(c.browsers, id)
			continue
		}
		if victim == "" || (victimAuthed && !authed) || (authed == victimAuthed && ls.Before(oldest)) {
			victim, oldest, victimAuthed = id, ls, authed
		}
	}
	if len(c.browsers) >= maxBrowsers && victim != "" {
		delete(c.browsers, victim)
	}
}
