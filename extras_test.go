package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fake Moodle for the read-only extras: AJAX methods answer from the maps
// below, pages come from testdata/*.html.
func extrasFake(t *testing.T) (*sess, *[]string) {
	var posted []string
	var s *sess
	ajax := map[string]any{
		"core_session_time_remaining": map[string]any{"userid": 7, "timeremaining": 7200},
		"message_popup_get_popup_notifications": map[string]any{"unreadcount": 2, "notifications": []any{
			map[string]any{"id": 3, "subject": "Срок сдачи ПР5: вторник, 6 октября 2026, 00:00", "contexturl": "https://online-edu.mirea.ru/mod/assign/view.php?id=982205&action=view", "component": "mod_assign", "eventtype": "assign_due_soon", "timecreated": 1791063982, "read": false, "fullmessage": "long text"},
			map[string]any{"id": 2, "subject": "У вас есть задания, которые нужно сдать через 7 дней", "component": "mod_assign", "eventtype": "assign_due_digest", "timecreated": 1791000000, "read": false,
				"fullmessage": "Hi Иван,\n\nThe following assignments are due on WEDNESDAY, 14 OCTOBER 2026.\n\n\t* ПРАКТИКА 3 in course\nСистемы\nDUE: 12:00 AM\nGo to activity [1]\n\n\n\nLinks:\n------\n[1] https://online-edu.mirea.ru/mod/assign/view.php?id=1\n"},
			map[string]any{"id": 1, "subject": "Открывается тест", "component": "mod_quiz", "eventtype": "quiz_open_soon", "timecreated": 1790000000, "read": true},
		}},
		"core_calendar_get_calendar_day_view": nil, // built per request
		"core_courseformat_get_state": `{"section":[{"id":"1","title":"Общее","cmlist":["5"]},{"id":"2","title":"Ликвидация академической задолженности","cmlist":["6","7"]}],` +
			`"cm":[{"id":"5","module":"forum","name":"Новости","url":"x"},{"id":"6","module":"label","name":"инфо"},{"id":"7","module":"page","name":"Ликвидация осенью","url":"{{BASE}}/mod/page/view.php?id=7"}]}`,
	}
	s = stubSess(t, func(w http.ResponseWriter, r *http.Request) {
		page := func(name string, extra ...string) {
			b, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			rep := append([]string{"{{BASE}}", s.base}, extra...)
			io.WriteString(w, strings.NewReplacer(rep...).Replace(string(b)))
		}
		switch r.URL.Path {
		case "/lib/ajax/service.php":
			m, args := ajaxReq(r)
			var data any = ajax[m]
			switch m {
			case "core_calendar_get_calendar_day_view":
				d := int(args["day"].(float64))
				ts := time.Date(2026, 10, d, 16, 20, 0, 0, msk).Unix()
				data = map[string]any{"events": []any{
					map[string]any{"id": 2, "name": "Практическая работа №2 - срок сдачи", "timestart": ts + 3600*7, "timeduration": 0, "modulename": "assign", "eventtype": "due", "url": "https://online-edu.mirea.ru/mod/assign/view.php?id=511511", "course": map[string]any{"id": 9, "fullname": "Анализ данных (часть 1/1) [I.26-27]"}},
					map[string]any{"id": 1, "name": fmt.Sprintf("%02d.10.2026 16:20 ЛК. Анализ данных", d), "timestart": ts, "timeduration": 5400, "modulename": "webinars", "eventtype": "group", "groupname": "ИКБО-50-23", "url": "https://online-edu.mirea.ru/mod/webinars/view.php?id=965921", "course": map[string]any{"id": 9, "fullname": "Анализ данных (часть 1/1) [I.26-27]"}},
				}}
			case "core_courseformat_get_state":
				data = strings.ReplaceAll(data.(string), "{{BASE}}", s.base)
			}
			if data == nil {
				t.Errorf("unexpected ajax %s", m)
			}
			json.NewEncoder(w).Encode([]any{map[string]any{"error": false, "data": data}})
		case "/grade/report/overview/index.php":
			page("grades_overview.html")
		case "/grade/report/user/index.php":
			page("grades_user.html")
		case "/mod/page/view.php":
			page("retakes.html")
		case "/calendar/export.php":
			cal := ""
			if r.Method == "POST" {
				r.ParseForm()
				posted = append(posted, r.PostForm.Get("sesskey"), r.PostForm.Get("events[exportevents]"), r.PostForm.Get("period[timeperiod]"), r.PostForm.Get("generateurl"))
				cal = `<input type="text" id="calendarexporturl" class="form-control" value="` + s.base + `/calendar/export_execute.php?userid=7&amp;authtoken=abc&amp;preset_what=all&amp;preset_time=recentupcoming" readonly />`
			}
			page("calexport.html", "{{CALURL}}", cal)
		default:
			http.NotFound(w, r)
		}
	})
	s.uid = 7
	dropCache("")
	return s, &posted
}

