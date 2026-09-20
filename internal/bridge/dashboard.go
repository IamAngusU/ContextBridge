package bridge

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dashboard/*
var dashboardFiles embed.FS

func dashboardHandler() http.Handler {
	root, err := fs.Sub(dashboardFiles, "dashboard")
	if err != nil {
		return http.NotFoundHandler()
	}
	return http.FileServer(http.FS(root))
}
