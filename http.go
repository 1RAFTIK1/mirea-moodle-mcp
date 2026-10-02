package main

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Streamable HTTP transport (MCP spec 2025-06-18), minimal and stateless:
//   POST /mcp  one JSON-RPC message → request: 200 application/json response
//                                    notification/response: 202, no body
//   GET  /mcp  405 (we don't open a server→client SSE stream)
// Auth: secret path /mcp/<token> or "Authorization: Bearer <token>".
// Clients that only take a URL (ChatGPT, claude.ai connectors) use the path form.

func tokenFile() string { return filepath.Join(cfgDir(), "http_token") }

func httpToken() (string, error) {
	if t := strings.TrimSpace(os.Getenv("MCP_HTTP_TOKEN")); t != "" {
		return t, nil
	}
	if b, err := os.ReadFile(tokenFile()); err == nil && len(strings.TrimSpace(string(b))) >= 16 {
		return strings.TrimSpace(string(b)), nil
	}
	r := make([]byte, 24)
	if _, err := rand.Read(r); err != nil {
		return "", err
	}
	t := hex.EncodeToString(r)
	os.MkdirAll(cfgDir(), 0o700)
	return t, os.WriteFile(tokenFile(), []byte(t+"\n"), 0o600)
}

func isLoopback(addr string) bool {
	h, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

type httpSrv struct {
	s       *server
	token   string
	origins map[string]bool
}

func (h *httpSrv) authed(r *http.Request) bool {
	if h.token == "" {
		return true
	}
	eq := func(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
	if p := strings.TrimPrefix(r.URL.Path, "/mcp/"); p != r.URL.Path && eq(strings.TrimSuffix(p, "/"), h.token) {
		return true
	}
	if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") && eq(strings.TrimPrefix(a, "Bearer "), h.token) {
		return true
	}
	return false
}

// originOK guards against DNS rebinding from browsers (spec requirement):
// requests without Origin (server-to-server) pass; browser ones only from
// localhost or explicitly allowed origins.
func (h *httpSrv) originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil {
		return false
	}
	hn := u.Hostname()
	if hn == "localhost" || hn == "127.0.0.1" || hn == "::1" {
		return true
	}
	return h.origins[strings.ToLower(o)]
}

func (h *httpSrv) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		io.WriteString(w, "ok\n")
		return
	}
	if r.URL.Path != "/mcp" && !strings.HasPrefix(r.URL.Path, "/mcp/") {
		http.NotFound(w, r)
		return
	}
	if !h.originOK(r) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if !h.authed(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodGet, http.MethodDelete:
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	default:
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	var q rpcReq
	if err := json.Unmarshal(body, &q); err != nil {
		writeJSON200(w, rpcResp{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcErr{-32700, "parse error"}})
		return
	}
	if len(q.ID) == 0 || q.Method == "" { // notification or a response to us
		w.WriteHeader(http.StatusAccepted)
		return
	}
	res, e := h.s.dispatch(q)
	writeJSON200(w, rpcResp{JSONRPC: "2.0", ID: q.ID, Result: res, Error: e})
}

func writeJSON200(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func serveHTTP(s *server, addr string, noAuth bool, origins []string) error {
	h := &httpSrv{s: s, origins: map[string]bool{}}
	for _, o := range origins {
		if o = strings.TrimSpace(strings.ToLower(o)); o != "" {
			h.origins[o] = true
		}
	}
	if noAuth {
		if !isLoopback(addr) {
			return errors.New("--no-auth разрешён только на 127.0.0.1/localhost")
		}
	} else {
		t, err := httpToken()
		if err != nil {
			return err
		}
		h.token = t
	}
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	base := "http://" + addr
	fmt.Fprintf(os.Stderr, "MCP (Streamable HTTP) слушает %s\n", addr)
	if h.token != "" {
		fmt.Fprintf(os.Stderr, "  URL для клиентов:   %s/mcp/%s\n  или заголовок:      Authorization: Bearer %s  →  %s/mcp\n", base, h.token, h.token, base)
		fmt.Fprintf(os.Stderr, "  Токен — доступ к твоему Moodle. Не публикуй URL. Сменить: удали %s\n", tokenFile())
	} else {
		fmt.Fprintf(os.Stderr, "  URL: %s/mcp (без авторизации, только локально)\n", base)
	}
	if !isLoopback(addr) {
		fmt.Fprintln(os.Stderr, "  ⚠ слушаю не только localhost — ставь HTTPS (туннель/реверс-прокси) перед публикацией")
	}
	return srv.ListenAndServe()
}
