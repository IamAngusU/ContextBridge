package cluster

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"net/http"
)

//go:embed dashboard.html
var dashboardHTML []byte

var dashboardScriptPolicy = func() string {
	start := bytes.Index(dashboardHTML, []byte("<script>"))
	end := bytes.LastIndex(dashboardHTML, []byte("</script>"))
	if start < 0 || end <= start {
		return "'none'"
	}
	script := dashboardHTML[start+len("<script>") : end]
	digest := sha256.Sum256(script)
	return "'sha256-" + base64.StdEncoding.EncodeToString(digest[:]) + "'"
}()

func (r *Relay) handleDashboard(w http.ResponseWriter, req *http.Request) {
	if req.URL.Path != "/" {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(dashboardHTML)
}
