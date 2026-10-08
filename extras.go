package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Quick read-only features: notifications feed, agenda from the Moodle
// calendar, grades, calendar subscription URL, coursework topic, retakes,
// session time remaining.

// ---- session time remaining ----

// sessionLeft: seconds until Moodle drops an idle session (2 h on online-edu).
// Any request resets the timer, so this mainly tells whether keepalive works.
func (s *sess) sessionLeft() (int64, error) {
	var r struct {
		Left int64 `json:"timeremaining"`
	}
	err := s.call("core_session_time_remaining", obj{}, &r)
	return r.Left, err
}

// ---- notifications ("что нового") ----

type notif struct {
	ID      int64  `json:"id"`
	Subject string `json:"subject"`
	Small   string `json:"smallmessage"`
	Full    string `json:"fullmessage"`
	URL     string `json:"contexturl"`
	URLName string `json:"contexturlname"`
	Comp    string `json:"component"`
	Type    string `json:"eventtype"`
	Time    int64  `json:"timecreated"`
	Read    bool   `json:"read"`
}

var notifKind = map[string]string{
	"assign_due_digest":        "задания на неделе",
	"assign_due_soon":          "скоро срок сдачи",
	"assign_overdue":           "срок сдачи прошёл",
	"assign_notification":      "оценка / отзыв по заданию",
	"assign_gradernotify":      "оценка / отзыв по заданию",
	"feedbackavailable":        "отзыв преподавателя",
	"quiz_open_soon":           "скоро откроется тест",
	"attempt_grading_complete": "тест проверен",
	"submission":               "работа отправлена",
	"confirmsubmission":        "работа отправлена",
	"posts":                    "новое сообщение на форуме",
	"digests":                  "дайджест форума",
	"topicreminder":            "выбор темы курсовой",
	"gradenotifications":       "новая оценка",
}

func (s *sess) notifications(limit int) ([]notif, int, error) {
	var r struct {
		N      []notif `json:"notifications"`
		Unread int     `json:"unreadcount"`
	}
	err := s.call("message_popup_get_popup_notifications", obj{"useridto": s.uid, "limit": limit, "offset": 0, "newestfirst": 1}, &r)
	return r.N, r.Unread, err
}

func notifObj(n notif) obj {
	k := notifKind[n.Type]
	if k == "" {
		k = n.Comp + "/" + n.Type
	}
	o := obj{"when": ft(n.Time), "kind": k, "subject": n.Subject}
	if !n.Read {
		o["unread"] = true
	}
	if n.URL != "" {
		o["url"] = n.URL
	}
	// digests list several assignments: their body is the useful part
	if n.Type == "assign_due_digest" || n.Type == "topicreminder" || n.Type == "assign_notification" || n.Type == "posts" {
		body := n.Full
		if body == "" {
			body = n.Small
		}
		o["text"] = trunc(cleanMail(body), 800)
	}
	return o
}

var (
	reMailLinks = regexp.MustCompile(`(?s)\n\s*Links:\s*\n-{3,}.*$`)
	reMailRef   = regexp.MustCompile(`\s*\[\d+\]`)
	reGreeting  = regexp.MustCompile(`^(Hi|Hello|Здравствуйте|Добрый день)(\s|,|!|$)[^.!]*[,.!]?$`)
)

// cleanMail: Moodle's plain-text e-mail body → compact text.
func cleanMail(s string) string {
	s = reMailLinks.ReplaceAllString(s, "")
	s = reMailRef.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	var out []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "Links:") || strings.Trim(l, "-") == "" || (len(out) == 0 && reGreeting.MatchString(l)) {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, " ")
}

// ---- agenda (calendar day view) ----

type calEvent struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Start  int64  `json:"timestart"`
	Dur    int64  `json:"timeduration"`
	Mod    string `json:"modulename"`
	Type   string `json:"eventtype"`
	Group  string `json:"groupname"`
	URL    string `json:"url"`
	Course *struct {
		ID   int64  `json:"id"`
		Name string `json:"fullname"`
	} `json:"course"`
}

