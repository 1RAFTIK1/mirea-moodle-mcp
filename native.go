package main

import (
	"embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Browser extension bridge (Native Messaging).
//
// The extension in ./extension reads the MoodleSession cookie the browser got
// after a normal SSO login and hands it to this binary; we never log in by
// ourselves. The browser starts us as a "native host": stdin/stdout carry
// messages framed as uint32 little-endian length + UTF-8 JSON. Only the
// extension ID listed in allowed_origins may launch us (enforced by the browser).
//
// Running MCP servers pick the new cookie up via sess.refresh() (session.json mtime).

const nativeHost = "ru.mirea.moodle_mcp"

// extID is derived from the public "key" in extension/manifest.json, so the ID
// is the same for every unpacked install.
const extID = "hfnkcippkhmahfiahihmncblkmfanibd"

//go:embed extension
var extFS embed.FS

// isNativeLaunch: Chromium passes the caller origin as the first argument
// (on Windows followed by --parent-window=N).
func isNativeLaunch(args []string) bool {
	return len(args) > 0 && strings.HasPrefix(args[0], "chrome-extension://")
}

const maxNativeMsg = 64 << 10 // we only ever receive a cookie

func readNative(r io.Reader) ([]byte, error) {
	var n uint32
	if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
		return nil, err
	}
	if n == 0 || n > maxNativeMsg {
		return nil, fmt.Errorf("native message: bad length %d", n)
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}

func writeNative(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := binary.Write(w, binary.LittleEndian, uint32(len(b))); err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

type nativeMsg struct {
	Type   string `json:"type"`
	Cookie string `json:"cookie"`
	Force  bool   `json:"force"`
}

// handleNative answers one message. Replies never contain the cookie or sesskey.
func handleNative(s *sess, m nativeMsg) obj {
	switch m.Type {
	case "ping":
		return obj{"ok": true, "version": version}
	case "status":
		if err := ping(s); err != nil {
			return obj{"ok": false, "needLogin": errors.Is(err, errExpired), "error": err.Error()}
		}
		return obj{"ok": true, "uid": s.uid, "version": version}
	case "set_cookie":
		v := strings.TrimSpace(m.Cookie)
		s.mu.Lock()
		same := v != "" && v == s.cookie() && s.key != ""
		s.mu.Unlock()
		if same && !m.Force {
			return obj{"ok": true, "unchanged": true, "uid": s.uid, "version": version}
		}
		name, err := s.setCookie(v)
		if err != nil {
			// The old session.json stays untouched: an anonymous pre-login
			// cookie must not overwrite a working session.
			s.load()
			return obj{"ok": false, "error": err.Error()}
		}
		return obj{"ok": true, "name": name, "uid": s.uid, "version": version}
	}
	return obj{"ok": false, "error": "неизвестная команда " + trunc(m.Type, 40)}
}

func runNativeHost(s *sess, in io.Reader, out io.Writer) int {
	for {
		b, err := readNative(in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0
			}
			return 1
		}
		var m nativeMsg
		r := obj{"ok": false, "error": "плохой JSON"}
		if json.Unmarshal(b, &m) == nil {
			r = handleNative(s, m)
		}
		if writeNative(out, r) != nil {
			return 1
		}
	}
}

// --- install ---

type nmBrowser struct {
	Name string
	dir  string // profile root; manifest goes to <dir>/NativeMessagingHosts (darwin/linux)
	reg  string // HKCU key prefix (windows)
}

func nmBrowsers() []nmBrowser {
	h := home()
	switch runtime.GOOS {
	case "darwin":
		as := filepath.Join(h, "Library", "Application Support")
		return []nmBrowser{
			{Name: "Яндекс Браузер", dir: filepath.Join(as, "Yandex", "YandexBrowser")},
			{Name: "Google Chrome", dir: filepath.Join(as, "Google", "Chrome")},
			{Name: "Chromium", dir: filepath.Join(as, "Chromium")},
			{Name: "Microsoft Edge", dir: filepath.Join(as, "Microsoft Edge")},
			{Name: "Brave", dir: filepath.Join(as, "BraveSoftware", "Brave-Browser")},
			{Name: "Vivaldi", dir: filepath.Join(as, "Vivaldi")},
			{Name: "Arc", dir: filepath.Join(as, "Arc", "User Data")},
		}
	case "windows":
		return []nmBrowser{
			{Name: "Яндекс Браузер", reg: `HKCU\Software\Yandex\YandexBrowser`},
			{Name: "Google Chrome (и Brave, Vivaldi)", reg: `HKCU\Software\Google\Chrome`},
			{Name: "Chromium", reg: `HKCU\Software\Chromium`},
			{Name: "Microsoft Edge", reg: `HKCU\Software\Microsoft\Edge`},
		}
	}
	cfg := filepath.Join(h, ".config")
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		cfg = x
	}
	return []nmBrowser{
		{Name: "Яндекс Браузер", dir: filepath.Join(cfg, "yandex-browser")},
		{Name: "Google Chrome", dir: filepath.Join(cfg, "google-chrome")},
		{Name: "Chromium", dir: filepath.Join(cfg, "chromium")},
		{Name: "Microsoft Edge", dir: filepath.Join(cfg, "microsoft-edge")},
		{Name: "Brave", dir: filepath.Join(cfg, "BraveSoftware", "Brave-Browser")},
		{Name: "Vivaldi", dir: filepath.Join(cfg, "vivaldi")},
	}
}