func TestSessionLeft(t *testing.T) {
	s, _ := extrasFake(t)
	if n, err := s.sessionLeft(); err != nil || n != 7200 {
		t.Fatalf("%d %v", n, err)
	}
}

func TestWhatsNew(t *testing.T) {
	s, _ := extrasFake(t)
	ns, unread, err := s.notifications(20)
	if err != nil || unread != 2 || len(ns) != 3 {
		t.Fatalf("%v %d %d", err, unread, len(ns))
	}
	o := notifObj(ns[1])
	txt, _ := o["text"].(string)
	if o["kind"] != "задания на неделе" || o["unread"] != true || !strings.Contains(txt, "ПРАКТИКА 3") || strings.Contains(txt, "Links") || strings.Contains(txt, "[1]") || strings.Contains(txt, "Иван") {
		t.Fatalf("digest: %v", o)
	}
	if o := notifObj(ns[0]); o["text"] != nil || o["url"] == nil || o["kind"] != "скоро срок сдачи" {
		t.Fatalf("due soon: %v", o)
	}
	if o := notifObj(ns[2]); o["unread"] != nil {
		t.Fatalf("read flag: %v", o)
	}
}

func TestCleanMail(t *testing.T) {
	got := cleanMail("Здравствуйте, Ксения Александровна,\n\nСледующие задания:\n* ПР 6\nПерейти к элементу [1]\n\nLinks:\n------\n[1] https://x\n")
	if got != "Следующие задания: * ПР 6 Перейти к элементу" {
		t.Fatalf("%q", got)
	}
}

func TestAgenda(t *testing.T) {
	s, _ := extrasFake(t)
	days, err := s.agenda(time.Date(2026, 10, 5, 12, 0, 0, 0, msk), 2)
	if err != nil || len(days) != 2 {
		t.Fatalf("%v %v", err, days)
	}
	if days[0]["date"] != "2026-10-05 пн" {
		t.Fatalf("date: %v", days[0]["date"])
	}
	it := days[0]["items"].([]obj)
	// sorted by time; title prefix and " - срок сдачи" stripped
	if len(it) != 2 || it[0]["kind"] != "вебинар" || it[0]["time"] != "16:20–17:50" || it[0]["name"] != "ЛК. Анализ данных" || it[0]["group"] != "ИКБО-50-23" {
		t.Fatalf("lecture: %v", it)
	}
	if it[1]["kind"] != "срок сдачи" || it[1]["name"] != "Практическая работа №2" || it[1]["course"] == nil {
		t.Fatalf("due: %v", it[1])
	}
}

