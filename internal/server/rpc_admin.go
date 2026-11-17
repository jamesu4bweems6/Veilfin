package server

import (
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"veilfin/internal/api"
	"veilfin/internal/auth"
	"veilfin/internal/db"
	"veilfin/internal/library"
	"veilfin/internal/scraper"
	"veilfin/internal/tasks"
)

func init() {
	register("admin.users.list", admin, rpcUsersList)
	register("admin.users.create", admin, rpcUsersCreate)
	register("admin.users.update", admin, rpcUsersUpdate)
	register("admin.users.delete", admin, rpcUsersDelete)
	register("admin.libs.list", admin, rpcLibsList)
	register("admin.libs.create", admin, rpcLibsSave)
	register("admin.libs.update", admin, rpcLibsSave)
	register("admin.libs.delete", admin, rpcLibsDelete)
	register("admin.libs.scan", admin, rpcLibsScan)
	register("admin.fs.list", admin, rpcFSList)
	register("admin.tasks", admin, func(s *Server, c *call) (any, error) {
		return []tasks.Status{s.Lib.Main.Status(), s.Lib.Heavy.Status()}, nil
	})
	register("admin.settings.get", admin, func(s *Server, c *call) (any, error) {
		st := s.Lib.Settings(c.ctx)
		return map[string]any{"settings": st, "ffmpeg": s.Lib.FF != nil}, nil
	})
	register("admin.settings.set", admin, rpcSettingsSet)
	register("admin.stats", admin, rpcStats)
	register("items.update", admin, rpcItemUpdate)
	register("items.unlock", admin, func(s *Server, c *call) (any, error) {
		var p struct {
			ID int64 `json:"id"`
		}
		if err := c.bind(&p); err != nil {
			return nil, err
		}
		return struct{}{}, s.DB.UnlockAll(c.ctx, p.ID)
	})
	register("items.image", admin, rpcItemImage)
	register("items.rescrape", admin, rpcItemRescrape)
	register("items.refresh", admin, rpcItemRefresh)
	register("items.delete", admin, rpcItemDelete)
}

// ---- 用户 ----

type userOut struct {
	*db.User
	HasPin    bool    `json:"hasPin"`
	Libraries []int64 `json:"libraries"`
}

func rpcUsersList(s *Server, c *call) (any, error) {
	users, err := s.DB.ListUsers(c.ctx)
	if err != nil {
		return nil, err
	}
	out := make([]userOut, 0, len(users))
	for _, u := range users {
		libs, err := s.DB.LibraryAccess(c.ctx, u.ID)
		if err != nil {
			return nil, err
		}
		if libs == nil {
			libs = []int64{}
		}
		out = append(out, userOut{User: u, HasPin: u.PinHash != "", Libraries: libs})
	}
	return out, nil
}

