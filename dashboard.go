package main

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type dItem struct {
	Name, Course, Type, Action, URL, Due, Rel string
	CID                                       int64
	TS                                        int64
	Urg                                       string // over | soon | week | later
}

type dCourse struct {
	ID         int64
	Name, URL  string
	Open, Over int
}

type dData struct {
	Now                       string
	Groups                    []dGroup
	Courses                   []dCourse
	NOver, NSoon, NWeek, NAll int
}

type dGroup struct {
	Title, Key string
	Items      []dItem
}

var reCourseTail = regexp.MustCompile(`\s*(\(часть[^)]*\))?\s*(\[[^\]]*\])?\s*$`)

func shortName(n string) string {
	n = reCourseTail.ReplaceAllString(n, "")
	n = strings.TrimSuffix(n, "_Бак")
	return strings.TrimSpace(n)
}

func rel(d time.Duration) string {
	neg := d < 0
	if neg {
		d = -d
	}
	days := int(d.Hours()) / 24
	hrs := int(d.Hours()) % 24
	var s string
	switch {
	case days > 0:
		s = fmt.Sprintf("%d дн %d ч", days, hrs)
	case hrs > 0:
		s = fmt.Sprintf("%d ч %d мин", hrs, int(d.Minutes())%60)
	default:
		s = fmt.Sprintf("%d мин", int(d.Minutes()))
	}
	if neg {
		return "просрочено на " + s
	}
	return "через " + s
}

var ruDays = []string{"вс", "пн", "вт", "ср", "чт", "пт", "сб"}

func dashData(s *sess) (*dData, error) {
	var cr struct {
		Courses []struct {
			ID   int64  `json:"id"`
			Full string `json:"fullname"`
			URL  string `json:"viewurl"`
		} `json:"courses"`
	}
	if err := s.call("core_course_get_enrolled_courses_by_timeline_classification", obj{"classification": "inprogress", "limit": 0, "offset": 0, "sort": "fullname"}, &cr); err != nil {
		return nil, err
	}
	now := time.Now().In(msk)
	var ev struct {
		Events []struct {
			Name   string `json:"name"`
			Mod    string `json:"modulename"`
			TS     int64  `json:"timesort"`
			URL    string `json:"url"`
			Course struct {
				ID   int64  `json:"id"`
				Name string `json:"fullname"`
			} `json:"course"`
			Act *struct {
				Name string `json:"name"`
			} `json:"action"`
		} `json:"events"`
	}
	if err := s.call("core_calendar_get_action_events_by_timesort", obj{"timesortfrom": now.Add(-30 * 24 * time.Hour).Unix(), "timesortto": now.Add(60 * 24 * time.Hour).Unix(), "limitnum": 50}, &ev); err != nil {
		return nil, err
	}
	d := &dData{Now: now.Format("02.01.2006 15:04")}
	cs := map[int64]*dCourse{}
	var order []int64
	for _, c := range cr.Courses {
		cs[c.ID] = &dCourse{ID: c.ID, Name: shortName(c.Full), URL: c.URL}
		order = append(order, c.ID)
	}
	g := map[string][]dItem{}
	for _, e := range ev.Events {
		t := time.Unix(e.TS, 0).In(msk)
		dl := t.Sub(now)
		u := "later"
		switch {
		case dl < 0:
			u = "over"
			d.NOver++
		case dl <= 48*time.Hour:
			u = "soon"
			d.NSoon++
		case dl <= 7*24*time.Hour:
			u = "week"
			d.NWeek++
		}
		d.NAll++
		name := strings.TrimSuffix(strings.TrimSuffix(e.Name, " - срок сдачи"), " закрывается")
		it := dItem{Name: name, Course: shortName(e.Course.Name), CID: e.Course.ID, Type: e.Mod, URL: e.URL, TS: e.TS,
			Due: fmt.Sprintf("%s, %s", ruDays[t.Weekday()], t.Format("02.01 15:04")), Rel: rel(dl), Urg: u}
		if e.Act != nil {
			it.Action = e.Act.Name
		}
		g[u] = append(g[u], it)
		c := cs[e.Course.ID]
		if c == nil {
			c = &dCourse{ID: e.Course.ID, Name: shortName(e.Course.Name), URL: s.base + fmt.Sprintf("/course/view.php?id=%d", e.Course.ID)}
			cs[e.Course.ID] = c
			order = append(order, e.Course.ID)
		}
		if u == "over" {
			c.Over++
		} else {
			c.Open++
		}
	}
	for _, k := range []struct{ k, t string }{{"over", "Просрочено"}, {"soon", "Ближайшие 48 часов"}, {"week", "На этой неделе"}, {"later", "Позже"}} {
		it := g[k.k]
		sort.Slice(it, func(i, j int) bool { return it[i].TS < it[j].TS })
		if len(it) > 0 {
			d.Groups = append(d.Groups, dGroup{Title: k.t, Key: k.k, Items: it})
		}
	}
	for _, id := range order {
		d.Courses = append(d.Courses, *cs[id])
	}
	return d, nil
}