func (s *sess) dayEvents(d time.Time) ([]calEvent, error) {
	key := "day:" + d.Format("2006-01-02")
	return cached(key, 5*time.Minute, func() ([]calEvent, error) {
		var r struct {
			Events []calEvent `json:"events"`
		}
		err := s.call("core_calendar_get_calendar_day_view", obj{"year": d.Year(), "month": int(d.Month()), "day": d.Day(), "courseid": 1, "categoryid": 0}, &r)
		return r.Events, err
	})
}

func eventKind(e calEvent) string {
	switch {
	case e.Mod == "webinars":
		return "вебинар"
	case e.Mod == "assign" && e.Type == "due":
		return "срок сдачи"
	case e.Mod == "assign" && e.Type == "gradingdue":
		return "срок проверки"
	case e.Mod == "quiz" && e.Type == "open":
		return "тест открывается"
	case e.Mod == "quiz" && e.Type == "close":
		return "тест закрывается"
	case e.Mod != "" && strings.HasSuffix(e.Type, "close"):
		return "закрывается"
	case e.Mod != "" && strings.HasSuffix(e.Type, "open"):
		return "открывается"
	case e.Mod != "":
		return e.Mod
	}
	return "событие"
}

var reEvtSuffix = regexp.MustCompile(`\s*(-\s*срок сдачи|открывается|закрывается)\s*$`)

func agendaItem(e calEvent) obj {
	t := time.Unix(e.Start, 0).In(msk)
	when := t.Format("15:04")
	if e.Dur > 0 {
		when += "–" + t.Add(time.Duration(e.Dur)*time.Second).Format("15:04")
	}
	name := reTitleHead.ReplaceAllString(e.Name, "")
	name = reEvtSuffix.ReplaceAllString(name, "")
	o := obj{"time": when, "kind": eventKind(e), "name": name}
	if e.Course != nil && e.Course.Name != "" {
		o["course"] = shortName(e.Course.Name)
	}
	if e.Group != "" {
		o["group"] = e.Group
	}
	if e.URL != "" {
		o["url"] = e.URL
	}
	return o
}

func (s *sess) agenda(start time.Time, days int) ([]obj, error) {
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, msk)
	res := make([][]calEvent, days)
	errs := make([]error, days)
	par(days, 4, func(i int) {
		res[i], errs[i] = s.dayEvents(start.AddDate(0, 0, i))
	})
	out := []obj{}
	for i := 0; i < days; i++ {
		if errs[i] != nil {
			return nil, errs[i]
		}
		evs := res[i]
		sort.SliceStable(evs, func(a, b int) bool { return evs[a].Start < evs[b].Start })
		items := []obj{}
		for _, e := range evs {
			items = append(items, agendaItem(e))
		}
		d := start.AddDate(0, 0, i)
		out = append(out, obj{"date": d.Format("2006-01-02") + " " + ruWeekday[d.Weekday()], "items": items})
	}
	return out, nil
}

var ruWeekday = [...]string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}

// ---- grades ----

var (
	reGrRow   = regexp.MustCompile(`(?s)<tr[^>]*>(.*?)</tr>`)
	reGrLevel = regexp.MustCompile(`class="level(\d+)`)
	reGrCat   = regexp.MustCompile(`(?s)category-content.*?<span>([^<]*)</span>`)
	reGrItem  = regexp.MustCompile(`(?s)<(a|span)([^>]*class="gradeitemheader[^"]*"[^>]*)>(.*?)</(?:a|span)>`)
	reGrCell  = func(col string) *regexp.Regexp {
		return regexp.MustCompile(`(?s)<td[^>]*column-` + col + `[^>]*>(.*?)</td>`)
	}
	reGrGrade   = reGrCell("grade")
	reGrRange   = reGrCell("range")
	reGrPct     = reGrCell("percentage")
	reGrFeed    = reGrCell("feedback")
	reGrFirstDv = regexp.MustCompile(`^\s*<div[^>]*>\s*<div>([^<]*)</div>`)
	reGrOvRow   = regexp.MustCompile(`(?s)<a href="[^"]*course/user\.php\?mode=grade&amp;id=(\d+)[^"]*">(.*?)</a></td>\s*<td[^>]*>(.*?)</td>`)
)

