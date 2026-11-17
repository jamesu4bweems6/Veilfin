package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"veilfin/internal/client"
	"veilfin/internal/library"
	"veilfin/internal/scraper"
	"veilfin/internal/server"
)

const adminPass = "admin-password-123"

// ---- 模拟 CDN：记录所有经过的流量 ----

type cdnRecorder struct {
	next http.Handler
	mu   sync.Mutex
	seen bytes.Buffer
}

type teeWriter struct {
	http.ResponseWriter
	buf *bytes.Buffer
}

func (t *teeWriter) Write(p []byte) (int, error) {
	t.buf.Write(p)
	return t.ResponseWriter.Write(p)
}

func (c *cdnRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var resp bytes.Buffer
	c.next.ServeHTTP(&teeWriter{w, &resp}, r)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen.WriteString(r.URL.String() + "\n")
	for k, v := range r.Header {
		c.seen.WriteString(k + ": " + strings.Join(v, ",") + "\n")
	}
	for k, v := range w.Header() {
		c.seen.WriteString(k + ": " + strings.Join(v, ",") + "\n")
	}
	c.seen.Write(body)
	c.seen.Write(resp.Bytes())
}

func (c *cdnRecorder) contains(b []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Contains(c.seen.Bytes(), b)
}

// ---- 测试环境 ----

type env struct {
	t        *testing.T
	ctx      context.Context
	srv      *server.Server
	cdn      *cdnRecorder
	upstream *httptest.Server
	dataDir  string
	mediaDir string
}

