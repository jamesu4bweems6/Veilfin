// Package api 定义加密通道内传输的 RPC 消息结构，服务端和客户端共用。
package api

import "encoding/json"

// Request 是加密信封里的请求明文。
type Request struct {
	TS  int64           `json:"ts"`  // 发送时间（毫秒，按服务端时钟校正）
	RID string          `json:"rid"` // 随机请求 ID，用于防重放
	M   string          `json:"m"`   // 方法名
	P   json.RawMessage `json:"p,omitempty"`
}

// Response 是加密信封里的响应明文。
type Response struct {
	RID  string          `json:"rid"`
	OK   bool            `json:"ok"`
	Data json.RawMessage `json:"data,omitempty"`
	Code string          `json:"code,omitempty"`
	Err  string          `json:"err,omitempty"`
}

// 错误码
const (
	CodeBadRequest   = "bad_request"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeLocked       = "locked" // 受限内容需要先输入 PIN
	CodeNotFound     = "not_found"
	CodeReplay       = "replay"
	CodeClockSkew    = "clock_skew"
	CodeTooMany      = "too_many"
	CodeInternal     = "internal"
	CodeNoMethod     = "no_method"
	CodeSetup        = "needs_setup"
)

// BlobRef 是响应中对加密对象（视频、图片、字幕、预览图）的引用。
// 客户端会把它替换成本地可访问的 URL（/b/{id}），并在内部记住解密密钥。
//
//	{"$b": "对象ID", "k": "解密密钥(base64url)", "s": 明文大小, "t": "MIME 类型"}
type BlobRef struct {
	ID   string `json:"$b"`
	Key  string `json:"k"`
	Size int64  `json:"s"`
	Mime string `json:"t"`
}

type LoginResult struct {
	Token string          `json:"token"`
	Me    json.RawMessage `json:"me"`
}

type TokenParams struct {
	Token string `json:"token"`
}

// BlobToken 是访问加密对象时放在 X-VF-T 请求头里的短期令牌。
type BlobToken struct {
	Token string `json:"token"`
	Exp   int64  `json:"exp"`
}

// TokenHeader 是携带对象访问令牌的请求头。
const TokenHeader = "X-VF-T"