type gradeItem struct {
	Name     string `json:"name"`
	Grade    string `json:"grade"`
	Range    string `json:"range,omitempty"`
	Percent  string `json:"percent,omitempty"`
	Feedback string `json:"feedback,omitempty"`
	Category string `json:"category,omitempty"`
	URL      string `json:"url,omitempty"`
	Total    bool   `json:"total,omitempty"`
}

func gradeCell(c string) string {
	if m := reGrFirstDv.FindStringSubmatch(c); m != nil {
		c = m[1]
	}
	v := strip(c)
	if v == "" || v == "–" {
		return "-"
	}
	return v
}

func parseUserGrades(h string) (course string, items []gradeItem) {
	if i := strings.Index(h, "user-grade"); i >= 0 {
		h = h[i:]
	}
	if j := strings.Index(h, "</table>"); j >= 0 {
		h = h[:j]
	}
	cats := map[int]string{}
	for _, m := range reGrRow.FindAllStringSubmatch(h, -1) {
		r := m[1]
		lvl := 0
		if l := reGrLevel.FindStringSubmatch(r); l != nil {
			lvl = atoi(l[1])
		}
		if c := reGrCat.FindStringSubmatch(r); c != nil && !strings.Contains(r, "gradeitemheader") {
			cats[lvl] = strip(c[1])
			if lvl == 1 {
				course = cats[1]
			}
			continue
		}
		it := reGrItem.FindStringSubmatch(r)
		if it == nil {
			continue
		}
		g := gradeItem{Name: strip(it[3])}
		if it[1] == "a" {
			g.URL = attr(it[2], "href")
		}
		if v := reGrGrade.FindStringSubmatch(r); v != nil {
			g.Grade = gradeCell(v[1])
		}
		if v := reGrRange.FindStringSubmatch(r); v != nil {
			if rg := strip(v[1]); rg != "–" {
				g.Range = rg
			}
		}
		if v := reGrPct.FindStringSubmatch(r); v != nil {
			if p := gradeCell(v[1]); p != "-" {
				g.Percent = p
			}
		}
		if v := reGrFeed.FindStringSubmatch(r); v != nil {
			g.Feedback = trunc(strip(v[1]), 600)
		}
		if lvl > 2 { // level 1 is the course itself
			g.Category = cats[lvl-1]
		}
		g.Total = strings.HasPrefix(g.Name, "Итого") || strings.HasPrefix(g.Name, "Итоговая оценка")
		items = append(items, g)
	}
	return
}

func parseOverview(h string) []obj {
	out := []obj{}
	for _, m := range reGrOvRow.FindAllStringSubmatch(h, -1) {
		out = append(out, obj{"course_id": atoi(m[1]), "course": strip(m[2]), "grade": gradeCell(m[3])})
	}
	return out
}

func (s *sess) grades(cid int64, onlyGraded bool) (any, error) {
	if cid <= 0 {
		h, _, err := s.getHTML("/grade/report/overview/index.php")
		if err != nil {
			return nil, err
		}
		return obj{"courses": parseOverview(h), "hint": "итог по курсу часто «-» до сессии; оценки за отдельные работы — grades(course_id)"}, nil
	}
	h, _, err := s.getHTML(fmt.Sprintf("/grade/report/user/index.php?id=%d", cid))
	if err != nil {
		return nil, err
	}
	course, items := parseUserGrades(h)
	if items == nil {
		return nil, errors.New("таблица оценок не найдена (нет доступа к журналу курса?)")
	}
	if onlyGraded {
		f := items[:0]
		for _, g := range items {
			if g.Grade != "-" {
				f = append(f, g)
			}
		}
		items = f
	}
	return obj{"course": course, "items": items}, nil
}

// ---- calendar subscription URL ----

var reCalURL = regexp.MustCompile(`<input[^>]*id="calendarexporturl"[^>]*>`)

