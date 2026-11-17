package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// ---- 媒体库 ----

type Library struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"` // home（自制）| movie | jav | other
	Restricted bool     `json:"restricted"`
	Scraper    string   `json:"scraper"` // none | tmdb | metatube
	Paths      []string `json:"paths"`
	SortOrder  int      `json:"sortOrder"`
	CreatedAt  int64    `json:"createdAt"`
	LastScanAt int64    `json:"lastScanAt"`
}

const libCols = "id,name,kind,restricted,scraper,paths,sort_order,created_at,last_scan_at"

func scanLib(row interface{ Scan(...any) error }) (*Library, error) {
	var l Library
	var paths string
	err := row.Scan(&l.ID, &l.Name, &l.Kind, &l.Restricted, &l.Scraper, &paths, &l.SortOrder, &l.CreatedAt, &l.LastScanAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(paths), &l.Paths)
	return &l, nil
}

func (d *DB) ListLibraries(ctx context.Context) ([]*Library, error) {
	rows, err := d.R.QueryContext(ctx, "SELECT "+libCols+" FROM libraries ORDER BY sort_order, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Library
	for rows.Next() {
		l, err := scanLib(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (d *DB) LibraryByID(ctx context.Context, id int64) (*Library, error) {
	return scanLib(d.R.QueryRowContext(ctx, "SELECT "+libCols+" FROM libraries WHERE id=?", id))
}

func (d *DB) CreateLibrary(ctx context.Context, l *Library) (int64, error) {
	paths, _ := json.Marshal(l.Paths)
	res, err := d.W.ExecContext(ctx, "INSERT INTO libraries(name,kind,restricted,scraper,paths,sort_order,created_at) VALUES(?,?,?,?,?,?,?)",
		l.Name, l.Kind, l.Restricted, l.Scraper, string(paths), l.SortOrder, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) UpdateLibrary(ctx context.Context, l *Library) error {
	paths, _ := json.Marshal(l.Paths)
	_, err := d.W.ExecContext(ctx, "UPDATE libraries SET name=?,kind=?,restricted=?,scraper=?,paths=?,sort_order=? WHERE id=?",
		l.Name, l.Kind, l.Restricted, l.Scraper, string(paths), l.SortOrder, l.ID)
	return err
}

func (d *DB) TouchLibraryScan(ctx context.Context, id int64) error {
	_, err := d.W.ExecContext(ctx, "UPDATE libraries SET last_scan_at=? WHERE id=?", now(), id)
	return err
}

// DeleteLibrary 删除媒体库，返回需要删除的缓存文件路径（图片、字幕等，不含原始视频）。
func (d *DB) DeleteLibrary(ctx context.Context, id int64) ([]string, error) {
	var files []string
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT b.path FROM blobs b JOIN items i ON b.item_id=i.id WHERE i.library_id=? AND b.kind<>'video'`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p string
			rows.Scan(&p)
			files = append(files, p)
		}
		rows.Close()
		_, err = tx.ExecContext(ctx, "DELETE FROM libraries WHERE id=?", id)
		return err
	})
	return files, err
}

func (d *DB) LibraryAccess(ctx context.Context, uid int64) ([]int64, error) {
	rows, err := d.R.QueryContext(ctx, "SELECT library_id FROM library_access WHERE user_id=?", uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *DB) SetLibraryAccess(ctx context.Context, uid int64, libs []int64) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM library_access WHERE user_id=?", uid); err != nil {
			return err
		}
		for _, l := range libs {
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO library_access(user_id,library_id) SELECT ?, id FROM libraries WHERE id=?", uid, l); err != nil {
				return err
			}
		}
		return nil
	})
}

// ---- 媒体条目 ----

type Item struct {
	ID            int64   `json:"id"`
	LibraryID     int64   `json:"libraryId"`
	Path          string  `json:"-"`
	Folder        string  `json:"folder"`
	Size          int64   `json:"size"`
	Mtime         int64   `json:"-"`
	VideoBlob     string  `json:"-"`
	Mime          string  `json:"mime"`
	Title         string  `json:"title"`
	SortTitle     string  `json:"sortTitle"`
	OriginalTitle string  `json:"originalTitle"`
	Overview      string  `json:"overview"`
	Year          int     `json:"year"`
	ReleaseDate   string  `json:"releaseDate"`
	Code          string  `json:"code"`
	Studio        string  `json:"studio"`
	Director      string  `json:"director"`
	Series        string  `json:"series"`
	Rating        float64 `json:"rating"`
	Duration      float64 `json:"duration"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	VCodec        string  `json:"vcodec"`
	ACodec        string  `json:"acodec"`
	Container     string  `json:"container"`
	Bitrate       int64   `json:"bitrate"`
	Poster        string  `json:"-"`
	Backdrop      string  `json:"-"`
	Thumb         string  `json:"-"`
	Trickplay     string  `json:"-"`
	Locked        string  `json:"-"`
	MetaSource    string  `json:"metaSource"`
	ScanState     int     `json:"-"`
	AddedAt       int64   `json:"addedAt"`
	UpdatedAt     int64   `json:"updatedAt"`
}

// 扫描状态位
const (
	StateProbed    = 1 << 0
	StateLocalMeta = 1 << 1
	StateScraped   = 1 << 2
	StateThumb     = 1 << 3
	StateSubs      = 1 << 4
	StateTrickplay = 1 << 5
)

const itemCols = `id,library_id,path,folder,size,mtime,video_blob,mime,title,sort_title,original_title,overview,year,
release_date,code,studio,director,series,rating,duration,width,height,vcodec,acodec,container,bitrate,poster,backdrop,
thumb,trickplay,locked,meta_source,scan_state,added_at,updated_at`

func scanItem(row interface{ Scan(...any) error }) (*Item, error) {
	var it Item
	err := row.Scan(&it.ID, &it.LibraryID, &it.Path, &it.Folder, &it.Size, &it.Mtime, &it.VideoBlob, &it.Mime, &it.Title,
		&it.SortTitle, &it.OriginalTitle, &it.Overview, &it.Year, &it.ReleaseDate, &it.Code, &it.Studio, &it.Director,
		&it.Series, &it.Rating, &it.Duration, &it.Width, &it.Height, &it.VCodec, &it.ACodec, &it.Container, &it.Bitrate,
		&it.Poster, &it.Backdrop, &it.Thumb, &it.Trickplay, &it.Locked, &it.MetaSource, &it.ScanState, &it.AddedAt, &it.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &it, err
}

func (d *DB) ItemByID(ctx context.Context, id int64) (*Item, error) {
	return scanItem(d.R.QueryRowContext(ctx, "SELECT "+itemCols+" FROM items WHERE id=?", id))
}

func (d *DB) ItemsByIDs(ctx context.Context, ids []int64) ([]*Item, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := d.R.QueryContext(ctx, "SELECT "+itemCols+" FROM items WHERE id IN ("+placeholders(len(ids))+")", int64Args(ids)...)
	if err != nil {
		return nil, err
	}
	return collectItems(rows)
}

func collectItems(rows *sql.Rows) ([]*Item, error) {
	defer rows.Close()
	var out []*Item
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// FileState 是扫描时用于比对的文件状态。
type FileState struct {
	ID    int64
	Size  int64
	Mtime int64
}

func (d *DB) LibraryFiles(ctx context.Context, lib int64) (map[string]FileState, error) {
	rows, err := d.R.QueryContext(ctx, "SELECT id,path,size,mtime FROM items WHERE library_id=?", lib)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]FileState{}
	for rows.Next() {
		var s FileState
		var p string
		if err := rows.Scan(&s.ID, &p, &s.Size, &s.Mtime); err != nil {
			return nil, err
		}
		out[p] = s
	}
	return out, rows.Err()
}

// ItemsNeedingWork 返回 scan_state 缺少 mask 中任意一位的条目 ID。
func (d *DB) ItemsNeedingWork(ctx context.Context, lib int64, mask int) ([]int64, error) {
	rows, err := d.R.QueryContext(ctx, "SELECT id FROM items WHERE library_id=? AND (scan_state & ?) <> ? ORDER BY added_at DESC", lib, mask, mask)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		out = append(out, id)
	}
	return out, rows.Err()
}

// InsertItem 插入新条目及其视频对象。
func (d *DB) InsertItem(ctx context.Context, it *Item) (int64, error) {
	t := now()
	var id int64
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO items(library_id,path,folder,size,mtime,video_blob,mime,title,sort_title,added_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`, it.LibraryID, it.Path, it.Folder, it.Size, it.Mtime, it.VideoBlob, it.Mime, it.Title, it.SortTitle, t, t)
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		_, err = tx.ExecContext(ctx, "INSERT INTO blobs(id,item_id,kind,path,size,mime,created_at) VALUES(?,?,?,?,?,?,?)",
			it.VideoBlob, id, "video", it.Path, it.Size, it.Mime, t)
		return err
	})
	return id, err
}

// ReplaceVideoFile 在文件内容变化后更换视频对象 ID（使 CDN 旧缓存失效），并要求重新探测。
func (d *DB) ReplaceVideoFile(ctx context.Context, id int64, size, mtime int64, newBlob, mime string) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var path string
		if err := tx.QueryRowContext(ctx, "SELECT path FROM items WHERE id=?", id).Scan(&path); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM blobs WHERE item_id=? AND kind='video'", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO blobs(id,item_id,kind,path,size,mime,created_at) VALUES(?,?,?,?,?,?,?)",
			newBlob, id, "video", path, size, mime, now()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE items SET size=?, mtime=?, video_blob=?, mime=?, updated_at=?,
			scan_state = scan_state & ~(?) WHERE id=?`, size, mtime, newBlob, mime, now(), StateProbed|StateTrickplay|StateThumb|StateSubs, id)
		return err
	})
}

// DeleteItem 删除条目，返回需要清理的缓存文件。
func (d *DB) DeleteItem(ctx context.Context, id int64) ([]string, error) {
	var files []string
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT path FROM blobs WHERE item_id=? AND kind<>'video'", id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p string
			rows.Scan(&p)
			files = append(files, p)
		}
		rows.Close()
		_, err = tx.ExecContext(ctx, "DELETE FROM items WHERE id=?", id)
		return err
	})
	return files, err
}