func TestGrades(t *testing.T) {
	s, _ := extrasFake(t)
	v, err := s.grades(0, false)
	if err != nil {
		t.Fatal(err)
	}
	cs := v.(obj)["courses"].([]obj)
	if len(cs) != 2 || cs[0]["course_id"] != 16391 || cs[0]["grade"] != "-" || cs[1]["grade"] != "78,00" || cs[1]["course"] != "Методы кодирования & сжатия" {
		t.Fatalf("overview: %v", cs)
	}
	v, err = s.grades(16391, false)
	if err != nil {
		t.Fatal(err)
	}
	o := v.(obj)
	items := o["items"].([]gradeItem)
	if o["course"] != "Информационные системы (часть 1/1) [I.26-27]" || len(items) != 6 {
		t.Fatalf("user report: %v %+v", o["course"], items)
	}
	q := items[0]
	if q.Name != "Текущий контроль 10" || q.Grade != "7,50" || q.Range != "0–10" || q.Category != "Текущая успеваемость" || !strings.Contains(q.URL, "quiz/view.php?id=765553") || q.Feedback != "" {
		t.Fatalf("quiz row: %+v", q)
	}
	if a := items[1]; a.Grade != "5,0" || a.Feedback != "Хорошо, но добавьте выводы." {
		t.Fatalf("assign row: %+v", a)
	}
	if e := items[2]; e.Grade != "-" || e.Range != "" || e.URL != "" {
		t.Fatalf("empty row: %+v", e)
	}
	if !items[3].Total || items[4].Total || !items[5].Total || items[4].Category != "" {
		t.Fatalf("totals/categories: %+v", items[3:])
	}
	v, _ = s.grades(16391, true)
	if n := len(v.(obj)["items"].([]gradeItem)); n != 3 {
		t.Fatalf("only_graded: %d", n)
	}
}

func TestCalendarURL(t *testing.T) {
	s, posted := extrasFake(t)
	u, err := s.calendarURL()
	if err != nil {
		t.Fatal(err)
	}
	if u != s.base+"/calendar/export_execute.php?userid=7&authtoken=abc&preset_what=all&preset_time=recentupcoming" {
		t.Fatalf("url: %s", u)
	}
	if strings.Join(*posted, "|") != "SK9|all|recentupcoming|Получить адрес календаря" {
		t.Fatalf("form: %v", *posted)
	}
	h := calendarHowTo("https://h/x?a=1")
	if h["webcal"] != "webcal://h/x?a=1" {
		t.Fatal(h["webcal"])
	}
	remoteMode = true
	defer func() { remoteMode = false }()
	for _, tl := range extraTools(s) {
		if tl.Name == "calendar_feed" {
			if _, err := tl.fn(nil); err == nil {
				t.Fatal("calendar_feed must refuse in HTTP mode")
			}
		}
	}
}

func TestCoursework(t *testing.T) {
	b, _ := os.ReadFile("testdata/coursework.html")
	o := parseCoursework(string(b))
	if o["title"] != "Тематика курсовой работы (2026-2027)" || o["state"] != "тема скорректирована руководителем" ||
		!strings.HasPrefix(o["my_topic"].(string), "Разработка проектных решений") || o["note"] != "Скорректирована руководителем" {
		t.Fatalf("%v", o)
	}
	ts := o["available_topics"].([]obj)
	if len(ts) != 2 || ts[0]["id"] != 152 || ts[0]["slots"] != "без ограничений ∞" || ts[1]["name"] != "АСУ ТП энергоблока" || ts[1]["slots"] != "занята 0 / 3" {
		t.Fatalf("topics: %v", ts)
	}
	if o := parseCoursework(`<div role="main"><h2>Темы</h2><div class="cw-wrap"></div></div>`); o["state"] != "тема не выбрана" {
		t.Fatalf("no status: %v", o)
	}
}

func TestRetakes(t *testing.T) {
	s, _ := extrasFake(t)
	v, err := s.retakes("иит")
	if err != nil {
		t.Fatal(err)
	}
	o := v.(obj)
	ds := o["departments"].([]obj)
	if !strings.HasSuffix(o["page"].(string), "/mod/page/view.php?id=7") || len(ds) != 2 || ds[0]["department"] != "Кафедра корпоративных информационных систем" || ds[0]["schedule"] != "https://cloud.mirea.ru/index.php/s/AAA" {
		t.Fatalf("%v", o)
	}
	v, _ = s.retakes("")
	if n := len(v.(obj)["departments"].([]obj)); n != 3 {
		t.Fatalf("all: %d", n)
	}
}

func TestToolNamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, tl := range allTools(newSess("https://example.invalid")) {
		if seen[tl.Name] {
			t.Fatalf("duplicate tool %s", tl.Name)
		}
		seen[tl.Name] = true
	}
	if len(seen) != 17 {
		t.Fatalf("tools: %d", len(seen))
	}
}
