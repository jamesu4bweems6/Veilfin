package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"veilfin/internal/api"
	"veilfin/internal/proto"
)

// sessionRefreshAge：服务端 24 小时后丢弃会话，客户端提前 1 小时主动换新。
const sessionRefreshAge = 23 * time.Hour

var errSessionGone = errors.New("会话已失效")

// RPCError 是服务端返回的业务错误。
type RPCError struct {
	Code string
	Msg  string
}

func (e *RPCError) Error() string { return e.Msg + " (" + e.Code + ")" }

// transport 负责握手和加密 RPC，对上层表现为普通的 Call(method, params)。
type transport struct {
	base string
	hc   *http.Client
	pub  ed25519.PublicKey

	hsMu sync.Mutex // 串行化握手

	mu         sync.Mutex
	sess       *proto.Session
	clockOff   time.Duration
	loginToken string
	authed     bool

	tokMu    sync.Mutex
	blobTok  string
	blobExp  time.Time
	tokEpoch int // 每次登录状态变化 +1，用来丢弃旧令牌
}

func (t *transport) current() *proto.Session {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sess != nil && time.Since(t.sess.Created) < sessionRefreshAge {
		return t.sess
	}
	return nil
}

func (t *transport) session(ctx context.Context) (*proto.Session, error) {
	if s := t.current(); s != nil {
		return s, nil
	}
	return t.rehandshake(ctx, nil)
}

// rehandshake 重新握手。old 是调用方认为已失效的会话，如果其他协程已经换过了就直接复用。
func (t *transport) rehandshake(ctx context.Context, old *proto.Session) (*proto.Session, error) {
	t.hsMu.Lock()
	defer t.hsMu.Unlock()
	if s := t.current(); s != nil && s != old {
		return s, nil
	}

	hs, err := proto.NewClientHandshake()
	if err != nil {
		return nil, err
	}
	resp, status, err := t.post(ctx, "/h", hs.Hello())
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("握手失败：HTTP %d", status)
	}
	sess, err := hs.Finish(resp, t.pub)
	if err != nil {
		return nil, errors.New("握手失败：服务端签名校验不通过，可能存在中间人或服务端密钥已更换")
	}

	t.mu.Lock()
	t.sess = sess
	t.clockOff = time.Until(sess.ServerTime)
	t.authed = false
	tok := t.loginToken
	t.mu.Unlock()
	t.resetBlobToken()

	// 用“记住登录”令牌自动恢复登录状态
	if tok != "" {
		_, err := t.callOn(ctx, sess, "auth.resume", api.TokenParams{Token: tok})
		var re *RPCError
		switch {
		case err == nil:
			t.setAuth(tok, true)
		case errors.As(err, &re):
			t.setAuth("", false)
		default:
			return nil, err
		}
	}
	return sess, nil
}

func (t *transport) setAuth(tok string, authed bool) {
	t.mu.Lock()
	t.loginToken, t.authed = tok, authed
	t.mu.Unlock()
	t.resetBlobToken()
}

func (t *transport) token() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.loginToken
}

func (t *transport) isAuthed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.authed
}

func (t *transport) resetBlobToken() {
	t.tokMu.Lock()
	t.blobTok, t.blobExp = "", time.Time{}
	t.tokEpoch++
	t.tokMu.Unlock()
}

// blobToken 返回访问加密对象的令牌，快过期或 force 时重新获取。
func (t *transport) blobToken(ctx context.Context, force bool) (string, error) {
	t.tokMu.Lock()
	tok, exp, epoch := t.blobTok, t.blobExp, t.tokEpoch
	t.tokMu.Unlock()
	if !force && tok != "" && time.Until(exp) > 5*time.Minute {
		return tok, nil
	}
	raw, err := t.Call(ctx, "auth.blobToken", nil)
	if err != nil {
		return "", err
	}
	var bt api.BlobToken
	if err := json.Unmarshal(raw, &bt); err != nil {
		return "", err
	}
	t.tokMu.Lock()
	if t.tokEpoch == epoch {
		t.blobTok, t.blobExp = bt.Token, time.Unix(bt.Exp, 0)
	}
	t.tokMu.Unlock()
	return bt.Token, nil
}

// Call 调用一个 RPC 方法。会话失效时自动重新握手并重试一次。
func (t *transport) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s, err := t.session(ctx)
	if err != nil {
		return nil, err
	}
	res, err := t.callOn(ctx, s, method, params)
	if errors.Is(err, errSessionGone) {
		if s, err = t.rehandshake(ctx, s); err != nil {
			return nil, err
		}
		res, err = t.callOn(ctx, s, method, params)
	}
	return res, err
}

func (t *transport) callOn(ctx context.Context, s *proto.Session, method string, params any) (json.RawMessage, error) {
	var p json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		p = b
	}
	ridb := make([]byte, 16)
	rand.Read(ridb)
	rid := hex.EncodeToString(ridb)

	t.mu.Lock()
	off := t.clockOff
	t.mu.Unlock()
	pt, err := json.Marshal(api.Request{TS: time.Now().Add(off).UnixMilli(), RID: rid, M: method, P: p})
	if err != nil {
		return nil, err
	}
	msg, err := s.Seal(pt)
	if err != nil {
		return nil, err
	}
	body, status, err := t.post(ctx, "/x", msg)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, errSessionGone
	default:
		return nil, fmt.Errorf("服务端返回 HTTP %d", status)
	}
	plain, err := s.Open(body)
	if err != nil {
		return nil, errors.New("响应解密失败，可能被篡改")
	}
	var resp api.Response
	if err := json.Unmarshal(plain, &resp); err != nil {
		return nil, err
	}
	if resp.RID != rid {
		return nil, errors.New("响应与请求不匹配")
	}
	if !resp.OK {
		if resp.Code == api.CodeUnauthorized && method != "auth.login" && method != "auth.unlock" &&
			method != "auth.changePassword" && method != "auth.setPin" && method != "setup.create" {
			t.mu.Lock()
			t.authed = false
			t.mu.Unlock()
		}
		return nil, &RPCError{Code: resp.Code, Msg: resp.Err}
	}
	return resp.Data, nil
}

func (t *transport) post(ctx context.Context, path string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := t.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, proto.MaxMessageSize+1))
	return b, resp.StatusCode, err
}
