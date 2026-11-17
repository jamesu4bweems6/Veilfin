package client

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"veilfin/internal/api"
	"veilfin/internal/mediacrypt"
)

const (
	cacheBytes   = 64 << 20 // 本地解密缓存上限（足够覆盖拖动回看和预取）
	readahead    = 4        // 顺序播放时每次向上游拉取的块数
	maxBlobs     = 200000   // 记住的对象引用上限
	prefetchAhea = 4        // 顺序播放时后台预取的块数
)

type blobInfo struct {
	id     string
	mime   string
	size   int64
	cipher *mediacrypt.Cipher
}

// registerRef 为某个浏览器记住服务端下发的对象引用（解密密钥只保存在本程序内存里，不交给浏览器）。
func (c *Client) registerRef(b *browser, r *api.BlobRef) (string, error) {
	key, err := base64.RawURLEncoding.DecodeString(r.Key)
	if err != nil || len(key) != mediacrypt.KeySize {
		return "", errors.New("对象密钥格式错误")
	}
	ci, err := mediacrypt.NewCipher(key, nil, r.ID, r.Size)
	if err != nil {
		return "", err
	}
	clear(key)
	b.blobMu.Lock()
	if len(b.blobs) >= maxBlobs {
		b.blobs = map[string]*blobInfo{}
	}
	b.blobs[r.ID] = &blobInfo{id: r.ID, mime: r.Mime, size: r.Size, cipher: ci}
	b.blobMu.Unlock()
	return "/b/" + r.ID, nil
}

func (b *browser) blob(id string) *blobInfo {
	b.blobMu.Lock()
	defer b.blobMu.Unlock()
	return b.blobs[id]
}