func rpcUsersCreate(s *Server, c *call) (any, error) {
	var p struct {
		Username  string  `json:"username"`
		Password  string  `json:"password"`
		IsAdmin   bool    `json:"isAdmin"`
		Libraries []int64 `json:"libraries"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	if err := validUsername(p.Username); err != nil {
		return nil, err
	}
	if err := auth.ValidatePassword(p.Password); err != nil {
		return nil, errf(api.CodeBadRequest, "%v", err)
	}
	if _, err := s.DB.UserByName(c.ctx, strings.TrimSpace(p.Username)); err == nil {
		return nil, errf(api.CodeBadRequest, "用户名已存在")
	}
	id, err := s.DB.CreateUser(c.ctx, strings.TrimSpace(p.Username), auth.Hash(p.Password), p.IsAdmin)
	if err != nil {
		return nil, err
	}
	if err := s.DB.SetLibraryAccess(c.ctx, id, p.Libraries); err != nil {
		return nil, err
	}
	s.invalidateAccess()
	return map[string]int64{"id": id}, nil
}

func (s *Server) adminCount(c *call, exclude int64) (int, error) {
	users, err := s.DB.ListUsers(c.ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, u := range users {
		if u.IsAdmin && !u.Disabled && u.ID != exclude {
			n++
		}
	}
	return n, nil
}

func rpcUsersUpdate(s *Server, c *call) (any, error) {
	var p struct {
		ID        int64   `json:"id"`
		IsAdmin   bool    `json:"isAdmin"`
		Disabled  bool    `json:"disabled"`
		Password  string  `json:"password"`
		ClearPin  bool    `json:"clearPin"`
		Libraries []int64 `json:"libraries"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	u, err := s.DB.UserByID(c.ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if (!p.IsAdmin || p.Disabled) && u.IsAdmin {
		n, err := s.adminCount(c, u.ID)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, errf(api.CodeBadRequest, "至少要保留一个可用的管理员")
		}
	}
	if err := s.DB.UpdateUserFlags(c.ctx, p.ID, p.IsAdmin, p.Disabled); err != nil {
		return nil, err
	}
	if p.Password != "" {
		if err := auth.ValidatePassword(p.Password); err != nil {
			return nil, errf(api.CodeBadRequest, "%v", err)
		}
		if err := s.DB.SetPassword(c.ctx, p.ID, auth.Hash(p.Password)); err != nil {
			return nil, err
		}
	}
	if p.ClearPin {
		if err := s.DB.SetPin(c.ctx, p.ID, ""); err != nil {
			return nil, err
		}
	}
	if err := s.DB.SetLibraryAccess(c.ctx, p.ID, p.Libraries); err != nil {
		return nil, err
	}
	if p.Disabled || p.Password != "" {
		s.sessions.logoutUser(p.ID)
	}
	s.invalidateAccess()
	return struct{}{}, nil
}

func rpcUsersDelete(s *Server, c *call) (any, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	if p.ID == c.u.ID {
		return nil, errf(api.CodeBadRequest, "不能删除自己")
	}
	u, err := s.DB.UserByID(c.ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if u.IsAdmin {
		if n, _ := s.adminCount(c, u.ID); n == 0 {
			return nil, errf(api.CodeBadRequest, "至少要保留一个可用的管理员")
		}
	}
	if err := s.DB.DeleteUser(c.ctx, p.ID); err != nil {
		return nil, err
	}
	s.sessions.logoutUser(p.ID)
	s.invalidateAccess()
	return struct{}{}, nil
}

// ---- 媒体库 ----

type libAdmin struct {
	*db.Library
	Count int `json:"count"`
}

func rpcLibsList(s *Server, c *call) (any, error) {
	libs, err := s.DB.ListLibraries(c.ctx)
	if err != nil {
		return nil, err
	}
	counts, err := s.DB.LibraryCounts(c.ctx)
	if err != nil {
		return nil, err
	}
	out := make([]libAdmin, 0, len(libs))
	for _, l := range libs {
		out = append(out, libAdmin{Library: l, Count: counts[l.ID]})
	}
	return out, nil
}

var libKinds = map[string]bool{"home": true, "movie": true, "jav": true, "other": true}
var scrapers = map[string]bool{"none": true, "tmdb": true, "metatube": true}

func rpcLibsSave(s *Server, c *call) (any, error) {
	var l db.Library
	if err := c.bind(&l); err != nil {
		return nil, err
	}
	l.Name = strings.TrimSpace(l.Name)
	if l.Name == "" {
		return nil, errf(api.CodeBadRequest, "请填写媒体库名称")
	}
	if !libKinds[l.Kind] {
		return nil, errf(api.CodeBadRequest, "媒体库类型无效")
	}
	if l.Scraper == "" {
		l.Scraper = "none"
	}
	if !scrapers[l.Scraper] {
		return nil, errf(api.CodeBadRequest, "刮削源无效")
	}
	var paths []string
	seen := map[string]bool{}
	for _, p := range l.Paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			return nil, errf(api.CodeBadRequest, "请使用绝对路径: %s", p)
		}
		p = filepath.Clean(p)
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			return nil, errf(api.CodeBadRequest, "目录不存在或无法访问: %s", p)
		}
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil, errf(api.CodeBadRequest, "至少需要一个媒体目录")
	}
	// 目录不能与其他媒体库（或本库的其他目录）重叠：同一个文件只能属于一个媒体库，
	// 否则受限库里的文件可能留在公开库中。
	others, err := s.DB.ListLibraries(c.ctx)
	if err != nil {
		return nil, err
	}
	for i, p := range paths {
		for j, q := range paths {
			if i != j && pathOverlap(p, q) {
				return nil, errf(api.CodeBadRequest, "目录互相包含：%s 与 %s", p, q)
			}
		}
		for _, o := range others {
			if o.ID == l.ID {
				continue
			}
			for _, q := range o.Paths {
				if pathOverlap(p, q) {
					return nil, errf(api.CodeBadRequest, "目录 %s 与媒体库「%s」的目录 %s 重叠，同一个文件只能属于一个媒体库", p, o.Name, q)
				}
			}
		}
	}
	l.Paths = paths
	if c.req.M == "admin.libs.create" {
		id, err := s.DB.CreateLibrary(c.ctx, &l)
		if err != nil {
			return nil, err
		}
		l.ID = id
	} else {
		old, err := s.DB.LibraryByID(c.ctx, l.ID)
		if err != nil {
			return nil, err
		}
		if err := s.DB.UpdateLibrary(c.ctx, &l); err != nil {
			return nil, err
		}
		if old.Scraper != l.Scraper { // 换了刮削源，重新刮削尚未刮削过的条目
			s.DB.W.ExecContext(c.ctx, "UPDATE items SET scan_state = scan_state & ~? WHERE library_id=? AND meta_source NOT IN ('nfo','json','manual')", db.StateScraped, l.ID)
		}
	}
	s.invalidateAccess()
	s.Lib.EnqueueScan(l.ID, false)
	s.Lib.RestartWatcher()
	return map[string]int64{"id": l.ID}, nil
}

