package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const (
	identityFile = "identity.key" // Ed25519 种子（32 字节）
	mediaKeyFile = "media.key"    // 媒体主密钥（32 字节）
)

// LoadKeys 读取数据目录下的密钥，不存在则生成。这两个文件丢失后所有客户端都要重新信任服务端，务必备份。
func LoadKeys(dataDir string) (ed25519.PrivateKey, []byte, error) {
	keyDir := filepath.Join(dataDir, "keys")
	if err := os.MkdirAll(keyDir, 0o700); err != nil {
		return nil, nil, err
	}
	seed, err := loadOrCreate(filepath.Join(keyDir, identityFile), ed25519.SeedSize)
	if err != nil {
		return nil, nil, err
	}
	master, err := loadOrCreate(filepath.Join(keyDir, mediaKeyFile), 32)
	if err != nil {
		return nil, nil, err
	}
	return ed25519.NewKeyFromSeed(seed), master, nil
}

func loadOrCreate(path string, n int) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		if len(b) != n {
			return nil, fmt.Errorf("密钥文件 %s 长度异常（%d 字节）", path, len(b))
		}
		return b, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	b = make([]byte, n)
	rand.Read(b)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return nil, err
	}
	return b, f.Close()
}
