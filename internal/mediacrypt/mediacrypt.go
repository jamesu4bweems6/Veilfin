// Package mediacrypt 实现“对象”（视频、图片、字幕、预览图）的分块加密。
//
// 文件按 1 MiB 切块，每块独立用 AES-256-GCM 加密，密文块格式为：
//
//	nonce(12) ‖ AES-256-GCM(encKey, nonce, 明文块, AAD)
//	nonce = HMAC-SHA256(nonceKey, BE64(块序号) ‖ 明文块)[:12]
//	AAD   = "VFM2" ‖ 对象ID ‖ BE64(块序号) ‖ 是否最后一块(1B)
//
// nonce 由明文内容派生（合成 IV，思路同 SIV），因此：
//   - 同一内容的密文是确定的，CDN 可以缓存；
//   - 内容哪怕被原地修改（大小、修改时间都不变），nonce 也会随之改变，不会出现 nonce 重用。
//
// 只有服务端持有 nonceKey；客户端只拿到 encKey，用于解密和校验。
package mediacrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash"
	"io"
	"sync"
	"time"
)

const (
	ChunkSize       = 1 << 20
	NonceSize       = 12
	TagSize         = 16
	Overhead        = NonceSize + TagSize
	CipherChunkSize = ChunkSize + Overhead
	KeySize         = 32
	tokenMACSize    = 16
	tokenBody       = 8 + 1 + 8 + 4
)

var (
	ErrChunk = errors.New("mediacrypt: 数据块校验失败")
	ErrRange = errors.New("mediacrypt: 块序号越界")
)

// NumChunks 返回明文大小对应的块数。
func NumChunks(size int64) int64 { return (size + ChunkSize - 1) / ChunkSize }

// CipherSize 返回明文大小对应的密文总大小。
func CipherSize(size int64) int64 { return size + NumChunks(size)*Overhead }

// NewID 生成一个随机的不透明对象 ID（128 位）。
func NewID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Keys 由媒体主密钥派生出的各类子密钥（只在服务端使用）。
type Keys struct {
	master   []byte
	tokenKey []byte
}

// NewKeys 从 32 字节媒体主密钥派生子密钥。
func NewKeys(master []byte) (*Keys, error) {
	if len(master) != 32 {
		return nil, errors.New("mediacrypt: 主密钥必须是 32 字节")
	}
	tokenKey, err := hkdf.Expand(sha256.New, master, "media/token", 32)
	if err != nil {
		return nil, err
	}
	return &Keys{master: append([]byte(nil), master...), tokenKey: tokenKey}, nil
}

// BlobKeys 派生某个对象的加密密钥和 nonce 密钥。
func (k *Keys) BlobKeys(id string) (encKey, nonceKey []byte) {
	encKey, err := hkdf.Expand(sha256.New, k.master, "blob/enc/"+id, KeySize)
	if err != nil {
		panic(err) // 只在长度参数非法时出错，这里不可能发生
	}
	nonceKey, err = hkdf.Expand(sha256.New, k.master, "blob/nonce/"+id, KeySize)
	if err != nil {
		panic(err)
	}
	return encKey, nonceKey
}

// 访问令牌标志位
const (
	// TokenUnlocked 表示该会话已用 PIN 解锁受限内容。
	TokenUnlocked byte = 1 << 0
)

// UserToken 生成访问对象密文的短期令牌：
//
//	BE64(过期) ‖ 标志(1) ‖ BE64(用户ID) ‖ BE32(令牌代数) ‖ HMAC[:16]
//
// 令牌代数在用户锁定受限内容、修改 PIN / 密码、被禁用时递增，使已签发的令牌立即失效。
func (k *Keys) UserToken(uid int64, gen uint32, flags byte, exp time.Time) string {
	buf := make([]byte, tokenBody, tokenBody+tokenMACSize)
	binary.BigEndian.PutUint64(buf, uint64(exp.Unix()))
	buf[8] = flags
	binary.BigEndian.PutUint64(buf[9:], uint64(uid))
	binary.BigEndian.PutUint32(buf[17:], gen)
	buf = append(buf, k.tokenMAC(buf[:tokenBody])...)
	return base64.RawURLEncoding.EncodeToString(buf)
}

