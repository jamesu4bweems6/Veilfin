// Package db 封装 SQLite 存储。读写分离：写连接只有一个，避免 SQLITE_BUSY；读连接池并发读取（WAL 模式）。
package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	R *sql.DB // 读连接池
	W *sql.DB // 单个写连接
}

var ErrNotFound = errors.New("记录不存在")

// FileName 是数据目录中数据库文件的名字。
const FileName = "veilfin.db"

// PathIn 返回数据目录中的数据库路径。
func PathIn(dataDir string) string {
	return filepath.Join(dataDir, FileName)
}

// Open 打开（必要时创建）数据库并执行迁移。
func Open(path string) (*DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	for _, p := range []string{"journal_mode(WAL)", "busy_timeout(10000)", "foreign_keys(1)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	dsn := "file:" + filepath.ToSlash(abs) + "?" + q.Encode()
	w, err := sql.Open("sqlite", dsn+"&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	r, err := sql.Open("sqlite", dsn)
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(8)
	d := &DB{R: r, W: w}
	if err := d.migrate(); err != nil {
		d.Close()
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}
	return d, nil
}

func (d *DB) Close() error {
	d.R.Close()
	return d.W.Close()
}

// Tx 在写连接上执行事务。
func (d *DB) Tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Snapshot 把数据库一致性地复制到 dst（用于备份）。
func (d *DB) Snapshot(ctx context.Context, dst string) error {
	_, err := d.W.ExecContext(ctx, "VACUUM INTO ?", dst)
	return err
}

func now() int64 { return time.Now().Unix() }

// placeholders 返回 "?,?,?"。
func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func int64Args(ids []int64) []any {
	out := make([]any, len(ids))
	for i, v := range ids {
		out[i] = v
	}
	return out
}

var migrations = []string{
	// v1
	`
CREATE TABLE settings(key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE users(
  id INTEGER PRIMARY KEY,
  username TEXT NOT NULL UNIQUE COLLATE NOCASE,
  pw_hash TEXT NOT NULL,
  is_admin INTEGER NOT NULL DEFAULT 0,
  disabled INTEGER NOT NULL DEFAULT 0,
  pin_hash TEXT NOT NULL DEFAULT '',
  blur_restricted INTEGER NOT NULL DEFAULT 1,
  failed_logins INTEGER NOT NULL DEFAULT 0,
  locked_until INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  last_login_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE login_tokens(
  hash BLOB PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL
);
CREATE INDEX login_tokens_user ON login_tokens(user_id);

CREATE TABLE libraries(
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'home',
  restricted INTEGER NOT NULL DEFAULT 0,
  scraper TEXT NOT NULL DEFAULT 'none',
  paths TEXT NOT NULL DEFAULT '[]',
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  last_scan_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE library_access(
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  PRIMARY KEY(user_id, library_id)
);

CREATE TABLE items(
  id INTEGER PRIMARY KEY,
  library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  path TEXT NOT NULL UNIQUE,
  folder TEXT NOT NULL DEFAULT '',
  size INTEGER NOT NULL,
  mtime INTEGER NOT NULL,
  video_blob TEXT NOT NULL,
  mime TEXT NOT NULL,
  title TEXT NOT NULL,
  sort_title TEXT NOT NULL,
  original_title TEXT NOT NULL DEFAULT '',
  overview TEXT NOT NULL DEFAULT '',
  year INTEGER NOT NULL DEFAULT 0,
  release_date TEXT NOT NULL DEFAULT '',
  code TEXT NOT NULL DEFAULT '',
  studio TEXT NOT NULL DEFAULT '',
  director TEXT NOT NULL DEFAULT '',
  series TEXT NOT NULL DEFAULT '',
  rating REAL NOT NULL DEFAULT 0,
  duration REAL NOT NULL DEFAULT 0,
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  vcodec TEXT NOT NULL DEFAULT '',
  acodec TEXT NOT NULL DEFAULT '',
  container TEXT NOT NULL DEFAULT '',
  bitrate INTEGER NOT NULL DEFAULT 0,
  poster TEXT NOT NULL DEFAULT '',
  backdrop TEXT NOT NULL DEFAULT '',
  thumb TEXT NOT NULL DEFAULT '',
  trickplay TEXT NOT NULL DEFAULT '',
  locked TEXT NOT NULL DEFAULT '[]',
  meta_source TEXT NOT NULL DEFAULT '',
  scan_state INTEGER NOT NULL DEFAULT 0,
  added_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX items_lib_added ON items(library_id, added_at DESC);
CREATE INDEX items_lib_sort ON items(library_id, sort_title);
CREATE INDEX items_lib_folder ON items(library_id, folder);
CREATE INDEX items_code ON items(code);

CREATE TABLE item_tags(
  item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  name TEXT NOT NULL,
  PRIMARY KEY(item_id, kind, name)
);
CREATE INDEX item_tags_kind_name ON item_tags(kind, name);

CREATE TABLE blobs(
  id TEXT PRIMARY KEY,
  item_id INTEGER REFERENCES items(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  path TEXT NOT NULL,
  size INTEGER NOT NULL,
  mime TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX blobs_item ON blobs(item_id);

CREATE TABLE subtitles(
  id INTEGER PRIMARY KEY,
  item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  blob TEXT NOT NULL,
  lang TEXT NOT NULL DEFAULT '',
  label TEXT NOT NULL,
  source TEXT NOT NULL,
  ord INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX subtitles_item ON subtitles(item_id);

CREATE TABLE progress(
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  position REAL NOT NULL DEFAULT 0,
  played INTEGER NOT NULL DEFAULT 0,
  play_count INTEGER NOT NULL DEFAULT 0,
  last_played_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(user_id, item_id)
);
CREATE INDEX progress_user_last ON progress(user_id, last_played_at DESC);

CREATE TABLE play_events(
  id INTEGER PRIMARY KEY,
  psid TEXT NOT NULL UNIQUE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL REFERENCES items(id) ON DELETE CASCADE,
  started_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  watched REAL NOT NULL DEFAULT 0,
  last_pos REAL NOT NULL DEFAULT 0
);
CREATE INDEX play_events_user ON play_events(user_id, started_at DESC);
CREATE INDEX play_events_started ON play_events(started_at);
`,
	// v2：PIN 失败计数持久化（防止换会话绕过）；令牌代数（锁定 / 改 PIN 后让已签发的访问令牌失效）
	`
ALTER TABLE users ADD COLUMN pin_fails INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN pin_locked_until INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN token_gen INTEGER NOT NULL DEFAULT 0;
`,
}

func (d *DB) migrate() error {
	var v int
	if err := d.W.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := d.W.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("v%d: %w", i+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// ---- 设置 ----

func (d *DB) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := d.R.QueryRowContext(ctx, "SELECT value FROM settings WHERE key=?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (d *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := d.W.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}
