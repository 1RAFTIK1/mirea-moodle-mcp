package main

import (
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const defBase = "https://online-edu.mirea.ru"

func baseURL() string {
	if b := strings.TrimRight(os.Getenv("MOODLE_URL"), "/"); b != "" {
		return b
	}
	return defBase
}

func cfgDir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "mirea-moodle-mcp")
}

var (
	reBlock = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li|/h\d|/tr)[^>]*>`)
	reLi    = regexp.MustCompile(`(?i)<\s*li[^>]*>`)
	reDrop  = regexp.MustCompile(`(?is)<(script|style)[^>]*>.*?</(script|style)>`)
	reTag   = regexp.MustCompile(`(?s)<[^>]*>`)
	reSp    = regexp.MustCompile(`[ \t\r\f\v]+`)
	reNl    = regexp.MustCompile(`\n\s*\n+`)
)

// strip turns HTML into readable plain text.
func strip(s string) string {
	if s == "" {
		return ""
	}
	s = reDrop.ReplaceAllString(s, "")
	s = reBlock.ReplaceAllString(s, "\n")
	s = reLi.ReplaceAllString(s, "\n• ")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, " ", " ")
	s = reSp.ReplaceAllString(s, " ")
	s = reNl.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

var msk = func() *time.Location {
	l, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return time.FixedZone("MSK", 3*3600)
	}
	return l
}()

func ft(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return time.Unix(ts, 0).In(msk).Format("2006-01-02 15:04 Mon")
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, p[2:])
	}
	return p
}
