// Package library 负责媒体库扫描和元数据处理流水线：
//
//	扫描目录 → ffprobe → 本地元数据（NFO / JSON / 图片）→ 在线刮削 → 截图 → 字幕 → 进度条预览图
package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"veilfin/internal/db"
	"veilfin/internal/imaging"
	"veilfin/internal/media"
	"veilfin/internal/mediacrypt"
	"veilfin/internal/scraper"
	"veilfin/internal/tasks"
)

// VideoExts 是支持的视频格式（浏览器可以直接播放的容器）。
var VideoExts = map[string]string{
	".mp4":  "video/mp4",
	".m4v":  "video/mp4",
	".mov":  "video/mp4",
	".webm": "video/webm",
}

var subExts = map[string]bool{".srt": true, ".ass": true, ".ssa": true, ".vtt": true}

// 扫描时跳过的目录
var skipDirs = map[string]bool{"@eaDir": true, "#recycle": true, "$RECYCLE.BIN": true, "System Volume Information": true, "lost+found": true}

// Settings 是可以在管理后台修改的系统设置。
type Settings struct {
	ServerName        string `json:"serverName"`
	TMDBKey           string `json:"tmdbKey"`
	TMDBLang          string `json:"tmdbLang"`
	MetaTubeURL       string `json:"metatubeUrl"`
	MetaTubeToken     string `json:"metatubeToken"`
	ScraperProxy      string `json:"scraperProxy"`
	ScanIntervalHours int    `json:"scanIntervalHours"`
	WatchEnabled      bool   `json:"watchEnabled"`
	TrickplayEnabled  bool   `json:"trickplayEnabled"`
}

func DefaultSettings() Settings {
	return Settings{ServerName: "Veilfin", TMDBLang: "zh-CN", ScanIntervalHours: 6, WatchEnabled: true, TrickplayEnabled: true}
}

type Service struct {
	DB       *db.DB
	FF       *media.Tools // 可能为 nil：没有 ffmpeg 时只能播放，不做截图和字幕
	AssetDir string
	Main     *tasks.Queue
	Heavy    *tasks.Queue // 预览图等耗时任务单独排队，不阻塞元数据处理

	// NewProvider 允许测试替换刮削数据源。
	NewProvider func(kind string, s Settings) (scraper.Provider, error)

	baseCtx  context.Context // 服务的生命周期（目录监听等长期任务挂在它上面，而不是某个请求上）
	mu       sync.Mutex
	settings *Settings
	watcher  *watcher
}

func New(ctx context.Context, d *db.DB, ff *media.Tools, assetDir string) *Service {
	s := &Service{
		DB:       d,
		FF:       ff,
		AssetDir: assetDir,
		Main:     tasks.New(ctx, "元数据", 2),
		Heavy:    tasks.New(ctx, "预览图", 1),
		baseCtx:  ctx,
	}
	s.NewProvider = s.defaultProvider
	return s
}

// ---- 设置 ----

func (s *Service) Settings(ctx context.Context) Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.settings != nil {
		return *s.settings
	}
	st := DefaultSettings()
	if v, err := s.DB.GetSetting(ctx, "config"); err == nil && v != "" {
		json.Unmarshal([]byte(v), &st)
	}
	s.settings = &st
	return st
}

func (s *Service) SaveSettings(ctx context.Context, st Settings) error {
	if st.ScanIntervalHours < 0 {
		st.ScanIntervalHours = 0
	}
	b, _ := json.Marshal(st)
	if err := s.DB.SetSetting(ctx, "config", string(b)); err != nil {
		return err
	}
	s.mu.Lock()
	s.settings = &st
	s.mu.Unlock()
	return nil
}

func (s *Service) defaultProvider(kind string, st Settings) (scraper.Provider, error) {
	h, err := scraper.NewHTTP(st.ScraperProxy)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "metatube":
		return &scraper.MetaTube{Base: st.MetaTubeURL, Token: st.MetaTubeToken, H: h}, nil
	case "tmdb":
		return &scraper.TMDB{Key: st.TMDBKey, Language: st.TMDBLang, H: h}, nil
	}
	return nil, fmt.Errorf("未知的刮削源: %s", kind)
}

// ---- 扫描 ----

