package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Quizzes tracker and lecture (MTS Link webinars) schedule.

// ---- user config (group) ----

type config struct {
	Group string `json:"group"`
}

func cfgPath() string { return filepath.Join(cfgDir(), "config.json") }

func loadCfg() config {
	var c config
	if b, err := os.ReadFile(cfgPath()); err == nil {
		json.Unmarshal(b, &c)
	}
	if g := os.Getenv("MIREA_GROUP"); g != "" {
		c.Group = g
	}
	return c
}

func saveCfg(c config) error {
	os.MkdirAll(cfgDir(), 0o700)
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(cfgPath(), b, 0o600)
}

var reGroup = regexp.MustCompile(`(?i)[А-ЯЁA-Z]{4}-\d{2}-\d{2}`)

func normGroup(g string) string { return strings.ToUpper(strings.TrimSpace(g)) }

// forGroup: keep if text mentions my group, or mentions no group at all.
func forGroup(g string, texts ...string) bool {
	if g == "" {
		return true
	}
	any := false
	for _, t := range texts {
		u := strings.ToUpper(t)
		if strings.Contains(u, g) {
			return true
		}
		if reGroup.MatchString(u) {
			any = true
		}
	}
	return !any
}

// ---- course modules across courses ----

type cmRef struct {
	CID    int64
	Course string
	ID     string
	Name   string
	Mod    string
	URL    string
}

func (s *sess) modules(cid int64, mods ...string) ([]cmRef, error) {
	type course struct {
		ID   int64
		Name string
	}
	var cs []course
	if cid > 0 {
		cs = []course{{ID: cid}}
	} else {
		var r struct {
			Courses []struct {
				ID   int64  `json:"id"`
				Full string `json:"fullname"`
			} `json:"courses"`
		}
		if err := s.call("core_course_get_enrolled_courses_by_timeline_classification", obj{"classification": "inprogress", "limit": 0, "offset": 0, "sort": "fullname"}, &r); err != nil {
			return nil, err
		}
		for _, c := range r.Courses {
			cs = append(cs, course{c.ID, shortName(c.Full)})
		}
	}
	want := map[string]bool{}
	for _, m := range mods {
		want[m] = true
	}
	var mu sync.Mutex
	var out []cmRef
	var firstErr error
	par(len(cs), 4, func(i int) {
		var raw string
		if err := s.call("core_courseformat_get_state", obj{"courseid": cs[i].ID}, &raw); err != nil {
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
			return
		}
		var st struct {
			Course struct {
				Name string `json:"fullname"`
			} `json:"course"`
			CM []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Mod  string `json:"module"`
				URL  string `json:"url"`
				UV   bool   `json:"uservisible"`
			} `json:"cm"`
		}
		json.Unmarshal([]byte(raw), &st)
		name := cs[i].Name
		if name == "" {
			name = shortName(st.Course.Name)
		}
		mu.Lock()
		defer mu.Unlock()
		for _, c := range st.CM {
			if want[c.Mod] && c.UV && c.URL != "" {
				out = append(out, cmRef{cs[i].ID, name, c.ID, c.Name, c.Mod, c.URL})
			}
		}
	})
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// par runs f(0..n-1) with at most k in parallel.
func par(n, k int, f func(int)) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, k)
	for i := 0; i < n; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; wg.Done() }()
			f(i)
		}(i)
	}
	wg.Wait()
}

func (s *sess) postHTML(path string, v url.Values) (string, error) {
	rs, err := s.hc.PostForm(s.abs(path), v)
	if err != nil {
		return "", err
	}
	defer rs.Body.Close()
	if strings.Contains(rs.Request.URL.String(), "/login/index.php") {
		return "", errExpired
	}
	b, err := io.ReadAll(rs.Body)
	return string(b), err
}

// ---- quizzes ----

type quiz struct {
	Course   string   `json:"course"`
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Dates    string   `json:"dates,omitempty"`
	Close    int64    `json:"-"`
	CloseStr string   `json:"closes,omitempty"`
	Info     []string `json:"info,omitempty"`
	Attempts []string `json:"attempts,omitempty"`
	Result   string   `json:"result,omitempty"`
	State    string   `json:"state"` // open | in_progress | done | closed | not_open
}