// MediaInfo 是 ffprobe 得到的技术信息。
type MediaInfo struct {
	Duration  float64
	Width     int
	Height    int
	VCodec    string
	ACodec    string
	Container string
	Bitrate   int64
}

func (d *DB) SetMediaInfo(ctx context.Context, id int64, m MediaInfo) error {
	_, err := d.W.ExecContext(ctx, `UPDATE items SET duration=?,width=?,height=?,vcodec=?,acodec=?,container=?,bitrate=?,
		scan_state = scan_state | ?, updated_at=? WHERE id=?`, m.Duration, m.Width, m.Height, m.VCodec, m.ACodec, m.Container, m.Bitrate, StateProbed, now(), id)
	return err
}

func (d *DB) AddScanState(ctx context.Context, id int64, bits int) error {
	_, err := d.W.ExecContext(ctx, "UPDATE items SET scan_state = scan_state | ? WHERE id=?", bits, id)
	return err
}

func (d *DB) ClearScanState(ctx context.Context, id int64, bits int) error {
	_, err := d.W.ExecContext(ctx, "UPDATE items SET scan_state = scan_state & ~(?) WHERE id=?", bits, id)
	return err
}

// Meta 是可编辑的元数据字段。
type Meta struct {
	Title         string   `json:"title"`
	SortTitle     string   `json:"sortTitle"`
	OriginalTitle string   `json:"originalTitle"`
	Overview      string   `json:"overview"`
	Year          int      `json:"year"`
	ReleaseDate   string   `json:"releaseDate"`
	Code          string   `json:"code"`
	Studio        string   `json:"studio"`
	Director      string   `json:"director"`
	Series        string   `json:"series"`
	Rating        float64  `json:"rating"`
	Genres        []string `json:"genres"`
	Tags          []string `json:"tags"`
	Actors        []string `json:"actors"`
}

