package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func nativeRoundTrip(t *testing.T, s *sess, msgs ...any) []map[string]any {
	t.Helper()
	var in, out bytes.Buffer
	for _, m := range msgs {
		if err := writeNative(&in, m); err != nil {
			t.Fatal(err)
		}
	}
	if code := runNativeHost(s, &in, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var res []map[string]any
	for {
		b, err := readNative(&out)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		res = append(res, m)
	}
	return res
}

func moodleFake(good string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie("MoodleSession")
		if r.URL.Path == "/my/" {
			if c == nil || c.Value != good {
				http.Redirect(w, r, "/login/index.php", http.StatusSeeOther)
				return
			}
			io.WriteString(w, `<script>M.cfg = {"sesskey":"SK1","userId":42};</script><span class="usertext mr-1">Иванов Иван</span>`)
			return
		}
		if r.URL.Path == "/lib/ajax/service.php" && c != nil && c.Value == good && r.URL.Query().Get("sesskey") == "SK1" {
			io.WriteString(w, `[{"error":false,"data":{"courses":[]}}]`)
			return
		}
		io.WriteString(w, `login page`)
	}
}

func TestNativeSetCookie(t *testing.T) {
	s := stubSess(t, moodleFake("GOOD"))
	s.key = ""
	r := nativeRoundTrip(t, s,
		map[string]any{"type": "ping"},
		map[string]any{"type": "set_cookie", "cookie": "GOOD"},
		map[string]any{"type": "set_cookie", "cookie": "GOOD"},     // unchanged → no network
		map[string]any{"type": "set_cookie", "cookie": "ANON-PRE"}, // pre-login cookie must not overwrite
		map[string]any{"type": "nope"},
	)
	if len(r) != 5 || r[0]["ok"] != true {
		t.Fatalf("replies %v", r)
	}
	if r[1]["ok"] != true || r[1]["name"] != "Иванов Иван" || r[1]["uid"].(float64) != 42 {
		t.Fatalf("set_cookie: %v", r[1])
	}
	if r[2]["unchanged"] != true {
		t.Fatalf("second set_cookie should be unchanged: %v", r[2])
	}
	if r[3]["ok"] != false || r[4]["ok"] != false {
		t.Fatalf("bad cookie / unknown type accepted: %v %v", r[3], r[4])
	}
	b, _ := os.ReadFile(sessPath())
	if !strings.Contains(string(b), `"GOOD"`) || s.cookie() != "GOOD" {
		t.Fatalf("session.json lost working cookie: %s", b)
	}
	for _, m := range r {
		for _, v := range m {
			if v == "GOOD" || v == "SK1" {
				t.Fatalf("secret leaked in reply %v", m)
			}
		}
	}
}

func TestNativeBadLength(t *testing.T) {
	in := bytes.NewReader([]byte{0xff, 0xff, 0xff, 0x7f})
	if code := runNativeHost(stubSess(t, moodleFake("x")), in, io.Discard); code != 1 {
		t.Fatalf("oversized message accepted")
	}
}

func TestIsNativeLaunch(t *testing.T) {
	if !isNativeLaunch([]string{"chrome-extension://" + extID + "/", "--parent-window=0"}) {
		t.Fatal("chrome launch not detected")
	}
	if isNativeLaunch(nil) || isNativeLaunch([]string{"status"}) {
		t.Fatal("false positive")
	}
}

func TestInstallHostManifest(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("registry")
	}
	fakeHome(t)
	var dir string
	for _, b := range nmBrowsers() {
		if strings.Contains(b.Name, "Chrome") {
			dir = b.dir
		}
	}
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "Local State"), []byte("{}"), 0o644)
	// an empty profile dir (left by another native-host installer) is not a browser
	for _, b := range nmBrowsers() {
		if strings.Contains(b.Name, "Edge") {
			os.MkdirAll(filepath.Join(b.dir, "NativeMessagingHosts"), 0o755)
		}
	}
	got, err := installHost("/opt/mm", []string{extID})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v err %v", got, err)
	}
	f := filepath.Join(dir, "NativeMessagingHosts", nativeHost+".json")
	var m struct {
		Name, Path, Type string
		Origins          []string `json:"allowed_origins"`
	}
	b, _ := os.ReadFile(f)
	if json.Unmarshal(b, &m) != nil || m.Name != nativeHost || m.Path != "/opt/mm" || m.Type != "stdio" ||
		len(m.Origins) != 1 || m.Origins[0] != "chrome-extension://"+extID+"/" {
		t.Fatalf("manifest %s", b)
	}
	uninstallHost()
	if exists(f) {
		t.Fatal("not removed")
	}
}

