// Package auth 提供密码和 PIN 的哈希（Argon2id）。
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id 参数：64 MiB 内存、2 轮、2 线程，单次约几十毫秒。
const (
	memKiB  = 64 * 1024
	iters   = 2
	threads = 2
	keyLen  = 32
	saltLen = 16
)

var ErrMismatch = errors.New("密码不正确")

// Hash 返回 PHC 格式的 Argon2id 哈希串。
func Hash(password string) string {
	salt := make([]byte, saltLen)
	rand.Read(salt)
	key := argon2.IDKey([]byte(password), salt, iters, memKiB, threads, keyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memKiB, iters, threads, enc.EncodeToString(salt), enc.EncodeToString(key))
}

// Verify 校验密码，比较过程是常量时间的。
func Verify(hash, password string) error {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return ErrMismatch
	}
	var v int
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return ErrMismatch
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return ErrMismatch
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[4])
	if err != nil {
		return ErrMismatch
	}
	want, err := enc.DecodeString(parts[5])
	if err != nil {
		return ErrMismatch
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

// dummyHash 用于用户名不存在时也执行一次哈希，避免通过响应时间判断用户名是否存在。
var dummyHash = Hash("dummy-password-for-timing")

// VerifyDummy 消耗与真实校验相同的时间。
func VerifyDummy(password string) { Verify(dummyHash, password) }

// ValidatePassword 检查密码强度（最低要求）。
func ValidatePassword(p string) error {
	if len([]rune(p)) < 8 {
		return errors.New("密码至少 8 位")
	}
	if len(p) > 256 {
		return errors.New("密码过长")
	}
	return nil
}

// ValidatePin 检查 PIN 格式：4 到 8 位数字。
func ValidatePin(pin string) error {
	if len(pin) < 4 || len(pin) > 8 {
		return errors.New("PIN 必须是 4 到 8 位数字")
	}
	for _, c := range pin {
		if c < '0' || c > '9' {
			return errors.New("PIN 必须是 4 到 8 位数字")
		}
	}
	return nil
}
