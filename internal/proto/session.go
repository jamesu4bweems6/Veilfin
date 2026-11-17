package proto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// DefaultRekeyMessages 表示每个方向发送多少条消息后推进一次棘轮。
	DefaultRekeyMessages = 500
	// DefaultRekeyInterval 表示每个方向最长多久推进一次棘轮。
	DefaultRekeyInterval = 5 * time.Minute
	// MaxEpochSkip 表示接收方一次最多向前追赶多少代密钥，防止被恶意的大 epoch 拖慢。
	// 正常情况下每 500 条消息或 5 分钟才换一代，相邻请求之间最多差一两代。
	MaxEpochSkip = 8

	// HeaderSize 是消息头长度：版本(1) ‖ SID(16) ‖ epoch(4) ‖ 外层 nonce(12)
	HeaderSize  = 1 + SIDSize + 4 + 12
	headerSize  = HeaderSize
	xNonceSize  = chacha20poly1305.NonceSizeX
	tagSize     = 16
	minMsgSize  = headerSize + tagSize + xNonceSize + tagSize + PadBlock
	lenFieldLen = 4

	// PadBlock 表示明文按多少字节对齐填充，用来隐藏消息的真实长度。
	PadBlock = 1024
	// MaxPlaintext 为单条消息明文的上限。
	MaxPlaintext = 8 << 20
	// MaxMessageSize 为单条密文消息的上限（含填充和各层开销）。
	MaxMessageSize = MaxPlaintext + PadBlock + headerSize + 2*tagSize + xNonceSize
)

var (
	ErrDecrypt  = errors.New("proto: 解密失败")
	ErrTooLarge = errors.New("proto: 消息过大")
	ErrStale    = errors.New("proto: 密钥已过期")
)

// Session 是握手后建立的双向加密通道。Seal / Open 可以并发调用。
type Session struct {
	SID []byte
	// Created 是本地建立会话的时间。
	Created time.Time
	// ServerTime 是握手时服务端的时钟，客户端可以用它估算时钟偏差。
	ServerTime time.Time

	send *sendState
	recv *recvState
}

// SetRekeyPolicy 调整发送方向的棘轮推进策略（主要用于测试）。
func (s *Session) SetRekeyPolicy(messages int, interval time.Duration) {
	s.send.mu.Lock()
	s.send.maxMsgs, s.send.maxAge = messages, interval
	s.send.mu.Unlock()
}

// SendEpoch 返回当前发送方向的密钥代数。
func (s *Session) SendEpoch() uint32 {
	s.send.mu.Lock()
	defer s.send.mu.Unlock()
	return s.send.epoch
}

// ---- 棘轮 ----

type epochKeys struct {
	outer cipher.AEAD // AES-256-GCM
	inner cipher.AEAD // XChaCha20-Poly1305
}

// deriveEpoch 由当前链密钥派生本代的两把消息密钥，以及下一代的链密钥。
// 链密钥只能单向推进，拿到后面的密钥推不出前面的，从而实现前向安全。
func deriveEpoch(chain []byte) (epochKeys, []byte, error) {
	ok, err := hkdf.Expand(sha256.New, chain, "msg/outer", 32)
	if err != nil {
		return epochKeys{}, nil, err
	}
	ik, err := hkdf.Expand(sha256.New, chain, "msg/inner", 32)
	if err != nil {
		return epochKeys{}, nil, err
	}
	next, err := hkdf.Expand(sha256.New, chain, "ratchet", 32)
	if err != nil {
		return epochKeys{}, nil, err
	}
	defer clear(ok)
	defer clear(ik)

	block, err := aes.NewCipher(ok)
	if err != nil {
		return epochKeys{}, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return epochKeys{}, nil, err
	}
	x, err := chacha20poly1305.NewX(ik)
	if err != nil {
		return epochKeys{}, nil, err
	}
	return epochKeys{outer: gcm, inner: x}, next, nil
}

type sendState struct {
	mu      sync.Mutex
	epoch   uint32
	keys    epochKeys
	chain   []byte // 下一代的链密钥
	count   int
	since   time.Time
	maxMsgs int
	maxAge  time.Duration
}

func newSendState(chain []byte, now time.Time) (*sendState, error) {
	k, next, err := deriveEpoch(chain)
	clear(chain)
	if err != nil {
		return nil, err
	}
	return &sendState{keys: k, chain: next, since: now,
		maxMsgs: DefaultRekeyMessages, maxAge: DefaultRekeyInterval}, nil
}

func (s *sendState) next(now time.Time) (uint32, epochKeys, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.count >= s.maxMsgs || now.Sub(s.since) >= s.maxAge {
		k, next, err := deriveEpoch(s.chain)
		if err != nil {
			return 0, epochKeys{}, err
		}
		clear(s.chain)
		s.chain, s.keys = next, k
		s.epoch++
		s.count = 0
		s.since = now
	}
	s.count++
	return s.epoch, s.keys, nil
}

