// Package proto 实现 Veilfin 的应用层加密协议：
//
//   - 握手：X25519 + ML-KEM-768 混合密钥交换（后量子），服务端用 Ed25519 身份密钥签名整个握手记录；
//   - 消息：XChaCha20-Poly1305（内层）套 AES-256-GCM（外层）双层加密，按 1 KiB 填充；
//   - 密钥轮换：每个方向独立的哈希棘轮，按消息数 / 时间推进，旧密钥用完即丢弃。
package proto

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/mlkem"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const (
	// Version 为协议版本号，出现在 ClientHello 和每条消息头的第一个字节。
	Version byte = 1
	// SIDSize 为会话 ID 长度。
	SIDSize = 16

	x25519Size = 32
	tsSize     = 8

	// ClientHelloSize = 版本(1) ‖ X25519 公钥(32) ‖ ML-KEM-768 封装公钥(1184)
	ClientHelloSize = 1 + x25519Size + mlkem.EncapsulationKeySize768
	// ServerHelloSize = X25519 公钥(32) ‖ ML-KEM 密文(1088) ‖ SID(16) ‖ 服务端时间(8) ‖ Ed25519 签名(64)
	ServerHelloSize = x25519Size + mlkem.CiphertextSize768 + SIDSize + tsSize + ed25519.SignatureSize

	hsLabel = "Veilfin/handshake/v1" // 网页端 proto.js 里有同样的值
)

// ErrHandshake 表示握手失败。为避免给攻击者提供信息，不区分具体原因。
var ErrHandshake = errors.New("proto: 握手失败")

// ClientHandshake 保存客户端握手过程中的临时私钥。
type ClientHandshake struct {
	x     *ecdh.PrivateKey
	dk    *mlkem.DecapsulationKey768
	hello []byte
}

// NewClientHandshake 生成临时 X25519 和 ML-KEM-768 密钥对，并构造 ClientHello。
func NewClientHandshake() (*ClientHandshake, error) {
	x, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	dk, err := mlkem.GenerateKey768()
	if err != nil {
		return nil, err
	}
	hello := make([]byte, 0, ClientHelloSize)
	hello = append(hello, Version)
	hello = append(hello, x.PublicKey().Bytes()...)
	hello = append(hello, dk.EncapsulationKey().Bytes()...)
	return &ClientHandshake{x: x, dk: dk, hello: hello}, nil
}

// Hello 返回要发送给服务端的 ClientHello。
func (c *ClientHandshake) Hello() []byte { return c.hello }

// Finish 校验 ServerHello 的签名并派生会话密钥。serverPub 必须是事先固定（pin）的服务端身份公钥。
func (c *ClientHandshake) Finish(serverHello []byte, serverPub ed25519.PublicKey) (*Session, error) {
	if len(serverHello) != ServerHelloSize || len(serverPub) != ed25519.PublicKeySize {
		return nil, ErrHandshake
	}
	p := serverHello
	sx, p := p[:x25519Size], p[x25519Size:]
	ct, p := p[:mlkem.CiphertextSize768], p[mlkem.CiphertextSize768:]
	sid, p := p[:SIDSize], p[SIDSize:]
	ts, sig := p[:tsSize], p[tsSize:]

	tr := transcript(c.hello, sx, ct, sid, ts)
	if !ed25519.Verify(serverPub, tr, sig) {
		return nil, ErrHandshake
	}
	pub, err := ecdh.X25519().NewPublicKey(sx)
	if err != nil {
		return nil, ErrHandshake
	}
	ssX, err := c.x.ECDH(pub) // 低阶点会在这里报错
	if err != nil {
		return nil, ErrHandshake
	}
	ssPQ, err := c.dk.Decapsulate(ct)
	if err != nil {
		return nil, ErrHandshake
	}
	s, err := newSession(sid, ssPQ, ssX, tr, true)
	if err != nil {
		return nil, err
	}
	s.ServerTime = time.UnixMilli(int64(binary.BigEndian.Uint64(ts)))
	return s, nil
}

