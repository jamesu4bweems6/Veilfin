package db

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"sort"
	"strings"
)

// Progress 是某个用户对某个条目的观看状态。
type Progress struct {
	Position   float64 `json:"position"`
	Played     bool    `json:"played"`
	PlayCount  int     `json:"playCount"`
	LastPlayed int64   `json:"lastPlayed"`
}

type ItemRow struct {
	*Item
	Progress
}

var qualifiedItemCols = func() string {
	parts := strings.Split(strings.ReplaceAll(itemCols, "\n", ""), ",")
	for i, p := range parts {
		parts[i] = "items." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ",")
}()

const progressCols = "COALESCE(p.position,0),COALESCE(p.played,0),COALESCE(p.play_count,0),COALESCE(p.last_played_at,0)"

func scanItemRow(rows *sql.Rows) (*ItemRow, error) {
	var it Item
	var r ItemRow
	err := rows.Scan(&it.ID, &it.LibraryID, &it.Path, &it.Folder, &it.Size, &it.Mtime, &it.VideoBlob, &it.Mime, &it.Title,
		&it.SortTitle, &it.OriginalTitle, &it.Overview, &it.Year, &it.ReleaseDate, &it.Code, &it.Studio, &it.Director,
		&it.Series, &it.Rating, &it.Duration, &it.Width, &it.Height, &it.VCodec, &it.ACodec, &it.Container, &it.Bitrate,
		&it.Poster, &it.Backdrop, &it.Thumb, &it.Trickplay, &it.Locked, &it.MetaSource, &it.ScanState, &it.AddedAt, &it.UpdatedAt,
		&r.Position, &r.Played, &r.PlayCount, &r.LastPlayed)
	r.Item = &it
	return &r, err
}

