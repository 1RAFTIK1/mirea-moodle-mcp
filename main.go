package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

var version = "dev" // set by -ldflags at release build

const usage = `mirea-moodle-mcp — MCP-сервер для online-edu.mirea.ru (Moodle РТУ МИРЭА)

  mirea-moodle-mcp setup              первая настройка: сессия + подключение к Claude Desktop
  mirea-moodle-mcp cookie <значение>  обновить сессию (значение cookie MoodleSession)
  mirea-moodle-mcp status             проверить, жива ли сессия
  mirea-moodle-mcp doctor             диагностика: сеть до online-edu, сессия, группа, папки, клиенты
  mirea-moodle-mcp dashboard          открыть дашборд с дедлайнами и курсами
  mirea-moodle-mcp today | week       план на сегодня / на 7 дней (лекции, сроки, тесты)
  mirea-moodle-mcp calendar           адрес подписки на календарь Moodle для Google/Apple/Яндекс
  mirea-moodle-mcp group ИКБО-50-23   задать группу (фильтр лекций)
  mirea-moodle-mcp clients            список поддерживаемых агентов/IDE и какие найдены
  mirea-moodle-mcp install <id|detected>  подключить к клиенту (claude-desktop, claude-code, cursor,
                                      vscode, cline, windsurf, gemini, codex, lmstudio, zed)
  mirea-moodle-mcp config <id>        фрагмент конфига для ручной вставки
  mirea-moodle-mcp roots [add|rm <папка>]  папки, из которых можно сдавать и куда скачивать
  mirea-moodle-mcp http [--addr 127.0.0.1:8787] [--allow-origin URL] [--no-auth] [--allow-submit]
                                      MCP по HTTP для ChatGPT, claude.ai и др. удалённых клиентов
  mirea-moodle-mcp version
  mirea-moodle-mcp                    без аргументов — режим MCP (так его запускает Claude)

Переменные окружения: MOODLE_URL, MOODLE_DOWNLOAD_DIR
`

