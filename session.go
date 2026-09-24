package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
}

type saved struct {
	Cookie string `json:"moodle_session"`
	Key    string `json:"sesskey"`
	UID    int64  `json:"uid"`
}

func sessPath() string { return filepath.Join(cfgDir(), "session.json") }

var errExpired = errors.New("сессия Moodle истекла или не задана: войди на online-edu.mirea.ru в браузере и выполни `mirea-moodle-mcp cookie <MoodleSession>` (см. README)")

var (
	reSesskey = regexp.MustCompile(`"sesskey":"([^"]+)"`)
	reUID     = regexp.MustCompile(`"userId":(\d+)`)
	reUID2    = regexp.MustCompile(`data-userid="(\d+)"`)
	reFull    = regexp.MustCompile(`(?s)class="[^"]*usertext[^"]*"[^>]*>(.*?)</span>`)
)

func newSess(base string) *sess {
	jar, _ := cookiejar.New(nil)
	s := &sess{base: base, jar: jar, hc: &http.Client{Jar: jar, Timeout: 120 * time.Second}}
	s.load()
	return s
}

func (s *sess) setJar(cookie string) {
	u, _ := url.Parse(s.base)
	s.jar, _ = cookiejar.New(nil)
	s.hc.Jar = s.jar
	if cookie != "" {
		s.jar.SetCookies(u, []*http.Cookie{{Name: "MoodleSession", Value: cookie, Path: "/"}})
	}
}

func (s *sess) load() {
	b, err := os.ReadFile(sessPath())
	if err != nil {
		return
	}
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
	rs, err := s.hc.Get(s.base + "/my/")
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
	return name, s.save()
}

var errRelogin = errors.New("relogin")

// call runs one function through lib/ajax/service.php.
func (s *sess) call(fn string, args any, out any) error {
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
	rs, err := s.hc.Post(u, "application/json", bytes.NewReader(body))
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
