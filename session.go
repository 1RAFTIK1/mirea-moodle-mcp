package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// A Moodle web session: the MoodleSession cookie the user copied from their
// own browser. Calls go to lib/ajax/service.php (with sesskey) and to normal
// pages. Nothing here logs in by itself.

type sess struct {
	base string
	hc   *http.Client
	jar  *cookiejar.Jar
	mu   sync.Mutex
	key  string // sesskey
	uid  int64
	mod  time.Time // mtime of session.json we loaded
}

type saved struct {
	Cookie string `json:"moodle_session"`
	Key    string `json:"sesskey"`
	UID    int64  `json:"uid"`
}

func sessPath() string { return filepath.Join(cfgDir(), "session.json") }

var errExpired = errors.New("сессия Moodle истекла или не задана: войди на online-edu.mirea.ru в браузере и выполни `mirea-moodle-mcp cookie <MoodleSession>` или нажми «Подключить» в расширении браузера (см. README)")

var (
	reSesskey = regexp.MustCompile(`"sesskey":"([^"]+)"`)
	reUID     = regexp.MustCompile(`"userId":(\d+)`)
	reUID2    = regexp.MustCompile(`data-userid="(\d+)"`)
	reFull    = regexp.MustCompile(`(?s)class="[^"]*usertext[^"]*"[^>]*>(.*?)</span>`)
)

func newSess(base string) *sess {
	jar, _ := cookiejar.New(nil)
	s := &sess{base: base, jar: jar, hc: newHTTP(jar)}
	s.load()
	return s
}

func (s *sess) setJar(cookie string) {
	u, _ := url.Parse(s.base)
	c := &http.Cookie{Name: "MoodleSession", Value: cookie, Path: "/"}
	if cookie == "" {
		c.MaxAge = -1
	}
	s.jar.SetCookies(u, []*http.Cookie{c}) // jar is concurrency-safe; same name+path replaces
}

func (s *sess) load() {
	st, err := os.Stat(sessPath())
	if err != nil {
		return
	}
	b, err := os.ReadFile(sessPath())
	if err != nil {
		return
	}
	s.mod = st.ModTime()
	var v saved
	if json.Unmarshal(b, &v) != nil {
		return
	}
	if v.Cookie == "" { // older format: {"cookies":[{"Name":..,"Value":..}]}
		var old struct {
			Cookies []struct{ Name, Value string } `json:"cookies"`
		}
		json.Unmarshal(b, &old)
		for _, c := range old.Cookies {
			if c.Name == "MoodleSession" {
				v.Cookie = c.Value
			}
		}
	}
	s.setJar(v.Cookie)
	s.key, s.uid = v.Key, v.UID
}

func (s *sess) cookie() string {
	u, _ := url.Parse(s.base)
	for _, c := range s.jar.Cookies(u) {
		if c.Name == "MoodleSession" {
			return c.Value
		}
	}
	return ""
}

func (s *sess) save() error {
	b, _ := json.Marshal(saved{s.cookie(), s.key, s.uid})
	if err := os.MkdirAll(cfgDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(sessPath(), b, 0o600)
}

// setCookie validates a MoodleSession value and stores it. Returns user name.
func (s *sess) setCookie(v string) (string, error) {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "MoodleSession="))
	if v == "" {
		return "", errors.New("пустое значение cookie")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setJar(v)
	rq, _ := http.NewRequest("GET", s.base+"/my/", nil)
	rs, err := s.do(rq, 60*time.Second, 60*time.Second)
	if err != nil {
		return "", err
	}
	defer rs.Body.Close()
	b, _ := io.ReadAll(rs.Body)
	page := string(b)
	k := reSesskey.FindStringSubmatch(page)
	if k == nil || strings.Contains(rs.Request.URL.String(), "/login/") {
		return "", errors.New("Moodle не принял эту сессию: проверь, что скопировано значение MoodleSession для online-edu.mirea.ru и что ты залогинен(а) в браузере")
	}
	s.key = k[1]
	if m := reUID.FindStringSubmatch(page); m != nil {
		fmt.Sscan(m[1], &s.uid)
	} else if m := reUID2.FindStringSubmatch(page); m != nil {
		fmt.Sscan(m[1], &s.uid)
	}
	name := ""
	if m := reFull.FindStringSubmatch(page); m != nil {
		name = strip(m[1])
	}
	if err := s.save(); err != nil {
		return name, err
	}
	if st, err := os.Stat(sessPath()); err == nil {
		s.mod = st.ModTime()
	}
	return name, nil
}

