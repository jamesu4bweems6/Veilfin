// Package backup 实现加密备份与恢复。
//
// 备份内容：keys/（身份密钥、媒体主密钥）、数据库快照、assets/（封面、字幕、预览图，可选）。
// 文件格式：
//
//	"VFBK1\n" ‖ salt(16) ‖ BE32(t) ‖ BE32(m) ‖ p(1) ‖ 流前缀(16)
//	之后是若干加密块：XChaCha20-Poly1305(key, 流前缀 ‖ BE64(序号), 明文块 ≤1MiB, AAD=头部 ‖ 是否最后一块)
//	每块前有 BE32 长度。key = Argon2id(口令, salt)。
//
// 明文是 tar.gz。最后一块带结束标记，截断会被发现。
package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"veilfin/internal/db"
)

const (
	magic     = "VFBK1\n"
	chunk     = 1 << 20
	argonT    = 3
	argonM    = 128 * 1024
	argonP    = 2
	headerLen = len(magic) + 16 + 4 + 4 + 1 + 16
	dbName    = db.FileName
)

var ErrBadPass = errors.New("口令错误或备份文件已损坏")

func deriveKey(pass string, salt []byte, t, m uint32, p uint8) (cipher.AEAD, error) {
	key := argon2.IDKey([]byte(pass), salt, t, m, p, chacha20poly1305.KeySize)
	defer clear(key)
	return chacha20poly1305.NewX(key)
}

// streamWriter 把数据切成块加密写出。
type streamWriter struct {
	w      io.Writer
	aead   cipher.AEAD
	header []byte
	prefix []byte
	seq    uint64
	buf    []byte
}

func (s *streamWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		room := chunk - len(s.buf)
		take := min(room, len(p))
		s.buf = append(s.buf, p[:take]...)
		p = p[take:]
		if len(s.buf) == chunk {
			if err := s.flush(false); err != nil {
				return 0, err
			}
		}
	}
	return n, nil
}

func (s *streamWriter) nonce() []byte {
	n := make([]byte, chacha20poly1305.NonceSizeX)
	copy(n, s.prefix)
	binary.BigEndian.PutUint64(n[16:], s.seq)
	return n
}

func (s *streamWriter) flush(last bool) error {
	aad := append(append([]byte(nil), s.header...), 0)
	if last {
		aad[len(aad)-1] = 1
	}
	ct := s.aead.Seal(nil, s.nonce(), s.buf, aad)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(ct)))
	if _, err := s.w.Write(l[:]); err != nil {
		return err
	}
	if _, err := s.w.Write(ct); err != nil {
		return err
	}
	s.seq++
	s.buf = s.buf[:0]
	return nil
}

func (s *streamWriter) Close() error { return s.flush(true) }

// streamReader 逐块解密校验。
type streamReader struct {
	r      io.Reader
	aead   cipher.AEAD
	header []byte
	prefix []byte
	seq    uint64
	buf    []byte
	done   bool
}

func (s *streamReader) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		if s.done {
			return 0, io.EOF
		}
		var l [4]byte
		if _, err := io.ReadFull(s.r, l[:]); err != nil {
			return 0, ErrBadPass // 没有遇到结束标记就到了文件尾：被截断
		}
		n := binary.BigEndian.Uint32(l[:])
		if n > chunk+chacha20poly1305.Overhead {
			return 0, ErrBadPass
		}
		ct := make([]byte, n)
		if _, err := io.ReadFull(s.r, ct); err != nil {
			return 0, ErrBadPass
		}
		nonce := make([]byte, chacha20poly1305.NonceSizeX)
		copy(nonce, s.prefix)
		binary.BigEndian.PutUint64(nonce[16:], s.seq)
		aad := append(append([]byte(nil), s.header...), 0)
		pt, err := s.aead.Open(nil, nonce, ct, aad)
		if err != nil {
			aad[len(aad)-1] = 1
			if pt, err = s.aead.Open(nil, nonce, ct, aad); err != nil {
				return 0, ErrBadPass
			}
			s.done = true
		}
		s.seq++
		s.buf = pt
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