func (s *sess) calendarURL() (string, error) {
	if _, _, err := s.getHTML("/calendar/export.php"); err != nil {
		return "", err
	}
	s.mu.Lock()
	key := s.key
	s.mu.Unlock()
	v := url.Values{
		"sesskey":                        {key},
		"_qf__core_calendar_export_form": {"1"},
		"events[exportevents]":           {"all"},
		"period[timeperiod]":             {"recentupcoming"},
		"generateurl":                    {"Получить адрес календаря"},
	}
	h, err := s.postHTML("/calendar/export.php", v)
	if err != nil {
		return "", err
	}
	tag := reCalURL.FindString(h)
	if tag == "" {
		return "", errors.New("Moodle не выдал адрес календаря (форма экспорта изменилась?)")
	}
	return attr(tag, "value"), nil
}

// ---- coursework topic (mod_choicecoursework) ----

var (
	reCWStatus = regexp.MustCompile(`(?s)<div class="cw-mystatus ?([a-z]*)">(.*?)</div>`)
	reCWLbl    = regexp.MustCompile(`(?s)<span class="(lbl|val|aside)[^"]*">(.*?)</span>`)
	reCWItem   = regexp.MustCompile(`(?s)<div class="cw-item( [a-z]+)?"[^>]*data-topicid="(\d+)"[^>]*>(.*?)</div></div>`)
	reCWName   = regexp.MustCompile(`(?s)class="cw-item-name">(.*?)</div>`)
	reCWBadge  = regexp.MustCompile(`(?s)class="cw-badge ([a-z]+)">(.*?)</span>`)
)

var cwState = map[string]string{"ok": "тема утверждена", "corr": "тема скорректирована руководителем", "wait": "ждёт подтверждения", "locked": "выбор закрыт", "": "тема не выбрана"}

func parseCoursework(h string) obj {
	o := obj{}
	if m := reH2.FindStringSubmatch(h); m != nil {
		o["title"] = strip(m[1])
	}
	if m := reCWStatus.FindStringSubmatch(h); m != nil {
		o["state"] = cwState[m[1]]
		for _, p := range reCWLbl.FindAllStringSubmatch(m[2], -1) {
			switch p[1] {
			case "val":
				o["my_topic"] = strip(p[2])
			case "aside":
				o["note"] = strip(p[2])
			}
		}
	} else {
		o["state"] = cwState[""]
	}
	topics := []obj{}
	for _, m := range reCWItem.FindAllStringSubmatch(h, -1) {
		t := obj{"id": atoi(m[2])}
		if n := reCWName.FindStringSubmatch(m[3]); n != nil {
			t["name"] = strip(n[1])
		}
		if b := reCWBadge.FindStringSubmatch(m[3]); b != nil {
			free := map[string]string{"free": "свободна", "taken": "частично занята", "full": "занята", "inf": "без ограничений"}[b[1]]
			t["slots"] = strings.TrimSpace(free + " " + strip(b[2]))
		}
		topics = append(topics, t)
	}
	o["available_topics"] = topics
	if d := reDates.FindStringSubmatch(h); d != nil {
		o["dates"] = strip(d[1])
	}
	return o
}

var reH2 = regexp.MustCompile(`(?s)<div role="main">\s*<h2[^>]*>(.*?)</h2>`)

func (s *sess) coursework(cid int64) ([]obj, error) {
	cms, err := s.modules(cid, "choicecoursework")
	if err != nil {
		return nil, err
	}
	out := []obj{}
	for _, c := range cms {
		h, _, err := s.getHTML(c.URL)
		if err != nil {
			out = append(out, obj{"course": c.Course, "name": c.Name, "url": c.URL, "error": err.Error()})
			continue
		}
		o := parseCoursework(h)
		o["course"], o["url"] = c.Course, c.URL
		out = append(out, o)
	}
	return out, nil
}

// ---- retakes (академические задолженности) ----

const retakeCourse = 122 // «Как учиться в электронной среде»

