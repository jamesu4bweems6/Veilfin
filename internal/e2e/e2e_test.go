package e2e

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"veilfin/internal/api"
	"veilfin/internal/backup"
	"veilfin/internal/client"
	"veilfin/internal/proto"
	"veilfin/internal/server"
)

// TestAuthAndUsers：初始化、登录、锁定、普通用户权限。
func TestAuthAndUsers(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin()
	admin.expectCode("setup.create", map[string]string{"code": "X", "username": "b", "password": adminPass}, "forbidden")

	var me struct {
		Username string `json:"username"`
		IsAdmin  bool   `json:"isAdmin"`
	}
	admin.must("auth.me", nil, &me)
	if me.Username != "admin" || !me.IsAdmin {
		t.Fatalf("初始化后的用户不对: %+v", me)
	}

	admin.must("admin.users.create", map[string]any{"username": "bob", "password": "bob-password"}, nil)
	admin.expectCode("admin.users.create", map[string]any{"username": "BOB", "password": "bob-password"}, "bad_request") // 用户名不区分大小写
	admin.expectCode("admin.users.create", map[string]any{"username": "carl", "password": "short"}, "bad_request")

	bob := e.newClient()
	bob.expectCode("auth.login", map[string]string{"username": "bob", "password": "wrong-pass"}, "unauthorized")
	bob.expectCode("auth.login", map[string]string{"username": "nobody", "password": "x"}, "unauthorized")
	bob.must("auth.login", map[string]string{"username": "bob", "password": "bob-password"}, nil)
	bob.expectCode("admin.users.list", nil, "forbidden")
	bob.expectCode("admin.libs.create", map[string]any{"name": "x", "kind": "home", "paths": []string{e.mediaDir}}, "forbidden")

	// 浏览器拿不到登录令牌和内部方法
	bob.expectCode("auth.blobToken", nil, "forbidden")
	bob.expectCode("auth.resume", map[string]string{"token": "x"}, "forbidden")

	// 连续输错密码后账号被锁定（即使换新会话也无效）
	mallory := e.newClient()
	for i := 0; i < 5; i++ {
		mallory.call("auth.login", map[string]string{"username": "bob", "password": "guess"})
	}
	other := e.newClient()
	other.expectCode("auth.login", map[string]string{"username": "bob", "password": "bob-password"}, "too_many")

	// 管理员重置密码后解除锁定，旧会话被登出
	var users []struct {
		ID       int64  `json:"id"`
		Username string `json:"username"`
	}
	admin.must("admin.users.list", nil, &users)
	var bobID int64
	for _, u := range users {
		if u.Username == "bob" {
			bobID = u.ID
		}
	}
	admin.must("admin.users.update", map[string]any{"id": bobID, "password": "new-bob-password", "libraries": []int64{}}, nil)
	bob.expectCode("home.get", nil, "unauthorized")
	other2 := e.newClient()
	other2.must("auth.login", map[string]string{"username": "bob", "password": "new-bob-password"}, nil)

	// 不能把最后一个管理员降级或删掉自己
	admin.expectCode("admin.users.update", map[string]any{"id": 1, "isAdmin": false, "libraries": []int64{}}, "bad_request")
	admin.expectCode("admin.users.delete", map[string]any{"id": 1}, "bad_request")

	// 禁用账号后立即失效
	admin.must("admin.users.update", map[string]any{"id": bobID, "disabled": true, "libraries": []int64{}}, nil)
	other2.expectCode("home.get", nil, "unauthorized")
}