type recvState struct {
	mu      sync.Mutex
	latest  uint32
	cur     epochKeys
	prev    epochKeys // 上一代，用来容纳并发请求的乱序到达
	hasPrev bool
	chain   []byte // latest+1 代的链密钥
}

func newRecvState(chain []byte) (*recvState, error) {
	k, next, err := deriveEpoch(chain)
	clear(chain)
	if err != nil {
		return nil, err
	}
	return &recvState{cur: k, chain: next}, nil
}

// open 用 epoch 对应的密钥执行 fn。epoch 本身没有经过认证，
// 所以向前推进的密钥只有在 fn 解密成功后才提交，防止伪造的大 epoch 把状态推乱。
func (r *recvState) open(e uint32, fn func(epochKeys) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case e == r.latest:
		return fn(r.cur)
	case e+1 == r.latest && r.hasPrev:
		return fn(r.prev)
	case e > r.latest:
		if e-r.latest > MaxEpochSkip {
			return ErrStale
		}
		chain := append([]byte(nil), r.chain...)
		prev, cur := r.cur, r.cur
		for i := r.latest; i < e; i++ {
			k, next, err := deriveEpoch(chain)
			clear(chain)
			if err != nil {
				return err
			}
			prev, cur, chain = cur, k, next
		}
		if err := fn(cur); err != nil {
			clear(chain)
			return err
		}
		clear(r.chain)
		r.latest, r.cur, r.prev, r.hasPrev, r.chain = e, cur, prev, true, chain
		return nil
	default:
		return ErrStale
	}
}

// ---- 消息封装 ----

// Seal 加密一条消息：明文 → 填充 → XChaCha20-Poly1305 → AES-256-GCM。
// 两层都以完整的消息头作为附加认证数据（AAD）。
func (s *Session) Seal(plaintext []byte) ([]byte, error) {
	if len(plaintext) > MaxPlaintext {
		return nil, ErrTooLarge
	}
	epoch, k, err := s.send.next(time.Now())
	if err != nil {
		return nil, err
	}
	padded := pad(plaintext)

	hdr := make([]byte, headerSize)
	hdr[0] = Version
	copy(hdr[1:], s.SID)
	binary.BigEndian.PutUint32(hdr[1+SIDSize:], epoch)
	nonce := hdr[1+SIDSize+4:]
	rand.Read(nonce)

	inner := make([]byte, xNonceSize, xNonceSize+len(padded)+tagSize)
	rand.Read(inner[:xNonceSize])
	inner = k.inner.Seal(inner, inner[:xNonceSize], padded, hdr)
	clear(padded)

	out := make([]byte, headerSize, headerSize+len(inner)+tagSize)
	copy(out, hdr)
	return k.outer.Seal(out, nonce, inner, hdr), nil
}

// Open 解密由对端 Seal 的消息。
func (s *Session) Open(msg []byte) ([]byte, error) {
	if len(msg) < minMsgSize || len(msg) > MaxMessageSize || msg[0] != Version {
		return nil, ErrDecrypt
	}
	if !bytes.Equal(msg[1:1+SIDSize], s.SID) {
		return nil, ErrDecrypt
	}
	hdr := msg[:headerSize]
	epoch := binary.BigEndian.Uint32(msg[1+SIDSize:])
	nonce := msg[1+SIDSize+4 : headerSize]

	var out []byte
	err := s.recv.open(epoch, func(k epochKeys) error {
		inner, err := k.outer.Open(nil, nonce, msg[headerSize:], hdr)
		if err != nil || len(inner) < xNonceSize+tagSize {
			return ErrDecrypt
		}
		padded, err := k.inner.Open(nil, inner[:xNonceSize], inner[xNonceSize:], hdr)
		if err != nil {
			return ErrDecrypt
		}
		out, err = unpad(padded)
		return err
	})
	if err != nil {
		return nil, ErrDecrypt
	}
	return out, nil
}

// PeekSID 从消息头中取出会话 ID（不做任何校验），供服务端查找会话。
func PeekSID(msg []byte) ([]byte, bool) {
	if len(msg) < headerSize || msg[0] != Version {
		return nil, false
	}
	return msg[1 : 1+SIDSize], true
}

// pad：长度(4) ‖ 明文 ‖ 0…，总长补齐到 PadBlock 的整数倍。
func pad(p []byte) []byte {
	n := lenFieldLen + len(p)
	size := (n + PadBlock - 1) / PadBlock * PadBlock
	buf := make([]byte, size)
	binary.BigEndian.PutUint32(buf, uint32(len(p)))
	copy(buf[lenFieldLen:], p)
	return buf
}

func unpad(b []byte) ([]byte, error) {
	if len(b) < lenFieldLen || len(b)%PadBlock != 0 {
		return nil, ErrDecrypt
	}
	n := int(binary.BigEndian.Uint32(b))
	if n > len(b)-lenFieldLen {
		return nil, ErrDecrypt
	}
	return b[lenFieldLen : lenFieldLen+n], nil
}