func collectRows(rows *sql.Rows, err error) ([]*ItemRow, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ItemRow
	for rows.Next() {
		r, err := scanItemRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type ItemQuery struct {
	UserID    int64
	Libraries []int64 // 允许访问的媒体库（调用方负责权限过滤）
	Folder    *string // 只看某个文件夹（不含子文件夹）
	Q         string
	Kind      string // 标签类型：genre | tag | actor
	Tag       string
	Played    *bool
	Sort      string // added | title | release | duration | lastPlayed | random
	Desc      bool
	Offset    int
	Limit     int
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// prefixArg 生成“以 s 开头”的 LIKE 参数。
func prefixArg(s string) string { return likeEscaper.Replace(s) + "%" }

// likeArg 生成“包含 s”的 LIKE 参数。
func likeArg(s string) string { return "%" + likeEscaper.Replace(s) + "%" }

// QueryItems 按条件分页查询条目，返回结果和总数。
func (d *DB) QueryItems(ctx context.Context, q ItemQuery) ([]*ItemRow, int, error) {
	if len(q.Libraries) == 0 {
		return nil, 0, nil
	}
	where := []string{"items.library_id IN (" + placeholders(len(q.Libraries)) + ")"}
	args := int64Args(q.Libraries)
	if q.Folder != nil {
		where = append(where, "items.folder = ?")
		args = append(args, *q.Folder)
	}
	if s := strings.TrimSpace(q.Q); s != "" {
		l := likeArg(s)
		where = append(where, `(items.title LIKE ? ESCAPE '\' OR items.original_title LIKE ? ESCAPE '\' OR items.code LIKE ? ESCAPE '\'
			OR EXISTS(SELECT 1 FROM item_tags t WHERE t.item_id=items.id AND t.name LIKE ? ESCAPE '\'))`)
		args = append(args, l, l, l, l)
	}
	if q.Tag != "" && q.Kind != "" {
		where = append(where, "EXISTS(SELECT 1 FROM item_tags t WHERE t.item_id=items.id AND t.kind=? AND t.name=?)")
		args = append(args, q.Kind, q.Tag)
	}
	if q.Played != nil {
		if *q.Played {
			where = append(where, "COALESCE(p.played,0)=1")
		} else {
			where = append(where, "COALESCE(p.played,0)=0")
		}
	}
	from := " FROM items LEFT JOIN progress p ON p.item_id=items.id AND p.user_id=? WHERE " + strings.Join(where, " AND ")
	allArgs := append([]any{q.UserID}, args...)

	var total int
	if err := d.R.QueryRowContext(ctx, "SELECT COUNT(*)"+from, allArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}

	dir := "ASC"
	if q.Desc {
		dir = "DESC"
	}
	var order string
	switch q.Sort {
	case "title":
		order = "items.sort_title " + dir
	case "release":
		order = "items.release_date " + dir + ", items.year " + dir
	case "duration":
		order = "items.duration " + dir
	case "lastPlayed":
		order = "COALESCE(p.last_played_at,0) " + dir
	case "random":
		order = "RANDOM()"
	default:
		order = "items.added_at " + dir
	}
	order += ", items.id " + dir
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 60
	}
	allArgs = append(allArgs, limit, max(q.Offset, 0))
	rows, err := collectRows(d.R.QueryContext(ctx, "SELECT "+qualifiedItemCols+","+progressCols+from+" ORDER BY "+order+" LIMIT ? OFFSET ?", allArgs...))
	return rows, total, err
}

// ItemRowByID 返回条目和该用户的观看进度。
func (d *DB) ItemRowByID(ctx context.Context, uid, id int64) (*ItemRow, error) {
	rows, err := collectRows(d.R.QueryContext(ctx, "SELECT "+qualifiedItemCols+","+progressCols+
		" FROM items LEFT JOIN progress p ON p.item_id=items.id AND p.user_id=? WHERE items.id=?", uid, id))
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrNotFound
	}
	return rows[0], nil
}

// Folder 是文件夹浏览中的一个子文件夹。
type Folder struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// SubFolders 返回 parent 下一级的子文件夹以及各自包含的视频数（含更深层）。
func (d *DB) SubFolders(ctx context.Context, lib int64, parent string) ([]Folder, error) {
	var rows *sql.Rows
	var err error
	if parent == "" {
		rows, err = d.R.QueryContext(ctx, "SELECT folder, COUNT(*) FROM items WHERE library_id=? AND folder<>'' GROUP BY folder", lib)
	} else {
		rows, err = d.R.QueryContext(ctx, `SELECT folder, COUNT(*) FROM items WHERE library_id=? AND folder LIKE ? ESCAPE '\' GROUP BY folder`,
			lib, strings.TrimSuffix(likeArg(parent+"/"), "%")[1:]+"%")
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var f string
		var n int
		rows.Scan(&f, &n)
		rest := f
		if parent != "" {
			rest = strings.TrimPrefix(f, parent+"/")
		}
		child := strings.SplitN(rest, "/", 2)[0]
		if child == "" {
			continue
		}
		counts[child] += n
	}
	out := make([]Folder, 0, len(counts))
	for name, n := range counts {
		p := name
		if parent != "" {
			p = parent + "/" + name
		}
		out = append(out, Folder{Path: p, Name: name, Count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, rows.Err()
}

// ---- 观看进度 ----

const (
	playedRatio = 0.90 // 播放超过 90% 视为看完
	resumeRatio = 0.05 // 不到 5% 不计入“继续观看”
)

// ReportProgress 记录一次播放进度上报，返回最新状态。
func (d *DB) ReportProgress(ctx context.Context, uid, itemID int64, psid string, pos, dur float64, t int64) (*Progress, error) {
	var out Progress
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		// 播放事件：累计观看时长。用“墙钟间隔”和“进度变化”中较小的一个，拖动和暂停都不会被算进去。
		var evID int64
		var updated int64
		var lastPos float64
		err := tx.QueryRowContext(ctx, "SELECT id, updated_at, last_pos FROM play_events WHERE psid=? AND user_id=?", psid, uid).Scan(&evID, &updated, &lastPos)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if _, err := tx.ExecContext(ctx, "INSERT INTO play_events(psid,user_id,item_id,started_at,updated_at,last_pos) VALUES(?,?,?,?,?,?)",
				psid, uid, itemID, t, t, pos); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			add := 0.0
			if wall := float64(t - updated); wall > 0 && wall <= 120 {
				add = math.Min(wall+1, math.Abs(pos-lastPos))
			}
			if _, err := tx.ExecContext(ctx, "UPDATE play_events SET updated_at=?, last_pos=?, watched=watched+? WHERE id=?", t, pos, add, evID); err != nil {
				return err
			}
		}

		var cur Progress
		err = tx.QueryRowContext(ctx, "SELECT position, played, play_count, last_played_at FROM progress WHERE user_id=? AND item_id=?", uid, itemID).
			Scan(&cur.Position, &cur.Played, &cur.PlayCount, &cur.LastPlayed)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		out = cur
		out.LastPlayed = t
		if dur > 0 && pos/dur >= playedRatio {
			if !cur.Played || cur.Position > 0 {
				out.PlayCount++
			}
			out.Played, out.Position = true, 0
		} else {
			out.Position = pos
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO progress(user_id,item_id,position,played,play_count,last_played_at) VALUES(?,?,?,?,?,?)
			ON CONFLICT(user_id,item_id) DO UPDATE SET position=excluded.position, played=excluded.played,
			play_count=excluded.play_count, last_played_at=excluded.last_played_at`,
			uid, itemID, out.Position, out.Played, out.PlayCount, out.LastPlayed)
		return err
	})
	return &out, err
}

// SetPlayed 手动标记已看 / 未看。
func (d *DB) SetPlayed(ctx context.Context, uid, itemID int64, played bool) error {
	if played {
		_, err := d.W.ExecContext(ctx, `INSERT INTO progress(user_id,item_id,position,played,play_count,last_played_at) VALUES(?,?,0,1,1,?)
			ON CONFLICT(user_id,item_id) DO UPDATE SET position=0, played=1, play_count=MAX(play_count,1)`, uid, itemID, now())
		return err
	}
	_, err := d.W.ExecContext(ctx, "UPDATE progress SET position=0, played=0 WHERE user_id=? AND item_id=?", uid, itemID)
	return err
}

// ContinueWatching 返回看了一部分、还没看完的条目。
func (d *DB) ContinueWatching(ctx context.Context, uid int64, libs []int64, limit int) ([]*ItemRow, error) {
	if len(libs) == 0 {
		return nil, nil
	}
	args := append([]any{uid}, int64Args(libs)...)
	args = append(args, resumeRatio, limit)
	return collectRows(d.R.QueryContext(ctx, "SELECT "+qualifiedItemCols+","+progressCols+
		` FROM progress p JOIN items ON items.id=p.item_id WHERE p.user_id=? AND items.library_id IN (`+placeholders(len(libs))+`)
		AND p.played=0 AND p.position > 0 AND (items.duration=0 OR p.position >= items.duration*?)
		ORDER BY p.last_played_at DESC LIMIT ?`, args...))
}

// Latest 返回某个媒体库最近添加的条目。
func (d *DB) Latest(ctx context.Context, uid, lib int64, limit int) ([]*ItemRow, error) {
	return collectRows(d.R.QueryContext(ctx, "SELECT "+qualifiedItemCols+","+progressCols+
		" FROM items LEFT JOIN progress p ON p.item_id=items.id AND p.user_id=? WHERE items.library_id=? ORDER BY items.added_at DESC, items.id DESC LIMIT ?",
		uid, lib, limit))
}

// HistoryEntry 是一次播放记录。
type HistoryEntry struct {
	Row       *ItemRow
	StartedAt int64
	Watched   float64
}

func (d *DB) History(ctx context.Context, uid int64, libs []int64, offset, limit int) ([]*HistoryEntry, int, error) {
	if len(libs) == 0 {
		return nil, 0, nil
	}
	libArgs := int64Args(libs)
	var total int
	cargs := append([]any{uid}, libArgs...)
	if err := d.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM play_events e JOIN items ON items.id=e.item_id
		WHERE e.user_id=? AND items.library_id IN (`+placeholders(len(libs))+`)`, cargs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args := append([]any{uid, uid}, libArgs...)
	args = append(args, limit, offset)
	rows, err := d.R.QueryContext(ctx, "SELECT "+qualifiedItemCols+","+progressCols+`, e.started_at, e.watched
		FROM play_events e JOIN items ON items.id=e.item_id LEFT JOIN progress p ON p.item_id=items.id AND p.user_id=?
		WHERE e.user_id=? AND items.library_id IN (`+placeholders(len(libs))+`) ORDER BY e.started_at DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*HistoryEntry
	for rows.Next() {
		var it Item
		var r ItemRow
		var h HistoryEntry
		if err := rows.Scan(&it.ID, &it.LibraryID, &it.Path, &it.Folder, &it.Size, &it.Mtime, &it.VideoBlob, &it.Mime, &it.Title,
			&it.SortTitle, &it.OriginalTitle, &it.Overview, &it.Year, &it.ReleaseDate, &it.Code, &it.Studio, &it.Director,
			&it.Series, &it.Rating, &it.Duration, &it.Width, &it.Height, &it.VCodec, &it.ACodec, &it.Container, &it.Bitrate,
			&it.Poster, &it.Backdrop, &it.Thumb, &it.Trickplay, &it.Locked, &it.MetaSource, &it.ScanState, &it.AddedAt, &it.UpdatedAt,
			&r.Position, &r.Played, &r.PlayCount, &r.LastPlayed, &h.StartedAt, &h.Watched); err != nil {
			return nil, 0, err
		}
		r.Item = &it
		h.Row = &r
		out = append(out, &h)
	}
	return out, total, rows.Err()
}

func (d *DB) ClearHistory(ctx context.Context, uid int64) error {
	_, err := d.W.ExecContext(ctx, "DELETE FROM play_events WHERE user_id=?", uid)
	return err
}

// ---- 统计 ----

type UserStat struct {
	UserID   int64   `json:"userId"`
	Username string  `json:"username"`
	Plays    int     `json:"plays"`
	Watched  float64 `json:"watched"`
	LastPlay int64   `json:"lastPlay"`
}

type DayStat struct {
	Day     string  `json:"day"`
	Watched float64 `json:"watched"`
	Plays   int     `json:"plays"`
}

type TopItem struct {
	ItemID    int64   `json:"itemId"`
	LibraryID int64   `json:"-"`
	Title     string  `json:"title"`
	Plays     int     `json:"plays"`
	Watched   float64 `json:"watched"`
}

type Stats struct {
	Users         []UserStat `json:"users"`
	Days          []DayStat  `json:"days"`
	Top           []TopItem  `json:"top"`
	ItemCount     int        `json:"itemCount"`
	TotalSize     int64      `json:"totalSize"`
	TotalDuration float64    `json:"totalDuration"`
}

func (d *DB) Stats(ctx context.Context, since int64) (*Stats, error) {
	var s Stats
	if err := d.R.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(size),0), COALESCE(SUM(duration),0) FROM items").
		Scan(&s.ItemCount, &s.TotalSize, &s.TotalDuration); err != nil {
		return nil, err
	}
	rows, err := d.R.QueryContext(ctx, `SELECT u.id, u.username, COUNT(e.id), COALESCE(SUM(e.watched),0), COALESCE(MAX(e.started_at),0)
		FROM users u LEFT JOIN play_events e ON e.user_id=u.id AND e.started_at>=? GROUP BY u.id ORDER BY 4 DESC`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var u UserStat
		rows.Scan(&u.UserID, &u.Username, &u.Plays, &u.Watched, &u.LastPlay)
		s.Users = append(s.Users, u)
	}
	rows.Close()
	rows, err = d.R.QueryContext(ctx, `SELECT date(started_at,'unixepoch','localtime') d, COALESCE(SUM(watched),0), COUNT(*)
		FROM play_events WHERE started_at>=? GROUP BY d ORDER BY d`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var x DayStat
		rows.Scan(&x.Day, &x.Watched, &x.Plays)
		s.Days = append(s.Days, x)
	}
	rows.Close()
	rows, err = d.R.QueryContext(ctx, `SELECT e.item_id, i.library_id, i.title, COUNT(*), COALESCE(SUM(e.watched),0) FROM play_events e
		JOIN items i ON i.id=e.item_id WHERE e.started_at>=? GROUP BY e.item_id ORDER BY 4 DESC, 5 DESC LIMIT 10`, since)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var x TopItem
		rows.Scan(&x.ItemID, &x.LibraryID, &x.Title, &x.Plays, &x.Watched)
		s.Top = append(s.Top, x)
	}
	rows.Close()
	return &s, nil
}