// ParseToken 校验令牌并返回用户 ID、令牌代数和标志位。
func (k *Keys) ParseToken(tok string, now time.Time) (uid int64, gen uint32, flags byte, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(raw) != tokenBody+tokenMACSize {
		return 0, 0, 0, false
	}
	if !hmac.Equal(raw[tokenBody:], k.tokenMAC(raw[:tokenBody])) {
		return 0, 0, 0, false
	}
	if now.Unix() > int64(binary.BigEndian.Uint64(raw[:8])) {
		return 0, 0, 0, false
	}
	return int64(binary.BigEndian.Uint64(raw[9:17])), binary.BigEndian.Uint32(raw[17:21]), raw[8], true
}

func (k *Keys) tokenMAC(b []byte) []byte {
	m := hmac.New(sha256.New, k.tokenKey)
	m.Write(b)
	return m.Sum(nil)[:tokenMACSize]
}

// Cipher 负责单个对象的分块加解密。
type Cipher struct {
	aead  cipher.AEAD
	id    string
	size  int64
	n     int64
	nmac  hash.Hash // 仅服务端（加密方）持有
	nmu   sync.Mutex
	nbuf  []byte
	nonce [NonceSize]byte
}

// NewCipher 创建 Cipher。nonceKey 为空时只能解密（客户端）。
func NewCipher(encKey, nonceKey []byte, id string, plainSize int64) (*Cipher, error) {
	if plainSize < 0 {
		return nil, errors.New("mediacrypt: 大小非法")
	}
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	c := &Cipher{aead: gcm, id: id, size: plainSize, n: NumChunks(plainSize)}
	if nonceKey != nil {
		c.nmac = hmac.New(sha256.New, nonceKey)
	}
	return c, nil
}

func (c *Cipher) PlainSize() int64  { return c.size }
func (c *Cipher) CipherSize() int64 { return CipherSize(c.size) }
func (c *Cipher) NumChunks() int64  { return c.n }

// PlainChunkLen 返回第 idx 块的明文长度。
func (c *Cipher) PlainChunkLen(idx int64) int {
	if idx < 0 || idx >= c.n {
		return 0
	}
	if rem := c.size - idx*ChunkSize; rem < ChunkSize {
		return int(rem)
	}
	return ChunkSize
}

// CipherRange 返回第 first 到 last 块（含两端）在密文中的偏移和长度。
func (c *Cipher) CipherRange(first, last int64) (off, length int64) {
	off = first * CipherChunkSize
	end := last*CipherChunkSize + int64(c.PlainChunkLen(last)) + Overhead
	return off, end - off
}

func (c *Cipher) aad(idx int64) []byte {
	aad := make([]byte, 0, 4+len(c.id)+9)
	aad = append(aad, "VFM2"...)
	aad = append(aad, c.id...)
	aad = binary.BigEndian.AppendUint64(aad, uint64(idx))
	if idx == c.n-1 {
		return append(aad, 1) // 标记最后一块，防止截断
	}
	return append(aad, 0)
}

// SealChunk 加密第 idx 块，结果（nonce ‖ 密文）追加到 dst 后返回。
func (c *Cipher) SealChunk(dst []byte, idx int64, plain []byte) ([]byte, error) {
	if c.nmac == nil {
		return nil, errors.New("mediacrypt: 没有 nonce 密钥，不能加密")
	}
	if idx < 0 || idx >= c.n || len(plain) != c.PlainChunkLen(idx) {
		return nil, ErrRange
	}
	c.nmu.Lock()
	c.nmac.Reset()
	var ib [8]byte
	binary.BigEndian.PutUint64(ib[:], uint64(idx))
	c.nmac.Write(ib[:])
	c.nmac.Write(plain)
	c.nbuf = c.nmac.Sum(c.nbuf[:0])
	copy(c.nonce[:], c.nbuf)
	nonce := c.nonce
	c.nmu.Unlock()

	dst = append(dst, nonce[:]...)
	return c.aead.Seal(dst, nonce[:], plain, c.aad(idx)), nil
}