func hostManifest(exe string, ids []string) []byte {
	var origins []string
	for _, id := range ids {
		origins = append(origins, "chrome-extension://"+id+"/")
	}
	b, _ := json.MarshalIndent(obj{
		"name":            nativeHost,
		"description":     "mirea-moodle-mcp: приём cookie MoodleSession от расширения браузера",
		"path":            exe,
		"type":            "stdio",
		"allowed_origins": origins,
	}, "", "  ")
	return append(b, '\n')
}

func exePath() (string, error) {
	e, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(e); err == nil {
		e = r
	}
	if strings.Contains(e, "go-build") {
		return "", errors.New("бинарник во временной папке `go run`: собери его (go build) или установи через install.sh и запусти оттуда")
	}
	return e, nil
}

func extDir() string { return filepath.Join(cfgDir(), "extension") }

// unpackExtension writes the embedded extension to extDir() (overwriting).
func unpackExtension() (string, error) {
	dst := extDir()
	os.RemoveAll(dst)
	err := fs.WalkDir(extFS, "extension", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		t := filepath.Join(dst, filepath.FromSlash(strings.TrimPrefix(p, "extension")))
		if d.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		b, err := extFS.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(t, b, 0o644)
	})
	return dst, err
}

// installHost registers the native host for every detected browser.
func installHost(exe string, ids []string) ([]string, error) {
	man := hostManifest(exe, ids)
	var done []string
	if runtime.GOOS == "windows" {
		f := filepath.Join(cfgDir(), nativeHost+".json")
		if err := os.MkdirAll(cfgDir(), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(f, man, 0o644); err != nil {
			return nil, err
		}
		for _, b := range nmBrowsers() {
			k := b.reg + `\NativeMessagingHosts\` + nativeHost
			if out, err := exec.Command("reg", "add", k, "/ve", "/t", "REG_SZ", "/d", f, "/f").CombinedOutput(); err != nil {
				return done, fmt.Errorf("reg add %s: %v %s", k, err, out)
			}
			done = append(done, b.Name)
		}
		return done, nil
	}
	for _, b := range nmBrowsers() {
		if !exists(b.dir) {
			continue
		}
		d := filepath.Join(b.dir, "NativeMessagingHosts")
		if err := os.MkdirAll(d, 0o755); err != nil {
			return done, err
		}
		if err := os.WriteFile(filepath.Join(d, nativeHost+".json"), man, 0o644); err != nil {
			return done, err
		}
		done = append(done, b.Name)
	}
	return done, nil
}

func uninstallHost() {
	if runtime.GOOS == "windows" {
		for _, b := range nmBrowsers() {
			exec.Command("reg", "delete", b.reg+`\NativeMessagingHosts\`+nativeHost, "/f").Run()
		}
		os.Remove(filepath.Join(cfgDir(), nativeHost+".json"))
		return
	}
	for _, b := range nmBrowsers() {
		os.Remove(filepath.Join(b.dir, "NativeMessagingHosts", nativeHost+".json"))
	}
}

func cmdExtension(args []string) error {
	sub := "install"
	if len(args) > 0 {
		sub = args[0]
	}
	ids := []string{extID}
	for i := 1; i < len(args); i++ { // --id <ID>: extra IDs (e.g. a store-published build)
		if args[i] == "--id" && i+1 < len(args) {
			ids = append(ids, args[i+1])
			i++
		}
	}
	switch sub {
	case "uninstall", "remove":
		uninstallHost()
		os.RemoveAll(extDir())
		fmt.Println("✓ связка с браузером удалена; само расширение удали на странице расширений браузера")
		return nil
	case "path":
		fmt.Println(extDir())
		return nil
	case "install":
	default:
		return fmt.Errorf("неизвестная подкоманда %q (install | uninstall | path)", sub)
	}
	exe, err := exePath()
	if err != nil {
		return err
	}
	dir, err := unpackExtension()
	if err != nil {
		return err
	}
	got, err := installHost(exe, ids)
	if err != nil {
		return err
	}
	if len(got) == 0 {
		return errors.New("не нашёл Chromium-браузеров (Chrome, Яндекс, Edge, Brave, Vivaldi, Arc). Firefox и Safari пока не поддерживаются")
	}
	fmt.Println("✓ программа зарегистрирована для:", strings.Join(got, ", "))
	fmt.Println("✓ расширение распаковано в:", dir)
	fmt.Print(`
Осталось один раз загрузить расширение в браузер:
  1) открой страницу расширений: browser://extensions (Яндекс), chrome://extensions (Chrome),
     edge://extensions (Edge) — адрес вставь в адресную строку вручную;
  2) включи «Режим разработчика»;
  3) «Загрузить распакованное расширение» → выбери папку выше
     (или перетащи её на страницу расширений);
  4) закрепи значок «Moodle MCP», войди на online-edu.mirea.ru и нажми «Подключить».

Дальше cookie обновляется сама при каждом входе в Moodle. Перезапусти браузер, если он был открыт.
После обновления mirea-moodle-mcp запусти эту команду снова и нажми «Обновить» у расширения.
`)
	if runtime.GOOS == "darwin" {
		exec.Command("open", "-R", dir).Start()
	}
	return nil
}