// ServerRespond 处理 ClientHello，返回 ServerHello 和建立好的服务端会话。
func ServerRespond(clientHello []byte, id ed25519.PrivateKey, now time.Time) ([]byte, *Session, error) {
	if len(clientHello) != ClientHelloSize || clientHello[0] != Version {
		return nil, nil, ErrHandshake
	}
	cx := clientHello[1 : 1+x25519Size]
	ekBytes := clientHello[1+x25519Size:]

	cpub, err := ecdh.X25519().NewPublicKey(cx)
	if err != nil {
		return nil, nil, ErrHandshake
	}
	ek, err := mlkem.NewEncapsulationKey768(ekBytes)
	if err != nil {
		return nil, nil, ErrHandshake
	}
	s, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	ssX, err := s.ECDH(cpub)
	if err != nil {
		return nil, nil, ErrHandshake
	}
	ssPQ, ct := ek.Encapsulate()

	sid := make([]byte, SIDSize)
	rand.Read(sid)
	ts := make([]byte, tsSize)
	binary.BigEndian.PutUint64(ts, uint64(now.UnixMilli()))
	sx := s.PublicKey().Bytes()

	tr := transcript(clientHello, sx, ct, sid, ts)
	sig := ed25519.Sign(id, tr)

	resp := make([]byte, 0, ServerHelloSize)
	resp = append(resp, sx...)
	resp = append(resp, ct...)
	resp = append(resp, sid...)
	resp = append(resp, ts...)
	resp = append(resp, sig...)

	sess, err := newSession(sid, ssPQ, ssX, tr, false)
	if err != nil {
		return nil, nil, err
	}
	sess.ServerTime = now
	return resp, sess, nil
}

func transcript(clientHello, sx, ct, sid, ts []byte) []byte {
	tr := make([]byte, 0, len(hsLabel)+len(clientHello)+len(sx)+len(ct)+len(sid)+len(ts))
	tr = append(tr, hsLabel...)
	tr = append(tr, clientHello...)
	tr = append(tr, sx...)
	tr = append(tr, ct...)
	tr = append(tr, sid...)
	tr = append(tr, ts...)
	return tr
}

// newSession 把两份共享秘密合并（任意一个算法没被攻破，结果就安全），派生两个方向的初始链密钥。
func newSession(sid, ssPQ, ssX, tr []byte, isClient bool) (*Session, error) {
	th := sha256.Sum256(tr)
	ikm := make([]byte, 0, len(ssPQ)+len(ssX))
	ikm = append(ikm, ssPQ...)
	ikm = append(ikm, ssX...)
	defer clear(ikm)
	defer clear(ssPQ)
	defer clear(ssX)

	root, err := hkdf.Extract(sha256.New, ikm, th[:])
	if err != nil {
		return nil, err
	}
	defer clear(root)
	c2s, err := hkdf.Expand(sha256.New, root, "chain/c2s", 32)
	if err != nil {
		return nil, err
	}
	s2c, err := hkdf.Expand(sha256.New, root, "chain/s2c", 32)
	if err != nil {
		return nil, err
	}

	sendChain, recvChain := s2c, c2s
	if isClient {
		sendChain, recvChain = c2s, s2c
	}
	now := time.Now()
	send, err := newSendState(sendChain, now)
	if err != nil {
		return nil, err
	}
	recv, err := newRecvState(recvChain)
	if err != nil {
		return nil, err
	}
	return &Session{
		SID:     append([]byte(nil), sid...),
		Created: now,
		send:    send,
		recv:    recv,
	}, nil
}

// Fingerprint 返回服务端身份公钥的指纹，便于人工比对。
func Fingerprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:16])
	parts := make([]string, 0, 8)
	for i := 0; i < len(h); i += 4 {
		parts = append(parts, h[i:i+4])
	}
	return strings.ToUpper(strings.Join(parts, "-"))
}
