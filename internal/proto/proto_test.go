package proto

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func handshakePair(t *testing.T) (*Session, *Session, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hs, err := NewClientHandshake()
	if err != nil {
		t.Fatal(err)
	}
	if len(hs.Hello()) != ClientHelloSize {
		t.Fatalf("ClientHello 长度 %d", len(hs.Hello()))
	}
	resp, srv, err := ServerRespond(hs.Hello(), priv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(resp) != ServerHelloSize {
		t.Fatalf("ServerHello 长度 %d", len(resp))
	}
	cli, err := hs.Finish(resp, pub)
	if err != nil {
		t.Fatal(err)
	}
	return cli, srv, pub
}

func TestRoundTrip(t *testing.T) {
	cli, srv, _ := handshakePair(t)
	for _, n := range []int{0, 1, 1019, 1020, 1021, 5000, 100000} {
		msg := make([]byte, n)
		rand.Read(msg)
		ct, err := cli.Seal(msg)
		if err != nil {
			t.Fatal(err)
		}
		if (len(ct)-headerSize-2*tagSize-xNonceSize)%PadBlock != 0 {
			t.Fatalf("密文长度 %d 未按块对齐", len(ct))
		}
		pt, err := srv.Open(ct)
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if !bytes.Equal(pt, msg) {
			t.Fatalf("n=%d: 明文不一致", n)
		}
		// 反方向
		ct2, _ := srv.Seal(msg)
		pt2, err := cli.Open(ct2)
		if err != nil || !bytes.Equal(pt2, msg) {
			t.Fatalf("n=%d: 反方向失败 %v", n, err)
		}
	}
}

func TestPaddingHidesLength(t *testing.T) {
	cli, _, _ := handshakePair(t)
	a, _ := cli.Seal([]byte("a"))
	b, _ := cli.Seal(bytes.Repeat([]byte("b"), 900))
	if len(a) != len(b) {
		t.Fatalf("同一填充块内的消息长度应相同：%d vs %d", len(a), len(b))
	}
}

func TestWrongServerKeyRejected(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	hs, _ := NewClientHandshake()
	resp, _, err := ServerRespond(hs.Hello(), priv, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hs.Finish(resp, otherPub); err == nil {
		t.Fatal("用错误的服务端公钥应当握手失败（防中间人）")
	}
}

func TestTamperedServerHelloRejected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	hs, _ := NewClientHandshake()
	resp, _, _ := ServerRespond(hs.Hello(), priv, time.Now())
	for _, i := range []int{0, 100, ServerHelloSize - 70, ServerHelloSize - 1} {
		bad := append([]byte(nil), resp...)
		bad[i] ^= 1
		if _, err := hs.Finish(bad, pub); err == nil {
			t.Fatalf("篡改第 %d 字节后仍握手成功", i)
		}
	}
}

func TestTamperRejected(t *testing.T) {
	cli, srv, _ := handshakePair(t)
	ct, _ := cli.Seal([]byte("hello"))
	for _, i := range []int{0, 5, 20, headerSize - 1, headerSize, len(ct) / 2, len(ct) - 1} {
		bad := append([]byte(nil), ct...)
		bad[i] ^= 0x80
		if _, err := srv.Open(bad); err == nil {
			t.Fatalf("篡改第 %d 字节后仍解密成功", i)
		}
	}
	if _, err := srv.Open(ct[:len(ct)-1]); err == nil {
		t.Fatal("截断消息后仍解密成功")
	}
}

func TestReflectionRejected(t *testing.T) {
	cli, _, _ := handshakePair(t)
	ct, _ := cli.Seal([]byte("hello"))
	// 两个方向的密钥不同，把客户端发出的消息原样反射回客户端应当失败
	if _, err := cli.Open(ct); err == nil {
		t.Fatal("反射消息被接受")
	}
}

func TestRatchet(t *testing.T) {
	cli, srv, _ := handshakePair(t)
	cli.SetRekeyPolicy(3, time.Hour)

	var msgs [][]byte
	for i := 0; i < 10; i++ {
		ct, err := cli.Seal([]byte{byte(i)})
		if err != nil {
			t.Fatal(err)
		}
		msgs = append(msgs, ct)
	}
	if e := cli.SendEpoch(); e != 3 {
		t.Fatalf("10 条消息、每 3 条换代，期望 epoch=3，实际 %d", e)
	}
	// 先收到 epoch 1 的消息（跳过 epoch 0 的部分消息）
	if _, err := srv.Open(msgs[4]); err != nil {
		t.Fatalf("前进到 epoch 1 失败: %v", err)
	}
	// epoch 0 是上一代，迟到的消息仍可接受
	if _, err := srv.Open(msgs[0]); err != nil {
		t.Fatalf("上一代消息应可接受: %v", err)
	}
	// 推进到 epoch 3 后，epoch 1 已经超过保留范围，必须拒绝（前向安全）
	if _, err := srv.Open(msgs[9]); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Open(msgs[5]); err == nil {
		t.Fatal("过期两代的密钥仍能解密")
	}
	if _, err := srv.Open(msgs[8]); err != nil {
		t.Fatalf("epoch 2（上一代）应可接受: %v", err)
	}
}

func TestForgedEpochDoesNotAdvance(t *testing.T) {
	cli, srv, _ := handshakePair(t)
	ct, _ := cli.Seal([]byte("x"))
	forged := append([]byte(nil), ct...)
	forged[1+SIDSize+3] = 50 // 把 epoch 改成 50
	if _, err := srv.Open(forged); err == nil {
		t.Fatal("伪造的 epoch 被接受")
	}
	// 伪造消息不应推进接收状态，正常消息仍能解密
	if _, err := srv.Open(ct); err != nil {
		t.Fatalf("伪造消息破坏了接收状态: %v", err)
	}
}

func TestFingerprint(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	fp := Fingerprint(pub)
	if len(fp) != 39 {
		t.Fatalf("指纹格式异常: %s", fp)
	}
}

func BenchmarkHandshake(b *testing.B) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	pub := priv.Public().(ed25519.PublicKey)
	for b.Loop() {
		hs, _ := NewClientHandshake()
		resp, _, _ := ServerRespond(hs.Hello(), priv, time.Now())
		if _, err := hs.Finish(resp, pub); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSealOpen1K(b *testing.B) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	hs, _ := NewClientHandshake()
	resp, srv, _ := ServerRespond(hs.Hello(), priv, time.Now())
	cli, _ := hs.Finish(resp, pub)
	msg := make([]byte, 800)
	for b.Loop() {
		ct, _ := cli.Seal(msg)
		if _, err := srv.Open(ct); err != nil {
			b.Fatal(err)
		}
	}
}