func buildDashboard(s *sess) (string, error) {
	d, err := dashData(s)
	if err != nil {
		return "", err
	}
	p := filepath.Join(cfgDir(), "dashboard.html")
	os.MkdirAll(cfgDir(), 0o700)
	f, err := os.Create(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return p, dashTpl.Execute(f, d)
}

var typeRu = map[string]string{"assign": "задание", "quiz": "тест", "forum": "форум", "workshop": "семинар", "lesson": "лекция", "feedback": "опрос", "choice": "опрос", "scorm": "курс", "h5pactivity": "интерактив"}

var dashTpl = template.Must(template.New("d").Funcs(template.FuncMap{
	"tr": func(s string) string {
		if v, ok := typeRu[s]; ok {
			return v
		}
		return s
	},
}).Parse(`<!doctype html>
<html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Дедлайны МИРЭА</title>
<style>
:root{--bg:#f6f7f9;--card:#fff;--fg:#1b1d21;--mut:#6b7280;--line:#e5e7eb;--over:#c62828;--soon:#d97706;--week:#2563eb;--later:#6b7280;--chip:#eef1f5}
@media (prefers-color-scheme:dark){:root{--bg:#111316;--card:#1b1e23;--fg:#e8eaed;--mut:#9aa0a6;--line:#2c3036;--over:#ef5350;--soon:#f59e0b;--week:#60a5fa;--later:#9aa0a6;--chip:#262a30}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--fg);font:15px/1.45 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif}
.w{max-width:980px;margin:0 auto;padding:24px 16px 48px}
h1{font-size:22px;margin:0}.sub{color:var(--mut);font-size:13px;margin-top:4px}
.stats{display:grid;grid-template-columns:repeat(4,1fr);gap:10px;margin:20px 0}
.st{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:12px 14px}
.st b{display:block;font-size:26px;line-height:1.1}.st span{color:var(--mut);font-size:12px}
.st.over b{color:var(--over)}.st.soon b{color:var(--soon)}.st.week b{color:var(--week)}
.chips{display:flex;flex-wrap:wrap;gap:6px;margin:8px 0 18px}
.chip{border:1px solid var(--line);background:var(--chip);color:var(--fg);border-radius:999px;padding:4px 10px;font-size:12px;cursor:pointer}
.chip.on{background:var(--fg);color:var(--bg)}
h2{font-size:14px;text-transform:uppercase;letter-spacing:.04em;color:var(--mut);margin:22px 0 8px}
.it{display:grid;grid-template-columns:4px 1fr auto;gap:12px;align-items:center;background:var(--card);border:1px solid var(--line);border-radius:10px;padding:10px 12px 10px 0;margin-bottom:6px;text-decoration:none;color:inherit}
.it:hover{border-color:var(--mut)}
.bar{align-self:stretch;border-radius:0 3px 3px 0}.over .bar{background:var(--over)}.soon .bar{background:var(--soon)}.week .bar{background:var(--week)}.later .bar{background:var(--later)}
.nm{font-weight:600}.meta{color:var(--mut);font-size:12.5px;margin-top:2px}
.due{text-align:right;white-space:nowrap;font-size:13px}.due small{display:block;color:var(--mut)}
.over .due small{color:var(--over)}.soon .due small{color:var(--soon)}
.cs{display:grid;grid-template-columns:repeat(auto-fill,minmax(260px,1fr));gap:8px}
.c{background:var(--card);border:1px solid var(--line);border-radius:10px;padding:10px 12px;text-decoration:none;color:inherit;font-size:14px}
.c .meta b{color:var(--fg)}.c .meta .o{color:var(--over)}
.empty{color:var(--mut);padding:24px;text-align:center;background:var(--card);border:1px dashed var(--line);border-radius:10px}
@media (max-width:600px){.stats{grid-template-columns:repeat(2,1fr)}.it{grid-template-columns:4px 1fr}.due{text-align:left;grid-column:2}}
</style></head><body><div class="w">
<h1>Дедлайны МИРЭА</h1>
<div class="sub">Обновлено {{.Now}} МСК · обновить: <code>mirea-moodle-mcp dashboard</code></div>
<div class="stats">
<div class="st over"><b>{{.NOver}}</b><span>просрочено</span></div>
<div class="st soon"><b>{{.NSoon}}</b><span>за 48 часов</span></div>
<div class="st week"><b>{{.NWeek}}</b><span>на неделе</span></div>
<div class="st"><b>{{.NAll}}</b><span>всего открыто</span></div>
</div>
<div class="chips" id="chips"><button class="chip on" data-c="">Все курсы</button>{{range .Courses}}{{if or .Open .Over}}<button class="chip" data-c="{{.ID}}">{{.Name}}</button>{{end}}{{end}}</div>
{{if .Groups}}{{range .Groups}}<section class="g"><h2>{{.Title}}</h2>
{{range .Items}}<a class="it {{.Urg}}" data-c="{{.CID}}" href="{{.URL}}" target="_blank" rel="noopener"><div class="bar"></div>
<div><div class="nm">{{.Name}}</div><div class="meta">{{.Course}} · {{tr .Type}}{{if .Action}} · {{.Action}}{{end}}</div></div>
<div class="due">{{.Due}}<small>{{.Rel}}</small></div></a>
{{end}}</section>{{end}}{{else}}<div class="empty">Открытых дедлайнов нет 🎉</div>{{end}}
<h2>Курсы</h2><div class="cs">{{range .Courses}}<a class="c" href="{{.URL}}" target="_blank" rel="noopener"><div class="nm">{{.Name}}</div>
<div class="meta">{{if .Over}}<span class="o">{{.Over}} просрочено</span> · {{end}}<b>{{.Open}}</b> открыто</div></a>{{end}}</div>
</div>
<script>
document.getElementById('chips').addEventListener('click',function(e){var b=e.target.closest('.chip');if(!b)return;
document.querySelectorAll('.chip').forEach(function(x){x.classList.toggle('on',x===b)});var c=b.dataset.c;
document.querySelectorAll('.it').forEach(function(x){x.style.display=!c||x.dataset.c===c?'':'none'});
document.querySelectorAll('section.g').forEach(function(s){var v=[].some.call(s.querySelectorAll('.it'),function(x){return x.style.display!=='none'});s.style.display=v?'':'none'})});
</script></body></html>`))
