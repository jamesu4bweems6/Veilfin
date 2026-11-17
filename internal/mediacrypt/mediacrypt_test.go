package mediacrypt

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"
	"time"
)

func newTestKeys(t testing.TB) *Keys {
	t.Helper()
	master := make([]byte, 32)
	rand.Read(master)
	k, err := NewKeys(master)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func newTestCipher(t testing.TB, size int64) (*Cipher, *Cipher, []byte) {
	t.Helper()
	k := newTestKeys(t)
	id := NewID()
	enc, nk := k.BlobKeys(id)
	srv, err := NewCipher(enc, nk, id, size)
	if err != nil {
		t.Fatal(err)
	}
	cli, _ := NewCipher(enc, nil, id, size)
	plain := make([]byte, size)
	rand.Read(plain)
	return srv, cli, plain
}

func encryptAll(t testing.TB, c *Cipher, plain []byte) []byte {
	t.Helper()
	ct, err := io.ReadAll(io.NewSectionReader(NewEncryptor(c, bytes.NewReader(plain)), 0, c.CipherSize()))
	if err != nil {
		t.Fatal(err)
	}
	return ct
}

func TestRoundTrip(t *testing.T) {
	for _, size := range []int64{0, 1, ChunkSize - 1, ChunkSize, ChunkSize + 1, 3*ChunkSize + 777} {
		srv, cli, plain := newTestCipher(t, size)
		ct := encryptAll(t, srv, plain)
		if int64(len(ct)) != CipherSize(size) {
			t.Fatalf("size=%d: 密文长度 %d", size, len(ct))
		}
		got, err := OpenAll(cli, ct)
		if err != nil {
			t.Fatalf("size=%d: %v", size, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size=%d: 明文不一致", size)
		}
		ct2, err := SealAll(srv, plain)
		if err != nil || !bytes.Equal(ct, ct2) {
			t.Fatalf("size=%d: SealAll 与 Encryptor 结果不一致", size)
		}
	}
}

func TestClientCannotEncrypt(t *testing.T) {
	_, cli, plain := newTestCipher(t, 10)
	if _, err := cli.SealChunk(nil, 0, plain); err == nil {
		t.Fatal("只有解密密钥的一方不应能加密")
	}
}

func TestRandomReads(t *testing.T) {
	srv, _, plain := newTestCipher(t, 3*ChunkSize+12345)
	full := encryptAll(t, srv, plain)
	enc := NewEncryptor(srv, bytes.NewReader(plain))
	for _, r := range [][2]int64{{0, 10}, {CipherChunkSize - 5, 10}, {2*CipherChunkSize + 100, 4000}, {srv.CipherSize() - 3, 3}, {5, CipherChunkSize * 2}} {
		buf := make([]byte, r[1])
		n, err := enc.ReadAt(buf, r[0])
		if err != nil && err != io.EOF {
			t.Fatal(err)
		}
		if !bytes.Equal(buf[:n], full[r[0]:r[0]+int64(n)]) {
			t.Fatalf("区间 %v 内容不一致", r)
		}
	}
	if _, err := enc.ReadAt(make([]byte, 10), srv.CipherSize()-3); err != io.EOF {
		t.Fatalf("读过末尾应返回 EOF，实际 %v", err)
	}
}

func TestDeterministicAndContentBoundNonce(t *testing.T) {
	srv, _, plain := newTestCipher(t, 2*ChunkSize+5)
	a := encryptAll(t, srv, plain)
	b := encryptAll(t, srv, plain)
	if !bytes.Equal(a, b) {
		t.Fatal("同一内容的密文应确定（CDN 才能缓存）")
	}
	// 原地修改内容（大小不变）：被修改块的 nonce 必须变化，未修改块保持不变
	mod := append([]byte(nil), plain...)
	mod[ChunkSize+10] ^= 1
	c := encryptAll(t, srv, mod)
	if !bytes.Equal(a[:NonceSize], c[:NonceSize]) {
		t.Fatal("未修改的块 nonce 不应变化")
	}
	if bytes.Equal(a[CipherChunkSize:CipherChunkSize+NonceSize], c[CipherChunkSize:CipherChunkSize+NonceSize]) {
		t.Fatal("内容变化后 nonce 必须变化，否则会造成 nonce 重用")
	}
}

func TestTamperSwapTruncate(t *testing.T) {
	srv, cli, plain := newTestCipher(t, 3*ChunkSize)
	ct := encryptAll(t, srv, plain)

	for _, pos := range []int64{0, 5, CipherChunkSize + 10, int64(len(ct)) - 1} {
		bad := append([]byte(nil), ct...)
		bad[pos] ^= 1
		if _, err := OpenAll(cli, bad); err == nil {
			t.Fatalf("篡改第 %d 字节后仍解密成功", pos)
		}
	}
	off1, l := cli.CipherRange(1, 1)
	if _, err := cli.OpenChunk(nil, 0, ct[off1:off1+l]); err == nil {
		t.Fatal("块被调换位置后仍解密成功")
	}
	// 攻击者把 3 块的文件截成 2 块
	k := newTestKeys(t)
	id := NewID()
	enc, nk := k.BlobKeys(id)
	long, _ := NewCipher(enc, nk, id, 3*ChunkSize)
	ct3 := encryptAll(t, long, plain)
	short, _ := NewCipher(enc, nil, id, 2*ChunkSize)
	off, l2 := short.CipherRange(1, 1)
	if _, err := short.OpenChunk(nil, 1, ct3[off:off+l2]); err == nil {
		t.Fatal("截断未被发现")
	}
	// 不同对象之间不能互换
	other, _ := NewCipher(enc, nil, NewID(), 3*ChunkSize)
	if _, err := OpenAll(other, ct3); err == nil {
		t.Fatal("对象 ID 未绑定到密文")
	}
}

func TestUserToken(t *testing.T) {
	k := newTestKeys(t)
	now := time.Now()
	tok := k.UserToken(42, 7, TokenUnlocked, now.Add(time.Hour))
	uid, gen, flags, ok := k.ParseToken(tok, now)
	if !ok || uid != 42 || gen != 7 || flags != TokenUnlocked {
		t.Fatalf("令牌解析错误: %d %d %d %v", uid, gen, flags, ok)
	}
	if _, _, _, ok := k.ParseToken(tok, now.Add(2*time.Hour)); ok {
		t.Fatal("过期令牌被接受")
	}
	raw := []byte(tok)
	raw[3] ^= 1
	if _, _, _, ok := k.ParseToken(string(raw), now); ok {
		t.Fatal("篡改令牌被接受")
	}
	if _, _, _, ok := newTestKeys(t).ParseToken(tok, now); ok {
		t.Fatal("其他服务端的令牌被接受")
	}
}

func BenchmarkEncrypt(b *testing.B) {
	srv, _, plain := newTestCipher(b, 32*ChunkSize)
	b.SetBytes(int64(len(plain)))
	for b.Loop() {
		if _, err := io.Copy(io.Discard, io.NewSectionReader(NewEncryptor(srv, bytes.NewReader(plain)), 0, srv.CipherSize())); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecrypt(b *testing.B) {
	srv, cli, plain := newTestCipher(b, 32*ChunkSize)
	ct := encryptAll(b, srv, plain)
	b.SetBytes(int64(len(plain)))
	for b.Loop() {
		if _, err := OpenAll(cli, ct); err != nil {
			b.Fatal(err)
		}
	}
}
