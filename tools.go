package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Tools on top of a Moodle web session (MoodleSession cookie).

type obj = map[string]any

func schema(props obj, req ...string) obj {
	s := obj{"type": "object", "properties": props}
	if len(req) > 0 {
		s["required"] = req
	}
	return s
}

func num(d string) obj     { return obj{"type": "integer", "description": d} }
func str(d string) obj     { return obj{"type": "string", "description": d} }
func boolean(d string) obj { return obj{"type": "boolean", "description": d} }

var ro = obj{"readOnlyHint": true}

func dlDir() string {
	if d := os.Getenv("MOODLE_DOWNLOAD_DIR"); d != "" {
		return expand(d)
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Downloads", "mirea")
}

func hsize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// ---- session helpers (no silent SSO: session comes from the browser) ----

func (s *sess) abs(u string) string {
	if strings.HasPrefix(u, "http") {
		return u
	}
	return s.base + u
}

func (s *sess) sameHost(u string) error {
	pu, err := url.Parse(u)
	if err != nil {
		return err
	}
	bu, _ := url.Parse(s.base)
	if pu.Host != bu.Host {
		return fmt.Errorf("url не с %s: %s", bu.Host, u)
	}
	return nil
}

// fetch GETs a page/file with the session; follows redirects; detects logout.
func (s *sess) fetch(u string) (*http.Response, error) {
	u = s.abs(u)
	if err := s.sameHost(u); err != nil {
		return nil, err
	}
	rs, err := s.hc.Get(u)
	if err != nil {
		return nil, err
	}
	fu := rs.Request.URL.String()
	if strings.Contains(fu, "/login/index.php") || strings.Contains(fu, "sso.mirea.ru") {
		rs.Body.Close()
		return nil, errExpired
	}
	if rs.StatusCode != 200 {
		b, _ := io.ReadAll(rs.Body)
		rs.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", rs.StatusCode, trunc(strip(string(b)), 200))
	}
	return rs, nil
}

func (s *sess) getHTML(u string) (string, string, error) {
	rs, err := s.fetch(u)
	if err != nil {
		return "", "", err
	}
	defer rs.Body.Close()
	b, err := io.ReadAll(rs.Body)
	if m := reSesskey.FindSubmatch(b); m != nil {
		s.mu.Lock()
		s.key = string(m[1])
		s.mu.Unlock()
	}
	return string(b), rs.Request.URL.String(), err
}

// ---- html parsing ----

var (
	reMain    = regexp.MustCompile(`(?s)<div role="main">(.*?)(<footer|id="page-footer"|<div class="activity-navigation)`)
	reH1      = regexp.MustCompile(`(?s)<h1[^>]*>(.*?)</h1>`)
	reRow     = regexp.MustCompile(`(?s)<tr[^>]*>\s*<th[^>]*>(.*?)</th>\s*<td[^>]*>(.*?)</td>`)
	reHref    = regexp.MustCompile(`href="(https?://[^"]*pluginfile\.php/[^"]+)"`)
	reDesc    = regexp.MustCompile(`(?s)<div class="activity-description"[^>]*>(.*?)</div>\s*</div>\s*</div>`)
	reDates   = regexp.MustCompile(`(?s)<div[^>]*data-region="activity-dates"[^>]*>(.*?)</div>\s*</div>`)
	reMForm   = regexp.MustCompile(`(?s)<form[^>]*class="mform[^"]*"[^>]*>(.*?)</form>`)
	reFormAct = regexp.MustCompile(`<form[^>]*action="([^"]+)"[^>]*class="mform`)
	reHidden  = regexp.MustCompile(`<input[^>]*>`)
	reFM      = regexp.MustCompile(`M\.form_filemanager\.init\(Y, (\{.*?\})\);`)
)

func attr(tag, name string) string {
	m := regexp.MustCompile(`\b` + name + `="([^"]*)"`).FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1])
}

func mainText(b string) string {
	if m := reMain.FindStringSubmatch(b); m != nil {
		return strip(m[1])
	}
	return strip(b)
}