// OpenChunk 解密并校验第 idx 块。
func (c *Cipher) OpenChunk(dst []byte, idx int64, ct []byte) ([]byte, error) {
	if idx < 0 || idx >= c.n || len(ct) != c.PlainChunkLen(idx)+Overhead {
		return nil, ErrRange
	}
	out, err := c.aead.Open(dst, ct[:NonceSize], ct[NonceSize:], c.aad(idx))
	if err != nil {
		return nil, ErrChunk
	}
	return out, nil
}

// Encryptor 把明文文件包装成密文视图，实现 io.ReaderAt（坐标为密文偏移）。
// 它会缓存最近加密的一块，顺序读取时每块只加密一次。
type Encryptor struct {
	c   *Cipher
	src io.ReaderAt

	mu       sync.Mutex
	cacheIdx int64
	cache    []byte
	plain    []byte
}

func NewEncryptor(c *Cipher, src io.ReaderAt) *Encryptor {
	return &Encryptor{c: c, src: src, cacheIdx: -1}
}

func (e *Encryptor) ReadAt(p []byte, off int64) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	total := e.c.CipherSize()
	if off < 0 {
		return 0, ErrRange
	}
	n := 0
	for len(p) > 0 && off < total {
		idx := off / CipherChunkSize
		ct, err := e.chunk(idx)
		if err != nil {
			return n, err
		}
		m := copy(p, ct[off-idx*CipherChunkSize:])
		n += m
		p = p[m:]
		off += int64(m)
	}
	if len(p) > 0 {
		return n, io.EOF
	}
	return n, nil
}

var plainPool = sync.Pool{New: func() any { b := make([]byte, ChunkSize); return &b }}

func (e *Encryptor) chunk(idx int64) ([]byte, error) {
	if idx == e.cacheIdx {
		return e.cache, nil
	}
	pl := e.c.PlainChunkLen(idx)
	bp := plainPool.Get().(*[]byte)
	defer plainPool.Put(bp)
	buf := (*bp)[:pl]
	n, err := e.src.ReadAt(buf, idx*ChunkSize)
	if n != pl {
		if err == nil || err == io.EOF {
			err = io.ErrUnexpectedEOF // 文件在扫描之后被截短了
		}
		return nil, err
	}
	if cap(e.cache) < CipherChunkSize {
		e.cache = make([]byte, 0, CipherChunkSize)
	}
	ct, err := e.c.SealChunk(e.cache[:0], idx, buf)
	if err != nil {
		return nil, err
	}
	e.cache, e.cacheIdx = ct, idx
	return ct, nil
}

// SealAll 一次性加密整个小对象（图片、字幕等），返回完整密文。
func SealAll(c *Cipher, plain []byte) ([]byte, error) {
	out := make([]byte, 0, c.CipherSize())
	for i := int64(0); i < c.n; i++ {
		start := i * ChunkSize
		var err error
		out, err = c.SealChunk(out, i, plain[start:start+int64(c.PlainChunkLen(i))])
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// OpenAll 解密整个小对象。
func OpenAll(c *Cipher, ct []byte) ([]byte, error) {
	if int64(len(ct)) != c.CipherSize() {
		return nil, ErrChunk
	}
	out := make([]byte, 0, c.size)
	for i := int64(0); i < c.n; i++ {
		off, l := c.CipherRange(i, i)
		var err error
		out, err = c.OpenChunk(out, i, ct[off:off+l])
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