// MetaFields 是 Meta 中可以被“锁定”的字段名。
var MetaFields = []string{"title", "sortTitle", "originalTitle", "overview", "year", "releaseDate", "code", "studio", "director", "series", "rating", "genres", "tags", "actors"}

// ApplyMeta 把 m 中的字段写入条目。只更新 fields 中列出的字段，并跳过已锁定的字段（除非 force）。
// 对标签类字段，给出的列表会整体替换原有值。
func (d *DB) ApplyMeta(ctx context.Context, id int64, m *Meta, fields []string, source string, force bool, lock bool) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var lockedJSON string
		if err := tx.QueryRowContext(ctx, "SELECT locked FROM items WHERE id=?", id).Scan(&lockedJSON); err != nil {
			return err
		}
		locked := map[string]bool{}
		var ll []string
		json.Unmarshal([]byte(lockedJSON), &ll)
		for _, f := range ll {
			locked[f] = true
		}
		cols := map[string]any{
			"title": m.Title, "sortTitle": m.SortTitle, "originalTitle": m.OriginalTitle, "overview": m.Overview,
			"year": m.Year, "releaseDate": m.ReleaseDate, "code": m.Code, "studio": m.Studio, "director": m.Director,
			"series": m.Series, "rating": m.Rating,
		}
		colName := map[string]string{"title": "title", "sortTitle": "sort_title", "originalTitle": "original_title",
			"overview": "overview", "year": "year", "releaseDate": "release_date", "code": "code", "studio": "studio",
			"director": "director", "series": "series", "rating": "rating"}
		tagKind := map[string]string{"genres": "genre", "tags": "tag", "actors": "actor"}
		tagVals := map[string][]string{"genres": m.Genres, "tags": m.Tags, "actors": m.Actors}

		var sets []string
		var args []any
		for _, f := range fields {
			if locked[f] && !force {
				continue
			}
			if lock {
				locked[f] = true
			}
			if c, ok := colName[f]; ok {
				v := cols[f]
				if f == "title" && m.Title == "" {
					continue // 标题不允许为空
				}
				sets = append(sets, c+"=?")
				args = append(args, v)
				if f == "title" && !contains(fields, "sortTitle") {
					sets = append(sets, "sort_title=?")
					args = append(args, SortKey(m.Title))
				}
				continue
			}
			if k, ok := tagKind[f]; ok {
				if _, err := tx.ExecContext(ctx, "DELETE FROM item_tags WHERE item_id=? AND kind=?", id, k); err != nil {
					return err
				}
				for _, name := range dedupe(tagVals[f]) {
					if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO item_tags(item_id,kind,name) VALUES(?,?,?)", id, k, name); err != nil {
						return err
					}
				}
			}
		}
		newLocked := make([]string, 0, len(locked))
		for _, f := range MetaFields {
			if locked[f] {
				newLocked = append(newLocked, f)
			}
		}
		lj, _ := json.Marshal(newLocked)
		sets = append(sets, "locked=?", "updated_at=?")
		args = append(args, string(lj), now())
		if source != "" {
			sets = append(sets, "meta_source=?")
			args = append(args, source)
		}
		args = append(args, id)
		_, err := tx.ExecContext(ctx, "UPDATE items SET "+strings.Join(sets, ",")+" WHERE id=?", args...)
		return err
	})
}