func fileLinks(b string) []obj {
	seen := map[string]bool{}
	out := []obj{}
	for _, m := range reHref.FindAllStringSubmatch(b, -1) {
		u := html.UnescapeString(m[1])
		if strings.Contains(u, "/theme_") || strings.Contains(u, "/user/icon") || strings.Contains(u, "/course/overviewfiles") || seen[u] {
			continue
		}
		seen[u] = true
		pu, _ := url.Parse(u)
		n, _ := url.PathUnescape(filepath.Base(pu.Path))
		out = append(out, obj{"file": n, "fileurl": u})
	}
	return out
}

// ---- tools ----

func tools(s *sess) []*tool {
	return []*tool{
		{
			Name: "list_courses", Desc: "Курсы студента. filter: inprogress (текущие, по умолчанию) | all | past | future.",
			Schema: schema(obj{"filter": obj{"type": "string", "enum": []string{"inprogress", "all", "past", "future"}}}), Ann: ro,
			fn: func(a json.RawMessage) (any, error) {
				var p struct{ Filter string }
				json.Unmarshal(a, &p)
				if p.Filter == "" {
					p.Filter = "inprogress"
				}
				var r struct {
					Courses []struct {
						ID    int64  `json:"id"`
						Full  string `json:"fullname"`
						End   int64  `json:"enddate"`
						URL   string `json:"viewurl"`
						Categ string `json:"coursecategory"`
					} `json:"courses"`
				}
				if err := s.call("core_course_get_enrolled_courses_by_timeline_classification", obj{"classification": p.Filter, "limit": 0, "offset": 0, "sort": "fullname"}, &r); err != nil {
					return nil, err
				}
				out := []obj{}
				for _, c := range r.Courses {
					o := obj{"id": c.ID, "name": c.Full, "url": c.URL}
					if c.Categ != "" {
						o["dept"] = c.Categ
					}
					out = append(out, o)
				}
				return out, nil
			},
		},
		{
			Name: "course_contents",
			Desc: "Разделы и элементы курса: cmid, тип (assign, resource, folder, page, url, quiz, forum…), название, url. Дальше — get_activity(url).",
			Schema: schema(obj{
				"course_id": num("id курса из list_courses"),
				"section":   str("необязательно: подстрока названия раздела"),
			}, "course_id"), Ann: ro,
			fn: func(a json.RawMessage) (any, error) {
				var p struct {
					CID int64  `json:"course_id"`
					Sec string `json:"section"`
				}
				json.Unmarshal(a, &p)
				var raw string
				if err := s.call("core_courseformat_get_state", obj{"courseid": p.CID}, &raw); err != nil {
					return nil, err
				}
				var st struct {
					Section []struct {
						ID    string   `json:"id"`
						Title string   `json:"title"`
						CMs   []string `json:"cmlist"`
						Visib bool     `json:"visible"`
					} `json:"section"`
					CM []struct {
						ID    string `json:"id"`
						Name  string `json:"name"`
						Mod   string `json:"module"`
						URL   string `json:"url"`
						UV    bool   `json:"uservisible"`
						Restr bool   `json:"hascmrestrictions"`
					} `json:"cm"`
				}
				if err := json.Unmarshal([]byte(raw), &st); err != nil {
					return nil, err
				}
				cms := map[string]obj{}
				for _, c := range st.CM {
					o := obj{"cmid": c.ID, "type": c.Mod, "name": c.Name}
					if c.URL != "" {
						o["url"] = c.URL
					}
					if !c.UV {
						o["locked"] = true
					}
					cms[c.ID] = o
				}
				out := []obj{}
				for _, sc := range st.Section {
					if p.Sec != "" && !strings.Contains(strings.ToLower(sc.Title), strings.ToLower(p.Sec)) {
						continue
					}
					items := []obj{}
					for _, id := range sc.CMs {
						if o, ok := cms[id]; ok {
							items = append(items, o)
						}
					}
					out = append(out, obj{"section": sc.Title, "items": items})
				}
				return out, nil
			},
		},
		{
			Name: "deadlines",
			Desc: "Дедлайны из таймлайна Moodle (невыполненные задания, тесты). url ведёт на элемент — дальше get_activity.",
			Schema: schema(obj{
				"days":         num("горизонт вперёд в днях, по умолчанию 30"),
				"overdue_days": num("сколько дней назад захватить просроченное, по умолчанию 14"),
				"course_id":    num("необязательно: только этот курс"),
			}), Ann: ro,
			fn: func(a json.RawMessage) (any, error) {
				var p struct {
					Days int   `json:"days"`
					Over *int  `json:"overdue_days"`
					CID  int64 `json:"course_id"`
				}
				json.Unmarshal(a, &p)
				if p.Days <= 0 {
					p.Days = 30
				}
				ov := 14
				if p.Over != nil {
					ov = *p.Over
				}
				now := time.Now()
				var r struct {
					Events []struct {
						Name   string `json:"name"`
						Mod    string `json:"modulename"`
						TS     int64  `json:"timesort"`
						URL    string `json:"url"`
						Over   bool   `json:"overdue"`
						Course struct {
							ID   int64  `json:"id"`
							Name string `json:"fullname"`
						} `json:"course"`
						Act *struct {
							Name string `json:"name"`
						} `json:"action"`
					} `json:"events"`
				}
				args := obj{"timesortfrom": now.Add(-time.Duration(ov) * 24 * time.Hour).Unix(), "timesortto": now.Add(time.Duration(p.Days) * 24 * time.Hour).Unix(), "limitnum": 50}
				if err := s.call("core_calendar_get_action_events_by_timesort", args, &r); err != nil {
					return nil, err
				}
				out := []obj{}
				for _, e := range r.Events {
					if p.CID > 0 && e.Course.ID != p.CID {
						continue
					}
					o := obj{"due": ft(e.TS), "name": e.Name, "type": e.Mod, "course": e.Course.Name, "course_id": e.Course.ID, "url": e.URL}
					if e.Over || e.TS < now.Unix() {
						o["overdue"] = true
					}
					if e.Act != nil {
						o["action"] = e.Act.Name
					}
					out = append(out, o)
				}
				if len(out) == 0 {
					return "Дедлайнов в этом окне нет.", nil
				}
				return out, nil
			},
		},
		{
			Name: "get_activity",
			Desc: "Открыть элемент курса по url (из course_contents/deadlines). assign: текст задания, сроки, файлы задания, статус моей сдачи; resource: ссылка на файл; folder: список файлов; url: внешняя ссылка; page и прочее: текст страницы.",
			Schema: schema(obj{
				"url":       str("url элемента, например https://online-edu.mirea.ru/mod/assign/view.php?id=765552"),
				"max_chars": num("лимит текста, по умолчанию 20000"),
			}, "url"), Ann: ro,
			fn: func(a json.RawMessage) (any, error) {
				var p struct {
					URL string `json:"url"`
					Max int    `json:"max_chars"`
				}
				json.Unmarshal(a, &p)
				if p.Max <= 0 {
					p.Max = 20000
				}
				return s.activity(p.URL, p.Max)
			},
		},
		{
			Name: "download_file",
			Desc: "Скачать файл Moodle (fileurl из get_activity или url элемента resource) на диск. Возвращает локальный путь.",
			Schema: schema(obj{
				"fileurl": str("pluginfile-ссылка или url mod/resource"),
				"dir":     str("папка (по умолчанию ~/Downloads/mirea или MOODLE_DOWNLOAD_DIR)"),
				"name":    str("имя файла (по умолчанию из ссылки)"),
			}, "fileurl"),
			fn: func(a json.RawMessage) (any, error) {
				var p struct{ Fileurl, Dir, Name string }
				json.Unmarshal(a, &p)
				d := dlDir()
				if p.Dir != "" {
					d = expand(p.Dir)
				}
				u := p.Fileurl
				if strings.Contains(u, "/mod/resource/view.php") && !strings.Contains(u, "redirect=1") {
					u += "&redirect=1"
				}
				rs, err := s.fetch(u)
				if err != nil {
					return nil, err
				}
				defer rs.Body.Close()
				if strings.HasPrefix(rs.Header.Get("Content-Type"), "text/html") && !strings.Contains(rs.Request.URL.Path, "pluginfile.php") {
					return nil, errors.New("по ссылке страница, а не файл — открой её get_activity и возьми fileurl")
				}
				n := p.Name
				if n == "" {
					n, _ = url.PathUnescape(filepath.Base(rs.Request.URL.Path))
				}
				n = strings.Map(func(r rune) rune {
					if r == '/' || r == '\\' || r == 0 {
						return '_'
					}
					return r
				}, n)
				os.MkdirAll(d, 0o755)
				path := filepath.Join(d, n)
				f, err := os.Create(path)
				if err != nil {
					return nil, err
				}
				sz, err := io.Copy(f, rs.Body)
				f.Close()
				if err != nil {
					return nil, err
				}
				return obj{"path": path, "size": hsize(sz)}, nil
			},
		},
		{
			Name:   "read_text",
			Desc:   "Прочитать текстовый файл Moodle (html, txt, md, csv, json) прямо в ответ. Для pdf/docx — download_file.",
			Schema: schema(obj{"fileurl": str("pluginfile-ссылка"), "max_chars": num("лимит, по умолчанию 30000")}, "fileurl"), Ann: ro,
			fn: func(a json.RawMessage) (any, error) {
				var p struct {
					Fileurl string
					Max     int `json:"max_chars"`
				}
				json.Unmarshal(a, &p)
				if p.Max <= 0 {
					p.Max = 30000
				}
				rs, err := s.fetch(p.Fileurl)
				if err != nil {
					return nil, err
				}
				defer rs.Body.Close()
				ct := rs.Header.Get("Content-Type")
				if !(strings.HasPrefix(ct, "text/") || strings.Contains(ct, "json") || strings.Contains(ct, "xml")) {
					return nil, fmt.Errorf("это не текст (%s) — используй download_file", ct)
				}
				b, err := io.ReadAll(io.LimitReader(rs.Body, 8<<20))
				if err != nil {
					return nil, err
				}
				t := string(b)
				if strings.Contains(ct, "html") {
					t = strip(t)
				}
				return trunc(t, p.Max), nil
			},
		},
		{
			Name: "submit_assignment",
			Desc: "Загрузить ответ в задание (mod_assign) по cmid. files — локальные пути; они ЗАМЕНЯЮТ ранее загруженные файлы. Сохраняется как черновик. " +
				"finalize=true дополнительно жмёт «Отправить на проверку» (если в задании это включено) — после этого правка обычно невозможна; спрашивай пользователя перед finalize.",
			Schema: schema(obj{
				"cmid":     num("cmid задания (id из url mod/assign/view.php?id=…)"),
				"files":    obj{"type": "array", "items": obj{"type": "string"}, "description": "абсолютные пути к файлам на этом компьютере"},
				"text":     str("онлайн-ответ (если в задании есть текстовый ответ)"),
				"finalize": boolean("отправить на проверку, по умолчанию false"),
				"dry_run":  boolean("только проверить: загрузить файлы во временную область и НЕ сохранять ответ"),
			}, "cmid"),
			Ann: obj{"destructiveHint": true},
			fn: func(a json.RawMessage) (any, error) {
				var p struct {
					CMID  int64    `json:"cmid"`
					Files []string `json:"files"`
					Text  string   `json:"text"`
					Fin   bool     `json:"finalize"`
					Dry   bool     `json:"dry_run"`
				}
				json.Unmarshal(a, &p)
				return s.submit(p.CMID, p.Files, p.Text, p.Fin, p.Dry)
			},
		},
		{
			Name: "dashboard", Desc: "Собрать и открыть в браузере HTML-дашборд с дедлайнами (просрочено / 48 ч / неделя / позже) и курсами. Возвращает путь и сводку.",
			Schema: schema(obj{"open": boolean("открыть в браузере, по умолчанию true")}), Ann: ro,
			fn: func(a json.RawMessage) (any, error) {
				var p struct {
					Open *bool `json:"open"`
				}
				json.Unmarshal(a, &p)
				path, err := buildDashboard(s)
				if err != nil {
					return nil, err
				}
				if p.Open == nil || *p.Open {
					openURL(path)
				}
				d, _ := dashData(s)
				o := obj{"path": path}
				if d != nil {
					o["overdue"], o["next_48h"], o["this_week"], o["open_total"] = d.NOver, d.NSoon, d.NWeek, d.NAll
				}
				return o, nil
			},
		},
		{
			Name: "session_status", Desc: "Проверить, жива ли сессия Moodle.", Schema: schema(obj{}), Ann: ro,
			fn: func(json.RawMessage) (any, error) {
				var r json.RawMessage
				if err := s.call("core_course_get_enrolled_courses_by_timeline_classification", obj{"classification": "inprogress", "limit": 1, "offset": 0, "sort": "fullname"}, &r); err != nil {
					return nil, err
				}
				return obj{"ok": true, "userid": s.uid}, nil
			},
		},
	}
}

