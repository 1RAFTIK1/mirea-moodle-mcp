package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeHome(t *testing.T) string {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h) // os.UserHomeDir on Windows
	t.Setenv("APPDATA", filepath.Join(h, "AppData"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(h, "AppData", "Local"))
	t.Setenv("PATH", "") // no client CLIs → file path is used
	return h
}

func TestWriteJSONKeepsOtherServers(t *testing.T) {
	fakeHome(t)
	c := findClient("cursor")
	p := c.path()
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(`{"mcpServers":{"other":{"command":"x"}},"theme":"dark"}`), 0o644)
	if _, err := install(c); err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	b, _ := os.ReadFile(p)
	json.Unmarshal(b, &cfg)
	srv := cfg["mcpServers"].(map[string]any)
	if srv["other"] == nil || srv[srvName] == nil || cfg["theme"] != "dark" {
		t.Fatalf("bad config: %s", b)
	}
	bk, _ := filepath.Glob(p + ".bak-*")
	if len(bk) != 1 {
		t.Fatal("no backup")
	}
}

func TestVSCodeShape(t *testing.T) {
	fakeHome(t)
	c := findClient("vscode")
	if _, err := install(c); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(c.path())
	if !strings.Contains(string(b), `"servers"`) || !strings.Contains(string(b), `"type": "stdio"`) {
		t.Fatalf("bad vscode config: %s", b)
	}
}

func TestJSONCNotTouched(t *testing.T) {
	fakeHome(t)
	c := findClient("zed")
	p := c.path()
	os.MkdirAll(filepath.Dir(p), 0o755)
	orig := "// my settings\n{\"theme\": \"One Dark\"}\n"
	os.WriteFile(p, []byte(orig), 0o644)
	_, err := install(c)
	if err == nil || !strings.Contains(err.Error(), "context_servers") {
		t.Fatalf("want manual snippet error, got %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != orig {
		t.Fatal("JSONC file was modified")
	}
}

func TestCodexTOML(t *testing.T) {
	h := fakeHome(t)
	c := findClient("codex")
	p := filepath.Join(h, ".codex", "config.toml")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte("model = \"o4\"\n\n[mcp_servers.mirea-moodle]\ncommand = 'old'\n\n[mcp_servers.other]\ncommand = 'x'\n"), 0o644)
	if _, err := install(c); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	s := string(b)
	if strings.Contains(s, "'old'") || strings.Count(s, "[mcp_servers.mirea-moodle]") != 1 || !strings.Contains(s, "[mcp_servers.other]") || !strings.Contains(s, `model = "o4"`) {
		t.Fatalf("bad toml:\n%s", s)
	}
	// second run is idempotent
	install(c)
	if b2, _ := os.ReadFile(p); string(b2) != s {
		t.Fatalf("not idempotent:\n%s\n---\n%s", s, b2)
	}
}

func rpc(t *testing.T, h http.Handler, path, auth, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json, text/event-stream")
	if auth != "" {
		r.Header.Set("Authorization", auth)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHTTPTransport(t *testing.T) {
	ping := &tool{Name: "ping2", Schema: schema(obj{}), fn: func(json.RawMessage) (any, error) { return "pong", nil }}
	h := &httpSrv{s: newServer([]*tool{ping}), token: "secret0123456789", origins: map[string]bool{}}
	init := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`

	if w := rpc(t, h, "/mcp", "", init); w.Code != 401 {
		t.Fatalf("no auth: %d", w.Code)
	}
	if w := rpc(t, h, "/mcp/wrong", "", init); w.Code != 401 {
		t.Fatalf("wrong token: %d", w.Code)
	}
	for _, c := range []struct{ path, auth string }{{"/mcp/secret0123456789", ""}, {"/mcp", "Bearer secret0123456789"}} {
		w := rpc(t, h, c.path, c.auth, init)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"protocolVersion":"2025-06-18"`) {
			t.Fatalf("%v: %d %s", c, w.Code, w.Body)
		}
	}
	if w := rpc(t, h, "/mcp/secret0123456789", "", `{"jsonrpc":"2.0","method":"notifications/initialized"}`); w.Code != 202 || w.Body.Len() != 0 {
		t.Fatalf("notification: %d %q", w.Code, w.Body)
	}
	w := rpc(t, h, "/mcp/secret0123456789", "", `{"jsonrpc":"2.0","id":"a","method":"tools/call","params":{"name":"ping2","arguments":{}}}`)
	var r struct {
		ID     string `json:"id"`
		Result struct {
			Content []struct{ Text string } `json:"content"`
		} `json:"result"`
	}
	json.Unmarshal(w.Body.Bytes(), &r)
	if r.ID != "a" || len(r.Result.Content) == 0 || r.Result.Content[0].Text != "pong" {
		t.Fatalf("tools/call: %s", w.Body)
	}
	// GET is not supported (no SSE stream) → 405
	g := httptest.NewRecorder()
	gr := httptest.NewRequest("GET", "/mcp/secret0123456789", nil)
	h.ServeHTTP(g, gr)
	if g.Code != 405 {
		t.Fatalf("GET: %d", g.Code)
	}
	// browser from a foreign origin → 403
	fr := httptest.NewRequest("POST", "/mcp/secret0123456789", bytes.NewBufferString(init))
	fr.Header.Set("Origin", "https://evil.example")
	fw := httptest.NewRecorder()
	h.ServeHTTP(fw, fr)
	if fw.Code != 403 {
		b, _ := io.ReadAll(fw.Body)
		t.Fatalf("origin: %d %s", fw.Code, b)
	}
}