// handleBlob 把解密后的内容以标准 HTTP（支持 Range）提供给本机浏览器。
func (c *Client) handleBlob(w http.ResponseWriter, r *http.Request) {
	b := c.lookupBrowser(r)
	if b == nil {
		http.Error(w, "未登录", http.StatusUnauthorized)
		return
	}
	info := b.blob(r.PathValue("id"))
	if info == nil {
		http.Error(w, "未知对象，请刷新页面", http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", info.mime)
	h.Set("ETag", `"`+info.id+`"`)
	// 解密后的明文不允许浏览器写入磁盘缓存
	h.Set("Cache-Control", "no-store")
	pr := &plainReader{c: c, b: b, info: info, ctx: r.Context()}
	http.ServeContent(w, r, "", time.Time{}, io.NewSectionReader(pr, 0, info.size))
}

// plainReader 以明文坐标提供 ReadAt：按需从上游拉取密文块、解密，并放进共享缓存。
type plainReader struct {
	c       *Client
	b       *browser
	info    *blobInfo
	ctx     context.Context
	fetched int
}

func (p *plainReader) ReadAt(b []byte, off int64) (int, error) {
	size := p.info.size
	if off < 0 {
		return 0, errors.New("偏移非法")
	}
	n := 0
	for len(b) > 0 && off < size {
		idx := off / mediacrypt.ChunkSize
		chunk, err := p.chunk(idx)
		if err != nil {
			return n, err
		}
		m := copy(b, chunk[off-idx*mediacrypt.ChunkSize:])
		n += m
		b = b[m:]
		off += int64(m)
	}
	if len(b) > 0 {
		return n, io.EOF
	}
	return n, nil
}

func (p *plainReader) chunk(idx int64) ([]byte, error) {
	last := p.info.cipher.NumChunks() - 1
	if data := p.c.cache.get(cacheID(p.b, p.info), idx); data != nil {
		if p.fetched > 0 {
			p.c.prefetch(p.b, p.info, idx+1, min(idx+prefetchAhea, last))
		}
		return data, nil
	}
	// 每个请求第一次只拉 1 块，让播放和拖动尽快开始；之后的顺序读取按 readahead 批量拉取
	n := int64(1)
	if p.fetched > 0 {
		n = readahead
	}
	end := min(idx+n-1, last)
	chunks, err := p.c.fetchChunks(p.ctx, p.b, p.info, idx, end)
	if err != nil {
		return nil, err
	}
	p.fetched++
	if p.fetched > 1 {
		p.c.prefetch(p.b, p.info, end+1, min(end+prefetchAhea, last))
	}
	return chunks[0], nil
}

// prefetch 在后台预取后续的块，掩盖 CDN 往返延迟。
// cacheID 是解密缓存的键：带上浏览器会话 ID，一个浏览器读不到另一个浏览器解密过的数据。
func cacheID(b *browser, info *blobInfo) string { return b.id + "/" + info.id }

func (c *Client) prefetch(b *browser, info *blobInfo, first, last int64) {
	if first > last {
		return
	}
	for first <= last && c.cache.get(cacheID(b, info), first) != nil {
		first++
	}
	if first > last {
		return
	}
	key := cacheID(b, info) + ":" + strconv.FormatInt(first, 10)
	c.pfMu.Lock()
	if c.prefetching[key] {
		c.pfMu.Unlock()
		return
	}
	c.prefetching[key] = true
	c.pfMu.Unlock()
	go func() {
		defer func() {
			c.pfMu.Lock()
			delete(c.prefetching, key)
			c.pfMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		c.fetchChunks(ctx, b, info, first, last)
	}()
}

var errForbidden = errors.New("无权访问该对象")

// fetchChunks 从服务端（或 CDN）拉取 [first, last] 的密文块，逐块解密校验后写入缓存。
func (c *Client) fetchChunks(ctx context.Context, b *browser, info *blobInfo, first, last int64) ([][]byte, error) {
	off, length := info.cipher.CipherRange(first, last)
	force := false
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := b.tr.blobToken(ctx, force)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/m/"+info.id, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-"+strconv.FormatInt(off+length-1, 10))
		req.Header.Set(api.TokenHeader, tok)
		resp, err := c.hc.Do(req)
		if err != nil {
			return nil, err
		}
		ct, err := readRange(resp, off, length)
		resp.Body.Close()
		if errors.Is(err, errForbidden) {
			force = true // 令牌失效或解锁状态变化，刷新后重试
			continue
		}
		if err != nil {
			return nil, err
		}
		out := make([][]byte, 0, last-first+1)
		pos := int64(0)
		for i := first; i <= last; i++ {
			clen := int64(info.cipher.PlainChunkLen(i)) + mediacrypt.Overhead
			plain, err := info.cipher.OpenChunk(nil, i, ct[pos:pos+clen])
			if err != nil {
				return nil, fmt.Errorf("第 %d 块校验失败（数据被篡改或损坏）", i)
			}
			c.cache.put(cacheID(b, info), i, plain)
			out = append(out, plain)
			pos += clen
		}
		return out, nil
	}
	return nil, errForbidden
}

// readRange 读取 Range 响应体。上游如果忽略 Range 返回了 200，就手动跳到需要的位置。
func readRange(resp *http.Response, off, length int64) ([]byte, error) {
	switch resp.StatusCode {
	case http.StatusPartialContent:
		start, ok := contentRangeStart(resp.Header.Get("Content-Range"))
		if !ok || start != off {
			return nil, errors.New("上游返回的 Content-Range 不匹配")
		}
	case http.StatusOK:
		if _, err := io.CopyN(io.Discard, resp.Body, off); err != nil {
			return nil, err
		}
	case http.StatusForbidden:
		return nil, errForbidden
	default:
		return nil, fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func contentRangeStart(v string) (int64, bool) {
	// 格式：bytes start-end/total
	const prefix = "bytes "
	if len(v) <= len(prefix) || v[:len(prefix)] != prefix {
		return 0, false
	}
	v = v[len(prefix):]
	for i := 0; i < len(v); i++ {
		if v[i] == '-' {
			n, err := strconv.ParseInt(v[:i], 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

// ---- 解密块缓存（按字节数限制的 LRU） ----

type cacheKey struct {
	id  string
	idx int64
}

type cacheEntry struct {
	key        cacheKey
	data       []byte
	prev, next *cacheEntry
}

type chunkCache struct {
	mu         sync.Mutex
	limit      int
	used       int
	m          map[cacheKey]*cacheEntry
	head, tail *cacheEntry // head 最新
}

func newChunkCache(limit int) *chunkCache {
	return &chunkCache{limit: limit, m: map[cacheKey]*cacheEntry{}}
}

func (c *chunkCache) unlink(e *cacheEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	e.prev, e.next = nil, nil
}

func (c *chunkCache) pushFront(e *cacheEntry) {
	e.next = c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
	if c.tail == nil {
		c.tail = e
	}
}

// clearPrefix 删除键以 prefix 开头的缓存块（某个浏览器会话的全部数据）。
func (c *chunkCache) clearPrefix(prefix string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.m {
		if strings.HasPrefix(k.id, prefix) {
			c.unlink(e)
			delete(c.m, k)
			c.used -= len(e.data)
		}
	}
}

func (c *chunkCache) get(id string, idx int64) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[cacheKey{id, idx}]; ok {
		c.unlink(e)
		c.pushFront(e)
		return e.data
	}
	return nil
}

func (c *chunkCache) put(id string, idx int64, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := cacheKey{id, idx}
	if e, ok := c.m[k]; ok {
		c.unlink(e)
		c.pushFront(e)
		return
	}
	e := &cacheEntry{key: k, data: data}
	c.m[k] = e
	c.pushFront(e)
	c.used += len(data)
	for c.used > c.limit && c.tail != nil && c.tail != e {
		old := c.tail
		c.unlink(old)
		delete(c.m, old.key)
		c.used -= len(old.data)
	}
}