func (s *sess) activity(u string, max int) (any, error) {
	u = s.abs(u)
	pu, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	mod := ""
	if parts := strings.Split(pu.Path, "/"); len(parts) > 2 && parts[1] == "mod" {
		mod = parts[2]
	}
	switch mod {
	case "resource":
		q := pu.Query()
		q.Set("redirect", "1")
		pu.RawQuery = q.Encode()
		rs, err := s.fetch(pu.String())
		if err != nil {
			return nil, err
		}
		rs.Body.Close()
		fu := rs.Request.URL.String()
		if strings.Contains(fu, "pluginfile.php") {
			n, _ := url.PathUnescape(filepath.Base(rs.Request.URL.Path))
			return obj{"type": "resource", "file": n, "fileurl": fu, "content_type": rs.Header.Get("Content-Type")}, nil
		}
	case "url":
		q := pu.Query()
		q.Set("redirect", "1")
		pu.RawQuery = q.Encode()
		cl := *s.hc
		cl.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		if rs, err := cl.Get(pu.String()); err == nil {
			rs.Body.Close()
			if l := rs.Header.Get("Location"); l != "" && !strings.Contains(l, "/login/") {
				return obj{"type": "url", "link": l}, nil
			}
		}
	}
	b, _, err := s.getHTML(u)
	if err != nil {
		return nil, err
	}
	o := obj{"type": mod, "url": u}
	if m := reH1.FindStringSubmatch(b); m != nil {
		o["title"] = strip(m[1])
	}
	switch mod {
	case "assign":
		if m := reDates.FindStringSubmatch(b); m != nil {
			o["dates"] = strip(m[1])
		}
		if m := reDesc.FindStringSubmatch(b); m != nil {
			o["task"] = trunc(strip(m[1]), max)
			o["task_files"] = fileLinks(m[1])
		}
		st := obj{}
		for _, r := range reRow.FindAllStringSubmatch(b, -1) {
			k := strip(r[1])
			if strings.HasPrefix(k, "Комментари") {
				continue
			}
			if strings.Contains(r[2], "pluginfile.php") {
				st[k] = fileLinks(r[2])
			} else {
				st[k] = strip(r[2])
			}
		}
		o["my_submission"] = st
		if strings.Contains(b, "action=editsubmission") {
			o["can_edit"] = true
		}
		if strings.Contains(b, "action=submit&") || strings.Contains(b, `value="submit"`) {
			o["can_finalize"] = true
		}
	case "folder":
		o["files"] = fileLinks(b)
	default:
		o["text"] = trunc(mainText(b), max)
		if fl := fileLinks(b); len(fl) > 0 {
			o["files"] = fl
		}
	}
	return o, nil
}