// TestFullLibrary：扫描、本地元数据、刮削、字幕、预览图、播放、进度、权限、PIN、编辑、CDN 视角。
func TestFullLibrary(t *testing.T) {
	if !hasFFmpeg() {
		t.Skip("需要 ffmpeg")
	}
	e := newEnv(t)
	e.useFakeScrapers(fakeMetaTube(t), fakeTMDB(t))

	home := filepath.Join(e.mediaDir, "home")
	jav := filepath.Join(e.mediaDir, "jav")
	movies := filepath.Join(e.mediaDir, "movies")
	makeVideo(t, filepath.Join(home, "家庭聚会.mp4"), 24, 440)
	os.WriteFile(filepath.Join(home, "家庭聚会.srt"), gbk("1\n00:00:01,000 --> 00:00:04,000\n生日快乐字幕\n"), 0o644)
	os.WriteFile(filepath.Join(home, "家庭聚会.nfo"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<movie><title>二零二三年家庭聚会</title><plot>隐私简介内容</plot><year>2023</year><genre>家庭</genre>
<actor><name>爷爷</name></actor><actor><name>奶奶</name></actor></movie>`), 0o644)
	os.WriteFile(filepath.Join(home, "家庭聚会-poster.jpg"), jpegBytes(rgba(250, 200, 0), 400, 600), 0o644)
	makeVideo(t, filepath.Join(home, "旅行", "海边.mp4"), 12, 660)
	makeVideo(t, filepath.Join(home, "旅行", "山顶.mp4"), 12, 770)
	makeVideo(t, filepath.Join(jav, "[site.com]ABC-123-C.mp4"), 12, 880)
	makeVideo(t, filepath.Join(movies, "The.Matrix.1999.1080p.mp4"), 12, 550)

	admin := e.setupAdmin()
	var homeLib, javLib, movieLib struct {
		ID int64 `json:"id"`
	}
	admin.must("admin.libs.create", map[string]any{"name": "家庭录像", "kind": "home", "scraper": "none", "paths": []string{home}}, &homeLib)
	admin.must("admin.libs.create", map[string]any{"name": "收藏", "kind": "jav", "scraper": "metatube", "restricted": true, "paths": []string{jav}}, &javLib)
	admin.must("admin.libs.create", map[string]any{"name": "电影", "kind": "movie", "scraper": "tmdb", "paths": []string{movies}}, &movieLib)
	admin.expectCode("admin.libs.create", map[string]any{"name": "坏", "kind": "home", "paths": []string{"relative/path"}}, "bad_request")
	admin.expectCode("admin.libs.create", map[string]any{"name": "坏", "kind": "home", "paths": []string{filepath.Join(e.mediaDir, "不存在")}}, "bad_request")
	e.waitIdle()

	// ---- 扫描结果 ----
	var all listResult
	admin.must("items.list", map[string]any{"libraryId": 0, "limit": 100}, &all)
	if all.Total != 5 {
		t.Fatalf("期望 5 个视频，实际 %d", all.Total)
	}
	party := findByTitle(t, all.Items, "二零二三年家庭聚会")
	var d detail
	admin.must("items.get", map[string]any{"id": party.ID}, &d)
	if d.Overview != "隐私简介内容" || d.MetaSource != "nfo" || len(d.Tags["actor"]) != 2 || d.Tags["genre"][0] != "家庭" {
		t.Fatalf("NFO 没有正确读取: %+v", d)
	}
	if d.Poster == "" || d.Thumb == "" || !d.HasTrickplay || d.Duration < 23 {
		t.Fatalf("海报 / 缩略图 / 预览图 / 时长缺失: poster=%q thumb=%q trick=%v dur=%v", d.Poster, d.Thumb, d.HasTrickplay, d.Duration)
	}
	if len(d.Subtitles) != 1 || !strings.Contains(d.Subtitles[0].Label, "外挂") {
		t.Fatalf("外挂字幕没有识别: %+v", d.Subtitles)
	}

	javItem := findByTitle(t, all.Items, "番号测试标题")
	var jd detail
	admin.must("items.get", map[string]any{"id": javItem.ID}, &jd)
	if jd.Code != "ABC-123" || jd.Director != "导演甲" || jd.Series != "系列名" || jd.ReleaseDate != "2024-05-01" ||
		len(jd.Tags["actor"]) != 2 || jd.Poster == "" || jd.Backdrop == "" || !jd.Restricted {
		t.Fatalf("MetaTube 刮削结果不对: %+v", jd)
	}
	matrix := findByTitle(t, all.Items, "黑客帝国")
	var md detail
	admin.must("items.get", map[string]any{"id": matrix.ID}, &md)
	if md.Director != "沃卓斯基" || md.Studio != "华纳" || md.Poster == "" {
		t.Fatalf("TMDB 刮削结果不对: %+v", md)
	}

	// 文件夹浏览
	var root listResult
	admin.must("items.list", map[string]any{"libraryId": homeLib.ID, "browse": true, "folder": ""}, &root)
	if len(root.Items) != 1 || len(root.Folders) != 1 || root.Folders[0].Path != "旅行" || root.Folders[0].Count != 2 {
		t.Fatalf("文件夹浏览不对: %+v", root)
	}
	// 搜索：标题、演员、番号
	for q, want := range map[string]int{"家庭": 1, "爷爷": 1, "abc-123": 1, "海": 1, "不存在的词": 0} {
		var r listResult
		admin.must("items.list", map[string]any{"libraryId": 0, "q": q}, &r)
		if r.Total != want {
			t.Fatalf("搜索 %q 期望 %d 个，实际 %d", q, want, r.Total)
		}
	}

	// ---- 播放：视频、字幕、预览图都经本地客户端解密 ----
	var play playResult
	admin.must("play.open", map[string]any{"id": party.ID}, &play)
	orig, _ := os.ReadFile(filepath.Join(home, "家庭聚会.mp4"))
	st, body, hdr := admin.get(play.Video, "")
	if st != 200 || !bytes.Equal(body, orig) || hdr.Get("Cache-Control") != "no-store" {
		t.Fatalf("视频解密结果与原文件不一致: status=%d len=%d/%d", st, len(body), len(orig))
	}
	st, body, _ = admin.get(play.Video, "bytes=1000-1999")
	if st != 206 || !bytes.Equal(body, orig[1000:2000]) {
		t.Fatalf("Range 请求结果不对: %d", st)
	}
	// 另一个浏览器即使拿到了视频地址，也取不到内容（解密密钥和缓存都属于管理员的会话）
	if st, _, _ := admin.sameClient().get(play.Video, "bytes=0-99"); st != 401 {
		t.Fatalf("没有会话的浏览器不应取到视频，实际 %d", st)
	}
	_, sub, _ := admin.get(play.Subtitles[0].Src, "")
	if !strings.HasPrefix(string(sub), "WEBVTT") || !strings.Contains(string(sub), "生日快乐字幕") {
		t.Fatalf("字幕（GBK 编码）转换失败: %q", sub)
	}
	if play.Trickplay == nil || len(play.Trickplay.Sprites) == 0 || play.Trickplay.Count < 2 {
		t.Fatalf("预览图信息缺失: %+v", play.Trickplay)
	}
	_, sprite, _ := admin.get(play.Trickplay.Sprites[0], "")
	if len(sprite) < 3 || sprite[0] != 0xFF || sprite[1] != 0xD8 {
		t.Fatal("预览图不是 JPEG")
	}
	_, poster, _ := admin.get(d.Poster, "")
	if len(poster) < 3 || poster[0] != 0xFF || poster[1] != 0xD8 {
		t.Fatal("海报不是 JPEG")
	}
	// 同文件夹的“下一个”
	var pj playResult
	sea := findByTitle(t, all.Items, "海边")
	admin.must("play.open", map[string]any{"id": findByTitle(t, all.Items, "山顶").ID}, &pj)
	if pj.Next == nil || pj.Next.ID != sea.ID {
		t.Fatalf("同文件夹的下一个视频不对: %+v", pj.Next)
	}

	// ---- 观看进度与记录 ----
	admin.must("play.report", map[string]any{"id": party.ID, "psid": "s1", "position": 2, "duration": 24}, nil)
	time.Sleep(1100 * time.Millisecond)
	admin.must("play.report", map[string]any{"id": party.ID, "psid": "s1", "position": 8, "duration": 24}, nil)
	var homeData struct {
		Continue []card `json:"continue"`
		Latest   []struct {
			Library struct{ ID int64 } `json:"library"`
		} `json:"latest"`
		Libraries []struct {
			ID     int64 `json:"id"`
			Locked bool  `json:"locked"`
		} `json:"libraries"`
	}
	admin.must("home.get", nil, &homeData)
	if len(homeData.Continue) != 1 || homeData.Continue[0].ID != party.ID || homeData.Continue[0].Position != 8 {
		t.Fatalf("继续观看不对: %+v", homeData.Continue)
	}
	admin.must("play.report", map[string]any{"id": party.ID, "psid": "s1", "position": 23, "duration": 24}, nil)
	admin.must("play.report", map[string]any{"id": party.ID, "psid": "s1", "position": 23.5, "duration": 24}, nil) // 看完后继续上报不应重复计数
	admin.must("items.get", map[string]any{"id": party.ID}, &d)
	if !d.Played || d.PlayCount != 1 || d.Position != 0 {
		t.Fatalf("看完后应标记已看、计数 1: played=%v count=%d pos=%v", d.Played, d.PlayCount, d.Position)
	}
	admin.must("home.get", nil, &homeData)
	if len(homeData.Continue) != 0 {
		t.Fatal("看完的视频不应出现在继续观看")
	}
	var hist struct {
		Items []struct {
			card
			Watched float64 `json:"watched"`
		} `json:"items"`
		Total int `json:"total"`
	}
	admin.must("history.list", nil, &hist)
	if hist.Total != 1 || hist.Items[0].Watched <= 0 || hist.Items[0].Watched > 3 {
		t.Fatalf("观看记录不对（拖动不应计入观看时长）: %+v", hist)
	}
	admin.must("items.setPlayed", map[string]any{"id": party.ID, "played": false}, nil)
	var unplayed listResult
	admin.must("items.list", map[string]any{"libraryId": homeLib.ID, "played": false}, &unplayed)
	if unplayed.Total != 3 {
		t.Fatalf("未看筛选不对: %d", unplayed.Total)
	}
	var stats struct {
		Users []struct {
			Plays int `json:"plays"`
		} `json:"users"`
		ItemCount int `json:"itemCount"`
	}
	admin.must("admin.stats", map[string]any{"days": 7}, &stats)
	if stats.ItemCount != 5 || stats.Users[0].Plays != 1 {
		t.Fatalf("统计不对: %+v", stats)
	}

	// ---- 元数据编辑与锁定 ----
	admin.must("items.update", map[string]any{"id": javItem.ID, "fields": []string{"title"}, "meta": map[string]any{"title": "我改的标题"}}, nil)
	admin.must("items.rescrape", map[string]any{"id": javItem.ID, "replaceImages": false}, nil)
	admin.must("items.get", map[string]any{"id": javItem.ID}, &jd)
	if jd.Title != "我改的标题" || len(jd.Locked) != 1 {
		t.Fatalf("手动修改的字段被刮削覆盖了: %q %v", jd.Title, jd.Locked)
	}
	admin.must("items.unlock", map[string]any{"id": javItem.ID}, nil)
	admin.must("items.rescrape", map[string]any{"id": javItem.ID}, nil)
	admin.must("items.get", map[string]any{"id": javItem.ID}, &jd)
	if jd.Title != "番号测试标题" {
		t.Fatalf("解除锁定后应恢复刮削标题: %q", jd.Title)
	}
	admin.expectCode("items.rescrape", map[string]any{"id": javItem.ID, "query": "NOPE-999"}, "not_found")

	// 截图作为海报
	oldPoster := d.Poster
	admin.must("items.image", map[string]any{"id": party.ID, "kind": "poster", "frameAt": 5.0}, nil)
	admin.must("items.get", map[string]any{"id": party.ID}, &d)
	if d.Poster == oldPoster || d.Poster == "" {
		t.Fatal("截图设置海报没有生效")
	}
	admin.must("items.image", map[string]any{"id": party.ID, "kind": "backdrop", "data": base64.StdEncoding.EncodeToString(jpegBytes(rgba(1, 2, 3), 640, 360))}, nil)
	admin.expectCode("items.image", map[string]any{"id": party.ID, "kind": "backdrop", "data": base64.StdEncoding.EncodeToString([]byte("not image"))}, "bad_request")

	// ---- 权限：普通用户只能看到授权的媒体库 ----
	admin.must("admin.users.create", map[string]any{"username": "kid", "password": "kid-password", "libraries": []int64{homeLib.ID}}, nil)
	kid := e.newClient()
	kid.must("auth.login", map[string]string{"username": "kid", "password": "kid-password"}, nil)
	var kidAll listResult
	kid.must("items.list", map[string]any{"libraryId": 0}, &kidAll)
	if kidAll.Total != 3 {
		t.Fatalf("普通用户应只能看到 3 个视频，实际 %d", kidAll.Total)
	}
	kid.expectCode("items.get", map[string]any{"id": javItem.ID}, "not_found")
	kid.expectCode("play.open", map[string]any{"id": javItem.ID}, "not_found")
	kid.expectCode("items.list", map[string]any{"libraryId": javLib.ID}, "not_found")
	var kd detail
	kid.must("items.get", map[string]any{"id": party.ID}, &kd)
	if kd.Path != "" {
		t.Fatal("普通用户不应看到文件路径")
	}

	// 普通用户直接拿令牌去取未授权的密文也会被拒绝
	javBlob := blobIDOf(t, admin, javItem.ID)
	kidTok := rawBlobToken(t, e, "kid", "kid-password")
	if code := fetchBlob(t, e, javBlob, kidTok); code != 403 {
		t.Fatalf("越权访问密文应返回 403，实际 %d", code)
	}

	// ---- PIN 锁定受限内容 ----
	admin.expectCode("auth.setPin", map[string]any{"password": "wrong", "pin": "1234"}, "unauthorized")
	admin.expectCode("auth.setPin", map[string]any{"password": adminPass, "pin": "12ab"}, "bad_request")
	admin.must("auth.setPin", map[string]any{"password": adminPass, "pin": "2468"}, nil)
	admin.must("home.get", nil, &homeData)
	locked := false
	for _, l := range homeData.Libraries {
		if l.ID == javLib.ID {
			locked = l.Locked
		}
	}
	if !locked {
		t.Fatal("设置 PIN 后受限库应处于锁定状态")
	}
	for _, row := range homeData.Latest {
		if row.Library.ID == javLib.ID {
			t.Fatal("锁定状态下首页不应出现受限库内容")
		}
	}
	admin.expectCode("items.get", map[string]any{"id": javItem.ID}, "locked")
	var lockedSearch listResult
	admin.must("items.list", map[string]any{"libraryId": 0, "q": "ABC"}, &lockedSearch)
	if lockedSearch.Total != 0 {
		t.Fatal("锁定状态下搜索不应搜到受限内容")
	}
	adminTok := rawBlobToken(t, e, "admin", adminPass)
	if code := fetchBlob(t, e, javBlob, adminTok); code != 403 {
		t.Fatalf("未解锁的令牌访问受限密文应返回 403，实际 %d", code)
	}
	admin.expectCode("auth.unlock", map[string]any{"pin": "0000"}, "unauthorized")
	admin.must("auth.unlock", map[string]any{"pin": "2468"}, nil)
	admin.must("items.get", map[string]any{"id": javItem.ID}, &jd)
	var jplay playResult
	admin.must("play.open", map[string]any{"id": javItem.ID}, &jplay)
	if st, _, _ := admin.get(jplay.Video, "bytes=0-99"); st != 206 {
		t.Fatalf("解锁后应能播放受限视频: %d", st)
	}
	admin.must("auth.lock", nil, nil)
	admin.expectCode("items.get", map[string]any{"id": javItem.ID}, "locked")

	// ---- CDN 视角：任何地方都不应出现明文 ----
	secrets := []string{"二零二三年家庭聚会", "家庭聚会", "隐私简介内容", "爷爷", "生日快乐字幕", "番号测试标题", "ABC-123", "演员甲",
		"黑客帝国", adminPass, "kid-password", "2468", "media.list", "items.list", "play.open", "auth.login", "WEBVTT", home}
	for _, s := range secrets {
		if e.cdn.contains([]byte(s)) {
			t.Fatalf("CDN 能看到明文: %q", s)
		}
	}
	if e.cdn.contains(orig[4096:4160]) {
		t.Fatal("CDN 能看到视频原始字节")
	}
	if e.cdn.contains(poster[100:164]) {
		t.Fatal("CDN 能看到图片原始字节")
	}
}

func rgba(r, g, b uint8) color.RGBA { return color.RGBA{r, g, b, 255} }

// blobIDOf 通过管理员的 play.open 取得视频的对象 ID（从本地 URL /b/{id} 中提取）。
func blobIDOf(t *testing.T, u *user, id int64) string {
	var p playResult
	u.must("play.open", map[string]any{"id": id}, &p)
	return strings.TrimPrefix(p.Video, "/b/")
}

// rawSession 直接用协议层与服务端建立会话并登录（绕过本地客户端，模拟能拿到令牌的攻击者）。
func rawSession(t *testing.T, e *env) (func(m string, p any) api.Response, *proto.Session) {
	hs, _ := proto.NewClientHandshake()
	resp, err := http.Post(e.upstream.URL+"/h", "application/octet-stream", bytes.NewReader(hs.Hello()))
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	sess, err := hs.Finish(sh, e.srv.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	send := func(m string, p any) api.Response {
		n++
		pb, _ := json.Marshal(p)
		req, _ := json.Marshal(api.Request{TS: time.Now().UnixMilli(), RID: fmt.Sprintf("r%d-%d", n, time.Now().UnixNano()), M: m, P: pb})
		msg, _ := sess.Seal(req)
		resp, err := http.Post(e.upstream.URL+"/x", "application/octet-stream", bytes.NewReader(msg))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		pt, err := sess.Open(b)
		if err != nil {
			t.Fatalf("响应解密失败: %d", resp.StatusCode)
		}
		var r api.Response
		json.Unmarshal(pt, &r)
		return r
	}
	return send, sess
}

func rawBlobToken(t *testing.T, e *env, name, pass string) string {
	send, _ := rawSession(t, e)
	if r := send("auth.login", map[string]string{"username": name, "password": pass}); !r.OK {
		t.Fatalf("登录失败: %s", r.Err)
	}
	r := send("auth.blobToken", nil)
	var bt api.BlobToken
	json.Unmarshal(r.Data, &bt)
	return bt.Token
}

func fetchBlob(t *testing.T, e *env, id, tok string) int {
	req, _ := http.NewRequest(http.MethodGet, e.upstream.URL+"/m/"+id, nil)
	req.Header.Set("Range", "bytes=0-99")
	req.Header.Set(api.TokenHeader, tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestProtocolSecurity：重放、过期时间戳、令牌、会话重建、本地客户端防护、公钥固定。
func TestProtocolSecurity(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin()

	send, sess := rawSession(t, e)
	req, _ := json.Marshal(api.Request{TS: time.Now().UnixMilli(), RID: "dup", M: "sys.ping"})
	msg, _ := sess.Seal(req)
	post := func(b []byte) api.Response {
		resp, err := http.Post(e.upstream.URL+"/x", "application/octet-stream", bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		rb, _ := io.ReadAll(resp.Body)
		pt, err := sess.Open(rb)
		if err != nil {
			t.Fatal(err)
		}
		var r api.Response
		json.Unmarshal(pt, &r)
		return r
	}
	if r := post(msg); !r.OK {
		t.Fatal("首次请求失败")
	}
	if r := post(msg); r.OK || r.Code != api.CodeReplay {
		t.Fatalf("重放未被拒绝: %+v", r)
	}
	old, _ := json.Marshal(api.Request{TS: time.Now().Add(-5 * time.Minute).UnixMilli(), RID: "old", M: "sys.ping"})
	m2, _ := sess.Seal(old)
	if r := post(m2); r.OK || r.Code != api.CodeClockSkew {
		t.Fatalf("过期时间戳未被拒绝: %+v", r)
	}
	if r := send("home.get", nil); r.OK || r.Code != api.CodeUnauthorized {
		t.Fatal("未登录的会话不应能访问业务接口")
	}
	// 篡改后的消息
	bad, _ := sess.Seal(req)
	bad[len(bad)-5] ^= 1
	resp, err := http.Post(e.upstream.URL+"/x", "application/octet-stream", bytes.NewReader(bad))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatalf("篡改的消息应返回 400，实际 %d", resp.StatusCode)
	}

	// 无效令牌
	for _, tok := range []string{"", "garbage", base64.RawURLEncoding.EncodeToString(make([]byte, 33))} {
		req, _ := http.NewRequest(http.MethodGet, e.upstream.URL+"/m/anything", nil)
		req.Header.Set(api.TokenHeader, tok)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 404 && resp.StatusCode != 403 {
			t.Fatalf("无效请求应被拒绝，实际 %d", resp.StatusCode)
		}
	}

	// 服务端丢弃会话（重启 / 过期）后客户端自动重新握手并恢复登录
	e.srv.DropSessions()
	admin.must("home.get", nil, nil)

	// 本地客户端的跨站防护
	check := func(name string, mod func(*http.Request), want int) {
		req, _ := http.NewRequest(http.MethodPost, admin.local.URL+"/api/rpc", strings.NewReader(`{"m":"sys.ping"}`))
		req.Header.Set("X-VF-Local", "1")
		mod(req)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s: 期望 %d，实际 %d", name, want, resp.StatusCode)
		}
	}
	check("正常请求", func(r *http.Request) {}, 200)
	check("缺少本地请求头", func(r *http.Request) { r.Header.Del("X-VF-Local") }, 403)
	check("跨站请求", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, 403)
	check("反向代理（域名 Host）", func(r *http.Request) { r.Host = "media.example.com" }, 200)

	// 每个浏览器独立登录：管理员已经登录，同一个客户端上的另一个浏览器（另一台设备、反向代理进来的访客）仍然是未登录状态
	stranger := admin.sameClient()
	stranger.expectCode("home.get", nil, "unauthorized")
	stranger.expectCode("admin.users.list", nil, "unauthorized")
	var st struct {
		Authed bool `json:"authed"`
	}
	{
		req, _ := http.NewRequest(http.MethodGet, admin.local.URL+"/api/status", nil)
		req.Header.Set("X-VF-Local", "1")
		resp, err := stranger.hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var r struct {
			Data json.RawMessage `json:"data"`
		}
		json.NewDecoder(resp.Body).Decode(&r)
		resp.Body.Close()
		json.Unmarshal(r.Data, &st)
		if st.Authed {
			t.Fatal("另一个浏览器不应继承别人的登录状态")
		}
	}
	// 陌生浏览器登录自己的账号后，退出也不会影响管理员
	stranger.expectCode("auth.login", map[string]string{"username": "admin", "password": "wrong-password"}, "unauthorized")
	admin.must("home.get", nil, nil)

	// 公钥固定：首次信任会提示，之后沿用；错误的公钥会被拒绝
	dir := t.TempDir()
	c1, err := client.New(e.ctx, client.Config{Server: e.upstream.URL, DataDir: dir})
	if err != nil || c1.Warning == "" || c1.ServerFingerprint() != proto.Fingerprint(e.srv.PublicKey()) {
		t.Fatal("首次信任应提示并记住正确的公钥")
	}
	c2, err := client.New(e.ctx, client.Config{Server: e.upstream.URL, DataDir: dir})
	if err != nil || c2.Warning != "" {
		t.Fatal("第二次应直接使用记住的公钥")
	}
	fake := make([]byte, 32)
	fake[0] = 9
	c3, _ := client.New(e.ctx, client.Config{Server: e.upstream.URL, DataDir: t.TempDir(), ServerKey: base64.StdEncoding.EncodeToString(fake)})
	l3 := httptest.NewServer(c3.Handler())
	defer l3.Close()
	u3 := newUser(t, c3, l3)
	r := u3.call("sys.info", nil)
	if r.OK || !strings.Contains(r.Err, "签名校验") {
		t.Fatalf("公钥不匹配时应拒绝连接: %+v", r)
	}
}

// TestWatchAndRemoval：目录监听、文件删除、硬盘未挂载时不误删。
func TestWatchAndRemoval(t *testing.T) {
	if !hasFFmpeg() {
		t.Skip("需要 ffmpeg")
	}
	e := newEnv(t)
	dir := filepath.Join(e.mediaDir, "videos")
	makeVideo(t, filepath.Join(dir, "a.mp4"), 3, 300)
	admin := e.setupAdmin()
	var lib struct {
		ID int64 `json:"id"`
	}
	admin.must("admin.libs.create", map[string]any{"name": "测试", "kind": "home", "paths": []string{dir}}, &lib)
	e.waitIdle()

	count := func() int {
		var r listResult
		admin.must("items.list", map[string]any{"libraryId": lib.ID}, &r)
		return r.Total
	}
	if count() != 1 {
		t.Fatal("初始扫描应有 1 个视频")
	}
	// 新增文件：目录监听在防抖后自动扫描
	tmp := filepath.Join(e.mediaDir, "b-staging.mp4") // 先在库目录外生成，再移入
	makeVideo(t, tmp, 3, 400)
	os.Rename(tmp, filepath.Join(dir, "b.mp4"))
	deadline := time.Now().Add(30 * time.Second)
	for count() != 2 && time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
	}
	if count() != 2 {
		t.Fatal("目录监听没有发现新文件")
	}
	e.waitIdle()

	// 删除文件后扫描，条目被移除
	os.Remove(filepath.Join(dir, "a.mp4"))
	admin.must("admin.libs.scan", map[string]any{"id": lib.ID}, nil)
	e.waitIdle()
	if count() != 1 {
		t.Fatalf("删除文件后应剩 1 个，实际 %d", count())
	}

	// 整个目录不可访问（硬盘未挂载）：不应清空媒体库
	os.Rename(dir, dir+"-offline")
	admin.must("admin.libs.scan", map[string]any{"id": lib.ID}, nil)
	e.waitIdle()
	if count() != 1 {
		t.Fatalf("目录不可访问时不应删除条目，实际剩 %d", count())
	}
}

// TestBackupRestore：加密备份与恢复。
func TestBackupRestore(t *testing.T) {
	e := newEnv(t)
	admin := e.setupAdmin()
	admin.must("admin.users.create", map[string]any{"username": "bob", "password": "bob-password"}, nil)
	os.MkdirAll(filepath.Join(e.dataDir, "assets", "ab"), 0o755)
	os.WriteFile(filepath.Join(e.dataDir, "assets", "ab", "x.jpg"), []byte("asset-data"), 0o644)

	out := filepath.Join(t.TempDir(), "b.vfbak")
	if err := backup.Create(e.ctx, e.dataDir, out, "backup-pass-1", true); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(out)
	for _, s := range []string{"bob", "asset-data", "argon2id", "SQLite"} {
		if bytes.Contains(raw, []byte(s)) {
			t.Fatalf("备份文件中出现明文 %q", s)
		}
	}
	dst := t.TempDir()
	if err := backup.Restore(out, dst, "wrong-pass", false); err != backup.ErrBadPass {
		t.Fatalf("错误口令应失败: %v", err)
	}
	trunc := filepath.Join(t.TempDir(), "t.vfbak")
	os.WriteFile(trunc, raw[:len(raw)-20], 0o600)
	if err := backup.Restore(trunc, dst, "backup-pass-1", false); err == nil {
		t.Fatal("截断的备份应失败")
	}
	if err := backup.Restore(out, dst, "backup-pass-1", false); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"keys/identity.key", "keys/media.key"} {
		a, _ := os.ReadFile(filepath.Join(e.dataDir, f))
		b, _ := os.ReadFile(filepath.Join(dst, f))
		if !bytes.Equal(a, b) {
			t.Fatalf("%s 恢复后不一致", f)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dst, "assets", "ab", "x.jpg")); string(b) != "asset-data" {
		t.Fatal("资源文件没有恢复")
	}
	if err := backup.Restore(out, dst, "backup-pass-1", false); err == nil {
		t.Fatal("目标已有密钥时不加 force 应拒绝")
	}
	// 用恢复的数据目录启动服务端：公钥不变，原有用户可以登录
	srv2, err := server.New(e.ctx, server.Config{DataDir: dst})
	if err != nil {
		t.Fatal(err)
	}
	defer srv2.Close()
	if !bytes.Equal(srv2.PublicKey(), e.srv.PublicKey()) {
		t.Fatal("恢复后服务端公钥变了")
	}
	if srv2.SetupCode() != "" {
		t.Fatal("恢复后不应再要求初始化")
	}
	up2 := httptest.NewServer(srv2.Handler())
	defer up2.Close()
	c, err := client.New(e.ctx, client.Config{Server: up2.URL, DataDir: t.TempDir(), ServerKey: base64.StdEncoding.EncodeToString(srv2.PublicKey())})
	if err != nil {
		t.Fatal(err)
	}
	l := httptest.NewServer(c.Handler())
	defer l.Close()
	u := newUser(t, c, l)
	u.must("auth.login", map[string]string{"username": "bob", "password": "bob-password"}, nil)
}