// Create 把数据目录备份到 out。
func Create(ctx context.Context, dataDir, out, pass string, withAssets bool) error {
	if len(pass) < 8 {
		return errors.New("备份口令至少 8 位")
	}
	tmp, err := os.MkdirTemp("", "epbk-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	snap := filepath.Join(tmp, dbName)
	d, err := db.Open(db.PathIn(dataDir))
	if err != nil {
		return err
	}
	err = d.Snapshot(ctx, snap)
	d.Close()
	if err != nil {
		return fmt.Errorf("数据库快照失败: %w", err)
	}

	f, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(out)
		}
	}()
	bw := bufio.NewWriterSize(f, 1<<20)
	header := make([]byte, 0, headerLen)
	header = append(header, magic...)
	salt := make([]byte, 16)
	rand.Read(salt)
	header = append(header, salt...)
	header = binary.BigEndian.AppendUint32(header, argonT)
	header = binary.BigEndian.AppendUint32(header, argonM)
	header = append(header, argonP)
	prefix := make([]byte, 16)
	rand.Read(prefix)
	header = append(header, prefix...)
	if _, err := bw.Write(header); err != nil {
		return err
	}
	aead, err := deriveKey(pass, salt, argonT, argonM, argonP)
	if err != nil {
		return err
	}
	sw := &streamWriter{w: bw, aead: aead, header: header, prefix: prefix}
	gz := gzip.NewWriter(sw)
	tw := tar.NewWriter(gz)

	addFile := func(src, name string) error {
		st, err := os.Stat(src)
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: filepath.ToSlash(name), Mode: 0o600, Size: st.Size(), ModTime: st.ModTime()}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(tw, in)
		return err
	}
	for _, k := range []string{"identity.key", "media.key"} {
		if err := addFile(filepath.Join(dataDir, "keys", k), "keys/"+k); err != nil {
			return fmt.Errorf("读取密钥失败: %w", err)
		}
	}
	if err := addFile(snap, dbName); err != nil {
		return err
	}
	if withAssets {
		root := filepath.Join(dataDir, "assets")
		err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return nil
				}
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if de.IsDir() {
				if strings.HasPrefix(de.Name(), "trick-") {
					return filepath.SkipDir // 生成中的临时目录
				}
				return nil
			}
			rel, _ := filepath.Rel(dataDir, p)
			return addFile(p, rel)
		})
		if err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := sw.Close(); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}

// Restore 把备份恢复到 dataDir。目标目录已有密钥时需要 force。
func Restore(in, dataDir, pass string, force bool) error {
	if _, err := os.Stat(filepath.Join(dataDir, "keys", "identity.key")); err == nil && !force {
		return errors.New("目标数据目录已有密钥，如确认覆盖请加 -force（会覆盖现有密钥和数据库）")
	}
	f, err := os.Open(in)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<20)
	header := make([]byte, headerLen)
	if _, err := io.ReadFull(br, header); err != nil || string(header[:len(magic)]) != magic {
		return errors.New("不是 Veilfin 备份文件")
	}
	p := header[len(magic):]
	salt := p[:16]
	t := binary.BigEndian.Uint32(p[16:])
	m := binary.BigEndian.Uint32(p[20:])
	par := p[24]
	prefix := p[25:41]
	if t == 0 || t > 20 || m < 8*1024 || m > 1024*1024 || par == 0 || par > 16 { // 内存上限 1 GiB
		return errors.New("备份文件参数异常")
	}
	aead, err := deriveKey(pass, salt, t, m, par)
	if err != nil {
		return err
	}
	sr := &streamReader{r: br, aead: aead, header: header, prefix: prefix}
	gz, err := gzip.NewReader(sr)
	if err != nil {
		if errors.Is(err, ErrBadPass) {
			return ErrBadPass
		}
		return ErrBadPass
	}
	// 先解到临时目录，全部校验通过后再替换，避免恢复到一半留下残缺的数据
	stage, err := os.MkdirTemp(filepath.Dir(filepath.Clean(dataDir)), ".restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ErrBadPass
		}
		name := filepath.FromSlash(hdr.Name)
		if !filepath.IsLocal(name) || hdr.Typeflag != tar.TypeReg {
			return fmt.Errorf("备份中包含非法路径: %s", hdr.Name)
		}
		dst := filepath.Join(stage, name)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return ErrBadPass
		}
		out.Close()
	}
	if _, err := io.Copy(io.Discard, sr); err != nil || !sr.done {
		return ErrBadPass
	}
	for _, need := range []string{"keys/identity.key", "keys/media.key", dbName} {
		if _, err := os.Stat(filepath.Join(stage, need)); err != nil {
			return fmt.Errorf("备份不完整，缺少 %s", need)
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	// 删除旧数据库的 WAL 文件，避免与恢复的数据库混用
	for _, suffix := range []string{"-wal", "-shm"} {
		os.Remove(filepath.Join(dataDir, dbName+suffix))
	}
	return filepath.WalkDir(stage, func(p string, de fs.DirEntry, err error) error {
		if err != nil || de.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(stage, p)
		dst := filepath.Join(dataDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		os.Remove(dst)
		if err := os.Rename(p, dst); err != nil {
			return copyFile(p, dst)
		}
		return nil
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
