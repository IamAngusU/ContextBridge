//go:build windows

package updater

import (
	"encoding/base64"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Exercise the real helper in an isolated directory, including a failed
// post-restart health check and restoration of the previous executable.
func TestWindowsHelperRollsBackFailedHealth(t *testing.T) {
	temporary := t.TempDir()
	source := filepath.Join(temporary, "main.go")
	program := `package main
import ("fmt"; "os")
var version = "dev"
func main() { if len(os.Args) > 1 && os.Args[1] == "version" { fmt.Println(version) } }
`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(temporary, "contextbridge.exe")
	next := current + ".next.exe"
	backup := current + ".previous.exe"
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go.exe")
	for _, item := range []struct{ version, target string }{{"v0.5.4", current}, {"v0.5.5", next}} {
		command := exec.Command(goBinary, "build", "-ldflags", "-X main.version="+item.version, "-o", item.target, source)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build helper fixture: %v: %s", err, output)
		}
	}
	script := filepath.Join(temporary, "update.ps1")
	if err := os.WriteFile(script, []byte(windowsHelperBody(t)), 0600); err != nil {
		t.Fatal(err)
	}
	failure := filepath.Join(temporary, "update-failed.json")
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"version":"v0.5.4"}`))
	}))
	command := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script,
		current, next, backup, "v0.5.5", "0", base64.StdEncoding.EncodeToString([]byte("")), service.URL, failure, "2")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("running manual terminal should block update: %s", output)
	}
	if output, err := exec.Command(current, "version").CombinedOutput(); err != nil || strings.TrimSpace(string(output)) != "v0.5.4" {
		t.Fatalf("blocked manual update changed the executable: %q %v", output, err)
	}
	service.Close()
	command = exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script,
		current, next, backup, "v0.5.5", "0", base64.StdEncoding.EncodeToString([]byte("run")), "http://127.0.0.1:1/health", failure, "2")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("health failure should exit nonzero: %s", output)
	}
	output, err := exec.Command(current, "version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "v0.5.4" {
		t.Fatalf("previous executable not restored: version=%q err=%v", output, err)
	}
	marker, err := os.ReadFile(failure)
	var recorded struct {
		Version string `json:"version"`
	}
	if err != nil || json.Unmarshal(marker, &recorded) != nil || recorded.Version != "v0.5.5" {
		t.Fatalf("failed release was not quarantined: %q err=%v", marker, err)
	}
	if err := copyFile(current+".failed.exe", next, 0700); err != nil {
		t.Fatal(err)
	}
	command = exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", script,
		current, next, backup, "v0.5.5", "0", base64.StdEncoding.EncodeToString([]byte("")), "http://127.0.0.1:1/health", failure, "2")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("stopped manual terminal should allow verified update: %v: %s", err, output)
	}
	output, err = exec.Command(current, "version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != "v0.5.5" {
		t.Fatalf("new executable not activated: version=%q err=%v", output, err)
	}
	if _, err := os.Stat(failure); !os.IsNotExist(err) {
		t.Fatalf("failed-release marker should be cleared after a healthy install: %v", err)
	}
}

func windowsHelperBody(t *testing.T) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "replace_windows.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "replaceExecutable" {
			continue
		}
		for _, statement := range function.Body.List {
			assignment, ok := statement.(*ast.AssignStmt)
			if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
				continue
			}
			name, ok := assignment.Lhs[0].(*ast.Ident)
			if !ok || name.Name != "body" {
				continue
			}
			literal, ok := assignment.Rhs[0].(*ast.BasicLit)
			if !ok {
				continue
			}
			body, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			return body
		}
	}
	t.Fatal("update helper script not found")
	return ""
}
