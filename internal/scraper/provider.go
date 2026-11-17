package scraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Result 是刮削得到的元数据。图片以 URL 给出，由调用方下载。
type Result struct {
	Source        string
	Title         string
	OriginalTitle string
	Overview      string
	ReleaseDate   string // YYYY-MM-DD
	Year          int
	Code          string
	Studio        string
	Director      string
	Series        string
	Rating        float64
	Genres        []string
	Actors        []string
	PosterURL     string // 竖版海报
	BackdropURL   string // 横版大图
	CropPoster    bool   // PosterURL 是横版封面，需要裁出右半部分
}

// Query 是刮削请求。
type Query struct {
	Title string
	Year  int
	Code  string
}

var ErrNoMatch = errors.New("没有找到匹配的结果")

// Provider 是在线数据源。
type Provider interface {
	Name() string
	Lookup(ctx context.Context, q Query) (*Result, error)
	// Fetch 下载图片（数据源可能需要特殊的请求头）。
	Fetch(ctx context.Context, url string) ([]byte, error)
}

// HTTP 是刮削器共用的 HTTP 客户端，支持代理。
type HTTP struct {
	c  *http.Client
	ua string
}

// NewHTTP 创建 HTTP 客户端。proxy 为空时使用系统代理环境变量。
func NewHTTP(proxy string) (*HTTP, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("代理地址无效: %w", err)
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &HTTP{c: &http.Client{Transport: tr, Timeout: 30 * time.Second},
		ua: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130 Safari/537.36"}, nil
}

const maxBody = 20 << 20

func (h *HTTP) get(ctx context.Context, rawURL string, header http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", h.ua)
	for k, v := range header {
		req.Header[k] = v
	}
	resp, err := h.c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNoMatch
	}
	if resp.StatusCode != http.StatusOK {
		msg := string(b)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(msg))
	}
	return b, nil
}

func (h *HTTP) getJSON(ctx context.Context, rawURL string, header http.Header, v any) error {
	b, err := h.get(ctx, rawURL, header)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func yearOf(date string) int {
	if len(date) >= 4 {
		var y int
		fmt.Sscanf(date[:4], "%d", &y)
		return y
	}
	return 0
}