// pathOverlap 判断两个目录是否相同或互为祖先 / 后代。
func pathOverlap(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		a, b = strings.ToLower(a), strings.ToLower(b)
	}
	sep := string(filepath.Separator)
	in := func(child, parent string) bool {
		return child == parent || strings.HasPrefix(child, strings.TrimSuffix(parent, sep)+sep)
	}
	return in(a, b) || in(b, a)
}

func rpcLibsDelete(s *Server, c *call) (any, error) {
	var p struct {
		ID int64 `json:"id"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	files, err := s.DB.DeleteLibrary(c.ctx, p.ID)
	if err != nil {
		return nil, err
	}
	s.Lib.RemoveAssets(files)
	s.invalidateAccess()
	s.Lib.RestartWatcher()
	return struct{}{}, nil
}

func rpcLibsScan(s *Server, c *call) (any, error) {
	var p struct {
		ID      int64 `json:"id"`
		Refresh bool  `json:"refresh"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	if p.ID == 0 {
		libs, err := s.DB.ListLibraries(c.ctx)
		if err != nil {
			return nil, err
		}
		for _, l := range libs {
			s.Lib.EnqueueScan(l.ID, p.Refresh)
		}
		return struct{}{}, nil
	}
	if _, err := s.DB.LibraryByID(c.ctx, p.ID); err != nil {
		return nil, err
	}
	s.Lib.EnqueueScan(p.ID, p.Refresh)
	return struct{}{}, nil
}

// rpcFSList 列出服务器上的目录，用于在管理后台选择媒体目录（只列目录，不列文件）。
func rpcFSList(s *Server, c *call) (any, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	type entry struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	var out []entry
	if p.Path == "" {
		if runtime.GOOS == "windows" {
			for d := 'A'; d <= 'Z'; d++ {
				root := string(d) + `:\`
				if _, err := os.Stat(root); err == nil {
					out = append(out, entry{Name: root, Path: root})
				}
			}
			return map[string]any{"path": "", "parent": "", "dirs": out}, nil
		}
		p.Path = "/"
	}
	dir := filepath.Clean(p.Path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, errf(api.CodeBadRequest, "无法读取目录: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") && !strings.HasPrefix(e.Name(), "$") {
			out = append(out, entry{Name: e.Name(), Path: filepath.Join(dir, e.Name())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	parent := filepath.Dir(dir)
	if parent == dir {
		parent = ""
	}
	return map[string]any{"path": dir, "parent": parent, "dirs": out}, nil
}

func rpcSettingsSet(s *Server, c *call) (any, error) {
	var st library.Settings
	if err := c.bind(&st); err != nil {
		return nil, err
	}
	if strings.TrimSpace(st.ServerName) == "" {
		st.ServerName = "Veilfin"
	}
	if st.ScraperProxy != "" {
		if _, err := scraper.NewHTTP(st.ScraperProxy); err != nil {
			return nil, errf(api.CodeBadRequest, "%v", err)
		}
	}
	old := s.Lib.Settings(c.ctx)
	if err := s.Lib.SaveSettings(c.ctx, st); err != nil {
		return nil, err
	}
	if old.WatchEnabled != st.WatchEnabled {
		s.Lib.RestartWatcher()
	}
	return st, nil
}

func rpcStats(s *Server, c *call) (any, error) {
	var p struct {
		Days int `json:"days"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	if p.Days <= 0 || p.Days > 365 {
		p.Days = 30
	}
	st, err := s.DB.Stats(c.ctx, s.now().Add(-time.Duration(p.Days)*24*time.Hour).Unix())
	if err != nil {
		return nil, err
	}
	if c.u.PinHash != "" && !c.ss.unlocked(s.now()) {
		libs, err := s.DB.ListLibraries(c.ctx)
		if err != nil {
			return nil, err
		}
		restricted := map[int64]bool{}
		for _, l := range libs {
			restricted[l.ID] = l.Restricted
		}
		top := st.Top[:0]
		for _, t := range st.Top {
			if !restricted[t.LibraryID] {
				top = append(top, t)
			}
		}
		st.Top = top
	}
	return st, nil
}

// ---- 元数据编辑 ----

func rpcItemUpdate(s *Server, c *call) (any, error) {
	var p struct {
		ID     int64    `json:"id"`
		Meta   db.Meta  `json:"meta"`
		Fields []string `json:"fields"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	var fields []string
	for _, f := range p.Fields {
		for _, ok := range db.MetaFields {
			if f == ok {
				fields = append(fields, f)
			}
		}
	}
	if len(fields) == 0 {
		return nil, errf(api.CodeBadRequest, "没有要修改的字段")
	}
	// 手动修改的字段会被锁定，之后重新刮削也不会覆盖
	if err := s.DB.ApplyMeta(c.ctx, p.ID, &p.Meta, fields, "manual", true, true); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

func rpcItemImage(s *Server, c *call) (any, error) {
	var p struct {
		ID      int64    `json:"id"`
		Kind    string   `json:"kind"`
		FrameAt *float64 `json:"frameAt"`
		Data    string   `json:"data"` // base64 编码的图片
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	if p.Kind != "poster" && p.Kind != "backdrop" && p.Kind != "thumb" {
		return nil, errf(api.CodeBadRequest, "图片类型无效")
	}
	if _, err := s.DB.ItemByID(c.ctx, p.ID); err != nil {
		return nil, err
	}
	var err error
	switch {
	case p.FrameAt != nil:
		err = s.Lib.SetImageFromFrame(c.ctx, p.ID, p.Kind, *p.FrameAt)
	case p.Data != "":
		data, derr := base64.StdEncoding.DecodeString(p.Data)
		if derr != nil {
			return nil, errf(api.CodeBadRequest, "图片数据格式错误")
		}
		err = s.Lib.SetImageData(c.ctx, p.ID, p.Kind, data, true)
	default:
		return nil, errf(api.CodeBadRequest, "请提供截图时间或图片数据")
	}
	if err != nil {
		return nil, errf(api.CodeBadRequest, "%v", err)
	}
	return struct{}{}, nil
}

func rpcItemRescrape(s *Server, c *call) (any, error) {
	var p struct {
		ID            int64  `json:"id"`
		Query         string `json:"query"`
		ReplaceImages bool   `json:"replaceImages"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	if err := s.Lib.Scrape(c.ctx, p.ID, strings.TrimSpace(p.Query), p.ReplaceImages); err != nil {
		if errors.Is(err, scraper.ErrNoMatch) {
			return nil, errf(api.CodeNotFound, "没有找到匹配的结果")
		}
		return nil, errf(api.CodeBadRequest, "刮削失败: %v", err)
	}
	return struct{}{}, nil
}

func rpcItemRefresh(s *Server, c *call) (any, error) {
	var p struct {
		ID        int64 `json:"id"`
		Trickplay bool  `json:"trickplay"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	if _, err := s.DB.ItemByID(c.ctx, p.ID); err != nil {
		return nil, err
	}
	s.Lib.EnqueueItem(p.ID, true)
	if p.Trickplay {
		s.Lib.EnqueueTrickplay(p.ID)
	}
	return struct{}{}, nil
}

func rpcItemDelete(s *Server, c *call) (any, error) {
	var p struct {
		ID         int64 `json:"id"`
		DeleteFile bool  `json:"deleteFile"`
	}
	if err := c.bind(&p); err != nil {
		return nil, err
	}
	it, err := s.DB.ItemByID(c.ctx, p.ID)
	if err != nil {
		return nil, err
	}
	if p.DeleteFile {
		lib, err := s.DB.LibraryByID(c.ctx, it.LibraryID)
		if err != nil {
			return nil, err
		}
		inside := false
		for _, root := range lib.Paths {
			if strings.HasPrefix(it.Path, filepath.Clean(root)+string(filepath.Separator)) {
				inside = true
			}
		}
		if !inside {
			return nil, errf(api.CodeForbidden, "文件不在媒体库目录内，拒绝删除")
		}
		if err := os.Remove(it.Path); err != nil && !os.IsNotExist(err) {
			return nil, errf(api.CodeInternal, "删除文件失败: %v", err)
		}
	}
	files, err := s.DB.DeleteItem(c.ctx, p.ID)
	if err != nil {
		return nil, err
	}
	s.Lib.RemoveAssets(files)
	return struct{}{}, nil
}
