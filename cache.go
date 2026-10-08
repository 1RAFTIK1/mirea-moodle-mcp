package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// ---- TTL cache with in-flight de-duplication ----
//
// lectures/quizzes/dashboard fetch one page per course element (~60 requests).
// Caching for a few minutes makes repeat calls instant and is gentler on
// online-edu. Errors are never cached.

type memoEntry struct {
	v    any
	err  error
	at   time.Time
	done chan struct{}
}

var memo = struct {
	sync.Mutex
	m map[string]*memoEntry
}{m: map[string]*memoEntry{}}

func cached[T any](key string, ttl time.Duration, f func() (T, error)) (T, error) {
	memo.Lock()
	if e, ok := memo.m[key]; ok {
		memo.Unlock()
		<-e.done
		if e.err == nil && time.Since(e.at) < ttl {
			return e.v.(T), nil
		}
		memo.Lock()
		if memo.m[key] == e {
			delete(memo.m, key)
		}
	}
	if e, ok := memo.m[key]; ok { // someone else started a fresh fetch meanwhile
		memo.Unlock()
		<-e.done
		if e.err != nil {
			var z T
			return z, e.err
		}
		return e.v.(T), nil
	}
	e := &memoEntry{done: make(chan struct{})}
	memo.m[key] = e
	memo.Unlock()

	v, err := f()
	e.v, e.err, e.at = v, err, time.Now()
	close(e.done)
	if err != nil {
		memo.Lock()
		if memo.m[key] == e {
			delete(memo.m, key)
		}
		memo.Unlock()
	}
	return v, err
}

func dropCache(prefix string) {
	memo.Lock()
	defer memo.Unlock()
	for k := range memo.m {
		if strings.HasPrefix(k, prefix) {
			delete(memo.m, k)
		}
	}
}

// ---- keepalive ----

// keepalive pings Moodle so an idle session does not expire while the server
// runs. core_session_touch is the AJAX call Moodle's own pages use for this;
// if it is not exposed, any cheap authenticated call refreshes the session too.
func (s *sess) keepalive(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for range t.C {
		s.touch()
	}
}

func (s *sess) touch() error {
	err := s.call("core_session_touch", obj{}, nil)
	if err == nil || errors.Is(err, errExpired) {
		return err
	}
	return s.call("core_course_get_enrolled_courses_by_timeline_classification", obj{"classification": "inprogress", "limit": 1, "offset": 0, "sort": "fullname"}, nil)
}

// ---- doctor ----

func doctor(s *sess) int {
	bad := 0
	ok := func(c bool, okMsg, badMsg string) {
		if c {
			fmt.Println("✓", okMsg)
		} else {
			bad++
			fmt.Println("✗", badMsg)
		}
	}
	fmt.Println("mirea-moodle-mcp", version)
	host := strings.TrimPrefix(strings.TrimPrefix(s.base, "https://"), "http://")
	ips, err := net.LookupHost(host)
	ok(err == nil, "DNS: "+host+" → "+strings.Join(ips, ", "), fmt.Sprintf("DNS %s: %v", host, err))
	if err == nil {
		start := time.Now()
		rq, _ := http.NewRequest("GET", s.base+"/login/index.php", nil)
		rs, err := s.do(rq, 15*time.Second, 15*time.Second)
		if err == nil {
			rs.Body.Close()
		}
		msg := fmt.Sprintf("HTTPS до %s: %v", host, err)
		if err != nil && (strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline")) {
			msg += "\n    похоже, соединение режется: МИРЭА не пускает часть зарубежных IP. Если включён VPN — добавь *.mirea.ru в исключения"
		}
		ok(err == nil, fmt.Sprintf("HTTPS до %s за %s", host, time.Since(start).Round(time.Millisecond)), msg)
	}
	err = ping(s)
	ok(err == nil, fmt.Sprintf("сессия Moodle жива (userid %d)", s.uid), "сессия: "+fmt.Sprint(err))
	if err == nil {
		if left, e := s.sessionLeft(); e == nil && left > 0 {
			fmt.Printf("• без активности сессия проживёт %d мин; запущенный MCP-сервер продлевает её сам\n", left/60)
		}
	}
	g := loadCfg().Group
	ok(g != "", "группа: "+g, "группа не задана — лекции не фильтруются: mirea-moodle-mcp group ИКБО-XX-XX")
	fmt.Println("• разрешённые папки:", strings.Join(roots(), ", "))
	n := 0
	for _, c := range clients {
		if c.path == nil {
			continue
		}
		if b, err := os.ReadFile(c.path()); err == nil && strings.Contains(string(b), srvName) {
			fmt.Println("• подключён к", c.Name)
			n++
		}
	}
	ok(n > 0 || inPath("claude"), "клиенты настроены", "ни в одном конфиге клиента нет mirea-moodle: mirea-moodle-mcp install detected")
	if bad > 0 {
		return 1
	}
	return 0
}