var (
	reQInfo   = regexp.MustCompile(`(?s)<div class="box py-3 quizinfo">(.*?)</div>`)
	reQTable  = regexp.MustCompile(`(?s)<table[^>]*quizattemptsummary[^>]*>(.*?)</table>`)
	reQFeed   = regexp.MustCompile(`(?s)<div id="feedback"[^>]*>(.*?)</div>`)
	reTR      = regexp.MustCompile(`(?s)<tr[^>]*>(.*?)</tr>`)
	reTD      = regexp.MustCompile(`(?s)<t[dh][^>]*>(.*?)</t[dh]>`)
	reRuDate  = regexp.MustCompile(`(\d{1,2}) (января|февраля|марта|апреля|мая|июня|июля|августа|сентября|октября|ноября|декабря) (\d{4}), (\d{1,2}):(\d{2})`)
	reCloseLn = regexp.MustCompile(`(?m)^\s*(Закрывается|Срок сдачи|Закрыто)[^:]*:\s*(.+)$`)
	reOpenLn  = regexp.MustCompile(`(?m)^\s*(Открывается|Открыто)[^:]*:\s*(.+)$`)
)

var ruMonths = map[string]time.Month{"января": 1, "февраля": 2, "марта": 3, "апреля": 4, "мая": 5, "июня": 6, "июля": 7, "августа": 8, "сентября": 9, "октября": 10, "ноября": 11, "декабря": 12}

func parseRuDate(s string) int64 {
	m := reRuDate.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	d, _ := strconv.Atoi(m[1])
	y, _ := strconv.Atoi(m[3])
	h, _ := strconv.Atoi(m[4])
	mi, _ := strconv.Atoi(m[5])
	return time.Date(y, ruMonths[m[2]], d, h, mi, 0, 0, msk).Unix()
}

func (s *sess) quizInfo(c cmRef) quiz {
	q := quiz{Course: c.Course, Name: c.Name, URL: c.URL, State: "open"}
	b, _, err := s.getHTML(c.URL)
	if err != nil {
		q.State = "error: " + err.Error()
		return q
	}
	now := time.Now().Unix()
	if m := reDates.FindStringSubmatch(b); m != nil {
		q.Dates = strip(m[1])
		if l := reCloseLn.FindStringSubmatch(q.Dates); l != nil {
			q.Close = parseRuDate(l[2])
		}
		if l := reOpenLn.FindStringSubmatch(q.Dates); l != nil {
			if t := parseRuDate(l[2]); t > now {
				q.State = "not_open"
			}
		}
	}
	if q.Close > 0 {
		q.CloseStr = ft(q.Close)
	}
	if m := reQInfo.FindStringSubmatch(b); m != nil {
		for _, l := range strings.Split(strip(m[1]), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				q.Info = append(q.Info, l)
			}
		}
	}
	if m := reQTable.FindStringSubmatch(b); m != nil {
		rows := reTR.FindAllStringSubmatch(m[1], -1)
		var head []string
		for i, r := range rows {
			var cells []string
			for _, c := range reTD.FindAllStringSubmatch(r[1], -1) {
				cells = append(cells, strip(c[1]))
			}
			if i == 0 {
				head = cells
				continue
			}
			var parts []string
			for j, c := range cells {
				if c == "" || strings.EqualFold(c, "Просмотр") || strings.EqualFold(c, "Отзыв") {
					continue
				}
				if j < len(head) && head[j] != "" {
					parts = append(parts, head[j]+": "+c)
				} else {
					parts = append(parts, c)
				}
			}
			q.Attempts = append(q.Attempts, strings.Join(parts, "; "))
		}
	}
	if m := reQFeed.FindStringSubmatch(b); m != nil {
		q.Result = strip(m[1])
	}
	switch {
	case q.State == "not_open":
	case strings.Contains(b, "Продолжить последнюю попытку") || strings.Contains(b, "Продолжить текущую попытку"):
		q.State = "in_progress"
	case strings.Contains(b, "Начать попытку") || strings.Contains(b, "Попытка теста") || strings.Contains(b, "Пройти тест заново") || strings.Contains(b, "Повторить попытку"):
		q.State = "open"
		if len(q.Attempts) > 0 {
			q.State = "open_retry"
		}
	case len(q.Attempts) > 0:
		q.State = "done"
	default:
		q.State = "closed"
	}
	if q.Close > 0 && q.Close < now && q.State != "in_progress" {
		if len(q.Attempts) > 0 {
			q.State = "done"
		} else {
			q.State = "closed"
		}
	}
	return q
}

