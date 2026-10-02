package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake Moodle serving testdata/*.html (markup modeled on online-edu.mirea.ru)
type fakeMoodle struct {
	mu      sync.Mutex
	base    string
	deleted []string
	uploads []string
	saved   map[string]string
}

func (f *fakeMoodle) page(w http.ResponseWriter, name string) {
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	now := time.Now().Unix()
	s := strings.NewReplacer("{{BASE}}", f.base,
		"{{NOWM10}}", fmt.Sprint(now-600), "{{NOWP80}}", fmt.Sprint(now+80*60)).Replace(string(b))
	io.WriteString(w, s)
}

func (f *fakeMoodle) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	switch {
	case r.URL.Path == "/mod/assign/view.php" && r.Method == "GET" && q.Get("action") == "editsubmission":
		f.page(w, "edit.html")
	case r.URL.Path == "/mod/assign/view.php" && r.Method == "GET":
		f.page(w, "assign.html")
	case r.URL.Path == "/mod/assign/view.php" && r.Method == "POST":
		r.ParseForm()
		f.saved = map[string]string{}
		for k, v := range r.PostForm {
			f.saved[k] = v[0]
		}
		f.page(w, "assign.html")
	case r.URL.Path == "/repository/draftfiles_ajax.php":
		r.ParseForm()
		f.deleted = append(f.deleted, r.PostForm.Get("filename")+"@"+r.PostForm.Get("itemid"))
		io.WriteString(w, `{}`)
	case r.URL.Path == "/repository/repository_ajax.php":
		r.ParseMultipartForm(32 << 20)
		_, h, _ := r.FormFile("repo_upload_file")
		f.uploads = append(f.uploads, h.Filename+"@"+r.FormValue("itemid")+"/repo"+r.FormValue("repo_id"))
		io.WriteString(w, `{"url":"x","id":999,"file":"`+h.Filename+`"}`)
	case r.URL.Path == "/mod/quiz/view.php":
		f.page(w, "quiz.html")
	case r.URL.Path == "/mod/webinars/view.php" && q.Get("id") == "1":
		f.page(w, "web_new.html")
	case r.URL.Path == "/mod/webinars/view.php":
		f.page(w, "web_legacy.html")
	case strings.HasSuffix(r.URL.Path, "/pages/new/studentTable_new.php"):
		f.page(w, "web_new_table.html")
	case strings.HasSuffix(r.URL.Path, "/pages/legacy/studentTable.php"):
		f.page(w, "web_legacy_table.html")
	default:
		http.NotFound(w, r)
	}
}

func newFake(t *testing.T) (*fakeMoodle, *sess) {
	f := &fakeMoodle{}
	s := stubSess(t, f.handler)
	f.base = s.base
	dropCache("")
	return f, s
}

func TestParseAssign(t *testing.T) {
	_, s := newFake(t)
	v, err := s.activity(s.base+"/mod/assign/view.php?id=5", 5000)
	if err != nil {
		t.Fatal(err)
	}
	o := v.(obj)
	if o["title"] != "Практическая работа №1" || !strings.Contains(o["dates"].(string), "28 сентября 2026") ||
		!strings.Contains(o["task"].(string), "Основы Python") || o["can_edit"] != true {
		t.Fatalf("%v", o)
	}
	if tf := o["task_files"].([]obj); len(tf) != 1 || tf[0]["file"] != "task.pdf" {
		t.Fatalf("task_files: %v", tf)
	}
	st := o["my_submission"].(obj)
	if st["Состояние ответа на задание"] != "Отправлено для оценивания" || st["Максимальная оценка"] != "8,0" {
		t.Fatalf("status: %v", st)
	}
	if _, ok := st["Комментарии к ответу"]; ok {
		t.Fatal("comments row must be dropped")
	}
	if fs := st["Ответ в виде файла"].([]obj); fs[0]["file"] != "report.pdf" {
		t.Fatalf("files: %v", fs)
	}
}

func TestParseQuiz(t *testing.T) {
	_, s := newFake(t)
	q := s.quizInfoNow(cmRef{Course: "C", Name: "TK4", URL: s.base + "/mod/quiz/view.php?id=9"})
	if q.State != "open_retry" || q.CloseStr == "" || len(q.Info) != 3 || len(q.Attempts) != 1 ||
		!strings.Contains(q.Attempts[0], "Оценка / 10,00: 7,50") || !strings.Contains(q.Result, "7,50") {
		t.Fatalf("%+v", q)
	}
	if strings.Contains(q.Attempts[0], "Просмотр") {
		t.Fatal("review link must be dropped")
	}
}

func TestParseWebinars(t *testing.T) {
	_, s := newFake(t)
	nw, ok := s.webinarsOfNow(cmRef{Course: "C", Name: "Лекции", URL: s.base + "/mod/webinars/view.php?id=1"}, "ИКБО-50-23")
	if !ok || len(nw) != 2 {
		t.Fatalf("new table (group filter must drop ИНБО row): %+v", nw)
	}
	if nw[0].Join != "https://my.mts-link.ru/j/1/2" || nw[0].Record != "" {
		t.Fatalf("join link: %+v", nw[0])
	}
	if nw[1].Record == "" || nw[1].Join != "" || nw[1].Start != 1790175600 {
		t.Fatalf("record row: %+v", nw[1])
	}
	lg, ok := s.webinarsOfNow(cmRef{Course: "C", Name: "Консультация", URL: s.base + "/mod/webinars/view.php?id=2"}, "")
	if !ok || len(lg) != 1 || lg[0].Record == "" || time.Unix(lg[0].Start, 0).In(msk).Format("2006-01-02 15:04") != "2026-01-12 18:04" {
		t.Fatalf("legacy: %+v", lg)
	}
}

func TestSubmitFlow(t *testing.T) {
	f, s := newFake(t)
	dl := filepath.Join(home(), "Downloads")
	os.MkdirAll(dl, 0o755)
	a, b, c := filepath.Join(dl, "a.ipynb"), filepath.Join(dl, "a.pdf"), filepath.Join(dl, "x.txt")
	for _, p := range []string{a, b, c} {
		os.WriteFile(p, []byte("data "+p), 0o644)
	}
	// maxfiles = 2 in the form
	if _, err := s.submit(5, []string{a, b, c}, "", false, false); err == nil || !strings.Contains(err.Error(), "максимум 2") {
		t.Fatalf("maxfiles: %v", err)
	}
	// dry run: uploads to draft, does not delete old files, does not save
	if _, err := s.submit(5, []string{a}, "", false, true); err != nil {
		t.Fatal(err)
	}
	if len(f.uploads) != 1 || f.saved != nil || len(f.deleted) != 0 {
		t.Fatalf("dry run touched more than draft: up=%v saved=%v del=%v", f.uploads, f.saved, f.deleted)
	}
	f.uploads = nil
	res, err := s.submit(5, []string{a, b}, "", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(f.deleted, ",") != "old.pdf@999" {
		t.Fatalf("old draft files not cleared: %v", f.deleted)
	}
	if strings.Join(f.uploads, ",") != "a.ipynb@999/repo4,a.pdf@999/repo4" {
		t.Fatalf("uploads: %v", f.uploads)
	}
	if f.saved["action"] != "savesubmission" || f.saved["files_filemanager"] != "999" || f.saved["sesskey"] != "SK1" || f.saved["submitbutton"] == "" {
		t.Fatalf("saved form: %v", f.saved)
	}
	if r := res.(obj); r["saved"] != true || r["now"] == nil {
		t.Fatalf("result: %v", r)
	}
}