var reCloud = regexp.MustCompile(`href="(https://cloud\.mirea\.ru/[^"]+)"`)

func parseRetakes(h string) []obj {
	out := []obj{}
	for _, m := range reTR.FindAllStringSubmatch(h, -1) {
		l := reCloud.FindStringSubmatch(m[1])
		if l == nil {
			continue
		}
		cells := reTD.FindAllStringSubmatch(m[1], -1)
		if len(cells) < 3 {
			continue
		}
		out = append(out, obj{"institute": strip(cells[0][1]), "department": strip(cells[1][1]), "schedule": l[1]})
	}
	return out
}

// retakePage finds the page with per-department retake schedules in the
// "Ликвидация академической задолженности" section.
func (s *sess) retakePage() (string, error) {
	raw, err := s.courseState(retakeCourse)
	if err != nil {
		return "", err
	}
	var st struct {
		Section []struct {
			Title string   `json:"title"`
			CMs   []string `json:"cmlist"`
		} `json:"section"`
		CM []struct {
			ID   string `json:"id"`
			Mod  string `json:"module"`
			URL  string `json:"url"`
			Name string `json:"name"`
		} `json:"cm"`
	}
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return "", err
	}
	cms := map[string]int{}
	for i, c := range st.CM {
		cms[c.ID] = i
	}
	for _, sc := range st.Section {
		if !strings.Contains(strings.ToLower(sc.Title), "задолженност") {
			continue
		}
		for _, id := range sc.CMs {
			if i, ok := cms[id]; ok && st.CM[i].Mod == "page" && st.CM[i].URL != "" {
				return st.CM[i].URL, nil
			}
		}
	}
	return "", errors.New("раздел «Ликвидация академической задолженности» не найден")
}

func (s *sess) retakes(q string) (any, error) {
	u, err := s.retakePage()
	if err != nil {
		return nil, err
	}
	h, _, err := s.getHTML(u)
	if err != nil {
		return nil, err
	}
	all := parseRetakes(h)
	q = strings.ToLower(strings.TrimSpace(q))
	out := []obj{}
	for _, r := range all {
		if q == "" || strings.Contains(strings.ToLower(r["institute"].(string)+" "+r["department"].(string)), q) {
			out = append(out, r)
		}
	}
	return obj{"page": u, "departments": out, "hint": "график консультаций и пересдач кафедры — по ссылке schedule (облако МИРЭА); кафедру курса видно в list_courses (поле dept)"}, nil
}

// ---- tools ----