// EnqueueScan 把媒体库扫描加入队列。refresh 为 true 时会重新读取所有条目的元数据（不覆盖手动锁定的字段）。
func (s *Service) EnqueueScan(libID int64, refresh bool) {
	key := fmt.Sprintf("scan:%d", libID)
	label := fmt.Sprintf("扫描媒体库 #%d", libID)
	if refresh {
		key += ":refresh"
		label += "（刷新元数据）"
	}
	s.Main.Add(key, label, func(ctx context.Context) error { return s.ScanLibrary(ctx, libID, refresh) })
}

// ScanLibrary 扫描媒体库目录，同步新增 / 变化 / 删除的文件，并把需要处理的条目加入队列。
func (s *Service) ScanLibrary(ctx context.Context, libID int64, refresh bool) error {
	lib, err := s.DB.LibraryByID(ctx, libID)
	if err != nil {
		return err
	}
	existing, err := s.DB.LibraryFiles(ctx, lib.ID)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var unavailable []string // 不可访问的根目录：其下的条目不删除（防止硬盘未挂载时清空媒体库）
	multi := len(lib.Paths) > 1

	for _, root := range lib.Paths {
		root = filepath.Clean(root)
		st, err := os.Stat(root)
		if err != nil || !st.IsDir() {
			log.Printf("媒体库「%s」的目录不可访问，跳过: %s", lib.Name, root)
			unavailable = append(unavailable, root)
			continue
		}
		found := 0
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				// 读不了的子目录（网络盘掉线、权限变化）：其下的条目一律保留，不当作已删除
				log.Printf("扫描时无法读取 %s: %v（其下条目保留）", path, err)
				unavailable = append(unavailable, path)
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if d.IsDir() {
				if path != root && (strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()]) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			mime, ok := VideoExts[strings.ToLower(filepath.Ext(d.Name()))]
			if !ok {
				return nil
			}
			info, err := d.Info()
			if err == nil && d.Type()&fs.ModeSymlink != 0 {
				info, err = os.Stat(path) // 符号链接取目标文件的大小和时间
			}
			if err != nil || info.Size() == 0 || !info.Mode().IsRegular() {
				// 暂时读不到或正在复制（大小为 0）的文件：已有条目保留，新文件等下次扫描
				if _, ok := existing[path]; ok {
					seen[path] = true
				}
				return nil
			}
			found++
			seen[path] = true
			mtime := info.ModTime().Unix()
			if ex, ok := existing[path]; ok {
				if ex.Size != info.Size() || ex.Mtime != mtime {
					// 内容变了：换新的对象 ID，让 CDN 上的旧缓存自然失效
					if err := s.DB.ReplaceVideoFile(ctx, ex.ID, info.Size(), mtime, mediacrypt.NewID(), mime); err != nil {
						log.Printf("更新 %s 失败: %v", path, err)
					}
				}
				return nil
			}
			it := &db.Item{
				LibraryID: lib.ID, Path: path, Folder: folderOf(root, path, multi), Size: info.Size(), Mtime: mtime,
				VideoBlob: mediacrypt.NewID(), Mime: mime, Title: initialTitle(lib.Kind, path),
			}
			it.SortTitle = db.SortKey(it.Title)
			if _, err := s.DB.InsertItem(ctx, it); err != nil {
				log.Printf("添加 %s 失败: %v", path, err)
			}
			return nil
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if found == 0 && hasUnder(existing, root) {
			log.Printf("媒体库「%s」的目录 %s 为空但之前有视频，可能未挂载，本次不删除条目", lib.Name, root)
			unavailable = append(unavailable, root)
		}
	}

	for path, ex := range existing {
		if seen[path] || underAny(path, unavailable) {
			continue
		}
		files, err := s.DB.DeleteItem(ctx, ex.ID)
		if err != nil {
			log.Printf("删除条目失败: %v", err)
			continue
		}
		s.RemoveAssets(files)
	}
	s.DB.TouchLibraryScan(ctx, lib.ID)

	need := db.StateProbed | db.StateLocalMeta | db.StateThumb | db.StateSubs
	if lib.Scraper != "" && lib.Scraper != "none" {
		need |= db.StateScraped
	}
	ids, err := s.DB.ItemsNeedingWork(ctx, lib.ID, need)
	if err != nil {
		return err
	}
	if refresh {
		all, _ := s.DB.ItemsNeedingWork(ctx, lib.ID, 1<<30) // 1<<30 这一位从不设置，等于取全部
		ids = all
	}
	pending := map[int64]bool{}
	for _, id := range ids {
		pending[id] = true
		s.EnqueueItem(id, refresh)
	}
	// 需要处理的条目会在处理完（拿到时长）后自己排队生成预览图，这里只补上其余缺预览图的
	if s.Settings(ctx).TrickplayEnabled && s.FF != nil {
		tids, _ := s.DB.ItemsNeedingWork(ctx, lib.ID, db.StateTrickplay)
		for _, id := range tids {
			if !pending[id] {
				s.EnqueueTrickplay(id)
			}
		}
	}
	return nil
}

