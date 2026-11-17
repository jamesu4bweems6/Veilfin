package library

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounce：目录内连续发生的变化（例如复制大文件期间）合并为一次扫描。
const debounce = 8 * time.Second

type watcher struct {
	w      *fsnotify.Watcher
	cancel context.CancelFunc
	roots  map[string]int64 // 根目录 → 媒体库 ID

	mu     sync.Mutex
	timers map[int64]*time.Timer
}

// RestartWatcher 按当前媒体库配置重建目录监听（媒体库增删改后调用）。
func (s *Service) RestartWatcher() {
	ctx := s.baseCtx
	s.stopWatcher()
	if !s.Settings(ctx).WatchEnabled {
		return
	}
	libs, err := s.DB.ListLibraries(ctx)
	if err != nil || len(libs) == 0 {
		return
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("无法启动目录监听: %v", err)
		return
	}
	wctx, cancel := context.WithCancel(ctx)
	w := &watcher{w: fw, cancel: cancel, roots: map[string]int64{}, timers: map[int64]*time.Timer{}}
	n := 0
	for _, l := range libs {
		for _, root := range l.Paths {
			root = filepath.Clean(root)
			w.roots[root] = l.ID
			n += addTree(fw, root)
		}
	}
	s.mu.Lock()
	s.watcher = w
	s.mu.Unlock()
	go s.watchLoop(wctx, w)
	log.Printf("目录监听已启动，共监听 %d 个目录", n)
}

func (s *Service) stopWatcher() {
	s.mu.Lock()
	w := s.watcher
	s.watcher = nil
	s.mu.Unlock()
	if w == nil {
		return
	}
	w.cancel()
	w.w.Close()
	w.mu.Lock()
	for _, t := range w.timers {
		t.Stop()
	}
	w.mu.Unlock()
}

func addTree(fw *fsnotify.Watcher, root string) int {
	n := 0
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if path != root && (strings.HasPrefix(d.Name(), ".") || skipDirs[d.Name()]) {
			return filepath.SkipDir
		}
		if fw.Add(path) == nil {
			n++
		}
		return nil
	})
	return n
}

func (s *Service) watchLoop(ctx context.Context, w *watcher) {
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			log.Printf("目录监听错误: %v", err)
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			lib := w.libraryOf(ev.Name)
			if lib == 0 {
				continue
			}
			if ev.Has(fsnotify.Create) {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					addTree(w.w, ev.Name) // 新建的子目录也要监听
				}
			}
			ext := strings.ToLower(filepath.Ext(ev.Name))
			_, isVideo := VideoExts[ext]
			relevant := isVideo || subExts[ext] || ext == ".nfo" || ext == ""
			if !relevant {
				continue
			}
			w.mu.Lock()
			if t, ok := w.timers[lib]; ok {
				t.Reset(debounce)
			} else {
				id := lib
				w.timers[lib] = time.AfterFunc(debounce, func() {
					w.mu.Lock()
					delete(w.timers, id)
					w.mu.Unlock()
					if ctx.Err() == nil {
						s.EnqueueScan(id, false)
					}
				})
			}
			w.mu.Unlock()
		}
	}
}

func (w *watcher) libraryOf(path string) int64 {
	best, id := "", int64(0)
	for root, lib := range w.roots {
		if (path == root || strings.HasPrefix(path, root+string(filepath.Separator))) && len(root) > len(best) {
			best, id = root, lib
		}
	}
	return id
}