func hasFFmpeg() bool {
	_, err1 := exec.LookPath("ffmpeg")
	_, err2 := exec.LookPath("ffprobe")
	return err1 == nil && err2 == nil
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	e := &env{t: t, ctx: ctx, dataDir: t.TempDir(), mediaDir: t.TempDir()}
	srv, err := server.New(ctx, server.Config{DataDir: e.dataDir, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	e.srv = srv
	e.cdn = &cdnRecorder{next: srv.Handler()}
	e.upstream = httptest.NewServer(e.cdn)
	t.Cleanup(e.upstream.Close)
	return e
}

// user 代表一个通过本地客户端访问的浏览器（有自己的 Cookie，即自己的会话）。
type user struct {
	t     *testing.T
	cli   *client.Client
	local *httptest.Server
	hc    *http.Client
}

func newUser(t *testing.T, cli *client.Client, local *httptest.Server) *user {
	jar, _ := cookiejar.New(nil)
	return &user{t: t, cli: cli, local: local, hc: &http.Client{Jar: jar}}
}

// sameClient 模拟同一个本地客户端上的另一个浏览器（另一台设备）。
func (u *user) sameClient() *user { return newUser(u.t, u.cli, u.local) }

func (e *env) newClient() *user {
	e.t.Helper()
	cli, err := client.New(e.ctx, client.Config{
		Server: e.upstream.URL, DataDir: e.t.TempDir(),
		ServerKey: base64.StdEncoding.EncodeToString(e.srv.PublicKey()),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	local := httptest.NewServer(cli.Handler())
	e.t.Cleanup(local.Close)
	return newUser(e.t, cli, local)
}

type rpcResult struct {
	OK   bool            `json:"ok"`
	Data json.RawMessage `json:"data"`
	Code string          `json:"code"`
	Err  string          `json:"err"`
}

func (u *user) call(m string, p any) rpcResult {
	u.t.Helper()
	b, _ := json.Marshal(map[string]any{"m": m, "p": p})
	req, _ := http.NewRequest(http.MethodPost, u.local.URL+"/api/rpc", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-VF-Local", "1")
	resp, err := u.hc.Do(req)
	if err != nil {
		u.t.Fatal(err)
	}
	defer resp.Body.Close()
	var r rpcResult
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		u.t.Fatal(err)
	}
	return r
}

// must 调用并要求成功，结果解码到 out。
func (u *user) must(m string, p any, out any) {
	u.t.Helper()
	r := u.call(m, p)
	if !r.OK {
		u.t.Fatalf("%s 失败: %s (%s)", m, r.Err, r.Code)
	}
	if out != nil {
		if err := json.Unmarshal(r.Data, out); err != nil {
			u.t.Fatalf("%s 结果解析失败: %v\n%s", m, err, r.Data)
		}
	}
}

func (u *user) expectCode(m string, p any, code string) {
	u.t.Helper()
	r := u.call(m, p)
	if r.OK || r.Code != code {
		u.t.Fatalf("%s：期望错误码 %s，实际 ok=%v code=%s err=%s", m, code, r.OK, r.Code, r.Err)
	}
}

func (u *user) get(path, rng string) (int, []byte, http.Header) {
	u.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, u.local.URL+path, nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := u.hc.Do(req)
	if err != nil {
		u.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

func (e *env) setupAdmin() *user {
	e.t.Helper()
	u := e.newClient()
	u.expectCode("home.get", nil, "needs_setup")
	u.expectCode("setup.create", map[string]string{"code": "WRONG", "username": "admin", "password": adminPass}, "unauthorized")
	u.must("setup.create", map[string]string{"code": e.srv.SetupCode(), "username": "admin", "password": adminPass}, nil)
	return u
}

// waitIdle 等待所有后台任务（扫描、元数据、预览图）完成。
func (e *env) waitIdle() {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.ctx, 3*time.Minute)
	defer cancel()
	for i := 0; i < 3; i++ { // 扫描会继续派生任务，多等几轮直到稳定
		if err := e.srv.Lib.Main.Wait(ctx); err != nil {
			e.t.Fatal("等待任务超时")
		}
		if err := e.srv.Lib.Heavy.Wait(ctx); err != nil {
			e.t.Fatal("等待任务超时")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func makeVideo(t *testing.T, path string, seconds int, freq int) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	cmd := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc=size=320x180:rate=25:duration=%d", seconds),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:duration=%d", freq, seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "25", "-c:a", "aac", "-movflags", "+faststart", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("生成测试视频失败: %v %s", err, out)
	}
}

func jpegBytes(c color.RGBA, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

// fakeMetaTube 模拟 MetaTube 服务端。
func fakeMetaTube(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/movies/search", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "ABC-123" {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		w.Write([]byte(`{"data":[{"id":"x1","provider":"FAKE","number":"abc-123","title":"t"}]}`))
	})
	mux.HandleFunc("GET /v1/movies/FAKE/x1", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"id":"x1","number":"ABC-123","title":"番号测试标题","summary":"番号简介文本",
			"provider":"FAKE","director":"导演甲","actors":["演员甲","演员乙"],"maker":"制作商","series":"系列名",
			"genres":["剧情"],"score":4.5,"release_date":"2024-05-01T00:00:00Z"}}`))
	})
	mux.HandleFunc("GET /v1/images/primary/FAKE/x1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(jpegBytes(color.RGBA{200, 30, 30, 255}, 300, 450))
	})
	mux.HandleFunc("GET /v1/images/backdrop/FAKE/x1", func(w http.ResponseWriter, r *http.Request) {
		w.Write(jpegBytes(color.RGBA{30, 30, 200, 255}, 800, 450))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// fakeTMDB 模拟 TMDB API。
func fakeTMDB(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /search/movie", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "The Matrix" && r.URL.Query().Get("api_key") == "k" {
			w.Write([]byte(`{"results":[{"id":603}]}`))
			return
		}
		w.Write([]byte(`{"results":[]}`))
	})
	mux.HandleFunc("GET /movie/603", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"title":"黑客帝国","original_title":"The Matrix","overview":"电影简介","release_date":"1999-03-31",
			"poster_path":"/p.jpg","backdrop_path":"/b.jpg","vote_average":8.2,"genres":[{"name":"动作"}],
			"production_companies":[{"name":"华纳"}],"credits":{"cast":[{"name":"基努·里维斯"}],"crew":[{"name":"沃卓斯基","job":"Director"}]}}`))
	})
	mux.HandleFunc("GET /img/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(jpegBytes(color.RGBA{10, 160, 10, 255}, 400, 600))
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (e *env) useFakeScrapers(mt, tm *httptest.Server) {
	e.srv.Lib.NewProvider = func(kind string, st library.Settings) (scraper.Provider, error) {
		h, _ := scraper.NewHTTP("")
		switch kind {
		case "metatube":
			return &scraper.MetaTube{Base: mt.URL, H: h}, nil
		case "tmdb":
			return &scraper.TMDB{Key: "k", Base: tm.URL, ImgBase: tm.URL + "/img", H: h}, nil
		}
		return nil, fmt.Errorf("unknown")
	}
}

// ---- 列表结果的结构 ----

type card struct {
	ID         int64   `json:"id"`
	LibraryID  int64   `json:"libraryId"`
	Title      string  `json:"title"`
	Code       string  `json:"code"`
	Poster     string  `json:"poster"`
	Thumb      string  `json:"thumb"`
	Duration   float64 `json:"duration"`
	Restricted bool    `json:"restricted"`
	Position   float64 `json:"position"`
	Played     bool    `json:"played"`
	PlayCount  int     `json:"playCount"`
}

type listResult struct {
	Items   []card `json:"items"`
	Total   int    `json:"total"`
	Folders []struct {
		Path  string `json:"path"`
		Count int    `json:"count"`
	} `json:"folders"`
}

type detail struct {
	card
	Overview     string              `json:"overview"`
	Studio       string              `json:"studio"`
	Director     string              `json:"director"`
	Series       string              `json:"series"`
	ReleaseDate  string              `json:"releaseDate"`
	Tags         map[string][]string `json:"tags"`
	Backdrop     string              `json:"backdrop"`
	MetaSource   string              `json:"metaSource"`
	Locked       []string            `json:"locked"`
	HasTrickplay bool                `json:"hasTrickplay"`
	Subtitles    []struct {
		Label string `json:"label"`
		Lang  string `json:"lang"`
	} `json:"subtitles"`
	Path string `json:"path"`
}

type playResult struct {
	Video     string `json:"video"`
	Subtitles []struct {
		Label string `json:"label"`
		Src   string `json:"src"`
	} `json:"subtitles"`
	Trickplay *struct {
		Interval float64  `json:"interval"`
		Count    int      `json:"count"`
		Sprites  []string `json:"sprites"`
	} `json:"trickplay"`
	Next *struct {
		ID int64 `json:"id"`
	} `json:"next"`
}

func findByTitle(t *testing.T, items []card, title string) card {
	t.Helper()
	for _, c := range items {
		if c.Title == title {
			return c
		}
	}
	names := []string{}
	for _, c := range items {
		names = append(names, c.Title)
	}
	t.Fatalf("没有找到 %q，现有: %v", title, names)
	return card{}
}

func gbk(s string) []byte {
	b, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s))
	return b
}