var errRelogin = errors.New("relogin")

// call runs one function through lib/ajax/service.php.
func (s *sess) call(fn string, args any, out any) error {
	s.refresh()
	s.mu.Lock()
	key := s.key
	s.mu.Unlock()
	if key == "" {
		return errExpired
	}
	if args == nil {
		args = obj{}
	}
	body, _ := json.Marshal([]obj{{"index": 0, "methodname": fn, "args": args}})
	u := fmt.Sprintf("%s/lib/ajax/service.php?sesskey=%s&info=%s", s.base, url.QueryEscape(key), fn)
	rq, _ := http.NewRequest("POST", u, bytes.NewReader(body))
	rq.Header.Set("Content-Type", "application/json")
	rs, err := s.do(rq, 60*time.Second, 60*time.Second)
	if err != nil {
		return fmt.Errorf("%s: %w", fn, err)
	}
	defer rs.Body.Close()
	b, _ := io.ReadAll(rs.Body)
	type exc struct {
		Msg  string `json:"message"`
		Code string `json:"errorcode"`
	}
	loginCodes := map[string]bool{"invalidsesskey": true, "servicerequireslogin": true, "requireloginerror": true}
	if bytes.HasPrefix(bytes.TrimSpace(b), []byte("{")) {
		var top struct {
			Code string `json:"errorcode"`
			Exc  *exc   `json:"exception"`
		}
		json.Unmarshal(b, &top)
		if top.Exc != nil {
			top.Code = top.Exc.Code
		}
		if loginCodes[top.Code] {
			return errExpired
		}
		return fmt.Errorf("%s: %s", fn, trunc(string(b), 300))
	}
	var rr []struct {
		Error bool            `json:"error"`
		Exc   *exc            `json:"exception"`
		Data  json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(b, &rr); err != nil || len(rr) == 0 {
		return fmt.Errorf("%s: неожиданный ответ: %s", fn, trunc(string(b), 300))
	}
	if r := rr[0]; r.Error {
		if r.Exc != nil {
			if loginCodes[r.Exc.Code] {
				return errExpired
			}
			return fmt.Errorf("%s: %s [%s]", fn, r.Exc.Msg, r.Exc.Code)
		}
		return fmt.Errorf("%s: ошибка", fn)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(rr[0].Data, out)
}

// newHTTP: timeouts per phase instead of http.Client.Timeout, which would also
// cap reading the body and kill large downloads/uploads on slow links.
func newHTTP(jar http.CookieJar) *http.Client {
	return &http.Client{Jar: jar, Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 90 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConnsPerHost:   8,
		ForceAttemptHTTP2:     true,
	}}
}

// idleBody cancels the request if the body stalls for longer than d.
type idleBody struct {
	io.ReadCloser
	t      *time.Timer
	d      time.Duration
	cancel context.CancelFunc
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.t.Reset(b.d)
	return n, err
}

func (b *idleBody) Close() error {
	b.t.Stop()
	b.cancel()
	return b.ReadCloser.Close()
}

// do sends rq; whole call bounded by total (if >0), body reads by idle timeout.
func (s *sess) do(rq *http.Request, total, idle time.Duration) (*http.Response, error) {
	var ctx context.Context
	var cancel context.CancelFunc
	if total > 0 {
		ctx, cancel = context.WithTimeout(rq.Context(), total)
	} else {
		ctx, cancel = context.WithCancel(rq.Context())
	}
	rs, err := s.hc.Do(rq.WithContext(ctx))
	if err != nil {
		cancel()
		return nil, err
	}
	rs.Body = &idleBody{ReadCloser: rs.Body, t: time.AfterFunc(idle, cancel), d: idle, cancel: cancel}
	return rs, nil
}

func (s *sess) postForm(u string, v url.Values) (*http.Response, error) {
	rq, _ := http.NewRequest("POST", u, strings.NewReader(v.Encode()))
	rq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return s.do(rq, 90*time.Second, 60*time.Second)
}

// refresh reloads session.json if `mirea-moodle-mcp cookie` changed it while
// this server process was running (so the agent needs no restart).
func (s *sess) refresh() {
	st, err := os.Stat(sessPath())
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.ModTime().After(s.mod) {
		s.load()
	}
}