func (s *sess) quizzes(cid int64) ([]quiz, error) {
	cms, err := s.modules(cid, "quiz")
	if err != nil {
		return nil, err
	}
	out := make([]quiz, len(cms))
	par(len(cms), 6, func(i int) { out[i] = s.quizInfo(cms[i]) })
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := quizRank(out[i]), quizRank(out[j])
		if ri != rj {
			return ri < rj
		}
		ci, cj := out[i].Close, out[j].Close
		if ci == 0 {
			ci = 1 << 62
		}
		if cj == 0 {
			cj = 1 << 62
		}
		return ci < cj
	})
	return out, nil
}

func quizRank(q quiz) int {
	switch q.State {
	case "in_progress":
		return 0
	case "open":
		return 1
	case "open_retry":
		return 2
	case "not_open":
		return 3
	case "done":
		return 4
	}
	return 5
}

var quizStateRu = map[string]string{"in_progress": "начат, не завершён", "open": "можно пройти", "open_retry": "есть попытка, можно ещё", "not_open": "ещё не открыт", "done": "пройден", "closed": "закрыт"}

// ---- webinars ----

type webinar struct {
	Course string `json:"course"`
	Title  string `json:"title"`
	Start  int64  `json:"-"`
	End    int64  `json:"-"`
	When   string `json:"when"`
	Status string `json:"status"` // live | upcoming | expected (predicted) | past
	Join   string `json:"join,omitempty"`
	Record string `json:"record,omitempty"`
	Page   string `json:"page"`
}

