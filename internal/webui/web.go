package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist placeholder.html
var files embed.FS

func Handler() http.Handler {
	root, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err)
	}
	assets := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			assets.ServeHTTP(w, r)
			return
		}
		if isConsoleRoute(r.URL.Path) {
			data, err := fs.ReadFile(root, "index.html")
			if err != nil {
				data, err = files.ReadFile("placeholder.html")
				if err != nil {
					http.NotFound(w, r)
					return
				}
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(data)
			return
		}
		http.NotFound(w, r)
	})
}

func isConsoleRoute(path string) bool {
	switch path {
	case "/", "/auth/callback", "/devices", "/sessions", "/agent-setup", "/downloads", "/audit":
		return true
	}
	if strings.HasPrefix(path, "/devices/") {
		id := strings.TrimPrefix(path, "/devices/")
		return id != "" && !strings.Contains(id, "/") && !strings.Contains(id, ".")
	}
	return false
}
