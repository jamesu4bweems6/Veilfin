// Package webui 内嵌构建好的前端页面，服务端（网页模式）和本地客户端共用。
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler 返回静态文件处理器。configJSON 生成 /config.json 的内容（告诉前端当前是哪种模式）。
func Handler(configJSON func(r *http.Request) []byte) http.Handler {
	sub, _ := fs.Sub(dist, "dist")
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean(r.URL.Path)
		switch {
		case p == "/config.json":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(configJSON(r))
			return
		case strings.HasPrefix(p, "/assets/"):
			// 文件名带内容哈希，可以长期缓存
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			w.Header().Set("Cache-Control", "no-cache")
		}
		if p == "/sw.js" {
			w.Header().Set("Service-Worker-Allowed", "/")
		}
		files.ServeHTTP(w, r)
	})
}
