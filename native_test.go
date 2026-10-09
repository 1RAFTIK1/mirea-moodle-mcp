package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