func folderOf(root, path string, multi bool) string {
	rel, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil || rel == "." {
		rel = ""
	}
	rel = filepath.ToSlash(rel)
	if multi {
		if rel == "" {
			return filepath.Base(root)
		}
		return filepath.Base(root) + "/" + rel
	}
	return rel
}

func initialTitle(kind, path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	switch kind {
	case "movie":
		t, _ := scraper.ParseTitle(path)
		return t
	case "jav":
		if c := scraper.ParseCode(path); c.Code != "" {
			return c.Code
		}
	}
	return base
}

func underAny(path string, roots []string) bool {
	for _, r := range roots {
		if path == r || strings.HasPrefix(path, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func hasUnder(existing map[string]db.FileState, root string) bool {
	for p := range existing {
		if strings.HasPrefix(p, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// ---- 单个条目的处理流水线 ----

func (s *Service) EnqueueItem(id int64, refresh bool) {
	s.Main.Add(fmt.Sprintf("item:%d", id), fmt.Sprintf("处理条目 #%d", id), func(ctx context.Context) error {
		return s.ProcessItem(ctx, id, refresh)
	})
}

func (s *Service) EnqueueTrickplay(id int64) {
	s.Heavy.Add(fmt.Sprintf("trick:%d", id), fmt.Sprintf("生成预览图 #%d", id), func(ctx context.Context) error {
		return s.GenerateTrickplay(ctx, id)
	})
}

// ProcessItem 依次执行探测、本地元数据、刮削、截图、字幕。refresh 时重新执行所有步骤。
func (s *Service) ProcessItem(ctx context.Context, id int64, refresh bool) error {
	it, err := s.DB.ItemByID(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	lib, err := s.DB.LibraryByID(ctx, it.LibraryID)
	if err != nil {
		return err
	}
	state := it.ScanState
	need := func(bit int) bool { return refresh || state&bit == 0 }

	var probe *media.ProbeResult
	if s.FF != nil && (need(db.StateProbed) || need(db.StateSubs)) {
		probe, err = s.FF.Probe(ctx, it.Path)
		if err != nil {
			return fmt.Errorf("探测 %s: %w", filepath.Base(it.Path), err)
		}
		if err := s.DB.SetMediaInfo(ctx, id, db.MediaInfo{Duration: probe.Duration, Width: probe.Width, Height: probe.Height,
			VCodec: probe.VCodec, ACodec: probe.ACodec, Container: probe.Container, Bitrate: probe.Bitrate}); err != nil {
			return err
		}
		it.Duration, it.Width, it.Height = probe.Duration, probe.Width, probe.Height
	}

	hasLocal := false
	if need(db.StateLocalMeta) {
		lm := readLocalMeta(it.Path, countVideosInDir(filepath.Dir(it.Path)) == 1)
		if lm.Source != "" {
			hasLocal = containsStr(lm.Fields, "title")
			if err := s.DB.ApplyMeta(ctx, id, &lm.Meta, lm.Fields, lm.Source, false, false); err != nil {
				return err
			}
		}
		if lib.Kind == "jav" && !containsStr(lm.Fields, "code") {
			if c := scraper.ParseCode(it.Path); c.Code != "" {
				s.DB.ApplyMeta(ctx, id, &db.Meta{Code: c.Code}, []string{"code"}, "", false, false)
			}
		}
		for kind, p := range map[string]string{"poster": lm.Poster, "backdrop": lm.Back, "thumb": lm.Thumb} {
			if p == "" {
				continue
			}
			if data, err := os.ReadFile(p); err == nil {
				if err := s.SetImageData(ctx, id, kind, data, false); err != nil {
					log.Printf("读取本地图片 %s 失败: %v", p, err)
				}
			}
		}
		s.DB.AddScanState(ctx, id, db.StateLocalMeta)
	} else {
		hasLocal = it.MetaSource == "nfo" || it.MetaSource == "json"
	}

	if lib.Scraper != "" && lib.Scraper != "none" && need(db.StateScraped) && !hasLocal {
		if err := s.Scrape(ctx, id, "", false); err != nil && !errors.Is(err, scraper.ErrNoMatch) {
			return fmt.Errorf("刮削 %s: %w", filepath.Base(it.Path), err)
		}
	}

	if s.FF != nil && need(db.StateThumb) {
		cur, _ := s.DB.ItemByID(ctx, id)
		if cur != nil && cur.Thumb == "" {
			at := 5.0
			if it.Duration > 0 {
				at = it.Duration * 0.1
			}
			if data, err := s.FF.Frame(ctx, it.Path, at, imaging.Limits["thumb"][0]); err == nil {
				s.SetImageData(ctx, id, "thumb", data, false)
			} else {
				log.Printf("截图 %s 失败: %v", filepath.Base(it.Path), err)
			}
		}
		s.DB.AddScanState(ctx, id, db.StateThumb)
	}

	if s.FF != nil && need(db.StateSubs) {
		if err := s.processSubtitles(ctx, it, probe); err != nil {
			log.Printf("处理字幕 %s 失败: %v", filepath.Base(it.Path), err)
		}
	}
	if s.FF != nil && (refresh || state&db.StateTrickplay == 0) && s.Settings(ctx).TrickplayEnabled {
		s.EnqueueTrickplay(id)
	}
	return nil
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func countVideosInDir(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			if _, ok := VideoExts[strings.ToLower(filepath.Ext(e.Name()))]; ok {
				n++
			}
		}
	}
	return n
}

// Scrape 用媒体库配置的数据源刮削条目。code 不为空时用它代替自动识别的番号 / 标题。
// replaceImages 为 true 时覆盖已有图片。
func (s *Service) Scrape(ctx context.Context, id int64, override string, replaceImages bool) error {
	it, err := s.DB.ItemByID(ctx, id)
	if err != nil {
		return err
	}
	lib, err := s.DB.LibraryByID(ctx, it.LibraryID)
	if err != nil {
		return err
	}
	if lib.Scraper == "" || lib.Scraper == "none" {
		return errors.New("该媒体库没有启用刮削")
	}
	p, err := s.NewProvider(lib.Scraper, s.Settings(ctx))
	if err != nil {
		return err
	}
	var q scraper.Query
	switch lib.Scraper {
	case "metatube":
		q.Code = override
		if q.Code == "" {
			q.Code = it.Code
		}
		if q.Code == "" {
			q.Code = scraper.ParseCode(it.Path).Code
		}
	default:
		q.Title, q.Year = scraper.ParseTitle(it.Path)
		if override != "" {
			q.Title, q.Year = override, 0
		}
	}
	res, err := p.Lookup(ctx, q)
	if errors.Is(err, scraper.ErrNoMatch) {
		s.DB.AddScanState(ctx, id, db.StateScraped)
		return err
	}
	if err != nil {
		return err
	}
	m := &db.Meta{Title: res.Title, OriginalTitle: res.OriginalTitle, Overview: res.Overview, Year: res.Year,
		ReleaseDate: res.ReleaseDate, Code: res.Code, Studio: res.Studio, Director: res.Director, Series: res.Series,
		Rating: res.Rating, Genres: res.Genres, Actors: res.Actors}
	var fields []string
	add := func(f string, ok bool) {
		if ok {
			fields = append(fields, f)
		}
	}
	add("title", m.Title != "")
	add("originalTitle", m.OriginalTitle != "")
	add("overview", m.Overview != "")
	add("year", m.Year > 0)
	add("releaseDate", m.ReleaseDate != "")
	add("code", m.Code != "")
	add("studio", m.Studio != "")
	add("director", m.Director != "")
	add("series", m.Series != "")
	add("rating", m.Rating > 0)
	add("genres", len(m.Genres) > 0)
	add("actors", len(m.Actors) > 0)
	if err := s.DB.ApplyMeta(ctx, id, m, fields, res.Source, false, false); err != nil {
		return err
	}
	for kind, u := range map[string]string{"poster": res.PosterURL, "backdrop": res.BackdropURL} {
		if u == "" {
			continue
		}
		data, err := p.Fetch(ctx, u)
		if err != nil {
			log.Printf("下载图片失败 %s: %v", u, err)
			continue
		}
		if kind == "poster" && res.CropPoster {
			if data, err = imaging.CropPoster(data); err != nil {
				continue
			}
		}
		if err := s.SetImageData(ctx, id, kind, data, replaceImages); err != nil {
			log.Printf("保存图片失败: %v", err)
		}
	}
	return s.DB.AddScanState(ctx, id, db.StateScraped)
}

// ---- 图片和资源文件 ----

// SetImageData 规范化图片并设为条目的 poster / backdrop / thumb。replace 为 false 时已有图片不覆盖。
func (s *Service) SetImageData(ctx context.Context, id int64, kind string, data []byte, replace bool) error {
	lim, ok := imaging.Limits[kind]
	if !ok {
		return errors.New("图片类型无效")
	}
	if !replace {
		it, err := s.DB.ItemByID(ctx, id)
		if err != nil {
			return err
		}
		cur := map[string]string{"poster": it.Poster, "backdrop": it.Backdrop, "thumb": it.Thumb}[kind]
		if cur != "" {
			return nil
		}
	}
	img, err := imaging.Normalize(data, lim[0], lim[1])
	if err != nil {
		return fmt.Errorf("图片无法识别: %w", err)
	}
	b, err := s.saveAsset(img, "image/jpeg", ".jpg")
	if err != nil {
		return err
	}
	old, err := s.DB.SetImage(ctx, id, kind, b)
	if err != nil {
		os.Remove(b.Path)
		return err
	}
	s.RemoveAssets([]string{old})
	return nil
}

// SetImageFromFrame 用视频某一时刻的画面作为图片。poster 会从画面中间裁出竖版。
func (s *Service) SetImageFromFrame(ctx context.Context, id int64, kind string, at float64) error {
	if s.FF == nil {
		return errors.New("没有 ffmpeg，无法截图")
	}
	it, err := s.DB.ItemByID(ctx, id)
	if err != nil {
		return err
	}
	data, err := s.FF.Frame(ctx, it.Path, at, 0)
	if err != nil {
		return err
	}
	if kind == "poster" {
		if data, err = imaging.CropCenter(data, 2.0/3.0); err != nil {
			return err
		}
	}
	return s.SetImageData(ctx, id, kind, data, true)
}

func (s *Service) saveAsset(data []byte, mime, ext string) (*db.Blob, error) {
	id := mediacrypt.NewID()
	dir := filepath.Join(s.AssetDir, id[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, id+ext)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		return nil, err
	}
	return &db.Blob{ID: id, Path: p, Size: int64(len(data)), Mime: mime}, nil
}

// RemoveAssets 删除缓存文件。只删除资源目录下的文件，绝不会碰原始视频。
func (s *Service) RemoveAssets(paths []string) {
	root, err := filepath.Abs(s.AssetDir)
	if err != nil {
		return
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil || !strings.HasPrefix(abs, root+string(filepath.Separator)) {
			continue
		}
		os.Remove(abs)
	}
}

// ---- 字幕 ----

func (s *Service) processSubtitles(ctx context.Context, it *db.Item, probe *media.ProbeResult) error {
	dir := filepath.Dir(it.Path)
	base := strings.TrimSuffix(filepath.Base(it.Path), filepath.Ext(it.Path))
	var subs []*db.Subtitle
	var blobs []*db.Blob

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if e.IsDir() || !subExts[ext] || !strings.HasPrefix(name, base) {
			continue
		}
		mid := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(name, base), filepath.Ext(name)), ".-_ ")
		if mid != "" && !strings.HasPrefix(strings.TrimPrefix(name, base), ".") {
			continue // 文件名只是前缀相同（如 abc.mp4 与 abcd.srt），不是这个视频的字幕
		}
		vtt, err := s.FF.ConvertSubtitle(ctx, filepath.Join(dir, name))
		if err != nil || len(vtt) < 10 {
			log.Printf("转换字幕 %s 失败: %v", name, err)
			continue
		}
		b, err := s.saveAsset(vtt, "text/vtt; charset=utf-8", ".vtt")
		if err != nil {
			return err
		}
		lang, label := langLabel(mid)
		subs = append(subs, &db.Subtitle{Lang: lang, Label: label + "（外挂）", Source: "external"})
		blobs = append(blobs, b)
	}
	if probe != nil {
		for _, st := range probe.Subs {
			if !st.Text {
				continue
			}
			vtt, err := s.FF.ExtractSubtitle(ctx, it.Path, st.Index)
			if err != nil || len(vtt) < 10 {
				continue
			}
			b, err := s.saveAsset(vtt, "text/vtt; charset=utf-8", ".vtt")
			if err != nil {
				return err
			}
			lang, label := langLabel(st.Lang)
			if st.Title != "" {
				label = st.Title
			}
			subs = append(subs, &db.Subtitle{Lang: lang, Label: label + "（内嵌）", Source: "embedded"})
			blobs = append(blobs, b)
		}
	}
	old, err := s.DB.ReplaceSubtitles(ctx, it.ID, subs, blobs)
	if err != nil {
		for _, b := range blobs {
			os.Remove(b.Path)
		}
		return err
	}
	s.RemoveAssets(old)
	return nil
}

var langNames = map[string][2]string{
	"zh": {"zh", "中文"}, "chi": {"zh", "中文"}, "zho": {"zh", "中文"}, "chs": {"zh", "简体中文"}, "sc": {"zh", "简体中文"},
	"zh-cn": {"zh", "简体中文"}, "zh-hans": {"zh", "简体中文"}, "cht": {"zh", "繁体中文"}, "tc": {"zh", "繁体中文"},
	"zh-tw": {"zh", "繁体中文"}, "zh-hant": {"zh", "繁体中文"}, "en": {"en", "英语"}, "eng": {"en", "英语"},
	"ja": {"ja", "日语"}, "jpn": {"ja", "日语"}, "jp": {"ja", "日语"}, "ko": {"ko", "韩语"}, "kor": {"ko", "韩语"},
}

func langLabel(code string) (string, string) {
	c := strings.ToLower(strings.TrimSpace(code))
	if v, ok := langNames[c]; ok {
		return v[0], v[1]
	}
	if c == "" || c == "und" {
		return "", "字幕"
	}
	return c, code
}

// ---- 进度条预览图 ----

// TrickplayData 是保存在条目上的预览图信息。
type TrickplayData struct {
	media.TrickplayInfo
	Sprites []string `json:"sprites"` // 对象 ID
}

func (s *Service) GenerateTrickplay(ctx context.Context, id int64) error {
	if s.FF == nil {
		return nil
	}
	it, err := s.DB.ItemByID(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if it.Duration <= 0 {
		return nil // 还没探测完，下次扫描再生成
	}
	tmp, err := os.MkdirTemp(s.AssetDir, "trick-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	info, files, err := s.FF.Trickplay(ctx, it.Path, it.Duration, it.Width, it.Height, tmp)
	if err != nil {
		return err
	}
	var blobs []*db.Blob
	td := TrickplayData{TrickplayInfo: *info}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		b, err := s.saveAsset(data, "image/jpeg", ".jpg")
		if err != nil {
			return err
		}
		blobs = append(blobs, b)
		td.Sprites = append(td.Sprites, b.ID)
	}
	j, _ := json.Marshal(td)
	old, err := s.DB.SetTrickplay(ctx, id, string(j), blobs)
	if err != nil {
		for _, b := range blobs {
			os.Remove(b.Path)
		}
		return err
	}
	s.RemoveAssets(old)
	return nil
}

// ---- 定时扫描 ----

// RunScheduler 启动时扫描一次所有媒体库，之后按设置的间隔定时扫描，并维护目录监听。
func (s *Service) RunScheduler(ctx context.Context) {
	libs, _ := s.DB.ListLibraries(ctx)
	for _, l := range libs {
		s.EnqueueScan(l.ID, false)
	}
	s.RestartWatcher()
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.stopWatcher()
			return
		case <-t.C:
			st := s.Settings(ctx)
			if st.ScanIntervalHours <= 0 {
				continue
			}
			libs, _ := s.DB.ListLibraries(ctx)
			for _, l := range libs {
				if time.Since(time.Unix(l.LastScanAt, 0)) >= time.Duration(st.ScanIntervalHours)*time.Hour {
					s.EnqueueScan(l.ID, false)
				}
			}
		}
	}
}