var (
	reLegacyID = regexp.MustCompile(`idelement:\s*'(\d+)'`)
	reNewID    = regexp.MustCompile(`ELEM_ID\s*=\s*(\d+)`)
	reDataVal  = regexp.MustCompile(`data-val="(\d{9,})"`)
	reHrefAny  = regexp.MustCompile(`(?:data-href|href)="(https?://[^"]+)"`)
	reTitleTS  = regexp.MustCompile(`(\d{2})\.(\d{2})\.(\d{4}) (\d{2}):(\d{2})-(\d{2}):(\d{2})`)
	reLegacyDT = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2}) (\d{2}):(\d{2})$`)
)

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func (s *sess) webinarsOf(c cmRef, g string) []webinar {
	b, _, err := s.getHTML(c.URL)
	if err != nil {
		return nil
	}
	var rows []webinar
	add := func(w webinar) {
		w.Course, w.Page = c.Course, c.URL
		if w.Start == 0 {
			if m := reTitleTS.FindStringSubmatch(w.Title); m != nil {
				day := time.Date(atoi(m[3]), time.Month(atoi(m[2])), atoi(m[1]), 0, 0, 0, 0, msk)
				w.Start = day.Add(time.Duration(atoi(m[4]))*time.Hour + time.Duration(atoi(m[5]))*time.Minute).Unix()
				w.End = day.Add(time.Duration(atoi(m[6]))*time.Hour + time.Duration(atoi(m[7]))*time.Minute).Unix()
			}
		}
		if !forGroup(g, w.Title, c.Name) {
			return
		}
		rows = append(rows, w)
	}
	if m := reNewID.FindStringSubmatch(b); m != nil {
		t, err := s.postHTML(dirOf(c.URL)+"pages/new/studentTable_new.php", url.Values{"idelement": {m[1]}})
		if err != nil {
			return nil
		}
		for i, r := range reTR.FindAllStringSubmatch(t, -1) {
			if i == 0 {
				continue
			}
			cells := reTD.FindAllStringSubmatch(r[1], -1)
			if len(cells) < 3 {
				continue
			}
			w := webinar{Title: strip(cells[0][1])}
			ts := reDataVal.FindAllStringSubmatch(r[1], -1)
			if len(ts) > 0 {
				w.Start, _ = strconv.ParseInt(ts[0][1], 10, 64)
			}
			if len(ts) > 1 {
				w.End, _ = strconv.ParseInt(ts[1][1], 10, 64)
			}
			linkKind(r[1], &w)
			add(w)
		}
	} else if m := reLegacyID.FindStringSubmatch(b); m != nil {
		t, err := s.postHTML(dirOf(c.URL)+"pages/legacy/studentTable.php", url.Values{"idelement": {m[1]}})
		if err != nil {
			return nil
		}
		for i, r := range reTR.FindAllStringSubmatch(t, -1) {
			if i == 0 {
				continue
			}
			cells := reTD.FindAllStringSubmatch(r[1], -1)
			if len(cells) < 5 {
				continue
			}
			w := webinar{Title: strip(cells[1][1])}
			for k, idx := range []int{3, 4} {
				if mm := reLegacyDT.FindStringSubmatch(strip(cells[idx][1])); mm != nil {
					t := time.Date(atoi(mm[1]), time.Month(atoi(mm[2])), atoi(mm[3]), atoi(mm[4]), atoi(mm[5]), 0, 0, msk).Unix()
					if k == 0 {
						w.Start = t
					} else {
						w.End = t
					}
				}
			}
			linkKind(r[1], &w)
			add(w)
		}
	}
	return rows
}

func dirOf(u string) string { return u[:strings.LastIndex(u, "/")+1] }

// linkKind decides whether a row's link is a recording or a join link.
func linkKind(row string, w *webinar) {
	for _, m := range reHrefAny.FindAllStringSubmatch(row, -1) {
		l := html.UnescapeString(m[1])
		if strings.Contains(l, "/record") || strings.Contains(strings.ToLower(row), "запис") && !strings.Contains(row, "Подключ") {
			w.Record = l
		} else {
			w.Join = l
		}
	}
}

func (s *sess) webinars(cid int64, g string, back, ahead time.Duration) ([]webinar, error) {
	cms, err := s.modules(cid, "webinars")
	if err != nil {
		return nil, err
	}
	var mu sync.Mutex
	var all []webinar
	par(len(cms), 6, func(i int) {
		ws := s.webinarsOf(cms[i], g)
		mu.Lock()
		all = append(all, ws...)
		mu.Unlock()
	})
	now := time.Now()
	all = append(all, predict(all, now)...)
	var out []webinar
	for _, w := range all {
		st, en := time.Unix(w.Start, 0), time.Unix(w.End, 0)
		if w.End == 0 {
			en = st.Add(2 * time.Hour)
		}
		switch {
		case w.Start == 0:
			continue
		case w.Status == "expected":
			if st.After(now.Add(ahead)) {
				continue
			}
		case !st.After(now.Add(15*time.Minute)) && en.After(now) || w.Join != "" && en.After(now):
			w.Status = "live"
		case st.After(now):
			if st.After(now.Add(ahead)) {
				continue
			}
			w.Status = "upcoming"
		default:
			if st.Before(now.Add(-back)) {
				continue
			}
			w.Status = "past"
		}
		w.When = fmt.Sprintf("%s, %s–%s", ruDays[st.In(msk).Weekday()], st.In(msk).Format("02.01 15:04"), en.In(msk).Format("15:04"))
		out = append(out, w)
	}
	rank := map[string]int{"live": 0, "upcoming": 1, "expected": 1, "past": 2}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Status] != rank[out[j].Status] {
			return rank[out[i].Status] < rank[out[j].Status]
		}
		if out[i].Status == "past" {
			return out[i].Start > out[j].Start
		}
		return out[i].Start < out[j].Start
	})
	return out, nil
}

// ---- MCP tools ----

func scheduleTools(s *sess) []*tool {
	return []*tool{
		{
			Name: "quizzes",
			Desc: "Трекер тестов (mod_quiz) по всем текущим курсам или одному: срок закрытия, лимит времени, число попыток, мои попытки и оценка, состояние (можно пройти / начат / пройден / закрыт). Только чтение — тесты не проходит.",
			Schema: schema(obj{
				"course_id": num("необязательно: только этот курс"),
				"only_open": boolean("показать только доступные для прохождения (по умолчанию false)"),
			}), Ann: ro,
			fn: func(a json.RawMessage) (any, error) {
				var p struct {
					CID  int64 `json:"course_id"`
					Open bool  `json:"only_open"`
				}
				json.Unmarshal(a, &p)
				qs, err := s.quizzes(p.CID)
				if err != nil {
					return nil, err
				}
				out := []obj{}
				for _, q := range qs {
					if p.Open && !strings.HasPrefix(q.State, "open") && q.State != "in_progress" {
						continue
					}
					o := obj{"course": q.Course, "name": q.Name, "url": q.URL, "state": quizStateRu[q.State]}
					if q.CloseStr != "" {
						o["closes"] = q.CloseStr
					}
					if len(q.Info) > 0 {
						o["info"] = q.Info
					}
					if len(q.Attempts) > 0 {
						o["attempts"] = q.Attempts
					}
					if q.Result != "" {
						o["result"] = q.Result
					}
					out = append(out, o)
				}
				if len(out) == 0 {
					return "Тестов не найдено.", nil
				}
				return out, nil
			},
		},
		{
			Name: "lectures",
			Desc: "Расписание дистанционных лекций/консультаций (вебинары MTS Link) из всех текущих курсов: идущие сейчас (со ссылкой «Подключиться», если Moodle её уже выдал), ближайшие и недавние с записями. Фильтр по группе — из настроек (mirea-moodle-mcp group …) или параметра group.",
			Schema: schema(obj{
				"course_id":  num("необязательно: только этот курс"),
				"group":      str("группа, например ИКБО-50-23 (по умолчанию из настроек; пусто — все)"),
				"days_back":  num("сколько дней назад показывать записи, по умолчанию 7"),
				"days_ahead": num("на сколько дней вперёд, по умолчанию 14"),
			}), Ann: ro,
			fn: func(a json.RawMessage) (any, error) {
				var p struct {
					CID   int64   `json:"course_id"`
					Group *string `json:"group"`
					Back  int     `json:"days_back"`
					Ahead int     `json:"days_ahead"`
				}
				json.Unmarshal(a, &p)
				g := loadCfg().Group
				if p.Group != nil {
					g = *p.Group
				}
				if p.Back <= 0 {
					p.Back = 7
				}
				if p.Ahead <= 0 {
					p.Ahead = 14
				}
				ws, err := s.webinars(p.CID, normGroup(g), time.Duration(p.Back)*24*time.Hour, time.Duration(p.Ahead)*24*time.Hour)
				if err != nil {
					return nil, err
				}
				if len(ws) == 0 {
					return fmt.Sprintf("Вебинаров в окне нет (группа: %q). Новые лекции обычно появляются в Moodle незадолго до начала.", g), nil
				}
				return obj{"group": g, "lectures": ws}, nil
			},
		},
	}
}

var reTitleHead = regexp.MustCompile(`^\d{2}\.\d{2}\.\d{4} \d{2}:\d{2}(-\d{2}:\d{2})?\s*`)

// predict: for each course, if the last two lectures are exactly 7 or 14 days
// apart, expect the next one at the same interval (unless already listed).
func predict(all []webinar, now time.Time) []webinar {
	by := map[string][]webinar{}
	for _, w := range all {
		if w.Start > 0 {
			by[w.Course] = append(by[w.Course], w)
		}
	}
	var out []webinar
	for _, ws := range by {
		sort.Slice(ws, func(i, j int) bool { return ws[i].Start < ws[j].Start })
		last := ws[len(ws)-1]
		if time.Unix(last.Start, 0).After(now) || len(ws) < 2 {
			continue // already have a future one, or not enough history
		}
		gap := last.Start - ws[len(ws)-2].Start
		day := int64(24 * 3600)
		if d := gap % (7 * day); gap < 6*day || gap > 15*day || (d > 3600 && d < 7*day-3600) {
			continue
		}
		dur := last.End - last.Start
		if dur <= 0 || dur > 4*3600 {
			dur = 90 * 60
		}
		next := last.Start + gap
		for time.Unix(next+dur, 0).Before(now) {
			next += gap
		}
		w := last
		w.Start, w.End = next, next+dur
		w.Status, w.Join, w.Record = "expected", "", ""
		w.Title = reTitleHead.ReplaceAllString(last.Title, "")
		out = append(out, w)
	}
	return out
}