// ---- submission ----

type fmOpts struct {
	ItemID   int64  `json:"itemid"`
	ClientID string `json:"client_id"`
	MaxBytes string `json:"maxbytes"`
	MaxFiles string `json:"maxfiles"`
	Author   string `json:"author"`
	License  string `json:"defaultlicense"`
	Ctx      struct {
		ID int64 `json:"id"`
	} `json:"context"`
	List []struct {
		Name string `json:"filename"`
		Path string `json:"filepath"`
	} `json:"list"`
	FP struct {
		Author  string `json:"author"`
		License string `json:"defaultlicense"`
		Repos   map[string]struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"repositories"`
	} `json:"filepicker"`
}

func formValues(body string) (string, url.Values, string) {
	act := ""
	if m := reFormAct.FindStringSubmatch(body); m != nil {
		act = html.UnescapeString(m[1])
	}
	m := reMForm.FindStringSubmatch(body)
	if m == nil {
		return act, nil, ""
	}
	v := url.Values{}
	for _, t := range reHidden.FindAllString(m[1], -1) {
		if attr(t, "type") == "hidden" && attr(t, "name") != "" {
			v.Set(attr(t, "name"), attr(t, "value"))
		}
	}
	return act, v, m[1]
}

func (s *sess) submit(cmid int64, files []string, text string, fin, dry bool) (any, error) {
	res := obj{}
	view := fmt.Sprintf("%s/mod/assign/view.php?id=%d", s.base, cmid)
	if len(files) > 0 || text != "" {
		b, _, err := s.getHTML(view + "&action=editsubmission")
		if err != nil {
			return nil, err
		}
		act, v, fb := formValues(b)
		if v == nil || v.Get("action") != "savesubmission" {
			return nil, errors.New("форма сдачи недоступна (срок вышел, работа уже отправлена на проверку или задание закрыто)")
		}
		if len(files) > 0 {
			if v.Get("files_filemanager") == "" {
				return nil, errors.New("в этом задании нет ответа файлом")
			}
			m := reFM.FindStringSubmatch(b)
			if m == nil {
				return nil, errors.New("не нашёл настройки загрузчика файлов на странице")
			}
			var fo fmOpts
			if err := json.Unmarshal([]byte(m[1]), &fo); err != nil {
				return nil, fmt.Errorf("filemanager: %v", err)
			}
			if mf := fo.MaxFiles; mf != "" && mf != "-1" {
				var n int
				fmt.Sscan(mf, &n)
				if n > 0 && len(files) > n {
					return nil, fmt.Errorf("в задании максимум %d файл(ов)", n)
				}
			}
			repo := ""
			for _, r := range fo.FP.Repos {
				if r.Type == "upload" {
					repo = r.ID
				}
			}
			if repo == "" {
				return nil, errors.New("на сайте нет репозитория загрузки файлов")
			}
			for _, f := range files {
				st, err := os.Stat(expand(f))
				if err != nil {
					return nil, err
				}
				var mb int64
				fmt.Sscan(fo.MaxBytes, &mb)
				if mb > 0 && st.Size() > mb {
					return nil, fmt.Errorf("%s: %s больше лимита %s", filepath.Base(f), hsize(st.Size()), hsize(mb))
				}
			}
			if dry {
				for _, f := range files {
					if err := s.upload(expand(f), repo, fo, v.Get("sesskey")); err != nil {
						return nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
					}
				}
				return obj{"dry_run": true, "uploaded_to_temp_draft": len(files), "saved": false,
					"note": "файлы приняты сервером во временную область; ответ НЕ сохранён, в задании ничего не изменилось"}, nil
			}
			// clear current draft files
			for _, f := range fo.List {
				s.hc.PostForm(s.base+"/repository/draftfiles_ajax.php?action=delete", url.Values{
					"sesskey": {v.Get("sesskey")}, "client_id": {fo.ClientID}, "itemid": {fmt.Sprint(fo.ItemID)},
					"filepath": {f.Path}, "filename": {f.Name},
				})
			}
			for _, f := range files {
				if err := s.upload(expand(f), repo, fo, v.Get("sesskey")); err != nil {
					return nil, fmt.Errorf("%s: %w", filepath.Base(f), err)
				}
			}
			res["uploaded"] = len(files)
		}
		if text != "" {
			if !strings.Contains(fb, "onlinetext_editor") {
				return nil, errors.New("в этом задании нет текстового ответа")
			}
			v.Set("onlinetext_editor[text]", text)
			v.Set("onlinetext_editor[format]", "1")
			if it := regexp.MustCompile(`name="onlinetext_editor\[itemid\]"[^>]*value="(\d+)"|value="(\d+)"[^>]*name="onlinetext_editor\[itemid\]"`).FindStringSubmatch(fb); it != nil {
				v.Set("onlinetext_editor[itemid]", it[1]+it[2])
			}
		}
		v.Set("submitbutton", "Сохранить")
		rb, _, err := s.postPage(act, v)
		if err != nil {
			return nil, err
		}
		if strings.Contains(rb, `class="alert alert-danger`) || strings.Contains(rb, `class="form-control-feedback invalid-feedback" id="id_error_`) {
			if m := regexp.MustCompile(`(?s)id="id_error_[^"]*"[^>]*>(.*?)</div>`).FindStringSubmatch(rb); m != nil && strip(m[1]) != "" {
				return nil, fmt.Errorf("Moodle отклонил ответ: %s", strip(m[1]))
			}
		}
		res["saved"] = true
	}
	if fin {
		b, _, err := s.getHTML(view + "&action=submit")
		if err != nil {
			return nil, err
		}
		act, v, fb := formValues(b)
		if v == nil || v.Get("action") != "confirmsubmit" {
			return nil, errors.New("отправка на проверку недоступна (уже отправлено или в задании нет такого шага)")
		}
		if strings.Contains(fb, `name="submissionstatement"`) {
			v.Set("submissionstatement", "1")
		}
		v.Set("submitbutton", "Продолжить")
		if _, _, err := s.postPage(act, v); err != nil {
			return nil, err
		}
		res["finalize_requested"] = true
	}
	st, err := s.activity(view, 4000)
	if err == nil {
		if o, ok := st.(obj); ok {
			res["now"] = o["my_submission"]
		}
	}
	return res, nil
}

