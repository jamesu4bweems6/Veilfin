package e2e

import (
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"veilfin/internal/api"
)

// 针对安全审查发现的问题的回归测试。

func writeRandom(t *testing.T, path string, n int) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	b := make([]byte, n)
	rand.Read(b)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReviewRegressions(t *testing.T) {
	e := newEnv(t)
	pub := filepath.Join(e.mediaDir, "public")
	priv := filepath.Join(e.mediaDir, "private")
	writeRandom(t, filepath.Join(pub, "公开.mp4"), 300_000)
	writeRandom(t, filepath.Join(priv, "私密.mp4"), 300_000)

	admin := e.setupAdmin()
	var pubLib, privLib struct {
		ID int64 `json:"id"`
	}
	admin.must("admin.libs.create", map[string]any{"name": "公开", "kind": "home", "paths": []string{pub}}, &pubLib)
	admin.must("admin.libs.create", map[string]any{"name": "私密", "kind": "home", "restricted": true, "paths": []string{priv}}, &privLib)
	e.waitIdle()

	// 1. 目录重叠被拒绝（否则受限文件会留在公开库里）
	admin.expectCode("admin.libs.create", map[string]any{"name": "重叠", "kind": "home", "restricted": true, "paths": []string{e.mediaDir}}, "bad_request")
	sub := filepath.Join(pub, "子目录")
	os.MkdirAll(sub, 0o755)
	r := admin.call("admin.libs.create", map[string]any{"name": "重叠2", "kind": "home", "paths": []string{sub}})
	if r.OK || !strings.Contains(r.Err, "重叠") {
		t.Fatalf("子目录与已有媒体库重叠应被拒绝: %+v", r)
	}
	admin.expectCode("admin.libs.create", map[string]any{"name": "自身重叠", "kind": "home", "paths": []string{filepath.Join(e.mediaDir, "x"), filepath.Join(e.mediaDir, "x", "y")}}, "bad_request")

	var all listResult
	admin.must("items.list", map[string]any{"libraryId": 0}, &all)
	privItem := findByTitle(t, all.Items, "私密")
	privBlob := blobIDOf(t, admin, privItem.ID)

	// 管理员设置 PIN，另建一个也设了 PIN 的普通用户 B
	admin.must("auth.setPin", map[string]any{"password": adminPass, "pin": "2468"}, nil)
	admin.must("admin.users.create", map[string]any{"username": "bob", "password": "bob-password", "libraries": []int64{pubLib.ID, privLib.ID}}, nil)
	bob := e.newClient()
	bob.must("auth.login", map[string]string{"username": "bob", "password": "bob-password"}, nil)
	bob.must("auth.setPin", map[string]any{"password": "bob-password", "pin": "9753"}, nil)

	// 2. auth.resume 不能继承上一个身份的解锁状态
	send, _ := rawSession(t, e)
	lr := send("auth.login", map[string]string{"username": "admin", "password": adminPass})
	if !lr.OK {
		t.Fatal(lr.Err)
	}
	if r := send("auth.unlock", map[string]string{"pin": "2468"}); !r.OK {
		t.Fatal(r.Err)
	}
	bobSend, _ := rawSession(t, e)
	bl := bobSend("auth.login", map[string]string{"username": "bob", "password": "bob-password"})
	var bobLogin struct {
		Token string `json:"token"`
	}
	json.Unmarshal(bl.Data, &bobLogin)
	res := send("auth.resume", map[string]string{"token": bobLogin.Token})
	var me struct {
		Username string `json:"username"`
		Unlocked bool   `json:"unlocked"`
	}
	json.Unmarshal(res.Data, &me)
	if !res.OK || me.Username != "bob" || me.Unlocked {
		t.Fatalf("resume 成 bob 后不应继承 admin 的解锁状态: %+v", me)
	}
	if r := send("items.get", map[string]int64{"id": privItem.ID}); r.OK || r.Code != api.CodeLocked {
		t.Fatalf("resume 后应处于锁定状态: %+v", r)
	}

	// 3. PIN 错误次数持久化：每次换新会话也绕不过锁定
	for i := 0; i < 5; i++ {
		s, _ := rawSession(t, e)
		s("auth.login", map[string]string{"username": "bob", "password": "bob-password"})
		s("auth.unlock", map[string]string{"pin": "0000"})
	}
	s6, _ := rawSession(t, e)
	s6("auth.login", map[string]string{"username": "bob", "password": "bob-password"})
	if r := s6("auth.unlock", map[string]string{"pin": "9753"}); r.OK || r.Code != api.CodeTooMany {
		t.Fatalf("PIN 连续错误 5 次后，即使换会话、输入正确 PIN 也应被锁定: %+v", r)
	}

	// 4. 锁定后已签发的解锁令牌立即失效
	tokSend, _ := rawSession(t, e)
	tokSend("auth.login", map[string]string{"username": "admin", "password": adminPass})
	tokSend("auth.unlock", map[string]string{"pin": "2468"})
	var bt api.BlobToken
	json.Unmarshal(tokSend("auth.blobToken", nil).Data, &bt)
	if code := fetchBlob(t, e, privBlob, bt.Token); code != 206 {
		t.Fatalf("解锁后的令牌应能取受限密文，实际 %d", code)
	}
	tokSend("auth.lock", nil)
	if code := fetchBlob(t, e, privBlob, bt.Token); code != 403 {
		t.Fatalf("锁定后旧令牌应失效，实际 %d", code)
	}

	// 锁定状态下连播放进度也不能上报到受限条目
	admin.expectCode("play.report", map[string]any{"id": privItem.ID, "psid": "x", "position": 1, "duration": 0}, api.CodeLocked)
}

