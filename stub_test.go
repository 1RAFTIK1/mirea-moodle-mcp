package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// stubSess points a session at a fake Moodle.
func stubSess(t *testing.T, h http.HandlerFunc) *sess {
	fakeHome(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	s := newSess(srv.URL)
	s.key = "KEY"
	return s
}

func ajaxReq(r *http.Request) (string, map[string]any) {
	var q []struct {
		Method string         `json:"methodname"`
		Args   map[string]any `json:"args"`
	}
	json.NewDecoder(r.Body).Decode(&q)
	return q[0].Method, q[0].Args
}

func TestActionEventsPaging(t *testing.T) {
	var calls []any
	s := stubSess(t, func(w http.ResponseWriter, r *http.Request) {
		_, a := ajaxReq(r)
		calls = append(calls, a["aftereventid"])
		n, start := 50, 1
		if a["aftereventid"] != nil {
			n, start = 7, 51
		}
		var ev []map[string]any
		for i := 0; i < n; i++ {
			ev = append(ev, map[string]any{"id": start + i, "name": fmt.Sprint("e", start+i), "timesort": 1800000000 + i})
		}
		json.NewEncoder(w).Encode([]any{map[string]any{"error": false, "data": map[string]any{"events": ev}}})
	})
	evs, err := s.actionEvents(0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 57 || len(calls) != 2 || calls[1].(float64) != 50 {
		t.Fatalf("got %d events, calls %v", len(evs), calls)
	}
}

func TestExpiredSession(t *testing.T) {
	s := stubSess(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"error":"…","errorcode":"invalidsesskey"}`)
	})
	if err := s.call("x", nil, nil); err != errExpired {
		t.Fatalf("want errExpired, got %v", err)
	}
}

func TestUploadMultipart(t *testing.T) {
	var gotLen int64
	var fields map[string]string
	var content string
	s := stubSess(t, func(w http.ResponseWriter, r *http.Request) {
		gotLen = r.ContentLength
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Errorf("multipart: %v", err)
			return
		}
		fields = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			fields[k] = v[0]
		}
		f, h, _ := r.FormFile("repo_upload_file")
		b, _ := io.ReadAll(f)
		content = h.Filename + ":" + string(b)
		io.WriteString(w, `{"url":"x","id":1,"file":"a.txt"}`)
	})
	p := filepath.Join(t.TempDir(), "отчёт.txt")
	data := strings.Repeat("данные ", 50000) // ~650 KB
	os.WriteFile(p, []byte(data), 0o644)
	fo := fmOpts{ItemID: 42, ClientID: "c1", MaxBytes: "20971520", Author: "Иван"}
	fo.Ctx.ID = 7
	if err := s.upload(p, "4", fo, "KEY"); err != nil {
		t.Fatal(err)
	}
	if content != "отчёт.txt:"+data {
		t.Fatalf("file content mismatch (%d bytes)", len(content))
	}
	if fields["itemid"] != "42" || fields["repo_id"] != "4" || fields["sesskey"] != "KEY" || fields["author"] != "Иван" || fields["license"] != "allrightsreserved" {
		t.Fatalf("fields: %v", fields)
	}
	if gotLen <= int64(len(data)) {
		t.Fatalf("Content-Length not set: %d", gotLen)
	}
}

func TestCachedSingleflight(t *testing.T) {
	dropCache("t:")
	var n int32
	var mu sync.Mutex
	f := func() (int, error) {
		mu.Lock()
		n++
		mu.Unlock()
		time.Sleep(50 * time.Millisecond)
		return 7, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); cached("t:a", time.Minute, f) }()
	}
	wg.Wait()
	if v, _ := cached("t:a", time.Minute, f); v != 7 || n != 1 {
		t.Fatalf("calls=%d v=%d", n, v)
	}
	// errors are not cached
	fails := 0
	g := func() (int, error) { fails++; return 0, errors.New("x") }
	cached("t:b", time.Minute, g)
	cached("t:b", time.Minute, g)
	if fails != 2 {
		t.Fatalf("error was cached: %d", fails)
	}
	// TTL
	cached("t:c", time.Nanosecond, f)
	time.Sleep(time.Millisecond)
	cached("t:c", time.Nanosecond, f)
	if n != 3 {
		t.Fatalf("ttl: calls=%d", n)
	}
}

func TestSessionHotReload(t *testing.T) {
	fakeHome(t)
	os.MkdirAll(cfgDir(), 0o700)
	os.WriteFile(sessPath(), []byte(`{"moodle_session":"old","sesskey":"K1","uid":1}`), 0o600)
	s := newSess("https://example.invalid")
	if s.key != "K1" || s.cookie() != "old" {
		t.Fatalf("load: %s %s", s.key, s.cookie())
	}
	// another process (`mirea-moodle-mcp cookie …`) writes a new session
	os.WriteFile(sessPath(), []byte(`{"moodle_session":"new","sesskey":"K2","uid":1}`), 0o600)
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(sessPath(), future, future)
	s.refresh()
	if s.key != "K2" || s.cookie() != "new" {
		t.Fatalf("reload: %s %s", s.key, s.cookie())
	}
}