func (s *sess) postPage(act string, v url.Values) (string, string, error) {
	if act == "" {
		return "", "", errors.New("нет action у формы")
	}
	rs, err := s.hc.PostForm(s.abs(act), v)
	if err != nil {
		return "", "", err
	}
	defer rs.Body.Close()
	b, _ := io.ReadAll(rs.Body)
	if strings.Contains(rs.Request.URL.String(), "/login/index.php") {
		return "", "", errExpired
	}
	return string(b), rs.Request.URL.String(), nil
}

func (s *sess) upload(path, repo string, fo fmOpts, key string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("repo_upload_file", filepath.Base(path))
	if _, err := io.Copy(fw, f); err != nil {
		return err
	}
	for k, val := range map[string]string{
		"title": filepath.Base(path), "author": first(fo.Author, fo.FP.Author), "license": first(fo.License, fo.FP.License, "allrightsreserved"),
		"itemid": fmt.Sprint(fo.ItemID), "repo_id": repo, "p": "", "page": "",
		"env": "filemanager", "sesskey": key, "client_id": fo.ClientID,
		"maxbytes": fo.MaxBytes, "areamaxbytes": "-1", "ctx_id": fmt.Sprint(fo.Ctx.ID), "savepath": "/",
	} {
		mw.WriteField(k, val)
	}
	mw.Close()
	rq, _ := http.NewRequest("POST", s.base+"/repository/repository_ajax.php?action=upload", &buf)
	rq.Header.Set("Content-Type", mw.FormDataContentType())
	rs, err := s.hc.Do(rq)
	if err != nil {
		return err
	}
	defer rs.Body.Close()
	b, _ := io.ReadAll(rs.Body)
	var r struct {
		Error string `json:"error"`
		Event string `json:"event"`
		URL   string `json:"url"`
	}
	json.Unmarshal(b, &r)
	if r.Error != "" {
		return errors.New(strip(r.Error))
	}
	if r.Event == "fileexists" {
		return errors.New("файл с таким именем уже есть в черновике")
	}
	if r.URL == "" && r.Event == "" {
		return fmt.Errorf("неожиданный ответ загрузки: %s", trunc(string(b), 200))
	}
	return nil
}

func first(v ...string) string {
	for _, x := range v {
		if x != "" {
			return x
		}
	}
	return ""
}