func TestStatsHideRestrictedWhenLocked(t *testing.T) {
	e := newEnv(t)
	priv := filepath.Join(e.mediaDir, "private")
	writeRandom(t, filepath.Join(priv, "隐私片名.mp4"), 200_000)
	admin := e.setupAdmin()
	admin.must("admin.libs.create", map[string]any{"name": "私密", "kind": "home", "restricted": true, "paths": []string{priv}}, nil)
	e.waitIdle()
	var all listResult
	admin.must("items.list", map[string]any{"libraryId": 0}, &all)
	id := all.Items[0].ID
	admin.must("play.report", map[string]any{"id": id, "psid": "p1", "position": 1, "duration": 100}, nil)

	admin.must("auth.setPin", map[string]any{"password": adminPass, "pin": "2468"}, nil)
	var st struct {
		Top []struct {
			Title string `json:"title"`
		} `json:"top"`
	}
	admin.must("admin.stats", nil, &st)
	if len(st.Top) != 0 {
		t.Fatalf("锁定状态下统计不应显示受限片名: %+v", st.Top)
	}
	admin.must("auth.unlock", map[string]string{"pin": "2468"}, nil)
	admin.must("admin.stats", nil, &st)
	if len(st.Top) != 1 || st.Top[0].Title != "隐私片名" {
		t.Fatalf("解锁后统计应显示: %+v", st.Top)
	}
}

func TestScanKeepsFilesBeingCopied(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Join(e.mediaDir, "v")
	f := filepath.Join(dir, "a.mp4")
	writeRandom(t, f, 100_000)
	admin := e.setupAdmin()
	var lib struct {
		ID int64 `json:"id"`
	}
	admin.must("admin.libs.create", map[string]any{"name": "v", "kind": "home", "paths": []string{dir}}, &lib)
	e.waitIdle()
	// 文件被覆盖复制、暂时大小为 0：条目和观看记录不应被删除
	var all listResult
	admin.must("items.list", map[string]any{"libraryId": lib.ID}, &all)
	admin.must("play.report", map[string]any{"id": all.Items[0].ID, "psid": "p", "position": 3, "duration": 100}, nil)
	os.WriteFile(f, nil, 0o644)
	admin.must("admin.libs.scan", map[string]any{"id": lib.ID}, nil)
	e.waitIdle()
	admin.must("items.list", map[string]any{"libraryId": lib.ID}, &all)
	if all.Total != 1 || all.Items[0].Position != 3 {
		t.Fatalf("正在复制的文件不应导致条目和进度被删除: %+v", all)
	}
}