// UnlockFields 解除字段锁定。
func (d *DB) UnlockAll(ctx context.Context, id int64) error {
	_, err := d.W.ExecContext(ctx, "UPDATE items SET locked='[]' WHERE id=?", id)
	return err
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	return out
}

// SortKey 生成排序用的标题：去掉开头的冠词和标点，统一小写。
func SortKey(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	for _, p := range []string{"the ", "a ", "an "} {
		if strings.HasPrefix(t, p) {
			t = t[len(p):]
			break
		}
	}
	return strings.TrimLeft(t, " \t-_.[]【】()（）")
}

func (d *DB) ItemTags(ctx context.Context, id int64) (map[string][]string, error) {
	rows, err := d.R.QueryContext(ctx, "SELECT kind,name FROM item_tags WHERE item_id=? ORDER BY rowid", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{"genre": {}, "tag": {}, "actor": {}}
	for rows.Next() {
		var k, n string
		rows.Scan(&k, &n)
		out[k] = append(out[k], n)
	}
	return out, rows.Err()
}

func (d *DB) ItemLocked(ctx context.Context, id int64) ([]string, error) {
	var s string
	if err := d.R.QueryRowContext(ctx, "SELECT locked FROM items WHERE id=?", id).Scan(&s); err != nil {
		return nil, err
	}
	var out []string
	json.Unmarshal([]byte(s), &out)
	return out, nil
}

// TopTags 返回若干媒体库中使用最多的标签，用于筛选。
func (d *DB) TopTags(ctx context.Context, libs []int64, kind string, limit int) ([]string, error) {
	if len(libs) == 0 {
		return nil, nil
	}
	args := append(int64Args(libs), kind, limit)
	rows, err := d.R.QueryContext(ctx, `SELECT t.name FROM item_tags t JOIN items i ON i.id=t.item_id
		WHERE i.library_id IN (`+placeholders(len(libs))+`) AND t.kind=? GROUP BY t.name ORDER BY COUNT(*) DESC, t.name LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		out = append(out, n)
	}
	return out, rows.Err()
}

// ---- 图片和预览图 ----

// SetImage 设置条目的图片（poster/backdrop/thumb），返回被替换下来的旧对象文件路径。
func (d *DB) SetImage(ctx context.Context, id int64, kind string, b *Blob) (string, error) {
	col := map[string]string{"poster": "poster", "backdrop": "backdrop", "thumb": "thumb"}[kind]
	if col == "" {
		return "", errors.New("图片类型无效")
	}
	var oldPath string
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		var old string
		if err := tx.QueryRowContext(ctx, "SELECT "+col+" FROM items WHERE id=?", id).Scan(&old); err != nil {
			return err
		}
		if old != "" {
			tx.QueryRowContext(ctx, "SELECT path FROM blobs WHERE id=?", old).Scan(&oldPath)
			if _, err := tx.ExecContext(ctx, "DELETE FROM blobs WHERE id=?", old); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO blobs(id,item_id,kind,path,size,mime,created_at) VALUES(?,?,?,?,?,?,?)",
			b.ID, id, "image", b.Path, b.Size, b.Mime, now()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE items SET "+col+"=?, updated_at=? WHERE id=?", b.ID, now(), id)
		return err
	})
	return oldPath, err
}

// SetTrickplay 替换条目的进度条预览图，返回需要删除的旧文件。
func (d *DB) SetTrickplay(ctx context.Context, id int64, info string, sprites []*Blob) ([]string, error) {
	var old []string
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT path FROM blobs WHERE item_id=? AND kind='trick'", id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p string
			rows.Scan(&p)
			old = append(old, p)
		}
		rows.Close()
		if _, err := tx.ExecContext(ctx, "DELETE FROM blobs WHERE item_id=? AND kind='trick'", id); err != nil {
			return err
		}
		for _, b := range sprites {
			if _, err := tx.ExecContext(ctx, "INSERT INTO blobs(id,item_id,kind,path,size,mime,created_at) VALUES(?,?,?,?,?,?,?)",
				b.ID, id, "trick", b.Path, b.Size, b.Mime, now()); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE items SET trickplay=?, scan_state = scan_state | ? WHERE id=?", info, StateTrickplay, id)
		return err
	})
	return old, err
}

// ---- 字幕 ----

type Subtitle struct {
	ID     int64  `json:"id"`
	ItemID int64  `json:"-"`
	Blob   string `json:"-"`
	Lang   string `json:"lang"`
	Label  string `json:"label"`
	Source string `json:"source"`
	Ord    int    `json:"-"`
}

// ReplaceSubtitles 替换条目的全部字幕，返回需要删除的旧文件。
func (d *DB) ReplaceSubtitles(ctx context.Context, id int64, subs []*Subtitle, blobs []*Blob) ([]string, error) {
	var old []string
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT path FROM blobs WHERE item_id=? AND kind='sub'", id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p string
			rows.Scan(&p)
			old = append(old, p)
		}
		rows.Close()
		for _, q := range []string{"DELETE FROM blobs WHERE item_id=? AND kind='sub'", "DELETE FROM subtitles WHERE item_id=?"} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		for i, s := range subs {
			b := blobs[i]
			if _, err := tx.ExecContext(ctx, "INSERT INTO blobs(id,item_id,kind,path,size,mime,created_at) VALUES(?,?,?,?,?,?,?)",
				b.ID, id, "sub", b.Path, b.Size, b.Mime, now()); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO subtitles(item_id,blob,lang,label,source,ord) VALUES(?,?,?,?,?,?)",
				id, b.ID, s.Lang, s.Label, s.Source, i); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE items SET scan_state = scan_state | ? WHERE id=?", StateSubs, id)
		return err
	})
	return old, err
}

func (d *DB) Subtitles(ctx context.Context, id int64) ([]*Subtitle, error) {
	rows, err := d.R.QueryContext(ctx, "SELECT id,item_id,blob,lang,label,source,ord FROM subtitles WHERE item_id=? ORDER BY ord", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Subtitle
	for rows.Next() {
		var s Subtitle
		if err := rows.Scan(&s.ID, &s.ItemID, &s.Blob, &s.Lang, &s.Label, &s.Source, &s.Ord); err != nil {
			return nil, err
		}
		out = append(out, &s)
	}
	return out, rows.Err()
}

// ---- 加密对象 ----

type Blob struct {
	ID        string
	ItemID    int64
	Kind      string // video | image | sub | trick
	Path      string
	Size      int64
	Mime      string
	LibraryID int64
}

func (d *DB) BlobByID(ctx context.Context, id string) (*Blob, error) {
	var b Blob
	var item sql.NullInt64
	var lib sql.NullInt64
	err := d.R.QueryRowContext(ctx, `SELECT b.id,b.item_id,b.kind,b.path,b.size,b.mime,i.library_id
		FROM blobs b LEFT JOIN items i ON i.id=b.item_id WHERE b.id=?`, id).
		Scan(&b.ID, &item, &b.Kind, &b.Path, &b.Size, &b.Mime, &lib)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	b.ItemID, b.LibraryID = item.Int64, lib.Int64
	return &b, err
}

// BlobSizes 批量查询对象大小，用于构造对象引用。
func (d *DB) BlobSizes(ctx context.Context, ids []string) (map[string]*Blob, error) {
	out := map[string]*Blob{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, v := range ids {
		args[i] = v
	}
	rows, err := d.R.QueryContext(ctx, "SELECT id,kind,size,mime FROM blobs WHERE id IN ("+placeholders(len(ids))+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b Blob
		if err := rows.Scan(&b.ID, &b.Kind, &b.Size, &b.Mime); err != nil {
			return nil, err
		}
		out[b.ID] = &b
	}
	return out, rows.Err()
}

// LibraryCounts 返回每个媒体库的条目数。
func (d *DB) LibraryCounts(ctx context.Context) (map[int64]int, error) {
	rows, err := d.R.QueryContext(ctx, "SELECT library_id, COUNT(*) FROM items GROUP BY library_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		rows.Scan(&id, &n)
		out[id] = n
	}
	return out, rows.Err()
}