func main() {
	s := newSess(baseURL())
	if len(os.Args) < 2 {
		go s.keepalive(10 * time.Minute)
		if err := newServer(allTools(s)).serve(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	switch os.Args[1] {
	case "setup":
		os.Exit(setup(s))
	case "cookie":
		v := ""
		if len(os.Args) > 2 {
			v = os.Args[2]
		} else {
			v = ask("Вставь значение MoodleSession: ")
		}
		name, err := s.setCookie(v)
		if err != nil {
			die(err)
		}
		fmt.Printf("✓ сессия сохранена%s\n", who(name))
	case "status":
		if err := ping(s); err != nil {
			die(err)
		}
		fmt.Println("✓ сессия жива, userid", s.uid)
	case "dashboard":
		p, err := buildDashboard(s)
		if err != nil {
			die(err)
		}
		fmt.Println("✓", p)
		openURL(p)
	case "group":
		c := loadCfg()
		if len(os.Args) > 2 {
			c.Group = normGroup(os.Args[2])
			if err := saveCfg(c); err != nil {
				die(err)
			}
		}
		fmt.Println("группа:", c.Group)
	case "install-claude":
		if err := cmdInstall("claude-desktop"); err != nil {
			die(err)
		}
	case "install":
		a := ""
		if len(os.Args) > 2 {
			a = os.Args[2]
		}
		if err := cmdInstall(a); err != nil {
			die(err)
		}
	case "doctor":
		os.Exit(doctor(s))
	case "roots":
		if err := cmdRoots(os.Args[2:]); err != nil {
			die(err)
		}
	case "clients":
		cmdClients()
	case "config":
		a := ""
		if len(os.Args) > 2 {
			a = os.Args[2]
		}
		if err := cmdConfig(a); err != nil {
			die(err)
		}
	case "http":
		addr, noAuth, origins := "127.0.0.1:8787", false, []string{}
		for i := 2; i < len(os.Args); i++ {
			switch os.Args[i] {
			case "--addr":
				i++
				if i < len(os.Args) {
					addr = os.Args[i]
				}
			case "--allow-origin":
				i++
				if i < len(os.Args) {
					origins = append(origins, os.Args[i])
				}
			case "--no-auth":
				noAuth = true
			case "--allow-submit":
				allowSubmit = true
			}
		}
		remoteMode = true
		go s.keepalive(10 * time.Minute)
		if err := serveHTTP(newServer(allTools(s)), addr, noAuth, origins); err != nil {
			die(err)
		}
	case "calendar":
		u, err := s.calendarURL()
		if err != nil {
			die(err)
		}
		h := calendarHowTo(u)
		fmt.Println("Адрес подписки на календарь Moodle (личный, никому не отправляй):")
		fmt.Println(" ", u)
		fmt.Println("  Apple:", h["webcal"])
		for _, l := range h["how"].([]string) {
			fmt.Println("  •", l)
		}
	case "today", "week":
		n := 1
		if os.Args[1] == "week" {
			n = 7
		}
		days, err := s.agenda(time.Now().In(msk), n)
		if err != nil {
			die(err)
		}
		printAgenda(days)
	case "version", "-v", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Print(usage)
		os.Exit(2)
	}
}

func ping(s *sess) error {
	return s.call("core_course_get_enrolled_courses_by_timeline_classification", obj{"classification": "inprogress", "limit": 1, "offset": 0, "sort": "fullname"}, nil)
}

func who(name string) string {
	if name == "" {
		return ""
	}
	return " — " + name
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "✗", err)
	os.Exit(1)
}

var stdin = bufio.NewReader(os.Stdin)

func ask(q string) string {
	fmt.Print(q)
	l, _ := stdin.ReadString('\n')
	return strings.TrimSpace(l)
}

func setup(s *sess) int {
	fmt.Print(`
=== Настройка mirea-moodle-mcp ===

Шаг 1. Сессия Moodle.
Сервер работает от твоего имени через сессию браузера — пароль ему не нужен.

  1) Открой https://online-edu.mirea.ru/my/ и войди как обычно (SSO МИРЭА).
  2) Открой инструменты разработчика:
       Chrome / Яндекс / Edge:  F12 (на Mac ⌥⌘I) → Application → Cookies → https://online-edu.mirea.ru
       Firefox:                 F12 → Хранилище → Куки → https://online-edu.mirea.ru
       Safari:                  ⌥⌘I → Хранилище → Cookies (сначала включи меню «Разработка» в настройках)
  3) Скопируй значение (Value) строки MoodleSession.

Никому это значение не отправляй — это доступ к твоему аккаунту, пока сессия жива.

`)
	openURL(s.base + "/my/")
	for i := 0; ; i++ {
		v := ask("MoodleSession: ")
		name, err := s.setCookie(v)
		if err == nil {
			fmt.Printf("✓ вошли%s\n\n", who(name))
			break
		}
		fmt.Println("✗", err)
		if i >= 2 {
			return 1
		}
	}

	if g := normGroup(ask("Твоя группа (например ИКБО-50-23, Enter — пропустить): ")); g != "" {
		c := loadCfg()
		c.Group = g
		saveCfg(c)
		fmt.Println("✓ группа", g)
	}

	fmt.Println("\nШаг 2. Подключение к агентам.")
	var found []string
	for _, c := range clients {
		if c.detect() {
			found = append(found, c.Name)
		}
	}
	if len(found) == 0 {
		fmt.Println("Не нашёл установленных клиентов MCP. Позже: mirea-moodle-mcp clients / install <id>")
	} else if a := strings.ToLower(ask("Подключить к: " + strings.Join(found, ", ") + "? [Y/n] ")); a == "" || a == "y" || a == "д" || a == "да" || a == "yes" {
		if err := cmdInstall("detected"); err != nil {
			fmt.Println("✗", err)
		}
	}

	fmt.Println("\nШаг 3. Дашборд.")
	if p, err := buildDashboard(s); err == nil {
		fmt.Println("✓", p)
		openURL(p)
	} else {
		fmt.Println("✗", err)
	}

	fmt.Print(`
Готово. Перезапусти подключённые приложения (Claude Desktop — полностью, через Quit) и спроси, например:
  «какие у меня дедлайны на этой неделе?»

Когда сессия истечёт (Claude скажет об этом), снова войди в браузере и выполни:
  mirea-moodle-mcp cookie <новое значение MoodleSession>
`)
	return 0
}

func openURL(u string) {
	var c *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		c = exec.Command("open", u)
	case "windows":
		c = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		c = exec.Command("xdg-open", u)
	}
	c.Start()
}

func allTools(s *sess) []*tool {
	return append(append(tools(s), scheduleTools(s)...), extraTools(s)...)
}

func printAgenda(days []obj) {
	for _, d := range days {
		fmt.Println(d["date"])
		items := d["items"].([]obj)
		if len(items) == 0 {
			fmt.Println("  —")
		}
		for _, it := range items {
			c := ""
			if v, ok := it["course"].(string); ok {
				c = " · " + v
			}
			fmt.Printf("  %-11s %-17s %s%s\n", it["time"], it["kind"], it["name"], c)
		}
	}
}