func extraTools(s *sess) []*tool {
	return []*tool{
		{
			Name:   "whats_new",
			Desc:   "Уведомления Moodle (колокольчик): скоро срок сдачи, дайджест заданий на неделю, открытие тестов, оценки и отзывы, форумы, выбор темы курсовой. Только чтение, отметки «прочитано» не ставит.",
			Schema: schema(obj{"limit": num("сколько последних, по умолчанию 20 (до 50)"), "only_unread": boolean("только непрочитанные (по умолчанию false)")}), Ann: ro,
			fn: func(a json.RawMessage) (any, error) {
				var p struct {
					Limit  int  `json:"limit"`
					Unread bool `json:"only_unread"`
				}
				json.Unmarshal(a, &p)
				if p.Limit <= 0 || p.Limit > 50 {
					p.Limit = 20
				}
				ns, unread, err := s.notifications(p.Limit)
				if err != nil {
					return nil, err
				}
				items := []obj{}
				for _, n := range ns {
					if p.Unread && n.Read {
						continue
					}
					items = append(items, notifObj(n))
				}
				return obj{"unread_total": unread, "items": items}, nil
			},
		},
		{
			Name:   "agenda",
			Desc:   "План на день/неделю из календаря Moodle одним запросом на день: вебинары (лекции) своей группы, сроки сдачи заданий, открытие/закрытие тестов. Вебинары попадают в календарь незадолго до начала (прогноз следующих — lectures); ссылку «Подключиться» даёт lectures, текст задания — get_activity(url).",
			Schema: schema(obj{"days": num("сколько дней, по умолчанию 1 (сегодня), до 14"), "start": str("дата начала YYYY-MM-DD, по умолчанию сегодня (МСК)")}), Ann: ro,
			fn: func(a json.RawMessage) (any, error) {
				var p struct {
					Days  int    `json:"days"`
					Start string `json:"start"`
				}
				json.Unmarshal(a, &p)
				if p.Days <= 0 {
					p.Days = 1
				}
				if p.Days > 14 {
					p.Days = 14
				}
				st := time.Now().In(msk)
				if p.Start != "" {
					t, err := time.ParseInLocation("2006-01-02", p.Start, msk)
					if err != nil {
						return nil, fmt.Errorf("start: нужен формат YYYY-MM-DD")
					}
					st = t
				}
				return s.agenda(st, p.Days)
			},
		},
		{
			Name:   "grades",
			Desc:   "Оценки. Без course_id — сводка по всем курсам (итог часто «-» до сессии). С course_id — журнал курса: каждая работа/тест, оценка, диапазон, категория, отзыв.",
			Schema: schema(obj{"course_id": num("id курса из list_courses"), "only_graded": boolean("только элементы с выставленной оценкой")}), Ann: ro,
			fn: func(a json.RawMessage) (any, error) {
				var p struct {
					CID  int64 `json:"course_id"`
					Only bool  `json:"only_graded"`
				}
				json.Unmarshal(a, &p)
				return s.grades(p.CID, p.Only)
			},
		},
		{
			Name:   "calendar_feed",
			Desc:   "Адрес подписки на календарь Moodle (iCal): дедлайны, тесты и лекции своей группы появятся в Google/Apple/Яндекс Календаре и будут обновляться сами. Адрес — личный секрет (даёт чтение календаря).",
			Schema: schema(obj{}), Ann: ro,
			fn: func(json.RawMessage) (any, error) {
				if remoteMode {
					return nil, errors.New("в HTTP-режиме адрес календаря не выдаётся (это секрет). Выполни на своём компьютере: mirea-moodle-mcp calendar")
				}
				u, err := s.calendarURL()
				if err != nil {
					return nil, err
				}
				return calendarHowTo(u), nil
			},
		},
		{
			Name:   "coursework",
			Desc:   "Статус выбора темы курсовой (mod_choicecoursework): моя тема, утверждена ли руководителем, доступные темы и свободные места. Только чтение — тему не выбирает.",
			Schema: schema(obj{"course_id": num("необязательно: только этот курс")}), Ann: ro,
			fn: func(a json.RawMessage) (any, error) {
				var p struct {
					CID int64 `json:"course_id"`
				}
				json.Unmarshal(a, &p)
				r, err := s.coursework(p.CID)
				if err == nil && len(r) == 0 {
					return "Элементов выбора темы курсовой в текущих курсах нет.", nil
				}
				return r, err
			},
		},
		{
			Name:   "retakes",
			Desc:   "Пересдачи (ликвидация академических задолженностей): ссылки на графики консультаций и пересдач по кафедрам. query — фильтр по институту или кафедре (например «ИИТ» или «корпоративных»).",
			Schema: schema(obj{"query": str("подстрока института/кафедры")}), Ann: ro,
			fn: func(a json.RawMessage) (any, error) {
				var p struct {
					Q string `json:"query"`
				}
				json.Unmarshal(a, &p)
				return s.retakes(p.Q)
			},
		},
	}
}

func calendarHowTo(u string) obj {
	return obj{
		"url":    u,
		"webcal": "webcal://" + strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://"),
		"how": []string{
			"Google Календарь: «Другие календари» → «+» → «Добавить по URL» → вставить url",
			"Apple Календарь (Mac/iPhone): открыть webcal-ссылку или Файл → Новая подписка на календарь",
			"Яндекс Календарь: «Новый календарь» → «Подписка на календарь» → url",
		},
		"note": "Календарь отдаёт события за последние и ближайшие 60 дней и обновляется сам (Google — раз в несколько часов). Не публикуй адрес: по нему видно твоё расписание.",
	}
}