func TestEmbeddedExtension(t *testing.T) {
	fakeHome(t)
	d, err := unpackExtension()
	if err != nil {
		t.Fatal(err)
	}
	var man struct {
		Key  string   `json:"key"`
		Perm []string `json:"permissions"`
	}
	b, _ := os.ReadFile(filepath.Join(d, "manifest.json"))
	if err := json.Unmarshal(b, &man); err != nil || man.Key == "" {
		t.Fatalf("manifest: %v", err)
	}
	for _, f := range []string{"background.js", "popup.html", "popup.js"} {
		if !exists(filepath.Join(d, f)) {
			t.Fatal("missing", f)
		}
	}
	if !strings.Contains(string(b), "nativeMessaging") {
		t.Fatal("no nativeMessaging permission")
	}
	// Chromium: ID = first 16 bytes of sha256(public key DER), hex digits 0-f → a-p.
	der, err := base64.StdEncoding.DecodeString(man.Key)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(der)
	id := []byte(hex.EncodeToString(h[:16]))
	for i, c := range id {
		if c <= '9' {
			id[i] = 'a' + c - '0'
		} else {
			id[i] = 'a' + 10 + c - 'a'
		}
	}
	if string(id) != extID {
		t.Fatalf("extID %s, key gives %s", extID, id)
	}
}

// fakeSetupEnv: Chrome "installed", no real browser/Finder, scripted terminal input.
func fakeSetupEnv(t *testing.T) chan string {
	if runtime.GOOS == "windows" {
		t.Skip("registry")
	}
	for _, b := range nmBrowsers() {
		if strings.Contains(b.Name, "Chrome") {
			os.MkdirAll(b.dir, 0o755)
			os.WriteFile(filepath.Join(b.dir, "Local State"), []byte("{}"), 0o644)
		}
	}
	oo, oe, or, ow, op := openURL, exePath, reveal, extWait, extPoll
	openURL, reveal = func(string) {}, func(string) {}
	exePath = func() (string, error) { return "/opt/mm", nil }
	extWait, extPoll = 5*time.Second, 20*time.Millisecond
	in := make(chan string, 4)
	lineOnce = sync.Once{}
	lineOnce.Do(func() { lineCh = in })
	t.Cleanup(func() {
		openURL, exePath, reveal, extWait, extPoll = oo, oe, or, ow, op
		lineOnce = sync.Once{}
	})
	return in
}

func TestSetupWaitsForExtension(t *testing.T) {
	s := stubSess(t, moodleFake("GOOD"))
	s.key = ""
	in := fakeSetupEnv(t)
	in <- "" // [Y/n] → yes
	go func() {
		time.Sleep(100 * time.Millisecond)
		// what the browser does: start the host and hand it the cookie
		host := newSess(s.base)
		if r := handleNative(host, nativeMsg{Type: "set_cookie", Cookie: "GOOD"}); r["ok"] != true {
			t.Errorf("host: %v", r)
		}
	}()
	if !sessionViaExtension(s) {
		t.Fatal("setup did not notice the extension")
	}
	if s.cookie() != "GOOD" || s.uid != 42 {
		t.Fatalf("session not reloaded: uid %d", s.uid)
	}
	if !exists(filepath.Join(extDir(), "manifest.json")) {
		t.Fatal("extension not unpacked")
	}
}

func TestSetupExtensionEnterFallsBack(t *testing.T) {
	s := stubSess(t, moodleFake("GOOD"))
	in := fakeSetupEnv(t)
	in <- ""
	in <- "" // Enter while waiting → manual
	if sessionViaExtension(s) {
		t.Fatal("expected fallback to manual")
	}
}

func TestSetupExtensionDeclined(t *testing.T) {
	s := stubSess(t, moodleFake("GOOD"))
	in := fakeSetupEnv(t)
	in <- "n"
	if sessionViaExtension(s) || exists(extDir()) {
		t.Fatal("declined but extension was set up")
	}
}

func TestNativeDeadlines(t *testing.T) {
	now := time.Now().Unix()
	s := stubSess(t, func(w http.ResponseWriter, r *http.Request) {
		m, _ := ajaxReq(r)
		if m != "core_calendar_get_action_events_by_timesort" {
			t.Errorf("method %s", m)
		}
		ev := []map[string]any{{"id": 1, "name": "old", "timesort": now - 3600, "overdue": true, "course": map[string]any{"fullname": "C0"}}}
		for i := 1; i <= 6; i++ {
			ev = append(ev, map[string]any{"id": 1 + i, "name": fmt.Sprint("ПР", i, " - срок сдачи"), "timesort": now + int64(i)*3600,
				"url": "https://online-edu.mirea.ru/mod/assign/view.php?id=" + fmt.Sprint(i), "course": map[string]any{"fullname": "Курс"}})
		}
		json.NewEncoder(w).Encode([]any{map[string]any{"error": false, "data": map[string]any{"events": ev}}})
	})
	r := handleNative(s, nativeMsg{Type: "deadlines", Limit: 4})
	items, _ := r["items"].([]obj)
	if r["ok"] != true || len(items) != 4 || r["overdue"] != 1 || items[0]["name"] != "ПР1" || items[3]["name"] != "ПР4" || items[0]["course"] != "Курс" {
		t.Fatalf("got %v", r)
	}

	s2 := stubSess(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"error":"…","errorcode":"invalidsesskey"}`)
	})
	if r := handleNative(s2, nativeMsg{Type: "deadlines"}); r["ok"] != false || r["needLogin"] != true {
		t.Fatalf("expired: %v", r)
	}
}
